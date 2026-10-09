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

// pdfFontRepair 保存字体封装修复范围，不混用字形度量与位置索引
type pdfFontRepair struct {
	metricsLimit uint16
	locations    bool
}

// embeddedTextFont 匹配同名内嵌字体，仅复用唯一且覆盖全部字符的字形程序
// 入参: source 外部字体, glyphs 当前文字字形
// 返回: *pdfImportedFont 可复用字体，无可靠匹配时为空
func (p *pdfImporter) embeddedTextFont(source *pdfgo.Font, glyphs []pdfgo.Glyph) *pdfImportedFont {
	name := pdfEmbeddedFontName(source.Name)
	if name == "" || len(glyphs) == 0 {
		return nil
	}
	var matched *pdfImportedFont
	for _, imported := range p.fonts {
		if pdfEmbeddedFontName(imported.resource.Font.FontName) != name {
			continue
		}
		if matched != nil && matched.checksum != imported.checksum {
			return nil
		}
		matched = imported
	}
	if matched == nil {
		return nil
	}
	for _, glyph := range glyphs {
		if glyph.Name == ".notdef" || utf8.RuneCountInString(glyph.Text) != 1 {
			return nil
		}
		char, _ := utf8.DecodeRuneInString(glyph.Text)
		id := matched.metrics.GlyphIndex(char)
		if id == 0 || id >= matched.metrics.NumGlyphs() || matched.repairLimit != 0 && id >= matched.repairLimit {
			return nil
		}
	}
	return matched
}

// pdfEmbeddedFontName 去除标准六位大写子集前缀，保留字体名称和样式后缀
// 入参: name PDF或内嵌字体名称
// 返回: string 原字体名称
func pdfEmbeddedFontName(name string) string {
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
	program, repair, err := pdfFontProgram(source, type1)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
	}
	if repair.locations && p.warning == nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, errTTInvalidLocations)
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
	result.checksum, result.repairLimit = sha256.Sum256(result.resource.Data), repair.metricsLimit
	if repair.locations {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s has invalid glyph locations; index rebuilt with original glyph IDs", source.Name)})
	}
	if p.fonts == nil {
		p.fonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	p.fonts[source] = result
	return result, nil
}

// symbolFont 加载标准符号字体，保留原字形编号和源字形名称
// 入参: source 未嵌入的PDF字体
// 返回: *pdfImportedFont 可用的符号字体，无匹配时为空, error 字体或取消错误
func (p *pdfImporter) symbolFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	name := pdfEmbeddedFontName(source.Name)
	if source.Subtype != "Type1" || name != "Symbol" && name != "ZapfDingbats" {
		return nil, nil
	}
	if imported, ok := p.symbolFonts[source]; ok {
		return imported, nil
	}
	if p.symbolFonts == nil {
		p.symbolFonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	definition := &Font{ID: name, FontName: name, FamilyName: name}
	reader := &Reader{doc: &Document{}, fontResourcesRead: true, fontCache: map[string]*Font{name: definition}}
	resolved, err := p.editor.newRenderer(reader).ResolveFont(name, true)
	if err != nil || len(resolved.Data) == 0 || p.editor.backends.FontResources == nil {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	program, names, err := symbolFontProgram(p.ctx, resolved.Data, name)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	resource, err := p.editor.backends.FontResources.OpenFontResource(FontFile{Name: name + ".ttf", Data: program}, 0)
	if err != nil {
		return nil, err
	}
	metrics, err := p.editor.backends.Fonts.OpenFont(resource.Data)
	if err != nil {
		return nil, err
	}
	if _, ok := metrics.(FontOutlines); !ok {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	result := &pdfImportedFont{resource: resource, checksum: sha256.Sum256(resource.Data), metrics: metrics, type1Glyphs: names}
	p.symbolFonts[source] = result
	return result, nil
}

// pdfFontProgram 为PDF子集字体补齐封装表，保留字形轮廓和编号
// 无效字符表按PDF已解码字形重建，不改变字符对应的字形编号
// 入参: source PDF字体, type1 已解析的Type1程序，nil时按需解析
// 返回: []byte 封装后的字体数据, pdfFontRepair 字体修复范围, error 错误信息
func pdfFontProgram(source *pdfgo.Font, type1 *type1Program) ([]byte, pdfFontRepair, error) {
	var repair pdfFontRepair
	program := source.Program
	if source.ProgramType == "FontFile" {
		if type1 == nil {
			parsed, err := parseType1Program(program)
			if err != nil {
				return nil, repair, err
			}
			type1 = &parsed
		}
		var err error
		program, err = type1.toCFF(pdfFontIdentity(source))
		if err != nil {
			return nil, repair, err
		}
	}
	bareCFF := len(program) >= 4 && program[0] == 1 && program[1] == 0 && program[2] >= 4 && program[3] >= 1 && program[3] <= 4
	if source.ProgramType == "Type1C" || source.ProgramType == "CIDFontType0C" || bareCFF {
		var err error
		program, _, err = wrapCFFToOTF(program)
		if err != nil {
			return nil, repair, err
		}
	}
	tables, err := fontFileTables(program, 0)
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
	repair.locations, err = normalizeTrueTypeLocations(tables)
	if err != nil {
		return nil, repair, err
	}
	changed := repair.locations
	if head := tables["head"]; len(head) == 56 && binary.BigEndian.Uint32(head[:4]) == 0x00010000 && head[54] == 0 && head[55] == 0 {
		tables["head"] = head[:54]
		changed = true
	}
	if cff := tables["CFF "]; len(cff) != 0 {
		sanitized, err := sanitizeCFF(cff)
		if err != nil {
			return nil, repair, err
		}
		normalized, err := normalizeCFFCharstringsAt(sanitized, binary.BigEndian.Uint16(tables["head"][18:20]))
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
		missing, err := pdfMissingLeftBearings(tables, limit, int(count))
		if err != nil {
			return nil, repair, err
		}
		tables["hmtx"] = append(bytes.Clone(tables["hmtx"]), missing...)
		repair.metricsLimit = uint16(limit)
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
	if len(tables["cmap"]) == 0 || source.Subtype == "Type0" && invalidCmap || (source.Subtype == "Type0" || source.Subtype == "TrueType") && pdfCmapNeedsRebuild(tables["cmap"]) {
		mapping := map[rune]uint16{}
		var codes []string
		if source.Subtype == "TrueType" {
			codes = make([]string, 256)
			for code := range codes {
				codes[code] = string([]byte{byte(code)})
			}
		} else {
			codes = slices.Sorted(maps.Keys(source.Unicode))
		}
		for _, code := range codes {
			glyphs, err := source.Decode([]byte(code))
			if err != nil {
				return nil, repair, err
			}
			if len(glyphs) != 1 || !glyphs[0].HasID {
				return nil, repair, &pdfgo.UnsupportedError{Feature: "font without explicit glyph mapping"}
			}
			glyph := glyphs[0]
			if glyph.ID >= count {
				return nil, repair, fmt.Errorf("PDF font glyph %d exceeds glyph count %d", glyph.ID, count)
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
		return program, repair, nil
	}
	result, err := serializeOTF(tables)
	return result, repair, err
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

// pdfCmapNeedsRebuild 检查字体封装字符表是否存在结构错误或交叠区段
// 入参: data cmap表数据
// 返回: bool 是否需要按PDF显式字形编号重建
func pdfCmapNeedsRebuild(data []byte) bool {
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
