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
	"context"
	"encoding/xml"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxIndexRunes 限制单段索引字形展开后的最大字形数量
const maxIndexRunes = 1 << 20

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

// textCodeRuneCount 统计文本编码中的字符数，识别转义及XML排版空白
// 入参: value 文本编码内容
// 返回: int 字符数量
func textCodeRuneCount(value string) int {
	if strings.Contains(value, "\\") {
		return len(textCodeRunes(value))
	}
	if strings.ContainsAny(value, "\r\n") {
		value = strings.TrimSpace(value)
	}
	return utf8.RuneCountInString(value)
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

// indexGlyphSpans 为索引字形生成指向单占位符的原文区间
// 入参: count 字形数量
// 返回: [][2]int 字符区间列表
func indexGlyphSpans(count int) [][2]int {
	spans := make([][2]int, count)
	for i := range spans {
		spans[i] = [2]int{0, 1}
	}
	return spans
}

// hasTextMatrix 判断文本是否需要应用字形变换
// 入参: ctm 变换矩阵
// 返回: bool 是否需要变换
func hasTextMatrix(ctm Matrix) bool {
	const eps = 1e-9
	return math.Abs(ctm.a-1) > eps || math.Abs(ctm.b) > eps || math.Abs(ctm.c) > eps || math.Abs(ctm.d-1) > eps
}

// textCodePositioned 判断文本编码是否带显式定位
// 入参: textCode 文本编码, xs 横坐标读取器, ys 纵坐标读取器
// 返回: bool 是否带显式定位
func textCodePositioned(textCode TextCode, xs, ys textPositionValues) bool {
	if strings.TrimSpace(textCode.DeltaX) != "" || strings.TrimSpace(textCode.DeltaY) != "" {
		return true
	}
	xs.next()
	ys.next()
	_, multipleX := xs.next()
	_, multipleY := ys.next()
	return multipleX || multipleY
}

// textCodeXMLValue 将空格转义还原为字符，供XML写出层统一编码
// 入参: value OFD文字编码
// 返回: string 待写出的文字编码
func textCodeXMLValue(value string) string {
	if !strings.Contains(value, `\0020`) {
		return value
	}
	if strings.ContainsAny(value, "\r\n") {
		return escapeOFDText(string(textCodeRunes(value)))
	}
	return strings.ReplaceAll(value, `\0020`, " ")
}

// textCodeRunes 获取文本编码字符
// 入参: value 文本编码内容
// 返回: []rune 文本字符
func textCodeRunes(value string) []rune {
	runes, _ := textCodeRunesContext(nil, value)
	return runes
}

// textCodeRunesContext 解码文本字符及转义序列，长文本分段检查取消
// 入参: ctx 取消上下文，可为nil, value 文本编码内容
// 返回: []rune 文本字符, error 取消错误
func textCodeRunesContext(ctx context.Context, value string) ([]rune, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if strings.ContainsAny(value, "\r\n") {
		value = strings.TrimSpace(value)
	}
	runes := []rune(value)
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if !strings.Contains(value, "\\") {
		return runes, nil
	}
	written := 0
	for i := 0; i < len(runes); i++ {
		if ctx != nil && written&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
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
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return runes[:written], nil
}

// Text 获取定位后的文字，横向换行以换行符分隔，字形索引以占位字符表示
// 返回: string 原文
func (obj TextObject) Text() string {
	lines := newTextLineScanner(obj)
	var text strings.Builder
	for _, code := range obj.TextCode {
		if lines.next(code) {
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

// UnmarshalXML 解析标准文字节点，保留跨TextCode的字形位置映射并统一为对象内全局字符索引
// 入参: decoder XML解码器, start 起始节点
// 返回: error 解码错误
func (obj *TextObject) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	type plain TextObject
	type pendingTransform struct {
		item       CGTransform
		afterCodes int
	}
	var value plain
	if err := decodeGraphicAttributes(start, &value); err != nil {
		return err
	}
	var pending []pendingTransform
	if err := decodeOFDChildren(decoder, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "FillColor":
			return d.DecodeElement(&value.FillColor, &node)
		case "StrokeColor":
			return d.DecodeElement(&value.StrokeColor, &node)
		case "Clips":
			return d.DecodeElement(&value.Clips, &node)
		case "Actions":
			return decodeOFDActions(d, node, &value.Actions)
		case "CGTransform":
			var item CGTransform
			if err := d.DecodeElement(&item, &node); err != nil {
				return err
			}
			pending = append(pending, pendingTransform{item: item, afterCodes: len(value.TextCode)})
			return nil
		case "TextCode":
			var code TextCode
			if err := d.DecodeElement(&code, &node); err != nil {
				return err
			}
			value.TextCode = append(value.TextCode, code)
			return nil
		}
		return d.Skip()
	}); err != nil {
		return err
	}
	if len(pending) != 0 && len(value.TextCode) != 0 {
		offsets := make([]int, len(value.TextCode))
		lengths := make([]int, len(value.TextCode))
		total := 0
		for i, code := range value.TextCode {
			offsets[i] = total
			lengths[i] = len(textCodeRunes(code.Value))
			total += lengths[i]
		}
		valid := make([]CGTransform, 0, len(pending))
		for _, entry := range pending {
			item := entry.item
			if item.CodePosition < 0 {
				continue
			}
			pos := item.CodePosition
			switch {
			case entry.afterCodes == 0:
				if pos >= total {
					continue
				}
			case entry.afterCodes < len(value.TextCode):
				segLen := lengths[entry.afterCodes]
				if pos < segLen {
					pos += offsets[entry.afterCodes]
				} else if pos >= total {
					continue
				}
			default:
				if pos >= total {
					continue
				}
			}
			if item.CodeCount <= 0 {
				item.CodeCount = 1
			}
			if item.CodeCount > total-pos {
				item.CodeCount = total - pos
			}
			if item.GlyphCount <= 0 {
				if n := len(strings.Fields(item.Glyphs)); n > 0 {
					item.GlyphCount = n
				} else {
					item.GlyphCount = 1
				}
			}
			item.CodePosition = pos
			valid = append(valid, item)
		}
		value.CGTransform = valid
	}
	*obj = TextObject(value)
	for _, attr := range ofdXMLStart(start).Attr {
		switch attr.Name.Local {
		case "LineWidth":
			obj.LineWidthSet = obj.LineWidth == 0
		case "MiterLimit":
			obj.MiterLimitSet = obj.MiterLimit == 0
		case "DashPattern":
			obj.DashPatternSet = obj.DashPattern == ""
		case "Weight":
			if strings.TrimSpace(attr.Value) != "" {
				obj.WeightSet = true
			}
		case "Italic":
			if strings.TrimSpace(attr.Value) != "" {
				obj.ItalicSet = true
			}
		}
	}
	return nil
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

// UnmarshalXML 解析文字定位及原文，不使用带外部命名空间的定位属性
// 入参: decoder XML解码器, start 文字节点
// 返回: error 解码错误
func (code *TextCode) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	type plain TextCode
	var value plain
	start = ofdXMLStart(start)
	if err := decoder.DecodeElement(&value, &start); err != nil {
		return err
	}
	*code = TextCode(value)
	return nil
}

// UnmarshalXML 解析字形变换，省略的字符数和字形数均取1
// 入参: decoder XML解码器, start 变换起始节点
// 返回: error 解码错误
func (transform *CGTransform) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	*transform = CGTransform{CodeCount: 1, GlyphCount: 1}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target *int
		switch attr.Name.Local {
		case "CodePosition":
			target = &transform.CodePosition
		case "CodeCount":
			target = &transform.CodeCount
		case "GlyphCount":
			target = &transform.GlyphCount
		}
		if target != nil {
			value, err := strconv.Atoi(strings.TrimSpace(attr.Value))
			if err != nil {
				return err
			}
			*target = value
		}
	}
	return decodeOFDChildren(decoder, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "Glyphs" {
			return d.DecodeElement(&transform.Glyphs, &node)
		}
		return d.Skip()
	})
}

