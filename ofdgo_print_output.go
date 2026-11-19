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
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"maps"
	"math"
	"slices"
	"strings"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
)

// RenderSVG 输出一个完整纸面的独立SVG，字体和图片内嵌，不依赖界面资源
// 入参: ctx 输出上下文, side 零基纸面索引, writer 输出流
// 返回: error 读取、后端、写入或取消错误；失败时调用方丢弃部分输出
func (p *PrintDocument) RenderSVG(ctx context.Context, side int, writer io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.renderPrintSVG(ctx, side, writer)
}

// RenderImage 按指定DPI输出一个纸面的独立白底图像，dpi为0时采用任务配置
// 入参: ctx 输出上下文, side 零基纸面索引, dpi 输出分辨率
// 返回: image.Image 纸面图像, error 配置、读取、后端或取消错误
func (p *PrintDocument) RenderImage(ctx context.Context, side int, dpi float64) (image.Image, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.renderPrintImage(ctx, side, dpi)
}

// WriteHTML 输出不含应用脚本的独立打印文档，每个纸面按毫米尺寸分页
// 入参: ctx 输出上下文, writer 输出流, title 文档标题
// 返回: error 输出或取消错误；调用方在字体和图片加载后触发打印
func (p *PrintDocument) WriteHTML(ctx context.Context, writer io.Writer, title string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.printSide(ctx, 0); err != nil {
		return err
	}
	if writer == nil {
		return fmt.Errorf("missing print writer")
	}
	out := convertWriter{ctx: ctx, writer: writer, count: new(int64)}
	if _, err := fmt.Fprintf(out, "<!doctype html><html><head><meta charset=\"utf-8\"><title>%s</title><style>html,body{margin:0;padding:0;background:white}*{box-sizing:border-box}section{break-after:page;overflow:hidden;print-color-adjust:exact;-webkit-print-color-adjust:exact}section:last-child{break-after:auto}svg.sheet{display:block;width:100%%;height:100%%}@media screen{section{margin:12px auto;box-shadow:0 0 4px #888}}</style><style>", html.EscapeString(title)); err != nil {
		return err
	}
	for i, side := range p.layout.Sides {
		if _, err := fmt.Fprintf(out, "@page s%d{size:%gmm %gmm;margin:0}.s%d{page:s%d;width:%gmm;height:%gmm}", i, side.Width, side.Height, i, i, side.Width, side.Height); err != nil {
			return err
		}
	}
	if p.options.Raster || p.options.Grayscale {
		if _, err := io.WriteString(out, "</style></head><body>"); err != nil {
			return err
		}
		for i := range p.layout.Sides {
			if _, err := fmt.Fprintf(out, `<section class="s%d">`, i); err != nil {
				return err
			}
			var svg bytes.Buffer
			if err := p.renderPrintSVG(ctx, i, &svg); err != nil {
				return err
			}
			body := strings.Replace(svg.String(), "<svg ", `<svg class="sheet" `, 1)
			if _, err := io.WriteString(out, body+"</section>"); err != nil {
				return err
			}
		}
		_, err := io.WriteString(out, "</body></html>")
		return err
	}
	return p.writeVectorPrintHTML(ctx, out)
}

