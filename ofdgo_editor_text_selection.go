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
	"image/color"
	"math"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// TextSelection 指定同页同一直接容器的文字，Root为空使用IDs，否则使用Indexes
// 分析返回的选区绑定文档和修订，结构或内容改变后需重新分析
type TextSelection struct {
	Root     *ObjectPath
	IDs      []string
	Indexes  []int
	editor   *Editor
	document editorDocumentIdentity
	revision uint64
	page     int
}

// TextSelectionEdit 指定码点范围替换，空Text表示删除，Font覆盖所有剩余文字
// Reflow非空时使用完整富文本显式重排并忽略范围和Text，Width为可选页面段落宽度，单位为毫米
// Size为页面毫米字号，零值保持原字号，Color为空保持原颜色，FontData优先于Font
type TextSelectionEdit struct {
	Start, End int
	Text       string
	Font       string
	FontData   []byte
	Size       float64
	Color      string
	Reflow     *RichText
	Width      float64
}

// TextSelectionPart 保存源图元及合并文本码点范围，Matrix将TextObject局部坐标映射至页面
// Deleted表示提交时删除源图元，重排生成项以Sources标识全部来源
// Points为不含换行的局部码点原点，Color为解析后的RGB分量，Start和End使用原合并文本范围
type TextSelectionPart struct {
	ID         string
	Index      int
	Start, End int
	TextObject TextObject
	Matrix     Matrix
	Deleted    bool
	Sources    []int
	Points     []Point
	Color      string
	points     []Point
	position   ObjectPosition
	raw        TextObject
	container  *editorXML
}

// TextSelectionAnalysis 保存阅读顺序、格式和可执行能力，不插入推测空格
type TextSelectionAnalysis struct {
	Selection               TextSelection
	Text                    string
	RichText                RichText
	Parts                   []TextSelectionPart
	Bounds                  Box
	Replace, Delete, Reflow bool
}

// TextSelectionPreview 保存实际更新后的文字和源映射，删除项保留原范围
type TextSelectionPreview struct {
	Selection TextSelection
	Font      string
	Text      string
	Parts     []TextSelectionPart
	Bounds    Box
	analysis  TextSelectionAnalysis
}

// AnalyzeTextSelection 分析同一容器内的横排文字，拒绝重叠及无法可靠映射的布局
// 入参: page 页面索引, selection 文字选区
// 返回: TextSelectionAnalysis 阅读顺序与能力, error 选择或布局错误
func (e *Editor) AnalyzeTextSelection(page int, selection TextSelection) (TextSelectionAnalysis, error) {
	if selection.editor != nil && (selection.editor != e || selection.page != page || !e.documentIdentity().matches(selection.document) || selection.revision != e.revision) {
		return TextSelectionAnalysis{}, fmt.Errorf("stale text selection")
	}
	copy := e.transactionSnapshot()
	result, err := copy.analyzeTextSelection(page, selection)
	if err == nil {
		result.Selection.editor, result.Selection.document, result.Selection.revision = e, e.documentIdentity(), e.revision
		result.Selection.page = page
	}
	return cloneEditorData(result), err
}

// PreviewTextSelection 在隔离快照生成与提交相同的结果，不分配文档资源或修改历史
// 入参: page 页面索引, selection 文字选区, edit 编辑范围与样式
// 返回: TextSelectionPreview 更新文字与来源映射, error 校验错误
func (e *Editor) PreviewTextSelection(page int, selection TextSelection, edit TextSelectionEdit) (TextSelectionPreview, error) {
	analysis, err := e.AnalyzeTextSelection(page, selection)
	if err != nil {
		return TextSelectionPreview{}, err
	}
	copy := e.transactionSnapshot()
	defer copy.bindPageIndex(page)()
	if err := copy.prepareTextSelectionEdit(&edit); err != nil {
		return TextSelectionPreview{}, err
	}
	result, err := copy.previewTextSelection(analysis, edit)
	return cloneEditorData(result), err
}

