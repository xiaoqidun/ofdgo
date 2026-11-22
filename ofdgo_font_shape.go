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
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/go-text/typesetting/di"
	typefont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/harfbuzz"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
)

// fontShapingBufferLimit 限制字体实例长期保留的字形缓冲容量
const fontShapingBufferLimit = 4096

// fontShaper 使用字体度量和原始数据提供可配置塑形，不依赖绘图后端
type fontShaper struct {
	metrics     FontMetrics
	shapeMu     sync.Mutex
	shapeFont   *harfbuzz.Font
	shapeFace   *typefont.Face
	shapeBuffer *harfbuzz.Buffer
}

// shapingFace 固定双向排版的字体来源，不隐式替换字体
type shapingFace struct{ face *typefont.Face }

// canonicalLanguageTag 按BCP47语法校验并规范化语言标签，不引入外部语言包
// 入参: raw 原始语言标签
// 返回: string 规范化语言标签, error 语法错误
func canonicalLanguageTag(raw string) (string, error) {
	normalized := strings.ReplaceAll(raw, "_", "-")
	if normalized == "" || strings.HasPrefix(normalized, "-") || strings.HasSuffix(normalized, "-") {
		return "", fmt.Errorf("invalid language tag %q", raw)
	}
	parts := strings.Split(normalized, "-")
	for i, part := range parts {
		if len(part) == 0 || len(part) > 8 {
			return "", fmt.Errorf("invalid language tag %q", raw)
		}
		alphaOnly := true
		for j := 0; j < len(part); j++ {
			c := part[j]
			isAlpha := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
			isDigit := c >= '0' && c <= '9'
			if !isAlpha && !isDigit {
				return "", fmt.Errorf("invalid language tag %q", raw)
			}
			if !isAlpha {
				alphaOnly = false
			}
		}
		if i == 0 {
			if part == "x" || part == "X" {
				if len(parts) < 2 {
					return "", fmt.Errorf("invalid language tag %q", raw)
				}
				parts[i] = "x"
				continue
			}
			if !alphaOnly || len(part) == 1 || len(part) == 4 {
				return "", fmt.Errorf("invalid language tag %q", raw)
			}
			parts[i] = strings.ToLower(part)
			continue
		}
		if len(part) == 1 {
			if i == len(parts)-1 {
				return "", fmt.Errorf("invalid language tag %q", raw)
			}
			parts[i] = strings.ToLower(part)
		} else if len(part) == 4 && alphaOnly && len(parts[i-1]) > 1 {
			parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		} else if (len(part) == 2 && alphaOnly || len(part) == 3 && !alphaOnly) && len(parts[i-1]) > 1 {
			parts[i] = strings.ToUpper(part)
		} else {
			parts[i] = strings.ToLower(part)
		}
	}
	return strings.Join(parts, "-"), nil
}

// ShapeText 使用默认选项塑形单行文字，不改动原文定位
// 入参: value 单行原文, size 毫米字号
// 返回: []ShapedGlyph 定位字形, error 塑形错误
func (f *fontShaper) ShapeText(value string, size float64) ([]ShapedGlyph, error) {
	return f.ShapeTextWithOptions(value, size, TextShapeOptions{})
}

// ShapeTextWithOptions 使用纯Go塑形引擎保留逻辑簇和视觉位置，可显式启用双向分段
// 入参: value 单行原文, size 毫米字号, options 塑形选项
// 返回: []ShapedGlyph 定位字形, error 字符或选项错误
func (f *fontShaper) ShapeTextWithOptions(value string, size float64, options TextShapeOptions) ([]ShapedGlyph, error) {
	if !rasterPositive(size) || !utf8.ValidString(value) {
		return nil, fmt.Errorf("invalid shaping text or size")
	}
	runes := []rune(value)
	return f.shapeTextRun(runes, 0, len(runes), size, options)
}

// ShapeTextRun 利用原文上下文塑形指定区间，保留原文簇索引
// 入参: value 已校验的原文符文, start 起始符文索引, end 结束符文索引, size 毫米字号, options 已分段的塑形选项
// 返回: []ShapedGlyph 原文簇索引及区间内定位字形, error 区间、字符或选项错误
func (f *fontShaper) ShapeTextRun(value []rune, start, end int, size float64, options TextShapeOptions) ([]ShapedGlyph, error) {
	if options.Bidi {
		return nil, fmt.Errorf("bidirectional segmentation is not valid for an individual shaping run")
	}
	return f.shapeTextRun(value, start, end, size, options)
}

