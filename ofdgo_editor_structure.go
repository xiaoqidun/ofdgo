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

// pruneStructureReferences 迁移公文图元编号并清理已删除的引用，保留未知标引和原有悬空引用
// 改动进入输出快照，由既有文档及版本隔离流程维护共享文件
// 入参: ctx 取消上下文, parts 输出覆盖条目, generated 本次编码结果及扫描记录
// 返回: error 读取、解析或取消错误
func (e *Editor) pruneStructureReferences(ctx context.Context, parts map[string][]byte, generated map[string]editorGeneratedReferences) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.source.document.CustomTags.Path == "" && len(e.source.document.CustomTags.CustomTag) == 0 {
		return nil
	}
	tags, err := e.source.reader.CustomTagsContext(ctx)
	if err != nil {
		return err
	}
	knownTags, err := e.source.reader.structureReferenceTags(ctx, tags)
	if err != nil {
		return err
	}
	for i, tag := range tags {
		if tag.TypeID == textTagType && tag.NameSpace == textNamespace {
			knownTags[i] = false
		}
	}
	if !slices.Contains(knownTags, true) {
		return nil
	}
	pages := make(map[string]bool, len(e.pages))
	for _, page := range e.pages {
		pages[editorResourceID(page.ID)] = true
	}
	removedPages := make(map[string]bool)
	removedObjects := make(map[string]map[string]bool)
	pageRemaps := make(map[string]map[string][]string)
	remainingObjects := make(map[string]map[string]bool)
	var snapshot *Reader
	var snapshotPages map[string]Page
	sources := make(map[string]*editorSourcePage, len(e.source.pages)+len(e.source.structureObjects))
	for id, source := range e.source.pages {
		sources[editorResourceID(id)] = source
	}
	for id := range e.source.structureObjects {
		if _, ok := sources[id]; !ok {
			sources[id] = nil
		}
	}
	for key, source := range sources {
		if !pages[key] {
			removedPages[key] = true
			continue
		}
		known := e.source.structureObjects[key]
		changed := false
		if source != nil {
			_, changed = parts[e.source.reader.ResPath(source.ref.BaseLoc)]
			changed = changed && source.original != nil
		}
		if !changed && len(known) == 0 {
			continue
		}
		if snapshot == nil {
			reader := e.source.reader
			files := make(map[string][]byte, len(reader.files)+len(parts))
			maps.Copy(files, reader.files)
			maps.Copy(files, parts)
			snapshot = &Reader{Zip: reader.Zip, sourceFile: reader.sourceFile, files: files, encryption: e.encryption, documentIndex: reader.documentIndex, selectedVersion: reader.selectedVersion}
			if err := snapshot.initRootContext(ctx); err != nil {
				return err
			}
			doc, err := snapshot.docStructureContext(ctx)
			if err != nil {
				return err
			}
			snapshotPages = make(map[string]Page, len(doc.Pages.Page))
			for _, page := range doc.Pages.Page {
				snapshotPages[editorResourceID(page.ID)] = page
			}
		}
		original := make(map[string]bool)
		if source != nil {
			var err error
			original, err = structurePageObjectIDs(ctx, e.source.reader, source.ref)
			if err != nil {
				return err
			}
		}
		maps.Copy(original, known)
		page := snapshotPages[key]
		name := snapshot.ResPath(page.BaseLoc)
		scanned := generated[name]
		remaining := scanned.objects
		if remaining == nil || !scanned.matches(parts[name]) {
			var err error
			remaining, err = structurePageObjectIDs(ctx, snapshot, page)
			if err != nil {
				return err
			}
		} else if snapshot.doc.Annotations != "" {
			if _, err := snapshot.DocContext(ctx); err != nil {
				return err
			}
			annots, err := snapshot.pageAnnotationsContext(ctx, page.ID)
			if err != nil {
				return err
			}
			visitor := &structureObjectVisitor{objects: maps.Clone(remaining)}
			renderer := NewRenderer(snapshot)
			renderer.pageContext = ctx
			for _, annotation := range annots {
				if err := renderer.walkAnnotation(annotation, visitor); err != nil {
					return err
				}
			}
			remaining = visitor.objects
		}
		remainingObjects[key] = remaining
		links, err := structureRemapLinks(ctx, e.structureRemaps[key])
		if err != nil {
			return err
		}
		related := make(map[string][]string)
		for from, targets := range links {
			for _, to := range targets {
				if err := ctx.Err(); err != nil {
					return err
				}
				related[from] = append(related[from], to)
				related[to] = append(related[to], from)
			}
		}
		for id := range remaining {
			if len(related[id]) != 0 {
				original[id] = true
			}
		}
		pending := slices.Collect(maps.Keys(original))
		for len(pending) != 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			for _, next := range related[id] {
				if !original[next] {
					original[next] = true
					pending = append(pending, next)
				}
			}
		}
		for id := range original {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(links[id]) != 0 {
				if pageRemaps[key] == nil {
					pageRemaps[key] = make(map[string][]string)
				}
				pageRemaps[key][id] = links[id]
			}
			if !remaining[id] {
				if removedObjects[key] == nil {
					removedObjects[key] = make(map[string]bool)
				}
				removedObjects[key][id] = true
			}
		}
	}
	if len(removedPages) == 0 && len(removedObjects) == 0 && len(pageRemaps) == 0 {
		return nil
	}
	protected := make(map[string]bool)
	for index, tag := range tags {
		if !knownTags[index] {
			protected[e.source.reader.ResPath(tag.FileLoc)] = true
		}
	}
	isolated := make(map[string]string)
	originals := make(map[string][]byte)
	redirects := make(map[int]string)
	for index, tag := range tags {
		if !knownTags[index] {
			continue
		}
		name := e.source.reader.ResPath(tag.FileLoc)
		data, ok := originals[name]
		if !ok {
			data, ok = parts[name]
			if !ok {
				data, err = e.source.reader.readFileViewContext(ctx, name)
				if err != nil {
					return err
				}
			}
			originals[name] = data
		}
		remaps := pageRemaps
		if starts := e.source.structureStarts[index]; len(starts) != 0 {
			remaps = maps.Clone(pageRemaps)
			for page, start := range starts {
				links, err := structureRemapLinks(ctx, e.structureRemaps[page][start:])
				if err != nil {
					return err
				}
				remaps[page] = links
			}
		}
		mappedObjects := make(map[string]map[string][]string)
		updated, err := rewriteStructureReferences(ctx, data, tag, func(page string, ids []string) (string, []string, error) {
			key := editorResourceID(page)
			if removedPages[key] {
				return page, nil, nil
			}
			kept := make([]string, 0, len(ids))
			for _, id := range ids {
				canonical := editorResourceID(id)
				if len(remaps[key][canonical]) != 0 {
					mapped, ok := mappedObjects[key][canonical]
					if !ok {
						var err error
						mapped, err = structureMappedIDs(ctx, canonical, remaps[key], remainingObjects[key])
						if err != nil {
							return "", nil, err
						}
						if mappedObjects[key] == nil {
							mappedObjects[key] = make(map[string][]string)
						}
						mappedObjects[key][canonical] = mapped
					}
					kept = append(kept, mapped...)
				} else if !removedObjects[key][canonical] {
					kept = append(kept, id)
				}
			}
			return page, kept, nil
		})
		if err != nil {
			return err
		}
		if !bytes.Equal(data, updated) {
			if protected[name] {
				target := isolated[name]
				if target == "" {
					target = packageAvailableName(e.source.reader, parts, path.Join(e.packageDirectory(), "Tags", "Tag.xml"))
					isolated[name] = target
				}
				redirects[index] = target
				name = target
			}
			parts[name] = updated
		}
	}
	if len(redirects) != 0 {
		return e.redirectStructureEntries(parts, redirects)
	}
	return nil
}

