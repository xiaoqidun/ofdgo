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

import "math"

// geometryConvexContains 判断单个非退化凸直线区域是否包含另一闭合直线区域
// 入参: outer 外部区域, inner 内部区域
// 返回: bool 是否完全包含，曲线及多轮廓返回false
func geometryConvexContains(outer, inner GeometryPath) bool {
	a, b := geometryPolygonPoints(outer), geometryPolygonPoints(inner)
	if len(a) < 3 || len(b) < 3 {
		return false
	}
	nondegenerate := false
	for i, start := range a {
		end := a[(i+1)%len(a)]
		dx, dy := end.X-start.X, end.Y-start.Y
		length := math.Hypot(dx, dy)
		if !finite(length) {
			return false
		}
		if length == 0 {
			continue
		}
		dx, dy = dx/length, dy/length
		distance := func(point Point) float64 {
			x, y := point.X-start.X, point.Y-start.Y
			value := math.FMA(dx, y, -dy*x)
			bound := (math.Abs(dx*y) + math.Abs(dy*x) + math.Abs(dx)*(math.Abs(point.Y)+math.Abs(start.Y)) + math.Abs(dy)*(math.Abs(point.X)+math.Abs(start.X))) * 0x1p-49
			if finite(bound) && math.Abs(value) <= bound {
				return 0
			}
			return value
		}
		sign := 0.0
		for _, point := range a {
			value := distance(point)
			if !finite(value) || sign*value < 0 {
				return false
			}
			if value != 0 {
				sign, nondegenerate = math.Copysign(1, value), true
			}
		}
		for _, point := range b {
			value := distance(point)
			if !finite(value) || sign*value < 0 {
				return false
			}
		}
	}
	return nondegenerate
}

// geometryPolygonPoints 读取单个显式闭合直线轮廓的顶点，不修改原路径
// 入参: path 直线轮廓
// 返回: []Point 独立顶点，其他路径返回nil
func geometryPolygonPoints(path GeometryPath) []Point {
	if len(path) < 4 || path[0].Verb != GeometryMove || path[len(path)-1].Verb != GeometryClose {
		return nil
	}
	for i, segment := range path[:len(path)-1] {
		if i > 0 && segment.Verb != GeometryLine || !finite(segment.End.X) || !finite(segment.End.Y) {
			return nil
		}
	}
	points := make([]Point, len(path)-1)
	for i := range points {
		points[i] = path[i].End
	}
	return points
}
