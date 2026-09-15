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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"image"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tdewolff/font"
)

// Editor 新建OFD文档，长度单位为毫米，页面索引从0开始，实例需串行使用
// Info可修改文档元数据，通过方法管理页面、对象和资源，不修改已有OFD文件
type Editor struct {
	Info         DocInfo
	pages        []PageContent
	resources    []editorResource
	fonts        map[string]*font.SFNT
	images       map[string]bool
	resourceID   map[editorResourceKey]string
	maxID        int
	history      []editorChange
	historyIndex int
	historyLimit int
	revision     uint64
	serial       uint64
}

// editorResource 文档内嵌资源
type editorResource struct {
	name  string
	data  []byte
	font  *Font
	image *MultiMedia
}

// editorResourceKey 资源内容和字体集合索引
type editorResourceKey struct {
	checksum [32]byte
	index    int
}

// NewEditor 创建空白文档，保存前至少添加一页
// 返回: *Editor 文档编辑器
func NewEditor() *Editor {
	var id [16]byte
	rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return &Editor{
		Info: DocInfo{
			DocID:        hex.EncodeToString(id[:]),
			CreationDate: time.Now().Format("2006-01-02"),
		},
		fonts:      make(map[string]*font.SFNT),
		images:     make(map[string]bool),
		resourceID: make(map[editorResourceKey]string),
	}
}

// PageCount 获取当前页数
// 返回: int 页数
func (e *Editor) PageCount() int {
	return len(e.pages)
}

// Page 获取页面的独立副本，修改副本不影响文档
// 入参: index 页面索引
// 返回: *PageContent 页面内容, error 错误信息
func (e *Editor) Page(index int) (*PageContent, error) {
	page, err := e.page(index)
	if err != nil {
		return nil, err
	}
	data, err := encodeOFDXML(func(x *ofdXML) { x.page(*page) })
	if err != nil {
		return nil, err
	}
	var result PageContent
	if err := xml.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	result.ID = page.ID
	return &result, nil
}

// page 按索引获取内部页面
// 入参: index 页面索引
// 返回: *PageContent 页面内容, error 错误信息
func (e *Editor) page(index int) (*PageContent, error) {
	if index < 0 || index >= len(e.pages) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	return &e.pages[index], nil
}

// AddPage 添加页面及默认正文图层
// 入参: width 页面宽度, height 页面高度
// 返回: int 页面索引, error 错误信息
func (e *Editor) AddPage(width, height float64) (int, error) {
	if !finite(width) || !finite(height) || width <= 0 || height <= 0 {
		return 0, fmt.Errorf("page dimensions must be finite and positive")
	}
	e.pages = append(e.pages, PageContent{
		ID:      e.nextID(),
		Area:    PageArea{PhysicalBox: fmt.Sprintf("0 0 %s %s", ofdNumber(width), ofdNumber(height))},
		Content: Content{Layer: []Layer{{ID: e.nextID(), Type: "Body"}}},
	})
	index := len(e.pages) - 1
	e.recordPageAddition(index)
	return index, nil
}

// CopyPage 复制页面并追加到文档末尾，分配新标识，复用字体和图片资源
// 副本可独立修改，位置通过MovePage调整
// 入参: index 原页面索引
// 返回: int 副本页面索引, error 错误信息
func (e *Editor) CopyPage(index int) (int, error) {
	source, err := e.page(index)
	if err != nil {
		return 0, err
	}
	page := PageContent{Area: source.Area, Content: Content{Layer: make([]Layer, len(source.Content.Layer))}}
	for i, layer := range source.Content.Layer {
		page.Content.Layer[i] = Layer{Type: layer.Type, Objects: make([]GraphicObject, len(layer.Objects))}
		for j, object := range layer.Objects {
			page.Content.Layer[i].Objects[j], err = cloneEditorObject(object)
			if err != nil {
				return 0, err
			}
		}
	}
	page.ID = e.nextID()
	for i := range page.Content.Layer {
		layer := &page.Content.Layer[i]
		layer.ID = e.nextID()
		for j := range layer.Objects {
			object := &layer.Objects[j]
			id := e.nextID()
			switch object.Type {
			case "TextObject":
				object.TextObject.ID = id
			case "PathObject":
				object.PathObject.ID = id
			case "ImageObject":
				object.ImageObject.ID = id
			}
		}
	}
	e.pages = append(e.pages, page)
	index = len(e.pages) - 1
	e.recordPageAddition(index)
	return index, nil
}

