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
	prefix        string
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
}

// ImportPages 将来源主文档中的指定页面插入目标位置，保持输入顺序并作为一次撤销操作
// 页面索引从0开始，at可等于当前页数；复制关联模板、注释、签章外观和实际引用的资源
// 不导入来源元数据和目录，指向未选页面的跳转被移除；签名数据仅保留原始凭据，不代表合并后文档有效
// 返回后可关闭来源Reader，目标原Reader的生命周期要求不变
// 入参: source 来源阅读器, indexes 来源页面索引，不可重复, at 目标插入位置
// 返回: []string 新页面标识, error 错误信息
func (e *Editor) ImportPages(source *Reader, indexes []int, at int) ([]string, error) {
	return e.importPages(source, indexes, at, false)
}

// importPages 迁移页面，同文档复制时复用原资源并保留其他页面的跳转
// 入参: source 来源阅读器, indexes 来源页索引, at 插入位置, copyPage 是否同文档复制
// 返回: []string 新页标识, error 错误信息
func (e *Editor) importPages(source *Reader, indexes []int, at int, copyPage bool) ([]string, error) {
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
	if err := e.prepareSourceIDs(); err != nil {
		return nil, err
	}
	base := e.source
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return nil, err
		}
	}
	prefix := path.Join(base.directory, "Import_"+strconv.Itoa(e.maxID+1))
	for editorDirectoryExists(base.reader, prefix) {
		prefix += "_"
	}
	m := &editorPageImport{reader: source, doc: doc, prefix: prefix, maximum: e.maxID, copyPage: copyPage,
		ids: make(map[string]string), pages: selected, files: make(map[string][]byte), resources: make(map[string]editorImportEntry), used: make(map[string][]byte), templates: make(map[string]TemplatePage), templateParts: make(map[string][]byte), attachments: make(map[string][]byte)}
	if copyPage {
		for _, page := range doc.Pages.Page {
			if !selected[page.ID] {
				m.ids[page.ID] = page.ID
			}
		}
	}
	for _, name := range append(slices.Clone(doc.CommonData.PublicRes), doc.CommonData.DocumentRes...) {
		if err := m.resourceIndex(source.ResPath(name)); err != nil {
			return nil, err
		}
	}
	for _, template := range doc.CommonData.TemplatePage {
		m.templates[template.ID] = template
	}
	if doc.CommonData.DefaultCS != 0 {
		m.defaultCS, err = m.reference(strconv.Itoa(doc.CommonData.DefaultCS))
		if err != nil {
			return nil, err
		}
	} else if !e.sourceRGB() {
		m.defaultCS = m.id("defaultRGB")
		m.used["defaultRGB"] = []byte(`<ofd:ColorSpace ID="` + m.defaultCS + `" Type="RGB" BitsPerComponent="8"/>`)
		m.resources["defaultRGB"] = editorImportEntry{group: "ColorSpaces"}
	}
	refs := make([]Page, len(indexes))
	ids := make([]string, len(indexes))
	for i, index := range indexes {
		page := doc.Pages.Page[index]
		ids[i] = m.id(page.ID)
		name, err := m.copyXML(source.ResPath(page.BaseLoc))
		if err != nil {
			return nil, err
		}
		refs[i] = Page{ID: ids[i], BaseLoc: name}
	}
	annotations, err := m.annotations()
	if err != nil {
		return nil, err
	}
	signatures, err := m.signatures()
	if err != nil {
		return nil, err
	}
	if !copyPage {
		var resourceContent []byte
		for _, group := range []string{"ColorSpaces", "DrawParams", "Fonts", "MultiMedias", "CompositeGraphicUnits"} {
			var entries []byte
			for _, id := range slices.Sorted(maps.Keys(m.used)) {
				if m.resources[id].group == group {
					entries = append(entries, m.used[id]...)
				}
			}
			if len(entries) == 0 {
				continue
			}
			encoded, err := editorXMLContainer(group, nil, entries)
			if err != nil {
				return nil, err
			}
			resourceContent = append(resourceContent, encoded...)
		}
		resources, err := editorXMLContainer("Res", nil, resourceContent)
		if err != nil {
			return nil, err
		}
		m.files[path.Join(prefix, "Resources.xml")] = resources
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
			layouts := make(map[string]*textLayout)
			for _, layer := range e.pages[index].Content.Layer {
				for _, object := range layer.Objects {
					if layout := object.TextObject.layout; object.Type == "TextObject" && layout != nil {
						value := *layout
						layouts[m.ids[object.TextObject.ID]] = &value
					}
				}
			}
			if err := preview.loadSourcePage(i); err != nil {
				return nil, err
			}
			for j := range added[i].Content.Layer {
				for k := range added[i].Content.Layer[j].Objects {
					object := &added[i].Content.Layer[j].Objects[k]
					if object.Type == "TextObject" {
						object.TextObject.layout = layouts[object.TextObject.ID]
					}
				}
			}
		}
	}
	if e.source == nil {
		for i := range e.resources {
			resource := &e.resources[i]
			if resource.font != nil {
				resource.font = cloneEditorData(resource.font)
				resource.font.FontFile = "/" + resource.name
			}
			if resource.image != nil {
				resource.image = cloneEditorData(resource.image)
				resource.image.MediaFile = "/" + resource.name
			}
		}
	}
	e.source, e.maxID = next, m.maximum
	e.pages = slices.Insert(e.pages, at, added...)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) {
			for i := range added {
				added[i] = copyEditorPage(e.pages[at+i])
			}
			e.pages = slices.Delete(e.pages, at, at+len(added))
			e.source = base
		}
		change.redo = func(e *Editor) {
			pages := make([]PageContent, len(added))
			for i := range added {
				pages[i] = copyEditorPage(added[i])
			}
			e.pages = slices.Insert(e.pages, at, pages...)
			e.source = next
		}
	}
	return ids, nil
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
	}); err != nil {
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
	data, err := m.reader.readFile(name)
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
			name, err := m.copyXML(m.reader.ResPath(template.BaseLoc))
			if err != nil {
				return "", err
			}
			data, err := editorXMLContainer("TemplatePage", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: m.id(value)}, {Name: xml.Name{Local: "Name"}, Value: template.Name}, {Name: xml.Name{Local: "BaseLoc"}, Value: name}, {Name: xml.Name{Local: "ZOrder"}, Value: template.ZOrder}}, nil)
			if err != nil {
				return "", err
			}
			m.templateParts[value] = bytes.TrimPrefix(data, []byte(xml.Header))
		}
	}
	return m.id(value), nil
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

