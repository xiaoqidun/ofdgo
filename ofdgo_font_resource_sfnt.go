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

	"github.com/tdewolff/font"
)

// SFNTBackend 提供默认字体资源处理，不依赖Canvas绘图库
type SFNTBackend struct{}

// sfntResourceMetrics 隔离资源解析器的度量实现
type sfntResourceMetrics struct{ *font.SFNT }

// Name 返回资源后端标识
// 返回: string 后端标识
func (SFNTBackend) Name() string { return "sfnt" }

// OpenFontResource 提取独立字体并读取名称、样式和裁剪能力
// 入参: file 字体文件, index 集合索引
// 返回: *FontResource 字体资源, error 解析错误
func (SFNTBackend) OpenFontResource(file FontFile, index int) (*FontResource, error) {
	data, err := file.Face(index)
	if err != nil {
		return nil, err
	}
	sfnt, err := font.ParseSFNT(data, 0)
	if err != nil {
		return nil, err
	}
	name := sfntFontName(sfnt, font.NamePostScript, font.NameFull, font.NameFontFamily)
	if name == "" {
		return nil, fmt.Errorf("font has no name")
	}
	extension := ".ttf"
	if sfnt.IsCFF {
		extension = ".otf"
	}
	return &FontResource{
		FontMetrics: sfntResourceMetrics{sfnt}, Data: data, Extension: extension,
		Font:      Font{FontName: name, FamilyName: sfntFontName(sfnt, font.NamePreferredFamily, font.NameFontFamily), Charset: "unicode", Bold: sfnt.Head.MacStyle[0], Italic: sfnt.Head.MacStyle[1], FixedWidth: sfnt.Post.IsFixedPitch != 0},
		CanSubset: sfntSubsettable(sfnt),
	}, nil
}

// SubsetFont 裁剪新建字体并按要求保留显式字形编号
// 入参: data 字体数据, glyphs 用字, preserve 是否保留编号
// 返回: []byte 子集, error 裁剪错误
func (SFNTBackend) SubsetFont(data []byte, glyphs []uint16, preserve bool) ([]byte, error) {
	return subsetSFNTFont(data, glyphs, preserve)
}

// SubsetSourceFont 按全包引用裁剪原字体，不确定时保留原资源
// 入参: data 字体数据, usage 用字信息
// 返回: []byte 子集, error 裁剪错误
func (SFNTBackend) SubsetSourceFont(data []byte, usage FontUsage) ([]byte, error) {
	u := newEditorFontUsage()
	u.unsafe = usage.Unsafe
	for _, char := range usage.Characters {
		u.chars[char] = true
	}
	for _, glyph := range usage.Glyphs {
		u.glyphs[glyph] = true
	}
	return subsetSourceSFNTFont(data, u), nil
}

// Write 返回未改动元信息的字体数据
// 返回: []byte 字体数据
func (f sfntResourceMetrics) Write() []byte { return fontSFNTData(f.SFNT) }

// sfntSubsettable 判断默认资源后端能否无损裁剪该字体
// 入参: sfnt 已解析字体
// 返回: bool 是否支持裁剪
func sfntSubsettable(sfnt *font.SFNT) bool {
	return sfnt.IsTrueType && !slices.ContainsFunc([]string{"fvar", "COLR", "CBDT", "sbix", "SVG "}, func(tag string) bool { return sfnt.Tables[tag] != nil })
}

// sfntFontName 获取字体中的名称
// 入参: sfnt 字体, names 名称类型，按顺序查找
// 返回: string 字体名称
func sfntFontName(sfnt *font.SFNT, names ...font.NameID) string {
	for _, name := range names {
		for _, record := range sfnt.Name.Get(name) {
			if value := record.String(); value != "" {
				return value
			}
		}
	}
	return ""
}

// subsetSFNTFont 裁剪TrueType字形及复合依赖，保留字符映射、名称、度量和提示指令
// 入参: data 字体数据, glyphs 已排序的字形编号，包含0, mapped 是否保留显式字形编号
// 返回: []byte 字体子集, error 错误信息
func subsetSFNTFont(data []byte, glyphs []uint16, mapped bool) ([]byte, error) {
	sfnt, err := font.ParseSFNT(bytes.Clone(data), 0)
	if err != nil {
		return nil, err
	}
	if mapped {
		usage := newEditorFontUsage()
		for _, id := range glyphs {
			usage.glyphs[id] = true
			for _, char := range sfnt.Cmap.ToUnicode(id) {
				usage.chars[char] = true
			}
		}
		tables := make(map[string][]byte)
		for _, tag := range []string{"cmap", "head", "hhea", "hmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "cvt ", "fpgm", "prep", "gasp"} {
			if table := sfnt.Tables[tag]; table != nil {
				tables[tag] = table
			}
		}
		fixed, err := serializeOTF(tables)
		if err != nil {
			return nil, err
		}
		if subset := subsetSourceSFNTFont(fixed, usage); subset != nil {
			return subset, nil
		}
		return fixed, nil
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

// subsetSourceSFNTFont 裁剪静态TrueType原有字体，保留字形编号、复合依赖、度量及提示指令
// 集合、字形替换、可变、彩色和未知表保持原样，不对不完整的引用或异常字体猜测修复
// 入参: data 字体数据, usage 全包用字记录
// 返回: []byte 更小的字体子集，无确定收益时为空
func subsetSourceSFNTFont(data []byte, usage *editorFontUsage) []byte {
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
