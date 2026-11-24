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
	"image"
	"image/color"
	"math"
	"reflect"
	"runtime"
	"slices"
	"sync"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// 图案标记保留设备四色通道及非零对象形状
const (
	pdfTransferChannels uint8 = 15
	pdfTransferShape    uint8 = 16
)

// pdfCompositeTileSize 限定单次合成块的像素边长
const pdfCompositeTileSize = codecpdf.CompositeTileSize

// pdfFormSpan 保存单个普通表单边界及其覆盖的连续图元数量
type pdfFormSpan struct {
	mark  *pdfgo.FormMark
	count int
}

// pdfCompositeNode 保留局部合成所需的原始图元和透明组层级
type pdfCompositeNode struct {
	path       *pdfgo.PathMark
	text       *pdfgo.TextMark
	image      *pdfgo.ImageMark
	group      *pdfgo.GroupMark
	forms      []pdfFormSpan
	children   []pdfCompositeNode
	textObject *pdfgo.TextObject
	glyph      *pdfgo.TextMark
}

// pdfCompositePixel 复用内部PDF像素布局
type pdfCompositePixel = codecpdf.CompositePixel

// pdfCompositor 在指定局部区域中按源颜色空间合成PDF图元
type pdfCompositor struct {
	importer        *pdfImporter
	box             Box
	width, height   int
	inverse         pdfgo.Matrix
	cache           *pdfCompositeCache
	scratch         *pdfCompositeScratch
	origins         *pdfOriginSet
	masks           map[*pdfgo.SoftMask][]float64
	clipMasks       map[pdfClipMaskKey]image.Image
	meshes          map[pdfMeshKey][]pdfShadingPixel
	gradients       map[pdfGradientKey][]pdfShadingPixel
	transfers       []*pdfgo.TransferFunction
	channels        []uint8
	transferModel   pdfgo.Name
	transferBlocked bool
	shadingSource   bool
	softMask        bool
}

// pdfCompositeKey 区分图元的填充与描边几何
type pdfCompositeKey struct {
	path   *pdfgo.PathMark
	text   *pdfgo.TextMark
	image  *pdfgo.ImageMark
	stroke bool
}

// pdfCompositeSceneKey 隔离不同图案坐标系，在单页内共享场景缓存预算
type pdfCompositeSceneKey struct {
	owner *pdfCompositeCache
	mark  pdfCompositeKey
}

// pdfClipPathRefKey 按原始切片指针缓存单条无文字裁剪路径的哈希键
type pdfClipPathRefKey struct {
	segments *pdfgo.Segment
	length   int
	evenOdd  bool
	matrix   pdfgo.Matrix
}

// pdfClipMaskKey 区分块区域与分辨率下的复杂裁剪光栅蒙版
type pdfClipMaskKey struct {
	key    [32]byte
	box    Box
	width  int
	height int
	dpi    float64
}

// pdfCompositeClip 缓存规范化裁剪轮廓及其外接框与延迟光栅场景
type pdfCompositeClip struct {
	path   GeometryPath
	box    Box
	rect   Box
	isRect bool
	key    [32]byte
	scene  *RasterPage
}

// pdfCompositeGeometry 保存后端中立的绘制命令及其页面边界
type pdfCompositeGeometry struct {
	scene *RasterPage
	box   Box
	clip  *pdfCompositeClip
}

// pdfCompositeColorSampler 复用渐变几何和相同轴向参数的颜色，不近似函数
type pdfCompositeColorSampler struct {
	paint      pdfgo.Paint
	space      *pdfgo.ColorSpace
	intent     pdfgo.Name
	conversion pdfgo.ColorConversion
	values     map[float64][4]float64
	position   pdfgo.GradientPosition
	converter  pdfgo.ColorConverter
	colorants  *codecpdf.ShadingColorants
	prepared   bool
	converted  bool
}

// pdfImageProcessKey 隔离图像的组空间及软蒙版采样，不复用错误的原生映射
type pdfImageProcessKey struct {
	image    *pdfgo.Image
	space    *pdfgo.ColorSpace
	softMask bool
}

// pdfCompositeImageColor 复用图像颜色变换及相邻相同分量的精确结果
type pdfCompositeImageColor struct {
	mark       *pdfgo.ImageMark
	space      *pdfgo.ColorSpace
	source     *pdfgo.ColorSpace
	intent     pdfgo.Name
	conversion pdfgo.ColorConversion
	converter  pdfgo.ColorConverter
	input      [4]float64
	output     [4]float64
	ready      bool
}

// pdfCompositeCache 在单页内复用几何、图像分量及蒙版内容
type pdfCompositeCache struct {
	resources       *Resources
	geometry        *renderCache[pdfCompositeSceneKey, pdfCompositeGeometry]
	clips           *renderCache[[32]byte, *pdfCompositeClip]
	clipPathKeys    map[pdfClipPathRefKey][32]byte
	bounds          map[pdfCompositeKey]Box
	images          map[*pdfgo.Image]*pdfgo.ImageComponents
	imageMatrix     map[*pdfgo.ImageMark]pdfgo.Matrix
	imageProcesses  map[pdfImageProcessKey]*pdfgo.ImageProcess
	imageColor      pdfCompositeImageColor
	masks           map[*pdfgo.SoftMask][]pdfCompositeNode
	meshes          map[*pdfgo.MeshGradient]*codecpdf.MeshPrepared
	linearMeshes    *renderCache[pdfLinearMeshKey, []pdfgo.MeshTriangle]
	patterns        map[pdfgo.Paint]*pdfCompositePattern
	shadingPatterns map[*pdfgo.ShadingPattern][]pdfCompositeNode
	shadingSources  map[pdfShadingSourceKey][]pdfCompositeNode
	shadings        map[pdfGradientKey]pdfCompositeGeometry
	transfers       codecpdf.TransferCache
	textDisjoint    map[*pdfgo.GroupMark]bool
	groups          map[*pdfgo.GroupMark]Box
}

// pdfCompositeScratch 复用像素及蒙版缓冲，不跨页持有活动内容
type pdfCompositeScratch struct {
	pixels     codecpdf.CompositeBuffers[pdfCompositePixel]
	masks      codecpdf.CompositeBuffers[float64]
	channels   codecpdf.CompositeBuffers[uint8]
	subRasters [8][]byte
}

// direct 检查sRGB图元能否使用标准OFD透明度而无需背景合成
// 返回: bool 是否可直接转换
func (n pdfCompositeNode) direct() bool {
	if g := n.group; g != nil {
		single := n.textObject != nil && len(n.children) == 1 && (n.children[0].glyph != nil || n.children[0].text != nil && len(n.children[0].text.Glyphs) == 1)
		if g.Knockout && !single || !g.Isolated && g.SoftMask != nil || !codecpdf.NormalBlend(g.BlendMode) || g.ColorSpace != nil && !g.ColorSpace.SRGBEquivalent() {
			return false
		}
		for _, child := range n.children {
			if !child.direct() {
				return false
			}
			if g.Alpha != 1 && child.group == nil {
				style, _, _ := child.style()
				if style.SoftMask != nil {
					return false
				}
			}
		}
		return true
	}
	s, fill, stroke := n.style()
	if (n.image == nil || n.image.Image.ImageMask) && (fill && s.Fill.Shading != nil || stroke && s.Stroke.Shading != nil) {
		return false
	}
	if n.image != nil && n.image.Image.ImageMask && s.Fill.Tiling != nil {
		return false
	}
	if fill && stroke {
		sameAlpha := n.path != nil && s.Fill.Alpha == s.Stroke.Alpha && !s.AlphaIsShape && !s.FillOverprint && !s.StrokeOverprint
		if s.SoftMask != nil || !sameAlpha && (s.Fill.Alpha != 1 || s.Stroke.Alpha != 1) {
			return false
		}
	}
	if n.image != nil && s.FillOverprint {
		return false
	}
	return codecpdf.NormalBlend(s.BlendMode) && !(fill && s.FillOverprint && codecpdf.OverprintNeedsSeparation(s.Fill) || stroke && s.StrokeOverprint && codecpdf.OverprintNeedsSeparation(s.Stroke))
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
	if n.textObject != nil || n.glyph != nil {
		for _, child := range n.children {
			if err := child.emit(visitor); err != nil {
				return err
			}
		}
		return nil
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
	style.Transfer = nil
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

// coverage 复用已编译的覆盖场景，跳过与当前区域无交集的图元
// 几何编译前让出调度，避免单线程运行时积累已失效的临时对象
// 入参: node 图元, stroke 是否描边
// 返回: image.Image 当前区域覆盖或空值, error 编译或渲染错误
func (c *pdfCompositor) coverage(node pdfCompositeNode, stroke bool) (image.Image, error) {
	key := pdfCompositeKey{path: node.path, text: node.text, image: node.image, stroke: stroke}
	if b, ok := c.cache.bounds[key]; ok && (b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y) {
		return nil, nil
	}
	if node.text != nil && len(node.text.Font.Program) == 0 && node.text.Font.BoundingBox != nil {
		b := c.importer.compositeTextBounds(*node.text, stroke)
		if b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
			return nil, nil
		}
	}
	geometry, err := c.geometry(node, stroke)
	if err != nil {
		return nil, err
	}
	b := geometry.box
	if geometry.scene == nil || b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
		return nil, nil
	}
	var mask image.Image
	if geometry.clip != nil {
		mask, err = c.clipMask(geometry.clip)
		if err != nil || mask == nil {
			return nil, err
		}
	}
	target, err := c.acquireCoverage()
	if err != nil {
		return nil, err
	}
	coverage, err := c.importer.renderGroupSceneInto(geometry.scene, c.box, target)
	if err != nil {
		c.releaseCoverage(target)
		return nil, err
	}
	if mask != nil {
		applied, any := applyClipMask(coverage, mask, image.Point{})
		if !any {
			c.releaseCoverage(coverage)
			return nil, nil
		}
		if applied != coverage {
			c.releaseCoverage(coverage)
			coverage = applied
		}
	}
	return coverage, nil
}

