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

	"github.com/xiaoqidun/pdfgo"
)

// draw 按原始顺序在当前颜色空间绘制图元及透明组
// 入参: nodes 图元, pixels 背景与输出像素, space 混合空间
// 返回: error 几何或颜色错误
func (c *pdfCompositor) draw(nodes []pdfCompositeNode, pixels []pdfCompositePixel, space *pdfgo.ColorSpace) error {
	for _, node := range nodes {
		if err := c.importer.ctx.Err(); err != nil {
			return err
		}
		if node.group != nil {
			if err := c.drawGroup(node, pixels, space); err != nil {
				return err
			}
			continue
		}
		if err := c.drawMark(node, pixels, space); err != nil {
			return err
		}
	}
	return nil
}

// drawMark 将填充与描边作为同一对象，保留重叠区域的挖空和叠印语义
// 入参: node 图元, pixels 背景与输出像素, space 混合空间
// 返回: error 几何或颜色错误
func (c *pdfCompositor) drawMark(node pdfCompositeNode, pixels []pdfCompositePixel, space *pdfgo.ColorSpace) error {
	style, fill, stroke := node.style()
	mask, err := c.mask(style.SoftMask, space)
	if err != nil {
		return err
	}
	var initial []pdfCompositePixel
	result := pixels
	grouped := fill && stroke && (style.FillOverprint || style.StrokeOverprint) && style.Fill.Alpha == style.Stroke.Alpha
	if fill && stroke {
		initial = append([]pdfCompositePixel(nil), pixels...)
		if grouped {
			result = append([]pdfCompositePixel(nil), pixels...)
			for i := range result {
				result[i].effect = 0
			}
		}
	}
	for _, outline := range []bool{false, true} {
		if outline && !stroke || !outline && !fill {
			continue
		}
		paint, overprint := style.Fill, style.FillOverprint
		if outline {
			paint, overprint = style.Stroke, style.StrokeOverprint
		}
		mode, paintMask := style.BlendMode, mask
		var knockout []pdfCompositePixel
		if grouped {
			paint.Alpha, mode, paintMask = 1, "Normal", nil
		} else if outline {
			knockout = initial
		}
		if err := c.drawPaint(node, outline, paint, mode, overprint, paintMask, knockout, result, space); err != nil {
			return err
		}
	}
	if grouped {
		return pdfCompositeGroup(pixels, initial, result, space, space, style.Fill.Alpha, mask, style.BlendMode)
	}
	return nil
}

// drawPaint 结合几何覆盖与源颜色绘制一个填充或描边
// 入参: node 图元, outline 描边标志, paint 画刷, mode 混合模式, overprint 叠印标志, mask 蒙版, initial 挖空初始背景, pixels 输出, space 混合空间
// 返回: error 几何或颜色错误
func (c *pdfCompositor) drawPaint(node pdfCompositeNode, outline bool, paint pdfgo.Paint, mode pdfgo.Name, overprint bool, mask []float64, initial, pixels []pdfCompositePixel, space *pdfgo.ColorSpace) error {
	coverage, err := c.coverage(node, outline)
	if err != nil || coverage == nil {
		return err
	}
	style, _, _ := node.style()
	compatible := overprint && style.OverprintMode == 1 && node.image == nil && space.Model == "DeviceCMYK" && !space.Calibrated() && (paint.CMYK != nil || paint.Space != nil && paint.Space.Model == "DeviceCMYK" && !paint.Space.Calibrated())
	step := 25.4 / c.importer.rasterDPI
	uniform := node.image == nil && paint.Axial == nil && paint.Radial == nil && paint.Tiling == nil
	var uniformValues [4]float64
	uniformReady := false
	for y := 0; y < c.height; y++ {
		if err := c.importer.ctx.Err(); err != nil {
			return err
		}
		for x := 0; x < c.width; x++ {
			_, _, _, a := coverage.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			index := y*c.width + x
			shape, opacity := float64(a)/65535, paint.Alpha
			if mask != nil {
				opacity *= mask[index]
			}
			var values [4]float64
			if uniform {
				if !uniformReady {
					uniformValues, _, err = pdfCompositeColor(paint, pdfgo.Point{}, space, style.RenderingIntent)
					uniformReady = true
				}
				values = uniformValues
			} else {
				point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: c.box.Y + (float64(y)+.5)*step})
				if node.image != nil {
					var alpha float64
					values, alpha, err = c.imageColor(*node.image, point, space)
					opacity *= alpha
				} else {
					var visible bool
					values, visible, err = pdfCompositeColor(paint, point, space, style.RenderingIntent)
					if !visible {
						shape = 0
					}
				}
			}
			if err != nil {
				return err
			}
			if initial != nil {
				if style.AlphaIsShape {
					shape, opacity = shape*opacity, 1
				}
				pixel := initial[index]
				if err := pdfCompositePaintOver(&pixel, values, opacity, space, mode, compatible); err != nil {
					return err
				}
				pdfCompositeInterpolate(&pixels[index], pixel, shape)
			} else if err := pdfCompositePaintOver(&pixels[index], values, shape*opacity, space, mode, compatible); err != nil {
				return err
			}
		}
	}
	return nil
}

