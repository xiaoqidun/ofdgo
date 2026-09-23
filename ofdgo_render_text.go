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
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
)

// PositionedGlyph 保存页面坐标字形与对应原文区间
type PositionedGlyph struct {
	Path       GeometryPath
	Underline  GeometryPath
	Start, End int
}

// PositionedText 保存绘制、搜索和度量共同使用的定位结果
// Glyphs中的路径和Clip为只读页面坐标，Run保留原文而非包装字符
type PositionedText struct {
	Glyphs []PositionedGlyph
	Run    TextRun
	Clip   *GeometryPath
	Matrix Matrix
}

// glyphOutlineKey 区分字体、字形与毫米字号
type glyphOutlineKey struct {
	font  *PreparedFont
	glyph uint16
	size  float64
}

// preparedMetrics 复用已包装字体的后端度量，不混用不同后端实例
// 入参: prepared 已包装字体
// 返回: FontMetrics 字体度量, error 字体解析错误
func (r *Renderer) preparedMetrics(prepared *PreparedFont) (FontMetrics, error) {
	if prepared.digest == ([32]byte{}) {
		prepared.digest = sha256.Sum256(prepared.Data)
	}
	key := prepared.digest
	if metrics, ok := r.fontMetrics.get(key); ok {
		return metrics, nil
	}
	metrics, err := r.backends.Fonts.OpenFont(prepared.Data)
	if err != nil {
		return nil, err
	}
	r.fontMetrics.put(key, metrics, len(prepared.Data)*2+256)
	return metrics, nil
}

// preparedOutline 按字形和字号复用只读轮廓
// 入参: prepared 字体, metrics 度量, glyph 字形编号, size 毫米字号
// 返回: GeometryPath 字形路径, error 能力或解析错误
func (r *Renderer) preparedOutline(prepared *PreparedFont, metrics FontMetrics, glyph uint16, size float64) (GeometryPath, error) {
	key := glyphOutlineKey{prepared, glyph, size}
	if path, ok := r.glyphOutlines.get(key); ok {
		return path, nil
	}
	provider, ok := metrics.(FontOutlines)
	if !ok {
		return nil, fmt.Errorf("font outlines: %w", ErrBackendUnavailable)
	}
	path, err := provider.GlyphOutline(glyph, size)
	if err == nil {
		r.glyphOutlines.put(key, path, len(path)*96+128)
	}
	return path, err
}

// preparedOutlines 批量提取缓存未命中的字形，保留原始编号顺序，不触发塑形
// 入参: prepared 字体, metrics 度量, glyphs 字形编号, size 毫米字号
// 返回: []GeometryPath 同序只读轮廓, error 能力或解析错误
func (r *Renderer) preparedOutlines(prepared *PreparedFont, metrics FontMetrics, glyphs []uint16, size float64) ([]GeometryPath, error) {
	result := make([]GeometryPath, len(glyphs))
	var indices []int
	missing := make([]uint16, 0)
	unique := make(map[uint16]int)
	for i, glyph := range glyphs {
		if path, ok := r.glyphOutlines.get(glyphOutlineKey{prepared, glyph, size}); ok {
			result[i] = path
			continue
		}
		if indices == nil {
			indices = make([]int, len(glyphs))
			for j := range indices {
				indices[j] = -1
			}
		}
		index, ok := unique[glyph]
		if !ok {
			index = len(missing)
			unique[glyph] = index
			missing = append(missing, glyph)
		}
		indices[i] = index
	}
	if len(missing) == 0 {
		return result, nil
	}
	var paths []GeometryPath
	if batch, ok := metrics.(FontOutlineBatch); ok {
		var err error
		paths, err = batch.GlyphOutlines(missing, size)
		if err != nil {
			return nil, err
		}
		if len(paths) != len(missing) {
			return nil, fmt.Errorf("font outline batch returned %d paths for %d glyphs", len(paths), len(missing))
		}
	} else {
		provider, ok := metrics.(FontOutlines)
		if !ok {
			return nil, fmt.Errorf("font outlines: %w", ErrBackendUnavailable)
		}
		paths = make([]GeometryPath, len(missing))
		for i, glyph := range missing {
			var err error
			paths[i], err = provider.GlyphOutline(glyph, size)
			if err != nil {
				return nil, err
			}
		}
	}
	for i, path := range paths {
		r.glyphOutlines.put(glyphOutlineKey{prepared, missing[i], size}, path, len(path)*96+128)
	}
	for i, index := range indices {
		if index >= 0 {
			result[i] = paths[index]
		}
	}
	return result, nil
}

