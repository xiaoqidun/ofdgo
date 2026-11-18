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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"reflect"
	"slices"
	"strconv"
)

// richTextTagType 标识富文本编辑标引，正文使用标准复合资源和图元
const richTextTagType = "OFDGo.RichText"

// richTextNamespace 标识富文本编辑语义的命名空间，版本仅随不兼容的语义变更升级
const richTextNamespace = "https://ofdgo.aite.me/ns/rich-text/1"

// AddRichText 原子添加标准组合及持久化编辑标引，成功时只产生一条历史
// 按给定宽度排版，高度随内容自适应
// 入参: page 页面索引, box 页面编辑框, value 富文本内容
// 返回: string 组合对象标识, error 排版、资源或持久化错误
func (e *Editor) AddRichText(page int, box Box, value RichText) (string, error) {
	var id string
	err := e.Transaction(func(e *Editor) error {
		if _, err := e.page(page); err != nil {
			return err
		}
		if _, err := creationBox(editorBoxString(box)); err != nil {
			return err
		}
		object, meta, err := e.prepareRichText(box, value)
		if err != nil {
			return err
		}
		id, err = e.AddObject(page, object)
		if err != nil {
			return err
		}
		return e.putRichTextMetadata(page, id, meta, -1)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// UpdateRichText 原子替换富文本内容，保留对象标识、段落宽度及页面变换，高度随内容调整
// 安全普通文字显式重排后提升为组合；不修改共享的旧组合资源
// 入参: page 页面索引, id 对象标识, value 完整富文本
// 返回: error 识别、排版或提交错误
func (e *Editor) UpdateRichText(page int, id string, value RichText) error {
	return e.updateRichTextFrame(page, id, value, 0, 0, false)
}

// UpdateTargetRichText 一次提交正文或组合内富文本的内容和局部宽度，保留页面变换
// 入参: page 页面索引, path 完整对象路径, value 富文本, offset 局部偏移, width 宽度，零值保留宽度模式
// 返回: error 路径、尺寸或排版错误
func (e *Editor) UpdateTargetRichText(page int, path ObjectPath, value RichText, offset, width float64) error {
	if !finite(offset) || !finite(width) || width < 0 || width == 0 && offset != 0 {
		return fmt.Errorf("invalid text frame")
	}
	if len(path.Children) == 0 {
		if path.Annotation != "" {
			return ErrNotRichText
		}
		return e.updateRichTextFrame(page, path.ID, value, offset, width, false)
	}
	index := path.Children[len(path.Children)-1]
	path.Children = path.Children[:len(path.Children)-1]
	return e.updateCompositeRichText(page, path, index, value, offset, width, false)
}

// ResizeRichTextFrame 调整局部左边界与段落宽度并原子重排，保留页面方向和缩放
// 入参: page 页面索引, id 对象标识, offset 局部横向偏移, width 新的局部段落宽度
// 返回: error 尺寸、排版或提交错误
func (e *Editor) ResizeRichTextFrame(page int, id string, offset, width float64) error {
	if !finite(offset) || !finite(width) || width <= 0 {
		return fmt.Errorf("rich text width must be positive and finite")
	}
	value, err := e.RichText(page, id)
	if err != nil {
		return err
	}
	return e.updateRichTextFrame(page, id, value, offset, width, false)
}

// DeleteRichText 原子删除富文本及其标引，不删除其他实例引用的共享资源
// 入参: page 页面索引, id 对象标识
// 返回: error 识别或删除错误
func (e *Editor) DeleteRichText(page int, id string) error {
	return e.Transaction(func(e *Editor) error {
		object, err := e.Object(page, id)
		if err != nil {
			return err
		}
		if _, err = e.RichText(page, id); err != nil {
			return err
		}
		if object.Type == "CompositeObject" {
			entry, err := e.richTextEntry(page, id)
			if err != nil {
				return err
			}
			if err := e.DeleteStructure(entry.index); err != nil {
				return err
			}
		}
		return e.DeleteObject(page, id)
	})
}

// RichText 返回独立的富文本编辑值，未标引组合返回ErrNotRichText，损坏标引返回诊断
// 普通文字仅在定位和外观可以安全提升时返回单片段，否则明确拒绝
// 入参: page 页面索引, id 对象标识
// 返回: RichText 编辑内容, error 识别或持久化错误
func (e *Editor) RichText(page int, id string) (RichText, error) {
	object, err := e.Object(page, id)
	if err != nil {
		return RichText{}, err
	}
	return e.richTextObject(page, object)
}

// RichTextFrame 返回原始局部编辑框及局部到页面的完整变换，不按组合缩放重新解释宽度
// 入参: page 页面索引, id 富文本或安全普通文字对象标识
// 返回: Box 局部编辑框, Matrix 页面变换, error 识别或几何错误
func (e *Editor) RichTextFrame(page int, id string) (Box, Matrix, error) {
	object, err := e.Object(page, id)
	if err != nil {
		return Box{}, Matrix{}, err
	}
	if object.Type == "TextObject" {
		_, frame, matrix, err := e.PrepareRichText(page, ObjectPath{ID: id}, false)
		return frame, matrix, err
	}
	if object.Type != "CompositeObject" {
		return Box{}, Matrix{}, ErrNotRichText
	}
	entry, err := e.richTextEntry(page, id)
	if err != nil {
		return Box{}, Matrix{}, err
	}
	box, err := creationBox(object.CompositeGraphicUnit.Boundary)
	if err != nil {
		return Box{}, Matrix{}, err
	}
	return entry.metadata.Frame, TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(object.CompositeGraphicUnit.CTM)).Multiply(TranslationMatrix(entry.metadata.Offset[0], entry.metadata.Offset[1])), nil
}

// AddCompositeRichText 向内部范围添加富文本，使用页面坐标并保存编辑标引
// 入参: page 页面索引, path 父路径, box 页面编辑框, value 富文本
// 返回: int 新成员序号, error 排版或提交错误
func (e *Editor) AddCompositeRichText(page int, path ObjectPath, box Box, value RichText) (int, error) {
	var index int
	err := e.Transaction(func(e *Editor) error {
		if _, err := creationBox(editorBoxString(box)); err != nil {
			return err
		}
		object, meta, err := e.prepareRichText(box, value)
		if err != nil {
			return err
		}
		index, err = e.AddCompositeObject(page, path, object)
		if err != nil {
			return err
		}
		member, err := e.CompositeObject(page, path, index)
		if err != nil {
			return err
		}
		return e.putCompositeRichTextMetadata(page, member.Object, meta, -1)
	})
	return index, err
}

// CompositeRichText 读取内部富文本值及局部编辑框，不提升未标引的普通文字
// 入参: page 页面索引, member 当前内部成员快照
// 返回: RichText 编辑内容, Box 局部编辑框, Matrix 页面变换, error 识别或持久化错误
func (e *Editor) CompositeRichText(page int, member CompositeMember) (RichText, Box, Matrix, error) {
	if _, err := e.page(page); err != nil {
		return RichText{}, Box{}, Matrix{}, err
	}
	if member.Object.Type != "CompositeObject" {
		return RichText{}, Box{}, Matrix{}, ErrNotRichText
	}
	value, err := e.richTextObject(page, member.Object)
	if err != nil {
		return RichText{}, Box{}, Matrix{}, err
	}
	entry, err := e.richTextEntry(page, editorObjectID(member.Object))
	if err != nil {
		return RichText{}, Box{}, Matrix{}, err
	}
	return value, entry.metadata.Frame, member.Matrix.Multiply(TranslationMatrix(entry.metadata.Offset[0], entry.metadata.Offset[1])), nil
}

// UpdateCompositeRichText 原子重排内部富文本，保留父变换并隔离共享实例
// 入参: page 页面索引, path 父路径, index 成员序号, value 富文本, offset 局部横向偏移, width 新段落宽度，零值沿用原宽度模式
// 返回: error 校验或提交错误
func (e *Editor) UpdateCompositeRichText(page int, path ObjectPath, index int, value RichText, offset, width float64) error {
	return e.updateCompositeRichText(page, path, index, value, offset, width, false)
}

// TargetRichText 读取完整路径文字的富文本模型，普通文字仅在可验证布局时提升
// 入参: page 页面索引, path 完整对象路径
// 返回: RichText 独立模型, error 路径或不安全提升错误
func (e *Editor) TargetRichText(page int, path ObjectPath) (RichText, error) {
	value, _, _, err := e.PrepareRichText(page, path, false)
	return value, err
}

// PrepareRichText 读取独立富文本模型和局部编辑框，定位文字采用自然宽度，已知段落保留宽度
// 显式重排保留原首行起点及页面变换，不从原始Boundary推断段落宽度
// 入参: page 页面索引, path 完整对象路径, reflow 是否允许重排普通文字
// 返回: RichText 内容, Box 局部编辑框, Matrix 页面变换, error 路径、外观或排版错误
func (e *Editor) PrepareRichText(page int, path ObjectPath, reflow bool) (RichText, Box, Matrix, error) {
	var text TextObject
	var matrix Matrix
	if len(path.Children) == 0 {
		if path.Annotation != "" {
			return RichText{}, Box{}, Matrix{}, ErrNotRichText
		}
		object, err := e.Object(page, path.ID)
		if err != nil {
			return RichText{}, Box{}, Matrix{}, err
		}
		if object.Type != "TextObject" {
			value, err := e.RichText(page, path.ID)
			if err != nil {
				return RichText{}, Box{}, Matrix{}, err
			}
			frame, matrix, err := e.RichTextFrame(page, path.ID)
			return value, frame, matrix, err
		}
		if origin := e.objectOrigin(path.ID); origin != nil && !editorXMLSupported(origin.node) {
			return RichText{}, Box{}, Matrix{}, ErrNotRichText
		}
		layer, _, err := e.findObject(page, path.ID)
		if err != nil {
			return RichText{}, Box{}, Matrix{}, err
		}
		object, err = e.resolveEditorStyle(object, layer.DrawParam)
		if err != nil {
			return RichText{}, Box{}, Matrix{}, err
		}
		text = object.TextObject
		box, err := creationBox(text.Boundary)
		if err != nil {
			return RichText{}, Box{}, Matrix{}, err
		}
		matrix = TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(text.CTM))
	} else {
		index := path.Children[len(path.Children)-1]
		path.Children = path.Children[:len(path.Children)-1]
		member, err := e.CompositeObject(page, path, index)
		if err != nil {
			return RichText{}, Box{}, Matrix{}, err
		}
		if member.Object.Type != "TextObject" {
			return e.CompositeRichText(page, member)
		}
		text, matrix = member.Object.TextObject, member.Matrix
	}
	frame, err := text.TextFrame()
	if err != nil {
		return RichText{}, Box{}, Matrix{}, err
	}
	dx, dy, err := e.richPlainOffset(text)
	if err != nil {
		return RichText{}, Box{}, Matrix{}, err
	}
	autoWidth := text.layout == nil || text.layout.autoWidth
	if reflow {
		value, layout := text.TextLayout()
		layout.AutoBidi = true
		if err := e.LayoutText(&text, value, layout); err != nil {
			return RichText{}, Box{}, Matrix{}, err
		}
	}
	value, err := e.promoteTextValue(text)
	if err != nil {
		return RichText{}, Box{}, Matrix{}, err
	}
	value.AutoWidth = autoWidth
	if autoWidth {
		result, err := e.layoutRichText(value, frame.W)
		if err != nil {
			return RichText{}, Box{}, Matrix{}, err
		}
		frame.W, frame.H = result.width, result.bounds.Y+result.bounds.H
	}
	return value, Box{W: frame.W, H: frame.H}, matrix.Multiply(TranslationMatrix(dx, dy)), nil
}

// ReflowRichText 原子重排完整路径文字，保留局部宽度、页面变换及共享实例隔离
// 入参: page 页面索引, path 完整对象路径, value 新的富文本内容
// 返回: error 路径、排版或提交错误
func (e *Editor) ReflowRichText(page int, path ObjectPath, value RichText) error {
	if len(path.Children) == 0 {
		if path.Annotation != "" {
			return ErrNotRichText
		}
		return e.updateRichTextFrame(page, path.ID, value, 0, 0, true)
	}
	index := path.Children[len(path.Children)-1]
	path.Children = path.Children[:len(path.Children)-1]
	return e.updateCompositeRichText(page, path, index, value, 0, 0, true)
}

// LayoutTextFrame 统一调整普通文字及富文本的段落选项和局部宽度，保留原首行起点与页面变换
// 对齐应用于所有段落；定位文字沿用自然宽度，显式改宽后采用固定宽度，提交一条撤销记录
// 入参: page 页面索引, path 完整对象路径, options 段落选项, offset 左边界局部偏移, width 新宽度，零值保留宽度模式
// 返回: error 路径、尺寸或排版错误
func (e *Editor) LayoutTextFrame(page int, path ObjectPath, options TextLayout, offset, width float64) error {
	if !finite(offset) || !finite(width) || width < 0 || width == 0 && offset != 0 {
		return fmt.Errorf("invalid text frame")
	}
	value, _, _, err := e.PrepareRichText(page, path, true)
	if err != nil {
		return err
	}
	value.Layout, value.Paragraphs = options, nil
	if len(path.Children) == 0 {
		return e.updateRichTextFrame(page, path.ID, value, offset, width, true)
	}
	index := path.Children[len(path.Children)-1]
	path.Children = path.Children[:len(path.Children)-1]
	return e.updateCompositeRichText(page, path, index, value, offset, width, true)
}

// richTextMetadata 不保存字体编号，以标准主文字对象序号恢复当前文档字体
type richTextMetadata struct {
	Version int        `json:"version"`
	Value   RichText   `json:"value"`
	Frame   Box        `json:"frame"`
	Extent  Box        `json:"extent"`
	Offset  [2]float64 `json:"offset"`
	Anchors []int      `json:"anchors"`
	Digest  string     `json:"digest"`
}

// richTextSnapshot 保存对象及嵌套成员的不可变编辑语义，成员按绘制顺序定位
type richTextSnapshot struct {
	metadata *richTextMetadata
	children map[int]*richTextSnapshot
}

// richTextEntry 保存标引入口及延迟报告的损坏诊断
type richTextEntry struct {
	index    int
	metadata richTextMetadata
	err      error
}

// richTextIndex 缓存当前文档修订的标引，不创建页面预览快照
type richTextIndex struct {
	source   *editorSource
	revision uint64
	remaps   map[string]int
	entries  map[string]richTextEntry
	err      error
}

// updateRichTextFrame 统一提交正文或编辑框变化，width为零时沿用原宽度模式
// 入参: page 页面索引, id 对象标识, value 新内容, offset 局部偏移, width 可选宽度, reflow 是否显式重排原文
// 返回: error 校验或提交错误
func (e *Editor) updateRichTextFrame(page int, id string, value RichText, offset, width float64, reflow bool) error {
	return e.Transaction(func(e *Editor) error {
		old, err := e.Object(page, id)
		if err != nil {
			return err
		}
		current, frame, matrix, err := e.PrepareRichText(page, ObjectPath{ID: id}, reflow)
		if err != nil {
			return err
		}
		if err := value.Validate(); err != nil {
			return err
		}
		if width > 0 {
			value.AutoWidth = false
		}
		if old.Type == "CompositeObject" && offset == 0 && (width == 0 || width == frame.W) && reflect.DeepEqual(normalizeRichText(current), normalizeRichText(value)) {
			return nil
		}
		if width > 0 {
			frame.W = width
			matrix = matrix.Multiply(TranslationMatrix(offset, 0))
		}
		object, meta, err := e.prepareRichText(Box{W: frame.W, H: frame.H}, value)
		if err != nil {
			return err
		}
		object.CompositeGraphicUnit.ID = id
		placement := richTextPlacement(meta, matrix)
		object.CompositeGraphicUnit.Boundary = placement.Boundary
		object.CompositeGraphicUnit.CTM = placement.CTM
		if old.Type == "CompositeObject" {
			unit := old.CompositeGraphicUnit
			if unit.Clips != nil {
				return fmt.Errorf("clipped rich text requires explicit clip removal before reflow")
			}
			unit.ResourceID = object.CompositeGraphicUnit.ResourceID
			unit.Boundary = placement.Boundary
			unit.CTM = placement.CTM
			object.CompositeGraphicUnit = unit
		}
		index := -1
		if old.Type == "CompositeObject" {
			entry, err := e.richTextEntry(page, id)
			if err != nil {
				return err
			}
			index = entry.index
		}
		if err := e.updateObjects(page, []GraphicObject{object}, true); err != nil {
			return err
		}
		return e.putRichTextMetadata(page, id, meta, index)
	})
}

// richTextObject 读取对象的编辑标引并校验标准内容
// 入参: page 页面索引, object 当前对象
// 返回: RichText 编辑内容, error 识别或持久化错误
func (e *Editor) richTextObject(page int, object GraphicObject) (RichText, error) {
	if object.Type == "TextObject" {
		return e.promoteRichText(page, object)
	}
	if object.Type != "CompositeObject" {
		return RichText{}, ErrNotRichText
	}
	entry, err := e.richTextEntry(page, editorObjectID(object))
	if err != nil {
		return RichText{}, err
	}
	unit, err := e.compositeDefinition(object.CompositeGraphicUnit.ResourceID)
	if err != nil {
		return RichText{}, fmt.Errorf("rich text resource: %w", err)
	}
	objects := unit.object.CompositeGraphicUnit.Objects
	if richTextDigest(objects) != entry.metadata.Digest {
		return RichText{}, fmt.Errorf("rich text content no longer matches its editing metadata")
	}
	value := cloneEditorData(entry.metadata.Value)
	if len(entry.metadata.Anchors) != len(value.Runs) {
		return RichText{}, fmt.Errorf("invalid rich text font anchors")
	}
	for i, anchor := range entry.metadata.Anchors {
		if anchor < 0 || anchor >= len(objects) || objects[anchor].Type != "TextObject" {
			return RichText{}, fmt.Errorf("invalid rich text font anchor %d", i)
		}
		value.Runs[i].Format.Font = objects[anchor].TextObject.Font
	}
	if err := value.Validate(); err != nil {
		return RichText{}, fmt.Errorf("invalid rich text metadata: %w", err)
	}
	return value, nil
}

// updateCompositeRichText 统一处理内部文字的安全提升与显式重排
// 入参: page 页面索引, path 父路径, index 成员序号, value 富文本, offset 局部偏移, width 可选宽度, reflow 是否显式重排
// 返回: error 校验或提交错误
func (e *Editor) updateCompositeRichText(page int, path ObjectPath, index int, value RichText, offset, width float64, reflow bool) error {
	if !finite(offset) || !finite(width) || width < 0 || width == 0 && offset != 0 {
		return fmt.Errorf("invalid rich text frame")
	}
	if width > 0 {
		value.AutoWidth = false
	}
	return e.Transaction(func(e *Editor) error {
		member, err := e.CompositeObject(page, path, index)
		if err != nil {
			return err
		}
		if member.Object.Type == "TextObject" {
			return e.updateCompositePlainRichText(page, path, index, value, offset, width, reflow)
		}
		var metadata richTextMetadata
		entryIndex := -1
		changed := false
		err = e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
			node := nodes[0]
			current, frame, _, err := e.CompositeRichText(page, members[0])
			if err != nil {
				return fmt.Errorf("read composite member %s: %w", editorObjectID(members[0].Object), err)
			}
			if err := value.Validate(); err != nil {
				return err
			}
			if offset == 0 && (width == 0 || width == frame.W) && reflect.DeepEqual(normalizeRichText(current), normalizeRichText(value)) {
				return nil
			}
			if node.object.CompositeGraphicUnit.Clips != nil {
				return fmt.Errorf("clipped rich text requires explicit clip removal before reflow")
			}
			entry, err := e.richTextEntry(page, editorObjectID(node.object))
			if err != nil {
				return fmt.Errorf("read composite node %s: %w", editorObjectID(node.object), err)
			}
			object := cloneEditorData(node.object)
			if !node.boundaryInCTM {
				object = compositeBoundary(object, node.parent, true)
			}
			unit := &object.CompositeGraphicUnit
			box, err := creationBox(unit.Boundary)
			if err != nil {
				return err
			}
			matrix := TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(unit.CTM)).Multiply(TranslationMatrix(entry.metadata.Offset[0]+offset, entry.metadata.Offset[1]))
			if width > 0 {
				frame.W = width
			}
			next, meta, err := e.prepareRichText(frame, value)
			if err != nil {
				return err
			}
			placement := richTextPlacement(meta, matrix)
			unit.ResourceID, unit.Boundary, unit.CTM = next.CompositeGraphicUnit.ResourceID, placement.Boundary, placement.CTM
			if !node.boundaryInCTM {
				object = compositeBoundary(object, node.parent, false)
			}
			metadata, entryIndex, changed = meta, entry.index, true
			return node.update(object)
		})
		if err != nil || !changed {
			return err
		}
		member, err = e.CompositeObject(page, path, index)
		if err != nil {
			return err
		}
		if entry, err := e.richTextEntry(page, editorObjectID(member.Object)); err == nil {
			entryIndex = entry.index
		} else if !errors.Is(err, ErrNotRichText) {
			return err
		}
		return e.putCompositeRichTextMetadata(page, member.Object, metadata, entryIndex)
	})
}

