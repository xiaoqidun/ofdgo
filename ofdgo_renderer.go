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
	fontFamily            *canvas.FontFamily
	DrawParams            map[string]*DrawParam
	CompositeGraphicUnits map[string]*CompositeGraphicUnit
	FontMap               map[string]*canvas.FontFamily
	FontGIDMap            map[string]map[uint16]rune
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
	box, err := r.GetPageBox(page)
	if err != nil {
		return err
	}
	pageH := box.H
	ctx.SetFillColor(canvas.White)
	ctx.DrawPath(0, 0, canvas.Rectangle(box.W, box.H))
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
				defaultFill = parseColorWithAlpha(dp.FillColor.Value, dp.FillColor.Alpha)
			}
			if dp.StrokeColor != nil {
				defaultStroke = parseColorWithAlpha(dp.StrokeColor.Value, dp.StrokeColor.Alpha)
			}
		}
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
	ctx.Push()
	currentCTM := NewMatrix(cgu.CTM)
	if parentCTM != nil {
		currentCTM = parentCTM.Multiply(currentCTM)
	}
	r.applyClips(ctx, cgu.Clips, pageH, &currentCTM)
	if cgu.ResourceID != "" {
		if ref, ok := r.CompositeGraphicUnits[cgu.ResourceID]; ok {
			r.renderCompositeGraphicUnit(ctx, *ref, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM)
		}
	}
	if cgu.DrawParam != "" {
		if dp := r.getDrawParam(cgu.DrawParam, nil); dp != nil {
			if dp.LineWidth > 0 {
				defaultLW = dp.LineWidth
			}
			if dp.FillColor != nil {
				defaultFill = parseColorWithAlpha(dp.FillColor.Value, dp.FillColor.Alpha)
			}
			if dp.StrokeColor != nil {
				defaultStroke = parseColorWithAlpha(dp.StrokeColor.Value, dp.StrokeColor.Alpha)
			}
		}
	}
	for _, imgObj := range cgu.ImageObject {
		r.renderImage(ctx, imgObj, pageH, &currentCTM)
	}
	for _, pathObj := range cgu.PathObject {
		r.renderPath(ctx, pathObj, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM)
	}
	for _, textObj := range cgu.TextObject {
		r.renderText(ctx, textObj, pageH, defaultFill, defaultStroke, &currentCTM)
	}
	for _, subCgu := range cgu.CompositeGraphicUnit {
		r.renderCompositeGraphicUnit(ctx, subCgu, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM)
	}
	ctx.Pop()
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
	sysFonts := []string{
		"SimHei", "Microsoft YaHei", "SimSun", "KaiTi", "FangSong",
		"Arial", "Segoe UI", "Times New Roman",
	}
	for _, name := range sysFonts {
		if err := r.fontFamily.LoadSystemFont(name, canvas.FontRegular); err == nil {
			break
		}
	}
}

// renderImage 渲染图片
// 入参: ctx 画布上下文, obj 图片对象, pageH 页面高度, parentCTM 父级CTM
func (r *Renderer) renderImage(ctx *canvas.Context, obj ImageObject, pageH float64, parentCTM *Matrix) {
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
	r.applyClips(ctx, obj.Clips, pageH, &ctm)
	ctx.Translate(canvasX, canvasY)
	ctx.Scale(ctm.a/imgW, ctm.d/imgH)
	ctx.DrawImage(0, 0, img, canvas.DPMM(1.0))
	ctx.Pop()
}

