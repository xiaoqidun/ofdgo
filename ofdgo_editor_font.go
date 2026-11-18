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
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// AddFont 注册OpenType字体，集合字体按索引提取，重复资源复用标识，引用后写入文档
// 入参: file 字体文件, index 集合内字体索引，单字体为0
// 返回: string 字体资源标识, error 错误信息
func (e *Editor) AddFont(file FontFile, index int) (string, error) {
	if e.backends.FontResources == nil {
		return "", fmt.Errorf("font resources: %w", ErrBackendUnavailable)
	}
	checksum := sha256.Sum256(file.Data)
	previous := e.fontRegistration
	if previous.id != "" && previous.checksum == checksum && previous.name == file.Name && previous.index == index {
		return previous.id, nil
	}
	e.fontRegistration = editorFontRegistration{}
	parsed, err := e.backends.FontResources.OpenFontResource(file, index)
	if err != nil {
		return "", err
	}
	id, err := e.addFontResource(parsed, sha256.Sum256(parsed.Data))
	if err == nil {
		e.fontRegistration = editorFontRegistration{checksum: checksum, name: file.Name, index: index, id: id}
	}
	return id, err
}

// AddExternalFont 注册不含字体文件的名称引用，重复注册复用标识，由阅读环境匹配
// 入参: name 字体名称
// 返回: string 字体资源标识, error 错误信息
func (e *Editor) AddExternalFont(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty external font name")
	}
	key := editorResourceKey{checksum: sha256.Sum256([]byte(name)), kind: "Font/external"}
	if id := e.resourceID[key]; id != "" {
		return id, nil
	}
	for _, resource := range e.resources {
		if resource.font != nil && resource.font.FontFile == "" && resource.font.FontName == name {
			e.resourceID[key] = resource.font.ID
			return resource.font.ID, nil
		}
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	id := e.nextID()
	e.resources = append(e.resources, editorResource{font: &Font{ID: id, FontName: name, FamilyName: name}})
	e.resourceID[key] = id
	e.fontRenderer = nil
	return id, nil
}

// editorFontUsage 汇总原文档字体的字符、显式字形及无法确定的引用
type editorFontUsage struct {
	chars  map[rune]bool
	glyphs map[uint16]bool
	unsafe bool
}

// newEditorFontUsage 创建字体用字记录
// 返回: *editorFontUsage 用字记录
func newEditorFontUsage() *editorFontUsage {
	return &editorFontUsage{chars: make(map[rune]bool), glyphs: make(map[uint16]bool)}
}

// text 收集TextCode字符和CGTransform显式字形，异常编号使该字体保持原样
// 入参: ctx 取消上下文，可为nil, key 元素名称, value 元素文本
// 返回: error 取消错误
func (u *editorFontUsage) text(ctx context.Context, key, value string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	switch key {
	case "TextCode":
		chars, err := textCodeRunesContext(ctx, value)
		if err != nil {
			return err
		}
		for index, char := range chars {
			if ctx != nil && index&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			u.chars[char] = true
		}
	case "Glyphs":
		count := 0
		for value := range strings.FieldsFuncSeq(value, func(char rune) bool { return char == ',' || unicode.IsSpace(char) }) {
			if ctx != nil && count&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			id, err := strconv.ParseUint(value, 10, 16)
			if err != nil {
				u.unsafe = true
				return nil
			}
			u.glyphs[uint16(id)] = true
			count++
		}
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
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

// editorFontSubset 保存单个新增字体及其快照最近一次裁剪结果，不保留历史字形集合
type editorFontSubset struct {
	source [32]byte
	glyphs []uint16
	data   []byte
	mapped bool
}

// editorFontRegistration 记录最近一次成功注册的输入与标识，不保留原始文件
type editorFontRegistration struct {
	checksum [32]byte
	name     string
	index    int
	id       string
}

// editorSourceFontKey 区分原字体内容及完整用字集合
type editorSourceFontKey struct {
	source [32]byte
	usage  [32]byte
}

// editorSourceFontCache 在单次资源处理中复用裁剪结果，空值表示无体积收益
type editorSourceFontCache map[editorSourceFontKey][]byte

// subset 按原字体和用字复用安全子集，不缓存错误、取消结果或原始字体
// 入参: ctx 取消上下文, backend 字体资源后端, data 原字体, usage 用字记录
// 返回: []byte 更小的字体子集，无收益时为nil, error 裁剪或取消错误
func (c *editorSourceFontCache) subset(ctx context.Context, backend FontResourceBackend, data []byte, usage *editorFontUsage) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if usage == nil || usage.unsafe || backend == nil {
		return nil, nil
	}
	characters, glyphs := slices.Sorted(maps.Keys(usage.chars)), slices.Sorted(maps.Keys(usage.glyphs))
	encoded := make([]byte, 0, 8+4*len(characters)+2*len(glyphs))
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(characters)))
	for _, char := range characters {
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(char))
	}
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(glyphs)))
	for _, glyph := range glyphs {
		encoded = binary.LittleEndian.AppendUint16(encoded, glyph)
	}
	key := editorSourceFontKey{source: sha256.Sum256(data), usage: sha256.Sum256(encoded)}
	if result, ok := (*c)[key]; ok {
		return result, ctx.Err()
	}
	result, err := backend.SubsetSourceFont(data, FontUsage{Characters: characters, Glyphs: glyphs})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 || len(result) >= len(data) {
		result = nil
	}
	if *c == nil {
		*c = make(editorSourceFontCache)
	}
	(*c)[key] = result
	return result, nil
}