// renderObjectMatrix 统一边界、局部变换与父变换的组合顺序
// 入参: boundary 对象边界, local 局部矩阵, state 继承状态
// 返回: Matrix 页面矩阵, Matrix 不含边界平移的组合矩阵
func renderObjectMatrix(boundary string, local Matrix, state RenderState) (Matrix, Matrix) {
	box, _ := ParseBox(boundary)
	linear := local
	if state.Parent != nil {
		linear = state.Parent.Multiply(local)
	}
	matrix := TranslationMatrix(box.X, box.Y).Multiply(linear)
	if state.BoundaryInCTM && state.Parent != nil {
		matrix = state.Parent.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(local)
	}
	return matrix, linear
}

// objectGeometryClip 解析对象裁剪并合并父裁剪
// 入参: clips 对象裁剪, boundary 边界, local 局部矩阵, state 继承状态
// 返回: *GeometryPath 页面裁剪, error 几何错误
func (r *Renderer) objectGeometryClip(clips *Clips, boundary string, local Matrix, state RenderState) (*GeometryPath, error) {
	if clips == nil {
		return state.Clip, nil
	}
	matrix, _ := renderObjectMatrix(boundary, local, state)
	if clips.TransFlag != nil && !*clips.TransFlag {
		copy := *clips
		copy.TransFlag = nil
		clips = &copy
		box, _ := ParseBox(boundary)
		matrix = TranslationMatrix(box.X, box.Y)
		if state.BoundaryInCTM && state.Parent != nil {
			matrix = state.Parent.Multiply(matrix)
		}
	}
	geometry, err := r.Geometry()
	if err != nil {
		return nil, err
	}
	return geometry.Clip(r, clips, matrix, state.Clip)
}

// clipGeometry 将非零填充路径与页面裁剪相交
// 入参: geometry 几何后端, path 页面路径, clip 页面裁剪
// 返回: GeometryPath 裁剪路径, error 几何错误
func clipGeometry(geometry GeometryBackend, path GeometryPath, clip *GeometryPath) (GeometryPath, error) {
	if clip == nil {
		return path, nil
	}
	return geometry.Combine(path, *clip, GeometryIntersect)
}

