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
	"strings"
	"time"
)

// AddAnnotation 新增标准注解并分配独立标识，外观中的基本对象使用注解局部毫米坐标
// Appearance.Objects为空时使用分类对象集合，输入对象及资源不被修改
// 入参: index 页面索引, annotation 注解内容，ID和LastModDate由编辑器生成
// 返回: string 注解标识, error 错误信息
func (e *Editor) AddAnnotation(index int, annotation Annotation) (string, error) {
	var id string
	err := e.Transaction(func(next *Editor) error {
		if index < 0 || index >= len(next.pages) {
			return fmt.Errorf("page index %d out of range", index)
		}
		if !slices.Contains([]string{"Link", "Path", "Highlight", "Stamp", "Watermark"}, annotation.Type) {
			return fmt.Errorf("invalid annotation type %q", annotation.Type)
		}
		if _, err := creationBox(annotation.Appearance.Boundary); err != nil {
			return err
		}
		if err := next.prepareSourceIDs(); err != nil {
			return err
		}
		id = next.nextID()
		objects := annotation.Appearance.Objects
		if len(objects) == 0 {
			for _, object := range annotation.Appearance.TextObject {
				objects = append(objects, GraphicObject{Type: "TextObject", TextObject: object})
			}
			for _, object := range annotation.Appearance.PathObject {
				objects = append(objects, GraphicObject{Type: "PathObject", PathObject: object})
			}
			for _, object := range annotation.Appearance.ImageObject {
				objects = append(objects, GraphicObject{Type: "ImageObject", ImageObject: object})
			}
			if len(annotation.Appearance.CompositeGraphicUnit) != 0 {
				return fmt.Errorf("use composite copy to insert existing complex content")
			}
		}
		var content []byte
		for _, object := range objects {
			prepared, err := next.prepareObject(next.nextID(), cloneEditorData(object))
			if err != nil {
				return err
			}
			data, err := editorObjectXML(prepared)
			if err != nil {
				return err
			}
			content = append(content, bytes.TrimPrefix(data, []byte(xml.Header))...)
		}
		appearance, err := editorXMLContainer("Appearance", ofdAttrs{{Name: xml.Name{Local: "Boundary"}, Value: annotation.Appearance.Boundary}}, content)
		if err != nil {
			return err
		}
		attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}, {Name: xml.Name{Local: "Type"}, Value: annotation.Type},
			{Name: xml.Name{Local: "Creator"}, Value: annotation.Creator}, {Name: xml.Name{Local: "LastModDate"}, Value: time.Now().Format("2006-01-02")}}
		attrs.add("Subtype", annotation.Subtype)
		attrs.flag("Visible", annotation.Visible)
		if annotation.NoZoom {
			attrs.add("NoZoom", "true")
		}
		if annotation.NoRotate {
			attrs.add("NoRotate", "true")
		}
		content = nil
		if annotation.Remark != "" {
			content = editorXMLText("Remark", annotation.Remark)
		}
		content = append(content, bytes.TrimPrefix(appearance, []byte(xml.Header))...)
		data, err := editorXMLContainer("Annot", attrs, content)
		if err != nil {
			return err
		}
		return next.appendAnnotations(index, data)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Annotation 获取注解的独立内容快照，修改原注解应使用局部编辑方法以保留扩展字段
// 入参: index 页面索引, id 注解标识
// 返回: Annotation 注解内容, error 错误信息
func (e *Editor) Annotation(index int, id string) (Annotation, error) {
	data, err := e.annotationXML(index, id)
	var annotation Annotation
	if err == nil {
		err = xml.Unmarshal(data, &annotation)
	}
	return annotation, err
}

// annotationXML 读取唯一注解的独立原文，不以类型、大小或可见性筛选
// 入参: index 页面索引, id 注解标识
// 返回: []byte 注解XML, error 错误信息
func (e *Editor) annotationXML(index int, id string) ([]byte, error) {
	if index < 0 || index >= len(e.pages) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	if e.source == nil || e.source.document.Annotations == "" {
		return nil, fmt.Errorf("page has no annotations")
	}
	reader := e.source.reader
	name := reader.ResPath(e.source.document.Annotations)
	data, err := reader.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var result []byte
	for _, ref := range root.children {
		if ref.name.Space != root.name.Space || ref.name.Local != "Page" || ref.attr("PageID") != e.pages[index].ID || ref.child("FileLoc") == nil {
			continue
		}
		file, err := editorPageLocation(reader, nil, name, strings.TrimSpace(editorImportText(data, ref.child("FileLoc"))))
		if err != nil {
			return nil, err
		}
		content, err := reader.readFile(file)
		if err != nil {
			return nil, err
		}
		page, err := parseEditorXML(content)
		if err != nil {
			return nil, err
		}
		for _, node := range page.children {
			if node.name.Local != "Annot" || node.name.Space != page.name.Space || node.attr("ID") != id {
				continue
			}
			if result != nil {
				return nil, fmt.Errorf("ambiguous annotation ID %q", id)
			}
			result, err = editorXMLStandalone(content[node.start:node.end], node)
			if err != nil {
				return nil, err
			}
		}
	}
	if result == nil {
		return nil, fmt.Errorf("annotation %q not found", id)
	}
	return result, nil
}

// appendAnnotations 在独立页面注解文件末尾追加原文，保留原索引及未知内容
// 入参: index 页面索引, annotations 标准注解XML片段
// 返回: error 错误信息
func (e *Editor) appendAnnotations(index int, annotations []byte) error {
	base := e.source
	var err error
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return err
		}
	}
	reader := base.reader
	name := reader.ResPath(base.document.Annotations)
	var data []byte
	if base.document.Annotations == "" {
		name = path.Join(base.directory, "Annotations.xml")
		data, err = editorXMLContainer("Annotations", nil, nil)
	} else {
		if file, ok := reader.packageFile(name); ok {
			name = cleanPackagePath(file.Name)
		}
		data, err = reader.readFile(name)
	}
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	var ref *editorXML
	var existing string
	content, err := editorXMLContainer("PageAnnot", nil, nil)
	if err != nil {
		return err
	}
	for _, item := range root.children {
		if item.name.Local == "Page" && item.name.Space == root.name.Space && item.attr("PageID") == e.pages[index].ID {
			ref = item
			loc := item.child("FileLoc")
			if loc == nil {
				return fmt.Errorf("annotation page has no FileLoc")
			}
			file, err := editorPageLocation(reader, nil, name, strings.TrimSpace(editorImportText(data, loc)))
			if err != nil {
				return err
			}
			content, err = reader.readFile(file)
			if err != nil {
				return err
			}
			existing = file
			break
		}
	}
	page, err := parseEditorXML(content)
	if err != nil {
		return err
	}
	added := bytes.TrimPrefix(annotations, []byte(xml.Header))
	if page.open == page.end {
		content = editorPatchXML(content, []editorXMLPatch{editorXMLContent(content, page, added)})
	} else {
		content = editorPatchXML(content, []editorXMLPatch{{page.close, page.close, added}})
	}
	file := existing
	if file != "" {
		for _, other := range root.children {
			if other == ref || other.name.Local != "Page" || other.name.Space != root.name.Space || other.child("FileLoc") == nil {
				continue
			}
			shared, err := editorPageLocation(reader, nil, name, strings.TrimSpace(editorImportText(data, other.child("FileLoc"))))
			if err == nil && shared == existing {
				file = ""
				break
			}
		}
	}
	if file == "" {
		file = path.Join(base.directory, "Annotations", e.nextID()+".xml")
	}
	if ref != nil {
		loc := ref.child("FileLoc")
		var value bytes.Buffer
		_ = xml.EscapeText(&value, []byte("/"+file))
		data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, loc, value.Bytes())})
	} else {
		entry, err := editorXMLContainer("Page", ofdAttrs{{Name: xml.Name{Local: "PageID"}, Value: e.pages[index].ID}}, editorXMLText("FileLoc", "/"+file))
		if err != nil {
			return err
		}
		entry = bytes.TrimPrefix(entry, []byte(xml.Header))
		if root.open == root.end {
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, entry)})
		} else {
			data = editorPatchXML(data, []editorXMLPatch{{root.close, root.close, entry}})
		}
	}
	parts := map[string][]byte{name: data, file: content}
	if base.document.Annotations == "" {
		docName := cleanPackagePath(reader.OFD.DocBody[0].DocRoot)
		docData, err := reader.readFile(docName)
		if err != nil {
			return err
		}
		doc, err := parseEditorXML(docData)
		if err != nil {
			return err
		}
		position := doc.close
		for _, child := range doc.children {
			if slices.Contains([]string{"CustomTags", "Attachments", "Extensions"}, child.name.Local) {
				position = child.start
				break
			}
		}
		parts[docName] = editorPatchXML(docData, []editorXMLPatch{{position, position, editorXMLText("Annotations", "/"+name)}})
	}
	if err := e.commitAnnotationParts(base, parts); err != nil {
		return err
	}
	pages := maps.Clone(e.source.annotationPages)
	if pages == nil {
		pages = make(map[string]bool)
	}
	pages[e.pages[index].ID] = true
	e.source.annotationPages = pages
	return nil
}

