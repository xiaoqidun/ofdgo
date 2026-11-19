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
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// PageImportOptions 页面导入选项，零值仅导入页面及其关联资源
// OnProgress按ids、pages、resources、commit阶段回报进度，total为0表示总量未知
// 回调返回错误则取消导入，commit检查点之后统一提交；回调不可重入修改编辑器
// AllowDecrypted显式允许将加密来源导入未加密目标，不影响原文件
type PageImportOptions struct {
	Outlines       bool
	AllowDecrypted bool
	OnProgress     func(stage string, completed, total int) error
}

// ObjectImportOptions 跨文档对象迁移选项，位移单位为毫米
// AllowDecrypted显式允许将加密来源导入未加密目标，进度回调返回错误时撤销整个操作
type ObjectImportOptions struct {
	DX, DY         float64
	AllowDecrypted bool
	OnProgress     func(stage string, completed, total int) error
}

// ImportObjects 迁移选区及引用资源，不改变来源与目标页面结构，提交一次撤销记录
// 保留对象原文与图层继承，同页链接映射到目标页，无法迁移的跨页链接明确报错
// 返回后可关闭来源阅读器，后续修改来源不影响副本
// 入参: source 来源编辑器, sourcePage 来源页, ids 对象标识, page 目标页, options 迁移选项
// 返回: []string 新对象标识, error 错误信息
func (e *Editor) ImportObjects(source *Editor, sourcePage int, ids []string, page int, options ObjectImportOptions) ([]string, error) {
	return e.ImportObjectsContext(context.Background(), source, sourcePage, ids, page, options)
}

// ImportObjectsContext 迁移对象及资源，取消时不提交修改或撤销记录
// 入参: ctx 取消上下文, source 来源编辑器, sourcePage 来源页, ids 对象标识, page 目标页, options 迁移选项
// 返回: []string 新对象标识, error 校验、迁移或取消错误
func (e *Editor) ImportObjectsContext(ctx context.Context, source *Editor, sourcePage int, ids []string, page int, options ObjectImportOptions) ([]string, error) {
	objects, _, err := e.importSelection(ctx, source, sourcePage, ids, nil, page, options, true)
	return objects, err
}

// ImportSelection 导入独立单页选区包，重映射对象和资源标识，一次撤销恢复全部
// 入参: ctx 取消上下文, source 单页选区阅读器, page 目标页, options 位移及导入配置
// 返回: []string 正文标识, []string 注解标识, error 校验或迁移错误
func (e *Editor) ImportSelection(ctx context.Context, source *Reader, page int, options ObjectImportOptions) ([]string, []string, error) {
	doc, err := source.DocContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(doc.Pages.Page) != 1 {
		return nil, nil, fmt.Errorf("selection requires exactly one page")
	}
	origin, err := source.EditorContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	content, err := origin.page(0)
	if err != nil {
		return nil, nil, err
	}
	var objects, annotations []string
	for _, layer := range content.Content.Layer {
		for _, object := range layer.Objects {
			objects = append(objects, editorObjectID(object))
		}
	}
	notes, err := origin.Annotations(0)
	if err != nil {
		return nil, nil, err
	}
	for _, note := range notes {
		annotations = append(annotations, note.ID)
	}
	return e.importSelection(ctx, origin, 0, objects, annotations, page, options, true)
}

// ImportPages 将来源当前文档中的指定页面插入目标位置，保持输入顺序并作为一次撤销操作
// 页面索引从0开始，at可等于当前页数；复制关联模板、注释、签章外观和实际引用的资源
// 不导入来源元数据和目录，指向未选页面的跳转被移除；签名数据仅保留原始凭据，不代表合并后文档有效
// 目标文档含版本列表时暂不支持导入带签章页面，返回错误且不修改目标
// 返回后可关闭来源Reader，目标原Reader的生命周期要求不变
// 入参: source 来源阅读器, indexes 来源页面索引，不可重复, at 目标插入位置
// 返回: []string 新页面标识, error 错误信息
func (e *Editor) ImportPages(source *Reader, indexes []int, at int) ([]string, error) {
	return e.ImportPagesWithOptions(source, indexes, at, PageImportOptions{})
}

// ImportPagesWithOptions 按选项导入页面，页面与目录作为一次原子操作提交
// Outlines启用时追加指向所选页面的目录及其祖先，保留层级和跳转坐标，不导入无关目录
// 其他行为与ImportPages相同，不迁移来源元数据
// 入参: source 来源阅读器, indexes 来源页面索引, at 目标插入位置, options 导入选项
// 返回: []string 新页面标识, error 错误信息
func (e *Editor) ImportPagesWithOptions(source *Reader, indexes []int, at int, options PageImportOptions) ([]string, error) {
	return e.ImportPagesContext(context.Background(), source, indexes, at, options)
}

// ImportPagesContext 导入页面及关联资源，取消检查覆盖共享资源隔离和版本清单更新
// 取消时不提交页面、目录或撤销记录，其他行为与ImportPagesWithOptions相同
// 入参: ctx 取消上下文, source 来源阅读器, indexes 来源页索引, at 插入位置, options 导入选项
// 返回: []string 新页面标识, error 校验、导入或取消错误
func (e *Editor) ImportPagesContext(ctx context.Context, source *Reader, indexes []int, at int, options PageImportOptions) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source.encryption != nil && e.encryption == nil && !options.AllowDecrypted {
		return nil, ErrEncryptionPolicyRequired
	}
	return e.importPages(ctx, source, indexes, at, false, options)
}

// editorImportEntry 资源或关联索引节点及其来源
type editorImportEntry struct {
	name, base, group string
	data              []byte
	node              *editorXML
	template          bool
	ids               map[string]string
	scope             []string
}

// editorPageImport 一次插页操作的独立资源图，提交前不修改目标文档
type editorPageImport struct {
	ctx                context.Context
	reader             *Reader
	doc                *Document
	directory          string
	target             *Reader
	xmlPaths           map[string]string
	dataPaths          map[string]string
	templatePaths      map[string]string
	evidence           map[string]string
	resourceXML        []byte
	pageParts          []byte
	structureXML       []byte
	documentXML        []byte
	documentRoot       *editorXML
	docRefs            map[string]map[string]*editorXML
	maximum            uint64
	ids                map[string]string
	resIDs             map[string]string
	pages              map[string]bool
	files              map[string][]byte
	resByFile          map[string]map[string]editorImportEntry
	resourceFiles      map[string]bool
	docRes             map[string]bool
	docScope           []string
	pageScopes         map[string][]string
	pageScopesByPath   map[string][]string
	used               map[string][]byte
	usedGroups         map[string]string
	templates          map[string]TemplatePage
	templateParts      map[string][]byte
	defaultCS          string
	attachments        map[string][]byte
	attachmentMap      map[string]editorImportEntry
	attachmentIDs      map[string]bool
	removedAttachments map[string]bool
	bookmarks          map[string]Dest
	signatureIDs       *signatureIDSequence
	copyPage           bool
	strictLinks        bool
	progress           editorProgress
}

