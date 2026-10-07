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
	"context"
	"fmt"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// draw 按原始顺序在当前颜色空间绘制图元及透明组
// 入参: nodes 图元, pixels 背景与输出像素, space 混合空间
// 返回: error 几何或颜色错误
func (c *pdfCompositor) draw(nodes []pdfCompositeNode, pixels []pdfCompositePixel, space *pdfgo.ColorSpace) error {
	if err := c.importer.ctx.Err(); err != nil {
		return err
	}
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
	box, err := c.markBounds(node)
	if err != nil || c.pixelBounds(box).Empty() {
		return err
	}
	style, fill, stroke := node.style()
	mask, err := c.mask(style.SoftMask, space)
	if err != nil {
		return err
	}
	var initial []pdfCompositePixel
	result := pixels
	grouped := fill && stroke && (style.FillOverprint || style.StrokeOverprint) && style.Fill.Alpha == style.Stroke.Alpha
	if fill && stroke {
		initial = c.acquirePixels(len(pixels))
		defer c.releasePixels(initial)
		copy(initial, pixels)
		if grouped {
			result = c.acquirePixels(len(pixels))
			defer c.releasePixels(result)
			copy(result, pixels)
			for i := range result {
				result[i].effect = 0
				result[i].shape = 0
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
		return pdfCompositeGroup(c.importer.ctx, pixels, initial, result, space, space, style.Fill.Alpha, mask, style.BlendMode, style.RenderingIntent, style.ColorConversion)
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
	defer c.releaseCoverage(coverage)
	style, _, _ := node.style()
	var transfer *pdfgo.TransferFunction
	imageShape := false
	if c.transfers != nil {
		transfer, err = c.selectTransfer(node, outline)
		if err != nil {
			return err
		}
		if node.image != nil && (node.image.Image.ImageMask || node.image.Image.Mask != nil) {
			soft, err := node.image.Image.HasSoftMask()
			if err != nil {
				return err
			}
			imageShape = !soft
		}
	}
	var processMask [4]bool
	processOverprint := false
	if overprint && space.Model == "DeviceCMYK" && !space.Calibrated() {
		if node.image != nil {
			processMask, processOverprint, err = pdfImageProcessColorants(node.image.Image)
			if err != nil {
				return err
			}
		} else {
			processMask, processOverprint = paint.Process.CMYKMask()
		}
	}
	step := 25.4 / c.importer.rasterDPI
	uniform := node.image == nil && paint.Axial == nil && paint.Radial == nil && paint.Function == nil && paint.Tiling == nil && paint.Mesh == nil && paint.Shading == nil
	compatible := overprint && !processOverprint && style.OverprintMode == 1 && uniform && paint.SourceSpace == "DeviceCMYK" && space.Model == "DeviceCMYK" && !space.Calibrated()
	var marked *[4]bool
	if processOverprint {
		marked = &processMask
	}
	var pattern []pdfCompositePixel
	if paint.Shading != nil && (node.image == nil || node.image.Image.ImageMask) {
		backdrop := pixels
		if initial != nil {
			backdrop = initial
		}
		pattern, err = c.shadingPattern(paint.Shading, node, outline, backdrop, space)
		if err != nil {
			return err
		}
		defer c.releasePixels(pattern)
	}
	if paint.Tiling != nil && (node.image == nil || node.image.Image.ImageMask) {
		backdrop := pixels
		if initial != nil {
			backdrop = initial
		}
		pattern, err = c.tiling(paint, backdrop, space)
		if err != nil {
			return err
		}
		defer c.releasePixels(pattern)
	}
	var shading []pdfShadingPixel
	if paint.Mesh != nil && (node.image == nil || node.image.Image.ImageMask) {
		shading, err = c.mesh(paint.Mesh, space, style.ColorConversion)
		if err != nil {
			return err
		}
	} else if (paint.Axial != nil || paint.Radial != nil || paint.Function != nil) && (node.image == nil || node.image.Image.ImageMask) {
		shading, err = c.gradient(paint, space, &node, outline)
		if err != nil {
			return err
		}
	}
	var uniformValues [4]float64
	uniformReady := false
	var sampler *pdfCompositeColorSampler
	if g := paint.Axial; node.image == nil && shading == nil && g != nil && g.Bounds == nil && g.Background == nil {
		sampler = &pdfCompositeColorSampler{paint: paint, space: space, intent: style.RenderingIntent, conversion: style.ColorConversion}
	}
	if g := paint.Radial; node.image == nil && shading == nil && g != nil && g.Bounds == nil && g.Background == nil {
		sampler = &pdfCompositeColorSampler{paint: paint, space: space, intent: style.RenderingIntent, conversion: style.ColorConversion}
	}
	bounds, err := c.geometryBounds(node, outline)
	if err != nil {
		return err
	}
	region := c.pixelBounds(bounds)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		if err := c.importer.ctx.Err(); err != nil {
			return err
		}
		for x := region.Min.X; x < region.Max.X; x++ {
			a := imageAlphaAt(coverage, x, y)
			if a == 0 {
				continue
			}
			index := y*c.width + x
			shape, opacity := float64(a)/65535, paint.Alpha
			selectionShape := 1.0
			if mask != nil {
				opacity *= mask[index]
			}
			var values [4]float64
			if pattern != nil {
				sample := pattern[index]
				values = sample.values
				if paint.Shading != nil {
					shape = sample.shape
				} else {
					shape *= sample.shape
				}
				if sample.shape != 0 {
					opacity *= sample.alpha / sample.shape
				}
				if node.image != nil && paint.Shading == nil {
					point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: c.box.Y + (float64(y)+.5)*step})
					var alpha float64
					_, alpha, err = c.imageColor(node.image, point, space)
					if imageShape {
						selectionShape = alpha
					}
					opacity *= alpha
				}
			} else if shading != nil {
				values = shading[index].values
				if paint.Mesh == nil {
					shape = shading[index].shape
				} else {
					shape *= shading[index].shape
				}
				if node.image != nil && paint.Mesh != nil {
					point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: c.box.Y + (float64(y)+.5)*step})
					var alpha float64
					_, alpha, err = c.imageColor(node.image, point, space)
					if imageShape {
						selectionShape = alpha
					}
					if c.shadingSource {
						shape *= alpha
					} else {
						opacity *= alpha
					}
				}
			} else if uniform {
				if !uniformReady {
					uniformValues, _, err = pdfCompositeColor(paint, pdfgo.Point{}, space, style.RenderingIntent, style.ColorConversion)
					uniformReady = true
				}
				values = uniformValues
			} else {
				point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: c.box.Y + (float64(y)+.5)*step})
				if node.image != nil {
					var alpha float64
					values, alpha, err = c.imageColor(node.image, point, space)
					if imageShape {
						selectionShape = alpha
					}
					if c.shadingSource {
						shape *= alpha
					} else {
						opacity *= alpha
					}
				} else {
					var visible bool
					if sampler != nil {
						values, visible, err = sampler.color(point)
					} else {
						values, visible, err = pdfCompositeColor(paint, point, space, style.RenderingIntent, style.ColorConversion)
					}
					if !visible {
						shape = 0
					}
				}
			}
			if err != nil {
				return err
			}
			if style.AlphaIsShape {
				shape, opacity = shape*opacity, 1
			}
			if initial != nil {
				pixel := initial[index]
				if err := pdfCompositePaintOver(&pixel, values, opacity, space, mode, compatible, marked); err != nil {
					return err
				}
				if err := pdfCompositeInterpolate(&pixels[index], pixel, shape); err != nil {
					return err
				}
			} else {
				if err := pdfCompositePaintOver(&pixels[index], values, shape*opacity, space, mode, compatible, marked); err != nil {
					return err
				}
			}
			pixels[index].shape += shape * (1 - pixels[index].shape)
			if c.transfers != nil && shape != 0 && selectionShape != 0 {
				c.transfers[index] = transfer
			}
		}
	}
	return nil
}

