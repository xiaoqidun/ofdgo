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
	"maps"
	"slices"
	"unicode/utf8"

	"github.com/go-text/typesetting/font/opentype/tables"
)

// HasCmapVariations 判断独立SFNT字体的cmap表是否包含格式14变体序列子表
// 入参: data 独立SFNT字体数据
// 返回: bool 是否包含格式14子表
func HasCmapVariations(data []byte) bool {
	fontTables, err := FileTables(data, 0)
	if err != nil {
		return false
	}
	cmap := fontTables["cmap"]
	if len(cmap) < 4 {
		return false
	}
	n := int(binary.BigEndian.Uint16(cmap[2:]))
	if n > (len(cmap)-4)/8 {
		return false
	}
	for i := 0; i < n; i++ {
		record := cmap[4+i*8:]
		offset := uint64(binary.BigEndian.Uint32(record[4:]))
		if offset+2 <= uint64(len(cmap)) && binary.BigEndian.Uint16(cmap[offset:]) == 14 {
			return true
		}
	}
	return false
}

// subsetCmapVariations 裁剪Unicode变体映射，将非默认变体的字形加入保留集合
// 入参: data 字符映射表, chars 使用字符, glyphs 保留字形, count 字形总数
// 返回: []byte 格式14子表，无变体时为空, bool 映射是否有效
func subsetCmapVariations(data []byte, chars map[rune]bool, glyphs map[uint16]bool, count uint16) ([]byte, bool) {
	if len(data) < 4 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(data[2:]))
	if n > (len(data)-4)/8 {
		return nil, false
	}
	var source []byte
	for i := 0; i < n; i++ {
		record := data[4+i*8:]
		offset := uint64(binary.BigEndian.Uint32(record[4:]))
		if offset < uint64(4+n*8) || offset+2 > uint64(len(data)) {
			return nil, false
		}
		if binary.BigEndian.Uint16(data[offset:]) != 14 {
			continue
		}
		if source != nil || binary.BigEndian.Uint16(record) != 0 || binary.BigEndian.Uint16(record[2:]) != 5 || offset+10 > uint64(len(data)) {
			return nil, false
		}
		length := uint64(binary.BigEndian.Uint32(data[offset+2:]))
		if length < 10 || offset+length > uint64(len(data)) {
			return nil, false
		}
		source = data[offset : offset+length]
	}
	if source == nil {
		return nil, true
	}
	return subsetCmapVariationTable(source, chars, glyphs, count)
}

// subsetCmapVariationTable 按使用字符筛选默认及非默认变体，保持选择符和映射有序
// 入参: data 格式14子表, chars 使用字符, glyphs 保留字形, count 字形总数
// 返回: []byte 裁剪后的子表, bool 子表是否有效
func subsetCmapVariationTable(data []byte, chars map[rune]bool, glyphs map[uint16]bool, count uint16) ([]byte, bool) {
	n := uint64(binary.BigEndian.Uint32(data[6:]))
	header := 10 + n*11
	if header > uint64(len(data)) {
		return nil, false
	}
	used := slices.Sorted(maps.Keys(chars))
	var records, payload []byte
	previous := rune(-1)
	for i := uint64(0); i < n; i++ {
		raw := data[10+i*11:]
		for j, size := range []uint64{4, 5} {
			offset := uint64(binary.BigEndian.Uint32(raw[3+j*4:]))
			if offset == 0 {
				continue
			}
			if offset < header || offset+4 > uint64(len(data)) {
				return nil, false
			}
			entries := uint64(binary.BigEndian.Uint32(data[offset:]))
			if offset+4+entries*size > uint64(len(data)) {
				return nil, false
			}
		}
		record, _, err := tables.ParseVariationSelector(raw, data)
		selector := cmapVariationRune(record.VarSelector)
		if err != nil || !utf8.ValidRune(selector) || selector <= previous || !validCmapVariation(record, count) {
			return nil, false
		}
		previous = selector
		if !chars[selector] {
			continue
		}
		defaults := subsetCmapDefaultVariations(record.DefaultUVS.Ranges, used)
		var alternatives []byte
		for _, entry := range record.NonDefaultUVS.Ranges {
			if chars[cmapVariationRune(entry.UnicodeValue)] {
				alternatives = append(alternatives, entry.UnicodeValue[:]...)
				alternatives = binary.BigEndian.AppendUint16(alternatives, uint16(entry.GlyphID))
				glyphs[uint16(entry.GlyphID)] = true
			}
		}
		if len(defaults)+len(alternatives) == 0 {
			continue
		}
		start := len(records)
		records = append(records, record.VarSelector[:]...)
		records = append(records, make([]byte, 8)...)
		for j, entries := range [][]byte{defaults, alternatives} {
			if len(entries) == 0 {
				continue
			}
			binary.BigEndian.PutUint32(records[start+3+j*4:], uint32(len(payload)+1))
			payload = binary.BigEndian.AppendUint32(payload, uint32(len(entries)/(4+j)))
			payload = append(payload, entries...)
		}
	}
	if len(records) == 0 {
		return nil, true
	}
	if uint64(len(records))+uint64(len(payload))+10 > 1<<32-1 {
		return nil, false
	}
	for i := 0; i < len(records); i += 11 {
		for _, j := range []int{3, 7} {
			offset := binary.BigEndian.Uint32(records[i+j:])
			if offset != 0 {
				binary.BigEndian.PutUint32(records[i+j:], offset-1+uint32(10+len(records)))
			}
		}
	}
	out := make([]byte, 10, 10+len(records)+len(payload))
	binary.BigEndian.PutUint16(out, 14)
	binary.BigEndian.PutUint32(out[2:], uint32(cap(out)))
	binary.BigEndian.PutUint32(out[6:], uint32(len(records)/11))
	out = append(out, records...)
	return append(out, payload...), true
}

