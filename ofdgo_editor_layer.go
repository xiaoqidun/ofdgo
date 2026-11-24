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
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// LayerInfo 描述普通页面图层，Order为同类型零基顺序，ObjectCount包含页块中的图元
// ObjectIDs按层内文件顺序列出图元标识，包含PageBlock成员但不展开复合图元，空层返回空切片
type LayerInfo struct {
	ID          string
	Type        string
	DrawParam   string
	Order       int
	ObjectCount int
	ObjectIDs   []string
}

// editorLayerInfos 将页面图层投影为不共享可变容器的管理清单
// 入参: p 已读取的页面
// 返回: []LayerInfo 图层清单
func editorLayerInfos(p *PageContent) []LayerInfo {
	result := make([]LayerInfo, 0, len(p.Content.Layer))
	orders := make(map[string]int)
	for _, layer := range p.Content.Layer {
		kind := editorLayerType(layer.Type)
		ids := make([]string, len(layer.Objects))
		for i, object := range layer.Objects {
			ids[i] = editorObjectID(object)
		}
		result = append(result, LayerInfo{ID: layer.ID, Type: kind, DrawParam: layer.DrawParam, Order: orders[kind], ObjectCount: len(ids), ObjectIDs: ids})
		orders[kind]++
	}
	return result
}

// editorLayerType 将标准省略类型规范化，不改变已明确指定的类型
// 入参: value 原类型
// 返回: string 有效类型
func editorLayerType(value string) string {
	if value == "" {
		return "Body"
	}
	return value
}

// validEditorLayerType 按正文表15校验可管理的图层类型
// 入参: value 类型值
// 返回: bool 是否为支持的标准类型
func validEditorLayerType(value string) bool {
	return value == "Body" || value == "Background" || value == "Foreground"
}

// editorStructureChildren 仅枚举当前标准命名空间中的直接子元素
// 入参: root 容器节点, kind 子元素名称
// 返回: []*editorXML 文件顺序节点清单
func editorStructureChildren(root *editorXML, kind string) []*editorXML {
	var nodes []*editorXML
	if root != nil {
		for _, node := range root.children {
			if node.matchesOFD(kind) {
				nodes = append(nodes, node)
			}
		}
	}
	return nodes
}

// editorFindStructureID 按数值标识查找唯一直接子元素，拒绝缺失及歧义标识
// 入参: root 容器节点, kind 元素名称, attribute 标识属性名, id 标识值
// 返回: *editorXML 唯一节点, error 标识错误
func editorFindStructureID(root *editorXML, kind, attribute, id string) (*editorXML, error) {
	var found *editorXML
	if editorResourceID(id) == "" {
		return nil, fmt.Errorf("invalid %s ID %q", kind, id)
	}
	for _, node := range editorStructureChildren(root, kind) {
		if sameResourceID(node.attr(attribute), id) {
			if found != nil {
				return nil, fmt.Errorf("ambiguous %s ID %q", kind, id)
			}
			found = node
		}
	}
	if found == nil {
		return nil, fmt.Errorf("%s %q not found", kind, id)
	}
	return found, nil
}

// editorStructureAttribute 局部替换或删除无命名空间属性，保留扩展及其余原文
// 入参: data 原文, node 目标节点, name 属性名, value 新值，空值删除属性
// 返回: []byte 更新后的完整原文, error XML错误
func editorStructureAttribute(data []byte, node *editorXML, name, value string) ([]byte, error) {
	var desired *string
	if value != "" {
		desired = &value
	}
	patch, err := editorXMLAttributeChange(data, node, name, desired)
	if err != nil {
		return nil, err
	}
	return editorPatchXML(data, []editorXMLPatch{patch}), nil
}

// editorAppendStructure 向普通或自闭合容器追加已绑定命名空间的元素
// 入参: data 原文, node 容器节点, fragment 待追加片段
// 返回: []byte 更新后的完整原文
func editorAppendStructure(data []byte, node *editorXML, fragment []byte) []byte {
	if node.open == node.end {
		return editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, fragment)})
	}
	return editorPatchXML(data, []editorXMLPatch{{node.close, node.close, fragment}})
}

