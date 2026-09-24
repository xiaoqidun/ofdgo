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
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	typefont "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/harfbuzz"
	"github.com/go-text/typesetting/language"
	xlanguage "golang.org/x/text/language"
)

// fontShaper 使用字体度量和原始数据提供可配置塑形，不依赖绘图后端
type fontShaper struct {
	metrics     FontMetrics
	shapeMu     sync.Mutex
	shapeFont   *harfbuzz.Font
	shapeBuffer *harfbuzz.Buffer
}

// ShapeText 使用默认选项塑形单行文字，不改动原文定位
// 入参: value 单行原文, size 毫米字号
// 返回: []ShapedGlyph 定位字形, error 塑形错误
func (f *fontShaper) ShapeText(value string, size float64) ([]ShapedGlyph, error) {
	return f.ShapeTextWithOptions(value, size, TextShapeOptions{})
}

// ShapeTextWithOptions 使用现有纯Go塑形引擎处理单一方向原文，保留逻辑簇和视觉位置
// 不执行混合方向分段，调用方应将双向段落拆为单向文字对象
// 入参: value 单行原文, size 毫米字号, options 塑形选项
// 返回: []ShapedGlyph 定位字形, error 字符或选项错误
func (f *fontShaper) ShapeTextWithOptions(value string, size float64, options TextShapeOptions) ([]ShapedGlyph, error) {
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
		face, err := typefont.ParseTTF(bytes.NewReader(f.metrics.Write()))
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
	unit := size / float64(f.metrics.UnitsPerEm())
	result := make([]ShapedGlyph, len(buffer.Info))
	var x, y float64
	for i, info := range buffer.Info {
		if info.Glyph >= typefont.GID(f.metrics.NumGlyphs()) {
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
