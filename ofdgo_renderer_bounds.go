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

// ObjectBounds 获取文字、路径、图片或复合对象在页面坐标中的轴对齐范围，不修改对象
// 文字采用字形范围，路径包含描边与裁剪，图片采用裁剪后的几何范围，不解码像素或排除透明像素
// 底纹按填充或描边轮廓度量，不展开图案单元
// 不应用页面边界或父级变换；无可见范围时返回零值，不支持的对象类型返回错误
// 入参: object 图形对象, drawParam 图层绘制参数标识，无继承时为空
// 返回: Box 毫米坐标范围, error 错误信息
func (r *Renderer) ObjectBounds(object GraphicObject, drawParam string) (Box, error) {
	return r.measureObject(object, drawParam, &boundsRenderer{})
}

// ObjectContour 页面坐标中的填充轮廓，Path为SVG路径，EvenOdd表示奇偶填充
type ObjectContour struct {
	Path    string `json:"path"`
	EvenOdd bool   `json:"evenOdd,omitempty"`
}

// AnnotationGeometry 获取注解外观的实际内容轮廓，不将Appearance容器边界视为绘制内容
// 链接保留透明对象定义的交互区域，图片按几何范围度量而不解码透明像素
// 入参: annotation 注解
// 返回: Box 页面范围, []ObjectContour 命中轮廓, error 错误信息
func (r *Renderer) AnnotationGeometry(annotation Annotation) (Box, []ObjectContour, error) {
	object := GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: CompositeGraphicUnit{Boundary: annotation.Appearance.Boundary, Objects: annotation.Appearance.Objects}}
	box, contours, err := r.ObjectGeometry(object, "")
	if err != nil {
		return box, contours, err
	}
	for _, source := range r.annotationActionSources([]Annotation{annotation}) {
		for _, action := range source.Actions {
			if action.Event != "CLICK" {
				continue
			}
			region, path := actionLinkRegion(source, action)
			if region.W <= 0 || region.H <= 0 {
				continue
			}
			if path == "" {
				path = canvas.Rectangle(region.W, region.H).Translate(region.X, region.Y).ToSVG()
			}
			box = unionTextBox(box, region)
			contours = append(contours, ObjectContour{Path: path})
		}
	}
	return box, contours, nil
}

// ObjectContours 获取路径、图片或复合对象的实际绘制区域，包含描边、虚线、变换及对象裁剪
// 图片不解码像素，底纹不展开图案单元；不包含页面边界或父级变换
// 入参: object 路径、图片或复合对象, drawParam 图层绘制参数标识
// 返回: []ObjectContour 可用于点选的轮廓, error 错误信息
func (r *Renderer) ObjectContours(object GraphicObject, drawParam string) ([]ObjectContour, error) {
	_, contours, err := r.ObjectGeometry(object, drawParam)
	return contours, err
}

// ObjectGeometry 一次度量路径、图片或复合对象的范围与轮廓，语义与ObjectBounds和ObjectContours一致
// 入参: object 图形对象, drawParam 图层绘制参数标识
// 返回: Box 毫米范围, []ObjectContour 绘制轮廓, error 错误信息
func (r *Renderer) ObjectGeometry(object GraphicObject, drawParam string) (Box, []ObjectContour, error) {
	if object.Type != "PathObject" && object.Type != "ImageObject" && object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
		return Box{}, nil, fmt.Errorf("contours require a path, image or composite object")
	}
	bounds := &boundsRenderer{collect: true}
	box, err := r.measureObject(object, drawParam, bounds)
	return box, bounds.contours, err
}

// measureObject 复用渲染逻辑收集对象范围与可选轮廓
// 入参: object 对象, drawParam 图层绘制参数标识, bounds 度量目标
// 返回: Box 毫米范围, error 错误信息
func (r *Renderer) measureObject(object GraphicObject, drawParam string, bounds *boundsRenderer) (Box, error) {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" && object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
		return Box{}, fmt.Errorf("unsupported object type %q", object.Type)
	}
	boundary, ctm := editorGeometry(object)
	if _, err := creationBox(boundary); err != nil {
		return Box{}, err
	}
	if ctm != "" {
		if _, err := creationNumbers(ctm, 6); err != nil {
			return Box{}, err
		}
	}
	renderer := *r
	renderer.pageText = nil
	renderer.textOnly = false
	defaults := renderer.drawParamDefaults(drawParam, nil)
	ctx := canvas.NewContext(bounds)
	switch object.Type {
	case "TextObject":
		renderer.pageText = &PageText{}
		renderer.textOnly = true
		renderer.renderText(ctx, object.TextObject, 0, defaults, nil, false, nil)
		var box Box
		for _, run := range renderer.pageText.Runs {
			for _, glyph := range run.Boxes {
				box = unionTextBox(box, glyph)
			}
		}
		return box, nil
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
		renderer.renderPath(ctx, obj, 0, defaults, nil, false, nil)
	case "ImageObject":
		renderer.measureImage(ctx, object.ImageObject, 0, nil, false, nil)
	case "CompositeObject", "CompositeGraphicUnit":
		renderer.renderCompositeGraphicUnit(ctx, object.CompositeGraphicUnit, 0, defaults, nil, false, nil)
	}
	return bounds.box, nil
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
