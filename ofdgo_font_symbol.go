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
	"context"
	"encoding/binary"
	"fmt"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

// symbolFontProgram 为标准符号字体补齐Unicode映射，保留字形编号和轮廓
// 入参: ctx 取消上下文, data 独立字体数据, name 标准字体名，为空时从字体名称表识别
// 返回: []byte 字体数据, map[string]uint16 标准字形编号，不适用时为空, error 映射或取消错误
func symbolFontProgram(ctx context.Context, data []byte, name string) ([]byte, map[string]uint16, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	tables, err := fontFileTables(data, 0)
	if err != nil {
		return data, nil, nil
	}
	legacy := symbolFontCmap(tables["cmap"])
	if name == "" && !legacy {
		return data, nil, nil
	}
	actual := symbolFontName(tables["name"])
	if name == "" {
		name = actual
	}
	if name != "Symbol" && name != "ZapfDingbats" || !legacy && actual != name {
		return data, nil, nil
	}
	standard, err := new(pdfgo.Reader).ReadFontContext(ctx, pdfgo.Dictionary{"Type": pdfgo.Name("Font"), "Subtype": pdfgo.Name("Type1"), "BaseFont": pdfgo.Name(name)})
	if err != nil {
		return nil, nil, err
	}
	codes := make([]byte, 256)
	for index := range codes {
		codes[index] = byte(index)
	}
	glyphs, err := standard.DecodeContext(ctx, codes)
	if err != nil {
		return nil, nil, err
	}
	original := parseCmapMappings(tables["cmap"])
	mapping := make(map[rune]uint16)
	names := make(map[string]uint16)
	for _, glyph := range glyphs {
		if glyph.Name == ".notdef" || glyph.Name == "" {
			continue
		}
		char, size := utf8.DecodeRuneInString(glyph.Text)
		var id uint16
		if legacy {
			id = original[rune(glyph.Code)]
			for _, base := range [...]rune{0xf000, 0xf100, 0xf200} {
				if candidate := original[base+rune(glyph.Code)]; candidate != 0 {
					if id != 0 && id != candidate {
						return nil, nil, fmt.Errorf("ambiguous symbol font mapping")
					}
					id = candidate
				}
			}
		} else if size != 0 && size == len(glyph.Text) {
			id = original[char]
		}
		if id == 0 {
			continue
		}
		names[glyph.Name] = id
		if size != 0 && size == len(glyph.Text) {
			mapping[char] = id
		}
	}
	if !legacy || len(mapping) == 0 {
		return data, names, nil
	}
	tables["cmap"] = buildCmapTable(0, mapping)
	program, err := serializeOTF(tables)
	return program, names, err
}

// symbolFontName 从字体名称表识别标准符号字体，不依据文件名推断编码
// 入参: data 名称表数据
// 返回: string 标准字体名，无法识别时为空
func symbolFontName(data []byte) string {
	for _, name := range fontNamesFromTable(data) {
		switch fontNormalizeName(name) {
		case "symbol", "symbolmt", "symbolregular":
			return "Symbol"
		case "zapfdingbats", "zapfdingbatsregular", "itczapfdingbats":
			return "ZapfDingbats"
		}
	}
	return ""
}

// symbolFontCmap 识别不含Unicode字符表的Windows符号字体
// 入参: data 字符映射表
// 返回: bool 是否为旧式符号字体
func symbolFontCmap(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	if count > (len(data)-4)/8 {
		return false
	}
	symbol := false
	for i := range count {
		pos := 4 + i*8
		platform, encoding := binary.BigEndian.Uint16(data[pos:]), binary.BigEndian.Uint16(data[pos+2:])
		if platform == 0 || platform == 3 && (encoding == 1 || encoding == 10) {
			return false
		}
		symbol = symbol || platform == 3 && encoding == 0
	}
	return symbol
}
