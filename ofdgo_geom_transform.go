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
	"slices"
)

// validate 校验公共路径指令、子路径起点和有限坐标
// 返回: error 无效路径
func (p GeometryPath) validate() error {
	started := false
	for _, s := range p {
		points := []Point{s.End}
		switch s.Verb {
		case GeometryMove:
			started = true
		case GeometryLine:
		case GeometryQuad:
			points = append(points, s.Control1)
		case GeometryCubic:
			points = append(points, s.Control1, s.Control2)
		case GeometryArc:
			if !finite(s.RadiusX) || !finite(s.RadiusY) || !finite(s.Rotation) || s.RadiusX < 0 || s.RadiusY < 0 {
				return fmt.Errorf("invalid arc parameters")
			}
		case GeometryClose:
			points = nil
		default:
			return fmt.Errorf("unsupported geometry command %d", s.Verb)
		}
		if !started {
			return fmt.Errorf("geometry path must start with move")
		}
		for _, point := range points {
			if !finite(point.X) || !finite(point.Y) {
				return fmt.Errorf("non-finite geometry coordinate")
			}
		}
	}
	return nil
}

// Transform 精确变换控制点和椭圆轴，退化弧沿投影极值分段保留折返
// 入参: matrix 仿射变换
// 返回: GeometryPath 独立路径, error 无效路径或数值溢出
func (p GeometryPath) Transform(matrix Matrix) (GeometryPath, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	for _, value := range matrix.Values() {
		if !finite(value) {
			return nil, fmt.Errorf("invalid geometry transform")
		}
	}
	matrixScale := math.Max(math.Max(math.Abs(matrix.a), math.Abs(matrix.b)), math.Max(math.Abs(matrix.c), math.Abs(matrix.d)))
	determinant := 0.0
	if matrixScale != 0 {
		determinant = (matrix.a/matrixScale)*(matrix.d/matrixScale) - (matrix.b/matrixScale)*(matrix.c/matrixScale)
	}
	transform := func(p Point) Point {
		x, y := matrix.Transform(p.X, p.Y)
		return Point{x, y}
	}
	var result GeometryPath
	var current, start Point
	for _, s := range p {
		originalSegment := s
		original := s.End
		s.End = transform(s.End)
		switch s.Verb {
		case GeometryMove:
			start = original
		case GeometryQuad:
			s.Control1 = transform(s.Control1)
		case GeometryCubic:
			s.Control1, s.Control2 = transform(s.Control1), transform(s.Control2)
		case GeometryClose:
			original, s.End = start, transform(start)
		case GeometryArc:
			a, err := centerArc(current, originalSegment)
			if err != nil {
				return nil, err
			}
			if current == original {
				current = original
				continue
			}
			if s.RadiusX == 0 || s.RadiusY == 0 {
				s = GeometrySegment{Verb: GeometryLine, End: s.End}
			} else {
				a.center = transform(a.center)
				a.u.X, a.u.Y = matrixVector(matrix, a.u.X, a.u.Y)
				a.v.X, a.v.Y = matrixVector(matrix, a.v.X, a.v.Y)
				scale := math.Max(math.Hypot(a.u.X, a.u.Y), math.Hypot(a.v.X, a.v.Y))
				if !finite(scale) {
					return nil, fmt.Errorf("arc transform overflow")
				}
				if scale == 0 {
					if matrixScale != 0 {
						return nil, fmt.Errorf("arc transform precision exhausted")
					}
					s = GeometrySegment{Verb: GeometryLine, End: s.End}
				} else {
					ux, uy, vx, vy := a.u.X/scale, a.u.Y/scale, a.v.X/scale, a.v.Y/scale
					det := ux*vy - uy*vx
					if determinant != 0 && det == 0 {
						return nil, fmt.Errorf("arc transform precision exhausted")
					}
					if determinant == 0 {
						fractions := []float64{}
						for _, angle := range []float64{math.Atan2(a.v.X, a.u.X), math.Atan2(a.v.Y, a.u.Y)} {
							for _, angle := range []float64{angle, angle + math.Pi} {
								if f := a.fraction(angle); f > 0 && f < 1 {
									fractions = append(fractions, f)
								}
							}
						}
						slices.Sort(fractions)
						for _, f := range slices.Compact(fractions) {
							result = append(result, GeometrySegment{Verb: GeometryLine, End: a.point(a.theta + f*a.delta)})
						}
						s = GeometrySegment{Verb: GeometryLine, End: s.End}
					} else {
						xx, yy, xy := ux*ux+vx*vx, uy*uy+vy*vy, ux*uy+vx*vy
						radius := math.Sqrt((xx + yy + math.Hypot(xx-yy, 2*xy)) / 2)
						s.RadiusX, s.RadiusY = scale*radius, scale*math.Abs(det)/radius
						if s.RadiusY == 0 {
							return nil, fmt.Errorf("arc transform precision exhausted")
						}
						s.Rotation = math.Atan2(2*xy, xx-yy) * 90 / math.Pi
						if det < 0 {
							s.Sweep = !s.Sweep
						}
					}
				}
			}
		}
		result = append(result, s)
		current = original
	}
	if err := result.validate(); err != nil {
		return nil, err
	}
	return result, nil
}
