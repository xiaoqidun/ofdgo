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
	"image/color"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tdewolff/canvas"
	canvastext "github.com/tdewolff/canvas/text"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfFontSubset 保存独立CFF输出字体及实际用字，包含缺省字形
type pdfFontSubset struct {
	font   *canvas.Font
	glyphs map[uint16]bool
}

// pdfGlyphItem 保存同基线批量输出的单个字形及局部横坐标
type pdfGlyphItem struct {
	glyph textGlyph
	id    uint16
	x     float64
}

// RenderText 记录后端文字区段，写出时校验数量并关联原文
// 入参: text 画布文字, matrix 文字变换
func (r *pdfRenderer) RenderText(text *canvas.Text, matrix canvas.Matrix) {
	if len(r.text) == 0 {
		r.text = append(r.text, codecpdf.TextPage{})
	}
	page := &r.text[len(r.text)-1]
	text.WalkSpans(func(_, _ float64, span canvas.TextSpan) {
		if span.IsText() {
			page.Count++
		}
	})
	r.PDF.RenderText(text, matrix)
}

// preserveText 将一个OFD文字对象关联到完整PDF文字区段
// 入参: first 起始文字对象序号, text 原文
func (r *pdfRenderer) preserveText(first int, text string) {
	page := &r.text[len(r.text)-1]
	if page.Count > first {
		page.Replacements = append(page.Replacements, pdfgo.TextReplacement{First: first, Count: page.Count - first, Text: text})
		r.exactText = true
	}
}

// preserveFauxBold 记录需要Tr=2填充并描边伪粗体的PDF文字对象区段
// 入参: first 起始文字对象序号, lineWidth 描边线宽(点), fillColor 填充纯色
func (r *pdfRenderer) preserveFauxBold(first int, lineWidth float64, fillColor color.RGBA) {
	if len(r.text) == 0 || lineWidth <= 0 {
		return
	}
	last := len(r.text) - 1
	count := r.text[last].Count - first
	if count <= 0 {
		return
	}
	for len(r.textFauxBold) <= last {
		r.textFauxBold = append(r.textFauxBold, nil)
	}
	r.textFauxBold[last] = append(r.textFauxBold[last], codecpdf.TextFauxBold{First: first, Count: count, LineWidth: lineWidth, Color: fillColor})
}

// outputFontBytes 缓存源字体序列化后的SFNT字节，避免CFF子集切分时重复拼接整套字库
// 入参: src 源画布字体
// 返回: []byte SFNT字体字节
func (r *pdfRenderer) outputFontBytes(src *canvas.Font) []byte {
	if r.fontData != nil {
		if data, ok := r.fontData[src]; ok {
			return data
		}
	}
	data := basefont.SFNTData(src.Tables)
	if r.fontData == nil {
		r.fontData = make(map[*canvas.Font][]byte)
	}
	r.fontData[src] = data
	return data
}

// outputFont 获取或创建与源字体隔离的独立输出字体
// 入参: src 源画布字体
// 返回: *canvas.Font 独立输出字体, error 字体解析错误
func (r *pdfRenderer) outputFont(src *canvas.Font) (*canvas.Font, error) {
	if font := r.fonts[src]; font != nil {
		return font, nil
	}
	loaded, err := loadCanvasFont(r.outputFontBytes(src), 0, src.Style())
	if err != nil {
		return nil, err
	}
	copy := *src
	copy.SFNT = loaded.SFNT
	font := &copy
	if r.fonts == nil {
		r.fonts = make(map[*canvas.Font]*canvas.Font)
	}
	r.fonts[src] = font
	return font, nil
}

// glyphText 使用独立输出字体和既定字形建立PDF文字，不改变字体缓存及定位
// 入参: face 字体, glyph 字形
// 返回: *canvas.Text 画布文字, error 字体解析错误
func (r *pdfRenderer) glyphText(face *canvas.FontFace, glyph textGlyph) (*canvas.Text, error) {
	srcFont := face.Font
	font, err := r.outputFont(srcFont)
	if err != nil {
		return nil, err
	}
	outputFace := *face
	outputFace.Font = font
	outputFace.FauxBold = 0
	outputFace.FauxItalic = 0
	face = &outputFace
	var text *canvas.Text
	if glyph.GlyphID < 0 || glyph.GlyphID > 0xffff {
		text = canvas.NewTextLine(face, glyph.Text, canvas.Left)
	} else {
		id := uint16(glyph.GlyphID)
		text = canvas.NewTextLine(face, " ", canvas.Left)
		text.WalkLines(func(_ float64, spans []canvas.TextSpan) {
			for i := range spans {
				spans[i].Text = glyph.Text
				spans[i].Glyphs = []canvastext.Glyph{{SFNT: face.Font.SFNT, Size: face.Size, ID: id, XAdvance: int32(face.Font.GlyphAdvance(id)), Text: glyph.Text}}
			}
		})
	}
	if font.IsCFF {
		if err := r.cffTextFont(srcFont, font, text); err != nil {
			return nil, err
		}
	}
	return text, nil
}

