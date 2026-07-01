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
	"bytes"
	"fmt"
	"image"
	"image/color"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/tdewolff/canvas/renderers/rasterizer"
	_ "github.com/xiaoqidun/jbig2"
)

// Renderer 渲染器实现
type Renderer struct {
	Reader                *Reader
	DPI                   float64
	RenderAnnotations     bool
	fontFamily            *canvas.FontFamily
	defaultFontLoaded     bool
	DrawParams            map[string]*DrawParam
	CompositeGraphicUnits map[string]*CompositeGraphicUnit
	FontMap               map[string]*canvas.FontFamily
	FontGIDMap            map[string]map[uint16]rune
	FontCIDMap            map[string]map[uint16]rune
	fontDirs              []string
	fontFS                []fs.FS
}

// RendererOption 渲染器配置选项
type RendererOption func(*Renderer)

// RenderPage 渲染特定页面内容
// 入参: page 页面内容
// 返回: *canvas.Canvas 画布实例, error 错误信息
func (r *Renderer) RenderPage(page *PageContent) (*canvas.Canvas, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	width, height := box.W, box.H
	c := canvas.New(width, height)
	ctx := canvas.NewContext(c)
	if err := r.RenderPageToContext(ctx, page); err != nil {
		return nil, err
	}
	return c, nil
}

// GetPageBox 获取页面物理区域
// 入参: page 页面内容
// 返回: Box 区域, error 错误信息
func (r *Renderer) GetPageBox(page *PageContent) (Box, error) {
	boxStr := page.Area.PhysicalBox
	if boxStr == "" {
		boxStr = page.Area.ApplicationBox
	}
	if boxStr == "" {
		boxStr = page.Area.ContentBox
	}
	if boxStr == "" && r.Reader.doc != nil {
		boxStr = r.Reader.doc.CommonData.PageArea.PhysicalBox
	}
	if boxStr == "" {
		boxStr = "0 0 210 297"
	}
	return ParseBox(boxStr)
}

// RenderPageToContext 渲染页面到指定上下文
// 入参: ctx 画布上下文, page 页面内容
// 返回: error 错误信息
func (r *Renderer) RenderPageToContext(ctx *canvas.Context, page *PageContent) error {
	return r.renderPageToContext(ctx, page, true)
}

// renderPageToContext 渲染页面到指定上下文
// 入参: ctx 画布上下文, page 页面内容, drawBackground 是否绘制页面背景
// 返回: error 错误信息
func (r *Renderer) renderPageToContext(ctx *canvas.Context, page *PageContent, drawBackground bool) error {
	box, err := r.GetPageBox(page)
	if err != nil {
		return err
	}
	pageH := box.H
	if drawBackground {
		ctx.SetFillColor(canvas.White)
		ctx.DrawPath(0, 0, canvas.Rectangle(box.W, box.H))
	}
	if len(page.Template) > 0 && r.Reader.doc != nil {
		for _, tplRef := range page.Template {
			if tplRef.ZOrder != "Foreground" {
				r.renderTemplate(ctx, tplRef.TemplateID, pageH)
			}
		}
	}
	if page.Content.Layer != nil {
		for _, layer := range page.Content.Layer {
			r.renderLayer(ctx, layer, pageH, nil, nil, 0, nil)
		}
	}
	if len(page.Template) > 0 && r.Reader.doc != nil {
		for _, tplRef := range page.Template {
			if tplRef.ZOrder == "Foreground" {
				r.renderTemplate(ctx, tplRef.TemplateID, pageH)
			}
		}
	}
	if r.RenderAnnotations {
		r.renderAnnotations(ctx, page.ID, pageH)
	}
	if stamps, ok := r.Reader.Stamps[page.ID]; ok {
		for _, stamp := range stamps {
			r.renderStamp(ctx, stamp, pageH)
		}
	}
	return nil
}

// RenderPageByIndex 按索引渲染页面
// 入参: index 页面索引
// 返回: *canvas.Canvas 画布实例, error 错误信息
func (r *Renderer) RenderPageByIndex(index int) (*canvas.Canvas, error) {
	doc, err := r.Reader.Doc()
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Pages.Page) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	page, err := r.Reader.PageContent(doc.Pages.Page[index])
	if err != nil {
		return nil, err
	}
	return r.RenderPage(page)
}

// RenderToImage 渲染为光栅图
// 入参: page 页面内容
// 返回: image.Image 图像对象, error 错误信息
func (r *Renderer) RenderToImage(page *PageContent) (image.Image, error) {
	c, err := r.RenderPage(page)
	if err != nil {
		return nil, err
	}
	dpmm := r.DPI / 25.4
	return rasterizer.Draw(c, canvas.DPMM(dpmm), canvas.DefaultColorSpace), nil
}

// RenderToSVG 渲染为SVG
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToSVG(page *PageContent, writer io.Writer) error {
	c, err := r.RenderPage(page)
	if err != nil {
		return err
	}
	return c.Write(writer, renderers.SVG())
}

// RenderToPDF 渲染为PDF
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToPDF(page *PageContent, writer io.Writer) error {
	c, err := r.RenderPage(page)
	if err != nil {
		return err
	}
	return c.Write(writer, renderers.PDF())
}

// RenderToEPS 渲染为EPS
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToEPS(page *PageContent, writer io.Writer) error {
	c, err := r.RenderPage(page)
	if err != nil {
		return err
	}
	return c.Write(writer, renderers.EPS())
}

// RenderToMultiPagePDF 将整个文档导出为多页PDF
// 入参: writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToMultiPagePDF(writer io.Writer) error {
	doc, err := r.Reader.Doc()
	if err != nil {
		return err
	}
	if len(doc.Pages.Page) == 0 {
		return fmt.Errorf("no pages found")
	}
	var p *pdf.PDF
	for i, pgRef := range doc.Pages.Page {
		page, err := r.Reader.PageContent(pgRef)
		if err != nil {
			continue
		}
		c, err := r.RenderPage(page)
		if err != nil {
			continue
		}
		if i == 0 {
			p = pdf.New(writer, c.W, c.H, nil)
		} else {
			p.NewPage(c.W, c.H)
		}
		c.RenderTo(p)
	}
	if p == nil {
		return fmt.Errorf("failed to render any page")
	}
	return p.Close()
}

