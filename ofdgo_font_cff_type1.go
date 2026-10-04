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

import "fmt"

// cffCharstringType 读取CFF字形程序类型，缺省为Type2
// 入参: dict 顶层字典
// 返回: int 字形程序类型, error 不支持的类型
func cffCharstringType(dict cffDict) (int, error) {
	values, ok := dict[1206]
	if !ok {
		return 2, nil
	}
	if len(values) != 1 || values[0] != 1 && values[0] != 2 {
		return 0, fmt.Errorf("unsupported CFF charstring type")
	}
	return int(values[0]), nil
}

// readCFFType1Program 读取Type1字形及无偏置局部子程序，保留源字形顺序
// 入参: data CFF字体, chars 字形程序, private 私有字典范围, cid 是否为CID字体
// 返回: *type1Program 执行资源, error 字符集或私有字典错误
func readCFFType1Program(data []byte, chars [][]byte, private []float64, cid bool) (*type1Program, error) {
	resources := &type2Font{}
	if _, err := readType2Private(data, private, resources); err != nil {
		return nil, err
	}
	program := &type1Program{subrs: resources.locals, lenIV: -1}
	if cid {
		return program, nil
	}
	identifiers, _, _, stringsOffset, ok := getCFFCharsetInfo(data, len(chars))
	if !ok {
		return nil, fmt.Errorf("invalid CFF Type1 charset")
	}
	strings, _, err := readType2Index(data, stringsOffset)
	if err != nil {
		return nil, err
	}
	program.glyphs = make([]type1Glyph, len(chars))
	for gid, charstring := range chars {
		name := ".notdef"
		if gid != 0 {
			sid := identifiers[gid]
			if sid < len(cffStandardStrings) {
				name = cffStandardStrings[sid]
			} else if sid-len(cffStandardStrings) < len(strings) {
				name = string(strings[sid-len(cffStandardStrings)])
			} else {
				return nil, fmt.Errorf("invalid CFF Type1 glyph name")
			}
			if name == "" {
				return nil, fmt.Errorf("invalid CFF Type1 glyph name")
			}
		}
		program.glyphs[gid] = type1Glyph{name: name, data: charstring}
	}
	return program, nil
}

// normalizeCFFType1 转换非CID字体的Type1程序，保留字符集、编码及字形编号
// 入参: data CFF字体, dict 顶层字典
// 返回: []byte Type2格式CFF, error 字形程序或范围错误
func normalizeCFFType1(data []byte, dict cffDict) ([]byte, error) {
	_, nameSize := getCFFIndexCount(data, int(data[2]))
	topStart := int(data[2]) + nameSize
	_, topSize := getCFFIndexCount(data, topStart)
	charStart, err := cffDictOffset(data, dict, 17)
	if err != nil {
		return nil, err
	}
	chars, charEnd, err := readType2Index(data, charStart)
	if err != nil || len(chars) == 0 || charStart < topStart+topSize {
		return nil, fmt.Errorf("invalid CFF Type1 charstrings")
	}
	program, err := readCFFType1Program(data, chars, dict[18], false)
	if err != nil {
		return nil, err
	}
	converted := make([][]byte, len(chars))
	for gid, glyph := range program.glyphs {
		converted[gid], err = program.outline(glyph)
		if err != nil {
			return nil, err
		}
	}
	dict[1206], dict[18] = []float64{2}, []float64{0, 0}
	return replaceCFFCharstrings(data, dict, topStart, topSize, charStart, charEnd-charStart, converted, nil), nil
}