// annotationFiles 获取仍被引用的注解文件，保留原文件中的未知页引用
// 返回: []string 文件路径
func (e *Editor) annotationFiles() []string {
	removed := make(map[string]bool)
	for id := range e.source.pages {
		removed[id] = true
	}
	for id := range e.source.annotationPages {
		removed[id] = true
	}
	for _, page := range e.pages {
		delete(removed, page.ID)
	}
	var files []string
	for name, pages := range e.source.reader.annotationFiles {
		for _, page := range pages {
			if !removed[page] {
				files = append(files, name)
				break
			}
		}
	}
	return files
}

// commitAnnotationParts 提交独立注解包快照，新建文档复用导入包基底
// 入参: base 原包, parts 修改的文件
// 返回: error 错误信息
func (e *Editor) commitAnnotationParts(base *editorSource, parts map[string][]byte) error {
	files := maps.Clone(base.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, parts)
	reader := &Reader{Zip: base.reader.Zip, files: files}
	if err := reader.initRoot(); err != nil {
		return err
	}
	doc, err := reader.Doc()
	if err != nil {
		return err
	}
	before, after := e.source, *base
	after.reader, after.document = reader, doc
	if before == nil {
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
	e.source = &after
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.source = before }
		change.redo = func(e *Editor) { e.source = &after }
	}
	return nil
}

