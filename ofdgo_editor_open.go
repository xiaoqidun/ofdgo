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
	"image"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// editorSource 保留输入包及按需加载的原始页面
type editorSource struct {
	reader           *Reader
	document         *Document
	info             DocInfo
	directory        string
	pages            map[string]*editorSourcePage
	origins          map[string]*editorObjectOrigin
	idsReady         bool
	annotationStates map[string]map[string]editorCompositeState
	annotationPages  map[string]bool
}

// editorObjectOrigin 保留原对象的XML语义，供复制和局部更新复用
type editorObjectOrigin struct {
	editor *Editor
	page   *editorSourcePage
	data   []byte
	node   *editorXML
	object GraphicObject
	reason error
}

// editorSourcePage 保存页面原文、解析快照及图层和页块内的对象位置
type editorSourcePage struct {
	ref      Page
	data     []byte
	root     *editorXML
	original *PageContent
	nodes    map[string]*editorXML
	repaired bool
}

// ObjectCapabilities 已有对象可执行的操作，Reason说明受限原因
// Update表示完整替换内容，ReplaceFont表示可显式替换文字字体，Transform表示保留文字定位的几何变换，Order仅限同一图层或末级页块
// Arrange表示可准确度量对齐与旋转范围；Reflow表示可重排文字，LayoutKnown表示段落选项可恢复
// TextContent表示可修改文字内容，未知段落保持原定位，不自动重排
// FitImage表示可按原始图片比例适应或填充现有边界
// ResetCrop表示有可单独还原的会话裁剪
// Paint表示可独立修改纯色填充和描边，不要求重新排版文字
// ReplaceImage表示可替换图片数据，CropImage表示可裁剪图片；内部裁剪与原裁剪取交集
// MissingGlyphs提供缺字导致操作受限时的结构化诊断
// RewriteText表示可保留原定位修改，具体替换仍需校验长度、字形映射和字体覆盖
// Ungroup表示可移除组合容器并保留内部内容，Stretch表示图片及纯图片组合可独立调整宽高
type ObjectCapabilities struct {
	Update        bool
	TextContent   bool
	RewriteText   bool
	Paint         bool
	ReplaceFont   bool
	ReplaceImage  bool
	CropImage     bool
	FitImage      bool
	ResetCrop     bool
	Reflow        bool
	LayoutKnown   bool
	Transform     bool
	Arrange       bool
	Copy          bool
	Delete        bool
	Order         bool
	Ungroup       bool
	Stretch       bool
	Reason        string
	ReasonCode    EditReason
	MissingGlyphs *MissingGlyphError
}

// editError 将操作受限原因转为错误，保留可供调用方识别的缺字诊断
// 返回: error 受限原因
func (c ObjectCapabilities) editError() error {
	var err error = errors.New(c.Reason)
	if c.MissingGlyphs != nil {
		err = c.MissingGlyphs
	}
	return &EditError{Code: c.ReasonCode, Err: err}
}

// PageCapabilities 页面可执行的操作，Insert表示可以添加RGB对象；新增空白页面始终可用
type PageCapabilities struct {
	Insert bool
	Copy   bool
	Delete bool
	Move   bool
	Resize bool
}

// Editor 打开主文档进行保真编辑，不修改输入包，不以权限声明或签名限制编辑
// 原权限和签名数据保留，修改受保护内容会使原签名失效，不重新签名
// 页面按需解析，未修改条目直接保留；编辑器及其Reader快照使用期间不得关闭输入Reader
// 返回: *Editor 编辑器, error 错误信息
func (r *Reader) Editor() (*Editor, error) {
	_, err := r.Doc()
	if err != nil {
		return nil, err
	}
	reader := cloneEditorReader(r)
	doc := reader.doc
	maximum := doc.CommonData.MaxUnitID
	if maximum < 0 {
		return nil, fmt.Errorf("invalid MaxUnitID")
	}
	info, err := reader.DocInfo()
	if err != nil {
		return nil, err
	}
	if info.CustomDatas != nil {
		for i := range info.CustomDatas.CustomData {
			info.CustomDatas.CustomData[i].sourceIndex = i + 1
		}
	}
	e := NewEditor()
	e.encryption = r.encryption
	e.Info, e.maxID = cloneEditorData(*info), maximum
	directory := cleanPackagePath(reader.RootDir)
	e.source = &editorSource{reader: reader, document: doc, info: cloneEditorData(*info), directory: directory, pages: make(map[string]*editorSourcePage, len(doc.Pages.Page)), origins: make(map[string]*editorObjectOrigin)}
	e.pages = make([]PageContent, len(doc.Pages.Page))
	sourcePages := make([]editorSourcePage, len(doc.Pages.Page))
	for i, page := range doc.Pages.Page {
		if page.ID == "" || e.source.pages[page.ID] != nil {
			return nil, fmt.Errorf("missing or duplicate page ID %q", page.ID)
		}
		sourcePages[i].ref = page
		e.source.pages[page.ID] = &sourcePages[i]
		e.pages[i] = PageContent{ID: page.ID}
	}
	return e, nil
}

