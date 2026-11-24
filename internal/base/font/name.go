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

// FileNames 按字体索引读取字体或集合中各项的名称
// 入参: file 字体数据
// 返回: [][]string 各字体名称及带样式的字体族名称
func FileNames(file io.ReaderAt) [][]string {
	if file == nil {
		return nil
	}
	var header [12]byte
	if err := readAtFull(file, header[:], 0); err != nil {
		return nil
	}
	if string(header[:4]) != "ttcf" {
		return [][]string{fontFaceNames(file, 0, nil)}
	}
	version := binary.BigEndian.Uint32(header[4:])
	if version != 0x00010000 && version != 0x00020000 {
		return nil
	}
	count := uint64(binary.BigEndian.Uint32(header[8:]))
	if count == 0 || count > maxFaceCount {
		return nil
	}
	rawOffsets := make([]byte, count*4)
	if err := readAtFull(file, rawOffsets, 12); err != nil {
		return nil
	}
	faces := make([][]string, 0, count)
	faceCache := make(map[uint64][]string)
	tableCache := make(map[[2]uint64][]string)
	for i := uint64(0); i < count; i++ {
		faceOffset := uint64(binary.BigEndian.Uint32(rawOffsets[i*4:]))
		if cached, ok := faceCache[faceOffset]; ok {
			faces = append(faces, append([]string(nil), cached...))
			continue
		}
		names := fontFaceNames(file, int64(faceOffset), tableCache)
		faceCache[faceOffset] = names
		faces = append(faces, append([]string(nil), names...))
	}
	return faces
}

// NamesFromTable 解析字体名称表
// 入参: data 名称表数据
// 返回: []string 字体名称列表
func NamesFromTable(data []byte) []string {
	records, values := fontNameValues(data)
	return fontNamesFromValues(records, values)
}

// fontFaceNames 读取指定字体表目录中的名称
// 入参: file 字体数据, offset 字体表目录偏移, tableCache 名称表解析缓存，可为nil
// 返回: []string 字体名称及带样式的字体族名称
func fontFaceNames(file io.ReaderAt, offset int64, tableCache map[[2]uint64][]string) []string {
	if offset < 0 {
		return nil
	}
	var header [12]byte
	if err := readAtFull(file, header[:], offset); err != nil {
		return nil
	}
	switch string(header[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
	default:
		return nil
	}
	tableCount := uint64(binary.BigEndian.Uint16(header[4:]))
	if tableCount == 0 || tableCount > maxFaceTableCount {
		return nil
	}
	entries := make([]byte, tableCount*16)
	if err := readAtFull(file, entries, offset+12); err != nil {
		return nil
	}
	for i := 0; i < len(entries); i += 16 {
		if string(entries[i:i+4]) != "name" {
			continue
		}
		tableOffset := uint64(binary.BigEndian.Uint32(entries[i+8:]))
		length := uint64(binary.BigEndian.Uint32(entries[i+12:]))
		if length == 0 || length > maxFaceTableSize {
			return nil
		}
		key := [2]uint64{tableOffset, length}
		if tableCache != nil {
			if cached, ok := tableCache[key]; ok {
				return cached
			}
		}
		data := make([]byte, length)
		if err := readAtFull(file, data, int64(tableOffset)); err != nil {
			return nil
		}
		names := NamesFromTable(data)
		if tableCache != nil {
			tableCache[key] = names
		}
		return names
	}
	return nil
}

// fontNamesFromValues 从已解码记录生成去重名称，保留原有顺序及样式匹配
// 入参: records 名称记录, values 已解码的名称文本
// 返回: []string 字体名称列表
func fontNamesFromValues(records []fontNameRecord, values map[[4]uint16]string) []string {
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
	for i := range count {
		off := 6 + i*12
		records[i] = fontNameRecord{
			Platform: binary.BigEndian.Uint16(data[off:]),
			Encoding: binary.BigEndian.Uint16(data[off+2:]),
			Language: binary.BigEndian.Uint16(data[off+4:]),
			Name:     binary.BigEndian.Uint16(data[off+6:]),
			Length:   binary.BigEndian.Uint16(data[off+8:]),
			Offset:   binary.BigEndian.Uint16(data[off+10:]),
		}
	}
	values := make(map[[4]uint16]string)
	for _, record := range records {
		switch record.Name {
		case 1, 2, 4, 5, 6, 8, 16, 17:
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
