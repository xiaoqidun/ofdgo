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

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
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
	if err != nil {
		return err
	}
	region := c.pixelBounds(box)
	if region.Empty() {
		return nil
	}
	style, fill, stroke := node.style()
	mask, err := c.mask(style.SoftMask, space)
	if err != nil {
		return err
	}
	var initial []pdfCompositePixel
	var initialChannels []uint8
	result := pixels
	grouped := fill && stroke && (style.FillOverprint || style.StrokeOverprint) && style.Fill.Alpha == style.Stroke.Alpha
	if fill && stroke {
		initial = c.acquirePixels(len(pixels))
		defer c.releasePixels(initial)
		if c.channels != nil && !grouped {
			initialChannels = c.acquireChannels(len(c.channels))
			defer c.releaseChannels(initialChannels)
		}
		if grouped {
			result = c.acquirePixels(len(pixels))
			defer c.releasePixels(result)
		}
		for y := region.Min.Y; y < region.Max.Y; y++ {
			start, end := y*c.width+region.Min.X, y*c.width+region.Max.X
			copy(initial[start:end], pixels[start:end])
			if initialChannels != nil {
				copy(initialChannels[start:end], c.channels[start:end])
			}
			if grouped {
				copy(result[start:end], pixels[start:end])
				for i := start; i < end; i++ {
					result[i].Effect = 0
					result[i].Shape = 0
				}
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
		if err := c.drawPaint(node, outline, paint, mode, overprint, paintMask, knockout, initialChannels, result, space); err != nil {
			return err
		}
	}
	if grouped {
		return codecpdf.CompositeGroupRegion(c.importer.ctx, pixels, initial, result, c.width, region, space, space, style.Fill.Alpha, mask, style.BlendMode, style.RenderingIntent, style.ColorConversion)
	}
	return nil
}

// drawPaint 结合几何覆盖与源颜色绘制一个填充或描边
// 入参: node 图元, outline 描边标志, paint 画刷, mode 混合模式, overprint 叠印标志, mask 蒙版, initial 挖空初始背景, initialChannels 初始通道, pixels 输出, space 混合空间
// 返回: error 几何或颜色错误
func (c *pdfCompositor) drawPaint(node pdfCompositeNode, outline bool, paint pdfgo.Paint, mode pdfgo.Name, overprint bool, mask []float64, initial []pdfCompositePixel, initialChannels []uint8, pixels []pdfCompositePixel, space *pdfgo.ColorSpace) error {
	coverage, covRect, err := c.coverageRect(node, outline)
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
	}
	if node.image != nil && (node.image.Image.ImageMask || node.image.Image.Mask != nil) {
		soft, err := node.image.Image.HasSoftMask()
		if err != nil {
			return err
		}
		imageShape = !soft
	}
	var processMask [4]bool
	processOverprint := false
	var nativePaint *pdfgo.Paint
	if !c.softMask && space.Model == "DeviceCMYK" && !space.Calibrated() {
		if node.image != nil && !node.image.Image.ImageMask {
			if overprint {
				processMask, processOverprint, err = codecpdf.ImageProcessColorants(node.image.Image)
				if err != nil {
					return err
				}
			}
		} else {
			if overprint {
				processMask, processOverprint = paint.Process.CMYKMask()
			}
			if paint.Colorant != nil {
				nativePaint, processMask, err = codecpdf.NativePaint(paint, space, style.RenderingIntent)
				if err != nil {
					return err
				}
				processOverprint = overprint && nativePaint != nil
			}
		}
	}
	var colorants *codecpdf.ShadingColorants
	if node.image == nil || node.image.Image.ImageMask {
		colorants = codecpdf.PrepareShadingColorants(paint, space, c.softMask)
		if colorants != nil && overprint {
			processMask, processOverprint = colorants.Process.Mask(), true
		}
	}
	step := 25.4 / c.importer.rasterDPI
	solid := paint.Axial == nil && paint.Radial == nil && paint.Function == nil && paint.Tiling == nil && paint.Mesh == nil && paint.Shading == nil
	uniform := node.image == nil && solid
	compatible := overprint && !processOverprint && style.OverprintMode == 1 && solid && (node.image == nil || node.image.Image.ImageMask) && paint.SourceSpace == "DeviceCMYK" && space.Model == "DeviceCMYK" && !space.Calibrated()
	sourceSpace := paint.SourceSpace
	if node.image != nil && !node.image.Image.ImageMask && transfer != nil && c.transferModel == "DeviceCMYK" {
		sourceSpace, err = node.image.Image.ColorSpaceFamily()
		if err != nil {
			return err
		}
	}
	var marked *[4]bool
	if processOverprint {
		marked = &processMask
	}
	if transfer != nil && c.transferModel == "DeviceCMYK" && !processOverprint && sourceSpace == "DeviceGray" {
		transfer = transfer.SelectChannels([4]bool{false, false, false, true})
	}
	var transferVariants [16]*pdfgo.TransferFunction
	var transferSelected [16]bool
	var pattern []pdfCompositePixel
	var patternChannels []uint8
	if (paint.Shading != nil || paint.Tiling != nil) && (node.image == nil || node.image.Image.ImageMask) && (c.channels != nil || c.transfers != nil) {
		patternChannels = c.acquireChannels(len(pixels))
		defer c.releaseChannels(patternChannels)
		clear(patternChannels)
	}
	if paint.Shading != nil && (node.image == nil || node.image.Image.ImageMask) {
		backdrop := pixels
		if initial != nil {
			backdrop = initial
		}
		pattern, err = c.shadingPattern(paint.Shading, node, outline, backdrop, space, patternChannels)
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
		pattern, err = c.tiling(paint, backdrop, space, patternChannels)
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
	var uniformPoint pdfgo.Point
	uniformChannels := pdfTransferChannels
	selectChannels := (c.channels != nil || transfer != nil && c.transferModel == "DeviceCMYK") && (compatible || marked != nil || patternChannels != nil)
	uniformReady := false
	var sampler *pdfCompositeColorSampler
	if g := paint.Axial; (node.image == nil || node.image.Image.ImageMask) && shading == nil && g != nil && g.Bounds == nil && g.Background == nil {
		sampler = &pdfCompositeColorSampler{paint: paint, space: space, intent: style.RenderingIntent, conversion: style.ColorConversion, colorants: colorants}
	}
	if g := paint.Radial; (node.image == nil || node.image.Image.ImageMask) && shading == nil && g != nil && g.Bounds == nil && g.Background == nil {
		sampler = &pdfCompositeColorSampler{paint: paint, space: space, intent: style.RenderingIntent, conversion: style.ColorConversion, colorants: colorants}
	}
	region := covRect
	if sampler != nil && node.image == nil && !region.Empty() && c.inverse[1] == 0 && c.inverse[2] == 0 {
		first := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(region.Min.X)+.5)*step, Y: c.box.Y + (float64(region.Min.Y)+.5)*step})
		last := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(region.Max.X)-.5)*step, Y: c.box.Y + (float64(region.Max.Y)-.5)*step})
		if sampler.constant(first, last) {
			uniform, uniformPoint = true, first
		}
	}
	var previous, composed pdfCompositePixel
	var previousValues [4]float64
	var previousAlpha float64
	composedReady := false
	originRecorded := c.origins == nil
	covW := covRect.Dx()
	for y := region.Min.Y; y < region.Max.Y; y++ {
		if err := c.importer.ctx.Err(); err != nil {
			return err
		}
		cy := y - covRect.Min.Y
		row := codecpdf.CoverageRow(coverage, cy, 0, covW)
		for x := region.Min.X; x < region.Max.X; x++ {
			cx := x - covRect.Min.X
			var a uint32
			if row != nil {
				a = uint32(row[4*cx+3]) * 257
			} else {
				a = baseimage.AlphaAt(coverage, cx, cy)
			}
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
				if patternChannels != nil && patternChannels[index]&pdfTransferShape == 0 {
					selectionShape = 0
				}
				values = sample.Values
				if paint.Shading != nil {
					shape = sample.Shape
				} else {
					shape *= sample.Shape
				}
				if sample.Shape != 0 {
					opacity *= sample.Alpha / sample.Shape
				}
				if node.image != nil && paint.Shading == nil {
					point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: c.box.Y + (float64(y)+.5)*step})
					var alpha float64
					_, alpha, err = c.imageColor(node.image, point, space)
					if imageShape {
						selectionShape *= alpha
						shape *= alpha
					} else {
						opacity *= alpha
					}
				}
			} else if shading != nil {
				values = shading[index].Values
				if paint.Mesh == nil {
					shape = shading[index].Shape
				} else {
					shape *= shading[index].Shape
				}
				if node.image != nil && paint.Mesh != nil {
					point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: c.box.Y + (float64(y)+.5)*step})
					var alpha float64
					_, alpha, err = c.imageColor(node.image, point, space)
					if imageShape {
						selectionShape *= alpha
					}
					if c.shadingSource || imageShape {
						shape *= alpha
					} else {
						opacity *= alpha
					}
				}
			} else if uniform {
				if !uniformReady {
					if sampler != nil {
						uniformValues, _, err = sampler.color(uniformPoint)
					} else if nativePaint != nil {
						uniformValues = nativePaint.Values
					} else {
						uniformValues, _, err = codecpdf.CompositeColor(paint, pdfgo.Point{}, space, style.RenderingIntent, style.ColorConversion)
					}
					if selectChannels {
						uniformChannels = 0
						for j, selected := range codecpdf.PaintChannels(uniformValues, compatible, marked) {
							if selected {
								uniformChannels |= 1 << j
							}
						}
					}
					uniformReady = true
				}
				values = uniformValues
			} else {
				point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*step, Y: c.box.Y + (float64(y)+.5)*step})
				if node.image != nil {
					var alpha float64
					values, alpha, err = c.imageColor(node.image, point, space)
					if nativePaint != nil {
						values = nativePaint.Values
					}
					if err == nil && sampler != nil {
						var visible bool
						values, visible, err = sampler.color(point)
						if !visible {
							alpha = 0
						}
					}
					if imageShape {
						selectionShape *= alpha
					}
					if c.shadingSource || imageShape {
						shape *= alpha
					} else {
						opacity *= alpha
					}
				} else {
					var visible bool
					if sampler != nil {
						values, visible, err = sampler.color(point)
					} else {
						values, visible, err = codecpdf.CompositeColor(paint, point, space, style.RenderingIntent, style.ColorConversion)
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
			if !originRecorded && shape != 0 {
				switch {
				case node.path != nil:
					c.origins.add(node.path.Sequence)
				case node.text != nil:
					origin := c.importer.textOrigin(*node.text)
					c.origins.add(origin.sequence)
					for _, sequence := range origin.related {
						c.origins.add(sequence)
					}
				case node.image != nil:
					c.origins.add(node.image.Sequence)
				}
				originRecorded = true
			}
			if initial != nil {
				ip := &initial[index]
				var pixel pdfCompositePixel
				if composedReady && ip.Alpha == previous.Alpha && ip.Effect == previous.Effect && ip.Shape == previous.Shape && ip.Values == previous.Values && values == previousValues && opacity == previousAlpha {
					pixel = composed
				} else {
					previous, previousValues, previousAlpha = *ip, values, opacity
					pixel = previous
					if err := codecpdf.CompositePaintOver(&pixel, values, opacity, space, mode, compatible, marked); err != nil {
						return err
					}
					composed, composedReady = pixel, true
				}
				if shape == 1 {
					tp := &pixels[index]
					tp.Values = pixel.Values
					tp.Alpha = pixel.Alpha
					tp.Effect = pixel.Effect
					tp.Shape = 1
				} else {
					if err := codecpdf.CompositeInterpolate(&pixels[index], pixel, shape); err != nil {
						return err
					}
					pixels[index].Shape += shape * (1 - pixels[index].Shape)
				}
			} else {
				alpha := shape * opacity
				tp := &pixels[index]
				if composedReady && tp.Alpha == previous.Alpha && tp.Effect == previous.Effect && tp.Shape == previous.Shape && tp.Values == previous.Values && values == previousValues && alpha == previousAlpha {
					*tp = composed
				} else {
					previous, previousValues, previousAlpha = *tp, values, alpha
					if err := codecpdf.CompositePaintOver(tp, values, alpha, space, mode, compatible, marked); err != nil {
						return err
					}
					composed, composedReady = *tp, true
				}
				tp.Shape += shape * (1 - tp.Shape)
			}
			bits := pdfTransferChannels
			if selectChannels {
				if uniformReady {
					bits = uniformChannels
				} else {
					bits = 0
					for j, selected := range codecpdf.PaintChannels(values, compatible, marked) {
						if selected {
							bits |= 1 << j
						}
					}
				}
				if patternChannels != nil {
					bits &= patternChannels[index]
				}
			}
			if c.channels != nil && shape != 0 && selectionShape != 0 {
				if initialChannels != nil && initial != nil {
					c.channels[index] = initialChannels[index]
				}
				c.channels[index] |= bits | pdfTransferShape
			}
			if c.transfers != nil && shape != 0 && selectionShape != 0 {
				selected := transfer
				if transfer != nil && c.transferModel == "DeviceCMYK" && bits != pdfTransferChannels {
					if !transferSelected[bits] {
						var channels [4]bool
						for j := range channels {
							channels[j] = bits&(1<<j) != 0
						}
						transferVariants[bits] = transfer.SelectChannels(channels)
						transferSelected[bits] = true
					}
					selected = transferVariants[bits]
				}
				c.transfers[index] = selected
			}
		}
	}
	return nil
}

