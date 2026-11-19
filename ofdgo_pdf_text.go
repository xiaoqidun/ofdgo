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
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfImportedFont 保存单次导入共用的字体资源、度量及源字形映射，不保存文档标识
type pdfImportedFont struct {
	resource    *FontResource
	checksum    [32]byte
	metrics     FontMetrics
	type1Glyphs map[string]uint16
	repairLimit uint16
}

// pdfFontProgramKey 区分字体封装内容及交给资源后端的来源名称
type pdfFontProgramKey struct {
	checksum [32]byte
	name     string
}

// pdfTextReplacement 记录一个完整替换区段的文字数量与转换状态
type pdfTextReplacement struct {
	text     *pdfgo.ReplacementText
	count    int
	matched  int
	offset   int64
	blocked  bool
	applied  bool
	identity bool
}

// pdfTextReplacements 按绘制顺序收集替换区段，避免拆分或重复写入原文
type pdfTextReplacements struct {
	importer *pdfImporter
	spans    map[*pdfgo.ReplacementText]*pdfTextReplacement
	order    []*pdfTextReplacement
	widget   bool
}

// span 查找最外层替换区段，相邻区段即使原文相同也分别保留
// 入参: text 替换文本
// 返回: *pdfTextReplacement 区段状态，未声明时为空
func (r *pdfTextReplacements) span(text *pdfgo.ReplacementText) *pdfTextReplacement {
	if text == nil {
		return nil
	}
	if r.spans == nil {
		r.spans = make(map[*pdfgo.ReplacementText]*pdfTextReplacement)
	}
	first := text
	for text.Parent != nil && r.spans[text] == nil {
		text = text.Parent
	}
	span := r.spans[text]
	if span == nil {
		span = &pdfTextReplacement{text: text}
		r.spans[text] = span
		r.order = append(r.order, span)
	}
	for first != text {
		r.spans[first] = span
		first = first.Parent
	}
	return span
}

// block 标记无法仅用字符与字形变换表达的区段
// 入参: text 替换文本
func (r *pdfTextReplacements) block(text *pdfgo.ReplacementText) {
	if span := r.span(text); span != nil {
		span.blocked = true
	}
}

// markedContent 处理结构区段、可选内容及替换文本属性
// 入参: mark 内容标记
// 返回: error 未支持的语义错误
func (r *pdfTextReplacements) markedContent(mark pdfgo.MarkedContentMark) error {
	if mark.Operator == "EMC" {
		return nil
	}
	if r.importer.structureBindings != nil {
		if err := r.importer.prepareSourceStructure(r.importer.ctx, mark.Reader); err != nil {
			return err
		}
		r.importer.structureBindings.observe(mark.Sequence)
		if mark.Sequence != nil && r.importer.structureBindings.marked != nil {
			key, ok, err := pdfStructureContentKey(mark.Sequence)
			if err != nil {
				return err
			}
			if ok && r.importer.structureBindings.marked[key] {
				return nil
			}
		}
	}
	if mark.Tag == "OC" {
		kind, err := mark.Reader.Resolve(mark.Properties["Type"])
		if err != nil {
			return err
		}
		if kind == pdfgo.Name("OCG") || kind == pdfgo.Name("OCMD") {
			return nil
		}
	}
	if r.widget && mark.Tag == "Tx" && len(mark.Properties) == 0 {
		return nil
	}
	if mark.Replacement != nil && len(mark.Properties) == 1 {
		return nil
	}
	if r.importer.warning == nil {
		return &pdfgo.UnsupportedError{Feature: "marked content requiring semantic preservation"}
	}
	r.importer.warning(pdfgo.Diagnostic{Offset: mark.Offset, Message: "marked content " + string(mark.Tag) + " not preserved"})
	return nil
}

// apply 合并可等价表示的替换区段，原文相同但样式不同的区段保留原图元
// 入参: nodes 原始图元
// 返回: []pdfCompositeNode 转换图元, error 取消或语义转换错误
func (r *pdfTextReplacements) apply(nodes []pdfCompositeNode) ([]pdfCompositeNode, error) {
	if len(r.order) == 0 {
		return nodes, nil
	}
	if err := r.count(nodes, false); err != nil {
		return nil, err
	}
	for _, span := range r.order {
		span.identity = !span.blocked && span.count > 0 && span.matched == len(span.text.Text)
	}
	var err error
	nodes, err = r.merge(nodes)
	if err != nil {
		return nil, err
	}
	for _, span := range r.order {
		if span.applied || span.identity || !span.blocked && span.count == 0 && span.text.Text == "" {
			continue
		}
		if r.importer.warning == nil {
			return nil, &pdfgo.UnsupportedError{Feature: "ActualText span without equivalent OFD glyph mapping"}
		}
		r.importer.warning(pdfgo.Diagnostic{Offset: span.offset, Message: "PDF ActualText span not preserved; original appearance retained"})
	}
	r.clear(nodes)
	return nodes, nil
}

