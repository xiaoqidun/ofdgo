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
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/tdewolff/font"
)

// editorSource 保留输入包及按需加载的原始页面
type editorSource struct {
	reader    *Reader
	document  *Document
	info      DocInfo
	directory string
	pages     map[string]*editorSourcePage
	origins   map[string]*editorObjectOrigin
	idsReady  bool
}

// editorObjectOrigin 保留原对象的XML语义，供复制和局部更新复用
type editorObjectOrigin struct {
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
}

// ObjectCapabilities 已有对象可执行的操作，Reason说明受限原因
// Update表示完整替换内容，ReplaceFont表示可显式替换文字字体，Transform表示保留文字定位的几何变换，Order仅限同一图层或末级页块
// Arrange表示可准确度量对齐与旋转范围；Reflow表示可重排文字，LayoutKnown表示段落选项可恢复
// Paint表示可独立修改纯色填充和描边，不要求重新排版文字
// MissingGlyphs提供缺字导致操作受限时的结构化诊断
type ObjectCapabilities struct {
	Update        bool
	Paint         bool
	ReplaceFont   bool
	Reflow        bool
	LayoutKnown   bool
	Transform     bool
	Arrange       bool
	Copy          bool
	Delete        bool
	Order         bool
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
	if r.Zip != nil {
		seen := make(map[string]bool)
		for _, file := range r.Zip.File {
			name := cleanPackagePath(file.Name)
			if seen[name] {
				return nil, fmt.Errorf("ambiguous package entry %q", file.Name)
			}
			seen[name] = true
		}
	}
	reader := &Reader{Zip: r.Zip, files: r.files}
	if err := reader.initRoot(); err != nil {
		return nil, err
	}
	doc, err := reader.Doc()
	if err != nil {
		return nil, err
	}
	maximum := doc.CommonData.MaxUnitID
	if maximum < 0 {
		return nil, fmt.Errorf("invalid MaxUnitID")
	}
	info, err := reader.DocInfo()
	if err != nil {
		return nil, err
	}
	e := NewEditor()
	e.Info, e.maxID = cloneEditorData(*info), maximum
	directory := path.Join(cleanPackagePath(reader.RootDir), "Edit")
	for i := 1; editorDirectoryExists(reader, directory); i++ {
		directory = path.Join(cleanPackagePath(reader.RootDir), "Edit_"+strconv.Itoa(i))
	}
	e.source = &editorSource{reader: reader, document: doc, info: cloneEditorData(*info), directory: directory, pages: make(map[string]*editorSourcePage), origins: make(map[string]*editorObjectOrigin)}
	for _, page := range doc.Pages.Page {
		if page.ID == "" || e.source.pages[page.ID] != nil {
			return nil, fmt.Errorf("missing or duplicate page ID %q", page.ID)
		}
		e.source.pages[page.ID] = &editorSourcePage{ref: page}
		e.pages = append(e.pages, PageContent{ID: page.ID})
	}
	return e, nil
}

// editorDirectoryExists 判断包内目录是否已经使用，避免覆盖原始资源
// 入参: reader 输入包, directory 目录路径
// 返回: bool 是否存在
func editorDirectoryExists(reader *Reader, directory string) bool {
	for name := range reader.fileIndex {
		if strings.EqualFold(name, directory) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(directory)+"/") {
			return true
		}
	}
	for name := range reader.files {
		if strings.EqualFold(name, directory) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(directory)+"/") {
			return true
		}
	}
	return false
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

