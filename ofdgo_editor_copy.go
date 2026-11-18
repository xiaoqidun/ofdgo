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
	"bytes"
	"encoding/xml"
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// editorClonePlans 仅共享类型的可变字段索引，不持有文档对象或资源
var editorClonePlans sync.Map

// copiedObjectStyle 保留复杂路径的原始颜色引用，普通对象沿用有效样式快照
// 入参: object 原对象, layer 图层绘制参数
// 返回: GraphicObject 复制快照, error 错误信息
func (e *Editor) copiedObjectStyle(object GraphicObject, layer string) (GraphicObject, error) {
	result, err := e.resolveEditorStyle(object, layer)
	if err != nil && object.Type == "PathObject" && editReason(err) == EditUnsupportedColor {
		return cloneEditorData(object), nil
	}
	return result, err
}

// snapshotOrigin 获取当前文档内捕获时的原文来源，外部快照按普通对象校验
// 入参: object 对象快照
// 返回: *editorObjectOrigin 原文来源
func (e *Editor) snapshotOrigin(object GraphicObject) *editorObjectOrigin {
	if object.origin != nil {
		if object.origin.editor == e && e.documentIdentity().matches(object.origin.document) || object.origin.page != nil && e.source != nil && e.source.pages[object.origin.page.ref.ID] == object.origin.page {
			return object.origin
		}
		return nil
	}
	return e.objectOrigin(editorObjectID(object))
}

