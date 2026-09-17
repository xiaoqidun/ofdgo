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
	if n.boundaryInCTM {
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
		data, err := encodeOFDXML(func(x *ofdXML) {
			x.clips(&Clips{Clip: []Clip{{Area: []ClipArea{{CTM: inverse.String(), Path: []PathObject{shape}}}}}})
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
			clip, err := editorXMLStandalone(data[root.children[0].start:root.children[0].end], root.children[0])
			if err != nil {
				return err
			}
			content := append(bytes.Clone(node.data[original.open:original.close]), clip...)
			patch = editorXMLContent(node.data, original, content)
		} else {
			data, err = editorXMLStandalone(data, root)
			if err != nil {
				return err
			}
			content := append(data, node.data[node.node.open:node.node.close]...)
			patch = editorXMLContent(node.data, node.node, content)
		}
		updated, err := newEditorCompositeNode(editorPatchXML(node.data, []editorXMLPatch{patch}))
		if err != nil {
			return err
		}
		node.data, node.node, node.object, node.changed = updated.data, updated.node, updated.object, true
		return nil
	})
}
