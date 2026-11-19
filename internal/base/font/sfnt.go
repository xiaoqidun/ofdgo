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
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/go-text/typesetting/font/opentype/tables"
)

// SFNT 保存独立字体表和基础度量，不包含绘图库对象
type SFNT struct {
	Tables                             map[string][]byte
	IsTrueType, IsCFF                  bool
	cmaps                              []sfntCmap
	hasUnicodeCmap                     bool
	units, count, horizontal, vertical uint16
	reverseOnce                        sync.Once
	reverse                            map[uint16][]rune
}

// ParseSFNT 校验独立字体的必需表、度量范围及字符映射
// 入参: data 独立SFNT数据
// 返回: *SFNT 字体, error 格式错误
func ParseSFNT(data []byte) (*SFNT, error) {
	table, err := FileTables(data, 0)
	if err != nil {
		return nil, err
	}
	return ParseSFNTTables(table)
}

// ParseSFNTTables 校验字体表并解析度量和字符映射，借用表数据且不修改其内容
// 入参: table 字体表
// 返回: *SFNT 字体, error 格式错误
func ParseSFNTTables(table map[string][]byte) (*SFNT, error) {
	head, maxp, hhea := table["head"], table["maxp"], table["hhea"]
	if len(head) != 54 || len(maxp) < 6 || len(hhea) < 36 {
		return nil, fmt.Errorf("missing or truncated font metrics")
	}
	f := &SFNT{Tables: maps.Clone(table), IsTrueType: table["glyf"] != nil, IsCFF: table["CFF "] != nil}
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
	cmaps, err := readSFNTCmaps(table["cmap"])
	if err != nil {
		return nil, err
	}
	f.cmaps = cmaps
	f.hasUnicodeCmap = slices.ContainsFunc(cmaps, func(c sfntCmap) bool { return c.isUnicode() })
	for _, cmap := range f.cmaps {
		if cmap.duplicate {
			continue
		}
		for _, glyph := range cmap.pairs() {
			if glyph >= f.count {
				return nil, fmt.Errorf("cmap glyph index out of range")
			}
		}
	}
	return f, nil
}

// UnitsPerEm 返回字体设计单位
// 返回: uint16 每em单位数
func (f *SFNT) UnitsPerEm() uint16 { return f.units }

// NumGlyphs 返回字形总数
// 返回: uint16 字形数
func (f *SFNT) NumGlyphs() uint16 { return f.count }

// CmapEntries 返回各编码记录的映射项总数，共享子表按记录分别计数
// 返回: uint64 用于缓存预算的映射项数量
func (f *SFNT) CmapEntries() uint64 {
	var count uint64
	for _, cmap := range f.cmaps {
		count += uint64(len(cmap.mapping)) + cmap.entries
	}
	return count
}

// GlyphIndex 按Unicode优先级顺序查找字符，仅在无Unicode子表时回退非Unicode子表
// 入参: char 字符
// 返回: uint16 字形编号
func (f *SFNT) GlyphIndex(char rune) uint16 {
	for _, cmap := range f.cmaps {
		if f.skipCmap(cmap) {
			continue
		}
		if gid, ok := cmap.lookup(char); ok && gid != 0 {
			return gid
		}
	}
	return 0
}

// GlyphAdvance 返回水平字宽
// 入参: gid 字形编号
// 返回: uint16 字宽
func (f *SFNT) GlyphAdvance(gid uint16) uint16 {
	if gid >= f.count {
		return 0
	}
	return binary.BigEndian.Uint16(f.Tables["hmtx"][int(min(gid, f.horizontal-1))*4:])
}

// GlyphVerticalAdvance 返回纵向字距，无垂直度量时使用em高度
// 入参: gid 字形编号
// 返回: uint16 纵向字距
func (f *SFNT) GlyphVerticalAdvance(gid uint16) uint16 {
	if f.vertical == 0 {
		return f.units
	}
	if gid >= f.count {
		return 0
	}
	return binary.BigEndian.Uint16(f.Tables["vmtx"][int(min(gid, f.vertical-1))*4:])
}