// UpdateTextSelection 原子提交联合文字修改，失败不改变资源、ID或撤销重做
// 入参: page 页面索引, selection 文字选区, edit 编辑范围与样式
// 返回: error 校验或提交错误
func (e *Editor) UpdateTextSelection(page int, selection TextSelection, edit TextSelectionEdit) error {
	analysis, err := e.AnalyzeTextSelection(page, selection)
	if err != nil {
		return err
	}
	return e.Transaction(func(e *Editor) error {
		defer e.bindPageIndex(page)()
		if err := e.prepareTextSelectionEdit(&edit); err != nil {
			return err
		}
		preview, err := e.previewTextSelection(analysis, edit)
		if err != nil {
			return err
		}
		if edit.Reflow != nil {
			return e.reflowTextSelection(page, selection, edit, preview)
		}
		if selection.Root != nil {
			parts := make(map[int]int, len(preview.Parts))
			for i, part := range preview.Parts {
				parts[part.Index] = i
			}
			return e.editCompositeObjects(page, *selection.Root, selection.Indexes, func(_ *Renderer, nodes []*editorCompositeNode, _ []CompositeMember) error {
				for i, node := range nodes {
					index, exists := parts[selection.Indexes[i]]
					if !exists {
						continue
					}
					part := &preview.Parts[index]
					if part.Deleted {
						node.owner.patches = append(node.owner.patches, editorXMLPatch{node.span.start, node.span.end, nil})
						continue
					}
					object := cloneEditorData(node.object)
					object.TextObject.TextCode, object.TextObject.CGTransform = part.TextObject.TextCode, part.TextObject.CGTransform
					if edit.Font != "" {
						object.TextObject.Font = part.TextObject.Font
					}
					if edit.Size != 0 {
						object.TextObject.Size = part.TextObject.Size
					}
					if edit.Color != "" {
						object.TextObject.FillColor = part.TextObject.FillColor
					}
					if reflect.DeepEqual(object, node.object) {
						continue
					}
					if err := node.update(object); err != nil {
						return err
					}
					node.setState(editorCompositeState{})
				}
				return nil
			})
		}
		var updates []GraphicObject
		var deleted []string
		for _, part := range preview.Parts {
			if part.Deleted {
				deleted = append(deleted, part.ID)
				continue
			}
			next := selectionUpdatedText(part, edit)
			if !reflect.DeepEqual(next, part.raw) {
				updates = append(updates, GraphicObject{Type: "TextObject", TextObject: next})
			}
		}
		if len(updates) > 0 {
			if err := e.updateObjects(page, updates, true); err != nil {
				return err
			}
		}
		if len(deleted) > 0 {
			return e.DeleteObjects(page, deleted)
		}
		return nil
	})
}

