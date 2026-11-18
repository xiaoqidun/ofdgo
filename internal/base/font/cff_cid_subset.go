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
)

// cidSubsetFont 保存CID字体裁剪所需的字典、映射和执行资源
type cidSubsetFont struct {
	prefix  []byte
	strings [][]byte
	top     cffDict
	dicts   []cffDict
	private []cffDict
	fonts   []*type2Font
	selects []int
	charset []byte
}

// write 重建CID字体偏移，保留CID映射、字体矩阵及各字典的私有参数
// 入参: chars 裁剪后的字形程序
// 返回: []byte CFF数据, error 偏移或字典错误
func (f *cidSubsetFont) write(chars [][]byte) ([]byte, error) {
	suffix := append(encodeCFFIndex(f.strings), encodeCFFIndex(f.fonts[0].globals)...)
	selects := encodeCFFSubsetFDSelect(f.selects)
	programs := encodeCFFIndex(chars)
	private := make([][]byte, len(f.fonts))
	locals := make([][]byte, len(f.fonts))
	fds := make([][]byte, len(f.fonts))
	for index, font := range f.fonts {
		var err error
		private[index], err = encodeCFFSubsetPrivate(f.private[index])
		if err != nil {
			return nil, err
		}
		if _, ok := f.private[index][19]; ok {
			locals[index] = encodeCFFIndex(font.locals)
		}
		fds[index] = encodeCFFDict(f.dicts[index])
	}
	for range 16 {
		top := encodeCFFIndex([][]byte{encodeCFFDict(f.top)})
		array := encodeCFFIndex(fds)
		offset := len(f.prefix) + len(top) + len(suffix)
		f.top[15] = []float64{float64(offset)}
		offset += len(f.charset)
		f.top[1237] = []float64{float64(offset)}
		offset += len(selects)
		f.top[17] = []float64{float64(offset)}
		offset += len(programs)
		f.top[1236] = []float64{float64(offset)}
		offset += len(array)
		for index := range f.fonts {
			f.dicts[index][18] = []float64{float64(len(private[index])), float64(offset)}
			fds[index] = encodeCFFDict(f.dicts[index])
			offset += len(private[index]) + len(locals[index])
		}
		updatedTop := encodeCFFIndex([][]byte{encodeCFFDict(f.top)})
		updatedArray := encodeCFFIndex(fds)
		if len(updatedTop) != len(top) || len(updatedArray) != len(array) {
			continue
		}
		result := make([]byte, 0, offset)
		for _, part := range [][]byte{f.prefix, updatedTop, suffix, f.charset, selects, programs, updatedArray} {
			result = append(result, part...)
		}
		for index := range f.fonts {
			result = append(result, private[index]...)
			result = append(result, locals[index]...)
		}
		return result, nil
	}
	return nil, fmt.Errorf("CID CFF offsets did not converge")
}

