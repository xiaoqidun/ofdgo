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
	"path"
	"slices"
	"strconv"
	"strings"
)

// ColorSpace 获取当前文档及版本的颜色空间定义，不读取或解析ICC文件
// 入参: ctx 取消上下文, id 资源标识
// 返回: *ColorSpace 独立副本，Profile为包内绝对引用, error 查询或解析错误
func (r *Reader) ColorSpace(ctx context.Context, id string) (*ColorSpace, error) {
	catalog, err := r.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	item, err := findResource(catalog.items, "ColorSpace", id)
	if err != nil {
		return nil, err
	}
	var space ColorSpace
	if err := catalog.styleDefinition(ctx, item, &space); err != nil {
		return nil, err
	}
	space.ID, space.Profile = item.ID, ""
	if item.File != "" {
		space.Profile = "/" + item.File
	}
	return &space, ctx.Err()
}

// DrawParam 获取当前文档的绘制参数，解析继承时拒绝缺失、类型错误及循环引用
// 入参: ctx 取消上下文, id 资源标识, resolved 是否合并基础绘制参数
// 返回: *DrawParam 独立副本，未解析时保留Relative, error 查询或继承错误
func (r *Reader) DrawParam(ctx context.Context, id string, resolved bool) (*DrawParam, error) {
	catalog, err := r.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var chain []DrawParam
	seen := make(map[string]bool)
	for {
		item, err := findResource(catalog.items, "DrawParam", id)
		if err != nil {
			return nil, err
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("cyclic draw parameter %s", item.ID)
		}
		seen[item.ID] = true
		var draw DrawParam
		if err := catalog.styleDefinition(ctx, item, &draw); err != nil {
			return nil, err
		}
		draw.ID = item.ID
		if !resolved {
			return &draw, nil
		}
		if draw.ResourceID != "" || draw.Link != "" || draw.BaseLoc != "" {
			return nil, fmt.Errorf("external draw parameter cannot be resolved")
		}
		chain = append(chain, draw)
		if draw.Relative == "" {
			break
		}
		id = draw.Relative
	}
	result := &DrawParam{}
	for i := len(chain) - 1; i >= 0; i-- {
		result = mergeDrawParam(*result, &chain[i])
	}
	return result, ctx.Err()
}

// ColorSpace 读取编辑器中的颜色空间，包括尚未绑定图元的新注册定义
// 入参: id 颜色空间标识
// 返回: *ColorSpace 独立副本，Profile由库管理, error 资源不存在
func (e *Editor) ColorSpace(id string) (*ColorSpace, error) {
	for _, resource := range e.resources {
		if resource.space != nil && sameResourceID(resource.space.ID, id) {
			space := *resource.space
			space.Palette, space.profile = slices.Clone(space.Palette), nil
			return &space, nil
		}
	}
	if e.source != nil {
		return e.source.reader.ColorSpace(context.Background(), id)
	}
	return nil, fmt.Errorf("color space %q not found", id)
}

// ImportColorSpace 持久注册颜色空间及可选ICC，未绑定图元时仍保留声明并支持撤销
// 入参: ctx 取消上下文, space 类型、位深及调色板，ID和Profile由库生成, profile 完整ICC或nil
// 返回: string 新资源标识, error 定义校验或隔离提交错误
func (e *Editor) ImportColorSpace(ctx context.Context, space ColorSpace, profile []byte) (string, error) {
	return e.importResourceStyle(ctx, func(working *Editor) (string, []byte, map[string][]byte, error) {
		id, err := working.AddColorSpace(space, profile)
		if err != nil {
			return "", nil, nil, err
		}
		resource := working.resources[len(working.resources)-1]
		data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) { x.resources(nil, nil, []ColorSpace{*resource.space}) })
		parts := make(map[string][]byte)
		if resource.name != "" {
			parts[resource.name] = bytes.Clone(resource.data)
		}
		return id, data, parts, err
	})
}

