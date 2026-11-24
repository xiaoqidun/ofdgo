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
	"fmt"
	"image"
	"math"
	"unicode"

	"github.com/tdewolff/canvas"
)

// maxMergedTextSpanRunes 限制单个相邻合并文本选区的最大Unicode字符数
const maxMergedTextSpanRunes = 12

// boundsRenderer 收集绘制范围，不分配页面像素或合并独立图形轮廓
type boundsRenderer struct {
	box      Box
	collect  bool
	contours []ObjectContour
}

// boundsColor 以底纹的基础颜色和透明度度量轮廓，不修改原画刷
// 入参: color 填充或描边颜色
// 返回: *FillColor 用于度量的颜色
func boundsColor(color *FillColor) *FillColor {
	if color == nil || color.Pattern == nil {
		return color
	}
	return &FillColor{Value: color.Value, Index: color.Index, ColorSpace: color.ColorSpace, Alpha: color.Alpha}
}

// textPathBox 将字形轮廓转换为页面区域
// 入参: path 字形轮廓, pageH 页面高度
// 返回: Box 字符区域
func textPathBox(path *canvas.Path, pageH float64) Box {
	if path.Empty() {
		return Box{}
	}
	bounds := path.Bounds()
	return Box{X: bounds.X0, Y: pageH - bounds.Y1, W: bounds.X1 - bounds.X0, H: bounds.Y1 - bounds.Y0}
}

// textSpanScript 返回字符所属选区合并脚本类别，空白字符返回零禁止合并
// 入参: r 字符码点
// 返回: uint8 脚本类别编号
func textSpanScript(r rune) uint8 {
	if unicode.IsSpace(r) {
		return 0
	}
	if r <= 0x7F || unicode.Is(unicode.Latin, r) {
		return 1
	}
	if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) || r >= 0x3000 && r <= 0x303F || r >= 0xFF01 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6 {
		return 2
	}
	return 3
}

// mergeAdjacentTextSpan 尝试将同行连续相邻且同脚本字符选区合并到前一个选区矩阵
// 入参: runes 文本对象原文字符切片, prev 前一个文字选区, cur 当前已闭合文字选区
// 返回: TextSpan 合并后的选区, bool 是否允许合并
func mergeAdjacentTextSpan(runes []rune, prev, cur TextSpan) (TextSpan, bool) {
	if prev.Start < 0 || prev.Start >= prev.End || prev.End != cur.Start || cur.Start >= cur.End || cur.End > len(runes) || cur.End-prev.Start > maxMergedTextSpanRunes {
		return TextSpan{}, false
	}
	script := textSpanScript(runes[prev.Start])
	if script == 0 {
		return TextSpan{}, false
	}
	for _, ch := range runes[prev.Start+1 : cur.End] {
		if textSpanScript(ch) != script {
			return TextSpan{}, false
		}
	}
	pa, pb, pc, pd, pe, pf := prev.Matrix[0], prev.Matrix[1], prev.Matrix[2], prev.Matrix[3], prev.Matrix[4], prev.Matrix[5]
	ma, mb, mc, md, me, mf := cur.Matrix[0], cur.Matrix[1], cur.Matrix[2], cur.Matrix[3], cur.Matrix[4], cur.Matrix[5]
	pLen := math.Hypot(pa, pb)
	cLen := math.Hypot(ma, mb)
	lineH := math.Hypot(pc, pd)
	if pLen <= 1e-6 || cLen <= 1e-6 || lineH <= 1e-6 {
		return TextSpan{}, false
	}
	tol := math.Min(0.05, 0.05*lineH)
	if math.Abs(pc-mc) > tol || math.Abs(pd-md) > tol {
		return TextSpan{}, false
	}
	ux, uy := pa/pLen, pb/pLen
	cx, cy := ma/cLen, mb/cLen
	if ux*cx+uy*cy < 0.999 || math.Abs(ux*cy-uy*cx) > 0.01 {
		return TextSpan{}, false
	}
	dx, dy := me-(pe+pa), mf-(pf+pb)
	perp := math.Abs(-dx*uy + dy*ux)
	if perp > tol {
		return TextSpan{}, false
	}
	gap := dx*ux + dy*uy
	if gap < -0.1*lineH || gap > 0.15*lineH {
		return TextSpan{}, false
	}
	totalW := (me+ma-pe)*ux + (mf+mb-pf)*uy
	if totalW <= pLen {
		return TextSpan{}, false
	}
	return TextSpan{Start: prev.Start, End: cur.End, Matrix: [6]float64{ux * totalW, uy * totalW, pc, pd, pe, pf}}, true
}

