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

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfCompositePattern 保存图案单元的原始图元和独立几何缓存
type pdfCompositePattern struct {
	nodes           []pdfCompositeNode
	local           pdfImporter
	box             Box
	transferChecked bool
	transferOpaque  bool
	denseFold       *pdfDensePatternFold
}

// pdfDensePatternFold 缓存高密度平铺图案的单周期折叠像素
type pdfDensePatternFold struct {
	space    *pdfgo.ColorSpace
	softMask bool
	hasChan  bool
	dpi      float64
	scaleX   float64
	scaleY   float64
	width    int
	height   int
	pixels   []pdfCompositePixel
	channels []uint8
}

// pdfCompositeOverPixel 对两个透明底合成像素执行标准源覆盖混合
// 入参: dst 目标像素, src 源像素, components 颜色分量数
func pdfCompositeOverPixel(dst *pdfCompositePixel, src pdfCompositePixel, components int) {
	if src.Shape == 0 && src.Alpha == 0 {
		return
	}
	if src.Alpha >= 1 {
		dst.Values, dst.Alpha = src.Values, 1
	} else if src.Alpha > 0 {
		alpha := src.Alpha + dst.Alpha*(1-src.Alpha)
		if alpha > 0 {
			for i := 0; i < components; i++ {
				dst.Values[i] = math.Max(0, math.Min(1, (src.Values[i]*src.Alpha+dst.Values[i]*dst.Alpha*(1-src.Alpha))/alpha))
			}
		}
		dst.Alpha = alpha
	}
	dst.Effect += src.Effect * (1 - dst.Effect)
	dst.Shape += src.Shape * (1 - dst.Shape)
}

// pdfShadingSourceKey 区分图案的使用几何及填充或描边
type pdfShadingSourceKey struct {
	pattern  *pdfgo.ShadingPattern
	geometry pdfCompositeKey
}

// pdfCompositePatternCell 合并互不重叠单元的覆盖贡献，避免相邻抗锯齿边缘重复混合
// 入参: target 图案累计结果, initial 原始背景, source 单元结果, disjoint 单元边界是否互不重叠
func pdfCompositePatternCell(target *pdfCompositePixel, initial, source pdfCompositePixel, disjoint bool) {
	if source.Shape == 0 {
		return
	}
	if disjoint {
		alpha := math.Max(0, math.Min(1, target.Alpha+source.Alpha-initial.Alpha))
		if alpha != 0 {
			for i := range target.Values {
				target.Values[i] = (target.Values[i]*target.Alpha + source.Values[i]*source.Alpha - initial.Values[i]*initial.Alpha) / alpha
			}
		}
		target.Alpha = alpha
		target.Effect = math.Min(1, target.Effect+source.Effect)
		target.Shape = math.Min(1, target.Shape+source.Shape)
	} else {
		target.Values, target.Alpha = source.Values, source.Alpha
		target.Effect += source.Effect * (1 - target.Effect)
		target.Shape += source.Shape * (1 - target.Shape)
	}
}

// opaqueTransfer 复用图案不透明性检查，图案内部函数不提前作用于源颜色
// 返回: bool 是否允许使用外层设备函数, error 单元解析错误
func (p *pdfCompositePattern) opaqueTransfer() (bool, error) {
	if !p.transferChecked {
		for _, child := range p.nodes {
			opaque, err := p.local.transferOpaque(child)
			if err != nil {
				return false, err
			}
			if !opaque {
				p.transferChecked = true
				return false, nil
			}
		}
		p.transferChecked, p.transferOpaque = true, true
	}
	return p.transferOpaque, nil
}