// pdfCompositeInterpolate 按形状覆盖混合预乘颜色及累计透明度
// 入参: target 原像素与输出, source 替换像素, shape 覆盖率
// 返回: error 像素或覆盖率错误
func pdfCompositeInterpolate(target *pdfCompositePixel, source pdfCompositePixel, shape float64) error {
	pixel := pdfgo.ColorantPixel{Values: target.values[:], Alpha: target.alpha, Shape: target.shape, Effect: target.effect}
	if err := pixel.Interpolate(pdfgo.ColorantPixel{Values: source.values[:], Alpha: source.alpha, Shape: source.shape, Effect: source.effect}, shape); err != nil {
		return err
	}
	target.alpha, target.effect = pixel.Alpha, pixel.Effect
	return nil
}

// drawGroup 按隔离标志保留初始背景，移除背景贡献后应用组透明度
// 入参: node 透明组, pixels 父组像素, parent 父混合空间
// 返回: error 颜色或合成错误
func (c *pdfCompositor) drawGroup(node pdfCompositeNode, pixels []pdfCompositePixel, parent *pdfgo.ColorSpace) error {
	g := node.group
	b, err := c.groupBounds(node)
	if err != nil {
		return err
	}
	if b.W <= 0 || b.H <= 0 || b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
		return nil
	}
	blocked := c.transferBlocked
	c.transferBlocked = blocked || g.Alpha != 1 || g.SoftMask != nil || !pdfNormalBlend(g.BlendMode)
	defer func() { c.transferBlocked = blocked }()
	if c.transfers != nil && node.textObject != nil && !c.transferBlocked {
		for _, child := range node.children {
			opaque, err := c.importer.transferOpaque(child)
			if err != nil {
				return err
			}
			if !opaque {
				c.transferBlocked = true
				break
			}
		}
	}
	space := g.ColorSpace
	if space == nil {
		space = parent
	}
	if !g.Isolated && !g.Knockout && g.Alpha == 1 && g.SoftMask == nil && pdfNormalBlend(g.BlendMode) && space.Equal(parent) {
		return c.draw(node.children, pixels, space)
	}
	initial := c.acquirePixels(len(pixels))
	defer c.releasePixels(initial)
	if g.Isolated {
		clear(initial)
	} else {
		sameSpace := space.Equal(parent)
		components := parent.Components()
		for i, pixel := range pixels {
			if i%pdfCompositeTileSize == 0 {
				if err := c.importer.ctx.Err(); err != nil {
					return err
				}
			}
			values := pixel.values
			if !sameSpace {
				var err error
				values, err = space.Convert(values[:components], parent, "RelativeColorimetric")
				if err != nil {
					return err
				}
			}
			initial[i] = pdfCompositePixel{values: values, alpha: pixel.alpha}
		}
	}
	result := c.acquirePixels(len(initial))
	defer c.releasePixels(result)
	copy(result, initial)
	if g.Knockout {
		if err := c.drawKnockout(node.children, initial, result, space); err != nil {
			return err
		}
	} else {
		if err := c.draw(node.children, result, space); err != nil {
			return err
		}
	}
	mask, err := c.mask(g.SoftMask, parent)
	if err != nil {
		return err
	}
	if g.AlphaIsShape {
		for i := range result {
			if i%pdfCompositeTileSize == 0 {
				if err := c.importer.ctx.Err(); err != nil {
					return err
				}
			}
			result[i].shape *= g.Alpha
			if mask != nil {
				result[i].shape *= mask[i]
			}
		}
	}
	return pdfCompositeGroup(c.importer.ctx, pixels, initial, result, space, parent, g.Alpha, mask, g.BlendMode, g.RenderingIntent, g.ColorConversion)
}

