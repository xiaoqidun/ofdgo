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
	"encoding/binary"
)

// fixTrueType 尝试修复 TrueType/OpenType 字体数据
// 入参: data 原始字体数据, fixCmap 是否修复cmap, fixName 是否修复name
// 返回: bool 是否修复, []byte 修复后数据, map[rune]uint16 字符映射, bool 是否缺失cmap, error 错误信息
func fixTrueType(data []byte, fixCmap, fixName bool) (bool, []byte, map[rune]uint16, bool, error) {
	if len(data) < 12 {
		return false, data, nil, false, nil
	}
	sfntTag := string(data[0:4])
	isCFFSfnt := sfntTag == "OTTO"
	numTables := binary.BigEndian.Uint16(data[4:6])
	existingTables := make(map[string][]byte)
	malformedDirectory := false
	pos := 12
	for i := 0; i < int(numTables); i++ {
		if len(data) < pos+16 {
			malformedDirectory = true
			break
		}
		tag := string(data[pos : pos+4])
		offset := binary.BigEndian.Uint32(data[pos+8 : pos+12])
		length := binary.BigEndian.Uint32(data[pos+12 : pos+16])
		if uint32(len(data)) >= offset+length {
			existingTables[tag] = data[offset : offset+length]
			padding := (4 - (length & 3)) & 3
			if offset%4 != 0 || uint32(len(data))-offset-length < padding {
				malformedDirectory = true
			}
		} else {
			malformedDirectory = true
		}
		pos += 16
	}
	if existingTables["CFF "] != nil {
		isCFFSfnt = true
	}
	missingHead := existingTables["head"] == nil
	if !missingHead {
		head := existingTables["head"]
		if len(head) > 54 {
			existingTables["head"] = head[:54]
			malformedDirectory = true
		}
		if len(existingTables["head"]) != 54 || binary.BigEndian.Uint16(existingTables["head"][18:20]) == 0 {
			missingHead = true
		}
	}
	missingMaxp := existingTables["maxp"] == nil
	missingHhea := existingTables["hhea"] == nil
	missingHmtx := existingTables["hmtx"] == nil
	missingOS2 := existingTables["OS/2"] == nil
	missingCmap := existingTables["cmap"] == nil
	if !missingCmap {
		if !hasUsableCmap(existingTables["cmap"]) {
			missingCmap = true
		}
	}
	missingName := existingTables["name"] == nil
	missingPost := existingTables["post"] == nil
	var numGlyphs uint16 = 0
	if !missingMaxp {
		maxp := existingTables["maxp"]
		if len(maxp) >= 6 {
			numGlyphs = binary.BigEndian.Uint16(maxp[4:6])
		}
	}
	if numGlyphs == 0 && isCFFSfnt {
		if cff := existingTables["CFF "]; cff != nil {
			if n, err := parseCFFAndCountGlyphs(cff); err == nil && n > 0 {
				if n > 0xFFFF {
					numGlyphs = 0xFFFF
				} else {
					numGlyphs = uint16(n)
				}
			}
		}
	}
	if numGlyphs == 0 {
		numGlyphs = 255
	}
	if !missingHhea && !missingHmtx {
		hhea := existingTables["hhea"]
		hmtx := existingTables["hmtx"]
		if len(hhea) < 36 {
			missingHhea = true
		} else {
			numMetrics := binary.BigEndian.Uint16(hhea[34:36])
			if numMetrics == 0 || numMetrics > numGlyphs {
				missingHhea = true
			} else {
				expectHmtx := int(numMetrics)*4 + int(numGlyphs-numMetrics)*2
				if len(hmtx) < expectHmtx {
					missingHmtx = true
				} else if len(hmtx) > expectHmtx {
					existingTables["hmtx"] = hmtx[:expectHmtx]
					malformedDirectory = true
				}
			}
		}
	}
	if !missingPost && hasBadPostTable(existingTables["post"], numGlyphs, !isCFFSfnt) {
		missingPost = true
	}
	if !missingHead && !missingMaxp && !missingHhea && !missingHmtx &&
		!missingOS2 && !missingCmap && !missingName && !missingPost && !malformedDirectory {
		return false, data, nil, false, nil
	}
	newTables := make(map[string][]byte)
	for k, v := range existingTables {
		newTables[k] = v
	}
	if missingHead {
		newTables["head"] = buildHeadTable(1000)
	}
	if missingMaxp {
		if isCFFSfnt {
			newTables["maxp"] = buildCFFMaxpTable(numGlyphs)
		} else {
			newTables["maxp"] = buildTrueTypeMaxpTable(numGlyphs)
		}
	}
	if missingHhea {
		newTables["hhea"] = buildHheaTable(numGlyphs)
	}
	if missingHmtx {
		var defWidths []uint16
		if isCFFSfnt {
			if widths, err := parseCFFWidths(newTables["CFF "], int(numGlyphs)); err == nil {
				defWidths = widths
			}
		}
		if len(defWidths) == 0 {
			defWidths = make([]uint16, numGlyphs)
			for i := range defWidths {
				defWidths[i] = 500
			}
		}
		newTables["hmtx"] = buildHmtxTable(defWidths)
	}
	ascender := int16(800)
	descender := int16(-200)
	if hhea, ok := newTables["hhea"]; ok && len(hhea) >= 10 {
		ascender = int16(binary.BigEndian.Uint16(hhea[4:6]))
		descender = int16(binary.BigEndian.Uint16(hhea[6:8]))
	}
	if missingOS2 {
		newTables["OS/2"] = buildOS2TableWithMetrics(ascender, descender)
	}
	var mapping map[rune]uint16
	if missingCmap && fixCmap {
		if isCFFSfnt {
			mapping = getCmapFromCFF(newTables["CFF "], int(numGlyphs))
		}
		if len(mapping) == 0 {
			mapping = make(map[rune]uint16)
			for i := uint16(0); i < numGlyphs; i++ {
				mapping[packedGlyphRune(i)] = i
			}
		}
		newTables["cmap"] = buildCmapTable(numGlyphs, mapping)
	}
	if missingName && fixName {
		newTables["name"] = buildNameTable()
	}
	if missingPost {
		newTables["post"] = buildPostTable()
	}
	finalData, err := serializeOTF(newTables)
	if err != nil {
		return false, data, nil, missingCmap, err
	}
	return true, finalData, mapping, missingCmap, nil
}

// hasBadPostTable 检查 post 表是否会被 canvas/font 拒绝
func hasBadPostTable(data []byte, numGlyphs uint16, isTrueType bool) bool {
	if len(data) < 32 {
		return true
	}
	version := binary.BigEndian.Uint32(data[0:4])
	switch version {
	case 0x00010000:
		return !isTrueType || len(data) != 32
	case 0x00020000:
		if len(data) < 34 {
			return true
		}
		postGlyphs := binary.BigEndian.Uint16(data[32:34])
		if postGlyphs != numGlyphs {
			return true
		}
		return len(data) < 34+int(postGlyphs)*2
	case 0x00030000:
		return len(data) != 32
	default:
		return true
	}
}

// hasUsableCmap 检查是否存在可用的 cmap 子表
// 入参: data cmap表数据
// 返回: bool 是否可用
func hasUsableCmap(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	numTables := binary.BigEndian.Uint16(data[2:4])
	pos := 4
	for i := 0; i < int(numTables); i++ {
		if len(data) < pos+8 {
			break
		}
		platformID := binary.BigEndian.Uint16(data[pos : pos+2])
		if platformID == 0 || platformID == 3 {
			return true
		}
		pos += 8
	}
	return false
}
