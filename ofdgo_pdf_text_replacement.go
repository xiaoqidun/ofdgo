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
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

// pdfTextReplacement 记录一个完整替换区段的文字数量与转换状态
type pdfTextReplacement struct {
	text    *pdfgo.ReplacementText
	count   int
	offset  int64
	blocked bool
	applied bool
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

// markedContent 核对已接入的替换文本属性，不静默丢弃其他内容语义
// 入参: mark 内容标记
// 返回: error 未支持的语义错误
func (r *pdfTextReplacements) markedContent(mark pdfgo.MarkedContentMark) error {
	if mark.Operator == "EMC" {
		return nil
	}
	if mark.Tag == "OC" {
		kind, err := r.importer.reader.Resolve(mark.Properties["Type"])
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

// apply 将完整且样式一致的区段合并为标准OFD文字对象
// 入参: nodes 原始图元
// 返回: []pdfCompositeNode 转换图元, error 取消或语义转换错误
func (r *pdfTextReplacements) apply(nodes []pdfCompositeNode) ([]pdfCompositeNode, error) {
	if len(r.order) == 0 {
		return nodes, nil
	}
	r.count(nodes, false)
	var err error
	nodes, err = r.merge(nodes)
	if err != nil {
		return nil, err
	}
	for _, span := range r.order {
		if span.applied || !span.blocked && span.count == 0 && span.text.Text == "" {
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

// count 统计完整区段中的文字图元，排除需要组级合成的内容
// 入参: nodes 图元, blocked 上层组是否阻止直接转换
func (r *pdfTextReplacements) count(nodes []pdfCompositeNode, blocked bool) {
	for _, node := range nodes {
		if node.text != nil {
			if span := r.span(node.text.Replacement); span != nil {
				span.count++
				span.blocked = span.blocked || blocked
			}
		}
		childBlocked := blocked
		if group := node.group; group != nil && node.textObject == nil {
			opaque, err := node.opaque(group.ColorSpace)
			childBlocked = childBlocked || !opaque || err != nil || group.ColorSpace != nil && !group.ColorSpace.SRGBEquivalent()
		}
		r.count(node.children, childBlocked)
	}
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
			if span := r.span(first.Replacement); span != nil && !span.applied && !span.blocked && span.text.Text != "" {
				var marks []pdfCompositeNode
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
					marks = append(marks, children...)
				}
				end = max(end, i+1)
				if len(marks) == span.count {
					if mark, ok := pdfMergeReplacement(marks, span.text); ok {
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

// clear 清除未转换的区段引用，防止局部绘制重复使用整段原文
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

// pdfMergeReplacement 统一同一替换区段内的坐标原点，不重新塑形或调整字距
// 入参: nodes 连续文字图元, replacement 整段原文
// 返回: *pdfgo.TextMark 合并文字, bool 是否可等价合并
func pdfMergeReplacement(nodes []pdfCompositeNode, replacement *pdfgo.ReplacementText) (*pdfgo.TextMark, bool) {
	first := nodes[0].text
	style := first.Style
	paint := style.Fill
	if first.Font == nil || len(first.Font.Program) == 0 || first.Mode != 0 || first.Size <= 0 || first.HorizontalScale <= 0 || first.Clip != nil || style.SoftMask != nil || style.Transfer != nil || style.AlphaIsShape || style.FillOverprint || paint.Alpha != 1 || !pdfNormalBlend(style.BlendMode) || paint.Tiling != nil || paint.Axial != nil || paint.Radial != nil || paint.Mesh != nil || paint.Function != nil {
		return nil, false
	}
	inverse, ok := first.Matrix.Inverse()
	if !ok {
		return nil, false
	}
	count := 0
	for _, node := range nodes {
		mark := node.text
		if mark.Font != first.Font || mark.Size != first.Size || mark.HorizontalScale != first.HorizontalScale || mark.Mode != first.Mode || mark.Clip != nil || len(mark.Glyphs) != len(mark.Positions) || !reflect.DeepEqual(mark.Style, style) {
			return nil, false
		}
		if mark.Matrix[0] != first.Matrix[0] || mark.Matrix[1] != first.Matrix[1] || mark.Matrix[2] != first.Matrix[2] || mark.Matrix[3] != first.Matrix[3] {
			return nil, false
		}
		count += len(mark.Glyphs)
	}
	if count == 0 {
		return nil, false
	}
	merged := *first
	merged.Replacement = replacement
	merged.Glyphs = make([]pdfgo.Glyph, 0, count)
	merged.Positions = make([]pdfgo.Point, 0, count)
	for _, node := range nodes {
		mark := node.text
		dx, dy := mark.Matrix[4]-first.Matrix[4], mark.Matrix[5]-first.Matrix[5]
		offset := pdfgo.Point{X: inverse[0]*dx + inverse[2]*dy, Y: inverse[1]*dx + inverse[3]*dy}
		for _, point := range mark.Positions {
			point.X += offset.X
			point.Y += offset.Y
			if math.IsNaN(point.X) || math.IsNaN(point.Y) || math.IsInf(point.X, 0) || math.IsInf(point.Y, 0) {
				return nil, false
			}
			merged.Positions = append(merged.Positions, point)
		}
		merged.Glyphs = append(merged.Glyphs, mark.Glyphs...)
	}
	return &merged, true
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