// pdfCompositeInterpolate 按形状覆盖混合预乘颜色及累计透明度
// 入参: target 原像素与输出, source 替换像素, shape 覆盖率
func pdfCompositeInterpolate(target *pdfCompositePixel, source pdfCompositePixel, shape float64) {
	alpha := target.alpha*(1-shape) + source.alpha*shape
	if alpha != 0 {
		for i := range target.values {
			target.values[i] = (target.values[i]*target.alpha*(1-shape) + source.values[i]*source.alpha*shape) / alpha
		}
	}
	target.alpha = alpha
	target.effect = target.effect*(1-shape) + source.effect*shape
}

// drawGroup 按隔离标志保留初始背景，移除背景贡献后应用组透明度
// 入参: node 透明组, pixels 父组像素, parent 父混合空间
// 返回: error 颜色或合成错误
func (c *pdfCompositor) drawGroup(node pdfCompositeNode, pixels []pdfCompositePixel, parent *pdfgo.ColorSpace) error {
	g := node.group
	space := g.ColorSpace
	if space == nil {
		space = parent
	}
	initial := make([]pdfCompositePixel, len(pixels))
	if !g.Isolated {
		for i, pixel := range pixels {
			values, err := space.Convert(pixel.values[:parent.Components()], parent, "RelativeColorimetric")
			if err != nil {
				return err
			}
			initial[i] = pdfCompositePixel{values: values, alpha: pixel.alpha}
		}
	}
	result := append([]pdfCompositePixel(nil), initial...)
	if err := c.draw(node.children, result, space); err != nil {
		return err
	}
	mask, err := c.mask(g.SoftMask, parent)
	if err != nil {
		return err
	}
	return pdfCompositeGroup(pixels, initial, result, space, parent, g.Alpha, mask, g.BlendMode)
}

// pdfCompositeGroup 移除组初始背景贡献后，将组结果合成到父空间
// 入参: pixels 父组输出, initial 初始背景, result 组结果, space 组空间, parent 父空间, opacity 组不透明度, mask 蒙版, mode 混合模式
// 返回: error 颜色或混合错误
func pdfCompositeGroup(pixels, initial, result []pdfCompositePixel, space, parent *pdfgo.ColorSpace, opacity float64, mask []float64, mode pdfgo.Name) error {
	for i, pixel := range result {
		if pixel.effect == 0 {
			continue
		}
		values := pixel.values
		for j := 0; j < space.Components(); j++ {
			values[j] = math.Max(0, math.Min(1, (pixel.values[j]*pixel.alpha-initial[i].values[j]*initial[i].alpha*(1-pixel.effect))/pixel.effect))
		}
		values, err := parent.Convert(values[:space.Components()], space, "RelativeColorimetric")
		if err != nil {
			return err
		}
		alpha := pixel.effect * opacity
		if mask != nil {
			alpha *= mask[i]
		}
		if err := pdfCompositeOver(&pixels[i], values, alpha, parent, mode); err != nil {
			return err
		}
	}
	return nil
}

