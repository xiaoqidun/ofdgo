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
	"math"

	"github.com/go-text/typesetting/font/cff"
	"github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
)

// defaultCFF2Font 将CFF2默认实例转换为静态OpenType字体，保留字形编号和三次轮廓
// 入参: data 独立字体数据
// 返回: []byte 字体数据，非CFF2原样返回, error 解析或编码错误
func defaultCFF2Font(data []byte) (result []byte, err error) {
	if len(data) < 12 || string(data[:4]) != "OTTO" {
		return data, nil
	}
	found := false
	for i, count := 0, int(binary.BigEndian.Uint16(data[4:])); i < count && i < (len(data)-12)/16; i++ {
		if string(data[12+i*16:16+i*16]) == "CFF2" {
			found = true
			break
		}
	}
	if !found {
		return data, nil
	}
	table, err := fontFileTables(data, 0)
	if err != nil || table["CFF2"] == nil {
		return data, err
	}
	if table["CFF "] != nil || table["glyf"] != nil || len(table["head"]) < 54 || len(table["maxp"]) < 6 || len(table["hhea"]) < 36 {
		return nil, fmt.Errorf("invalid CFF2 font tables")
	}
	defer func() {
		if failure := recover(); failure != nil {
			result, err = nil, fmt.Errorf("invalid CFF2 font: %v", failure)
		}
	}()
	cffData := table["CFF2"]
	if len(cffData) < 5 || cffData[0] != 2 || cffData[1] != 0 || cffData[2] < 5 || binary.BigEndian.Uint32(table["maxp"]) != 0x5000 {
		return nil, fmt.Errorf("invalid CFF2 header")
	}
	start := int(cffData[2])
	end := start + int(binary.BigEndian.Uint16(cffData[3:]))
	if end > len(cffData) {
		return nil, fmt.Errorf("invalid CFF2 dictionary length")
	}
	dict, err := readCFFDictionary(cffData[start:end], true)
	if err != nil {
		return nil, err
	}
	matrix, err := readCFFMatrix(dict, [6]float64{.001, 0, 0, .001, 0, 0})
	units := binary.BigEndian.Uint16(table["head"][18:])
	if err != nil || units < 16 || units > 16384 || matrix[0] != matrix[3] || matrix[1] != 0 || matrix[2] != 0 || matrix[4] != 0 || matrix[5] != 0 || math.Abs(matrix[0]*float64(units)-1) > 1e-6 {
		return nil, fmt.Errorf("CFF2 FontMatrix does not match design units")
	}
	program, err := cff.ParseCFF2(table["CFF2"])
	if err != nil {
		return nil, err
	}
	if len(dict[24]) != 0 {
		fvar := table["fvar"]
		if len(fvar) < 16 || binary.BigEndian.Uint32(fvar) != 0x10000 || int(binary.BigEndian.Uint16(fvar[8:])) != program.VarStore.AxisCount() {
			return nil, fmt.Errorf("CFF2 variation axis count mismatch")
		}
	}
	count := int(binary.BigEndian.Uint16(table["maxp"][4:]))
	metrics := int(binary.BigEndian.Uint16(table["hhea"][34:]))
	if count == 0 || len(program.Charstrings) != count || metrics == 0 || metrics > count || len(table["hmtx"]) < metrics*4+(count-metrics)*2 {
		return nil, fmt.Errorf("invalid CFF2 glyph metrics")
	}
	chars := make([][]byte, count)
	for gid := range chars {
		segments, _, err := program.LoadGlyph(tables.GlyphID(gid), nil)
		if err != nil {
			return nil, fmt.Errorf("CFF2 glyph %d: %w", gid, err)
		}
		width := binary.BigEndian.Uint16(table["hmtx"][min(gid, metrics-1)*4:])
		chars[gid], err = cffOutlineProgram(segments, width)
		if err != nil {
			return nil, fmt.Errorf("CFF2 glyph %d: %w", gid, err)
		}
	}
	table["CFF "], err = staticCIDFont(table, chars)
	if err != nil {
		return nil, err
	}
	for _, tag := range []string{"CFF2", "fvar", "avar", "STAT", "HVAR", "VVAR", "MVAR", "DSIG"} {
		delete(table, tag)
	}
	return serializeOTF(table)
}

