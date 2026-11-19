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
	"slices"
)

// compactCFFGlyphs 重排已裁剪字体的字形，同步字符集、编码和字体字典归属
// 入参: data 已裁剪的CFF数据, keep 保留的原字形编号
// 返回: []byte 紧凑CFF数据, []uint16 新编号对应的原编号, error 结构或编码错误
func compactCFFGlyphs(data []byte, keep map[uint16]bool) ([]byte, []uint16, error) {
	ids := []uint16{0}
	for gid, used := range keep {
		if used && gid != 0 {
			ids = append(ids, gid)
		}
	}
	slices.Sort(ids)
	font, err := readType2Font(data)
	if err == nil {
		sids, _, _, _, ok := getCFFCharsetInfo(data, len(font.chars))
		if !ok {
			return nil, nil, fmt.Errorf("invalid CFF charset")
		}
		_, end, _ := readType2Index(data, int(data[2]))
		tops, _, _ := readType2Index(data, end)
		dict, _ := readCFFDict(tops[0])
		encoding, err := cffSubsetEncoding(data, dict)
		if err != nil {
			return nil, nil, err
		}
		if encoding != nil {
			codes, err := readCFFSubsetEncoding(encoding, sids)
			if err != nil {
				return nil, nil, err
			}
			ordered := []uint16{0}
			seen := map[uint16]bool{0: true}
			for _, gid := range codes {
				if keep[gid] && !seen[gid] {
					ordered = append(ordered, gid)
					seen[gid] = true
				}
			}
			for _, gid := range ids {
				if !seen[gid] {
					ordered = append(ordered, gid)
				}
			}
			ids = ordered
		}
		chars := make([][]byte, len(ids))
		for index, gid := range ids {
			if int(gid) >= len(font.chars) {
				return nil, nil, fmt.Errorf("CFF glyph index out of range")
			}
			chars[index] = font.chars[gid]
		}
		result, err := rebuildNamedCFF(data, font, chars, ids)
		return result, ids, err
	}
	cid, err := readCIDSubsetFont(data)
	if err != nil {
		return nil, nil, err
	}
	cids, _, _, _, ok := getCFFCharsetInfo(data, len(cid.selects))
	if !ok {
		return nil, nil, fmt.Errorf("invalid CID CFF charset")
	}
	chars := make([][]byte, len(ids))
	selected := make([]int, len(ids))
	selects := make([]int, len(ids))
	dicts, private, fonts := []cffDict{}, []cffDict{}, []*type2Font{}
	remap := make(map[int]int)
	for index, gid := range ids {
		if int(gid) >= len(cid.selects) {
			return nil, nil, fmt.Errorf("CFF glyph index out of range")
		}
		fd := cid.selects[gid]
		mapped, exists := remap[fd]
		if !exists {
			mapped = len(fonts)
			remap[fd] = mapped
			dicts = append(dicts, cid.dicts[fd])
			private = append(private, cid.private[fd])
			fonts = append(fonts, cid.fonts[fd])
		}
		chars[index], selected[index], selects[index] = cid.fonts[fd].chars[gid], cids[gid], mapped
	}
	cid.dicts, cid.private, cid.fonts = dicts, private, fonts
	cid.charset, cid.selects = encodeCFFSubsetCharset(selected), selects
	result, err := cid.write(chars)
	return result, ids, err
}

