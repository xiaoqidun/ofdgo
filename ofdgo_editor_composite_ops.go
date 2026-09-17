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
	"fmt"
	"maps"
	"slices"
)

// siblings 获取同一原始容器内的直接绘制成员，不跨越资源和页块
// 返回: []*editorCompositeNode 按绘制顺序排列的成员
func (n *editorCompositeNode) siblings() []*editorCompositeNode {
	var result []*editorCompositeNode
	for _, member := range n.owner.children {
		if member.span.parent == n.span.parent {
			result = append(result, member)
		}
	}
	return result
}

// DeleteCompositeObjects 删除内部成员，保留容器、未选对象及其他实例，一次撤销恢复全部
// 入参: page 页面索引, path 父复合路径, indexes 成员序号
// 返回: error 错误信息
func (e *Editor) DeleteCompositeObjects(page int, path ObjectPath, indexes []int) error {
	return e.editCompositeObjects(page, path, indexes, func(_ *Renderer, nodes []*editorCompositeNode, _ []CompositeMember) error {
		for _, node := range nodes {
			node.owner.patches = append(node.owner.patches, editorXMLPatch{node.span.start, node.span.end, nil})
		}
		return nil
	})
}

// CopyCompositeObjects 在原容器末尾复制选区并按页面毫米平移，保留原文并重映射副本内部标识
// 入参: page 页面索引, path 父复合路径, indexes 同一容器的成员序号, dx、dy 页面位移
// 返回: []int 按绘制顺序排列的副本序号, error 错误信息
func (e *Editor) CopyCompositeObjects(page int, path ObjectPath, indexes []int, dx, dy float64) ([]int, error) {
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("copy requires finite offsets")
	}
	var result []int
	err := e.editCompositeObjects(page, path, indexes, func(renderer *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		container := nodes[0].span.parent
		for i, node := range nodes {
			if !members[i].Capabilities.Copy || node.span.parent != container {
				return fmt.Errorf("copy requires editable members in the same container")
			}
		}
		slices.SortFunc(nodes, func(a, b *editorCompositeNode) int { return a.index - b.index })
		data, states, err := e.copyCompositeNodes(renderer, nodes, dx, dy)
		if err != nil {
			return err
		}
		owner := nodes[0].owner
		maps.Copy(owner.states, states)
		owner.patches = append(owner.patches, editorXMLPatch{container.close, container.close, data})
		next := 0
		for _, member := range owner.children {
			if container.start < member.span.start && member.span.end <= container.close {
				next = member.index + 1
			}
		}
		for i := range nodes {
			result = append(result, next+i)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// copyCompositeNodes 复制成员原文并统一重映射标识，保持副本之间的引用
// 入参: renderer 渲染器, nodes 快照, dx、dy 页面位移
// 返回: []byte 副本XML, map[string]editorCompositeState 会话信息, error 错误信息
func (e *Editor) copyCompositeNodes(renderer *Renderer, nodes []*editorCompositeNode, dx, dy float64) ([]byte, map[string]editorCompositeState, error) {
	ids := make(map[string]string)
	for _, node := range nodes {
		if err := e.collectCompositeIDs(node.node, ids); err != nil {
			return nil, nil, err
		}
	}
	var data []byte
	states := make(map[string]editorCompositeState)
	for _, node := range nodes {
		copy := *node
		if err := e.transformCompositeMember(renderer, &copy, TranslationMatrix(dx, dy)); err != nil {
			return nil, nil, err
		}
		fragment, err := editorXMLRemapIDs(copy.data, copy.node, ids)
		if err != nil {
			return nil, nil, err
		}
		maps.Copy(states, remapCompositeStates(node.states, ids))
		data = append(data, fragment...)
	}
	return data, states, nil
}

// OrderCompositeObjects 调整同一直接容器内的绘制顺序，不改变页块和其他实例
// 入参: page 页面索引, path 父复合路径, indexes 成员序号, order 为up、down、top或bottom
// 返回: []int 调整后的选区序号, error 错误信息
func (e *Editor) OrderCompositeObjects(page int, path ObjectPath, indexes []int, order string) ([]int, error) {
	if !slices.Contains([]string{"up", "down", "top", "bottom"}, order) {
		return nil, fmt.Errorf("invalid object order %q", order)
	}
	var result []int
	err := e.editCompositeObjects(page, path, indexes, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		siblings := nodes[0].siblings()
		selected := make([]bool, len(siblings))
		for j, node := range nodes {
			i := slices.Index(siblings, node)
			if i < 0 || !members[j].Capabilities.Order {
				return fmt.Errorf("ordering requires members in the same container")
			}
			selected[i] = true
		}
		ordered := slices.Clone(siblings)
		orderEditorObjects(ordered, selected, order)
		for i, node := range siblings {
			if node != ordered[i] {
				node.owner.patches = append(node.owner.patches, editorXMLPatch{node.span.start, node.span.end, ordered[i].data})
			}
			if slices.Contains(nodes, ordered[i]) {
				result = append(result, node.index)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReplaceCompositeImage 替换内部图片数据，保留变换、裁剪、蒙版、边框和透明度
// 入参: page 页面索引, path 父复合路径, index 图片序号, data PNG或JPEG数据
// 返回: error 错误信息
func (e *Editor) ReplaceCompositeImage(page int, path ObjectPath, index int, data []byte) error {
	return e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, _ []CompositeMember) error {
		node := nodes[0]
		if node.object.Type != "ImageObject" {
			return fmt.Errorf("composite member is not an image")
		}
		object := node.object
		id, err := e.AddImage(data)
		if err != nil {
			return err
		}
		object.ImageObject.ResourceID = id
		return node.update(object)
	})
}

// update 合并成员属性变化，不重写原文中未修改的字段
// 入参: object 修改后的对象
// 返回: error 错误信息
func (n *editorCompositeNode) update(object GraphicObject) error {
	data, err := editorXMLObject(n.data, n.node, n.object, object)
	if err != nil || bytes.Equal(data, n.data) {
		return err
	}
	next, err := newEditorCompositeNode(data)
	if err != nil {
		return err
	}
	n.data, n.node, n.object, n.changed = data, next.node, next.object, true
	return nil
}

// collectCompositeIDs 为已校验副本中的标准标识分配新值，拒绝重复标识
// 入参: node 原节点, ids 原标识与新标识
// 返回: error 错误信息
func (e *Editor) collectCompositeIDs(node *editorXML, ids map[string]string) error {
	if id := editorResourceID(node.attr("ID")); id != "" {
		if ids[id] != "" {
			return fmt.Errorf("duplicate object ID %q", id)
		}
		ids[id] = e.nextID()
	}
	for _, child := range node.children {
		if err := e.collectCompositeIDs(child, ids); err != nil {
			return err
		}
	}
	return nil
}