// encodeResources 编码本次迁移实际引用的资源，不包含未使用的资源定义
// 返回: error 编码错误
func (m *editorPageImport) encodeResources() error {
	var content []byte
	ids := slices.Sorted(maps.Keys(m.used))
	for _, group := range []string{"ColorSpaces", "DrawParams", "Fonts", "MultiMedias", "CompositeGraphicUnits"} {
		var entries []byte
		for _, id := range ids {
			if err := m.ctx.Err(); err != nil {
				return err
			}
			if m.usedGroups[id] == group {
				entries = append(entries, m.used[id]...)
			}
		}
		if len(entries) == 0 {
			continue
		}
		encoded, err := editorXMLContainer(group, nil, entries)
		if err != nil {
			return err
		}
		content = append(content, encoded...)
	}
	if len(content) == 0 {
		return nil
	}
	var err error
	m.resourceXML, err = editorXMLContainer("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}}, content)
	return err
}

// readFile 分块读取导入条目，允许资源解压过程中取消，不共享来源内存
// 入参: name 包内路径
// 返回: []byte 条目内容, error 错误信息
func (m *editorPageImport) readFile(name string) ([]byte, error) {
	input, err := m.reader.openFile(name)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	return io.ReadAll(&editorProgressReader{Reader: input, progress: m.progress, stage: "resources"})
}

// outlines 筛选指向导入页面的目录树并复用标准动作迁移
// 返回: []byte 目录片段, error 错误信息
func (m *editorPageImport) outlines() ([]byte, error) {
	name := m.reader.documentRoot
	data, err := m.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(m.ctx, data)
	if err != nil {
		return nil, err
	}
	root = root.child("Outlines")
	if root == nil {
		return nil, nil
	}
	var patches []editorXMLPatch
	var keep func(*editorXML) (bool, error)
	keep = func(node *editorXML) (bool, error) {
		if err := m.ctx.Err(); err != nil {
			return false, err
		}
		start, selected := len(patches), false
		if actions := node.child("Actions"); actions != nil {
			var value struct {
				Action []Action `xml:"Action"`
			}
			if err := xml.Unmarshal(data[actions.start:actions.end], &value); err != nil {
				return false, err
			}
			for _, action := range value.Action {
				if action.Goto != nil {
					if dest := gotoDest(action.Goto, m.bookmarks); dest != nil && m.pages[editorImportKey(dest.PageID)] {
						selected = true
					}
				}
			}
		}
		for _, child := range editorOutlineChildren(node) {
			retained, err := keep(child)
			if err != nil {
				return false, err
			}
			selected = selected || retained
		}
		if !selected {
			patches = append(patches[:start], editorXMLPatch{node.start, node.end, nil})
		}
		return selected, nil
	}
	for _, node := range editorOutlineChildren(root) {
		if _, err := keep(node); err != nil {
			return nil, err
		}
	}
	data = editorPatchXML(data, patches)
	root, err = parseEditorXMLContext(m.ctx, data)
	if err != nil {
		return nil, err
	}
	root = root.child("Outlines")
	if len(editorOutlineChildren(root)) == 0 {
		return nil, nil
	}
	var validate func(*editorXML) error
	validate = func(node *editorXML) error {
		if err := m.ctx.Err(); err != nil {
			return err
		}
		for _, attr := range node.attrs {
			if attr.Name.Space == "" || attr.Name.Space == "xmlns" {
				continue
			}
			if attr.Name.Space == XMLSchemaInstanceNamespace && (attr.Name.Local == "schemaLocation" || attr.Name.Local == "noNamespaceSchemaLocation") {
				continue
			}
			return fmt.Errorf("cannot remap outline extension attribute %q", attr.Name.Local)
		}
		for _, child := range node.children {
			if err := validate(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validate(root); err != nil {
		return nil, err
	}
	return m.encode(editorImportEntry{name: name, data: data}, root)
}

// id 为数值相等的来源标识分配同一新标识，保留原写法供编辑状态迁移
// 入参: value 来源标识
// 返回: string 新标识
func (m *editorPageImport) id(value string) string {
	key := editorImportKey(value)
	if id := m.ids[key]; id != "" {
		m.ids[value] = id
		return id
	}
	m.maximum++
	id := formatUnitID(m.maximum)
	m.ids[key], m.ids[value] = id, id
	return id
}

// resourceIndex 每个索引只登记一次，拒绝同一文件内的重复资源标识，按引用延迟复制资源内容
// 入参: name 资源索引路径
// 返回: error 错误信息
func (m *editorPageImport) resourceIndex(name string) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	name = cleanPackagePath(name)
	if m.resourceFiles[name] {
		return nil
	}
	data, err := m.readFile(name)
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(m.ctx, data)
	if err != nil {
		return err
	}
	if root.name.Local != "Res" {
		return fmt.Errorf("invalid resource index %q", name)
	}
	entries := make(map[string]editorImportEntry)
	for _, group := range root.children {
		for _, node := range group.children {
			if err := m.ctx.Err(); err != nil {
				return err
			}
			id := editorImportKey(node.attr("ID"))
			if id == "" {
				return fmt.Errorf("missing resource ID in %s", name)
			}
			if _, exists := entries[id]; exists {
				return fmt.Errorf("duplicate resource ID %q in %s", id, name)
			}
			entries[id] = editorImportEntry{name: name, base: root.attr("BaseLoc"), group: group.name.Local, data: data, node: node}
		}
	}
	m.resByFile[name] = entries
	if m.resourceFiles == nil {
		m.resourceFiles = make(map[string]bool)
	}
	m.resourceFiles[name] = true
	return nil
}

// lookupResource 按可见资源作用域链查找资源声明
// 入参: scope 资源文件优先级列表, value 来源资源标识
// 返回: editorImportEntry 资源条目, bool 是否找到
func (m *editorPageImport) lookupResource(scope []string, value string) (editorImportEntry, bool) {
	if len(scope) == 0 {
		scope = m.docScope
	}
	key := editorImportKey(value)
	for _, resFile := range scope {
		if entry, ok := m.resByFile[resFile][key]; ok {
			return entry, true
		}
	}
	return editorImportEntry{}, false
}

// resourceTargetID 按资源所属文件与标识分配独立目标标识
// 入参: entry 来源资源条目, key 规范化标识
// 返回: string 目标资源标识
func (m *editorPageImport) resourceTargetID(entry editorImportEntry, key string) string {
	if m.copyPage {
		return entry.node.attr("ID")
	}
	resKey := entry.name + "\x00" + key
	if id := m.resIDs[resKey]; id != "" {
		return id
	}
	m.maximum++
	id := formatUnitID(m.maximum)
	m.resIDs[resKey] = id
	return id
}

// resolvePageScope 按页面自身局部资源声明登记索引并拼装完整页面作用域链
// 入参: pageName 页面路径, pageRes 页面PageRes声明列表
// 返回: []string 按优先级排列的资源文件列表, error 读取或解析错误
func (m *editorPageImport) resolvePageScope(pageName string, pageRes []string) ([]string, error) {
	var local []string
	for _, value := range pageRes {
		loc, err := m.resolve(pageName, "", strings.TrimSpace(value))
		if err != nil {
			return nil, err
		}
		if err := m.resourceIndex(loc); err != nil {
			return nil, err
		}
		local = append(local, cleanPackagePath(loc))
	}
	var scope []string
	for _, res := range slices.Backward(local) {
		scope = appendUniquePath(scope, res)
	}
	for _, dRes := range m.docScope {
		scope = appendUniquePath(scope, dRes)
	}
	return scope, nil
}

// resource 按当前作用域与引用用途校验资源类型并迁移，避免生成悬空或类型不符的引用
// 入参: scope 当前可见资源作用域, value 来源资源标识, kind 资源类型或多媒体用途，空值仅检查资源是否存在
// 返回: string 新标识, error 资源缺失、类型或迁移错误
func (m *editorPageImport) resource(scope []string, value, kind string) (string, error) {
	if err := m.ctx.Err(); err != nil {
		return "", err
	}
	entry, exists := m.lookupResource(scope, value)
	if !exists {
		return "", fmt.Errorf("resource %q not found", value)
	}
	if kind != "" {
		expected, media := kind, ""
		if kind == "Image" || kind == "Audio" || kind == "Video" {
			kind, media = "MultiMedia", kind
		}
		if entry.group != kind+"s" || entry.node == nil || !entry.node.matchesOFD(kind) || media != "" && entry.node.attr("Type") != media {
			return "", fmt.Errorf("invalid resource type for %q: expected %s", value, expected)
		}
	}
	if m.copyPage {
		return entry.node.attr("ID"), nil
	}
	key := editorImportKey(value)
	resKey := entry.name + "\x00" + key
	targetID := m.resourceTargetID(entry, key)
	if data, seen := m.used[resKey]; seen && data == nil {
		return "", fmt.Errorf("cyclic resource reference %q", value)
	} else if !seen {
		m.used[resKey] = nil
		m.usedGroups[resKey] = entry.group
		resEntry := entry
		resEntry.ids = map[string]string{key: targetID}
		if m.docRes[entry.name] {
			resEntry.scope = m.docScope
		} else if len(scope) > 0 {
			resEntry.scope = scope
		} else {
			resEntry.scope = append([]string{entry.name}, m.docScope...)
		}
		data, err := m.encode(resEntry, entry.node)
		if err != nil {
			return "", err
		}
		m.used[resKey] = data
	}
	return targetID, nil
}

// reference 复制被引用的模板并映射页面或对象标识
// 入参: value 来源标识
// 返回: string 新标识, error 错误信息
func (m *editorPageImport) reference(value string) (string, error) {
	key := editorImportKey(value)
	if m.copyPage {
		if template, ok := m.templates[key]; ok {
			return template.ID, nil
		}
		return m.id(value), nil
	}
	if template, ok := m.templates[key]; ok {
		if _, seen := m.templateParts[key]; !seen {
			m.templateParts[key] = nil
			name, err := m.copyXML(m.reader.ResPath(template.BaseLoc), path.Join(m.directory, "Templates", "Template_"+m.id(value), "Content.xml"), true, nil)
			if err != nil {
				return "", err
			}
			data, err := m.documentReference("CommonData", "TemplatePage", value, m.id(value), name)
			if err != nil {
				return "", err
			}
			m.templateParts[key] = bytes.TrimPrefix(data, []byte(xml.Header))
		}
	}
	return m.id(value), nil
}

// documentReference 迁移页面或模板引用，只替换标识与路径并保留未知属性
// 入参: group 父节点名称, kind 引用节点名称, source 原标识, id 新标识, loc 新路径
// 返回: []byte 引用XML, error 错误信息
func (m *editorPageImport) documentReference(group, kind, source, id, loc string) ([]byte, error) {
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}
	nodes := m.docRefs[kind]
	if nodes == nil {
		nodes = make(map[string]*editorXML)
		if parent := m.documentRoot.child(group); parent != nil {
			for _, node := range parent.children {
				if err := m.ctx.Err(); err != nil {
					return nil, err
				}
				if !node.matchesOFD(kind) {
					continue
				}
				key := editorImportKey(node.attr("ID"))
				if nodes[key] != nil {
					return nil, fmt.Errorf("duplicate %s reference %q", kind, key)
				}
				nodes[key] = node
			}
		}
		if m.docRefs == nil {
			m.docRefs = make(map[string]map[string]*editorXML)
		}
		m.docRefs[kind] = nodes
	}
	node := nodes[editorImportKey(source)]
	if node == nil {
		return nil, fmt.Errorf("missing %s reference %q", kind, source)
	}
	data, err := editorXMLStandalone(m.documentXML[node.start:node.end], node)
	if err != nil {
		return nil, err
	}
	for _, attr := range [][2]string{{"ID", id}, {"BaseLoc", loc}} {
		root, err := parseEditorXMLContext(m.ctx, data)
		if err != nil {
			return nil, err
		}
		patch, err := editorXMLAttributePatch(data, root, attr[0], attr[1])
		if err != nil {
			return nil, err
		}
		data = editorPatchXML(data, []editorXMLPatch{patch})
	}
	return data, nil
}

// resolve 按所在XML和资源基准目录解析包内路径
// 入参: name 所在XML, base 资源目录, value 路径值
// 返回: string 包内路径, error 错误信息
func (m *editorPageImport) resolve(name, base, value string) (string, error) {
	candidate := resolveResourcePath(name, base, value)
	if _, ok := m.reader.files[candidate]; ok {
		return candidate, nil
	}
	if file, ok := m.reader.packageFile(candidate); ok {
		return cleanPackagePath(file.Name), nil
	}
	if m.reader.encryptedSource().has(candidate) {
		return candidate, nil
	}
	return "", fmt.Errorf("import reference %q in %s not found", value, name)
}

// copyData 复制原始数据，资源内容、签名凭据与重写XML使用独立路径映射
// 入参: name 来源路径, relative 文档内候选路径, evidence 是否原始签名凭据
// 返回: string 新路径, error 错误信息
func (m *editorPageImport) copyData(name, relative string, evidence bool) (string, error) {
	paths := m.dataPaths
	if evidence {
		paths = m.evidence
		relative = path.Join("Signs", "Signed", path.Base(name))
	}
	key := cleanPackagePath(name)
	if target := paths[key]; target != "" {
		return "/" + target, nil
	}
	if m.copyPage && !evidence {
		return "/" + cleanPackagePath(name), nil
	}
	target := packageAvailableName(m.target, m.files, path.Join(m.directory, relative))
	data, err := m.readFile(name)
	if err != nil {
		return "", err
	}
	paths[key], m.files[target] = target, data
	return "/" + target, nil
}

// copyXML 复制页面或标准关联XML并重映射引用
// 入参: name 来源路径, candidate 目标候选路径, template 是否按模板页复制, parentScope 外部继承的资源作用域
// 返回: string 新路径, error 错误信息
func (m *editorPageImport) copyXML(name, candidate string, template bool, parentScope []string) (string, error) {
	paths := m.xmlPaths
	if template {
		if m.templatePaths == nil {
			m.templatePaths = make(map[string]string)
		}
		paths = m.templatePaths
	}
	key := cleanPackagePath(name)
	if target := paths[key]; target != "" {
		return "/" + target, nil
	}
	target := packageAvailableName(m.target, m.files, candidate)
	data, err := m.readFile(name)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXMLContext(m.ctx, data)
	if err != nil {
		return "", err
	}
	paths[key], m.files[target] = target, nil
	scope := parentScope
	if root.name.Local == "Page" {
		scope, err = m.resolvePageScope(name, collectXMLPageRes(data, root))
		if err != nil {
			return "", err
		}
		if !template {
			m.pageScopesByPath[key] = scope
		}
	}
	entry := editorImportEntry{name: name, data: data, template: template, scope: scope}
	if template && slices.ContainsFunc(m.doc.Pages.Page, func(page Page) bool {
		return cleanPackagePath(m.reader.ResPath(page.BaseLoc)) == key
	}) {
		entry.ids = make(map[string]string)
		m.templateObjectIDs(root, entry.ids)
	}
	encoded, err := m.encode(entry, root)
	if err != nil {
		return "", err
	}
	m.files[target] = append([]byte(xml.Header), encoded...)
	return "/" + target, nil
}

// templateObjectIDs 为兼作普通页的模板内容分配独立对象标识
// 入参: node 当前节点, ids 模板副本的标识映射
func (m *editorPageImport) templateObjectIDs(node *editorXML, ids map[string]string) {
	if value := node.attr("ID"); value != "" {
		key := editorImportKey(value)
		if ids[key] == "" {
			m.maximum++
			ids[key] = formatUnitID(m.maximum)
		}
	}
	for _, child := range node.children {
		m.templateObjectIDs(child, ids)
	}
}

// encode 编码标准OFD节点，重映射引用并将旧式裁剪换算到标准坐标
// 入参: entry 来源上下文, node 当前节点
// 返回: []byte 独立XML片段, error 错误信息
func (m *editorPageImport) encode(entry editorImportEntry, node *editorXML) ([]byte, error) {
	if m.ctx != nil {
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}
	}
	if classifyOFDNamespace(node.name.Space) == ofdXMLUnknown {
		return nil, fmt.Errorf("cannot remap extension namespace %q", node.name.Space)
	}
	name := node.name.Local
	if name == "PageRes" {
		if m.copyPage {
			loc, err := m.resolve(entry.name, "", strings.TrimSpace(editorImportText(entry.data, node)))
			if err != nil {
				return nil, err
			}
			return editorXMLText("PageRes", "/"+loc), nil
		}
		return nil, nil
	}
	if name == "StampAnnot" && !m.pages[editorImportKey(node.attr("PageRef"))] {
		return nil, nil
	}
	if name == "Action" {
		if gotoNode := node.child("Goto"); gotoNode != nil {
			var action Goto
			if err := xml.Unmarshal(entry.data[gotoNode.start:gotoNode.end], &action); err != nil {
				return nil, err
			}
			if dest := gotoDest(&action, m.bookmarks); !m.copyPage && (dest == nil || !m.pages[editorImportKey(dest.PageID)]) {
				if m.strictLinks {
					return nil, fmt.Errorf("object link targets a page outside the imported selection")
				}
				return nil, nil
			}
		}
	}
	var attrs ofdAttrs
	for _, attr := range node.attrs {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
			continue
		}
		if attr.Name.Space != "" {
			attrs = append(attrs, attr)
			continue
		}
		key, value := attr.Name.Local, attr.Value
		if name == "Clips" && key == "TransFlag" {
			continue
		}
		var err error
		switch key {
		case "ID":
			if id := entry.ids[editorImportKey(value)]; id != "" && (entry.node == nil || node == entry.node) {
				value = id
			} else if name == "Attachment" {
				value, err = m.attachmentID(value)
			} else if name == "Signature" || name == "StampAnnot" {
				value, err = m.signatureID(entry.name, name, value)
			} else {
				value = m.id(value)
			}
		case "PageID":
			if !m.copyPage || m.pages[editorImportKey(value)] {
				value, err = m.reference(value)
			} else if id := m.ids[editorImportKey(value)]; id != "" {
				value = id
			}
		case "Font", "DrawParam", "ColorSpace":
			value, err = m.resource(entry.scope, value, key)
		case "Relative":
			value, err = m.resource(entry.scope, value, "DrawParam")
		case "Substitution", "ImageMask", "Thumbnail":
			value, err = m.resource(entry.scope, value, "Image")
		case "ResourceID":
			kind := ""
			switch name {
			case "CompositeObject":
				kind = "CompositeGraphicUnit"
			case "ImageObject":
				kind = "Image"
			case "Sound":
				kind = "Audio"
			case "Movie":
				kind = "Video"
			}
			value, err = m.resource(entry.scope, value, kind)
		case "TemplateID":
			if _, exists := m.templates[editorImportKey(value)]; !exists {
				return nil, fmt.Errorf("template %q not found", value)
			}
			value, err = m.reference(value)
		case "PageRef", "RefId":
			value, err = m.reference(value)
		case "BaseLoc", "FileRef":
			loc, resolveErr := m.resolve(entry.name, entry.base, value)
			if resolveErr != nil {
				return nil, resolveErr
			}
			if key == "FileRef" {
				value, err = m.copyData(loc, "", true)
			} else {
				directory := "Res"
				if name == "Signature" {
					id, idErr := m.signatureID(entry.name, name, node.attr("ID"))
					if idErr != nil {
						return nil, idErr
					}
					directory = path.Join("Signs", "Sign_"+id)
				}
				value, err = m.copyXML(loc, path.Join(m.directory, directory, path.Base(loc)), false, entry.scope)
			}
		case "AttachID":
			value, err = m.attachment(value)
		case "Profile":
			if name == "ColorSpace" {
				loc, resolveErr := m.resolve(entry.name, entry.base, strings.TrimSpace(value))
				if resolveErr != nil {
					return nil, resolveErr
				}
				value, err = m.copyData(loc, path.Join("Res/ColorSpaces", path.Base(loc)), false)
			}
		}
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: key}, Value: value})
	}
	if name == "Area" && node.parent != nil && node.parent.name.Local == "Clip" && node.parent.parent != nil && node.parent.parent.name.Local == "Clips" {
		clips := node.parent.parent
		transformed := classifyOFDNamespace(clips.name.Space) == ofdXMLLegacy
		if clips.hasAttr("TransFlag") {
			var err error
			transformed, err = strconv.ParseBool(clips.attr("TransFlag"))
			if err != nil {
				return nil, err
			}
		}
		if transformed && clips.parent != nil {
			matrix := NewMatrix(clips.parent.attr("CTM")).Multiply(NewMatrix(node.attr("CTM")))
			attrs = slices.DeleteFunc(attrs, func(attr xml.Attr) bool { return attr.Name.Space == "" && attr.Name.Local == "CTM" })
			attrs.add("CTM", matrix.String())
		}
	}
	if m.defaultCS != "" && (name == "FillColor" || name == "StrokeColor" || name == "Color") && node.attr("ColorSpace") == "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "ColorSpace"}, Value: m.defaultCS})
	}
	var content []byte
	pageContent := name == "Page" && node.parent == nil
	if pageContent {
		for _, child := range node.children {
			if child.matchesOFD("PageRes") || !entry.template && child.matchesOFD("Template") {
				encoded, err := m.encode(entry, child)
				if err != nil {
					return nil, err
				}
				content = append(content, encoded...)
			}
		}
		local := node.child("Area")
		if local == nil {
			var templates []Template
			for _, child := range node.children {
				if !entry.template && child.matchesOFD("Template") {
					templates = append(templates, Template{TemplateID: child.attr("TemplateID")})
				}
			}
			area, err := m.reader.resolvePageArea(PageArea{}, templates)
			if err != nil {
				return nil, err
			}
			var fields []byte
			for _, field := range [][2]string{{"PhysicalBox", area.PhysicalBox}, {"ApplicationBox", area.ApplicationBox}, {"ContentBox", area.ContentBox}, {"BleedBox", area.BleedBox}} {
				if field[1] != "" {
					fields = append(fields, editorXMLText(field[0], field[1])...)
				}
			}
			encoded, err := editorXMLContainer("Area", nil, fields)
			if err != nil {
				return nil, err
			}
			content = append(content, encoded...)
		} else {
			encoded, err := m.encode(entry, local)
			if err != nil {
				return nil, err
			}
			content = append(content, encoded...)
		}
	}
	for _, child := range node.children {
		if pageContent && (child.matchesOFD("Area") || child.matchesOFD("Template") || child.matchesOFD("PageRes")) {
			continue
		}
		if name == "Goto" && child.name.Local == "Bookmark" {
			if dest, ok := m.bookmarks[child.attr("Name")]; ok {
				encoded, err := xml.Marshal(dest)
				if err != nil {
					return nil, err
				}
				root, err := parseEditorXMLContext(m.ctx, encoded)
				if err != nil {
					return nil, err
				}
				encoded, err = m.encode(editorImportEntry{data: encoded}, root)
				if err != nil {
					return nil, err
				}
				content = append(content, encoded...)
			}
			continue
		}
		encoded, err := m.encode(entry, child)
		if err != nil {
			return nil, err
		}
		content = append(content, encoded...)
	}
	if name == "Actions" && len(content) == 0 {
		return nil, nil
	}
	if len(node.children) == 0 {
		value := editorImportText(entry.data, node)
		var err error
		switch name {
		case "FontFile", "MediaFile", "Profile", "SignedValue", "BaseLoc":
			loc, resolveErr := m.resolve(entry.name, entry.base, strings.TrimSpace(value))
			if resolveErr != nil {
				return nil, resolveErr
			}
			directory := "Res"
			file := path.Base(loc)
			owner := node.parent.attr("ID")
			ownerID := entry.ids[editorImportKey(owner)]
			if ownerID == "" && owner != "" {
				ownerID = m.id(owner)
			}
			switch name {
			case "FontFile":
				directory = "Res/Fonts"
				if ownerID != "" {
					file = "Font_" + ownerID + strings.ToLower(path.Ext(loc))
				}
			case "MediaFile":
				directory = "Res/Images"
				if node.parent.attr("Type") != "Image" {
					directory = "Res/Media"
				} else if ownerID != "" {
					file = "Image_" + ownerID + strings.ToLower(path.Ext(loc))
				}
			case "Profile":
				directory = "Res/ColorSpaces"
			case "SignedValue", "BaseLoc":
				directory = "Signs"
			}
			value, err = m.copyData(loc, path.Join(directory, file), false)
		case "FileLoc":
			loc, resolveErr := m.resolve(entry.name, "", strings.TrimSpace(value))
			if resolveErr != nil {
				return nil, resolveErr
			}
			if node.parent.name.Local == "Attachment" {
				value, err = m.copyData(loc, path.Join("Attachments", path.Base(loc)), false)
			} else {
				pageID := node.parent.attr("PageID")
				candidate := path.Join(m.directory, "Annotations", "Page_"+m.id(pageID)+".xml")
				value, err = m.copyXML(loc, candidate, false, m.pageScopes[editorImportKey(pageID)])
			}
		case "Substitution", "Thumbnail":
			value, err = m.resource(entry.scope, strings.TrimSpace(value), "Image")
		}
		if err != nil {
			return nil, err
		}
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
			return nil, err
		}
		content = append(content, escaped.Bytes()...)
	}
	encoded, err := editorXMLContainer(name, attrs, content)
	return bytes.TrimPrefix(encoded, []byte(xml.Header)), err
}

