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
	"fmt"
	"maps"
	"slices"
)

// subsetCFFFont 裁剪CFF字体，保留组合组件和度量，按需重排字形并在保留编号时维护排版表
// 入参: sfnt 已解析字体, glyphs 使用的字形, preserve 是否保留原字形编号
// 返回: []byte 字体子集, error 裁剪错误
func subsetCFFFont(sfnt *SFNT, glyphs []uint16, preserve bool) ([]byte, error) {
	keep := map[uint16]bool{0: true}
	for _, gid := range glyphs {
		if gid >= sfnt.NumGlyphs() {
			return nil, fmt.Errorf("CFF glyph index out of range")
		}
		keep[gid] = true
	}
	if !preserve {
		if compact, ids, err := compactCFFDirect(sfnt.Tables["CFF "], int(sfnt.NumGlyphs()), keep); err == nil {
			if result, err := compactCFFTables(sfnt, compact, ids, nil, nil); err == nil {
				return result, nil
			}
		}
	}
	retainLayout := false
	if preserve {
		retainLayout = retainSFNTSubstitutions(sfnt.Tables["GSUB"], keep, sfnt.NumGlyphs())
	}
	data, err := subsetCFFCharstrings(sfnt.Tables["CFF "], keep)
	if err != nil {
		return bytes.Clone(sfnt.Write()), nil
	}
	if !preserve {
		if compact, ids, err := compactCFFGlyphs(data, keep); err == nil {
			return compactCFFTables(sfnt, compact, ids, nil, nil)
		}
	}
	tables := maps.Clone(sfnt.Tables)
	tables["CFF "] = data
	mapping := make(map[rune]uint16)
	for gid := range keep {
		for _, char := range sfnt.glyphCharacters(gid) {
			mapping[char] = gid
		}
	}
	tables["cmap"] = BuildCmapTable(sfnt.NumGlyphs(), mapping)
	dropTransientSubsetTables(tables)
	if !preserve || !retainLayout {
		for _, tag := range []string{"GSUB", "GPOS", "GDEF"} {
			delete(tables, tag)
		}
	}
	return SerializeOTF(tables)
}

// subsetCFFCharstrings 清空未引用的Type2轮廓及子程序，保留原字宽、提示和编号
// 入参: data CFF数据, keep 保留的字形，调用后包含组合组件
// 返回: []byte 重建的CFF数据, error 结构或依赖错误
func subsetCFFCharstrings(data []byte, keep map[uint16]bool) ([]byte, error) {
	font, err := readType2Font(data)
	if err != nil {
		cid, err := readCIDSubsetFont(data)
		if err != nil {
			return nil, err
		}
		chars, err := subsetType2Programs(cid.fonts, cid.selects, keep)
		if err != nil {
			return nil, err
		}
		return cid.write(chars)
	}
	chars, err := subsetType2Programs([]*type2Font{font}, nil, keep)
	if err != nil {
		return nil, err
	}
	return rebuildNamedCFF(data, font, chars, nil, false)
}

// normalizeCFFCharstring 将空Type2字形程序规范化为合法的endchar指令
// 入参: program 原始字形程序
// 返回: []byte 规范化后的字形程序
func normalizeCFFCharstring(program []byte) []byte {
	if len(program) == 0 {
		return []byte{14}
	}
	return program
}

// collectType2Dependencies 仅对保留字形及组合组件运行Type2虚拟机并收集子程序引用
// 入参: fonts 执行资源, selects 字形所属字典，单字典时可为空, keep 保留字形集合，调用后包含组合组件
// 返回: [][]bool 各字典局部子程序使用标记, []bool 全局子程序使用标记, error 执行或越界错误
func collectType2Dependencies(fonts []*type2Font, selects []int, keep map[uint16]bool) ([][]bool, []bool, error) {
	keep[0] = true
	programs := fonts[0].chars
	locals := make([][]bool, len(fonts))
	for index, font := range fonts {
		locals[index] = make([]bool, len(font.locals))
	}
	globals := make([]bool, len(fonts[0].globals))
	visited := make(map[uint16]bool, len(keep))
	queue := make([]uint16, 0, len(keep))
	for gid, retained := range keep {
		if retained {
			queue = append(queue, gid)
		}
	}
	slices.Sort(queue)
	var stack [48]float64
	for len(queue) > 0 {
		gid := queue[0]
		queue = queue[1:]
		if visited[gid] {
			continue
		}
		if int(gid) >= len(programs) {
			return nil, nil, fmt.Errorf("CFF glyph index out of range")
		}
		visited[gid] = true
		fd := 0
		if selects != nil {
			fd = selects[gid]
		}
		font := fonts[fd]
		steps := 0
		prevLen := len(keep)
		state := type2State{font: font, args: stack[:0], width: font.def, seed: font.seed + uint64(gid), steps: &steps, dependencies: keep, localUsage: locals[fd], globalUsage: globals}
		if _, err := state.run(normalizeCFFCharstring(font.chars[gid]), 0); err != nil {
			return nil, nil, err
		}
		if len(keep) > prevLen {
			var added []uint16
			for dep, retained := range keep {
				if retained && !visited[dep] {
					added = append(added, dep)
				}
			}
			slices.Sort(added)
			queue = append(queue, added...)
		}
	}
	return locals, globals, nil
}

