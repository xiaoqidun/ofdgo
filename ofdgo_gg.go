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

	"github.com/gogpu/gg"
)

// GGBackend 不注册全局GPU设备或回退到其他绘图库的CPU后端
type GGBackend struct{}

// Name 返回后端标识
// 返回: string 后端标识
func (GGBackend) Name() string { return "gg" }

// Render 绘制已编译页面，使用自有双圆渐变语义保持原有定位
// 入参: page 只读页面
// 返回: image.Image 图像, error 绘制错误
func (GGBackend) Render(page *RasterPage) (image.Image, error) {
	w, h, err := page.PixelSize()
	if err != nil {
		return nil, err
	}
	pixmap := gg.NewPixmap(w, h)
	img := pixmap.ImageView()
	raster := gg.NewSoftwareRenderer(w, h)
	for i, cmd := range page.Commands {
		m := page.PixelTransform(cmd.Transform, h)
		if cmd.Image != nil {
			drawRasterImage(img, cmd.Image, m)
			continue
		}
		path := gg.NewPath()
		for _, s := range cmd.Path {
			end, c1, c2 := m.Apply(s.End), m.Apply(s.Control1), m.Apply(s.Control2)
			switch s.Verb {
			case RasterMove:
				path.MoveTo(end.X, end.Y)
			case RasterLine:
				path.LineTo(end.X, end.Y)
			case RasterQuad:
				path.QuadraticTo(c1.X, c1.Y, end.X, end.Y)
			case RasterCubic:
				path.CubicTo(c1.X, c1.Y, c2.X, c2.Y, end.X, end.Y)
			case RasterClose:
				path.Close()
			default:
				return nil, fmt.Errorf("gg command %d: unsupported path verb %d", i, s.Verb)
			}
		}
		paint := gg.NewPaint()
		paint.SetFillBrush(gg.SolidBrush{Color: ggRGBA(cmd.Paint.Color)})
		if g := cmd.Paint.Gradient; g != nil {
			if g.Kind != RasterLinear && g.Kind != RasterRadial {
				return nil, fmt.Errorf("gg command %d: unsupported gradient %d", i, g.Kind)
			}
			inverse, err := m.Inverse()
			if err != nil {
				return nil, fmt.Errorf("gg command %d: %w", i, err)
			}
			paint.SetFillBrush(gg.NewCustomBrush(func(x, y float64) gg.RGBA {
				p := inverse.Apply(RasterPoint{X: x, Y: y})
				return ggRGBA(g.At(p.X, p.Y))
			}))
		}
		if cmd.EvenOdd {
			paint.FillRule = gg.FillRuleEvenOdd
		}
		if err := raster.Fill(pixmap, path, paint); err != nil {
			return nil, fmt.Errorf("gg command %d: %w", i, err)
		}
	}
	return img, nil
}

// ggRGBA 将标准库预乘颜色转换为gg的非预乘颜色
// 入参: c 预乘颜色
// 返回: gg.RGBA 非预乘颜色
func ggRGBA(c color.RGBA) gg.RGBA {
	if c.A == 0 {
		return gg.Transparent
	}
	a := float64(c.A)
	return gg.RGBA{R: float64(c.R) / a, G: float64(c.G) / a, B: float64(c.B) / a, A: a / 255}
}
