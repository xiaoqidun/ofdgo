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
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
)

// ObjectPath 定位页面顶层对象或注解外观内的成员，Children为从0开始的逐层绘制序号
// Annotation非空时定位该注解的Appearance，ID留空
// 路径不依赖资源标识，几何变换、资源隔离及撤销重做不改变路径；成员增删或排序后需重新枚举
type ObjectPath struct {
	ID         string `json:"id"`
	Annotation string `json:"annotation,omitempty"`
	Children   []int  `json:"children,omitempty"`
}

// CompositeMember 复合对象的直接成员快照，Object包含有效样式，不用于整体替换原对象
// Bounds与Contours使用页面毫米坐标，StrokeScale为描边倍率，Matrix将对象局部坐标映射至页面
// Position表示当前范围内的直接容器位置，结构操作后需重新枚举
type CompositeMember struct {
	Object       GraphicObject
	Bounds       Box
	Contours     []ObjectContour
	StrokeScale  float64
	Matrix       Matrix
	Capabilities ObjectCapabilities
	Position     ObjectPosition
	Transform    bool
	Cropped      bool
}

// editorCompositeNode 保留原始XML及实例上下文，不修改共享资源
type editorCompositeNode struct {
	data          []byte
	node          *editorXML
	object        GraphicObject
	parent        Matrix
	boundaryInCTM bool
	defaults      *DrawParam
	drawParams    []string
	clip          *GeometryPath
	visible       bool
	alpha         *int
	ref           *editorCompositeNode
	children      []*editorCompositeNode
	expanded      bool
	changed       bool
	span          *editorXML
	owner         *editorCompositeNode
	patches       []editorXMLPatch
	index         int
	states        map[string]editorCompositeState
}

// editorCompositeState 保存内部对象的会话排版及可还原裁剪，不写入OFD
type editorCompositeState struct {
	layout *textLayout
	crop   *editorCompositeCrop
	origin [2]float64
}

// editorCompositeCrop 记录本次编辑新增的裁剪序号及原节点存在性
type editorCompositeCrop struct {
	indexes []int
	exists  bool
}

// setStates 独立保存当前XML子树的会话信息，共享不可变记录
// 入参: states 来源记录
func (n *editorCompositeNode) setStates(states map[string]editorCompositeState) {
	n.states = make(map[string]editorCompositeState)
	var visit func(*editorXML)
	visit = func(node *editorXML) {
		if state, ok := states[node.attr("ID")]; ok {
			n.states[node.attr("ID")] = state
		}
		for _, child := range node.children {
			visit(child)
		}
	}
	visit(n.node)
	n.object.TextObject.layout = n.states[editorObjectID(n.object)].layout
}

// setState 替换当前对象的会话记录并计入本次事务
// 入参: state 新记录
func (n *editorCompositeNode) setState(state editorCompositeState) {
	id := editorObjectID(n.object)
	if reflect.DeepEqual(n.states[id], state) {
		return
	}
	if n.states == nil {
		n.states = make(map[string]editorCompositeState)
	}
	if state == (editorCompositeState{}) {
		delete(n.states, id)
	} else {
		n.states[id] = state
	}
	n.object.TextObject.layout = state.layout
	n.changed = true
}

// remapCompositeStates 随XML标识重映射会话记录，仅保留当前子树的对象
// 入参: states 原记录, ids 标识映射
// 返回: map[string]editorCompositeState 新记录
func remapCompositeStates(states map[string]editorCompositeState, ids map[string]string) map[string]editorCompositeState {
	var result map[string]editorCompositeState
	for id, next := range ids {
		if state, ok := states[id]; ok {
			if result == nil {
				result = make(map[string]editorCompositeState)
			}
			result[next] = state
		}
	}
	return result
}

// removeCompositeStates 清除子树旧记录，再由写入结果合并新记录
// 入参: states 目标记录, node 子树根节点
func removeCompositeStates(states map[string]editorCompositeState, node *editorXML) {
	delete(states, node.attr("ID"))
	for _, child := range node.children {
		removeCompositeStates(states, child)
	}
}