// editorMoveStructure 在指定同类节点位置间移动完整原文，不触及夹杂的扩展元素
// 入参: data 原文, nodes 同类节点, node 被移动节点, order 目标零基顺序
// 返回: []byte 更新后的完整原文, error 顺序错误
func editorMoveStructure(data []byte, nodes []*editorXML, node *editorXML, order int) ([]byte, error) {
	if order < 0 || order >= len(nodes) {
		return nil, fmt.Errorf("order %d out of range", order)
	}
	from := slices.Index(nodes, node)
	if from == order {
		return data, nil
	}
	ordered := slices.Clone(nodes)
	moveEditorItem(ordered, from, order)
	patches := make([]editorXMLPatch, len(nodes))
	for i, old := range nodes {
		next := ordered[i]
		patches[i] = editorXMLPatch{old.start, old.end, data[next.start:next.end]}
	}
	return editorPatchXML(data, patches), nil
}

// patchFastMaxUnitID 在标准Document头部快速替换MaxUnitID，遇非标准结构回退完整XML解析
// 入参: data 文档XML原文, maxID 目标最大标识
// 返回: []byte 更新后的XML原文, bool 是否命中快路径
func patchFastMaxUnitID(data []byte, maxID uint64) ([]byte, bool) {
	limit := min(len(data), 8192)
	head := data[:limit]
	if bytes.Contains(head, []byte("<!--")) || bytes.Contains(head, []byte("<![CDATA[")) {
		return nil, false
	}
	for _, tags := range [][2][]byte{
		{[]byte("<ofd:MaxUnitID>"), []byte("</ofd:MaxUnitID>")},
		{[]byte("<MaxUnitID>"), []byte("</MaxUnitID>")},
	} {
		start := bytes.Index(head, tags[0])
		if start < 0 {
			continue
		}
		valStart := start + len(tags[0])
		relEnd := bytes.Index(head[valStart:], tags[1])
		if relEnd < 0 {
			return nil, false
		}
		valEnd := valStart + relEnd
		current, ok := parseFastUint32(head[valStart:valEnd])
		if !ok {
			return nil, false
		}
		if maxID <= current {
			return data, true
		}
		encoded := []byte(formatUnitID(maxID))
		out := make([]byte, 0, len(data)-(valEnd-valStart)+len(encoded))
		out = append(out, data[:valStart]...)
		out = append(out, encoded...)
		out = append(out, data[valEnd:]...)
		return out, true
	}
	return nil, false
}

// Layers 按文件顺序返回普通页面图层，省略的类型规范化为Body，不包含模板图层
// 入参: page 零基页面索引
// 返回: []LayerInfo 独立图层清单, error 页面读取错误
func (e *Editor) Layers(page int) ([]LayerInfo, error) {
	p, err := e.page(page)
	if err != nil {
		return nil, err
	}
	return editorLayerInfos(p), nil
}

