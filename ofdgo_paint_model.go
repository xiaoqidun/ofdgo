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

import "image/color"

// ColorStop 保存预乘RGBA颜色节点及渐变区间位置
type ColorStop struct {
	Offset float64
	Color  color.RGBA
}

// PaintKind 区分无画刷、纯色、轴向渐变、径向渐变和图案
type PaintKind uint8

const (
	PaintNone PaintKind = iota
	PaintSolid
	PaintLinear
	PaintRadial
	PaintPattern
)

// Paint 保存后端无关的颜色、渐变和图案，不修改源节点
type Paint struct {
	Kind     PaintKind
	Color    color.RGBA
	Gradient *Shading
	Pattern  *PatternPaint
}

// Shading 保存OFD对象局部坐标中的渐变，单位为毫米，纵轴向下
// Extend、MapType和MapUnit保留标准的延伸及重复语义
type Shading struct {
	Start, End                   Point
	StartRadius, EndRadius       float64
	Stops                        []ColorStop
	Extend, MapType              string
	MapUnit, Eccentricity, Angle float64
}

// ResolvePaint 解析颜色、透明度、渐变分段和图案，不依赖绘图库
// 入参: fill 填充或转换后的描边节点
// 返回: Paint 只读画刷
func (r *Renderer) ResolvePaint(fill *FillColor) Paint {
	if fill == nil {
		return Paint{}
	}
	if fill.Pattern != nil {
		return Paint{Kind: PaintPattern, Pattern: parsePatternPaint(fill)}
	}
	base := r.parseFillColor(fill)
	if base == nil {
		return Paint{}
	}
	paint := Paint{Kind: PaintSolid, Color: colorToRGBA(base)}
	if node := fill.AxialShd; node != nil {
		start, end := parseFloats(node.StartPoint), parseFloats(node.EndPoint)
		stops := r.GradientStops(node.Segment, fill.Alpha)
		if len(start) >= 2 && len(end) >= 2 && len(stops) != 0 && (!geometryEqual(start[0], end[0]) || !geometryEqual(start[1], end[1])) {
			paint.Kind = PaintLinear
			paint.Gradient = &Shading{Start: Point{X: start[0], Y: start[1]}, End: Point{X: end[0], Y: end[1]}, Stops: stops, Extend: node.Extend, MapType: node.MapType, MapUnit: node.MapUnit}
			return paint
		}
	}
	if node := fill.RadialShd; node != nil && node.EndRadius > 0 {
		start, end := parseFloats(node.StartPoint), parseFloats(node.EndPoint)
		stops := r.GradientStops(node.Segment, fill.Alpha)
		if len(start) >= 2 && len(end) >= 2 && len(stops) != 0 {
			paint.Kind = PaintRadial
			paint.Gradient = &Shading{Start: Point{X: start[0], Y: start[1]}, End: Point{X: end[0], Y: end[1]}, StartRadius: node.StartRadius, EndRadius: node.EndRadius, Stops: stops, Extend: node.Extend, MapType: node.MapType, MapUnit: node.MapUnit, Eccentricity: node.Eccentricity, Angle: node.Angle}
		}
	}
	return paint
}
