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

package pdf

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"unicode/utf16"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	"github.com/xiaoqidun/pdfgo"
)

// FontRepair 保存字体封装修复范围，不混用字形度量与位置索引
type FontRepair struct {
	MetricsLimit uint16
	Locations    bool
}

// EmbeddedFontName 去除标准六位大写子集前缀，保留字体名称和样式后缀
// 入参: name PDF或内嵌字体名称
// 返回: string 原字体名称
func EmbeddedFontName(name string) string {
	if len(name) <= 7 || name[6] != '+' {
		return name
	}
	for i := range 6 {
		if name[i] < 'A' || name[i] > 'Z' {
			return name
		}
	}
	return name[7:]
}

// FontProgram 为PDF子集字体补齐封装表，保留字形轮廓和编号
// 无效字符表按PDF已解码字形重建，不改变字符对应的字形编号
// 入参: source PDF字体, type1 已解析的Type1程序，nil时按需解析
// 返回: []byte 封装后的字体数据, FontRepair 字体修复范围, error 错误信息
func FontProgram(source *pdfgo.Font, type1 *basefont.Type1Program) ([]byte, FontRepair, error) {
	var repair FontRepair
	program := source.Program
	if source.ProgramType == "FontFile" {
		if type1 == nil {
			parsed, err := basefont.ParseType1Program(program)
			if err != nil {
				return nil, repair, err
			}
			type1 = &parsed
		}
		var err error
		program, err = type1.ToCFF(fontIdentity(source))
		if err != nil {
			return nil, repair, err
		}
	}
	bareCFF := len(program) >= 4 && program[0] == 1 && program[1] == 0 && program[2] >= 4 && program[3] >= 1 && program[3] <= 4
	if source.ProgramType == "Type1C" || source.ProgramType == "CIDFontType0C" || bareCFF {
		var err error
		program, _, err = basefont.WrapCFFToOTF(program)
		if err != nil {
			return nil, repair, err
		}
	}
	tables, err := basefont.FileTables(program, 0)
	if err != nil {
		return nil, repair, err
	}
	if len(tables["head"]) < 54 || len(tables["maxp"]) < 6 || len(tables["hhea"]) < 36 || len(tables["hmtx"]) == 0 {
		return nil, repair, fmt.Errorf("embedded PDF font lacks required metrics")
	}
	count := binary.BigEndian.Uint16(tables["maxp"][4:6])
	if count == 0 {
		return nil, repair, fmt.Errorf("embedded PDF font has no glyphs")
	}
	repair.Locations, err = basefont.NormalizeTrueTypeLocations(tables)
	if err != nil {
		return nil, repair, err
	}
	changed := repair.Locations
	if os2 := basefont.NormalizeOS2(tables["OS/2"]); len(os2) != len(tables["OS/2"]) {
		tables["OS/2"] = os2
		changed = true
	}
	if head := tables["head"]; len(head) == 56 && binary.BigEndian.Uint32(head[:4]) == 0x00010000 && head[54] == 0 && head[55] == 0 {
		tables["head"] = head[:54]
		changed = true
	}
	if cff := tables["CFF "]; len(cff) != 0 {
		sanitized, err := basefont.SanitizeCFF(cff)
		if err != nil {
			return nil, repair, err
		}
		normalized, err := basefont.NormalizeCFFCharstringsAt(sanitized, binary.BigEndian.Uint16(tables["head"][18:20]))
		if err != nil {
			return nil, repair, err
		}
		if !bytes.Equal(normalized, cff) {
			tables["CFF "] = normalized
			changed = true
		}
	}
	metrics := int(binary.BigEndian.Uint16(tables["hhea"][34:36]))
	if metrics == 0 || metrics > int(count) {
		return nil, repair, fmt.Errorf("invalid embedded PDF font metric count")
	}
	metricLength := 4*metrics + 2*(int(count)-metrics)
	if len(tables["hmtx"]) < metricLength {
		if len(tables["hmtx"]) < 4*metrics || len(tables["hmtx"])%2 != 0 {
			return nil, repair, fmt.Errorf("incomplete embedded PDF font metrics: have %d, need %d", len(tables["hmtx"]), metricLength)
		}
		limit := metrics + (len(tables["hmtx"])-4*metrics)/2
		missing, err := missingLeftBearings(tables, limit, int(count))
		if err != nil {
			return nil, repair, err
		}
		tables["hmtx"] = append(bytes.Clone(tables["hmtx"]), missing...)
		repair.MetricsLimit = uint16(limit)
		changed = true
	}
	if len(tables["hmtx"]) > metricLength {
		tables["hmtx"] = tables["hmtx"][:metricLength]
		changed = true
	}
	invalidCmap := false
	if source.Subtype == "TrueType" || source.Subtype == "Type0" {
		mapping := basefont.ParseCmapMappings(tables["cmap"])
		for char, glyph := range mapping {
			if glyph >= count {
				delete(mapping, char)
				invalidCmap = true
			}
		}
		if invalidCmap && source.Subtype == "TrueType" {
			tables["cmap"] = basefont.BuildCmapTable(count, mapping)
			changed = true
		}
	}
	if len(tables["cmap"]) == 0 || source.Subtype == "Type0" && invalidCmap || (source.Subtype == "Type0" || source.Subtype == "TrueType") && cmapNeedsRebuild(tables["cmap"]) {
		mapping, err := source.GlyphMap()
		if err != nil {
			return nil, repair, err
		}
		for _, glyph := range mapping {
			if glyph >= count {
				return nil, repair, fmt.Errorf("PDF font glyph %d exceeds glyph count %d", glyph, count)
			}
		}
		basefont.AddPackedGlyphMapping(mapping, count)
		tables["cmap"] = basefont.BuildCmapTable(count, mapping)
		changed = true
	}
	if len(basefont.NamesFromTable(tables["name"])) == 0 {
		fontName := fontIdentity(source)
		name := utf16.Encode([]rune(fontName))
		if len(name) > 32767 {
			return nil, repair, fmt.Errorf("PDF font name exceeds name table capacity")
		}
		data := make([]byte, 42+len(name)*2)
		binary.BigEndian.PutUint16(data[2:], 3)
		binary.BigEndian.PutUint16(data[4:], 42)
		for n, id := range []uint16{1, 4, 6} {
			record := data[6+n*12:]
			binary.BigEndian.PutUint16(record, 3)
			binary.BigEndian.PutUint16(record[2:], 1)
			binary.BigEndian.PutUint16(record[4:], 0x409)
			binary.BigEndian.PutUint16(record[6:], id)
			binary.BigEndian.PutUint16(record[8:], uint16(len(name)*2))
		}
		for n, v := range name {
			binary.BigEndian.PutUint16(data[42+n*2:], v)
		}
		tables["name"] = data
		changed = true
	}
	if len(tables["post"]) == 0 {
		tables["post"] = basefont.BuildPostTable()
		changed = true
	}
	if len(tables["OS/2"]) == 0 {
		hhea := tables["hhea"]
		tables["OS/2"] = basefont.BuildOS2TableWithMetrics(int16(binary.BigEndian.Uint16(hhea[4:6])), int16(binary.BigEndian.Uint16(hhea[6:8])))
		changed = true
	}
	for _, tag := range []string{"cvt ", "fpgm", "prep"} {
		if data, ok := tables[tag]; ok && len(data) == 0 {
			delete(tables, tag)
			changed = true
		}
	}
	if !changed && !sfntMissingPadding(program) {
		return program, repair, nil
	}
	result, err := basefont.SerializeOTF(tables)
	return result, repair, err
}

