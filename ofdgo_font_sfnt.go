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
	"sort"
	"sync"

	"github.com/go-text/typesetting/font/opentype/tables"
)

// sfntFont 保存独立字体表和基础度量，不包含绘图库对象
type sfntFont struct {
	Tables                             map[string][]byte
	IsTrueType, IsCFF                  bool
	cmaps                              []sfntCmap
	units, count, horizontal, vertical uint16
	reverseOnce                        sync.Once
	reverse                            map[uint16][]rune
}

// sfntCmap 保存编码记录及对应字符映射
type sfntCmap struct {
	platform, encoding, format uint16
	mapping                    map[rune]uint16
}

// parseSFNTFont 校验独立字体的必需表、度量范围及字符映射
// 入参: data 独立SFNT数据
// 返回: *sfntFont 字体, error 格式错误
func parseSFNTFont(data []byte) (*sfntFont, error) {
	table, err := fontFileTables(data, 0)
	if err != nil {
		return nil, err
	}
	head, maxp, hhea := table["head"], table["maxp"], table["hhea"]
	if len(head) != 54 || len(maxp) < 6 || len(hhea) < 36 {
		return nil, fmt.Errorf("missing or truncated font metrics")
	}
	f := &sfntFont{Tables: table, IsTrueType: table["glyf"] != nil, IsCFF: table["CFF "] != nil}
	f.units, f.count = binary.BigEndian.Uint16(head[18:]), binary.BigEndian.Uint16(maxp[4:])
	f.horizontal = binary.BigEndian.Uint16(hhea[34:])
	if f.units < 16 || f.units > 16384 || f.count == 0 || f.IsTrueType == f.IsCFF || !validSFNTMetrics(table["hmtx"], f.count, f.horizontal) {
		return nil, fmt.Errorf("invalid font outlines or horizontal metrics")
	}
	if vhea := table["vhea"]; vhea != nil {
		if len(vhea) < 36 {
			return nil, fmt.Errorf("truncated vertical metrics header")
		}
		f.vertical = binary.BigEndian.Uint16(vhea[34:])
		if !validSFNTMetrics(table["vmtx"], f.count, f.vertical) {
			return nil, fmt.Errorf("invalid vertical metrics")
		}
	}
	if f.IsTrueType {
		format := binary.BigEndian.Uint16(head[50:])
		if format > 1 || len(table["loca"]) < (int(f.count)+1)*(2+2*int(format)) {
			return nil, fmt.Errorf("invalid glyph location table")
		}
	}
	f.cmaps, err = readSFNTCmaps(table["cmap"])
	if err != nil {
		return nil, err
	}
	for _, cmap := range f.cmaps {
		for _, glyph := range cmap.mapping {
			if glyph >= f.count {
				return nil, fmt.Errorf("cmap glyph index out of range")
			}
		}
	}
	return f, nil
}

// validSFNTMetrics 校验完整度量记录和末尾边距数组的范围
// 入参: data 度量表, count 字形数, metrics 完整度量记录数
// 返回: bool 是否有效
func validSFNTMetrics(data []byte, count, metrics uint16) bool {
	return metrics != 0 && metrics <= count && len(data) >= int(metrics)*4+int(count-metrics)*2
}

