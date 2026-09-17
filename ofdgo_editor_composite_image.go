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
)

// matrix 获取对象局部坐标到页面的变换，兼容旧式内联边界
// 入参: transform 是否包含对象CTM
// 返回: Matrix 坐标变换
func (n *editorCompositeNode) matrix(transform bool) Matrix {
	boundary, ctm := editorGeometry(n.object)
	box, _ := ParseBox(boundary)
	m := IdentityMatrix
	if transform {
		m = NewMatrix(ctm)
		if n.object.Type == "ImageObject" && ctm == "" {
			m = Matrix{a: box.W, d: box.H}
		}
	}
	if n.boundaryInCTM || n.object.Type == "CompositeObject" || n.object.Type == "CompositeGraphicUnit" {
		return n.parent.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(m)
	}
	if !transform {
		return TranslationMatrix(box.X, box.Y)
	}
	return TranslationMatrix(box.X, box.Y).Multiply(n.parent).Multiply(m)
}

// CropCompositeImage 将内部图片与页面矩形取交集，保留原裁剪、蒙版、变换和边框
// 只收窄可见区域，撤销恢复上次裁剪，不重采样图片
// 入参: page 页面索引, path 父复合路径, index 图片序号, box 页面毫米范围
// 返回: error 错误信息
func (e *Editor) CropCompositeImage(page int, path ObjectPath, index int, box Box) error {
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return err
	}
	return e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		node, member := nodes[0], members[0]
		if !member.Capabilities.CropImage {
			return fmt.Errorf("composite image cannot be cropped")
		}
		before := member.Bounds
		if box.X <= before.X && box.Y <= before.Y && box.X+box.W >= before.X+before.W && box.Y+box.H >= before.Y+before.H {
			return nil
		}
		if box.X+box.W <= before.X || box.Y+box.H <= before.Y || box.X >= before.X+before.W || box.Y >= before.Y+before.H {
			return fmt.Errorf("crop does not intersect the image")
		}
		clips := node.object.ImageObject.Clips
		inverse, _ := node.matrix(clips == nil || clips.TransFlag == nil || *clips.TransFlag).Invert()
		shape, err := NewShape(ShapeRectangle, box)
		if err != nil {
			return err
		}
		fill, stroke := true, false
		shape.Fill, shape.Stroke = &fill, &stroke
		return appendCompositeClip(node, shape, inverse, true)
	})
}

// appendCompositeClip 追加独立交集裁剪并记录可还原序号，保留原始裁剪XML
// 入参: node 对象节点, shape 裁剪路径, matrix 路径到裁剪坐标的变换, track 是否记录还原序号
// 返回: error 错误信息
func appendCompositeClip(node *editorCompositeNode, shape PathObject, matrix Matrix, track bool) error {
	clip := Clip{Area: []ClipArea{{CTM: matrix.String(), Path: []PathObject{shape}}}}
	if clips := *editorObjectClips(&node.object); clips != nil && slices.ContainsFunc(clips.Clip, func(previous Clip) bool { return reflect.DeepEqual(previous, clip) }) {
		return nil
	}
	state := node.states[editorObjectID(node.object)]
	if track {
		crop := editorCompositeCrop{exists: node.node.child("Clips") != nil}
		if state.crop != nil {
			crop = *state.crop
		}
		count := 0
		if clips := node.object.ImageObject.Clips; clips != nil {
			count = len(clips.Clip)
		}
		crop.indexes = append(slices.Clone(crop.indexes), count)
		state.crop = &crop
	}
	data, err := encodeOFDXML(func(x *ofdXML) {
		x.clips(&Clips{Clip: []Clip{clip}})
	})
	if err != nil {
		return err
	}
	data = bytes.TrimPrefix(data, []byte(xml.Header))
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	var patch editorXMLPatch
	if original := node.node.child("Clips"); original != nil {
		clip := editorXMLEncodedFragment(data, root.children[0])
		content := append(bytes.Clone(node.data[original.open:original.close]), clip...)
		patch = editorXMLContent(node.data, original, content)
	} else {
		data = editorXMLEncodedFragment(data, root)
		content := append(data, node.data[node.node.open:node.node.close]...)
		patch = editorXMLContent(node.data, node.node, content)
	}
	updated, err := newEditorCompositeNode(editorPatchXML(node.data, []editorXMLPatch{patch}))
	if err != nil {
		return err
	}
	node.data, node.node, node.object, node.changed = updated.data, updated.node, updated.object, true
	node.setState(state)
	return nil
}

