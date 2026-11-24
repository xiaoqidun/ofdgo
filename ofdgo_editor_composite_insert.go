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
	"image/color"
	"reflect"
	"strconv"
)

// AddCompositeObject 按页面坐标向内部范围末尾添加对象，复用已注册资源
// 入参: page 页面索引, path 父路径, object 基本对象或复合快照
// 返回: int 新成员序号, error 错误信息
func (e *Editor) AddCompositeObject(page int, path ObjectPath, object GraphicObject) (int, error) {
	indexes, err := e.CopyObjectsToComposite(page, path, []GraphicObject{object}, 0, 0)
	if err != nil {
		return 0, err
	}
	return indexes[0], nil
}

// CopyObjectsToComposite 将页面坐标快照复制到内部范围，隔离目标继承样式并分配新标识
// 保真快照保留原文，全部对象成功后提交一次撤销记录
// 入参: page 页面索引, path 父路径, objects 当前文档快照或基本对象, dx 页面横向位移, dy 页面纵向位移
// 返回: []int 新成员序号, error 错误信息
func (e *Editor) CopyObjectsToComposite(page int, path ObjectPath, objects []GraphicObject, dx, dy float64) ([]int, error) {
	metadata, err := e.copiedRichTextMetadata(objects)
	if err != nil {
		return nil, err
	}
	if len(metadata) == 0 {
		return e.copyObjectsToComposite(page, path, objects, dx, dy)
	}
	var indexes []int
	err = e.Transaction(func(e *Editor) error {
		var err error
		indexes, err = e.copyObjectsToComposite(page, path, objects, dx, dy)
		if err != nil {
			return err
		}
		return e.putCompositeRichTextCopies(page, path, indexes, metadata)
	})
	return indexes, err
}

