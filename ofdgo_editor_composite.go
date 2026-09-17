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
	"math"
	"slices"

	"github.com/tdewolff/canvas"
)

// ObjectPath 定位页面顶层对象及复合对象内的成员，Children为从0开始的逐层绘制序号
// 路径不依赖资源标识，几何变换、资源隔离及撤销重做不改变路径；成员增删或排序后需重新枚举
type ObjectPath struct {
	ID       string `json:"id"`
	Children []int  `json:"children,omitempty"`
}

// CompositeMember 复合对象的直接成员快照，Bounds与Contours使用页面毫米坐标
type CompositeMember struct {
	Object    GraphicObject
	Bounds    Box
	Contours  []ObjectContour
	Transform bool
}

// editorCompositeNode 保留原始XML及实例上下文，不修改共享资源
type editorCompositeNode struct {
	data          []byte
	node          *editorXML
	object        GraphicObject
	parent        Matrix
	boundaryInCTM bool
	defaults      *DrawParam
	clip          *canvas.Path
	alpha         *int
	visible       bool
	ref           *editorCompositeNode
	children      []*editorCompositeNode
	expanded      bool
	changed       bool
	span          *editorXML
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
	return measureCompositeMembers(renderer, members), nil
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

// ResizeCompositeObjects 等比缩放内部选区到目标范围，不重排文字或重采样图片
// 入参: page 页面索引, path 父复合对象路径, indexes 成员序号, box 页面毫米目标范围
// 返回: error 错误信息
func (e *Editor) ResizeCompositeObjects(page int, path ObjectPath, indexes []int, box Box) error {
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return err
	}
	members, err := e.CompositeObjects(page, path)
	if err != nil {
		return err
	}
	var before Box
	for _, i := range indexes {
		if i < 0 || i >= len(members) {
			return fmt.Errorf("invalid composite selection")
		}
		before = unionTextBox(before, members[i].Bounds)
	}
	if before.W <= 0 || before.H <= 0 {
		return fmt.Errorf("selection has no visible bounds")
	}
	sx, sy := box.W/before.W, box.H/before.H
	if math.Abs(sx-sy) > 1e-9*math.Max(sx, sy) {
		return fmt.Errorf("composite members require proportional resizing")
	}
	return e.TransformCompositeObjects(page, path, indexes, box.X-before.X*sx, box.Y-before.Y*sx, sx)
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
	reader, err := e.Reader()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if _, err = reader.PageContentByIndex(page); err != nil {
		reader.Close()
		return nil, nil, nil, nil, err
	}
	renderer := NewRenderer(reader, WithFontFS(e.fontFS...))
	root.parent, root.visible, root.defaults = IdentityMatrix, true, renderer.drawParamDefaults(layer.DrawParam, nil)
	scope := root
	for depth := 0; ; depth++ {
		var members []*editorCompositeNode
		members, err = scope.members(reader, renderer, make(map[string]bool))
		if err != nil {
			break
		}
		if depth == len(path.Children) {
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

// members 展开当前复合实例的资源引用与直接绘制成员
// 入参: reader 资源读取器, renderer 渲染上下文, visiting 当前资源链
// 返回: []*editorCompositeNode 成员, error 错误信息
func (n *editorCompositeNode) members(reader *Reader, renderer *Renderer, visiting map[string]bool) ([]*editorCompositeNode, error) {
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
		clip := intersectClipPath(n.clip, renderer.buildClipPath(clips, 0, 0, 0, clipMatrix))
		defaults := renderer.drawParamDefaults(c.DrawParam, n.defaults)
		alpha := mergeAlpha(c.Alpha, n.alpha)
		visible := n.visible && (c.Visible == nil || *c.Visible)
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
			n.ref.parent, n.ref.boundaryInCTM, n.ref.defaults, n.ref.clip, n.ref.alpha, n.ref.visible = matrix, true, defaults, clip, alpha, visible
		}
		var collect func(*editorXML) error
		collect = func(container *editorXML) error {
			for _, child := range container.children {
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
					item.parent, item.boundaryInCTM, item.defaults, item.clip, item.alpha, item.visible, item.span = matrix, n.boundaryInCTM, defaults, clip, alpha, visible, child
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
		members, err := n.ref.members(reader, renderer, visiting)
		delete(visiting, id)
		if err != nil {
			return nil, err
		}
		result = append(result, members...)
	}
	return append(result, n.children...), nil
}

// measureCompositeMembers 按实际父变换、裁剪和继承样式度量成员
// 入参: renderer 渲染器, nodes 成员
// 返回: []CompositeMember 独立快照及操作能力
func measureCompositeMembers(renderer *Renderer, nodes []*editorCompositeNode) []CompositeMember {
	result := make([]CompositeMember, len(nodes))
	for i, node := range nodes {
		bounds := &boundsRenderer{collect: true}
		object := mergeGraphicObjectAlpha(node.object, node.alpha)
		if node.visible {
			renderer.renderObject(canvas.NewContext(bounds), &object, 0, node.defaults, &node.parent, node.boundaryInCTM, node.clip)
		}
		_, invertible := node.parent.Invert()
		borderActions := node.object.Type == "ImageObject" && node.object.ImageObject.Border != nil && len(node.object.ImageObject.Actions) != 0
		result[i] = CompositeMember{Object: cloneEditorData(node.object), Bounds: bounds.box, Contours: bounds.contours, Transform: invertible && editorXMLTransformable(node.node) && validateEditorGeometry(node.object) == nil && !borderActions}
	}
	return result
}

// changeCompositeObjects 原子变换内部选区，沿改动路径复制共享资源
// 入参: page 页面索引, path 父路径, indexes 成员序号, transform 页面变换生成器
// 返回: error 错误信息
func (e *Editor) changeCompositeObjects(page int, path ObjectPath, indexes []int, transform func(Box) Matrix) (err error) {
	reader, renderer, root, nodes, err := e.compositeScope(page, path)
	if err != nil {
		return err
	}
	defer reader.Close()
	members := measureCompositeMembers(renderer, nodes)
	selected := make(map[int]bool)
	var box Box
	for _, i := range indexes {
		if i < 0 || i >= len(nodes) || selected[i] {
			return fmt.Errorf("invalid composite selection")
		}
		if !members[i].Transform {
			return fmt.Errorf("composite member %d cannot be transformed", i)
		}
		selected[i] = true
		box = unionTextBox(box, members[i].Bounds)
	}
	if len(selected) == 0 {
		return nil
	}
	matrix := transform(box)
	if matrix == IdentityMatrix {
		return nil
	}
	count, maximum := len(e.resources), e.maxID
	ready := e.source.idsReady
	defer func() {
		if err != nil {
			clear(e.resources[count:])
			e.resources, e.maxID = e.resources[:count], maximum
			e.source.idsReady = ready
		}
	}()
	if err = e.prepareSourceIDs(); err != nil {
		return err
	}
	for i := range selected {
		n := nodes[i]
		object := cloneEditorData(n.object)
		inverse, _ := n.parent.Invert()
		basic := object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit"
		if basic && !n.boundaryInCTM {
			object = compositeBoundary(object, n.parent, true)
		}
		local := inverse.Multiply(matrix).Multiply(n.parent)
		if object.Type == "ImageObject" && object.ImageObject.Border != nil {
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
	}
	data, _, err := e.writeCompositeNode(root)
	if err != nil {
		return err
	}
	next, err := newEditorCompositeNode(data)
	if err != nil {
		return err
	}
	origin := e.objectOrigin(path.ID)
	next.node.parent = origin.node.parent
	updated := &editorObjectOrigin{page: origin.page, data: data, node: next.node, object: next.object}
	return e.updateObjectOrigins(page, []GraphicObject{next.object}, true, map[string]*editorObjectOrigin{path.ID: updated})
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
	var patches []editorXMLPatch
	for _, child := range node.children {
		data, changed, err := e.writeCompositeNode(child)
		if err != nil {
			return nil, false, err
		}
		if changed {
			patches = append(patches, editorXMLPatch{child.span.start, child.span.end, data})
		}
	}
	data := editorPatchXML(node.data, patches)
	changed := node.changed || len(patches) != 0
	if node.ref != nil {
		fragment, dirty, err := e.writeCompositeNode(node.ref)
		if err != nil {
			return nil, false, err
		}
		if dirty {
			id, err := e.addCompositeResource(fragment)
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
// 入参: data 复合资源片段
// 返回: string 新资源标识, error 错误信息
func (e *Editor) addCompositeResource(data []byte) (string, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	if !editorXMLCopyable(root) {
		return "", fmt.Errorf("composite contains unsupported identifiers")
	}
	ids := make(map[string]string)
	var collect func(*editorXML) error
	collect = func(node *editorXML) error {
		if id := editorResourceID(node.attr("ID")); id != "" {
			if ids[id] != "" {
				return fmt.Errorf("duplicate object ID %q", id)
			}
			ids[id] = e.nextID()
		}
		for _, child := range node.children {
			if err := collect(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(root); err != nil {
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
	e.resources = append(e.resources, resource)
	return id, nil
}

// compositeResource 记录生成复合资源的完整引用，用于保存时按依赖保留资源
// 入参: id 资源标识, data 资源XML
// 返回: editorResource 资源, error 错误信息
func (e *Editor) compositeResource(id string, data []byte) (editorResource, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return editorResource{}, err
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
	return editorResource{name: e.resourceDirectory() + "/Composite_" + id + ".xml", data: data, composite: id, references: slices.Sorted(maps.Keys(refs))}, nil
}

// collectCompositeReferences 收集内联复合对象引用的新资源
// 入参: composite 复合对象, used 引用集合
func collectCompositeReferences(composite CompositeGraphicUnit, used map[string]bool) {
	used[composite.ResourceID] = true
	for _, object := range composite.Objects {
		switch object.Type {
		case "TextObject":
			used[object.TextObject.Font] = true
		case "ImageObject":
			used[object.ImageObject.ResourceID] = true
			used[object.ImageObject.ImageMask] = true
		case "CompositeObject", "CompositeGraphicUnit":
			collectCompositeReferences(object.CompositeGraphicUnit, used)
		}
	}
}