// renderAnnotations 渲染页面注释外观
// 入参: ctx 画布上下文, pageID 页面ID, pageH 页面高度
func (r *Renderer) renderAnnotations(ctx *canvas.Context, pageID string, pageH float64) {
	for _, annot := range r.Reader.Annots[pageID] {
		if len(annot.Appearance.Objects) == 0 {
			continue
		}
		box, _ := ParseBox(annot.Appearance.Boundary)
		ctm := Matrix{a: 1, d: 1, e: box.X, f: box.Y}
		for _, obj := range annot.Appearance.Objects {
			r.renderObject(ctx, obj, pageH, nil, nil, 0, &ctm)
		}
	}
}

// renderTemplate 渲染模板
// 入参: ctx 画布上下文, templateID 模板ID, pageH 页面高度
func (r *Renderer) renderTemplate(ctx *canvas.Context, templateID string, pageH float64) {
	var tplPage *TemplatePage
	for _, tp := range r.Reader.doc.CommonData.TemplatePage {
		if tp.ID == templateID {
			tplPage = &tp
			break
		}
	}
	if tplPage == nil {
		return
	}
	tplContent, err := r.Reader.PageContent(Page{BaseLoc: tplPage.BaseLoc})
	if err != nil {
		return
	}
	if tplContent.Content.Layer != nil {
		for _, layer := range tplContent.Content.Layer {
			r.renderLayer(ctx, layer, pageH, nil, nil, 0, nil)
		}
	}
}

// renderLayer 渲染图层
// 入参: ctx 画布上下文, layer 图层对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM
func (r *Renderer) renderLayer(ctx *canvas.Context, layer Layer, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix) {
	if layer.DrawParam != "" {
		if dp := r.getDrawParam(layer.DrawParam, nil); dp != nil {
			if dp.LineWidth > 0 {
				defaultLW = dp.LineWidth
			}
			if dp.FillColor != nil {
				defaultFill = parseFillColor(dp.FillColor)
			}
			if dp.StrokeColor != nil {
				defaultStroke = parseStrokeColor(dp.StrokeColor)
			}
		}
	}
	if len(layer.Objects) > 0 {
		for _, obj := range layer.Objects {
			r.renderObject(ctx, obj, pageH, defaultFill, defaultStroke, defaultLW, parentCTM)
		}
		return
	}
	for _, textObj := range layer.TextObject {
		r.renderText(ctx, textObj, pageH, defaultFill, defaultStroke, parentCTM)
	}
	for _, pathObj := range layer.PathObject {
		r.renderPath(ctx, pathObj, pageH, defaultFill, defaultStroke, defaultLW, parentCTM)
	}
	for _, imgObj := range layer.ImageObject {
		r.renderImage(ctx, imgObj, pageH, parentCTM)
	}
	for _, cgu := range layer.CompositeGraphicUnit {
		r.renderCompositeGraphicUnit(ctx, cgu, pageH, defaultFill, defaultStroke, defaultLW, parentCTM)
	}
}

// renderCompositeGraphicUnit 渲染复合图元
// 入参: ctx 画布上下文, cgu 复合图元对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM
func (r *Renderer) renderCompositeGraphicUnit(ctx *canvas.Context, cgu CompositeGraphicUnit, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix) {
	if cgu.Visible != nil && !*cgu.Visible {
		return
	}
	ctx.Push()
	currentCTM := NewMatrix(cgu.CTM)
	if parentCTM != nil {
		currentCTM = parentCTM.Multiply(currentCTM)
	}
	if cgu.ResourceID != "" {
		if ref, ok := r.CompositeGraphicUnits[cgu.ResourceID]; ok {
			refCopy := *ref
			refCopy.Alpha = mergeAlpha(refCopy.Alpha, cgu.Alpha)
			r.renderCompositeGraphicUnit(ctx, refCopy, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM)
		}
	}
	if cgu.DrawParam != "" {
		if dp := r.getDrawParam(cgu.DrawParam, nil); dp != nil {
			if dp.LineWidth > 0 {
				defaultLW = dp.LineWidth
			}
			if dp.FillColor != nil {
				defaultFill = parseFillColor(dp.FillColor)
			}
			if dp.StrokeColor != nil {
				defaultStroke = parseStrokeColor(dp.StrokeColor)
			}
		}
	}
	if len(cgu.Objects) > 0 {
		for _, obj := range cgu.Objects {
			obj = mergeGraphicObjectAlpha(obj, cgu.Alpha)
			r.renderObject(ctx, obj, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM)
		}
		ctx.Pop()
		return
	}
	for _, imgObj := range cgu.ImageObject {
		imgObj.Alpha = mergeAlpha(imgObj.Alpha, cgu.Alpha)
		r.renderImage(ctx, imgObj, pageH, &currentCTM)
	}
	for _, pathObj := range cgu.PathObject {
		pathObj.Alpha = mergeAlpha(pathObj.Alpha, cgu.Alpha)
		r.renderPath(ctx, pathObj, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM)
	}
	for _, textObj := range cgu.TextObject {
		textObj.Alpha = mergeAlpha(textObj.Alpha, cgu.Alpha)
		r.renderText(ctx, textObj, pageH, defaultFill, defaultStroke, &currentCTM)
	}
	for _, subCgu := range cgu.CompositeGraphicUnit {
		subCgu.Alpha = mergeAlpha(subCgu.Alpha, cgu.Alpha)
		r.renderCompositeGraphicUnit(ctx, subCgu, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM)
	}
	ctx.Pop()
}

