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

package pdf

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/tdewolff/canvas"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	"github.com/xiaoqidun/pdfgo"
)

// cmapChunkLimit 为ISO 32000-1规定的ToUnicode CMap单段最大映射条数
const cmapChunkLimit = 100

// FontEntry 保存单个PDF输出字体的对象引用、资源名及并发安全字形子集表
type FontEntry struct {
	Ref          pdfgo.Reference
	Name         pdfgo.Name
	Font         *canvas.Font
	Vertical     bool
	StandardName string
	mu           sync.Mutex
	subset       *canvas.FontSubsetter
}

// FontRegistry 管理文档级PDF字体对象去重、并发字形收集与并行子集写出
type FontRegistry struct {
	mu       sync.Mutex
	allocRef func() pdfgo.Reference
	entries  map[fontRegistryKey]*FontEntry
	order    []*FontEntry
	subset   bool
	compress bool
	level    int
}

// fontRegistryKey 按字体实例与书写方向隔离PDF字体对象
type fontRegistryKey struct {
	font     *canvas.Font
	vertical bool
}

// compiledFont 保存单个字体并行子集化与压缩后的待写出字节数据
type compiledFont struct {
	entry            *FontEntry
	dict             pdfgo.Dictionary
	toUnicodeData    []byte
	fontFileData     []byte
	fontFileDict     pdfgo.Dictionary
	fontFileKey      pdfgo.Name
	cidMapData       []byte
	encodingCMapData []byte
	encodingCMapDict pdfgo.Dictionary
	subsetTag        string
	baseFontName     string
	err              error
}

// NewFontRegistry 创建文档级并发安全字体注册表
// 入参: allocRef 对象引用分配器, subset 是否启用字形子集化, compress 是否压缩字体流, level zlib压缩级别
// 返回: *FontRegistry 字体注册表
func NewFontRegistry(allocRef func() pdfgo.Reference, subset, compress bool, level int) *FontRegistry {
	return &FontRegistry{
		allocRef: allocRef,
		entries:  make(map[fontRegistryKey]*FontEntry),
		subset:   subset,
		compress: compress,
		level:    level,
	}
}

