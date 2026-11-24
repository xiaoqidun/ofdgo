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
	"strings"
)

// editorStructureIndex 读取或准备当前文档的标引索引，保留内联入口和未知扩展
// 入参: base 编辑包快照, parts 本次输出条目，可为nil
// 返回: string 索引文件路径, []byte 原文, *editorXML 容器, []*editorXML 标引入口, error 读取错误
func editorStructureIndex(base *editorSource, parts map[string][]byte) (string, []byte, *editorXML, []*editorXML, error) {
	return editorStructureIndexContext(context.Background(), base, parts)
}

// editorStructureIndexContext 读取或准备标引索引，文件读取及解析期间响应取消
// 入参: ctx 取消上下文, base 编辑包快照, parts 本次输出条目，可为nil
// 返回: string 索引路径, []byte 原文, *editorXML 容器, []*editorXML 入口, error 读取或取消错误
func editorStructureIndexContext(ctx context.Context, base *editorSource, parts map[string][]byte) (string, []byte, *editorXML, []*editorXML, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, nil, nil, err
	}
	name := base.reader.documentRoot
	if base.document.CustomTags.Path != "" {
		name = base.reader.ResPath(base.document.CustomTags.Path)
	}
	data, ok := parts[name]
	if !ok {
		var err error
		data, err = base.reader.readFileViewContext(ctx, name)
		if err != nil {
			return "", nil, nil, nil, err
		}
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if base.document.CustomTags.Path == "" {
		container := root.child("CustomTags")
		if container == nil || len(container.children) == 0 {
			name = packageAvailableName(base.reader, nil, path.Join(base.reader.rootDir, "CustomTags.xml"))
			data, err = root.containerXML("CustomTags", nil, nil)
			if err == nil {
				root, err = parseEditorXMLContext(ctx, data)
			}
			if err != nil {
				return "", nil, nil, nil, err
			}
		} else {
			root = container
		}
	}
	var entries []*editorXML
	for _, child := range root.children {
		if child.matchesOFD("CustomTag") {
			entries = append(entries, child)
		}
	}
	return name, data, root, entries, nil
}

// editorStructureNode 按完整XML子节点索引定位，与Reader.Structure的路径一致
// 入参: root 根节点, path 从0开始的路径
// 返回: *editorXML 节点, error 路径错误
func editorStructureNode(root *editorXML, path []int) (*editorXML, error) {
	for _, index := range path {
		if index < 0 || index >= len(root.children) {
			return nil, fmt.Errorf("custom tag node not found")
		}
		root = root.children[index]
	}
	return root, nil
}