// DeletePage 删除页面，不回收资源或复用标识，删除后页面索引随之变化
// 入参: index 页面索引
// 返回: error 错误信息
func (e *Editor) DeletePage(index int) error {
	page, err := e.page(index)
	if err != nil {
		return err
	}
	if change := e.recordChange(); change != nil {
		saved := copyEditorPage(*page)
		change.undo = func(e *Editor) { e.pages = slices.Insert(e.pages, index, copyEditorPage(saved)) }
		change.redo = func(e *Editor) { e.pages = slices.Delete(e.pages, index, index+1) }
	}
	e.pages = slices.Delete(e.pages, index, index+1)
	return nil
}

// MovePage 移动页面，保留页面及对象标识
// 入参: from 原页面索引, to 移动后的页面索引
// 返回: error 错误信息
func (e *Editor) MovePage(from, to int) error {
	if _, err := e.page(from); err != nil {
		return err
	}
	if _, err := e.page(to); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	moveEditorItem(e.pages, from, to)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { moveEditorItem(e.pages, to, from) }
		change.redo = func(e *Editor) { moveEditorItem(e.pages, from, to) }
	}
	return nil
}

// ResizePage 调整页面尺寸，不缩放或移动页面内的对象
// 入参: index 页面索引, width 页面宽度, height 页面高度
// 返回: error 错误信息
func (e *Editor) ResizePage(index int, width, height float64) error {
	page, err := e.page(index)
	if err != nil {
		return err
	}
	if !finite(width) || !finite(height) || width <= 0 || height <= 0 {
		return fmt.Errorf("page dimensions must be finite and positive")
	}
	before, after := page.Area.PhysicalBox, fmt.Sprintf("0 0 %s %s", ofdNumber(width), ofdNumber(height))
	if before == after {
		return nil
	}
	page.Area.PhysicalBox = after
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.pages[index].Area.PhysicalBox = before }
		change.redo = func(e *Editor) { e.pages[index].Area.PhysicalBox = after }
	}
	return nil
}

// nextID 分配文档内唯一标识
// 返回: string 对象标识
func (e *Editor) nextID() string {
	e.maxID++
	return strconv.Itoa(e.maxID)
}

// AddFont 注册OpenType字体，集合字体按索引提取，重复资源复用标识，引用后写入文档
// 入参: file 字体文件, index 集合内字体索引，单字体为0
// 返回: string 字体资源标识, error 错误信息
func (e *Editor) AddFont(file FontFile, index int) (string, error) {
	if index < 0 {
		return "", fmt.Errorf("font index must not be negative")
	}
	data, err := font.ToSFNT(file.Data)
	if err != nil {
		return "", err
	}
	key := editorResourceKey{checksum: sha256.Sum256(data), index: index}
	if id, ok := e.resourceID[key]; ok {
		return id, nil
	}
	if bytes.HasPrefix(data, []byte("ttcf")) {
		data, err = extractCollectionFont(data, index)
		if err != nil {
			return "", err
		}
		index = 0
	} else {
		data = bytes.Clone(data)
	}
	sfnt, err := font.ParseSFNT(data, index)
	if err != nil {
		return "", err
	}
	name := editorFontName(sfnt, font.NamePostScript, font.NameFull, font.NameFontFamily)
	if name == "" {
		return "", fmt.Errorf("font has no name")
	}
	id := e.nextID()
	extension := ".ttf"
	if sfnt.IsCFF {
		extension = ".otf"
	}
	resource := editorResource{
		name: "Doc_0/Res/Font_" + id + extension,
		data: data,
		font: &Font{
			ID: id, FontName: name,
			FamilyName: editorFontName(sfnt, font.NamePreferredFamily, font.NameFontFamily),
			Charset:    "unicode",
			Bold:       sfnt.Head.MacStyle[0],
			Italic:     sfnt.Head.MacStyle[1],
			FixedWidth: sfnt.Post.IsFixedPitch != 0,
		},
	}
	resource.font.FontFile = strings.TrimPrefix(resource.name, "Doc_0/Res/")
	e.resources = append(e.resources, resource)
	e.fonts[id] = sfnt
	e.resourceID[key] = id
	return id, nil
}