// annotations 复制所选页面的注释索引
// 返回: []byte 索引条目, error 错误信息
func (m *editorPageImport) annotations() ([]byte, error) {
	if m.doc.Annotations == "" {
		return nil, nil
	}
	name := m.reader.ResPath(m.doc.Annotations)
	data, err := m.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(m.ctx, data)
	if err != nil {
		return nil, err
	}
	var entries []byte
	for _, node := range root.children {
		if !m.pages[editorImportKey(node.attr("PageID"))] {
			continue
		}
		encoded, err := m.encode(editorImportEntry{name: name, data: data}, node)
		if err != nil {
			return nil, err
		}
		entries = append(entries, encoded...)
	}
	return entries, nil
}

// attachment 复制页面动作引用的附件，不导入无关附件
// 入参: id 来源附件标识
// 返回: string 新标识, error 错误信息
func (m *editorPageImport) attachment(id string) (string, error) {
	if err := m.ctx.Err(); err != nil {
		return "", err
	}
	if m.copyPage {
		return id, nil
	}
	if _, exists := m.attachments[id]; exists {
		return m.attachmentID(id)
	}
	if m.attachmentMap == nil {
		name, data := m.reader.documentRoot, m.documentXML
		root := m.documentRoot.child("Attachments")
		if location := strings.TrimSpace(m.doc.Attachments.Path); location != "" {
			name = m.reader.ResPath(location)
			var err error
			data, err = m.readFile(name)
			if err != nil {
				return "", err
			}
			root, err = parseEditorXMLContext(m.ctx, data)
			if err != nil {
				return "", err
			}
		}
		entries := make(map[string]editorImportEntry)
		if root != nil {
			for _, node := range root.children {
				if err := m.ctx.Err(); err != nil {
					return "", err
				}
				if !node.matchesOFD("Attachment") {
					continue
				}
				key := node.attr("ID")
				if _, exists := entries[key]; exists {
					return "", fmt.Errorf("duplicate attachment ID: %s", key)
				}
				entries[key] = editorImportEntry{name: name, data: data, node: node}
			}
		}
		m.attachmentMap = entries
	}
	entry, exists := m.attachmentMap[id]
	if !exists {
		return "", fmt.Errorf("attachment %q not found", id)
	}
	data, err := m.encode(entry, entry.node)
	if err != nil {
		return "", err
	}
	m.attachments[id] = data
	return m.attachmentID(id)
}

