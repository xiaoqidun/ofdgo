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
	"github.com/tdewolff/canvas"
)

// canvasPageVisitor 将公共页面遍历结果交给Canvas解释器
type canvasPageVisitor struct {
	renderer *Renderer
	ctx      *canvas.Context
	height   float64
	clip     *canvas.Path
}

// DrawObject 绘制叶子对象，转换页面坐标裁剪
// 入参: object 源对象, state 继承状态
// 返回: error 绘制错误
func (v *canvasPageVisitor) DrawObject(object *GraphicObject, state RenderState) error {
	if state.Hidden {
		return nil
	}
	clip, err := geometryToCanvasPath(state.Clip)
	if err != nil {
		return err
	}
	if clip != nil {
		clip = clip.Translate(0, v.height)
	}
	clip = v.renderer.intersectCachedClipPath(v.clip, clip)
	switch object.Type {
	case "TextObject":
		v.renderer.renderText(v.ctx, object.TextObject, v.height, state.Defaults, state.Parent, state.BoundaryInCTM, clip)
	case "PathObject":
		v.renderer.renderPath(v.ctx, object.PathObject, v.height, state.Defaults, state.Parent, state.BoundaryInCTM, clip)
	case "ImageObject":
		v.renderer.renderImage(v.ctx, object.ImageObject, v.height, state.Parent, state.BoundaryInCTM, clip)
	}
	return v.renderer.renderError
}

// DrawStamp 绘制签章外观
// 入参: stamp 签章
// 返回: error 绘制错误
func (v *canvasPageVisitor) DrawStamp(stamp Stamp) error {
	if groups, ok := v.ctx.Renderer.(interface {
		beginStamp(Stamp) bool
		endObject()
	}); ok && groups.beginStamp(stamp) {
		defer groups.endObject()
	}
	v.renderer.renderStamp(v.ctx, stamp, v.height, v.clip)
	return v.renderer.renderError
}

// BeginObject 开始可编辑对象分组
// 入参: object 源对象
// 返回: bool 是否建立分组
func (v *canvasPageVisitor) BeginObject(object *GraphicObject) bool {
	groups, ok := v.ctx.Renderer.(interface {
		beginObject(*GraphicObject) bool
		endObject()
	})
	return ok && groups.beginObject(object)
}

// BeginAnnotation 开始注解分组
// 入参: id 注解标识
// 返回: bool 是否建立分组
func (v *canvasPageVisitor) BeginAnnotation(id string) bool {
	groups, ok := v.ctx.Renderer.(interface {
		beginAnnotation(string) bool
		endObject()
	})
	return ok && groups.beginAnnotation(id)
}

// EndObject 结束对象或注解分组
func (v *canvasPageVisitor) EndObject() {
	v.ctx.Renderer.(interface{ endObject() }).endObject()
}

// SkipObjects 保留不可见对象的编号
// 入参: count 对象数量
func (v *canvasPageVisitor) SkipObjects(count int) {
	if groups, ok := v.ctx.Renderer.(interface{ skipObjects(int) }); ok {
		groups.skipObjects(count)
	}
}

// renderCanvasPage 渲染特定页面内容
// 入参: page 页面内容
// 返回: *canvas.Canvas 画布实例, error 错误信息
func (r *Renderer) renderCanvasPage(page *PageContent) (*canvas.Canvas, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	width, height := box.W, box.H
	c := canvas.New(width, height)
	ctx := canvas.NewContext(c)
	if err := r.renderCanvasPageToContext(ctx, page, !r.TransparentBackground); err != nil {
		return nil, err
	}
	return c, nil
}

// renderCanvasContext 渲染页面到默认解释器上下文
// 入参: ctx 画布上下文, page 页面内容
// 返回: error 错误信息
func (r *Renderer) renderCanvasContext(ctx *canvas.Context, page *PageContent) error {
	renderer := *r
	renderer.decodeImages = true
	return renderer.renderCanvasPageToContext(ctx, page, !r.TransparentBackground)
}

// renderCanvasPageToContext 渲染页面到指定上下文
// 入参: ctx 画布上下文, page 页面内容, drawBackground 是否绘制页面背景
// 返回: error 错误信息
func (r *Renderer) renderCanvasPageToContext(ctx *canvas.Context, page *PageContent, drawBackground bool) error {
	return r.renderCanvasPageClipped(ctx, page, drawBackground, nil)
}

// renderCanvasPageClipped 按可选画布裁剪渲染页面到指定上下文
// 入参: ctx 画布上下文, page 页面内容, drawBackground 是否绘制页面背景, clip 页面画布裁剪
// 返回: error 错误信息
func (r *Renderer) renderCanvasPageClipped(ctx *canvas.Context, page *PageContent, drawBackground bool, clip *canvas.Path) error {
	defer r.bindPageResources(page)()
	r.renderError = nil
	box, err := r.GetPageBox(page)
	if err != nil {
		return err
	}
	if r.OnPageText != nil {
		renderer := *r
		renderer.pageText = &PageText{}
		r = &renderer
	}
	pageH := box.H
	if drawBackground {
		ctx.SetFillColor(canvas.White)
		ctx.DrawPath(0, 0, applyClipPath(canvas.Rectangle(box.W, box.H), clip))
	}
	if err := r.WalkPage(page, &canvasPageVisitor{renderer: r, ctx: ctx, height: pageH, clip: clip}); err != nil {
		return err
	}
	if r.renderError != nil {
		return r.renderError
	}
	if r.OnPageText != nil {
		r.OnPageText(page, r.pageText)
	}
	return nil
}

// renderCompositeGraphicUnit 通过公共遍历绘制复合图元
// 入参: ctx 画布上下文, cgu 复合图元, pageH 页面高度, defaults 默认样式, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
func (r *Renderer) renderCompositeGraphicUnit(ctx *canvas.Context, cgu CompositeGraphicUnit, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if err := r.walkComposite(cgu, canvasRenderState(pageH, defaults, parentCTM, boundaryInCTM, parentClip), &canvasPageVisitor{renderer: r, ctx: ctx, height: pageH}, nil); err != nil {
		r.renderError = err
	}
}

// renderObject 通过公共遍历绘制图形对象
// 入参: ctx 画布上下文, obj 图形对象, pageH 页面高度, defaults 默认样式, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
func (r *Renderer) renderObject(ctx *canvas.Context, obj *GraphicObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if err := r.WalkObject(obj, canvasRenderState(pageH, defaults, parentCTM, boundaryInCTM, parentClip), &canvasPageVisitor{renderer: r, ctx: ctx, height: pageH}); err != nil {
		r.renderError = err
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
