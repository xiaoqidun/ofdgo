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
	"image"
	"math"
	"strconv"
	"sync"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfShadingPixel 保存抗锯齿覆盖率与混合空间分量
type pdfShadingPixel = codecpdf.ShadingPixel

// pdfGradientKey 区分渐变画刷及其输出混合空间
type pdfGradientKey struct {
	axial      *pdfgo.AxialGradient
	radial     *pdfgo.RadialGradient
	function   *pdfgo.FunctionGradient
	space      *pdfgo.ColorSpace
	geometry   pdfCompositeKey
	conversion pdfgo.ColorConversion
	softMask   bool
}

// pdfICCKey 区分原始ICC空间及输出渲染意图
type pdfICCKey struct {
	space  *pdfgo.ColorSpace
	intent pdfgo.Name
}

// pdfMeshKey 区分网格及其输出混合空间
type pdfMeshKey struct {
	mesh       *pdfgo.MeshGradient
	space      *pdfgo.ColorSpace
	conversion pdfgo.ColorConversion
	softMask   bool
}

// pdfLinearMeshKey 定位源网格中需分段展开的三角形，不包含输出变换
type pdfLinearMeshKey struct {
	mesh  *pdfgo.MeshGradient
	index int
}

// pdfICCTransfer 检查OFD可原生表达的ICC模型与单位分量范围
// 入参: space 源颜色空间
// 返回: bool 是否可保留原始配置及颜色分量
func pdfICCTransfer(space *pdfgo.ColorSpace) bool {
	model, ranges := space.ICCSource()
	if model != "GRAY" && model != "RGB" && model != "CMYK" {
		return false
	}
	for i := range space.Components() {
		if ranges[2*i] != 0 || ranges[2*i+1] != 1 {
			return false
		}
	}
	return true
}

// pdfICCValues 将单位源分量量化到OFD16位颜色，不提前转换为显示RGB
// 入参: values 单位分量, count 分量数
// 返回: string OFD颜色分量, error 非有限或范围错误
func pdfICCValues(values []float64, count int) (string, error) {
	if count < 1 || count > 4 || len(values) < count {
		return "", fmt.Errorf("invalid ICC shading component count")
	}
	var encoded [4]float64
	for i := range count {
		if !finite(values[i]) || values[i] < 0 || values[i] > 1 {
			return "", fmt.Errorf("invalid ICC shading component")
		}
		encoded[i] = math.Round(values[i] * 65535)
	}
	return pdfNumbers(encoded[:count]...), nil
}

// pdfGradientStopsError 检查源颜色分段是否可由OFD基本颜色表达
// 入参: stops 源颜色分段, space 源颜色空间
// 返回: error 不可线性表达的变换
func pdfGradientStopsError(stops []pdfgo.GradientStop, space *pdfgo.ColorSpace) error {
	if len(stops) == 0 {
		return &pdfgo.UnsupportedError{Feature: "nonlinear gradient conversion"}
	}
	calibrated := space != nil && space.Calibrated() && !pdfICCTransfer(space) && !space.SRGBEquivalent()
	if calibrated {
		for i := 1; i < len(stops); i++ {
			a, b := stops[i-1], stops[i]
			if a.Position != b.Position && a.Values != b.Values {
				return &pdfgo.UnsupportedError{Feature: "nonlinear calibrated gradient conversion"}
			}
		}
	}
	return nil
}

// pdfGradientPattern 判断渐变是否需要底纹保留边界或背景
// 入参: paint PDF画刷
// 返回: bool 是否需要底纹单元
func pdfGradientPattern(paint pdfgo.Paint) bool {
	return paint.Axial != nil && (paint.Axial.Bounds != nil || paint.Axial.Background != nil && paint.Axial.Extend != [2]bool{true, true}) || paint.Radial != nil && (paint.Radial.Bounds != nil || paint.Radial.Background != nil && paint.Radial.Extend != [2]bool{true, true}) || paint.Mesh != nil && paint.Mesh.Bounds != nil
}

// pdfRadialNested 判断起止圆是否严格内含，确保绘制区由内外圆边界唯一限定
// 入参: gradient 径向渐变
// 返回: bool 是否严格内含
func pdfRadialNested(gradient *pdfgo.RadialGradient) bool {
	return math.Hypot(gradient.End.X-gradient.Start.X, gradient.End.Y-gradient.Start.Y) < math.Abs(gradient.EndRadius-gradient.StartRadius)
}

// pdfRadialBackground 用圆弧保留内含双圆的背景区域，不离散化渐变边界
// 入参: gradient 源渐变, matrix 渐变到页面的矩阵, box 对象边界
// 返回: string 奇偶填充背景路径, error 几何转换错误
func pdfRadialBackground(gradient *pdfgo.RadialGradient, matrix pdfgo.Matrix, box Box) (string, error) {
	inner, outer := gradient.Start, gradient.End
	r0, r1, extend := gradient.StartRadius, gradient.EndRadius, gradient.Extend
	if r1 < r0 {
		inner, outer, r0, r1 = outer, inner, r1, r0
		extend = [2]bool{extend[1], extend[0]}
	}
	var circles, path GeometryPath
	if !extend[1] {
		path = geometryRectangle(Box{W: box.W, H: box.H})
		circles = geometryCircle(RasterPoint{X: outer.X, Y: outer.Y}, r1)
	}
	if !extend[0] && r0 > 0 {
		circles = append(circles, geometryCircle(RasterPoint{X: inner.X, Y: inner.Y}, r0)...)
	}
	local := Matrix{a: matrix[0], b: matrix[1], c: matrix[2], d: matrix[3], e: matrix[4] - box.X, f: matrix[5] - box.Y}
	circles, err := circles.Transform(local)
	if err != nil {
		return "", err
	}
	return append(path, circles...).OFD()
}