// putCompositeRichTextMetadata 在容器坐标规范化后更新标准内容摘要和编辑标引
// 入参: page 页面索引, object 当前成员, meta 编辑信息, index 原入口或-1
// 返回: error 资源或提交错误
func (e *Editor) putCompositeRichTextMetadata(page int, object GraphicObject, meta richTextMetadata, index int) error {
	unit, err := e.compositeDefinition(object.CompositeGraphicUnit.ResourceID)
	if err != nil {
		return err
	}
	meta.Digest = richTextDigest(unit.object.CompositeGraphicUnit.Objects)
	return e.putRichTextMetadata(page, editorObjectID(object), meta, index)
}

// putCompositeRichTextCopies 一次读取复制后的选区，按对象顺序恢复富文本标引
// 入参: page 页面索引, path 父路径, indexes 新成员序号, metadata 选区位置对应的编辑语义
// 返回: error 读取或提交错误
func (e *Editor) putCompositeRichTextCopies(page int, path ObjectPath, indexes []int, metadata map[int]*richTextSnapshot) error {
	if len(metadata) == 0 {
		return nil
	}
	selected := make([]int, 0, len(metadata))
	values := make([]*richTextSnapshot, 0, len(metadata))
	for i, index := range indexes {
		if meta, ok := metadata[i]; ok {
			selected = append(selected, index)
			values = append(values, meta)
		}
	}
	members, err := e.CompositeObjectsAt(page, path, selected)
	if err != nil {
		return err
	}
	for i, member := range members {
		if err := e.putRichTextSnapshot(page, member.Object, values[i]); err != nil {
			return err
		}
	}
	return nil
}

