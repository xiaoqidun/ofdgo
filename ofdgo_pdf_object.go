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
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

// image 转换图像样本，不将整页或其他对象栅格化
// 入参: mark PDF图像绘制信息
// 返回: error 错误信息
func (p *pdfImporter) image(mark pdfgo.ImageMark) error {
	if mark.Style.BlendMode == "Multiply" {
		return &pdfgo.UnsupportedError{Feature: "multiply image outside annotation appearance"}
	}
	if err := p.flushPath(); err != nil {
		return err
	}
	if mark.Image.ImageMask && mark.Style.Fill.Axial != nil {
		return &pdfgo.UnsupportedError{Feature: "gradient stencil image"}
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		return err
	}
	if mark.Style.FillOverprint {
		return &pdfgo.UnsupportedError{Feature: "image color separation overprint"}
	}
	source := mark.Image
	id := p.imageIDs[source.Stream]
	if source.ImageMask {
		id = ""
	}
	if id == "" {
		jbig2Original, err := source.JBIG2File()
		if err != nil {
			return err
		}
		filter, err := p.reader.Resolve(source.Stream.Dictionary["Filter"])
		if err != nil {
			return err
		}
		space, _ := source.ColorSpace.(pdfgo.Name)
		jpegOriginal := filter == pdfgo.Name("DCTDecode") && len(source.Decode) == 0 && !source.ImageMask && (space == "DeviceGray" || space == "DeviceRGB") && source.Mask == nil && source.SoftMask == nil
		var decoded image.Image
		if len(jbig2Original) == 0 {
			decoded, err = source.DecodeImage()
			if err != nil {
				return err
			}
		}
		if source.ImageMask {
			if mark.Style.Fill.CMYK != nil {
				return &pdfgo.UnsupportedError{Feature: "CMYK stencil image"}
			}
			stencil := image.NewNRGBA64(image.Rect(0, 0, source.Width, source.Height))
			fill := mark.Style.Fill.RGB
			for y := 0; y < source.Height; y++ {
				for x := 0; x < source.Width; x++ {
					gray, _, _, _ := decoded.At(x, y).RGBA()
					stencil.SetNRGBA64(x, y, color.NRGBA64{
						R: uint16(math.Round(fill[0] * 65535)),
						G: uint16(math.Round(fill[1] * 65535)),
						B: uint16(math.Round(fill[2] * 65535)),
						A: uint16(65535 - gray),
					})
				}
			}
			decoded = stencil
		}
		var encoded bytes.Buffer
		if len(jbig2Original) != 0 {
			encoded.Write(jbig2Original)
		} else if jpegOriginal {
			encoded.Write(source.Stream.Data)
		} else {
			if err := png.Encode(&encoded, decoded); err != nil {
				return err
			}
		}
		id, err = p.editor.AddImage(encoded.Bytes())
		if err != nil {
			return err
		}
		if !source.ImageMask {
			if p.imageIDs == nil {
				p.imageIDs = map[*pdfgo.Stream]string{}
			}
			p.imageIDs[source.Stream] = id
		}
	}
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1, 0, 0, -1, 0, 1})
	box := pdfBounds([]pdfgo.Point{m.Apply(pdfgo.Point{}), m.Apply(pdfgo.Point{X: 1}), m.Apply(pdfgo.Point{Y: 1}), m.Apply(pdfgo.Point{X: 1, Y: 1})})
	alpha := int(math.Round(mark.Style.Fill.Alpha * 255))
	object := ImageObject{Boundary: pdfBoundary(box), ResourceID: id, CTM: pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y), Alpha: &alpha, Clips: p.clips(mark.Style.Clips, box)}
	p.objects = append(p.objects, GraphicObject{Type: "ImageObject", ImageObject: object})
	p.report.ImageObjects++
	return nil
}