// CompositeObjects 枚举复合对象的直接可绘制成员，资源引用层不占用路径层级
// 入参: page 页面索引, path 复合对象路径
// 返回: []CompositeMember 按绘制顺序排列的成员, error 错误信息
func (e *Editor) CompositeObjects(page int, path ObjectPath) ([]CompositeMember, error) {
	reader, renderer, _, members, err := e.compositeScope(page, path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return e.measureCompositeMembers(renderer, members)
}

// CompositeObject 读取指定内部成员，仅度量该成员并保留完整容器位置
// 入参: page 页面索引, path 父复合路径, index 成员序号
// 返回: CompositeMember 独立成员快照, error 错误信息
func (e *Editor) CompositeObject(page int, path ObjectPath, index int) (CompositeMember, error) {
	members, err := e.CompositeObjectsAt(page, path, []int{index})
	if err != nil {
		return CompositeMember{}, err
	}
	return members[0], nil
}

// CompositeObjectsAt 按输入顺序读取指定成员，一次解析范围且仅度量选区
// 入参: page 页面索引, path 父复合路径, indexes 不重复的成员序号
// 返回: []CompositeMember 独立成员快照, error 错误信息
func (e *Editor) CompositeObjectsAt(page int, path ObjectPath, indexes []int) ([]CompositeMember, error) {
	reader, renderer, _, nodes, err := e.compositeScope(page, path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	selected := make([]*editorCompositeNode, len(indexes))
	seen := make(map[int]bool, len(indexes))
	for i, index := range indexes {
		if index < 0 || index >= len(nodes) || seen[index] {
			return nil, fmt.Errorf("invalid composite member index %d", index)
		}
		seen[index] = true
		selected[i] = nodes[index]
	}
	positions := make([]ObjectPosition, len(nodes))
	containers := make(map[*editorXML]ObjectPosition)
	for i, node := range nodes {
		position := containers[node.span.parent]
		if position.Count == 0 {
			position.Container = fmt.Sprint(i)
		}
		positions[i] = ObjectPosition{Container: position.Container, Index: position.Count}
		position.Count++
		containers[node.span.parent] = position
	}
	members, err := e.measureCompositeMembers(renderer, selected)
	if err != nil {
		return nil, err
	}
	for i, index := range indexes {
		members[i].Position = positions[index]
		members[i].Position.Count = containers[nodes[index].span.parent].Count
	}
	return members, nil
}

// TransformCompositeObjects 在页面坐标中等比缩放并平移内部选区，只隔离被修改实例的资源
// 入参: page 页面索引, path 父复合对象路径, indexes 成员序号, dx、dy 位移, scale 正缩放比例
// 返回: error 错误信息
func (e *Editor) TransformCompositeObjects(page int, path ObjectPath, indexes []int, dx, dy, scale float64) error {
	if !finite(dx) || !finite(dy) || !finite(scale) || scale <= 0 {
		return fmt.Errorf("invalid composite transform")
	}
	return e.changeCompositeObjects(page, path, indexes, func(Box) Matrix { return Matrix{a: scale, d: scale, e: dx, f: dy} })
}

// RotateCompositeObjects 绕内部选区的可见中心旋转，提交一次撤销记录
// 入参: page 页面索引, path 父复合对象路径, indexes 成员序号, degrees 顺时针角度，为90度的整数倍
// 返回: error 错误信息
func (e *Editor) RotateCompositeObjects(page int, path ObjectPath, indexes []int, degrees int) error {
	if degrees%90 != 0 {
		return fmt.Errorf("rotation must be a multiple of 90 degrees")
	}
	m := []Matrix{IdentityMatrix, {b: 1, c: -1}, {a: -1, d: -1}, {b: -1, c: 1}}[(degrees%360+360)%360/90]
	return e.changeCompositeObjects(page, path, indexes, func(box Box) Matrix { return compositeOrientation(box, m) })
}

// FlipCompositeObjects 绕内部选区的可见中心镜像
// 入参: page 页面索引, path 父复合对象路径, indexes 成员序号, axis 为horizontal或vertical
// 返回: error 错误信息
func (e *Editor) FlipCompositeObjects(page int, path ObjectPath, indexes []int, axis string) error {
	m := IdentityMatrix
	switch axis {
	case "horizontal":
		m.a = -1
	case "vertical":
		m.d = -1
	default:
		return fmt.Errorf("invalid flip axis %q", axis)
	}
	return e.changeCompositeObjects(page, path, indexes, func(box Box) Matrix { return compositeOrientation(box, m) })
}

// ResizeCompositeObjects 按内部选区范围计算变换，图片及纯图片资源支持独立宽高，描边遵循OFD变换规则
// 入参: page 页面索引, path 父复合对象路径, indexes 成员序号, box 页面毫米目标范围
// 返回: error 错误信息
func (e *Editor) ResizeCompositeObjects(page int, path ObjectPath, indexes []int, box Box) error {
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return err
	}
	return e.editCompositeObjects(page, path, indexes, func(renderer *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		var before Box
		for _, member := range members {
			if !member.Capabilities.Transform {
				return member.Capabilities.editError()
			}
			before = unionTextBox(before, member.Bounds)
		}
		if before.W <= 0 || before.H <= 0 {
			return fmt.Errorf("selection has no visible bounds")
		}
		sx, sy := box.W/before.W, box.H/before.H
		uniform := math.Abs(sx-sy) <= 1e-9*math.Max(sx, sy)
		if !uniform {
			for _, node := range nodes {
				if !e.objectStretchable(node.object, make(map[string]bool)) {
					return fmt.Errorf("nonuniform resizing requires images or image-only groups")
				}
			}
		} else {
			sy = sx
		}
		matrix := Matrix{a: sx, d: sy, e: box.X - before.X*sx, f: box.Y - before.Y*sy}
		for _, node := range nodes {
			if err := e.transformCompositeMember(renderer, node, matrix); err != nil {
				return err
			}
		}
		return nil
	})
}

// compositeOrientation 将旋转或镜像中心移至选区中心
// 入参: box 选区, matrix 方向矩阵
// 返回: Matrix 页面变换
func compositeOrientation(box Box, matrix Matrix) Matrix {
	x, y := box.X+box.W/2, box.Y+box.H/2
	return TranslationMatrix(x, y).Multiply(matrix).Multiply(TranslationMatrix(-x, -y))
}

// compositeScope 加载所需的复合路径及直接成员，不展开无关子树
// 入参: page 页面索引, path 复合对象路径
// 返回: *Reader 预览读取器, *Renderer 度量器, *editorCompositeNode 顶层节点, []*editorCompositeNode 成员, error 错误信息
func (e *Editor) compositeScope(page int, path ObjectPath) (*Reader, *Renderer, *editorCompositeNode, []*editorCompositeNode, error) {
	if path.Annotation != "" {
		return e.annotationScope(page, path)
	}
	layer, index, err := e.findObject(page, path.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	object := layer.Objects[index]
	origin := e.objectOrigin(path.ID)
	if origin == nil {
		return nil, nil, nil, nil, fmt.Errorf("composite requires original content")
	}
	data, err := editorXMLObject(origin.data, origin.node, origin.object, object)
	if err == nil {
		data, err = editorXMLStandalone(data, origin.node)
	}
	if err != nil {
		return nil, nil, nil, nil, err
	}
	root, err := newEditorCompositeNode(data)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	root.setStates(object.CompositeGraphicUnit.states)
	reader, err := e.Reader()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if _, err = reader.PageContentByIndex(page); err != nil {
		reader.Close()
		return nil, nil, nil, nil, err
	}
	renderer := e.newRenderer(reader)
	root.parent, root.visible, root.defaults = IdentityMatrix, true, renderer.drawParamDefaults(layer.DrawParam, nil)
	root.drawParams = []string{layer.DrawParam}
	scope := root
	for depth := 0; ; depth++ {
		var members []*editorCompositeNode
		members, err = e.compositeMembers(scope, reader, renderer, make(map[string]bool))
		if err != nil {
			break
		}
		if depth == len(path.Children) {
			for i, member := range members {
				member.index = i
			}
			return reader, renderer, root, members, nil
		}
		i := path.Children[depth]
		if i < 0 || i >= len(members) {
			err = fmt.Errorf("composite member index %d out of range", i)
			break
		}
		scope = members[i]
	}
	reader.Close()
	return nil, nil, nil, nil, err
}

// newEditorCompositeNode 解析独立图形片段，保留全部原始属性
// 入参: data XML片段
// 返回: *editorCompositeNode 节点, error 错误信息
func newEditorCompositeNode(data []byte) (*editorCompositeNode, error) {
	node, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var object GraphicObject
	object.Type = node.name.Local
	switch object.Type {
	case "Appearance":
		object.Type = "CompositeObject"
		err = xml.Unmarshal(data, &object.CompositeGraphicUnit)
	case "TextObject":
		err = xml.Unmarshal(data, &object.TextObject)
	case "PathObject":
		err = xml.Unmarshal(data, &object.PathObject)
	case "ImageObject":
		err = xml.Unmarshal(data, &object.ImageObject)
	case "CompositeObject", "CompositeGraphicUnit":
		err = xml.Unmarshal(data, &object.CompositeGraphicUnit)
	default:
		err = fmt.Errorf("unsupported composite member %q", object.Type)
	}
	return &editorCompositeNode{data: data, node: node, object: object}, err
}

// compositeMembers 展开当前复合实例的资源引用与直接绘制成员
// 入参: n 当前节点, reader 资源读取器, renderer 渲染上下文, visiting 当前资源链
// 返回: []*editorCompositeNode 成员, error 错误信息
func (e *Editor) compositeMembers(n *editorCompositeNode, reader *Reader, renderer *Renderer, visiting map[string]bool) ([]*editorCompositeNode, error) {
	if n.object.Type != "CompositeObject" && n.object.Type != "CompositeGraphicUnit" {
		return nil, fmt.Errorf("object is not composite")
	}
	if !n.expanded {
		c := n.object.CompositeGraphicUnit
		box, _ := ParseBox(c.Boundary)
		boundary := n.parent.Multiply(TranslationMatrix(box.X, box.Y))
		matrix := boundary.Multiply(NewMatrix(c.CTM))
		clips, clipMatrix := c.Clips, matrix
		if clips != nil && clips.TransFlag != nil && !*clips.TransFlag {
			copy := *clips
			copy.TransFlag = nil
			clips, clipMatrix = &copy, boundary
		}
		clip := n.clip
		if clips != nil {
			geometry, err := renderer.Geometry()
			if err != nil {
				return nil, err
			}
			clip, err = geometry.Clip(renderer, clips, clipMatrix, n.clip)
			if err != nil {
				return nil, err
			}
		}
		defaults := renderer.drawParamDefaults(c.DrawParam, n.defaults)
		drawParams := append(slices.Clone(n.drawParams), c.DrawParam)
		visible := n.visible && (c.Visible == nil || *c.Visible)
		alpha := mergeAlpha(n.alpha, c.Alpha)
		if c.ResourceID != "" {
			data, err := reader.readFile(reader.resourceFiles[c.ResourceID])
			if err != nil {
				return nil, err
			}
			root, err := parseEditorXML(data)
			if err != nil {
				return nil, err
			}
			var resource *editorXML
			if units := root.child("CompositeGraphicUnits"); units != nil {
				for _, child := range units.children {
					if child.name.Local == "CompositeGraphicUnit" && child.attr("ID") == c.ResourceID {
						resource = child
						break
					}
				}
			}
			if resource == nil {
				return nil, fmt.Errorf("composite resource %q not found", c.ResourceID)
			}
			fragment, err := editorXMLStandalone(data[resource.start:resource.end], resource)
			if err != nil {
				return nil, err
			}
			n.ref, err = newEditorCompositeNode(fragment)
			if err != nil {
				return nil, err
			}
			var states map[string]editorCompositeState
			for _, resource := range e.resources {
				if resource.composite == c.ResourceID {
					states = resource.states
					break
				}
			}
			n.ref.setStates(states)
			n.ref.parent, n.ref.boundaryInCTM, n.ref.defaults, n.ref.clip, n.ref.visible = matrix, true, defaults, clip, visible
			n.ref.drawParams = drawParams
			n.ref.alpha = alpha
		}
		var collect func(*editorXML) error
		collect = func(container *editorXML) error {
			for _, child := range container.children {
				if child.name.Space != "" && child.name.Space != ofdNamespace && child.name.Space != "http://www.ofdspec.org" {
					continue
				}
				switch child.name.Local {
				case "Content", "PageBlock":
					if err := collect(child); err != nil {
						return err
					}
				case "TextObject", "PathObject", "ImageObject", "CompositeObject", "CompositeGraphicUnit":
					data, err := editorXMLStandalone(n.data[child.start:child.end], child)
					if err != nil {
						return err
					}
					item, err := newEditorCompositeNode(data)
					if err != nil {
						return err
					}
					item.setStates(n.states)
					item.parent, item.boundaryInCTM, item.defaults, item.clip, item.visible, item.span = matrix, n.boundaryInCTM, defaults, clip, visible, child
					item.drawParams = drawParams
					item.alpha = alpha
					item.owner = n
					n.children = append(n.children, item)
				}
			}
			return nil
		}
		if err := collect(n.node); err != nil {
			return nil, err
		}
		n.expanded = true
	}
	var result []*editorCompositeNode
	if n.ref != nil {
		id := n.object.CompositeGraphicUnit.ResourceID
		if visiting[id] {
			return nil, fmt.Errorf("cyclic composite resource %q", id)
		}
		visiting[id] = true
		members, err := e.compositeMembers(n.ref, reader, renderer, visiting)
		delete(visiting, id)
		if err != nil {
			return nil, err
		}
		result = append(result, members...)
	}
	return append(result, n.children...), nil
}

// measureCompositeMembers 按父变换、裁剪和继承样式度量成员，透明对象保留选区，未着色路径按轮廓度量
// 入参: renderer 渲染器, nodes 成员
// 返回: []CompositeMember 独立快照及操作能力, error 度量错误
func (e *Editor) measureCompositeMembers(renderer *Renderer, nodes []*editorCompositeNode) ([]CompositeMember, error) {
	result := make([]CompositeMember, len(nodes))
	positions := make(map[*editorXML]ObjectPosition)
	orderable := make(map[*editorXML]bool)
	for _, node := range nodes {
		parent := node.span.parent
		position := positions[parent]
		if position.Count == 0 {
			position.Container = fmt.Sprint(node.index)
			orderable[parent] = editorContainerOrderable(parent)
		}
		position.Count++
		positions[parent] = position
	}
	for i, node := range nodes {
		var bounds ObjectMeasurement
		object := node.object
		object.TextObject.Alpha, object.PathObject.Alpha = nil, nil
		object.ImageObject.Alpha, object.CompositeGraphicUnit.Alpha = nil, nil
		if object.Type == "PathObject" && object.PathObject.Stroke != nil && !*object.PathObject.Stroke && (object.PathObject.Fill == nil || !*object.PathObject.Fill) {
			object.PathObject.Stroke = nil
		}
		if node.visible {
			var err error
			bounds, err = renderer.MeasureObject(object, MeasureOptions{Defaults: node.defaults, Parent: &node.parent, BoundaryInCTM: node.boundaryInCTM, Clip: node.clip, Contours: true})
			if err != nil {
				return nil, err
			}
		}
		transform := node.transformable()
		capability := ObjectCapabilities{Transform: transform, Arrange: transform && bounds.Bounds.W > 0 && bounds.Bounds.H > 0,
			Delete: true, Copy: transform && editorXMLCopyable(node.node), Order: orderable[node.span.parent], ReplaceImage: node.object.Type == "ImageObject"}
		capability.Ungroup = transform && capability.Order && e.compositeUngroupable(node.node) && (node.object.CompositeGraphicUnit.ResourceID != "" || len(node.object.CompositeGraphicUnit.Objects) != 0)
		capability.Stretch = transform && e.objectStretchable(node.object, make(map[string]bool))
		style, err := e.compositeMemberStyle(node)
		if !transform {
			capability.ReasonCode, capability.Reason = EditUnsupportedContainer, "composite member cannot be transformed"
		} else if err != nil {
			capability.ReasonCode, capability.Reason = editReason(err), err.Error()
		} else {
			capability.Paint = e.editorPaintable(style)
		}
		if err != nil {
			style = cloneEditorData(node.object)
		}
		if transform && err == nil && node.object.Type == "TextObject" && editorXMLSupported(node.node) {
			capability.ReplaceFont = true
			text := cloneEditorData(style.TextObject)
			if err := e.prepareText(&text); err != nil {
				capability.ReasonCode, capability.Reason = editReason(err), err.Error()
				errors.As(err, &capability.MissingGlyphs)
			} else {
				capability.TextContent = text.ReadDirection == 0 && text.CharDirection == 0
				if _, err := text.TextFrame(); err == nil {
					capability.Reflow = capability.TextContent
					capability.LayoutKnown = text.layout != nil
				}
			}
		}
		if transform && node.object.Type == "ImageObject" && bounds.Bounds.W > 0 && bounds.Bounds.H > 0 {
			clips := node.object.ImageObject.Clips
			_, capability.CropImage = node.matrix(clips == nil || clips.TransFlag == nil || *clips.TransFlag).Invert()
			capability.FitImage = capability.CropImage && axisAlignedMatrix(NewMatrix(node.object.ImageObject.CTM))
			capability.ResetCrop = node.states[editorObjectID(node.object)].crop != nil
		}
		_, ctm := editorGeometry(node.object)
		matrix := node.parent.Multiply(NewMatrix(ctm))
		position := positions[node.span.parent]
		result[i] = CompositeMember{Object: style, Bounds: bounds.Bounds, Contours: bounds.Contours, Matrix: node.matrix(true), StrokeScale: math.Sqrt(math.Abs(matrix.a*matrix.d - matrix.b*matrix.c)), Capabilities: capability, Position: position, Transform: transform, Cropped: node.states[editorObjectID(node.object)].crop != nil}
		position.Index++
		positions[node.span.parent] = position
	}
	return result, nil
}

// transformable 校验内部对象能否保留原文进行几何操作，不执行绘制测量
// 返回: bool 是否支持
func (n *editorCompositeNode) transformable() bool {
	_, invertible := n.parent.Invert()
	borderActions := n.object.Type == "ImageObject" && n.object.ImageObject.Border != nil && len(n.object.ImageObject.Actions) != 0
	return invertible && editorXMLTransformable(n.node) && validateEditorGeometry(n.object) == nil && !borderActions
}

// compositeMemberStyle 解析成员的继承样式，缺失或循环参数不开放改色
// 入参: node 成员节点
// 返回: GraphicObject 有效外观, error 错误信息
func (e *Editor) compositeMemberStyle(node *editorCompositeNode) (GraphicObject, error) {
	for _, id := range node.drawParams {
		if _, err := e.editorDrawParam(id, make(map[string]bool)); err != nil {
			return GraphicObject{}, err
		}
	}
	base := node.defaults
	if base == nil {
		base = &DrawParam{}
	}
	object, err := e.resolveEditorStyleDefaults(node.object, base)
	if err == nil && object.Type == "PathObject" && object.PathObject.LineWidth == 0 && !object.PathObject.LineWidthSet {
		object.PathObject.LineWidth = defaultPathLineWidth
	}
	return object, err
}

// StyleCompositeObjects 原子修改成员透明度、文字颜色及路径填充、描边和线宽
// 不重排文字，不改写继承参数；显式实线覆盖原虚线继承
// 入参: page 页面索引, path 父路径, indexes 成员序号, style 待修改属性，线宽使用页面毫米
// 返回: error 错误信息
func (e *Editor) StyleCompositeObjects(page int, path ObjectPath, indexes []int, style ObjectStyle) error {
	return e.editCompositeObjects(page, path, indexes, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		paint := style.Fill != nil || style.Stroke != nil || style.FillColor != nil || style.StrokeColor != nil || style.LineWidth != nil || style.DashPattern != nil || style.DashOffset != nil || style.Cap != nil || style.Join != nil
		for i, node := range nodes {
			capability := members[i].Capabilities
			if !capability.Transform || paint && !capability.Paint {
				return fmt.Errorf("composite member %d cannot accept this style: %w", indexes[i], capability.editError())
			}
			local := style
			switch members[i].Object.Type {
			case "TextObject":
				local.FillColor = editorStyleColor(style.FillColor, members[i].Object.TextObject.FillColor)
			case "PathObject":
				p := members[i].Object.PathObject
				local.FillColor = editorStyleColor(style.FillColor, p.FillColor)
				local.StrokeColor = (*StrokeColor)(editorStyleColor((*FillColor)(style.StrokeColor), (*FillColor)(p.StrokeColor)))
				if style.LineWidth != nil {
					width := *style.LineWidth / members[i].StrokeScale
					local.LineWidth = &width
				}
			}
			object, err := e.styleObject(node.object, local)
			if err != nil {
				return err
			}
			if err := node.update(object); err != nil {
				return err
			}
		}
		return nil
	})
}

// AlignCompositeObjects 将内部多选成员对齐至选区边界，单选时对齐页面
// 入参: page 页面索引, path 父路径, indexes 成员序号, alignment 为left、center、right、top、middle或bottom
// 返回: error 错误信息
func (e *Editor) AlignCompositeObjects(page int, path ObjectPath, indexes []int, alignment string) error {
	if !slices.Contains([]string{"left", "center", "right", "top", "middle", "bottom"}, alignment) {
		return fmt.Errorf("invalid alignment %q", alignment)
	}
	return e.arrangeCompositeObjects(page, path, indexes, func(boxes []Box) ([]Matrix, error) {
		target, _ := ParseBox(e.pages[page].Area.PhysicalBox)
		if len(boxes) > 1 {
			target = Box{}
			for _, box := range boxes {
				target = unionTextBox(target, box)
			}
		}
		matrices := make([]Matrix, len(boxes))
		for i, box := range boxes {
			matrices[i] = editorAlignment(box, target, alignment)
		}
		return matrices, nil
	})
}

// DistributeCompositeObjects 按可见范围等距分布内部成员，固定两端并保留绘制顺序
// 入参: page 页面索引, path 父路径, indexes 成员序号, axis 为horizontal或vertical
// 返回: error 错误信息
func (e *Editor) DistributeCompositeObjects(page int, path ObjectPath, indexes []int, axis string) error {
	if axis != "horizontal" && axis != "vertical" {
		return fmt.Errorf("invalid distribution axis %q", axis)
	}
	return e.arrangeCompositeObjects(page, path, indexes, func(boxes []Box) ([]Matrix, error) {
		positions := make([]editorObjectPosition, len(indexes))
		for i, index := range indexes {
			positions[i].index = index
		}
		return editorDistribution(boxes, positions, axis)
	})
}

// arrangeCompositeObjects 按成员实际轮廓计算独立页面变换并提交一次历史记录
// 入参: page 页面索引, path 父路径, indexes 成员序号, arrange 排列计算
// 返回: error 错误信息
func (e *Editor) arrangeCompositeObjects(page int, path ObjectPath, indexes []int, arrange func([]Box) ([]Matrix, error)) error {
	return e.editCompositeObjects(page, path, indexes, func(renderer *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		boxes := make([]Box, len(members))
		for i, member := range members {
			if !member.Capabilities.Arrange {
				return fmt.Errorf("composite member has no editable bounds")
			}
			boxes[i] = member.Bounds
		}
		matrices, err := arrange(boxes)
		if err != nil {
			return err
		}
		for i, matrix := range matrices {
			if err := e.transformCompositeMember(renderer, nodes[i], matrix); err != nil {
				return err
			}
		}
		return nil
	})
}

// changeCompositeObjects 原子变换内部选区，沿改动路径复制共享资源
// 入参: page 页面索引, path 父路径, indexes 成员序号, transform 页面变换生成器
// 返回: error 错误信息
func (e *Editor) changeCompositeObjects(page int, path ObjectPath, indexes []int, transform func(Box) Matrix) error {
	return e.editCompositeObjects(page, path, indexes, func(renderer *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		var box Box
		for i := range nodes {
			if !members[i].Capabilities.Transform {
				return members[i].Capabilities.editError()
			}
			box = unionTextBox(box, members[i].Bounds)
		}
		matrix := transform(box)
		for _, node := range nodes {
			if err := e.transformCompositeMember(renderer, node, matrix); err != nil {
				return err
			}
		}
		return nil
	})
}

// editCompositeObjects 校验选区并原子提交成员改动，失败时回收本次分配的资源
// 入参: page 页面索引, path 父路径, indexes 成员序号, edit 修改回调
// 返回: error 错误信息
func (e *Editor) editCompositeObjects(page int, path ObjectPath, indexes []int, edit func(*Renderer, []*editorCompositeNode, []CompositeMember) error) (err error) {
	return e.editCompositeScope(page, path, func(renderer *Renderer, _ *editorCompositeNode, nodes []*editorCompositeNode) error {
		selected := make(map[int]bool)
		var selectedNodes []*editorCompositeNode
		for _, i := range indexes {
			if i < 0 || i >= len(nodes) || selected[i] {
				return fmt.Errorf("invalid composite selection")
			}
			selected[i] = true
			selectedNodes = append(selectedNodes, nodes[i])
		}
		if len(selected) == 0 {
			return nil
		}
		members, err := e.measureCompositeMembers(renderer, selectedNodes)
		if err != nil {
			return err
		}
		return edit(renderer, selectedNodes, members)
	})
}

// editCompositeScope 原子提交内部范围修改，支持向空容器粘贴并在失败时回收新增资源
// 入参: page 页面索引, path 父路径, edit 修改回调
// 返回: error 错误信息
func (e *Editor) editCompositeScope(page int, path ObjectPath, edit func(*Renderer, *editorCompositeNode, []*editorCompositeNode) error) (err error) {
	reader, renderer, root, nodes, err := e.compositeScope(page, path)
	if err != nil {
		return err
	}
	defer reader.Close()
	count, maximum := len(e.resources), e.maxID
	ready := e.source != nil && e.source.idsReady
	defer func() {
		if err != nil {
			removed := make(map[string]bool)
			for _, resource := range e.resources[count:] {
				if resource.image != nil {
					removed[resource.image.ID] = true
					delete(e.images, resource.image.ID)
				}
			}
			maps.DeleteFunc(e.resourceID, func(_ editorResourceKey, id string) bool { return removed[id] })
			clear(e.resources[count:])
			e.resources, e.maxID = e.resources[:count], maximum
			if e.source != nil {
				e.source.idsReady = ready
			}
		}
	}()
	if err = e.prepareSourceIDs(); err != nil {
		return err
	}
	if err = edit(renderer, root, nodes); err != nil {
		return err
	}
	data, changed, err := e.writeCompositeNode(root)
	if err != nil || !changed {
		return err
	}
	if path.Annotation != "" {
		return e.writeAnnotationScope(page, path.Annotation, data, root.states)
	}
	next, err := newEditorCompositeNode(data)
	if err != nil {
		return err
	}
	next.setStates(root.states)
	next.object.CompositeGraphicUnit.states = next.states
	origin := e.objectOrigin(path.ID)
	next.node.parent = origin.node.parent
	updated := &editorObjectOrigin{page: origin.page, data: data, node: next.node, object: next.object}
	return e.updateObjectOrigins(page, []GraphicObject{next.object}, true, map[string]*editorObjectOrigin{path.ID: updated})
}

// transformCompositeMember 将页面变换换算至成员坐标，保留边框及旧式边界约定
// 入参: renderer 渲染器, n 成员, matrix 页面变换
// 返回: error 错误信息
func (e *Editor) transformCompositeMember(renderer *Renderer, n *editorCompositeNode, matrix Matrix) (err error) {
	if matrix == IdentityMatrix {
		return nil
	}
	object := cloneEditorData(n.object)
	inverse, _ := n.parent.Invert()
	basic := object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit"
	if basic && !n.boundaryInCTM {
		object = compositeBoundary(object, n.parent, true)
	}
	local := inverse.Multiply(matrix).Multiply(n.parent)
	if object.Type == "ImageObject" && object.ImageObject.Border != nil {
		object.state = n.states[editorObjectID(object)]
		origin := &editorObjectOrigin{data: n.data, node: n.node, object: n.object}
		object, err = e.transformBorderedImage(object, local, origin, renderer)
		if err != nil {
			return err
		}
	} else {
		object = transformEditorMatrix(object, local)
	}
	if basic && object.Type == n.object.Type && !n.boundaryInCTM {
		object = compositeBoundary(object, n.parent, false)
	}
	if err = validateEditorGeometry(object); err != nil {
		return err
	}
	n.data, err = editorXMLObject(n.data, n.node, n.object, object)
	if err != nil {
		return err
	}
	if !basic && !n.boundaryInCTM {
		n.data, err = transformCompositeOffsets(n.data, matrix)
		if err != nil {
			return err
		}
	}
	updated, err := newEditorCompositeNode(n.data)
	if err != nil {
		return err
	}
	n.node, n.object, n.changed = updated.node, updated.object, true
	return nil
}

// transformCompositeOffsets 变换旧式内联复合成员的页面偏移，资源引用保持原坐标约定
// 入参: data 修改后的复合片段, matrix 页面变换
// 返回: []byte 同步内部边界及非随动裁剪后的片段, error 错误信息
func transformCompositeOffsets(data []byte, matrix Matrix) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var patches []editorXMLPatch
	var visit func(*editorXML) error
	visit = func(node *editorXML) error {
		for _, child := range node.children {
			switch child.name.Local {
			case "Content", "PageBlock", "CompositeObject", "CompositeGraphicUnit":
				if err := visit(child); err != nil {
					return err
				}
			case "TextObject", "PathObject", "ImageObject":
				fragment, err := editorXMLStandalone(data[child.start:child.end], child)
				if err != nil {
					return err
				}
				n, err := newEditorCompositeNode(fragment)
				if err != nil {
					return err
				}
				object := cloneEditorData(n.object)
				object = compositeBoundary(object, Matrix{a: matrix.a, b: matrix.b, c: matrix.c, d: matrix.d}, false)
				if clips := editorObjectClips(&object); *clips != nil && (*clips).TransFlag != nil && !*(*clips).TransFlag {
					*clips = transformObjectClips(*clips, TranslationMatrix(matrix.e, matrix.f))
				}
				fragment, err = editorXMLObject(n.data, n.node, n.object, object)
				if err != nil {
					return err
				}
				patches = append(patches, editorXMLPatch{child.start, child.end, fragment})
			}
		}
		return nil
	}
	if err = visit(root); err != nil {
		return nil, err
	}
	return editorPatchXML(data, patches), nil
}

