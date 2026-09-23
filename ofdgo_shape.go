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
)

// Outline 使用默认几何后端获取SVG轮廓，自定义后端使用Renderer.PathOutline
// 返回: string SVG路径数据, error 错误信息
func (p PathObject) Outline() (string, error) {
	r := &Renderer{backends: defaultRenderBackends()}
	return r.PathOutline(p)
}

// ShapeKind 基本图形类型
type ShapeKind string

const (
	ShapeLine        ShapeKind = "line"
	ShapeArrow       ShapeKind = "arrow"
	ShapeDoubleArrow ShapeKind = "double-arrow"
	ShapeRectangle   ShapeKind = "rectangle"
	ShapeEllipse     ShapeKind = "ellipse"
)

// NewShape 创建使用标准紧缩路径的图形，默认黑色描边、不填充
// 可调整返回对象的颜色、线宽等属性后通过Editor.AddObject写入
// 水平、垂直直线使用正尺寸边界，路径端点不变
// 入参: kind 图形类型, box 图形范围；直线从(X,Y)到(X+W,Y+H)，W、H可为负或零
// 返回: PathObject 路径对象, error 错误信息
func NewShape(kind ShapeKind, box Box) (PathObject, error) {
	if !finite(box.X) || !finite(box.Y) || !finite(box.W) || !finite(box.H) || !finite(box.X+box.W) || !finite(box.Y+box.H) {
		return PathObject{}, fmt.Errorf("shape coordinates must be finite")
	}
	object := PathObject{LineWidth: defaultPathLineWidth}
	switch kind {
	case ShapeLine, ShapeArrow, ShapeDoubleArrow:
		if box.W == 0 && box.H == 0 {
			return PathObject{}, fmt.Errorf("line endpoints must differ")
		}
		points := []Point{{}, {X: box.W, Y: box.H}}
		if kind != ShapeLine {
			length := math.Hypot(box.W, box.H)
			head := math.Min(4, length/3)
			x, y := box.W/length*head, box.H/length*head
			points = append(points, Point{X: box.W - x - y*0.45, Y: box.H - y + x*0.45}, Point{X: box.W, Y: box.H}, Point{X: box.W - x + y*0.45, Y: box.H - y - x*0.45})
			if kind == ShapeDoubleArrow {
				points = append(points, Point{X: x - y*0.45, Y: y + x*0.45}, Point{}, Point{X: x + y*0.45, Y: y - x*0.45})
			}
		}
		left, top, right, bottom := 0.0, 0.0, 0.0, 0.0
		for _, point := range points {
			left, top, right, bottom = math.Min(left, point.X), math.Min(top, point.Y), math.Max(right, point.X), math.Max(bottom, point.Y)
		}
		left, top, right, bottom = left-defaultPathLineWidth/2, top-defaultPathLineWidth/2, right+defaultPathLineWidth/2, bottom+defaultPathLineWidth/2
		var data strings.Builder
		for i, point := range points {
			command := "L"
			if i == 0 || i == 2 || i == 5 {
				command = "M"
			}
			if i > 0 {
				data.WriteByte(' ')
			}
			fmt.Fprintf(&data, "%s %g %g", command, point.X-left, point.Y-top)
		}
		object.AbbreviatedData = data.String()
		box = Box{X: box.X + left, Y: box.Y + top, W: right - left, H: bottom - top}
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

// Shape 识别NewShape生成的基本路径，直线支持可逆仿射变换，矩形和椭圆要求轴对齐
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
	case (commands == "MLMLL" && len(values) == 10) || (commands == "MLMLLMLL" && len(values) == 16):
		kind = ShapeArrow
		if commands == "MLMLLMLL" {
			kind = ShapeDoubleArrow
		}
		box = Box{X: values[0], Y: values[1], W: values[2] - values[0], H: values[3] - values[1]}
		shape, err := NewShape(kind, Box{W: box.W, H: box.H})
		if err != nil || !sameShapePath(tokens, strings.Fields(shape.AbbreviatedData)) {
			return "", Box{}
		}
	case commands == "ML" && len(values) == 4 && len(tokens) == 6 && tokens[0] == "M" && tokens[3] == "L":
		kind, box = ShapeLine, Box{X: values[0], Y: values[1], W: values[2] - values[0], H: values[3] - values[1]}
	case commands == "MLLLC" && len(values) == 8:
		kind, box = ShapeRectangle, Box{W: values[2], H: values[5]}
	case commands == "MAAC" && len(values) == 16:
		kind, box = ShapeEllipse, Box{W: values[7], H: values[1] * 2}
	default:
		return "", Box{}
	}
	line := kind == ShapeLine || kind == ShapeArrow || kind == ShapeDoubleArrow
	if !line {
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
	_, invertible := m.Invert()
	if err != nil || !axisAlignedMatrix(m) && (!line || !invertible) {
		return "", Box{}
	}
	if line {
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

// ShapeFrame 获取矩形或椭圆的局部几何及到所在坐标系的变换
// 返回: ShapeKind 图形类型, Box 局部范围, Matrix 坐标变换，非基本图形返回空类型
func (p PathObject) ShapeFrame() (ShapeKind, Box, Matrix) {
	if p.CTM != "" {
		if _, err := creationNumbers(p.CTM, 6); err != nil {
			return "", Box{}, Matrix{}
		}
	}
	boundary, err := ParseBox(p.Boundary)
	matrix := TranslationMatrix(boundary.X, boundary.Y).Multiply(NewMatrix(p.CTM))
	if _, ok := matrix.Invert(); err != nil || !ok {
		return "", Box{}, Matrix{}
	}
	p.Boundary, p.CTM = "0 0 1 1", ""
	kind, box := p.Shape()
	if kind != ShapeRectangle && kind != ShapeEllipse {
		return "", Box{}, Matrix{}
	}
	return kind, box, matrix
}

// ReshapeFrame 沿矩形或椭圆自身方向调整局部范围，保留样式、变换方向和裁剪
// 入参: box ShapeFrame坐标系中的目标范围
// 返回: PathObject 调整后的路径, error 错误信息
func (p PathObject) ReshapeFrame(box Box) (PathObject, error) {
	kind, previous, matrix := p.ShapeFrame()
	if kind == "" {
		return PathObject{}, fmt.Errorf("path is not a supported framed shape")
	}
	if box == previous {
		return p, nil
	}
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return PathObject{}, err
	}
	shape, err := NewShape(kind, Box{W: box.W, H: box.H})
	if err != nil {
		return PathObject{}, err
	}
	matrix = matrix.Multiply(TranslationMatrix(box.X, box.Y))
	bounds := matrix.TransformBox(Box{W: box.W, H: box.H})
	if _, err := creationBox(editorBoxString(bounds)); err != nil {
		return PathObject{}, err
	}
	p.Boundary = editorBoxString(bounds)
	p.CTM = TranslationMatrix(-bounds.X, -bounds.Y).Multiply(matrix).String()
	p.AbbreviatedData = shape.AbbreviatedData
	return p, nil
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
		if errX != nil || errY != nil || !finite(x) || !finite(y) || math.Abs(x-y) > 1e-10*math.Max(1, math.Max(math.Abs(x), math.Abs(y))) {
			return false
		}
	}
	return true
}

// Reshape 调整基本路径在所在坐标系中的几何范围，保留对象标识、方向、缩放和绘制属性，不缩放线宽
// 返回对象可通过Editor.UpdateObject写入；仅支持Shape可识别的路径
// 入参: box 新几何范围，直线使用起点和有符号的端点位移
// 返回: PathObject 调整后的对象, error 错误信息
func (p PathObject) Reshape(box Box) (PathObject, error) {
	kind, _ := p.Shape()
	return p.reshape(kind, box)
}

// ReshapeLine 调整直线端点及箭头类型，保留绘制样式、变换及裁剪
// 入参: kind 为ShapeLine、ShapeArrow或ShapeDoubleArrow, box 起点及有符号端点位移
// 返回: PathObject 调整后的直线, error 错误信息
func (p PathObject) ReshapeLine(kind ShapeKind, box Box) (PathObject, error) {
	before, _ := p.Shape()
	if !lineShapeKind(before) || !lineShapeKind(kind) {
		return PathObject{}, fmt.Errorf("path is not a supported line")
	}
	return p.reshape(kind, box)
}

// lineShapeKind 判断基本图形是否使用直线端点
// 入参: kind 图形类型
// 返回: bool 是否为直线或箭头
func lineShapeKind(kind ShapeKind) bool {
	return kind == ShapeLine || kind == ShapeArrow || kind == ShapeDoubleArrow
}

// reshape 在原坐标变换下重建已识别基本图形的几何，不改写样式
// 入参: kind 目标类型, box 新几何范围
// 返回: PathObject 调整后的对象, error 错误信息
func (p PathObject) reshape(kind ShapeKind, box Box) (PathObject, error) {
	before, previous := p.Shape()
	if kind == "" {
		return PathObject{}, fmt.Errorf("path is not a supported basic shape")
	}
	if box == previous && kind == before {
		return p, nil
	}
	if !finite(box.X) || !finite(box.Y) || !finite(box.X+box.W) || !finite(box.Y+box.H) {
		return PathObject{}, fmt.Errorf("shape coordinates must be finite")
	}
	m := NewMatrix(p.CTM)
	var w, h float64
	line := lineShapeKind(kind)
	if m.b == 0 && m.c == 0 {
		w, h = box.W/m.a, box.H/m.d
	} else if m.a == 0 && m.d == 0 {
		w, h = box.H/m.b, box.W/m.c
	} else {
		inverse, _ := m.Invert()
		w, h = matrixVector(inverse, box.W, box.H)
	}
	if !line {
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
		if line {
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
