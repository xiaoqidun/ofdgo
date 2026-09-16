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
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/tdewolff/font"
)

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

// subsetSourceFont 裁剪静态TrueType原有字体，保留字形编号、复合依赖、度量及提示指令
// 集合、字形替换、可变、彩色和未知表保持原样，不对不完整的引用或异常字体猜测修复
// 入参: data 字体数据, usage 全包用字记录
// 返回: []byte 更小的字体子集，无确定收益时为空
func subsetSourceFont(data []byte, usage *editorFontUsage) []byte {
	if usage.unsafe || len(usage.chars)+len(usage.glyphs) == 0 || bytes.HasPrefix(data, []byte("ttcf")) {
		return nil
	}
	sfnt, err := font.ParseSFNT(data, 0)
	if err != nil || !sfnt.IsTrueType {
		return nil
	}
	for tag := range sfnt.Tables {
		switch tag {
		case "cmap", "head", "hhea", "hmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "cvt ", "fpgm", "prep", "gasp", "kern", "vhea", "vmtx", "hdmx", "LTSH", "VDMX", "GDEF", "GPOS", "DSIG", "FFTM":
		default:
			return nil
		}
	}
	var unicodeTables []uint16
	subtables := make(map[uint32]uint16)
	for i, record := range sfnt.Cmap.EncodingRecords {
		if record.Format == 14 {
			return nil
		}
		offset := binary.BigEndian.Uint32(sfnt.Tables["cmap"][8+8*i:])
		index, ok := subtables[offset]
		if !ok {
			index = uint16(len(subtables))
			subtables[offset] = index
		}
		if record.PlatformID == 0 || record.PlatformID == 3 && (record.EncodingID == 1 || record.EncodingID == 10) {
			unicodeTables = append(unicodeTables, index)
		}
	}
	if len(unicodeTables) == 0 {
		return nil
	}
	glyphs := maps.Clone(usage.glyphs)
	glyphs[0] = true
	mapping := make(map[rune]uint16, len(usage.chars))
	for char := range usage.chars {
		id := sfnt.GlyphIndex(char)
		for _, index := range unicodeTables {
			if alternate, ok := sfnt.Cmap.Subtables[index].Get(char); ok && alternate != id {
				return nil
			}
		}
		glyphs[id] = true
		if id != 0 {
			mapping[char] = id
		}
	}
	for id := range glyphs {
		if id >= sfnt.NumGlyphs() {
			return nil
		}
		dependencies, err := sfnt.Glyf.Dependencies(id)
		if err != nil {
			return nil
		}
		for _, dependency := range dependencies {
			glyphs[dependency] = true
		}
	}
	var glyf, loca []byte
	for id := 0; id <= int(sfnt.NumGlyphs()); id++ {
		if sfnt.Head.IndexToLocFormat == 0 {
			loca = binary.BigEndian.AppendUint16(loca, uint16(len(glyf)/2))
		} else {
			loca = binary.BigEndian.AppendUint32(loca, uint32(len(glyf)))
		}
		if id < int(sfnt.NumGlyphs()) && glyphs[uint16(id)] {
			glyf = append(glyf, sfnt.Glyf.Get(uint16(id))...)
			if len(glyf)%2 != 0 {
				glyf = append(glyf, 0)
			}
		}
	}
	tables := maps.Clone(sfnt.Tables)
	tables["glyf"], tables["loca"] = glyf, loca
	tables["cmap"] = buildCmapTable(uint16(sfnt.NumGlyphs()), mapping)
	tables["head"] = bytes.Clone(tables["head"])
	clear(tables["head"][8:12])
	delete(tables, "DSIG")
	result, err := serializeOTF(tables)
	if err != nil || len(result) >= len(data) {
		return nil
	}
	return result
}

// subsetFonts 收集新增字体实际使用的字形，仅裁剪保存结果，不修改编辑资源或原文档字体
// 返回: map[string][]byte 包内字体子集, error 错误信息
func (e *Editor) subsetFonts() (map[string][]byte, error) {
	used := make(map[string]map[uint16]bool)
	for _, resource := range e.resources {
		if resource.font == nil {
			continue
		}
		sfnt := e.fonts[resource.font.ID]
		if sfnt.IsTrueType && !slices.ContainsFunc([]string{"fvar", "COLR", "CBDT", "sbix", "SVG "}, func(tag string) bool { return sfnt.Tables[tag] != nil }) {
			used[resource.font.ID] = make(map[uint16]bool)
		}
	}
	for _, page := range e.pages {
		for _, layer := range page.Content.Layer {
			for _, object := range layer.Objects {
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
			}
		}
	}
	result := make(map[string][]byte)
	for _, resource := range e.resources {
		if resource.font == nil || len(used[resource.font.ID]) == 0 {
			continue
		}
		glyphs := used[resource.font.ID]
		glyphs[0] = true
		ids := make([]uint16, 0, len(glyphs))
		for id := range glyphs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		data, err := subsetEditorFont(resource.data, ids)
		if err != nil {
			return nil, fmt.Errorf("subset font %s: %w", resource.font.FontName, err)
		}
		if len(data) < len(resource.data) {
			result[resource.name] = data
		}
	}
	return result, nil
}

// subsetEditorFont 裁剪TrueType字形及复合依赖，保留字符映射、名称、度量和提示指令
// 入参: data 字体数据, glyphs 已排序的字形编号，包含0
// 返回: []byte 字体子集, error 错误信息
func subsetEditorFont(data []byte, glyphs []uint16) ([]byte, error) {
	sfnt, err := font.ParseSFNT(bytes.Clone(data), 0)
	if err != nil {
		return nil, err
	}
	subset, err := sfnt.Subset(glyphs, font.SubsetOptions{Tables: []string{
		"cmap", "head", "hhea", "hmtx", "maxp", "OS/2", "post", "glyf", "loca", "cvt ", "fpgm", "prep", "gasp",
	}})
	if err != nil {
		return nil, err
	}
	subset.Tables["name"] = sfnt.Tables["name"]
	clear(subset.Tables["head"][8:12])
	return serializeOTF(subset.Tables)
}
