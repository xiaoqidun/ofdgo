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
	"strconv"
	"strings"
)

// versionEditFile 保存版本内容XML及标准文件引用，不解析字体、图片或附件数据
type versionEditFile struct {
	data  []byte
	links []versionEditLink
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

// versionEditGraph 沿标准文件引用建立版本依赖图，二进制资源只记录路径
// 入参: ctx 取消上下文, entry 文档入口, parts 待修改条目
// 返回: *versionEditGraph 依赖图, error 读取或结构错误
func (r *Reader) versionEditGraph(ctx context.Context, entry string, parts map[string][]byte) (*versionEditGraph, error) {
	graph := &versionEditGraph{files: make(map[string]*versionEditFile), used: make(map[string]bool)}
	queue := []string{entry}
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := queue[0]
		queue = queue[1:]
		if graph.files[name] != nil {
			continue
		}
		data, ok := parts[name]
		if !ok {
			var err error
			data, err = r.readFile(name)
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, fmt.Errorf("version file %s: %w", name, err)
		}
		file := &versionEditFile{data: data}
		graph.files[name], graph.used[name] = file, true
		base := ""
		if root.name.Local == "Res" {
			base = root.attr("BaseLoc")
		}
		var visit func(*editorXML)
		visit = func(node *editorXML) {
			if node != root && !node.matchesOFD(node.name.Local) {
				return
			}
			parent := ""
			if node.parent != nil {
				parent = node.parent.name.Local
			}
			if parent == "Extension" && node.name.Local == "Data" {
				return
			}
			add := func(attribute, value string, kind int) {
				if strings.TrimSpace(value) == "" {
					return
				}
				target := resolveResourcePath(name, base, value)
				file.links = append(file.links, versionEditLink{node: node, attribute: attribute, target: target})
				graph.used[target] = true
				if kind == packageReferenceXML {
					queue = append(queue, target)
				}
			}
			if kind := packageReference(root.name.Local, parent, node.name.Local, ""); kind != packageReferenceNone {
				add("", editorImportText(data, node), kind)
			}
			for _, attr := range node.attrs {
				if attr.Name.Space != "" {
					continue
				}
				if kind := packageReference(root.name.Local, parent, node.name.Local, attr.Name.Local); kind != packageReferenceNone {
					add(attr.Name.Local, attr.Value, kind)
				}
			}
			for _, child := range node.children {
				visit(child)
			}
		}
		visit(root)
	}
	return graph, nil
}

// versionSharedFiles 汇总其他版本、主入口及其他文档依赖，保护共享条目不被覆盖
// 入参: ctx 取消上下文
// 返回: map[string]bool 受保护路径, error 读取或结构错误
func (r *Reader) versionSharedFiles(ctx context.Context) (map[string]bool, error) {
	shared := make(map[string]bool)
	data, err := r.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
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
	for index := range r.DocumentCount() {
		if index == r.documentIndex {
			continue
		}
		files, err := r.documentFiles(ctx, index)
		if err != nil {
			return nil, err
		}
		maps.Copy(shared, files)
	}
	versions, err := r.DocumentVersions()
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		if r.versionInfo != nil && version.ID == r.versionInfo.ID {
			continue
		}
		shared[version.Location] = true
		for _, file := range version.Files {
			shared[file.Location] = true
		}
	}
	if r.versionInfo != nil {
		entry, err := versionFileLocation("OFD.xml", r.OFD.DocBody[r.documentIndex].DocRoot)
		if err != nil {
			return nil, err
		}
		graph, err := r.versionEditGraph(ctx, entry, nil)
		if err != nil {
			return nil, err
		}
		maps.Copy(shared, graph.used)
	}
	return shared, nil
}

// versionResourceFiles 限定当前版本的资源扫描范围，合并其他文档依赖以保护跨文档引用
// 入参: ctx 取消上下文, parts 待写入条目
// 返回: map[string]bool 扫描路径，非版本文档返回nil, error 读取或结构错误
func (r *Reader) versionResourceFiles(ctx context.Context, parts map[string][]byte) (map[string]bool, error) {
	if r == nil || !r.OFD.DocBody[r.documentIndex].versioned {
		return nil, nil
	}
	entry, err := r.DocumentRoot()
	if err != nil {
		return nil, err
	}
	graph, err := r.versionEditGraph(ctx, entry, parts)
	if err != nil {
		return nil, err
	}
	if r.versionInfo != nil {
		for _, file := range r.versionInfo.Files {
			graph.used[file.Location] = true
		}
	}
	for index := range r.DocumentCount() {
		if index == r.documentIndex {
			continue
		}
		files, err := r.documentFiles(ctx, index)
		if err != nil {
			return nil, err
		}
		maps.Copy(graph.used, files)
	}
	return graph.used, nil
}

// versionChanges 隔离当前入口修改涉及的共享文件，同步版本清单，不修改输入包或修改集
// 入参: ctx 取消上下文, changes 待写入条目
// 返回: map[string][]byte 隔离后的修改集, error 读取、引用或结构错误
func (r *Reader) versionChanges(ctx context.Context, changes map[string][]byte) (map[string][]byte, error) {
	return r.versionOutputChanges(ctx, changes, nil)
}