// Structure 获取当前编辑状态的标引，包括页面和图元删除后的引用调整
// 返回: []Structure 标引结构, error 快照或解析错误
func (e *Editor) Structure() ([]Structure, error) {
	r, err := e.reader(nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Structure()
}

// AddStructure 添加独立标引及根节点，FileLoc由库分配，不接收外部内容路径
// SchemaLoc仅允许引用包内已有文件，不访问网络或验证业务Schema
// 入参: tag 类型、命名空间及可选Schema位置, root 根节点
// 返回: int 从0开始的标引索引, error 校验或提交错误
func (e *Editor) AddStructure(tag CustomTag, root StructureNode) (int, error) {
	data, err := encodeStructureNode(root)
	if err != nil {
		return 0, err
	}
	return e.addStructureXML(context.Background(), tag, data)
}

// AddStructureXML 添加XML标引原文，保留处理指令、注释、前缀及混合文本
// FileLoc由库分配，SchemaLoc仅引用包内已有文件，返回后不再引用输入数据
// 入参: tag 标引入口, data 完整XML
// 返回: int 从0开始的标引索引, error 解析、引用或提交错误
func (e *Editor) AddStructureXML(tag CustomTag, data []byte) (int, error) {
	return e.AddStructureXMLContext(context.Background(), tag, data)
}

// AddStructureXMLContext 校验并保存XML标引原文，取消或失败时不提交编辑状态
// 入参: ctx 取消上下文, tag 标引入口, data 完整XML
// 返回: int 从0开始的标引索引, error 解析、引用、提交或取消错误
func (e *Editor) AddStructureXMLContext(ctx context.Context, tag CustomTag, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return e.addStructureXML(ctx, tag, bytes.Clone(data))
}

// UpdateStructureXML 替换标引XML原文，保留入口属性并隔离共享内容文件
// 入参: index 标引索引, data 完整XML，返回后不再引用输入数据
// 返回: error 索引、解析、引用或提交错误
func (e *Editor) UpdateStructureXML(index int, data []byte) error {
	return e.UpdateStructureXMLContext(context.Background(), index, data)
}

// UpdateStructureXMLContext 校验并原子替换标引原文，相同内容不产生撤销记录
// 入参: ctx 取消上下文, index 标引索引, data 完整XML
// 返回: error 索引、解析、引用、提交或取消错误
func (e *Editor) UpdateStructureXMLContext(ctx context.Context, index int, data []byte) error {
	return e.editStructureContext(ctx, index, func([]byte, *editorXML, CustomTag) ([]byte, error) {
		return bytes.Clone(data), nil
	})
}

// DeleteStructure 删除标引入口，不删除正文或其他入口共享的资源
// 入参: index 从0开始的标引索引
// 返回: error 索引或提交错误
func (e *Editor) DeleteStructure(index int) error {
	if e.source == nil {
		return fmt.Errorf("custom tag structure not found: %d", index)
	}
	name, data, _, entries, err := editorStructureIndex(e.source, nil)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(entries) {
		return fmt.Errorf("custom tag structure not found: %d", index)
	}
	node := entries[index]
	base := *e.source
	base.structureStarts = make(map[int]map[string]int, len(e.source.structureStarts))
	for entry, starts := range e.source.structureStarts {
		if entry < index {
			base.structureStarts[entry] = starts
		} else if entry > index {
			base.structureStarts[entry-1] = starts
		}
	}
	return e.commitAnnotationParts(&base, map[string][]byte{name: editorPatchXML(data, []editorXMLPatch{{node.start, node.end, nil}})})
}

// AddStructureNode 在指定父节点末尾添加子树，空路径表示根节点
// 入参: index 标引索引, parent 从0开始的节点路径, node 新节点及子树
// 返回: []int 新节点路径, error 校验或提交错误
func (e *Editor) AddStructureNode(index int, parent []int, node StructureNode) ([]int, error) {
	encoded, err := encodeStructureNode(node)
	if err != nil {
		return nil, err
	}
	var result []int
	err = e.editStructure(index, func(data []byte, root *editorXML, tag CustomTag) ([]byte, error) {
		target, err := editorStructureNode(root, parent)
		if err != nil {
			return nil, err
		}
		structure, err := parseStructure(context.Background(), data, tag)
		if err != nil {
			return nil, err
		}
		if (structureOfficial(tag, root.name.Space) || structureInvoiceReferences(structure)) && target.name.Space == root.name.Space && target.name.Local == "ObjectRef" {
			return nil, fmt.Errorf("ObjectRef cannot contain child nodes")
		}
		content := bytes.Clone(encoded)
		if target.open != target.end {
			content = append(bytes.Clone(data[target.open:target.close]), content...)
		}
		result = append(slices.Clone(parent), len(target.children))
		return editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, target, content)}), nil
	})
	return result, err
}

// UpdateStructureNode 修改节点名称、命名空间、属性和直接文本，保留子节点、注释及处理指令
// node的Children和References必须为空，图元关联通过SetStructureReferences维护
// 入参: index 标引索引, path 从0开始的节点路径, node 节点字段
// 返回: error 校验或提交错误
func (e *Editor) UpdateStructureNode(index int, path []int, node StructureNode) error {
	if len(node.Children) != 0 || len(node.References) != 0 {
		return fmt.Errorf("use node operations to edit children or references")
	}
	return e.editStructure(index, func(data []byte, root *editorXML, _ CustomTag) ([]byte, error) {
		target, err := editorStructureNode(root, path)
		if err != nil {
			return nil, err
		}
		updated, err := updateStructureNodeXML(data, target, node)
		if err != nil {
			return nil, err
		}
		return editorPatchXML(data, []editorXMLPatch{{target.start, target.end, updated}}), nil
	})
}

