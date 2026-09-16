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
	"strings"

	"github.com/tdewolff/font"
)

// FontFace 字体文件中的字体名称与零起始索引，不依赖文件名
type FontFace struct {
	Index          int      `json:"index"`
	FullName       string   `json:"fullName"`
	Family         string   `json:"family"`
	Style          string   `json:"style"`
	PostScriptName string   `json:"postscriptName"`
	Names          []string `json:"names"`
}

// Faces 读取字体名称表，集合文件按原顺序列出各项，不解析字形轮廓
// 返回: []FontFace 字体列表, error 格式或名称表错误
func (f FontFile) Faces() ([]FontFace, error) {
	data, err := font.ToSFNT(f.Data)
	if err != nil {
		return nil, err
	}
	count, err := fontFileCount(data)
	if err != nil {
		return nil, err
	}
	faces := make([]FontFace, 0, count)
	for index := 0; index < count; index++ {
		tables, err := fontFileTables(data, index)
		if err != nil {
			return nil, err
		}
		face := fontFaceInfo(tables["name"], index)
		if face.FullName == "" {
			return nil, fmt.Errorf("font %d has no usable name", index)
		}
		faces = append(faces, face)
	}
	return faces, nil
}

// fontFaceInfo 从名称表读取字体信息
// 入参: data 名称表数据, index 零起始字体索引
// 返回: FontFace 字体信息
func fontFaceInfo(data []byte, index int) FontFace {
	records, values := fontNameValues(data)
	name := func(ids ...uint16) string {
		for _, id := range ids {
			for _, record := range records {
				if record.Name == id {
					if value := strings.TrimSpace(values[[4]uint16{record.Platform, record.Encoding, record.Language, id}]); value != "" {
						return value
					}
				}
			}
		}
		return ""
	}
	return FontFace{Index: index, FullName: name(4, 6, 16, 1), Family: name(16, 1), Style: name(17, 2), PostScriptName: name(6), Names: fontNamesFromTable(data)}
}

// Face 提取指定字体，返回独立的OpenType数据，可用于预览或AddFont，且不修改原文件
// 入参: index 零起始字体索引，非集合文件只能为0
// 返回: []byte 独立字体数据, error 格式或索引错误
func (f FontFile) Face(index int) ([]byte, error) {
	data, err := font.ToSFNT(f.Data)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("ttcf")) {
		return extractCollectionFont(data, index)
	}
	if _, err := fontFileTables(data, index); err != nil {
		return nil, err
	}
	return bytes.Clone(data), nil
}

// fontFileCount 校验字体或集合头并读取字体数量
// 入参: data OpenType数据
// 返回: int 字体数量, error 格式错误
func fontFileCount(data []byte) (int, error) {
	if len(data) < 12 {
		return 0, fmt.Errorf("invalid font header")
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
		return 1, nil
	case "ttcf":
		version := binary.BigEndian.Uint32(data[4:])
		count := uint64(binary.BigEndian.Uint32(data[8:]))
		if (version != 0x00010000 && version != 0x00020000) || count == 0 || count > uint64(len(data)-12)/4 {
			return 0, fmt.Errorf("invalid font collection header")
		}
		return int(count), nil
	default:
		return 0, fmt.Errorf("invalid OpenType header")
	}
}
