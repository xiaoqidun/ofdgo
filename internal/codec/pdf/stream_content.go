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

package pdf

import (
	"bytes"
	"cmp"
	"image"
	"image/color"
	"math"
	"slices"
	"strconv"

	"github.com/tdewolff/canvas"
	ctext "github.com/tdewolff/canvas/text"
	"github.com/xiaoqidun/pdfgo"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"
)

// ContentResourceSink 为内容流构建器提供线程安全的全局PDF资源登记回调
type ContentResourceSink interface {
	FontResource(font *canvas.Font, vertical bool) (pdfgo.Name, pdfgo.Reference, *FontEntry)
	OpacityGS(alpha uint8) (pdfgo.Name, pdfgo.Reference)
	ShadingPattern(gradient canvas.Gradient, m canvas.Matrix) (pdfgo.Name, pdfgo.Reference)
}

// ContentResources 记录单个页面或Form内容流实际引用的PDF资源集合
type ContentResources struct {
	Fonts      map[pdfgo.Name]pdfgo.Reference
	XObjects   map[pdfgo.Name]pdfgo.Reference
	ExtGStates map[pdfgo.Name]pdfgo.Reference
	Patterns   map[pdfgo.Name]pdfgo.Reference
}

// ContentBuilder 单遍构建PDF页面或Form内容流，直接完成文字合并、伪粗体、ActualText与矢量裁剪
type ContentBuilder struct {
	sink           ContentResourceSink
	buf            []byte
	resources      ContentResources
	state          contentGState
	inText         bool
	isolateNext    bool
	hasSavedState  bool
	savedState     contentGState
	spanStarts     []int
	localGlyphMaps map[*FontEntry]map[uint16]uint16
}

// Dictionary 将资源集合转换为标准PDF Resources字典
// 返回: pdfgo.Dictionary 资源字典
func (r *ContentResources) Dictionary() pdfgo.Dictionary {
	dict := make(pdfgo.Dictionary)
	if len(r.Fonts) > 0 {
		sub := make(pdfgo.Dictionary, len(r.Fonts))
		for k, v := range r.Fonts {
			sub[k] = v
		}
		dict["Font"] = sub
	}
	if len(r.XObjects) > 0 {
		sub := make(pdfgo.Dictionary, len(r.XObjects))
		for k, v := range r.XObjects {
			sub[k] = v
		}
		dict["XObject"] = sub
	}
	if len(r.ExtGStates) > 0 {
		sub := make(pdfgo.Dictionary, len(r.ExtGStates))
		for k, v := range r.ExtGStates {
			sub[k] = v
		}
		dict["ExtGState"] = sub
	}
	if len(r.Patterns) > 0 {
		sub := make(pdfgo.Dictionary, len(r.Patterns))
		for k, v := range r.Patterns {
			sub[k] = v
		}
		dict["Pattern"] = sub
	}
	return dict
}

// Key 生成确定性资源签名，用于跨页共享完全相同的Resources间接对象
// 返回: string 资源签名字符串
func (r *ContentResources) Key() string {
	var buf bytes.Buffer
	writeMap := func(tag byte, m map[pdfgo.Name]pdfgo.Reference) {
		if len(m) == 0 {
			return
		}
		buf.WriteByte(tag)
		keys := make([]pdfgo.Name, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b pdfgo.Name) int { return cmp.Compare(a, b) })
		for _, k := range keys {
			buf.WriteString(string(k))
			buf.WriteByte('=')
			buf.WriteString(strconv.FormatInt(m[k].Number, 10))
			buf.WriteByte(';')
		}
	}
	writeMap('F', r.Fonts)
	writeMap('X', r.XObjects)
	writeMap('G', r.ExtGStates)
	writeMap('P', r.Patterns)
	return buf.String()
}

// NewContentBuilder 创建内容流构建器并初始化毫米到点的缩放矩阵
// 入参: sink 全局资源登记回调
// 返回: *ContentBuilder 内容流构建器
func NewContentBuilder(sink ContentResourceSink) *ContentBuilder {
	b := &ContentBuilder{
		sink: sink,
		buf:  make([]byte, 0, 1024),
		state: contentGState{
			alpha:          255,
			fill:           canvas.Paint{Color: canvas.Black},
			fillValid:      true,
			stroke:         canvas.Paint{Color: canvas.Black},
			strokeValid:    true,
			lineWidth:      1,
			lineCap:        0,
			lineJoin:       0,
			miterLimit:     10,
			dashes:         []float64{0},
			fontDirection:  ctext.LeftToRight,
			textPos:        canvas.Identity,
			textRenderMode: 0,
		},
	}
	b.buf = appendCompactFloatBytes(b.buf, PtPerMM, 7)
	b.buf = append(b.buf, " 0 0 "...)
	b.buf = appendCompactFloatBytes(b.buf, PtPerMM, 7)
	b.buf = append(b.buf, " 0 0 cm"...)
	return b
}

// Resources 返回当前内容流实际引用的资源集合
// 返回: *ContentResources 资源集合
func (b *ContentBuilder) Resources() *ContentResources {
	return &b.resources
}

// Bytes 结束未闭合文本对象并返回最终内容流字节
// 返回: []byte 内容流字节
func (b *ContentBuilder) Bytes() []byte {
	b.EndText()
	return b.buf
}

// SpanCount 返回当前已记录的文字span计数
// 返回: int span数量
func (b *ContentBuilder) SpanCount() int {
	return len(b.spanStarts)
}

// IsolateNextText 断开与前驱文字对象的BT合并并记录隔离前图形状态快照
func (b *ContentBuilder) IsolateNextText() {
	b.EndText()
	b.isolateNext = true
	if !b.hasSavedState {
		b.savedState = b.cloneState()
		b.hasSavedState = true
	}
}

