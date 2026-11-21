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
	"math"
	"sort"
)

// ExtractCollectionFont 提取集合中的单个OpenType字体，保留表内容并重新计算校验和
// 入参: data 集合数据, index 字体索引
// 返回: []byte 单字体数据, error 错误信息
func ExtractCollectionFont(data []byte, index int) ([]byte, error) {
	tables, err := FileTables(data, index)
	if err != nil {
		return nil, err
	}
	if len(tables["head"]) < 12 {
		return nil, fmt.Errorf("invalid head font table")
	}
	return SerializeOTF(tables)
}

// FileTables 读取单字体或集合指定项的表目录，不复制表数据
// 入参: data 字体数据, index 字体索引
// 返回: map[string][]byte 字体表, error 错误信息
func FileTables(data []byte, index int) (map[string][]byte, error) {
	count, err := FileCount(data)
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

// NormalizePDFProgram 修复可确定的PDF内嵌SFNT封装，不补造TrueType轮廓或字形编号
// 入参: data 原始字体程序
// 返回: []byte 原程序或独立修复程序, bool 是否修复, error 无法确认的目录或度量错误
func NormalizePDFProgram(data []byte) ([]byte, bool, error) {
	if len(data) < 12 || string(data[:4]) != "OTTO" && string(data[:4]) != "\x00\x01\x00\x00" && string(data[:4]) != "true" {
		return data, false, nil
	}
	count := int(binary.BigEndian.Uint16(data[4:6]))
	if count == 0 || count > (len(data)-12)/16 {
		return nil, false, fmt.Errorf("unrecoverable PDF font directory size")
	}
	first := len(data)
	for n := 0; n < count; n++ {
		record := data[12+n*16:]
		start, size := uint64(binary.BigEndian.Uint32(record[8:])), uint64(binary.BigEndian.Uint32(record[12:]))
		if size != 0 && start >= 12 && start+size <= uint64(len(data)) {
			first = min(first, int(start))
		}
	}
	changed := false
	if first < 12+count*16 {
		if (first-12)%16 != 0 {
			return nil, false, fmt.Errorf("ambiguous PDF font directory overlap")
		}
		actual := (first - 12) / 16
		for n := 0; n < count; n++ {
			record := data[12+n*16:]
			printable := true
			for _, b := range record[:4] {
				printable = printable && b >= 32 && b <= 126
			}
			if printable != (n < actual) {
				return nil, false, fmt.Errorf("ambiguous PDF font table count")
			}
			if n < actual {
				start, size := uint64(binary.BigEndian.Uint32(record[8:])), uint64(binary.BigEndian.Uint32(record[12:]))
				if size != 0 && (start < uint64(first) || start+size > uint64(len(data))) {
					return nil, false, fmt.Errorf("incomplete PDF font table")
				}
			}
		}
		data = bytes.Clone(data)
		binary.BigEndian.PutUint16(data[4:6], uint16(actual))
		changed = true
	}
	tables, err := FileTables(data, 0)
	if err != nil {
		return nil, false, err
	}
	if head := tables["head"]; len(head) > 54 && len(head) <= 57 {
		tables["head"] = head[:54]
		changed = true
	}
	if hhea := tables["hhea"]; len(hhea) > 36 && len(hhea) <= 39 && binary.BigEndian.Uint32(hhea) == 0x00010000 {
		tables["hhea"] = hhea[:36]
		changed = true
	}
	if maxp := tables["maxp"]; tables["glyf"] != nil && len(maxp) > 32 && len(maxp) <= 35 && binary.BigEndian.Uint32(maxp) == 0x00010000 {
		tables["maxp"] = maxp[:32]
		changed = true
	}
	if post := tables["post"]; len(post) > 32 && binary.BigEndian.Uint32(post) == 0x00030000 {
		tables["post"] = post[:32]
		changed = true
	}
	if cvt := tables["cvt "]; len(cvt)%2 != 0 && cvt[len(cvt)-1] == 0 {
		tables["cvt "] = cvt[:len(cvt)-1]
		changed = true
	}
	if len(tables["CFF "]) != 0 && len(tables["head"]) == 0 && len(tables["maxp"]) == 0 && len(tables["hhea"]) == 0 && len(tables["hmtx"]) == 0 {
		program, _, err := WrapCFFToOTF(tables["CFF "])
		return program, true, err
	}
	if maxp := tables["maxp"]; len(tables["CFF "]) != 0 && tables["glyf"] == nil && len(maxp) >= 6 && (binary.BigEndian.Uint32(maxp) == 0x00010000 || binary.BigEndian.Uint32(maxp) == 0x00005000 && len(maxp) != 6) {
		count, err := parseCFFAndCountGlyphs(tables["CFF "])
		if err != nil {
			return nil, false, err
		}
		if count != int(binary.BigEndian.Uint16(maxp[4:6])) {
			program, _, err := WrapCFFToOTF(tables["CFF "])
			if err != nil {
				return nil, false, err
			}
			rebuilt, err := FileTables(program, 0)
			if err != nil {
				return nil, false, err
			}
			if len(tables["head"]) < 20 || !bytes.Equal(tables["head"][18:20], rebuilt["head"][18:20]) {
				return nil, false, fmt.Errorf("CFF and SFNT design units differ")
			}
			return program, true, nil
		}
		tables["maxp"] = buildCFFMaxpTable(uint16(count))
		changed = true
	}
	if !changed {
		return data, false, nil
	}
	program, err := SerializeOTF(tables)
	return program, true, err
}

// BuildOS2TableWithMetrics 构建OS/2表
// 入参: ascender 上升部, descender 下降部
// 返回: []byte OS/2表数据
func BuildOS2TableWithMetrics(ascender, descender int16) []byte {
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

// BuildPostTable 构建post表 (版本3.0, 无字形名称)
// 返回: []byte post表数据
func BuildPostTable() []byte {
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

// SerializeOTF 序列化OpenType字体结构，一次分配输出，不修改输入表数据
// 复用各表校验和计算整份字体的调整值，不重复扫描表数据
// 入参: tables 表数据映射
// 返回: []byte 完整字体数据, error 错误信息
func SerializeOTF(tables map[string][]byte) ([]byte, error) {
	totalSize, err := serializedOTFSize(tables)
	if err != nil {
		return nil, err
	}
	headerSize := 12 + 16*len(tables)
	tags := make([]string, 0, len(tables))
	for tag := range tables {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	data := make([]byte, totalSize)
	numTables := len(tables)
	entrySelector := 0
	for 1<<(entrySelector+1) <= numTables {
		entrySelector++
	}
	searchRange := 1 << (entrySelector + 4)
	rangeShift := numTables*16 - searchRange
	if tables["CFF "] != nil || tables["CFF2"] != nil {
		copy(data, "OTTO")
	} else {
		binary.BigEndian.PutUint32(data, 0x00010000)
	}
	binary.BigEndian.PutUint16(data[4:], uint16(numTables))
	binary.BigEndian.PutUint16(data[6:], uint16(searchRange))
	binary.BigEndian.PutUint16(data[8:], uint16(entrySelector))
	binary.BigEndian.PutUint16(data[10:], uint16(rangeShift))
	offset, adjustment := headerSize, 0
	var checksum uint32
	for i, tag := range tags {
		table := tables[tag]
		length := (len(table) + 3) &^ 3
		padded := data[offset : offset+length]
		copy(padded, table)
		if tag == "head" {
			clear(padded[8:12])
			adjustment = offset + 8
		}
		record := data[12+i*16 : 28+i*16]
		copy(record, tag)
		tableChecksum := calcTableChecksum(padded)
		checksum += tableChecksum
		binary.BigEndian.PutUint32(record[4:], tableChecksum)
		binary.BigEndian.PutUint32(record[8:], uint32(offset))
		binary.BigEndian.PutUint32(record[12:], uint32(len(table)))
		offset += length
	}
	if adjustment != 0 {
		binary.BigEndian.PutUint32(data[adjustment:], 0xB1B0AFBA-checksum-calcTableChecksum(data[:headerSize]))
	}
	return data, nil
}

// serializedOTFSize 计算OpenType表集合序列化后的精确字节长度
// 入参: tables 表数据映射
// 返回: int 序列化字节数, error 表目录或长度错误
func serializedOTFSize(tables map[string][]byte) (int, error) {
	if len(tables) > math.MaxUint16/16 {
		return 0, fmt.Errorf("too many font tables")
	}
	size := uint64(12 + 16*len(tables))
	for tag, data := range tables {
		if len(tag) != 4 {
			return 0, fmt.Errorf("invalid font table tag %q", tag)
		}
		if tag == "head" && len(data) < 12 {
			return 0, fmt.Errorf("invalid head font table")
		}
		size += (uint64(len(data)) + 3) &^ 3
		if size > math.MaxUint32 || size > uint64(math.MaxInt) {
			return 0, fmt.Errorf("font table data too large")
		}
	}
	return int(size), nil
}

// finite 判断数值是否有限
// 入参: value 数值
// 返回: bool 是否有限
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// calcTableChecksum 计算字体表校验和
// 入参: data 表数据
// 返回: uint32 校验和
func calcTableChecksum(data []byte) uint32 {
	var a, b, c, d uint32
	for len(data) >= 16 {
		a += binary.BigEndian.Uint32(data[:4])
		b += binary.BigEndian.Uint32(data[4:8])
		c += binary.BigEndian.Uint32(data[8:12])
		d += binary.BigEndian.Uint32(data[12:16])
		data = data[16:]
	}
	sum := a + b + c + d
	for len(data) >= 4 {
		sum += binary.BigEndian.Uint32(data[:4])
		data = data[4:]
	}
	for i, value := range data {
		sum += uint32(value) << (24 - 8*i)
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
	return BuildOS2TableWithMetrics(800, -200)
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
