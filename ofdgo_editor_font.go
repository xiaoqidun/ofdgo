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

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
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

// index 解析TextCode的Index属性字形编号与区间，异常或超限时标记该字体不可裁剪
// 入参: ctx 取消上下文(可为nil), value Index属性值
// 返回: error 取消错误
func (u *editorFontUsage) index(ctx context.Context, value string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	total := 0
	for part := range strings.FieldsSeq(value) {
		if ctx != nil && total&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if startText, endText, ok := strings.Cut(part, "-"); ok {
			if strings.Contains(endText, "-") {
				u.unsafe = true
				return nil
			}
			start, err1 := strconv.ParseUint(startText, 10, 16)
			end, err2 := strconv.ParseUint(endText, 10, 16)
			if err1 != nil || err2 != nil || start > end {
				u.unsafe = true
				return nil
			}
			total += int(end - start + 1)
			if total > maxIndexRunes {
				u.unsafe = true
				return nil
			}
			for k := start; k <= end; k++ {
				if ctx != nil && k&4095 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				u.glyphs[uint16(k)] = true
			}
		} else {
			val, err := strconv.ParseUint(part, 10, 16)
			if err != nil {
				u.unsafe = true
				return nil
			}
			total++
			if total > maxIndexRunes {
				u.unsafe = true
				return nil
			}
			u.glyphs[uint16(val)] = true
		}
	}
	if total == 0 {
		u.unsafe = true
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
	chars  [32]byte
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
			if resource.font.Charset != "" && !strings.EqualFold(resource.font.Charset, "unicode") {
				continue
			}
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
		if bytes.HasPrefix(data, []byte("ttcf")) {
			continue
		}
		resource := &e.resources[candidates[name]]
		parsed, err := e.backends.FontResources.OpenFontResource(FontFile{Data: data}, 0)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if e.fonts[resource.font.ID] == nil {
				continue
			}
			return nil, err
		}
		if !parsed.CanSubset {
			continue
		}
		subset, err := e.subsetFontResource(ctx, resource, data, parsed, font, len(font.glyphs) != 0)
		if err != nil {
			return nil, err
		}
		if subset != nil {
			result[name] = subset
		}
	}
	return result, progress.report("fonts", len(candidates), len(candidates))
}

// subsetFontResource 按用字记录复用或生成字体子集缓存，无体积收益或存在无效字形时返回nil
// 入参: ctx 取消上下文, resource 字体资源条目, data 字体原始字节, parsed 已解析字体资源, usage 用字记录, preserve 是否保留原字形编号
// 返回: []byte 字体子集数据, error 裁剪或取消错误
func (e *Editor) subsetFontResource(ctx context.Context, resource *editorResource, data []byte, parsed *FontResource, usage *editorFontUsage, preserve bool) ([]byte, error) {
	glyphs, ok := expandEditorFontGlyphs(data, usage.glyphs, parsed.NumGlyphs())
	if !ok {
		resource.subset = nil
		return nil, nil
	}
	var explicit []uint16
	if preserve {
		glyphs[0] = true
		explicit = slices.Sorted(maps.Keys(glyphs))
	}
	for char := range usage.chars {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		glyphs[parsed.GlyphIndex(char)] = true
	}
	glyphs[0] = true
	ids := slices.Sorted(maps.Keys(glyphs))
	chars := slices.Sorted(maps.Keys(usage.chars))
	charsDigest := editorFontCharsDigest(chars)
	checksum := sha256.Sum256(data)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resource.subset == nil || resource.subset.source != checksum || resource.subset.chars != charsDigest || resource.subset.mapped != preserve || !slices.Equal(resource.subset.glyphs, ids) {
		subset, err := subsetEditorFontData(ctx, e.backends.FontResources, data, chars, explicit, ids, preserve)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(subset) >= len(data) {
			subset = nil
		}
		resource.subset = &editorFontSubset{source: checksum, chars: charsDigest, glyphs: ids, data: subset, mapped: preserve}
	}
	if len(resource.subset.data) == 0 {
		return nil, nil
	}
	return resource.subset.data, nil
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
		return resource.font != nil && resource.font.FontFile != "" && resource.name != "" && len(resource.data) > 0 && (e.source == nil || !e.source.reader.fileNames[resource.name])
	}) {
		return nil, nil
	}
	fonts, _, _, files, _, err := e.usedResources(ctx)
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
		if resource.font.FontFile == "" || resource.name == "" || len(resource.data) == 0 || e.source != nil && e.source.reader.fileNames[resource.name] {
			continue
		}
		if resource.font.Charset != "" && !strings.EqualFold(resource.font.Charset, "unicode") {
			continue
		}
		if bytes.HasPrefix(resource.data, []byte("ttcf")) {
			continue
		}
		resourceFont := e.fonts[resource.font.ID]
		if resourceFont == nil {
			var err error
			resourceFont, err = e.backends.FontResources.OpenFontResource(FontFile{Data: resource.data}, 0)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				continue
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
					origin := e.objectOrigin(editorObjectID(object))
					if origin == nil {
						hasResourceRef, collectErr := e.collectMemoryObjectFontUsage(ctx, object, used, mapped)
						if collectErr != nil {
							return nil, collectErr
						}
						if hasResourceRef {
							nested = true
						}
						continue
					}
					nested = true
					data, err := editorXMLObject(origin.data, origin.node, origin.object, object)
					if err == nil {
						data, err = editorXMLStandalone(data, origin.node)
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
				if err := e.collectTextFontUsage(ctx, object.TextObject, used, mapped); err != nil {
					return nil, err
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
		if resource.font == nil || resource.name == "" || len(resource.data) == 0 || e.source != nil && e.source.reader.fileNames[resource.name] {
			continue
		}
		usage := used[resource.font.ID]
		if usage == nil || len(usage.chars) == 0 && len(usage.glyphs) == 0 {
			resource.subset = nil
			continue
		}
		subset, err := e.subsetFontResource(ctx, resource, resource.data, e.fonts[resource.font.ID], usage, mapped[resource.font.ID])
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			return nil, fmt.Errorf("subset font %s: %w", resource.font.FontName, err)
		}
		if subset != nil {
			result[resource.name] = subset
		}
	}
	if err := progress.report("fonts", total, total); err != nil {
		return nil, err
	}
	return result, nil
}

