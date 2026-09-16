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
	"io"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
)

// fontNameRecord 字体名称表记录
type fontNameRecord struct {
	Platform uint16
	Encoding uint16
	Language uint16
	Name     uint16
	Length   uint16
	Offset   uint16
}

// appendFontFileNames 使用字体内部名称匹配，缺失时保留文件名
// 入参: candidates 字体候选, index 文件候选索引, faces 各字体内部名称
// 返回: []fontFileCandidate 字体候选列表
func appendFontFileNames(candidates []fontFileCandidate, index int, faces [][]string) []fontFileCandidate {
	file := candidates[index]
	replaced := false
	for face, names := range faces {
		file.face = face
		for _, name := range names {
			file.normalized = fontNormalizeName(name)
			if !replaced {
				candidates[index] = file
				replaced = true
			} else {
				candidates = append(candidates, file)
			}
		}
	}
	return candidates
}

// fontFileNames 按字体索引读取字体或集合中各项的名称
// 入参: file 字体数据
// 返回: [][]string 各字体名称及带样式的字体族名称
func fontFileNames(file io.ReaderAt) [][]string {
	var header [12]byte
	if _, err := file.ReadAt(header[:], 0); err != nil {
		return nil
	}
	if string(header[:4]) != "ttcf" {
		return [][]string{fontFaceNames(file, 0)}
	}
	version := binary.BigEndian.Uint32(header[4:])
	if version != 0x00010000 && version != 0x00020000 {
		return nil
	}
	var faces [][]string
	for i, count := uint32(0), binary.BigEndian.Uint32(header[8:]); i < count; i++ {
		var entry [4]byte
		if _, err := file.ReadAt(entry[:], 12+int64(i)*4); err != nil {
			return nil
		}
		faces = append(faces, fontFaceNames(file, int64(binary.BigEndian.Uint32(entry[:]))))
	}
	return faces
}

// fontCollectionIndex 按内部名称与样式选择集合项，未匹配时保留首项
// 入参: data 集合数据, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: int 零起始字体索引
func fontCollectionIndex(data []byte, names []string, bold, italic bool) int {
	return fontNameIndex(fontFileNames(bytes.NewReader(data)), names, bold, italic)
}

// fontNameIndex 按名称与样式选择字体索引，未匹配时保留首项
// 入参: faces 各字体名称, names 目标名称, bold 是否粗体, italic 是否斜体
// 返回: int 零起始字体索引
func fontNameIndex(faces [][]string, names []string, bold, italic bool) int {
	candidates := appendFontFileNames([]fontFileCandidate{{}}, 0, faces)
	if matches := fontFileMatches(candidates, names, bold, italic); len(matches) > 0 {
		return matches[0].face
	}
	return 0
}

// fontFaceNames 读取指定字体表目录中的名称
// 入参: file 字体数据, offset 字体表目录偏移
// 返回: []string 字体名称及带样式的字体族名称
func fontFaceNames(file io.ReaderAt, offset int64) []string {
	var header [12]byte
	if _, err := file.ReadAt(header[:], offset); err != nil {
		return nil
	}
	switch string(header[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
	default:
		return nil
	}
	entries := make([]byte, int(binary.BigEndian.Uint16(header[4:]))*16)
	if _, err := file.ReadAt(entries, offset+12); err != nil {
		return nil
	}
	for i := 0; i < len(entries); i += 16 {
		if string(entries[i:i+4]) != "name" {
			continue
		}
		offset := int64(binary.BigEndian.Uint32(entries[i+8:]))
		length := int64(binary.BigEndian.Uint32(entries[i+12:]))
		data, err := io.ReadAll(io.NewSectionReader(file, offset, length))
		if err != nil {
			return nil
		}
		return fontNamesFromTable(data)
	}
	return nil
}

// fontNamesFromTable 解析字体名称表
// 入参: data 名称表数据
// 返回: []string 字体名称列表
func fontNamesFromTable(data []byte) []string {
	records, values := fontNameValues(data)
	var names []string
	seen := make(map[string]bool)
	for _, record := range records {
		key := [4]uint16{record.Platform, record.Encoding, record.Language, record.Name}
		name := values[key]
		switch record.Name {
		case 1, 16:
			key[3]++
			if style := values[key]; style != "" && name != "" {
				name += " " + style
			}
		case 4, 6:
		default:
			continue
		}
		names = appendFontName(names, seen, name)
	}
	return names
}

// fontNameValues 解码字体名称表的多语言记录
// 入参: data 名称表数据
// 返回: []fontNameRecord 名称记录, map[[4]uint16]string 平台、编码、语言和名称对应的文本
func fontNameValues(data []byte) ([]fontNameRecord, map[[4]uint16]string) {
	if len(data) < 6 || binary.BigEndian.Uint16(data) > 1 {
		return nil, nil
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	storage := int(binary.BigEndian.Uint16(data[4:]))
	if 6+count*12 > storage || storage > len(data) {
		return nil, nil
	}
	records := make([]fontNameRecord, count)
	if err := binary.Read(bytes.NewReader(data[6:]), binary.BigEndian, records); err != nil {
		return nil, nil
	}
	values := make(map[[4]uint16]string)
	for _, record := range records {
		switch record.Name {
		case 1, 2, 4, 6, 16, 17:
		default:
			continue
		}
		start := storage + int(record.Offset)
		end := start + int(record.Length)
		if end > len(data) {
			continue
		}
		var name string
		switch {
		case record.Platform == 0 || record.Platform == 3 && (record.Encoding == 0 || record.Encoding == 1 || record.Encoding == 10):
			if record.Length%2 != 0 {
				continue
			}
			chars := make([]uint16, int(record.Length)/2)
			for i := range chars {
				chars[i] = binary.BigEndian.Uint16(data[start+i*2:])
			}
			name = string(utf16.Decode(chars))
		case record.Platform == 1 && record.Encoding == 0:
			name, _ = charmap.Macintosh.NewDecoder().String(string(data[start:end]))
		default:
			continue
		}
		values[[4]uint16{record.Platform, record.Encoding, record.Language, record.Name}] = name
	}
	return records, values
}