// WritePDF 将相同打印布局写成矢量PDF；显式图像或灰度模式按任务DPI输出
// 入参: ctx 输出上下文, writer 输出流
// 返回: error 后端、读取、写入或取消错误；失败时调用方丢弃部分输出
func (p *PrintDocument) WritePDF(ctx context.Context, writer io.Writer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.printSide(ctx, 0); err != nil {
		return err
	}
	if writer == nil {
		return fmt.Errorf("missing print writer")
	}
	sheets := make([]codecpdf.PrintSheet, len(p.layout.Sides))
	raster := p.options.Raster || p.options.Grayscale
	for i, side := range p.layout.Sides {
		sheets[i] = codecpdf.PrintSheet{Width: side.Width, Height: side.Height}
		if raster {
			continue
		}
		sheets[i].Lines = p.printLines(side)
		sheets[i].Label, sheets[i].LabelX, sheets[i].LabelY = side.Label, side.LabelX, side.LabelY
		for _, placement := range side.Placements {
			sheets[i].Placements = append(sheets[i].Placements, codecpdf.PrintPlacement{Page: placement.Page, Matrix: placement.Transform, Clip: [4]float64{placement.Clip.X, placement.Clip.Y, placement.Clip.W, placement.Clip.H}})
		}
	}
	if raster {
		return codecpdf.WritePrintImages(ctx, writer, sheets, func(i int) (image.Image, error) { return p.renderPrintImage(ctx, i, p.options.DPI) })
	}
	var indices []int
	for _, side := range p.layout.Sides {
		for _, placement := range side.Placements {
			indices = append(indices, placement.Page)
		}
	}
	slices.Sort(indices)
	indices = slices.Compact(indices)
	if len(indices) == 0 {
		return codecpdf.WritePrintPDF(ctx, writer, nil, 0, sheets)
	}
	var source editorSnapshotBuffer
	defer func() { _ = source.Close() }()
	if err := p.renderer.RenderToMultiPagePDF(&source, indices...); err != nil {
		return err
	}
	for i := range sheets {
		for j := range sheets[i].Placements {
			sheets[i].Placements[j].Page, _ = slices.BinarySearch(indices, sheets[i].Placements[j].Page)
		}
	}
	return codecpdf.WritePrintPDF(ctx, writer, &source, source.size, sheets)
}

// printSharedImageKey 按图片资源名与声明宽高唯一标识可跨页复用的图片定义
type printSharedImageKey struct {
	name   string
	width  string
	height string
}

// printSharedImageDef 保存首张纸面SVG中待输出的共享图片定义
type printSharedImageDef struct {
	id     string
	width  string
	height string
	url    string
}

// svgTagAttr 保存单个SVG标签属性的名称、值与引号字符
type svgTagAttr struct {
	name  string
	val   string
	quote byte
}

// printSide 校验任务及纸面索引，并设置当前输出上下文
// 入参: ctx 输出上下文, index 零基纸面索引
// 返回: PrintSide 当前纸面, error 状态、索引或取消错误
func (p *PrintDocument) printSide(ctx context.Context, index int) (PrintSide, error) {
	if ctx == nil {
		return PrintSide{}, fmt.Errorf("nil print context")
	}
	if err := ctx.Err(); err != nil {
		return PrintSide{}, err
	}
	if p.closed {
		return PrintSide{}, fmt.Errorf("print document is closed")
	}
	if index < 0 || index >= len(p.layout.Sides) {
		return PrintSide{}, fmt.Errorf("print side outside layout")
	}
	p.renderer.OutputContext, p.renderer.pageContext = ctx, ctx
	return p.layout.Sides[index], nil
}