// validCmapVariation 检查变体映射的字符范围、排序、互斥关系及字形编号
// 入参: record 选择符记录, count 字形总数
// 返回: bool 映射是否有效
func validCmapVariation(record tables.VariationSelector, count uint16) bool {
	defaults := record.DefaultUVS.Ranges
	previous := rune(-1)
	for _, entry := range defaults {
		first := cmapVariationRune(entry.StartUnicodeValue)
		last := first + rune(entry.AdditionalCount)
		if first <= previous || !utf8.ValidRune(first) || !utf8.ValidRune(last) || first < 0xd800 && last >= 0xd800 {
			return false
		}
		previous = last
	}
	previous = -1
	index := 0
	for _, entry := range record.NonDefaultUVS.Ranges {
		char := cmapVariationRune(entry.UnicodeValue)
		if char <= previous || !utf8.ValidRune(char) || uint16(entry.GlyphID) >= count {
			return false
		}
		previous = char
		for index < len(defaults) && cmapVariationRune(defaults[index].StartUnicodeValue)+rune(defaults[index].AdditionalCount) < char {
			index++
		}
		if index < len(defaults) && cmapVariationRune(defaults[index].StartUnicodeValue) <= char {
			return false
		}
	}
	return true
}

// subsetCmapDefaultVariations 筛选默认变体的使用字符，将连续字符合并为格式14范围
// 入参: ranges 默认变体范围, chars 已排序的使用字符
// 返回: []byte 范围记录，不含数量字段
func subsetCmapDefaultVariations(ranges []tables.UnicodeRange, chars []rune) []byte {
	var out []byte
	index := 0
	last := rune(-2)
	for _, char := range chars {
		for index < len(ranges) && cmapVariationRune(ranges[index].StartUnicodeValue)+rune(ranges[index].AdditionalCount) < char {
			index++
		}
		if index == len(ranges) {
			break
		}
		if cmapVariationRune(ranges[index].StartUnicodeValue) > char {
			continue
		}
		if char == last+1 && out[len(out)-1] < 255 {
			out[len(out)-1]++
		} else {
			out = append(out, byte(char>>16), byte(char>>8), byte(char), 0)
		}
		last = char
	}
	return out
}

// cmapVariationRune 读取变体映射中的24位Unicode码点
// 入参: value 三字节码点
// 返回: rune Unicode码点
func cmapVariationRune(value [3]byte) rune {
	return rune(value[0])<<16 | rune(value[1])<<8 | rune(value[2])
}

// buildCmapWithVariations 在生成的Unicode映射中插入格式14记录，保持编码记录有序
// 入参: count 字形总数, mapping 字符映射, variations 格式14子表
// 返回: []byte 字符映射表
func buildCmapWithVariations(count uint16, mapping map[rune]uint16, variations []byte) []byte {
	base := BuildCmapTable(count, mapping)
	if len(variations) == 0 {
		return base
	}
	payload := base[binary.BigEndian.Uint32(base[8:]):]
	out := make([]byte, 28, 28+len(payload)+len(variations))
	binary.BigEndian.PutUint16(out[2:], 3)
	encoding := uint16(3)
	if binary.BigEndian.Uint16(payload) == 12 {
		encoding = 4
	}
	binary.BigEndian.PutUint16(out[6:], encoding)
	binary.BigEndian.PutUint32(out[8:], 28)
	binary.BigEndian.PutUint16(out[14:], 5)
	binary.BigEndian.PutUint32(out[16:], uint32(28+len(payload)))
	last := 4 + 8*(int(binary.BigEndian.Uint16(base[2:]))-1)
	copy(out[20:28], base[last:last+8])
	binary.BigEndian.PutUint32(out[24:], 28)
	out = append(out, payload...)
	return append(out, variations...)
}

// remapCmapVariations 按新字形映射表重写格式14非默认变体的字形编号
// 入参: data 格式14子表, remap 原字形编号到新字形编号的映射
// 返回: []byte 重映射后的格式14子表, bool 是否全部映射成功
func remapCmapVariations(data []byte, remap map[uint16]uint16) ([]byte, bool) {
	if len(data) == 0 {
		return nil, true
	}
	if len(data) < 10 || binary.BigEndian.Uint16(data) != 14 {
		return nil, false
	}
	n := uint64(binary.BigEndian.Uint32(data[6:]))
	if 10+n*11 > uint64(len(data)) {
		return nil, false
	}
	out := bytes.Clone(data)
	for i := uint64(0); i < n; i++ {
		offset := uint64(binary.BigEndian.Uint32(out[10+i*11+7:]))
		if offset == 0 {
			continue
		}
		if offset+4 > uint64(len(out)) {
			return nil, false
		}
		count := uint64(binary.BigEndian.Uint32(out[offset:]))
		if offset+4+count*5 > uint64(len(out)) {
			return nil, false
		}
		for j := uint64(0); j < count; j++ {
			pos := offset + 4 + j*5 + 3
			mapped, ok := remap[binary.BigEndian.Uint16(out[pos:])]
			if !ok {
				return nil, false
			}
			binary.BigEndian.PutUint16(out[pos:], mapped)
		}
	}
	return out, true
}
