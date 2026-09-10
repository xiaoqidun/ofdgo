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

	"github.com/tdewolff/canvas"
)

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

// parseFillPaint 解析填充画刷
// 入参: fillColor 填充颜色节点, x X坐标, y Y坐标, pageH 页面高度
// 返回: any 填充画刷
func (r *Renderer) parseFillPaint(fillColor *FillColor, x, y, pageH float64) any {
	if fillColor == nil {
		return nil
	}
	if fillColor.Pattern != nil {
		return nil
	}
	if gradient := r.parseAxialShdGradient(fillColor.AxialShd, fillColor.Alpha, x, y, pageH); gradient != nil {
		return newShdPaint(gradient, fillColor.AxialShd.Extend, fillColor.AxialShd.MapType, fillColor.AxialShd.MapUnit)
	}
	if paint := r.parseRadialShdPaint(fillColor.RadialShd, fillColor.Alpha, x, y, pageH); paint != nil {
		return paint
	}
	return r.parseFillColor(fillColor)
}

// parseStrokeColor 解析勾边颜色
// 入参: strokeColor 勾边颜色节点
// 返回: color.Color 颜色对象
func (r *Renderer) parseStrokeColor(strokeColor *StrokeColor) color.Color {
	return r.parseFillColor((*FillColor)(strokeColor))
}

// parseStrokePaint 解析勾边画刷
// 入参: strokeColor 勾边颜色节点, x X坐标, y Y坐标, pageH 页面高度
// 返回: any 勾边画刷
func (r *Renderer) parseStrokePaint(strokeColor *StrokeColor, x, y, pageH float64) any {
	return r.parseFillPaint((*FillColor)(strokeColor), x, y, pageH)
}

// patternPaint 底纹画刷
type patternPaint struct {
	*Pattern
	color FillColor
	alpha *int
}

// parsePatternPaint 解析底纹画刷
// 入参: fill 填充颜色节点
// 返回: *patternPaint 底纹画刷
func parsePatternPaint(fill *FillColor) *patternPaint {
	if fill.Pattern == nil {
		return nil
	}
	return &patternPaint{Pattern: fill.Pattern, color: FillColor{Value: fill.Value, Index: fill.Index, ColorSpace: fill.ColorSpace}, alpha: fill.Alpha}
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

// parseShdSegments 解析渐变分段
// 入参: segments 渐变分段, alpha 透明度
// 返回: canvas.Grad 渐变分段
func (r *Renderer) parseShdSegments(segments []ShdSegment, alpha *int) canvas.Grad {
	gradient := canvas.NewGradient()
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
		gradient.Add(offset, colorToRGBA(r.parseColorWithAlpha(segment.Color.Value, segment.Color.Index, segment.Color.ColorSpace, segmentAlpha)))
	}
	if len(gradient) == 0 {
		return nil
	}
	return gradient
}

// parseAxialShdGradient 解析轴向渐变
// 入参: axialShd 轴向渐变节点, alpha 透明度, x X坐标, y Y坐标, pageH 页面高度
// 返回: canvas.Gradient 渐变对象
func (r *Renderer) parseAxialShdGradient(axialShd *AxialShd, alpha *int, x, y, pageH float64) canvas.Gradient {
	if axialShd == nil {
		return nil
	}
	start := parseFloats(axialShd.StartPoint)
	end := parseFloats(axialShd.EndPoint)
	if len(start) < 2 || len(end) < 2 {
		return nil
	}
	gradient := r.parseShdSegments(axialShd.Segment, alpha)
	if gradient == nil {
		return nil
	}
	startPoint := canvas.Point{X: x + start[0], Y: pageH - (y + start[1])}
	endPoint := canvas.Point{X: x + end[0], Y: pageH - (y + end[1])}
	if startPoint.Equals(endPoint) {
		return nil
	}
	return gradient.ToLinear(startPoint, endPoint)
}

// axialShdClip 获取轴向渐变的延伸裁剪区域
// 入参: gradient 轴向渐变, extend 延伸方向, bounds 可见区域
// 返回: *canvas.Path 裁剪区域
func axialShdClip(gradient *canvas.LinearGradient, extend int, bounds canvas.Rect) *canvas.Path {
	if extend == 3 {
		return nil
	}
	d := gradient.End.Sub(gradient.Start)
	axis := canvas.Matrix{{d.X, -d.Y, gradient.Start.X}, {d.Y, d.X, gradient.Start.Y}}
	area := bounds.Transform(axis.Inv())
	if extend&1 == 0 {
		area.X0 = math.Max(area.X0, 0)
	}
	if extend&2 == 0 {
		area.X1 = math.Min(area.X1, 1)
	}
	if area.X1 <= area.X0 {
		return &canvas.Path{}
	}
	return area.ToPath().Transform(axis)
}

// parseRadialShdPaint 解析径向渐变画刷
// 入参: radialShd 径向渐变节点, alpha 透明度, x X坐标, y Y坐标, pageH 页面高度
// 返回: *shdPaint 渐变画刷
func (r *Renderer) parseRadialShdPaint(radialShd *RadialShd, alpha *int, x, y, pageH float64) *shdPaint {
	if radialShd == nil || radialShd.EndRadius <= 0 {
		return nil
	}
	start := parseFloats(radialShd.StartPoint)
	end := parseFloats(radialShd.EndPoint)
	if len(start) < 2 || len(end) < 2 {
		return nil
	}
	gradient := r.parseShdSegments(radialShd.Segment, alpha)
	if gradient == nil {
		return nil
	}
	startPoint := canvas.Point{X: x + start[0], Y: pageH - (y + start[1])}
	endPoint := canvas.Point{X: x + end[0], Y: pageH - (y + end[1])}
	paint := newShdPaint(gradient.ToRadial(startPoint, radialShd.StartRadius, endPoint, radialShd.EndRadius), radialShd.Extend, radialShd.MapType, radialShd.MapUnit)
	if e := radialShd.Eccentricity; 0 < e && e < 1 {
		paint.view = canvas.Identity.Translate(startPoint.X, startPoint.Y).Rotate(-radialShd.Angle).Scale(1, math.Sqrt(1-e*e)).Translate(-startPoint.X, -startPoint.Y)
		paint.gradient = gradient.ToRadial(startPoint, radialShd.StartRadius, paint.view.Inv().Dot(endPoint), radialShd.EndRadius)
	}
	return paint
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
