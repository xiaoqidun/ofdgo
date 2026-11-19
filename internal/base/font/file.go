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
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	// maxFaceCount 限制单个集合流式读取的最大字体数量
	maxFaceCount = 4096
	// maxFaceTableCount 限制单个字体的最大表目录项数
	maxFaceTableCount = 4096
	// maxFaceTableSize 限制流式读取名称与度量单表的最大字节数
	maxFaceTableSize = 4 << 20
)

// Face 字体文件中的字体名称与零起始索引，不依赖文件名
// Family和Style为排版族，MenuFamily和MenuStyle为兼容四款式的菜单名称
// FamilyKey区分兼容族、厂商和版本，未知时为空；Bold和Italic表示字体固有样式
type Face struct {
	Index          int      `json:"index"`
	FullName       string   `json:"fullName"`
	Family         string   `json:"family"`
	Style          string   `json:"style"`
	PostScriptName string   `json:"postscriptName"`
	Names          []string `json:"names"`
	MenuFamily     string   `json:"menuFamily"`
	MenuStyle      string   `json:"menuStyle"`
	FamilyKey      string   `json:"familyKey"`
	Bold           bool     `json:"bold"`
	Italic         bool     `json:"italic"`
}

// FaceInfo 从名称表读取字体信息
// 入参: data 名称表数据, index 零起始字体索引
// 返回: Face 字体信息
func FaceInfo(data []byte, index int) Face {
	return faceInfoLanguage(data, index, 0xffff, 0xffff)
}

// TableFaceInfo 读取名称及字体固有款式，名称键不随显示语言变化
// 入参: tables 字体表, index 集合索引, platform 平台标识, language 语言标识
// 返回: Face 字体信息
func TableFaceInfo(tables map[string][]byte, index int, platform, language uint16) Face {
	face := faceInfoLanguage(tables["name"], index, platform, language)
	key := face.FamilyKey
	if head := tables["head"]; len(head) >= 46 {
		key += string(head[4:8])
		style := binary.BigEndian.Uint16(head[44:])
		face.Bold, face.Italic = style&1 != 0, style&2 != 0
	}
	if os2 := tables["OS/2"]; len(os2) >= 64 {
		key += string(os2[58:62])
		style := binary.BigEndian.Uint16(os2[62:])
		face.Bold, face.Italic = style&32 != 0, style&1 != 0 || style&512 != 0
	}
	if face.FamilyKey != "" {
		face.FamilyKey = fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	}
	return face
}

