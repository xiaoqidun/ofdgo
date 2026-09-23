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
	"image/color"
	"strconv"
	"strings"
)

// ResolveColor 按文档颜色空间和调色板解析颜色，返回预乘透明度的RGBA
// 入参: value 颜色分量, index 调色板索引, space 颜色空间标识, alpha 透明度
// 返回: color.RGBA 标准颜色
func (r *Renderer) ResolveColor(value string, index *int, space string, alpha *int) color.RGBA {
	return colorToRGBA(r.parseColorWithAlpha(value, index, space, alpha))
}

// parseColorWithAlpha 解析带透明度的颜色
// 入参: value 颜色值, index 调色板索引, space 颜色空间标识, alpha 透明度
// 返回: color.Color 颜色对象
func (r *Renderer) parseColorWithAlpha(value string, index *int, space string, alpha *int) color.Color {
	if space == "" && r.Reader.doc != nil {
		space = strconv.Itoa(r.Reader.doc.CommonData.DefaultCS)
	}
	cs := r.Reader.colorSpaceCache[space]
	kind, bits, count := "RGB", 8, 3
	if cs != nil {
		kind = cs.Type
		switch cs.BitsPerComponent {
		case 1, 2, 4, 8, 16:
			bits = cs.BitsPerComponent
		}
		if strings.TrimSpace(value) == "" && index != nil && 0 <= *index && *index < len(cs.Palette) {
			value = cs.Palette[*index]
		}
	}
	switch kind {
	case "GRAY":
		count = 1
	case "CMYK":
		count = 4
	}
	parts := strings.Fields(value)
	var components [4]uint8
	limit := 1<<bits - 1
	if len(parts) >= count {
		for i, part := range parts[:count] {
			v, err := parseColorComponent(part)
			if err != nil || v < 0 || limit < v {
				components = [4]uint8{}
				break
			}
			components[i] = uint8((v*255 + limit/2) / limit)
		}
	}
	red, green, blue := components[0], components[1], components[2]
	switch kind {
	case "GRAY":
		green, blue = red, red
	case "CMYK":
		red, green, blue = color.CMYKToRGB(components[0], components[1], components[2], components[3])
	}
	a := 255
	if alpha != nil {
		a = clampColor(*alpha)
	}
	return color.RGBA{R: uint8(int(red) * a / 255), G: uint8(int(green) * a / 255), B: uint8(int(blue) * a / 255), A: uint8(a)}
}

// parseColorComponent 解析颜色分量
// 入参: s 颜色分量
// 返回: int 颜色分量值, error 错误信息
func parseColorComponent(s string) (int, error) {
	if strings.HasPrefix(s, "#") {
		v, err := strconv.ParseInt(s[1:], 16, 0)
		return int(v), err
	}
	return strconv.Atoi(s)
}