// glyphsText 将同基线连续字形合并为单次PDF文字绘制，以累计字体单位步进消除漂移
// 入参: face 字体, items 连续字形序列, hScale 水平缩放比例
// 返回: *canvas.Text 画布文字, error 字体解析错误
func (r *pdfRenderer) glyphsText(face *canvas.FontFace, items []pdfGlyphItem, hScale float64) (*canvas.Text, error) {
	if len(items) == 1 {
		return r.glyphText(face, items[0].glyph)
	}
	srcFont := face.Font
	font, err := r.outputFont(srcFont)
	if err != nil {
		return nil, err
	}
	outputFace := *face
	outputFace.Font = font
	outputFace.FauxBold = 0
	outputFace.FauxItalic = 0
	face = &outputFace
	glyphs := make([]canvastext.Glyph, len(items))
	var joined strings.Builder
	unitScale := hScale * face.MmPerEm
	x0 := items[0].x
	var cursor int64
	for i, item := range items {
		adv := int32(face.Font.GlyphAdvance(item.id))
		if i+1 < len(items) {
			target := int64(math.Round((items[i+1].x - x0) / unitScale))
			adv = int32(target - cursor)
			cursor = target
		}
		glyphs[i] = canvastext.Glyph{SFNT: face.Font.SFNT, Size: face.Size, ID: item.id, XAdvance: adv, Text: item.glyph.Text}
		joined.WriteString(item.glyph.Text)
	}
	text := canvas.NewTextLine(face, " ", canvas.Left)
	combinedText := joined.String()
	text.WalkLines(func(_ float64, spans []canvas.TextSpan) {
		for i := range spans {
			spans[i].Text = combinedText
			spans[i].Glyphs = glyphs
		}
	})
	if font.IsCFF {
		if err := r.cffTextFont(srcFont, font, text); err != nil {
			return nil, err
		}
	}
	return text, nil
}

// cffTextFont 按实际用字隔离CFF子集，优先复用已有字形所在子集并复用缓存字体数据
// 入参: srcFont 源画布字体, font 独立输出字体, text 已定位文字
// 返回: error 字形数量或字体解析错误
func (r *pdfRenderer) cffTextFont(srcFont, font *canvas.Font, text *canvas.Text) error {
	const limit = 229
	ids := []uint16{0}
	text.WalkSpans(func(_, _ float64, span canvas.TextSpan) {
		for _, glyph := range span.Glyphs {
			if !slices.Contains(ids, glyph.ID) {
				ids = append(ids, glyph.ID)
			}
		}
	})
	if len(ids) > limit {
		return fmt.Errorf("CFF text glyph count exceeds output subset capacity")
	}
	var selected *pdfFontSubset
	if glyphMap := r.cffGlyphMap[font]; glyphMap != nil && len(ids) > 1 {
		if candidate := glyphMap[ids[1]]; candidate != nil {
			count := len(candidate.glyphs)
			for _, id := range ids {
				if !candidate.glyphs[id] {
					count++
				}
			}
			if count <= limit {
				selected = candidate
			}
		}
	}
	if selected == nil {
		bestOverlap := -1
		for _, subset := range r.cffFonts[font] {
			count := len(subset.glyphs)
			overlap := 0
			for _, id := range ids {
				if subset.glyphs[id] {
					overlap++
				} else {
					count++
				}
			}
			if count <= limit && overlap > bestOverlap {
				bestOverlap = overlap
				selected = subset
				if overlap == len(ids) {
					break
				}
			}
		}
	}
	if selected == nil {
		output := font
		if len(r.cffFonts[font]) != 0 {
			loaded, err := loadCanvasFont(r.outputFontBytes(srcFont), 0, font.Style())
			if err != nil {
				return err
			}
			copy := *font
			copy.SFNT = loaded.SFNT
			output = &copy
		}
		selected = &pdfFontSubset{font: output, glyphs: make(map[uint16]bool)}
		if r.cffFonts == nil {
			r.cffFonts = make(map[*canvas.Font][]*pdfFontSubset)
		}
		r.cffFonts[font] = append(r.cffFonts[font], selected)
	}
	if r.cffGlyphMap == nil {
		r.cffGlyphMap = make(map[*canvas.Font]map[uint16]*pdfFontSubset)
	}
	if r.cffGlyphMap[font] == nil {
		r.cffGlyphMap[font] = make(map[uint16]*pdfFontSubset)
	}
	for _, id := range ids {
		selected.glyphs[id] = true
		if id != 0 {
			r.cffGlyphMap[font][id] = selected
		}
	}
	if selected.font != font {
		text.WalkLines(func(_ float64, spans []canvas.TextSpan) {
			for i := range spans {
				face := *spans[i].Face
				face.Font = selected.font
				spans[i].Face = &face
				for j := range spans[i].Glyphs {
					spans[i].Glyphs[j].SFNT = selected.font.SFNT
				}
			}
		})
	}
	return nil
}

