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
	"image/color"
	"math"

	"github.com/gogpu/gg"
)

// ggGradientBrush 仅在颜色插值及延伸等价时使用原生渐变，否则保留OFD采样
// 入参: gradient 局部渐变, matrix 局部到像素变换
// 返回: gg.Brush 画刷, error 类型或变换错误
func ggGradientBrush(gradient *RasterGradient, matrix RasterMatrix) (gg.Brush, error) {
	if gradient.Kind != RasterLinear && gradient.Kind != RasterRadial {
		return nil, fmt.Errorf("unsupported gradient %d", gradient.Kind)
	}
	inverse, err := matrix.Inverse()
	if err != nil {
		return nil, err
	}
	if brush := ggNativeGradient(gradient, matrix, inverse); brush != nil {
		return brush, nil
	}
	return gg.NewCustomBrush(func(x, y float64) gg.RGBA {
		p := inverse.Apply(RasterPoint{X: x, Y: y})
		return ggRGBA(gradient.At(p.X, p.Y))
	}), nil
}

// ggNativeGradient 将恒定非预乘色的透明度渐变映射为GG原生画刷
// GG使用线性光颜色插值，不接受改变色相、重复节点或单侧透明延伸的OFD渐变
// 入参: g 局部渐变, matrix 正变换, inverse 逆变换
// 返回: gg.Brush 等价原生画刷，不匹配时为空
func ggNativeGradient(g *RasterGradient, matrix, inverse RasterMatrix) gg.Brush {
	if len(g.Stops) < 2 {
		return nil
	}
	var reference color.RGBA
	for i, stop := range g.Stops {
		if !finite(stop.Offset) || stop.Offset < 0 || stop.Offset > 1 || i > 0 && stop.Offset <= g.Stops[i-1].Offset {
			return nil
		}
		if stop.Color.A > reference.A {
			reference = stop.Color
		}
	}
	if reference.A == 0 {
		return nil
	}
	for _, stop := range g.Stops {
		c := stop.Color
		if uint32(c.R)*uint32(reference.A) != uint32(reference.R)*uint32(c.A) ||
			uint32(c.G)*uint32(reference.A) != uint32(reference.G)*uint32(c.A) ||
			uint32(c.B)*uint32(reference.A) != uint32(reference.B)*uint32(c.A) {
			return nil
		}
	}
	extend, period := gg.ExtendPad, 1.0
	if spread := g.Spread; spread != nil {
		if spread.Extend != 3 {
			return nil
		}
		switch spread.MapType {
		case "Repeat":
			extend = gg.ExtendRepeat
		case "Reflect":
			extend = gg.ExtendReflect
		}
		if extend != gg.ExtendPad && spread.Period > 0 {
			period = spread.Period
		}
	}
	stops := make([]gg.ColorStop, len(g.Stops))
	for i, stop := range g.Stops {
		c := ggRGBA(reference)
		c.A = float64(stop.Color.A) / 255
		stops[i] = gg.ColorStop{Offset: stop.Offset, Color: c}
	}
	start := matrix.Apply(g.Start)
	if g.Kind == RasterLinear {
		dx, dy := g.End.X-g.Start.X, g.End.Y-g.Start.Y
		length := dx*dx + dy*dy
		if !rasterPositive(length) {
			return nil
		}
		// 渐变参数为协向量，非等比缩放或错切不能直接变换两个端点
		x, y := (inverse[0]*dx+inverse[1]*dy)/length, (inverse[2]*dx+inverse[3]*dy)/length
		factor := period / (x*x + y*y)
		brush := gg.NewLinearGradientBrush(start.X, start.Y, start.X+x*factor, start.Y+y*factor)
		brush.Stops, brush.Extend = stops, extend
		return brush
	}
	xscale, yscale := math.Hypot(matrix[0], matrix[1]), math.Hypot(matrix[2], matrix[3])
	if g.Start != g.End || g.R0 != 0 || !rasterPositive(g.R1) ||
		math.Abs(xscale-yscale) > 1e-12*xscale || math.Abs(matrix[0]*matrix[2]+matrix[1]*matrix[3]) > 1e-12*xscale*yscale {
		return nil
	}
	brush := gg.NewRadialGradientBrush(start.X, start.Y, 0, g.R1*xscale*period)
	brush.Stops, brush.Extend = stops, extend
	return brush
}