// drawKnockout 将每个对象与组初始背景合成，再按对象形状替换先前对象
// 入参: nodes 组图元, initial 初始背景, pixels 组输出, space 混合空间
// 返回: error 图元或合成错误
func (c *pdfCompositor) drawKnockout(nodes []pdfCompositeNode, initial, pixels []pdfCompositePixel, space *pdfgo.ColorSpace) error {
	if err := c.importer.ctx.Err(); err != nil {
		return err
	}
	candidate := c.acquirePixels(len(initial))
	defer c.releasePixels(candidate)
	clear(candidate)
	for _, node := range nodes {
		var box Box
		var err error
		if node.group != nil {
			box, err = c.groupBounds(node)
		} else {
			box, err = c.markBounds(node)
		}
		if err != nil {
			return err
		}
		region := c.pixelBounds(box)
		if region.Empty() {
			continue
		}
		for y := region.Min.Y; y < region.Max.Y; y++ {
			if err := c.importer.ctx.Err(); err != nil {
				return err
			}
			start, end := y*c.width+region.Min.X, y*c.width+region.Max.X
			copy(candidate[start:end], initial[start:end])
		}
		if err := c.draw([]pdfCompositeNode{node}, candidate, space); err != nil {
			return err
		}
		for y := region.Min.Y; y < region.Max.Y; y++ {
			for x := region.Min.X; x < region.Max.X; x++ {
				i := y*c.width + x
				if err := pdfCompositeKnockout(&pixels[i], initial[i], candidate[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// pdfCompositeKnockout 移除未覆盖区域的初始背景贡献，保留形状与不透明度差异
// 入参: target 累计组结果, initial 初始背景, source 单个对象合成结果
// 返回: error 像素错误
func pdfCompositeKnockout(target *pdfCompositePixel, initial, source pdfCompositePixel) error {
	pixel := pdfgo.ColorantPixel{Values: target.values[:], Alpha: target.alpha, Shape: target.shape, Effect: target.effect}
	if err := pixel.Knockout(
		pdfgo.ColorantPixel{Values: initial.values[:], Alpha: initial.alpha, Shape: initial.shape, Effect: initial.effect},
		pdfgo.ColorantPixel{Values: source.values[:], Alpha: source.alpha, Shape: source.shape, Effect: source.effect},
	); err != nil {
		return err
	}
	target.alpha, target.effect, target.shape = pixel.Alpha, pixel.Effect, pixel.Shape
	return nil
}

// pdfCompositeGroup 移除组初始背景贡献后，将组结果合成到父空间
// 入参: ctx 取消上下文, pixels 父组输出, initial 初始背景, result 组结果, space 组空间, parent 父空间, opacity 组不透明度, mask 蒙版, mode 混合模式, intent 渲染意图, conversion 设备转换函数
// 返回: error 颜色或混合错误
func pdfCompositeGroup(ctx context.Context, pixels, initial, result []pdfCompositePixel, space, parent *pdfgo.ColorSpace, opacity float64, mask []float64, mode, intent pdfgo.Name, conversion pdfgo.ColorConversion) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pixels) != len(initial) || len(pixels) != len(result) || mask != nil && len(mask) != len(pixels) {
		return fmt.Errorf("invalid PDF transparency group buffers")
	}
	group, destination := pdfgo.ColorantGroup{Space: space}, pdfgo.ColorantGroup{Space: parent}
	compositor, err := destination.PrepareGroup(&group, mode, intent, conversion)
	if err != nil {
		return err
	}
	components, parentComponents := space.Components(), parent.Components()
	for i, pixel := range result {
		if i%pdfCompositeTileSize == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		alpha := opacity
		if mask != nil {
			alpha *= mask[i]
		}
		target := pdfgo.ColorantPixel{Values: pixels[i].values[:parentComponents], Alpha: pixels[i].alpha, Shape: pixels[i].shape, Effect: pixels[i].effect}
		if err := compositor.Composite(&target,
			pdfgo.ColorantPixel{Values: initial[i].values[:components], Alpha: initial[i].alpha, Shape: initial[i].shape, Effect: initial[i].effect},
			pdfgo.ColorantPixel{Values: pixel.values[:components], Alpha: pixel.alpha, Shape: pixel.shape, Effect: pixel.effect},
			alpha,
		); err != nil {
			return err
		}
		pixels[i].alpha, pixels[i].effect, pixels[i].shape = target.Alpha, target.Effect, target.Shape
	}
	return nil
}

// mask 在组颜色空间计算蒙版，保留背景色、组隔离和亮度传递
// 入参: mask 蒙版或空值, inherited 未指定蒙版空间时继承的空间
// 返回: []float64 蒙版透明度或空值, error 蒙版错误
func (c *pdfCompositor) mask(mask *pdfgo.SoftMask, inherited *pdfgo.ColorSpace) ([]float64, error) {
	if err := c.importer.ctx.Err(); err != nil {
		return nil, err
	}
	if mask == nil {
		return nil, nil
	}
	if cached, ok := c.masks[mask]; ok {
		return cached, nil
	}
	nodes, ok := c.cache.masks[mask]
	var err error
	if !ok {
		nodes, err = c.importer.collectCompositeNodes(func(v pdfgo.Visitor) error { return mask.WalkContext(c.importer.ctx, v) })
		if err != nil {
			return nil, err
		}
		c.cache.masks[mask] = nodes
	}
	space := mask.ColorSpace
	if space == nil {
		space = inherited
	}
	pixels := c.acquirePixels(c.width * c.height)
	defer c.releasePixels(pixels)
	clear(pixels)
	if mask.Subtype == "Luminosity" {
		for i := range pixels {
			if i%pdfCompositeTileSize == 0 {
				if err := c.importer.ctx.Err(); err != nil {
					return nil, err
				}
			}
			copy(pixels[i].values[:], mask.Backdrop)
			pixels[i].alpha = 1
		}
	}
	local := *c
	local.transfers = nil
	if err := local.draw(nodes, pixels, space); err != nil {
		return nil, err
	}
	result := c.scratch.masks.acquire(len(pixels))
	complete := false
	defer func() {
		if !complete {
			c.scratch.masks.release(result)
		}
	}()
	for i, pixel := range pixels {
		if i%pdfCompositeTileSize == 0 {
			if err := c.importer.ctx.Err(); err != nil {
				return nil, err
			}
		}
		v := pixel.alpha
		if mask.Subtype == "Luminosity" {
			v, err = space.Luminosity(pixel.values[:space.Components()], "RelativeColorimetric")
			if err != nil {
				return nil, err
			}
		}
		result[i], err = mask.Transfer(v)
		if err != nil {
			return nil, err
		}
	}
	complete = true
	c.masks[mask] = result
	return result, nil
}

// imageColor 采样映射后的原始图像分量，插值在源空间内进行
// 入参: mark 图像, point 页面坐标, space 混合空间
// 返回: [4]float64 源颜色, float64 图像透明度, error 解码或变换错误
func (c *pdfCompositor) imageColor(mark *pdfgo.ImageMark, point pdfgo.Point, space *pdfgo.ColorSpace) ([4]float64, float64, error) {
	inverse, found := c.cache.imageMatrix[mark]
	if !found {
		var ok bool
		inverse, ok = mark.Matrix.Inverse()
		if !ok {
			return [4]float64{}, 0, fmt.Errorf("invalid PDF image matrix")
		}
		if c.cache.imageMatrix == nil {
			c.cache.imageMatrix = make(map[*pdfgo.ImageMark]pdfgo.Matrix)
		}
		c.cache.imageMatrix[mark] = inverse
	}
	point = inverse.Apply(point)
	if point.X < 0 || point.X > 1 || point.Y < 0 || point.Y > 1 {
		return [4]float64{}, 0, nil
	}
	source := c.cache.images[mark.Image]
	if source == nil {
		var err error
		source, err = mark.Image.DecodeComponentsViewContext(c.importer.ctx)
		if err != nil {
			return [4]float64{}, 0, err
		}
		c.cache.images[mark.Image] = source
	}
	x, y := point.X*float64(source.Rect.Dx())-.5, (1-point.Y)*float64(source.Rect.Dy())-.5
	_, nativeProcess := source.Process.CMYKMask()
	process := mark.Style.FillOverprint && space.Model == "DeviceCMYK" && !space.Calibrated() && len(source.Colorants) > 0
	var indices [4]int
	if process && !nativeProcess {
		for i, name := range source.Colorants {
			index := -1
			for j, known := range []pdfgo.Name{"Cyan", "Magenta", "Yellow", "Black"} {
				if name == known {
					index = j
				}
			}
			if index < 0 {
				process = false
				break
			}
			indices[i] = index
		}
	}
	sample := func(x, y int) ([4]float64, float64) {
		x, y = max(0, min(source.Rect.Dx()-1, x)), max(0, min(source.Rect.Dy()-1, y))
		values, alpha := source.ValuesAt(x, y)
		if process && !nativeProcess {
			values = [4]float64{}
			for j, tint := range source.TintsAt(x, y) {
				values[indices[j]] = float64(tint) / 65535
			}
		}
		return values, alpha
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
				for k := range values {
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
		if mark.Style.Fill.Tiling != nil || mark.Style.Fill.Shading != nil {
			return [4]float64{}, alpha * (1 - values[0]), nil
		}
		color, visible, err := pdfCompositeColor(mark.Style.Fill, mark.Matrix.Apply(point), space, mark.Style.RenderingIntent, mark.Style.ColorConversion)
		if !visible {
			return color, 0, err
		}
		return color, alpha * (1 - values[0]), err
	}
	if process || space.Equal(source.Space) {
		return values, alpha, nil
	}
	values, err := space.ConvertWith(values[:source.Space.Components()], source.Space, mark.Style.RenderingIntent, mark.Style.ColorConversion)
	return values, alpha, err
}

// pdfImageProcessColorants 识别可在四色设备上直接绘制的专色色料
// 入参: image PDF图像
// 返回: [4]bool 参与绘制的四色通道, bool 是否全部属于印刷原色, error 色空间错误
func pdfImageProcessColorants(image *pdfgo.Image) ([4]bool, bool, error) {
	var mask [4]bool
	process, err := image.ProcessColorants()
	if err != nil {
		return mask, false, err
	}
	if mask, native := process.CMYKMask(); native {
		return mask, true, nil
	}
	names, err := image.Colorants()
	if err != nil {
		return mask, false, err
	}
	if len(names) == 0 {
		return mask, false, nil
	}
	for _, name := range names {
		found := false
		for j, known := range []pdfgo.Name{"Cyan", "Magenta", "Yellow", "Black"} {
			if name == known {
				mask[j] = true
				found = true
				break
			}
		}
		if !found {
			return [4]bool{}, false, nil
		}
	}
	return mask, true, nil
}

// pdfCompositeColor 将原始画刷变换到混合空间，渐变在源空间求值
// 入参: paint 画刷, point 页面坐标, space 混合空间, intent 渲染意图, conversion 设备转换函数
// 返回: [4]float64 混合分量, bool 是否着色, error 颜色错误
func pdfCompositeColor(paint pdfgo.Paint, point pdfgo.Point, space *pdfgo.ColorSpace, intent pdfgo.Name, conversion pdfgo.ColorConversion) ([4]float64, bool, error) {
	if paint.Shading != nil {
		return [4]float64{}, false, &pdfgo.UnsupportedError{Feature: "local shading pattern compositing"}
	}
	if paint.Tiling != nil {
		return [4]float64{}, false, &pdfgo.UnsupportedError{Feature: "local tiling pattern compositing"}
	}
	var values [4]float64
	var source *pdfgo.ColorSpace
	if paint.Function != nil {
		g := paint.Function
		if g.Bounds != nil {
			inverse, ok := g.PatternMatrix.Inverse()
			if !ok {
				return values, false, fmt.Errorf("singular function shading bounds transform")
			}
			local := inverse.Apply(point)
			if local.X < g.Bounds.XMin || local.X > g.Bounds.XMax || local.Y < g.Bounds.YMin || local.Y > g.Bounds.YMax {
				return values, false, nil
			}
		}
		inverse, ok := g.Matrix.Inverse()
		if !ok {
			return values, false, fmt.Errorf("singular function shading transform")
		}
		local := inverse.Apply(point)
		source, intent = g.Space, g.Intent
		if local.X < g.Domain.XMin || local.X > g.Domain.XMax || local.Y < g.Domain.YMin || local.Y > g.Domain.YMax {
			if g.Background == nil {
				return values, false, nil
			}
			values = *g.Background
		} else {
			var err error
			values, err = g.ValuesAt(local)
			if err != nil {
				return values, false, err
			}
		}
	} else if paint.Axial != nil || paint.Radial != nil {
		var background *[4]float64
		var bounds *pdfgo.Rectangle
		var matrix pdfgo.Matrix
		if g := paint.Axial; g != nil {
			source, intent = g.Space, g.Intent
			background, bounds, matrix = g.Background, g.Bounds, g.Matrix
		} else {
			g := paint.Radial
			source, intent = g.Space, g.Intent
			background, bounds, matrix = g.Background, g.Bounds, g.Matrix
		}
		if bounds != nil {
			local := point
			if matrix != (pdfgo.Matrix{}) {
				inverse, ok := matrix.Inverse()
				if !ok {
					return values, false, fmt.Errorf("singular shading transform")
				}
				local = inverse.Apply(point)
			}
			if local.X < bounds.XMin || local.X > bounds.XMax || local.Y < bounds.YMin || local.Y > bounds.YMax {
				return values, false, nil
			}
		}
		t, visible := pdfShadingPosition(paint, point)
		var err error
		if !visible {
			if background == nil {
				return values, false, nil
			}
			values = *background
		} else if paint.Axial != nil {
			values, err = paint.Axial.ValuesAt(t)
		} else {
			values, err = paint.Radial.ValuesAt(t)
		}
		if err != nil {
			return values, false, err
		}
	} else if paint.Space != nil {
		values, source = paint.Values, paint.Space
	} else if paint.CMYK != nil {
		values, source = *paint.CMYK, &pdfgo.ColorSpace{Model: "DeviceCMYK"}
	} else if paint.SourceSpace == "DeviceGray" {
		values[0], source = paint.RGB[0], &pdfgo.ColorSpace{Model: "DeviceGray"}
	} else {
		copy(values[:], paint.RGB[:])
		source = &pdfgo.ColorSpace{Model: "DeviceRGB"}
	}
	values, err := space.ConvertWith(values[:source.Components()], source, intent, conversion)
	return values, true, err
}

// color 复用精确参数对应的颜色，区域、背景及其他画刷保持原求值路径
// 入参: point 页面坐标
// 返回: [4]float64 混合分量, bool 是否着色, error 颜色错误
func (s *pdfCompositeColorSampler) color(point pdfgo.Point) ([4]float64, bool, error) {
	g := s.paint.Axial
	r := s.paint.Radial
	if g == nil && r == nil || g != nil && (g.Bounds != nil || g.Background != nil) || r != nil && (r.Bounds != nil || r.Background != nil) || s.paint.Function != nil || s.paint.Tiling != nil {
		return pdfCompositeColor(s.paint, point, s.space, s.intent, s.conversion)
	}
	if !s.prepared {
		var err error
		if g != nil {
			s.position, err = g.PreparePosition()
		} else {
			s.position, err = r.PreparePosition()
		}
		if err != nil {
			return [4]float64{}, false, err
		}
		s.prepared = true
	}
	position, visible := s.position.PositionAt(point)
	if !visible {
		return [4]float64{}, false, nil
	}
	if values, ok := s.values[position]; ok {
		return values, true, nil
	}
	var values [4]float64
	var source *pdfgo.ColorSpace
	var intent pdfgo.Name
	var err error
	if g != nil {
		values, err = g.ValuesAt(position)
		source, intent = g.Space, g.Intent
	} else {
		values, err = r.ValuesAt(position)
		source, intent = r.Space, r.Intent
	}
	if err == nil {
		if !s.converted {
			s.converter, err = s.space.PrepareConversion(source, intent, s.conversion)
			s.converted = err == nil
		}
		if err == nil {
			values, err = s.converter.Convert(values[:source.Components()])
		}
	}
	if err == nil && g != nil && len(s.values) < 2*pdfCompositeTileSize {
		if s.values == nil {
			s.values = make(map[float64][4]float64)
		}
		s.values[position] = values
	}
	return values, visible, err
}

// pdfCompositePaintOver 以兼容叠印计算零色料分量，再应用对象混合模式
// 入参: backdrop 背景及输出, source 源分量, alpha 源透明度, space 混合空间, mode 混合模式, compatible 是否保留零色料背景, marked 原生过程通道，nil使用普通空间
// 返回: error 未支持的混合模式
func pdfCompositePaintOver(backdrop *pdfCompositePixel, source [4]float64, alpha float64, space *pdfgo.ColorSpace, mode pdfgo.Name, compatible bool, marked *[4]bool) error {
	if !compatible && marked == nil {
		return pdfCompositeOver(backdrop, source, alpha, space, mode)
	}
	if alpha == 0 {
		return nil
	}
	mask := [4]bool{true, true, true, true}
	if marked != nil {
		mask = *marked
	}
	if compatible {
		for i := range source {
			if source[i] == 0 {
				mask[i] = false
			}
		}
	}
	values, combined, err := space.CompositeOverprint(backdrop.values[:space.Components()], source[:space.Components()], backdrop.alpha, alpha, mode, mask[:space.Components()])
	if err != nil {
		return err
	}
	copy(backdrop.values[:space.Components()], values[:space.Components()])
	backdrop.alpha = combined
	backdrop.effect = alpha + backdrop.effect*(1-alpha)
	return nil
}

// pdfCompositeOver 按PDF透明模型合成源颜色，四色混合使用分量补色
// 入参: backdrop 背景及输出, source 源分量, alpha 源透明度, space 混合空间, mode 混合模式
// 返回: error 未支持的混合模式
func pdfCompositeOver(backdrop *pdfCompositePixel, source [4]float64, alpha float64, space *pdfgo.ColorSpace, mode pdfgo.Name) error {
	if alpha == 0 {
		return nil
	}
	values, combined, err := space.Composite(backdrop.values[:space.Components()], source[:space.Components()], backdrop.alpha, alpha, mode)
	if err != nil {
		return err
	}
	copy(backdrop.values[:space.Components()], values[:space.Components()])
	backdrop.alpha = combined
	backdrop.effect = alpha + backdrop.effect*(1-alpha)
	return nil
}
