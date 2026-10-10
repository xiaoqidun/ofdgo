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
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// sourceParts 生成编辑快照的XML和新增资源，删页时同步清理标准引用
// 入参: progress 保存进度回调，预览时为nil
// 返回: map[string][]byte 替换及新增条目, error 错误信息
func (e *Editor) sourceParts(progress editorProgress) (map[string][]byte, error) {
	parts, err := e.sourcePartsReferences(progress, nil)
	if err != nil {
		return nil, err
	}
	return e.source.reader.versionChanges(context.Background(), parts)
}

// sourcePartsReferences 生成保存快照，同步收集完全自产页面的实际编码引用
// 入参: progress 保存进度回调, generated 本次自产页面引用，可为nil
// 返回: map[string][]byte 输出条目, error 编码错误
func (e *Editor) sourcePartsReferences(progress editorProgress, generated map[string]editorGeneratedReferences) (map[string][]byte, error) {
	return e.sourcePartsPrepared(progress, generated, false)
}

// sourcePartsPrepared 准备保存条目，按需压缩暂存自产页面
// 入参: progress 保存进度回调, generated 本次页面及引用, stage 是否压缩暂存
// 返回: map[string][]byte 输出条目, error 编码错误
func (e *Editor) sourcePartsPrepared(progress editorProgress, generated map[string]editorGeneratedReferences, stage bool) (map[string][]byte, error) {
	parts := make(map[string][]byte)
	var stager editorPageStager
	source := e.source
	reader := source.reader
	pageRefs := make([]Page, len(e.pages))
	originalCount := 0
	for i, page := range e.pages {
		if err := progress.report("pages", i, len(e.pages)); err != nil {
			return nil, err
		}
		original := source.pages[page.ID]
		if original != nil {
			originalCount++
			pageRefs[i] = original.ref
			if original.original == nil {
				continue
			}
			data, err := e.sourcePageXML(i, original)
			if err != nil {
				return nil, err
			}
			if original.repaired || !bytes.Equal(data, original.data) {
				parts[reader.ResPath(original.ref.BaseLoc)] = data
			}
		} else {
			name := e.packageName(packagePagePath("", page.ID))
			var scan *editorReferenceScan
			if generated != nil {
				scan = &editorReferenceScan{refs: &editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}, name: name, safe: true}
			}
			if stage && generated != nil && !e.sourcePagePreserved(page) {
				data, err := stager.encode(e, page, scan)
				if err != nil {
					return nil, err
				}
				parts[name] = nil
				known := editorGeneratedReferences{staged: data}
				if scan != nil && scan.safe && scan.seen && len(scan.stack) == 0 {
					known.refs = scan.refs
				}
				generated[name] = known
				if reader.OFD.DocBody[reader.documentIndex].versioned && !known.stagedLeaf(nil) {
					input := data.open()
					if e.output != nil {
						input = imageInput{ReadCloser: input, context: e.output.ctx}
					}
					parts[name], err = io.ReadAll(input)
					input.Close()
					if err != nil {
						return nil, err
					}
					generated[name] = editorGeneratedReferences{data: parts[name], refs: known.refs}
				}
			} else {
				data, err := e.sourcePageData(page, scan)
				if err != nil {
					return nil, err
				}
				parts[name] = data
				if scan != nil && scan.safe && scan.seen && len(scan.stack) == 0 {
					generated[name] = editorGeneratedReferences{data: data, refs: scan.refs}
				}
			}
			pageRefs[i] = Page{ID: page.ID, BaseLoc: "/" + name}
		}
	}
	if err := progress.report("pages", len(e.pages), len(e.pages)); err != nil {
		return nil, err
	}
	fonts, images, spaces, resourceFiles := e.usedResources()
	for _, resource := range e.resources {
		if resource.definition() != "" && slices.Contains(resourceFiles, resource.name) || resource.colorProfileUsed(spaces) {
			parts[resource.name] = resource.data
		}
	}
	commonData := source.document.CommonData
	for _, name := range append(slices.Clone(commonData.DocumentRes), commonData.PublicRes...) {
		resourceFiles = slices.DeleteFunc(resourceFiles, func(file string) bool {
			return file == reader.ResPath(name)
		})
	}
	if len(fonts)+len(images)+len(spaces) != 0 {
		data, err := e.encodeXML(func(x *ofdXML) { x.resources(fonts, images, spaces) })
		if err != nil {
			return nil, err
		}
		resourcePath, added, err := mergePackageResources(reader, parts, source.document, source.directory, data)
		if err != nil {
			return nil, err
		}
		if added {
			resourceFiles = append(resourceFiles, resourcePath)
		}
		for _, resource := range append(fonts, images...) {
			if resource.name != "" {
				parts[resource.name] = resource.data
			}
		}
	}
	pagesChanged := !slices.Equal(pageRefs, source.document.Pages.Page)
	if pagesChanged || len(resourceFiles) != 0 || len(parts) != 0 && e.maxID != source.document.CommonData.MaxUnitID {
		name := reader.documentRoot
		data, err := reader.readFile(name)
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
		var patches []editorXMLPatch
		if e.maxID != source.document.CommonData.MaxUnitID {
			value := common.textXML("MaxUnitID", strconv.Itoa(e.maxID))
			if maxID := common.child("MaxUnitID"); maxID != nil {
				value = maxID.textXML("MaxUnitID", strconv.Itoa(e.maxID))
				patches = append(patches, editorXMLPatch{maxID.start, maxID.end, value})
			} else {
				patches = append(patches, editorXMLPatch{common.open, common.open, value})
			}
		}
		if len(resourceFiles) != 0 {
			position := common.close
			for _, child := range common.children {
				if child.name.Local == "TemplatePage" || child.name.Local == "DefaultCS" {
					position = child.start
					break
				}
			}
			var references []byte
			for _, name := range resourceFiles {
				references = append(references, common.textXML("DocumentRes", "/"+name)...)
			}
			patches = append(patches, editorXMLPatch{position, position, references})
		}
		if pagesChanged {
			pages := root.child("Pages")
			if pages == nil {
				return nil, fmt.Errorf("document has no Pages")
			}
			originals := make(map[string]*editorXML, len(pages.children))
			for _, child := range pages.children {
				if child.matchesOFD("Page") {
					id := child.attr("ID")
					if originals[id] == nil {
						originals[id] = child
					}
				}
			}
			entries := make([][]byte, 0, len(pageRefs))
			for _, page := range pageRefs {
				original := originals[page.ID]
				if original != nil {
					entries = append(entries, data[original.start:original.end])
				} else {
					item, err := pages.containerXML("Page", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: page.ID}, {Name: xml.Name{Local: "BaseLoc"}, Value: page.BaseLoc}}, nil)
					if err != nil {
						return nil, err
					}
					entries = append(entries, bytes.TrimPrefix(item, []byte(xml.Header)))
				}
			}
			position := 0
			for _, child := range pages.children {
				if !child.matchesOFD("Page") {
					continue
				}
				var entry []byte
				if position < len(entries) {
					entry = entries[position]
					position++
				}
				patches = append(patches, editorXMLPatch{child.start, child.end, entry})
			}
			if position < len(entries) {
				added := bytes.Join(entries[position:], nil)
				if pages.open == pages.end {
					patches = append(patches, editorXMLContent(data, pages, added))
				} else {
					patches = append(patches, editorXMLPatch{pages.close, pages.close, added})
				}
			}
		}
		updated := editorPatchXML(data, patches)
		if !bytes.Equal(data, updated) {
			parts[name] = updated
		}
	}
	if !reflect.DeepEqual(e.Info, source.info) {
		data, err := e.sourceInfoXML()
		if err != nil {
			return nil, err
		}
		parts["OFD.xml"] = data
	}
	if e.outlines != nil {
		name := reader.documentRoot
		data, ok := parts[name]
		if !ok {
			var err error
			data, err = reader.readFile(name)
			if err != nil {
				return nil, err
			}
		}
		updated, err := e.withOutlines(data)
		if err != nil {
			return nil, err
		}
		parts[name] = updated
	}
	if originalCount != len(source.pages) || len(source.annotationPages) != 0 || len(e.removedPages) != 0 {
		if err := e.prunePageReferences(parts, generated); err != nil {
			return nil, err
		}
	}
	for name, data := range maps.Clone(parts) {
		if file, ok := reader.packageFile(name); ok {
			actual := cleanPackagePath(file.Name)
			if actual != name {
				delete(parts, name)
				parts[actual] = data
				if known, ok := generated[name]; ok {
					delete(generated, name)
					generated[actual] = known
				}
			}
		}
	}
	return parts, nil
}