// editorFontName 获取字体中的名称
// 入参: sfnt 字体, names 名称类型，按顺序查找
// 返回: string 字体名称
func editorFontName(sfnt *font.SFNT, names ...font.NameID) string {
	for _, name := range names {
		for _, record := range sfnt.Name.Get(name) {
			if value := record.String(); value != "" {
				return value
			}
		}
	}
	return ""
}

// AddImage 注册PNG或JPEG图片，保留原始编码，重复资源复用标识，引用后写入文档
// 入参: data 图片数据
// 返回: string 图片资源标识, error 错误信息
func (e *Editor) AddImage(data []byte) (string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	if (format != "png" && format != "jpeg") || config.Width <= 0 || config.Height <= 0 {
		return "", fmt.Errorf("only PNG and JPEG images are supported")
	}
	key := editorResourceKey{checksum: sha256.Sum256(data), index: -1}
	if id, ok := e.resourceID[key]; ok {
		return id, nil
	}
	id := e.nextID()
	extension := format
	if extension == "jpeg" {
		extension = "jpg"
	}
	name := "Image_" + id + "." + extension
	e.resources = append(e.resources, editorResource{
		name:  "Doc_0/Res/" + name,
		data:  bytes.Clone(data),
		image: &MultiMedia{ID: id, Type: "Image", Format: strings.ToUpper(format), MediaFile: name},
	})
	e.images[id] = true
	e.resourceID[key] = id
	return id, nil
}

// AddObject 按添加顺序放入正文图层，支持文字、路径和图片，自动分配对象ID
// 首版支持基本颜色和直接资源引用，不支持动作、裁剪、渐变及复合图元
// 入参: page 页面索引, object 对象内容，添加后不再引用调用方的可变数据
// 返回: string 对象标识, error 错误信息
func (e *Editor) AddObject(page int, object GraphicObject) (string, error) {
	content, err := e.page(page)
	if err != nil {
		return "", err
	}
	id := strconv.Itoa(e.maxID + 1)
	object, err = e.prepareObject(id, object)
	if err != nil {
		return "", err
	}
	e.maxID++
	content.Content.Layer[0].Objects = append(content.Content.Layer[0].Objects, object)
	if change := e.recordChange(); change != nil {
		index := len(content.Content.Layer[0].Objects) - 1
		change.undo = func(e *Editor) {
			layer := &e.pages[page].Content.Layer[0]
			layer.Objects = slices.Delete(layer.Objects, index, index+1)
		}
		change.redo = func(e *Editor) {
			layer := &e.pages[page].Content.Layer[0]
			layer.Objects = slices.Insert(layer.Objects, index, object)
		}
	}
	return id, nil
}

// Object 获取对象的独立副本，修改副本不影响文档
// 入参: page 页面索引, id 对象标识
// 返回: GraphicObject 对象内容, error 错误信息
func (e *Editor) Object(page int, id string) (GraphicObject, error) {
	layer, index, err := e.findObject(page, id)
	if err != nil {
		return GraphicObject{}, err
	}
	return cloneEditorObject(layer.Objects[index])
}

// UpdateObject 替换对象内容，保留原对象标识和绘制顺序，校验规则与AddObject相同
// 入参: page 页面索引, id 对象标识, object 新内容，忽略其ID且不引用调用方的可变数据
// 返回: error 错误信息
func (e *Editor) UpdateObject(page int, id string, object GraphicObject) error {
	_, index, err := e.findObject(page, id)
	if err != nil {
		return err
	}
	object, err = e.prepareObject(id, object)
	if err != nil {
		return err
	}
	e.replaceObject(page, index, object)
	return nil
}

// DeleteObject 删除对象，不回收资源或复用标识
// 入参: page 页面索引, id 对象标识
// 返回: error 错误信息
func (e *Editor) DeleteObject(page int, id string) error {
	layer, index, err := e.findObject(page, id)
	if err != nil {
		return err
	}
	if change := e.recordChange(); change != nil {
		object := layer.Objects[index]
		change.undo = func(e *Editor) {
			layer := &e.pages[page].Content.Layer[0]
			layer.Objects = slices.Insert(layer.Objects, index, object)
		}
		change.redo = func(e *Editor) {
			layer := &e.pages[page].Content.Layer[0]
			layer.Objects = slices.Delete(layer.Objects, index, index+1)
		}
	}
	layer.Objects = slices.Delete(layer.Objects, index, index+1)
	return nil
}