// resourceDirectory 获取新增二进制资源的目录
// 返回: string 包内路径
func (e *Editor) resourceDirectory() string {
	if e.source != nil {
		return e.source.directory + "/Res"
	}
	return "Doc_0/Res"
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

// loadSourcePage 首次访问时解析原始页面，不加载图片或字体二进制数据
// 入参: index 页面索引
// 返回: error 错误信息
func (e *Editor) loadSourcePage(index int) error {
	source := e.source.pages[e.pages[index].ID]
	if source == nil || source.original != nil {
		return nil
	}
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
	object := layer.Objects[index]
	all := ObjectCapabilities{Update: true, Paint: object.Type == "TextObject" || object.Type == "PathObject", ReplaceFont: object.Type == "TextObject", Reflow: object.Type == "TextObject" && object.TextObject.ReadDirection == 0 && object.TextObject.CharDirection == 0, LayoutKnown: object.Type == "TextObject" && (e.objectOrigin(id) == nil || object.TextObject.layout != nil), Transform: true, Arrange: true, Copy: true, Delete: true, Order: true}
	origin := e.objectOrigin(id)
	if origin == nil {
		return all, nil
	}
	node := origin.node
	if !editorXMLTransformable(node) {
		return ObjectCapabilities{Reason: "object uses unsupported editing features", ReasonCode: EditUnsupportedObject}, nil
	}
	if origin.reason != nil && editReason(origin.reason) == EditUnsupportedStyle {
		return ObjectCapabilities{Reason: origin.reason.Error(), ReasonCode: EditUnsupportedStyle}, nil
	}
	if !editorXMLContainersSupported(node) {
		return ObjectCapabilities{Reason: "object container uses unsupported editing features", ReasonCode: EditUnsupportedContainer}, nil
	}
	if err := validateEditorGeometry(object); err != nil {
		return ObjectCapabilities{Reason: err.Error(), ReasonCode: EditInvalidObject}, nil
	}
	all.Order = editorContainerOrderable(node.parent)
	all.Copy = editorXMLCopyable(node) && origin.reason == nil
	if origin.reason != nil {
		all.Update, all.Reflow, all.ReplaceFont, all.Paint = false, false, false, false
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
	if object.Type == "ImageObject" && object.ImageObject.Border != nil && len(object.ImageObject.Actions) != 0 {
		all.Transform, all.Arrange = false, false
		all.Reason, all.ReasonCode = "image actions cannot be transformed together with the border", EditUnsupportedObject
	}
	if object.Type == "TextObject" {
		all.Arrange = all.Update || e.editorTextMeasurable(object.TextObject)
		if _, err := object.TextObject.TextFrame(); err != nil {
			all.Reflow = false
		}
	}
	return all, nil
}

// objectOrigin 获取原对象或其副本的保真来源
// 入参: id 对象标识
// 返回: *editorObjectOrigin 原始对象，无来源时为nil
func (e *Editor) objectOrigin(id string) *editorObjectOrigin {
	if e.source == nil {
		return nil
	}
	return e.source.origins[id]
}

// editorPreservedObject 判断对象是否仅改变标识、几何和可独立写回的外观字段
// 入参: before 原始快照, after 新快照
// 返回: bool 是否保留原内容
func editorPreservedObject(before, after GraphicObject) bool {
	if before.Type != after.Type {
		return false
	}
	before = cloneEditorData(before)
	switch before.Type {
	case "TextObject":
		a, b := &before.TextObject, after.TextObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
		a.Fill, a.Stroke, a.FillColor, a.StrokeColor, a.LineWidth = b.Fill, b.Stroke, b.FillColor, b.StrokeColor, b.LineWidth
	case "PathObject":
		a, b := &before.PathObject, after.PathObject
		a.ID, a.Boundary, a.CTM, a.Alpha = b.ID, b.Boundary, b.CTM, b.Alpha
		a.Fill, a.Stroke, a.FillColor, a.StrokeColor, a.LineWidth = b.Fill, b.Stroke, b.FillColor, b.StrokeColor, b.LineWidth
		a.DashOffset, a.DashPattern, a.Cap, a.Join = b.DashOffset, b.DashPattern, b.Cap, b.Join
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
	object.origin = nil
	if origin == nil {
		return e.prepareObject(id, object)
	}
	if !editorXMLTransformable(origin.node) || !editorXMLContainersSupported(origin.node) || !editorXMLCopyable(origin.node) || origin.reason != nil {
		return GraphicObject{}, fmt.Errorf("object cannot be copied without changing its original structure")
	}
	var drawParam string
	for parent := origin.node.parent; parent != nil; parent = parent.parent {
		if parent.name.Local == "Layer" {
			drawParam = parent.attr("DrawParam")
			break
		}
	}
	before, err := e.resolveEditorStyle(origin.object, drawParam)
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

// editorFont 按需解析原文档内嵌字体，不用系统回退替换原字体
// 入参: id 字体资源标识
// 返回: *font.SFNT 字体, error 错误信息
func (e *Editor) editorFont(id string) (*font.SFNT, error) {
	if sfnt := e.fonts[id]; sfnt != nil {
		return sfnt, nil
	}
	if e.source != nil {
		data, err := e.source.reader.FontData(id)
		if err != nil {
			return nil, &EditError{Code: EditFontUnavailable, Err: err}
		}
		sfnt, err := font.ParseSFNT(data, 0)
		if err != nil {
			return nil, &EditError{Code: EditFontUnavailable, Err: err}
		}
		e.fonts[id] = sfnt
		return sfnt, nil
	}
	return nil, &EditError{Code: EditFontUnavailable, Err: fmt.Errorf("embedded font %q not found", id)}
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
					if err := creationDeltas(value, len(textCodeRunes(code.Value))-1); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