// attachmentID 为来源附件分配独立的文本标识，避免与数值型资源标识混淆
// 入参: id 来源附件标识
// 返回: string 目标附件标识, error 附件索引错误
func (m *editorPageImport) attachmentID(id string) (string, error) {
	key := "Attachment:" + id
	if mapped := m.ids[key]; mapped != "" {
		return mapped, nil
	}
	if m.attachmentIDs == nil {
		attachments, err := m.target.Attachments()
		if err != nil {
			return "", err
		}
		used := make(map[string]bool, len(attachments))
		for _, attachment := range attachments {
			if err := m.ctx.Err(); err != nil {
				return "", err
			}
			if used[attachment.ID] {
				return "", fmt.Errorf("duplicate attachment ID: %s", attachment.ID)
			}
			used[attachment.ID] = true
		}
		for removedID := range m.removedAttachments {
			used[removedID] = true
		}
		m.attachmentIDs = used
	}
	mapped, maximum := nextAttachmentID(m.attachmentIDs, m.maximum)
	m.maximum, m.ids[key] = maximum, mapped
	return mapped, nil
}

// signatureID 分配导入签章的文本标识，同名签章注释按来源文件区分
// 入参: source 来源文件, kind 节点类型, id 来源标识
// 返回: string 目标标识, error 签章索引或编号错误
func (m *editorPageImport) signatureID(source, kind, id string) (string, error) {
	key := kind + ":" + id
	if kind == "StampAnnot" {
		key = kind + ":" + source + ":" + id
	}
	if mapped := m.ids[key]; mapped != "" {
		return mapped, nil
	}
	if m.signatureIDs == nil {
		var sequence signatureIDSequence
		doc, err := m.target.Doc()
		if err != nil {
			return "", err
		}
		if doc.Signatures != "" {
			name := m.target.ResPath(doc.Signatures)
			data, err := m.target.readFileViewContext(m.ctx, name)
			if err != nil {
				return "", err
			}
			var list Signatures
			if err := xml.Unmarshal(data, &list); err != nil {
				return "", err
			}
			sequence.observe(list.MaxSignID)
			for _, ref := range list.List {
				sequence.observe(ref.ID)
				stamps, err := m.signatureStamps(m.target, signatureRefPath(name, ref.BaseLoc))
				if err != nil {
					return "", err
				}
				for _, stamp := range stamps {
					sequence.observe(stamp.ID)
				}
			}
		}
		m.signatureIDs = &sequence
	}
	mapped, err := m.signatureIDs.next()
	if err != nil {
		return "", err
	}
	m.ids[key] = mapped
	return mapped, nil
}

