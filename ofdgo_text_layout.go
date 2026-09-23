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
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// LayoutText 按自有字体度量重排横向文字，保留绘制属性，可显式启用字体塑形
// 不修改文档，通过AddObject或UpdateObject提交；选项仅供当前编辑过程使用，保存为标准文字定位
// 入参: obj 文字对象, value 原文, options 段落排版选项, metrics 字体度量
// 返回: error 错误信息
func LayoutText(obj *TextObject, value string, options TextLayout, metrics FontMetrics) error {
	if obj == nil || metrics == nil {
		return fmt.Errorf("text object and font metrics are required")
	}
	if metrics.UnitsPerEm() == 0 {
		return fmt.Errorf("font units per em must be positive")
	}
	if obj.ReadDirection != 0 || obj.CharDirection != 0 {
		return fmt.Errorf("automatic text layout requires horizontal text")
	}
	if !finite(obj.Size) || obj.Size <= 0 || !finite(obj.HScale) || obj.HScale < 0 {
		return fmt.Errorf("invalid text dimensions")
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if !utf8.ValidString(value) || strings.Contains(value, "\t") || strings.Trim(value, "\n") == "" {
		return fmt.Errorf("text must contain UTF-8 characters without tabs")
	}
	lineHeight := options.LineHeight
	if !finite(lineHeight) || lineHeight < 0 {
		return fmt.Errorf("line height must be finite and nonnegative")
	}
	if !finite(options.LetterSpacing) {
		return fmt.Errorf("letter spacing must be finite")
	}
	if !finite(options.LeftIndent) || !finite(options.RightIndent) || !finite(options.FirstLineIndent) {
		return fmt.Errorf("paragraph indents must be finite")
	}
	if !slices.Contains([]string{"", "left", "center", "right", "justify"}, options.Align) {
		return fmt.Errorf("invalid text alignment %q", options.Align)
	}
	var width float64
	if options.Wrap || options.Align != "" && options.Align != "left" || options.LeftIndent != 0 || options.RightIndent != 0 || options.FirstLineIndent != 0 {
		box, err := obj.TextFrame()
		if err != nil {
			return err
		}
		width = box.W - options.LeftIndent - options.RightIndent
		if !finite(width) || width <= 0 || !finite(width-options.FirstLineIndent) || width-options.FirstLineIndent <= 0 {
			return fmt.Errorf("paragraph indents leave no text width")
		}
	}
	hScale := obj.HScale
	if hScale == 0 {
		hScale = 1
	}
	ascender, descender, gap := metrics.VerticalMetrics()
	unit := obj.Size / float64(metrics.UnitsPerEm())
	if lineHeight == 0 {
		lineHeight = math.Max(obj.Size, float64(int(ascender)+int(descender)+int(gap))*unit)
	}
	if options.Shape || options.Shaping != (TextShapeOptions{}) {
		return layoutShapedText(obj, value, options, metrics, width, lineHeight, hScale, float64(ascender)*unit)
	}
	var codes []TextCode
	var missing []rune
	for _, paragraph := range strings.Split(value, "\n") {
		runes := []rune(paragraph)
		advances := make([]float64, len(runes))
		for i, char := range runes {
			glyph := metrics.GlyphIndex(char)
			if glyph == 0 {
				missing = append(missing, char)
				continue
			}
			advances[i] = float64(metrics.GlyphAdvance(glyph)) * unit * hScale
		}
		if len(missing) != 0 {
			continue
		}
		spaceTextAdvances(runes, advances, options.LetterSpacing)
		for _, advance := range advances {
			if !finite(advance) {
				return fmt.Errorf("text advance exceeds finite range")
			}
		}
		lines := breakTextLines(runes, advances, width, options.Wrap, options.LetterSpacing, options.FirstLineIndent)
		for i, line := range lines {
			indent := 0.0
			if i == 0 {
				indent = options.FirstLineIndent
			}
			x, deltas := alignTextLine(runes[line[0]:line[1]], advances[line[0]:line[1]], width-indent, options.Align, i+1 < len(lines), options.LetterSpacing)
			x += options.LeftIndent + indent
			y := float64(ascender)*unit + float64(len(codes))*lineHeight
			if !finite(x) || !finite(y) {
				return fmt.Errorf("text position exceeds finite range")
			}
			codes = append(codes, TextCode{X: ofdNumber(x), Y: ofdNumber(y), DeltaX: deltas, Value: escapeOFDText(string(runes[line[0]:line[1]]))})
		}
	}
	if err := missingGlyphError(obj.Font, missing); err != nil {
		return err
	}
	obj.TextCode = codes
	obj.CGTransform = nil
	obj.layout = nil
	if options != (TextLayout{}) {
		obj.layout = &textLayout{value: value, options: options}
	}
	return nil
}