// renderObject 渲染图形对象
// 入参: ctx 画布上下文, obj 图形对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM
func (r *Renderer) renderObject(ctx *canvas.Context, obj GraphicObject, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix) {
	switch obj.Type {
	case "TextObject":
		r.renderText(ctx, obj.TextObject, pageH, defaultFill, defaultStroke, parentCTM)
	case "PathObject":
		r.renderPath(ctx, obj.PathObject, pageH, defaultFill, defaultStroke, defaultLW, parentCTM)
	case "ImageObject":
		r.renderImage(ctx, obj.ImageObject, pageH, parentCTM)
	case "CompositeGraphicUnit", "CompositeObject":
		r.renderCompositeGraphicUnit(ctx, obj.CompositeGraphicUnit, pageH, defaultFill, defaultStroke, defaultLW, parentCTM)
	}
}

// mergeGraphicObjectAlpha 合并图形对象透明度
// 入参: obj 图形对象, alpha 父级透明度
// 返回: GraphicObject 合并后的图形对象
func mergeGraphicObjectAlpha(obj GraphicObject, alpha *int) GraphicObject {
	if alpha == nil {
		return obj
	}
	switch obj.Type {
	case "TextObject":
		obj.TextObject.Alpha = mergeAlpha(obj.TextObject.Alpha, alpha)
	case "PathObject":
		obj.PathObject.Alpha = mergeAlpha(obj.PathObject.Alpha, alpha)
	case "ImageObject":
		obj.ImageObject.Alpha = mergeAlpha(obj.ImageObject.Alpha, alpha)
	case "CompositeGraphicUnit", "CompositeObject":
		obj.CompositeGraphicUnit.Alpha = mergeAlpha(obj.CompositeGraphicUnit.Alpha, alpha)
	}
	return obj
}

// getDrawParam 获取绘制参数逻辑
// 入参: id 参数ID, visited 访问记录
// 返回: *DrawParam 绘制参数
func (r *Renderer) getDrawParam(id string, visited map[string]bool) *DrawParam {
	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[id] {
		return nil
	}
	visited[id] = true
	if dp, ok := r.DrawParams[id]; ok {
		if dp.Relative != "" {
			base := r.getDrawParam(dp.Relative, visited)
			if base == nil {
				return dp
			}
			merged := *base
			if dp.LineWidth > 0 {
				merged.LineWidth = dp.LineWidth
			}
			if dp.Join != "" {
				merged.Join = dp.Join
			}
			if dp.Cap != "" {
				merged.Cap = dp.Cap
			}
			if dp.DashPattern != "" {
				merged.DashPattern = dp.DashPattern
				merged.DashOffset = dp.DashOffset
			}
			if dp.MiterLimit > 0 {
				merged.MiterLimit = dp.MiterLimit
			}
			if dp.FillColor != nil {
				merged.FillColor = dp.FillColor
			}
			if dp.StrokeColor != nil {
				merged.StrokeColor = dp.StrokeColor
			}
			if dp.Font != "" {
				merged.Font = dp.Font
			}
			if dp.Size > 0 {
				merged.Size = dp.Size
			}
			if dp.Weight > 0 {
				merged.Weight = dp.Weight
			}
			if dp.Italic {
				merged.Italic = dp.Italic
			}
			return &merged
		}
		return dp
	}
	return nil
}

// initCommon 初始化公共资源
func (r *Renderer) initCommon() {
	r.fontFamily = canvas.NewFontFamily("default")
	r.defaultFontLoaded = r.loadDefaultFonts()
}

// renderImage 渲染图片
// 入参: ctx 画布上下文, obj 图片对象, pageH 页面高度, parentCTM 父级CTM
func (r *Renderer) renderImage(ctx *canvas.Context, obj ImageObject, pageH float64, parentCTM *Matrix) {
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	resPath, ok := r.Reader.ResMap[obj.ResourceID]
	if !ok {
		return
	}
	imgData, err := r.Reader.ResData(resPath)
	if err != nil {
		return
	}
	img, _, err := image.Decode(bytes.NewReader(imgData))
	if err != nil {
		return
	}
	box, _ := ParseBox(obj.Boundary)
	imgBounds := img.Bounds()
	imgW, imgH := float64(imgBounds.Dx()), float64(imgBounds.Dy())
	if imgW <= 0 || imgH <= 0 {
		return
	}
	img = imageWithAlpha(img, obj.Alpha)
	ctm := NewMatrix(obj.CTM)
	if obj.CTM == "" {
		ctm = Matrix{a: box.W, d: box.H}
	}
	if parentCTM != nil {
		ctm = parentCTM.Multiply(ctm)
	}
	tx, ty := ctm.Transform(0, 1)
	canvasX, canvasY := tx+box.X, pageH-(ty+box.Y)
	ctx.Push()
	ctx.Translate(canvasX, canvasY)
	ctx.Scale(ctm.a/imgW, ctm.d/imgH)
	ctx.DrawImage(0, 0, img, canvas.DPMM(1.0))
	ctx.Pop()
}

