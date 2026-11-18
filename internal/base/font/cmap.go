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
	"unicode/utf16"
	"unicode/utf8"
)

// NormalizeCmap 转换扩展字符子表，保留各编码记录及原字形编号
// 入参: data 字符映射表
// 返回: []byte 等价字符映射表, error 子表错误
func NormalizeCmap(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return data, nil
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	if count > (len(data)-4)/8 {
		return nil, fmt.Errorf("invalid cmap encoding records")
	}
	var out []byte
	converted := make(map[uint32]uint32)
	for n := 0; n < count; n++ {
		record := 8 + 8*n
		offset := binary.BigEndian.Uint32(data[record:])
		if uint64(offset)+2 > uint64(len(data)) {
			return nil, fmt.Errorf("invalid cmap subtable offset")
		}
		format := binary.BigEndian.Uint16(data[offset:])
		if format != 2 && format != 8 && format != 10 && format != 13 {
			continue
		}
		if out == nil {
			out = bytes.Clone(data)
		}
		next, ok := converted[offset]
		if !ok {
			var mapping map[rune]uint16
			var err error
			language := uint32(0)
			if format == 2 {
				mapping, err = parseCmapFormat2(data[offset:])
				if err == nil {
					language = uint32(binary.BigEndian.Uint16(data[offset+4:]))
				}
			} else {
				mapping, err = parseCmapExtended(data[offset:])
				if err == nil {
					language = binary.BigEndian.Uint32(data[offset+8:])
				}
			}
			if err != nil {
				return nil, err
			}
			var table []byte
			if language > 65535 {
				table = buildCmapTableFormat12(0, mapping)
			} else {
				table = BuildCmapTable(0, mapping)
			}
			subtable := table[binary.BigEndian.Uint32(table[8:]):]
			if binary.BigEndian.Uint16(subtable) == 12 {
				binary.BigEndian.PutUint32(subtable[8:], language)
			} else {
				binary.BigEndian.PutUint16(subtable[4:], uint16(language))
			}
			if uint64(len(out))+uint64(len(subtable)) > 1<<32-1 {
				return nil, fmt.Errorf("normalized cmap exceeds offset range")
			}
			next = uint32(len(out))
			converted[offset] = next
			out = append(out, subtable...)
		}
		binary.BigEndian.PutUint32(out[record:], next)
	}
	if out == nil {
		return data, nil
	}
	return out, nil
}

// PackedGlyphRune 获取包装字体字符
// 入参: gid 字形ID
// 返回: rune 包装字体字符
func PackedGlyphRune(gid uint16) rune {
	return 0xF0000 + rune(gid)
}

