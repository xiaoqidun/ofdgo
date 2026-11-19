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
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
)

// ResourceInfo 描述当前文档及所选版本的资源，Size为文件字节数
// Kind为Font、Image、Audio、Video、Attachment、ColorSpace或DrawParam
// Complete表示已完整识别引用；不包含其他文档和历史版本的引用
// ColorSpace的File为ICC文件，未声明ICC及内联DrawParam的File为空且Missing为false
// Missing表示声明的文件不在包内，与外部字体及零字节文件区分，不表示文件解码结果
type ResourceInfo struct {
	Kind       string              `json:"kind"`
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Format     string              `json:"format"`
	File       string              `json:"file"`
	Size       uint64              `json:"size"`
	Missing    bool                `json:"missing"`
	Complete   bool                `json:"complete"`
	References []ResourceReference `json:"references"`
}

// ResourceSummary 保存资源摘要及引用总数，内嵌ResourceInfo的References为空
type ResourceSummary struct {
	ResourceInfo
	ReferenceCount int `json:"referenceCount"`
}

// ResourceReference 记录资源引用所在的XML及字段，Page为零基页码，非页面或多实例文件为-1
// Object为最近的图元标识，模板、注解、图案和复合资源通过File区分，不推断其页面实例数
type ResourceReference struct {
	File   string `json:"file"`
	Page   int    `json:"page"`
	Object string `json:"object"`
	Role   string `json:"role"`
}

// ResourceLocation 保存资源引用的页面实例范围，Page从1开始，Bounds单位为毫米
// Object为承载引用的图元标识；隐藏或完全裁剪的图元不产生定位结果
type ResourceLocation struct {
	Page   int    `json:"page"`
	Object string `json:"object"`
	Bounds Box    `json:"bounds"`
}

// Resources 列出当前文档及所选版本的资源，标记缺失文件，不读取二进制内容或执行媒体动作
// 重复查询复用已完成的扫描，返回值及引用位置与阅读器相互独立
// 入参: ctx 取消上下文
// 返回: []ResourceInfo 资源及引用位置, error 结构或读取错误
func (r *Reader) Resources(ctx context.Context) ([]ResourceInfo, error) {
	items, err := r.resourceList(ctx)
	if err != nil {
		return nil, err
	}
	result := slices.Clone(items)
	for i := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result[i].References = slices.Clone(result[i].References)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ResourceSummaries 列出资源摘要及引用总数，不复制引用明细
// 入参: ctx 取消上下文
// 返回: []ResourceSummary 资源摘要, error 查询错误
func (r *Reader) ResourceSummaries(ctx context.Context) ([]ResourceSummary, error) {
	items, err := r.resourceList(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ResourceSummary, len(items))
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result[i] = ResourceSummary{ResourceInfo: item, ReferenceCount: len(item.References)}
		result[i].References = nil
	}
	return result, ctx.Err()
}

// ResourceReferences 分页读取当前文档及版本的资源引用，顺序与Resources一致
// 入参: ctx 取消上下文, kind 类型, id 标识, offset 起始位置, limit 最大数量
// 返回: []ResourceReference 独立引用副本, error 查询或分页参数错误
func (r *Reader) ResourceReferences(ctx context.Context, kind, id string, offset, limit int) ([]ResourceReference, error) {
	if offset < 0 || limit < 1 {
		return nil, fmt.Errorf("invalid resource reference range")
	}
	items, err := r.resourceList(ctx)
	if err != nil {
		return nil, err
	}
	var refs []ResourceReference
	found := false
	for _, item := range items {
		if resourceMatches(item, kind, id) {
			found = true
			refs = append(refs, item.References...)
		}
	}
	if !found {
		return nil, fmt.Errorf("resource not found: %s/%s", kind, id)
	}
	if offset >= len(refs) {
		return []ResourceReference{}, ctx.Err()
	}
	return slices.Clone(refs[offset : offset+min(limit, len(refs)-offset)]), ctx.Err()
}

// OpenResource 打开当前资源的原始文件，ColorSpace导出ICC，无ICC或内联DrawParam不提供文件流
// 外部字体同样无可导出的内嵌文件，定义读取使用ColorSpace或DrawParam
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识
// 返回: io.ReadCloser 文件流，由调用方关闭, error 资源或文件错误
func (r *Reader) OpenResource(ctx context.Context, kind, id string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if kind == "Attachment" {
		return r.OpenAttachmentContext(ctx, id)
	}
	if r.resourceInfoCache != nil {
		item, count, err := findResourceWithCount(r.resourceInfoCache, kind, id)
		if err != nil {
			return nil, err
		}
		if count == 1 {
			if item.File == "" {
				return nil, fmt.Errorf("resource has no embedded file")
			}
			stream, err := r.openFile(item.File)
			if err != nil {
				return nil, err
			}
			return imageInput{ReadCloser: stream, ctx: ctx}, nil
		}
	}
	fileLoc, found, err := r.locateResourceFileContext(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("resource not found: %s/%s", kind, id)
	}
	if fileLoc == "" {
		return nil, fmt.Errorf("resource has no embedded file")
	}
	stream, err := r.openFile(r.ResPath(fileLoc))
	if err != nil {
		return nil, err
	}
	return imageInput{ReadCloser: stream, ctx: ctx}, nil
}

