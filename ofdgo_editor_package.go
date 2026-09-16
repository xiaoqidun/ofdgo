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
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// sourceParts 生成编辑过的XML和新增资源，不读取未修改页面或原始二进制资源。
// 返回: map[string][]byte 替换及新增条目, error 错误信息
func (e *Editor) sourceParts() (map[string][]byte, error) {
	parts := make(map[string][]byte)
	source := e.source
	reader := source.reader
	pageRefs := make([]Page, len(e.pages))
	for i, page := range e.pages {
		original := source.pages[page.ID]
		if original != nil {
			pageRefs[i] = original.ref
			if original.original == nil {
				continue
			}
			data, err := e.sourcePageXML(i, original)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(data, original.data) {
				parts[reader.ResPath(original.ref.BaseLoc)] = data
			}
		} else {
			name := path.Join(source.directory, "Pages", page.ID+".xml")
			data, err := e.sourceNewPageXML(page)
			if err != nil {
				return nil, err
			}
			parts[name] = data
			pageRefs[i] = Page{ID: page.ID, BaseLoc: "/" + name}
		}
	}
	fonts, images, resourceFiles := e.usedResources()
	commonData := source.document.CommonData
	for _, name := range append(slices.Clone(commonData.DocumentRes), commonData.PublicRes...) {
		resourceFiles = slices.DeleteFunc(resourceFiles, func(file string) bool {
			return strings.EqualFold(file, reader.ResPath(name))
		})
	}
	if len(fonts)+len(images) != 0 {
		resourcePath := path.Join(source.directory, "Resources.xml")
		resourceFiles = append(resourceFiles, resourcePath)
		data, err := encodeOFDXML(func(x *ofdXML) { x.resources(fonts, images) })
		if err != nil {
			return nil, err
		}
		parts[resourcePath] = data
		for _, resource := range append(fonts, images...) {
			if resource.name != "" {
				parts[resource.name] = resource.data
			}
		}
	}
	pagesChanged := !reflect.DeepEqual(pageRefs, source.document.Pages.Page)
	if len(parts) != 0 || pagesChanged {
		name := reader.ResPath(reader.OFD.DocBody[0].DocRoot)
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
			value := editorXMLText("MaxUnitID", strconv.Itoa(e.maxID))
			if maxID := common.child("MaxUnitID"); maxID != nil {
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
				references = append(references, editorXMLText("DocumentRes", "/"+name)...)
			}
			patches = append(patches, editorXMLPatch{position, position, references})
		}
		if pagesChanged {
			pages := root.child("Pages")
			if pages == nil {
				return nil, fmt.Errorf("document has no Pages")
			}
			var encoded bytes.Buffer
			for _, page := range pageRefs {
				var original *editorXML
				for _, child := range pages.children {
					if child.attr("ID") == page.ID {
						original = child
						break
					}
				}
				if original != nil {
					encoded.Write(data[original.start:original.end])
				} else {
					item, err := encodeOFDXML(func(x *ofdXML) {
						x.root("Page", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: page.ID}, {Name: xml.Name{Local: "BaseLoc"}, Value: page.BaseLoc}})
						x.end("Page")
					})
					if err != nil {
						return nil, err
					}
					encoded.Write(bytes.TrimPrefix(item, []byte(xml.Header)))
				}
			}
			patches = append(patches, editorXMLContent(data, pages, encoded.Bytes()))
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
	for name, data := range maps.Clone(parts) {
		if file, ok := reader.packageFile(name); ok {
			actual := cleanPackagePath(file.Name)
			if actual != name {
				delete(parts, name)
				parts[actual] = data
			}
		}
	}
	return parts, nil
}

// sourcePageXML 仅改写已修改的对象、页面尺寸和新增图层，保留原页块层级。
// 入参: index 页面索引, source 原页面
// 返回: []byte 页面XML, error 错误信息
func (e *Editor) sourcePageXML(index int, source *editorSourcePage) ([]byte, error) {
	page := e.pages[index]
	if reflect.DeepEqual(page, *source.original) {
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
			areaData := data[area.start:area.end]
			areaRoot, err := parseEditorXML(areaData)
			if err != nil {
				return nil, err
			}
			updated := editorXMLSetText(areaData, areaRoot, [][2]string{{"PhysicalBox", page.Area.PhysicalBox}})
			patches = append(patches, editorXMLPatch{area.start, area.end, updated})
		} else {
			value := []byte("<ofd:Area xmlns:ofd=\"" + ofdNamespace + "\">")
			value = append(value, editorXMLText("PhysicalBox", page.Area.PhysicalBox)...)
			value = append(value, []byte("</ofd:Area>")...)
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
			if node := source.nodes[id]; node != nil {
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
				if reflect.DeepEqual(before[nextID], next) {
					encoded = data[origin.start:origin.end]
				} else {
					var err error
					encoded, err = editorXMLObject(data, origin, before[nextID], next)
					if err != nil {
						return nil, err
					}
				}
			}
			if !bytes.Equal(encoded, data[node.start:node.end]) {
				patches = append(patches, editorXMLPatch{node.start, node.end, encoded})
			}
		}
	}
	var added []byte
	for _, layer := range page.Content.Layer[len(source.original.Content.Layer):] {
		if len(layer.Objects) == 0 {
			continue
		}
		encoded, err := e.sourceLayerXML(layer)
		if err != nil {
			return nil, err
		}
		added = append(added, bytes.TrimPrefix(encoded, []byte(xml.Header))...)
	}
	if len(added) != 0 {
		content := source.root.child("Content")
		if content == nil {
			value := []byte("<ofd:Content xmlns:ofd=\"" + ofdNamespace + "\">")
			value = append(value, added...)
			value = append(value, []byte("</ofd:Content>")...)
			patches = append(patches, editorXMLPatch{source.root.close, source.root.close, value})
		} else if content.open == content.end {
			patches = append(patches, editorXMLContent(data, content, added))
		} else {
			patches = append(patches, editorXMLPatch{content.close, content.close, added})
		}
	}
	return editorPatchXML(data, patches), nil
}

