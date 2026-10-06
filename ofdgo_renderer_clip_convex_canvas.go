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

	"github.com/tdewolff/canvas"
)

// intersectConvexCanvasPaths 逐边裁剪单个凸多边形，避免仿射矩形共边进入曲线扫线
// 入参: left 左侧路径, right 右侧路径
// 返回: *canvas.Path 交集路径, bool 是否适用凸多边形裁剪
func intersectConvexCanvasPaths(left, right *canvas.Path) (*canvas.Path, bool) {
	a, ok := convexCanvasPolygon(left)
	if !ok {
		return nil, false
	}
	b, ok := convexCanvasPolygon(right)
	if !ok {
		return nil, false
	}
	return intersectCanvasPolygons(a, b)
}

// intersectLinearCanvasContours 精确裁剪直线轮廓，不展开曲线或改变填充绕数
// 入参: path 绘制路径, clip 单个凸裁剪窗口
// 返回: *canvas.Path 交集路径, bool 是否适用直线轮廓裁剪
func intersectLinearCanvasContours(path, clip *canvas.Path) (*canvas.Path, bool) {
	if path == nil {
		return nil, false
	}
	data := path.Data()
	for i := 0; i < len(data); i += 4 {
		if i+4 > len(data) || data[i] != canvas.MoveToCmd && data[i] != canvas.LineToCmd && data[i] != canvas.CloseCmd {
			return nil, false
		}
	}
	return intersectCanvasContours(path, clip)
}

// intersectCanvasContours 按凸窗口裁剪闭合轮廓，保留绕向、孔洞和重叠透明度
// 入参: path 绘制路径, clip 单个凸裁剪路径
// 返回: *canvas.Path 交集路径, bool 是否适用分段裁剪
func intersectCanvasContours(path, clip *canvas.Path) (*canvas.Path, bool) {
	if path == nil {
		return nil, false
	}
	b, ok := convexCanvasPolygon(clip)
	if !ok {
		return nil, false
	}
	parts := path.Split()
	polygons := make([][]canvas.Point, 0, len(parts))
	for _, part := range parts {
		part = part.Flatten(canvas.Tolerance)
		if !part.Closed() {
			return nil, false
		}
		a := part.Coords()
		if len(a) < 4 || a[0] != a[len(a)-1] {
			continue
		}
		for _, point := range a {
			if !finite(point.X) || !finite(point.Y) {
				return nil, false
			}
		}
		polygons = append(polygons, a[:len(a)-1])
	}
	result := &canvas.Path{}
	for _, polygon := range polygons {
		part, ok := intersectCanvasPolygons(polygon, b)
		if !ok {
			return nil, false
		}
		result = result.Append(part)
	}
	return result, true
}

// intersectCanvasPolygons 按凸窗口逐边裁剪多边形，保留原顶点绕向
// 入参: a 绘制顶点, b 裁剪顶点
// 返回: *canvas.Path 交集路径, bool 是否满足数值精度
func intersectCanvasPolygons(a, b []canvas.Point) (*canvas.Path, bool) {
	for i, start := range b {
		end := b[(i+1)%len(b)]
		dx, dy := end.X-start.X, end.Y-start.Y
		length := math.Hypot(dx, dy)
		if length == 0 {
			continue
		}
		dx, dy = dx/length, dy/length
		distance := func(p canvas.Point) float64 { return convexCanvasDistance(start, p, dx, dy) }
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
		var clipped []canvas.Point
		appendPoint := func(p canvas.Point) {
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
				intersection := geometryLerp(Point{previous.X, previous.Y}, Point{p.X, p.Y}, fraction)
				if dx == 0 {
					intersection.X = start.X
				}
				if dy == 0 {
					intersection.Y = start.Y
				}
				appendPoint(canvas.Point{X: intersection.X, Y: intersection.Y})
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
	result := &canvas.Path{}
	if len(a) >= 3 {
		result.MoveTo(a[0].X, a[0].Y)
		for _, p := range a[1:] {
			result.LineTo(p.X, p.Y)
		}
		result.Close()
	}
	return result, true
}

// convexCanvasPolygon 检查单个闭合直线轮廓的所有顶点位于各边的同一侧
// 入参: path 路径
// 返回: []canvas.Point 独立顶点, bool 是否为非退化凸多边形
func convexCanvasPolygon(path *canvas.Path) ([]canvas.Point, bool) {
	if path == nil || path.HasSubpaths() || !path.Closed() {
		return nil, false
	}
	data := path.Data()
	for i := 0; i < len(data); i += 4 {
		if i+4 > len(data) || data[i] != canvas.MoveToCmd && data[i] != canvas.LineToCmd && data[i] != canvas.CloseCmd {
			return nil, false
		}
	}
	points := path.Coords()
	if len(points) < 4 || points[0] != points[len(points)-1] {
		return nil, false
	}
	points = points[:len(points)-1]
	positive := false
	for i, start := range points {
		end := points[(i+1)%len(points)]
		dx, dy := end.X-start.X, end.Y-start.Y
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
			d := convexCanvasDistance(start, p, dx, dy)
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

// convexCanvasDistance 按浮点乘法误差界识别共线点，不使用固定坐标容差
// 入参: start 边起点, point 待测顶点, dx 横向单位分量, dy 纵向单位分量
// 返回: float64 顶点到边的有向距离
func convexCanvasDistance(start, point canvas.Point, dx, dy float64) float64 {
	x, y := point.X-start.X, point.Y-start.Y
	distance := math.FMA(dx, y, -dy*x)
	errorBound := (math.Abs(dx*y) + math.Abs(dy*x)) * 0x1p-49
	if finite(errorBound) && math.Abs(distance) <= errorBound {
		return 0
	}
	return distance
}
