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
	"crypto/sha256"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/go-text/typesetting/font/opentype/tables"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfImportedFont 保存单次导入共用的字体资源、度量及源字形映射，不保存文档标识
type pdfImportedFont struct {
	resource    *FontResource
	checksum    [32]byte
	metrics     FontMetrics
	type1Glyphs map[string]uint16
	repairLimit uint16
}

// pdfFontProgramKey 区分字体封装内容及交给资源后端的来源名称
type pdfFontProgramKey struct {
	checksum [32]byte
	name     string
}

// pdfTextReplacement 记录一个完整替换区段的文字数量与转换状态
type pdfTextReplacement struct {
	text     *pdfgo.ReplacementText
	count    int
	matched  int
	offset   int64
	blocked  bool
	applied  bool
	identity bool
}

// pdfTextReplacements 按绘制顺序收集替换区段，避免拆分或重复写入原文
type pdfTextReplacements struct {
	importer *pdfImporter
	spans    map[*pdfgo.ReplacementText]*pdfTextReplacement
	order    []*pdfTextReplacement
	widget   bool
}

// pdfGSUBVertCache 按字体字节摘要缓存只读的OpenType竖排替换映射表
var pdfGSUBVertCache sync.Map

// pdfFallbackVertHolder 在Editor字体度量表中附带缓存竖排替换表，不改变主度量契约
type pdfFallbackVertHolder struct {
	FontMetrics
	vert map[uint16]uint16
}

// pdfTextGlyphItem 保存单字形或连字多字形簇的基线坐标、转义原文及字形索引，用于同行合并
type pdfTextGlyphItem struct {
	x            float64
	y            float64
	text         string
	runes        int
	hasTransform bool
	gid          uint16
	gids         []uint16
	xs           []float64
	ys           []float64
}

// pdfEmptyLigatureGlyph 保存待并入相邻含文本字形的显式空串字形坐标与编号
type pdfEmptyLigatureGlyph struct {
	x   float64
	y   float64
	gid uint16
}

// pdfGlyphExplicitEmpty 判断字形是否在ToUnicode映射中显式映射为空串，与缺失映射区分开
// 入参: font 源PDF字体, glyph 待检查字形
// 返回: bool 是否为显式空串映射
func pdfGlyphExplicitEmpty(font *pdfgo.Font, glyph pdfgo.Glyph) bool {
	if glyph.Text != "" || glyph.Name == ".notdef" || font == nil || len(font.Unicode) == 0 {
		return false
	}
	code := glyph.Code
	var buf [4]byte
	if font.Subtype == "Type0" || code > 0xff {
		if code <= 0xffff {
			buf[0], buf[1] = byte(code>>8), byte(code)
			if text, ok := font.Unicode[string(buf[:2])]; ok {
				return text == ""
			}
		} else {
			if code <= 0xffffff {
				buf[0], buf[1], buf[2] = byte(code>>16), byte(code>>8), byte(code)
				if text, ok := font.Unicode[string(buf[:3])]; ok {
					return text == ""
				}
			}
			buf[0], buf[1], buf[2], buf[3] = byte(code>>24), byte(code>>16), byte(code>>8), byte(code)
			if text, ok := font.Unicode[string(buf[:4])]; ok {
				return text == ""
			}
		}
	}
	if code <= 0xff {
		buf[0] = byte(code)
		if text, ok := font.Unicode[string(buf[:1])]; ok {
			return text == ""
		}
	}
	return false
}