// readSFNTCmaps 校验各编码子表并保留原顺序，共享子表只解析一次
// 入参: data cmap表
// 返回: []sfntCmap 编码记录, error 表结构错误
func readSFNTCmaps(data []byte) ([]sfntCmap, error) {
	data, err := normalizeCmap(data)
	if err != nil {
		return nil, err
	}
	if _, _, err := tables.ParseCmap(data); err != nil {
		return nil, err
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	result := make([]sfntCmap, 0, count)
	parsed := make(map[uint32]map[rune]uint16)
	for i := range count {
		record := data[4+8*i:]
		offset := binary.BigEndian.Uint32(record[4:])
		if uint64(offset)+2 > uint64(len(data)) {
			return nil, fmt.Errorf("invalid cmap offset")
		}
		sub := data[offset:]
		format := binary.BigEndian.Uint16(sub)
		mapping, found := parsed[offset]
		if !found {
			mapping = make(map[rune]uint16)
			var err error
			switch format {
			case 0:
				parseCmapFormat0(sub, mapping)
			case 4:
				parseCmapFormat4(sub, mapping)
			case 6:
				parseCmapFormat6(sub, mapping)
			case 12:
				parseCmapFormat12(sub, mapping)
			case 2:
				mapping, err = parseCmapFormat2(sub)
			case 8, 10, 13:
				mapping, err = parseCmapExtended(sub)
			case 14:
			default:
				return nil, fmt.Errorf("unsupported cmap format %d", format)
			}
			if err != nil {
				return nil, err
			}
			parsed[offset] = mapping
		}
		result = append(result, sfntCmap{binary.BigEndian.Uint16(record), binary.BigEndian.Uint16(record[2:]), format, mapping})
	}
	return result, nil
}

// UnitsPerEm 返回字体设计单位
// 返回: uint16 每em单位数
func (f *sfntFont) UnitsPerEm() uint16 { return f.units }

// NumGlyphs 返回字形总数
// 返回: uint16 字形数
func (f *sfntFont) NumGlyphs() uint16 { return f.count }

// GlyphIndex 按编码表顺序查找字符，不替换缺失字形
// 入参: char 字符
// 返回: uint16 字形编号
func (f *sfntFont) GlyphIndex(char rune) uint16 {
	for _, cmap := range f.cmaps {
		if gid := cmap.mapping[char]; gid != 0 {
			return gid
		}
	}
	return 0
}

// glyphCharacters 返回指向指定字形的全部字符
// 入参: gid 字形编号
// 返回: []rune 排序后的字符
func (f *sfntFont) glyphCharacters(gid uint16) []rune {
	f.reverseOnce.Do(func() {
		f.reverse = make(map[uint16][]rune)
		seen := make(map[rune]bool)
		for _, cmap := range f.cmaps {
			for char, glyph := range cmap.mapping {
				if glyph != 0 && !seen[char] {
					f.reverse[glyph] = append(f.reverse[glyph], char)
					seen[char] = true
				}
			}
		}
		for _, chars := range f.reverse {
			sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
		}
	})
	return f.reverse[gid]
}

// GlyphAdvance 返回水平字宽
// 入参: gid 字形编号
// 返回: uint16 字宽
func (f *sfntFont) GlyphAdvance(gid uint16) uint16 {
	if gid >= f.count {
		return 0
	}
	return binary.BigEndian.Uint16(f.Tables["hmtx"][int(min(gid, f.horizontal-1))*4:])
}

// GlyphVerticalAdvance 返回纵向字距，无垂直度量时使用em高度
// 入参: gid 字形编号
// 返回: uint16 纵向字距
func (f *sfntFont) GlyphVerticalAdvance(gid uint16) uint16 {
	if f.vertical == 0 {
		return f.units
	}
	if gid >= f.count {
		return 0
	}
	return binary.BigEndian.Uint16(f.Tables["vmtx"][int(min(gid, f.vertical-1))*4:])
}

// sideBearing 返回指定方向的字形边距
// 入参: gid 字形编号, vertical 是否读取顶部边距
// 返回: int16 边距
func (f *sfntFont) sideBearing(gid uint16, vertical bool) int16 {
	data, count := f.Tables["hmtx"], f.horizontal
	if vertical {
		data, count = f.Tables["vmtx"], f.vertical
	}
	if count == 0 || gid >= f.count {
		return 0
	}
	offset := int(gid)*4 + 2
	if gid >= count {
		offset = int(count)*4 + int(gid-count)*2
	}
	return int16(binary.BigEndian.Uint16(data[offset:]))
}

// VerticalMetrics 依据OS/2排版标志选择行度量，缺失时使用hhea
// 返回: uint16 上升高度, uint16 下降高度, uint16 行间隙
func (f *sfntFont) VerticalMetrics() (uint16, uint16, uint16) {
	hhea, os2 := f.Tables["hhea"], f.Tables["OS/2"]
	a, d, gap := int(int16(binary.BigEndian.Uint16(hhea[4:]))), int(int16(binary.BigEndian.Uint16(hhea[6:]))), int(int16(binary.BigEndian.Uint16(hhea[8:])))
	if len(os2) >= 78 {
		if binary.BigEndian.Uint16(os2[62:])&0x80 != 0 {
			ta, td := int(int16(binary.BigEndian.Uint16(os2[68:]))), int(int16(binary.BigEndian.Uint16(os2[70:])))
			if ta > 0 && td < 0 {
				a, d, gap = ta, td, int(int16(binary.BigEndian.Uint16(os2[72:])))
			}
		} else {
			wa, wd := int(binary.BigEndian.Uint16(os2[74:])), int(binary.BigEndian.Uint16(os2[76:]))
			if wa != 0 && wd != 0 {
				gap, a, d = a-d+gap-wa-wd, wa, -wd
			}
		}
	}
	return uint16(max(0, a)), uint16(max(0, -d)), uint16(min(65535, max(0, gap)))
}

// Write 保留原始表数据并重新计算字体校验和
// 返回: []byte 独立字体
func (f *sfntFont) Write() []byte { data, _ := serializeOTF(f.Tables); return data }

// glyphDependencies 收集复合字形依赖，拒绝循环引用和截断记录
// 入参: gid 字形编号
// 返回: []uint16 依赖字形, error 字形错误
func (f *sfntFont) glyphDependencies(gid uint16) ([]uint16, error) {
	seen, active := make(map[uint16]bool), make(map[uint16]bool)
	var result []uint16
	var visit func(uint16) error
	visit = func(id uint16) error {
		if id >= f.count || active[id] || len(active) > 64 {
			return fmt.Errorf("invalid composite glyph reference")
		}
		if seen[id] {
			return nil
		}
		seen[id], active[id] = true, true
		defer delete(active, id)
		data, err := trueTypeGlyphData(f.Tables, id)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return nil
		}
		if len(data) < 10 {
			return fmt.Errorf("truncated glyph header")
		}
		if int16(binary.BigEndian.Uint16(data)) >= 0 {
			return nil
		}
		for pos := 10; ; {
			if len(data)-pos < 4 {
				return fmt.Errorf("truncated composite glyph")
			}
			flags, child := binary.BigEndian.Uint16(data[pos:]), binary.BigEndian.Uint16(data[pos+2:])
			pos += 6
			if flags&1 != 0 {
				pos += 2
			}
			switch flags & 0xc8 {
			case 8:
				pos += 2
			case 0x40:
				pos += 4
			case 0x80:
				pos += 8
			case 0:
			default:
				return fmt.Errorf("invalid composite transform")
			}
			if pos > len(data) {
				return fmt.Errorf("truncated composite transform")
			}
			if !seen[child] {
				result = append(result, child)
			}
			if err := visit(child); err != nil {
				return err
			}
			if flags&0x20 == 0 {
				break
			}
		}
		return nil
	}
	err := visit(gid)
	return result, err
}
