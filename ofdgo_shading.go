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
	"image/color"
	"math"
	"strconv"

	"github.com/tdewolff/canvas"
)

type shdPaint struct {
	gradient canvas.Gradient
	view     canvas.Matrix
	extend   int
	mapType  string
	period   float64
}

// repeatAxialGradient 轴向重复渐变
type repeatAxialGradient struct {
	*canvas.LinearGradient
	period float64
}

// At 获取重复区间内的渐变颜色
// 入参: x X坐标, y Y坐标
// 返回: color.RGBA 渐变颜色
func (g *repeatAxialGradient) At(x, y float64) color.RGBA {
	d := g.End.Sub(g.Start)
	t := (canvas.Point{X: x, Y: y}).Sub(g.Start).Dot(d) / d.Dot(d) / g.period
	return g.Grad.At(t - math.Floor(t))
}

// SetColorSpace 转换重复渐变颜色空间
// 入参: space 颜色空间
// 返回: canvas.Gradient 渐变对象
func (g *repeatAxialGradient) SetColorSpace(space canvas.ColorSpace) canvas.Gradient {
	return &repeatAxialGradient{LinearGradient: g.LinearGradient.SetColorSpace(space).(*canvas.LinearGradient), period: g.period}
}

// newShdPaint 创建渐变画刷
// 入参: gradient 渐变对象, extend 延伸方向, mapType 绘制方式, mapUnit 渐变区间长度
// 返回: *shdPaint 渐变画刷
func newShdPaint(gradient canvas.Gradient, extend, mapType string, mapUnit float64) *shdPaint {
	flags, _ := strconv.Atoi(extend)
	length := 0.0
	switch g := gradient.(type) {
	case *canvas.LinearGradient:
		length = g.End.Sub(g.Start).Length()
	case *canvas.RadialGradient:
		length = g.C1.Sub(g.C0).Length()
	}
	period := 1.0
	if mapUnit > 0 && length > 0 {
		period = mapUnit / length
	}
	return &shdPaint{gradient: gradient, view: canvas.Identity, extend: flags, mapType: mapType, period: period}
}

// resolveShdPaint 解析渐变画刷和裁剪区域
// 入参: ctx 画布上下文, paint 画刷
// 返回: any 画刷, *canvas.Path 裁剪区域, canvas.Matrix 渐变变换
func resolveShdPaint(ctx *canvas.Context, paint any) (any, *canvas.Path, canvas.Matrix) {
	s, ok := paint.(*shdPaint)
	if !ok {
		return paint, nil, canvas.Identity
	}
	if canvas.Equal(s.view.Det(), 0) {
		return nil, &canvas.Path{}, canvas.Identity
	}
	switch g := s.gradient.(type) {
	case *canvas.LinearGradient:
		bounds := shdCanvasBounds(ctx).Transform(s.view.Inv())
		clip := axialShdClip(g, s.extend, bounds)
		if clip != nil && s.view != canvas.Identity {
			clip = clip.Transform(s.view)
		}
		if s.mapType == "Repeat" {
			return &repeatAxialGradient{LinearGradient: g, period: s.period}, clip, s.view
		}
		if s.mapType != "Reflect" {
			return g, clip, s.view
		}
		d := g.End.Sub(g.Start)
		axis := canvas.Matrix{{d.X, -d.Y, g.Start.X}, {d.Y, d.X, g.Start.Y}}
		area := bounds.Transform(axis.Inv())
		lo, hi := s.rangeLimits(area.X0, area.X1)
		if hi <= lo {
			return g, &canvas.Path{}, s.view
		}
		gradient := reflectShdGrad(g.Grad, s.period, lo, hi)
		return gradient.ToLinear(g.Start.Add(d.Mul(lo)), g.Start.Add(d.Mul(hi))), clip, s.view
	case *canvas.RadialGradient:
		d, dr := g.C1.Sub(g.C0), g.R1-g.R0
		length := d.Length()
		if length >= math.Abs(dr) {
			return g, nil, s.view
		}
		bounds := shdCanvasBounds(ctx).Transform(s.view.Inv())
		clip := radialShdClip(g, s.extend, bounds)
		if clip != nil && s.view != canvas.Identity {
			clip = clip.Transform(s.view)
		}
		if s.mapType != "Reflect" {
			return g, clip, s.view
		}
		lo, hi := 0.0, 1.0
		if s.extend != 0 {
			distance := 0.0
			for _, point := range []canvas.Point{{X: bounds.X0, Y: bounds.Y0}, {X: bounds.X1, Y: bounds.Y0}, {X: bounds.X1, Y: bounds.Y1}, {X: bounds.X0, Y: bounds.Y1}} {
				distance = math.Max(distance, point.Sub(g.C0).Length())
			}
			limit := (distance + g.R0) / (math.Abs(dr) - length)
			lo, hi = s.rangeLimits(-limit, limit)
		}
		if dr > 0 {
			lo = math.Max(lo, -g.R0/dr)
		} else {
			hi = math.Min(hi, -g.R0/dr)
		}
		if hi <= lo {
			return g, &canvas.Path{}, s.view
		}
		gradient := reflectShdGrad(g.Grad, s.period, lo, hi)
		return gradient.ToRadial(g.C0.Add(d.Mul(lo)), math.Max(0, g.R0+dr*lo), g.C0.Add(d.Mul(hi)), math.Max(0, g.R0+dr*hi)), clip, s.view
	}
	return s.gradient, nil, s.view
}

