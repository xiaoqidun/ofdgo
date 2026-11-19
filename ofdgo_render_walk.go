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
	"fmt"
	"strings"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
)

// RenderState 保存对象继承的样式、父变换和页面坐标裁剪，所有引用只读
// Clip为nil表示未提供裁剪，空路径表示完全裁去，单位为毫米
// Hidden表示当前对象或其上层容器不可见，不改变源对象的Visible属性
type RenderState struct {
	Defaults      *DrawParam
	Parent        *Matrix
	BoundaryInCTM bool
	Hidden        bool
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

// PageComposites 可选地接收展开前的复合图元及矢量资源，不替代成员遍历
// 状态包含当前图元到页面的完整变换，源对象及状态只读
type PageComposites interface {
	VisitComposite(object *CompositeGraphicUnit, state RenderState) error
}

// PageAnalysis 控制WalkPage和WalkObject对隐藏内容及裁剪的处理，不影响已构建场景的重放
// 跳过裁剪时RenderState.Clip为空，源对象的裁剪定义不变，叶对象仍由访问器解释
type PageAnalysis interface {
	IncludeHidden() bool
	SkipClips() bool
}

// PageTransparencyGroups 提供可选的离屏透明组隔离，返回true时配对调用EndTransparencyGroup
type PageTransparencyGroups interface {
	BeginTransparencyGroup(alpha uint8) bool
	EndTransparencyGroup()
}

// WalkPage 按模板、图层、注解和印章顺序访问页面，不依赖任何绘图库
// 页面背景由调用方处理，文字裁剪可通过几何后端加载字体，图片由访问器按需加载
// 注释与印章按当前页惰性加载，不触发整份文档全量解析
// 入参: page 页面内容, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) WalkPage(page *PageContent, visitor PageVisitor) error {
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Reader != nil && r.Reader.doc == nil && r.Reader.OFD != nil {
		if _, err := r.Reader.docStructureContext(ctx); err != nil {
			return err
		}
	}
	defer r.bindPageResources(page)()
	var pageOrigin *Matrix
	var offsetX, offsetY float64
	if box, err := r.GetPageBox(page); err == nil && (box.X != 0 || box.Y != 0) {
		offsetX, offsetY = -box.X, -box.Y
		shift := TranslationMatrix(offsetX, offsetY)
		pageOrigin = &shift
		if r.patternPage == nil {
			r.patternPage = &shift
			defer func() { r.patternPage = nil }()
		}
	}
	for order := range 3 {
		if r.printing && (r.printContents == "annotations" || r.printContents == "forms") {
			break
		}
		if r.Reader != nil && r.Reader.doc != nil {
			for _, ref := range page.Template {
				if r.templateOrder(ref) != order {
					continue
				}
				template := r.loadTemplate(ref.TemplateID)
				if template == nil {
					continue
				}
				restoreTpl := r.bindParsedResources(template.pageResources)
				for templateOrder := range 3 {
					if err := r.walkLayers(template.Content.Layer, templateOrder, pageOrigin, visitor); err != nil {
						restoreTpl()
						return err
					}
				}
				restoreTpl()
			}
		}
		if err := r.walkLayers(page.Content.Layer, order, pageOrigin, visitor); err != nil {
			return err
		}
	}
	if r.RenderAnnotations && r.Reader != nil {
		ctx := r.pageContext
		if ctx == nil {
			ctx = r.outputContext()
		}
		annotations, err := r.Reader.pageAnnotationsContext(ctx, page.ID)
		if err != nil {
			return err
		}
		for _, annotation := range annotations {
			if r.printing && annotation.Print != nil && !*annotation.Print {
				continue
			}
			if r.printing && (r.printContents == "document" && annotation.Subtype != "Widget" || r.printContents == "forms" && annotation.Subtype != "Widget" || r.printContents == "annotations" && annotation.Subtype == "Widget") {
				continue
			}
			if r.printing && annotation.Print == nil && annotation.Parameters != nil {
				for _, parameter := range annotation.Parameters.Parameter {
					if parameter.Name == "PDF.Printable" && (parameter.Value == "true" || parameter.Value == "false") {
						visible := parameter.Value == "true"
						annotation.Visible = &visible
						break
					}
				}
			}
			if annotation.Visible != nil && !*annotation.Visible && !pageIncludesHidden(visitor) {
				continue
			}
			if err := r.walkPageAnnotation(annotation, offsetX, offsetY, visitor); err != nil {
				return err
			}
		}
	}
	if r.printing && (r.printContents == "annotations" || r.printContents == "forms") {
		return nil
	}
	if r.Reader != nil {
		ctx := r.pageContext
		if ctx == nil {
			ctx = r.outputContext()
		}
		if err := r.Reader.ensurePageStampsContext(ctx, page.ID); err != nil {
			return err
		}
		for _, stamp := range r.Reader.Stamps[r.Reader.resolveStampPageID(page.ID)] {
			if r.pageContext != nil {
				if err := r.pageContext.Err(); err != nil {
					return err
				}
			}
			stamp.Box.X += offsetX
			stamp.Box.Y += offsetY
			if err := visitor.DrawStamp(stamp); err != nil {
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

// walkAnnotation 访问独立注解外观并保持分组、定位与外观边界裁剪
// 入参: annotation 注解, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) walkAnnotation(annotation Annotation, visitor PageVisitor) error {
	return r.walkPageAnnotation(annotation, 0, 0, visitor)
}

// walkPageAnnotation 按页面视口偏移访问注解外观并施加外观边界裁剪
// 入参: annotation 注解, offsetX 横向偏移, offsetY 纵向偏移, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) walkPageAnnotation(annotation Annotation, offsetX, offsetY float64, visitor PageVisitor) error {
	if groups, ok := visitor.(PageGroups); ok && groups.BeginAnnotation(annotation.ID) {
		defer groups.EndObject()
	}
	box, boxErr := ParseBox(annotation.Appearance.Boundary)
	matrix := TranslationMatrix(box.X+offsetX, box.Y+offsetY)
	var clip *GeometryPath
	if !pageSkipsClips(visitor) && strings.TrimSpace(annotation.Appearance.Boundary) != "" && boxErr == nil && box.W > 0 && box.H > 0 {
		var err error
		clip, err = r.geometryExtentClip(box.W, box.H, matrix, nil)
		if err != nil {
			return err
		}
	}
	state := RenderState{Parent: &matrix, BoundaryInCTM: true, Hidden: annotation.Visible != nil && !*annotation.Visible, Clip: clip}
	for object := range annotation.Appearance.objects() {
		if err := r.WalkObject(object, state, visitor); err != nil {
			return err
		}
	}
	return nil
}

// walkLayers 按原顺序访问指定层级，不重新排列同类图层
// 入参: layers 图层列表, order 层级顺序, pageOrigin 页面原点变换, visitor 页面访问器
// 返回: error 遍历或访问器错误
func (r *Renderer) walkLayers(layers []Layer, order int, pageOrigin *Matrix, visitor PageVisitor) error {
	for _, layer := range layers {
		if layerOrder(layer.Type) != order {
			continue
		}
		state := RenderState{Defaults: r.drawParamDefaults(layer.DrawParam, nil), Parent: pageOrigin}
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

// walkObject 访问对象并传递当前资源引用链
// 入参: object 源对象, state 继承状态, visitor 页面访问器, references 当前资源引用链
// 返回: error 遍历或访问器错误
func (r *Renderer) walkObject(object *GraphicObject, state RenderState, visitor PageVisitor, references map[*CompositeGraphicUnit]bool) error {
	if r.pageContext != nil {
		if err := r.pageContext.Err(); err != nil {
			return err
		}
	}
	if pageSkipsClips(visitor) {
		state.Clip = nil
	}
	if groups, ok := visitor.(PageGroups); ok && groups.BeginObject(object) {
		defer groups.EndObject()
	}
	switch object.Type {
	case "TextObject":
		state.Hidden = state.Hidden || object.TextObject.Visible != nil && !*object.TextObject.Visible
		return visitor.DrawObject(object, state)
	case "PathObject":
		state.Hidden = state.Hidden || object.PathObject.Visible != nil && !*object.PathObject.Visible
		return visitor.DrawObject(object, state)
	case "ImageObject":
		state.Hidden = state.Hidden || object.ImageObject.Visible != nil && !*object.ImageObject.Visible
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
func (r *Renderer) walkComposite(object CompositeGraphicUnit, state RenderState, visitor PageVisitor, references map[*CompositeGraphicUnit]bool) error {
	state.Hidden = state.Hidden || object.Visible != nil && !*object.Visible
	if state.Hidden && !pageIncludesHidden(visitor) {
		if groups, ok := visitor.(PageGroups); ok {
			count, ref, seen := len(object.Objects), object.ResourceID, make(map[*CompositeGraphicUnit]bool)
			for ref != "" {
				unit := r.compositeDefinition(ref)
				if unit == nil || seen[unit] {
					break
				}
				seen[unit] = true
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
	if pageSkipsClips(visitor) {
		state.Clip = nil
	} else {
		var err error
		if object.Boundary != "" {
			state.Clip, err = r.geometryExtentClip(box.W, box.H, boundary, state.Clip)
			if err != nil {
				return err
			}
		}
		state.Clip, err = r.compositeExtentClip(object, matrix, state.Clip)
		if err != nil {
			return err
		}
		clips, clipMatrix := object.Clips, matrix
		if !clips.usesObjectMatrix() {
			clipMatrix = boundary
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
	}
	state.Defaults = r.compositeGraphicUnitDefaults(object, state.Defaults)
	state.Parent = &matrix
	if composites, ok := visitor.(PageComposites); ok {
		if err := composites.VisitComposite(&object, state); err != nil {
			return err
		}
	}
	state.BoundaryInCTM = true
	useGroup := false
	if object.Alpha != nil {
		alpha := clampColor(*object.Alpha)
		if alpha < 255 && r.compositeObjectCount(object, references) > 1 {
			if tg, ok := visitor.(PageTransparencyGroups); ok && tg.BeginTransparencyGroup(uint8(alpha)) {
				useGroup = true
				defer tg.EndTransparencyGroup()
			} else if cv, ok := visitor.(*canvasPageVisitor); ok {
				if compiler, ok := cv.ctx.Renderer.(*canvasPageCompiler); ok {
					compiler.page.Commands = append(compiler.page.Commands, RasterCommand{Group: RasterGroupBegin, GroupAlpha: uint8(alpha)})
					useGroup = true
					defer func() {
						compiler.page.Commands = append(compiler.page.Commands, RasterCommand{Group: RasterGroupEnd})
					}()
				}
			}
		}
	}
	if object.ResourceID != "" {
		if ref := r.compositeDefinition(object.ResourceID); ref != nil {
			if references[ref] {
				return fmt.Errorf("cyclic composite resource %q", object.ResourceID)
			}
			if references == nil {
				references = make(map[*CompositeGraphicUnit]bool)
			}
			references[ref] = true
			copy := *ref
			if !useGroup {
				copy.Alpha = mergeAlpha(copy.Alpha, object.Alpha)
			}
			err := r.walkComposite(copy, state, visitor, references)
			delete(references, ref)
			if err != nil {
				return err
			}
		}
	}
	for member := range object.objects() {
		if !useGroup {
			member = mergeGraphicObjectAlpha(member, object.Alpha)
		}
		if err := r.walkObject(&member, state, visitor, references); err != nil {
			return err
		}
	}
	return nil
}

// compositeObjectCount 估算复合图元展开后的可见子图元数量，超过1即提前返回
// 入参: object 复合图元, references 当前资源引用链
// 返回: int 截断至2的子图元计数
func (r *Renderer) compositeObjectCount(object CompositeGraphicUnit, references map[*CompositeGraphicUnit]bool) int {
	count := 0
	var visit func(unit CompositeGraphicUnit, seen map[*CompositeGraphicUnit]bool)
	visit = func(unit CompositeGraphicUnit, seen map[*CompositeGraphicUnit]bool) {
		if count > 1 {
			return
		}
		if unit.ResourceID != "" {
			if ref := r.compositeDefinition(unit.ResourceID); ref != nil && !references[ref] && !seen[ref] {
				if seen == nil {
					seen = make(map[*CompositeGraphicUnit]bool)
				}
				seen[ref] = true
				visit(*ref, seen)
				delete(seen, ref)
				if count > 1 {
					return
				}
			}
		}
		for member := range unit.objects() {
			switch member.Type {
			case "CompositeGraphicUnit", "CompositeObject":
				visit(member.CompositeGraphicUnit, seen)
			default:
				count++
			}
			if count > 1 {
				return
			}
		}
	}
	visit(object, nil)
	return count
}

// compositeExtentClip 按矢量资源尺寸裁剪内容，资源裁剪随实例变换
// 入参: object 矢量资源, matrix 资源到页面矩阵, parent 父裁剪
// 返回: *GeometryPath 页面裁剪, error 尺寸或几何错误
func (r *Renderer) compositeExtentClip(object CompositeGraphicUnit, matrix Matrix, parent *GeometryPath) (*GeometryPath, error) {
	if !object.extentSet && object.Width == 0 && object.Height == 0 {
		return parent, nil
	}
	return r.geometryExtentClip(object.Width, object.Height, matrix, parent)
}

// geometryExtentClip 按给定坐标系限制宽高并与父裁剪求交
// 入参: width 宽度, height 高度, matrix 到页面矩阵, parent 父裁剪
// 返回: *GeometryPath 页面裁剪, error 尺寸或几何错误
func (r *Renderer) geometryExtentClip(width, height float64, matrix Matrix, parent *GeometryPath) (*GeometryPath, error) {
	if !finite(width) || !finite(height) || width < 0 || height < 0 {
		return nil, fmt.Errorf("invalid clipping dimensions")
	}
	if width == 0 || height == 0 {
		return new(GeometryPath), nil
	}
	geometry, err := r.Geometry()
	if err != nil {
		return nil, err
	}
	clip, err := geometry.Transform(GeometryPath(basegeom.Rectangle(Box{W: width, H: height})), matrix)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		clip, err = clipGeometry(geometry, clip, parent)
		if err != nil {
			return nil, err
		}
	}
	return &clip, nil
}

// pageIncludesHidden 查询访问器是否需要分析隐藏内容，不改变渲染器配置
// 入参: visitor 页面访问器
// 返回: bool 是否访问隐藏内容
func pageIncludesHidden(visitor PageVisitor) bool {
	analysis, ok := visitor.(PageAnalysis)
	return ok && analysis.IncludeHidden()
}

// pageSkipsClips 查询访问器是否只需资源和变换，不计算几何裁剪
// 入参: visitor 页面访问器
// 返回: bool 是否跳过裁剪
func pageSkipsClips(visitor PageVisitor) bool {
	analysis, ok := visitor.(PageAnalysis)
	return ok && analysis.SkipClips()
}
