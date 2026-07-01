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

	"github.com/tdewolff/canvas"
)

// parseColor 解析颜色字符串
// 入参: val 颜色值(R G B)
// 返回: color.Color 颜色对象
func parseColor(val string) color.Color {
	return parseColorWithAlpha(val, nil)
}

// parseColorWithAlpha 解析带透明度的颜色
// 入参: val 颜色值, alpha 透明度
// 返回: color.Color 颜色对象
func parseColorWithAlpha(val string, alpha *int) color.Color {
	parts := strings.Fields(val)
	if len(parts) >= 3 {
		r, _ := strconv.Atoi(parts[0])
		g, _ := strconv.Atoi(parts[1])
		b, _ := strconv.Atoi(parts[2])
		a := 255
		if alpha != nil {
			a = *alpha
		}
		a = clampColor(a)
		return color.RGBA{
			R: uint8(clampColor(r) * a / 255),
			G: uint8(clampColor(g) * a / 255),
			B: uint8(clampColor(b) * a / 255),
			A: uint8(a),
		}
	}
	return color.Black
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
func parseFillColor(fillColor *FillColor) color.Color {
	if fillColor == nil {
		return nil
	}
	if fillColor.Pattern != nil {
		return nil
	}
	if strings.TrimSpace(fillColor.Value) != "" {
		return parseColorWithAlpha(fillColor.Value, fillColor.Alpha)
	}
	if fillColor.AxialShd != nil {
		return parseAxialShdColor(fillColor.AxialShd, fillColor.Alpha)
	}
	return parseRadialShdColor(fillColor.RadialShd, fillColor.Alpha)
}

// parseFillPaint 解析填充画刷
// 入参: fillColor 填充颜色节点, x X坐标, y Y坐标, pageH 页面高度, originX 原点X坐标, originY 原点Y坐标
// 返回: any 填充画刷
func parseFillPaint(fillColor *FillColor, x, y, pageH, originX, originY float64) any {
	if fillColor == nil {
		return nil
	}
	if fillColor.Pattern != nil {
		return nil
	}
	if strings.TrimSpace(fillColor.Value) != "" {
		return parseColorWithAlpha(fillColor.Value, fillColor.Alpha)
	}
	if gradient := parseAxialShdGradient(fillColor.AxialShd, fillColor.Alpha, x, y, pageH, originX, originY); gradient != nil {
		return gradient
	}
	if gradient := parseRadialShdGradient(fillColor.RadialShd, fillColor.Alpha, x, y, pageH, originX, originY); gradient != nil {
		return gradient
	}
	if fillColor.AxialShd != nil {
		return parseAxialShdColor(fillColor.AxialShd, fillColor.Alpha)
	}
	return parseRadialShdColor(fillColor.RadialShd, fillColor.Alpha)
}

// parseStrokeColor 解析勾边颜色
// 入参: strokeColor 勾边颜色节点
// 返回: color.Color 颜色对象
func parseStrokeColor(strokeColor *StrokeColor) color.Color {
	if strokeColor == nil {
		return nil
	}
	if strings.TrimSpace(strokeColor.Value) != "" {
		return parseColorWithAlpha(strokeColor.Value, strokeColor.Alpha)
	}
	if strokeColor.AxialShd != nil {
		return parseAxialShdColor(strokeColor.AxialShd, strokeColor.Alpha)
	}
	return parseRadialShdColor(strokeColor.RadialShd, strokeColor.Alpha)
}

// parseStrokePaint 解析勾边画刷
// 入参: strokeColor 勾边颜色节点, x X坐标, y Y坐标, pageH 页面高度, originX 原点X坐标, originY 原点Y坐标
// 返回: any 勾边画刷
func parseStrokePaint(strokeColor *StrokeColor, x, y, pageH, originX, originY float64) any {
	if strokeColor == nil {
		return nil
	}
	if strings.TrimSpace(strokeColor.Value) != "" {
		return parseColorWithAlpha(strokeColor.Value, strokeColor.Alpha)
	}
	if gradient := parseAxialShdGradient(strokeColor.AxialShd, strokeColor.Alpha, x, y, pageH, originX, originY); gradient != nil {
		return gradient
	}
	if gradient := parseRadialShdGradient(strokeColor.RadialShd, strokeColor.Alpha, x, y, pageH, originX, originY); gradient != nil {
		return gradient
	}
	if strokeColor.AxialShd != nil {
		return parseAxialShdColor(strokeColor.AxialShd, strokeColor.Alpha)
	}
	return parseRadialShdColor(strokeColor.RadialShd, strokeColor.Alpha)
}

// parseAxialShdColor 解析轴向渐变颜色
// 入参: axialShd 轴向渐变节点, alpha 透明度
// 返回: color.Color 颜色对象
func parseAxialShdColor(axialShd *AxialShd, alpha *int) color.Color {
	if axialShd != nil {
		for _, segment := range axialShd.Segment {
			if strings.TrimSpace(segment.Color.Value) == "" {
				continue
			}
			if alpha == nil {
				alpha = segment.Color.Alpha
			}
			return parseColorWithAlpha(segment.Color.Value, alpha)
		}
	}
	return color.Black
}

// parseAxialShdGradient 解析轴向渐变
// 入参: axialShd 轴向渐变节点, alpha 透明度, x X坐标, y Y坐标, pageH 页面高度, originX 原点X坐标, originY 原点Y坐标
// 返回: canvas.Gradient 渐变对象
func parseAxialShdGradient(axialShd *AxialShd, alpha *int, x, y, pageH, originX, originY float64) canvas.Gradient {
	if axialShd == nil {
		return nil
	}
	start := parseFloats(axialShd.StartPoint)
	end := parseFloats(axialShd.EndPoint)
	if len(start) < 2 || len(end) < 2 {
		return nil
	}
	grad := canvas.NewGradient()
	for _, segment := range axialShd.Segment {
		if strings.TrimSpace(segment.Color.Value) == "" {
			continue
		}
		segmentAlpha := alpha
		if segmentAlpha == nil {
			segmentAlpha = segment.Color.Alpha
		}
		grad.Add(segment.Position, colorToRGBA(parseColorWithAlpha(segment.Color.Value, segmentAlpha)))
	}
	if len(grad) == 0 {
		return nil
	}
	startPoint := canvas.Point{X: x + start[0] - originX, Y: pageH - (y + start[1]) - originY}
	endPoint := canvas.Point{X: x + end[0] - originX, Y: pageH - (y + end[1]) - originY}
	if startPoint.Equals(endPoint) {
		return nil
	}
	return grad.ToLinear(startPoint, endPoint)
}

// parseRadialShdColor 解析径向渐变颜色
// 入参: radialShd 径向渐变节点, alpha 透明度
// 返回: color.Color 颜色对象
func parseRadialShdColor(radialShd *RadialShd, alpha *int) color.Color {
	if radialShd != nil {
		for _, segment := range radialShd.Segment {
			if strings.TrimSpace(segment.Color.Value) == "" {
				continue
			}
			if alpha == nil {
				alpha = segment.Color.Alpha
			}
			return parseColorWithAlpha(segment.Color.Value, alpha)
		}
	}
	return color.Black
}

// parseRadialShdGradient 解析径向渐变
// 入参: radialShd 径向渐变节点, alpha 透明度, x X坐标, y Y坐标, pageH 页面高度, originX 原点X坐标, originY 原点Y坐标
// 返回: canvas.Gradient 渐变对象
func parseRadialShdGradient(radialShd *RadialShd, alpha *int, x, y, pageH, originX, originY float64) canvas.Gradient {
	if radialShd == nil || radialShd.EndRadius <= 0 {
		return nil
	}
	start := parseFloats(radialShd.StartPoint)
	end := parseFloats(radialShd.EndPoint)
	if len(start) < 2 || len(end) < 2 {
		return nil
	}
	grad := canvas.NewGradient()
	for _, segment := range radialShd.Segment {
		if strings.TrimSpace(segment.Color.Value) == "" {
			continue
		}
		segmentAlpha := alpha
		if segmentAlpha == nil {
			segmentAlpha = segment.Color.Alpha
		}
		grad.Add(segment.Position, colorToRGBA(parseColorWithAlpha(segment.Color.Value, segmentAlpha)))
	}
	if len(grad) == 0 {
		return nil
	}
	startPoint := canvas.Point{X: x + start[0] - originX, Y: pageH - (y + start[1]) - originY}
	endPoint := canvas.Point{X: x + end[0] - originX, Y: pageH - (y + end[1]) - originY}
	return grad.ToRadial(startPoint, radialShd.StartRadius, endPoint, radialShd.EndRadius)
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

// GetDeltaX 获取X轴偏移量数组
// 返回: []float64 偏移量数组
func (tc *TextCode) GetDeltaX() []float64 {
	return parseFloats(tc.DeltaX)
}

// GetDeltaY 获取Y轴偏移量数组
// 返回: []float64 偏移量数组
func (tc *TextCode) GetDeltaY() []float64 {
	return parseFloats(tc.DeltaY)
}