// compositeBoundary 在边界参与和不参与父变换的坐标约定之间转换
// 入参: object 基本对象, parent 父矩阵, inward 是否转换到父坐标
// 返回: GraphicObject 转换后的对象
func compositeBoundary(object GraphicObject, parent Matrix, inward bool) GraphicObject {
	matrix := parent
	matrix.e, matrix.f = 0, 0
	clipMatrix := parent
	if inward {
		matrix, _ = matrix.Invert()
		clipMatrix, _ = parent.Invert()
	}
	boundary, _ := editorGeometry(object)
	box, _ := ParseBox(boundary)
	box.X, box.Y = matrix.a*box.X+matrix.c*box.Y, matrix.b*box.X+matrix.d*box.Y
	switch object.Type {
	case "TextObject":
		object.TextObject.Boundary = editorBoxString(box)
	case "PathObject":
		object.PathObject.Boundary = editorBoxString(box)
	case "ImageObject":
		object.ImageObject.Boundary = editorBoxString(box)
	}
	if clips := editorObjectClips(&object); *clips != nil && (*clips).TransFlag != nil && !*(*clips).TransFlag {
		*clips = transformObjectClips(*clips, clipMatrix)
	}
	return object
}

// writeCompositeNode 保留原文逐级合并内部修改，仅复制发生变化的资源分支
// 入参: node 实例节点
// 返回: []byte 新片段, bool 是否变化, error 错误信息
func (e *Editor) writeCompositeNode(node *editorCompositeNode) ([]byte, bool, error) {
	patches := slices.Clone(node.patches)
	states := maps.Clone(node.states)
	if states == nil {
		states = make(map[string]editorCompositeState)
	}
	for _, child := range node.children {
		data, changed, err := e.writeCompositeNode(child)
		if err != nil {
			return nil, false, err
		}
		if changed {
			patches = append(patches, editorXMLPatch{child.span.start, child.span.end, data})
			removeCompositeStates(states, child.node)
			maps.Copy(states, child.states)
		}
	}
	data := editorPatchXML(node.data, patches)
	changed := node.changed || len(patches) != 0
	node.states = states
	if node.ref != nil {
		fragment, dirty, err := e.writeCompositeNode(node.ref)
		if err != nil {
			return nil, false, err
		}
		if dirty {
			id, err := e.addCompositeResource(fragment, node.ref.states)
			if err != nil {
				return nil, false, err
			}
			root, err := parseEditorXML(data)
			if err != nil {
				return nil, false, err
			}
			after := node.object
			after.CompositeGraphicUnit.ResourceID = id
			data, err = editorXMLObject(data, root, node.object, after)
			if err != nil {
				return nil, false, err
			}
			changed = true
		}
	}
	return data, changed, nil
}