// sourcePageXML 仅改写已修改的对象、页面尺寸、动作和新增图层，保留原页块层级
// 入参: index 页面索引, source 原页面
// 返回: []byte 页面XML, error 错误信息
func (e *Editor) sourcePageXML(index int, source *editorSourcePage) ([]byte, error) {
	page := e.pages[index]
	unchanged := reflect.DeepEqual(page, *source.original)
	if unchanged && len(e.origins) != 0 {
		for _, layer := range page.Content.Layer {
			for _, object := range layer.Objects {
				id := editorObjectID(object)
				if origin := e.origins[id]; origin != nil && origin.node != source.nodes[id] {
					unchanged = false
					break
				}
			}
			if !unchanged {
				break
			}
		}
	}
	if unchanged {
		return source.data, nil
	}
	if source.root.open == source.root.end {
		copy := *source
		copy.data = editorPatchXML(source.data, []editorXMLPatch{editorXMLContent(source.data, source.root, nil)})
		var err error
		copy.root, err = parseEditorXML(copy.data)
		if err != nil {
			return nil, err
		}
		source = &copy
	}
	data := source.data
	var patches []editorXMLPatch
	if page.Area.PhysicalBox != source.original.Area.PhysicalBox {
		area := source.root.child("Area")
		if area != nil {
			areaData, err := editorXMLStandalone(data[area.start:area.end], area)
			if err != nil {
				return nil, err
			}
			areaRoot, err := parseEditorXML(areaData)
			if err != nil {
				return nil, err
			}
			updated, err := editorXMLSetText(areaData, areaRoot, [][2]string{{"PhysicalBox", page.Area.PhysicalBox}})
			if err != nil {
				return nil, err
			}
			patches = append(patches, editorXMLPatch{area.start, area.end, updated})
		} else {
			value, err := source.root.containerXML("Area", nil, source.root.textXML("PhysicalBox", page.Area.PhysicalBox))
			if err != nil {
				return nil, err
			}
			patches = append(patches, editorXMLPatch{source.root.open, source.root.open, value})
		}
	}
	for layerIndex, original := range source.original.Content.Layer {
		current := page.Content.Layer[layerIndex]
		before := make(map[string]GraphicObject)
		for _, object := range original.Objects {
			before[editorObjectID(object)] = object
		}
		ordered := make(map[*editorXML][]GraphicObject)
		after := make(map[string]GraphicObject)
		for _, object := range current.Objects {
			id := editorObjectID(object)
			after[id] = object
			node := source.nodes[id]
			if node == nil {
				if origin := e.objectOrigin(id); origin != nil {
					node = origin.node
				}
			}
			if node != nil {
				ordered[node.parent] = append(ordered[node.parent], object)
			}
		}
		positions := make(map[*editorXML]int)
		orderable := make(map[*editorXML]bool)
		for parent := range ordered {
			orderable[parent] = editorContainerOrderable(parent)
		}
		for _, object := range original.Objects {
			id := editorObjectID(object)
			node := source.nodes[id]
			if node == nil {
				continue
			}
			next, exists := after[id]
			if orderable[node.parent] {
				position := positions[node.parent]
				members := ordered[node.parent]
				exists = position < len(members)
				if exists {
					next = members[position]
				}
				positions[node.parent]++
			}
			var encoded []byte
			if exists {
				nextID := editorObjectID(next)
				origin := source.nodes[nextID]
				current := e.objectOrigin(nextID)
				if origin != nil && current.node == origin && reflect.DeepEqual(before[nextID], next) {
					encoded = data[origin.start:origin.end]
				} else {
					var err error
					encoded, err = editorXMLObject(current.data, current.node, current.object, next)
					if err != nil {
						return nil, err
					}
				}
			}
			if !bytes.Equal(encoded, data[node.start:node.end]) {
				patches = append(patches, editorXMLPatch{node.start, node.end, encoded})
			}
		}
		for parent, members := range ordered {
			if !orderable[parent] || positions[parent] >= len(members) {
				continue
			}
			var added []byte
			for _, object := range members[positions[parent]:] {
				origin := e.objectOrigin(editorObjectID(object))
				encoded, err := editorXMLObject(origin.data, origin.node, origin.object, object)
				if err != nil {
					return nil, err
				}
				added = append(added, encoded...)
			}
			patches = append(patches, editorXMLPatch{parent.close, parent.close, added})
		}
	}
	var added []byte
	context := source.root.child("Content")
	if context == nil {
		context = source.root
	}
	for _, layer := range page.Content.Layer[len(source.original.Content.Layer):] {
		if len(layer.Objects) == 0 {
			continue
		}
		encoded, err := e.sourceLayerXML(layer, context.name.Space)
		if err != nil {
			return nil, err
		}
		added = append(added, bytes.TrimPrefix(encoded, []byte(xml.Header))...)
	}
	if len(added) != 0 {
		content := source.root.child("Content")
		if content == nil {
			value, err := context.containerXML("Content", nil, added)
			if err != nil {
				return nil, err
			}
			position := source.root.close
			if actions := source.root.child("Actions"); actions != nil {
				position = actions.start
			}
			patches = append(patches, editorXMLPatch{position, position, value})
		} else if content.open == content.end {
			patches = append(patches, editorXMLContent(data, content, added))
		} else {
			patches = append(patches, editorXMLPatch{content.close, content.close, added})
		}
	}
	if !reflect.DeepEqual(page.Actions, source.original.Actions) {
		encoded, err := editorActionsXML(page.Actions)
		if err != nil {
			return nil, err
		}
		patch := editorXMLPatch{source.root.close, source.root.close, encoded}
		context := source.root
		if node := source.root.child("Actions"); node != nil {
			patch.start, patch.end = node.start, node.end
			context = node
		}
		patch.data, err = editorXMLGenerated(encoded, context.name.Space)
		if err != nil {
			return nil, err
		}
		patches = append(patches, patch)
	}
	return editorPatchXML(data, patches), nil
}