// compileFontEntry 将单个字体登记项编译为Type1或Type0+CIDFont字典及压缩字体流
// 入参: entry 字体登记项, doSubset 是否子集化, compress 是否Flate压缩, level zlib级别, zw 复用压缩器, zbuf 复用缓冲
// 返回: compiledFont 编译结果, *zlib.Writer 复用后的压缩器
func compileFontEntry(entry *FontEntry, doSubset, compress bool, level int, zw *zlib.Writer, zbuf *bytes.Buffer) (compiledFont, *zlib.Writer) {
	if entry.StandardName != "" {
		dict := pdfgo.Dictionary{
			"Type":     pdfgo.Name("Font"),
			"Subtype":  pdfgo.Name("Type1"),
			"BaseFont": pdfgo.Name(entry.StandardName),
		}
		if entry.StandardName != "Symbol" && entry.StandardName != "ZapfDingbats" {
			dict["Encoding"] = pdfgo.Name("WinAnsiEncoding")
		}
		return compiledFont{entry: entry, dict: dict}, zw
	}
	font := entry.Font
	if font == nil || font.SFNT == nil || font.SFNT.Head == nil || font.SFNT.Head.UnitsPerEm == 0 {
		return compiledFont{entry: entry, err: fmt.Errorf("invalid PDF font unitsPerEm")}, zw
	}
	glyphIDs := entry.subset.List()
	fontProgram := font.SFNT.Write()
	parsed, parseErr := basefont.ParseSFNT(fontProgram)
	subsetOK := false
	if doSubset && parseErr == nil {
		if subsetData, err := basefont.SubsetSFNTForPDFParsed(parsed, glyphIDs); err == nil {
			fontProgram = subsetData
			subsetOK = true
		}
	}
	f := 1000.0 / float64(font.SFNT.Head.UnitsPerEm)
	widths := make([]int, len(glyphIDs))
	for subsetGID, gid := range glyphIDs {
		widths[subsetGID] = int(f*float64(font.SFNT.GlyphAdvance(gid)) + 0.5)
	}
	dw := 1000
	if len(widths) > 0 {
		dw = widths[0]
	}
	customCMap := !subsetOK && !font.SFNT.IsTrueType
	registry, ordering, supplement := "Adobe", "Identity", 0
	var targetCIDs []uint16
	if customCMap {
		targetCIDs = make([]uint16, len(glyphIDs))
		rawCIDs, reg, ord, sup, isCID := basefont.CFFCIDInfo(parsed)
		if isCID && reg != "" && ord != "" {
			registry, ordering, supplement = reg, ord, sup
		}
		for i, gid := range glyphIDs {
			if isCID && int(gid) < len(rawCIDs) {
				targetCIDs[i] = rawCIDs[gid]
			} else {
				targetCIDs[i] = gid
			}
		}
	}
	var wArr pdfgo.Array
	if customCMap {
		wArr = buildCIDWidthsArray(targetCIDs, widths, dw)
	} else {
		wArr = buildFontWidthsArray(widths, dw)
	}
	toUnicodeRaw := buildToUnicodeCMap(font, parsed, glyphIDs)
	toUnicodeData := toUnicodeRaw
	var err error
	if compress {
		toUnicodeData, zw, err = CompressBytes(zw, zbuf, level, toUnicodeRaw)
		if err != nil {
			return compiledFont{entry: entry, err: err}, zw
		}
	}
	fontFileData := fontProgram
	fontFileDict := pdfgo.Dictionary{}
	if compress {
		fontFileData, zw, err = CompressBytes(zw, zbuf, level, fontProgram)
		if err != nil {
			return compiledFont{entry: entry, err: err}, zw
		}
		fontFileDict["Filter"] = pdfgo.Name("FlateDecode")
	}
	var cidSubtype pdfgo.Name
	var fontFileKey pdfgo.Name
	if font.SFNT.IsTrueType {
		cidSubtype = "CIDFontType2"
		fontFileKey = "FontFile2"
	} else {
		cidSubtype = "CIDFontType0"
		fontFileKey = "FontFile3"
		fontFileDict["Subtype"] = pdfgo.Name("OpenType")
	}
	name := font.Name()
	if parseErr == nil {
		if psName := basefont.SFNTName(parsed, 6); psName != "" {
			name = psName
		}
	}
	baseFontName := strings.ReplaceAll(name, " ", "")
	if baseFontName == "" {
		baseFontName = "Font"
	}
	baseFont := baseFontName
	subsetTag := ""
	if subsetOK {
		subsetTag = fontSubsetTag(fontProgram, entry.Ref.Number, entry.Vertical)
		baseFont = subsetTag + "+" + baseFontName
	}
	encoding := pdfgo.Name("Identity-H")
	if entry.Vertical {
		encoding = "Identity-V"
	}
	ascent, descent := 0, 0
	if font.SFNT.Hhea != nil {
		ascent = int(f * float64(font.SFNT.Hhea.Ascender))
		descent = int(f * float64(font.SFNT.Hhea.Descender))
		if descent > 0 {
			descent = -descent
		}
	}
	italicAngle := 0.0
	if font.SFNT.Post != nil && finite(float64(font.SFNT.Post.ItalicAngle)) {
		italicAngle = float64(font.SFNT.Post.ItalicAngle)
	}
	capHeight := 0
	if font.SFNT.OS2 != nil {
		capHeight = int(f * float64(font.SFNT.OS2.SCapHeight))
	}
	cidDict := pdfgo.Dictionary{
		"Type":     pdfgo.Name("Font"),
		"Subtype":  cidSubtype,
		"BaseFont": pdfgo.Name(baseFont),
		"DW":       pdfgo.Integer(dw),
		"W":        wArr,
		"CIDSystemInfo": pdfgo.Dictionary{
			"Registry":   pdfgo.String(registry),
			"Ordering":   pdfgo.String(ordering),
			"Supplement": pdfgo.Integer(supplement),
		},
		"FontDescriptor": pdfgo.Dictionary{
			"Type":     pdfgo.Name("FontDescriptor"),
			"FontName": pdfgo.Name(baseFont),
			"Flags":    pdfgo.Integer(4),
			"FontBBox": pdfgo.Array{
				pdfgo.Integer(int(f * float64(font.SFNT.Head.XMin))),
				pdfgo.Integer(int(f * float64(font.SFNT.Head.YMin))),
				pdfgo.Integer(int(f * float64(font.SFNT.Head.XMax))),
				pdfgo.Integer(int(f * float64(font.SFNT.Head.YMax))),
			},
			"ItalicAngle": pdfgo.Real(italicAngle),
			"Ascent":      pdfgo.Integer(ascent),
			"Descent":     pdfgo.Integer(descent),
			"CapHeight":   pdfgo.Integer(capHeight),
			"StemV":       pdfgo.Integer(80),
		},
	}
	if entry.Vertical {
		dw2, w2 := buildFontVerticalMetrics(font, glyphIDs, targetCIDs, widths, f)
		cidDict["DW2"] = dw2
		if len(w2) > 0 {
			cidDict["W2"] = w2
		}
	}
	var cidMapData []byte
	var encodingCMapData []byte
	var encodingCMapDict pdfgo.Dictionary
	if font.SFNT.IsTrueType {
		if subsetOK {
			cidDict["CIDToGIDMap"] = pdfgo.Name("Identity")
		} else {
			rawMap := make([]byte, 2*len(glyphIDs))
			for subsetGID, gid := range glyphIDs {
				rawMap[subsetGID*2] = byte(gid >> 8)
				rawMap[subsetGID*2+1] = byte(gid)
			}
			cidMapData = rawMap
			if compress {
				cidMapData, zw, err = CompressBytes(zw, zbuf, level, rawMap)
				if err != nil {
					return compiledFont{entry: entry, err: err}, zw
				}
			}
		}
	} else if !subsetOK {
		cmapName := fmt.Sprintf("CMap%d", entry.Ref.Number)
		wMode := 0
		if entry.Vertical {
			wMode = 1
		}
		cmapRaw := buildCIDEncodingCMap(cmapName, registry, ordering, supplement, wMode, targetCIDs)
		encodingCMapData = cmapRaw
		encodingCMapDict = pdfgo.Dictionary{
			"Type":     pdfgo.Name("CMap"),
			"CMapName": pdfgo.Name(cmapName),
			"CIDSystemInfo": pdfgo.Dictionary{
				"Registry":   pdfgo.String(registry),
				"Ordering":   pdfgo.String(ordering),
				"Supplement": pdfgo.Integer(supplement),
			},
			"WMode": pdfgo.Integer(wMode),
		}
		if compress {
			encodingCMapData, zw, err = CompressBytes(zw, zbuf, level, cmapRaw)
			if err != nil {
				return compiledFont{entry: entry, err: err}, zw
			}
			encodingCMapDict["Filter"] = pdfgo.Name("FlateDecode")
		}
	}
	dict := pdfgo.Dictionary{
		"Type":            pdfgo.Name("Font"),
		"Subtype":         pdfgo.Name("Type0"),
		"BaseFont":        pdfgo.Name(baseFont),
		"Encoding":        encoding,
		"DescendantFonts": pdfgo.Array{cidDict},
	}
	return compiledFont{
		entry:            entry,
		dict:             dict,
		toUnicodeData:    toUnicodeData,
		fontFileData:     fontFileData,
		fontFileDict:     fontFileDict,
		fontFileKey:      fontFileKey,
		cidMapData:       cidMapData,
		encodingCMapData: encodingCMapData,
		encodingCMapDict: encodingCMapDict,
		subsetTag:        subsetTag,
		baseFontName:     baseFontName,
	}, zw
}