// Media 获取已加载的音视频资源，页面资源须先读取所属页面
// 不加载注解或签名
// 入参: id 资源标识
// 返回: MultiMedia 资源信息, error 资源缺失或类型错误
func (r *Reader) Media(id string) (MultiMedia, error) {
	if _, err := r.docStructure(); err != nil {
		return MultiMedia{}, err
	}
	media, ok := r.docMedias.get(id)
	if !ok || (media.Type != "Audio" && media.Type != "Video") {
		if candidate, found := resourceValue(r.mediaCache, id); found && (candidate.Type == "Audio" || candidate.Type == "Video") {
			media, ok = candidate, true
		} else {
			ok = false
		}
	}
	if !ok {
		for _, name := range slices.Sorted(maps.Keys(r.resFileCache)) {
			if parsed := r.resFileCache[name]; parsed != nil {
				if candidate, found := parsed.medias.get(id); found && (candidate.Type == "Audio" || candidate.Type == "Video") {
					media, ok = candidate, true
					break
				}
			}
		}
	}
	if !ok || media.Type != "Audio" && media.Type != "Video" {
		return MultiMedia{}, fmt.Errorf("audio or video resource %q is unavailable", id)
	}
	return media, nil
}

// OpenMedia 打开文档内音视频原始数据，不解码或执行播放
// 入参: id 资源标识
// 返回: io.ReadCloser 数据流，由调用方关闭, error 资源或文件读取错误
func (r *Reader) OpenMedia(id string) (io.ReadCloser, error) {
	media, err := r.Media(id)
	if err != nil {
		return nil, err
	}
	return r.openFile(r.ResPath(media.MediaFile))
}

