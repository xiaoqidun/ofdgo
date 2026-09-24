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
	"image/color"
	"math"
	"strings"
)

// AddDrawParam 注册绘制参数，保留继承引用，未引用的资源不写入文档
// 入参: draw 绘制参数，ID由编辑器分配
// 返回: string 资源标识, error 错误信息
func (e *Editor) AddDrawParam(draw DrawParam) (string, error) {
	if draw.ResourceID != "" || draw.BaseLoc != "" || draw.Link != "" {
		return "", fmt.Errorf("external draw parameter references cannot be registered directly")
	}
	base, err := e.editorDrawParam(draw.Relative, make(map[string]bool))
	if err != nil {
		return "", err
	}
	effective := mergeDrawParam(*base, &draw)
	if err := validateEditorStroke(PathObject{LineWidth: draw.LineWidth, Cap: draw.Cap, Join: draw.Join, MiterLimit: draw.MiterLimit, DashPattern: draw.DashPattern, DashOffset: draw.DashOffset}); err != nil {
		return "", err
	}
	if !finite(draw.Size) || draw.Size < 0 || draw.Weight < 0 || draw.Weight > 900 || draw.Weight%100 != 0 {
		return "", fmt.Errorf("invalid draw parameter font size or weight")
	}
	if effective.Font != "" {
		if _, err := e.editorFont(effective.Font); err != nil {
			return "", err
		}
	}
	for _, paint := range []*FillColor{draw.FillColor, (*FillColor)(draw.StrokeColor)} {
		if err := e.editorColor(paint); err != nil {
			return "", err
		}
	}
	return e.addEditorDrawParam(cloneEditorData(draw))
}

// editorDrawParam 解析原文档的绘制参数继承链，缺失或循环引用不进入编辑快照
// 入参: id 绘制参数标识, visited 已访问标识
// 返回: *DrawParam 合并参数, error 错误信息
func (e *Editor) editorDrawParam(id string, visited map[string]bool) (*DrawParam, error) {
	if id == "" {
		return &DrawParam{}, nil
	}
	var dp *DrawParam
	for _, resource := range e.resources {
		if resource.draw != nil && resource.draw.ID == id {
			dp = resource.draw
			break
		}
	}
	if dp == nil && e.source != nil {
		dp = e.source.reader.drawParamCache[id]
	}
	if dp == nil || visited[id] {
		return nil, &EditError{Code: EditUnsupportedStyle, Err: fmt.Errorf("invalid draw parameter reference %q", id)}
	}
	visited[id] = true
	base, err := e.editorDrawParam(dp.Relative, visited)
	if err != nil {
		return nil, err
	}
	return mergeDrawParam(*base, dp), nil
}

// resolveEditorStyle 为对象生成独立的有效样式，不改写共享资源或原XML
// 入参: object 原对象, layer 图层绘制参数标识
// 返回: GraphicObject 编辑快照, error 错误信息
func (e *Editor) resolveEditorStyle(object GraphicObject, layer string) (GraphicObject, error) {
	var id string
	switch object.Type {
	case "TextObject":
		id = object.TextObject.DrawParam
	case "PathObject":
		id = object.PathObject.DrawParam
	case "CompositeObject", "CompositeGraphicUnit":
		for _, id := range []string{layer, object.CompositeGraphicUnit.DrawParam} {
			if _, err := e.editorDrawParam(id, make(map[string]bool)); err != nil {
				return GraphicObject{}, err
			}
		}
		return object, nil
	default:
		return object, nil
	}
	if layer == "" && id == "" {
		return object, nil
	}
	base, err := e.editorDrawParam(layer, make(map[string]bool))
	if err != nil {
		return GraphicObject{}, err
	}
	return e.resolveEditorStyleDefaults(object, base)
}