// ResetCompositeImageCrop 仅移除本次编辑追加的裁剪，保留输入文件中的裁剪、蒙版和其他属性
// 入参: page 页面索引, path 父路径, index 图片序号
// 返回: error 错误信息
func (e *Editor) ResetCompositeImageCrop(page int, path ObjectPath, index int) error {
	return e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, _ []CompositeMember) error {
		return resetCompositeImageCrop(nodes[0])
	})
}

// ResetImageCrop 还原从内部范围粘贴到页面的图片裁剪，保留原始裁剪与父范围限制
// 仅处理当前会话记录的新增裁剪，无还原记录时不修改对象
// 入参: page 页面索引, id 图片标识
// 返回: error 错误信息
func (e *Editor) ResetImageCrop(page int, id string) error {
	object, err := e.Object(page, id)
	if err != nil {
		return err
	}
	if object.Type != "ImageObject" {
		return fmt.Errorf("object %q is not an image", id)
	}
	if object.state.crop == nil {
		return nil
	}
	origin := e.snapshotOrigin(object)
	var data []byte
	if origin == nil {
		data, err = editorObjectXML(object)
	} else {
		data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
		if err == nil {
			data, err = editorXMLStandalone(data, origin.node)
		}
	}
	if err != nil {
		return err
	}
	node, err := newEditorCompositeNode(data)
	if err != nil {
		return err
	}
	node.setState(object.state)
	if err := resetCompositeImageCrop(node); err != nil {
		return err
	}
	node.object.state = node.states[id]
	if origin == nil {
		return e.UpdateObject(page, id, node.object)
	}
	node.node.parent = origin.node.parent
	next := &editorObjectOrigin{page: origin.page, data: node.data, node: node.node, object: node.object}
	return e.updateObjectOrigins(page, []GraphicObject{node.object}, true, map[string]*editorObjectOrigin{id: next})
}

// resetCompositeImageCrop 恢复图片原始裁剪节点，不还原后续变换和资源替换
// 入参: node 图片节点
// 返回: error 错误信息
func resetCompositeImageCrop(node *editorCompositeNode) error {
	if node.object.Type != "ImageObject" {
		return fmt.Errorf("composite member is not an image")
	}
	state := node.states[editorObjectID(node.object)]
	if state.crop == nil {
		return nil
	}
	clips := node.node.child("Clips")
	var patches []editorXMLPatch
	if !state.crop.exists && len(node.object.ImageObject.Clips.Clip) == len(state.crop.indexes) {
		patches = append(patches, editorXMLPatch{clips.start, clips.end, nil})
	} else {
		count := 0
		for _, child := range clips.children {
			if child.name.Local == "Clip" {
				if slices.Contains(state.crop.indexes, count) {
					patches = append(patches, editorXMLPatch{child.start, child.end, nil})
				}
				count++
			}
		}
	}
	updated, err := newEditorCompositeNode(editorPatchXML(node.data, patches))
	if err != nil {
		return err
	}
	node.data, node.node, node.object, node.changed = updated.data, updated.node, updated.object, true
	state.crop = nil
	node.setState(state)
	return nil
}

// FitCompositeImage 按原像素比例适应或填充内部图片框，保留原始裁剪和蒙版
// 入参: page 页面索引, path 父路径, index 图片序号, mode 为contain或cover
// 返回: error 错误信息
func (e *Editor) FitCompositeImage(page int, path ObjectPath, index int, mode string) error {
	return e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		node := nodes[0]
		if !members[0].Capabilities.FitImage {
			return fmt.Errorf("composite image fitting is not supported")
		}
		if err := resetCompositeImageCrop(node); err != nil {
			return err
		}
		object := cloneEditorData(node.object)
		if err := e.fitImage(&object.ImageObject, mode); err != nil {
			return err
		}
		object.ImageObject.Clips = node.object.ImageObject.Clips
		if err := node.update(object); err != nil {
			return err
		}
		if mode == "contain" {
			return nil
		}
		box, _ := ParseBox(object.ImageObject.Boundary)
		shape, err := NewShape(ShapeRectangle, Box{W: box.W, H: box.H})
		if err != nil {
			return err
		}
		fill, stroke := true, false
		shape.Fill, shape.Stroke = &fill, &stroke
		frame := node.parent.Multiply(TranslationMatrix(box.X, box.Y))
		if !node.boundaryInCTM {
			frame = TranslationMatrix(box.X, box.Y).Multiply(node.parent)
		}
		clips := object.ImageObject.Clips
		inverse, _ := node.matrix(clips == nil || clips.TransFlag == nil || *clips.TransFlag).Invert()
		return appendCompositeClip(node, shape, inverse.Multiply(frame), true)
	})
}
