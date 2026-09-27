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
	"math"
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
	kind, values := r.colorComponents(value, index, space)
	var components [4]uint8
	for i, value := range values {
		components[i] = uint8(math.Round(value * 255))
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

// colorComponents 读取颜色空间及归一化分量，保留源位深供渐变插值
// 入参: value 颜色值, index 调色板索引, space 颜色空间标识
// 返回: string 颜色模型, [4]float64 单位分量
func (r *Renderer) colorComponents(value string, index *int, space string) (string, [4]float64) {
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
	var components [4]float64
	limit := 1<<bits - 1
	if len(parts) >= count {
		for i, part := range parts[:count] {
			v, err := parseColorComponent(part)
			if err != nil || v < 0 || limit < v {
				components = [4]float64{}
				break
			}
			components[i] = float64(v) / float64(limit)
		}
	}
	return kind, components
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
	positions := gradientPositions(segments)
	for i, segment := range segments {
		segmentAlpha := mergeAlpha(segment.Color.Alpha, alpha)
		gradient = append(gradient, ColorStop{Offset: positions[i], Color: r.ResolveColor(segment.Color.Value, segment.Color.Index, segment.Color.ColorSpace, segmentAlpha)})
	}
	return gradient
}

// renderGradientStops 在CMYK分量空间插值，将RGB分段误差控制在半个8位灰阶内
// 入参: segments 原始颜色分段, alpha 对象透明度
// 返回: []ColorStop 仅供绘制的颜色节点，不改变原始分段
func (r *Renderer) renderGradientStops(segments []ShdSegment, alpha *int) []ColorStop {
	stops := r.GradientStops(segments, alpha)
	if len(stops) < 2 {
		return stops
	}
	var result []ColorStop
	for i, stop := range stops {
		if i > 0 && stops[i-1].Offset < stop.Offset {
			a, b := segments[i-1].Color, segments[i].Color
			leftKind, left := r.colorComponents(a.Value, a.Index, a.ColorSpace)
			rightKind, right := r.colorComponents(b.Value, b.Index, b.ColorSpace)
			if leftKind == "CMYK" && rightKind == "CMYK" {
				a0, a1 := float64(stops[i-1].Color.A)/255, float64(stop.Color.A)/255
				dk, da := right[3]-left[3], a1-a0
				bound := 0.0
				for c := 0; c < 3; c++ {
					dc := right[c] - left[c]
					bound = math.Max(bound, 2*math.Abs(dc*dk)+2*math.Abs(da)*(math.Abs(dc)+math.Abs(dk)))
				}
				steps := int(math.Ceil(math.Sqrt(bound * 255 / 4)))
				if steps > 1 && result == nil {
					result = append(make([]ColorStop, 0, len(stops)+steps-1), stops[:i]...)
				}
				for j := 1; j < steps; j++ {
					t := float64(j) / float64(steps)
					var values [4]float64
					for c := range values {
						values[c] = left[c]*(1-t) + right[c]*t
					}
					opacity := a0*(1-t) + a1*t
					black := (1 - values[3]) * opacity * 255
					color := color.RGBA{R: uint8(math.Round((1 - values[0]) * black)), G: uint8(math.Round((1 - values[1]) * black)), B: uint8(math.Round((1 - values[2]) * black)), A: uint8(math.Round(opacity * 255))}
					result = append(result, ColorStop{Offset: stops[i-1].Offset*(1-t) + stop.Offset*t, Color: color})
				}
			}
		}
		if result != nil {
			result = append(result, stop)
		}
	}
	if result == nil {
		return stops
	}
	return result
}

// gradientPositions 按显式锚点补全渐变分段位置，保留原顺序
// 入参: segments 渐变分段
// 返回: []float64 分段位置
func gradientPositions(segments []ShdSegment) []float64 {
	positions := make([]float64, len(segments))
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
		positions[i] = position
		position += step
	}
	return positions
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