// cloneEditorReader 复用已验证的只读包索引，独立保存可变文档与读取缓存
// 入参: reader 已加载主文档的阅读器
// 返回: *Reader 不拥有输入文件的独立阅读器
func cloneEditorReader(reader *Reader) *Reader {
	next := *reader
	next.Closer = nil
	next.OFD = cloneEditorData(reader.OFD)
	doc := *reader.doc
	pages := doc.Pages.Page
	doc.Pages.Page = nil
	doc = cloneEditorData(doc)
	doc.Pages.Page = slices.Clone(pages)
	next.doc = &doc
	next.files = maps.Clone(reader.files)
	next.ResMap = maps.Clone(reader.ResMap)
	next.resourcesRead = maps.Clone(reader.resourcesRead)
	next.resourceFiles = maps.Clone(reader.resourceFiles)
	next.fontCache = cloneEditorMap(reader.fontCache)
	next.fontFaces = maps.Clone(reader.fontFaces)
	next.colorSpaceCache = cloneEditorMap(reader.colorSpaceCache)
	next.drawParamCache = cloneEditorMap(reader.drawParamCache)
	next.compositeGraphicUnitCache = cloneEditorMap(reader.compositeGraphicUnitCache)
	next.pageHeaderCache = maps.Clone(reader.pageHeaderCache)
	next.Stamps = cloneEditorMap(reader.Stamps)
	next.Annots = cloneEditorMap(reader.Annots)
	next.annotationFiles = cloneEditorMap(reader.annotationFiles)
	return &next
}

// cloneEditorMap 复制对外可变缓存值，避免快照之间共享嵌套切片或指针
// 入参: values 原缓存
// 返回: map[string]T 独立缓存
func cloneEditorMap[T any](values map[string]T) map[string]T {
	result := maps.Clone(values)
	for key, value := range result {
		result[key] = cloneEditorData(value)
	}
	return result
}

// prepareSourceIDs 首次新增内容前流式核对原包标识，不依赖可能缺失或过期的MaxUnitID
// 返回: error 错误信息
func (e *Editor) prepareSourceIDs() error {
	maximum, err := e.sourceMaxID(nil)
	if err != nil {
		return err
	}
	e.maxID = maximum
	if e.source != nil {
		e.source.idsReady = true
	}
	return nil
}

// sourceMaxID 核对最大标识，不修改文档或缓存，允许导入准备阶段取消
// 入参: progress 进度回调
// 返回: int 最大标识, error 错误信息
func (e *Editor) sourceMaxID(progress editorProgress) (int, error) {
	if e.source == nil || e.source.idsReady {
		return e.maxID, nil
	}
	reader := e.source.reader
	names := make(map[string]bool)
	for name := range reader.fileIndex {
		names[name] = true
	}
	for name := range reader.files {
		names[name] = true
	}
	maximum := e.maxID
	for name := range names {
		if !strings.EqualFold(path.Ext(name), ".xml") {
			continue
		}
		input, err := reader.openFile(name)
		if err != nil {
			return 0, err
		}
		id, err := editorXMLMaxID(&editorProgressReader{Reader: input, progress: progress, stage: "ids"})
		input.Close()
		if err != nil {
			return 0, fmt.Errorf("scan IDs in %s: %w", name, err)
		}
		maximum = max(maximum, id)
	}
	return maximum, nil
}

// editorXMLMaxID 流式读取OFD结构中的最大标识，跳过非OFD的XML附件
// 入参: input XML输入流
// 返回: int 最大标识, error 错误信息
func editorXMLMaxID(input io.Reader) (int, error) {
	decoder := xml.NewDecoder(input)
	maximum, started := 0, false
	for {
		token, err := decoder.Token()
		var syntax *xml.SyntaxError
		if err == io.EOF || !started && errors.As(err, &syntax) {
			return maximum, nil
		}
		if err != nil {
			return 0, err
		}
		node, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !started {
			switch node.Name.Local {
			case "OFD", "Document", "Page", "Res", "PageAnnot", "Annotations", "Attachments", "CustomTags":
			default:
				return 0, nil
			}
			started = true
		}
		for _, attr := range node.Attr {
			if attr.Name.Space == "" && attr.Name.Local == "ID" {
				id, err := strconv.Atoi(attr.Value)
				if err == nil {
					maximum = max(maximum, id)
				}
			}
		}
	}
}

