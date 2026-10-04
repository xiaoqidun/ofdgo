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
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

// pdfImageKey 区分同一图像流在不同渲染意图下的资源
type pdfImageKey struct {
	stream *pdfgo.Stream
	intent pdfgo.Name
}

// pdfImageResource 保存图像的有效颜色空间及对应资源，避免跨资源重映射复用
type pdfImageResource struct {
	space pdfgo.Object
	id    string
}

// pdfStencilImage 按当前填充色读取模板图像，不分配整幅彩色样本
type pdfStencilImage struct {
	source image.Image
	fill   color.NRGBA64
}

// ColorModel 保留填充色和模板的有效采样精度
// 返回: color.Model 非预乘颜色模型
func (s *pdfStencilImage) ColorModel() color.Model {
	model := s.source.ColorModel()
	if s.fill.R%257 == 0 && s.fill.G%257 == 0 && s.fill.B%257 == 0 && (model == color.GrayModel || model == color.NRGBAModel || model == color.RGBAModel) {
		return color.NRGBAModel
	}
	return color.NRGBA64Model
}

// Bounds 返回模板图像的采样边界
// 返回: image.Rectangle 图像边界
func (s *pdfStencilImage) Bounds() image.Rectangle { return s.source.Bounds() }

// At 以模板灰度计算填充覆盖率，不重复变换颜色
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 非预乘颜色
func (s *pdfStencilImage) At(x, y int) color.Color {
	return s.NRGBA64At(x, y)
}

// NRGBA64At 直接读取模板覆盖率，保留填充色的有效精度
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.NRGBA64 非预乘颜色
func (s *pdfStencilImage) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.Bounds()) {
		return color.NRGBA64{}
	}
	gray := imageNRGBA64At(s.source, x, y)
	pixel := s.fill
	pixel.A = uint16(65535 - gray.R)
	return pixel
}

// image 转换图像样本，不将整页或其他对象栅格化
// 入参: mark PDF图像绘制信息
// 返回: error 错误信息
func (p *pdfImporter) image(mark pdfgo.ImageMark) error {
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "image blend mode " + string(mark.Style.BlendMode)}
	}
	if err := p.flushPath(); err != nil {
		return err
	}
	if mark.Image.ImageMask && (mark.Style.Fill.Axial != nil || mark.Style.Fill.Radial != nil || mark.Style.Fill.Function != nil || mark.Style.Fill.Mesh != nil) {
		if p.warning != nil {
			return p.compositeRegion(nil, pdfCompositeNode{image: &mark}, &pdfgo.ColorSpace{Model: "DeviceRGB"}, false)
		}
		return &pdfgo.UnsupportedError{Feature: "gradient stencil image"}
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Image(mark) })
		}
		return err
	}
	if mark.Style.FillOverprint {
		return &pdfgo.UnsupportedError{Feature: "image color separation overprint"}
	}
	source := mark.Image
	key := pdfImageKey{stream: source.Stream, intent: source.Intent}
	id := ""
	if !source.ImageMask {
		for _, resource := range p.imageIDs[key] {
			if reflect.DeepEqual(resource.space, source.ColorSpace) {
				id = resource.id
				break
			}
		}
	}
	if id == "" {
		jbig2Original, err := source.JBIG2FileContext(p.ctx)
		if err != nil {
			return err
		}
		jpegOriginal, err := source.JPEGFileContext(p.ctx)
		if err != nil {
			return err
		}
		var decoded image.Image
		if len(jbig2Original) == 0 {
			decoded, err = source.DecodeImageContext(p.ctx)
			if err != nil {
				return err
			}
		}
		if source.ImageMask {
			fill := mark.Style.Fill.RGB
			if values := mark.Style.Fill.CMYK; values != nil {
				space := &pdfgo.ColorSpace{Model: "DeviceCMYK"}
				fill, err = space.RGB(values[:], mark.Style.RenderingIntent)
				if err != nil {
					return err
				}
			}
			tint := color.NRGBA64{
				R: uint16(math.Round(fill[0] * 65535)),
				G: uint16(math.Round(fill[1] * 65535)),
				B: uint16(math.Round(fill[2] * 65535)),
			}
			decoded = &pdfStencilImage{source: decoded, fill: tint}
		}
		var encoded bytes.Buffer
		if len(jbig2Original) != 0 {
			encoded.Write(jbig2Original)
		} else if len(jpegOriginal) != 0 {
			encoded.Write(jpegOriginal)
		} else {
			if err := pdfgo.EncodePNG(p.ctx, &encoded, decoded); err != nil {
				return err
			}
		}
		id, err = p.editor.AddImage(encoded.Bytes())
		if err != nil {
			return err
		}
		if !source.ImageMask {
			if p.imageIDs == nil {
				p.imageIDs = map[pdfImageKey][]pdfImageResource{}
			}
			p.imageIDs[key] = append(p.imageIDs[key], pdfImageResource{space: source.ColorSpace, id: id})
		}
	}
	return p.appendImage(mark, id)
}