// resolveEditorStyleDefaults 合并对象参数与继承外观，仅生成编辑快照
// 入参: object 原对象, base 继承参数
// 返回: GraphicObject 有效样式快照, error 错误信息
func (e *Editor) resolveEditorStyleDefaults(object GraphicObject, base *DrawParam) (GraphicObject, error) {
	var id string
	switch object.Type {
	case "TextObject":
		id = object.TextObject.DrawParam
	case "PathObject":
		id = object.PathObject.DrawParam
	default:
		return cloneEditorData(object), nil
	}
	dp, err := e.editorDrawParam(id, make(map[string]bool))
	if err != nil {
		return GraphicObject{}, err
	}
	style := mergeDrawParam(*base, dp)
	if object.Type == "PathObject" {
		obj := &object.PathObject
		style = mergeDrawParam(*style, &DrawParam{LineWidth: obj.LineWidth, Join: obj.Join, Cap: obj.Cap,
			MiterLimit: obj.MiterLimit, DashPattern: obj.DashPattern, dashPatternSet: obj.dashPatternSet, DashOffset: obj.DashOffset,
			FillColor: obj.FillColor, StrokeColor: obj.StrokeColor})
		obj.DrawParam = ""
		obj.LineWidth, obj.Join, obj.Cap = style.LineWidth, style.Join, style.Cap
		obj.MiterLimit, obj.DashPattern, obj.DashOffset = style.MiterLimit, style.DashPattern, style.DashOffset
		obj.dashPatternSet = style.dashPatternSet
		obj.FillColor, obj.StrokeColor = style.FillColor, style.StrokeColor
	} else {
		obj := &object.TextObject
		if style.Cap != "" && style.Cap != "Butt" || style.DashPattern != "" || style.DashOffset != nil && *style.DashOffset != 0 {
			return GraphicObject{}, &EditError{Code: EditUnsupportedStyle, Err: fmt.Errorf("text draw parameters require unsupported stroke styles")}
		}
		style = mergeDrawParam(*style, &DrawParam{LineWidth: obj.LineWidth, Join: obj.Join,
			MiterLimit: obj.MiterLimit, FillColor: obj.FillColor, StrokeColor: obj.StrokeColor})
		text := mergeDrawParam(*dp, &DrawParam{Font: obj.Font, Size: obj.Size, Weight: obj.Weight, Italic: obj.Italic})
		obj.DrawParam = ""
		obj.Font, obj.Size, obj.Weight, obj.Italic = text.Font, text.Size, text.Weight, text.Italic
		obj.LineWidth, obj.Join, obj.MiterLimit = style.LineWidth, style.Join, style.MiterLimit
		obj.FillColor, obj.StrokeColor = style.FillColor, style.StrokeColor
	}
	for _, color := range []*FillColor{style.FillColor, (*FillColor)(style.StrokeColor)} {
		if _, err := e.Color(color); err != nil {
			return GraphicObject{}, &EditError{Code: EditUnsupportedColor, Err: fmt.Errorf("unsupported draw parameter color: %w", err)}
		}
	}
	return cloneEditorObject(object)
}

// ObjectStyle 对象外观，nil字段保持原值，颜色的Alpha为nil时保留原颜色透明度
// StyleObjects使用对象坐标毫米，StyleCompositeObjects的LineWidth使用页面毫米
type ObjectStyle struct {
	Alpha       *int
	Fill        *bool
	Stroke      *bool
	FillColor   *FillColor
	StrokeColor *StrokeColor
	LineWidth   *float64
	DashPattern *string
	DashOffset  *float64
	Cap         *string
	Join        *string
}

// StyleObjects 原子更新对象透明度、文字颜色及路径填充和描边样式
// 入参: page 页面索引, ids 对象标识, style 待修改属性
// 返回: error 错误信息
func (e *Editor) StyleObjects(page int, ids []string, style ObjectStyle) error {
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	for i := range objects {
		if style.Fill != nil || style.Stroke != nil || style.FillColor != nil || style.StrokeColor != nil || style.LineWidth != nil {
			capability, err := e.ObjectCapabilities(page, ids[i])
			if err != nil {
				return err
			}
			if !capability.Paint {
				return fmt.Errorf("object %q cannot be painted: %w", ids[i], capability.editError())
			}
		}
		objects[i], err = e.styleObject(objects[i], style)
		if err != nil {
			return err
		}
	}
	return e.updateObjects(page, objects, true)
}