// prepareRichText 构造独立组合资源和不含字体编号的编辑标引，高度随排版内容自适应
// 入参: box 页面或局部编辑框, value 富文本
// 返回: GraphicObject 标准组合, richTextMetadata 标引, error 准备错误
func (e *Editor) prepareRichText(box Box, value RichText) (GraphicObject, richTextMetadata, error) {
	if err := value.Validate(); err != nil {
		return GraphicObject{}, richTextMetadata{}, err
	}
	if box.W <= 0 || box.H <= 0 {
		return GraphicObject{}, richTextMetadata{}, fmt.Errorf("rich text frame must be positive")
	}
	result, err := e.layoutRichText(value, box.W)
	if err != nil {
		return GraphicObject{}, richTextMetadata{}, err
	}
	box.W, box.H = result.width, result.bounds.Y+result.bounds.H
	padX, padY := -min(0, result.bounds.X), -min(0, result.bounds.Y)
	extent := Box{W: max(box.W, result.bounds.X+result.bounds.W) + padX, H: box.H + padY}
	if !finite(extent.W) || !finite(extent.H) || extent.W <= 0 || extent.H <= 0 {
		return GraphicObject{}, richTextMetadata{}, fmt.Errorf("rich text extent exceeds finite range")
	}
	if err := e.prepareSourceIDs(); err != nil {
		return GraphicObject{}, richTextMetadata{}, err
	}
	for i := range result.objects {
		if padX != 0 || padY != 0 {
			result.objects[i] = transformEditorMatrix(result.objects[i], TranslationMatrix(padX, padY))
		}
		if err := e.richTextColors(&result.objects[i]); err != nil {
			return GraphicObject{}, richTextMetadata{}, err
		}
	}
	resource, err := e.addOwnedVector(context.Background(), result.objects, extent.W, extent.H)
	if err != nil {
		return GraphicObject{}, richTextMetadata{}, err
	}
	unit, err := e.compositeDefinition(resource)
	if err != nil {
		return GraphicObject{}, richTextMetadata{}, err
	}
	meta := richTextMetadata{Version: 1, Value: cloneEditorData(value), Frame: Box{W: box.W, H: box.H}, Extent: extent, Offset: [2]float64{padX, padY}, Anchors: result.anchors, Digest: richTextDigest(unit.object.CompositeGraphicUnit.Objects)}
	for i := range meta.Value.Runs {
		meta.Value.Runs[i].Format.Font = ""
	}
	object := GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: richTextPlacement(meta, TranslationMatrix(box.X, box.Y))}
	object.CompositeGraphicUnit.ResourceID = resource
	return object, meta, nil
}