// mask 在组颜色空间计算蒙版，保留背景色、组隔离和亮度传递
// 入参: mask 蒙版或空值, inherited 未指定蒙版空间时继承的空间
// 返回: []float64 蒙版透明度或空值, error 蒙版错误
func (c *pdfCompositor) mask(mask *pdfgo.SoftMask, inherited *pdfgo.ColorSpace) ([]float64, error) {
	if mask == nil {
		return nil, nil
	}
	if cached, ok := c.masks[mask]; ok {
		return cached, nil
	}
	nodes, ok := c.cache.masks[mask]
	var err error
	if !ok {
		nodes, err = pdfCompositeNodes(mask.Walk, c.importer.warning)
		if err != nil {
			return nil, err
		}
		c.cache.masks[mask] = nodes
	}
	space := mask.ColorSpace
	if space == nil {
		space = inherited
	}
	pixels := make([]pdfCompositePixel, c.width*c.height)
	if mask.Subtype == "Luminosity" {
		for i := range pixels {
			copy(pixels[i].values[:], mask.Backdrop)
			pixels[i].alpha = 1
		}
	}
	if err := c.draw(nodes, pixels, space); err != nil {
		return nil, err
	}
	result := make([]float64, len(pixels))
	for i, pixel := range pixels {
		v := pixel.alpha
		if mask.Subtype == "Luminosity" {
			v, err = space.Luminosity(pixel.values[:space.Components()], "RelativeColorimetric")
			if err != nil {
				return nil, err
			}
		}
		result[i] = math.Max(0, math.Min(1, mask.Transfer[0]+v*(mask.Transfer[1]-mask.Transfer[0])))
	}
	c.masks[mask] = result
	return result, nil
}

// imageColor 采样映射后的原始图像分量，插值在源空间内进行
// 入参: mark 图像, point 页面坐标, space 混合空间
// 返回: [4]float64 源颜色, float64 图像透明度, error 解码或变换错误
func (c *pdfCompositor) imageColor(mark pdfgo.ImageMark, point pdfgo.Point, space *pdfgo.ColorSpace) ([4]float64, float64, error) {
	source := c.cache.images[mark.Image]
	if source == nil {
		var err error
		source, err = mark.Image.DecodeComponents()
		if err != nil {
			return [4]float64{}, 0, err
		}
		c.cache.images[mark.Image] = source
	}
	inverse, ok := mark.Matrix.Inverse()
	if !ok {
		return [4]float64{}, 0, fmt.Errorf("invalid PDF image matrix")
	}
	point = inverse.Apply(point)
	if point.X < 0 || point.X > 1 || point.Y < 0 || point.Y > 1 {
		return [4]float64{}, 0, nil
	}
	x, y := point.X*float64(source.Rect.Dx())-.5, (1-point.Y)*float64(source.Rect.Dy())-.5
	sample := func(x, y int) ([4]float64, float64) {
		return source.ValuesAt(max(0, min(source.Rect.Dx()-1, x)), max(0, min(source.Rect.Dy()-1, y)))
	}
	values, alpha := sample(int(math.Floor(x+.5)), int(math.Floor(y+.5)))
	if mark.Image.Interpolate {
		ix, iy := int(math.Floor(x)), int(math.Floor(y))
		fx, fy := x-float64(ix), y-float64(iy)
		values, alpha = [4]float64{}, 0
		for j := 0; j < 2; j++ {
			for i := 0; i < 2; i++ {
				weight := (1 - fx) * (1 - fy)
				if i == 1 {
					weight = fx * (1 - fy)
				}
				if j == 1 {
					weight = (1 - fx) * fy
					if i == 1 {
						weight = fx * fy
					}
				}
				v, a := sample(ix+i, iy+j)
				alpha += a * weight
				for k := 0; k < source.Space.Components(); k++ {
					values[k] += v[k] * a * weight
				}
			}
		}
		if alpha != 0 {
			for k := range values {
				values[k] = math.Max(0, math.Min(1, values[k]/alpha))
			}
		}
	}
	if mark.Image.ImageMask {
		color, visible, err := pdfCompositeColor(mark.Style.Fill, mark.Matrix.Apply(point), space, mark.Style.RenderingIntent)
		if !visible {
			return color, 0, err
		}
		return color, alpha * (1 - values[0]), err
	}
	values, err := space.Convert(values[:source.Space.Components()], source.Space, mark.Style.RenderingIntent)
	return values, alpha, err
}

// pdfCompositeColor 将原始画刷变换到混合空间，渐变在源空间求值
// 入参: paint 画刷, point 页面坐标, space 混合空间, intent 渲染意图
// 返回: [4]float64 混合分量, bool 是否着色, error 颜色错误
func pdfCompositeColor(paint pdfgo.Paint, point pdfgo.Point, space *pdfgo.ColorSpace, intent pdfgo.Name) ([4]float64, bool, error) {
	if paint.Tiling != nil {
		return [4]float64{}, false, &pdfgo.UnsupportedError{Feature: "local tiling pattern compositing"}
	}
	var values [4]float64
	var source *pdfgo.ColorSpace
	if paint.Axial != nil || paint.Radial != nil {
		t, visible := pdfShadingPosition(paint, point)
		if !visible {
			return values, false, nil
		}
		var err error
		if g := paint.Axial; g != nil {
			values, err = g.ValuesAt(t)
			source, intent = g.Space, g.Intent
		} else {
			g := paint.Radial
			values, err = g.ValuesAt(t)
			source, intent = g.Space, g.Intent
		}
		if err != nil {
			return values, false, err
		}
	} else if paint.Space != nil {
		values, source = paint.Values, paint.Space
	} else if paint.CMYK != nil {
		values, source = *paint.CMYK, &pdfgo.ColorSpace{Model: "DeviceCMYK"}
	} else {
		copy(values[:], paint.RGB[:])
		source = &pdfgo.ColorSpace{Model: "DeviceRGB"}
	}
	values, err := space.Convert(values[:source.Components()], source, intent)
	return values, true, err
}