// signatureStamps 流式读取签章位置，跳过本次迁移无需解析的摘要和提供者信息
// 入参: reader 来源或目标阅读器, name 签名文件路径
// 返回: []SignatureStamp 签章位置, error 读取、解析或取消错误
func (m *editorPageImport) signatureStamps(reader *Reader, name string) ([]SignatureStamp, error) {
	input, err := reader.openFile(name)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	var file struct {
		XMLName xml.Name         `xml:"Signature"`
		Stamps  []SignatureStamp `xml:"SignedInfo>StampAnnot"`
	}
	progress := m.progress.withContext(m.ctx)
	decoder := xml.NewDecoder(&editorProgressReader{Reader: input, progress: progress, stage: "resources"})
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	for {
		if err := m.ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			return file.Stamps, nil
		}
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			return nil, fmt.Errorf("multiple XML roots in signature %s", name)
		case xml.CharData:
			if len(bytes.TrimSpace(value)) != 0 {
				return nil, fmt.Errorf("unexpected text after signature %s", name)
			}
		}
	}
}

// signatures 复制所选页面的签章及原始验证凭据，过滤其他页面的签章位置
// 返回: []byte 签名索引条目, error 错误信息
func (m *editorPageImport) signatures() ([]byte, error) {
	if m.doc.Signatures == "" {
		return nil, nil
	}
	name := m.reader.ResPath(m.doc.Signatures)
	data, err := m.readFile(name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(m.ctx, data)
	if err != nil {
		return nil, err
	}
	var entries []byte
	for _, node := range root.children {
		if node.name.Local != "Signature" {
			continue
		}
		loc, err := m.resolve(name, "", node.attr("BaseLoc"))
		if err != nil {
			return nil, err
		}
		stamps, err := m.signatureStamps(m.reader, loc)
		if err != nil {
			return nil, err
		}
		matched := false
		for _, stamp := range stamps {
			matched = matched || m.pages[editorImportKey(stamp.PageRef)]
		}
		if !matched {
			continue
		}
		encoded, err := m.encode(editorImportEntry{name: name, data: data}, node)
		if err != nil {
			return nil, err
		}
		entries = append(entries, encoded...)
	}
	return entries, nil
}

// merge 合并包索引及资源图，原有页面和元数据保持原文
// 入参: base 目标基底, refs 插入页面, annotations 注释索引, signatures 签名索引
// 返回: *editorSource 新基底, error 错误信息
func (m *editorPageImport) merge(base *editorSource, refs []Page, annotations, signatures []byte) (*editorSource, error) {
	if err := m.ctx.Err(); err != nil {
		return nil, err
	}
	files := maps.Clone(base.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, m.files)
	name := base.reader.documentRoot
	if file, ok := base.reader.packageFile(name); ok {
		name = cleanPackagePath(file.Name)
	}
	data, err := base.reader.readFileViewContext(m.ctx, name)
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(m.ctx, data)
	if err != nil {
		return nil, err
	}
	common := root.child("CommonData")
	if common == nil {
		return nil, fmt.Errorf("document has no CommonData")
	}
	position := common.close
	for _, node := range common.children {
		if node.name.Local == "TemplatePage" || node.name.Local == "DefaultCS" {
			position = node.start
			break
		}
	}
	var content []byte
	if len(m.resourceXML) != 0 {
		name, added, err := mergePackageResources(m.ctx, base.reader, files, base.document, m.directory, m.resourceXML)
		if err != nil {
			return nil, err
		}
		if added {
			content = common.textXML("DocumentRes", "/"+strings.TrimPrefix(name, "/"))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(m.templateParts)) {
		entry, err := editorXMLGenerated(m.templateParts[id], common.name.Space)
		if err != nil {
			return nil, err
		}
		content = append(content, entry...)
	}
	if m.maximum > maxUnitID {
		return nil, fmt.Errorf("MaxUnitID %d exceeds uint32 range", m.maximum)
	}
	patches := []editorXMLPatch{{position, position, content}}
	patch := editorXMLPatch{common.open, common.open, common.textXML("MaxUnitID", formatUnitID(m.maximum))}
	if maximum := common.child("MaxUnitID"); maximum != nil {
		patch = editorXMLContent(data, maximum, []byte(formatUnitID(m.maximum)))
	}
	patches = append(patches, patch)
	pages := root.child("Pages")
	if pages == nil {
		return nil, fmt.Errorf("document has no Pages")
	}
	if len(m.pageParts) != 0 {
		entries, err := editorXMLGenerated(m.pageParts, pages.name.Space)
		if err != nil {
			return nil, err
		}
		if pages.open == pages.end {
			patches = append(patches, editorXMLContent(data, pages, entries))
		} else {
			patches = append(patches, editorXMLPatch{pages.close, pages.close, entries})
		}
	}
	data = editorPatchXML(data, patches)
	if len(annotations) != 0 {
		loc, err := mergeImportIndex(m.ctx, base.reader, files, base.document.Annotations, path.Join(m.directory, "Annotations.xml"), "Annotations", annotations)
		if err != nil {
			return nil, err
		}
		if base.document.Annotations == "" {
			root, err = parseEditorXMLContext(m.ctx, data)
			if err != nil {
				return nil, err
			}
			data, err = editorXMLSetText(data, root, [][2]string{{"Annotations", loc}})
			if err != nil {
				return nil, err
			}
		}
	}
	if len(m.attachments) != 0 {
		var entries []byte
		for _, id := range slices.Sorted(maps.Keys(m.attachments)) {
			entries = append(entries, m.attachments[id]...)
		}
		root, err = parseEditorXMLContext(m.ctx, data)
		if err != nil {
			return nil, err
		}
		node := root.child("Attachments")
		if node != nil && len(node.children) != 0 {
			entries, err = editorXMLGenerated(entries, node.name.Space)
			if err != nil {
				return nil, err
			}
			content := append(bytes.Clone(data[node.open:node.close]), entries...)
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, content)})
		} else {
			loc, err := mergeImportIndex(m.ctx, base.reader, files, base.document.Attachments.Path, path.Join(m.directory, "Attachments.xml"), "Attachments", entries)
			if err != nil {
				return nil, err
			}
			if base.document.Attachments.Path == "" {
				data, err = editorXMLSetText(data, root, [][2]string{{"Attachments", loc}})
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if len(m.structureXML) != 0 {
		root, err = parseEditorXMLContext(m.ctx, data)
		if err != nil {
			return nil, err
		}
		if node := root.child("CustomTags"); node != nil && len(node.children) != 0 {
			entries, err := editorXMLGenerated(m.structureXML, node.name.Space)
			if err != nil {
				return nil, err
			}
			content := append(bytes.Clone(data[node.open:node.close]), entries...)
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, content)})
		} else {
			loc, err := mergeImportIndex(m.ctx, base.reader, files, base.document.CustomTags.Path, path.Join(m.directory, "CustomTags.xml"), "CustomTags", m.structureXML)
			if err != nil {
				return nil, err
			}
			if base.document.CustomTags.Path == "" {
				data, err = editorXMLSetText(data, root, [][2]string{{"CustomTags", loc}})
				if err != nil {
					return nil, err
				}
			}
		}
	}
	files[name] = data
	if len(signatures) != 0 {
		if base.reader.OFD.DocBody[base.reader.documentIndex].versioned {
			return nil, fmt.Errorf("signature import into a versioned document is not supported")
		}
		loc, err := mergeImportIndex(m.ctx, base.reader, files, base.document.Signatures, path.Join(m.directory, "Signs", "Signatures.xml"), "Signatures", signatures)
		if err != nil {
			return nil, err
		}
		indexName := cleanPackagePath(loc)
		indexData := files[indexName]
		indexRoot, err := parseEditorXMLContext(m.ctx, indexData)
		if err != nil {
			return nil, err
		}
		maximum := "s" + strconv.FormatUint(uint64(*m.signatureIDs), 10)
		value := indexRoot.textXML("MaxSignId", maximum)
		patch := editorXMLPatch{indexRoot.open, indexRoot.open, value}
		if node := indexRoot.child("MaxSignId"); node != nil {
			patch = editorXMLPatch{node.start, node.end, value}
		}
		files[indexName] = editorPatchXML(indexData, []editorXMLPatch{patch})
		if base.document.Signatures == "" {
			ofd, err := base.reader.readFileViewContext(m.ctx, "OFD.xml")
			if err != nil {
				return nil, err
			}
			root, err := parseEditorXMLContext(m.ctx, ofd)
			if err != nil {
				return nil, err
			}
			name := "OFD.xml"
			if file, ok := base.reader.packageFile(name); ok {
				name = cleanPackagePath(file.Name)
			}
			files[name], err = editorXMLSetText(ofd, root.childAt("DocBody", base.reader.documentIndex), [][2]string{{"Signatures", loc}})
			if err != nil {
				return nil, err
			}
		}
	}
	parts, err := base.reader.versionChanges(m.ctx, files)
	if err != nil {
		return nil, err
	}
	files = maps.Clone(base.reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, parts)
	reader := &Reader{Zip: base.reader.Zip, encrypted: base.reader.encryptedSource(), sourceFile: base.reader.sourceFile, files: files, encryption: base.reader.encryption, documentIndex: base.reader.documentIndex, selectedVersion: base.reader.selectedVersion}
	if err := reader.initRoot(); err != nil {
		return nil, err
	}
	doc, err := reader.DocContext(m.ctx)
	if err != nil {
		return nil, err
	}
	next := *base
	next.reader, next.document = reader, doc
	next.pages, next.origins = maps.Clone(base.pages), maps.Clone(base.origins)
	for id, page := range next.pages {
		if page.original == nil {
			next.pages[id] = &editorSourcePage{ref: page.ref}
		}
	}
	for _, ref := range refs {
		next.pages[ref.ID] = &editorSourcePage{ref: ref}
	}
	next.idsReady = true
	return &next, nil
}