// transformShdPaint 应用渐变的父级坐标变换
// 入参: paint 画刷, parentCTM 父级变换矩阵, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, boundaryInCTM 边界是否参与父级变换
func transformShdPaint(paint any, parentCTM *Matrix, bx, by, pageH float64, boundaryInCTM bool) {
	s, ok := paint.(*shdPaint)
	if !ok || parentCTM == nil {
		return
	}
	ctm := *parentCTM
	if !boundaryInCTM {
		ctm = TranslationMatrix(bx, by).Multiply(ctm).Multiply(TranslationMatrix(-bx, -by))
	}
	s.view = canvas.Matrix{
		{ctm.a, -ctm.c, ctm.c*pageH + ctm.e},
		{-ctm.b, ctm.d, pageH*(1-ctm.d) - ctm.f},
	}.Mul(s.view)
}

// drawShdPath 绘制渐变路径并保持图形轮廓不变
// 入参: ctx 画布上下文, path 填充路径, view 渐变变换
func drawShdPath(ctx *canvas.Context, path *canvas.Path, view canvas.Matrix) {
	if gradient, ok := ctx.Style.Fill.Gradient.(*repeatAxialGradient); ok {
		drawRepeatAxialPath(ctx, path, gradient, view)
		return
	}
	if view == canvas.Identity {
		ctx.DrawPath(0, 0, path)
		return
	}
	origin := ctx.CoordView().Dot(canvas.Point{})
	m := ctx.CoordSystemView().Mul(ctx.View()).Translate(origin.X, origin.Y).Mul(view)
	ctx.RenderPath(path.Copy().Transform(view.Inv()), ctx.Style, m)
}

// drawRepeatAxialPath 按周期裁剪并绘制轴向重复渐变
// 入参: ctx 画布上下文, path 填充路径, gradient 重复渐变, view 渐变变换
func drawRepeatAxialPath(ctx *canvas.Context, path *canvas.Path, gradient *repeatAxialGradient, view canvas.Matrix) {
	p := path.Copy()
	if ctx.FillRule == canvas.EvenOdd {
		p = p.Settle(canvas.EvenOdd)
	}
	p = p.Transform(view.Inv())
	d := gradient.End.Sub(gradient.Start).Mul(gradient.period)
	axis := canvas.Matrix{{d.X, -d.Y, gradient.Start.X}, {d.Y, d.X, gradient.Start.Y}}
	bounds := p.FastBounds().And(shdCanvasBounds(ctx).Transform(view.Inv()))
	area := bounds.Transform(axis.Inv())
	origin := ctx.CoordView().Dot(canvas.Point{})
	m := ctx.CoordSystemView().Mul(ctx.View()).Translate(origin.X, origin.Y).Mul(view)
	style := ctx.Style
	style.FillRule = canvas.NonZero
	stops := gradient.Grad
	if stops[0].Offset > 0 {
		stops = append(canvas.Grad{{Offset: 0, Color: stops[0].Color}}, stops...)
	}
	if last := stops[len(stops)-1]; last.Offset < 1 {
		stops = append(append(canvas.Grad(nil), stops...), canvas.Stop{Offset: 1, Color: last.Color})
	}
	for i, end := math.Floor(area.X0), math.Ceil(area.X1); i < end; i++ {
		clip := (canvas.Rect{X0: i, Y0: area.Y0, X1: i + 1, Y1: area.Y1}).ToPath().Transform(axis)
		part := applyClipPath(p, clip)
		if part.Empty() {
			continue
		}
		start := gradient.Start.Add(d.Mul(i))
		style.Fill = canvas.Paint{Gradient: stops.ToLinear(start, start.Add(d))}
		ctx.RenderPath(part, style, m)
	}
}

