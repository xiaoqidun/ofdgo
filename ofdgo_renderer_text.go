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
	"strings"

	"github.com/tdewolff/canvas"
)

const ptPerMM = 72.0 / 25.4

// renderText 渲染文本
// 入参: ctx 画布上下文, obj 文本对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM, parentClip 父级裁剪路径
func (r *Renderer) renderText(ctx *canvas.Context, obj TextObject, pageH float64, defaultFill, defaultStroke color.Color, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	ctx.Push()
	bx, by := 0.0, 0.0
	if obj.Boundary != "" {
		if box, err := ParseBox(obj.Boundary); err == nil {
			bx, by = box.X, box.Y
		}
	}
	localCTM := NewMatrix(obj.CTM)
	ctm := localCTM
	if parentCTM != nil {
		ctm = parentCTM.Multiply(ctm)
	}
	clipPath := intersectClipPath(parentClip, r.buildClipPath(obj.Clips, pageH, bx, by, ctm))
	var dp *DrawParam
	if obj.DrawParam != "" {
		dp = r.getDrawParam(obj.DrawParam, nil)
	}
	sizeMM := obj.Size
	if sizeMM == 0 && dp != nil && dp.Size > 0 {
		sizeMM = dp.Size
	}
	if sizeMM == 0 {
		sizeMM = 3.5
	}
	if obj.VScale != 0 {
		sizeMM *= obj.VScale
	}
	hScale := obj.HScale
	if hScale == 0 {
		hScale = 1
	}
	useTextMatrix := hasTextMatrix(ctm)
	if scale := ctm.YScale(); scale > 0 && !useTextMatrix {
		sizeMM *= scale
	}
	sizePt := sizeMM * ptPerMM
	fillColor := colorWithAlpha(defaultFill, obj.Alpha)
	if fillColor == nil {
		fillColor = colorWithAlpha(canvas.Black, obj.Alpha)
	}
	var fillPaint any = fillColor
	var fillColorNode *FillColor
	if dp != nil && dp.FillColor != nil {
		fillColorNode = withFillAlpha(dp.FillColor, obj.Alpha)
		fillColor = parseFillColor(fillColorNode)
		fillPaint = parseFillPaint(fillColorNode, bx, by, pageH, 0, 0)
	}
	if obj.FillColor != nil {
		fillColorNode = withFillAlpha(obj.FillColor, obj.Alpha)
		fillColor = parseFillColor(fillColorNode)
		fillPaint = parseFillPaint(fillColorNode, bx, by, pageH, 0, 0)
	}
	if fillPaint == nil {
		fillPaint = fillColor
	}
	fontStyle := canvas.FontRegular
	weight := obj.Weight
	if weight == 0 && dp != nil && dp.Weight > 0 {
		weight = dp.Weight
	}
	syntheticBold := weight >= 700
	italic := obj.Italic
	if !italic && dp != nil && dp.Italic {
		italic = true
	}
	if italic {
		fontStyle |= canvas.FontItalic
	}
	fontID := r.textObjectFontID(obj)
	embeddedFont := false
	if of, ok := r.Reader.fontCache[fontID]; ok {
		embeddedFont = of.FontFile != ""
		if !embeddedFont && fontNoSyntheticBold(of.FontName, of.FamilyName) {
			syntheticBold = false
		}
		if of.Bold {
			fontStyle |= canvas.FontBold
		}
		if of.Italic {
			fontStyle |= canvas.FontItalic
		}
	}
	if syntheticBold {
		fontStyle |= canvas.FontBold
	}
	ff := r.loadFont(fontID)
	if ff == nil {
		ctx.Pop()
		return
	}
	face := ff.Face(sizePt, fillPaint, fontStyle, canvas.FontNormal)
	glyphTransforms := r.textObjectGlyphTransforms(fontID, obj)
	hasUnderline := strings.Contains(obj.Decoration, "Underline")
	useGlyphFillPaint := fillColorNode != nil && fillColorNode.AxialShd != nil
	codePos := 0
	for _, tc := range obj.TextCode {
		var runes []rune
		var glyphs []textGlyph
		if tc.Index != "" {
			runes = r.parseIndexRunes(tc.Index, fontID)
			glyphs = textRuneGlyphs(runes)
		} else {
			runes = textCodeRunes(tc.Value)
			glyphs = textCodeGlyphs(runes, glyphTransforms, codePos)
		}
		dxs, dys := parseFloats(tc.DeltaX), parseFloats(tc.DeltaY)
		xs, ys := parseFloats(tc.X), parseFloats(tc.Y)
		drawAsPath := embeddedFont || textCodePositioned(tc, xs, ys) || clipPath != nil
		cx, cy := 0.0, 0.0
		if len(xs) > 0 {
			cx = xs[0]
		}
		if len(ys) > 0 {
			cy = ys[0]
		}
		for i, glyph := range glyphs {
			str := glyph.Text
			drawAsGlyphPath := drawAsPath || glyph.GlyphID >= 0
			var glyphPath *canvas.Path
			var glyphWidth float64
			if drawAsGlyphPath {
				glyphPath, glyphWidth = r.cachedTextGlyphPath(face, glyph)
			} else {
				glyphWidth = textGlyphWidth(face, glyph)
			}
			if i < len(xs) {
				cx = xs[i]
			} else if i > 0 {
				if dx, ok := textDelta(dxs, i-1); ok {
					cx += dx
				} else if len(dys) == 0 {
					cx += glyphWidth * hScale
				}
			}
			if i < len(ys) {
				cy = ys[i]
			} else if i > 0 {
				if dy, ok := textDelta(dys, i-1); ok {
					cy += dy
				}
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
			textWidth := glyphWidth * hScale
			glyphFillPaint := fillPaint
			if useGlyphFillPaint {
				glyphFillPaint = parseFillPaint(fillColorNode, bx, by, pageH, canvasX, canvasY)
			}
			advanceLimit := textGlyphAdvanceLimit(dxs, dys, xs, i, len(glyphs), cx)
			if glyphFillPaint != nil {
				ctx.SetFill(glyphFillPaint)
				if clipPath != nil {
					scaleX := hScale
					if advanceLimit > 0 && glyphWidth*scaleX > advanceLimit {
						scaleX = advanceLimit / glyphWidth
					}
					textWidth = glyphWidth * scaleX
					textTransform := canvas.Identity.Translate(canvasX, canvasY)
					if useTextMatrix {
						textTransform = textTransform.Mul(textMatrix(ctm))
					}
					glyphPath = applyClipPath(glyphPath.Copy().Transform(textTransform.Scale(scaleX, 1)), clipPath)
					ctx.DrawPath(0, 0, glyphPath)
					if hasUnderline {
						uw := sizeMM * 0.05
						off := sizeMM * 0.1
						underline := &canvas.Path{}
						underline.MoveTo(0, -off)
						underline.LineTo(textWidth, -off)
						underline = underline.Stroke(uw, canvas.ButtCap, canvas.MiterJoin, canvas.Tolerance)
						underline = applyClipPath(underline.Transform(textTransform), clipPath)
						ctx.SetFillColor(fillColor)
						ctx.SetStrokeColor(canvas.Transparent)
						ctx.DrawPath(0, 0, underline)
					}
					continue
				}
				drawGlyph := func(x, y float64) {
					if drawAsGlyphPath {
						scaleX := hScale
						if advanceLimit > 0 && glyphWidth*scaleX > advanceLimit {
							scaleX = advanceLimit / glyphWidth
						}
						textWidth = glyphWidth * scaleX
						if scaleX != 1 {
							ctx.Push()
							ctx.Translate(x, y)
							ctx.Scale(scaleX, 1)
							ctx.DrawPath(0, 0, glyphPath)
							ctx.Pop()
						} else {
							ctx.DrawPath(x, y, glyphPath)
						}
					} else {
						scaled := hScale != 1
						if scaled {
							ctx.Push()
							ctx.Translate(x, y)
							ctx.Scale(hScale, 1)
							x, y = 0, 0
						}
						textFace := face
						if useGlyphFillPaint {
							textFace = ff.Face(sizePt, glyphFillPaint, fontStyle, canvas.FontNormal)
						}
						text := canvas.NewTextLine(textFace, str, canvas.Left)
						ctx.DrawText(x, y, text)
						if scaled {
							ctx.Pop()
						}
					}
				}
				if useTextMatrix {
					ctx.Push()
					ctx.Translate(canvasX, canvasY)
					ctx.ComposeView(textMatrix(ctm))
					drawGlyph(0, 0)
					if hasUnderline {
						uw := sizeMM * 0.05
						ctx.SetStrokeWidth(uw)
						ctx.SetStrokeColor(fillColor)
						off := sizeMM * 0.1
						ctx.MoveTo(0, -off)
						ctx.LineTo(textWidth, -off)
						ctx.Stroke()
					}
					ctx.Pop()
					continue
				}
				drawGlyph(canvasX, canvasY)
			}
			if hasUnderline {
				uw := sizeMM * 0.05
				ctx.SetStrokeWidth(uw)
				ctx.SetStrokeColor(fillColor)
				off := sizeMM * 0.1
				ctx.MoveTo(canvasX, canvasY-off)
				ctx.LineTo(canvasX+textWidth, canvasY-off)
				ctx.Stroke()
			}
		}
		codePos += len(runes)
	}
	ctx.Pop()
}