// renderPrintSVG 按统一布局输出矢量或显式光栅纸面
// 入参: ctx 输出上下文, index 零基纸面索引, writer 输出流
// 返回: error 渲染、写入或取消错误
func (p *PrintDocument) renderPrintSVG(ctx context.Context, index int, writer io.Writer) error {
	side, err := p.printSide(ctx, index)
	if err != nil {
		return err
	}
	if writer == nil {
		return fmt.Errorf("missing print writer")
	}
	out := bufio.NewWriter(convertWriter{ctx: ctx, writer: writer, count: new(int64)})
	fmt.Fprintf(out, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="%gmm" height="%gmm" viewBox="0 0 %g %g"><rect width="100%%" height="100%%" fill="white"/>`, side.Width, side.Height, side.Width, side.Height)
	if p.options.Raster || p.options.Grayscale {
		img, err := p.renderPrintImage(ctx, index, p.options.DPI)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, `<image width="%g" height="%g" xlink:href="data:image/png;base64,`, side.Width, side.Height)
		encoder := base64.NewEncoder(base64.StdEncoding, out)
		if err := png.Encode(encoder, img); err != nil {
			return err
		}
		if err := encoder.Close(); err != nil {
			return err
		}
		out.WriteString(`"/>`)
	} else {
		for i, placement := range side.Placements {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := p.pages.load(ctx, placement.Page, func() ([]byte, int, error) {
				page, err := p.renderer.Reader.PageContentByIndexContext(ctx, placement.Page)
				if err != nil {
					return nil, 0, err
				}
				var output bytes.Buffer
				if err := p.renderer.RenderToSVG(page, &output); err != nil {
					return nil, 0, err
				}
				return output.Bytes(), output.Cap(), nil
			})
			if err != nil {
				return err
			}
			c, m := placement.Clip, placement.Transform
			fmt.Fprintf(out, `<defs><clipPath id="print%d"><rect x="%g" y="%g" width="%g" height="%g"/></clipPath></defs><g clip-path="url(#print%d)"><image width="%g" height="%g" transform="matrix(%g %g %g %g %g %g)" xlink:href="data:image/svg+xml;base64,`, i, c.X, c.Y, c.W, c.H, i, placement.Width, placement.Height, m[0], m[1], m[2], m[3], m[4], m[5])
			encoder := base64.NewEncoder(base64.StdEncoding, out)
			if _, err := encoder.Write(data); err != nil {
				return err
			}
			if err := encoder.Close(); err != nil {
				return err
			}
			out.WriteString(`"/></g>`)
		}
		for _, line := range p.printLines(side) {
			fmt.Fprintf(out, `<path d="M %g %g L %g %g" fill="none" stroke="black" stroke-width="0.15"/>`, line[0], line[1], line[2], line[3])
		}
		if side.Label != "" {
			fmt.Fprintf(out, `<text x="%g" y="%g" font-family="sans-serif" font-size="2.8" fill="black">%s</text>`, side.LabelX, side.LabelY, html.EscapeString(side.Label))
		}
	}
	out.WriteString(`</svg>`)
	return out.Flush()
}

// renderPrintImage 仅保留当前纸面及单个源页图像，不积累整份文档像素
// 入参: ctx 输出上下文, index 零基纸面索引, dpi 输出分辨率
// 返回: image.Image 白底纸面图像, error 渲染或取消错误
func (p *PrintDocument) renderPrintImage(ctx context.Context, index int, dpi float64) (image.Image, error) {
	side, err := p.printSide(ctx, index)
	if err != nil {
		return nil, err
	}
	if dpi == 0 {
		dpi = p.options.DPI
	}
	if !printPositive(dpi) || dpi > 2400 {
		return nil, fmt.Errorf("invalid print DPI")
	}
	scale := dpi / 25.4
	w, h, err := (&RasterPage{Width: side.Width, Height: side.Height, DPI: dpi}).PixelSize()
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	for _, placement := range side.Placements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := p.renderer.Reader.PageContentByIndexContext(ctx, placement.Page)
		if err != nil {
			return nil, err
		}
		view := *p.renderer
		m := placement.Transform
		view.DPI = dpi * math.Hypot(m[0], m[1])
		img, err := view.RenderToImage(page)
		if err != nil {
			return nil, err
		}
		px, py := placement.Width/float64(img.Bounds().Dx()), placement.Height/float64(img.Bounds().Dy())
		matrix := f64.Aff3{m[0] * px * scale, m[2] * py * scale, m[4] * scale, m[1] * px * scale, m[3] * py * scale, m[5] * scale}
		clip := placement.Clip
		rect := image.Rect(int(math.Ceil(clip.X*scale)), int(math.Ceil(clip.Y*scale)), int(math.Floor((clip.X+clip.W)*scale)), int(math.Floor((clip.Y+clip.H)*scale))).Intersect(dst.Bounds())
		if !rect.Empty() {
			for top := rect.Min.Y; top < rect.Max.Y; top += 128 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				tile := image.Rect(rect.Min.X, top, rect.Max.X, min(top+128, rect.Max.Y))
				xdraw.CatmullRom.Transform(dst.SubImage(tile).(*image.RGBA), matrix, img, img.Bounds(), draw.Over, nil)
			}
		}
	}
	for _, line := range p.printLines(side) {
		x0, y0, x1, y1 := line[0]*scale, line[1]*scale, line[2]*scale, line[3]*scale
		steps := max(1, int(math.Ceil(math.Max(math.Abs(x1-x0), math.Abs(y1-y0)))))
		for i := 0; i <= steps; i++ {
			t := float64(i) / float64(steps)
			dst.Set(int(x0+(x1-x0)*t), int(y0+(y1-y0)*t), color.Black)
		}
	}
	if side.Label != "" {
		label := image.NewRGBA(image.Rect(0, 0, len(side.Label)*7, 13))
		(&font.Drawer{Dst: label, Src: image.Black, Face: basicfont.Face7x13, Dot: fixed.P(0, 11)}).DrawString(side.Label)
		x, y := int(side.LabelX*scale), int((side.LabelY-2.8)*scale)
		width, height := int(math.Round(float64(label.Bounds().Dx())*2.8/13*scale)), max(1, int(math.Round(2.8*scale)))
		xdraw.CatmullRom.Scale(dst, image.Rect(x, y, x+max(1, width), y+height), label, label.Bounds(), draw.Over, nil)
	}
	if p.options.Grayscale {
		for y := 0; y < h; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := 0; x < w; x++ {
				i := dst.PixOffset(x, y)
				v := uint8((uint32(dst.Pix[i])*19595 + uint32(dst.Pix[i+1])*38470 + uint32(dst.Pix[i+2])*7471 + 32768) >> 16)
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2] = v, v, v
			}
		}
	}
	return dst, ctx.Err()
}