// clampColor 限制颜色分量范围
// 入参: v 颜色分量
// 返回: int 颜色分量
func clampColor(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// mergeAlpha 合并透明度
// 入参: colorAlpha 颜色透明度, objectAlpha 对象透明度
// 返回: *int 合并后的透明度
func mergeAlpha(colorAlpha, objectAlpha *int) *int {
	if colorAlpha == nil && objectAlpha == nil {
		return nil
	}
	alpha := 255
	if colorAlpha != nil {
		alpha = *colorAlpha
	}
	if objectAlpha != nil {
		alpha = alpha * *objectAlpha / 255
	}
	alpha = clampColor(alpha)
	return &alpha
}

// withFillAlpha 合并填充色透明度
// 入参: fillColor 填充颜色节点, alpha 对象透明度
// 返回: *FillColor 合并后的填充颜色节点
func withFillAlpha(fillColor *FillColor, alpha *int) *FillColor {
	if fillColor == nil || alpha == nil {
		return fillColor
	}
	merged := *fillColor
	merged.Alpha = mergeAlpha(fillColor.Alpha, alpha)
	return &merged
}

// withStrokeAlpha 合并勾边色透明度
// 入参: strokeColor 勾边颜色节点, alpha 对象透明度
// 返回: *StrokeColor 合并后的勾边颜色节点
func withStrokeAlpha(strokeColor *StrokeColor, alpha *int) *StrokeColor {
	if strokeColor == nil || alpha == nil {
		return strokeColor
	}
	merged := *strokeColor
	merged.Alpha = mergeAlpha(strokeColor.Alpha, alpha)
	return &merged
}

// colorWithAlpha 合并颜色透明度
// 入参: c 颜色对象, alpha 对象透明度
// 返回: color.Color 合并后的颜色对象
func colorWithAlpha(c color.Color, alpha *int) color.Color {
	if c == nil || alpha == nil {
		return c
	}
	a := clampColor(*alpha)
	rgba := colorToRGBA(c)
	return color.RGBA{
		R: uint8(int(rgba.R) * a / 255),
		G: uint8(int(rgba.G) * a / 255),
		B: uint8(int(rgba.B) * a / 255),
		A: uint8(int(rgba.A) * a / 255),
	}
}

// parseFillColor 解析填充颜色
// 入参: fillColor 填充颜色节点
// 返回: color.Color 颜色对象
func (r *Renderer) parseFillColor(fillColor *FillColor) color.Color {
	if fillColor == nil {
		return nil
	}
	if fillColor.Pattern != nil {
		return nil
	}
	if fillColor.AxialShd != nil {
		return r.parseShdColor(fillColor.AxialShd.Segment, fillColor.Alpha)
	}
	if fillColor.RadialShd != nil {
		return r.parseShdColor(fillColor.RadialShd.Segment, fillColor.Alpha)
	}
	if fillColor.unsupported && strings.TrimSpace(fillColor.Value) == "" && fillColor.Index == nil {
		return nil
	}
	return r.parseColorWithAlpha(fillColor.Value, fillColor.Index, fillColor.ColorSpace, fillColor.Alpha)
}

// parseStrokeColor 解析勾边颜色
// 入参: strokeColor 勾边颜色节点
// 返回: color.Color 颜色对象
func (r *Renderer) parseStrokeColor(strokeColor *StrokeColor) color.Color {
	return r.parseFillColor((*FillColor)(strokeColor))
}

// PatternPaint 底纹画刷
type PatternPaint struct {
	*Pattern
	Color FillColor
	Alpha *int
}

// parsePatternPaint 解析底纹画刷
// 入参: fill 填充颜色节点
// 返回: *PatternPaint 底纹画刷
func parsePatternPaint(fill *FillColor) *PatternPaint {
	if fill.Pattern == nil {
		return nil
	}
	return &PatternPaint{Pattern: fill.Pattern, Color: FillColor{Value: fill.Value, Index: fill.Index, ColorSpace: fill.ColorSpace}, Alpha: fill.Alpha}
}

// parseShdColor 解析渐变颜色
// 入参: segments 渐变分段, alpha 透明度
// 返回: color.Color 颜色对象
func (r *Renderer) parseShdColor(segments []ShdSegment, alpha *int) color.Color {
	if len(segments) == 0 {
		return color.Black
	}
	c := segments[0].Color
	return r.parseColorWithAlpha(c.Value, c.Index, c.ColorSpace, mergeAlpha(c.Alpha, alpha))
}

// GradientStops 解析OFD渐变分段的位置与透明度，返回独立数据并保留原分段顺序
// 入参: segments 渐变分段, alpha 透明度
// 返回: []ColorStop 后端无关的渐变分段
func (r *Renderer) GradientStops(segments []ShdSegment, alpha *int) []ColorStop {
	var gradient []ColorStop
	position, step := 0.0, 0.0
	for i, segment := range segments {
		if !segment.positionMissing {
			position = segment.Position
		} else if i == len(segments)-1 {
			position = 1
		}
		if i == 0 || !segment.positionMissing {
			next := i + 1
			for next < len(segments)-1 && segments[next].positionMissing {
				next++
			}
			end := 1.0
			if next < len(segments) && !segments[next].positionMissing {
				end = segments[next].Position
			}
			step = (end - position) / float64(next-i)
		}
		offset := position
		position += step
		segmentAlpha := mergeAlpha(segment.Color.Alpha, alpha)
		gradient = append(gradient, ColorStop{Offset: offset, Color: r.ResolveColor(segment.Color.Value, segment.Color.Index, segment.Color.ColorSpace, segmentAlpha)})
	}
	if len(gradient) == 0 {
		return nil
	}
	return gradient
}

// colorToRGBA 转换颜色对象
// 入参: c 颜色对象
// 返回: color.RGBA RGBA颜色
func colorToRGBA(c color.Color) color.RGBA {
	if rgba, ok := c.(color.RGBA); ok {
		return rgba
	}
	r, g, b, a := c.RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}
