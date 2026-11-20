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
	"compress/flate"
	"context"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"
)

// editorStagedPage 保存单次写出的压缩页面及校验值，不持有未压缩XML
type editorStagedPage struct {
	data     []byte
	size     uint64
	checksum uint32
}

// open 流式读取暂存页面，仅用于无法完整收集编码引用的分支
// 返回: io.ReadCloser 页面解压流
func (p *editorStagedPage) open() io.ReadCloser {
	return flate.NewReader(bytes.NewReader(p.data))
}

// editorPageStager 复用单次保存的页面压缩器，不跨文档共享状态
type editorPageStager struct {
	compressor *flate.Writer
}

// encode 边编码边压缩自产页面，同步收集引用及用字
// 入参: editor 保存快照, page 页面内容, references 本次引用扫描
// 返回: *editorStagedPage 压缩页面, error 编码或取消错误
func (s *editorPageStager) encode(editor *Editor, page PageContent, references *editorReferenceScan) (*editorStagedPage, error) {
	var data bytes.Buffer
	if s.compressor == nil {
		level := flate.DefaultCompression
		if editor.output != nil && editor.output.options.Mode != CompressionUnchanged {
			level = flate.BestCompression
		}
		var err error
		s.compressor, err = flate.NewWriter(&data, level)
		if err != nil {
			return nil, err
		}
	} else {
		s.compressor.Reset(&data)
	}
	output := editorPageEncoding{writer: s.compressor}
	x := &ofdXML{encoder: xml.NewEncoder(&output), references: references}
	if editor.output != nil {
		x.ctx = editor.output.ctx
	}
	x.page(page)
	if err := x.finish(); err != nil {
		return nil, err
	}
	if err := s.compressor.Close(); err != nil {
		return nil, err
	}
	if !x.checkContext() {
		return nil, x.err
	}
	return &editorStagedPage{data: data.Bytes(), size: output.size, checksum: output.checksum}, nil
}

// editorPageEncoding 同步统计编码字节及CRC，保留实际写入错误
type editorPageEncoding struct {
	writer   io.Writer
	size     uint64
	checksum uint32
}

// Write 写入页面字节并统计实际接收的部分
// 入参: data 未压缩编码数据
// 返回: int 实际字节数, error 写入或短写错误
func (w *editorPageEncoding) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.size += uint64(n)
	w.checksum = crc32.Update(w.checksum, crc32.IEEETable, data[:n])
	if n != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

