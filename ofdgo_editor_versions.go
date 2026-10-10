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
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
)

// DocumentVersions 获取当前编辑文档的版本信息，包含尚未写出的修改
// 返回: []DocumentVersionInfo 版本信息, error 读取错误
func (e *Editor) DocumentVersions() ([]DocumentVersionInfo, error) {
	if e.source == nil {
		return nil, nil
	}
	reader, err := e.Reader()
	if err != nil {
		return nil, err
	}
	return reader.DocumentVersions()
}

// DocumentVersionID 获取当前编辑版本的标识，主入口返回空字符串
// 返回: string 版本标识, error 读取错误
func (e *Editor) DocumentVersionID() (string, error) {
	if e.source == nil {
		return "", nil
	}
	return e.source.reader.DocumentVersionID()
}

// SelectVersion 切换当前文档的编辑版本，保留各版本修改，不改变默认版本
// 切换及此前编辑均可通过Undo撤销；空标识选择主入口
// 入参: id 版本标识
// 返回: error 切换错误
func (e *Editor) SelectVersion(id string) error {
	current, err := e.DocumentVersionID()
	if err != nil || current == id {
		return err
	}
	reader, err := e.Reader()
	if err != nil {
		return err
	}
	view, err := reader.DocumentVersion(id)
	if err != nil {
		return err
	}
	before := e.transactionSnapshot()
	if err := e.adoptDocumentReader(view); err != nil {
		return err
	}
	e.recordTransaction(before)
	return nil
}

// AddVersion 以当前编辑内容创建版本并选中，设为默认版本，可通过Undo撤销
// 初始共享文件，后续修改按需隔离；不自动写入创建日期，修改入口可能使已有签名失效
// 入参: name 版本名称，可为空
// 返回: string 新版本标识, error 创建错误
func (e *Editor) AddVersion(name string) (string, error) {
	var id string
	err := e.editDocuments(func(reader *Reader, data []byte, root *editorXML) (int, error) {
		versions, err := reader.documentVersionEntries()
		if err != nil {
			return 0, err
		}
		index := int32(0)
		for _, version := range versions {
			index = max(index, version.Index)
		}
		if index == math.MaxInt32 {
			return 0, fmt.Errorf("document version index exhausted")
		}
		index++
		ids := make(map[string]bool)
		var collect func(*editorXML)
		collect = func(node *editorXML) {
			ids[node.attr("ID")] = true
			for _, child := range node.children {
				collect(child)
			}
		}
		collect(root)
		for serial := int64(index); ; serial++ {
			id = "v" + strconv.FormatInt(serial, 10)
			if !ids[id] {
				break
			}
		}
		entry, err := reader.DocumentRoot()
		if err != nil {
			return 0, err
		}
		files, err := reader.versionSnapshotFiles(context.Background())
		if err != nil {
			return 0, err
		}
		body := root.childAt("DocBody", reader.documentIndex)
		location := packageAvailableName(reader, nil, path.Join(path.Dir(entry), "Versions", id+".xml"))
		attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}}
		format := reader.Version()
		if reader.versionInfo != nil && reader.versionInfo.Version != "" {
			format = reader.versionInfo.Version
		}
		attrs.add("Version", format)
		if name != "" {
			attrs.add("Name", name)
		}
		descriptor, err := encodeOFDXML(func(x *ofdXML) {
			x.root("DocVersion", attrs)
			x.start("FileList", nil)
			for serial, file := range slices.Sorted(maps.Keys(files)) {
				x.start("File", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: "file" + strconv.Itoa(serial+1)}})
				x.token(xml.CharData("/" + file))
				x.end("File")
			}
			x.end("FileList")
			x.text("DocRoot", "/"+entry)
			x.end("DocVersion")
		})
		if err != nil {
			return 0, err
		}
		descriptor, err = editorXMLGenerated(descriptor, body.name.Space)
		if err != nil {
			return 0, err
		}
		version, err := body.containerXML("Version", ofdAttrs{
			{Name: xml.Name{Local: "ID"}, Value: id},
			{Name: xml.Name{Local: "Index"}, Value: strconv.FormatInt(int64(index), 10)},
			{Name: xml.Name{Local: "Current"}, Value: "true"},
			{Name: xml.Name{Local: "BaseLoc"}, Value: "/" + location},
		}, nil)
		if err != nil {
			return 0, err
		}
		data, err = editorVersionDefault(data, body, "")
		if err != nil {
			return 0, err
		}
		root, err = parseEditorXML(data)
		if err != nil {
			return 0, err
		}
		body = root.childAt("DocBody", reader.documentIndex)
		var patch editorXMLPatch
		if list := body.child("Versions"); list != nil {
			patch = editorXMLPatch{list.close, list.close, version}
		} else {
			list, err := body.containerXML("Versions", nil, version)
			if err != nil {
				return 0, err
			}
			position := body.child("DocRoot").end
			patch = editorXMLPatch{position, position, list}
		}
		reader.files[location] = descriptor
		reader.files["OFD.xml"] = editorPatchXML(data, []editorXMLPatch{patch})
		reader.selectedVersion = &id
		return reader.documentIndex, nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// SetDefaultVersion 设置下次打开使用的版本，不切换当前编辑入口，可通过Undo撤销
