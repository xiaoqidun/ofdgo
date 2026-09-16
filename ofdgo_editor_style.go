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
		if err := creationColor(color); err != nil {
			return GraphicObject{}, &EditError{Code: EditUnsupportedColor, Err: fmt.Errorf("unsupported draw parameter color: %w", err)}
		}
	}
	return cloneEditorObject(object)
}