// subsetType2Programs 按字形所属字典收集子程序依赖并清空未使用的轮廓
// 入参: fonts 执行资源, selects 字形所属字典，单字典时可为空, keep 保留的字形
// 返回: [][]byte 字形程序, error 执行或字宽错误
func subsetType2Programs(fonts []*type2Font, selects []int, keep map[uint16]bool) ([][]byte, error) {
	locals, globals, err := collectType2Dependencies(fonts, selects, keep)
	if err != nil {
		return nil, err
	}
	programs := fonts[0].chars
	empty := make([]map[float64][]byte, len(fonts))
	for index := range fonts {
		empty[index] = make(map[float64][]byte)
	}
	var stack [48]float64
	chars := make([][]byte, len(programs))
	for gid, program := range programs {
		normalized := normalizeCFFCharstring(program)
		if keep[uint16(gid)] {
			chars[gid] = normalized
			continue
		}
		fd := 0
		if selects != nil {
			fd = selects[gid]
		}
		font := fonts[fd]
		steps := 0
		state := type2State{font: font, args: stack[:0], width: font.def, widthOnly: true, seed: font.seed + uint64(gid), steps: &steps, dependencies: keep}
		if _, err := state.run(normalized, 0); err != nil {
			return nil, err
		}
		if encoded, ok := empty[fd][state.width]; ok {
			chars[gid] = encoded
			continue
		}
		var encoded bytes.Buffer
		if state.width != font.def {
			value := state.width - font.nominal
			divisor := 1
			if value < -32768 || value >= 32768 {
				divisor = 2
			}
			for range divisor {
				if err := encodeType2Number(&encoded, value/float64(divisor)); err != nil {
					return nil, err
				}
			}
			if parseNumberType2(encoded.Bytes(), 0)*float64(divisor) != value {
				return nil, fmt.Errorf("CFF width cannot be represented exactly")
			}
			if divisor == 2 {
				encoded.Write([]byte{12, 10})
			}
		}
		encoded.WriteByte(14)
		chars[gid] = encoded.Bytes()
		empty[fd][state.width] = chars[gid]
	}
	globalPrograms := subsetCFFSubroutines(fonts[0].globals, globals)
	for index, font := range fonts {
		font.locals = subsetCFFSubroutines(font.locals, locals[index])
		font.globals = globalPrograms
	}
	return chars, nil
}

// subsetCFFSubroutines 将未引用的子程序替换为空返回，保持调用编号及偏移基数
// 入参: programs 子程序索引, used 引用标记
// 返回: [][]byte 裁剪后的子程序索引
func subsetCFFSubroutines(programs [][]byte, used []bool) [][]byte {
	result := make([][]byte, len(programs))
	empty := []byte{11}
	for index, program := range programs {
		if used[index] {
			result[index] = program
		} else {
			result[index] = empty
		}
	}
	return result
}

