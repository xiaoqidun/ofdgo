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

import "fmt"

// Style 获取内部成员的独立样式快照，路径CTM只表达页面描边倍率
// 可传入CopyStyle或CopyCompositeStyle，不作为几何复制快照
// 返回: GraphicObject 有效样式来源
func (m CompositeMember) Style() GraphicObject {
	object := cloneEditorData(m.Object)
	if object.Type == "PathObject" {
		object.PathObject.CTM = Matrix{a: m.StrokeScale, d: m.StrokeScale}.String()
	}
	return object
}

// CopyCompositeStyle 将同类型样式复制到内部选区，保留内容、位置、裁剪及其他实例
// 路径复制页面实际线宽和虚线，文字沿用目标段落和基线，全部成功后提交一次撤销
// 入参: page 页面索引, path 父路径, indexes 成员序号, source Object或CompositeMember.Style取得的样式快照
// 返回: error 错误信息
func (e *Editor) CopyCompositeStyle(page int, path ObjectPath, indexes []int, source GraphicObject) error {
	if err := e.validateCopyStyle(source); err != nil {
		return err
	}
	return e.editCompositeObjects(page, path, indexes, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
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
				object.TextObject.FillColor = cloneEditorData(source.TextObject.FillColor)
				object.TextObject.Alpha = cloneEditorData(source.TextObject.Alpha)
				object.TextObject.DrawParam, err = e.neutralDrawParam()
				if err != nil {
					return err
				}
			case "PathObject":
				object.PathObject = copyEditorPathStyle(object.PathObject, source.PathObject, editorStrokeScale(source.PathObject.CTM)/member.StrokeScale)
				if err := validateEditorStroke(object.PathObject); err != nil {
					return err
				}
				var err error
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
