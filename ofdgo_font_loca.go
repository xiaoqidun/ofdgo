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
	"errors"
	"fmt"
	"slices"
)

// errTTInvalidLocations 表示字形索引不满足递增偏移要求
var errTTInvalidLocations = errors.New("invalid TrueType glyph locations")

// normalizeTrueTypeLocations 验证物理字形边界后按原编号重排，不改变空字形和分量引用
// 入参: tables 独占字体表目录，表内容只读
// 返回: bool 是否重建索引, error 无法可靠恢复的索引或轮廓错误
func normalizeTrueTypeLocations(tables map[string][]byte) (bool, error) {
	if _, ok := tables["glyf"]; !ok {
		return false, nil
	}
	head, maxp, loca, glyf := tables["head"], tables["maxp"], tables["loca"], tables["glyf"]
	if len(head) < 54 || len(maxp) < 6 {
		return false, fmt.Errorf("%w: missing header", errTTInvalidLocations)
	}
	count := int(binary.BigEndian.Uint16(maxp[4:]))
	width := 2
	switch binary.BigEndian.Uint16(head[50:]) {
	case 0:
	case 1:
		width = 4
	default:
		return false, fmt.Errorf("%w: format", errTTInvalidLocations)
	}
	if count == 0 || len(loca) < (count+1)*width {
		return false, fmt.Errorf("%w: truncated index", errTTInvalidLocations)
	}
	location := func(i int) uint64 {
		if width == 2 {
			return uint64(binary.BigEndian.Uint16(loca[i*2:])) * 2
		}
		return uint64(binary.BigEndian.Uint32(loca[i*4:]))
	}
	ordered, previous := true, uint64(0)
	for i := range count + 1 {
		offset := location(i)
		if offset > uint64(len(glyf)) {
			return false, fmt.Errorf("%w: offset %d outside table", errTTInvalidLocations, i)
		}
		ordered = ordered && previous <= offset
		previous = offset
	}
	if ordered {
		return false, nil
	}
	offsets := make([]int, count+1)
	for i := range offsets {
		offsets[i] = int(location(i))
	}
	physical := slices.Clone(offsets)
	physical = append(physical, len(glyf))
	slices.Sort(physical)
	physical = slices.Compact(physical)
	bodies := make(map[int][]byte, count)
	components := make([][]uint16, count)
	var total uint64
	for id := range count {
		start := offsets[id]
		if start == offsets[id+1] {
			continue
		}
		if _, duplicate := bodies[start]; duplicate {
			return false, fmt.Errorf("%w: ambiguous shared offset %d", errTTInvalidLocations, start)
		}
		index, _ := slices.BinarySearch(physical, start)
		if index+1 >= len(physical) {
			return false, fmt.Errorf("%w: missing glyph %d body", errTTInvalidLocations, id)
		}
		data := glyf[start:physical[index+1]]
		size, refs, err := trueTypeGlyphSize(data, count)
		if err != nil {
			return false, fmt.Errorf("%w: glyph %d: %w", errTTInvalidLocations, id, err)
		}
		if len(data)-size > 3 || len(bytes.Trim(data[size:], "\x00")) != 0 {
			return false, fmt.Errorf("%w: ambiguous glyph %d padding", errTTInvalidLocations, id)
		}
		bodies[start], components[id] = data[:size], refs
		total += (uint64(size) + 3) &^ 3
		if total > uint64(^uint(0)>>1) || total > uint64(^uint32(0)) {
			return false, fmt.Errorf("%w: rebuilt table exceeds capacity", errTTInvalidLocations)
		}
	}
	state := make([]uint8, count)
	var visit func(int) error
	visit = func(id int) error {
		if state[id] == 1 {
			return fmt.Errorf("%w: cyclic component %d", errTTInvalidLocations, id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, ref := range components[id] {
			if err := visit(int(ref)); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range count {
		if err := visit(id); err != nil {
			return false, err
		}
	}
	result, locations := make([]byte, 0, total), make([]byte, (count+1)*4)
	for id := range count {
		binary.BigEndian.PutUint32(locations[id*4:], uint32(len(result)))
		if offsets[id] != offsets[id+1] {
			result = append(result, bodies[offsets[id]]...)
			result = append(result, make([]byte, (-len(result))&3)...)
		}
	}
	binary.BigEndian.PutUint32(locations[count*4:], uint32(len(result)))
	header := bytes.Clone(head)
	binary.BigEndian.PutUint16(header[50:], 1)
	tables["head"], tables["loca"], tables["glyf"] = header, locations, result
	return true, nil
}

// trueTypeGlyphSize 检查单个字形的完整记录长度及复合引用，不执行指令或分配坐标
// 入参: data 字形及尾部对齐数据, count 字形总数
// 返回: int 记录长度, []uint16 分量编号, error 结构错误
func trueTypeGlyphSize(data []byte, count int) (int, []uint16, error) {
	if len(data) < 10 {
		return 0, nil, fmt.Errorf("truncated glyph header")
	}
	contours := int(int16(binary.BigEndian.Uint16(data)))
	pos := 10
	if contours >= 0 {
		if contours == 0 && len(data) == 10 {
			return 10, nil, nil
		}
		if contours > (len(data)-12)/2 || len(data) < 12 {
			return 0, nil, fmt.Errorf("truncated contour endpoints")
		}
		points := 0
		for range contours {
			end := int(binary.BigEndian.Uint16(data[pos:])) + 1
			if end <= points {
				return 0, nil, fmt.Errorf("unordered contour endpoints")
			}
			points, pos = end, pos+2
		}
		instructions := int(binary.BigEndian.Uint16(data[pos:]))
		pos += 2
		if instructions > len(data)-pos {
			return 0, nil, fmt.Errorf("truncated glyph instructions")
		}
		pos += instructions
		coordinates := 0
		for seen := 0; seen < points; {
			if pos >= len(data) {
				return 0, nil, fmt.Errorf("truncated glyph flags")
			}
			flag, repeat := data[pos], 1
			pos++
			if flag&0x80 != 0 {
				return 0, nil, fmt.Errorf("reserved glyph flag")
			}
			if flag&8 != 0 {
				if pos >= len(data) {
					return 0, nil, fmt.Errorf("truncated glyph repeat")
				}
				repeat += int(data[pos])
				pos++
			}
			if repeat > points-seen {
				return 0, nil, fmt.Errorf("excessive glyph repeat")
			}
			for axis := range 2 {
				if flag&byte(2<<axis) != 0 {
					coordinates += repeat
				} else if flag&byte(16<<axis) == 0 {
					coordinates += 2 * repeat
				}
			}
			seen += repeat
		}
		if coordinates > len(data)-pos {
			return 0, nil, fmt.Errorf("truncated glyph coordinates")
		}
		return pos + coordinates, nil, nil
	}
	var refs []uint16
	instructions := false
	for {
		if len(data)-pos < 6 {
			return 0, nil, fmt.Errorf("truncated glyph component")
		}
		flags, id := binary.BigEndian.Uint16(data[pos:]), binary.BigEndian.Uint16(data[pos+2:])
		if int(id) >= count || flags&0xe010 != 0 {
			return 0, nil, fmt.Errorf("invalid glyph component")
		}
		refs = append(refs, id)
		pos += 6
		if flags&1 != 0 {
			pos += 2
		}
		switch flags & 0xc8 {
		case 0:
		case 8:
			pos += 2
		case 0x40:
			pos += 4
		case 0x80:
			pos += 8
		default:
			return 0, nil, fmt.Errorf("conflicting component transforms")
		}
		if pos > len(data) {
			return 0, nil, fmt.Errorf("truncated component arguments")
		}
		instructions = instructions || flags&0x100 != 0
		if flags&0x20 == 0 {
			break
		}
	}
	if instructions {
		if len(data)-pos < 2 {
			return 0, nil, fmt.Errorf("truncated component instructions")
		}
		length := int(binary.BigEndian.Uint16(data[pos:]))
		pos += 2
		if length > len(data)-pos {
			return 0, nil, fmt.Errorf("truncated component instructions")
		}
		pos += length
	}
	return pos, refs, nil
}

// trueTypePointCount 按完整轮廓计算虚拟点编号，局部复用分量计数，不执行字形指令
// 入参: tables 字体表, id 字形编号, counts 本次计数缓存, active 递归访问集合
// 返回: int 不含虚拟点的轮廓点数, error 结构或循环引用错误
func trueTypePointCount(tables map[string][]byte, id uint16, counts map[uint16]int, active map[uint16]bool) (int, error) {
	if count, ok := counts[id]; ok {
		return count, nil
	}
	maxp := tables["maxp"]
	if len(maxp) < 6 || id >= binary.BigEndian.Uint16(maxp[4:]) || active[id] {
		return 0, fmt.Errorf("invalid TrueType component reference %d", id)
	}
	active[id] = true
	defer delete(active, id)
	data, err := trueTypeGlyphData(tables, id)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		counts[id] = 0
		return 0, nil
	}
	_, refs, err := trueTypeGlyphSize(data, int(binary.BigEndian.Uint16(maxp[4:])))
	if err != nil {
		return 0, err
	}
	contours := int(int16(binary.BigEndian.Uint16(data)))
	count := 0
	if contours > 0 {
		count = int(binary.BigEndian.Uint16(data[10+2*(contours-1):])) + 1
	} else {
		for _, ref := range refs {
			points, err := trueTypePointCount(tables, ref, counts, active)
			if err != nil {
				return 0, err
			}
			count += points
			if count > 65536 {
				return 0, fmt.Errorf("excessive TrueType component points")
			}
		}
	}
	counts[id] = count
	return count, nil
}