// pdfTextSupported 核对原生文字的字形和未裁剪范围，支持合法GID索引并复用字形缓存
// 入参: object 文字对象, fontID 字体标识, face 实际字体, transforms 字形映射, clip 裁剪路径, height 页面高度, matrix 对象变换, glyphMatrix 字形变换
// 返回: bool 是否能完整保留字形外观
func (r *Renderer) pdfTextSupported(object TextObject, fontID string, face *canvas.FontFace, transforms map[int]textGlyphTransform, clip *canvas.Path, height float64, matrix Matrix, glyphMatrix canvas.Matrix) bool {
	if face.Font.IsCFF && face.Font.CFF == nil {
		return false
	}
	var rect canvas.Rect
	if clip != nil {
		var ok bool
		rect, ok = rectangularPath(clip)
		if !ok {
			return false
		}
		const tolerance = 1e-9
		rect = canvas.Rect{X0: rect.X0 - tolerance, Y0: rect.Y0 - tolerance, X1: rect.X1 + tolerance, Y1: rect.Y1 + tolerance}
	}
	scale := object.HScale
	if scale == 0 {
		scale = 1
	}
	position := 0
	for _, code := range object.TextCode {
		runes := textCodeRunes(code.Value)
		var glyphs []textGlyph
		if code.Index != "" {
			glyphs = r.parseIndexGlyphs(code.Index, fontID)
		} else {
			glyphs = textCodeGlyphs(runes, transforms, position)
		}
		positioner := NewTextPositioner(code, object.ReadDirection)
		for _, glyph := range glyphs {
			if glyph.GlyphID == 0 || glyph.GlyphID >= int(face.Font.NumGlyphs()) {
				return false
			}
			if glyph.GlyphID < 0 {
				for _, char := range glyph.Text {
					if face.Font.GlyphIndex(char) == 0 {
						return false
					}
				}
			}
			origin := positioner.Next()
			if clip == nil {
				continue
			}
			path, _ := r.cachedTextGlyphPath(face, glyph)
			if path == nil || r.renderError != nil {
				return false
			}
			if path.Empty() {
				continue
			}
			x, y := matrix.Transform(origin.X, origin.Y)
			view := canvas.Identity.Translate(x, height-y).Mul(glyphMatrix).Scale(scale, 1)
			if !rect.Contains(path.Copy().Transform(view).Bounds()) {
				return false
			}
		}
		position += len(runes)
	}
	return true
}

// pdfActualText 提取PDF可复制原文，对TextCode.Index优先保留显式文本或通过cmap反查Unicode
// 入参: object 文字对象, fontID 字体标识, face 字体
// 返回: string PDF ActualText原文
func (r *Renderer) pdfActualText(object TextObject, fontID string, face *canvas.FontFace) string {
	hasIndex := false
	for _, code := range object.TextCode {
		if code.Index != "" {
			hasIndex = true
			break
		}
	}
	if !hasIndex {
		return object.Text()
	}
	var reverse map[uint16]rune
	lines := newTextLineScanner(object)
	var text strings.Builder
	for _, code := range object.TextCode {
		if lines.next(code) {
			text.WriteByte('\n')
		}
		if code.Index == "" {
			text.WriteString(string(textCodeRunes(code.Value)))
			continue
		}
		if runes := textCodeRunes(code.Value); len(runes) > 0 {
			text.WriteString(string(runes))
			continue
		}
		if reverse == nil {
			reverse = buildFontReverseCmap(face.Font.Tables["cmap"])
		}
		glyphs := r.parseIndexGlyphs(code.Index, fontID)
		mapped := false
		var segment strings.Builder
		for _, glyph := range glyphs {
			if glyph.Text != "" {
				segment.WriteString(glyph.Text)
				mapped = true
			} else if glyph.GlyphID > 0 && glyph.GlyphID <= 0xFFFF {
				if char, ok := reverse[uint16(glyph.GlyphID)]; ok {
					segment.WriteRune(char)
					mapped = true
				}
			}
		}
		if mapped {
			text.WriteString(segment.String())
		} else {
			text.WriteRune('\uFFFC')
		}
	}
	return text.String()
}

