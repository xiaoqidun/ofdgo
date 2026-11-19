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
	compiler, isCompiler := ctx.Renderer.(*canvasPageCompiler)
	objAlpha := fillAlphaByte(obj.Alpha)
	useGroup := isCompiler && !r.textOnly && shouldFill && shouldStroke && objAlpha < 255
	styleAlpha := obj.Alpha
	if useGroup {
		styleAlpha = nil
	}
	fillColor := colorWithAlpha(canvas.Black, styleAlpha)
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
		fillColorNode = withFillAlpha(fillColorNode, styleAlpha)
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
	var localStrokeWidth, localDashOffset float64
	var localDashPattern []float64
	strokeClip := clipPath
	strokeView := canvas.Identity
	if shouldStroke {
		strokeStyle = r.newPathStyle(defaults, bx, by, pageH, styleAlpha)
		if dp != nil {
			strokeStyle.applyDrawParam(r, dp, bx, by, pageH, styleAlpha)
		}
		strokeObject := obj.pathStyle()
		strokeObject.FillColor = nil
		strokeObject.Alpha = styleAlpha
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
	if face.FauxBold != 0 && sfntTablesBold(face.Font.Tables) {
		face.FauxBold = 0
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
	if r.textOnly && r.textContentOnly {
		ctx.Pop()
		return
	}
	glyphTransforms := r.textObjectGlyphTransforms(fontID, obj)
	_, shadedFill := fillPaint.(canvas.Gradient)
	pdfOutput, _ := ctx.Renderer.(*pdfRenderer)
	var pdfBoldColor color.RGBA
	pdfFauxBold := false
	fauxBoldOK := face.FauxBold == 0
	if !fauxBoldOK && face.FauxBold > 0 && (!useTextMatrix || glyphMatrix.IsSimilarity()) && hScale == 1 {
		if c, ok := pdfOpaqueSolidColor(fillPaint); ok {
			pdfBoldColor = c
			pdfFauxBold = true
			fauxBoldOK = true
		}
	}
	pdfText := pdfOutput != nil && !r.textOnly && shouldFill && !shouldStroke && fillPattern == nil && !shadedFill && fauxBoldOK
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
		firstBT := pdfOutput.text[len(pdfOutput.text)-1].Count
		defer pdfOutput.preserveText(firstBT, r.pdfActualText(effectiveObj, fontID, face))
		if pdfFauxBold {
			boldScale := 1.0
			if useTextMatrix {
				boldScale = math.Sqrt(math.Abs(glyphMatrix.Det()))
			}
			boldWidth := 2 * face.FauxBold * face.Size * boldScale
			defer pdfOutput.preserveFauxBold(firstBT, boldWidth, pdfBoldColor)
		}
	}
	if useGroup {
		compiler.page.Commands = append(compiler.page.Commands, RasterCommand{Group: RasterGroupBegin, GroupAlpha: objAlpha})
		defer func() {
			compiler.page.Commands = append(compiler.page.Commands, RasterCommand{Group: RasterGroupEnd})
		}()
	}
	nonUniformStroke := shouldStroke && !matrixIsSimilarity(ctm)
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
		ctx.SetFill(fillPaint)
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
			if tc.Index != "" && glyph.Text == "" && len(runes) == len(glyphs) {
				glyph.Text = string(runes[i])
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
				if pdfText && obj.CharDirection == 0 && face.MmPerEm > 0 && hScale != 0 {
					if gid, ok := pdfSingleGlyphID(face, glyph); ok {
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
	ctx.Pop()
}