// ClearTextIsolation 清除当前文字对象隔离快照
func (b *ContentBuilder) ClearTextIsolation() {
	b.isolateNext = false
	b.hasSavedState = false
}

// EndText 闭合当前活跃的PDF文字对象
func (b *ContentBuilder) EndText() {
	if !b.inText {
		return
	}
	b.buf = append(b.buf, " ET"...)
	b.inText = false
}

// RenderPath 按画布路径样式与变换矩阵直接写出紧凑PDF绘图指令
// 入参: path 几何路径, style 绘制样式, m 变换矩阵
func (b *ContentBuilder) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if path == nil || path.Empty() {
		return
	}
	b.EndText()
	strokeUnsupported := false
	if _, ok := style.StrokeJoiner.(canvas.ArcsJoiner); ok {
		strokeUnsupported = true
	} else if miter, ok := style.StrokeJoiner.(canvas.MiterJoiner); ok {
		if math.IsNaN(miter.Limit) {
			strokeUnsupported = true
		} else if _, ok := miter.GapJoiner.(canvas.BevelJoiner); !ok {
			strokeUnsupported = true
		}
	}
	if !strokeUnsupported {
		if m.IsSimilarity() {
			scale := math.Sqrt(math.Abs(m.Det()))
			style.StrokeWidth *= scale
			style.DashOffset, style.Dashes = canvas.ScaleDash(style.StrokeWidth, style.DashOffset, style.Dashes)
		} else {
			strokeUnsupported = true
		}
	}
	if !style.HasStroke() || !strokeUnsupported {
		if style.HasFill() && !style.HasStroke() {
			b.setFill(style.Fill, m)
			b.appendPathAndFill(path, m, style.FillRule == canvas.EvenOdd)
		} else if !style.HasFill() && style.HasStroke() {
			b.setStroke(style.Stroke, m)
			b.setStrokeStyle(style)
			closed := b.appendPath(path, m, true, true)
			if closed {
				b.buf = append(b.buf, " s"...)
			} else {
				b.buf = append(b.buf, " S"...)
			}
		} else if style.HasFill() && style.HasStroke() {
			sameAlpha := style.Fill.IsColor() && style.Stroke.IsColor() && style.Fill.Color.A == style.Stroke.Color.A
			if sameAlpha {
				b.setFill(style.Fill, m)
				b.setStroke(style.Stroke, m)
				b.setStrokeStyle(style)
				closed := b.appendPath(path, m, true, true)
				if closed {
					b.buf = append(b.buf, " b"...)
				} else {
					b.buf = append(b.buf, " B"...)
				}
				if style.FillRule == canvas.EvenOdd {
					b.buf = append(b.buf, '*')
				}
			} else {
				b.setFill(style.Fill, m)
				b.appendPathAndFill(path, m, style.FillRule == canvas.EvenOdd)
				b.setStroke(style.Stroke, m)
				b.setStrokeStyle(style)
				closed := b.appendPath(path, m, true, true)
				if closed {
					b.buf = append(b.buf, " s"...)
				} else {
					b.buf = append(b.buf, " S"...)
				}
			}
		}
		return
	}
	if style.HasFill() {
		b.setFill(style.Fill, m)
		b.appendPathAndFill(path, m, style.FillRule == canvas.EvenOdd)
	}
	stroked := path
	if style.IsDashed() {
		stroked = stroked.Dash(style.DashOffset, style.Dashes...)
	}
	stroked = stroked.Stroke(style.StrokeWidth, style.StrokeCapper, style.StrokeJoiner, canvas.Tolerance)
	b.setFill(style.Stroke, m)
	b.appendPathAndFill(stroked, m, false)
}

// RenderText 写出画布文字，自动合并连续BT块并支持文字装饰与嵌入式对象
// 入参: text 画布文字, m 变换矩阵, onSubCanvas 子画布回调，可为空
func (b *ContentBuilder) RenderText(text *canvas.Text, m canvas.Matrix, onSubCanvas func(c *canvas.Canvas, view canvas.Matrix)) {
	text.RenderDecorationsTo(contentPathAdapter{b}, m, 0)
	text.WalkSpans(func(x, y float64, span canvas.TextSpan) {
		if span.IsText() {
			if b.isolateNext {
				b.EndText()
				b.isolateNext = false
			}
			spanStart := len(b.buf)
			b.spanStarts = append(b.spanStarts, spanStart)
			if !b.inText {
				b.buf = append(b.buf, " BT"...)
				b.state.textPos = canvas.Identity
				b.inText = true
			}
			b.setFill(span.Face.Fill, m)
			b.setFont(span.Face.Font, span.Face.Size, span.Direction)
			b.setTextPosition(m.Translate(x, y).Shear(span.Face.FauxItalic, 0))
			if span.Face.FauxBold > 0 {
				b.setTextRenderMode(2)
				b.setStroke(span.Face.Fill, m)
				b.setLineWidth(span.Face.FauxBold * span.Face.Size * 2)
			} else {
				b.setTextRenderMode(0)
			}
			b.writeGlyphs(text.WritingMode, span.Glyphs)
			return
		}
		if onSubCanvas != nil {
			for _, obj := range span.Objects {
				onSubCanvas(obj.Canvas, m.Mul(obj.View(x, y, span.Face)))
			}
		}
	})
}

