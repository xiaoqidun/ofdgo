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

package geom

import (
	"fmt"
	"math"
)

// Curves 按毫米误差上限将端点式椭圆弧转换为三次贝塞尔曲线，其他指令保持原样
// 入参: tolerance 正有限误差上限
// 返回: Path 独立路径, error 参数错误
func (p Path) Curves(tolerance float64) (Path, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !positive(tolerance) {
		return nil, fmt.Errorf("invalid curve tolerance")
	}
	result := make(Path, 0, len(p))
	var current, start Point
	for _, s := range p {
		switch s.Verb {
		case Move:
			start = s.End
		case Arc:
			curves, err := ArcCurves(current, s, tolerance)
			if err != nil {
				return nil, err
			}
			result = append(result, curves...)
			current = s.End
			continue
		case ClosePath:
			result = append(result, s)
			current = start
			continue
		case Line, Quad, Cubic:
		default:
			return nil, fmt.Errorf("unsupported geometry command %d", s.Verb)
		}
		result = append(result, s)
		current = s.End
	}
	return result, nil
}

// ArcCurves 按标准端点参数求椭圆中心，只在绘制边界近似弧线
// 入参: start 起点, arc 弧线, tolerance 毫米误差上限
// 返回: Path 贝塞尔片段, error 无效弧线
func ArcCurves(start Point, arc Segment, tolerance float64) (Path, error) {
	a, err := centerArc(start, arc)
	if err != nil {
		return nil, err
	}
	if start == arc.End {
		return nil, nil
	}
	if arc.RadiusX == 0 || arc.RadiusY == 0 {
		return Path{{Verb: Line, End: arc.End}}, nil
	}
	radius := math.Max(math.Hypot(a.u.X, a.u.Y), math.Hypot(a.v.X, a.v.Y))
	step := math.Min(math.Pi/2, math.Pow(tolerance/radius*40000, 1.0/6))
	n := math.Ceil(math.Abs(a.delta) / step)
	if !finite(n) || n < 1 || n > 1<<18 {
		return nil, fmt.Errorf("arc subdivision limit exceeded")
	}
	count := int(n)
	step = a.delta / float64(count)
	point := func(x, y float64) Point {
		return Point{a.center.X + a.u.X*x + a.v.X*y, a.center.Y + a.u.Y*x + a.v.Y*y}
	}
	result := make(Path, 0, count)
	for i := 0; i < count; i++ {
		sa, ca := math.Sincos(a.theta + float64(i)*step)
		sb, cb := math.Sincos(a.theta + float64(i+1)*step)
		k := 4.0 / 3 * math.Tan(step/4)
		result = append(result, Segment{Verb: Cubic, Control1: point(ca-k*sa, sa+k*ca), Control2: point(cb+k*sb, sb-k*cb), End: point(cb, sb)})
	}
	result[len(result)-1].End = arc.End
	return result, nil
}

// geometryArc 保存中心式椭圆及有向参数区间
type geometryArc struct {
	center, u, v Point
	theta, delta float64
}

// point 计算椭圆参数对应的坐标
// 入参: angle 参数角
// 返回: Point 椭圆上的点
func (a geometryArc) point(angle float64) Point {
	s, c := math.Sincos(angle)
	return Point{a.center.X + a.u.X*c + a.v.X*s, a.center.Y + a.u.Y*c + a.v.Y*s}
}

// fraction 判断参数角是否位于有向弧段内
// 入参: angle 参数角
// 返回: float64 弧段比例
func (a geometryArc) fraction(angle float64) float64 {
	d := math.Mod(angle-a.theta, 2*math.Pi)
	if a.delta > 0 && d < 0 {
		d += 2 * math.Pi
	} else if a.delta < 0 && d > 0 {
		d -= 2 * math.Pi
	}
	return d / a.delta
}

// centerArc 将端点式圆弧解析为中心式，统一半径修正和方向语义
// 入参: start 起点, arc 弧线
// 返回: geometryArc 中心式参数, error 无效参数或数值溢出
func centerArc(start Point, arc Segment) (geometryArc, error) {
	rx, ry := arc.RadiusX, arc.RadiusY
	if !finite(rx) || !finite(ry) || rx < 0 || ry < 0 || !finite(arc.Rotation) ||
		!finite(start.X) || !finite(start.Y) || !finite(arc.End.X) || !finite(arc.End.Y) {
		return geometryArc{}, fmt.Errorf("invalid arc parameters")
	}
	if start == arc.End {
		return geometryArc{}, nil
	}
	if rx == 0 || ry == 0 {
		return geometryArc{}, nil
	}
	angle := math.Mod(arc.Rotation, 360) * math.Pi / 180
	sine, cosine := math.Sincos(angle)
	dx, dy := arcCoordinateMean(start.X, -arc.End.X), arcCoordinateMean(start.Y, -arc.End.Y)
	x, y := cosine*dx+sine*dy, -sine*dx+cosine*dy
	scale := math.Hypot(x/rx, y/ry)
	if scale > 1 {
		rx *= scale
		ry *= scale
	}
	u, v := x/rx, y/ry
	chord := math.Hypot(u, v)
	if !finite(chord) || chord == 0 {
		return geometryArc{}, fmt.Errorf("invalid arc extent")
	}
	root := math.Sqrt(math.Max(0, (1-chord)*(1+chord)))
	sign := 1.0
	if arc.Large == arc.Sweep {
		sign = -1
	}
	cx, cy := sign*root*(v/chord)*rx, -sign*root*(u/chord)*ry
	theta := math.Atan2((y-cy)/ry, (x-cx)/rx)
	delta := 2 * math.Atan2(chord, root)
	if arc.Large {
		delta = 2*math.Pi - delta
	}
	if !arc.Sweep {
		delta = -delta
	}
	center := Point{cosine*cx - sine*cy + arcCoordinateMean(start.X, arc.End.X), sine*cx + cosine*cy + arcCoordinateMean(start.Y, arc.End.Y)}
	if !finite(center.X) || !finite(center.Y) || !finite(rx) || !finite(ry) || !finite(delta) || !finite(theta) || delta == 0 {
		return geometryArc{}, fmt.Errorf("invalid arc extent")
	}
	return geometryArc{center, Point{rx * cosine, rx * sine}, Point{-ry * sine, ry * cosine}, theta, delta}, nil
}

// arcCoordinateMean 计算有限坐标均值，仅在求和溢出时先减半，避免微小坐标提前下溢
// 入参: a 第一个坐标, b 第二个坐标
// 返回: float64 坐标均值
func arcCoordinateMean(a, b float64) float64 {
	value := (a + b) / 2
	if math.IsInf(value, 0) {
		return a/2 + b/2
	}
	return value
}