// MoveObject 调整同页正文图层内的绘制顺序，保留对象标识和内容
// 入参: page 页面索引, id 对象标识, to 移动后的对象索引，0为最底层，末尾为最顶层
// 返回: error 错误信息
func (e *Editor) MoveObject(page int, id string, to int) error {
	layer, from, err := e.findObject(page, id)
	if err != nil {
		return err
	}
	if to < 0 || to >= len(layer.Objects) {
		return fmt.Errorf("object index %d out of range", to)
	}
	if from == to {
		return nil
	}
	moveEditorItem(layer.Objects, from, to)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { moveEditorItem(e.pages[page].Content.Layer[0].Objects, to, from) }
		change.redo = func(e *Editor) { moveEditorItem(e.pages[page].Content.Layer[0].Objects, from, to) }
	}
	return nil
}

// replaceObject 提交已校验的对象并记录前后内容
// 入参: page 页面索引, index 对象索引, object 新内容
func (e *Editor) replaceObject(page, index int, object GraphicObject) {
	layer := &e.pages[page].Content.Layer[0]
	before := layer.Objects[index]
	if reflect.DeepEqual(before, object) {
		return
	}
	layer.Objects[index] = object
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.pages[page].Content.Layer[0].Objects[index] = before }
		change.redo = func(e *Editor) { e.pages[page].Content.Layer[0].Objects[index] = object }
	}
}

// findObject 查找正文图层中的对象
// 入参: page 页面索引, id 对象标识
// 返回: *Layer 所属图层, int 对象索引, error 错误信息
func (e *Editor) findObject(page int, id string) (*Layer, int, error) {
	content, err := e.page(page)
	if err != nil {
		return nil, 0, err
	}
	layer := &content.Content.Layer[0]
	for index, object := range layer.Objects {
		var objectID string
		switch object.Type {
		case "TextObject":
			objectID = object.TextObject.ID
		case "PathObject":
			objectID = object.PathObject.ID
		case "ImageObject":
			objectID = object.ImageObject.ID
		}
		if objectID == id {
			return layer, index, nil
		}
	}
	return nil, 0, fmt.Errorf("object %q not found on page %d", id, page)
}

