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
)

// GroupCompositeObjects 组合内部同一容器的连续成员，保留标识、绘制顺序及共享资源
// 入参: page 页面索引, path 父路径, indexes 至少两个成员序号
// 返回: int 新组合序号, error 错误信息
func (e *Editor) GroupCompositeObjects(page int, path ObjectPath, indexes []int) (int, error) {
	if len(indexes) < 2 {
		return 0, fmt.Errorf("grouping requires at least two objects")
	}
	var result int
	err := e.editCompositeObjects(page, path, indexes, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		for _, member := range members {
			if !member.Capabilities.Copy || !member.Capabilities.Order {
				return fmt.Errorf("composite member cannot be grouped")
			}
		}
		slices.SortFunc(nodes, func(a, b *editorCompositeNode) int { return a.index - b.index })
		siblings := nodes[0].siblings()
		first := slices.Index(siblings, nodes[0])
		var content []byte
		for i, node := range nodes {
			if first+i >= len(siblings) || siblings[first+i] != node {
				return fmt.Errorf("grouping requires consecutive members in the same container")
			}
			content = append(content, bytes.TrimPrefix(node.data, []byte(xml.Header))...)
		}
		content, err := editorXMLContainer("Content", nil, content)
		if err != nil {
			return err
		}
		content, err = editorXMLContainer("CompositeObject", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: e.nextID()}, {Name: xml.Name{Local: "Boundary"}, Value: "0 0 1 1"}}, bytes.TrimPrefix(content, []byte(xml.Header)))
		if err != nil {
			return err
		}
		for i, node := range nodes {
			var replacement []byte
			if i == 0 {
				replacement = bytes.TrimPrefix(content, []byte(xml.Header))
			}
			node.owner.patches = append(node.owner.patches, editorXMLPatch{node.span.start, node.span.end, replacement})
		}
		result = nodes[0].index
		return nil
	})
	return result, err
}

// UngroupCompositeObject 展开内部组合，隔离引用资源并保留父容器的样式和裁剪
// 入参: page 页面索引, path 父路径, index 组合序号
// 返回: []int 展开后的成员序号, error 错误信息
func (e *Editor) UngroupCompositeObject(page int, path ObjectPath, index int) ([]int, error) {
	var result []int
	err := e.editCompositeObjects(page, path, []int{index}, func(renderer *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		node := nodes[0]
		if !members[0].Capabilities.Order {
			return fmt.Errorf("composite container cannot be reordered")
		}
		return e.ungroupCompositeNode(renderer, node, func(objects []GraphicObject) error {
			var content []byte
			for i, object := range objects {
				content = append(content, object.origin.data...)
				maps.Copy(node.owner.states, object.CompositeGraphicUnit.states)
				node.owner.states[editorObjectID(object)] = object.state
				result = append(result, index+i)
			}
			delete(node.owner.states, editorObjectID(node.object))
			node.owner.patches = append(node.owner.patches, editorXMLPatch{node.span.start, node.span.end, content})
			return nil
		})
	})
	return result, err
}

// ungroupCompositeNode 将组合展开至所在坐标系，祖先透明度和裁剪仅由原父级应用一次
// 入参: renderer 当前读取器, node 待展开组合, replace 原子替换回调
// 返回: error 错误信息
func (e *Editor) ungroupCompositeNode(renderer *Renderer, node *editorCompositeNode, replace func([]GraphicObject) error) error {
	if !e.compositeUngroupable(node.node) {
		return fmt.Errorf("group container cannot be removed without losing attributes")
	}
	copy, err := newEditorCompositeNode(node.data)
	if err != nil {
		return err
	}
	copy.setStates(node.states)
	if !node.boundaryInCTM {
		if err := copy.convertCoordinates(node.parent, true); err != nil {
			return err
		}
	}
	copy.parent, copy.boundaryInCTM, copy.visible = IdentityMatrix, true, true
	copy.defaults, copy.drawParams = node.defaults, node.drawParams
	members, err := e.compositeMembers(copy, renderer.Reader, renderer, make(map[string]bool))
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return fmt.Errorf("group has no objects")
	}
	for _, member := range members {
		if !member.transformable() || !editorXMLCopyable(member.node) {
			return fmt.Errorf("group member cannot be expanded")
		}
	}
	return e.pasteCompositeSelection(&CompositeSelection{editor: e, nodes: members}, func(objects []GraphicObject) error {
		ids := make(map[string]string)
		if copy.ref != nil {
			for _, object := range objects {
				if err := e.collectCompositeIDs(object.origin.node, ids); err != nil {
					return err
				}
			}
		}
		for i, object := range objects {
			data := object.origin.data
			if len(ids) != 0 {
				data, err = editorXMLRemapIDs(data, object.origin.node, ids)
				if err != nil {
					return err
				}
			}
			member, err := newEditorCompositeNode(data)
			if err != nil {
				return err
			}
			states := maps.Clone(object.CompositeGraphicUnit.states)
			if states == nil {
				states = make(map[string]editorCompositeState)
			}
			states[editorObjectID(object)] = object.state
			if len(ids) != 0 {
				states = remapCompositeStates(states, ids)
			}
			if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
				if err := member.convertCoordinates(IdentityMatrix, true); err != nil {
					return err
				}
			}
			if !node.boundaryInCTM {
				if err := member.convertCoordinates(node.parent, false); err != nil {
					return err
				}
			}
			member.node.parent = &editorXML{name: xml.Name{Local: "Layer"}, parent: &editorXML{name: xml.Name{Local: "Content"}}}
			member.object.state = states[editorObjectID(member.object)]
			member.object.TextObject.layout = member.object.state.layout
			member.object.CompositeGraphicUnit.states = states
			member.object.origin = &editorObjectOrigin{editor: e, data: member.data, node: member.node, object: member.object}
			objects[i] = member.object
		}
		return replace(objects)
	})
}

// compositeUngroupable 检查待移除的容器及引用链，子对象原文不展开重写
// 入参: node 组合节点
// 返回: bool 是否可解组
func (e *Editor) compositeUngroupable(node *editorXML) bool {
	seen := make(map[string]bool)
	for {
		if !editorUngroupable(node) {
			return false
		}
		id := node.attr("ResourceID")
		if id == "" {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		resource, err := e.compositeDefinition(id)
		if err != nil {
			return false
		}
		node = resource.node
	}
}

// compositeDefinition 读取复合资源原文，不加载图片或字体数据
// 入参: id 资源标识
// 返回: *editorCompositeNode 定义节点, error 错误信息
func (e *Editor) compositeDefinition(id string) (*editorCompositeNode, error) {
	var data []byte
	for _, resource := range e.resources {
		if resource.composite == id {
			data = resource.data
			break
		}
	}
	if data == nil && e.source != nil {
		var err error
		data, err = e.source.reader.readFile(e.source.reader.resourceFiles[id])
		if err != nil {
			return nil, err
		}
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	if units := root.child("CompositeGraphicUnits"); units != nil {
		for _, unit := range units.children {
			if unit.name.Local == "CompositeGraphicUnit" && unit.attr("ID") == id {
				fragment, err := editorXMLStandalone(data[unit.start:unit.end], unit)
				if err != nil {
					return nil, err
				}
				return newEditorCompositeNode(fragment)
			}
		}
	}
	return nil, fmt.Errorf("composite resource %q not found", id)
}