// tiling 将图案作为非隔离组绘制，保留背景贡献及单元间隙
// 入参: paint 图案画刷, backdrop 初始背景, space 混合空间, channels 可选源通道输出
// 返回: []pdfCompositePixel 去除背景贡献的图案像素, error 解析或绘制错误
func (c *pdfCompositor) tiling(paint pdfgo.Paint, backdrop []pdfCompositePixel, space *pdfgo.ColorSpace, channels []uint8) ([]pdfCompositePixel, error) {
	source := paint.Tiling
	matrix := c.importer.matrix.Mul(source.Matrix)
	inverse, ok := matrix.Inverse()
	if !ok {
		return nil, fmt.Errorf("invalid PDF tiling pattern matrix")
	}
	pattern, err := c.importer.compositePattern(paint)
	if err != nil {
		return nil, err
	}
	xstep, ystep := math.Abs(source.XStep), math.Abs(source.YStep)
	area := pdfBounds([]pdfgo.Point{
		inverse.Apply(pdfgo.Point{X: c.box.X, Y: c.box.Y}), inverse.Apply(pdfgo.Point{X: c.box.X + c.box.W, Y: c.box.Y}),
		inverse.Apply(pdfgo.Point{X: c.box.X + c.box.W, Y: c.box.Y + c.box.H}), inverse.Apply(pdfgo.Point{X: c.box.X, Y: c.box.Y + c.box.H}),
	})
	left, right := math.Ceil((area.X-source.BBox.XMax)/xstep), math.Floor((area.X+area.W-source.BBox.XMin)/xstep)
	top, bottom := math.Ceil((area.Y-source.BBox.YMax)/ystep), math.Floor((area.Y+area.H-source.BBox.YMin)/ystep)
	countX, countY := right-left+1, bottom-top+1
	if finite(countX) && finite(countY) && (countX <= 0 || countY <= 0) {
		result := c.acquirePixels(len(backdrop))
		clear(result)
		return result, nil
	}
	limit := float64(int(^uint(0) >> 1))
	dense := !finite(countX) || !finite(countY) || countX*countY > pdfPatternDenseFoldLimit
	for _, value := range []float64{left, right, top, bottom} {
		if !finite(value) || value <= -limit || value >= limit {
			dense = true
			break
		}
	}
	if dense {
		return c.tilingDense(source, pattern, matrix, inverse, backdrop, space, channels)
	}
	result := c.acquirePixels(len(backdrop))
	complete := false
	defer func() {
		if !complete {
			c.releasePixels(result)
		}
	}()
	for i, pixel := range backdrop {
		if i%pdfCompositeTileSize == 0 {
			if err := c.importer.ctx.Err(); err != nil {
				return nil, err
			}
		}
		result[i] = pdfCompositePixel{Values: pixel.Values, Alpha: pixel.Alpha}
	}
	candidate := c.acquirePixels(len(backdrop))
	defer c.releasePixels(candidate)
	var candidateChannels []uint8
	if channels != nil {
		candidateChannels = c.acquireChannels(len(backdrop))
		defer c.releaseChannels(candidateChannels)
	}
	disjoint := source.BBox.XMax-source.BBox.XMin <= xstep && source.BBox.YMax-source.BBox.YMin <= ystep
	pixelStep := 25.4 / c.importer.rasterDPI
	localInverse, _ := pattern.local.matrix.Inverse()
	rowStart, rowEnd, rowStep := int(top), int(bottom), 1
	if source.YStep < 0 {
		rowStart, rowEnd, rowStep = int(bottom), int(top), -1
	}
	colStart, colEnd, colStep := int(left), int(right), 1
	if source.XStep < 0 {
		colStart, colEnd, colStep = int(right), int(left), -1
	}
	for row := rowStart; row != rowEnd+rowStep; row += rowStep {
		for column := colStart; column != colEnd+colStep; column += colStep {
			if err := c.importer.ctx.Err(); err != nil {
				return nil, err
			}
			x, y := float64(column)*xstep, float64(row)*ystep
			dx, dy := matrix[0]*x+matrix[2]*y+pattern.box.X, matrix[1]*x+matrix[3]*y+pattern.box.Y
			left := int(math.Max(0, math.Min(float64(c.width), math.Floor((dx-c.box.X)/pixelStep)-1)))
			right := int(math.Max(0, math.Min(float64(c.width), math.Ceil((dx+pattern.box.W-c.box.X)/pixelStep)+1)))
			top := int(math.Max(0, math.Min(float64(c.height), math.Floor((dy-c.box.Y)/pixelStep)-1)))
			bottom := int(math.Max(0, math.Min(float64(c.height), math.Ceil((dy+pattern.box.H-c.box.Y)/pixelStep)+1)))
			width, height := right-left, bottom-top
			if width <= 0 || height <= 0 {
				continue
			}
			pixels := candidate[:width*height]
			var cellChannels []uint8
			if channels != nil {
				cellChannels = candidateChannels[:width*height]
			}
			for y := range height {
				if err := c.importer.ctx.Err(); err != nil {
					return nil, err
				}
				for x := range width {
					index := (top+y)*c.width + left + x
					initial := result[index]
					if disjoint {
						initial = backdrop[index]
					}
					pixels[y*width+x] = pdfCompositePixel{Values: initial.Values, Alpha: initial.Alpha}
					if cellChannels != nil {
						cellChannels[y*width+x] = 0
						if !disjoint {
							cellChannels[y*width+x] = channels[index]
						}
					}
				}
			}
			cell := pdfCompositor{importer: &pattern.local, box: Box{X: c.box.X + float64(left)*pixelStep - dx, Y: c.box.Y + float64(top)*pixelStep - dy, W: float64(width) * pixelStep, H: float64(height) * pixelStep},
				width: width, height: height, inverse: localInverse, cache: pattern.local.compositingCache(), scratch: c.scratch, origins: c.origins, masks: map[*pdfgo.SoftMask][]float64{}, softMask: c.softMask, channels: cellChannels}
			err := cell.draw(pattern.nodes, pixels, space)
			cell.releaseMasks()
			if err != nil {
				return nil, err
			}
			for y := range height {
				if err := c.importer.ctx.Err(); err != nil {
					return nil, err
				}
				for x := range width {
					index := (top+y)*c.width + left + x
					pdfCompositePatternCell(&result[index], backdrop[index], pixels[y*width+x], disjoint)
					if channels != nil && pixels[y*width+x].Shape != 0 {
						if disjoint {
							channels[index] |= cellChannels[y*width+x]
						} else {
							channels[index] = cellChannels[y*width+x]
						}
					}
				}
			}
		}
	}
	for i := range result {
		if i%pdfCompositeTileSize == 0 {
			if err := c.importer.ctx.Err(); err != nil {
				return nil, err
			}
		}
		pixel := &result[i]
		if pixel.Effect != 0 {
			for j := 0; j < space.Components(); j++ {
				pixel.Values[j] = math.Max(0, math.Min(1, (pixel.Values[j]*pixel.Alpha-backdrop[i].Values[j]*backdrop[i].Alpha*(1-pixel.Effect))/pixel.Effect))
			}
		}
		pixel.Alpha = pixel.Effect
	}
	complete = true
	return result, nil
}

// tilingNodesNeedBackdrop 检查高密度平铺单元内是否存在依赖外部背景的非Normal混合
// 入参: nodes 平铺合成节点列表
// 返回: bool 是否依赖外部背景
func tilingNodesNeedBackdrop(nodes []pdfCompositeNode) bool {
	for _, node := range nodes {
		if g := node.group; g != nil {
			if !codecpdf.NormalBlend(g.BlendMode) {
				return true
			}
			if !g.Isolated && tilingNodesNeedBackdrop(node.children) {
				return true
			}
			continue
		}
		style, _, _ := node.style()
		if !codecpdf.NormalBlend(style.BlendMode) {
			return true
		}
	}
	return false
}