// drawGroup 按隔离标志保留初始背景，移除背景贡献后应用组透明度
// 非隔离同空间的非挖空组借用父组背景，子组只写入独立结果缓冲
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
	region := c.pixelBounds(b)
	if region.Empty() {
		return nil
	}
	blocked := c.transferBlocked
	c.transferBlocked = blocked || g.Alpha != 1 || g.SoftMask != nil || !codecpdf.NormalBlend(g.BlendMode)
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
	sameSpace := space.Equal(parent)
	sharedInitial := !g.Isolated && !g.Knockout && sameSpace
	if sharedInitial && g.Alpha == 1 && g.SoftMask == nil && codecpdf.NormalBlend(g.BlendMode) {
		return c.draw(node.children, pixels, space)
	}
	subW, subH := region.Dx(), region.Dy()
	if subW < c.width || subH < c.height {
		step := 25.4 / c.importer.rasterDPI
		sub := *c
		sub.box = Box{X: c.box.X + float64(region.Min.X)*step, Y: c.box.Y + float64(region.Min.Y)*step, W: float64(subW) * step, H: float64(subH) * step}
		sub.width, sub.height = subW, subH
		sub.masks = map[*pdfgo.SoftMask][]float64{}
		sub.clipMasks = nil
		sub.gradients, sub.meshes = nil, nil
		subCount := subW * subH
		subPixels := c.acquirePixels(subCount)
		defer c.releasePixels(subPixels)
		if c.channels != nil {
			sub.channels = c.acquireChannels(subCount)
			defer c.releaseChannels(sub.channels)
		}
		if c.transfers != nil {
			sub.transfers = make([]*pdfgo.TransferFunction, subCount)
		}
		for y := range subH {
			srcOff := (region.Min.Y+y)*c.width + region.Min.X
			dstOff := y * subW
			copy(subPixels[dstOff:dstOff+subW], pixels[srcOff:srcOff+subW])
			if sub.channels != nil {
				copy(sub.channels[dstOff:dstOff+subW], c.channels[srcOff:srcOff+subW])
			}
			if sub.transfers != nil {
				copy(sub.transfers[dstOff:dstOff+subW], c.transfers[srcOff:srcOff+subW])
			}
		}
		err := sub.drawGroupRegion(node, subPixels, parent, space, sameSpace, sharedInitial, image.Rect(0, 0, subW, subH))
		sub.releaseMasks()
		if err != nil {
			return err
		}
		for y := range subH {
			dstOff := (region.Min.Y+y)*c.width + region.Min.X
			srcOff := y * subW
			copy(pixels[dstOff:dstOff+subW], subPixels[srcOff:srcOff+subW])
			if sub.channels != nil {
				copy(c.channels[dstOff:dstOff+subW], sub.channels[srcOff:srcOff+subW])
			}
			if sub.transfers != nil {
				copy(c.transfers[dstOff:dstOff+subW], sub.transfers[srcOff:srcOff+subW])
			}
		}
		return nil
	}
	return c.drawGroupRegion(node, pixels, parent, space, sameSpace, sharedInitial, region)
}