// ImportDrawParam 持久注册共享绘制参数，未绑定图元时仍保留声明并支持撤销
// 入参: ctx 取消上下文, draw 参数定义，ID由库生成，引用可来自当前文档或编辑器的待注册资源
// 返回: string 新资源标识, error 定义校验或隔离提交错误
func (e *Editor) ImportDrawParam(ctx context.Context, draw DrawParam) (string, error) {
	return e.importResourceStyle(ctx, func(working *Editor) (string, []byte, map[string][]byte, error) {
		id, err := working.addDrawParam(ctx, draw)
		if err != nil {
			return "", nil, nil, err
		}
		resource := working.resources[len(working.resources)-1]
		return id, resource.data, make(map[string][]byte), nil
	})
}

// ReplaceColorSpace 原子替换颜色空间，保留当前文档内的资源标识并隔离其他文档及历史版本
// 入参: ctx 取消上下文, id 既有标识, space 新定义，ID和Profile由库管理, profile 完整ICC，nil移除配置
// 返回: error 位深、调色板、ICC或既有颜色不兼容错误，失败不改变文档，成功支持撤销
func (e *Editor) ReplaceColorSpace(ctx context.Context, id string, space ColorSpace, profile []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	working := NewEditor()
	if _, err := working.AddColorSpace(space, profile); err != nil {
		return err
	}
	space = *working.resources[0].space
	return e.changeResourceStyle(ctx, "ColorSpace", id, func(catalog *resourceCatalog, item ResourceInfo, changes map[string][]byte) error {
		space.ID = item.ID
		if err := validateResourceColors(ctx, catalog, space); err != nil {
			return err
		}
		space.Profile = ""
		if len(profile) != 0 {
			name := packageAvailableName(catalog.scan.reader, changes, path.Join(e.packageDirectory(), "Res", "ColorSpaces", "ColorSpace_"+item.ID+".icc"))
			space.Profile = "/" + name
			changes[name] = bytes.Clone(profile)
		}
		data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) { x.resources(nil, nil, []ColorSpace{space}) })
		if err != nil {
			return err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		node := root.child("ColorSpaces").child("ColorSpace")
		fragment, err := editorXMLStandaloneContext(ctx, data[node.start:node.end], node)
		if err != nil {
			return err
		}
		return replaceResourceStyle(ctx, catalog, item, fragment, changes)
	})
}

// ReplaceDrawParam 原子替换共享绘制参数，保留全部既有绑定及图元的局部覆盖
// 入参: ctx 取消上下文, id 既有标识, draw 新定义，ID由库管理，不接受外部定义引用
// 返回: error 参数、依赖或循环错误，失败不改变文档，成功隔离其他文档及历史版本并支持撤销
func (e *Editor) ReplaceDrawParam(ctx context.Context, id string, draw DrawParam) error {
	return e.changeResourceStyle(ctx, "DrawParam", id, func(catalog *resourceCatalog, item ResourceInfo, changes map[string][]byte) error {
		working, err := catalog.scan.reader.EditorContext(ctx)
		if err != nil {
			return err
		}
		if err := working.prepareSourceIDsContext(ctx); err != nil {
			return err
		}
		added, err := working.addDrawParam(ctx, draw)
		if err != nil {
			return err
		}
		fragment, err := working.drawParamXMLContext(ctx, added)
		if err != nil {
			return err
		}
		root, err := parseEditorXMLContext(ctx, fragment)
		if err != nil {
			return err
		}
		fragment, err = editorXMLAttribute(fragment, root, "ID", item.ID)
		if err != nil {
			return err
		}
		return replaceResourceStyle(ctx, catalog, item, fragment, changes)
	})
}

// resourceStyleReference 保存资源依赖及可选的类型要求，不持有XML节点
type resourceStyleReference struct {
	key  string
	kind string
}

// styleDefinition 从声明原文解码样式定义，补齐继承的XML命名空间
// 入参: ctx 取消上下文, item 目录条目, target 解码目标
// 返回: error 取消或XML解析错误
func (c *resourceCatalog) styleDefinition(ctx context.Context, item ResourceInfo, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := c.nodes[item.Kind+"/"+item.ID]
	data, err := editorXMLStandaloneContext(ctx, entry.file.data[entry.node.start:entry.node.end], entry.node)
	if err != nil {
		return err
	}
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ctx.Err()
}

