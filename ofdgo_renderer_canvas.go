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
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"slices"
	"sync"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/rasterizer"
	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// canvasSRGBTables 复用八位预乘sRGB的双向传递表，透明度参与索引以保留原运算精度
var canvasSRGBTables = sync.OnceValue(func() [2]*[256][256]uint8 {
	var result [2]*[256][256]uint8
	space := canvas.SRGBColorSpace{}
	for direction, transform := range []func(color.Color) color.RGBA{space.ToLinear, space.FromLinear} {
		table := new([256][256]uint8)
		for alpha := range 256 {
			for value := range 256 {
				table[alpha][value] = transform(color.RGBA{R: uint8(value), A: uint8(alpha)}).R
			}
		}
		result[direction] = table
	}
	return result
})

// init 注册Canvas适配器和默认组合
func init() {
	if err := RegisterRenderBackend("canvas", defaultRenderBackends); err != nil {
		panic(err)
	}
}

// CanvasBackend canvas页面编译与绘制适配器，ColorSpace仅影响光栅输出
type CanvasBackend struct {
	ColorSpace canvas.ColorSpace
}

// Name 返回后端标识
// 返回: string 后端标识
func (CanvasBackend) Name() string { return "canvas" }

// Render 绘制已编译页面，不修改输入数据
// 入参: page 只读页面
// 返回: image.Image 图像, error 绘制错误
func (b CanvasBackend) Render(page *RasterPage) (image.Image, error) {
	w, h, err := page.PixelSize()
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	return b.renderImage(page, img)
}

// RenderInto 在调用方独占的紧密RGBA缓冲中重新绘制，不修改页面或保留缓冲
// 入参: page 只读页面, target 原点为零且尺寸匹配的目标，不与输入图像共享数据
// 返回: error 参数或绘制错误，绘制失败时目标可能包含部分结果
func (b CanvasBackend) RenderInto(page *RasterPage, target *image.RGBA) error {
	w, h, err := page.PixelSize()
	if err != nil {
		return err
	}
	if target == nil || target.Rect != image.Rect(0, 0, w, h) || target.Stride != 4*w || len(target.Pix) < 4*w*h {
		return fmt.Errorf("invalid canvas target buffer")
	}
	clear(target.Pix[:4*w*h])
	_, err = b.renderImage(page, target)
	return err
}

// OpenFont 使用独立字体后端读取字体度量
// 入参: data 字体数据
// 返回: FontMetrics 字体度量, error 解析错误
func (CanvasBackend) OpenFont(data []byte) (FontMetrics, error) {
	return (SFNTBackend{}).OpenFont(data)
}

// ResolveFont 使用独立字体后端解析字体来源
// 入参: r 渲染器, id 字体标识, exact 是否禁止无关回退
// 返回: ResolvedFont 字体与来源, error 解析错误
func (CanvasBackend) ResolveFont(r *Renderer, id string, exact bool) (ResolvedFont, error) {
	return (SFNTBackend{}).ResolveFont(r, id, exact)
}

// rasterStrokeGradientCanvas 保留页面坐标描边的局部渐变定位
type rasterStrokeGradientCanvas struct {
	rasterGradientCanvas
	inverse RasterMatrix
}

// At 返回原局部坐标的描边渐变颜色
// 入参: x 页面横坐标, y 页面纵坐标
// 返回: color.RGBA 预乘颜色
func (g rasterStrokeGradientCanvas) At(x, y float64) color.RGBA {
	p := g.inverse.Apply(RasterPoint{x, y})
	return g.rasterGradientCanvas.At(p.X, p.Y)
}

// SetColorSpace 转换描边渐变颜色，不修改共享分段
// 入参: space 颜色空间
// 返回: canvas.Gradient 独立渐变
func (g rasterStrokeGradientCanvas) SetColorSpace(space canvas.ColorSpace) canvas.Gradient {
	g.rasterGradientCanvas = g.rasterGradientCanvas.SetColorSpace(space).(rasterGradientCanvas)
	return g
}

