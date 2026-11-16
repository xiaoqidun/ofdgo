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
	"io"
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

// packageName 为新增条目分配文档内路径，不覆盖原文件或本次资源
// 无暂存资源冲突时直接返回路径，避免重复构建资源映射
// 入参: relative 文档相对路径
// 返回: string 未占用路径
func (e *Editor) packageName(relative string) string {
	var reader *Reader
	if e.source != nil {
		reader = e.source.reader
	}
	name := path.Join(e.packageDirectory(), relative)
	candidate := packageAvailableName(reader, nil, name)
	if !slices.ContainsFunc(e.resources, func(resource editorResource) bool { return resource.name == candidate }) {
		return candidate
	}
	files := make(map[string][]byte, len(e.resources))
	for _, resource := range e.resources {
		files[resource.name] = nil
	}
	return packageAvailableName(reader, files, name)
}

// packageAvailableName 仅在包内同名条目冲突时添加序号，路径区分大小写
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
			_, cached := reader.files[candidate]
			occupied = occupied || cached
		}
		_, pending := files[candidate]
		occupied = occupied || pending
		if !occupied {
			return candidate
		}
	}
}

// packageDataEqual 流式比较包内文件与给定数据，文件缺失时返回false
// 入参: ctx 取消上下文, reader 原包, name 包内路径, data 待比较内容
// 返回: bool 内容是否相同, error 读取或取消错误
func packageDataEqual(ctx context.Context, reader *Reader, name string, data []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	name = cleanPackagePath(name)
	if current, ok := reader.files[name]; ok {
		return bytes.Equal(current, data), ctx.Err()
	}
	if _, ok := reader.packageFile(name); !ok {
		return false, nil
	}
	stream, err := reader.openFile(name)
	if err != nil {
		return false, err
	}
	defer stream.Close()
	input := imageInput{ReadCloser: stream, ctx: ctx}
	var buffer [32 << 10]byte
	for len(data) != 0 {
		block := buffer[:min(len(buffer), len(data))]
		_, err := io.ReadFull(input, block)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, ctx.Err()
		}
		if err != nil {
			return false, err
		}
		if !bytes.Equal(block, data[:len(block)]) {
			return false, ctx.Err()
		}
		data = data[len(block):]
	}
	_, err = io.ReadFull(input, buffer[:1])
	if err == io.EOF {
		return true, ctx.Err()
	}
	if err == nil {
		err = ctx.Err()
	}
	return false, err
}

// mergePackageResources 合并新增资源定义，保留原索引、路径基准与未知字段
// 入参: ctx 取消上下文, reader 原包, parts 待写入条目, doc 文档, directory 文档目录, added 新增资源XML，文件引用使用绝对包路径
// 返回: string 资源索引路径, bool 是否需要新增文档引用, error 读取、解析或取消错误
func mergePackageResources(ctx context.Context, reader *Reader, parts map[string][]byte, doc *Document, directory string, added []byte) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
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
		data, err = reader.readFileViewContext(ctx, name)
		if err != nil {
			return "", false, err
		}
	}
	addition, err := parseEditorXMLContext(ctx, added)
	if err != nil {
		return "", false, err
	}
	order := []string{"ColorSpaces", "DrawParams", "Fonts", "MultiMedias", "CompositeGraphicUnits"}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return "", false, err
	}
	slices.SortStableFunc(addition.children, func(a, b *editorXML) int {
		return slices.Index(order, a.name.Local) - slices.Index(order, b.name.Local)
	})
	var patches []editorXMLPatch
	var addedContent []byte
	for _, group := range addition.children {
		target := root.child(group.name.Local)
		scope := root
		used := make(map[[2]string]bool)
		if target != nil {
			scope = target
			for _, old := range target.children {
				if err := ctx.Err(); err != nil {
					return "", false, err
				}
				if old.matchesOFD(old.name.Local) {
					used[[2]string{old.name.Local, editorImportKey(old.attr("ID"))}] = true
				}
			}
		}
		var content []byte
		for _, node := range group.children {
			if err := ctx.Err(); err != nil {
				return "", false, err
			}
			key := [2]string{node.name.Local, editorImportKey(node.attr("ID"))}
			if used[key] {
				continue
			}
			entry, err := editorXMLStandalone(added[node.start:node.end], node)
			if err != nil {
				return "", false, err
			}
			entry, err = editorXMLGenerated(entry, scope.name.Space)
			if err != nil {
				return "", false, err
			}
			content = append(content, entry...)
			used[key] = true
		}
		if len(content) == 0 {
			continue
		}
		if target != nil {
			content = append(bytes.Clone(data[target.open:target.close]), content...)
			patches = append(patches, editorXMLContent(data, target, content))
			continue
		}
		content, err = root.containerXML(group.name.Local, nil, content)
		if err != nil {
			return "", false, err
		}
		position := root.close
		for _, child := range root.children {
			if child.matchesOFD(child.name.Local) && slices.Index(order, child.name.Local) > slices.Index(order, group.name.Local) {
				position = child.start
				break
			}
		}
		if root.open == root.end {
			addedContent = append(addedContent, content...)
		} else {
			patches = append(patches, editorXMLPatch{position, position, content})
		}
	}
	if len(addedContent) != 0 {
		patches = append(patches, editorXMLContent(data, root, addedContent))
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if len(patches) != 0 {
		data = editorPatchXML(data, patches)
	}
	parts[name] = data
	return name, false, nil
}