// richTextPlacement 根据局部逻辑坐标确定物理组合边界，负向装饰不会被资源裁剪
// 入参: meta 编辑框与资源尺寸, matrix 逻辑坐标到页面变换
// 返回: CompositeGraphicUnit 组合几何
func richTextPlacement(meta richTextMetadata, matrix Matrix) CompositeGraphicUnit {
	matrix = matrix.Multiply(TranslationMatrix(-meta.Offset[0], -meta.Offset[1]))
	box := matrix.TransformBox(meta.Extent)
	return CompositeGraphicUnit{Boundary: editorBoxString(box), CTM: TranslationMatrix(-box.X, -box.Y).Multiply(matrix).String()}
}

// richTextDigest 对已编码图元计算不含字体、颜色空间和字形编号的内容摘要
// 入参: objects 标准子对象
// 返回: string 稳定内容摘要
func richTextDigest(objects []GraphicObject) string {
	copy := cloneEditorData(objects)
	for i := range copy {
		o := &copy[i]
		o.origin = nil
		o.state = editorCompositeState{}
		o.TextObject.ID = ""
		o.TextObject.Font = ""
		o.TextObject.CGTransform = nil
		o.TextObject.layout = nil
		o.PathObject.ID = ""
		if o.TextObject.FillColor != nil {
			o.TextObject.FillColor.ColorSpace = ""
		}
		if o.TextObject.StrokeColor != nil {
			o.TextObject.StrokeColor.ColorSpace = ""
		}
		if o.PathObject.FillColor != nil {
			o.PathObject.FillColor.ColorSpace = ""
		}
		if o.PathObject.StrokeColor != nil {
			o.PathObject.StrokeColor.ColorSpace = ""
		}
	}
	data, _ := json.Marshal(copy)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// richTextEntry 从当前文档标引缓存定位对象，损坏项不降级为普通组合
// 入参: page 页面索引, id 组合标识
// 返回: richTextEntry 标引, error 缺失或损坏错误
func (e *Editor) richTextEntry(page int, id string) (richTextEntry, error) {
	index := e.richTextMetadataIndex()
	if index.err != nil {
		return richTextEntry{}, index.err
	}
	entry, ok := index.entries[editorResourceID(e.pages[page].ID)+"/"+editorResourceID(id)]
	if !ok {
		return richTextEntry{}, ErrNotRichText
	}
	if entry.err != nil {
		return entry, entry.err
	}
	return entry, nil
}

// richTextMetadataIndex 按来源和修订缓存标引，仅读取标引文件，不生成Reader快照
// 返回: *richTextIndex 只读缓存
func (e *Editor) richTextMetadataIndex() *richTextIndex {
	if index := e.richIndex; index != nil && index.source == e.source && index.revision == e.revision {
		valid := len(index.remaps) == len(e.structureRemaps)
		for page, remaps := range e.structureRemaps {
			valid = valid && index.remaps[page] == len(remaps)
		}
		if valid {
			return index
		}
	}
	index := &richTextIndex{source: e.source, revision: e.revision, remaps: e.structureRemapStarts(), entries: make(map[string]richTextEntry)}
	e.richIndex = index
	if e.source == nil {
		return index
	}
	tags, err := e.source.reader.CustomTags()
	if err != nil {
		index.err = err
		return index
	}
	for i, tag := range tags {
		if tag.TypeID != richTextTagType || tag.NameSpace != richTextNamespace {
			continue
		}
		structure, err := e.source.reader.readStructureContext(context.Background(), tag)
		if err != nil {
			index.err = fmt.Errorf("rich text metadata: %w", err)
			return index
		}
		root := structure.Root
		entry := richTextEntry{index: i}
		if root.Name != "RichText" || root.Namespace != richTextNamespace {
			entry.err = fmt.Errorf("invalid rich text metadata root")
		} else {
			entry.err = json.Unmarshal([]byte(root.Text), &entry.metadata)
		}
		if entry.err == nil && (entry.metadata.Version != 1 || entry.metadata.Frame.X != 0 || entry.metadata.Frame.Y != 0 || !finite(entry.metadata.Frame.W) || !finite(entry.metadata.Frame.H) || entry.metadata.Frame.W <= 0 || entry.metadata.Frame.H <= 0 || len(entry.metadata.Digest) != 64) {
			entry.err = fmt.Errorf("invalid rich text metadata version or frame")
		}
		if entry.err == nil {
			meta := entry.metadata
			if meta.Extent.X != 0 || meta.Extent.Y != 0 || !finite(meta.Extent.W) || !finite(meta.Extent.H) || meta.Extent.W <= 0 || meta.Extent.H <= 0 || !finite(meta.Offset[0]) || !finite(meta.Offset[1]) || meta.Offset[0] < 0 || meta.Offset[1] < 0 || meta.Offset[0] > meta.Extent.W || meta.Offset[1] > meta.Extent.H || len(meta.Anchors) != len(meta.Value.Runs) {
				entry.err = fmt.Errorf("invalid rich text metadata extent or anchors")
			}
		}
		for _, child := range root.Children {
			for _, ref := range child.References {
				for _, id := range ref.ObjectIDs {
					key := editorResourceID(ref.PageID) + "/" + editorResourceID(id)
					if _, exists := index.entries[key]; exists {
						entry.err = fmt.Errorf("ambiguous rich text metadata")
					}
					index.entries[key] = entry
				}
			}
		}
	}
	for page, remaps := range e.structureRemaps {
		for _, ids := range remaps {
			for from, to := range ids {
				if _, exists := index.entries[page+"/"+to]; !exists {
					if entry, ok := index.entries[page+"/"+from]; ok {
						index.entries[page+"/"+to] = entry
					}
				}
			}
		}
	}
	return index
}

// putRichTextMetadata 添加或替换带有标准标引引用的富文本语义
// 入参: page 页面索引, id 对象标识, meta 编辑信息, index 原入口或-1
// 返回: error 编码或提交错误
func (e *Editor) putRichTextMetadata(page int, id string, meta richTextMetadata, index int) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	root := StructureNode{Name: "RichText", Namespace: richTextNamespace, Text: string(data)}
	if index >= 0 {
		separate := false
		err := e.editStructure(index, func(data []byte, node *editorXML, tag CustomTag) ([]byte, error) {
			updated, err := rewriteStructureReferences(context.Background(), data, tag, func(pageID string, ids []string) (string, []string, error) {
				var remaining []string
				for _, objectID := range ids {
					if editorResourceID(pageID) != editorResourceID(e.pages[page].ID) || editorResourceID(objectID) != editorResourceID(id) {
						remaining = append(remaining, objectID)
						separate = true
					}
				}
				return pageID, remaining, nil
			})
			if err != nil || separate {
				return updated, err
			}
			return updateStructureNodeXML(data, node, root)
		})
		if err != nil || !separate {
			return err
		}
	}
	root.Children = []StructureNode{{Name: "ObjectRef", Namespace: richTextNamespace, Attributes: []StructureAttribute{{Name: "PageRef", Value: e.pages[page].ID}}, Text: id}}
	_, err = e.AddStructure(CustomTag{TypeID: richTextTagType, NameSpace: richTextNamespace}, root)
	return err
}

