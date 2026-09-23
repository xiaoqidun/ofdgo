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
	"sync"

	"github.com/gogpu/gg"
)

// GGBackend 提供字体、页面编译和CPU绘制，不注册全局GPU设备或隐式切换后端
// StrokeGeometry用于GG暂不能准确处理的尖角连接、闭合或曲线虚线及亚像素渐变描边，未配置时返回能力错误
type GGBackend struct{ StrokeGeometry GeometryBackend }

// Name 返回后端标识
// 返回: string 后端标识
func (GGBackend) Name() string { return "gg" }

// Prepare 将路径、样式和裁剪准备为固定DPI的GG场景
// 入参: page 只读页面
// 返回: RasterScene 可重复绘制场景, error 准备错误
func (b GGBackend) Prepare(page *RasterPage) (RasterScene, error) {
	w, h, err := page.PixelSize()
	if err != nil {
		return nil, err
	}
	page = cloneRasterPage(page)
	scene := &ggRasterScene{page: page, width: w, height: h, masks: renderCache[[32]byte, *image.Alpha]{limit: 16 << 20}}
	for i, cmd := range page.Commands {
		if cmd.Stroke != nil && !ggNativeStroke(cmd, page.DPI/25.4) {
			cmd, err = b.expandStroke(cmd)
			if err != nil {
				return nil, fmt.Errorf("gg command %d: %w", i, err)
			}
		}
		m := page.PixelTransform(cmd.Transform, h)
		if cmd.Image != nil {
			scene.commands = append(scene.commands, ggRasterCommand{command: cmd})
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
		if cmd.EvenOdd && cmd.Stroke == nil {
			paint.FillRule = gg.FillRuleEvenOdd
		}
		if cmd.Clip != nil {
			mask, err := rasterClipMask(GGBackend{}, page, cmd.Clip, &scene.masks)
			if err != nil {
				return nil, err
			}
			if mask.Rect.Empty() {
				continue
			}
			paint.ClipMask, paint.ClipMaskW, paint.ClipMaskH = mask.Pix, mask.Stride, mask.Rect.Dy()
			paint.ClipMaskX, paint.ClipMaskY = mask.Rect.Min.X, mask.Rect.Min.Y
		}
		if cmd.Stroke != nil {
			style, err := ggStroke(*cmd.Stroke, page.DPI/25.4)
			if err != nil {
				return nil, err
			}
			paint.SetStroke(style)
			paint.SetStrokeBrush(paint.FillBrush())
		}
		scene.commands = append(scene.commands, ggRasterCommand{command: cmd, path: path, paint: paint})
	}
	return scene, nil
}

// ggRasterCommand 保存已转换的像素路径和样式
type ggRasterCommand struct {
	command RasterCommand
	path    *gg.Path
	paint   *gg.Paint
}

// ggRasterScene 复用路径和局部裁剪，串行保护掩码缓存
type ggRasterScene struct {
	mu            sync.Mutex
	page          *RasterPage
	width, height int
	commands      []ggRasterCommand
	masks         renderCache[[32]byte, *image.Alpha]
}

// Render 绘制独立像素缓冲，不累积上次内容
// 返回: image.Image 图像, error 绘制错误
func (s *ggRasterScene) Render() (image.Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pixmap := gg.NewPixmap(s.width, s.height)
	img := pixmap.ImageView()
	raster := gg.NewSoftwareRenderer(s.width, s.height)
	for i, cmd := range s.commands {
		var err error
		switch {
		case cmd.command.Image != nil:
			err = drawRasterImageCommand(GGBackend{}, s.page, img, imagePixelSource(cmd.command.Image), cmd.command, &s.masks)
		case cmd.command.Stroke != nil:
			err = raster.Stroke(pixmap, cmd.path, cmd.paint)
		default:
			err = raster.Fill(pixmap, cmd.path, cmd.paint)
		}
		if err != nil {
			return nil, fmt.Errorf("gg command %d: %w", i, err)
		}
	}
	return img, nil
}

// Render 准备并绘制页面，重复绘制可使用Prepare复用场景
// 入参: page 只读页面
// 返回: image.Image 图像, error 绘制错误
func (b GGBackend) Render(page *RasterPage) (image.Image, error) {
	scene, err := b.Prepare(page)
	if err != nil {
		return nil, err
	}
	return scene.Render()
}

// ggStroke 将页面毫米样式转换为GG像素描边，虚线与线宽同步换算
// 入参: options 描边样式, scale 每毫米像素数
// 返回: gg.Stroke 像素样式, error 样式错误
func ggStroke(options StrokeOptions, scale float64) (gg.Stroke, error) {
	if err := validateStroke(options); err != nil {
		return gg.Stroke{}, err
	}
	style := gg.DefaultStroke().WithWidth(options.Width * scale)
	style.MiterLimit = options.MiterLimit
	if style.MiterLimit == 0 {
		style.MiterLimit = defaultMiterLimit
	}
	switch options.Cap {
	case "Round":
		style.Cap = gg.LineCapRound
	case "Square":
		style.Cap = gg.LineCapSquare
	}
	switch options.Join {
	case "Round":
		style.Join = gg.LineJoinRound
	case "Bevel":
		style.Join = gg.LineJoinBevel
	}
	if len(options.Dashes) > 0 {
		dashes := make([]float64, len(options.Dashes))
		for i, value := range options.Dashes {
			dashes[i] = value * scale
		}
		style = style.WithDashPattern(dashes...).WithDashOffset(options.DashOffset * scale)
	}
	return style, nil
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
