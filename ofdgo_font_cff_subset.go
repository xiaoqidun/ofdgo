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
	"maps"
)

// subsetNamedCFFFont 裁剪非CID的CFF字体，保留字形编号、组合组件和度量
// 入参: sfnt 已解析字体, glyphs 使用的字形
// 返回: []byte 字体子集, error 裁剪错误
func subsetNamedCFFFont(sfnt *sfntFont, glyphs []uint16) ([]byte, error) {
	keep := map[uint16]bool{0: true}
	for _, gid := range glyphs {
		if gid >= sfnt.NumGlyphs() {
			return nil, fmt.Errorf("CFF glyph index out of range")
		}
		keep[gid] = true
	}
	data, err := subsetCFFCharstrings(sfnt.Tables["CFF "], keep)
	if err != nil {
		return bytes.Clone(sfnt.Write()), nil
	}
	tables := maps.Clone(sfnt.Tables)
	tables["CFF "] = data
	mapping := make(map[rune]uint16)
	for gid := range keep {
		for _, char := range sfnt.glyphCharacters(gid) {
			mapping[char] = gid
		}
	}
	tables["cmap"] = buildCmapTable(sfnt.NumGlyphs(), mapping)
	for _, tag := range []string{"DSIG", "GSUB", "GPOS", "GDEF"} {
		delete(tables, tag)
	}
	return serializeOTF(tables)
}

// subsetCFFCharstrings 清空未引用的Type2轮廓及子程序，保留原字宽、提示和编号
// 入参: data 非CID的CFF数据, keep 保留的字形，调用后包含组合组件
// 返回: []byte 重建的CFF数据, error 结构或依赖错误
func subsetCFFCharstrings(data []byte, keep map[uint16]bool) ([]byte, error) {
	font, err := readType2Font(data)
	if err != nil {
		return nil, err
	}
	keep[0] = true
	locals := make([]bool, len(font.locals))
	globals := make([]bool, len(font.globals))
	var stack [48]float64
	for gid := range keep {
		if int(gid) >= len(font.chars) {
			return nil, fmt.Errorf("CFF glyph index out of range")
		}
		steps := 0
		state := type2State{font: font, args: stack[:0], width: font.def, seed: font.seed + uint64(gid), steps: &steps, dependencies: keep, localUsage: locals, globalUsage: globals}
		if _, err := state.run(font.chars[gid], 0); err != nil {
			return nil, err
		}
	}
	chars := make([][]byte, len(font.chars))
	empty := make(map[float64][]byte)
	for gid, program := range font.chars {
		if keep[uint16(gid)] {
			chars[gid] = program
			continue
		}
		steps := 0
		state := type2State{font: font, args: stack[:0], width: font.def, widthOnly: true, seed: font.seed + uint64(gid), steps: &steps, dependencies: keep}
		if _, err := state.run(program, 0); err != nil {
			return nil, err
		}
		if encoded, ok := empty[state.width]; ok {
			chars[gid] = encoded
			continue
		}
		var encoded bytes.Buffer
		if state.width != font.def {
			value := state.width - font.nominal
			if err := encodeType2Number(&encoded, value); err != nil {
				return nil, err
			}
			if parseNumberType2(encoded.Bytes(), 0) != value {
				return nil, fmt.Errorf("CFF width cannot be represented exactly")
			}
		}
		encoded.WriteByte(14)
		chars[gid] = encoded.Bytes()
		empty[state.width] = chars[gid]
	}
	font.locals = subsetCFFSubroutines(font.locals, locals)
	font.globals = subsetCFFSubroutines(font.globals, globals)
	return rebuildNamedCFF(data, font, chars)
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

// rebuildNamedCFF 重建非CID的CFF偏移，保留字符集、编码和私有提示参数
// 入参: data 原始CFF数据, font 执行资源, chars 替换后的字形程序
// 返回: []byte 重建的CFF数据, error 不支持的结构
func rebuildNamedCFF(data []byte, font *type2Font, chars [][]byte) ([]byte, error) {
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
	sids, _, _, _, ok := getCFFCharsetInfo(data, len(chars))
	if !ok {
		return nil, fmt.Errorf("invalid CFF charset")
	}
	charset := []byte{0}
	for _, sid := range sids[1:] {
		charset = binary.BigEndian.AppendUint16(charset, uint16(sid))
	}
	encoding, err := cffSubsetEncoding(data, dict)
	if err != nil {
		return nil, err
	}
	private, err := readType2Private(data, dict[18], &type2Font{})
	if err != nil {
		return nil, err
	}
	for op := range private {
		switch op {
		case 6, 7, 8, 9, 10, 11, 19, 20, 21, 1209, 1210, 1211, 1212, 1213, 1214, 1217, 1218, 1219:
		default:
			return nil, fmt.Errorf("unsupported CFF private operator %d", op)
		}
	}
	privateData := encodeCFFDict(private)
	if _, ok := private[19]; ok {
		for private[19][0] != float64(len(privateData)) {
			private[19] = []float64{float64(len(privateData))}
			privateData = encodeCFFDict(private)
		}
	}
	prefix := bytes.Clone(data[:int(data[2])])
	prefix = append(prefix, encodeCFFIndex(names)...)
	suffix := encodeCFFIndex(strings)
	suffix = append(suffix, encodeCFFIndex(font.globals)...)
	encodedChars := encodeCFFIndex(chars)
	var top []byte
	for attempt := 0; attempt < 16; attempt++ {
		top = encodeCFFIndex([][]byte{encodeCFFDict(dict)})
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
		encoded := encodeCFFIndex([][]byte{encodeCFFDict(updated)})
		if len(encoded) == len(top) {
			result := prefix
			for _, part := range [][]byte{encoded, suffix, charset, encoding, encodedChars, privateData, encodeCFFIndex(font.locals)} {
				result = append(result, part...)
			}
			return result, nil
		}
		dict = updated
	}
	return nil, fmt.Errorf("CFF dictionary offsets did not converge")
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
