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
	"strings"

	"github.com/go-text/typesetting/segmenter"
)

// MissingGlyphError 字体缺字错误，FontID为OFD字体资源标识，Characters按首次出现顺序保留不重复的缺失字符
// 可通过errors.As从编辑错误中取得，不表示字体缺失或文件损坏
type MissingGlyphError struct {
	FontID     string `json:"fontID"`
	Characters string `json:"characters"`
}

// Error 返回缺失字符的Unicode码点
// 返回: string 错误描述
func (e *MissingGlyphError) Error() string {
	var codes []string
	for _, char := range e.Characters {
		codes = append(codes, fmt.Sprintf("U+%04X", char))
	}
	return fmt.Sprintf("font %q does not contain %s", e.FontID, strings.Join(codes, ", "))
}

// missingGlyphError 汇总缺失字符，无缺字时返回nil
// 入参: id 字体资源标识, characters 缺失字符
// 返回: error 缺字诊断
func missingGlyphError(id string, characters []rune) error {
	if len(characters) == 0 {
		return nil
	}
	seen := make(map[rune]bool)
	var value strings.Builder
	for _, char := range characters {
		if !seen[char] {
			seen[char] = true
			value.WriteRune(char)
		}
	}
	return &MissingGlyphError{FontID: id, Characters: value.String()}
}

// TextLayout 本地横向段落选项，Wrap按CTM变换前的边界宽度折行，Align为left、center、right或justify
// LineHeight为毫米单位的基线间距，0使用字体度量；LetterSpacing为字素间的附加毫米间距，可为负
// 零值保持显式换行和左对齐
type TextLayout struct {
	Wrap          bool
	Align         string
	LineHeight    float64
	LetterSpacing float64
}

// textLayout 保存编辑中的原文与选项，不写入OFD，也不参与渲染
type textLayout struct {
	value   string
	options TextLayout
}

// TextLayout 获取最近一次排版的原文与选项，区分软换行和显式换行
// 信息仅在当前编辑过程保留，读取OFD时返回定位后的文字和默认选项
// 返回: string 原文, TextLayout 排版选项
func (obj TextObject) TextLayout() (string, TextLayout) {
	if obj.layout != nil {
		return obj.layout.value, obj.layout.options
	}
	return obj.Text(), TextLayout{}
}

// breakTextLines 优先使用Unicode断行机会，过长词仅在字素边界折行，不丢弃空白
// 入参: runes 段落字符, advances 含字距的字符步进, width 本地排版宽度, wrap 是否自动折行, spacing 附加字距
// 返回: [][2]int 各行的字符起止索引，左闭右开
func breakTextLines(runes []rune, advances []float64, width float64, wrap bool, spacing float64) [][2]int {
	if !wrap || len(runes) == 0 {
		return [][2]int{{0, len(runes)}}
	}
	var seg segmenter.Segmenter
	seg.Init(runes)
	breaks := make([]bool, len(runes)+1)
	for it := seg.LineIterator(); it.Next(); {
		line := it.Line()
		breaks[line.Offset+len(line.Text)] = true
	}
	var ends []int
	for it := seg.GraphemeIterator(); it.Next(); {
		cluster := it.Grapheme()
		ends = append(ends, cluster.Offset+len(cluster.Text))
	}
	sums := make([]float64, len(runes)+1)
	for i, advance := range advances {
		sums[i+1] = sums[i] + advance
	}
	var lines [][2]int
	for start, cursor := 0, 0; start < len(runes); {
		end, legal, next := start, start, cursor
		for next < len(ends) {
			candidate := ends[next]
			visible := candidate
			for visible > start && runes[visible-1] == ' ' {
				visible--
			}
			length := sums[visible] - sums[start]
			if visible > start {
				length -= spacing
			}
			if length > width && end > start {
				break
			}
			end = candidate
			next++
			if breaks[end] {
				legal = end
			}
			if length > width {
				break
			}
		}
		if end < len(runes) && legal > start {
			end = legal
		}
		lines = append(lines, [2]int{start, end})
		start = end
		for cursor < len(ends) && ends[cursor] <= start {
			cursor++
		}
	}
	return lines
}

// alignTextLine 将段落对齐转换为标准X和DeltaX，两端对齐保留段落末行左对齐
// 入参: runes 行内字符, advances 含字距的字符步进, width 本地排版宽度, alignment 对齐方式, justify 是否允许本行两端对齐, spacing 附加字距
// 返回: float64 行首X坐标, string 字符间的DeltaX序列
func alignTextLine(runes []rune, advances []float64, width float64, alignment string, justify bool, spacing float64) (float64, string) {
	end := len(runes)
	for end > 0 && runes[end-1] == ' ' {
		end--
	}
	length := 0.0
	for _, advance := range advances[:end] {
		length += advance
	}
	if end > 0 {
		length -= spacing
	}
	x, extra := 0.0, max(0, width-length)
	if alignment == "center" {
		x = extra / 2
	} else if alignment == "right" {
		x = extra
	}
	gaps := make(map[int]bool)
	if alignment == "justify" && justify && extra > 0 {
		var seg segmenter.Segmenter
		seg.Init(runes[:end])
		for it := seg.LineIterator(); it.Next(); {
			line := it.Line()
			at := line.Offset + len(line.Text)
			if at < end {
				gaps[at-1] = true
			}
		}
	}
	deltas := make([]string, max(0, len(runes)-1))
	for i := range deltas {
		advance := advances[i]
		if gaps[i] {
			advance += extra / float64(len(gaps))
		}
		deltas[i] = ofdNumber(advance)
	}
	return x, strings.Join(deltas, " ")
}

// spaceTextAdvances 将字距加入各字素末尾，不拆散组合字符
// 入参: runes 段落字符, advances 待更新的字符步进, spacing 附加毫米字距
func spaceTextAdvances(runes []rune, advances []float64, spacing float64) {
	if spacing == 0 {
		return
	}
	var seg segmenter.Segmenter
	seg.Init(runes)
	for it := seg.GraphemeIterator(); it.Next(); {
		cluster := it.Grapheme()
		advances[cluster.Offset+len(cluster.Text)-1] += spacing
	}
}
