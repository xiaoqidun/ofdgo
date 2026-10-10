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
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/andybalholm/brotli"
)

// fontContainerLimit 限制压缩字体展开后的表数据总量
const fontContainerLimit = 256 << 20

// woff2Tags 保存WOFF2表目录的标准标签顺序
var woff2Tags = [...]string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post",
	"cvt ", "fpgm", "glyf", "loca", "prep", "CFF ", "VORG", "EBDT",
	"EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea",
	"vmtx", "BASE", "GDEF", "GPOS", "GSUB", "EBSC", "JSTF", "MATH",
	"CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt", "avar",
	"bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar",
	"gvar", "hsty", "just", "lcar", "mort", "morx", "opbd", "prop",
	"trak", "Zapf", "Silf", "Glat", "Gloc", "Feat", "Sill",
}

// decodeWOFF2 展开未变换的WOFF2表，字体集合及字形变换交由绘图适配器处理
// 入参: data WOFF2文件
// 返回: []byte OpenType数据, bool 是否已处理, error 封装错误
func decodeWOFF2(data []byte) ([]byte, bool, error) {
	if len(data) < 48 || string(data[:4]) != "wOF2" || uint64(binary.BigEndian.Uint32(data[8:])) != uint64(len(data)) {
		return nil, true, fmt.Errorf("invalid WOFF2 header")
	}
	if string(data[4:8]) == "ttcf" {
		return nil, false, nil
	}
	count := int(binary.BigEndian.Uint16(data[12:]))
	if count == 0 || count > (len(data)-48)/2 {
		return nil, true, fmt.Errorf("invalid WOFF2 table count")
	}
	tags := make([]string, 0, count)
	sizes := make([]uint32, 0, count)
	table := make(map[string][]byte, count)
	pos, total, transformed := 48, uint64(0), false
	for range count {
		if pos >= len(data) {
			return nil, true, fmt.Errorf("truncated WOFF2 directory")
		}
		flags := data[pos]
		pos++
		var tag string
		if index := flags & 63; index == 63 {
			if len(data)-pos < 4 {
				return nil, true, fmt.Errorf("truncated WOFF2 tag")
			}
			tag = string(data[pos : pos+4])
			pos += 4
		} else {
			tag = woff2Tags[index]
		}
		if _, exists := table[tag]; exists {
			return nil, true, fmt.Errorf("duplicate WOFF2 table %q", tag)
		}
		table[tag] = nil
		size, err := readWOFF2Base128(data, &pos)
		if err != nil {
			return nil, true, err
		}
		version := flags >> 6
		changed := version != 0
		if tag == "glyf" || tag == "loca" {
			if version != 0 && version != 3 {
				return nil, true, fmt.Errorf("invalid WOFF2 glyph transform")
			}
			changed = version == 0
		} else if version != 0 && (tag != "hmtx" || version != 1) {
			return nil, true, fmt.Errorf("invalid WOFF2 table transform %q", tag)
		}
		if changed {
			transformed = true
			size, err = readWOFF2Base128(data, &pos)
			if err != nil {
				return nil, true, err
			}
			if tag == "loca" && size != 0 {
				return nil, true, fmt.Errorf("invalid WOFF2 loca length")
			}
		}
		total += uint64(size)
		if total+uint64(12+19*count) > fontContainerLimit {
			return nil, true, fmt.Errorf("WOFF2 font exceeds size limit")
		}
		tags = append(tags, tag)
		sizes = append(sizes, size)
	}
	end := uint64(pos) + uint64(binary.BigEndian.Uint32(data[20:]))
	if end > uint64(len(data)) {
		return nil, true, fmt.Errorf("invalid WOFF2 compressed length")
	}
	if err := validateWOFFBlocks(data, end, 28); err != nil {
		return nil, true, err
	}
	if transformed {
		return nil, false, nil
	}
	reader := brotli.NewReader(bytes.NewReader(data[pos:end]))
	body, err := io.ReadAll(io.LimitReader(reader, int64(total)+1))
	if err != nil {
		return nil, true, fmt.Errorf("decode WOFF2 data: %w", err)
	}
	if uint64(len(body)) != total {
		return nil, true, fmt.Errorf("invalid WOFF2 expanded length")
	}
	for i, tag := range tags {
		size := int(sizes[i])
		table[tag], body = body[:size], body[size:]
	}
	decoded, err := serializeOTF(table)
	return decoded, true, err
}

