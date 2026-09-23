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
	"errors"
	"fmt"
	"image/color"
	"strings"
	"unicode/utf8"

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
// LeftIndent和RightIndent为左右缩进，FirstLineIndent为每段首行相对左缩进的偏移，单位为毫米
// 零值保持显式换行和左对齐
// Shape显式启用FontShaper，保存为标准TextCode和CGTransform，不改变阅读时的原文定位
type TextLayout struct {
	Shape           bool
	Wrap            bool
	Align           string
	LineHeight      float64
	LetterSpacing   float64
	LeftIndent      float64
	RightIndent     float64
	FirstLineIndent float64
}

// TextStyle 文字样式增量，Font和Color为空、Size为0时保留原值
// Size单位为毫米，Color为OFD的RGB分量字符串，保留原颜色透明度
type TextStyle struct {
	Font  string
	Size  float64
	Color string
}

// StyleText 原子更新同页文字样式，全部校验通过后提交一次撤销记录
// 已知段落重新排版；原布局未知时替换字体和颜色保留坐标，不允许自动调整字号
// 入参: page 页面索引, ids 文字对象标识, style 样式增量
// 返回: error 错误信息
func (e *Editor) StyleText(page int, ids []string, style TextStyle) error {
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	updates, err := e.styleTextObjects(objects, style)
	if err != nil {
		return err
	}
	return e.UpdateObjects(page, updates)
}

// UpdateTextContent 修改文字内容及样式，原文保留定位，已知段落按会话选项重排
// 原文等长替换保留全部位移，单个TextCode内增删仅调整局部步进，跨定位段增删需显式重排
// 入参: page 页面索引, id 文字对象标识, value 新内容, style 样式增量
// 返回: error 错误信息
func (e *Editor) UpdateTextContent(page int, id, value string, style TextStyle) error {
	capability, err := e.ObjectCapabilities(page, id)
	if err != nil {
		return err
	}
	if !capability.ReplaceFont {
		return capability.editError()
	}
	object, err := e.Object(page, id)
	if err != nil {
		return err
	}
	updates, err := e.styleTextObjects([]GraphicObject{object}, style)
	if err != nil {
		return err
	}
	text := &updates[0].TextObject
	if capability.LayoutKnown {
		_, layout := text.TextLayout()
		err = e.LayoutText(text, value, layout)
	} else {
		err = e.RewriteText(text, value)
	}
	if err != nil {
		return err
	}
	return e.updateObjects(page, updates, true)
}

// RewriteText 保留普通横向原文的字形定位，不修改对象样式或文档
// 等长替换保留各字原位；单段增删保留前缀和后缀步进，新增字符沿用局部字距
// 不跨定位段增删或自动生成新行，复杂字形映射需单独处理
// 入参: obj 原文字对象, value 新内容
// 返回: error 错误信息
func (e *Editor) RewriteText(obj *TextObject, value string) error {
	if obj.ReadDirection != 0 || obj.CharDirection != 0 || len(obj.CGTransform) != 0 || obj.VScale != 0 || obj.Decoration != "" {
		return &EditError{Code: EditUnsupportedObject, Err: fmt.Errorf("text positioning is not supported for content editing")}
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if !utf8.ValidString(value) || strings.Contains(value, "\t") || strings.Trim(value, "\n") == "" {
		return fmt.Errorf("text must contain UTF-8 characters without tabs")
	}
	check := cloneEditorData(*obj)
	if err := e.prepareText(&check); err != nil {
		var missing *MissingGlyphError
		if !errors.As(err, &missing) {
			return err
		}
	}
	sfnt, err := e.editorFont(obj.Font)
	if err != nil {
		return err
	}
	var missing []rune
	for _, char := range value {
		if char != '\n' && sfnt.GlyphIndex(char) == 0 {
			missing = append(missing, char)
		}
	}
	if err := missingGlyphError(obj.Font, missing); err != nil {
		return err
	}
	old, next := []rune(obj.Text()), []rune(value)
	codes := append([]TextCode(nil), obj.TextCode...)
	if len(old) == len(next) {
		offset := 0
		for i := range codes {
			if obj.textCodeLineBreak(i) {
				if next[offset] != '\n' {
					return &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("changing text lines requires explicit layout")}
				}
				offset++
			}
			end := offset + len(textCodeRunes(codes[i].Value))
			content := string(next[offset:end])
			if strings.Contains(content, "\n") {
				return &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("changing text lines requires explicit layout")}
			}
			if content != string(textCodeRunes(codes[i].Value)) {
				codes[i].Value = escapeOFDText(content)
			}
			offset = end
		}
	} else {
		start := 0
		for start < min(len(old), len(next)) && old[start] == next[start] {
			start++
		}
		end, nextEnd := len(old), len(next)
		for end > start && nextEnd > start && old[end-1] == next[nextEnd-1] {
			end--
			nextEnd--
		}
		if strings.ContainsAny(string(old[start:end])+string(next[start:nextEnd]), "\n") {
			return &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("changing text lines requires explicit layout")}
		}
		offset, changed := 0, false
		for i := range codes {
			if obj.textCodeLineBreak(i) {
				offset++
			}
			runes := textCodeRunes(codes[i].Value)
			limit := offset + len(runes)
			if start >= offset && end <= limit {
				from, to := start-offset, end-offset
				replacement := next[start:nextEnd]
				content := append(append(append([]rune(nil), runes[:from]...), replacement...), runes[to:]...)
				if len(content) == 0 {
					return &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("removing a positioned text run requires explicit layout")}
				}
				unit := obj.Size / float64(sfnt.UnitsPerEm())
				if obj.HScale != 0 {
					unit *= obj.HScale
				}
				advance := func(char rune) float64 { return float64(sfnt.GlyphAdvance(sfnt.GlyphIndex(char))) * unit }
				if codes[i].DeltaX != "" || codes[i].DeltaY == "" {
					codes[i].DeltaX = rewriteTextDeltas(codes[i].DeltaX, runes, replacement, from, to, advance)
				}
				if codes[i].DeltaY != "" {
					codes[i].DeltaY = rewriteTextDeltas(codes[i].DeltaY, runes, replacement, from, to, func(rune) float64 { return 0 })
				}
				codes[i].Value = escapeOFDText(string(content))
				changed = true
				break
			}
			offset = limit
		}
		if !changed {
			return &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("editing across positioned text runs requires explicit layout")}
		}
	}
	check = *obj
	check.TextCode = codes
	if err := validateEditorGeometry(GraphicObject{Type: "TextObject", TextObject: check}); err != nil {
		return err
	}
	obj.TextCode, obj.layout = codes, nil
	return nil
}

