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
	"math"
)

// RasterPage 已解析的光栅绘制页面，单位为毫米，页面坐标原点在左上角
// DPI决定像素尺寸和编译时的字形、描边精度，修改DPI后应重新编译
// 页面及其引用资源在渲染期间只读，不用于重建或保存OFD
type RasterPage struct {
	Width, Height float64
	DPI           float64
	Commands      []RasterCommand
}

// RasterMatrix 按a、b、c、d、e、f排列的仿射矩阵，零值不是单位矩阵
type RasterMatrix [6]float64

// RasterPoint 路径或渐变坐标
type RasterPoint struct{ X, Y float64 }

// RasterVerb 路径指令
type RasterVerb uint8

const (
	RasterMove RasterVerb = iota
	RasterLine
	RasterQuad
	RasterCubic
	RasterClose
)

// RasterSegment 路径片段，RasterMove和RasterLine使用End，RasterQuad使用Control1和End
type RasterSegment struct {
	Verb               RasterVerb
	Control1, Control2 RasterPoint
	End                RasterPoint
}

// RasterCommand 填充路径或绘制图像，Image非空时为图像指令
// Transform将局部坐标映射到页面，图像局部坐标与image.Image的像素坐标一致
// 字形和描边由页面编译器生成轮廓，裁剪与底纹在编译时解析
type RasterCommand struct {
	Path      []RasterSegment
	Paint     RasterPaint
	EvenOdd   bool
	Transform RasterMatrix
	Image     image.Image
}

// RasterPaint 预乘RGBA纯色或渐变，Gradient非空时忽略Color
type RasterPaint struct {
	Color    color.RGBA
	Gradient *RasterGradient
}

// RasterGradientKind 渐变类型
type RasterGradientKind uint8

const (
	RasterLinear RasterGradientKind = iota
	RasterRadial
)

// RasterStop 按Offset升序排列的预乘RGBA颜色节点
type RasterStop struct {
	Offset float64
	Color  color.RGBA
}

// RasterGradient 局部坐标下的线性或双圆径向渐变，区间外延续边界颜色
type RasterGradient struct {
	Kind       RasterGradientKind
	Start, End RasterPoint
	R0, R1     float64
	Stops      []RasterStop
}

// PixelSize 检查页面尺寸并计算目标像素大小
// 返回: int 宽度, int 高度, error 无效尺寸
func (p *RasterPage) PixelSize() (int, int, error) {
	if p == nil || !rasterPositive(p.Width) || !rasterPositive(p.Height) || !rasterPositive(p.DPI) {
		return 0, 0, fmt.Errorf("invalid raster page size or DPI")
	}
	scale := p.DPI / 25.4
	w, h := math.Floor(p.Width*scale+0.5), math.Floor(p.Height*scale+0.5)
	if !rasterPositive(w) || !rasterPositive(h) || w*h > math.MaxInt32/4 {
		return 0, 0, fmt.Errorf("invalid raster pixel dimensions: %g x %g", w, h)
	}
	return int(w), int(h), nil
}

// rasterPositive 判断正有限数
// 入参: v 待检查数值
// 返回: bool 是否为正有限数
func rasterPositive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

// Apply 将局部点映射到目标坐标
// 入参: p 局部点
// 返回: RasterPoint 目标点
func (m RasterMatrix) Apply(p RasterPoint) RasterPoint {
	return RasterPoint{m[0]*p.X + m[2]*p.Y + m[4], m[1]*p.X + m[3]*p.Y + m[5]}
}

// Inverse 求逆矩阵，退化或行列式非有限时返回错误
// 返回: RasterMatrix 逆矩阵, error 无效矩阵
func (m RasterMatrix) Inverse() (RasterMatrix, error) {
	det := m[0]*m[3] - m[1]*m[2]
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return RasterMatrix{}, fmt.Errorf("invalid raster transform")
	}
	return RasterMatrix{m[3] / det, -m[1] / det, -m[2] / det, m[0] / det,
		(m[2]*m[5] - m[3]*m[4]) / det, (m[1]*m[4] - m[0]*m[5]) / det}, nil
}

// At 采样局部坐标下的渐变，使用预乘颜色插值
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 采样颜色
func (g *RasterGradient) At(x, y float64) color.RGBA {
	dx, dy := g.End.X-g.Start.X, g.End.Y-g.Start.Y
	px, py := x-g.Start.X, y-g.Start.Y
	t := 0.0
	if g.Kind == RasterRadial {
		dr := g.R1 - g.R0
		a, b, c := dx*dx+dy*dy-dr*dr, -2*(px*dx+py*dy+g.R0*dr), px*px+py*py-g.R0*g.R0
		t = rasterRadialPosition(a, b, c, g.R0, dr)
	} else if length := dx*dx + dy*dy; length > 0 {
		t = (px*dx + py*dy) / length
	}
	return g.colorAt(t)
}

// rasterRadialPosition 求双圆渐变中半径非负的有效参数
// 入参: a 二次项系数, b 一次项系数, c 常数项, radius 起始半径, delta 半径增量
// 返回: float64 渐变位置，范围为0至1
func rasterRadialPosition(a, b, c, radius, delta float64) float64 {
	x, y := math.NaN(), math.NaN()
	if a == 0 {
		if b != 0 {
			x = -c / b
		}
	} else if d := b*b - 4*a*c; d >= 0 {
		q := -0.5 * (b + math.Copysign(math.Sqrt(d), b))
		if q == 0 {
			x = -b / (2 * a)
		} else {
			x, y = q/a, c/q
			if x > y {
				x, y = y, x
			}
		}
	}
	for _, t := range []float64{y, x} {
		if t >= 0 && t <= 1 && radius+delta*t >= 0 {
			return t
		}
	}
	for _, t := range []float64{x, y} {
		if t > 0 && radius+delta*t >= 0 {
			return 1
		}
	}
	return 0
}

// colorAt 插值有序颜色节点
// 入参: t 渐变位置
// 返回: color.RGBA 预乘颜色
func (g *RasterGradient) colorAt(t float64) color.RGBA {
	if len(g.Stops) == 0 {
		return color.RGBA{}
	}
	if t <= g.Stops[0].Offset {
		return g.Stops[0].Color
	}
	for i := 1; i < len(g.Stops); i++ {
		before, after := g.Stops[i-1], g.Stops[i]
		if t < after.Offset {
			u := uint32((t-before.Offset)/(after.Offset-before.Offset)*65535 + 0.5)
			blend := func(a, b uint8) uint8 { return uint8(((65535-u)*uint32(a)*257 + u*uint32(b)*257) >> 24) }
			return color.RGBA{blend(before.Color.R, after.Color.R), blend(before.Color.G, after.Color.G), blend(before.Color.B, after.Color.B), blend(before.Color.A, after.Color.A)}
		}
	}
	return g.Stops[len(g.Stops)-1].Color
}
