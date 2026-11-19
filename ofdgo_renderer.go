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
	"context"
	"image"
	"io/fs"
	"maps"
	"slices"
)

// Renderer 渲染器实现
// 通过NewRenderer创建，实例及共享的Reader需串行使用
// Fork创建独立任务实例，派生实例之间可并行渲染
// 外部字体缺失且无可用回退时不绘制对应文字，其余内容继续；字体状态可通过FontInfos查询
// OnPageText可选，接收页面绘制时同步提取的文字，不包含图案和签名外观
// OnExportProgress可选，同步回报文档导出的已处理页数及总页数，返回错误则停止，页数完成不代表写入成功
// TransparentBackground关闭页面白底，供嵌套图案和印章保持透明背景
// Compression仅控制导出编码，OutputContext用于导出和文字提取的取消，nil使用后台上下文
type Renderer struct {
	Reader                *Reader
	DPI                   float64
	Compression           CompressionOptions
	OutputContext         context.Context
	RenderAnnotations     bool
	OnPageText            func(*PageContent, *PageText)
	OnExportProgress      func(completed, total int) error
	DrawParams            map[string]*DrawParam
	CompositeGraphicUnits map[string]*CompositeGraphicUnit
	hasBaseScope          bool
	scopedDrawParams      map[string]*DrawParam
	scopedComposites      map[string]*CompositeGraphicUnit
	pageResources         []*parsedRes
	templatePageCache     map[string]*PageContent
	imageCache            *renderCache[string, image.Image]
	fontDirs              []string
	fontFS                []fs.FS
	decodeImages          bool
	pageText              *PageText
	pageContext           context.Context
	textOnly              bool
	printing              bool
	compactStrokes        bool
	printContents         string
	textContentOnly       bool
	knownSVGResources     map[string]bool
	backends              RenderBackends
	rasterDisabled        bool
	backendStates         map[any]any
	sharedBackendStates   map[any]any
	resolvedFonts         map[resolvedFontKey]resolvedFontResult
	preparedFonts         map[string]*PreparedFont
	fontDigests           *renderCache[fontDataKey, [32]byte]
	fontPreparations      *renderCache[[32]byte, PreparedFont]
	fontSourcesCache      *fontSourceCache
	fontMetrics           *renderCache[[32]byte, FontMetrics]
	glyphOutlines         *renderCache[glyphOutlineKey, GeometryPath]
	renderError           error
	patternReferences     map[*Pattern]bool
	patternPage           *Matrix
	TransparentBackground bool
}

// RendererOption 渲染器配置选项
type RendererOption func(*Renderer)

// Fork 创建独立渲染实例，复用只读资源及线程安全的派生数据缓存
// 创建过程需与原实例串行，输入生命周期遵循Reader.Fork，外部后端及字体文件系统需支持并行读取
// 页面文字及导出进度回调不继承，取消上下文使用ctx
// 入参: ctx 取消上下文
// 返回: *Renderer 独立渲染器, error 准备或取消错误
func (r *Renderer) Fork(ctx context.Context) (*Renderer, error) {
	reader, err := r.Reader.Fork(ctx)
	if err != nil {
		return nil, err
	}
	drawParams := r.DrawParams
	composites := r.CompositeGraphicUnits
	if r.hasBaseScope {
		drawParams = r.scopedDrawParams
		composites = r.scopedComposites
	}
	maps.Copy(reader.drawParamCache, drawParams)
	maps.Copy(reader.compositeGraphicUnitCache, composites)
	next := NewRenderer(reader, WithDPI(r.DPI), WithAnnotations(r.RenderAnnotations), WithRenderBackends(r.backends), WithFontDirs(r.fontDirs...), WithFontFS(r.fontFS...))
	next.rasterDisabled = r.rasterDisabled
	next.Compression = r.Compression
	next.OutputContext = ctx
	next.TransparentBackground = r.TransparentBackground
	next.decodeImages = r.decodeImages
	next.printing = r.printing
	next.printContents = r.printContents
	next.imageCache = r.imageCache
	next.fontDigests = r.fontDigests
	next.fontPreparations = r.fontPreparations
	next.fontMetrics = r.fontMetrics
	next.glyphOutlines = r.glyphOutlines
	if r.fontSourcesCache != nil && next.fontSourcesCache != nil {
		next.fontSourcesCache.collections = r.fontSourcesCache.collections
		next.fontSourcesCache.shared = r.fontSourcesCache.shared
	}
	maps.Copy(next.sharedBackendStates, r.sharedBackendStates)
	seenForkers := make(map[any]bool)
	for _, candidate := range []any{r.backends.Compiler, r.backends.Raster, r.backends.Fonts, r.backends.Geometry, r.backends.SVG, r.backends.PDF, r.backends.EPS} {
		if candidate == nil || seenForkers[candidate] {
			continue
		}
		seenForkers[candidate] = true
		if forker, ok := candidate.(sharedStateForker); ok {
			forker.forkSharedState(r, next)
		}
	}
	return next, nil
}

