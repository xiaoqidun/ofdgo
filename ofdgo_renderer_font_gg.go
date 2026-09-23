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

	ggtext "github.com/gogpu/gg/text"
)

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
	return &ggFontMetrics{font: source.Parsed(), data: data}, nil
}

// ggFontMetrics 适配GG设计单位度量与字形轮廓
type ggFontMetrics struct {
	font ggtext.ParsedFont
	data []byte
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
	result := make(GeometryPath, 0, len(outline.Segments)+1)
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