// 空标识取消默认版本并使用主入口；修改OFD.xml可能使已有签名失效
// 入参: id 版本标识
// 返回: error 设置错误
func (e *Editor) SetDefaultVersion(id string) error {
	versions, err := e.DocumentVersions()
	if err != nil {
		return err
	}
	found, changed := id == "", false
	for _, version := range versions {
		found = found || version.ID == id
		changed = changed || version.Current != (version.ID == id)
	}
	if !found {
		return fmt.Errorf("document version not found: %s", id)
	}
	if !changed {
		return nil
	}
	return e.editDocuments(func(reader *Reader, data []byte, root *editorXML) (int, error) {
		current, err := reader.DocumentVersionID()
		if err != nil {
			return 0, err
		}
		updated, err := editorVersionDefault(data, root.childAt("DocBody", reader.documentIndex), id)
		if err != nil {
			return 0, err
		}
		reader.files["OFD.xml"], reader.selectedVersion = updated, &current
		return reader.documentIndex, nil
	})
}

// RenameVersion 修改版本名称，保留其他属性及扩展内容，可通过Undo撤销
// 描述文件被共享时不覆盖原文件；修改入口可能使已有签名失效
// 入参: id 版本标识, name 版本名称，空值清空名称
// 返回: error 修改错误
func (e *Editor) RenameVersion(id, name string) error {
	versions, err := e.DocumentVersions()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(versions, func(version DocumentVersionInfo) bool { return version.ID == id })
	if index < 0 {
		return fmt.Errorf("document version not found: %s", id)
	}
	if versions[index].Name == name {
		return nil
	}
	return e.editDocuments(func(reader *Reader, data []byte, root *editorXML) (int, error) {
		version := versions[index]
		original, err := reader.readFile(version.Location)
		if err != nil {
			return 0, err
		}
		descriptor, err := parseEditorXML(original)
		if err != nil {
			return 0, err
		}
		updated, err := editorXMLAttribute(original, descriptor, "Name", name)
		if err != nil {
			return 0, err
		}
		extension := path.Ext(version.Location)
		location := packageAvailableName(reader, nil, strings.TrimSuffix(version.Location, extension)+".edit"+extension)
		body := root.childAt("DocBody", reader.documentIndex)
		for _, node := range body.child("Versions").children {
			if node.matchesOFD("Version") && node.attr("ID") == id {
				fragment, err := editorXMLAttribute(data, node, "BaseLoc", "/"+location)
				if err != nil {
					return 0, err
				}
				reader.files["OFD.xml"] = editorPatchXML(data, []editorXMLPatch{{node.start, node.end, fragment}})
				break
			}
		}
		reader.files[location] = updated
		if err := reader.retireVersionFiles(map[string]bool{version.Location: true}); err != nil {
			return 0, err
		}
		return reader.documentIndex, nil
	})
}