// addCompositeResource 注册独立复合资源并重映射内部标识，保留未知XML
// 入参: data 复合资源片段, states 内部会话记录
// 返回: string 新资源标识, error 错误信息
func (e *Editor) addCompositeResource(data []byte, states map[string]editorCompositeState) (string, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	if !editorXMLCopyable(root) {
		return "", fmt.Errorf("composite contains unsupported identifiers")
	}
	ids := make(map[string]string)
	if err := e.collectCompositeIDs(root, ids); err != nil {
		return "", err
	}
	id := ids[editorResourceID(root.attr("ID"))]
	data, err = editorXMLRemapIDs(data, root, ids)
	if err != nil {
		return "", err
	}
	data, err = editorXMLContainer("CompositeGraphicUnits", nil, bytes.TrimPrefix(data, []byte(xml.Header)))
	if err != nil {
		return "", err
	}
	data, err = editorXMLContainer("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}}, bytes.TrimPrefix(data, []byte(xml.Header)))
	if err != nil {
		return "", err
	}
	resource, err := e.compositeResource(id, data)
	if err != nil {
		return "", err
	}
	resource.states = remapCompositeStates(states, ids)
	e.resources = append(e.resources, resource)
	return id, nil
}

// compositeResource 记录生成复合资源的完整引用，用于保存时按依赖保留资源
// 入参: id 资源标识, data 资源XML
// 返回: editorResource 资源, error 错误信息
func (e *Editor) compositeResource(id string, data []byte) (editorResource, error) {
	refs, err := editorResourceReferences(data)
	if err != nil {
		return editorResource{}, err
	}
	return editorResource{name: e.packageName("Res/Composites/Composite_" + id + ".xml"), data: data, composite: id, references: refs}, nil
}

// editorResourceReferences 收集标准资源XML中的对象引用
// 入参: data 资源XML
// 返回: []string 引用标识, error 错误信息
func editorResourceReferences(data []byte) ([]string, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]bool)
	var collect func(*editorXML)
	collect = func(node *editorXML) {
		if node.name.Space == "" || node.name.Space == ofdNamespace || node.name.Space == "http://www.ofdspec.org" {
			for _, attr := range node.attrs {
				if attr.Name.Space == "" && editorObjectReference(attr.Name.Local) {
					refs[attr.Value] = true
				}
			}
		}
		for _, child := range node.children {
			collect(child)
		}
	}
	collect(root)
	return slices.Sorted(maps.Keys(refs)), nil
}

// collectCompositeReferences 收集内联复合对象引用的新资源
// 入参: composite 复合对象, used 引用集合
func collectCompositeReferences(composite CompositeGraphicUnit, used map[string]bool) {
	used[composite.ResourceID] = true
	used[composite.DrawParam] = true
	for _, object := range composite.Objects {
		collectObjectReferences(object, used)
	}
}