// buildFontWidthsArray 按连续相同宽度压缩构造CIDFont的W宽度数组
// 入参: widths 按子集CID排列的字宽切片, dw 缺省宽度
// 返回: pdfgo.Array PDF宽度数组
func buildFontWidthsArray(widths []int, dw int) pdfgo.Array {
	var w pdfgo.Array
	i, j := 1, 1
	for k, width := range widths {
		if k != 0 && width != widths[j] {
			if k-j > 4 {
				if i < j {
					arr := make(pdfgo.Array, 0, j-i)
					for _, v := range widths[i:j] {
						arr = append(arr, pdfgo.Integer(v))
					}
					w = append(w, pdfgo.Integer(i), arr)
				}
				if widths[j] != dw {
					w = append(w, pdfgo.Integer(j), pdfgo.Integer(k-1), pdfgo.Integer(widths[j]))
				}
				i = k
			}
			j = k
		}
	}
	if i < len(widths) {
		arr := make(pdfgo.Array, 0, len(widths)-i)
		for _, v := range widths[i:] {
			arr = append(arr, pdfgo.Integer(v))
		}
		w = append(w, pdfgo.Integer(i), arr)
	}
	return w
}

// buildToUnicodeCMap 生成遵循ISO 32000-1每百条分块限制的ToUnicode CMap字节流
// 入参: font 源画布字体, parsed 已解析标准SFNT字体(可为nil), glyphIDs 子集字形列表
// 返回: []byte ToUnicode CMap流
func buildToUnicodeCMap(font *canvas.Font, parsed *basefont.SFNT, glyphIDs []uint16) []byte {
	type bfRangeItem struct {
		start, end uint16
		unicode    uint32
	}
	type bfCharItem struct {
		cid     uint16
		unicode uint32
	}
	var ranges []bfRangeItem
	var chars []bfCharItem
	startGID := uint16(0)
	startUni := uint32('\uFFFD')
	length := uint16(1)
	flush := func() {
		if length > 1 {
			ranges = append(ranges, bfRangeItem{start: startGID, end: startGID + length - 1, unicode: startUni})
		} else {
			chars = append(chars, bfCharItem{cid: startGID, unicode: startUni})
		}
	}
	if len(glyphIDs) > 1 {
		for subsetIdx, gid := range glyphIDs[1:] {
			var rs []rune
			if parsed != nil {
				rs = parsed.GlyphToUnicode(gid)
			} else {
				rs = font.SFNT.GlyphToUnicode(gid)
			}
			if len(rs) == 0 {
				continue
			}
			uni := uint32(rs[0])
			if uni >= 0x10000 && uni <= 0x10FFFF {
				uni -= 0x10000
				uni = (0xD800+(uni>>10)&0x3FF)<<16 + 0xDC00 + uni&0x3FF
			}
			cid := uint16(subsetIdx + 1)
			if cid == startGID+length && (startGID>>8) == (cid>>8) && uni == startUni+uint32(length) && (startUni>>8) == (uni>>8) {
				length++
			} else {
				flush()
				startGID = cid
				startUni = uni
				length = 1
			}
		}
	}
	flush()
	var buf bytes.Buffer
	buf.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n/CIDSystemInfo <</Registry(Adobe)/Ordering(UCS)/Supplement 0>> def\n/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n1 begincodespacerange\n<0000> <FFFF> endcodespacerange")
	for i := 0; i < len(ranges); i += cmapChunkLimit {
		chunk := ranges[i:min(i+cmapChunkLimit, len(ranges))]
		fmt.Fprintf(&buf, "\n%d beginbfrange", len(chunk))
		for _, item := range chunk {
			if item.unicode > 0xFFFF {
				fmt.Fprintf(&buf, "\n<%04X> <%04X> <%08X>", item.start, item.end, item.unicode)
			} else {
				fmt.Fprintf(&buf, "\n<%04X> <%04X> <%04X>", item.start, item.end, item.unicode)
			}
		}
		buf.WriteString("\nendbfrange")
	}
	for i := 0; i < len(chars); i += cmapChunkLimit {
		chunk := chars[i:min(i+cmapChunkLimit, len(chars))]
		fmt.Fprintf(&buf, "\n%d beginbfchar", len(chunk))
		for _, item := range chunk {
			if item.unicode > 0xFFFF {
				fmt.Fprintf(&buf, "\n<%04X> <%08X>", item.cid, item.unicode)
			} else {
				fmt.Fprintf(&buf, "\n<%04X> <%04X>", item.cid, item.unicode)
			}
		}
		buf.WriteString("\nendbfchar")
	}
	buf.WriteString("\nendcmap\nCMapName currentdict /CMap defineresource pop\nend\nend")
	return buf.Bytes()
}