// AddLayer 追加空图层并返回新标识，空类型采用Body，空DrawParam表示不设置继承参数
// 入参: page 页面索引, layerType 图层类型, drawParam 绘制参数标识
// 返回: string 新图层标识, error 类型、资源或结构错误
func (e *Editor) AddLayer(page int, layerType, drawParam string) (string, error) {
	layerType = editorLayerType(layerType)
	if !validEditorLayerType(layerType) {
		return "", fmt.Errorf("invalid layer type %q", layerType)
	}
	var id string
	err := e.editLayerPageMode(page, true, func(r *Reader, data []byte, root *editorXML) ([]byte, error) {
		if err := e.validateLayerDrawParam(page, drawParam); err != nil {
			return nil, err
		}
		if err := e.prepareSourceIDs(); err != nil {
			return nil, err
		}
		var err error
		id, err = e.nextID()
		if err != nil {
			return nil, err
		}
		attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}, {Name: xml.Name{Local: "Type"}, Value: layerType}}
		attrs.add("DrawParam", drawParam)
		content := root.child("Content")
		fragment, err := root.containerXML("Layer", attrs, nil)
		if err != nil {
			return nil, err
		}
		if content != nil {
			return editorAppendStructure(data, content, fragment), nil
		}
		fragment, err = root.containerXML("Content", nil, fragment)
		if err != nil {
			return nil, err
		}
		at := root.close
		if actions := root.child("Actions"); actions != nil {
			at = actions.start
		}
		if root.open == root.end {
			return editorAppendStructure(data, root, fragment), nil
		}
		return editorPatchXML(data, []editorXMLPatch{{at, at, fragment}}), nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// SetLayerType 设置图层类型，不改变图元坐标及绘制参数；同类型顺序由文件位置决定
// 入参: page 页面索引, id 图层标识, layerType 图层类型
// 返回: error 标识、类型或结构错误
func (e *Editor) SetLayerType(page int, id, layerType string) error {
	layerType = editorLayerType(layerType)
	if !validEditorLayerType(layerType) {
		return fmt.Errorf("invalid layer type %q", layerType)
	}
	return e.editLayerPageMode(page, true, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		node, err := editorFindStructureID(root.child("Content"), "Layer", "ID", id)
		if err != nil {
			return nil, err
		}
		if editorLayerType(node.attr("Type")) == layerType {
			return data, nil
		}
		return editorStructureAttribute(data, node, "Type", layerType)
	})
}

// SetLayerDrawParam 设置图层绘制参数，空标识清除引用，图元局部覆盖保持不变
// 入参: page 页面索引, id 图层标识, drawParam 绘制参数标识
// 返回: error 标识、资源或结构错误
func (e *Editor) SetLayerDrawParam(page int, id, drawParam string) error {
	return e.editLayerPage(page, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		node, err := editorFindStructureID(root.child("Content"), "Layer", "ID", id)
		if err != nil {
			return nil, err
		}
		if err := e.validateLayerDrawParam(page, drawParam); err != nil {
			return nil, err
		}
		if node.attr("DrawParam") == drawParam {
			return data, nil
		}
		return editorStructureAttribute(data, node, "DrawParam", drawParam)
	})
}

// MoveLayer 调整图层在同类型图层中的零基顺序，保留其他类型及扩展节点的位置
// 入参: page 页面索引, id 图层标识, order 同类型目标顺序
// 返回: error 标识、顺序或结构错误
func (e *Editor) MoveLayer(page int, id string, order int) error {
	return e.editLayerPageMode(page, true, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		node, err := editorFindStructureID(root.child("Content"), "Layer", "ID", id)
		if err != nil {
			return nil, err
		}
		var nodes []*editorXML
		for _, child := range root.child("Content").children {
			if child.matchesOFD("Layer") && editorLayerType(child.attr("Type")) == editorLayerType(node.attr("Type")) {
				nodes = append(nodes, child)
			}
		}
		return editorMoveStructure(data, nodes, node, order)
	})
}

// DeleteLayer 删除空图层，含图元、页块或扩展子元素时拒绝；可先在Transaction中迁移图元
// 入参: page 页面索引, id 图层标识
// 返回: error 标识、非空图层或结构错误
func (e *Editor) DeleteLayer(page int, id string) error {
	return e.editLayerPage(page, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		content := root.child("Content")
		node, err := editorFindStructureID(content, "Layer", "ID", id)
		if err != nil {
			return nil, err
		}
		if len(node.children) != 0 {
			return nil, fmt.Errorf("layer %q is not empty", id)
		}
		if len(content.children) == 1 && len(content.attrs) == 0 {
			return editorPatchXML(data, []editorXMLPatch{{content.start, content.end, nil}}), nil
		}
		if len(editorStructureChildren(content, "Layer")) == 1 {
			return nil, fmt.Errorf("last layer contains a preserved Content container")
		}
		return editorPatchXML(data, []editorXMLPatch{{node.start, node.end, nil}}), nil
	})
}

