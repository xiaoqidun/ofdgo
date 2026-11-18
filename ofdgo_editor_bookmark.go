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
	"slices"
	"strings"
	"unicode/utf8"
)

// Bookmarks 按声明顺序读取当前文档版本的命名书签，返回独立副本，不读取跳转引用
// 返回: []Bookmark 书签列表，无书签时为nil；error 读取、结构或名称重复错误
func (e *Editor) Bookmarks() ([]Bookmark, error) {
	if e.source == nil {
		return nil, nil
	}
	data, err := e.source.reader.readFile(e.source.reader.documentRoot)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	list, err := editorBookmarks(data, root)
	if err != nil {
		return nil, err
	}
	pages := e.validBookmarkPages()
	var result []Bookmark
	for _, item := range list {
		if pages[editorResourceID(item.Dest.PageID)] {
			result = append(result, item)
		}
	}
	return result, nil
}

// AddBookmark 在当前文档版本末尾添加名称唯一的书签，保留扩展内容并记录一次撤销
// 入参: name 非空且区分大小写的名称，不自动裁剪空白；dest 目标，PageID为当前页面标识而非页序号
// 返回: error 名称、目标、页面或提交错误
func (e *Editor) AddBookmark(name string, dest Dest) error {
	return e.editBookmark("", &Bookmark{Name: name, Dest: dest}, true)
}

// UpdateBookmark 按原名称更新书签，重命名同步当前文档全部标准跳转引用，整体可撤销
// 入参: name 原名称；next 完整的新书签，Dest.PageID为当前页面标识；名称不变时仅修改目标
// 返回: error 名称不存在、重名、目标无效、引用读取或提交错误；内容未变时不记录撤销
func (e *Editor) UpdateBookmark(name string, next Bookmark) error {
	return e.editBookmark(name, &next, false)
}

// DeleteBookmark 删除当前文档版本的命名书签，存在标准跳转引用时拒绝删除
// 入参: name 书签名称；其他文档和历史版本的引用不阻止当前版本删除
// 返回: error 名称不存在、仍被引用、引用读取或提交错误
func (e *Editor) DeleteBookmark(name string) error {
	return e.editBookmark(name, nil, false)
}

// editorBookmarks 读取标准书签定义，拒绝重复集合、重名及缺失或重复目标，不解析扩展子树
// 入参: data 文档原文；root 文档根节点
// 返回: []Bookmark 独立定义列表；error 结构或解码错误
func editorBookmarks(data []byte, root *editorXML) ([]Bookmark, error) {
	if root.childAt("Bookmarks", 1) != nil {
		return nil, fmt.Errorf("multiple bookmark collections")
	}
	container := root.child("Bookmarks")
	if container == nil {
		return nil, nil
	}
	var result []Bookmark
	seen := make(map[string]bool)
	for _, node := range container.children {
		if !node.matchesOFD("Bookmark") {
			continue
		}
		name := node.attr("Name")
		if err := validateBookmarkName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate bookmark %q", name)
		}
		seen[name] = true
		if node.child("Dest") == nil || node.childAt("Dest", 1) != nil {
			return nil, fmt.Errorf("bookmark %q requires one destination", name)
		}
		fragment, err := editorXMLStandalone(data[node.start:node.end], node)
		if err != nil {
			return nil, err
		}
		var bookmark Bookmark
		if err := xml.Unmarshal(fragment, &bookmark); err != nil {
			return nil, err
		}
		result = append(result, bookmark)
	}
	return result, nil
}

// validateBookmarkName 校验非空名称及XML可无损表示的Unicode字符
// 入参: name 名称
// 返回: error 名称错误
func validateBookmarkName(name string) error {
	if strings.TrimSpace(name) == "" || !utf8.ValidString(name) {
		return fmt.Errorf("bookmark name must be nonempty valid UTF-8")
	}
	for _, char := range name {
		if char < 0x20 && char != '\t' && char != '\n' && char != '\r' || char == 0xfffe || char == 0xffff {
			return fmt.Errorf("bookmark name contains an invalid XML character")
		}
	}
	return nil
}

// validateBookmark 校验书签名称、标准目标参数及当前编辑页树中的目标标识
// 入参: bookmark 待写入定义
// 返回: error 名称、目标或页面错误
func (e *Editor) validateBookmark(bookmark Bookmark) error {
	if err := validateBookmarkName(bookmark.Name); err != nil {
		return err
	}
	if _, err := bookmark.Dest.attributes(); err != nil {
		return err
	}
	id := editorResourceID(bookmark.Dest.PageID)
	for _, page := range e.pages {
		if id != "" && editorResourceID(page.ID) == id {
			return nil
		}
	}
	return fmt.Errorf("bookmark destination page %q does not exist", bookmark.Dest.PageID)
}