// buildCIDWidthsArray 按稀疏目标CID排序并压缩构造CIDFont的W宽度数组
// 入参: targetCIDs 按子集索引排列的目标CID切片, widths 对应字宽切片, dw 缺省宽度
// 返回: pdfgo.Array PDF宽度数组
func buildCIDWidthsArray(targetCIDs []uint16, widths []int, dw int) pdfgo.Array {
	type item struct {
		cid   uint16
		width int
	}
	list := make([]item, 0, len(targetCIDs))
	for i, cid := range targetCIDs {
		if i < len(widths) && widths[i] != dw {
			list = append(list, item{cid: cid, width: widths[i]})
		}
	}
	if len(list) == 0 {
		return nil
	}
	slices.SortFunc(list, func(a, b item) int { return cmp.Compare(a.cid, b.cid) })
	var dedup []item
	for _, it := range list {
		if len(dedup) == 0 || dedup[len(dedup)-1].cid != it.cid {
			dedup = append(dedup, it)
		}
	}
	var w pdfgo.Array
	for i := 0; i < len(dedup); {
		j := i + 1
		for j < len(dedup) && dedup[j].cid == dedup[j-1].cid+1 && dedup[j].width == dedup[i].width {
			j++
		}
		if j-i >= 3 {
			w = append(w, pdfgo.Integer(dedup[i].cid), pdfgo.Integer(dedup[j-1].cid), pdfgo.Integer(dedup[i].width))
			i = j
			continue
		}
		end := i + 1
		for end < len(dedup) && dedup[end].cid == dedup[end-1].cid+1 {
			k := end + 1
			for k < len(dedup) && dedup[k].cid == dedup[k-1].cid+1 && dedup[k].width == dedup[end].width {
				k++
			}
			if k-end >= 3 {
				break
			}
			end++
		}
		arr := make(pdfgo.Array, 0, end-i)
		for _, it := range dedup[i:end] {
			arr = append(arr, pdfgo.Integer(it.width))
		}
		w = append(w, pdfgo.Integer(dedup[i].cid), arr)
		i = end
	}
	return w
}

