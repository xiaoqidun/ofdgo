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
	"github.com/xiaoqidun/jbig2"
)

// Editor 编辑OFD文档，长度单位为毫米，页面索引从0开始，实例需串行使用
// Info可修改文档元数据，通过方法管理页面、对象和资源，WriteTo另存结果，不覆盖输入
// OnWriteProgress可选，按fonts、pages、references、resources、write阶段同步回报准备进度，返回错误则停止保存
// completed和total为当前阶段已处理及总工作项，write阶段不计数；阶段可重复，准备完成不代表保存成功
// 回调不可重入修改编辑器，可返回context.Canceled等调用方停止原因
type Editor struct {
	OnWriteProgress func(stage string, completed, total int) error
	Info            DocInfo
	pages           []PageContent
	resources       []editorResource
	fonts           map[string]*font.SFNT
	images          map[string]image.Point
	resourceID      map[editorResourceKey]string
	maxID           int
	history         []editorChange
	historyIndex    int
	historyLimit    int
	revision        uint64
	serial          uint64
	source          *editorSource
	outlines        []byte
}

// editorResource 文档内嵌资源
type editorResource struct {
	name   string
	data   []byte
	font   *Font
	image  *MultiMedia
	subset *editorFontSubset
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
		images:     make(map[string]image.Point),
		resourceID: make(map[editorResourceKey]string),
	}
}

// PageCount 获取当前页数
// 返回: int 页数
func (e *Editor) PageCount() int {
	return len(e.pages)
}

// SetInfo 更新文档元数据，独立保存输入并计入修订和撤销记录
// 入参: info 完整元数据，未修改字段由调用方保留
func (e *Editor) SetInfo(info DocInfo) {
	if reflect.DeepEqual(e.Info, info) {
		return
	}
	before, after := cloneEditorData(e.Info), cloneEditorData(info)
	e.Info = cloneEditorData(after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.Info = cloneEditorData(before) }
		change.redo = func(e *Editor) { e.Info = cloneEditorData(after) }
	}
}

// Page 获取页面的独立副本，修改副本不影响文档
// 入参: index 页面索引
// 返回: *PageContent 页面内容, error 错误信息
func (e *Editor) Page(index int) (*PageContent, error) {
	page, err := e.page(index)
	if err != nil {
		return nil, err
	}
	result := cloneEditorData(*page)
	result.XMLName = xml.Name{Space: ofdNamespace, Local: "Page"}
	for i := range result.Content.Layer {
		layer := &result.Content.Layer[i]
		layer.TextObject, layer.PathObject, layer.ImageObject, layer.CompositeGraphicUnit = nil, nil, nil, nil
		for _, object := range layer.Objects {
			switch object.Type {
			case "TextObject":
				object.TextObject.layout = nil
				layer.TextObject = append(layer.TextObject, object.TextObject)
			case "PathObject":
				layer.PathObject = append(layer.PathObject, object.PathObject)
			case "ImageObject":
				layer.ImageObject = append(layer.ImageObject, object.ImageObject)
			case "CompositeObject", "CompositeGraphicUnit":
				layer.CompositeGraphicUnit = append(layer.CompositeGraphicUnit, object.CompositeGraphicUnit)
			}
		}
	}
	return &result, nil
}