// PositionText 定位文字、字形与搜索范围，不依赖页面输出后端
// 入参: object 文字对象, state 继承状态
// 返回: *PositionedText 定位结果, error 字体或几何错误
func (r *Renderer) PositionText(object TextObject, state RenderState) (*PositionedText, error) {
	result := &PositionedText{Run: TextRun{ID: object.ID, Text: object.Text()}}
	result.Run.Boxes = make([]Box, len([]rune(result.Run.Text)))
	if object.Visible != nil && !*object.Visible {
		return result, nil
	}
	geometry, err := r.Geometry()
	if err != nil {
		return nil, err
	}
	local := NewMatrix(object.CTM)
	matrix, linear := renderObjectMatrix(object.Boundary, local, state)
	result.Matrix = matrix
	result.Clip, err = r.objectGeometryClip(object.Clips, object.Boundary, local, state)
	if err != nil {
		return nil, err
	}
	defaults := r.drawParamDefaults(object.DrawParam, state.Defaults)
	size := object.Size
	if size == 0 && defaults != nil {
		size = defaults.Size
	}
	if size == 0 {
		size = 3.5
	}
	if object.VScale != 0 {
		size *= object.VScale
	}
	horizontal := object.HScale
	if horizontal == 0 {
		horizontal = 1
	}
	id := object.Font
	if id == "" && defaults != nil {
		id = defaults.Font
	}
	prepared, err := r.PrepareFont(id)
	if err != nil {
		return nil, err
	}
	if len(prepared.Data) == 0 {
		box, _ := ParseBox(object.Boundary)
		boundary, _ := renderObjectMatrix(object.Boundary, IdentityMatrix, state)
		path, err := geometry.Transform(geometryRectangle(Box{W: box.W, H: box.H}), boundary)
		if err != nil {
			return nil, err
		}
		path, err = clipGeometry(geometry, path, result.Clip)
		if err != nil {
			return nil, err
		}
		box, err = geometry.Bounds(path)
		if err != nil {
			return nil, err
		}
		for i := range result.Run.Boxes {
			result.Run.Boxes[i] = box
		}
		return result, nil
	}
	metrics, err := r.preparedMetrics(prepared)
	if err != nil {
		return nil, err
	}
	weight, italic := object.Weight, object.Italic
	if defaults != nil {
		if weight == 0 {
			weight = defaults.Weight
		}
		italic = italic || defaults.Italic
	}
	bold := weight >= 700
	if definition := r.Reader.fontCache[id]; definition != nil {
		bold = bold && !definition.Bold
		if definition.FontFile == "" && fontNoSyntheticBold(definition.FontName, definition.FamilyName) {
			bold = false
		}
		italic = italic && !definition.Italic
	}
	ascent, descent, _ := metrics.VerticalMetrics()
	unit := size / float64(metrics.UnitsPerEm())
	transforms := r.textObjectGlyphTransforms(id, object)
	codeOffset, textOffset := 0, 0
	angle := float64(object.CharDirection) * math.Pi / 180
	rotate := Matrix{a: math.Cos(angle), b: math.Sin(angle), c: -math.Sin(angle), d: math.Cos(angle)}
	glyphLinear := Matrix{a: linear.a, b: linear.b, c: linear.c, d: linear.d}.Multiply(rotate)
	for index, code := range object.TextCode {
		if object.textCodeLineBreak(index) {
			textOffset++
		}
		runes := textCodeRunes(code.Value)
		glyphs := textCodeGlyphs(runes, transforms, codeOffset)
		spans := textGlyphSpans(runes, transforms, codeOffset)
		if code.Index != "" {
			runes = r.parseIndexRunes(code.Index, id)
			glyphs = textRuneGlyphs(runes)
			spans = make([][2]int, len(glyphs))
			for i := range spans {
				spans[i] = [2]int{0, 1}
			}
		}
		ids := make([]uint16, len(glyphs))
		for i, glyph := range glyphs {
			if glyph.GlyphID > math.MaxUint16 || glyph.GlyphID < 0 && glyph.Text == "" {
				return nil, fmt.Errorf("invalid glyph index %d", glyph.GlyphID)
			}
			gid := uint16(glyph.GlyphID)
			if glyph.GlyphID < 0 {
				characters := []rune(glyph.Text)
				gid = metrics.GlyphIndex(characters[0])
			}
			ids[i] = gid
		}
		paths, err := r.preparedOutlines(prepared, metrics, ids, size)
		if err != nil {
			return nil, err
		}
		positioner := NewTextPositioner(code, object.ReadDirection)
		for i, gid := range ids {
			path := paths[i]
			width := float64(metrics.GlyphAdvance(gid)) * unit
			advance := width * horizontal
			if (object.ReadDirection-object.CharDirection)%180 != 0 {
				advance = size
			}
			origin := positioner.Next(advance)
			scaleX := horizontal
			if object.CharDirection == 0 {
				limit := textGlyphAdvanceLimit(positioner.dxs, positioner.dys, positioner.xs, i, len(glyphs), origin.X)
				if limit > 0 && width*scaleX > limit {
					scaleX = limit / width
				}
			}
			if bold && len(path) > 0 {
				outline, err := geometry.Stroke(path, StrokeOptions{Width: size * 0.04, Join: "Round", Cap: "Round"})
				if err != nil {
					return nil, err
				}
				path, err = geometry.Combine(path, outline, GeometryUnion)
				if err != nil {
					return nil, err
				}
			}
			if italic {
				path, err = geometry.Transform(path, Matrix{a: 1, c: -0.3, d: 1})
				if err != nil {
					return nil, err
				}
			}
			x, y := matrix.Transform(origin.X, origin.Y)
			transform := TranslationMatrix(x, y).Multiply(glyphLinear).Multiply(Matrix{a: scaleX, d: 1})
			bounds := Box{Y: -float64(ascent) * unit, W: width, H: (float64(ascent) + float64(descent)) * unit}
			if len(path) > 0 {
				shape, err := geometry.Bounds(path)
				if err != nil {
					return nil, err
				}
				bounds = unionTextBox(bounds, shape)
			}
			placed, err := geometry.Transform(path, transform)
			if err != nil {
				return nil, err
			}
			clipped, err := clipGeometry(geometry, placed, result.Clip)
			if err != nil {
				return nil, err
			}
			box, err := geometry.Bounds(clipped)
			if err != nil {
				return nil, err
			}
			span := spans[i]
			item := PositionedGlyph{Path: placed, Start: textOffset + span[0], End: textOffset + span[1]}
			if strings.Contains(object.Decoration, "Underline") {
				line := GeometryPath{{Verb: GeometryMove, End: Point{Y: size * 0.1}}, {Verb: GeometryLine, End: Point{X: width * scaleX, Y: size * 0.1}}}
				line, err = geometry.Stroke(line, StrokeOptions{Width: size * 0.05})
				if err != nil {
					return nil, err
				}
				item.Underline, err = geometry.Transform(line, TranslationMatrix(x, y).Multiply(glyphLinear))
				if err != nil {
					return nil, err
				}
			}
			for j := item.Start; j < item.End; j++ {
				result.Run.Boxes[j] = unionTextBox(result.Run.Boxes[j], box)
			}
			if err := result.Run.addGeometrySpan(geometry, item.Start, item.End, bounds, transform, result.Clip); err != nil {
				return nil, err
			}
			result.Glyphs = append(result.Glyphs, item)
		}
		codeOffset += len(runes)
		if code.Index == "" {
			textOffset += len(runes)
		} else {
			textOffset++
		}
	}
	return result, nil
}

