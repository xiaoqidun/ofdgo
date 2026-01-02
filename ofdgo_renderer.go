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
	Reader     *Reader
	DPI        float64
	fontFamily *canvas.FontFamily
	DrawParams map[string]*DrawParam
	FontMap    map[string]*canvas.FontFamily
	fontDirs   []string
	fontFS     []fs.FS
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
			r.renderLayer(ctx, layer, pageH, nil, nil, 0)
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
			r.renderLayer(ctx, layer, pageH, nil, nil, 0)
		}
	}
}

// renderLayer 渲染图层
// 入参: ctx 画布上下文, layer 图层对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽
func (r *Renderer) renderLayer(ctx *canvas.Context, layer Layer, pageH float64, defaultFill color.Color, defaultStroke color.Color, defaultLW float64) {
	if layer.DrawParam != "" {
		if dp := r.getDrawParam(layer.DrawParam, nil); dp != nil {
			if dp.LineWidth > 0 {
				defaultLW = dp.LineWidth
			}
			if dp.FillColor != nil {
				defaultFill = parseColor(dp.FillColor.Value)
			}
			if dp.StrokeColor != nil {
				defaultStroke = parseColor(dp.StrokeColor.Value)
			}
		}
	}
	for _, imgObj := range layer.ImageObject {
		r.renderImage(ctx, imgObj, pageH)
	}
	for _, pathObj := range layer.PathObject {
		r.renderPath(ctx, pathObj, pageH, defaultFill, defaultStroke, defaultLW)
	}
	for _, textObj := range layer.TextObject {
		r.renderText(ctx, textObj, pageH, defaultFill, defaultStroke)
	}
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
			if dp.FillColor != nil {
				merged.FillColor = dp.FillColor
			}
			if dp.StrokeColor != nil {
				merged.StrokeColor = dp.StrokeColor
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
// 入参: ctx 画布上下文, obj 图片对象, pageH 页面高度
func (r *Renderer) renderImage(ctx *canvas.Context, obj ImageObject, pageH float64) {
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
	tx, ty := ctm.Transform(0, 1)
	canvasX, canvasY := tx+box.X, pageH-(ty+box.Y)
	ctx.Push()
	ctx.Translate(canvasX, canvasY)
	ctx.Scale(ctm.a/imgW, ctm.d/imgH)
	ctx.DrawImage(0, 0, img, canvas.DPMM(1.0))
	ctx.Pop()
}

// renderPath 渲染路径
// 入参: ctx 画布上下文, obj 路径对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽
func (r *Renderer) renderPath(ctx *canvas.Context, obj PathObject, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64) {
	ctx.Push()
	bx, by := 0.0, 0.0
	if obj.Boundary != "" {
		if box, err := ParseBox(obj.Boundary); err == nil {
			bx, by = box.X, box.Y
		}
	}
	ctm := NewMatrix(obj.CTM)
	fillColor, strokeColor := defaultFill, defaultStroke
	if strokeColor == nil {
		strokeColor = canvas.Black
	}
	lineWidth := defaultLW
	if lineWidth == 0 {
		lineWidth = 0.353
	}
	if obj.DrawParam != "" {
		if dp := r.getDrawParam(obj.DrawParam, nil); dp != nil {
			if dp.LineWidth > 0 {
				lineWidth = dp.LineWidth
			}
			if dp.FillColor != nil {
				fillColor = parseColor(dp.FillColor.Value)
			}
			if dp.StrokeColor != nil {
				strokeColor = parseColor(dp.StrokeColor.Value)
			}
		}
	}
	if obj.LineWidth > 0 {
		lineWidth = obj.LineWidth
	}
	if obj.FillColor != nil {
		fillColor = parseColor(obj.FillColor.Value)
	}
	if obj.StrokeColor != nil {
		strokeColor = parseColor(obj.StrokeColor.Value)
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
		case "C":
			p.Close()
		}
	}
	if fillColor != nil {
		ctx.SetFillColor(fillColor)
		ctx.DrawPath(0, 0, p)
	}
	ctx.SetStrokeColor(strokeColor)
	ctx.SetStrokeWidth(lineWidth)
	ctx.DrawPath(0, 0, p)
	ctx.Pop()
}

// renderText 渲染文本
// 入参: ctx 画布上下文, obj 文本对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色
func (r *Renderer) renderText(ctx *canvas.Context, obj TextObject, pageH float64, defaultFill, defaultStroke color.Color) {
	ctx.Push()
	bx, by := 0.0, 0.0
	if obj.Boundary != "" {
		if box, err := ParseBox(obj.Boundary); err == nil {
			bx, by = box.X, box.Y
		}
	}
	ctm := NewMatrix(obj.CTM)
	sizeMM := obj.Size
	if sizeMM == 0 {
		sizeMM = 3.5
	}
	if scale := ctm.YScale(); scale > 0 {
		sizeMM *= scale
	}
	sizePt := sizeMM * 2.83465
	textColor := defaultFill
	if textColor == nil {
		textColor = canvas.Black
	}
	if obj.DrawParam != "" {
		if dp := r.getDrawParam(obj.DrawParam, nil); dp != nil && dp.FillColor != nil {
			textColor = parseColor(dp.FillColor.Value)
		}
	}
	if obj.FillColor != nil {
		textColor = parseColor(obj.FillColor.Value)
	}
	fontStyle := canvas.FontRegular
	if obj.Weight >= 700 {
		fontStyle |= canvas.FontBold
	}
	if obj.Italic {
		fontStyle |= canvas.FontItalic
	}
	if of, ok := r.Reader.fontCache[obj.Font]; ok {
		if of.Bold {
			fontStyle |= canvas.FontBold
		}
		if of.Italic {
			fontStyle |= canvas.FontItalic
		}
	}
	ff := r.loadFont(obj.Font)
	face := ff.Face(sizePt, textColor, fontStyle, canvas.FontNormal)
	for _, tc := range obj.TextCode {
		runes := []rune(tc.Value)
		dxs, dys := parseFloats(tc.DeltaX), parseFloats(tc.DeltaY)
		cx, cy := tc.X, tc.Y
		for i, run := range runes {
			str := string(run)
			if i > 0 {
				if i-1 < len(dxs) {
					cx += dxs[i-1]
				} else {
					cx += face.TextWidth(str)
				}
				if i-1 < len(dys) {
					cy += dys[i-1]
				}
			}
			tx, ty := ctm.Transform(cx, cy)
			canvasX, canvasY := tx+bx, pageH-(ty+by)
			text := canvas.NewTextLine(face, str, canvas.Left)
			ctx.DrawText(canvasX, canvasY, text)
			if strings.Contains(obj.Decoration, "Underline") {
				uw := sizeMM * 0.05
				if uw < 0.05 {
					uw = 0.05
				}
				ctx.SetStrokeWidth(uw)
				ctx.SetStrokeColor(textColor)
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
			fontStyle := canvas.FontRegular
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
		}
	}
	var fontStyle canvas.FontStyle
	if of.Bold {
		fontStyle |= canvas.FontBold
	}
	if of.Italic {
		fontStyle |= canvas.FontItalic
	}
	for _, dir := range r.fontDirs {
		matches, _ := filepath.Glob(filepath.Join(dir, of.FontName+"*"))
		for _, m := range matches {
			ext := strings.ToLower(filepath.Ext(m))
			if ext == ".ttf" || ext == ".otf" || ext == ".ttc" {
				if err := ff.LoadFontFile(m, fontStyle); err == nil {
					r.FontMap[fontID] = ff
					return ff
				}
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
		matches, _ := filepath.Glob(filepath.Join(winFontDir, "*"+targetName+"*"))
		if len(matches) == 0 {
			if targetName == "SimSun" {
				matches, _ = filepath.Glob(filepath.Join(winFontDir, "simsun.ttc"))
			} else if targetName == "KaiTi" {
				matches, _ = filepath.Glob(filepath.Join(winFontDir, "simkai.ttf"))
			} else if targetName == "SimHei" {
				matches, _ = filepath.Glob(filepath.Join(winFontDir, "simhei.ttf"))
			} else if targetName == "FangSong" {
				matches, _ = filepath.Glob(filepath.Join(winFontDir, "simfang.ttf"))
			}
		}
		for _, m := range matches {
			if err := ff.LoadFontFile(m, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
		}
	}
	return defaultFont
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