// clearFontResourceCache 清除字体后端生成的解析与裁剪缓存，保留原始资源
func (e *Editor) clearFontResourceCache() {
	e.fonts = make(map[string]*FontResource)
	e.fontRegistration = editorFontRegistration{}
	for i := range e.resources {
		e.resources[i].subset = nil
	}
}

// addFontResource 注册已解析的只读字体，各文档独立分配标识和包内路径
// 入参: parsed 字体资源, checksum 字体数据摘要
// 返回: string 字体标识, error 标识准备错误
func (e *Editor) addFontResource(parsed *FontResource, checksum [32]byte) (string, error) {
	key := editorResourceKey{checksum: checksum}
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
		data: parsed.Data,
		font: &definition,
	}
	resource.font.FontFile = "/" + resource.name
	e.resources = append(e.resources, resource)
	e.fonts[id] = parsed
	e.resourceID[key] = id
	return id, nil
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

// editorObjectFontScan 判断对象是否需要扫描嵌套文字或复合资源
// 入参: object 图形对象
// 返回: bool 是否需要完整引用扫描
func editorObjectFontScan(object GraphicObject) bool {
	var clips *Clips
	var fill, stroke *FillColor
	switch object.Type {
	case "CompositeObject", "CompositeGraphicUnit":
		return true
	case "TextObject", "Text":
		clips = object.TextObject.Clips
		fill, stroke = object.TextObject.FillColor, (*FillColor)(object.TextObject.StrokeColor)
	case "PathObject", "Path":
		clips = object.PathObject.Clips
		fill, stroke = object.PathObject.FillColor, (*FillColor)(object.PathObject.StrokeColor)
	case "ImageObject":
		clips = object.ImageObject.Clips
		if object.ImageObject.Border != nil {
			stroke = (*FillColor)(object.ImageObject.Border.BorderColor)
		}
	}
	return clips != nil || fill != nil && fill.Pattern != nil || stroke != nil && stroke.Pattern != nil
}

// subsetFonts 仅解析实际引用的新增字体，合并用字后裁剪保存结果，保留完整编辑字体
// 入参: progress 保存进度回调
// 返回: map[string][]byte 包内字体子集, error 错误信息
func (e *Editor) subsetFonts(progress editorProgress) (map[string][]byte, error) {
	result, err := e.subsetNewFonts(progress)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	source, err := e.subsetSnapshotFonts(ctx, progress.withContext(ctx))
	if err != nil {
		return nil, err
	}
	if result == nil {
		return source, nil
	}
	maps.Copy(result, source)
	return result, nil
}

