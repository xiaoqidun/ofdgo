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
	"bytes"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/go-text/typesetting/di"
	typefont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/shaping"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	"golang.org/x/text/unicode/bidi"
)

// richLayout 保存局部标准对象、含墨迹的边界及各片段的字体锚点序号
type richLayout struct {
	width   float64
	objects []GraphicObject
	bounds  Box
	anchors []int
}

// richMetrics 保存片段字体、共用基线度量和实际需要的合成样式
type richMetrics struct {
	font                    FontMetrics
	ascent, descent, height float64
	bold, italic            bool
	outlines                map[uint16]GeometryPath
}

// richSpan 保存方向和格式均一致的逻辑区间及行内视觉坐标
type richSpan struct {
	start, end, owner int
	rtl               bool
	glyphs            []ShapedGlyph
	x, width          float64
}

// richContextShaper 将带上下文的区间塑形接入已有簇校验流程
type richContextShaper struct {
	FontShaperRun
	text       []rune
	start, end int
	options    TextShapeOptions
}

// richParagraphOptions 以整段原文检测双向排版，与普通文字的自动模式一致
// 入参: chars 段落字符, options 排版选项
// 返回: TextLayout 生效的段落排版选项
func richParagraphOptions(chars []rune, options TextLayout) TextLayout {
	if options.AutoBidi && !options.Shaping.Bidi {
		for _, char := range chars {
			p, _ := bidi.LookupRune(char)
			switch p.Class() {
			case bidi.R, bidi.AL, bidi.RLE, bidi.RLO, bidi.RLI, bidi.FSI:
				options.Shaping.Bidi = true
			}
		}
	}
	return options
}

// richShapeLine 按方向、脚本和格式塑形整行，跨同格式片段合并，保留区间外的塑形上下文
// 入参: chars 行字符, owners 字符所属运行段索引, value 富文本, metrics 字体度量, options 排版选项, face 分段字体
// 返回: []richSpan 塑形片段, []float64 字符步进, error 塑形或缺字错误
func richShapeLine(chars []rune, owners []int, value RichText, metrics []richMetrics, options TextLayout, face *typefont.Face) ([]richSpan, []float64, error) {
	advances := make([]float64, len(chars))
	if len(chars) == 0 {
		return nil, advances, nil
	}
	direction := di.DirectionLTR
	if options.Shaping.Direction == "rtl" {
		direction = di.DirectionRTL
	}
	parts := []shaping.Input{{Text: chars, RunEnd: len(chars), Direction: direction}}
	if options.Shaping.Bidi {
		var segmenter shaping.Segmenter
		parts = segmenter.Split(parts[0], shapingFace{face})
	}
	var spans []richSpan
	for _, part := range parts {
		for start := part.RunStart; start < part.RunEnd; {
			owner := owners[start]
			format := value.Runs[owner].Format
			end := start + 1
			for end < part.RunEnd && value.Runs[owners[end]].Format == format {
				end++
			}
			font := metrics[owner].font
			span := richSpan{start: start, end: end, owner: owner, rtl: part.Direction.Progression() == di.TowardTopLeft}
			if options.Shape || options.Shaping != (TextShapeOptions{}) {
				settings := options.Shaping
				settings.Bidi = false
				if options.Shaping.Bidi {
					settings.Direction = "ltr"
					if span.rtl {
						settings.Direction = "rtl"
					}
					if settings.Script == "" {
						settings.Script = part.Script.String()
					}
				}
				shaper, ok := font.(FontShaper)
				if !ok {
					return nil, nil, fmt.Errorf("rich text shaping: %w", ErrBackendUnavailable)
				}
				if contextual, ok := font.(FontShaperRun); ok {
					shaper = richContextShaper{FontShaperRun: contextual, text: chars, start: start, end: end, options: settings}
				} else if settings != (TextShapeOptions{}) {
					configured, ok := font.(FontShaperOptions)
					if !ok {
						return nil, nil, fmt.Errorf("rich text shaping options: %w", ErrBackendUnavailable)
					}
					shaper = configuredTextShaper{FontShaperOptions: configured, options: settings}
				}
				glyphs, steps, err := shapeTextLine(shaper, chars[start:end], format.Font, format.Size, 1, 0)
				if err != nil {
					return nil, nil, err
				}
				span.glyphs = glyphs
				copy(advances[start:end], steps)
			} else {
				var missing []rune
				var x float64
				for i, char := range chars[start:end] {
					id := font.GlyphIndex(char)
					if id == 0 {
						missing = append(missing, char)
					}
					step := float64(font.GlyphAdvance(id)) * format.Size / float64(font.UnitsPerEm())
					span.glyphs = append(span.glyphs, ShapedGlyph{Glyph: id, Cluster: i, VisualOrder: i, X: x, Advance: step})
					advances[start+i] = step
					x += step
				}
				if err := missingGlyphError(format.Font, missing); err != nil {
					return nil, nil, err
				}
			}
			spans = append(spans, span)
			start = end
		}
	}
	spaceTextAdvances(chars, advances, options.LetterSpacing)
	var sum float64
	for _, advance := range advances {
		sum += advance
		if !finite(advance) || !finite(sum) {
			return nil, nil, fmt.Errorf("rich text advance overflow")
		}
	}
	return spans, advances, nil
}