// pdfParseGSUBVertical 解析OpenType字体GSUB表的vert与vrt2单字形竖排替换映射
// 入参: data 字体原始字节
// 返回: map[uint16]uint16 横排字形到竖排字形的映射表
func pdfParseGSUBVertical(data []byte) map[uint16]uint16 {
	tbls, err := basefont.FileTables(data, 0)
	if err != nil || len(tbls["GSUB"]) == 0 {
		return nil
	}
	layout, _, err := tables.ParseLayout(tbls["GSUB"])
	if err != nil {
		return nil
	}
	var vertLookups, vrt2Lookups []uint16
	for i, rec := range layout.FeatureList.Records {
		if i >= len(layout.FeatureList.Features) {
			break
		}
		tag := rec.Tag.String()
		if tag == "vert" {
			vertLookups = append(vertLookups, layout.FeatureList.Features[i].LookupListIndices...)
		} else if tag == "vrt2" {
			vrt2Lookups = append(vrt2Lookups, layout.FeatureList.Features[i].LookupListIndices...)
		}
	}
	if len(vertLookups) == 0 && len(vrt2Lookups) == 0 {
		return nil
	}
	result := make(map[uint16]uint16)
	applyLookup := func(li uint16, overwrite bool) {
		if int(li) >= len(layout.LookupList.Lookups) {
			return
		}
		subtables, err := layout.LookupList.Lookups[li].AsGSUBLookups()
		if err != nil {
			return
		}
		for _, st := range subtables {
			if ext, ok := st.(tables.ExtensionSubs); ok {
				if ext.ExtensionOffset < 8 || uint64(ext.ExtensionOffset) > uint64(len(ext.RawData)) {
					continue
				}
				st, err = ext.Resolve()
				if err != nil {
					continue
				}
			}
			single, ok := st.(tables.SingleSubs)
			if !ok {
				continue
			}
			add := func(src, dst uint16) {
				if src == 0 || dst == 0 {
					return
				}
				if overwrite || result[src] == 0 {
					result[src] = dst
				}
			}
			switch d := single.Data.(type) {
			case tables.SingleSubstData1:
				switch cov := d.Coverage.(type) {
				case tables.Coverage1:
					for _, gid := range cov.Glyphs {
						add(uint16(gid), uint16(int32(gid)+int32(d.DeltaGlyphID)))
					}
				case tables.Coverage2:
					for _, rg := range cov.Ranges {
						for gid := rg.StartGlyphID; gid <= rg.EndGlyphID; gid++ {
							add(uint16(gid), uint16(int32(gid)+int32(d.DeltaGlyphID)))
							if gid == math.MaxUint16 {
								break
							}
						}
					}
				}
			case tables.SingleSubstData2:
				switch cov := d.Coverage.(type) {
				case tables.Coverage1:
					for idx, gid := range cov.Glyphs {
						if idx < len(d.SubstituteGlyphIDs) {
							add(uint16(gid), uint16(d.SubstituteGlyphIDs[idx]))
						}
					}
				case tables.Coverage2:
					for _, rg := range cov.Ranges {
						for gid := rg.StartGlyphID; gid <= rg.EndGlyphID; gid++ {
							idx := int(rg.StartCoverageIndex) + int(gid-rg.StartGlyphID)
							if idx < len(d.SubstituteGlyphIDs) {
								add(uint16(gid), uint16(d.SubstituteGlyphIDs[idx]))
							}
							if gid == math.MaxUint16 {
								break
							}
						}
					}
				}
			}
		}
	}
	for _, li := range vertLookups {
		applyLookup(li, true)
	}
	for _, li := range vrt2Lookups {
		applyLookup(li, false)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// pdfVerticalFallbackRune 在回退字体中解析竖排字符字形，兼容波浪号与破折号缺字回退
// 入参: metrics 回退字体度量, r 原Unicode字符
// 返回: uint16 字形编号, bool 是否发生了等价字符替换
func pdfVerticalFallbackRune(metrics FontMetrics, r rune) (uint16, bool) {
	if gid := metrics.GlyphIndex(r); gid != 0 {
		return gid, false
	}
	switch r {
	case '\u301c':
		if gid := metrics.GlyphIndex('\uff5e'); gid != 0 {
			return gid, true
		}
	case '\u2015':
		if gid := metrics.GlyphIndex('\u2014'); gid != 0 {
			return gid, true
		}
	}
	return 0, false
}

// pdfVerticalRotateRune 判断竖排排版中缺少vert字形时需顺时针旋转90度的标点与符号
// 入参: r Unicode字符
// 返回: bool 是否需要顺时针旋转90度
func pdfVerticalRotateRune(r rune) bool {
	switch r {
	case '\u301c', '\uff5e', '\u30fc', '\u2015', '\u2014', '\u2013', '\u2026', '\u2025',
		'\uff08', '\uff09', '\u3014', '\u3015', '\uff3b', '\uff3d', '\uff5b', '\uff5d',
		'\u3008', '\u3009', '\u300a', '\u300b', '\u300c', '\u300d', '\u300e', '\u300f',
		'\u3010', '\u3011', '\u3016', '\u3017', '\u3018', '\u3019', '\u301a', '\u301b',
		'\uff1d', '\uff0d', '\uff1a', '\uff1b', '(', ')', '[', ']', '{', '}':
		return true
	}
	return false
}

// pdfRotateVerticalGlyph 将横排字形轮廓围绕字形框中心顺时针旋转90度
// 入参: path 横排字形轮廓, metrics 字体度量, gid 字形编号, size 毫米字号
// 返回: GeometryPath 旋转后轮廓, error 变换错误
func pdfRotateVerticalGlyph(path GeometryPath, metrics FontMetrics, gid uint16, size float64) (GeometryPath, error) {
	if len(path) == 0 {
		return path, nil
	}
	cx, cy := 0.5*size, -0.38*size
	if upem := float64(metrics.UnitsPerEm()); upem > 0 {
		if adv := float64(metrics.GlyphAdvance(gid)) * size / upem; adv > 0 {
			cx = adv / 2
		}
		if asc, desc, _ := metrics.VerticalMetrics(); asc != 0 || desc != 0 {
			cy = -(float64(asc) - float64(desc)) / 2 * size / upem
		}
	}
	return path.Transform(Matrix{a: 0, b: 1, c: -1, d: 0, e: cx + cy, f: cy - cx})
}

// pdfItemFlat 判断字形项的所有字形是否位于同一水平基线
// 入参: item 字形项
// 返回: bool 是否同基线
func pdfItemFlat(item pdfTextGlyphItem) bool {
	if len(item.gids) <= 1 {
		return item.hasTransform || item.runes == 1
	}
	for i := 1; i < len(item.ys); i++ {
		if math.Abs(item.ys[i]-item.ys[0]) > 1e-9 {
			return false
		}
	}
	return true
}

// pdfFormatGlyphIDs 将字形编号列表格式化为空格分隔的CGTransform序列
// 入参: gids 字形编号切片
// 返回: string 空格分隔字符串
func pdfFormatGlyphIDs(gids []uint16) string {
	var buf strings.Builder
	for i, gid := range gids {
		if i > 0 {
			buf.WriteByte(' ')
		}
		buf.WriteString(strconv.Itoa(int(gid)))
	}
	return buf.String()
}

// pdfApplyReplacement 将整段原文映射到全部字形，字形位移遵循GB/T 33190的文字定位规则
// 入参: object 文字对象, text 整段原文, positions 原始字形位置
func pdfApplyReplacement(object *TextObject, text string, positions []pdfgo.Point) {
	const unit = 25.4 / 72
	glyphs := make([]string, len(object.CGTransform))
	for i, transform := range object.CGTransform {
		glyphs[i] = transform.Glyphs
	}
	dx, dy := make([]float64, len(positions)-1), make([]float64, len(positions)-1)
	for i := 1; i < len(positions); i++ {
		dx[i-1] = (positions[i].X - positions[i-1].X) * unit
		dy[i-1] = -(positions[i].Y - positions[i-1].Y) * unit
	}
	object.TextCode = []TextCode{{X: pdfNumbers(positions[0].X * unit), Y: pdfNumbers(-positions[0].Y * unit), DeltaX: pdfNumbers(dx...), DeltaY: pdfNumbers(dy...), Value: escapeOFDText(text)}}
	object.CGTransform = []CGTransform{{CodeCount: utf8.RuneCountInString(text), GlyphCount: len(glyphs), Glyphs: strings.Join(glyphs, " ")}}
}

// pdfCompactTextRuns 将同基线连续字形合并为单条TextCode与DeltaX，并输出多字形连字CGTransform
// 入参: object 目标文字对象, items 字形项序列
func pdfCompactTextRuns(object *TextObject, items []pdfTextGlyphItem) {
	if len(items) == 0 {
		return
	}
	object.TextCode = make([]TextCode, 0, 1)
	var textBuf strings.Builder
	var glyphBuf strings.Builder
	var deltas []float64
	pos := 0
	for start := 0; start < len(items); {
		first := items[start]
		if len(first.gids) > 1 && !pdfItemFlat(first) {
			dx := make([]float64, len(first.xs)-1)
			dy := make([]float64, len(first.ys)-1)
			for k := 1; k < len(first.xs); k++ {
				dx[k-1] = first.xs[k] - first.xs[k-1]
				dy[k-1] = first.ys[k] - first.ys[k-1]
			}
			object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: pos, CodeCount: first.runes, GlyphCount: len(first.gids), Glyphs: pdfFormatGlyphIDs(first.gids)})
			object.TextCode = append(object.TextCode, TextCode{X: pdfNumbers(first.xs[0]), Y: pdfNumbers(first.ys[0]), DeltaX: pdfNumbers(dx...), DeltaY: pdfNumbers(dy...), Value: first.text})
			pos += first.runes
			start++
			continue
		}
		end := start + 1
		if pdfItemFlat(first) {
			for end < len(items) && math.Abs(items[end].y-first.y) <= 1e-9 && pdfItemFlat(items[end]) {
				end++
			}
		}
		textBuf.Reset()
		deltas = deltas[:0]
		curX := first.x
		runStart, runCount := -1, 0
		glyphBuf.Reset()
		flushTransform := func() {
			if runCount == 0 {
				return
			}
			object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: runStart, CodeCount: runCount, GlyphCount: runCount, Glyphs: glyphBuf.String()})
			runStart, runCount = -1, 0
			glyphBuf.Reset()
		}
		for i := start; i < end; i++ {
			item := items[i]
			textBuf.WriteString(item.text)
			if len(item.gids) > 1 {
				for k, gx := range item.xs {
					if i > start || k > 0 {
						dx := gx - curX
						deltas = append(deltas, dx)
						curX += dx
					}
				}
				flushTransform()
				object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: pos, CodeCount: item.runes, GlyphCount: len(item.gids), Glyphs: pdfFormatGlyphIDs(item.gids)})
			} else {
				if i > start {
					dx := item.x - curX
					deltas = append(deltas, dx)
					curX += dx
				}
				if !item.hasTransform {
					flushTransform()
				} else if item.runes == 1 {
					if runCount == 0 {
						runStart = pos
					} else {
						glyphBuf.WriteByte(' ')
					}
					glyphBuf.WriteString(strconv.Itoa(int(item.gid)))
					runCount++
				} else {
					flushTransform()
					object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: pos, CodeCount: item.runes, GlyphCount: 1, Glyphs: strconv.Itoa(int(item.gid))})
				}
			}
			pos += item.runes
		}
		flushTransform()
		code := TextCode{X: pdfNumbers(first.x), Y: pdfNumbers(first.y), Value: textBuf.String()}
		if len(deltas) > 0 {
			code.DeltaX = pdfNumbers(deltas...)
		}
		object.TextCode = append(object.TextCode, code)
		start = end
	}
}

// span 查找最外层替换区段，相邻区段即使原文相同也分别保留
// 入参: text 替换文本
// 返回: *pdfTextReplacement 区段状态，未声明时为空
func (r *pdfTextReplacements) span(text *pdfgo.ReplacementText) *pdfTextReplacement {
	if text == nil {
		return nil
	}
	if r.spans == nil {
		r.spans = make(map[*pdfgo.ReplacementText]*pdfTextReplacement)
	}
	first := text
	for text.Parent != nil && r.spans[text] == nil {
		text = text.Parent
	}
	span := r.spans[text]
	if span == nil {
		span = &pdfTextReplacement{text: text}
		r.spans[text] = span
		r.order = append(r.order, span)
	}
	for first != text {
		r.spans[first] = span
		first = first.Parent
	}
	return span
}

// block 标记无法仅用字符与字形变换表达的区段
// 入参: text 替换文本
func (r *pdfTextReplacements) block(text *pdfgo.ReplacementText) {
	if span := r.span(text); span != nil {
		span.blocked = true
	}
}