// ResourceLocations 定位当前文档及版本内的一处资源引用，返回各页面上的可见使用范围
// 模板、图案和复合资源沿引用关系定位承载图元，不将资源声明位置当作页面位置
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识, reference 为Resources返回的引用索引
// 返回: []ResourceLocation 页面位置，无可见图元时为空, error 引用、度量或取消错误
func (r *Renderer) ResourceLocations(ctx context.Context, kind, id string, reference int) ([]ResourceLocation, error) {
	catalog, err := r.Reader.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	targetIdx, ref, err := findResourceRefTarget(catalog.items, kind, id, reference)
	if err != nil {
		return nil, err
	}
	doc, err := r.Reader.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool)
	users := make(map[string][]string)
	for extFile, parentRes := range catalog.extParents {
		if parentRes != "" && parentRes != "*" {
			users["file:"+extFile] = append(users["file:"+extFile], parentRes)
		}
	}
	for _, fileName := range slices.Sorted(maps.Keys(catalog.scan.files)) {
		file := catalog.scan.files[fileName]
		cleanName := cleanPackagePath(file.name)
		root, err := parseEditorXMLContext(ctx, file.data)
		if err != nil {
			return nil, err
		}
		isResRoot := root.matchesOFD("Res")
		_, isExtFile := catalog.extParents[cleanName]
		var visit func(*editorXML, string) error
		visit = func(node *editorXML, object string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !node.matchesOFD(node.name.Local) || node.matchesOFD("Attachments") {
				return nil
			}
			if slices.Contains([]string{"TextObject", "PathObject", "ImageObject", "CompositeObject"}, node.name.Local) {
				object = node.attr("ID")
			}
			ownerID := resourceReferenceOwner(node)
			ownerNode := ""
			if ownerID != "" {
				if isResRoot {
					if declKind := catalog.resKinds[cleanName][ownerID]; declKind != "" {
						ownerNode = "res:" + cleanName + ":" + declKind + ":" + ownerID
					}
				} else if isExtFile {
					ownerNode = "file:" + cleanName
				} else {
					ownerNode = "obj:" + cleanName + ":" + ownerID
				}
			}
			if ownerNode != "" && resourceColorNode(node) && node.attr("ColorSpace") == "" && doc.CommonData.DefaultCS != 0 {
				defCS := fmt.Sprint(doc.CommonData.DefaultCS)
				if depNode := catalog.resolveScopedDepNode(cleanName, node.name.Local, "ColorSpace", defCS); depNode != "" && depNode != ownerNode {
					users[depNode] = append(users[depNode], ownerNode)
				}
				if ref.Role == "DefaultCS" {
					if idx, ok := catalog.resolveRefIndex(cleanName, node.name.Local, "ColorSpace", defCS); ok && idx == targetIdx {
						wanted[ownerNode] = true
					}
				}
			}
			add := func(role, value string) {
				if ownerNode == "" {
					return
				}
				if role != "AttachID" {
					if depNode := catalog.resolveScopedDepNode(cleanName, node.name.Local, role, value); depNode != "" && depNode != ownerNode {
						users[depNode] = append(users[depNode], ownerNode)
					}
				}
				if file.name == ref.File && object == ref.Object && role == ref.Role {
					if idx, ok := catalog.resolveRefIndex(cleanName, node.name.Local, role, value); ok && idx == targetIdx {
						wanted[ownerNode] = true
					}
				}
			}
			for _, attr := range node.attrs {
				if attr.Name.Space == "" && (editorResourceReferenceKind(attr.Name.Local, isResRoot) == editorReferenceID || attr.Name.Local == "AttachID") {
					add(attr.Name.Local, attr.Value)
				}
			}
			if slices.Contains([]string{"Thumbnail", "Substitution"}, node.name.Local) {
				add(node.name.Local, strings.TrimSpace(editorImportText(file.data, node)))
			}
			parent := node.parent
			for parent != nil && parent.matchesOFD("PageBlock") {
				parent = parent.parent
			}
			if !isResRoot && !isExtFile && ownerNode != "" && parent != nil && parent.matchesOFD("Layer") {
				if layer := editorResourceID(parent.attr("ID")); layer != "" {
					layerNode := "obj:" + cleanName + ":" + layer
					if layerNode != ownerNode {
						users[layerNode] = append(users[layerNode], ownerNode)
					}
				}
			}
			for _, child := range node.children {
				if err := visit(child, object); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(root, ""); err != nil {
			return nil, err
		}
	}
	queue := slices.Sorted(maps.Keys(wanted))
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[0]
		queue = queue[1:]
		for _, user := range users[current] {
			if !wanted[user] {
				wanted[user] = true
				queue = append(queue, user)
			}
		}
	}
	hasObjectWanted := false
	for node := range wanted {
		if strings.HasPrefix(node, "obj:") {
			hasObjectWanted = true
			break
		}
	}
	view := *r
	view.pageContext = ctx
	collectTargets := func(files ...string) map[string]*StructureTarget {
		var targets map[string]*StructureTarget
		for _, cf := range files {
			prefix := "obj:" + cf + ":"
			for node := range wanted {
				if strings.HasPrefix(node, prefix) {
					objID := strings.TrimPrefix(node, prefix)
					if objID != "" {
						if targets == nil {
							targets = make(map[string]*StructureTarget)
						}
						if targets[objID] == nil {
							targets[objID] = &StructureTarget{ID: objID}
						}
					}
				}
			}
		}
		return targets
	}
	var result []ResourceLocation
	for page := range doc.Pages.Page {
		if ref.Page >= 0 && ref.Page != page || !hasObjectWanted {
			continue
		}
		content, err := r.Reader.PageContentByIndexContext(ctx, page)
		if err != nil {
			return nil, err
		}
		pageRef := doc.Pages.Page[page]
		var pageHits []ResourceLocation
		walkPart := func(part PageContent, renderAnnots bool, targets map[string]*StructureTarget) error {
			if len(targets) == 0 {
				return nil
			}
			partView := view
			partView.RenderAnnotations = renderAnnots
			visitor := &structureVisitor{ctx: ctx, renderer: &partView, targets: targets, boundsOnly: true}
			if err := partView.WalkPage(&part, visitor); err != nil {
				return err
			}
			for _, objID := range slices.Sorted(maps.Keys(targets)) {
				box := targets[objID].Bounds
				if box.W > 0 && box.H > 0 {
					pageHits = append(pageHits, ResourceLocation{Page: page + 1, Object: objID, Bounds: box})
				}
			}
			return nil
		}
		for _, tplRef := range content.Template {
			for _, tpl := range doc.CommonData.TemplatePage {
				if sameResourceID(tpl.ID, tplRef.TemplateID) {
					tplPart := *content
					tplPart.Content = Content{}
					tplPart.Template = []Template{tplRef}
					if err := walkPart(tplPart, false, collectTargets(cleanPackagePath(r.Reader.ResPath(tpl.BaseLoc)))); err != nil {
						return nil, err
					}
					break
				}
			}
		}
		pagePart := *content
		pagePart.Template = nil
		if err := walkPart(pagePart, false, collectTargets(cleanPackagePath(r.Reader.ResPath(pageRef.BaseLoc)))); err != nil {
			return nil, err
		}
		if pageKey := editorResourceID(pageRef.ID); pageKey != "" {
			annotPart := *content
			annotPart.Content = Content{}
			annotPart.Template = nil
			if err := walkPart(annotPart, true, collectTargets(catalog.annotByPage[pageKey]...)); err != nil {
				return nil, err
			}
		}
		slices.SortStableFunc(pageHits, func(a, b ResourceLocation) int {
			return cmp.Compare(a.Object, b.Object)
		})
		result = append(result, pageHits...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// resourceCatalog 保存当前资源声明和原文位置，供查询及隔离修改复用
type resourceCatalog struct {
	scan        *editorFontDocument
	docRes      map[string]bool
	docScope    []string
	pageScopes  map[string][]string
	fileScopes  map[string][]string
	annotByPage map[string][]string
	extParents  map[string]string
	resKinds    map[string]map[string]string
	itemIndex   map[string]int
	items       []ResourceInfo
	itemFiles   []string
	nodes       map[string]editorFontText
	images      []editorFontText
	fileIDs     map[string]string
}

// add 加入资源声明，拒绝同一资源文件内的重复标识，允许不同资源文件声明同名标识
// 入参: item 资源信息, node 声明位置
// 返回: error 标识冲突
func (c *resourceCatalog) add(item ResourceInfo, node editorFontText) error {
	if item.Kind == "Attachment" {
		key := "Attachment/" + item.ID
		if _, exists := c.nodes[key]; exists {
			return fmt.Errorf("duplicate resource %s", key)
		}
		c.nodes[key] = node
		c.items = append(c.items, item)
		c.itemFiles = append(c.itemFiles, "")
		return nil
	}
	fileName := ""
	if node.file != nil {
		fileName = cleanPackagePath(node.file.name)
	}
	if fileName != "" {
		fileKey := fileName + "\x00" + item.ID
		if prevKind, exists := c.fileIDs[fileKey]; exists {
			if prevKind == item.Kind {
				return fmt.Errorf("duplicate resource %s/%s", item.Kind, item.ID)
			}
			return fmt.Errorf("conflicting resource ID %s", item.ID)
		}
		c.fileIDs[fileKey] = item.Kind
		if c.resKinds[fileName] == nil {
			c.resKinds[fileName] = make(map[string]string)
		}
		c.resKinds[fileName][item.ID] = item.Kind
	}
	key := item.Kind + "/" + item.ID
	if _, exists := c.nodes[key]; !exists {
		c.nodes[key] = node
	}
	if fileName != "" {
		c.nodes[fileName+":"+key] = node
	}
	c.items = append(c.items, item)
	c.itemFiles = append(c.itemFiles, fileName)
	return nil
}

// scopeForFile 返回指定XML文件在资源解析时遵循的局部至文档级作用域链
// 入参: name 包内XML路径
// 返回: []string 按优先级排列的资源文件路径
func (c *resourceCatalog) scopeForFile(name string) []string {
	if scope, ok := c.fileScopes[cleanPackagePath(name)]; ok {
		return scope
	}
	return c.docScope
}

// resolveRefIndex 按引用所在文件的作用域链与期望类型定位实际命中的资源条目索引
// 入参: file 引用所在文件, elem 引用所在元素名, role 引用字段角色, rawValue 原始标识文本
// 返回: int 命中的catalog.items索引, bool 是否命中公开资源
func (c *resourceCatalog) resolveRefIndex(file, elem, role, rawValue string) (int, bool) {
	if role == "AttachID" {
		idx, ok := c.itemIndex["Attachment\x00"+rawValue]
		return idx, ok
	}
	if slices.Contains([]string{"TemplateID", "PageID", "PageRef", "RefId"}, role) {
		return -1, false
	}
	id := editorResourceID(rawValue)
	if id == "" {
		return -1, false
	}
	expected := resourceExpectedKind(elem, role)
	scope := c.scopeForFile(file)
	if expected == "CompositeGraphicUnit" {
		for _, resFile := range scope {
			if c.resKinds[resFile][id] == "CompositeGraphicUnit" {
				return -1, false
			}
		}
	} else if expected != "" {
		for _, resFile := range scope {
			if idx, ok := c.itemIndex[resFile+"\x00"+expected+"\x00"+id]; ok {
				return idx, true
			}
		}
	}
	for _, resFile := range scope {
		kind := c.resKinds[resFile][id]
		if kind == "" || kind == "CompositeGraphicUnit" {
			continue
		}
		if idx, ok := c.itemIndex[resFile+"\x00"+kind+"\x00"+id]; ok {
			return idx, true
		}
	}
	return -1, false
}

// resolveScopedDepNode 解析引用在当前文件作用域内命中的资源声明图节点键
// 入参: file 引用所在文件, elem 引用所在元素名, role 引用字段角色, rawValue 原始标识文本
// 返回: string 作用域限定的资源声明图节点键
func (c *resourceCatalog) resolveScopedDepNode(file, elem, role, rawValue string) string {
	if role == "AttachID" || slices.Contains([]string{"TemplateID", "PageID", "PageRef", "RefId"}, role) {
		return ""
	}
	id := editorResourceID(rawValue)
	if id == "" {
		return ""
	}
	expected := resourceExpectedKind(elem, role)
	scope := c.scopeForFile(file)
	if expected != "" {
		for _, resFile := range scope {
			if c.resKinds[resFile][id] == expected {
				return "res:" + resFile + ":" + expected + ":" + id
			}
		}
	}
	for _, resFile := range scope {
		if kind := c.resKinds[resFile][id]; kind != "" {
			return "res:" + resFile + ":" + kind + ":" + id
		}
	}
	return ""
}

// resourcePageFrame 保存流式页面扫描的图元归属及直接文本位置
type resourcePageFrame struct {
	name   string
	object string
	start  int
	role   string
	link   bool
}

// rawResourceRef 记录尚未按作用域归属的原始资源引用事件
type rawResourceRef struct {
	ref   ResourceReference
	elem  string
	value string
}

// resourceList 缓存当前阅读视图的资源摘要，不保留扫描使用的XML树
// 入参: ctx 取消上下文
// 返回: []ResourceInfo 内部只读摘要, error 扫描或取消错误
func (r *Reader) resourceList(ctx context.Context) ([]ResourceInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.resourceInfoCache != nil {
		return r.resourceInfoCache, nil
	}
	catalog, err := r.resourceCatalogMode(ctx, false)
	if err != nil {
		return nil, err
	}
	r.resourceInfoCache = catalog.items
	return r.resourceInfoCache, nil
}

// resourceCatalog 单次解析标准内容路径，复用字体扫描的XML树收集资源声明及引用
// 结果按文件名及原节点顺序归集，不把任意数字属性作为资源引用
// 入参: ctx 取消上下文
// 返回: *resourceCatalog 资源索引, error 结构或读取错误
func (r *Reader) resourceCatalog(ctx context.Context) (*resourceCatalog, error) {
	return r.resourceCatalogMode(ctx, true)
}

// resourceCatalogMode 只读查询不保留图元节点及页面原文，编辑查询保留原节点
// 入参: ctx 取消上下文, retain 是否保留编辑节点及原文
// 返回: *resourceCatalog 资源索引, error 结构或读取错误
func (r *Reader) resourceCatalogMode(ctx context.Context, retain bool) (*resourceCatalog, error) {
	catalog := &resourceCatalog{
		docRes:      make(map[string]bool),
		pageScopes:  make(map[string][]string),
		fileScopes:  make(map[string][]string),
		annotByPage: make(map[string][]string),
		extParents:  make(map[string]string),
		resKinds:    make(map[string]map[string]string),
		items:       []ResourceInfo{},
		nodes:       make(map[string]editorFontText),
		fileIDs:     make(map[string]string),
	}
	pages := make(map[string]int)
	pageIDToFile := make(map[string]string)
	isPageOrTpl := make(map[string]bool)
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return nil, err
	}
	for _, list := range [][]string{doc.CommonData.DocumentRes, doc.CommonData.PublicRes} {
		for _, loc := range slices.Backward(list) {
			if name := r.ResPath(loc); name != "" && name != "." {
				catalog.docRes[name] = true
				catalog.docScope = appendUniquePath(catalog.docScope, name)
			}
		}
	}
	for i, page := range doc.Pages.Page {
		name := cleanPackagePath(r.ResPath(page.BaseLoc))
		isPageOrTpl[name] = true
		if _, exists := pages[name]; exists {
			pages[name] = -1
		} else {
			pages[name] = i
		}
		if id := editorResourceID(page.ID); id != "" {
			if prev, exists := pageIDToFile[id]; exists && prev != name {
				pageIDToFile[id] = "*"
			} else {
				pageIDToFile[id] = name
			}
		}
	}
	for _, page := range doc.CommonData.TemplatePage {
		name := cleanPackagePath(r.ResPath(page.BaseLoc))
		isPageOrTpl[name] = true
		pages[name] = -1
	}
	var rawRefs []rawResourceRef
	declaredPageRes := make(map[string][]string)
	annotOwners := make(map[string]string)
	extResOwners := make(map[string]string)
	scan, err := scanEditorFontFilesMode(ctx, r, r.DocumentIndex(), func(file *editorFontXML, root *editorXML) error {
		name := file.name
		cleanName := cleanPackagePath(name)
		if root.matchesOFD("Res") {
			base := root.attr("BaseLoc")
			for _, group := range root.children {
				for _, node := range group.children {
					if group.matchesOFD("CompositeGraphicUnits") && node.matchesOFD("CompositeGraphicUnit") {
						cguID := editorResourceID(node.attr("ID"))
						if cguID != "" {
							if catalog.resKinds[cleanName] == nil {
								catalog.resKinds[cleanName] = make(map[string]string)
							}
							if catalog.resKinds[cleanName][cguID] == "" {
								catalog.resKinds[cleanName][cguID] = "CompositeGraphicUnit"
							}
							if loc := strings.TrimSpace(node.attr("BaseLoc")); loc != "" {
								extFile := cleanPackagePath(resolveResourcePath(name, base, loc))
								recordSingleOwner(catalog.extParents, extFile, "res:"+cleanName+":CompositeGraphicUnit:"+cguID)
								recordSingleOwner(extResOwners, extFile, cleanName)
							}
						}
						continue
					}
					item := ResourceInfo{ID: editorResourceID(node.attr("ID"))}
					field := ""
					if group.matchesOFD("Fonts") && node.matchesOFD("Font") {
						item.Kind, item.Name, field = "Font", node.attr("FontName"), "FontFile"
					} else if group.matchesOFD("MultiMedias") && node.matchesOFD("MultiMedia") && slices.Contains([]string{"Image", "Audio", "Video"}, node.attr("Type")) {
						item.Kind, item.Format, field = node.attr("Type"), node.attr("Format"), "MediaFile"
					} else if group.matchesOFD("ColorSpaces") && node.matchesOFD("ColorSpace") {
						item.Kind, item.Name = "ColorSpace", node.attr("Type")
						if profile := strings.TrimSpace(node.attr("Profile")); profile != "" {
							item.File = resolveResourcePath(name, base, profile)
							item.Format = "ICC"
						}
					} else if group.matchesOFD("DrawParams") && node.matchesOFD("DrawParam") {
						item.Kind, item.Name = "DrawParam", "DrawParam "+item.ID
						if loc := strings.TrimSpace(node.attr("Link")); loc != "" && item.ID != "" {
							extFile := cleanPackagePath(resolveResourcePath(name, base, loc))
							recordSingleOwner(catalog.extParents, extFile, "res:"+cleanName+":DrawParam:"+item.ID)
							recordSingleOwner(extResOwners, extFile, cleanName)
						}
					} else {
						continue
					}
					if item.ID == "" {
						return fmt.Errorf("invalid resource ID")
					}
					if target := node.child(field); target != nil {
						location := strings.TrimSpace(editorImportText(file.data, target))
						if location != "" {
							item.File = resolveResourcePath(name, base, location)
						}
					}
					if item.Name == "" {
						item.Name = path.Base(item.File)
					}
					if err := catalog.add(item, editorFontText{file: file, node: node}); err != nil {
						return err
					}
				}
			}
		}
		if root.matchesOFD("Page") {
			for _, child := range root.children {
				if child.matchesOFD("PageRes") {
					loc := strings.TrimSpace(editorImportText(file.data, child))
					if loc != "" {
						declaredPageRes[cleanName] = append(declaredPageRes[cleanName], cleanPackagePath(resolveResourcePath(name, "", loc)))
					}
				}
			}
		}
		if root.matchesOFD("Annotations") {
			for _, child := range root.children {
				if !child.matchesOFD("Page") {
					continue
				}
				pageID := editorResourceID(child.attr("PageID"))
				loc := strings.TrimSpace(child.attr("FileLoc"))
				if loc == "" {
					if locNode := child.child("FileLoc"); locNode != nil {
						loc = strings.TrimSpace(editorImportText(file.data, locNode))
					}
				}
				if loc == "" || pageID == "" {
					continue
				}
				annotFile := cleanPackagePath(resolveResourcePath(name, "", loc))
				catalog.annotByPage[pageID] = appendUniquePath(catalog.annotByPage[pageID], annotFile)
				recordSingleOwner(annotOwners, annotFile, pageIDToFile[pageID])
			}
		}
		page := -1
		if index, ok := pages[cleanName]; ok {
			page = index
		}
		isResRoot := root.matchesOFD("Res")
		var visit func(*editorXML, string) error
		visit = func(node *editorXML, object string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !node.matchesOFD(node.name.Local) || node.matchesOFD("Attachments") {
				return nil
			}
			if slices.Contains([]string{"TextObject", "PathObject", "ImageObject", "CompositeObject"}, node.name.Local) {
				object = node.attr("ID")
			}
			if retain && node.matchesOFD("ImageObject") {
				catalog.images = append(catalog.images, editorFontText{file: file, node: node})
			}
			add := func(role, value string) {
				if value != "" {
					rawRefs = append(rawRefs, rawResourceRef{
						ref:   ResourceReference{File: name, Page: page, Object: object, Role: role},
						elem:  node.name.Local,
						value: value,
					})
				}
			}
			for _, attr := range node.attrs {
				if attr.Name.Space == "" && (editorResourceReferenceKind(attr.Name.Local, isResRoot) == editorReferenceID || attr.Name.Local == "AttachID") {
					add(attr.Name.Local, attr.Value)
				}
			}
			if slices.Contains([]string{"Thumbnail", "Substitution", "DefaultCS"}, node.name.Local) {
				add(node.name.Local, strings.TrimSpace(editorImportText(file.data, node)))
			}
			for _, child := range node.children {
				if err := visit(child, object); err != nil {
					return err
				}
			}
			return nil
		}
		return visit(root, "")
	}, retain, func(file *editorFontXML) (bool, bool, []string, error) {
		return scanResourcePage(ctx, file, pages, declaredPageRes, &rawRefs)
	})
	if err != nil {
		return nil, err
	}
	catalog.scan = scan
	order := make([]int, len(catalog.items))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Compare(catalog.itemFiles[a], catalog.itemFiles[b])
	})
	sortedItems := make([]ResourceInfo, len(catalog.items))
	sortedFiles := make([]string, len(catalog.itemFiles))
	for i, idx := range order {
		sortedItems[i] = catalog.items[idx]
		sortedFiles[i] = catalog.itemFiles[idx]
	}
	catalog.items = sortedItems
	catalog.itemFiles = sortedFiles
	for i := len(catalog.items) - 1; i >= 0; i-- {
		key := catalog.items[i].Kind + "/" + catalog.items[i].ID
		if node, ok := catalog.nodes[catalog.itemFiles[i]+":"+key]; ok {
			catalog.nodes[key] = node
		}
	}
	slices.SortStableFunc(catalog.images, func(a, b editorFontText) int {
		return cmp.Compare(a.file.name, b.file.name)
	})
	attachments, err := r.attachmentsContext(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, attachment := range attachments {
		item := ResourceInfo{Kind: "Attachment", ID: attachment.ID, Name: attachment.Name, Format: attachment.Format, File: r.ResPath(attachment.FileLoc), Complete: !scan.unsafe}
		if err := catalog.add(item, editorFontText{}); err != nil {
			return nil, err
		}
	}
	resPageOwners := make(map[string][]string)
	for pFile := range isPageOrTpl {
		var scope []string
		for _, res := range slices.Backward(declaredPageRes[pFile]) {
			scope = appendUniquePath(scope, res)
			resPageOwners[res] = appendUniquePath(resPageOwners[res], pFile)
		}
		for _, dRes := range catalog.docScope {
			scope = appendUniquePath(scope, dRes)
		}
		catalog.pageScopes[pFile] = scope
		catalog.fileScopes[pFile] = scope
	}
	for pFile, local := range declaredPageRes {
		if _, exists := catalog.pageScopes[pFile]; exists {
			continue
		}
		var scope []string
		for _, res := range slices.Backward(local) {
			scope = appendUniquePath(scope, res)
			resPageOwners[res] = appendUniquePath(resPageOwners[res], pFile)
		}
		for _, dRes := range catalog.docScope {
			scope = appendUniquePath(scope, dRes)
		}
		catalog.pageScopes[pFile] = scope
		catalog.fileScopes[pFile] = scope
	}
	for resFile := range catalog.resKinds {
		if catalog.docRes[resFile] {
			catalog.fileScopes[resFile] = catalog.docScope
			continue
		}
		if owners := resPageOwners[resFile]; len(owners) == 1 {
			catalog.fileScopes[resFile] = catalog.pageScopes[owners[0]]
		} else {
			scope := []string{resFile}
			for _, dRes := range catalog.docScope {
				scope = appendUniquePath(scope, dRes)
			}
			catalog.fileScopes[resFile] = scope
		}
	}
	for annotFile, ownerPage := range annotOwners {
		if ownerPage != "" && ownerPage != "*" {
			catalog.fileScopes[annotFile] = catalog.scopeForFile(ownerPage)
		}
	}
	for extFile, ownerRes := range extResOwners {
		if ownerRes != "" && ownerRes != "*" {
			catalog.fileScopes[extFile] = catalog.scopeForFile(ownerRes)
		}
	}
	catalog.itemIndex = make(map[string]int, len(catalog.items))
	for i, item := range catalog.items {
		if item.Kind == "Attachment" {
			catalog.itemIndex["Attachment\x00"+item.ID] = i
		} else {
			catalog.itemIndex[catalog.itemFiles[i]+"\x00"+item.Kind+"\x00"+item.ID] = i
		}
	}
	itemRefs := make([][]ResourceReference, len(catalog.items))
	for _, raw := range rawRefs {
		if idx, ok := catalog.resolveRefIndex(raw.ref.File, raw.elem, raw.ref.Role, raw.value); ok {
			itemRefs[idx] = append(itemRefs[idx], raw.ref)
		}
	}
	for i := range catalog.items {
		item := &catalog.items[i]
		item.Complete = !scan.unsafe
		item.References = itemRefs[i]
		slices.SortStableFunc(item.References, func(a, b ResourceReference) int {
			return cmp.Compare(a.File, b.File)
		})
		if item.File != "" {
			if data, ok := r.files[cleanPackagePath(item.File)]; ok {
				item.Size = uint64(len(data))
			} else if file, ok := r.packageFile(item.File); ok {
				item.Size = file.UncompressedSize64
			} else if size, ok := r.encryptedSource().size(item.File); ok {
				item.Size = size
			} else {
				item.Missing = true
			}
		} else {
			item.Missing = item.Kind != "Font" && item.Kind != "ColorSpace" && item.Kind != "DrawParam"
		}
	}
	return catalog, ctx.Err()
}

// locateResourceFileContext 仅扫描资源索引文件定位单个资源路径，优先返回含有效内嵌文件的定义
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识
// 返回: string 资源文件路径, bool 是否找到声明, error 读取或取消错误
func (r *Reader) locateResourceFileContext(ctx context.Context, kind, id string) (string, bool, error) {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return "", false, err
	}
	matchIn := func(parsed *parsedRes) (string, bool) {
		if parsed == nil {
			return "", false
		}
		switch kind {
		case "Font":
			if f, ok := parsed.fonts.get(id); ok && f != nil {
				return f.FontFile, true
			}
		case "Image", "Audio", "Video":
			if m, ok := parsed.medias.get(id); ok && m.Type == kind {
				return m.MediaFile, true
			}
		case "ColorSpace":
			if cs, ok := parsed.colorSpaces.get(id); ok && cs != nil {
				return cs.Profile, true
			}
		case "DrawParam":
			if dp, ok := parsed.drawParams.get(id); ok && dp != nil {
				return "", true
			}
		}
		return "", false
	}
	foundAny := false
	fallbackLoc := ""
	consider := func(loc string) bool {
		foundAny = true
		if loc == "" {
			return false
		}
		if r.hasPackageFile(r.ResPath(loc)) {
			fallbackLoc = loc
			return true
		}
		if fallbackLoc == "" {
			fallbackLoc = loc
		}
		return false
	}
	seenRes := make(map[string]bool)
	for _, list := range [][]string{doc.CommonData.DocumentRes, doc.CommonData.PublicRes} {
		for _, res := range slices.Backward(list) {
			resFull := cleanPackagePath(r.ResPath(res))
			if resFull == "" || resFull == "." || seenRes[resFull] {
				continue
			}
			seenRes[resFull] = true
			parsed, err := r.parseResFileContext(ctx, resFull)
			if err != nil {
				return "", false, err
			}
			if loc, ok := matchIn(parsed); ok && consider(loc) {
				return fallbackLoc, true, nil
			}
		}
	}
	checkPageHeader := func(p Page) (bool, error) {
		hdr, hdrErr := r.readPageHeaderContext(ctx, p)
		if hdrErr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		}
		pageFullPath := r.ResPath(p.BaseLoc)
		for _, res := range slices.Backward(hdr.PageRes) {
			resFull := cleanPackagePath(resolveResourcePath(pageFullPath, "", res))
			if resFull == "" || resFull == "." || seenRes[resFull] {
				continue
			}
			seenRes[resFull] = true
			parsed, parseErr := r.parseResFileContext(ctx, resFull)
			if parseErr != nil {
				return false, parseErr
			}
			if loc, ok := matchIn(parsed); ok && consider(loc) {
				return true, nil
			}
		}
		return false, nil
	}
	for _, p := range doc.Pages.Page {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if done, err := checkPageHeader(p); err != nil || done {
			return fallbackLoc, done, err
		}
	}
	for _, tpl := range doc.CommonData.TemplatePage {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if done, err := checkPageHeader(Page{BaseLoc: tpl.BaseLoc}); err != nil || done {
			return fallbackLoc, done, err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	return fallbackLoc, foundAny, nil
}

// scanResourcePage 流式收集常规页面引用与PageRes声明，特殊结构回退到完整节点扫描
// 入参: ctx 取消上下文, file 页面原文, pages 文件页码, declaredPageRes 页面局部资源声明, rawRefs 原始引用列表
// 返回: bool 是否已处理, bool 引用是否不安全, []string 依赖路径, error 解析或取消错误
func scanResourcePage(ctx context.Context, file *editorFontXML, pages map[string]int, declaredPageRes map[string][]string, rawRefs *[]rawResourceRef) (bool, bool, []string, error) {
	d := xml.NewDecoder(&editorXMLReader{data: file.data, ctx: ctx})
	var stack []resourcePageFrame
	var links []string
	var localPageRes []string
	var localRefs []rawResourceRef
	cleanName := cleanPackagePath(file.name)
	page, exists := pages[cleanName]
	if !exists {
		page = -1
	}
	seen, unsafe := false, false
	add := func(role, value, object, elem string) {
		if value != "" {
			localRefs = append(localRefs, rawResourceRef{
				ref:   ResourceReference{File: file.name, Page: page, Object: object, Role: role},
				elem:  elem,
				value: value,
			})
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return true, false, nil, err
		}
		start := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			if !seen {
				return false, false, nil, nil
			}
			if len(localPageRes) > 0 {
				declaredPageRes[cleanName] = append(declaredPageRes[cleanName], localPageRes...)
			}
			*rawRefs = append(*rawRefs, localRefs...)
			return true, unsafe, links, nil
		}
		if err != nil {
			return true, false, nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				if seen {
					return true, false, nil, fmt.Errorf("multiple XML roots")
				}
				if token.Name.Local != "Page" {
					return false, false, nil, nil
				}
				seen = true
			}
			if classifyOFDNamespace(token.Name.Space) == ofdXMLUnknown || slices.Contains([]string{"Attachments", "Signatures", "CustomTags", "Extensions", "ExtendData", "Data"}, token.Name.Local) {
				return false, false, nil, nil
			}
			for _, attr := range token.Attr {
				if attr.Name.Space != "" && attr.Name.Space != "xmlns" && attr.Name.Space != XMLNamespace {
					return false, false, nil, nil
				}
			}
			frame := resourcePageFrame{name: token.Name.Local, start: start}
			parent := ""
			if len(stack) != 0 {
				outer := stack[len(stack)-1]
				if outer.role != "" || outer.link {
					return false, false, nil, nil
				}
				frame.object, parent = outer.object, outer.name
			}
			node := editorXML{name: token.Name, attrs: token.Attr}
			if slices.Contains([]string{"TextObject", "PathObject", "ImageObject", "CompositeObject"}, frame.name) {
				frame.object = node.attr("ID")
			}
			if frame.name == "TextObject" || frame.name == "Text" && parent == "Area" {
				unsafe = unsafe || editorResourceID(node.attr("Font")) == ""
			}
			for _, attr := range token.Attr {
				if attr.Name.Space == "" && (editorResourceReferenceKind(attr.Name.Local, false) == editorReferenceID || attr.Name.Local == "AttachID") {
					add(attr.Name.Local, attr.Value, frame.object, frame.name)
				}
			}
			if slices.Contains([]string{"Thumbnail", "Substitution", "DefaultCS"}, frame.name) {
				frame.role = frame.name
			}
			value := ""
			switch frame.name {
			case "PublicRes", "DocumentRes", "PageRes", "Annotations":
				frame.link = true
			case "Page", "TemplatePage", "CompositeGraphicUnit":
				value = node.attr("BaseLoc")
			case "PageAnnot":
				value = node.attr("FileLoc")
			case "DrawParam":
				value = node.attr("Link")
			}
			if value != "" {
				links = append(links, resolveResourcePath(file.name, "", value))
			}
			stack = append(stack, frame)
		case xml.EndElement:
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if frame.role != "" || frame.link {
				node := editorXML{start: frame.start, end: int(d.InputOffset())}
				value := strings.TrimSpace(editorImportText(file.data, &node))
				if frame.role != "" {
					add(frame.role, value, frame.object, frame.name)
				}
				if frame.link && value != "" {
					resolved := resolveResourcePath(file.name, "", value)
					links = append(links, resolved)
					if frame.name == "PageRes" && len(stack) == 1 && stack[0].name == "Page" {
						localPageRes = append(localPageRes, cleanPackagePath(resolved))
					}
				}
			}
		}
	}
}