// MoveObjectsToLayer 将普通页面选区按原绘制顺序追加到目标层，保持标识、坐标、扩展及有效样式
// 不接受模板或注解标识；页块成员须先通过已有复合编辑接口处理，失败不产生部分移动
// 入参: page 页面索引, ids 不重复的普通图元标识, layerID 目标图层标识
// 返回: error 选区、继承参数或结构错误
func (e *Editor) MoveObjectsToLayer(page int, ids []string, layerID string) error {
	return e.editLayerPage(page, func(_ *Reader, data []byte, root *editorXML) ([]byte, error) {
		defer e.bindPageIndex(page)()
		target, err := editorFindStructureID(root.child("Content"), "Layer", "ID", layerID)
		if err != nil {
			return nil, err
		}
		wanted := make(map[string]bool)
		for _, id := range ids {
			key := editorResourceID(id)
			if key == "" || wanted[key] {
				return nil, fmt.Errorf("invalid or duplicate object ID %q", id)
			}
			wanted[key] = true
		}
		var patches []editorXMLPatch
		var added []byte
		layers := editorStructureChildren(root.child("Content"), "Layer")
		slices.SortStableFunc(layers, func(a, b *editorXML) int { return layerOrder(a.attr("Type")) - layerOrder(b.attr("Type")) })
		for _, layer := range layers {
			nodes := make(map[string]*editorXML)
			if err := editorXMLObjects(layer, nodes); err != nil {
				return nil, err
			}
			ordered := make([]*editorXML, 0, len(nodes))
			for _, node := range nodes {
				ordered = append(ordered, node)
			}
			slices.SortFunc(ordered, func(a, b *editorXML) int { return a.start - b.start })
			for _, node := range ordered {
				key := editorResourceID(node.attr("ID"))
				if !wanted[key] {
					continue
				}
				delete(wanted, key)
				if layer == target {
					continue
				}
				if node.parent != layer {
					return nil, fmt.Errorf("object %q belongs to a PageBlock", key)
				}
				fragment, err := editorXMLStandalone(data[node.start:node.end], node)
				if err != nil {
					return nil, err
				}
				if layer.attr("DrawParam") != target.attr("DrawParam") {
					member, err := newEditorCompositeNode(fragment)
					if err != nil {
						return nil, err
					}
					member.drawParams = []string{layer.attr("DrawParam")}
					member.defaults, err = e.editorDrawParam(layer.attr("DrawParam"), make(map[string]bool))
					if err != nil {
						return nil, err
					}
					if member.object.Type == "TextObject" || member.object.Type == "ImageObject" {
						styled, err := e.compositeMemberStyle(member)
						if err != nil {
							return nil, err
						}
						if err := member.update(styled); err != nil {
							return nil, err
						}
					}
					if err := e.isolateCompositeStyle(member); err != nil {
						return nil, err
					}
					fragment, _, err = e.writeCompositeNode(member)
					if err != nil {
						return nil, err
					}
				}
				added = append(added, fragment...)
				patches = append(patches, editorXMLPatch{node.start, node.end, nil})
			}
		}
		if len(wanted) != 0 {
			return nil, fmt.Errorf("selection contains missing ordinary page objects")
		}
		if len(added) == 0 {
			return data, nil
		}
		if target.open == target.end {
			patches = append(patches, editorXMLContent(data, target, added))
		} else {
			patches = append(patches, editorXMLPatch{target.close, target.close, added})
		}
		return editorPatchXML(data, patches), nil
	})
}

// Layers 直接读取普通页面图层，不创建编辑器或分配新标识，返回值不持有页面可变数据
// 入参: page 零基页面索引
// 返回: []LayerInfo 按文件顺序排列的图层, error 页面读取错误
func (r *Reader) Layers(page int) ([]LayerInfo, error) {
	p, err := r.PageContentByIndex(page)
	if err != nil {
		return nil, err
	}
	return editorLayerInfos(p), nil
}

