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
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

// pdfImportedFont 保存单次导入共用的字体资源、度量及源字形映射，不保存文档标识
type pdfImportedFont struct {
	resource    *FontResource
	checksum    [32]byte
	metrics     FontMetrics
	type1Glyphs map[string]uint16
	repairLimit uint16
}

// importedFont 按原PDF字体复用包装和解析结果，失败结果不进入缓存
// 入参: source 源字体
// 返回: *pdfImportedFont 只读导入字体, error 字体程序或后端错误
func (p *pdfImporter) importedFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	if cached := p.fonts[source]; cached != nil {
		return cached, nil
	}
	var type1 *type1Program
	result := &pdfImportedFont{}
	if source.ProgramType == "FontFile" {
		parsed, err := parseType1Program(source.Program)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
		}
		type1 = &parsed
		result.type1Glyphs = parsed.glyphIDs()
	}
	program, limit, err := pdfFontProgram(source, type1)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
	}
	if p.editor.backends.FontResources == nil {
		return nil, fmt.Errorf("PDF font %s resource: font resources: %w", source.Name, ErrBackendUnavailable)
	}
	result.resource, err = p.editor.backends.FontResources.OpenFontResource(FontFile{Name: source.Name + ".ttf", Data: program}, 0)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s resource: %w", source.Name, err)
	}
	if p.editor.backends.Fonts == nil {
		return nil, fmt.Errorf("PDF font backend unavailable")
	}
	result.metrics, err = p.editor.backends.Fonts.OpenFont(result.resource.Data)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s metrics: %w", source.Name, err)
	}
	if _, ok := result.metrics.(FontOutlines); !ok {
		return nil, fmt.Errorf("PDF font backend does not provide glyph outlines")
	}
	result.checksum, result.repairLimit = sha256.Sum256(result.resource.Data), limit
	if p.fonts == nil {
		p.fonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	p.fonts[source] = result
	return result, nil
}

