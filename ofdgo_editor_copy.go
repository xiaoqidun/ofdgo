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
	"image"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// editorClonePlans 仅共享类型的可变字段索引，不持有文档对象或资源
var editorClonePlans sync.Map

// copiedObjectStyle 保留复杂路径的原始颜色引用，普通对象沿用有效样式快照
// 入参: object 原对象, layer 图层绘制参数
// 返回: GraphicObject 复制快照, error 错误信息
func (e *Editor) copiedObjectStyle(object GraphicObject, layer string) (GraphicObject, error) {
	result, err := e.resolveEditorStyle(object, layer)
	if err != nil && object.Type == "PathObject" && editReason(err) == EditUnsupportedColor {
		return cloneEditorData(object), nil
	}
	return result, err
}

// snapshotOrigin 获取当前文档内捕获时的原文来源，外部快照按普通对象校验
// 入参: object 对象快照
// 返回: *editorObjectOrigin 原文来源
func (e *Editor) snapshotOrigin(object GraphicObject) *editorObjectOrigin {
	if object.origin != nil {
		if object.origin.editor == e && e.documentIdentity().matches(object.origin.document) || object.origin.page != nil && e.source != nil && e.source.pages[object.origin.page.ref.ID] == object.origin.page {
			return object.origin
		}
		return nil
	}
	return e.objectOrigin(editorObjectID(object))
}