// tilingDense 对超密集或亚像素步距平铺图案执行单周期折叠并在当前块查表填充
// 入参: source 原图案, pattern 预解析单元, matrix 图案到页面矩阵, inverse 页面到图案逆矩阵, backdrop 外部背景像素, space 混合空间, channels 可选源通道输出
// 返回: []pdfCompositePixel 源颜色及覆盖, error 绘制或取消错误
func (c *pdfCompositor) tilingDense(source *pdfgo.TilingPattern, pattern *pdfCompositePattern, matrix, inverse pdfgo.Matrix, backdrop []pdfCompositePixel, space *pdfgo.ColorSpace, channels []uint8) ([]pdfCompositePixel, error) {
	count := len(backdrop)
	b := source.BBox
	bw, bh := b.XMax-b.XMin, b.YMax-b.YMin
	xstep, ystep := math.Abs(source.XStep), math.Abs(source.YStep)
	if bw <= 0 || bh <= 0 || xstep <= 0 || ystep <= 0 {
		result := c.acquirePixels(count)
		clear(result)
		return result, nil
	}
	pixelStep := 25.4 / c.importer.rasterDPI
	const maxDim = 512.0
	spanW, spanH := math.Max(bw, xstep), math.Max(bh, ystep)
	sx := math.Hypot(matrix[0], matrix[1]) / pixelStep
	sy := math.Hypot(matrix[2], matrix[3]) / pixelStep
	if !finite(sx) || sx <= 0 {
		sx = 1
	}
	if !finite(sy) || sy <= 0 {
		sy = 1
	}
	if spanW*sx > maxDim {
		sx = maxDim / spanW
	}
	if spanH*sy > maxDim {
		sy = maxDim / spanH
	}
	components := space.Components()
	buildFold := func(initBg *pdfCompositePixel) (*pdfDensePatternFold, error) {
		srcW := max(1, int(math.Ceil(spanW*sx)))
		srcH := max(1, int(math.Ceil(spanH*sy)))
		cellW, cellH := float64(srcW)*pixelStep, float64(srcH)*pixelStep
		local := pattern.local
		local.matrix = pdfgo.Matrix{cellW / spanW, 0, 0, -cellH / spanH, -b.XMin * cellW / spanW, b.YMax * cellH / spanH}
		local.pageWidth, local.pageHeight = cellW, cellH
		local.compositeCache = nil
		localInverse, ok := local.matrix.Inverse()
		if !ok {
			return nil, fmt.Errorf("invalid PDF dense pattern matrix")
		}
		srcPixels := make([]pdfCompositePixel, srcW*srcH)
		if initBg != nil {
			bg := pdfCompositePixel{Values: initBg.Values, Alpha: initBg.Alpha}
			for i := range srcPixels {
				srcPixels[i] = bg
			}
		}
		var srcChannels []uint8
		if channels != nil {
			srcChannels = make([]uint8, srcW*srcH)
		}
		cell := pdfCompositor{importer: &local, box: Box{W: cellW, H: cellH}, width: srcW, height: srcH, inverse: localInverse, cache: local.compositingCache(), scratch: &pdfCompositeScratch{}, origins: c.origins, masks: map[*pdfgo.SoftMask][]float64{}, softMask: c.softMask, channels: srcChannels}
		err := cell.draw(pattern.nodes, srcPixels, space)
		cell.releaseMasks()
		if err != nil {
			return nil, err
		}
		if initBg != nil {
			for i := range srcPixels {
				pixel := &srcPixels[i]
				if pixel.Effect != 0 {
					for j := range components {
						pixel.Values[j] = math.Max(0, math.Min(1, (pixel.Values[j]*pixel.Alpha-initBg.Values[j]*initBg.Alpha*(1-pixel.Effect))/pixel.Effect))
					}
				}
				pixel.Alpha = pixel.Effect
			}
		} else {
			for i := range srcPixels {
				srcPixels[i].Alpha = srcPixels[i].Effect
			}
		}
		dx := max(1, min(srcW, int(math.Round(xstep*float64(srcW)/spanW))))
		dy := max(1, min(srcH, int(math.Round(ystep*float64(srcH)/spanH))))
		band := srcPixels
		bandChannels := srcChannels
		if bh > ystep && dy < srcH {
			band = make([]pdfCompositePixel, srcW*dy)
			if srcChannels != nil {
				bandChannels = make([]uint8, srcW*dy)
			}
			rowCount := (srcH + dy - 1) / dy
			start, end, step := 0, rowCount-1, 1
			if source.YStep < 0 {
				start, end, step = rowCount-1, 0, -1
			}
			for k := start; k != end+step; k += step {
				if err := c.importer.ctx.Err(); err != nil {
					return nil, err
				}
				y0 := k * dy
				for y := 0; y < dy && y0+y < srcH; y++ {
					for x := range srcW {
						si, di := (y0+y)*srcW+x, y*srcW+x
						pdfCompositeOverPixel(&band[di], srcPixels[si], components)
						if bandChannels != nil && srcPixels[si].Shape != 0 {
							bandChannels[di] = srcChannels[si]
						}
					}
				}
			}
		}
		folded := band
		foldedChannels := bandChannels
		if bw > xstep && dx < srcW {
			folded = make([]pdfCompositePixel, dx*dy)
			if bandChannels != nil {
				foldedChannels = make([]uint8, dx*dy)
			}
			colCount := (srcW + dx - 1) / dx
			start, end, step := colCount-1, 0, -1
			if source.XStep < 0 {
				start, end, step = 0, colCount-1, 1
			}
			for k := start; k != end+step; k += step {
				if err := c.importer.ctx.Err(); err != nil {
					return nil, err
				}
				x0 := k * dx
				for y := range dy {
					for x := 0; x < dx && x0+x < srcW; x++ {
						si, di := y*srcW+x0+x, y*dx+x
						pdfCompositeOverPixel(&folded[di], band[si], components)
						if foldedChannels != nil && band[si].Shape != 0 {
							foldedChannels[di] = bandChannels[si]
						}
					}
				}
			}
		} else if srcW != dx || len(folded) != dx*dy {
			cropped := make([]pdfCompositePixel, dx*dy)
			var croppedChannels []uint8
			if foldedChannels != nil {
				croppedChannels = make([]uint8, dx*dy)
			}
			for y := range dy {
				for x := range dx {
					cropped[y*dx+x] = folded[y*srcW+x]
					if croppedChannels != nil {
						croppedChannels[y*dx+x] = foldedChannels[y*srcW+x]
					}
				}
			}
			folded, foldedChannels = cropped, croppedChannels
		}
		return &pdfDensePatternFold{space: space, softMask: c.softMask, hasChan: channels != nil, dpi: c.importer.rasterDPI, scaleX: sx, scaleY: sy, width: dx, height: dy, pixels: folded, channels: foldedChannels}, nil
	}
	if tilingNodesNeedBackdrop(pattern.nodes) {
		type bgExactKey struct {
			values [4]float64
			alpha  float64
		}
		exactFolds := make(map[bgExactKey]*pdfDensePatternFold)
		quantized := false
		for i := range count {
			p := backdrop[i]
			k := bgExactKey{values: p.Values, alpha: p.Alpha}
			if _, ok := exactFolds[k]; !ok {
				if len(exactFolds) >= 32 {
					quantized = true
					break
				}
				exactFolds[k] = nil
			}
		}
		type bgQuantKey struct {
			c [5]uint8
		}
		quantKey := func(p pdfCompositePixel) (bgQuantKey, pdfCompositePixel) {
			var k bgQuantKey
			var rep pdfCompositePixel
			for j := range components {
				q := uint8(math.Round(math.Max(0, math.Min(1, p.Values[j])) * 31))
				k.c[j] = q
				rep.Values[j] = float64(q) / 31
			}
			qa := uint8(math.Round(math.Max(0, math.Min(1, p.Alpha)) * 31))
			k.c[4] = qa
			rep.Alpha = float64(qa) / 31
			return k, rep
		}
		var quantFolds map[bgQuantKey]*pdfDensePatternFold
		if quantized {
			quantFolds = make(map[bgQuantKey]*pdfDensePatternFold)
		}
		result := c.acquirePixels(count)
		for y := range c.height {
			if err := c.importer.ctx.Err(); err != nil {
				c.releasePixels(result)
				return nil, err
			}
			py := c.box.Y + (float64(y)+.5)*pixelStep
			for x := range c.width {
				index := y*c.width + x
				bg := backdrop[index]
				var fold *pdfDensePatternFold
				var err error
				if !quantized {
					k := bgExactKey{values: bg.Values, alpha: bg.Alpha}
					fold = exactFolds[k]
					if fold == nil {
						fold, err = buildFold(&bg)
						if err != nil {
							c.releasePixels(result)
							return nil, err
						}
						exactFolds[k] = fold
					}
				} else {
					qk, rep := quantKey(bg)
					fold = quantFolds[qk]
					if fold == nil {
						fold, err = buildFold(&rep)
						if err != nil {
							c.releasePixels(result)
							return nil, err
						}
						quantFolds[qk] = fold
					}
				}
				pt := inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*pixelStep, Y: py})
				fx := math.Mod(pt.X-b.XMin, xstep)
				if fx < 0 {
					fx += xstep
				}
				fy := math.Mod(b.YMax-pt.Y, ystep)
				if fy < 0 {
					fy += ystep
				}
				tx := max(0, min(fold.width-1, int(fx/xstep*float64(fold.width))))
				ty := max(0, min(fold.height-1, int(fy/ystep*float64(fold.height))))
				fi := ty*fold.width + tx
				result[index] = fold.pixels[fi]
				if channels != nil && fold.pixels[fi].Shape != 0 {
					channels[index] = fold.channels[fi]
				}
			}
		}
		return result, nil
	}
	fold := pattern.denseFold
	if fold == nil || !fold.space.Equal(space) || fold.softMask != c.softMask || fold.dpi != c.importer.rasterDPI || fold.scaleX != sx || fold.scaleY != sy || channels != nil && !fold.hasChan {
		var err error
		fold, err = buildFold(nil)
		if err != nil {
			return nil, err
		}
		pattern.denseFold = fold
	}
	result := c.acquirePixels(count)
	for y := range c.height {
		if err := c.importer.ctx.Err(); err != nil {
			c.releasePixels(result)
			return nil, err
		}
		py := c.box.Y + (float64(y)+.5)*pixelStep
		for x := range c.width {
			pt := inverse.Apply(pdfgo.Point{X: c.box.X + (float64(x)+.5)*pixelStep, Y: py})
			fx := math.Mod(pt.X-b.XMin, xstep)
			if fx < 0 {
				fx += xstep
			}
			fy := math.Mod(b.YMax-pt.Y, ystep)
			if fy < 0 {
				fy += ystep
			}
			tx := max(0, min(fold.width-1, int(fx/xstep*float64(fold.width))))
			ty := max(0, min(fold.height-1, int(fy/ystep*float64(fold.height))))
			index := y*c.width + x
			fi := ty*fold.width + tx
			result[index] = fold.pixels[fi]
			if channels != nil && fold.pixels[fi].Shape != 0 {
				channels[index] = fold.channels[fi]
			}
		}
	}
	return result, nil
}

