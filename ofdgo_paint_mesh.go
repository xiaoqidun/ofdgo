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
	"encoding/xml"
	"fmt"
	"image/color"
	"math"
	"slices"
	"strconv"
	"strings"
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
	Point   Point
	Space   string
	Values  [4]float64
	Alpha   float64
	profile *colorProfile
}

// MeshShading 保存后端无关的三角网格，后出现的三角形覆盖先出现的三角形
type MeshShading struct {
	Triangles  [][3]MeshVertex
	Background color.RGBA
}

// MeshSampler 保存独立网格快照和空间索引，支持并发采样
type MeshSampler struct {
	mesh  MeshShading
	nodes []meshSampleNode
}

// meshSampleNode 保存连续三角形组的包围盒及右子树位置
type meshSampleNode struct {
	minX, minY, maxX, maxY float64
	right                  int
}

// meshTriangleColor 按面积权重采样单个三角形，范围外不产生颜色
// 入参: triangle 三角形顶点, x 横坐标, y 纵坐标
// 返回: color.RGBA 预乘颜色, bool 是否位于三角形内
func meshTriangleColor(triangle *[3]MeshVertex, x, y float64) (color.RGBA, bool) {
	a, b, c := triangle[0].Point, triangle[1].Point, triangle[2].Point
	d := (b.Y-c.Y)*(a.X-c.X) + (c.X-b.X)*(a.Y-c.Y)
	if d == 0 {
		return color.RGBA{}, false
	}
	u := ((b.Y-c.Y)*(x-c.X) + (c.X-b.X)*(y-c.Y)) / d
	v := ((c.Y-a.Y)*(x-c.X) + (a.X-c.X)*(y-c.Y)) / d
	w := 1 - u - v
	if u < -1e-12 || v < -1e-12 || w < -1e-12 {
		return color.RGBA{}, false
	}
	var values [4]float64
	alpha := 0.0
	for j, weight := range [3]float64{u, v, w} {
		alpha += weight * triangle[j].Alpha
		for k := range values {
			values[k] += weight * triangle[j].Values[k]
		}
	}
	red, green, blue := triangle[0].rgb(values)
	return color.RGBA{meshByte(red * alpha), meshByte(green * alpha), meshByte(blue * alpha), meshByte(alpha)}, true
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

// meshRGB 将插值后的颜色分量转换为RGB
// 入参: space 颜色模型, values 分量
// 返回: float64 红、绿、蓝分量
func meshRGB(space string, values [4]float64) (float64, float64, float64) {
	return (MeshVertex{Space: space}).rgb(values)
}

// meshByte 将归一化颜色分量量化为八位整数
// 入参: value 分量
// 返回: uint8 量化结果
func meshByte(value float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, value)) * 255)) }

// UnmarshalXML 解析高洛德渐变的控制点和背景色
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (s *GouraudShd) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*s = GouraudShd{}
	if value := strings.TrimSpace(attrValue(start, "Extend")); value != "" {
		number, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		s.Extend = number
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Point":
			return d.DecodeElement(&s.Point, &node)
		case "BackColor":
			return d.DecodeElement(&s.BackColor, &node)
		default:
			return d.Skip()
		}
	})
}

// UnmarshalXML 解析格构高洛德渐变的每行控制点数、控制点和背景色
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (s *LaGouraudShd) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*s = LaGouraudShd{}
	for _, field := range []struct {
		name   string
		target *int
	}{
		{"Extend", &s.Extend}, {"VerticesPerRow", &s.VerticesPerRow},
	} {
		if value := strings.TrimSpace(attrValue(start, field.name)); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			*field.target = number
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Point":
			return d.DecodeElement(&s.Point, &node)
		case "BackColor":
			return d.DecodeElement(&s.BackColor, &node)
		default:
			return d.Skip()
		}
	})
}

// UnmarshalXML 解析网格控制点坐标、连接标志和基本颜色
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (p *ShdPoint) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = ShdPoint{}
	for _, field := range []struct {
		name   string
		target *float64
	}{
		{"X", &p.X}, {"Y", &p.Y},
	} {
		if value := strings.TrimSpace(attrValue(start, field.name)); value != "" {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			*field.target = number
		}
	}
	if value := strings.TrimSpace(attrValue(start, "EdgeFlag")); value != "" {
		number, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		p.EdgeFlag = number
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "Color" {
			return d.DecodeElement(&p.Color, &node)
		}
		return d.Skip()
	})
}

// At 按三角形面积权重插值原颜色分量，返回预乘RGBA
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 采样颜色
func (m *MeshShading) At(x, y float64) color.RGBA {
	for i := len(m.Triangles) - 1; i >= 0; i-- {
		if value, ok := meshTriangleColor(&m.Triangles[i], x, y); ok {
			return value
		}
	}
	return m.Background
}

// NewSampler 创建网格快照，后续修改原网格不影响采样结果
// 返回: *MeshSampler 只读采样器
func (m *MeshShading) NewSampler() *MeshSampler {
	s := &MeshSampler{}
	if m != nil {
		s.mesh = *m
		s.mesh.Triangles = slices.Clone(m.Triangles)
		if len(m.Triangles) > 8 {
			s.build(0, len(m.Triangles))
		}
	}
	return s
}

