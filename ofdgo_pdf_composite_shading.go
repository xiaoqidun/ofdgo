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
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// pdfShadingSourceKey 区分图案的使用几何及填充或描边
type pdfShadingSourceKey struct {
	pattern  *pdfgo.ShadingPattern
	geometry pdfCompositeKey
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
	})
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
// 入参: pattern 着色图案, node 使用图元, outline 是否描边, backdrop 初始背景, space 父级混合空间
// 返回: []pdfCompositePixel 源颜色、形状及不透明度, error 状态或合成错误
func (c *pdfCompositor) shadingPattern(pattern *pdfgo.ShadingPattern, node pdfCompositeNode, outline bool, backdrop []pdfCompositePixel, space *pdfgo.ColorSpace) ([]pdfCompositePixel, error) {
	nodes, err := c.importer.shadingSources(pattern, node, outline)
	if err != nil {
		return nil, err
	}
	local := *c
	local.transfers, local.shadingSource = nil, true
	result, err := local.shadingSourcePixels(nodes[len(nodes)-1], backdrop, space)
	if err != nil || len(nodes) == 1 {
		return result, err
	}
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
			a := imageAlphaAt(coverage, i%c.width, i/c.width)
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
			remaining -= math.Min(1, result[i].shape/shape)
		}
		alpha := result[i].alpha + background[i].alpha*remaining
		if alpha != 0 {
			for j := 0; j < space.Components(); j++ {
				result[i].values[j] = (result[i].values[j]*result[i].alpha + background[i].values[j]*background[i].alpha*remaining) / alpha
			}
		}
		result[i].alpha, result[i].effect = alpha, alpha
		result[i].shape += background[i].shape * remaining
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
	outer, _, _ := node.style()
	sources := make([]pdfCompositeNode, 0, len(nodes[0].children))
	for _, child := range nodes[0].children {
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
		result[i] = pdfCompositePixel{values: pixel.values, alpha: pixel.alpha}
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
		if pixel.effect != 0 {
			for j := 0; j < space.Components(); j++ {
				pixel.values[j] = math.Max(0, math.Min(1, (pixel.values[j]*pixel.alpha-backdrop[i].values[j]*backdrop[i].alpha*(1-pixel.effect))/pixel.effect))
			}
		}
		pixel.alpha = pixel.effect
	}
	complete = true
	return result, nil
}