// copyData 复制二进制资源或签名引用原文，返回绝对包路径
// 入参: name 来源路径, evidence 是否原始签名凭据
// 返回: string 新路径, error 错误信息
func (m *editorPageImport) copyData(name string, evidence bool) (string, error) {
	directory := m.prefix
	if evidence {
		directory = path.Join(directory, "Signed")
	}
	target := path.Join(directory, name)
	if _, exists := m.files[target]; !exists {
		data, err := m.reader.readFile(name)
		if err != nil {
			return "", err
		}
		m.files[target] = bytes.Clone(data)
	}
	return "/" + target, nil
}

// copyXML 复制页面或标准关联XML并重映射引用
// 入参: name 来源路径
// 返回: string 新路径, error 错误信息
func (m *editorPageImport) copyXML(name string) (string, error) {
	target := path.Join(m.prefix, name)
	if _, exists := m.files[target]; exists {
		return "/" + target, nil
	}
	data, err := m.reader.readFile(name)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	m.files[target] = nil
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
			if dest := gotoDest(&action, bookmarks); dest == nil || !m.pages[dest.PageID] && !m.copyPage {
				return nil, nil
			}
		}
	}
	var attrs ofdAttrs
	for _, attr := range node.attrs {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
			continue
		}
		if attr.Name.Space == "http://www.w3.org/2001/XMLSchema-instance" && (attr.Name.Local == "schemaLocation" || attr.Name.Local == "noNamespaceSchemaLocation") {
			continue
		}
		if attr.Name.Space != "" {
			return nil, fmt.Errorf("cannot remap extension attribute %q", attr.Name.Local)
		}
		key, value := attr.Name.Local, attr.Value
		var err error
		switch key {
		case "ID":
			if name == "Signature" || name == "StampAnnot" {
				value = name + ":" + value
			}
			value = m.id(value)
		case "Font", "ResourceID", "Substitution", "ImageMask", "Relative", "DrawParam", "ColorSpace", "Thumbnail", "TemplateID", "PageID", "PageRef", "RefId":
			value, err = m.reference(value)
		case "BaseLoc", "FileRef":
			loc, resolveErr := m.resolve(entry.name, "", value)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if key == "FileRef" {
				value, err = m.copyData(loc, true)
			} else {
				value, err = m.copyXML(loc)
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
	if name == "Page" {
		area := m.doc.CommonData.PageArea
		if local := node.child("Area"); local != nil {
			var own PageArea
			if err := xml.Unmarshal(entry.data[local.start:local.end], &own); err != nil {
				return nil, err
			}
			if own.PhysicalBox != "" {
				area.PhysicalBox = own.PhysicalBox
			}
			if own.ApplicationBox != "" {
				area.ApplicationBox = own.ApplicationBox
			}
			if own.ContentBox != "" {
				area.ContentBox = own.ContentBox
			}
			if own.BleedBox != "" {
				area.BleedBox = own.BleedBox
			}
		}
		var fields []byte
		for _, field := range [][2]string{{"PhysicalBox", area.PhysicalBox}, {"ApplicationBox", area.ApplicationBox}, {"ContentBox", area.ContentBox}, {"BleedBox", area.BleedBox}} {
			if field[1] != "" {
				fields = append(fields, editorXMLText(field[0], field[1])...)
			}
		}
		encoded, err := editorXMLContainer("Area", nil, fields)
		if err != nil {
			return nil, err
		}
		content = bytes.TrimPrefix(encoded, []byte(xml.Header))
	}
	for _, child := range node.children {
		if name == "Page" && child.name.Local == "Area" {
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
			value, err = m.copyData(loc, false)
		case "FileLoc":
			loc, resolveErr := m.resolve(entry.name, "", strings.TrimSpace(value))
			if resolveErr != nil {
				return nil, resolveErr
			}
			if node.parent.name.Local == "Attachment" {
				value, err = m.copyData(loc, false)
			} else {
				value, err = m.copyXML(loc)
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
	data, err := m.reader.readFile(name)
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
	data, err := m.reader.readFile(name)
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
	data, err := m.reader.readFile(name)
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
		value, err := m.reader.readFile(loc)
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
	if m.files[path.Join(m.prefix, "Resources.xml")] != nil {
		content = editorXMLText("DocumentRes", "/"+path.Join(m.prefix, "Resources.xml"))
	}
	for _, id := range slices.Sorted(maps.Keys(m.templateParts)) {
		content = append(content, m.templateParts[id]...)
	}
	data = editorPatchXML(data, []editorXMLPatch{{position, position, content}})
	if len(annotations) != 0 {
		loc, err := mergeImportIndex(base.reader, files, base.document.Annotations, path.Join(m.prefix, "Annotations.xml"), "Annotations", annotations)
		if err != nil {
			return nil, err
		}
		root, _ = parseEditorXML(data)
		data = editorXMLSetText(data, root, [][2]string{{"Annotations", loc}})
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
			loc, err := mergeImportIndex(base.reader, files, base.document.Attachments.Path, path.Join(m.prefix, "Attachments.xml"), "Attachments", entries)
			if err != nil {
				return nil, err
			}
			data = editorXMLSetText(data, root, [][2]string{{"Attachments", loc}})
		}
	}
	files[name] = data
	if len(signatures) != 0 {
		loc, err := mergeImportIndex(base.reader, files, base.document.Signatures, path.Join(m.prefix, "Signatures.xml"), "Signatures", signatures)
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
			files["OFD.xml"] = editorXMLSetText(ofd, root.child("DocBody"), [][2]string{{"Signatures", loc}})
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
		data, err = editorXMLContainer(rootName, nil, entries)
	} else {
		name = reader.ResPath(loc)
		data, err = reader.readFile(name)
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