// originalPage 判断索引是否指向原文档页面
// 入参: index 页面索引
// 返回: bool 是否为原始页面
func (e *Editor) originalPage(index int) bool {
	return e.source != nil && index >= 0 && index < len(e.pages) && e.source.pages[e.pages[index].ID] != nil
}

// PageCapabilities 获取页面操作范围，复制和删除由库统一维护标准引用
// 入参: index 页面索引
// 返回: PageCapabilities 操作能力, error 错误信息
func (e *Editor) PageCapabilities(index int) (PageCapabilities, error) {
	if index < 0 || index >= len(e.pages) {
		return PageCapabilities{}, fmt.Errorf("page index %d out of range", index)
	}
	return PageCapabilities{Insert: e.sourceRGB(), Copy: true, Delete: true, Move: true, Resize: true}, nil
}

// sourceRGB 判断新建RGB对象能否直接使用文档默认颜色空间
// 返回: bool 是否为8位RGB文档
func (e *Editor) sourceRGB() bool {
	if e.source == nil || e.source.document.CommonData.DefaultCS == 0 {
		return true
	}
	space := e.source.reader.colorSpaceCache[strconv.Itoa(e.source.document.CommonData.DefaultCS)]
	return space != nil && space.Type == "RGB" && (space.BitsPerComponent == 0 || space.BitsPerComponent == 8) && len(space.Palette) == 0
}

