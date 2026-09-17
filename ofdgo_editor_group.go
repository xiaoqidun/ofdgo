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
	"maps"
	"slices"
	"strconv"
)

// GroupObjects 将同一容器内连续的对象组合，保留标识、绘制顺序及原始内容
// 入参: page 页面索引, ids 至少两个对象标识
// 返回: string 组合标识, error 错误信息
func (e *Editor) GroupObjects(page int, ids []string) (string, error) {
	_, indexes, err := e.selectedObjects(page, ids)
	if err != nil {
		return "", err
	}
	if len(indexes) < 2 {
		return "", fmt.Errorf("grouping requires at least two objects")
	}
	slices.SortFunc(indexes, compareEditorPosition)
	layer := &e.pages[page].Content.Layer[indexes[0].layer]
	parent, siblings := e.objectOrderIndexes(page, layer, editorObjectID(layer.Objects[indexes[0].index]))
	first := slices.Index(siblings, indexes[0].index)
	for i, index := range indexes {
		id := editorObjectID(e.pages[page].Content.Layer[index.layer].Objects[index.index])
		capability, err := e.ObjectCapabilities(page, id)
		if err != nil {
			return "", err
		}
		if index.layer != indexes[0].layer || first+i >= len(siblings) || siblings[first+i] != index.index || !capability.Copy || !capability.Order {
			return "", fmt.Errorf("grouping requires consecutive objects in the same container")
		}
	}
	var content []byte
	var source *editorSourcePage
	states := make(map[string]editorCompositeState)
	for _, index := range indexes {
		object := layer.Objects[index.index]
		var data []byte
		if origin := e.objectOrigin(editorObjectID(object)); origin != nil {
			source = origin.page
			data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
			if err == nil {
				data, err = editorXMLStandalone(data, origin.node)
			}
		} else {
			data, err = editorObjectXML(object)
		}
		if err != nil {
			return "", err
		}
		content = append(content, bytes.TrimPrefix(data, []byte(xml.Header))...)
		maps.Copy(states, object.CompositeGraphicUnit.states)
		state := object.state
		if object.Type == "TextObject" && object.TextObject.layout != nil {
			state.layout = object.TextObject.layout
			state.origin[0], _ = strconv.ParseFloat(object.TextObject.TextCode[0].X, 64)
			state.origin[1], _ = strconv.ParseFloat(object.TextObject.TextCode[0].Y, 64)
		}
		states[editorObjectID(object)] = state
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	id := strconv.Itoa(e.maxID + 1)
	content, err = editorXMLContainer("Content", nil, content)
	if err != nil {
		return "", err
	}
	data, err := editorXMLContainer("CompositeObject", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}, {Name: xml.Name{Local: "Boundary"}, Value: "0 0 1 1"}}, bytes.TrimPrefix(content, []byte(xml.Header)))
	if err != nil {
		return "", err
	}
	group, err := newEditorCompositeNode(data)
	if err != nil {
		return "", err
	}
	if parent == nil {
		parent = &editorXML{name: xml.Name{Local: "Layer"}, attrs: []xml.Attr{{Name: xml.Name{Local: "ID"}, Value: layer.ID}}, parent: &editorXML{name: xml.Name{Local: "Content"}}}
	}
	group.node.parent = parent
	group.object.CompositeGraphicUnit.states = states
	origin := &editorObjectOrigin{page: source, data: data, node: group.node, object: group.object}
	e.maxID++
	e.replaceGroup(page, indexes, []GraphicObject{group.object}, map[string]*editorObjectOrigin{id: origin})
	return id, nil
}

// UngroupObject 展开组合，保留页面外观与裁剪，资源引用分配独立标识，含容器行为或扩展的组合不拆解
// 入参: page 页面索引, id 组合标识
// 返回: []string 展开后的对象标识, error 错误信息
func (e *Editor) UngroupObject(page int, id string) ([]string, error) {
	_, indexes, err := e.selectedObjects(page, []string{id})
	if err != nil {
		return nil, err
	}
	origin := e.objectOrigin(id)
	if origin == nil || !editorContainerOrderable(origin.node.parent) {
		return nil, fmt.Errorf("group container cannot be removed without losing attributes")
	}
	reader, renderer, root, _, err := e.compositeScope(page, ObjectPath{ID: id})
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var result []string
	err = e.ungroupCompositeNode(renderer, root, func(objects []GraphicObject) error {
		origins := make(map[string]*editorObjectOrigin, len(objects))
		for i := range objects {
			object := &objects[i]
			copy := *object.origin
			copy.node.parent = origin.node.parent
			object.origin = nil
			id := editorObjectID(*object)
			origins[id] = &copy
			result = append(result, id)
		}
		e.replaceGroup(page, indexes, objects, origins)
		return nil
	})
	return result, err
}

// editorUngroupable 判断移除容器是否会丢失对象行为或未知属性
// 入参: node 容器节点
// 返回: bool 是否可解组
func editorUngroupable(node *editorXML) bool {
	if node.name.Local != "CompositeObject" && node.name.Local != "CompositeGraphicUnit" {
		return false
	}
	if !editorXMLAttributes(node, "ID Boundary CTM DrawParam Alpha Visible ResourceID Width Height") {
		return false
	}
	for _, child := range node.children {
		switch child.name.Local {
		case "Content":
			if !editorXMLAttributes(child, "") {
				return false
			}
			for _, member := range child.children {
				if member.name.Space != "" && member.name.Space != ofdNamespace && member.name.Space != "http://www.ofdspec.org" || !slices.Contains([]string{"TextObject", "PathObject", "ImageObject", "CompositeObject", "CompositeGraphicUnit"}, member.name.Local) {
					return false
				}
			}
		case "Clips":
			if !editorXMLSupported(child) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// replaceGroup 原子替换同容器连续成员及原文来源，提交一次撤销记录
// 入参: page 页面索引, indexes 原对象位置, objects 新成员, origins 新成员原文
func (e *Editor) replaceGroup(page int, indexes []editorObjectPosition, objects []GraphicObject, origins map[string]*editorObjectOrigin) {
	before := copyEditorPage(e.pages[page]).Content.Layer
	after := copyEditorPage(e.pages[page]).Content.Layer
	layer := &after[indexes[0].layer]
	layer.Objects = slices.Replace(layer.Objects, indexes[0].index, indexes[len(indexes)-1].index+1, objects...)
	previous := make(map[string]*editorObjectOrigin, len(origins))
	for id := range origins {
		previous[id] = e.objectOrigin(id)
	}
	apply := func(e *Editor, layers []Layer, origins map[string]*editorObjectOrigin) {
		e.pages[page].Content.Layer = copyEditorPage(PageContent{Content: Content{Layer: layers}}).Content.Layer
		for id, origin := range origins {
			e.setObjectOrigin(id, origin)
		}
	}
	apply(e, after, origins)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { apply(e, before, previous) }
		change.redo = func(e *Editor) { apply(e, after, origins) }
	}
}