// DeleteStructureNode 删除节点及其子树，根节点须通过DeleteStructure删除
// 入参: index 标引索引, path 从0开始的非空节点路径
// 返回: error 路径或提交错误
func (e *Editor) DeleteStructureNode(index int, path []int) error {
	if len(path) == 0 {
		return fmt.Errorf("delete the structure entry to remove its root")
	}
	return e.editStructure(index, func(data []byte, root *editorXML, _ CustomTag) ([]byte, error) {
		node, err := editorStructureNode(root, path)
		if err != nil {
			return nil, err
		}
		return editorPatchXML(data, []editorXMLPatch{{node.start, node.end, nil}}), nil
	})
}

// SetStructureReferences 替换公文节点的直接ObjectRef，保留其他子节点和原文
// 按输入顺序绑定图元，允许跨页和重复引用；空列表解除绑定，不修改版式内容
// 入参: index 标引索引, path 节点路径, references 页面标识和有序图元标识
// 返回: error 类型、目标校验或提交错误
func (e *Editor) SetStructureReferences(index int, path []int, references []StructureReference) error {
	return e.editStructure(index, func(data []byte, root *editorXML, tag CustomTag) ([]byte, error) {
		node, err := editorStructureNode(root, path)
		if err != nil {
			return nil, err
		}
		structure, err := parseStructure(context.Background(), data, tag)
		if err != nil {
			return nil, err
		}
		if !(structureOfficial(tag, root.name.Space) || structureInvoiceReferences(structure)) || node.name.Local == "ObjectRef" {
			return nil, fmt.Errorf("custom tag does not support ObjectRef binding")
		}
		for parent := node; parent != nil; parent = parent.parent {
			if parent.name.Space != root.name.Space {
				return nil, fmt.Errorf("cannot bind objects inside an extension namespace")
			}
		}
		var added []byte
		for _, ref := range references {
			if editorResourceID(ref.PageID) == "" || len(ref.ObjectIDs) == 0 {
				return nil, fmt.Errorf("ObjectRef requires a page and object identifiers")
			}
			for _, id := range ref.ObjectIDs {
				if editorResourceID(id) == "" {
					return nil, fmt.Errorf("invalid custom tag object identifier: %s", id)
				}
			}
			encoded, err := encodeStructureNode(StructureNode{Name: "ObjectRef", Namespace: root.name.Space, Attributes: []StructureAttribute{{Name: "PageRef", Value: ref.PageID}}, Text: strings.Join(ref.ObjectIDs, ",")})
			if err != nil {
				return nil, err
			}
			added = append(added, encoded...)
		}
		var content []byte
		if node.open != node.end {
			var patches []editorXMLPatch
			for _, child := range node.children {
				if child.name.Space == root.name.Space && child.name.Local == "ObjectRef" {
					patches = append(patches, editorXMLPatch{child.start - node.open, child.end - node.open, nil})
				}
			}
			content = editorPatchXML(data[node.open:node.close], patches)
		}
		content = append(content, added...)
		return editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, content)}), nil
	})
}

