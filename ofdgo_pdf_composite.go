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
	"image/color"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// pdfCompositeNode 保留局部合成所需的原始图元和透明组层级
type pdfCompositeNode struct {
	path     *pdfgo.PathMark
	text     *pdfgo.TextMark
	image    *pdfgo.ImageMark
	group    *pdfgo.GroupMark
	children []pdfCompositeNode
}

// pdfCompositePixel 保存非预乘源空间分量及累计透明度
type pdfCompositePixel struct {
	values [4]float64
	alpha  float64
	effect float64
}

// pdfCompositor 在指定局部区域中按源颜色空间合成PDF图元
type pdfCompositor struct {
	importer      *pdfImporter
	box           Box
	width, height int
	inverse       pdfgo.Matrix
	cache         *pdfCompositeCache
	masks         map[*pdfgo.SoftMask][]float64
}

// pdfCompositeKey 区分图元的填充与描边几何
type pdfCompositeKey struct {
	path   *pdfgo.PathMark
	text   *pdfgo.TextMark
	image  *pdfgo.ImageMark
	stroke bool
}

// pdfCompositeGeometry 保存后端中立的绘制命令及其页面边界
type pdfCompositeGeometry struct {
	scene *RasterPage
	box   Box
}

// pdfCompositeCache 在单页内复用几何、图像分量及蒙版内容
type pdfCompositeCache struct {
	geometry map[pdfCompositeKey]pdfCompositeGeometry
	images   map[*pdfgo.Image]*pdfgo.ImageComponents
	masks    map[*pdfgo.SoftMask][]pdfCompositeNode
}

// compositingCache 按需创建单页合成缓存，页面切换时由导入器释放
// 返回: *pdfCompositeCache 当前页面缓存
func (p *pdfImporter) compositingCache() *pdfCompositeCache {
	if p.compositeCache == nil {
		p.compositeCache = &pdfCompositeCache{geometry: map[pdfCompositeKey]pdfCompositeGeometry{}, images: map[*pdfgo.Image]*pdfgo.ImageComponents{}, masks: map[*pdfgo.SoftMask][]pdfCompositeNode{}}
	}
	return p.compositeCache
}

// coverage 复用已编译的覆盖场景，跳过与当前区域无交集的图元
// 入参: node 图元, stroke 是否描边
// 返回: image.Image 当前区域覆盖或空值, error 编译或渲染错误
func (c *pdfCompositor) coverage(node pdfCompositeNode, stroke bool) (image.Image, error) {
	if node.text != nil && len(node.text.Font.Program) == 0 && node.text.Font.BoundingBox != nil {
		b := c.importer.compositeTextBounds(*node.text, stroke)
		if b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
			return nil, nil
		}
	}
	key := pdfCompositeKey{path: node.path, text: node.text, image: node.image, stroke: stroke}
	geometry, ok := c.cache.geometry[key]
	if !ok {
		var err error
		geometry.scene, geometry.box, err = c.importer.compileGroup(func(local *pdfImporter) error { return node.coverage(local, stroke) })
		if err != nil {
			return nil, err
		}
		c.cache.geometry[key] = geometry
	}
	b := geometry.box
	if geometry.scene == nil || b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
		return nil, nil
	}
	return c.importer.renderGroupScene(geometry.scene, c.box)
}

// compositeTextBounds 按字体描述符计算保守边界，仅用于排除不相交的外部字体
// 入参: mark 具有字体边界的文字, stroke 是否描边
// 返回: Box 包含描边和抗锯齿边缘的页面范围
func (p *pdfImporter) compositeTextBounds(mark pdfgo.TextMark, stroke bool) Box {
	b := mark.Font.BoundingBox
	matrix := p.matrix.Mul(mark.Matrix)
	var points []pdfgo.Point
	for _, origin := range mark.Positions {
		for _, point := range []pdfgo.Point{{X: b.XMin, Y: b.YMin}, {X: b.XMax, Y: b.YMin}, {X: b.XMax, Y: b.YMax}, {X: b.XMin, Y: b.YMax}} {
			points = append(points, matrix.Apply(pdfgo.Point{X: origin.X + point.X*mark.Size*mark.HorizontalScale/1000, Y: origin.Y + point.Y*mark.Size/1000}))
		}
	}
	box := pdfBounds(points)
	marginX, marginY := 25.4/p.rasterDPI, 25.4/p.rasterDPI
	if stroke {
		m := mark.StrokeMatrix
		if m == (pdfgo.Matrix{}) {
			m = pdfgo.Identity()
		}
		m = p.matrix.Mul(m)
		distance := mark.Style.LineWidth / 2 * math.Max(1, mark.Style.MiterLimit)
		marginX += distance * (math.Abs(m[0]) + math.Abs(m[2]))
		marginY += distance * (math.Abs(m[1]) + math.Abs(m[3]))
	}
	return Box{X: box.X - marginX, Y: box.Y - marginY, W: box.W + 2*marginX, H: box.H + 2*marginY}
}

