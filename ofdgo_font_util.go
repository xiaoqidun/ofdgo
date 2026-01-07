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
	"sort"
)

// align4 计算 4 字节对齐后的长度
// 入参: n 原始长度
// 返回: uint32 对齐后的长度
func align4(n uint32) uint32 {
	return (n + 3) & ^uint32(3)
}

// calcTableChecksum 计算字体表校验和
// 入参: data 表数据
// 返回: uint32 校验和
func calcTableChecksum(data []byte) uint32 {
	var sum uint32
	length := len(data)
	for i := 0; i < length; i += 4 {
		if i+4 <= length {
			sum += binary.BigEndian.Uint32(data[i : i+4])
		} else {
			var val uint32
			rem := data[i:]
			for j, b := range rem {
				val |= uint32(b) << (24 - 8*j)
			}
			sum += val
		}
	}
	return sum
}

// checkMissingCmap 检查是否缺失 cmap 表
// 入参: data 字体数据
// 返回: bool 是否缺失
func checkMissingCmap(data []byte) bool {
	if len(data) < 12 {
		return true
	}
	numTables := binary.BigEndian.Uint16(data[4:6])
	for i := 0; i < int(numTables); i++ {
		pos := 12 + i*16
		if len(data) < pos+4 {
			break
		}
		tag := string(data[pos : pos+4])
		if tag == "cmap" {
			return false
		}
	}
	return true
}

// buildHeadTable 构建 head 表
// 入参: unitsPerEm 每em单位数
// 返回: []byte head表数据
func buildHeadTable(unitsPerEm uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(1))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0x5F0F3CF5))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, unitsPerEm)
	binary.Write(buf, binary.BigEndian, int64(0))
	binary.Write(buf, binary.BigEndian, int64(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(-500))
	binary.Write(buf, binary.BigEndian, int16(1000))
	binary.Write(buf, binary.BigEndian, int16(1000))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, int16(2))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	return buf.Bytes()
}

// buildHheaTable 构建 hhea 表
// 入参: numGlyphs 字形数量
// 返回: []byte hhea表数据
func buildHheaTable(numGlyphs uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(1))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, int16(800))
	binary.Write(buf, binary.BigEndian, int16(-200))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, uint16(1000))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(1000))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, uint16(numGlyphs))
	return buf.Bytes()
}

// buildMaxpTable 构建 maxp 表
// 入参: numGlyphs 字形数量
// 返回: []byte maxp表数据
func buildMaxpTable(numGlyphs uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(0x00005000))
	binary.Write(buf, binary.BigEndian, uint16(numGlyphs))
	return buf.Bytes()
}

// buildOS2Table 构建 OS/2 表 (使用默认 Metrics)
// 返回: []byte OS/2表数据
func buildOS2Table() []byte {
	return buildOS2TableWithMetrics(800, -200)
}

// buildOS2TableWithMetrics 构建 OS/2 表
// 入参: ascender 上升部, descender 下降部
// 返回: []byte OS/2表数据
func buildOS2TableWithMetrics(ascender, descender int16) []byte {
	os2 := new(bytes.Buffer)
	binary.Write(os2, binary.BigEndian, uint16(3))
	binary.Write(os2, binary.BigEndian, int16(500))
	binary.Write(os2, binary.BigEndian, uint16(400))
	binary.Write(os2, binary.BigEndian, uint16(5))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(50))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(0))
	os2.Write(make([]byte, 10))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	os2.WriteString("PfEd")
	binary.Write(os2, binary.BigEndian, uint16(0x0040))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, uint16(255))
	binary.Write(os2, binary.BigEndian, ascender)
	binary.Write(os2, binary.BigEndian, descender)
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, uint16(ascender))
	if descender < 0 {
		binary.Write(os2, binary.BigEndian, uint16(-descender))
	} else {
		binary.Write(os2, binary.BigEndian, uint16(descender))
	}
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, uint16(0))
	return os2.Bytes()
}

// buildNameTable 构建 name 表 (最小化)
// 返回: []byte name表数据
func buildNameTable() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(6))
	return buf.Bytes()
}

// buildPostTable 构建 post 表 (版本 3.0, 无字形名称)
// 返回: []byte post表数据
func buildPostTable() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(0x00030000))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	return buf.Bytes()
}

// buildHmtxTable 构建 hmtx 表
// 入参: widths 宽度列表
// 返回: []byte hmtx表数据
func buildHmtxTable(widths []uint16) []byte {
	buf := new(bytes.Buffer)
	for _, w := range widths {
		binary.Write(buf, binary.BigEndian, uint16(w))
		binary.Write(buf, binary.BigEndian, int16(0))
	}
	return buf.Bytes()
}

// cmapSegment cmap 表段结构
// 字段: start 开始字符, end 结束字符, delta 增量, offset 偏移
type cmapSegment struct {
	start, end uint16
	delta      int16
	offset     uint16
}

// buildCmapTable 构建 cmap 表 (Format 4)
// 入参: numGlyphs 字形数量, mapping 字符映射
// 返回: []byte cmap表数据
func buildCmapTable(numGlyphs uint16, mapping map[rune]uint16) []byte {
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

// otfTableRecord OTF 表记录结构
// 字段: tag 标签, checksum 校验和, offset 偏移, length 长度, data 数据
type otfTableRecord struct {
	tag      string
	checksum uint32
	offset   int
	length   int
	data     []byte
}

// serializeOTF 序列化 OpenType 字体结构
// 入参: tables 表数据映射
// 返回: []byte 完整字体数据, error 错误信息
func serializeOTF(tables map[string][]byte) ([]byte, error) {
	numTables := uint16(len(tables))
	entrySelector := 0
	for 1<<(entrySelector+1) <= int(numTables) {
		entrySelector++
	}
	searchRange := 1 << (entrySelector + 4)
	rangeShift := int(numTables)*16 - searchRange
	buf := new(bytes.Buffer)
	if _, ok := tables["CFF "]; ok {
		buf.WriteString("OTTO")
	} else {
		binary.Write(buf, binary.BigEndian, uint32(0x00010000))
	}
	binary.Write(buf, binary.BigEndian, numTables)
	binary.Write(buf, binary.BigEndian, uint16(searchRange))
	binary.Write(buf, binary.BigEndian, uint16(entrySelector))
	binary.Write(buf, binary.BigEndian, uint16(rangeShift))
	var tags []string
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	headerSize := 12 + 16*int(numTables)
	offset := headerSize
	var records []otfTableRecord
	for _, tag := range tags {
		data := tables[tag]
		pad := (4 - (len(data) % 4)) % 4
		padded := make([]byte, len(data)+pad)
		copy(padded, data)
		cs := calcTableChecksum(padded)
		records = append(records, otfTableRecord{tag, cs, offset, len(data), padded})
		offset += len(padded)
	}
	for _, r := range records {
		buf.WriteString(r.tag)
		binary.Write(buf, binary.BigEndian, r.checksum)
		binary.Write(buf, binary.BigEndian, uint32(r.offset))
		binary.Write(buf, binary.BigEndian, uint32(r.length))
	}
	for _, r := range records {
		buf.Write(r.data)
	}
	fullData := buf.Bytes()
	for _, r := range records {
		if r.tag == "head" {
			adjOffset := r.offset + 8
			if adjOffset+4 <= len(fullData) {
				binary.BigEndian.PutUint32(fullData[adjOffset:], 0)
				cs := calcTableChecksum(fullData)
				checksumAdj := 0xB1B0AFBA - cs
				binary.BigEndian.PutUint32(fullData[adjOffset:], checksumAdj)
			}
			break
		}
	}
	return fullData, nil
}