// page 按索引获取内部页面
// 入参: index 页面索引
// 返回: *PageContent 页面内容, error 错误信息
func (e *Editor) page(index int) (*PageContent, error) {
	if index < 0 || index >= len(e.pages) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	if e.source != nil {
		if err := e.loadSourcePage(index); err != nil {
			return nil, err
		}
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
	if err := e.prepareSourceIDs(); err != nil {
		return 0, err
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
	indexes, err := e.CopyPages([]int{index})
	if err != nil {
		return 0, err
	}
	return indexes[0], nil
}

// CopyPages 按指定顺序复制页面并追加到文档末尾，一次操作计入一条撤销记录
// 入参: indexes 不重复的原页面索引
// 返回: []int 副本页面索引, error 错误信息
func (e *Editor) CopyPages(indexes []int) ([]int, error) {
	if err := e.validatePageIndexes(indexes); err != nil {
		return nil, err
	}
	if len(indexes) == 0 {
		return nil, nil
	}
	at := len(e.pages)
	if e.source != nil {
		source, err := e.Reader()
		if err != nil {
			return nil, err
		}
		if _, err := e.importPages(source, indexes, at, true, PageImportOptions{}); err != nil {
			return nil, err
		}
		result := make([]int, len(indexes))
		for i := range result {
			result[i] = at + i
		}
		return result, nil
	}
	pages := make([]PageContent, len(indexes))
	for n, index := range indexes {
		source := &e.pages[index]
		page := &pages[n]
		*page = PageContent{Area: source.Area, Content: Content{Layer: make([]Layer, len(source.Content.Layer))}}
		for i, layer := range source.Content.Layer {
			page.Content.Layer[i] = Layer{Type: layer.Type, Objects: make([]GraphicObject, len(layer.Objects))}
			for j, object := range layer.Objects {
				copy, err := cloneEditorObject(object)
				if err != nil {
					return nil, err
				}
				page.Content.Layer[i].Objects[j] = copy
			}
		}
	}
	result := make([]int, len(pages))
	for n := range pages {
		page := &pages[n]
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
		result[n] = at + n
	}
	e.pages = append(e.pages, pages...)
	if change := e.recordChange(); change != nil {
		for i := range pages {
			pages[i] = copyEditorPage(pages[i])
		}
		change.undo = func(e *Editor) { e.pages = e.pages[:at] }
		change.redo = func(e *Editor) {
			for _, page := range pages {
				e.pages = append(e.pages, copyEditorPage(page))
			}
		}
	}
	return result, nil
}

// DeletePage 删除页面，输出时清理相关目录、跳转、注释和签章位置，保留其他页面及原始签名凭据
// 入参: index 页面索引
// 返回: error 错误信息
func (e *Editor) DeletePage(index int) error {
	return e.DeletePages([]int{index})
}

// DeletePages 删除指定页面，输出时清理页面引用，一次操作计入一条撤销记录
// 入参: indexes 不重复的页面索引
// 返回: error 错误信息
func (e *Editor) DeletePages(indexes []int) error {
	if err := e.validatePageIndexes(indexes); err != nil {
		return err
	}
	if len(indexes) == 0 {
		return nil
	}
	indexes = slices.Clone(indexes)
	slices.Sort(indexes)
	saved := make([]PageContent, len(indexes))
	for i, index := range indexes {
		page, err := e.page(index)
		if err != nil {
			return err
		}
		saved[i] = copyEditorPage(*page)
	}
	remove := func(e *Editor) {
		kept, next := e.pages[:0], 0
		for index, page := range e.pages {
			if next < len(indexes) && index == indexes[next] {
				next++
			} else {
				kept = append(kept, page)
			}
		}
		clear(e.pages[len(kept):])
		e.pages = kept
	}
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) {
			pages := make([]PageContent, len(e.pages)+len(saved))
			kept, removed := 0, 0
			for index := range pages {
				if removed < len(indexes) && index == indexes[removed] {
					pages[index] = copyEditorPage(saved[removed])
					removed++
				} else {
					pages[index] = e.pages[kept]
					kept++
				}
			}
			e.pages = pages
		}
		change.redo = remove
	}
	remove(e)
	return nil
}

