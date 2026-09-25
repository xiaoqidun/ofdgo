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
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// editorImportEntry 资源或关联索引节点及其来源
type editorImportEntry struct {
	name, base, group string
	data              []byte
	node              *editorXML
}

// editorPageImport 一次插页操作的独立资源图，提交前不修改目标文档
type editorPageImport struct {
	reader        *Reader
	doc           *Document
	directory     string
	target        *Reader
	paths         map[string]string
	evidence      map[string]string
	resourceXML   []byte
	pageParts     []byte
	documentXML   []byte
	documentRoot  *editorXML
	maximum       int
	ids           map[string]string
	pages         map[string]bool
	files         map[string][]byte
	resources     map[string]editorImportEntry
	used          map[string][]byte
	templates     map[string]TemplatePage
	templateParts map[string][]byte
	defaultCS     string
	attachments   map[string][]byte
	copyPage      bool
	strictLinks   bool
	progress      editorProgress
}

// PageImportOptions 页面导入选项，零值仅导入页面及其关联资源
// OnProgress按ids、pages、resources、commit阶段回报进度，total为0表示总量未知
// 回调返回错误则取消导入，commit检查点之后统一提交；回调不可重入修改编辑器
// AllowDecrypted显式允许将加密来源导入未加密目标，不影响原文件
type PageImportOptions struct {
	Outlines       bool
	AllowDecrypted bool
	OnProgress     func(stage string, completed, total int) error
}

// ObjectImportOptions 跨文档对象迁移选项，位移单位为毫米
// AllowDecrypted显式允许将加密来源导入未加密目标，进度回调返回错误时撤销整个操作
type ObjectImportOptions struct {
	DX, DY         float64
	AllowDecrypted bool
	OnProgress     func(stage string, completed, total int) error
}