// pathStyle 提取文字绘制属性和变换，供路径描边处理复用
// 返回: PathObject 绘制属性
func (obj TextObject) pathStyle() PathObject {
	return PathObject{CTM: obj.CTM, DrawParam: obj.DrawParam, Alpha: obj.Alpha, Fill: obj.Fill, Stroke: obj.Stroke,
		FillColor: obj.FillColor, StrokeColor: obj.StrokeColor, LineWidth: obj.LineWidth, LineWidthSet: obj.LineWidthSet,
		Cap: obj.Cap, Join: obj.Join, MiterLimit: obj.MiterLimit, MiterLimitSet: obj.MiterLimitSet, DashPattern: obj.DashPattern,
		DashPatternSet: obj.DashPatternSet, DashOffset: obj.DashOffset}
}

// withPathStyle 替换共用绘制属性，保留文字内容、字体及定位
// 入参: style 绘制属性
// 返回: TextObject 修改后的文字对象
func (obj TextObject) withPathStyle(style PathObject) TextObject {
	obj.DrawParam, obj.Alpha, obj.Fill, obj.Stroke = style.DrawParam, style.Alpha, style.Fill, style.Stroke
	obj.FillColor, obj.StrokeColor = style.FillColor, style.StrokeColor
	obj.LineWidth, obj.LineWidthSet = style.LineWidth, style.LineWidthSet
	obj.Cap, obj.Join, obj.MiterLimit = style.Cap, style.Join, style.MiterLimit
	obj.MiterLimitSet = style.MiterLimitSet
	obj.DashPattern, obj.DashPatternSet, obj.DashOffset = style.DashPattern, style.DashPatternSet, style.DashOffset
	return obj
}

