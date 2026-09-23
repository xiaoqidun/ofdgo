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

import "fmt"

// RenderState 保存对象继承的样式、父变换和页面坐标裁剪，所有引用只读
// Clip为nil表示未裁剪，空路径表示完全裁去，单位为毫米
type RenderState struct {
	Defaults      *DrawParam
	Parent        *Matrix
	BoundaryInCTM bool
	Clip          *GeometryPath
}

// PageVisitor 消费按标准图层顺序展开的对象与印章，源对象和状态只读
// 保留引用时文档也须保持不变，坐标与属性继续遵循OFD语义
// 复合图元的变换、透明度和引用由公共遍历处理，具体对象外观由访问器解释
type PageVisitor interface {
	DrawObject(object *GraphicObject, state RenderState) error
	DrawStamp(stamp Stamp) error
}

// PageGroups 提供可选的对象及注解分组，返回true时配对调用EndObject
// SkipObjects保持不可见复合图元的成员编号，供可编辑矢量输出使用
type PageGroups interface {
	BeginObject(object *GraphicObject) bool
	BeginAnnotation(id string) bool
	EndObject()
	SkipObjects(count int)
}

// WalkPage 按模板、图层、注解和印章顺序访问页面，不依赖任何绘图库
// 页面背景由调用方处理，文字裁剪可通过几何后端加载字体，图片由访问器按需加载
// 入参: page 页面内容, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) WalkPage(page *PageContent, visitor PageVisitor) error {
	for order := range 3 {
		if r.Reader.doc != nil {
			for _, ref := range page.Template {
				kind := ref.ZOrder
				if kind == "" {
					kind = "Background"
				}
				if layerOrder(kind) != order {
					continue
				}
				template := r.loadTemplate(ref.TemplateID)
				if template == nil {
					continue
				}
				for templateOrder := range 3 {
					if err := r.walkLayers(template.Content.Layer, templateOrder, visitor); err != nil {
						return err
					}
				}
			}
		}
		if err := r.walkLayers(page.Content.Layer, order, visitor); err != nil {
			return err
		}
	}
	if r.RenderAnnotations {
		for _, annotation := range r.Reader.Annots[page.ID] {
			if annotation.Visible != nil && !*annotation.Visible {
				continue
			}
			if err := r.walkAnnotation(annotation, visitor); err != nil {
				return err
			}
		}
	}
	for _, stamp := range r.Reader.Stamps[page.ID] {
		if err := visitor.DrawStamp(stamp); err != nil {
			return err
		}
	}
	return nil
}

// walkAnnotation 访问注解外观并保持独立分组和定位
// 入参: annotation 注解, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) walkAnnotation(annotation Annotation, visitor PageVisitor) error {
	if groups, ok := visitor.(PageGroups); ok && groups.BeginAnnotation(annotation.ID) {
		defer groups.EndObject()
	}
	box, _ := ParseBox(annotation.Appearance.Boundary)
	matrix := TranslationMatrix(box.X, box.Y)
	state := RenderState{Parent: &matrix}
	for i := range annotation.Appearance.Objects {
		if err := r.WalkObject(&annotation.Appearance.Objects[i], state, visitor); err != nil {
			return err
		}
	}
	return nil
}

// walkLayers 按原顺序访问指定层级，不重新排列同类图层
// 入参: layers 图层列表, order 层级顺序, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) walkLayers(layers []Layer, order int, visitor PageVisitor) error {
	for _, layer := range layers {
		if layerOrder(layer.Type) != order {
			continue
		}
		state := RenderState{Defaults: r.drawParamDefaults(layer.DrawParam, nil)}
		if len(layer.Objects) != 0 {
			for i := range layer.Objects {
				if err := r.WalkObject(&layer.Objects[i], state, visitor); err != nil {
					return err
				}
			}
			continue
		}
		for _, object := range layer.TextObject {
			if err := r.WalkObject(&GraphicObject{Type: "TextObject", TextObject: object}, state, visitor); err != nil {
				return err
			}
		}
		for _, object := range layer.PathObject {
			if err := r.WalkObject(&GraphicObject{Type: "PathObject", PathObject: object}, state, visitor); err != nil {
				return err
			}
		}
		for _, object := range layer.ImageObject {
			if err := r.WalkObject(&GraphicObject{Type: "ImageObject", ImageObject: object}, state, visitor); err != nil {
				return err
			}
		}
		for _, object := range layer.CompositeGraphicUnit {
			if err := r.WalkObject(&GraphicObject{Type: "CompositeGraphicUnit", CompositeGraphicUnit: object}, state, visitor); err != nil {
				return err
			}
		}
	}
	return nil
}

