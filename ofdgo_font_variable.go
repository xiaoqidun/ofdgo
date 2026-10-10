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
	"fmt"
)

// variableSFNTSubsettable 核对TrueType可变轴与字形变化表的基本结构
// 入参: sfnt 已解析字体
// 返回: bool 是否具备可裁剪的变化数据
func variableSFNTSubsettable(sfnt *sfntFont) bool {
	fvar, gvar := sfnt.Tables["fvar"], sfnt.Tables["gvar"]
	if len(fvar) < 16 || len(gvar) < 20 || binary.BigEndian.Uint32(fvar) != 0x10000 || binary.BigEndian.Uint32(gvar) != 0x10000 {
		return false
	}
	axes := int(binary.BigEndian.Uint16(fvar[8:]))
	offset, size := int(binary.BigEndian.Uint16(fvar[4:])), int(binary.BigEndian.Uint16(fvar[10:]))
	return axes > 0 && size >= 20 && offset >= 16 && offset <= len(fvar) && axes <= (len(fvar)-offset)/size &&
		int(binary.BigEndian.Uint16(gvar[4:])) == axes && binary.BigEndian.Uint16(gvar[12:]) == sfnt.NumGlyphs()
}

// subsetVariableSFNTFont 保留字形编号及可变轴，仅裁剪未使用的轮廓和字形变化数据
// 入参: sfnt 已解析字体, glyphs 使用的字形
// 返回: []byte 字体子集, error 字形或变化表错误
func subsetVariableSFNTFont(sfnt *sfntFont, glyphs []uint16) ([]byte, error) {
	if !variableSFNTSubsettable(sfnt) {
		return nil, fmt.Errorf("invalid TrueType variation tables")
	}
	usage := newEditorFontUsage()
	usage.glyphs[0] = true
	for _, gid := range glyphs {
		if gid >= sfnt.NumGlyphs() {
			return nil, fmt.Errorf("variable font glyph index out of range")
		}
		usage.glyphs[gid] = true
		for _, char := range sfnt.glyphCharacters(gid) {
			usage.chars[char] = true
		}
	}
	for gid := range usage.glyphs {
		dependencies, err := sfnt.glyphDependencies(gid)
		if err != nil {
			return nil, err
		}
		for _, dependency := range dependencies {
			usage.glyphs[dependency] = true
		}
	}
	mapping := make(map[rune]uint16, len(usage.chars))
	for char := range usage.chars {
		mapping[char] = sfnt.GlyphIndex(char)
	}
	table := make(map[string][]byte)
	for _, tag := range []string{"head", "hhea", "hmtx", "maxp", "OS/2", "post", "name", "glyf", "loca", "cvt ", "fpgm", "prep", "gasp", "vhea", "vmtx"} {
		if data := sfnt.Tables[tag]; data != nil {
			table[tag] = data
		}
	}
	table["cmap"] = buildCmapTable(sfnt.NumGlyphs(), mapping)
	static, err := serializeOTF(table)
	if err != nil {
		return nil, err
	}
	if subset := subsetSourceSFNTFont(static, usage); subset != nil {
		table, err = fontFileTables(subset, 0)
		if err != nil {
			return nil, err
		}
	}
	table["gvar"], err = subsetGlyphVariations(sfnt.Tables["gvar"], usage.glyphs)
	if err != nil {
		return nil, err
	}
	for _, tag := range []string{"fvar", "avar", "cvar", "HVAR", "VVAR", "MVAR", "STAT"} {
		if data := sfnt.Tables[tag]; data != nil {
			table[tag] = data
		}
	}
	return serializeOTF(table)
}

// subsetGlyphVariations 按原字形编号重建gvar偏移表，保留共享元组及使用字形的完整变化数据
// 入参: data gvar表, glyphs 保留字形集合
// 返回: []byte 裁剪后的gvar表, error 表结构错误
func subsetGlyphVariations(data []byte, glyphs map[uint16]bool) ([]byte, error) {
	if len(data) < 20 || binary.BigEndian.Uint32(data) != 0x10000 {
		return nil, fmt.Errorf("invalid gvar header")
	}
	axes := uint64(binary.BigEndian.Uint16(data[4:]))
	tuples := uint64(binary.BigEndian.Uint16(data[6:]))
	shared := uint64(binary.BigEndian.Uint32(data[8:]))
	count := int(binary.BigEndian.Uint16(data[12:]))
	flags := binary.BigEndian.Uint16(data[14:])
	start := uint64(binary.BigEndian.Uint32(data[16:]))
	width := 2
	if flags&1 != 0 {
		width = 4
	}
	sharedSize := axes * tuples * 2
	headerEnd := uint64(20 + (count+1)*width)
	if flags > 1 || headerEnd > uint64(len(data)) || start < headerEnd || start > uint64(len(data)) || shared > uint64(len(data)) || sharedSize > uint64(len(data))-shared || sharedSize != 0 && shared < headerEnd {
		return nil, fmt.Errorf("invalid gvar offsets")
	}
	readOffset := func(index int) uint64 {
		if width == 2 {
			return uint64(binary.BigEndian.Uint16(data[20+index*2:])) * 2
		}
		return uint64(binary.BigEndian.Uint32(data[20+index*4:]))
	}
	output := make([]byte, 20+(count+1)*4)
	copy(output, data[:20])
	binary.BigEndian.PutUint16(output[14:], 1)
	binary.BigEndian.PutUint32(output[8:], uint32(len(output)))
	output = append(output, data[shared:shared+sharedSize]...)
	base := len(output)
	binary.BigEndian.PutUint32(output[16:], uint32(base))
	for gid := range count {
		first, last := readOffset(gid), readOffset(gid+1)
		if last < first || last > uint64(len(data))-start {
			return nil, fmt.Errorf("invalid gvar glyph offsets")
		}
		if last > first && sharedSize != 0 && start+first < shared+sharedSize && shared < start+last {
			return nil, fmt.Errorf("overlapping gvar glyph and shared tuple data")
		}
		binary.BigEndian.PutUint32(output[20+gid*4:], uint32(len(output)-base))
		if glyphs[uint16(gid)] {
			output = append(output, data[start+first:start+last]...)
		}
	}
	binary.BigEndian.PutUint32(output[20+count*4:], uint32(len(output)-base))
	return output, nil
}
