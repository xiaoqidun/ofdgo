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

// Bounds 通过解析极值计算直线、贝塞尔和椭圆弧的轴对齐边界
// 不进行曲线展开且不使用Tolerance，结果受float64舍入误差影响，不保证向外舍入
// 返回: Box 浮点路径范围, error 无效路径、弧线或数值溢出
func (p GeometryPath) Bounds() (Box, error) {
	if err := p.validate(); err != nil {
		return Box{}, err
	}
	left, top, right, bottom := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	add := func(p Point) {
		left, top = math.Min(left, p.X), math.Min(top, p.Y)
		right, bottom = math.Max(right, p.X), math.Max(bottom, p.Y)
	}
	var current, start Point
	for _, s := range p {
		switch s.Verb {
		case GeometryMove:
			start = s.End
		case GeometryClose:
			s.End = start
		case GeometryArc:
			a, err := centerArc(current, s)
			if err != nil {
				return Box{}, err
			}
			if a.delta != 0 {
				for _, angle := range []float64{math.Atan2(a.v.X, a.u.X), math.Atan2(a.v.Y, a.u.Y)} {
					for _, angle := range []float64{angle, angle + math.Pi} {
						if f := a.fraction(angle); f > 0 && f < 1 {
							add(a.point(angle))
						}
					}
				}
			}
		case GeometryQuad, GeometryCubic:
			for _, axis := range [][4]float64{{current.X, s.Control1.X, s.Control2.X, s.End.X}, {current.Y, s.Control1.Y, s.Control2.Y, s.End.Y}} {
				if s.Verb == GeometryQuad {
					axis[2] = 0
				}
				scale := math.Max(math.Abs(axis[0]), math.Max(math.Abs(axis[1]), math.Max(math.Abs(axis[2]), math.Abs(axis[3]))))
				if scale != 0 {
					for i := range axis {
						axis[i] /= scale
					}
				}
				var roots []float64
				if s.Verb == GeometryQuad {
					roots = geometryRoots(0, axis[0]-2*axis[1]+axis[3], axis[1]-axis[0])
				} else {
					roots = geometryRoots(-axis[0]+3*axis[1]-3*axis[2]+axis[3], 2*(axis[0]-2*axis[1]+axis[2]), axis[1]-axis[0])
				}
				for _, t := range roots {
					if t > 0 && t < 1 {
						q := geometryLerp(current, s.Control1, t)
						if s.Verb == GeometryQuad {
							q = geometryLerp(q, geometryLerp(s.Control1, s.End, t), t)
						} else {
							r := geometryLerp(s.Control1, s.Control2, t)
							q = geometryLerp(geometryLerp(q, r, t), geometryLerp(r, geometryLerp(s.Control2, s.End, t), t), t)
						}
						add(q)
					}
				}
			}
		}
		add(s.End)
		current = s.End
	}
	if len(p) == 0 {
		return Box{}, nil
	}
	if !finite(left) || !finite(top) || !finite(right-left) || !finite(bottom-top) {
		return Box{}, fmt.Errorf("geometry bounds overflow")
	}
	return Box{left, top, right - left, bottom - top}, nil
}

// geometryRoots 稳定求解一元二次方程的实根
// 入参: a、b、c 方程系数
// 返回: []float64 实根
func geometryRoots(a, b, c float64) []float64 {
	scale := math.Max(math.Abs(a), math.Max(math.Abs(b), math.Abs(c)))
	if scale == 0 {
		return nil
	}
	a, b, c = a/scale, b/scale, c/scale
	if a == 0 {
		if b == 0 {
			return nil
		}
		return []float64{-c / b}
	}
	d := b*b - 4*a*c
	if d < 0 {
		return nil
	}
	q := -.5 * (b + math.Copysign(math.Sqrt(d), b))
	if q == 0 {
		return []float64{-b / (2 * a)}
	}
	return []float64{q / a, c / q}
}

// geometryLerp 线性插值坐标
// 入参: a、b 端点, t 插值比例
// 返回: Point 插值坐标
func geometryLerp(a, b Point, t float64) Point {
	return Point{(1-t)*a.X + t*b.X, (1-t)*a.Y + t*b.Y}
}
