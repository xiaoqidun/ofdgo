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
	"github.com/tdewolff/font"
)

// parseColorWithAlpha 解析带透明度的颜色
// 入参: val 颜色值, alpha 透明度
// 返回: color.Color 颜色对象
func parseColorWithAlpha(val string, alpha *int) color.Color {
	parts := strings.Fields(val)
	if len(parts) >= 3 {
		r := parseColorComponent(parts[0])
		g := parseColorComponent(parts[1])
		b := parseColorComponent(parts[2])
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

// parseColorComponent 解析颜色分量
// 入参: s 颜色分量
// 返回: int 颜色分量值
func parseColorComponent(s string) int {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "#") {
		v, _ := strconv.ParseInt(strings.TrimPrefix(s, "#"), 16, 0)
		return int(v)
	}
	v, _ := strconv.Atoi(s)
	return v
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
	if fillColor.AxialShd != nil {
		return parseAxialShdColor(fillColor.AxialShd, fillColor.Alpha)
	}
	if fillColor.RadialShd != nil {
		return parseRadialShdColor(fillColor.RadialShd, fillColor.Alpha)
	}
	if strings.TrimSpace(fillColor.Value) != "" {
		return parseColorWithAlpha(fillColor.Value, fillColor.Alpha)
	}
	return nil
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
	if gradient := parseAxialShdGradient(fillColor.AxialShd, fillColor.Alpha, x, y, pageH, originX, originY); gradient != nil {
		return gradient
	}
	if gradient := parseRadialShdGradient(fillColor.RadialShd, fillColor.Alpha, x, y, pageH, originX, originY); gradient != nil {
		return gradient
	}
	if fillColor.AxialShd != nil {
		return parseAxialShdColor(fillColor.AxialShd, fillColor.Alpha)
	}
	if fillColor.RadialShd != nil {
		return parseRadialShdColor(fillColor.RadialShd, fillColor.Alpha)
	}
	if strings.TrimSpace(fillColor.Value) != "" {
		return parseColorWithAlpha(fillColor.Value, fillColor.Alpha)
	}
	return nil
}

// parseStrokeColor 解析勾边颜色
// 入参: strokeColor 勾边颜色节点
// 返回: color.Color 颜色对象
func parseStrokeColor(strokeColor *StrokeColor) color.Color {
	if strokeColor == nil {
		return nil
	}
	if strokeColor.AxialShd != nil {
		return parseAxialShdColor(strokeColor.AxialShd, strokeColor.Alpha)
	}
	if strokeColor.RadialShd != nil {
		return parseRadialShdColor(strokeColor.RadialShd, strokeColor.Alpha)
	}
	if strings.TrimSpace(strokeColor.Value) != "" {
		return parseColorWithAlpha(strokeColor.Value, strokeColor.Alpha)
	}
	return nil
}

// parseStrokePaint 解析勾边画刷
// 入参: strokeColor 勾边颜色节点, x X坐标, y Y坐标, pageH 页面高度, originX 原点X坐标, originY 原点Y坐标
// 返回: any 勾边画刷
func parseStrokePaint(strokeColor *StrokeColor, x, y, pageH, originX, originY float64) any {
	if strokeColor == nil {
		return nil
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
	if strokeColor.RadialShd != nil {
		return parseRadialShdColor(strokeColor.RadialShd, strokeColor.Alpha)
	}
	if strings.TrimSpace(strokeColor.Value) != "" {
		return parseColorWithAlpha(strokeColor.Value, strokeColor.Alpha)
	}
	return nil
}

// textGlyph 绘制字形
type textGlyph struct {
	Text    string
	GlyphID int
}

// textGlyphTransform 字符到字形变换
type textGlyphTransform struct {
	CodeCount int
	Glyphs    []textGlyph
}

// textObjectGlyphTransforms 获取文本对象的字形变换
// 入参: fontID 字体ID, text 文本对象
// 返回: map[int]textGlyphTransform 文本位置到字形变换的映射
func (r *Renderer) textObjectGlyphTransforms(fontID string, text TextObject) map[int]textGlyphTransform {
	if fontID == "" || len(text.CGTransform) == 0 {
		return nil
	}
	result := make(map[int]textGlyphTransform)
	for _, transform := range text.CGTransform {
		ids := parseInts(transform.Glyphs)
		glyphCount := len(ids)
		if transform.GlyphCount > 0 && transform.GlyphCount < glyphCount {
			glyphCount = transform.GlyphCount
		}
		if glyphCount == 0 {
			continue
		}
		codeCount := transform.CodeCount
		if codeCount <= 0 {
			codeCount = 1
		}
		glyphs := make([]textGlyph, 0, glyphCount)
		for i := 0; i < glyphCount; i++ {
			glyphs = append(glyphs, r.textGlyphFromID(fontID, ids[i]))
		}
		result[transform.CodePosition] = textGlyphTransform{
			CodeCount: codeCount,
			Glyphs:    glyphs,
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// textCodeGlyphs 获取文本编码对应的绘制字形
// 入参: runes 文本字符, transforms 字形变换, codeOffset 文本编码偏移
// 返回: []textGlyph 绘制字形列表
func textCodeGlyphs(runes []rune, transforms map[int]textGlyphTransform, codeOffset int) []textGlyph {
	if len(transforms) == 0 {
		return textRuneGlyphs(runes)
	}
	glyphs := make([]textGlyph, 0, len(runes))
	for i := 0; i < len(runes); {
		if transform, ok := transforms[codeOffset+i]; ok && i+transform.CodeCount <= len(runes) {
			glyphs = append(glyphs, transform.Glyphs...)
			i += transform.CodeCount
			continue
		}
		glyphs = append(glyphs, textGlyph{Text: string(runes[i]), GlyphID: -1})
		i++
	}
	return glyphs
}

// textRuneGlyphs 转换文本字符为绘制字形
// 入参: runes 文本字符
// 返回: []textGlyph 绘制字形列表
func textRuneGlyphs(runes []rune) []textGlyph {
	glyphs := make([]textGlyph, 0, len(runes))
	for _, r := range runes {
		glyphs = append(glyphs, textGlyph{Text: string(r), GlyphID: -1})
	}
	return glyphs
}

// textGlyphFromID 获取字形ID对应的绘制字形
// 入参: fontID 字体ID, glyphID 字形ID或CID
// 返回: textGlyph 绘制字形
func (r *Renderer) textGlyphFromID(fontID string, glyphID int) textGlyph {
	if mapped, ok := r.fontGlyphRune(fontID, glyphID); ok {
		return textGlyph{Text: string(mapped), GlyphID: -1}
	}
	return textGlyph{GlyphID: glyphID}
}

// textGlyphWidth 获取绘制字形宽度
// 入参: face 字体, glyph 绘制字形
// 返回: float64 字形宽度
func textGlyphWidth(face *canvas.FontFace, glyph textGlyph) float64 {
	if glyph.GlyphID >= 0 && glyph.GlyphID <= 0xFFFF {
		return face.MmPerEm * float64(face.Font.GlyphAdvance(uint16(glyph.GlyphID)))
	}
	return face.TextWidth(glyph.Text)
}

// textGlyphPath 获取绘制字形路径
// 入参: face 字体, glyph 绘制字形
// 返回: *canvas.Path 字形路径, float64 字形宽度
func textGlyphPath(face *canvas.FontFace, glyph textGlyph) (*canvas.Path, float64) {
	if glyph.GlyphID < 0 || glyph.GlyphID > 0xFFFF {
		return face.ToPath(glyph.Text)
	}
	p := &canvas.Path{}
	glyphID := uint16(glyph.GlyphID)
	_ = face.Font.GlyphPath(p, glyphID, face.PPEM(canvas.DefaultResolution), 0, 0, face.MmPerEm, font.NoHinting)
	if face.FauxBold != 0 {
		d := face.FauxBold * face.Size
		if face.Font.IsTrueType {
			d = -d
		}
		origFastStroke := canvas.FastStroke
		canvas.FastStroke = true
		p = p.Offset(d, canvas.Tolerance)
		canvas.FastStroke = origFastStroke
	}
	if face.FauxItalic != 0 {
		p = p.Transform(canvas.Identity.Shear(face.FauxItalic, 0))
	}
	return p, face.MmPerEm * float64(face.Font.GlyphAdvance(glyphID))
}

// fontGlyphRune 获取字形ID对应的包装字体字符
// 入参: fontID 字体ID, glyphID 字形ID或CID
// 返回: rune 包装字体字符, bool 是否存在
func (r *Renderer) fontGlyphRune(fontID string, glyphID int) (rune, bool) {
	if glyphID < 0 || glyphID > 0xFFFF {
		return 0, false
	}
	id := uint16(glyphID)
	if r.FontCIDMap != nil {
		if mapping := r.FontCIDMap[fontID]; mapping != nil {
			if mapped, ok := mapping[id]; ok {
				return mapped, true
			}
		}
	}
	if r.FontGIDMap != nil {
		if mapping := r.FontGIDMap[fontID]; mapping != nil {
			if mapped, ok := mapping[id]; ok {
				return mapped, true
			}
		}
	}
	return 0, false
}

// textGlyphAdvanceLimit 获取显式字形推进宽度
// 入参: dxs X方向偏移, dys Y方向偏移, xs X坐标列表, index 字形索引, count 字形数量, currentX 当前X坐标
// 返回: float64 推进宽度
func textGlyphAdvanceLimit(dxs, dys, xs []float64, index int, count int, currentX float64) float64 {
	if index+1 >= count || len(dys) > 0 {
		return 0
	}
	if index+1 < len(xs) {
		if advance := xs[index+1] - currentX; advance > 0 {
			return advance
		}
	}
	if advance, ok := textDelta(dxs, index); ok && advance > 0 {
		return advance
	}
	return 0
}

// textCodePositioned 判断文本编码是否带显式定位
// 入参: textCode 文本编码
// 返回: bool 是否带显式定位
func textCodePositioned(textCode TextCode) bool {
	return strings.TrimSpace(textCode.DeltaX) != "" ||
		strings.TrimSpace(textCode.DeltaY) != "" ||
		len(parseFloats(textCode.X)) > 1 ||
		len(parseFloats(textCode.Y)) > 1
}

// textDelta 获取文本偏移量
// 入参: deltas 偏移量数组, index 偏移量索引
// 返回: float64 偏移量, bool 是否存在
func textDelta(deltas []float64, index int) (float64, bool) {
	if len(deltas) == 0 {
		return 0, false
	}
	if index < len(deltas) {
		return deltas[index], true
	}
	return deltas[len(deltas)-1], true
}

// textCodeRunes 获取文本编码字符
// 入参: value 文本编码内容
// 返回: []rune 文本字符
func textCodeRunes(value string) []rune {
	if strings.ContainsAny(value, "\r\n") {
		value = strings.TrimSpace(value)
	}
	return []rune(value)
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
