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
	"runtime"

	"github.com/xiaoqidun/pdfgo"
)

// pdfCompositeNode 保留局部合成所需的原始图元和透明组层级
type pdfCompositeNode struct {
	path       *pdfgo.PathMark
	text       *pdfgo.TextMark
	image      *pdfgo.ImageMark
	group      *pdfgo.GroupMark
	children   []pdfCompositeNode
	textObject *pdfgo.TextObject
	glyph      *pdfgo.TextMark
	formCount  int
}

// pdfCompositePixel 保存非预乘源空间分量及累计透明度
type pdfCompositePixel struct {
	values [4]float64
	alpha  float64
	effect float64
	shape  float64
}

// pdfCompositor 在指定局部区域中按源颜色空间合成PDF图元
type pdfCompositor struct {
	importer        *pdfImporter
	box             Box
	width, height   int
	inverse         pdfgo.Matrix
	cache           *pdfCompositeCache
	scratch         *pdfCompositeScratch
	masks           map[*pdfgo.SoftMask][]float64
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

// pdfCompositeGeometry 保存后端中立的绘制命令及其页面边界
type pdfCompositeGeometry struct {
	scene *RasterPage
	box   Box
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
	colorants  *pdfShadingColorants
	prepared   bool
	converted  bool
}

// pdfImageProcessKey 隔离图像的组空间及软蒙版采样，不复用错误的原生映射
type pdfImageProcessKey struct {
	image    *pdfgo.Image
	space    *pdfgo.ColorSpace
	softMask bool
}

// pdfCompositeCache 在单页内复用几何、图像分量及蒙版内容
type pdfCompositeCache struct {
	geometry        *renderCache[pdfCompositeSceneKey, pdfCompositeGeometry]
	clips           *renderCache[[32]byte, GeometryPath]
	bounds          map[pdfCompositeKey]Box
	images          map[*pdfgo.Image]*pdfgo.ImageComponents
	imageMatrix     map[*pdfgo.ImageMark]pdfgo.Matrix
	imageProcesses  map[pdfImageProcessKey]*pdfgo.ImageProcess
	masks           map[*pdfgo.SoftMask][]pdfCompositeNode
	meshes          map[*pdfgo.MeshGradient][]pdfMeshTriangle
	patterns        map[pdfgo.Paint]*pdfCompositePattern
	shadingPatterns map[*pdfgo.ShadingPattern][]pdfCompositeNode
	shadingSources  map[pdfShadingSourceKey][]pdfCompositeNode
	shadings        map[pdfGradientKey]pdfCompositeGeometry
	transfers       map[pdfTransferKey]*pdfgo.TransferFunction
	textDisjoint    map[*pdfgo.GroupMark]bool
	groups          map[*pdfgo.GroupMark]Box
}

// compositingCache 按需创建单页合成缓存，页面切换时由导入器释放
// 返回: *pdfCompositeCache 当前页面缓存
func (p *pdfImporter) compositingCache() *pdfCompositeCache {
	if p.compositeCache == nil {
		p.compositeCache = &pdfCompositeCache{images: map[*pdfgo.Image]*pdfgo.ImageComponents{}, masks: map[*pdfgo.SoftMask][]pdfCompositeNode{}, meshes: map[*pdfgo.MeshGradient][]pdfMeshTriangle{}}
	}
	return p.compositeCache
}

// geometryCache 限制完整覆盖场景的保留量，边界独立保存以便排除无交集图元
// 返回: *renderCache[pdfCompositeSceneKey, pdfCompositeGeometry] 单页场景缓存
func (c *pdfCompositeCache) geometryCache() *renderCache[pdfCompositeSceneKey, pdfCompositeGeometry] {
	if c.geometry == nil {
		c.geometry = &renderCache[pdfCompositeSceneKey, pdfCompositeGeometry]{limit: 64 << 20}
	}
	return c.geometry
}

// clipCache 限制页面及嵌套图案共享的只读裁剪轮廓
// 返回: *renderCache[[32]byte, GeometryPath] 裁剪缓存
func (c *pdfCompositeCache) clipCache() *renderCache[[32]byte, GeometryPath] {
	if c.clips == nil {
		c.clips = &renderCache[[32]byte, GeometryPath]{limit: 8 << 20}
	}
	return c.clips
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
	target, err := c.acquireCoverage()
	if err != nil {
		return nil, err
	}
	coverage, err := c.importer.renderGroupSceneInto(geometry.scene, c.box, target)
	if err != nil {
		c.releaseCoverage(target)
	}
	return coverage, err
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
		geometry.scene, geometry.box, err = c.importer.compileCoverage(node, stroke)
		if err != nil {
			return geometry, err
		}
		if c.cache.bounds == nil {
			c.cache.bounds = make(map[pdfCompositeKey]Box)
		}
		c.cache.bounds[key] = geometry.box
		cost := 256
		if geometry.scene != nil {
			for _, command := range geometry.scene.Commands {
				part := rasterCommandCost(command)
				if part <= 0 || part > cache.limit-cost {
					cost = cache.limit + 1
					break
				}
				cost += part
			}
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
// 入参: walk 内容访问函数
// 返回: []pdfCompositeNode 原始图元, error 解析错误
func (p *pdfImporter) collectCompositeNodes(walk func(pdfgo.Visitor) error) ([]pdfCompositeNode, error) {
	active := make(map[*pdfgo.Font]map[string]bool)
	var seenFonts map[*pdfgo.Font]bool
	var externalNames map[string]bool
	var embeddedFonts map[*pdfgo.Font]bool
	var textMarks []pdfgo.TextMark
	storeText := func(mark pdfgo.TextMark) *pdfgo.TextMark {
		if len(textMarks) == cap(textMarks) {
			textMarks = make([]pdfgo.TextMark, 0, min(64, max(8, cap(textMarks)*2)))
		}
		textMarks = append(textMarks, mark)
		return &textMarks[len(textMarks)-1]
	}
	var collect func(func(pdfgo.Visitor) error) ([]pdfCompositeNode, error)
	collect = func(walk func(pdfgo.Visitor) error) ([]pdfCompositeNode, error) {
		var nodes []pdfCompositeNode
		v := pdfgo.Visitor{Warning: p.warning, Reference: p.referencePage, Halftones: p.halftones}
		v.Path = func(mark pdfgo.PathMark) error {
			if err := p.halftone(mark.Style); err != nil {
				return err
			}
			nodes = append(nodes, pdfCompositeNode{path: &mark})
			return nil
		}
		v.Text = func(mark pdfgo.TextMark) error {
			if mark.Font.Subtype != "Type3" && !seenFonts[mark.Font] {
				if seenFonts == nil {
					seenFonts = make(map[*pdfgo.Font]bool)
				}
				seenFonts[mark.Font] = true
				if len(mark.Font.Program) == 0 {
					if externalNames == nil {
						externalNames = make(map[string]bool)
					}
					name := pdfEmbeddedFontName(mark.Font.Name)
					if !externalNames[fontNormalizeName(name)] {
						for _, candidate := range fontExactCandidateNames(name) {
							externalNames[fontNormalizeName(candidate)] = true
						}
					}
				} else {
					if embeddedFonts == nil {
						embeddedFonts = make(map[*pdfgo.Font]bool)
					}
					embeddedFonts[mark.Font] = true
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
				if err := p.halftone(mark.Style); err != nil {
					return err
				}
				if len(mark.Glyphs) > 1 && (mark.Object != nil && !mark.Object.Knockout || mark.Style.AlphaIsShape || mark.Style.Fill.Alpha != 1 || mark.Style.Stroke.Alpha != 1 || mark.Style.SoftMask != nil || !pdfNormalBlend(mark.Style.BlendMode) || mark.Mode%4 == 1 || mark.Mode%4 == 2) {
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
					children, err := collect(func(visitor pdfgo.Visitor) error { return p.reader.WalkType3Glyph(p.ctx, source, index, visitor) })
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
			if err := p.halftone(mark.Style); err != nil {
				return err
			}
			nodes = append(nodes, pdfCompositeNode{image: &mark})
			return nil
		}
		v.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
			children, err := collect(walk)
			if err != nil {
				return err
			}
			nodes = append(nodes, pdfCompositeNode{group: &mark, children: children})
			return nil
		}
		v.Form = func(_ pdfgo.FormMark, walk func(pdfgo.Visitor) error) error {
			children, err := collect(walk)
			if err != nil {
				return err
			}
			if len(children) != 0 {
				children[0].formCount = len(children)
				nodes = append(nodes, children...)
			}
			return nil
		}
		err := walk(v)
		return nodes, err
	}
	nodes, err := collect(walk)
	if err != nil {
		return nil, err
	}
	for font := range embeddedFonts {
		if !externalNames[fontNormalizeName(pdfEmbeddedFontName(font.Name))] {
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

// compositePage 保留独立不透明对象，仅将需要背景参与的效果在局部区域合成
// 入参: space 页面混合空间, walk 页面内容
// 返回: error 转换错误
func (p *pdfImporter) compositePage(space *pdfgo.ColorSpace, walk func(pdfgo.Visitor) error) error {
	nodes, err := p.collectCompositeNodes(walk)
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

// compositeObjects 按绘制顺序转换图元并保留后续混合所需的背景
// 入参: nodes 待转换图元
// 返回: error 转换或合成错误
func (p *pdfImporter) compositeObjects(nodes []pdfCompositeNode) error {
	space := p.compositeSpace
	for i := 0; i < len(nodes); i++ {
		node := nodes[i]
		if count := node.formCount; count >= 16 && count <= len(nodes)-i && !p.transferBackdrop {
			packed, err := p.compositeForm(nodes[i : i+count])
			if err != nil {
				return fmt.Errorf("convert form at graphic %d: %w", i+1, err)
			}
			if packed {
				p.compositeNodes = append(p.compositeNodes, nodes[i:i+count]...)
				i += count - 1
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
			if err := p.compositeRegion(p.compositeNodes, node, space, true); err != nil {
				return fmt.Errorf("composite graphic %d: %w", i+1, err)
			}
		}
		p.compositeNodes = append(p.compositeNodes, node)
		p.transferBackdrop = p.transferBackdrop || transfer
	}
	return p.flushPath()
}

// opaque 检查图元是否无需与页面背景混合
// 入参: space 当前混合空间
// 返回: bool 是否可直接保留
func (n pdfCompositeNode) opaque(space *pdfgo.ColorSpace) bool {
	if n.group != nil {
		g := n.group
		if g.Knockout && n.textObject == nil || g.Alpha != 1 || g.SoftMask != nil || !pdfNormalBlend(g.BlendMode) || g.ColorSpace != nil && !g.ColorSpace.Equal(space) {
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
	if n.image != nil && style.FillOverprint {
		return false
	}
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

// direct 检查sRGB图元能否使用标准OFD透明度而无需背景合成
// 返回: bool 是否可直接转换
func (n pdfCompositeNode) direct() bool {
	if g := n.group; g != nil {
		single := n.textObject != nil && len(n.children) == 1 && (n.children[0].glyph != nil || n.children[0].text != nil && len(n.children[0].text.Glyphs) == 1)
		if g.Knockout && !single || !pdfNormalBlend(g.BlendMode) || g.ColorSpace != nil && !g.ColorSpace.SRGBEquivalent() {
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
	if fill && stroke && (s.Fill.Alpha != 1 || s.Stroke.Alpha != 1 || s.SoftMask != nil) {
		return false
	}
	if n.image != nil && s.FillOverprint {
		return false
	}
	return pdfNormalBlend(s.BlendMode) && !(fill && s.FillOverprint && pdfOverprintNeedsSeparation(s.Fill) || stroke && s.StrokeOverprint && pdfOverprintNeedsSeparation(s.Stroke))
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

// compositeRegion 按原始背景计算局部效果，不将组外文字和图形栅格化
// 入参: backdrop 前序图元, node 当前效果, space 页面混合空间, flatten 是否合并页面背景
// 返回: error 合成或保存错误
func (p *pdfImporter) compositeRegion(backdrop []pdfCompositeNode, node pdfCompositeNode, space *pdfgo.ColorSpace, flatten bool) error {
	if err := p.ctx.Err(); err != nil {
		return err
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
	buffer := make([]pdfCompositePixel, min(pdfCompositeTileSize, w)*min(pdfCompositeTileSize, h))
	scratch := &pdfCompositeScratch{}
	var transfers []*pdfgo.TransferFunction
	var previousTransfers []*pdfgo.TransferFunction
	device := p.transferModel
	if device == "" {
		device = "DeviceRGB"
	}
	hasTransfer := false
	for _, nodes := range [][]pdfCompositeNode{backdrop, {node}} {
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
	for y := 0; y < h; y += pdfCompositeTileSize {
		for x := 0; x < w; x += pdfCompositeTileSize {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			runtime.Gosched()
			width, height := min(pdfCompositeTileSize, w-x), min(pdfCompositeTileSize, h-y)
			c := pdfCompositor{importer: p, box: Box{X: box.X + float64(x)*step, Y: box.Y + float64(y)*step, W: float64(width) * step, H: float64(height) * step}, width: width, height: height, inverse: inverse, cache: cache, scratch: scratch, masks: map[*pdfgo.SoftMask][]float64{}}
			if transfers != nil {
				c.transfers = transfers[:width*height]
				c.transferModel = device
				clear(c.transfers)
			}
			pixels := buffer[:width*height]
			clear(pixels)
			if err := c.draw(backdrop, pixels, space); err != nil {
				return err
			}
			for i := range pixels {
				pixels[i].effect = 0
			}
			copy(previousTransfers, c.transfers)
			if err := c.draw([]pdfCompositeNode{node}, pixels, space); err != nil {
				return err
			}
			for i, pixel := range pixels {
				if i%pdfCompositeTileSize == 0 {
					if err := p.ctx.Err(); err != nil {
						return err
					}
				}
				if pixel.effect == 0 && (c.transfers == nil || c.transfers[i] == previousTransfers[i]) {
					continue
				}
				var rgb [3]float64
				var err error
				if c.transfers != nil && device == "DeviceCMYK" {
					values := pixel.values
					if flatten {
						for j := range values {
							values[j] *= pixel.alpha
						}
					}
					values, err = c.transfers[i].Apply(values[:], device, "")
					if err == nil {
						rgb, err = pdfCompositeOutputColor(space, values)
					}
				} else if c.transfers != nil {
					rgb, err = space.RGB(pixel.values[:space.Components()], "RelativeColorimetric")
				} else {
					rgb, err = pdfCompositeOutputColor(space, pixel.values)
				}
				if err != nil {
					return err
				}
				alpha := pixel.alpha
				if flatten {
					if c.transfers == nil || device != "DeviceCMYK" {
						for j := range rgb {
							rgb[j] = pixel.alpha*rgb[j] + 1 - pixel.alpha
						}
					}
					alpha = 1
				}
				if c.transfers != nil && device != "DeviceCMYK" && c.transfers[i] != nil {
					values, err := c.transfers[i].Apply(rgb[:], "DeviceRGB", "")
					if err != nil {
						return err
					}
					copy(rgb[:], values[:3])
				}
				output.SetNRGBA(x+i%width, y+i/width, color.NRGBA{R: uint8(math.Round(rgb[0] * 255)), G: uint8(math.Round(rgb[1] * 255)), B: uint8(math.Round(rgb[2] * 255)), A: uint8(math.Round(alpha * 255))})
			}
			c.releaseMasks()
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
