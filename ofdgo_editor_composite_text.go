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
	"image/color"
	"strconv"
)

// UpdateCompositeText 修改内部普通横向文字，保留首行起点、变换、裁剪及未修改属性
// 复杂字形定位不自动重排，空样式保留原字体、字号和颜色
// 入参: page 页面索引, path 父复合路径, index 成员序号, value 文字, style 文字样式增量
// 返回: error 错误信息
func (e *Editor) UpdateCompositeText(page int, path ObjectPath, index int, value string, style TextStyle) error {
	return e.changeCompositeText(page, path, []int{index}, &value, style, nil, nil)
}

// StyleCompositeText 批量修改内部文字字体、字号或颜色，换字体保留原文字定位
// 字体缺字或复杂字形映射使整批操作失败，不使用回退字体代替原文
// 入参: page 页面索引, path 父复合路径, indexes 成员序号, style 文字样式增量
// 返回: error 错误信息
func (e *Editor) StyleCompositeText(page int, path ObjectPath, indexes []int, style TextStyle) error {
	return e.changeCompositeText(page, path, indexes, nil, style, nil, nil)
}

// LayoutCompositeText 显式重排内部普通文字，保留原始基线、变换、裁剪和绘制属性
// 段落选项仅保留在编辑会话中，撤销重做随对象恢复，保存为标准TextCode定位
// 入参: page 页面索引, path 父路径, index 成员序号, value 原文, options 段落选项
// 返回: error 错误信息
func (e *Editor) LayoutCompositeText(page int, path ObjectPath, index int, value string, options TextLayout) error {
	return e.changeCompositeText(page, path, []int{index}, &value, TextStyle{}, &options, nil)
}

// ResizeCompositeTextFrame 调整内部文字框本地宽度并按指定段落选项重排
// 入参: page 页面索引, path 父路径, index 成员序号, offset 左侧偏移, width 宽度, options 段落选项
// 返回: error 错误信息
func (e *Editor) ResizeCompositeTextFrame(page int, path ObjectPath, index int, offset, width float64, options TextLayout) error {
	return e.changeCompositeText(page, path, []int{index}, nil, TextStyle{}, &options, &Box{X: offset, W: width})
}

// changeCompositeText 生成独立文字副本，全部校验通过后合并原文
// 入参: page 页面索引, path 父复合路径, indexes 成员序号, value 新内容，nil保留定位, style 样式增量, options 显式段落选项, frame 可选文字框调整
// 返回: error 错误信息
func (e *Editor) changeCompositeText(page int, path ObjectPath, indexes []int, value *string, style TextStyle, options *TextLayout, frame *Box) error {
	return e.editCompositeObjects(page, path, indexes, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		for i, node := range nodes {
			member := members[i]
			if node.object.Type != "TextObject" || !member.Capabilities.ReplaceFont {
				return fmt.Errorf("composite text style is not supported")
			}
			object := cloneEditorData(node.object)
			text := cloneEditorData(member.Object.TextObject)
			state := node.states[editorObjectID(node.object)]
			content, layout := text.TextLayout()
			original := content
			if style.Font != "" {
				text.Font = style.Font
			}
			if value != nil {
				content = *value
			}
			if options != nil {
				layout = *options
			}
			if frame != nil {
				var err error
				if !node.boundaryInCTM {
					text = compositeBoundary(GraphicObject{Type: "TextObject", TextObject: text}, node.parent, true).TextObject
				}
				text, err = text.ResizeTextFrame(frame.X, frame.W)
				if err != nil {
					return err
				}
				if !node.boundaryInCTM {
					text = compositeBoundary(GraphicObject{Type: "TextObject", TextObject: text}, node.parent, false).TextObject
				}
				object.TextObject.Boundary, object.TextObject.CTM = text.Boundary, text.CTM
			}
			if options != nil || content != original || style.Size != 0 && style.Size != text.Size {
				if style.Size != 0 {
					text.Size = style.Size
				}
				if state.layout == nil {
					if len(text.TextCode) == 0 {
						return fmt.Errorf("text codes are empty")
					}
					origin, err := creationNumbers(text.TextCode[0].X+" "+text.TextCode[0].Y, 2)
					if err != nil {
						return err
					}
					copy(state.origin[:], origin)
				}
				if err := e.layoutCompositeText(&text, content, layout, state.origin); err != nil {
					return err
				}
				object.TextObject.Size, object.TextObject.TextCode = text.Size, text.TextCode
				state.layout = &textLayout{value: content, options: layout}
			}
			if style.Font != "" && style.Font != object.TextObject.Font {
				if err := e.prepareText(&text); err != nil {
					return err
				}
				object.TextObject.Font = style.Font
			}
			if style.Color != "" {
				if err := creationColor(&FillColor{Value: style.Color}); err != nil {
					return err
				}
				var r, g, b float64
				fmt.Sscan(style.Color, &r, &g, &b)
				fill, err := e.RGBColor(color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
				if err != nil {
					return err
				}
				object.TextObject.FillColor = editorStyleColor(fill, member.Object.TextObject.FillColor)
			}
			if err := node.update(object); err != nil {
				return err
			}
			node.setState(state)
		}
		return nil
	})
}

// layoutCompositeText 重排普通文字并保持原首行基线起点
// 入参: text 有效文字样式, value 新内容, options 段落选项, origin 原始基线起点
// 返回: error 错误信息
func (e *Editor) layoutCompositeText(text *TextObject, value string, options TextLayout, origin [2]float64) error {
	if err := e.LayoutText(text, value, options); err != nil {
		return err
	}
	baseline, _ := strconv.ParseFloat(text.TextCode[0].Y, 64)
	for i := range text.TextCode {
		code := &text.TextCode[i]
		cx, _ := strconv.ParseFloat(code.X, 64)
		cy, _ := strconv.ParseFloat(code.Y, 64)
		code.X, code.Y = ofdNumber(cx+origin[0]), ofdNumber(cy+origin[1]-baseline)
	}
	return e.prepareText(text)
}
