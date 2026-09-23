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
	"math"

	"github.com/tdewolff/canvas"
)

// parseFillPaint 将公共画刷转换为Canvas对象
// 入参: fillColor 填充颜色节点, x X坐标, y Y坐标, pageH 页面高度
// 返回: any 填充画刷
func (r *Renderer) parseFillPaint(fillColor *FillColor, x, y, pageH float64) any {
	paint := r.ResolvePaint(fillColor)
	switch paint.Kind {
	case PaintNone, PaintPattern:
		return nil
	case PaintLinear, PaintRadial:
		return canvasShading(paint, x, y, pageH)
	default:
		return paint.Color
	}
}

// parseStrokePaint 解析勾边画刷
// 入参: strokeColor 勾边颜色节点, x X坐标, y Y坐标, pageH 页面高度
// 返回: any 勾边画刷
func (r *Renderer) parseStrokePaint(strokeColor *StrokeColor, x, y, pageH float64) any {
	return r.parseFillPaint((*FillColor)(strokeColor), x, y, pageH)
}

// canvasGradient 将公共颜色节点转换为Canvas渐变
// 入参: stops 渐变分段
// 返回: canvas.Grad 渐变分段
func canvasGradient(stops []ColorStop) canvas.Grad {
	var gradient canvas.Grad
	for _, stop := range stops {
		gradient.Add(stop.Offset, stop.Color)
	}
	return gradient
}

// canvasShading 转换坐标与椭圆渐变变换，不重新解析OFD样式
// 入参: paint 公共渐变画刷, x X坐标, y Y坐标, pageH 页面高度
// 返回: *shdPaint Canvas渐变画刷
func canvasShading(paint Paint, x, y, pageH float64) *shdPaint {
	source := paint.Gradient
	gradient := canvasGradient(source.Stops)
	start := canvas.Point{X: x + source.Start.X, Y: pageH - y - source.Start.Y}
	end := canvas.Point{X: x + source.End.X, Y: pageH - y - source.End.Y}
	if paint.Kind == PaintLinear {
		return newShdPaint(gradient.ToLinear(start, end), source.Extend, source.MapType, source.MapUnit)
	}
	result := newShdPaint(gradient.ToRadial(start, source.StartRadius, end, source.EndRadius), source.Extend, source.MapType, source.MapUnit)
	if e := source.Eccentricity; 0 < e && e < 1 {
		result.view = canvas.Identity.Translate(start.X, start.Y).Rotate(-source.Angle).Scale(1, math.Sqrt(1-e*e)).Translate(-start.X, -start.Y)
		result.gradient = gradient.ToRadial(start, source.StartRadius, result.view.Inv().Dot(end), source.EndRadius)
	}
	return result
}

// axialShdClip 获取轴向渐变的延伸裁剪区域
// 入参: gradient 轴向渐变, extend 延伸方向, bounds 可见区域
// 返回: *canvas.Path 裁剪区域
func axialShdClip(gradient *canvas.LinearGradient, extend int, bounds canvas.Rect) *canvas.Path {
	if extend == 3 {
		return nil
	}
	d := gradient.End.Sub(gradient.Start)
	axis := canvas.Matrix{{d.X, -d.Y, gradient.Start.X}, {d.Y, d.X, gradient.Start.Y}}
	area := bounds.Transform(axis.Inv())
	if extend&1 == 0 {
		area.X0 = math.Max(area.X0, 0)
	}
	if extend&2 == 0 {
		area.X1 = math.Min(area.X1, 1)
	}
	if area.X1 <= area.X0 {
		return &canvas.Path{}
	}
	return area.ToPath().Transform(axis)
}