// resourceMatches 判断资源条目是否匹配指定的资源类型与标识
// 入参: item 资源条目, kind 资源类型, id 资源标识
// 返回: bool 是否匹配
func resourceMatches(item ResourceInfo, kind, id string) bool {
	return item.Kind == kind && (item.ID == id || kind != "Attachment" && sameResourceID(item.ID, id))
}

// findResource 查找当前视图内的资源，不接受调用方提供的任意包路径
// 入参: items 资源列表, kind 资源类型, id 资源标识
// 返回: ResourceInfo 资源信息, error 未找到资源
func findResource(items []ResourceInfo, kind, id string) (ResourceInfo, error) {
	item, _, err := findResourceWithCount(items, kind, id)
	return item, err
}

// findResourceWithCount 查找首个匹配资源并统计同名同类型声明数量
// 入参: items 资源列表, kind 资源类型, id 资源标识
// 返回: ResourceInfo 首个匹配资源, int 匹配总数, error 未找到资源
func findResourceWithCount(items []ResourceInfo, kind, id string) (ResourceInfo, int, error) {
	var first ResourceInfo
	count := 0
	for _, item := range items {
		if resourceMatches(item, kind, id) {
			if count == 0 {
				first = item
			}
			count++
		}
	}
	if count == 0 {
		return ResourceInfo{}, 0, fmt.Errorf("resource not found: %s/%s", kind, id)
	}
	return first, count, nil
}