// prepareObject 校验基本对象并生成独立副本
// 入参: id 对象标识, object 对象内容
// 返回: GraphicObject 对象内容, error 错误信息
func (e *Editor) prepareObject(id string, object GraphicObject) (GraphicObject, error) {
	var boundary, ctm, drawParam, join string
	var alpha *int
	var clips *Clips
	var actions []Action
	var fill, stroke *FillColor
	switch object.Type {
	case "TextObject":
		obj := &object.TextObject
		if err := e.prepareText(obj); err != nil {
			return GraphicObject{}, err
		}
		obj.ID = id
		boundary, ctm, drawParam = obj.Boundary, obj.CTM, obj.DrawParam
		join = obj.Join
		alpha, clips, actions = obj.Alpha, obj.Clips, obj.Actions
		fill, stroke = obj.FillColor, (*FillColor)(obj.StrokeColor)
	case "PathObject":
		obj := &object.PathObject
		if err := creationPath(obj.AbbreviatedData); err != nil {
			return GraphicObject{}, err
		}
		if obj.Rule != "" && obj.Rule != "NonZero" && obj.Rule != "Even-Odd" {
			return GraphicObject{}, fmt.Errorf("invalid fill rule %q", obj.Rule)
		}
		if !finite(obj.LineWidth) || obj.LineWidth < 0 || !finite(obj.MiterLimit) || obj.MiterLimit < 0 {
			return GraphicObject{}, fmt.Errorf("invalid path stroke dimensions")
		}
		if obj.Cap != "" && obj.Cap != "Butt" && obj.Cap != "Round" && obj.Cap != "Square" {
			return GraphicObject{}, fmt.Errorf("invalid line cap %q", obj.Cap)
		}
		if obj.DashOffset != nil && !finite(*obj.DashOffset) {
			return GraphicObject{}, fmt.Errorf("invalid dash offset")
		}
		if obj.DashPattern != "" {
			values, err := creationNumbers(obj.DashPattern, len(strings.Fields(obj.DashPattern)))
			if err != nil {
				return GraphicObject{}, err
			}
			total := 0.0
			for _, value := range values {
				if value < 0 {
					return GraphicObject{}, fmt.Errorf("dash lengths must not be negative")
				}
				total += value
			}
			if total == 0 {
				return GraphicObject{}, fmt.Errorf("dash pattern must have a positive length")
			}
		}
		obj.ID = id
		boundary, ctm, drawParam = obj.Boundary, obj.CTM, obj.DrawParam
		join = obj.Join
		alpha, clips, actions = obj.Alpha, obj.Clips, obj.Actions
		fill, stroke = obj.FillColor, (*FillColor)(obj.StrokeColor)
	case "ImageObject":
		obj := &object.ImageObject
		if !e.images[obj.ResourceID] || (obj.ImageMask != "" && !e.images[obj.ImageMask]) {
			return GraphicObject{}, fmt.Errorf("image resource not found")
		}
		if obj.Border != nil {
			return GraphicObject{}, fmt.Errorf("image borders are not supported for creation")
		}
		obj.ID = id
		boundary, ctm = obj.Boundary, obj.CTM
		alpha, clips, actions = obj.Alpha, obj.Clips, obj.Actions
	default:
		return GraphicObject{}, fmt.Errorf("unsupported object type %q", object.Type)
	}
	box, err := creationBox(boundary)
	if err != nil {
		return GraphicObject{}, err
	}
	if ctm != "" {
		if _, err := creationNumbers(ctm, 6); err != nil {
			return GraphicObject{}, fmt.Errorf("invalid CTM: %w", err)
		}
	} else if object.Type == "ImageObject" {
		object.ImageObject.CTM = fmt.Sprintf("%s 0 0 %s 0 0", ofdNumber(box.W), ofdNumber(box.H))
	}
	if drawParam != "" || clips != nil || len(actions) != 0 {
		return GraphicObject{}, fmt.Errorf("draw parameter references, clips and actions are not supported for creation")
	}
	if alpha != nil && (*alpha < 0 || *alpha > 255) {
		return GraphicObject{}, fmt.Errorf("alpha must be between 0 and 255")
	}
	for _, color := range []*FillColor{fill, stroke} {
		if err := creationColor(color); err != nil {
			return GraphicObject{}, err
		}
	}
	if join != "" && join != "Miter" && join != "Round" && join != "Bevel" {
		return GraphicObject{}, fmt.Errorf("invalid line join %q", join)
	}
	return cloneEditorObject(object)
}

// cloneEditorObject 通过统一序列化规则复制对象
// 入参: object 对象内容
// 返回: GraphicObject 独立副本, error 错误信息
func cloneEditorObject(object GraphicObject) (GraphicObject, error) {
	data, err := encodeOFDXML(func(x *ofdXML) { x.object(object, true) })
	if err != nil {
		return GraphicObject{}, err
	}
	object = GraphicObject{Type: object.Type}
	var target any
	switch object.Type {
	case "TextObject":
		target = &object.TextObject
	case "PathObject":
		target = &object.PathObject
	case "ImageObject":
		target = &object.ImageObject
	}
	if err := xml.Unmarshal(data, target); err != nil {
		return GraphicObject{}, err
	}
	return object, nil
}

// AddText 添加单行文字，使用嵌入字体的字宽定位，不进行段落排版和复杂文字塑形
// 入参: page 页面索引, box 文字边界, value 原文, fontID 字体资源标识, size 字号
// 返回: string 对象标识, error 错误信息
func (e *Editor) AddText(page int, box Box, value, fontID string, size float64) (string, error) {
	object := GraphicObject{Type: "TextObject", TextObject: TextObject{
		Boundary: fmt.Sprintf("%s %s %s %s", ofdNumber(box.X), ofdNumber(box.Y), ofdNumber(box.W), ofdNumber(box.H)),
		Font:     fontID, Size: size,
	}}
	if err := e.LayoutText(&object.TextObject, value); err != nil {
		return "", err
	}
	return e.AddObject(page, object)
}

