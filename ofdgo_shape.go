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
	"strconv"
	"strings"

	"github.com/tdewolff/canvas"
)

// Outline 获取页面坐标系中的SVG路径轮廓，不包含描边宽度和填充
// 入参: p 路径对象
// 返回: string SVG路径数据, error 错误信息
func (p PathObject) Outline() (string, error) {
	if err := creationPath(p.AbbreviatedData); err != nil {
		return "", err
	}
	if _, err := creationBox(p.Boundary); err != nil {
		return "", err
	}
	if p.CTM != "" {
		if _, err := creationNumbers(p.CTM, 6); err != nil {
			return "", err
		}
	}
	r := &Renderer{}
	path := r.buildPath(p, 0, NewMatrix(p.CTM), false)
	return path.Transform(canvas.Matrix{{1, 0, 0}, {0, -1, 0}}).ToSVG(), nil
}

// ShapeKind 基本图形类型
type ShapeKind string

const (
	ShapeLine      ShapeKind = "line"
	ShapeRectangle ShapeKind = "rectangle"
	ShapeEllipse   ShapeKind = "ellipse"
)

// NewShape 创建使用标准紧缩路径的图形，默认黑色描边、不填充
// 可调整返回对象的颜色、线宽等属性后通过 Editor.AddObject 写入
// 水平、垂直直线使用正尺寸边界，路径端点不变
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

// Shape 识别 NewShape 生成的基本路径及其轴对齐缩放、直角旋转、镜像和平移
// 直线范围以起点和有符号的端点位移表示；非基本路径或不支持的变换返回空类型
// 返回: ShapeKind 图形类型, Box 几何范围（不含描边）
func (p PathObject) Shape() (ShapeKind, Box) {
	tokens := strings.Fields(p.AbbreviatedData)
	var commands string
	var values []float64
	for _, token := range tokens {
		if token == "M" || token == "L" || token == "A" || token == "C" {
			commands += token
			continue
		}
		value, err := strconv.ParseFloat(token, 64)
		if err != nil || !finite(value) {
			return "", Box{}
		}
		values = append(values, value)
	}
	var kind ShapeKind
	var box Box
	switch {
	case commands == "ML" && len(values) == 4 && len(tokens) == 6 && tokens[0] == "M" && tokens[3] == "L":
		kind, box = ShapeLine, Box{X: values[0], Y: values[1], W: values[2] - values[0], H: values[3] - values[1]}
	case commands == "MLLLC" && len(values) == 8:
		kind, box = ShapeRectangle, Box{W: values[2], H: values[5]}
	case commands == "MAAC" && len(values) == 16:
		kind, box = ShapeEllipse, Box{W: values[7], H: values[1] * 2}
	default:
		return "", Box{}
	}
	if kind != ShapeLine {
		shape, err := NewShape(kind, box)
		if err != nil || !sameShapePath(tokens, strings.Fields(shape.AbbreviatedData)) {
			return "", Box{}
		}
	} else if box.W == 0 && box.H == 0 {
		return "", Box{}
	}
	if p.CTM != "" {
		if _, err := creationNumbers(p.CTM, 6); err != nil {
			return "", Box{}
		}
	}
	boundary, err := ParseBox(p.Boundary)
	m := NewMatrix(p.CTM)
	if err != nil || !axisAlignedMatrix(m) {
		return "", Box{}
	}
	if kind == ShapeLine {
		x, y := m.Transform(box.X, box.Y)
		w, h := matrixVector(m, box.W, box.H)
		box = Box{X: boundary.X + x, Y: boundary.Y + y, W: w, H: h}
	} else {
		box = TranslationMatrix(boundary.X, boundary.Y).Multiply(m).TransformBox(box)
	}
	if !finite(box.X) || !finite(box.Y) || !finite(box.W) || !finite(box.H) || !finite(box.X+box.W) || !finite(box.Y+box.H) {
		return "", Box{}
	}
	return kind, box
}

// sameShapePath 比较基本路径的命令与数值，忽略数值格式差异
// 入参: a、b 路径词元
// 返回: bool 是否相同
func sameShapePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, token := range a {
		if token == b[i] {
			continue
		}
		x, errX := strconv.ParseFloat(token, 64)
		y, errY := strconv.ParseFloat(b[i], 64)
		if errX != nil || errY != nil || x != y {
			return false
		}
	}
	return true
}

// Reshape 调整基本路径在所在坐标系中的几何范围，保留对象标识、方向、缩放和绘制属性，不缩放线宽
// 返回对象可通过 Editor.UpdateObject 写入；仅支持 Shape 可识别的路径
// 入参: box 新几何范围，直线使用起点和有符号的端点位移
// 返回: PathObject 调整后的对象, error 错误信息
func (p PathObject) Reshape(box Box) (PathObject, error) {
	kind, previous := p.Shape()
	if kind == "" {
		return PathObject{}, fmt.Errorf("path is not a supported basic shape")
	}
	if box == previous {
		return p, nil
	}
	if !finite(box.X) || !finite(box.Y) || !finite(box.X+box.W) || !finite(box.Y+box.H) {
		return PathObject{}, fmt.Errorf("shape coordinates must be finite")
	}
	m := NewMatrix(p.CTM)
	var w, h float64
	if m.b == 0 {
		w, h = box.W/m.a, box.H/m.d
	} else {
		w, h = box.H/m.b, box.W/m.c
	}
	if kind != ShapeLine {
		w, h = math.Abs(w), math.Abs(h)
	}
	shape, err := NewShape(kind, Box{W: w, H: h})
	if err != nil {
		return PathObject{}, err
	}
	boundary, _ := ParseBox(shape.Boundary)
	if m.a > 0 && m.d > 0 && m.b == 0 && m.c == 0 {
		p.Boundary = fmt.Sprintf("%g %g %g %g", box.X+boundary.X*m.a-m.e, box.Y+boundary.Y*m.d-m.f, boundary.W*m.a, boundary.H*m.d)
	} else {
		m.e, m.f = 0, 0
		var x, y float64
		if kind == ShapeLine {
			x, y = m.Transform(-boundary.X, -boundary.Y)
		} else {
			bounds := m.TransformBox(boundary)
			x, y = bounds.X, bounds.Y
		}
		m = TranslationMatrix(box.X-x, box.Y-y).Multiply(m)
		bounds := m.TransformBox(Box{W: boundary.W, H: boundary.H})
		p.Boundary = editorBoxString(bounds)
		p.CTM = TranslationMatrix(-bounds.X, -bounds.Y).Multiply(m).String()
	}
	p.AbbreviatedData = shape.AbbreviatedData
	return p, nil
}