// validateLayerDrawParam 在目标页面资源环境中核验完整绘制参数继承链
// 入参: page 页面索引, id 绘制参数标识
// 返回: error 页面或资源错误
func (e *Editor) validateLayerDrawParam(page int, id string) error {
	if _, err := e.page(page); err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	defer e.bindPageIndex(page)()
	_, err := e.editorDrawParam(id, make(map[string]bool))
	return err
}

// editLayerPage 在完整包快照上编辑页面原文，事务失败回滚，提交时隔离共享页面及历史版本
// 入参: page 页面索引, edit 页面原文修改回调
// 返回: error 读取、修改或提交错误
func (e *Editor) editLayerPage(page int, edit func(*Reader, []byte, *editorXML) ([]byte, error)) error {
	return e.editLayerPageMode(page, false, edit)
}

// editLayerPageMode 按追加或常规语义在包快照上编辑单页原文，优先直接读取未改原始页并增量提交
// 入参: page 页面索引, appendOnly 是否为不缩减原包引用的操作, edit 页面原文修改回调
// 返回: error 读取、修改或提交错误
func (e *Editor) editLayerPageMode(page int, appendOnly bool, edit func(*Reader, []byte, *editorXML) ([]byte, error)) error {
	return e.Transaction(func(_ *Editor) error {
		if _, err := e.page(page); err != nil {
			return err
		}
		var r *Reader
		direct := false
		if e.source != nil && len(e.resources) == 0 && e.outlines == nil && reflect.DeepEqual(e.Info, e.source.info) && len(e.pages) == len(e.source.document.Pages.Page) && e.source.document.Pages.Page[page].ID == e.pages[page].ID {
			if srcPage := e.source.pages[e.pages[page].ID]; srcPage != nil && (srcPage.original == nil || sourcePageUntouched(e.pages[page], srcPage)) {
				r = e.source.reader
				direct = true
			}
		}
		if r == nil {
			var err error
			r, err = e.reader(nil)
			if err != nil {
				return err
			}
			defer r.Close()
		}
		doc, err := r.docStructure()
		if err != nil {
			return err
		}
		name := r.ResPath(doc.Pages.Page[page].BaseLoc)
		data, err := r.readFile(name)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		next, err := edit(r, data, root)
		if err != nil || bytes.Equal(data, next) {
			return err
		}
		parts := map[string][]byte{name: next}
		rootDir := strings.TrimPrefix(strings.ReplaceAll(r.rootDir, "\\", "/"), "/")
		shared := slices.ContainsFunc(doc.CommonData.TemplatePage, func(ref TemplatePage) bool { return resPathFast(r, rootDir, ref.BaseLoc) == name })
		if !shared {
			for i, ref := range doc.Pages.Page {
				if i != page && resPathFast(r, rootDir, ref.BaseLoc) == name {
					shared = true
					break
				}
			}
		}
		if shared {
			private := packageAvailableName(r, parts, name)
			delete(parts, name)
			parts[private] = next
			document, err := r.readFile(r.documentRoot)
			if err != nil {
				return err
			}
			entry, err := parseEditorXML(document)
			if err != nil {
				return err
			}
			refNode := entry.child("Pages").childAt("Page", page)
			parts[r.documentRoot], err = editorStructureAttribute(document, refNode, "BaseLoc", "/"+private)
			if err != nil {
				return err
			}
		}
		if direct {
			return e.commitLayerTemplatePartsMode(r, parts, appendOnly)
		}
		return e.commitLayerTemplatePartsMode(r, parts, false)
	})
}

// commitLayerTemplateParts 持久化结构与已注册资源，隔离共享文件并恢复未变图元的会话编辑状态
// 入参: reader 当前独立包快照, parts 文件修改集
// 返回: error 资源、隔离或提交错误
func (e *Editor) commitLayerTemplateParts(reader *Reader, parts map[string][]byte) error {
	return e.commitLayerTemplatePartsMode(reader, parts, false)
}