// count 统计区段文字并逐字核对原文，排除需要组级合成的内容
// 入参: nodes 图元, blocked 上层组是否阻止直接转换
// 返回: error 取消错误
func (r *pdfTextReplacements) count(nodes []pdfCompositeNode, blocked bool) error {
	for _, node := range nodes {
		if err := r.importer.ctx.Err(); err != nil {
			return err
		}
		if node.text != nil {
			if span := r.span(node.text.Replacement); span != nil {
				span.count++
				span.blocked = span.blocked || blocked
				for i, glyph := range node.text.Glyphs {
					if span.matched < 0 {
						break
					}
					if i%256 == 0 {
						if err := r.importer.ctx.Err(); err != nil {
							return err
						}
					}
					if glyph.Text == "" || !strings.HasPrefix(span.text.Text[span.matched:], glyph.Text) {
						span.matched = -1
						break
					}
					span.matched += len(glyph.Text)
				}
			}
		}
		childBlocked := blocked
		if group := node.group; group != nil && node.textObject == nil {
			opaque, err := r.importer.opaque(node, group.ColorSpace)
			if err := r.importer.ctx.Err(); err != nil {
				return err
			}
			childBlocked = childBlocked || !opaque || err != nil || group.ColorSpace != nil && !group.ColorSpace.SRGBEquivalent()
		}
		if err := r.count(node.children, childBlocked); err != nil {
			return err
		}
	}
	return nil
}

// merge 合并同层连续图元，保留原始绘制顺序与字形坐标
// 入参: nodes 图元
// 返回: []pdfCompositeNode 合并结果, error 取消错误
func (r *pdfTextReplacements) merge(nodes []pdfCompositeNode) ([]pdfCompositeNode, error) {
	result := nodes[:0]
	for i := 0; i < len(nodes); {
		if err := r.importer.ctx.Err(); err != nil {
			return nil, err
		}
		node := nodes[i]
		var err error
		node.children, err = r.merge(node.children)
		if err != nil {
			return nil, err
		}
		end := i + 1
		combined := false
		first := node.text
		if first == nil && node.textObject != nil && len(node.children) != 0 {
			first = node.children[0].text
		}
		if first != nil {
			if span := r.span(first.Replacement); span != nil && !span.applied && !span.blocked && span.text.Text != "" && !(span.identity && span.count == 1) {
				var marks []*pdfgo.TextMark
				for end = i; end < len(nodes); end++ {
					current := nodes[end]
					children := current.children
					if current.text != nil {
						children = nodes[end : end+1]
					} else if current.textObject == nil {
						break
					}
					valid := len(children) != 0
					for _, child := range children {
						if child.text == nil || r.span(child.text.Replacement) != span {
							valid = false
							break
						}
					}
					if !valid {
						break
					}
					for _, child := range children {
						marks = append(marks, child.text)
					}
				}
				end = max(end, i+1)
				if len(marks) == span.count {
					if mark, ok := codecpdf.MergeReplacement(marks, span.text); ok {
						var origins pdfOriginSet
						for _, source := range marks {
							if err := r.importer.ctx.Err(); err != nil {
								return nil, err
							}
							origin := r.importer.textOrigin(*source)
							origins.add(origin.sequence)
							for _, sequence := range origin.related {
								origins.add(sequence)
							}
						}
						if len(origins.origin.related) != 0 {
							if r.importer.textOrigins == nil {
								r.importer.textOrigins = make(map[*pdfgo.ReplacementText]pdfObjectOrigin)
							}
							r.importer.textOrigins[span.text] = origins.origin
						}
						if node.text != nil {
							node.text = mark
						} else {
							node.children = []pdfCompositeNode{{text: mark}}
						}
						span.applied = true
						combined = true
					}
				}
			}
		}
		node.formCount = 0
		result = append(result, node)
		if !combined {
			for _, remaining := range nodes[i+1 : end] {
				remaining.formCount = 0
				result = append(result, remaining)
			}
		}
		i = end
	}
	clear(nodes[len(result):])
	return result, nil
}

// clear 清除无需替换及未转换的区段引用，防止局部绘制重复使用整段原文
// 入参: nodes 图元
func (r *pdfTextReplacements) clear(nodes []pdfCompositeNode) {
	for _, node := range nodes {
		if node.text != nil {
			if span := r.span(node.text.Replacement); span != nil && !span.applied {
				node.text.Replacement = nil
			}
		}
		r.clear(node.children)
	}
}

// pdfTextGlyphItem 保存单字形的基线坐标、转义原文及字形索引，用于同行合并
type pdfTextGlyphItem struct {
	x            float64
	y            float64
	text         string
	runes        int
	hasTransform bool
	gid          uint16
}