// validatePageIndexes 校验页面索引范围和重复值
// 入参: indexes 页面索引
// 返回: error 错误信息
func (e *Editor) validatePageIndexes(indexes []int) error {
	seen := make(map[int]bool, len(indexes))
	for _, index := range indexes {
		if index < 0 || index >= len(e.pages) {
			return fmt.Errorf("page index %d out of range", index)
		}
		if seen[index] {
			return fmt.Errorf("duplicate page index %d", index)
		}
		seen[index] = true
	}
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

// MovePages 按指定顺序将页面移动到移除这些页面后的目标位置，保留页面及对象标识
// 一次操作计入一条撤销记录，不解析或重写页面内容
// 入参: indexes 不重复的原页面索引, to 移动后的起始页面索引，最大值为总页数减去移动页数
// 返回: error 错误信息
func (e *Editor) MovePages(indexes []int, to int) error {
	if err := e.validatePageIndexes(indexes); err != nil {
		return err
	}
	if to < 0 || to > len(e.pages)-len(indexes) {
		return fmt.Errorf("page index %d out of range", to)
	}
	if len(indexes) == 0 {
		return nil
	}
	selected := make(map[int]bool, len(indexes))
	for _, index := range indexes {
		selected[index] = true
	}
	order := make([]int, 0, len(e.pages))
	for index := range e.pages {
		if !selected[index] {
			order = append(order, index)
		}
	}
	order = slices.Insert(order, to, indexes...)
	changed := false
	for index, previous := range order {
		changed = changed || index != previous
	}
	if !changed {
		return nil
	}
	e.reorderPages(order)
	if change := e.recordChange(); change != nil {
		inverse := make([]int, len(order))
		for index, previous := range order {
			inverse[previous] = index
		}
		change.undo = func(e *Editor) { e.reorderPages(inverse) }
		change.redo = func(e *Editor) { e.reorderPages(order) }
	}
	return nil
}

// reorderPages 按原页面索引序列调整页面顺序
// 入参: order 页面索引序列
func (e *Editor) reorderPages(order []int) {
	pages := slices.Clone(e.pages)
	for index, previous := range order {
		e.pages[index] = pages[previous]
	}
}

// ResizePage 调整页面尺寸，不缩放或移动页面内的对象
// 入参: index 页面索引, width 页面宽度, height 页面高度
// 返回: error 错误信息
func (e *Editor) ResizePage(index int, width, height float64) error {
	return e.ResizePages([]int{index}, width, height)
}

// ResizePages 统一调整指定页面尺寸，不缩放或移动对象，一次操作计入一条撤销记录
// 入参: indexes 不重复的页面索引, width 页面宽度, height 页面高度
// 返回: error 错误信息
func (e *Editor) ResizePages(indexes []int, width, height float64) error {
	if err := e.validatePageIndexes(indexes); err != nil {
		return err
	}
	if !finite(width) || !finite(height) || width <= 0 || height <= 0 {
		return fmt.Errorf("page dimensions must be finite and positive")
	}
	after := fmt.Sprintf("0 0 %s %s", ofdNumber(width), ofdNumber(height))
	before := make([]string, len(indexes))
	changed := false
	for i, index := range indexes {
		page, err := e.page(index)
		if err != nil {
			return err
		}
		before[i] = page.Area.PhysicalBox
		changed = changed || before[i] != after
	}
	if !changed {
		return nil
	}
	indexes = slices.Clone(indexes)
	resize := func(e *Editor) {
		for _, index := range indexes {
			e.pages[index].Area.PhysicalBox = after
		}
	}
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) {
			for i, index := range indexes {
				e.pages[index].Area.PhysicalBox = before[i]
			}
		}
		change.redo = resize
	}
	resize(e)
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
	data, err := font.ToSFNT(file.Data)
	if err != nil {
		return "", err
	}
	key := editorResourceKey{checksum: sha256.Sum256(data), index: index}
	if id, ok := e.resourceID[key]; ok {
		return id, nil
	}
	data, err = (FontFile{Data: data}).Face(index)
	if err != nil {
		return "", err
	}
	sfnt, err := font.ParseSFNT(data, 0)
	if err != nil {
		return "", err
	}
	name := editorFontName(sfnt, font.NamePostScript, font.NameFull, font.NameFontFamily)
	if name == "" {
		return "", fmt.Errorf("font has no name")
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	id := e.nextID()
	extension := ".ttf"
	if sfnt.IsCFF {
		extension = ".otf"
	}
	resource := editorResource{
		name: e.resourceDirectory() + "/Font_" + id + extension,
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
	resource.font.FontFile = "Font_" + id + extension
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

// AddImage 注册PNG或JPEG图片，重复资源复用标识，引用后写入文档
// 不透明纯黑白PNG仅在无损JBIG2编码更小时转换，其他图片保留原始编码
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
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	if format == "png" && uint64(config.Width)*uint64(config.Height) <= 64<<20 {
		if compact := editorBinaryImage(data); compact != nil {
			data, format = compact, "jbig2"
		}
	}
	id := e.nextID()
	extension := format
	if extension == "jpeg" {
		extension = "jpg"
	} else if extension == "jbig2" {
		extension = "jb2"
	}
	name := "Image_" + id + "." + extension
	e.resources = append(e.resources, editorResource{
		name:  e.resourceDirectory() + "/" + name,
		data:  bytes.Clone(data),
		image: &MultiMedia{ID: id, Type: "Image", Format: strings.ToUpper(format), MediaFile: name},
	})
	e.images[id] = image.Pt(config.Width, config.Height)
	e.resourceID[key] = id
	return id, nil
}

// editorBinaryImage 尝试纯黑白无损编码，不二值化，不改变透明度，无体积收益时保留原图
// 入参: data PNG图片数据
// 返回: []byte 更小的JBIG2数据，不适用时为nil
func editorBinaryImage(data []byte) []byte {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var output bytes.Buffer
	if err := jbig2.Encode(&output, img, &jbig2.Options{MaxPageBytes: uint64(len(data))}); err != nil || output.Len() >= len(data) {
		return nil
	}
	return output.Bytes()
}

// AddObject 按添加顺序放入正文图层，支持文字、路径和图片，自动分配对象ID
// 支持基本颜色、直接资源引用及路径裁剪，不支持动作、渐变及复合图元
// 入参: page 页面索引, object 对象内容，添加后不再引用调用方的可变数据
// 返回: string 对象标识, error 错误信息
func (e *Editor) AddObject(page int, object GraphicObject) (string, error) {
	ids, err := e.CopyObjects(page, []GraphicObject{object}, 0, 0)
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// Object 获取对象的独立副本，修改副本不影响文档
// 可编辑的继承样式解析为直接属性；仅在对象修改或复制时写入独立样式
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
	switch object.Type {
	case "TextObject":
		object.TextObject.ID = id
	case "PathObject":
		object.PathObject.ID = id
	case "ImageObject":
		object.ImageObject.ID = id
	default:
		return fmt.Errorf("unsupported object type %q", object.Type)
	}
	return e.UpdateObjects(page, []GraphicObject{object})
}

// DeleteObject 删除对象，不回收资源或复用标识
// 入参: page 页面索引, id 对象标识
// 返回: error 错误信息
func (e *Editor) DeleteObject(page int, id string) error {
	return e.DeleteObjects(page, []string{id})
}

// MoveObject 调整同一图层或末级页块内的绘制顺序，保留对象标识和内容
// 入参: page 页面索引, id 对象标识, to 直接容器内的对象索引，0为最底层，末尾为最顶层
// 返回: error 错误信息
func (e *Editor) MoveObject(page int, id string, to int) error {
	layer, from, err := e.findObject(page, id)
	if err != nil {
		return err
	}
	_, siblings := e.objectOrderIndexes(page, layer, id)
	from = slices.Index(siblings, from)
	if to < 0 || to >= len(siblings) {
		return fmt.Errorf("object index %d out of range", to)
	}
	if from == to {
		return nil
	}
	capability, err := e.ObjectCapabilities(page, id)
	if err != nil {
		return err
	}
	if !capability.Order {
		return fmt.Errorf("object %q cannot be reordered", id)
	}
	layers := copyEditorPage(e.pages[page]).Content.Layer
	for i := range layers {
		if layers[i].ID == layer.ID {
			objects := make([]GraphicObject, len(siblings))
			for j, index := range siblings {
				objects[j] = layer.Objects[index]
			}
			moveEditorItem(objects, from, to)
			for j, index := range siblings {
				layers[i].Objects[index] = objects[j]
			}
		}
	}
	e.replaceLayers(page, layers)
	return nil
}

// findObject 查找页面各图层中的对象
// 入参: page 页面索引, id 对象标识
// 返回: *Layer 所属图层, int 对象索引, error 错误信息
func (e *Editor) findObject(page int, id string) (*Layer, int, error) {
	content, err := e.page(page)
	if err != nil {
		return nil, 0, err
	}
	for i := range content.Content.Layer {
		layer := &content.Content.Layer[i]
		for index, object := range layer.Objects {
			if editorObjectID(object) == id && id != "" {
				return layer, index, nil
			}
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
		codes := obj.TextCode
		if err := e.prepareText(obj); err != nil {
			return GraphicObject{}, err
		}
		if e.objectOrigin(obj.ID) != nil {
			obj.TextCode = codes
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
		if err := validateEditorStroke(*obj); err != nil {
			return GraphicObject{}, err
		}
		obj.ID = id
		boundary, ctm, drawParam = obj.Boundary, obj.CTM, obj.DrawParam
		join = obj.Join
		alpha, clips, actions = obj.Alpha, obj.Clips, obj.Actions
		fill, stroke = obj.FillColor, (*FillColor)(obj.StrokeColor)
	case "ImageObject":
		obj := &object.ImageObject
		if _, err := e.editorImage(obj.ResourceID); err != nil {
			return GraphicObject{}, err
		}
		if obj.ImageMask != "" {
			if _, err := e.editorImage(obj.ImageMask); err != nil {
				return GraphicObject{}, err
			}
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
	if drawParam != "" || len(actions) != 0 {
		return GraphicObject{}, fmt.Errorf("draw parameter references and actions are not supported for creation")
	}
	if err := e.validateObjectClips(clips); err != nil {
		return GraphicObject{}, err
	}
	if alpha != nil && (*alpha < 0 || *alpha > 255) {
		return GraphicObject{}, fmt.Errorf("alpha must be between 0 and 255")
	}
	for _, color := range []*FillColor{fill, stroke} {
		if err := creationColor(color); err != nil {
			return GraphicObject{}, &EditError{Code: EditUnsupportedColor, Err: err}
		}
	}
	if join != "" && join != "Miter" && join != "Round" && join != "Bevel" {
		return GraphicObject{}, fmt.Errorf("invalid line join %q", join)
	}
	return cloneEditorObject(object)
}

// validateObjectClips 校验对象的路径裁剪，仅接受无资源引用及嵌套裁剪的路径
// 入参: clips 裁剪集合，nil表示无裁剪
// 返回: error 错误信息
func (e *Editor) validateObjectClips(clips *Clips) error {
	if clips == nil {
		return nil
	}
	if len(clips.Clip) == 0 {
		return fmt.Errorf("clips must contain a clip")
	}
	for _, clip := range clips.Clip {
		if len(clip.Area) == 0 {
			return fmt.Errorf("clip must contain an area")
		}
		for _, area := range clip.Area {
			if len(area.Path) == 0 || area.DrawParam != "" || len(area.Text) != 0 {
				return fmt.Errorf("clips only support paths without draw parameter references")
			}
			if area.CTM != "" {
				if _, err := creationNumbers(area.CTM, 6); err != nil {
					return err
				}
			}
			for _, path := range area.Path {
				if path.ID != "" || path.Clips != nil {
					return fmt.Errorf("clip paths must not have object IDs or nested clips")
				}
				if _, err := e.prepareObject("", GraphicObject{Type: "PathObject", PathObject: path}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// cloneEditorObject 复制对象全部已解析字段，保留不可变的段落信息
// 入参: object 对象内容
// 返回: GraphicObject 独立副本, error 错误信息
func cloneEditorObject(object GraphicObject) (GraphicObject, error) {
	switch object.Type {
	case "TextObject":
		return GraphicObject{Type: object.Type, TextObject: cloneEditorData(object.TextObject)}, nil
	case "PathObject":
		return GraphicObject{Type: object.Type, PathObject: cloneEditorData(object.PathObject)}, nil
	case "ImageObject":
		return GraphicObject{Type: object.Type, ImageObject: cloneEditorData(object.ImageObject)}, nil
	case "CompositeObject", "CompositeGraphicUnit":
		return GraphicObject{Type: object.Type, CompositeGraphicUnit: cloneEditorData(object.CompositeGraphicUnit)}, nil
	}
	return GraphicObject{}, fmt.Errorf("unsupported object type %q", object.Type)
}

// AddText 添加文字，按显式换行和嵌入字体度量定位，不自动折行或进行复杂文字塑形
// 入参: page 页面索引, box 文字边界, value 原文, fontID 字体资源标识, size 字号
// 返回: string 对象标识, error 错误信息
func (e *Editor) AddText(page int, box Box, value, fontID string, size float64) (string, error) {
	object := GraphicObject{Type: "TextObject", TextObject: TextObject{
		Boundary: fmt.Sprintf("%s %s %s %s", ofdNumber(box.X), ofdNumber(box.Y), ofdNumber(box.W), ofdNumber(box.H)),
		Font:     fontID, Size: size,
	}}
	if err := e.LayoutText(&object.TextObject, value, TextLayout{}); err != nil {
		return "", err
	}
	return e.AddObject(page, object)
}

// UpdateText 按现有段落选项重排横向文字，保留对象标识、顺序、边界及绘制属性
// 自定义文字定位使用UpdateObject，不进行复杂文字塑形
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
	_, layout := object.TextObject.TextLayout()
	if err := e.LayoutText(&object.TextObject, value, layout); err != nil {
		return err
	}
	return e.UpdateObject(page, id, object)
}

// TransformObject 以页面原点等比缩放后平移对象，文字同步缩放字号和字距，保留原有排版
// 入参: page 页面索引, id 对象标识, dx 横向位移, dy 纵向位移, scale 正缩放比例
// 返回: error 错误信息
func (e *Editor) TransformObject(page int, id string, dx, dy, scale float64) error {
	return e.TransformObjects(page, []string{id}, dx, dy, scale)
}

// transformEditorObject 变换独立对象副本，保留绘制属性和编辑中的段落信息
// 入参: object 对象副本, dx 横向位移, dy 纵向位移, scale 正缩放比例
// 返回: GraphicObject 变换后的对象, error 错误信息
func transformEditorObject(object GraphicObject, dx, dy, scale float64) (GraphicObject, error) {
	var boundary, ctm *string
	switch object.Type {
	case "TextObject":
		obj := &object.TextObject
		boundary, ctm = &obj.Boundary, &obj.CTM
		if scale != 1 {
			obj.Size *= scale
			if obj.layout != nil {
				layout := *obj.layout
				layout.options.LineHeight *= scale
				layout.options.LetterSpacing *= scale
				layout.options.LeftIndent *= scale
				layout.options.RightIndent *= scale
				layout.options.FirstLineIndent *= scale
				obj.layout = &layout
			}
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
	default:
		return GraphicObject{}, fmt.Errorf("unsupported object type %q", object.Type)
	}
	if clips := editorObjectClips(&object); scale != 1 && *clips != nil && (object.Type == "TextObject" || (*clips).TransFlag != nil && !*(*clips).TransFlag) {
		*clips = transformObjectClips(*clips, Matrix{a: scale, d: scale})
	}
	box, err := ParseBox(*boundary)
	if err != nil {
		return GraphicObject{}, err
	}
	if object.Type == "ImageObject" && *ctm == "" && scale != 1 {
		*ctm = Matrix{a: box.W, d: box.H}.String()
	}
	*boundary = fmt.Sprintf("%s %s %s %s", ofdNumber(box.X*scale+dx), ofdNumber(box.Y*scale+dy), ofdNumber(box.W*scale), ofdNumber(box.H*scale))
	if scale != 1 && (*ctm != "" || object.Type != "TextObject") {
		m := NewMatrix(*ctm)
		if object.Type != "TextObject" {
			m.a, m.b, m.c, m.d = m.a*scale, m.b*scale, m.c*scale, m.d*scale
		}
		*ctm = fmt.Sprintf("%s %s %s %s %s %s", ofdNumber(m.a), ofdNumber(m.b), ofdNumber(m.c), ofdNumber(m.d), ofdNumber(m.e*scale), ofdNumber(m.f*scale))
	}
	return object, nil
}

// ReplaceImage 替换图片资源，保留对象标识和绘制顺序，一次撤销恢复资源及布局
// 入参: page 页面索引, id 图片对象标识, data PNG或JPEG数据, fit 为contain、cover或空，空值保留原变换和裁剪
// 返回: error 错误信息
func (e *Editor) ReplaceImage(page int, id string, data []byte, fit string) error {
	object, err := e.Object(page, id)
	if err != nil {
		return err
	}
	if object.Type != "ImageObject" {
		return fmt.Errorf("object %q is not an image", id)
	}
	object.ImageObject.ResourceID, err = e.AddImage(data)
	if err != nil {
		return err
	}
	if fit != "" {
		if err := e.fitImage(&object.ImageObject, fit); err != nil {
			return err
		}
	}
	return e.UpdateObject(page, id, object)
}

// AlignObject 将对象对齐页面，文字采用实际字形范围，路径和图片采用对象边界
// 入参: page 页面索引, id 对象标识, alignment 为left、center、right、top、middle或bottom
// 返回: error 错误信息
func (e *Editor) AlignObject(page int, id, alignment string) error {
	return e.AlignObjects(page, []string{id}, alignment)
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

// LayoutText 按嵌入字体度量和段落选项重排横向文字，保留绘制属性，不进行复杂文字塑形
// 不修改文档，通过AddObject或UpdateObject提交；选项仅供当前编辑过程使用，保存为标准文字定位
// 入参: obj 文字对象, value 原文, options 段落排版选项
// 返回: error 错误信息
func (e *Editor) LayoutText(obj *TextObject, value string, options TextLayout) error {
	if obj.ReadDirection != 0 || obj.CharDirection != 0 {
		return fmt.Errorf("automatic text layout requires horizontal text")
	}
	sfnt, err := e.editorFont(obj.Font)
	if err != nil {
		return err
	}
	if !finite(obj.Size) || obj.Size <= 0 || !finite(obj.HScale) || obj.HScale < 0 {
		return fmt.Errorf("invalid text dimensions")
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if !utf8.ValidString(value) || strings.Contains(value, "\t") || strings.Trim(value, "\n") == "" {
		return fmt.Errorf("text must contain UTF-8 characters without tabs")
	}
	lineHeight := options.LineHeight
	if !finite(lineHeight) || lineHeight < 0 {
		return fmt.Errorf("line height must be finite and nonnegative")
	}
	if !finite(options.LetterSpacing) {
		return fmt.Errorf("letter spacing must be finite")
	}
	if !finite(options.LeftIndent) || !finite(options.RightIndent) || !finite(options.FirstLineIndent) {
		return fmt.Errorf("paragraph indents must be finite")
	}
	if !slices.Contains([]string{"", "left", "center", "right", "justify"}, options.Align) {
		return fmt.Errorf("invalid text alignment %q", options.Align)
	}
	var width float64
	if options.Wrap || options.Align != "" && options.Align != "left" || options.LeftIndent != 0 || options.RightIndent != 0 || options.FirstLineIndent != 0 {
		box, err := obj.TextFrame()
		if err != nil {
			return err
		}
		width = box.W - options.LeftIndent - options.RightIndent
		if !finite(width) || width <= 0 || !finite(width-options.FirstLineIndent) || width-options.FirstLineIndent <= 0 {
			return fmt.Errorf("paragraph indents leave no text width")
		}
	}
	hScale := obj.HScale
	if hScale == 0 {
		hScale = 1
	}
	ascender, descender, gap := sfnt.VerticalMetrics()
	unit := obj.Size / float64(sfnt.UnitsPerEm())
	if lineHeight == 0 {
		lineHeight = math.Max(obj.Size, float64(int(ascender)-int(descender)+int(gap))*unit)
	}
	var codes []TextCode
	var missing []rune
	for _, paragraph := range strings.Split(value, "\n") {
		runes := []rune(paragraph)
		advances := make([]float64, len(runes))
		for i, char := range runes {
			glyph := sfnt.GlyphIndex(char)
			if glyph == 0 {
				missing = append(missing, char)
				continue
			}
			advances[i] = float64(sfnt.GlyphAdvance(glyph)) * unit * hScale
		}
		if len(missing) != 0 {
			continue
		}
		spaceTextAdvances(runes, advances, options.LetterSpacing)
		for _, advance := range advances {
			if !finite(advance) {
				return fmt.Errorf("text advance exceeds finite range")
			}
		}
		lines := breakTextLines(runes, advances, width, options.Wrap, options.LetterSpacing, options.FirstLineIndent)
		for i, line := range lines {
			indent := 0.0
			if i == 0 {
				indent = options.FirstLineIndent
			}
			x, deltas := alignTextLine(runes[line[0]:line[1]], advances[line[0]:line[1]], width-indent, options.Align, i+1 < len(lines), options.LetterSpacing)
			x += options.LeftIndent + indent
			y := float64(ascender)*unit + float64(len(codes))*lineHeight
			if !finite(x) || !finite(y) {
				return fmt.Errorf("text position exceeds finite range")
			}
			codes = append(codes, TextCode{X: ofdNumber(x), Y: ofdNumber(y), DeltaX: deltas, Value: escapeOFDText(string(runes[line[0]:line[1]]))})
		}
	}
	if err := missingGlyphError(obj.Font, missing); err != nil {
		return err
	}
	obj.TextCode = codes
	obj.layout = nil
	if options != (TextLayout{}) || e.objectOrigin(obj.ID) != nil {
		obj.layout = &textLayout{value: value, options: options}
	}
	return nil
}

// prepareText 校验并补全标准文字定位，避免依赖阅读器的缺省字距补偿
// 入参: obj 文字对象
// 返回: error 错误信息
func (e *Editor) prepareText(obj *TextObject) error {
	sfnt, err := e.editorFont(obj.Font)
	if err != nil {
		return err
	}
	if !finite(obj.Size) || obj.Size <= 0 {
		return fmt.Errorf("text requires an embedded font and a positive finite size")
	}
	if len(obj.TextCode) == 0 {
		return fmt.Errorf("text codes are empty")
	}
	if obj.VScale != 0 || obj.Decoration != "" || len(obj.CGTransform) != 0 {
		return &EditError{Code: EditUnsupportedObject, Err: fmt.Errorf("text extensions and glyph transforms are not supported for creation")}
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
	var missing []rune
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
		for _, char := range runes {
			if sfnt.GlyphIndex(char) == 0 {
				missing = append(missing, char)
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
	if strings.Trim(obj.Text(), "\n") == "" {
		return fmt.Errorf("text codes are empty")
	}
	return missingGlyphError(obj.Font, missing)
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