// FileCount 校验字体或集合头并读取字体数量
// 入参: data OpenType数据
// 返回: int 字体数量, error 格式错误
func FileCount(data []byte) (int, error) {
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

// ReadFaces 读取已解封装字体或集合的名称信息
// 入参: data OpenType数据
// 返回: []Face 字体列表, error 格式或名称表错误
func ReadFaces(data []byte) ([]Face, error) {
	return ReadLocalizedFaces(data, 0xffff, 0xffff)
}

// ReadLocalizedFaces 读取指定平台和语言的字体名称，缺失时沿用名称表顺序
// 入参: data OpenType数据, platform 名称表平台标识, language 名称表语言标识
// 返回: []Face 字体列表, error 格式或名称表错误
func ReadLocalizedFaces(data []byte, platform, language uint16) ([]Face, error) {
	count, err := FileCount(data)
	if err != nil {
		return nil, err
	}
	faces := make([]Face, 0, count)
	for index := 0; index < count; index++ {
		tables, err := FileTables(data, index)
		if err != nil {
			return nil, err
		}
		face := TableFaceInfo(tables, index, platform, language)
		if face.FullName == "" {
			return nil, fmt.Errorf("font %d has no usable name", index)
		}
		faces = append(faces, face)
	}
	return faces, nil
}

// ReadLocalizedFacesAt 通过随机读取流按需读取字体头、表目录及名称和款式表，避免整文件读盘
// 入参: reader 随机读取流, size 文件总字节数, platform 名称表平台标识, language 名称表语言标识
// 返回: []Face 字体列表, error 读取、格式或名称表错误
func ReadLocalizedFacesAt(reader io.ReaderAt, size int64, platform, language uint16) ([]Face, error) {
	if reader == nil || size < 12 {
		return nil, fmt.Errorf("invalid font header")
	}
	fileSize := uint64(size)
	var header [12]byte
	if err := readAtFull(reader, header[:], 0); err != nil {
		return nil, err
	}
	var offsets []uint64
	switch string(header[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
		offsets = []uint64{0}
	case "ttcf":
		version := binary.BigEndian.Uint32(header[4:])
		count := uint64(binary.BigEndian.Uint32(header[8:]))
		if (version != 0x00010000 && version != 0x00020000) || count == 0 || count > (fileSize-12)/4 || count > maxFaceCount {
			return nil, fmt.Errorf("invalid font collection header")
		}
		rawOffsets := make([]byte, count*4)
		if err := readAtFull(reader, rawOffsets, 12); err != nil {
			return nil, err
		}
		offsets = make([]uint64, count)
		for i := range offsets {
			offsets[i] = uint64(binary.BigEndian.Uint32(rawOffsets[i*4:]))
			if offsets[i] > fileSize-12 {
				return nil, fmt.Errorf("invalid font offset")
			}
		}
	default:
		return nil, fmt.Errorf("invalid OpenType header")
	}
	faces := make([]Face, 0, len(offsets))
	cache := make(map[[2]uint64][]byte)
	for index, faceOffset := range offsets {
		var sfntHeader [12]byte
		if faceOffset == 0 {
			sfntHeader = header
		} else if err := readAtFull(reader, sfntHeader[:], int64(faceOffset)); err != nil {
			return nil, err
		}
		if string(sfntHeader[:4]) != "\x00\x01\x00\x00" && string(sfntHeader[:4]) != "OTTO" && string(sfntHeader[:4]) != "true" {
			return nil, fmt.Errorf("invalid OpenType header")
		}
		tableCount := uint64(binary.BigEndian.Uint16(sfntHeader[4:]))
		dirOffset := faceOffset + 12
		if tableCount == 0 || tableCount > (fileSize-dirOffset)/16 || tableCount > maxFaceTableCount {
			return nil, fmt.Errorf("invalid font table directory")
		}
		directory := make([]byte, tableCount*16)
		if err := readAtFull(reader, directory, int64(dirOffset)); err != nil {
			return nil, err
		}
		seen := make(map[string]bool, tableCount)
		tables := make(map[string][]byte, 3)
		for i := uint64(0); i < tableCount; i++ {
			entry := directory[i*16 : (i+1)*16]
			tag := string(entry[:4])
			start := uint64(binary.BigEndian.Uint32(entry[8:]))
			length := uint64(binary.BigEndian.Uint32(entry[12:]))
			if start > fileSize || length > fileSize-start {
				return nil, fmt.Errorf("invalid %s font table range", tag)
			}
			if seen[tag] {
				return nil, fmt.Errorf("duplicate %s font table", tag)
			}
			seen[tag] = true
			if tag != "name" && tag != "head" && tag != "OS/2" {
				continue
			}
			if length > maxFaceTableSize {
				return nil, fmt.Errorf("%s font table too large", tag)
			}
			key := [2]uint64{start, length}
			data, ok := cache[key]
			if !ok {
				data = make([]byte, length)
				if length > 0 {
					if err := readAtFull(reader, data, int64(start)); err != nil {
						return nil, err
					}
				}
				cache[key] = data
			}
			tables[tag] = data
		}
		face := TableFaceInfo(tables, index, platform, language)
		if face.FullName == "" {
			return nil, fmt.Errorf("font %d has no usable name", index)
		}
		faces = append(faces, face)
	}
	return faces, nil
}

// ExtractFace 提取已解封装字体中的集合项，规范字符映射并保留原始表
// 入参: data OpenType数据, index 零起始字体索引
// 返回: []byte 独立字体数据, error 格式或索引错误
func ExtractFace(data []byte, index int) ([]byte, error) {
	var err error
	if bytes.HasPrefix(data, []byte("ttcf")) {
		data, err = ExtractCollectionFont(data, index)
		if err != nil {
			return nil, err
		}
		index = 0
	}
	tables, err := FileTables(data, index)
	if err != nil {
		return nil, err
	}
	cmap, err := NormalizeCmap(tables["cmap"])
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(cmap, tables["cmap"]) {
		tables["cmap"] = cmap
		return SerializeOTF(tables)
	}
	return bytes.Clone(data), nil
}

// MissingGlyphs 检查独立字体的字符覆盖，忽略段落换行并按首次出现顺序去重
// 入参: data 独立OpenType数据, text 待检查文字
// 返回: string 缺失字符, error 字体或文字格式错误
func MissingGlyphs(data []byte, text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("text must be UTF-8")
	}
	data, err := DefaultCFF2Font(data)
	if err != nil {
		return "", err
	}
	sfnt, err := ParseSFNT(data)
	if err != nil {
		return "", err
	}
	seen := make(map[rune]bool)
	var missing strings.Builder
	for _, char := range text {
		if char == '\r' || char == '\n' || seen[char] {
			continue
		}
		seen[char] = true
		if sfnt.GlyphIndex(char) == 0 {
			missing.WriteRune(char)
		}
	}
	return missing.String(), nil
}

// faceInfoLanguage 优先读取指定平台和语言的显示名称，保留标识及全部别名
// 入参: data 名称表数据, index 字体索引, platform 平台标识, language 语言标识
// 返回: Face 字体信息
func faceInfoLanguage(data []byte, index int, platform, language uint16) Face {
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
	localized := func(ids ...uint16) string {
		for _, id := range ids {
			for _, record := range records {
				if record.Platform == platform && record.Language == language && record.Name == id {
					if value := strings.TrimSpace(values[[4]uint16{record.Platform, record.Encoding, record.Language, id}]); value != "" {
						return value
					}
				}
			}
		}
		return name(ids...)
	}
	key := ""
	if family, version := name(1), name(5); family != "" && version != "" {
		key = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{family, version, name(8)}, "\x00"))))
	}
	family, style := localized(1), localized(2)
	if family == "" {
		family = localized(16, 4, 6)
	}
	if style == "" {
		style = localized(17)
	}
	return Face{Index: index, FullName: localized(4, 6, 16, 1), Family: localized(16, 1), Style: localized(17, 2), PostScriptName: name(6), Names: fontNamesFromValues(records, values), MenuFamily: family, MenuStyle: style, FamilyKey: key}
}

// readAtFull 从随机读取流读取完整缓冲区，允许末尾恰好返回EOF
// 入参: reader 随机读取流, buf 目标缓冲区, offset 读取偏移
// 返回: error 读取错误
func readAtFull(reader io.ReaderAt, buf []byte, offset int64) error {
	n, err := reader.ReadAt(buf, offset)
	if err != nil && !(err == io.EOF && n == len(buf)) {
		return err
	}
	if n != len(buf) {
		return io.ErrUnexpectedEOF
	}
	return nil
}