// compositePattern 复用图案单元解析及几何上下文，供检查和局部合成共同使用
// 入参: paint 图案画刷
// 返回: *pdfCompositePattern 单元内容, error 解析或边界错误
func (p *pdfImporter) compositePattern(paint pdfgo.Paint) (*pdfCompositePattern, error) {
	paint.Alpha = 1
	cache := p.compositingCache()
	if cache.patterns == nil {
		cache.patterns = make(map[pdfgo.Paint]*pdfCompositePattern)
	}
	if pattern := cache.patterns[paint]; pattern != nil {
		return pattern, nil
	}
	source := paint.Tiling
	nodes, err := p.collectCompositeNodes(func(v pdfgo.Visitor) error { return source.Walk(p.ctx, paint, v) }, false)
	if err != nil {
		return nil, err
	}
	matrix := p.matrix.Mul(source.Matrix)
	b := source.BBox
	box := pdfBounds([]pdfgo.Point{
		matrix.Apply(pdfgo.Point{X: b.XMin, Y: b.YMin}), matrix.Apply(pdfgo.Point{X: b.XMax, Y: b.YMin}),
		matrix.Apply(pdfgo.Point{X: b.XMax, Y: b.YMax}), matrix.Apply(pdfgo.Point{X: b.XMin, Y: b.YMax}),
	})
	if box.W <= 0 || box.H <= 0 {
		return nil, fmt.Errorf("invalid PDF tiling pattern bounds")
	}
	local := *p
	local.matrix = matrix
	local.matrix[4] -= box.X
	local.matrix[5] -= box.Y
	local.pageWidth, local.pageHeight = box.W, box.H
	local.compositeCache = nil
	local.compositingCache().geometry = cache.geometryCache()
	local.compositingCache().clips = cache.clipCache()
	pattern := &pdfCompositePattern{nodes: nodes, local: local, box: box}
	cache.patterns[paint] = pattern
	return pattern, nil
}

