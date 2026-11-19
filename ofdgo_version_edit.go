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
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// versionEditFile 保存结构文件及引用位置，仅在改写时读取尚未加载的原文
type versionEditFile struct {
	reader *Reader
	name   string
	data   []byte
	links  []versionEditLink
}

// rewrite 按需读取并更新已隔离文件的引用，保留其余XML及相对路径基准
// 入参: ctx 取消上下文, paths 原路径到新路径的映射
// 返回: []byte 更新后的XML，未改动时返回已加载原文或nil, error 读取或属性编码错误
func (file *versionEditFile) rewrite(ctx context.Context, paths map[string]string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	needed := false
	for _, link := range file.links {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if paths[link.target] != "" {
			needed = true
			break
		}
	}
	if !needed {
		return file.data, nil
	}
	source := file.data
	if source == nil {
		var err error
		source, err = file.reader.readFileViewContext(ctx, file.name)
		if err != nil {
			return nil, err
		}
	}
	var patches []editorXMLPatch
	headers := make(map[*editorXML][]versionEditLink)
	for _, link := range file.links {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if paths[link.target] == "" {
			continue
		}
		if link.attribute == "" {
			patches = append(patches, editorXMLContent(source, link.node, editorFontXMLText("/"+paths[link.target])))
		} else {
			headers[link.node] = append(headers[link.node], link)
		}
	}
	for node, links := range headers {
		data := source[node.start:node.end]
		for _, link := range links {
			root, err := parseEditorXMLContext(ctx, data)
			if err != nil {
				return nil, err
			}
			data, err = editorXMLAttribute(data, root, link.attribute, "/"+paths[link.target])
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		if node.open == node.end {
			patches = append(patches, editorXMLPatch{node.start, node.end, data})
		} else {
			patches = append(patches, editorXMLPatch{node.start, node.open, data[:root.open]})
		}
	}
	if len(patches) == 0 {
		return file.data, nil
	}
	return editorPatchXML(source, patches), ctx.Err()
}

// versionEditLink 记录文件引用的位置与包内目标
type versionEditLink struct {
	node      *editorXML
	attribute string
	target    string
}

// versionEditGraph 保存一个入口的结构文件与全部直接、间接文件依赖
type versionEditGraph struct {
	files map[string]*versionEditFile
	used  map[string]bool
}

// versionFileDependencies 流式汇总版本入口的标准文件引用，不保留页面节点或读取二进制资源
// 入参: ctx 取消上下文, entry 文档入口, parts 待修改条目, generated 本次自产页面及引用
// 返回: map[string]bool 引用路径, error 读取、结构或取消错误
func (r *Reader) versionFileDependencies(ctx context.Context, entry string, parts map[string][]byte, generated map[string]editorGeneratedReferences) (map[string]bool, error) {
	used, _, err := r.versionDependencies(ctx, entry, parts, generated)
	return used, err
}

// versionDependencies 从指定入口汇总文件依赖和结构文件，不混入其他版本或读取二进制资源
// 入参: ctx 取消上下文, entry 文档入口, parts 待修改条目, generated 本次自产页面及引用
// 返回: map[string]bool 全部依赖, map[string]bool 结构文件, error 读取、结构或取消错误
func (r *Reader) versionDependencies(ctx context.Context, entry string, parts map[string][]byte, generated map[string]editorGeneratedReferences) (map[string]bool, map[string]bool, error) {
	view := *r
	if len(parts) != 0 {
		view.files = maps.Clone(r.files)
		if view.files == nil {
			view.files = make(map[string][]byte, len(parts))
		}
		maps.Copy(view.files, parts)
	}
	used := map[string]bool{entry: true}
	queued := map[string]bool{entry: true}
	queue := []string{entry}
	add := func(name string, kind int) {
		used[name] = true
		if kind == packageReferenceXML && !queued[name] {
			queued[name] = true
			queue = append(queue, name)
		}
	}
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := queue[0]
		queue = queue[1:]
		if data, ok := parts[name]; ok && generated[name].stagedLeaf(data) {
			continue
		}
		if err := view.documentFileReferences(ctx, name, false, add); err != nil {
			return nil, nil, err
		}
	}
	return used, queued, ctx.Err()
}

