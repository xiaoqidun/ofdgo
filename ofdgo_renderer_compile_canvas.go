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
	"image/draw"

	"github.com/tdewolff/canvas"
)

// CompilePage 使用Canvas页面解释器和配置的字体、几何后端生成独立绘制数据
// 入参: r 渲染器, page 页面内容
// 返回: *RasterPage 绘制页面, error 编译错误
func (CanvasBackend) CompilePage(r *Renderer, page *PageContent) (*RasterPage, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	result := &RasterPage{Width: box.W, Height: box.H, DPI: r.DPI}
	if _, _, err := result.PixelSize(); err != nil {
		return nil, err
	}
	geometry, err := r.Geometry()
	if err != nil {
		return nil, err
	}
	compiler := &canvasPageCompiler{page: result, geometry: geometry}
	if err := r.renderCanvasContext(canvas.NewContext(compiler), page); err != nil {
		return nil, err
	}
	if compiler.err != nil {
		return nil, compiler.err
	}
	return result, nil
}

// canvasPageCompiler 将已解释的页面绘制转换为自有数据，不保存第三方绘图对象
type canvasPageCompiler struct {
	page     *RasterPage
	geometry GeometryBackend
	err      error
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
		if len(style.Dashes) > 0 {
			offset, dashes := canvas.ScaleDash(style.StrokeWidth, style.DashOffset, style.Dashes)
			stroke = stroke.Dash(offset, dashes...)
		}
		var err error
		stroke, err = canvasStrokePath(c.geometry, stroke, canvasStrokeOptions(style.StrokeWidth, style.StrokeCapper, style.StrokeJoiner, canvas.PixelTolerance/(c.page.DPI/25.4)))
		if err != nil {
			c.err = err
			return
		}
		c.fill(stroke, style.Stroke, style.FillRule, m)
	}
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
	path = path.ReplaceArcs()
	cmd.Path = make([]RasterSegment, 0, path.Len())
	scanner := path.Scanner()
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
			c.err = fmt.Errorf("unsupported raster path command %g", scanner.Cmd())
			return
		}
		cmd.Path = append(cmd.Path, s)
	}
	c.page.Commands = append(c.page.Commands, cmd)
}

// rasterPoint 转换局部坐标点
// 入参: p 默认引擎坐标点
// 返回: RasterPoint 独立坐标点
func rasterPoint(p canvas.Point) RasterPoint { return RasterPoint{X: p.X, Y: p.Y} }

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
	img = imagePixelSource(img)
	if (m[0][1] != 0 || m[1][0] != 0) && (m[0][0] != 0 || m[1][1] == 0) {
		bounds := img.Bounds()
		padded := image.NewRGBA(image.Rect(0, 0, bounds.Dx()+8, bounds.Dy()+8))
		draw.Draw(padded, image.Rect(4, 4, bounds.Dx()+4, bounds.Dy()+4), img, bounds.Min, draw.Src)
		img = padded
		m = m.Translate(-4, -4)
	}
	h := float64(img.Bounds().Dy())
	t := RasterMatrix{m[0][0], -m[1][0], -m[0][1], m[1][1], m[0][2] + m[0][1]*h, c.page.Height - m[1][2] - m[1][1]*h}
	c.page.Commands = append(c.page.Commands, RasterCommand{Image: img, Transform: t})
}