// MeasureObject 使用默认解释器度量对象，不分配页面像素
// 入参: ctx 取消上下文, r 渲染器, object 图形对象, options 度量上下文
// 返回: ObjectMeasurement 度量结果, error 错误信息
func (DefaultBackend) MeasureObject(ctx context.Context, r *Renderer, object GraphicObject, options MeasureOptions) (ObjectMeasurement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ObjectMeasurement{}, err
	}
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" && object.Type != "CompositeObject" {
		return ObjectMeasurement{}, fmt.Errorf("unsupported object type %q", object.Type)
	}
	boundary, ctm := editorGeometry(object)
	if _, err := creationBox(boundary); err != nil {
		return ObjectMeasurement{}, err
	}
	if ctm != "" {
		if _, err := creationNumbers(ctm, 6); err != nil {
			return ObjectMeasurement{}, err
		}
	}
	clip, err := geometryToCanvasPath(options.Clip)
	if err != nil {
		return ObjectMeasurement{}, err
	}
	bounds := &boundsRenderer{collect: options.Contours}
	if len(r.pageResources) > 0 {
		defer r.bindParsedResources(r.pageResources)()
	}
	prevCtx, prevErr, prevText, prevTextOnly, prevContentOnly := r.pageContext, r.renderError, r.pageText, r.textOnly, r.textContentOnly
	r.pageContext, r.renderError, r.pageText, r.textOnly, r.textContentOnly = ctx, nil, nil, false, false
	defer func() {
		r.pageContext, r.renderError, r.pageText, r.textOnly, r.textContentOnly = prevCtx, prevErr, prevText, prevTextOnly, prevContentOnly
	}()
	defaults := options.Defaults
	canvasCtx := canvas.NewContext(bounds)
	switch object.Type {
	case "TextObject":
		if options.Contours {
			r.renderText(canvasCtx, object.TextObject, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
			break
		}
		r.pageText = &PageText{}
		r.textOnly = true
		r.renderText(canvasCtx, object.TextObject, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
		var box Box
		for _, run := range r.pageText.Runs {
			for _, glyph := range run.Boxes {
				box = unionTextBox(box, glyph)
			}
		}
		return ObjectMeasurement{Bounds: box}, r.renderError
	case "PathObject":
		obj := object.PathObject
		defaults = r.drawParamDefaults(obj.DrawParam, defaults)
		obj.DrawParam = ""
		if defaults != nil {
			style := *defaults
			style.FillColor = boundsColor(style.FillColor)
			style.StrokeColor = (*StrokeColor)(boundsColor((*FillColor)(style.StrokeColor)))
			defaults = &style
		}
		obj.FillColor = boundsColor(obj.FillColor)
		obj.StrokeColor = (*StrokeColor)(boundsColor((*FillColor)(obj.StrokeColor)))
		r.renderPath(canvasCtx, obj, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
	case "ImageObject":
		r.measureImage(canvasCtx, object.ImageObject, 0, options.Parent, options.BoundaryInCTM, clip)
	case "CompositeObject":
		r.renderCompositeObject(canvasCtx, object.CompositeObject, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
	}
	return ObjectMeasurement{Bounds: bounds.box, Contours: bounds.contours}, r.renderError
}

// PageText 使用默认解释器提取原文与字形位置
// 入参: ctx 取消上下文, r 渲染器, page 页面内容
// 返回: *PageText 页面文字, error 错误信息
func (DefaultBackend) PageText(ctx context.Context, r *Renderer, page *PageContent) (*PageText, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prevCtx, prevOnText, prevText, prevTextOnly, prevErr := r.pageContext, r.OnPageText, r.pageText, r.textOnly, r.renderError
	extracted := &PageText{}
	r.pageContext, r.OnPageText, r.pageText, r.textOnly, r.renderError = ctx, nil, extracted, true, nil
	defer func() {
		r.pageContext, r.OnPageText, r.pageText, r.textOnly, r.renderError = prevCtx, prevOnText, prevText, prevTextOnly, prevErr
	}()
	defer r.bindPageResources(page)()
	box, err := r.GetPageBoxContext(ctx, page)
	if err != nil {
		return nil, err
	}
	if err := r.renderCanvasPageToContext(canvas.NewContext(canvas.New(box.W, box.H)), page, false); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return extracted, nil
}

// Size 返回不限定边界的度量画布尺寸
// 返回: float64 宽度, float64 高度
func (r *boundsRenderer) Size() (float64, float64) {
	return 0, 0
}

// RenderPath 收集填充与描边范围，保持端点、转角和虚线语义
// 入参: path 路径, style 绘制样式, m 变换矩阵
func (r *boundsRenderer) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if style.HasFill() {
		r.addContour(path.Copy().Transform(m), style.FillRule == canvas.EvenOdd)
	}
	if style.HasStroke() {
		p := strokeCanvasPath(path.Dash(style.DashOffset, style.Dashes...), style.StrokeWidth, style.StrokeCapper, style.StrokeJoiner, canvas.Tolerance)
		r.add(p.Transform(m))
	}
}

// RenderText 收集绘制文字范围
// 入参: text 文字, m 变换矩阵
func (r *boundsRenderer) RenderText(text *canvas.Text, m canvas.Matrix) {
	text.RenderTo(r, m, 0)
}

// RenderImage 度量不展开底纹图片
// 入参: img 图片, m 变换矩阵
func (r *boundsRenderer) RenderImage(img image.Image, m canvas.Matrix) {}

// add 合并路径的精确曲线范围并转换为向下的纵轴
// 入参: path 绘制路径
func (r *boundsRenderer) add(path *canvas.Path) {
	r.addContour(path, false)
}

// addContour 合并范围并按需保留独立填充区域
// 入参: path 绘制路径, evenOdd 是否使用奇偶规则
func (r *boundsRenderer) addContour(path *canvas.Path, evenOdd bool) {
	if path.Empty() {
		return
	}
	rect := path.Bounds()
	r.box = unionTextBox(r.box, Box{X: rect.X0, Y: -rect.Y1, W: rect.W(), H: rect.H()})
	if r.collect {
		outline := &canvas.Path{}
		for _, subpath := range path.Split() {
			subpath = subpath.Copy()
			subpath.Close()
			outline = outline.Append(subpath)
		}
		r.contours = append(r.contours, ObjectContour{Path: outline.Transform(canvas.Matrix{{1, 0, 0}, {0, -1, 0}}).ToSVG(), EvenOdd: evenOdd})
	}
}

// measureImage 复用图片变换与裁剪语义度量范围，不读取像素数据
// 入参: ctx 度量画布, obj 图片, pageH 页面高度, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
func (r *Renderer) measureImage(ctx *canvas.Context, obj ImageObject, pageH float64, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if obj.Visible != nil && !*obj.Visible || obj.Alpha != nil && *obj.Alpha == 0 {
		return
	}
	box, _ := ParseBox(obj.Boundary)
	ctm := NewMatrix(obj.CTM)
	if obj.CTM == "" {
		ctm = Matrix{a: box.W, d: box.H}
	}
	m := TranslationMatrix(box.X, box.Y).Multiply(ctm)
	if parentCTM != nil {
		if boundaryInCTM {
			m = parentCTM.Multiply(m)
		} else {
			m = TranslationMatrix(box.X, box.Y).Multiply(*parentCTM).Multiply(ctm)
		}
	}
	p := canvas.Rectangle(1, 1).Transform(canvas.Matrix{{m.a, m.c, m.e}, {-m.b, -m.d, pageH - m.f}})
	clip := r.intersectCachedClipPath(parentClip, r.buildObjectClipPath(obj.Clips, pageH, obj.Boundary, ctm, parentCTM, boundaryInCTM))
	ctx.Renderer.(*boundsRenderer).add(applyClipPath(p, clip))
	if obj.Border != nil {
		border := *obj.Border
		border.BorderColor = (*StrokeColor)(boundsColor((*FillColor)(border.BorderColor)))
		obj.Border = &border
		r.renderImageBorder(ctx, obj, box, pageH, ctm, parentCTM, boundaryInCTM, parentClip)
	}
}

// addSpan 收集字形选择区域并聚合同一原文区间的多字形范围
// 入参: start 起始字符, end 结束字符, bounds 字形布局区域, m 字形变换, clip 裁剪路径, pageH 页面高度
func (run *TextRun) addSpan(start, end int, bounds canvas.Rect, m canvas.Matrix, clip *canvas.Path, pageH float64) {
	if m.Det() == 0 {
		return
	}
	if clip != nil {
		if clipRect, isRect := rectangularPath(clip); isRect {
			world := bounds.Transform(m)
			if !clipRect.Contains(world) {
				if isAxisAlignedCanvasMatrix(m) {
					clipped, ok := intersectCanvasRect(world, clipRect)
					if !ok {
						return
					}
					x0, x1 := (clipped.X0-m[0][2])/m[0][0], (clipped.X1-m[0][2])/m[0][0]
					if x0 > x1 {
						x0, x1 = x1, x0
					}
					y0, y1 := (clipped.Y0-m[1][2])/m[1][1], (clipped.Y1-m[1][2])/m[1][1]
					if y0 > y1 {
						y0, y1 = y1, y0
					}
					bounds = canvas.Rect{X0: x0, Y0: y0, X1: x1, Y1: y1}
				} else {
					path := canvas.Rectangle(bounds.W(), bounds.H()).Translate(bounds.X0, bounds.Y0).Transform(m)
					path = applyClipPath(path, clip)
					if path.Empty() {
						return
					}
					inverse, ok := invertCanvasMatrix(m)
					if !ok {
						return
					}
					bounds = path.Transform(inverse).Bounds()
				}
			}
		} else {
			path := canvas.Rectangle(bounds.W(), bounds.H()).Translate(bounds.X0, bounds.Y0).Transform(m)
			path = applyClipPath(path, clip)
			if path.Empty() {
				return
			}
			inverse, ok := invertCanvasMatrix(m)
			if !ok {
				return
			}
			bounds = path.Transform(inverse).Bounds()
		}
	}
	if bounds.W() < 0 || bounds.H() <= 0 {
		return
	}
	m = canvas.Matrix{{1, 0, 0}, {0, -1, pageH}}.Mul(m).Translate(bounds.X0, bounds.Y1).Scale(bounds.W(), -bounds.H())
	curMatrix := [6]float64{m[0][0], m[1][0], m[0][1], m[1][1], m[0][2], m[1][2]}
	if n := len(run.Spans); n > 0 && run.Spans[n-1].Start == start && run.Spans[n-1].End == end {
		span := run.Spans[n-1]
		v := span.Matrix
		previous := canvas.Matrix{{v[0], v[2], v[4]}, {v[1], v[3], v[5]}}
		if inverse, ok := invertCanvasMatrix(previous); ok {
			merged := canvas.Rect{X1: 1, Y1: 1}.Add(canvas.Rect{X1: 1, Y1: 1}.Transform(inverse.Mul(m)))
			m = previous.Translate(merged.X0, merged.Y0).Scale(merged.W(), merged.H())
			curMatrix = [6]float64{m[0][0], m[1][0], m[0][1], m[1][1], m[0][2], m[1][2]}
		}
		run.Spans[n-1] = TextSpan{Start: start, End: end, Matrix: curMatrix}
		return
	}
	run.Spans = append(run.Spans, TextSpan{Start: start, End: end, Matrix: curMatrix})
}

// mergeLastSpan 在完整原文区间闭合后尝试将末选区与前序相邻选区合并
// 入参: runes 文本对象原文字符切片, start 已闭合区间起始字符, end 已闭合区间结束字符
func (run *TextRun) mergeLastSpan(runes []rune, start, end int) {
	n := len(run.Spans)
	if n < 2 {
		return
	}
	last := run.Spans[n-1]
	if last.Start != start || last.End != end {
		return
	}
	if merged, ok := mergeAdjacentTextSpan(runes, run.Spans[n-2], last); ok {
		run.Spans[n-2] = merged
		run.Spans = run.Spans[:n-1]
	}
}
