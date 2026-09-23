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

import "github.com/tdewolff/canvas"

// canvasPageVisitor 将公共页面遍历结果交给Canvas解释器
type canvasPageVisitor struct {
	renderer *Renderer
	context  *canvas.Context
	height   float64
}

// DrawObject 绘制叶子对象，转换页面坐标裁剪
// 入参: object 源对象, state 继承状态
// 返回: error 绘制错误
func (v *canvasPageVisitor) DrawObject(object *GraphicObject, state RenderState) error {
	clip, err := geometryToCanvasPath(state.Clip)
	if err != nil {
		return err
	}
	if clip != nil {
		clip = clip.Translate(0, v.height)
	}
	switch object.Type {
	case "TextObject":
		v.renderer.renderText(v.context, object.TextObject, v.height, state.Defaults, state.Parent, state.BoundaryInCTM, clip)
	case "PathObject":
		v.renderer.renderPath(v.context, object.PathObject, v.height, state.Defaults, state.Parent, state.BoundaryInCTM, clip)
	case "ImageObject":
		v.renderer.renderImage(v.context, object.ImageObject, v.height, state.Parent, state.BoundaryInCTM, clip)
	}
	return v.renderer.renderError
}

// DrawStamp 绘制签章外观
// 入参: stamp 签章
// 返回: error 绘制错误
func (v *canvasPageVisitor) DrawStamp(stamp Stamp) error {
	v.renderer.renderStamp(v.context, stamp, v.height)
	return v.renderer.renderError
}

// BeginObject 开始可编辑对象分组
// 入参: object 源对象
// 返回: bool 是否建立分组
func (v *canvasPageVisitor) BeginObject(object *GraphicObject) bool {
	groups, ok := v.context.Renderer.(interface {
		beginObject(*GraphicObject) bool
		endObject()
	})
	return ok && groups.beginObject(object)
}

// BeginAnnotation 开始注解分组
// 入参: id 注解标识
// 返回: bool 是否建立分组
func (v *canvasPageVisitor) BeginAnnotation(id string) bool {
	groups, ok := v.context.Renderer.(interface {
		beginAnnotation(string) bool
		endObject()
	})
	return ok && groups.beginAnnotation(id)
}

// EndObject 结束对象或注解分组
func (v *canvasPageVisitor) EndObject() {
	v.context.Renderer.(interface{ endObject() }).endObject()
}

// SkipObjects 保留不可见对象的编号
// 入参: count 对象数量
func (v *canvasPageVisitor) SkipObjects(count int) {
	if groups, ok := v.context.Renderer.(interface{ skipObjects(int) }); ok {
		groups.skipObjects(count)
	}
}

// canvasRenderState 将Canvas裁剪转换为公共页面状态
// 入参: pageH 页面高度, defaults 默认样式, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
// 返回: RenderState 公共页面状态
func canvasRenderState(pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) RenderState {
	var clip *GeometryPath
	if parentClip != nil {
		clip = geometryFromCanvasPath(parentClip.Copy().Translate(0, -pageH))
	}
	return RenderState{Defaults: defaults, Parent: parentCTM, BoundaryInCTM: boundaryInCTM, Clip: clip}
}

// renderCompositeGraphicUnit 通过公共遍历绘制复合图元
// 入参: ctx 画布上下文, cgu 复合图元, pageH 页面高度, defaults 默认样式, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
func (r *Renderer) renderCompositeGraphicUnit(ctx *canvas.Context, cgu CompositeGraphicUnit, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if err := r.walkComposite(cgu, canvasRenderState(pageH, defaults, parentCTM, boundaryInCTM, parentClip), &canvasPageVisitor{r, ctx, pageH}, nil); err != nil {
		r.renderError = err
	}
}

// renderObject 通过公共遍历绘制图形对象
// 入参: ctx 画布上下文, obj 图形对象, pageH 页面高度, defaults 默认样式, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
func (r *Renderer) renderObject(ctx *canvas.Context, obj *GraphicObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if err := r.WalkObject(obj, canvasRenderState(pageH, defaults, parentCTM, boundaryInCTM, parentClip), &canvasPageVisitor{r, ctx, pageH}); err != nil {
		r.renderError = err
	}
}