// addStructureXML 接管独立XML数据并提交标引，复用编码或解码结果而不再次复制
// 入参: ctx 取消上下文, tag 标引入口, data 独立XML数据，提交后不再修改
// 返回: int 标引索引, error 校验、提交或取消错误
func (e *Editor) addStructureXML(ctx context.Context, tag CustomTag, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(tag.NameSpace) == "" && strings.TrimSpace(tag.TypeID) == "" || tag.FileLoc != "" {
		return 0, fmt.Errorf("custom tag requires NameSpace or TypeID and an automatically assigned FileLoc")
	}
	if !structureXMLText(tag.TypeID) || !structureXMLText(tag.NameSpace) || !structureXMLText(tag.SchemaLoc) {
		return 0, fmt.Errorf("invalid custom tag metadata")
	}
	tree, err := parseStructure(ctx, data, tag)
	if err != nil {
		return 0, err
	}
	objects, err := e.validateStructureReferencesContext(ctx, tree, tag, nil)
	if err != nil {
		return 0, err
	}
	base := e.source
	if base == nil {
		base, err = e.importBaseContext(ctx)
		if err != nil {
			return 0, err
		}
	}
	if tag.SchemaLoc != "" {
		name := base.reader.ResPath(tag.SchemaLoc)
		if _, err := base.reader.readFileViewContext(ctx, name); err != nil {
			return 0, err
		}
		tag.SchemaLoc = "/" + name
	}
	tag.FileLoc = "/" + e.packageName("Tags/Tag.xml")
	var attrs ofdAttrs
	attrs.add("TypeID", tag.TypeID)
	attrs.add("NameSpace", tag.NameSpace)
	if base.document.CustomTags.Path != "" && base.customTagCountReady {
		indexName := base.reader.ResPath(base.document.CustomTags.Path)
		if existing, readErr := base.reader.readFileViewContext(ctx, indexName); readErr == nil {
			var content []byte
			if tag.SchemaLoc != "" {
				content = append(content, editorXMLText("SchemaLoc", tag.SchemaLoc)...)
			}
			content = append(content, editorXMLText("FileLoc", tag.FileLoc)...)
			entry, encErr := editorXMLContainer("CustomTag", attrs, content)
			if encErr == nil {
				entry = bytes.TrimPrefix(entry, []byte(xml.Header))
				if patched, ok := appendOFDXMLBeforeClosing(existing, "CustomTags", entry); ok {
					idx := base.customTagCount
					parts := map[string][]byte{strings.TrimPrefix(tag.FileLoc, "/"): data, indexName: patched}
					if err := e.commitStructurePartsContext(ctx, base, parts, objects, idx); err != nil {
						return 0, err
					}
					if e.source != nil {
						updated := *e.source
						updated.customTagCount = idx + 1
						updated.customTagCountReady = true
						e.source = &updated
					}
					return idx, nil
				}
			}
		}
	}
	name, index, container, entries, err := editorStructureIndexContext(ctx, base, nil)
	if err != nil {
		return 0, err
	}
	var content []byte
	if tag.SchemaLoc != "" {
		content = append(content, container.textXML("SchemaLoc", tag.SchemaLoc)...)
	}
	content = append(content, container.textXML("FileLoc", tag.FileLoc)...)
	entry, err := container.containerXML("CustomTag", attrs, content)
	if err != nil {
		return 0, err
	}
	parts := map[string][]byte{strings.TrimPrefix(tag.FileLoc, "/"): data}
	if container.open != container.end {
		entry = append(bytes.Clone(index[container.open:container.close]), entry...)
	}
	parts[name] = editorPatchXML(index, []editorXMLPatch{editorXMLContent(index, container, entry)})
	if base.document.CustomTags.Path == "" && name != base.reader.documentRoot {
		docName := base.reader.documentRoot
		docData, err := base.reader.readFileViewContext(ctx, docName)
		if err != nil {
			return 0, err
		}
		doc, err := parseEditorXMLContext(ctx, docData)
		if err != nil {
			return 0, err
		}
		parts[docName], err = editorXMLSetText(docData, doc, [][2]string{{"CustomTags", "/" + name}})
		if err != nil {
			return 0, err
		}
	}
	if err := e.commitStructurePartsContext(ctx, base, parts, objects, len(entries)); err != nil {
		return 0, err
	}
	if e.source != nil {
		updated := *e.source
		updated.customTagCount = len(entries) + 1
		updated.customTagCountReady = true
		e.source = &updated
	}
	return len(entries), nil
}