// WrapFauxBold 为指定起始span之后的文字区间原地注入Tr=2填充并细描边状态
// 入参: firstSpan 起始span序号, lineWidth 描边线宽, fillColor 填充颜色
func (b *ContentBuilder) WrapFauxBold(firstSpan int, lineWidth float64, fillColor color.RGBA) {
	if firstSpan < 0 || firstSpan >= len(b.spanStarts) || lineWidth <= 0 {
		return
	}
	b.EndText()
	start := b.spanStarts[firstSpan]
	if start < 0 || start >= len(b.buf) {
		return
	}
	var prefix []byte
	prefix = append(prefix, " q "...)
	prefix = appendCompactFloatBytes(prefix, lineWidth, 4)
	prefix = append(prefix, " w 1 j 1 J "...)
	if fillColor.R == fillColor.G && fillColor.G == fillColor.B {
		prefix = appendCompactFloatBytes(prefix, float64(fillColor.R)/255, 4)
		prefix = append(prefix, " G"...)
	} else {
		prefix = appendCompactFloatBytes(prefix, float64(fillColor.R)/255, 4)
		prefix = append(prefix, ' ')
		prefix = appendCompactFloatBytes(prefix, float64(fillColor.G)/255, 4)
		prefix = append(prefix, ' ')
		prefix = appendCompactFloatBytes(prefix, float64(fillColor.B)/255, 4)
		prefix = append(prefix, " RG"...)
	}
	b.injectFauxBoldRenderMode(start)
	b.insertBytes(start, prefix)
	b.buf = append(b.buf, " Q"...)
	b.restoreAfterQ()
}

// WrapActualText 为指定起始span之后的文字区间原地包裹Marked Content ActualText标记
// 入参: firstSpan 起始span序号, text 原文内容
func (b *ContentBuilder) WrapActualText(firstSpan int, text string) {
	if firstSpan < 0 || firstSpan >= len(b.spanStarts) {
		return
	}
	b.EndText()
	start := b.spanStarts[firstSpan]
	if start < 0 || start >= len(b.buf) {
		return
	}
	encoded, err := pdfgo.EncodeTextString(text)
	if err != nil {
		return
	}
	var prefix bytes.Buffer
	prefix.WriteString(" /Span<</ActualText")
	appendPDFLiteralString(&prefix, []byte(encoded))
	prefix.WriteString(">>BDC")
	b.insertBytes(start, prefix.Bytes())
	b.buf = append(b.buf, " EMC"...)
}

// WrapClip 为指定起始span之后的文字区间原地包裹PDF原生矢量裁剪路径
// 入参: firstSpan 起始span序号, clip 页面坐标系裁剪路径
func (b *ContentBuilder) WrapClip(firstSpan int, clip *canvas.Path) {
	if clip == nil || firstSpan < 0 || firstSpan >= len(b.spanStarts) {
		return
	}
	b.EndText()
	start := b.spanStarts[firstSpan]
	if start < 0 || start >= len(b.buf) {
		return
	}
	var prefix []byte
	prefix = append(prefix, " q"...)
	prefix = appendClipPathOps(prefix, clip)
	b.insertBytes(start, prefix)
	b.buf = append(b.buf, " Q"...)
	b.restoreAfterQ()
}

// DrawImage 绘制PDF图像XObject并按需施加原生矢量裁剪，隔离图形状态
// 入参: name XObject资源名, ref XObject引用, imgW 图像像素宽, imgH 图像像素高, m 像素到毫米变换矩阵, clip 页面裁剪路径
func (b *ContentBuilder) DrawImage(name pdfgo.Name, ref pdfgo.Reference, imgW, imgH int, m canvas.Matrix, clip *canvas.Path) {
	b.EndText()
	b.addXObject(name, ref)
	b.buf = append(b.buf, " q"...)
	if clip != nil {
		b.buf = appendClipPathOps(b.buf, clip)
	} else if math.Abs(m[0][1]) > 1e-9 || math.Abs(m[1][0]) > 1e-9 {
		w, h := float64(imgW), float64(imgH)
		bl := m.Dot(canvas.Point{X: 0, Y: 0})
		br := m.Dot(canvas.Point{X: w, Y: 0})
		tl := m.Dot(canvas.Point{X: 0, Y: h})
		tr := m.Dot(canvas.Point{X: w, Y: h})
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, bl.X, 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, bl.Y, 4)
		b.buf = append(b.buf, " m "...)
		b.buf = appendCompactFloatBytes(b.buf, tl.X, 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, tl.Y, 4)
		b.buf = append(b.buf, " l "...)
		b.buf = appendCompactFloatBytes(b.buf, tr.X, 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, tr.Y, 4)
		b.buf = append(b.buf, " l "...)
		b.buf = appendCompactFloatBytes(b.buf, br.X, 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, br.Y, 4)
		b.buf = append(b.buf, " l h W n"...)
	}
	if b.state.alpha != 255 {
		gsName, gsRef := b.sink.OpacityGS(255)
		b.addExtGState(gsName, gsRef)
		b.buf = append(b.buf, " /"...)
		b.buf = append(b.buf, gsName...)
		b.buf = append(b.buf, " gs"...)
	}
	scaled := m.Scale(float64(imgW), float64(imgH))
	b.appendMatrixOp(scaled, " cm")
	b.buf = append(b.buf, " /"...)
	b.buf = append(b.buf, name...)
	b.buf = append(b.buf, " Do Q"...)
}

// DrawForm 在当前内容流中调用PDF Form XObject，支持裁剪、仿射变换与透明度隔离
// 入参: name Form资源名, ref Form引用, m 变换矩阵(从Form默认点空间映射到当前毫米空间), clip 页面裁剪路径, alpha 整体透明度
func (b *ContentBuilder) DrawForm(name pdfgo.Name, ref pdfgo.Reference, m canvas.Matrix, clip *canvas.Path, alpha uint8) {
	b.EndText()
	b.addXObject(name, ref)
	b.buf = append(b.buf, " q"...)
	if clip != nil {
		b.buf = appendClipPathOps(b.buf, clip)
	}
	if alpha != b.state.alpha {
		gsName, gsRef := b.sink.OpacityGS(alpha)
		b.addExtGState(gsName, gsRef)
		b.buf = append(b.buf, " /"...)
		b.buf = append(b.buf, gsName...)
		b.buf = append(b.buf, " gs"...)
	}
	if m != canvas.Identity {
		b.appendMatrixOp(m, " cm")
	}
	b.buf = append(b.buf, " /"...)
	b.buf = append(b.buf, name...)
	b.buf = append(b.buf, " Do Q"...)
}