// rasterGradientCanvas 将公共渐变交给Canvas采样，不改变OFD周期语义
type rasterGradientCanvas struct {
	gradient *RasterGradient
	space    canvas.ColorSpace
	mesh     *MeshSampler
}

// newRasterGradientCanvas 准备渐变适配器及独立网格采样器
// 入参: gradient 公共渐变
// 返回: rasterGradientCanvas Canvas渐变
func newRasterGradientCanvas(gradient *RasterGradient) rasterGradientCanvas {
	result := rasterGradientCanvas{gradient: gradient}
	if gradient.Kind == RasterMesh {
		result.mesh = gradient.Mesh.NewSampler()
	}
	return result
}

// At 返回局部坐标的预乘颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 渐变颜色
func (g rasterGradientCanvas) At(x, y float64) color.RGBA {
	var value color.RGBA
	if g.mesh != nil {
		value = g.mesh.At(x, y)
	} else {
		value = g.gradient.At(x, y)
	}
	if g.space != nil {
		return g.space.ToLinear(value)
	}
	return value
}

// SetColorSpace 返回转换颜色空间后的独立渐变
// 入参: space 目标颜色空间
// 返回: canvas.Gradient 采样器
func (g rasterGradientCanvas) SetColorSpace(space canvas.ColorSpace) canvas.Gradient {
	if g.gradient.Mesh != nil || g.gradient.Sample != nil {
		g.space = space
		return g
	}
	copy := *g.gradient
	copy.Stops = append([]ColorStop(nil), copy.Stops...)
	for i := range copy.Stops {
		copy.Stops[i].Color = space.ToLinear(copy.Stops[i].Color)
	}
	return rasterGradientCanvas{gradient: &copy}
}

// canvasColorTransfer 准备八位预乘颜色变换，任意自定义空间保留其原始计算
// 入参: space 颜色空间, linear 是否转入线性空间
// 返回: func(color.RGBA) color.RGBA 单像素变换
func canvasColorTransfer(space canvas.ColorSpace, linear bool) func(color.RGBA) color.RGBA {
	if _, ok := space.(canvas.SRGBColorSpace); ok {
		direction := 1
		if linear {
			direction = 0
		}
		table := canvasSRGBTables()[direction]
		return func(value color.RGBA) color.RGBA {
			return color.RGBA{R: table[value.A][value.R], G: table[value.A][value.G], B: table[value.A][value.B], A: value.A}
		}
	}
	if linear {
		return func(value color.RGBA) color.RGBA { return space.ToLinear(value) }
	}
	return func(value color.RGBA) color.RGBA { return space.FromLinear(value) }
}