// editStructure 验证完整标引后原子提交，隔离共享入口并保留未修改的XML
// 入参: index 标引索引, edit 内容变换
// 返回: error 校验、解析或提交错误
func (e *Editor) editStructure(index int, edit func([]byte, *editorXML, CustomTag) ([]byte, error)) error {
	return e.editStructureContext(context.Background(), index, edit)
}

// editStructureContext 校验并提交标引编辑，快照读取及引用遍历期间响应取消
// 入参: ctx 取消上下文, index 标引索引, edit 内容变换
// 返回: error 校验、解析、提交或取消错误
func (e *Editor) editStructureContext(ctx context.Context, index int, edit func([]byte, *editorXML, CustomTag) ([]byte, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.source == nil {
		return fmt.Errorf("custom tag structure not found: %d", index)
	}
	tags, err := e.source.reader.CustomTagsContext(ctx)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(tags) {
		return fmt.Errorf("custom tag structure not found: %d", index)
	}
	tag := tags[index]
	name := e.source.reader.ResPath(tag.FileLoc)
	snapshot, err := e.Reader(ctx)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	current, err := snapshot.CustomTagsContext(ctx)
	if err != nil {
		return err
	}
	if index >= len(current) {
		return fmt.Errorf("custom tag structure not found: %d", index)
	}
	data, err := snapshot.readFileViewContext(ctx, snapshot.ResPath(current[index].FileLoc))
	if err != nil {
		return err
	}
	if _, err := parseStructure(ctx, data, tag); err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	updated, err := edit(data, root, tag)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if bytes.Equal(data, updated) {
		return nil
	}
	tree, err := parseStructure(ctx, updated, tag)
	if err != nil {
		return err
	}
	objects, err := e.validateStructureReferencesContext(ctx, tree, tag, snapshot)
	if err != nil {
		return err
	}
	parts := make(map[string][]byte)
	for i, other := range tags {
		if i != index && e.source.reader.ResPath(other.FileLoc) == name {
			indexName, indexData, _, entries, err := editorStructureIndexContext(ctx, e.source, nil)
			if err != nil {
				return err
			}
			name = e.packageName("Tags/Tag.xml")
			loc := entries[index].child("FileLoc")
			if loc == nil {
				return fmt.Errorf("custom tag requires FileLoc")
			}
			var value bytes.Buffer
			_ = xml.EscapeText(&value, []byte("/"+name))
			parts[indexName] = editorPatchXML(indexData, []editorXMLPatch{editorXMLContent(indexData, loc, value.Bytes())})
			break
		}
	}
	parts[name] = updated
	return e.commitStructurePartsContext(ctx, e.source, parts, objects, index)
}

// commitStructurePartsContext 准备标引引用记录并原子提交，取消时保留原编辑状态
// 入参: ctx 取消上下文, base 原包, parts 修改条目, objects 有效图元, index 标引入口序号
// 返回: error 提交或取消错误
func (e *Editor) commitStructurePartsContext(ctx context.Context, base *editorSource, parts map[string][]byte, objects map[string]map[string]bool, index int) error {
	next := *base
	if len(e.structureRemaps) != 0 {
		next.structureStarts = maps.Clone(base.structureStarts)
		if next.structureStarts == nil {
			next.structureStarts = make(map[int]map[string]int)
		}
		next.structureStarts[index] = e.structureRemapStarts()
	}
	next.structureObjects = maps.Clone(base.structureObjects)
	if len(objects) != 0 && next.structureObjects == nil {
		next.structureObjects = make(map[string]map[string]bool)
	}
	for page, ids := range objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		known := make(map[string]bool, len(next.structureObjects[page])+len(ids))
		maps.Copy(known, next.structureObjects[page])
		maps.Copy(known, ids)
		next.structureObjects[page] = known
	}
	return e.commitAnnotationPartsContext(ctx, &next, parts)
}