// commitLayerTemplatePartsMode 持久化图层与模板修改，满足条件时仅增量提交并精准重载受影响单页
// 入参: reader 当前独立包快照, parts 文件修改集, appendOnly 是否为不缩减原包引用的操作
// 返回: error 资源、隔离或提交错误
func (e *Editor) commitLayerTemplatePartsMode(reader *Reader, parts map[string][]byte, appendOnly bool) error {
	ctx := context.Background()
	if e.maxID > maxUnitID {
		return fmt.Errorf("MaxUnitID %d exceeds uint32 range", e.maxID)
	}
	if e.canCommitLayerPartsIncremental(parts) {
		return e.commitLayerPartsIncremental(ctx, reader, parts, appendOnly)
	}
	data := parts[reader.documentRoot]
	var err error
	if data == nil {
		data, err = reader.readFile(reader.documentRoot)
		if err != nil {
			return err
		}
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	common := root.child("CommonData")
	if common == nil {
		return fmt.Errorf("document has no CommonData")
	}
	var definitions []byte
	registered := make(map[string]bool)
	for _, kind := range []string{"PublicRes", "DocumentRes"} {
		for _, node := range editorStructureChildren(common, kind) {
			registered[reader.ResPath(editorImportText(data, node))] = true
		}
	}
	var fonts, images []editorResource
	var spaces []ColorSpace
	for _, resource := range e.resources {
		if resource.name != "" {
			parts[resource.name] = resource.data
		}
		if resource.definition() != "" && !registered[resource.name] {
			definitions = append(definitions, common.textXML("DocumentRes", "/"+resource.name)...)
			registered[resource.name] = true
		}
		if resource.font != nil {
			fonts = append(fonts, resource)
		}
		if resource.image != nil {
			images = append(images, resource)
		}
		if resource.space != nil {
			spaces = append(spaces, *resource.space)
		}
	}
	if len(fonts)+len(images)+len(spaces) > 0 {
		encoded, err := e.encodeXML(func(x *ofdXML) { x.resources(fonts, images, spaces) })
		if err != nil {
			return err
		}
		doc, err := reader.docInternal()
		if err != nil {
			return err
		}
		name, added, err := mergePackageResources(ctx, reader, parts, doc, e.packageDirectory(), encoded)
		if err != nil {
			return err
		}
		if added {
			definitions = append(definitions, common.textXML("DocumentRes", "/"+name)...)
		}
	}
	var patches []editorXMLPatch
	if maximum := common.child("MaxUnitID"); maximum != nil {
		value, _ := strconv.ParseUint(strings.TrimSpace(editorImportText(data, maximum)), 10, 32)
		if e.maxID > value {
			patches = append(patches, editorXMLPatch{maximum.start, maximum.end, maximum.textXML("MaxUnitID", formatUnitID(e.maxID))})
		}
	} else {
		patches = append(patches, editorXMLPatch{common.open, common.open, common.textXML("MaxUnitID", formatUnitID(e.maxID))})
	}
	if len(definitions) > 0 {
		at := common.close
		for _, node := range common.children {
			if node.matchesOFD("TemplatePage") || node.matchesOFD("DefaultCS") {
				at = node.start
				break
			}
		}
		patches = append(patches, editorXMLPatch{at, at, definitions})
	}
	parts[reader.documentRoot] = editorPatchXML(data, patches)
	scan := &editorFontDocument{reader: reader, index: reader.DocumentIndex()}
	if err := scan.apply(ctx, parts); err != nil {
		return err
	}
	previous := e.transactionSnapshot()
	if err := e.commitResourceSnapshot(ctx, reader); err != nil {
		return err
	}
	prevMap := make(map[string]int, len(previous.pages))
	for idx, p := range previous.pages {
		prevMap[p.ID] = idx
	}
	for i := range e.pages {
		oldIndex, exists := prevMap[e.pages[i].ID]
		if !exists || len(previous.pages[oldIndex].Content.Layer) == 0 {
			continue
		}
		states := make(map[string]GraphicObject)
		for _, layer := range previous.pages[oldIndex].Content.Layer {
			for _, object := range layer.Objects {
				states[editorObjectID(object)] = object
			}
		}
		page, err := e.page(i)
		if err != nil {
			return err
		}
		for j := range page.Content.Layer {
			for k := range page.Content.Layer[j].Objects {
				object := &page.Content.Layer[j].Objects[k]
				if old, ok := states[editorObjectID(*object)]; ok {
					object.state = old.state
					object.TextObject.layout = old.TextObject.layout
					object.CompositeObject.states = old.CompositeObject.states
				}
			}
		}
	}
	return nil
}

// canCommitLayerPartsIncremental 判断图层与模板改动是否可走增量提交而不重建全文档页面索引
// 入参: parts 待提交文件修改集
// 返回: bool 是否允许走增量提交路径
func (e *Editor) canCommitLayerPartsIncremental(parts map[string][]byte) bool {
	if e.source == nil || len(e.resources) != 0 || e.outlines != nil || !reflect.DeepEqual(e.Info, e.source.info) {
		return false
	}
	doc := e.source.document
	if doc == nil || len(e.pages) != len(doc.Pages.Page) {
		return false
	}
	if !canCommitIncrementalParts(e.source, parts) {
		return false
	}
	for i, page := range e.pages {
		if page.ID != doc.Pages.Page[i].ID {
			return false
		}
	}
	return true
}

// commitLayerPartsIncremental 增量提交图层与模板修改，仅精准失效并重载受影响的页面
// 入参: ctx 取消上下文, reader 当前快照阅读器, parts 待提交文件修改集, appendOnly 是否为不缩减原包引用的操作
// 返回: error 解析、提交或重载错误
func (e *Editor) commitLayerPartsIncremental(ctx context.Context, reader *Reader, parts map[string][]byte, appendOnly bool) error {
	base := e.source
	baseReader := base.reader
	docRoot := cleanPackagePath(baseReader.documentRoot)
	nextParts := maps.Clone(parts)
	if nextParts == nil {
		nextParts = make(map[string][]byte)
	}
	docData := nextParts[docRoot]
	needMaxID := e.maxID > base.document.CommonData.MaxUnitID
	if docData != nil || needMaxID {
		if docData == nil {
			var err error
			docData, err = baseReader.readFileViewContext(ctx, docRoot)
			if err != nil {
				return err
			}
		}
		if patched, ok := patchFastMaxUnitID(docData, e.maxID); ok {
			if !bytes.Equal(patched, docData) {
				nextParts[docRoot] = patched
			}
		} else {
			root, err := parseEditorXMLContext(ctx, docData)
			if err != nil {
				return err
			}
			common := root.child("CommonData")
			if common == nil {
				return fmt.Errorf("document has no CommonData")
			}
			var patches []editorXMLPatch
			if maximum := common.child("MaxUnitID"); maximum != nil {
				value, _ := strconv.ParseUint(strings.TrimSpace(editorImportText(docData, maximum)), 10, 32)
				if e.maxID > value {
					patches = append(patches, editorXMLPatch{maximum.start, maximum.end, maximum.textXML("MaxUnitID", formatUnitID(e.maxID))})
				}
			} else {
				patches = append(patches, editorXMLPatch{common.open, common.open, common.textXML("MaxUnitID", formatUnitID(e.maxID))})
			}
			if len(patches) != 0 {
				nextParts[docRoot] = editorPatchXML(docData, patches)
			}
		}
	}
	oldDoc := base.document
	dirtyPaths := make(map[string]bool, len(nextParts))
	for name := range nextParts {
		clean := cleanPackagePath(name)
		if file, ok := baseReader.packageFile(name); ok {
			clean = cleanPackagePath(file.Name)
		}
		if clean != docRoot {
			dirtyPaths[clean] = true
		}
	}
	if err := e.commitAnnotationPartsModeContext(ctx, base, nextParts, appendOnly); err != nil {
		return err
	}
	newDoc := e.source.document
	newReader := e.source.reader
	dirtyTemplates := make(map[string]bool)
	oldTemplates := make(map[string]TemplatePage, len(oldDoc.CommonData.TemplatePage))
	for _, tpl := range oldDoc.CommonData.TemplatePage {
		id := editorResourceID(tpl.ID)
		if id == "" {
			id = tpl.ID
		}
		oldTemplates[id] = tpl
		if dirtyPaths[cleanPackagePath(baseReader.ResPath(tpl.BaseLoc))] {
			dirtyTemplates[id] = true
		}
	}
	newTemplates := make(map[string]TemplatePage, len(newDoc.CommonData.TemplatePage))
	for _, tpl := range newDoc.CommonData.TemplatePage {
		id := editorResourceID(tpl.ID)
		if id == "" {
			id = tpl.ID
		}
		newTemplates[id] = tpl
		if prev, ok := oldTemplates[id]; !ok || prev != tpl || dirtyPaths[cleanPackagePath(newReader.ResPath(tpl.BaseLoc))] {
			dirtyTemplates[id] = true
		}
	}
	for id := range oldTemplates {
		if _, ok := newTemplates[id]; !ok {
			dirtyTemplates[id] = true
		}
	}
	rootDir := strings.TrimPrefix(strings.ReplaceAll(newReader.rootDir, "\\", "/"), "/")
	var dirtyPages []int
	for i := range e.pages {
		pageID := e.pages[i].ID
		srcPage := e.source.pages[pageID]
		if srcPage == nil {
			continue
		}
		newRef := newDoc.Pages.Page[i]
		changed := srcPage.ref.BaseLoc != newRef.BaseLoc
		if !changed && len(dirtyPaths) != 0 {
			if dirtyPaths[resPathFast(newReader, rootDir, srcPage.ref.BaseLoc)] {
				changed = true
			}
		}
		if !changed && srcPage.original != nil && len(dirtyTemplates) != 0 {
			for _, tpl := range e.pages[i].Template {
				id := editorResourceID(tpl.TemplateID)
				if id == "" {
					id = tpl.TemplateID
				}
				if dirtyTemplates[id] {
					changed = true
					break
				}
			}
		}
		if changed {
			dirtyPages = append(dirtyPages, i)
		}
	}
	if len(dirtyPages) == 0 {
		return nil
	}
	nextSource := *e.source
	nextSource.pages = maps.Clone(e.source.pages)
	nextSource.origins = maps.Clone(e.source.origins)
	e.source = &nextSource
	e.pages = slices.Clone(e.pages)
	for _, idx := range dirtyPages {
		pageID := e.pages[idx].ID
		oldSource := e.source.pages[pageID]
		wasLoaded := oldSource != nil && oldSource.original != nil
		var states map[string]GraphicObject
		if len(e.pages[idx].Content.Layer) != 0 {
			states = make(map[string]GraphicObject)
			for _, layer := range e.pages[idx].Content.Layer {
				for _, object := range layer.Objects {
					states[editorObjectID(object)] = object
				}
			}
		}
		if oldSource != nil {
			for id := range oldSource.nodes {
				if origin := e.source.origins[id]; origin != nil && origin.page == oldSource {
					delete(e.source.origins, id)
				}
			}
		}
		e.source.pages[pageID] = &editorSourcePage{ref: newDoc.Pages.Page[idx]}
		if wasLoaded {
			if err := e.loadSourcePageContext(ctx, idx); err != nil {
				return err
			}
			if len(states) != 0 {
				page := &e.pages[idx]
				for j := range page.Content.Layer {
					for k := range page.Content.Layer[j].Objects {
						object := &page.Content.Layer[j].Objects[k]
						if old, ok := states[editorObjectID(*object)]; ok {
							object.state = old.state
							object.TextObject.layout = old.TextObject.layout
							object.CompositeObject.states = old.CompositeObject.states
						}
					}
				}
			}
		} else {
			e.pages[idx] = PageContent{ID: pageID}
		}
	}
	return nil
}