// ImportObjects 迁移选区及引用资源，不改变来源与目标页面结构，提交一次撤销记录
// 保留对象原文与图层继承，同页链接映射到目标页，无法迁移的跨页链接明确报错
// 返回后可关闭来源阅读器，后续修改来源不影响副本
// 入参: source 来源编辑器, sourcePage 来源页, ids 对象标识, page 目标页, options 迁移选项
// 返回: []string 新对象标识, error 错误信息
func (e *Editor) ImportObjects(source *Editor, sourcePage int, ids []string, page int, options ObjectImportOptions) ([]string, error) {
	if !finite(options.DX) || !finite(options.DY) {
		return nil, fmt.Errorf("import requires finite offsets")
	}
	if source.encryption != nil && e.encryption == nil && !options.AllowDecrypted {
		return nil, ErrEncryptionPolicyRequired
	}
	objects, err := source.Objects(sourcePage, ids)
	if err != nil {
		return nil, err
	}
	target, err := e.page(page)
	if err != nil || len(objects) == 0 {
		return nil, err
	}
	if source == e {
		return e.CopyObjects(page, objects, options.DX, options.DY)
	}
	reader, err := source.Reader()
	if err != nil {
		return nil, err
	}
	doc, err := reader.Doc()
	if err != nil {
		return nil, err
	}
	ref := doc.Pages.Page[sourcePage]
	progress := editorProgress(options.OnProgress)
	base, migration, err := e.prepareImport(reader, doc, map[string]bool{ref.ID: true}, false, progress)
	if err != nil {
		return nil, err
	}
	migration.ids[ref.ID], migration.strictLinks = target.ID, true
	pageName := reader.ResPath(ref.BaseLoc)
	content, err := reader.PageContentByIndex(sourcePage)
	if err != nil {
		return nil, err
	}
	for _, value := range content.PageRes {
		name, err := migration.resolve(pageName, "", value)
		if err != nil {
			return nil, err
		}
		if err := migration.resourceIndex(name); err != nil {
			return nil, err
		}
	}
	layers := make([][]byte, len(objects))
	for i, object := range objects {
		if err := progress.report("objects", i, len(objects)); err != nil {
			return nil, err
		}
		var data []byte
		var attrs ofdAttrs
		if origin := object.origin; origin != nil {
			data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
			if err == nil {
				data, err = editorXMLStandalone(data, origin.node)
			}
			for parent := origin.node.parent; parent != nil; parent = parent.parent {
				if parent.name.Local == "Layer" {
					attrs.add("DrawParam", parent.attr("DrawParam"))
					break
				}
			}
		} else {
			data, err = editorObjectXML(object)
		}
		if err != nil {
			return nil, err
		}
		data, err = editorXMLContainer("Layer", attrs, data)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		layers[i], err = migration.encode(editorImportEntry{name: pageName, data: data}, root)
		if err != nil {
			return nil, err
		}
	}
	if err := migration.encodeResources(); err != nil {
		return nil, err
	}
	for _, resource := range e.resources {
		delete(migration.files, resource.name)
	}
	next, err := migration.merge(base, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	var copied []string
	err = e.Transaction(func(editor *Editor) error {
		if err := progress.report("commit", 0, 0); err != nil {
			return err
		}
		editor.bindImportResources()
		editor.source, editor.maxID = next, migration.maximum
		next.idsReady = true
		imported := make([]GraphicObject, len(layers))
		for i, data := range layers {
			data, err := editorXMLContainer("Content", nil, data)
			if err != nil {
				return err
			}
			var content Content
			if err := xml.Unmarshal(data, &content); err != nil {
				return err
			}
			layer := content.Layer[0]
			root, err := parseEditorXML(data)
			if err != nil {
				return err
			}
			object := layer.Objects[0]
			imported[i], err = editor.copiedObjectStyle(object, layer.DrawParam)
			if err != nil {
				return err
			}
			imported[i].TextObject.layout = objects[i].TextObject.layout
			imported[i].state = objects[i].state
			imported[i].CompositeGraphicUnit.states = remapCompositeStates(objects[i].CompositeGraphicUnit.states, migration.ids)
			imported[i].origin = &editorObjectOrigin{editor: editor, data: data, node: root.child("Layer").children[0], object: object}
		}
		copied, err = editor.CopyObjects(page, imported, options.DX, options.DY)
		return err
	})
	return copied, err
}

// ImportPages 将来源主文档中的指定页面插入目标位置，保持输入顺序并作为一次撤销操作
// 页面索引从0开始，at可等于当前页数；复制关联模板、注释、签章外观和实际引用的资源
// 不导入来源元数据和目录，指向未选页面的跳转被移除；签名数据仅保留原始凭据，不代表合并后文档有效
// 返回后可关闭来源Reader，目标原Reader的生命周期要求不变
// 入参: source 来源阅读器, indexes 来源页面索引，不可重复, at 目标插入位置
// 返回: []string 新页面标识, error 错误信息
func (e *Editor) ImportPages(source *Reader, indexes []int, at int) ([]string, error) {
	return e.ImportPagesWithOptions(source, indexes, at, PageImportOptions{})
}

// ImportPagesWithOptions 按选项导入页面，页面与目录作为一次原子操作提交
// Outlines启用时追加指向所选页面的目录及其祖先，保留层级和跳转坐标，不导入无关目录
// 其他行为与ImportPages相同，不迁移来源元数据
// 入参: source 来源阅读器, indexes 来源页面索引, at 目标插入位置, options 导入选项
// 返回: []string 新页面标识, error 错误信息
func (e *Editor) ImportPagesWithOptions(source *Reader, indexes []int, at int, options PageImportOptions) ([]string, error) {
	if source.encryption != nil && e.encryption == nil && !options.AllowDecrypted {
		return nil, ErrEncryptionPolicyRequired
	}
	return e.importPages(source, indexes, at, false, options)
}

// importPages 迁移页面，同文档复制时复用原资源并保留其他页面的跳转
// 入参: source 来源阅读器, indexes 来源页索引, at 插入位置, copyPage 是否同文档复制, options 导入选项
// 返回: []string 新页标识, error 错误信息
func (e *Editor) importPages(source *Reader, indexes []int, at int, copyPage bool, options PageImportOptions) ([]string, error) {
	if at < 0 || at > len(e.pages) {
		return nil, fmt.Errorf("page index %d out of range", at)
	}
	doc, err := source.Doc()
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool)
	for _, index := range indexes {
		if index < 0 || index >= len(doc.Pages.Page) {
			return nil, fmt.Errorf("source page index %d out of range", index)
		}
		id := doc.Pages.Page[index].ID
		if selected[id] {
			return nil, fmt.Errorf("duplicate source page %q", id)
		}
		selected[id] = true
	}
	if len(indexes) == 0 {
		return nil, nil
	}
	progress := editorProgress(options.OnProgress)
	base, m, err := e.prepareImport(source, doc, selected, copyPage, progress)
	if err != nil {
		return nil, err
	}
	refs := make([]Page, len(indexes))
	ids := make([]string, len(indexes))
	for i, index := range indexes {
		if err := progress.report("pages", i, len(indexes)); err != nil {
			return nil, err
		}
		page := doc.Pages.Page[index]
		ids[i] = m.id(page.ID)
		name, err := m.copyXML(source.ResPath(page.BaseLoc), packagePagePath(m.directory, ids[i]))
		if err != nil {
			return nil, err
		}
		refs[i] = Page{ID: ids[i], BaseLoc: name}
		entry, err := m.documentReference("Pages", "Page", page.ID, ids[i], name)
		if err != nil {
			return nil, err
		}
		m.pageParts = append(m.pageParts, entry...)
	}
	if err := progress.report("pages", len(indexes), len(indexes)); err != nil {
		return nil, err
	}
	annotations, err := m.annotations()
	if err != nil {
		return nil, err
	}
	signatures, err := m.signatures()
	if err != nil {
		return nil, err
	}
	beforeOutlines, outlines := e.outlines, e.outlines
	if options.Outlines {
		imported, err := m.outlines()
		if err != nil {
			return nil, err
		}
		outlines, err = e.appendImportedOutlines(imported)
		if err != nil {
			return nil, err
		}
	}
	if !copyPage {
		if err := m.encodeResources(); err != nil {
			return nil, err
		}
	}
	for _, resource := range e.resources {
		delete(m.files, resource.name)
	}
	next, err := m.merge(base, refs, annotations, signatures)
	if err != nil {
		return nil, err
	}
	added := make([]PageContent, len(refs))
	for i, ref := range refs {
		added[i] = PageContent{ID: ref.ID}
	}
	if copyPage {
		preview := *e
		preview.source, preview.pages = next, added
		for i, index := range indexes {
			states := make(map[string]editorCompositeState)
			groups := make(map[string]map[string]editorCompositeState)
			for _, layer := range e.pages[index].Content.Layer {
				for _, object := range layer.Objects {
					id := m.ids[editorObjectID(object)]
					state := object.state
					if layout := object.TextObject.layout; object.Type == "TextObject" && layout != nil {
						state.layout = layout
					}
					states[id] = state
					groups[id] = remapCompositeStates(object.CompositeGraphicUnit.states, m.ids)
				}
			}
			if err := preview.loadSourcePage(i); err != nil {
				return nil, err
			}
			for j := range added[i].Content.Layer {
				for k := range added[i].Content.Layer[j].Objects {
					object := &added[i].Content.Layer[j].Objects[k]
					id := editorObjectID(*object)
					object.state = states[id]
					if object.Type == "TextObject" {
						object.TextObject.layout = object.state.layout
					}
					object.CompositeGraphicUnit.states = groups[id]
				}
			}
		}
	}
	if err := progress.report("commit", 0, 0); err != nil {
		return nil, err
	}
	e.bindImportResources()
	e.source, e.maxID = next, m.maximum
	next.idsReady = true
	e.outlines = outlines
	e.pages = slices.Insert(e.pages, at, added...)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) {
			for i := range added {
				added[i] = copyEditorPage(e.pages[at+i])
			}
			e.pages = slices.Delete(e.pages, at, at+len(added))
			e.source = base
			e.outlines = beforeOutlines
		}
		change.redo = func(e *Editor) {
			pages := make([]PageContent, len(added))
			for i := range added {
				pages[i] = copyEditorPage(added[i])
			}
			e.pages = slices.Insert(e.pages, at, pages...)
			e.source = next
			e.outlines = outlines
		}
	}
	return ids, nil
}