// versionEditGraph 流式建立标准文件依赖图，不保留完整XML节点树或解压后的原包文件
// 入参: ctx 取消上下文, entry 文档入口, parts 待修改条目, generated 本次自产页面及引用
// 返回: *versionEditGraph 依赖图, error 读取、结构或取消错误
func (r *Reader) versionEditGraph(ctx context.Context, entry string, parts map[string][]byte, generated map[string]editorGeneratedReferences) (*versionEditGraph, error) {
	view := *r
	if len(parts) != 0 {
		view.files = maps.Clone(r.files)
		if view.files == nil {
			view.files = make(map[string][]byte, len(parts))
		}
		maps.Copy(view.files, parts)
	}
	graph := &versionEditGraph{files: make(map[string]*versionEditFile), used: make(map[string]bool)}
	queue := []string{entry}
	queued := map[string]bool{entry: true}
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := queue[0]
		queue = queue[1:]
		data, ok := parts[name]
		if ok && generated[name].stagedLeaf(data) {
			graph.files[name], graph.used[name] = &versionEditFile{}, true
			continue
		}
		file := &versionEditFile{reader: &view, name: name, data: view.files[name]}
		graph.files[name], graph.used[name] = file, true
		add := func(target string, kind int) {
			graph.used[target] = true
			if kind == packageReferenceXML && !queued[target] {
				queued[target] = true
				queue = append(queue, target)
			}
		}
		record := func(node *editorXML, attribute, target string) {
			file.links = append(file.links, versionEditLink{node: node, attribute: attribute, target: target})
		}
		if err := view.scanDocumentFileReferences(ctx, name, false, add, record, nil); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return graph, nil
}

// versionSharedFiles 汇总其他版本、主入口及其他文档依赖，保护共享条目不被覆盖
// 入参: ctx 取消上下文
// 返回: map[string]bool 受保护路径, error 读取或结构错误
func (r *Reader) versionSharedFiles(ctx context.Context) (map[string]bool, error) {
	shared, err := r.versionPreservedFiles(ctx)
	if err != nil {
		return nil, err
	}
	var indexes []int
	for index := range r.DocumentCount() {
		if index != r.documentIndex {
			indexes = append(indexes, index)
		}
	}
	files, err := r.documentFiles(ctx, indexes...)
	if err != nil {
		return nil, err
	}
	maps.Copy(shared, files)
	return shared, nil
}

// versionPreservedFiles 汇总当前文档封面、未选版本及原始入口依赖，不包含其他文档
// 入参: ctx 取消上下文
// 返回: map[string]bool 需要保留的文件, error 读取或取消错误
func (r *Reader) versionPreservedFiles(ctx context.Context) (map[string]bool, error) {
	shared := make(map[string]bool)
	data, err := r.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	body := root.childAt("DocBody", r.documentIndex)
	if info := body.child("DocInfo"); info != nil {
		if cover := info.child("Cover"); cover != nil {
			name, err := versionFileLocation("OFD.xml", editorImportText(data, cover))
			if err != nil {
				return nil, err
			}
			shared[name] = true
		}
	}
	if r.versionInfo != nil {
		if sigNode := body.child("Signatures"); sigNode != nil {
			sigList, err := versionFileLocation("OFD.xml", editorImportText(data, sigNode))
			if err == nil && sigList != "" {
				shared[sigList] = true
				listData, readErr := r.readFileViewContext(ctx, sigList)
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				var list Signatures
				if readErr == nil && xml.Unmarshal(listData, &list) == nil {
					for _, item := range list.List {
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						sigPath := cleanPackagePath(resolveResourcePath(sigList, "", item.BaseLoc))
						if sigPath == "" {
							continue
						}
						shared[sigPath] = true
						owned, protected, sigErr := r.signatureRemovalFiles(ctx, sigPath)
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						if sigErr == nil {
							maps.Copy(shared, owned)
							maps.Copy(shared, protected)
							delete(shared, "")
							delete(shared, ".")
						}
					}
				}
			}
		}
	}
	versions, err := r.DocumentVersionsContext(ctx)
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.versionInfo != nil && version.ID == r.versionInfo.ID {
			continue
		}
		shared[version.Location] = true
		for _, file := range version.Files {
			shared[file.Location] = true
		}
	}
	if r.versionInfo != nil && r.OFD.DocBody[r.documentIndex].DocRoot != "" {
		entry, err := versionFileLocation("OFD.xml", r.OFD.DocBody[r.documentIndex].DocRoot)
		if err != nil {
			return nil, err
		}
		files, err := r.versionFileDependencies(ctx, entry, nil, nil)
		if err != nil {
			return nil, err
		}
		maps.Copy(shared, files)
	}
	return shared, nil
}

