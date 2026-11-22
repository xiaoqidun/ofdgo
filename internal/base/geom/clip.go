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

import "math"

// polygonPoint 允许同结构的后端顶点直接参与裁剪，避免整片坐标转换
type polygonPoint interface {
	~struct{ X, Y float64 }
}

// ClipPolygon 按非退化凸窗口逐边裁剪多边形，保留原顶点绕向，不修改输入
// 入参: a 绘制顶点, b 已通过ConvexPolygon检查的裁剪顶点，均不含重复闭合点
// 返回: []P 交集顶点，少于三个顶点时不构成区域, bool 是否满足数值精度
func ClipPolygon[P polygonPoint](a, b []P) ([]P, bool) {
	for i, start := range b {
		origin, end := Point(start), Point(b[(i+1)%len(b)])
		dx, dy := end.X-origin.X, end.Y-origin.Y
		length := math.Hypot(dx, dy)
		if length == 0 {
			continue
		}
		dx, dy = dx/length, dy/length
		distance := func(p P) float64 { return convexDistance(origin, Point(p), dx, dy) }
		sign := 1.0
		for _, p := range b {
			if d := distance(p); d != 0 {
				sign = math.Copysign(1, d)
				break
			}
		}
		if len(a) == 0 {
			break
		}
		var clipped []P
		appendPoint := func(p P) {
			if len(clipped) == 0 || p != clipped[len(clipped)-1] {
				clipped = append(clipped, p)
			}
		}
		previous := a[len(a)-1]
		previousDistance := sign * distance(previous)
		if !finite(previousDistance) {
			return nil, false
		}
		for _, p := range a {
			d := sign * distance(p)
			if !finite(d) {
				return nil, false
			}
			if (d >= 0) != (previousDistance >= 0) {
				scale := math.Max(math.Abs(d), math.Abs(previousDistance))
				fraction := (previousDistance / scale) / (previousDistance/scale - d/scale)
				intersection := Lerp(Point(previous), Point(p), fraction)
				if dx == 0 {
					intersection.X = origin.X
				}
				if dy == 0 {
					intersection.Y = origin.Y
				}
				appendPoint(P(intersection))
			}
			if d >= 0 {
				appendPoint(p)
			}
			previous, previousDistance = p, d
		}
		if len(clipped) > 1 && clipped[0] == clipped[len(clipped)-1] {
			clipped = clipped[:len(clipped)-1]
		}
		a = clipped
	}
	return a, true
}

// ConvexPolygon 检查所有顶点位于各边的同一侧，不修改输入
// 入参: points 不含重复闭合点的独立顶点
// 返回: []P 原顶点，非有限距离或异侧顶点返回nil, bool 是否为非退化凸多边形
func ConvexPolygon[P polygonPoint](points []P) ([]P, bool) {
	positive := false
	for i, start := range points {
		origin, end := Point(start), Point(points[(i+1)%len(points)])
		dx, dy := end.X-origin.X, end.Y-origin.Y
		length := math.Hypot(dx, dy)
		if !finite(length) {
			return nil, false
		}
		if length == 0 {
			continue
		}
		dx, dy = dx/length, dy/length
		sign := 0.0
		for _, p := range points {
			d := convexDistance(origin, Point(p), dx, dy)
			if !finite(d) || sign*d < 0 {
				return nil, false
			}
			if d != 0 {
				sign, positive = math.Copysign(1, d), true
			}
		}
	}
	return points, positive
}

// convexDistance 按浮点乘法误差界识别共线点，不使用固定坐标容差
// 入参: start 边起点, point 待测顶点, dx 横向单位分量, dy 纵向单位分量
// 返回: float64 顶点到边的有向距离
func convexDistance(start, point Point, dx, dy float64) float64 {
	x, y := point.X-start.X, point.Y-start.Y
	distance := math.FMA(dx, y, -dy*x)
	errorBound := (math.Abs(dx*y) + math.Abs(dy*x)) * 0x1p-49
	if finite(errorBound) && math.Abs(distance) <= errorBound {
		return 0
	}
	return distance
}