// sourceLayerXML 写出新增图层，对复制对象保留原始定位和显式默认值
// 入参: layer 图层, namespace 所在容器的命名空间
// 返回: []byte 图层XML, error 错误信息
func (e *Editor) sourceLayerXML(layer Layer, namespace string) ([]byte, error) {
	if !slices.ContainsFunc(layer.Objects, func(object GraphicObject) bool { return e.objectOrigin(editorObjectID(object)) != nil }) {
		data, err := e.encodeXML(func(x *ofdXML) {
			attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: layer.ID}, {Name: xml.Name{Local: "Type"}, Value: layer.Type}}
			attrs.add("DrawParam", layer.DrawParam)
			x.root("Layer", attrs)
			for _, object := range layer.Objects {
				if x.err != nil {
					return
				}
				x.object(object, false)
			}
			x.end("Layer")
		})
		if err != nil {
			return nil, err
		}
		return editorXMLGenerated(bytes.TrimPrefix(data, []byte(xml.Header)), namespace)
	}
	var content []byte
	for _, object := range layer.Objects {
		var data []byte
		var err error
		if origin := e.objectOrigin(editorObjectID(object)); origin != nil {
			data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
			if err == nil {
				data, err = editorXMLStandalone(data, origin.node)
			}
		} else {
			data, err = editorObjectXML(object)
			if err == nil {
				data, err = editorXMLGenerated(data, namespace)
			}
		}
		if err != nil {
			return nil, err
		}
		content = append(content, data...)
	}
	attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: layer.ID}, {Name: xml.Name{Local: "Type"}, Value: layer.Type}}
	attrs.add("DrawParam", layer.DrawParam)
	return (&editorXML{name: xml.Name{Space: namespace}}).containerXML("Layer", attrs, content)
}