// promoteRichText 仅提升已有明确排版或可验证单行定位的普通文字
// 入参: page 页面索引, object 普通文字快照
// 返回: RichText 单片段, error 不安全提升错误
func (e *Editor) promoteRichText(page int, object GraphicObject) (RichText, error) {
	layer, _, err := e.findObject(page, object.TextObject.ID)
	if err != nil {
		return RichText{}, err
	}
	if origin := e.objectOrigin(object.TextObject.ID); origin != nil && !editorXMLSupported(origin.node) {
		return RichText{}, &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("text cannot be safely promoted to rich text")}
	}
	object, err = e.resolveEditorStyle(object, layer.DrawParam)
	if err != nil {
		return RichText{}, err
	}
	return e.promoteTextValue(object.TextObject)
}

// promoteTextValue 校验普通文字是否可无损提升，保留明确段落或可验证单行布局
// 入参: t 有效文字样式与局部定位
// 返回: RichText 单片段, error 布局或格式错误
func (e *Editor) promoteTextValue(t TextObject) (RichText, error) {
	unsupported := func() (RichText, error) {
		return RichText{}, &EditError{Code: EditLayoutRequired, Err: fmt.Errorf("text cannot be safely promoted to rich text")}
	}
	if t.DrawParam != "" || t.Clips != nil || len(t.Actions) != 0 || t.Alpha != nil && *t.Alpha != 255 || t.Visible != nil && !*t.Visible || t.Stroke != nil && *t.Stroke || t.Fill != nil && !*t.Fill || t.Decoration != "" || t.VScale != 0 || t.HScale != 0 && t.HScale != 1 || t.ReadDirection != 0 || t.CharDirection != 0 || t.Weight != 0 && t.Weight != 400 && t.Weight != 700 {
		return unsupported()
	}
	if _, err := t.TextFrame(); err != nil {
		return unsupported()
	}
	value, layout := t.TextLayout()
	if t.layout == nil {
		if len(t.TextCode) != 1 || len(t.CGTransform) != 0 {
			return unsupported()
		}
		check := t
		if err := e.LayoutText(&check, value, TextLayout{}); err != nil {
			return RichText{}, err
		}
		actual, expected := t.TextCode[0], check.TextCode[0]
		if actual.Index != "" || len(textCodeRunes(actual.Value)) != len(textCodeRunes(expected.Value)) {
			return unsupported()
		}
		if !reflect.DeepEqual(parseFloatsWithG(actual.DeltaX), parseFloatsWithG(expected.DeltaX)) {
			return unsupported()
		}
		for _, dy := range parseFloatsWithG(actual.DeltaY) {
			if dy != 0 {
				return unsupported()
			}
		}
		if _, _, err := e.richPlainOffset(t); err != nil {
			return unsupported()
		}
	}
	color := "0 0 0"
	if t.FillColor != nil {
		c := t.FillColor
		if c.Pattern != nil || c.AxialShd != nil || c.RadialShd != nil || c.GouraudShd != nil || c.LaGouraudShd != nil || c.Alpha != nil && *c.Alpha != 255 || c.unsupported {
			return unsupported()
		}
		resolved, err := e.Color(c)
		if err != nil {
			return RichText{}, err
		}
		r, g, b, a := resolved.RGBA()
		if a != 65535 {
			return unsupported()
		}
		color = fmt.Sprintf("%d %d %d", r>>8, g>>8, b>>8)
	}
	result := RichText{AutoWidth: t.layout == nil || t.layout.autoWidth, Runs: []RichTextRun{{Text: value, Format: TextFormat{Font: t.Font, Size: t.Size, Color: color, Bold: t.Weight == 700, Italic: t.Italic}}}, Layout: layout}
	return result, result.Validate()
}