// redirectStructureEntries 一次改写输出快照中的标引路径，保留同次页面及索引改动
// 入参: parts 输出覆盖条目, targets 标引入口序号及新内容路径
// 返回: error 索引读取或路径改写错误
func (e *Editor) redirectStructureEntries(parts map[string][]byte, targets map[int]string) error {
	name, data, _, entries, err := editorStructureIndex(e.source, parts)
	if err != nil {
		return err
	}
	patches := make([]editorXMLPatch, 0, len(targets))
	for index, target := range targets {
		if index < 0 || index >= len(entries) {
			return fmt.Errorf("custom tag structure not found: %d", index)
		}
		loc := entries[index].child("FileLoc")
		if loc == nil {
			return fmt.Errorf("custom tag requires FileLoc")
		}
		var value bytes.Buffer
		if err := xml.EscapeText(&value, []byte("/"+target)); err != nil {
			return err
		}
		patches = append(patches, editorXMLContent(data, loc, value.Bytes()))
	}
	parts[name] = editorPatchXML(data, patches)
	return nil
}

// remapStructureObjects 将图元修改和标引编号迁移作为一次操作提交，映射只作用于当前页
// 入参: page 页面索引, remaps 按修改顺序记录的编号映射, edit 图元修改
// 返回: error 图元修改错误
func (e *Editor) remapStructureObjects(page int, remaps []map[string]string, edit func() error) error {
	if len(remaps) == 0 || e.source == nil || e.source.document.CustomTags.Path == "" && len(e.source.document.CustomTags.CustomTag) == 0 {
		return edit()
	}
	return e.Transaction(func(editor *Editor) error {
		if err := edit(); err != nil {
			return err
		}
		if editor.structureRemaps == nil {
			editor.structureRemaps = make(map[string][]map[string]string)
		}
		key := editorResourceID(editor.pages[page].ID)
		next := slices.Clone(editor.structureRemaps[key])
		for _, ids := range remaps {
			if len(ids) != 0 {
				next = append(next, maps.Clone(ids))
			}
		}
		editor.structureRemaps[key] = next
		return nil
	})
}

