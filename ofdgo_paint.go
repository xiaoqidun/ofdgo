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
	"image/color"
	"math"
	"slices"
	"strconv"
	"strings"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
)

// PatternPaint 底纹画刷
type PatternPaint struct {
	*Pattern
	Color FillColor
	Alpha *int
}

// ColorStop 保存渐变区间位置及预乘RGBA颜色节点
type ColorStop struct {
	Offset float64
	Color  color.RGBA
}

// PaintKind 区分无画刷、纯色、轴向渐变、径向渐变、图案和网格渐变
type PaintKind uint8

// 画刷类型，区分纯色、渐变和图案填充
const (
	PaintNone PaintKind = iota
	PaintSolid
	PaintLinear
	PaintRadial
	PaintPattern
	PaintMesh
)

// Paint 保存后端无关的颜色、渐变和图案，不修改源节点
type Paint struct {
	Kind     PaintKind
	Color    color.RGBA
	Gradient *Shading
	Pattern  *PatternPaint
	Mesh     *MeshShading
}

// Shading 保存OFD对象局部坐标中的渐变，单位为毫米，纵轴向下
// Extend、MapType和MapUnit保留标准的延伸及重复语义
// Sample存在时按单位区间采样源颜色变换，Stops仍保存各段的显示颜色
type Shading struct {
	Start, End                   Point
	StartRadius, EndRadius       float64
	Stops                        []ColorStop
	Extend, MapType              string
	MapUnit, Eccentricity, Angle float64
	Sample                       func(float64) color.RGBA
}

// ResolveColor 按文档颜色空间和调色板解析颜色，返回预乘透明度的RGBA
// 入参: value 颜色分量, index 调色板索引, space 颜色空间标识, alpha 透明度
// 返回: color.RGBA 标准颜色, error 颜色定义或配置错误
func (r *Renderer) ResolveColor(value string, index *int, space string, alpha *int) (color.RGBA, error) {
	kind, values, err := r.colorComponents(value, index, space)
	if err != nil {
		return color.RGBA{}, err
	}
	if alpha != nil && (*alpha < 0 || *alpha > 255) {
		return color.RGBA{}, fmt.Errorf("color alpha must be between 0 and 255")
	}
	profile, err := r.Reader.colorProfile(r.colorDefinition(space))
	if err != nil {
		return color.RGBA{}, err
	}
	rgb, err := displayColor(kind, values, profile)
	if err != nil {
		return color.RGBA{}, err
	}
	a := 1.0
	if alpha != nil {
		a = float64(*alpha) / 255
	}
	return profileColor(rgb, a), nil
}

// GradientStops 解析OFD渐变分段的位置与透明度，返回独立数据并保留原分段顺序
// 入参: segments 渐变分段, alpha 透明度
// 返回: []ColorStop 后端无关的渐变色标, error 颜色定义或配置错误
func (r *Renderer) GradientStops(segments []ShdSegment, alpha *int) ([]ColorStop, error) {
	var gradient []ColorStop
	positions := gradientPositions(segments)
	for i, segment := range segments {
		segmentAlpha := mergeAlpha(segment.Color.Alpha, alpha)
		value, err := r.ResolveColor(segment.Color.Value, segment.Color.Index, segment.Color.ColorSpace, segmentAlpha)
		if err != nil {
			return nil, err
		}
		gradient = append(gradient, ColorStop{Offset: positions[i], Color: value})
	}
	return gradient, nil
}