// cffOutlineProgram 将默认设计轮廓编码为无子程序的Type2字形
// 入参: segments 字体设计坐标路径, width 水平字宽
// 返回: []byte 字形程序, error 坐标超出编码范围
func cffOutlineProgram(segments []opentype.Segment, width uint16) ([]byte, error) {
	var output bytes.Buffer
	if width < 32768 {
		if err := encodeType2Number(&output, float64(width)); err != nil {
			return nil, err
		}
	} else {
		for range 2 {
			if err := encodeType2Number(&output, float64(width)/2); err != nil {
				return nil, err
			}
		}
		output.Write([]byte{12, 10})
	}
	var x, y float64
	for _, segment := range segments {
		count, op := 1, byte(21)
		switch segment.Op {
		case opentype.SegmentOpMoveTo:
		case opentype.SegmentOpLineTo:
			op = 5
		case opentype.SegmentOpCubeTo:
			count, op = 3, 8
		default:
			return nil, fmt.Errorf("invalid CFF2 path operation")
		}
		for _, point := range segment.Args[:count] {
			nx, ny := float64(point.X), float64(point.Y)
			if err := encodeType2Number(&output, nx-x); err != nil {
				return nil, err
			}
			if err := encodeType2Number(&output, ny-y); err != nil {
				return nil, err
			}
			x, y = nx, ny
		}
		output.WriteByte(op)
	}
	output.WriteByte(14)
	return output.Bytes(), nil
}

// staticCIDFont 封装独立Type2字形，CID与字形编号一致，不依赖字形名称
// 入参: table OpenType表, chars 字形程序
// 返回: []byte CFF字体, error 字体结构错误
func staticCIDFont(table map[string][]byte, chars [][]byte) ([]byte, error) {
	if len(chars) == 0 || len(chars) > 65535 || len(table["head"]) < 54 {
		return nil, fmt.Errorf("invalid static CFF glyph count")
	}
	units := binary.BigEndian.Uint16(table["head"][18:])
	if units < 16 || units > 16384 {
		return nil, fmt.Errorf("invalid CFF design units")
	}
	name := fontFaceInfo(table["name"], 0).PostScriptName
	if name == "" {
		name = "OpenTypeInstance"
	}
	names := encodeCFFIndex([][]byte{[]byte(name)})
	strings := encodeCFFIndex([][]byte{[]byte("Adobe"), []byte("Identity")})
	charset := []byte{0}
	for gid := 1; gid < len(chars); gid++ {
		charset = binary.BigEndian.AppendUint16(charset, uint16(gid))
	}
	selects := []byte{3, 0, 1, 0, 0, 0}
	selects = binary.BigEndian.AppendUint16(selects, uint16(len(chars)))
	fonts := encodeCFFIndex([][]byte{encodeCFFDict(cffDict{18: {0, 0}})})
	charIndex := encodeCFFIndex(chars)
	scale := 1 / float64(units)
	dict := cffDict{1230: {391, 392, 0}, 1234: {float64(len(chars))}, 1207: {scale, 0, 0, scale, 0, 0}, 15: {0}, 17: {0}, 1236: {0}, 1237: {0}}
	for i := 0; i < 4; i++ {
		dict[5] = append(dict[5], float64(int16(binary.BigEndian.Uint16(table["head"][36+2*i:]))))
	}
	var top []byte
	for range 16 {
		top = encodeCFFIndex([][]byte{encodeCFFDict(dict)})
		offset := 4 + len(names) + len(top) + len(strings) + 2
		if dict[15][0] == float64(offset) {
			var output bytes.Buffer
			for _, part := range [][]byte{{1, 0, 4, 4}, names, top, strings, {0, 0}, charset, selects, fonts, charIndex} {
				output.Write(part)
			}
			return output.Bytes(), nil
		}
		dict[15][0] = float64(offset)
		dict[1237][0] = float64(offset + len(charset))
		dict[1236][0] = float64(offset + len(charset) + len(selects))
		dict[17][0] = float64(offset + len(charset) + len(selects) + len(fonts))
	}
	return nil, fmt.Errorf("CFF offsets did not converge")
}