// sourceParts 准备编辑或归档条目，仅在最终归档或导出时同步普通文字排版标引
// 入参: ctx 取消上下文, progress 进度回调, generated 本次页面及引用, stage 是否压缩暂存, syncText 是否同步普通文字标引
// 返回: map[string][]byte 输出条目, map[string]bool 已淘汰的旧标引路径, error 编码错误
func (e *Editor) sourceParts(ctx context.Context, progress editorProgress, generated map[string]editorGeneratedReferences, stage, syncText bool) (map[string][]byte, map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	progress = progress.withContext(ctx)
	parts := make(map[string][]byte)
	var stager editorPageStager
	source := e.source
	reader := source.reader
	pageRefs := make([]Page, len(e.pages))
	keptSource := make(map[string]bool, len(e.pages))
	originalCount := 0
	for i, page := range e.pages {
		if err := progress.report("pages", i, len(e.pages)); err != nil {
			return nil, nil, err
		}
		original := source.pages[page.ID]
		if original != nil {
			keptSource[page.ID] = true
			originalCount++
			pageRefs[i] = original.ref
			if original.original == nil {
				continue
			}
			data, err := e.sourcePageXML(i, original)
			if err != nil {
				return nil, nil, err
			}
			if original.repaired || !bytes.Equal(data, original.data) {
				parts[reader.ResPath(original.ref.BaseLoc)] = data
			}
		} else {
			name := e.packageName(packagePagePath("", page.ID))
			bound := len(source.structureObjects[editorResourceID(page.ID)]) != 0
			var scan *editorReferenceScan
			if generated != nil {
				scan = &editorReferenceScan{refs: &editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}, name: name, safe: true}
				if bound {
					scan.graphicIDs = make(map[string]bool)
				}
			}
			if stage && generated != nil && !e.sourcePagePreserved(page) {
				data, err := stager.encode(e, page, scan)
				if err != nil {
					return nil, nil, err
				}
				parts[name] = nil
				known := editorGeneratedReferences{staged: data}
				if scan != nil && scan.safe && scan.seen && len(scan.stack) == 0 {
					known.refs = scan.refs
					known.objects = scan.graphicIDs
				}
				generated[name] = known
				if (reader.DocumentCount() > 1 || reader.OFD.DocBody[reader.documentIndex].versioned) && !known.stagedLeaf(nil) || bound && known.objects == nil {
					input := imageInput{ReadCloser: data.open(), ctx: ctx}
					parts[name], err = io.ReadAll(input)
					input.Close()
					if err != nil {
						return nil, nil, err
					}
					generated[name] = editorGeneratedReferences{data: parts[name], refs: known.refs, objects: known.objects}
				}
			} else {
				data, err := e.sourcePageData(page, scan)
				if err != nil {
					return nil, nil, err
				}
				parts[name] = data
				if scan != nil && scan.safe && scan.seen && len(scan.stack) == 0 {
					generated[name] = editorGeneratedReferences{data: data, refs: scan.refs, objects: scan.graphicIDs}
				}
			}
			pageRefs[i] = Page{ID: page.ID, BaseLoc: "/" + name}
		}
	}
	if err := progress.report("pages", len(e.pages), len(e.pages)); err != nil {
		return nil, nil, err
	}
	fonts, images, spaces, usedFiles, extraParts, err := e.usedResources(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, resource := range e.resources {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if resource.definition() != "" && usedFiles[resource.name] || resource.colorProfileUsed(spaces) {
			parts[resource.name] = resource.data
		}
	}
	maps.Copy(parts, extraParts)
	commonData := source.document.CommonData
	for _, name := range append(slices.Clone(commonData.DocumentRes), commonData.PublicRes...) {
		delete(usedFiles, reader.ResPath(name))
	}
	resourceFiles := slices.Sorted(maps.Keys(usedFiles))
	if len(fonts)+len(images)+len(spaces) != 0 {
		data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) { x.resources(fonts, images, spaces) })
		if err != nil {
			return nil, nil, err
		}
		resourcePath, added, err := mergePackageResources(ctx, reader, parts, source.document, source.directory, data)
		if err != nil {
			return nil, nil, err
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
	if pagesChanged || len(resourceFiles) != 0 || e.maxID != uint64(source.document.CommonData.MaxUnitID) {
		name := reader.documentRoot
		data, err := reader.readFileViewContext(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, nil, err
		}
		common := root.child("CommonData")
		if common == nil {
			return nil, nil, fmt.Errorf("document has no CommonData")
		}
		var patches []editorXMLPatch
		if e.maxID != uint64(source.document.CommonData.MaxUnitID) {
			value := common.textXML("MaxUnitID", formatUnitID(e.maxID))
			if maxID := common.child("MaxUnitID"); maxID != nil {
				value = maxID.textXML("MaxUnitID", formatUnitID(e.maxID))
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
				return nil, nil, fmt.Errorf("document has no Pages")
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
						return nil, nil, err
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
			return nil, nil, err
		}
		parts["OFD.xml"] = data
	}
	if e.outlines != nil {
		name := reader.documentRoot
		data, ok := parts[name]
		if !ok {
			var err error
			data, err = reader.readFileViewContext(ctx, name)
			if err != nil {
				return nil, nil, err
			}
		}
		updated, err := e.withOutlines(data)
		if err != nil {
			return nil, nil, err
		}
		parts[name] = updated
	}
	if originalCount != len(source.pages) || len(source.annotationPages) != 0 || len(e.removedPages) != 0 || len(source.removedAttachments) != 0 {
		if err := e.pruneDocumentReferences(ctx, parts, generated); err != nil {
			return nil, nil, err
		}
	}
	if err := e.pruneStructureReferences(ctx, parts, generated); err != nil {
		return nil, nil, err
	}
	var retired map[string]bool
	if syncText {
		retired, err = e.syncSourceTextMetadata(ctx, parts)
		if err != nil {
			return nil, nil, err
		}
	}
	retired, err = e.collectRetiredPages(ctx, keptSource, retired)
	if err != nil {
		return nil, nil, err
	}
	for name, data := range maps.Clone(parts) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
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
	for name := range maps.Clone(retired) {
		if file, ok := reader.packageFile(name); ok {
			actual := cleanPackagePath(file.Name)
			if actual != name {
				delete(retired, name)
				retired[actual] = true
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return parts, retired, nil
}

// collectRetiredPages 收集相对原包已删除页面、其页面资源及仅归属被删页的注解路径
// 入参: ctx 取消上下文, keptSource 保留的原页面标识, retired 已有淘汰路径集合
// 返回: map[string]bool 合并后的淘汰路径集合, error 读取或取消错误
func (e *Editor) collectRetiredPages(ctx context.Context, keptSource, retired map[string]bool) (map[string]bool, error) {
	source := e.source
	if source == nil || len(keptSource) == len(source.pages) {
		return retired, ctx.Err()
	}
	reader := source.reader
	addRetired := func(name string) {
		clean := cleanPackagePath(name)
		if clean == "" || clean == "." {
			return
		}
		if retired == nil {
			retired = make(map[string]bool)
		}
		retired[clean] = true
	}
	removedPageIDs := make(map[string]bool)
	for id, orig := range source.pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if keptSource[id] {
			continue
		}
		if rid := editorResourceID(id); rid != "" {
			removedPageIDs[rid] = true
		}
		pagePath := cleanPackagePath(reader.ResPath(orig.ref.BaseLoc))
		if pagePath == "" || pagePath == "." {
			continue
		}
		addRetired(pagePath)
		data, err := reader.readFileViewContext(ctx, pagePath)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		for _, child := range root.children {
			if child.matchesOFD("PageRes") && len(child.children) == 0 {
				if loc := strings.TrimSpace(editorImportText(data, child)); loc != "" {
					addRetired(resolveResourcePath(pagePath, "", loc))
				}
			}
		}
	}
	if len(removedPageIDs) != 0 && source.document.Annotations != "" {
		annotsPath := cleanPackagePath(reader.ResPath(source.document.Annotations))
		if data, err := reader.readFileViewContext(ctx, annotsPath); err == nil {
			if root, parseErr := parseEditorXMLContext(ctx, data); parseErr == nil {
				removedAnnots := make(map[string]bool)
				keptAnnots := make(map[string]bool)
				for _, node := range root.children {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if !node.matchesOFD("Page") {
						continue
					}
					target := keptAnnots
					if removedPageIDs[editorResourceID(node.attr("PageID"))] {
						target = removedAnnots
					}
					if loc := strings.TrimSpace(node.attr("FileLoc")); loc != "" {
						target[cleanPackagePath(resolveResourcePath(annotsPath, "", loc))] = true
					}
					for _, child := range node.children {
						if child.matchesOFD("FileLoc") && len(child.children) == 0 {
							if loc := strings.TrimSpace(editorImportText(data, child)); loc != "" {
								target[cleanPackagePath(resolveResourcePath(annotsPath, "", loc))] = true
							}
						}
					}
				}
				for name := range removedAnnots {
					if !keptAnnots[name] {
						addRetired(name)
					}
				}
			} else if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return retired, ctx.Err()
}

// sourcePageXML 仅改写已修改的对象、页面尺寸、动作和新增图层，保留原页块层级
// 入参: index 页面索引, source 原页面
// 返回: []byte 页面XML, error 错误信息
func (e *Editor) sourcePageXML(index int, source *editorSourcePage) ([]byte, error) {
	page := e.pages[index]
	unchanged := sourcePageUntouched(page, source) || reflect.DeepEqual(page, *source.original)
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
	if page.Area != source.original.Area {
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
			var fields [][2]string
			for _, field := range [][3]string{
				{"PhysicalBox", page.Area.PhysicalBox, source.original.Area.PhysicalBox},
				{"ApplicationBox", page.Area.ApplicationBox, source.original.Area.ApplicationBox},
				{"ContentBox", page.Area.ContentBox, source.original.Area.ContentBox},
				{"BleedBox", page.Area.BleedBox, source.original.Area.BleedBox},
			} {
				if field[1] != field[2] {
					fields = append(fields, [2]string{field[0], field[1]})
				}
			}
			updated, err := editorXMLSetText(areaData, areaRoot, fields)
			if err != nil {
				return nil, err
			}
			patches = append(patches, editorXMLPatch{area.start, area.end, updated})
		} else {
			var fields []byte
			for _, field := range [][2]string{{"PhysicalBox", page.Area.PhysicalBox}, {"ApplicationBox", page.Area.ApplicationBox}, {"ContentBox", page.Area.ContentBox}, {"BleedBox", page.Area.BleedBox}} {
				if field[1] != "" {
					fields = append(fields, source.root.textXML(field[0], field[1])...)
				}
			}
			value, err := source.root.containerXML("Area", nil, fields)
			if err != nil {
				return nil, err
			}
			position := source.root.open
			for _, child := range source.root.children {
				if child.matchesOFD("Template") || child.matchesOFD("PageRes") {
					position = child.end
				}
			}
			patches = append(patches, editorXMLPatch{position, position, value})
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
	ctx := source.root.child("Content")
	if ctx == nil {
		ctx = source.root
	}
	for _, layer := range page.Content.Layer[len(source.original.Content.Layer):] {
		if len(layer.Objects) == 0 {
			continue
		}
		encoded, err := e.sourceLayerXML(layer, ctx.name.Space)
		if err != nil {
			return nil, err
		}
		added = append(added, bytes.TrimPrefix(encoded, []byte(xml.Header))...)
	}
	if len(added) != 0 {
		content := source.root.child("Content")
		if content == nil {
			value, err := ctx.containerXML("Content", nil, added)
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
		ctx := source.root
		if node := source.root.child("Actions"); node != nil {
			patch.start, patch.end = node.start, node.end
			ctx = node
		}
		patch.data, err = editorXMLGenerated(encoded, ctx.name.Space)
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
	header, err := e.encodeXML(func(x *ofdXML) { x.pageHeader(page) })
	if err != nil {
		return nil, err
	}
	data := bytes.Clone(bytes.TrimPrefix(header, []byte(xml.Header)))
	if len(page.Content.Layer) > 0 {
		var layers []byte
		for _, layer := range page.Content.Layer {
			encoded, err := e.sourceLayerXML(layer, ofdNamespace2016)
			if err != nil {
				return nil, err
			}
			layers = append(layers, encoded...)
		}
		content, err := editorXMLContainer("Content", nil, layers)
		if err != nil {
			return nil, err
		}
		data = append(data, content...)
	}
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
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	generated := make(map[string]editorGeneratedReferences)
	parts, retiredParts, err := e.sourceParts(ctx, progress, generated, true, true)
	if err != nil {
		return 0, err
	}
	maps.Copy(parts, fonts)
	reader := e.source.reader
	retired := maps.Clone(e.source.retiredAttachments)
	if len(retiredParts) != 0 {
		if retired == nil {
			retired = make(map[string]bool, len(retiredParts))
		}
		maps.Copy(retired, retiredParts)
	}
	versioned := reader.OFD.DocBody[reader.documentIndex].versioned
	if !versioned && reader.DocumentCount() > 1 {
		parts, err = reader.versionOutputChanges(ctx, parts, retired, generated)
		if err != nil {
			return 0, err
		}
	}
	removed, err := e.compactResourceReferences(parts, reader, progress, generated, retired)
	if err != nil {
		return 0, err
	}
	if removed == nil {
		removed = make(map[string]bool)
	}
	if len(retired) != 0 && (e.output == nil || !e.output.protected) {
		protected, sigErr := reader.hasOutputSignatures(ctx)
		if sigErr != nil {
			return 0, sigErr
		}
		if !protected {
			shared, shErr := reader.versionSharedFiles(ctx)
			docRoot, rootErr := reader.DocumentRootContext(ctx)
			var currentUsed map[string]bool
			var depErr error
			if shErr == nil && rootErr == nil {
				currentUsed, _, depErr = reader.versionDependencies(ctx, docRoot, parts, generated)
			}
			if shErr == nil && rootErr == nil && depErr == nil {
				for itemPath := range retired {
					if !currentUsed[itemPath] && parts[itemPath] == nil && (versioned || !shared[itemPath]) {
						removed[itemPath] = true
					}
				}
			}
		}
	}
	if err := e.compressResourceReferences(parts, reader, removed, generated); err != nil {
		return 0, err
	}
	if versioned {
		parts, err = reader.versionOutputChanges(ctx, parts, removed, generated)
		if err != nil {
			return 0, err
		}
	}
	if err := e.compactPackageFonts(ctx, parts, removed, generated); err != nil {
		return 0, err
	}
	if len(parts) != 0 || len(removed) != 0 || e.output != nil && e.output.options.Mode != CompressionUnchanged && !e.output.protected {
		name := "OFD.xml"
		if file, ok := reader.packageFile(name); ok {
			name = cleanPackagePath(file.Name)
		}
		if e.output == nil || !e.output.protectedFiles[name] {
			data, ok := parts[name]
			if !ok {
				data, err = reader.readFile(name)
				if err != nil {
					return 0, err
				}
			}
			data, _, err = editorDocInfoXML(data, e.source.fallbackDocID, reader.documentIndex)
			if err != nil {
				return 0, err
			}
			parts[name] = data
		}
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
	if e.output != nil && e.output.options.Mode != CompressionUnchanged {
		configureArchive(archive, e.output.options)
	}
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
	if reader.Zip == nil {
		if src := reader.encryptedSource(); src != nil {
			buffer := make([]byte, 32*1024)
			compress := e.output != nil && e.output.options.Mode != CompressionUnchanged
			for _, name := range src.names() {
				if err := ctx.Err(); err != nil {
					return output.count, err
				}
				if removed[name] {
					continue
				}
				if _, modified := remaining[name]; modified {
					continue
				}
				if compress {
					if size, ok := src.size(name); ok && size <= outputOptimizationBufferLimit {
						input, openErr := src.open(name)
						if openErr != nil {
							return output.count, openErr
						}
						data, readErr := io.ReadAll(imageInput{ReadCloser: input, ctx: ctx})
						closeErr := input.Close()
						if readErr != nil {
							return output.count, readErr
						}
						if closeErr != nil {
							return output.count, closeErr
						}
						if err := e.writeOutputEntry(archive, zip.FileHeader{Name: name, Method: zip.Deflate}, data); err != nil {
							return output.count, err
						}
						continue
					}
				}
				method := uint16(zip.Store)
				if compress {
					switch strings.ToLower(path.Ext(name)) {
					case ".jpg", ".jpeg", ".png", ".jp2", ".jb2", ".zip", ".pdf", ".ofd":
					default:
						method = zip.Deflate
					}
				} else if strings.HasSuffix(strings.ToLower(name), ".xml") {
					method = zip.Deflate
				}
				header := zip.FileHeader{Name: name, Method: method}
				input, openErr := src.open(name)
				if openErr != nil {
					return output.count, openErr
				}
				entry, createErr := archive.CreateHeader(&header)
				if createErr != nil {
					_ = input.Close()
					return output.count, createErr
				}
				_, copyErr := io.CopyBuffer(entry, imageInput{ReadCloser: input, ctx: ctx}, buffer)
				closeErr := input.Close()
				if copyErr != nil {
					return output.count, copyErr
				}
				if closeErr != nil {
					return output.count, closeErr
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

// sourceReader 按预览或导出模式构建包快照，导出模式同步普通文字标引并返回淘汰路径
// 入参: ctx 取消上下文, progress 准备进度回调, syncText 是否同步普通文字标引
// 返回: *Reader 阅读器, map[string]bool 已淘汰的旧标引路径, error 错误信息
func (e *Editor) sourceReader(ctx context.Context, progress editorProgress, syncText bool) (*Reader, map[string]bool, error) {
	parts, retired, err := e.sourceParts(ctx, progress, nil, false, syncText)
	if err != nil {
		return nil, nil, err
	}
	parts, err = e.source.reader.versionOutputChanges(ctx, parts, retired, nil)
	if err != nil {
		return nil, nil, err
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
	if len(otherParts) == 0 && len(retired) == 0 && (!syncText || e.source.reader.encryptedSource() == nil) {
		reader := cloneEditorReader(e.source.reader)
		if reader.files == nil {
			reader.files = make(map[string][]byte)
		}
		maps.Copy(reader.files, parts)
		if len(parts) != 0 {
			reader.resourceInfoCache = nil
		}
		reader.encryption = e.encryption
		for name := range reader.pageHeaderCache {
			if _, changed := parts[name]; changed {
				delete(reader.pageHeaderCache, name)
			}
		}
		if _, err := reader.DocContext(ctx); err != nil {
			return nil, nil, err
		}
		return reader, nil, nil
	}
	files := maps.Clone(e.source.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, parts)
	zipArchive := e.source.reader.Zip
	var encSource *encryptedPackageSource
	if src := e.source.reader.encryptedSource(); src != nil {
		if syncText {
			limit := int64(editorSnapshotArchiveLimit)
			if e.runtimeResources != nil {
				limit = e.runtimeResources.options.SnapshotBytes
			}
			zipArchive, err = src.materializeArchive(ctx, limit)
			if err != nil {
				return nil, nil, err
			}
		} else {
			encSource = src
		}
	}
	reader := &Reader{Zip: zipArchive, encrypted: encSource, sourceFile: e.source.reader.sourceFile, files: files, encryption: e.encryption, documentIndex: e.source.reader.documentIndex, selectedVersion: e.source.reader.selectedVersion, pageContents: e.source.reader.pageContents, resources: e.runtimeResources}
	if err := reader.initRootContext(ctx); err != nil {
		return nil, nil, err
	}
	if _, err := reader.DocContext(ctx); err != nil {
		return nil, nil, err
	}
	for name, header := range e.source.reader.pageHeaderCache {
		if _, changed := parts[name]; !changed {
			reader.pageHeaderCache[name] = header
		}
	}
	return reader, retired, nil
}

// writeStagedPage 直接写入已经压缩的自产页面，保持ZIP条目属性和取消语义
// 入参: archive 输出归档, header 条目属性, page 暂存页面
// 返回: error 写入、短写或取消错误
func (e *Editor) writeStagedPage(archive *zip.Writer, header zip.FileHeader, page *editorStagedPage) error {
	if e.output != nil && e.output.options.Mode != CompressionUnchanged {
		if err := editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0); err != nil {
			return err
		}
	}
	prepareOutputHeader(&header, zip.Deflate, page.size, uint64(len(page.data)), page.checksum)
	entry, err := archive.CreateRaw(&header)
	if err != nil {
		return err
	}
	for offset := 0; offset < len(page.data); {
		if e.output != nil {
			if err := e.output.ctx.Err(); err != nil {
				return err
			}
		}
		end := min(offset+64<<10, len(page.data))
		if n, err := entry.Write(page.data[offset:end]); err != nil {
			return err
		} else if n != end-offset {
			return io.ErrShortWrite
		}
		offset = end
	}
	if e.output != nil && e.output.options.Mode != CompressionUnchanged {
		e.output.completed++
		return editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0)
	}
	return nil
}
