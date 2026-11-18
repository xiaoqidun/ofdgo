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
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-text/typesetting/segmenter"
	"golang.org/x/text/unicode/bidi"
)

// LayoutText 按自有字体度量重排横向文字，保留绘制属性，可显式启用字体塑形
// 不修改文档，通过AddObject或UpdateObject提交；选项随标准文字定位保存，重新打开时恢复
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
	if !slices.Contains([]string{"", "left", "center", "right", "justify", "distributed"}, options.Align) {
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
	if options.AutoBidi && !options.Shaping.Bidi {
		for _, char := range value {
			properties, _ := bidi.LookupRune(char)
			switch properties.Class() {
			case bidi.R, bidi.AL, bidi.RLE, bidi.RLO, bidi.RLI, bidi.FSI:
				options.Shaping.Bidi = true
			}
			if options.Shaping.Bidi {
				break
			}
		}
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
			x, deltas := alignTextLine(runes[line[0]:line[1]], advances[line[0]:line[1]], width-indent, options.Align, i+1 < len(lines), options.LetterSpacing, nil)
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
	autoWidth := options == (TextLayout{})
	if obj.layout != nil && obj.layout.options == options {
		autoWidth = obj.layout.autoWidth
	}
	obj.layout = &textLayout{value: value, options: options, autoWidth: autoWidth}
	return nil
}

// configuredTextShaper 将会话选项传递给每次换行后的塑形
type configuredTextShaper struct {
	FontShaperOptions
	options TextShapeOptions
}

// ShapeText 使用固定选项塑形单行原文
// 入参: value 原文, size 毫米字号
// 返回: []ShapedGlyph 字形, error 塑形错误
func (s configuredTextShaper) ShapeText(value string, size float64) ([]ShapedGlyph, error) {
	return s.ShapeTextWithOptions(value, size, s.options)
}

// layoutShapedText 将显式塑形结果保存为标准文字定位，成功前不修改对象
// 入参: obj 文字对象, value 原文, options 排版选项, metrics 字体度量, width 行宽, lineHeight 行高, hScale 横向比例, baseline 首行基线
// 返回: error 塑形或排版错误
func layoutShapedText(obj *TextObject, value string, options TextLayout, metrics FontMetrics, width, lineHeight, hScale, baseline float64) error {
	shaper, ok := metrics.(FontShaper)
	if !ok {
		return fmt.Errorf("font shaping: %w", ErrBackendUnavailable)
	}
	if options.Shaping != (TextShapeOptions{}) {
		configured, ok := metrics.(FontShaperOptions)
		if !ok {
			return fmt.Errorf("font shaping options: %w", ErrBackendUnavailable)
		}
		shaper = configuredTextShaper{FontShaperOptions: configured, options: options.Shaping}
	}
	var codes []TextCode
	var transforms []CGTransform
	offset := 0
	for _, paragraph := range strings.Split(value, "\n") {
		runes := []rune(paragraph)
		paragraphGlyphs, advances, err := shapeTextLine(shaper, runes, obj.Font, obj.Size, hScale, options.LetterSpacing)
		if err != nil {
			return err
		}
		lines := breakTextLines(runes, advances, width, options.Wrap, options.LetterSpacing, options.FirstLineIndent)
		for i := 0; i < len(lines); i++ {
			line := lines[i]
			indent := 0.0
			if i == 0 {
				indent = options.FirstLineIndent
			}
			available := width - indent
			glyphs, lineAdvances := paragraphGlyphs, advances
			if line[0] != 0 || line[1] != len(runes) {
				glyphs, lineAdvances, err = shapeTextLine(shaper, runes[line[0]:line[1]], obj.Font, obj.Size, hScale, options.LetterSpacing)
				if err != nil {
					return err
				}
			}
			for options.Wrap && shapedLineWidth(runes[line[0]:line[1]], lineAdvances, options.LetterSpacing) > available {
				var seg segmenter.Segmenter
				seg.Init(runes[line[0]:line[1]])
				previous := 0
				for it := seg.GraphemeIterator(); it.Next(); {
					previous = it.Grapheme().Offset
				}
				if previous == 0 {
					break
				}
				oldEnd := line[1]
				line[1] = line[0] + previous
				if i+1 < len(lines) {
					lines[i+1][0] = line[1]
				} else {
					lines = append(lines, [2]int{line[1], oldEnd})
				}
				lines[i] = line
				glyphs, lineAdvances, err = shapeTextLine(shaper, runes[line[0]:line[1]], obj.Font, obj.Size, hScale, options.LetterSpacing)
				if err != nil {
					return err
				}
			}
			chars := runes[line[0]:line[1]]
			var clusters map[int]bool
			if options.Align == "distributed" {
				clusters = make(map[int]bool)
				for _, glyph := range glyphs {
					clusters[glyph.Cluster] = true
				}
			}
			x, delta := alignTextLine(chars, lineAdvances, available, options.Align, i+1 < len(lines), options.LetterSpacing, clusters)
			if options.Shaping.Direction == "rtl" {
				for j := len(chars) - 1; j >= 0 && chars[j] == ' '; j-- {
					x -= lineAdvances[j]
				}
			}
			x += options.LeftIndent + indent
			y := baseline + float64(len(codes))*lineHeight
			if !finite(x) || !finite(y) {
				return fmt.Errorf("text position exceeds finite range")
			}
			adjusted := parseFloats(delta)
			extra := make([]float64, len(chars)+1)
			raw := make([]float64, len(chars))
			for _, glyph := range glyphs {
				raw[glyph.Cluster] += glyph.Advance * hScale
			}
			for j := range chars {
				advance := lineAdvances[j]
				if j < len(adjusted) {
					advance = adjusted[j]
				}
				extra[j+1] = extra[j] + advance - raw[j]
			}
			code := TextCode{X: ofdNumber(x), Y: ofdNumber(y), Value: escapeOFDText(string(chars))}
			var dx, dy []string
			var lastX, lastY float64
			var visualShifts map[int]float64
			if options.Shaping.Bidi {
				visualShifts = bidiClusterShifts(glyphs, extra)
			}
			for j, glyph := range glyphs {
				shift := extra[glyph.Cluster]
				if options.Shaping.Bidi {
					shift = visualShifts[glyph.Cluster]
				} else if options.Shaping.Direction == "rtl" {
					shift = extra[len(chars)] - extra[glyph.Cluster] - options.LetterSpacing
				}
				gx, gy := x+glyph.X*hScale+shift, y+glyph.Y
				if !finite(gx) || !finite(gy) {
					return fmt.Errorf("shaped position exceeds finite range")
				}
				if j == 0 {
					code.X, code.Y = ofdNumber(gx), ofdNumber(gy)
				} else {
					dx, dy = append(dx, ofdNumber(gx-lastX)), append(dy, ofdNumber(gy-lastY))
				}
				lastX, lastY = gx, gy
			}
			code.DeltaX, code.DeltaY = strings.Join(dx, " "), strings.Join(dy, " ")
			for j := 0; j < len(glyphs); {
				end := j + 1
				for end < len(glyphs) && glyphs[end].Cluster == glyphs[j].Cluster {
					end++
				}
				next := len(chars)
				if end < len(glyphs) {
					next = glyphs[end].Cluster
				}
				ids := make([]string, end-j)
				for k := j; k < end; k++ {
					ids[k-j] = strconv.Itoa(int(glyphs[k].Glyph))
				}
				transforms = append(transforms, CGTransform{CodePosition: offset + glyphs[j].Cluster, CodeCount: next - glyphs[j].Cluster, GlyphCount: end - j, Glyphs: strings.Join(ids, " ")})
				j = end
			}
			codes = append(codes, code)
			offset += len(chars)
		}
	}
	obj.TextCode, obj.CGTransform = codes, transforms
	obj.layout = &textLayout{value: value, options: options}
	return nil
}

// bidiClusterShifts 按视觉簇顺序累计字距和两端对齐产生的偏移
// 入参: glyphs 逻辑顺序字形, extra 逻辑字符累计偏移
// 返回: map[int]float64 各簇的视觉偏移
func bidiClusterShifts(glyphs []ShapedGlyph, extra []float64) map[int]float64 {
	type cluster struct {
		index, order int
		extra        float64
	}
	var clusters []cluster
	for i := 0; i < len(glyphs); {
		end := i + 1
		for end < len(glyphs) && glyphs[end].Cluster == glyphs[i].Cluster {
			end++
		}
		next := len(extra) - 1
		if end < len(glyphs) {
			next = glyphs[end].Cluster
		}
		clusters = append(clusters, cluster{glyphs[i].Cluster, glyphs[i].VisualOrder, extra[next] - extra[glyphs[i].Cluster]})
		i = end
	}
	slices.SortFunc(clusters, func(a, b cluster) int { return a.order - b.order })
	shifts := make(map[int]float64, len(clusters))
	var offset float64
	for _, cluster := range clusters {
		shifts[cluster.index] = offset
		offset += cluster.extra
	}
	return shifts
}

// shapeTextLine 检查可选后端的簇映射，按原文字符分配塑形步进
// 入参: shaper 字体塑形器, runes 单行字符, font 字体标识, size 字号, scale 横向比例, spacing 字距
// 返回: []ShapedGlyph 字形, []float64 含字距的字符步进, error 塑形错误
func shapeTextLine(shaper FontShaper, runes []rune, font string, size, scale, spacing float64) ([]ShapedGlyph, []float64, error) {
	glyphs, err := shaper.ShapeText(string(runes), size)
	if err != nil {
		return nil, nil, err
	}
	if len(runes) > 0 && (len(glyphs) == 0 || glyphs[0].Cluster != 0) {
		return nil, nil, fmt.Errorf("shaper omitted source text")
	}
	advances := make([]float64, len(runes))
	var missing []rune
	for i, glyph := range glyphs {
		if glyph.Cluster < 0 || glyph.Cluster >= len(runes) || i > 0 && glyph.Cluster < glyphs[i-1].Cluster || glyph.Glyph >= shaper.NumGlyphs() || !finite(glyph.X) || !finite(glyph.Y) || !finite(glyph.Advance) {
			return nil, nil, fmt.Errorf("invalid shaped glyph")
		}
		advances[glyph.Cluster] += glyph.Advance * scale
	}
	for i := 0; i < len(glyphs); {
		end := i + 1
		for end < len(glyphs) && glyphs[end].Cluster == glyphs[i].Cluster {
			end++
		}
		next := len(runes)
		if end < len(glyphs) {
			next = glyphs[end].Cluster
		}
		start := glyphs[i].Cluster
		if slices.ContainsFunc(glyphs[i:end], func(glyph ShapedGlyph) bool { return glyph.Glyph == 0 }) {
			before := len(missing)
			for _, char := range runes[start:next] {
				if shaper.GlyphIndex(char) == 0 {
					missing = append(missing, char)
				}
			}
			if len(missing) == before {
				missing = append(missing, runes[start:next]...)
			}
		}
		advance := advances[start] / float64(next-start)
		for j := start; j < next; j++ {
			advances[j] = advance
		}
		i = end
	}
	if err := missingGlyphError(font, missing); err != nil {
		return nil, nil, err
	}
	spaceTextAdvances(runes, advances, spacing)
	if slices.ContainsFunc(advances, func(value float64) bool { return !finite(value) }) {
		return nil, nil, fmt.Errorf("shaped advance exceeds finite range")
	}
	return glyphs, advances, nil
}

// shapedLineWidth 计算不含尾随空格和末尾字距的行宽
// 入参: runes 行内字符, advances 字符步进, spacing 字距
// 返回: float64 行宽
func shapedLineWidth(runes []rune, advances []float64, spacing float64) float64 {
	end := len(runes)
	for end > 0 && runes[end-1] == ' ' {
		end--
	}
	width := 0.0
	for _, advance := range advances[:end] {
		width += advance
	}
	if end > 0 {
		width -= spacing
	}
	return width
}

// validateTextGlyphs 校验显式字形编号及原文区间，不允许跨TextCode或重叠映射
// 入参: obj 文字对象, glyphCount 字体字形数量
// 返回: error 无效映射
func validateTextGlyphs(obj TextObject, glyphCount int) error {
	if len(obj.CGTransform) == 0 {
		return nil
	}
	var ends []int
	total := 0
	for _, code := range obj.TextCode {
		total += len(textCodeRunes(code.Value))
		ends = append(ends, total)
	}
	previous, boundary := 0, 0
	for _, transform := range obj.CGTransform {
		start, count := transform.CodePosition, transform.CodeCount
		if start < previous || count <= 0 || start >= total || count > total-start {
			return fmt.Errorf("invalid glyph character range")
		}
		end := start + count
		for ends[boundary] <= start {
			boundary++
		}
		if end > ends[boundary] {
			return fmt.Errorf("glyph mapping crosses text codes")
		}
		ids := strings.Fields(transform.Glyphs)
		if len(ids) == 0 || len(ids) != transform.GlyphCount {
			return fmt.Errorf("invalid glyph count")
		}
		for _, value := range ids {
			id, err := strconv.ParseUint(value, 10, 16)
			if err != nil || id >= uint64(glyphCount) {
				return fmt.Errorf("invalid glyph index %q", value)
			}
		}
		previous = end
	}
	return nil
}