// editorProgressReader 为输入流提供可取消的分块读取检查点
type editorProgressReader struct {
	io.Reader
	progress  editorProgress
	stage     string
	completed int
}

// Read 读取数据并在下一块开始前检查取消
// 入参: data 接收缓冲区
// 返回: int 读取字节数, error 错误信息
func (r *editorProgressReader) Read(data []byte) (int, error) {
	if err := r.progress.report(r.stage, r.completed, 0); err != nil {
		return 0, err
	}
	n, err := r.Reader.Read(data[:min(len(data), 1<<20)])
	r.completed += n
	return n, err
}

// importSelection 迁移正文与注解，共用资源映射并保持来源绘制顺序
// 入参: ctx 取消上下文, source 来源编辑器, sourcePage 来源页, ids 正文标识, annotations 注解标识, page 目标页, options 导入配置, strictLinks 拒绝跨页链接
// 返回: []string 正文标识, []string 注解标识, error 迁移或提交错误
func (e *Editor) importSelection(ctx context.Context, source *Editor, sourcePage int, ids, annotations []string, page int, options ObjectImportOptions, strictLinks bool) ([]string, []string, error) {
	progress := editorProgress(options.OnProgress).withContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !finite(options.DX) || !finite(options.DY) {
		return nil, nil, fmt.Errorf("import requires finite offsets")
	}
	if source.encryption != nil && e.encryption == nil && !options.AllowDecrypted {
		return nil, nil, ErrEncryptionPolicyRequired
	}
	objects, err := source.Objects(sourcePage, ids)
	if err != nil {
		return nil, nil, err
	}
	target, err := e.page(page)
	if err != nil || len(objects) == 0 && len(annotations) == 0 {
		return nil, nil, err
	}
	if source == e {
		if err := progress.report("commit", 0, 0); err != nil {
			return nil, nil, err
		}
		copied, err := e.CopyObjectsContext(ctx, page, objects, options.DX, options.DY)
		return copied, nil, err
	}
	reader, err := source.readerContext(ctx, progress)
	if err != nil {
		return nil, nil, err
	}
	defer reader.Close()
	doc, err := reader.Doc()
	if err != nil {
		return nil, nil, err
	}
	ref := doc.Pages.Page[sourcePage]
	base, migration, err := e.prepareImport(ctx, reader, doc, map[string]bool{ref.ID: true}, false, progress)
	if err != nil {
		return nil, nil, err
	}
	migration.ids[editorImportKey(ref.ID)], migration.strictLinks = target.ID, strictLinks
	pageName := reader.ResPath(ref.BaseLoc)
	content, err := reader.PageContentByIndex(sourcePage)
	if err != nil {
		return nil, nil, err
	}
	pageScope, err := migration.resolvePageScope(pageName, content.PageRes)
	if err != nil {
		return nil, nil, err
	}
	layers := make([][]byte, len(objects))
	for i, object := range objects {
		if err := progress.report("objects", i, len(objects)); err != nil {
			return nil, nil, err
		}
		var data []byte
		var attrs ofdAttrs
		if origin := object.origin; origin != nil {
			data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
			if err == nil {
				data, err = editorXMLStandalone(data, origin.node)
			}
			for parent := origin.node.parent; parent != nil; parent = parent.parent {
				if parent.name.Local == "Layer" {
					attrs.add("DrawParam", parent.attr("DrawParam"))
					break
				}
			}
		} else {
			data, err = editorObjectXML(object)
		}
		if err != nil {
			return nil, nil, err
		}
		data, err = editorXMLContainer("Layer", attrs, data)
		if err != nil {
			return nil, nil, err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, nil, err
		}
		layers[i], err = migration.encode(editorImportEntry{name: pageName, data: data, scope: pageScope}, root)
		if err != nil {
			return nil, nil, err
		}
	}
	notes := &AnnotationSelection{editor: e, document: e.documentIdentity()}
	for _, id := range annotations {
		data, err := source.annotationXML(sourcePage, id)
		if err != nil {
			return nil, nil, err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, nil, err
		}
		data, err = migration.encode(editorImportEntry{name: pageName, data: data, scope: pageScope}, root)
		if err != nil {
			return nil, nil, err
		}
		notes.data = append(notes.data, data)
	}
	if err := migration.encodeResources(); err != nil {
		return nil, nil, err
	}
	for _, resource := range e.resources {
		delete(migration.files, resource.name)
	}
	next, err := migration.merge(base, nil, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var copied, copiedNotes []string
	err = e.Transaction(func(editor *Editor) error {
		if err := progress.report("commit", 0, 0); err != nil {
			return err
		}
		editor.bindImportResources()
		editor.source, editor.maxID = next, migration.maximum
		next.idsReady = true
		imported := make([]GraphicObject, len(layers))
		for i, data := range layers {
			data, err := editorXMLContainer("Content", nil, data)
			if err != nil {
				return err
			}
			var content Content
			if err := xml.Unmarshal(data, &content); err != nil {
				return err
			}
			layer := content.Layer[0]
			root, err := parseEditorXMLContext(ctx, data)
			if err != nil {
				return err
			}
			object := layer.Objects[0]
			object.TextObject.layout = objects[i].TextObject.layout
			object.state = objects[i].state
			srcStates := source.collectCompositeStates(objects[i].CompositeGraphicUnit)
			object.CompositeGraphicUnit.states = remapCompositeStates(srcStates, migration.ids)
			imported[i], err = editor.copiedObjectStyle(object, layer.DrawParam)
			if err != nil {
				return err
			}
			imported[i].origin = &editorObjectOrigin{editor: editor, document: editor.documentIdentity(), data: data, node: root.child("Layer").children[0], object: object}
		}
		copied, err = editor.CopyObjectsContext(ctx, page, imported, options.DX, options.DY)
		if err != nil {
			return err
		}
		orderedIDs := make([]string, len(objects))
		for i, object := range objects {
			orderedIDs[i] = editorObjectID(object)
		}
		notes.document = editor.documentIdentity()
		copiedNotes, err = editor.PasteAnnotations(page, notes, options.DX, options.DY)
		if err != nil {
			return err
		}
		return editor.copyRichTextMetadata(ctx, source, sourcePage, orderedIDs, annotations, page, copied, copiedNotes)
	})
	if err != nil {
		return nil, nil, err
	}
	return copied, copiedNotes, err
}

// importPages 迁移页面，同文档复制时复用原资源并保留其他页面的跳转
// 入参: ctx 取消上下文, source 来源阅读器, indexes 来源页索引, at 插入位置, copyPage 是否同文档复制, options 导入选项
// 返回: []string 新页标识, error 错误信息
func (e *Editor) importPages(ctx context.Context, source *Reader, indexes []int, at int, copyPage bool, options PageImportOptions) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if at < 0 || at > len(e.pages) {
		return nil, fmt.Errorf("page index %d out of range", at)
	}
	doc, err := source.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool)
	for _, index := range indexes {
		if index < 0 || index >= len(doc.Pages.Page) {
			return nil, fmt.Errorf("source page index %d out of range", index)
		}
		id := editorImportKey(doc.Pages.Page[index].ID)
		if selected[id] {
			return nil, fmt.Errorf("duplicate source page %q", id)
		}
		selected[id] = true
	}
	if len(indexes) == 0 {
		return nil, nil
	}
	progress := editorProgress(options.OnProgress).withContext(ctx)
	base, m, err := e.prepareImport(ctx, source, doc, selected, copyPage, progress)
	if err != nil {
		return nil, err
	}
	refs := make([]Page, len(indexes))
	ids := make([]string, len(indexes))
	for i, index := range indexes {
		if err := progress.report("pages", i, len(indexes)); err != nil {
			return nil, err
		}
		page := doc.Pages.Page[index]
		ids[i] = m.id(page.ID)
		pagePath := source.ResPath(page.BaseLoc)
		name, err := m.copyXML(pagePath, packagePagePath(m.directory, ids[i]), false, nil)
		if err != nil {
			return nil, err
		}
		if scope, ok := m.pageScopesByPath[cleanPackagePath(pagePath)]; ok {
			m.pageScopes[editorImportKey(page.ID)] = scope
		}
		refs[i] = Page{ID: ids[i], BaseLoc: name}
		entry, err := m.documentReference("Pages", "Page", page.ID, ids[i], name)
		if err != nil {
			return nil, err
		}
		m.pageParts = append(m.pageParts, entry...)
	}
	if err := progress.report("pages", len(indexes), len(indexes)); err != nil {
		return nil, err
	}
	annotations, err := m.annotations()
	if err != nil {
		return nil, err
	}
	signatures, err := m.signatures()
	if err != nil {
		return nil, err
	}
	beforeOutlines, outlines := e.outlines, e.outlines
	m.structureXML, err = m.structures()
	if err != nil {
		return nil, err
	}
	if options.Outlines {
		imported, err := m.outlines()
		if err != nil {
			return nil, err
		}
		outlines, err = e.appendImportedOutlines(ctx, imported)
		if err != nil {
			return nil, err
		}
	}
	if !copyPage {
		if err := m.encodeResources(); err != nil {
			return nil, err
		}
	}
	for _, resource := range e.resources {
		delete(m.files, resource.name)
	}
	next, err := m.merge(base, refs, annotations, signatures)
	if err != nil {
		return nil, err
	}
	added := make([]PageContent, len(refs))
	for i, ref := range refs {
		added[i] = PageContent{ID: ref.ID}
	}
	if copyPage {
		preview := *e
		preview.source, preview.pages = next, added
		for i, index := range indexes {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			states := make(map[string]editorCompositeState)
			groups := make(map[string]map[string]editorCompositeState)
			for _, layer := range e.pages[index].Content.Layer {
				for _, object := range layer.Objects {
					id := m.ids[editorObjectID(object)]
					state := object.state
					if layout := object.TextObject.layout; object.Type == "TextObject" && layout != nil {
						state.layout = layout
					}
					states[id] = state
					groups[id] = remapCompositeStates(object.CompositeGraphicUnit.states, m.ids)
				}
			}
			if err := preview.loadSourcePage(i); err != nil {
				return nil, err
			}
			for j := range added[i].Content.Layer {
				for k := range added[i].Content.Layer[j].Objects {
					object := &added[i].Content.Layer[j].Objects[k]
					id := editorObjectID(*object)
					object.state = states[id]
					if object.Type == "TextObject" {
						object.TextObject.layout = object.state.layout
					}
					object.CompositeGraphicUnit.states = groups[id]
				}
			}
		}
	}
	if err := progress.report("commit", 0, 0); err != nil {
		return nil, err
	}
	e.bindImportResources()
	e.source, e.maxID = next, m.maximum
	next.idsReady = true
	e.outlines = outlines
	e.pages = slices.Insert(e.pages, at, added...)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) {
			for i := range added {
				added[i] = copyEditorPage(e.pages[at+i])
			}
			e.pages = slices.Delete(e.pages, at, at+len(added))
			e.source = base
			e.outlines = beforeOutlines
		}
		change.redo = func(e *Editor) {
			pages := make([]PageContent, len(added))
			for i := range added {
				pages[i] = copyEditorPage(added[i])
			}
			e.pages = slices.Insert(e.pages, at, pages...)
			e.source = next
			e.outlines = outlines
		}
	}
	return ids, nil
}