// markedContent 处理结构区段、可选内容及替换文本属性
// 入参: mark 内容标记
// 返回: error 未支持的语义错误
func (r *pdfTextReplacements) markedContent(mark pdfgo.MarkedContentMark) error {
	if mark.Operator == "EMC" {
		return nil
	}
	if r.importer.structureBindings != nil {
		if err := r.importer.prepareSourceStructure(r.importer.ctx, mark.Reader); err != nil {
			return err
		}
		r.importer.structureBindings.observe(r.importer.ctx, mark.Sequence)
		if mark.Sequence != nil && r.importer.structureBindings.marked != nil {
			key, ok, err := pdfStructureContentKey(mark.Sequence)
			if err != nil {
				return err
			}
			if ok && r.importer.structureBindings.marked[key] {
				return nil
			}
		}
	}
	if mark.Tag == "OC" {
		kind, err := mark.Reader.Resolve(mark.Properties["Type"])
		if err != nil {
			return err
		}
		if kind == pdfgo.Name("OCG") || kind == pdfgo.Name("OCMD") {
			return nil
		}
	}
	if r.widget && mark.Tag == "Tx" && len(mark.Properties) == 0 {
		return nil
	}
	if mark.Replacement != nil && len(mark.Properties) == 1 {
		return nil
	}
	if r.importer.warning == nil {
		return &pdfgo.UnsupportedError{Feature: "marked content requiring semantic preservation"}
	}
	r.importer.warning(pdfgo.Diagnostic{Offset: mark.Offset, Message: "marked content " + string(mark.Tag) + " not preserved"})
	return nil
}

// pdfClipTextEntry 保存文字裁剪转换得到的文本对象及竖排替形或旋转后的轮廓路径
type pdfClipTextEntry struct {
	texts []TextObject
	paths []PathObject
}

// apply 合并可等价表示的替换区段及相邻显式空串连字，原文相同但样式不同的区段保留原图元
// 入参: nodes 原始图元
// 返回: []pdfCompositeNode 转换图元, error 取消或语义转换错误
func (r *pdfTextReplacements) apply(nodes []pdfCompositeNode) ([]pdfCompositeNode, error) {
	if len(r.order) != 0 {
		if err := r.count(nodes, false, r.importer.compositeSpace); err != nil {
			return nil, err
		}
		for _, span := range r.order {
			span.identity = !span.blocked && span.count > 0 && span.matched == len(span.text.Text)
		}
		var err error
		nodes, err = r.merge(nodes)
		if err != nil {
			return nil, err
		}
		for _, span := range r.order {
			if span.applied || span.identity || !span.blocked && span.count == 0 && span.text.Text == "" {
				continue
			}
			if r.importer.warning == nil {
				return nil, &pdfgo.UnsupportedError{Feature: "ActualText span without equivalent OFD glyph mapping"}
			}
			r.importer.warning(pdfgo.Diagnostic{Offset: span.offset, Message: "PDF ActualText span not preserved; original appearance retained"})
		}
		r.clear(nodes)
	}
	return r.mergeEmptyLigatures(nodes)
}

// pdfCanMergeEmptyLigature 检查两个连续文字图元是否同组同样式且可无损合并连字字形
// 入参: first 首个文字图元, next 后续文字图元
// 返回: bool 是否可合并
func pdfCanMergeEmptyLigature(first, next *pdfgo.TextMark) bool {
	if first == nil || next == nil || first.Font == nil || len(first.Font.Program) == 0 || first.Font != next.Font {
		return false
	}
	if first.Object != next.Object || first.Sequence != next.Sequence || first.Replacement != nil || next.Replacement != nil {
		return false
	}
	if first.Mode != 0 || next.Mode != 0 || first.Size <= 0 || first.Size != next.Size || first.HorizontalScale <= 0 || first.HorizontalScale != next.HorizontalScale || first.Clip != nil || next.Clip != nil {
		return false
	}
	if first.Matrix[0] != next.Matrix[0] || first.Matrix[1] != next.Matrix[1] || first.Matrix[2] != next.Matrix[2] || first.Matrix[3] != next.Matrix[3] {
		return false
	}
	style := first.Style
	paint := style.Fill
	if style.SoftMask != nil || style.Transfer != nil || style.Halftone != nil || style.AlphaIsShape || style.FillOverprint || paint.Alpha != 1 || !codecpdf.NormalBlend(style.BlendMode) || paint.Tiling != nil || paint.Axial != nil || paint.Radial != nil || paint.Mesh != nil || paint.Function != nil || paint.Shading != nil {
		return false
	}
	return reflect.DeepEqual(style, next.Style)
}

// mergeEmptyLigatures 将同对象同样式的显式空串连字图元并入相邻含文本文字图元，消除占位符污染
// 入参: nodes 同层图元列表
// 返回: []pdfCompositeNode 合并后的图元列表, error 取消错误
func (r *pdfTextReplacements) mergeEmptyLigatures(nodes []pdfCompositeNode) ([]pdfCompositeNode, error) {
	if len(nodes) == 0 {
		return nodes, nil
	}
	result := nodes[:0]
	for i := 0; i < len(nodes); {
		if err := r.importer.ctx.Err(); err != nil {
			return nil, err
		}
		node := nodes[i]
		var err error
		node.children, err = r.mergeEmptyLigatures(node.children)
		if err != nil {
			return nil, err
		}
		if node.text == nil || len(node.forms) != 0 || node.text.Font == nil || len(node.text.Font.Program) == 0 || len(node.text.Font.Unicode) == 0 {
			result = append(result, node)
			i++
			continue
		}
		end := i + 1
		for end < len(nodes) && len(nodes[end].forms) == 0 && pdfCanMergeEmptyLigature(node.text, nodes[end].text) {
			end++
		}
		if end-i > 1 {
			hasEmpty, hasText := false, false
			for k := i; k < end; k++ {
				for _, g := range nodes[k].text.Glyphs {
					if pdfGlyphExplicitEmpty(nodes[k].text.Font, g) {
						hasEmpty = true
					} else if g.Text != "" {
						hasText = true
					}
				}
			}
			if hasEmpty && hasText {
				marks := make([]*pdfgo.TextMark, 0, end-i)
				for k := i; k < end; k++ {
					marks = append(marks, nodes[k].text)
				}
				if merged, ok := codecpdf.MergeReplacement(marks, nil); ok {
					node.text = merged
					result = append(result, node)
					i = end
					continue
				}
			}
		}
		result = append(result, node)
		i++
	}
	clear(nodes[len(result):])
	return result, nil
}

// count 统计区段文字并逐字核对原文，排除需要组级合成的内容
// 入参: nodes 图元, blocked 上层组是否阻止直接转换, space 当前继承的混合色彩空间
// 返回: error 取消错误
func (r *pdfTextReplacements) count(nodes []pdfCompositeNode, blocked bool, space *pdfgo.ColorSpace) error {
	for _, node := range nodes {
		if err := r.importer.ctx.Err(); err != nil {
			return err
		}
		if node.text != nil {
			if span := r.span(node.text.Replacement); span != nil {
				span.count++
				span.blocked = span.blocked || blocked
				for i, glyph := range node.text.Glyphs {
					if span.matched < 0 {
						break
					}
					if i%256 == 0 {
						if err := r.importer.ctx.Err(); err != nil {
							return err
						}
					}
					if glyph.Text == "" || !strings.HasPrefix(span.text.Text[span.matched:], glyph.Text) {
						span.matched = -1
						break
					}
					span.matched += len(glyph.Text)
				}
			}
		}
		childSpace := space
		childBlocked := blocked
		if group := node.group; group != nil && node.textObject == nil {
			if group.ColorSpace != nil {
				childSpace = group.ColorSpace
			}
			opaque, err := r.importer.opaque(node, childSpace)
			if err := r.importer.ctx.Err(); err != nil {
				return err
			}
			childBlocked = childBlocked || !opaque || err != nil || childSpace != nil && !childSpace.SRGBEquivalent()
		}
		if err := r.count(node.children, childBlocked, childSpace); err != nil {
			return err
		}
	}
	return nil
}