// pdfCompositePaintOver 以兼容叠印计算零色料分量，再应用对象混合模式
// 入参: backdrop 背景及输出, source 源分量, alpha 源透明度, space 混合空间, mode 混合模式, compatible 是否保留零色料背景
// 返回: error 未支持的混合模式
func pdfCompositePaintOver(backdrop *pdfCompositePixel, source [4]float64, alpha float64, space *pdfgo.ColorSpace, mode pdfgo.Name, compatible bool) error {
	if compatible {
		for i := range source {
			if source[i] == 0 {
				source[i] = backdrop.values[i] * backdrop.alpha
			}
		}
	}
	return pdfCompositeOver(backdrop, source, alpha, space, mode)
}

// pdfCompositeOver 按PDF透明模型合成源颜色，四色混合使用分量补色
// 入参: backdrop 背景及输出, source 源分量, alpha 源透明度, space 混合空间, mode 混合模式
// 返回: error 未支持的混合模式
func pdfCompositeOver(backdrop *pdfCompositePixel, source [4]float64, alpha float64, space *pdfgo.ColorSpace, mode pdfgo.Name) error {
	if alpha == 0 {
		return nil
	}
	combined := alpha + backdrop.alpha*(1-alpha)
	for i := 0; i < space.Components(); i++ {
		b, s := backdrop.values[i], source[i]
		if space.Model == "DeviceCMYK" {
			b, s = 1-b, 1-s
		}
		mixed, err := pdfSeparableBlend(b, s, mode)
		if err != nil {
			return err
		}
		if space.Model == "DeviceCMYK" {
			mixed = 1 - mixed
		}
		backdrop.values[i] = math.Max(0, math.Min(1, ((1-alpha)*backdrop.alpha*backdrop.values[i]+alpha*((1-backdrop.alpha)*source[i]+backdrop.alpha*mixed))/combined))
	}
	backdrop.alpha = combined
	backdrop.effect = alpha + backdrop.effect*(1-alpha)
	return nil
}

// pdfSeparableBlend 计算标准可分离混合函数
// 入参: b 背景分量, s 源分量, mode 混合模式
// 返回: float64 混合分量, error 未支持的模式
func pdfSeparableBlend(b, s float64, mode pdfgo.Name) (float64, error) {
	switch mode {
	case "", "Normal", "Compatible":
		return s, nil
	case "Multiply":
		return b * s, nil
	case "Screen":
		return b + s - b*s, nil
	case "Overlay":
		if b <= .5 {
			return 2 * b * s, nil
		}
		return 1 - 2*(1-b)*(1-s), nil
	case "Darken":
		return math.Min(b, s), nil
	case "Lighten":
		return math.Max(b, s), nil
	case "ColorDodge":
		if b == 0 {
			return 0, nil
		}
		if s == 1 {
			return 1, nil
		}
		return math.Min(1, b/(1-s)), nil
	case "ColorBurn":
		if b == 1 {
			return 1, nil
		}
		if s == 0 {
			return 0, nil
		}
		return 1 - math.Min(1, (1-b)/s), nil
	case "HardLight":
		if s <= .5 {
			return 2 * b * s, nil
		}
		return 1 - 2*(1-b)*(1-s), nil
	case "SoftLight":
		if s <= .5 {
			return b - (1-2*s)*b*(1-b), nil
		}
		d := math.Sqrt(b)
		if b <= .25 {
			d = ((16*b-12)*b + 4) * b
		}
		return b + (2*s-1)*(d-b), nil
	case "Difference":
		return math.Abs(b - s), nil
	case "Exclusion":
		return b + s - 2*b*s, nil
	default:
		return 0, &pdfgo.UnsupportedError{Feature: "nonseparable blend mode " + string(mode)}
	}
}