// importResourceStyle 在独立快照中生成并合并样式声明，更新最大标识并隔离共享资源文件
// 入参: ctx 取消上下文, create 已校验定义及附属文件的生成操作
// 返回: string 持久资源标识, error 生成、引用或提交错误
func (e *Editor) importResourceStyle(ctx context.Context, create func(*Editor) (string, []byte, map[string][]byte, error)) (string, error) {
	reader, err := e.resourceStyleReader(ctx)
	if err != nil {
		return "", err
	}
	working, err := reader.EditorContext(ctx)
	if err != nil {
		return "", err
	}
	if err := working.prepareSourceIDsContext(ctx); err != nil {
		return "", err
	}
	id, data, parts, err := create(working)
	if err != nil {
		return "", err
	}
	if err := applyResourceStyleParts(ctx, reader, working.maxID, data, parts, nil); err != nil {
		return "", err
	}
	catalog, err := reader.resourceCatalog(ctx)
	if err != nil {
		return "", err
	}
	if err := validateResourceStyleGraph(ctx, catalog, id, nil); err != nil {
		return "", err
	}
	return id, e.commitResourceSnapshot(ctx, reader)
}

// resourceStyleReader 为样式资源操作补齐待注册声明及附属文件，不改变公开快照的按引用导出语义
// 复用内容扫描收集已有标识，不重复解析结构文件或构建未使用的资源目录
// 入参: ctx 取消上下文
// 返回: *Reader 包含待注册字体、颜色空间及参数依赖的独立快照, error 读取或隔离错误
func (e *Editor) resourceStyleReader(ctx context.Context) (*Reader, error) {
	reader, err := e.Reader(ctx)
	if err != nil || len(e.resources) == 0 {
		return reader, err
	}
	known := make(map[string]bool)
	_, err = scanEditorFontFiles(ctx, reader, reader.DocumentIndex(), func(_ *editorFontXML, root *editorXML) error {
		if !root.matchesOFD("Res") {
			return nil
		}
		for _, group := range root.children {
			for _, node := range group.children {
				if err := ctx.Err(); err != nil {
					return err
				}
				if node.matchesOFD(editorResourceElement(group.name.Local)) {
					known[editorResourceID(node.attr("ID"))] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var fonts, images []editorResource
	var spaces []ColorSpace
	var indexes []string
	parts := make(map[string][]byte)
	for _, resource := range e.resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := resource.definition()
		switch {
		case resource.font != nil:
			id = resource.font.ID
		case resource.image != nil:
			id = resource.image.ID
		case resource.space != nil:
			id = resource.space.ID
		}
		if id == "" || known[editorResourceID(id)] {
			continue
		}
		switch {
		case resource.font != nil:
			fonts = append(fonts, resource)
		case resource.image != nil:
			images = append(images, resource)
		case resource.space != nil:
			spaces = append(spaces, *resource.space)
		default:
			indexes = append(indexes, resource.name)
		}
		if resource.name != "" {
			parts[resource.name] = bytes.Clone(resource.data)
		}
	}
	if len(fonts)+len(images)+len(spaces)+len(indexes) == 0 {
		return reader, ctx.Err()
	}
	var data []byte
	if len(fonts)+len(images)+len(spaces) != 0 {
		data, err = encodeOFDXMLContext(ctx, func(x *ofdXML) { x.resources(fonts, images, spaces) })
		if err != nil {
			return nil, err
		}
	}
	if err := applyResourceStyleParts(ctx, reader, e.maxID, data, parts, indexes); err != nil {
		return nil, err
	}
	return reader, ctx.Err()
}

// applyResourceStyleParts 合并资源声明及独立索引，统一更新最大标识并隔离其他文档和历史版本
// 入参: ctx 取消上下文, reader 独立快照, maximumID 最大注册标识, data 合并声明, parts 附属文件, indexes 独立索引路径
// 返回: error 结构、读取或隔离错误
func applyResourceStyleParts(ctx context.Context, reader *Reader, maximumID uint64, data []byte, parts map[string][]byte, indexes []string) error {
	if maximumID > maxUnitID {
		return fmt.Errorf("MaxUnitID %d exceeds uint32 range", maximumID)
	}
	doc, err := reader.DocContext(ctx)
	if err != nil {
		return err
	}
	if len(data) != 0 {
		name, added, err := mergePackageResources(ctx, reader, parts, doc, path.Dir(reader.documentRoot), data)
		if err != nil {
			return err
		}
		if added {
			indexes = append(slices.Clone(indexes), name)
		}
	}
	entry := reader.documentRoot
	content, err := reader.readFileViewContext(ctx, entry)
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, content)
	if err != nil {
		return err
	}
	common := root.child("CommonData")
	if common == nil {
		return fmt.Errorf("missing CommonData")
	}
	patch := editorXMLPatch{common.open, common.open, common.textXML("MaxUnitID", formatUnitID(maximumID))}
	if maximum := common.child("MaxUnitID"); maximum != nil {
		current, err := strconv.ParseUint(strings.TrimSpace(editorImportText(content, maximum)), 10, 32)
		if err != nil {
			return err
		}
		patch = editorXMLPatch{maximum.start, maximum.end, maximum.textXML("MaxUnitID", formatUnitID(max(current, maximumID)))}
	}
	patches := []editorXMLPatch{patch}
	if len(indexes) != 0 {
		position := common.close
		for _, child := range common.children {
			if child.matchesOFD("TemplatePage") || child.matchesOFD("DefaultCS") {
				position = child.start
				break
			}
		}
		var refs []byte
		for _, name := range indexes {
			refs = append(refs, common.textXML("DocumentRes", "/"+name)...)
		}
		patches = append(patches, editorXMLPatch{position, position, refs})
	}
	parts[entry] = editorPatchXML(content, patches)
	scan := &editorFontDocument{reader: reader, index: reader.DocumentIndex()}
	return scan.apply(ctx, parts)
}

// changeResourceStyle 在补齐待注册资源的快照中替换定义，依赖与目标仅在全部校验通过后一次提交
// 入参: ctx 取消上下文, kind 资源类型, id 目标标识, replace 定义替换操作
// 返回: error 引用、校验或提交错误，失败不改变注册资源和撤销状态
func (e *Editor) changeResourceStyle(ctx context.Context, kind, id string, replace func(*resourceCatalog, ResourceInfo, map[string][]byte) error) error {
	reader, err := e.resourceStyleReader(ctx)
	if err != nil {
		return err
	}
	catalog, err := reader.resourceCatalog(ctx)
	if err != nil {
		return err
	}
	item, err := findResource(catalog.items, kind, id)
	if err != nil {
		return err
	}
	if !item.Complete {
		return fmt.Errorf("resource references contain unsupported content")
	}
	changes := make(map[string][]byte)
	if err := replace(catalog, item, changes); err != nil {
		return err
	}
	if err := catalog.scan.apply(ctx, changes); err != nil {
		return err
	}
	if err := pruneUnreferencedFiles(ctx, reader, item.File); err != nil {
		return err
	}
	return e.commitResourceSnapshot(ctx, reader)
}

// replaceResourceStyle 更新声明原文并验证资源依赖，清除受影响的复合及底纹预览
// 入参: ctx 取消上下文, catalog 当前目录, item 目标条目, fragment 新声明, changes 待提交修改
// 返回: error 不支持的定义、引用或预览处理错误
func replaceResourceStyle(ctx context.Context, catalog *resourceCatalog, item ResourceInfo, fragment []byte, changes map[string][]byte) error {
	target := catalog.nodes[item.Kind+"/"+item.ID]
	if item.Kind == "DrawParam" && (target.node.attr("Link") != "" || target.node.attr("BaseLoc") != "" || target.node.attr("ResourceID") != "") {
		return fmt.Errorf("external draw parameter cannot be replaced")
	}
	changes[target.file.name] = editorPatchXML(target.file.data, []editorXMLPatch{{target.node.start, target.node.end, fragment}})
	if err := validateResourceStyleGraph(ctx, catalog, item.ID, changes); err != nil {
		return err
	}
	return invalidateResourcePreviews(ctx, catalog.scan, changes, item.ID)
}

// resourceColorNode 判断可携带基本颜色或复杂填充的标准节点
// 入参: node XML节点
// 返回: bool 是否为颜色节点
func resourceColorNode(node *editorXML) bool {
	return node.matchesOFD(node.name.Local) && slices.Contains([]string{"FillColor", "StrokeColor", "BorderColor", "Color", "BackColor"}, node.name.Local)
}

// validateResourceColors 校验显式及默认颜色空间的既有颜色，防止位深或调色板更新使引用失效
// 入参: ctx 取消上下文, catalog 当前资源目录, space 替换后的颜色空间
// 返回: error 不兼容的色值或调色板索引
func validateResourceColors(ctx context.Context, catalog *resourceCatalog, space ColorSpace) error {
	doc, err := catalog.scan.reader.DocContext(ctx)
	if err != nil {
		return err
	}
	working := NewEditor()
	space.Profile, space.profile = "", nil
	working.resources = []editorResource{{space: &space}}
	for _, file := range catalog.scan.files {
		root, err := parseEditorXMLContext(ctx, file.data)
		if err != nil {
			return err
		}
		var visit func(*editorXML) error
		visit = func(node *editorXML) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !node.matchesOFD(node.name.Local) {
				return nil
			}
			id := node.attr("ColorSpace")
			if id == "" {
				id = formatUnitID(uint64(doc.CommonData.DefaultCS))
			}
			if resourceColorNode(node) && sameResourceID(id, space.ID) {
				var paint FillColor
				decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(file.data[node.start:node.end])), ctx: ctx})
				if err := decoder.Decode(&paint); err != nil {
					return err
				}
				if _, err := working.Color(&FillColor{Value: paint.Value, Index: paint.Index, Alpha: paint.Alpha, ColorSpace: space.ID}); err != nil {
					return fmt.Errorf("color in %s: %w", file.name, err)
				}
			}
			for _, child := range node.children {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(root); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// validateResourceStyleGraph 检查替换定义的传递依赖，拒绝缺失资源、错误类型及跨底纹复合资源的循环
// 逐文件收集依赖后统一校验，不同时保留所有结构文件的XML树
// 入参: ctx 取消上下文, catalog 当前目录, id 替换标识, changes 待提交修改
// 返回: error 资源引用或循环错误
func validateResourceStyleGraph(ctx context.Context, catalog *resourceCatalog, id string, changes map[string][]byte) error {
	types := make(map[string]string)
	graph := make(map[string][]resourceStyleReference)
	for name, file := range catalog.scan.files {
		data, ok := changes[name]
		if !ok {
			data = file.data
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		types["file/"+name] = "File"
		if root.matchesOFD("Res") {
			for _, group := range root.children {
				for _, node := range group.children {
					if node.matchesOFD(editorResourceElement(group.name.Local)) {
						key := editorResourceID(node.attr("ID"))
						if key == "" || types[key] != "" {
							return fmt.Errorf("invalid or duplicate resource ID %q", key)
						}
						types[key] = node.name.Local
						location := ""
						if node.matchesOFD("CompositeGraphicUnit") {
							location = node.attr("BaseLoc")
						} else if node.matchesOFD("DrawParam") {
							location = node.attr("Link")
						}
						if location != "" {
							file := "file/" + resolveResourcePath(name, root.attr("BaseLoc"), location)
							graph[key] = append(graph[key], resourceStyleReference{key: file})
						}
					}
				}
			}
		}
		var visit func(*editorXML) error
		visit = func(node *editorXML) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !node.matchesOFD(node.name.Local) {
				return nil
			}
			owner := resourceReferenceOwner(node)
			if !root.matchesOFD("Res") {
				owner = "file/" + name
			}
			if slices.Contains([]string{"Thumbnail", "Substitution"}, node.name.Local) {
				graph[owner] = append(graph[owner], resourceStyleReference{key: editorResourceID(editorImportText(data, node))})
			}
			for _, attr := range node.attrs {
				if attr.Name.Space == "" && attr.Value != "" && slices.Contains([]string{"Relative", "DrawParam", "ColorSpace", "Font", "ResourceID", "ImageMask", "Substitution", "Thumbnail"}, attr.Name.Local) {
					key := editorResourceID(attr.Value)
					kind := ""
					switch attr.Name.Local {
					case "Relative", "DrawParam":
						kind = "DrawParam"
					case "ColorSpace", "Font":
						kind = attr.Name.Local
					}
					graph[owner] = append(graph[owner], resourceStyleReference{key: key, kind: kind})
				}
			}
			for _, child := range node.children {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(root); err != nil {
			return err
		}
	}
	state := make(map[string]int)
	var visit func(resourceStyleReference) error
	visit = func(reference resourceStyleReference) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := reference.key
		if types[key] == "" || reference.kind != "" && types[key] != reference.kind || state[key] == 1 {
			return fmt.Errorf("missing, mistyped or cyclic style resource %q", key)
		}
		if state[key] == 2 {
			return nil
		}
		state[key] = 1
		for _, dependency := range graph[key] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[key] = 2
		return nil
	}
	return visit(resourceStyleReference{key: id})
}

// drawParamXML 读取绘制参数原文，保留复杂颜色、扩展属性及命名空间
// 入参: id 绘制参数标识
// 返回: []byte 独立参数片段, error 错误信息
func (e *Editor) drawParamXML(id string) ([]byte, error) {
	return e.drawParamXMLContext(context.Background(), id)
}

// drawParamXMLContext 读取并提取绘制参数原文，保留扩展信息并响应取消
// 入参: ctx 取消上下文, id 绘制参数标识
// 返回: []byte 独立参数片段, error 读取、解析或取消错误
func (e *Editor) drawParamXMLContext(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var data []byte
	for _, resource := range e.resources {
		if resource.draw != nil && sameResourceID(resource.draw.ID, id) {
			data = resource.data
			break
		}
	}
	if data == nil && e.source != nil {
		var err error
		name, _ := resourceValue(e.source.reader.resourceFiles, id)
		data, err = e.source.reader.readFileViewContext(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	if node := editorResourceNode(root, "DrawParams", "DrawParam", id); node != nil {
		fragment, err := editorXMLStandaloneContext(ctx, data[node.start:node.end], node)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return fragment, nil
	}
	return nil, fmt.Errorf("draw parameter %q not found", id)
}

// copyDrawParam 将原绘制参数继承链接到独立基准，复用相同副本且不改写原资源
// 入参: id 源参数标识, base 独立基准标识, visited 当前继承链
// 返回: string 新继承链末端标识, error 错误信息
func (e *Editor) copyDrawParam(id, base string, visited map[string]bool) (string, error) {
	if id == "" || sameResourceID(id, base) {
		return base, nil
	}
	key := id
	if value := editorResourceID(id); value != "" {
		key = value
	}
	if visited[key] {
		return "", fmt.Errorf("cyclic draw parameter %q", id)
	}
	visited[key] = true
	data, err := e.drawParamXML(id)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	if sameResourceID(root.attr("Relative"), base) {
		return id, nil
	}
	base, err = e.copyDrawParam(root.attr("Relative"), base, visited)
	if err != nil {
		return "", err
	}
	for _, resource := range e.resources {
		if sameResourceID(resource.drawSource, id) && sameResourceID(resource.draw.Relative, base) {
			return resource.draw.ID, nil
		}
	}
	if !editorXMLCopyable(root) {
		return "", fmt.Errorf("draw parameter contains unsupported identifiers")
	}
	before, err := editorXMLContainer("DrawParam", ofdAttrs{{Name: xml.Name{Local: "Relative"}, Value: root.attr("Relative")}}, nil)
	if err != nil {
		return "", err
	}
	after, err := editorXMLContainer("DrawParam", ofdAttrs{{Name: xml.Name{Local: "Relative"}, Value: base}}, nil)
	if err != nil {
		return "", err
	}
	data, err = editorXMLMerge(data, root, before, after)
	if err != nil {
		return "", err
	}
	root, err = parseEditorXML(data)
	if err != nil {
		return "", err
	}
	ids := make(map[string]string)
	if err := e.collectCompositeIDs(root, ids); err != nil {
		return "", err
	}
	data, err = editorXMLRemapIDs(data, root, ids)
	if err != nil {
		return "", err
	}
	var draw DrawParam
	if err := xml.Unmarshal(data, &draw); err != nil {
		return "", err
	}
	data, err = editorXMLContainer("DrawParams", nil, data)
	if err != nil {
		return "", err
	}
	data, err = editorXMLContainer("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}}, data)
	if err != nil {
		return "", err
	}
	refs, err := editorResourceReferences(data)
	if err != nil {
		return "", err
	}
	e.resources = append(e.resources, editorResource{name: e.packageName("Res/DrawParams/DrawParam_" + draw.ID + ".xml"), data: data, draw: &draw, drawSource: id, references: refs})
	return draw.ID, nil
}