// rebuildNamedCFF 重建非CID的CFF偏移，保留字符集、私有提示参数并按需保留编码
// 入参: data 原始CFF数据, font 执行资源, chars 替换后的字形程序, ids 字形重排顺序，nil时保留原编号, dropEncoding 是否移除自定义编码表
// 返回: []byte 重建的CFF数据, error 不支持的结构
func rebuildNamedCFF(data []byte, font *type2Font, chars [][]byte, ids []uint16, dropEncoding bool) ([]byte, error) {
	names, end, err := readType2Index(data, int(data[2]))
	if err != nil {
		return nil, err
	}
	tops, end, err := readType2Index(data, end)
	if err != nil || len(tops) != 1 {
		return nil, fmt.Errorf("invalid CFF top dictionary")
	}
	strings, _, err := readType2Index(data, end)
	if err != nil {
		return nil, err
	}
	dict, err := readCFFDict(tops[0])
	if err != nil {
		return nil, err
	}
	for op := range dict {
		switch op {
		case 0, 1, 2, 3, 4, 5, 13, 14, 15, 16, 17, 18, 1200, 1201, 1202, 1203, 1204, 1205, 1206, 1207, 1208, 1222, 1223:
		default:
			return nil, fmt.Errorf("unsupported CFF top operator %d", op)
		}
	}
	sids, _, _, _, ok := getCFFCharsetInfo(data, len(font.chars))
	if !ok {
		return nil, fmt.Errorf("invalid CFF charset")
	}
	var encoding []byte
	if dropEncoding {
		delete(dict, 16)
	} else {
		encoding, err = cffSubsetEncoding(data, dict)
		if err != nil {
			return nil, err
		}
	}
	if ids != nil {
		if encoding != nil {
			codes, err := readCFFSubsetEncoding(encoding, sids)
			if err != nil {
				return nil, err
			}
			encoding, err = encodeCFFSubsetEncoding(codes, sids, ids)
			if err != nil {
				return nil, err
			}
		}
		selected := make([]int, len(ids))
		for index, gid := range ids {
			selected[index] = sids[gid]
		}
		sids = selected
	}
	charset := encodeCFFSubsetCharset(sids)
	private, err := readType2Private(data, dict[18], &type2Font{})
	if err != nil {
		return nil, err
	}
	if err := normalizeCFFPrivate(private); err != nil {
		return nil, err
	}
	privateData, err := encodeCFFSubsetPrivate(private)
	if err != nil {
		return nil, err
	}
	nameIndex, err := encodeCFFIndexChecked(names)
	if err != nil {
		return nil, err
	}
	stringIndex, err := encodeCFFIndexChecked(strings)
	if err != nil {
		return nil, err
	}
	globalIndex, err := encodeCFFIndexChecked(font.globals)
	if err != nil {
		return nil, err
	}
	encodedChars, err := encodeCFFIndexChecked(chars)
	if err != nil {
		return nil, err
	}
	localIndex, err := encodeCFFIndexChecked(font.locals)
	if err != nil {
		return nil, err
	}
	prefix := bytes.Clone(data[:int(data[2])])
	prefix = append(prefix, nameIndex...)
	suffix := append(stringIndex, globalIndex...)
	var top []byte
	for attempt := 0; attempt < 16; attempt++ {
		top, err = encodeCFFIndexChecked([][]byte{encodeCFFDict(dict)})
		if err != nil {
			return nil, err
		}
		offset := len(prefix) + len(top) + len(suffix)
		updated := maps.Clone(dict)
		updated[15] = []float64{float64(offset)}
		offset += len(charset)
		if encoding != nil {
			updated[16] = []float64{float64(offset)}
		}
		offset += len(encoding)
		updated[17] = []float64{float64(offset)}
		updated[18] = []float64{float64(len(privateData)), float64(offset + len(encodedChars))}
		encoded, err := encodeCFFIndexChecked([][]byte{encodeCFFDict(updated)})
		if err != nil {
			return nil, err
		}
		if len(encoded) == len(top) {
			result := prefix
			for _, part := range [][]byte{encoded, suffix, charset, encoding, encodedChars, privateData, localIndex} {
				result = append(result, part...)
			}
			return result, nil
		}
		dict = updated
	}
	return nil, fmt.Errorf("CFF dictionary offsets did not converge")
}

// encodeCFFSubsetPrivate 保留私有字典参数，将局部子程序安排在字典之后
// 入参: private 私有字典
// 返回: []byte 字典数据, error 不支持的参数或偏移错误
func encodeCFFSubsetPrivate(private cffDict) ([]byte, error) {
	for op := range private {
		switch op {
		case 6, 7, 8, 9, 10, 11, 19, 20, 21, 1209, 1210, 1211, 1212, 1213, 1214, 1217, 1218, 1219:
		default:
			return nil, fmt.Errorf("unsupported CFF private operator %d", op)
		}
	}
	for range 16 {
		data := encodeCFFDict(private)
		value, ok := private[19]
		if !ok || len(value) == 1 && value[0] == float64(len(data)) {
			return data, nil
		}
		private[19] = []float64{float64(len(data))}
	}
	return nil, fmt.Errorf("CFF private offset did not converge")
}

// cffSubsetEncoding 读取自定义CFF编码及补充映射，预定义编码无需复制
// 入参: data CFF数据, dict 顶层字典
// 返回: []byte 编码数据, error 编码格式错误
func cffSubsetEncoding(data []byte, dict cffDict) ([]byte, error) {
	values := dict[16]
	if values == nil || len(values) == 1 && (values[0] == 0 || values[0] == 1) {
		return nil, nil
	}
	start, err := cffDictOffset(data, dict, 16)
	if err != nil || start > len(data)-2 {
		return nil, fmt.Errorf("invalid CFF encoding offset")
	}
	end := start + 2
	switch data[start] & 0x7f {
	case 0:
		end += int(data[start+1])
	case 1:
		end += 2 * int(data[start+1])
	default:
		return nil, fmt.Errorf("invalid CFF encoding format")
	}
	if data[start]&0x80 != 0 {
		if end >= len(data) {
			return nil, fmt.Errorf("truncated CFF encoding supplements")
		}
		end += 1 + 3*int(data[end])
	}
	if end > len(data) {
		return nil, fmt.Errorf("truncated CFF encoding")
	}
	return data[start:end], nil
}