// subsetSnapshotFonts 在写出时扫描快照中新增字体的完整包引用，不改动既有字体
// 入参: ctx 取消上下文, progress 写出进度
// 返回: map[string][]byte 新增字体的安全子集, error 扫描或裁剪错误
func (e *Editor) subsetSnapshotFonts(ctx context.Context, progress editorProgress) (map[string][]byte, error) {
	if e.source == nil || e.backends.FontResources == nil {
		return nil, ctx.Err()
	}
	candidates := make(map[string]int)
	for i, resource := range e.resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resource.font != nil && resource.font.FontFile != "" && e.source.reader.fileNames[resource.name] {
			candidates[resource.name] = i
		}
	}
	if len(candidates) == 0 {
		return nil, ctx.Err()
	}
	view, err := e.readerContext(ctx, progress)
	if err != nil {
		return nil, err
	}
	defer view.Close()
	refs, resources, references, safe, err := scanEditorResources(ctx, view, view.files, nil, maps.Clone(view.fileNames), progress, true)
	if err != nil || !safe {
		return nil, err
	}
	scoped, err := resourceReferenceScopes(ctx, refs.documents, resources, references, "")
	if err != nil || scoped == nil {
		return nil, err
	}
	read := func(name string) ([]byte, error) { return view.readFileViewContext(ctx, name) }
	usage, err := resourceFontUsage(ctx, resources, scoped, read)
	if err != nil {
		return nil, err
	}
	for _, source := range references {
		for name := range source.files {
			if font := usage[name]; font != nil && !source.versions[name] {
				font.unsafe = true
			}
		}
		for _, definition := range source.definitions {
			for name := range definition.files {
				if font := usage[name]; font != nil {
					font.unsafe = true
				}
			}
		}
	}
	result := make(map[string][]byte)
	for i, name := range slices.Sorted(maps.Keys(candidates)) {
		if err := progress.report("fonts", i, len(candidates)); err != nil {
			return nil, err
		}
		font := usage[name]
		if font == nil || font.unsafe || len(font.chars)+len(font.glyphs) == 0 {
			continue
		}
		data, err := read(name)
		if err != nil {
			return nil, err
		}
		parsed, err := e.backends.FontResources.OpenFontResource(FontFile{Data: data}, 0)
		if err != nil {
			return nil, err
		}
		if !parsed.CanSubset {
			continue
		}
		preserve := len(font.glyphs) != 0
		glyphs := maps.Clone(font.glyphs)
		for char := range font.chars {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			glyphs[parsed.GlyphIndex(char)] = true
		}
		glyphs[0] = true
		for id := range glyphs {
			if id >= parsed.NumGlyphs() {
				font.unsafe = true
				break
			}
		}
		if font.unsafe {
			continue
		}
		ids := slices.Sorted(maps.Keys(glyphs))
		checksum := sha256.Sum256(data)
		resource := &e.resources[candidates[name]]
		if resource.subset == nil || resource.subset.source != checksum || resource.subset.mapped != preserve || !slices.Equal(resource.subset.glyphs, ids) {
			subset, err := e.backends.FontResources.SubsetFont(data, ids, preserve)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(subset) >= len(data) {
				subset = nil
			}
			resource.subset = &editorFontSubset{source: checksum, glyphs: ids, data: subset, mapped: preserve}
		}
		if len(resource.subset.data) != 0 {
			result[name] = resource.subset.data
		}
	}
	return result, progress.report("fonts", len(candidates), len(candidates))
}