// versionResourceFiles 分别记录当前入口依赖与完整扫描范围，其他文档和版本清单用于保护共享引用
// 入参: ctx 取消上下文, parts 待写入条目, generated 本次自产页面及引用
// 返回: map[string]bool 当前入口依赖, map[string]bool 扫描范围，非版本文档均为nil, error 读取或结构错误
func (r *Reader) versionResourceFiles(ctx context.Context, parts map[string][]byte, generated map[string]editorGeneratedReferences) (map[string]bool, map[string]bool, error) {
	if r == nil || !r.OFD.DocBody[r.documentIndex].versioned {
		return nil, nil, nil
	}
	entry, err := r.DocumentRootContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	files, err := r.versionFileDependencies(ctx, entry, parts, generated)
	if err != nil {
		return nil, nil, err
	}
	selected := maps.Clone(files)
	if r.versionInfo != nil {
		for _, file := range r.versionInfo.Files {
			files[file.Location] = true
		}
	}
	var indexes []int
	for index := range r.DocumentCount() {
		if index != r.documentIndex {
			indexes = append(indexes, index)
		}
	}
	other, err := r.documentFiles(ctx, indexes...)
	if err != nil {
		return nil, nil, err
	}
	maps.Copy(files, other)
	return selected, files, nil
}

// versionChanges 隔离当前文档或版本修改涉及的共享文件，同步版本清单，不修改输入包或修改集
// 入参: ctx 取消上下文, changes 待写入条目
// 返回: map[string][]byte 隔离后的修改集, error 读取、引用或结构错误
func (r *Reader) versionChanges(ctx context.Context, changes map[string][]byte) (map[string][]byte, error) {
	return r.versionOutputChanges(ctx, changes, nil, nil)
}

// versionFileUnchanged 分块比较原文件与待写入内容，读取失败时保留替换内容
// 入参: ctx 取消上下文, name 包内路径, data 待写入内容, buffer 复用缓冲区，比较ZIP条目时按需分配
// 返回: bool 内容相同, error 取消错误
func (r *Reader) versionFileUnchanged(ctx context.Context, name string, data []byte, buffer *[]byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	name = cleanPackagePath(name)
	if original, ok := r.files[name]; ok {
		if len(original) != len(data) {
			return false, nil
		}
		for offset := 0; offset < len(data); {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			end := offset + min(32*1024, len(data)-offset)
			if !bytes.Equal(original[offset:end], data[offset:end]) {
				return false, nil
			}
			offset = end
		}
		return true, ctx.Err()
	}
	var input io.ReadCloser
	if file, ok := r.packageFile(name); ok {
		if file.UncompressedSize64 != uint64(len(data)) {
			return false, nil
		}
		var err error
		input, err = file.Open()
		if err != nil {
			return false, nil
		}
	} else if src := r.encryptedSource(); src != nil && src.has(name) {
		var err error
		input, err = src.open(name)
		if err != nil {
			return false, nil
		}
	} else {
		return false, nil
	}
	defer input.Close()
	if len(*buffer) == 0 {
		*buffer = make([]byte, 32*1024)
	}
	reader := imageInput{ReadCloser: input, ctx: ctx}
	for len(data) != 0 {
		block := (*buffer)[:min(len(*buffer), len(data))]
		if _, err := io.ReadFull(&reader, block); err != nil {
			return false, ctx.Err()
		}
		if !bytes.Equal(block, data[:len(block)]) {
			return false, nil
		}
		data = data[len(block):]
	}
	n, err := reader.Read((*buffer)[:1])
	return n == 0 && err == io.EOF, ctx.Err()
}