// analyzeTextSelection 在隔离资源视图中计算选区和逐字坐标
// 入参: page 页面索引, selection 文字选区
// 返回: TextSelectionAnalysis 分析快照, error 选择或布局错误
func (e *Editor) analyzeTextSelection(page int, selection TextSelection) (TextSelectionAnalysis, error) {
	defer e.bindPageIndex(page)()
	result := TextSelectionAnalysis{Selection: selection, Replace: true, Delete: true, Reflow: true}
	result.Selection.IDs = slices.Clone(selection.IDs)
	result.Selection.Indexes = slices.Clone(selection.Indexes)
	if selection.Root != nil {
		root := cloneEditorData(*selection.Root)
		result.Selection.Root = &root
		if len(selection.IDs) != 0 || len(selection.Indexes) == 0 {
			return result, fmt.Errorf("composite selection requires indexes only")
		}
		reader, renderer, _, nodes, err := e.compositeScope(page, root)
		if err != nil {
			return result, err
		}
		defer reader.Close()
		members, err := e.measureCompositeSelection(renderer, nodes, selection.Indexes)
		if err != nil {
			return result, err
		}
		for i, member := range members {
			if member.Object.Type != "TextObject" || !member.Capabilities.RewriteText {
				return result, fmt.Errorf("selection member is not editable text")
			}
			node := nodes[selection.Indexes[i]]
			result.Parts = append(result.Parts, TextSelectionPart{ID: member.Object.TextObject.ID, Index: selection.Indexes[i], TextObject: member.Object.TextObject, raw: node.object.TextObject, Matrix: member.Matrix, position: member.Position, container: node.span.parent})
		}
	} else {
		if len(selection.Indexes) != 0 || len(selection.IDs) == 0 {
			return result, fmt.Errorf("page selection requires IDs only")
		}
		objects, _, err := e.selectedObjects(page, selection.IDs)
		if err != nil {
			return result, err
		}
		for _, object := range objects {
			if object.Type != "TextObject" {
				return result, fmt.Errorf("selection member is not text")
			}
			t := object.TextObject
			capability, err := e.ObjectCapabilities(page, t.ID)
			if err != nil {
				return result, err
			}
			if !capability.RewriteText {
				return result, capability.editError()
			}
			position, err := e.ObjectPosition(page, t.ID)
			if err != nil {
				return result, err
			}
			layer, _, err := e.findObject(page, t.ID)
			if err != nil {
				return result, err
			}
			resolved, err := e.resolveEditorStyle(object, layer.DrawParam)
			if err != nil {
				return result, err
			}
			box, err := ParseBox(t.Boundary)
			if err != nil {
				return result, err
			}
			container, _ := e.objectOrderIndexes(page, layer, t.ID)
			result.Parts = append(result.Parts, TextSelectionPart{ID: t.ID, Index: position.Index, TextObject: resolved.TextObject, raw: t, Matrix: TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(t.CTM)), position: position, container: container})
		}
	}
	positions := make([]int, len(result.Parts))
	for i, part := range result.Parts {
		positions[i] = part.position.Index
	}
	slices.Sort(positions)
	for i := 1; i < len(positions); i++ {
		if positions[i] != positions[i-1]+1 {
			return result, fmt.Errorf("text selection skips unselected container members")
		}
	}
	for i := range result.Parts {
		p := &result.Parts[i]
		if p.container != result.Parts[0].container || p.position.Container != result.Parts[0].position.Container || p.position.Layer != result.Parts[0].position.Layer {
			return result, fmt.Errorf("text selection crosses containers")
		}
		t := p.TextObject
		m := p.Matrix
		if math.Abs(m.a-m.d) > 1e-9*math.Max(m.a, m.d) {
			result.Reflow = false
		}
		if t.ReadDirection != 0 || t.CharDirection != 0 || math.Abs(m.b) > 1e-9 || math.Abs(m.c) > 1e-9 || m.a <= 0 || m.d <= 0 {
			return result, fmt.Errorf("text selection requires unambiguous horizontal character mapping")
		}
		if err := validateTextGlyphs(t, 1<<16); err != nil {
			return result, err
		}
		for _, transform := range t.CGTransform {
			if transform.CodeCount != transform.GlyphCount {
				return result, fmt.Errorf("combined glyphs require independent explicit layout")
			}
		}
		points, err := e.selectionTextPoints(t)
		if err != nil {
			return result, err
		}
		p.points = points
		chars := []rune(t.Text())
		var previous Point
		hasPrevious := false
		for j, point := range points {
			if chars[j] == '\n' {
				continue
			}
			p.Points = append(p.Points, point)
			x, y := m.Transform(point.X, point.Y)
			if !finite(x) || !finite(y) || hasPrevious && (point.Y < previous.Y-1e-7 || point.Y > previous.Y+1e-7 && chars[j-1] != '\n' || math.Abs(point.Y-previous.Y) < 1e-7 && point.X <= previous.X) {
				return result, fmt.Errorf("ambiguous text reading order")
			}
			previous, hasPrevious = point, true
			result.Bounds = unionTextBox(result.Bounds, m.TransformBox(Box{X: point.X, Y: point.Y - t.Size, W: t.Size, H: t.Size}))
		}
		if t.Clips != nil || len(t.Actions) != 0 || t.DrawParam != "" || t.Alpha != nil && *t.Alpha != 255 || t.Visible != nil && !*t.Visible || t.Stroke != nil && *t.Stroke || t.Fill != nil && !*t.Fill || t.HScale != 0 && t.HScale != 1 || t.Weight != 0 && t.Weight != 400 && t.Weight != 700 {
			result.Reflow = false
		}
	}
	slices.SortFunc(result.Parts, func(a, b TextSelectionPart) int {
		ax, ay := a.Matrix.Transform(a.points[0].X, a.points[0].Y)
		bx, by := b.Matrix.Transform(b.points[0].X, b.points[0].Y)
		if ay < by {
			return -1
		}
		if ay > by {
			return 1
		}
		if ax < bx {
			return -1
		}
		if ax > bx {
			return 1
		}
		return 0
	})
	for start := 0; start < len(result.Parts); {
		first := result.Parts[start]
		_, anchor := first.Matrix.Transform(first.points[0].X, first.points[0].Y)
		end := start + 1
		for end < len(result.Parts) {
			part := result.Parts[end]
			_, y := part.Matrix.Transform(part.points[0].X, part.points[0].Y)
			if y-anchor > 1e-6 {
				break
			}
			end++
		}
		slices.SortStableFunc(result.Parts[start:end], func(a, b TextSelectionPart) int {
			ax, _ := a.Matrix.Transform(a.points[0].X, a.points[0].Y)
			bx, _ := b.Matrix.Transform(b.points[0].X, b.points[0].Y)
			if ax < bx {
				return -1
			}
			if ax > bx {
				return 1
			}
			return 0
		})
		start = end
	}
	var baseline, lastEnd, lastOrigin, previousSize, rowStart float64
	var text strings.Builder
	offset := 0
	for i := range result.Parts {
		p := &result.Parts[i]
		x, y := p.Matrix.Transform(p.points[0].X, p.points[0].Y)
		if i > 0 && (y < baseline-1e-6 || math.Abs(y-baseline) < 1e-6 && (x <= lastOrigin+1e-7 || x < lastEnd-1e-7)) {
			return result, fmt.Errorf("text objects overlap or interleave")
		}
		size := p.TextObject.Size * p.Matrix.d
		if i > 0 {
			limit := math.Max(size, previousSize)
			if math.Abs(y-baseline) < 1e-6 && x-lastEnd > limit*8 || y > baseline+1e-6 && (y-baseline > limit*3 || math.Abs(x-rowStart) > limit*2) {
				return result, fmt.Errorf("text selection has ambiguous column or paragraph gaps")
			}
		}
		if i == 0 || y > baseline+1e-6 {
			rowStart = x
		}
		if i > 0 && y > baseline+1e-6 {
			text.WriteByte('\n')
			offset++
			result.RichText.Runs[len(result.RichText.Runs)-1].Text += "\n"
		}
		lastOrigin, baseline = p.Matrix.Transform(p.points[len(p.points)-1].X, p.points[len(p.points)-1].Y)
		font, err := e.editorFont(p.TextObject.Font)
		if err != nil {
			return result, err
		}
		chars := []rune(strings.ReplaceAll(p.TextObject.Text(), "\n", ""))
		glyph := font.GlyphIndex(chars[len(chars)-1])
		for _, mapping := range p.TextObject.CGTransform {
			if len(chars)-1 >= mapping.CodePosition && len(chars)-1 < mapping.CodePosition+mapping.CodeCount {
				glyph = uint16(parseInts(mapping.Glyphs)[len(chars)-1-mapping.CodePosition])
			}
		}
		scale := p.TextObject.HScale
		if scale == 0 {
			scale = 1
		}
		lastEnd = lastOrigin + float64(font.GlyphAdvance(glyph))*p.TextObject.Size/float64(font.UnitsPerEm())*scale*p.Matrix.a
		previousSize = size
		value := p.TextObject.Text()
		p.Start = offset
		text.WriteString(value)
		offset += utf8.RuneCountInString(value)
		p.End = offset
		format := TextFormat{Font: p.TextObject.Font, Size: p.TextObject.Size * p.Matrix.d, Color: "0 0 0", Bold: p.TextObject.Weight == 700, Italic: p.TextObject.Italic}
		if p.TextObject.FillColor != nil {
			c := p.TextObject.FillColor
			if c.Pattern != nil || c.AxialShd != nil || c.RadialShd != nil || c.GouraudShd != nil || c.LaGouraudShd != nil || c.unsupported {
				result.Reflow = false
			}
			color, err := e.Color(p.TextObject.FillColor)
			if err != nil {
				return result, err
			}
			r, g, b, a := color.RGBA()
			if a > 0 && a < 65535 {
				r = r * 65535 / a
				g = g * 65535 / a
				b = b * 65535 / a
			}
			format.Color = fmt.Sprintf("%d %d %d", r>>8, g>>8, b>>8)
			if a != 65535 {
				result.Reflow = false
			}
		}
		result.RichText.Runs = append(result.RichText.Runs, RichTextRun{Text: value, Format: format})
		p.Color = format.Color
	}
	result.Text = text.String()
	if result.Reflow && e.source != nil && (e.source.document.CustomTags.Path != "" || len(e.source.document.CustomTags.CustomTag) != 0) {
		structures, err := e.Structure()
		if err != nil {
			result.Reflow = false
		} else {
			ids := make(map[string]bool, len(result.Parts)+1)
			for _, part := range result.Parts {
				ids[editorResourceID(part.ID)] = true
			}
			if selection.Root != nil {
				ids[editorResourceID(selection.Root.ID)] = true
				for i, index := range selection.Root.Children {
					parent := *selection.Root
					parent.Children = parent.Children[:i]
					member, err := e.CompositeObject(page, parent, index)
					if err != nil {
						result.Reflow = false
						break
					}
					ids[editorResourceID(editorObjectID(member.Object))] = true
				}
			}
			pageID := editorResourceID(e.pages[page].ID)
			for _, structure := range structures {
				if structure.Error != "" || structure.Root == nil || !structure.CanBind {
					result.Reflow = false
					break
				}
				pending := []*StructureNode{structure.Root}
				for len(pending) != 0 && result.Reflow {
					node := pending[len(pending)-1]
					pending = pending[:len(pending)-1]
					for _, ref := range node.References {
						if editorResourceID(ref.PageID) != pageID {
							continue
						}
						for _, id := range ref.ObjectIDs {
							if ids[editorResourceID(id)] {
								result.Reflow = false
							}
						}
					}
					for i := range node.Children {
						pending = append(pending, &node.Children[i])
					}
				}
			}
		}
	}
	return result, nil
}