// bindImportResources 将新建文档资源路径绑定到包根，保持迁移前后的解析一致
func (e *Editor) bindImportResources() {
	if e.source != nil {
		return
	}
	for i := range e.resources {
		resource := &e.resources[i]
		if resource.font != nil {
			resource.font = cloneEditorData(resource.font)
			if resource.name != "" {
				resource.font.FontFile = "/" + resource.name
			}
		}
		if resource.image != nil {
			resource.image = cloneEditorData(resource.image)
			resource.image.MediaFile = "/" + resource.name
		}
	}
}

// prepareImport 建立页面和对象迁移共用的资源图，不修改目标编辑器
// 入参: source 来源, doc 来源文档, selected 页面映射范围, copyPage 是否同文档, progress 进度
// 返回: *editorSource 目标基础, *editorPageImport 资源图, error 错误信息
func (e *Editor) prepareImport(source *Reader, doc *Document, selected map[string]bool, copyPage bool, progress editorProgress) (*editorSource, *editorPageImport, error) {
	maximum, err := e.sourceMaxID(progress)
	if err != nil {
		return nil, nil, err
	}
	base := e.source
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return nil, nil, err
		}
	}
	m := &editorPageImport{reader: source, doc: doc, directory: e.packageDirectory(), target: base.reader, maximum: maximum, copyPage: copyPage, progress: progress,
		paths: make(map[string]string), evidence: make(map[string]string), ids: make(map[string]string), pages: selected,
		files: make(map[string][]byte), resources: make(map[string]editorImportEntry), used: make(map[string][]byte),
		templates: make(map[string]TemplatePage), templateParts: make(map[string][]byte), attachments: make(map[string][]byte)}
	for _, resource := range e.resources {
		m.files[resource.name] = nil
	}
	m.documentXML, err = m.readFile(source.ResPath(source.OFD.DocBody[0].DocRoot))
	if err != nil {
		return nil, nil, err
	}
	m.documentRoot, err = parseEditorXML(m.documentXML)
	if err != nil {
		return nil, nil, err
	}
	if copyPage {
		for _, page := range doc.Pages.Page {
			if !selected[page.ID] {
				m.ids[page.ID] = page.ID
			}
		}
	}
	for _, name := range append(slices.Clone(doc.CommonData.PublicRes), doc.CommonData.DocumentRes...) {
		if err := m.resourceIndex(source.ResPath(name)); err != nil {
			return nil, nil, err
		}
	}
	for _, template := range doc.CommonData.TemplatePage {
		m.templates[template.ID] = template
	}
	if doc.CommonData.DefaultCS != 0 {
		m.defaultCS, err = m.reference(strconv.Itoa(doc.CommonData.DefaultCS))
		if err != nil {
			return nil, nil, err
		}
	} else if !e.sourceRGB() {
		m.defaultCS = m.id("defaultRGB")
		m.used["defaultRGB"] = []byte(`<ofd:ColorSpace ID="` + m.defaultCS + `" Type="RGB" BitsPerComponent="8"/>`)
		m.resources["defaultRGB"] = editorImportEntry{group: "ColorSpaces"}
	}
	return base, m, nil
}

// encodeResources 编码本次迁移实际引用的资源，不包含未使用的资源定义
// 返回: error 编码错误
func (m *editorPageImport) encodeResources() error {
	var content []byte
	ids := slices.Sorted(maps.Keys(m.used))
	for _, group := range []string{"ColorSpaces", "DrawParams", "Fonts", "MultiMedias", "CompositeGraphicUnits"} {
		var entries []byte
		for _, id := range ids {
			if m.resources[id].group == group {
				entries = append(entries, m.used[id]...)
			}
		}
		if len(entries) == 0 {
			continue
		}
		encoded, err := editorXMLContainer(group, nil, entries)
		if err != nil {
			return err
		}
		content = append(content, encoded...)
	}
	if len(content) == 0 {
		return nil
	}
	var err error
	m.resourceXML, err = editorXMLContainer("Res", nil, content)
	return err
}

// readFile 分块读取导入条目，允许资源解压过程中取消，不共享来源内存
// 入参: name 包内路径
// 返回: []byte 条目内容, error 错误信息
func (m *editorPageImport) readFile(name string) ([]byte, error) {
	input, err := m.reader.openFile(name)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	return io.ReadAll(&editorProgressReader{Reader: input, progress: m.progress, stage: "resources"})
}

