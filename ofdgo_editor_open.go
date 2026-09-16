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
	"image"
	"io"
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/tdewolff/font"
)

// editorSource 保留输入包及按需加载的原始页面。
type editorSource struct {
	reader    *Reader
	document  *Document
	info      DocInfo
	directory string
	pages     map[string]*editorSourcePage
	origins   map[string]*editorObjectOrigin
	idsReady  bool
}

// editorObjectOrigin 保留原对象的XML语义，供复制和局部更新复用。
type editorObjectOrigin struct {
	page   *editorSourcePage
	node   *editorXML
	object GraphicObject
}

// editorSourcePage 保存页面原文、解析快照及可定位的直接对象。
type editorSourcePage struct {
	ref      Page
	data     []byte
	root     *editorXML
	original *PageContent
	nodes    map[string]*editorXML
}

// ObjectCapabilities 已有对象可执行的操作，Reason说明受限原因。
// Update表示完整替换内容，Transform表示保留文字定位的几何变换，Order仅限当前图层。
// Arrange表示可准确度量对齐与旋转范围；Reflow表示可重排文字，LayoutKnown表示段落选项可恢复。
type ObjectCapabilities struct {
	Update      bool
	Reflow      bool
	LayoutKnown bool
	Transform   bool
	Arrange     bool
	Copy        bool
	Delete      bool
	Order       bool
	Reason      string
}

// PageCapabilities 页面可执行的操作，Insert表示可以添加RGB对象；新增空白页面始终可用。
type PageCapabilities struct {
	Insert bool
	Copy   bool
	Delete bool
	Move   bool
	Resize bool
}

// Editor 打开主文档进行保真编辑，不修改输入包，不以权限声明或签名限制编辑。
// 原权限和签名数据保留，修改受保护内容会使原签名失效，不重新签名。
// 页面按需解析，未修改条目直接保留；编辑器及其Reader快照使用期间不得关闭输入Reader。
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
	directory := path.Join(reader.RootDir, "Edit")
	for i := 1; editorDirectoryExists(reader, directory); i++ {
		directory = path.Join(reader.RootDir, "Edit_"+strconv.Itoa(i))
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

// editorDirectoryExists 判断包内目录是否已经使用，避免覆盖原始资源。
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

// prepareSourceIDs 首次新增内容前流式核对原包标识，不依赖可能缺失或过期的MaxUnitID。
// 返回: error 错误信息
func (e *Editor) prepareSourceIDs() error {
	if e.source == nil || e.source.idsReady {
		return nil
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
			return err
		}
		id, err := editorXMLMaxID(input)
		input.Close()
		if err != nil {
			return fmt.Errorf("scan IDs in %s: %w", name, err)
		}
		maximum = max(maximum, id)
	}
	e.maxID, e.source.idsReady = maximum, true
	return nil
}

// editorXMLMaxID 流式读取OFD结构中的最大标识，跳过非OFD的XML附件。
// 入参: input XML输入流
// 返回: int 最大标识, error 错误信息
func editorXMLMaxID(input io.Reader) (int, error) {
	decoder := xml.NewDecoder(input)
	maximum, started := 0, false
	for {
		token, err := decoder.Token()
		if err == io.EOF || err != nil && !started {
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

// resourceDirectory 获取新增二进制资源的目录。
// 返回: string 包内路径
func (e *Editor) resourceDirectory() string {
	if e.source != nil {
		return e.source.directory + "/Res"
	}
	return "Doc_0/Res"
}

// originalPage 判断索引是否指向原文档页面。
// 入参: index 页面索引
// 返回: bool 是否为原始页面
func (e *Editor) originalPage(index int) bool {
	return e.source != nil && index >= 0 && index < len(e.pages) && e.source.pages[e.pages[index].ID] != nil
}

// PageCapabilities 获取页面操作范围；原始页面暂不复制、删除，避免破坏模板与外部引用。
// 入参: index 页面索引
// 返回: PageCapabilities 操作能力, error 错误信息
func (e *Editor) PageCapabilities(index int) (PageCapabilities, error) {
	if index < 0 || index >= len(e.pages) {
		return PageCapabilities{}, fmt.Errorf("page index %d out of range", index)
	}
	original := e.originalPage(index)
	return PageCapabilities{Insert: e.sourceRGB(), Copy: !original, Delete: !original, Move: true, Resize: true}, nil
}

// sourceRGB 判断新建RGB对象能否直接使用文档默认颜色空间。
// 返回: bool 是否为8位RGB文档
func (e *Editor) sourceRGB() bool {
	if e.source == nil || e.source.document.CommonData.DefaultCS == 0 {
		return true
	}
	space := e.source.reader.colorSpaceCache[strconv.Itoa(e.source.document.CommonData.DefaultCS)]
	return space != nil && space.Type == "RGB" && (space.BitsPerComponent == 0 || space.BitsPerComponent == 8) && len(space.Palette) == 0
}

// loadSourcePage 首次访问时解析原始页面，不加载图片或字体二进制数据。
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
			for _, node := range layer.children {
				id := node.attr("ID")
				if id != "" {
					if nodes[id] != nil {
						return fmt.Errorf("duplicate object ID %q", id)
					}
					nodes[id] = node
				}
			}
		}
	}
	source.data, source.root, source.nodes = data, root, nodes
	source.original = page
	for _, layer := range page.Content.Layer {
		for _, object := range layer.Objects {
			id := editorObjectID(object)
			if node := nodes[id]; node != nil {
				e.source.origins[id] = &editorObjectOrigin{page: source, node: node, object: object}
			}
		}
	}
	e.pages[index] = copyEditorPage(*page)
	return nil
}