// styleObject 仅替换请求的外观字段，保留内容、定位及未修改样式
// 入参: object 原对象, style 待修改属性
// 返回: GraphicObject 修改后的独立对象, error 错误信息
func (e *Editor) styleObject(object GraphicObject, style ObjectStyle) (GraphicObject, error) {
	if style.Alpha != nil && (*style.Alpha < 0 || *style.Alpha > 255) {
		return GraphicObject{}, fmt.Errorf("alpha must be between 0 and 255")
	}
	stroke := style.DashPattern != nil || style.DashOffset != nil || style.Cap != nil || style.Join != nil
	paint := style.Fill != nil || style.Stroke != nil || style.StrokeColor != nil || style.LineWidth != nil
	if (stroke || paint) && object.Type != "PathObject" {
		return GraphicObject{}, fmt.Errorf("stroke style requires path objects")
	}
	if style.FillColor != nil && object.Type != "TextObject" && object.Type != "PathObject" {
		return GraphicObject{}, fmt.Errorf("fill color requires text or path objects")
	}
	for _, value := range []*FillColor{style.FillColor, (*FillColor)(style.StrokeColor)} {
		if value != nil {
			if err := e.editorColor(value); err != nil {
				return GraphicObject{}, err
			}
		}
	}
	switch object.Type {
	case "TextObject":
		if style.Alpha != nil {
			object.TextObject.Alpha = style.Alpha
		}
		if style.FillColor != nil {
			object.TextObject.FillColor = editorStyleColor(style.FillColor, object.TextObject.FillColor)
		}
	case "ImageObject":
		if style.Alpha != nil {
			object.ImageObject.Alpha = style.Alpha
		}
	case "CompositeObject", "CompositeGraphicUnit":
		if style.Alpha != nil {
			object.CompositeGraphicUnit.Alpha = style.Alpha
		}
	case "PathObject":
		path := &object.PathObject
		if style.Alpha != nil {
			path.Alpha = style.Alpha
		}
		if style.Fill != nil {
			path.Fill = style.Fill
		}
		if style.Stroke != nil {
			path.Stroke = style.Stroke
		}
		if style.FillColor != nil {
			path.FillColor = editorStyleColor(style.FillColor, path.FillColor)
		}
		if style.StrokeColor != nil {
			path.StrokeColor = (*StrokeColor)(editorStyleColor((*FillColor)(style.StrokeColor), (*FillColor)(path.StrokeColor)))
		}
		if style.LineWidth != nil {
			if !finite(*style.LineWidth) || *style.LineWidth <= 0 {
				return GraphicObject{}, fmt.Errorf("line width must be positive")
			}
			path.LineWidth = *style.LineWidth
		}
		if style.DashPattern != nil {
			path.DashPattern = *style.DashPattern
			path.dashPatternSet = path.DashPattern == ""
		}
		if style.DashOffset != nil {
			path.DashOffset = style.DashOffset
		}
		if style.Cap != nil {
			path.Cap = *style.Cap
		}
		if style.Join != nil {
			path.Join = *style.Join
		}
		if stroke {
			if err := validateEditorStroke(*path); err != nil {
				return GraphicObject{}, err
			}
		}
	default:
		return GraphicObject{}, fmt.Errorf("unsupported object type %q", object.Type)
	}
	return cloneEditorData(object), nil
}

// editorStyleColor 替换色值时保留未指定的颜色透明度
// 入参: color 新颜色, before 原有效颜色
// 返回: *FillColor 独立颜色
func editorStyleColor(color, before *FillColor) *FillColor {
	result := cloneEditorData(color)
	if result != nil && result.Alpha == nil && before != nil {
		result.Alpha = cloneEditorData(before.Alpha)
	}
	return result
}