// UpdateText 按AddText规则重排横向单行文字，保留对象标识、顺序、边界及绘制属性
// 自定义文字定位使用UpdateObject，不进行段落排版和复杂文字塑形
// 入参: page 页面索引, id 文字对象标识, value 原文, fontID 字体资源标识, size 字号
// 返回: error 错误信息
func (e *Editor) UpdateText(page int, id, value, fontID string, size float64) error {
	layer, index, err := e.findObject(page, id)
	if err != nil {
		return err
	}
	object := layer.Objects[index]
	if object.Type != "TextObject" {
		return fmt.Errorf("object %q is not text", id)
	}
	object.TextObject.Font, object.TextObject.Size = fontID, size
	if err := e.LayoutText(&object.TextObject, value); err != nil {
		return err
	}
	object, err = e.prepareObject(id, object)
	if err != nil {
		return err
	}
	e.replaceObject(page, index, object)
	return nil
}

// TransformObject 以页面原点等比缩放后平移对象，文字同步缩放字号和字距，保留原有排版
// 入参: page 页面索引, id 对象标识, dx 横向位移, dy 纵向位移, scale 正缩放比例
// 返回: error 错误信息
func (e *Editor) TransformObject(page int, id string, dx, dy, scale float64) error {
	if !finite(dx) || !finite(dy) || !finite(scale) || scale <= 0 {
		return fmt.Errorf("transform requires finite offsets and a positive finite scale")
	}
	object, err := e.Object(page, id)
	if err != nil {
		return err
	}
	if dx == 0 && dy == 0 && scale == 1 {
		return nil
	}
	var boundary, ctm *string
	switch object.Type {
	case "TextObject":
		obj := &object.TextObject
		boundary, ctm = &obj.Boundary, &obj.CTM
		if scale != 1 {
			obj.Size *= scale
			if obj.LineWidth == 0 && obj.Stroke != nil && *obj.Stroke {
				obj.LineWidth = defaultPathLineWidth
			}
			obj.LineWidth *= scale
			for i := range obj.TextCode {
				code := &obj.TextCode[i]
				for _, value := range []*string{&code.X, &code.Y, &code.DeltaX, &code.DeltaY} {
					*value = scaleTextNumbers(*value, scale)
				}
			}
		}
	case "PathObject":
		boundary, ctm = &object.PathObject.Boundary, &object.PathObject.CTM
	case "ImageObject":
		boundary, ctm = &object.ImageObject.Boundary, &object.ImageObject.CTM
	}
	box, err := ParseBox(*boundary)
	if err != nil {
		return err
	}
	*boundary = fmt.Sprintf("%s %s %s %s", ofdNumber(box.X*scale+dx), ofdNumber(box.Y*scale+dy), ofdNumber(box.W*scale), ofdNumber(box.H*scale))
	if scale != 1 && (*ctm != "" || object.Type != "TextObject") {
		m := NewMatrix(*ctm)
		if object.Type != "TextObject" {
			m.a, m.b, m.c, m.d = m.a*scale, m.b*scale, m.c*scale, m.d*scale
		}
		*ctm = fmt.Sprintf("%s %s %s %s %s %s", ofdNumber(m.a), ofdNumber(m.b), ofdNumber(m.c), ofdNumber(m.d), ofdNumber(m.e*scale), ofdNumber(m.f*scale))
	}
	return e.UpdateObject(page, id, object)
}

// scaleTextNumbers 缩放已校验的文字定位数值，保留g重复编码
// 入参: value 定位数值, scale 缩放比例
// 返回: string 缩放后的数值
func scaleTextNumbers(value string, scale float64) string {
	fields := strings.Fields(value)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "g" {
			i += 2
		}
		number, _ := strconv.ParseFloat(fields[i], 64)
		fields[i] = ofdNumber(number * scale)
	}
	return strings.Join(fields, " ")
}

