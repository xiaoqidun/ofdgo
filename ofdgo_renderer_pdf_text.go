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
	"image/color"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/tdewolff/canvas"
	canvastext "github.com/tdewolff/canvas/text"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
)

// pdfGlyphItem 保存同基线批量输出的单个字形及局部横坐标
type pdfGlyphItem struct {
	glyph textGlyph
	id    uint16
	x     float64
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

// pdfSolidColor 提取任意透明度纯色的预乘RGBA值，非纯色返回false
// 入参: paint 绘制画刷
// 返回: color.RGBA 预乘颜色, bool 是否为纯色
func pdfSolidColor(paint any) (color.RGBA, bool) {
	c, ok := paint.(color.Color)
	if !ok || c == nil {
		return color.RGBA{}, false
	}
	return color.RGBAModel.Convert(c).(color.RGBA), true
}

// pdfSingleGlyphID 解析可直接合并为批量PDF字形输出的唯一GID
// 入参: face 字体, glyph 绘制字形
// 返回: uint16 字形编号, bool 是否可作为单字形批量输出
func (r *Renderer) pdfSingleGlyphID(face *canvas.FontFace, glyph textGlyph) (uint16, bool) {
	if glyph.GlyphID > 0 && glyph.GlyphID <= 0xFFFF {
		return uint16(glyph.GlyphID), true
	}
	if glyph.GlyphID < 0 {
		runes := []rune(glyph.Text)
		if len(runes) == 1 {
			if id := r.canvasGlyphIndex(face.Font, runes[0]); id != 0 {
				return id, true
			}
		}
	}
	return 0, false
}

// RenderText 向单遍内容流写入文字并同步span计数以供原地包裹ActualText、伪粗体与矢量裁剪
// 入参: text 画布文字, matrix 文字变换
func (r *pdfRenderer) RenderText(text *canvas.Text, matrix canvas.Matrix) {
	if r.imageError != nil || r.textClipEmpty {
		return
	}
	if len(r.text) == 0 {
		r.text = append(r.text, codecpdf.TextPage{})
	}
	r.content.RenderText(text, matrix, func(c *canvas.Canvas, view canvas.Matrix) {
		c.RenderViewTo(r, view)
	})
	r.text[len(r.text)-1].Count = r.content.SpanCount()
}

// preserveText 在当前文字对象结束时原地包裹ActualText标记，无需二次解析内容流
// 入参: first 起始文字span序号, text 原文
func (r *pdfRenderer) preserveText(first int, text string) {
	if r.textClipEmpty || r.content.SpanCount() <= first {
		return
	}
	r.content.WrapActualText(first, text)
}

// preserveFauxBold 在当前文字对象结束时原地包裹Tr=2伪粗体描边状态
// 入参: first 起始文字span序号, lineWidth 描边线宽(毫米), fillColor 填充纯色
func (r *pdfRenderer) preserveFauxBold(first int, lineWidth float64, fillColor color.RGBA) {
	if r.textClipEmpty || lineWidth <= 0 || r.content.SpanCount() <= first {
		return
	}
	r.content.WrapFauxBold(first, lineWidth, fillColor)
}

// preserveTextStyle 在当前文字对象结束时原地包裹原生PDF文本渲染模式与描边样式
// 入参: first 起始文字span序号, style 文本渲染模式与描边样式
func (r *pdfRenderer) preserveTextStyle(first int, style codecpdf.TextStrokeStyle) {
	if r.textClipEmpty || r.content.SpanCount() <= first {
		return
	}
	r.content.WrapTextStyle(first, style)
}

// preserveRenderMode 在当前文字对象结束时原地包裹指定PDF文本渲染模式
// 入参: first 起始文字span序号, mode 文本渲染模式编号
func (r *pdfRenderer) preserveRenderMode(first int, mode int) {
	if r.textClipEmpty || r.content.SpanCount() <= first {
		return
	}
	r.content.WrapRenderMode(first, mode)
}

// setPendingTextClip 记录即将绘制的文字对象原生PDF裁剪路径并隔离前驱BT块
// 入参: clip 页面坐标系裁剪路径
func (r *pdfRenderer) setPendingTextClip(clip *canvas.Path) {
	r.content.IsolateNextText()
	r.textClip = clip
	r.textClipFirst = r.content.SpanCount()
	r.textClipEmpty = false
}

// finishTextObject 在文字对象渲染完毕后原地应用原生PDF矢量裁剪并清理隔离状态
func (r *pdfRenderer) finishTextObject() {
	if r.textClip != nil && !r.textClipEmpty && r.content.SpanCount() > r.textClipFirst {
		r.content.WrapClip(r.textClipFirst, r.textClip)
	}
	r.textClip = nil
	r.textClipEmpty = false
	r.content.ClearTextIsolation()
}

// glyphText 使用共享输出字体和既定字形建立PDF文字，不改变字体缓存及定位
// 入参: face 字体, glyph 字形
// 返回: *canvas.Text 画布文字, error 字体解析错误
func (r *pdfRenderer) glyphText(face *canvas.FontFace, glyph textGlyph) (*canvas.Text, error) {
	outputFace := *face
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
	return text, nil
}

// glyphsText 将同基线连续字形合并为单次PDF文字绘制，以累计字体单位步进消除漂移
// 入参: face 字体, items 连续字形序列, hScale 水平缩放比例
// 返回: *canvas.Text 画布文字, error 字体解析错误
func (r *pdfRenderer) glyphsText(face *canvas.FontFace, items []pdfGlyphItem, hScale float64) (*canvas.Text, error) {
	if len(items) == 1 {
		return r.glyphText(face, items[0].glyph)
	}
	outputFace := *face
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
	return text, nil
}

// pdfFontGlyphsSupported 检查文本对象的全部字形是否均可使用原字体作为PDF子集字形写出
// 入参: object 文字对象, fontID 字体标识, face 实际字体, transforms 字形映射
// 返回: bool 原字体是否支持全部字形原生PDF输出
func (r *Renderer) pdfFontGlyphsSupported(object TextObject, fontID string, face *canvas.FontFace, transforms map[int]textGlyphTransform) bool {
	if face == nil || face.Font == nil || face.Font.IsCFF && face.Font.CFF == nil {
		return false
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
		for _, glyph := range glyphs {
			if glyph.GlyphID == 0 || glyph.GlyphID >= int(face.Font.NumGlyphs()) {
				return false
			}
			if glyph.GlyphID < 0 {
				for _, char := range glyph.Text {
					if r.canvasGlyphIndex(face.Font, char) == 0 {
						return false
					}
				}
			}
		}
		position += len(runes)
	}
	return true
}

// pdfTextSupported 核对原生文字的字形合法性，并通过原生PDF矢量裁剪保留带边界裁剪的真文字
// 入参: object 文字对象, fontID 字体标识, face 实际字体, transforms 字形映射, clip 裁剪路径, height 页面高度, matrix 对象变换, glyphMatrix 字形变换, stroked 是否包含描边或伪粗体
// 返回: bool 是否能以PDF原生文字完整保留外观
func (r *Renderer) pdfTextSupported(object TextObject, fontID string, face *canvas.FontFace, transforms map[int]textGlyphTransform, clip *canvas.Path, height float64, matrix Matrix, glyphMatrix canvas.Matrix, stroked bool) bool {
	if face.Font.IsCFF && face.Font.CFF == nil {
		return false
	}
	var pdfOut *pdfRenderer
	if r.pageContext != nil {
		pdfOut, _ = r.pageContext.Value(pdfRendererCtxKey{}).(*pdfRenderer)
	}
	if clip != nil && clip.Empty() {
		if pdfOut != nil {
			pdfOut.textClipEmpty = true
			return true
		}
		return false
	}
	var rect canvas.Rect
	isRectClip := false
	if clip != nil {
		rect, isRectClip = rectangularPath(clip)
		if isRectClip {
			const tolerance = 1e-9
			rect = canvas.Rect{X0: rect.X0 - tolerance, Y0: rect.Y0 - tolerance, X1: rect.X1 + tolerance, Y1: rect.Y1 + tolerance}
		} else if pdfOut == nil {
			return false
		}
	}
	scale := object.HScale
	if scale == 0 {
		scale = 1
	}
	needsNativeClip := clip != nil && (!isRectClip || stroked)
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
					if r.canvasGlyphIndex(face.Font, char) == 0 {
						return false
					}
				}
			}
			origin := positioner.Next()
			if clip == nil || needsNativeClip {
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
				if pdfOut == nil {
					return false
				}
				needsNativeClip = true
			}
		}
		position += len(runes)
	}
	if pdfOut != nil {
		if needsNativeClip {
			pdfOut.setPendingTextClip(clip)
		} else if stroked || face.FauxBold > 0 {
			pdfOut.content.IsolateNextText()
		}
	}
	return true
}

