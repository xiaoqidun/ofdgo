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

// normalizeCmap 转换旧式双字节子表，保留各编码记录及原字形编号
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
		if binary.BigEndian.Uint16(data[offset:]) != 2 {
			continue
		}
		if out == nil {
			out = bytes.Clone(data)
		}
		next, ok := converted[offset]
		if !ok {
			mapping, err := parseCmapFormat2(data[offset:])
			if err != nil {
				return nil, err
			}
			table := buildCmapTable(0, mapping)
			subtable := table[binary.BigEndian.Uint32(table[8:]):]
			language := binary.BigEndian.Uint16(data[offset+4:])
			if binary.BigEndian.Uint16(subtable) == 12 {
				binary.BigEndian.PutUint32(subtable[8:], uint32(language))
			} else {
				binary.BigEndian.PutUint16(subtable[4:], language)
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
