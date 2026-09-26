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
	if mark.Image.ImageMask && (mark.Style.Fill.Axial != nil || mark.Style.Fill.Radial != nil) {
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
			fill := mark.Style.Fill.RGB
			tint := color.NRGBA64{
				R: uint16(math.Round(fill[0] * 65535)),
				G: uint16(math.Round(fill[1] * 65535)),
				B: uint16(math.Round(fill[2] * 65535)),
			}
			if mark.Style.Fill.CMYK != nil {
				components := *mark.Style.Fill.CMYK
				tint = color.NRGBA64Model.Convert(color.CMYK{
					C: uint8(math.Round(components[0] * 255)),
					M: uint8(math.Round(components[1] * 255)),
					Y: uint8(math.Round(components[2] * 255)),
					K: uint8(math.Round(components[3] * 255)),
				}).(color.NRGBA64)
			}
			stencil := image.NewNRGBA64(image.Rect(0, 0, source.Width, source.Height))
			for y := 0; y < source.Height; y++ {
				for x := 0; x < source.Width; x++ {
					gray, _, _, _ := decoded.At(x, y).RGBA()
					stencil.SetNRGBA64(x, y, color.NRGBA64{
						R: tint.R,
						G: tint.G,
						B: tint.B,
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
	clips, err := p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	object := ImageObject{Boundary: pdfBoundary(box), ResourceID: id, CTM: pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y), Alpha: &alpha, Clips: clips}
	p.objects = append(p.objects, GraphicObject{Type: "ImageObject", ImageObject: object})
	p.report.ImageObjects++
	return nil
}

// text 保留内嵌字形及逐字基线，外部字体保留名称引用
// 入参: mark PDF文字绘制信息
// 返回: error 错误信息
func (p *pdfImporter) text(mark pdfgo.TextMark) error {
	if mark.Font.Subtype == pdfgo.Name("Type3") {
		if mark.Clip != nil {
			return &pdfgo.UnsupportedError{Feature: "Type3 text clipping"}
		}
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
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		return err
	}
	paintMode := mark.Mode % 4
	if (paintMode == 0 || paintMode == 2) && mark.Style.FillOverprint && pdfOverprintNeedsSeparation(mark.Style.Fill) || (paintMode == 1 || paintMode == 2) && mark.Style.StrokeOverprint && pdfOverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "text color separation overprint"}
	}
	font := mark.Font
	embedded := len(font.Program) != 0
	if embedded && font.ProgramType != "FontFile" && font.ProgramType != "FontFile2" && font.ProgramType != "OpenType" && font.ProgramType != "Type1C" && font.ProgramType != "CIDFontType0C" {
		return &pdfgo.UnsupportedError{Feature: "font program " + string(font.ProgramType)}
	}
	id := p.fontIDs[font]
	if id == "" {
		var err error
		if embedded {
			var type1 *type1Program
			if font.ProgramType == "FontFile" {
				parsed, err := parseType1Program(font.Program)
				if err != nil {
					return fmt.Errorf("PDF font %s program: %w", font.Name, err)
				}
				type1 = &parsed
				if p.type1Glyphs == nil {
					p.type1Glyphs = make(map[*pdfgo.Font]map[string]uint16)
				}
				p.type1Glyphs[font] = parsed.glyphIDs()
			}
			var program []byte
			var limit uint16
			program, limit, err = pdfFontProgram(font, type1)
			if err != nil {
				return fmt.Errorf("PDF font %s program: %w", font.Name, err)
			}
			if limit != 0 {
				if p.fontRepairLimit == nil {
					p.fontRepairLimit = make(map[*pdfgo.Font]uint16)
				}
				p.fontRepairLimit[font] = limit
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded font lacks trailing side bearings; unused metrics reconstructed from glyph bounds"})
			}
			id, err = p.editor.AddFont(FontFile{Name: font.Name + ".ttf", Data: program}, 0)
		} else {
			id, err = p.editor.AddExternalFont(font.Name)
		}
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.fontIDs[font] = id
	}
	if limit := p.fontRepairLimit[font]; limit != 0 {
		for _, glyph := range mark.Glyphs {
			if glyph.HasID && glyph.ID >= limit {
				return &pdfgo.UnsupportedError{Feature: "embedded font glyph with missing side bearing"}
			}
		}
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
				return fmt.Errorf("PDF font %s metrics: %w", font.Name, err)
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
		if embedded && font.ProgramType == "FontFile" {
			var found bool
			gid, found = p.type1Glyphs[font][glyph.Name]
			if !found {
				return fmt.Errorf("missing Type1 glyph %q in PDF font %s", glyph.Name, font.Name)
			}
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
	fill, stroke, visible := paintMode == 0 || paintMode == 2, paintMode == 1 || paintMode == 2, paintMode != 3
	if stroke {
		scale := math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
		if scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			return &pdfgo.UnsupportedError{Feature: "degenerate text stroke matrix"}
		}
		margin := mark.Style.LineWidth * unit / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{box.X - margin, box.Y - margin, box.W + 2*margin, box.H + 2*margin}
		object.LineWidth = mark.Style.LineWidth * unit / scale
		object.LineWidthSet = object.LineWidth == 0
		object.Join = []string{"Miter", "Round", "Bevel"}[mark.Style.Join]
		object.MiterLimit = mark.Style.MiterLimit
		if len(mark.Style.Dash) > 0 {
			return &pdfgo.UnsupportedError{Feature: "dashed text stroke"}
		}
	}
	object.Boundary = pdfBoundary(box)
	if stroke {
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
	object.FillColor, err = p.paintColor(mark.Style.Fill, box)
	if err != nil {
		return err
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
		p.clipTexts[mark.Clip] = clipObject
	}
	p.objects = append(p.objects, GraphicObject{Type: "TextObject", TextObject: object})
	p.report.TextObjects++
	return nil
}

// pdfFontProgram 为PDF子集字体补齐封装表，保留字形轮廓和编号
// 入参: source PDF字体, type1 已解析的Type1程序，nil时按需解析
// 返回: []byte 封装后的字体数据, uint16 缺失度量前的完整字形数, error 错误信息
func pdfFontProgram(source *pdfgo.Font, type1 *type1Program) ([]byte, uint16, error) {
	program := source.Program
	if source.ProgramType == "FontFile" {
		if type1 == nil {
			parsed, err := parseType1Program(program)
			if err != nil {
				return nil, 0, err
			}
			type1 = &parsed
		}
		var err error
		program, err = type1.toCFF(source.Name)
		if err != nil {
			return nil, 0, err
		}
	}
	bareCFF := len(program) >= 4 && program[0] == 1 && program[1] == 0 && program[2] >= 4 && program[3] >= 1 && program[3] <= 4
	if source.ProgramType == "Type1C" || source.ProgramType == "CIDFontType0C" || bareCFF {
		var err error
		program, _, err = wrapCFFToOTF(program)
		if err != nil {
			return nil, 0, err
		}
	}
	tables, err := fontFileTables(program, 0)
	if err != nil {
		return nil, 0, err
	}
	if len(tables["head"]) < 54 || len(tables["maxp"]) < 6 || len(tables["hhea"]) < 36 || len(tables["hmtx"]) == 0 {
		return nil, 0, fmt.Errorf("embedded PDF font lacks required metrics")
	}
	count := binary.BigEndian.Uint16(tables["maxp"][4:6])
	if count == 0 {
		return nil, 0, fmt.Errorf("embedded PDF font has no glyphs")
	}
	changed := false
	var repairedLimit uint16
	metrics := int(binary.BigEndian.Uint16(tables["hhea"][34:36]))
	if metrics == 0 || metrics > int(count) {
		return nil, 0, fmt.Errorf("invalid embedded PDF font metric count")
	}
	metricLength := 4*metrics + 2*(int(count)-metrics)
	if len(tables["hmtx"]) < metricLength {
		if len(tables["hmtx"]) < 4*metrics || len(tables["hmtx"])%2 != 0 {
			return nil, 0, fmt.Errorf("incomplete embedded PDF font metrics: have %d, need %d", len(tables["hmtx"]), metricLength)
		}
		limit := metrics + (len(tables["hmtx"])-4*metrics)/2
		missing, err := pdfMissingLeftBearings(tables, limit, int(count))
		if err != nil {
			return nil, 0, err
		}
		tables["hmtx"] = append(bytes.Clone(tables["hmtx"]), missing...)
		repairedLimit = uint16(limit)
		changed = true
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
				return nil, 0, err
			}
			if len(glyphs) != 1 || !glyphs[0].HasID {
				return nil, 0, &pdfgo.UnsupportedError{Feature: "font without explicit glyph mapping"}
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
			return nil, 0, fmt.Errorf("PDF font name missing or excessive")
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
	for _, tag := range []string{"cvt ", "fpgm", "prep"} {
		if data, ok := tables[tag]; ok && len(data) == 0 {
			delete(tables, tag)
			changed = true
		}
	}
	if !changed && !pdfSFNTMissingPadding(program) {
		return program, repairedLimit, nil
	}
	result, err := serializeOTF(tables)
	return result, repairedLimit, err
}

// pdfSFNTMissingPadding 判断字体表目录是否引用了未写入的末尾对齐字节
// 入参: program OpenType字体数据
// 返回: bool 是否需要重新封装
func pdfSFNTMissingPadding(program []byte) bool {
	if len(program) < 12 {
		return true
	}
	count := int(binary.BigEndian.Uint16(program[4:6]))
	if count > (len(program)-12)/16 {
		return true
	}
	for index := range count {
		record := program[12+16*index:]
		offset := int(binary.BigEndian.Uint32(record[8:12]))
		length := int(binary.BigEndian.Uint32(record[12:16]))
		if offset > len(program) || length > len(program)-offset || (4-length%4)%4 > len(program)-offset-length {
			return true
		}
	}
	return false
}

// pdfMissingLeftBearings 从轮廓边界恢复缺失的尾部左侧边距
// 入参: tables 字体表, start 首个缺失字形, count 字形总数
// 返回: []byte 补齐的hmtx数据, error 无法读取轮廓
func pdfMissingLeftBearings(tables map[string][]byte, start, count int) ([]byte, error) {
	head, loca, glyf := tables["head"], tables["loca"], tables["glyf"]
	if len(head) < 54 || len(glyf) == 0 {
		return nil, fmt.Errorf("embedded PDF font lacks glyph outlines for missing metrics")
	}
	format := int(int16(binary.BigEndian.Uint16(head[50:52])))
	entrySize := 2
	if format == 1 {
		entrySize = 4
	} else if format != 0 {
		return nil, fmt.Errorf("invalid embedded PDF font location format")
	}
	if len(loca) < (count+1)*entrySize {
		return nil, fmt.Errorf("incomplete embedded PDF font glyph locations")
	}
	location := func(index int) int {
		if format == 0 {
			return int(binary.BigEndian.Uint16(loca[index*2:])) * 2
		}
		return int(binary.BigEndian.Uint32(loca[index*4:]))
	}
	result := make([]byte, 2*(count-start))
	for index := start; index < count; index++ {
		from, to := location(index), location(index+1)
		if from > to || to > len(glyf) {
			return nil, fmt.Errorf("invalid embedded PDF font glyph location")
		}
		if from == to {
			continue
		}
		if to-from < 10 {
			return nil, fmt.Errorf("incomplete embedded PDF font glyph outline")
		}
		copy(result[(index-start)*2:], glyf[from+2:from+4])
	}
	return result, nil
}
