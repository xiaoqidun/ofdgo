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
	"encoding/xml"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// textPageTokens 保留文字及其复合图元和裁剪，跳过独立路径与图片
type textPageTokens struct {
	*xml.Decoder
	parents []string
}

// Token 读取文字页面所需的XML节点
// 返回: xml.Token XML节点, error 错误信息
func (d *textPageTokens) Token() (xml.Token, error) {
	for {
		token, err := d.Decoder.Token()
		if err != nil {
			return nil, err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if len(d.parents) > 0 && (node.Name.Local == "PathObject" || node.Name.Local == "ImageObject") {
				switch d.parents[len(d.parents)-1] {
				case "Layer", "PageBlock", "CompositeGraphicUnit", "CompositeObject":
					if err := d.Decoder.Skip(); err != nil {
						return nil, err
					}
					continue
				}
			}
			d.parents = append(d.parents, node.Name.Local)
		case xml.EndElement:
			d.parents = d.parents[:len(d.parents)-1]
		}
		return token, nil
	}
}

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

// TextMatch 文字匹配结果，ID为对象标识，Run为文本对象索引，Start和End为原文Unicode字符区间
type TextMatch struct {
	ID     string `json:"id"`
	Run    int    `json:"run"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Before string `json:"before"`
	Text   string `json:"text"`
	After  string `json:"after"`
	Boxes  []Box  `json:"boxes"`
}

// PageText 提取页面及启用注释的原文，复用当前编译器的字体和几何语义
// 入参: page 页面内容
// 返回: *PageText 页面文字, error 错误信息
func (r *Renderer) PageText(page *PageContent) (*PageText, error) {
	if r.backends.Compiler == nil {
		return nil, fmt.Errorf("%w: page text", ErrBackendUnavailable)
	}
	return r.backends.Compiler.PageText(r, page)
}

// PageTextByIndex 按页面索引提取文字，避免解析独立路径和图片图元
// 入参: index 页面索引，从0开始
// 返回: *PageText 页面文字, error 错误信息
func (r *Renderer) PageTextByIndex(index int) (*PageText, error) {
	doc, err := r.Reader.Doc()
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Pages.Page) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	page, err := r.Reader.readPageContent(doc.Pages.Page[index], true)
	if err != nil {
		return nil, err
	}
	return r.PageText(page)
}

// String 按文本对象顺序合并原文，以换行分隔非空对象
// 返回: string 页面原文
func (p *PageText) String() string {
	var text strings.Builder
	for _, run := range p.Runs {
		if run.Text == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(run.Text)
	}
	return text.String()
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
		var runes []rune
		codeOffset := 0
		for offset := 0; offset < len(text); {
			found := strings.Index(text[offset:], query)
			if found < 0 {
				break
			}
			if runes == nil {
				runes = []rune(run.Text)
			}
			start := codeOffset + utf8.RuneCountInString(text[offset:offset+found])
			offset += found
			end := start + length
			match := TextMatch{
				ID:  run.ID,
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
	text := obj.Text()
	p.Runs = append(p.Runs, TextRun{ID: obj.ID, Text: text, Boxes: make([]Box, utf8.RuneCountInString(text))})
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