// coverageRect 按图元边界与当前块的交集紧致像素框渲染覆盖缓冲，避免微小图元清空整块画布
// 入参: node 图元, stroke 是否描边
// 返回: image.Image 紧致覆盖图像, image.Rectangle 当前块内像素范围, error 编译或渲染错误
func (c *pdfCompositor) coverageRect(node pdfCompositeNode, stroke bool) (image.Image, image.Rectangle, error) {
	key := pdfCompositeKey{path: node.path, text: node.text, image: node.image, stroke: stroke}
	if b, ok := c.cache.bounds[key]; ok && (b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y) {
		return nil, image.Rectangle{}, nil
	}
	if node.text != nil && len(node.text.Font.Program) == 0 && node.text.Font.BoundingBox != nil {
		b := c.importer.compositeTextBounds(*node.text, stroke)
		if b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
			return nil, image.Rectangle{}, nil
		}
	}
	geometry, err := c.geometry(node, stroke)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	b := geometry.box
	if geometry.scene == nil || b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
		return nil, image.Rectangle{}, nil
	}
	region := c.pixelBounds(b)
	if region.Empty() {
		return nil, image.Rectangle{}, nil
	}
	var mask image.Image
	if geometry.clip != nil {
		mask, err = c.clipMask(geometry.clip)
		if err != nil || mask == nil {
			return nil, image.Rectangle{}, err
		}
	}
	step := 25.4 / c.importer.rasterDPI
	subBox := Box{X: c.box.X + float64(region.Min.X)*step, Y: c.box.Y + float64(region.Min.Y)*step, W: float64(region.Dx()) * step, H: float64(region.Dy()) * step}
	target, err := c.acquireCoverageBox(subBox)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	coverage, err := c.importer.renderGroupSceneInto(geometry.scene, subBox, target)
	if err != nil {
		c.releaseCoverage(target)
		return nil, image.Rectangle{}, err
	}
	if mask != nil {
		applied, any := applyClipMask(coverage, mask, region.Min)
		if !any {
			c.releaseCoverage(coverage)
			return nil, image.Rectangle{}, nil
		}
		if applied != coverage {
			c.releaseCoverage(coverage)
			coverage = applied
		}
	}
	return coverage, region, nil
}

// clipMask 在当前合成块按共享裁剪键缓存一次复杂非矩形裁剪的光栅蒙版
// 入参: clip 规范化裁剪
// 返回: image.Image 当前块裁剪蒙版或空值, error 编译或渲染错误
func (c *pdfCompositor) clipMask(clip *pdfCompositeClip) (image.Image, error) {
	if clip == nil || len(clip.path) == 0 {
		return nil, nil
	}
	b := clip.box
	if b.X >= c.box.X+c.box.W || b.Y >= c.box.Y+c.box.H || b.X+b.W <= c.box.X || b.Y+b.H <= c.box.Y {
		return nil, nil
	}
	key := pdfClipMaskKey{key: clip.key, box: c.box, width: c.width, height: c.height, dpi: c.importer.rasterDPI}
	if c.clipMasks != nil {
		if cached, ok := c.clipMasks[key]; ok {
			return cached, nil
		}
	}
	if clip.scene == nil {
		geometry, err := c.importer.renderer.Geometry()
		if err != nil {
			return nil, err
		}
		compiler := semanticCompiler{geometry: geometry, page: &RasterPage{Width: c.importer.pageWidth, Height: c.importer.pageHeight, DPI: c.importer.rasterDPI}}
		if err := compiler.command(clip.path, Paint{Color: color.RGBA{255, 255, 255, 255}}, false, nil, IdentityMatrix(), nil); err != nil {
			return nil, err
		}
		clip.scene = compiler.page
	}
	target, err := c.acquireCoverage()
	if err != nil {
		return nil, err
	}
	rendered, err := c.importer.renderGroupSceneInto(clip.scene, c.box, target)
	if err != nil {
		c.releaseCoverage(target)
		return nil, err
	}
	if rgba, ok := rendered.(*image.RGBA); ok {
		any := false
		for i := 3; i < len(rgba.Pix); i += 4 {
			if rgba.Pix[i] != 0 {
				any = true
				break
			}
		}
		if !any {
			c.releaseCoverage(rendered)
			rendered = nil
		}
	}
	if c.clipMasks == nil {
		c.clipMasks = map[pdfClipMaskKey]image.Image{}
	}
	c.clipMasks[key] = rendered
	return rendered, nil
}

// applyClipMask 将局部覆盖图像与当前块裁剪蒙版逐像素相乘并返回是否存在非零覆盖
// 入参: coverage 局部覆盖图像, mask 块裁剪蒙版, offset 局部区域在块内左上偏移
// 返回: image.Image 相乘后的覆盖图像, bool 是否存在非零覆盖
func applyClipMask(coverage, mask image.Image, offset image.Point) (image.Image, bool) {
	if dst, ok := coverage.(*image.RGBA); ok {
		if src, ok2 := mask.(*image.RGBA); ok2 {
			any := false
			tw, th := dst.Rect.Dx(), dst.Rect.Dy()
			sw, sh := src.Rect.Dx(), src.Rect.Dy()
			for y := range th {
				sy := offset.Y + y
				dRow := dst.Pix[y*dst.Stride : y*dst.Stride+tw*4]
				if sy < 0 || sy >= sh {
					clear(dRow)
					continue
				}
				sRow := src.Pix[sy*src.Stride:]
				for x := range tw {
					dOff := x * 4
					a := dRow[dOff+3]
					if a == 0 {
						continue
					}
					sx := offset.X + x
					if sx < 0 || sx >= sw {
						dRow[dOff], dRow[dOff+1], dRow[dOff+2], dRow[dOff+3] = 0, 0, 0, 0
						continue
					}
					m := sRow[sx*4+3]
					if m == 255 {
						any = true
						continue
					}
					if m == 0 {
						dRow[dOff], dRow[dOff+1], dRow[dOff+2], dRow[dOff+3] = 0, 0, 0, 0
						continue
					}
					v := uint8((uint32(a)*uint32(m) + 127) / 255)
					dRow[dOff], dRow[dOff+1], dRow[dOff+2], dRow[dOff+3] = v, v, v, v
					if v != 0 {
						any = true
					}
				}
			}
			return dst, any
		}
	}
	bounds := coverage.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	any := false
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			a := baseimage.AlphaAt(coverage, x, y)
			if a == 0 {
				continue
			}
			m := baseimage.AlphaAt(mask, offset.X+x, offset.Y+y)
			if m == 0 {
				continue
			}
			v := uint8((uint64(a)*uint64(m)/65535 + 128) >> 8)
			if v != 0 {
				any = true
				off := y*out.Stride + x*4
				out.Pix[off], out.Pix[off+1], out.Pix[off+2], out.Pix[off+3] = v, v, v, v
			}
		}
	}
	return out, any
}

