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
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tdewolff/canvas"
)

// CompositeSelection 同一内部容器的独立选区快照，共享不可变字体和图片资源
// 可在当前Editor内粘贴，源对象修改或删除不改变快照，不用于跨文档交换
// 父路径的祖先成员增删或排序后需重新捕获，避免使用已失效的序号路径
type CompositeSelection struct {
	editor     *Editor
	page       string
	path       ObjectPath
	references int
	containers []int
	nodes      []*editorCompositeNode
	source     *editorSourcePage
}

// PasteCompositeSelection 将内部对象快照粘贴到页面顶层，保持嵌套结构、源继承外观和裁剪
// 入参: page 目标页, selection 当前编辑器内捕获的快照, dx、dy 页面位移
// 返回: []string 新对象标识, error 错误信息
func (e *Editor) PasteCompositeSelection(page int, selection *CompositeSelection, dx, dy float64) ([]string, error) {
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("paste requires finite offsets")
	}
	if _, err := e.page(page); err != nil {
		return nil, err
	}
	var result []string
	err := e.pasteCompositeSelection(selection, func(objects []GraphicObject) error {
		var err error
		result, err = e.CopyObjects(page, objects, dx, dy)
		return err
	})
	return result, err
}

// pasteCompositeSelection 固定内部快照的页面几何和外观，粘贴失败时回收新增绘制资源
// 入参: selection 独立内部快照, paste 目标范围粘贴操作
// 返回: error 错误信息
func (e *Editor) pasteCompositeSelection(selection *CompositeSelection, paste func([]GraphicObject) error) (err error) {
	if selection == nil || selection.editor != e {
		return fmt.Errorf("composite selection belongs to another editor")
	}
	count, maximum, ready := len(e.resources), e.maxID, e.source.idsReady
	defer func() {
		if err != nil {
			clear(e.resources[count:])
			e.resources, e.maxID, e.source.idsReady = e.resources[:count], maximum, ready
		}
	}()
	if err := e.prepareSourceIDs(); err != nil {
		return err
	}
	reader, err := e.Reader()
	if err != nil {
		return err
	}
	defer reader.Close()
	renderer := NewRenderer(reader, WithFontFS(e.fontFS...))
	var objects []GraphicObject
	for _, source := range selection.nodes {
		copy, err := newEditorCompositeNode(source.data)
		if err != nil {
			return err
		}
		copy.setStates(source.states)
		copy.defaults = source.defaults
		object, err := e.compositeMemberStyle(source)
		if err != nil {
			return err
		}
		object = mergeGraphicObjectAlpha(object, source.alpha)
		if !source.visible || source.clip != nil && source.clip.Empty() {
			visible := false
			switch object.Type {
			case "TextObject":
				object.TextObject.Visible = &visible
			case "PathObject":
				object.PathObject.Visible = &visible
			case "ImageObject":
				object.ImageObject.Visible = &visible
			case "CompositeObject", "CompositeGraphicUnit":
				object.CompositeGraphicUnit.Visible = &visible
			}
		}
		if err := copy.update(object); err != nil {
			return err
		}
		if !source.boundaryInCTM {
			if err := copy.convertCoordinates(source.parent, true); err != nil {
				return err
			}
		}
		copy.parent, copy.boundaryInCTM = IdentityMatrix, true
		if err := e.transformCompositeMember(renderer, copy, source.parent); err != nil {
			return err
		}
		if err := e.isolateCompositeStyle(copy); err != nil {
			return err
		}
		if source.clip != nil && !source.clip.Empty() {
			shape := compositeClipPath(source.clip)
			clips := *editorObjectClips(&copy.object)
			matrix, ok := copy.matrix(clips == nil || clips.TransFlag == nil || *clips.TransFlag).Invert()
			if !ok {
				return fmt.Errorf("composite clip transform is not invertible")
			}
			if err := appendCompositeClip(copy, shape, matrix, false); err != nil {
				return err
			}
		}
		if copy.object.Type == "CompositeObject" || copy.object.Type == "CompositeGraphicUnit" {
			if err := copy.convertCoordinates(IdentityMatrix, false); err != nil {
				return err
			}
		}
		copy.node.parent = &editorXML{name: xml.Name{Local: "Layer"}, parent: &editorXML{name: xml.Name{Local: "Content"}}}
		object, err = e.resolveEditorStyle(copy.object, "")
		if err != nil {
			return err
		}
		object.state = copy.states[editorObjectID(copy.object)]
		object.TextObject.layout = object.state.layout
		copy.object.TextObject.layout = object.TextObject.layout
		if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
			object.CompositeGraphicUnit.states = maps.Clone(copy.states)
			copy.object.CompositeGraphicUnit.states = object.CompositeGraphicUnit.states
		}
		object.origin = &editorObjectOrigin{page: selection.source, data: copy.data, node: copy.node, object: copy.object}
		objects = append(objects, object)
	}
	return paste(objects)
}