// validateStructureReferencesContext 校验当前文档及版本的引用，遍历及快照读取期间响应取消
// 入参: ctx 取消上下文, root 标引树, tag 标引入口, r 可复用的编辑快照
// 返回: map[string]map[string]bool 已校验目标, error 引用或取消错误
func (e *Editor) validateStructureReferencesContext(ctx context.Context, root *StructureNode, tag CustomTag, r *Reader) (map[string]map[string]bool, error) {
	var refs []StructureReference
	var collect func(*StructureNode, bool) error
	collect = func(node *StructureNode, official bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		official = official && node.Namespace == root.Namespace
		if official && node.Name == "ObjectRef" && len(node.Children) != 0 {
			return fmt.Errorf("ObjectRef cannot contain child nodes")
		}
		refs = append(refs, node.References...)
		for i := range node.Children {
			if err := collect(&node.Children[i], official); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(root, structureOfficial(tag, root.Namespace) || structureInvoiceReferences(root)); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if r == nil {
		if fast, ok, err := e.validateStructureReferencesFast(ctx, refs); err != nil {
			return nil, err
		} else if ok {
			return fast, nil
		}
	}
	if r == nil {
		var err error
		r, err = e.Reader(ctx)
		if err != nil {
			return nil, err
		}
		defer r.Close()
	}
	doc, err := r.docContext(ctx)
	if err != nil {
		return nil, err
	}
	renderer := e.newRenderer(r)
	renderer.pageContext = ctx
	pages := signaturePageNumbers(doc)
	checked := make(map[int]map[string]bool)
	result := make(map[string]map[string]bool)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, _ := resourceValue(pages, ref.PageID)
		if page == 0 {
			return nil, fmt.Errorf("custom tag page not found: %s", ref.PageID)
		}
		objects := checked[page]
		if objects == nil {
			content, err := r.PageContentByIndexContext(ctx, page-1)
			if err != nil {
				return nil, err
			}
			visitor := &structureObjectVisitor{objects: make(map[string]bool)}
			if err := renderer.WalkPage(content, visitor); err != nil {
				return nil, err
			}
			objects = visitor.objects
			checked[page] = objects
		}
		for _, id := range ref.ObjectIDs {
			if !objects[editorResourceID(id)] {
				return nil, fmt.Errorf("custom tag object not found: %s/%s", ref.PageID, id)
			}
			key := editorResourceID(ref.PageID)
			if result[key] == nil {
				result[key] = make(map[string]bool)
			}
			result[key][editorResourceID(id)] = true
		}
	}
	return result, nil
}

// validateStructureReferencesFast 优先通过内存页对象索引校验引用，避免批量添加时重复打包整本快照
// 入参: ctx 取消上下文, refs 待校验引用集合
// 返回: map[string]map[string]bool 已校验目标, bool 是否全部命中内存快路径, error 取消错误
func (e *Editor) validateStructureReferencesFast(ctx context.Context, refs []StructureReference) (map[string]map[string]bool, bool, error) {
	pageMap := make(map[string]int, len(e.pages))
	for i := range e.pages {
		if id := editorResourceID(e.pages[i].ID); id != "" {
			pageMap[id] = i
		}
	}
	result := make(map[string]map[string]bool)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		pageKey := editorResourceID(ref.PageID)
		pageIdx, exists := pageMap[pageKey]
		if !exists {
			return nil, false, nil
		}
		content := &e.pages[pageIdx]
		if e.source != nil && e.source.pages[content.ID] != nil && e.source.pages[content.ID].original == nil {
			return nil, false, nil
		}
		positions := e.objectPositions(content)
		for _, id := range ref.ObjectIDs {
			objKey := editorResourceID(id)
			if objKey == "" {
				return nil, false, nil
			}
			_, found := positions[id]
			if !found && objKey != id {
				_, found = positions[objKey]
			}
			if !found {
				return nil, false, nil
			}
			if result[pageKey] == nil {
				result[pageKey] = make(map[string]bool)
			}
			result[pageKey][objKey] = true
		}
	}
	return result, true, nil
}