// imageWithAlpha 合并图片透明度
// 入参: img 图片对象, alpha 对象透明度
// 返回: image.Image 合并后的图片对象
func imageWithAlpha(img image.Image, alpha *int) image.Image {
	if img == nil || alpha == nil {
		return img
	}
	a := clampColor(*alpha)
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			c.A = uint8(int(c.A) * a / 255)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

// renderPath 渲染路径
// 入参: ctx 画布上下文, obj 路径对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM
func (r *Renderer) renderPath(ctx *canvas.Context, obj PathObject, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix) {
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
	ctm := NewMatrix(obj.CTM)
	if parentCTM != nil {
		ctm = parentCTM.Multiply(ctm)
	}
	fillColor, strokeColor := colorWithAlpha(defaultFill, obj.Alpha), colorWithAlpha(defaultStroke, obj.Alpha)
	var fillPaint any = fillColor
	var strokePaint any = strokeColor
	var fillPattern *Pattern
	var fillPatternColor color.Color
	lineWidth := defaultLW
	if lineWidth == 0 {
		lineWidth = 0.353
	}
	lineCap := canvas.ButtCap
	lineJoin := canvas.MiterJoin
	dashOffset := 0.0
	var dashPattern []float64
	if obj.DrawParam != "" {
		if dp := r.getDrawParam(obj.DrawParam, nil); dp != nil {
			if dp.LineWidth > 0 {
				lineWidth = dp.LineWidth
			}
			if dp.FillColor != nil {
				fillColorNode := withFillAlpha(dp.FillColor, obj.Alpha)
				fillPattern = fillColorNode.Pattern
				fillPatternColor = patternColor(fillColorNode)
				fillColor = parseFillColor(fillColorNode)
				fillPaint = parseFillPaint(fillColorNode, bx, by, pageH, 0, 0)
			}
			if dp.StrokeColor != nil {
				strokeColorNode := withStrokeAlpha(dp.StrokeColor, obj.Alpha)
				strokeColor = parseStrokeColor(strokeColorNode)
				strokePaint = parseStrokePaint(strokeColorNode, bx, by, pageH, 0, 0)
			}
			if dp.Cap == "Round" {
				lineCap = canvas.RoundCap
			} else if dp.Cap == "Square" {
				lineCap = canvas.SquareCap
			}
			if dp.Join == "Round" {
				lineJoin = canvas.RoundJoin
			} else if dp.Join == "Bevel" {
				lineJoin = canvas.BevelJoin
			}
			if dp.DashPattern != "" {
				dashPattern = parseFloats(dp.DashPattern)
				dashOffset = dp.DashOffset
			}
		}
	}
	if obj.LineWidth > 0 {
		lineWidth = obj.LineWidth
	}
	if obj.FillColor != nil {
		fillColorNode := withFillAlpha(obj.FillColor, obj.Alpha)
		fillPattern = fillColorNode.Pattern
		fillPatternColor = patternColor(fillColorNode)
		fillColor = parseFillColor(fillColorNode)
		fillPaint = parseFillPaint(fillColorNode, bx, by, pageH, 0, 0)
	}
	if obj.StrokeColor != nil {
		strokeColorNode := withStrokeAlpha(obj.StrokeColor, obj.Alpha)
		strokeColor = parseStrokeColor(strokeColorNode)
		strokePaint = parseStrokePaint(strokeColorNode, bx, by, pageH, 0, 0)
	}
	if obj.Cap != "" {
		if obj.Cap == "Round" {
			lineCap = canvas.RoundCap
		} else if obj.Cap == "Square" {
			lineCap = canvas.SquareCap
		} else {
			lineCap = canvas.ButtCap
		}
	}
	if obj.Join != "" {
		if obj.Join == "Round" {
			lineJoin = canvas.RoundJoin
		} else if obj.Join == "Bevel" {
			lineJoin = canvas.BevelJoin
		} else {
			lineJoin = canvas.MiterJoin
		}
	}
	if obj.DashPattern != "" {
		dashPattern = parseFloats(obj.DashPattern)
		dashOffset = obj.DashOffset
	}
	if scale := math.Sqrt(math.Abs(ctm.a*ctm.d - ctm.b*ctm.c)); scale > 0 {
		lineWidth *= scale
		dashOffset *= scale
		for i := range dashPattern {
			dashPattern[i] *= scale
		}
	}
	p := r.buildPath(obj, pageH, ctm)
	if rectPath := r.buildTinyFillRectPath(obj, pageH, ctm, bx, by); rectPath != nil {
		p = rectPath
	}
	clipPath := r.buildClipPath(obj.Clips, pageH, bx, by, ctm)
	shouldFill := false
	if obj.Fill != nil {
		shouldFill = *obj.Fill
	}
	if fillPaint == nil {
		fillPaint = fillColor
	}
	if shouldFill && fillPattern != nil {
		fp := p
		if clipPath != nil {
			fp = p.Copy()
			fp.Close()
			fp = fp.And(clipPath)
		}
		r.renderPattern(ctx, fillPattern, fillPatternColor, pageH, fp, ctm)
	} else if shouldFill && fillPaint != nil {
		ctx.SetFill(fillPaint)
		ctx.SetStrokeColor(canvas.Transparent)
		fp := p
		if clipPath != nil {
			fp = p.Copy()
			fp.Close()
			fp = fp.And(clipPath)
		}
		ctx.DrawPath(0, 0, fp)
	}
	shouldStroke := true
	if obj.Stroke != nil {
		shouldStroke = *obj.Stroke
	}
	if shouldStroke {
		if strokePaint == nil {
			strokePaint = strokeColor
		}
		if strokePaint == nil {
			strokePaint = colorWithAlpha(canvas.Black, obj.Alpha)
		}
		ctx.SetFillColor(canvas.Transparent)
		ctx.SetStroke(strokePaint)
		ctx.SetStrokeWidth(lineWidth)
		ctx.SetStrokeCapper(lineCap)
		ctx.SetStrokeJoiner(lineJoin)
		if len(dashPattern) > 0 {
			ctx.SetDashes(dashOffset, dashPattern...)
		}
		if clipPath != nil {
			sp := p.Copy()
			if len(dashPattern) > 0 {
				sp = sp.Dash(dashOffset, dashPattern...)
			}
			sp = sp.Stroke(lineWidth, lineCap, lineJoin, canvas.Tolerance)
			sp = sp.And(clipPath)
			ctx.SetFill(strokePaint)
			ctx.SetStrokeColor(canvas.Transparent)
			ctx.DrawPath(0, 0, sp)
		} else {
			ctx.DrawPath(0, 0, p)
		}
	}
	ctx.Pop()
}

// renderPattern 渲染图案填充
// 入参: ctx 画布上下文, pattern 图案对象, defaultColor 默认颜色, pageH 页面高度, clip 填充区域, parentCTM 父级CTM
func (r *Renderer) renderPattern(ctx *canvas.Context, pattern *Pattern, defaultColor color.Color, pageH float64, clip *canvas.Path, parentCTM Matrix) {
	if pattern == nil || clip == nil || len(pattern.CellContent.Objects) == 0 {
		return
	}
	xStep, yStep := pattern.XStep, pattern.YStep
	if xStep == 0 {
		xStep = pattern.Width
	}
	if yStep == 0 {
		yStep = pattern.Height
	}
	if xStep <= 0 || yStep <= 0 {
		return
	}
	patternCTM := parentCTM.Multiply(NewMatrix(pattern.CTM))
	invCTM, ok := patternCTM.Invert()
	if !ok {
		return
	}
	bounds := clip.FastBounds()
	points := [][2]float64{
		{bounds.X0, pageH - bounds.Y0},
		{bounds.X1, pageH - bounds.Y0},
		{bounds.X1, pageH - bounds.Y1},
		{bounds.X0, pageH - bounds.Y1},
	}
	minX, maxX := 0.0, 0.0
	minY, maxY := 0.0, 0.0
	for i, point := range points {
		x, y := invCTM.Transform(point[0], point[1])
		if i == 0 {
			minX, maxX = x, x
			minY, maxY = y, y
			continue
		}
		minX = math.Min(minX, x)
		maxX = math.Max(maxX, x)
		minY = math.Min(minY, y)
		maxY = math.Max(maxY, y)
	}
	startX := int(math.Floor(minX/xStep)) - 1
	endX := int(math.Ceil(maxX/xStep)) + 1
	startY := int(math.Floor(minY/yStep)) - 1
	endY := int(math.Ceil(maxY/yStep)) + 1
	for ix := startX; ix <= endX; ix++ {
		for iy := startY; iy <= endY; iy++ {
			tileCTM := patternCTM.Multiply(TranslationMatrix(float64(ix)*xStep, float64(iy)*yStep))
			for _, obj := range pattern.CellContent.Objects {
				r.renderObject(ctx, obj, pageH, defaultColor, defaultColor, 0, &tileCTM)
			}
		}
	}
}

// patternColor 获取图案单元默认颜色
// 入参: fillColor 填充颜色节点
// 返回: color.Color 默认颜色
func patternColor(fillColor *FillColor) color.Color {
	if fillColor == nil || strings.TrimSpace(fillColor.Value) == "" {
		return nil
	}
	return parseColorWithAlpha(fillColor.Value, fillColor.Alpha)
}

// renderText 渲染文本
// 入参: ctx 画布上下文, obj 文本对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, parentCTM 父级CTM
func (r *Renderer) renderText(ctx *canvas.Context, obj TextObject, pageH float64, defaultFill, defaultStroke color.Color, parentCTM *Matrix) {
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
	ctm := NewMatrix(obj.CTM)
	if parentCTM != nil {
		ctm = parentCTM.Multiply(ctm)
	}
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
	useTextMatrix := hasTextMatrix(ctm)
	if scale := ctm.YScale(); scale > 0 && !useTextMatrix {
		sizeMM *= scale
	}
	sizePt := sizeMM * 2.83465
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
	if weight >= 700 {
		fontStyle |= canvas.FontBold
	}
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
		if of.Bold {
			fontStyle |= canvas.FontBold
		}
		if of.Italic {
			fontStyle |= canvas.FontItalic
		}
	}
	ff := r.loadFont(fontID)
	if ff == nil {
		return
	}
	face := ff.Face(sizePt, fillPaint, fontStyle, canvas.FontNormal)
	glyphRunes := r.textObjectGlyphRunes(fontID, obj)
	codePos := 0
	for _, tc := range obj.TextCode {
		var runes []rune
		if tc.Index != "" {
			runes = r.parseIndexRunes(tc.Index, fontID)
		} else {
			runes = []rune(tc.Value)
		}
		dxs, dys := parseFloats(tc.DeltaX), parseFloats(tc.DeltaY)
		xs, ys := parseFloats(tc.X), parseFloats(tc.Y)
		cx, cy := 0.0, 0.0
		if len(xs) > 0 {
			cx = xs[0]
		}
		if len(ys) > 0 {
			cy = ys[0]
		}
		for i, run := range runes {
			str := string(run)
			if embeddedFont && tc.Index == "" && glyphRunes != nil {
				if mapped, ok := glyphRunes[codePos+i]; ok {
					str = string(mapped)
				}
			}
			if i < len(xs) {
				cx = xs[i]
			} else if i > 0 {
				if dx, ok := textDelta(dxs, i-1); ok {
					cx += dx
				} else if len(dys) == 0 {
					cx += face.TextWidth(str)
				}
			}
			if i < len(ys) {
				cy = ys[i]
			} else if i > 0 {
				if dy, ok := textDelta(dys, i-1); ok {
					cy += dy
				}
			}
			tx, ty := ctm.Transform(cx, cy)
			canvasX, canvasY := tx+bx, pageH-(ty+by)
			textWidth := face.TextWidth(str)
			glyphFillPaint := fillPaint
			if fillColorNode != nil && fillColorNode.AxialShd != nil {
				glyphFillPaint = parseFillPaint(fillColorNode, bx, by, pageH, canvasX, canvasY)
			}
			if glyphFillPaint != nil {
				ctx.SetFill(glyphFillPaint)
				drawGlyph := func(x, y float64) {
					if embeddedFont {
						path, width := face.ToPath(str)
						textWidth = width
						ctx.DrawPath(x, y, path)
					} else {
						textFace := face
						if fillColorNode != nil && fillColorNode.AxialShd != nil {
							textFace = ff.Face(sizePt, glyphFillPaint, fontStyle, canvas.FontNormal)
						}
						text := canvas.NewTextLine(textFace, str, canvas.Left)
						ctx.DrawText(x, y, text)
					}
				}
				if useTextMatrix {
					ctx.Push()
					ctx.Translate(canvasX, canvasY)
					ctx.ComposeView(textMatrix(ctm))
					drawGlyph(0, 0)
					if strings.Contains(obj.Decoration, "Underline") {
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
			if strings.Contains(obj.Decoration, "Underline") {
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

// textDelta 获取文本偏移量
// 入参: deltas 偏移量数组, index 偏移量索引
// 返回: float64 偏移量, bool 是否存在
func textDelta(deltas []float64, index int) (float64, bool) {
	if len(deltas) == 0 {
		return 0, false
	}
	if index < len(deltas) {
		return deltas[index], true
	}
	return deltas[len(deltas)-1], true
}

// hasTextMatrix 判断文本是否需要应用字形变换
// 入参: ctm 变换矩阵
// 返回: bool 是否需要变换
func hasTextMatrix(ctm Matrix) bool {
	const eps = 1e-9
	return math.Abs(ctm.a-1) > eps || math.Abs(ctm.b) > eps || math.Abs(ctm.c) > eps || math.Abs(ctm.d-1) > eps
}

// textMatrix 获取文本字形变换矩阵
// 入参: ctm OFD变换矩阵
// 返回: canvas.Matrix 画布变换矩阵
func textMatrix(ctm Matrix) canvas.Matrix {
	return canvas.Matrix{
		{ctm.a, -ctm.c, 0},
		{-ctm.b, ctm.d, 0},
	}
}

// loadFont 加载字体
// 入参: fontID 字体ID
// 返回: *canvas.FontFamily 字体族
func (r *Renderer) loadFont(fontID string) *canvas.FontFamily {
	if ff, ok := r.FontMap[fontID]; ok {
		return ff
	}
	var defaultFont *canvas.FontFamily
	if r.defaultFontLoaded {
		defaultFont = r.fontFamily
	}
	of, ok := r.Reader.fontCache[fontID]
	if !ok {
		return defaultFont
	}
	ff := canvas.NewFontFamily(of.FontName)
	if of.FontFile != "" {
		if fontData, err := r.Reader.ResData(of.FontFile); err == nil {
			if cidMap := getCFFCIDRuneMap(fontData); len(cidMap) > 0 {
				if r.FontCIDMap == nil {
					r.FontCIDMap = make(map[string]map[uint16]rune)
				}
				r.FontCIDMap[fontID] = cidMap
			}
			if _, fixedData, mapping, _, err := FixFontDataAggressive(fontData, true, true); err == nil {
				fontData = fixedData
				if mapping != nil {
					if r.FontGIDMap == nil {
						r.FontGIDMap = make(map[string]map[uint16]rune)
					}
					inv := make(map[uint16]rune)
					for k, v := range mapping {
						if k == packedGlyphRune(v) {
							inv[v] = k
						}
					}
					for k, v := range mapping {
						if _, ok := inv[v]; !ok {
							inv[v] = k
						}
					}
					r.FontGIDMap[fontID] = inv
				}
			}
			var fontStyle canvas.FontStyle
			if of.Bold {
				fontStyle |= canvas.FontBold
			}
			if of.Italic {
				fontStyle |= canvas.FontItalic
			}
			if err := ff.LoadFont(fontData, 0, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
			return nil
		}
		return nil
	}
	var fontStyle canvas.FontStyle
	if of.Bold {
		fontStyle |= canvas.FontBold
	}
	if of.Italic {
		fontStyle |= canvas.FontItalic
	}
	for _, dir := range r.fontDirs {
		for _, pattern := range fontFilePatterns(of.FontName, of.FamilyName) {
			matches := r.globFontFiles(dir, pattern)
			for _, m := range matches {
				if err := ff.LoadFontFile(m, fontStyle); err == nil {
					r.FontMap[fontID] = ff
					return ff
				}
			}
		}
	}
	for _, fsys := range r.fontFS {
		for _, pattern := range fontFilePatterns(of.FontName, of.FamilyName) {
			if matches, err := fs.Glob(fsys, pattern); err == nil {
				for _, m := range matches {
					resData, err := fs.ReadFile(fsys, m)
					if err == nil {
						if err := ff.LoadFont(resData, 0, fontStyle); err == nil {
							r.FontMap[fontID] = ff
							return ff
						}
					}
				}
			}
		}
	}
	if !canLoadSystemFonts() {
		for _, fsys := range r.fontFS {
			if matches, err := fs.Glob(fsys, "*"); err == nil {
				for _, m := range matches {
					resData, err := fs.ReadFile(fsys, m)
					if err == nil {
						if err := ff.LoadFont(resData, 0, fontStyle); err == nil {
							r.FontMap[fontID] = ff
							return ff
						}
					}
				}
			}
		}
		r.FontMap[fontID] = defaultFont
		return defaultFont
	}
	names := []string{of.FamilyName, of.FontName}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, targetName := range fontSystemNames(name) {
			if err := ff.LoadSystemFont(targetName, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
		}
		var sysFontDir string
		switch runtime.GOOS {
		case "linux":
			sysFontDir = `/usr/share/fonts`
		case "darwin":
			sysFontDir = `/Library/Fonts`
		default:
			sysFontDir = `C:\Windows\Fonts`
		}
		for _, pattern := range fontFilePatterns(name) {
			matches := r.globFontFiles(sysFontDir, pattern)
			for _, m := range matches {
				if err := ff.LoadFontFile(m, fontStyle); err == nil {
					r.FontMap[fontID] = ff
					return ff
				}
			}
		}
	}
	r.FontMap[fontID] = defaultFont
	return defaultFont
}

// globFontFiles 查找字体文件
// 入参: dir 目录, pattern 模式
// 返回: []string 文件列表
func (r *Renderer) globFontFiles(dir, pattern string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	var result []string
	for _, m := range matches {
		ext := strings.ToLower(filepath.Ext(m))
		if ext == ".ttf" || ext == ".otf" || ext == ".ttc" {
			result = append(result, m)
		}
	}
	return result
}

// textObjectFontID 获取文本对象字体ID
// 入参: text 文本对象
// 返回: string 字体ID
func (r *Renderer) textObjectFontID(text TextObject) string {
	fontID := text.Font
	if fontID == "" && text.DrawParam != "" {
		if dp := r.getDrawParam(text.DrawParam, nil); dp != nil && dp.Font != "" {
			fontID = dp.Font
		}
	}
	return fontID
}

// textObjectGlyphRunes 获取文本对象的字形映射
// 入参: fontID 字体ID, text 文本对象
// 返回: map[int]rune 文本位置到包装字体字符的映射
func (r *Renderer) textObjectGlyphRunes(fontID string, text TextObject) map[int]rune {
	if fontID == "" || len(text.CGTransform) == 0 {
		return nil
	}
	result := make(map[int]rune)
	for _, transform := range text.CGTransform {
		glyphs := parseInts(transform.Glyphs)
		count := len(glyphs)
		if transform.GlyphCount > 0 && transform.GlyphCount < count {
			count = transform.GlyphCount
		}
		if transform.CodeCount > 0 && transform.CodeCount < count {
			count = transform.CodeCount
		}
		if count == 0 {
			continue
		}
		if transform.CodeCount > 0 && transform.GlyphCount > 0 && transform.CodeCount != transform.GlyphCount {
			continue
		}
		for i := 0; i < count; i++ {
			if mapped, ok := r.fontGlyphRune(fontID, glyphs[i]); ok {
				result[transform.CodePosition+i] = mapped
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// fontGlyphRune 获取字形ID对应的包装字体字符
// 入参: fontID 字体ID, glyphID 字形ID或CID
// 返回: rune 包装字体字符, bool 是否存在
func (r *Renderer) fontGlyphRune(fontID string, glyphID int) (rune, bool) {
	if glyphID < 0 || glyphID > 0xFFFF {
		return 0, false
	}
	id := uint16(glyphID)
	if r.FontCIDMap != nil {
		if mapping := r.FontCIDMap[fontID]; mapping != nil {
			if mapped, ok := mapping[id]; ok {
				return mapped, true
			}
		}
	}
	if r.FontGIDMap != nil {
		if mapping := r.FontGIDMap[fontID]; mapping != nil {
			if mapped, ok := mapping[id]; ok {
				return mapped, true
			}
		}
	}
	return 0, false
}

// renderStamp 渲染印章
// 入参: ctx 画布上下文, s 印章对象, pageH 页面高度
func (r *Renderer) renderStamp(ctx *canvas.Context, s Stamp, pageH float64) {
	x, y, w, h := s.Box.X, s.Box.Y, s.Box.W, s.Box.H
	screenY := pageH - (y + h)
	if s.Type == "ofd" && len(s.Data) > 0 {
		reader, err := NewReader(bytes.NewReader(s.Data), int64(len(s.Data)))
		if err == nil {
			defer reader.Close()
			doc, err := reader.Doc()
			if err == nil {
				renderer := NewRenderer(reader)
				for _, pageRef := range doc.Pages.Page {
					content, err := reader.PageContent(pageRef)
					if err != nil {
						continue
					}
					sealBox, err := renderer.GetPageBox(content)
					if err != nil {
						continue
					}
					ctx.Push()
					ctx.Translate(x, screenY)
					ctx.Scale(w/sealBox.W, h/sealBox.H)
					renderer.renderPageToContext(ctx, content, false)
					ctx.Pop()
				}
				return
			}
		}
	}
	if len(s.Data) > 0 {
		img, _, err := image.Decode(bytes.NewReader(s.Data))
		if err == nil {
			ctx.Push()
			ctx.Translate(x, screenY)
			ctx.Scale(w/float64(img.Bounds().Dx()), h/float64(img.Bounds().Dy()))
			ctx.DrawImage(0, 0, img, canvas.DPMM(1.0))
			ctx.Pop()
			return
		}
	}
}

// buildPath 解析路径并返回Canvas Path
// 入参: obj 路径对象, pageH 页面高度, ctm 变换矩阵
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildPath(obj PathObject, pageH float64, ctm Matrix) *canvas.Path {
	bx, by := 0.0, 0.0
	if obj.Boundary != "" {
		if box, err := ParseBox(obj.Boundary); err == nil {
			bx, by = box.X, box.Y
		}
	}
	p := &canvas.Path{}
	tokens := strings.Fields(obj.AbbreviatedData)
	for i := 0; i < len(tokens); {
		cmd := tokens[i]
		i++
		switch cmd {
		case "M", "S":
			if i+1 < len(tokens) {
				x, _ := strconv.ParseFloat(tokens[i], 64)
				y, _ := strconv.ParseFloat(tokens[i+1], 64)
				tx, ty := ctm.Transform(x, y)
				p.MoveTo(tx+bx, pageH-(ty+by))
				i += 2
			}
		case "L":
			if i+1 < len(tokens) {
				x, _ := strconv.ParseFloat(tokens[i], 64)
				y, _ := strconv.ParseFloat(tokens[i+1], 64)
				tx, ty := ctm.Transform(x, y)
				p.LineTo(tx+bx, pageH-(ty+by))
				i += 2
			}
		case "B":
			if i+5 < len(tokens) {
				x1, _ := strconv.ParseFloat(tokens[i], 64)
				y1, _ := strconv.ParseFloat(tokens[i+1], 64)
				x2, _ := strconv.ParseFloat(tokens[i+2], 64)
				y2, _ := strconv.ParseFloat(tokens[i+3], 64)
				x3, _ := strconv.ParseFloat(tokens[i+4], 64)
				y3, _ := strconv.ParseFloat(tokens[i+5], 64)
				tx1, ty1 := ctm.Transform(x1, y1)
				tx2, ty2 := ctm.Transform(x2, y2)
				tx3, ty3 := ctm.Transform(x3, y3)
				p.CubeTo(tx1+bx, pageH-(ty1+by), tx2+bx, pageH-(ty2+by), tx3+bx, pageH-(ty3+by))
				i += 6
			}
		case "Q":
			if i+3 < len(tokens) {
				x1, _ := strconv.ParseFloat(tokens[i], 64)
				y1, _ := strconv.ParseFloat(tokens[i+1], 64)
				x2, _ := strconv.ParseFloat(tokens[i+2], 64)
				y2, _ := strconv.ParseFloat(tokens[i+3], 64)
				tx1, ty1 := ctm.Transform(x1, y1)
				tx2, ty2 := ctm.Transform(x2, y2)
				p.QuadTo(tx1+bx, pageH-(ty1+by), tx2+bx, pageH-(ty2+by))
				i += 4
			}
		case "A":
			if i+6 < len(tokens) {
				rx, _ := strconv.ParseFloat(tokens[i], 64)
				ry, _ := strconv.ParseFloat(tokens[i+1], 64)
				rot, _ := strconv.ParseFloat(tokens[i+2], 64)
				large, _ := strconv.ParseBool(tokens[i+3])
				sweep, _ := strconv.ParseBool(tokens[i+4])
				x, _ := strconv.ParseFloat(tokens[i+5], 64)
				y, _ := strconv.ParseFloat(tokens[i+6], 64)
				sx := math.Hypot(ctm.a, ctm.c)
				sy := math.Hypot(ctm.b, ctm.d)
				ctmRot := math.Atan2(ctm.b, ctm.a) * 180 / math.Pi
				tx, ty := ctm.Transform(x, y)
				sweep = !sweep
				p.ArcTo(rx*sx, ry*sy, -(rot + ctmRot), large, sweep, tx+bx, pageH-(ty+by))
				i += 7
			}
		case "C":
			p.Close()
		}
	}
	return p
}

// buildClipPath 构建裁剪路径
// 入参: clips 裁剪对象, pageH 页面高度, bx 边界X坐标, by 边界Y坐标, objectCTM 对象CTM
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildClipPath(clips *Clips, pageH float64, bx, by float64, objectCTM Matrix) *canvas.Path {
	if clips == nil {
		return nil
	}
	var p *canvas.Path
	for _, clip := range clips.Clip {
		var clipPath *canvas.Path
		for _, area := range clip.Area {
			areaCTM := NewMatrix(area.CTM)
			if clips.TransFlag {
				areaCTM = objectCTM.Multiply(areaCTM)
			}
			for _, pathObj := range area.Path {
				ctm := areaCTM.Multiply(NewMatrix(pathObj.CTM))
				cp := r.buildPath(pathObj, pageH, ctm)
				cp.Translate(bx, -by)
				cp.Close()
				if clipPath == nil {
					clipPath = cp
				} else {
					clipPath = clipPath.Or(cp)
				}
			}
		}
		if clipPath != nil {
			if p == nil {
				p = clipPath
			} else {
				p = p.And(clipPath)
			}
		}
	}
	return p
}

// buildTinyFillRectPath 构建微小填充矩形路径
// 入参: obj 路径对象, pageH 页面高度, ctm 变换矩阵, bx 边界X坐标, by 边界Y坐标
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildTinyFillRectPath(obj PathObject, pageH float64, ctm Matrix, bx, by float64) *canvas.Path {
	if obj.Fill == nil || !*obj.Fill || obj.Stroke == nil || *obj.Stroke {
		return nil
	}
	box, err := ParseBox(obj.Boundary)
	if err != nil || box.W <= 0 || box.H <= 0 || box.W > 0.6 || box.H > 0.6 {
		return nil
	}
	tokens := strings.Fields(obj.AbbreviatedData)
	if len(tokens) != 11 || tokens[0] != "M" || tokens[3] != "L" || tokens[6] != "L" || tokens[9] != "L" || tokens[10] != "C" {
		return nil
	}
	points := make([][2]float64, 0, 4)
	for i := 1; i < 10; i += 3 {
		x, errX := strconv.ParseFloat(tokens[i], 64)
		y, errY := strconv.ParseFloat(tokens[i+1], 64)
		if errX != nil || errY != nil {
			return nil
		}
		tx, ty := ctm.Transform(x, y)
		points = append(points, [2]float64{tx + bx, pageH - (ty + by)})
	}
	minX, maxX := points[0][0], points[0][0]
	minY, maxY := points[0][1], points[0][1]
	for _, point := range points[1:] {
		minX = math.Min(minX, point[0])
		maxX = math.Max(maxX, point[0])
		minY = math.Min(minY, point[1])
		maxY = math.Max(maxY, point[1])
	}
	expand := math.Min(box.W, box.H) * 0.08
	p := &canvas.Path{}
	p.MoveTo(minX-expand, minY-expand)
	p.LineTo(maxX+expand, minY-expand)
	p.LineTo(maxX+expand, maxY+expand)
	p.LineTo(minX-expand, maxY+expand)
	p.Close()
	return p
}

// parseIndexRunes 解析索引字形
// 入参: indexStr 索引字符串, fontID 字体ID
// 返回: []rune 字形列表
func (r *Renderer) parseIndexRunes(indexStr string, fontID string) []rune {
	var gids []int
	parts := strings.Fields(indexStr)
	for _, p := range parts {
		if strings.Contains(p, "-") {
			sub := strings.Split(p, "-")
			if len(sub) == 2 {
				start, _ := strconv.Atoi(sub[0])
				end, _ := strconv.Atoi(sub[1])
				for k := start; k <= end; k++ {
					gids = append(gids, k)
				}
			}
		} else {
			val, _ := strconv.Atoi(p)
			gids = append(gids, val)
		}
	}
	var res []rune
	for _, gid := range gids {
		if rVal, ok := r.fontGlyphRune(fontID, gid); ok {
			res = append(res, rVal)
			continue
		}
		res = append(res, rune(gid))
	}
	return res
}
