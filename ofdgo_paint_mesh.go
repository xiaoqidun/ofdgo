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
	"image/color"
	"math"
)

// GouraudShd 高洛德渐变，按EdgeFlag连接三角形
type GouraudShd struct {
	Extend    int        `xml:"Extend,attr,omitempty"`
	Point     []ShdPoint `xml:"Point"`
	BackColor *ShdColor  `xml:"BackColor,omitempty"`
}

// LaGouraudShd 格构高洛德渐变，按行排列控制点
type LaGouraudShd struct {
	VerticesPerRow int        `xml:"VerticesPerRow,attr"`
	Extend         int        `xml:"Extend,attr,omitempty"`
	Point          []ShdPoint `xml:"Point"`
	BackColor      *ShdColor  `xml:"BackColor,omitempty"`
}

// ShdPoint 网格控制点及基本颜色
type ShdPoint struct {
	X        float64  `xml:"X,attr"`
	Y        float64  `xml:"Y,attr"`
	EdgeFlag int      `xml:"EdgeFlag,attr,omitempty"`
	Color    ShdColor `xml:"Color"`
}

// MeshVertex 保存局部毫米坐标、原颜色分量及非预乘透明度
type MeshVertex struct {
	Point  Point
	Space  string
	Values [4]float64
	Alpha  float64
}

// MeshShading 保存后端无关的三角网格，后出现的三角形覆盖先出现的三角形
type MeshShading struct {
	Triangles  [][3]MeshVertex
	Background color.RGBA
}