// pdfAxialBackground 裁出轴向渐变未延伸的两侧区域，与渐变绘制区互不重叠
// 入参: shading 对象局部渐变, box 对象边界
// 返回: string 背景路径, error 非有限坐标
func pdfAxialBackground(shading *AxialShd, box Box) (string, error) {
	start, end := parseFloats(shading.StartPoint), parseFloats(shading.EndPoint)
	dx, dy := end[0]-start[0], end[1]-start[1]
	scale := math.Max(math.Abs(dx), math.Abs(dy))
	if !finite(scale) || scale == 0 {
		return "", fmt.Errorf("invalid PDF gradient background axis")
	}
	dx, dy = dx/scale, dy/scale
	corners := [4]pdfgo.Point{{}, {X: box.W}, {X: box.W, Y: box.H}, {Y: box.H}}
	data := ""
	for side, origin := range [][]float64{start, end} {
		if side == 0 && (shading.Extend == "1" || shading.Extend == "3") || side == 1 && (shading.Extend == "2" || shading.Extend == "3") {
			continue
		}
		distances := [4]float64{}
		for i, point := range corners {
			distances[i] = (point.X-origin[0])*dx + (point.Y-origin[1])*dy
			if side == 1 {
				distances[i] = -distances[i]
			}
			if !finite(distances[i]) {
				return "", fmt.Errorf("invalid PDF gradient background extent")
			}
		}
		points := make([]pdfgo.Point, 0, 5)
		previous := 3
		for i, point := range corners {
			a, b := distances[previous], distances[i]
			if (a <= 0) != (b <= 0) {
				scale := math.Max(math.Abs(a), math.Abs(b))
				ratio := (a / scale) / (a/scale - b/scale)
				from := corners[previous]
				points = append(points, pdfgo.Point{X: from.X + (point.X-from.X)*ratio, Y: from.Y + (point.Y-from.Y)*ratio})
			}
			if b <= 0 {
				points = append(points, point)
			}
			previous = i
		}
		if len(points) < 3 {
			continue
		}
		for i, point := range points {
			if i == 0 {
				data += " M "
			} else {
				data += " L "
			}
			data += pdfNumbers(point.X, point.Y)
		}
		data += " C"
	}
	return data, nil
}

// pdfShadingClippedNode 将函数定义域作为几何裁剪，避免重合边界的覆盖率重复相乘
// 入参: node 原图元, box 定义域矩形, matrix 定义域到页面的变换
// 返回: pdfCompositeNode 带独立裁剪的图元
func pdfShadingClippedNode(node pdfCompositeNode, box pdfgo.Rectangle, matrix pdfgo.Matrix) pdfCompositeNode {
	clip := pdfgo.Path{Segments: []pdfgo.Segment{
		{Operator: "M", Points: []pdfgo.Point{matrix.Apply(pdfgo.Point{X: box.XMin, Y: box.YMin})}},
		{Operator: "L", Points: []pdfgo.Point{matrix.Apply(pdfgo.Point{X: box.XMax, Y: box.YMin})}},
		{Operator: "L", Points: []pdfgo.Point{matrix.Apply(pdfgo.Point{X: box.XMax, Y: box.YMax})}},
		{Operator: "L", Points: []pdfgo.Point{matrix.Apply(pdfgo.Point{X: box.XMin, Y: box.YMax})}},
		{Operator: "C"},
	}}
	var style *pdfgo.Style
	if node.path != nil {
		mark := *node.path
		node.path = &mark
		style = &mark.Style
	} else if node.text != nil {
		mark := *node.text
		node.text = &mark
		style = &mark.Style
	} else {
		mark := *node.image
		node.image = &mark
		style = &mark.Style
	}
	style.Clips = append(append([]pdfgo.Path(nil), style.Clips...), clip)
	return node
}

// iccColorSpace 注册16位ICC空间，复用同一源空间与意图的资源
// 入参: space 源颜色空间, intent 输出渲染意图
// 返回: string OFD资源标识, error 配置或注册错误
func (p *pdfImporter) iccColorSpace(space *pdfgo.ColorSpace, intent pdfgo.Name) (string, error) {
	if p.ctx != nil {
		if err := p.ctx.Err(); err != nil {
			return "", err
		}
	}
	key := pdfICCKey{space: space, intent: intent}
	if id := p.iccSpaces[key]; id != "" {
		return id, nil
	}
	if !pdfICCTransfer(space) {
		return "", &pdfgo.UnsupportedError{Feature: "ICC source range conversion"}
	}
	for cached, id := range p.iccSpaces {
		if cached.intent == intent && cached.space.Equal(space) {
			p.iccSpaces[key] = id
			return id, nil
		}
	}
	model, _ := space.ICCSource()
	id, err := p.editor.AddColorSpace(ColorSpace{Type: string(model), BitsPerComponent: 16}, space.ICCProfile(intent))
	if err != nil {
		return "", err
	}
	if p.iccSpaces == nil {
		p.iccSpaces = make(map[pdfICCKey]string)
	}
	p.iccSpaces[key] = id
	return id, nil
}

