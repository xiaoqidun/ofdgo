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
)

// FixFontData 修复字体数据
// 本函数主要解决嵌入字体缺失 OS/2 表导致部分渲染库（如 tdewolff/font）Panic 的问题
// 通过解析 hhea 表获取 Ascender/Descender 等度量信息，构造并注入一个标准的 OS/2 表
// 入参: data 原始字体文件数据
// 返回: bool 是否进行了修补, []byte 修复后的字体数据, error 错误信息
func FixFontData(data []byte) (bool, []byte, error) {
	if len(data) < 12 {
		return false, data, nil
	}
	numTables := binary.BigEndian.Uint16(data[4:6])
	hasOS2 := false
	type TableRecord struct {
		Tag      string
		CheckSum uint32
		Offset   uint32
		Length   uint32
	}
	tables := make([]TableRecord, 0, numTables)
	var hhea TableRecord
	hasHhea := false
	pos := 12
	for i := 0; i < int(numTables); i++ {
		if len(data) < pos+16 {
			return false, data, nil
		}
		tag := string(data[pos : pos+4])
		checkSum := binary.BigEndian.Uint32(data[pos+4 : pos+8])
		offset := binary.BigEndian.Uint32(data[pos+8 : pos+12])
		length := binary.BigEndian.Uint32(data[pos+12 : pos+16])
		tables = append(tables, TableRecord{tag, checkSum, offset, length})
		if tag == "OS/2" {
			hasOS2 = true
		}
		if tag == "hhea" {
			hhea = TableRecord{tag, checkSum, offset, length}
			hasHhea = true
		}
		pos += 16
	}
	if hasOS2 {
		return false, data, nil
	}
	if !hasHhea {
		return false, data, nil
	}
	if uint32(len(data)) < hhea.Offset+hhea.Length {
		return false, data, nil
	}
	hheaData := data[hhea.Offset : hhea.Offset+hhea.Length]
	if len(hheaData) < 10 {
		return false, data, nil
	}
	ascender := int16(binary.BigEndian.Uint16(hheaData[4:6]))
	descender := int16(binary.BigEndian.Uint16(hheaData[6:8]))
	os2 := new(bytes.Buffer)
	binary.Write(os2, binary.BigEndian, uint16(3))
	binary.Write(os2, binary.BigEndian, int16(500))
	binary.Write(os2, binary.BigEndian, uint16(400))
	binary.Write(os2, binary.BigEndian, uint16(5))
	binary.Write(os2, binary.BigEndian, uint16(0))
	for i := 0; i < 11; i++ {
		binary.Write(os2, binary.BigEndian, int16(0))
	}
	os2.Write(make([]byte, 10))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	os2.WriteString("PfEd")
	binary.Write(os2, binary.BigEndian, uint16(0x0040))
	binary.Write(os2, binary.BigEndian, uint16(0x0020))
	binary.Write(os2, binary.BigEndian, uint16(0xFFFD))
	binary.Write(os2, binary.BigEndian, ascender)
	binary.Write(os2, binary.BigEndian, descender)
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, uint16(ascender))
	if descender < 0 {
		binary.Write(os2, binary.BigEndian, uint16(-descender))
	} else {
		binary.Write(os2, binary.BigEndian, uint16(descender))
	}
	binary.Write(os2, binary.BigEndian, uint32(1))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, uint16(0x0020))
	binary.Write(os2, binary.BigEndian, uint16(0))
	os2Data := os2.Bytes()
	os2CheckSum := calcTableChecksum(os2Data)
	newNumTables := numTables + 1
	entrySelector := 0
	for 1<<(entrySelector+1) <= int(newNumTables) {
		entrySelector++
	}
	searchRange := 1 << (entrySelector + 4)
	rangeShift := int(newNumTables)*16 - searchRange
	buf := new(bytes.Buffer)
	buf.Write(data[0:4])
	binary.Write(buf, binary.BigEndian, uint16(newNumTables))
	binary.Write(buf, binary.BigEndian, uint16(searchRange))
	binary.Write(buf, binary.BigEndian, uint16(entrySelector))
	binary.Write(buf, binary.BigEndian, uint16(rangeShift))
	newTables := append(tables, TableRecord{
		Tag:      "OS/2",
		CheckSum: os2CheckSum,
		Offset:   0,
		Length:   uint32(len(os2Data)),
	})
	for i := 0; i < len(newTables); i++ {
		for j := i + 1; j < len(newTables); j++ {
			if newTables[i].Tag > newTables[j].Tag {
				newTables[i], newTables[j] = newTables[j], newTables[i]
			}
		}
	}
	headerSize := 12 + 16*uint32(len(newTables))
	currentOffset := headerSize
	for i := range newTables {
		t := &newTables[i]
		t.Offset = currentOffset
		currentOffset += align4(t.Length)
	}
	for _, t := range newTables {
		buf.WriteString(t.Tag)
		binary.Write(buf, binary.BigEndian, t.CheckSum)
		binary.Write(buf, binary.BigEndian, t.Offset)
		binary.Write(buf, binary.BigEndian, t.Length)
	}
	for _, t := range newTables {
		var tableData []byte
		if t.Tag == "OS/2" {
			tableData = os2Data
		} else {
			found := false
			for _, old := range tables {
				if old.Tag == t.Tag {
					if uint32(len(data)) < old.Offset+old.Length {
						return false, data, nil
					}
					tableData = data[old.Offset : old.Offset+old.Length]
					found = true
					break
				}
			}
			if !found {
				return false, data, fmt.Errorf("lost table %s", t.Tag)
			}
		}
		buf.Write(tableData)
		padding := align4(t.Length) - t.Length
		for k := uint32(0); k < padding; k++ {
			buf.WriteByte(0)
		}
	}
	fullData := buf.Bytes()
	var newHeadOffset uint32
	var newHeadLength uint32
	for _, t := range newTables {
		if t.Tag == "head" {
			newHeadOffset = t.Offset
			newHeadLength = t.Length
		}
	}
	if newHeadLength >= 12 {
		checkSumAdjPos := newHeadOffset + 8
		binary.BigEndian.PutUint32(fullData[checkSumAdjPos:checkSumAdjPos+4], 0)
		cs := calcTableChecksum(fullData)
		adjustment := 0xB1B0AFBA - cs
		binary.BigEndian.PutUint32(fullData[checkSumAdjPos:checkSumAdjPos+4], adjustment)
	}
	return true, fullData, nil
}

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