// repairSourceObjectIDs 按实际节点区分重复对象，只修复没有引用歧义的编号
// 入参: data 页面原文, root 页面节点
// 返回: []byte 编辑副本, bool 是否修复, error 错误信息
func (e *Editor) repairSourceObjectIDs(data []byte, root *editorXML) ([]byte, bool, error) {
	seen := make(map[string]bool)
	duplicates := make(map[string]bool)
	var nodes []*editorXML
	var visit func(*editorXML) error
	visit = func(parent *editorXML) error {
		for _, node := range parent.children {
			id := node.attr("ID")
			if id != "" && seen[id] {
				switch node.name.Local {
				case "TextObject", "PathObject", "ImageObject", "CompositeObject":
					nodes = append(nodes, node)
					duplicates[id] = true
				default:
					return fmt.Errorf("duplicate container ID %q", id)
				}
			}
			seen[id] = true
			if node.name.Local == "PageBlock" {
				if err := visit(node); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if content := root.child("Content"); content != nil {
		for _, layer := range content.children {
			if layer.name.Local == "Layer" {
				if err := visit(layer); err != nil {
					return nil, false, err
				}
			}
		}
	}
	if len(nodes) == 0 {
		return data, false, nil
	}
	if err := e.checkDuplicateObjectReferences(duplicates); err != nil {
		return nil, false, err
	}
	if err := e.prepareSourceIDs(); err != nil {
		return nil, false, err
	}
	maximum := e.maxID
	var patches []editorXMLPatch
	for _, node := range nodes {
		maximum++
		encoded, err := editorXMLAttribute(data, node, "ID", strconv.Itoa(maximum))
		if err != nil {
			return nil, false, err
		}
		patches = append(patches, editorXMLPatch{node.start, node.end, encoded})
	}
	e.maxID = maximum
	return editorPatchXML(data, patches), true, nil
}

// checkDuplicateObjectReferences 拒绝无法判定目标的编号引用，不猜测其所属对象
// 入参: duplicates 重复编号
// 返回: error 引用歧义或读取错误
func (e *Editor) checkDuplicateObjectReferences(duplicates map[string]bool) error {
	reader := e.source.reader
	for _, name := range reader.fileNamesFold {
		if !strings.EqualFold(path.Ext(name), ".xml") {
			continue
		}
		input, err := reader.openFile(name)
		if err != nil {
			return err
		}
		decoder := xml.NewDecoder(input)
		for {
			token, readErr := decoder.Token()
			if readErr != nil {
				input.Close()
				if readErr != io.EOF {
					return readErr
				}
				break
			}
			if node, ok := token.(xml.StartElement); ok {
				for _, attr := range node.Attr {
					if editorObjectReference(attr.Name.Local) && duplicates[editorResourceID(attr.Value)] {
						input.Close()
						return fmt.Errorf("ambiguous reference to duplicate object ID %q in %s", attr.Value, name)
					}
				}
			}
		}
	}
	return nil
}

// loadSourcePage 首次访问时解析原始页面，不加载图片或字体二进制数据
// 入参: index 页面索引
// 返回: error 错误信息
func (e *Editor) loadSourcePage(index int) error {
	source := e.source.pages[e.pages[index].ID]
	if source == nil || source.original != nil {
		return nil
	}
	loaded := *source
	source = &loaded
	reader := e.source.reader
	data, err := reader.readFile(reader.ResPath(source.ref.BaseLoc))
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	page, err := reader.PageContent(source.ref)
	if err != nil {
		return err
	}
	data, repaired, err := e.repairSourceObjectIDs(data, root)
	if err != nil {
		return err
	}
	if repaired {
		root, err = parseEditorXML(data)
		if err != nil {
			return err
		}
		var updated PageContent
		if err := xml.Unmarshal(data, &updated); err != nil {
			return err
		}
		page.Content = updated.Content
	}
	nodes := make(map[string]*editorXML)
	if content := root.child("Content"); content != nil {
		for _, layer := range content.children {
			if layer.name.Local != "Layer" {
				continue
			}
			if err := editorXMLObjects(layer, nodes); err != nil {
				return err
			}
		}
	}
	source.data, source.root, source.nodes = data, root, nodes
	source.repaired = repaired
	for i := range page.Content.Layer {
		layer := &page.Content.Layer[i]
		for j, object := range layer.Objects {
			id := editorObjectID(object)
			if node := nodes[id]; node != nil {
				origin := &editorObjectOrigin{page: source, data: data, node: node, object: object}
				e.source.origins[id] = origin
				if editorXMLTransformable(node) {
					resolved, err := e.resolveEditorStyle(object, layer.DrawParam)
					if err != nil {
						origin.reason = err
					} else {
						layer.Objects[j] = resolved
					}
				}
			}
		}
	}
	source.original = page
	e.source.pages[source.ref.ID] = source
	e.pages[index] = copyEditorPage(*page)
	return nil
}

// ObjectCapabilities 获取对象可执行的操作，不支持的内容保持原文，不强制转换
// 字体缺失或子集不包含原文时仍可保真移动、复制和删除，不自动重新排版
// 显式提供可用字体后可通过UpdateObject替换；原文布局未知时不自动推断段落选项
// 入参: page 页面索引, id 对象标识
// 返回: ObjectCapabilities 操作能力, error 错误信息
func (e *Editor) ObjectCapabilities(page int, id string) (ObjectCapabilities, error) {
	layer, index, err := e.findObject(page, id)
	if err != nil {
		return ObjectCapabilities{}, err
	}
	return e.objectCapabilities(layer.Objects[index], make(map[*editorXML]bool)), nil
}

// objectCapabilities 检查对象能力，在同次遍历中复用原容器排序检查
// 入参: object 当前对象, orderable 原容器排序能力
// 返回: ObjectCapabilities 操作能力
func (e *Editor) objectCapabilities(object GraphicObject, orderable map[*editorXML]bool) ObjectCapabilities {
	id := editorObjectID(object)
	all := ObjectCapabilities{Update: true, Paint: object.Type == "TextObject" || object.Type == "PathObject", ReplaceFont: object.Type == "TextObject", Reflow: object.Type == "TextObject" && object.TextObject.ReadDirection == 0 && object.TextObject.CharDirection == 0, LayoutKnown: object.Type == "TextObject" && (e.objectOrigin(id) == nil || object.TextObject.layout != nil), Transform: true, Arrange: true, Copy: true, Delete: true, Order: true}
	all.ResetCrop = object.Type == "ImageObject" && object.state.crop != nil
	origin := e.objectOrigin(id)
	if origin == nil {
		all.TextContent = all.Reflow
		all.RewriteText = object.Type == "TextObject"
		all.Stretch = object.Type == "ImageObject"
		all.ReplaceImage = object.Type == "ImageObject"
		all.CropImage = all.ReplaceImage
		all.FitImage = all.ReplaceImage && axisAlignedMatrix(NewMatrix(object.ImageObject.CTM))
		return all
	}
	node := origin.node
	if !editorXMLTransformable(node) {
		return ObjectCapabilities{Reason: "object uses unsupported editing features", ReasonCode: EditUnsupportedObject}
	}
	if origin.reason != nil && editReason(origin.reason) == EditUnsupportedStyle {
		return ObjectCapabilities{Reason: origin.reason.Error(), ReasonCode: EditUnsupportedStyle}
	}
	if !editorXMLContainersSupported(node) {
		return ObjectCapabilities{Reason: "object container uses unsupported editing features", ReasonCode: EditUnsupportedContainer}
	}
	if err := validateEditorGeometry(object); err != nil {
		return ObjectCapabilities{Reason: err.Error(), ReasonCode: EditInvalidObject}
	}
	var checked bool
	if all.Order, checked = orderable[node.parent]; !checked {
		all.Order = editorContainerOrderable(node.parent)
		orderable[node.parent] = all.Order
	}
	all.Ungroup = all.Order && e.compositeUngroupable(node) && (object.CompositeGraphicUnit.ResourceID != "" || len(object.CompositeGraphicUnit.Objects) != 0)
	all.Copy = editorXMLCopyable(node) && (origin.reason == nil || object.Type == "PathObject" && editReason(origin.reason) == EditUnsupportedColor)
	if origin.reason != nil {
		all.Update, all.Reflow, all.ReplaceFont, all.Paint = false, false, false, false
		all.Paint = (object.Type == "TextObject" || object.Type == "PathObject") && editReason(origin.reason) == EditUnsupportedColor
		all.Reason, all.ReasonCode = origin.reason.Error(), editReason(origin.reason)
	} else if !editorXMLSupported(node) {
		all.Update, all.Reflow, all.ReplaceFont = false, false, false
		all.Reason, all.ReasonCode = "content can only be edited with its original structure preserved", EditUnsupportedObject
	} else if _, err := e.prepareObject(id, object); err != nil {
		all.Update, all.Reflow, all.Reason = false, false, err.Error()
		all.ReasonCode = editReason(err)
		errors.As(err, &all.MissingGlyphs)
	}
	all.Paint = all.Paint && e.editorPaintable(object)
	all.ReplaceImage = all.Update && object.Type == "ImageObject"
	all.CropImage = all.ReplaceImage
	all.FitImage = all.ReplaceImage && axisAlignedMatrix(NewMatrix(object.ImageObject.CTM))
	if object.Type == "ImageObject" && object.ImageObject.Border != nil && len(object.ImageObject.Actions) != 0 {
		all.Transform, all.Arrange = false, false
		all.Reason, all.ReasonCode = "image actions cannot be transformed together with the border", EditUnsupportedObject
	}
	if object.Type == "TextObject" {
		all.TextContent = all.Update && object.TextObject.ReadDirection == 0 && object.TextObject.CharDirection == 0 && (all.LayoutKnown || len(object.TextObject.CGTransform) == 0)
		all.RewriteText = all.TextContent
		if !all.RewriteText {
			text := object.TextObject
			all.RewriteText = e.RewriteText(&text, text.Text()) == nil
		}
		all.Arrange = all.Update || e.editorTextMeasurable(object.TextObject)
		if _, err := object.TextObject.TextFrame(); err != nil {
			all.Reflow = false
		}
	}
	all.Stretch = all.Transform && e.objectStretchable(object, make(map[string]bool))
	return all
}

// objectOrigin 获取原对象或其副本的保真来源
// 入参: id 对象标识
// 返回: *editorObjectOrigin 原始对象，无来源时为nil
func (e *Editor) objectOrigin(id string) *editorObjectOrigin {
	if origin := e.origins[id]; origin != nil {
		return origin
	}
	if e.source == nil {
		return nil
	}
	return e.source.origins[id]
}

// setObjectOrigin 保存当前编辑器的对象原文，支持新建文档中的组合对象
// 入参: id 对象标识, origin 原文来源，nil恢复原始来源
func (e *Editor) setObjectOrigin(id string, origin *editorObjectOrigin) {
	if e.origins == nil {
		e.origins = make(map[string]*editorObjectOrigin)
	}
	if origin != nil && origin.page == nil && origin.editor == nil {
		copy := *origin
		copy.editor = e
		origin = &copy
	}
	e.origins[id] = origin
}

// editorPreservedObject 判断对象是否仅改变标识、几何和可独立写回的外观字段
// 入参: before 原始快照, after 新快照
// 返回: bool 是否保留原内容
func editorPreservedObject(before, after GraphicObject) bool {
	if before.Type != after.Type {
		return false
	}
	before = cloneEditorData(before)
	before.state = after.state
	switch before.Type {
	case "TextObject":
		a, b := &before.TextObject, after.TextObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
		a.Fill, a.Stroke, a.FillColor, a.StrokeColor, a.LineWidth = b.Fill, b.Stroke, b.FillColor, b.StrokeColor, b.LineWidth
		a.LineWidthSet = b.LineWidthSet
	case "PathObject":
		a, b := &before.PathObject, after.PathObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
		a.Fill, a.Stroke, a.FillColor, a.StrokeColor, a.LineWidth = b.Fill, b.Stroke, b.FillColor, b.StrokeColor, b.LineWidth
		a.LineWidthSet = b.LineWidthSet
		a.DashOffset, a.DashPattern, a.Cap, a.Join = b.DashOffset, b.DashPattern, b.Cap, b.Join
		a.dashPatternSet = b.dashPatternSet
	case "ImageObject":
		a, b := &before.ImageObject, after.ImageObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
	case "CompositeObject", "CompositeGraphicUnit":
		a, b := &before.CompositeGraphicUnit, after.CompositeGraphicUnit
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
	default:
		return false
	}
	a, b := *editorObjectClips(&before), *editorObjectClips(&after)
	if a == nil {
		*editorObjectClips(&before) = b
	} else if b != nil && len(a.Clip) <= len(b.Clip) {
		a.Clip = append(a.Clip, b.Clip[len(a.Clip):]...)
		for i := range a.Clip {
			if len(a.Clip[i].Area) == len(b.Clip[i].Area) {
				for j := range a.Clip[i].Area {
					a.Clip[i].Area[j].CTM = b.Clip[i].Area[j].CTM
				}
			}
		}
	}
	return reflect.DeepEqual(before, after)
}

// prepareCopiedObject 保真复制原对象，不重新生成字形或丢弃未修改字段
// 入参: id 新标识, object 对象快照
// 返回: GraphicObject 独立副本, error 错误信息
func (e *Editor) prepareCopiedObject(id string, object GraphicObject) (GraphicObject, error) {
	origin := e.snapshotOrigin(object)
	if origin == nil && object.origin != nil {
		references := make(map[string]bool)
		collectObjectReferences(object, references)
		delete(references, "")
		if len(references) != 0 {
			return GraphicObject{}, fmt.Errorf("copying a foreign snapshot requires importing its resources")
		}
	}
	object.origin = nil
	if origin == nil {
		return e.prepareObject(id, object)
	}
	if !editorXMLTransformable(origin.node) || !editorXMLContainersSupported(origin.node) || !editorXMLCopyable(origin.node) || origin.reason != nil && (object.Type != "PathObject" || editReason(origin.reason) != EditUnsupportedColor) {
		return GraphicObject{}, fmt.Errorf("object cannot be copied without changing its original structure")
	}
	var drawParam string
	for parent := origin.node.parent; parent != nil; parent = parent.parent {
		if parent.name.Local == "Layer" {
			drawParam = parent.attr("DrawParam")
			break
		}
	}
	before, err := e.copiedObjectStyle(origin.object, drawParam)
	if err != nil {
		return GraphicObject{}, err
	}
	if !editorPreservedObject(before, object) {
		if !editorXMLSupported(origin.node) {
			return GraphicObject{}, fmt.Errorf("copy would replace unsupported object content")
		}
		return e.prepareObject(id, object)
	}
	if err := validateEditorGeometry(object); err != nil {
		return GraphicObject{}, err
	}
	if err := e.validatePreservedAppearance(before, object); err != nil {
		return GraphicObject{}, err
	}
	result := cloneEditorData(object)
	switch result.Type {
	case "TextObject":
		result.TextObject.ID = id
	case "PathObject":
		result.PathObject.ID = id
	case "ImageObject":
		result.ImageObject.ID = id
	case "CompositeObject", "CompositeGraphicUnit":
		result.CompositeGraphicUnit.ID = id
	}
	return result, nil
}

// validatePreservedAppearance 校验保真对象实际修改的颜色、描边和裁剪，不重新解释未修改字段
// 入参: before 原对象, after 新对象
// 返回: error 错误信息
func (e *Editor) validatePreservedAppearance(before, after GraphicObject) error {
	old := []*FillColor{before.TextObject.FillColor, (*FillColor)(before.TextObject.StrokeColor), before.PathObject.FillColor, (*FillColor)(before.PathObject.StrokeColor)}
	next := []*FillColor{after.TextObject.FillColor, (*FillColor)(after.TextObject.StrokeColor), after.PathObject.FillColor, (*FillColor)(after.PathObject.StrokeColor)}
	for i, value := range next {
		if !reflect.DeepEqual(old[i], value) {
			if err := e.editorColor(value); err != nil {
				return err
			}
		}
	}
	a, b := *editorObjectClips(&before), *editorObjectClips(&after)
	if b != nil {
		count := 0
		if a != nil {
			count = len(a.Clip)
		}
		for i, clip := range b.Clip {
			if i >= count {
				if err := e.validateObjectClips(&Clips{Clip: []Clip{clip}}); err != nil {
					return err
				}
				continue
			}
			for j, area := range clip.Area {
				if area.CTM != a.Clip[i].Area[j].CTM && area.CTM != "" {
					if _, err := creationNumbers(area.CTM, 6); err != nil {
						return err
					}
				}
			}
		}
	}
	if after.Type == "PathObject" {
		return validateEditorStroke(after.PathObject)
	}
	return nil
}

// editorContainerOrderable 判断容器的直接对象能否在保留页块的前提下排序
// 入参: container 图层或页块
// 返回: bool 是否可排序
func editorContainerOrderable(container *editorXML) bool {
	for _, child := range container.children {
		if !editorXMLTransformable(child) {
			return false
		}
	}
	return true
}

// editorGeometry 获取基本对象的边界与变换
// 入参: object 对象
// 返回: string 边界, string 变换矩阵
func editorGeometry(object GraphicObject) (string, string) {
	switch object.Type {
	case "TextObject":
		return object.TextObject.Boundary, object.TextObject.CTM
	case "PathObject":
		return object.PathObject.Boundary, object.PathObject.CTM
	case "ImageObject":
		return object.ImageObject.Boundary, object.ImageObject.CTM
	case "CompositeObject", "CompositeGraphicUnit":
		return object.CompositeGraphicUnit.Boundary, object.CompositeGraphicUnit.CTM
	}
	return "", ""
}

// editorFont 按需解析内嵌字体或名称匹配的外部字体，不使用无关回退字体
// 入参: id 字体资源标识
// 返回: FontMetrics 字体度量, error 错误信息
func (e *Editor) editorFont(id string) (FontMetrics, error) {
	if metrics := e.fontMetrics[id]; metrics != nil {
		return metrics, nil
	}
	if e.backends.Fonts == nil {
		return nil, &EditError{Code: EditFontUnavailable, Err: fmt.Errorf("fonts: %w", ErrBackendUnavailable)}
	}
	var data []byte
	createdExternal := false
	for _, resource := range e.resources {
		if resource.font != nil && resource.font.ID == id {
			data = resource.data
			createdExternal = resource.font.FontFile == ""
			break
		}
	}
	if data == nil && e.source != nil && !createdExternal {
		if e.fontRenderer == nil {
			e.fontRenderer = e.newRenderer(e.source.reader)
		}
		resolved, err := e.fontRenderer.ResolveFont(id, true)
		if err != nil {
			return nil, &EditError{Code: EditFontUnavailable, Err: err}
		}
		data = resolved.Data
	}
	if data == nil && createdExternal {
		reader, err := e.Reader()
		if err != nil {
			return nil, &EditError{Code: EditFontUnavailable, Err: err}
		}
		resolved, err := e.newRenderer(reader).ResolveFont(id, false)
		if err != nil {
			return nil, &EditError{Code: EditFontUnavailable, Err: err}
		}
		data = resolved.Data
	}
	if data == nil {
		return nil, &EditError{Code: EditFontUnavailable, Err: fmt.Errorf("font %q not found", id)}
	}
	metrics, err := e.backends.Fonts.OpenFont(data)
	if err != nil {
		return nil, &EditError{Code: EditFontUnavailable, Err: err}
	}
	e.fontMetrics[id] = metrics
	return metrics, nil
}

// FontData 获取编辑使用的字体数据，外部字体不写入文档资源
// 入参: id 字体资源标识
// 返回: []byte 独立字体数据, error 错误信息
func (e *Editor) FontData(id string) ([]byte, error) {
	for _, resource := range e.resources {
		if resource.font != nil && resource.font.ID == id && len(resource.data) != 0 {
			return bytes.Clone(resource.data), nil
		}
	}
	if e.source != nil {
		if definition := e.source.reader.fontCache[id]; definition != nil && definition.FontFile != "" {
			return e.source.reader.FontData(id)
		}
	}
	sfnt, err := e.editorFont(id)
	if err != nil {
		return nil, err
	}
	return sfnt.Write(), nil
}

// editorTextMeasurable 判断原字形能否在不使用回退字体的情况下度量
// 入参: text 原文字对象
// 返回: bool 是否可准确度量
func (e *Editor) editorTextMeasurable(text TextObject) bool {
	sfnt, err := e.editorFont(text.Font)
	if err != nil {
		return false
	}
	transforms := make(map[int]CGTransform, len(text.CGTransform))
	for _, transform := range text.CGTransform {
		transforms[transform.CodePosition] = transform
	}
	offset := 0
	for _, code := range text.TextCode {
		runes := textCodeRunes(code.Value)
		for i := 0; i < len(runes); {
			if transform, ok := transforms[offset+i]; ok {
				ids := parseInts(transform.Glyphs)
				count := max(1, transform.CodeCount)
				if len(ids) == 0 || count > len(runes)-i {
					return false
				}
				for _, id := range ids {
					if id <= 0 || id >= int(sfnt.NumGlyphs()) {
						return false
					}
				}
				i += count
			} else {
				if sfnt.GlyphIndex(runes[i]) == 0 {
					return false
				}
				i++
			}
		}
		offset += len(runes)
	}
	return true
}

// editorImage 按需读取原图片尺寸，不解码完整像素
// 入参: id 图片资源标识
// 返回: image.Point 图片尺寸, error 错误信息
func (e *Editor) editorImage(id string) (image.Point, error) {
	if size := e.images[id]; size.X > 0 {
		return size, nil
	}
	if e.source == nil {
		return image.Point{}, fmt.Errorf("image resource %q not found", id)
	}
	reader := e.source.reader
	input, err := reader.openFile(reader.ResPath(reader.ResMap[id]))
	if err != nil {
		return image.Point{}, err
	}
	defer input.Close()
	config, _, err := image.DecodeConfig(input)
	if err != nil {
		return image.Point{}, err
	}
	size := image.Pt(config.Width, config.Height)
	e.images[id] = size
	return size, nil
}

// cloneEditorData 深复制编辑快照，未导出的排版及原文来源保持不可变共享
// 入参: value 原值
// 返回: T 独立副本
func cloneEditorData[T any](value T) T {
	return cloneEditorValue(reflect.ValueOf(value)).Interface().(T)
}

// cloneEditorValue 复制结构体中的可变指针和切片
// 入参: value 原值
// 返回: reflect.Value 独立副本
func cloneEditorValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			result := reflect.New(value.Type().Elem())
			result.Elem().Set(cloneEditorValue(value.Elem()))
			return result
		}
	case reflect.Slice:
		if !value.IsNil() {
			result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			for i := range value.Len() {
				result.Index(i).Set(cloneEditorValue(value.Index(i)))
			}
			return result
		}
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for i := range value.NumField() {
			if result.Field(i).CanSet() && value.Type().Field(i).IsExported() {
				result.Field(i).Set(cloneEditorValue(value.Field(i)))
			}
		}
		return result
	}
	return value
}

