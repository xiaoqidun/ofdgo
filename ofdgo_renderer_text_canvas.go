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
	"slices"

	"github.com/tdewolff/canvas"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
)

// ptPerMM 为毫米转换为排版点的比例
const ptPerMM = 72.0 / 25.4

// intersectCanvasRect 计算两个画布矩形的交集并判断是否有正面积或有效范围
// 入参: a 矩形一, b 矩形二
// 返回: canvas.Rect 交集矩形, bool 是否相交
func intersectCanvasRect(a, b canvas.Rect) (canvas.Rect, bool) {
	r := canvas.Rect{
		X0: math.Max(a.X0, b.X0),
		Y0: math.Max(a.Y0, b.Y0),
		X1: math.Min(a.X1, b.X1),
		Y1: math.Min(a.Y1, b.Y1),
	}
	if r.X1 < r.X0 || r.Y1 < r.Y0 || (r.X1 == r.X0 && r.Y1 == r.Y0) {
		return canvas.Rect{}, false
	}
	return r, true
}

// renderText 渲染文本
// 入参: ctx 画布上下文, obj 文本对象, pageH 页面高度, defaults 默认绘制参数, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM, parentClip 父级裁剪路径
func (r *Renderer) renderText(ctx *canvas.Context, obj TextObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if r.pageContext != nil {
		if err := r.pageContext.Err(); err != nil {
			r.renderError = err
			return
		}
	}
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	shouldFill := obj.Fill == nil || *obj.Fill
	shouldStroke := obj.Stroke != nil && *obj.Stroke
	if !shouldFill && !shouldStroke && r.pageText == nil && !r.textOnly {
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
	objAlpha := fillAlphaByte(obj.Alpha)
	useGroup := canCanvasTransparencyGroup(ctx.Renderer) && !r.textOnly && shouldFill && shouldStroke && objAlpha < 255
	styleAlpha := obj.Alpha
	if useGroup {
		styleAlpha = nil
	}
	var fillColor color.Color
	var fillPaint any
	var fillPattern *PatternPaint
	var fillClip *canvas.Path
	var fillView canvas.Matrix
	var strokeStyle pathStyle
	var localStrokeWidth, localDashOffset float64
	var localDashPattern []float64
	var strokeClip *canvas.Path
	var strokeView canvas.Matrix
	resolveTextPaints := func(alpha *int) {
		fillColor = colorWithAlpha(canvas.Black, alpha)
		fillPaint = fillColor
		fillPattern = nil
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
			fillColorNode = withFillAlpha(fillColorNode, alpha)
			fillColor = r.parseFillColor(fillColorNode)
			fillPaint = r.parseFillPaint(fillColorNode, bx, by, pageH)
			fillPattern = parsePatternPaint(fillColorNode)
		}
		if fillPaint == nil {
			fillPaint = fillColor
		}
		transformShdPaint(fillPaint, parentCTM, bx, by, pageH, boundaryInCTM)
		var shadingClip *canvas.Path
		fillPaint, shadingClip, fillView = resolveShdPaint(ctx, fillPaint)
		fillClip = r.intersectCachedClipPath(clipPath, shadingClip)
		strokeStyle = pathStyle{}
		localStrokeWidth, localDashOffset = 0, 0
		localDashPattern = nil
		strokeClip = clipPath
		strokeView = canvas.Identity
		if shouldStroke {
			strokeStyle = r.newPathStyle(defaults, bx, by, pageH, alpha)
			if dp != nil {
				strokeStyle.applyDrawParam(r, dp, bx, by, pageH, alpha)
			}
			strokeObject := obj.pathStyle()
			strokeObject.FillColor = nil
			strokeObject.Alpha = alpha
			strokeStyle.applyPathObject(r, strokeObject, bx, by, pageH)
			if strokeStyle.strokePaint == nil {
				strokeStyle.strokePaint = canvas.Transparent
			}
			localStrokeWidth = strokeStyle.lineWidth
			localDashOffset = strokeStyle.dashOffset
			localDashPattern = slices.Clone(strokeStyle.dashPattern)
			strokeStyle.scale(ctm)
			strokeStyle.lineWidth = r.deviceLineWidth(strokeStyle.lineWidth)
			transformShdPaint(strokeStyle.strokePaint, parentCTM, bx, by, pageH, boundaryInCTM)
			strokeStyle.strokePaint, shadingClip, strokeView = resolveShdPaint(ctx, strokeStyle.strokePaint)
			strokeClip = r.intersectCachedClipPath(strokeClip, shadingClip)
		}
	}
	resolveTextPaints(styleAlpha)
	fontStyle := canvas.FontRegular
	syntheticBold := obj.Weight >= 700
	italic := obj.Italic
	if italic {
		fontStyle |= canvas.FontItalic
	}
	fontID := r.textObjectFontID(obj)
	embeddedFont := false
	if of := r.fontDefinition(fontID); of != nil {
		embeddedFont = of.FontFile != ""
		if syntheticBold && basefont.NoSyntheticBold(of.FontName, of.FamilyName) {
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
		if textRun != nil {
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
	if face.FauxBold != 0 && sfntTablesBold(face.Font.Tables) {
		face.FauxBold = 0
	}
	if face.FauxItalic != 0 && sfntTablesItalic(face.Font.Tables) {
		face.FauxItalic = 0
	}
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
	var runRunes []rune
	if textRun != nil {
		runRunes = []rune(textRun.Text)
	}
	glyphTransforms := r.textObjectGlyphTransforms(fontID, obj)
	if r.textOnly && r.textContentOnly {
		if textRun != nil && (clipPath == nil || !clipPath.Empty()) {
			var clipRect canvas.Rect
			hasRectClip := false
			if clipPath != nil {
				clipRect, hasRectClip = rectangularPath(clipPath)
			}
			codePos := 0
			textPos := 0
			lines := newTextLineScanner(effectiveObj)
			for _, tc := range obj.TextCode {
				if r.pageContext != nil {
					if err := r.pageContext.Err(); err != nil {
						r.renderError = err
						ctx.Pop()
						return
					}
				}
				if lines.next(tc) {
					textPos++
				}
				runes := textCodeRunes(tc.Value)
				var glyphs []textGlyph
				var spans [][2]int
				if tc.Index != "" {
					glyphs = r.parseIndexGlyphs(tc.Index, fontID)
					spans = indexGlyphSpans(len(glyphs))
				} else {
					glyphs = textCodeGlyphs(runes, glyphTransforms, codePos)
					spans = textGlyphSpans(runes, glyphTransforms, codePos)
				}
				positioner := NewTextPositioner(tc, obj.ReadDirection)
				for i, glyph := range glyphs {
					if i&31 == 0 && r.pageContext != nil {
						if err := r.pageContext.Err(); err != nil {
							r.renderError = err
							ctx.Pop()
							return
						}
					}
					if tc.Index != "" && glyph.Text == "" && len(runes) == len(glyphs) {
						glyph.Text = string(runes[i])
					}
					pos := positioner.Next()
					if i >= len(spans) {
						continue
					}
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
					glyphWidth := textGlyphWidth(face, glyph)
					m := canvas.Identity.Translate(canvasX, canvasY)
					if useTextMatrix {
						m = m.Mul(glyphMatrix)
					}
					gm := m.Scale(hScale, 1)
					bounds := canvas.Rect{Y0: -metrics.Descent, X1: glyphWidth, Y1: metrics.Ascent}
					rect := bounds.Transform(gm)
					if !hasRectClip || rect.Overlaps(clipRect) {
						box := Box{X: rect.X0, Y: pageH - rect.Y1, W: rect.X1 - rect.X0, H: rect.Y1 - rect.Y0}
						for j := spans[i][0]; j < spans[i][1]; j++ {
							textRun.Boxes[textPos+j] = unionTextBox(textRun.Boxes[textPos+j], box)
						}
					}
					start, end := textPos+spans[i][0], textPos+spans[i][1]
					textRun.addSpan(start, end, bounds, gm, clipPath, pageH)
					if i+1 == len(spans) || spans[i+1] != spans[i] {
						textRun.mergeLastSpan(runRunes, start, end)
					}
				}
				codePos += len(runes)
				if tc.Index == "" {
					textPos += len(runes)
				} else {
					textPos++
				}
			}
		}
		ctx.Pop()
		return
	}
	nonUniformStroke := shouldStroke && !matrixIsSimilarity(ctm)
	_, shadedFill := fillPaint.(canvas.Gradient)
	_, shadedStroke := strokeStyle.strokePaint.(canvas.Gradient)
	fillRGBA, solidFill := pdfSolidColor(fillPaint)
	strokeRGBA, solidStroke := pdfSolidColor(strokeStyle.strokePaint)
	hasVisibleStroke := shouldStroke && strokeStyle.lineWidth > 0 && strokeRGBA.A > 0
	pdfOutput, _ := ctx.Renderer.(*pdfRenderer)
	var pdfBoldColor color.RGBA
	pdfFauxBold := false
	fauxBoldOK := face.FauxBold == 0
	if !fauxBoldOK && face.FauxBold > 0 && !shouldStroke && (!useTextMatrix || glyphMatrix.IsSimilarity()) && hScale == 1 {
		if c, ok := pdfOpaqueSolidColor(fillPaint); ok {
			pdfBoldColor = c
			pdfFauxBold = true
			fauxBoldOK = true
		}
	}
	var pdfTextStyle *codecpdf.TextStrokeStyle
	pdfTextEligible := false
	if pdfOutput != nil && !r.textOnly && (shouldFill || shouldStroke) {
		if shouldFill && !shouldStroke {
			pdfTextEligible = fillPattern == nil && !shadedFill && fauxBoldOK
		} else if shouldStroke && !nonUniformStroke && !shadedStroke && strokeStyle.strokePattern == nil && solidStroke && face.FauxBold == 0 {
			if shouldFill {
				if fillPattern == nil && !shadedFill && solidFill {
					pdfTextEligible = true
					if hasVisibleStroke {
						pdfTextStyle = &codecpdf.TextStrokeStyle{
							RenderMode:  2,
							FillAlpha:   fillRGBA.A,
							HasFill:     true,
							StrokeColor: strokeRGBA,
							LineWidth:   strokeStyle.lineWidth,
							LineCap:     strokeStyle.lineCap,
							LineJoin:    strokeStyle.lineJoin,
							DashOffset:  strokeStyle.dashOffset,
							Dashes:      strokeStyle.dashPattern,
						}
					}
				}
			} else {
				pdfTextEligible = true
				if hasVisibleStroke {
					pdfTextStyle = &codecpdf.TextStrokeStyle{
						RenderMode:  1,
						StrokeColor: strokeRGBA,
						LineWidth:   strokeStyle.lineWidth,
						LineCap:     strokeStyle.lineCap,
						LineJoin:    strokeStyle.lineJoin,
						DashOffset:  strokeStyle.dashOffset,
						Dashes:      strokeStyle.dashPattern,
					}
				} else {
					pdfTextStyle = &codecpdf.TextStrokeStyle{RenderMode: 3}
				}
			}
		}
	}
	if useGroup {
		if endGroup, ok := beginCanvasTransparencyGroup(ctx.Renderer, objAlpha); ok {
			defer endGroup()
			if pdfOutput != nil {
				defer pdfOutput.finishTextObject()
			}
		} else {
			styleAlpha = obj.Alpha
			resolveTextPaints(styleAlpha)
			face = ff.Face(sizePt, fillPaint, fontStyle, canvas.FontNormal)
			face.Hinting = 0
			if face.FauxBold != 0 && sfntTablesBold(face.Font.Tables) {
				face.FauxBold = 0
			}
			if face.FauxItalic != 0 && sfntTablesItalic(face.Font.Tables) {
				face.FauxItalic = 0
			}
			_, shadedFill = fillPaint.(canvas.Gradient)
			_, shadedStroke = strokeStyle.strokePaint.(canvas.Gradient)
			fillRGBA, solidFill = pdfSolidColor(fillPaint)
			strokeRGBA, solidStroke = pdfSolidColor(strokeStyle.strokePaint)
			hasVisibleStroke = shouldStroke && strokeStyle.lineWidth > 0 && strokeRGBA.A > 0
			pdfBoldColor = color.RGBA{}
			pdfFauxBold = false
			fauxBoldOK = face.FauxBold == 0
			if !fauxBoldOK && face.FauxBold > 0 && !shouldStroke && (!useTextMatrix || glyphMatrix.IsSimilarity()) && hScale == 1 {
				if c, ok := pdfOpaqueSolidColor(fillPaint); ok {
					pdfBoldColor = c
					pdfFauxBold = true
					fauxBoldOK = true
				}
			}
			pdfTextStyle = nil
			pdfTextEligible = false
			if pdfOutput != nil && !r.textOnly && (shouldFill || shouldStroke) {
				if shouldFill && !shouldStroke {
					pdfTextEligible = fillPattern == nil && !shadedFill && fauxBoldOK
				} else if shouldStroke && !nonUniformStroke && !shadedStroke && strokeStyle.strokePattern == nil && solidStroke && face.FauxBold == 0 {
					if shouldFill {
						if fillPattern == nil && !shadedFill && solidFill {
							pdfTextEligible = true
							if hasVisibleStroke {
								pdfTextStyle = &codecpdf.TextStrokeStyle{
									RenderMode:  2,
									FillAlpha:   fillRGBA.A,
									HasFill:     true,
									StrokeColor: strokeRGBA,
									LineWidth:   strokeStyle.lineWidth,
									LineCap:     strokeStyle.lineCap,
									LineJoin:    strokeStyle.lineJoin,
									DashOffset:  strokeStyle.dashOffset,
									Dashes:      strokeStyle.dashPattern,
								}
							}
						}
					} else {
						pdfTextEligible = true
						if hasVisibleStroke {
							pdfTextStyle = &codecpdf.TextStrokeStyle{
								RenderMode:  1,
								StrokeColor: strokeRGBA,
								LineWidth:   strokeStyle.lineWidth,
								LineCap:     strokeStyle.lineCap,
								LineJoin:    strokeStyle.lineJoin,
								DashOffset:  strokeStyle.dashOffset,
								Dashes:      strokeStyle.dashPattern,
							}
						} else {
							pdfTextStyle = &codecpdf.TextStrokeStyle{RenderMode: 3}
						}
					}
				}
			}
		}
	}
	pdfText := pdfTextEligible
	if pdfText {
		view := canvas.Identity
		if useTextMatrix {
			view = glyphMatrix
		}
		stroked := pdfTextStyle != nil && (pdfTextStyle.RenderMode == 1 || pdfTextStyle.RenderMode == 2) || pdfFauxBold
		pdfText = r.pdfTextSupported(obj, fontID, face, glyphTransforms, fillClip, pageH, objectCTM, view, stroked)
	}
	if pdfText {
		if len(pdfOutput.text) == 0 {
			pdfOutput.text = append(pdfOutput.text, codecpdf.TextPage{})
		}
		firstBT := pdfOutput.text[len(pdfOutput.text)-1].Count
		if r.pdfTextNeedsActualText(pdfOutput, effectiveObj, fontID, face, glyphTransforms) {
			defer pdfOutput.preserveText(firstBT, r.pdfActualText(effectiveObj, fontID, face))
		}
		if pdfTextStyle != nil {
			pdfOutput.content.IsolateNextText()
			style := *pdfTextStyle
			defer pdfOutput.preserveTextStyle(firstBT, style)
		} else if pdfFauxBold {
			boldScale := 1.0
			if useTextMatrix {
				boldScale = math.Sqrt(math.Abs(glyphMatrix.Det()))
			}
			boldWidth := 2 * face.FauxBold * face.Size * boldScale
			defer pdfOutput.preserveFauxBold(firstBT, boldWidth, pdfBoldColor)
		}
	}
	textFillPaint := fillPaint
	if pdfText && !shouldFill && shouldStroke {
		textFillPaint = strokeStyle.strokePaint
	}
	codePos := 0
	textPos := 0
	var lines textLineScanner
	if textRun != nil {
		lines = newTextLineScanner(effectiveObj)
	}
	var batch []pdfGlyphItem
	var batchIDs map[uint16]bool
	var batchY, batchCanvasX, batchCanvasY float64
	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		ctx.SetFill(textFillPaint)
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
	var clipRect canvas.Rect
	hasRectClip := false
	if clipPath != nil {
		clipRect, hasRectClip = rectangularPath(clipPath)
	}
	visibleInClip := clipPath == nil
	for _, tc := range obj.TextCode {
		if r.pageContext != nil {
			if err := r.pageContext.Err(); err != nil {
				r.renderError = err
				ctx.Pop()
				return
			}
		}
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
			if i&31 == 0 && r.pageContext != nil {
				if err := r.pageContext.Err(); err != nil {
					r.renderError = err
					ctx.Pop()
					return
				}
			}
			if tc.Index != "" && glyph.Text == "" && len(runes) == len(glyphs) {
				glyph.Text = string(runes[i])
			}
			str := glyph.Text
			drawAsGlyphPath := drawAsPath || glyph.GlyphID >= 0
			if !drawAsGlyphPath {
				for _, char := range str {
					if r.canvasGlyphIndex(face.Font, char) == 0 {
						drawAsGlyphPath = true
						break
					}
				}
			}
			var glyphPath *canvas.Path
			var glyphWidth float64
			var glyphBounds canvas.Rect
			hasGlyphCached := false
			if drawAsGlyphPath && !pdfText {
				cached := r.cachedTextGlyph(face, glyph)
				glyphPath, glyphWidth, glyphBounds = cached.path, cached.width, cached.bounds
				hasGlyphCached = true
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
					if !hasGlyphCached {
						cached := r.cachedTextGlyph(face, glyph)
						glyphPath, glyphBounds = cached.path, cached.bounds
					}
					scaleX := hScale
					m := canvas.Identity.Translate(canvasX, canvasY)
					if useTextMatrix {
						m = m.Mul(glyphMatrix)
					}
					gm := m.Scale(scaleX, 1)
					hasGlyphOutline := !glyphPath.Empty()
					var box Box
					if hasGlyphOutline {
						rect := glyphBounds.Transform(gm)
						if !hasRectClip || rect.Overlaps(clipRect) {
							if isAxisAlignedCanvasMatrix(gm) && (clipPath == nil || hasRectClip && clipRect.Contains(rect)) {
								box = Box{X: rect.X0, Y: pageH - rect.Y1, W: rect.X1 - rect.X0, H: rect.Y1 - rect.Y0}
							} else {
								path := glyphPath.Copy().Transform(gm)
								box = textPathBox(r.applyCachedClipPath(path, clipPath), pageH)
							}
						}
					}
					for j := spans[i][0]; j < spans[i][1]; j++ {
						textRun.Boxes[textPos+j] = unionTextBox(textRun.Boxes[textPos+j], box)
					}
					bounds := canvas.Rect{Y0: -metrics.Descent, X1: glyphWidth, Y1: metrics.Ascent}
					if hasGlyphOutline {
						bounds = bounds.Add(glyphBounds)
					}
					start, end := textPos+spans[i][0], textPos+spans[i][1]
					textRun.addSpan(start, end, bounds, gm, clipPath, pageH)
					if i+1 == len(spans) || spans[i+1] != spans[i] {
						textRun.mergeLastSpan(runRunes, start, end)
					}
				}
				if r.textOnly || !shouldFill && !shouldStroke {
					continue
				}
			}
			if !shouldFill && !shouldStroke {
				continue
			}
			if shouldStroke && !pdfText {
				scaleX := hScale
				transform := canvas.Identity.Translate(canvasX, canvasY)
				if useTextMatrix {
					transform = transform.Mul(glyphMatrix)
				}
				path := glyphPath.Copy().Transform(transform.Scale(scaleX, 1))
				if shouldFill && fillPattern != nil {
					clippedFill := applyClipPath(path.Copy(), fillClip)
					if !clippedFill.Empty() {
						visibleInClip = true
					}
					r.renderPattern(ctx, fillPattern, pageH, clippedFill, patternOrigin)
				} else if shouldFill && fillPaint != nil {
					clippedFill := applyClipPath(path.Copy(), fillClip)
					if !clippedFill.Empty() {
						visibleInClip = true
					}
					ctx.SetFill(fillPaint)
					ctx.SetStrokeColor(canvas.Transparent)
					r.drawShdPath(ctx, clippedFill, fillView)
				}
				if nonUniformStroke {
					localTransform := canvas.Identity.Rotate(-float64(obj.CharDirection)).Scale(scaleX, 1)
					localGlyph := glyphPath.Copy().Transform(localTransform)
					localStroked := r.strokeDashedCanvasPath(localGlyph, r.localStrokeWidth(localStrokeWidth, ctm), strokeStyle.lineCap, strokeStyle.lineJoin, localDashOffset, localDashPattern, false)
					pageTransform := canvas.Identity.Translate(canvasX, canvasY).Mul(textMatrix(ctm))
					path = r.applyCachedClipPath(localStroked.Transform(pageTransform), strokeClip)
				} else {
					path = r.strokeDashedCanvasPath(path, strokeStyle.lineWidth, strokeStyle.lineCap, strokeStyle.lineJoin, strokeStyle.dashOffset, strokeStyle.dashPattern, false)
					path = r.applyCachedClipPath(path, strokeClip)
				}
				if !path.Empty() {
					visibleInClip = true
				}
				if strokeStyle.strokePattern != nil {
					r.renderPattern(ctx, strokeStyle.strokePattern, pageH, path, patternOrigin)
				} else {
					ctx.SetFill(strokeStyle.strokePaint)
					ctx.SetStrokeColor(canvas.Transparent)
					r.drawShdPath(ctx, path, strokeView)
				}
				continue
			}
			if pdfText || fillPaint != nil || fillPattern != nil {
				if pdfText && obj.CharDirection == 0 && face.MmPerEm > 0 && hScale != 0 {
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
								ctx.Pop()
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
						ctx.Pop()
						return
					}
				}
				ctx.SetFill(textFillPaint)
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
								available = available && r.canvasGlyphIndex(face.Font, char) != 0
							}
							if available {
								glyphClip, drawAsGlyphPath = nil, false
								visibleInClip = true
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
					if !glyphPath.Empty() {
						visibleInClip = true
					}
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
						shear := pdfOutput != nil && face.FauxItalic != 0
						transformed := hScale != 1 || shear
						if transformed {
							ctx.Push()
							ctx.Translate(x, y)
							if hScale != 1 {
								ctx.Scale(hScale, 1)
							}
							if shear {
								ctx.ComposeView(canvas.Identity.Shear(face.FauxItalic, 0))
							}
							x, y = 0, 0
						}
						var text *canvas.Text
						if pdfOutput != nil {
							var err error
							text, err = pdfOutput.glyphText(face, glyph)
							if err != nil {
								r.renderError = err
								if transformed {
									ctx.Pop()
								}
								return
							}
						} else {
							text = canvas.NewTextLine(face, str, canvas.Left)
						}
						ctx.DrawText(x, y, text)
						if transformed {
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
		flushBatch()
		if r.renderError != nil {
			ctx.Pop()
			return
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
	if pdfOutput != nil && !r.textOnly && !pdfText && (shouldFill || shouldStroke) && r.renderError == nil && (clipPath == nil || !clipPath.Empty() && visibleInClip) {
		r.emitPDFInvisibleText(ctx, pdfOutput, effectiveObj, fontID, face, glyphTransforms, pageH, bx, by, ctm, localCTM, parentCTM, boundaryInCTM, hScale, useTextMatrix, glyphMatrix, clipPath)
	}
	ctx.Pop()
}