// readCIDSubsetFont 读取CID字体的原始字典及逐字形归属，不合并字体字典
// 入参: data CFF数据
// 返回: *cidSubsetFont 裁剪资源, error 结构错误或不支持的字典
func readCIDSubsetFont(data []byte) (*cidSubsetFont, error) {
	if !isBareCFFData(data) {
		return nil, fmt.Errorf("invalid CFF header")
	}
	names, end, err := readType2Index(data, int(data[2]))
	if err != nil || len(names) != 1 {
		return nil, fmt.Errorf("invalid CFF name index")
	}
	tops, end, err := readType2Index(data, end)
	if err != nil || len(tops) != 1 {
		return nil, fmt.Errorf("invalid CFF top index")
	}
	strings, end, err := readType2Index(data, end)
	if err != nil {
		return nil, err
	}
	globals, _, err := readType2Index(data, end)
	if err != nil {
		return nil, err
	}
	top, err := readCFFDict(tops[0])
	if err != nil {
		return nil, err
	}
	ros := top[1230]
	if len(ros) != 3 {
		return nil, fmt.Errorf("invalid CFF ROS")
	}
	for index, value := range ros {
		limit := float64(math.MaxInt32)
		if index < 2 {
			limit = float64(min(64999, 390+len(strings)))
		}
		if !finite(value) || value < 0 || value > limit || value != math.Trunc(value) {
			return nil, fmt.Errorf("invalid CFF ROS operand")
		}
	}
	for op := range top {
		switch op {
		case 0, 1, 2, 3, 4, 5, 13, 14, 15, 17, 1200, 1201, 1202, 1203, 1204, 1205, 1206, 1207, 1208, 1230, 1231, 1232, 1233, 1234, 1236, 1237:
		default:
			return nil, fmt.Errorf("unsupported CID CFF top operator %d", op)
		}
	}
	if value, ok := top[1206]; ok && (len(value) != 1 || value[0] != 2) {
		return nil, fmt.Errorf("unsupported CFF charstring type")
	}
	if _, err := readCFFMatrix(top, [6]float64{.001, 0, 0, .001, 0, 0}); err != nil {
		return nil, err
	}
	charOffset, err := cffDictOffset(data, top, 17)
	if err != nil {
		return nil, err
	}
	chars, _, err := readType2Index(data, charOffset)
	if err != nil || len(chars) == 0 {
		return nil, fmt.Errorf("invalid CFF charstrings")
	}
	charsetOffset, err := cffDictOffset(data, top, 15)
	if err != nil || charsetOffset <= 2 {
		return nil, fmt.Errorf("invalid CID CFF charset offset")
	}
	cids, err := parseCFFCharset(data, charsetOffset, len(chars))
	if err != nil {
		return nil, err
	}
	fdOffset, err := cffDictOffset(data, top, 1236)
	if err != nil {
		return nil, err
	}
	fds, _, err := readType2Index(data, fdOffset)
	if err != nil || len(fds) == 0 || len(fds) > 256 {
		return nil, fmt.Errorf("invalid CFF font dictionary count")
	}
	selectOffset, err := cffDictOffset(data, top, 1237)
	if err != nil {
		return nil, err
	}
	selects, err := readType2FDSelect(data, selectOffset, len(chars), len(fds))
	if err != nil {
		return nil, err
	}
	if data[selectOffset] == 3 && binary.BigEndian.Uint16(data[selectOffset+3:]) != 0 {
		return nil, fmt.Errorf("CFF FDSelect must include glyph zero")
	}
	font := &cidSubsetFont{
		prefix:  append(bytes.Clone(data[:int(data[2])]), encodeCFFIndex(names)...),
		strings: strings, top: top, selects: selects, charset: encodeCFFSubsetCharset(cids),
		dicts: make([]cffDict, len(fds)), private: make([]cffDict, len(fds)), fonts: make([]*type2Font, len(fds)),
	}
	for index, fd := range fds {
		dict, err := readCFFDict(fd)
		if err != nil {
			return nil, err
		}
		for op := range dict {
			switch op {
			case 18, 1207, 1238:
			default:
				return nil, fmt.Errorf("unsupported CFF font dictionary operator %d", op)
			}
		}
		if _, err := readCFFMatrix(dict, [6]float64{1, 0, 0, 1, 0, 0}); err != nil {
			return nil, err
		}
		if len(dict[18]) != 2 {
			return nil, fmt.Errorf("missing CFF private dictionary")
		}
		resource := &type2Font{data: data, chars: chars, globals: globals, seed: 1, cid: true}
		private, err := readType2Private(data, dict[18], resource)
		if err != nil {
			return nil, err
		}
		if _, err := encodeCFFSubsetPrivate(private); err != nil {
			return nil, err
		}
		font.dicts[index], font.private[index], font.fonts[index] = dict, private, resource
	}
	return font, nil
}

// encodeCFFSubsetFDSelect 选择较短的逐字形或分段格式保存字体字典归属
// 入参: selects 字形所属字典
// 返回: []byte FDSelect数据
func encodeCFFSubsetFDSelect(selects []int) []byte {
	ranges := []byte{3, 0, 0}
	count := 0
	for gid, fd := range selects {
		if gid == 0 || fd != selects[gid-1] {
			ranges = binary.BigEndian.AppendUint16(ranges, uint16(gid))
			ranges = append(ranges, byte(fd))
			count++
		}
	}
	binary.BigEndian.PutUint16(ranges[1:], uint16(count))
	ranges = binary.BigEndian.AppendUint16(ranges, uint16(len(selects)))
	if len(ranges) < len(selects)+1 {
		return ranges
	}
	result := make([]byte, len(selects)+1)
	for gid, fd := range selects {
		result[gid+1] = byte(fd)
	}
	return result
}