// collectMemoryObjectFontUsage 直接递归遍历无外部原文的自产图元树收集字体用字，消除逐图元XML序列化与解析
// 入参: ctx 取消上下文, object 图形对象, used 新增字体用字表, mapped 显式字形映射标记
// 返回: bool 是否包含外部复合资源引用, error 取消错误
func (e *Editor) collectMemoryObjectFontUsage(ctx context.Context, object GraphicObject, used map[string]*editorFontUsage, mapped map[string]bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	hasResourceRef := false
	var collectText func(TextObject) error
	var collectClips func(*Clips) error
	var collectColor func(*FillColor) error
	collectText = func(obj TextObject) error {
		if err := collectClips(obj.Clips); err != nil {
			return err
		}
		if err := collectColor(obj.FillColor); err != nil {
			return err
		}
		if err := collectColor((*FillColor)(obj.StrokeColor)); err != nil {
			return err
		}
		return e.collectTextFontUsage(ctx, obj, used, mapped)
	}
	collectClips = func(clips *Clips) error {
		if clips == nil {
			return nil
		}
		for _, clip := range clips.Clip {
			for _, area := range clip.Area {
				if area.DrawParam != "" {
					hasResourceRef = true
				}
				for _, path := range area.Path {
					if path.DrawParam != "" {
						hasResourceRef = true
					}
					if err := collectClips(path.Clips); err != nil {
						return err
					}
					if err := collectColor(path.FillColor); err != nil {
						return err
					}
					if err := collectColor((*FillColor)(path.StrokeColor)); err != nil {
						return err
					}
				}
				for _, text := range area.Text {
					if text.DrawParam != "" {
						hasResourceRef = true
					}
					if err := collectText(text); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	collectColor = func(c *FillColor) error {
		if c == nil || c.Pattern == nil {
			return nil
		}
		for sub := range c.Pattern.CellContent.objects() {
			ref, err := e.collectMemoryObjectFontUsage(ctx, sub, used, mapped)
			if err != nil {
				return err
			}
			if ref {
				hasResourceRef = true
			}
		}
		return nil
	}
	switch object.Type {
	case "TextObject", "Text":
		if object.TextObject.DrawParam != "" {
			hasResourceRef = true
		}
		if err := collectText(object.TextObject); err != nil {
			return false, err
		}
	case "PathObject", "Path":
		if object.PathObject.DrawParam != "" {
			hasResourceRef = true
		}
		if err := collectClips(object.PathObject.Clips); err != nil {
			return false, err
		}
		if err := collectColor(object.PathObject.FillColor); err != nil {
			return false, err
		}
		if err := collectColor((*FillColor)(object.PathObject.StrokeColor)); err != nil {
			return false, err
		}
	case "ImageObject":
		if object.ImageObject.DrawParam != "" {
			hasResourceRef = true
		}
		if err := collectClips(object.ImageObject.Clips); err != nil {
			return false, err
		}
		if object.ImageObject.Border != nil {
			if err := collectColor((*FillColor)(object.ImageObject.Border.BorderColor)); err != nil {
				return false, err
			}
		}
	case "CompositeObject", "CompositeGraphicUnit":
		if object.CompositeGraphicUnit.ResourceID != "" || object.CompositeGraphicUnit.DrawParam != "" {
			hasResourceRef = true
		}
		if err := collectClips(object.CompositeGraphicUnit.Clips); err != nil {
			return false, err
		}
		for child := range object.CompositeGraphicUnit.objects() {
			ref, err := e.collectMemoryObjectFontUsage(ctx, child, used, mapped)
			if err != nil {
				return false, err
			}
			if ref {
				hasResourceRef = true
			}
		}
	}
	return hasResourceRef, nil
}

// collectTextFontUsage 收集文字图元的字符与显式字形编号，遇到索引编码或越界字形时放弃该字体裁剪
// 入参: ctx 取消上下文, obj 文字对象, used 新增字体用字表, mapped 显式字形映射标记
// 返回: error 取消错误
func (e *Editor) collectTextFontUsage(ctx context.Context, obj TextObject, used map[string]*editorFontUsage, mapped map[string]bool) error {
	fontID := editorResourceID(obj.Font)
	if fontID == "" {
		fontID = obj.Font
	}
	usage := used[fontID]
	if usage == nil {
		return nil
	}
	for _, code := range obj.TextCode {
		if code.Index != "" {
			delete(used, fontID)
			return nil
		}
		if err := usage.text(ctx, "TextCode", code.Value); err != nil {
			return err
		}
	}
	for _, transform := range obj.CGTransform {
		mapped[fontID] = true
		count := 0
		for value := range strings.FieldsSeq(transform.Glyphs) {
			if count&4095 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			id, err := strconv.ParseUint(value, 10, 16)
			if err != nil {
				delete(used, fontID)
				return nil
			}
			usage.glyphs[uint16(id)] = true
			count++
		}
	}
	return nil
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

// editorFontCharsDigest 计算排序后字符集合的内容摘要，用于缓存命中校验
// 入参: chars 已排序字符切片
// 返回: [32]byte SHA-256摘要
func editorFontCharsDigest(chars []rune) [32]byte {
	if len(chars) == 0 {
		return [32]byte{}
	}
	encoded := make([]byte, 0, len(chars)*4)
	for _, char := range chars {
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(char))
	}
	return sha256.Sum256(encoded)
}

// expandEditorFontGlyphs 展开显式字形编号并对CID编码CFF字体执行CID到GID反查
// 入参: data 字体原始字节, source 显式字形集合, numGlyphs 字体字形总数
// 返回: map[uint16]bool 展开后的字形集合, bool 编号是否全部有效
func expandEditorFontGlyphs(data []byte, source map[uint16]bool, numGlyphs uint16) (map[uint16]bool, bool) {
	glyphs := make(map[uint16]bool, len(source)+1)
	if len(source) == 0 {
		return glyphs, true
	}
	cidToGID := basefont.CFFCIDToGID(data)
	for id := range source {
		valid := false
		if id < numGlyphs {
			glyphs[id] = true
			valid = true
		}
		if gid, ok := cidToGID[id]; ok && gid < numGlyphs {
			glyphs[gid] = true
			valid = true
		}
		if !valid {
			return nil, false
		}
	}
	return glyphs, true
}

// subsetEditorFontData 在保留原编号或存在格式14变体映射时优先传入字符集合裁剪，存在变体表且无结果时保留原字体
// 入参: ctx 取消上下文, backend 字体资源后端, data 字体字节, chars 排序后字符, explicit 显式字形, ids 全部字形, preserve 是否保留原编号
// 返回: []byte 字体子集, error 裁剪或取消错误
func subsetEditorFontData(ctx context.Context, backend FontResourceBackend, data []byte, chars []rune, explicit, ids []uint16, preserve bool) ([]byte, error) {
	hasVariations := basefont.HasCmapVariations(data)
	if len(chars) > 0 && (preserve || hasVariations) {
		usage := FontUsage{Characters: chars}
		if preserve {
			usage.Glyphs = explicit
		}
		subset, err := backend.SubsetSourceFont(data, usage)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(subset) > 0 && len(subset) < len(data) {
			return subset, nil
		}
		if hasVariations {
			return nil, nil
		}
	}
	return backend.SubsetFont(data, ids, preserve)
}