// copyObjectOrigin 复制原XML并重映射内部标识和引用，不修改共享资源与原对象
// 入参: before 输入快照, after 副本快照, maximum 本批次最大标识
// 返回: GraphicObject 副本, *editorObjectOrigin 副本原文, error 错误信息
func (e *Editor) copyObjectOrigin(before, after GraphicObject, maximum *uint64) (GraphicObject, *editorObjectOrigin, error) {
	origin := e.snapshotOrigin(before)
	if origin == nil {
		return after, nil, nil
	}
	rootID := editorResourceID(origin.node.attr("ID"))
	ids := map[string]string{rootID: editorObjectID(after)}
	selfReference := false
	var collect func(*editorXML) error
	collect = func(node *editorXML) error {
		for _, attr := range node.attrs {
			if attr.Name.Space == "" && editorObjectReference(attr.Name.Local) && editorResourceID(attr.Value) == rootID {
				selfReference = true
			}
		}
		if (node.name.Local == "Font" || node.name.Local == "Substitution") && editorResourceID(editorImportText(origin.data, node)) == rootID {
			selfReference = true
		}
		for _, child := range node.children {
			if id := editorResourceID(child.attr("ID")); id != "" {
				if ids[id] != "" {
					return fmt.Errorf("duplicate object ID %q", id)
				}
				*maximum += 1
				ids[id] = formatUnitID(*maximum)
			}
			if err := collect(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(origin.node); err != nil {
		return GraphicObject{}, nil, err
	}
	if len(ids) == 1 && !selfReference {
		return after, origin, nil
	}
	data, err := editorXMLObject(origin.data, origin.node, origin.object, after)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	data, err = editorXMLStandalone(data, origin.node)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	data, err = editorXMLRemapIDs(data, root, ids)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	root, err = parseEditorXML(data)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	result := GraphicObject{Type: after.Type}
	var target any
	switch result.Type {
	case "TextObject":
		target = &result.TextObject
	case "PathObject":
		target = &result.PathObject
	case "ImageObject":
		target = &result.ImageObject
	case "CompositeObject", "CompositeGraphicUnit":
		target = &result.CompositeGraphicUnit
	}
	if err := xml.Unmarshal(data, target); err != nil {
		return GraphicObject{}, nil, err
	}
	result.TextObject.layout = after.TextObject.layout
	result.state = after.state
	result.CompositeGraphicUnit.states = remapCompositeStates(after.CompositeGraphicUnit.states, ids)
	root.parent = origin.node.parent
	return result, &editorObjectOrigin{page: origin.page, data: data, node: root, object: result}, nil
}

// copiedLayerStyle 保留复合对象和复杂路径的图层参数，普通对象使用已解析样式
// 入参: object 对象
// 返回: string 绘制参数标识
func (e *Editor) copiedLayerStyle(object GraphicObject) string {
	if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" || object.Type == "PathObject" {
		if origin := e.snapshotOrigin(object); origin != nil {
			for parent := origin.node.parent; parent != nil; parent = parent.parent {
				if parent.name.Local == "Layer" {
					id := parent.attr("DrawParam")
					if object.Type == "PathObject" {
						restore := func() {}
						if origin.page != nil && origin.page.original != nil {
							restore = e.bindPageResources(origin.page.original)
						}
						_, err := e.resolveEditorStyle(object, id)
						restore()
						if err == nil || editReason(err) != EditUnsupportedColor {
							return ""
						}
					}
					return id
				}
			}
		}
	}
	return ""
}

// copiedTargetLayerStyle 解析目标页面上的复制图层参数并在跨页局部冲突时克隆提升
// 入参: style 源图层绘制参数标识, srcScope 源页面局部资源作用域, dstScope 目标页面局部资源作用域
// 返回: string 目标页面可用的绘制参数标识, error 资源克隆错误
func (e *Editor) copiedTargetLayerStyle(style string, srcScope, dstScope []*parsedRes) (string, error) {
	if style == "" {
		return "", nil
	}
	mapped, _, err := e.promoteScopedResource(style, "DrawParam", srcScope, dstScope, false, make(map[string]bool))
	return mapped, err
}

// pageLocalScope 获取指定页面的局部资源列表
// 入参: page 页面索引
// 返回: []*parsedRes 页面局部资源作用域
func (e *Editor) pageLocalScope(page int) []*parsedRes {
	if len(e.pages[page].pageResources) > 0 {
		return e.pages[page].pageResources
	}
	if e.source != nil {
		if source := e.source.pages[e.pages[page].ID]; source != nil && source.original != nil {
			return source.original.pageResources
		}
	}
	return nil
}

// resolveScopeResourceFile 按局部作用域与文档公共资源优先级定位资源声明所在路径
// 入参: scope 页面局部资源作用域, id 资源标识
// 返回: string 命中的资源文件路径标识，未找到时为空
func (e *Editor) resolveScopeResourceFile(scope []*parsedRes, id string) string {
	for _, resource := range e.resources {
		if resID := resource.id(); resID != "" && sameResourceID(resID, id) {
			return "@editor/" + editorResourceID(id)
		}
	}
	for _, parsed := range slices.Backward(scope) {
		if parsedHasResource(parsed, id, "") {
			return parsed.path
		}
	}
	if e.source != nil {
		reader := e.source.reader
		common := e.source.document.CommonData
		for _, list := range [][]string{common.DocumentRes, common.PublicRes} {
			for _, loc := range slices.Backward(list) {
				parsed, err := reader.parseResFileContext(context.Background(), reader.ResPath(loc))
				if err == nil && parsedHasResource(parsed, id, "") {
					return parsed.path
				}
			}
		}
	}
	return ""
}

// promoteScopedResource 在源页与目标页资源作用域不一致时将源资源克隆注册为独立文档资源
// 入参: id 原资源标识, kind 期望资源类型, srcScope 源页面作用域, dstScope 目标页面作用域, forceDoc 是否作为文档级资源内部依赖, visiting 循环依赖检查集合
// 返回: string 目标资源标识, bool 是否已重映射, error 读取、解析或循环错误
func (e *Editor) promoteScopedResource(id, kind string, srcScope, dstScope []*parsedRes, forceDoc bool, visiting map[string]bool) (string, bool, error) {
	key := editorResourceID(id)
	if key == "" || e.source == nil {
		return id, false, nil
	}
	srcFile := e.resolveScopeResourceFile(srcScope, key)
	if srcFile == "" || strings.HasPrefix(srcFile, "@editor/") {
		return id, false, nil
	}
	dstFile := e.resolveScopeResourceFile(dstScope, key)
	docFile := e.resolveScopeResourceFile(nil, key)
	if srcFile == dstFile && (!forceDoc || srcFile == docFile) {
		return id, false, nil
	}
	cacheKey := srcFile + "\x00" + key
	if cached := e.promoted[cacheKey]; cached != "" && strings.HasPrefix(e.resolveScopeResourceFile(nil, cached), "@editor/") {
		return cached, true, nil
	}
	if visiting[cacheKey] {
		return "", false, fmt.Errorf("cyclic resource reference %q", id)
	}
	visiting[cacheKey] = true
	defer delete(visiting, cacheKey)
	reader := e.source.reader
	resData, err := reader.readFile(srcFile)
	if err != nil {
		return "", false, err
	}
	resRoot, err := parseEditorXML(resData)
	if err != nil {
		return "", false, err
	}
	var resNode *editorXML
	var groupName string
	for _, group := range resRoot.children {
		elemName := editorResourceElement(group.name.Local)
		if elemName == "" {
			continue
		}
		for _, child := range group.children {
			if child.matchesOFD(elemName) && sameResourceID(child.attr("ID"), key) {
				if kind == "" || elemName == kind || (kind == "Image" || kind == "Audio" || kind == "Video") && elemName == "MultiMedia" {
					resNode, groupName = child, group.name.Local
					break
				}
				if resNode == nil {
					resNode, groupName = child, group.name.Local
				}
			}
		}
		if resNode != nil && (kind == "" || editorResourceElement(groupName) == kind) {
			break
		}
	}
	if resNode == nil {
		return "", false, fmt.Errorf("resource %q not found in %s", id, srcFile)
	}
	var innerSrcScope []*parsedRes
	if srcFile != docFile {
		innerSrcScope = srcScope
		if len(innerSrcScope) == 0 {
			if parsed, parseErr := reader.parseResFileContext(context.Background(), srcFile); parseErr == nil && parsed != nil {
				innerSrcScope = []*parsedRes{parsed}
			}
		}
	}
	baseLoc := resRoot.attr("BaseLoc")
	standalone, err := editorXMLStandalone(resData[resNode.start:resNode.end], resNode)
	if err != nil {
		return "", false, err
	}
	var newID string
	switch groupName {
	case "Fonts":
		var f Font
		if err := xml.Unmarshal(standalone, &f); err != nil {
			return "", false, err
		}
		newID = e.nextID()
		f.ID = newID
		var res editorResource
		if loc := strings.TrimSpace(f.FontFile); loc != "" {
			fontPath := resolveResourcePath(srcFile, baseLoc, loc)
			fontBytes, readErr := reader.readFile(fontPath)
			if readErr != nil {
				return "", false, readErr
			}
			resName := e.packageName("Res/Fonts/Font_" + newID + strings.ToLower(path.Ext(fontPath)))
			f.FontFile = "/" + resName
			res = editorResource{name: resName, data: fontBytes, font: &f}
		} else {
			f.FontFile = ""
			res = editorResource{font: &f}
		}
		e.resources = append(e.resources, res)
	case "MultiMedias":
		mmRoot, parseErr := parseEditorXML(standalone)
		if parseErr != nil {
			return "", false, parseErr
		}
		standalone, _, err = e.remapXMLResources(standalone, mmRoot, innerSrcScope, dstScope, true, visiting)
		if err != nil {
			return "", false, err
		}
		var m MultiMedia
		if err := xml.Unmarshal(standalone, &m); err != nil {
			return "", false, err
		}
		newID = e.nextID()
		m.ID = newID
		mediaPath := resolveResourcePath(srcFile, baseLoc, strings.TrimSpace(m.MediaFile))
		mediaBytes, readErr := reader.readFile(mediaPath)
		if readErr != nil {
			return "", false, readErr
		}
		dir, prefix := "Res/Images", "Image_"
		if m.Type != "Image" {
			dir, prefix = "Res/Media", "Media_"
		}
		resName := e.packageName(dir + "/" + prefix + newID + strings.ToLower(path.Ext(mediaPath)))
		m.MediaFile = "/" + resName
		e.resources = append(e.resources, editorResource{name: resName, data: mediaBytes, image: &m})
		if m.Type == "Image" {
			if cfg, _, cfgErr := baseimage.DecodeConfigData(mediaBytes); cfgErr == nil {
				e.images[newID] = image.Pt(cfg.Width, cfg.Height)
			}
		}
	case "ColorSpaces":
		var cs ColorSpace
		if err := xml.Unmarshal(standalone, &cs); err != nil {
			return "", false, err
		}
		newID = e.nextID()
		cs.ID = newID
		cs.Palette = slices.Clone(cs.Palette)
		res := editorResource{space: &cs}
		if loc := strings.TrimSpace(cs.Profile); loc != "" {
			profPath := resolveResourcePath(srcFile, baseLoc, loc)
			profBytes, readErr := reader.readFile(profPath)
			if readErr != nil {
				return "", false, readErr
			}
			if parsedProf, profErr := parseColorProfile(cs.Type, profBytes); profErr == nil {
				cs.profile = parsedProf
			}
			res.name = e.packageName("Res/ColorSpaces/ColorSpace_" + newID + ".icc")
			res.data = profBytes
			cs.Profile = "/" + res.name
		}
		e.resources = append(e.resources, res)
	case "DrawParams":
		if linkLoc := strings.TrimSpace(resNode.attr("Link")); linkLoc != "" {
			extPath := resolveResourcePath(srcFile, baseLoc, linkLoc)
			extData, readErr := reader.readFile(extPath)
			if readErr != nil {
				return "", false, readErr
			}
			extRoot, parseErr := parseEditorXML(extData)
			if parseErr != nil {
				return "", false, parseErr
			}
			standalone, err = editorXMLStandalone(extData[extRoot.start:extRoot.end], extRoot)
			if err != nil {
				return "", false, err
			}
			extRoot, err = parseEditorXML(standalone)
			if err != nil {
				return "", false, err
			}
			standalone, err = editorXMLAttribute(standalone, extRoot, "ID", key)
			if err != nil {
				return "", false, err
			}
			if rel := resNode.attr("Relative"); rel != "" {
				extRoot, err = parseEditorXML(standalone)
				if err != nil {
					return "", false, err
				}
				standalone, err = editorXMLAttribute(standalone, extRoot, "Relative", rel)
				if err != nil {
					return "", false, err
				}
			}
		}
		var dp DrawParam
		wrapped, objIDs, err := e.promoteXMLResource(standalone, groupName, &dp, innerSrcScope, dstScope, visiting)
		if err != nil {
			return "", false, err
		}
		newID = objIDs[key]
		refs, err := editorResourceReferences(wrapped)
		if err != nil {
			return "", false, err
		}
		e.resources = append(e.resources, editorResource{name: e.packageName("Res/DrawParams/DrawParam_" + newID + ".xml"), data: wrapped, draw: &dp, references: refs})
	case "CompositeGraphicUnits":
		if extLoc := strings.TrimSpace(resNode.attr("BaseLoc")); extLoc != "" {
			extPath := resolveResourcePath(srcFile, baseLoc, extLoc)
			extData, readErr := reader.readFile(extPath)
			if readErr != nil {
				return "", false, readErr
			}
			extRoot, parseErr := parseEditorXML(extData)
			if parseErr != nil {
				return "", false, parseErr
			}
			extStandalone, stdErr := editorXMLStandalone(extData[extRoot.start:extRoot.end], extRoot)
			if stdErr != nil {
				return "", false, stdErr
			}
			extParsed, pErr := parseEditorXML(extStandalone)
			if pErr != nil {
				return "", false, pErr
			}
			var innerContent []byte
			if extParsed.name.Local == "CompositeGraphicUnit" {
				if extParsed.open != extParsed.end {
					innerContent = bytes.Clone(extStandalone[extParsed.open:extParsed.close])
				}
			} else {
				wrappedContent, wrapErr := editorXMLContainer("Content", nil, bytes.TrimPrefix(extStandalone, []byte(xml.Header)))
				if wrapErr != nil {
					return "", false, wrapErr
				}
				innerContent = bytes.TrimPrefix(wrappedContent, []byte(xml.Header))
			}
			cguRoot, cErr := parseEditorXML(standalone)
			if cErr != nil {
				return "", false, cErr
			}
			patch, attrErr := editorXMLAttributeChange(standalone, cguRoot, "BaseLoc", nil)
			if attrErr != nil {
				return "", false, attrErr
			}
			standalone = editorPatchXML(standalone, []editorXMLPatch{patch})
			cguRoot, err = parseEditorXML(standalone)
			if err != nil {
				return "", false, err
			}
			if cguRoot.open == cguRoot.end {
				standalone = editorPatchXML(standalone, []editorXMLPatch{editorXMLContent(standalone, cguRoot, innerContent)})
			} else {
				standalone = editorPatchXML(standalone, []editorXMLPatch{{cguRoot.close, cguRoot.close, innerContent}})
			}
		}
		wrapped, objIDs, err := e.promoteXMLResource(standalone, groupName, nil, innerSrcScope, dstScope, visiting)
		if err != nil {
			return "", false, err
		}
		newID = objIDs[key]
		res, err := e.compositeResource(newID, wrapped)
		if err != nil {
			return "", false, err
		}
		res.objectIDs = objIDs
		e.resources = append(e.resources, res)
	default:
		return id, false, nil
	}
	e.promoted[cacheKey] = newID
	return newID, true, nil
}

// promoteXMLResource 重映射定义节点的资源依赖与内部标识并封装为独立Res文档
// 入参: standalone 独立资源定义片段, group 容器元素名, target 可选解码目标, srcScope 源页面作用域, dstScope 目标页面作用域, visiting 循环检查集合
// 返回: []byte 独立Res文档, map[string]string 内部标识映射, error 解析或重映射错误
func (e *Editor) promoteXMLResource(standalone []byte, group string, target any, srcScope, dstScope []*parsedRes, visiting map[string]bool) ([]byte, map[string]string, error) {
	root, err := parseEditorXML(standalone)
	if err != nil {
		return nil, nil, err
	}
	standalone, changed, err := e.remapXMLResources(standalone, root, srcScope, dstScope, true, visiting)
	if err != nil {
		return nil, nil, err
	}
	if changed {
		root, err = parseEditorXML(standalone)
		if err != nil {
			return nil, nil, err
		}
	}
	objIDs := make(map[string]string)
	if err := e.collectCompositeIDs(root, objIDs); err != nil {
		return nil, nil, err
	}
	standalone, err = editorXMLRemapOnlyIDs(standalone, root, objIDs)
	if err != nil {
		return nil, nil, err
	}
	if target != nil {
		if err := xml.Unmarshal(standalone, target); err != nil {
			return nil, nil, err
		}
	}
	wrapped, err := editorXMLContainer(group, nil, bytes.TrimPrefix(standalone, []byte(xml.Header)))
	if err != nil {
		return nil, nil, err
	}
	wrapped, err = editorXMLContainer("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}}, bytes.TrimPrefix(wrapped, []byte(xml.Header)))
	if err != nil {
		return nil, nil, err
	}
	return wrapped, objIDs, nil
}

// remapXMLResources 扫描并按需克隆提升XML片段引用的页面局部资源
// 入参: data XML片段, root 解析根节点, srcScope 源页面作用域, dstScope 目标页面作用域, forceDoc 是否按文档级依赖处理, visiting 循环检查集合
// 返回: []byte 更新后的XML片段, bool 是否发生重写, error 资源克隆或编码错误
func (e *Editor) remapXMLResources(data []byte, root *editorXML, srcScope, dstScope []*parsedRes, forceDoc bool, visiting map[string]bool) ([]byte, bool, error) {
	if e.source == nil || len(srcScope) == 0 && len(dstScope) == 0 {
		return data, false, nil
	}
	var patches []editorXMLPatch
	var visit func(*editorXML) error
	visit = func(node *editorXML) error {
		if classifyOFDNamespace(node.name.Space) != ofdXMLUnknown {
			decoder := xml.NewDecoder(bytes.NewReader(data[node.start:node.open]))
			token, err := decoder.RawToken()
			if err != nil {
				return err
			}
			start := token.(xml.StartElement)
			changed := false
			for i := range start.Attr {
				attr := &start.Attr[i]
				if attr.Name.Space != "" {
					continue
				}
				kind, isResRef := editorScopedResourceAttrKind(node.name.Local, attr.Name.Local)
				if !isResRef {
					continue
				}
				mapped, ok, promErr := e.promoteScopedResource(attr.Value, kind, srcScope, dstScope, forceDoc, visiting)
				if promErr != nil {
					return promErr
				}
				if ok {
					attr.Value, changed = mapped, true
				}
			}
			if changed {
				encoded, encErr := encodeStartElementToken(start, node.open == node.end)
				if encErr != nil {
					return encErr
				}
				patches = append(patches, editorXMLPatch{node.start, node.open, encoded})
			}
			if len(node.children) == 0 {
				textKind := ""
				if node.name.Local == "Thumbnail" || node.name.Local == "Substitution" {
					textKind = "Image"
				} else if node.name.Local == "Font" && node.attr("ID") == "" {
					textKind = "Font"
				}
				if textKind != "" {
					mapped, ok, promErr := e.promoteScopedResource(strings.TrimSpace(editorImportText(data, node)), textKind, srcScope, dstScope, forceDoc, visiting)
					if promErr != nil {
						return promErr
					}
					if ok {
						patches = append(patches, editorXMLContent(data, node, []byte(mapped)))
					}
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
		return nil, false, err
	}
	if len(patches) == 0 {
		return data, false, nil
	}
	return editorPatchXML(data, patches), true, nil
}

// remapCopiedObject 为跨页复制的对象副本克隆提升局部资源并重写引用
// 入参: object 已准备的对象副本, origin 副本原文来源, srcScope 源页面局部资源, dstScope 目标页面局部资源, targetPage 目标页面索引
// 返回: GraphicObject 更新后的对象副本, *editorObjectOrigin 更新后的原文来源, error 重映射或解码错误
func (e *Editor) remapCopiedObject(object GraphicObject, origin *editorObjectOrigin, srcScope, dstScope []*parsedRes, targetPage int) (GraphicObject, *editorObjectOrigin, error) {
	if e.source == nil || len(srcScope) == 0 && len(dstScope) == 0 {
		return object, origin, nil
	}
	targetSource := e.source.pages[e.pages[targetPage].ID]
	var data []byte
	var err error
	if origin != nil {
		data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
		if err == nil {
			data, err = editorXMLStandalone(data, origin.node)
		}
	} else {
		data, err = editorObjectXML(object)
	}
	if err != nil {
		return GraphicObject{}, nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	remapped, changed, err := e.remapXMLResources(data, root, srcScope, dstScope, false, make(map[string]bool))
	if err != nil {
		return GraphicObject{}, nil, err
	}
	if !changed {
		if origin != nil && origin.page != targetSource {
			copiedOrigin := *origin
			copiedOrigin.page = targetSource
			origin = &copiedOrigin
		}
		return object, origin, nil
	}
	result := GraphicObject{Type: object.Type}
	var target any
	switch result.Type {
	case "TextObject":
		target = &result.TextObject
	case "PathObject":
		target = &result.PathObject
	case "ImageObject":
		target = &result.ImageObject
	case "CompositeObject", "CompositeGraphicUnit":
		target = &result.CompositeGraphicUnit
	}
	if err := xml.Unmarshal(remapped, target); err != nil {
		return GraphicObject{}, nil, err
	}
	result.TextObject.layout = object.TextObject.layout
	result.ImageObject.LineWidthSet = object.ImageObject.LineWidthSet
	result.ImageObject.MiterLimitSet = object.ImageObject.MiterLimitSet
	result.ImageObject.dashPatternSet = object.ImageObject.dashPatternSet
	result.state = object.state
	result.CompositeGraphicUnit.states = object.CompositeGraphicUnit.states
	if origin == nil {
		return result, nil, nil
	}
	newRoot, err := parseEditorXML(remapped)
	if err != nil {
		return GraphicObject{}, nil, err
	}
	newRoot.parent = origin.node.parent
	return result, &editorObjectOrigin{page: targetSource, data: remapped, node: newRoot, object: result}, nil
}

// editorXMLRemapIDs 仅重写标准标识和指向副本内部的引用，保留扩展、文字和XML结构
// 入参: data 独立片段, root 根节点, ids 原标识与新标识
// 返回: []byte 修改后的片段, error 错误信息
func editorXMLRemapIDs(data []byte, root *editorXML, ids map[string]string) ([]byte, error) {
	return remapEditorXMLIDs(data, root, ids, true)
}

// editorObjectReference 判断对象内可按ST_RefID重映射的标准属性
// 入参: name 属性名
// 返回: bool 是否为标识引用
func editorObjectReference(name string) bool {
	switch name {
	case "Font", "ResourceID", "Substitution", "ImageMask", "Relative", "DrawParam", "ColorSpace", "Thumbnail", "TemplateID", "PageID", "PageRef", "RefId":
		return true
	}
	return false
}

// editorXMLRemapOnlyIDs 仅重写XML节点树上的ID属性而不触碰外部资源引用属性
// 入参: data 独立片段, root 根节点, ids 原标识与新标识映射
// 返回: []byte 修改后的片段, error 编码错误
func editorXMLRemapOnlyIDs(data []byte, root *editorXML, ids map[string]string) ([]byte, error) {
	return remapEditorXMLIDs(data, root, ids, false)
}

// remapEditorXMLIDs 遍历重写标准节点的标识属性及可选的内部引用
// 入参: data 独立片段, root 根节点, ids 原标识与新标识映射, includeRefs 是否同时重写引用属性和文本节点
// 返回: []byte 修改后的片段, error 编码错误
func remapEditorXMLIDs(data []byte, root *editorXML, ids map[string]string, includeRefs bool) ([]byte, error) {
	var patches []editorXMLPatch
	var visit func(*editorXML) error
	visit = func(node *editorXML) error {
		if classifyOFDNamespace(node.name.Space) != ofdXMLUnknown {
			decoder := xml.NewDecoder(bytes.NewReader(data[node.start:node.open]))
			token, err := decoder.RawToken()
			if err != nil {
				return err
			}
			start := token.(xml.StartElement)
			changed := false
			for i := range start.Attr {
				attr := &start.Attr[i]
				if attr.Name.Space != "" {
					continue
				}
				if attr.Name.Local == "ID" || includeRefs && editorObjectReference(attr.Name.Local) {
					if value := ids[editorResourceID(attr.Value)]; value != "" {
						attr.Value, changed = value, true
					}
				}
			}
			if changed {
				value, err := encodeStartElementToken(start, node.open == node.end)
				if err != nil {
					return err
				}
				patches = append(patches, editorXMLPatch{node.start, node.open, value})
			}
			if includeRefs && (node.name.Local == "Font" || node.name.Local == "Substitution" || node.name.Local == "Thumbnail") {
				if value := ids[editorResourceID(editorImportText(data, node))]; value != "" {
					patches = append(patches, editorXMLContent(data, node, []byte(value)))
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
		return nil, err
	}
	return editorPatchXML(data, patches), nil
}

// encodeStartElementToken 序列化保留原前缀的起始标签Token
// 入参: start 起始标签, selfClosing 是否自闭合
// 返回: []byte 标签字节切片, error 编码错误
func encodeStartElementToken(start xml.StartElement, selfClosing bool) ([]byte, error) {
	if start.Name.Space != "" {
		start.Name.Local = start.Name.Space + ":" + start.Name.Local
		start.Name.Space = ""
	}
	for i := range start.Attr {
		attr := &start.Attr[i]
		if attr.Name.Space != "" {
			attr.Name.Local = attr.Name.Space + ":" + attr.Name.Local
			attr.Name.Space = ""
		}
	}
	var header bytes.Buffer
	encoder := xml.NewEncoder(&header)
	if err := encoder.EncodeToken(start); err != nil {
		return nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	value := header.Bytes()
	if selfClosing {
		value = append(value[:len(value)-1], '/', '>')
	}
	return value, nil
}

// editorScopedResourceAttrKind 判断属性是否为资源表引用并返回预期资源类别
// 入参: elem 元素局部名, attr 属性局部名
// 返回: string 期望资源类别, bool 是否为资源表引用属性
func editorScopedResourceAttrKind(elem, attr string) (string, bool) {
	switch attr {
	case "Font":
		return "Font", true
	case "DrawParam", "Relative":
		return "DrawParam", true
	case "ColorSpace":
		return "ColorSpace", true
	case "ImageMask", "Substitution", "Thumbnail":
		return "Image", true
	case "ResourceID":
		switch elem {
		case "ImageObject":
			return "Image", true
		case "Sound":
			return "Audio", true
		case "Movie":
			return "Video", true
		case "DrawParam":
			return "DrawParam", true
		case "CompositeObject", "CompositeGraphicUnit":
			return "CompositeGraphicUnit", true
		}
		return "", true
	}
	return "", false
}

// cloneEditorData 深复制编辑快照，未导出的排版及原文来源保持不可变共享
// 入参: value 原值
// 返回: T 独立副本
func cloneEditorData[T any](value T) T {
	switch source := any(value).(type) {
	case GraphicObject:
		return any(copyEditorGraphicObject(source)).(T)
	case TextObject:
		return any(copyEditorTextObject(source)).(T)
	case PathObject:
		return any(copyEditorPathObject(source)).(T)
	case ImageObject:
		return any(copyEditorImageObject(source)).(T)
	case CompositeGraphicUnit:
		return any(copyEditorCompositeGraphicUnit(source)).(T)
	case PageContent:
		return any(copyEditorPageContent(source)).(T)
	case Layer:
		return any(copyEditorLayer(source)).(T)
	}
	source := reflect.ValueOf(value)
	if !source.IsValid() {
		return value
	}
	return cloneEditorValue(source).Interface().(T)
}

// cloneEditorValue 复制公开指针、切片及数组，纯值数据不创建额外结构
// 入参: value 原值
// 返回: reflect.Value 独立副本
func cloneEditorValue(value reflect.Value) reflect.Value {
	if !value.IsValid() || !editorCloneMutable(value.Type()) {
		return value
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return value
		}
		result := reflect.New(value.Type().Elem())
		cloneEditorInto(result.Elem(), value.Elem())
		return result
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		if !editorCloneMutable(value.Type().Elem()) {
			reflect.Copy(result, value)
		} else {
			for i := range value.Len() {
				cloneEditorInto(result.Index(i), value.Index(i))
			}
		}
		return result
	default:
		result := reflect.New(value.Type()).Elem()
		cloneEditorInto(result, value)
		return result
	}
}

// cloneEditorInto 直接填充目标结构，避免指针及切片成员的二次结构分配
// 入参: target 可写目标, source 原值
func cloneEditorInto(target, source reflect.Value) {
	switch source.Kind() {
	case reflect.Struct:
		target.Set(source)
		for _, i := range editorCloneFields(source.Type()) {
			cloneEditorInto(target.Field(i), source.Field(i))
		}
	case reflect.Array:
		target.Set(source)
		if editorCloneMutable(source.Type().Elem()) {
			for i := range source.Len() {
				cloneEditorInto(target.Index(i), source.Index(i))
			}
		}
	default:
		target.Set(cloneEditorValue(source))
	}
}

// editorCloneMutable 判断类型是否含有需要隔离的公开可变成员
// 入参: kind 数据类型
// 返回: bool 是否需要独立复制
func editorCloneMutable(kind reflect.Type) bool {
	switch kind.Kind() {
	case reflect.Pointer, reflect.Slice:
		return true
	case reflect.Array:
		return editorCloneMutable(kind.Elem())
	case reflect.Struct:
		return len(editorCloneFields(kind)) != 0
	}
	return false
}

// editorCloneFields 按类型复用公开可变字段，索引发布后保持只读
// 入参: kind 结构类型
// 返回: []int 可变字段索引
func editorCloneFields(kind reflect.Type) []int {
	if cached, ok := editorClonePlans.Load(kind); ok {
		return cached.([]int)
	}
	var fields []int
	for i := range kind.NumField() {
		field := kind.Field(i)
		if field.IsExported() && editorCloneMutable(field.Type) {
			fields = append(fields, i)
		}
	}
	actual, _ := editorClonePlans.LoadOrStore(kind, fields)
	return actual.([]int)
}

// copyEditorGraphicObject 复制GraphicObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: GraphicObject 独立副本
func copyEditorGraphicObject(value GraphicObject) GraphicObject {
	value.TextObject = copyEditorTextObject(value.TextObject)
	value.PathObject = copyEditorPathObject(value.PathObject)
	value.ImageObject = copyEditorImageObject(value.ImageObject)
	value.CompositeGraphicUnit = copyEditorCompositeGraphicUnit(value.CompositeGraphicUnit)
	return value
}

// copyEditorTextObject 复制TextObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: TextObject 独立副本
func copyEditorTextObject(value TextObject) TextObject {
	if value.DashOffset != nil {
		copied := *value.DashOffset
		value.DashOffset = &copied
	}
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	if value.Fill != nil {
		copied := *value.Fill
		value.Fill = &copied
	}
	if value.Stroke != nil {
		copied := *value.Stroke
		value.Stroke = &copied
	}
	value.StrokeColor = cloneEditorData(value.StrokeColor)
	value.FillColor = cloneEditorData(value.FillColor)
	value.CGTransform = slices.Clone(value.CGTransform)
	value.TextCode = slices.Clone(value.TextCode)
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorPathObject 复制PathObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: PathObject 独立副本
func copyEditorPathObject(value PathObject) PathObject {
	if value.DashOffset != nil {
		copied := *value.DashOffset
		value.DashOffset = &copied
	}
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	if value.Stroke != nil {
		copied := *value.Stroke
		value.Stroke = &copied
	}
	if value.Fill != nil {
		copied := *value.Fill
		value.Fill = &copied
	}
	value.StrokeColor = cloneEditorData(value.StrokeColor)
	value.FillColor = cloneEditorData(value.FillColor)
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorImageObject 复制ImageObject的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: ImageObject 独立副本
func copyEditorImageObject(value ImageObject) ImageObject {
	if value.DashOffset != nil {
		copied := *value.DashOffset
		value.DashOffset = &copied
	}
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	value.Border = cloneEditorData(value.Border)
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorCompositeGraphicUnit 复制CompositeGraphicUnit的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: CompositeGraphicUnit 独立副本
func copyEditorCompositeGraphicUnit(value CompositeGraphicUnit) CompositeGraphicUnit {
	if value.Alpha != nil {
		copied := *value.Alpha
		value.Alpha = &copied
	}
	if value.Visible != nil {
		copied := *value.Visible
		value.Visible = &copied
	}
	value.Objects = slices.Clone(value.Objects)
	for i := range value.Objects {
		value.Objects[i] = copyEditorGraphicObject(value.Objects[i])
	}
	value.TextObject = slices.Clone(value.TextObject)
	for i := range value.TextObject {
		value.TextObject[i] = copyEditorTextObject(value.TextObject[i])
	}
	value.PathObject = slices.Clone(value.PathObject)
	for i := range value.PathObject {
		value.PathObject[i] = copyEditorPathObject(value.PathObject[i])
	}
	value.ImageObject = slices.Clone(value.ImageObject)
	for i := range value.ImageObject {
		value.ImageObject[i] = copyEditorImageObject(value.ImageObject[i])
	}
	value.CompositeGraphicUnit = slices.Clone(value.CompositeGraphicUnit)
	for i := range value.CompositeGraphicUnit {
		value.CompositeGraphicUnit[i] = copyEditorCompositeGraphicUnit(value.CompositeGraphicUnit[i])
	}
	value.Clips = cloneEditorData(value.Clips)
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorPageContent 复制PageContent的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: PageContent 独立副本
func copyEditorPageContent(value PageContent) PageContent {
	value.Template = slices.Clone(value.Template)
	value.PageRes = slices.Clone(value.PageRes)
	value.Content.Layer = slices.Clone(value.Content.Layer)
	for i := range value.Content.Layer {
		value.Content.Layer[i] = copyEditorLayer(value.Content.Layer[i])
	}
	value.Actions = cloneEditorData(value.Actions)
	return value
}

// copyEditorLayer 复制Layer的可变字段，保留内部只读状态
// 入参: value 原值
// 返回: Layer 独立副本
func copyEditorLayer(value Layer) Layer {
	value.Objects = slices.Clone(value.Objects)
	for i := range value.Objects {
		value.Objects[i] = copyEditorGraphicObject(value.Objects[i])
	}
	value.TextObject = slices.Clone(value.TextObject)
	for i := range value.TextObject {
		value.TextObject[i] = copyEditorTextObject(value.TextObject[i])
	}
	value.PathObject = slices.Clone(value.PathObject)
	for i := range value.PathObject {
		value.PathObject[i] = copyEditorPathObject(value.PathObject[i])
	}
	value.ImageObject = slices.Clone(value.ImageObject)
	for i := range value.ImageObject {
		value.ImageObject[i] = copyEditorImageObject(value.ImageObject[i])
	}
	value.CompositeGraphicUnit = slices.Clone(value.CompositeGraphicUnit)
	for i := range value.CompositeGraphicUnit {
		value.CompositeGraphicUnit[i] = copyEditorCompositeGraphicUnit(value.CompositeGraphicUnit[i])
	}
	return value
}