// DeleteVersion 删除版本及其独占文件，可通过Undo撤销，不删除文档主入口
// 删除当前版本后转到剩余默认版本或主入口；修改入口可能使已有签名失效
// 入参: id 版本标识，不能为空
// 返回: error 删除错误
func (e *Editor) DeleteVersion(id string) error {
	return e.editDocuments(func(reader *Reader, data []byte, root *editorXML) (int, error) {
		versions, err := reader.DocumentVersions()
		if err != nil {
			return 0, err
		}
		index := slices.IndexFunc(versions, func(version DocumentVersionInfo) bool { return version.ID == id })
		if index < 0 {
			return 0, fmt.Errorf("document version not found: %s", id)
		}
		current, err := reader.DocumentVersionID()
		if err != nil {
			return 0, err
		}
		list := root.childAt("DocBody", reader.documentIndex).child("Versions")
		for _, node := range list.children {
			if node.matchesOFD("Version") && node.attr("ID") == id {
				if len(versions) == 1 {
					node = list
				}
				reader.files["OFD.xml"] = editorPatchXML(data, []editorXMLPatch{{start: node.start, end: node.end}})
				break
			}
		}
		if current == id {
			reader.selectedVersion = nil
		}
		removed := map[string]bool{versions[index].Location: true}
		for _, file := range versions[index].Files {
			removed[file.Location] = true
		}
		if err := reader.retireVersionFiles(removed); err != nil {
			return 0, err
		}
		return reader.documentIndex, nil
	})
}

// editorVersionDefault 修改版本入口的Current属性，不改变条目顺序或其他属性
// 入参: data OFD入口原文, body 文档节点, id 默认版本，空值取消默认版本
// 返回: []byte 更新后的入口, error 属性编码错误
func editorVersionDefault(data []byte, body *editorXML, id string) ([]byte, error) {
	list := body.child("Versions")
	if list == nil {
		return data, nil
	}
	var patches []editorXMLPatch
	for _, node := range list.children {
		if !node.matchesOFD("Version") {
			continue
		}
		current := node.attr("ID") == id
		if !current && !node.hasAttr("Current") {
			continue
		}
		fragment, err := editorXMLAttribute(data, node, "Current", strconv.FormatBool(current))
		if err != nil {
			return nil, err
		}
		patches = append(patches, editorXMLPatch{node.start, node.end, fragment})
	}
	return editorPatchXML(data, patches), nil
}

// versionSnapshotFiles 汇总当前入口及其版本清单，补入适用的封面和签章文件
// 入参: ctx 取消上下文
// 返回: map[string]bool 快照文件, error 引用或读取错误
func (r *Reader) versionSnapshotFiles(ctx context.Context) (map[string]bool, error) {
	entry, err := r.DocumentRoot()
	if err != nil {
		return nil, err
	}
	graph, err := r.versionEditGraph(ctx, entry, nil)
	if err != nil {
		return nil, err
	}
	if r.versionInfo != nil {
		for _, file := range r.versionInfo.Files {
			graph.used[file.Location] = true
		}
	}
	body := r.OFD.DocBody[r.documentIndex]
	data, err := r.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	if info := root.childAt("DocBody", r.documentIndex).child("DocInfo"); info != nil {
		if cover := info.child("Cover"); cover != nil {
			name, err := versionFileLocation("OFD.xml", editorImportText(data, cover))
			if err != nil {
				return nil, err
			}
			graph.used[name] = true
		}
	}
	if signature := r.versionSignatures(body.Signatures); signature != "" {
		files, err := r.versionEditGraph(ctx, cleanPackagePath(signature), nil)
		if err != nil {
			return nil, err
		}
		maps.Copy(graph.used, files.used)
	}
	for name := range graph.used {
		if !r.fileNames[name] {
			return nil, fmt.Errorf("version file not found: %s", name)
		}
	}
	return graph.used, ctx.Err()
}

// retireVersionFiles 清理修改入口后不再被任何文档或版本引用的候选文件
// 入参: candidates 待清理路径，OFD.xml始终保留
// 返回: error 依赖读取错误
func (r *Reader) retireVersionFiles(candidates map[string]bool) error {
	view := *r
	if err := view.initRoot(); err != nil {
		return err
	}
	for index := range view.DocumentCount() {
		files, err := view.documentFiles(context.Background(), index)
		if err != nil {
			return err
		}
		for name := range files {
			delete(candidates, name)
		}
	}
	delete(candidates, "OFD.xml")
	for name := range candidates {
		delete(r.files, name)
	}
	if r.Zip != nil {
		r.Zip = &zip.Reader{Comment: r.Zip.Comment, File: slices.DeleteFunc(slices.Clone(r.Zip.File), func(file *zip.File) bool {
			return candidates[cleanPackagePath(file.Name)]
		})}
	}
	return nil
}
