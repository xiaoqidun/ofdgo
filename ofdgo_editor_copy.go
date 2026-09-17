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
	"strconv"
)

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
		if object.origin.editor == e || object.origin.page != nil && e.source != nil && e.source.pages[object.origin.page.ref.ID] == object.origin.page {
			return object.origin
		}
		return nil
	}
	return e.objectOrigin(editorObjectID(object))
}

// copyObjectOrigin 复制原XML并重映射内部标识和引用，不修改共享资源与原对象
// 入参: before 输入快照, after 副本快照, maximum 本批次最大标识
// 返回: GraphicObject 副本, *editorObjectOrigin 副本原文, error 错误信息
func (e *Editor) copyObjectOrigin(before, after GraphicObject, maximum *int) (GraphicObject, *editorObjectOrigin, error) {
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
				ids[id] = strconv.Itoa(*maximum)
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
		if node.name.Space == "" || node.name.Space == ofdNamespace || node.name.Space == "http://www.ofdspec.org" {
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
			if node.name.Local == "Font" || node.name.Local == "Substitution" {
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