// appendImage 定位已注册的图像资源，保留透明度与裁剪
// 入参: mark 图像绘制信息, id 资源编号
// 返回: error 裁剪错误
func (p *pdfImporter) appendImage(mark pdfgo.ImageMark, id string) error {
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1, 0, 0, -1, 0, 1})
	box := pdfBounds([]pdfgo.Point{m.Apply(pdfgo.Point{}), m.Apply(pdfgo.Point{X: 1}), m.Apply(pdfgo.Point{Y: 1}), m.Apply(pdfgo.Point{X: 1, Y: 1})})
	alpha := int(math.Round(mark.Style.Fill.Alpha * 255))
	clips, err := p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	object := ImageObject{Boundary: pdfBoundary(box), ResourceID: id, CTM: pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y), Alpha: &alpha, Clips: clips}
	p.objects = append(p.objects, GraphicObject{Type: "ImageObject", ImageObject: object})
	p.report.ImageObjects++
	return nil
}

// text 保留字形及逐字基线，外部字体优先复用同名内嵌资源
// 入参: mark PDF文字绘制信息
// 返回: error 错误信息
func (p *pdfImporter) text(mark pdfgo.TextMark) error {
	if mark.Font.Subtype == pdfgo.Name("Type3") && mark.Mode == 3 {
		return nil
	}
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "text blend mode " + string(mark.Style.BlendMode)}
	}
	if mark.Font.Subtype == pdfgo.Name("Type3") {
		if err := p.flushPath(); err != nil {
			return err
		}
		visitor := pdfgo.Visitor{Path: p.path, Image: p.image, Warning: p.warning, Reference: p.referencePage}
		visitor.Group = func(group pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
			return p.group(group, walk, visitor)
		}
		for index := range mark.Glyphs {
			if err := p.reader.WalkType3Glyph(p.ctx, mark, index, visitor); err != nil {
				return err
			}
		}
		return p.flushPath()
	}
	if mark.Size == 0 || mark.HorizontalScale == 0 {
		return p.degenerateText(mark)
	}
	if mark.Size < 0 || mark.HorizontalScale < 0 {
		x, y := math.Copysign(1, mark.Size*mark.HorizontalScale), math.Copysign(1, mark.Size)
		mark.Matrix = mark.Matrix.Mul(pdfgo.Matrix{x, 0, 0, y, 0, 0})
		mark.Size, mark.HorizontalScale = math.Abs(mark.Size), math.Abs(mark.HorizontalScale)
		positions := make([]pdfgo.Point, len(mark.Positions))
		for index, position := range mark.Positions {
			positions[index] = pdfgo.Point{X: position.X * x, Y: position.Y * y}
		}
		mark.Positions = positions
	}
	if err := p.flushPath(); err != nil {
		return err
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if mark.Clip == nil && p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Text(mark) })
		}
		return err
	}
	paintMode := mark.Mode % 4
	if (paintMode == 0 || paintMode == 2) && mark.Style.FillOverprint && pdfOverprintNeedsSeparation(mark.Style.Fill) || (paintMode == 1 || paintMode == 2) && mark.Style.StrokeOverprint && pdfOverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "text color separation overprint"}
	}
	if p.warning != nil && ((paintMode == 0 || paintMode == 2) && pdfGradientError(mark.Style.Fill) != nil || (paintMode == 1 || paintMode == 2) && pdfGradientError(mark.Style.Stroke) != nil) {
		return p.compositeRegion(nil, pdfCompositeNode{text: &mark}, &pdfgo.ColorSpace{Model: "DeviceRGB"}, false)
	}
	font := mark.Font
	embedded := len(font.Program) != 0
	if embedded && font.ProgramType != "FontFile" && font.ProgramType != "FontFile2" && font.ProgramType != "OpenType" && font.ProgramType != "Type1C" && font.ProgramType != "CIDFontType0C" {
		return &pdfgo.UnsupportedError{Feature: "font program " + string(font.ProgramType)}
	}
	var imported *pdfImportedFont
	reused := false
	if embedded {
		imported, err = p.importedFont(font)
		if err != nil {
			return err
		}
	} else if imported = p.embeddedTextFont(font, mark.Glyphs); imported != nil {
		reused, embedded = true, true
		mark.Glyphs = slices.Clone(mark.Glyphs)
		for index := range mark.Glyphs {
			char, _ := utf8.DecodeRuneInString(mark.Glyphs[index].Text)
			mark.Glyphs[index].ID = imported.metrics.GlyphIndex(char)
			mark.Glyphs[index].HasID = true
		}
	}
	id := p.fontIDs[font]
	if reused {
		id, err = p.editor.addFontResource(imported.resource, imported.checksum)
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.editor.fontMetrics[id] = imported.metrics
	} else if id == "" {
		var err error
		if embedded {
			if imported.repairLimit != 0 {
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded font lacks trailing side bearings; unused metrics reconstructed from glyph bounds"})
			}
			id, err = p.editor.addFontResource(imported.resource, imported.checksum)
			if err == nil {
				p.editor.fontMetrics[id] = imported.metrics
			}
		} else {
			id, err = p.editor.AddExternalFont(font.Name)
		}
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.fontIDs[font] = id
	}
	if embedded && imported.repairLimit != 0 {
		for _, glyph := range mark.Glyphs {
			if glyph.HasID && glyph.ID >= imported.repairLimit {
				return &pdfgo.UnsupportedError{Feature: "embedded font glyph with missing side bearing"}
			}
		}
	}
	var metrics FontMetrics
	var outline FontOutlines
	if embedded {
		metrics = imported.metrics
		outline = metrics.(FontOutlines)
	}
	fill, stroke, visible := paintMode == 0 || paintMode == 2, paintMode == 1 || paintMode == 2, paintMode != 3
	if stroke && !embedded {
		if resolved, err := p.editor.editorFont(id); err == nil {
			if provider, ok := resolved.(FontOutlines); ok {
				metrics, outline = resolved, provider
				for _, glyph := range mark.Glyphs {
					char, _ := utf8.DecodeRuneInString(glyph.Text)
					if utf8.RuneCountInString(glyph.Text) != 1 || resolved.GlyphIndex(char) == 0 && char != 0 {
						metrics, outline = nil, nil
						break
					}
				}
			}
		}
	}
	outlineStroke := stroke && outline != nil
	var glyphPaths []pdfgo.Path
	const unit = 25.4 / 72
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, 0, 0})
	points := make([]pdfgo.Point, 0, 4*len(mark.Glyphs))
	object := TextObject{Font: id, Size: mark.Size * unit, HScale: mark.HorizontalScale, TextCode: make([]TextCode, 0, len(mark.Glyphs))}
	if embedded {
		object.CGTransform = make([]CGTransform, 0, len(mark.Glyphs))
	}
	position := 0
	for n, glyph := range mark.Glyphs {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		gid := glyph.ID
		if embedded && !reused && font.ProgramType == "FontFile" {
			gid = imported.type1Glyphs[glyph.Name]
			glyph.HasID = true
		}
		if glyph.Text == "" && glyph.HasID {
			character := getUnicodeFromName(glyph.Name)
			if character == 0 {
				character = packedGlyphRune(gid)
			}
			glyph.Text = string(character)
		}
		if embedded && !glyph.HasID {
			if utf8.RuneCountInString(glyph.Text) != 1 {
				return &pdfgo.UnsupportedError{Feature: "simple font glyph ligature mapping"}
			}
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			gid = metrics.GlyphIndex(char)
			if gid == 0 && char != 0 {
				return fmt.Errorf("missing PDF glyph for %U", char)
			}
		}
		if embedded && gid >= metrics.NumGlyphs() {
			return fmt.Errorf("PDF glyph index %d outside embedded font %s (%d glyphs)", gid, font.Name, metrics.NumGlyphs())
		}
		if !embedded && glyph.Text == "" {
			return &pdfgo.UnsupportedError{Feature: "unmapped external font character"}
		}
		if outlineStroke && !embedded {
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			gid = metrics.GlyphIndex(char)
		}
		origin := mark.Positions[n]
		object.TextCode = append(object.TextCode, TextCode{X: pdfNumbers(origin.X * unit), Y: pdfNumbers(-origin.Y * unit), Value: glyph.Text})
		if embedded {
			count := utf8.RuneCountInString(glyph.Text)
			object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: position, CodeCount: count, GlyphCount: 1, Glyphs: strconv.Itoa(int(gid))})
			position += count
		}
		if embedded || outlineStroke {
			var path GeometryPath
			var bounds Box
			var err error
			if provider, ok := outline.(FontGlyphBounds); ok && !outlineStroke {
				bounds, err = provider.GlyphBounds(gid, object.Size)
			} else {
				path, err = outline.GlyphOutline(gid, object.Size)
				if err == nil {
					bounds, err = path.Bounds()
				}
			}
			if err != nil {
				return fmt.Errorf("PDF font %s outline: %w", font.Name, err)
			}
			if diagnostics, ok := outline.(FontGlyphDiagnostics); ok {
				if err := diagnostics.GlyphWarning(gid); err != nil {
					if p.warning == nil {
						return fmt.Errorf("PDF font %s glyph %d hinting: %w", font.Name, gid, err)
					}
					if p.fontWarnings == nil {
						p.fontWarnings = make(map[string]bool)
					}
					if !p.fontWarnings[id] {
						p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s has invalid hinting instructions; design outlines and original font retained", font.Name)})
						p.fontWarnings[id] = true
					}
				}
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				points = append(points, m.Apply(pdfgo.Point{X: origin.X*unit + point.X*mark.HorizontalScale, Y: -origin.Y*unit + point.Y}))
			}
			if outlineStroke {
				matrix := mark.Matrix.Mul(pdfgo.Matrix{mark.HorizontalScale / unit, 0, 0, -1 / unit, origin.X, origin.Y})
				glyphPath, err := pdfGlyphPath(path, matrix)
				if err != nil {
					return err
				}
				glyphPaths = append(glyphPaths, glyphPath)
			}
		} else {
			width := glyph.Width / 1000 * object.Size * mark.HorizontalScale
			for _, point := range []pdfgo.Point{{X: 0, Y: -object.Size}, {X: width, Y: -object.Size}, {X: 0, Y: object.Size / 4}, {X: width, Y: object.Size / 4}} {
				points = append(points, m.Apply(pdfgo.Point{X: origin.X*unit + point.X, Y: -origin.Y*unit + point.Y}))
			}
		}
	}
	box := pdfBounds(points)
	if stroke && !outlineStroke {
		strokeMatrix := mark.StrokeMatrix
		if strokeMatrix == (pdfgo.Matrix{}) {
			strokeMatrix = pdfgo.Identity()
		}
		sx, sy := math.Hypot(strokeMatrix[0], strokeMatrix[1]), math.Hypot(strokeMatrix[2], strokeMatrix[3])
		if math.Abs(sx-sy) > 1e-8*math.Max(1, sx) || math.Abs(strokeMatrix[0]*strokeMatrix[2]+strokeMatrix[1]*strokeMatrix[3]) > 1e-8*math.Max(1, sx*sy) {
			return &pdfgo.UnsupportedError{Feature: "anisotropic text stroke"}
		}
		scale := math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
		if scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			return &pdfgo.UnsupportedError{Feature: "degenerate text stroke matrix"}
		}
		lineWidth := mark.Style.LineWidth * math.Hypot(p.matrix[0], p.matrix[1]) * sx
		margin := lineWidth / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{box.X - margin, box.Y - margin, box.W + 2*margin, box.H + 2*margin}
		object.LineWidth = lineWidth / scale
		object.LineWidthSet = object.LineWidth == 0
		object.Join = []string{"Miter", "Round", "Bevel"}[mark.Style.Join]
		object.MiterLimit = mark.Style.MiterLimit
		if len(mark.Style.Dash) > 0 {
			return &pdfgo.UnsupportedError{Feature: "dashed text stroke"}
		}
	}
	object.Boundary = pdfBoundary(box)
	if stroke && !outlineStroke {
		strokePaint, err := p.paintColor(mark.Style.Stroke, box)
		if err != nil {
			return err
		}
		color := StrokeColor(*strokePaint)
		object.StrokeColor = &color
	}
	if box.X >= p.pageWidth || box.Y >= p.pageHeight || box.X+box.W <= 0 || box.Y+box.H <= 0 {
		visible = false
	}
	object.CTM = pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y)
	object.Fill = &fill
	object.Stroke = &stroke
	object.Visible = &visible
	object.FillColor = p.color(mark.Style.Fill)
	if fill && !outlineStroke {
		object.FillColor, err = p.paintColor(mark.Style.Fill, box)
		if err != nil {
			return err
		}
	}
	object.Clips, err = p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	if mark.Clip != nil {
		clipObject := object
		clipObject.Clips = nil
		clipObject.Fill, clipObject.Stroke = new(bool), new(bool)
		*clipObject.Fill = true
		*clipObject.Stroke = false
		clipObject.Visible = nil
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		}
		p.clipTexts[mark.Clip] = []TextObject{clipObject}
	}
	if outlineStroke {
		for _, path := range glyphPaths {
			if len(path.Segments) == 0 {
				continue
			}
			if err := p.ctx.Err(); err != nil {
				return err
			}
			matrix := mark.StrokeMatrix
			if matrix == (pdfgo.Matrix{}) {
				matrix = pdfgo.Identity()
			}
			if err := p.appendPath(pdfgo.PathMark{Path: path, Style: mark.Style, Fill: fill, Stroke: true, StrokeMatrix: matrix}); err != nil {
				return err
			}
		}
		object.Visible = new(bool)
		object.Fill, object.Stroke = new(bool), new(bool)
	}
	p.objects = append(p.objects, GraphicObject{Type: "TextObject", TextObject: object})
	p.report.TextObjects++
	return nil
}