// richPlainOffset 计算原首行相对标准排版的位移，保留既有段落和组合内基线
// 入参: text 已校验的普通文字
// 返回: float64 横向偏移, float64 基线偏移, error 无效坐标
func (e *Editor) richPlainOffset(text TextObject) (float64, float64, error) {
	if len(text.TextCode) == 0 {
		return 0, 0, fmt.Errorf("text codes are empty")
	}
	value, layout := text.TextLayout()
	check := text
	if err := e.LayoutText(&check, value, layout); err != nil {
		return 0, 0, err
	}
	x, err := strconv.ParseFloat(text.TextCode[0].X, 64)
	if err != nil || !finite(x) {
		return 0, 0, fmt.Errorf("invalid text origin")
	}
	y, err := strconv.ParseFloat(text.TextCode[0].Y, 64)
	if err != nil || !finite(y) {
		return 0, 0, fmt.Errorf("invalid text baseline")
	}
	baseline, err := strconv.ParseFloat(check.TextCode[0].Y, 64)
	if err != nil {
		return 0, 0, err
	}
	origin, err := strconv.ParseFloat(check.TextCode[0].X, 64)
	if err != nil {
		return 0, 0, err
	}
	return x - origin, y - baseline, nil
}

// copyRichTextMetadata 迁移选区及其组合、注解内部的编辑标引，复用标准内容中的字体引用
// 入参: ctx 取消上下文, source 来源编辑器, sourcePage 来源页, ids 正文标识, annotations 注解标识, page 目标页, copied 新正文标识, copiedNotes 新注解标识
// 返回: error 元数据损坏、结构不一致或提交错误
func (e *Editor) copyRichTextMetadata(ctx context.Context, source *Editor, sourcePage int, ids, annotations []string, page int, copied, copiedNotes []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := source.richTextMetadataIndex()
	if metadata.err != nil || len(metadata.entries) == 0 {
		return metadata.err
	}
	copyObject := func(before, after GraphicObject) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := source.captureRichTextSnapshot(sourcePage, before)
		if err != nil || snapshot == nil {
			return err
		}
		if err := e.putRichTextSnapshot(page, after, snapshot); err != nil {
			return err
		}
		return ctx.Err()
	}
	for i, id := range ids {
		before, err := source.Object(sourcePage, id)
		if err != nil {
			return err
		}
		after, err := e.Object(page, copied[i])
		if err != nil {
			return err
		}
		if err := copyObject(before, after); err != nil {
			return err
		}
	}
	for i, id := range annotations {
		before, err := source.Annotation(sourcePage, id)
		if err != nil {
			return err
		}
		after, err := e.Annotation(page, copiedNotes[i])
		if err != nil {
			return err
		}
		if err := copyObject(GraphicObject{Type: "CompositeGraphicUnit", CompositeGraphicUnit: CompositeGraphicUnit{Objects: before.Appearance.Objects}}, GraphicObject{Type: "CompositeGraphicUnit", CompositeGraphicUnit: CompositeGraphicUnit{Objects: after.Appearance.Objects}}); err != nil {
			return err
		}
	}
	return nil
}

