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

	"github.com/xiaoqidun/pdfgo"
)

// pdfShadingPixel 保存抗锯齿覆盖率与混合空间分量
type pdfShadingPixel struct {
	values [4]float64
	shape  float64
}

// pdfGradientKey 区分渐变画刷及其输出混合空间
type pdfGradientKey struct {
	axial      *pdfgo.AxialGradient
	radial     *pdfgo.RadialGradient
	function   *pdfgo.FunctionGradient
	space      *pdfgo.ColorSpace
	geometry   pdfCompositeKey
	conversion pdfgo.ColorConversion
}

// gradientSegments 保留线性源颜色的设备分量，校准颜色经源空间转换
// 入参: stops 源颜色分段, space 源颜色空间
// 返回: []ShdSegment OFD颜色分段, error 不可线性表达的变换
func (p *pdfImporter) gradientSegments(stops []pdfgo.GradientStop, space *pdfgo.ColorSpace) ([]ShdSegment, error) {
	if err := pdfGradientStopsError(stops, space); err != nil {
		return nil, err
	}
	cmyk := space != nil && space.Model == "DeviceCMYK" && !space.Calibrated()
	segments := make([]ShdSegment, len(stops))
	for i, stop := range stops {
		paint := pdfgo.Paint{RGB: stop.RGB, Alpha: 1}
		if cmyk {
			paint.CMYK = &stop.Values
		}
		color := p.color(paint)
		segments[i] = ShdSegment{Position: stop.Position, Color: ShdColor{Value: color.Value, ColorSpace: color.ColorSpace}}
	}
	return segments, nil
}

// pdfGradientStopsError 检查源颜色分段是否可由OFD基本颜色表达
// 入参: stops 源颜色分段, space 源颜色空间
// 返回: error 不可线性表达的变换
func pdfGradientStopsError(stops []pdfgo.GradientStop, space *pdfgo.ColorSpace) error {
	if len(stops) == 0 {
		return &pdfgo.UnsupportedError{Feature: "nonlinear gradient conversion"}
	}
	calibrated := space != nil && space.Calibrated() && !space.SRGBEquivalent()
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

// pdfGradientError 检查渐变能否精确映射为OFD线性分段
// 入参: paint 画刷
// 返回: error 不可等价表达的渐变
func pdfGradientError(paint pdfgo.Paint) error {
	if paint.Function != nil {
		return &pdfgo.UnsupportedError{Feature: "function shading conversion"}
	}
	if paint.Mesh != nil {
		mesh := paint.Mesh
		if len(mesh.Patches) != 0 || len(mesh.Triangles) == 0 || mesh.UsesFunction() || mesh.Background != nil || mesh.Bounds != nil {
			return &pdfgo.UnsupportedError{Feature: "nonlinear mesh conversion"}
		}
		if mesh.Space.Calibrated() && !mesh.Space.SRGBEquivalent() {
			return &pdfgo.UnsupportedError{Feature: "nonlinear calibrated mesh conversion"}
		}
	}
	if paint.Axial != nil {
		if paint.Axial.Background != nil || paint.Axial.Bounds != nil {
			return &pdfgo.UnsupportedError{Feature: "bounded axial pattern conversion"}
		}
		return pdfGradientStopsError(paint.Axial.Stops, paint.Axial.Space)
	}
	if paint.Radial != nil {
		if paint.Radial.Background != nil || paint.Radial.Bounds != nil {
			return &pdfgo.UnsupportedError{Feature: "bounded radial pattern conversion"}
		}
		return pdfGradientStopsError(paint.Radial.Stops, paint.Radial.Space)
	}
	return nil
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
	key := pdfGradientKey{axial: paint.Axial, radial: paint.Radial, function: paint.Function, space: space}
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
	var coverage image.Image
	domainClipped := node != nil && paint.Function != nil && background == nil
	if node != nil {
		local, importer := *c, *c.importer
		importer.rasterDPI *= samples
		local.importer = &importer
		var err error
		if domainClipped {
			geometry, ok := c.cache.shadings[key]
			if !ok {
				g := paint.Function
				n := pdfShadingClippedNode(*node, g.Domain, g.Matrix)
				geometry.scene, geometry.box, err = c.importer.compileGroup(func(p *pdfImporter) error { return n.coverage(p, outline) })
				if err != nil {
					return nil, err
				}
				if c.cache.shadings == nil {
					c.cache.shadings = map[pdfGradientKey]pdfCompositeGeometry{}
				}
				c.cache.shadings[key] = geometry
			}
			if geometry.scene != nil {
				coverage, err = importer.renderGroupScene(geometry.scene, c.box)
			}
		} else {
			coverage, err = local.coverage(*node, outline)
		}
		if err != nil {
			return nil, err
		}
		if coverage == nil {
			return make([]pdfShadingPixel, c.width*c.height), nil
		}
	}
	step := 25.4 / c.importer.rasterDPI
	pixels := make([]pdfShadingPixel, c.width*c.height)
	for y := range c.height {
		if err := c.importer.ctx.Err(); err != nil {
			return nil, err
		}
		for x := range c.width {
			pixel := &pixels[y*c.width+x]
			for sy := range samples {
				for sx := range samples {
					weight := 1.0
					if coverage != nil {
						_, _, _, a := coverage.At(x*samples+sx, y*samples+sy).RGBA()
						weight = float64(a) / 65535
						if weight == 0 {
							continue
						}
					}
					pagePoint := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+(float64(sx)+.5)/samples)*step, Y: c.box.Y + (float64(y)+(float64(sy)+.5)/samples)*step})
					point := pagePoint
					local := point
					if bounds != nil || paint.Radial != nil {
						local = inverse.Apply(point)
					}
					if bounds != nil {
						if local.X < bounds.XMin || local.X > bounds.XMax || local.Y < bounds.YMin || local.Y > bounds.YMax {
							continue
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
							values, err = g.ValuesAt(local)
							if err == nil {
								values, err = space.ConvertWith(values[:source.Components()], source, intent, conversion)
							}
						}
					} else {
						values, visible, err = pdfCompositeColor(paint, point, space, intent, conversion)
					}
					if err != nil {
						return nil, err
					}
					if !visible {
						if background == nil {
							continue
						}
						values = backgroundValues
					}
					if node != nil && node.image != nil {
						_, alpha, err := c.imageColor(node.image, pagePoint, space)
						if err != nil {
							return nil, err
						}
						weight *= alpha
					}
					for i, value := range values {
						pixel.values[i] += value * weight
					}
					pixel.shape += weight
				}
			}
			if pixel.shape != 0 {
				for i := range pixel.values {
					pixel.values[i] /= pixel.shape
				}
				pixel.shape /= samples * samples
			}
		}
	}
	if c.gradients == nil {
		c.gradients = map[pdfGradientKey][]pdfShadingPixel{}
	}
	c.gradients[key] = pixels
	return pixels, nil
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

