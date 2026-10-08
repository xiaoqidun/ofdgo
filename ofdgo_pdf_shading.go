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
	softMask   bool
}

// pdfICCKey 区分原始ICC空间及输出渲染意图
type pdfICCKey struct {
	space  *pdfgo.ColorSpace
	intent pdfgo.Name
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

// pdfGradientError 检查渐变函数与源空间能否原生映射为OFD分段
// 入参: paint 画刷
// 返回: error 不可等价表达的渐变
func pdfGradientError(paint pdfgo.Paint) error {
	if paint.Shading != nil {
		return &pdfgo.UnsupportedError{Feature: "shading pattern state compositing"}
	}
	if paint.Function != nil {
		return &pdfgo.UnsupportedError{Feature: "function shading conversion"}
	}
	if paint.Mesh != nil {
		mesh := paint.Mesh
		if len(mesh.Patches) != 0 || len(mesh.Triangles) == 0 || mesh.UsesFunction() || mesh.Background != nil || mesh.Bounds != nil {
			return &pdfgo.UnsupportedError{Feature: "nonlinear mesh conversion"}
		}
		if mesh.Space.Calibrated() && !pdfICCTransfer(mesh.Space) && !mesh.Space.SRGBEquivalent() {
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
	converter, err := space.PrepareConversion(source, intent, conversion)
	if err != nil {
		return nil, err
	}
	colorants := pdfPrepareShadingColorants(paint, space, c.softMask)
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
							if colorants != nil {
								values, err = colorants.pointAt(local)
							} else {
								values, err = g.ValuesAt(local)
								if err == nil {
									values, err = converter.Convert(values[:source.Components()])
								}
							}
						}
					} else {
						var parameter float64
						parameter, visible = position.PositionAt(point)
						if visible {
							if colorants != nil {
								values, err = colorants.at(parameter)
							} else if paint.Axial != nil {
								values, err = paint.Axial.ValuesAt(parameter)
							} else {
								values, err = paint.Radial.ValuesAt(parameter)
							}
							if err == nil && colorants == nil {
								values, err = converter.Convert(values[:source.Components()])
							}
						}
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
	var geometry pdfgo.GradientPosition
	var err error
	if g := paint.Axial; g != nil {
		geometry, err = g.PreparePosition()
	} else {
		geometry, err = paint.Radial.PreparePosition()
	}
	if err != nil {
		return 0, false
	}
	return geometry.PositionAt(point)
}