// renderImage 将页面绘制到已清空且尺寸匹配的目标
// 入参: page 只读页面, img 独占目标
// 返回: image.Image 目标图像, error 绘制错误
func (b CanvasBackend) renderImage(page *RasterPage, img *image.RGBA) (image.Image, error) {
	h := img.Rect.Dy()
	space := b.ColorSpace
	if space == nil {
		space = canvas.DefaultColorSpace
	}
	r := rasterizer.FromImage(img, canvas.DPMM(page.DPI/25.4), space)
	masks := renderCache[[32]byte, *image.Alpha]{limit: 16 << 20}
	var pathData []float64
	for i, cmd := range page.Commands {
		if cmd.Image != nil {
			source, err := baseimage.PixelData(cmd.Image)
			if err != nil {
				return nil, fmt.Errorf("canvas image %d: %w", i, err)
			}
			if _, linear := space.(canvas.LinearColorSpace); !linear {
				converted := image.NewRGBA(source.Bounds())
				baseimage.TransformRGBA(converted, source, canvasColorTransfer(space, true))
				source = converted
			}
			if err := drawRasterImageCommand(b, page, img, source, cmd, &masks); err != nil {
				return nil, err
			}
			continue
		}
		p, err := rasterCanvasPath(visibleRasterPath(cmd, page), pathData)
		if err != nil {
			return nil, fmt.Errorf("canvas command %d: %w", i, err)
		}
		pathData = p.Data()[:0]
		paint := canvas.Paint{Color: cmd.Paint.Color}
		var sampled rasterGradientCanvas
		if g := cmd.Paint.Gradient; g != nil {
			sampled = newRasterGradientCanvas(g)
			stops := make(canvas.Grad, len(g.Stops))
			for j, stop := range g.Stops {
				stops[j] = canvas.Stop{Offset: stop.Offset, Color: stop.Color}
			}
			start, end := canvas.Point{X: g.Start.X, Y: g.Start.Y}, canvas.Point{X: g.End.X, Y: g.End.Y}
			switch g.Kind {
			case RasterLinear:
				paint = canvas.Paint{Gradient: stops.ToLinear(start, end)}
			case RasterRadial:
				paint = canvas.Paint{Gradient: stops.ToRadial(start, g.R0, end, g.R1)}
			case RasterMesh:
				paint = canvas.Paint{Gradient: sampled}
			default:
				return nil, fmt.Errorf("canvas command %d: unsupported gradient %d", i, g.Kind)
			}
			if g.Spread != nil || g.Sample != nil {
				paint = canvas.Paint{Gradient: sampled}
			}
		}
		style := canvas.Style{Fill: paint, FillRule: canvas.NonZero}
		if cmd.EvenOdd {
			style.FillRule = canvas.EvenOdd
		}
		m := cmd.Transform
		if cmd.Stroke != nil {
			options := *cmd.Stroke
			if err := basegeom.ValidateStroke(options); err != nil {
				return nil, err
			}
			p = p.Transform(canvas.Matrix{{m[0], m[2], m[4]}, {m[1], m[3], m[5]}})
			if cmd.Paint.Gradient != nil {
				inverse, err := m.Inverse()
				if err != nil {
					return nil, err
				}
				paint = canvas.Paint{Gradient: rasterStrokeGradientCanvas{sampled, inverse}}
			}
			line := pathStyle{lineJoin: canvas.MiterJoin, miterLimit: defaultMiterLimit}
			line.applyLineJoin(options.Join, options.MiterLimit, options.MiterLimitSet)
			style = canvas.Style{Stroke: paint, StrokeWidth: options.Width, StrokeCapper: pathLineCap(options.Cap, canvas.ButtCap), StrokeJoiner: line.lineJoin, FillRule: canvas.NonZero}
			if len(options.Dashes) > 0 {
				style.DashOffset = options.DashOffset / options.Width
				style.Dashes = make([]float64, len(options.Dashes))
				for i, dash := range options.Dashes {
					style.Dashes[i] = dash / options.Width
				}
			}
			var pointPath GeometryPath
			pointStroke := false
			if options.Cap == "Round" {
				pointPath, pointStroke = canvasPointPath(geometryFromRaster(cmd.Path))
			}
			if options.Cap == "Round" && (p.Empty() || pointStroke) {
				path, err := pointPath.Transform(MatrixFromValues([6]float64(m)))
				if err != nil {
					return nil, err
				}
				path, err = path.Stroke(options)
				if err != nil {
					return nil, err
				}
				p, err = canvasObjectPath(path)
				if err != nil {
					return nil, err
				}
				style = canvas.Style{Fill: paint, FillRule: canvas.NonZero}
			} else if slices.Contains(options.Dashes, 0) {
				var err error
				p, err = canvasStrokePath(b, p, options)
				if err != nil {
					return nil, err
				}
				style = canvas.Style{Fill: paint, FillRule: canvas.NonZero}
			}
			m = RasterMatrix{1, 0, 0, 1, 0, 0}
		}
		matrix := canvas.Matrix{{m[0], m[2], m[4]}, {-m[1], -m[3], page.Height - m[5]}}
		if cmd.Clip == nil {
			r.RenderPath(p, style, matrix)
			continue
		}
		mask, err := rasterClipMask(b, page, cmd.Clip, &masks)
		if err != nil {
			return nil, err
		}
		if mask.Rect.Empty() {
			continue
		}
		part := image.NewRGBA(image.Rect(0, 0, mask.Rect.Dx(), mask.Rect.Dy()))
		local := rasterizer.FromImage(part, canvas.DPMM(page.DPI/25.4), space)
		matrix[0][2] -= float64(mask.Rect.Min.X) / (page.DPI / 25.4)
		matrix[1][2] -= float64(h-mask.Rect.Max.Y) / (page.DPI / 25.4)
		local.RenderPath(p, style, matrix)
		draw.DrawMask(img, mask.Rect, part, image.Point{}, mask, mask.Rect.Min, draw.Over)
	}
	if _, linear := space.(canvas.LinearColorSpace); !linear {
		baseimage.TransformRGBA(img, img, canvasColorTransfer(space, false))
	}
	return img, nil
}

