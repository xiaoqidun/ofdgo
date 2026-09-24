// Copyright 2025-2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ofdgo

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// editorFontUsage 汇总原文档字体的字符、显式字形及无法确定的引用
type editorFontUsage struct {
	chars  map[rune]bool
	glyphs map[uint16]bool
	unsafe bool
}

// editorFontSubset 保存单个新增字体最近一次裁剪结果，不保留历史字形集合
type editorFontSubset struct {
	glyphs []uint16
	data   []byte
	mapped bool
}

// AddFont 注册OpenType字体，集合字体按索引提取，重复资源复用标识，引用后写入文档
// 入参: file 字体文件, index 集合内字体索引，单字体为0
// 返回: string 字体资源标识, error 错误信息
func (e *Editor) AddFont(file FontFile, index int) (string, error) {
	if e.backends.FontResources == nil {
		return "", fmt.Errorf("font resources: %w", ErrBackendUnavailable)
	}
	parsed, err := e.backends.FontResources.OpenFontResource(file, index)
	if err != nil {
		return "", err
	}
	data := parsed.Data
	key := editorResourceKey{checksum: sha256.Sum256(data)}
	if id, ok := e.resourceID[key]; ok {
		return id, nil
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	id := e.nextID()
	definition := parsed.Font
	definition.ID = id
	resource := editorResource{
		name: e.packageName("Res/Fonts/Font_" + id + parsed.Extension),
		data: data,
		font: &definition,
	}
	resource.font.FontFile = "/" + resource.name
	e.resources = append(e.resources, resource)
	e.fonts[id] = parsed
	e.resourceID[key] = id
	return id, nil
}

// newEditorFontUsage 创建字体用字记录
// 返回: *editorFontUsage 用字记录
func newEditorFontUsage() *editorFontUsage {
	return &editorFontUsage{chars: make(map[rune]bool), glyphs: make(map[uint16]bool)}
}

// fontUsage 获取规范化资源标识对应的字体用字记录
// 入参: id 字体资源标识
// 返回: *editorFontUsage 用字记录
func (r *editorResourceRefs) fontUsage(id string) *editorFontUsage {
	if r.fonts == nil {
		r.fonts = make(map[string]*editorFontUsage)
	}
	id = editorResourceID(id)
	if r.fonts[id] == nil {
		r.fonts[id] = newEditorFontUsage()
	}
	return r.fonts[id]
}

// text 收集TextCode字符和CGTransform显式字形，异常编号使该字体保持原样
// 入参: key 元素名称, value 元素文本
func (u *editorFontUsage) text(key, value string) {
	switch key {
	case "TextCode":
		for _, char := range textCodeRunes(value) {
			u.chars[char] = true
		}
	case "Glyphs":
		for _, value := range strings.Fields(strings.ReplaceAll(value, ",", " ")) {
			id, err := strconv.ParseUint(value, 10, 16)
			if err != nil {
				u.unsafe = true
				return
			}
			u.glyphs[uint16(id)] = true
		}
	}
}

// merge 合并指向同一字体文件的全部资源用字
// 入参: other 用字记录, unsafe 是否存在无法确定的引用
func (u *editorFontUsage) merge(other *editorFontUsage, unsafe bool) {
	u.unsafe = u.unsafe || unsafe
	if other != nil {
		u.unsafe = u.unsafe || other.unsafe
		maps.Copy(u.chars, other.chars)
		maps.Copy(u.glyphs, other.glyphs)
	}
}

// subsetFonts 收集新增字体实际使用的字形，仅裁剪保存结果，保留完整编辑字体
// 入参: progress 保存进度回调
// 返回: map[string][]byte 包内字体子集, error 错误信息
func (e *Editor) subsetFonts(progress editorProgress) (map[string][]byte, error) {
	if e.backends.FontResources == nil {
		return nil, nil
	}
	used := make(map[string]map[uint16]bool)
	mapped := make(map[string]bool)
	for _, resource := range e.resources {
		if resource.font == nil {
			continue
		}
		resourceFont := e.fonts[resource.font.ID]
		if resourceFont == nil {
			var err error
			resourceFont, err = e.backends.FontResources.OpenFontResource(FontFile{Data: resource.data}, 0)
			if err != nil {
				return nil, err
			}
			e.fonts[resource.font.ID] = resourceFont
		}
		if resourceFont.CanSubset {
			used[resource.font.ID] = make(map[uint16]bool)
		}
	}
	if len(used) == 0 {
		return nil, progress.report("fonts", 0, 0)
	}
	refs := editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool)}
	composite := false
	if e.source != nil {
		for _, name := range e.annotationFiles() {
			if data, changed := e.source.reader.files[name]; changed {
				composite = true
				if _, safe := refs.scan(bytes.NewReader(data), name); !safe {
					return nil, nil
				}
			}
		}
	}
	total := len(e.pages) + len(e.resources)
	for i, page := range e.pages {
		if err := progress.report("fonts", i, total); err != nil {
			return nil, err
		}
		for _, layer := range page.Content.Layer {
			for _, object := range layer.Objects {
				if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
					composite = true
					origin := e.objectOrigin(editorObjectID(object))
					if origin == nil {
						return nil, nil
					}
					data, err := editorXMLObject(origin.data, origin.node, origin.object, object)
					if err == nil {
						data, err = editorXMLStandalone(data, origin.node)
					}
					if err != nil {
						return nil, err
					}
					if _, safe := refs.scan(bytes.NewReader(data), "Page.xml"); !safe {
						return nil, nil
					}
				}
				if object.Type != "TextObject" {
					continue
				}
				obj := object.TextObject
				glyphs := used[obj.Font]
				if glyphs == nil {
					continue
				}
				for _, code := range obj.TextCode {
					for _, char := range textCodeRunes(code.Value) {
						glyphs[e.fonts[obj.Font].GlyphIndex(char)] = true
					}
				}
				for _, transform := range obj.CGTransform {
					mapped[obj.Font] = true
					for _, value := range strings.Fields(transform.Glyphs) {
						id, err := strconv.ParseUint(value, 10, 16)
						if err != nil || id >= uint64(e.fonts[obj.Font].NumGlyphs()) {
							delete(used, obj.Font)
							break
						}
						glyphs[uint16(id)] = true
					}
				}
			}
		}
	}
	if composite {
		_, _, _, files := e.usedResources()
		for i, resource := range e.resources {
			if resource.composite == "" || !slices.Contains(files, resource.name) {
				continue
			}
			if err := progress.report("fonts", len(e.pages)+i, total); err != nil {
				return nil, err
			}
			if _, safe := refs.scan(bytes.NewReader(resource.data), resource.name); !safe {
				return nil, nil
			}
		}
		for id, usage := range refs.fonts {
			glyphs := used[id]
			if usage.unsafe || len(usage.glyphs) != 0 {
				delete(used, id)
			} else if glyphs != nil {
				for char := range usage.chars {
					glyphs[e.fonts[id].GlyphIndex(char)] = true
				}
			}
		}
	}
	result := make(map[string][]byte)
	for i := range e.resources {
		if err := progress.report("fonts", len(e.pages)+i, total); err != nil {
			return nil, err
		}
		resource := &e.resources[i]
		if resource.font == nil {
			continue
		}
		glyphs := used[resource.font.ID]
		if len(glyphs) == 0 {
			resource.subset = nil
			continue
		}
		glyphs[0] = true
		ids := make([]uint16, 0, len(glyphs))
		for id := range glyphs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		if resource.subset == nil || resource.subset.mapped != mapped[resource.font.ID] || !slices.Equal(resource.subset.glyphs, ids) {
			data, err := e.backends.FontResources.SubsetFont(resource.data, ids, mapped[resource.font.ID])
			if err != nil {
				return nil, fmt.Errorf("subset font %s: %w", resource.font.FontName, err)
			}
			if len(data) >= len(resource.data) {
				data = nil
			}
			resource.subset = &editorFontSubset{glyphs: ids, data: data, mapped: mapped[resource.font.ID]}
		}
		if resource.subset.data != nil {
			result[resource.name] = resource.subset.data
		}
	}
	if err := progress.report("fonts", total, total); err != nil {
		return nil, err
	}
	return result, nil
}