// directCompositeNode 检查图元、透明组和图案单元能否独立保留，逐层核对背景混合
// 入参: node 图元, space 混合空间
// 返回: bool 是否可直接转换, error 图案或遮罩解析错误
func (p *pdfImporter) directCompositeNode(node pdfCompositeNode, space *pdfgo.ColorSpace) (bool, error) {
	if node.group == nil && space.Device() {
		style, fill, stroke := node.style()
		if node.image == nil || node.image.Image.ImageMask {
			for i, paint := range [2]pdfgo.Paint{style.Fill, style.Stroke} {
				if i == 0 && !fill || i == 1 && !stroke {
					continue
				}
				if native := codecpdf.PrepareShadingColorants(paint, space, false); native != nil {
					if !native.Process.AlternateEquivalent() || i == 0 && style.FillOverprint || i == 1 && style.StrokeOverprint {
						return false, nil
					}
				}
			}
		}
		if space.Model == "DeviceCMYK" && style.ColorConversion != (pdfgo.ColorConversion{}) {
			if node.image != nil && !node.image.Image.ImageMask {
				_, native, err := codecpdf.ImageProcessColorants(node.image.Image)
				if err != nil {
					return false, err
				}
				if !native {
					source := p.compositingCache().images[node.image.Image]
					if source == nil {
						source, err = node.image.Image.DecodeComponentsViewContext(p.ctx)
						if err != nil {
							return false, err
						}
						p.compositingCache().images[node.image.Image] = source
					}
					if source.Space.Model == "DeviceRGB" && source.Space.Device() {
						return false, nil
					}
				}
			} else {
				for i, paint := range [2]pdfgo.Paint{style.Fill, style.Stroke} {
					if i == 0 && !fill || i == 1 && !stroke || paint.None || paint.Tiling != nil {
						continue
					}
					source := paint.Space
					switch {
					case paint.Axial != nil:
						source = paint.Axial.Space
					case paint.Radial != nil:
						source = paint.Radial.Space
					case paint.Function != nil:
						source = paint.Function.Space
					case paint.Mesh != nil:
						source = paint.Mesh.Space
					}
					if source != nil && source.Model == "DeviceRGB" && source.Device() || source == nil && paint.CMYK == nil && paint.SourceSpace != "DeviceGray" {
						return false, nil
					}
				}
			}
		}
	}
	directGroup := false
	if group := node.group; group != nil && !group.Knockout && group.Alpha == 1 && group.SoftMask == nil && codecpdf.NormalBlend(group.BlendMode) && (group.ColorSpace == nil || group.ColorSpace.SRGBEquivalent()) {
		directGroup = true
	}
	opaque, err := p.opaque(node, space)
	if err != nil {
		return false, err
	}
	if !opaque && !(space.SRGBEquivalent() && (directGroup || node.direct())) {
		if node.textObject != nil && space.SRGBEquivalent() {
			for _, child := range node.children {
				if direct, err := p.directCompositeNode(child, space); err != nil || !direct {
					return direct, err
				}
			}
			return p.disjointText(node)
		}
		return false, nil
	}
	if node.group != nil {
		for _, child := range node.children {
			if direct, err := p.directCompositeNode(child, space); err != nil || !direct {
				return direct, err
			}
		}
		return true, nil
	}
	if node.image != nil && !node.image.Image.ImageMask {
		return true, nil
	}
	style, fill, stroke := node.style()
	for i, paint := range []pdfgo.Paint{style.Fill, style.Stroke} {
		if i == 0 && !fill || i == 1 && !stroke || paint.Tiling == nil {
			continue
		}
		pattern, err := p.compositePattern(paint)
		if err != nil {
			return false, err
		}
		for _, child := range pattern.nodes {
			if direct, err := pattern.local.directCompositeNode(child, space); err != nil || !direct {
				return direct, err
			}
		}
	}
	return true, nil
}

