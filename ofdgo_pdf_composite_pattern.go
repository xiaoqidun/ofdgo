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

// pdfCompositePattern 保存图案单元的原始图元和独立几何缓存
type pdfCompositePattern struct {
	nodes []pdfCompositeNode
	local pdfImporter
	box   Box
}

// tiling 将图案作为非隔离组绘制，保留背景贡献及单元间隙
// 入参: paint 图案画刷, backdrop 初始背景, space 混合空间
// 返回: []pdfCompositePixel 去除背景贡献的图案像素, error 解析或绘制错误
func (c *pdfCompositor) tiling(paint pdfgo.Paint, backdrop []pdfCompositePixel, space *pdfgo.ColorSpace) ([]pdfCompositePixel, error) {
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
	limit := float64(int(^uint(0) >> 1))
	for _, value := range []float64{left, right, top, bottom} {
		if !finite(value) || value <= -limit || value >= limit {
			return nil, fmt.Errorf("PDF tiling pattern instance range overflow")
		}
	}
	result := make([]pdfCompositePixel, len(backdrop))
	for i, pixel := range backdrop {
		result[i] = pdfCompositePixel{values: pixel.values, alpha: pixel.alpha}
	}
	localInverse, _ := pattern.local.matrix.Inverse()
	for row := int(top); row <= int(bottom); row++ {
		for column := int(left); column <= int(right); column++ {
			if err := c.importer.ctx.Err(); err != nil {
				return nil, err
			}
			x, y := float64(column)*xstep, float64(row)*ystep
			dx, dy := matrix[0]*x+matrix[2]*y+pattern.box.X, matrix[1]*x+matrix[3]*y+pattern.box.Y
			cell := pdfCompositor{importer: &pattern.local, box: Box{X: c.box.X - dx, Y: c.box.Y - dy, W: c.box.W, H: c.box.H},
				width: c.width, height: c.height, inverse: localInverse, cache: pattern.local.compositingCache(), masks: map[*pdfgo.SoftMask][]float64{}}
			if err := cell.draw(pattern.nodes, result, space); err != nil {
				return nil, err
			}
		}
	}
	for i := range result {
		pixel := &result[i]
		if pixel.effect != 0 {
			for j := 0; j < space.Components(); j++ {
				pixel.values[j] = math.Max(0, math.Min(1, (pixel.values[j]*pixel.alpha-backdrop[i].values[j]*backdrop[i].alpha*(1-pixel.effect))/pixel.effect))
			}
		}
		pixel.alpha = pixel.effect
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
	nodes, err := p.collectCompositeNodes(func(v pdfgo.Visitor) error { return source.Walk(p.ctx, paint, v) })
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
	pattern := &pdfCompositePattern{nodes: nodes, local: local, box: box}
	cache.patterns[paint] = pattern
	return pattern, nil
}

// directCompositeNode 检查图元及其图案单元能否独立保留，不忽略单元内的背景混合
// 入参: node 图元, space 混合空间
// 返回: bool 是否可直接转换, error 图案解析错误
func (p *pdfImporter) directCompositeNode(node pdfCompositeNode, space *pdfgo.ColorSpace) (bool, error) {
	if !node.opaque(space) && !(space.SRGBEquivalent() && node.direct()) {
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
			if _, found, err := pdfImageProcessColorants(node.image.Image); err != nil || found {
				return found, err
			}
		}
		if fill && style.FillOverprint && pdfOverprintNeedsSeparation(style.Fill) || stroke && style.StrokeOverprint && pdfOverprintNeedsSeparation(style.Stroke) {
			return true, nil
		}
		for i, paint := range []pdfgo.Paint{style.Fill, style.Stroke} {
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