// ObjectCapabilities 获取对象可执行的操作，不支持的内容保持原文，不强制转换。
// 字体缺失或子集不包含原文时仍可移动、删除，不允许重新排版或复制。
// 显式提供可用字体后可通过UpdateObject替换；原文布局未知时不自动推断段落选项。
// 入参: page 页面索引, id 对象标识
// 返回: ObjectCapabilities 操作能力, error 错误信息
func (e *Editor) ObjectCapabilities(page int, id string) (ObjectCapabilities, error) {
	layer, index, err := e.findObject(page, id)
	if err != nil {
		return ObjectCapabilities{}, err
	}
	object := layer.Objects[index]
	all := ObjectCapabilities{Update: true, Reflow: object.Type == "TextObject" && object.TextObject.ReadDirection == 0 && object.TextObject.CharDirection == 0, LayoutKnown: object.Type == "TextObject" && (e.objectOrigin(id) == nil || object.TextObject.layout != nil), Transform: true, Arrange: true, Copy: true, Delete: true, Order: true}
	if !e.originalPage(page) {
		return all, nil
	}
	source := e.source.pages[e.pages[page].ID]
	node := source.nodes[id]
	if node == nil {
		for _, original := range source.original.Content.Layer {
			for _, object := range original.Objects {
				if editorObjectID(object) == id {
					return ObjectCapabilities{Reason: "nested objects are read-only"}, nil
				}
			}
		}
		return all, nil
	}
	if layer.DrawParam != "" || !editorXMLSupported(node) {
		return ObjectCapabilities{Reason: "object uses unsupported editing features"}, nil
	}
	for _, attr := range node.parent.attrs {
		if attr.Name.Space == "xmlns" || attr.Name.Space == "" && attr.Name.Local == "xmlns" {
			continue
		}
		if attr.Name.Space != "" || attr.Name.Local != "ID" && attr.Name.Local != "Type" && attr.Name.Local != "DrawParam" {
			return ObjectCapabilities{Reason: "layer uses unsupported editing features"}, nil
		}
	}
	if err := validateEditorGeometry(object); err != nil {
		return ObjectCapabilities{Reason: err.Error()}, nil
	}
	all.Order = e.sourceLayerOrderable(page, layer.ID)
	if _, err := e.prepareObject(id, object); err != nil {
		all.Update, all.Reflow, all.Copy, all.Reason = false, false, false, err.Error()
	}
	if !e.sourceRGB() {
		all.Update, all.Reflow, all.Copy, all.Reason = false, false, false, "document uses a non-RGB default color space"
	}
	if object.Type == "TextObject" {
		all.Arrange = all.Update
		if _, err := object.TextObject.TextFrame(); err != nil {
			all.Reflow = false
		}
	}
	return all, nil
}

// objectOrigin 获取原对象或其副本的保真来源。
// 入参: id 对象标识
// 返回: *editorObjectOrigin 原始对象，无来源时为nil
func (e *Editor) objectOrigin(id string) *editorObjectOrigin {
	if e.source == nil {
		return nil
	}
	return e.source.origins[id]
}

// sourceLayerOrderable 判断图层是否仅含可独立排序的直接对象。
// 入参: page 页面索引, id 图层标识
// 返回: bool 是否可排序
func (e *Editor) sourceLayerOrderable(page int, id string) bool {
	if !e.originalPage(page) {
		return true
	}
	content := e.source.pages[e.pages[page].ID].root.child("Content")
	if content != nil {
		for _, layer := range content.children {
			if layer.attr("ID") == id {
				for _, child := range layer.children {
					if !editorXMLSupported(child) {
						return false
					}
				}
			}
		}
	}
	return true
}

// editorGeometry 获取基本对象的边界与变换。
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
	}
	return "", ""
}

// editorFont 按需解析原文档内嵌字体，不用系统回退替换原字体。
// 入参: id 字体资源标识
// 返回: *font.SFNT 字体, error 错误信息
func (e *Editor) editorFont(id string) (*font.SFNT, error) {
	if sfnt := e.fonts[id]; sfnt != nil {
		return sfnt, nil
	}
	if e.source != nil {
		reader := e.source.reader
		if f := reader.fontCache[id]; f != nil && f.FontFile != "" {
			data, err := reader.ResData(f.FontFile)
			if err != nil {
				return nil, err
			}
			data, err = font.ToSFNT(data)
			if err != nil {
				return nil, err
			}
			sfnt, err := font.ParseSFNT(data, 0)
			if err != nil {
				return nil, err
			}
			e.fonts[id] = sfnt
			return sfnt, nil
		}
	}
	return nil, fmt.Errorf("embedded font %q not found", id)
}

// editorImage 按需读取原图片尺寸，不解码完整像素。
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

// cloneEditorData 深复制编辑快照，未导出的排版信息保持不可变共享。
// 入参: value 原值
// 返回: T 独立副本
func cloneEditorData[T any](value T) T {
	return cloneEditorValue(reflect.ValueOf(value)).Interface().(T)
}

// cloneEditorValue 复制结构体中的可变指针和切片。
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

// editorObjectXML 编码可编辑对象，包含独立命名空间声明。
// 入参: object 对象
// 返回: []byte XML片段, error 错误信息
func editorObjectXML(object GraphicObject) ([]byte, error) {
	data, err := encodeOFDXML(func(x *ofdXML) { x.object(object, true) })
	return bytes.TrimPrefix(data, []byte(xml.Header)), err
}

// validateEditorGeometry 校验几何操作产生的数值，不重新排版或替换字体。
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
	if object.Type == "TextObject" {
		obj := object.TextObject
		if !finite(obj.Size) || obj.Size <= 0 || !finite(obj.LineWidth) {
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