// contentGState 保存当前内容流的PDF图形状态缓存
type contentGState struct {
	alpha          uint8
	fill           canvas.Paint
	fillValid      bool
	stroke         canvas.Paint
	strokeValid    bool
	lineWidth      float64
	lineCap        int
	lineJoin       int
	miterLimit     float64
	dashes         []float64
	font           *canvas.Font
	fontEntry      *FontEntry
	fontSize       float64
	fontDirection  ctext.Direction
	textPos        canvas.Matrix
	textRenderMode int
}

// contentPathAdapter 适配canvas.Renderer接口以接收文字下划线等装饰路径
type contentPathAdapter struct {
	b *ContentBuilder
}

// Size 返回装饰适配器的名义尺寸
// 返回: float64 宽, float64 高
func (a contentPathAdapter) Size() (float64, float64) { return 0, 0 }

// RenderPath 将文字装饰路径转交ContentBuilder绘制
// 入参: path 路径, style 样式, m 变换矩阵
func (a contentPathAdapter) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	a.b.RenderPath(path, style, m)
}

// RenderText 装饰适配器不处理递归文字
// 入参: text 文字, m 变换矩阵
func (a contentPathAdapter) RenderText(_ *canvas.Text, _ canvas.Matrix) {}

// RenderImage 装饰适配器不处理图像
// 入参: img 图像, m 变换矩阵
func (a contentPathAdapter) RenderImage(_ image.Image, _ canvas.Matrix) {}

// insertBytes 在内容流指定位置原地插入字节片段
// 入参: pos 插入偏移, data 待插入字节
func (b *ContentBuilder) insertBytes(pos int, data []byte) {
	n := len(data)
	if n == 0 {
		return
	}
	oldLen := len(b.buf)
	b.buf = append(b.buf, data...)
	copy(b.buf[pos+n:], b.buf[pos:oldLen])
	copy(b.buf[pos:pos+n], data)
}

// injectFauxBoldRenderMode 在指定区间内所有顶层BT指令后注入2 Tr，自动跳过字符串字面量
// 入参: start 区间起始偏移
func (b *ContentBuilder) injectFauxBoldRenderMode(start int) {
	segment := b.buf[start:]
	var positions []int
	parenDepth := 0
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if parenDepth > 0 {
			if c == '\\' {
				i++
			} else if c == '(' {
				parenDepth++
			} else if c == ')' {
				parenDepth--
			}
			continue
		}
		if c == '(' {
			parenDepth = 1
			continue
		}
		if c == 'B' && i+1 < len(segment) && segment[i+1] == 'T' {
			prevOK := i == 0 || isPDFDelimiterOrSpace(segment[i-1])
			nextOK := i+2 == len(segment) || isPDFDelimiterOrSpace(segment[i+2])
			if prevOK && nextOK {
				positions = append(positions, i+2)
				i++
			}
		}
	}
	if len(positions) == 0 {
		return
	}
	const trOp = " 2 Tr"
	extra := len(positions) * len(trOp)
	oldLen := len(b.buf)
	b.buf = append(b.buf, make([]byte, extra)...)
	dst := len(b.buf)
	srcEnd := oldLen
	for idx := len(positions) - 1; idx >= 0; idx-- {
		split := start + positions[idx]
		tailLen := srcEnd - split
		dst -= tailLen
		copy(b.buf[dst:dst+tailLen], b.buf[split:srcEnd])
		dst -= len(trOp)
		copy(b.buf[dst:dst+len(trOp)], trOp)
		srcEnd = split
	}
}

// cloneState 复制当前图形状态快照
// 返回: contentGState 状态副本
func (b *ContentBuilder) cloneState() contentGState {
	st := b.state
	if len(st.dashes) > 0 {
		st.dashes = slices.Clone(st.dashes)
	}
	return st
}

// restoreAfterQ 在q...Q隔离块闭合后恢复状态缓存，避免状态追踪漂移
func (b *ContentBuilder) restoreAfterQ() {
	if b.hasSavedState {
		b.state = b.savedState
		if len(b.state.dashes) > 0 {
			b.state.dashes = slices.Clone(b.state.dashes)
		}
		return
	}
	b.state.fillValid = false
	b.state.strokeValid = false
	b.state.font = nil
	b.state.fontEntry = nil
	b.state.lineWidth = -1
	b.state.lineCap = -1
	b.state.lineJoin = -1
	b.state.miterLimit = -1
	b.state.dashes = nil
	b.state.textRenderMode = -1
}

// addFont 登记内容流引用的字体资源
// 入参: name 字体资源名, ref 字体对象引用
func (b *ContentBuilder) addFont(name pdfgo.Name, ref pdfgo.Reference) {
	if b.resources.Fonts == nil {
		b.resources.Fonts = make(map[pdfgo.Name]pdfgo.Reference)
	}
	b.resources.Fonts[name] = ref
}

// addXObject 登记内容流引用的XObject资源
// 入参: name XObject资源名, ref XObject对象引用
func (b *ContentBuilder) addXObject(name pdfgo.Name, ref pdfgo.Reference) {
	if b.resources.XObjects == nil {
		b.resources.XObjects = make(map[pdfgo.Name]pdfgo.Reference)
	}
	b.resources.XObjects[name] = ref
}

// addExtGState 登记内容流引用的扩展图形状态资源
// 入参: name 状态资源名, ref 状态对象引用
func (b *ContentBuilder) addExtGState(name pdfgo.Name, ref pdfgo.Reference) {
	if b.resources.ExtGStates == nil {
		b.resources.ExtGStates = make(map[pdfgo.Name]pdfgo.Reference)
	}
	b.resources.ExtGStates[name] = ref
}