// printLines 生成纸面边框和位于可打印区域内的裁切标记
// 入参: side 当前输出纸面
// 返回: [][4]float64 毫米坐标线段
func (p *PrintDocument) printLines(side PrintSide) [][4]float64 {
	var result [][4]float64
	for _, placement := range side.Placements {
		b := placement.Clip
		if p.options.Borders {
			result = append(result, [4]float64{b.X, b.Y, b.X + b.W, b.Y}, [4]float64{b.X, b.Y, b.X, b.Y + b.H}, [4]float64{b.X + b.W, b.Y, b.X + b.W, b.Y + b.H}, [4]float64{b.X, b.Y + b.H, b.X + b.W, b.Y + b.H})
		}
		if p.options.CropMarks {
			limits := b
			m := placement.Transform
			x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for _, point := range [][2]float64{{0, 0}, {placement.Width, 0}, {0, placement.Height}, {placement.Width, placement.Height}} {
				x, y := m[0]*point[0]+m[2]*point[1]+m[4], m[1]*point[0]+m[3]*point[1]+m[5]
				x0, y0, x1, y1 = math.Min(x0, x), math.Min(y0, y), math.Max(x1, x), math.Max(y1, y)
			}
			if side.TileColumns > 0 {
				if side.TileColumn > 1 {
					b.X += p.options.Overlap / 2
					b.W -= p.options.Overlap / 2
				}
				if side.TileColumn < side.TileColumns {
					b.W -= p.options.Overlap / 2
				}
				if side.TileRow > 1 {
					b.Y += p.options.Overlap / 2
					b.H -= p.options.Overlap / 2
				}
				if side.TileRow < side.TileRows {
					b.H -= p.options.Overlap / 2
				}
			}
			x0, y0, x1, y1 = math.Max(x0, b.X), math.Max(y0, b.Y), math.Min(x1, b.X+b.W), math.Min(y1, b.Y+b.H)
			b = Box{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
			if b.W <= 0 || b.H <= 0 {
				continue
			}
			for _, x := range []float64{b.X, b.X + b.W} {
				for _, y := range []float64{b.Y, b.Y + b.H} {
					dx, dy, roomX, roomY := -1.0, -1.0, x-limits.X, y-limits.Y
					if x > b.X {
						dx = 1
						roomX = limits.X + limits.W - x
					}
					if y > b.Y {
						dy = 1
						roomY = limits.Y + limits.H - y
					}
					if roomX > 0.7 {
						result = append(result, [4]float64{x + dx*0.5, y, x + dx*math.Min(3.5, roomX-0.1), y})
					}
					if roomY > 0.7 {
						result = append(result, [4]float64{x, y + dy*0.5, x, y + dy*math.Min(3.5, roomY-0.1)})
					}
				}
			}
		}
	}
	return result
}

// writeVectorPrintHTML 以跨页共享单份字体和内联SVG输出矢量打印文档，避免多层Base64膨胀
// 入参: ctx 输出上下文, out 输出流
// 返回: error 渲染或写入错误
func (p *PrintDocument) writeVectorPrintHTML(ctx context.Context, out io.Writer) error {
	emitted := make(map[string]bool)
	pageFragments := make(map[int]string)
	pageBoxes := make(map[int]Box)
	imageURLs := make(map[string]string)
	for _, side := range p.layout.Sides {
		for _, placement := range side.Placements {
			if _, exists := pageFragments[placement.Page]; exists {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			page, err := p.renderer.Reader.PageContentByIndexContext(ctx, placement.Page)
			if err != nil {
				return err
			}
			box, err := p.renderer.GetPageBox(page)
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			res, err := p.renderer.RenderPreview(page, &buf, PreviewOptions{KnownResources: maps.Clone(emitted)})
			if err != nil {
				return err
			}
			for _, f := range res.Fonts {
				if !emitted[f.Name] {
					emitted[f.Name] = true
					if _, err := fmt.Fprintf(out, "@font-face{font-family:'%s';font-style:%s;font-weight:%d;src:url(data:font/sfnt;base64,%s)}.%s{font-family:'%s'!important}", f.Name, f.Style, f.Weight, base64.StdEncoding.EncodeToString(f.Data), f.Name, f.Name); err != nil {
						return err
					}
				}
			}
			for _, img := range res.Images {
				if !emitted[img.Name] {
					emitted[img.Name] = true
					imageURLs[img.Name] = "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
				}
			}
			pageBoxes[placement.Page] = box
			pageFragments[placement.Page] = stripSVGOuterTag(buf.String())
		}
	}
	pageImageKeys := make(map[int][]printSharedImageKey, len(pageFragments))
	for pageIdx, fragment := range pageFragments {
		pageImageKeys[pageIdx] = collectSVGImageKeys(fragment, imageURLs)
	}
	imageUseCounts := make(map[printSharedImageKey]int)
	for _, side := range p.layout.Sides {
		for _, placement := range side.Placements {
			for _, key := range pageImageKeys[placement.Page] {
				imageUseCounts[key]++
			}
		}
	}
	var sharedKeys []printSharedImageKey
	for key, count := range imageUseCounts {
		if count > 1 {
			sharedKeys = append(sharedKeys, key)
		}
	}
	slices.SortFunc(sharedKeys, func(a, b printSharedImageKey) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		if c := strings.Compare(a.width, b.width); c != 0 {
			return c
		}
		return strings.Compare(a.height, b.height)
	})
	sharedImages := make(map[printSharedImageKey]string, len(sharedKeys))
	sharedDefs := make([]printSharedImageDef, len(sharedKeys))
	for i, key := range sharedKeys {
		id := fmt.Sprintf("shared_img_%d", i)
		sharedImages[key] = id
		sharedDefs[i] = printSharedImageDef{id: id, width: key.width, height: key.height, url: imageURLs[key.name]}
	}
	if _, err := io.WriteString(out, "</style></head><body>"); err != nil {
		return err
	}
	for sIdx, side := range p.layout.Sides {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, `<section class="s%d"><svg class="sheet" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="%gmm" height="%gmm" viewBox="0 0 %g %g">`, sIdx, side.Width, side.Height, side.Width, side.Height); err != nil {
			return err
		}
		if sIdx == 0 && len(sharedDefs) != 0 {
			if _, err := io.WriteString(out, `<defs>`); err != nil {
				return err
			}
			for _, def := range sharedDefs {
				if _, err := fmt.Fprintf(out, `<image id="%s" width="%s" height="%s" href="%s" xlink:href="%s"/>`, def.id, def.width, def.height, def.url, def.url); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(out, `</defs>`); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(out, `<rect width="100%" height="100%" fill="white"/>`); err != nil {
			return err
		}
		for pIdx, placement := range side.Placements {
			c, m := placement.Clip, placement.Transform
			box := pageBoxes[placement.Page]
			prefix := fmt.Sprintf("s%dp%d_", sIdx, pIdx)
			fragment := namespaceSVGFragment(pageFragments[placement.Page], prefix, imageURLs, sharedImages)
			clipID := fmt.Sprintf("pclip_%d_%d", sIdx, pIdx)
			if _, err := fmt.Fprintf(out, `<defs><clipPath id="%s"><rect x="%g" y="%g" width="%g" height="%g"/></clipPath></defs><g clip-path="url(#%s)"><g transform="matrix(%g %g %g %g %g %g)"><svg width="%g" height="%g" viewBox="0 0 %g %g" overflow="hidden">%s</svg></g></g>`, clipID, c.X, c.Y, c.W, c.H, clipID, m[0], m[1], m[2], m[3], m[4], m[5], placement.Width, placement.Height, box.W, box.H, fragment); err != nil {
				return err
			}
		}
		for _, line := range p.printLines(side) {
			if _, err := fmt.Fprintf(out, `<path d="M %g %g L %g %g" fill="none" stroke="black" stroke-width="0.15"/>`, line[0], line[1], line[2], line[3]); err != nil {
				return err
			}
		}
		if side.Label != "" {
			if _, err := fmt.Fprintf(out, `<text x="%g" y="%g" font-family="sans-serif" font-size="2.8" fill="black">%s</text>`, side.LabelX, side.LabelY, html.EscapeString(side.Label)); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(out, "</svg></section>"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(out, "</body></html>")
	return err
}

// stripSVGOuterTag 剥离单页SVG的根标签外壳与制作软件元数据，保留内部图元与局部样式定义
// 入参: raw 完整单页SVG文本
// 返回: string 内部SVG片段
func stripSVGOuterTag(raw string) string {
	start := strings.Index(raw, "<svg")
	if start >= 0 {
		if endOpen := strings.IndexByte(raw[start:], '>'); endOpen >= 0 {
			raw = raw[start+endOpen+1:]
		}
	}
	if closeIdx := strings.LastIndex(raw, "</svg>"); closeIdx >= 0 {
		raw = raw[:closeIdx]
	}
	return strings.TrimPrefix(raw, svgCreatorMetadata)
}

// walkSVGFragment 顺序扫描SVG片段，将非元素内容透传至out并对开始或自闭合元素标签调用回调
// 入参: fragment SVG内部片段, out 非元素片段输出缓冲(可为nil), onTag 元素标签回调
func walkSVGFragment(fragment string, out *strings.Builder, onTag func(string)) {
	writeRaw := func(s string) {
		if out != nil {
			out.WriteString(s)
		}
	}
	pos := 0
	for pos < len(fragment) {
		rel := strings.IndexByte(fragment[pos:], '<')
		if rel < 0 {
			writeRaw(fragment[pos:])
			return
		}
		tagStart := pos + rel
		writeRaw(fragment[pos:tagStart])
		rest := fragment[tagStart:]
		if strings.HasPrefix(rest, "<!--") {
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				writeRaw(rest)
				return
			}
			writeRaw(rest[:4+end+3])
			pos = tagStart + 4 + end + 3
			continue
		}
		if strings.HasPrefix(rest, "<![CDATA[") {
			end := strings.Index(rest[9:], "]]>")
			if end < 0 {
				writeRaw(rest)
				return
			}
			writeRaw(rest[:9+end+3])
			pos = tagStart + 9 + end + 3
			continue
		}
		if len(rest) < 2 || rest[1] == '/' || rest[1] == '!' || rest[1] == '?' {
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				writeRaw(rest)
				return
			}
			writeRaw(rest[:end+1])
			pos = tagStart + end + 1
			continue
		}
		tagEnd := findSVGTagEnd(rest)
		if tagEnd < 0 {
			writeRaw(rest)
			return
		}
		onTag(rest[:tagEnd+1])
		pos = tagStart + tagEnd + 1
	}
}

// namespaceSVGFragment 仅在SVG标签属性内部重写局部标识与共享图片引用，绝不篡改标签外正文文本
// 入参: fragment SVG内部片段, prefix 唯一前缀, imageURLs 共享图片DataURL映射, sharedImages 顶层复用图片标识映射
// 返回: string 重写后的SVG片段
func namespaceSVGFragment(fragment, prefix string, imageURLs map[string]string, sharedImages map[printSharedImageKey]string) string {
	var b strings.Builder
	b.Grow(len(fragment) + len(fragment)/8)
	walkSVGFragment(fragment, &b, func(tag string) {
		rewriteSVGTag(&b, tag, prefix, imageURLs, sharedImages)
	})
	return b.String()
}

// collectSVGImageKeys 收集单页SVG片段中可复用的自闭合图片标签键
// 入参: fragment SVG内部片段, imageURLs 图片资源映射
// 返回: []printSharedImageKey 可复用图片键列表
func collectSVGImageKeys(fragment string, imageURLs map[string]string) []printSharedImageKey {
	if len(imageURLs) == 0 {
		return nil
	}
	var keys []printSharedImageKey
	walkSVGFragment(fragment, nil, func(tag string) {
		if key, _, ok := parseSharedSVGImageTag(tag, imageURLs); ok {
			keys = append(keys, key)
		}
	})
	return keys
}

// parseSharedSVGImageTag 解析可跨页复用的自闭合SVG图片标签及其属性列表
// 入参: tag 完整XML元素标签, imageURLs 图片资源映射
// 返回: printSharedImageKey 图片键, []svgTagAttr 标签属性列表, bool 是否满足复用条件
func parseSharedSVGImageTag(tag string, imageURLs map[string]string) (printSharedImageKey, []svgTagAttr, bool) {
	if !strings.HasSuffix(tag, "/>") || len(tag) < 8 || !strings.HasPrefix(tag, "<image") || !isSVGAttrSpace(tag[6]) {
		return printSharedImageKey{}, nil, false
	}
	i := 6
	var attrs []svgTagAttr
	var href, width, height string
	for i < len(tag) {
		for i < len(tag) && isSVGAttrSpace(tag[i]) {
			i++
		}
		if i >= len(tag) || tag[i] == '>' || tag[i] == '/' {
			break
		}
		nameStart := i
		for i < len(tag) && !isSVGAttrSpace(tag[i]) && tag[i] != '=' && tag[i] != '/' && tag[i] != '>' && tag[i] != '"' && tag[i] != '\'' {
			i++
		}
		if i == nameStart {
			return printSharedImageKey{}, nil, false
		}
		name := tag[nameStart:i]
		for i < len(tag) && isSVGAttrSpace(tag[i]) {
			i++
		}
		if i >= len(tag) || tag[i] != '=' {
			return printSharedImageKey{}, nil, false
		}
		i++
		for i < len(tag) && isSVGAttrSpace(tag[i]) {
			i++
		}
		if i >= len(tag) || (tag[i] != '"' && tag[i] != '\'') {
			return printSharedImageKey{}, nil, false
		}
		quote := tag[i]
		valStart := i + 1
		relEnd := strings.IndexByte(tag[valStart:], quote)
		if relEnd < 0 {
			return printSharedImageKey{}, nil, false
		}
		val := tag[valStart : valStart+relEnd]
		i = valStart + relEnd + 1
		switch name {
		case "href", "xlink:href":
			href = val
		case "width":
			width = val
		case "height":
			height = val
		case "preserveAspectRatio":
			return printSharedImageKey{}, nil, false
		}
		attrs = append(attrs, svgTagAttr{name: name, val: val, quote: quote})
	}
	if href == "" || width == "" || height == "" {
		return printSharedImageKey{}, nil, false
	}
	if imageURLs != nil {
		if _, ok := imageURLs[href]; !ok {
			return printSharedImageKey{}, nil, false
		}
	}
	return printSharedImageKey{name: href, width: width, height: height}, attrs, true
}

// findSVGTagEnd 定位XML开始或自闭合标签的结束尖括号，跳过引号内的尖括号
// 入参: s 从左尖括号开始的切片
// 返回: int 结束尖括号索引，未闭合时为-1
func findSVGTagEnd(s string) int {
	var quote byte
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
		} else if ch == '>' {
			return i
		}
	}
	return -1
}

// rewriteSVGTag 扫描单个XML元素标签并重写id、href、xlink:href与url(#...)属性引用
// 入参: out 输出缓冲, tag 单个完整XML元素标签, prefix 唯一前缀, imageURLs 共享图片DataURL映射, sharedImages 顶层复用图片标识映射
func rewriteSVGTag(out *strings.Builder, tag, prefix string, imageURLs map[string]string, sharedImages map[printSharedImageKey]string) {
	if len(sharedImages) != 0 {
		if key, attrs, ok := parseSharedSVGImageTag(tag, nil); ok {
			if sharedID, exists := sharedImages[key]; exists {
				out.WriteString("<use")
				for _, attr := range attrs {
					if attr.name == "width" || attr.name == "height" || attr.name == "href" || attr.name == "xlink:href" {
						continue
					}
					val := rewriteSVGAttrValue(attr.name, attr.val, prefix, nil)
					out.WriteByte(' ')
					out.WriteString(attr.name)
					out.WriteByte('=')
					out.WriteByte(attr.quote)
					out.WriteString(val)
					out.WriteByte(attr.quote)
				}
				out.WriteString(` href="#`)
				out.WriteString(sharedID)
				out.WriteString(`" xlink:href="#`)
				out.WriteString(sharedID)
				out.WriteString(`"/>`)
				return
			}
		}
	}
	i := 1
	for i < len(tag) && !isSVGAttrSpace(tag[i]) && tag[i] != '/' && tag[i] != '>' {
		i++
	}
	out.WriteString(tag[:i])
	for i < len(tag) {
		wsStart := i
		for i < len(tag) && isSVGAttrSpace(tag[i]) {
			i++
		}
		out.WriteString(tag[wsStart:i])
		if i >= len(tag) || tag[i] == '>' || tag[i] == '/' {
			out.WriteString(tag[i:])
			return
		}
		nameStart := i
		for i < len(tag) && !isSVGAttrSpace(tag[i]) && tag[i] != '=' && tag[i] != '/' && tag[i] != '>' && tag[i] != '"' && tag[i] != '\'' {
			i++
		}
		if i == nameStart {
			out.WriteByte(tag[i])
			i++
			continue
		}
		name := tag[nameStart:i]
		out.WriteString(name)
		wsEq := i
		for i < len(tag) && isSVGAttrSpace(tag[i]) {
			i++
		}
		out.WriteString(tag[wsEq:i])
		if i >= len(tag) || tag[i] != '=' {
			continue
		}
		out.WriteByte('=')
		i++
		wsVal := i
		for i < len(tag) && isSVGAttrSpace(tag[i]) {
			i++
		}
		out.WriteString(tag[wsVal:i])
		if i >= len(tag) {
			return
		}
		if tag[i] == '"' || tag[i] == '\'' {
			quote := tag[i]
			valStart := i + 1
			relEnd := strings.IndexByte(tag[valStart:], quote)
			if relEnd < 0 {
				out.WriteString(tag[i:])
				return
			}
			val := tag[valStart : valStart+relEnd]
			val = rewriteSVGAttrValue(name, val, prefix, imageURLs)
			out.WriteByte(quote)
			out.WriteString(val)
			out.WriteByte(quote)
			i = valStart + relEnd + 1
			continue
		}
		rawStart := i
		for i < len(tag) && !isSVGAttrSpace(tag[i]) && tag[i] != '/' && tag[i] != '>' {
			i++
		}
		out.WriteString(tag[rawStart:i])
	}
}

// rewriteSVGAttrValue 重写单个SVG属性值中的局部标识定义、井号引用与图片资源链接
// 入参: name 属性名称, val 属性原值, prefix 唯一前缀, imageURLs 共享图片DataURL映射
// 返回: string 重写后的属性值
func rewriteSVGAttrValue(name, val, prefix string, imageURLs map[string]string) string {
	if name == "id" {
		if val != "" {
			return prefix + val
		}
		return val
	}
	if name == "href" || name == "xlink:href" {
		if strings.HasPrefix(val, "#") && len(val) > 1 {
			return "#" + prefix + val[1:]
		}
		if dataURL, ok := imageURLs[val]; ok {
			return dataURL
		}
		return val
	}
	if strings.Contains(val, "url(#") {
		return strings.ReplaceAll(val, "url(#", "url(#"+prefix)
	}
	return val
}

// isSVGAttrSpace 判断字节是否为XML标签内的空白字符
// 入参: ch 待判断字节
// 返回: bool 是否为空白字符
func isSVGAttrSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}