// annotationScope 将注解外观作为内部编辑根容器，保留标准页块结构和坐标约定
// 入参: page 页面索引, path 注解内部路径
// 返回: *Reader 预览, *Renderer 度量器, *editorCompositeNode 根, []*editorCompositeNode 成员, error 错误信息
func (e *Editor) annotationScope(page int, path ObjectPath) (*Reader, *Renderer, *editorCompositeNode, []*editorCompositeNode, error) {
	data, err := e.annotationXML(page, path.Annotation)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	annotation, err := parseEditorXML(data)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	appearance := annotation.child("Appearance")
	if appearance == nil {
		return nil, nil, nil, nil, fmt.Errorf("annotation has no appearance")
	}
	data, err = editorXMLStandalone(data[appearance.start:appearance.end], appearance)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	root, err := newEditorCompositeNode(data)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	root.setStates(e.source.annotationStates[e.pages[page].ID+"/"+path.Annotation])
	reader, err := e.Reader()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if _, err = reader.PageContentByIndex(page); err != nil {
		reader.Close()
		return nil, nil, nil, nil, err
	}
	renderer := e.newRenderer(reader)
	root.parent, root.visible = IdentityMatrix, true
	scope := root
	for depth := 0; ; depth++ {
		members, loadErr := e.compositeMembers(scope, reader, renderer, make(map[string]bool))
		if loadErr != nil {
			err = loadErr
			break
		}
		if depth == len(path.Children) {
			for i, member := range members {
				member.index = i
			}
			return reader, renderer, root, members, nil
		}
		i := path.Children[depth]
		if i < 0 || i >= len(members) {
			err = fmt.Errorf("annotation member index %d out of range", i)
			break
		}
		scope = members[i]
	}
	reader.Close()
	return nil, nil, nil, nil, err
}

// writeAnnotationScope 只替换外观片段并保存会话排版状态，不改写注解元数据和动作
// 入参: page 页面索引, id 注解标识, data 外观XML, states 内部编辑状态
// 返回: error 错误信息
func (e *Editor) writeAnnotationScope(page int, id string, data []byte, states map[string]editorCompositeState) error {
	revision := e.Revision()
	err := e.editAnnotations(page, []string{id}, func(original []byte, node *editorXML) ([]byte, error) {
		fragment, err := editorXMLStandalone(original[node.start:node.end], node)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(fragment)
		if err != nil {
			return nil, err
		}
		appearance := root.child("Appearance")
		return editorAnnotationDate(editorPatchXML(fragment, []editorXMLPatch{{appearance.start, appearance.end, bytes.TrimPrefix(data, []byte(xml.Header))}}))
	})
	if err == nil && revision != e.Revision() {
		statesByID := maps.Clone(e.source.annotationStates)
		if statesByID == nil {
			statesByID = make(map[string]map[string]editorCompositeState)
		}
		statesByID[e.pages[page].ID+"/"+id] = maps.Clone(states)
		e.source.annotationStates = statesByID
	}
	return err
}
