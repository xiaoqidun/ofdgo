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

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/font"
)

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

// hasTextMatrix 判断文本是否需要应用字形变换
// 入参: ctm 变换矩阵
// 返回: bool 是否需要变换
func hasTextMatrix(ctm Matrix) bool {
	const eps = 1e-9
	return math.Abs(ctm.a-1) > eps || math.Abs(ctm.b) > eps || math.Abs(ctm.c) > eps || math.Abs(ctm.d-1) > eps
}

// textMatrix 获取文本字形变换矩阵
// 入参: ctm OFD变换矩阵
// 返回: canvas.Matrix 画布变换矩阵
func textMatrix(ctm Matrix) canvas.Matrix {
	return canvas.Matrix{
		{ctm.a, -ctm.c, 0},
		{-ctm.b, ctm.d, 0},
	}
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

// parseIndexRunes 解析索引字形
// 入参: indexStr 索引字符串, fontID 字体ID
// 返回: []rune 字形列表
func (r *Renderer) parseIndexRunes(indexStr string, fontID string) []rune {
	var gids []int
	parts := strings.Fields(indexStr)
	for _, p := range parts {
		if strings.Contains(p, "-") {
			sub := strings.Split(p, "-")
			if len(sub) == 2 {
				start, _ := strconv.Atoi(sub[0])
				end, _ := strconv.Atoi(sub[1])
				for k := start; k <= end; k++ {
					gids = append(gids, k)
				}
			}
		} else {
			val, _ := strconv.Atoi(p)
			gids = append(gids, val)
		}
	}
	var res []rune
	for _, gid := range gids {
		if rVal, ok := r.fontGlyphRune(fontID, gid); ok {
			res = append(res, rVal)
			continue
		}
		res = append(res, rune(gid))
	}
	return res
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
	return []rune(value)
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