// text 保留字形及逐字基线，外部字体优先复用同名内嵌资源
// 入参: mark PDF文字绘制信息
// 返回: error 错误信息
func (p *pdfImporter) text(mark pdfgo.TextMark) error {
	if mark.Font.Subtype == pdfgo.Name("Type3") && mark.Mode == 3 {
		return nil
	}
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "text blend mode " + string(mark.Style.BlendMode)}
	}
	if mark.Font.Subtype == pdfgo.Name("Type3") {
		visitor := p.visitor()
		for index := range mark.Glyphs {
			if err := p.reader.WalkType3Glyph(p.ctx, mark, index, visitor); err != nil {
				return err
			}
		}
		return p.ctx.Err()
	}
	if mark.Size == 0 || mark.HorizontalScale == 0 {
		return p.degenerateText(mark)
	}
	if mark.Size < 0 || mark.HorizontalScale < 0 {
		x, y := math.Copysign(1, mark.Size*mark.HorizontalScale), math.Copysign(1, mark.Size)
		mark.Matrix = mark.Matrix.Mul(pdfgo.Matrix{x, 0, 0, y, 0, 0})
		mark.Size, mark.HorizontalScale = math.Abs(mark.Size), math.Abs(mark.HorizontalScale)
		positions := make([]pdfgo.Point, len(mark.Positions))
		for index, position := range mark.Positions {
			positions[index] = pdfgo.Point{X: position.X * x, Y: position.Y * y}
		}
		mark.Positions = positions
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if mark.Clip == nil && p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			masked := mark
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Text(masked) })
		}
		return err
	}
	paintMode := mark.Mode % 4
	if (paintMode == 0 || paintMode == 2) && mark.Style.FillOverprint && codecpdf.OverprintNeedsSeparation(mark.Style.Fill) || (paintMode == 1 || paintMode == 2) && mark.Style.StrokeOverprint && codecpdf.OverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "text color separation overprint"}
	}
	if p.warning != nil && ((paintMode == 0 || paintMode == 2) && p.gradientError(mark.Style.Fill) != nil || (paintMode == 1 || paintMode == 2) && p.gradientError(mark.Style.Stroke) != nil) {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		gradient := mark
		return p.compositeRegion(nil, pdfCompositeNode{text: &gradient}, &pdfgo.ColorSpace{Model: "DeviceRGB"}, false)
	}
	font := mark.Font
	embedded := len(font.Program) != 0
	if embedded && font.ProgramType != "FontFile" && font.ProgramType != "FontFile2" && font.ProgramType != "OpenType" && font.ProgramType != "Type1C" && font.ProgramType != "CIDFontType0C" {
		return &pdfgo.UnsupportedError{Feature: "font program " + string(font.ProgramType)}
	}
	var imported *pdfImportedFont
	reused := false
	if embedded {
		imported, err = p.importedFont(font)
		if err != nil {
			return err
		}
	} else {
		imported = p.embeddedTextFont(font, mark.Glyphs)
		symbol := false
		if imported == nil {
			imported, err = p.symbolFont(font)
			if err != nil {
				return err
			}
			symbol = imported != nil
		}
		if imported != nil {
			reused, embedded = true, true
			mark.Glyphs = slices.Clone(mark.Glyphs)
			for index := range mark.Glyphs {
				glyph := &mark.Glyphs[index]
				if symbol {
					glyph.ID = imported.type1Glyphs[glyph.Name]
				} else {
					char, _ := utf8.DecodeRuneInString(glyph.Text)
					glyph.ID = imported.metrics.GlyphIndex(char)
				}
				glyph.HasID = true
			}
		}
	}
	id := p.fontIDs[font]
	if reused {
		id, err = p.editor.addFontResource(imported.resource, imported.checksum)
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.editor.fontMetrics[id] = imported.metrics
	} else if id == "" {
		var err error
		if embedded {
			if imported.repairLimit != 0 {
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded font lacks trailing side bearings; unused metrics reconstructed from glyph bounds"})
			}
			id, err = p.editor.addFontResource(imported.resource, imported.checksum)
			if err == nil {
				p.editor.fontMetrics[id] = imported.metrics
			}
		} else {
			if font.Recovered {
				if err := p.preservePDFObject(p.ctx, "Font", font.Dictionary); err != nil {
					return err
				}
			}
			id, err = p.editor.AddExternalFont(font.Name)
		}
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.fontIDs[font] = id
	}
	if embedded && imported.repairLimit != 0 {
		for _, glyph := range mark.Glyphs {
			if glyph.HasID && glyph.ID >= imported.repairLimit {
				return &pdfgo.UnsupportedError{Feature: "embedded font glyph with missing side bearing"}
			}
		}
	}
	var metrics FontMetrics
	var outline FontOutlines
	if embedded {
		metrics = imported.metrics
		outline = metrics.(FontOutlines)
	}
	fill, stroke, visible := paintMode == 0 || paintMode == 2, paintMode == 1 || paintMode == 2, paintMode != 3
	needOutline := stroke
	for _, glyph := range mark.Glyphs {
		needOutline = needOutline || glyph.Name == ".notdef"
	}
	if needOutline && !embedded {
		if resolved, err := p.editor.editorFont(id); err == nil {
			if provider, ok := resolved.(FontOutlines); ok {
				metrics, outline = resolved, provider
				for _, glyph := range mark.Glyphs {
					if glyph.Name == ".notdef" {
						continue
					}
					char, _ := utf8.DecodeRuneInString(glyph.Text)
					if utf8.RuneCountInString(glyph.Text) != 1 || resolved.GlyphIndex(char) == 0 && char != 0 {
						metrics, outline = nil, nil
						break
					}
				}
			}
		}
	}
	outlineStroke := stroke && outline != nil
	var glyphPaths []pdfgo.Path
	const unit = 25.4 / 72
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, 0, 0})
	var extent pdfPointBounds
	object := TextObject{Font: id, Size: mark.Size * unit, HScale: mark.HorizontalScale}
	items := make([]pdfTextGlyphItem, 0, len(mark.Glyphs))
	if mark.Replacement != nil {
		object.TextCode = make([]TextCode, 0, len(mark.Glyphs))
		if embedded {
			object.CGTransform = make([]CGTransform, 0, len(mark.Glyphs))
		}
	}
	position := 0
	for n, glyph := range mark.Glyphs {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		gid := glyph.ID
		undefined := glyph.Name == ".notdef"
		if !embedded && undefined {
			gid = 0
		}
		if embedded && !reused && font.ProgramType == "FontFile" {
			gid = imported.type1Glyphs[glyph.Name]
			glyph.HasID = true
		}
		if glyph.Text == "" && (glyph.HasID || undefined) {
			glyph.Text = "\u00a4"
		}
		if embedded && !glyph.HasID {
			if utf8.RuneCountInString(glyph.Text) != 1 {
				return &pdfgo.UnsupportedError{Feature: "simple font glyph ligature mapping"}
			}
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			gid = metrics.GlyphIndex(char)
			if gid == 0 && char != 0 {
				return fmt.Errorf("missing PDF glyph for %U", char)
			}
		}
		if embedded && gid >= metrics.NumGlyphs() {
			return fmt.Errorf("PDF glyph index %d outside embedded font %s (%d glyphs)", gid, font.Name, metrics.NumGlyphs())
		}
		if !embedded && glyph.Text == "" {
			return &pdfgo.UnsupportedError{Feature: "unmapped external font character"}
		}
		if outlineStroke && !embedded && !undefined {
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			gid = metrics.GlyphIndex(char)
		}
		origin := mark.Positions[n]
		escaped := escapeOFDText(glyph.Text)
		count := utf8.RuneCountInString(glyph.Text)
		hasTransform := embedded || undefined
		if mark.Replacement != nil {
			object.TextCode = append(object.TextCode, TextCode{X: pdfNumbers(origin.X * unit), Y: pdfNumbers(-origin.Y * unit), Value: escaped})
			if hasTransform {
				object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: position, CodeCount: count, GlyphCount: 1, Glyphs: strconv.Itoa(int(gid))})
			}
		} else {
			items = append(items, pdfTextGlyphItem{x: origin.X * unit, y: -origin.Y * unit, text: escaped, runes: count, hasTransform: hasTransform, gid: gid})
		}
		position += count
		if embedded || outlineStroke || undefined && outline != nil {
			var path GeometryPath
			var bounds Box
			var err error
			if provider, ok := outline.(FontGlyphBounds); ok && !outlineStroke {
				bounds, err = provider.GlyphBounds(gid, object.Size)
			} else {
				path, err = outline.GlyphOutline(gid, object.Size)
				if err == nil {
					bounds, err = path.Bounds()
				}
			}
			if err != nil {
				return fmt.Errorf("PDF font %s outline: %w", font.Name, err)
			}
			if diagnostics, ok := outline.(FontGlyphDiagnostics); ok {
				if err := diagnostics.GlyphWarning(gid); err != nil {
					if p.warning == nil {
						return fmt.Errorf("PDF font %s glyph %d hinting: %w", font.Name, gid, err)
					}
					if p.fontWarnings == nil {
						p.fontWarnings = make(map[string]bool)
					}
					if !p.fontWarnings[id] {
						p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s has invalid hinting instructions; design outlines and original font retained", font.Name)})
						p.fontWarnings[id] = true
					}
				}
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				extent.add(m.Apply(pdfgo.Point{X: origin.X*unit + point.X*mark.HorizontalScale, Y: -origin.Y*unit + point.Y}))
			}
			if outlineStroke {
				matrix := mark.Matrix.Mul(pdfgo.Matrix{mark.HorizontalScale / unit, 0, 0, -1 / unit, origin.X, origin.Y})
				glyphPath, err := pdfGlyphPath(path, matrix)
				if err != nil {
					return err
				}
				glyphPaths = append(glyphPaths, glyphPath)
			}
		} else {
			width := glyph.Width / 1000 * object.Size * mark.HorizontalScale
			bounds := Box{X: 0, Y: -object.Size, W: width, H: object.Size * 1.25}
			if undefined {
				b := font.BoundingBox
				if b == nil {
					b = font.FallbackBoundingBox
				}
				if b != nil {
					bounds = Box{X: b.XMin / 1000 * object.Size * mark.HorizontalScale, Y: -b.YMax / 1000 * object.Size, W: (b.XMax - b.XMin) / 1000 * object.Size * mark.HorizontalScale, H: (b.YMax - b.YMin) / 1000 * object.Size}
				}
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				extent.add(m.Apply(pdfgo.Point{X: origin.X*unit + point.X, Y: -origin.Y*unit + point.Y}))
			}
		}
	}
	if mark.Replacement != nil {
		pdfApplyReplacement(&object, mark.Replacement.Text, mark.Positions)
	} else {
		pdfCompactTextRuns(&object, items)
	}
	box := extent.box()
	if stroke && !outlineStroke {
		strokeMatrix := mark.StrokeMatrix
		if strokeMatrix == (pdfgo.Matrix{}) {
			strokeMatrix = pdfgo.Identity()
		}
		sx, sy := math.Hypot(strokeMatrix[0], strokeMatrix[1]), math.Hypot(strokeMatrix[2], strokeMatrix[3])
		if math.Abs(sx-sy) > 1e-8*math.Max(1, sx) || math.Abs(strokeMatrix[0]*strokeMatrix[2]+strokeMatrix[1]*strokeMatrix[3]) > 1e-8*math.Max(1, sx*sy) {
			return &pdfgo.UnsupportedError{Feature: "anisotropic text stroke"}
		}
		scale := math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
		if scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			return &pdfgo.UnsupportedError{Feature: "degenerate text stroke matrix"}
		}
		lineWidth := mark.Style.LineWidth * math.Hypot(p.matrix[0], p.matrix[1]) * sx
		margin := lineWidth / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{X: box.X - margin, Y: box.Y - margin, W: box.W + 2*margin, H: box.H + 2*margin}
		object.LineWidth = lineWidth / scale
		object.LineWidthSet = object.LineWidth == 0
		object.Cap = []string{"Butt", "Round", "Square"}[mark.Style.Cap]
		object.Join = []string{"Miter", "Round", "Bevel"}[mark.Style.Join]
		object.MiterLimit = mark.Style.MiterLimit
		if len(mark.Style.Dash) > 0 {
			strokeScale := math.Hypot(p.matrix[0], p.matrix[1]) * sx / scale
			object.DashPattern = pdfDashPattern(mark.Style.Dash, strokeScale)
			phase := mark.Style.DashPhase * strokeScale
			object.DashOffset = &phase
		}
	}
	object.Boundary = pdfBoundary(box)
	if stroke && !outlineStroke {
		strokePaint, err := p.paintColor(mark.Style.Stroke, box, mark.Style.RenderingIntent)
		if err != nil {
			return err
		}
		color := StrokeColor(*strokePaint)
		object.StrokeColor = &color
	}
	if box.X >= p.pageWidth || box.Y >= p.pageHeight || box.X+box.W <= 0 || box.Y+box.H <= 0 {
		visible = false
	}
	object.CTM = pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y)
	object.Fill = &fill
	object.Stroke = &stroke
	object.Visible = &visible
	object.FillColor, err = p.color(mark.Style.Fill, mark.Style.RenderingIntent)
	if err != nil {
		return err
	}
	if fill && !outlineStroke {
		object.FillColor, err = p.paintColor(mark.Style.Fill, box, mark.Style.RenderingIntent)
		if err != nil {
			return err
		}
	}
	object.Clips, err = p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	if mark.Clip != nil {
		clipObject := object
		clipObject.Clips = nil
		clipObject.Fill, clipObject.Stroke = new(bool), new(bool)
		*clipObject.Fill = true
		*clipObject.Stroke = false
		clipObject.Visible = nil
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		}
		p.clipTexts[mark.Clip] = []TextObject{clipObject}
	}
	if outlineStroke {
		for _, path := range glyphPaths {
			if len(path.Segments) == 0 {
				continue
			}
			if err := p.ctx.Err(); err != nil {
				return err
			}
			matrix := mark.StrokeMatrix
			if matrix == (pdfgo.Matrix{}) {
				matrix = pdfgo.Identity()
			}
			if err := p.appendPath(pdfgo.PathMark{Sequence: mark.Sequence, Path: path, Style: mark.Style, Fill: fill, Stroke: true, StrokeMatrix: matrix}); err != nil {
				return err
			}
		}
		object.Visible = new(bool)
		object.Fill, object.Stroke = new(bool), new(bool)
	}
	p.appendOriginObject(GraphicObject{Type: "TextObject", TextObject: object}, p.textOrigin(mark))
	p.report.TextObjects++
	return nil
}

