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
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Text 获取定位后的文字，横向换行以换行符分隔，字形索引以占位字符表示
// 返回: string 原文
func (obj TextObject) Text() string {
	var text strings.Builder
	for i, code := range obj.TextCode {
		if obj.textCodeLineBreak(i) {
			text.WriteByte('\n')
		}
		if code.Index == "" {
			text.WriteString(string(textCodeRunes(code.Value)))
		} else {
			text.WriteRune('\uFFFC')
		}
	}
	return text.String()
}

// textCodeLineBreak 判断横向文字的行首回退，或有足够行距且水平范围重叠的对齐段落
// 入参: index 文本编码索引
// 返回: bool 是否换行
func (obj TextObject) textCodeLineBreak(index int) bool {
	if index == 0 || obj.ReadDirection != 0 || obj.CharDirection != 0 {
		return false
	}
	code, previous := obj.TextCode[index], obj.TextCode[index-1]
	x, ex := strconv.ParseFloat(code.X, 64)
	start, es := strconv.ParseFloat(obj.TextCode[0].X, 64)
	y, ey := strconv.ParseFloat(code.Y, 64)
	prev, ep := strconv.ParseFloat(previous.Y, 64)
	if ex != nil || es != nil || ey != nil || ep != nil {
		return false
	}
	if x == start && y != prev {
		return true
	}
	left, err := strconv.ParseFloat(previous.X, 64)
	if err != nil || obj.Size <= 0 || y-prev < obj.Size/2 || previous.DeltaY != "" || code.DeltaY != "" {
		return false
	}
	right := left + obj.Size
	for _, dx := range previous.GetDeltaX() {
		right += dx
	}
	return x <= right
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

// textObjectFontID 获取文本对象字体ID
// 入参: text 文本对象
// 返回: string 字体ID
func (r *Renderer) textObjectFontID(text TextObject) string {
	fontID := text.Font
	if fontID == "" && text.DrawParam != "" {
		if dp := r.getDrawParam(text.DrawParam, nil); dp != nil && dp.Font != "" {
			fontID = dp.Font
		}
	}
	return fontID
}

// textObjectGlyphTransforms 获取文本对象的字形变换
// 入参: fontID 字体ID, text 文本对象
// 返回: map[int]textGlyphTransform 文本位置到字形变换的映射
func (r *Renderer) textObjectGlyphTransforms(fontID string, text TextObject) map[int]textGlyphTransform {
	if fontID == "" || len(text.CGTransform) == 0 {
		return nil
	}
	result := make(map[int]textGlyphTransform, len(text.CGTransform))
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
		glyphs := make([]textGlyph, glyphCount)
		for i := range glyphs {
			glyphs[i] = r.textGlyphFromID(fontID, ids[i])
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
	glyphs := make([]textGlyph, len(runes))
	for i, r := range runes {
		glyphs[i] = textGlyph{Text: string(r), GlyphID: -1}
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

// hasTextMatrix 判断文本是否需要应用字形变换
// 入参: ctm 变换矩阵
// 返回: bool 是否需要变换
func hasTextMatrix(ctm Matrix) bool {
	const eps = 1e-9
	return math.Abs(ctm.a-1) > eps || math.Abs(ctm.b) > eps || math.Abs(ctm.c) > eps || math.Abs(ctm.d-1) > eps
}

// fontGlyphRune 获取字形ID对应的包装字体字符
// 入参: fontID 字体ID, glyphID 字形ID或CID
// 返回: rune 包装字体字符, bool 是否存在
func (r *Renderer) fontGlyphRune(fontID string, glyphID int) (rune, bool) {
	if glyphID < 0 || glyphID > 0xFFFF {
		return 0, false
	}
	id := uint16(glyphID)
	if mapped, ok := r.fontCIDMap[fontID][id]; ok {
		return mapped, true
	}
	if mapped, ok := r.fontGIDMap[fontID][id]; ok {
		return mapped, true
	}
	return 0, false
}

// parseIndexRunes 解析索引字形
// 入参: indexStr 索引字符串, fontID 字体ID
// 返回: []rune 字形列表
func (r *Renderer) parseIndexRunes(indexStr string, fontID string) []rune {
	parts := strings.Fields(indexStr)
	result := make([]rune, 0, len(parts))
	for _, p := range parts {
		if startText, endText, ok := strings.Cut(p, "-"); ok {
			if !strings.Contains(endText, "-") {
				start, _ := strconv.Atoi(startText)
				end, _ := strconv.Atoi(endText)
				for k := start; k <= end; k++ {
					result = append(result, r.textIndexRune(fontID, k))
				}
			}
		} else {
			val, _ := strconv.Atoi(p)
			result = append(result, r.textIndexRune(fontID, val))
		}
	}
	return result
}

// textIndexRune 获取索引字形对应的字符
// 入参: fontID 字体ID, glyphID 字形ID或CID
// 返回: rune 字符
func (r *Renderer) textIndexRune(fontID string, glyphID int) rune {
	if mapped, ok := r.fontGlyphRune(fontID, glyphID); ok {
		return mapped
	}
	return rune(glyphID)
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
// 入参: textCode 文本编码, xs X坐标列表, ys Y坐标列表
// 返回: bool 是否带显式定位
func textCodePositioned(textCode TextCode, xs, ys []float64) bool {
	return strings.TrimSpace(textCode.DeltaX) != "" ||
		strings.TrimSpace(textCode.DeltaY) != "" ||
		len(xs) > 1 ||
		len(ys) > 1
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
	runes := []rune(value)
	if !strings.Contains(value, "\\") {
		return runes
	}
	written := 0
	for i := 0; i < len(runes); i++ {
		char := runes[i]
		if char == '\\' && i+4 < len(runes) {
			if code, err := strconv.ParseUint(string(runes[i+1:i+5]), 16, 16); err == nil {
				decoded := rune(code)
				if !utf16.IsSurrogate(decoded) {
					char = decoded
					i += 4
				} else if decoded >= 0xd800 && decoded <= 0xdbff && i+9 < len(runes) && runes[i+5] == '\\' {
					if low, err := strconv.ParseUint(string(runes[i+6:i+10]), 16, 16); err == nil && low >= 0xdc00 && low <= 0xdfff {
						char = utf16.DecodeRune(decoded, rune(low))
						i += 9
					}
				}
			}
		}
		runes[written] = char
		written++
	}
	return runes[:written]
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
