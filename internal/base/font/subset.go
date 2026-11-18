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

package font

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"

	"github.com/go-text/typesetting/font/opentype/tables"
)

// Subsettable 判断默认资源后端能否无损裁剪该字体
// 入参: sfnt 已解析字体
// 返回: bool 是否支持裁剪
func Subsettable(sfnt *SFNT) bool {
	if fontEmbeddingFlags(sfnt)&0x0100 != 0 || slices.ContainsFunc([]string{"COLR", "CBDT", "sbix", "SVG "}, func(tag string) bool { return sfnt.Tables[tag] != nil }) {
		return false
	}
	if sfnt.IsCFF {
		if sfnt.Tables["fvar"] != nil {
			return false
		}
		font, err := readType2Font(sfnt.Tables["CFF "])
		if err == nil {
			return len(font.chars) == int(sfnt.NumGlyphs())
		}
		cid, err := readCIDSubsetFont(sfnt.Tables["CFF "])
		return err == nil && len(cid.fonts[0].chars) == int(sfnt.NumGlyphs())
	}
	return sfnt.IsTrueType && (sfnt.Tables["fvar"] == nil || variableSFNTSubsettable(sfnt))
}

// ValidateEmbedding 校验字体是否允许作为可编辑轮廓字体嵌入
// 入参: data 独立OpenType字体
// 返回: error 字体解析或嵌入限制
func ValidateEmbedding(data []byte) error {
	sfnt, err := ParseSFNT(data)
	if err != nil {
		return err
	}
	flags := fontEmbeddingFlags(sfnt)
	if flags&0x0200 != 0 || flags&0x000e != 0 && flags&0x0008 == 0 {
		return fmt.Errorf("font does not permit editable embedding")
	}
	return nil
}