// sourceNewPageXML 编码新增页面，同时保留复制对象的原文语义
// 入参: page 页面
// 返回: []byte 页面XML, error 错误信息
func (e *Editor) sourceNewPageXML(page PageContent) ([]byte, error) {
	return e.sourcePageData(page, nil)
}

// sourcePageData 编码新增页面，仅在未保留原文时收集编码引用
// 入参: page 页面内容, references 本次引用记录，可为nil
// 返回: []byte 页面XML, error 编码错误
func (e *Editor) sourcePageData(page PageContent, references *editorReferenceScan) ([]byte, error) {
	if !e.sourcePagePreserved(page) {
		var data bytes.Buffer
		x := newOFDXML(&data)
		if e.output != nil {
			x.ctx = e.output.ctx
		}
		x.references = references
		x.page(page)
		err := x.finish()
		return bytes.TrimPrefix(data.Bytes(), []byte(xml.Header)), err
	}
	area, err := editorXMLContainer("Area", nil, editorXMLText("PhysicalBox", page.Area.PhysicalBox))
	if err != nil {
		return nil, err
	}
	var layers []byte
	for _, layer := range page.Content.Layer {
		data, err := e.sourceLayerXML(layer, ofdNamespace2016)
		if err != nil {
			return nil, err
		}
		layers = append(layers, data...)
	}
	content, err := editorXMLContainer("Content", nil, layers)
	if err != nil {
		return nil, err
	}
	data := append(area, content...)
	if len(page.Actions) != 0 {
		actions, err := e.encodeXML(func(x *ofdXML) { x.actions(page.Actions) })
		if err != nil {
			return nil, err
		}
		data = append(data, bytes.TrimPrefix(actions, []byte(xml.Header))...)
	}
	return editorXMLContainer("Page", nil, data)
}