// shapeTextRun 共用完整行与局部区间的选项校验、字体初始化及字形塑形
// 入参: runes 原文符文, start 起始符文索引, end 结束符文索引, size 毫米字号, options 塑形选项
// 返回: []ShapedGlyph 定位字形, error 字体、区间或选项错误
func (f *fontShaper) shapeTextRun(runes []rune, start, end int, size float64, options TextShapeOptions) ([]ShapedGlyph, error) {
	if !rasterPositive(size) {
		return nil, fmt.Errorf("invalid shaping size")
	}
	if start < 0 || end < start || end > len(runes) {
		return nil, fmt.Errorf("invalid shaping text range")
	}
	for _, char := range runes[start:end] {
		if !utf8.ValidRune(char) || unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return nil, fmt.Errorf("shaping requires a single text line")
		}
	}
	props := harfbuzz.SegmentProperties{Direction: harfbuzz.LeftToRight}
	switch options.Direction {
	case "", "ltr":
	case "rtl":
		props.Direction = harfbuzz.RightToLeft
	default:
		return nil, fmt.Errorf("shaping direction %q: %w", options.Direction, ErrBackendUnavailable)
	}
	if options.Script != "" {
		if len(options.Script) != 4 || strings.ContainsFunc(options.Script, func(c rune) bool { return c < 'A' || c > 'Z' && c < 'a' || c > 'z' }) {
			return nil, fmt.Errorf("invalid shaping script %q", options.Script)
		}
		props.Script, _ = language.ParseScript(options.Script)
	}
	if options.Language != "" {
		tag, err := canonicalLanguageTag(options.Language)
		if err != nil {
			return nil, fmt.Errorf("shaping language: %w", err)
		}
		props.Language = language.NewLanguage(tag)
	}
	var features []harfbuzz.Feature
	if options.Features != "" {
		for _, setting := range strings.Split(options.Features, ",") {
			setting = strings.TrimSpace(setting)
			if strings.ContainsAny(setting, "[]") {
				return nil, fmt.Errorf("shaping feature ranges: %w", ErrBackendUnavailable)
			}
			tag, value, hasValue := strings.Cut(setting, "=")
			if len(tag) != 4 || strings.ContainsFunc(tag, func(c rune) bool { return c < '0' || c > '9' && c < 'A' || c > 'Z' && c < 'a' || c > 'z' }) {
				return nil, fmt.Errorf("invalid shaping feature %q", setting)
			}
			if hasValue {
				if _, err := strconv.ParseUint(value, 10, 32); err != nil {
					return nil, fmt.Errorf("shaping feature %q: %w", setting, err)
				}
			}
			feature, err := harfbuzz.ParseFeature(setting)
			if err != nil {
				return nil, fmt.Errorf("shaping feature %q: %w", setting, err)
			}
			if feature.Start != harfbuzz.FeatureGlobalStart || feature.End != harfbuzz.FeatureGlobalEnd {
				return nil, fmt.Errorf("shaping feature ranges: %w", ErrBackendUnavailable)
			}
			features = append(features, feature)
		}
	}
	if start == end {
		return nil, nil
	}
	f.shapeMu.Lock()
	defer f.shapeMu.Unlock()
	if f.shapeFont == nil {
		face, err := typefont.ParseTTF(bytes.NewReader(f.metrics.Write()))
		if err != nil {
			return nil, fmt.Errorf("shaping font: %w", err)
		}
		f.shapeFont = harfbuzz.NewFont(face)
		f.shapeFace = face
		f.shapeBuffer = harfbuzz.NewBuffer()
	}
	if options.Bidi {
		return f.shapeBidiText(runes, size, props, features)
	}
	buffer := f.shapeBuffer
	buffer.Clear()
	defer f.releaseShapeBuffer()
	buffer.Props = props
	buffer.AddRunes(runes, start, end-start)
	buffer.GuessSegmentProperties()
	buffer.Shape(f.shapeFont, features)
	unit := size / float64(f.metrics.UnitsPerEm())
	result := make([]ShapedGlyph, len(buffer.Info))
	var x, y float64
	previous, order := -1, -1
	for i, info := range buffer.Info {
		if info.Glyph >= typefont.GID(f.metrics.NumGlyphs()) {
			return nil, fmt.Errorf("invalid shaped glyph index %d", info.Glyph)
		}
		if info.Cluster < start || info.Cluster >= end {
			return nil, fmt.Errorf("invalid shaped cluster index %d", info.Cluster)
		}
		if info.Cluster != previous {
			previous = info.Cluster
			order++
		}
		position := buffer.Pos[i]
		result[i] = ShapedGlyph{Glyph: uint16(info.Glyph), Cluster: info.Cluster, VisualOrder: order,
			X: (x + float64(position.XOffset)) * unit, Y: -(y + float64(position.YOffset)) * unit,
			Advance: float64(position.XAdvance) * unit}
		if !finite(result[i].X) || !finite(result[i].Y) || !finite(result[i].Advance) {
			return nil, fmt.Errorf("shaped position exceeds finite range")
		}
		x += float64(position.XAdvance)
		y += float64(position.YAdvance)
	}
	slices.SortStableFunc(result, func(a, b ShapedGlyph) int { return cmp.Compare(a.Cluster, b.Cluster) })
	return result, nil
}