// renderPath 渲染路径
// 入参: ctx 画布上下文, obj 路径对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM
func (r *Renderer) renderPath(ctx *canvas.Context, obj PathObject, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix) {
	ctx.Push()
	ctm := NewMatrix(obj.CTM)
	if parentCTM != nil {
		ctm = parentCTM.Multiply(ctm)
	}
	r.applyClips(ctx, obj.Clips, pageH, &ctm)
	fillColor, strokeColor := defaultFill, defaultStroke
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
				fillColor = parseColorWithAlpha(dp.FillColor.Value, dp.FillColor.Alpha)
			}
			if dp.StrokeColor != nil {
				strokeColor = parseColorWithAlpha(dp.StrokeColor.Value, dp.StrokeColor.Alpha)
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
		fillColor = parseColorWithAlpha(obj.FillColor.Value, obj.FillColor.Alpha)
	}
	if obj.StrokeColor != nil {
		strokeColor = parseColorWithAlpha(obj.StrokeColor.Value, obj.StrokeColor.Alpha)
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
	p := r.buildPath(obj, pageH, ctm)
	shouldFill := true
	if obj.Fill != nil {
		shouldFill = *obj.Fill
	}
	if shouldFill && fillColor != nil {
		ctx.SetFillColor(fillColor)
		ctx.DrawPath(0, 0, p)
	}
	shouldStroke := true
	if obj.Stroke != nil {
		shouldStroke = *obj.Stroke
	}
	if shouldStroke {
		if strokeColor == nil {
			strokeColor = color.Transparent
		}
		ctx.SetStrokeColor(strokeColor)
		ctx.SetStrokeWidth(lineWidth)
		ctx.SetStrokeCapper(lineCap)
		ctx.SetStrokeJoiner(lineJoin)
		if len(dashPattern) > 0 {
			ctx.SetDashes(dashOffset, dashPattern...)
		}
		ctx.DrawPath(0, 0, p)
	}
	ctx.Pop()
}

// renderText 渲染文本
// 入参: ctx 画布上下文, obj 文本对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, parentCTM 父级CTM
func (r *Renderer) renderText(ctx *canvas.Context, obj TextObject, pageH float64, defaultFill, defaultStroke color.Color, parentCTM *Matrix) {
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
	r.applyClips(ctx, obj.Clips, pageH, &ctm)
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
	if scale := ctm.YScale(); scale > 0 {
		sizeMM *= scale
	}
	sizePt := sizeMM * 2.83465
	fillColor := defaultFill
	if fillColor == nil {
		fillColor = canvas.Black
	}
	if dp != nil && dp.FillColor != nil {
		fillColor = parseColorWithAlpha(dp.FillColor.Value, dp.FillColor.Alpha)
	}
	if obj.FillColor != nil {
		fillColor = parseColorWithAlpha(obj.FillColor.Value, obj.FillColor.Alpha)
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
	fontID := obj.Font
	if fontID == "" && dp != nil && dp.Font != "" {
		fontID = dp.Font
	}
	if of, ok := r.Reader.fontCache[fontID]; ok {
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
	face := ff.Face(sizePt, fillColor, fontStyle, canvas.FontNormal)
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
			if i < len(xs) {
				cx = xs[i]
			} else if i > 0 {
				if i-1 < len(dxs) {
					cx += dxs[i-1]
				} else if len(dys) == 0 {
					cx += face.TextWidth(str)
				}
			}
			if i < len(ys) {
				cy = ys[i]
			} else if i > 0 {
				if i-1 < len(dys) {
					cy += dys[i-1]
				}
			}
			tx, ty := ctm.Transform(cx, cy)
			canvasX, canvasY := tx+bx, pageH-(ty+by)
			text := canvas.NewTextLine(face, str, canvas.Left)
			if fillColor != nil {
				ctx.SetFillColor(fillColor)
				ctx.DrawText(canvasX, canvasY, text)
			}
			if strings.Contains(obj.Decoration, "Underline") {
				uw := sizeMM * 0.05
				ctx.SetStrokeWidth(uw)
				ctx.SetStrokeColor(fillColor)
				off := sizeMM * 0.1
				ctx.MoveTo(canvasX, canvasY-off)
				ctx.LineTo(canvasX+face.TextWidth(str), canvasY-off)
				ctx.Stroke()
			}
		}
	}
	ctx.Pop()
}

