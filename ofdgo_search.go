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
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tdewolff/canvas"
)

// PageText 页面文字，按绘制顺序保留文本对象，不包含图像和签名外观
type PageText struct {
	Runs []TextRun `json:"runs"`
}

// TextRun 文本对象原文及逐字符区域，坐标以页面左上角为原点，单位为毫米
// Boxes与Text的Unicode字符一一对应，缺少字体时使用文本对象区域
type TextRun struct {
	ID    string     `json:"id"`
	Text  string     `json:"text"`
	Boxes []Box      `json:"boxes"`
	Spans []TextSpan `json:"spans"`
}

// TextSpan 可选文字区间，Start和End为Unicode字符偏移
// Matrix依次为a、b、c、d、e、f，将单位矩形映射到页面左上角为原点的毫米坐标
type TextSpan struct {
	Start  int        `json:"start"`
	End    int        `json:"end"`
	Matrix [6]float64 `json:"matrix"`
}

// TextMatch 文字匹配结果，Run为文本对象索引，Start和End为原文Unicode字符区间
type TextMatch struct {
	Run    int    `json:"run"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Before string `json:"before"`
	Text   string `json:"text"`
	After  string `json:"after"`
	Boxes  []Box  `json:"boxes"`
}

// PageText 提取页面及启用注释的原文，复用当前字体配置和渲染位置计算
// 入参: page 页面内容
// 返回: *PageText 页面文字, error 错误信息
func (r *Renderer) PageText(page *PageContent) (*PageText, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	renderer := *r
	renderer.pageText = &PageText{}
	if err := renderer.renderPageToContext(canvas.NewContext(canvas.New(box.W, box.H)), page, false); err != nil {
		return nil, err
	}
	return renderer.pageText, nil
}

// Search 按原文进行忽略大小写的字面匹配，不跨文本对象拼接或识别图像
// 入参: query 搜索文字
// 返回: []TextMatch 匹配结果及上下文
func (p *PageText) Search(query string) []TextMatch {
	var matches []TextMatch
	if query == "" {
		return matches
	}
	query = foldText(query)
	length := utf8.RuneCountInString(query)
	for index, run := range p.Runs {
		text := foldText(run.Text)
		runes := []rune(run.Text)
		codeOffset := 0
		for offset := 0; offset < len(text); {
			found := strings.Index(text[offset:], query)
			if found < 0 {
				break
			}
			start := codeOffset + utf8.RuneCountInString(text[offset:offset+found])
			offset += found
			end := start + length
			match := TextMatch{
				Run: index, Start: start, End: end,
				Before: string(runes[max(0, start-16):start]),
				Text:   string(runes[start:end]),
				After:  string(runes[end:min(len(runes), end+16)]),
			}
			for _, box := range run.Boxes[start:end] {
				if box.W > 0 && box.H > 0 && (len(match.Boxes) == 0 || match.Boxes[len(match.Boxes)-1] != box) {
					match.Boxes = append(match.Boxes, box)
				}
			}
			matches = append(matches, match)
			offset += len(query)
			codeOffset = end
		}
	}
	return matches
}

// foldText 统一Unicode大小写等价字符，保留字符数量
// 入参: text 原文
// 返回: string 匹配用文字
func foldText(text string) string {
	return strings.Map(func(char rune) rune {
		folded := char
		for next := unicode.SimpleFold(char); next != char; next = unicode.SimpleFold(next) {
			folded = min(folded, next)
		}
		return folded
	}, text)
}

// addRun 收集原始文本，字形索引不替代Unicode原文
// 入参: obj 文本对象
// 返回: *TextRun 文本及待填充的字符区域
func (p *PageText) addRun(obj TextObject) *TextRun {
	var text strings.Builder
	for _, code := range obj.TextCode {
		if code.Index == "" {
			text.WriteString(string(textCodeRunes(code.Value)))
		} else {
			text.WriteRune('\uFFFC')
		}
	}
	p.Runs = append(p.Runs, TextRun{ID: obj.ID, Text: text.String(), Boxes: make([]Box, utf8.RuneCountInString(text.String()))})
	return &p.Runs[len(p.Runs)-1]
}

// textGlyphSpans 将绘制字形映射到原文字符区间
// 入参: runes 原文字符, transforms 字形变换, offset 文本编码偏移
// 返回: [][2]int 每个字形对应的字符起止位置
func textGlyphSpans(runes []rune, transforms map[int]textGlyphTransform, offset int) [][2]int {
	spans := make([][2]int, 0, len(runes))
	for i := 0; i < len(runes); {
		if transform, ok := transforms[offset+i]; ok && i+transform.CodeCount <= len(runes) {
			for range transform.Glyphs {
				spans = append(spans, [2]int{i, i + transform.CodeCount})
			}
			i += transform.CodeCount
		} else {
			spans = append(spans, [2]int{i, i + 1})
			i++
		}
	}
	return spans
}

// textPathBox 将字形轮廓转换为页面区域
// 入参: path 字形轮廓, pageH 页面高度
// 返回: Box 字符区域
func textPathBox(path *canvas.Path, pageH float64) Box {
	if path.Empty() {
		return Box{}
	}
	bounds := path.Bounds()
	return Box{X: bounds.X0, Y: pageH - bounds.Y1, W: bounds.X1 - bounds.X0, H: bounds.Y1 - bounds.Y0}
}

// unionTextBox 合并同一字符的字形区域
// 入参: a 已有区域, b 新区域
// 返回: Box 合并区域
func unionTextBox(a, b Box) Box {
	if a.W <= 0 || a.H <= 0 {
		return b
	}
	if b.W <= 0 || b.H <= 0 {
		return a
	}
	x, y := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	return Box{X: x, Y: y, W: math.Max(a.X+a.W, b.X+b.W) - x, H: math.Max(a.Y+a.H, b.Y+b.H) - y}
}

// addSpan 收集字形选择区域，同一原文区间的多个字形合并为一项
// 入参: start 起始字符, end 结束字符, bounds 字形布局区域, m 字形变换, clip 裁剪路径, pageH 页面高度
func (run *TextRun) addSpan(start, end int, bounds canvas.Rect, m canvas.Matrix, clip *canvas.Path, pageH float64) {
	if m.Det() == 0 {
		return
	}
	if clip != nil {
		path := canvas.Rectangle(bounds.W(), bounds.H()).Translate(bounds.X0, bounds.Y0).Transform(m)
		path = applyClipPath(path, clip)
		if path.Empty() {
			return
		}
		bounds = path.Transform(m.Inv()).Bounds()
	}
	if bounds.W() < 0 || bounds.H() <= 0 {
		return
	}
	m = canvas.Matrix{{1, 0, 0}, {0, -1, pageH}}.Mul(m).Translate(bounds.X0, bounds.Y1).Scale(bounds.W(), -bounds.H())
	span := TextSpan{Start: start, End: end}
	if n := len(run.Spans); n > 0 && run.Spans[n-1].Start == start && run.Spans[n-1].End == end {
		span = run.Spans[n-1]
		v := span.Matrix
		previous := canvas.Matrix{{v[0], v[2], v[4]}, {v[1], v[3], v[5]}}
		if previous.Det() != 0 {
			bounds = canvas.Rect{X1: 1, Y1: 1}.Add(canvas.Rect{X1: 1, Y1: 1}.Transform(previous.Inv().Mul(m)))
			m = previous.Translate(bounds.X0, bounds.Y0).Scale(bounds.W(), bounds.H())
		}
		run.Spans = run.Spans[:n-1]
	}
	span.Matrix = [6]float64{m[0][0], m[1][0], m[0][1], m[1][1], m[0][2], m[1][2]}
	run.Spans = append(run.Spans, span)
}