// ClearCache 释放派生字体、图片、模板与后端缓存，不改变文档和渲染配置
func (r *Renderer) ClearCache() {
	r.imageCache.clear()
	r.backendStates = make(map[any]any)
	r.imageCache = resourceCache[string, image.Image](r.Reader.resources, imageCacheLimit)
	r.templatePageCache = make(map[string]*PageContent)
	r.resetFontCache()
}

// SetFontFS 替换外部字体文件系统并重置字体缓存
// 入参: fsys 字体文件系统
func (r *Renderer) SetFontFS(fsys ...fs.FS) {
	r.fontFS = append([]fs.FS(nil), fsys...)
	r.resetFontCache()
}

// FontSources 返回外部字体配置副本，供自定义编译器和导出后端读取
// 内嵌字体通过Reader.FontData读取，系统字体由各实现自行匹配
// 返回: []string 字体目录, []fs.FS 字体文件系统
func (r *Renderer) FontSources() ([]string, []fs.FS) {
	return append([]string(nil), r.fontDirs...), append([]fs.FS(nil), r.fontFS...)
}

// BindPageResources 解析并绑定指定页面的局部资源作用域，返回恢复闭包
// 入参: page 页面内容
// 返回: func() 恢复之前的作用域状态
func (r *Renderer) BindPageResources(page *PageContent) func() {
	return r.bindPageResources(page)
}

// sharedStateForker 为多后端提供并发Fork时的线程安全共享缓存继承钩子
type sharedStateForker interface {
	forkSharedState(src, dst *Renderer)
}

// bindPageResources 解析并绑定当前页面的局部资源作用域，返回恢复闭包
// 入参: page 页面内容
// 返回: func() 恢复之前的作用域状态
func (r *Renderer) bindPageResources(page *PageContent) func() {
	if page == nil {
		return r.bindParsedResources(nil)
	}
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	if r.Reader != nil && r.Reader.doc == nil && r.Reader.OFD != nil {
		_, _ = r.Reader.docStructureContext(ctx)
	}
	res := page.pageResources
	if res == nil && len(page.PageRes) > 0 && r.Reader != nil {
		basePath := ""
		if r.Reader.doc != nil && page.ID != "" {
			for _, ref := range r.Reader.doc.Pages.Page {
				if sameResourceID(ref.ID, page.ID) {
					basePath = r.Reader.ResPath(ref.BaseLoc)
					break
				}
			}
		}
		if basePath == "" {
			basePath = r.Reader.ResPath("Document.xml")
		}
		if collected, err := r.Reader.collectPageResourcesContext(ctx, basePath, page); err == nil {
			res = collected
		}
	}
	return r.bindParsedResources(res)
}

// bindParsedResources 绑定指定的局部资源列表并同步局部绘制参数与复合图元作用域
// 入参: res 局部资源表列表
// 返回: func() 恢复之前的作用域状态
func (r *Renderer) bindParsedResources(res []*parsedRes) func() {
	prevRes := r.pageResources
	prevDrawParams := r.DrawParams
	prevComposites := r.CompositeGraphicUnits
	prevHasBase := r.hasBaseScope
	prevBaseDP := r.scopedDrawParams
	prevBaseComp := r.scopedComposites
	if !r.hasBaseScope {
		r.hasBaseScope = true
		r.scopedDrawParams = prevDrawParams
		r.scopedComposites = prevComposites
	}
	r.pageResources = res
	r.DrawParams = r.mergeScopedDrawParams(r.scopedDrawParams, res)
	r.CompositeGraphicUnits = r.mergeScopedComposites(r.scopedComposites, res)
	return func() {
		r.pageResources = prevRes
		r.DrawParams = prevDrawParams
		r.CompositeGraphicUnits = prevComposites
		r.hasBaseScope = prevHasBase
		r.scopedDrawParams = prevBaseDP
		r.scopedComposites = prevBaseComp
	}
}