// editBookmark 校验定义后原子提交局部XML补丁，重命名使用包含未保存编辑的完整快照
// 入参: name 原名称；next 新定义，nil表示删除；add 是否新增
// 返回: error 校验、引用扫描或提交错误
func (e *Editor) editBookmark(name string, next *Bookmark, add bool) error {
	if !add {
		if err := validateBookmarkName(name); err != nil {
			return err
		}
	}
	if next != nil {
		if err := e.validateBookmark(*next); err != nil {
			return err
		}
	}
	bookmarks, err := e.Bookmarks()
	if err != nil {
		return err
	}
	index := -1
	for i, bookmark := range bookmarks {
		if !add && bookmark.Name == name {
			index = i
		}
		if next != nil && bookmark.Name == next.Name && (add || bookmark.Name != name) {
			return fmt.Errorf("bookmark %q already exists", next.Name)
		}
	}
	if !add && index < 0 {
		return fmt.Errorf("bookmark %q not found", name)
	}
	rename := !add && next != nil && name != next.Name
	return e.Transaction(func(e *Editor) error {
		base := e.source
		if base == nil {
			base, err = e.importBase()
			if err != nil {
				return err
			}
		}
		parts := make(map[string][]byte)
		if rename || next == nil {
			ctx := context.Background()
			if e.output != nil {
				ctx = e.output.ctx
			}
			pending, _, err := e.sourceParts(ctx, nil, nil, false, false)
			if err != nil {
				return err
			}
			reader := cloneEditorReader(base.reader)
			if reader.files == nil {
				reader.files = make(map[string][]byte)
			}
			maps.Copy(reader.files, pending)
			parts, err = editorBookmarkReferences(reader, name, next)
			if err != nil {
				return err
			}
			for i, resource := range e.resources {
				if resource.definition() == "" || len(resource.data) == 0 {
					continue
				}
				data, err := editorBookmarkReferenceXML(resource.data, resource.name, name, next)
				if err != nil {
					return err
				}
				if !bytes.Equal(data, resource.data) {
					e.resources[i].data = data
					e.resources[i].vectorKey = [32]byte{}
				}
			}
			if rename {
				maps.Copy(pending, parts)
				parts = pending
			}
		}
		entry := base.reader.documentRoot
		data, ok := parts[entry]
		if !ok {
			data, err = base.reader.readFile(entry)
			if err != nil {
				return err
			}
		}
		updated, err := editorBookmarkDefinition(data, name, next, add, e.validBookmarkPages())
		if err != nil {
			return err
		}
		if bytes.Equal(data, updated) && len(parts) == 0 {
			return nil
		}
		parts[entry] = updated
		if !rename {
			return e.commitBookmarkParts(base, parts)
		}
		working := &Editor{}
		if err := working.commitBookmarkParts(base, parts); err != nil {
			return err
		}
		return e.commitBookmarkSnapshot(working.source.reader)
	})
}

// commitBookmarkSnapshot 接入重命名后的包，保留已加载对象的会话排版、裁剪及尚未使用的注册资源
// 入参: reader 已完成隔离和重命名的包快照
// 返回: error 快照重建或页面加载错误
func (e *Editor) commitBookmarkSnapshot(reader *Reader) error {
	before := e.transactionSnapshot()
	if err := e.commitResourceSnapshot(context.Background(), reader); err != nil {
		return err
	}
	e.maxID = max(e.maxID, before.maxID)
	e.fonts, e.images, e.resourceID = before.fonts, before.images, before.resourceID
	e.fontRegistration = before.fontRegistration
	for i, page := range before.pages {
		states := make(map[string]GraphicObject)
		for _, layer := range page.Content.Layer {
			for _, object := range layer.Objects {
				states[editorObjectID(object)] = object
			}
		}
		if len(states) == 0 {
			continue
		}
		current, err := e.page(i)
		if err != nil {
			return err
		}
		for j := range current.Content.Layer {
			for k := range current.Content.Layer[j].Objects {
				object := &current.Content.Layer[j].Objects[k]
				previous := states[editorObjectID(*object)]
				object.state = previous.state
				object.TextObject.layout = previous.TextObject.layout
				object.CompositeGraphicUnit.states = previous.CompositeGraphicUnit.states
			}
		}
	}
	if before.source != nil {
		e.source.annotationStates = before.source.annotationStates
	}
	return nil
}