// degenerateText 保留零字号或零水平缩放的逐字位置与退化字形
// 入参: mark PDF文字绘制信息
// 返回: error 转换错误
func (p *pdfImporter) degenerateText(mark pdfgo.TextMark) error {
	x, y := math.Copysign(1, mark.Size*mark.HorizontalScale), math.Copysign(1, mark.Size)
	size, scale := math.Abs(mark.Size), math.Abs(mark.HorizontalScale)
	if size == 0 {
		size, x, y = 1, 0, 0
	}
	if scale == 0 {
		scale, x = 1, 0
	}
	var clips []TextObject
	for index, position := range mark.Positions {
		glyph := mark
		glyph.Size, glyph.HorizontalScale = size, scale
		glyph.Matrix = mark.Matrix.Mul(pdfgo.Matrix{1, 0, 0, 1, position.X, position.Y}).Mul(pdfgo.Matrix{x, 0, 0, y, 0, 0})
		glyph.Positions = []pdfgo.Point{{}}
		glyph.Glyphs = mark.Glyphs[index : index+1]
		if mark.Clip != nil {
			glyph.Clip = new(pdfgo.TextClip)
		}
		if err := p.text(glyph); err != nil {
			return err
		}
		if glyph.Clip != nil {
			clips = append(clips, p.clipTexts[glyph.Clip]...)
			delete(p.clipTexts, glyph.Clip)
		}
	}
	if mark.Clip != nil {
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		}
		p.clipTexts[mark.Clip] = clips
	}
	return nil
}
