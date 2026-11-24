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
	"encoding/xml"
	"errors"
	"fmt"
	"image/color"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// richTextTagType 标识富文本编辑标引，正文使用标准复合资源和图元
const richTextTagType = "OFDGo.RichText"

// richTextNamespace 标识富文本编辑语义的命名空间，版本仅随不兼容的语义变更升级
const richTextNamespace = "https://ofdgo.aite.me/ns/rich-text/1"

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
	index     int
	aggregate bool
	metadata  richTextMetadata
	err       error
}

// textLayoutEntry 保存普通文字的标引入口与可恢复排版语义
type textLayoutEntry struct {
	index    int
	metadata textMetadata
}

// richTextIndex 缓存当前文档修订的标引，不创建页面预览快照
type richTextIndex struct {
	source        *editorSource
	revision      uint64
	remaps        map[string]int
	entries       map[string]richTextEntry
	layouts       map[string]textLayoutEntry
	objectLayouts map[string]textLayoutEntry
	ambiguousObjs map[string]bool
	err           error
}

// textLayoutGroup 聚合相同普通文字排版语义的页面对象引用
type textLayoutGroup struct {
	metadata string
	refs     []StructureReference
}

// isRichTextAggregateRoot 判断富文本标引根节点是否采用多条目聚合结构
// 入参: root 标引根节点
// 返回: bool 是否为聚合格式
func isRichTextAggregateRoot(root *StructureNode) bool {
	if root == nil || strings.TrimSpace(root.Text) != "" || len(root.Children) == 0 {
		return false
	}
	for _, child := range root.Children {
		if child.Name == "ObjectRef" && child.Namespace == richTextNamespace {
			return false
		}
	}
	return true
}

// parseRichTextMetadata 解码并校验单条富文本语义JSON
// 入参: text 原始JSON文本, meta 输出结构
// 返回: error 解码或字段范围错误
func parseRichTextMetadata(text string, meta *richTextMetadata) error {
	if err := json.Unmarshal([]byte(text), meta); err != nil {
		return err
	}
	if meta.Version != 1 || meta.Frame.X != 0 || meta.Frame.Y != 0 || !finite(meta.Frame.W) || !finite(meta.Frame.H) || meta.Frame.W <= 0 || meta.Frame.H <= 0 || len(meta.Digest) != 64 {
		return fmt.Errorf("invalid rich text metadata version or frame")
	}
	if meta.Extent.X != 0 || meta.Extent.Y != 0 || !finite(meta.Extent.W) || !finite(meta.Extent.H) || meta.Extent.W <= 0 || meta.Extent.H <= 0 || !finite(meta.Offset[0]) || !finite(meta.Offset[1]) || meta.Offset[0] < 0 || meta.Offset[1] < 0 || meta.Offset[0] > meta.Extent.W || meta.Offset[1] > meta.Extent.H || len(meta.Anchors) != len(meta.Value.Runs) {
		return fmt.Errorf("invalid rich text metadata extent or anchors")
	}
	return nil
}

// encodeRichTextEntryFragment 编码聚合富文本标引内的单条Entry片段
// 入参: pageID 页面标识, objectID 对象标识, payload 序列化后的元数据JSON
// 返回: []byte Entry XML片段, error 转义错误
func encodeRichTextEntryFragment(pageID, objectID string, payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(payload) + 96)
	buf.WriteString("<Entry>")
	if err := xml.EscapeText(&buf, payload); err != nil {
		return nil, err
	}
	buf.WriteString(`<ObjectRef PageRef="`)
	if err := xml.EscapeText(&buf, []byte(pageID)); err != nil {
		return nil, err
	}
	buf.WriteString(`">`)
	if err := xml.EscapeText(&buf, []byte(objectID)); err != nil {
		return nil, err
	}
	buf.WriteString("</ObjectRef></Entry>")
	return buf.Bytes(), nil
}

// richTextPlacement 根据局部逻辑坐标确定物理组合边界，负向装饰不会被资源裁剪
// 入参: meta 编辑框与资源尺寸, matrix 逻辑坐标到页面变换
// 返回: CompositeObject 组合几何
func richTextPlacement(meta richTextMetadata, matrix Matrix) CompositeObject {
	matrix = matrix.Multiply(TranslationMatrix(-meta.Offset[0], -meta.Offset[1]))
	box := matrix.TransformBox(meta.Extent)
	return CompositeObject{Boundary: editorBoxString(box), CTM: TranslationMatrix(-box.X, -box.Y).Multiply(matrix).String()}
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

// buildTextLayoutNode 构造独立命名空间下的普通文字自定义标引树
// 入参: group 聚合标引组
// 返回: StructureNode 标引根节点
func buildTextLayoutNode(group textLayoutGroup) StructureNode {
	children := make([]StructureNode, 0, len(group.refs))
	for _, ref := range group.refs {
		children = append(children, StructureNode{
			Name:       "ObjectRef",
			Namespace:  textNamespace,
			Attributes: []StructureAttribute{{Name: "PageRef", Value: ref.PageID}},
			Text:       strings.Join(ref.ObjectIDs, " "),
		})
	}
	return StructureNode{Name: "Text", Namespace: textNamespace, Text: group.metadata, Children: children}
}

// richTextTagsFontSafe 仅允许完整识别且不含资源编号的本库标引通过字体重绑定检查
// 入参: ctx 取消上下文, reader 包阅读器, index 文档索引
// 返回: bool 是否可以排除字体引用, error 读取或取消错误
func richTextTagsFontSafe(ctx context.Context, reader *Reader, index int) (bool, error) {
	view, err := editorDocumentViewContext(ctx, reader, index)
	if err != nil {
		return false, err
	}
	defer view.Close()
	tags, err := view.CustomTagsContext(ctx)
	if err != nil {
		return false, err
	}
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if tag.TypeID == textTagType && tag.NameSpace == textNamespace {
			if tag.SchemaLoc != "" {
				return false, nil
			}
			structure, err := view.readStructureContext(ctx, tag)
			if err != nil {
				return false, err
			}
			root := structure.Root
			if root.Name != "Text" || root.Namespace != textNamespace || len(root.Attributes) != 0 || !textMetadataFontSafe(root.Text) {
				return false, nil
			}
			for _, child := range root.Children {
				if child.Name != "ObjectRef" || child.Namespace != textNamespace || len(child.Children) != 0 || len(child.Attributes) != 1 || child.Attributes[0].Namespace != "" || child.Attributes[0].Name != "PageRef" {
					return false, nil
				}
			}
			continue
		}
		if tag.TypeID == richTextTagType && tag.NameSpace == richTextNamespace {
			if tag.SchemaLoc != "" {
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
			if isRichTextAggregateRoot(root) {
				for _, item := range root.Children {
					if item.Name != "Entry" || item.Namespace != richTextNamespace || len(item.Attributes) != 0 || !richTextMetadataFontSafe(item.Text) {
						return false, nil
					}
					for _, child := range item.Children {
						if child.Name != "ObjectRef" || child.Namespace != richTextNamespace || len(child.Children) != 0 || len(child.Attributes) != 1 || child.Attributes[0].Namespace != "" || child.Attributes[0].Name != "PageRef" {
							return false, nil
						}
					}
				}
				continue
			}
			if !richTextMetadataFontSafe(root.Text) {
				return false, nil
			}
			for _, child := range root.Children {
				if child.Name != "ObjectRef" || child.Namespace != richTextNamespace || len(child.Children) != 0 || len(child.Attributes) != 1 || child.Attributes[0].Namespace != "" || child.Attributes[0].Name != "PageRef" {
					return false, nil
				}
			}
			continue
		}
		if strings.TrimSpace(tag.FileLoc) == "" || !isKnownResourceSafeTagMeta(tag) {
			return false, nil
		}
		if tag.SchemaLoc != "" {
			schemaFile, err := view.openFile(view.ResPath(tag.SchemaLoc))
			if err != nil {
				return false, nil
			}
			_ = schemaFile.Close()
		}
		structure, err := view.readStructureContext(ctx, tag)
		if err != nil {
			return false, ctx.Err()
		}
		if !knownResourceSafeCustomTag(tag, structure.Root) {
			return false, nil
		}
	}
	return true, ctx.Err()
}

// textMetadataFontSafe 核对本库普通文字元数据不包含字体资源编号或未知字段
// 入参: value 元数据JSON
// 返回: bool 是否可由标准图元完整统计字体引用
func textMetadataFontSafe(value string) bool {
	var plain textMetadata
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plain); err != nil {
		return false
	}
	if decoder.Decode(new(any)) != io.EOF || len(plain.Digest) != 64 {
		return false
	}
	return validateRichLayout(plain.Layout) == nil && structureXMLText(plain.Text)
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
		defer e.bindPageIndex(page)()
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
		defer e.bindPageIndex(page)()
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
			if err := e.deleteRichTextMetadata(page, id, entry); err != nil {
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
	defer e.bindPageIndex(page)()
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
	defer e.bindPageIndex(page)()
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
	box, err := creationBox(object.CompositeObject.Boundary)
	if err != nil {
		return Box{}, Matrix{}, err
	}
	return entry.metadata.Frame, TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(object.CompositeObject.CTM)).Multiply(TranslationMatrix(entry.metadata.Offset[0], entry.metadata.Offset[1])), nil
}

