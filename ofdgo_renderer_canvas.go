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

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/rasterizer"
)

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
	space := b.ColorSpace
	if space == nil {
		space = canvas.DefaultColorSpace
	}
	r := rasterizer.FromImage(img, canvas.DPMM(page.DPI/25.4), space)
	masks := renderCache[[32]byte, *image.Alpha]{limit: 16 << 20}
	for i, cmd := range page.Commands {
		if cmd.Image != nil {
			source := cmd.Image
			if _, linear := space.(canvas.LinearColorSpace); !linear {
				converted := image.NewRGBA(source.Bounds())
				for y := source.Bounds().Min.Y; y < source.Bounds().Max.Y; y++ {
					for x := source.Bounds().Min.X; x < source.Bounds().Max.X; x++ {
						converted.SetRGBA(x, y, space.ToLinear(color.RGBAModel.Convert(source.At(x, y)).(color.RGBA)))
					}
				}
				source = converted
			}
			if err := drawRasterImageCommand(b, page, img, source, cmd, &masks); err != nil {
				return nil, err
			}
			continue
		}
		p := &canvas.Path{}
		for _, s := range cmd.Path {
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
				return nil, fmt.Errorf("canvas command %d: unsupported path verb %d", i, s.Verb)
			}
		}
		paint := canvas.Paint{Color: cmd.Paint.Color}
		if g := cmd.Paint.Gradient; g != nil {
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
			default:
				return nil, fmt.Errorf("canvas command %d: unsupported gradient %d", i, g.Kind)
			}
			if g.Spread != nil {
				paint = canvas.Paint{Gradient: rasterGradientCanvas{g}}
			}
		}
		style := canvas.Style{Fill: paint, FillRule: canvas.NonZero}
		if cmd.EvenOdd {
			style.FillRule = canvas.EvenOdd
		}
		m := cmd.Transform
		if cmd.Stroke != nil {
			options := *cmd.Stroke
			if err := validateStroke(options); err != nil {
				return nil, err
			}
			p = p.Transform(canvas.Matrix{{m[0], m[2], m[4]}, {m[1], m[3], m[5]}})
			if cmd.Paint.Gradient != nil {
				inverse, err := m.Inverse()
				if err != nil {
					return nil, err
				}
				paint = canvas.Paint{Gradient: rasterStrokeGradientCanvas{rasterGradientCanvas{cmd.Paint.Gradient}, inverse}}
			}
			line := pathStyle{lineJoin: canvas.MiterJoin, miterLimit: defaultMiterLimit}
			line.applyLineJoin(options.Join, options.MiterLimit)
			style = canvas.Style{Stroke: paint, StrokeWidth: options.Width, StrokeCapper: pathLineCap(options.Cap, canvas.ButtCap), StrokeJoiner: line.lineJoin, FillRule: canvas.NonZero}
			if len(options.Dashes) > 0 {
				style.DashOffset = options.DashOffset / options.Width
				style.Dashes = make([]float64, len(options.Dashes))
				for i, dash := range options.Dashes {
					style.Dashes[i] = dash / options.Width
				}
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
	r.Close()
	return img, nil
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
	return g.gradient.At(p.X, p.Y)
}

// SetColorSpace 转换描边渐变颜色，不修改共享分段
// 入参: space 颜色空间
// 返回: canvas.Gradient 独立渐变
func (g rasterStrokeGradientCanvas) SetColorSpace(space canvas.ColorSpace) canvas.Gradient {
	g.rasterGradientCanvas = g.rasterGradientCanvas.SetColorSpace(space).(rasterGradientCanvas)
	return g
}

// rasterGradientCanvas 将公共渐变交给Canvas采样，不改变OFD周期语义
type rasterGradientCanvas struct{ gradient *RasterGradient }

// At 返回局部坐标的预乘颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 渐变颜色
func (g rasterGradientCanvas) At(x, y float64) color.RGBA { return g.gradient.At(x, y) }

// SetColorSpace 返回转换颜色空间后的独立渐变
// 入参: space 目标颜色空间
// 返回: canvas.Gradient 采样器
func (g rasterGradientCanvas) SetColorSpace(space canvas.ColorSpace) canvas.Gradient {
	copy := *g.gradient
	copy.Stops = append([]ColorStop(nil), copy.Stops...)
	for i := range copy.Stops {
		copy.Stops[i].Color = space.ToLinear(copy.Stops[i].Color)
	}
	return rasterGradientCanvas{&copy}
}