// degenerateText 保留零字号或零水平缩放的逐字位置与退化字形
// 入参: mark PDF文字绘制信息
// 返回: error 转换错误
func (p *pdfImporter) degenerateText(mark pdfgo.TextMark) error {
	x, y := math.Copysign(1, mark.Size*mark.HorizontalScale), math.Copysign(1, mark.Size)
	size, scale := math.Abs(mark.Size), math.Abs(mark.HorizontalScale)
	if size == 0 {
		size, x, y = 1, 0, 0
	}
	if scale == 0 {
		scale, x = 1, 0
	}
	var clips []TextObject
	for index, position := range mark.Positions {
		glyph := mark
		glyph.Size, glyph.HorizontalScale = size, scale
		glyph.Matrix = mark.Matrix.Mul(pdfgo.Matrix{1, 0, 0, 1, position.X, position.Y}).Mul(pdfgo.Matrix{x, 0, 0, y, 0, 0})
		glyph.Positions = []pdfgo.Point{{}}
		glyph.Glyphs = mark.Glyphs[index : index+1]
		if mark.Clip != nil {
			glyph.Clip = new(pdfgo.TextClip)
		}
		if err := p.text(glyph); err != nil {
			return err
		}
		if glyph.Clip != nil {
			clips = append(clips, p.clipTexts[glyph.Clip]...)
			delete(p.clipTexts, glyph.Clip)
		}
	}
	if mark.Clip != nil {
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		}
		p.clipTexts[mark.Clip] = clips
	}
	return nil
}