// AddCompositeRichText 向内部范围添加富文本，使用页面坐标并保存编辑标引
// 入参: page 页面索引, path 父路径, box 页面编辑框, value 富文本
// 返回: int 新成员序号, error 排版或提交错误
func (e *Editor) AddCompositeRichText(page int, path ObjectPath, box Box, value RichText) (int, error) {
	var index int
	err := e.Transaction(func(e *Editor) error {
		if _, err := e.page(page); err != nil {
			return err
		}
		defer e.bindPageIndex(page)()
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
	defer e.bindPageIndex(page)()
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
	defer e.bindPageIndex(page)()
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

// updateRichTextFrame 统一提交正文或编辑框变化，width为零时沿用原宽度模式
// 入参: page 页面索引, id 对象标识, value 新内容, offset 局部偏移, width 可选宽度, reflow 是否显式重排原文
// 返回: error 校验或提交错误
func (e *Editor) updateRichTextFrame(page int, id string, value RichText, offset, width float64, reflow bool) error {
	return e.Transaction(func(e *Editor) error {
		defer e.bindPageIndex(page)()
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
		object.CompositeObject.ID = id
		placement := richTextPlacement(meta, matrix)
		object.CompositeObject.Boundary = placement.Boundary
		object.CompositeObject.CTM = placement.CTM
		if old.Type == "CompositeObject" {
			unit := old.CompositeObject
			if unit.Clips != nil {
				return fmt.Errorf("clipped rich text requires explicit clip removal before reflow")
			}
			unit.ResourceID = object.CompositeObject.ResourceID
			unit.Boundary = placement.Boundary
			unit.CTM = placement.CTM
			object.CompositeObject = unit
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
	unit, err := e.compositeDefinition(object.CompositeObject.ResourceID)
	if err != nil || unit.unit == nil {
		return RichText{}, fmt.Errorf("rich text resource: %w", err)
	}
	objects := unit.unit.Objects
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
		defer e.bindPageIndex(page)()
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
			if node.object.CompositeObject.Clips != nil {
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
			unit := &object.CompositeObject
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
			unit.ResourceID, unit.Boundary, unit.CTM = next.CompositeObject.ResourceID, placement.Boundary, placement.CTM
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
	unit, err := e.compositeDefinition(object.CompositeObject.ResourceID)
	if err != nil || unit.unit == nil {
		return err
	}
	meta.Digest = richTextDigest(unit.unit.Objects)
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
		if result.objects[i].Type == "TextObject" && e.source != nil && len(e.pageScope) > 0 {
			mapped, _, err := e.promoteScopedResource(result.objects[i].TextObject.Font, "Font", e.pageScope, nil, true, make(map[string]bool))
			if err != nil {
				return GraphicObject{}, richTextMetadata{}, err
			}
			result.objects[i].TextObject.Font = mapped
		}
	}
	resource, err := e.addOwnedVector(context.Background(), result.objects, extent.W, extent.H)
	if err != nil {
		return GraphicObject{}, richTextMetadata{}, err
	}
	unit, err := e.compositeDefinition(resource)
	if err != nil || unit.unit == nil {
		return GraphicObject{}, richTextMetadata{}, err
	}
	meta := richTextMetadata{Version: 1, Value: cloneEditorData(value), Frame: Box{W: box.W, H: box.H}, Extent: extent, Offset: [2]float64{padX, padY}, Anchors: result.anchors, Digest: richTextDigest(unit.unit.Objects)}
	for i := range meta.Value.Runs {
		meta.Value.Runs[i].Format.Font = ""
	}
	object := GraphicObject{Type: "CompositeObject", CompositeObject: richTextPlacement(meta, TranslationMatrix(box.X, box.Y))}
	object.CompositeObject.ResourceID = resource
	return object, meta, nil
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
	index := &richTextIndex{source: e.source, revision: e.revision, remaps: e.structureRemapStarts(), entries: make(map[string]richTextEntry), layouts: make(map[string]textLayoutEntry), objectLayouts: make(map[string]textLayoutEntry), ambiguousObjs: make(map[string]bool)}
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
		if tag.TypeID == textTagType && tag.NameSpace == textNamespace {
			structure, err := e.source.reader.readStructureContext(context.Background(), tag)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					index.err = err
					return index
				}
				continue
			}
			root := structure.Root
			var plain textMetadata
			if root.Name != "Text" || root.Namespace != textNamespace || json.Unmarshal([]byte(root.Text), &plain) != nil || len(plain.Digest) != 64 || validateRichLayout(plain.Layout) != nil || !structureXMLText(plain.Text) {
				continue
			}
			for _, child := range root.Children {
				for _, ref := range child.References {
					pageKey := editorResourceID(ref.PageID)
					if pageKey == "" {
						continue
					}
					for _, id := range ref.ObjectIDs {
						objKey := editorResourceID(id)
						if objKey == "" {
							continue
						}
						entry := textLayoutEntry{index: i, metadata: plain}
						index.layouts[pageKey+"/"+objKey] = entry
						if prev, exists := index.objectLayouts[objKey]; exists && prev.metadata != plain {
							delete(index.objectLayouts, objKey)
							index.ambiguousObjs[objKey] = true
						} else if !index.ambiguousObjs[objKey] {
							index.objectLayouts[objKey] = entry
						}
					}
				}
			}
			continue
		}
		if tag.TypeID != richTextTagType || tag.NameSpace != richTextNamespace {
			continue
		}
		structure, err := e.source.reader.readStructureContext(context.Background(), tag)
		if err != nil {
			index.err = fmt.Errorf("rich text metadata: %w", err)
			return index
		}
		root := structure.Root
		if root.Name != "RichText" || root.Namespace != richTextNamespace {
			entry := richTextEntry{index: i, err: fmt.Errorf("invalid rich text metadata root")}
			for _, child := range root.Children {
				for _, ref := range child.References {
					for _, id := range ref.ObjectIDs {
						key := editorResourceID(ref.PageID) + "/" + editorResourceID(id)
						index.entries[key] = entry
					}
				}
			}
			continue
		}
		if isRichTextAggregateRoot(root) {
			for _, item := range root.Children {
				entry := richTextEntry{index: i, aggregate: true}
				if item.Name != "Entry" || item.Namespace != richTextNamespace {
					entry.err = fmt.Errorf("invalid rich text metadata entry")
				} else {
					entry.err = parseRichTextMetadata(item.Text, &entry.metadata)
				}
				for _, child := range item.Children {
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
			continue
		}
		entry := richTextEntry{index: i}
		entry.err = parseRichTextMetadata(root.Text, &entry.metadata)
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
				if _, exists := index.layouts[page+"/"+to]; !exists {
					if entry, ok := index.layouts[page+"/"+from]; ok {
						index.layouts[page+"/"+to] = entry
						if !index.ambiguousObjs[to] {
							if prev, has := index.objectLayouts[to]; has && prev.metadata != entry.metadata {
								delete(index.objectLayouts, to)
								index.ambiguousObjs[to] = true
							} else {
								index.objectLayouts[to] = entry
							}
						}
					}
				}
			}
		}
	}
	return index
}

// putRichTextMetadata 添加或替换带有标准标引引用的富文本语义，新条目聚合到统一富文本标引文件
// 入参: page 页面索引, id 对象标识, meta 编辑信息, index 原入口或-1
// 返回: error 编码或提交错误
func (e *Editor) putRichTextMetadata(page int, id string, meta richTextMetadata, index int) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if index >= 0 {
		if handled, err := e.updateRichTextAggregateMetadata(page, id, data, index); err != nil || handled {
			return err
		}
		root := StructureNode{Name: "RichText", Namespace: richTextNamespace, Text: string(data)}
		separate := false
		err := e.editStructure(index, func(raw []byte, node *editorXML, tag CustomTag) ([]byte, error) {
			updated, err := rewriteStructureReferences(context.Background(), raw, tag, func(pageID string, ids []string) (string, []string, error) {
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
			return updateStructureNodeXML(raw, node, root)
		})
		if err != nil || !separate {
			return err
		}
	}
	return e.appendRichTextAggregateMetadata(page, id, data)
}

// findRichTextAggregateTag 查找并缓存当前文档的统一内部富文本聚合标引文件
// 入参: ctx 取消上下文, base 编辑包快照
// 返回: string 文件路径, int 标引序号, bool 是否存在, error 读取或取消错误
func (e *Editor) findRichTextAggregateTag(ctx context.Context, base *editorSource) (string, int, bool, error) {
	if base.richTextTagKnown {
		return base.richTextTagPath, base.richTextTagIndex, base.richTextTagPath != "", nil
	}
	tags, err := base.reader.CustomTagsContext(ctx)
	if err != nil {
		return "", -1, false, err
	}
	for i, tag := range tags {
		if tag.TypeID != richTextTagType || tag.NameSpace != richTextNamespace || tag.SchemaLoc != "" {
			continue
		}
		name := base.reader.ResPath(tag.FileLoc)
		raw, readErr := base.reader.readFileViewContext(ctx, name)
		if readErr != nil {
			continue
		}
		root, parseErr := parseEditorXMLContext(ctx, raw)
		if parseErr != nil || root.name.Local != "RichText" || root.name.Space != richTextNamespace {
			continue
		}
		isAggregate := false
		for _, child := range root.children {
			if child.name.Local == "Entry" && child.name.Space == richTextNamespace {
				isAggregate = true
				break
			}
		}
		if !isAggregate {
			continue
		}
		shared := false
		for j, other := range tags {
			if j != i && base.reader.ResPath(other.FileLoc) == name {
				shared = true
				break
			}
		}
		if shared {
			continue
		}
		base.richTextTagPath = name
		base.richTextTagIndex = i
		base.richTextTagKnown = true
		return name, i, true, nil
	}
	base.richTextTagPath = ""
	base.richTextTagIndex = -1
	base.richTextTagKnown = true
	return "", -1, false, nil
}

// appendRichTextAggregateMetadata 将单条富文本语义追加到统一内部富文本标引文件，避免碎片XML
// 入参: page 页面索引, id 对象标识, payload 元数据JSON
// 返回: error 编码或提交错误
func (e *Editor) appendRichTextAggregateMetadata(page int, id string, payload []byte) error {
	ctx := context.Background()
	base := e.source
	var err error
	if base == nil {
		base, err = e.importBaseContext(ctx)
		if err != nil {
			return err
		}
	}
	pageID := e.pages[page].ID
	fragment, err := encodeRichTextEntryFragment(pageID, id, payload)
	if err != nil {
		return err
	}
	objects := map[string]map[string]bool{editorResourceID(pageID): {editorResourceID(id): true}}
	tagPath, tagIndex, found, err := e.findRichTextAggregateTag(ctx, base)
	if err != nil {
		return err
	}
	if found {
		existing, err := base.reader.readFileViewContext(ctx, tagPath)
		if err != nil {
			return err
		}
		updated, ok := appendNamespacedXMLBeforeClosing(existing, "RichText", richTextNamespace, fragment)
		if !ok {
			root, parseErr := parseEditorXMLContext(ctx, existing)
			if parseErr != nil {
				return parseErr
			}
			if root.open == root.end {
				updated = editorPatchXML(existing, []editorXMLPatch{editorXMLContent(existing, root, fragment)})
			} else {
				updated = editorPatchXML(existing, []editorXMLPatch{{root.close, root.close, fragment}})
			}
		}
		if err := e.commitStructurePartsContext(ctx, base, map[string][]byte{tagPath: updated}, objects, tagIndex); err != nil {
			return err
		}
		e.source.richTextTagPath = tagPath
		e.source.richTextTagIndex = tagIndex
		e.source.richTextTagKnown = true
		return nil
	}
	var docBuf bytes.Buffer
	docBuf.Grow(len(xml.Header) + len(richTextNamespace) + len(fragment) + 48)
	docBuf.WriteString(xml.Header)
	docBuf.WriteString(`<RichText xmlns="`)
	docBuf.WriteString(richTextNamespace)
	docBuf.WriteString(`">`)
	docBuf.Write(fragment)
	docBuf.WriteString("</RichText>")
	tagData := docBuf.Bytes()
	filePath := e.packageName("Tags/RichText.xml")
	attrs := ofdAttrs{}
	attrs.add("TypeID", richTextTagType)
	attrs.add("NameSpace", richTextNamespace)
	if base.document.CustomTags.Path != "" && base.customTagCountReady {
		indexName := base.reader.ResPath(base.document.CustomTags.Path)
		if existing, readErr := base.reader.readFileViewContext(ctx, indexName); readErr == nil {
			entry, encErr := editorXMLContainer("CustomTag", attrs, editorXMLText("FileLoc", "/"+filePath))
			if encErr == nil {
				entry = bytes.TrimPrefix(entry, []byte(xml.Header))
				if patched, ok := appendOFDXMLBeforeClosing(existing, "CustomTags", entry); ok {
					idx := base.customTagCount
					parts := map[string][]byte{filePath: tagData, indexName: patched}
					if err := e.commitStructurePartsContext(ctx, base, parts, objects, idx); err != nil {
						return err
					}
					e.source.customTagCount = idx + 1
					e.source.customTagCountReady = true
					e.source.richTextTagPath = filePath
					e.source.richTextTagIndex = idx
					e.source.richTextTagKnown = true
					return nil
				}
			}
		}
	}
	name, indexData, container, entries, err := editorStructureIndexContext(ctx, base, nil)
	if err != nil {
		return err
	}
	entry, err := container.containerXML("CustomTag", attrs, container.textXML("FileLoc", "/"+filePath))
	if err != nil {
		return err
	}
	parts := map[string][]byte{filePath: tagData}
	if container.open != container.end {
		entry = append(bytes.Clone(indexData[container.open:container.close]), entry...)
	}
	parts[name] = editorPatchXML(indexData, []editorXMLPatch{editorXMLContent(indexData, container, entry)})
	if base.document.CustomTags.Path == "" && name != base.reader.documentRoot {
		docName := base.reader.documentRoot
		docData, err := base.reader.readFileViewContext(ctx, docName)
		if err != nil {
			return err
		}
		doc, err := parseEditorXMLContext(ctx, docData)
		if err != nil {
			return err
		}
		parts[docName], err = editorXMLSetText(docData, doc, [][2]string{{"CustomTags", "/" + name}})
		if err != nil {
			return err
		}
	}
	idx := len(entries)
	if err := e.commitStructurePartsContext(ctx, base, parts, objects, idx); err != nil {
		return err
	}
	e.source.customTagCount = idx + 1
	e.source.customTagCountReady = true
	e.source.richTextTagPath = filePath
	e.source.richTextTagIndex = idx
	e.source.richTextTagKnown = true
	return nil
}

// updateRichTextAggregateMetadata 更新聚合富文本标引文件中的指定条目，共享引用时自动拆出独立条目
// 入参: page 页面索引, id 对象标识, payload 新元数据JSON, index 标引入口序号
// 返回: bool 是否由聚合格式处理, error 读取或提交错误
func (e *Editor) updateRichTextAggregateMetadata(page int, id string, payload []byte, index int) (bool, error) {
	if e.source == nil {
		return false, nil
	}
	ctx := context.Background()
	tags, err := e.source.reader.CustomTagsContext(ctx)
	if err != nil || index < 0 || index >= len(tags) {
		return false, err
	}
	tag := tags[index]
	name := e.source.reader.ResPath(tag.FileLoc)
	raw, err := e.source.reader.readFileViewContext(ctx, name)
	if err != nil {
		return false, err
	}
	root, err := parseEditorXMLContext(ctx, raw)
	if err != nil {
		return false, err
	}
	hasEntry := false
	for _, child := range root.children {
		if child.name.Local == "Entry" && child.name.Space == richTextNamespace {
			hasEntry = true
			break
		}
	}
	if !hasEntry {
		return false, nil
	}
	pageID := e.pages[page].ID
	targetPage, targetObj := editorResourceID(pageID), editorResourceID(id)
	fragment, err := encodeRichTextEntryFragment(pageID, id, payload)
	if err != nil {
		return true, err
	}
	var patches []editorXMLPatch
	matched := false
	separated := false
	for _, entryNode := range root.children {
		if entryNode.name.Local != "Entry" || entryNode.name.Space != richTextNamespace {
			continue
		}
		var refPatches []editorXMLPatch
		hasTarget, hasOther := false, false
		for _, child := range entryNode.children {
			if child.name.Local != "ObjectRef" || child.name.Space != richTextNamespace {
				continue
			}
			refPage := child.attr("PageRef")
			ids := structureObjectIDs(editorImportText(raw, child))
			var kept []string
			changed := false
			for _, objID := range ids {
				if editorResourceID(refPage) == targetPage && editorResourceID(objID) == targetObj {
					hasTarget = true
					changed = true
				} else {
					hasOther = true
					kept = append(kept, objID)
				}
			}
			if changed {
				if len(kept) == 0 {
					refPatches = append(refPatches, editorXMLPatch{start: child.start, end: child.end})
				} else {
					refPatches = append(refPatches, editorXMLContent(raw, child, []byte(strings.Join(kept, ","))))
				}
			}
		}
		if !hasTarget {
			continue
		}
		matched = true
		if !hasOther {
			patches = append(patches, editorXMLPatch{start: entryNode.start, end: entryNode.end, data: fragment})
		} else {
			patches = append(patches, refPatches...)
			separated = true
		}
	}
	if !matched || separated {
		patches = append(patches, editorXMLPatch{start: root.close, end: root.close, data: fragment})
	}
	updated := editorPatchXML(raw, patches)
	parts := make(map[string][]byte)
	for i, other := range tags {
		if i != index && e.source.reader.ResPath(other.FileLoc) == name {
			indexName, indexData, _, entries, idxErr := editorStructureIndexContext(ctx, e.source, nil)
			if idxErr != nil {
				return true, idxErr
			}
			name = e.packageName("Tags/RichText.xml")
			loc := entries[index].child("FileLoc")
			if loc == nil {
				return true, fmt.Errorf("custom tag requires FileLoc")
			}
			var value bytes.Buffer
			_ = xml.EscapeText(&value, []byte("/"+name))
			parts[indexName] = editorPatchXML(indexData, []editorXMLPatch{editorXMLContent(indexData, loc, value.Bytes())})
			break
		}
	}
	parts[name] = updated
	objects := map[string]map[string]bool{targetPage: {targetObj: true}}
	if err := e.commitStructurePartsContext(ctx, e.source, parts, objects, index); err != nil {
		return true, err
	}
	e.source.richTextTagPath = name
	e.source.richTextTagIndex = index
	e.source.richTextTagKnown = true
	return true, nil
}

// deleteRichTextMetadata 删除单个富文本标引条目，聚合文件仅剔除目标条目并在清空时移除入口
// 入参: page 页面索引, id 对象标识, entry 目标富文本标引记录
// 返回: error 删除或提交错误
func (e *Editor) deleteRichTextMetadata(page int, id string, entry richTextEntry) error {
	if !entry.aggregate {
		return e.DeleteStructure(entry.index)
	}
	ctx := context.Background()
	tags, err := e.source.reader.CustomTagsContext(ctx)
	if err != nil || entry.index < 0 || entry.index >= len(tags) {
		return err
	}
	tag := tags[entry.index]
	name := e.source.reader.ResPath(tag.FileLoc)
	raw, err := e.source.reader.readFileViewContext(ctx, name)
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, raw)
	if err != nil {
		return err
	}
	targetPage, targetObj := editorResourceID(e.pages[page].ID), editorResourceID(id)
	var patches []editorXMLPatch
	remainingEntries := 0
	for _, entryNode := range root.children {
		if entryNode.name.Local != "Entry" || entryNode.name.Space != richTextNamespace {
			continue
		}
		var refPatches []editorXMLPatch
		hasTarget, hasOther := false, false
		for _, child := range entryNode.children {
			if child.name.Local != "ObjectRef" || child.name.Space != richTextNamespace {
				continue
			}
			refPage := child.attr("PageRef")
			ids := structureObjectIDs(editorImportText(raw, child))
			var kept []string
			changed := false
			for _, objID := range ids {
				if editorResourceID(refPage) == targetPage && editorResourceID(objID) == targetObj {
					hasTarget = true
					changed = true
				} else {
					hasOther = true
					kept = append(kept, objID)
				}
			}
			if changed {
				if len(kept) == 0 {
					refPatches = append(refPatches, editorXMLPatch{start: child.start, end: child.end})
				} else {
					refPatches = append(refPatches, editorXMLContent(raw, child, []byte(strings.Join(kept, ","))))
				}
			}
		}
		if !hasTarget {
			remainingEntries++
			continue
		}
		if !hasOther {
			patches = append(patches, editorXMLPatch{start: entryNode.start, end: entryNode.end})
		} else {
			patches = append(patches, refPatches...)
			remainingEntries++
		}
	}
	if remainingEntries == 0 {
		return e.DeleteStructure(entry.index)
	}
	if len(patches) == 0 {
		return nil
	}
	updated := editorPatchXML(raw, patches)
	parts := make(map[string][]byte)
	for i, other := range tags {
		if i != entry.index && e.source.reader.ResPath(other.FileLoc) == name {
			indexName, indexData, _, entries, idxErr := editorStructureIndexContext(ctx, e.source, nil)
			if idxErr != nil {
				return idxErr
			}
			name = e.packageName("Tags/RichText.xml")
			loc := entries[entry.index].child("FileLoc")
			if loc == nil {
				return fmt.Errorf("custom tag requires FileLoc")
			}
			var value bytes.Buffer
			_ = xml.EscapeText(&value, []byte("/"+name))
			parts[indexName] = editorPatchXML(indexData, []editorXMLPatch{editorXMLContent(indexData, loc, value.Bytes())})
			break
		}
	}
	parts[name] = updated
	if err := e.commitStructurePartsContext(ctx, e.source, parts, nil, entry.index); err != nil {
		return err
	}
	e.source.richTextTagPath = name
	e.source.richTextTagIndex = entry.index
	e.source.richTextTagKnown = true
	return nil
}

// lookupTextLayout 按页面标识、图元标识及定位摘要匹配普通文字排版语义
// 入参: pageID 页面标识，为空时按无歧义图元标识匹配, objectID 图元标识, digest 文字定位摘要
// 返回: *textLayout 独立排版语义，无匹配或摘要变化时返回nil
func (e *Editor) lookupTextLayout(pageID, objectID, digest string) *textLayout {
	if e == nil || digest == "" {
		return nil
	}
	objKey := editorResourceID(objectID)
	if objKey == "" {
		return nil
	}
	index := e.richTextMetadataIndex()
	if index == nil || len(index.layouts) == 0 {
		return nil
	}
	pageKey := editorResourceID(pageID)
	if pageKey != "" {
		if entry, ok := index.layouts[pageKey+"/"+objKey]; ok && entry.metadata.Digest == digest {
			meta := entry.metadata
			return &textLayout{value: meta.Text, options: meta.Layout, autoWidth: meta.AutoWidth}
		}
	}
	if entry, ok := index.objectLayouts[objKey]; ok && entry.metadata.Digest == digest {
		meta := entry.metadata
		return &textLayout{value: meta.Text, options: meta.Layout, autoWidth: meta.AutoWidth}
	}
	if pageKey == "" && index.ambiguousObjs[objKey] {
		suffix := "/" + objKey
		var matched textMetadata
		found := false
		for key, entry := range index.layouts {
			if strings.HasSuffix(key, suffix) && entry.metadata.Digest == digest {
				if found && matched != entry.metadata {
					return nil
				}
				matched, found = entry.metadata, true
			}
		}
		if found {
			return &textLayout{value: matched.Text, options: matched.Layout, autoWidth: matched.AutoWidth}
		}
	}
	return nil
}

// textLayoutOrigin 计算复合容器内已知排版文字的局部基线起点，字体暂不可用时回退首段定位坐标
// 入参: text 文字图元
// 返回: [2]float64 基线起点坐标, error 坐标或排版校验错误
func (e *Editor) textLayoutOrigin(text TextObject) ([2]float64, error) {
	if len(text.TextCode) == 0 {
		return [2]float64{}, fmt.Errorf("text codes are empty")
	}
	y, err := strconv.ParseFloat(text.TextCode[0].Y, 64)
	if err != nil || !finite(y) {
		return [2]float64{}, fmt.Errorf("invalid text baseline")
	}
	dx, _, offsetErr := e.richPlainOffset(text)
	if offsetErr == nil {
		return [2]float64{dx, y}, nil
	}
	if editReason(offsetErr) != EditFontUnavailable {
		return [2]float64{}, offsetErr
	}
	x, xErr := strconv.ParseFloat(text.TextCode[0].X, 64)
	if xErr != nil || !finite(x) {
		return [2]float64{}, offsetErr
	}
	return [2]float64{x, y}, nil
}

// restoreCompositeNodeTextLayout 在展开复合或注解节点时按需恢复文字排版语义与会话状态
// 入参: n 复合成员节点
func (e *Editor) restoreCompositeNodeTextLayout(n *editorCompositeNode) {
	if e == nil || n == nil || n.object.Type != "TextObject" {
		return
	}
	id := editorObjectID(n.object)
	if id == "" {
		return
	}
	if state, ok := n.states[id]; ok && state.layout != nil {
		n.object.TextObject.layout = state.layout
		return
	}
	if state, ok := resourceValue(n.states, id); ok && state.layout != nil {
		n.object.TextObject.layout = state.layout
		n.states[id] = state
		return
	}
	layout := n.object.TextObject.layout
	if layout == nil {
		layout = e.lookupTextLayout(n.pageID, id, n.object.TextObject.textDigest())
	}
	if layout == nil {
		return
	}
	n.object.TextObject.layout = layout
	if n.states == nil {
		n.states = make(map[string]editorCompositeState)
	}
	state := n.states[id]
	state.layout = layout
	if origin, err := e.textLayoutOrigin(n.object.TextObject); err == nil {
		state.origin = origin
	}
	n.states[id] = state
}

// collectCompositeStates 汇总复合对象及其引用的会话资源内部的全部成员编辑状态
// 入参: obj 复合对象
// 返回: map[string]editorCompositeState 合并后的成员状态
func (e *Editor) collectCompositeStates(obj CompositeObject) map[string]editorCompositeState {
	var result map[string]editorCompositeState
	visiting := make(map[string]bool)
	var visitComposite func(CompositeObject)
	var visitUnit func(CompositeGraphicUnit)
	visitComposite = func(cur CompositeObject) {
		if len(cur.states) != 0 {
			if result == nil {
				result = make(map[string]editorCompositeState)
			}
			maps.Copy(result, cur.states)
		}
		if cur.ResourceID != "" {
			key := editorResourceID(cur.ResourceID)
			if key == "" {
				key = cur.ResourceID
			}
			if !visiting[key] {
				visiting[key] = true
				for _, resource := range e.resources {
					if sameResourceID(resource.composite, cur.ResourceID) && len(resource.states) != 0 {
						if result == nil {
							result = make(map[string]editorCompositeState)
						}
						maps.Copy(result, resource.states)
						break
					}
				}
				if def, err := e.compositeDefinition(cur.ResourceID); err == nil && def.unit != nil {
					visitUnit(*def.unit)
				}
				delete(visiting, key)
			}
		}
	}
	visitUnit = func(unit CompositeGraphicUnit) {
		for child := range unit.objects() {
			if child.Type == "CompositeObject" {
				visitComposite(child.CompositeObject)
			}
		}
	}
	visitComposite(obj)
	return result
}

// restorePageTextLayouts 从普通文字自定义标引恢复页面内顶层、内联复合及外部复合资源的文字排版元数据
// 入参: page 已解析页面内容
func (e *Editor) restorePageTextLayouts(page *PageContent) {
	if e == nil || page == nil || page.ID == "" {
		return
	}
	index := e.richTextMetadataIndex()
	if index == nil || len(index.layouts) == 0 {
		return
	}
	for i := range page.Content.Layer {
		for j := range page.Content.Layer[i].Objects {
			e.restoreObjectTextLayouts(page.ID, &page.Content.Layer[i].Objects[j], nil)
		}
	}
}

// restoreObjectTextLayouts 递归恢复单个图元及嵌套复合资源的文字排版元数据
// 入参: pageID 页面标识, object 目标图元, reverseIDs 解组编号反向映射，可为nil
func (e *Editor) restoreObjectTextLayouts(pageID string, object *GraphicObject, reverseIDs map[string]string) {
	lookup := func(id, digest string) *textLayout {
		if layout := e.lookupTextLayout(pageID, id, digest); layout != nil {
			return layout
		}
		cur := editorResourceID(id)
		seen := make(map[string]bool)
		for cur != "" && !seen[cur] {
			seen[cur] = true
			prev := reverseIDs[cur]
			if prev == "" {
				break
			}
			if layout := e.lookupTextLayout(pageID, prev, digest); layout != nil {
				return layout
			}
			cur = prev
		}
		return nil
	}
	switch object.Type {
	case "TextObject":
		if object.TextObject.layout == nil {
			object.TextObject.layout = lookup(object.TextObject.ID, object.TextObject.textDigest())
		}
	case "CompositeObject":
		states := maps.Clone(object.CompositeObject.states)
		recordText := func(text *TextObject) {
			if text.ID == "" {
				return
			}
			if st, ok := resourceValue(states, text.ID); ok && st.layout != nil {
				text.layout = st.layout
				return
			}
			layout := text.layout
			if layout == nil {
				layout = lookup(text.ID, text.textDigest())
			}
			if layout == nil {
				return
			}
			text.layout = layout
			if states == nil {
				states = make(map[string]editorCompositeState)
			}
			st := states[text.ID]
			st.layout = layout
			if origin, err := e.textLayoutOrigin(*text); err == nil {
				st.origin = origin
			}
			states[text.ID] = st
		}
		visiting := make(map[string]bool)
		var visitObject func(*CompositeObject)
		var visitUnit func(*CompositeGraphicUnit)
		visitObject = func(comp *CompositeObject) {
			if comp.ResourceID == "" {
				return
			}
			resKey := editorResourceID(comp.ResourceID)
			if resKey == "" {
				resKey = comp.ResourceID
			}
			if visiting[resKey] {
				return
			}
			visiting[resKey] = true
			for _, res := range e.resources {
				if sameResourceID(res.composite, comp.ResourceID) && len(res.states) != 0 {
					if states == nil {
						states = make(map[string]editorCompositeState)
					}
					for k, v := range res.states {
						if _, exists := states[k]; !exists {
							states[k] = v
						}
					}
					break
				}
			}
			if def, err := e.compositeDefinition(comp.ResourceID); err == nil && def.unit != nil {
				visitUnit(def.unit)
			}
			delete(visiting, resKey)
		}
		visitUnit = func(unit *CompositeGraphicUnit) {
			for i := range unit.Objects {
				switch unit.Objects[i].Type {
				case "TextObject":
					recordText(&unit.Objects[i].TextObject)
				case "CompositeObject":
					visitObject(&unit.Objects[i].CompositeObject)
				}
			}
			for i := range unit.TextObject {
				recordText(&unit.TextObject[i])
			}
			for i := range unit.CompositeObject {
				visitObject(&unit.CompositeObject[i])
			}
		}
		visitObject(&object.CompositeObject)
		if len(states) != 0 {
			object.CompositeObject.states = states
		}
	}
}

// restoreUngroupedLayouts 恢复解组展开后的顶层文字及嵌套复合对象的排版语义
// 入参: pageID 页面标识, objects 展开后的图元切片, remaps 解组编号迁移记录
func (e *Editor) restoreUngroupedLayouts(pageID string, objects []GraphicObject, remaps []map[string]string) {
	if e == nil || len(objects) == 0 {
		return
	}
	var reverse map[string]string
	for _, m := range remaps {
		for from, to := range m {
			fromKey, toKey := editorResourceID(from), editorResourceID(to)
			if fromKey != "" && toKey != "" && fromKey != toKey {
				if reverse == nil {
					reverse = make(map[string]string)
				}
				reverse[toKey] = fromKey
			}
		}
	}
	for i := range objects {
		e.restoreObjectTextLayouts(pageID, &objects[i], reverse)
	}
}

// collectTextLayoutGroups 按编码内容聚合页面图层、复合单元资源、模板页及注释外观中普通文字的排版语义引用
// 入参: ctx 取消上下文, pages 页面列表, collectPages 仅收集指定页面标识集合，nil表示全部页面
// 返回: []textLayoutGroup 聚合标引组, error 编码或取消错误
func (e *Editor) collectTextLayoutGroups(ctx context.Context, pages []PageContent, collectPages map[string]bool) ([]textLayoutGroup, error) {
	var groups []textLayoutGroup
	groupIndex := make(map[string]int)
	seenRef := make(map[string]bool)
	recordText := func(pageID string, obj TextObject, layout *textLayout) error {
		if obj.ID == "" || layout == nil {
			return nil
		}
		pageKey, objKey := editorResourceID(pageID), editorResourceID(obj.ID)
		if pageKey == "" || objKey == "" || seenRef[pageKey+"/"+objKey] {
			return nil
		}
		obj.layout = layout
		meta, err := obj.textMetadata()
		if err != nil || meta == "" {
			return err
		}
		seenRef[pageKey+"/"+objKey] = true
		idx, ok := groupIndex[meta]
		if !ok {
			idx = len(groups)
			groupIndex[meta] = idx
			groups = append(groups, textLayoutGroup{metadata: meta})
		}
		refs := &groups[idx].refs
		if len(*refs) > 0 && (*refs)[len(*refs)-1].PageID == pageID {
			(*refs)[len(*refs)-1].ObjectIDs = append((*refs)[len(*refs)-1].ObjectIDs, obj.ID)
		} else {
			*refs = append(*refs, StructureReference{PageID: pageID, ObjectIDs: []string{obj.ID}})
		}
		return nil
	}
	var visitComposite func(string, string, CompositeObject, map[string]editorCompositeState, map[string]bool) error
	var visitUnit func(string, string, CompositeGraphicUnit, map[string]editorCompositeState, map[string]bool) error
	visitComposite = func(pageID, altPageID string, comp CompositeObject, inherited map[string]editorCompositeState, visiting map[string]bool) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		states := inherited
		if len(comp.states) != 0 {
			states = maps.Clone(inherited)
			if states == nil {
				states = make(map[string]editorCompositeState, len(comp.states))
			}
			maps.Copy(states, comp.states)
		}
		if comp.ResourceID != "" && e != nil {
			resKey := editorResourceID(comp.ResourceID)
			if resKey == "" {
				resKey = comp.ResourceID
			}
			if !visiting[resKey] {
				visiting[resKey] = true
				resStates := states
				for _, res := range e.resources {
					if sameResourceID(res.composite, comp.ResourceID) && len(res.states) != 0 {
						resStates = maps.Clone(states)
						if resStates == nil {
							resStates = make(map[string]editorCompositeState, len(res.states))
						}
						maps.Copy(resStates, res.states)
						break
					}
				}
				if def, err := e.compositeDefinition(comp.ResourceID); err == nil && def.unit != nil {
					if err := visitUnit(pageID, altPageID, *def.unit, resStates, visiting); err != nil {
						delete(visiting, resKey)
						return err
					}
				}
				delete(visiting, resKey)
			}
		}
		return nil
	}
	visitUnit = func(pageID, altPageID string, unit CompositeGraphicUnit, states map[string]editorCompositeState, visiting map[string]bool) error {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for child := range unit.objects() {
			switch child.Type {
			case "TextObject":
				layout := child.TextObject.layout
				if layout == nil {
					if st, ok := resourceValue(states, child.TextObject.ID); ok && st.layout != nil {
						layout = st.layout
					}
				}
				if layout == nil && e != nil {
					digest := child.TextObject.textDigest()
					layout = e.lookupTextLayout(pageID, child.TextObject.ID, digest)
					if layout == nil && altPageID != "" {
						layout = e.lookupTextLayout(altPageID, child.TextObject.ID, digest)
					}
				}
				if err := recordText(pageID, child.TextObject, layout); err != nil {
					return err
				}
			case "CompositeObject":
				if err := visitComposite(pageID, altPageID, child.CompositeObject, states, visiting); err != nil {
					return err
				}
			}
		}
		return nil
	}
	visitObjects := func(pageID, altPageID string, objects []GraphicObject) error {
		for i := range objects {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			switch objects[i].Type {
			case "TextObject":
				layout := objects[i].TextObject.layout
				if layout == nil && objects[i].state.layout != nil {
					layout = objects[i].state.layout
				}
				if layout == nil && e != nil {
					digest := objects[i].TextObject.textDigest()
					layout = e.lookupTextLayout(pageID, objects[i].TextObject.ID, digest)
					if layout == nil && altPageID != "" {
						layout = e.lookupTextLayout(altPageID, objects[i].TextObject.ID, digest)
					}
				}
				if err := recordText(pageID, objects[i].TextObject, layout); err != nil {
					return err
				}
			case "CompositeObject":
				if err := visitComposite(pageID, altPageID, objects[i].CompositeObject, objects[i].CompositeObject.states, make(map[string]bool)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var annotReader *Reader
	var cachedTemplates map[string]*PageContent
	for _, page := range pages {
		if page.ID == "" {
			continue
		}
		pageKey := editorResourceID(page.ID)
		if collectPages != nil && !collectPages[pageKey] {
			continue
		}
		for _, layer := range page.Content.Layer {
			if err := visitObjects(page.ID, "", layer.Objects); err != nil {
				return nil, err
			}
		}
		if e != nil && e.source != nil && len(page.Template) != 0 {
			for _, tplRef := range page.Template {
				tplKey := editorResourceID(tplRef.TemplateID)
				if tplKey == "" {
					continue
				}
				var tplContent *PageContent
				if cachedTemplates != nil {
					tplContent = cachedTemplates[tplKey]
				}
				var tplID string
				for _, def := range e.source.document.CommonData.TemplatePage {
					if editorResourceID(def.ID) == tplKey {
						tplID = def.ID
						if tplContent == nil {
							readCtx := ctx
							if readCtx == nil {
								readCtx = context.Background()
							}
							if loaded, err := e.source.reader.PageContentContext(readCtx, Page{ID: def.ID, BaseLoc: def.BaseLoc}); err == nil {
								tplContent = loaded
								if cachedTemplates == nil {
									cachedTemplates = make(map[string]*PageContent)
								}
								cachedTemplates[tplKey] = tplContent
							}
						}
						break
					}
				}
				if tplContent != nil {
					for _, layer := range tplContent.Content.Layer {
						if err := visitObjects(page.ID, tplID, layer.Objects); err != nil {
							return nil, err
						}
					}
				}
			}
		}
		if e != nil && e.source != nil && (len(e.source.reader.annots[page.ID]) != 0 || len(e.source.reader.pageAnnotFiles) != 0 || e.source.document.Annotations != "") {
			annots := e.source.reader.annots[page.ID]
			if !e.source.reader.pageAnnotsLoaded[page.ID] {
				if annotReader == nil {
					annotReader = cloneEditorReader(e.source.reader)
				}
				readCtx := ctx
				if readCtx == nil {
					readCtx = context.Background()
				}
				annots, _ = annotReader.pageAnnotationsContext(readCtx, page.ID)
			}
			for _, annot := range annots {
				if annot.Appearance == nil {
					continue
				}
				annotStates := e.source.annotationStates[page.ID+"/"+editorResourceID(annot.ID)]
				if annotStates == nil {
					annotStates = e.source.annotationStates[page.ID+"/"+annot.ID]
				}
				unit := CompositeGraphicUnit{
					Objects:         annot.Appearance.Objects,
					TextObject:      annot.Appearance.TextObject,
					PathObject:      annot.Appearance.PathObject,
					ImageObject:     annot.Appearance.ImageObject,
					CompositeObject: annot.Appearance.CompositeObject,
				}
				if err := visitUnit(page.ID, "", unit, annotStates, make(map[string]bool)); err != nil {
					return nil, err
				}
			}
		}
	}
	return groups, nil
}

// syncSourceTextMetadata 将已加载页面与已修改注释的普通文字排版语义同步写入输出条目，并返回淘汰的旧标引路径
// 入参: ctx 取消上下文, parts 本次输出条目映射
// 返回: map[string]bool 已淘汰的原包旧标引路径, error 读取、编码或解析错误
func (e *Editor) syncSourceTextMetadata(ctx context.Context, parts map[string][]byte) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.source == nil {
		return nil, nil
	}
	activePages := make(map[string]bool, len(e.pages))
	loadedPages := make(map[string]bool, len(e.pages)+len(e.removedPages))
	for _, page := range e.pages {
		pageKey := editorResourceID(page.ID)
		activePages[pageKey] = true
		src := e.source.pages[page.ID]
		if src == nil || src.original != nil {
			loadedPages[pageKey] = true
		}
	}
	for pageID := range e.source.pages {
		if key := editorResourceID(pageID); key != "" && !activePages[key] {
			loadedPages[key] = true
		}
	}
	for pageID, removed := range e.removedPages {
		if removed {
			if key := editorResourceID(pageID); key != "" {
				loadedPages[key] = true
			}
		}
	}
	collectPages := maps.Clone(loadedPages)
	annotChangedPages := make(map[string]bool)
	for pageID := range e.source.annotationPages {
		if key := editorResourceID(pageID); key != "" && activePages[key] {
			collectPages[key] = true
			annotChangedPages[key] = true
		}
	}
	for stateKey := range e.source.annotationStates {
		if slash := strings.IndexByte(stateKey, '/'); slash > 0 {
			if key := editorResourceID(stateKey[:slash]); key != "" && activePages[key] {
				collectPages[key] = true
				annotChangedPages[key] = true
			}
		}
	}
	groups, err := e.collectTextLayoutGroups(ctx, e.pages, collectPages)
	if err != nil {
		return nil, err
	}
	collectedObjects := make(map[string]map[string]bool)
	for _, group := range groups {
		for _, ref := range group.refs {
			pKey := editorResourceID(ref.PageID)
			if collectedObjects[pKey] == nil {
				collectedObjects[pKey] = make(map[string]bool)
			}
			for _, id := range ref.ObjectIDs {
				collectedObjects[pKey][editorResourceID(id)] = true
			}
		}
	}
	existing := e.richTextMetadataIndex()
	hasExistingTouched := false
	if existing != nil {
		for key := range existing.layouts {
			if slash := strings.IndexByte(key, '/'); slash > 0 {
				pKey := key[:slash]
				if loadedPages[pKey] || annotChangedPages[pKey] {
					hasExistingTouched = true
					break
				}
			}
		}
	}
	if len(groups) == 0 && !hasExistingTouched {
		return nil, nil
	}
	indexName, indexData, container, entries, err := editorStructureIndexContext(ctx, e.source, parts)
	if err != nil {
		return nil, err
	}
	pathCounts := make(map[string]int, len(entries))
	for _, entry := range entries {
		if locNode := entry.child("FileLoc"); locNode != nil {
			if locText := strings.TrimSpace(editorImportText(indexData, locNode)); locText != "" {
				pathCounts[cleanPackagePath(resolveResourcePath(indexName, "", locText))]++
			}
		}
	}
	var patches []editorXMLPatch
	var retired map[string]bool
	baseDir := indexName
	for _, entry := range entries {
		if entry.attr("TypeID") != textTagType || entry.attr("NameSpace") != textNamespace {
			continue
		}
		locNode := entry.child("FileLoc")
		if locNode == nil {
			continue
		}
		locText := strings.TrimSpace(editorImportText(indexData, locNode))
		if locText == "" {
			continue
		}
		tagPath := cleanPackagePath(resolveResourcePath(baseDir, "", locText))
		tagData, ok := parts[tagPath]
		if !ok {
			tagData, err = e.source.reader.readFileViewContext(ctx, tagPath)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				continue
			}
		}
		tagMeta := CustomTag{TypeID: textTagType, NameSpace: textNamespace, FileLoc: locText}
		keptCount := 0
		updatedTag, rwErr := rewriteStructureReferences(ctx, tagData, tagMeta, func(pageID string, ids []string) (string, []string, error) {
			pKey := editorResourceID(pageID)
			if loadedPages[pKey] {
				return pageID, nil, nil
			}
			if len(collectedObjects[pKey]) == 0 && len(e.source.structureObjects[pKey]) == 0 {
				keptCount += len(ids)
				return pageID, ids, nil
			}
			var remaining []string
			for _, id := range ids {
				oKey := editorResourceID(id)
				if collectedObjects[pKey][oKey] || e.source.structureObjects[pKey][oKey] {
					continue
				}
				remaining = append(remaining, id)
			}
			keptCount += len(remaining)
			return pageID, remaining, nil
		})
		if rwErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if keptCount == 0 {
			patches = append(patches, editorXMLPatch{start: entry.start, end: entry.end})
			delete(parts, tagPath)
			pathCounts[tagPath]--
			if pathCounts[tagPath] <= 0 {
				if retired == nil {
					retired = make(map[string]bool)
				}
				retired[tagPath] = true
			}
		} else if !bytes.Equal(updatedTag, tagData) {
			parts[tagPath] = updatedTag
		}
	}
	var addedEntries []byte
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tagXML, encErr := encodeStructureNodeContext(ctx, buildTextLayoutNode(group))
		if encErr != nil {
			return nil, encErr
		}
		fileLoc := packageAvailableName(e.source.reader, parts, e.packageName("Tags/Tag.xml"))
		parts[fileLoc] = tagXML
		delete(retired, cleanPackagePath(fileLoc))
		attrs := ofdAttrs{{Name: xml.Name{Local: "TypeID"}, Value: textTagType}, {Name: xml.Name{Local: "NameSpace"}, Value: textNamespace}}
		entryBytes, cErr := container.containerXML("CustomTag", attrs, container.textXML("FileLoc", "/"+fileLoc))
		if cErr != nil {
			return nil, cErr
		}
		addedEntries = append(addedEntries, bytes.TrimPrefix(entryBytes, []byte(xml.Header))...)
	}
	if len(patches) == 0 && len(addedEntries) == 0 {
		return retired, nil
	}
	if len(patches) > 0 {
		indexData = editorPatchXML(indexData, patches)
		container, err = parseEditorXMLContext(ctx, indexData)
		if err != nil {
			return nil, err
		}
		if e.source.document.CustomTags.Path == "" && indexName == e.source.reader.documentRoot {
			if sub := container.child("CustomTags"); sub != nil {
				container = sub
			}
		}
	}
	if len(addedEntries) > 0 {
		if container.open != container.end {
			addedEntries = append(bytes.Clone(indexData[container.open:container.close]), addedEntries...)
		}
		indexData = editorPatchXML(indexData, []editorXMLPatch{editorXMLContent(indexData, container, addedEntries)})
		if e.source.document.CustomTags.Path == "" && indexName != e.source.reader.documentRoot {
			docName := e.source.reader.documentRoot
			docData, ok := parts[docName]
			if !ok {
				docData, err = e.source.reader.readFileViewContext(ctx, docName)
				if err != nil {
					return nil, err
				}
			}
			docRoot, pErr := parseEditorXMLContext(ctx, docData)
			if pErr != nil {
				return nil, pErr
			}
			parts[docName], err = editorXMLSetText(docData, docRoot, [][2]string{{"CustomTags", "/" + indexName}})
			if err != nil {
				return nil, err
			}
		}
	}
	parts[indexName] = indexData
	return retired, nil
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
	if t.DrawParam != "" || t.Clips != nil || len(t.Actions) != 0 || t.Alpha != nil && *t.Alpha != 255 || t.Visible != nil && !*t.Visible || t.Stroke != nil && *t.Stroke || t.Fill != nil && !*t.Fill || t.HScale != 0 && t.HScale != 1 || t.ReadDirection != 0 || t.CharDirection != 0 || t.Weight != 0 && t.Weight != 400 && t.Weight != 700 {
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
	if err := e.copyImportedTextLayouts(ctx, source, sourcePage, ids, annotations, page, copied, copiedNotes); err != nil {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if before.Appearance == nil || after.Appearance == nil {
			continue
		}
		snapshot, err := source.captureRichTextSnapshotObjects(sourcePage, before.Appearance.Objects)
		if err != nil {
			return err
		}
		if snapshot != nil {
			if err := e.putRichTextSnapshotObjects(page, after.Appearance.Objects, snapshot); err != nil {
				return err
			}
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
		if object.Type != "CompositeObject" {
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
	if object.Type != "CompositeObject" {
		return nil, nil
	}
	index := e.richTextMetadataIndex()
	if index.err != nil || len(index.entries) == 0 {
		return nil, index.err
	}
	active := make(map[string]bool)
	var capture func(GraphicObject) (*richTextSnapshot, error)
	capture = func(object GraphicObject) (*richTextSnapshot, error) {
		if object.Type != "CompositeObject" {
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
		resID := object.CompositeObject.ResourceID
		if resID == "" {
			return nil, nil
		}
		if active[resID] {
			return nil, fmt.Errorf("cyclic composite resource")
		}
		active[resID] = true
		defer delete(active, resID)
		definition, err := e.compositeDefinition(resID)
		if err != nil {
			return nil, err
		}
		if definition.unit == nil {
			return nil, nil
		}
		var snapshot *richTextSnapshot
		i := 0
		for child := range definition.unit.objects() {
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

// captureRichTextSnapshotObjects 捕获对象列表内的富文本标引快照
// 入参: page 页面索引, objects 图形对象列表
// 返回: *richTextSnapshot 不可变快照，无标引时为空, error 标引或资源错误
func (e *Editor) captureRichTextSnapshotObjects(page int, objects []GraphicObject) (*richTextSnapshot, error) {
	index := e.richTextMetadataIndex()
	if index.err != nil || len(index.entries) == 0 {
		return nil, index.err
	}
	var snapshot *richTextSnapshot
	for i, child := range objects {
		value, err := e.captureRichTextSnapshot(page, child)
		if err != nil {
			return nil, err
		}
		if value != nil {
			if snapshot == nil {
				snapshot = &richTextSnapshot{children: make(map[int]*richTextSnapshot)}
			}
			snapshot.children[i] = value
		}
	}
	return snapshot, nil
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
	resID := object.CompositeObject.ResourceID
	if resID == "" {
		if len(snapshot.children) != 0 {
			return fmt.Errorf("copied composite structure differs")
		}
		return nil
	}
	definition, err := e.compositeDefinition(resID)
	if err != nil {
		return err
	}
	if definition.unit == nil {
		if len(snapshot.children) != 0 {
			return fmt.Errorf("copied composite structure differs")
		}
		return nil
	}
	i, count := 0, 0
	for child := range definition.unit.objects() {
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

// putRichTextSnapshotObjects 恢复对象列表中的嵌套富文本标引
// 入参: page 目标页, objects 目标图形对象列表, snapshot 来源不可变语义
// 返回: error 结构或标引错误
func (e *Editor) putRichTextSnapshotObjects(page int, objects []GraphicObject, snapshot *richTextSnapshot) error {
	count := 0
	for i, child := range objects {
		if value := snapshot.children[i]; value != nil {
			if err := e.putRichTextSnapshot(page, child, value); err != nil {
				return err
			}
			count++
		}
	}
	if count != len(snapshot.children) {
		return fmt.Errorf("copied composite structure differs")
	}
	return nil
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
		unit.ID, unit.ResourceID = node.object.TextObject.ID, next.CompositeObject.ResourceID
		next.CompositeObject = unit
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

// copyImportedTextLayouts 在跨文档迁移选区后同步递归恢复正文、复合资源及注解内部的普通文字排版语义
// 入参: ctx 取消上下文, source 来源编辑器, sourcePage 来源页, ids 来源正文标识, annotations 来源注解标识, page 目标页, copied 目标正文标识, copiedNotes 目标注解标识
// 返回: error 读取或取消错误
func (e *Editor) copyImportedTextLayouts(ctx context.Context, source *Editor, sourcePage int, ids, annotations []string, page int, copied, copiedNotes []string) error {
	if source == nil || e == nil || sourcePage < 0 || sourcePage >= len(source.pages) || page < 0 || page >= len(e.pages) {
		return nil
	}
	srcPageID, dstPageID := source.pages[sourcePage].ID, e.pages[page].ID
	type capturedText struct {
		digest string
		layout *textLayout
	}
	captureUnitTree := func(ed *Editor, pageID string, rootUnit CompositeGraphicUnit, baseStates map[string]editorCompositeState) []capturedText {
		var list []capturedText
		visiting := make(map[string]bool)
		var visitComposite func(CompositeObject, map[string]editorCompositeState)
		var visitUnit func(CompositeGraphicUnit, map[string]editorCompositeState)
		visitComposite = func(comp CompositeObject, inherited map[string]editorCompositeState) {
			states := inherited
			if len(comp.states) != 0 {
				states = maps.Clone(inherited)
				if states == nil {
					states = make(map[string]editorCompositeState, len(comp.states))
				}
				maps.Copy(states, comp.states)
			}
			if comp.ResourceID != "" {
				resKey := editorResourceID(comp.ResourceID)
				if resKey == "" {
					resKey = comp.ResourceID
				}
				if !visiting[resKey] {
					visiting[resKey] = true
					resStates := states
					for _, res := range ed.resources {
						if sameResourceID(res.composite, comp.ResourceID) && len(res.states) != 0 {
							resStates = maps.Clone(states)
							if resStates == nil {
								resStates = make(map[string]editorCompositeState, len(res.states))
							}
							maps.Copy(resStates, res.states)
							break
						}
					}
					if def, err := ed.compositeDefinition(comp.ResourceID); err == nil && def.unit != nil {
						visitUnit(*def.unit, resStates)
					}
					delete(visiting, resKey)
				}
			}
		}
		visitUnit = func(unit CompositeGraphicUnit, states map[string]editorCompositeState) {
			for child := range unit.objects() {
				switch child.Type {
				case "TextObject":
					digest := child.TextObject.textDigest()
					layout := child.TextObject.layout
					if layout == nil {
						if st, ok := resourceValue(states, child.TextObject.ID); ok && st.layout != nil {
							layout = st.layout
						}
					}
					if layout == nil {
						layout = ed.lookupTextLayout(pageID, child.TextObject.ID, digest)
					}
					list = append(list, capturedText{digest: digest, layout: layout})
				case "CompositeObject":
					visitComposite(child.CompositeObject, states)
				}
			}
		}
		visitUnit(rootUnit, baseStates)
		return list
	}
	captureTree := func(ed *Editor, pageID string, obj GraphicObject, baseStates map[string]editorCompositeState) []capturedText {
		switch obj.Type {
		case "TextObject":
			digest := obj.TextObject.textDigest()
			layout := obj.TextObject.layout
			if layout == nil {
				layout = ed.lookupTextLayout(pageID, obj.TextObject.ID, digest)
			}
			return []capturedText{{digest: digest, layout: layout}}
		case "CompositeObject":
			return captureUnitTree(ed, pageID, CompositeGraphicUnit{Objects: []GraphicObject{obj}}, baseStates)
		}
		return nil
	}
	applyUnitStates := func(rootUnit CompositeGraphicUnit, captured []capturedText, existing map[string]editorCompositeState) map[string]editorCompositeState {
		cursor := 0
		states := maps.Clone(existing)
		visiting := make(map[string]bool)
		var visitComposite func(CompositeObject)
		var visitUnit func(CompositeGraphicUnit)
		visitComposite = func(comp CompositeObject) {
			if comp.ResourceID != "" {
				resKey := editorResourceID(comp.ResourceID)
				if resKey == "" {
					resKey = comp.ResourceID
				}
				if !visiting[resKey] {
					visiting[resKey] = true
					if def, err := e.compositeDefinition(comp.ResourceID); err == nil && def.unit != nil {
						visitUnit(*def.unit)
					}
					delete(visiting, resKey)
				}
			}
		}
		visitUnit = func(unit CompositeGraphicUnit) {
			for child := range unit.objects() {
				switch child.Type {
				case "TextObject":
					if cursor < len(captured) {
						item := captured[cursor]
						cursor++
						if item.layout != nil && child.TextObject.ID != "" && child.TextObject.textDigest() == item.digest {
							if states == nil {
								states = make(map[string]editorCompositeState)
							}
							st := states[child.TextObject.ID]
							st.layout = item.layout
							if origin, err := e.textLayoutOrigin(child.TextObject); err == nil {
								st.origin = origin
							}
							states[child.TextObject.ID] = st
						}
					}
				case "CompositeObject":
					visitComposite(child.CompositeObject)
				}
			}
		}
		visitUnit(rootUnit)
		return states
	}
	positions := e.objectPositions(&e.pages[page])
	for i := 0; i < len(ids) && i < len(copied); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, err := source.Object(sourcePage, ids[i])
		if err != nil {
			return err
		}
		captured := captureTree(source, srcPageID, before, before.CompositeObject.states)
		hasAny := slices.ContainsFunc(captured, func(c capturedText) bool { return c.layout != nil })
		if !hasAny {
			continue
		}
		pos, ok := positions[copied[i]]
		if !ok {
			continue
		}
		target := &e.pages[page].Content.Layer[pos.layer].Objects[pos.index]
		if target.Type == "TextObject" && len(captured) == 1 && captured[0].layout != nil && target.TextObject.textDigest() == captured[0].digest {
			target.TextObject.layout = captured[0].layout
			if origin := e.origins[copied[i]]; origin != nil {
				copiedOrigin := *origin
				copiedOrigin.object.TextObject.layout = captured[0].layout
				e.origins[copied[i]] = &copiedOrigin
			}
		} else if target.Type == "CompositeObject" {
			target.CompositeObject.states = applyUnitStates(CompositeGraphicUnit{Objects: []GraphicObject{*target}}, captured, target.CompositeObject.states)
			if origin := e.origins[copied[i]]; origin != nil {
				copiedOrigin := *origin
				copiedOrigin.object.CompositeObject.states = target.CompositeObject.states
				e.origins[copied[i]] = &copiedOrigin
			}
		}
	}
	for i := 0; i < len(annotations) && i < len(copiedNotes); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, err := source.Annotation(sourcePage, annotations[i])
		if err != nil {
			return err
		}
		after, err := e.Annotation(page, copiedNotes[i])
		if err != nil {
			return err
		}
		if before.Appearance == nil || after.Appearance == nil {
			continue
		}
		var srcStates map[string]editorCompositeState
		if source.source != nil {
			srcStates = source.source.annotationStates[srcPageID+"/"+editorResourceID(annotations[i])]
			if srcStates == nil {
				srcStates = source.source.annotationStates[srcPageID+"/"+annotations[i]]
			}
		}
		srcUnit := CompositeGraphicUnit{Objects: before.Appearance.Objects, TextObject: before.Appearance.TextObject, PathObject: before.Appearance.PathObject, ImageObject: before.Appearance.ImageObject, CompositeObject: before.Appearance.CompositeObject}
		captured := captureUnitTree(source, srcPageID, srcUnit, srcStates)
		if !slices.ContainsFunc(captured, func(c capturedText) bool { return c.layout != nil }) {
			continue
		}
		dstUnit := CompositeGraphicUnit{Objects: after.Appearance.Objects, TextObject: after.Appearance.TextObject, PathObject: after.Appearance.PathObject, ImageObject: after.Appearance.ImageObject, CompositeObject: after.Appearance.CompositeObject}
		key := dstPageID + "/" + editorResourceID(copiedNotes[i])
		var existing map[string]editorCompositeState
		if e.source != nil {
			existing = e.source.annotationStates[key]
		}
		updatedStates := applyUnitStates(dstUnit, captured, existing)
		if len(updatedStates) != 0 && e.source != nil {
			statesByID := maps.Clone(e.source.annotationStates)
			if statesByID == nil {
				statesByID = make(map[string]map[string]editorCompositeState)
			}
			statesByID[key] = updatedStates
			e.source.annotationStates = statesByID
		}
	}
	return ctx.Err()
}