// textObjectFontID 获取文本对象字体ID
// 入参: text 文本对象
// 返回: string 字体ID
func (r *Renderer) textObjectFontID(text TextObject) string {
	return text.Font
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

// textGlyphFromID 获取字形ID对应的绘制字形
// 入参: fontID 字体ID, glyphID 字形ID或CID
// 返回: textGlyph 绘制字形
func (r *Renderer) textGlyphFromID(fontID string, glyphID int) textGlyph {
	if mapped, ok := r.fontGlyphRune(fontID, glyphID); ok {
		return textGlyph{Text: string(mapped), GlyphID: -1}
	}
	return textGlyph{GlyphID: glyphID}
}

// fontGlyphRune 获取字形ID对应的包装字体字符
// 入参: fontID 字体ID, glyphID 字形ID或CID
// 返回: rune 包装字体字符, bool 是否存在
func (r *Renderer) fontGlyphRune(fontID string, glyphID int) (rune, bool) {
	if glyphID < 0 || glyphID > 0xFFFF {
		return 0, false
	}
	id := uint16(glyphID)
	prepared := r.preparedFonts[fontID]
	if prepared == nil {
		return 0, false
	}
	if mapped, ok := prepared.CIDs[id]; ok {
		return mapped, true
	}
	if mapped, ok := prepared.Glyphs[id]; ok {
		return mapped, true
	}
	return 0, false
}

// parseIndexGlyphs 解析索引字形
// 入参: indexStr 索引字符串, fontID 字体ID
// 返回: []textGlyph 绘制字形列表
func (r *Renderer) parseIndexGlyphs(indexStr, fontID string) []textGlyph {
	parts := strings.Fields(indexStr)
	result := make([]textGlyph, 0, min(len(parts), maxIndexRunes))
	for _, p := range parts {
		if len(result) >= maxIndexRunes {
			break
		}
		if startText, endText, ok := strings.Cut(p, "-"); ok {
			if !strings.Contains(endText, "-") {
				start, err1 := strconv.ParseUint(startText, 10, 16)
				end, err2 := strconv.ParseUint(endText, 10, 16)
				if err1 != nil || err2 != nil || start > end {
					continue
				}
				for k := start; k <= end && len(result) < maxIndexRunes; k++ {
					result = append(result, r.textGlyphFromID(fontID, int(k)))
				}
			}
		} else {
			val, err := strconv.ParseUint(p, 10, 16)
			if err != nil {
				continue
			}
			result = append(result, r.textGlyphFromID(fontID, int(val)))
		}
	}
	return result
}
