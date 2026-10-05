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
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

// cmapSegment cmap表段结构
// 字段: start 开始字符, end 结束字符, delta 增量, offset 偏移
type cmapSegment struct {
	start, end uint16
	delta      int16
	offset     uint16
}

// normalizeCmap 转换扩展字符子表，保留各编码记录及原字形编号
// 入参: data 字符映射表
// 返回: []byte 等价字符映射表, error 子表错误
func normalizeCmap(data []byte) ([]byte, error) {
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
			table := buildCmapTable(0, mapping)
			if language > 65535 {
				table = buildCmapTableFormat12(0, mapping)
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

// packedGlyphRune 获取包装字体字符
// 入参: gid 字形ID
// 返回: rune 包装字体字符
func packedGlyphRune(gid uint16) rune {
	return 0xF0000 + rune(gid)
}

// parseCmapMappings 解析cmap字符映射
// 入参: data cmap表数据
// 返回: map[rune]uint16 字符到字形映射
func parseCmapMappings(data []byte) map[rune]uint16 {
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

// addPackedGlyphMapping 添加字形ID私有映射
// 入参: mapping 字符映射, numGlyphs 字形数量
func addPackedGlyphMapping(mapping map[rune]uint16, numGlyphs uint16) {
	for i := uint16(0); i < numGlyphs; i++ {
		mapping[packedGlyphRune(i)] = i
	}
}

// buildCmapTable 构建cmap表 (Format 4)
// 入参: numGlyphs 字形数量, mapping 字符映射
// 返回: []byte cmap表数据
func buildCmapTable(numGlyphs uint16, mapping map[rune]uint16) []byte {
	if shouldBuildCmapFormat12(mapping) {
		return buildCmapTableFormat12(numGlyphs, mapping)
	}
	var segs []cmapSegment
	if mapping == nil {
		end := uint16(0xFFFF)
		if numGlyphs > 0 {
			end = numGlyphs - 1
		}
		segs = append(segs, cmapSegment{start: 0, end: end, delta: 0, offset: 0})
	} else {
		var codes []int
		for r := range mapping {
			if r <= 0xFFFF {
				codes = append(codes, int(r))
			}
		}
		sort.Ints(codes)
		if len(codes) > 0 {
			start := codes[0]
			prev := start
			for i := 1; i < len(codes); i++ {
				curr := codes[i]
				if curr != prev+1 {
					segs = append(segs, cmapSegment{start: uint16(start), end: uint16(prev), delta: 0, offset: 0})
					start = curr
				}
				prev = curr
			}
			segs = append(segs, cmapSegment{start: uint16(start), end: uint16(prev), delta: 0, offset: 0})
		}
	}
	segs = append(segs, cmapSegment{start: 0xFFFF, end: 0xFFFF, delta: 1, offset: 0})
	segCount := uint16(len(segs))
	searchRange := uint16(1)
	entrySelector := uint16(0)
	for searchRange*2 <= segCount {
		searchRange *= 2
		entrySelector++
	}
	searchRange *= 2
	rangeShift := segCount*2 - searchRange
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(4))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(segCount*2))
	binary.Write(buf, binary.BigEndian, searchRange)
	binary.Write(buf, binary.BigEndian, entrySelector)
	binary.Write(buf, binary.BigEndian, rangeShift)
	var endCounts, startCounts, idDeltas, idRangeOffsets []uint16
	var glyphIds []uint16
	for _, s := range segs {
		endCounts = append(endCounts, s.end)
		startCounts = append(startCounts, s.start)
		if mapping == nil {
			idDeltas = append(idDeltas, 0)
			idRangeOffsets = append(idRangeOffsets, 0)
			continue
		}
		if s.start == 0xFFFF {
			idDeltas = append(idDeltas, 1)
			idRangeOffsets = append(idRangeOffsets, 0)
			continue
		}
		idDeltas = append(idDeltas, 0)
		currentGlyphIdx := len(glyphIds)
		for c := int(s.start); c <= int(s.end); c++ {
			gid := mapping[rune(c)]
			glyphIds = append(glyphIds, gid)
		}
		offset := (int(segCount)-int(len(idRangeOffsets))-1)*2 + 2 + currentGlyphIdx*2
		idRangeOffsets = append(idRangeOffsets, uint16(offset))
	}
	for _, v := range endCounts {
		binary.Write(buf, binary.BigEndian, v)
	}
	binary.Write(buf, binary.BigEndian, uint16(0))
	for _, v := range startCounts {
		binary.Write(buf, binary.BigEndian, v)
	}
	for _, v := range idDeltas {
		binary.Write(buf, binary.BigEndian, v)
	}
	for _, v := range idRangeOffsets {
		binary.Write(buf, binary.BigEndian, v)
	}
	for _, v := range glyphIds {
		binary.Write(buf, binary.BigEndian, v)
	}
	data := buf.Bytes()
	binary.BigEndian.PutUint16(data[2:4], uint16(len(data)))
	mainBuf := new(bytes.Buffer)
	binary.Write(mainBuf, binary.BigEndian, uint16(0))
	binary.Write(mainBuf, binary.BigEndian, uint16(1))
	binary.Write(mainBuf, binary.BigEndian, uint16(3))
	binary.Write(mainBuf, binary.BigEndian, uint16(1))
	binary.Write(mainBuf, binary.BigEndian, uint32(12))
	mainBuf.Write(data)
	return mainBuf.Bytes()
}

// shouldBuildCmapFormat12 判断终止码、补充平面字符或format 4长度溢出时是否需要format 12
// 入参: mapping 字符到字形的映射
// 返回: bool 是否需要构建format 12子表
func shouldBuildCmapFormat12(mapping map[rune]uint16) bool {
	if mapping == nil {
		return false
	}
	var codes []int
	for r := range mapping {
		if r >= 0xFFFF {
			return true
		}
		codes = append(codes, int(r))
	}
	if len(codes) == 0 {
		return false
	}
	sort.Ints(codes)
	segCount := 1
	glyphIDCount := 1
	prev := codes[0]
	for i := 1; i < len(codes); i++ {
		curr := codes[i]
		if curr == prev {
			continue
		}
		if curr != prev+1 {
			segCount++
		}
		glyphIDCount++
		prev = curr
	}
	segCount++
	length := 16 + segCount*8 + glyphIDCount*2
	return length > 0xFFFF
}

// buildCmapTableFormat12 构建cmap表 (Format 12)
// 入参: numGlyphs 字形数量, mapping 字符映射
// 返回: []byte cmap表数据
func buildCmapTableFormat12(numGlyphs uint16, mapping map[rune]uint16) []byte {
	var codes []int
	if mapping == nil {
		for i := 0; i < int(numGlyphs); i++ {
			codes = append(codes, i)
		}
	} else {
		for r := range mapping {
			if r >= 0 {
				codes = append(codes, int(r))
			}
		}
	}
	sort.Ints(codes)
	type cmapGroup struct {
		startChar uint32
		endChar   uint32
		startGID  uint32
	}
	var groups []cmapGroup
	for i := 0; i < len(codes); {
		r := rune(codes[i])
		gid := uint16(codes[i])
		if mapping != nil {
			gid = mapping[r]
		}
		group := cmapGroup{startChar: uint32(r), endChar: uint32(r), startGID: uint32(gid)}
		prevRune := r
		prevGID := gid
		i++
		for i < len(codes) {
			nextRune := rune(codes[i])
			nextGID := uint16(codes[i])
			if mapping != nil {
				nextGID = mapping[nextRune]
			}
			if nextRune != prevRune+1 || nextGID != prevGID+1 {
				break
			}
			group.endChar = uint32(nextRune)
			prevRune = nextRune
			prevGID = nextGID
			i++
		}
		groups = append(groups, group)
	}
	sub := new(bytes.Buffer)
	binary.Write(sub, binary.BigEndian, uint16(12))
	binary.Write(sub, binary.BigEndian, uint16(0))
	binary.Write(sub, binary.BigEndian, uint32(16+12*len(groups)))
	binary.Write(sub, binary.BigEndian, uint32(0))
	binary.Write(sub, binary.BigEndian, uint32(len(groups)))
	for _, group := range groups {
		binary.Write(sub, binary.BigEndian, group.startChar)
		binary.Write(sub, binary.BigEndian, group.endChar)
		binary.Write(sub, binary.BigEndian, group.startGID)
	}
	mainBuf := new(bytes.Buffer)
	binary.Write(mainBuf, binary.BigEndian, uint16(0))
	binary.Write(mainBuf, binary.BigEndian, uint16(2))
	binary.Write(mainBuf, binary.BigEndian, uint16(0))
	binary.Write(mainBuf, binary.BigEndian, uint16(4))
	binary.Write(mainBuf, binary.BigEndian, uint32(20))
	binary.Write(mainBuf, binary.BigEndian, uint16(3))
	binary.Write(mainBuf, binary.BigEndian, uint16(10))
	binary.Write(mainBuf, binary.BigEndian, uint32(20))
	mainBuf.Write(sub.Bytes())
	return mainBuf.Bytes()
}
