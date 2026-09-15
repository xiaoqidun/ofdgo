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

// ShapeKind 基本图形类型
type ShapeKind string

const (
	ShapeLine      ShapeKind = "line"
	ShapeRectangle ShapeKind = "rectangle"
	ShapeEllipse   ShapeKind = "ellipse"
)

// NewShape 创建使用标准紧缩路径的图形，默认黑色描边、不填充。
// 可调整返回对象的颜色、线宽等属性后通过 Editor.AddObject 写入。
// 入参: kind 图形类型, box 图形范围；直线从 (X,Y) 到 (X+W,Y+H)，W、H 可为负或零
// 返回: PathObject 路径对象, error 错误信息
func NewShape(kind ShapeKind, box Box) (PathObject, error) {
	if !finite(box.X) || !finite(box.Y) || !finite(box.W) || !finite(box.H) || !finite(box.X+box.W) || !finite(box.Y+box.H) {
		return PathObject{}, fmt.Errorf("shape coordinates must be finite")
	}
	object := PathObject{LineWidth: defaultPathLineWidth}
	switch kind {
	case ShapeLine:
		if box.W == 0 && box.H == 0 {
			return PathObject{}, fmt.Errorf("line endpoints must differ")
		}
		// 为水平、垂直直线保留有效的正尺寸边界，路径端点不变。
		x, y := math.Min(0, box.W)-defaultPathLineWidth/2, math.Min(0, box.H)-defaultPathLineWidth/2
		object.AbbreviatedData = fmt.Sprintf("M %g %g L %g %g", -x, -y, box.W-x, box.H-y)
		box = Box{X: box.X + x, Y: box.Y + y, W: math.Abs(box.W) + defaultPathLineWidth, H: math.Abs(box.H) + defaultPathLineWidth}
	case ShapeRectangle, ShapeEllipse:
		if box.W <= 0 || box.H <= 0 {
			return PathObject{}, fmt.Errorf("shape dimensions must be positive")
		}
		if kind == ShapeRectangle {
			object.AbbreviatedData = fmt.Sprintf("M 0 0 L %g 0 L %g %g L 0 %g C", box.W, box.W, box.H, box.H)
		} else {
			rx, ry := box.W/2, box.H/2
			object.AbbreviatedData = fmt.Sprintf("M 0 %g A %g %g 0 0 1 %g %g A %g %g 0 0 1 0 %g C", ry, rx, ry, box.W, ry, rx, ry, ry)
		}
	default:
		return PathObject{}, fmt.Errorf("unsupported shape %q", kind)
	}
	object.Boundary = fmt.Sprintf("%g %g %g %g", box.X, box.Y, box.W, box.H)
	return object, nil
}
