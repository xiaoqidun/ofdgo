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
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"

	"github.com/tdewolff/canvas"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// canvasPageCompiler 将已解释的页面绘制转换为自有数据，不保存第三方绘图对象
type canvasPageCompiler struct {
	page             *RasterPage
	geometry         GeometryBackend
	imageInterpolate *bool
	err              error
}

// rasterPaddedImage 为旋转采样提供透明边距，共享原图而不复制整幅像素
type rasterPaddedImage struct {
	source image.Image
	rect   image.Rectangle
}

// canvasRasterPath 转换曲线路径，保留各子路径和闭合标记
// 入参: path Canvas路径
// 返回: []RasterSegment 独立路径, error 未知指令错误
func canvasRasterPath(path *canvas.Path) ([]RasterSegment, error) {
	segments := make([]RasterSegment, 0, path.Len())
	parts := path.Split()
	if len(parts) == 0 {
		parts = []*canvas.Path{path}
	}
	for _, part := range parts {
		scanner := part.ReplaceArcs().Scanner()
		for scanner.Scan() {
			s := RasterSegment{End: rasterPoint(scanner.End())}
			switch scanner.Cmd() {
			case canvas.MoveToCmd:
				s.Verb = RasterMove
			case canvas.LineToCmd:
				s.Verb = RasterLine
			case canvas.QuadToCmd:
				s.Verb, s.Control1 = RasterQuad, rasterPoint(scanner.CP1())
			case canvas.CubeToCmd:
				s.Verb, s.Control1, s.Control2 = RasterCubic, rasterPoint(scanner.CP1()), rasterPoint(scanner.CP2())
			case canvas.CloseCmd:
				s.Verb = RasterClose
			default:
				return nil, fmt.Errorf("unsupported raster path command %g", scanner.Cmd())
			}
			segments = append(segments, s)
		}
	}
	data := path.Data()
	last := parts[len(parts)-1].Data()
	if len(data) >= 4 && data[len(data)-4] == canvas.MoveToCmd && len(last) != 0 && last[len(last)-1] != canvas.MoveToCmd {
		segments = append(segments, RasterSegment{Verb: RasterMove, End: RasterPoint{X: data[len(data)-3], Y: data[len(data)-2]}})
	}
	return segments, nil
}

// rasterPoint 转换局部坐标点
// 入参: p 默认引擎坐标点
// 返回: RasterPoint 独立坐标点
func rasterPoint(p canvas.Point) RasterPoint { return RasterPoint{X: p.X, Y: p.Y} }

// CompilePage 使用Canvas页面解释器和配置的字体、几何后端生成独立绘制数据
// 入参: ctx 取消上下文, r 渲染器, page 页面内容
// 返回: *RasterPage 绘制页面, error 编译或取消错误
func (DefaultBackend) CompilePage(ctx context.Context, r *Renderer, page *PageContent) (*RasterPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prevCtx := r.pageContext
	r.pageContext = ctx
	defer func() { r.pageContext = prevCtx }()
	box, err := r.GetPageBoxContext(ctx, page)
	if err != nil {
		return nil, err
	}
	result := &RasterPage{Width: box.W, Height: box.H, DPI: r.dpi}
	if _, _, err := result.PixelSize(); err != nil {
		return nil, err
	}
	geometry, err := r.Geometry()
	if err != nil {
		return nil, err
	}
	prevEdges := r.transparentEdges
	if prevEdges == nil {
		r.transparentEdges = make(map[image.Image]transparentEdgeResult)
		defer func() { r.transparentEdges = prevEdges }()
	}
	prefetch := &pageImagePrefetch{}
	r.pageContext = context.WithValue(ctx, pageImagePrefetchKey{}, prefetch)
	compiler := &canvasPageCompiler{page: result, geometry: geometry}
	if err := r.renderCanvasContext(canvas.NewContext(compiler), page); err != nil {
		return nil, err
	}
	if compiler.err != nil {
		return nil, compiler.err
	}
	return result, nil
}