// copyObjectsToComposite 插入对象原文，由调用方同步富文本语义
// 入参: page 页面索引, path 父路径, objects 对象快照, dx 横向位移, dy 纵向位移
// 返回: []int 成员序号, error 校验或提交错误
func (e *Editor) copyObjectsToComposite(page int, path ObjectPath, objects []GraphicObject, dx, dy float64) ([]int, error) {
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("copy requires finite offsets")
	}
	var result []int
	err := e.editCompositeScope(page, path, func(renderer *Renderer, root *editorCompositeNode, members []*editorCompositeNode) error {
		if len(objects) == 0 {
			return nil
		}
		owner := root.loadedScope(path.Children)
		if owner.ref != nil {
			owner = owner.ref
		}
		box, _ := ParseBox(owner.object.CompositeObject.Boundary)
		parent := owner.parent.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(NewMatrix(owner.object.CompositeObject.CTM))
		inverse, ok := parent.Invert()
		if !ok {
			return fmt.Errorf("composite insertion requires an invertible transform")
		}
		next := len(members)
		if container := owner.node.child("Content"); container != nil {
			next = 0
			for _, member := range members {
				if member.owner != owner || member.span.start < container.close {
					next++
				}
			}
		}
		dstScope := e.pageLocalScope(page)
		var content []byte
		validation := &editorValidation{Editor: e}
		for _, source := range objects {
			var srcScope []*parsedRes
			restoreOrigin := func() {}
			if srcOrigin := e.snapshotOrigin(source); srcOrigin != nil && srcOrigin.page != nil && srcOrigin.page.original != nil {
				srcScope = srcOrigin.page.original.pageResources
				restoreOrigin = e.bindPageResources(srcOrigin.page.original)
			}
			id, err := e.nextID()
			if err != nil {
				restoreOrigin()
				return err
			}
			object, err := validation.prepareCopiedObject(id, source)
			restoreOrigin()
			if err != nil {
				return err
			}
			maximum := e.maxID
			object, origin, err := e.copyObjectOrigin(source, object, &maximum)
			if err != nil {
				return err
			}
			e.maxID = maximum
			srcLayerStyle := e.copiedLayerStyle(source)
			object, origin, err = e.remapCopiedObject(object, origin, srcScope, dstScope, page)
			if err != nil {
				return err
			}
			targetLayerStyle, err := e.copiedTargetLayerStyle(srcLayerStyle, srcScope, dstScope)
			if err != nil {
				return err
			}
			var data []byte
			if origin != nil {
				data, err = editorXMLObject(origin.data, origin.node, origin.object, object)
				if err == nil {
					data, err = editorXMLStandalone(data, origin.node)
				}
			} else {
				data, err = editorObjectXML(object)
			}
			if err != nil {
				return err
			}
			node, err := newEditorCompositeNode(data)
			if err != nil {
				return err
			}
			if node.object.Type == "PathObject" {
				restoreSrc := e.bindParsedScope(srcScope)
				node.defaults = renderer.drawParamDefaults(srcLayerStyle, nil)
				style, styleErr := e.resolveEditorStyle(node.object, targetLayerStyle)
				_, fillErr := e.Color(style.PathObject.FillColor)
				_, strokeErr := e.Color((*FillColor)(style.PathObject.StrokeColor))
				restoreSrc()
				if styleErr != nil && editReason(styleErr) == EditUnsupportedColor || fillErr != nil || strokeErr != nil {
					node, err = e.wrapCompositePath(renderer, node)
					if err != nil {
						return err
					}
				}
			}
			node.parent, node.boundaryInCTM = IdentityMatrix(), true
			node.setStates(object.CompositeObject.states)
			node.drawParams = []string{targetLayerStyle}
			if object.Type == "CompositeObject" {
				if err := node.convertCoordinates(IdentityMatrix(), true); err != nil {
					return err
				}
			}
			if err := e.isolateCompositeStyle(node); err != nil {
				return err
			}
			if err := e.transformCompositeMember(renderer, node, inverse.Multiply(TranslationMatrix(dx, dy))); err != nil {
				return err
			}
			if !owner.boundaryInCTM {
				if err := node.convertCoordinates(parent, false); err != nil {
					return err
				}
			}
			state := object.state
			if object.TextObject.layout != nil && !reflect.DeepEqual(state.layout, object.TextObject.layout) {
				y, _ := strconv.ParseFloat(object.TextObject.TextCode[0].Y, 64)
				state.layout, state.origin = object.TextObject.layout, [2]float64{0, y}
			}
			node.setState(state)
			for id, state := range node.states {
				owner.states[id] = state
			}
			content = append(content, node.data...)
			result = append(result, next+len(result))
		}
		if container := owner.node.child("Content"); container != nil {
			if container.open == container.end {
				owner.patches = append(owner.patches, editorXMLContent(owner.data, container, content))
			} else {
				owner.patches = append(owner.patches, editorXMLPatch{container.close, container.close, content})
			}
		} else if owner.node.name.Local == "Appearance" {
			if owner.node.open == owner.node.end {
				owner.patches = append(owner.patches, editorXMLContent(owner.data, owner.node, content))
			} else {
				owner.patches = append(owner.patches, editorXMLPatch{owner.node.close, owner.node.close, content})
			}
		} else {
			content, err := editorXMLContainer("Content", nil, content)
			if err != nil {
				return err
			}
			content = bytes.TrimPrefix(content, []byte(xml.Header))
			if owner.node.open == owner.node.end {
				owner.patches = append(owner.patches, editorXMLContent(owner.data, owner.node, content))
			} else {
				owner.patches = append(owner.patches, editorXMLPatch{owner.node.close, owner.node.close, content})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// wrapCompositePath 通过标准矢量实例变换复杂路径，保持渐变坐标与原始颜色节点
// 入参: renderer 资源度量器, node 复杂路径
// 返回: *editorCompositeNode 包装节点, error 错误信息
func (e *Editor) wrapCompositePath(renderer *Renderer, node *editorCompositeNode) (*editorCompositeNode, error) {
	return e.vectorInstance(renderer, []*editorCompositeNode{node})
}

// isolateCompositeStyle 固定对象的绘制参数，避免目标容器改变颜色、虚线和端点样式
// 入参: node 页面坐标中的对象节点
// 返回: error 错误信息
func (e *Editor) isolateCompositeStyle(node *editorCompositeNode) error {
	if err := e.isolateCompositeDefaultPaint(node); err != nil {
		return err
	}
	if node.object.Type == "CompositeObject" || node.object.Type == "CompositeGraphicUnit" || node.object.Type == "PathObject" {
		id, err := e.neutralDrawParam()
		if err != nil {
			return err
		}
		for _, source := range node.drawParams {
			id, err = e.copyDrawParam(source, id, make(map[string]bool))
			if err != nil {
				return err
			}
		}
		parameter := node.object.CompositeObject.DrawParam
		if node.object.Type == "PathObject" {
			parameter = node.object.PathObject.DrawParam
		}
		id, err = e.copyDrawParam(parameter, id, make(map[string]bool))
		if err != nil {
			return err
		}
		object := node.object
		if object.Type == "PathObject" {
			object.PathObject.DrawParam = id
		} else {
			object.CompositeObject.DrawParam = id
		}
		return node.update(object)
	}
	object, err := e.resolveEditorStyleDefaults(node.object, &DrawParam{})
	if err != nil {
		return err
	}
	if object.Type == "TextObject" {
		id, err := e.neutralDrawParam()
		if err != nil {
			return err
		}
		object.TextObject.DrawParam = id
	}
	return node.update(object)
}

// isolateCompositeDefaultPaint 固定基准参数会覆盖的默认透明色，按继承环境复用资源副本
// 入参: node 待复制的独立节点
// 返回: error 参数、资源或XML错误
func (e *Editor) isolateCompositeDefaultPaint(node *editorCompositeNode) error {
	type resourceKey struct {
		id           string
		fill, stroke bool
	}
	cache := make(map[resourceKey]string)
	visiting := make(map[string]bool)
	var transparent *FillColor
	var isolate func(*editorCompositeNode, DrawParam) error
	isolate = func(current *editorCompositeNode, base DrawParam) error {
		parameter, err := e.editorDrawParam(current.node.attr("DrawParam"), make(map[string]bool))
		if err != nil {
			return err
		}
		base = *mergeDrawParam(base, parameter)
		if base.FillColor != nil && base.StrokeColor != nil {
			return nil
		}
		object := current.object
		path := object.Type == "PathObject" && object.PathObject.FillColor == nil && base.FillColor == nil
		text := object.Type == "TextObject" && object.TextObject.StrokeColor == nil && base.StrokeColor == nil
		if path || text {
			if transparent == nil {
				transparent, err = e.RGBColor(color.NRGBA{})
				if err != nil {
					return err
				}
			}
			if path {
				object.PathObject.FillColor = transparent
			} else {
				object.TextObject.StrokeColor = (*StrokeColor)(transparent)
			}
			return current.update(object)
		}
		if object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
			return nil
		}
		id := ""
		if object.Type == "CompositeObject" {
			id = object.CompositeObject.ResourceID
		}
		replacement := id
		if id != "" {
			key := resourceKey{editorResourceID(id), base.FillColor != nil, base.StrokeColor != nil}
			if key.id == "" {
				key.id = id
			}
			if visiting[key.id] {
				return fmt.Errorf("cyclic composite resource %q", id)
			}
			var ok bool
			replacement, ok = cache[key]
			if !ok {
				resource, err := e.compositeDefinition(id)
				if err != nil {
					return err
				}
				visiting[key.id] = true
				err = isolate(resource, base)
				delete(visiting, key.id)
				if err != nil {
					return err
				}
				replacement = id
				if resource.changed {
					for _, entry := range e.resources {
						if sameResourceID(entry.composite, id) {
							resource.setStates(entry.states)
							break
						}
					}
					replacement, err = e.addCompositeResource(resource.data, resource.states)
					if err != nil {
						return err
					}
					node.remaps = append(node.remaps, e.resources[len(e.resources)-1].objectIDs)
				}
				cache[key] = replacement
			}
		}
		var patches []editorXMLPatch
		var visit func(*editorXML) error
		visit = func(parent *editorXML) error {
			for _, child := range parent.children {
				if classifyOFDNamespace(child.name.Space) == ofdXMLUnknown {
					continue
				}
				switch child.name.Local {
				case "Content", "PageBlock":
					if err := visit(child); err != nil {
						return err
					}
				case "TextObject", "PathObject", "CompositeObject", "CompositeGraphicUnit":
					if child.name.Local == "TextObject" && (base.StrokeColor != nil || child.child("StrokeColor") != nil) || child.name.Local == "PathObject" && (base.FillColor != nil || child.child("FillColor") != nil) {
						continue
					}
					data, err := editorXMLStandalone(current.data[child.start:child.end], child)
					if err != nil {
						return err
					}
					member, err := newEditorCompositeNode(data)
					if err != nil {
						return err
					}
					if err := isolate(member, base); err != nil {
						return err
					}
					if member.changed {
						patches = append(patches, editorXMLPatch{child.start, child.end, member.data})
					}
				}
			}
			return nil
		}
		if err := visit(current.node); err != nil {
			return err
		}
		if len(patches) != 0 {
			next, err := newEditorCompositeNode(editorPatchXML(current.data, patches))
			if err != nil {
				return err
			}
			current.data, current.node, current.object, current.unit, current.changed = next.data, next.node, next.object, next.unit, true
		}
		if replacement != id {
			object = current.object
			object.CompositeObject.ResourceID = replacement
			return current.update(object)
		}
		return nil
	}
	var base DrawParam
	for _, id := range node.drawParams {
		parameter, err := e.editorDrawParam(id, make(map[string]bool))
		if err != nil {
			return err
		}
		base = *mergeDrawParam(base, parameter)
	}
	return isolate(node, base)
}

// neutralDrawParam 注册隔离继承用的基准参数，默认透明色由对象显式保留
// 返回: string 绘制参数标识, error 错误信息
func (e *Editor) neutralDrawParam() (string, error) {
	fill, err := e.RGBColor(color.NRGBA{A: 255})
	if err != nil {
		return "", err
	}
	zero := 0.0
	return e.addEditorDrawParam(DrawParam{LineWidth: defaultPathLineWidth, Cap: "Butt", Join: "Miter", MiterLimit: defaultMiterLimit, DashOffset: &zero, DashPatternSet: true, FillColor: fill, StrokeColor: (*StrokeColor)(fill)})
}

// addEditorDrawParam 注册已校验的绘制参数，相同定义复用资源
// 入参: draw 绘制参数
// 返回: string 资源标识, error 错误信息
func (e *Editor) addEditorDrawParam(draw DrawParam) (string, error) {
	ctx := context.Background()
	if e.output != nil && e.output.ctx != nil {
		ctx = e.output.ctx
	}
	return e.addEditorDrawParamContext(ctx, draw)
}

// addEditorDrawParamContext 注册已校验的参数，编码及引用收集期间响应取消
// 入参: ctx 取消上下文, draw 绘制参数
// 返回: string 资源标识, error 编码、扫描或取消错误
func (e *Editor) addEditorDrawParamContext(ctx context.Context, draw DrawParam) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	draw.ID = ""
	for _, resource := range e.resources {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if resource.draw != nil {
			previous := *resource.draw
			previous.ID = ""
			if reflect.DeepEqual(previous, draw) {
				return resource.draw.ID, nil
			}
		}
	}
	if err := e.prepareSourceIDsContext(ctx); err != nil {
		return "", err
	}
	drawID, err := e.nextID()
	if err != nil {
		return "", err
	}
	draw.ID = drawID
	data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) {
		x.root("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}})
		x.start("DrawParams", nil)
		attrs := ofdAttrs{}
		for _, pair := range [][2]string{{"ID", draw.ID}, {"Relative", draw.Relative}, {"Cap", draw.Cap}, {"Join", draw.Join}} {
			attrs.add(pair[0], pair[1])
		}
		attrs.number("LineWidth", draw.LineWidth)
		if draw.LineWidthSet && draw.LineWidth == 0 {
			attrs.add("LineWidth", "0")
		}
		attrs.number("MiterLimit", draw.MiterLimit)
		if draw.MiterLimitSet && draw.MiterLimit == 0 {
			attrs.add("MiterLimit", "0")
		}
		if draw.DashPattern != "" || draw.DashPatternSet && draw.Relative != "" {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "DashPattern"}, Value: draw.DashPattern})
		}
		if draw.DashOffset != nil {
			attrs.add("DashOffset", ofdNumber(*draw.DashOffset))
		}
		x.start("DrawParam", attrs)
		x.color("FillColor", draw.FillColor)
		x.color("StrokeColor", (*FillColor)(draw.StrokeColor))
		x.end("DrawParam")
		x.end("DrawParams")
		x.end("Res")
	})
	if err != nil {
		return "", err
	}
	refs, err := editorResourceReferencesContext(ctx, data)
	if err != nil {
		return "", err
	}
	resource := editorResource{name: e.packageName("Res/DrawParams/DrawParam_" + draw.ID + ".xml"), data: data, draw: &draw, references: refs}
	e.resources = append(e.resources, resource)
	return draw.ID, nil
}