// disjointText 检查实际字形轮廓是否互不相交，允许无重叠挖空文字直接保留
// 入参: node 隐式文字组
// 返回: bool 是否互不相交, error 轮廓编译错误
func (p *pdfImporter) disjointText(node pdfCompositeNode) (bool, error) {
	cache := p.compositingCache()
	if result, found := cache.textDisjoint[node.group]; found {
		return result, nil
	}
	geometry, err := p.renderer.Geometry()
	if err != nil {
		return false, err
	}
	var inverse pdfgo.Matrix
	var boxes []Box
	for _, child := range node.children {
		mark := child.text
		if mark == nil {
			mark = child.glyph
		}
		if mark == nil || len(mark.Glyphs) != 1 {
			return false, nil
		}
		if inverse == (pdfgo.Matrix{}) {
			var ok bool
			inverse, ok = p.matrix.Mul(mark.Matrix).Inverse()
			if !ok {
				return false, nil
			}
		}
		scene, _, err := p.compileGroup(func(local *pdfImporter) error {
			if child.group != nil {
				return child.coverage(local, false)
			}
			_, fill, stroke := child.style()
			if fill {
				if err := child.coverage(local, false); err != nil {
					return err
				}
			}
			if stroke {
				return child.coverage(local, true)
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if scene == nil {
			continue
		}
		var box Box
		for _, command := range scene.Commands {
			path, err := rasterCommandGeometry(geometry, command)
			if err != nil {
				return false, err
			}
			path, err = geometry.Transform(path, MatrixFromValues([6]float64(inverse)))
			if err != nil {
				return false, err
			}
			bounds, err := geometry.Bounds(path)
			if err != nil {
				return false, err
			}
			box = unionTextBox(box, bounds)
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		for _, previous := range boxes {
			if box.X < previous.X+previous.W && previous.X < box.X+box.W && box.Y < previous.Y+previous.H && previous.Y < box.Y+box.H {
				if cache.textDisjoint == nil {
					cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
				}
				cache.textDisjoint[node.group] = false
				return false, nil
			}
		}
		boxes = append(boxes, box)
	}
	if cache.textDisjoint == nil {
		cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
	}
	cache.textDisjoint[node.group] = true
	return true, nil
}

// shapePDFText 使用导入字体整形注释文字，将毫米坐标约定转换为PDF文字空间方向
// 入参: ctx 取消上下文, source 源字体, text 单行原文, rightToLeft 段落方向
// 返回: []pdfgo.ShapedGlyph 千分之一字号的定位字形, error 字体、整形或取消错误
func (p *pdfImporter) shapePDFText(ctx context.Context, source *pdfgo.Font, text string, rightToLeft bool) ([]pdfgo.ShapedGlyph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(source.Program) == 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "annotation shaping without embedded font"}
	}
	font, err := p.importedFont(source)
	if err != nil {
		return nil, err
	}
	shaper, ok := font.metrics.(FontShaperOptions)
	if !ok {
		return nil, &pdfgo.UnsupportedError{Feature: "annotation text shaping backend"}
	}
	options := TextShapeOptions{Bidi: true}
	if rightToLeft {
		options.Direction = "rtl"
	}
	glyphs, err := shaper.ShapeTextWithOptions(text, 1000, options)
	if err != nil {
		return nil, err
	}
	result := make([]pdfgo.ShapedGlyph, len(glyphs))
	units := float64(font.metrics.UnitsPerEm())
	if units <= 0 {
		return nil, fmt.Errorf("invalid annotation font units")
	}
	for i, glyph := range glyphs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result[i] = pdfgo.ShapedGlyph{ID: glyph.Glyph, Cluster: glyph.Cluster, VisualOrder: glyph.VisualOrder, X: glyph.X, Y: -glyph.Y, Advance: glyph.Advance, Width: float64(font.metrics.GlyphAdvance(glyph.Glyph)) * 1000 / units}
	}
	return result, ctx.Err()
}

// embeddedTextFont 匹配同名内嵌字体，仅复用唯一且覆盖全部字符的字形程序
// 入参: source 外部字体, glyphs 当前文字字形
// 返回: *pdfImportedFont 可复用字体，无可靠匹配时为空
func (p *pdfImporter) embeddedTextFont(source *pdfgo.Font, glyphs []pdfgo.Glyph) *pdfImportedFont {
	name := codecpdf.EmbeddedFontName(source.Name)
	if name == "" || len(glyphs) == 0 {
		return nil
	}
	var matched *pdfImportedFont
	for _, imported := range p.fonts {
		if codecpdf.EmbeddedFontName(imported.resource.Font.FontName) != name {
			continue
		}
		if matched != nil && matched.checksum != imported.checksum {
			return nil
		}
		matched = imported
	}
	if matched == nil {
		return nil
	}
	for _, glyph := range glyphs {
		if glyph.Name == ".notdef" || utf8.RuneCountInString(glyph.Text) != 1 {
			return nil
		}
		char, _ := utf8.DecodeRuneInString(glyph.Text)
		id := matched.metrics.GlyphIndex(char)
		if id == 0 || id >= matched.metrics.NumGlyphs() || matched.repairLimit != 0 && id >= matched.repairLimit {
			return nil
		}
	}
	return matched
}

// importedFont 按原PDF字体复用包装和解析结果，失败结果不进入缓存
// 入参: source 源字体
// 返回: *pdfImportedFont 只读导入字体, error 字体程序或后端错误
func (p *pdfImporter) importedFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	if cached := p.fonts[source]; cached != nil {
		return cached, nil
	}
	var type1 *basefont.Type1Program
	result := &pdfImportedFont{}
	programSource := *source
	programRecovered := false
	if p.warning != nil {
		var err error
		if source.ProgramType == "FontFile" {
			programSource.Program, programRecovered, err = basefont.NormalizePDFType1(source.Program)
		} else {
			programSource.Program, programRecovered, err = basefont.NormalizePDFProgram(source.Program)
		}
		if err != nil {
			return nil, fmt.Errorf("PDF font %s container: %w", source.Name, err)
		}
	}
	if source.ProgramType == "FontFile" {
		parsed, err := basefont.ParseType1Program(programSource.Program)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
		}
		type1 = &parsed
		result.type1Glyphs = parsed.GlyphIDs()
	}
	program, repair, err := codecpdf.FontProgram(&programSource, type1)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
	}
	if repair.Locations && p.warning == nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, basefont.ErrTTInvalidLocations)
	}
	if p.editor.backends.FontResources == nil {
		return nil, fmt.Errorf("PDF font %s resource: font resources: %w", source.Name, ErrBackendUnavailable)
	}
	if p.editor.backends.Fonts == nil {
		return nil, fmt.Errorf("PDF font backend unavailable")
	}
	key := pdfFontProgramKey{checksum: sha256.Sum256(program), name: source.Name}
	if cached := p.fontPrograms[key]; cached != nil {
		result.resource, result.metrics, result.checksum = cached.resource, cached.metrics, cached.checksum
	} else {
		result.resource, err = p.editor.backends.FontResources.OpenFontResource(FontFile{Name: source.Name + ".ttf", Data: program}, 0)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s resource: %w", source.Name, err)
		}
		result.metrics, err = p.editor.backends.Fonts.OpenFont(result.resource.Data)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s metrics: %w", source.Name, err)
		}
		if _, ok := result.metrics.(FontOutlines); !ok {
			return nil, fmt.Errorf("PDF font backend does not provide glyph outlines")
		}
		result.checksum = sha256.Sum256(result.resource.Data)
	}
	result.repairLimit = repair.MetricsLimit
	if source.Recovered || programRecovered {
		if err := p.preservePDFObject(p.ctx, "Font", source.Dictionary); err != nil {
			return nil, err
		}
	}
	if programRecovered {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s container and metrics normalized from embedded program; original glyph IDs retained", source.Name)})
	}
	if repair.Locations {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s has invalid glyph locations; index rebuilt with original glyph IDs", source.Name)})
	}
	if p.fonts == nil {
		p.fonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if p.fontPrograms == nil {
		p.fontPrograms = make(map[pdfFontProgramKey]*pdfImportedFont)
	}
	p.fontPrograms[key] = result
	p.fonts[source] = result
	return result, nil
}