// merge 合并同层连续图元，保留原始绘制顺序、字形坐标与合法表单复用区间
// 入参: nodes 图元
// 返回: []pdfCompositeNode 合并结果, error 取消错误
func (r *pdfTextReplacements) merge(nodes []pdfCompositeNode) ([]pdfCompositeNode, error) {
	type formRange struct {
		start int
		end   int
		mark  *pdfgo.FormMark
	}
	origLen := len(nodes)
	var origForms []formRange
	for idx := range nodes {
		for _, sp := range nodes[idx].forms {
			if sp.count > 0 && idx+sp.count <= origLen {
				origForms = append(origForms, formRange{start: idx, end: idx + sp.count, mark: sp.mark})
			}
		}
	}
	var oldToNew []int
	if len(origForms) != 0 {
		oldToNew = make([]int, origLen)
	}
	result := nodes[:0]
	for i := 0; i < len(nodes); {
		if err := r.importer.ctx.Err(); err != nil {
			return nil, err
		}
		node := nodes[i]
		var err error
		node.children, err = r.merge(node.children)
		if err != nil {
			return nil, err
		}
		end := i + 1
		combined := false
		first := node.text
		if first == nil && node.textObject != nil && len(node.children) != 0 {
			first = node.children[0].text
		}
		if first != nil {
			if span := r.span(first.Replacement); span != nil && !span.applied && !span.blocked && span.text.Text != "" && !(span.identity && span.count == 1) {
				var marks []*pdfgo.TextMark
				for end = i; end < len(nodes); end++ {
					current := nodes[end]
					children := current.children
					if current.text != nil {
						children = nodes[end : end+1]
					} else if current.textObject == nil {
						break
					}
					valid := len(children) != 0
					for _, child := range children {
						if child.text == nil || r.span(child.text.Replacement) != span {
							valid = false
							break
						}
					}
					if !valid {
						break
					}
					for _, child := range children {
						marks = append(marks, child.text)
					}
				}
				end = max(end, i+1)
				if len(marks) == span.count {
					if mark, ok := codecpdf.MergeReplacement(marks, span.text); ok {
						var origins pdfOriginSet
						for _, source := range marks {
							if err := r.importer.ctx.Err(); err != nil {
								return nil, err
							}
							origin := r.importer.textOrigin(*source)
							origins.add(origin.sequence)
							for _, sequence := range origin.related {
								origins.add(sequence)
							}
						}
						if len(origins.origin.related) != 0 {
							if r.importer.textOrigins == nil {
								r.importer.textOrigins = make(map[*pdfgo.ReplacementText]pdfObjectOrigin)
							}
							r.importer.textOrigins[span.text] = origins.origin
						}
						if node.text != nil {
							node.text = mark
						} else {
							node.children = []pdfCompositeNode{{text: mark}}
						}
						span.applied = true
						combined = true
					}
				}
			}
		}
		newIdx := len(result)
		result = append(result, node)
		if oldToNew != nil {
			if combined {
				for k := i; k < end; k++ {
					oldToNew[k] = newIdx
				}
			} else {
				oldToNew[i] = newIdx
			}
		}
		if !combined {
			for k := i + 1; k < end; k++ {
				if oldToNew != nil {
					oldToNew[k] = len(result)
				}
				result = append(result, nodes[k])
			}
		}
		i = end
	}
	if len(origForms) != 0 && len(result) != origLen {
		for k := range result {
			result[k].forms = nil
		}
		for _, fr := range origForms {
			if fr.start > 0 && oldToNew[fr.start] == oldToNew[fr.start-1] {
				continue
			}
			if fr.end < origLen && oldToNew[fr.end] == oldToNew[fr.end-1] {
				continue
			}
			newStart := oldToNew[fr.start]
			newEnd := oldToNew[fr.end-1] + 1
			if newEnd > newStart {
				result[newStart].forms = append(result[newStart].forms, pdfFormSpan{mark: fr.mark, count: newEnd - newStart})
			}
		}
	}
	clear(nodes[len(result):])
	return result, nil
}

// clear 清除无需替换及未转换的区段引用，防止局部绘制重复使用整段原文
// 入参: nodes 图元
func (r *pdfTextReplacements) clear(nodes []pdfCompositeNode) {
	for _, node := range nodes {
		if node.text != nil {
			if span := r.span(node.text.Replacement); span != nil && !span.applied {
				node.text.Replacement = nil
			}
		}
		r.clear(node.children)
	}
}

// fallbackExternalFont 解析非内嵌字体实际渲染所用的系统回退字体度量及竖排替换映射
// 入参: id 字体资源标识
// 返回: FontMetrics 回退字体度量, map[uint16]uint16 竖排替换表
func (p *pdfImporter) fallbackExternalFont(id string) (FontMetrics, map[uint16]uint16) {
	if p.editor == nil || p.editor.backends.Fonts == nil {
		return nil, nil
	}
	cacheKey := "\x00fb:" + id
	if cached := p.editor.fontMetrics[cacheKey]; cached != nil {
		if holder, ok := p.editor.fontMetrics[cacheKey+":v"].(pdfFallbackVertHolder); ok {
			return cached, holder.vert
		}
		return cached, nil
	}
	var external *Font
	for _, resource := range p.editor.resources {
		if resource.font != nil && resource.font.ID == id {
			external = resource.font
			break
		}
	}
	if external == nil {
		return nil, nil
	}
	reader := &Reader{doc: &Document{}, fontResourcesRead: true, fontCache: map[string]*Font{id: external}}
	resolved, err := p.editor.newRenderer(reader).ResolveFont(id, false)
	if err != nil || len(resolved.Data) == 0 {
		return nil, nil
	}
	metrics, err := p.editor.backends.Fonts.OpenFont(resolved.Data)
	if err != nil {
		return nil, nil
	}
	digest := sha256.Sum256(resolved.Data)
	var vert map[uint16]uint16
	if raw, ok := pdfGSUBVertCache.Load(digest); ok {
		vert, _ = raw.(map[uint16]uint16)
	} else {
		vert = pdfParseGSUBVertical(resolved.Data)
		pdfGSUBVertCache.Store(digest, vert)
	}
	if p.editor.fontMetrics == nil {
		p.editor.fontMetrics = make(map[string]FontMetrics)
	}
	p.editor.fontMetrics[cacheKey] = metrics
	p.editor.fontMetrics[cacheKey+":v"] = pdfFallbackVertHolder{vert: vert}
	return metrics, vert
}

// pdfSimpleFontWidthScales 按非内嵌简单字体显式Widths与回退字形进位宽计算逐字水平缩放比例
// 入参: font 源PDF字体, glyphs 字形序列, metrics 回退字体度量
// 返回: []float64 逐字缩放比例, bool 是否存在显著偏差
func pdfSimpleFontWidthScales(font *pdfgo.Font, glyphs []pdfgo.Glyph, metrics FontMetrics) ([]float64, bool) {
	if font == nil || font.Vertical || font.Dictionary == nil || font.Dictionary["Widths"] == nil || metrics == nil || len(glyphs) == 0 {
		return nil, false
	}
	if font.Subtype != "Type1" && font.Subtype != "TrueType" && font.Subtype != "MMType1" {
		return nil, false
	}
	upem := float64(metrics.UnitsPerEm())
	if upem <= 0 {
		return nil, false
	}
	scales := make([]float64, len(glyphs))
	changed := false
	for i, glyph := range glyphs {
		scales[i] = 1
		if glyph.Width <= 0 || glyph.Name == ".notdef" || utf8.RuneCountInString(glyph.Text) != 1 {
			continue
		}
		char, _ := utf8.DecodeRuneInString(glyph.Text)
		if unicode.IsSpace(char) {
			continue
		}
		gid := metrics.GlyphIndex(char)
		if gid == 0 {
			continue
		}
		adv := float64(metrics.GlyphAdvance(gid)) * 1000 / upem
		if adv <= 0 {
			continue
		}
		ratio := glyph.Width / adv
		if math.Abs(ratio-1) > 0.05 && ratio >= 0.05 && ratio <= 20 {
			scales[i] = ratio
			changed = true
		}
	}
	return scales, changed
}

// text 保留字形及逐字基线，外部字体优先复用同名内嵌资源
// 入参: mark PDF文字绘制信息
// 返回: error 错误信息
func (p *pdfImporter) text(mark pdfgo.TextMark) error {
	return p.textRender(mark, 1, false, false, false)
}