// addGeometrySpan 以公共几何语义收集文字选择区域，保留旋转和斜切
// 入参: geometry 几何后端, start 起始字符, end 结束字符, bounds 局部范围, matrix 页面变换, clip 页面裁剪
// 返回: error 几何错误
func (run *TextRun) addGeometrySpan(geometry GeometryBackend, start, end int, bounds Box, matrix Matrix, clip *GeometryPath) error {
	inverse, ok := matrix.Invert()
	if !ok {
		return nil
	}
	if clip != nil {
		path, err := geometry.Transform(geometryRectangle(bounds), matrix)
		if err != nil {
			return err
		}
		path, err = clipGeometry(geometry, path, clip)
		if err != nil {
			return err
		}
		if len(path) == 0 {
			return nil
		}
		path, err = geometry.Transform(path, inverse)
		if err != nil {
			return err
		}
		bounds, err = geometry.Bounds(path)
		if err != nil {
			return err
		}
	}
	if bounds.W < 0 || bounds.H <= 0 {
		return nil
	}
	matrix = matrix.Multiply(TranslationMatrix(bounds.X, bounds.Y)).Multiply(Matrix{a: bounds.W, d: bounds.H})
	if n := len(run.Spans); n > 0 && run.Spans[n-1].Start == start && run.Spans[n-1].End == end {
		previous := MatrixFromValues(run.Spans[n-1].Matrix)
		if inverse, ok := previous.Invert(); ok {
			box := unionTextBox(Box{W: 1, H: 1}, inverse.Multiply(matrix).TransformBox(Box{W: 1, H: 1}))
			matrix = previous.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(Matrix{a: box.W, d: box.H})
		}
		run.Spans = run.Spans[:n-1]
	}
	run.Spans = append(run.Spans, TextSpan{Start: start, End: end, Matrix: matrix.Values()})
	return nil
}