// rewriteTextDeltas 局部替换字符步进，保留未修改字符及末尾附加位移
// 入参: value 原位移, old 原字符, replacement 替换字符, start 起点, end 终点, advance 字体步进
// 返回: string 更新后的位移数组
func rewriteTextDeltas(value string, old, replacement []rune, start, end int, advance func(rune) float64) string {
	deltas := parseFloatsWithG(value)
	steps := make([]float64, len(old))
	for i, char := range old {
		steps[i] = advance(char)
		if i < len(old)-1 || len(deltas) >= len(old) {
			if delta, ok := textDelta(deltas, i); ok {
				steps[i] = delta
			}
		}
	}
	tracking := 0.0
	if len(old) > 0 && (len(old) > 1 || len(deltas) != 0) {
		index := min(start, max(0, len(old)-2))
		tracking = steps[index] - advance(old[index])
	}
	result := append([]float64(nil), steps[:start]...)
	for _, char := range replacement {
		result = append(result, advance(char)+tracking)
	}
	result = append(result, steps[end:]...)
	if len(deltas) < len(old) {
		result = result[:len(result)-1]
	} else {
		result = append(result, deltas[len(old):]...)
	}
	numbers := make([]string, len(result))
	for i, delta := range result {
		numbers[i] = ofdNumber(delta)
	}
	return strings.Join(numbers, " ")
}

// styleTextObjects 生成文字样式副本，不提交文档或历史
// 入参: objects 原文字对象, style 样式增量
// 返回: []GraphicObject 更新对象, error 错误信息
func (e *Editor) styleTextObjects(objects []GraphicObject, style TextStyle) ([]GraphicObject, error) {
	updates := make([]GraphicObject, len(objects))
	for i, object := range objects {
		id := editorObjectID(object)
		if object.Type != "TextObject" {
			return nil, fmt.Errorf("object %q is not text", id)
		}
		updates[i] = cloneEditorData(object)
		text := &updates[i].TextObject
		value, layout := text.TextLayout()
		known := e.objectOrigin(id) == nil || text.layout != nil
		fontChanged := style.Font != "" && style.Font != text.Font
		sizeChanged := style.Size != 0 && style.Size != text.Size
		if sizeChanged && !known {
			return nil, &EditError{Code: EditUnsupportedObject, Err: fmt.Errorf("object %q requires explicit paragraph layout before changing size", id)}
		}
		if fontChanged {
			text.Font = style.Font
		}
		if sizeChanged {
			text.Size = style.Size
		}
		if known && (fontChanged || sizeChanged) {
			if err := e.LayoutText(text, value, layout); err != nil {
				return nil, err
			}
		}
		if style.Color != "" {
			if err := creationColor(&FillColor{Value: style.Color}); err != nil {
				return nil, err
			}
			var r, g, b float64
			fmt.Sscan(style.Color, &r, &g, &b)
			value, err := e.RGBColor(color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
			if err != nil {
				return nil, err
			}
			if text.FillColor != nil {
				value.Alpha = text.FillColor.Alpha
			}
			text.FillColor = value
		}
	}
	return updates, nil
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
// 入参: runes 段落字符, advances 含字距的字符步进, width 本地排版宽度, wrap 是否自动折行, spacing 附加字距, indent 首行缩进
// 返回: [][2]int 各行的字符起止索引，左闭右开
func breakTextLines(runes []rune, advances []float64, width float64, wrap bool, spacing, indent float64) [][2]int {
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
		available := width
		if start == 0 {
			available -= indent
		}
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
			if length > available && end > start {
				break
			}
			end = candidate
			next++
			if breaks[end] {
				legal = end
			}
			if length > available {
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
