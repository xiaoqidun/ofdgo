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
// 入参: page 页面索引, path 父路径, objects 当前文档快照或基本对象, dx、dy 页面位移
// 返回: []int 新成员序号, error 错误信息
func (e *Editor) CopyObjectsToComposite(page int, path ObjectPath, objects []GraphicObject, dx, dy float64) ([]int, error) {
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("copy requires finite offsets")
	}
	var result []int
	err := e.editCompositeScope(page, path, func(renderer *Renderer, root *editorCompositeNode, members []*editorCompositeNode) error {
		if len(objects) == 0 {
			return nil
		}
		owner := root.loadedScope(path.Children)
		box, _ := ParseBox(owner.object.CompositeGraphicUnit.Boundary)
		parent := owner.parent.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(NewMatrix(owner.object.CompositeGraphicUnit.CTM))
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
		var content []byte
		for _, source := range objects {
			id := e.nextID()
			object, err := e.prepareCopiedObject(id, source)
			if err != nil {
				return err
			}
			maximum := e.maxID
			object, origin, err := e.copyObjectOrigin(source, object, &maximum)
			if err != nil {
				return err
			}
			e.maxID = maximum
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
				_, styleErr := e.resolveEditorStyle(node.object, e.copiedLayerStyle(source))
				if styleErr != nil && editReason(styleErr) == EditUnsupportedColor || !e.editorPaintable(node.object) {
					node, err = e.wrapCompositePath(node)
					if err != nil {
						return err
					}
				}
			}
			node.parent, node.boundaryInCTM = IdentityMatrix, true
			node.setStates(object.CompositeGraphicUnit.states)
			node.drawParams = []string{e.copiedLayerStyle(source)}
			if object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
				if err := node.convertCoordinates(IdentityMatrix, true); err != nil {
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

// wrapCompositePath 通过独立容器变换复杂路径，保持渐变坐标与原始颜色节点
// 入参: node 复杂路径
// 返回: *editorCompositeNode 包装节点, error 错误信息
func (e *Editor) wrapCompositePath(node *editorCompositeNode) (*editorCompositeNode, error) {
	content, err := editorXMLContainer("Content", nil, bytes.TrimPrefix(node.data, []byte(xml.Header)))
	if err != nil {
		return nil, err
	}
	data, err := editorXMLContainer("CompositeObject", ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: e.nextID()}, {Name: xml.Name{Local: "Boundary"}, Value: "0 0 1 1"}}, bytes.TrimPrefix(content, []byte(xml.Header)))
	if err != nil {
		return nil, err
	}
	return newEditorCompositeNode(data)
}

// isolateCompositeStyle 固定对象的绘制参数，避免目标容器改变颜色、虚线和线帽
// 入参: node 页面坐标中的对象节点
// 返回: error 错误信息
func (e *Editor) isolateCompositeStyle(node *editorCompositeNode) error {
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
		parameter := node.object.CompositeGraphicUnit.DrawParam
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
			object.CompositeGraphicUnit.DrawParam = id
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

// neutralDrawParam 注册可复用的完整默认绘制参数，不改变文档默认颜色空间
// 返回: string 绘制参数标识, error 错误信息
func (e *Editor) neutralDrawParam() (string, error) {
	fill, err := e.RGBColor(color.NRGBA{A: 255})
	if err != nil {
		return "", err
	}
	zero := 0.0
	return e.addEditorDrawParam(DrawParam{LineWidth: defaultPathLineWidth, Cap: "Butt", Join: "Miter", MiterLimit: defaultMiterLimit, DashOffset: &zero, dashPatternSet: true, FillColor: fill, StrokeColor: (*StrokeColor)(fill)})
}

// addEditorDrawParam 注册完整有效绘制参数，相同外观复用资源
// 入参: draw 不含继承关系的有效参数
// 返回: string 资源标识, error 错误信息
func (e *Editor) addEditorDrawParam(draw DrawParam) (string, error) {
	draw.ID, draw.Relative, draw.ResourceID, draw.BaseLoc, draw.Link = "", "", "", "", ""
	for _, resource := range e.resources {
		if resource.draw != nil {
			previous := *resource.draw
			previous.ID = ""
			if reflect.DeepEqual(previous, draw) {
				return resource.draw.ID, nil
			}
		}
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	draw.ID = e.nextID()
	data, err := encodeOFDXML(func(x *ofdXML) {
		x.root("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}})
		x.start("DrawParams", nil)
		attrs := ofdAttrs{}
		for _, pair := range [][2]string{{"ID", draw.ID}, {"LineWidth", ofdNumber(draw.LineWidth)}, {"Cap", draw.Cap}, {"Join", draw.Join}, {"MiterLimit", ofdNumber(draw.MiterLimit)}, {"Font", draw.Font}} {
			attrs.add(pair[0], pair[1])
		}
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "DashPattern"}, Value: draw.DashPattern})
		if draw.DashOffset != nil {
			attrs.add("DashOffset", ofdNumber(*draw.DashOffset))
		}
		attrs.number("Size", draw.Size)
		attrs.number("Weight", float64(draw.Weight))
		if draw.Italic {
			attrs.add("Italic", "true")
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
	refs, err := editorResourceReferences(data)
	if err != nil {
		return "", err
	}
	resource := editorResource{name: e.resourceDirectory() + "/DrawParam_" + draw.ID + ".xml", data: data, draw: &draw, references: refs}
	e.resources = append(e.resources, resource)
	return draw.ID, nil
}
