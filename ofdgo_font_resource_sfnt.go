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
)

// SFNTBackend 提供独立的OpenType字体度量、轮廓和资源处理
type SFNTBackend struct{}

// sfntResourceMetrics 隔离资源解析器的度量实现
type sfntResourceMetrics struct{ *sfntFont }

// Name 返回资源后端标识
// 返回: string 后端标识
func (SFNTBackend) Name() string { return "sfnt" }

// OpenFontResource 提取独立字体并读取名称、样式和裁剪能力
// CFF2使用默认实例的静态轮廓，不改变原字体文件
// 入参: file 字体文件, index 集合索引
// 返回: *FontResource 字体资源, error 解析错误
func (SFNTBackend) OpenFontResource(file FontFile, index int) (*FontResource, error) {
	data, err := file.Face(index)
	if err != nil {
		return nil, err
	}
	data, err = defaultCFF2Font(data)
	if err != nil {
		return nil, err
	}
	sfnt, err := parseSFNTFont(data)
	if err != nil {
		return nil, err
	}
	name := sfntFontName(sfnt, 6, 4, 1)
	if name == "" {
		return nil, fmt.Errorf("font has no name")
	}
	extension := ".ttf"
	if sfnt.IsCFF {
		extension = ".otf"
	}
	return &FontResource{
		FontMetrics: sfntResourceMetrics{sfnt}, Data: data, Extension: extension,
		Font:      Font{FontName: name, FamilyName: sfntFontName(sfnt, 16, 1), Charset: "unicode", Bold: binary.BigEndian.Uint16(sfnt.Tables["head"][44:])&1 != 0, Italic: binary.BigEndian.Uint16(sfnt.Tables["head"][44:])&2 != 0, FixedWidth: len(sfnt.Tables["post"]) >= 16 && binary.BigEndian.Uint32(sfnt.Tables["post"][12:]) != 0},
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
func (f sfntResourceMetrics) Write() []byte { return f.sfntFont.Write() }

// sfntSubsettable 判断默认资源后端能否无损裁剪该字体
// 入参: sfnt 已解析字体
// 返回: bool 是否支持裁剪
func sfntSubsettable(sfnt *sfntFont) bool {
	if fontEmbeddingFlags(sfnt)&0x0100 != 0 || slices.ContainsFunc([]string{"COLR", "CBDT", "sbix", "SVG "}, func(tag string) bool { return sfnt.Tables[tag] != nil }) {
		return false
	}
	if sfnt.IsCFF {
		if sfnt.Tables["fvar"] != nil {
			return false
		}
		font, err := readType2Font(sfnt.Tables["CFF "])
		return err == nil && len(font.chars) == int(sfnt.NumGlyphs()) || staticCFFPrograms(sfnt) != nil
	}
	return sfnt.IsTrueType && (sfnt.Tables["fvar"] == nil || variableSFNTSubsettable(sfnt))
}

// fontEmbeddingFlags 读取OpenType的嵌入标志，未提供OS/2表时返回零
// 入参: sfnt 已解析字体
// 返回: uint16 嵌入标志
func fontEmbeddingFlags(sfnt *sfntFont) uint16 {
	if table := sfnt.Tables["OS/2"]; len(table) >= 10 {
		return binary.BigEndian.Uint16(table[8:10])
	}
	return 0
}

// validateFontEmbedding 校验字体是否允许作为可编辑轮廓字体嵌入
// 入参: data 独立OpenType字体
// 返回: error 字体解析或嵌入限制
func validateFontEmbedding(data []byte) error {
	sfnt, err := parseSFNTFont(data)
	if err != nil {
		return err
	}
	flags := fontEmbeddingFlags(sfnt)
	if flags&0x0200 != 0 || flags&0x000e != 0 && flags&0x0008 == 0 {
		return fmt.Errorf("font does not permit editable embedding")
	}
	return nil
}

// sfntFontName 获取字体中的名称
// 入参: sfnt 字体, names 名称类型，按顺序查找
// 返回: string 字体名称
func sfntFontName(sfnt *sfntFont, names ...uint16) string {
	records, values := fontNameValues(sfnt.Tables["name"])
	for _, name := range names {
		for _, record := range records {
			if record.Name != name {
				continue
			}
			if value := values[[4]uint16{record.Platform, record.Encoding, record.Language, name}]; value != "" {
				return value
			}
		}
	}
	return ""
}

// subsetSFNTFont 按实际用字裁剪字体，保留字符映射、名称及度量
// 入参: data 字体数据, glyphs 已排序的字形编号，包含0, mapped 是否保留显式字形编号
// 返回: []byte 字体子集, error 错误信息
func subsetSFNTFont(data []byte, glyphs []uint16, mapped bool) ([]byte, error) {
	data, err := defaultCFF2Font(data)
	if err != nil {
		return nil, err
	}
	sfnt, err := parseSFNTFont(data)
	if err != nil {
		return nil, err
	}
	if fontEmbeddingFlags(sfnt)&0x0100 != 0 {
		return bytes.Clone(data), nil
	}
	if sfnt.IsCFF {
		if font, err := readType2Font(sfnt.Tables["CFF "]); err == nil {
			if len(font.chars) != int(sfnt.NumGlyphs()) {
				return nil, fmt.Errorf("CFF glyph count does not match maxp")
			}
			return subsetNamedCFFFont(sfnt, glyphs)
		}
		return subsetStaticCFFFont(sfnt, glyphs)
	}
	if sfnt.Tables["fvar"] != nil {
		return subsetVariableSFNTFont(sfnt, glyphs)
	}
	if mapped {
		usage := newEditorFontUsage()
		for _, id := range glyphs {
			usage.glyphs[id] = true
			for _, char := range sfnt.glyphCharacters(id) {
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
	return subsetCompactSFNTFont(sfnt, glyphs)
}

// subsetSourceSFNTFont 裁剪静态TrueType或非CID的CFF原有字体，保留字形编号、组合依赖、度量和提示指令
// 集合、字形替换、可变、彩色和未知表保持原样，不对不完整的引用或异常字体猜测修复
// 入参: data 字体数据, usage 全包用字记录
// 返回: []byte 更小的字体子集，无确定收益时为空
func subsetSourceSFNTFont(data []byte, usage *editorFontUsage) []byte {
	if usage.unsafe || len(usage.chars)+len(usage.glyphs) == 0 || bytes.HasPrefix(data, []byte("ttcf")) {
		return nil
	}
	sfnt, err := parseSFNTFont(data)
	if err != nil || !sfnt.IsTrueType && !sfnt.IsCFF || fontEmbeddingFlags(sfnt)&0x0100 != 0 {
		return nil
	}
	for tag := range sfnt.Tables {
		switch tag {
		case "cmap", "head", "hhea", "hmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "CFF ", "cvt ", "fpgm", "prep", "gasp", "kern", "vhea", "vmtx", "hdmx", "LTSH", "VDMX", "GDEF", "GPOS", "DSIG", "FFTM":
		default:
			return nil
		}
	}
	var unicodeTables []sfntCmap
	for _, record := range sfnt.cmaps {
		if record.format == 14 {
			return nil
		}
		if record.platform == 0 || record.platform == 3 && (record.encoding == 1 || record.encoding == 10) {
			unicodeTables = append(unicodeTables, record)
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
		for _, cmap := range unicodeTables {
			if alternate, ok := cmap.mapping[char]; ok && alternate != id {
				return nil
			}
		}
		glyphs[id] = true
		if id != 0 {
			mapping[char] = id
		}
	}
	if sfnt.IsCFF {
		font, err := readType2Font(sfnt.Tables["CFF "])
		if err != nil || len(font.chars) != int(sfnt.NumGlyphs()) {
			return nil
		}
		cff, err := subsetCFFCharstrings(sfnt.Tables["CFF "], glyphs)
		if err != nil {
			return nil
		}
		tables := maps.Clone(sfnt.Tables)
		tables["CFF "] = cff
		tables["cmap"] = buildCmapTable(sfnt.NumGlyphs(), mapping)
		delete(tables, "DSIG")
		result, err := serializeOTF(tables)
		if err != nil || len(result) >= len(data) {
			return nil
		}
		return result
	}
	for id := 0; id < int(sfnt.NumGlyphs()); id++ {
		if _, err := trueTypeGlyphData(sfnt.Tables, uint16(id)); err != nil {
			return nil
		}
	}
	for id := range glyphs {
		if id >= sfnt.NumGlyphs() {
			return nil
		}
		dependencies, err := sfnt.glyphDependencies(id)
		if err != nil {
			return nil
		}
		for _, dependency := range dependencies {
			glyphs[dependency] = true
		}
	}
	var glyf, loca []byte
	for id := 0; id <= int(sfnt.NumGlyphs()); id++ {
		if binary.BigEndian.Uint16(sfnt.Tables["head"][50:]) == 0 {
			loca = binary.BigEndian.AppendUint16(loca, uint16(len(glyf)/2))
		} else {
			loca = binary.BigEndian.AppendUint32(loca, uint32(len(glyf)))
		}
		if id < int(sfnt.NumGlyphs()) && glyphs[uint16(id)] {
			data, _ := trueTypeGlyphData(sfnt.Tables, uint16(id))
			glyf = append(glyf, data...)
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

// subsetCompactSFNTFont 重排新建静态字体的字形，同步复合引用、字符映射和度量
// 入参: sfnt 字体, glyphs 使用字形
// 返回: []byte 紧凑字体子集, error 字形数据错误
func subsetCompactSFNTFont(sfnt *sfntFont, glyphs []uint16) ([]byte, error) {
	ids := []uint16{0}
	remap := map[uint16]uint16{0: 0}
	for _, id := range glyphs {
		if id >= sfnt.NumGlyphs() {
			return nil, fmt.Errorf("font glyph index out of range")
		}
		if _, ok := remap[id]; !ok {
			remap[id] = uint16(len(ids))
			ids = append(ids, id)
		}
	}
	for index := 0; index < len(ids); index++ {
		dependencies, err := sfnt.glyphDependencies(ids[index])
		if err != nil {
			return nil, err
		}
		for _, id := range dependencies {
			if _, ok := remap[id]; !ok {
				remap[id] = uint16(len(ids))
				ids = append(ids, id)
			}
		}
	}
	table := make(map[string][]byte)
	for _, tag := range []string{"head", "hhea", "maxp", "OS/2", "post", "name", "cvt ", "fpgm", "prep", "gasp", "vhea"} {
		if data := sfnt.Tables[tag]; data != nil {
			table[tag] = bytes.Clone(data)
		}
	}
	var glyf, loca, hmtx, vmtx []byte
	mapping := make(map[rune]uint16)
	for _, id := range ids {
		loca = binary.BigEndian.AppendUint32(loca, uint32(len(glyf)))
		data, err := trueTypeGlyphData(sfnt.Tables, id)
		if err != nil {
			return nil, err
		}
		data = bytes.Clone(data)
		if len(data) >= 10 && int16(binary.BigEndian.Uint16(data)) < 0 {
			for pos := 10; ; {
				flags := binary.BigEndian.Uint16(data[pos:])
				child := binary.BigEndian.Uint16(data[pos+2:])
				binary.BigEndian.PutUint16(data[pos+2:], remap[child])
				pos += 6
				if flags&1 != 0 {
					pos += 2
				}
				switch flags & 0xc8 {
				case 8:
					pos += 2
				case 0x40:
					pos += 4
				case 0x80:
					pos += 8
				}
				if flags&0x20 == 0 {
					break
				}
			}
		}
		glyf = append(glyf, data...)
		for len(glyf)%4 != 0 {
			glyf = append(glyf, 0)
		}
		hmtx = binary.BigEndian.AppendUint16(hmtx, sfnt.GlyphAdvance(id))
		hmtx = binary.BigEndian.AppendUint16(hmtx, uint16(sfnt.sideBearing(id, false)))
		if sfnt.vertical != 0 {
			vmtx = binary.BigEndian.AppendUint16(vmtx, sfnt.GlyphVerticalAdvance(id))
			vmtx = binary.BigEndian.AppendUint16(vmtx, uint16(sfnt.sideBearing(id, true)))
		}
		for _, char := range sfnt.glyphCharacters(id) {
			mapping[char] = remap[id]
		}
	}
	loca = binary.BigEndian.AppendUint32(loca, uint32(len(glyf)))
	table["glyf"], table["loca"], table["hmtx"] = glyf, loca, hmtx
	table["cmap"] = buildCmapTable(uint16(len(ids)), mapping)
	binary.BigEndian.PutUint16(table["head"][50:], 1)
	binary.BigEndian.PutUint16(table["hhea"][34:], uint16(len(ids)))
	binary.BigEndian.PutUint16(table["maxp"][4:], uint16(len(ids)))
	if len(table["post"]) >= 32 {
		table["post"] = table["post"][:32]
		binary.BigEndian.PutUint32(table["post"], 0x30000)
	}
	if sfnt.vertical != 0 {
		table["vmtx"] = vmtx
		binary.BigEndian.PutUint16(table["vhea"][34:], uint16(len(ids)))
	}
	return serializeOTF(table)
}