// richPositionSpans 按核心排版相同的方向分组规则排列片段，按视觉簇分配字距及两端对齐
// 入参: spans 塑形片段, steps 字符步进, options 排版选项, x 行起始横坐标
// 返回: error 坐标溢出错误
func richPositionSpans(spans []richSpan, steps []float64, options TextLayout, x float64) error {
	order := make([]int, len(spans))
	for i := range order {
		order[i] = i
	}
	rtl := options.Shaping.Direction == "rtl"
	for start := 0; start < len(order); {
		end := start + 1
		for end < len(order) && spans[end].rtl == spans[start].rtl {
			end++
		}
		if spans[start].rtl != rtl {
			slices.Reverse(order[start:end])
		}
		start = end
	}
	if rtl {
		slices.Reverse(order)
	}
	for _, index := range order {
		span := &spans[index]
		extra := make([]float64, span.end-span.start+1)
		raw := make([]float64, span.end-span.start)
		for _, glyph := range span.glyphs {
			raw[glyph.Cluster] += glyph.Advance
		}
		for j, step := range steps[span.start:span.end] {
			extra[j+1] = extra[j] + step - raw[j]
			span.width += step
		}
		shifts := bidiClusterShifts(span.glyphs, extra)
		span.x = x
		for j := range span.glyphs {
			glyph := &span.glyphs[j]
			glyph.X += x + shifts[glyph.Cluster]
			if !finite(glyph.X) {
				return fmt.Errorf("rich text position overflow")
			}
		}
		x += span.width
		if !finite(x) {
			return fmt.Errorf("rich text position overflow")
		}
	}
	return nil
}

// richSpanText 将逻辑原文和绝对视觉位置写为标准字符字形映射
// 入参: chars 行字符, span 塑形片段, format 文字格式, baseline 基线纵坐标
// 返回: TextObject 标准文字对象
func richSpanText(chars []rune, span richSpan, format TextFormat, baseline float64) TextObject {
	text := richTextObject(format)
	code := TextCode{Value: escapeOFDText(string(chars[span.start:span.end]))}
	dx, dy := make([]string, 0, len(span.glyphs)), make([]string, 0, len(span.glyphs))
	for i, glyph := range span.glyphs {
		if i == 0 {
			code.X, code.Y = ofdNumber(glyph.X), ofdNumber(baseline+glyph.Y)
		} else {
			dx = append(dx, ofdNumber(glyph.X-span.glyphs[i-1].X))
			dy = append(dy, ofdNumber(glyph.Y-span.glyphs[i-1].Y))
		}
	}
	code.DeltaX, code.DeltaY = strings.Join(dx, " "), strings.Join(dy, " ")
	text.TextCode = []TextCode{code}
	for i := 0; i < len(span.glyphs); {
		end := i + 1
		for end < len(span.glyphs) && span.glyphs[end].Cluster == span.glyphs[i].Cluster {
			end++
		}
		next := span.end - span.start
		if end < len(span.glyphs) {
			next = span.glyphs[end].Cluster
		}
		ids := make([]string, end-i)
		for j := i; j < end; j++ {
			ids[j-i] = strconv.Itoa(int(span.glyphs[j].Glyph))
		}
		text.CGTransform = append(text.CGTransform, CGTransform{CodePosition: span.glyphs[i].Cluster, CodeCount: next - span.glyphs[i].Cluster, GlyphCount: end - i, Glyphs: strings.Join(ids, " ")})
		i = end
	}
	return text
}

