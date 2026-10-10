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
	"github.com/tdewolff/canvas"
	canvastext "github.com/tdewolff/canvas/text"
	"github.com/xiaoqidun/pdfgo"
)

// RenderText 记录后端文字区段，写出时校验数量并关联原文
// 入参: text 画布文字, matrix 文字变换
func (r *pdfRenderer) RenderText(text *canvas.Text, matrix canvas.Matrix) {
	if len(r.text) == 0 {
		r.text = append(r.text, pdfPageText{})
	}
	page := &r.text[len(r.text)-1]
	text.WalkSpans(func(_, _ float64, span canvas.TextSpan) {
		if span.IsText() {
			page.count++
		}
	})
	r.PDF.RenderText(text, matrix)
}

// preserveText 将一个OFD文字对象关联到完整PDF文字区段
// 入参: first 起始文字对象序号, text 原文
func (r *pdfRenderer) preserveText(first int, text string) {
	page := &r.text[len(r.text)-1]
	if page.count > first {
		page.replacements = append(page.replacements, pdfgo.TextReplacement{First: first, Count: page.count - first, Text: text})
		r.exactText = true
	}
}

// glyphText 使用独立输出字体和既定字形建立PDF文字，不改变字体缓存及定位
// 入参: face 字体, glyph 字形
// 返回: *canvas.Text 画布文字, error 字体解析错误
func (r *pdfRenderer) glyphText(face *canvas.FontFace, glyph textGlyph) (*canvas.Text, error) {
	font := r.fonts[face.Font]
	if font == nil {
		loaded, err := loadCanvasFont(fontSFNTData(face.Font.Tables), 0, face.Font.Style())
		if err != nil {
			return nil, err
		}
		copy := *face.Font
		copy.SFNT = loaded.SFNT
		font = &copy
		if r.fonts == nil {
			r.fonts = make(map[*canvas.Font]*canvas.Font)
		}
		r.fonts[face.Font] = font
	}
	outputFace := *face
	outputFace.Font = font
	face = &outputFace
	if glyph.GlyphID < 0 || glyph.GlyphID > 0xffff {
		return canvas.NewTextLine(face, glyph.Text, canvas.Left), nil
	}
	id := uint16(glyph.GlyphID)
	text := canvas.NewTextLine(face, " ", canvas.Left)
	text.WalkLines(func(_ float64, spans []canvas.TextSpan) {
		for i := range spans {
			spans[i].Text = glyph.Text
			spans[i].Glyphs = []canvastext.Glyph{{SFNT: face.Font.SFNT, Size: face.Size, ID: id, XAdvance: int32(face.Font.GlyphAdvance(id)), Text: glyph.Text}}
		}
	})
	return text, nil
}

// pdfTextSupported 核对原生文字的字形和未裁剪范围，复用实际绘制的字形缓存
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
		glyphs := textCodeGlyphs(runes, transforms, position)
		if code.Index != "" {
			runes = r.parseIndexRunes(code.Index, fontID)
			glyphs = textRuneGlyphs(runes)
		}
		positioner := NewTextPositioner(code)
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