// mergeScopedDrawParams 基于外部初始覆盖与文档资源合并当前页面局部绘制参数
// 入参: base 外部基准绘制参数表, res 当前页面局部资源
// 返回: map[string]*DrawParam 当前作用域绘制参数表
func (r *Renderer) mergeScopedDrawParams(base map[string]*DrawParam, res []*parsedRes) map[string]*DrawParam {
	locals := make([]map[string]*DrawParam, 0, len(res))
	for _, item := range res {
		if item != nil && len(item.drawParams.exact) > 0 {
			locals = append(locals, item.drawParams.exact)
		}
	}
	if len(locals) == 0 {
		return base
	}
	var docIndex *resourceIndex[*DrawParam]
	var docFallback map[string]*DrawParam
	if r.Reader != nil {
		docIndex = r.Reader.docDrawParams
		docFallback = r.Reader.drawParamCache
	}
	return mergeScopedResources(base, docIndex, docFallback, r.Reader != nil, locals)
}

// mergeScopedComposites 基于外部初始覆盖与文档资源合并当前页面局部复合图元
// 入参: base 外部基准复合图元表, res 当前页面局部资源
// 返回: map[string]*CompositeGraphicUnit 当前作用域复合图元表
func (r *Renderer) mergeScopedComposites(base map[string]*CompositeGraphicUnit, res []*parsedRes) map[string]*CompositeGraphicUnit {
	locals := make([]map[string]*CompositeGraphicUnit, 0, len(res))
	for _, item := range res {
		if item != nil && len(item.composites.exact) > 0 {
			locals = append(locals, item.composites.exact)
		}
	}
	if len(locals) == 0 {
		return base
	}
	var docIndex *resourceIndex[*CompositeGraphicUnit]
	var docFallback map[string]*CompositeGraphicUnit
	if r.Reader != nil {
		docIndex = r.Reader.docComposites
		docFallback = r.Reader.compositeGraphicUnitCache
	}
	return mergeScopedResources(base, docIndex, docFallback, r.Reader != nil, locals)
}

// hasCompositeOverride 判断外部基准表是否显式注入了覆盖文档定义的复合图元
// 入参: base 外部基准表, id 资源标识
// 返回: bool 是否存在外部显式覆盖
func (r *Renderer) hasCompositeOverride(base map[string]*CompositeGraphicUnit, id string) bool {
	existing, ok := resourceValue(base, id)
	if !ok || existing == nil {
		return false
	}
	if r.Reader == nil {
		return true
	}
	var docVal *CompositeGraphicUnit
	if r.Reader.docComposites != nil {
		docVal, _ = r.Reader.docComposites.get(id)
	} else {
		docVal, _ = resourceValue(r.Reader.compositeGraphicUnitCache, id)
	}
	return existing != docVal
}

// fontDefinition 按页面局部资源优先查找字体定义
// 入参: id 字体资源标识
// 返回: *Font 字体定义
func (r *Renderer) fontDefinition(id string) *Font {
	if r.Reader == nil {
		return nil
	}
	if font, ok := r.Reader.resolveFontScoped(r.pageResources, id); ok {
		return font
	}
	return r.Reader.fontDefinition(id)
}

// compositeDefinition 按外部覆盖与页面局部资源优先查找复合图元定义
// 入参: id 复合图元资源标识
// 返回: *CompositeGraphicUnit 复合图元定义
func (r *Renderer) compositeDefinition(id string) *CompositeGraphicUnit {
	base := r.CompositeGraphicUnits
	if r.hasBaseScope {
		base = r.scopedComposites
	}
	if r.hasCompositeOverride(base, id) {
		unit, _ := resourceValue(base, id)
		return unit
	}
	if r.Reader != nil {
		if scoped, found := r.Reader.resolveCompositeScoped(r.pageResources, id); found {
			return scoped
		}
	}
	unit, _ := resourceValue(r.CompositeGraphicUnits, id)
	return unit
}

// deviceLineWidth 按设备分辨率计算描边宽度，零线宽为一像素，正线宽至少为两像素
// 入参: width 页面空间线宽，单位为毫米
// 返回: float64 设备描边宽度，单位为毫米
func (r *Renderer) deviceLineWidth(width float64) float64 {
	pixel := 25.4 / r.DPI
	if width == 0 {
		return pixel
	}
	if width > 0 {
		return max(width, 2*pixel)
	}
	return width
}

// childRenderer 继承配置并复用字体文件索引、候选与数据，文档解析与回退状态保持独立
// 入参: reader 子阅读器
// 返回: *Renderer 子渲染器
func (r *Renderer) childRenderer(reader *Reader) *Renderer {
	opts := []RendererOption{
		WithDPI(r.DPI),
		WithAnnotations(r.RenderAnnotations),
		WithRenderBackends(r.backends),
	}
	if len(r.fontDirs) > 0 {
		opts = append(opts, WithFontDirs(r.fontDirs...))
	}
	if len(r.fontFS) > 0 {
		opts = append(opts, WithFontFS(r.fontFS...))
	}
	renderer := NewRenderer(reader, opts...)
	renderer.rasterDisabled = r.rasterDisabled
	renderer.printing = r.printing
	renderer.sharedBackendStates = r.sharedBackendStates
	renderer.fontPreparations = r.fontPreparations
	renderer.fontDigests = r.fontDigests
	renderer.fontMetrics = r.fontMetrics
	renderer.glyphOutlines = r.glyphOutlines
	renderer.fontSourcesCache = r.fontSourcesCache.child()
	renderer.decodeImages = r.decodeImages
	renderer.pageContext = r.pageContext
	renderer.TransparentBackground = r.TransparentBackground
	return renderer
}