// At 采样快照中的预乘颜色，后绘制的三角形优先
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 预乘颜色
func (s *MeshSampler) At(x, y float64) color.RGBA {
	if len(s.nodes) == 0 || !finite(x) || !finite(y) {
		return s.mesh.At(x, y)
	}
	if value, ok := s.sample(0, 0, len(s.mesh.Triangles), x, y); ok {
		return value
	}
	return s.mesh.Background
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

// resolveMesh 按GB/T33190的三角形连接规则解析网格
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
		kind, values, err := r.colorComponents(point.Color.Value, point.Color.Index, space)
		if err != nil {
			return nil, err
		}
		profile, err := r.reader.colorProfile(r.colorDefinition(space))
		if err != nil {
			return nil, err
		}
		alpha := 1.0
		if value := mergeAlpha(point.Color.Alpha, fill.Alpha); value != nil {
			alpha = float64(*value) / 255
		}
		vertices[i] = MeshVertex{Point: Point{X: point.X, Y: point.Y}, Space: kind, Values: values, Alpha: alpha, profile: profile}
	}
	mesh := &MeshShading{}
	if extend == 1 && back != nil {
		space := back.ColorSpace
		var err error
		mesh.Background, err = r.ResolveColor(back.Value, back.Index, space, mergeAlpha(back.Alpha, fill.Alpha))
		if err != nil {
			return nil, err
		}
	}
	for _, triangle := range indices {
		mesh.Triangles = append(mesh.Triangles, [3]MeshVertex{vertices[triangle[0]], vertices[triangle[1]], vertices[triangle[2]]})
	}
	for i := range mesh.Triangles {
		t := &mesh.Triangles[i]
		if !t[0].sameSpace(t[1]) || !t[0].sameSpace(t[2]) {
			for j := range t {
				r, g, b := t[j].rgb(t[j].Values)
				t[j].Space, t[j].Values = "RGB", [4]float64{r, g, b}
				t[j].profile = nil
			}
		}
	}
	return mesh, nil
}

// sameSpace 判断分量能否在原颜色空间内插值，不混合不同ICC定义的分量
// 入参: other 另一顶点
// 返回: bool 模型及配置是否相同
func (v MeshVertex) sameSpace(other MeshVertex) bool {
	if v.Space != other.Space {
		return false
	}
	if v.profile == other.profile {
		return true
	}
	return v.profile != nil && other.profile != nil && v.profile.space.Equal(other.profile.space)
}

// rgb 转换采样分量，插值浮点误差裁切到源空间单位区间
// 入参: values 插值分量
// 返回: float64 红、绿、蓝分量
func (v MeshVertex) rgb(values [4]float64) (float64, float64, float64) {
	for i := range values {
		values[i] = math.Max(0, math.Min(1, values[i]))
	}
	rgb, err := displayColor(v.Space, values, v.profile)
	if err != nil {
		return 0, 0, 0
	}
	return rgb[0], rgb[1], rgb[2]
}

// build 按绘制顺序构建包围盒层级，不重排原三角形
// 入参: first 起始三角形, end 结束位置
// 返回: int 节点位置
func (s *MeshSampler) build(first, end int) int {
	index := len(s.nodes)
	s.nodes = append(s.nodes, meshSampleNode{})
	if end-first <= 8 {
		node := meshSampleNode{minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
		for _, triangle := range s.mesh.Triangles[first:end] {
			a, b, c := triangle[0].Point, triangle[1].Point, triangle[2].Point
			d := (b.Y-c.Y)*(a.X-c.X) + (c.X-b.X)*(a.Y-c.Y)
			if !finite(d) {
				node = meshSampleNode{minX: math.Inf(-1), minY: math.Inf(-1), maxX: math.Inf(1), maxY: math.Inf(1)}
				break
			}
			for _, vertex := range triangle {
				node.minX, node.minY = math.Min(node.minX, vertex.Point.X), math.Min(node.minY, vertex.Point.Y)
				node.maxX, node.maxY = math.Max(node.maxX, vertex.Point.X), math.Max(node.maxY, vertex.Point.Y)
			}
		}
		if finite(node.minX) && finite(node.minY) && finite(node.maxX) && finite(node.maxY) {
			dx := 2e-12*(node.maxX-node.minX) + 1e-14*math.Max(math.Abs(node.minX), math.Abs(node.maxX))
			dy := 2e-12*(node.maxY-node.minY) + 1e-14*math.Max(math.Abs(node.minY), math.Abs(node.maxY))
			node.minX, node.maxX = math.Nextafter(node.minX-dx, math.Inf(-1)), math.Nextafter(node.maxX+dx, math.Inf(1))
			node.minY, node.maxY = math.Nextafter(node.minY-dy, math.Inf(-1)), math.Nextafter(node.maxY+dy, math.Inf(1))
		}
		s.nodes[index] = node
		return index
	}
	middle := first + (end-first)/2
	left := s.build(first, middle)
	right := s.build(middle, end)
	a, b := s.nodes[left], s.nodes[right]
	s.nodes[index] = meshSampleNode{minX: math.Min(a.minX, b.minX), minY: math.Min(a.minY, b.minY), maxX: math.Max(a.maxX, b.maxX), maxY: math.Max(a.maxY, b.maxY), right: right}
	return index
}

// sample 优先查询后绘制的区域，包围盒外跳过整组三角形
// 入参: index 节点位置, first 起始三角形, end 结束位置, x 横坐标, y 纵坐标
// 返回: color.RGBA 采样颜色, bool 是否命中
func (s *MeshSampler) sample(index, first, end int, x, y float64) (color.RGBA, bool) {
	node := s.nodes[index]
	if x < node.minX || x > node.maxX || y < node.minY || y > node.maxY {
		return color.RGBA{}, false
	}
	if end-first <= 8 {
		for i := end - 1; i >= first; i-- {
			if value, ok := meshTriangleColor(&s.mesh.Triangles[i], x, y); ok {
				return value, true
			}
		}
		return color.RGBA{}, false
	}
	middle := first + (end-first)/2
	if value, ok := s.sample(node.right, middle, end, x, y); ok {
		return value, true
	}
	return s.sample(index+1, first, middle, x, y)
}
