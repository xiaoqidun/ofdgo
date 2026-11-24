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

// resourceStyleReference 保存资源依赖及可选的类型要求，不持有XML节点
type resourceStyleReference struct {
	scope   string
	ownerID string
	key     string
	kind    string
	exact   bool
}

// applyResourceStyleParts 合并资源声明及独立索引，统一更新最大标识并隔离其他文档和历史版本
// 入参: ctx 取消上下文, reader 独立快照, maximumID 最大注册标识, data 合并声明, parts 附属文件, indexes 独立索引路径
// 返回: error 结构、读取或隔离错误
func applyResourceStyleParts(ctx context.Context, reader *Reader, maximumID uint64, data []byte, parts map[string][]byte, indexes []string) error {
	if maximumID > maxUnitID {
		return fmt.Errorf("MaxUnitID %d exceeds uint32 range", maximumID)
	}
	doc, err := reader.docContext(ctx)
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

// replaceResourceStyle 更新声明原文并验证资源依赖，清除受影响的复合及底纹预览
// 入参: ctx 取消上下文, catalog 当前目录, item 目标条目, targetFile 目标所在资源文件, target 目标节点, fragment 新声明, changes 待提交修改
// 返回: error 不支持的定义、引用或预览处理错误
func replaceResourceStyle(ctx context.Context, catalog *resourceCatalog, item ResourceInfo, targetFile string, target editorFontText, fragment []byte, changes map[string][]byte) error {
	if item.Kind == "DrawParam" && (target.node.attr("Link") != "" || target.node.attr("BaseLoc") != "" || target.node.attr("ResourceID") != "") {
		return fmt.Errorf("external draw parameter cannot be replaced")
	}
	changes[target.file.name] = editorPatchXML(target.file.data, []editorXMLPatch{{target.node.start, target.node.end, fragment}})
	if err := validateResourceStyleGraph(ctx, catalog, target.file.name, item.ID, changes); err != nil {
		return err
	}
	return invalidateScopedResourcePreviews(ctx, catalog, changes, targetFile, item.Kind, item.ID)
}

// resourceColorNode 判断可携带基本颜色或复杂填充的标准节点
// 入参: node XML节点
// 返回: bool 是否为颜色节点
func resourceColorNode(node *editorXML) bool {
	return node.matchesOFD(node.name.Local) && slices.Contains([]string{"FillColor", "StrokeColor", "BorderColor", "Color", "BackColor"}, node.name.Local)
}

// validateResourceColors 校验按作用域精确命中目标颜色空间的显式及默认颜色，不误伤其他页面的同名颜色空间
// 入参: ctx 取消上下文, catalog 当前资源目录, targetFile 目标颜色空间所在文件, space 替换后的颜色空间
// 返回: error 不兼容的色值或调色板索引
func validateResourceColors(ctx context.Context, catalog *resourceCatalog, targetFile string, space ColorSpace) error {
	doc, err := catalog.scan.reader.docContext(ctx)
	if err != nil {
		return err
	}
	defaultCS := ""
	if doc.CommonData.DefaultCS != 0 {
		defaultCS = formatUnitID(doc.CommonData.DefaultCS)
	}
	expectedKey := ""
	if targetFile != "" {
		expectedKey = "res:" + cleanPackagePath(targetFile) + ":ColorSpace:" + editorResourceID(space.ID)
	}
	working := NewEditor()
	space.Profile, space.profile = "", nil
	working.resources = []editorResource{{space: &space}}
	for _, file := range catalog.scan.files {
		cleanName := cleanPackagePath(file.name)
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
			if resourceColorNode(node) {
				id := node.attr("ColorSpace")
				if id == "" {
					id = defaultCS
				}
				matched := sameResourceID(id, space.ID)
				if matched && expectedKey != "" {
					matched = resolveCatalogScopedKey(catalog, cleanName, node.name.Local, "", "ColorSpace", id) == expectedKey
				}
				if matched {
					var paint FillColor
					decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(file.data[node.start:node.end])), ctx: ctx})
					if err := decoder.Decode(&paint); err != nil {
						return err
					}
					if _, err := working.Color(&FillColor{Value: paint.Value, Index: paint.Index, Alpha: paint.Alpha, ColorSpace: space.ID}); err != nil {
						return fmt.Errorf("color in %s: %w", file.name, err)
					}
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
// 入参: ctx 取消上下文, catalog 当前目录, targetFile 目标定义所在资源文件，空值使用文档级作用域, id 替换标识, changes 待提交修改
// 返回: error 资源引用或循环错误
func validateResourceStyleGraph(ctx context.Context, catalog *resourceCatalog, targetFile, id string, changes map[string][]byte) error {
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
		cleanName := cleanPackagePath(name)
		types["file/"+cleanName] = "File"
		if root.matchesOFD("Res") {
			for _, group := range root.children {
				for _, node := range group.children {
					if node.matchesOFD(editorResourceElement(group.name.Local)) {
						key := editorResourceID(node.attr("ID"))
						resKey := cleanName + "\x00" + key
						if key == "" || types[resKey] != "" {
							return fmt.Errorf("invalid or duplicate resource ID %q", key)
						}
						types[resKey] = node.name.Local
						location := ""
						if node.matchesOFD("CompositeGraphicUnit") {
							location = node.attr("BaseLoc")
						} else if node.matchesOFD("DrawParam") {
							location = node.attr("Link")
						}
						if location != "" {
							extFile := "file/" + cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), location))
							graph[resKey] = append(graph[resKey], resourceStyleReference{key: extFile, exact: true})
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
			owner := "file/" + cleanName
			ownerResID := ""
			if root.matchesOFD("Res") {
				owner = ""
				if ownerID := resourceReferenceOwner(node); ownerID != "" {
					ownerResID = ownerID
					owner = cleanName + "\x00" + ownerID
				}
			}
			if owner != "" {
				if slices.Contains([]string{"Thumbnail", "Substitution"}, node.name.Local) {
					graph[owner] = append(graph[owner], resourceStyleReference{scope: cleanName, key: editorResourceID(editorImportText(data, node))})
				}
				for _, attr := range node.attrs {
					if attr.Name.Space == "" && attr.Value != "" && slices.Contains([]string{"Relative", "DrawParam", "ColorSpace", "Font", "ResourceID", "ImageMask", "Substitution", "Thumbnail"}, attr.Name.Local) {
						key := editorResourceID(attr.Value)
						kind := ""
						refOwner := ""
						switch attr.Name.Local {
						case "Relative":
							kind = "DrawParam"
							refOwner = ownerResID
						case "DrawParam":
							kind = "DrawParam"
						case "ColorSpace", "Font":
							kind = attr.Name.Local
						}
						graph[owner] = append(graph[owner], resourceStyleReference{scope: cleanName, ownerID: refOwner, key: key, kind: kind})
					}
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
		nodeKey := reference.key
		if !reference.exact {
			nodeKey = ""
			if reference.key != "" {
				scope := catalog.scopeForFile(reference.scope)
				if reference.ownerID != "" {
					if pos := slices.Index(scope, reference.scope); pos >= 0 {
						if reference.key == reference.ownerID {
							scope = scope[pos+1:]
						} else {
							scope = scope[pos:]
						}
					}
				}
				for _, resFile := range scope {
					candidate := resFile + "\x00" + reference.key
					if types[candidate] != "" {
						nodeKey = candidate
						break
					}
				}
			}
		}
		if types[nodeKey] == "" || reference.kind != "" && types[nodeKey] != reference.kind || state[nodeKey] == 1 {
			return fmt.Errorf("missing, mistyped or cyclic style resource %q", reference.key)
		}
		if state[nodeKey] == 2 {
			return nil
		}
		state[nodeKey] = 1
		for _, dependency := range graph[nodeKey] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[nodeKey] = 2
		return nil
	}
	start := resourceStyleReference{scope: "", key: editorResourceID(id)}
	if targetFile != "" {
		start = resourceStyleReference{key: cleanPackagePath(targetFile) + "\x00" + editorResourceID(id), exact: true}
	}
	return visit(start)
}

// ColorSpace 获取当前文档及版本的颜色空间定义，不读取或解析ICC文件
// 入参: ctx 取消上下文, id 资源标识
// 返回: *ColorSpace 独立副本，Profile为包内绝对引用, error 查询或解析错误
func (r *Reader) ColorSpace(ctx context.Context, id string) (*ColorSpace, error) {
	catalog, err := r.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	item, _, resFile, _, err := findUniqueCatalogResource(catalog, "ColorSpace", id)
	if err != nil {
		return nil, err
	}
	var space ColorSpace
	if err := catalog.styleDefinitionAt(ctx, resFile, item, &space); err != nil {
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
	var matches []int
	for i, item := range catalog.items {
		if resourceMatches(item, "DrawParam", id) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("resource not found: DrawParam/%s", id)
	}
	curIdx := matches[0]
	if len(matches) > 1 {
		if !resolved {
			return nil, fmt.Errorf("ambiguous resource DrawParam/%s defined in multiple resource files", id)
		}
		bestIdx := -1
		for _, candidate := range matches {
			scope := catalog.scopeForFile(catalog.itemFiles[candidate])
			coversAll := true
			for _, other := range matches {
				if !slices.Contains(scope, catalog.itemFiles[other]) {
					coversAll = false
					break
				}
			}
			if coversAll {
				if bestIdx != -1 {
					return nil, fmt.Errorf("ambiguous resource DrawParam/%s defined in multiple resource files", id)
				}
				bestIdx = candidate
			}
		}
		if bestIdx == -1 {
			return nil, fmt.Errorf("ambiguous resource DrawParam/%s defined in multiple resource files", id)
		}
		curIdx = bestIdx
	}
	var chain []DrawParam
	seen := make(map[string]bool)
	for {
		item := catalog.items[curIdx]
		curFile := catalog.itemFiles[curIdx]
		visitKey := curFile + "\x00" + item.ID
		if seen[visitKey] {
			return nil, fmt.Errorf("cyclic draw parameter %s", item.ID)
		}
		seen[visitKey] = true
		var draw DrawParam
		if err := catalog.styleDefinitionAt(ctx, curFile, item, &draw); err != nil {
			return nil, err
		}
		draw.ID = item.ID
		if !resolved {
			return &draw, nil
		}
		chain = append(chain, draw)
		if draw.Relative == "" {
			break
		}
		relID := editorResourceID(draw.Relative)
		scope := catalog.scopeForFile(curFile)
		if pos := slices.Index(scope, curFile); pos >= 0 {
			if relID == item.ID {
				scope = scope[pos+1:]
			} else {
				scope = scope[pos:]
			}
		}
		nextIdx := -1
		for _, resFile := range scope {
			if idx, ok := catalog.itemIndex[resFile+"\x00DrawParam\x00"+relID]; ok {
				nextIdx = idx
				break
			}
		}
		if nextIdx < 0 {
			if relID == item.ID {
				return nil, fmt.Errorf("cyclic draw parameter %s", item.ID)
			}
			return nil, fmt.Errorf("resource not found: DrawParam/%s", draw.Relative)
		}
		curIdx = nextIdx
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
		if found, ok := e.sourceColorSpace(id); ok && found != nil {
			space := *found
			space.Palette, space.profile = slices.Clone(space.Palette), nil
			if space.Profile != "" {
				space.Profile = "/" + cleanPackagePath(e.source.reader.ResPath(space.Profile))
			}
			return &space, nil
		}
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
	return e.changeResourceStyle(ctx, "ColorSpace", id, func(catalog *resourceCatalog, item ResourceInfo, targetFile string, target editorFontText, changes map[string][]byte) error {
		space.ID = item.ID
		if err := validateResourceColors(ctx, catalog, targetFile, space); err != nil {
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
		return replaceResourceStyle(ctx, catalog, item, targetFile, target, fragment, changes)
	})
}

// ReplaceDrawParam 原子替换共享绘制参数，保留全部既有绑定及图元的局部覆盖
// 入参: ctx 取消上下文, id 既有标识, draw 新定义，ID由库管理，不接受外部定义引用
// 返回: error 参数、依赖或循环错误，失败不改变文档，成功隔离其他文档及历史版本并支持撤销
func (e *Editor) ReplaceDrawParam(ctx context.Context, id string, draw DrawParam) error {
	return e.changeResourceStyle(ctx, "DrawParam", id, func(catalog *resourceCatalog, item ResourceInfo, targetFile string, target editorFontText, changes map[string][]byte) error {
		working, err := catalog.scan.reader.EditorContext(ctx)
		if err != nil {
			return err
		}
		defer working.Close()
		if err := working.prepareSourceIDsContext(ctx); err != nil {
			return err
		}
		restoreScope := func() {}
		if targetFile != "" && !catalog.docRes[targetFile] {
			var localParsed []*parsedRes
			scope := catalog.scopeForFile(targetFile)
			for i := len(scope) - 1; i >= 0; i-- {
				if catalog.docRes[scope[i]] {
					continue
				}
				parsed, parseErr := working.source.reader.parseResFileContext(ctx, scope[i])
				if parseErr != nil {
					return parseErr
				}
				if parsed != nil {
					localParsed = append(localParsed, parsed)
				}
			}
			if len(localParsed) > 0 {
				restoreScope = working.bindParsedScope(localParsed)
			}
		}
		defer restoreScope()
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
		return replaceResourceStyle(ctx, catalog, item, targetFile, target, fragment, changes)
	})
}

// styleDefinitionAt 从指定资源文件的声明原文解码样式定义，补齐继承的XML命名空间
// 入参: ctx 取消上下文, resFile 声明所在资源文件, item 目录条目, target 解码目标
// 返回: error 取消或XML解析错误
func (c *resourceCatalog) styleDefinitionAt(ctx context.Context, resFile string, item ResourceInfo, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := c.nodes[item.Kind+"/"+item.ID]
	if resFile != "" {
		if scoped, ok := c.nodes[resFile+":"+item.Kind+"/"+item.ID]; ok {
			entry = scoped
		}
	}
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
	defer reader.Close()
	working, err := reader.EditorContext(ctx)
	if err != nil {
		return "", err
	}
	defer working.Close()
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
	if err := validateResourceStyleGraph(ctx, catalog, "", id, nil); err != nil {
		return "", err
	}
	return id, e.commitResourceSnapshot(ctx, reader)
}

// resourceStyleReader 为样式资源操作补齐待注册声明及附属文件，不改变公开快照的按引用导出语义
// 复用内容扫描收集已有标识，不重复解析结构文件或构建未使用的资源目录
// 入参: ctx 取消上下文
// 返回: *Reader 包含待注册字体、颜色空间及参数依赖的独立快照, error 读取或隔离错误
func (e *Editor) resourceStyleReader(ctx context.Context) (result *Reader, err error) {
	reader, err := e.Reader(ctx)
	if err != nil || len(e.resources) == 0 {
		return reader, err
	}
	defer func() {
		if result == nil {
			_ = reader.Close()
		}
	}()
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return reader, nil
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return reader, nil
}

// changeResourceStyle 在补齐待注册资源的快照中替换定义，依赖与目标仅在全部校验通过后一次提交
// 入参: ctx 取消上下文, kind 资源类型, id 目标标识, replace 定义替换操作
// 返回: error 引用、校验或提交错误，失败不改变注册资源和撤销状态
func (e *Editor) changeResourceStyle(ctx context.Context, kind, id string, replace func(*resourceCatalog, ResourceInfo, string, editorFontText, map[string][]byte) error) error {
	reader, err := e.resourceStyleReader(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	catalog, err := reader.resourceCatalog(ctx)
	if err != nil {
		return err
	}
	item, _, targetFile, target, err := findUniqueCatalogResource(catalog, kind, id)
	if err != nil {
		return err
	}
	if !item.Complete {
		return fmt.Errorf("resource references contain unsupported content")
	}
	changes := make(map[string][]byte)
	if err := replace(catalog, item, targetFile, target, changes); err != nil {
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
		name, ok, err := e.sourceResourceFileContext(ctx, id, "DrawParam")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("draw parameter %q not found", id)
		}
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

// drawParamXMLAtScope 从指定资源层级提取单个绘制参数独立XML，支持展开外部Link文件
// 入参: ctx 取消上下文, scope 资源层级, id 绘制参数标识
// 返回: []byte 独立参数片段, error 读取或解析错误
func (e *Editor) drawParamXMLAtScope(ctx context.Context, scope editorDrawParamScope, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch scope.key {
	case "@editor":
		for _, resource := range e.resources {
			if resource.draw != nil && sameResourceID(resource.draw.ID, id) {
				root, err := parseEditorXMLContext(ctx, resource.data)
				if err != nil {
					return nil, err
				}
				if node := editorResourceNode(root, "DrawParams", "DrawParam", id); node != nil {
					return editorXMLStandaloneContext(ctx, resource.data[node.start:node.end], node)
				}
			}
		}
		return nil, fmt.Errorf("draw parameter %q not found", id)
	case "@doc":
		return e.drawParamXMLContext(ctx, id)
	default:
		if e.source == nil {
			return nil, fmt.Errorf("draw parameter %q not found", id)
		}
		data, err := e.source.reader.readFileViewContext(ctx, scope.key)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		node := editorResourceNode(root, "DrawParams", "DrawParam", id)
		if node == nil {
			return nil, fmt.Errorf("draw parameter %q not found", id)
		}
		if link := strings.TrimSpace(node.attr("Link")); link != "" {
			extPath := resolveResourcePath(scope.key, root.attr("BaseLoc"), link)
			extData, err := e.source.reader.readFileViewContext(ctx, extPath)
			if err != nil {
				return nil, err
			}
			extRoot, err := parseEditorXMLContext(ctx, extData)
			if err != nil {
				return nil, err
			}
			if extRoot.matchesOFD("DrawParam") {
				fragment, err := editorXMLStandaloneContext(ctx, extData[extRoot.start:extRoot.end], extRoot)
				if err != nil {
					return nil, err
				}
				fragRoot, err := parseEditorXMLContext(ctx, fragment)
				if err != nil {
					return nil, err
				}
				fragment, err = editorXMLAttribute(fragment, fragRoot, "ID", node.attr("ID"))
				if err != nil {
					return nil, err
				}
				if rel := node.attr("Relative"); rel != "" && extRoot.attr("Relative") == "" {
					fragRoot, err = parseEditorXMLContext(ctx, fragment)
					if err != nil {
						return nil, err
					}
					fragment, err = editorXMLAttribute(fragment, fragRoot, "Relative", rel)
					if err != nil {
						return nil, err
					}
				}
				return fragment, ctx.Err()
			}
		}
		return editorXMLStandaloneContext(ctx, data[node.start:node.end], node)
	}
}

// copyDrawParam 将原绘制参数继承链接到独立基准，复用相同副本且不改写原资源
// 入参: id 源参数标识, base 独立基准标识, visited 当前继承链
// 返回: string 新继承链末端标识, error 错误信息
func (e *Editor) copyDrawParam(id, base string, visited map[string]bool) (string, error) {
	return e.copyDrawParamAt(id, base, 0, e.drawParamScopes(), visited)
}

// copyDrawParamAt 按作用域链复制绘制参数继承链并提升局部资源引用
// 入参: id 源参数标识, base 独立基准标识, startLevel 起始层级, scopes 作用域链, visited 已访问作用域标识
// 返回: string 新继承链末端标识, error 错误信息
func (e *Editor) copyDrawParamAt(id, base string, startLevel int, scopes []editorDrawParamScope, visited map[string]bool) (string, error) {
	if id == "" || sameResourceID(id, base) {
		return base, nil
	}
	foundLevel := -1
	for i := startLevel; i < len(scopes); i++ {
		if e.lookupDrawParamInScope(scopes[i], id) != nil {
			foundLevel = i
			break
		}
	}
	if foundLevel < 0 {
		return "", fmt.Errorf("draw parameter %q not found", id)
	}
	key := id
	if value := editorResourceID(id); value != "" {
		key = value
	}
	scope := scopes[foundLevel]
	visitKey := scope.key + "\x00" + key
	if visited[visitKey] {
		return "", fmt.Errorf("cyclic draw parameter %q", id)
	}
	visited[visitKey] = true
	data, err := e.drawParamXMLAtScope(context.Background(), scope, id)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	localIdx := slices.IndexFunc(e.pageScope, func(p *parsedRes) bool { return p.path == scope.key })
	isLocalScope := scope.parsed != nil && localIdx >= 0
	if !isLocalScope && sameResourceID(root.attr("Relative"), base) {
		return id, nil
	}
	nextStart := foundLevel
	if root.attr("Relative") != "" && sameResourceID(root.attr("Relative"), id) {
		nextStart = foundLevel + 1
	}
	base, err = e.copyDrawParamAt(root.attr("Relative"), base, nextStart, scopes, visited)
	if err != nil {
		return "", err
	}
	sourceTag := key
	if scope.key != "@editor" && scope.key != "@doc" && scope.key != "" {
		sourceTag = scope.key + "\x00" + key
	}
	for _, resource := range e.resources {
		if resource.draw != nil && resource.drawSource == sourceTag && sameResourceID(resource.draw.Relative, base) {
			return resource.draw.ID, nil
		}
	}
	if !editorXMLCopyable(root) {
		return "", fmt.Errorf("draw parameter contains unsupported identifiers")
	}
	if isLocalScope {
		data, err = editorXMLAttribute(data, root, "Relative", "")
		if err != nil {
			return "", err
		}
		data, err = e.promoteScopeXMLToDocument(data, e.pageScope[:localIdx+1])
		if err != nil {
			return "", err
		}
		root, err = parseEditorXML(data)
		if err != nil {
			return "", err
		}
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
	e.resources = append(e.resources, editorResource{name: e.packageName("Res/DrawParams/DrawParam_" + draw.ID + ".xml"), data: data, draw: &draw, drawSource: sourceTag, references: refs})
	return draw.ID, nil
}
