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
func (p *pdfImporter) image(mark pdfgo.ImageMark) error {
	source := mark.Image
	filter, err := p.reader.Resolve(source.Stream.Dictionary["Filter"])
	if err != nil {
		return err
	}
	jpegOriginal := filter == pdfgo.Name("DCTDecode") && len(source.Decode) == 0 && !source.ImageMask && source.ColorSpace != pdfgo.Name("DeviceCMYK")
	if source.Mask != nil || source.SoftMask != nil {
		return &pdfgo.UnsupportedError{Feature: "image mask conversion"}
	}
	if !source.ImageMask && source.ColorSpace != pdfgo.Name("DeviceRGB") && source.ColorSpace != pdfgo.Name("DeviceGray") && source.ColorSpace != pdfgo.Name("DeviceCMYK") {
		return &pdfgo.UnsupportedError{Feature: "image color management"}
	}
	if source.Stream.Dictionary["Intent"] != nil || source.Stream.Dictionary["SMaskInData"] != nil {
		return &pdfgo.UnsupportedError{Feature: "image rendering attributes"}
	}
	decoded, err := source.DecodeSamples()
	if err != nil {
		return err
	}
	if len(source.Decode) > 0 {
		components := 1
		if source.ColorSpace == pdfgo.Name("DeviceRGB") {
			components = 3
		} else if source.ColorSpace == pdfgo.Name("DeviceCMYK") {
			components = 4
		}
		if len(source.Decode) != components*2 {
			return fmt.Errorf("invalid PDF image field %q", "Decode")
		}
		ranges := make([]float64, len(source.Decode))
		for n, v := range source.Decode {
			switch v := v.(type) {
			case pdfgo.Integer:
				ranges[n] = float64(v)
			case pdfgo.Real:
				ranges[n] = float64(v)
			default:
				return fmt.Errorf("invalid PDF image field %q", "Decode")
			}
		}
		adjusted := image.NewNRGBA64(image.Rect(0, 0, source.Width, source.Height))
		for y := 0; y < source.Height; y++ {
			for x := 0; x < source.Width; x++ {
				if components == 4 {
					cmyk, ok := decoded.At(x, y).(color.CMYK)
					if !ok {
						return fmt.Errorf("invalid CMYK image samples")
					}
					values := []uint8{cmyk.C, cmyk.M, cmyk.Y, cmyk.K}
					for c := range values {
						value := ranges[2*c] + float64(values[c])/255*(ranges[2*c+1]-ranges[2*c])
						values[c] = uint8(math.Round(math.Max(0, math.Min(1, value)) * 255))
					}
					adjusted.Set(x, y, color.CMYK{C: values[0], M: values[1], Y: values[2], K: values[3]})
					continue
				}
				r, g, b, _ := decoded.At(x, y).RGBA()
				values := []uint32{r, g, b}
				for c := range values {
					index := c
					if components == 1 {
						index = 0
					}
					value := ranges[2*index] + float64(values[c])/65535*(ranges[2*index+1]-ranges[2*index])
					values[c] = uint32(math.Round(math.Max(0, math.Min(1, value)) * 65535))
				}
				adjusted.SetNRGBA64(x, y, color.NRGBA64{R: uint16(values[0]), G: uint16(values[1]), B: uint16(values[2]), A: 65535})
			}
		}
		decoded = adjusted
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
	if jpegOriginal {
		encoded.Write(source.Stream.Data)
	} else {
		if err := png.Encode(&encoded, decoded); err != nil {
			return err
		}
	}
	id, err := p.editor.AddImage(encoded.Bytes())
	if err != nil {
		return err
	}
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1, 0, 0, -1, 0, 1})
	box := pdfBounds([]pdfgo.Point{m.Apply(pdfgo.Point{}), m.Apply(pdfgo.Point{X: 1}), m.Apply(pdfgo.Point{Y: 1}), m.Apply(pdfgo.Point{X: 1, Y: 1})})
	alpha := int(math.Round(mark.Style.Fill.Alpha * 255))
	object := ImageObject{Boundary: pdfBoundary(box), ResourceID: id, CTM: pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y), Alpha: &alpha, Clips: p.clips(mark.Style.Clips, box)}
	p.objects = append(p.objects, GraphicObject{Type: "ImageObject", ImageObject: object})
	p.report.ImageObjects++
	return nil
}

