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
	"path"
	"slices"
	"strconv"
	"strings"
)

// editorNameCache 缓存当前编辑器的已占资源路径与前缀递增序号，消除重复命名探测
type editorNameCache struct {
	owner    *Editor
	source   *editorSource
	resCount int
	used     map[string]struct{}
	counters map[string]int
}

// packagePagePath 按稳定页面标识生成路径，页面重排不改变文件名
// 入参: directory 文档目录, id 页面标识
// 返回: string 页面路径
func packagePagePath(directory, id string) string {
	return path.Join(directory, "Pages", "Page_"+id, "Content.xml")
}

// appendOFDXMLBeforeClosing 在标准OFD 2016根元素闭合标签前直接追加子XML片段
// 入参: data 原XML字节, localName 根元素本地名, addition 待追加子片段
// 返回: []byte 追加结果, bool 是否满足安全快路径
func appendOFDXMLBeforeClosing(data []byte, localName string, addition []byte) ([]byte, bool) {
	openPrefix := "<ofd:" + localName + ` xmlns:ofd="` + ofdNamespace2016 + `">`
	closeTag := "</ofd:" + localName + ">"
	return appendRawXMLBeforeClosing(data, openPrefix, closeTag, addition)
}

// appendNamespacedXMLBeforeClosing 在指定默认命名空间根元素闭合标签前直接追加子XML片段
// 入参: data 原XML字节, localName 根元素本地名, namespace 默认命名空间, addition 待追加子片段
// 返回: []byte 追加结果, bool 是否满足安全快路径
func appendNamespacedXMLBeforeClosing(data []byte, localName, namespace string, addition []byte) ([]byte, bool) {
	openPrefix := "<" + localName + ` xmlns="` + namespace + `">`
	closeTag := "</" + localName + ">"
	return appendRawXMLBeforeClosing(data, openPrefix, closeTag, addition)
}

// appendRawXMLBeforeClosing 校验标准开闭标签并在根闭合标签前执行O(1)字节拼接
// 入参: data 原XML字节, openPrefix 期望根开始标签, closeTag 期望根结束标签, addition 待追加子片段
// 返回: []byte 追加结果, bool 是否匹配标准结构
func appendRawXMLBeforeClosing(data []byte, openPrefix, closeTag string, addition []byte) ([]byte, bool) {
	body := bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	body = bytes.TrimLeft(body, " \t\r\n")
	if bytes.HasPrefix(body, []byte(xml.Header)) {
		body = bytes.TrimLeft(body[len(xml.Header):], " \t\r\n")
	}
	if !bytes.HasPrefix(body, []byte(openPrefix)) {
		return nil, false
	}
	trimmed := bytes.TrimRight(data, " \t\r\n")
	if !bytes.HasSuffix(trimmed, []byte(closeTag)) {
		return nil, false
	}
	closeStart := len(trimmed) - len(closeTag)
	if closeStart <= 0 {
		return nil, false
	}
	out := make([]byte, 0, len(data)+len(addition))
	out = append(out, data[:closeStart]...)
	out = append(out, addition...)
	out = append(out, data[closeStart:]...)
	return out, true
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
			occupied = occupied || cached || reader.encryptedSource().has(candidate)
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
	if _, ok := reader.packageFile(name); !ok && !reader.encryptedSource().has(name) {
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

// packageDirectory 获取当前文档的包内根目录，新建文档与编辑文档共用布局
// 返回: string 文档目录
func (e *Editor) packageDirectory() string {
	if e.source != nil {
		return e.source.directory
	}
	return "Doc_0"
}

// packageName 为新增条目分配文档内路径，不覆盖原文件或本次资源
// 维护当前编辑器路径缓存与按前缀递增的序号计数器，将命名探测降至均摊O(1)
// 入参: relative 文档相对路径
// 返回: string 未占用路径
func (e *Editor) packageName(relative string) string {
	var reader *Reader
	if e.source != nil {
		reader = e.source.reader
	}
	name := path.Join(e.packageDirectory(), relative)
	cache := e.nameCache
	if cache == nil || cache.owner != e {
		cache = &editorNameCache{
			owner:    e,
			source:   e.source,
			resCount: len(e.resources),
			used:     make(map[string]struct{}, len(e.resources)),
			counters: make(map[string]int),
		}
		for _, resource := range e.resources {
			cache.used[resource.name] = struct{}{}
		}
		e.nameCache = cache
	} else if cache.source != e.source || cache.resCount > len(e.resources) {
		clear(cache.used)
		clear(cache.counters)
		for _, resource := range e.resources {
			cache.used[resource.name] = struct{}{}
		}
		cache.source = e.source
		cache.resCount = len(e.resources)
	} else if cache.resCount < len(e.resources) {
		for _, resource := range e.resources[cache.resCount:] {
			cache.used[resource.name] = struct{}{}
		}
		cache.resCount = len(e.resources)
	}
	base, extension := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	for i := cache.counters[name]; ; i++ {
		candidate := name
		if i != 0 {
			candidate = base + "_" + strconv.Itoa(i) + extension
		}
		occupied := false
		if reader != nil {
			_, occupied = reader.packageFile(candidate)
			_, cached := reader.files[candidate]
			occupied = occupied || cached || reader.encryptedSource().has(candidate)
		}
		if !occupied {
			_, occupied = cache.used[candidate]
		}
		if !occupied {
			cache.counters[name] = i
			return candidate
		}
	}
}

// syncPackageNameCache 在增量提交推进文档快照后同步命名缓存来源，保留序号计数器
// 入参: before 提交前的包快照, after 提交后的包快照
func (e *Editor) syncPackageNameCache(before, after *editorSource) {
	if e.nameCache != nil && e.nameCache.owner == e && e.nameCache.source == before {
		e.nameCache.source = after
	}
}