// CopyStyle 将同类型对象的外观复制到选区，不复制内容、位置、资源数据或动作
// 文字复制字体、字号、颜色及透明度，沿用目标段落设置；字体标识须属于当前文档
// 路径按页面中的实际线宽和虚线长度复制，保持目标变换
// 入参: page 目标页面索引, ids 目标对象标识, source 样式来源快照
// 返回: error 错误信息
func (e *Editor) CopyStyle(page int, ids []string, source GraphicObject) (err error) {
	if err := e.validateCopyStyle(source); err != nil {
		return err
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) == 0 {
		return err
	}
	for _, object := range objects {
		if object.Type != source.Type {
			return fmt.Errorf("style requires objects of the same type")
		}
	}
	count, maximum := len(e.resources), e.maxID
	ready := e.source != nil && e.source.idsReady
	defer func() {
		if err != nil {
			clear(e.resources[count:])
			e.resources, e.maxID = e.resources[:count], maximum
			if e.source != nil {
				e.source.idsReady = ready
			}
		}
	}()
	if source.Type == "TextObject" && source.TextObject.FillColor == nil || source.Type == "PathObject" && (source.PathObject.FillColor == nil || source.PathObject.StrokeColor == nil) {
		black, err := e.RGBColor(color.NRGBA{A: 255})
		if err != nil {
			return err
		}
		if source.Type == "TextObject" {
			source.TextObject.FillColor = black
		} else {
			if source.PathObject.FillColor == nil {
				source.PathObject.FillColor = black
			}
			if source.PathObject.StrokeColor == nil {
				source.PathObject.StrokeColor = (*StrokeColor)(black)
			}
		}
	}
	if source.Type == "TextObject" {
		objects, err = e.styleTextObjects(objects, TextStyle{Font: source.TextObject.Font, Size: source.TextObject.Size})
		if err != nil {
			return err
		}
		for i := range objects {
			objects[i].TextObject.FillColor = cloneEditorData(source.TextObject.FillColor)
			objects[i].TextObject.Alpha = cloneEditorData(source.TextObject.Alpha)
		}
		return e.UpdateObjects(page, objects)
	}
	for i, object := range objects {
		object = cloneEditorData(object)
		switch source.Type {
		case "PathObject":
			object.PathObject = copyEditorPathStyle(object.PathObject, source.PathObject, editorStrokeScale(source.PathObject.CTM)/editorStrokeScale(object.PathObject.CTM))
		case "ImageObject":
			object.ImageObject.Alpha = source.ImageObject.Alpha
		}
		objects[i] = object
	}
	return e.updateObjects(page, objects, true)
}

// validateCopyStyle 校验样式来源，不分配资源或改写对象
// 入参: source 有效外观快照
// 返回: error 错误信息
func (e *Editor) validateCopyStyle(source GraphicObject) error {
	var alpha *int
	var colors []*FillColor
	switch source.Type {
	case "TextObject":
		alpha, colors = source.TextObject.Alpha, []*FillColor{source.TextObject.FillColor}
	case "PathObject":
		from := source.PathObject
		alpha, colors = from.Alpha, []*FillColor{from.FillColor, (*FillColor)(from.StrokeColor)}
		if from.DrawParam != "" {
			return fmt.Errorf("style source must have resolved draw parameters")
		}
		if err := validateEditorStroke(from); err != nil {
			return err
		}
	case "ImageObject":
		alpha = source.ImageObject.Alpha
	default:
		return fmt.Errorf("unsupported style source %q", source.Type)
	}
	if alpha != nil && (*alpha < 0 || *alpha > 255) {
		return fmt.Errorf("alpha must be between 0 and 255")
	}
	for _, color := range colors {
		if err := e.editorColor(color); err != nil {
			return err
		}
	}
	return nil
}

