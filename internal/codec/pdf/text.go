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

package pdf

import (
	"math"
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// MergeReplacement 统一同一替换区段内的坐标原点，不重新塑形或调整字距
// 入参: marks 连续文字图元, replacement 整段原文
// 返回: *pdfgo.TextMark 合并文字, bool 是否可等价合并
func MergeReplacement(marks []*pdfgo.TextMark, replacement *pdfgo.ReplacementText) (*pdfgo.TextMark, bool) {
	first := marks[0]
	style := first.Style
	paint := style.Fill
	if first.Font == nil || len(first.Font.Program) == 0 || first.Mode != 0 || first.Size <= 0 || first.HorizontalScale <= 0 || first.Clip != nil || style.SoftMask != nil || style.Transfer != nil || style.AlphaIsShape || style.FillOverprint || paint.Alpha != 1 || !NormalBlend(style.BlendMode) || paint.Tiling != nil || paint.Axial != nil || paint.Radial != nil || paint.Mesh != nil || paint.Function != nil {
		return nil, false
	}
	inverse, ok := first.Matrix.Inverse()
	if !ok {
		return nil, false
	}
	count := 0
	sequence := first.Sequence
	for _, mark := range marks {
		if mark.Font != first.Font || mark.Size != first.Size || mark.HorizontalScale != first.HorizontalScale || mark.Mode != first.Mode || mark.Clip != nil || len(mark.Glyphs) != len(mark.Positions) || !reflect.DeepEqual(mark.Style, style) {
			return nil, false
		}
		if mark.Matrix[0] != first.Matrix[0] || mark.Matrix[1] != first.Matrix[1] || mark.Matrix[2] != first.Matrix[2] || mark.Matrix[3] != first.Matrix[3] {
			return nil, false
		}
		count += len(mark.Glyphs)
		sequence = commonSequence(sequence, mark.Sequence)
	}
	if count == 0 {
		return nil, false
	}
	merged := *first
	merged.Replacement = replacement
	merged.Sequence = sequence
	merged.Glyphs = make([]pdfgo.Glyph, 0, count)
	merged.Positions = make([]pdfgo.Point, 0, count)
	for _, mark := range marks {
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

// commonSequence 查找两个内容区段的最近共同外层，不将合并内容归入单个子区段
// 入参: first 首个区段, second 后续区段
// 返回: *pdfgo.ContentSequence 共同区段，没有共同区段时为空
func commonSequence(first, second *pdfgo.ContentSequence) *pdfgo.ContentSequence {
	if first == second {
		return first
	}
	if first == nil || second == nil {
		return nil
	}
	depth := func(sequence *pdfgo.ContentSequence) int {
		count := 0
		for ; sequence != nil; sequence = sequence.Parent {
			count++
		}
		return count
	}
	a, b := depth(first), depth(second)
	for ; a > b; a-- {
		first = first.Parent
	}
	for ; b > a; b-- {
		second = second.Parent
	}
	for first != second {
		first, second = first.Parent, second.Parent
	}
	return first
}