// pdfShadingPosition 计算渐变参数，未延伸区域及不存在非负圆半径的位置不着色
// 入参: paint 渐变画刷, point 页面坐标
// 返回: float64 归一化参数, bool 是否着色
func pdfShadingPosition(paint pdfgo.Paint, point pdfgo.Point) (float64, bool) {
	if g := paint.Axial; g != nil {
		dx, dy := g.End.X-g.Start.X, g.End.Y-g.Start.Y
		t := ((point.X-g.Start.X)*dx + (point.Y-g.Start.Y)*dy) / (dx*dx + dy*dy)
		return t, finite(t) && (t >= 0 || g.Extend[0]) && (t <= 1 || g.Extend[1])
	}
	g := paint.Radial
	if g.Matrix != (pdfgo.Matrix{}) {
		inverse, ok := g.Matrix.Inverse()
		if !ok {
			return 0, false
		}
		point = inverse.Apply(point)
	}
	dx, dy, dr := g.End.X-g.Start.X, g.End.Y-g.Start.Y, g.EndRadius-g.StartRadius
	px, py := point.X-g.Start.X, point.Y-g.Start.Y
	a, b, c := dx*dx+dy*dy-dr*dr, -2*(px*dx+py*dy+g.StartRadius*dr), px*px+py*py-g.StartRadius*g.StartRadius
	roots := [2]float64{math.NaN(), math.NaN()}
	if a == 0 {
		if b != 0 {
			roots[0] = -c / b
		}
	} else if discriminant := b*b - 4*a*c; discriminant >= 0 {
		q := -.5 * (b + math.Copysign(math.Sqrt(discriminant), b))
		if q == 0 {
			roots[0] = -b / (2 * a)
		} else {
			roots[0], roots[1] = q/a, c/q
		}
	}
	t := math.Inf(-1)
	for _, root := range roots {
		if finite(root) && g.StartRadius+root*dr >= 0 && (root >= 0 || g.Extend[0]) && (root <= 1 || g.Extend[1]) && root > t {
			t = root
		}
	}
	return t, finite(t)
}