// compactCFFTables 重建按字形编号索引的度量、字符映射和格式14变体子表
// 入参: font 原字体, cff 紧凑CFF数据, ids 新编号对应的原编号, sourceMapping 指定字符映射(nil时按原映射反查), variations 格式14变体子表
// 返回: []byte 独立OpenType字体, error 封装错误
func compactCFFTables(font *SFNT, cff []byte, ids []uint16, sourceMapping map[rune]uint16, variations []byte) ([]byte, error) {
	tables := make(map[string][]byte)
	for _, tag := range []string{"head", "hhea", "maxp", "OS/2", "post", "name", "vhea", "gasp"} {
		if data := font.Tables[tag]; data != nil {
			tables[tag] = bytes.Clone(data)
		}
	}
	var hmtx, vmtx []byte
	remap := make(map[uint16]uint16, len(ids))
	for index, gid := range ids {
		remap[gid] = uint16(index)
		hmtx = binary.BigEndian.AppendUint16(hmtx, font.GlyphAdvance(gid))
		hmtx = binary.BigEndian.AppendUint16(hmtx, uint16(font.sideBearing(gid, false)))
		if font.vertical != 0 {
			vmtx = binary.BigEndian.AppendUint16(vmtx, font.GlyphVerticalAdvance(gid))
			vmtx = binary.BigEndian.AppendUint16(vmtx, uint16(font.sideBearing(gid, true)))
		}
	}
	cmap, err := buildCompactCmap(font, ids, remap, sourceMapping, variations)
	if err != nil {
		return nil, err
	}
	tables["CFF "], tables["hmtx"], tables["cmap"] = cff, hmtx, cmap
	binary.BigEndian.PutUint16(tables["hhea"][34:], uint16(len(ids)))
	binary.BigEndian.PutUint16(tables["maxp"][4:], uint16(len(ids)))
	if len(tables["post"]) >= 32 {
		tables["post"] = tables["post"][:32]
		binary.BigEndian.PutUint32(tables["post"], 0x30000)
	}
	if font.vertical != 0 {
		tables["vmtx"] = vmtx
		binary.BigEndian.PutUint16(tables["vhea"][34:], uint16(len(ids)))
	}
	if data := font.Tables["VORG"]; data != nil {
		var err error
		tables["VORG"], err = compactCFFVerticalOrigins(data, font.NumGlyphs(), ids)
		if err != nil {
			return nil, err
		}
	}
	return SerializeOTF(tables)
}

// compactCFFVerticalOrigins 重排CFF字体的逐字形纵向原点，保留默认原点
// 入参: data VORG表, count 原字形数, ids 新编号对应的原编号
// 返回: []byte 重建的VORG表, error 表结构或记录顺序错误
func compactCFFVerticalOrigins(data []byte, count uint16, ids []uint16) ([]byte, error) {
	if len(data) < 8 || binary.BigEndian.Uint32(data) != 0x10000 {
		return nil, fmt.Errorf("invalid CFF vertical origin header")
	}
	length := int(binary.BigEndian.Uint16(data[6:]))
	if length > (len(data)-8)/4 {
		return nil, fmt.Errorf("truncated CFF vertical origins")
	}
	origins := make(map[uint16]uint16, length)
	previous := -1
	for index := range length {
		pos := 8 + index*4
		gid := binary.BigEndian.Uint16(data[pos:])
		if gid >= count || int(gid) <= previous {
			return nil, fmt.Errorf("invalid CFF vertical origin glyph")
		}
		origins[gid] = binary.BigEndian.Uint16(data[pos+2:])
		previous = int(gid)
	}
	result := bytes.Clone(data[:8])
	for index, gid := range ids {
		if origin, ok := origins[gid]; ok {
			result = binary.BigEndian.AppendUint16(result, uint16(index))
			result = binary.BigEndian.AppendUint16(result, origin)
		}
	}
	binary.BigEndian.PutUint16(result[6:], uint16((len(result)-8)/4))
	return result, nil
}

// encodeCFFSubsetCharset 选择最短的标准字符集格式，保持SID或CID及字形顺序
// 入参: ids 按字形编号排列的SID或CID，包含.notdef
// 返回: []byte 字符集数据
func encodeCFFSubsetCharset(ids []int) []byte {
	sizes := [3]int{1 + 2*(len(ids)-1), 1, 1}
	for first := 1; first < len(ids); {
		end := first + 1
		for end < len(ids) && ids[end] == ids[end-1]+1 {
			end++
		}
		sizes[1] += 3 * ((end - first + 255) / 256)
		sizes[2] += 4
		first = end
	}
	format := 0
	for index, size := range sizes {
		if size < sizes[format] {
			format = index
		}
	}
	result := make([]byte, 1, sizes[format])
	result[0] = byte(format)
	for first := 1; first < len(ids); {
		end := first + 1
		if format != 0 {
			limit := len(ids)
			if format == 1 {
				limit = min(limit, first+256)
			}
			for end < limit && ids[end] == ids[end-1]+1 {
				end++
			}
		}
		result = binary.BigEndian.AppendUint16(result, uint16(ids[first]))
		if format == 1 {
			result = append(result, byte(end-first-1))
		} else if format == 2 {
			result = binary.BigEndian.AppendUint16(result, uint16(end-first-1))
		}
		first = end
	}
	return result
}