// addPattern 登记内容流引用的底纹或渐变资源
// 入参: name Pattern资源名, ref Pattern对象引用
func (b *ContentBuilder) addPattern(name pdfgo.Name, ref pdfgo.Reference) {
	if b.resources.Patterns == nil {
		b.resources.Patterns = make(map[pdfgo.Name]pdfgo.Reference)
	}
	b.resources.Patterns[name] = ref
}

// setAlpha 同步当前非描边与描边透明度图形状态
// 入参: alpha 八位透明度值
func (b *ContentBuilder) setAlpha(alpha uint8) {
	if alpha == b.state.alpha {
		return
	}
	name, ref := b.sink.OpacityGS(alpha)
	b.addExtGState(name, ref)
	b.buf = append(b.buf, " /"...)
	b.buf = append(b.buf, name...)
	b.buf = append(b.buf, " gs"...)
	b.state.alpha = alpha
}

// setFill 设置当前填充颜色或渐变，渐变前强制重置不透明状态
// 入参: fill 填充画刷, m 当前矩阵
func (b *ContentBuilder) setFill(fill canvas.Paint, m canvas.Matrix) {
	if fill.IsPattern() {
		return
	}
	if fill.IsGradient() {
		b.setAlpha(255)
		name, ref := b.sink.ShadingPattern(fill.Gradient, m)
		b.addPattern(name, ref)
		b.buf = append(b.buf, " /Pattern cs /"...)
		b.buf = append(b.buf, name...)
		b.buf = append(b.buf, " scn"...)
		b.state.fill = fill
		b.state.fillValid = false
		return
	}
	if b.state.fillValid && fill.Equal(b.state.fill) && b.state.alpha == fill.Color.A {
		return
	}
	c := fill.Color
	if c.A == 0 {
		b.buf = append(b.buf, " 0 g"...)
	} else {
		a := float64(c.A) / 255.0
		if c.R == c.G && c.R == c.B {
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.R)/255.0/a, 4)
			b.buf = append(b.buf, " g"...)
		} else {
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.R)/255.0/a, 4)
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.G)/255.0/a, 4)
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.B)/255.0/a, 4)
			b.buf = append(b.buf, " rg"...)
		}
	}
	b.setAlpha(c.A)
	b.state.fill = fill
	b.state.fillValid = true
}

// setStroke 设置当前描边颜色或渐变，渐变前强制重置不透明状态
// 入参: stroke 描边画刷, m 当前矩阵
func (b *ContentBuilder) setStroke(stroke canvas.Paint, m canvas.Matrix) {
	if stroke.IsPattern() {
		return
	}
	if stroke.IsGradient() {
		b.setAlpha(255)
		name, ref := b.sink.ShadingPattern(stroke.Gradient, m)
		b.addPattern(name, ref)
		b.buf = append(b.buf, " /Pattern CS /"...)
		b.buf = append(b.buf, name...)
		b.buf = append(b.buf, " SCN"...)
		b.state.stroke = stroke
		b.state.strokeValid = false
		return
	}
	if b.state.strokeValid && stroke.Equal(b.state.stroke) && b.state.alpha == stroke.Color.A {
		return
	}
	c := stroke.Color
	if c.A == 0 {
		b.buf = append(b.buf, " 0 G"...)
	} else {
		a := float64(c.A) / 255.0
		if c.R == c.G && c.R == c.B {
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.R)/255.0/a, 4)
			b.buf = append(b.buf, " G"...)
		} else {
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.R)/255.0/a, 4)
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.G)/255.0/a, 4)
			b.buf = append(b.buf, ' ')
			b.buf = appendCompactFloatBytes(b.buf, float64(c.B)/255.0/a, 4)
			b.buf = append(b.buf, " RG"...)
		}
	}
	b.setAlpha(c.A)
	b.state.stroke = stroke
	b.state.strokeValid = true
}

// setStrokeStyle 同步线宽、线端、线接与虚线样式
// 入参: style 画布样式
func (b *ContentBuilder) setStrokeStyle(style canvas.Style) {
	b.setLineWidth(style.StrokeWidth)
	b.setLineCap(style.StrokeCapper)
	b.setLineJoin(style.StrokeJoiner)
	b.setDashes(style.DashOffset, style.Dashes)
}

// setLineWidth 更新PDF描边线宽
// 入参: lineWidth 线宽(毫米)
func (b *ContentBuilder) setLineWidth(lineWidth float64) {
	if lineWidth == b.state.lineWidth {
		return
	}
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, lineWidth, 4)
	b.buf = append(b.buf, " w"...)
	b.state.lineWidth = lineWidth
}

// setLineCap 更新PDF线端样式
// 入参: capper 画布线端样式
func (b *ContentBuilder) setLineCap(capper canvas.Capper) {
	lineCap := 0
	switch capper.(type) {
	case canvas.RoundCapper:
		lineCap = 1
	case canvas.SquareCapper:
		lineCap = 2
	}
	if lineCap == b.state.lineCap {
		return
	}
	b.buf = append(b.buf, ' ', byte('0'+lineCap), ' ', 'J')
	b.state.lineCap = lineCap
}

// setLineJoin 更新PDF线接与斜接限制
// 入参: joiner 画布线接样式
func (b *ContentBuilder) setLineJoin(joiner canvas.Joiner) {
	lineJoin := 0
	miterLimit := 10.0
	switch j := joiner.(type) {
	case canvas.RoundJoiner:
		lineJoin = 1
	case canvas.BevelJoiner:
		lineJoin = 2
	case canvas.MiterJoiner:
		lineJoin = 0
		if !math.IsNaN(j.Limit) {
			miterLimit = j.Limit
		}
	}
	if lineJoin != b.state.lineJoin {
		b.buf = append(b.buf, ' ', byte('0'+lineJoin), ' ', 'j')
		b.state.lineJoin = lineJoin
	}
	if lineJoin == 0 && miterLimit != b.state.miterLimit {
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, miterLimit, 4)
		b.buf = append(b.buf, " M"...)
		b.state.miterLimit = miterLimit
	}
}