// ResolvePaint 解析颜色、透明度、渐变分段和图案，不依赖绘图库
// 入参: fill 填充或转换后的描边节点
// 返回: Paint 只读画刷, error 无效渐变
func (r *Renderer) ResolvePaint(fill *FillColor) (Paint, error) {
	previous := r.renderError
	r.renderError = nil
	defer func() { r.renderError = previous }()
	if fill == nil {
		return Paint{}, nil
	}
	if fill.Pattern != nil {
		return Paint{Kind: PaintPattern, Pattern: parsePatternPaint(fill)}, nil
	}
	if fill.GouraudShd != nil || fill.LaGouraudShd != nil {
		mesh, err := r.resolveMesh(fill)
		if err != nil {
			return Paint{}, err
		}
		if r.renderError != nil {
			return Paint{}, r.renderError
		}
		return Paint{Kind: PaintMesh, Mesh: mesh}, nil
	}
	base := r.parseFillColor(fill)
	if r.renderError != nil {
		return Paint{}, r.renderError
	}
	if base == nil {
		return Paint{}, nil
	}
	paint := Paint{Kind: PaintSolid, Color: colorToRGBA(base)}
	if node := fill.AxialShd; node != nil {
		start, end := parseFloats(node.StartPoint), parseFloats(node.EndPoint)
		stops := r.renderGradientStops(node.Segment, fill.Alpha)
		if r.renderError != nil {
			return Paint{}, r.renderError
		}
		sample, err := r.profileGradient(node.Segment, fill.Alpha)
		if err != nil {
			return Paint{}, err
		}
		if len(start) >= 2 && len(end) >= 2 && len(stops) != 0 && (!basegeom.Equal(start[0], end[0]) || !basegeom.Equal(start[1], end[1])) {
			paint.Kind = PaintLinear
			paint.Gradient = &Shading{Start: Point{X: start[0], Y: start[1]}, End: Point{X: end[0], Y: end[1]}, Stops: stops, Extend: node.Extend, MapType: node.MapType, MapUnit: node.MapUnit}
			paint.Gradient.Sample = sample
			return paint, nil
		}
	}
	if node := fill.RadialShd; node != nil && node.StartRadius >= 0 && node.EndRadius >= 0 && (node.StartRadius > 0 || node.EndRadius > 0) {
		start, end := parseFloats(node.StartPoint), parseFloats(node.EndPoint)
		stops := r.renderGradientStops(node.Segment, fill.Alpha)
		if r.renderError != nil {
			return Paint{}, r.renderError
		}
		sample, err := r.profileGradient(node.Segment, fill.Alpha)
		if err != nil {
			return Paint{}, err
		}
		if len(start) >= 2 && len(end) >= 2 && len(stops) != 0 {
			paint.Kind = PaintRadial
			paint.Gradient = &Shading{Start: Point{X: start[0], Y: start[1]}, End: Point{X: end[0], Y: end[1]}, StartRadius: node.StartRadius, EndRadius: node.EndRadius, Stops: stops, Extend: node.Extend, MapType: node.MapType, MapUnit: node.MapUnit, Eccentricity: node.Eccentricity, Angle: node.Angle}
			paint.Gradient.Sample = sample
		}
	}
	return paint, nil
}

// profileGradient 保存独立色标和只读配置，延伸及重复由几何采样器处理
type profileGradient struct {
	positions []float64
	colors    []MeshVertex
}

// At 在相同源空间内插值，不同空间先转换为sRGB，保留重复位置的右侧色标
// 入参: t 单位区间位置
// 返回: color.RGBA 预乘采样颜色
func (g *profileGradient) At(t float64) color.RGBA {
	if len(g.colors) == 0 || math.IsNaN(t) {
		return color.RGBA{}
	}
	first := g.colors[0]
	if t < g.positions[0] {
		r, green, b := first.rgb(first.Values)
		return profileColor([3]float64{r, green, b}, first.Alpha)
	}
	for i := 1; i < len(g.colors); i++ {
		if t < g.positions[i] {
			before, after := g.colors[i-1], g.colors[i]
			x := (t - g.positions[i-1]) / (g.positions[i] - g.positions[i-1])
			if before.sameSpace(after) {
				var values [4]float64
				for j := range values {
					values[j] = before.Values[j]*(1-x) + after.Values[j]*x
				}
				r, green, b := before.rgb(values)
				return profileColor([3]float64{r, green, b}, before.Alpha*(1-x)+after.Alpha*x)
			}
			r0, g0, b0 := before.rgb(before.Values)
			r1, g1, b1 := after.rgb(after.Values)
			return color.RGBA{meshByte(r0*before.Alpha*(1-x) + r1*after.Alpha*x), meshByte(g0*before.Alpha*(1-x) + g1*after.Alpha*x), meshByte(b0*before.Alpha*(1-x) + b1*after.Alpha*x), meshByte(before.Alpha*(1-x) + after.Alpha*x)}
		}
	}
	last := g.colors[len(g.colors)-1]
	r, green, b := last.rgb(last.Values)
	return profileColor([3]float64{r, green, b}, last.Alpha)
}