// richTextColors 为自产RGB颜色指定文档内有效颜色空间，不改变默认颜色空间
// 入参: object 排版生成的标准图元
// 返回: error 颜色或资源错误
func (e *Editor) richTextColors(object *GraphicObject) error {
	var colors []*FillColor
	switch object.Type {
	case "TextObject":
		colors = []*FillColor{object.TextObject.FillColor, (*FillColor)(object.TextObject.StrokeColor)}
	case "PathObject":
		colors = []*FillColor{object.PathObject.FillColor, (*FillColor)(object.PathObject.StrokeColor)}
	default:
		return fmt.Errorf("rich text layout must emit standard text or path objects")
	}
	for _, paint := range colors {
		if paint == nil {
			continue
		}
		var r, g, b float64
		if _, err := fmt.Sscan(paint.Value, &r, &g, &b); err != nil {
			return err
		}
		rgb, err := e.RGBColor(color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
		if err != nil {
			return err
		}
		paint.ColorSpace = rgb.ColorSpace
	}
	return nil
}

// copiedRichTextMetadata 检查当前文档对象快照的标引，不将外部同号对象误认成本地对象
// 入参: objects 待复制快照
// 返回: map[int]*richTextSnapshot 需要复制的语义, error 损坏诊断
func (e *Editor) copiedRichTextMetadata(objects []GraphicObject) (map[int]*richTextSnapshot, error) {
	result := make(map[int]*richTextSnapshot)
	for i, object := range objects {
		if object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
			continue
		}
		if object.origin != nil && e.snapshotOrigin(object) == nil {
			continue
		}
		if object.richText != nil {
			result[i] = object.richText
			continue
		}
		index := e.richTextMetadataIndex()
		if index.err != nil {
			return nil, index.err
		}
		if len(index.entries) == 0 {
			continue
		}
		for page := range e.pages {
			snapshot, err := e.captureRichTextSnapshot(page, object)
			if err != nil {
				return nil, err
			}
			if snapshot != nil {
				result[i] = snapshot
				break
			}
		}
	}
	return result, nil
}

// attachRichTextSnapshot 为对象快照附加嵌套编辑语义，来源删除后仍可跨页粘贴
// 入参: page 页面索引, object 对象快照
// 返回: error 标引或资源错误
func (e *Editor) attachRichTextSnapshot(page int, object *GraphicObject) error {
	snapshot, err := e.captureRichTextSnapshot(page, *object)
	object.richText = snapshot
	return err
}

// captureRichTextSnapshot 捕获组合树内的富文本标引，不复制共享二进制资源
// 入参: page 页面索引, object 根对象
// 返回: *richTextSnapshot 不可变快照，无标引时为空, error 标引或资源错误
func (e *Editor) captureRichTextSnapshot(page int, object GraphicObject) (*richTextSnapshot, error) {
	if object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
		return nil, nil
	}
	index := e.richTextMetadataIndex()
	if index.err != nil || len(index.entries) == 0 {
		return nil, index.err
	}
	active := make(map[string]bool)
	var capture func(GraphicObject) (*richTextSnapshot, error)
	capture = func(object GraphicObject) (*richTextSnapshot, error) {
		if object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
			return nil, nil
		}
		entry, err := e.richTextEntry(page, editorObjectID(object))
		if err == nil {
			if _, err := e.richTextObject(page, object); err != nil {
				return nil, err
			}
			meta := cloneEditorData(entry.metadata)
			return &richTextSnapshot{metadata: &meta}, nil
		}
		if !errors.Is(err, ErrNotRichText) {
			return nil, err
		}
		unit := object.CompositeGraphicUnit
		if unit.ResourceID != "" {
			if active[unit.ResourceID] {
				return nil, fmt.Errorf("cyclic composite resource")
			}
			active[unit.ResourceID] = true
			defer delete(active, unit.ResourceID)
			definition, err := e.compositeDefinition(unit.ResourceID)
			if err != nil {
				return nil, err
			}
			unit = definition.object.CompositeGraphicUnit
		}
		var snapshot *richTextSnapshot
		i := 0
		for child := range unit.objects() {
			value, err := capture(child)
			if err != nil {
				return nil, err
			}
			if value != nil {
				if snapshot == nil {
					snapshot = &richTextSnapshot{children: make(map[int]*richTextSnapshot)}
				}
				snapshot.children[i] = value
			}
			i++
		}
		return snapshot, nil
	}
	return capture(object)
}