// outline 返回方向一致的三角形轮廓，供绘制裁剪和对象度量共用
// 返回: GeometryPath 非零绕数填充轮廓
func (m *MeshShading) outline() GeometryPath {
	var path GeometryPath
	for _, t := range m.Triangles {
		a, b, c := t[0].Point, t[1].Point, t[2].Point
		cross := (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
		if cross == 0 {
			continue
		}
		if cross < 0 {
			b, c = c, b
		}
		path = append(path, GeometrySegment{Verb: GeometryMove, End: a}, GeometrySegment{Verb: GeometryLine, End: b}, GeometrySegment{Verb: GeometryLine, End: c}, GeometrySegment{Verb: GeometryClose})
	}
	return path
}

// resolveMesh 按GB/T 33190的三角形连接规则解析网格
// 入参: fill 网格颜色
// 返回: *MeshShading 网格画刷, error 无效控制点
func (r *Renderer) resolveMesh(fill *FillColor) (*MeshShading, error) {
	var points []ShdPoint
	var back *ShdColor
	var extend, columns int
	if node := fill.GouraudShd; node != nil {
		points, back, extend = node.Point, node.BackColor, node.Extend
	} else {
		node := fill.LaGouraudShd
		points, back, extend, columns = node.Point, node.BackColor, node.Extend, node.VerticesPerRow
		if columns == 0 {
			return nil, fmt.Errorf("invalid lattice shading dimensions")
		}
	}
	indices, err := meshTriangleIndices(points, columns, extend)
	if err != nil {
		return nil, err
	}
	vertices := make([]MeshVertex, len(points))
	for i, point := range points {
		space := point.Color.ColorSpace
		if space == "" {
			space = fill.ColorSpace
		}
		kind, values := r.colorComponents(point.Color.Value, point.Color.Index, space)
		alpha := 1.0
		if value := mergeAlpha(point.Color.Alpha, fill.Alpha); value != nil {
			alpha = float64(*value) / 255
		}
		vertices[i] = MeshVertex{Point{point.X, point.Y}, kind, values, alpha}
	}
	mesh := &MeshShading{}
	if extend == 1 && back != nil {
		space := back.ColorSpace
		if space == "" {
			space = fill.ColorSpace
		}
		mesh.Background = r.ResolveColor(back.Value, back.Index, space, mergeAlpha(back.Alpha, fill.Alpha))
	}
	for _, triangle := range indices {
		mesh.Triangles = append(mesh.Triangles, [3]MeshVertex{vertices[triangle[0]], vertices[triangle[1]], vertices[triangle[2]]})
	}
	for i := range mesh.Triangles {
		t := &mesh.Triangles[i]
		if t[0].Space != t[1].Space || t[0].Space != t[2].Space {
			for j := range t {
				r, g, b := meshRGB(t[j].Space, t[j].Values)
				t[j].Space, t[j].Values = "RGB", [4]float64{r, g, b}
			}
		}
	}
	return mesh, nil
}

// meshTriangleIndices 校验控制点并生成共享边或格构三角形编号
// 入参: points 控制点, columns 每行点数，零表示非格构, extend 延伸标志
// 返回: [][3]int 三角形顶点编号, error 非法拓扑
func meshTriangleIndices(points []ShdPoint, columns, extend int) ([][3]int, error) {
	if len(points) < 3 || extend < 0 || extend > 1 {
		return nil, fmt.Errorf("invalid mesh shading")
	}
	if columns != 0 && (columns < 2 || columns > len(points)/2 || len(points)%columns != 0) {
		return nil, fmt.Errorf("invalid lattice shading dimensions")
	}
	for i, p := range points {
		if !finite(p.X) || !finite(p.Y) || p.EdgeFlag < 0 || p.EdgeFlag > 2 {
			return nil, fmt.Errorf("invalid mesh shading point %d", i)
		}
	}
	var result [][3]int
	if columns != 0 {
		for i := columns; i < len(points); i++ {
			if i%columns != 0 {
				result = append(result, [3]int{i - columns - 1, i - columns, i - 1}, [3]int{i - columns, i - 1, i})
			}
		}
		return result, nil
	}
	var triangle [3]int
	for i := 0; i < len(points); {
		switch points[i].EdgeFlag {
		case 0:
			if len(points)-i < 3 {
				return nil, fmt.Errorf("incomplete mesh shading triangle")
			}
			triangle = [3]int{i, i + 1, i + 2}
			i += 3
		case 1, 2:
			if i == 0 {
				return nil, fmt.Errorf("mesh shading starts with a shared edge")
			}
			if points[i].EdgeFlag == 1 {
				triangle[0] = triangle[1]
			}
			triangle[1], triangle[2] = triangle[2], i
			i++
		}
		result = append(result, triangle)
	}
	return result, nil
}

// At 按三角形面积权重插值原颜色分量，返回预乘RGBA
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 采样颜色
func (m *MeshShading) At(x, y float64) color.RGBA {
	for i := len(m.Triangles) - 1; i >= 0; i-- {
		t := m.Triangles[i]
		a, b, c := t[0].Point, t[1].Point, t[2].Point
		d := (b.Y-c.Y)*(a.X-c.X) + (c.X-b.X)*(a.Y-c.Y)
		if d == 0 {
			continue
		}
		u := ((b.Y-c.Y)*(x-c.X) + (c.X-b.X)*(y-c.Y)) / d
		v := ((c.Y-a.Y)*(x-c.X) + (a.X-c.X)*(y-c.Y)) / d
		w := 1 - u - v
		if u < -1e-12 || v < -1e-12 || w < -1e-12 {
			continue
		}
		var values [4]float64
		alpha := 0.0
		for j, weight := range [3]float64{u, v, w} {
			alpha += weight * t[j].Alpha
			for k := range values {
				values[k] += weight * t[j].Values[k]
			}
		}
		red, green, blue := meshRGB(t[0].Space, values)
		return color.RGBA{meshByte(red * alpha), meshByte(green * alpha), meshByte(blue * alpha), meshByte(alpha)}
	}
	return m.Background
}

// meshRGB 将插值后的颜色分量转换为RGB
// 入参: space 颜色模型, values 分量
// 返回: float64 红、绿、蓝分量
func meshRGB(space string, values [4]float64) (float64, float64, float64) {
	switch space {
	case "GRAY":
		return values[0], values[0], values[0]
	case "CMYK":
		return (1 - values[0]) * (1 - values[3]), (1 - values[1]) * (1 - values[3]), (1 - values[2]) * (1 - values[3])
	default:
		return values[0], values[1], values[2]
	}
}

// meshByte 将归一化颜色分量量化为八位整数
// 入参: value 分量
// 返回: uint8 量化结果
func meshByte(value float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, value)) * 255)) }
