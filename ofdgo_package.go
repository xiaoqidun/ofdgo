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
	"path"
	"slices"
	"strconv"
	"strings"
)

// packageDirectory 获取当前文档的包内根目录，新建文档与编辑文档共用布局
// 返回: string 文档目录
func (e *Editor) packageDirectory() string {
	if e.source != nil {
		return e.source.directory
	}
	return "Doc_0"
}

// packagePagePath 按稳定页面标识生成路径，页面重排不改变文件名
// 入参: directory 文档目录, id 页面标识
// 返回: string 页面路径
func packagePagePath(directory, id string) string {
	return path.Join(directory, "Pages", "Page_"+id, "Content.xml")
}

// packageOFDNode 判断包索引中的标准节点，兼容旧版及省略命名空间的文档
// 入参: node XML节点, name 节点名称
// 返回: bool 是否为标准节点
func packageOFDNode(node *editorXML, name string) bool {
	return node.name.Local == name && (node.name.Space == "" || node.name.Space == ofdNamespace || node.name.Space == "http://www.ofdspec.org")
}

// packageName 为新增条目分配文档内路径，不覆盖原文件或本次资源
// 入参: relative 文档相对路径
// 返回: string 未占用路径
func (e *Editor) packageName(relative string) string {
	var reader *Reader
	if e.source != nil {
		reader = e.source.reader
	}
	files := make(map[string][]byte, len(e.resources))
	for _, resource := range e.resources {
		files[resource.name] = nil
	}
	return packageAvailableName(reader, files, path.Join(e.packageDirectory(), relative))
}

// packageAvailableName 仅在包内同名条目冲突时添加序号，忽略大小写差异
// 入参: reader 原包，可为nil, files 待写入条目, name 期望路径
// 返回: string 未占用路径
func packageAvailableName(reader *Reader, files map[string][]byte, name string) string {
	base, extension := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	for i := 0; ; i++ {
		candidate := name
		if i != 0 {
			candidate = base + "_" + strconv.Itoa(i) + extension
		}
		occupied := false
		if reader != nil {
			_, occupied = reader.packageFile(candidate)
			for file := range reader.files {
				occupied = occupied || strings.EqualFold(cleanPackagePath(file), candidate)
			}
		}
		for file := range files {
			occupied = occupied || strings.EqualFold(cleanPackagePath(file), candidate)
		}
		if !occupied {
			return candidate
		}
	}
}

// mergePackageResources 合并新增资源定义，保留原索引、路径基准与未知字段
// 入参: reader 原包, parts 待写入条目, doc 文档, directory 文档目录, added 新增资源XML，文件引用使用绝对包路径
// 返回: string 资源索引路径, bool 是否需要新增文档引用, error 错误信息
func mergePackageResources(reader *Reader, parts map[string][]byte, doc *Document, directory string, added []byte) (string, bool, error) {
	if len(doc.CommonData.DocumentRes) == 0 {
		name := packageAvailableName(reader, parts, path.Join(directory, "DocumentRes.xml"))
		parts[name] = added
		return name, true, nil
	}
	name := reader.ResPath(doc.CommonData.DocumentRes[0])
	if file, ok := reader.packageFile(name); ok {
		name = cleanPackagePath(file.Name)
	}
	data, ok := parts[name]
	if !ok {
		var err error
		data, err = reader.readFile(name)
		if err != nil {
			return "", false, err
		}
	}
	addition, err := parseEditorXML(added)
	if err != nil {
		return "", false, err
	}
	order := []string{"ColorSpaces", "DrawParams", "Fonts", "MultiMedias", "CompositeGraphicUnits"}
	for _, group := range addition.children {
		root, err := parseEditorXML(data)
		if err != nil {
			return "", false, err
		}
		target := root.child(group.name.Local)
		var content []byte
		for _, node := range group.children {
			if target != nil && slices.ContainsFunc(target.children, func(old *editorXML) bool {
				return packageOFDNode(old, node.name.Local) && old.attr("ID") == node.attr("ID")
			}) {
				continue
			}
			entry, err := editorXMLStandalone(added[node.start:node.end], node)
			if err != nil {
				return "", false, err
			}
			content = append(content, entry...)
		}
		if len(content) == 0 {
			continue
		}
		if target != nil {
			content = append(bytes.Clone(data[target.open:target.close]), content...)
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, target, content)})
			continue
		}
		content, err = editorXMLContainer(group.name.Local, nil, content)
		if err != nil {
			return "", false, err
		}
		position := root.close
		for _, child := range root.children {
			if packageOFDNode(child, child.name.Local) && slices.Index(order, child.name.Local) > slices.Index(order, group.name.Local) {
				position = child.start
				break
			}
		}
		if root.open == root.end {
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, content)})
		} else {
			data = editorPatchXML(data, []editorXMLPatch{{position, position, content}})
		}
	}
	parts[name] = data
	return name, false, nil
}
