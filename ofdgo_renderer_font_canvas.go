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
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/tdewolff/canvas"
	canvastext "github.com/tdewolff/canvas/text"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// canvasFontLoadMu 保护绘图后端加载无名称字体时使用的共享状态
var canvasFontLoadMu sync.Mutex

// textGlyphPathCacheKey 字形路径缓存键
type textGlyphPathCacheKey struct {
	fontDigest [32]byte
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

// textGlyphPathCacheValue 字形路径与局部包围盒缓存值
type textGlyphPathCacheValue struct {
	path   *canvas.Path
	width  float64
	bounds canvas.Rect
}

// loadCanvasFontFamily 加载绘图字体族，含标准名称记录时无锁并行加载
// 入参: family 目标字体族, data 字体数据, index 集合索引, style 字体样式
// 返回: error 加载错误
func loadCanvasFontFamily(family *canvas.FontFamily, data []byte, index int, style canvas.FontStyle) error {
	if index == 0 && sfntHasNameRecord(data) {
		return family.LoadFont(data, index, style)
	}
	canvasFontLoadMu.Lock()
	defer canvasFontLoadMu.Unlock()
	return family.LoadFont(data, index, style)
}

// loadCanvasFont 加载绘图字体，含标准名称记录时无锁并行加载，不改动原始字体数据
// 入参: data 字体数据, index 集合索引, style 字体样式
// 返回: *canvas.Font 绘图字体, error 加载错误
func loadCanvasFont(data []byte, index int, style canvas.FontStyle) (*canvas.Font, error) {
	if index == 0 && sfntHasNameRecord(data) {
		return canvas.LoadFont(data, index, style)
	}
	canvasFontLoadMu.Lock()
	defer canvasFontLoadMu.Unlock()
	return canvas.LoadFont(data, index, style)
}

// sfntHasNameRecord 快速检查OpenType字体是否包含非空标准名称记录
// 入参: data SFNT字体字节
// 返回: bool 是否确保存在非空字体名称
func sfntHasNameRecord(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
	default:
		return false
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	if 12+numTables*16 > len(data) {
		return false
	}
	var nameOff, nameLen int
	for i := range numTables {
		rec := data[12+i*16:]
		if string(rec[:4]) == "name" {
			nameOff = int(binary.BigEndian.Uint32(rec[8:12]))
			nameLen = int(binary.BigEndian.Uint32(rec[12:16]))
			break
		}
	}
	if nameLen < 6 || nameOff < 0 || nameOff+nameLen > len(data) {
		return false
	}
	table := data[nameOff : nameOff+nameLen]
	count := int(binary.BigEndian.Uint16(table[2:4]))
	strOff := int(binary.BigEndian.Uint16(table[4:6]))
	if 6+count*12 > len(table) || strOff > len(table) {
		return false
	}
	for _, wantID := range [3]uint16{6, 4, 1} {
		found := false
		for i := range count {
			rec := table[6+i*12:]
			if binary.BigEndian.Uint16(rec[6:8]) != wantID {
				continue
			}
			found = true
			platform := binary.BigEndian.Uint16(rec[0:2])
			encoding := binary.BigEndian.Uint16(rec[2:4])
			length := int(binary.BigEndian.Uint16(rec[8:10]))
			offset := int(binary.BigEndian.Uint16(rec[10:12]))
			if length == 0 || strOff+offset+length > len(table) {
				return false
			}
			raw := table[strOff+offset : strOff+offset+length]
			if platform == 3 && encoding == 1 && length >= 2 && length%2 == 0 {
				if raw[0] == 0xfe && raw[1] == 0xff {
					raw = raw[2:]
				}
				for j := 0; j+1 < len(raw); j += 2 {
					if raw[j] != 0 || raw[j+1] != 0 {
						return true
					}
				}
				return false
			}
			if platform == 1 && encoding == 0 {
				for _, b := range raw {
					if b != 0 {
						return true
					}
				}
				return false
			}
			return false
		}
		if found {
			return false
		}
	}
	return false
}

// decodeFontContainer 展开字体封装，保留原始OpenType表及CFF2轮廓
// 入参: data 字体文件
// 返回: []byte OpenType数据, error 格式错误
func decodeFontContainer(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("invalid font header")
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true", "ttcf":
		return data, nil
	case "wOFF":
		return basefont.DecodeWOFF(data)
	case "wOF2":
		decoded, handled, err := basefont.DecodeWOFF2(data)
		if handled {
			return decoded, err
		}
	}
	loaded, err := loadCanvasFont(data, 0, canvas.FontRegular)
	if err != nil {
		return nil, err
	}
	return basefont.SerializeOTF(loaded.Tables)
}