// VerticalMetrics 依据OS/2排版标志选择行度量，缺失时使用hhea
// 返回: uint16 上升高度, uint16 下降高度, uint16 行间隙
func (f *SFNT) VerticalMetrics() (uint16, uint16, uint16) {
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
func (f *SFNT) Write() []byte { data, _ := SerializeOTF(f.Tables); return data }

// sfntCmap 保存编码记录及字符映射，连续区间与共享子表不重复展开
type sfntCmap struct {
	platform, encoding, format uint16
	offset                     uint32
	mapping                    map[rune]uint16
	groups                     []byte
	entries                    uint64
	duplicate                  bool
}

// isUnicode 判断编码子表是否为常规Unicode字符映射
// 返回: bool 是否为Unicode映射子表
func (c sfntCmap) isUnicode() bool {
	return c.priority() <= 2
}

// priority 返回编码子表的Unicode检索优先级，值越小越优先
// 返回: int 优先级数值
func (c sfntCmap) priority() int {
	if c.format == 14 {
		return 4
	}
	if c.platform == 0 && c.encoding == 4 || c.platform == 3 && c.encoding == 10 {
		return 0
	}
	if c.platform == 0 && c.encoding == 3 || c.platform == 3 && c.encoding == 1 {
		return 1
	}
	if c.platform == 0 && (c.encoding <= 2 || c.encoding == 6) {
		return 2
	}
	return 3
}

// pairs 按原映射返回字符与字形，连续编码段不展开存储
// 返回: iter.Seq2[rune, uint16] 非零有效字符映射
func (c sfntCmap) pairs() iter.Seq2[rune, uint16] {
	return func(yield func(rune, uint16) bool) {
		for char, glyph := range c.mapping {
			if !yield(char, glyph) {
				return
			}
		}
		for offset := 0; offset+12 <= len(c.groups); offset += 12 {
			group := c.groups[offset:]
			first, last, glyph := binary.BigEndian.Uint32(group), binary.BigEndian.Uint32(group[4:]), binary.BigEndian.Uint32(group[8:])
			if first > last || first > utf8.MaxRune || glyph > 65535 {
				continue
			}
			end := min(uint64(last), uint64(first)+65535-uint64(glyph), uint64(utf8.MaxRune))
			for char := uint64(first); char <= end; char++ {
				id := uint16(uint64(glyph) + char - uint64(first))
				if id != 0 && utf8.ValidRune(rune(char)) && !yield(rune(char), id) {
					return
				}
			}
		}
	}
}

// lookup 查找原字符映射，连续编码段使用有序区间查询
// 入参: char 字符
// 返回: uint16 字形编号, bool 是否存在映射
func (c sfntCmap) lookup(char rune) (uint16, bool) {
	if glyph, ok := c.mapping[char]; ok {
		return glyph, true
	}
	if !utf8.ValidRune(char) {
		return 0, false
	}
	low, high := 0, len(c.groups)/12
	for low < high {
		middle := low + (high-low)/2
		group := c.groups[middle*12:]
		first, last := binary.BigEndian.Uint32(group), binary.BigEndian.Uint32(group[4:])
		if uint32(char) < first {
			high = middle
		} else if uint32(char) > last {
			low = middle + 1
		} else {
			glyph := uint64(binary.BigEndian.Uint32(group[8:])) + uint64(uint32(char)-first)
			return uint16(glyph), glyph > 0 && glyph <= 65535
		}
	}
	return 0, false
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
	data, err := NormalizeCmap(data)
	if err != nil {
		return nil, err
	}
	if _, _, err := tables.ParseCmap(data); err != nil {
		return nil, err
	}
	count := int(binary.BigEndian.Uint16(data[2:]))
	result := make([]sfntCmap, 0, count)
	parsed := make(map[uint32]sfntCmap)
	for i := range count {
		record := data[4+8*i:]
		offset := binary.BigEndian.Uint32(record[4:])
		if uint64(offset)+2 > uint64(len(data)) {
			return nil, fmt.Errorf("invalid cmap offset")
		}
		sub := data[offset:]
		format := binary.BigEndian.Uint16(sub)
		cmap, found := parsed[offset]
		if !found {
			cmap.mapping = make(map[rune]uint16)
			var err error
			switch format {
			case 0:
				parseCmapFormat0(sub, cmap.mapping)
			case 4:
				parseCmapFormat4(sub, cmap.mapping)
			case 6:
				parseCmapFormat6(sub, cmap.mapping)
			case 12:
				if len(sub) >= 16 {
					length := uint64(binary.BigEndian.Uint32(sub[4:]))
					count := uint64(binary.BigEndian.Uint32(sub[12:]))
					if length >= 16 && length <= uint64(len(sub)) && count <= (length-16)/12 {
						cmap.groups = sub[16 : 16+12*count]
						for offset := 0; offset < len(cmap.groups); offset += 12 {
							first := binary.BigEndian.Uint32(cmap.groups[offset:])
							last := binary.BigEndian.Uint32(cmap.groups[offset+4:])
							if first > last || offset != 0 && first <= binary.BigEndian.Uint32(cmap.groups[offset-8:]) {
								cmap.groups = nil
								parseCmapFormat12(sub, cmap.mapping)
								break
							}
						}
						if cmap.groups != nil {
							cmap.mapping = nil
							for range cmap.pairs() {
								cmap.entries++
							}
						}
					}
				}
			case 2:
				cmap.mapping, err = parseCmapFormat2(sub)
			case 8, 10, 13:
				cmap.mapping, err = parseCmapExtended(sub)
			case 14:
			default:
				return nil, fmt.Errorf("unsupported cmap format %d", format)
			}
			if err != nil {
				return nil, err
			}
			parsed[offset] = cmap
		}
		cmap.platform, cmap.encoding, cmap.format, cmap.offset = binary.BigEndian.Uint16(record), binary.BigEndian.Uint16(record[2:]), format, offset
		result = append(result, cmap)
	}
	slices.SortStableFunc(result, func(a, b sfntCmap) int { return a.priority() - b.priority() })
	seen := make(map[uint32]bool, len(result))
	for i := range result {
		result[i].duplicate = seen[result[i].offset]
		seen[result[i].offset] = true
	}
	return result, nil
}

// skipCmap 判断查找字符映射时是否跳过该编码子表
// 入参: c 编码子表
// 返回: bool 是否跳过
func (f *SFNT) skipCmap(c sfntCmap) bool {
	return c.duplicate || c.format == 14 || f.hasUnicodeCmap && !c.isUnicode()
}

// glyphCharacters 返回指向指定字形的全部字符，连续区间按需反查
// 入参: gid 字形编号
// 返回: []rune 排序后的字符
func (f *SFNT) glyphCharacters(gid uint16) []rune {
	f.reverseOnce.Do(func() {
		f.reverse = make(map[uint16][]rune)
		seen := make(map[rune]bool)
		for _, cmap := range f.cmaps {
			if f.skipCmap(cmap) {
				continue
			}
			for char, glyph := range cmap.mapping {
				if glyph != 0 && !seen[char] && f.GlyphIndex(char) == glyph {
					f.reverse[glyph] = append(f.reverse[glyph], char)
					seen[char] = true
				}
			}
		}
		for _, chars := range f.reverse {
			slices.Sort(chars)
		}
	})
	if gid == 0 {
		return nil
	}
	chars := slices.Clone(f.reverse[gid])
	for _, cmap := range f.cmaps {
		if f.skipCmap(cmap) {
			continue
		}
		for offset := 0; offset+12 <= len(cmap.groups); offset += 12 {
			group := cmap.groups[offset:]
			first, last, glyph := binary.BigEndian.Uint32(group), binary.BigEndian.Uint32(group[4:]), binary.BigEndian.Uint32(group[8:])
			if first > last || uint32(gid) < glyph {
				continue
			}
			char := uint64(first) + uint64(uint32(gid)-glyph)
			if char <= uint64(last) && char <= utf8.MaxRune && utf8.ValidRune(rune(char)) && f.GlyphIndex(rune(char)) == gid {
				chars = append(chars, rune(char))
			}
		}
	}
	slices.Sort(chars)
	return slices.Compact(chars)
}

// sideBearing 返回指定方向的字形边距
// 入参: gid 字形编号, vertical 是否读取顶部边距
// 返回: int16 边距
func (f *SFNT) sideBearing(gid uint16, vertical bool) int16 {
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

// glyphDependencies 批量收集复合字形依赖，校验引用、记录长度和嵌套深度
// 入参: glyphs 字形编号
// 返回: []uint16 依赖字形, error 字形错误
func (f *SFNT) glyphDependencies(glyphs ...uint16) ([]uint16, error) {
	heights, active := make(map[uint16]int), make(map[uint16]bool)
	var result []uint16
	var visit func(uint16) error
	visit = func(id uint16) error {
		if id >= f.count || active[id] || len(active) > 64 {
			return fmt.Errorf("invalid composite glyph reference")
		}
		if height := heights[id]; height != 0 {
			if len(active)+height > 65 {
				return fmt.Errorf("invalid composite glyph reference")
			}
			return nil
		}
		heights[id], active[id] = 1, true
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
			if heights[child] == 0 {
				result = append(result, child)
			}
			if err := visit(child); err != nil {
				return err
			}
			heights[id] = max(heights[id], heights[child]+1)
			if flags&0x20 == 0 {
				break
			}
		}
		return nil
	}
	for _, id := range glyphs {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}