// geometry 缓存图元或组的实际覆盖场景与像素对齐边界
// 入参: node 图元或组, stroke 是否描边
// 返回: pdfCompositeGeometry 覆盖场景, error 编译错误
func (c *pdfCompositor) geometry(node pdfCompositeNode, stroke bool) (pdfCompositeGeometry, error) {
	key := pdfCompositeKey{path: node.path, text: node.text, image: node.image, stroke: stroke}
	cache := c.cache.geometryCache()
	sceneKey := pdfCompositeSceneKey{owner: c.cache, mark: key}
	geometry, ok := cache.get(sceneKey)
	if !ok {
		runtime.Gosched()
		var err error
		geometry, err = c.importer.compileCoverage(node, stroke)
		if err != nil {
			return geometry, err
		}
		if c.cache.bounds == nil {
			c.cache.bounds = make(map[pdfCompositeKey]Box)
		}
		c.cache.bounds[key] = geometry.box
		cost := 256
		if geometry.scene != nil {
			cost += rasterCommandsCost(geometry.scene.Commands, cache.limit-cost)
		}
		cache.put(sceneKey, geometry, cost)
	}
	return geometry, nil
}

// geometryBounds 复用已度量边界，场景淘汰后不因范围判断而重新编译
// 入参: node 图元, stroke 是否描边
// 返回: Box 覆盖范围, error 编译错误
func (c *pdfCompositor) geometryBounds(node pdfCompositeNode, stroke bool) (Box, error) {
	key := pdfCompositeKey{path: node.path, text: node.text, image: node.image, stroke: stroke}
	if box, ok := c.cache.bounds[key]; ok {
		return box, nil
	}
	geometry, err := c.geometry(node, stroke)
	return geometry.box, err
}

// coverageBounds 合并实际覆盖边界并复用图元编译缓存，不采用字体估算范围
// 入参: node 图元或透明组
// 返回: Box 覆盖范围, error 编译或取消错误
func (c *pdfCompositor) coverageBounds(node pdfCompositeNode) (Box, error) {
	if err := c.importer.ctx.Err(); err != nil {
		return Box{}, err
	}
	var box Box
	if node.group != nil {
		for _, child := range node.children {
			bounds, err := c.coverageBounds(child)
			if err != nil {
				return Box{}, err
			}
			box = unionTextBox(box, bounds)
		}
		return box, nil
	}
	_, fill, stroke := node.style()
	for _, outline := range []bool{false, true} {
		if outline && !stroke || !outline && !fill {
			continue
		}
		bounds, err := c.geometryBounds(node, outline)
		if err != nil {
			return Box{}, err
		}
		box = unionTextBox(box, bounds)
	}
	return box, nil
}

// groupBounds 缓存组内实际覆盖范围，外部字体沿用描述符保守边界
// 入参: node 透明组
// 返回: Box 组覆盖范围, error 几何编译错误
func (c *pdfCompositor) groupBounds(node pdfCompositeNode) (Box, error) {
	if box, found := c.cache.groups[node.group]; found {
		return box, nil
	}
	var box Box
	for _, child := range node.children {
		if err := c.importer.ctx.Err(); err != nil {
			return Box{}, err
		}
		if child.group != nil {
			bounds, err := c.groupBounds(child)
			if err != nil {
				return Box{}, err
			}
			box = unionTextBox(box, bounds)
			continue
		}
		bounds, err := c.markBounds(child)
		if err != nil {
			return Box{}, err
		}
		box = unionTextBox(box, bounds)
	}
	if c.cache.groups == nil {
		c.cache.groups = make(map[*pdfgo.GroupMark]Box)
	}
	c.cache.groups[node.group] = box
	return box, nil
}

// markBounds 合并图元的填充与描边范围，外部字体不在排除阶段加载
// 入参: node 基本图元
// 返回: Box 图元覆盖范围, error 几何编译错误
func (c *pdfCompositor) markBounds(node pdfCompositeNode) (Box, error) {
	_, fill, stroke := node.style()
	var box Box
	for _, outline := range []bool{false, true} {
		if outline && !stroke || !outline && !fill {
			continue
		}
		if node.text != nil && len(node.text.Font.Program) == 0 && node.text.Font.BoundingBox != nil {
			box = unionTextBox(box, c.importer.compositeTextBounds(*node.text, outline))
			continue
		}
		bounds, err := c.geometryBounds(node, outline)
		if err != nil {
			return Box{}, err
		}
		box = unionTextBox(box, bounds)
	}
	return box, nil
}

// pixelBounds 将页面范围映射到当前块，保留一像素余量供浮点边界与抗锯齿使用
// 入参: box 页面范围
// 返回: image.Rectangle 当前块内的像素范围
func (c *pdfCompositor) pixelBounds(box Box) image.Rectangle {
	if box.W <= 0 || box.H <= 0 {
		return image.Rectangle{}
	}
	step := 25.4 / c.importer.rasterDPI
	x0 := int(math.Max(0, math.Min(float64(c.width), math.Floor((box.X-c.box.X)/step)-1)))
	y0 := int(math.Max(0, math.Min(float64(c.height), math.Floor((box.Y-c.box.Y)/step)-1)))
	x1 := int(math.Max(0, math.Min(float64(c.width), math.Ceil((box.X+box.W-c.box.X)/step)+1)))
	y1 := int(math.Max(0, math.Min(float64(c.height), math.Ceil((box.Y+box.H-c.box.Y)/step)+1)))
	return image.Rect(x0, y0, x1, y1)
}

// acquireCoverage 借用当前块的独占画布，保留活动画布供嵌套合成读取
// 返回: *image.RGBA 未清空画布，不支持缓冲绘制的后端返回nil, error 无效绘制区域
func (c *pdfCompositor) acquireCoverage() (*image.RGBA, error) {
	return c.acquireCoverageBox(c.box)
}

// acquireCoverageBox 按目标页面范围借用紧致尺寸的独占覆盖画布，复用底层字节缓冲
// 入参: box 页面绘制范围
// 返回: *image.RGBA 未清空画布，不支持缓冲绘制的后端返回nil, error 无效绘制区域
func (c *pdfCompositor) acquireCoverageBox(box Box) (*image.RGBA, error) {
	if _, ok := c.importer.editor.Backends().Raster.(RasterBufferBackend); !ok {
		return nil, nil
	}
	page := RasterPage{Width: box.W, Height: box.H, DPI: c.importer.rasterDPI}
	w, h, err := page.PixelSize()
	if err != nil {
		return nil, err
	}
	if c.scratch == nil {
		c.scratch = &pdfCompositeScratch{}
	}
	need := 4 * w * h
	selected := -1
	for i, pix := range c.scratch.subRasters {
		if cap(pix) >= need && (selected < 0 || cap(pix) < cap(c.scratch.subRasters[selected])) {
			selected = i
		}
	}
	var pix []byte
	if selected >= 0 {
		pix = c.scratch.subRasters[selected][:need]
		c.scratch.subRasters[selected] = nil
	} else {
		pix = make([]byte, need)
	}
	return &image.RGBA{Pix: pix, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}, nil
}

// releaseCoverage 归还当前调用不再读取的画布，限制单次合成保留的内存
// 入参: coverage 当前调用的覆盖图像
func (c *pdfCompositor) releaseCoverage(coverage image.Image) {
	if _, ok := c.importer.editor.Backends().Raster.(RasterBufferBackend); !ok || c.scratch == nil {
		return
	}
	if buffer, ok := coverage.(*image.RGBA); ok && buffer != nil && cap(buffer.Pix) <= 4*pdfCompositeTileSize*pdfCompositeTileSize {
		for i, stored := range c.scratch.subRasters {
			if stored == nil {
				c.scratch.subRasters[i] = buffer.Pix
				return
			}
		}
	}
}

// acquirePixels 为当前合成器借用独立像素缓冲
// 入参: count 所需像素数量
// 返回: []pdfCompositePixel 尚未初始化的独占缓冲
func (c *pdfCompositor) acquirePixels(count int) []pdfCompositePixel {
	if c.scratch == nil {
		c.scratch = &pdfCompositeScratch{}
	}
	return c.scratch.pixels.Acquire(count)
}

// releasePixels 归还当前调用已不再访问的像素缓冲
// 入参: pixels 当前调用借用的缓冲
func (c *pdfCompositor) releasePixels(pixels []pdfCompositePixel) {
	if c.scratch != nil {
		c.scratch.pixels.Release(pixels)
	}
}

// acquireChannels 借用图案通道标记，仅在设备函数需要时分配
// 入参: count 当前块像素数
// 返回: []uint8 尚未初始化的独占通道缓冲
func (c *pdfCompositor) acquireChannels(count int) []uint8 {
	if c.scratch == nil {
		c.scratch = &pdfCompositeScratch{}
	}
	return c.scratch.channels.Acquire(count)
}