// sourcePagePreserved 判断新增页面是否仍有对象原文需要保留
// 入参: page 页面内容
// 返回: bool 是否包含原文对象
func (e *Editor) sourcePagePreserved(page PageContent) bool {
	for _, layer := range page.Content.Layer {
		if slices.ContainsFunc(layer.Objects, func(object GraphicObject) bool { return e.objectOrigin(editorObjectID(object)) != nil }) {
			return true
		}
	}
	return false
}

// writeSource 将未修改ZIP条目直接复制到新包，逐项写入改动，不持有原资源解压副本
// 入参: writer 输出流, fonts 新增字体子集, progress 保存进度回调
// 返回: int64 写入字节数, error 错误信息
func (e *Editor) writeSource(writer io.Writer, fonts map[string][]byte, progress editorProgress) (int64, error) {
	generated := make(map[string]editorGeneratedReferences)
	parts, err := e.sourcePartsPrepared(progress, generated, true)
	if err != nil {
		return 0, err
	}
	maps.Copy(parts, fonts)
	removed, err := e.compactSourceReferences(parts, progress, generated)
	if err != nil {
		return 0, err
	}
	reader := e.source.reader
	if removed == nil {
		removed = make(map[string]bool)
	}
	if err := e.compressResourceReferences(parts, reader, removed, generated); err != nil {
		return 0, err
	}
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	parts, err = reader.versionOutputChanges(ctx, parts, removed, generated)
	if err != nil {
		return 0, err
	}
	if len(parts) != 0 || len(removed) != 0 || e.output != nil && e.output.options.Mode != CompressionUnchanged && !e.output.protected {
		name := "OFD.xml"
		if file, ok := reader.packageFile(name); ok {
			name = cleanPackagePath(file.Name)
		}
		data, ok := parts[name]
		if !ok {
			data, err = reader.readFile(name)
			if err != nil {
				return 0, err
			}
		}
		data, err = editorCreatorXML(data, e.source.fallbackDocID, reader.documentIndex)
		if err != nil {
			return 0, err
		}
		parts[name] = data
	}
	remaining := maps.Clone(reader.files)
	if remaining == nil {
		remaining = make(map[string][]byte)
	}
	maps.Copy(remaining, parts)
	for name := range remaining {
		if removed[cleanPackagePath(name)] {
			delete(remaining, name)
		}
	}
	if err := progress.report("write", 0, 0); err != nil {
		return 0, err
	}
	output := &ofdCountingWriter{writer: writer}
	archive := zip.NewWriter(output)
	if reader.Zip != nil {
		_ = archive.SetComment(reader.Zip.Comment)
		buffer := make([]byte, 32*1024)
		for _, file := range reader.Zip.File {
			if file.FileInfo().IsDir() {
				header := file.FileHeader
				if _, err := archive.CreateHeader(&header); err != nil {
					return output.count, err
				}
				continue
			}
			name := cleanPackagePath(file.Name)
			if removed[name] {
				continue
			}
			if data, ok := remaining[name]; ok {
				header := file.FileHeader
				if staged := generated[name].staged; data == nil && staged != nil {
					if err := e.writeStagedPage(archive, header, staged); err != nil {
						return output.count, err
					}
				} else if err := e.writeOutputEntry(archive, header, data); err != nil {
					return output.count, err
				}
				delete(remaining, name)
			} else if e.output != nil && e.output.options.Mode != CompressionUnchanged {
				if err := e.writeOutputFile(archive, file, buffer); err != nil {
					return output.count, err
				}
			} else {
				input, err := file.OpenRaw()
				if err != nil {
					return output.count, err
				}
				header := file.FileHeader
				entry, err := archive.CreateRaw(&header)
				if err != nil {
					return output.count, err
				}
				if _, err := io.CopyBuffer(entry, input, buffer); err != nil {
					return output.count, err
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(remaining)) {
		method := uint16(zip.Store)
		if strings.HasSuffix(strings.ToLower(name), ".xml") {
			method = zip.Deflate
		}
		header := zip.FileHeader{Name: name, Method: method}
		if staged := generated[name].staged; remaining[name] == nil && staged != nil {
			if err := e.writeStagedPage(archive, header, staged); err != nil {
				return output.count, err
			}
		} else if err := e.writeOutputEntry(archive, header, remaining[name]); err != nil {
			return output.count, err
		}
	}
	err = archive.Close()
	return output.count, err
}

// sourceReader 生成原包与修改条目组成的独立预览快照，不进行ZIP压缩
// 入参: progress 准备进度回调
// 返回: *Reader 阅读器, error 错误信息
func (e *Editor) sourceReader(progress editorProgress) (*Reader, error) {
	parts, err := e.sourceParts(progress)
	if err != nil {
		return nil, err
	}
	otherParts := maps.Clone(parts)
	if len(parts) != 0 {
		for _, page := range e.source.pages {
			if page.original != nil {
				name := e.source.reader.ResPath(page.ref.BaseLoc)
				if file, ok := e.source.reader.packageFile(name); ok {
					name = cleanPackagePath(file.Name)
				}
				delete(otherParts, name)
			}
		}
	}
	if len(otherParts) == 0 {
		reader := cloneEditorReader(e.source.reader)
		if reader.files == nil {
			reader.files = make(map[string][]byte)
		}
		maps.Copy(reader.files, parts)
		reader.encryption = e.encryption
		for name := range reader.pageHeaderCache {
			if _, changed := parts[name]; changed {
				delete(reader.pageHeaderCache, name)
			}
		}
		if _, err := reader.Doc(); err != nil {
			return nil, err
		}
		return reader, nil
	}
	files := maps.Clone(e.source.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, parts)
	reader := &Reader{Zip: e.source.reader.Zip, files: files, encryption: e.encryption, documentIndex: e.source.reader.documentIndex, selectedVersion: e.source.reader.selectedVersion}
	if err := reader.initRoot(); err != nil {
		return nil, err
	}
	if _, err := reader.Doc(); err != nil {
		return nil, err
	}
	for name, header := range e.source.reader.pageHeaderCache {
		if _, changed := parts[name]; !changed {
			reader.pageHeaderCache[name] = header
		}
	}
	return reader, nil
}