// commitBookmarkParts 隔离修改后复用注解快照提交，保守复制路径不覆盖未知扩展可能引用的原文件
// 入参: base 原包快照；parts 当前入口修改集
// 返回: error 依赖解析、隔离或提交错误
func (e *Editor) commitBookmarkParts(base *editorSource, parts map[string][]byte) error {
	changes, err := base.reader.versionChanges(context.Background(), parts)
	if err != nil {
		changes, err = editorBookmarkIsolate(base.reader, parts)
		if err != nil {
			return err
		}
	}
	reader := cloneEditorReader(base.reader)
	if reader.files == nil {
		reader.files = make(map[string][]byte)
	}
	maps.Copy(reader.files, changes)
	source := *base
	source.reader = reader
	return e.commitAnnotationParts(&source, nil)
}

// editorBookmarkIsolate 在无法完整分析其他入口时保护所有已有文件，复制修改文件及其标准引用祖先
// 入参: reader 原包；changes 当前入口修改集
// 返回: map[string][]byte 隔离后的修改集；error 当前入口依赖或版本清单错误
func editorBookmarkIsolate(reader *Reader, changes map[string][]byte) (map[string][]byte, error) {
	ctx := context.Background()
	entry, err := reader.DocumentRootContext(ctx)
	if err != nil {
		return nil, err
	}
	parts := maps.Clone(changes)
	graph, err := reader.versionEditGraph(ctx, entry, parts, nil)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]string)
	reserved := maps.Clone(parts)
	var pending []string
	allocate := func(name string) {
		if paths[name] != "" || !reader.fileNames[name] {
			return
		}
		target := packageAvailableName(reader, reserved, name)
		paths[name], reserved[target] = target, nil
		pending = append(pending, name)
	}
	for _, name := range slices.Sorted(maps.Keys(parts)) {
		if name != "OFD.xml" && graph.used[name] {
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
			allocate(name)
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
			data, err = reader.readFile(source)
			if err != nil {
				return nil, err
			}
		}
		parts[target] = data
		delete(parts, source)
	}
	if reader.versionInfo != nil {
		if err := reader.versionManifest(ctx, parts, paths, graph.used, reader.fileNames, nil); err != nil {
			return nil, err
		}
	} else if target := paths[entry]; target != "" {
		data, ok := parts["OFD.xml"]
		if !ok {
			data, err = reader.readFile("OFD.xml")
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		node := root.childAt("DocBody", reader.documentIndex).child("DocRoot")
		parts["OFD.xml"] = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, editorFontXMLText("/"+target))})
	}
	return parts, nil
}

// validBookmarkPages 返回当前有效页面标识集合，排除已删除页面
// 返回: map[string]bool 有效页面标识集合
func (e *Editor) validBookmarkPages() map[string]bool {
	pages := make(map[string]bool, len(e.pages))
	for _, page := range e.pages {
		id := editorResourceID(page.ID)
		if id != "" && !e.removedPages[id] {
			pages[id] = true
		}
	}
	return pages
}