// editorProgressReader 为输入流提供可取消的分块读取检查点
type editorProgressReader struct {
	io.Reader
	progress  editorProgress
	stage     string
	completed int
}

// Read 读取数据并在下一块开始前检查取消
// 入参: data 接收缓冲区
// 返回: int 读取字节数, error 错误信息
func (r *editorProgressReader) Read(data []byte) (int, error) {
	if err := r.progress.report(r.stage, r.completed, 0); err != nil {
		return 0, err
	}
	n, err := r.Reader.Read(data[:min(len(data), 1<<20)])
	r.completed += n
	return n, err
}

// outlines 筛选指向导入页面的目录树并复用标准动作迁移
// 返回: []byte 目录片段, error 错误信息
func (m *editorPageImport) outlines() ([]byte, error) {
	name := m.reader.ResPath(m.reader.OFD.DocBody[0].DocRoot)
	data, err := m.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	root = root.child("Outlines")
	if root == nil {
		return nil, nil
	}
	bookmarks := make(map[string]Dest, len(m.doc.Bookmarks.Bookmark))
	for _, bookmark := range m.doc.Bookmarks.Bookmark {
		bookmarks[bookmark.Name] = bookmark.Dest
	}
	var patches []editorXMLPatch
	var keep func(*editorXML) (bool, error)
	keep = func(node *editorXML) (bool, error) {
		start, selected := len(patches), false
		if actions := node.child("Actions"); actions != nil {
			var value struct {
				Action []Action `xml:"Action"`
			}
			if err := xml.Unmarshal(data[actions.start:actions.end], &value); err != nil {
				return false, err
			}
			for _, action := range value.Action {
				if action.Goto != nil {
					if dest := gotoDest(action.Goto, bookmarks); dest != nil && m.pages[dest.PageID] {
						selected = true
					}
				}
			}
		}
		for _, child := range editorOutlineChildren(node) {
			retained, err := keep(child)
			if err != nil {
				return false, err
			}
			selected = selected || retained
		}
		if !selected {
			patches = append(patches[:start], editorXMLPatch{node.start, node.end, nil})
		}
		return selected, nil
	}
	for _, node := range editorOutlineChildren(root) {
		if _, err := keep(node); err != nil {
			return nil, err
		}
	}
	data = editorPatchXML(data, patches)
	root, err = parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	root = root.child("Outlines")
	if len(editorOutlineChildren(root)) == 0 {
		return nil, nil
	}
	var validate func(*editorXML) error
	validate = func(node *editorXML) error {
		for _, attr := range node.attrs {
			if attr.Name.Space == "" || attr.Name.Space == "xmlns" {
				continue
			}
			if attr.Name.Space == "http://www.w3.org/2001/XMLSchema-instance" && (attr.Name.Local == "schemaLocation" || attr.Name.Local == "noNamespaceSchemaLocation") {
				continue
			}
			return fmt.Errorf("cannot remap outline extension attribute %q", attr.Name.Local)
		}
		for _, child := range node.children {
			if err := validate(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validate(root); err != nil {
		return nil, err
	}
	return m.encode(editorImportEntry{name: name, data: data}, root)
}

// appendImportedOutlines 将已迁移目录追加到目标根目录，规范计数且不修改当前编辑器
// 入参: imported 已迁移目录片段
// 返回: []byte 合并后的目录, error 错误信息
func (e *Editor) appendImportedOutlines(imported []byte) ([]byte, error) {
	if len(imported) == 0 {
		return e.outlines, nil
	}
	added, err := parseEditorXML(imported)
	if err != nil {
		return nil, err
	}
	data, err := e.outlineXML()
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var content []byte
	if root.open != root.end {
		content = bytes.Clone(data[root.open:root.close])
	}
	for _, child := range editorOutlineChildren(added) {
		value, err := editorXMLStandalone(imported[child.start:child.end], child)
		if err != nil {
			return nil, err
		}
		content = append(content, value...)
	}
	preview := *e
	preview.historyLimit = 0
	if err := preview.setOutlineXML(editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, content)})); err != nil {
		return nil, err
	}
	return preview.outlines, nil
}

// importBase 为新建文档提供空的保真包基底，不重复保存已创建的页面和资源
// 返回: *editorSource 包基底, error 错误信息
func (e *Editor) importBase() (*editorSource, error) {
	seed := NewEditor()
	seed.Info = e.Info
	seed.pages = []PageContent{{Area: PageArea{PhysicalBox: "0 0 210 297"}}}
	files := make(map[string][]byte)
	if err := seed.writeParts(func(name string, data []byte, _ bool) error {
		if name == "OFD.xml" || name == "Doc_0/Document.xml" {
			files[name] = data
		}
		return nil
	}, nil); err != nil {
		return nil, err
	}
	data := files["Doc_0/Document.xml"]
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	files["Doc_0/Document.xml"] = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root.child("Pages"), nil)})
	reader := &Reader{files: files}
	if err := reader.initRoot(); err != nil {
		return nil, err
	}
	editor, err := reader.Editor()
	if err != nil {
		return nil, err
	}
	editor.source.idsReady = true
	return editor.source, nil
}

// id 为来源标识分配稳定且不与目标冲突的新标识
// 入参: value 来源标识
// 返回: string 新标识
func (m *editorPageImport) id(value string) string {
	if id := m.ids[value]; id != "" {
		return id
	}
	m.maximum++
	id := strconv.Itoa(m.maximum)
	m.ids[value] = id
	return id
}