// releaseShapeBuffer 释放原文上下文和超限缓冲，普通文本继续复用已有容量
func (f *fontShaper) releaseShapeBuffer() {
	buffer := f.shapeBuffer
	if cap(buffer.Info) > fontShapingBufferLimit || cap(buffer.Pos) > fontShapingBufferLimit {
		f.shapeBuffer = harfbuzz.NewBuffer()
		return
	}
	buffer.Clear()
	buffer.AddRunes(nil, 0, 0)
}

// shapeBidiText 使用现有分段和行排列引擎生成单行双向字形
// 入参: runes 原文, size 毫米字号, props 段落属性, features 字体特性
// 返回: []ShapedGlyph 字形, error 错误信息
func (f *fontShaper) shapeBidiText(runes []rune, size float64, props harfbuzz.SegmentProperties, features []harfbuzz.Feature) ([]ShapedGlyph, error) {
	if len(runes) == 0 {
		return nil, nil
	}
	direction := di.DirectionLTR
	if props.Direction == harfbuzz.RightToLeft {
		direction = di.DirectionRTL
	}
	input := shaping.Input{Text: runes, RunEnd: len(runes), Direction: direction, Face: f.shapeFace, Size: fixed.I(int(f.metrics.UnitsPerEm())), Script: props.Script, Language: props.Language}
	for _, feature := range features {
		input.FontFeatures = append(input.FontFeatures, shaping.FontFeature{Tag: feature.Tag, Value: feature.Value})
	}
	var segmenter shaping.Segmenter
	var shaper shaping.HarfbuzzShaper
	var wrapper shaping.LineWrapper
	var runs []shaping.Output
	var advance int64
	for _, part := range segmenter.Split(input, shapingFace{f.shapeFace}) {
		if props.Script != 0 {
			part.Script = props.Script
		}
		if props.Language != "" {
			part.Language = props.Language
		}
		run := shaper.Shape(part)
		for _, glyph := range run.Glyphs {
			advance += int64(glyph.Advance)
		}
		if advance < 0 || advance >= math.MaxInt32 {
			return nil, fmt.Errorf("shaped line exceeds coordinate range")
		}
		runs = append(runs, run)
	}
	lines, truncated := wrapper.WrapParagraphF(shaping.WrapConfig{Direction: direction, DisableTrailingWhitespaceTrim: true}, fixed.Int26_6(math.MaxInt32), runes, shaping.NewSliceIterator(runs))
	if truncated != 0 || len(lines) != 1 {
		return nil, fmt.Errorf("shaping requires a single text line")
	}
	slices.SortFunc(lines[0], func(a, b shaping.Output) int { return cmp.Compare(a.VisualIndex, b.VisualIndex) })
	unit := size / float64(f.metrics.UnitsPerEm()) / 64
	var result []ShapedGlyph
	var x float64
	visual := make(map[int]int)
	for _, run := range lines[0] {
		for _, glyph := range run.Glyphs {
			if glyph.GlyphID >= typefont.GID(f.metrics.NumGlyphs()) {
				return nil, fmt.Errorf("invalid shaped glyph index %d", glyph.GlyphID)
			}
			cluster := glyph.TextIndex()
			if cluster < 0 || cluster >= len(runes) {
				return nil, fmt.Errorf("invalid shaped cluster index %d", cluster)
			}
			order, exists := visual[cluster]
			if !exists {
				order = len(visual)
				visual[cluster] = order
			}
			shaped := ShapedGlyph{Glyph: uint16(glyph.GlyphID), Cluster: cluster, VisualOrder: order, X: (x + float64(glyph.XOffset)) * unit, Y: -float64(glyph.YOffset) * unit, Advance: float64(glyph.Advance) * unit}
			if !finite(shaped.X) || !finite(shaped.Y) || !finite(shaped.Advance) {
				return nil, fmt.Errorf("shaped position exceeds finite range")
			}
			result = append(result, shaped)
			x += float64(glyph.Advance)
		}
	}
	slices.SortStableFunc(result, func(a, b ShapedGlyph) int { return cmp.Compare(a.Cluster, b.Cluster) })
	return result, nil
}

// ResolveFace 返回指定字体
// 入参: char 字符
// 返回: *typefont.Face 字体
func (f shapingFace) ResolveFace(char rune) *typefont.Face { return f.face }
