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
// 入参: index 页面索引, annotation 注解内容，ID由编辑器生成，空LastModDate使用当前日期
// 返回: string 注解标识, error 错误信息
func (e *Editor) AddAnnotation(index int, annotation Annotation) (string, error) {
	var id string
	err := e.Transaction(func(next *Editor) error {
		var data []byte
		var err error
		id, data, err = next.prepareAnnotation(index, annotation)
		if err != nil {
			return err
		}
		return next.appendAnnotations(index, data, true)
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

// AnnotationTexts 按绘制顺序读取注解外观中的文字，展开复合资源并去重，不依赖字体或渲染后端
// 入参: index 页面索引, id 注解标识
// 返回: []string 文字列表, error 注解读取或资源引用错误
func (e *Editor) AnnotationTexts(index int, id string) ([]string, error) {
	defer e.bindPageIndex(index)()
	annotation, err := e.Annotation(index, id)
	if err != nil {
		return nil, err
	}
	reader, err := e.reader(nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := reader.PageContentByIndex(index)
	if err != nil {
		return nil, err
	}
	texts := make([]string, 0)
	seen := make(map[string]bool)
	visiting := make(map[*CompositeGraphicUnit]bool)
	collected := make(map[*CompositeGraphicUnit]bool)
	var collect func(CompositeGraphicUnit) error
	collect = func(unit CompositeGraphicUnit) error {
		if unit.ResourceID != "" {
			ref, _ := reader.resolveCompositeScoped(content.pageResources, unit.ResourceID)
			if ref == nil || visiting[ref] {
				return fmt.Errorf("invalid composite resource %q", unit.ResourceID)
			}
			if !collected[ref] {
				visiting[ref] = true
				if err := collect(*ref); err != nil {
					return err
				}
				delete(visiting, ref)
				collected[ref] = true
			}
		}
		for object := range unit.objects() {
			switch object.Type {
			case "TextObject":
				value := object.TextObject.Text()
				if !seen[value] {
					seen[value] = true
					texts = append(texts, value)
				}
			case "CompositeObject", "CompositeGraphicUnit":
				if err := collect(object.CompositeGraphicUnit); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := collect(CompositeGraphicUnit{Objects: annotation.Appearance.Objects}); err != nil {
		return nil, err
	}
	return texts, nil
}

// editorFileBackup 保存单次增量提交覆盖前的文件快照，用于失败回滚与撤销重做
type editorFileBackup struct {
	data    []byte
	existed bool
	inIndex bool
}

// prepareAnnotation 校验注解并分配外观标识，提交由调用方事务或独立导入文档负责
// 入参: index 页面索引, annotation 注解内容
// 返回: string 注解标识, []byte 标准XML, error 校验错误
func (e *Editor) prepareAnnotation(index int, annotation Annotation) (string, []byte, error) {
	if index < 0 || index >= len(e.pages) {
		return "", nil, fmt.Errorf("page index %d out of range", index)
	}
	defer e.bindPageIndex(index)()
	if !slices.Contains([]string{"Link", "Path", "Highlight", "Stamp", "Watermark"}, annotation.Type) {
		return "", nil, fmt.Errorf("invalid annotation type %q", annotation.Type)
	}
	if _, err := creationBox(annotation.Appearance.Boundary); err != nil {
		return "", nil, err
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", nil, err
	}
	if e.maxID >= maxUnitID {
		return "", nil, fmt.Errorf("MaxUnitID exceeds uint32 range")
	}
	id := e.nextID()
	objects := annotation.Appearance.Objects
	if len(objects) == 0 {
		objects = nil
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
			return "", nil, fmt.Errorf("use composite copy to insert existing complex content")
		}
	}
	if uint64(len(objects)) > maxUnitID-e.maxID {
		return "", nil, fmt.Errorf("MaxUnitID exceeds uint32 range")
	}
	var content []byte
	for _, object := range objects {
		prepared, err := e.prepareObject(e.nextID(), cloneEditorData(object))
		if err != nil {
			return "", nil, err
		}
		data, err := editorObjectXML(prepared)
		if err != nil {
			return "", nil, err
		}
		content = append(content, bytes.TrimPrefix(data, []byte(xml.Header))...)
	}
	appearance, err := editorXMLContainer("Appearance", ofdAttrs{{Name: xml.Name{Local: "Boundary"}, Value: annotation.Appearance.Boundary}}, content)
	if err != nil {
		return "", nil, err
	}
	date := annotation.LastModDate
	if date == "" {
		date = time.Now().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", nil, fmt.Errorf("invalid annotation date %q: %w", date, err)
	}
	attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}, {Name: xml.Name{Local: "Type"}, Value: annotation.Type},
		{Name: xml.Name{Local: "Creator"}, Value: annotation.Creator}, {Name: xml.Name{Local: "LastModDate"}, Value: date}}
	attrs.add("Subtype", annotation.Subtype)
	attrs.flag("Visible", annotation.Visible)
	attrs.flag("Print", annotation.Print)
	if annotation.NoZoom {
		attrs.add("NoZoom", "true")
	}
	if annotation.NoRotate {
		attrs.add("NoRotate", "true")
	}
	attrs.flag("ReadOnly", annotation.ReadOnly)
	content = nil
	if annotation.Remark != "" {
		content = editorXMLText("Remark", annotation.Remark)
	}
	if annotation.Parameters != nil && len(annotation.Parameters.Parameter) != 0 {
		var parameters []byte
		for _, parameter := range annotation.Parameters.Parameter {
			if parameter.Name == "" {
				return "", nil, fmt.Errorf("empty annotation parameter name")
			}
			var value bytes.Buffer
			if err := xml.EscapeText(&value, []byte(parameter.Value)); err != nil {
				return "", nil, err
			}
			data, err := editorXMLContainer("Parameter", ofdAttrs{{Name: xml.Name{Local: "Name"}, Value: parameter.Name}}, value.Bytes())
			if err != nil {
				return "", nil, err
			}
			parameters = append(parameters, bytes.TrimPrefix(data, []byte(xml.Header))...)
		}
		data, err := editorXMLContainer("Parameters", nil, parameters)
		if err != nil {
			return "", nil, err
		}
		content = append(content, bytes.TrimPrefix(data, []byte(xml.Header))...)
	}
	content = append(content, bytes.TrimPrefix(appearance, []byte(xml.Header))...)
	data, err := editorXMLContainer("Annot", attrs, content)
	return id, data, err
}

// annotationXML 按ST_ID数值读取唯一注解的原文，不以类型、大小或可见性筛选
// 入参: index 页面索引, id 注解标识
// 返回: []byte 注解XML, error 错误信息
func (e *Editor) annotationXML(index int, id string) ([]byte, error) {
	if index < 0 || index >= len(e.pages) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	key := editorResourceID(id)
	if key == "" {
		return nil, fmt.Errorf("invalid annotation ID %q", id)
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
	pageID := editorResourceID(e.pages[index].ID)
	for _, ref := range root.children {
		if !ref.matchesOFD("Page") || pageID == "" || editorResourceID(ref.attr("PageID")) != pageID || ref.child("FileLoc") == nil {
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
			if !node.matchesOFD("Annot") || editorResourceID(node.attr("ID")) != key {
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
// 入参: index 页面索引, annotations 注解XML片段, generated 是否为自产内容
// 返回: error 错误信息
func (e *Editor) appendAnnotations(index int, annotations []byte, generated bool) error {
	return e.appendPageAnnotations(context.Background(), []int{index}, [][]byte{annotations}, generated)
}

// appendPageAnnotations 向多个页面批量追加注解原文，单次解析与提交包快照
// 入参: ctx 取消上下文, indexes 页面索引列表, annotations 按页对应的注解XML片段, generated 是否为自产内容
// 返回: error 错误信息
func (e *Editor) appendPageAnnotations(ctx context.Context, indexes []int, annotations [][]byte, generated bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(indexes) == 0 {
		return nil
	}
	base := e.source
	var err error
	if base == nil {
		base, err = e.importBaseContext(ctx)
		if err != nil {
			return err
		}
	}
	reader := base.reader
	if generated && len(indexes) == 1 && base.document.Annotations != "" {
		pageID := e.pages[indexes[0]].ID
		if files := reader.pageAnnotFiles[pageID]; len(files) > 0 {
			existing := files[0]
			if len(reader.annotationFiles[existing]) == 1 {
				if content, readErr := reader.readFileViewContext(ctx, existing); readErr == nil {
					added := bytes.TrimPrefix(annotations[0], []byte(xml.Header))
					if patched, ok := appendOFDXMLBeforeClosing(content, "PageAnnot", added); ok {
						if err := e.commitAnnotationPartsContext(ctx, base, map[string][]byte{existing: patched}); err != nil {
							return err
						}
						if !e.source.annotationPages[pageID] {
							pages := maps.Clone(e.source.annotationPages)
							if pages == nil {
								pages = make(map[string]bool)
							}
							pages[pageID] = true
							e.source.annotationPages = pages
						}
						return nil
					}
				}
			}
		}
	}
	name := reader.ResPath(base.document.Annotations)
	var data []byte
	if base.document.Annotations == "" {
		name = e.packageName("Annotations.xml")
		data, err = editorXMLContainer("Annotations", nil, nil)
	} else {
		if file, ok := reader.packageFile(name); ok {
			name = cleanPackagePath(file.Name)
		}
		data, err = reader.readFileViewContext(ctx, name)
	}
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	refs := make(map[string]*editorXML)
	existingFiles := make(map[string]string)
	fileCounts := make(map[string]int)
	for _, item := range root.children {
		if !item.matchesOFD("Page") {
			continue
		}
		pid := editorResourceID(item.attr("PageID"))
		loc := item.child("FileLoc")
		if loc != nil {
			shared, locErr := editorPageLocation(reader, nil, name, strings.TrimSpace(editorImportText(data, loc)))
			if locErr == nil {
				fileCounts[shared]++
				if pid != "" && refs[pid] == nil {
					refs[pid] = item
					existingFiles[pid] = shared
				}
			}
		} else if pid != "" && refs[pid] == nil {
			refs[pid] = item
		}
	}
	parts := make(map[string][]byte, len(indexes)+2)
	reserved := map[string][]byte{name: nil}
	var dataPatches []editorXMLPatch
	var newEntries []byte
	for i, index := range indexes {
		if err := ctx.Err(); err != nil {
			return err
		}
		pageID := editorResourceID(e.pages[index].ID)
		ref := refs[pageID]
		existing := existingFiles[pageID]
		var content []byte
		if ref != nil {
			if ref.child("FileLoc") == nil {
				return fmt.Errorf("annotation page has no FileLoc")
			}
			if existing == "" {
				loc := ref.child("FileLoc")
				existing, err = editorPageLocation(reader, nil, name, strings.TrimSpace(editorImportText(data, loc)))
				if err != nil {
					return err
				}
			}
			content, err = reader.readFileViewContext(ctx, existing)
			if err != nil {
				return err
			}
		} else {
			content, err = editorXMLContainer("PageAnnot", nil, nil)
			if err != nil {
				return err
			}
		}
		added := bytes.TrimPrefix(annotations[i], []byte(xml.Header))
		if generated {
			if patched, ok := appendOFDXMLBeforeClosing(content, "PageAnnot", added); ok {
				content = patched
			} else {
				page, err := parseEditorXMLContext(ctx, content)
				if err != nil {
					return err
				}
				added, err = editorXMLGenerated(added, page.name.Space)
				if err != nil {
					return err
				}
				if page.open == page.end {
					content = editorPatchXML(content, []editorXMLPatch{editorXMLContent(content, page, added)})
				} else {
					content = editorPatchXML(content, []editorXMLPatch{{page.close, page.close, added}})
				}
			}
		} else {
			page, err := parseEditorXMLContext(ctx, content)
			if err != nil {
				return err
			}
			if page.open == page.end {
				content = editorPatchXML(content, []editorXMLPatch{editorXMLContent(content, page, added)})
			} else {
				content = editorPatchXML(content, []editorXMLPatch{{page.close, page.close, added}})
			}
		}
		file := existing
		if file != "" && fileCounts[file] > 1 {
			file = ""
		}
		if file == "" {
			file = packageAvailableName(reader, reserved, e.packageName(path.Join("Annotations", "Page_"+e.pages[index].ID+".xml")))
		}
		reserved[file] = nil
		parts[file] = content
		if ref != nil {
			if file != existing {
				loc := ref.child("FileLoc")
				var value bytes.Buffer
				_ = xml.EscapeText(&value, []byte("/"+file))
				dataPatches = append(dataPatches, editorXMLContent(data, loc, value.Bytes()))
			}
		} else {
			entry, err := root.containerXML("Page", ofdAttrs{{Name: xml.Name{Local: "PageID"}, Value: e.pages[index].ID}}, root.textXML("FileLoc", "/"+file))
			if err != nil {
				return err
			}
			newEntries = append(newEntries, bytes.TrimPrefix(entry, []byte(xml.Header))...)
		}
	}
	if len(newEntries) > 0 {
		if root.open == root.end {
			dataPatches = append(dataPatches, editorXMLContent(data, root, newEntries))
		} else {
			dataPatches = append(dataPatches, editorXMLPatch{root.close, root.close, newEntries})
		}
	}
	if len(dataPatches) > 0 {
		data = editorPatchXML(data, dataPatches)
	}
	parts[name] = data
	if base.document.Annotations == "" {
		docName := reader.documentRoot
		docData, err := reader.readFileViewContext(ctx, docName)
		if err != nil {
			return err
		}
		doc, err := parseEditorXMLContext(ctx, docData)
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
		patch := editorXMLPatch{position, position, doc.textXML("Annotations", "/"+name)}
		if node := doc.child("Annotations"); node != nil {
			var value bytes.Buffer
			_ = xml.EscapeText(&value, []byte("/"+name))
			patch = editorXMLContent(docData, node, value.Bytes())
		}
		parts[docName] = editorPatchXML(docData, []editorXMLPatch{patch})
	}
	if err := e.commitAnnotationPartsContext(ctx, base, parts); err != nil {
		return err
	}
	pages := maps.Clone(e.source.annotationPages)
	if pages == nil {
		pages = make(map[string]bool)
	}
	for _, index := range indexes {
		pages[e.pages[index].ID] = true
	}
	e.source.annotationPages = pages
	return nil
}

// annotationFiles 获取仍被引用的注解文件，保留原文件中的未知页引用
// 返回: []string 文件路径
func (e *Editor) annotationFiles() []string {
	if len(e.source.reader.annotationFiles) == 0 {
		return nil
	}
	removed := make(map[string]bool)
	for id := range e.source.pages {
		removed[editorResourceID(id)] = true
	}
	for id := range e.source.annotationPages {
		removed[editorResourceID(id)] = true
	}
	for _, page := range e.pages {
		delete(removed, editorResourceID(page.ID))
	}
	delete(removed, "")
	var files []string
	for name, pages := range e.source.reader.annotationFiles {
		for _, page := range pages {
			if !removed[editorResourceID(page)] {
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
	return e.commitAnnotationPartsContext(context.Background(), base, parts)
}

// commitAnnotationPartsContext 准备并增量提交包快照，避免每次写入全量复制与重建索引
// 入参: ctx 取消上下文, base 原包, parts 修改的文件
// 返回: error 结构、读取或取消错误
func (e *Editor) commitAnnotationPartsContext(ctx context.Context, base *editorSource, parts map[string][]byte) error {
	parts, err := base.reader.versionChanges(ctx, parts)
	if err != nil {
		return err
	}
	if !canCommitIncrementalParts(base, parts) {
		return e.commitAnnotationPartsFullContext(ctx, base, parts)
	}
	updates := make(map[string][]byte, len(parts))
	docPath := cleanPackagePath(base.reader.documentRoot)
	annPath := ""
	if base.document.Annotations != "" {
		annPath = cleanPackagePath(base.reader.ResPath(base.document.Annotations))
	}
	attPath := ""
	if strings.TrimSpace(base.document.Attachments.Path) != "" {
		attPath = cleanPackagePath(base.reader.ResPath(base.document.Attachments.Path))
	}
	tagPath := ""
	if strings.TrimSpace(base.document.CustomTags.Path) != "" {
		tagPath = cleanPackagePath(base.reader.ResPath(base.document.CustomTags.Path))
	}
	docChanged, annChanged, attChanged, tagChanged := false, false, false, false
	for name, data := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file, ok := base.reader.packageFile(name); ok {
			name = cleanPackagePath(file.Name)
		} else {
			name = cleanPackagePath(name)
		}
		if !validPackagePath(name) {
			return fmt.Errorf("invalid package path: %q", name)
		}
		updates[name] = data
		if name == docPath {
			docChanged = true
		}
		if annPath != "" && name == annPath {
			annChanged = true
		}
		if attPath != "" && name == attPath {
			attChanged = true
		}
		if tagPath != "" && name == tagPath {
			tagChanged = true
		}
	}
	inPlace := base.reader == e.ownedReader && e.ownedOwner == e && e.ownedTx == e.transactionChanges && (e.historyLimit == 0 || e.ownedRevision == e.revision)
	var reader *Reader
	var backups map[string]editorFileBackup
	prevDoc := base.reader.doc
	prevAnnFiles := base.reader.annotationFiles
	prevPageAnnFiles := base.reader.pageAnnotFiles
	if inPlace {
		reader = base.reader
		backups = make(map[string]editorFileBackup, len(updates))
		if reader.files == nil {
			reader.files = make(map[string][]byte, len(updates))
		}
		for name, data := range updates {
			oldData, existed := reader.files[name]
			_, inIndex := reader.fileIndex[name]
			backups[name] = editorFileBackup{data: oldData, existed: existed, inIndex: inIndex || reader.encryptedSource().has(name)}
			reader.files[name] = data
			reader.fileNames[name] = true
		}
	} else {
		files := maps.Clone(base.reader.files)
		if files == nil {
			files = make(map[string][]byte, len(updates))
		}
		fileNames := maps.Clone(base.reader.fileNames)
		for name, data := range updates {
			files[name] = data
			fileNames[name] = true
		}
		copied := *base.reader
		copied.files = files
		copied.fileNames = fileNames
		copied.resources = e.runtimeResources
		reader = &copied
	}
	restoreInPlace := func() {
		if !inPlace {
			return
		}
		for name, backup := range backups {
			if backup.existed {
				reader.files[name] = backup.data
			} else {
				delete(reader.files, name)
				if !backup.inIndex {
					delete(reader.fileNames, name)
				}
			}
		}
	}
	doc := base.document
	if docChanged {
		probe := *reader
		probe.doc = nil
		var docErr error
		doc, docErr = probe.docStructureContext(ctx)
		if docErr != nil {
			restoreInPlace()
			return docErr
		}
		if doc.Annotations != base.document.Annotations {
			annChanged = true
		}
	}
	newAnnFiles, newPageAnnFiles := reader.annotationFiles, reader.pageAnnotFiles
	if annChanged {
		probe := *reader
		probe.doc = doc
		probe.annotationFiles = nil
		probe.pageAnnotFiles = nil
		_, _ = probe.annotationIndexContext(ctx, doc)
		if err := ctx.Err(); err != nil {
			restoreInPlace()
			return err
		}
		newAnnFiles, newPageAnnFiles = probe.annotationFiles, probe.pageAnnotFiles
	}
	if err := ctx.Err(); err != nil {
		restoreInPlace()
		return err
	}
	before, after := e.source, *base
	resources := e.resources
	if before == nil {
		resources = slices.Clone(resources)
		for i := range resources {
			if err := ctx.Err(); err != nil {
				restoreInPlace()
				return err
			}
			resource := &resources[i]
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
	if err := ctx.Err(); err != nil {
		restoreInPlace()
		return err
	}
	reader.doc = doc
	if annChanged {
		reader.annotationFiles = newAnnFiles
		reader.pageAnnotFiles = newPageAnnFiles
		reader.Annots = nil
		reader.pageAnnotsLoaded = nil
	} else if len(reader.Annots) != 0 || len(reader.pageAnnotsLoaded) != 0 {
		var dirtyPages []string
		for name := range updates {
			if pages := reader.annotationFiles[name]; len(pages) > 0 {
				dirtyPages = append(dirtyPages, pages...)
			}
		}
		if len(dirtyPages) > 0 {
			if !inPlace {
				reader.Annots = maps.Clone(reader.Annots)
				reader.pageAnnotsLoaded = maps.Clone(reader.pageAnnotsLoaded)
			}
			for _, pageID := range dirtyPages {
				delete(reader.Annots, pageID)
				delete(reader.pageAnnotsLoaded, pageID)
			}
		}
	}
	if docChanged || attChanged {
		after.attachmentIDs = nil
	}
	if docChanged || tagChanged {
		after.customTagCountReady = false
		after.richTextTagKnown = false
	}
	after.reader, after.document = reader, doc
	e.resources = resources
	e.source = &after
	e.ownedReader = reader
	e.ownedOwner = e
	e.ownedTx = e.transactionChanges
	e.syncPackageNameCache(before, &after)
	if change := e.recordChange(); change != nil {
		if inPlace {
			change.undo = func(e *Editor) {
				for name, backup := range backups {
					if backup.existed {
						reader.files[name] = backup.data
					} else {
						delete(reader.files, name)
						if !backup.inIndex {
							delete(reader.fileNames, name)
						}
					}
				}
				reader.doc = prevDoc
				reader.annotationFiles = prevAnnFiles
				reader.pageAnnotFiles = prevPageAnnFiles
				reader.Annots = nil
				reader.pageAnnotsLoaded = nil
				e.source = before
			}
			change.redo = func(e *Editor) {
				for name, data := range updates {
					reader.files[name] = data
					reader.fileNames[name] = true
				}
				reader.doc = doc
				reader.annotationFiles = newAnnFiles
				reader.pageAnnotFiles = newPageAnnFiles
				reader.Annots = nil
				reader.pageAnnotsLoaded = nil
				e.source = &after
			}
		} else {
			change.undo = func(e *Editor) { e.source = before }
			change.redo = func(e *Editor) { e.source = &after }
		}
	}
	e.ownedRevision = e.revision
	return nil
}

// canCommitIncrementalParts 判断本次改动能否在不重建整包根索引的前提下增量提交
// 入参: base 原包, parts 待写入条目
// 返回: bool 是否允许走增量提交快路径
func canCommitIncrementalParts(base *editorSource, parts map[string][]byte) bool {
	if base == nil || base.reader == nil || base.reader.OFD == nil || base.document == nil || base.reader.fileNames == nil || base.reader.versionInfo != nil || len(parts) == 0 {
		return false
	}
	reader := base.reader
	for name := range parts {
		clean := cleanPackagePath(name)
		if file, ok := reader.packageFile(name); ok {
			clean = cleanPackagePath(file.Name)
		}
		if clean == "OFD.xml" || !validPackagePath(clean) {
			return false
		}
		if reader.resFileCache[clean] != nil || reader.resourcesRead[clean] {
			return false
		}
		if _, isPage := reader.pageHeaderCache[clean]; isPage {
			return false
		}
		for _, res := range base.document.CommonData.PublicRes {
			if cleanPackagePath(reader.ResPath(res)) == clean {
				return false
			}
		}
		for _, res := range base.document.CommonData.DocumentRes {
			if cleanPackagePath(reader.ResPath(res)) == clean {
				return false
			}
		}
	}
	return true
}

// commitAnnotationPartsFullContext 完整重建包快照与索引，用于根入口或资源索引变更时的回退路径
// 入参: ctx 取消上下文, base 原包, parts 修改的文件
// 返回: error 结构、读取或取消错误
func (e *Editor) commitAnnotationPartsFullContext(ctx context.Context, base *editorSource, parts map[string][]byte) error {
	files := maps.Clone(base.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	for name, data := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file, ok := base.reader.packageFile(name); ok {
			name = cleanPackagePath(file.Name)
		}
		files[name] = data
	}
	reader := &Reader{Zip: base.reader.Zip, encrypted: base.reader.encryptedSource(), sourceFile: base.reader.sourceFile, files: files, encryption: base.reader.encryption, documentIndex: base.reader.documentIndex, selectedVersion: base.reader.selectedVersion, pageContents: base.reader.pageContents, resources: e.runtimeResources}
	if err := reader.initRootContext(ctx); err != nil {
		return err
	}
	doc, err := reader.docStructureContext(ctx)
	if err != nil {
		return err
	}
	_, _ = reader.annotationIndexContext(ctx, doc)
	if err := ctx.Err(); err != nil {
		return err
	}
	before, after := e.source, *base
	after.reader, after.document = reader, doc
	after.attachmentIDs = nil
	after.customTagCountReady = false
	after.richTextTagKnown = false
	resources := e.resources
	if before == nil {
		resources = slices.Clone(resources)
		for i := range resources {
			if err := ctx.Err(); err != nil {
				return err
			}
			resource := &resources[i]
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
	if err := ctx.Err(); err != nil {
		return err
	}
	e.resources = resources
	e.source = &after
	e.ownedReader = reader
	e.ownedOwner = e
	e.ownedTx = e.transactionChanges
	e.syncPackageNameCache(before, &after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.source = before }
		change.redo = func(e *Editor) { e.source = &after }
	}
	e.ownedRevision = e.revision
	return nil
}

// annotationScope 将注解外观作为内部编辑根容器，保留标准页块结构和坐标约定
// 入参: page 页面索引, path 注解内部路径
// 返回: *Reader 预览, *Renderer 度量器, *editorCompositeNode 根, []*editorCompositeNode 成员, error 错误信息
func (e *Editor) annotationScope(page int, path ObjectPath) (*Reader, *Renderer, *editorCompositeNode, []*editorCompositeNode, error) {
	defer e.bindPageIndex(page)()
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
	root.pageID = e.pages[page].ID
	root.setStates(e.source.annotationStates[e.pages[page].ID+"/"+editorResourceID(path.Annotation)])
	reader, err := e.reader(nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	content, err := reader.PageContentByIndex(page)
	if err != nil {
		reader.Close()
		return nil, nil, nil, nil, err
	}
	renderer := e.newRenderer(reader)
	renderer.bindPageResources(content)
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
		statesByID[e.pages[page].ID+"/"+editorResourceID(id)] = maps.Clone(states)
		e.source.annotationStates = statesByID
	}
	return err
}
