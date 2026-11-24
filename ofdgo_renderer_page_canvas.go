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

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/svg"
)

// canvasPageVisitor 将公共页面遍历结果交给Canvas解释器
type canvasPageVisitor struct {
	renderer *Renderer
	ctx      *canvas.Context
	height   float64
	clip     *canvas.Path
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

// DrawObject 绘制叶子对象，转换页面坐标裁剪
// 入参: object 源对象, state 继承状态
// 返回: error 绘制错误
func (v *canvasPageVisitor) DrawObject(object *GraphicObject, state RenderState) error {
	if object.Type == "TextObject" {
		if pdfOut, isPDF := v.ctx.Renderer.(*pdfRenderer); isPDF {
			defer pdfOut.finishTextObject()
		}
	}
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

// DrawStamp 绘制签章外观，PDF后端下优先以独立Form直出OFD印章
// 入参: stamp 签章
// 返回: error 绘制错误
func (v *canvasPageVisitor) DrawStamp(stamp Stamp) error {
	if v.renderer.pageText == nil && !v.renderer.textOnly && stamp.Type == "ofd" && len(stamp.Data) > 0 && v.ctx.View() == canvas.Identity && v.ctx.CoordSystemView() == canvas.Identity {
		if pdfOut, isPDF := v.ctx.Renderer.(*pdfRenderer); isPDF && pdfOut.imageError == nil {
			return v.renderer.drawPDFOFDStamp(pdfOut, stamp, v.height, v.clip)
		}
	}
	if groups, ok := v.ctx.Renderer.(interface {
		beginStamp(Stamp) bool
		endObject()
	}); ok && groups.beginStamp(stamp) {
		defer groups.endObject()
	}
	v.renderer.renderStamp(v.ctx, stamp, v.height, v.clip)
	return v.renderer.renderError
}

// canCanvasTransparencyGroup 判断画布后端是否支持离屏透明组隔离
// 入参: renderer 目标画布渲染器
// 返回: bool 是否支持透明组
func canCanvasTransparencyGroup(renderer canvas.Renderer) bool {
	switch renderer.(type) {
	case *canvasPageCompiler, *pdfRenderer, *svgResourceRenderer:
		return true
	default:
		return false
	}
}

// beginCanvasTransparencyGroup 在支持的画布后端上开启离屏透明组隔离并返回关闭闭包
// 入参: renderer 目标画布渲染器, alpha 透明组整体不透明度
// 返回: func() 关闭闭包, bool 是否成功开启
func beginCanvasTransparencyGroup(renderer canvas.Renderer, alpha uint8) (func(), bool) {
	switch out := renderer.(type) {
	case *canvasPageCompiler:
		out.page.Commands = append(out.page.Commands, RasterCommand{Group: RasterGroupBegin, GroupAlpha: alpha})
		return func() {
			out.page.Commands = append(out.page.Commands, RasterCommand{Group: RasterGroupEnd})
		}, true
	case *pdfRenderer:
		if !out.beginTransparencyGroup(alpha) {
			return nil, false
		}
		return out.endTransparencyGroup, true
	case *svgResourceRenderer:
		if !out.beginTransparencyGroup(alpha) {
			return nil, false
		}
		return out.endTransparencyGroup, true
	default:
		return nil, false
	}
}

// BeginTransparencyGroup 为复合图元开启离屏透明组隔离
// 入参: alpha 透明组不透明度
// 返回: bool 是否开启了透明组
func (v *canvasPageVisitor) BeginTransparencyGroup(alpha uint8) bool {
	if compiler, ok := v.ctx.Renderer.(*canvasPageCompiler); ok {
		compiler.page.Commands = append(compiler.page.Commands, RasterCommand{Group: RasterGroupBegin, GroupAlpha: alpha})
		return true
	}
	if pdfOut, ok := v.ctx.Renderer.(*pdfRenderer); ok {
		return pdfOut.beginTransparencyGroup(alpha)
	}
	if svgOut, ok := v.ctx.Renderer.(*svgResourceRenderer); ok {
		return svgOut.beginTransparencyGroup(alpha)
	}
	return false
}

// EndTransparencyGroup 结束复合图元的离屏透明组隔离
func (v *canvasPageVisitor) EndTransparencyGroup() {
	if compiler, ok := v.ctx.Renderer.(*canvasPageCompiler); ok {
		compiler.page.Commands = append(compiler.page.Commands, RasterCommand{Group: RasterGroupEnd})
		return
	}
	if pdfOut, ok := v.ctx.Renderer.(*pdfRenderer); ok {
		pdfOut.endTransparencyGroup()
		return
	}
	if svgOut, ok := v.ctx.Renderer.(*svgResourceRenderer); ok {
		svgOut.endTransparencyGroup()
	}
}

// renderTemplatePage 在无外部裁剪依赖时复用PDF表单、光栅指令或SVG片段模板缓存
// 入参: template 模板页内容, offsetX 页面视口横偏移, offsetY 页面视口纵偏移, pageOrigin 页面原点变换
// 返回: bool 是否已由缓存路径完成渲染, error 渲染错误
func (v *canvasPageVisitor) renderTemplatePage(template *PageContent, offsetX, offsetY float64, pageOrigin *Matrix) (bool, error) {
	r := v.renderer
	if r.pageText != nil || r.textOnly || v.clip != nil || v.ctx.View() != canvas.Identity || v.ctx.CoordSystemView() != canvas.Identity {
		return false, nil
	}
	if pdfOut, isPDF := v.ctx.Renderer.(*pdfRenderer); isPDF && pdfOut.imageError == nil {
		return true, r.drawPDFTemplatePage(pdfOut, template, offsetX, offsetY, pageOrigin, v.height)
	}
	if compiler, isCompiler := v.ctx.Renderer.(*canvasPageCompiler); isCompiler && compiler.err == nil && canvasNativeStroke(compiler.geometry) {
		key := templateRasterCacheKey{
			template:      template,
			width:         compiler.page.Width,
			height:        compiler.page.Height,
			offsetX:       offsetX,
			offsetY:       offsetY,
			dpi:           r.dpi,
			printing:      r.printing,
			printContents: r.printContents,
			decodeImages:  r.decodeImages,
		}
		cache := r.canvasTemplateCommands()
		if cached, hit := cache.get(key); hit {
			compiler.page.Commands = append(compiler.page.Commands, cached...)
			return true, nil
		}
		subPage := &RasterPage{Width: compiler.page.Width, Height: compiler.page.Height, DPI: compiler.page.DPI}
		subCompiler := &canvasPageCompiler{page: subPage, geometry: compiler.geometry}
		subVisitor := &canvasPageVisitor{renderer: r, ctx: canvas.NewContext(subCompiler), height: v.height}
		restoreTpl := r.bindParsedResources(template.pageResources)
		for templateOrder := range 3 {
			if err := r.walkLayers(template.Content.Layer, templateOrder, pageOrigin, subVisitor); err != nil {
				restoreTpl()
				return true, err
			}
		}
		restoreTpl()
		if subCompiler.err != nil {
			compiler.err = subCompiler.err
			return true, subCompiler.err
		}
		if r.renderError != nil {
			return true, r.renderError
		}
		if cost := rasterCommandsCost(subPage.Commands, cache.limit); cost <= cache.limit {
			cache.put(key, subPage.Commands, cost)
		}
		compiler.page.Commands = append(compiler.page.Commands, subPage.Commands...)
		return true, nil
	}
	if svgOut, isSVG := v.ctx.Renderer.(*svgResourceRenderer); isSVG && !svgOut.embeddedFonts && len(svgOut.objectStack) == 0 && svgOut.err == nil {
		w, h := svgOut.Size()
		prec, _ := svgOut.effectivePrecision()
		key := svgTemplateKey{
			template:       template,
			width:          w,
			height:         h,
			offsetX:        offsetX,
			offsetY:        offsetY,
			dpi:            r.dpi,
			precision:      prec,
			pathMerge:      svgOut.pathMerge,
			printing:       r.printing,
			printContents:  r.printContents,
			externalImages: svgOut.imageNames != nil,
			compression:    r.compression,
		}
		cache := r.svgTemplateFragments()
		if cached, hit := cache.get(key); hit {
			if cached != svgNonReusableTemplate {
				return true, svgOut.appendTemplateFragment(cached)
			}
			return false, nil
		}
		var buf bytes.Buffer
		options := svg.DefaultOptions
		options.EmbedFonts = false
		prevKnown, prevEdges := r.knownSVGResources, r.transparentEdges
		r.knownSVGResources, r.transparentEdges = nil, nil
		sub := &svgResourceRenderer{
			SVG:          svg.New(&buf, w, h, &options),
			renderer:     r,
			writer:       &buf,
			seen:         make(map[string]bool),
			collectClips: true,
			pathMerge:    svgOut.pathMerge,
			precision:    prec,
		}
		if svgOut.imageNames != nil {
			sub.imageNames = make(map[svgImageKey]string)
		}
		buf.Reset()
		subVisitor := &canvasPageVisitor{renderer: r, ctx: canvas.NewContext(sub), height: v.height}
		restoreTpl := r.bindParsedResources(template.pageResources)
		var walkErr error
		for templateOrder := range 3 {
			if walkErr = r.walkLayers(template.Content.Layer, templateOrder, pageOrigin, subVisitor); walkErr != nil {
				break
			}
		}
		restoreTpl()
		r.knownSVGResources, r.transparentEdges = prevKnown, prevEdges
		if walkErr != nil {
			return true, walkErr
		}
		sub.flushPendingPath()
		if sub.err != nil {
			svgOut.err = sub.err
			return true, sub.err
		}
		if r.renderError != nil {
			return true, r.renderError
		}
		if !sub.hasSVGDefs {
			frag := &svgTemplateFragment{
				markup:   bytes.Clone(buf.Bytes()),
				fonts:    sub.fonts,
				images:   sub.images,
				clipDefs: sub.clipDefs,
			}
			cost := len(frag.markup) + 256
			for _, img := range frag.images {
				cost += len(img.Data) + 64
			}
			for _, def := range frag.clipDefs {
				cost += len(def.path) + 64
			}
			if cost <= cache.limit {
				cache.put(key, frag, cost)
			}
			return true, svgOut.appendTemplateFragment(frag)
		}
		if 64 <= cache.limit {
			cache.put(key, svgNonReusableTemplate, 64)
		}
	}
	return false, nil
}

// renderCompositeForm 在PDF后端下将复合图元引用直接输出为Form对象
// 入参: ref 复合图元定义, object 复合图元实例, matrix 变换矩阵, clip 页面裁剪, references 当前资源引用链
// 返回: bool 是否已由Form路径完成输出, error 输出错误
func (v *canvasPageVisitor) renderCompositeForm(ref *CompositeGraphicUnit, object CompositeObject, matrix Matrix, clip *GeometryPath, references map[*CompositeGraphicUnit]bool) (bool, error) {
	if v.ctx.View() != canvas.Identity || v.ctx.CoordSystemView() != canvas.Identity {
		return false, nil
	}
	pdfOut, isPDF := v.ctx.Renderer.(*pdfRenderer)
	if !isPDF || pdfOut.imageError != nil {
		return false, nil
	}
	if matrix != IdentityMatrix() && v.renderer.compositeUsesPagePattern(ref, references) {
		return false, nil
	}
	alphaByte := uint8(255)
	if object.Alpha != nil {
		alphaByte = uint8(clampColor(*object.Alpha))
	}
	needGroup := alphaByte < 255
	return true, v.renderer.drawPDFCompositeForm(pdfOut, ref, matrix, clip, v.clip, v.height, alphaByte, needGroup, references)
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
	box, err := r.GetPageBoxContext(r.pageContext, page)
	if err != nil {
		return nil, err
	}
	width, height := box.W, box.H
	c := canvas.New(width, height)
	ctx := canvas.NewContext(c)
	if err := r.renderCanvasPageToContext(ctx, page, !r.transparentBackground); err != nil {
		return nil, err
	}
	return c, nil
}

// renderCanvasContext 渲染页面到默认解释器上下文
// 入参: ctx 画布上下文, page 页面内容
// 返回: error 错误信息
func (r *Renderer) renderCanvasContext(ctx *canvas.Context, page *PageContent) error {
	prevDecode := r.decodeImages
	r.decodeImages = true
	defer func() { r.decodeImages = prevDecode }()
	return r.renderCanvasPageToContext(ctx, page, !r.transparentBackground)
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
	box, err := r.GetPageBoxContext(r.pageContext, page)
	if err != nil {
		return err
	}
	if r.OnPageText != nil {
		prevPageText := r.pageText
		r.pageText = &PageText{}
		defer func() { r.pageText = prevPageText }()
	}
	pageH := box.H
	if drawBackground {
		ctx.SetFillColor(canvas.White)
		ctx.DrawPath(0, 0, applyClipPath(canvas.Rectangle(box.W, box.H), clip))
	}
	if err := r.WalkPageContext(r.pageContext, page, &canvasPageVisitor{renderer: r, ctx: ctx, height: pageH, clip: clip}); err != nil {
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

// renderCompositeObject 通过公共遍历绘制复合图元对象
// 入参: ctx 画布上下文, obj 复合图元对象, pageH 页面高度, defaults 默认样式, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
func (r *Renderer) renderCompositeObject(ctx *canvas.Context, obj CompositeObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if err := r.walkComposite(obj, canvasRenderState(pageH, defaults, parentCTM, boundaryInCTM, parentClip), &canvasPageVisitor{renderer: r, ctx: ctx, height: pageH}, nil); err != nil {
		r.renderError = err
	}
}

// renderObject 通过公共遍历绘制图形对象
// 入参: ctx 画布上下文, obj 图形对象, pageH 页面高度, defaults 默认样式, parentCTM 父变换, boundaryInCTM 边界是否参与变换, parentClip 父裁剪
func (r *Renderer) renderObject(ctx *canvas.Context, obj *GraphicObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if err := r.WalkObjectContext(r.pageContext, obj, canvasRenderState(pageH, defaults, parentCTM, boundaryInCTM, parentClip), &canvasPageVisitor{renderer: r, ctx: ctx, height: pageH}); err != nil {
		r.renderError = err
	}
}