// releaseChannels 归还不再参与嵌套合成的通道标记
// 入参: channels 当前调用借用的通道缓冲
func (c *pdfCompositor) releaseChannels(channels []uint8) {
	if c.scratch != nil {
		c.scratch.channels.Release(channels)
	}
}

// releaseMasks 在当前块绘制结束后归还蒙版与复杂裁剪掩码，不复用其他块的坐标缓存
func (c *pdfCompositor) releaseMasks() {
	if c.scratch != nil {
		for _, mask := range c.masks {
			c.scratch.masks.Release(mask)
		}
	}
	clear(c.masks)
	for _, mask := range c.clipMasks {
		if mask != nil {
			c.releaseCoverage(mask)
		}
	}
	clear(c.clipMasks)
}

// geometryCache 限制完整覆盖场景的保留量，边界独立保存以便排除无交集图元
// 返回: *renderCache[pdfCompositeSceneKey, pdfCompositeGeometry] 单页场景缓存
func (c *pdfCompositeCache) geometryCache() *renderCache[pdfCompositeSceneKey, pdfCompositeGeometry] {
	if c.geometry == nil {
		c.geometry = resourceCache[pdfCompositeSceneKey, pdfCompositeGeometry](c.resources, 128<<20)
	}
	return c.geometry
}

// clipCache 限制页面及嵌套图案共享的只读裁剪轮廓
// 返回: *renderCache[[32]byte, *pdfCompositeClip] 裁剪缓存
func (c *pdfCompositeCache) clipCache() *renderCache[[32]byte, *pdfCompositeClip] {
	if c.clips == nil {
		c.clips = resourceCache[[32]byte, *pdfCompositeClip](c.resources, 32<<20)
	}
	return c.clips
}

// compositingCache 按需创建单页合成缓存，页面切换时由导入器释放
// 返回: *pdfCompositeCache 当前页面缓存
func (p *pdfImporter) compositingCache() *pdfCompositeCache {
	if p.compositeCache == nil {
		p.compositeCache = &pdfCompositeCache{resources: p.resources, images: map[*pdfgo.Image]*pdfgo.ImageComponents{}, masks: map[*pdfgo.SoftMask][]pdfCompositeNode{}, meshes: map[*pdfgo.MeshGradient]*codecpdf.MeshPrepared{}}
	}
	return p.compositeCache
}