// richSpanOutline 使用现有字体及几何后端生成与文字合成样式一致的墨迹
// 入参: span 塑形片段, format 文字格式, metrics 字体度量, geometry 几何后端, baseline 基线纵坐标
// 返回: GeometryPath 合成轮廓路径, error 轮廓计算错误
func richSpanOutline(span richSpan, format TextFormat, metrics richMetrics, geometry GeometryBackend, baseline float64) (GeometryPath, error) {
	font, ok := metrics.font.(FontOutlines)
	if !ok {
		return nil, fmt.Errorf("rich text outlines: %w", ErrBackendUnavailable)
	}
	var result GeometryPath
	for _, glyph := range span.glyphs {
		path, exists := metrics.outlines[glyph.Glyph]
		if !exists {
			var err error
			path, err = font.GlyphOutline(glyph.Glyph, format.Size)
			if err != nil {
				return nil, err
			}
			if metrics.bold && len(path) > 0 {
				stroke, err := geometry.Stroke(path, StrokeOptions{Width: format.Size * 0.04, Join: "Round", Cap: "Round"})
				if err != nil {
					return nil, err
				}
				path, err = geometry.Combine(path, stroke, GeometryUnion)
				if err != nil {
					return nil, err
				}
			}
			metrics.outlines[glyph.Glyph] = path
		}
		if len(path) == 0 {
			continue
		}
		matrix := TranslationMatrix(glyph.X, baseline+glyph.Y)
		if metrics.italic {
			matrix = matrix.Multiply(Matrix{a: 1, c: -0.3, d: 1})
		}
		placed, err := geometry.Transform(path, matrix)
		if err != nil {
			return nil, err
		}
		result = append(result, placed...)
	}
	return result, nil
}

// richTextObject 构造具有显式填充和标准字重的文字对象
// 入参: format 文字格式
// 返回: TextObject 文字对象
func richTextObject(format TextFormat) TextObject {
	fill, stroke := true, false
	weight := 400
	if format.Bold {
		weight = 700
	}
	return TextObject{Font: format.Font, Size: format.Size, Weight: weight, Italic: format.Italic, Fill: &fill, Stroke: &stroke, FillColor: &FillColor{Value: richColor(format.Color)}}
}

// richColor 将空前景色转换为标准黑色
// 入参: value 颜色文本
// 返回: string 标准RGB颜色值
func richColor(value string) string {
	if value == "" {
		return "0 0 0"
	}
	return value
}

// richUnion 合并排版范围和墨迹范围，保留负坐标及零宽空行
// 入参: a 矩形范围, b 矩形范围
// 返回: Box 合并后的外接矩形
func richUnion(a, b Box) Box {
	x, y := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	return Box{X: x, Y: y, W: math.Max(a.X+a.W, b.X+b.W) - x, H: math.Max(a.Y+a.H, b.Y+b.H) - y}
}

// richRectangle 使用标准填充路径绘制背景或文字装饰
// 入参: box 局部矩形范围, color 填充色值
// 返回: GraphicObject 路径图元
func richRectangle(box Box, color string) GraphicObject {
	fill, stroke := true, false
	return GraphicObject{Type: "PathObject", PathObject: PathObject{Boundary: editorBoxString(box), Fill: &fill, Stroke: &stroke, FillColor: &FillColor{Value: color}, AbbreviatedData: fmt.Sprintf("M 0 0 L %s 0 L %s %s L 0 %s C", ofdNumber(box.W), ofdNumber(box.W), ofdNumber(box.H), ofdNumber(box.H))}}
}