// ParseCmapMappings 解析cmap字符映射
// 入参: data cmap表数据
// 返回: map[rune]uint16 字符到字形映射
func ParseCmapMappings(data []byte) map[rune]uint16 {
	if len(data) < 4 {
		return nil
	}
	numTables := int(binary.BigEndian.Uint16(data[2:4]))
	result := make(map[rune]uint16)
	for i := 0; i < numTables; i++ {
		pos := 4 + i*8
		if pos+8 > len(data) {
			break
		}
		platformID := binary.BigEndian.Uint16(data[pos : pos+2])
		if platformID != 0 && platformID != 3 {
			continue
		}
		offset := uint64(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		if offset+2 > uint64(len(data)) {
			continue
		}
		switch binary.BigEndian.Uint16(data[offset : offset+2]) {
		case 0:
			parseCmapFormat0(data[offset:], result)
		case 4:
			parseCmapFormat4(data[offset:], result)
		case 6:
			parseCmapFormat6(data[offset:], result)
		case 12:
			parseCmapFormat12(data[offset:], result)
		case 2, 8, 10, 13:
			var mapping map[rune]uint16
			var err error
			if binary.BigEndian.Uint16(data[offset:]) == 2 {
				mapping, err = parseCmapFormat2(data[offset:])
			} else {
				mapping, err = parseCmapExtended(data[offset:])
			}
			if err == nil {
				for char, glyph := range mapping {
					result[char] = glyph
				}
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// AddPackedGlyphMapping 添加字形ID私有映射
// 入参: mapping 字符映射, numGlyphs 字形数量
func AddPackedGlyphMapping(mapping map[rune]uint16, numGlyphs uint16) {
	for i := uint16(0); i < numGlyphs; i++ {
		mapping[PackedGlyphRune(i)] = i
	}
}

// BuildCmapTable 构建格式4或12字符映射，省略缺字映射
// 入参: numGlyphs 字形数量, mapping 字符映射
// 返回: []byte cmap表数据
func BuildCmapTable(numGlyphs uint16, mapping map[rune]uint16) []byte {
	if mapping == nil {
		return buildCmapFormat4Sorted(numGlyphs, nil, nil)
	}
	codes, needFormat12 := sortedCmapCodes(mapping)
	if needFormat12 {
		return encodeCmapFormat12(mapping, codes, 0)
	}
	return buildCmapFormat4Sorted(numGlyphs, mapping, codes)
}

// cmapSegment cmap表段结构
// 字段: start 开始字符, end 结束字符
type cmapSegment struct {
	start, end uint16
}

// cmapGroup 格式12连续字符映射区间组
type cmapGroup struct {
	startChar uint32
	endChar   uint32
	startGID  uint32
}

// parseCmapExtended 读取混合字符、扩展数组及常量分组，限制到有效Unicode范围
// 入参: data 格式8、10或13子表
// 返回: map[rune]uint16 独立字符映射, error 结构或映射错误
func parseCmapExtended(data []byte) (map[rune]uint16, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("truncated extended cmap")
	}
	length := uint64(binary.BigEndian.Uint32(data[4:]))
	if length < 12 || length > uint64(len(data)) {
		return nil, fmt.Errorf("invalid extended cmap length")
	}
	data = data[:int(length)]
	format := binary.BigEndian.Uint16(data)
	result := make(map[rune]uint16)
	add := func(code uint32, glyph uint16) error {
		char := rune(code)
		if code > utf8.MaxRune {
			high, low := rune(code>>16), rune(code&65535)
			if format != 8 || high < 0xd800 || high > 0xdbff || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("invalid extended cmap character")
			}
			char = utf16.DecodeRune(high, low)
		}
		if glyph != 0 && utf8.ValidRune(char) {
			if previous, exists := result[char]; exists && previous != glyph {
				return fmt.Errorf("ambiguous extended cmap character")
			}
			result[char] = glyph
		}
		return nil
	}
	if format == 10 {
		if len(data) < 20 {
			return nil, fmt.Errorf("truncated cmap format 10")
		}
		first, count := uint64(binary.BigEndian.Uint32(data[12:])), uint64(binary.BigEndian.Uint32(data[16:]))
		if count > uint64((len(data)-20)/2) || first+count > utf8.MaxRune+1 {
			return nil, fmt.Errorf("invalid cmap format 10 range")
		}
		for index := uint64(0); index < count; index++ {
			if err := add(uint32(first+index), binary.BigEndian.Uint16(data[20+int(index)*2:])); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	offset := 16
	if format == 8 {
		offset = 8208
	} else if format != 13 {
		return nil, fmt.Errorf("invalid extended cmap format %d", format)
	}
	if len(data) < offset {
		return nil, fmt.Errorf("truncated cmap format %d", format)
	}
	count := uint64(binary.BigEndian.Uint32(data[offset-4:]))
	if count > uint64((len(data)-offset)/12) {
		return nil, fmt.Errorf("invalid extended cmap group count")
	}
	var previous uint32
	for index := uint64(0); index < count; index++ {
		group := data[offset+int(index)*12:]
		first, last, glyph := binary.BigEndian.Uint32(group), binary.BigEndian.Uint32(group[4:]), uint64(binary.BigEndian.Uint32(group[8:]))
		if first > last || index != 0 && first <= previous {
			return nil, fmt.Errorf("invalid extended cmap group order")
		}
		previous = last
		endGlyph := glyph
		if format == 8 {
			endGlyph += uint64(last) - uint64(first)
			if first <= 65535 && last > 65535 {
				return nil, fmt.Errorf("mixed cmap character range")
			}
			if last > utf8.MaxRune && (first>>16 != last>>16 || first>>16 < 0xd800 || last>>16 > 0xdbff || first&65535 < 0xdc00 || last&65535 > 0xdfff) {
				return nil, fmt.Errorf("invalid UTF-16 cmap group")
			}
		} else if last > utf8.MaxRune {
			return nil, fmt.Errorf("invalid cmap Unicode range")
		}
		if endGlyph > 65535 {
			return nil, fmt.Errorf("extended cmap glyph index overflow")
		}
		for code := uint64(first); code <= uint64(last); code++ {
			if format == 8 {
				word := code
				if code > 65535 {
					word >>= 16
				}
				if (data[12+word/8]&(1<<(7-word%8)) != 0) != (code > 65535) {
					return nil, fmt.Errorf("invalid cmap character width")
				}
			}
			id := glyph
			if format == 8 {
				id += code - uint64(first)
			}
			if err := add(uint32(code), uint16(id)); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// parseCmapFormat2 解析混合单字节与双字节字符的子头映射
// 入参: data format 2子表
// 返回: map[rune]uint16 编码值到字形编号, error 子头或字形数组越界
func parseCmapFormat2(data []byte) (map[rune]uint16, error) {
	if len(data) < 526 {
		return nil, fmt.Errorf("truncated cmap format 2")
	}
	length := int(binary.BigEndian.Uint16(data[2:]))
	if length < 526 || length > len(data) {
		return nil, fmt.Errorf("invalid cmap format 2 length")
	}
	data = data[:length]
	maxKey := 0
	for n := 0; n < 256; n++ {
		key := int(binary.BigEndian.Uint16(data[6+2*n:]))
		if key%8 != 0 || 518+key+8 > len(data) {
			return nil, fmt.Errorf("invalid cmap format 2 subheader")
		}
		maxKey = max(maxKey, key)
	}
	mapping := make(map[rune]uint16)
	for high := 0; high < 256; high++ {
		key := 0
		if high != 0 {
			key = int(binary.BigEndian.Uint16(data[6+2*high:]))
			if key == 0 {
				continue
			}
		}
		pos := 518 + key
		first, count := int(binary.BigEndian.Uint16(data[pos:])), int(binary.BigEndian.Uint16(data[pos+2:]))
		delta := binary.BigEndian.Uint16(data[pos+4:])
		start := pos + 6 + int(binary.BigEndian.Uint16(data[pos+6:]))
		if first+count > 256 || count > 0 && (start < 526+maxKey || start+count*2 > len(data)) {
			return nil, fmt.Errorf("invalid cmap format 2 glyph array")
		}
		for n := 0; n < count; n++ {
			low := first + n
			if high == 0 && binary.BigEndian.Uint16(data[6+2*low:]) != 0 {
				continue
			}
			glyph := binary.BigEndian.Uint16(data[start+2*n:])
			if glyph != 0 {
				mapping[rune(high*256+low)] = glyph + delta
			}
		}
	}
	return mapping, nil
}

// parseCmapFormat0 解析cmap format 0
// 入参: data 子表数据, result 字符映射
func parseCmapFormat0(data []byte, result map[rune]uint16) {
	if len(data) < 262 {
		return
	}
	for i := 0; i < 256; i++ {
		gid := uint16(data[6+i])
		if gid != 0 {
			result[rune(i)] = gid
		}
	}
}

// parseCmapFormat4 解析cmap format 4
// 入参: data 子表数据, result 字符映射
func parseCmapFormat4(data []byte, result map[rune]uint16) {
	if len(data) < 16 {
		return
	}
	length := int(binary.BigEndian.Uint16(data[2:4]))
	if length > len(data) {
		length = len(data)
	}
	segCount := int(binary.BigEndian.Uint16(data[6:8]) / 2)
	endPos := 14
	startPos := endPos + segCount*2 + 2
	deltaPos := startPos + segCount*2
	rangePos := deltaPos + segCount*2
	if rangePos+segCount*2 > length {
		return
	}
	for i := 0; i < segCount; i++ {
		end := binary.BigEndian.Uint16(data[endPos+i*2:])
		start := binary.BigEndian.Uint16(data[startPos+i*2:])
		delta := int16(binary.BigEndian.Uint16(data[deltaPos+i*2:]))
		rangeOffsetPos := rangePos + i*2
		rangeOffset := binary.BigEndian.Uint16(data[rangeOffsetPos:])
		if start == 0xFFFF && end == 0xFFFF {
			continue
		}
		for c := start; c <= end; c++ {
			var gid uint16
			if rangeOffset == 0 {
				gid = uint16(int(c) + int(delta))
			} else {
				gidPos := rangeOffsetPos + int(rangeOffset) + int(c-start)*2
				if gidPos+2 > length {
					continue
				}
				gid = binary.BigEndian.Uint16(data[gidPos:])
				if gid != 0 {
					gid = uint16(int(gid) + int(delta))
				}
			}
			if gid != 0 {
				result[rune(c)] = gid
			}
			if c == 0xFFFF {
				break
			}
		}
	}
}

// parseCmapFormat6 解析cmap format 6
// 入参: data 子表数据, result 字符映射
func parseCmapFormat6(data []byte, result map[rune]uint16) {
	if len(data) < 10 {
		return
	}
	firstCode := int(binary.BigEndian.Uint16(data[6:8]))
	entryCount := int(binary.BigEndian.Uint16(data[8:10]))
	for i := 0; i < entryCount && 10+i*2+2 <= len(data); i++ {
		gid := binary.BigEndian.Uint16(data[10+i*2:])
		if gid != 0 {
			result[rune(firstCode+i)] = gid
		}
	}
}

// parseCmapFormat12 解析cmap format 12
// 入参: data 子表数据, result 字符映射
func parseCmapFormat12(data []byte, result map[rune]uint16) {
	if len(data) < 16 {
		return
	}
	length := uint64(binary.BigEndian.Uint32(data[4:8]))
	if length < 16 || length > uint64(len(data)) {
		return
	}
	nGroups := uint64(binary.BigEndian.Uint32(data[12:16]))
	if nGroups > (length-16)/12 {
		return
	}
	for i := uint64(0); i < nGroups; i++ {
		pos := 16 + int(i)*12
		startChar := binary.BigEndian.Uint32(data[pos:])
		endChar := binary.BigEndian.Uint32(data[pos+4:])
		startGID := binary.BigEndian.Uint32(data[pos+8:])
		if startChar > endChar || startGID > 65535 || startChar > utf8.MaxRune {
			continue
		}
		last := min(uint64(endChar), uint64(startChar)+65535-uint64(startGID), uint64(utf8.MaxRune))
		for c := uint64(startChar); c <= last; c++ {
			gid := uint64(startGID) + c - uint64(startChar)
			if gid != 0 && utf8.ValidRune(rune(c)) {
				result[rune(c)] = uint16(gid)
			}
		}
	}
}

// sortedCmapCodes 单次提取有效码点并排序，同时判断是否需要格式12子表
// 入参: mapping 字符到字形的映射
// 返回: []rune 升序码点切片, bool 是否需要构建format 12子表
func sortedCmapCodes(mapping map[rune]uint16) ([]rune, bool) {
	if len(mapping) == 0 {
		return nil, false
	}
	codes := make([]rune, 0, len(mapping))
	needFormat12 := false
	for r, gid := range mapping {
		if r < 0 || r > utf8.MaxRune || gid == 0 {
			continue
		}
		if r >= 0xFFFF {
			needFormat12 = true
		}
		codes = append(codes, r)
	}
	slices.Sort(codes)
	if !needFormat12 && len(codes) > 0 {
		segCount := 2
		for i := 1; i < len(codes); i++ {
			if codes[i] != codes[i-1]+1 {
				segCount++
			}
		}
		if 16+segCount*8+len(codes)*2 > 0xFFFF {
			needFormat12 = true
		}
	}
	return codes, needFormat12
}

// buildCmapFormat4Sorted 基于已排序码点一次分配构建格式4字符映射表
// 入参: numGlyphs 字形数量, mapping 字符映射, codes 已排序BMP码点
// 返回: []byte cmap表数据
func buildCmapFormat4Sorted(numGlyphs uint16, mapping map[rune]uint16, codes []rune) []byte {
	var segs []cmapSegment
	glyphCount := 0
	if mapping == nil {
		if numGlyphs > 0 {
			segs = append(segs, cmapSegment{start: 0, end: numGlyphs - 1})
		}
	} else if len(codes) > 0 {
		glyphCount = len(codes)
		start := uint16(codes[0])
		prev := start
		for i := 1; i < len(codes); i++ {
			curr := uint16(codes[i])
			if curr != prev+1 {
				segs = append(segs, cmapSegment{start: start, end: prev})
				start = curr
			}
			prev = curr
		}
		segs = append(segs, cmapSegment{start: start, end: prev})
	}
	segs = append(segs, cmapSegment{start: 0xFFFF, end: 0xFFFF})
	segCount := len(segs)
	searchRange := uint16(1)
	entrySelector := uint16(0)
	for searchRange*2 <= uint16(segCount) {
		searchRange *= 2
		entrySelector++
	}
	searchRange *= 2
	rangeShift := uint16(segCount)*2 - searchRange
	subLen := 16 + segCount*8 + glyphCount*2
	out := make([]byte, 12+subLen)
	binary.BigEndian.PutUint16(out[2:], 1)
	binary.BigEndian.PutUint16(out[4:], 3)
	binary.BigEndian.PutUint16(out[6:], 1)
	binary.BigEndian.PutUint32(out[8:], 12)
	sub := out[12:]
	binary.BigEndian.PutUint16(sub, 4)
	binary.BigEndian.PutUint16(sub[2:], uint16(subLen))
	binary.BigEndian.PutUint16(sub[6:], uint16(segCount*2))
	binary.BigEndian.PutUint16(sub[8:], searchRange)
	binary.BigEndian.PutUint16(sub[10:], entrySelector)
	binary.BigEndian.PutUint16(sub[12:], rangeShift)
	endPos := 14
	startPos := 16 + segCount*2
	deltaPos := 16 + segCount*4
	rangePos := 16 + segCount*6
	glyphPos := 16 + segCount*8
	glyphIdx := 0
	for idx, s := range segs {
		binary.BigEndian.PutUint16(sub[endPos+idx*2:], s.end)
		binary.BigEndian.PutUint16(sub[startPos+idx*2:], s.start)
		if s.start == 0xFFFF {
			binary.BigEndian.PutUint16(sub[deltaPos+idx*2:], 1)
			continue
		}
		if mapping == nil {
			continue
		}
		offset := (segCount-idx)*2 + glyphIdx*2
		binary.BigEndian.PutUint16(sub[rangePos+idx*2:], uint16(offset))
		for c := int(s.start); c <= int(s.end); c++ {
			binary.BigEndian.PutUint16(sub[glyphPos+glyphIdx*2:], mapping[rune(c)])
			glyphIdx++
		}
	}
	return out
}

// buildCmapTableFormat12 构建cmap表 (Format 12)
// 入参: numGlyphs 字形数量, mapping 字符映射
// 返回: []byte cmap表数据
func buildCmapTableFormat12(numGlyphs uint16, mapping map[rune]uint16) []byte {
	if mapping == nil {
		var groups []cmapGroup
		if numGlyphs > 0 {
			groups = []cmapGroup{{startChar: 0, endChar: uint32(numGlyphs - 1), startGID: 0}}
		}
		return serializeCmapFormat12(groups)
	}
	codes := make([]rune, 0, len(mapping))
	for r, gid := range mapping {
		if r >= 0 && r <= utf8.MaxRune && gid != 0 {
			codes = append(codes, r)
		}
	}
	slices.Sort(codes)
	return encodeCmapFormat12(mapping, codes, 0)
}

// buildPackedCmapTable 将显式字符映射与字形私有区区间合并编码为cmap表
// 入参: numGlyphs 字形数量, mapping 显式字符映射
// 返回: []byte cmap表数据
func buildPackedCmapTable(numGlyphs uint16, mapping map[rune]uint16) []byte {
	if numGlyphs <= 1 {
		if numGlyphs == 1 && mapping[0xF0000] != 0 {
			cloned := make(map[rune]uint16, len(mapping))
			for r, gid := range mapping {
				if r != 0xF0000 {
					cloned[r] = gid
				}
			}
			return BuildCmapTable(numGlyphs, cloned)
		}
		return BuildCmapTable(numGlyphs, mapping)
	}
	packEnd := rune(0xF0000) + rune(numGlyphs-1)
	codes := make([]rune, 0, len(mapping))
	for r, gid := range mapping {
		if r >= 0 && r <= utf8.MaxRune && gid != 0 && (r < 0xF0000 || r > packEnd) {
			codes = append(codes, r)
		}
	}
	slices.Sort(codes)
	return encodeCmapFormat12(mapping, codes, numGlyphs-1)
}

// encodeCmapFormat12 合并已排序显式映射与私有区连续映射区间组并编码为Format 12
// 入参: mapping 显式字符映射, codes 升序显式码点, maxPackedGID 最大私有映射字形ID
// 返回: []byte cmap表数据
func encodeCmapFormat12(mapping map[rune]uint16, codes []rune, maxPackedGID uint16) []byte {
	packCur := uint32(0xF0001)
	packEnd := uint32(0xF0000) + uint32(maxPackedGID)
	if maxPackedGID == 0 {
		packCur = 1
		packEnd = 0
	}
	var groups []cmapGroup
	for i := 0; i < len(codes); {
		startChar := uint32(codes[i])
		if maxPackedGID > 0 && startChar == 0xF0000 {
			i++
			continue
		}
		if packCur <= packEnd && startChar >= packCur {
			groups = appendCmapGroup(groups, packCur, packEnd, packCur-0xF0000)
			packCur = packEnd + 1
		}
		if maxPackedGID > 0 && startChar >= 0xF0001 && startChar <= packEnd {
			i++
			continue
		}
		startGID := uint32(mapping[codes[i]])
		endChar := startChar
		prevGID := startGID
		i++
		for i < len(codes) {
			nextChar := uint32(codes[i])
			if maxPackedGID > 0 && nextChar >= 0xF0000 && nextChar <= packEnd {
				break
			}
			nextGID := uint32(mapping[codes[i]])
			if nextChar != endChar+1 || nextGID != prevGID+1 {
				break
			}
			endChar = nextChar
			prevGID = nextGID
			i++
		}
		groups = appendCmapGroup(groups, startChar, endChar, startGID)
	}
	if packCur <= packEnd {
		groups = appendCmapGroup(groups, packCur, packEnd, packCur-0xF0000)
	}
	return serializeCmapFormat12(groups)
}

// appendCmapGroup 追加或合并连续递增的Format 12映射区间组
// 入参: groups 已有区间组, startChar 起始码点, endChar 结束码点, startGID 起始字形ID
// 返回: []cmapGroup 更新后的区间组
func appendCmapGroup(groups []cmapGroup, startChar, endChar, startGID uint32) []cmapGroup {
	if n := len(groups); n > 0 {
		last := &groups[n-1]
		if last.endChar+1 == startChar && last.startGID+(last.endChar-last.startChar)+1 == startGID {
			last.endChar = endChar
			return groups
		}
	}
	return append(groups, cmapGroup{startChar: startChar, endChar: endChar, startGID: startGID})
}

// serializeCmapFormat12 将区间组编码为完整的Format 12字符映射表
// 入参: groups 有序区间组
// 返回: []byte cmap表数据
func serializeCmapFormat12(groups []cmapGroup) []byte {
	data := make([]byte, 36+12*len(groups))
	binary.BigEndian.PutUint16(data[2:], 2)
	binary.BigEndian.PutUint16(data[6:], 4)
	binary.BigEndian.PutUint32(data[8:], 20)
	binary.BigEndian.PutUint16(data[12:], 3)
	binary.BigEndian.PutUint16(data[14:], 10)
	binary.BigEndian.PutUint32(data[16:], 20)
	sub := data[20:]
	binary.BigEndian.PutUint16(sub, 12)
	binary.BigEndian.PutUint32(sub[4:], uint32(len(sub)))
	binary.BigEndian.PutUint32(sub[12:], uint32(len(groups)))
	for index, group := range groups {
		position := 16 + 12*index
		binary.BigEndian.PutUint32(sub[position:], group.startChar)
		binary.BigEndian.PutUint32(sub[position+4:], group.endChar)
		binary.BigEndian.PutUint32(sub[position+8:], group.startGID)
	}
	return data
}