// buildFontVerticalMetrics 计算竖排CIDFont的DW2默认垂直度量与W2逐字形垂直度量数组
// 入参: font 源画布字体, glyphIDs 子集字形列表, targetCIDs 可选目标CID映射, widths 水平宽度列表, f 字体单位到千分em换算系数
// 返回: pdfgo.Array DW2数组, pdfgo.Array W2数组
func buildFontVerticalMetrics(font *canvas.Font, glyphIDs, targetCIDs []uint16, widths []int, f float64) (pdfgo.Array, pdfgo.Array) {
	if !finite(f) || f <= 0 {
		f = 1
	}
	defaultOriginY, vorgOverrides, hasVORG := parseVORGTable(font.SFNT.Tables["VORG"])
	defaultVY := 0
	if font.SFNT.Hhea != nil {
		defaultVY = int(math.Round(f * float64(font.SFNT.Hhea.Ascender)))
	}
	if hasVORG {
		defaultVY = int(math.Round(f * float64(defaultOriginY)))
	} else if defaultVY == 0 {
		defaultVY = 880
	}
	type vMetric struct {
		cid uint16
		w1y int
		vx  int
		vy  int
	}
	metrics := make([]vMetric, len(glyphIDs))
	freq := make(map[int]int, len(glyphIDs))
	defaultW1Y := -1000
	bestCount := 0
	for i, gid := range glyphIDs {
		cid := uint16(i)
		if len(targetCIDs) == len(glyphIDs) {
			cid = targetCIDs[i]
		}
		w1y := -int(math.Round(f * float64(font.SFNT.GlyphVerticalAdvance(gid))))
		vx := widths[i] / 2
		vy := defaultVY
		if hasVORG {
			if override, ok := vorgOverrides[gid]; ok {
				vy = int(math.Round(f * float64(override)))
			}
		}
		metrics[i] = vMetric{cid: cid, w1y: w1y, vx: vx, vy: vy}
		freq[w1y]++
		if freq[w1y] > bestCount || (freq[w1y] == bestCount && w1y < defaultW1Y) {
			bestCount = freq[w1y]
			defaultW1Y = w1y
		}
	}
	dw2 := pdfgo.Array{pdfgo.Integer(defaultVY), pdfgo.Integer(defaultW1Y)}
	var nonDefault []vMetric
	for _, m := range metrics {
		if m.w1y != defaultW1Y || m.vy != defaultVY {
			nonDefault = append(nonDefault, m)
		}
	}
	if len(nonDefault) == 0 {
		return dw2, nil
	}
	slices.SortFunc(nonDefault, func(a, b vMetric) int { return cmp.Compare(a.cid, b.cid) })
	var dedup []vMetric
	for _, m := range nonDefault {
		if len(dedup) == 0 || dedup[len(dedup)-1].cid != m.cid {
			dedup = append(dedup, m)
		}
	}
	var w2 pdfgo.Array
	for i := 0; i < len(dedup); {
		j := i + 1
		for j < len(dedup) && dedup[j].cid == dedup[j-1].cid+1 && dedup[j].w1y == dedup[i].w1y && dedup[j].vx == dedup[i].vx && dedup[j].vy == dedup[i].vy {
			j++
		}
		if j-i >= 2 {
			w2 = append(w2, pdfgo.Integer(dedup[i].cid), pdfgo.Integer(dedup[j-1].cid), pdfgo.Integer(dedup[i].w1y), pdfgo.Integer(dedup[i].vx), pdfgo.Integer(dedup[i].vy))
			i = j
			continue
		}
		end := i + 1
		for end < len(dedup) && dedup[end].cid == dedup[end-1].cid+1 {
			if end+1 < len(dedup) && dedup[end+1].cid == dedup[end].cid+1 && dedup[end+1].w1y == dedup[end].w1y && dedup[end+1].vx == dedup[end].vx && dedup[end+1].vy == dedup[end].vy {
				break
			}
			end++
		}
		arr := make(pdfgo.Array, 0, (end-i)*3)
		for _, m := range dedup[i:end] {
			arr = append(arr, pdfgo.Integer(m.w1y), pdfgo.Integer(m.vx), pdfgo.Integer(m.vy))
		}
		w2 = append(w2, pdfgo.Integer(dedup[i].cid), arr)
		i = end
	}
	return dw2, w2
}