// versionOutputChanges 隔离版本改动并同步删除清单，保留其他入口仍需使用的文件
// 入参: ctx 取消上下文, changes 待写入条目, removed 待删除条目，返回前移除受保护路径
// 返回: map[string][]byte 隔离后的修改集, error 读取、引用或结构错误
func (r *Reader) versionOutputChanges(ctx context.Context, changes map[string][]byte, removed map[string]bool) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, err := r.DocumentRoot()
	if err != nil {
		return nil, err
	}
	if !r.OFD.DocBody[r.documentIndex].versioned || len(changes) == 0 && len(removed) == 0 {
		return changes, ctx.Err()
	}
	parts := maps.Clone(changes)
	if parts == nil {
		parts = make(map[string][]byte)
	}
	for name, data := range parts {
		if original, err := r.readFileView(name); err == nil && bytes.Equal(original, data) {
			delete(parts, name)
		}
	}
	if len(parts) == 0 && len(removed) == 0 {
		return parts, ctx.Err()
	}
	shared, err := r.versionSharedFiles(ctx)
	if err != nil {
		return nil, err
	}
	graph, err := r.versionEditGraph(ctx, entry, parts)
	if err != nil {
		return nil, err
	}
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
		extension := path.Ext(name)
		target := packageAvailableName(r, reserved, strings.TrimSuffix(name, extension)+".edit"+extension)
		paths[name], reserved[target] = target, nil
		pending = append(pending, name)
	}
	for _, name := range slices.Sorted(maps.Keys(parts)) {
		if name != "OFD.xml" && shared[name] && owned[name] {
			allocate(name)
		}
	}
	parents := make(map[string][]string)
	for _, name := range slices.Sorted(maps.Keys(graph.files)) {
		for _, link := range graph.files[name].links {
			parents[link.target] = append(parents[link.target], name)
		}
	}
	for i := 0; i < len(pending); i++ {
		for _, name := range parents[pending[i]] {
			if shared[name] && paths[name] == "" {
				allocate(name)
			}
		}
	}
	for name, file := range graph.files {
		data, err := file.rewrite(paths)
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
			data, err = r.readFile(source)
			if err != nil {
				return nil, err
			}
		}
		parts[target] = data
		delete(parts, source)
	}
	if r.versionInfo != nil {
		if err := r.versionManifest(parts, paths, graph.used, shared, removed); err != nil {
			return nil, err
		}
	} else if target := paths[entry]; target != "" {
		data, ok := parts["OFD.xml"]
		if !ok {
			data, err = r.readFile("OFD.xml")
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		node := root.childAt("DocBody", r.documentIndex).child("DocRoot")
		parts["OFD.xml"] = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, editorFontXMLText("/"+target))})
	}
	for name := range removed {
		if shared[name] {
			delete(removed, name)
		}
	}
	return parts, ctx.Err()
}

// rewrite 更新已隔离文件的引用，保留其余XML及相对路径基准
// 入参: paths 原路径到新路径的映射
// 返回: []byte 更新后的XML, error 属性编码错误
func (file *versionEditFile) rewrite(paths map[string]string) ([]byte, error) {
	var patches []editorXMLPatch
	headers := make(map[*editorXML][]versionEditLink)
	for _, link := range file.links {
		if paths[link.target] == "" {
			continue
		}
		if link.attribute == "" {
			patches = append(patches, editorXMLContent(file.data, link.node, editorFontXMLText("/"+paths[link.target])))
		} else {
			headers[link.node] = append(headers[link.node], link)
		}
	}
	for node, links := range headers {
		data := file.data[node.start:node.end]
		for _, link := range links {
			root, err := parseEditorXML(data)
			if err != nil {
				return nil, err
			}
			data, err = editorXMLAttribute(data, root, link.attribute, "/"+paths[link.target])
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		if node.open == node.end {
			patches = append(patches, editorXMLPatch{node.start, node.end, data})
		} else {
			patches = append(patches, editorXMLPatch{node.start, node.open, data[:root.open]})
		}
	}
	return editorPatchXML(file.data, patches), nil
}

// versionManifest 更新所选版本的入口与文件清单，保留已有文件标识及扩展字段
// 入参: parts 输出条目, paths 隔离路径, used 当前内容依赖, shared 其他入口依赖, removed 当前版本移除的文件
// 返回: error 描述读取或XML编码错误
func (r *Reader) versionManifest(parts map[string][]byte, paths map[string]string, used, shared, removed map[string]bool) error {
	version := r.versionInfo
	data, err := r.readFile(version.Location)
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	list := root.child("FileList")
	var patches []editorXMLPatch
	listed := make(map[string]bool)
	ids := map[string]bool{version.ID: true}
	for _, node := range list.children {
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
		extension := path.Ext(name)
		name = packageAvailableName(r, parts, strings.TrimSuffix(name, extension)+".edit"+extension)
		entry, ok := parts["OFD.xml"]
		if !ok {
			entry, err = r.readFile("OFD.xml")
			if err != nil {
				return err
			}
		}
		root, err := parseEditorXML(entry)
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