// drawGroupRegion 在已紧致对齐的像素区域内执行透明组隔离、挖空与混合合成
// 入参: node 透明组, pixels 父区域像素, parent 父混合空间, space 组混合空间, sameSpace 空间是否相同, sharedInitial 是否共享背景, region 目标区域
// 返回: error 颜色或合成错误
func (c *pdfCompositor) drawGroupRegion(node pdfCompositeNode, pixels []pdfCompositePixel, parent, space *pdfgo.ColorSpace, sameSpace, sharedInitial bool, region image.Rectangle) error {
	g := node.group
	initial := pixels
	if !sharedInitial {
		initial = c.acquirePixels(len(pixels))
		defer c.releasePixels(initial)
	}
	result := c.acquirePixels(len(initial))
	defer c.releasePixels(result)
	if g.Isolated {
		clear(initial)
	} else {
		background := initial
		if sharedInitial {
			background = result
		}
		components := parent.Components()
		for i := range pixels {
			if i%pdfCompositeTileSize == 0 {
				if err := c.importer.ctx.Err(); err != nil {
					return err
				}
			}
			pixel := &pixels[i]
			values := pixel.Values
			if !sameSpace {
				var err error
				values, err = space.Convert(values[:components], parent, "RelativeColorimetric")
				if err != nil {
					return err
				}
			}
			background[i] = pdfCompositePixel{Values: values, Alpha: pixel.Alpha}
		}
	}
	if !sharedInitial {
		copy(result, initial)
	}
	local := *c
	if c.channels != nil {
		local.channels = c.acquireChannels(len(c.channels))
		defer c.releaseChannels(local.channels)
		if g.Isolated {
			clear(local.channels)
		} else {
			copy(local.channels, c.channels)
		}
	}
	if g.Knockout {
		if err := local.drawKnockout(node.children, initial, result, space); err != nil {
			return err
		}
	} else {
		if err := local.draw(node.children, result, space); err != nil {
			return err
		}
	}
	c.gradients, c.meshes, c.clipMasks = local.gradients, local.meshes, local.clipMasks
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
			result[i].Shape *= g.Alpha
			if mask != nil {
				result[i].Shape *= mask[i]
			}
		}
	}
	if err := codecpdf.CompositeGroupRegion(c.importer.ctx, pixels, initial, result, c.width, region, space, parent, g.Alpha, mask, g.BlendMode, g.RenderingIntent, g.ColorConversion); err != nil {
		return err
	}
	if c.channels != nil {
		independent := g.Isolated || !space.Equal(parent)
		for y := region.Min.Y; y < region.Max.Y; y++ {
			if err := c.importer.ctx.Err(); err != nil {
				return err
			}
			for i, end := y*c.width+region.Min.X, y*c.width+region.Max.X; i < end; i++ {
				if result[i].Shape != 0 && local.channels[i]&pdfTransferShape != 0 {
					c.channels[i] = local.channels[i]
					if independent {
						c.channels[i] |= pdfTransferChannels
					}
				}
			}
		}
	}
	return nil
}