// structureRemapStarts 记录各页面已应用的编号迁移位置，不与后续编辑共享可变状态
// 返回: map[string]int 页面编号及已应用的迁移记录数
func (e *Editor) structureRemapStarts() map[string]int {
	starts := make(map[string]int, len(e.structureRemaps))
	for page, remaps := range e.structureRemaps {
		starts[page] = len(remaps)
	}
	return starts
}

// annotationStructureSource 记录被修改注释原有的外观图元，供保存时清理失效标引
// 入参: page 页面索引, wanted 待修改注释标识
// 返回: *editorSource 保留已知图元的包快照, error 读取或遍历错误
func (e *Editor) annotationStructureSource(page int, wanted map[string]bool) (*editorSource, error) {
	base := e.source
	if base.document.CustomTags.Path == "" && len(base.document.CustomTags.CustomTag) == 0 {
		return base, nil
	}
	tags, err := base.reader.CustomTags()
	if err != nil {
		return nil, err
	}
	knownTags, err := base.reader.structureReferenceTags(context.Background(), tags)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(knownTags, true) {
		return base, nil
	}
	if _, err := base.reader.Doc(); err != nil {
		return nil, err
	}
	if source := base.pages[e.pages[page].ID]; source != nil {
		if _, err := base.reader.PageContent(source.ref); err != nil {
			return nil, err
		}
	}
	annots, err := base.reader.pageAnnotationsContext(context.Background(), e.pages[page].ID)
	if err != nil {
		return nil, err
	}
	visitor := &structureObjectVisitor{objects: make(map[string]bool)}
	renderer := NewRenderer(base.reader)
	for _, annotation := range annots {
		if wanted[editorResourceID(annotation.ID)] {
			if err := renderer.walkAnnotation(annotation, visitor); err != nil {
				return nil, err
			}
		}
	}
	next := *base
	next.structureObjects = maps.Clone(base.structureObjects)
	if next.structureObjects == nil {
		next.structureObjects = make(map[string]map[string]bool)
	}
	key := editorResourceID(e.pages[page].ID)
	maps.Copy(visitor.objects, next.structureObjects[key])
	next.structureObjects[key] = visitor.objects
	return &next, nil
}

// structures 迁移所选页面的公文标引，按已迁移图元映射引用，不猜测其他行业的引用规则
// 返回: []byte 标引索引条目, error 读取、解析或取消错误
func (m *editorPageImport) structures() ([]byte, error) {
	tags, err := m.reader.CustomTagsContext(m.ctx)
	if err != nil {
		return nil, err
	}
	knownTags, err := m.reader.structureReferenceTags(m.ctx, tags)
	if err != nil {
		return nil, err
	}
	var entries []byte
	for i, tag := range tags {
		if !knownTags[i] {
			continue
		}
		data, err := m.readFile(m.reader.ResPath(tag.FileLoc))
		if err != nil {
			return nil, err
		}
		count := 0
		updated, err := rewriteStructureReferences(m.ctx, data, tag, func(page string, ids []string) (string, []string, error) {
			key := editorImportKey(page)
			if !m.pages[key] {
				if _, ok := m.templateParts[key]; !ok {
					return page, nil, nil
				}
			}
			var mapped []string
			for _, id := range ids {
				if value := m.ids[editorImportKey(id)]; value != "" {
					mapped = append(mapped, value)
				}
			}
			count += len(mapped)
			return m.ids[key], mapped, nil
		})
		if err != nil {
			return nil, err
		}
		if count == 0 {
			continue
		}
		name := packageAvailableName(m.target, m.files, path.Join(m.directory, "Tags", fmt.Sprintf("Tag_%d.xml", i)))
		m.files[name] = updated
		var content []byte
		if tag.SchemaLoc != "" {
			schema, err := m.copyData(m.reader.ResPath(tag.SchemaLoc), path.Join(m.directory, "Tags", path.Base(tag.SchemaLoc)), false)
			if err != nil {
				return nil, err
			}
			content = append(content, editorXMLText("SchemaLoc", "/"+strings.TrimPrefix(schema, "/"))...)
		}
		content = append(content, editorXMLText("FileLoc", "/"+name)...)
		var attrs ofdAttrs
		attrs.add("TypeID", tag.TypeID)
		attrs.add("NameSpace", tag.NameSpace)
		entry, err := editorXMLContainer("CustomTag", attrs, content)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry...)
	}
	return entries, nil
}