// textRender 转换单段PDF文字，支持非内嵌简单字体宽度缩放与竖排字体回退旋转
// 入参: mark PDF文字绘制信息, widthScale 显式Widths水平缩放比例, skipSplit 是否已拆分, forceOutlineFill 是否以矢量轮廓填充, verticalRotateText 是否使用CharDirection旋转
// 返回: error 错误信息
func (p *pdfImporter) textRender(mark pdfgo.TextMark, widthScale float64, skipSplit, forceOutlineFill, verticalRotateText bool) error {
	if mark.Font.Subtype == pdfgo.Name("Type3") && mark.Mode == 3 {
		return nil
	}
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "text blend mode " + string(mark.Style.BlendMode)}
	}
	if mark.Font.Subtype == pdfgo.Name("Type3") {
		visitor := p.visitor()
		for index := range mark.Glyphs {
			if err := p.reader.WalkType3Glyph(p.ctx, mark, index, visitor); err != nil {
				return err
			}
		}
		return p.ctx.Err()
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
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if mark.Clip == nil && p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			masked := mark
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Text(masked) })
		}
		return err
	}
	paintMode := mark.Mode % 4
	if (paintMode == 0 || paintMode == 2) && mark.Style.FillOverprint && codecpdf.OverprintNeedsSeparation(mark.Style.Fill) || (paintMode == 1 || paintMode == 2) && mark.Style.StrokeOverprint && codecpdf.OverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "text color separation overprint"}
	}
	if p.warning != nil && ((paintMode == 0 || paintMode == 2) && p.gradientError(mark.Style.Fill) != nil || (paintMode == 1 || paintMode == 2) && p.gradientError(mark.Style.Stroke) != nil) {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		gradient := mark
		return p.compositeRegion(nil, pdfCompositeNode{text: &gradient}, &pdfgo.ColorSpace{Model: "DeviceRGB"}, false)
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
	} else {
		imported = p.embeddedTextFont(font, mark.Glyphs)
		symbol := false
		if imported == nil {
			imported, err = p.symbolFont(font)
			if err != nil {
				return err
			}
			symbol = imported != nil
		}
		if imported != nil {
			reused, embedded = true, true
			mark.Glyphs = slices.Clone(mark.Glyphs)
			for index := range mark.Glyphs {
				glyph := &mark.Glyphs[index]
				if symbol {
					glyph.ID = imported.type1Glyphs[glyph.Name]
				} else {
					char, _ := utf8.DecodeRuneInString(glyph.Text)
					glyph.ID = imported.metrics.GlyphIndex(char)
				}
				glyph.HasID = true
			}
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
				p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded font lacks trailing side bearings; unused metrics reconstructed from glyph bounds"})
			}
			id, err = p.editor.addFontResource(imported.resource, imported.checksum)
			if err == nil {
				p.editor.fontMetrics[id] = imported.metrics
			}
		} else {
			if font.Recovered {
				if err := p.preservePDFObject(p.ctx, "Font", font.Dictionary); err != nil {
					return err
				}
			}
			family, bold, italic, descErr := p.externalFontDescriptor(font)
			if descErr != nil {
				return descErr
			}
			id, err = p.editor.addExternalFont(font.Name, family, "", bold, italic)
		}
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.fontIDs[font] = id
	}
	if embedded && imported.repairLimit != 0 {
		for _, glyph := range mark.Glyphs {
			if glyph.HasID && glyph.ID >= imported.repairLimit {
				if p.warning == nil {
					return &pdfgo.UnsupportedError{Feature: "embedded font glyph with missing side bearing"}
				}
				if p.fontWarnings == nil {
					p.fontWarnings = make(map[string]bool)
				}
				msg := fmt.Sprintf("PDF embedded font %s uses glyph with reconstructed side bearing", font.Name)
				if !p.fontWarnings[msg] {
					p.fontWarnings[msg] = true
					p.warning(pdfgo.Diagnostic{Message: msg})
				}
				break
			}
		}
	}
	fill, stroke, visible := paintMode == 0 || paintMode == 2, paintMode == 1 || paintMode == 2, true
	var vertMap map[uint16]uint16
	if !embedded && !skipSplit && mark.Replacement == nil && len(mark.Glyphs) > 0 {
		if font.Vertical && (paintMode != 3 || mark.Clip != nil) {
			fbMetrics, fbVert := p.fallbackExternalFont(id)
			if fbOutline, ok := fbMetrics.(FontOutlines); ok && fbOutline != nil {
				if !stroke && (fill || mark.Clip != nil) {
					flags := make([]bool, len(mark.Glyphs))
					anyFlag, allFlag := false, true
					for i, glyph := range mark.Glyphs {
						if utf8.RuneCountInString(glyph.Text) == 1 {
							ch, _ := utf8.DecodeRuneInString(glyph.Text)
							baseGID, sub := pdfVerticalFallbackRune(fbMetrics, ch)
							if sub || fbVert[baseGID] != 0 || baseGID != 0 && pdfVerticalRotateRune(ch) {
								flags[i] = true
							}
						}
						anyFlag = anyFlag || flags[i]
						allFlag = allFlag && flags[i]
					}
					if allFlag {
						forceOutlineFill = true
					} else if anyFlag {
						var clipEntry pdfClipTextEntry
						for s := 0; s < len(mark.Glyphs); {
							e := s + 1
							for e < len(mark.Glyphs) && flags[e] == flags[s] {
								e++
							}
							sub := mark
							sub.Glyphs, sub.Positions = mark.Glyphs[s:e], mark.Positions[s:e]
							if mark.Clip != nil {
								sub.Clip = new(pdfgo.TextClip)
							}
							if err := p.textRender(sub, 1, true, flags[s], false); err != nil {
								return err
							}
							if sub.Clip != nil {
								part := p.clipTexts[sub.Clip]
								clipEntry.texts = append(clipEntry.texts, part.texts...)
								clipEntry.paths = append(clipEntry.paths, part.paths...)
								delete(p.clipTexts, sub.Clip)
							}
							s = e
						}
						if mark.Clip != nil {
							if p.clipTexts == nil {
								p.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
							}
							p.clipTexts[mark.Clip] = clipEntry
						}
						return nil
					}
				}
			} else {
				flags := make([]bool, len(mark.Glyphs))
				anyFlag, allFlag := false, true
				for i, glyph := range mark.Glyphs {
					if utf8.RuneCountInString(glyph.Text) == 1 {
						ch, _ := utf8.DecodeRuneInString(glyph.Text)
						flags[i] = pdfVerticalRotateRune(ch)
					}
					anyFlag = anyFlag || flags[i]
					allFlag = allFlag && flags[i]
				}
				if allFlag {
					verticalRotateText = true
				} else if anyFlag {
					var clipEntry pdfClipTextEntry
					for s := 0; s < len(mark.Glyphs); {
						e := s + 1
						for e < len(mark.Glyphs) && flags[e] == flags[s] {
							e++
						}
						sub := mark
						sub.Glyphs, sub.Positions = mark.Glyphs[s:e], mark.Positions[s:e]
						if mark.Clip != nil {
							sub.Clip = new(pdfgo.TextClip)
						}
						if err := p.textRender(sub, 1, true, false, flags[s]); err != nil {
							return err
						}
						if sub.Clip != nil {
							part := p.clipTexts[sub.Clip]
							clipEntry.texts = append(clipEntry.texts, part.texts...)
							clipEntry.paths = append(clipEntry.paths, part.paths...)
							delete(p.clipTexts, sub.Clip)
						}
						s = e
					}
					if mark.Clip != nil {
						if p.clipTexts == nil {
							p.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
						}
						p.clipTexts[mark.Clip] = clipEntry
					}
					return nil
				}
			}
		} else if !font.Vertical && mark.Clip == nil {
			fbMetrics, _ := p.fallbackExternalFont(id)
			if scales, changed := pdfSimpleFontWidthScales(font, mark.Glyphs, fbMetrics); changed {
				same := true
				firstScale := scales[0]
				for i := 1; i < len(scales); i++ {
					if math.Abs(scales[i]-firstScale) > 1e-4 {
						same = false
						break
					}
				}
				if same {
					widthScale = firstScale
				} else {
					for s := 0; s < len(mark.Glyphs); {
						e := s + 1
						for e < len(mark.Glyphs) && math.Abs(scales[e]-scales[s]) <= 1e-4 {
							e++
						}
						sub := mark
						sub.Glyphs, sub.Positions = mark.Glyphs[s:e], mark.Positions[s:e]
						if err := p.textRender(sub, scales[s], true, false, false); err != nil {
							return err
						}
						s = e
					}
					return nil
				}
			}
		}
	}
	var metrics FontMetrics
	var outline FontOutlines
	if embedded {
		metrics = imported.metrics
		outline = metrics.(FontOutlines)
	}
	needOutline := stroke
	for _, glyph := range mark.Glyphs {
		needOutline = needOutline || glyph.Name == ".notdef"
	}
	verticalFallbackOutline := font.Vertical && !embedded && (stroke || forceOutlineFill)
	if verticalFallbackOutline {
		fbMetrics, fbVert := p.fallbackExternalFont(id)
		if provider, ok := fbMetrics.(FontOutlines); ok && provider != nil {
			metrics, outline, vertMap = fbMetrics, provider, fbVert
		}
	}
	if needOutline && !embedded && outline == nil {
		if resolved, err := p.editor.editorFont(id); err == nil {
			if provider, ok := resolved.(FontOutlines); ok {
				metrics, outline = resolved, provider
				for _, glyph := range mark.Glyphs {
					if glyph.Name == ".notdef" {
						continue
					}
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
	outlineFill := forceOutlineFill && (fill || mark.Clip != nil) && !stroke && outline != nil
	outlineDraw := outlineStroke || outlineFill
	var glyphPaths []pdfgo.Path
	const unit = 25.4 / 72
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, 0, 0})
	var extent pdfPointBounds
	object := TextObject{Font: id, Size: mark.Size * unit, HScale: mark.HorizontalScale * widthScale}
	if verticalRotateText {
		object.CharDirection = 90
	}
	mergeEmptyCluster := false
	if embedded && mark.Replacement == nil && len(mark.Glyphs) > 1 {
		hasEmpty, hasText := false, false
		for _, g := range mark.Glyphs {
			if pdfGlyphExplicitEmpty(font, g) {
				hasEmpty = true
			} else if g.Text != "" {
				hasText = true
			}
		}
		mergeEmptyCluster = hasEmpty && hasText
	}
	items := make([]pdfTextGlyphItem, 0, len(mark.Glyphs))
	var pendingEmpty []pdfEmptyLigatureGlyph
	if mark.Replacement != nil {
		object.TextCode = make([]TextCode, 0, len(mark.Glyphs))
		if embedded {
			object.CGTransform = make([]CGTransform, 0, len(mark.Glyphs))
		}
	}
	position := 0
	for n, glyph := range mark.Glyphs {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		gid := glyph.ID
		undefined := glyph.Name == ".notdef"
		explicitEmpty := mergeEmptyCluster && pdfGlyphExplicitEmpty(font, glyph)
		if !embedded && undefined {
			gid = 0
		}
		if embedded && !reused && font.ProgramType == "FontFile" {
			gid = imported.type1Glyphs[glyph.Name]
			glyph.HasID = true
		}
		if !explicitEmpty && glyph.Text == "" && (glyph.HasID || undefined) {
			glyph.Text = "\u00a4"
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
		rotateOutline := false
		if outlineDraw && !embedded && !undefined {
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			if verticalFallbackOutline {
				baseGID, _ := pdfVerticalFallbackRune(metrics, char)
				if vgid := vertMap[baseGID]; vgid != 0 {
					gid = vgid
				} else {
					gid = baseGID
					rotateOutline = baseGID != 0 && pdfVerticalRotateRune(char)
				}
			} else {
				gid = metrics.GlyphIndex(char)
			}
		}
		origin := mark.Positions[n]
		itemX, itemY := origin.X*unit, -origin.Y*unit
		if verticalRotateText {
			cx, cy := 0.5*object.Size, -0.38*object.Size
			itemX += cx + cy
			itemY += cy - cx
		}
		if explicitEmpty {
			if len(items) > 0 {
				last := &items[len(items)-1]
				if len(last.gids) == 0 {
					last.gids = []uint16{last.gid}
					last.xs = []float64{last.x}
					last.ys = []float64{last.y}
				}
				last.gids = append(last.gids, gid)
				last.xs = append(last.xs, itemX)
				last.ys = append(last.ys, itemY)
			} else {
				pendingEmpty = append(pendingEmpty, pdfEmptyLigatureGlyph{x: itemX, y: itemY, gid: gid})
			}
		} else {
			escaped := escapeOFDText(glyph.Text)
			count := utf8.RuneCountInString(glyph.Text)
			hasTransform := embedded || undefined
			if mark.Replacement != nil {
				object.TextCode = append(object.TextCode, TextCode{X: pdfNumbers(itemX), Y: pdfNumbers(itemY), Value: escaped})
				if hasTransform {
					object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: position, CodeCount: count, GlyphCount: 1, Glyphs: strconv.Itoa(int(gid))})
				}
			} else if len(pendingEmpty) > 0 {
				gids := make([]uint16, 0, len(pendingEmpty)+1)
				xs := make([]float64, 0, len(pendingEmpty)+1)
				ys := make([]float64, 0, len(pendingEmpty)+1)
				for _, empty := range pendingEmpty {
					gids = append(gids, empty.gid)
					xs = append(xs, empty.x)
					ys = append(ys, empty.y)
				}
				gids = append(gids, gid)
				xs = append(xs, itemX)
				ys = append(ys, itemY)
				items = append(items, pdfTextGlyphItem{x: xs[0], y: ys[0], text: escaped, runes: count, hasTransform: true, gid: gids[0], gids: gids, xs: xs, ys: ys})
				pendingEmpty = pendingEmpty[:0]
			} else {
				items = append(items, pdfTextGlyphItem{x: itemX, y: itemY, text: escaped, runes: count, hasTransform: hasTransform, gid: gid})
			}
			position += count
		}
		if embedded || outlineDraw || undefined && outline != nil {
			var path GeometryPath
			var bounds Box
			var err error
			if provider, ok := outline.(FontGlyphBounds); ok && !outlineDraw {
				bounds, err = provider.GlyphBounds(gid, object.Size)
			} else {
				path, err = outline.GlyphOutline(gid, object.Size)
				if err == nil && rotateOutline {
					path, err = pdfRotateVerticalGlyph(path, metrics, gid, object.Size)
				}
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
					msg := fmt.Sprintf("PDF font %s has invalid hinting instructions; design outlines and original font retained", font.Name)
					if !p.fontWarnings[id] && !p.fontWarnings[msg] {
						p.warning(pdfgo.Diagnostic{Message: msg})
						p.fontWarnings[id] = true
						p.fontWarnings[msg] = true
					}
				}
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				extent.add(m.Apply(pdfgo.Point{X: origin.X*unit + point.X*mark.HorizontalScale*widthScale, Y: -origin.Y*unit + point.Y}))
			}
			if outlineDraw {
				matrix := mark.Matrix.Mul(pdfgo.Matrix{mark.HorizontalScale * widthScale / unit, 0, 0, -1 / unit, origin.X, origin.Y})
				glyphPath, err := pdfGlyphPath(path, matrix)
				if err != nil {
					return err
				}
				glyphPaths = append(glyphPaths, glyphPath)
			}
		} else {
			width := glyph.Width / 1000 * object.Size * mark.HorizontalScale
			if font.Vertical {
				if fbMetrics, _ := p.fallbackExternalFont(id); fbMetrics != nil && fbMetrics.UnitsPerEm() > 0 {
					if utf8.RuneCountInString(glyph.Text) == 1 {
						char, _ := utf8.DecodeRuneInString(glyph.Text)
						if fbGID, _ := pdfVerticalFallbackRune(fbMetrics, char); fbGID != 0 {
							if fbWidth := float64(fbMetrics.GlyphAdvance(fbGID)) / float64(fbMetrics.UnitsPerEm()) * object.Size * mark.HorizontalScale; fbWidth > width {
								width = fbWidth
							}
						}
					}
				}
			}
			bounds := Box{X: 0, Y: -object.Size, W: width, H: object.Size * 1.25}
			if undefined {
				b := font.BoundingBox
				if b == nil {
					b = font.FallbackBoundingBox
				}
				if b != nil {
					bounds = Box{X: b.XMin / 1000 * object.Size * mark.HorizontalScale, Y: -b.YMax / 1000 * object.Size, W: (b.XMax - b.XMin) / 1000 * object.Size * mark.HorizontalScale, H: (b.YMax - b.YMin) / 1000 * object.Size}
				}
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				extent.add(m.Apply(pdfgo.Point{X: origin.X*unit + point.X, Y: -origin.Y*unit + point.Y}))
			}
		}
	}
	if mark.Replacement != nil {
		pdfApplyReplacement(&object, mark.Replacement.Text, mark.Positions)
	} else {
		pdfCompactTextRuns(&object, items)
	}
	box := extent.box()
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
		box = Box{X: box.X - margin, Y: box.Y - margin, W: box.W + 2*margin, H: box.H + 2*margin}
		object.LineWidth = lineWidth / scale
		object.LineWidthSet = object.LineWidth == 0
		object.Cap = pdfLineCapName(mark.Style.Cap)
		object.Join = pdfLineJoinName(mark.Style.Join)
		object.MiterLimit = mark.Style.MiterLimit
		if len(mark.Style.Dash) > 0 {
			strokeScale := math.Hypot(p.matrix[0], p.matrix[1]) * sx / scale
			object.DashPattern = pdfDashPattern(mark.Style.Dash, strokeScale)
			phase := mark.Style.DashPhase * strokeScale
			object.DashOffset = &phase
		}
	}
	object.Boundary = pdfBoundary(box)
	if stroke && !outlineStroke {
		strokePaint, err := p.paintColor(mark.Style.Stroke, box, mark.Style.RenderingIntent)
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
	object.FillColor, err = p.color(mark.Style.Fill, mark.Style.RenderingIntent)
	if err != nil {
		return err
	}
	if fill && !outlineDraw {
		object.FillColor, err = p.paintColor(mark.Style.Fill, box, mark.Style.RenderingIntent)
		if err != nil {
			return err
		}
	}
	object.Clips, err = p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	if mark.Clip != nil {
		var entry pdfClipTextEntry
		if outlineFill {
			for _, path := range glyphPaths {
				if len(path.Segments) == 0 {
					continue
				}
				encoded, clipErr := p.clipPath(path)
				if clipErr != nil {
					return clipErr
				}
				entry.paths = append(entry.paths, encoded.path[0])
			}
		} else {
			clipObject := object
			clipObject.Clips = nil
			clipObject.Fill, clipObject.Stroke = new(bool), new(bool)
			*clipObject.Fill = true
			*clipObject.Stroke = false
			clipObject.Visible = nil
			entry.texts = []TextObject{clipObject}
		}
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
		}
		p.clipTexts[mark.Clip] = entry
	}
	if outlineDraw && (fill || outlineStroke) {
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
			if err := p.appendPath(pdfgo.PathMark{Sequence: mark.Sequence, Path: path, Style: mark.Style, Fill: fill, Stroke: outlineStroke, StrokeMatrix: matrix}); err != nil {
				return err
			}
		}
		object.Fill, object.Stroke = new(bool), new(bool)
	}
	p.appendOriginObject(GraphicObject{Type: "TextObject", TextObject: object}, p.textOrigin(mark))
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
	var clips pdfClipTextEntry
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
			part := p.clipTexts[glyph.Clip]
			clips.texts = append(clips.texts, part.texts...)
			clips.paths = append(clips.paths, part.paths...)
			delete(p.clipTexts, glyph.Clip)
		}
	}
	if mark.Clip != nil {
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
		}
		p.clipTexts[mark.Clip] = clips
	}
	return nil
}

