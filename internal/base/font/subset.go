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
	if hasBitmapTables(sfnt.Tables) {
		hasOutline := false
		for _, id := range append([]uint16{0}, glyphs...) {
			if outline, err := trueTypeGlyphData(sfnt.Tables, id); err == nil && len(outline) > 0 {
				hasOutline = true
				break
			}
		}
		if !hasOutline {
			return nil, nil
		}
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
		for _, tag := range []string{"cmap", "head", "hhea", "hmtx", "vhea", "vmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "cvt ", "fpgm", "prep", "gasp", "GSUB", "GPOS", "GDEF", "kern", "BASE", "MATH", "meta", "PCLT", "FFTM", "STAT"} {
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
	return subsetCompactSFNTFont(sfnt, glyphs, nil, nil)
}

// SubsetSourceSFNT 裁剪静态TrueType或CFF原有字体，保留组合依赖、度量和提示指令
// 安全删除点阵和辅助提示表，在无显式编号及布局表依赖时优先产出紧凑子集
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
		case "cmap", "head", "hhea", "hmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "CFF ", "cvt ", "fpgm", "prep", "gasp", "kern", "vhea", "vmtx", "VORG", "GDEF", "GPOS", "GSUB", "BASE", "MATH", "meta", "PCLT", "FFTM", "STAT", "EBDT", "EBLC", "EBSC", "JSTF", "hdmx", "LTSH", "VDMX", "DSIG", "MERG":
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

// hasBitmapTables 判断字体是否包含嵌入式点阵表
// 入参: tables 字体表映射
// 返回: bool 是否含点阵表
func hasBitmapTables(tables map[string][]byte) bool {
	return tables["EBDT"] != nil || tables["EBLC"] != nil || tables["EBSC"] != nil
}

// dropTransientSubsetTables 删除轮廓裁剪后失效的点阵、辅助提示和签名表
// 入参: tables 字体表映射
func dropTransientSubsetTables(tables map[string][]byte) {
	for _, tag := range []string{"DSIG", "EBDT", "EBLC", "EBSC", "JSTF", "hdmx", "LTSH", "VDMX", "MERG"} {
		delete(tables, tag)
	}
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
		if !record.duplicate && record.isUnicode() {
			unicodeTables = append(unicodeTables, record)
		}
	}
	if len(unicodeTables) == 0 {
		return nil
	}
	glyphs := make(map[uint16]bool, len(usage.Glyphs)+1)
	var cidToGID map[uint16]uint16
	if sfnt.IsCFF && len(usage.Glyphs) > 0 {
		cidToGID = CFFCIDToGID(sfnt.Tables["CFF "])
	}
	if len(cidToGID) > 0 {
		for id := range usage.Glyphs {
			if id < sfnt.NumGlyphs() {
				glyphs[id] = true
			}
			if gid, ok := cidToGID[id]; ok && gid < sfnt.NumGlyphs() {
				glyphs[gid] = true
			} else if id >= sfnt.NumGlyphs() {
				glyphs[id] = true
			}
		}
	} else {
		maps.Copy(glyphs, usage.Glyphs)
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
	canCompact := len(usage.Glyphs) == 0 && sfnt.Tables["GSUB"] == nil && sfnt.Tables["GPOS"] == nil && sfnt.Tables["GDEF"] == nil && sfnt.Tables["kern"] == nil && sfnt.Tables["MATH"] == nil
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
		dropTransientSubsetTables(tables)
		best, err := SerializeOTF(tables)
		if err != nil || len(best) >= size {
			best = nil
		}
		if canCompact {
			if compact, ids, err := compactCFFGlyphs(cff, glyphs); err == nil {
				if candidate, err := compactCFFTables(sfnt, compact, ids, mapping, variations); err == nil && len(candidate) < size && (best == nil || len(candidate) < len(best)) {
					best = candidate
				}
			}
		}
		return best
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
	if glyfSize == 0 && hasBitmapTables(sfnt.Tables) {
		return nil
	}
	locaFormat := uint16(1)
	if glyfSize <= 0x1fffe {
		locaFormat = 0
	}
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
	binary.BigEndian.PutUint16(tables["head"][50:], locaFormat)
	clear(tables["head"][8:12])
	dropTransientSubsetTables(tables)
	best, err := SerializeOTF(tables)
	if err != nil || len(best) >= size {
		best = nil
	}
	if canCompact {
		if candidate, err := subsetCompactSFNTFont(sfnt, slices.Sorted(maps.Keys(glyphs)), mapping, variations); err == nil && len(candidate) > 0 && len(candidate) < size && (best == nil || len(candidate) < len(best)) {
			best = candidate
		}
	}
	return best
}

// retainSFNTSubstitutions 按当前字形集合做GSUB迭代不动点闭包，仅保留可达替换结果
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
	var active []tables.GSUBLookup
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
			switch sub := subtable.(type) {
			case tables.SingleSubs:
				switch sub.Data.(type) {
				case tables.SingleSubstData1, tables.SingleSubstData2:
					active = append(active, sub)
				default:
					return false
				}
			case tables.MultipleSubs, tables.AlternateSubs, tables.LigatureSubs, tables.ReverseChainSingleSubs:
				active = append(active, sub)
			case tables.ContextualSubs, tables.ChainedContextualSubs:
			default:
				return false
			}
		}
	}
	if len(active) == 0 {
		return true
	}
	all := make([]uint16, 0, len(glyphs))
	for id, retained := range glyphs {
		if retained && id < count {
			all = append(all, id)
		}
	}
	frontier := all
	for len(frontier) > 0 {
		var next []uint16
		add := func(id tables.GlyphID) bool {
			if uint16(id) >= count {
				return false
			}
			if !glyphs[uint16(id)] {
				glyphs[uint16(id)] = true
				next = append(next, uint16(id))
			}
			return true
		}
		for _, subtable := range active {
			switch sub := subtable.(type) {
			case tables.SingleSubs:
				switch data := sub.Data.(type) {
				case tables.SingleSubstData1:
					for _, id := range frontier {
						if _, ok := data.Coverage.Index(tables.GlyphID(id)); ok {
							if !add(tables.GlyphID(id + uint16(data.DeltaGlyphID))) {
								return false
							}
						}
					}
				case tables.SingleSubstData2:
					for _, id := range frontier {
						if idx, ok := data.Coverage.Index(tables.GlyphID(id)); ok {
							if idx < 0 || idx >= len(data.SubstituteGlyphIDs) || !add(data.SubstituteGlyphIDs[idx]) {
								return false
							}
						}
					}
				}
			case tables.MultipleSubs:
				for _, id := range frontier {
					if idx, ok := sub.Coverage.Index(tables.GlyphID(id)); ok {
						if idx < 0 || idx >= len(sub.Sequences) {
							return false
						}
						for _, out := range sub.Sequences[idx].SubstituteGlyphIDs {
							if !add(out) {
								return false
							}
						}
					}
				}
			case tables.AlternateSubs:
				for _, id := range frontier {
					if idx, ok := sub.Coverage.Index(tables.GlyphID(id)); ok {
						if idx < 0 || idx >= len(sub.AlternateSets) {
							return false
						}
						for _, out := range sub.AlternateSets[idx].AlternateGlyphIDs {
							if !add(out) {
								return false
							}
						}
					}
				}
			case tables.ReverseChainSingleSubs:
				for _, id := range frontier {
					if idx, ok := sub.Cov().Index(tables.GlyphID(id)); ok {
						if idx < 0 || idx >= len(sub.SubstituteGlyphIDs) || !add(sub.SubstituteGlyphIDs[idx]) {
							return false
						}
					}
				}
			case tables.LigatureSubs:
				for _, id := range all {
					if idx, ok := sub.Coverage.Index(tables.GlyphID(id)); ok {
						if idx < 0 || idx >= len(sub.LigatureSets) {
							return false
						}
						for _, ligature := range sub.LigatureSets[idx].Ligatures {
							matched := true
							for _, comp := range ligature.ComponentGlyphIDs {
								if !glyphs[uint16(comp)] {
									matched = false
									break
								}
							}
							if matched && !add(ligature.LigatureGlyph) {
								return false
							}
						}
					}
				}
			}
		}
		all = append(all, next...)
		frontier = next
	}
	return true
}