// sourceLayerXML 写出新增图层，对复制对象保留原始定位和显式默认值。
// 入参: layer 图层
// 返回: []byte 图层XML, error 错误信息
func (e *Editor) sourceLayerXML(layer Layer) ([]byte, error) {
	var content []byte
	for _, object := range layer.Objects {
		var data []byte
		var err error
		if origin := e.objectOrigin(editorObjectID(object)); origin != nil {
			data, err = editorXMLObject(origin.page.data, origin.node, origin.object, object)
			if err == nil {
				data, err = editorXMLStandalone(data, origin.node)
			}
		} else {
			data, err = editorObjectXML(object)
		}
		if err != nil {
			return nil, err
		}
		content = append(content, data...)
	}
	return editorXMLContainer("Layer", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: layer.ID}, {Name: xml.Name{Local: "Type"}, Value: layer.Type}}, content)
}

// sourceNewPageXML 编码新增页面，同时保留复制对象的原文语义。
// 入参: page 页面
// 返回: []byte 页面XML, error 错误信息
func (e *Editor) sourceNewPageXML(page PageContent) ([]byte, error) {
	area, err := editorXMLContainer("Area", nil, editorXMLText("PhysicalBox", page.Area.PhysicalBox))
	if err != nil {
		return nil, err
	}
	var layers []byte
	for _, layer := range page.Content.Layer {
		data, err := e.sourceLayerXML(layer)
		if err != nil {
			return nil, err
		}
		layers = append(layers, data...)
	}
	content, err := editorXMLContainer("Content", nil, layers)
	if err != nil {
		return nil, err
	}
	return editorXMLContainer("Page", nil, append(area, content...))
}

// sourceInfoXML 更新公开元数据，保留原创建程序和未识别字段。
// 返回: []byte 根索引XML, error 错误信息
func (e *Editor) sourceInfoXML() ([]byte, error) {
	data, err := e.source.reader.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	body := root.child("DocBody")
	if body == nil {
		return nil, fmt.Errorf("document has no DocBody")
	}
	info := body.child("DocInfo")
	if info == nil {
		return nil, fmt.Errorf("document has no DocInfo")
	}
	before, after := e.source.info, e.Info
	if !reflect.DeepEqual(before.CustomDatas, after.CustomDatas) {
		return nil, fmt.Errorf("editing imported custom metadata is not supported")
	}
	var values [][2]string
	for _, field := range [][3]string{
		{"DocID", before.DocID, after.DocID}, {"Title", before.Title, after.Title}, {"Author", before.Author, after.Author},
		{"Subject", before.Subject, after.Subject}, {"Abstract", before.Abstract, after.Abstract},
		{"CreationDate", before.CreationDate, after.CreationDate}, {"ModDate", before.ModDate, after.ModDate},
	} {
		if field[1] != field[2] {
			values = append(values, [2]string{field[0], field[2]})
		}
	}
	return editorXMLSetText(data, info, values), nil
}

// writeSource 将未修改ZIP条目直接复制到新包，逐项写入改动，不持有原资源解压副本。
// 入参: writer 输出流, fonts 新增字体子集
// 返回: int64 写入字节数, error 错误信息
func (e *Editor) writeSource(writer io.Writer, fonts map[string][]byte) (int64, error) {
	parts, err := e.sourceParts()
	if err != nil {
		return 0, err
	}
	maps.Copy(parts, fonts)
	reader := e.source.reader
	remaining := maps.Clone(reader.files)
	if remaining == nil {
		remaining = make(map[string][]byte)
	}
	maps.Copy(remaining, parts)
	output := &ofdCountingWriter{writer: writer}
	archive := zip.NewWriter(output)
	if reader.Zip != nil {
		_ = archive.SetComment(reader.Zip.Comment)
		for _, file := range reader.Zip.File {
			if file.FileInfo().IsDir() {
				header := file.FileHeader
				if _, err := archive.CreateHeader(&header); err != nil {
					return output.count, err
				}
				continue
			}
			name := cleanPackagePath(file.Name)
			if data, ok := remaining[name]; ok {
				header := file.FileHeader
				entry, err := archive.CreateHeader(&header)
				if err != nil {
					return output.count, err
				}
				if _, err := entry.Write(data); err != nil {
					return output.count, err
				}
				delete(remaining, name)
			} else if err := archive.Copy(file); err != nil {
				return output.count, err
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(remaining)) {
		method := uint16(zip.Store)
		if strings.HasSuffix(strings.ToLower(name), ".xml") {
			method = zip.Deflate
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			return output.count, err
		}
		if _, err := entry.Write(remaining[name]); err != nil {
			return output.count, err
		}
	}
	err = archive.Close()
	return output.count, err
}

// sourceReader 生成原包与修改条目组成的独立预览快照，不进行ZIP压缩。
// 返回: *Reader 阅读器, error 错误信息
func (e *Editor) sourceReader() (*Reader, error) {
	parts, err := e.sourceParts()
	if err != nil {
		return nil, err
	}
	files := maps.Clone(e.source.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, parts)
	reader := &Reader{Zip: e.source.reader.Zip, files: files}
	if err := reader.initRoot(); err != nil {
		return nil, err
	}
	if _, err := reader.Doc(); err != nil {
		return nil, err
	}
	return reader, nil
}