// text 保留原字体、字形编号与逐字基线，不重新塑形
func (p *pdfImporter) text(mark pdfgo.TextMark) error {
	font := mark.Font
	if len(font.Program) == 0 {
		return &pdfgo.UnsupportedError{Feature: "text without embedded font"}
	}
	if font.ProgramType != "FontFile2" && font.ProgramType != "OpenType" {
		return &pdfgo.UnsupportedError{Feature: "font program " + string(font.ProgramType)}
	}
	id := p.fontIDs[font]
	if id == "" {
		var err error
		program, err := pdfFontProgram(font)
		if err != nil {
			return err
		}
		id, err = p.editor.AddFont(FontFile{Name: font.Name + ".ttf", Data: program}, 0)
		if err != nil {
			return err
		}
		p.fontIDs[font] = id
	}
	metrics := p.fontMetrics[id]
	if metrics == nil {
		if p.editor.backends.Fonts == nil {
			return fmt.Errorf("PDF font backend unavailable")
		}
		var err error
		metrics, err = p.editor.backends.Fonts.OpenFont(p.editor.fonts[id].Data)
		if err != nil {
			return err
		}
		p.fontMetrics[id] = metrics
	}
	outline, ok := metrics.(FontOutlines)
	if !ok {
		return fmt.Errorf("PDF font backend does not provide glyph outlines")
	}
	const unit = 25.4 / 72
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, 0, 0})
	var points []pdfgo.Point
	object := TextObject{Font: id, Size: mark.Size * unit, HScale: mark.HorizontalScale}
	position := 0
	for n, glyph := range mark.Glyphs {
		gid := glyph.ID
		if !glyph.HasID {
			if utf8.RuneCountInString(glyph.Text) != 1 {
				return &pdfgo.UnsupportedError{Feature: "simple font glyph ligature mapping"}
			}
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			gid = metrics.GlyphIndex(char)
			if gid == 0 && char != 0 {
				return fmt.Errorf("missing PDF glyph for %U", char)
			}
		}
		if gid >= metrics.NumGlyphs() {
			return fmt.Errorf("PDF glyph index outside embedded font")
		}
		origin := mark.Positions[n]
		object.TextCode = append(object.TextCode, TextCode{X: pdfNumbers(origin.X * unit), Y: pdfNumbers(-origin.Y * unit), Value: glyph.Text})
		object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: position, CodeCount: utf8.RuneCountInString(glyph.Text), GlyphCount: 1, Glyphs: strconv.Itoa(int(gid))})
		position += utf8.RuneCountInString(glyph.Text)
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
	}
	box := pdfBounds(points)
	fill, stroke, visible := mark.Mode == 0 || mark.Mode == 2, mark.Mode == 1 || mark.Mode == 2, mark.Mode != 3
	if stroke {
		margin := mark.Style.LineWidth * unit * math.Hypot(m[0], m[1]) / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{box.X - margin, box.Y - margin, box.W + 2*margin, box.H + 2*margin}
		color := StrokeColor(*p.color(mark.Style.Stroke))
		object.StrokeColor = &color
		object.LineWidth = mark.Style.LineWidth * unit
		object.Join = []string{"Miter", "Round", "Bevel"}[mark.Style.Join]
		object.MiterLimit = mark.Style.MiterLimit
		if len(mark.Style.Dash) > 0 {
			return &pdfgo.UnsupportedError{Feature: "dashed text stroke"}
		}
	}
	object.Boundary = pdfBoundary(box)
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

// pdfFontProgram 为PDF子集字体补齐封装表，不改动字形轮廓、编号或原始度量
func pdfFontProgram(source *pdfgo.Font) ([]byte, error) {
	tables, err := fontFileTables(source.Program, 0)
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
		return source.Program, nil
	}
	return serializeOTF(tables)
}