// pdfCompositeNodes 收集原始绘制顺序，语义警告只在首次解析时交付
// 入参: walk 内容访问函数, warning 警告回调
// 返回: []pdfCompositeNode 原始图元, error 解析错误
func pdfCompositeNodes(walk func(pdfgo.Visitor) error, warning func(pdfgo.Diagnostic)) ([]pdfCompositeNode, error) {
	var nodes []pdfCompositeNode
	v := pdfgo.Visitor{Warning: warning}
	v.Path = func(mark pdfgo.PathMark) error { nodes = append(nodes, pdfCompositeNode{path: &mark}); return nil }
	v.Text = func(mark pdfgo.TextMark) error { nodes = append(nodes, pdfCompositeNode{text: &mark}); return nil }
	v.Image = func(mark pdfgo.ImageMark) error { nodes = append(nodes, pdfCompositeNode{image: &mark}); return nil }
	v.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
		children, err := pdfCompositeNodes(walk, warning)
		if err != nil {
			return err
		}
		nodes = append(nodes, pdfCompositeNode{group: &mark, children: children})
		return nil
	}
	err := walk(v)
	return nodes, err
}

// compositePage 保留独立不透明对象，仅将需要背景参与的效果在局部区域合成
// 入参: space 页面混合空间, walk 页面内容
// 返回: error 转换错误
func (p *pdfImporter) compositePage(space *pdfgo.ColorSpace, walk func(pdfgo.Visitor) error) error {
	nodes, err := pdfCompositeNodes(walk, p.warning)
	if err != nil {
		return err
	}
	for i, node := range nodes {
		if node.opaque(space) {
			if err := node.emit(p.visitor()); err != nil {
				return fmt.Errorf("convert graphic %d: %w", i+1, err)
			}
		} else {
			if p.warning == nil {
				return &pdfgo.UnsupportedError{Feature: "page color-space compositing"}
			}
			if err := p.flushPath(); err != nil {
				return err
			}
			if err := p.compositeRegion(nodes[:i], node, space, true); err != nil {
				return fmt.Errorf("composite graphic %d: %w", i+1, err)
			}
		}
	}
	return p.flushPath()
}

// opaque 检查图元是否无需与页面背景混合
// 入参: space 当前混合空间
// 返回: bool 是否可直接保留
func (n pdfCompositeNode) opaque(space *pdfgo.ColorSpace) bool {
	if n.group != nil {
		g := n.group
		if g.Alpha != 1 || g.SoftMask != nil || !pdfNormalBlend(g.BlendMode) || g.ColorSpace != nil && !g.ColorSpace.Equal(space) {
			return false
		}
		for _, child := range n.children {
			if !child.opaque(space) {
				return false
			}
		}
		return true
	}
	style, fill, stroke := n.style()
	if style.SoftMask != nil || !pdfNormalBlend(style.BlendMode) || fill && style.Fill.Alpha != 1 || stroke && style.Stroke.Alpha != 1 {
		return false
	}
	if fill && style.FillOverprint && pdfOverprintNeedsSeparation(style.Fill) || stroke && style.StrokeOverprint && pdfOverprintNeedsSeparation(style.Stroke) {
		return false
	}
	if fill && pdfGradientError(style.Fill) != nil || stroke && pdfGradientError(style.Stroke) != nil {
		return false
	}
	if n.image != nil && (n.image.Image.Mask != nil || n.image.Image.SoftMask != nil) {
		return false
	}
	return true
}

// style 获取图元实际使用的图形状态
// 返回: pdfgo.Style 图形状态, bool 是否填充, bool 是否描边
func (n pdfCompositeNode) style() (pdfgo.Style, bool, bool) {
	if n.path != nil {
		return n.path.Style, n.path.Fill, n.path.Stroke
	}
	if n.text != nil {
		mode := n.text.Mode % 4
		return n.text.Style, mode == 0 || mode == 2, mode == 1 || mode == 2
	}
	return n.image.Style, true, false
}

// emit 向现有转换器交付原始图元
// 入参: visitor 图元访问器
// 返回: error 转换错误
func (n pdfCompositeNode) emit(visitor pdfgo.Visitor) error {
	if n.path != nil {
		return visitor.Path(*n.path)
	}
	if n.text != nil {
		return visitor.Text(*n.text)
	}
	if n.image != nil {
		return visitor.Image(*n.image)
	}
	return visitor.Group(*n.group, func(v pdfgo.Visitor) error {
		for _, child := range n.children {
			if err := child.emit(v); err != nil {
				return err
			}
		}
		return nil
	})
}