// glyphToUnicodeRune 缓存字体字形编号在PDF ToUnicode表中的首字符映射
// 入参: renderer 渲染器, font 画布字体, gid 字形编号
// 返回: rune 映射码点, bool 是否存在有效映射
func (p *pdfRenderer) glyphToUnicodeRune(renderer *Renderer, font *canvas.Font, gid uint16) (rune, bool) {
	if font == nil || font.SFNT == nil {
		return 0, false
	}
	table := p.glyphUnicode[font]
	if table != nil {
		if ch, ok := table[gid]; ok {
			return ch, ch >= 0
		}
	} else {
		if p.glyphUnicode == nil {
			p.glyphUnicode = make(map[*canvas.Font]map[uint16]rune)
		}
		table = make(map[uint16]rune)
		p.glyphUnicode[font] = table
	}
	if ch, ok := renderer.canvasGlyphToUnicode(font, gid); ok {
		table[gid] = ch
		return ch, true
	}
	table[gid] = -1
	return 0, false
}

// pdfTextNeedsActualText 判断原生PDF文字是否无法仅靠ToUnicode精确还原原文而需要注入ActualText
// 入参: pdfOutput PDF渲染器, object 文字对象, fontID 字体标识, face 实际字体, transforms 字形映射
// 返回: bool 是否需要注入ActualText
func (r *Renderer) pdfTextNeedsActualText(pdfOutput *pdfRenderer, object TextObject, fontID string, face *canvas.FontFace, transforms map[int]textGlyphTransform) bool {
	needs := r.checkPDFTextNeedsActualText(pdfOutput, object, fontID, face, transforms)
	if needs && pdfOutput != nil && !pdfOutput.textClipEmpty {
		pdfOutput.content.IsolateNextText()
	}
	return needs
}

