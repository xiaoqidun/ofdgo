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
	"fmt"
	"slices"

	"github.com/tdewolff/font"
)

// subsetFonts 收集新增字体实际使用的字形，仅裁剪保存结果，不修改编辑资源或原文档字体。
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

// subsetEditorFont 裁剪TrueType字形及复合依赖，保留字符映射、名称、度量和提示指令。
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