// parseVORGTable 解析OpenType VORG表的默认垂直原点与逐字形覆写表
// 入参: data VORG原始字节
// 返回: int16 默认垂直原点Y, map[uint16]int16 逐字形垂直原点映射, bool 表是否合法
func parseVORGTable(data []byte) (int16, map[uint16]int16, bool) {
	if len(data) < 8 || binary.BigEndian.Uint32(data[:4]) != 0x00010000 {
		return 0, nil, false
	}
	defaultY := int16(binary.BigEndian.Uint16(data[4:6]))
	count := int(binary.BigEndian.Uint16(data[6:8]))
	if 8+count*4 > len(data) {
		return 0, nil, false
	}
	var overrides map[uint16]int16
	if count > 0 {
		overrides = make(map[uint16]int16, count)
		for i := range count {
			pos := 8 + i*4
			gid := binary.BigEndian.Uint16(data[pos : pos+2])
			y := int16(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
			overrides[gid] = y
		}
	}
	return defaultY, overrides, true
}

// buildCIDEncodingCMap 生成将两字节子集代码映射到源CFF真实CID或GID的自定义PDF CMap流
// 入参: cmapName CMap名称, registry 注册机构, ordering 排序名, supplement 补充版本, wMode 书写模式, targetCIDs 目标CID映射表
// 返回: []byte 标准PostScript CMap流字节
func buildCIDEncodingCMap(cmapName, registry, ordering string, supplement, wMode int, targetCIDs []uint16) []byte {
	type cidRangeItem struct {
		start, end uint16
		cid        uint16
	}
	type cidCharItem struct {
		code uint16
		cid  uint16
	}
	var ranges []cidRangeItem
	var chars []cidCharItem
	if len(targetCIDs) > 0 {
		startCode := uint16(0)
		startCID := targetCIDs[0]
		length := uint16(1)
		flush := func() {
			if length > 1 {
				ranges = append(ranges, cidRangeItem{start: startCode, end: startCode + length - 1, cid: startCID})
			} else {
				chars = append(chars, cidCharItem{code: startCode, cid: startCID})
			}
		}
		for idx := 1; idx < len(targetCIDs); idx++ {
			code := uint16(idx)
			cid := targetCIDs[idx]
			if code == startCode+length && cid == startCID+length && (startCode>>8) == (code>>8) {
				length++
			} else {
				flush()
				startCode = code
				startCID = cid
				length = 1
			}
		}
		flush()
	}
	var buf bytes.Buffer
	buf.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n/CIDSystemInfo <</Registry")
	appendPDFLiteralString(&buf, []byte(registry))
	buf.WriteString("/Ordering")
	appendPDFLiteralString(&buf, []byte(ordering))
	fmt.Fprintf(&buf, "/Supplement %d>> def\n/CMapName ", supplement)
	appendPDFName(&buf, cmapName)
	fmt.Fprintf(&buf, " def\n/CMapType 1 def\n/WMode %d def\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange", wMode)
	for i := 0; i < len(ranges); i += cmapChunkLimit {
		chunk := ranges[i:min(i+cmapChunkLimit, len(ranges))]
		fmt.Fprintf(&buf, "\n%d begincidrange", len(chunk))
		for _, item := range chunk {
			fmt.Fprintf(&buf, "\n<%04X> <%04X> %d", item.start, item.end, item.cid)
		}
		buf.WriteString("\nendcidrange")
	}
	for i := 0; i < len(chars); i += cmapChunkLimit {
		chunk := chars[i:min(i+cmapChunkLimit, len(chars))]
		fmt.Fprintf(&buf, "\n%d begincidchar", len(chunk))
		for _, item := range chunk {
			fmt.Fprintf(&buf, "\n<%04X> %d", item.code, item.cid)
		}
		buf.WriteString("\nendcidchar")
	}
	buf.WriteString("\nendcmap\nCMapName currentdict /CMap defineresource pop\nend\nend")
	return buf.Bytes()
}

// fontSubsetTag 按字体程序、对象编号与书写方向生成高熵确定性六位大写子集前缀
// 入参: program 字体程序字节, refNum 对象编号, vertical 是否竖排
// 返回: string 六位子集前缀
func fontSubsetTag(program []byte, refNum int64, vertical bool) string {
	h := sha256.New()
	var meta [9]byte
	binary.LittleEndian.PutUint64(meta[:8], uint64(refNum))
	if vertical {
		meta[8] = 1
	}
	h.Write(meta[:])
	h.Write(program)
	var sum [32]byte
	h.Sum(sum[:0])
	val := binary.LittleEndian.Uint64(sum[:8])
	var tag [6]byte
	for i := range 6 {
		tag[i] = 'A' + byte(val%26)
		val /= 26
	}
	return string(tag[:])
}

// nextSubsetTag 将六位大写字母子集前缀按26进制递增以消除文档内极小概率碰撞
// 入参: tag 当前六位前缀
// 返回: string 递增后的六位前缀
func nextSubsetTag(tag string) string {
	var out [6]byte
	copy(out[:], tag)
	for i := 5; i >= 0; i-- {
		if out[i] < 'Z' {
			out[i]++
			break
		}
		out[i] = 'A'
	}
	return string(out[:])
}

// standardPDFFontName 仅在无嵌入字体数据时精确匹配PDF 14种内置Type1标准字体名称
// 入参: font 画布字体
// 返回: string 标准字体名，不匹配时为空
func standardPDFFontName(font *canvas.Font) string {
	if font == nil || (font.SFNT != nil && (font.SFNT.Head != nil || len(font.SFNT.Tables) > 0)) {
		return ""
	}
	name := strings.TrimSpace(font.Name())
	switch name {
	case "Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
		"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
		"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
		"Symbol", "ZapfDingbats":
		return name
	}
	switch strings.ToLower(name) {
	case "courier":
		switch font.Style() {
		case canvas.FontRegular:
			return "Courier"
		case canvas.FontBold:
			return "Courier-Bold"
		case canvas.FontItalic:
			return "Courier-Oblique"
		case canvas.FontBold | canvas.FontItalic:
			return "Courier-BoldOblique"
		}
	case "zapfdingbats":
		if font.Style() == canvas.FontRegular {
			return "ZapfDingbats"
		}
	case "helvetica":
		switch font.Style() {
		case canvas.FontRegular:
			return "Helvetica"
		case canvas.FontBold:
			return "Helvetica-Bold"
		case canvas.FontItalic:
			return "Helvetica-Oblique"
		case canvas.FontBold | canvas.FontItalic:
			return "Helvetica-BoldOblique"
		}
	case "symbol":
		if font.Style() == canvas.FontRegular {
			return "Symbol"
		}
	case "times", "times-roman":
		switch font.Style() {
		case canvas.FontRegular:
			return "Times-Roman"
		case canvas.FontBold:
			return "Times-Bold"
		case canvas.FontItalic:
			return "Times-Italic"
		case canvas.FontBold | canvas.FontItalic:
			return "Times-BoldItalic"
		}
	}
	return ""
}

// MapGlyph 线程安全地登记源字形编号并返回子集内连续CID编号
// 入参: gid 源字体字形编号
// 返回: uint16 子集字形编号
func (e *FontEntry) MapGlyph(gid uint16) uint16 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.subset.Get(gid)
}