// LayoutText 按嵌入字体度量重排横向单行文字，设置基线和字距，保留绘制属性
// 不进行段落排版和复杂文字塑形，不修改文档，通过AddObject或UpdateObject提交
// 入参: obj 文字对象, value 原文
// 返回: error 错误信息
func (e *Editor) LayoutText(obj *TextObject, value string) error {
	if obj.ReadDirection != 0 || obj.CharDirection != 0 {
		return fmt.Errorf("automatic text layout requires horizontal text")
	}
	sfnt, ok := e.fonts[obj.Font]
	if !ok {
		return fmt.Errorf("font resource %q not found", obj.Font)
	}
	if !finite(obj.Size) || obj.Size <= 0 || !finite(obj.HScale) || obj.HScale < 0 {
		return fmt.Errorf("invalid text dimensions")
	}
	if !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\t") || value == "" {
		return fmt.Errorf("text must be a nonempty UTF-8 line")
	}
	hScale := obj.HScale
	if hScale == 0 {
		hScale = 1
	}
	runes := []rune(value)
	deltas := make([]string, 0, len(runes)-1)
	for i, char := range runes {
		glyph := sfnt.GlyphIndex(char)
		if glyph == 0 {
			return fmt.Errorf("font does not contain U+%04X", char)
		}
		if i+1 < len(runes) {
			deltas = append(deltas, ofdNumber(float64(sfnt.GlyphAdvance(glyph))*obj.Size/float64(sfnt.UnitsPerEm())*hScale))
		}
	}
	ascender, _, _ := sfnt.VerticalMetrics()
	obj.TextCode = []TextCode{{X: "0", Y: ofdNumber(float64(ascender) * obj.Size / float64(sfnt.UnitsPerEm())), DeltaX: strings.Join(deltas, " "), Value: escapeOFDText(value)}}
	return nil
}

// prepareText 校验并补全标准文字定位，避免依赖阅读器的缺省字距补偿
// 入参: obj 文字对象
// 返回: error 错误信息
func (e *Editor) prepareText(obj *TextObject) error {
	if e.fonts[obj.Font] == nil || !finite(obj.Size) || obj.Size <= 0 {
		return fmt.Errorf("text requires an embedded font and a positive finite size")
	}
	if len(obj.TextCode) == 0 {
		return fmt.Errorf("text codes are empty")
	}
	if obj.VScale != 0 || obj.Decoration != "" || len(obj.CGTransform) != 0 {
		return fmt.Errorf("text extensions and glyph transforms are not supported for creation")
	}
	if !finite(obj.HScale) || obj.HScale < 0 || !finite(obj.LineWidth) || obj.LineWidth < 0 || !finite(obj.MiterLimit) || obj.MiterLimit < 0 {
		return fmt.Errorf("invalid text dimensions")
	}
	if obj.Weight < 0 || obj.Weight > 900 || obj.Weight%100 != 0 {
		return fmt.Errorf("invalid text weight")
	}
	for _, direction := range []int{obj.ReadDirection, obj.CharDirection} {
		if direction != 0 && direction != 90 && direction != 180 && direction != 270 {
			return fmt.Errorf("invalid text direction %d", direction)
		}
	}
	obj.TextCode = append([]TextCode(nil), obj.TextCode...)
	x, y := "", ""
	for i := range obj.TextCode {
		code := &obj.TextCode[i]
		if code.Index != "" || !utf8.ValidString(code.Value) {
			return fmt.Errorf("text requires Unicode character data")
		}
		if code.X != "" {
			x = code.X
		}
		if code.Y != "" {
			y = code.Y
		}
		for _, coordinate := range []string{x, y} {
			if _, err := creationNumbers(coordinate, 1); err != nil {
				return fmt.Errorf("invalid text origin: %w", err)
			}
		}
		code.X, code.Y = x, y
		runes := textCodeRunes(code.Value)
		if len(runes) == 0 {
			return fmt.Errorf("text code is empty")
		}
		for _, char := range runes {
			if e.fonts[obj.Font].GlyphIndex(char) == 0 {
				return fmt.Errorf("font does not contain U+%04X", char)
			}
		}
		for _, delta := range []string{code.DeltaX, code.DeltaY} {
			if delta != "" {
				if err := creationDeltas(delta, len(runes)-1); err != nil {
					return fmt.Errorf("invalid text delta: %w", err)
				}
			}
		}
		if len(runes) > 1 && code.DeltaX == "" && code.DeltaY == "" {
			code.DeltaX = strings.TrimSpace(strings.Repeat("0 ", len(runes)-1))
		}
		code.Value = escapeOFDText(string(runes))
	}
	return nil
}

// creationDeltas 校验文字位移数组及g重复编码
// 入参: value 位移数组, count 所需位移数量
// 返回: error 错误信息
func creationDeltas(value string, count int) error {
	fields := strings.Fields(value)
	numbers := 0
	for i := 0; i < len(fields); i++ {
		repeat := 1
		if fields[i] == "g" {
			if i+2 >= len(fields) {
				return fmt.Errorf("incomplete delta repetition")
			}
			var err error
			repeat, err = strconv.Atoi(fields[i+1])
			if err != nil || repeat <= 0 {
				return fmt.Errorf("invalid delta repetition")
			}
			i += 2
		}
		if _, err := creationNumbers(fields[i], 1); err != nil {
			return err
		}
		if repeat > count-numbers {
			return fmt.Errorf("too many text deltas")
		}
		numbers += repeat
	}
	if numbers != count {
		return fmt.Errorf("expected %d text deltas", count)
	}
	return nil
}