// SFNTName 获取字体中的名称
// 入参: sfnt 字体, names 名称类型，按顺序查找
// 返回: string 字体名称
func SFNTName(sfnt *SFNT, names ...uint16) string {
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

// SubsetSFNT 按实际用字裁剪字体，保留字符映射、名称及度量
// 入参: data 字体数据, glyphs 已排序的字形编号，包含0, mapped 是否保留显式字形编号
// 返回: []byte 字体子集, error 错误信息
func SubsetSFNT(data []byte, glyphs []uint16, mapped bool) ([]byte, error) {
	data, err := DefaultCFF2Font(data)
	if err != nil {
		return nil, err
	}
	sfnt, err := ParseSFNT(data)
	if err != nil {
		return nil, err
	}
	if fontEmbeddingFlags(sfnt)&0x0100 != 0 {
		return bytes.Clone(data), nil
	}
	if sfnt.IsCFF {
		count, err := parseCFFAndCountGlyphs(sfnt.Tables["CFF "])
		if err != nil {
			return nil, err
		}
		if count != int(sfnt.NumGlyphs()) {
			return nil, fmt.Errorf("CFF glyph count does not match maxp")
		}
		return subsetCFFFont(sfnt, glyphs, mapped)
	}
	if sfnt.Tables["fvar"] != nil {
		return subsetVariableSFNTFont(sfnt, glyphs)
	}
	if mapped {
		usage := newUsage()
		for _, id := range glyphs {
			usage.Glyphs[id] = true
			for _, char := range sfnt.glyphCharacters(id) {
				usage.Characters[char] = true
			}
		}
		tables := make(map[string][]byte)
		for _, tag := range []string{"cmap", "head", "hhea", "hmtx", "vhea", "vmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "cvt ", "fpgm", "prep", "gasp"} {
			if table := sfnt.Tables[tag]; table != nil {
				tables[tag] = table
			}
		}
		fixed, err := SerializeOTF(tables)
		if err != nil {
			return nil, err
		}
		sfnt.Tables = tables
		if subset := subsetSourceSFNT(sfnt, usage, len(fixed)); subset != nil {
			return subset, nil
		}
		return fixed, nil
	}
	return subsetCompactSFNTFont(sfnt, glyphs)
}

// SubsetSourceSFNT 裁剪静态TrueType或CFF原有字体，保留字形编号、组合依赖、度量和提示指令
// 字形替换结果一并保留，集合、可变、彩色和未知表保持原样
// 入参: data 字体数据, usage 全包用字记录
// 返回: []byte 更小的字体子集，无确定收益时为空
func SubsetSourceSFNT(data []byte, usage *Usage) []byte {
	if usage == nil || usage.Unsafe || len(usage.Characters)+len(usage.Glyphs) == 0 || bytes.HasPrefix(data, []byte("ttcf")) {
		return nil
	}
	fontTables, err := FileTables(data, 0)
	if err != nil {
		return nil
	}
	for tag := range fontTables {
		switch tag {
		case "cmap", "head", "hhea", "hmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "CFF ", "cvt ", "fpgm", "prep", "gasp", "kern", "vhea", "vmtx", "VORG", "hdmx", "LTSH", "VDMX", "GDEF", "GPOS", "GSUB", "meta", "DSIG", "FFTM", "PCLT":
		default:
			return nil
		}
	}
	sfnt, err := ParseSFNTTables(fontTables)
	if err != nil {
		return nil
	}
	return subsetSourceSFNT(sfnt, usage, len(data))
}

// fontEmbeddingFlags 读取OpenType的嵌入标志，未提供OS/2表时返回零
// 入参: sfnt 已解析字体
// 返回: uint16 嵌入标志
func fontEmbeddingFlags(sfnt *SFNT) uint16 {
	if table := sfnt.Tables["OS/2"]; len(table) >= 10 {
		return binary.BigEndian.Uint16(table[8:10])
	}
	return 0
}

// subsetSourceSFNT 复用已校验的字体表裁剪字形，仅返回小于原编码的结果
// 入参: sfnt 已解析字体, usage 用字记录, size 原编码长度
// 返回: []byte 更小的字体子集，无确定收益时为空
func subsetSourceSFNT(sfnt *SFNT, usage *Usage, size int) []byte {
	if usage == nil || usage.Unsafe || len(usage.Characters)+len(usage.Glyphs) == 0 || !sfnt.IsTrueType && !sfnt.IsCFF || fontEmbeddingFlags(sfnt)&0x0100 != 0 {
		return nil
	}
	var unicodeTables []sfntCmap
	for _, record := range sfnt.cmaps {
		if record.format == 14 {
			continue
		}
		if record.platform == 0 || record.platform == 3 && (record.encoding == 1 || record.encoding == 10) {
			unicodeTables = append(unicodeTables, record)
		}
	}
	if len(unicodeTables) == 0 {
		return nil
	}
	glyphs := maps.Clone(usage.Glyphs)
	if glyphs == nil {
		glyphs = make(map[uint16]bool)
	}
	glyphs[0] = true
	mapping := make(map[rune]uint16, len(usage.Characters))
	for char := range usage.Characters {
		id := sfnt.GlyphIndex(char)
		for _, cmap := range unicodeTables {
			if alternate, ok := cmap.lookup(char); ok && alternate != id {
				return nil
			}
		}
		glyphs[id] = true
		if id != 0 {
			mapping[char] = id
		}
	}
	variations, ok := subsetCmapVariations(sfnt.Tables["cmap"], usage.Characters, glyphs, sfnt.NumGlyphs())
	if !ok || !retainSFNTSubstitutions(sfnt.Tables["GSUB"], glyphs, sfnt.NumGlyphs()) {
		return nil
	}
	if sfnt.IsCFF {
		count, err := parseCFFAndCountGlyphs(sfnt.Tables["CFF "])
		if err != nil || count != int(sfnt.NumGlyphs()) {
			return nil
		}
		cff, err := subsetCFFCharstrings(sfnt.Tables["CFF "], glyphs)
		if err != nil {
			return nil
		}
		tables := maps.Clone(sfnt.Tables)
		tables["CFF "] = cff
		tables["cmap"] = buildCmapWithVariations(sfnt.NumGlyphs(), mapping, variations)
		delete(tables, "DSIG")
		result, err := SerializeOTF(tables)
		if err != nil || len(result) >= size {
			return nil
		}
		return result
	}
	for id := 0; id < int(sfnt.NumGlyphs()); id++ {
		if _, err := trueTypeGlyphData(sfnt.Tables, uint16(id)); err != nil {
			return nil
		}
	}
	dependencies, err := sfnt.glyphDependencies(slices.Collect(maps.Keys(glyphs))...)
	if err != nil {
		return nil
	}
	for _, dependency := range dependencies {
		glyphs[dependency] = true
	}
	glyfSize := 0
	for id, retained := range glyphs {
		if retained {
			data, _ := trueTypeGlyphData(sfnt.Tables, id)
			glyfSize += len(data) + len(data)%2
		}
	}
	locaFormat := binary.BigEndian.Uint16(sfnt.Tables["head"][50:])
	glyf := make([]byte, 0, glyfSize)
	loca := make([]byte, 0, (int(sfnt.NumGlyphs())+1)*(2+2*int(locaFormat)))
	for id := 0; id <= int(sfnt.NumGlyphs()); id++ {
		if locaFormat == 0 {
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
	tables["cmap"] = buildCmapWithVariations(sfnt.NumGlyphs(), mapping, variations)
	tables["head"] = bytes.Clone(tables["head"])
	clear(tables["head"][8:12])
	delete(tables, "DSIG")
	result, err := SerializeOTF(tables)
	if err != nil || len(result) >= size {
		return nil
	}
	return result
}

// retainSFNTSubstitutions 保留全部替换结果，使原字形替换表在裁剪后仍可使用
// 入参: data 替换表, glyphs 待保留字形, count 字形总数
// 返回: bool 替换表及结果编号是否有效
func retainSFNTSubstitutions(data []byte, glyphs map[uint16]bool, count uint16) bool {
	if len(data) == 0 {
		return true
	}
	if len(data) < 10 || binary.BigEndian.Uint16(data) != 1 || binary.BigEndian.Uint16(data[2:]) > 1 {
		return false
	}
	if binary.BigEndian.Uint16(data[2:]) == 1 && (len(data) < 14 || binary.BigEndian.Uint32(data[10:]) != 0) {
		return false
	}
	layout, _, err := tables.ParseLayout(data)
	if err != nil {
		return false
	}
	for _, lookup := range layout.LookupList.Lookups {
		subtables, err := lookup.AsGSUBLookups()
		if err != nil {
			return false
		}
		for _, subtable := range subtables {
			if extension, ok := subtable.(tables.ExtensionSubs); ok {
				if extension.ExtensionOffset < 8 || uint64(extension.ExtensionOffset) > uint64(len(extension.RawData)) {
					return false
				}
				subtable, err = extension.Resolve()
				if err != nil {
					return false
				}
			}
			if !retainSFNTSubstitution(subtable, glyphs, count) {
				return false
			}
		}
	}
	return true
}

// retainSFNTSubstitution 收集替换子表的输出字形，上下文替换的结果由被引用的查找表提供
// 入参: subtable 替换子表, glyphs 待保留字形, count 字形总数
// 返回: bool 子表类型及输出字形是否有效
func retainSFNTSubstitution(subtable tables.GSUBLookup, glyphs map[uint16]bool, count uint16) bool {
	var outputs []tables.GlyphID
	switch subtable := subtable.(type) {
	case tables.SingleSubs:
		switch data := subtable.Data.(type) {
		case tables.SingleSubstData1:
			for id := uint16(0); id < count; id++ {
				if _, ok := data.Coverage.Index(tables.GlyphID(id)); ok {
					outputs = append(outputs, tables.GlyphID(id+uint16(data.DeltaGlyphID)))
				}
			}
		case tables.SingleSubstData2:
			outputs = data.SubstituteGlyphIDs
		default:
			return false
		}
	case tables.MultipleSubs:
		for _, sequence := range subtable.Sequences {
			outputs = append(outputs, sequence.SubstituteGlyphIDs...)
		}
	case tables.AlternateSubs:
		for _, set := range subtable.AlternateSets {
			outputs = append(outputs, set.AlternateGlyphIDs...)
		}
	case tables.LigatureSubs:
		for _, set := range subtable.LigatureSets {
			for _, ligature := range set.Ligatures {
				outputs = append(outputs, ligature.LigatureGlyph)
			}
		}
	case tables.ReverseChainSingleSubs:
		outputs = subtable.SubstituteGlyphIDs
	case tables.ContextualSubs, tables.ChainedContextualSubs:
	default:
		return false
	}
	for _, id := range outputs {
		if uint16(id) >= count {
			return false
		}
		glyphs[uint16(id)] = true
	}
	return true
}

// subsetCompactSFNTFont 重排新建静态字体的字形，同步复合引用、字符映射和度量
// 入参: sfnt 字体, glyphs 使用字形
// 返回: []byte 紧凑字体子集, error 字形数据错误
func subsetCompactSFNTFont(sfnt *SFNT, glyphs []uint16) ([]byte, error) {
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
	dependencies, err := sfnt.glyphDependencies(ids...)
	if err != nil {
		return nil, err
	}
	for _, id := range dependencies {
		if _, ok := remap[id]; !ok {
			remap[id] = uint16(len(ids))
			ids = append(ids, id)
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
	table["cmap"] = BuildCmapTable(uint16(len(ids)), mapping)
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
	return SerializeOTF(table)
}
