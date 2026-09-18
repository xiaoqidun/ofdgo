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

	"github.com/tdewolff/canvas"
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
	path := &canvas.Path{}
	stationary := true
	for i, point := range points {
		if !finite(point.X) || !finite(point.Y) || !finite(point.Pressure) || point.Pressure < 0 || point.Pressure > 1 {
			return PathObject{}, fmt.Errorf("invalid ink point %d", i)
		}
		stationary = stationary && point.X == points[0].X && point.Y == points[0].Y
		if !pressure {
			if i == 0 {
				path.MoveTo(point.X, point.Y)
			} else {
				path.LineTo(point.X, point.Y)
			}
			continue
		}
		radius := width * point.Pressure / 2
		if radius > 0 {
			path = path.Append(canvas.Circle(radius).Translate(point.X, point.Y))
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
		segment := &canvas.Path{}
		segment.MoveTo(previous.X+r*mx, previous.Y+r*my)
		segment.LineTo(point.X+radius*mx, point.Y+radius*my)
		segment.LineTo(point.X+radius*nx, point.Y+radius*ny)
		segment.LineTo(previous.X+r*nx, previous.Y+r*ny)
		segment.Close()
		path = path.Append(segment)
	}
	filled := pressure || stationary
	if !pressure && stationary {
		path = canvas.Circle(width/2).Translate(points[0].X, points[0].Y)
	}
	if path.Empty() {
		return PathObject{}, fmt.Errorf("ink has no visible stroke")
	}
	box := path.Bounds()
	padding := width / 2
	boundary := Box{X: box.X0 - padding, Y: box.Y0 - padding, W: math.Max(box.W(), width) + width, H: math.Max(box.H(), width) + width}
	object := editorClipPath(path.Translate(-boundary.X, -boundary.Y))
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