// visibleRasterPath 排除填充区域外的完整子路径，保留曲线控制范围与抗锯齿边缘
// 入参: command 绘制指令, page 目标页面
// 返回: []RasterSegment 可见子路径，描边和短路径沿用原数据
func visibleRasterPath(command RasterCommand, page *RasterPage) []RasterSegment {
	if command.Stroke != nil || len(command.Path) < 256 || command.Path[0].Verb != RasterMove {
		return command.Path
	}
	padding := 25.4 / page.DPI
	var visible []RasterSegment
	filtered := false
	start := 0
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	flush := func(end int) {
		if minX <= page.Width+padding && maxX >= -padding && minY <= page.Height+padding && maxY >= -padding {
			if filtered {
				visible = append(visible, command.Path[start:end]...)
			}
		} else if !filtered {
			filtered = true
			visible = append(visible, command.Path[:start]...)
		}
	}
	include := func(point RasterPoint) bool {
		point = command.Transform.Apply(point)
		if !finite(point.X) || !finite(point.Y) {
			return false
		}
		minX, minY = math.Min(minX, point.X), math.Min(minY, point.Y)
		maxX, maxY = math.Max(maxX, point.X), math.Max(maxY, point.Y)
		return true
	}
	for index, segment := range command.Path {
		if segment.Verb == RasterMove && index != start {
			flush(index)
			start = index
			minX, minY, maxX, maxY = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		}
		if segment.Verb > RasterClose || segment.Verb != RasterClose && !include(segment.End) || (segment.Verb == RasterQuad || segment.Verb == RasterCubic) && !include(segment.Control1) || segment.Verb == RasterCubic && !include(segment.Control2) {
			return command.Path
		}
	}
	flush(len(command.Path))
	if !filtered {
		return command.Path
	}
	return visible
}

// rasterCanvasPath 预留路径容量，保留光栅指令的构建及闭合规则
// 入参: path 只读光栅路径, buffer 可复用的独占路径缓冲
// 返回: *canvas.Path 使用独占缓冲的绘制路径, error 不支持的指令
func rasterCanvasPath(path []RasterSegment, buffer []float64) (*canvas.Path, error) {
	capacity := 0
	for _, segment := range path {
		switch segment.Verb {
		case RasterQuad:
			capacity += 6
		case RasterCubic:
			capacity += 8
		default:
			capacity += 4
		}
	}
	if cap(buffer) < capacity {
		buffer = make([]float64, 0, capacity)
	}
	p := canvas.NewPathFromData(buffer[:0])
	for _, s := range path {
		switch s.Verb {
		case RasterMove:
			p.MoveTo(s.End.X, s.End.Y)
		case RasterLine:
			p.LineTo(s.End.X, s.End.Y)
		case RasterQuad:
			p.QuadTo(s.Control1.X, s.Control1.Y, s.End.X, s.End.Y)
		case RasterCubic:
			p.CubeTo(s.Control1.X, s.Control1.Y, s.Control2.X, s.Control2.Y, s.End.X, s.End.Y)
		case RasterClose:
			p.Close()
		default:
			return nil, fmt.Errorf("unsupported path verb %d", s.Verb)
		}
	}
	return p, nil
}

// defaultRenderBackends 创建默认组合，调用方可按职责替换任意后端
// 返回: RenderBackends 默认后端组合
func defaultRenderBackends() RenderBackends {
	backend := CanvasBackend{}
	return RenderBackends{FontResources: SFNTBackend{}, Fonts: SFNTBackend{}, Geometry: backend, Compiler: backend, Raster: backend, SVG: backend, PDF: backend, EPS: backend}
}