// setDashes 更新PDF虚线模式及相位
// 入参: dashPhase 虚线相位, dashArray 虚线数组
func (b *ContentBuilder) setDashes(dashPhase float64, dashArray []float64) {
	arr := dashArray
	if len(arr)%2 == 1 {
		arr = append(slices.Clone(arr), arr...)
	}
	if dashPhase < 0 && len(arr) > 0 {
		total := 0.0
		for _, d := range arr {
			total += d
		}
		if total > 0 {
			for dashPhase < 0 {
				dashPhase += total
			}
		}
	}
	dashes := make([]float64, len(arr)+1)
	copy(dashes, arr)
	dashes[len(arr)] = dashPhase
	if len(dashes) == 1 {
		dashes[0] = 0
	}
	if slices.Equal(dashes, b.state.dashes) {
		return
	}
	if len(dashes) == 1 {
		b.buf = append(b.buf, " [] 0 d"...)
	} else {
		b.buf = append(b.buf, " ["...)
		for i, d := range dashes[:len(dashes)-1] {
			if i > 0 {
				b.buf = append(b.buf, ' ')
			}
			b.buf = appendCompactFloatBytes(b.buf, d, 4)
		}
		b.buf = append(b.buf, "] "...)
		b.buf = appendCompactFloatBytes(b.buf, dashes[len(dashes)-1], 4)
		b.buf = append(b.buf, " d"...)
	}
	b.state.dashes = dashes
}

// setFont 更新当前文字字体、字号与书写方向
// 入参: font 画布字体, size 字号(点), direction 书写方向
func (b *ContentBuilder) setFont(font *canvas.Font, size float64, direction ctext.Direction) {
	if font == b.state.font && size == b.state.fontSize && direction == b.state.fontDirection && b.state.fontEntry != nil {
		return
	}
	vertical := direction == ctext.TopToBottom || direction == ctext.BottomToTop
	name, ref, entry := b.sink.FontResource(font, vertical)
	b.addFont(name, ref)
	b.buf = append(b.buf, " /"...)
	b.buf = append(b.buf, name...)
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, size, 4)
	b.buf = append(b.buf, " Tf"...)
	b.state.font = font
	b.state.fontEntry = entry
	b.state.fontSize = size
	b.state.fontDirection = direction
}

// setTextPosition 更新PDF文字矩阵，线性部分一致时输出更紧凑的Td指令
// 入参: m 目标文字矩阵
func (b *ContentBuilder) setTextPosition(m canvas.Matrix) {
	if m.Equals(b.state.textPos) {
		return
	}
	if canvas.Equal(m[0][0], b.state.textPos[0][0]) && canvas.Equal(m[0][1], b.state.textPos[0][1]) && canvas.Equal(m[1][0], b.state.textPos[1][0]) && canvas.Equal(m[1][1], b.state.textPos[1][1]) {
		d := b.state.textPos.Inv().Dot(canvas.Point{X: m[0][2], Y: m[1][2]})
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, d.X, 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, d.Y, 4)
		b.buf = append(b.buf, " Td"...)
	} else {
		b.appendMatrixOp(m, " Tm")
	}
	b.state.textPos = m
}

// setTextRenderMode 更新PDF文字渲染模式
// 入参: mode 渲染模式编号
func (b *ContentBuilder) setTextRenderMode(mode int) {
	if mode == b.state.textRenderMode {
		return
	}
	b.buf = append(b.buf, ' ', byte('0'+mode), ' ', 'T', 'r')
	b.state.textRenderMode = mode
}

// writeGlyphs 编码并写入字形序列及精确字距调整，无字距调整时直接输出Tj
// 入参: mode 排版方向, glyphs 字形列表
func (b *ContentBuilder) writeGlyphs(mode canvas.WritingMode, glyphs []ctext.Glyph) {
	if len(glyphs) == 0 || b.state.font == nil || b.state.fontEntry == nil {
		return
	}
	entry := b.state.fontEntry
	if mode != canvas.HorizontalTB && !glyphs[0].Vertical && glyphs[0].SFNT != nil {
		rot := glyphs[0].Rotation()
		off := glyphs[0].YOffset - int32(glyphs[0].SFNT.Head.UnitsPerEm/2)
		if rot != ctext.NoRotation || off != 0 {
			const mmPerPt = 25.4 / 72.0
			b.setTextPosition(b.state.textPos.Rotate(float64(rot)).Translate(0, glyphs[0].Size/float64(glyphs[0].SFNT.Head.UnitsPerEm)*mmPerPt*float64(off)))
		}
	}
	f := 1000.0 / float64(b.state.font.SFNT.Head.UnitsPerEm)
	hasAdjust := false
	for j, glyph := range glyphs {
		if j == len(glyphs)-1 {
			continue
		}
		if mode == canvas.HorizontalTB || !glyph.Vertical {
			origAdv := int32(b.state.font.SFNT.GlyphAdvance(glyph.ID))
			if int(math.Round(f*float64(glyph.XAdvance-origAdv))) != 0 {
				hasAdjust = true
				break
			}
		} else {
			origAdv := -int32(b.state.font.SFNT.GlyphVerticalAdvance(glyph.ID))
			if int(math.Round(f*float64(glyph.YAdvance-origAdv))) != 0 {
				hasAdjust = true
				break
			}
		}
	}
	if !hasAdjust {
		b.appendGlyphString(entry, glyphs)
		b.buf = append(b.buf, "Tj"...)
		return
	}
	b.buf = append(b.buf, '[')
	start := 0
	for j, glyph := range glyphs {
		var adj int
		if mode == canvas.HorizontalTB || !glyph.Vertical {
			origAdv := int32(b.state.font.SFNT.GlyphAdvance(glyph.ID))
			if glyph.XAdvance != origAdv {
				adj = -int(math.Round(f * float64(glyph.XAdvance-origAdv)))
			}
		} else {
			origAdv := -int32(b.state.font.SFNT.GlyphVerticalAdvance(glyph.ID))
			if glyph.YAdvance != origAdv {
				adj = -int(math.Round(f * float64(glyph.YAdvance-origAdv)))
			}
		}
		if adj != 0 {
			b.appendGlyphString(entry, glyphs[start:j+1])
			b.buf = strconv.AppendInt(b.buf, int64(adj), 10)
			start = j + 1
		}
	}
	if start < len(glyphs) {
		b.appendGlyphString(entry, glyphs[start:])
	}
	b.buf = append(b.buf, "]TJ"...)
}