// disjointText 检查实际字形轮廓是否互不相交，允许无重叠挖空文字直接保留
// 入参: node 隐式文字组
// 返回: bool 是否互不相交, error 轮廓编译错误
func (p *pdfImporter) disjointText(node pdfCompositeNode) (bool, error) {
	cache := p.compositingCache()
	if result, found := cache.textDisjoint[node.group]; found {
		return result, nil
	}
	geometry, err := p.renderer.Geometry()
	if err != nil {
		return false, err
	}
	var inverse pdfgo.Matrix
	var boxes []Box
	for _, child := range node.children {
		mark := child.text
		if mark == nil {
			mark = child.glyph
		}
		if mark == nil || len(mark.Glyphs) != 1 {
			return false, nil
		}
		if inverse == (pdfgo.Matrix{}) {
			var ok bool
			inverse, ok = p.matrix.Mul(mark.Matrix).Inverse()
			if !ok {
				return false, nil
			}
		}
		scene, _, err := p.compileGroup(func(local *pdfImporter) error {
			if child.group != nil {
				return child.coverage(local, false)
			}
			_, fill, stroke := child.style()
			if fill {
				if err := child.coverage(local, false); err != nil {
					return err
				}
			}
			if stroke {
				return child.coverage(local, true)
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if scene == nil {
			continue
		}
		var box Box
		for _, command := range scene.Commands {
			path, err := rasterCommandGeometry(geometry, command)
			if err != nil {
				return false, err
			}
			path, err = geometry.Transform(path, MatrixFromValues([6]float64(inverse)))
			if err != nil {
				return false, err
			}
			bounds, err := geometry.Bounds(path)
			if err != nil {
				return false, err
			}
			box = unionTextBox(box, bounds)
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		for _, previous := range boxes {
			if box.X < previous.X+previous.W && previous.X < box.X+box.W && box.Y < previous.Y+previous.H && previous.Y < box.Y+box.H {
				if cache.textDisjoint == nil {
					cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
				}
				cache.textDisjoint[node.group] = false
				return false, nil
			}
		}
		boxes = append(boxes, box)
	}
	if cache.textDisjoint == nil {
		cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
	}
	cache.textDisjoint[node.group] = true
	return true, nil
}

// shapePDFText 使用导入字体整形注释文字，将毫米坐标约定转换为PDF文字空间方向
// 入参: ctx 取消上下文, source 源字体, text 单行原文, rightToLeft 段落方向
// 返回: []pdfgo.ShapedGlyph 千分之一字号的定位字形, error 字体、整形或取消错误
func (p *pdfImporter) shapePDFText(ctx context.Context, source *pdfgo.Font, text string, rightToLeft bool) ([]pdfgo.ShapedGlyph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(source.Program) == 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "annotation shaping without embedded font"}
	}
	font, err := p.importedFont(source)
	if err != nil {
		return nil, err
	}
	shaper, ok := font.metrics.(FontShaperOptions)
	if !ok {
		return nil, &pdfgo.UnsupportedError{Feature: "annotation text shaping backend"}
	}
	options := TextShapeOptions{Bidi: true}
	if rightToLeft {
		options.Direction = "rtl"
	}
	glyphs, err := shaper.ShapeTextWithOptions(text, 1000, options)
	if err != nil {
		return nil, err
	}
	result := make([]pdfgo.ShapedGlyph, len(glyphs))
	units := float64(font.metrics.UnitsPerEm())
	if units <= 0 {
		return nil, fmt.Errorf("invalid annotation font units")
	}
	for i, glyph := range glyphs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result[i] = pdfgo.ShapedGlyph{ID: glyph.Glyph, Cluster: glyph.Cluster, VisualOrder: glyph.VisualOrder, X: glyph.X, Y: -glyph.Y, Advance: glyph.Advance, Width: float64(font.metrics.GlyphAdvance(glyph.Glyph)) * 1000 / units}
	}
	return result, ctx.Err()
}

// embeddedTextFont 匹配同名内嵌字体，仅复用唯一且覆盖全部字符的字形程序
// 入参: source 外部字体, glyphs 当前文字字形
// 返回: *pdfImportedFont 可复用字体，无可靠匹配时为空
func (p *pdfImporter) embeddedTextFont(source *pdfgo.Font, glyphs []pdfgo.Glyph) *pdfImportedFont {
	name := codecpdf.EmbeddedFontName(source.Name)
	if name == "" || len(glyphs) == 0 {
		return nil
	}
	var matched *pdfImportedFont
	for _, imported := range p.fonts {
		if codecpdf.EmbeddedFontName(imported.resource.Font.FontName) != name {
			continue
		}
		if matched != nil && matched.checksum != imported.checksum {
			return nil
		}
		matched = imported
	}
	if matched == nil {
		return nil
	}
	for _, glyph := range glyphs {
		if glyph.Name == ".notdef" || utf8.RuneCountInString(glyph.Text) != 1 {
			return nil
		}
		char, _ := utf8.DecodeRuneInString(glyph.Text)
		id := matched.metrics.GlyphIndex(char)
		if id == 0 || id >= matched.metrics.NumGlyphs() || matched.repairLimit != 0 && id >= matched.repairLimit {
			return nil
		}
	}
	return matched
}

// importedFont 按原PDF字体复用包装和解析结果，失败结果不进入缓存
// 入参: source 源字体
// 返回: *pdfImportedFont 只读导入字体, error 字体程序或后端错误
func (p *pdfImporter) importedFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	if cached := p.fonts[source]; cached != nil {
		return cached, nil
	}
	var type1 *basefont.Type1Program
	result := &pdfImportedFont{}
	programSource := *source
	programRecovered := false
	if p.warning != nil {
		var err error
		if source.ProgramType == "FontFile" {
			programSource.Program, programRecovered, err = basefont.NormalizePDFType1(source.Program)
		} else {
			programSource.Program, programRecovered, err = basefont.NormalizePDFProgram(source.Program)
		}
		if err != nil {
			return nil, fmt.Errorf("PDF font %s container: %w", source.Name, err)
		}
	}
	if source.ProgramType == "FontFile" {
		parsed, err := basefont.ParseType1Program(programSource.Program)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
		}
		type1 = &parsed
		result.type1Glyphs = parsed.GlyphIDs()
	}
	program, repair, err := codecpdf.FontProgram(&programSource, type1)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
	}
	if repair.Locations && p.warning == nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, basefont.ErrTTInvalidLocations)
	}
	if p.editor.backends.FontResources == nil {
		return nil, fmt.Errorf("PDF font %s resource: font resources: %w", source.Name, ErrBackendUnavailable)
	}
	if p.editor.backends.Fonts == nil {
		return nil, fmt.Errorf("PDF font backend unavailable")
	}
	key := pdfFontProgramKey{checksum: sha256.Sum256(program), name: source.Name}
	if cached := p.fontPrograms[key]; cached != nil {
		result.resource, result.metrics, result.checksum = cached.resource, cached.metrics, cached.checksum
	} else {
		result.resource, err = p.editor.backends.FontResources.OpenFontResource(FontFile{Name: source.Name + ".ttf", Data: program}, 0)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s resource: %w", source.Name, err)
		}
		result.metrics, err = p.editor.backends.Fonts.OpenFont(result.resource.Data)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s metrics: %w", source.Name, err)
		}
		if _, ok := result.metrics.(FontOutlines); !ok {
			return nil, fmt.Errorf("PDF font backend does not provide glyph outlines")
		}
		result.checksum = sha256.Sum256(result.resource.Data)
	}
	result.repairLimit = repair.MetricsLimit
	if source.Recovered || programRecovered {
		if err := p.preservePDFObject(p.ctx, "Font", source.Dictionary); err != nil {
			return nil, err
		}
	}
	if programRecovered {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s container and metrics normalized from embedded program; original glyph IDs retained", source.Name)})
	}
	if repair.Locations {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s has invalid glyph locations; index rebuilt with original glyph IDs", source.Name)})
	}
	if p.fonts == nil {
		p.fonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if p.fontPrograms == nil {
		p.fontPrograms = make(map[pdfFontProgramKey]*pdfImportedFont)
	}
	p.fontPrograms[key] = result
	p.fonts[source] = result
	return result, nil
}