// creationPath 校验紧缩路径的命令及有限数值参数
// 入参: value 路径数据
// 返回: error 错误信息
func creationPath(value string) error {
	tokens := strings.Fields(value)
	if len(tokens) == 0 || (tokens[0] != "M" && tokens[0] != "S") {
		return fmt.Errorf("path must start with M or S")
	}
	for i := 0; i < len(tokens); {
		command := tokens[i]
		i++
		count := 0
		switch command {
		case "M", "S", "L":
			count = 2
		case "Q":
			count = 4
		case "B":
			count = 6
		case "A":
			count = 7
		case "C":
		default:
			return fmt.Errorf("invalid path command %q", command)
		}
		if len(tokens)-i < count {
			return fmt.Errorf("missing arguments for path command %s", command)
		}
		values, err := creationNumbers(strings.Join(tokens[i:i+count], " "), count)
		if err != nil {
			return err
		}
		if command == "A" && (values[0] < 0 || values[1] < 0 || (values[3] != 0 && values[3] != 1) || (values[4] != 0 && values[4] != 1)) {
			return fmt.Errorf("invalid arc radii or flags")
		}
		i += count
	}
	return nil
}

// creationNumbers 解析创建接口的有限十进制数值序列
// 入参: value 数值序列, count 数值数量
// 返回: []float64 数值, error 错误信息
func creationNumbers(value string, count int) ([]float64, error) {
	fields := strings.Fields(value)
	if len(fields) != count {
		return nil, fmt.Errorf("expected %d numbers", count)
	}
	values := make([]float64, count)
	for i, field := range fields {
		number, err := strconv.ParseFloat(field, 64)
		if err != nil || !finite(number) || strings.ContainsAny(field, "xX_") {
			return nil, fmt.Errorf("invalid number %q", field)
		}
		values[i] = number
	}
	return values, nil
}

// creationBox 校验对象边界
// 入参: value 边界描述
// 返回: Box 矩形, error 错误信息
func creationBox(value string) (Box, error) {
	values, err := creationNumbers(value, 4)
	if err != nil {
		return Box{}, err
	}
	if values[2] <= 0 || values[3] <= 0 {
		return Box{}, fmt.Errorf("boundary dimensions must be positive")
	}
	return Box{X: values[0], Y: values[1], W: values[2], H: values[3]}, nil
}

// creationColor 校验默认RGB颜色空间中的基本颜色
// 入参: color 颜色
// 返回: error 错误信息
func creationColor(color *FillColor) error {
	if color == nil {
		return nil
	}
	if color.ColorSpace != "" || color.Index != nil || color.Pattern != nil || color.AxialShd != nil || color.RadialShd != nil || color.unsupported {
		return fmt.Errorf("only direct RGB colors are supported for creation")
	}
	values, err := creationNumbers(color.Value, 3)
	if err != nil {
		return err
	}
	for _, value := range values {
		if value < 0 || value > 255 || math.Trunc(value) != value {
			return fmt.Errorf("RGB channels must be integers between 0 and 255")
		}
	}
	if color.Alpha != nil && (*color.Alpha < 0 || *color.Alpha > 255) {
		return fmt.Errorf("color alpha must be between 0 and 255")
	}
	return nil
}

// finite 判断数值是否有限
// 入参: value 数值
// 返回: bool 是否有限
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// ofdNumber 格式化OFD数值
// 入参: value 数值
// 返回: string 数值文本
func ofdNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// escapeOFDText 转义控制字符、反斜线和XML不允许的字符
// 入参: value 原文
// 返回: string OFD文字编码
func escapeOFDText(value string) string {
	var text strings.Builder
	for _, char := range value {
		if char < ' ' || char == '\\' || char == '\ufffe' || char == '\uffff' {
			fmt.Fprintf(&text, "\\%04X", char)
		} else {
			text.WriteRune(char)
		}
	}
	return text.String()
}