// loadFont 加载字体
// 入参: fontID 字体ID
// 返回: *canvas.FontFamily 字体族
func (r *Renderer) loadFont(fontID string) *canvas.FontFamily {
	if ff, ok := r.FontMap[fontID]; ok {
		return ff
	}
	defaultFont := r.fontFamily
	of, ok := r.Reader.fontCache[fontID]
	if !ok {
		return defaultFont
	}
	ff := canvas.NewFontFamily(of.FontName)
	if of.FontFile != "" {
		if fontData, err := r.Reader.ResData(of.FontFile); err == nil {
			if _, fixedData, mapping, _, err := FixFontDataAggressive(fontData, true, true); err == nil {
				fontData = fixedData
				if mapping != nil {
					if r.FontGIDMap == nil {
						r.FontGIDMap = make(map[string]map[uint16]rune)
					}
					inv := make(map[uint16]rune)
					for k, v := range mapping {
						inv[v] = k
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
		matches := r.globFontFiles(dir, of.FontName+"*")
		for _, m := range matches {
			if err := ff.LoadFontFile(m, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
		}
	}
	for _, fsys := range r.fontFS {
		if matches, err := fs.Glob(fsys, of.FontName+"*"); err == nil {
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
	names := []string{of.FamilyName, of.FontName}
	aliases := map[string]string{
		"simhei":          "SimHei",
		"黑体":              "SimHei",
		"microsoft yahei": "Microsoft YaHei",
		"微软雅黑":            "Microsoft YaHei",
		"simsun":          "SimSun",
		"宋体":              "SimSun",
		"kaiti":           "KaiTi",
		"楷体":              "KaiTi",
		"fangsong":        "FangSong",
		"仿宋":              "FangSong",
		"arial":           "Arial",
		"segoe ui":        "Segoe UI",
		"times new roman": "Times New Roman",
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		targetName := name
		lower := strings.ToLower(name)
		if mapped, ok := aliases[lower]; ok {
			targetName = mapped
		} else {
			for k, v := range aliases {
				if strings.Contains(lower, k) {
					targetName = v
					break
				}
			}
		}
		if err := ff.LoadSystemFont(targetName, fontStyle); err == nil {
			r.FontMap[fontID] = ff
			return ff
		}
		if targetName != name {
			if err := ff.LoadSystemFont(name, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
		}
		winFontDir := `C:\Windows\Fonts`
		matches := r.globFontFiles(winFontDir, "*"+targetName+"*")
		if len(matches) == 0 {
			switch targetName {
			case "SimSun":
				matches = r.globFontFiles(winFontDir, "simsun.ttc")
			case "KaiTi":
				matches = r.globFontFiles(winFontDir, "simkai.ttf")
			case "SimHei":
				matches = r.globFontFiles(winFontDir, "simhei.ttf")
			case "FangSong":
				matches = r.globFontFiles(winFontDir, "simfang.ttf")
			}
		}
		for _, m := range matches {
			if err := ff.LoadFontFile(m, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
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
					renderer.RenderPageToContext(ctx, content)
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
	ctx.Push()
	ctx.SetStrokeColor(canvas.Red)
	ctx.SetStrokeWidth(0.5)
	ctx.SetFillColor(canvas.Transparent)
	ctx.DrawPath(x, screenY, canvas.Rectangle(w, h))
	ctx.SetFillColor(canvas.Red)
	fontSize := 3.0
	if r.fontFamily != nil {
		font := r.fontFamily.Face(fontSize*2.83465, canvas.Red, canvas.FontRegular, canvas.FontNormal)
		ctx.DrawText(x+w/2-font.TextWidth("Signature")/2, screenY+h/2-fontSize/2, canvas.NewTextLine(font, "Signature", canvas.Left))
	}
	ctx.Pop()
}

// applyClips 应用裁剪
// 入参: ctx 画布上下文, clips 裁剪对象, pageH 页面高度, parentCTM 父级CTM
func (r *Renderer) applyClips(ctx *canvas.Context, clips *Clips, pageH float64, parentCTM *Matrix) {
	if clips == nil {
		return
	}
	for _, clip := range clips.Clip {
		for _, area := range clip.Area {
			for _, pathObj := range area.Path {
				clipCTM := NewMatrix(pathObj.CTM)
				if parentCTM != nil {
					clipCTM = parentCTM.Multiply(clipCTM)
				}
				r.buildPath(pathObj, pageH, clipCTM)
			}
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
	mapping := r.FontGIDMap[fontID]
	var res []rune
	for _, gid := range gids {
		if mapping != nil {
			if rVal, ok := mapping[uint16(gid)]; ok {
				res = append(res, rVal)
				continue
			}
		}
		res = append(res, rune(gid))
	}
	return res
}