// checkPDFTextNeedsActualText 逐字检查字体ToUnicode映射能否无损还原OFD原文
// 入参: pdfOutput PDF渲染器, object 文字对象, fontID 字体标识, face 实际字体, transforms 字形映射
// 返回: bool 是否需要注入ActualText
func (r *Renderer) checkPDFTextNeedsActualText(pdfOutput *pdfRenderer, object TextObject, fontID string, face *canvas.FontFace, transforms map[int]textGlyphTransform) bool {
	if object.ReadDirection != 0 || object.CharDirection != 0 {
		return true
	}
	lines := newTextLineScanner(object)
	codePos := 0
	for _, tc := range object.TextCode {
		if lines.next(tc) {
			return true
		}
		runes := textCodeRunes(tc.Value)
		if tc.Index != "" {
			glyphs := r.parseIndexGlyphs(tc.Index, fontID)
			if len(runes) > 0 && len(runes) != len(glyphs) {
				return true
			}
			for i, glyph := range glyphs {
				gid, ok := r.pdfSingleGlyphID(face, glyph)
				if !ok {
					return true
				}
				mapped, has := pdfOutput.glyphToUnicodeRune(r, face.Font, gid)
				if !has || mapped > 0xFFFF {
					return true
				}
				if len(runes) == len(glyphs) {
					if mapped != runes[i] {
						return true
					}
					continue
				}
				if glyph.Text != "" {
					ch, size := utf8.DecodeRuneInString(glyph.Text)
					if size != len(glyph.Text) || ch != mapped {
						return true
					}
				}
			}
			codePos += len(runes)
			continue
		}
		if len(transforms) == 0 {
			for _, ch := range runes {
				if ch > 0xFFFF {
					return true
				}
				gid := r.canvasGlyphIndex(face.Font, ch)
				if gid == 0 {
					return true
				}
				mapped, has := pdfOutput.glyphToUnicodeRune(r, face.Font, gid)
				if !has || mapped != ch {
					return true
				}
			}
			codePos += len(runes)
			continue
		}
		glyphs := textCodeGlyphs(runes, transforms, codePos)
		if len(glyphs) != len(runes) {
			return true
		}
		for i, glyph := range glyphs {
			if runes[i] > 0xFFFF {
				return true
			}
			gid, ok := r.pdfSingleGlyphID(face, glyph)
			if !ok {
				return true
			}
			mapped, has := pdfOutput.glyphToUnicodeRune(r, face.Font, gid)
			if !has || mapped != runes[i] {
				return true
			}
		}
		codePos += len(runes)
	}
	return false
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
		if reverse == nil && face != nil && face.Font != nil {
			reverse = basefont.ReverseCmap(face.Font.Tables["cmap"])
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

// emitPDFInvisibleText 为已转矢量路径绘制的文字叠加3 Tr不可见文字层以保障PDF文本可复制
// 入参: ctx 画布上下文, pdfOutput PDF渲染器, obj 有效文本对象, fontID 字体标识, face 字体, transforms 字形映射, pageH 页面高度, bx 边界X, by 边界Y, ctm 合并变换, localCTM 局部变换, parentCTM 父变换, boundaryInCTM 边界是否参与父变换, hScale 水平缩放, useTextMatrix 是否使用文字矩阵, glyphMatrix 字形变换矩阵, clipPath 裁剪路径
func (r *Renderer) emitPDFInvisibleText(ctx *canvas.Context, pdfOutput *pdfRenderer, obj TextObject, fontID string, face *canvas.FontFace, transforms map[int]textGlyphTransform, pageH, bx, by float64, ctm, localCTM Matrix, parentCTM *Matrix, boundaryInCTM bool, hScale float64, useTextMatrix bool, glyphMatrix canvas.Matrix, clipPath *canvas.Path) {
	actualText := r.pdfActualText(obj, fontID, face)
	if actualText == "" {
		return
	}
	if r.pdfFontGlyphsSupported(obj, fontID, face, transforms) {
		if len(pdfOutput.text) == 0 {
			pdfOutput.text = append(pdfOutput.text, codecpdf.TextPage{})
		}
		pdfOutput.content.IsolateNextText()
		firstBT := pdfOutput.content.SpanCount()
		var batch []pdfGlyphItem
		var batchIDs map[uint16]bool
		var batchY, batchCanvasX, batchCanvasY float64
		flushBatch := func() {
			if len(batch) == 0 {
				return
			}
			ctx.SetFillColor(canvas.Black)
			ctx.SetStrokeColor(canvas.Transparent)
			text, err := pdfOutput.glyphsText(face, batch, hScale)
			batch = batch[:0]
			clear(batchIDs)
			if err != nil {
				r.renderError = err
				return
			}
			needTransform := useTextMatrix || hScale != 1 || face.FauxItalic != 0
			if needTransform {
				ctx.Push()
				ctx.Translate(batchCanvasX, batchCanvasY)
				if useTextMatrix {
					ctx.ComposeView(glyphMatrix)
				}
				if hScale != 1 {
					ctx.Scale(hScale, 1)
				}
				if face.FauxItalic != 0 {
					ctx.ComposeView(canvas.Identity.Shear(face.FauxItalic, 0))
				}
				ctx.DrawText(0, 0, text)
				ctx.Pop()
			} else {
				ctx.DrawText(batchCanvasX, batchCanvasY, text)
			}
		}
		codePos := 0
		for _, tc := range obj.TextCode {
			runes := textCodeRunes(tc.Value)
			var glyphs []textGlyph
			if tc.Index != "" {
				glyphs = r.parseIndexGlyphs(tc.Index, fontID)
			} else {
				glyphs = textCodeGlyphs(runes, transforms, codePos)
			}
			positioner := NewTextPositioner(tc, obj.ReadDirection)
			for i, glyph := range glyphs {
				if tc.Index != "" && glyph.Text == "" && len(runes) == len(glyphs) {
					glyph.Text = string(runes[i])
				}
				pos := positioner.Next()
				cx, cy := pos.X, pos.Y
				var canvasX, canvasY float64
				if boundaryInCTM && parentCTM != nil {
					tx, ty := localCTM.Transform(cx, cy)
					tx, ty = parentCTM.Transform(tx+bx, ty+by)
					canvasX, canvasY = tx, pageH-ty
				} else {
					tx, ty := ctm.Transform(cx, cy)
					canvasX, canvasY = tx+bx, pageH-(ty+by)
				}
				if obj.CharDirection == 0 && face.MmPerEm > 0 && hScale != 0 {
					if gid, ok := r.pdfSingleGlyphID(face, glyph); ok {
						canJoin := len(batch) == 0
						if !canJoin && math.Abs(cy-batchY) <= 1e-9 {
							unitScale := hScale * face.MmPerEm
							delta := cx - batch[0].x
							target := math.Round(delta / unitScale)
							cffOK := !face.Font.IsCFF || batchIDs[gid] || len(batchIDs) < 228
							canJoin = cffOK && math.Abs(target) < 1e7 && math.Abs(target*unitScale-delta) <= 1e-3
						}
						if !canJoin {
							flushBatch()
							if r.renderError != nil {
								pdfOutput.content.ClearTextIsolation()
								return
							}
						}
						if len(batch) == 0 {
							batchY, batchCanvasX, batchCanvasY = cy, canvasX, canvasY
						}
						if face.Font.IsCFF {
							if batchIDs == nil {
								batchIDs = make(map[uint16]bool)
							}
							batchIDs[gid] = true
						}
						batch = append(batch, pdfGlyphItem{glyph: glyph, id: gid, x: cx})
						continue
					}
					flushBatch()
					if r.renderError != nil {
						pdfOutput.content.ClearTextIsolation()
						return
					}
				}
				ctx.SetFillColor(canvas.Black)
				ctx.SetStrokeColor(canvas.Transparent)
				text, err := pdfOutput.glyphText(face, glyph)
				if err != nil {
					r.renderError = err
					pdfOutput.content.ClearTextIsolation()
					return
				}
				if useTextMatrix || hScale != 1 || face.FauxItalic != 0 {
					ctx.Push()
					ctx.Translate(canvasX, canvasY)
					if useTextMatrix {
						ctx.ComposeView(glyphMatrix)
					}
					if hScale != 1 {
						ctx.Scale(hScale, 1)
					}
					if face.FauxItalic != 0 {
						ctx.ComposeView(canvas.Identity.Shear(face.FauxItalic, 0))
					}
					ctx.DrawText(0, 0, text)
					ctx.Pop()
				} else {
					ctx.DrawText(canvasX, canvasY, text)
				}
			}
			flushBatch()
			if r.renderError != nil {
				pdfOutput.content.ClearTextIsolation()
				return
			}
			codePos += len(runes)
		}
		if pdfOutput.content.SpanCount() > firstBT {
			pdfOutput.preserveRenderMode(firstBT, 3)
			if r.checkPDFTextNeedsActualText(pdfOutput, obj, fontID, face, transforms) {
				pdfOutput.preserveText(firstBT, actualText)
			}
			if clipPath != nil {
				pdfOutput.content.WrapClip(firstBT, clipPath.Copy().Transform(ctx.CoordSystemView().Mul(ctx.View())))
			}
		}
		pdfOutput.content.ClearTextIsolation()
		return
	}
	cx, cy := 0.0, 0.0
	if len(obj.TextCode) > 0 {
		positioner := NewTextPositioner(obj.TextCode[0], obj.ReadDirection)
		pos := positioner.Next()
		cx, cy = pos.X, pos.Y
	}
	var canvasX, canvasY float64
	if boundaryInCTM && parentCTM != nil {
		tx, ty := localCTM.Transform(cx, cy)
		tx, ty = parentCTM.Transform(tx+bx, ty+by)
		canvasX, canvasY = tx, pageH-ty
	} else {
		tx, ty := ctm.Transform(cx, cy)
		canvasX, canvasY = tx+bx, pageH-(ty+by)
	}
	m := canvas.Identity.Translate(canvasX, canvasY)
	if useTextMatrix {
		m = m.Mul(glyphMatrix)
	}
	if hScale != 1 {
		m = m.Scale(hScale, 1)
	}
	fontSize := obj.Size
	if face != nil {
		if face.FauxItalic != 0 {
			m = m.Shear(face.FauxItalic, 0)
		}
		if face.Size > 0 {
			fontSize = face.Size
		}
	}
	view := ctx.CoordSystemView().Mul(ctx.View())
	var pdfClip *canvas.Path
	if clipPath != nil {
		pdfClip = clipPath.Copy().Transform(view)
	}
	pdfOutput.content.WriteInvisibleActualText(view.Mul(m), fontSize, actualText, pdfClip)
}