// structureRemapLinks 合并按操作顺序记录的编号迁移，保留一对多关系并排除重复边
// 入参: ctx 取消上下文, remaps 图元编号迁移记录
// 返回: map[string][]string 原编号及其迁移目标, error 取消错误
func structureRemapLinks(ctx context.Context, remaps []map[string]string) (map[string][]string, error) {
	links := make(map[string][]string)
	seen := make(map[[2]string]bool)
	for _, ids := range remaps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for from, to := range ids {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key := [2]string{from, to}
			if from == to || seen[key] {
				continue
			}
			seen[key] = true
			links[from] = append(links[from], to)
		}
	}
	return links, ctx.Err()
}

// structureMappedIDs 沿编号迁移关系收集当前仍可达的图元，同一来源的结果不重复
// 入参: ctx 取消上下文, id 原编号, links 编号迁移关系, remaining 当前页面可达图元
// 返回: []string 保留的原编号及迁移后的编号, error 取消错误
func structureMappedIDs(ctx context.Context, id string, links map[string][]string, remaining map[string]bool) ([]string, error) {
	var result []string
	pending := []string{id}
	seen := make(map[string]bool)
	for len(pending) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id = pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		if remaining[id] {
			result = append(result, id)
		}
		for i := len(links[id]) - 1; i >= 0; i-- {
			pending = append(pending, links[id][i])
		}
	}
	return result, nil
}

// structurePageObjectIDs 收集页面、模板、复合资源及注释外观中的图元标识，不加载字体或图像
// 入参: ctx 取消上下文, reader 资源已加载的阅读器, page 页面引用
// 返回: map[string]bool 可达图元标识, error 页面读取或遍历错误
func structurePageObjectIDs(ctx context.Context, reader *Reader, page Page) (map[string]bool, error) {
	if _, err := reader.DocContext(ctx); err != nil {
		return nil, err
	}
	content, err := reader.PageContentContext(ctx, page)
	if err != nil {
		return nil, err
	}
	renderer := NewRenderer(reader, WithAnnotations(true))
	renderer.pageContext = ctx
	visitor := &structureObjectVisitor{objects: make(map[string]bool)}
	if err := renderer.WalkPage(content, visitor); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return visitor.objects, nil
}

// rewriteStructureReferences 仅改写已识别公文命名空间的叶子引用，保留其他XML字节
// 入参: ctx 取消上下文, data 原始标引, tag 标引入口, rewrite 引用映射，空标识列表表示删除引用
// 返回: []byte 修改后的标引, error 解析或取消错误
func rewriteStructureReferences(ctx context.Context, data []byte, tag CustomTag, rewrite func(string, []string) (string, []string, error)) ([]byte, error) {
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	if !structureOfficial(tag, root.name.Space) {
		structure, err := parseStructure(ctx, data, tag)
		if err != nil {
			return nil, err
		}
		if !structureInvoiceReferences(structure) {
			return data, nil
		}
	}
	var patches []editorXMLPatch
	var walk func(*editorXML, int) error
	walk = func(node *editorXML, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 128 {
			return fmt.Errorf("custom tag structure limit exceeded")
		}
		if node.name.Space != root.name.Space {
			return nil
		}
		if node.name.Local == "ObjectRef" && len(node.children) == 0 {
			value := strings.TrimSpace(editorImportText(data, node))
			ids := structureObjectIDs(value)
			if len(ids) == 0 {
				return nil
			}
			page, next, err := rewrite(node.attr("PageRef"), ids)
			if err != nil {
				return err
			}
			if len(next) == 0 {
				patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
				return nil
			}
			if strings.Join(next, ",") != strings.Join(ids, ",") {
				var text bytes.Buffer
				_ = xml.EscapeText(&text, []byte(strings.Join(next, ",")))
				patches = append(patches, editorXMLContent(data, node, text.Bytes()))
			}
			if page != node.attr("PageRef") {
				patch, err := editorXMLAttributePatch(data, node, "PageRef", page)
				if err != nil {
					return err
				}
				patches = append(patches, patch)
			}
			return nil
		}
		for _, child := range node.children {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, 1); err != nil {
		return nil, err
	}
	if len(patches) == 0 {
		return data, nil
	}
	return editorPatchXML(data, patches), nil
}
