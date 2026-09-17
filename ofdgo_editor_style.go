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
	"math"
	"strings"
)

// editorDrawParam 解析原文档的绘制参数继承链，缺失或循环引用不进入编辑快照
// 入参: id 绘制参数标识, visited 已访问标识
// 返回: *DrawParam 合并参数, error 错误信息
func (e *Editor) editorDrawParam(id string, visited map[string]bool) (*DrawParam, error) {
	if id == "" {
		return &DrawParam{}, nil
	}
	dp := e.source.reader.drawParamCache[id]
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
	dp, err := e.editorDrawParam(id, make(map[string]bool))
	if err != nil {
		return GraphicObject{}, err
	}
	style := mergeDrawParam(*base, dp)
	if object.Type == "PathObject" {
		obj := &object.PathObject
		style = mergeDrawParam(*style, &DrawParam{LineWidth: obj.LineWidth, Join: obj.Join, Cap: obj.Cap,
			MiterLimit: obj.MiterLimit, DashPattern: obj.DashPattern, DashOffset: obj.DashOffset,
			FillColor: obj.FillColor, StrokeColor: obj.StrokeColor})
		obj.DrawParam = ""
		obj.LineWidth, obj.Join, obj.Cap = style.LineWidth, style.Join, style.Cap
		obj.MiterLimit, obj.DashPattern, obj.DashOffset = style.MiterLimit, style.DashPattern, style.DashOffset
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
		if err := e.editorColor(color); err != nil {
			return GraphicObject{}, &EditError{Code: EditUnsupportedColor, Err: fmt.Errorf("unsupported draw parameter color: %w", err)}
		}
	}
	return cloneEditorObject(object)
}

// ObjectStyle 对象外观，nil字段保持原值，长度单位为对象坐标系中的毫米
type ObjectStyle struct {
	Alpha       *int
	DashPattern *string
	DashOffset  *float64
	Cap         *string
	Join        *string
}

// StyleObjects 原子更新文字、图片、路径与复合对象透明度，路径另支持描边样式
// 入参: page 页面索引, ids 对象标识, style 待修改属性
// 返回: error 错误信息
func (e *Editor) StyleObjects(page int, ids []string, style ObjectStyle) error {
	if style.Alpha != nil && (*style.Alpha < 0 || *style.Alpha > 255) {
		return fmt.Errorf("alpha must be between 0 and 255")
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	stroke := style.DashPattern != nil || style.DashOffset != nil || style.Cap != nil || style.Join != nil
	for i := range objects {
		object := cloneEditorData(objects[i])
		if stroke && object.Type != "PathObject" {
			return fmt.Errorf("stroke style requires path objects")
		}
		switch object.Type {
		case "TextObject":
			if style.Alpha != nil {
				object.TextObject.Alpha = style.Alpha
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
			if style.DashPattern != nil {
				path.DashPattern = *style.DashPattern
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
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported object type %q", object.Type)
		}
		objects[i] = object
	}
	return e.updateObjects(page, objects, true)
}

// CopyStyle 将同类型对象的外观复制到选区，不复制内容、位置、资源数据或动作
// 文字复制字体、字号、颜色及透明度，沿用目标段落设置；字体标识须属于当前文档
// 路径按页面中的实际线宽和虚线长度复制，保持目标变换
// 入参: page 目标页面索引, ids 目标对象标识, source 样式来源快照
// 返回: error 错误信息
func (e *Editor) CopyStyle(page int, ids []string, source GraphicObject) error {
	var alpha *int
	switch source.Type {
	case "TextObject":
		alpha = source.TextObject.Alpha
	case "PathObject":
		alpha = source.PathObject.Alpha
	case "ImageObject":
		alpha = source.ImageObject.Alpha
	default:
		return fmt.Errorf("unsupported style source %q", source.Type)
	}
	if alpha != nil && (*alpha < 0 || *alpha > 255) {
		return fmt.Errorf("alpha must be between 0 and 255")
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.Type != source.Type {
			return fmt.Errorf("style requires objects of the same type")
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
	if source.Type == "PathObject" {
		from := source.PathObject
		if from.DrawParam != "" {
			return fmt.Errorf("style source must have resolved draw parameters")
		}
		if err := validateEditorStroke(from); err != nil {
			return err
		}
		for _, color := range []*FillColor{from.FillColor, (*FillColor)(from.StrokeColor)} {
			if err := e.editorColor(color); err != nil {
				return err
			}
		}
	}
	for i, object := range objects {
		object = cloneEditorData(object)
		switch source.Type {
		case "PathObject":
			from, to := source.PathObject, &object.PathObject
			to.Fill, to.Stroke, to.Alpha = from.Fill, from.Stroke, from.Alpha
			to.FillColor, to.StrokeColor = from.FillColor, from.StrokeColor
			to.LineWidth, to.Cap, to.Join, to.MiterLimit = from.LineWidth, from.Cap, from.Join, from.MiterLimit
			to.DashPattern, to.DashOffset = from.DashPattern, from.DashOffset
			scale := editorStrokeScale(from.CTM) / editorStrokeScale(to.CTM)
			if scale != 1 {
				if to.LineWidth == 0 {
					to.LineWidth = defaultPathLineWidth
				}
				to.LineWidth *= scale
				to.DashPattern = scaleTextNumbers(to.DashPattern, scale)
				if to.DashOffset != nil {
					value := *to.DashOffset * scale
					to.DashOffset = &value
				}
			}
		case "ImageObject":
			object.ImageObject.Alpha = source.ImageObject.Alpha
		}
		objects[i] = object
	}
	return e.updateObjects(page, objects, true)
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