// resourceIndex 登记资源定义，按引用延迟复制资源内容
// 入参: name 资源索引路径
// 返回: error 错误信息
func (m *editorPageImport) resourceIndex(name string) error {
	data, err := m.readFile(name)
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	if root.name.Local != "Res" {
		return fmt.Errorf("invalid resource index %q", name)
	}
	for _, group := range root.children {
		for _, node := range group.children {
			m.resources[node.attr("ID")] = editorImportEntry{name: name, base: root.attr("BaseLoc"), group: group.name.Local, data: data, node: node}
		}
	}
	return nil
}

// reference 复制被引用的资源及其传递依赖
// 入参: value 来源资源标识
// 返回: string 新标识, error 错误信息
func (m *editorPageImport) reference(value string) (string, error) {
	if m.copyPage {
		if _, ok := m.resources[value]; ok {
			return value, nil
		}
		if _, ok := m.templates[value]; ok {
			return value, nil
		}
	}
	if entry, ok := m.resources[value]; ok {
		if _, seen := m.used[value]; !seen {
			m.used[value] = nil
			data, err := m.encode(entry, entry.node)
			if err != nil {
				return "", err
			}
			m.used[value] = data
		}
	}
	if template, ok := m.templates[value]; ok {
		if _, seen := m.templateParts[value]; !seen {
			m.templateParts[value] = nil
			name, err := m.copyXML(m.reader.ResPath(template.BaseLoc), path.Join(m.directory, "Templates", "Template_"+m.id(value), "Content.xml"))
			if err != nil {
				return "", err
			}
			data, err := m.documentReference("CommonData", "TemplatePage", value, m.id(value), name)
			if err != nil {
				return "", err
			}
			m.templateParts[value] = bytes.TrimPrefix(data, []byte(xml.Header))
		}
	}
	return m.id(value), nil
}