// versionOutputChanges 隔离文档或版本改动并同步删除清单，失败时不修改调用方清单
// 入参: ctx 取消上下文, changes 待写入条目, removed 待删除条目，返回前移除受保护路径, generated 本次自产页面及引用，随隔离路径更新
// 返回: map[string][]byte 隔离后的修改集, error 读取、引用或结构错误
func (r *Reader) versionOutputChanges(ctx context.Context, changes map[string][]byte, removed map[string]bool, generated map[string]editorGeneratedReferences) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, err := r.DocumentRootContext(ctx)
	if err != nil {
		return nil, err
	}
	if r.DocumentCount() == 1 && !r.OFD.DocBody[r.documentIndex].versioned || len(changes) == 0 && len(removed) == 0 {
		return changes, ctx.Err()
	}
	parts := maps.Clone(changes)
	if parts == nil {
		parts = make(map[string][]byte)
	}
	var buffer []byte
	for name, data := range parts {
		if generated[name].stagedLeaf(data) {
			continue
		}
		same, err := r.versionFileUnchanged(ctx, name, data, &buffer)
		if err != nil {
			return nil, err
		}
		if same {
			delete(parts, name)
		}
	}
	if len(parts) == 0 && len(removed) == 0 {
		return parts, ctx.Err()
	}
	preserved, err := r.versionPreservedFiles(ctx)
	if err != nil {
		return nil, err
	}
	var otherIndexes []int
	for index := range r.DocumentCount() {
		if index != r.documentIndex {
			otherIndexes = append(otherIndexes, index)
		}
	}
	shared := maps.Clone(preserved)
	if len(otherIndexes) != 0 {
		otherFiles, err := r.documentFiles(ctx, otherIndexes...)
		if err != nil {
			return nil, err
		}
		maps.Copy(shared, otherFiles)
	}
	if r.versionInfo == nil && len(removed) == 0 {
		changed := false
		for name := range parts {
			if name != "OFD.xml" && shared[name] {
				changed = true
				break
			}
		}
		if !changed {
			return parts, ctx.Err()
		}
	}
	graph, err := r.versionEditGraph(ctx, entry, parts, generated)
	if err != nil {
		return nil, err
	}
	outputRemoved := removed
	removed = maps.Clone(removed)
	for name := range removed {
		if graph.used[name] {
			delete(removed, name)
		} else {
			delete(parts, name)
		}
	}
	owned := maps.Clone(graph.used)
	if r.versionInfo != nil {
		for _, file := range r.versionInfo.Files {
			owned[file.Location] = true
		}
	}
	paths := make(map[string]string)
	reserved := maps.Clone(parts)
	var pending []string
	allocate := func(name string) {
		target := packageAvailableName(r, reserved, name)
		paths[name], reserved[target] = target, nil
		pending = append(pending, name)
	}
	for _, name := range slices.Sorted(maps.Keys(parts)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name != "OFD.xml" && shared[name] && owned[name] {
			allocate(name)
		}
	}
	parents := make(map[string][]string)
	for _, name := range slices.Sorted(maps.Keys(graph.files)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, link := range graph.files[name].links {
			parents[link.target] = append(parents[link.target], name)
		}
	}
	for i := 0; i < len(pending); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, name := range parents[pending[i]] {
			if shared[name] && paths[name] == "" {
				allocate(name)
			}
		}
	}
	for name, file := range graph.files {
		data, err := file.rewrite(ctx, paths)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(data, file.data) {
			parts[name] = data
		}
	}
	for source, target := range paths {
		data, ok := parts[source]
		if !ok {
			data, err = r.readFileViewContext(ctx, source)
			if err != nil {
				return nil, err
			}
		}
		parts[target] = data
		delete(parts, source)
	}
	if r.versionInfo != nil {
		if err := r.versionManifest(ctx, parts, paths, graph.used, shared, removed); err != nil {
			return nil, err
		}
	} else if target := paths[entry]; target != "" {
		data, ok := parts["OFD.xml"]
		if !ok {
			data, err = r.readFileViewContext(ctx, "OFD.xml")
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		node := root.childAt("DocBody", r.documentIndex).child("DocRoot")
		parts["OFD.xml"] = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, editorFontXMLText("/"+target))})
	}
	retainedShared := shared
	if len(removed) != 0 && len(otherIndexes) != 0 && len(parts) != 0 {
		afterView := *r
		afterView.files = maps.Clone(r.files)
		if afterView.files == nil {
			afterView.files = make(map[string][]byte, len(parts))
		}
		for name, data := range parts {
			if data != nil {
				afterView.files[name] = data
			}
		}
		afterFiles, err := afterView.documentFiles(ctx, otherIndexes...)
		if err != nil {
			return nil, err
		}
		retainedShared = maps.Clone(preserved)
		maps.Copy(retainedShared, afterFiles)
	}
	for name := range removed {
		if retainedShared[name] {
			delete(removed, name)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for name := range outputRemoved {
		if _, ok := removed[name]; !ok {
			delete(outputRemoved, name)
		}
	}
	for source, target := range paths {
		if known := generated[source]; known.stagedLeaf(parts[target]) {
			generated[target] = known
			delete(generated, source)
		}
	}
	return parts, nil
}

// versionManifest 更新所选版本的入口与文件清单，保留已有文件标识及扩展字段
// 入参: ctx 取消上下文, parts 输出条目, paths 隔离路径, used 当前内容依赖, shared 其他入口依赖, removed 当前版本移除的文件
// 返回: error 描述读取或XML编码错误
func (r *Reader) versionManifest(ctx context.Context, parts map[string][]byte, paths map[string]string, used, shared, removed map[string]bool) error {
	version := r.versionInfo
	data, err := r.readFileViewContext(ctx, version.Location)
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	list := root.child("FileList")
	var patches []editorXMLPatch
	listed := make(map[string]bool)
	ids := map[string]bool{version.ID: true}
	for _, node := range list.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !node.matchesOFD("File") {
			continue
		}
		ids[strings.TrimSpace(node.attr("ID"))] = true
		name, err := versionFileLocation(version.Location, editorImportText(data, node))
		if err != nil {
			return err
		}
		if removed[name] {
			patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
			continue
		}
		if target := paths[name]; target != "" {
			name = target
			patches = append(patches, editorXMLContent(data, node, editorFontXMLText("/"+target)))
		}
		listed[name] = true
	}
	var added []byte
	serial := 0
	for _, name := range slices.Sorted(maps.Keys(used)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if target := paths[name]; target != "" {
			name = target
		}
		if listed[name] {
			continue
		}
		id := ""
		for id == "" || ids[id] {
			serial++
			id = "file" + strconv.Itoa(serial)
		}
		ids[id], listed[name] = true, true
		item, err := list.containerXML("File", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}}, editorFontXMLText("/"+name))
		if err != nil {
			return err
		}
		added = append(added, bytes.TrimPrefix(item, []byte(xml.Header))...)
	}
	if len(added) != 0 {
		patches = append(patches, editorXMLPatch{list.close, list.close, added})
	}
	if target := paths[version.DocRoot]; target != "" {
		patches = append(patches, editorXMLContent(data, root.child("DocRoot"), editorFontXMLText("/"+target)))
	}
	updated := editorPatchXML(data, patches)
	if bytes.Equal(updated, data) {
		return nil
	}
	name := version.Location
	if shared[name] {
		name = packageAvailableName(r, parts, name)
		entry, ok := parts["OFD.xml"]
		if !ok {
			entry, err = r.readFileViewContext(ctx, "OFD.xml")
			if err != nil {
				return err
			}
		}
		root, err := parseEditorXMLContext(ctx, entry)
		if err != nil {
			return err
		}
		for _, node := range root.childAt("DocBody", r.documentIndex).child("Versions").children {
			if node.matchesOFD("Version") && node.attr("ID") == version.ID {
				fragment, err := editorXMLAttribute(entry, node, "BaseLoc", "/"+name)
				if err != nil {
					return err
				}
				parts["OFD.xml"] = editorPatchXML(entry, []editorXMLPatch{{node.start, node.end, fragment}})
				break
			}
		}
	}
	parts[name] = updated
	return nil
}