// parseColorWithAlpha 解析带透明度的颜色
// 入参: value 色值, index 调色板索引, space 颜色空间标识, alpha 透明度
// 返回: color.Color 颜色对象
func (r *Renderer) parseColorWithAlpha(value string, index *int, space string, alpha *int) color.Color {
	result, err := r.ResolveColor(value, index, space, alpha)
	if err != nil {
		r.renderError = err
	}
	return result
}

// colorComponents 读取颜色空间及归一化分量，保留源位深供渐变插值
// 入参: value 色值, index 调色板索引, space 颜色空间标识
// 返回: string 颜色模型, [4]float64 单位分量, error 颜色定义错误
func (r *Renderer) colorComponents(value string, index *int, space string) (string, [4]float64, error) {
	cs := r.colorDefinition(space)
	defaultSpace := r.Reader != nil && r.Reader.doc != nil && r.Reader.doc.CommonData.DefaultCS != 0
	if cs == nil && (defaultSpace || space != "" && (index != nil || len(strings.Fields(value)) != 3)) {
		if space == "" {
			space = formatUnitID(uint64(r.Reader.doc.CommonData.DefaultCS))
		}
		return "", [4]float64{}, fmt.Errorf("color space %q not found", space)
	}
	kind, bits, count := "RGB", 8, 3
	if cs != nil {
		kind = strings.ToUpper(cs.Type)
		switch {
		case cs.BitsPerComponent == 0 && !cs.bitsSet:
		case cs.BitsPerComponent == 1, cs.BitsPerComponent == 2, cs.BitsPerComponent == 4, cs.BitsPerComponent == 8, cs.BitsPerComponent == 16:
			bits = cs.BitsPerComponent
		default:
			return "", [4]float64{}, fmt.Errorf("unsupported color depth %d", cs.BitsPerComponent)
		}
	}
	if strings.TrimSpace(value) == "" && index != nil {
		if cs == nil || *index < 0 || *index >= len(cs.Palette) {
			return "", [4]float64{}, fmt.Errorf("color palette index out of range")
		}
		value = cs.Palette[*index]
	}
	switch kind {
	case "RGB":
	case "GRAY":
		count = 1
	case "CMYK":
		count = 4
	default:
		return "", [4]float64{}, fmt.Errorf("unsupported color space %q", kind)
	}
	parts := strings.Fields(value)
	var components [4]float64
	limit := 1<<bits - 1
	if len(parts) != 0 && len(parts) != count {
		return "", components, fmt.Errorf("expected %d color components", count)
	}
	for i, part := range parts {
		v, err := parseColorComponent(part)
		if err != nil || v < 0 || limit < v {
			return "", [4]float64{}, fmt.Errorf("invalid color component %q", part)
		}
		components[i] = float64(v) / float64(limit)
	}
	return kind, components, nil
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
	for _, value := range []*int{colorAlpha, objectAlpha} {
		if value != nil && (*value < 0 || *value > 255) {
			alpha := *value
			return &alpha
		}
	}
	alpha := 255
	if colorAlpha != nil {
		alpha = *colorAlpha
	}
	if objectAlpha != nil {
		alpha = alpha * *objectAlpha / 255
	}
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
	if fillColor.Pattern != nil || fillColor.GouraudShd != nil || fillColor.LaGouraudShd != nil {
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

// renderGradientStops 在源CMYK空间插值，保留截断边界及透明度变化
// 入参: segments 原始颜色分段, alpha 对象透明度
// 返回: []ColorStop 仅供绘制的颜色节点，不改变原始分段
func (r *Renderer) renderGradientStops(segments []ShdSegment, alpha *int) []ColorStop {
	stops, err := r.GradientStops(segments, alpha)
	if err != nil {
		r.renderError = err
		return nil
	}
	if len(stops) < 2 {
		return stops
	}
	var result []ColorStop
	for i, stop := range stops {
		if i > 0 && stops[i-1].Offset < stop.Offset {
			a, b := segments[i-1].Color, segments[i].Color
			leftKind, left, _ := r.colorComponents(a.Value, a.Index, a.ColorSpace)
			rightKind, right, _ := r.colorComponents(b.Value, b.Index, b.ColorSpace)
			leftSpace, rightSpace := r.colorDefinition(a.ColorSpace), r.colorDefinition(b.ColorSpace)
			if leftKind == "CMYK" && rightKind == "CMYK" && (leftSpace == nil || leftSpace.Profile == "") && (rightSpace == nil || rightSpace.Profile == "") {
				if result == nil {
					result = append([]ColorStop(nil), stops[:i]...)
				}
				result = append(result, cmykGradientStops(left, right, stops[i-1], stop)...)
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

// cmykGradientStops 在加黑截断点分段，将预乘插值误差控制在四分之一灰阶内
// 入参: left 左端分量, right 右端分量, start 左端色标, end 右端色标
// 返回: []ColorStop 不含端点的显示色标
func cmykGradientStops(left, right [4]float64, start, end ColorStop) []ColorStop {
	positions := []float64{0, 1}
	a0, a1 := float64(start.Color.A)/255, float64(end.Color.A)/255
	bound := 0.0
	for channel := 0; channel < 3; channel++ {
		first, last := left[channel]+left[3], right[channel]+right[3]
		delta := last - first
		bound = math.Max(bound, 2*math.Abs((a1-a0)*delta))
		if delta != 0 {
			position := (1 - first) / delta
			if 0 < position && position < 1 {
				positions = append(positions, position)
			}
		}
	}
	slices.Sort(positions)
	positions = slices.Compact(positions)
	var result []ColorStop
	for i := 1; i < len(positions); i++ {
		first, last := positions[i-1], positions[i]
		steps := max(1, int(math.Ceil((last-first)*math.Sqrt(bound*255/2))))
		for step := 1; step <= steps; step++ {
			t := first + (last-first)*float64(step)/float64(steps)
			if t >= 1 {
				continue
			}
			var values [4]float64
			for channel := range values {
				values[channel] = left[channel]*(1-t) + right[channel]*t
			}
			r, g, b := meshRGB("CMYK", values)
			result = append(result, ColorStop{
				Offset: start.Offset*(1-t) + end.Offset*t,
				Color:  profileColor([3]float64{r, g, b}, a0*(1-t)+a1*t),
			})
		}
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

// profileGradient 准备ICC渐变的源分量采样，不用转换后的端点近似非线性色彩变换
// 入参: segments 原色标, alpha 外层透明度
// 返回: func(float64) color.RGBA 单位区间采样器，无ICC时为nil, error 配置错误
func (r *Renderer) profileGradient(segments []ShdSegment, alpha *int) (func(float64) color.RGBA, error) {
	profiled := false
	for _, segment := range segments {
		if space := r.colorDefinition(segment.Color.ColorSpace); space != nil && space.Profile != "" {
			profiled = true
			break
		}
	}
	if !profiled {
		return nil, nil
	}
	gradient := &profileGradient{positions: gradientPositions(segments), colors: make([]MeshVertex, len(segments))}
	for i, segment := range segments {
		kind, values, err := r.colorComponents(segment.Color.Value, segment.Color.Index, segment.Color.ColorSpace)
		if err != nil {
			return nil, err
		}
		profile, err := r.Reader.colorProfile(r.colorDefinition(segment.Color.ColorSpace))
		if err != nil {
			return nil, err
		}
		opacity := 1.0
		if value := mergeAlpha(segment.Color.Alpha, alpha); value != nil {
			opacity = float64(clampColor(*value)) / 255
		}
		gradient.colors[i] = MeshVertex{Space: kind, Values: values, Alpha: opacity, profile: profile}
	}
	return gradient.At, nil
}