// documentReference 迁移页面或模板引用，只替换标识与路径并保留未知属性
// 入参: group 父节点名称, kind 引用节点名称, source 原标识, id 新标识, loc 新路径
// 返回: []byte 引用XML, error 错误信息
func (m *editorPageImport) documentReference(group, kind, source, id, loc string) ([]byte, error) {
	if parent := m.documentRoot.child(group); parent != nil {
		for _, node := range parent.children {
			if node.name.Local != kind || node.attr("ID") != source {
				continue
			}
			data, err := editorXMLStandalone(m.documentXML[node.start:node.end], node)
			if err != nil {
				return nil, err
			}
			for _, attr := range [][2]string{{"ID", id}, {"BaseLoc", loc}} {
				root, err := parseEditorXML(data)
				if err != nil {
					return nil, err
				}
				data, err = editorXMLAttribute(data, root, attr[0], attr[1])
				if err != nil {
					return nil, err
				}
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("missing %s reference %q", kind, source)
}

// resolve 按所在XML解析包内路径，兼容阅读器接受的文档相对路径
// 入参: name 所在XML, base 资源目录, value 路径值
// 返回: string 包内路径, error 错误信息
func (m *editorPageImport) resolve(name, base, value string) (string, error) {
	for _, candidate := range []string{resolveResourcePath(name, base, value), m.reader.ResPath(value)} {
		if m.reader.files[candidate] != nil {
			return candidate, nil
		}
		if file, ok := m.reader.packageFile(candidate); ok {
			return cleanPackagePath(file.Name), nil
		}
	}
	return "", fmt.Errorf("import reference %q in %s not found", value, name)
}

// copyData 复制二进制资源或签名引用原文，凭据与当前引用使用独立映射
// 入参: name 来源路径, relative 文档内候选路径, evidence 是否原始签名凭据
// 返回: string 新路径, error 错误信息
func (m *editorPageImport) copyData(name, relative string, evidence bool) (string, error) {
	paths := m.paths
	if evidence {
		paths = m.evidence
		relative = path.Join("Signs", "Signed", path.Base(name))
	}
	key := strings.ToLower(cleanPackagePath(name))
	if target := paths[key]; target != "" {
		return "/" + target, nil
	}
	if m.copyPage && !evidence {
		return "/" + cleanPackagePath(name), nil
	}
	target := packageAvailableName(m.target, m.files, path.Join(m.directory, relative))
	data, err := m.readFile(name)
	if err != nil {
		return "", err
	}
	paths[key], m.files[target] = target, data
	return "/" + target, nil
}

// copyXML 复制页面或标准关联XML并重映射引用
// 入参: name 来源路径, candidate 目标候选路径
// 返回: string 新路径, error 错误信息
func (m *editorPageImport) copyXML(name, candidate string) (string, error) {
	key := strings.ToLower(cleanPackagePath(name))
	if target := m.paths[key]; target != "" {
		return "/" + target, nil
	}
	target := packageAvailableName(m.target, m.files, candidate)
	data, err := m.readFile(name)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	m.paths[key], m.files[target] = target, nil
	if root.name.Local == "Page" {
		for _, child := range root.children {
			if child.name.Local == "PageRes" {
				loc, err := m.resolve(name, "", editorImportText(data, child))
				if err != nil {
					return "", err
				}
				if err := m.resourceIndex(loc); err != nil {
					return "", err
				}
			}
		}
	}
	encoded, err := m.encode(editorImportEntry{name: name, data: data}, root)
	if err != nil {
		return "", err
	}
	m.files[target] = append([]byte(xml.Header), encoded...)
	return "/" + target, nil
}

// editorImportText 获取节点直接文本，解码XML实体
// 入参: data XML, node 节点
// 返回: string 文本
func editorImportText(data []byte, node *editorXML) string {
	var value struct {
		Text string `xml:",chardata"`
	}
	_ = xml.Unmarshal(data[node.start:node.end], &value)
	return value.Text
}

// encode 编码标准OFD节点，保留文字数据和几何值，仅改写资源、标识及路径
// 入参: entry 来源上下文, node 当前节点
// 返回: []byte 独立XML片段, error 错误信息
func (m *editorPageImport) encode(entry editorImportEntry, node *editorXML) ([]byte, error) {
	if node.name.Space != "" && node.name.Space != ofdNamespace && node.name.Space != "http://www.ofdspec.org" {
		return nil, fmt.Errorf("cannot remap extension namespace %q", node.name.Space)
	}
	name := node.name.Local
	if name == "PageRes" {
		if m.copyPage {
			loc, err := m.resolve(entry.name, "", strings.TrimSpace(editorImportText(entry.data, node)))
			if err != nil {
				return nil, err
			}
			return editorXMLText("PageRes", "/"+loc), nil
		}
		return nil, nil
	}
	if name == "StampAnnot" && !m.pages[node.attr("PageRef")] {
		return nil, nil
	}
	if name == "Action" {
		if gotoNode := node.child("Goto"); gotoNode != nil {
			var action Goto
			if err := xml.Unmarshal(entry.data[gotoNode.start:gotoNode.end], &action); err != nil {
				return nil, err
			}
			bookmarks := make(map[string]Dest)
			for _, bookmark := range m.doc.Bookmarks.Bookmark {
				bookmarks[bookmark.Name] = bookmark.Dest
			}
			if dest := gotoDest(&action, bookmarks); !m.copyPage && (dest == nil || !m.pages[dest.PageID]) {
				if m.strictLinks {
					return nil, fmt.Errorf("object link targets a page outside the imported selection")
				}
				return nil, nil
			}
		}
	}
	var attrs ofdAttrs
	for _, attr := range node.attrs {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
			continue
		}
		if attr.Name.Space != "" {
			attrs = append(attrs, attr)
			continue
		}
		key, value := attr.Name.Local, attr.Value
		var err error
		switch key {
		case "ID":
			if name == "Signature" || name == "StampAnnot" {
				value = name + ":" + value
			}
			value = m.id(value)
		case "PageID":
			if !m.copyPage || m.pages[value] {
				value, err = m.reference(value)
			}
		case "Font", "ResourceID", "Substitution", "ImageMask", "Relative", "DrawParam", "ColorSpace", "Thumbnail", "TemplateID", "PageRef", "RefId":
			value, err = m.reference(value)
		case "BaseLoc", "FileRef":
			loc, resolveErr := m.resolve(entry.name, "", value)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if key == "FileRef" {
				value, err = m.copyData(loc, "", true)
			} else {
				directory := "Res"
				if name == "Signature" {
					directory = path.Join("Signs", "Sign_"+m.id("Signature:"+node.attr("ID")))
				}
				value, err = m.copyXML(loc, path.Join(m.directory, directory, path.Base(loc)))
			}
		case "AttachID":
			value, err = m.attachment(value)
		}
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: key}, Value: value})
	}
	if m.defaultCS != "" && (name == "FillColor" || name == "StrokeColor" || name == "Color") && node.attr("ColorSpace") == "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "ColorSpace"}, Value: m.defaultCS})
	}
	var content []byte
	pageContent := name == "Page" && node.parent == nil
	if pageContent {
		area := m.doc.CommonData.PageArea
		local := node.child("Area")
		var fields []byte
		for _, field := range [][2]string{{"PhysicalBox", area.PhysicalBox}, {"ApplicationBox", area.ApplicationBox}, {"ContentBox", area.ContentBox}, {"BleedBox", area.BleedBox}} {
			if field[1] != "" && (local == nil || local.child(field[0]) == nil) {
				fields = append(fields, editorXMLText(field[0], field[1])...)
			}
		}
		if local == nil {
			encoded, err := editorXMLContainer("Area", nil, fields)
			if err != nil {
				return nil, err
			}
			content = encoded
		} else {
			encoded, err := m.encode(entry, local)
			if err != nil {
				return nil, err
			}
			root, err := parseEditorXML(encoded)
			if err != nil {
				return nil, err
			}
			if root.open != root.end {
				fields = append(bytes.Clone(encoded[root.open:root.close]), fields...)
			}
			content = editorPatchXML(encoded, []editorXMLPatch{editorXMLContent(encoded, root, fields)})
		}
	}
	for _, child := range node.children {
		if pageContent && child.name.Local == "Area" {
			continue
		}
		if name == "Goto" && child.name.Local == "Bookmark" {
			for _, bookmark := range m.doc.Bookmarks.Bookmark {
				if bookmark.Name != child.attr("Name") {
					continue
				}
				dest := bookmark.Dest
				encoded, err := xml.Marshal(struct {
					XMLName xml.Name `xml:"Dest"`
					Dest
				}{Dest: dest})
				if err != nil {
					return nil, err
				}
				root, err := parseEditorXML(encoded)
				if err != nil {
					return nil, err
				}
				encoded, err = m.encode(editorImportEntry{data: encoded}, root)
				if err != nil {
					return nil, err
				}
				content = append(content, encoded...)
			}
			continue
		}
		encoded, err := m.encode(entry, child)
		if err != nil {
			return nil, err
		}
		content = append(content, encoded...)
	}
	if len(node.children) == 0 {
		value := editorImportText(entry.data, node)
		var err error
		switch name {
		case "FontFile", "MediaFile", "Profile", "SignedValue", "BaseLoc":
			loc, resolveErr := m.resolve(entry.name, entry.base, strings.TrimSpace(value))
			if resolveErr != nil {
				return nil, resolveErr
			}
			directory := "Res"
			file := path.Base(loc)
			owner := node.parent.attr("ID")
			switch name {
			case "FontFile":
				directory = "Res/Fonts"
				if owner != "" {
					file = "Font_" + m.id(owner) + strings.ToLower(path.Ext(loc))
				}
			case "MediaFile":
				directory = "Res/Images"
				if node.parent.attr("Type") != "Image" {
					directory = "Res/Media"
				} else if owner != "" {
					file = "Image_" + m.id(owner) + strings.ToLower(path.Ext(loc))
				}
			case "Profile":
				directory = "Res/ColorSpaces"
			case "SignedValue", "BaseLoc":
				directory = "Signs"
			}
			value, err = m.copyData(loc, path.Join(directory, file), false)
		case "FileLoc":
			loc, resolveErr := m.resolve(entry.name, "", strings.TrimSpace(value))
			if resolveErr != nil {
				return nil, resolveErr
			}
			if node.parent.name.Local == "Attachment" {
				value, err = m.copyData(loc, path.Join("Attachments", path.Base(loc)), false)
			} else {
				candidate := path.Join(m.directory, "Annotations", "Page_"+m.id(node.parent.attr("PageID"))+".xml")
				value, err = m.copyXML(loc, candidate)
			}
		case "Font", "Substitution":
			value, err = m.reference(strings.TrimSpace(value))
		}
		if err != nil {
			return nil, err
		}
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
			return nil, err
		}
		content = append(content, escaped.Bytes()...)
	}
	encoded, err := editorXMLContainer(name, attrs, content)
	return bytes.TrimPrefix(encoded, []byte(xml.Header)), err
}

