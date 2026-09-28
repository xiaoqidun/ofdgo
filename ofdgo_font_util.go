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
)

// otfTableRecord OTF表记录结构
// 字段: tag 标签, checksum 校验和, offset 偏移, length 长度, data 数据
type otfTableRecord struct {
	tag      string
	checksum uint32
	offset   int
	length   int
	data     []byte
}

// extractCollectionFont 提取集合中的单个OpenType字体，保留表内容并重新计算校验和
// 入参: data 集合数据, index 字体索引
// 返回: []byte 单字体数据, error 错误信息
func extractCollectionFont(data []byte, index int) ([]byte, error) {
	tables, err := fontFileTables(data, index)
	if err != nil {
		return nil, err
	}
	if len(tables["head"]) < 12 {
		return nil, fmt.Errorf("invalid head font table")
	}
	tables["head"] = bytes.Clone(tables["head"])
	clear(tables["head"][8:12])
	return serializeOTF(tables)
}

// fontFileTables 读取单字体或集合指定项的表目录，不复制表数据
// 入参: data 字体数据, index 字体索引
// 返回: map[string][]byte 字体表, error 错误信息
func fontFileTables(data []byte, index int) (map[string][]byte, error) {
	count, err := fontFileCount(data)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= count {
		return nil, fmt.Errorf("invalid font index %d", index)
	}
	var offset uint64
	if string(data[:4]) == "ttcf" {
		offset = uint64(binary.BigEndian.Uint32(data[12+index*4:]))
	}
	if offset > uint64(len(data)-12) {
		return nil, fmt.Errorf("invalid font offset")
	}
	header := data[offset : offset+12]
	if string(header[:4]) != "\x00\x01\x00\x00" && string(header[:4]) != "OTTO" && string(header[:4]) != "true" {
		return nil, fmt.Errorf("invalid OpenType header")
	}
	tableCount := uint64(binary.BigEndian.Uint16(header[4:]))
	offset += 12
	if tableCount == 0 || tableCount > (uint64(len(data))-offset)/16 {
		return nil, fmt.Errorf("invalid font table directory")
	}
	tables := make(map[string][]byte, tableCount)
	for i := uint64(0); i < tableCount; i++ {
		entry := data[offset+i*16 : offset+(i+1)*16]
		tag := string(entry[:4])
		start := uint64(binary.BigEndian.Uint32(entry[8:]))
		length := uint64(binary.BigEndian.Uint32(entry[12:]))
		if start > uint64(len(data)) || length > uint64(len(data))-start {
			return nil, fmt.Errorf("invalid %s font table range", tag)
		}
		if _, exists := tables[tag]; exists {
			return nil, fmt.Errorf("duplicate %s font table", tag)
		}
		tables[tag] = data[start : start+length]
	}
	return tables, nil
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

// buildHeadTable 构建head表
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

// buildHheaTable 构建hhea表
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

// buildCFFMaxpTable 构建CFF轮廓使用的maxp 0.5表
// 入参: numGlyphs 字形数量
// 返回: []byte maxp表数据
func buildCFFMaxpTable(numGlyphs uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(0x00005000))
	binary.Write(buf, binary.BigEndian, uint16(numGlyphs))
	return buf.Bytes()
}

// buildTrueTypeMaxpTable 构建TrueType轮廓使用的maxp 1.0表
// 入参: numGlyphs 字形数量
// 返回: []byte maxp表数据
func buildTrueTypeMaxpTable(numGlyphs uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(0x00010000))
	binary.Write(buf, binary.BigEndian, uint16(numGlyphs))
	for i := 0; i < 13; i++ {
		binary.Write(buf, binary.BigEndian, uint16(0))
	}
	return buf.Bytes()
}

// buildOS2Table 构建OS/2表 (使用默认Metrics)
// 返回: []byte OS/2表数据
func buildOS2Table() []byte {
	return buildOS2TableWithMetrics(800, -200)
}

// buildOS2TableWithMetrics 构建OS/2表
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

// buildNameTable 构建name表 (最小化)
// 返回: []byte name表数据
func buildNameTable() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(6))
	return buf.Bytes()
}

// buildPostTable 构建post表 (版本3.0, 无字形名称)
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

// buildHmtxTable 构建hmtx表
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

// serializeOTF 序列化OpenType字体结构
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
		if tag == "head" && len(data) >= 12 {
			clear(padded[8:12])
		}
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
