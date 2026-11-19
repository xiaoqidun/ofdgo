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
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
)

// ptPerMM 为毫米转换为排版点的比例
const ptPerMM = 72.0 / 25.4

// renderText 渲染文本
// 入参: ctx 画布上下文, obj 文本对象, pageH 页面高度, defaults 默认绘制参数, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM, parentClip 父级裁剪路径
func (r *Renderer) renderText(ctx *canvas.Context, obj TextObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	shouldFill := obj.Fill == nil || *obj.Fill
	shouldStroke := obj.Stroke != nil && *obj.Stroke
	if !shouldFill && !shouldStroke {
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
	objectCTM := TranslationMatrix(bx, by).Multiply(ctm)
	if boundaryInCTM && parentCTM != nil {
		objectCTM = parentCTM.Multiply(TranslationMatrix(bx, by)).Multiply(localCTM)
	}
	patternOrigin := TranslationMatrix(bx, by)
	if parentCTM != nil {
		if boundaryInCTM {
			patternOrigin = parentCTM.Multiply(patternOrigin)
		} else {
			patternOrigin = patternOrigin.Multiply(*parentCTM)
		}
	}
	clipPath := r.intersectCachedClipPath(parentClip, r.buildObjectClipPath(obj.Clips, pageH, obj.Boundary, localCTM, parentCTM, boundaryInCTM))
	dp := r.drawParamDefaults(obj.DrawParam, defaults)
	sizeMM := obj.Size
	if sizeMM == 0 && dp != nil && dp.Size > 0 {
		sizeMM = dp.Size
	}
	if sizeMM == 0 {
		sizeMM = 3.5
	}
	effectiveObj := obj
	effectiveObj.Size = sizeMM
	hScale := obj.HScale
	if hScale == 0 {
		hScale = 1
	}
	useTextMatrix := hasTextMatrix(ctm) || obj.CharDirection != 0
	glyphMatrix := textMatrix(ctm).Rotate(-float64(obj.CharDirection))
	if scale := ctm.YScale(); scale > 0 && !useTextMatrix {
		sizeMM *= scale
	}
	sizePt := sizeMM * ptPerMM
	fillColor := colorWithAlpha(canvas.Black, obj.Alpha)
	var fillPaint any = fillColor
	var fillPattern *PatternPaint
	var fillColorNode *FillColor
	if defaults != nil {
		fillColorNode = defaults.FillColor
	}
	if dp != nil && dp.FillColor != nil {
		fillColorNode = dp.FillColor
	}
	if obj.FillColor != nil {
		fillColorNode = obj.FillColor
	}
	if fillColorNode != nil {
		fillColorNode = withFillAlpha(fillColorNode, obj.Alpha)
		fillColor = r.parseFillColor(fillColorNode)
		fillPaint = r.parseFillPaint(fillColorNode, bx, by, pageH)
		fillPattern = parsePatternPaint(fillColorNode)
	}
	if fillPaint == nil {
		fillPaint = fillColor
	}
	transformShdPaint(fillPaint, parentCTM, bx, by, pageH, boundaryInCTM)
	fillPaint, shadingClip, fillView := resolveShdPaint(ctx, fillPaint)
	fillClip := r.intersectCachedClipPath(clipPath, shadingClip)
	var strokeStyle pathStyle
	strokeClip := clipPath
	strokeView := canvas.Identity
	if shouldStroke {
		strokeStyle = r.newPathStyle(defaults, bx, by, pageH, obj.Alpha)
		if dp != nil {
			strokeStyle.applyDrawParam(r, dp, bx, by, pageH, obj.Alpha)
		}
		strokeObject := obj.pathStyle()
		strokeObject.FillColor = nil
		strokeStyle.applyPathObject(r, strokeObject, bx, by, pageH)
		if strokeStyle.strokePaint == nil {
			strokeStyle.strokePaint = canvas.Transparent
		}
		strokeStyle.scale(ctm)
		strokeStyle.lineWidth = r.deviceLineWidth(strokeStyle.lineWidth)
		transformShdPaint(strokeStyle.strokePaint, parentCTM, bx, by, pageH, boundaryInCTM)
		strokeStyle.strokePaint, shadingClip, strokeView = resolveShdPaint(ctx, strokeStyle.strokePaint)
		strokeClip = r.intersectCachedClipPath(strokeClip, shadingClip)
	}
	fontStyle := canvas.FontRegular
	weight := obj.Weight
	if weight == 0 && !obj.WeightSet && dp != nil && (dp.Weight > 0 || dp.WeightSet) {
		weight = dp.Weight
	}
	syntheticBold := weight >= 700
	italic := obj.Italic
	if !obj.ItalicSet && !italic && dp != nil && dp.Italic {
		italic = true
	}
	if italic {
		fontStyle |= canvas.FontItalic
	}
	fontID := r.textObjectFontID(obj)
	if fontID == "" && dp != nil && dp.Font != "" {
		fontID = dp.Font
	}
	embeddedFont := false
	if of := r.fontDefinition(fontID); of != nil {
		embeddedFont = of.FontFile != ""
		if syntheticBold && !embeddedFont && basefont.NoSyntheticBold(of.FontName, of.FamilyName) {
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
	var textRun *TextRun
	if r.pageText != nil {
		textRun = r.pageText.addRun(effectiveObj)
	}
	if ff == nil {
		if textRun != nil && !r.textContentOnly {
			box, _ := ParseBox(obj.Boundary)
			boundaryCTM := TranslationMatrix(box.X, box.Y)
			if parentCTM != nil {
				if boundaryInCTM {
					boundaryCTM = parentCTM.Multiply(boundaryCTM)
				} else {
					boundaryCTM = boundaryCTM.Multiply(*parentCTM)
				}
			}
			m := canvas.Matrix{{boundaryCTM.a, -boundaryCTM.c, boundaryCTM.e}, {-boundaryCTM.b, boundaryCTM.d, pageH - boundaryCTM.f}}
			path := canvas.Rectangle(box.W, box.H).Translate(0, -box.H).Transform(m)
			box = textPathBox(r.applyCachedClipPath(path, clipPath), pageH)
			for i := range textRun.Boxes {
				textRun.Boxes[i] = box
			}
		}
		ctx.Pop()
		return
	}
	face := ff.Face(sizePt, fillPaint, fontStyle, canvas.FontNormal)
	face.Hinting = 0
	recoverBaseline := len(obj.TextCode) > 0 && obj.TextCode[0].Y == ""
	var metrics canvas.FontMetrics
	if textRun != nil || recoverBaseline {
		metrics = face.Metrics()
	}
	ascent := metrics.Ascent
	if scale := ctm.YScale(); scale > 0 && !useTextMatrix {
		ascent /= scale
	}
	obj.TextCode = textCodeOrigins(obj.TextCode, ascent)
	effectiveObj.TextCode = obj.TextCode
	if textRun != nil && recoverBaseline {
		if text := effectiveObj.Text(); text != textRun.Text {
			textRun.Text = text
			textRun.Boxes = make([]Box, len([]rune(text)))
		}
	}
	if r.textOnly && r.textContentOnly {
		ctx.Pop()
		return
	}
	glyphTransforms := r.textObjectGlyphTransforms(fontID, obj)
	_, shadedFill := fillPaint.(canvas.Gradient)
	pdfOutput, _ := ctx.Renderer.(*pdfRenderer)
	pdfText := pdfOutput != nil && !r.textOnly && shouldFill && !shouldStroke && fillPattern == nil && !shadedFill && face.FauxBold == 0 && face.FauxItalic == 0
	if pdfText {
		view := canvas.Identity
		if useTextMatrix {
			view = glyphMatrix
		}
		pdfText = r.pdfTextSupported(obj, fontID, face, glyphTransforms, fillClip, pageH, objectCTM, view)
	}
	if pdfText {
		if len(pdfOutput.text) == 0 {
			pdfOutput.text = append(pdfOutput.text, codecpdf.TextPage{})
		}
		defer pdfOutput.preserveText(pdfOutput.text[len(pdfOutput.text)-1].Count, effectiveObj.Text())
	}
	codePos := 0
	textPos := 0
	var lines textLineScanner
	if textRun != nil {
		lines = newTextLineScanner(effectiveObj)
	}
	for _, tc := range obj.TextCode {
		if textRun != nil && lines.next(tc) {
			textPos++
		}
		runes := textCodeRunes(tc.Value)
		var glyphs []textGlyph
		if tc.Index != "" {
			glyphs = r.parseIndexGlyphs(tc.Index, fontID)
		} else {
			glyphs = textCodeGlyphs(runes, glyphTransforms, codePos)
		}
		var spans [][2]int
		if textRun != nil {
			if tc.Index == "" {
				spans = textGlyphSpans(runes, glyphTransforms, codePos)
			} else {
				spans = indexGlyphSpans(len(glyphs))
			}
		}
		positioner := NewTextPositioner(tc, obj.ReadDirection)
		positioned := textCodePositioned(tc, positioner.xs, positioner.ys) || obj.ReadDirection != 0
		drawAsPath := embeddedFont || face.FauxBold > 0 || positioned || fillClip != nil || shadedFill || fillPattern != nil || shouldStroke
		for i, glyph := range glyphs {
			if i%64 == 0 && r.pageContext != nil {
				if err := r.pageContext.Err(); err != nil {
					r.renderError = err
					ctx.Pop()
					return
				}
			}
			str := glyph.Text
			drawAsGlyphPath := drawAsPath || glyph.GlyphID >= 0
			if !drawAsGlyphPath {
				for _, char := range str {
					if face.Font.GlyphIndex(char) == 0 {
						drawAsGlyphPath = true
						break
					}
				}
			}
			var glyphPath *canvas.Path
			var glyphWidth float64
			if drawAsGlyphPath && !pdfText {
				glyphPath, glyphWidth = r.cachedTextGlyphPath(face, glyph)
			} else {
				glyphWidth = textGlyphWidth(face, glyph)
			}
			position := positioner.Next()
			cx, cy := position.X, position.Y
			var canvasX, canvasY float64
			if boundaryInCTM && parentCTM != nil {
				tx, ty := localCTM.Transform(cx, cy)
				tx, ty = parentCTM.Transform(tx+bx, ty+by)
				canvasX, canvasY = tx, pageH-ty
			} else {
				tx, ty := ctm.Transform(cx, cy)
				canvasX, canvasY = tx+bx, pageH-(ty+by)
			}
			if textRun != nil {
				if i < len(spans) {
					if glyphPath == nil {
						glyphPath, _ = r.cachedTextGlyphPath(face, glyph)
					}
					scaleX := hScale
					m := canvas.Identity.Translate(canvasX, canvasY)
					if useTextMatrix {
						m = m.Mul(glyphMatrix)
					}
					path := glyphPath.Copy().Transform(m.Scale(scaleX, 1))
					box := textPathBox(r.applyCachedClipPath(path, clipPath), pageH)
					for j := spans[i][0]; j < spans[i][1]; j++ {
						textRun.Boxes[textPos+j] = unionTextBox(textRun.Boxes[textPos+j], box)
					}
					bounds := canvas.Rect{Y0: -metrics.Descent, X1: glyphWidth, Y1: metrics.Ascent}
					if !glyphPath.Empty() {
						bounds = bounds.Add(glyphPath.Bounds())
					}
					textRun.addSpan(textPos+spans[i][0], textPos+spans[i][1], bounds, m.Scale(scaleX, 1), clipPath, pageH)
				}
				if r.textOnly {
					continue
				}
			}
			if shouldStroke {
				scaleX := hScale
				transform := canvas.Identity.Translate(canvasX, canvasY)
				if useTextMatrix {
					transform = transform.Mul(glyphMatrix)
				}
				path := glyphPath.Copy().Transform(transform.Scale(scaleX, 1))
				if shouldFill && fillPattern != nil {
					r.renderPattern(ctx, fillPattern, pageH, applyClipPath(path.Copy(), fillClip), patternOrigin)
				} else if shouldFill && fillPaint != nil {
					ctx.SetFill(fillPaint)
					ctx.SetStrokeColor(canvas.Transparent)
					r.drawShdPath(ctx, applyClipPath(path.Copy(), fillClip), fillView)
				}
				path = r.strokeDashedCanvasPath(path, strokeStyle.lineWidth, strokeStyle.lineCap, strokeStyle.lineJoin, strokeStyle.dashOffset, strokeStyle.dashPattern, false)
				path = r.applyCachedClipPath(path, strokeClip)
				if strokeStyle.strokePattern != nil {
					r.renderPattern(ctx, strokeStyle.strokePattern, pageH, path, patternOrigin)
				} else {
					ctx.SetFill(strokeStyle.strokePaint)
					ctx.SetStrokeColor(canvas.Transparent)
					r.drawShdPath(ctx, path, strokeView)
				}
				continue
			}
			if fillPaint != nil || fillPattern != nil {
				ctx.SetFill(fillPaint)
				ctx.SetStrokeColor(canvas.Transparent)
				glyphClip := fillClip
				if pdfText {
					glyphClip = nil
				}
				if !pdfText && fillClip != nil && !embeddedFont && face.FauxBold == 0 && !positioned && !shadedFill && fillPattern == nil && glyph.GlyphID < 0 {
					if rect, ok := rectangularPath(fillClip); ok {
						transform := canvas.Identity.Translate(canvasX, canvasY)
						if useTextMatrix {
							transform = transform.Mul(glyphMatrix)
						}
						if rect.Contains(glyphPath.Copy().Transform(transform.Scale(hScale, 1)).Bounds()) {
							available := true
							for _, char := range str {
								available = available && face.Font.GlyphIndex(char) != 0
							}
							if available {
								glyphClip, drawAsGlyphPath = nil, false
							}
						}
					}
				}
				if glyphClip != nil || shadedFill || fillPattern != nil {
					scaleX := hScale
					textTransform := canvas.Identity.Translate(canvasX, canvasY)
					if useTextMatrix {
						textTransform = textTransform.Mul(glyphMatrix)
					}
					glyphPath = r.applyCachedClipPath(glyphPath.Copy().Transform(textTransform.Scale(scaleX, 1)), glyphClip)
					if fillPattern != nil {
						r.renderPattern(ctx, fillPattern, pageH, glyphPath, patternOrigin)
					} else {
						r.drawShdPath(ctx, glyphPath, fillView)
					}
					continue
				}
				drawGlyph := func(x, y float64) {
					if drawAsGlyphPath && !pdfText {
						scaleX := hScale
						if face.FauxBold != 0 {
							if renderer, ok := ctx.Renderer.(*pdfRenderer); ok {
								glyphPath = renderer.glyphPath(glyphPath, ctx.View().Scale(scaleX, 1))
							}
						}
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
						var text *canvas.Text
						if pdfOutput != nil {
							var err error
							text, err = pdfOutput.glyphText(face, glyph)
							if err != nil {
								r.renderError = err
								if scaled {
									ctx.Pop()
								}
								return
							}
						} else {
							text = canvas.NewTextLine(face, str, canvas.Left)
						}
						ctx.DrawText(x, y, text)
						if scaled {
							ctx.Pop()
						}
					}
				}
				if useTextMatrix {
					ctx.Push()
					ctx.Translate(canvasX, canvasY)
					ctx.ComposeView(glyphMatrix)
					drawGlyph(0, 0)
					ctx.Pop()
					continue
				}
				drawGlyph(canvasX, canvasY)
			}
		}
		codePos += len(runes)
		if textRun != nil {
			if tc.Index == "" {
				textPos += len(runes)
			} else {
				textPos++
			}
		}
	}
	ctx.Pop()
}