// findResourceRefTarget 按Resources与ResourceReferences一致的顺序定位引用所属声明与引用项
// 入参: items 资源列表, kind 资源类型, id 资源标识, reference 全局引用下标
// 返回: int 声明条目索引, ResourceReference 命中引用, error 未找到资源或引用错误
func findResourceRefTarget(items []ResourceInfo, kind, id string, reference int) (int, ResourceReference, error) {
	found := false
	rem := reference
	for i, item := range items {
		if !resourceMatches(item, kind, id) {
			continue
		}
		found = true
		if rem >= 0 && rem < len(item.References) {
			return i, item.References[rem], nil
		}
		rem -= len(item.References)
	}
	if !found {
		return -1, ResourceReference{}, fmt.Errorf("resource not found: %s/%s", kind, id)
	}
	return -1, ResourceReference{}, fmt.Errorf("resource reference not found")
}

// resourceExpectedKind 根据承载元素与引用角色推断标准目标资源类别
// 入参: elem 元素局部名, role 引用字段名
// 返回: string 目标资源类别，不确定时为空
func resourceExpectedKind(elem, role string) string {
	switch role {
	case "AttachID":
		return "Attachment"
	case "Font":
		return "Font"
	case "DrawParam", "Relative":
		return "DrawParam"
	case "ColorSpace", "DefaultCS":
		return "ColorSpace"
	case "ImageMask", "Substitution", "Thumbnail":
		return "Image"
	case "ResourceID":
		switch elem {
		case "ImageObject":
			return "Image"
		case "Sound":
			return "Audio"
		case "Movie":
			return "Video"
		case "DrawParam":
			return "DrawParam"
		case "CompositeObject", "CompositeGraphicUnit":
			return "CompositeGraphicUnit"
		}
	}
	return ""
}