// text 保留内嵌字形及逐字基线，外部字体保留名称引用
// 入参: mark PDF文字绘制信息
// 返回: error 错误信息
func (p *pdfImporter) text(mark pdfgo.TextMark) error {
	if mark.Font.Subtype == pdfgo.Name("Type3") {
		if err := p.flushPath(); err != nil {
			return err
		}
		visitor := pdfgo.Visitor{Path: p.path, Image: p.image}
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
	if mark.Style.BlendMode == "Multiply" {
		return &pdfgo.UnsupportedError{Feature: "multiply text"}
	}
	if err := p.flushPath(); err != nil {
		return err
	}
	if mark.Style.Fill.Axial != nil || mark.Style.Stroke.Axial != nil {
		return &pdfgo.UnsupportedError{Feature: "gradient text paint"}
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		return err
	}
	if (mark.Mode == 0 || mark.Mode == 2) && mark.Style.FillOverprint && pdfOverprintNeedsSeparation(mark.Style.Fill) || (mark.Mode == 1 || mark.Mode == 2) && mark.Style.StrokeOverprint && pdfOverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "text color separation overprint"}
	}
	font := mark.Font
	embedded := len(font.Program) != 0
	if embedded && font.ProgramType != "FontFile2" && font.ProgramType != "OpenType" && font.ProgramType != "Type1C" && font.ProgramType != "CIDFontType0C" {
		return &pdfgo.UnsupportedError{Feature: "font program " + string(font.ProgramType)}
	}
	id := p.fontIDs[font]
	if id == "" {
		var err error
		if embedded {
			program, err := pdfFontProgram(font)
			if err != nil {
				return err
			}
			id, err = p.editor.AddFont(FontFile{Name: font.Name + ".ttf", Data: program}, 0)
		} else {
			id, err = p.editor.AddExternalFont(font.Name)
		}
		if err != nil {
			return err
		}
		p.fontIDs[font] = id
	}
	var metrics FontMetrics
	var outline FontOutlines
	if embedded {
		metrics = p.fontMetrics[id]
		if metrics == nil {
			if p.editor.backends.Fonts == nil {
				return fmt.Errorf("PDF font backend unavailable")
			}
			metrics, err = p.editor.backends.Fonts.OpenFont(p.editor.fonts[id].Data)
			if err != nil {
				return err
			}
			p.fontMetrics[id] = metrics
		}
		var ok bool
		outline, ok = metrics.(FontOutlines)
		if !ok {
			return fmt.Errorf("PDF font backend does not provide glyph outlines")
		}
	}
	const unit = 25.4 / 72
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, 0, 0})
	var points []pdfgo.Point
	object := TextObject{Font: id, Size: mark.Size * unit, HScale: mark.HorizontalScale}
	position := 0
	for n, glyph := range mark.Glyphs {
		gid := glyph.ID
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
			return fmt.Errorf("PDF glyph index outside embedded font")
		}
		if !embedded && glyph.Text == "" {
			return &pdfgo.UnsupportedError{Feature: "unmapped external font character"}
		}
		origin := mark.Positions[n]
		object.TextCode = append(object.TextCode, TextCode{X: pdfNumbers(origin.X * unit), Y: pdfNumbers(-origin.Y * unit), Value: glyph.Text})
		if embedded {
			count := utf8.RuneCountInString(glyph.Text)
			object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: position, CodeCount: count, GlyphCount: 1, Glyphs: strconv.Itoa(int(gid))})
			position += count
			path, err := outline.GlyphOutline(gid, object.Size)
			if err != nil {
				return err
			}
			bounds, err := path.Bounds()
			if err != nil {
				return err
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				points = append(points, m.Apply(pdfgo.Point{X: origin.X*unit + point.X*mark.HorizontalScale, Y: -origin.Y*unit + point.Y}))
			}
		} else {
			width := glyph.Width / 1000 * object.Size * mark.HorizontalScale
			for _, point := range []pdfgo.Point{{X: 0, Y: -object.Size}, {X: width, Y: -object.Size}, {X: 0, Y: object.Size / 4}, {X: width, Y: object.Size / 4}} {
				points = append(points, m.Apply(pdfgo.Point{X: origin.X*unit + point.X, Y: -origin.Y*unit + point.Y}))
			}
		}
	}
	box := pdfBounds(points)
	fill, stroke, visible := mark.Mode == 0 || mark.Mode == 2, mark.Mode == 1 || mark.Mode == 2, mark.Mode != 3
	if stroke {
		scale := math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
		if scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			return &pdfgo.UnsupportedError{Feature: "degenerate text stroke matrix"}
		}
		margin := mark.Style.LineWidth * unit / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{box.X - margin, box.Y - margin, box.W + 2*margin, box.H + 2*margin}
		color := StrokeColor(*p.color(mark.Style.Stroke))
		object.StrokeColor = &color
		object.LineWidth = mark.Style.LineWidth * unit / scale
		object.LineWidthSet = object.LineWidth == 0
		object.Join = []string{"Miter", "Round", "Bevel"}[mark.Style.Join]
		object.MiterLimit = mark.Style.MiterLimit
		if len(mark.Style.Dash) > 0 {
			return &pdfgo.UnsupportedError{Feature: "dashed text stroke"}
		}
	}
	object.Boundary = pdfBoundary(box)
	if box.X >= p.pageWidth || box.Y >= p.pageHeight || box.X+box.W <= 0 || box.Y+box.H <= 0 {
		visible = false
	}
	object.CTM = pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y)
	object.Fill = &fill
	object.Stroke = &stroke
	object.Visible = &visible
	object.FillColor = p.color(mark.Style.Fill)
	object.Clips = p.clips(mark.Style.Clips, box)
	p.objects = append(p.objects, GraphicObject{Type: "TextObject", TextObject: object})
	p.report.TextObjects++
	return nil
}