// staticCFFPrograms 读取无子程序和私有字典的CID字形，其他CFF保留原有保存策略
// 入参: sfnt 已解析字体
// 返回: [][]byte 独立字形程序，不符合条件时为空
func staticCFFPrograms(sfnt *sfntFont) [][]byte {
	data := sfnt.Tables["CFF "]
	if len(data) < 4 {
		return nil
	}
	_, end, err := readType2Index(data, int(data[2]))
	if err != nil {
		return nil
	}
	tops, end, err := readType2Index(data, end)
	if err != nil || len(tops) != 1 {
		return nil
	}
	_, end, err = readType2Index(data, end)
	if err != nil {
		return nil
	}
	globals, _, err := readType2Index(data, end)
	if err != nil || len(globals) != 0 {
		return nil
	}
	dict, err := readCFFDict(tops[0])
	if err != nil || len(dict[1230]) != 3 {
		return nil
	}
	matrix, err := readCFFMatrix(dict, [6]float64{.001, 0, 0, .001, 0, 0})
	if err != nil || matrix[0] != matrix[3] || matrix[1] != 0 || matrix[2] != 0 || matrix[4] != 0 || matrix[5] != 0 || math.Abs(matrix[0]*float64(sfnt.UnitsPerEm())-1) > 1e-6 {
		return nil
	}
	offset, err := cffDictOffset(data, dict, 1236)
	if err != nil {
		return nil
	}
	fds, _, err := readType2Index(data, offset)
	if err != nil || len(fds) != 1 {
		return nil
	}
	fd, err := readCFFDict(fds[0])
	if err != nil || len(fd) != 1 || len(fd[18]) != 2 || fd[18][0] != 0 {
		return nil
	}
	offset, err = cffDictOffset(data, dict, 17)
	if err != nil {
		return nil
	}
	chars, _, err := readType2Index(data, offset)
	if err != nil || len(chars) != int(sfnt.NumGlyphs()) {
		return nil
	}
	return chars
}

// subsetStaticCFFFont 裁剪静态CID字形并保留原编号、字符映射和度量
// 入参: sfnt 已解析字体, glyphs 使用的字形
// 返回: []byte 字体子集, error 裁剪错误
func subsetStaticCFFFont(sfnt *sfntFont, glyphs []uint16) ([]byte, error) {
	chars := staticCFFPrograms(sfnt)
	if chars == nil {
		return nil, fmt.Errorf("CFF font cannot be subset safely")
	}
	keep := make(map[uint16]bool, len(glyphs)+1)
	keep[0] = true
	for _, gid := range glyphs {
		if int(gid) >= len(chars) {
			return nil, fmt.Errorf("CFF glyph index out of range")
		}
		keep[gid] = true
	}
	empty := make(map[uint16][]byte)
	for gid := range chars {
		if !keep[uint16(gid)] {
			width := sfnt.GlyphAdvance(uint16(gid))
			program := empty[width]
			if program == nil {
				var err error
				program, err = cffOutlineProgram(nil, width)
				if err != nil {
					return nil, err
				}
				empty[width] = program
			}
			chars[gid] = program
		}
	}
	table := maps.Clone(sfnt.Tables)
	var err error
	table["CFF "], err = staticCIDFont(table, chars)
	if err != nil {
		return nil, err
	}
	mapping := make(map[rune]uint16)
	for gid := range keep {
		for _, char := range sfnt.glyphCharacters(gid) {
			mapping[char] = gid
		}
	}
	table["cmap"] = buildCmapTable(sfnt.NumGlyphs(), mapping)
	for _, tag := range []string{"DSIG", "GSUB", "GPOS", "GDEF"} {
		delete(table, tag)
	}
	return serializeOTF(table)
}
