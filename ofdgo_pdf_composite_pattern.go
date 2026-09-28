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
	paint.Alpha = 1
	if c.cache.patterns == nil {
		c.cache.patterns = make(map[pdfgo.Paint]*pdfCompositePattern)
	}
	pattern := c.cache.patterns[paint]
	matrix := c.importer.matrix.Mul(source.Matrix)
	inverse, ok := matrix.Inverse()
	if !ok {
		return nil, fmt.Errorf("invalid PDF tiling pattern matrix")
	}
	if pattern == nil {
		nodes, err := c.importer.collectCompositeNodes(func(v pdfgo.Visitor) error { return source.Walk(c.importer.ctx, paint, v) })
		if err != nil {
			return nil, err
		}
		b := source.BBox
		box := pdfBounds([]pdfgo.Point{
			matrix.Apply(pdfgo.Point{X: b.XMin, Y: b.YMin}), matrix.Apply(pdfgo.Point{X: b.XMax, Y: b.YMin}),
			matrix.Apply(pdfgo.Point{X: b.XMax, Y: b.YMax}), matrix.Apply(pdfgo.Point{X: b.XMin, Y: b.YMax}),
		})
		if box.W <= 0 || box.H <= 0 {
			return nil, fmt.Errorf("invalid PDF tiling pattern bounds")
		}
		local := *c.importer
		local.matrix = matrix
		local.matrix[4] -= box.X
		local.matrix[5] -= box.Y
		local.pageWidth, local.pageHeight = box.W, box.H
		local.compositeCache = nil
		pattern = &pdfCompositePattern{nodes: nodes, local: local, box: box}
		c.cache.patterns[paint] = pattern
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