// fontIdentity 返回字体名称或由原始程序生成的稳定封装标识
// 入参: source PDF字体
// 返回: string 字体标识
func fontIdentity(source *pdfgo.Font) string {
	if source.Name != "" {
		return source.Name
	}
	checksum := sha256.Sum256(source.Program)
	return fmt.Sprintf("PDF-%x", checksum[:28])
}

// cmapNeedsRebuild 检查字体封装字符表是否存在结构错误或交叠区段
// 入参: data cmap表数据
// 返回: bool 是否需要按PDF显式字形编号重建
func cmapNeedsRebuild(data []byte) bool {
	if len(data) < 4 || binary.BigEndian.Uint16(data) != 0 {
		return true
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	if count == 0 || count > (len(data)-4)/8 {
		return true
	}
	for index := range count {
		offset := uint64(binary.BigEndian.Uint32(data[8+8*index:]))
		if offset < uint64(4+8*count) || offset > uint64(len(data)) || uint64(len(data))-offset < 2 {
			return true
		}
		sub := data[int(offset):]
		if binary.BigEndian.Uint16(sub) == 6 {
			if len(sub) < 10 {
				return true
			}
			length := int(binary.BigEndian.Uint16(sub[2:]))
			first, glyphs := int(binary.BigEndian.Uint16(sub[6:])), int(binary.BigEndian.Uint16(sub[8:]))
			if length%2 != 0 || length > len(sub) || 10+2*glyphs > length || first+glyphs > 65536 {
				return true
			}
			continue
		}
		if binary.BigEndian.Uint16(sub) != 4 {
			continue
		}
		if len(sub) < 16 {
			return true
		}
		length := int(binary.BigEndian.Uint16(sub[2:]))
		segmentBytes := int(binary.BigEndian.Uint16(sub[6:]))
		segments := segmentBytes / 2
		if segmentBytes == 0 || segmentBytes%2 != 0 || length%2 != 0 || length > len(sub) || 16+8*segments > length {
			return true
		}
		glyphStart := 16 + 8*segments
		previous := -1
		for i := 0; i < segments; i++ {
			end := int(binary.BigEndian.Uint16(sub[14+2*i:]))
			start := int(binary.BigEndian.Uint16(sub[16+2*segments+2*i:]))
			if end < start || start <= previous || i == segments-1 && (start != 65535 || end != 65535) {
				return true
			}
			previous = end
			position := 16 + 6*segments + 2*i
			offset := int(binary.BigEndian.Uint16(sub[position:]))
			if offset != 0 && (offset%2 != 0 || position+offset < glyphStart || position+offset+2*(end-start+1) > length) {
				return true
			}
		}
	}
	return false
}

// sfntMissingPadding 判断字体表目录是否引用了未写入的末尾对齐字节
// 入参: program OpenType字体数据
// 返回: bool 是否需要重新封装
func sfntMissingPadding(program []byte) bool {
	if len(program) < 12 {
		return true
	}
	size := uint64(len(program))
	count := uint64(binary.BigEndian.Uint16(program[4:6]))
	if count > (size-12)/16 {
		return true
	}
	for index := uint64(0); index < count; index++ {
		record := program[12+16*index:]
		offset := uint64(binary.BigEndian.Uint32(record[8:12]))
		length := uint64(binary.BigEndian.Uint32(record[12:16]))
		pad := (4 - length%4) % 4
		if offset > size || length > size-offset || pad > size-offset-length {
			return true
		}
	}
	return false
}

// missingLeftBearings 从轮廓边界恢复缺失的尾部左侧边距
// 入参: tables 字体表, start 首个缺失字形, count 字形总数
// 返回: []byte 补齐的hmtx数据, error 无法读取轮廓
func missingLeftBearings(tables map[string][]byte, start, count int) ([]byte, error) {
	head, loca, glyf := tables["head"], tables["loca"], tables["glyf"]
	if len(head) < 54 || len(glyf) == 0 {
		return nil, fmt.Errorf("embedded PDF font lacks glyph outlines for missing metrics")
	}
	format := int(int16(binary.BigEndian.Uint16(head[50:52])))
	entrySize := 2
	if format == 1 {
		entrySize = 4
	} else if format != 0 {
		return nil, fmt.Errorf("invalid embedded PDF font location format")
	}
	if len(loca) < (count+1)*entrySize {
		return nil, fmt.Errorf("incomplete embedded PDF font glyph locations")
	}
	location := func(index int) uint64 {
		if format == 0 {
			return uint64(binary.BigEndian.Uint16(loca[index*2:])) * 2
		}
		return uint64(binary.BigEndian.Uint32(loca[index*4:]))
	}
	result := make([]byte, 2*(count-start))
	for index := start; index < count; index++ {
		from, to := location(index), location(index+1)
		if from > to || to > uint64(len(glyf)) {
			return nil, fmt.Errorf("invalid embedded PDF font glyph location")
		}
		if from == to {
			continue
		}
		if to-from < 10 {
			return nil, fmt.Errorf("incomplete embedded PDF font glyph outline")
		}
		copy(result[(index-start)*2:], glyf[from+2:from+4])
	}
	return result, nil
}