// editorBookmarkDefinition 修改指定书签的标准字段，保留其扩展属性、扩展节点及其他定义原文
// 入参: data 文档原文；name 原名称；next 新定义，nil表示删除；add 是否新增；pages 当前有效页面集合
// 返回: []byte 修改后的文档；error 结构或编码错误
func editorBookmarkDefinition(data []byte, name string, next *Bookmark, add bool, pages map[string]bool) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	container := root.child("Bookmarks")
	staleNode := func(node *editorXML) bool {
		if !node.matchesOFD("Bookmark") {
			return false
		}
		dest := node.child("Dest")
		if dest == nil {
			return false
		}
		return !pages[editorResourceID(dest.attr("PageID"))]
	}
	if add {
		ctx := container
		if ctx == nil {
			ctx = root
		}
		attrs, err := next.Dest.attributes()
		if err != nil {
			return nil, err
		}
		dest, err := ctx.containerXML("Dest", attrs, nil)
		if err != nil {
			return nil, err
		}
		item, err := ctx.containerXML("Bookmark", ofdAttrs{{Name: xml.Name{Local: "Name"}, Value: next.Name}}, dest)
		if err != nil {
			return nil, err
		}
		if container != nil {
			if container.open == container.end {
				return editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, container, item)}), nil
			}
			var patches []editorXMLPatch
			for _, node := range container.children {
				if staleNode(node) && node.attr("Name") == next.Name {
					patches = append(patches, editorXMLPatch{node.start, node.end, nil})
				}
			}
			patches = append(patches, editorXMLPatch{container.close, container.close, item})
			return editorPatchXML(data, patches), nil
		}
		item, err = root.containerXML("Bookmarks", nil, item)
		if err != nil {
			return nil, err
		}
		at := root.close
		for _, child := range root.children {
			if child.matchesOFD(child.name.Local) && slices.Contains([]string{"Annotations", "CustomTags", "Attachments", "Extensions"}, child.name.Local) {
				at = child.start
				break
			}
		}
		return editorPatchXML(data, []editorXMLPatch{{at, at, item}}), nil
	}
	if container == nil {
		return nil, fmt.Errorf("bookmark %q not found", name)
	}
	for _, node := range container.children {
		if !node.matchesOFD("Bookmark") || node.attr("Name") != name || staleNode(node) {
			continue
		}
		if next == nil {
			patch := editorXMLPatch{node.start, node.end, nil}
			if len(container.children) == 1 && editorXMLAttributes(container, "") && len(bytes.TrimSpace(data[container.open:node.start])) == 0 && len(bytes.TrimSpace(data[node.end:container.close])) == 0 {
				patch.start, patch.end = container.start, container.end
			}
			return editorPatchXML(data, []editorXMLPatch{patch}), nil
		}
		fragment, err := editorXMLStandalone(data[node.start:node.end], node)
		if err != nil {
			return nil, err
		}
		var current Bookmark
		if err := xml.Unmarshal(fragment, &current); err != nil {
			return nil, err
		}
		var patches []editorXMLPatch
		if name != next.Name {
			for _, other := range container.children {
				if other != node && staleNode(other) && other.attr("Name") == next.Name {
					patches = append(patches, editorXMLPatch{other.start, other.end, nil})
				}
			}
			patch, err := editorXMLAttributePatch(data, node, "Name", next.Name)
			if err != nil {
				return nil, err
			}
			patches = append(patches, patch)
		}
		if current.Dest != next.Dest {
			dest := node.child("Dest")
			if dest == nil {
				return nil, fmt.Errorf("bookmark %q has no destination", name)
			}
			attrs, err := next.Dest.attributes()
			if err != nil {
				return nil, err
			}
			values := make(map[string]string, len(attrs))
			for _, attr := range attrs {
				values[attr.Name.Local] = attr.Value
			}
			value := bytes.Clone(data[dest.start:dest.end])
			for _, key := range []string{"Type", "PageID", "Left", "Top", "Right", "Bottom", "Zoom"} {
				parsed, err := parseEditorXML(value)
				if err != nil {
					return nil, err
				}
				var replacement *string
				if text, ok := values[key]; ok {
					replacement = &text
				}
				patch, err := editorXMLAttributeChange(value, parsed, key, replacement)
				if err != nil {
					return nil, err
				}
				value = editorPatchXML(value, []editorXMLPatch{patch})
			}
			patches = append(patches, editorXMLPatch{dest.start, dest.end, value})
		}
		return editorPatchXML(data, patches), nil
	}
	return nil, fmt.Errorf("bookmark %q not found", name)
}

// editorBookmarkReferences 扫描当前入口依赖中的标准书签跳转，拒绝带引用删除或生成重命名补丁
// 入参: reader 包含未保存修改的快照；name 原名称；next 新定义，nil表示删除
// 返回: map[string][]byte 需要修改的结构文件；error 读取、解析或被引用错误
func editorBookmarkReferences(reader *Reader, name string, next *Bookmark) (map[string][]byte, error) {
	_, structures, err := reader.versionDependencies(context.Background(), reader.documentRoot, nil, nil)
	if err != nil {
		return nil, err
	}
	parts := make(map[string][]byte)
	for _, file := range slices.Sorted(maps.Keys(structures)) {
		data, err := reader.readFile(file)
		if err != nil {
			return nil, err
		}
		updated, err := editorBookmarkReferenceXML(data, file, name, next)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(data, updated) {
			parts[file] = updated
		}
	}
	return parts, nil
}

// editorBookmarkReferenceXML 仅识别标准动作中的名称引用，保留扩展命名空间及不透明扩展负载
// 入参: data 结构原文；file 错误上下文中的文件名；name 原名称；next 新定义，nil表示检查删除
// 返回: []byte 修改后的原文；error 解析、属性编码或被引用错误
func editorBookmarkReferenceXML(data []byte, file, name string, next *Bookmark) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var patches []editorXMLPatch
	var walk func(*editorXML) error
	walk = func(node *editorXML) error {
		if !node.matchesOFD(node.name.Local) {
			return nil
		}
		if node.matchesOFD("Data") && node.parent != nil && node.parent.matchesOFD("Extension") {
			return nil
		}
		if node.matchesOFD("Action") && node.parent != nil && node.parent.matchesOFD("Actions") {
			for _, target := range node.children {
				if !target.matchesOFD("Goto") {
					continue
				}
				for _, bookmark := range target.children {
					if !bookmark.matchesOFD("Bookmark") || bookmark.attr("Name") != name {
						continue
					}
					if next == nil {
						return fmt.Errorf("bookmark %q is referenced in %s", name, file)
					}
					patch, err := editorXMLAttributePatch(data, bookmark, "Name", next.Name)
					if err != nil {
						return err
					}
					patches = append(patches, patch)
				}
			}
		}
		for _, child := range node.children {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return editorPatchXML(data, patches), nil
}