// pdfFontProgram 为PDF子集字体补齐封装表，保留字形轮廓和编号
// 入参: source PDF字体
// 返回: []byte 封装后的字体数据, error 错误信息
func pdfFontProgram(source *pdfgo.Font) ([]byte, error) {
	program := source.Program
	if source.ProgramType == "Type1C" || source.ProgramType == "CIDFontType0C" {
		var err error
		program, _, err = wrapCFFToOTF(source.Program)
		if err != nil {
			return nil, err
		}
	}
	tables, err := fontFileTables(program, 0)
	if err != nil {
		return nil, err
	}
	if len(tables["head"]) < 54 || len(tables["maxp"]) < 6 || len(tables["hhea"]) < 36 || len(tables["hmtx"]) == 0 {
		return nil, fmt.Errorf("embedded PDF font lacks required metrics")
	}
	count := binary.BigEndian.Uint16(tables["maxp"][4:6])
	if count == 0 {
		return nil, fmt.Errorf("embedded PDF font has no glyphs")
	}
	changed := false
	metrics := int(binary.BigEndian.Uint16(tables["hhea"][34:36]))
	if metrics == 0 || metrics > int(count) {
		return nil, fmt.Errorf("invalid embedded PDF font metric count")
	}
	metricLength := 4*metrics + 2*(int(count)-metrics)
	if len(tables["hmtx"]) < metricLength {
		return nil, fmt.Errorf("incomplete embedded PDF font metrics")
	}
	if len(tables["hmtx"]) > metricLength {
		tables["hmtx"] = tables["hmtx"][:metricLength]
		changed = true
	}
	if len(tables["cmap"]) == 0 {
		mapping := map[rune]uint16{}
		for code := range source.Unicode {
			glyphs, err := source.Decode([]byte(code))
			if err != nil {
				return nil, err
			}
			if len(glyphs) != 1 || !glyphs[0].HasID {
				return nil, &pdfgo.UnsupportedError{Feature: "font without explicit glyph mapping"}
			}
			glyph := glyphs[0]
			if utf8.RuneCountInString(glyph.Text) == 1 {
				char, _ := utf8.DecodeRuneInString(glyph.Text)
				if _, exists := mapping[char]; !exists {
					mapping[char] = glyph.ID
				}
			}
		}
		addPackedGlyphMapping(mapping, count)
		tables["cmap"] = buildCmapTable(count, mapping)
		changed = true
	}
	if len(fontNamesFromTable(tables["name"])) == 0 {
		name := utf16.Encode([]rune(source.Name))
		if len(name) == 0 || len(name) > 32767 {
			return nil, fmt.Errorf("PDF font name missing or excessive")
		}
		data := make([]byte, 42+len(name)*2)
		binary.BigEndian.PutUint16(data[2:], 3)
		binary.BigEndian.PutUint16(data[4:], 42)
		for n, id := range []uint16{1, 4, 6} {
			record := data[6+n*12:]
			binary.BigEndian.PutUint16(record, 3)
			binary.BigEndian.PutUint16(record[2:], 1)
			binary.BigEndian.PutUint16(record[4:], 0x409)
			binary.BigEndian.PutUint16(record[6:], id)
			binary.BigEndian.PutUint16(record[8:], uint16(len(name)*2))
		}
		for n, v := range name {
			binary.BigEndian.PutUint16(data[42+n*2:], v)
		}
		tables["name"] = data
		changed = true
	}
	if len(tables["post"]) == 0 {
		tables["post"] = buildPostTable()
		changed = true
	}
	if len(tables["OS/2"]) == 0 {
		hhea := tables["hhea"]
		tables["OS/2"] = buildOS2TableWithMetrics(int16(binary.BigEndian.Uint16(hhea[4:6])), int16(binary.BigEndian.Uint16(hhea[6:8])))
		changed = true
	}
	if !changed {
		return program, nil
	}
	return serializeOTF(tables)
}