// readWOFF2Base128 读取WOFF2无符号变长整数，拒绝溢出和前导零
// 入参: data 编码数据, pos 当前读取位置
// 返回: uint32 整数值, error 编码错误
func readWOFF2Base128(data []byte, pos *int) (uint32, error) {
	var value uint32
	for i := range 5 {
		if *pos >= len(data) {
			return 0, fmt.Errorf("truncated WOFF2 integer")
		}
		b := data[*pos]
		*pos++
		if i == 0 && b == 0x80 || value > 0x1ffffff {
			return 0, fmt.Errorf("invalid WOFF2 integer")
		}
		value = value<<7 | uint32(b&127)
		if b&128 == 0 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("invalid WOFF2 integer")
}

// decodeWOFF 展开WOFF表数据，不解析或更改字体轮廓
// 入参: data WOFF文件
// 返回: []byte OpenType数据, error 封装错误
func decodeWOFF(data []byte) ([]byte, error) {
	if len(data) < 44 || uint64(binary.BigEndian.Uint32(data[8:])) != uint64(len(data)) || binary.BigEndian.Uint16(data[14:]) != 0 {
		return nil, fmt.Errorf("invalid WOFF header")
	}
	count := int(binary.BigEndian.Uint16(data[12:]))
	if count == 0 || count > (len(data)-44)/20 {
		return nil, fmt.Errorf("invalid WOFF table count")
	}
	table := make(map[string][]byte, count)
	ranges := make([][2]uint64, 0, count)
	total := uint64(12 + 16*count)
	for i := range count {
		record := data[44+i*20:]
		tag := string(record[:4])
		offset, packed, size := uint64(binary.BigEndian.Uint32(record[4:])), uint64(binary.BigEndian.Uint32(record[8:])), uint64(binary.BigEndian.Uint32(record[12:]))
		total += (size + 3) &^ 3
		if _, exists := table[tag]; exists || offset < uint64(44+20*count) || offset%4 != 0 || packed > size || offset+packed > uint64(len(data)) || total > fontContainerLimit {
			return nil, fmt.Errorf("invalid WOFF table %q", tag)
		}
		body := data[offset : offset+packed]
		if packed < size {
			reader, err := zlib.NewReader(bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			body, err = io.ReadAll(io.LimitReader(reader, int64(size)+1))
			reader.Close()
			if err != nil {
				return nil, err
			}
			if uint64(len(body)) != size {
				return nil, fmt.Errorf("invalid WOFF expanded length")
			}
		}
		table[tag] = body
		ranges = append(ranges, [2]uint64{offset, offset + packed})
	}
	if total != uint64(binary.BigEndian.Uint32(data[16:])) {
		return nil, fmt.Errorf("invalid WOFF SFNT size")
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	end := uint64(44 + 20*count)
	for _, interval := range ranges {
		if interval[0] != (end+3)&^3 {
			return nil, fmt.Errorf("invalid WOFF table ranges")
		}
		end = interval[1]
	}
	if err := validateWOFFBlocks(data, end, 24); err != nil {
		return nil, err
	}
	return serializeOTF(table)
}

// validateWOFFBlocks 校验附加数据块的边界及对齐，字体元数据不写入输出
// 入参: data 字体文件, end 字体数据结束位置, fields 元数据偏移字段位置
// 返回: error 附加块错误
func validateWOFFBlocks(data []byte, end uint64, fields int) error {
	for _, pos := range []int{fields, fields + 12} {
		offset, length := uint64(binary.BigEndian.Uint32(data[pos:])), uint64(binary.BigEndian.Uint32(data[pos+4:]))
		if offset == 0 {
			if length != 0 || pos == fields && binary.BigEndian.Uint32(data[pos+8:]) != 0 {
				return fmt.Errorf("invalid WOFF optional block")
			}
			continue
		}
		if length == 0 || offset != (end+3)&^3 || offset+length > uint64(len(data)) {
			return fmt.Errorf("invalid WOFF block range")
		}
		for _, b := range data[end:offset] {
			if b != 0 {
				return fmt.Errorf("invalid WOFF padding")
			}
		}
		end = offset + length
	}
	if end > uint64(len(data)) || uint64(len(data)) > (end+3)&^3 {
		return fmt.Errorf("invalid WOFF trailing data")
	}
	for _, b := range data[end:] {
		if b != 0 {
			return fmt.Errorf("invalid WOFF padding")
		}
	}
	return nil
}