// pageImagePrefetchKey 标识页面编译期间并发预解码图像的上下文键
type pageImagePrefetchKey struct{}

// pagePrefetchedImage 保存单张并发预解码完成的原始像素与透明补边结果
type pagePrefetchedImage struct {
	raw     image.Image
	edge    image.Image
	pad     int
	hasEdge bool
}

// pagePrefetchedAlphaKey 标识单页内复用的预解码图像透明变体键
type pagePrefetchedAlphaKey struct {
	path  string
	alpha uint8
}

// pageImagePrefetch 保存当前页面并发预解码与复用的图像资源映射
type pageImagePrefetch struct {
	ready   bool
	byPath  map[string]pagePrefetchedImage
	byAlpha map[pagePrefetchedAlphaKey]image.Image
}

// prefetchPageEncodedImages 在页面光栅编译前去重并并发预解码惰性编码图像及透明补边结果
// 入参: ctx 取消上下文, r 渲染器, page 页面内容
func prefetchPageEncodedImages(ctx context.Context, r *Renderer, page *PageContent) {
	if r == nil || r.reader == nil || page == nil || !r.decodeImages || r.textOnly {
		return
	}
	pf, ok := ctx.Value(pageImagePrefetchKey{}).(*pageImagePrefetch)
	if !ok || pf == nil || pf.ready {
		return
	}
	pf.ready = true
	type prefetchItem struct {
		path   string
		enc    *baseimage.EncodedImage
		source image.Image
		padded image.Image
		pad    int
		hasEdg bool
	}
	var pending []prefetchItem
	pathCount := make(map[string]int)
	hasReused := false
	inStack := make(map[string]bool)
	addResource := func(id string) {
		if id == "" {
			return
		}
		resolved, ok := r.reader.resolveResPathScoped(r.pageResources, id)
		if !ok {
			return
		}
		cleanPath := cleanPackagePath(r.reader.ResPath(resolved))
		pathCount[cleanPath]++
		if pathCount[cleanPath] > 1 {
			hasReused = true
			return
		}
		if len(pending) >= 64 {
			return
		}
		img, err := r.ImageResource(id)
		if err != nil {
			return
		}
		if enc, ok := unwrapEncodedImage(img).(*baseimage.EncodedImage); ok {
			pending = append(pending, prefetchItem{path: cleanPath, enc: enc})
		}
	}
	var walkSeq func(seq func(func(GraphicObject) bool))
	walkSeq = func(seq func(func(GraphicObject) bool)) {
		for obj := range seq {
			switch obj.Type {
			case "ImageObject":
				imgObj := obj.ImageObject
				if imgObj.Visible != nil && !*imgObj.Visible || imgObj.ImageMask != "" {
					continue
				}
				primary := imgObj.ResourceID
				if imgObj.Substitution != "" && !sameResourceID(imgObj.Substitution, imgObj.ResourceID) && (r.printing || r.dpi >= 300) {
					primary = imgObj.Substitution
				}
				addResource(primary)
			case "CompositeObject":
				id := obj.CompositeObject.ResourceID
				if id == "" || inStack[id] {
					continue
				}
				inStack[id] = true
				if unit := r.compositeDefinition(id); unit != nil {
					walkSeq(unit.objects())
				}
				delete(inStack, id)
			}
		}
	}
	for i := range page.Content.Layer {
		walkSeq(func(yield func(GraphicObject) bool) {
			for _, obj := range page.Content.Layer[i].Objects {
				if !yield(obj) {
					return
				}
			}
		})
	}
	if r.reader.doc != nil {
		for _, ref := range page.Template {
			if tpl := r.loadTemplate(ref.TemplateID); tpl != nil {
				restoreTpl := r.bindParsedResources(tpl.pageResources)
				for i := range tpl.Content.Layer {
					walkSeq(func(yield func(GraphicObject) bool) {
						for _, obj := range tpl.Content.Layer[i].Objects {
							if !yield(obj) {
								return
							}
						}
					})
				}
				restoreTpl()
			}
		}
	}
	if len(pending) == 0 || (len(pending) == 1 && !hasReused) {
		return
	}
	decodeOne := func(idx int) {
		if ctx.Err() != nil {
			return
		}
		decoded, err := pending[idx].enc.ImageContext(ctx)
		if err != nil || decoded == nil {
			return
		}
		pending[idx].source = decoded
		padded, pad, err := baseimage.WithTransparentEdge(ctx, decoded)
		if err != nil {
			return
		}
		pending[idx].hasEdg = true
		if padded == decoded {
			pending[idx].padded, pending[idx].pad = decoded, 0
		} else {
			pending[idx].padded, pending[idx].pad = wrapEncodedImage(padded), pad
		}
	}
	workers := min(runtime.GOMAXPROCS(0), 8, len(pending))
	if workers <= 1 {
		for i := range pending {
			decodeOne(i)
		}
	} else {
		jobs := make(chan int, len(pending))
		for i := range pending {
			jobs <- i
		}
		close(jobs)
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for idx := range jobs {
					decodeOne(idx)
				}
			})
		}
		wg.Wait()
	}
	pf.byPath = make(map[string]pagePrefetchedImage, len(pending))
	for i := range pending {
		item := pending[i]
		if item.source != nil {
			pf.byPath[item.path] = pagePrefetchedImage{raw: item.source, edge: item.padded, pad: item.pad, hasEdge: item.hasEdg}
			if r.transparentEdges != nil && item.hasEdg && isPointerImage(item.source) {
				r.transparentEdges[item.source] = transparentEdgeResult{img: item.padded, pad: item.pad}
			}
		}
	}
}

