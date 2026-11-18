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
	item, err := findResource(items, kind, id)
	if err != nil {
		return nil, err
	}
	if offset >= len(item.References) {
		return []ResourceReference{}, ctx.Err()
	}
	return slices.Clone(item.References[offset : offset+min(limit, len(item.References)-offset)]), ctx.Err()
}

// OpenResource 打开当前资源的原始文件，ColorSpace导出ICC，无ICC或内联DrawParam不提供文件流
// 外部字体同样无可导出的内嵌文件，定义读取使用ColorSpace或DrawParam
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识
// 返回: io.ReadCloser 文件流，由调用方关闭, error 资源或文件错误
func (r *Reader) OpenResource(ctx context.Context, kind, id string) (io.ReadCloser, error) {
	items, err := r.resourceList(ctx)
	if err != nil {
		return nil, err
	}
	item, err := findResource(items, kind, id)
	if err != nil {
		return nil, err
	}
	if item.File == "" {
		return nil, fmt.Errorf("resource has no embedded file")
	}
	stream, err := r.openFile(item.File)
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
	media, ok := resourceValue(r.mediaCache, id)
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
	item, err := findResource(catalog.items, kind, id)
	if err != nil {
		return nil, err
	}
	if reference < 0 || reference >= len(item.References) {
		return nil, fmt.Errorf("resource reference not found")
	}
	ref := item.References[reference]
	doc, err := r.Reader.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool)
	users := make(map[string][]string)
	for _, file := range catalog.scan.files {
		root, err := parseEditorXMLContext(ctx, file.data)
		if err != nil {
			return nil, err
		}
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
			owner := resourceReferenceOwner(node)
			if owner != "" && resourceColorNode(node) && node.attr("ColorSpace") == "" && doc.CommonData.DefaultCS != 0 {
				dependency := fmt.Sprint(doc.CommonData.DefaultCS)
				users[dependency] = append(users[dependency], owner)
				if ref.Role == "DefaultCS" && sameResourceID(dependency, item.ID) {
					wanted[owner] = true
				}
			}
			add := func(role, value string) {
				if owner == "" {
					return
				}
				if role != "AttachID" {
					if dependency := editorResourceID(value); dependency != "" && dependency != owner {
						users[dependency] = append(users[dependency], owner)
					}
				}
				match := value == item.ID
				if kind != "Attachment" {
					match = sameResourceID(value, item.ID)
				}
				if file.name == ref.File && object == ref.Object && role == ref.Role && match {
					wanted[owner] = true
				}
			}
			for _, attr := range node.attrs {
				if attr.Name.Space == "" && (editorResourceReferenceKind(attr.Name.Local, root.matchesOFD("Res")) == editorReferenceID || attr.Name.Local == "AttachID") {
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
			if owner != "" && parent != nil && parent.matchesOFD("Layer") {
				if layer := editorResourceID(parent.attr("ID")); layer != "" && layer != owner {
					users[layer] = append(users[layer], owner)
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
	queue := slices.Collect(maps.Keys(wanted))
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := queue[0]
		queue = queue[1:]
		for _, user := range users[id] {
			if !wanted[user] {
				wanted[user] = true
				queue = append(queue, user)
			}
		}
	}
	view := *r
	view.RenderAnnotations, view.pageContext = true, ctx
	var result []ResourceLocation
	for page := range doc.Pages.Page {
		if ref.Page >= 0 && ref.Page != page || len(wanted) == 0 {
			continue
		}
		content, err := r.Reader.PageContentByIndexContext(ctx, page)
		if err != nil {
			return nil, err
		}
		targets := make(map[string]*StructureTarget, len(wanted))
		for id := range wanted {
			targets[id] = &StructureTarget{ID: id}
		}
		visitor := &structureVisitor{ctx: ctx, renderer: &view, targets: targets, boundsOnly: true}
		if err := view.WalkPage(content, visitor); err != nil {
			return nil, err
		}
		for _, id := range slices.Sorted(maps.Keys(targets)) {
			box := targets[id].Bounds
			if box.W > 0 && box.H > 0 {
				result = append(result, ResourceLocation{Page: page + 1, Object: id, Bounds: box})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// resourceCatalog 保存当前资源声明和原文位置，供查询及隔离修改复用
type resourceCatalog struct {
	scan   *editorFontDocument
	items  []ResourceInfo
	nodes  map[string]editorFontText
	images []editorFontText
}

// add 加入资源声明，拒绝同一类型内的重复标识
// 入参: item 资源信息, node 声明位置
// 返回: error 标识冲突
func (c *resourceCatalog) add(item ResourceInfo, node editorFontText) error {
	key := item.Kind + "/" + item.ID
	if _, exists := c.nodes[key]; exists {
		return fmt.Errorf("duplicate resource %s", key)
	}
	if item.Kind != "Attachment" {
		for _, kind := range []string{"Font", "Image", "Audio", "Video", "ColorSpace", "DrawParam"} {
			if _, exists := c.nodes[kind+"/"+item.ID]; exists {
				return fmt.Errorf("conflicting resource ID %s", item.ID)
			}
		}
	}
	c.nodes[key] = node
	c.items = append(c.items, item)
	return nil
}

// resourcePageFrame 保存流式页面扫描的图元归属及直接文本位置
type resourcePageFrame struct {
	name   string
	object string
	start  int
	role   string
	link   bool
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
	catalog := &resourceCatalog{nodes: make(map[string]editorFontText), items: []ResourceInfo{}}
	pages := make(map[string]int)
	doc, err := r.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	for i, page := range doc.Pages.Page {
		name := cleanPackagePath(r.ResPath(page.BaseLoc))
		if _, exists := pages[name]; exists {
			pages[name] = -1
		} else {
			pages[name] = i
		}
	}
	for _, page := range doc.CommonData.TemplatePage {
		pages[cleanPackagePath(r.ResPath(page.BaseLoc))] = -1
	}
	refs := make(map[string][]ResourceReference)
	scan, err := scanEditorFontFilesMode(ctx, r, r.DocumentIndex(), func(file *editorFontXML, root *editorXML) error {
		name := file.name
		if root.matchesOFD("Res") {
			for _, group := range root.children {
				for _, node := range group.children {
					item := ResourceInfo{ID: editorResourceID(node.attr("ID"))}
					field := ""
					if group.matchesOFD("Fonts") && node.matchesOFD("Font") {
						item.Kind, item.Name, field = "Font", node.attr("FontName"), "FontFile"
					} else if group.matchesOFD("MultiMedias") && node.matchesOFD("MultiMedia") && slices.Contains([]string{"Image", "Audio", "Video"}, node.attr("Type")) {
						item.Kind, item.Format, field = node.attr("Type"), node.attr("Format"), "MediaFile"
					} else if group.matchesOFD("ColorSpaces") && node.matchesOFD("ColorSpace") {
						item.Kind, item.Name = "ColorSpace", node.attr("Type")
						if profile := strings.TrimSpace(node.attr("Profile")); profile != "" {
							item.File = resolveResourcePath(name, root.attr("BaseLoc"), profile)
							item.Format = "ICC"
						}
					} else if group.matchesOFD("DrawParams") && node.matchesOFD("DrawParam") {
						item.Kind, item.Name = "DrawParam", "DrawParam "+item.ID
					} else {
						continue
					}
					if item.ID == "" {
						return fmt.Errorf("invalid resource ID")
					}
					if target := node.child(field); target != nil {
						location := strings.TrimSpace(editorImportText(file.data, target))
						if location != "" {
							item.File = resolveResourcePath(name, root.attr("BaseLoc"), location)
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
		page := -1
		if index, ok := pages[name]; ok {
			page = index
		}
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
			add := func(role, id string) {
				key := editorResourceID(id)
				if role == "AttachID" {
					key = "Attachment/" + id
				}
				if key != "" {
					refs[key] = append(refs[key], ResourceReference{File: name, Page: page, Object: object, Role: role})
				}
			}
			for _, attr := range node.attrs {
				if attr.Name.Space == "" && (editorResourceReferenceKind(attr.Name.Local, root.matchesOFD("Res")) == editorReferenceID || attr.Name.Local == "AttachID") {
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
		return scanResourcePage(ctx, file, pages, refs)
	})
	if err != nil {
		return nil, err
	}
	catalog.scan = scan
	slices.SortStableFunc(catalog.items, func(a, b ResourceInfo) int {
		return cmp.Compare(catalog.nodes[a.Kind+"/"+a.ID].file.name, catalog.nodes[b.Kind+"/"+b.ID].file.name)
	})
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
	for i := range catalog.items {
		item := &catalog.items[i]
		item.Complete = !scan.unsafe
		key := item.ID
		if item.Kind == "Attachment" {
			key = "Attachment/" + item.ID
		}
		item.References = refs[key]
		slices.SortStableFunc(item.References, func(a, b ResourceReference) int {
			return cmp.Compare(a.File, b.File)
		})
		if item.File != "" {
			if data, ok := r.files[cleanPackagePath(item.File)]; ok {
				item.Size = uint64(len(data))
			} else if file, ok := r.packageFile(item.File); ok {
				item.Size = file.UncompressedSize64
			} else {
				item.Missing = true
			}
		} else {
			item.Missing = item.Kind != "Font" && item.Kind != "ColorSpace" && item.Kind != "DrawParam"
		}
	}
	return catalog, ctx.Err()
}

// scanResourcePage 流式收集常规页面引用，特殊结构回退到完整节点扫描
// 入参: ctx 取消上下文, file 页面原文, pages 文件页码, refs 已收集引用
// 返回: bool 是否已处理, bool 引用是否不安全, []string 依赖路径, error 解析或取消错误
func scanResourcePage(ctx context.Context, file *editorFontXML, pages map[string]int, refs map[string][]ResourceReference) (bool, bool, []string, error) {
	d := xml.NewDecoder(&editorXMLReader{data: file.data, ctx: ctx})
	var stack []resourcePageFrame
	var links []string
	local := make(map[string][]ResourceReference)
	page, exists := pages[file.name]
	if !exists {
		page = -1
	}
	seen, unsafe := false, false
	add := func(role, id, object string) {
		key := editorResourceID(id)
		if role == "AttachID" {
			key = "Attachment/" + id
		}
		if key != "" {
			local[key] = append(local[key], ResourceReference{File: file.name, Page: page, Object: object, Role: role})
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
			for key, values := range local {
				refs[key] = append(refs[key], values...)
			}
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
				if attr.Name.Space != "" && attr.Name.Space != "xmlns" && attr.Name.Space != XMLNamespace && !textMetadataAttribute(token.Name, attr) {
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
					add(attr.Name.Local, attr.Value, frame.object)
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
					add(frame.role, value, frame.object)
				}
				if frame.link && value != "" {
					links = append(links, resolveResourcePath(file.name, "", value))
				}
			}
		}
	}
}

// findResource 查找当前视图内的资源，不接受调用方提供的任意包路径
// 入参: items 资源列表, kind 资源类型, id 资源标识
// 返回: ResourceInfo 资源信息, error 未找到资源
func findResource(items []ResourceInfo, kind, id string) (ResourceInfo, error) {
	for _, item := range items {
		if item.Kind == kind && (item.ID == id || kind != "Attachment" && sameResourceID(item.ID, id)) {
			return item, nil
		}
	}
	return ResourceInfo{}, fmt.Errorf("resource not found: %s/%s", kind, id)
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