// annotations 复制所选页面的注释索引
// 返回: []byte 索引条目, error 错误信息
func (m *editorPageImport) annotations() ([]byte, error) {
	if m.doc.Annotations == "" {
		return nil, nil
	}
	name := m.reader.ResPath(m.doc.Annotations)
	data, err := m.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var entries []byte
	for _, node := range root.children {
		if !m.pages[node.attr("PageID")] {
			continue
		}
		encoded, err := m.encode(editorImportEntry{name: name, data: data}, node)
		if err != nil {
			return nil, err
		}
		entries = append(entries, encoded...)
	}
	return entries, nil
}

// attachment 复制页面动作引用的附件，不导入无关附件
// 入参: id 来源附件标识
// 返回: string 新标识, error 错误信息
func (m *editorPageImport) attachment(id string) (string, error) {
	if m.copyPage {
		return id, nil
	}
	if _, exists := m.attachments[id]; exists {
		return m.id(id), nil
	}
	name := m.reader.ResPath(m.reader.OFD.DocBody[0].DocRoot)
	if m.doc.Attachments.Path != "" {
		name = m.reader.ResPath(m.doc.Attachments.Path)
	}
	data, err := m.readFile(name)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	if m.doc.Attachments.Path == "" {
		root = root.child("Attachments")
	}
	if root != nil {
		for _, node := range root.children {
			if node.name.Local == "Attachment" && node.attr("ID") == id {
				m.attachments[id], err = m.encode(editorImportEntry{name: name, data: data}, node)
				return m.id(id), err
			}
		}
	}
	return "", fmt.Errorf("attachment %q not found", id)
}

// signatures 复制所选页面的签章及原始验证凭据，过滤其他页面的签章位置
// 返回: []byte 签名索引条目, error 错误信息
func (m *editorPageImport) signatures() ([]byte, error) {
	if m.doc.Signatures == "" {
		return nil, nil
	}
	name := m.reader.ResPath(m.doc.Signatures)
	data, err := m.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var entries []byte
	for _, node := range root.children {
		if node.name.Local != "Signature" {
			continue
		}
		loc, err := m.resolve(name, "", node.attr("BaseLoc"))
		if err != nil {
			return nil, err
		}
		value, err := m.readFile(loc)
		if err != nil {
			return nil, err
		}
		var signature SignatureFile
		if err := xml.Unmarshal(value, &signature); err != nil {
			return nil, err
		}
		matched := false
		for _, stamp := range signature.SignedInfo.StampAnnot {
			matched = matched || m.pages[stamp.PageRef]
		}
		if !matched {
			continue
		}
		encoded, err := m.encode(editorImportEntry{name: name, data: data}, node)
		if err != nil {
			return nil, err
		}
		entries = append(entries, encoded...)
	}
	return entries, nil
}