// Size 返回物理页面尺寸
// 返回: float64 宽度, float64 高度
func (c *canvasPageCompiler) Size() (float64, float64) { return c.page.Width, c.page.Height }

// RenderPath 记录填充并按当前DPI展开描边轮廓
// 入参: path 路径, style 绘制样式, m 变换矩阵
func (c *canvasPageCompiler) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if c.err != nil {
		return
	}
	if style.HasFill() {
		c.fill(path, style.Fill, style.FillRule, m)
	}
	if style.HasStroke() {
		stroke := path
		options := canvasStrokeOptions(style.StrokeWidth, style.StrokeCapper, style.StrokeJoiner, canvas.PixelTolerance/(c.page.DPI/25.4))
		if len(style.Dashes) > 0 {
			options.DashOffset, options.Dashes = canvas.ScaleDash(style.StrokeWidth, style.DashOffset, style.Dashes)
		}
		var err error
		stroke, err = canvasStrokeFillPath(c.geometry, stroke, options)
		if err != nil {
			c.err = err
			return
		}
		c.fill(stroke, style.Stroke, canvas.NonZero, m)
	}
}

// RenderText 展开已定位字形，使用与目标DPI一致的字形精度
// 入参: text 文字, m 变换矩阵
func (c *canvasPageCompiler) RenderText(text *canvas.Text, m canvas.Matrix) {
	if c.err == nil {
		text.RenderTo(c, m, canvas.DPMM(c.page.DPI/25.4))
	}
}

// RenderImage 记录按像素左上原点定位的图片，旋转或斜切时补充透明采样边距
// 入参: img 图片, m 变换矩阵
func (c *canvasPageCompiler) RenderImage(img image.Image, m canvas.Matrix) {
	if c.err != nil {
		return
	}
	var err error
	img, err = baseimage.PixelData(img)
	if err != nil {
		c.err = err
		return
	}
	if (math.Abs(m[0][1]) > 1e-12 || math.Abs(m[1][0]) > 1e-12) && (math.Abs(m[0][0]) > 1e-12 || math.Abs(m[1][1]) > 1e-12) {
		bounds := img.Bounds()
		img = &rasterPaddedImage{source: img, rect: image.Rect(0, 0, bounds.Dx()+8, bounds.Dy()+8)}
		m = m.Translate(-4, -4)
	}
	bounds := img.Bounds()
	h := float64(bounds.Dy())
	t := RasterMatrix{m[0][0], -m[1][0], -m[0][1], m[1][1], m[0][2] + m[0][1]*h, c.page.Height - m[1][2] - m[1][1]*h}
	t[4] -= t[0]*float64(bounds.Min.X) + t[2]*float64(bounds.Min.Y)
	t[5] -= t[1]*float64(bounds.Min.X) + t[3]*float64(bounds.Min.Y)
	c.page.Commands = append(c.page.Commands, RasterCommand{Image: img, Interpolate: c.imageInterpolate, Transform: t})
}