// selectionTextPoints 读取标准逐字原点，不推断缺失的字符映射
// 入参: t 文字对象
// 返回: []Point 含换行占位的码点原点, error 字体或映射错误
func (e *Editor) selectionTextPoints(t TextObject) ([]Point, error) {
	font, err := e.editorFont(t.Font)
	if err != nil {
		return nil, err
	}
	ascent, _, _ := font.VerticalMetrics()
	var points []Point
	lines := newTextLineScanner(t)
	mappings := make(map[int]CGTransform, len(t.CGTransform))
	for _, mapping := range t.CGTransform {
		mappings[mapping.CodePosition] = mapping
	}
	offset := 0
	for _, code := range textCodeOrigins(t.TextCode, float64(ascent)*t.Size/float64(font.UnitsPerEm())) {
		if lines.next(code) {
			points = append(points, Point{})
		}
		if code.Index != "" {
			return nil, fmt.Errorf("glyph-index text has no character mapping")
		}
		position := NewTextPositioner(code, t.ReadDirection)
		chars := textCodeRunes(code.Value)
		for i := 0; i < len(chars); {
			mapping, ok := mappings[offset+i]
			if !ok {
				points = append(points, position.Next())
				i++
				continue
			}
			if mapping.CodeCount <= 0 || mapping.GlyphCount <= 0 || i+mapping.CodeCount > len(chars) {
				return nil, fmt.Errorf("invalid text character mapping")
			}
			glyphPoints := make([]Point, mapping.GlyphCount)
			for j := range glyphPoints {
				glyphPoints[j] = position.Next()
			}
			for j := 0; j < mapping.CodeCount; j++ {
				points = append(points, glyphPoints[min(j, len(glyphPoints)-1)])
			}
			i += mapping.CodeCount
		}
		offset += len(chars)
	}
	if len(points) == 0 || len(points) != utf8.RuneCountInString(t.Text()) {
		return nil, fmt.Errorf("text selection requires a nonempty character mapping")
	}
	return points, nil
}