// appendGlyphString 将字形序列按标准字体WinAnsi或子集CID双字节写入PDF字面量串
// 入参: entry 字体登记项, glyphs 字形切片
func (b *ContentBuilder) appendGlyphString(entry *FontEntry, glyphs []ctext.Glyph) {
	b.buf = append(b.buf, '(')
	if entry.StandardName != "" {
		for _, glyph := range glyphs {
			s := norm.NFC.String(glyph.Text)
			for _, r := range s {
				c, ok := charmap.Windows1252.EncodeRune(r)
				if !ok && ctext.IsSpace(glyph.Text) {
					c = ' '
				}
				b.appendEscapedByte(c)
			}
		}
		b.buf = append(b.buf, ')')
		return
	}
	var local map[uint16]uint16
	if b.localGlyphMaps != nil {
		local = b.localGlyphMaps[entry]
	}
	if local == nil {
		if b.localGlyphMaps == nil {
			b.localGlyphMaps = make(map[*FontEntry]map[uint16]uint16, 8)
		}
		local = make(map[uint16]uint16, 64)
		b.localGlyphMaps[entry] = local
	}
	for _, glyph := range glyphs {
		gid, ok := local[glyph.ID]
		if !ok {
			gid = entry.MapGlyph(glyph.ID)
			local[glyph.ID] = gid
		}
		b.appendEscapedByte(byte(gid >> 8))
		b.appendEscapedByte(byte(gid))
	}
	b.buf = append(b.buf, ')')
}

// appendEscapedByte 向PDF字面量字符串追加单个转义字节
// 入参: c 字节值
func (b *ContentBuilder) appendEscapedByte(c byte) {
	switch c {
	case '\n':
		b.buf = append(b.buf, '\\', 'n')
	case '\r':
		b.buf = append(b.buf, '\\', 'r')
	case '\t':
		b.buf = append(b.buf, '\\', 't')
	case '\b':
		b.buf = append(b.buf, '\\', 'b')
	case '\f':
		b.buf = append(b.buf, '\\', 'f')
	case '\\', '(', ')':
		b.buf = append(b.buf, '\\', c)
	default:
		b.buf = append(b.buf, c)
	}
}

// appendMatrixOp 追加6参数PDF仿射变换操作符
// 入参: m 仿射变换矩阵, op 操作符文本(含前导空格)
func (b *ContentBuilder) appendMatrixOp(m canvas.Matrix, op string) {
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, m[0][0], 6)
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, m[1][0], 6)
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, m[0][1], 6)
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, m[1][1], 6)
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, m[0][2], 4)
	b.buf = append(b.buf, ' ')
	b.buf = appendCompactFloatBytes(b.buf, m[1][2], 4)
	b.buf = append(b.buf, op...)
}

// appendPathAndFill 追加路径与填充操作符
// 入参: path 几何路径, m 变换矩阵, evenOdd 是否奇偶填充规则
func (b *ContentBuilder) appendPathAndFill(path *canvas.Path, m canvas.Matrix, evenOdd bool) {
	b.appendPath(path, m, false, false)
	if evenOdd {
		b.buf = append(b.buf, " f*"...)
	} else {
		b.buf = append(b.buf, " f"...)
	}
}

// appendPath 追加变换后的PDF路径指令，对轴对齐矩形输出紧凑re指令
// 入参: path 几何路径, m 变换矩阵, stripTrailingClose 是否剥离尾部闭合符以配合s/b操作符, requireClosed 是否要求路径显式闭合
// 返回: bool 路径是否以闭合指令结尾
func (b *ContentBuilder) appendPath(path *canvas.Path, m canvas.Matrix, stripTrailingClose, requireClosed bool) bool {
	if rect, ok := transformedAxisRect(path, m, requireClosed); ok {
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, rect.X0, 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, rect.Y0, 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, rect.W(), 4)
		b.buf = append(b.buf, ' ')
		b.buf = appendCompactFloatBytes(b.buf, rect.H(), 4)
		b.buf = append(b.buf, " re"...)
		return false
	}
	var closed bool
	b.buf, closed = appendTransformedPathPDF(b.buf, path, m, stripTrailingClose)
	return closed
}

// appendClipPathOps 将页面坐标系裁剪路径转为紧凑PDF裁剪指令并追加W n
// 入参: dst 目标缓冲, clip 裁剪路径
// 返回: []byte 追加后的缓冲
func appendClipPathOps(dst []byte, clip *canvas.Path) []byte {
	if rect, ok := transformedAxisRect(clip, canvas.Identity, false); ok {
		dst = append(dst, ' ')
		dst = appendCompactFloatBytes(dst, rect.X0, 4)
		dst = append(dst, ' ')
		dst = appendCompactFloatBytes(dst, rect.Y0, 4)
		dst = append(dst, ' ')
		dst = appendCompactFloatBytes(dst, rect.W(), 4)
		dst = append(dst, ' ')
		dst = appendCompactFloatBytes(dst, rect.H(), 4)
		return append(dst, " re W n"...)
	}
	dst, _ = appendTransformedPathPDF(dst, clip, canvas.Identity, false)
	return append(dst, " W n"...)
}