// fill 转换路径和画刷，未知类型返回错误而非丢弃内容
// 入参: path 路径, paint 画刷, rule 填充规则, m 变换矩阵
func (c *canvasPageCompiler) fill(path *canvas.Path, paint canvas.Paint, rule canvas.FillRule, m canvas.Matrix) {
	if c.err != nil || path.Empty() {
		return
	}
	if paint.Pattern != nil {
		c.err = fmt.Errorf("unsupported raster pattern %T", paint.Pattern)
		return
	}
	cmd := RasterCommand{Paint: RasterPaint{Color: paint.Color}, EvenOdd: rule == canvas.EvenOdd,
		Transform: RasterMatrix{m[0][0], -m[1][0], m[0][1], -m[1][1], m[0][2], c.page.Height - m[1][2]}}
	if paint.Gradient != nil {
		g := &RasterGradient{}
		var stops canvas.Grad
		switch source := paint.Gradient.(type) {
		case rasterGradientCanvas:
			copy := *source.gradient
			g = &copy
			stops = canvasGradient(g.Stops)
		case *canvas.LinearGradient:
			g.Kind, g.Start, g.End = RasterLinear, rasterPoint(source.Start), rasterPoint(source.End)
			stops = source.Grad
		case *canvas.RadialGradient:
			g.Kind, g.Start, g.End = RasterRadial, rasterPoint(source.C0), rasterPoint(source.C1)
			g.R0, g.R1, stops = source.R0, source.R1, source.Grad
		default:
			c.err = fmt.Errorf("unsupported raster gradient %T", paint.Gradient)
			return
		}
		g.Stops = make([]ColorStop, len(stops))
		for i, stop := range stops {
			g.Stops[i] = ColorStop{Offset: stop.Offset, Color: stop.Color}
		}
		cmd.Paint.Gradient = g
	}
	var err error
	cmd.Path, err = canvasRasterPath(path)
	if err != nil {
		c.err = err
		return
	}
	c.page.Commands = append(c.page.Commands, cmd)
}

// Bounds 返回含四像素边距的图像边界
// 返回: image.Rectangle 图像边界
func (s *rasterPaddedImage) Bounds() image.Rectangle { return s.rect }

// BackingImage 返回共享的原图，用于计算底层像素存储
// 返回: image.Image 原图
func (s *rasterPaddedImage) BackingImage() image.Image { return s.source }

// ColorModel 按原图采样精度返回支持透明补边的预乘颜色模型
// 返回: color.Model 颜色模型
func (s *rasterPaddedImage) ColorModel() color.Model {
	if baseimage.HighPrecision(s.source) {
		return color.RGBA64Model
	}
	return color.RGBAModel
}

// At 读取补边图像的预乘像素，边距及图像外透明
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 像素颜色
func (s *rasterPaddedImage) At(x, y int) color.Color { return s.RGBA64At(x, y) }

// RGBA64At 保留原始预乘精度，支持采样接口的图像不进行颜色装箱
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 像素颜色
func (s *rasterPaddedImage) RGBA64At(x, y int) color.RGBA64 {
	if !image.Pt(x, y).In(s.rect.Inset(4)) {
		return color.RGBA64{}
	}
	bounds := s.source.Bounds()
	x, y = x-4+bounds.Min.X, y-4+bounds.Min.Y
	if source, ok := s.source.(image.RGBA64Image); ok {
		return source.RGBA64At(x, y)
	}
	r, g, b, a := s.source.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}