// processOverprint 检查图元、透明组和图案单元是否需要设备四色套印
// 入参: nodes 原始图元
// 返回: bool 是否需要四色混合, error 图像或图案解析错误
func (p *pdfImporter) processOverprint(nodes []pdfCompositeNode) (bool, error) {
	for _, node := range nodes {
		if node.group != nil {
			if found, err := p.processOverprint(node.children); err != nil || found {
				return found, err
			}
			continue
		}
		style, fill, stroke := node.style()
		if node.image != nil && style.FillOverprint {
			if _, found, err := codecpdf.ImageProcessColorants(node.image.Image); err != nil || found {
				return found, err
			}
		}
		if node.image != nil && !node.image.Image.ImageMask {
			continue
		}
		if fill && style.FillOverprint && codecpdf.OverprintNeedsSeparation(style.Fill) || stroke && style.StrokeOverprint && codecpdf.OverprintNeedsSeparation(style.Stroke) {
			return true, nil
		}
		if style.Transfer != nil || style.Halftone != nil {
			if fill && style.FillOverprint && style.Fill.CMYK != nil || stroke && style.StrokeOverprint && style.Stroke.CMYK != nil {
				transfer, err := p.resolveTransfer(style, "DeviceCMYK")
				if err != nil || transfer != nil {
					return transfer != nil, err
				}
			}
		}
		for i, paint := range []pdfgo.Paint{style.Fill, style.Stroke} {
			if i == 0 && fill && style.FillOverprint || i == 1 && stroke && style.StrokeOverprint {
				if native := codecpdf.PrepareShadingColorants(paint, &pdfgo.ColorSpace{Model: "DeviceCMYK"}, false); native != nil {
					return true, nil
				}
				native, _, err := codecpdf.NativePaint(paint, &pdfgo.ColorSpace{Model: "DeviceCMYK"}, style.RenderingIntent)
				if err != nil {
					return false, err
				}
				if native != nil && (!codecpdf.OpaqueBlack(*native) || !codecpdf.OpaqueBlack(paint)) {
					return true, nil
				}
			}
			if paint.Shading != nil && (i == 0 && fill || i == 1 && stroke) {
				nodes, err := p.compositeShadingPattern(paint.Shading)
				if err != nil {
					return false, err
				}
				if found, err := p.processOverprint(nodes); err != nil || found {
					return found, err
				}
			}
			if i == 0 && !fill || i == 1 && !stroke || paint.Tiling == nil {
				continue
			}
			pattern, err := p.compositePattern(paint)
			if err != nil {
				return false, err
			}
			if found, err := pattern.local.processOverprint(pattern.nodes); err != nil || found {
				return found, err
			}
		}
	}
	return false, nil
}

// resolveTransfer 无设备函数时保留无缓存快速路径，按需绑定单页解析缓存
// 入参: style 原始图形状态, model 原生设备颜色空间
// 返回: *pdfgo.TransferFunction 有效函数, error 网屏传递函数错误
func (p *pdfImporter) resolveTransfer(style pdfgo.Style, model pdfgo.Name) (*pdfgo.TransferFunction, error) {
	if style.Halftone == nil && style.Transfer == nil {
		return nil, nil
	}
	return p.compositingCache().transfers.Resolve(p.reader, style, model)
}

// compositeHasTransfer 检查最终设备使用的通道，不将备用通道误判为有效函数
// 入参: nodes 原始图元及透明组, model 原生设备颜色空间
// 返回: bool 是否存在非恒等函数, error 网屏函数错误
func (p *pdfImporter) compositeHasTransfer(nodes []pdfCompositeNode, model pdfgo.Name) (bool, error) {
	for _, node := range nodes {
		if node.group != nil {
			if found, err := p.compositeHasTransfer(node.children, model); err != nil || found {
				return found, err
			}
		} else {
			style, _, _ := node.style()
			transfer, err := p.resolveTransfer(style, model)
			if err != nil || transfer != nil {
				return transfer != nil, err
			}
		}
	}
	return false, nil
}

// transferDevice 为四色叠印保留原生设备通道，其余页面使用连续RGB设备
// 入参: space 页面混合空间, nodes 参与合成的图元
// 返回: pdfgo.Name 原生设备空间, error 图案或图像解析错误
func (p *pdfImporter) transferDevice(space *pdfgo.ColorSpace, nodes []pdfCompositeNode) (pdfgo.Name, error) {
	if space.Model == "DeviceCMYK" && !space.Calibrated() {
		if found, err := p.processOverprint(nodes); err != nil {
			return "", err
		} else if found {
			return "DeviceCMYK", nil
		}
	}
	return "DeviceRGB", nil
}

// selectTransfer 按最上层基本图元及祖先的不透明状态选择最终设备函数
// 入参: node 基本图元, outline 是否描边
// 返回: *pdfgo.TransferFunction 当前函数或设备默认值, error 图案解析错误
func (c *pdfCompositor) selectTransfer(node pdfCompositeNode, outline bool) (*pdfgo.TransferFunction, error) {
	style, _, _ := node.style()
	paint := style.Fill
	if outline {
		paint = style.Stroke
	}
	if c.transferBlocked || paint.Alpha != 1 || style.SoftMask != nil || !codecpdf.NormalBlend(style.BlendMode) {
		return nil, nil
	}
	transfer, err := c.importer.resolveTransfer(style, c.transferModel)
	if err != nil || transfer == nil {
		return transfer, err
	}
	if pattern := paint.Shading; pattern != nil && (node.image == nil || node.image.Image.ImageMask) {
		inner := pattern.Style
		if inner.Fill.Alpha != 1 || inner.SoftMask != nil || !codecpdf.NormalBlend(inner.BlendMode) {
			return nil, nil
		}
	}
	if node.image != nil {
		soft, err := node.image.Image.HasSoftMask()
		if err != nil || soft {
			return nil, err
		}
	}
	if paint.Tiling != nil && (node.image == nil || node.image.Image.ImageMask) {
		pattern, err := c.importer.compositePattern(paint)
		if err != nil {
			return nil, err
		}
		opaque, err := pattern.opaqueTransfer()
		if err != nil || !opaque {
			return nil, err
		}
	}
	return transfer, nil
}