// LayoutRichText 生成局部坐标的标准文字和路径，不修改页面或编辑历史
// 边界包含内容的排版高度和溢出墨迹，不包含空余段落宽度，可能有负的X或Y
// 非标准RGB文档按需复用或注册RGB颜色空间，返回对象可直接用于当前文档
// 注册不产生修订或撤销记录；失败时不保留新增资源和资源编号
// 提交对象后应创建新的Reader；已有Reader是独立快照，不包含新注册的资源
// 返回值的资源编号属于当前资源视图，切换文档、版本或替换资源快照后须重新排版
// 入参: value 富文本内容, width 段落排版宽度
// 返回: []GraphicObject 生成的标准图元, Box 局部边界, error 排版或资源错误
func (e *Editor) LayoutRichText(value RichText, width float64) ([]GraphicObject, Box, error) {
	result, err := e.layoutRichText(value, width)
	if err != nil {
		return nil, Box{}, err
	}
	err = e.Transaction(func(editor *Editor) error {
		for i := range result.objects {
			if err := editor.richTextColors(&result.objects[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, Box{}, err
	}
	return result.objects, result.bounds, nil
}

// ShapeText 返回区间相对簇索引，同时保留完整行提供的上下文
// 入参: _ 忽略的局部字符串, size 字号
// 返回: []ShapedGlyph 塑形字形列表, error 塑形错误
func (s richContextShaper) ShapeText(_ string, size float64) ([]ShapedGlyph, error) {
	glyphs, err := s.ShapeTextRun(s.text, s.start, s.end, size, s.options)
	if err != nil {
		return nil, err
	}
	for i := range glyphs {
		glyphs[i].Cluster -= s.start
	}
	return glyphs, nil
}

// layoutRichText 统一计算段落换行、视觉顺序和基线，颜色资源由调用方绑定
// 入参: value 富文本内容, width 段落排版宽度
// 返回: richLayout 排版结果, error 校验或塑形错误
func (e *Editor) layoutRichText(value RichText, width float64) (richLayout, error) {
	if err := value.Validate(); err != nil {
		return richLayout{}, err
	}
	options := value.Layout
	if !finite(width) || width <= 0 {
		return richLayout{}, fmt.Errorf("invalid rich text width or indents")
	}
	geometry, err := e.Geometry()
	if err != nil {
		return richLayout{}, err
	}
	metrics := make([]richMetrics, len(value.Runs))
	result := richLayout{anchors: make([]int, len(value.Runs))}
	owners := make([]int, 0, value.Len())
	for i, run := range value.Runs {
		font, err := e.editorFont(run.Format.Font)
		if err != nil {
			return richLayout{}, err
		}
		if font.UnitsPerEm() == 0 {
			return richLayout{}, fmt.Errorf("invalid font units")
		}
		a, d, g := font.VerticalMetrics()
		unit := run.Format.Size / float64(font.UnitsPerEm())
		bold, italic := e.richSynthetic(run.Format)
		metrics[i] = richMetrics{font: font, ascent: float64(a) * unit, descent: float64(d) * unit, height: max(run.Format.Size, float64(int(a)+int(d)+int(g))*unit), bold: bold, italic: italic, outlines: make(map[uint16]GeometryPath)}
		if !finite(metrics[i].height) {
			return richLayout{}, fmt.Errorf("rich text metrics overflow")
		}
		result.anchors[i] = -1
		for range []rune(run.Text) {
			owners = append(owners, i)
		}
	}
	chars := []rune(value.Text())
	var face *typefont.Face
	if value.AutoWidth && !options.Wrap {
		width = 1
		for start, paragraphIndex := 0, 0; start <= len(chars); paragraphIndex++ {
			end := start
			for end < len(chars) && chars[end] != '\n' {
				end++
			}
			paragraph := chars[start:end]
			local := richParagraphOptions(paragraph, value.ParagraphLayout(paragraphIndex))
			if local.Shaping.Bidi && face == nil && len(paragraph) > 0 {
				face, err = typefont.ParseTTF(bytes.NewReader(metrics[owners[start]].font.Write()))
				if err != nil {
					return richLayout{}, err
				}
			}
			_, advances, err := richShapeLine(paragraph, owners[start:end], value, metrics, local, face)
			if err != nil {
				return richLayout{}, err
			}
			length := 0.0
			for _, advance := range advances {
				length += advance
			}
			width = max(width, local.LeftIndent+local.RightIndent+max(0, local.FirstLineIndent)+1, local.LeftIndent+local.RightIndent+local.FirstLineIndent+length)
			if end == len(chars) {
				break
			}
			start = end + 1
		}
	}
	result.width = width
	var underlays []GraphicObject
	var shadows []GraphicObject
	var y float64
	for start, paragraphIndex := 0, 0; start <= len(chars); paragraphIndex++ {
		end := start
		for end < len(chars) && chars[end] != '\n' {
			end++
		}
		paragraph := chars[start:end]
		local := value.ParagraphLayout(paragraphIndex)
		available := width - local.LeftIndent - local.RightIndent
		if !finite(available) || available <= 0 || !finite(available-local.FirstLineIndent) || available-local.FirstLineIndent <= 0 {
			return richLayout{}, fmt.Errorf("invalid rich text width or indents")
		}
		local = richParagraphOptions(paragraph, local)
		if local.Shaping.Bidi && face == nil && len(paragraph) > 0 {
			face, err = typefont.ParseTTF(bytes.NewReader(metrics[owners[start]].font.Write()))
			if err != nil {
				return richLayout{}, fmt.Errorf("rich text segmentation font: %w", err)
			}
		}
		_, advances, err := richShapeLine(paragraph, owners[start:end], value, metrics, local, face)
		if err != nil {
			return richLayout{}, err
		}
		lines := breakTextLines(paragraph, advances, available, local.Wrap, local.LetterSpacing, local.FirstLineIndent)
		for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
			line := lines[lineIndex]
			indent := 0.0
			if lineIndex == 0 {
				indent = local.FirstLineIndent
			}
			var spans []richSpan
			var steps []float64
			for {
				spans, steps, err = richShapeLine(paragraph[line[0]:line[1]], owners[start+line[0]:start+line[1]], value, metrics, local, face)
				if err != nil {
					return richLayout{}, err
				}
				if !local.Wrap || shapedLineWidth(paragraph[line[0]:line[1]], steps, local.LetterSpacing) <= available-indent {
					break
				}
				previous := 0
				for at := range richTextBoundaries(string(paragraph[line[0]:line[1]])) {
					if at < line[1]-line[0] {
						previous = max(previous, at)
					}
				}
				if previous == 0 {
					break
				}
				oldEnd := line[1]
				line[1] = line[0] + previous
				if lineIndex+1 < len(lines) {
					lines[lineIndex+1][0] = line[1]
				} else {
					lines = append(lines, [2]int{line[1], oldEnd})
				}
				lines[lineIndex] = line
			}
			from, to := start+line[0], start+line[1]
			fallback := len(value.Runs) - 1
			if from < len(owners) {
				fallback = owners[from]
			}
			ascent, descent, height := 0.0, 0.0, 0.0
			for pos := from; pos < max(from+1, to); pos++ {
				index := fallback
				if pos < to {
					index = owners[pos]
				}
				m := metrics[index]
				ascent, descent, height = max(ascent, m.ascent), max(descent, m.descent), max(height, m.height)
			}
			height = max(height, ascent+descent)
			var clusters map[int]bool
			if local.Align == "distributed" {
				clusters = make(map[int]bool)
				for _, span := range spans {
					for _, glyph := range span.glyphs {
						clusters[span.start+glyph.Cluster] = true
					}
				}
			}
			x, delta := alignTextLine(chars[from:to], steps, available-indent, local.Align, lineIndex+1 < len(lines), local.LetterSpacing, clusters)
			copy(steps, parseFloats(delta))
			if local.Shaping.Direction == "rtl" {
				for j := to - from - 1; j >= 0 && chars[from+j] == ' '; j-- {
					x -= steps[j]
				}
			}
			x += local.LeftIndent + indent
			if err := richPositionSpans(spans, steps, local, x); err != nil {
				return richLayout{}, err
			}
			lineBounds := Box{X: x, Y: y, H: height}
			if result.bounds.H == 0 {
				result.bounds = lineBounds
			} else {
				result.bounds = richUnion(result.bounds, lineBounds)
			}
			for _, span := range spans {
				format := value.Runs[span.owner].Format
				for _, glyph := range span.glyphs {
					if !finite(y + ascent + glyph.Y) {
						return richLayout{}, fmt.Errorf("rich text position overflow")
					}
				}
				text := richSpanText(chars[from:to], span, format, y+ascent)
				ink, err := richSpanOutline(span, format, metrics[span.owner], geometry, y+ascent)
				if err != nil {
					return richLayout{}, err
				}
				if len(ink) > 0 {
					box, err := geometry.Bounds(ink)
					if err != nil {
						return richLayout{}, err
					}
					result.bounds = richUnion(result.bounds, box)
				}
				decorationWidth := max(0, span.width-local.LetterSpacing)
				if format.Background != "" && decorationWidth > 0 {
					underlays = append(underlays, richRectangle(Box{X: span.x, Y: y, W: decorationWidth, H: height}, format.Background))
				}
				if format.Shadow && len(ink) > 0 {
					shift := format.Size * 0.06
					shadow, err := geometry.Transform(ink, TranslationMatrix(shift, shift))
					if err != nil {
						return richLayout{}, err
					}
					path, err := geometryClipPath(shadow)
					if err != nil {
						return richLayout{}, err
					}
					path.FillColor = &FillColor{Value: "128 128 128"}
					shadows = append(shadows, GraphicObject{Type: "PathObject", PathObject: path})
				}
				anchor := len(result.objects)
				for _, owner := range owners[from+span.start : from+span.end] {
					if result.anchors[owner] < 0 {
						result.anchors[owner] = anchor
					}
				}
				result.objects = append(result.objects, GraphicObject{Type: "TextObject", TextObject: text})
				thickness := max(0.05, format.Size*0.05)
				if format.Underline && decorationWidth > 0 {
					result.objects = append(result.objects, richRectangle(Box{X: span.x, Y: y + ascent + format.Size*0.1, W: decorationWidth, H: thickness}, richColor(format.Color)))
				}
				if format.Strikeout && decorationWidth > 0 {
					result.objects = append(result.objects, richRectangle(Box{X: span.x, Y: y + ascent - format.Size*0.3, W: decorationWidth, H: thickness}, richColor(format.Color)))
				}
				result.bounds = richUnion(result.bounds, Box{X: span.x, Y: y, W: max(0, span.width), H: height})
			}
			advance := height
			if local.LineHeight > 0 {
				advance = local.LineHeight
			}
			y += advance
			if !finite(y) {
				return richLayout{}, fmt.Errorf("rich text layout overflow")
			}
		}
		if end == len(chars) {
			break
		}
		start = end + 1
	}
	underlays = append(underlays, shadows...)
	for i, anchor := range result.anchors {
		if anchor >= 0 {
			result.anchors[i] += len(underlays)
		}
	}
	result.objects = append(underlays, result.objects...)
	for _, object := range result.objects {
		if object.Type == "PathObject" {
			box, err := ParseBox(object.PathObject.Boundary)
			if err != nil {
				return richLayout{}, err
			}
			result.bounds = richUnion(result.bounds, box)
		}
	}
	if !finite(result.bounds.X) || !finite(result.bounds.Y) || !finite(result.bounds.W) || !finite(result.bounds.H) || result.bounds.H <= 0 {
		return richLayout{}, fmt.Errorf("rich text layout overflow")
	}
	if result.bounds.W == 0 {
		result.bounds.W = min(width, value.Runs[0].Format.Size)
	}
	for i, anchor := range result.anchors {
		if anchor >= 0 {
			continue
		}
		text := richTextObject(value.Runs[i].Format)
		visible := false
		text.Visible = &visible
		text.TextCode = []TextCode{{X: "0", Y: ofdNumber(metrics[i].ascent), Value: " "}}
		result.anchors[i] = len(result.objects)
		result.objects = append(result.objects, GraphicObject{Type: "TextObject", TextObject: text})
	}
	for i := range result.objects {
		if result.objects[i].Type != "TextObject" {
			continue
		}
		text := &result.objects[i].TextObject
		text.Boundary = editorBoxString(result.bounds)
		for j := range text.TextCode {
			code := &text.TextCode[j]
			x, _ := strconv.ParseFloat(code.X, 64)
			y, _ := strconv.ParseFloat(code.Y, 64)
			code.X = ofdNumber(x - result.bounds.X)
			code.Y = ofdNumber(y - result.bounds.Y)
		}
	}
	return result, nil
}

// richSynthetic 与标准绘制一样避免对已加粗或倾斜的字体重复合成
// 入参: format 文字格式
// 返回: bool 是否合成加粗, bool 是否合成斜体
func (e *Editor) richSynthetic(format TextFormat) (bool, bool) {
	bold, italic := format.Bold, format.Italic
	var definition *Font
	for _, resource := range e.resources {
		if resource.font != nil && resource.font.ID == format.Font {
			definition = resource.font
			break
		}
	}
	if definition == nil && e.source != nil {
		definition = e.source.reader.fontDefinition(format.Font)
	}
	if definition != nil {
		bold = bold && !definition.Bold
		italic = italic && !definition.Italic
		if definition.FontFile == "" && basefont.NoSyntheticBold(definition.FontName, definition.FamilyName) {
			bold = false
		}
	}
	return bold, italic
}