// symbolFont 加载标准符号字体，保留原字形编号和源字形名称
// 入参: source 未嵌入的PDF字体
// 返回: *pdfImportedFont 可用的符号字体，无匹配时为空, error 字体或取消错误
func (p *pdfImporter) symbolFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	name := codecpdf.EmbeddedFontName(source.Name)
	if source.Subtype != "Type1" || name != "Symbol" && name != "ZapfDingbats" {
		return nil, nil
	}
	if imported, ok := p.symbolFonts[source]; ok {
		return imported, nil
	}
	if p.symbolFonts == nil {
		p.symbolFonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	definition := &Font{ID: name, FontName: name, FamilyName: name}
	reader := &Reader{doc: &Document{}, fontResourcesRead: true, fontCache: map[string]*Font{name: definition}}
	resolved, err := p.editor.newRenderer(reader).ResolveFont(name, true)
	if err != nil || len(resolved.Data) == 0 || p.editor.backends.FontResources == nil {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	program, names, err := basefont.SymbolProgram(p.ctx, resolved.Data, name)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	resource, err := p.editor.backends.FontResources.OpenFontResource(FontFile{Name: name + ".ttf", Data: program}, 0)
	if err != nil {
		return nil, err
	}
	metrics, err := p.editor.backends.Fonts.OpenFont(resource.Data)
	if err != nil {
		return nil, err
	}
	if _, ok := metrics.(FontOutlines); !ok {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	result := &pdfImportedFont{resource: resource, checksum: sha256.Sum256(resource.Data), metrics: metrics, type1Glyphs: names}
	p.symbolFonts[source] = result
	return result, nil
}

// textOrigin 获取合并文字的完整来源，普通文字沿用原始区段
// 入参: mark 文字图元
// 返回: pdfObjectOrigin 来源区段
func (p *pdfImporter) textOrigin(mark pdfgo.TextMark) pdfObjectOrigin {
	if origin, ok := p.textOrigins[mark.Replacement]; ok {
		return origin
	}
	return pdfObjectOrigin{sequence: mark.Sequence}
}

// externalFontDescriptor 从PDF字体名称与字体描述符提取家族名称及粗斜体样式
// 入参: font PDF字体定义
// 返回: string 字体家族名称, bool 是否粗体, bool 是否斜体, error 取消错误
func (p *pdfImporter) externalFontDescriptor(font *pdfgo.Font) (string, bool, bool, error) {
	if err := p.ctx.Err(); err != nil {
		return "", false, false, err
	}
	cleanName := codecpdf.EmbeddedFontName(strings.TrimSpace(font.Name))
	family, bold, italic := basefont.SplitNameStyle(cleanName)
	if p.reader == nil || font.Dictionary == nil {
		return family, bold, italic, nil
	}
	dict := font.Dictionary
	if font.Subtype == "Type0" {
		if resolved, err := p.reader.Resolve(dict["DescendantFonts"]); err == nil {
			if list, ok := resolved.(pdfgo.Array); ok && len(list) > 0 {
				if descendant, err := p.reader.Resolve(list[0]); err == nil {
					if descendantDict, ok := descendant.(pdfgo.Dictionary); ok {
						dict = descendantDict
					}
				}
			}
		}
	}
	resolved, err := p.reader.Resolve(dict["FontDescriptor"])
	if err != nil {
		if err := p.ctx.Err(); err != nil {
			return "", false, false, err
		}
		return family, bold, italic, nil
	}
	descriptor, ok := resolved.(pdfgo.Dictionary)
	if !ok {
		return family, bold, italic, nil
	}
	if descriptor["FontName"] != nil {
		if value, err := p.reader.Resolve(descriptor["FontName"]); err == nil {
			if name, ok := value.(pdfgo.Name); ok {
				descFamily, descBold, descItalic := basefont.SplitNameStyle(codecpdf.EmbeddedFontName(strings.TrimSpace(string(name))))
				bold = bold || descBold
				italic = italic || descItalic
				if family == "" {
					family = descFamily
				}
			}
		}
	}
	if descriptor["FontFamily"] != nil {
		if text, err := p.reader.ReadAnnotationTextContext(p.ctx, pdfgo.Annotation{Dictionary: descriptor}, "FontFamily", nil); err == nil {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				family = trimmed
			}
		} else if err := p.ctx.Err(); err != nil {
			return "", false, false, err
		}
	}
	if descriptor["FontWeight"] != nil {
		if value, err := p.reader.Resolve(descriptor["FontWeight"]); err == nil {
			switch weight := value.(type) {
			case pdfgo.Integer:
				if weight >= 700 {
					bold = true
				}
			case pdfgo.Real:
				if finite(float64(weight)) && float64(weight) >= 700 {
					bold = true
				}
			}
		}
	}
	if descriptor["Flags"] != nil {
		if value, err := p.reader.Resolve(descriptor["Flags"]); err == nil {
			if flags, ok := value.(pdfgo.Integer); ok {
				if flags&(1<<6) != 0 {
					italic = true
				}
				if flags&(1<<18) != 0 {
					bold = true
				}
			}
		}
	}
	if descriptor["ItalicAngle"] != nil {
		if value, err := p.reader.Resolve(descriptor["ItalicAngle"]); err == nil {
			switch angle := value.(type) {
			case pdfgo.Integer:
				if angle != 0 {
					italic = true
				}
			case pdfgo.Real:
				if finite(float64(angle)) && float64(angle) != 0 {
					italic = true
				}
			}
		}
	}
	if err := p.ctx.Err(); err != nil {
		return "", false, false, err
	}
	return family, bold, italic, nil
}