// buildCompactCmap 按重排后的字形编号构建字符映射及格式14变体子表
// 入参: sfnt 原字体, ids 新编号对应的原编号, remap 原编号到新编号映射, sourceMapping 指定字符映射(nil时按原映射反查), variations 格式14变体子表
// 返回: []byte 字符映射表, error 变体映射错误
func buildCompactCmap(sfnt *SFNT, ids []uint16, remap map[uint16]uint16, sourceMapping map[rune]uint16, variations []byte) ([]byte, error) {
	mapping := make(map[rune]uint16)
	if sourceMapping == nil {
		for index, gid := range ids {
			for _, char := range sfnt.glyphCharacters(gid) {
				mapping[char] = uint16(index)
			}
		}
	} else {
		for char, gid := range sourceMapping {
			if mapped, ok := remap[gid]; ok && mapped != 0 {
				mapping[char] = mapped
			}
		}
	}
	remappedVariations, ok := remapCmapVariations(variations, remap)
	if !ok {
		return nil, fmt.Errorf("invalid cmap variation glyph remap")
	}
	return buildCmapWithVariations(uint16(len(ids)), mapping, remappedVariations), nil
}

// subsetCompactSFNTFont 重排静态TrueType字形，同步复合引用、字符映射、格式14变体和度量
// 入参: sfnt 字体, glyphs 使用字形, sourceMapping 指定字符映射(nil时按原映射反查), variations 格式14变体子表
// 返回: []byte 紧凑字体子集, error 字形数据错误
func subsetCompactSFNTFont(sfnt *SFNT, glyphs []uint16, sourceMapping map[rune]uint16, variations []byte) ([]byte, error) {
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
	var glyf, hmtx, vmtx []byte
	offsets := make([]int, len(ids)+1)
	for idx, id := range ids {
		offsets[idx] = len(glyf)
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
	}
	offsets[len(ids)] = len(glyf)
	if len(glyf) == 0 && hasBitmapTables(sfnt.Tables) {
		return nil, nil
	}
	cmap, err := buildCompactCmap(sfnt, ids, remap, sourceMapping, variations)
	if err != nil {
		return nil, err
	}
	locaFormat := uint16(1)
	if len(glyf) <= 0x1fffe {
		locaFormat = 0
	}
	loca := make([]byte, 0, len(offsets)*(2+2*int(locaFormat)))
	for _, off := range offsets {
		if locaFormat == 0 {
			loca = binary.BigEndian.AppendUint16(loca, uint16(off/2))
		} else {
			loca = binary.BigEndian.AppendUint32(loca, uint32(off))
		}
	}
	table["glyf"], table["loca"], table["hmtx"], table["cmap"] = glyf, loca, hmtx, cmap
	binary.BigEndian.PutUint16(table["head"][50:], locaFormat)
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