// subsetNewFonts 解析尚未物化为源资源的新增字体，保持编辑字体完整
// 入参: progress 写出进度
// 返回: map[string][]byte 包内字体子集, error 读取或裁剪错误
func (e *Editor) subsetNewFonts(progress editorProgress) (map[string][]byte, error) {
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	progress = progress.withContext(ctx)
	if e.backends.FontResources == nil {
		return nil, nil
	}
	if err := progress.report("fonts", 0, 0); err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(e.resources, func(resource editorResource) bool {
		return resource.font != nil && resource.font.FontFile != "" && (e.source == nil || !e.source.reader.fileNames[resource.name])
	}) {
		return nil, nil
	}
	fonts, _, _, files, err := e.usedResources(ctx)
	if err != nil {
		return nil, err
	}
	total := len(fonts) + len(e.pages) + 2*len(e.resources)
	used := make(map[string]*editorFontUsage)
	mapped := make(map[string]bool)
	for i, resource := range fonts {
		if err := progress.report("fonts", i, total); err != nil {
			return nil, err
		}
		if resource.font.FontFile == "" || e.source != nil && e.source.reader.fileNames[resource.name] {
			continue
		}
		resourceFont := e.fonts[resource.font.ID]
		if resourceFont == nil {
			var err error
			resourceFont, err = e.backends.FontResources.OpenFontResource(FontFile{Data: resource.data}, 0)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			e.fonts[resource.font.ID] = resourceFont
		}
		if resourceFont.CanSubset {
			used[resource.font.ID] = newEditorFontUsage()
		}
	}
	if len(used) == 0 {
		for i := range e.resources {
			e.resources[i].subset = nil
		}
		return nil, progress.report("fonts", total, total)
	}
	refs := editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool)}
	scan := func(data []byte, name string) (bool, error) {
		_, safe := refs.scanContext(ctx, bytes.NewReader(data), name)
		return safe, ctx.Err()
	}
	nested := false
	for _, resource := range e.resources {
		if resource.draw != nil {
			nested = true
			break
		}
	}
	if e.source != nil {
		for _, name := range e.annotationFiles() {
			if data, changed := e.source.reader.files[name]; changed {
				nested = true
				if safe, err := scan(data, name); err != nil || !safe {
					return nil, err
				}
			}
		}
	}
	for i, page := range e.pages {
		if err := progress.report("fonts", len(fonts)+i, total); err != nil {
			return nil, err
		}
		for _, layer := range page.Content.Layer {
			for _, object := range layer.Objects {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if editorObjectFontScan(object) {
					nested = true
					origin := e.objectOrigin(editorObjectID(object))
					var data []byte
					var err error
					if origin == nil {
						data, err = e.encodeXML(func(x *ofdXML) { x.object(object, true) })
					} else {
						data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
						if err == nil {
							data, err = editorXMLStandalone(data, origin.node)
						}
					}
					if err != nil {
						return nil, err
					}
					if safe, err := scan(data, "Page.xml"); err != nil || !safe {
						return nil, err
					}
					continue
				}
				if object.Type != "TextObject" {
					continue
				}
				obj := object.TextObject
				usage := used[obj.Font]
				if usage == nil {
					continue
				}
				for _, code := range obj.TextCode {
					if err := usage.text(ctx, "TextCode", code.Value); err != nil {
						return nil, err
					}
				}
				for _, transform := range obj.CGTransform {
					mapped[obj.Font] = true
					count := 0
					for value := range strings.FieldsSeq(transform.Glyphs) {
						if count&4095 == 0 {
							if err := ctx.Err(); err != nil {
								return nil, err
							}
						}
						id, err := strconv.ParseUint(value, 10, 16)
						if err != nil || id >= uint64(e.fonts[obj.Font].NumGlyphs()) {
							delete(used, obj.Font)
							break
						}
						usage.glyphs[uint16(id)] = true
						count++
					}
				}
			}
		}
	}
	if nested {
		for i, resource := range e.resources {
			if resource.definition() == "" || !files[resource.name] {
				continue
			}
			if err := progress.report("fonts", len(fonts)+len(e.pages)+i, total); err != nil {
				return nil, err
			}
			if resource.usage.matches(resource.name, resource.data) {
				refs.merge(resource.usage.refs)
			} else if safe, err := scan(resource.data, resource.name); err != nil || !safe {
				return nil, err
			}
		}
		for id, usage := range refs.fonts {
			collected := used[id]
			if usage.unsafe {
				delete(used, id)
			} else if collected != nil {
				collected.merge(usage, false)
				if len(usage.glyphs) != 0 {
					mapped[id] = true
					for glyph := range usage.glyphs {
						if glyph >= e.fonts[id].NumGlyphs() {
							delete(used, id)
							break
						}
					}
				}
			}
		}
	}
	result := make(map[string][]byte)
	for i := range e.resources {
		if err := progress.report("fonts", len(fonts)+len(e.pages)+len(e.resources)+i, total); err != nil {
			return nil, err
		}
		resource := &e.resources[i]
		if resource.font == nil || e.source != nil && e.source.reader.fileNames[resource.name] {
			continue
		}
		usage := used[resource.font.ID]
		if usage == nil || len(usage.chars) == 0 && len(usage.glyphs) == 0 {
			resource.subset = nil
			continue
		}
		glyphs := usage.glyphs
		for char := range usage.chars {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			glyphs[e.fonts[resource.font.ID].GlyphIndex(char)] = true
		}
		glyphs[0] = true
		ids := make([]uint16, 0, len(glyphs))
		for id := range glyphs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resource.subset == nil || resource.subset.mapped != mapped[resource.font.ID] || !slices.Equal(resource.subset.glyphs, ids) {
			data, err := e.backends.FontResources.SubsetFont(resource.data, ids, mapped[resource.font.ID])
			if err != nil {
				return nil, fmt.Errorf("subset font %s: %w", resource.font.FontName, err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
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