// transformedAxisRect 识别变换后为单一轴对齐矩形的路径以启用紧凑re操作符
// 入参: path 源路径, m 变换矩阵, requireClosed 是否要求显式闭合
// 返回: canvas.Rect 矩形范围, bool 是否为单一轴对齐矩形
func transformedAxisRect(path *canvas.Path, m canvas.Matrix, requireClosed bool) (canvas.Rect, bool) {
	if path == nil {
		return canvas.Rect{}, false
	}
	sc := path.Scanner()
	var pts [5]canvas.Point
	n := 0
	closed := false
	for sc.Scan() {
		cmd := sc.Cmd()
		switch cmd {
		case canvas.MoveToCmd:
			if n != 0 {
				return canvas.Rect{}, false
			}
			pts[0] = m.Dot(sc.End())
			n = 1
		case canvas.LineToCmd:
			if closed || n == 0 || n >= 5 {
				return canvas.Rect{}, false
			}
			pts[n] = m.Dot(sc.End())
			n++
		case canvas.CloseCmd:
			if closed {
				return canvas.Rect{}, false
			}
			closed = true
		default:
			return canvas.Rect{}, false
		}
	}
	explicitlyClosed := closed
	if n == 5 {
		if !pts[4].Equals(pts[0]) {
			return canvas.Rect{}, false
		}
		n = 4
		explicitlyClosed = true
	}
	if n != 4 || (requireClosed && !explicitlyClosed) {
		return canvas.Rect{}, false
	}
	a, b, c, d := pts[0], pts[1], pts[2], pts[3]
	const eps = 1e-6
	eq := func(u, v float64) bool { return math.Abs(u-v) <= eps }
	axisAligned := (eq(a.Y, b.Y) && eq(b.X, c.X) && eq(c.Y, d.Y) && eq(d.X, a.X)) ||
		(eq(a.X, b.X) && eq(b.Y, c.Y) && eq(c.X, d.X) && eq(d.Y, a.Y))
	if !axisAligned {
		return canvas.Rect{}, false
	}
	x0, x1 := min(a.X, c.X), max(a.X, c.X)
	y0, y1 := min(a.Y, c.Y), max(a.Y, c.Y)
	if x1 <= x0 || y1 <= y0 {
		return canvas.Rect{}, false
	}
	return canvas.Rect{X0: x0, Y0: y0, X1: x1, Y1: y1}, true
}

// appendTransformedPathPDF 零拷贝变换并序列化画布路径为紧凑PDF路径指令，圆弧自动转为三次贝塞尔
// 入参: dst 目标字节切片, path 源路径, m 变换矩阵, stripTrailingClose 是否剥离尾部h指令
// 返回: []byte 追加后的切片, bool 尾部是否含闭合指令
func appendTransformedPathPDF(dst []byte, path *canvas.Path, m canvas.Matrix, stripTrailingClose bool) ([]byte, bool) {
	startLen := len(dst)
	sc := path.Scanner()
	var prev canvas.Point
	lastClosed := false
	lastCloseOffset := -1
	for sc.Scan() {
		lastClosed = false
		lastCloseOffset = -1
		cmd := sc.Cmd()
		switch cmd {
		case canvas.ArcToCmd:
			replaced := path.Copy().Transform(m).ReplaceArcs()
			return appendTransformedPathPDF(dst[:startLen], replaced, canvas.Identity, stripTrailingClose)
		case canvas.MoveToCmd:
			p := m.Dot(sc.End())
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, p.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, p.Y, 4)
			dst = append(dst, " m"...)
			prev = p
		case canvas.LineToCmd:
			p := m.Dot(sc.End())
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, p.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, p.Y, 4)
			dst = append(dst, " l"...)
			prev = p
		case canvas.QuadToCmd:
			cp := m.Dot(sc.CP1())
			end := m.Dot(sc.End())
			c1 := canvas.Point{X: prev.X + 2*(cp.X-prev.X)/3, Y: prev.Y + 2*(cp.Y-prev.Y)/3}
			c2 := canvas.Point{X: end.X + 2*(cp.X-end.X)/3, Y: end.Y + 2*(cp.Y-end.Y)/3}
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c1.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c1.Y, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c2.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c2.Y, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, end.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, end.Y, 4)
			dst = append(dst, " c"...)
			prev = end
		case canvas.CubeToCmd:
			c1 := m.Dot(sc.CP1())
			c2 := m.Dot(sc.CP2())
			end := m.Dot(sc.End())
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c1.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c1.Y, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c2.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, c2.Y, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, end.X, 4)
			dst = append(dst, ' ')
			dst = appendCompactFloatBytes(dst, end.Y, 4)
			dst = append(dst, " c"...)
			prev = end
		case canvas.CloseCmd:
			lastCloseOffset = len(dst)
			dst = append(dst, " h"...)
			lastClosed = true
			prev = m.Dot(sc.End())
		}
	}
	if stripTrailingClose && lastClosed && lastCloseOffset >= 0 {
		dst = dst[:lastCloseOffset]
		return dst, true
	}
	return dst, lastClosed
}

// isPDFDelimiterOrSpace 判断字节是否为PDF词法空白或分隔符
// 入参: c 字节值
// 返回: bool 是否为空白或分隔符
func isPDFDelimiterOrSpace(c byte) bool {
	switch c {
	case 0x00, '\t', '\n', '\f', '\r', ' ', '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}
