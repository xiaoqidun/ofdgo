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
	"strconv"

	"github.com/tdewolff/canvas"
)

type shdPaint struct {
	gradient canvas.Gradient
	extend   int
	mapType  string
	period   float64
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
	return &shdPaint{gradient: gradient, extend: flags, mapType: mapType, period: period}
}

// resolveShdPaint 解析渐变画刷和裁剪区域
// 入参: ctx 画布上下文, paint 画刷
// 返回: any 画刷, *canvas.Path 裁剪区域
func resolveShdPaint(ctx *canvas.Context, paint any) (any, *canvas.Path) {
	s, ok := paint.(*shdPaint)
	if !ok {
		return paint, nil
	}
	switch g := s.gradient.(type) {
	case *canvas.LinearGradient:
		clip := axialShdClip(ctx, g, s.extend)
		if s.mapType != "Reflect" {
			return g, clip
		}
		d := g.End.Sub(g.Start)
		axis := canvas.Matrix{{d.X, -d.Y, g.Start.X}, {d.Y, d.X, g.Start.Y}}
		area := shdCanvasBounds(ctx).Transform(axis.Inv())
		lo, hi := s.rangeLimits(area.X0, area.X1)
		if hi <= lo {
			return g, &canvas.Path{}
		}
		gradient := reflectShdGrad(g.Grad, s.period, lo, hi)
		return gradient.ToLinear(g.Start.Add(d.Mul(lo)), g.Start.Add(d.Mul(hi))), clip
	case *canvas.RadialGradient:
		d, dr := g.C1.Sub(g.C0), g.R1-g.R0
		length := d.Length()
		if length >= math.Abs(dr) {
			return g, nil
		}
		clip := radialShdClip(ctx, g, s.extend)
		if s.mapType != "Reflect" {
			return g, clip
		}
		lo, hi := 0.0, 1.0
		if s.extend != 0 {
			distance := 0.0
			bounds := shdCanvasBounds(ctx)
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
			return g, &canvas.Path{}
		}
		gradient := reflectShdGrad(g.Grad, s.period, lo, hi)
		return gradient.ToRadial(g.C0.Add(d.Mul(lo)), math.Max(0, g.R0+dr*lo), g.C0.Add(d.Mul(hi)), math.Max(0, g.R0+dr*hi)), clip
	}
	return s.gradient, nil
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
// 入参: ctx 画布上下文, g 径向渐变, extend 延伸方向
// 返回: *canvas.Path 裁剪区域
func radialShdClip(ctx *canvas.Context, g *canvas.RadialGradient, extend int) *canvas.Path {
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
		clip = shdCanvasBounds(ctx).ToPath()
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
