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
	"fmt"
	"hash/crc32"
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

// MapGlyph 线程安全地登记源字形编号并返回子集内连续CID编号
// 入参: gid 源字体字形编号
// 返回: uint16 子集字形编号
func (e *FontEntry) MapGlyph(gid uint16) uint16 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.subset.Get(gid)
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

// fontRegistryKey 按字体实例与书写方向隔离PDF字体对象
type fontRegistryKey struct {
	font     *canvas.Font
	vertical bool
}

// compiledFont 保存单个字体并行子集化与压缩后的待写出字节数据
type compiledFont struct {
	entry         *FontEntry
	dict          pdfgo.Dictionary
	toUnicodeData []byte
	fontFileData  []byte
	fontFileDict  pdfgo.Dictionary
	fontFileKey   pdfgo.Name
	cidMapData    []byte
	err           error
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
			"Encoding": pdfgo.Name("WinAnsiEncoding"),
		}
		return compiledFont{entry: entry, dict: dict}, zw
	}
	font := entry.Font
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
	widths := make([]int, len(glyphIDs)+1)
	for subsetGID, gid := range glyphIDs {
		widths[subsetGID] = int(f*float64(font.SFNT.GlyphAdvance(gid)) + 0.5)
	}
	dw := widths[0]
	wArr := buildFontWidthsArray(widths, dw)
	toUnicodeRaw := buildToUnicodeCMap(font, glyphIDs)
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
	baseFont := strings.ReplaceAll(name, " ", "")
	if baseFont == "" {
		baseFont = "Font"
	}
	if subsetOK {
		baseFont = fontSubsetTag(fontProgram) + "+" + baseFont
	}
	encoding := pdfgo.Name("Identity-H")
	if entry.Vertical {
		encoding = "Identity-V"
	}
	cidDict := pdfgo.Dictionary{
		"Type":     pdfgo.Name("Font"),
		"Subtype":  cidSubtype,
		"BaseFont": pdfgo.Name(baseFont),
		"DW":       pdfgo.Integer(dw),
		"W":        wArr,
		"CIDSystemInfo": pdfgo.Dictionary{
			"Registry":   pdfgo.String("Adobe"),
			"Ordering":   pdfgo.String("Identity"),
			"Supplement": pdfgo.Integer(0),
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
			"ItalicAngle": pdfgo.Real(float64(font.SFNT.Post.ItalicAngle)),
			"Ascent":      pdfgo.Integer(int(f * float64(font.SFNT.Hhea.Ascender))),
			"Descent":     pdfgo.Integer(-int(f * float64(font.SFNT.Hhea.Descender))),
			"CapHeight":   pdfgo.Integer(int(f * float64(font.SFNT.OS2.SCapHeight))),
			"StemV":       pdfgo.Integer(80),
		},
	}
	var cidMapData []byte
	if !subsetOK {
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
	} else if font.SFNT.IsTrueType {
		cidDict["CIDToGIDMap"] = pdfgo.Name("Identity")
	}
	dict := pdfgo.Dictionary{
		"Type":            pdfgo.Name("Font"),
		"Subtype":         pdfgo.Name("Type0"),
		"BaseFont":        pdfgo.Name(baseFont),
		"Encoding":        encoding,
		"DescendantFonts": pdfgo.Array{cidDict},
	}
	return compiledFont{
		entry:         entry,
		dict:          dict,
		toUnicodeData: toUnicodeData,
		fontFileData:  fontFileData,
		fontFileDict:  fontFileDict,
		fontFileKey:   fontFileKey,
		cidMapData:    cidMapData,
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
// 入参: font 源画布字体, glyphIDs 子集字形列表
// 返回: []byte ToUnicode CMap流
func buildToUnicodeCMap(font *canvas.Font, glyphIDs []uint16) []byte {
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
	for subsetIdx, gid := range glyphIDs[1:] {
		rs := font.SFNT.GlyphToUnicode(gid)
		if len(rs) == 0 {
			continue
		}
		uni := uint32(rs[0])
		if uni >= 0x10000 && uni <= 0x10FFFF {
			uni -= 0x10000
			uni = (0xD800+(uni>>10)&0x3FF)<<16 + 0xDC00 + uni&0x3FF
		}
		cid := uint16(subsetIdx + 1)
		if cid == startGID+length && uni == startUni+uint32(length) && (startUni>>8) == (uni>>8) {
			length++
		} else {
			flush()
			startGID = cid
			startUni = uni
			length = 1
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

// fontSubsetTag 按嵌入字体程序校验和生成确定性六位大写子集前缀
// 入参: program 字体程序字节
// 返回: string 六位子集前缀
func fontSubsetTag(program []byte) string {
	var tag [6]byte
	checksum := crc32.ChecksumIEEE(program)
	for i := range 6 {
		tag[i] = 'A' + byte(checksum%26)
		checksum >>= 5
	}
	return string(tag[:])
}

// standardPDFFontName 识别可映射为PDF 14种内置Type1标准字体的名称与样式
// 入参: font 画布字体
// 返回: string 标准字体名，不匹配时为空
func standardPDFFontName(font *canvas.Font) string {
	switch strings.ToLower(font.Name()) {
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
	case "dingbats":
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
	case "times":
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