// previewTextSelection 统一生成定位更新或显式富文本布局
// 入参: analysis 选区快照, edit 编辑参数
// 返回: TextSelectionPreview 更新预览, error 编辑错误
func (e *Editor) previewTextSelection(analysis TextSelectionAnalysis, edit TextSelectionEdit) (TextSelectionPreview, error) {
	result := TextSelectionPreview{analysis: analysis, Bounds: analysis.Bounds, Font: edit.Font}
	if edit.Reflow != nil {
		if !analysis.Reflow {
			return result, fmt.Errorf("selection appearance cannot be safely reflowed")
		}
		value := cloneEditorData(*edit.Reflow)
		for i := range value.Runs {
			if edit.Font != "" {
				value.Runs[i].Format.Font = edit.Font
			}
			if edit.Size != 0 {
				value.Runs[i].Format.Size = edit.Size
			}
			if edit.Color != "" {
				value.Runs[i].Format.Color = edit.Color
			}
		}
		width := edit.Width
		if width == 0 {
			width = analysis.Bounds.W
		}
		layout, err := e.layoutRichText(value, width)
		if err != nil {
			return result, err
		}
		result.Text = value.Text()
		for _, object := range layout.objects {
			if object.Type != "TextObject" {
				continue
			}
			box, err := ParseBox(object.TextObject.Boundary)
			if err != nil {
				return result, err
			}
			part := TextSelectionPart{Index: -1, TextObject: object.TextObject, Matrix: TranslationMatrix(analysis.Bounds.X+box.X, analysis.Bounds.Y+box.Y).Multiply(NewMatrix(object.TextObject.CTM))}
			part.Color = "0 0 0"
			if object.TextObject.FillColor != nil {
				part.Color = object.TextObject.FillColor.Value
			}
			points, err := e.selectionTextPoints(object.TextObject)
			if err != nil {
				return result, err
			}
			for i, char := range []rune(object.TextObject.Text()) {
				if char != '\n' {
					part.Points = append(part.Points, points[i])
				}
			}
			for i := range analysis.Parts {
				part.Sources = append(part.Sources, i)
			}
			result.Parts = append(result.Parts, part)
		}
		result.Selection = selectionAfterTextEdit(analysis, nil, true)
		return result, nil
	}
	if !utf8.ValidString(edit.Text) || strings.ContainsAny(edit.Text, "\r\t") || edit.Start < 0 || edit.End < edit.Start || edit.End > utf8.RuneCountInString(analysis.Text) {
		return result, fmt.Errorf("invalid text selection range")
	}
	boundaries := richTextBoundaries(analysis.Text)
	if !boundaries[edit.Start] || !boundaries[edit.End] {
		return result, fmt.Errorf("text range splits a grapheme")
	}
	replacement := []rune(edit.Text)
	originalText := []rune(analysis.Text)
	wantedText := string(originalText[:edit.Start]) + edit.Text + string(originalText[edit.End:])
	if len(replacement) != 0 && len(replacement) != edit.End-edit.Start {
		return result, &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("positioned replacement must have equal length or be a deletion")}
	}
	for _, source := range analysis.Parts {
		p := source
		t := cloneEditorData(source.TextObject)
		old := []rune(t.Text())
		var next []rune
		var codes []TextCode
		var transforms []CGTransform
		glyphs := make(map[int]string)
		for _, mapping := range t.CGTransform {
			for j, glyph := range strings.Fields(mapping.Glyphs) {
				glyphs[mapping.CodePosition+j] = glyph
			}
		}
		glyphPosition, retained := 0, 0
		p.Points = nil
		for j, char := range old {
			at := source.Start + j
			original := char
			if char != '\n' {
				glyphPosition++
			}
			if at >= edit.Start && at < edit.End {
				if len(replacement) == 0 {
					continue
				}
				char = replacement[at-edit.Start]
			}
			if (original == '\n') != (char == '\n') {
				return result, &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("changing line breaks requires reflow")}
			}
			next = append(next, char)
			if char == '\n' {
				continue
			}
			point := source.points[j]
			p.Points = append(p.Points, point)
			codes = append(codes, TextCode{X: ofdNumber(point.X), Y: ofdNumber(point.Y), Value: escapeOFDText(string(char))})
			if glyph, ok := glyphs[glyphPosition-1]; ok {
				transforms = append(transforms, CGTransform{CodePosition: retained, CodeCount: 1, GlyphCount: 1, Glyphs: glyph})
			}
			retained++
		}
		p.Deleted = len(codes) == 0
		if !p.Deleted {
			if len(next) == len(old) && (string(next) != string(old) || edit.Font != "" && edit.Font != t.Font) {
				if err := e.RewriteTextWithFont(&t, string(next), edit.Font); err != nil {
					return result, err
				}
			} else if len(next) != len(old) {
				t.TextCode, t.CGTransform, t.layout = codes, transforms, nil
				if err := e.RewriteTextWithFont(&t, t.Text(), edit.Font); err != nil {
					return result, err
				}
			}
			if edit.Size != 0 {
				t.Size = edit.Size / source.Matrix.d
			}
			if edit.Color != "" {
				p.Color = edit.Color
				var r, g, b float64
				fmt.Sscan(edit.Color, &r, &g, &b)
				fill, err := e.RGBColor(color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
				if err != nil {
					return result, err
				}
				t.FillColor = editorStyleColor(fill, t.FillColor)
			}
			if err := validateEditorGeometry(GraphicObject{Type: "TextObject", TextObject: t}); err != nil {
				return result, err
			}
			if result.Text != "" && source.Start > 0 && []rune(analysis.Text)[source.Start-1] == '\n' {
				result.Text += "\n"
			}
			result.Text += t.Text()
		}
		p.TextObject = t
		result.Parts = append(result.Parts, p)
	}
	result.Selection = selectionAfterTextEdit(analysis, result.Parts, false)
	if result.Text != wantedText {
		return result, &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("changing positioned line breaks requires explicit reflow")}
	}
	return result, nil
}