// editorObjectXML 编码可编辑对象，包含独立命名空间声明
// 入参: object 对象
// 返回: []byte XML片段, error 错误信息
func editorObjectXML(object GraphicObject) ([]byte, error) {
	data, err := encodeOFDXML(func(x *ofdXML) { x.object(object, true) })
	return bytes.TrimPrefix(data, []byte(xml.Header)), err
}

// validateEditorGeometry 校验几何操作产生的数值，不重新排版或替换字体
// 入参: object 变换后的对象
// 返回: error 错误信息
func validateEditorGeometry(object GraphicObject) error {
	boundary, ctm := editorGeometry(object)
	if _, err := creationBox(boundary); err != nil {
		return err
	}
	if ctm != "" {
		if _, err := creationNumbers(ctm, 6); err != nil {
			return err
		}
	}
	var alpha *int
	switch object.Type {
	case "TextObject":
		alpha = object.TextObject.Alpha
	case "PathObject":
		alpha = object.PathObject.Alpha
	case "ImageObject":
		alpha = object.ImageObject.Alpha
	case "CompositeObject", "CompositeGraphicUnit":
		alpha = object.CompositeGraphicUnit.Alpha
	}
	if alpha != nil && (*alpha < 0 || *alpha > 255) {
		return fmt.Errorf("alpha must be between 0 and 255")
	}
	if object.Type == "TextObject" {
		obj := object.TextObject
		if !finite(obj.Size) || obj.Size <= 0 || !finite(obj.LineWidth) || obj.LineWidth < 0 {
			return fmt.Errorf("invalid text geometry")
		}
		for _, code := range obj.TextCode {
			for _, value := range []string{code.X, code.Y} {
				if value != "" {
					if _, err := creationNumbers(value, 1); err != nil {
						return err
					}
				}
			}
			for _, value := range []string{code.DeltaX, code.DeltaY} {
				if value != "" {
					if err := creationDeltas(value); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