// gradientSegments 保留设备及ICC源分量，其他校准颜色经源空间转换
// 入参: stops 源颜色分段, space 源颜色空间, intent 输出渲染意图
// 返回: []ShdSegment OFD颜色分段, error 不可线性表达的变换
func (p *pdfImporter) gradientSegments(stops []pdfgo.GradientStop, space *pdfgo.ColorSpace, intent pdfgo.Name) ([]ShdSegment, error) {
	if p.ctx != nil {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := pdfGradientStopsError(stops, space); err != nil {
		return nil, err
	}
	cmyk := space != nil && space.Model == "DeviceCMYK" && !space.Calibrated()
	segments := make([]ShdSegment, len(stops))
	if pdfICCTransfer(space) {
		id, err := p.iccColorSpace(space, intent)
		if err != nil {
			return nil, err
		}
		for i, stop := range stops {
			if p.ctx != nil && i&255 == 0 {
				if err := p.ctx.Err(); err != nil {
					return nil, err
				}
			}
			value, err := pdfICCValues(stop.Values[:], space.Components())
			if err != nil {
				return nil, err
			}
			segments[i] = ShdSegment{Position: stop.Position, Color: ShdColor{Value: value, ColorSpace: id}}
		}
		return segments, nil
	}
	for i, stop := range stops {
		if p.ctx != nil && i&255 == 0 {
			if err := p.ctx.Err(); err != nil {
				return nil, err
			}
		}
		paint := pdfgo.Paint{RGB: stop.RGB, Alpha: 1}
		if cmyk {
			paint.CMYK = &stop.Values
		}
		color, err := p.color(paint, intent)
		if err != nil {
			return nil, err
		}
		segments[i] = ShdSegment{Position: stop.Position, Color: ShdColor{Value: color.Value, ColorSpace: color.ColorSpace}}
	}
	return segments, nil
}

// gradientError 检查渐变函数与源空间能否原生映射为OFD分段
// 入参: paint 画刷
// 返回: error 不可等价表达的渐变或取消错误
func (p *pdfImporter) gradientError(paint pdfgo.Paint) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if paint.Shading != nil {
		return &pdfgo.UnsupportedError{Feature: "shading pattern state compositing"}
	}
	if paint.Function != nil {
		return &pdfgo.UnsupportedError{Feature: "function shading conversion"}
	}
	if paint.Mesh != nil {
		mesh := paint.Mesh
		if len(mesh.Patches) != 0 || len(mesh.Triangles) == 0 {
			return &pdfgo.UnsupportedError{Feature: "nonlinear mesh conversion"}
		}
		for i := range mesh.Triangles {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			if _, err := mesh.LinearColors(i); err != nil {
				if _, err := p.linearMeshTriangles(mesh, i); err != nil {
					return err
				}
			}
		}
		if mesh.Space.Calibrated() && !pdfICCTransfer(mesh.Space) && !mesh.Space.SRGBEquivalent() {
			return &pdfgo.UnsupportedError{Feature: "nonlinear calibrated mesh conversion"}
		}
	}
	if paint.Axial != nil {
		return pdfGradientStopsError(paint.Axial.Stops, paint.Axial.Space)
	}
	if paint.Radial != nil {
		if paint.Radial.Background != nil && !pdfRadialNested(paint.Radial) {
			return &pdfgo.UnsupportedError{Feature: "radial pattern background conversion"}
		}
		return pdfGradientStopsError(paint.Radial.Stops, paint.Radial.Space)
	}
	return p.ctx.Err()
}

// gradientPattern 用底纹单元保留渐变边界及背景，单元覆盖整个对象以避免重复绘制
// 入参: paint PDF渐变画刷, box 页面坐标中的对象边界
// 返回: *FillColor OFD底纹颜色, error 坐标或渐变转换错误
func (p *pdfImporter) gradientPattern(paint pdfgo.Paint, box Box) (*FillColor, error) {
	if !finite(box.X) || !finite(box.Y) || !finite(box.W) || !finite(box.H) || box.W <= 0 || box.H <= 0 {
		return nil, fmt.Errorf("invalid PDF gradient paint bounds")
	}
	var bounds *pdfgo.Rectangle
	var background *[4]float64
	var space *pdfgo.ColorSpace
	var intent pdfgo.Name
	var extend [2]bool
	matrix := pdfgo.Identity()
	plain := paint
	plain.Alpha = 1
	if paint.Axial != nil {
		gradient := *paint.Axial
		bounds, matrix = gradient.Bounds, gradient.Matrix
		background, space, intent, extend = gradient.Background, gradient.Space, gradient.Intent, gradient.Extend
		gradient.Bounds = nil
		gradient.Background = nil
		plain.Axial = &gradient
	} else if paint.Radial != nil {
		gradient := *paint.Radial
		bounds, matrix = gradient.Bounds, gradient.Matrix
		background, space, intent, extend = gradient.Background, gradient.Space, gradient.Intent, gradient.Extend
		gradient.Bounds = nil
		gradient.Background = nil
		plain.Radial = &gradient
	} else {
		gradient := *paint.Mesh
		bounds, matrix = gradient.Bounds, gradient.Matrix
		gradient.Bounds = nil
		plain.Mesh = &gradient
	}
	if matrix == (pdfgo.Matrix{}) {
		matrix = pdfgo.Identity()
	}
	matrix = p.matrix.Mul(matrix)
	if _, ok := matrix.Inverse(); !ok {
		return nil, fmt.Errorf("invalid PDF gradient bounds matrix")
	}
	fill, err := p.paintColor(plain, box, intent)
	if err != nil {
		return nil, err
	}
	filled, stroked := true, false
	boundary := pdfBoundary(Box{W: box.W, H: box.H})
	object := PathObject{Boundary: boundary, AbbreviatedData: "M 0 0 L " + pdfNumbers(box.W, 0) + " L " + pdfNumbers(box.W, box.H) + " L " + pdfNumbers(0, box.H) + " C", Fill: &filled, Stroke: &stroked, FillColor: fill}
	if bounds != nil {
		points := [4]pdfgo.Point{{X: bounds.XMin, Y: bounds.YMin}, {X: bounds.XMax, Y: bounds.YMin}, {X: bounds.XMax, Y: bounds.YMax}, {X: bounds.XMin, Y: bounds.YMax}}
		data := ""
		for i, point := range points {
			point = matrix.Apply(point)
			if !finite(point.X) || !finite(point.Y) {
				return nil, fmt.Errorf("invalid PDF gradient bounds")
			}
			if i == 0 {
				data = "M "
			} else {
				data += " L "
			}
			data += pdfNumbers(point.X-box.X, point.Y-box.Y)
		}
		clip := PathObject{Boundary: boundary, AbbreviatedData: data + " C", Fill: &filled, Stroke: &stroked}
		object.Clips = &Clips{Clip: []Clip{{Area: []ClipArea{{Path: []PathObject{clip}}}}}}
	}
	objects := []PathObject{object}
	if background != nil && extend != [2]bool{true, true} {
		data := ""
		if paint.Axial != nil {
			data, err = pdfAxialBackground(fill.AxialShd, box)
		} else {
			data, err = pdfRadialBackground(paint.Radial, matrix, box)
			clip := object
			clip.FillColor, clip.Clips = nil, nil
			clips := &Clips{Clip: []Clip{{Area: []ClipArea{{Path: []PathObject{clip}}}}}}
			if object.Clips != nil {
				clips.Clip = append(clips.Clip, object.Clips.Clip...)
			}
			object.Clips = clips
			object.Rule = "Even-Odd"
		}
		if err != nil {
			return nil, err
		}
		if data != "" {
			color, err := p.gradientBackgroundColor(*background, space, intent)
			if err != nil {
				return nil, err
			}
			object.AbbreviatedData = data
			object.FillColor = &FillColor{Value: color.Value, ColorSpace: color.ColorSpace}
			objects = append(objects, object)
		}
	}
	alpha := int(math.Round(paint.Alpha * 255))
	color := &FillColor{Alpha: &alpha}
	color.Pattern = &Pattern{Width: box.W, Height: box.H, RelativeTo: "Page", CTM: pdfNumbers(1, 0, 0, 1, box.X, box.Y), CellContent: PatternContent{PathObject: objects}}
	return color, nil
}

