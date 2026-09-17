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
	"slices"
)

// CompositeSelection 同一内部容器的独立选区快照，共享不可变字体和图片资源
// 仅可粘贴回当前Editor的原范围，源对象修改或删除不改变快照，不用于跨文档交换
// 父路径的祖先成员增删或排序后需重新捕获，避免使用已失效的序号路径
type CompositeSelection struct {
	editor     *Editor
	page       string
	path       ObjectPath
	references int
	containers []int
	nodes      []*editorCompositeNode
}

// CaptureCompositeObjects 捕获内部选区，不修改文档或分配资源
// 入参: page 页面索引, path 父复合路径, indexes 同一容器的成员序号
// 返回: *CompositeSelection 独立快照, error 错误信息
func (e *Editor) CaptureCompositeObjects(page int, path ObjectPath, indexes []int) (*CompositeSelection, error) {
	reader, renderer, root, nodes, err := e.compositeScope(page, path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if len(indexes) == 0 {
		return nil, fmt.Errorf("composite selection is empty")
	}
	indexes = slices.Clone(indexes)
	slices.Sort(indexes)
	members := e.measureCompositeMembers(renderer, nodes)
	var owner *editorCompositeNode
	var container *editorXML
	selection := &CompositeSelection{editor: e, page: e.pages[page].ID, path: ObjectPath{ID: path.ID, Children: slices.Clone(path.Children)}}
	for j, i := range indexes {
		if i < 0 || i >= len(nodes) || j > 0 && indexes[j-1] == i || !members[i].Capabilities.Copy {
			return nil, fmt.Errorf("invalid composite copy selection")
		}
		node := nodes[i]
		if j == 0 {
			owner, container = node.owner, node.span.parent
		} else if node.span.parent != container {
			return nil, fmt.Errorf("copy requires members in the same container")
		}
		copy := *node
		copy.owner, copy.span = nil, nil
		selection.nodes = append(selection.nodes, &copy)
	}
	scope := root.loadedScope(path.Children)
	for scope != owner {
		scope = scope.ref
		selection.references++
	}
	for node := container; node != owner.node; node = node.parent {
		containers := compositeContainers(node.parent)
		selection.containers = append(selection.containers, slices.Index(containers, node))
	}
	slices.Reverse(selection.containers)
	return selection, nil
}

// PasteCompositeObjects 将快照粘贴至原内部容器末尾，不依赖源成员当前的序号或存在性
// 位移使用当前父变换下的页面毫米，父容器和继承样式沿用当前范围，提交一次撤销记录
// 入参: page 页面索引, path 父复合路径, selection 捕获的快照, dx、dy 页面位移
// 返回: []int 新成员序号, error 错误信息
func (e *Editor) PasteCompositeObjects(page int, path ObjectPath, selection *CompositeSelection, dx, dy float64) ([]int, error) {
	if selection == nil || selection.editor != e || path.ID != selection.path.ID || !slices.Equal(path.Children, selection.path.Children) {
		return nil, fmt.Errorf("paste requires the original composite scope")
	}
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("paste requires finite offsets")
	}
	var result []int
	err := e.editCompositeScope(page, path, func(renderer *Renderer, root *editorCompositeNode, _ []*editorCompositeNode) error {
		if e.pages[page].ID != selection.page {
			return fmt.Errorf("paste requires the original page")
		}
		scope := root.loadedScope(path.Children)
		owner := scope
		for i := 0; i < selection.references; i++ {
			owner = owner.ref
			if owner == nil {
				return fmt.Errorf("composite resource container no longer exists")
			}
		}
		container := owner.node
		for _, index := range selection.containers {
			containers := compositeContainers(container)
			if index >= len(containers) {
				return fmt.Errorf("composite page block no longer exists")
			}
			container = containers[index]
		}
		box, _ := ParseBox(owner.object.CompositeGraphicUnit.Boundary)
		parent := owner.parent.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(NewMatrix(owner.object.CompositeGraphicUnit.CTM))
		nodes := make([]*editorCompositeNode, len(selection.nodes))
		for i, node := range selection.nodes {
			copy := *node
			copy.parent, copy.boundaryInCTM = parent, owner.boundaryInCTM
			nodes[i] = &copy
		}
		data, err := e.copyCompositeNodes(renderer, nodes, dx, dy)
		if err != nil {
			return err
		}
		next, reached := 0, false
		var count func(*editorCompositeNode)
		count = func(node *editorCompositeNode) {
			if node.ref != nil {
				count(node.ref)
			}
			if reached {
				return
			}
			for _, child := range node.children {
				if node != owner || child.span.start < container.close {
					next++
				}
			}
			reached = node == owner
		}
		count(scope)
		patch := editorXMLPatch{container.close, container.close, data}
		if container.open == container.end {
			patch = editorXMLContent(owner.data, container, data)
		}
		owner.patches = append(owner.patches, patch)
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

// loadedScope 获取已由compositeScope展开并校验的范围
// 入参: path 逐层成员序号
// 返回: *editorCompositeNode 范围节点
func (n *editorCompositeNode) loadedScope(path []int) *editorCompositeNode {
	for _, index := range path {
		var nodes []*editorCompositeNode
		var collect func(*editorCompositeNode)
		collect = func(node *editorCompositeNode) {
			if node.ref != nil {
				collect(node.ref)
			}
			nodes = append(nodes, node.children...)
		}
		collect(n)
		n = nodes[index]
	}
	return n
}

// compositeContainers 获取直接内容容器，序号不随绘制成员增删变化
// 入参: node 父节点
// 返回: []*editorXML 内容容器与页块
func compositeContainers(node *editorXML) []*editorXML {
	var result []*editorXML
	for _, child := range node.children {
		if child.name.Local == "Content" || child.name.Local == "PageBlock" {
			result = append(result, child)
		}
	}
	return result
}