// bindImportResources 将新建文档资源路径绑定到包根，保持迁移前后的解析一致
func (e *Editor) bindImportResources() {
	if e.source != nil {
		return
	}
	for i := range e.resources {
		resource := &e.resources[i]
		if resource.font != nil {
			resource.font = cloneEditorData(resource.font)
			if resource.name != "" {
				resource.font.FontFile = "/" + resource.name
			}
		}
		if resource.image != nil {
			resource.image = cloneEditorData(resource.image)
			resource.image.MediaFile = "/" + resource.name
		}
	}
}

// prepareImport 建立页面和对象迁移共用的资源图，不修改目标编辑器
// 入参: ctx 取消上下文, source 来源, doc 来源文档, selected 页面映射范围, copyPage 是否同文档, progress 进度
// 返回: *editorSource 目标基础, *editorPageImport 资源图, error 错误信息
func (e *Editor) prepareImport(ctx context.Context, source *Reader, doc *Document, selected map[string]bool, copyPage bool, progress editorProgress) (*editorSource, *editorPageImport, error) {
	maximum, err := e.sourceMaxID(progress)
	if err != nil {
		return nil, nil, err
	}
	base := e.source
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return nil, nil, err
		}
	}
	m := &editorPageImport{ctx: ctx, reader: source, doc: doc, directory: e.packageDirectory(), target: base.reader, maximum: maximum, copyPage: copyPage, progress: progress,
		xmlPaths: make(map[string]string), dataPaths: make(map[string]string), evidence: make(map[string]string), ids: make(map[string]string), resIDs: make(map[string]string), pages: make(map[string]bool, len(selected)),
		files: make(map[string][]byte), resByFile: make(map[string]map[string]editorImportEntry), docRes: make(map[string]bool), pageScopes: make(map[string][]string), pageScopesByPath: make(map[string][]string),
		used: make(map[string][]byte), usedGroups: make(map[string]string),
		templates: make(map[string]TemplatePage), templateParts: make(map[string][]byte), attachments: make(map[string][]byte), removedAttachments: base.removedAttachments, bookmarks: make(map[string]Dest, len(doc.Bookmarks.Bookmark))}
	for _, bookmark := range doc.Bookmarks.Bookmark {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		m.bookmarks[bookmark.Name] = bookmark.Dest
	}
	for id, value := range selected {
		m.pages[editorImportKey(id)] = value
	}
	for _, resource := range e.resources {
		m.files[resource.name] = nil
	}
	m.documentXML, err = m.readFile(source.documentRoot)
	if err != nil {
		return nil, nil, err
	}
	m.documentRoot, err = parseEditorXMLContext(ctx, m.documentXML)
	if err != nil {
		return nil, nil, err
	}
	if copyPage {
		for _, page := range doc.Pages.Page {
			if !m.pages[editorImportKey(page.ID)] {
				m.ids[editorImportKey(page.ID)] = page.ID
			}
		}
	}
	for _, list := range [][]string{doc.CommonData.DocumentRes, doc.CommonData.PublicRes} {
		for _, res := range slices.Backward(list) {
			loc := source.ResPath(res)
			if err := m.resourceIndex(loc); err != nil {
				return nil, nil, err
			}
			m.docRes[loc] = true
			m.docScope = appendUniquePath(m.docScope, loc)
		}
	}
	for _, template := range doc.CommonData.TemplatePage {
		m.templates[editorImportKey(template.ID)] = template
	}
	if doc.CommonData.DefaultCS != 0 {
		m.defaultCS, err = m.resource(m.docScope, formatUnitID(uint64(doc.CommonData.DefaultCS)), "ColorSpace")
		if err != nil {
			return nil, nil, err
		}
	} else if !e.sourceRGB() {
		m.defaultCS = m.id("defaultRGB")
		m.used["defaultRGB"] = []byte(`<ofd:ColorSpace ID="` + m.defaultCS + `" Type="RGB" BitsPerComponent="8"/>`)
		m.usedGroups["defaultRGB"] = "ColorSpaces"
	}
	return base, m, nil
}