// compositeTextBounds 按字体描述符计算保守边界，仅用于排除不相交的外部字体
// 入参: mark 具有字体边界的文字, stroke 是否描边
// 返回: Box 包含描边和抗锯齿边缘的页面范围
func (p *pdfImporter) compositeTextBounds(mark pdfgo.TextMark, stroke bool) Box {
	b := mark.Font.BoundingBox
	matrix := p.matrix.Mul(mark.Matrix)
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, origin := range mark.Positions {
		for _, point := range [4]pdfgo.Point{{X: b.XMin, Y: b.YMin}, {X: b.XMax, Y: b.YMin}, {X: b.XMax, Y: b.YMax}, {X: b.XMin, Y: b.YMax}} {
			point = matrix.Apply(pdfgo.Point{X: origin.X + point.X*mark.Size*mark.HorizontalScale/1000, Y: origin.Y + point.Y*mark.Size/1000})
			minX, minY = math.Min(minX, point.X), math.Min(minY, point.Y)
			maxX, maxY = math.Max(maxX, point.X), math.Max(maxY, point.Y)
		}
	}
	box := Box{}
	if len(mark.Positions) != 0 {
		box = Box{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
	}
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

// collectCompositeNodes 收集原始绘制顺序并展开Type3字形，保留透明组层级
// 入参: walk 内容访问函数, widget 是否为表单外观
// 返回: []pdfCompositeNode 原始图元, error 解析错误
func (p *pdfImporter) collectCompositeNodes(walk func(pdfgo.Visitor) error, widget bool) ([]pdfCompositeNode, error) {
	return p.collectNodes(walk, widget, false)
}

// collectNodes 按模式收集原始图元，背景按需收集模式跳过告警、结构观察及文本替换
// 入参: walk 内容访问函数, widget 是否为表单外观, backdrop 是否仅用于背景合成
// 返回: []pdfCompositeNode 原始图元, error 解析错误
func (p *pdfImporter) collectNodes(walk func(pdfgo.Visitor) error, widget, backdrop bool) ([]pdfCompositeNode, error) {
	active := make(map[*pdfgo.Font]map[string]bool)
	var seenFonts map[*pdfgo.Font]bool
	var externalNames map[string]bool
	var embeddedFonts map[*pdfgo.Font]bool
	var textMarks []pdfgo.TextMark
	replacements := pdfTextReplacements{importer: p, widget: widget}
	warning := p.warning
	if backdrop {
		warning = func(pdfgo.Diagnostic) {}
	}
	storeText := func(mark pdfgo.TextMark) *pdfgo.TextMark {
		if backdrop {
			mark.Replacement = nil
		}
		if len(textMarks) == cap(textMarks) {
			textMarks = make([]pdfgo.TextMark, 0, min(64, max(8, cap(textMarks)*2)))
		}
		textMarks = append(textMarks, mark)
		return &textMarks[len(textMarks)-1]
	}
	var collect func(func(pdfgo.Visitor) error, *pdfgo.ReplacementText) ([]pdfCompositeNode, error)
	collect = func(walk func(pdfgo.Visitor) error, replacement *pdfgo.ReplacementText) ([]pdfCompositeNode, error) {
		var nodes []pdfCompositeNode
		v := p.newBaseVisitor()
		if backdrop {
			v.Warning = warning
			v.PostScript = nil
		}
		if !backdrop {
			v.MarkedContent = func(mark pdfgo.MarkedContentMark) error {
				if mark.Replacement != nil {
					if mark.Operator == "EMC" {
						replacement = mark.Replacement.Parent
					} else {
						replacement = mark.Replacement
						if !mark.Hidden {
							span := replacements.span(replacement)
							if replacement.Parent == nil {
								span.offset = mark.Offset
							}
						}
					}
				}
				return replacements.markedContent(mark)
			}
		}
		v.Path = func(mark pdfgo.PathMark) error {
			if !backdrop {
				replacements.block(replacement)
				if err := p.halftone(mark.Style); err != nil {
					return err
				}
			}
			nodes = append(nodes, pdfCompositeNode{path: &mark})
			return nil
		}
		v.Text = func(mark pdfgo.TextMark) error {
			if !backdrop {
				replacements.span(mark.Replacement)
				if mark.Font.Subtype != "Type3" && !seenFonts[mark.Font] {
					if seenFonts == nil {
						seenFonts = make(map[*pdfgo.Font]bool)
					}
					seenFonts[mark.Font] = true
					if len(mark.Font.Program) == 0 {
						if externalNames == nil {
							externalNames = make(map[string]bool)
						}
						name := codecpdf.EmbeddedFontName(mark.Font.Name)
						if !externalNames[basefont.NormalizeName(name)] {
							for _, candidate := range basefont.ExactCandidateNames(name) {
								externalNames[basefont.NormalizeName(candidate)] = true
							}
						}
					} else {
						if embeddedFonts == nil {
							embeddedFonts = make(map[*pdfgo.Font]bool)
						}
						embeddedFonts[mark.Font] = true
					}
				}
			}
			text := &nodes
			if mark.Object != nil && mark.Object.Knockout {
				if len(nodes) == 0 || nodes[len(nodes)-1].textObject != mark.Object {
					nodes = append(nodes, pdfCompositeNode{group: &pdfgo.GroupMark{Alpha: 1, Knockout: true}, textObject: mark.Object})
				}
				text = &nodes[len(nodes)-1].children
			}
			if mark.Font.Subtype != "Type3" {
				if !backdrop {
					if err := p.halftone(mark.Style); err != nil {
						return err
					}
				}
				if len(mark.Glyphs) > 1 && (mark.Style.AlphaIsShape || mark.Style.Fill.Alpha != 1 || mark.Style.Stroke.Alpha != 1 || mark.Style.SoftMask != nil || !codecpdf.NormalBlend(mark.Style.BlendMode) || mark.Mode%4 == 1 || mark.Mode%4 == 2) {
					for i := range mark.Glyphs {
						glyph := mark
						glyph.Glyphs, glyph.Positions, glyph.Clip = mark.Glyphs[i:i+1], mark.Positions[i:i+1], nil
						*text = append(*text, pdfCompositeNode{text: storeText(glyph)})
					}
				} else {
					*text = append(*text, pdfCompositeNode{text: storeText(mark)})
				}
			} else {
				glyphs := active[mark.Font]
				if glyphs == nil {
					glyphs = make(map[string]bool)
					active[mark.Font] = glyphs
				}
				for index, glyph := range mark.Glyphs {
					if glyphs[glyph.Name] {
						return fmt.Errorf("recursive PDF Type3 glyph %q", glyph.Name)
					}
					glyphs[glyph.Name] = true
					source := mark
					if !backdrop {
						replacements.block(mark.Replacement)
					}
					children, err := collect(func(visitor pdfgo.Visitor) error { return p.reader.WalkType3Glyph(p.ctx, source, index, visitor) }, replacement)
					delete(glyphs, glyph.Name)
					if err != nil {
						return err
					}
					part := mark
					part.Glyphs, part.Positions = mark.Glyphs[index:index+1], mark.Positions[index:index+1]
					*text = append(*text, pdfCompositeNode{group: &pdfgo.GroupMark{Alpha: 1}, children: children, glyph: &part})
				}
			}
			return nil
		}
		v.Image = func(mark pdfgo.ImageMark) error {
			if !backdrop {
				replacements.block(replacement)
				if err := p.halftone(mark.Style); err != nil {
					return err
				}
			}
			nodes = append(nodes, pdfCompositeNode{image: &mark})
			return nil
		}
		v.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
			children, err := collect(walk, replacement)
			if err != nil {
				return err
			}
			nodes = append(nodes, pdfCompositeNode{group: &mark, children: children})
			return nil
		}
		v.Form = func(mark pdfgo.FormMark, walk func(pdfgo.Visitor) error) error {
			children, err := collect(walk, replacement)
			if err != nil {
				return err
			}
			if len(children) != 0 {
				formMark := mark
				children[0].forms = append([]pdfFormSpan{{mark: &formMark, count: len(children)}}, children[0].forms...)
				nodes = append(nodes, children...)
			}
			return nil
		}
		err := walk(v)
		return nodes, err
	}
	nodes, err := collect(walk, nil)
	if err != nil {
		return nil, err
	}
	if backdrop {
		return nodes, nil
	}
	nodes, err = replacements.apply(nodes)
	if err != nil {
		return nil, err
	}
	for font := range embeddedFonts {
		if !externalNames[basefont.NormalizeName(codecpdf.EmbeddedFontName(font.Name))] {
			continue
		}
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := p.importedFont(font); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// ensureCompositeBackdrop 在并行或重复页注解需要与正文背景混合时按需收集当前页正文图元
// 返回: error 遍历或取消错误
func (p *pdfImporter) ensureCompositeBackdrop() error {
	if p.compositeBackdropPage == nil || p.compositeBackdropLoaded {
		return nil
	}
	collector := *p
	collector.warning = func(pdfgo.Diagnostic) {}
	collector.structureBindings = nil
	collector.objects = nil
	collector.report = PDFImportReport{}
	collector.resetWarnings()
	defer func() {
		if collector.references != nil && collector.references != p.references {
			if p.references == nil {
				p.references = collector.references
			} else {
				collector.references.Close()
			}
		}
		p.usedReferences = p.usedReferences || collector.usedReferences
	}()
	nodes, err := collector.collectNodes(func(visitor pdfgo.Visitor) error {
		return collector.reader.WalkPage(collector.ctx, p.compositeBackdropPage, visitor)
	}, false, true)
	if err != nil {
		return err
	}
	if len(nodes) == 1 && nodes[0].group != nil && nodes[0].group.Page && !nodes[0].group.Knockout {
		nodes = nodes[0].children
	}
	p.compositeBackdropLoaded = true
	p.compositeBackdropNodes = nodes
	if len(nodes) != 0 {
		combined := make([]pdfCompositeNode, 0, len(nodes)+len(p.compositeNodes))
		combined = append(combined, nodes...)
		combined = append(combined, p.compositeNodes...)
		p.compositeNodes = combined
	}
	return nil
}

// compositePage 保留独立不透明对象，仅将需要背景参与的效果在局部区域合成
// 入参: space 页面混合空间, walk 页面内容
// 返回: error 转换错误
func (p *pdfImporter) compositePage(space *pdfgo.ColorSpace, walk func(pdfgo.Visitor) error) error {
	nodes, err := p.collectCompositeNodes(walk, false)
	if err != nil {
		return err
	}
	if len(nodes) == 1 && nodes[0].group != nil && nodes[0].group.Page {
		space = nodes[0].group.ColorSpace
		if !nodes[0].group.Knockout {
			nodes = nodes[0].children
		}
	}
	if space == nil {
		space = &pdfgo.ColorSpace{Model: "DeviceRGB"}
		found, err := p.processOverprint(nodes)
		if err != nil {
			return err
		}
		if found {
			space = &pdfgo.ColorSpace{Model: "DeviceCMYK"}
		}
	}
	p.compositeNodes = nil
	p.transferBackdrop = false
	p.compositeSpace = space
	device, err := p.transferDevice(space, nodes)
	if err != nil {
		return err
	}
	p.transferModel = device
	hasTransfer, err := p.compositeHasTransfer(nodes, device)
	if err != nil {
		return err
	}
	if hasTransfer {
		if p.warning == nil {
			return &pdfgo.UnsupportedError{Feature: "device transfer function rasterization"}
		}
		if err := p.compositeRegion(nil, pdfCompositeNode{group: &pdfgo.GroupMark{Alpha: 1, ColorSpace: space}, children: nodes}, space, true); err != nil {
			return err
		}
		p.compositeNodes = nodes
		p.transferBackdrop = true
		return nil
	}
	return p.compositeObjects(nodes)
}

// directTextMark 提取可参与同行合并的直通文本图元，兼容逐字文本对象隐式组
// 入参: node 合成节点
// 返回: *pdfgo.TextMark 文本图元, bool 是否可提取
func directTextMark(node pdfCompositeNode) (*pdfgo.TextMark, bool) {
	if len(node.forms) != 0 {
		return nil, false
	}
	if node.text != nil && node.group == nil {
		return node.text, true
	}
	if node.textObject != nil && node.group != nil && node.group.Alpha == 1 && node.group.SoftMask == nil && node.group.ColorSpace == nil && codecpdf.NormalBlend(node.group.BlendMode) && len(node.children) == 1 {
		child := node.children[0]
		if child.text != nil && child.group == nil && len(child.forms) == 0 && (!node.group.Knockout || len(child.text.Glyphs) == 1 || canMergeDirectTextMark(child.text)) {
			return child.text, true
		}
	}
	return nil, false
}

// pdfSameColorSpace 判断两个颜色空间指针是否等价
// 入参: a 首空间, b 次空间
// 返回: bool 是否等价
func pdfSameColorSpace(a, b *pdfgo.ColorSpace) bool {
	return a == b || a != nil && b != nil && a.Equal(b)
}

// pdfSameTextStyle 判断两个直通纯色文本图元的绘制样式是否完全等价
// 入参: a 首样式, b 后续样式, mode 文本渲染模式
// 返回: bool 是否完全等价
func pdfSameTextStyle(a, b pdfgo.Style, mode int) bool {
	if a.BlendMode != b.BlendMode || a.AlphaIsShape != b.AlphaIsShape || a.FillOverprint != b.FillOverprint || a.StrokeOverprint != b.StrokeOverprint || a.RenderingIntent != b.RenderingIntent || a.ColorConversion != b.ColorConversion || a.SoftMask != b.SoftMask || a.Transfer != b.Transfer || a.Halftone != b.Halftone || a.Fill.RGB != b.Fill.RGB || a.Fill.Alpha != b.Fill.Alpha || !pdfSameColorSpace(a.Fill.Space, b.Fill.Space) || a.Fill.Values != b.Fill.Values || a.Fill.None != b.Fill.None || len(a.Clips) != len(b.Clips) {
		return false
	}
	if mode == 2 {
		if a.LineWidth != b.LineWidth || a.Cap != b.Cap || a.Join != b.Join || a.MiterLimit != b.MiterLimit || a.DashPhase != b.DashPhase || a.StrokeAdjust != b.StrokeAdjust || !slices.Equal(a.Dash, b.Dash) || a.Stroke.RGB != b.Stroke.RGB || a.Stroke.Alpha != b.Stroke.Alpha || !pdfSameColorSpace(a.Stroke.Space, b.Stroke.Space) || a.Stroke.Values != b.Stroke.Values || a.Stroke.None != b.Stroke.None || a.Stroke.CMYK != b.Stroke.CMYK || a.Stroke.Colorant != b.Stroke.Colorant || a.Stroke.Process != b.Stroke.Process || a.Stroke.Tiling != nil || b.Stroke.Tiling != nil || a.Stroke.Axial != nil || b.Stroke.Axial != nil || a.Stroke.Radial != nil || b.Stroke.Radial != nil || a.Stroke.Mesh != nil || b.Stroke.Mesh != nil || a.Stroke.Function != nil || b.Stroke.Function != nil || a.Stroke.Shading != nil || b.Stroke.Shading != nil {
			return false
		}
	}
	if len(a.Clips) == 0 && a.Fill.CMYK == b.Fill.CMYK && a.Fill.Colorant == b.Fill.Colorant && a.Fill.Process == b.Fill.Process && a.Fill.Tiling == nil && b.Fill.Tiling == nil && a.Fill.Axial == nil && b.Fill.Axial == nil && a.Fill.Radial == nil && b.Fill.Radial == nil && a.Fill.Mesh == nil && b.Fill.Mesh == nil && a.Fill.Function == nil && b.Fill.Function == nil && a.Fill.Shading == nil && b.Fill.Shading == nil {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// pdfGlyphAdvance 估算字形在文本坐标系中的水平前进宽
// 入参: glyph 字形, size 字号, hscale 水平缩放
// 返回: float64 文本坐标系前进宽
func pdfGlyphAdvance(glyph pdfgo.Glyph, size, hscale float64) float64 {
	if finite(glyph.Width) && glyph.Width > 0 {
		return glyph.Width / 1000 * size * hscale
	}
	return 0.5 * size * hscale
}

// canMergeDirectTextMark 检查单个直通文本图元是否具备同行合并前提
// 入参: mark 文本图元
// 返回: bool 是否允许参与同行合并
func canMergeDirectTextMark(mark *pdfgo.TextMark) bool {
	if mark == nil || mark.Font == nil || mark.Font.Subtype == "Type3" || mark.Size <= 0 || mark.HorizontalScale <= 0 || mark.Clip != nil || mark.Replacement != nil || (mark.Mode != 0 && mark.Mode != 2 && mark.Mode != 3) || len(mark.Glyphs) == 0 || len(mark.Glyphs) != len(mark.Positions) {
		return false
	}
	s := mark.Style
	if s.SoftMask != nil || s.Transfer != nil || s.Halftone != nil || s.AlphaIsShape || s.FillOverprint || !codecpdf.NormalBlend(s.BlendMode) {
		return false
	}
	f := s.Fill
	if mark.Mode != 3 && f.Alpha != 1 {
		return false
	}
	if f.Tiling != nil || f.Axial != nil || f.Radial != nil || f.Mesh != nil || f.Function != nil || f.Shading != nil {
		return false
	}
	if mark.Mode == 2 {
		st := s.Stroke
		if s.StrokeOverprint || st.Alpha != 1 || st.Tiling != nil || st.Axial != nil || st.Radial != nil || st.Mesh != nil || st.Function != nil || st.Shading != nil {
			return false
		}
	}
	return true
}

// mergeDirectTextNodes 将严格相邻、同样式且同行紧邻的直通文本图元合并为单个TextMark
// 入参: nodes 待转换图元列表, start 起始索引, first 起始直通文本图元, hasStructure 是否启用逻辑结构绑定, allow 可选后续节点直通校验
// 返回: pdfgo.TextMark 合并结果, int 消费到的结束索引, bool 是否发生合并
func mergeDirectTextNodes(nodes []pdfCompositeNode, start int, first *pdfgo.TextMark, hasStructure bool, allow func(pdfCompositeNode) bool) (pdfgo.TextMark, int, bool) {
	if !canMergeDirectTextMark(first) || start+1 >= len(nodes) {
		return pdfgo.TextMark{}, start + 1, false
	}
	inv0, ok := first.Matrix.Inverse()
	if !ok {
		return pdfgo.TextMark{}, start + 1, false
	}
	lastPos := first.Positions[len(first.Positions)-1]
	prevX, prevY := lastPos.X, lastPos.Y
	baseY := first.Positions[0].Y
	prevGlyph := first.Glyphs[len(first.Glyphs)-1]
	yTol := 0.05 * first.Size
	ySnap := 1e-4 * first.Size
	minGap, maxGap := -0.25*first.Size, 3*first.Size
	end := start + 1
	totalGlyphs := len(first.Glyphs)
	for end < len(nodes) {
		if allow != nil && !allow(nodes[end]) {
			break
		}
		m, ok := directTextMark(nodes[end])
		if !ok {
			break
		}
		if m.Font != first.Font || m.Size != first.Size || m.HorizontalScale != first.HorizontalScale || m.Mode != first.Mode || m.Clip != nil || m.Replacement != nil || len(m.Glyphs) == 0 || len(m.Glyphs) != len(m.Positions) {
			break
		}
		if m.Mode == 2 && m.StrokeMatrix != first.StrokeMatrix {
			break
		}
		if hasStructure && m.Sequence != first.Sequence {
			break
		}
		if m.Matrix[0] != first.Matrix[0] || m.Matrix[1] != first.Matrix[1] || m.Matrix[2] != first.Matrix[2] || m.Matrix[3] != first.Matrix[3] {
			break
		}
		if !pdfSameTextStyle(first.Style, m.Style, first.Mode) {
			break
		}
		dx, dy := m.Matrix[4]-first.Matrix[4], m.Matrix[5]-first.Matrix[5]
		ox, oy := inv0[0]*dx+inv0[2]*dy, inv0[1]*dx+inv0[3]*dy
		if !finite(ox) || !finite(oy) {
			break
		}
		firstPt := pdfgo.Point{X: m.Positions[0].X + ox, Y: m.Positions[0].Y + oy}
		if !finite(firstPt.X) || !finite(firstPt.Y) || math.Abs(firstPt.Y-prevY) > yTol || math.Abs(firstPt.Y-baseY) > yTol {
			break
		}
		gap := firstPt.X - (prevX + pdfGlyphAdvance(prevGlyph, first.Size, first.HorizontalScale))
		if gap < minGap || gap > maxGap {
			break
		}
		valid := true
		for _, pt := range m.Positions {
			if !finite(pt.X+ox) || !finite(pt.Y+oy) {
				valid = false
				break
			}
		}
		if !valid {
			break
		}
		tail := m.Positions[len(m.Positions)-1]
		prevX, prevY = tail.X+ox, tail.Y+oy
		prevGlyph = m.Glyphs[len(m.Glyphs)-1]
		totalGlyphs += len(m.Glyphs)
		end++
	}
	if end == start+1 {
		return pdfgo.TextMark{}, end, false
	}
	merged := *first
	merged.Glyphs = make([]pdfgo.Glyph, 0, totalGlyphs)
	merged.Positions = make([]pdfgo.Point, 0, totalGlyphs)
	merged.Glyphs = append(merged.Glyphs, first.Glyphs...)
	merged.Positions = append(merged.Positions, first.Positions...)
	for k := start + 1; k < end; k++ {
		m, _ := directTextMark(nodes[k])
		dx, dy := m.Matrix[4]-first.Matrix[4], m.Matrix[5]-first.Matrix[5]
		ox, oy := inv0[0]*dx+inv0[2]*dy, inv0[1]*dx+inv0[3]*dy
		for _, pt := range m.Positions {
			mapped := pdfgo.Point{X: pt.X + ox, Y: pt.Y + oy}
			if math.Abs(mapped.Y-baseY) <= ySnap {
				mapped.Y = baseY
			}
			merged.Positions = append(merged.Positions, mapped)
		}
		merged.Glyphs = append(merged.Glyphs, m.Glyphs...)
	}
	return merged, end, true
}

// emitDirectNodes 输出已确认直通的节点列表，并合并同层相邻同行同样式文本图元
// 入参: nodes 同层直通节点, visitor 目标访问器
// 返回: error 转换错误
func (p *pdfImporter) emitDirectNodes(nodes []pdfCompositeNode, visitor pdfgo.Visitor) error {
	hasStructure := p.structureBindings != nil
	for i := 0; i < len(nodes); i++ {
		node := nodes[i]
		if first, ok := directTextMark(node); ok {
			if merged, end, ok := mergeDirectTextNodes(nodes, i, first, hasStructure, nil); ok {
				if err := visitor.Text(merged); err != nil {
					return err
				}
				i = end - 1
				continue
			}
		}
		if node.textObject != nil || node.glyph != nil {
			if err := p.emitDirectNodes(node.children, visitor); err != nil {
				return err
			}
			continue
		}
		if node.group != nil {
			if err := visitor.Group(*node.group, func(v pdfgo.Visitor) error {
				return p.emitDirectNodes(node.children, v)
			}); err != nil {
				return err
			}
			continue
		}
		if err := node.emit(visitor); err != nil {
			return err
		}
	}
	return nil
}

// independentFromBackdrop 判断单个非直通图元能否在透明底上独立合成，无需重绘前序背景
// 入参: node 当前图元, space 页面混合空间
// 返回: bool 是否与前序背景无关
func (p *pdfImporter) independentFromBackdrop(node pdfCompositeNode, space *pdfgo.ColorSpace) bool {
	if !space.SRGBEquivalent() || node.group != nil || node.image != nil {
		return false
	}
	s, fill, stroke := node.style()
	if !fill || !stroke || s.SoftMask != nil || s.AlphaIsShape || s.FillOverprint || s.StrokeOverprint || s.Transfer != nil || s.Halftone != nil || !codecpdf.NormalBlend(s.BlendMode) {
		return false
	}
	return s.Fill.Tiling == nil && s.Stroke.Tiling == nil && s.Fill.Shading == nil && s.Stroke.Shading == nil
}

// compositeObjects 按绘制顺序转换图元并保留后续混合所需的背景
// 入参: nodes 待转换图元
// 返回: error 转换或合成错误
func (p *pdfImporter) compositeObjects(nodes []pdfCompositeNode) error {
	space := p.compositeSpace
	if space == nil {
		space = &pdfgo.ColorSpace{Model: "DeviceRGB"}
	}
	previousLocalForms := p.localFormCounts
	previousNeighborForms := p.neighborFormRefs
	p.localFormCounts = p.countLocalForms(nodes)
	p.recordPageForms(p.localFormCounts)
	if previousNeighborForms == nil && p.formRefCounts == nil && !p.hasReferenceDocuments() && len(p.pageOrder) >= 2 && len(p.localFormCounts) != 0 {
		p.neighborFormRefs = p.probeNeighborFormRefs(p.localFormCounts)
	}
	defer func() {
		p.localFormCounts = previousLocalForms
		p.neighborFormRefs = previousNeighborForms
	}()
	for i := 0; i < len(nodes); i++ {
		node := nodes[i]
		if len(node.forms) > 0 && !p.transferBackdrop {
			packed := false
			for _, span := range node.forms {
				if span.count <= 0 || span.count > len(nodes)-i || !p.shouldReuseForm(span.mark, span.count) {
					continue
				}
				ok, err := p.compositeForm(span.mark, nodes[i:i+span.count])
				if err != nil {
					return fmt.Errorf("convert form at graphic %d: %w", i+1, err)
				}
				if ok {
					p.compositeNodes = append(p.compositeNodes, nodes[i:i+span.count]...)
					i += span.count - 1
					packed = true
					break
				}
			}
			if packed {
				continue
			}
		}
		if node.group != nil && !node.group.Page && (node.group.Ref != (pdfgo.Reference{}) || node.group.Stream != nil) && !p.transferBackdrop && p.shouldReuseGroupForm(node.group, len(node.children)) {
			packed, err := p.compositeGroupForm(node)
			if err != nil {
				return fmt.Errorf("convert group form at graphic %d: %w", i+1, err)
			}
			if packed {
				p.compositeNodes = append(p.compositeNodes, node)
				continue
			}
		}
		direct, err := p.directCompositeNode(node, space)
		if err != nil {
			return fmt.Errorf("inspect graphic %d: %w", i+1, err)
		}
		device := p.transferModel
		if device == "" {
			device = "DeviceRGB"
		}
		transfer, err := p.compositeHasTransfer([]pdfCompositeNode{node}, device)
		if err != nil {
			return err
		}
		if p.transferBackdrop || transfer {
			direct = false
		}
		if direct {
			if first, ok := directTextMark(node); ok {
				allowNext := func(next pdfCompositeNode) bool {
					nextDirect, err := p.directCompositeNode(next, space)
					if err != nil || !nextDirect {
						return false
					}
					nextTransfer, err := p.compositeHasTransfer([]pdfCompositeNode{next}, device)
					return err == nil && !nextTransfer
				}
				if merged, end, ok := mergeDirectTextNodes(nodes, i, first, p.structureBindings != nil, allowNext); ok {
					if err := p.visitor().Text(merged); err != nil {
						return fmt.Errorf("convert graphic %d: %w", i+1, err)
					}
					stored := merged
					p.compositeNodes = append(p.compositeNodes, pdfCompositeNode{text: &stored})
					i = end - 1
					continue
				}
			}
			if err := p.emitDirectNodes([]pdfCompositeNode{node}, p.visitor()); err != nil {
				return fmt.Errorf("convert graphic %d: %w", i+1, err)
			}
		} else {
			if p.warning == nil {
				return &pdfgo.UnsupportedError{Feature: "page color-space compositing"}
			}
			if !p.transferBackdrop && !transfer && p.independentFromBackdrop(node, space) {
				if err := p.compositeRegion(nil, node, space, false); err != nil {
					return fmt.Errorf("composite graphic %d: %w", i+1, err)
				}
			} else {
				if err := p.ensureCompositeBackdrop(); err != nil {
					return fmt.Errorf("composite graphic %d: %w", i+1, err)
				}
				if err := p.compositeRegion(p.compositeNodes, node, space, true); err != nil {
					return fmt.Errorf("composite graphic %d: %w", i+1, err)
				}
			}
		}
		p.compositeNodes = append(p.compositeNodes, node)
		p.transferBackdrop = p.transferBackdrop || transfer
	}
	return p.ctx.Err()
}

// opaque 检查图元是否无需与页面背景混合
// 入参: n 待检查图元, space 当前混合空间
// 返回: bool 是否可直接保留, error 遮罩解析或取消错误
func (p *pdfImporter) opaque(n pdfCompositeNode, space *pdfgo.ColorSpace) (bool, error) {
	if err := p.ctx.Err(); err != nil {
		return false, err
	}
	if n.group != nil {
		g := n.group
		if g.Knockout && n.textObject == nil || g.Alpha != 1 || g.SoftMask != nil || !codecpdf.NormalBlend(g.BlendMode) || g.ColorSpace != nil && !g.ColorSpace.Equal(space) {
			return false, nil
		}
		for _, child := range n.children {
			if opaque, err := p.opaque(child, space); err != nil || !opaque {
				return false, err
			}
		}
		return true, nil
	}
	style, fill, stroke := n.style()
	if n.image != nil && style.FillOverprint {
		return false, nil
	}
	if style.SoftMask != nil || !codecpdf.NormalBlend(style.BlendMode) || fill && style.Fill.Alpha != 1 || stroke && style.Stroke.Alpha != 1 {
		return false, nil
	}
	if fill && style.FillOverprint && codecpdf.OverprintNeedsSeparation(style.Fill) || stroke && style.StrokeOverprint && codecpdf.OverprintNeedsSeparation(style.Stroke) {
		return false, nil
	}
	if fill && p.gradientError(style.Fill) != nil || stroke && p.gradientError(style.Stroke) != nil {
		return false, p.ctx.Err()
	}
	if n.image != nil {
		masked, err := n.image.Image.HasMask()
		return !masked, err
	}
	return true, nil
}

// compositeRegionBounds 复用内置后端的图元覆盖，自定义后端保留整组度量
// 入参: node 当前效果
// 返回: Box 覆盖范围, error 编译或取消错误
func (p *pdfImporter) compositeRegionBounds(node pdfCompositeNode) (Box, error) {
	if err := p.ctx.Err(); err != nil {
		return Box{}, err
	}
	switch p.editor.Backends().Compiler.(type) {
	case DefaultBackend, *DefaultBackend, OFDCompiler, *OFDCompiler:
		geometry, err := p.renderer.Geometry()
		if err != nil {
			return Box{}, err
		}
		switch geometry.(type) {
		case DefaultBackend, *DefaultBackend:
			c := pdfCompositor{importer: p, cache: p.compositingCache()}
			return c.coverageBounds(node)
		}
	}
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
		return Box{}, err
	}
	return box, nil
}

// compositeRegion 按原始背景计算局部效果，不将组外文字和图形栅格化
// 入参: backdrop 前序图元, node 当前效果, space 页面混合空间, flatten 是否合并页面背景
// 返回: error 合成或保存错误
func (p *pdfImporter) compositeRegion(backdrop []pdfCompositeNode, node pdfCompositeNode, space *pdfgo.ColorSpace, flatten bool) error {
	box, err := p.compositeRegionBounds(node)
	if err != nil || box.W <= 0 || box.H <= 0 {
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
	if err := p.ctx.Err(); err != nil {
		return err
	}
	output := image.NewNRGBA(image.Rect(0, 0, w, h))
	cache := p.compositingCache()
	step := 25.4 / p.rasterDPI
	tilesX := (w + pdfCompositeTileSize - 1) / pdfCompositeTileSize
	tilesY := (h + pdfCompositeTileSize - 1) / pdfCompositeTileSize
	var filteredBackdrop []pdfCompositeNode
	var tileBuckets [][]pdfCompositeNode
	if len(backdrop) != 0 {
		boundsCalc := pdfCompositor{importer: p, cache: cache}
		useBuckets := tilesX*tilesY > 1
		if useBuckets {
			tileBuckets = make([][]pdfCompositeNode, tilesX*tilesY)
		}
		tileMM := float64(pdfCompositeTileSize) * step
		for _, prev := range backdrop {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			var prevBox Box
			if prev.group != nil {
				prevBox, err = boundsCalc.groupBounds(prev)
			} else {
				prevBox, err = boundsCalc.markBounds(prev)
			}
			if err != nil {
				return err
			}
			if prevBox.W <= 0 || prevBox.H <= 0 || prevBox.X >= box.X+box.W || prevBox.Y >= box.Y+box.H || prevBox.X+prevBox.W <= box.X || prevBox.Y+prevBox.H <= box.Y {
				continue
			}
			clippedX0 := math.Max(box.X, prevBox.X)
			clippedY0 := math.Max(box.Y, prevBox.Y)
			clippedX1 := math.Min(box.X+box.W, prevBox.X+prevBox.W)
			clippedY1 := math.Min(box.Y+box.H, prevBox.Y+prevBox.H)
			if clippedX1 <= clippedX0 || clippedY1 <= clippedY0 {
				continue
			}
			filteredBackdrop = append(filteredBackdrop, prev)
			if useBuckets {
				tx0 := max(0, min(tilesX-1, int(math.Floor((clippedX0-box.X-step)/tileMM))))
				ty0 := max(0, min(tilesY-1, int(math.Floor((clippedY0-box.Y-step)/tileMM))))
				tx1 := max(0, min(tilesX-1, int(math.Floor((clippedX1-box.X+step)/tileMM))))
				ty1 := max(0, min(tilesY-1, int(math.Floor((clippedY1-box.Y+step)/tileMM))))
				for ty := ty0; ty <= ty1; ty++ {
					rowOff := ty * tilesX
					for tx := tx0; tx <= tx1; tx++ {
						tileBuckets[rowOff+tx] = append(tileBuckets[rowOff+tx], prev)
					}
				}
			}
		}
	}
	buffer := make([]pdfCompositePixel, min(pdfCompositeTileSize, w)*min(pdfCompositeTileSize, h))
	scratch := &pdfCompositeScratch{}
	var transfers []*pdfgo.TransferFunction
	var previousTransfers []*pdfgo.TransferFunction
	device := p.transferModel
	if device == "" {
		device = "DeviceRGB"
	}
	hasTransfer := false
	for _, nodes := range [][]pdfCompositeNode{filteredBackdrop, {node}} {
		found, err := p.compositeHasTransfer(nodes, device)
		if err != nil {
			return err
		}
		hasTransfer = hasTransfer || found
	}
	if hasTransfer {
		transfers = make([]*pdfgo.TransferFunction, len(buffer))
		previousTransfers = make([]*pdfgo.TransferFunction, len(buffer))
	}
	var origins pdfOriginSet
	for ty, y := 0, 0; y < h; ty, y = ty+1, y+pdfCompositeTileSize {
		for tx, x := 0, 0; x < w; tx, x = tx+1, x+pdfCompositeTileSize {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			runtime.Gosched()
			width, height := min(pdfCompositeTileSize, w-x), min(pdfCompositeTileSize, h-y)
			c := pdfCompositor{importer: p, box: Box{X: box.X + float64(x)*step, Y: box.Y + float64(y)*step, W: float64(width) * step, H: float64(height) * step}, width: width, height: height, inverse: inverse, cache: cache, scratch: scratch, origins: &origins, masks: map[*pdfgo.SoftMask][]float64{}}
			if transfers != nil {
				c.transfers = transfers[:width*height]
				c.transferModel = device
				clear(c.transfers)
			}
			pixels := buffer[:width*height]
			clear(pixels)
			tileBackdrop := filteredBackdrop
			if tileBuckets != nil {
				tileBackdrop = tileBuckets[ty*tilesX+tx]
			}
			if len(tileBackdrop) != 0 {
				if err := c.draw(tileBackdrop, pixels, space); err != nil {
					c.releaseMasks()
					return err
				}
				for i := range pixels {
					pixels[i].Effect = 0
				}
			}
			copy(previousTransfers, c.transfers)
			if err := c.draw([]pdfCompositeNode{node}, pixels, space); err != nil {
				c.releaseMasks()
				return err
			}
			if err := compositeWriteTileOutput(p.ctx, pixels, output, x, y, width, height, space, flatten, c.transfers, previousTransfers, device); err != nil {
				c.releaseMasks()
				return err
			}
			c.releaseMasks()
		}
	}
	return p.appendRasterGroup(output, box, 1, origins.origin)
}

// compositeWriteTileOutput 将合成块像素转换到sRGB并写入输出图像，对非sRGB大块启用多行并行
// 入参: ctx 上下文, pixels 合成像素, output 输出图像, x 块左偏移, y 块上偏移, width 块宽, height 块高, space 混合空间, flatten 是否平铺白底, transfers 传递函数表, previousTransfers 背景传递函数表, device 设备空间名
// 返回: error 转换或取消错误
func compositeWriteTileOutput(ctx context.Context, pixels []pdfCompositePixel, output *image.NRGBA, x, y, width, height int, space *pdfgo.ColorSpace, flatten bool, transfers, previousTransfers []*pdfgo.TransferFunction, device pdfgo.Name) error {
	writeRows := func(y0, y1 int) error {
		var colorValues [4]float64
		var colorRGB [3]float64
		colorReady := false
		for ry := y0; ry < y1; ry++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			rowStart := ry * width
			pixRow := output.Pix[(y+ry)*output.Stride+x*4:]
			for rx := range width {
				i := rowStart + rx
				pixel := pixels[i]
				if pixel.Effect == 0 && (transfers == nil || transfers[i] == previousTransfers[i]) {
					continue
				}
				var rgb [3]float64
				var err error
				if transfers != nil && device == "DeviceCMYK" {
					values := pixel.Values
					if flatten {
						for j := range values {
							values[j] *= pixel.Alpha
						}
					}
					values, err = transfers[i].Apply(values[:], device, "")
					if err == nil {
						rgb, err = codecpdf.OutputColor(space, values)
					}
				} else if transfers != nil {
					rgb, err = space.SRGB(pixel.Values[:space.Components()], "RelativeColorimetric")
				} else {
					if colorReady && pixel.Values == colorValues {
						rgb = colorRGB
					} else {
						rgb, err = codecpdf.OutputColor(space, pixel.Values)
						if err == nil {
							colorValues, colorRGB, colorReady = pixel.Values, rgb, true
						}
					}
				}
				if err != nil {
					return err
				}
				alpha := pixel.Alpha
				if flatten {
					if transfers == nil || device != "DeviceCMYK" {
						for j := range rgb {
							rgb[j] = pixel.Alpha*rgb[j] + 1 - pixel.Alpha
						}
					}
					alpha = 1
				}
				if transfers != nil && device != "DeviceCMYK" && transfers[i] != nil {
					values, err := transfers[i].Apply(rgb[:], "DeviceRGB", "")
					if err != nil {
						return err
					}
					copy(rgb[:], values[:3])
				}
				off := rx * 4
				pixRow[off] = uint8(math.Round(rgb[0] * 255))
				pixRow[off+1] = uint8(math.Round(rgb[1] * 255))
				pixRow[off+2] = uint8(math.Round(rgb[2] * 255))
				pixRow[off+3] = uint8(math.Round(alpha * 255))
			}
		}
		return nil
	}
	workers := conversionWorkers(ctx)
	if workers > 1 && transfers == nil && !space.SRGBEquivalent() && height >= 32 && width*height >= 4096 {
		if workers > height/8 {
			workers = max(1, height/8)
		}
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for w := range workers {
			y0 := w * height / workers
			y1 := (w + 1) * height / workers
			wg.Add(1)
			go func(idx, startY, endY int) {
				defer wg.Done()
				errs[idx] = writeRows(startY, endY)
			}(w, y0, y1)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	}
	return writeRows(0, height)
}