// gradientBackgroundColor 保留渐变背景的源颜色分量并复用颜色空间资源
// 入参: values 背景分量, space 源颜色空间, intent 渲染意图
// 返回: ShdColor OFD基本颜色, error 颜色转换错误
func (p *pdfImporter) gradientBackgroundColor(values [4]float64, space *pdfgo.ColorSpace, intent pdfgo.Name) (ShdColor, error) {
	if space == nil || space.Components() < 1 || space.Components() > 4 {
		return ShdColor{}, fmt.Errorf("invalid PDF gradient background color space")
	}
	stop := pdfgo.GradientStop{Values: values}
	if !pdfICCTransfer(space) && (space.Model != "DeviceCMYK" || space.Calibrated()) {
		var err error
		stop.RGB, err = space.SRGB(values[:space.Components()], intent)
		if err != nil {
			return ShdColor{}, err
		}
	}
	segments, err := p.gradientSegments([]pdfgo.GradientStop{stop}, space, intent)
	if err != nil {
		return ShdColor{}, err
	}
	return segments[0].Color, nil
}

// gradientPath 按原始函数合成渐变路径，保留填充描边的单一对象语义
// 入参: mark 路径绘制信息
// 返回: error 几何或着色错误
func (p *pdfImporter) gradientPath(mark pdfgo.PathMark) error {
	cmyk := true
	for i, paint := range []pdfgo.Paint{mark.Style.Fill, mark.Style.Stroke} {
		if i == 0 && !mark.Fill || i == 1 && !mark.Stroke {
			continue
		}
		space := paint.Space
		switch {
		case paint.Axial != nil:
			space = paint.Axial.Space
		case paint.Radial != nil:
			space = paint.Radial.Space
		case paint.Function != nil:
			space = paint.Function.Space
		case paint.Mesh != nil:
			space = paint.Mesh.Space
		}
		if space != nil && space.Calibrated() || paint.CMYK == nil && (space == nil || space.Model != "DeviceCMYK") {
			cmyk = false
			break
		}
	}
	space := &pdfgo.ColorSpace{Model: "DeviceRGB"}
	if cmyk {
		space.Model = "DeviceCMYK"
	}
	return p.compositeRegion(nil, pdfCompositeNode{path: &mark}, space, false)
}