// buildFontReverseCmap 从OpenType cmap表构建GID到Unicode码点的反查表
// 入参: cmap cmap表字节
// 返回: map[uint16]rune 字形编号到Unicode字符的映射
func buildFontReverseCmap(cmap []byte) map[uint16]rune {
	result := make(map[uint16]rune)
	if len(cmap) < 4 {
		return result
	}
	numTables := int(binary.BigEndian.Uint16(cmap[2:4]))
	if 4+numTables*8 > len(cmap) {
		return result
	}
	for i := range numTables {
		rec := cmap[4+i*8:]
		platform := binary.BigEndian.Uint16(rec[0:2])
		encoding := binary.BigEndian.Uint16(rec[2:4])
		offset := int(binary.BigEndian.Uint32(rec[4:8]))
		if !(platform == 0 || platform == 3 && (encoding == 1 || encoding == 10)) {
			continue
		}
		if offset < 0 || offset+4 > len(cmap) {
			continue
		}
		sub := cmap[offset:]
		format := binary.BigEndian.Uint16(sub[0:2])
		switch format {
		case 4:
			if len(sub) < 14 {
				continue
			}
			segCount := int(binary.BigEndian.Uint16(sub[6:8])) / 2
			endOff := 14
			startOff := endOff + segCount*2 + 2
			deltaOff := startOff + segCount*2
			rangeOff := deltaOff + segCount*2
			if rangeOff+segCount*2 > len(sub) {
				continue
			}
			for s := range segCount {
				endCode := uint32(binary.BigEndian.Uint16(sub[endOff+s*2:]))
				startCode := uint32(binary.BigEndian.Uint16(sub[startOff+s*2:]))
				if startCode == 0xFFFF && endCode == 0xFFFF {
					break
				}
				idDelta := int32(int16(binary.BigEndian.Uint16(sub[deltaOff+s*2:])))
				idRangeOffset := int(binary.BigEndian.Uint16(sub[rangeOff+s*2:]))
				for code := startCode; code <= endCode; code++ {
					var gid uint16
					if idRangeOffset == 0 {
						gid = uint16((int32(code) + idDelta) & 0xFFFF)
					} else {
						pos := rangeOff + s*2 + idRangeOffset + int(code-startCode)*2
						if pos+2 > len(sub) {
							continue
						}
						raw := binary.BigEndian.Uint16(sub[pos:])
						if raw != 0 {
							gid = uint16((int32(raw) + idDelta) & 0xFFFF)
						}
					}
					if gid != 0 && utf8.ValidRune(rune(code)) {
						if _, exists := result[gid]; !exists {
							result[gid] = rune(code)
						}
					}
				}
			}
		case 12:
			if len(sub) < 16 {
				continue
			}
			numGroups := int(binary.BigEndian.Uint32(sub[12:16]))
			if 16+numGroups*12 > len(sub) {
				continue
			}
			for g := range numGroups {
				group := sub[16+g*12:]
				startChar := binary.BigEndian.Uint32(group[0:4])
				endChar := binary.BigEndian.Uint32(group[4:8])
				startGID := binary.BigEndian.Uint32(group[8:12])
				if endChar < startChar || startChar > utf8.MaxRune {
					continue
				}
				limit := min(endChar, uint32(utf8.MaxRune))
				if limit-startChar > 65535 {
					limit = startChar + 65535
				}
				for code := startChar; code <= limit; code++ {
					gid := startGID + (code - startChar)
					if gid > 0 && gid <= 0xFFFF && utf8.ValidRune(rune(code)) {
						if _, exists := result[uint16(gid)]; !exists {
							result[uint16(gid)] = rune(code)
						}
					}
				}
			}
		}
	}
	return result
}

// pdfOpaqueSolidColor 提取用于Tr=2伪粗体描边的不透明纯色，非纯色或半透明返回false
// 入参: paint 填充画刷
// 返回: color.RGBA 填充颜色, bool 是否为不透明纯色
func pdfOpaqueSolidColor(paint any) (color.RGBA, bool) {
	c, ok := paint.(color.Color)
	if !ok || c == nil {
		return color.RGBA{}, false
	}
	r, g, b, a := c.RGBA()
	if a != 0xFFFF {
		return color.RGBA{}, false
	}
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}, true
}

// pdfSingleGlyphID 解析可直接合并为批量PDF字形输出的唯一GID
// 入参: face 字体, glyph 绘制字形
// 返回: uint16 字形编号, bool 是否可作为单字形批量输出
func pdfSingleGlyphID(face *canvas.FontFace, glyph textGlyph) (uint16, bool) {
	if glyph.GlyphID > 0 && glyph.GlyphID <= 0xFFFF {
		return uint16(glyph.GlyphID), true
	}
	if glyph.GlyphID < 0 {
		runes := []rune(glyph.Text)
		if len(runes) == 1 {
			if id := face.Font.GlyphIndex(runes[0]); id != 0 {
				return id, true
			}
		}
	}
	return 0, false
}
