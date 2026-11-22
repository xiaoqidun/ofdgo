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
	"context"
	"fmt"
	"maps"
	"slices"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// transformLineBox 变换起点及有符号端点位移
// 入参: box 直线端点, matrix 坐标变换
// 返回: Box 变换后的端点
func transformLineBox(box Box, matrix Matrix) Box {
	x, y := matrix.Transform(box.X, box.Y)
	w, h := basegeom.Vector(basegeom.MatrixFromValues(matrix.Values()), box.W, box.H)
	return Box{X: x, Y: y, W: w, H: h}
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
// 入参: page 页面索引, path 父复合路径, indexes 同一容器的成员序号, dx 页面横向位移, dy 页面纵向位移
// 返回: []int 按绘制顺序排列的副本序号, error 错误信息
func (e *Editor) CopyCompositeObjects(page int, path ObjectPath, indexes []int, dx, dy float64) ([]int, error) {
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("copy requires finite offsets")
	}
	var result []int
	metadata := make(map[int]*richTextSnapshot)
	err := e.Transaction(func(e *Editor) error {
		err := e.editCompositeObjects(page, path, indexes, func(renderer *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
			container := nodes[0].span.parent
			for i, node := range nodes {
				if !members[i].Capabilities.Copy || node.span.parent != container {
					return fmt.Errorf("copy requires editable members in the same container")
				}
			}
			slices.SortFunc(nodes, func(a, b *editorCompositeNode) int { return a.index - b.index })
			for i, node := range nodes {
				snapshot, err := e.captureRichTextSnapshot(page, node.object)
				if err != nil {
					return err
				}
				if snapshot != nil {
					metadata[i] = snapshot
				}
			}
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
			return err
		}
		return e.putCompositeRichTextCopies(page, path, result, metadata)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
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
// 更换主图资源时清除旧替换图引用，不修改其他共享实例
// 入参: page 页面索引, path 父复合路径, index 图片序号, data AddImage支持的图片数据
// 返回: error 错误信息
func (e *Editor) ReplaceCompositeImage(page int, path ObjectPath, index int, data []byte) error {
	return e.editCompositeObjects(page, path, []int{index}, func(renderer *Renderer, nodes []*editorCompositeNode, _ []CompositeMember) error {
		node := nodes[0]
		if node.object.Type != "ImageObject" {
			return fmt.Errorf("composite member is not an image")
		}
		object := node.object
		decoded, _, err := baseimage.DecodePixelsContext(context.Background(), data)
		if err != nil {
			return err
		}
		if object.ImageObject.ImageMask != "" {
			mask, err := renderer.ImageResource(object.ImageObject.ImageMask)
			if err != nil {
				return err
			}
			if err := validateImageMask(context.Background(), mask, decoded.Bounds().Size()); err != nil {
				return err
			}
		}
		id, err := e.addDecodedImage(data, decoded)
		if err != nil {
			return err
		}
		if !sameResourceID(object.ImageObject.ResourceID, id) {
			object.ImageObject.Substitution = ""
		}
		object.ImageObject.ResourceID = id
		return node.update(object)
	})
}

// ReshapeCompositeFrame 沿内部矩形或椭圆自身方向拉伸，不修改共享实例
// 入参: page 页面索引, path 父路径, index 成员序号, box ShapeFrame局部目标范围
// 返回: error 错误信息
func (e *Editor) ReshapeCompositeFrame(page int, path ObjectPath, index int, box Box) error {
	return e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		kind, previous, _ := members[0].ShapeFrame()
		if kind == "" {
			return fmt.Errorf("composite member is not a framed shape")
		}
		if box == previous {
			return nil
		}
		node := nodes[0]
		object := cloneEditorData(node.object)
		if !node.boundaryInCTM {
			object = compositeBoundary(object, node.parent, true)
		}
		var err error
		object.PathObject, err = object.PathObject.ReshapeFrame(box)
		if err != nil {
			return err
		}
		if !node.boundaryInCTM {
			object = compositeBoundary(object, node.parent, false)
		}
		object.PathObject.Clips = node.object.PathObject.Clips
		return node.update(object)
	})
}

// ReshapeCompositeLine 修改内部直线的页面端点及箭头，保留原始属性和父变换
// 入参: page 页面索引, path 父路径, index 成员序号, kind 直线类型, box 页面端点
// 返回: error 错误信息
func (e *Editor) ReshapeCompositeLine(page int, path ObjectPath, index int, kind ShapeKind, box Box) error {
	if kind != "" && !lineShapeKind(kind) {
		return fmt.Errorf("unsupported line kind %q", kind)
	}
	return e.reshapeCompositeObject(page, path, index, kind, box, true)
}

// ReshapeCompositeObject 调整内部基本路径的页面几何，保留绘制样式、变换与裁剪
// 入参: page 页面索引, path 父路径, index 成员序号, box 页面范围，直线使用有符号端点位移
// 返回: error 错误信息
func (e *Editor) ReshapeCompositeObject(page int, path ObjectPath, index int, box Box) error {
	return e.reshapeCompositeObject(page, path, index, "", box, false)
}

// CopyCompositeStyle 将同类型样式复制到内部选区，保留内容、位置、裁剪及其他实例
// 描边复制页面实际线宽和虚线，文字沿用目标段落和基线，全部成功后提交一次撤销
// 入参: page 页面索引, path 父路径, indexes 成员序号, source Object或CompositeMember.Style取得的样式快照
// 返回: error 错误信息
func (e *Editor) CopyCompositeStyle(page int, path ObjectPath, indexes []int, source GraphicObject) error {
	if err := e.validateCopyStyle(source); err != nil {
		return err
	}
	return e.editCompositeObjects(page, path, indexes, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		if len(nodes) == 0 {
			return nil
		}
		var err error
		source, err = e.explicitDefaultPaint(source)
		if err != nil {
			return err
		}
		for i, node := range nodes {
			member := members[i]
			if node.object.Type != source.Type || !member.Capabilities.Transform || source.Type != "ImageObject" && !member.Capabilities.Paint {
				return fmt.Errorf("style requires editable members of the same type")
			}
			object := cloneEditorData(member.Object)
			switch source.Type {
			case "TextObject":
				if err := e.updateCompositeText(node, member, nil, TextStyle{Font: source.TextObject.Font, Size: source.TextObject.Size}, nil, nil); err != nil {
					return err
				}
				var err error
				object, err = e.compositeMemberStyle(node)
				if err != nil {
					return err
				}
				style, err := copyEditorPathStyle(object.TextObject.pathStyle(), source.TextObject.pathStyle(), editorStrokeScale(source.TextObject.CTM)/safeStrokeScale(member.StrokeScale))
				if err != nil {
					return err
				}
				if err := validateEditorStroke(style); err != nil {
					return err
				}
				object.TextObject = object.TextObject.withPathStyle(style)
				object.TextObject.DrawParam, err = e.neutralDrawParam()
				if err != nil {
					return err
				}
			case "PathObject":
				var err error
				object.PathObject, err = copyEditorPathStyle(object.PathObject, source.PathObject, editorStrokeScale(source.PathObject.CTM)/safeStrokeScale(member.StrokeScale))
				if err != nil {
					return err
				}
				if err := validateEditorStroke(object.PathObject); err != nil {
					return err
				}
				object.PathObject.DrawParam, err = e.neutralDrawParam()
				if err != nil {
					return err
				}
			case "ImageObject":
				object.ImageObject.Alpha = cloneEditorData(source.ImageObject.Alpha)
			}
			if err := node.update(object); err != nil {
				return err
			}
		}
		return nil
	})
}

// Shape 获取内部基本路径的页面几何，矩形和椭圆要求轴对齐，其他成员返回空类型
// 返回: ShapeKind 图形类型, Box 几何范围，直线使用起点及有符号端点位移
func (m CompositeMember) Shape() (ShapeKind, Box) {
	if m.Object.Type != "PathObject" || !m.Capabilities.Transform {
		return "", Box{}
	}
	kind, box := m.Object.PathObject.Shape()
	if kind == "" {
		return "", Box{}
	}
	parent, ok := m.shapeParent()
	if !ok {
		return "", Box{}
	}
	if lineShapeKind(kind) {
		return kind, transformLineBox(box, parent)
	}
	if !basegeom.AxisAligned(basegeom.MatrixFromValues(parent.Values())) {
		return "", Box{}
	}
	return kind, parent.TransformBox(box)
}

// ShapeFrame 获取内部矩形或椭圆的局部范围及页面变换
// 返回: ShapeKind 图形类型, Box 局部范围, Matrix 页面变换
func (m CompositeMember) ShapeFrame() (ShapeKind, Box, Matrix) {
	if m.Object.Type != "PathObject" || !m.Capabilities.Transform {
		return "", Box{}, Matrix{}
	}
	kind, box, _ := m.Object.PathObject.ShapeFrame()
	if _, ok := m.Matrix.Invert(); !ok {
		return "", Box{}, Matrix{}
	}
	return kind, box, m.Matrix
}

// Style 获取内部成员的独立样式快照，文字和路径CTM只表达页面描边倍率
// 可传入CopyStyle或CopyCompositeStyle，不作为几何复制快照
// 返回: GraphicObject 有效样式来源
func (m CompositeMember) Style() GraphicObject {
	object := cloneEditorData(m.Object)
	if object.Type == "PathObject" {
		object.PathObject.CTM = Matrix{a: m.StrokeScale, d: m.StrokeScale}.String()
	} else if object.Type == "TextObject" {
		object.TextObject.CTM = Matrix{a: m.StrokeScale, d: m.StrokeScale}.String()
	}
	return object
}

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

// copyCompositeNodes 复制成员原文并统一重映射标识，保持副本之间的引用
// 入参: renderer 渲染器, nodes 快照, dx 页面横向位移, dy 页面纵向位移
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

// update 合并成员属性变化，不重写原文中未修改的字段
// 入参: object 修改后的对象
// 返回: error 错误信息
func (n *editorCompositeNode) update(object GraphicObject) error {
	if err := validateEditorGeometry(object); err != nil {
		return err
	}
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

// shapeParent 获取路径所在坐标到页面的变换，兼容旧式内联边界
// 返回: Matrix 路径父坐标变换, bool 是否可逆
func (m CompositeMember) shapeParent() (Matrix, bool) {
	p := m.Object.PathObject
	box, _ := ParseBox(p.Boundary)
	inverse, ok := TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(p.CTM)).Invert()
	return m.Matrix.Multiply(inverse), ok
}

// reshapeCompositeObject 按页面几何修改单个内部路径，可选修改箭头类型
// 入参: page 页面索引, path 父路径, index 成员序号, kind 目标类型，空值保持原类型, box 页面几何, lineOnly 是否仅接受直线
// 返回: error 错误信息
func (e *Editor) reshapeCompositeObject(page int, path ObjectPath, index int, kind ShapeKind, box Box, lineOnly bool) error {
	return e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		before, geometry := members[0].Shape()
		if before == "" || lineOnly && !lineShapeKind(before) {
			return fmt.Errorf("composite member is not a supported shape")
		}
		if kind == "" {
			kind = before
		}
		if kind == before && geometry == box {
			return nil
		}
		node := nodes[0]
		inverse, _ := node.parent.Invert()
		object := cloneEditorData(node.object)
		if !node.boundaryInCTM {
			object = compositeBoundary(object, node.parent, true)
		}
		var err error
		if lineShapeKind(kind) {
			object.PathObject, err = object.PathObject.ReshapeLine(kind, transformLineBox(box, inverse))
		} else {
			if err := validateCreationBox(box); err != nil {
				return err
			}
			object.PathObject, err = object.PathObject.Reshape(inverse.TransformBox(box))
		}
		if err != nil {
			return err
		}
		if !node.boundaryInCTM {
			object = compositeBoundary(object, node.parent, false)
		}
		object.PathObject.Clips = node.object.PathObject.Clips
		return node.update(object)
	})
}

// collectCompositeIDs 为已校验副本中的标准标识分配新值，拒绝重复标识
// 入参: node 原节点, ids 原标识与新标识
// 返回: error 错误信息
func (e *Editor) collectCompositeIDs(node *editorXML, ids map[string]string) error {
	if id := editorResourceID(node.attr("ID")); id != "" {
		if ids[id] != "" {
			return fmt.Errorf("duplicate object ID %q", id)
		}
		nextID, err := e.nextID()
		if err != nil {
			return err
		}
		ids[id] = nextID
	}
	for _, child := range node.children {
		if err := e.collectCompositeIDs(child, ids); err != nil {
			return err
		}
	}
	return nil
}