// appendUniquePath 向切片追加不重复的非空路径并保持首次出现顺序
// 入参: list 原路径列表, value 待追加路径
// 返回: []string 追加后的路径列表
func appendUniquePath(list []string, value string) []string {
	if value == "" || value == "." || slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

// recordSingleOwner 记录文件唯一归属者，多归属时标记为星号避免错误绑定单一页面作用域
// 入参: owners 归属映射, key 被引用文件, owner 归属标识
func recordSingleOwner(owners map[string]string, key, owner string) {
	if key == "" || owner == "" {
		return
	}
	if prev, exists := owners[key]; exists && prev != owner {
		owners[key] = "*"
		return
	}
	owners[key] = owner
}

// resourceReferenceOwner 查找承载引用的资源定义或最外层图元，图层只承接其自身的绘制参数
// 入参: node 引用节点
// 返回: string 图元或资源标识，无法映射时为空
func resourceReferenceOwner(node *editorXML) string {
	id := ""
	for current := node; current != nil; current = current.parent {
		if !current.matchesOFD(current.name.Local) {
			return ""
		}
		if slices.Contains([]string{"TextObject", "PathObject", "ImageObject", "CompositeObject", "CompositeGraphicUnit"}, current.name.Local) || current.parent != nil && current.name.Local == editorResourceElement(current.parent.name.Local) {
			id = editorResourceID(current.attr("ID"))
		}
		if id == "" && current.matchesOFD("Layer") {
			id = editorResourceID(current.attr("ID"))
		}
	}
	return id
}