// coverage 构建白色几何覆盖，颜色和透明度由合成器单独计算
// 入参: p 局部导入器, stroke 是否只取描边
// 返回: error 几何错误
func (n pdfCompositeNode) coverage(p *pdfImporter, stroke bool) error {
	if n.group != nil {
		for _, child := range n.children {
			if child.group != nil {
				if err := child.coverage(p, false); err != nil {
					return err
				}
				continue
			}
			_, fill, hasStroke := child.style()
			if fill {
				if err := child.coverage(p, false); err != nil {
					return err
				}
			}
			if hasStroke {
				if err := child.coverage(p, true); err != nil {
					return err
				}
			}
		}
		return nil
	}
	style, _, _ := n.style()
	style.Fill, style.Stroke = pdfgo.Paint{RGB: [3]float64{1, 1, 1}, Alpha: 1}, pdfgo.Paint{RGB: [3]float64{1, 1, 1}, Alpha: 1}
	style.SoftMask, style.BlendMode = nil, "Normal"
	style.FillOverprint, style.StrokeOverprint = false, false
	if n.path != nil {
		mark := *n.path
		mark.Style = style
		mark.Fill, mark.Stroke = !stroke, stroke
		return p.path(mark)
	}
	if n.text != nil {
		mark := *n.text
		mark.Style = style
		mark.Mode = 0
		if stroke {
			mark.Mode = 1
		}
		return p.text(mark)
	}
	mark := n.image
	path := pdfgo.Path{Segments: []pdfgo.Segment{
		{Operator: "M", Points: []pdfgo.Point{mark.Matrix.Apply(pdfgo.Point{})}},
		{Operator: "L", Points: []pdfgo.Point{mark.Matrix.Apply(pdfgo.Point{X: 1})}},
		{Operator: "L", Points: []pdfgo.Point{mark.Matrix.Apply(pdfgo.Point{X: 1, Y: 1})}},
		{Operator: "L", Points: []pdfgo.Point{mark.Matrix.Apply(pdfgo.Point{Y: 1})}},
		{Operator: "C"},
	}}
	return p.path(pdfgo.PathMark{Path: path, Style: style, Fill: true})
}

// compositeRegion 按原始背景计算局部效果，不将组外文字和图形栅格化
// 入参: backdrop 前序图元, node 当前效果, space 页面混合空间, flatten 是否合并页面背景
// 返回: error 合成或保存错误
func (p *pdfImporter) compositeRegion(backdrop []pdfCompositeNode, node pdfCompositeNode, space *pdfgo.ColorSpace, flatten bool) error {
	scene, box, err := p.compileGroup(func(local *pdfImporter) error {
		if node.group != nil {
			return node.coverage(local, false)
		}
		_, fill, stroke := node.style()
		if fill {
			if err := node.coverage(local, false); err != nil {
				return err
			}
		}
		if stroke {
			return node.coverage(local, true)
		}
		return nil
	})
	if err != nil || scene == nil {
		return err
	}
	inverse, ok := p.matrix.Inverse()
	if !ok {
		return fmt.Errorf("invalid PDF compositing transform")
	}
	w, h, err := (&RasterPage{Width: box.W, Height: box.H, DPI: p.rasterDPI}).PixelSize()
	if err != nil {
		return err
	}
	output := image.NewNRGBA(image.Rect(0, 0, w, h))
	cache := p.compositingCache()
	step := 25.4 / p.rasterDPI
	for y := 0; y < h; y += 256 {
		for x := 0; x < w; x += 256 {
			width, height := min(256, w-x), min(256, h-y)
			c := pdfCompositor{importer: p, box: Box{X: box.X + float64(x)*step, Y: box.Y + float64(y)*step, W: float64(width) * step, H: float64(height) * step}, width: width, height: height, inverse: inverse, cache: cache, masks: map[*pdfgo.SoftMask][]float64{}}
			pixels := make([]pdfCompositePixel, width*height)
			if err := c.draw(backdrop, pixels, space); err != nil {
				return err
			}
			for i := range pixels {
				pixels[i].effect = 0
			}
			if err := c.draw([]pdfCompositeNode{node}, pixels, space); err != nil {
				return err
			}
			for i, pixel := range pixels {
				if pixel.effect == 0 {
					continue
				}
				rgb, err := pdfCompositeOutputColor(space, pixel.values)
				if err != nil {
					return err
				}
				alpha := pixel.alpha
				if flatten {
					for j := range rgb {
						rgb[j] = pixel.alpha*rgb[j] + 1 - pixel.alpha
					}
					alpha = 1
				}
				output.SetNRGBA(x+i%width, y+i/width, color.NRGBA{R: uint8(math.Round(rgb[0] * 255)), G: uint8(math.Round(rgb[1] * 255)), B: uint8(math.Round(rgb[2] * 255)), A: uint8(math.Round(alpha * 255))})
			}
		}
	}
	return p.appendRasterGroup(output, box, 1)
}

// pdfCompositeOutputColor 将合成结果映射到OFD输出，与保留的设备四色对象使用相同显示换算
// 入参: space 合成空间, values 非预乘分量
// 返回: [3]float64 显示颜色, error 校准颜色转换错误
func pdfCompositeOutputColor(space *pdfgo.ColorSpace, values [4]float64) ([3]float64, error) {
	if space.Model == "DeviceCMYK" && !space.Calibrated() {
		black := 1 - values[3]
		return [3]float64{(1 - values[0]) * black, (1 - values[1]) * black, (1 - values[2]) * black}, nil
	}
	return space.RGB(values[:space.Components()], "RelativeColorimetric")
}

// pdfNormalBlend 判断混合模式是否为普通覆盖
// 入参: mode 混合模式
// 返回: bool 是否普通覆盖
func pdfNormalBlend(mode pdfgo.Name) bool {
	return mode == "" || mode == "Normal" || mode == "Compatible"
}