// transferOpaque 检查全部图案内容的不透明条件，不限制混合颜色空间
// 入参: node 单元图元或透明组
// 返回: bool 是否不透明, error 图案或图片遮罩引用错误
func (p *pdfImporter) transferOpaque(node pdfCompositeNode) (bool, error) {
	if group := node.group; group != nil {
		if group.Alpha != 1 || group.SoftMask != nil || !codecpdf.NormalBlend(group.BlendMode) {
			return false, nil
		}
		for _, child := range node.children {
			if opaque, err := p.transferOpaque(child); err != nil || !opaque {
				return opaque, err
			}
		}
		return true, nil
	}
	style, fill, stroke := node.style()
	if style.SoftMask != nil || !codecpdf.NormalBlend(style.BlendMode) || fill && style.Fill.Alpha != 1 || stroke && style.Stroke.Alpha != 1 {
		return false, nil
	}
	if node.image != nil {
		soft, err := node.image.Image.HasSoftMask()
		if err != nil || soft {
			return false, err
		}
		if !node.image.Image.ImageMask {
			return true, nil
		}
	}
	for i, paint := range [2]pdfgo.Paint{style.Fill, style.Stroke} {
		if pattern := paint.Shading; pattern != nil && (i == 0 && fill || i == 1 && stroke) {
			inner := pattern.Style
			if inner.Fill.Alpha != 1 || inner.SoftMask != nil || !codecpdf.NormalBlend(inner.BlendMode) {
				return false, nil
			}
		}
		if i == 0 && !fill || i == 1 && !stroke || paint.Tiling == nil {
			continue
		}
		pattern, err := p.compositePattern(paint)
		if err != nil {
			return false, err
		}
		if opaque, err := pattern.opaqueTransfer(); err != nil || !opaque {
			return opaque, err
		}
	}
	return true, nil
}

// halftone 检查连续色调转换，避免将打印网屏误用于屏幕显示
// 入参: style 图形状态
// 返回: error 严格模式无法保留设备网屏
func (p *pdfImporter) halftone(style pdfgo.Style) error {
	h := style.Halftone
	message := "PDF continuous-tone appearance retained; device halftone not transferred to OFD"
	if h == nil || h.Type == 0 {
		if style.HalftoneOrigin == nil {
			return nil
		}
		message = "PDF continuous-tone appearance retained; device halftone origin not transferred to OFD"
	}
	if p.warning == nil {
		return &pdfgo.UnsupportedError{Feature: "device halftone conversion"}
	}
	if p.halftoneWarnings == nil {
		p.halftoneWarnings = make(map[*pdfgo.Halftone]bool)
	}
	if !p.halftoneWarnings[h] {
		p.halftoneWarnings[h] = true
		if p.halftoneWarningMessages == nil {
			p.halftoneWarningMessages = make(map[string]bool)
		}
		p.halftoneWarningMessages[message] = true
		p.warning(pdfgo.Diagnostic{Message: message})
	}
	return nil
}

// compositeShadingPattern 在页面内复用图案内部图元，不缓存依赖背景的像素
// 入参: pattern 只读着色图案
// 返回: []pdfCompositeNode 隐式挖空组, error 遍历或资源错误
func (p *pdfImporter) compositeShadingPattern(pattern *pdfgo.ShadingPattern) ([]pdfCompositeNode, error) {
	cache := p.compositingCache()
	if nodes, found := cache.shadingPatterns[pattern]; found {
		return nodes, nil
	}
	inverse, ok := p.matrix.Inverse()
	if !ok {
		return nil, &pdfgo.UnsupportedError{Feature: "shading pattern page transform"}
	}
	box := pdfBounds([]pdfgo.Point{
		inverse.Apply(pdfgo.Point{}), inverse.Apply(pdfgo.Point{X: p.pageWidth}),
		inverse.Apply(pdfgo.Point{X: p.pageWidth, Y: p.pageHeight}), inverse.Apply(pdfgo.Point{Y: p.pageHeight}),
	})
	nodes, err := p.collectCompositeNodes(func(visitor pdfgo.Visitor) error {
		return pattern.Walk(p.ctx, pdfgo.Rectangle{XMin: box.X, YMin: box.Y, XMax: box.X + box.W, YMax: box.Y + box.H}, visitor)
	}, false)
	if err != nil {
		return nil, err
	}
	if cache.shadingPatterns == nil {
		cache.shadingPatterns = make(map[*pdfgo.ShadingPattern][]pdfCompositeNode)
	}
	cache.shadingPatterns[pattern] = nodes
	return nodes, nil
}