// symbolFont 加载标准符号字体，保留原字形编号和源字形名称
// 入参: source 未嵌入的PDF字体
// 返回: *pdfImportedFont 可用的符号字体，无匹配时为空, error 字体或取消错误
func (p *pdfImporter) symbolFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	name := codecpdf.EmbeddedFontName(source.Name)
	if source.Subtype != "Type1" || name != "Symbol" && name != "ZapfDingbats" {
		return nil, nil
	}
	if imported, ok := p.symbolFonts[source]; ok {
		return imported, nil
	}
	if p.symbolFonts == nil {
		p.symbolFonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	definition := &Font{ID: name, FontName: name, FamilyName: name}
	reader := &Reader{doc: &Document{}, fontResourcesRead: true, fontCache: map[string]*Font{name: definition}}
	resolved, err := p.editor.newRenderer(reader).ResolveFont(name, true)
	if err != nil || len(resolved.Data) == 0 || p.editor.backends.FontResources == nil {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	program, names, err := basefont.SymbolProgram(p.ctx, resolved.Data, name)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	resource, err := p.editor.backends.FontResources.OpenFontResource(FontFile{Name: name + ".ttf", Data: program}, 0)
	if err != nil {
		return nil, err
	}
	metrics, err := p.editor.backends.Fonts.OpenFont(resource.Data)
	if err != nil {
		return nil, err
	}
	if _, ok := metrics.(FontOutlines); !ok {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	result := &pdfImportedFont{resource: resource, checksum: sha256.Sum256(resource.Data), metrics: metrics, type1Glyphs: names}
	p.symbolFonts[source] = result
	return result, nil
}

// textOrigin 获取合并文字的完整来源，普通文字沿用原始区段
// 入参: mark 文字图元
// 返回: pdfObjectOrigin 来源区段
func (p *pdfImporter) textOrigin(mark pdfgo.TextMark) pdfObjectOrigin {
	if origin, ok := p.textOrigins[mark.Replacement]; ok {
		return origin
	}
	return pdfObjectOrigin{sequence: mark.Sequence}
}

// pdfApplyReplacement 将整段原文映射到全部字形，字形位移遵循GB/T 33190的文字定位规则
// 入参: object 文字对象, text 整段原文, positions 原始字形位置
func pdfApplyReplacement(object *TextObject, text string, positions []pdfgo.Point) {
	const unit = 25.4 / 72
	glyphs := make([]string, len(object.CGTransform))
	for i, transform := range object.CGTransform {
		glyphs[i] = transform.Glyphs
	}
	dx, dy := make([]float64, len(positions)-1), make([]float64, len(positions)-1)
	for i := 1; i < len(positions); i++ {
		dx[i-1] = (positions[i].X - positions[i-1].X) * unit
		dy[i-1] = -(positions[i].Y - positions[i-1].Y) * unit
	}
	object.TextCode = []TextCode{{X: pdfNumbers(positions[0].X * unit), Y: pdfNumbers(-positions[0].Y * unit), DeltaX: pdfNumbers(dx...), DeltaY: pdfNumbers(dy...), Value: escapeOFDText(text)}}
	object.CGTransform = []CGTransform{{CodeCount: utf8.RuneCountInString(text), GlyphCount: len(glyphs), Glyphs: strings.Join(glyphs, " ")}}
}

// pdfCompactTextRuns 将同基线连续字形合并为单条TextCode与DeltaX，并合并连续一对一CGTransform
// 入参: object 目标文字对象, items 字形项序列
func pdfCompactTextRuns(object *TextObject, items []pdfTextGlyphItem) {
	if len(items) == 0 {
		return
	}
	object.TextCode = make([]TextCode, 0, 1)
	var textBuf strings.Builder
	var glyphBuf strings.Builder
	var deltas []float64
	pos := 0
	for start := 0; start < len(items); {
		first := items[start]
		end := start + 1
		if first.hasTransform || first.runes == 1 {
			for end < len(items) && math.Abs(items[end].y-first.y) <= 1e-9 && (items[end].hasTransform || items[end].runes == 1) {
				end++
			}
		}
		textBuf.Reset()
		deltas = deltas[:0]
		curX := first.x
		runStart, runCount := -1, 0
		glyphBuf.Reset()
		flushTransform := func() {
			if runCount == 0 {
				return
			}
			object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: runStart, CodeCount: runCount, GlyphCount: runCount, Glyphs: glyphBuf.String()})
			runStart, runCount = -1, 0
			glyphBuf.Reset()
		}
		for i := start; i < end; i++ {
			item := items[i]
			textBuf.WriteString(item.text)
			if i > start {
				dx := item.x - curX
				deltas = append(deltas, dx)
				curX += dx
			}
			if !item.hasTransform {
				flushTransform()
			} else if item.runes == 1 {
				if runCount == 0 {
					runStart = pos
				} else {
					glyphBuf.WriteByte(' ')
				}
				glyphBuf.WriteString(strconv.Itoa(int(item.gid)))
				runCount++
			} else {
				flushTransform()
				object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: pos, CodeCount: item.runes, GlyphCount: 1, Glyphs: strconv.Itoa(int(item.gid))})
			}
			pos += item.runes
		}
		flushTransform()
		code := TextCode{X: pdfNumbers(first.x), Y: pdfNumbers(first.y), Value: textBuf.String()}
		if len(deltas) > 0 {
			code.DeltaX = pdfNumbers(deltas...)
		}
		object.TextCode = append(object.TextCode, code)
		start = end
	}
}
