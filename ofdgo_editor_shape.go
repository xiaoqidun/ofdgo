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

import "fmt"

// PathStyle 描述路径对象的页面坐标样式增量，nil字段保持原值
// LineWidth为页面坐标毫米，应用时按对象CTM自动折算为局部线宽
// 直线与箭头路径自动禁填并保留描边，非直线路径不可同时关闭填充与描边
type PathStyle struct {
	Fill        *bool
	Stroke      *bool
	FillColor   *FillColor
	StrokeColor *StrokeColor
	LineWidth   *float64
	Alpha       *int
	Cap         *string
	Join        *string
	MiterLimit  *float64
	DashPattern *string
	DashOffset  *float64
}

// StylePath 按页面坐标增量更新单个路径样式，保留未指定属性并处理CTM线宽折算与直线禁填规则
// 可用于NewShape创建后配置样式，或配合Editor.UpdateObject写入文档
// 入参: object 原路径对象, style 样式增量
// 返回: PathObject 独立更新后的路径对象, error 样式校验错误
func StylePath(object PathObject, style PathStyle) (PathObject, error) {
	object = cloneEditorData(object)
	if style.Alpha != nil {
		if *style.Alpha < 0 || *style.Alpha > 255 {
			return PathObject{}, fmt.Errorf("alpha must be between 0 and 255")
		}
		alpha := *style.Alpha
		object.Alpha = &alpha
	}
	fill := object.Fill != nil && *object.Fill
	stroke := object.Stroke == nil || *object.Stroke
	if style.Fill != nil {
		fill = *style.Fill
	}
	if style.Stroke != nil {
		stroke = *style.Stroke
	}
	if kind, _ := object.Shape(); lineShapeKind(kind) {
		fill, stroke = false, true
	}
	if !fill && !stroke {
		if style.Fill == nil || style.Stroke == nil {
			stroke = true
		} else {
			return PathObject{}, fmt.Errorf("fill or stroke must be enabled")
		}
	}
	if style.Fill != nil || fill != (object.Fill != nil && *object.Fill) {
		value := fill
		object.Fill = &value
	}
	if style.Stroke != nil || stroke != (object.Stroke == nil || *object.Stroke) {
		value := stroke
		object.Stroke = &value
	}
	if style.LineWidth != nil {
		width := *style.LineWidth
		if !finite(width) || width < 0 {
			return PathObject{}, fmt.Errorf("line width must be nonnegative and finite")
		}
		if scale := editorStrokeScale(object.CTM); width != object.LineWidth*scale {
			object.LineWidth = width / scale
		}
		object.LineWidthSet = width == 0
	}
	if style.FillColor != nil {
		object.FillColor = editorStyleColor(style.FillColor, object.FillColor)
	}
	if style.StrokeColor != nil {
		object.StrokeColor = (*StrokeColor)(editorStyleColor((*FillColor)(style.StrokeColor), (*FillColor)(object.StrokeColor)))
	}
	strokeAttrs := style.Cap != nil || style.Join != nil || style.MiterLimit != nil || style.DashPattern != nil || style.DashOffset != nil
	if style.Cap != nil {
		object.Cap = *style.Cap
	}
	if style.Join != nil {
		object.Join = *style.Join
	}
	if style.MiterLimit != nil {
		object.MiterLimit = *style.MiterLimit
		object.MiterLimitSet = object.MiterLimit == 0
	}
	if style.DashPattern != nil {
		object.DashPattern = *style.DashPattern
		object.DashPatternSet = object.DashPattern == ""
	}
	if style.DashOffset != nil {
		object.DashOffset = cloneEditorData(style.DashOffset)
	}
	if strokeAttrs {
		if err := validateEditorStroke(object); err != nil {
			return PathObject{}, err
		}
	}
	return object, nil
}

// UpdatePathStyle 原子更新选中路径对象的页面坐标样式，保留未修改属性并计入一次撤销记录
// 线宽按各对象CTM折算为局部线宽，直线与箭头路径自动禁填并保持描边
// 入参: page 页面索引, ids 不重复的路径对象标识列表, style 样式增量
// 返回: error 校验或更新错误
func (e *Editor) UpdatePathStyle(page int, ids []string, style PathStyle) error {
	defer e.bindPageIndex(page)()
	for _, value := range []*FillColor{style.FillColor, (*FillColor)(style.StrokeColor)} {
		if value != nil {
			if err := e.editorColor(value); err != nil {
				return err
			}
		}
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) == 0 {
		return err
	}
	paint := style.Fill != nil || style.Stroke != nil || style.FillColor != nil || style.StrokeColor != nil || style.LineWidth != nil || style.Cap != nil || style.Join != nil || style.MiterLimit != nil || style.DashPattern != nil || style.DashOffset != nil
	for i := range objects {
		if objects[i].Type != "PathObject" {
			return fmt.Errorf("object %q is not a path", ids[i])
		}
		if paint {
			capability, err := e.ObjectCapabilities(page, ids[i])
			if err != nil {
				return err
			}
			if !capability.Paint {
				return fmt.Errorf("object %q cannot be painted: %w", ids[i], capability.editError())
			}
		}
		updated, err := StylePath(objects[i].PathObject, style)
		if err != nil {
			return err
		}
		objects[i].PathObject = updated
	}
	return e.updateObjects(page, objects, true)
}
