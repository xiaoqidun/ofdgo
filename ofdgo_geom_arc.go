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
	"math"
)

// Curves 按毫米误差上限将端点式椭圆弧转换为三次贝塞尔曲线，其他指令保持原样
// 入参: tolerance 正有限误差上限
// 返回: GeometryPath 独立路径, error 参数错误
func (p GeometryPath) Curves(tolerance float64) (GeometryPath, error) {
	if !rasterPositive(tolerance) {
		return nil, fmt.Errorf("invalid curve tolerance")
	}
	result := make(GeometryPath, 0, len(p))
	var current, start Point
	for _, s := range p {
		switch s.Verb {
		case GeometryMove:
			start = s.End
		case GeometryArc:
			curves, err := arcCurves(current, s, tolerance)
			if err != nil {
				return nil, err
			}
			result = append(result, curves...)
			current = s.End
			continue
		case GeometryClose:
			result = append(result, s)
			current = start
			continue
		case GeometryLine, GeometryQuad, GeometryCubic:
		default:
			return nil, fmt.Errorf("unsupported geometry command %d", s.Verb)
		}
		result = append(result, s)
		current = s.End
	}
	return result, nil
}

// arcCurves 按标准端点参数求椭圆中心，只在绘制边界近似弧线
// 入参: start 起点, arc 弧线, tolerance 毫米误差上限
// 返回: GeometryPath 贝塞尔片段, error 无效弧线
func arcCurves(start Point, arc GeometrySegment, tolerance float64) (GeometryPath, error) {
	rx, ry := arc.RadiusX, arc.RadiusY
	if !finite(rx) || !finite(ry) || rx < 0 || ry < 0 || !finite(arc.Rotation) ||
		!finite(start.X) || !finite(start.Y) || !finite(arc.End.X) || !finite(arc.End.Y) {
		return nil, fmt.Errorf("invalid arc parameters")
	}
	if start == arc.End {
		return nil, nil
	}
	if rx == 0 || ry == 0 {
		return GeometryPath{{Verb: GeometryLine, End: arc.End}}, nil
	}
	angle := math.Mod(arc.Rotation, 360) * math.Pi / 180
	sine, cosine := math.Sincos(angle)
	dx, dy := (start.X-arc.End.X)/2, (start.Y-arc.End.Y)/2
	x, y := cosine*dx+sine*dy, -sine*dx+cosine*dy
	scale := math.Hypot(x/rx, y/ry)
	if scale > 1 {
		rx *= scale
		ry *= scale
	}
	u, v := x/rx, y/ry
	square := u*u + v*v
	factor := math.Sqrt(math.Max(0, (1-square)/square))
	if arc.Large == arc.Sweep {
		factor = -factor
	}
	cx, cy := factor*rx*v, -factor*ry*u
	theta := math.Atan2((y-cy)/ry, (x-cx)/rx)
	end := math.Atan2((-y-cy)/ry, (-x-cx)/rx)
	delta := math.Mod(end-theta, 2*math.Pi)
	if arc.Sweep && delta < 0 {
		delta += 2 * math.Pi
	}
	if !arc.Sweep && delta > 0 {
		delta -= 2 * math.Pi
	}
	center := Point{cosine*cx - sine*cy + (start.X+arc.End.X)/2, sine*cx + cosine*cy + (start.Y+arc.End.Y)/2}
	step := math.Min(math.Pi/2, math.Pow(tolerance/math.Max(rx, ry)*40000, 1.0/6))
	count := int(math.Ceil(math.Abs(delta) / step))
	if count < 1 || !finite(center.X) || !finite(center.Y) {
		return nil, fmt.Errorf("invalid arc extent")
	}
	step = delta / float64(count)
	point := func(x, y float64) Point {
		return Point{center.X + rx*cosine*x - ry*sine*y, center.Y + rx*sine*x + ry*cosine*y}
	}
	result := make(GeometryPath, 0, count)
	for i := 0; i < count; i++ {
		a, b := theta+float64(i)*step, theta+float64(i+1)*step
		sa, ca := math.Sincos(a)
		sb, cb := math.Sincos(b)
		k := 4.0 / 3 * math.Tan(step/4)
		result = append(result, GeometrySegment{Verb: GeometryCubic, Control1: point(ca-k*sa, sa+k*ca), Control2: point(cb+k*sb, sb-k*cb), End: point(cb, sb)})
	}
	result[len(result)-1].End = arc.End
	return result, nil
}
