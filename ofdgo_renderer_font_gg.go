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

	typefont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/harfbuzz"
	"github.com/go-text/typesetting/language"
	ggtext "github.com/gogpu/gg/text"
	xlanguage "golang.org/x/text/language"
)

// ggFontMetrics 适配GG设计单位度量与字形轮廓，按需缓存可配置塑形器
type ggFontMetrics struct {
	font        ggtext.ParsedFont
	data        []byte
	source      *ggtext.FontSource
	shaper      *ggtext.OwnShaper
	shapeMu     sync.Mutex
	shapeFont   *harfbuzz.Font
	shapeBuffer *harfbuzz.Buffer
}

// ResolveFont 使用公共字体来源匹配，不创建Canvas字体
// 入参: r 渲染器, id 字体ID, exact 是否禁止无关回退
// 返回: ResolvedFont 字体及来源, error 解析错误
func (b GGBackend) ResolveFont(r *Renderer, id string, exact bool) (ResolvedFont, error) {
	return r.resolveFontSource(b, id, exact)
}

// OpenFont 通过GG解析独立字体，保留原始SFNT编码
// 入参: data 字体数据
// 返回: FontMetrics 字体度量, error 解析错误
func (GGBackend) OpenFont(data []byte) (FontMetrics, error) {
	data = bytes.Clone(data)
	source, err := ggtext.NewFontSource(data)
	if err != nil {
		return nil, err
	}
	return &ggFontMetrics{font: source.Parsed(), data: data, source: source, shaper: ggtext.NewOwnShaper()}, nil
}

// ShapeText 使用GG执行横向字偶距与连字塑形，不改动原文定位
// 当前支持拉丁、希腊、西里尔和东亚文字，复杂文字可通过ShapeTextWithOptions指定脚本与方向
// 入参: value 单行原文, size 毫米字号
// 返回: []ShapedGlyph 定位字形, error 字符或能力错误
func (f *ggFontMetrics) ShapeText(value string, size float64) ([]ShapedGlyph, error) {
	if !rasterPositive(size) || !utf8.ValidString(value) {
		return nil, fmt.Errorf("invalid shaping text or size")
	}
	for _, char := range value {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return nil, fmt.Errorf("shaping requires a single text line")
		}
		if !unicode.In(char, unicode.Latin, unicode.Greek, unicode.Cyrillic, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Common, unicode.Inherited) {
			return nil, fmt.Errorf("GG shaping for U+%04X: %w", char, ErrBackendUnavailable)
		}
	}
	glyphs := f.shaper.Shape(value, f.source.Face(size))
	result := make([]ShapedGlyph, len(glyphs))
	for i, glyph := range glyphs {
		result[i] = ShapedGlyph{Glyph: uint16(glyph.GID), Cluster: glyph.Cluster, X: glyph.X, Y: -glyph.Y, Advance: glyph.XAdvance}
	}
	return result, nil
}