// WalkObject 访问单个对象或展开复合图元，保留父级裁剪和源对象标识
// 入参: object 源对象, state 继承状态, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) WalkObject(object *GraphicObject, state RenderState, visitor PageVisitor) error {
	return r.walkObject(object, state, visitor, nil)
}

// walkObject 访问对象并传递当前资源引用链
// 入参: object 源对象, state 继承状态, visitor 页面访问器, references 当前资源引用链
// 返回: error 遍历或访问器错误
func (r *Renderer) walkObject(object *GraphicObject, state RenderState, visitor PageVisitor, references map[string]bool) error {
	if groups, ok := visitor.(PageGroups); ok && groups.BeginObject(object) {
		defer groups.EndObject()
	}
	switch object.Type {
	case "TextObject", "PathObject", "ImageObject":
		return visitor.DrawObject(object, state)
	case "CompositeGraphicUnit", "CompositeObject":
		return r.walkComposite(object.CompositeGraphicUnit, state, visitor, references)
	default:
		return fmt.Errorf("unsupported graphic object %q", object.Type)
	}
}

// walkComposite 展开复合图元的资源、成员与透明度，不修改共享资源
// 入参: object 复合图元, state 继承状态, visitor 页面访问器, references 当前资源引用链
// 返回: error 几何或访问器错误
func (r *Renderer) walkComposite(object CompositeGraphicUnit, state RenderState, visitor PageVisitor, references map[string]bool) error {
	if object.Visible != nil && !*object.Visible {
		if groups, ok := visitor.(PageGroups); ok {
			count, ref, seen := len(object.Objects), object.ResourceID, make(map[string]bool)
			for ref != "" && !seen[ref] {
				seen[ref] = true
				unit := r.CompositeGraphicUnits[ref]
				if unit == nil {
					break
				}
				count, ref = count+len(unit.Objects), unit.ResourceID
			}
			groups.SkipObjects(count)
		}
		return nil
	}
	box, _ := ParseBox(object.Boundary)
	boundary := TranslationMatrix(box.X, box.Y)
	if state.Parent != nil {
		boundary = state.Parent.Multiply(boundary)
	}
	matrix := boundary.Multiply(NewMatrix(object.CTM))
	clips, clipMatrix := object.Clips, matrix
	if clips != nil && clips.TransFlag != nil && !*clips.TransFlag {
		copy := *clips
		copy.TransFlag = nil
		clips, clipMatrix = &copy, boundary
	}
	if clips != nil {
		geometry, err := r.Geometry()
		if err != nil {
			return err
		}
		state.Clip, err = geometry.Clip(r, clips, clipMatrix, state.Clip)
		if err != nil {
			return err
		}
	}
	state.Defaults = r.drawParamDefaults(object.DrawParam, state.Defaults)
	state.Parent = &matrix
	if object.ResourceID != "" {
		if references[object.ResourceID] {
			return fmt.Errorf("cyclic composite resource %q", object.ResourceID)
		}
		if ref, ok := r.CompositeGraphicUnits[object.ResourceID]; ok {
			if references == nil {
				references = make(map[string]bool)
			}
			references[object.ResourceID] = true
			copy := *ref
			copy.Alpha = mergeAlpha(copy.Alpha, object.Alpha)
			referenceState := state
			referenceState.BoundaryInCTM = true
			err := r.walkComposite(copy, referenceState, visitor, references)
			delete(references, object.ResourceID)
			if err != nil {
				return err
			}
		}
	}
	if len(object.Objects) != 0 {
		for _, member := range object.Objects {
			member = mergeGraphicObjectAlpha(member, object.Alpha)
			if err := r.walkObject(&member, state, visitor, references); err != nil {
				return err
			}
		}
		return nil
	}
	for _, member := range object.ImageObject {
		member.Alpha = mergeAlpha(member.Alpha, object.Alpha)
		if err := visitor.DrawObject(&GraphicObject{Type: "ImageObject", ImageObject: member}, state); err != nil {
			return err
		}
	}
	for _, member := range object.PathObject {
		member.Alpha = mergeAlpha(member.Alpha, object.Alpha)
		if err := visitor.DrawObject(&GraphicObject{Type: "PathObject", PathObject: member}, state); err != nil {
			return err
		}
	}
	for _, member := range object.TextObject {
		member.Alpha = mergeAlpha(member.Alpha, object.Alpha)
		if err := visitor.DrawObject(&GraphicObject{Type: "TextObject", TextObject: member}, state); err != nil {
			return err
		}
	}
	for _, member := range object.CompositeGraphicUnit {
		member.Alpha = mergeAlpha(member.Alpha, object.Alpha)
		if err := r.walkComposite(member, state, visitor, references); err != nil {
			return err
		}
	}
	return nil
}