// pdfFontProgram 为PDF子集字体补齐封装表，保留字形轮廓和编号
// 复合字体的重复映射区段按PDF字符映射重建，不改变CID对应的字形
// 入参: source PDF字体, type1 已解析的Type1程序，nil时按需解析
// 返回: []byte 封装后的字体数据, uint16 缺失度量前的完整字形数, error 错误信息
func pdfFontProgram(source *pdfgo.Font, type1 *type1Program) ([]byte, uint16, error) {
	program := source.Program
	if source.ProgramType == "FontFile" {
		if type1 == nil {
			parsed, err := parseType1Program(program)
			if err != nil {
				return nil, 0, err
			}
			type1 = &parsed
		}
		var err error
		program, err = type1.toCFF(pdfFontIdentity(source))
		if err != nil {
			return nil, 0, err
		}
	}
	bareCFF := len(program) >= 4 && program[0] == 1 && program[1] == 0 && program[2] >= 4 && program[3] >= 1 && program[3] <= 4
	if source.ProgramType == "Type1C" || source.ProgramType == "CIDFontType0C" || bareCFF {
		var err error
		program, _, err = wrapCFFToOTF(program)
		if err != nil {
			return nil, 0, err
		}
	}
	tables, err := fontFileTables(program, 0)
	if err != nil {
		return nil, 0, err
	}
	if len(tables["head"]) < 54 || len(tables["maxp"]) < 6 || len(tables["hhea"]) < 36 || len(tables["hmtx"]) == 0 {
		return nil, 0, fmt.Errorf("embedded PDF font lacks required metrics")
	}
	count := binary.BigEndian.Uint16(tables["maxp"][4:6])
	if count == 0 {
		return nil, 0, fmt.Errorf("embedded PDF font has no glyphs")
	}
	changed := false
	var repairedLimit uint16
	metrics := int(binary.BigEndian.Uint16(tables["hhea"][34:36]))
	if metrics == 0 || metrics > int(count) {
		return nil, 0, fmt.Errorf("invalid embedded PDF font metric count")
	}
	metricLength := 4*metrics + 2*(int(count)-metrics)
	if len(tables["hmtx"]) < metricLength {
		if len(tables["hmtx"]) < 4*metrics || len(tables["hmtx"])%2 != 0 {
			return nil, 0, fmt.Errorf("incomplete embedded PDF font metrics: have %d, need %d", len(tables["hmtx"]), metricLength)
		}
		limit := metrics + (len(tables["hmtx"])-4*metrics)/2
		missing, err := pdfMissingLeftBearings(tables, limit, int(count))
		if err != nil {
			return nil, 0, err
		}
		tables["hmtx"] = append(bytes.Clone(tables["hmtx"]), missing...)
		repairedLimit = uint16(limit)
		changed = true
	}
	if len(tables["hmtx"]) > metricLength {
		tables["hmtx"] = tables["hmtx"][:metricLength]
		changed = true
	}
	invalidCmap := false
	if source.Subtype == "TrueType" || source.Subtype == "Type0" {
		mapping := parseCmapMappings(tables["cmap"])
		for char, glyph := range mapping {
			if glyph >= count {
				delete(mapping, char)
				invalidCmap = true
			}
		}
		if invalidCmap && source.Subtype == "TrueType" {
			tables["cmap"] = buildCmapTable(count, mapping)
			changed = true
		}
	}
	if len(tables["cmap"]) == 0 || source.Subtype == "Type0" && (invalidCmap || pdfCmapOverlaps(tables["cmap"])) {
		mapping := map[rune]uint16{}
		for _, code := range slices.Sorted(maps.Keys(source.Unicode)) {
			glyphs, err := source.Decode([]byte(code))
			if err != nil {
				return nil, 0, err
			}
			if len(glyphs) != 1 || !glyphs[0].HasID {
				return nil, 0, &pdfgo.UnsupportedError{Feature: "font without explicit glyph mapping"}
			}
			glyph := glyphs[0]
			if glyph.ID >= count {
				return nil, 0, fmt.Errorf("PDF font glyph %d exceeds glyph count %d", glyph.ID, count)
			}
			if utf8.RuneCountInString(glyph.Text) == 1 {
				char, _ := utf8.DecodeRuneInString(glyph.Text)
				if _, exists := mapping[char]; !exists {
					mapping[char] = glyph.ID
				}
			}
		}
		addPackedGlyphMapping(mapping, count)
		tables["cmap"] = buildCmapTable(count, mapping)
		changed = true
	}
	if len(fontNamesFromTable(tables["name"])) == 0 {
		fontName := pdfFontIdentity(source)
		name := utf16.Encode([]rune(fontName))
		if len(name) > 32767 {
			return nil, 0, fmt.Errorf("PDF font name exceeds name table capacity")
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
		tables["post"] = buildPostTable()
		changed = true
	}
	if len(tables["OS/2"]) == 0 {
		hhea := tables["hhea"]
		tables["OS/2"] = buildOS2TableWithMetrics(int16(binary.BigEndian.Uint16(hhea[4:6])), int16(binary.BigEndian.Uint16(hhea[6:8])))
		changed = true
	}
	for _, tag := range []string{"cvt ", "fpgm", "prep"} {
		if data, ok := tables[tag]; ok && len(data) == 0 {
			delete(tables, tag)
			changed = true
		}
	}
	if !changed && !pdfSFNTMissingPadding(program) {
		return program, repairedLimit, nil
	}
	result, err := serializeOTF(tables)
	return result, repairedLimit, err
}

// pdfFontIdentity 返回字体名称或由原始程序生成的稳定封装标识
// 入参: source PDF字体
// 返回: string 字体标识
func pdfFontIdentity(source *pdfgo.Font) string {
	if source.Name != "" {
		return source.Name
	}
	checksum := sha256.Sum256(source.Program)
	return fmt.Sprintf("PDF-%x", checksum[:28])
}

// pdfCmapOverlaps 检查格式4字符映射是否包含交叠区段
// 入参: data cmap表数据
// 返回: bool 是否存在交叠
func pdfCmapOverlaps(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	if count > (len(data)-4)/8 {
		return false
	}
	for index := range count {
		offset := uint64(binary.BigEndian.Uint32(data[8+8*index:]))
		if offset > uint64(len(data)) || uint64(len(data))-offset < 16 {
			continue
		}
		sub := data[int(offset):]
		if binary.BigEndian.Uint16(sub) != 4 {
			continue
		}
		length := int(binary.BigEndian.Uint16(sub[2:]))
		segments := int(binary.BigEndian.Uint16(sub[6:])) / 2
		if length > len(sub) || 16+8*segments > length {
			continue
		}
		for i := 1; i < segments; i++ {
			previous := binary.BigEndian.Uint16(sub[14+2*(i-1):])
			start := binary.BigEndian.Uint16(sub[16+2*segments+2*i:])
			if start <= previous {
				return true
			}
		}
	}
	return false
}

// pdfSFNTMissingPadding 判断字体表目录是否引用了未写入的末尾对齐字节
// 入参: program OpenType字体数据
// 返回: bool 是否需要重新封装
func pdfSFNTMissingPadding(program []byte) bool {
	if len(program) < 12 {
		return true
	}
	count := int(binary.BigEndian.Uint16(program[4:6]))
	if count > (len(program)-12)/16 {
		return true
	}
	for index := range count {
		record := program[12+16*index:]
		offset := int(binary.BigEndian.Uint32(record[8:12]))
		length := int(binary.BigEndian.Uint32(record[12:16]))
		if offset > len(program) || length > len(program)-offset || (4-length%4)%4 > len(program)-offset-length {
			return true
		}
	}
	return false
}

// pdfMissingLeftBearings 从轮廓边界恢复缺失的尾部左侧边距
// 入参: tables 字体表, start 首个缺失字形, count 字形总数
// 返回: []byte 补齐的hmtx数据, error 无法读取轮廓
func pdfMissingLeftBearings(tables map[string][]byte, start, count int) ([]byte, error) {
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
	location := func(index int) int {
		if format == 0 {
			return int(binary.BigEndian.Uint16(loca[index*2:])) * 2
		}
		return int(binary.BigEndian.Uint32(loca[index*4:]))
	}
	result := make([]byte, 2*(count-start))
	for index := start; index < count; index++ {
		from, to := location(index), location(index+1)
		if from > to || to > len(glyf) {
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