// putRichTextSnapshot 恢复复制后的嵌套标引，按目标标准内容更新摘要和字体映射
// 入参: page 目标页, object 复制后的对象, snapshot 来源不可变语义
// 返回: error 结构或标引错误
func (e *Editor) putRichTextSnapshot(page int, object GraphicObject, snapshot *richTextSnapshot) error {
	if snapshot.metadata != nil {
		index := -1
		if entry, err := e.richTextEntry(page, editorObjectID(object)); err == nil {
			index = entry.index
		} else if !errors.Is(err, ErrNotRichText) {
			return err
		}
		return e.putCompositeRichTextMetadata(page, object, *snapshot.metadata, index)
	}
	unit := object.CompositeGraphicUnit
	if unit.ResourceID != "" {
		definition, err := e.compositeDefinition(unit.ResourceID)
		if err != nil {
			return err
		}
		unit = definition.object.CompositeGraphicUnit
	}
	i, count := 0, 0
	for child := range unit.objects() {
		if value := snapshot.children[i]; value != nil {
			if err := e.putRichTextSnapshot(page, child, value); err != nil {
				return err
			}
			count++
		}
		i++
	}
	if count != len(snapshot.children) {
		return fmt.Errorf("copied composite structure differs")
	}
	return nil
}

// richTextTagsFontSafe 仅允许完整识别且不含资源编号的本库标引通过字体重绑定检查
// 入参: ctx 取消上下文, reader 包阅读器, index 文档索引
// 返回: bool 是否可以排除字体引用, error 读取或取消错误
func richTextTagsFontSafe(ctx context.Context, reader *Reader, index int) (bool, error) {
	view, err := reader.DocumentContext(ctx, index)
	if err != nil {
		return false, err
	}
	defer view.Close()
	tags, err := view.CustomTagsContext(ctx)
	if err != nil {
		return false, err
	}
	for _, tag := range tags {
		if tag.TypeID != richTextTagType || tag.NameSpace != richTextNamespace || tag.SchemaLoc != "" {
			return false, nil
		}
		structure, err := view.readStructureContext(ctx, tag)
		if err != nil {
			return false, err
		}
		root := structure.Root
		if root.Name != "RichText" || root.Namespace != richTextNamespace || len(root.Attributes) != 0 {
			return false, nil
		}
		if !richTextMetadataFontSafe(root.Text) {
			return false, nil
		}
		for _, child := range root.Children {
			if child.Name != "ObjectRef" || child.Namespace != richTextNamespace || len(child.Children) != 0 || len(child.Attributes) != 1 || child.Attributes[0].Namespace != "" || child.Attributes[0].Name != "PageRef" {
				return false, nil
			}
		}
	}
	return true, ctx.Err()
}

// richTextMetadataFontSafe 核对本库富文本元数据不包含字体资源编号或未知字段
// 入参: value 元数据JSON
// 返回: bool 是否可由标准图元完整统计字体引用
func richTextMetadataFontSafe(value string) bool {
	var meta richTextMetadata
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return false
	}
	if decoder.Decode(new(any)) != io.EOF || meta.Version != 1 {
		return false
	}
	for i := range meta.Value.Runs {
		if meta.Value.Runs[i].Format.Font != "" {
			return false
		}
		meta.Value.Runs[i].Format.Font = "1"
	}
	if err := meta.Value.Validate(); err != nil {
		return false
	}
	return true
}

// formatCompositeText 修改内部富文本格式，普通文字安全提升后保留原实例变换
// 入参: page 页面索引, path 父路径, index 成员序号, patch 格式增量, reflow 显式重排
// 返回: error 识别、排版或提交错误
func (e *Editor) formatCompositeText(page int, path ObjectPath, index int, patch TextFormatPatch, reflow bool) error {
	value, _, _, err := e.PrepareRichText(page, ObjectPath{ID: path.ID, Annotation: path.Annotation, Children: append(slices.Clone(path.Children), index)}, reflow)
	if err != nil {
		return err
	}
	if err := value.ApplyFormat(0, value.Len(), patch); err != nil {
		return err
	}
	return e.updateCompositeRichText(page, path, index, value, 0, 0, reflow)
}

// updateCompositePlainRichText 将可验证布局的内部文字提升为富文本并保留实例坐标
// 入参: page 页面索引, path 父路径, index 成员序号, value 富文本内容, offset 局部偏移, width 可选宽度, reflow 是否显式重排
// 返回: error 校验或提交错误
func (e *Editor) updateCompositePlainRichText(page int, path ObjectPath, index int, value RichText, offset, width float64, reflow bool) error {
	target := ObjectPath{ID: path.ID, Annotation: path.Annotation, Children: append(slices.Clone(path.Children), index)}
	_, frame, matrix, err := e.PrepareRichText(page, target, reflow)
	if err != nil {
		return err
	}
	matrix = matrix.Multiply(TranslationMatrix(offset, 0))
	if width > 0 {
		frame.W = width
	}
	var metadata richTextMetadata
	if err := e.editCompositeObjects(page, path, []int{index}, func(_ *Renderer, nodes []*editorCompositeNode, members []CompositeMember) error {
		node := nodes[0]
		if node.object.Type != "TextObject" || !editorXMLSupported(node.span) {
			return ErrNotRichText
		}
		inverse, ok := node.parent.Invert()
		if !ok {
			return fmt.Errorf("invalid composite parent transform")
		}
		next, meta, err := e.prepareRichText(Box{W: frame.W, H: frame.H}, value)
		if err != nil {
			return err
		}
		unit := richTextPlacement(meta, inverse.Multiply(matrix))
		unit.ID, unit.ResourceID = node.object.TextObject.ID, next.CompositeGraphicUnit.ResourceID
		next.CompositeGraphicUnit = unit
		if !node.boundaryInCTM {
			next = compositeBoundary(next, node.parent, false)
		}
		metadata = meta
		return node.update(next)
	}); err != nil {
		return err
	}
	member, err := e.CompositeObject(page, path, index)
	if err != nil {
		return err
	}
	return e.putCompositeRichTextMetadata(page, member.Object, metadata, -1)
}