// loadTemplate 按标识数值加载模板页面，等价引用共用解析结果
// 入参: templateID 模板ID
// 返回: *PageContent 模板页面
func (r *Renderer) loadTemplate(templateID string) *PageContent {
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	if key := editorResourceID(templateID); key != "" {
		templateID = key
	}
	if page := r.templatePageCache[templateID]; page != nil {
		return page
	}
	for _, tpl := range r.Reader.doc.CommonData.TemplatePage {
		if !sameResourceID(tpl.ID, templateID) {
			continue
		}
		page, err := r.Reader.readPageContent(ctx, Page{BaseLoc: tpl.BaseLoc}, false, true)
		if err != nil {
			return nil
		}
		r.templatePageCache[templateID] = page
		return page
	}
	return nil
}

// templateOrder 获取页面模板引用的绘制顺序，省略时继承文档模板页声明并回退背景层
// 入参: ref 页面模板引用
// 返回: int 绘制顺序
func (r *Renderer) templateOrder(ref Template) int {
	kind := ref.ZOrder
	if kind == "" && r.Reader.doc != nil {
		for _, tpl := range r.Reader.doc.CommonData.TemplatePage {
			if sameResourceID(tpl.ID, ref.TemplateID) {
				kind = tpl.ZOrder
				break
			}
		}
	}
	if kind == "" {
		kind = "Background"
	}
	return layerOrder(kind)
}

// internalRasterBackend 获取矢量导出内部局部采样可用的光栅后端
// 返回: RasterBackend 光栅后端，未配置且非仅禁用图像输出时为nil
func (r *Renderer) internalRasterBackend() RasterBackend {
	if r.backends.Raster != nil {
		return r.backends.Raster
	}
	if r.rasterDisabled {
		return CanvasBackend{}
	}
	return nil
}

// mergeScopedResources 按常数时间索引将页面局部资源合并至基准表，保留外部显式覆盖
// 入参: base 基准资源表, docIndex 文档级资源索引, docFallback 文档级回退表, hasReader 是否绑定阅读器, locals 各局部资源文件的精确表
// 返回: map[string]T 合并后的作用域资源表
func mergeScopedResources[T comparable](base map[string]T, docIndex *resourceIndex[T], docFallback map[string]T, hasReader bool, locals []map[string]T) map[string]T {
	var zero T
	totalLocal := 0
	for _, exact := range locals {
		totalLocal += len(exact)
	}
	scoped := make(map[string]T, len(base)+totalLocal)
	var baseIdx resourceIndex[T]
	normKeys := make(map[string][]string, len(base))
	for key, val := range base {
		scoped[key] = val
		baseIdx.put(key, val)
		if norm := editorResourceID(key); norm != "" {
			normKeys[norm] = append(normKeys[norm], key)
		}
	}
	var fallbackIdx *resourceIndex[T]
	for _, exact := range locals {
		for _, id := range slices.Sorted(maps.Keys(exact)) {
			val := exact[id]
			if existing, ok := baseIdx.get(id); ok && existing != zero {
				if !hasReader {
					continue
				}
				var docVal T
				if docIndex != nil {
					docVal, _ = docIndex.get(id)
				} else if len(docFallback) != 0 {
					if fallbackIdx == nil {
						fallbackIdx = &resourceIndex[T]{}
						for fk, fv := range docFallback {
							fallbackIdx.put(fk, fv)
						}
					}
					docVal, _ = fallbackIdx.get(id)
				}
				if existing != docVal {
					continue
				}
			}
			if norm := editorResourceID(id); norm != "" {
				for _, oldKey := range normKeys[norm] {
					if oldKey != id {
						delete(scoped, oldKey)
					}
				}
				normKeys[norm] = []string{id}
			}
			scoped[id] = val
		}
	}
	return scoped
}

// layerOrder 获取图层类型的绘制顺序
// 入参: kind 图层类型
// 返回: int 绘制顺序
func layerOrder(kind string) int {
	switch kind {
	case "Background":
		return 0
	case "Foreground":
		return 2
	}
	return 1
}