// appendImportedOutlines 将已迁移目录追加到目标根目录，规范计数且不修改当前编辑器
// 入参: ctx 取消上下文, imported 已迁移目录片段
// 返回: []byte 合并后的目录, error 错误信息
func (e *Editor) appendImportedOutlines(ctx context.Context, imported []byte) ([]byte, error) {
	if len(imported) == 0 {
		return e.outlines, nil
	}
	added, err := parseEditorXMLContext(ctx, imported)
	if err != nil {
		return nil, err
	}
	data, err := e.outlineXML()
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	var content []byte
	if root.open != root.end {
		content = bytes.Clone(data[root.open:root.close])
	}
	for _, child := range editorOutlineChildren(added) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := editorXMLStandalone(imported[child.start:child.end], child)
		if err != nil {
			return nil, err
		}
		value, err = editorXMLGenerated(value, root.name.Space)
		if err != nil {
			return nil, err
		}
		content = append(content, value...)
	}
	preview := *e
	preview.historyLimit = 0
	if err := preview.setOutlineXML(editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, content)})); err != nil {
		return nil, err
	}
	return preview.outlines, nil
}

// importBase 为新建文档提供空的保真包基底，不重复保存已创建的页面和资源
// 返回: *editorSource 包基底, error 错误信息
func (e *Editor) importBase() (*editorSource, error) {
	return e.importBaseContext(context.Background())
}

// importBaseContext 生成新建文档的独立包基底，初始化期间响应取消
// 入参: ctx 取消上下文
// 返回: *editorSource 包基底, error 编码、解析或取消错误
func (e *Editor) importBaseContext(ctx context.Context) (*editorSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seed := NewEditor()
	seed.Info = e.Info
	seed.pages = []PageContent{{Area: PageArea{PhysicalBox: "0 0 210 297"}}}
	files := make(map[string][]byte)
	if err := seed.writeParts(ctx, func(name string, data []byte, _ bool) error {
		if name == "OFD.xml" || name == "Doc_0/Document.xml" {
			files[name] = data
		}
		return nil
	}, editorProgress(nil).withContext(ctx)); err != nil {
		return nil, err
	}
	data := files["Doc_0/Document.xml"]
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	files["Doc_0/Document.xml"] = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root.child("Pages"), nil)})
	reader := &Reader{files: files}
	if err := reader.initRootContext(ctx); err != nil {
		return nil, err
	}
	editor, err := reader.EditorContext(ctx)
	if err != nil {
		return nil, err
	}
	editor.source.idsReady = true
	editor.source.fromNew = true
	return editor.source, nil
}

// collectXMLPageRes 提取Page节点下声明的PageRes文本列表
// 入参: data 页面XML数据, root 页面根节点
// 返回: []string PageRes声明列表
func collectXMLPageRes(data []byte, root *editorXML) []string {
	var values []string
	if root.name.Local == "Page" {
		for _, child := range root.children {
			if child.name.Local == "PageRes" {
				values = append(values, editorImportText(data, child))
			}
		}
	}
	return values
}

// editorImportKey 统一数值标识的映射键，保留导入过程中使用的内部名称
// 入参: value 标识或内部名称
// 返回: string 映射键
func editorImportKey(value string) string {
	if id := editorResourceID(value); id != "" {
		return id
	}
	return value
}

// editorImportText 获取节点直接文本，解码XML实体
// 入参: data XML, node 节点
// 返回: string 文本
func editorImportText(data []byte, node *editorXML) string {
	var value struct {
		Text string `xml:",chardata"`
	}
	_ = xml.Unmarshal(data[node.start:node.end], &value)
	return value.Text
}

// mergeImportIndex 向原索引追加条目，不移动原条目的相对路径基准
// 入参: ctx 取消上下文, reader 目标输入包, files 新条目, loc 原索引, fallback 新索引路径, rootName 根节点名称, entries 新条目
// 返回: string 索引绝对路径, error 错误信息
func mergeImportIndex(ctx context.Context, reader *Reader, files map[string][]byte, loc, fallback, rootName string, entries []byte) (string, error) {
	name := fallback
	var data []byte
	var err error
	if loc == "" {
		name = packageAvailableName(reader, files, fallback)
		data, err = editorXMLContainer(rootName, nil, entries)
	} else {
		name, err = editorPageLocation(reader, files, "", "/"+reader.ResPath(loc))
		if err != nil {
			return "", err
		}
		var exists bool
		data, exists = files[name]
		if !exists {
			data, err = reader.readFileViewContext(ctx, name)
		}
		if err != nil {
			return "", err
		}
		root, parseErr := parseEditorXMLContext(ctx, data)
		if parseErr != nil {
			return "", parseErr
		}
		entries, err = editorXMLGenerated(entries, root.name.Space)
		if err != nil {
			return "", err
		}
		if root.open == root.end {
			data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, entries)})
		} else {
			data = editorPatchXML(data, []editorXMLPatch{{root.close, root.close, entries}})
		}
	}
	if err != nil {
		return "", err
	}
	files[name] = data
	return "/" + name, nil
}
