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

// InkPoint 手写采样点，X和Y为毫米，Pressure为0到1的压力
type InkPoint struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Pressure float64 `json:"pressure"`
}

// NewInk 将采样点转为标准矢量路径，等宽笔保留中心线，压感笔使用一次填充的连续轮廓
// 不推测笔压、不修改输入；单点生成圆点，页面外路径由页面边界裁切
// 入参: points 采样点, width 等宽线宽或压感最大线宽，单位毫米, pressure 是否使用压力
// 返回: PathObject 黑色笔迹，可修改颜色和透明度后插入页面或注解, error 错误信息
func NewInk(points []InkPoint, width float64, pressure bool) (PathObject, error) {
	if len(points) == 0 || !finite(width) || width <= 0 {
		return PathObject{}, fmt.Errorf("ink requires points and a positive finite width")
	}
	var path GeometryPath
	stationary := true
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i, point := range points {
		if !finite(point.X) || !finite(point.Y) || !finite(point.Pressure) || point.Pressure < 0 || point.Pressure > 1 {
			return PathObject{}, fmt.Errorf("invalid ink point %d", i)
		}
		stationary = stationary && point.X == points[0].X && point.Y == points[0].Y
		radius := 0.0
		if pressure {
			radius = width * point.Pressure / 2
		}
		x0, y0 = math.Min(x0, point.X-radius), math.Min(y0, point.Y-radius)
		x1, y1 = math.Max(x1, point.X+radius), math.Max(y1, point.Y+radius)
		if !pressure {
			verb := GeometryLine
			if i == 0 {
				verb = GeometryMove
			}
			path = append(path, GeometrySegment{Verb: verb, End: Point{X: point.X, Y: point.Y}})
			continue
		}
		if radius > 0 {
			path = append(path, inkCircle(point.X, point.Y, radius)...)
		}
		if i == 0 {
			continue
		}
		previous := points[i-1]
		r := width * previous.Pressure / 2
		dx, dy := point.X-previous.X, point.Y-previous.Y
		length := math.Hypot(dx, dy)
		if radius == 0 && r == 0 || length <= math.Abs(radius-r) {
			continue
		}
		x, y := dx/length, dy/length
		a := (r - radius) / length
		b := math.Sqrt(1 - a*a)
		nx, ny, mx, my := a*x-b*y, a*y+b*x, a*x+b*y, a*y-b*x
		start := Point{X: previous.X + r*mx, Y: previous.Y + r*my}
		path = append(path,
			GeometrySegment{Verb: GeometryMove, End: start},
			GeometrySegment{Verb: GeometryLine, End: Point{X: point.X + radius*mx, Y: point.Y + radius*my}},
			GeometrySegment{Verb: GeometryLine, End: Point{X: point.X + radius*nx, Y: point.Y + radius*ny}},
			GeometrySegment{Verb: GeometryLine, End: Point{X: previous.X + r*nx, Y: previous.Y + r*ny}},
			GeometrySegment{Verb: GeometryClose, End: start})
	}
	filled := pressure || stationary
	if !pressure && stationary {
		path = inkCircle(points[0].X, points[0].Y, width/2)
		x0, y0, x1, y1 = x0-width/2, y0-width/2, x1+width/2, y1+width/2
	}
	if len(path) == 0 {
		return PathObject{}, fmt.Errorf("ink has no visible stroke")
	}
	padding := width / 2
	boundary := Box{X: x0 - padding, Y: y0 - padding, W: math.Max(x1-x0, width) + width, H: math.Max(y1-y0, width) + width}
	for i := range path {
		path[i].End.X -= boundary.X
		path[i].End.Y -= boundary.Y
	}
	object, err := geometryClipPath(path)
	if err != nil {
		return PathObject{}, err
	}
	object.Boundary = editorBoxString(boundary)
	stroke := !filled
	object.Fill, object.Stroke = &filled, &stroke
	object.Cap, object.Join, object.LineWidth = "Round", "Round", width
	if filled {
		object.FillColor = &FillColor{Value: "0 0 0"}
	} else {
		object.StrokeColor = &StrokeColor{Value: "0 0 0"}
	}
	return object, nil
}

// inkCircle 使用两个半圆弧构造笔迹圆点，不扁平化圆形轮廓
// 入参: x、y 圆心, radius 半径
// 返回: GeometryPath 圆形路径
func inkCircle(x, y, radius float64) GeometryPath {
	start := Point{X: x + radius, Y: y}
	return GeometryPath{
		{Verb: GeometryMove, End: start},
		{Verb: GeometryArc, End: Point{X: x - radius, Y: y}, RadiusX: radius, RadiusY: radius, Sweep: true},
		{Verb: GeometryArc, End: start, RadiusX: radius, RadiusY: radius, Sweep: true},
		{Verb: GeometryClose, End: start},
	}
}