// shadingPattern 统一对象和图案的几何覆盖，按相交面积合成背景及内部着色
// 入参: pattern 着色图案, node 使用图元, outline 是否描边, backdrop 初始背景, space 父级混合空间, channels 可选源通道输出
// 返回: []pdfCompositePixel 源颜色、形状及不透明度, error 状态或合成错误
func (c *pdfCompositor) shadingPattern(pattern *pdfgo.ShadingPattern, node pdfCompositeNode, outline bool, backdrop []pdfCompositePixel, space *pdfgo.ColorSpace, channels []uint8) ([]pdfCompositePixel, error) {
	nodes, err := c.importer.shadingSources(pattern, node, outline)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		result := c.acquirePixels(len(backdrop))
		clear(result)
		return result, nil
	}
	local := *c
	local.transfers, local.shadingSource = nil, true
	local.channels = channels
	result, err := local.shadingSourcePixels(nodes[len(nodes)-1], backdrop, space)
	if err != nil || len(nodes) == 1 {
		return result, err
	}
	var backgroundChannels []uint8
	if channels != nil {
		backgroundChannels = c.acquireChannels(len(channels))
		defer c.releaseChannels(backgroundChannels)
		clear(backgroundChannels)
	}
	local.channels = backgroundChannels
	background, err := local.shadingSourcePixels(nodes[0], backdrop, space)
	if err != nil {
		c.releasePixels(result)
		return nil, err
	}
	defer c.releasePixels(background)
	coverage, err := c.coverage(nodes[0], outline)
	if err != nil {
		c.releasePixels(result)
		return nil, err
	}
	defer c.releaseCoverage(coverage)
	step := 25.4 / c.importer.rasterDPI
	for i := range result {
		if i%pdfCompositeTileSize == 0 {
			if err := c.importer.ctx.Err(); err != nil {
				c.releasePixels(result)
				return nil, err
			}
		}
		shape := 0.0
		if coverage != nil {
			a := baseimage.AlphaAt(coverage, i%c.width, i/c.width)
			shape = float64(a) / 65535
		}
		if nodes[0].image != nil && shape != 0 {
			point := c.inverse.Apply(pdfgo.Point{X: c.box.X + (float64(i%c.width)+.5)*step, Y: c.box.Y + (float64(i/c.width)+.5)*step})
			_, alpha, err := c.imageColor(nodes[0].image, point, space)
			if err != nil {
				c.releasePixels(result)
				return nil, err
			}
			shape *= alpha
		}
		remaining := 1.0
		if shape != 0 {
			remaining -= math.Min(1, result[i].Shape/shape)
		}
		alpha := result[i].Alpha + background[i].Alpha*remaining
		if alpha != 0 {
			for j := 0; j < space.Components(); j++ {
				result[i].Values[j] = (result[i].Values[j]*result[i].Alpha + background[i].Values[j]*background[i].Alpha*remaining) / alpha
			}
		}
		result[i].Alpha, result[i].Effect = alpha, alpha
		result[i].Shape += background[i].Shape * remaining
		if channels != nil && background[i].Shape*remaining > 0 {
			channels[i] |= backgroundChannels[i]
		}
	}
	return result, nil
}

// shadingSources 将内部状态绑定到使用图元，几何和裁剪仅计算一次
// 入参: pattern 只读着色图案, node 使用图元, outline 是否描边
// 返回: []pdfCompositeNode 背景及着色图元, error 图案遍历错误
func (p *pdfImporter) shadingSources(pattern *pdfgo.ShadingPattern, node pdfCompositeNode, outline bool) ([]pdfCompositeNode, error) {
	cache := p.compositingCache()
	key := pdfShadingSourceKey{pattern, pdfCompositeKey{path: node.path, text: node.text, image: node.image, stroke: outline}}
	if sources, ok := cache.shadingSources[key]; ok {
		return sources, nil
	}
	nodes, err := p.compositeShadingPattern(pattern)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	outer, _, _ := node.style()
	sources := make([]pdfCompositeNode, 0, len(nodes[0].children))
	for _, child := range nodes[0].children {
		if child.path == nil {
			continue
		}
		style := child.path.Style
		style.LineWidth, style.Cap, style.Join, style.MiterLimit = outer.LineWidth, outer.Cap, outer.Join, outer.MiterLimit
		style.Dash, style.DashPhase, style.StrokeAdjust = outer.Dash, outer.DashPhase, outer.StrokeAdjust
		style.Clips = append(append([]pdfgo.Path(nil), outer.Clips...), style.Clips...)
		style.Stroke, style.StrokeOverprint = style.Fill, style.FillOverprint
		source := node
		if node.path != nil {
			mark := *node.path
			mark.Style, mark.Fill, mark.Stroke = style, !outline, outline
			source.path = &mark
		} else if node.text != nil {
			mark := *node.text
			mark.Style, mark.Mode = style, 0
			if outline {
				mark.Mode = 1
			}
			source.text = &mark
		} else {
			mark := *node.image
			mark.Style = style
			source.image = &mark
		}
		sources = append(sources, source)
	}
	if cache.shadingSources == nil {
		cache.shadingSources = make(map[pdfShadingSourceKey][]pdfCompositeNode)
	}
	cache.shadingSources[key] = sources
	return sources, nil
}

// shadingSourcePixels 求值单个内部图元并移除初始背景贡献
// 入参: node 内部图元, backdrop 初始背景, space 混合空间
// 返回: []pdfCompositePixel 源颜色和覆盖, error 合成或取消错误
func (c *pdfCompositor) shadingSourcePixels(node pdfCompositeNode, backdrop []pdfCompositePixel, space *pdfgo.ColorSpace) ([]pdfCompositePixel, error) {
	result := c.acquirePixels(len(backdrop))
	complete := false
	defer func() {
		if !complete {
			c.releasePixels(result)
		}
	}()
	for i, pixel := range backdrop {
		result[i] = pdfCompositePixel{Values: pixel.Values, Alpha: pixel.Alpha}
	}
	if err := c.drawMark(node, result, space); err != nil {
		return nil, err
	}
	for i := range result {
		if i%pdfCompositeTileSize == 0 {
			if err := c.importer.ctx.Err(); err != nil {
				return nil, err
			}
		}
		pixel := &result[i]
		if pixel.Effect != 0 {
			for j := 0; j < space.Components(); j++ {
				pixel.Values[j] = math.Max(0, math.Min(1, (pixel.Values[j]*pixel.Alpha-backdrop[i].Values[j]*backdrop[i].Alpha*(1-pixel.Effect))/pixel.Effect))
			}
		}
		pixel.Alpha = pixel.Effect
	}
	complete = true
	return result, nil
}
