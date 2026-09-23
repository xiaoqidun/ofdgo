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
	"github.com/tdewolff/canvas"
	canvastext "github.com/tdewolff/canvas/text"
	"github.com/tdewolff/font"
)

// textGlyphPathCacheKey 字形路径缓存键
type textGlyphPathCacheKey struct {
	font       *canvas.Font
	size       float64
	fauxBold   float64
	fauxItalic float64
	xOffset    int32
	yOffset    int32
	language   string
	script     canvastext.Script
	direction  canvastext.Direction
	glyph      textGlyph
}

// textGlyphPathCacheValue 字形路径缓存值
type textGlyphPathCacheValue struct {
	path  *canvas.Path
	width float64
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

// cachedTextGlyphPath 获取缓存的字形路径
// 入参: face 字体, glyph 绘制字形
// 返回: *canvas.Path 字形路径, float64 字形宽度
func (r *Renderer) cachedTextGlyphPath(face *canvas.FontFace, glyph textGlyph) (*canvas.Path, float64) {
	key := textGlyphPathCacheKey{
		font:       face.Font,
		size:       face.Size,
		fauxBold:   face.FauxBold,
		fauxItalic: face.FauxItalic,
		xOffset:    face.XOffset,
		yOffset:    face.YOffset,
		language:   face.Language,
		script:     face.Script,
		direction:  face.Direction,
		glyph:      glyph,
	}
	if cached, ok := r.textGlyphPathCache[key]; ok {
		return cached.path, cached.width
	}
	path, width := textGlyphPath(face, glyph)
	r.textGlyphPathCache[key] = textGlyphPathCacheValue{path: path, width: width}
	return path, width
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