// copyEditorPathStyle 按页面描边倍率复制路径外观，保留目标几何和行为
// 入参: to 目标路径, from 来源路径, scale 来源与目标的描边倍率比
// 返回: PathObject 独立路径副本
func copyEditorPathStyle(to, from PathObject, scale float64) PathObject {
	from = cloneEditorData(from)
	to.Fill, to.Stroke, to.Alpha = from.Fill, from.Stroke, from.Alpha
	to.FillColor, to.StrokeColor = from.FillColor, from.StrokeColor
	to.LineWidth, to.Cap, to.Join, to.MiterLimit = from.LineWidth, from.Cap, from.Join, from.MiterLimit
	to.DashPattern, to.DashOffset, to.dashPatternSet = from.DashPattern, from.DashOffset, true
	if to.LineWidth == 0 {
		to.LineWidth = defaultPathLineWidth
	}
	if to.Cap == "" {
		to.Cap = "Butt"
	}
	if to.Join == "" {
		to.Join = "Miter"
	}
	if to.MiterLimit == 0 {
		to.MiterLimit = defaultMiterLimit
	}
	if to.DashOffset == nil {
		zero := 0.0
		to.DashOffset = &zero
	}
	if scale != 1 {
		to.LineWidth *= scale
		to.DashPattern = scaleTextNumbers(to.DashPattern, scale)
		value := *to.DashOffset * scale
		to.DashOffset = &value
	}
	return to
}

// editorStrokeScale 获取路径在页面坐标中的描边倍率，与渲染器保持一致
// 入参: ctm 对象变换
// 返回: float64 描边倍率
func editorStrokeScale(ctm string) float64 {
	m := NewMatrix(ctm)
	if scale := math.Sqrt(math.Abs(m.a*m.d - m.b*m.c)); scale > 0 {
		return scale
	}
	return 1
}

// SetImageBorders 原子替换图片边框，nil移除边框
// 入参: page 页面索引, ids 图片标识, border 边框样式
// 返回: error 错误信息
func (e *Editor) SetImageBorders(page int, ids []string, border *ImageBorder) error {
	if err := e.validateImageBorder(border); err != nil {
		return err
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	for i := range objects {
		if objects[i].Type != "ImageObject" {
			return fmt.Errorf("object %q is not an image", ids[i])
		}
		objects[i].ImageObject.Border = cloneEditorData(border)
	}
	return e.updateObjects(page, objects, true)
}

// validateImageBorder 校验图片边框的线型、圆角和绘制颜色
// 入参: border 边框
// 返回: error 错误信息
func (e *Editor) validateImageBorder(border *ImageBorder) error {
	if border == nil {
		return nil
	}
	for _, radius := range []float64{border.HorizonalCornerRadius, border.VerticalCornerRadius} {
		if !finite(radius) || radius < 0 {
			return fmt.Errorf("image corner radius must be finite and nonnegative")
		}
	}
	stroke := PathObject{DashPattern: border.DashPattern, DashOffset: &border.DashOffset}
	if border.LineWidth != nil {
		stroke.LineWidth = *border.LineWidth
	}
	if err := validateEditorStroke(stroke); err != nil {
		return err
	}
	return e.editorColor((*FillColor)(border.BorderColor))
}

// validateEditorStroke 校验路径描边尺寸、端点和虚线
// 入参: path 路径对象
// 返回: error 错误信息
func validateEditorStroke(path PathObject) error {
	if path.Join != "" && path.Join != "Miter" && path.Join != "Round" && path.Join != "Bevel" {
		return fmt.Errorf("invalid line join %q", path.Join)
	}
	if !finite(path.LineWidth) || path.LineWidth < 0 || !finite(path.MiterLimit) || path.MiterLimit < 0 {
		return fmt.Errorf("invalid path stroke dimensions")
	}
	if path.Cap != "" && path.Cap != "Butt" && path.Cap != "Round" && path.Cap != "Square" {
		return fmt.Errorf("invalid line cap %q", path.Cap)
	}
	if path.DashOffset != nil && !finite(*path.DashOffset) {
		return fmt.Errorf("invalid dash offset")
	}
	if path.DashPattern == "" {
		return nil
	}
	values, err := creationNumbers(path.DashPattern, len(strings.Fields(path.DashPattern)))
	if err != nil {
		return err
	}
	total := 0.0
	for _, value := range values {
		if value < 0 {
			return fmt.Errorf("dash lengths must not be negative")
		}
		total += value
	}
	if total == 0 {
		return fmt.Errorf("dash pattern must have a positive length")
	}
	return nil
}