// drawKnockout 将每个对象与组初始背景合成，再按对象形状替换先前对象
// 入参: nodes 待绘制节点列表, initial 组初始背景像素, pixels 当前累积像素, space 组颜色空间
// 返回: error 绘制或取消错误
func (c *pdfCompositor) drawKnockout(nodes []pdfCompositeNode, initial, pixels []pdfCompositePixel, space *pdfgo.ColorSpace) error {
	if err := c.importer.ctx.Err(); err != nil {
		return err
	}
	var candidate []pdfCompositePixel
	var initialChannels []uint8
	var localChannels []uint8
	local := *c
	if c.channels != nil {
		initialChannels = c.acquireChannels(len(c.channels))
		defer c.releaseChannels(initialChannels)
		copy(initialChannels, c.channels)
	}
	ensureCandidate := func() {
		if candidate == nil {
			candidate = c.acquirePixels(len(initial))
			copy(candidate, initial)
			if initialChannels != nil {
				localChannels = c.acquireChannels(len(c.channels))
				copy(localChannels, initialChannels)
				local.channels = localChannels
			}
		}
	}
	defer func() {
		if candidate != nil {
			c.releasePixels(candidate)
		}
		if localChannels != nil {
			c.releaseChannels(localChannels)
		}
	}()
	for _, node := range nodes {
		if node.group == nil {
			style, fill, stroke := node.style()
			if fill != stroke {
				box, err := c.markBounds(node)
				if err != nil {
					return err
				}
				if c.pixelBounds(box).Empty() {
					continue
				}
				paint, overprint := style.Fill, style.FillOverprint
				if stroke {
					paint, overprint = style.Stroke, style.StrokeOverprint
				}
				if c.channels == nil || paint.Tiling == nil {
					mask, err := c.mask(style.SoftMask, space)
					if err != nil {
						return err
					}
					if err := c.drawPaint(node, stroke, paint, style.BlendMode, overprint, mask, initial, initialChannels, pixels, space); err != nil {
						return err
					}
					continue
				}
			}
		}
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
		ensureCandidate()
		if err := local.draw([]pdfCompositeNode{node}, candidate, space); err != nil {
			return err
		}
		isGroup := node.group != nil
		for y := region.Min.Y; y < region.Max.Y; y++ {
			if y&15 == 0 {
				if err := c.importer.ctx.Err(); err != nil {
					return err
				}
			}
			rowStart := y*c.width + region.Min.X
			rowEnd := y*c.width + region.Max.X
			if !isGroup {
				for i := rowStart; i < rowEnd; i++ {
					cp := &candidate[i]
					if cp.Shape == 0 {
						continue
					}
					src := *cp
					init := initial[i]
					*cp = init
					if err := codecpdf.CompositeKnockoutFast(&pixels[i], init, src); err != nil {
						return err
					}
					if initialChannels != nil {
						c.channels[i] = local.channels[i]
						local.channels[i] = initialChannels[i]
					}
				}
				continue
			}
			for i := rowStart; i < rowEnd; i++ {
				cp := &candidate[i]
				ip := &initial[i]
				if cp.Shape == 0 && cp.Alpha == ip.Alpha && cp.Effect == ip.Effect && cp.Values == ip.Values {
					if initialChannels != nil && local.channels[i] != initialChannels[i] {
						local.channels[i] = initialChannels[i]
					}
					continue
				}
				src := *cp
				init := *ip
				*cp = init
				if src.Shape != 0 {
					if err := codecpdf.CompositeKnockoutFast(&pixels[i], init, src); err != nil {
						return err
					}
					if initialChannels != nil {
						c.channels[i] = local.channels[i]
					}
				}
				if initialChannels != nil {
					local.channels[i] = initialChannels[i]
				}
			}
		}
	}
	if candidate != nil {
		c.gradients, c.meshes, c.clipMasks = local.gradients, local.meshes, local.clipMasks
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
		nodes, err = c.importer.collectCompositeNodes(func(v pdfgo.Visitor) error { return mask.WalkContext(c.importer.ctx, v) }, false)
		if err != nil {
			return nil, err
		}
		c.cache.masks[mask] = nodes
	}
	space := mask.ColorSpace
	if space == nil {
		space = inherited
	}
	count := c.width * c.height
	hasAlphaBBox, allInside, allOutside := false, false, false
	var maskBounds pdfgo.Rectangle
	var toForm pdfgo.Matrix
	if mask.Subtype == "Alpha" {
		if len(nodes) == 0 {
			allOutside = true
		} else if len(nodes) == 1 && nodes[0].group != nil && nodes[0].group.Matrix != (pdfgo.Matrix{}) {
			g := nodes[0].group
			if gInv, ok := g.Matrix.Inverse(); ok {
				hasAlphaBBox = true
				maskBounds = g.Bounds
				toForm = gInv.Mul(c.inverse)
				corners := [4]pdfgo.Point{
					toForm.Apply(pdfgo.Point{X: c.box.X, Y: c.box.Y}),
					toForm.Apply(pdfgo.Point{X: c.box.X + c.box.W, Y: c.box.Y}),
					toForm.Apply(pdfgo.Point{X: c.box.X + c.box.W, Y: c.box.Y + c.box.H}),
					toForm.Apply(pdfgo.Point{X: c.box.X, Y: c.box.Y + c.box.H}),
				}
				minX, minY := corners[0].X, corners[0].Y
				maxX, maxY := corners[0].X, corners[0].Y
				for _, pt := range corners[1:] {
					minX, minY = math.Min(minX, pt.X), math.Min(minY, pt.Y)
					maxX, maxY = math.Max(maxX, pt.X), math.Max(maxY, pt.Y)
				}
				if maxX < maskBounds.XMin || minX > maskBounds.XMax || maxY < maskBounds.YMin || minY > maskBounds.YMax {
					allOutside = true
				} else if minX >= maskBounds.XMin && maxX <= maskBounds.XMax && minY >= maskBounds.YMin && maxY <= maskBounds.YMax {
					allInside = true
				}
			}
		}
	}
	if allOutside {
		result := c.scratch.masks.Acquire(count)
		clear(result)
		c.masks[mask] = result
		return result, nil
	}
	pixels := c.acquirePixels(count)
	defer c.releasePixels(pixels)
	clear(pixels)
	if mask.Subtype == "Luminosity" {
		for i := range pixels {
			if i%pdfCompositeTileSize == 0 {
				if err := c.importer.ctx.Err(); err != nil {
					return nil, err
				}
			}
			copy(pixels[i].Values[:], mask.Backdrop)
			pixels[i].Alpha = 1
		}
	}
	local := *c
	local.transfers = nil
	local.channels = nil
	local.softMask = true
	if err := local.draw(nodes, pixels, space); err != nil {
		c.clipMasks = local.clipMasks
		return nil, err
	}
	c.clipMasks = local.clipMasks
	result := c.scratch.masks.Acquire(len(pixels))
	complete := false
	defer func() {
		if !complete {
			c.scratch.masks.Release(result)
		}
	}()
	values := make(map[[4]float64]float64)
	var previous [4]float64
	var opacity float64
	prevValid := false
	step := 25.4 / c.importer.rasterDPI
	checkOOB := hasAlphaBBox && !allInside
	for i, pixel := range pixels {
		if i%pdfCompositeTileSize == 0 {
			if err := c.importer.ctx.Err(); err != nil {
				return nil, err
			}
		}
		if checkOOB && pixel.Alpha == 0 {
			pt := toForm.Apply(pdfgo.Point{X: c.box.X + (float64(i%c.width)+.5)*step, Y: c.box.Y + (float64(i/c.width)+.5)*step})
			if pt.X < maskBounds.XMin || pt.X > maskBounds.XMax || pt.Y < maskBounds.YMin || pt.Y > maskBounds.YMax {
				result[i] = 0
				continue
			}
		}
		key := [4]float64{pixel.Alpha}
		if mask.Subtype == "Luminosity" {
			key = pixel.Values
		}
		if prevValid && key == previous {
			result[i] = opacity
			continue
		}
		if cached, ok := values[key]; ok {
			previous, opacity, prevValid, result[i] = key, cached, true, cached
			continue
		}
		v := pixel.Alpha
		if mask.Subtype == "Luminosity" {
			v, err = space.Luminosity(pixel.Values[:space.Components()], "RelativeColorimetric")
			if err != nil {
				return nil, err
			}
		}
		result[i], err = mask.Transfer(v)
		if err != nil {
			return nil, err
		}
		previous, opacity, prevValid = key, result[i], true
		if len(values) < 2*pdfCompositeTileSize {
			values[key] = opacity
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
	process, err := c.cache.imageProcess(c.importer, mark.Image, space, c.softMask)
	if err != nil {
		return [4]float64{}, 0, err
	}
	source := c.cache.images[mark.Image]
	if process == nil && source == nil {
		var err error
		source, err = mark.Image.DecodeComponentsViewContext(c.importer.ctx)
		if err != nil {
			return [4]float64{}, 0, err
		}
		c.cache.images[mark.Image] = source
	}
	bounds := process.Bounds()
	if process == nil {
		bounds = source.Rect
	}
	x, y := point.X*float64(bounds.Dx())-.5, (1-point.Y)*float64(bounds.Dy())-.5
	sample := func(x, y int) ([4]float64, float64) {
		x, y = bounds.Min.X+max(0, min(bounds.Dx()-1, x)), bounds.Min.Y+max(0, min(bounds.Dy()-1, y))
		if process != nil {
			return process.ValuesAt(x, y)
		}
		return source.ValuesAt(x, y)
	}
	var values [4]float64
	var alpha float64
	if mark.Image.Interpolate {
		ix, iy := int(math.Floor(x)), int(math.Floor(y))
		fx, fy := x-float64(ix), y-float64(iy)
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
	} else {
		values, alpha = sample(int(math.Floor(x+.5)), int(math.Floor(y+.5)))
	}
	if mark.Image.ImageMask {
		if mark.Style.Fill.Tiling != nil || mark.Style.Fill.Shading != nil {
			return [4]float64{}, alpha * (1 - values[0]), nil
		}
		color, visible, err := codecpdf.CompositeColor(mark.Style.Fill, mark.Matrix.Apply(point), space, mark.Style.RenderingIntent, mark.Style.ColorConversion)
		if !visible {
			return color, 0, err
		}
		return color, alpha * (1 - values[0]), err
	}
	if process != nil || space.Equal(source.Space) {
		return values, alpha, nil
	}
	color := &c.cache.imageColor
	if color.mark != mark || color.space != space || color.source != source.Space || color.intent != mark.Style.RenderingIntent || color.conversion != mark.Style.ColorConversion {
		converter, err := space.PrepareConversion(source.Space, mark.Style.RenderingIntent, mark.Style.ColorConversion)
		if err != nil {
			return [4]float64{}, alpha, err
		}
		*color = pdfCompositeImageColor{mark: mark, space: space, source: source.Space, intent: mark.Style.RenderingIntent, conversion: mark.Style.ColorConversion, converter: converter}
	}
	if color.ready && color.input == values {
		return color.output, alpha, nil
	}
	converted, err := color.converter.Convert(values[:source.Space.Components()])
	if err == nil {
		color.input, color.output, color.ready = values, converted, true
	}
	return converted, alpha, err
}

// constant 判断轴向延伸是否覆盖整个采样区间，不近似渐变内部颜色
// 入参: first 首个采样点, last 末个采样点
// 返回: bool 是否恒定
func (s *pdfCompositeColorSampler) constant(first, last pdfgo.Point) bool {
	g := s.paint.Axial
	if g == nil || g.Start.X != g.End.X && g.Start.Y != g.End.Y || g.Bounds != nil || g.Background != nil || s.paint.Function != nil || s.paint.Tiling != nil {
		return false
	}
	if len(g.Stops) != 0 && (g.Stops[0].Position < 0 || g.Stops[len(g.Stops)-1].Position > 1) {
		return false
	}
	position, err := g.PreparePosition()
	if err != nil {
		return false
	}
	a, visibleA := position.PositionAt(first)
	b, visibleB := position.PositionAt(last)
	return visibleA && visibleB && (a < 0 && b < 0 || a > 1 && b > 1)
}

// color 复用精确参数及端点延伸颜色，区域、背景及其他画刷保持原求值路径
// 入参: point 页面坐标
// 返回: [4]float64 混合分量, bool 是否着色, error 颜色错误
func (s *pdfCompositeColorSampler) color(point pdfgo.Point) ([4]float64, bool, error) {
	g := s.paint.Axial
	r := s.paint.Radial
	if g == nil && r == nil || g != nil && (g.Bounds != nil || g.Background != nil) || r != nil && (r.Bounds != nil || r.Background != nil) || s.paint.Function != nil || s.paint.Tiling != nil {
		return codecpdf.CompositeColor(s.paint, point, s.space, s.intent, s.conversion)
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
	key := position
	var colors []pdfgo.GradientStop
	if g != nil {
		colors = g.Stops
	} else {
		colors = r.Stops
	}
	if len(colors) == 0 || colors[0].Position >= 0 && colors[len(colors)-1].Position <= 1 {
		if position < 0 {
			key = math.Inf(-1)
		} else if position > 1 {
			key = math.Inf(1)
		}
	}
	if values, ok := s.values[key]; ok {
		return values, true, nil
	}
	var values [4]float64
	var source *pdfgo.ColorSpace
	var intent pdfgo.Name
	var err error
	if s.colorants != nil {
		values, err = s.colorants.At(position)
	} else if g != nil {
		values, err = g.ValuesAt(position)
		source, intent = g.Space, g.Intent
	} else {
		values, err = r.ValuesAt(position)
		source, intent = r.Space, r.Intent
	}
	if err == nil && s.colorants == nil {
		if !s.converted {
			s.converter, err = s.space.PrepareConversion(source, intent, s.conversion)
			s.converted = err == nil
		}
		if err == nil {
			values, err = s.converter.Convert(values[:source.Components()])
		}
	}
	if err == nil && (g != nil || key != position) && len(s.values) < 2*pdfCompositeTileSize {
		if s.values == nil {
			s.values = make(map[float64][4]float64)
		}
		s.values[key] = values
	}
	return values, visible, err
}