// readCFFSubsetEncoding 解析自定义编码及补充映射，校验重复编码和字形归属
// 入参: data 编码数据, sids 原字符集
// 返回: [256]uint16 编码对应的字形编号, error 编码结构错误
func readCFFSubsetEncoding(data []byte, sids []int) ([256]uint16, error) {
	var codes [256]uint16
	if len(data) < 2 {
		return codes, fmt.Errorf("truncated CFF encoding")
	}
	pos, gid := 2, 1
	for index := 0; index < int(data[1]); index++ {
		if pos >= len(data) {
			return codes, fmt.Errorf("truncated CFF encoding")
		}
		first, count := int(data[pos]), 1
		pos++
		switch data[0] & 0x7f {
		case 0:
		case 1:
			if pos >= len(data) {
				return codes, fmt.Errorf("truncated CFF encoding range")
			}
			count += int(data[pos])
			pos++
		default:
			return codes, fmt.Errorf("invalid CFF encoding format")
		}
		if first+count > len(codes) || gid+count > len(sids) {
			return codes, fmt.Errorf("CFF encoding range out of bounds")
		}
		for code := first; code < first+count; code++ {
			if codes[code] != 0 {
				return codes, fmt.Errorf("duplicate CFF encoding")
			}
			codes[code] = uint16(gid)
			gid++
		}
	}
	if data[0]&0x80 != 0 {
		if pos >= len(data) {
			return codes, fmt.Errorf("truncated CFF encoding supplements")
		}
		count := int(data[pos])
		pos++
		if count > (len(data)-pos)/3 {
			return codes, fmt.Errorf("truncated CFF encoding supplements")
		}
		for range count {
			code, sid := data[pos], int(binary.BigEndian.Uint16(data[pos+1:]))
			pos += 3
			mapped := slices.Index(sids[:gid], sid)
			if codes[code] != 0 || mapped <= 0 {
				return codes, fmt.Errorf("invalid CFF supplemental glyph")
			}
			codes[code] = uint16(mapped)
		}
	}
	if data[0]&0x7f > 1 || pos != len(data) {
		return codes, fmt.Errorf("invalid CFF encoding length or format")
	}
	return codes, nil
}

// encodeCFFSubsetEncoding 按新字形顺序重建主编码及同字形的补充编码
// 入参: codes 原编码映射, sids 原字符集, ids 新编号对应的原编号
// 返回: []byte 编码数据, error 字形顺序不满足编码约束
func encodeCFFSubsetEncoding(codes [256]uint16, sids []int, ids []uint16) ([]byte, error) {
	var base, supplements []byte
	encodings := make(map[uint16][]byte)
	for code, gid := range codes {
		if gid != 0 {
			encodings[gid] = append(encodings[gid], byte(code))
		}
	}
	unencoded := false
	for _, gid := range ids[1:] {
		values := encodings[gid]
		if len(values) == 0 {
			unencoded = true
		} else if unencoded {
			return nil, fmt.Errorf("CFF encoded glyph follows unencoded glyph")
		} else {
			base = append(base, values[0])
			for _, code := range values[1:] {
				supplements = append(supplements, code)
				supplements = binary.BigEndian.AppendUint16(supplements, uint16(sids[gid]))
			}
		}
	}
	var result []byte
	if len(base) == 256 {
		for index, code := range base {
			if int(code) != index {
				return nil, fmt.Errorf("CFF encoding exceeds format zero capacity")
			}
		}
		result = []byte{1, 1, 0, 255}
	} else {
		result = append([]byte{0, byte(len(base))}, base...)
	}
	if len(supplements) != 0 {
		result[0] |= 0x80
		result = append(result, byte(len(supplements)/3))
		result = append(result, supplements...)
	}
	return result, nil
}