// rangeLimits 限制渐变延伸区间
// 入参: lo 区间起点, hi 区间终点
// 返回: float64 起点, float64 终点
func (s *shdPaint) rangeLimits(lo, hi float64) (float64, float64) {
	if s.extend&1 == 0 {
		lo = math.Max(lo, 0)
	}
	if s.extend&2 == 0 {
		hi = math.Min(hi, 1)
	}
	return lo, hi
}

// shdCanvasBounds 获取渐变坐标系下的画布边界
// 入参: ctx 画布上下文
// 返回: canvas.Rect 画布边界
func shdCanvasBounds(ctx *canvas.Context) canvas.Rect {
	origin := ctx.CoordView().Dot(canvas.Point{})
	view := ctx.CoordSystemView().Mul(ctx.View()).Translate(origin.X, origin.Y)
	if view.Det() == 0 {
		return canvas.Rect{}
	}
	width, height := ctx.Size()
	return (canvas.Rect{X1: width, Y1: height}).Transform(view.Inv())
}

// radialShdClip 获取包含双圆的径向渐变裁剪区域
// 入参: g 径向渐变, extend 延伸方向, bounds 渐变坐标系下的画布边界
// 返回: *canvas.Path 裁剪区域
func radialShdClip(g *canvas.RadialGradient, extend int, bounds canvas.Rect) *canvas.Path {
	inner, outer := g.C0, g.C1
	r0, r1 := g.R0, g.R1
	if r1 < r0 {
		inner, outer, r0, r1 = outer, inner, r1, r0
		extend = (extend&1)<<1 | (extend&2)>>1
	}
	if extend == 3 {
		return nil
	}
	var clip *canvas.Path
	if extend&2 == 0 {
		clip = canvas.Circle(r1).Translate(outer.X, outer.Y)
	} else {
		clip = bounds.ToPath()
	}
	if extend&1 == 0 && r0 > 0 {
		clip = clip.Not(canvas.Circle(r0).Translate(inner.X, inner.Y))
	}
	return clip
}

// reflectShdGrad 构造镜像渐变颜色分段
// 入参: gradient 颜色分段, period 周期长度, lo 起点, hi 终点
// 返回: canvas.Grad 镜像颜色分段
func reflectShdGrad(gradient canvas.Grad, period, lo, hi float64) canvas.Grad {
	base := append(canvas.Grad{{Offset: 0, Color: gradient.At(0)}}, gradient...)
	base = append(base, canvas.Stop{Offset: 1, Color: gradient.At(1)})
	reversed := make(canvas.Grad, len(base))
	for i, stop := range base {
		reversed[len(base)-1-i] = canvas.Stop{Offset: 1 - stop.Offset, Color: stop.Color}
	}
	result := canvas.Grad{{Offset: 0, Color: gradient.At(reflectShdPosition(lo / period))}}
	for i, last := math.Floor(lo/period), math.Ceil(hi/period); i < last; i++ {
		stops := base
		if math.Mod(i, 2) != 0 {
			stops = reversed
		}
		for _, stop := range stops {
			position := (i + stop.Offset) * period
			offset := (position - lo) / (hi - lo)
			if lo < position && position < hi && result[len(result)-1].Offset < offset {
				result = append(result, canvas.Stop{Offset: offset, Color: stop.Color})
			}
		}
	}
	return append(result, canvas.Stop{Offset: 1, Color: gradient.At(reflectShdPosition(hi / period))})
}

// reflectShdPosition 获取镜像周期内的位置
// 入参: position 渐变位置
// 返回: float64 周期内的位置
func reflectShdPosition(position float64) float64 {
	index := math.Floor(position)
	position -= index
	if math.Mod(index, 2) != 0 {
		return 1 - position
	}
	return position
}