// prepareTextSelectionEdit 在当前隔离视图校验覆盖样式并注册替换字体
// 入参: edit 编辑参数
// 返回: error 字体或样式错误
func (e *Editor) prepareTextSelectionEdit(edit *TextSelectionEdit) error {
	if !finite(edit.Size) || edit.Size < 0 || !finite(edit.Width) || edit.Width < 0 {
		return fmt.Errorf("invalid text selection dimensions")
	}
	if edit.Color != "" {
		if err := creationColor(&FillColor{Value: edit.Color}); err != nil {
			return err
		}
	}
	if len(edit.FontData) > 0 {
		id, err := e.AddFont(FontFile{Data: edit.FontData}, 0)
		if err != nil {
			return err
		}
		edit.Font = id
	}
	return nil
}

// selectionAfterTextEdit 计算删除及重排后的选中位置，不保留旧修订绑定
// 入参: analysis 原选区, parts 更新项, reflow 是否合并为富文本
// 返回: TextSelection 提交后可用于重新枚举的选择
func selectionAfterTextEdit(analysis TextSelectionAnalysis, parts []TextSelectionPart, reflow bool) TextSelection {
	result := TextSelection{Root: analysis.Selection.Root}
	var deleted []int
	if reflow {
		for _, part := range analysis.Parts[1:] {
			deleted = append(deleted, part.Index)
		}
		parts = analysis.Parts[:1]
	} else {
		for _, part := range parts {
			if part.Deleted {
				deleted = append(deleted, part.Index)
			}
		}
	}
	slices.Sort(deleted)
	deleted = slices.Compact(deleted)
	for _, part := range parts {
		if part.Deleted {
			continue
		}
		if result.Root == nil {
			result.IDs = append(result.IDs, part.ID)
			continue
		}
		removed, _ := slices.BinarySearch(deleted, part.Index)
		result.Indexes = append(result.Indexes, part.Index-removed)
	}
	return result
}