// merge 合并包索引及资源图，原有页面和元数据保持原文
// 入参: base 目标基底, refs 插入页面, annotations 注释索引, signatures 签名索引
// 返回: *editorSource 新基底, error 错误信息
func (m *editorPageImport) merge(base *editorSource, refs []Page, annotations, signatures []byte) (*editorSource, error) {
	files := maps.Clone(base.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, m.files)
	name := base.reader.ResPath(base.reader.OFD.DocBody[0].DocRoot)
	if file, ok := base.reader.packageFile(name); ok {
		name = cleanPackagePath(file.Name)
	}
	data, err := base.reader.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	common := root.child("CommonData")
	if common == nil {
		return nil, fmt.Errorf("document has no CommonData")
	}
	position := common.close
	for _, node := range common.children {
		if node.name.Local == "TemplatePage" || node.name.Local == "DefaultCS" {
			position = node.start
			break
		}
	}
	var content []byte
	if len(m.resourceXML) != 0 {
		name, added, err := mergePackageResources(base.reader, files, base.document, m.directory, m.resourceXML)
		if err != nil {
			return nil, err
		}
		if added {
			content = editorXMLText("DocumentRes", "/"+strings.TrimPrefix(name, "/"))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.templateParts)) {
		content = append(content, m.templateParts[id]...)
	}
	patches := []editorXMLPatch{{position, position, content}}
	patch := editorXMLPatch{common.open, common.open, editorXMLText("MaxUnitID", strconv.Itoa(m.maximum))}
	if maximum := common.child("MaxUnitID"); maximum != nil {
		patch = editorXMLContent(data, maximum, []byte(strconv.Itoa(m.maximum)))
	}
	patches = append(patches, patch)
	pages := root.child("Pages")
	if pages == nil {
		return nil, fmt.Errorf("document has no Pages")
	}
	if len(m.pageParts) != 0 {
		if pages.open == pages.end {
			patches = append(patches, editorXMLContent(data, pages, m.pageParts))
		} else {
			patches = append(patches, editorXMLPatch{pages.close, pages.close, m.pageParts})
		}
	}
	data = editorPatchXML(data, patches)
	if len(annotations) != 0 {
		loc, err := mergeImportIndex(base.reader, files, base.document.Annotations, path.Join(m.directory, "Annotations.xml"), "Annotations", annotations)
		if err != nil {
			return nil, err
		}
		if base.document.Annotations == "" {
			root, _ = parseEditorXML(data)
			data = editorXMLSetText(data, root, [][2]string{{"Annotations", loc}})
		}
	}
	if len(m.attachments) != 0 {
		var entries []byte
		for _, id := range slices.Sorted(maps.Keys(m.attachments)) {
			entries = append(entries, m.attachments[id]...)
		}
		root, _ = parseEditorXML(data)
		node := root.child("Attachments")
		if node != nil && len(node.children) != 0 {
			content := append(bytes.Clone(data[node.open:node.close]), entries...)
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, content)})
		} else {
			loc, err := mergeImportIndex(base.reader, files, base.document.Attachments.Path, path.Join(m.directory, "Attachments.xml"), "Attachments", entries)
			if err != nil {
				return nil, err
			}
			if base.document.Attachments.Path == "" {
				data = editorXMLSetText(data, root, [][2]string{{"Attachments", loc}})
			}
		}
	}
	files[name] = data
	if len(signatures) != 0 {
		loc, err := mergeImportIndex(base.reader, files, base.document.Signatures, path.Join(m.directory, "Signs", "Signatures.xml"), "Signatures", signatures)
		if err != nil {
			return nil, err
		}
		if base.document.Signatures == "" {
			ofd, err := base.reader.readFile("OFD.xml")
			if err != nil {
				return nil, err
			}
			root, err := parseEditorXML(ofd)
			if err != nil {
				return nil, err
			}
			name := "OFD.xml"
			if file, ok := base.reader.packageFile(name); ok {
				name = cleanPackagePath(file.Name)
			}
			files[name] = editorXMLSetText(ofd, root.child("DocBody"), [][2]string{{"Signatures", loc}})
		}
	}
	reader := &Reader{Zip: base.reader.Zip, files: files}
	if err := reader.initRoot(); err != nil {
		return nil, err
	}
	doc, err := reader.Doc()
	if err != nil {
		return nil, err
	}
	next := *base
	next.reader, next.document = reader, doc
	next.pages, next.origins = maps.Clone(base.pages), maps.Clone(base.origins)
	for id, page := range next.pages {
		if page.original == nil {
			next.pages[id] = &editorSourcePage{ref: page.ref}
		}
	}
	for _, ref := range refs {
		next.pages[ref.ID] = &editorSourcePage{ref: ref}
	}
	next.idsReady = true
	return &next, nil
}

// mergeImportIndex 向原索引追加条目，不移动原条目的相对路径基准
// 入参: reader 目标输入包, files 新条目, loc 原索引, fallback 新索引路径, rootName 根节点名称, entries 新条目
// 返回: string 索引绝对路径, error 错误信息
func mergeImportIndex(reader *Reader, files map[string][]byte, loc, fallback, rootName string, entries []byte) (string, error) {
	name := fallback
	var data []byte
	var err error
	if loc == "" {
		name = packageAvailableName(reader, files, fallback)
		data, err = editorXMLContainer(rootName, nil, entries)
	} else {
		name, err = editorPageLocation(reader, files, "", "/"+reader.ResPath(loc))
		if err != nil {
			return "", err
		}
		var exists bool
		data, exists = files[name]
		if !exists {
			data, err = reader.readFile(name)
		}
		if err != nil {
			return "", err
		}
		root, parseErr := parseEditorXML(data)
		if parseErr != nil {
			return "", parseErr
		}
		if root.open == root.end {
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, entries)})
		} else {
			data = editorPatchXML(data, []editorXMLPatch{{root.close, root.close, entries}})
		}
	}
	if err != nil {
		return "", err
	}
	if rootName == "Signatures" {
		root, err := parseEditorXML(data)
		if err != nil {
			return "", err
		}
		maximum := 0
		for _, node := range root.children {
			value := node.attr("ID")
			if node.name.Local == "MaxSignId" {
				value = editorImportText(data, node)
			}
			id, _ := strconv.Atoi(value)
			maximum = max(maximum, id)
		}
		value := editorXMLText("MaxSignId", strconv.Itoa(maximum))
		patch := editorXMLPatch{root.open, root.open, value}
		if node := root.child("MaxSignId"); node != nil {
			patch = editorXMLPatch{node.start, node.end, value}
		}
		data = editorPatchXML(data, []editorXMLPatch{patch})
	}
	files[name] = data
	return "/" + name, nil
}
