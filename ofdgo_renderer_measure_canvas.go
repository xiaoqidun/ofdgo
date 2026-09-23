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
	"fmt"
	"image"

	"github.com/tdewolff/canvas"
)

// MeasureObject 使用默认解释器度量对象，不分配页面像素
// 入参: r 渲染器, object 图形对象, options 度量上下文
// 返回: ObjectMeasurement 度量结果, error 错误信息
func (CanvasBackend) MeasureObject(r *Renderer, object GraphicObject, options MeasureOptions) (ObjectMeasurement, error) {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" && object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
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
	renderer := *r
	renderer.renderError = nil
	renderer.pageText = nil
	renderer.textOnly = false
	defaults := options.Defaults
	ctx := canvas.NewContext(bounds)
	switch object.Type {
	case "TextObject":
		if options.Contours {
			renderer.renderText(ctx, object.TextObject, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
			break
		}
		renderer.pageText = &PageText{}
		renderer.textOnly = true
		renderer.renderText(ctx, object.TextObject, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
		var box Box
		for _, run := range renderer.pageText.Runs {
			for _, glyph := range run.Boxes {
				box = unionTextBox(box, glyph)
			}
		}
		return ObjectMeasurement{Bounds: box}, renderer.renderError
	case "PathObject":
		obj := object.PathObject
		defaults = renderer.drawParamDefaults(obj.DrawParam, defaults)
		obj.DrawParam = ""
		if defaults != nil {
			style := *defaults
			style.FillColor = boundsColor(style.FillColor)
			style.StrokeColor = (*StrokeColor)(boundsColor((*FillColor)(style.StrokeColor)))
			defaults = &style
		}
		obj.FillColor = boundsColor(obj.FillColor)
		obj.StrokeColor = (*StrokeColor)(boundsColor((*FillColor)(obj.StrokeColor)))
		renderer.renderPath(ctx, obj, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
	case "ImageObject":
		renderer.measureImage(ctx, object.ImageObject, 0, options.Parent, options.BoundaryInCTM, clip)
	case "CompositeObject", "CompositeGraphicUnit":
		renderer.renderCompositeGraphicUnit(ctx, object.CompositeGraphicUnit, 0, defaults, options.Parent, options.BoundaryInCTM, clip)
	}
	return ObjectMeasurement{Bounds: bounds.box, Contours: bounds.contours}, renderer.renderError
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
	clip := intersectClipPath(parentClip, r.buildObjectClipPath(obj.Clips, pageH, box.X, box.Y, ctm, parentCTM, boundaryInCTM))
	ctx.Renderer.(*boundsRenderer).add(applyClipPath(p, clip))
	if obj.Border != nil {
		border := *obj.Border
		border.BorderColor = (*StrokeColor)(boundsColor((*FillColor)(border.BorderColor)))
		obj.Border = &border
		r.renderImageBorder(ctx, obj, box, pageH, parentCTM, boundaryInCTM, clip)
	}
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

// boundsRenderer 收集绘制范围，不分配页面像素或合并独立图形轮廓
type boundsRenderer struct {
	box      Box
	collect  bool
	contours []ObjectContour
}

// Size 返回不限定边界的度量画布尺寸
// 返回: float64 宽度, float64 高度
func (r *boundsRenderer) Size() (float64, float64) {
	return 0, 0
}

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

// RenderPath 收集填充与描边范围，保持线帽、连接和虚线语义
// 入参: path 路径, style 绘制样式, m 变换矩阵
func (r *boundsRenderer) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if style.HasFill() {
		r.addContour(path.Copy().Transform(m), style.FillRule == canvas.EvenOdd)
	}
	if style.HasStroke() {
		p := path.Dash(style.DashOffset, style.Dashes...).Stroke(style.StrokeWidth, style.StrokeCapper, style.StrokeJoiner, canvas.Tolerance)
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

// PageText 使用默认解释器提取原文与字形位置
// 入参: r 渲染器, page 页面内容
// 返回: *PageText 页面文字, error 错误信息
func (CanvasBackend) PageText(r *Renderer, page *PageContent) (*PageText, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	renderer := *r
	renderer.OnPageText = nil
	renderer.pageText = &PageText{}
	renderer.textOnly = true
	if err := renderer.renderCanvasPageToContext(canvas.NewContext(canvas.New(box.W, box.H)), page, false); err != nil {
		return nil, err
	}
	return renderer.pageText, nil
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

// addSpan 收集字形选择区域，同一原文区间的多个字形合并为一项
// 入参: start 起始字符, end 结束字符, bounds 字形布局区域, m 字形变换, clip 裁剪路径, pageH 页面高度
func (run *TextRun) addSpan(start, end int, bounds canvas.Rect, m canvas.Matrix, clip *canvas.Path, pageH float64) {
	if m.Det() == 0 {
		return
	}
	if clip != nil {
		path := canvas.Rectangle(bounds.W(), bounds.H()).Translate(bounds.X0, bounds.Y0).Transform(m)
		path = applyClipPath(path, clip)
		if path.Empty() {
			return
		}
		bounds = path.Transform(m.Inv()).Bounds()
	}
	if bounds.W() < 0 || bounds.H() <= 0 {
		return
	}
	m = canvas.Matrix{{1, 0, 0}, {0, -1, pageH}}.Mul(m).Translate(bounds.X0, bounds.Y1).Scale(bounds.W(), -bounds.H())
	span := TextSpan{Start: start, End: end}
	if n := len(run.Spans); n > 0 && run.Spans[n-1].Start == start && run.Spans[n-1].End == end {
		span = run.Spans[n-1]
		v := span.Matrix
		previous := canvas.Matrix{{v[0], v[2], v[4]}, {v[1], v[3], v[5]}}
		if previous.Det() != 0 {
			bounds = canvas.Rect{X1: 1, Y1: 1}.Add(canvas.Rect{X1: 1, Y1: 1}.Transform(previous.Inv().Mul(m)))
			m = previous.Translate(bounds.X0, bounds.Y0).Scale(bounds.W(), bounds.H())
		}
		run.Spans = run.Spans[:n-1]
	}
	span.Matrix = [6]float64{m[0][0], m[1][0], m[0][1], m[1][1], m[0][2], m[1][2]}
	run.Spans = append(run.Spans, span)
}
