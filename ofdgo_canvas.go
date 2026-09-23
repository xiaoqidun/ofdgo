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
			drawRasterImage(img, source, page.PixelTransform(cmd.Transform, h))
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
		}
		style := canvas.Style{Fill: paint, FillRule: canvas.NonZero}
		if cmd.EvenOdd {
			style.FillRule = canvas.EvenOdd
		}
		m := cmd.Transform
		r.RenderPath(p, style, canvas.Matrix{{m[0], m[2], m[4]}, {-m[1], -m[3], page.Height - m[5]}})
	}
	r.Close()
	return img, nil
}