// convertCoordinates 转换内联基本成员的边界约定，引用资源保持标准局部坐标
// 入参: parent 所在范围矩阵, inward 是否转换到随父变换的坐标
// 返回: error 错误信息
func (n *editorCompositeNode) convertCoordinates(parent Matrix, inward bool) error {
	if n.object.Type != "CompositeObject" && n.object.Type != "CompositeGraphicUnit" {
		return n.update(compositeBoundary(n.object, parent, inward))
	}
	var patches []editorXMLPatch
	var visit func(*editorXML, Matrix) error
	visit = func(node *editorXML, matrix Matrix) error {
		if node.name.Local == "CompositeObject" || node.name.Local == "CompositeGraphicUnit" {
			box, _ := ParseBox(node.attr("Boundary"))
			matrix = matrix.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(NewMatrix(node.attr("CTM")))
			if _, ok := matrix.Invert(); !ok {
				return fmt.Errorf("composite copy requires invertible transforms")
			}
		}
		for _, child := range node.children {
			if child.name.Space != "" && child.name.Space != ofdNamespace && child.name.Space != "http://www.ofdspec.org" {
				continue
			}
			switch child.name.Local {
			case "Content", "PageBlock", "CompositeObject", "CompositeGraphicUnit":
				if err := visit(child, matrix); err != nil {
					return err
				}
			case "TextObject", "PathObject", "ImageObject":
				data, err := editorXMLStandalone(n.data[child.start:child.end], child)
				if err != nil {
					return err
				}
				member, err := newEditorCompositeNode(data)
				if err != nil {
					return err
				}
				if err := member.update(compositeBoundary(cloneEditorData(member.object), matrix, inward)); err != nil {
					return err
				}
				patches = append(patches, editorXMLPatch{child.start, child.end, member.data})
			}
		}
		return nil
	}
	if err := visit(n.node, parent); err != nil {
		return err
	}
	data := editorPatchXML(n.data, patches)
	next, err := newEditorCompositeNode(data)
	if err != nil {
		return err
	}
	n.data, n.node, n.object = data, next.node, next.object
	return nil
}

// compositeClipPath 将已解析的父裁剪转换为标准紧缩路径，使用页面毫米坐标
// 入参: path 画布裁剪
// 返回: PathObject 标准裁剪路径
func compositeClipPath(path *canvas.Path) PathObject {
	return editorClipPath(path.Copy().Transform(canvas.Matrix{{1, 0, 0}, {0, -1, 0}}))
}

// editorClipPath 将页面坐标路径写为标准紧缩裁剪路径
// 入参: path 页面毫米路径
// 返回: PathObject 标准裁剪路径
func editorClipPath(path *canvas.Path) PathObject {
	path = path.ReplaceArcs()
	data := path.Data()
	var value strings.Builder
	for i := 0; i < len(data); {
		switch data[i] {
		case canvas.MoveToCmd, canvas.LineToCmd:
			command := "L"
			if data[i] == canvas.MoveToCmd {
				command = "M"
			}
			fmt.Fprintf(&value, "%s %s %s ", command, ofdNumber(data[i+1]), ofdNumber(data[i+2]))
			i += 4
		case canvas.QuadToCmd:
			fmt.Fprintf(&value, "Q %s %s %s %s ", ofdNumber(data[i+1]), ofdNumber(data[i+2]), ofdNumber(data[i+3]), ofdNumber(data[i+4]))
			i += 6
		case canvas.CubeToCmd:
			fmt.Fprintf(&value, "B %s %s %s %s %s %s ", ofdNumber(data[i+1]), ofdNumber(data[i+2]), ofdNumber(data[i+3]), ofdNumber(data[i+4]), ofdNumber(data[i+5]), ofdNumber(data[i+6]))
			i += 8
		case canvas.CloseCmd:
			value.WriteString("C ")
			i += 4
		}
	}
	fill, stroke := true, false
	return PathObject{Boundary: "0 0 1 1", Fill: &fill, Stroke: &stroke, AbbreviatedData: strings.TrimSpace(value.String())}
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
	selection := &CompositeSelection{editor: e, page: e.pages[page].ID, path: ObjectPath{ID: path.ID, Children: slices.Clone(path.Children)}, source: e.objectOrigin(path.ID).page}
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

// PasteCompositeObjects 将快照粘贴至内部范围末尾，不依赖源成员当前的序号或存在性
// 同范围沿用原容器和当前继承样式，跨范围固定对象的源外观，位移使用页面毫米
// 目标范围的父透明度及裁剪仍作用于新成员，全部成员提交一次撤销记录
// 入参: page 页面索引, path 父复合路径, selection 捕获的快照, dx、dy 页面位移
// 返回: []int 新成员序号, error 错误信息
func (e *Editor) PasteCompositeObjects(page int, path ObjectPath, selection *CompositeSelection, dx, dy float64) ([]int, error) {
	if selection == nil || selection.editor != e {
		return nil, fmt.Errorf("composite selection belongs to another editor")
	}
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("paste requires finite offsets")
	}
	if _, err := e.page(page); err != nil {
		return nil, err
	}
	if e.pages[page].ID != selection.page || path.ID != selection.path.ID || !slices.Equal(path.Children, selection.path.Children) {
		var result []int
		err := e.pasteCompositeSelection(selection, func(objects []GraphicObject) error {
			var err error
			result, err = e.CopyObjectsToComposite(page, path, objects, dx, dy)
			return err
		})
		return result, err
	}
	var result []int
	err := e.editCompositeScope(page, path, func(renderer *Renderer, root *editorCompositeNode, _ []*editorCompositeNode) error {
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
		data, states, err := e.copyCompositeNodes(renderer, nodes, dx, dy)
		if err != nil {
			return err
		}
		maps.Copy(owner.states, states)
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
		if (child.name.Local == "Content" || child.name.Local == "PageBlock") && (child.name.Space == "" || child.name.Space == ofdNamespace || child.name.Space == "http://www.ofdspec.org") {
			result = append(result, child)
		}
	}
	return result
}