// selectionUpdatedText 仅更新请求字段，保留源对象继承样式与排版状态
// 入参: part 预览项, edit 编辑参数
// 返回: TextObject 可提交的原始对象
func selectionUpdatedText(part TextSelectionPart, edit TextSelectionEdit) TextObject {
	next := cloneEditorData(part.raw)
	next.TextCode, next.CGTransform, next.layout = part.TextObject.TextCode, part.TextObject.CGTransform, part.TextObject.layout
	if edit.Font != "" {
		next.Font = part.TextObject.Font
	}
	if edit.Size != 0 {
		next.Size = part.TextObject.Size
	}
	if edit.Color != "" {
		next.FillColor = part.TextObject.FillColor
	}
	return next
}

// reflowTextSelection 将富文本排版结果替换至首个源位置，其他源成员一并删除
// 入参: page 页面索引, selection 原选择, edit 编辑参数, preview 布局预览
// 返回: error 排版或提交错误
func (e *Editor) reflowTextSelection(page int, selection TextSelection, edit TextSelectionEdit, preview TextSelectionPreview) error {
	value := cloneEditorData(*edit.Reflow)
	for i := range value.Runs {
		if edit.Font != "" {
			value.Runs[i].Format.Font = edit.Font
		}
		if edit.Size != 0 {
			value.Runs[i].Format.Size = edit.Size
		}
		if edit.Color != "" {
			value.Runs[i].Format.Color = edit.Color
		}
	}
	box := preview.Bounds
	if edit.Width != 0 {
		box.W = edit.Width
	}
	object, meta, err := e.prepareRichText(box, value)
	if err != nil {
		return err
	}
	first := preview.analysis.Parts[0]
	if selection.Root == nil {
		object.CompositeGraphicUnit.ID = first.ID
		if err := e.updateObjects(page, []GraphicObject{object}, true); err != nil {
			return err
		}
		var deleted []string
		for _, part := range preview.analysis.Parts[1:] {
			deleted = append(deleted, part.ID)
		}
		if len(deleted) > 0 {
			if err := e.DeleteObjects(page, deleted); err != nil {
				return err
			}
		}
		return e.putRichTextMetadata(page, first.ID, meta, -1)
	}
	if err := e.editCompositeObjects(page, *selection.Root, selection.Indexes, func(_ *Renderer, nodes []*editorCompositeNode, _ []CompositeMember) error {
		for i, node := range nodes {
			if selection.Indexes[i] != first.Index {
				node.owner.patches = append(node.owner.patches, editorXMLPatch{node.span.start, node.span.end, nil})
				continue
			}
			inverse, ok := node.parent.Invert()
			if !ok {
				return fmt.Errorf("invalid composite parent transform")
			}
			local := transformEditorMatrix(object, inverse)
			local.CompositeGraphicUnit.ID = node.object.TextObject.ID
			if !node.boundaryInCTM {
				local = compositeBoundary(local, node.parent, false)
			}
			if err := node.update(local); err != nil {
				return err
			}
			node.setState(editorCompositeState{})
		}
		return nil
	}); err != nil {
		return err
	}
	index := first.Index
	for _, part := range preview.analysis.Parts[1:] {
		if part.Index < first.Index {
			index--
		}
	}
	member, err := e.CompositeObject(page, *selection.Root, index)
	if err != nil {
		return err
	}
	return e.putCompositeRichTextMetadata(page, member.Object, meta, -1)
}