// copyObjectOrigin 复制原XML并重映射内部标识和引用，不修改共享资源与原对象
// 入参: before 输入快照, after 副本快照, maximum 本批次最大标识
// 返回: GraphicObject 副本, *editorObjectOrigin 副本原文, error 错误信息
func (e *Editor) copyObjectOrigin(before, after GraphicObject, maximum *uint64) (GraphicObject, *editorObjectOrigin, error) {
	origin := e.snapshotOrigin(before)
	if origin == nil {
		return after, nil, nil
	}
	rootID := editorResourceID(origin.node.attr("ID"))
	ids := map[string]string{rootID: editorObjectID(after)}
	selfReference := false
	var collect func(*editorXML) error
	collect = func(node *editorXML) error {
		for _, attr := range node.attrs {
			if attr.Name.Space == "" && editorObjectReference(attr.Name.Local) && editorResourceID(attr.Value) == rootID {
				selfReference = true
			}
		}
		if (node.name.Local == "Font" || node.name.Local == "Substitution") && editorResourceID(editorImportText(origin.data, node)) == rootID {
			selfReference = true
		}
		for _, child := range node.children {
			if id := editorResourceID(child.attr("ID")); id != "" {
				if ids[id] != "" {
					return fmt.Errorf("duplicate object ID %q", id)
				}
				*maximum += 1
				ids[id] = formatUnitID(*maximum)
			}
			if err := collect(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(origin.node); err != nil {
		return GraphicObject{}, nil, err
	}
	if len(ids) == 1 && !selfReference {
		return after, origin, nil
	}
	data, err := editorXMLObject(origin.data, origin.node, origin.object, after)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	data, err = editorXMLStandalone(data, origin.node)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	data, err = editorXMLRemapIDs(data, root, ids)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	root, err = parseEditorXML(data)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	result := GraphicObject{Type: after.Type}
	var target any
	switch result.Type {
	case "TextObject":
		target = &result.TextObject
	case "PathObject":
		target = &result.PathObject
	case "ImageObject":
		target = &result.ImageObject
	case "CompositeObject", "CompositeGraphicUnit":
		target = &result.CompositeGraphicUnit
	}
	if err := xml.Unmarshal(data, target); err != nil {
		return GraphicObject{}, nil, err
	}
	result.TextObject.layout = after.TextObject.layout
	result.state = after.state
	result.CompositeGraphicUnit.states = remapCompositeStates(after.CompositeGraphicUnit.states, ids)
	root.parent = origin.node.parent
	return result, &editorObjectOrigin{page: origin.page, data: data, node: root, object: result}, nil
}

// editorXMLRemapIDs 仅重写标准标识和指向副本内部的引用，保留扩展、文字和XML结构
// 入参: data 独立片段, root 根节点, ids 原标识与新标识
// 返回: []byte 修改后的片段, error 错误信息
func editorXMLRemapIDs(data []byte, root *editorXML, ids map[string]string) ([]byte, error) {
	var patches []editorXMLPatch
	var visit func(*editorXML) error
	visit = func(node *editorXML) error {
		if classifyOFDNamespace(node.name.Space) != ofdXMLUnknown {
			decoder := xml.NewDecoder(bytes.NewReader(data[node.start:node.open]))
			token, err := decoder.RawToken()
			if err != nil {
				return err
			}
			start := token.(xml.StartElement)
			changed := false
			for i := range start.Attr {
				attr := &start.Attr[i]
				if attr.Name.Space != "" {
					continue
				}
				if attr.Name.Local == "ID" || editorObjectReference(attr.Name.Local) {
					if value := ids[editorResourceID(attr.Value)]; value != "" {
						attr.Value, changed = value, true
					}
				}
			}
			if changed {
				if start.Name.Space != "" {
					start.Name.Local = start.Name.Space + ":" + start.Name.Local
					start.Name.Space = ""
				}
				for i := range start.Attr {
					attr := &start.Attr[i]
					if attr.Name.Space != "" {
						attr.Name.Local = attr.Name.Space + ":" + attr.Name.Local
						attr.Name.Space = ""
					}
				}
				var header bytes.Buffer
				encoder := xml.NewEncoder(&header)
				if err := encoder.EncodeToken(start); err != nil {
					return err
				}
				if err := encoder.Flush(); err != nil {
					return err
				}
				value := header.Bytes()
				if node.open == node.end {
					value = append(value[:len(value)-1], '/', '>')
				}
				patches = append(patches, editorXMLPatch{node.start, node.open, value})
			}
			if node.name.Local == "Font" || node.name.Local == "Substitution" || node.name.Local == "Thumbnail" {
				if value := ids[editorResourceID(editorImportText(data, node))]; value != "" {
					patches = append(patches, editorXMLContent(data, node, []byte(value)))
				}
			}
		}
		for _, child := range node.children {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return editorPatchXML(data, patches), nil
}

// editorObjectReference 判断对象内可按ST_RefID重映射的标准属性
// 入参: name 属性名
// 返回: bool 是否为标识引用
func editorObjectReference(name string) bool {
	switch name {
	case "Font", "ResourceID", "Substitution", "ImageMask", "Relative", "DrawParam", "ColorSpace", "Thumbnail", "TemplateID", "PageID", "PageRef", "RefId":
		return true
	}
	return false
}

// copiedLayerStyle 保留复合对象和复杂路径的图层参数，普通对象使用已解析样式
// 入参: object 对象
// 返回: string 绘制参数标识
func (e *Editor) copiedLayerStyle(object GraphicObject) string {
	if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" || object.Type == "PathObject" {
		if origin := e.snapshotOrigin(object); origin != nil {
			for parent := origin.node.parent; parent != nil; parent = parent.parent {
				if parent.name.Local == "Layer" {
					id := parent.attr("DrawParam")
					if object.Type == "PathObject" {
						if _, err := e.resolveEditorStyle(object, id); err == nil || editReason(err) != EditUnsupportedColor {
							return ""
						}
					}
					return id
				}
			}
		}
	}
	return ""
}

// cloneEditorData 深复制编辑快照，未导出的排版及原文来源保持不可变共享
// 入参: value 原值
// 返回: T 独立副本
func cloneEditorData[T any](value T) T {
	switch source := any(value).(type) {
	case GraphicObject:
		return any(copyEditorGraphicObject(source)).(T)
	case TextObject:
		return any(copyEditorTextObject(source)).(T)
	case PathObject:
		return any(copyEditorPathObject(source)).(T)
	case ImageObject:
		return any(copyEditorImageObject(source)).(T)
	case CompositeGraphicUnit:
		return any(copyEditorCompositeGraphicUnit(source)).(T)
	case PageContent:
		return any(copyEditorPageContent(source)).(T)
	case Layer:
		return any(copyEditorLayer(source)).(T)
	}
	source := reflect.ValueOf(value)
	if !source.IsValid() {
		return value
	}
	return cloneEditorValue(source).Interface().(T)
}

// cloneEditorValue 复制公开指针、切片及数组，纯值数据不创建额外结构
// 入参: value 原值
// 返回: reflect.Value 独立副本
func cloneEditorValue(value reflect.Value) reflect.Value {
	if !value.IsValid() || !editorCloneMutable(value.Type()) {
		return value
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return value
		}
		result := reflect.New(value.Type().Elem())
		cloneEditorInto(result.Elem(), value.Elem())
		return result
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		if !editorCloneMutable(value.Type().Elem()) {
			reflect.Copy(result, value)
		} else {
			for i := range value.Len() {
				cloneEditorInto(result.Index(i), value.Index(i))
			}
		}
		return result
	default:
		result := reflect.New(value.Type()).Elem()
		cloneEditorInto(result, value)
		return result
	}
}

// cloneEditorInto 直接填充目标结构，避免指针及切片成员的二次结构分配
// 入参: target 可写目标, source 原值
func cloneEditorInto(target, source reflect.Value) {
	switch source.Kind() {
	case reflect.Struct:
		target.Set(source)
		for _, i := range editorCloneFields(source.Type()) {
			cloneEditorInto(target.Field(i), source.Field(i))
		}
	case reflect.Array:
		target.Set(source)
		if editorCloneMutable(source.Type().Elem()) {
			for i := range source.Len() {
				cloneEditorInto(target.Index(i), source.Index(i))
			}
		}
	default:
		target.Set(cloneEditorValue(source))
	}
}

// editorCloneMutable 判断类型是否含有需要隔离的公开可变成员
// 入参: kind 数据类型
// 返回: bool 是否需要独立复制
func editorCloneMutable(kind reflect.Type) bool {
	switch kind.Kind() {
	case reflect.Pointer, reflect.Slice:
		return true
	case reflect.Array:
		return editorCloneMutable(kind.Elem())
	case reflect.Struct:
		return len(editorCloneFields(kind)) != 0
	}
	return false
}

// editorCloneFields 按类型复用公开可变字段，索引发布后保持只读
// 入参: kind 结构类型
// 返回: []int 可变字段索引
func editorCloneFields(kind reflect.Type) []int {
	if cached, ok := editorClonePlans.Load(kind); ok {
		return cached.([]int)
	}
	var fields []int
	for i := range kind.NumField() {
		field := kind.Field(i)
		if field.IsExported() && editorCloneMutable(field.Type) {
			fields = append(fields, i)
		}
	}
	actual, _ := editorClonePlans.LoadOrStore(kind, fields)
	return actual.([]int)
}

// copyEditorGraphicObject 复制GraphicObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: GraphicObject 独立副本
func copyEditorGraphicObject(value GraphicObject) GraphicObject {
	value.TextObject = copyEditorTextObject(value.TextObject)
	value.PathObject = copyEditorPathObject(value.PathObject)
	value.ImageObject = copyEditorImageObject(value.ImageObject)
	value.CompositeGraphicUnit = copyEditorCompositeGraphicUnit(value.CompositeGraphicUnit)
	return value
}

// copyEditorTextObject 复制TextObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: TextObject 独立副本
func copyEditorTextObject(value TextObject) TextObject {
	if value.DashOffset != nil {
		copied := *value.DashOffset
		value.DashOffset = &copied
	}
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	if value.Fill != nil {
		copied := *value.Fill
		value.Fill = &copied
	}
	if value.Stroke != nil {
		copied := *value.Stroke
		value.Stroke = &copied
	}
	value.StrokeColor = cloneEditorData(value.StrokeColor)
	value.FillColor = cloneEditorData(value.FillColor)
	value.CGTransform = slices.Clone(value.CGTransform)
	value.TextCode = slices.Clone(value.TextCode)
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorPathObject 复制PathObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: PathObject 独立副本
func copyEditorPathObject(value PathObject) PathObject {
	if value.DashOffset != nil {
		copied := *value.DashOffset
		value.DashOffset = &copied
	}
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	if value.Stroke != nil {
		copied := *value.Stroke
		value.Stroke = &copied
	}
	if value.Fill != nil {
		copied := *value.Fill
		value.Fill = &copied
	}
	value.StrokeColor = cloneEditorData(value.StrokeColor)
	value.FillColor = cloneEditorData(value.FillColor)
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorImageObject 复制ImageObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: ImageObject 独立副本
func copyEditorImageObject(value ImageObject) ImageObject {
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	value.Border = cloneEditorData(value.Border)
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorCompositeGraphicUnit 复制CompositeGraphicUnit的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: CompositeGraphicUnit 独立副本
func copyEditorCompositeGraphicUnit(value CompositeGraphicUnit) CompositeGraphicUnit {
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	value.Objects = slices.Clone(value.Objects)
	for i := range value.Objects {
		value.Objects[i] = copyEditorGraphicObject(value.Objects[i])
	}
	value.TextObject = slices.Clone(value.TextObject)
	for i := range value.TextObject {
		value.TextObject[i] = copyEditorTextObject(value.TextObject[i])
	}
	value.PathObject = slices.Clone(value.PathObject)
	for i := range value.PathObject {
		value.PathObject[i] = copyEditorPathObject(value.PathObject[i])
	}
	value.ImageObject = slices.Clone(value.ImageObject)
	for i := range value.ImageObject {
		value.ImageObject[i] = copyEditorImageObject(value.ImageObject[i])
	}
	value.CompositeGraphicUnit = slices.Clone(value.CompositeGraphicUnit)
	for i := range value.CompositeGraphicUnit {
		value.CompositeGraphicUnit[i] = copyEditorCompositeGraphicUnit(value.CompositeGraphicUnit[i])
	}
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorPageContent 复制PageContent的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: PageContent 独立副本
func copyEditorPageContent(value PageContent) PageContent {
	value.Template = slices.Clone(value.Template)
	value.PageRes = slices.Clone(value.PageRes)
	value.Content.Layer = slices.Clone(value.Content.Layer)
	for i := range value.Content.Layer {
		value.Content.Layer[i] = copyEditorLayer(value.Content.Layer[i])
	}
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorLayer 复制Layer的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: Layer 独立副本
func copyEditorLayer(value Layer) Layer {
	value.Objects = slices.Clone(value.Objects)
	for i := range value.Objects {
		value.Objects[i] = copyEditorGraphicObject(value.Objects[i])
	}
	value.TextObject = slices.Clone(value.TextObject)
	for i := range value.TextObject {
		value.TextObject[i] = copyEditorTextObject(value.TextObject[i])
	}
	value.PathObject = slices.Clone(value.PathObject)
	for i := range value.PathObject {
		value.PathObject[i] = copyEditorPathObject(value.PathObject[i])
	}
	value.ImageObject = slices.Clone(value.ImageObject)
	for i := range value.ImageObject {
		value.ImageObject[i] = copyEditorImageObject(value.ImageObject[i])
	}
	value.CompositeGraphicUnit = slices.Clone(value.CompositeGraphicUnit)
	for i := range value.CompositeGraphicUnit {
		value.CompositeGraphicUnit[i] = copyEditorCompositeGraphicUnit(value.CompositeGraphicUnit[i])
	}
	return value
}