// canvasFontStyle 获取Canvas字体样式
// 入参: font OFD字体定义
// 返回: canvas.FontStyle Canvas字体样式
func canvasFontStyle(font *Font) canvas.FontStyle {
	var style canvas.FontStyle
	if font.Bold {
		style |= canvas.FontBold
	}
	if font.Italic {
		style |= canvas.FontItalic
	}
	return style
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
// 入参: face 字体, glyph 绘制字形, outlines 轮廓解析器
// 返回: *canvas.Path 字形路径, float64 字形宽度, error 字形解析错误
func textGlyphPath(face *canvas.FontFace, glyph textGlyph, outlines *basefont.Outliner) (*canvas.Path, float64, error) {
	p := &canvas.Path{}
	width := 0.0
	if glyph.GlyphID < 0 || glyph.GlyphID > 0xFFFF {
		x, y := face.XOffset, face.YOffset
		for _, shaped := range face.Glyphs(glyph.Text) {
			part := &canvas.Path{}
			if err := outlines.Path(part, shaped.ID, face.MmPerEm); err != nil {
				return nil, 0, err
			}
			p = p.Append(part.Translate(face.MmPerEm*float64(x+shaped.XOffset), face.MmPerEm*float64(y+shaped.YOffset)))
			x, y = x+shaped.XAdvance, y+shaped.YAdvance
		}
		width = face.MmPerEm * float64(x)
	} else {
		glyphID := uint16(glyph.GlyphID)
		if err := outlines.Path(p, glyphID, face.MmPerEm); err != nil {
			return nil, 0, err
		}
		width = face.MmPerEm * float64(face.Font.GlyphAdvance(glyphID))
	}
	if face.FauxBold != 0 {
		p = p.Offset(face.FauxBold*face.Size, canvas.Tolerance)
	}
	if face.FauxItalic != 0 {
		p = p.Transform(canvas.Identity.Shear(face.FauxItalic, 0))
	}
	return p, width, nil
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

// loadFont 加载字体，绘图别名按局部字体定义、数据和样式隔离，不改动原始字体名称
// 入参: fontID 字体ID
// 返回: *canvas.FontFamily 字体族
func (r *Renderer) loadFont(fontID string) *canvas.FontFamily {
	resolved, err := r.PrepareFont(fontID)
	if err != nil {
		r.renderError = err
		return nil
	}
	keyID := fontID
	if resolved.definition != nil && resolved.definition.ID != "" {
		keyID = resolved.definition.ID
	}
	state := r.canvasState()
	mapKey := canvasFontMapKey{id: keyID, definition: resolved.definition}
	if ff, ok := state.fontMap[mapKey]; ok {
		return ff
	}
	if len(resolved.Data) == 0 {
		return nil
	}
	of := resolved.definition
	if of == nil {
		of = &Font{ID: fontID, FontName: "OFDGoFallback"}
	}
	fontData := resolved.Data
	fontStyle := canvasFontStyle(of)
	if resolved.digest == ([32]byte{}) {
		resolved.digest = r.fontDigest(fontData)
	}
	key := canvasFontKey{digest: resolved.digest, name: of.FontName, style: fontStyle}
	if ff, ok := state.fontFamilies[key]; ok {
		state.fontMap[mapKey] = ff
		return ff
	}
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	family, err := r.canvasFontFamilies().load(ctx, key, func() (*canvas.FontFamily, int, error) {
		sfntData, dataErr := r.canvasFontData().load(ctx, resolved.digest, func() ([]byte, int, error) {
			decoded, decodeErr := decodeFontContainer(fontData)
			if decodeErr != nil {
				return nil, 0, decodeErr
			}
			cost := 256
			if len(decoded) > 0 && &decoded[0] != &fontData[0] {
				cost += len(decoded)
			}
			return decoded, cost, nil
		})
		if dataErr != nil {
			return nil, 0, dataErr
		}
		built := canvas.NewFontFamily(fmt.Sprintf("ofdgo-%x-%d", resolved.digest, fontStyle))
		if err := loadCanvasFontFamily(built, sfntData, 0, fontStyle); err != nil {
			return nil, 0, err
		}
		cost := min(max(4096, len(sfntData)), r.canvasFontFamilies().limit)
		return built, cost, nil
	})
	if err != nil {
		r.renderError = err
		return nil
	}
	if face := family.Face(10, canvas.Black, fontStyle, canvas.FontNormal); face != nil && face.Font != nil {
		state.fontDigests[face.Font] = resolved.digest
	}
	state.fontFamilies[key] = family
	state.fontMap[mapKey] = family
	return family
}

// cachedTextGlyphPath 获取缓存的字形路径
// 入参: face 字体, glyph 绘制字形
// 返回: *canvas.Path 字形路径, float64 字形宽度
func (r *Renderer) cachedTextGlyphPath(face *canvas.FontFace, glyph textGlyph) (*canvas.Path, float64) {
	cached := r.cachedTextGlyph(face, glyph)
	return cached.path, cached.width
}

// canvasFontOutliner 获取或按摘要缓存Canvas字体对应的OpenType轮廓与标准映射解析器
// 入参: font 画布字体
// 返回: *basefont.Outliner 轮廓解析器, error 解析错误
func (r *Renderer) canvasFontOutliner(font *canvas.Font) (*basefont.Outliner, error) {
	if font == nil {
		return nil, nil
	}
	state := r.canvasState()
	if outlines := state.fontOutlines[font]; outlines != nil {
		return outlines, nil
	}
	digest, ok := state.fontDigests[font]
	if !ok {
		digest = sha256.Sum256(basefont.SFNTData(font.Tables))
		state.fontDigests[font] = digest
	}
	cache := r.canvasFontOutlines()
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	outlines, err := cache.load(ctx, digest, func() (*basefont.Outliner, int, error) {
		metrics, parseErr := basefont.ParseSFNTTables(font.Tables)
		if parseErr != nil {
			return nil, 0, parseErr
		}
		cost := uint64(512)
		for _, data := range metrics.Tables {
			cost += uint64(len(data))
		}
		cost += metrics.CmapEntries() * 16
		return &basefont.Outliner{Font: metrics}, int(min(cost, uint64(cache.limit))), nil
	})
	if err != nil {
		return nil, err
	}
	state.fontOutlines[font] = outlines
	return outlines, nil
}

// canvasGlyphIndex 按OpenType标准Unicode优先级查询Canvas字体的字形编号，避免被MacRoman等旧子表覆盖
// 入参: font 画布字体, char 字符
// 返回: uint16 字形编号
func (r *Renderer) canvasGlyphIndex(font *canvas.Font, char rune) uint16 {
	if font == nil {
		return 0
	}
	if outlines, err := r.canvasFontOutliner(font); err == nil && outlines != nil {
		return outlines.Font.GlyphIndex(char)
	}
	return font.GlyphIndex(char)
}

// canvasGlyphToUnicode 按OpenType标准Unicode优先级反查字形对应的首个Unicode码点
// 入参: font 画布字体, gid 字形编号
// 返回: rune 映射码点, bool 是否存在有效映射
func (r *Renderer) canvasGlyphToUnicode(font *canvas.Font, gid uint16) (rune, bool) {
	if font == nil {
		return 0, false
	}
	if outlines, err := r.canvasFontOutliner(font); err == nil && outlines != nil {
		if rs := outlines.Font.GlyphToUnicode(gid); len(rs) > 0 {
			return rs[0], true
		}
		return 0, false
	}
	if font.SFNT != nil {
		if rs := font.SFNT.GlyphToUnicode(gid); len(rs) > 0 {
			return rs[0], true
		}
	}
	return 0, false
}

// cachedTextGlyph 获取缓存的字形路径、宽度与局部包围盒
// 入参: face 字体, glyph 绘制字形
// 返回: textGlyphPathCacheValue 字形缓存值
func (r *Renderer) cachedTextGlyph(face *canvas.FontFace, glyph textGlyph) textGlyphPathCacheValue {
	state := r.canvasState()
	digest, ok := state.fontDigests[face.Font]
	if !ok {
		digest = sha256.Sum256(basefont.SFNTData(face.Font.Tables))
		state.fontDigests[face.Font] = digest
	}
	key := textGlyphPathCacheKey{
		fontDigest: digest,
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
	glyphCache := r.canvasTextGlyphPaths()
	if cached, ok := glyphCache.get(key); ok {
		return cached
	}
	outlines, err := r.canvasFontOutliner(face.Font)
	if err != nil {
		r.renderError = err
		return textGlyphPathCacheValue{path: &canvas.Path{}}
	}
	path, width, err := textGlyphPath(face, glyph, outlines)
	if err != nil {
		r.renderError = err
		return textGlyphPathCacheValue{path: &canvas.Path{}}
	}
	var bounds canvas.Rect
	if !path.Empty() {
		bounds = path.Bounds()
	}
	val := textGlyphPathCacheValue{path: path, width: width, bounds: bounds}
	glyphCache.put(key, val, 256+len(path.Data())*8+len(glyph.Text)+len(face.Language))
	return val
}