// Register 线程安全地获取或注册字体对应的PDF资源名、间接引用与字形映射项
// 入参: font 画布字体, vertical 是否竖排
// 返回: pdfgo.Name 资源名, pdfgo.Reference 对象引用, *FontEntry 字体登记项
func (r *FontRegistry) Register(font *canvas.Font, vertical bool) (pdfgo.Name, pdfgo.Reference, *FontEntry) {
	std := standardPDFFontName(font)
	key := fontRegistryKey{font: font, vertical: vertical && std == ""}
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries[key]; ok {
		return entry.Name, entry.Ref, entry
	}
	ref := r.allocRef()
	name := pdfgo.Name("F" + strconv.Itoa(len(r.order)))
	entry := &FontEntry{
		Ref:          ref,
		Name:         name,
		Font:         font,
		Vertical:     key.vertical,
		StandardName: std,
	}
	if std == "" {
		entry.subset = canvas.NewFontSubsetter()
	}
	r.entries[key] = entry
	r.order = append(r.order, entry)
	return name, ref, entry
}

// WriteAll 多核并发完成全部字体的子集化、ToUnicode构建与Flate压缩，并按序流式写出
// 入参: ctx 取消上下文, writer 流式PDF写出器, workers 并发工作协程数
// 返回: error 子集化或写出错误
func (r *FontRegistry) WriteAll(ctx context.Context, writer *StreamWriter, workers int) error {
	r.mu.Lock()
	list := slices.Clone(r.order)
	r.mu.Unlock()
	if len(list) == 0 {
		return ctx.Err()
	}
	slices.SortFunc(list, func(a, b *FontEntry) int { return cmp.Compare(a.Ref.Number, b.Ref.Number) })
	results := make([]compiledFont, len(list))
	if workers < 1 {
		workers = 1
	}
	if workers > len(list) {
		workers = len(list)
	}
	if workers == 1 {
		var zw *zlib.Writer
		var zbuf bytes.Buffer
		for i, entry := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			results[i], zw = compileFontEntry(entry, r.subset, r.compress, r.level, zw, &zbuf)
			if results[i].err != nil {
				return results[i].err
			}
		}
	} else {
		jobs := make(chan int, len(list))
		for i := range list {
			jobs <- i
		}
		close(jobs)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var zw *zlib.Writer
				var zbuf bytes.Buffer
				for i := range jobs {
					if ctx.Err() != nil {
						return
					}
					results[i], zw = compileFontEntry(list[i], r.subset, r.compress, r.level, zw, &zbuf)
				}
			}()
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	usedTags := make(map[string]bool, len(results))
	for i := range results {
		res := &results[i]
		if res.err != nil {
			return res.err
		}
		if res.entry.StandardName != "" {
			if err := writer.WriteObject(res.entry.Ref, res.dict); err != nil {
				return err
			}
			continue
		}
		toUnicodeRef := writer.AllocRef()
		toUnicodeDict := pdfgo.Dictionary{}
		if r.compress {
			toUnicodeDict["Filter"] = pdfgo.Name("FlateDecode")
		}
		if err := writer.WritePreencodedStream(toUnicodeRef, toUnicodeDict, res.toUnicodeData); err != nil {
			return err
		}
		res.toUnicodeData = nil
		fontFileRef := writer.AllocRef()
		if err := writer.WritePreencodedStream(fontFileRef, res.fontFileDict, res.fontFileData); err != nil {
			return err
		}
		res.fontFileData = nil
		res.dict["ToUnicode"] = toUnicodeRef
		descendants := res.dict["DescendantFonts"].(pdfgo.Array)
		cidFont := descendants[0].(pdfgo.Dictionary)
		descriptor := cidFont["FontDescriptor"].(pdfgo.Dictionary)
		descriptor[res.fontFileKey] = fontFileRef
		if res.subsetTag != "" {
			tag := res.subsetTag
			for usedTags[tag] {
				tag = nextSubsetTag(tag)
			}
			usedTags[tag] = true
			if tag != res.subsetTag {
				fullName := pdfgo.Name(tag + "+" + res.baseFontName)
				res.dict["BaseFont"] = fullName
				cidFont["BaseFont"] = fullName
				descriptor["FontName"] = fullName
			}
		}
		if res.encodingCMapData != nil {
			encRef := writer.AllocRef()
			if err := writer.WritePreencodedStream(encRef, res.encodingCMapDict, res.encodingCMapData); err != nil {
				return err
			}
			res.encodingCMapData = nil
			res.encodingCMapDict = nil
			res.dict["Encoding"] = encRef
		}
		if res.cidMapData != nil {
			cidMapRef := writer.AllocRef()
			cidMapDict := pdfgo.Dictionary{}
			if r.compress {
				cidMapDict["Filter"] = pdfgo.Name("FlateDecode")
			}
			if err := writer.WritePreencodedStream(cidMapRef, cidMapDict, res.cidMapData); err != nil {
				return err
			}
			res.cidMapData = nil
			cidFont["CIDToGIDMap"] = cidMapRef
		}
		if err := writer.WriteObject(res.entry.Ref, res.dict); err != nil {
			return err
		}
	}
	return ctx.Err()
}