// ShapeTextWithOptions 使用现有纯Go塑形引擎处理单一方向原文，保留逻辑簇和视觉位置
// 不执行混合方向分段，调用方应将双向段落拆为单向文字对象
// 入参: value 单行原文, size 毫米字号, options 塑形选项
// 返回: []ShapedGlyph 定位字形, error 字符或选项错误
func (f *ggFontMetrics) ShapeTextWithOptions(value string, size float64, options TextShapeOptions) ([]ShapedGlyph, error) {
	if options == (TextShapeOptions{}) {
		return f.ShapeText(value, size)
	}
	if !rasterPositive(size) || !utf8.ValidString(value) {
		return nil, fmt.Errorf("invalid shaping text or size")
	}
	for _, char := range value {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
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
		tag, err := xlanguage.Parse(options.Language)
		if err != nil {
			return nil, fmt.Errorf("shaping language: %w", err)
		}
		props.Language = language.NewLanguage(tag.String())
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
	f.shapeMu.Lock()
	defer f.shapeMu.Unlock()
	if f.shapeFont == nil {
		face, err := typefont.ParseTTF(bytes.NewReader(f.data))
		if err != nil {
			return nil, fmt.Errorf("shaping font: %w", err)
		}
		f.shapeFont = harfbuzz.NewFont(face)
		f.shapeBuffer = harfbuzz.NewBuffer()
	}
	buffer := f.shapeBuffer
	buffer.Clear()
	buffer.Props = props
	runes := []rune(value)
	buffer.AddRunes(runes, 0, len(runes))
	buffer.GuessSegmentProperties()
	buffer.Shape(f.shapeFont, features)
	unit := size / float64(f.UnitsPerEm())
	result := make([]ShapedGlyph, len(buffer.Info))
	var x, y float64
	for i, info := range buffer.Info {
		if info.Glyph >= typefont.GID(f.NumGlyphs()) {
			return nil, fmt.Errorf("invalid shaped glyph index %d", info.Glyph)
		}
		position := buffer.Pos[i]
		result[i] = ShapedGlyph{Glyph: uint16(info.Glyph), Cluster: info.Cluster,
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

// UnitsPerEm 返回字体设计单位
// 返回: uint16 每字单位数
func (f *ggFontMetrics) UnitsPerEm() uint16 { return uint16(f.font.UnitsPerEm()) }

// NumGlyphs 返回字形数量
// 返回: uint16 字形数量
func (f *ggFontMetrics) NumGlyphs() uint16 { return uint16(f.font.NumGlyphs()) }

// GlyphIndex 返回字符对应的字形编号
// 入参: character 字符
// 返回: uint16 字形编号
func (f *ggFontMetrics) GlyphIndex(character rune) uint16 { return f.font.GlyphIndex(character) }

// GlyphAdvance 返回字形设计单位推进宽度
// 入参: glyph 字形编号
// 返回: uint16 推进宽度
func (f *ggFontMetrics) GlyphAdvance(glyph uint16) uint16 {
	return uint16(math.Round(f.font.GlyphAdvance(glyph, float64(f.font.UnitsPerEm()))))
}

// VerticalMetrics 返回非负的上升、下降与行间隙
// 返回: uint16 上升高度, uint16 下降高度, uint16 行间隙
func (f *ggFontMetrics) VerticalMetrics() (uint16, uint16, uint16) {
	m := f.font.Metrics(float64(f.font.UnitsPerEm()))
	return uint16(math.Round(math.Max(0, m.Ascent))), uint16(math.Round(math.Max(0, -m.Descent))), uint16(math.Round(math.Max(0, m.LineGap)))
}

// Write 返回独立字体数据，不更新字体元信息
// 返回: []byte SFNT数据
func (f *ggFontMetrics) Write() []byte { return bytes.Clone(f.data) }

// GlyphOutline 返回基线原点、纵轴向下的字形路径
// 入参: glyph 字形编号, size 字号，单位为毫米
// 返回: GeometryPath 字形路径, error 轮廓解析错误
func (f *ggFontMetrics) GlyphOutline(glyph uint16, size float64) (GeometryPath, error) {
	if !rasterPositive(size) || int(glyph) >= f.font.NumGlyphs() {
		return nil, fmt.Errorf("invalid glyph or size")
	}
	outline, err := ggtext.NewOutlineExtractor().ExtractOutline(f.font, ggtext.GlyphID(glyph), size)
	if err != nil || outline == nil {
		return nil, err
	}
	return ggOutlinePath(outline)
}

// GlyphOutlines 按指定编号批量提取轮廓，重复字形只解析一次
// 入参: glyphs 字形编号, size 毫米字号
// 返回: []GeometryPath 同序只读轮廓, error 轮廓解析错误
func (f *ggFontMetrics) GlyphOutlines(glyphs []uint16, size float64) ([]GeometryPath, error) {
	if !rasterPositive(size) {
		return nil, fmt.Errorf("invalid glyph size")
	}
	for _, glyph := range glyphs {
		if int(glyph) >= f.font.NumGlyphs() {
			return nil, fmt.Errorf("invalid glyph index %d", glyph)
		}
	}
	result := make([]GeometryPath, len(glyphs))
	paths := make(map[uint16]GeometryPath, len(glyphs))
	for i, glyph := range glyphs {
		path, ok := paths[glyph]
		if !ok {
			var err error
			path, err = f.GlyphOutline(glyph, size)
			if err != nil {
				return nil, err
			}
			paths[glyph] = path
		}
		result[i] = path
	}
	return result, nil
}

// ggOutlinePath 将GG轮廓转换为闭合的公共字形路径
// 入参: outline GG字形轮廓
// 返回: GeometryPath 公共路径, error 未知轮廓指令
func ggOutlinePath(outline *ggtext.GlyphOutline) (GeometryPath, error) {
	capacity := len(outline.Segments)
	for _, segment := range outline.Segments {
		if segment.Op == ggtext.OutlineOpMoveTo {
			capacity++
		}
	}
	result := make(GeometryPath, 0, capacity)
	point := func(p ggtext.OutlinePoint) Point { return Point{X: float64(p.X), Y: float64(p.Y)} }
	for _, s := range outline.Segments {
		segment := GeometrySegment{End: point(s.Points[0])}
		switch s.Op {
		case ggtext.OutlineOpMoveTo:
			if len(result) > 0 {
				result = append(result, GeometrySegment{Verb: GeometryClose})
			}
			segment.Verb = GeometryMove
		case ggtext.OutlineOpLineTo:
			segment.Verb = GeometryLine
		case ggtext.OutlineOpQuadTo:
			segment.Verb, segment.Control1, segment.End = GeometryQuad, point(s.Points[0]), point(s.Points[1])
		case ggtext.OutlineOpCubicTo:
			segment.Verb, segment.Control1, segment.Control2, segment.End = GeometryCubic, point(s.Points[0]), point(s.Points[1]), point(s.Points[2])
		default:
			return nil, fmt.Errorf("unsupported GG outline command %d", s.Op)
		}
		result = append(result, segment)
	}
	if len(result) > 0 {
		result = append(result, GeometrySegment{Verb: GeometryClose})
	}
	return result, nil
}