// gradient 合成二维函数着色及带区域的渐变，保留边缘覆盖和对象透明度
// 入参: paint 渐变画刷, space 混合空间, node 几何图元，可为空, outline 是否描边
// 返回: []pdfShadingPixel 颜色及覆盖, error 求值或变换错误
func (c *pdfCompositor) gradient(paint pdfgo.Paint, space *pdfgo.ColorSpace, node *pdfCompositeNode, outline bool) ([]pdfShadingPixel, error) {
	if paint.Axial != nil && paint.Axial.Background == nil && paint.Axial.Bounds == nil || paint.Radial != nil && paint.Radial.Background == nil && paint.Radial.Bounds == nil {
		return nil, nil
	}
	key := pdfGradientKey{axial: paint.Axial, radial: paint.Radial, function: paint.Function, space: space, softMask: c.softMask}
	var conversion pdfgo.ColorConversion
	if node != nil {
		key.geometry = pdfCompositeKey{path: node.path, text: node.text, image: node.image, stroke: outline}
		style, _, _ := node.style()
		conversion = style.ColorConversion
		key.conversion = conversion
	}
	if pixels, ok := c.gradients[key]; ok {
		return pixels, nil
	}
	var background *[4]float64
	var bounds *pdfgo.Rectangle
	var matrix pdfgo.Matrix
	var source *pdfgo.ColorSpace
	var intent pdfgo.Name
	if paint.Axial != nil {
		g := *paint.Axial
		background, bounds, matrix, source, intent = g.Background, g.Bounds, g.Matrix, g.Space, g.Intent
		g.Background, g.Bounds = nil, nil
		paint.Axial = &g
	} else if paint.Radial != nil {
		g := *paint.Radial
		background, bounds, matrix, source, intent = g.Background, g.Bounds, g.Matrix, g.Space, g.Intent
		g.Background, g.Bounds = nil, nil
		g.Matrix = pdfgo.Matrix{}
		paint.Radial = &g
	} else {
		g := *paint.Function
		background, bounds, matrix, source, intent = g.Background, g.Bounds, g.PatternMatrix, g.Space, g.Intent
		g.Background, g.Bounds = nil, nil
		paint.Function = &g
	}
	var backgroundValues [4]float64
	if background != nil {
		var err error
		backgroundValues, err = space.ConvertWith(background[:source.Components()], source, intent, conversion)
		if err != nil {
			return nil, err
		}
	}
	inverse := pdfgo.Identity()
	if matrix != (pdfgo.Matrix{}) && (bounds != nil || paint.Radial != nil) {
		var ok bool
		inverse, ok = matrix.Inverse()
		if !ok {
			return nil, fmt.Errorf("singular shading transform")
		}
	}
	var functionInverse pdfgo.Matrix
	if paint.Function != nil {
		var ok bool
		functionInverse, ok = paint.Function.Matrix.Inverse()
		if !ok {
			return nil, fmt.Errorf("singular function shading transform")
		}
	}
	const samples = 4
	var baseCoverage image.Image
	var hiCoverage image.Image
	var domainGeometry pdfCompositeGeometry
	domainClipped := node != nil && paint.Function != nil && background == nil
	if node != nil {
		var err error
		if domainClipped {
			var ok bool
			domainGeometry, ok = c.cache.shadings[key]
			if !ok {
				g := paint.Function
				n := pdfShadingClippedNode(*node, g.Domain, g.Matrix)
				domainGeometry.scene, domainGeometry.box, err = c.importer.compileGroup(func(p *pdfImporter) error { return n.coverage(p, outline) })
				if err != nil {
					return nil, err
				}
				if c.cache.shadings == nil {
					c.cache.shadings = map[pdfGradientKey]pdfCompositeGeometry{}
				}
				c.cache.shadings[key] = domainGeometry
			}
			b := domainGeometry.box
			if domainGeometry.scene != nil && !(b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y) {
				target, targetErr := c.acquireCoverage()
				if targetErr != nil {
					return nil, targetErr
				}
				baseCoverage, err = c.importer.renderGroupSceneInto(domainGeometry.scene, c.box, target)
				if err != nil {
					c.releaseCoverage(target)
				}
			}
		} else {
			baseCoverage, err = c.coverage(*node, outline)
		}
		if err != nil {
			return nil, err
		}
		if baseCoverage == nil {
			return make([]pdfShadingPixel, c.width*c.height), nil
		}
		defer c.releaseCoverage(baseCoverage)
	}
	ensureHiCoverage := func() (image.Image, error) {
		if node == nil || hiCoverage != nil {
			return hiCoverage, nil
		}
		local, importer := *c, *c.importer
		importer.rasterDPI *= samples
		local.importer = &importer
		local.masks = nil
		local.clipMasks = nil
		var err error
		if domainClipped {
			if domainGeometry.scene != nil {
				hiCoverage, err = importer.renderGroupScene(domainGeometry.scene, c.box)
			}
		} else {
			hiCoverage, err = local.coverage(*node, outline)
			local.releaseMasks()
		}
		return hiCoverage, err
	}
	var position pdfgo.GradientPosition
	if paint.Axial != nil || paint.Radial != nil {
		var err error
		if paint.Axial != nil {
			position, err = paint.Axial.PreparePosition()
		} else {
			position, err = paint.Radial.PreparePosition()
		}
		if err != nil {
			return nil, err
		}
	}
	colorants := codecpdf.PrepareShadingColorants(paint, space, c.softMask)
	sameSpace := colorants == nil && source.Equal(space)
	var converter pdfgo.ColorConverter
	if !sameSpace && colorants == nil {
		var err error
		converter, err = space.PrepareConversion(source, intent, conversion)
		if err != nil {
			return nil, err
		}
	}
	components := source.Components()
	step := 25.4 / c.importer.rasterDPI
	var boundsMat, domainMat pdfgo.Matrix
	var boundsMarginX, boundsMarginY, domainMarginX, domainMarginY float64
	if bounds != nil {
		boundsMat = inverse.Mul(c.inverse)
		boundsMarginX = (math.Abs(boundsMat[0]) + math.Abs(boundsMat[2])) * step * 0.55
		boundsMarginY = (math.Abs(boundsMat[1]) + math.Abs(boundsMat[3])) * step * 0.55
	}
	if paint.Function != nil && !domainClipped {
		domainMat = functionInverse.Mul(c.inverse)
		domainMarginX = (math.Abs(domainMat[0]) + math.Abs(domainMat[2])) * step * 0.55
		domainMarginY = (math.Abs(domainMat[1]) + math.Abs(domainMat[3])) * step * 0.55
	}
	evalSample := func(pagePoint pdfgo.Point) ([4]float64, bool, bool, error) {
		point := pagePoint
		local := point
		if bounds != nil || paint.Radial != nil {
			local = inverse.Apply(point)
		}
		if bounds != nil {
			if local.X < bounds.XMin || local.X > bounds.XMax || local.Y < bounds.YMin || local.Y > bounds.YMax {
				return [4]float64{}, false, true, nil
			}
		}
		if paint.Radial != nil {
			point = local
		}
		var values [4]float64
		var visible bool
		var err error
		if g := paint.Function; g != nil {
			local = functionInverse.Apply(point)
			visible = domainClipped || local.X >= g.Domain.XMin && local.X <= g.Domain.XMax && local.Y >= g.Domain.YMin && local.Y <= g.Domain.YMax
			if visible {
				if colorants != nil {
					values, err = colorants.PointAt(local)
				} else {
					values, err = g.ValuesAt(local)
					if err == nil && !sameSpace {
						values, err = converter.Convert(values[:components])
					}
				}
			}
		} else {
			var parameter float64
			parameter, visible = position.PositionAt(point)
			if visible {
				if colorants != nil {
					values, err = colorants.At(parameter)
				} else if paint.Axial != nil {
					values, err = paint.Axial.ValuesAt(parameter)
				} else {
					values, err = paint.Radial.ValuesAt(parameter)
				}
				if err == nil && colorants == nil && !sameSpace {
					values, err = converter.Convert(values[:components])
				}
			}
		}
		if err != nil {
			return [4]float64{}, false, false, err
		}
		if !visible {
			if background == nil {
				return [4]float64{}, false, false, nil
			}
			return backgroundValues, true, false, nil
		}
		return values, true, false, nil
	}
	isImageNode := node != nil && node.image != nil
	extendBoth := paint.Axial != nil && paint.Axial.Extend == [2]bool{true, true}
	pixels := make([]pdfShadingPixel, c.width*c.height)
	evalSupersample := func(x, y int, pixel *pdfShadingPixel) error {
		hi, err := ensureHiCoverage()
		if err != nil {
			return err
		}
		for sy := range samples {
			for sx := range samples {
				weight := 1.0
				if hi != nil {
					a := baseimage.AlphaAt(hi, x*samples+sx, y*samples+sy)
					weight = float64(a) / 65535
					if weight == 0 {
						continue
					}
				}
				pagePoint := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+(float64(sx)+.5)/samples)*step, Y: c.box.Y + (float64(y)+(float64(sy)+.5)/samples)*step})
				values, ok, outBounds, err := evalSample(pagePoint)
				if err != nil {
					return err
				}
				if outBounds || !ok {
					continue
				}
				if isImageNode {
					_, alpha, err := c.imageColor(node.image, pagePoint, space)
					if err != nil {
						return err
					}
					weight *= alpha
				}
				for i, value := range values {
					pixel.Values[i] += value * weight
				}
				pixel.Shape += weight
			}
		}
		if pixel.Shape != 0 {
			for i := range pixel.Values {
				pixel.Values[i] /= pixel.Shape
			}
			pixel.Shape /= samples * samples
		}
		return nil
	}
	evalRowRange := func(y0, y1 int, deferSupersample bool) error {
		for y := y0; y < y1; y++ {
			if err := c.importer.ctx.Err(); err != nil {
				return err
			}
			var covRow []byte
			if baseCoverage != nil {
				covRow = codecpdf.CoverageRow(baseCoverage, y, 0, c.width)
			}
			centerOFDY := c.box.Y + (float64(y)+.5)*step
			for x := range c.width {
				baseA := uint32(65535)
				if baseCoverage != nil {
					if covRow != nil {
						baseA = uint32(covRow[4*x+3]) * 257
					} else {
						baseA = baseimage.AlphaAt(baseCoverage, x, y)
					}
					if baseA == 0 {
						continue
					}
				}
				pixel := &pixels[y*c.width+x]
				centerOFD := pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: centerOFDY}
				supersample := isImageNode || baseA < 65535
				if !supersample && bounds != nil {
					bp := boundsMat.Apply(centerOFD)
					if bp.X+boundsMarginX < bounds.XMin || bp.X-boundsMarginX > bounds.XMax || bp.Y+boundsMarginY < bounds.YMin || bp.Y-boundsMarginY > bounds.YMax {
						continue
					}
					if bp.X-boundsMarginX < bounds.XMin || bp.X+boundsMarginX > bounds.XMax || bp.Y-boundsMarginY < bounds.YMin || bp.Y+boundsMarginY > bounds.YMax {
						supersample = true
					}
				}
				if !supersample && paint.Function != nil && !domainClipped {
					g := paint.Function
					dp := domainMat.Apply(centerOFD)
					if dp.X+domainMarginX < g.Domain.XMin || dp.X-domainMarginX > g.Domain.XMax || dp.Y+domainMarginY < g.Domain.YMin || dp.Y-domainMarginY > g.Domain.YMax {
						if background == nil {
							continue
						}
						pixel.Values, pixel.Shape = backgroundValues, 1
						continue
					}
					if dp.X-domainMarginX < g.Domain.XMin || dp.X+domainMarginX > g.Domain.XMax || dp.Y-domainMarginY < g.Domain.YMin || dp.Y+domainMarginY > g.Domain.YMax {
						supersample = true
					}
				}
				if !supersample && (paint.Axial != nil && !extendBoth || paint.Radial != nil) {
					half := 0.5 * step
					visCount := 0
					for _, delta := range [4]pdfgo.Point{{X: -half, Y: -half}, {X: half, Y: -half}, {X: half, Y: half}, {X: -half, Y: half}} {
						pt := c.inverse.Apply(pdfgo.Point{X: centerOFD.X + delta.X, Y: centerOFD.Y + delta.Y})
						if paint.Radial != nil {
							pt = inverse.Apply(pt)
						}
						if _, vis := position.PositionAt(pt); vis {
							visCount++
						}
					}
					if visCount != 0 && visCount != 4 {
						supersample = true
					} else if visCount == 0 {
						if background == nil {
							continue
						}
						pixel.Values, pixel.Shape = backgroundValues, 1
						continue
					}
				}
				if !supersample {
					values, ok, _, err := evalSample(c.inverse.Apply(centerOFD))
					if err != nil {
						return err
					}
					if ok {
						pixel.Values, pixel.Shape = values, 1
					}
					continue
				}
				if deferSupersample {
					pixel.Shape = -1
					continue
				}
				if err := evalSupersample(x, y, pixel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	workers := conversionWorkers(c.importer.ctx)
	if workers > 1 && colorants == nil && !isImageNode && c.height >= 32 && c.width*c.height >= 4096 {
		if workers > c.height/8 {
			workers = max(1, c.height/8)
		}
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for w := range workers {
			y0 := w * c.height / workers
			y1 := (w + 1) * c.height / workers
			wg.Add(1)
			go func(idx, startY, endY int) {
				defer wg.Done()
				errs[idx] = evalRowRange(startY, endY, true)
			}(w, y0, y1)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		for y := range c.height {
			rowOff := y * c.width
			for x := range c.width {
				pixel := &pixels[rowOff+x]
				if pixel.Shape >= 0 {
					continue
				}
				pixel.Shape = 0
				if err := evalSupersample(x, y, pixel); err != nil {
					return nil, err
				}
			}
		}
	} else {
		if err := evalRowRange(0, c.height, false); err != nil {
			return nil, err
		}
	}
	if c.gradients == nil {
		c.gradients = map[pdfGradientKey][]pdfShadingPixel{}
	}
	c.gradients[key] = pixels
	return pixels, nil
}

// color 保留设备及ICC源分量，其他校准色以16位显示颜色输出
// 入参: paint PDF颜色与不透明度, intent 实际渲染意图
// 返回: *FillColor OFD颜色, error 配置或颜色转换错误
func (p *pdfImporter) color(paint pdfgo.Paint, intent pdfgo.Name) (*FillColor, error) {
	alpha := int(math.Round(paint.Alpha * 255))
	if pdfICCTransfer(paint.Space) {
		id, err := p.iccColorSpace(paint.Space, intent)
		if err != nil {
			return nil, err
		}
		value, err := pdfICCValues(paint.Values[:], paint.Space.Components())
		if err != nil {
			return nil, err
		}
		return &FillColor{ColorSpace: id, Value: value, Alpha: &alpha}, nil
	}
	cmyk := paint.CMYK
	if cmyk == nil && paint.Space != nil && paint.Space.Model == "DeviceCMYK" && !paint.Space.Calibrated() {
		cmyk = &paint.Values
	}
	if cmyk != nil && (paint.Space == nil || !paint.Space.Calibrated()) {
		if p.cmykSpace == "" {
			cmykID, err := p.editor.nextID()
			if err != nil {
				return nil, err
			}
			p.cmykSpace = cmykID
			p.editor.resources = append(p.editor.resources, editorResource{space: &ColorSpace{ID: p.cmykSpace, Type: "CMYK", BitsPerComponent: 16}})
		}
		v := cmyk
		return &FillColor{ColorSpace: p.cmykSpace, Value: pdfNumbers(math.Round(v[0]*65535), math.Round(v[1]*65535), math.Round(v[2]*65535), math.Round(v[3]*65535)), Alpha: &alpha}, nil
	}
	if paint.Space.Calibrated() {
		if p.rgbSpace == "" {
			id, err := p.editor.AddColorSpace(ColorSpace{Type: "RGB", BitsPerComponent: 16}, nil)
			if err != nil {
				return nil, err
			}
			p.rgbSpace = id
		}
		value, err := pdfICCValues(paint.RGB[:], 3)
		if err != nil {
			return nil, err
		}
		return &FillColor{ColorSpace: p.rgbSpace, Value: value, Alpha: &alpha}, nil
	}
	return &FillColor{Value: pdfNumbers(math.Round(paint.RGB[0]*255), math.Round(paint.RGB[1]*255), math.Round(paint.RGB[2]*255)), Alpha: &alpha}, nil
}

// paintColor 将页面渐变转换为对象局部坐标
// 入参: paint PDF画刷, box 对象边界, intent 实际渲染意图
// 返回: *FillColor OFD颜色或渐变, error 图案转换错误
func (p *pdfImporter) paintColor(paint pdfgo.Paint, box Box, intent pdfgo.Name) (*FillColor, error) {
	if err := p.gradientError(paint); err != nil {
		return nil, err
	}
	if pdfGradientPattern(paint) {
		return p.gradientPattern(paint, box)
	}
	if paint.Mesh != nil {
		return p.meshColor(paint, box)
	}
	alpha := int(math.Round(paint.Alpha * 255))
	color := &FillColor{Alpha: &alpha}
	if paint.Tiling != nil {
		pattern, err := p.tilingPattern(paint.Tiling, paint)
		if err != nil {
			return nil, err
		}
		color.Pattern = pattern
		return color, nil
	}
	if paint.Radial != nil {
		gradient := paint.Radial
		matrix := p.matrix
		if gradient.Matrix != (pdfgo.Matrix{}) {
			matrix = matrix.Mul(gradient.Matrix)
		}
		start, end := matrix.Apply(gradient.Start), matrix.Apply(gradient.End)
		xx, yy, xy := matrix[0]*matrix[0]+matrix[2]*matrix[2], matrix[1]*matrix[1]+matrix[3]*matrix[3], matrix[0]*matrix[1]+matrix[2]*matrix[3]
		major := (xx + yy + math.Hypot(xx-yy, 2*xy)) / 2
		determinant := matrix[0]*matrix[3] - matrix[1]*matrix[2]
		if major <= 0 || determinant == 0 {
			return nil, fmt.Errorf("invalid PDF radial gradient matrix")
		}
		scale := math.Sqrt(major)
		eccentricity := math.Sqrt(math.Max(0, 1-determinant*determinant/(major*major)))
		angle := math.Atan2(2*xy, xx-yy) * 90 / math.Pi
		extend := 0
		if gradient.Extend[0] {
			extend |= 1
		}
		if gradient.Extend[1] {
			extend |= 2
		}
		shading := &RadialShd{StartPoint: pdfNumbers(start.X-box.X, start.Y-box.Y), EndPoint: pdfNumbers(end.X-box.X, end.Y-box.Y), StartRadius: gradient.StartRadius * scale, EndRadius: gradient.EndRadius * scale, Eccentricity: eccentricity, Angle: angle, Extend: strconv.Itoa(extend)}
		var err error
		shading.Segment, err = p.gradientSegments(gradient.Stops, gradient.Space, gradient.Intent)
		if err != nil {
			return nil, err
		}
		color.Value = ""
		color.RadialShd = shading
		return color, nil
	}
	if paint.Axial == nil {
		return p.color(paint, intent)
	}
	gradient := paint.Axial
	start := p.matrix.Apply(gradient.Start)
	inverse, ok := p.matrix.Inverse()
	if !ok {
		return nil, fmt.Errorf("invalid PDF gradient matrix")
	}
	dx, dy := gradient.End.X-gradient.Start.X, gradient.End.Y-gradient.Start.Y
	gx, gy := inverse[0]*dx+inverse[1]*dy, inverse[2]*dx+inverse[3]*dy
	length := gx*gx + gy*gy
	if length == 0 {
		return nil, fmt.Errorf("invalid PDF axial gradient axis")
	}
	factor := (dx*dx + dy*dy) / length
	end := pdfgo.Point{X: start.X + gx*factor, Y: start.Y + gy*factor}
	extend := 0
	if gradient.Extend[0] {
		extend |= 1
	}
	if gradient.Extend[1] {
		extend |= 2
	}
	shading := &AxialShd{StartPoint: pdfNumbers(start.X-box.X, start.Y-box.Y), EndPoint: pdfNumbers(end.X-box.X, end.Y-box.Y), Extend: strconv.Itoa(extend)}
	var err error
	shading.Segment, err = p.gradientSegments(gradient.Stops, gradient.Space, gradient.Intent)
	if err != nil {
		return nil, err
	}
	color.Value = ""
	color.AxialShd = shading
	return color, nil
}

// linearMeshTriangles 在单页内复用只读分区，固定布局计量内存并丢弃超预算缓存
// 入参: mesh 源网格, index 源三角形索引
// 返回: []pdfgo.MeshTriangle 只读分区结果, error 转换或取消错误
func (p *pdfImporter) linearMeshTriangles(mesh *pdfgo.MeshGradient, index int) ([]pdfgo.MeshTriangle, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	cache := p.compositingCache()
	if cache.linearMeshes == nil {
		cache.linearMeshes = resourceCache[pdfLinearMeshKey, []pdfgo.MeshTriangle](p.resources, 8<<20)
	}
	key := pdfLinearMeshKey{mesh: mesh, index: index}
	if triangles, ok := cache.linearMeshes.get(key); ok {
		return triangles, nil
	}
	triangles, err := mesh.LinearTrianglesContext(p.ctx, index)
	if err != nil {
		return nil, err
	}
	if len(triangles) > (cache.linearMeshes.limit-64)/216 {
		return triangles, nil
	}
	stored := make([]pdfgo.MeshTriangle, len(triangles))
	colors := make([][3][4]float64, len(triangles))
	for i, triangle := range triangles {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		stored[i].Points = triangle.Points
		for j, values := range triangle.Colors {
			copy(colors[i][j][:], values)
			stored[i].Colors[j] = colors[i][j][:len(values):len(values)]
		}
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	cache.linearMeshes.put(key, stored, 64+len(stored)*216)
	return stored, nil
}

// meshColor 将线性三角网格映射为OFD高洛德渐变，保留顶点和覆盖顺序
// 入参: paint PDF网格画刷, box 对象边界
// 返回: *FillColor OFD渐变, error 不可线性表达的颜色或坐标错误
func (p *pdfImporter) meshColor(paint pdfgo.Paint, box Box) (*FillColor, error) {
	if err := p.gradientError(paint); err != nil {
		return nil, err
	}
	mesh := paint.Mesh
	matrix := p.matrix.Mul(mesh.Matrix)
	shading := &GouraudShd{}
	icc := ""
	if pdfICCTransfer(mesh.Space) {
		var err error
		icc, err = p.iccColorSpace(mesh.Space, mesh.Intent)
		if err != nil {
			return nil, err
		}
	}
	var previous pdfgo.MeshTriangle
	var previousColors [3][4]float64
	var colors [3][4]float64
	for i := range previous.Colors {
		previous.Colors[i] = previousColors[i][:mesh.Space.Components()]
	}
	for index, triangle := range mesh.Triangles {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		colors, err = mesh.LinearColors(index)
		var triangles []pdfgo.MeshTriangle
		if err != nil {
			triangles, err = p.linearMeshTriangles(mesh, index)
			if err != nil {
				return nil, err
			}
		} else {
			for i := range triangle.Colors {
				triangle.Colors[i] = colors[i][:mesh.Space.Components()]
			}
			triangles = []pdfgo.MeshTriangle{triangle}
		}
		for _, triangle := range triangles {
			if err := p.ctx.Err(); err != nil {
				return nil, err
			}
			first, flag := 0, 0
			if len(shading.Point) > 0 {
				flag = codecpdf.MeshEdge(previous, triangle)
				if flag != 0 {
					first = 2
				}
			}
			for i := first; i < len(triangle.Points); i++ {
				point := triangle.Points[i]
				point = matrix.Apply(point)
				if !finite(point.X) || !finite(point.Y) {
					return nil, fmt.Errorf("nonfinite mesh vertex")
				}
				values := triangle.Colors[i]
				if icc != "" {
					value, err := pdfICCValues(values, mesh.Space.Components())
					if err != nil {
						return nil, err
					}
					shading.Point = append(shading.Point, ShdPoint{X: point.X - box.X, Y: point.Y - box.Y, EdgeFlag: flag, Color: ShdColor{Value: value, ColorSpace: icc}})
					continue
				}
				rgb, err := mesh.Space.SRGB(values[:mesh.Space.Components()], mesh.Intent)
				if err != nil {
					return nil, err
				}
				color := pdfgo.Paint{RGB: rgb, Alpha: 1}
				if mesh.Space.Model == "DeviceCMYK" && !mesh.Space.Calibrated() {
					color.CMYK = (*[4]float64)(values)
				}
				converted, err := p.color(color, mesh.Intent)
				if err != nil {
					return nil, err
				}
				shading.Point = append(shading.Point, ShdPoint{X: point.X - box.X, Y: point.Y - box.Y, EdgeFlag: flag, Color: ShdColor{Value: converted.Value, ColorSpace: converted.ColorSpace}})
			}
			previous.Points = triangle.Points
			for i := range previous.Colors {
				copy(previous.Colors[i], triangle.Colors[i])
			}
		}
	}
	if mesh.Background != nil {
		background, err := p.gradientBackgroundColor(*mesh.Background, mesh.Space, mesh.Intent)
		if err != nil {
			return nil, err
		}
		shading.Extend, shading.BackColor = 1, &background
	}
	alpha := int(math.Round(paint.Alpha * 255))
	color := &FillColor{Alpha: &alpha}
	color.GouraudShd = shading
	return color, nil
}

// mesh 复用页内网格及当前块采样结果，不缓存失败结果
// 入参: mesh 原曲面, space 输出混合空间, conversion 设备转换函数
// 返回: []pdfShadingPixel 颜色及覆盖, error 几何、颜色或取消错误
func (c *pdfCompositor) mesh(mesh *pdfgo.MeshGradient, space *pdfgo.ColorSpace, conversion pdfgo.ColorConversion) ([]pdfShadingPixel, error) {
	if err := c.importer.ctx.Err(); err != nil {
		return nil, err
	}
	key := pdfMeshKey{mesh, space, conversion, c.softMask}
	if pixels, ok := c.meshes[key]; ok {
		return pixels, nil
	}
	converter, err := space.PrepareConversion(mesh.Space, mesh.Intent, conversion)
	if err != nil {
		return nil, err
	}
	prepared, ok := c.cache.meshes[mesh]
	if !ok {
		var err error
		prepared, err = codecpdf.PrepareMesh(c.importer.ctx, mesh, c.importer.matrix)
		if err != nil {
			return nil, err
		}
		if c.cache.meshes == nil {
			c.cache.meshes = map[*pdfgo.MeshGradient]*codecpdf.MeshPrepared{}
		}
		c.cache.meshes[mesh] = prepared
	}
	options := codecpdf.MeshRasterOptions{Matrix: c.importer.matrix, Origin: pdfgo.Point{X: c.box.X, Y: c.box.Y}, DPI: c.importer.rasterDPI, Width: c.width, Height: c.height, Workers: conversionWorkers(c.importer.ctx), SoftMask: c.softMask}
	pixels, err := codecpdf.MeshRasterPrepared(c.importer.ctx, mesh, prepared, space, converter, options)
	if err != nil {
		return nil, err
	}
	if c.meshes == nil {
		c.meshes = map[pdfMeshKey][]pdfShadingPixel{}
	}
	c.meshes[key] = pixels
	return pixels, nil
}
