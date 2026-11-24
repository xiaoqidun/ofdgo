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
	"fmt"
	"image"
	"io/fs"
	"maps"
	"reflect"
	"slices"
)

// Renderer 渲染器实现
// 通过NewRenderer创建，实例及共享的Reader需串行使用
// Fork创建独立任务实例，派生实例之间可并行渲染
// 外部字体缺失且无可用回退时不绘制对应文字，其余内容继续；字体状态可通过FontInfos查询
// OnPageText可选，接收页面绘制时同步提取的文字，不包含图案和签名外观
// OnExportProgress可选，同步回报文档导出的已处理页数及总页数，返回错误则停止，页数完成不代表写入成功
type Renderer struct {
	reader                *Reader
	ownsReader            bool
	dpi                   float64
	compression           CompressionOptions
	renderAnnotations     bool
	OnPageText            func(*PageContent, *PageText)
	OnExportProgress      func(completed, total int) error
	drawParams            map[string]*DrawParam
	compositeGraphicUnits map[string]*CompositeGraphicUnit
	hasBaseScope          bool
	scopedDrawParams      map[string]*DrawParam
	scopedComposites      map[string]*CompositeGraphicUnit
	pageResources         []*parsedRes
	templatePageCache     map[string]*PageContent
	templatePages         *renderCache[string, *PageContent]
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
	svgIDPrefix           string
	svgPrecision          int
	svgPathMerge          SVGPathMergeOptions
	backends              RenderBackends
	rasterDisabled        bool
	backendStates         map[any]any
	sharedBackendStates   map[any]any
	resolvedFonts         map[resolvedFontKey]resolvedFontResult
	preparedFonts         map[string]*PreparedFont
	fontPreparations      *renderCache[[32]byte, PreparedFont]
	fontSourcesCache      *fontSourceCache
	fontMetrics           *renderCache[[32]byte, FontMetrics]
	glyphOutlines         *renderCache[glyphOutlineKey, GeometryPath]
	renderError           error
	patternReferences     map[*Pattern]bool
	patternPage           *Matrix
	transparentEdges      map[image.Image]transparentEdgeResult
	transparentBackground bool
}

// RendererOption 渲染器配置选项
type RendererOption func(*Renderer)

// sharedStateForker 为多后端提供并发Fork时的线程安全共享缓存继承钩子
type sharedStateForker interface {
	forkSharedState(src, dst *Renderer)
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

// sameForker 安全比较两个后端候选是否为同一实例，支持不可比较类型避免Panic
// 入参: left 左侧后端, right 右侧后端
// 返回: bool 是否为同一实例
func sameForker(left, right any) bool {
	lt, rt := reflect.TypeOf(left), reflect.TypeOf(right)
	if lt != rt {
		return false
	}
	if lt.Comparable() {
		return left == right
	}
	return reflect.DeepEqual(left, right)
}

// Reader 返回当前渲染器绑定的阅读器实例
// 返回: *Reader 阅读器实例
func (r *Renderer) Reader() *Reader {
	if r == nil {
		return nil
	}
	return r.reader
}

// DPI 返回当前渲染分辨率
// 返回: float64 分辨率DPI
func (r *Renderer) DPI() float64 {
	if r == nil {
		return 0
	}
	return r.dpi
}

// SetDPI 更新渲染分辨率，拒绝非正数或非有限值以防止除零异常
// 入参: dpi 目标DPI值
// 返回: error 无效DPI或空渲染器错误
func (r *Renderer) SetDPI(dpi float64) error {
	if r == nil {
		return fmt.Errorf("nil renderer")
	}
	if !finite(dpi) || dpi <= 0 {
		return fmt.Errorf("invalid DPI: %v", dpi)
	}
	r.dpi = dpi
	return nil
}

// RenderAnnotations 返回是否渲染页面注释外观
// 返回: bool 是否渲染注释
func (r *Renderer) RenderAnnotations() bool {
	if r == nil {
		return false
	}
	return r.renderAnnotations
}

// SetRenderAnnotations 设置是否渲染页面注释外观
// 入参: enabled 是否启用注释渲染
func (r *Renderer) SetRenderAnnotations(enabled bool) {
	if r != nil {
		r.renderAnnotations = enabled
	}
}

// TransparentBackground 返回是否关闭页面白色背景
// 返回: bool 是否透明背景
func (r *Renderer) TransparentBackground() bool {
	if r == nil {
		return false
	}
	return r.transparentBackground
}

// SetTransparentBackground 设置是否关闭页面白色背景
// 入参: enabled 是否透明背景
func (r *Renderer) SetTransparentBackground(enabled bool) {
	if r != nil {
		r.transparentBackground = enabled
	}
}

// Compression 返回当前导出编码压缩选项
// 返回: CompressionOptions 压缩选项
func (r *Renderer) Compression() CompressionOptions {
	if r == nil {
		return CompressionOptions{}
	}
	return r.compression
}

// SetCompression 设置导出编码压缩选项
// 入参: options 压缩选项
func (r *Renderer) SetCompression(options CompressionOptions) {
	if r != nil {
		r.compression = options
	}
}

// SVGPathMerge 返回当前SVG路径合并选项
// 返回: SVGPathMergeOptions 路径合并选项
func (r *Renderer) SVGPathMerge() SVGPathMergeOptions {
	if r == nil {
		return SVGPathMergeOptions{}.normalize()
	}
	return r.svgPathMerge.normalize()
}

// SetSVGPathMerge 设置SVG路径合并选项
// 入参: options 路径合并选项
func (r *Renderer) SetSVGPathMerge(options SVGPathMergeOptions) {
	if r != nil {
		r.svgPathMerge = options.normalize()
	}
}

// Fork 创建独立渲染实例，复用只读资源及线程安全的派生数据缓存
// 创建过程需与原实例串行，输入生命周期遵循Reader.Fork，外部后端及字体文件系统需支持并行读取
// 页面文字及导出进度回调不继承
// 入参: ctx 取消上下文
// 返回: *Renderer 独立渲染器, error 准备或取消错误
func (r *Renderer) Fork(ctx context.Context) (*Renderer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reader, err := r.reader.Fork(ctx)
	if err != nil {
		return nil, err
	}
	drawParams := r.drawParams
	composites := r.compositeGraphicUnits
	if r.hasBaseScope {
		drawParams = r.scopedDrawParams
		composites = r.scopedComposites
	}
	maps.Copy(reader.drawParamCache, drawParams)
	maps.Copy(reader.compositeGraphicUnitCache, composites)
	next := NewRenderer(reader, WithDPI(r.dpi), WithAnnotations(r.renderAnnotations), WithCompression(r.compression), WithTransparentBackground(r.transparentBackground), WithSVGPathMerge(r.svgPathMerge), WithRenderBackends(r.backends), WithFontDirs(r.fontDirs...), WithFontFS(r.fontFS...))
	next.ownsReader = true
	next.rasterDisabled = r.rasterDisabled
	next.decodeImages = r.decodeImages
	next.printing = r.printing
	next.printContents = r.printContents
	next.imageCache = r.imageCache
	next.templatePages = r.sharedTemplateCache()
	maps.Copy(next.templatePageCache, r.templatePageCache)
	next.fontPreparations = r.fontPreparations
	next.fontMetrics = r.fontMetrics
	next.glyphOutlines = r.glyphOutlines
	if r.fontSourcesCache != nil && next.fontSourcesCache != nil {
		next.fontSourcesCache.data = r.fontSourcesCache.data
		next.fontSourcesCache.collections = r.fontSourcesCache.collections
		next.fontSourcesCache.resolved = r.fontSourcesCache.resolved
		next.fontSourcesCache.shared = r.fontSourcesCache.shared
	}
	maps.Copy(next.sharedBackendStates, r.sharedBackendStates)
	var seenForkers []any
	for _, candidate := range []any{r.backends.Compiler, r.backends.Raster, r.backends.Fonts, r.backends.Geometry, r.backends.SVG, r.backends.PDF, r.backends.EPS} {
		if candidate == nil {
			continue
		}
		duplicated := false
		for _, prev := range seenForkers {
			if sameForker(prev, candidate) {
				duplicated = true
				break
			}
		}
		if duplicated {
			continue
		}
		seenForkers = append(seenForkers, candidate)
		if forker, ok := candidate.(sharedStateForker); ok {
			forker.forkSharedState(r, next)
		}
	}
	return next, nil
}

// Close 仅关闭Fork派生渲染器自有的Reader以释放加密凭据租约，不清理共享缓存，幂等且nil安全
// 返回: error 关闭错误
func (r *Renderer) Close() error {
	if r == nil || !r.ownsReader {
		return nil
	}
	r.ownsReader = false
	if r.reader == nil {
		return nil
	}
	return r.reader.Close()
}

// ClearCache 释放派生字体、图片、模板与全部后端及共享后端缓存，不改变文档和渲染配置
func (r *Renderer) ClearCache() {
	if r == nil {
		return
	}
	r.imageCache.clear()
	r.templatePages.clear()
	for _, state := range r.backendStates {
		if cache, ok := state.(interface{ clear() }); ok {
			cache.clear()
		} else if cache, ok := state.(interface{ Clear() }); ok {
			cache.Clear()
		}
	}
	r.backendStates = make(map[any]any)
	for _, state := range r.sharedBackendStates {
		if cache, ok := state.(interface{ clear() }); ok {
			cache.clear()
		} else if cache, ok := state.(interface{ Clear() }); ok {
			cache.Clear()
		}
	}
	r.sharedBackendStates = make(map[any]any)
	var resources *Resources
	if r.reader != nil {
		resources = r.reader.resources
	}
	r.imageCache = resourceCache[string, image.Image](resources, imageCacheLimit)
	r.templatePageCache = make(map[string]*PageContent)
	r.templatePages = nil
	r.transparentEdges = nil
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

// RebindReader 同步重绑阅读器快照并重置依赖旧快照的图片、模板与局部映射，避免命中脏缓存
// 入参: reader 新的阅读器快照
func (r *Renderer) RebindReader(reader *Reader) {
	if r == nil || reader == nil {
		return
	}
	if r.ownsReader && r.reader != nil && r.reader != reader {
		_ = r.reader.Close()
		r.ownsReader = false
	}
	if reader.resources == nil {
		reader.resources, _ = NewResources(ResourceOptions{})
	}
	r.reader = reader
	r.drawParams = reader.drawParamCache
	r.compositeGraphicUnits = reader.compositeGraphicUnitCache
	r.hasBaseScope = false
	r.scopedDrawParams = nil
	r.scopedComposites = nil
	r.pageResources = nil
	r.imageCache = resourceCache[string, image.Image](reader.resources, imageCacheLimit)
	r.templatePageCache = make(map[string]*PageContent)
	r.templatePages = nil
	r.backendStates = make(map[any]any)
	r.transparentEdges = nil
	r.resetFontCache()
}

// BindPageResources 解析并绑定指定页面的局部资源作用域，返回恢复闭包
// 入参: page 页面内容
// 返回: func() 恢复之前的作用域状态
func (r *Renderer) BindPageResources(page *PageContent) func() {
	return r.bindPageResources(page)
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
	if r.reader != nil && r.reader.doc == nil && r.reader.ofd != nil {
		_, _ = r.reader.docStructureContext(ctx)
	}
	res := page.pageResources
	if res == nil && len(page.PageRes) > 0 && r.reader != nil {
		basePath := ""
		if r.reader.doc != nil && page.ID != "" {
			for _, ref := range r.reader.doc.Pages.Page {
				if sameResourceID(ref.ID, page.ID) {
					basePath = r.reader.ResPath(ref.BaseLoc)
					break
				}
			}
		}
		if basePath == "" {
			basePath = r.reader.ResPath("Document.xml")
		}
		if collected, err := r.reader.collectPageResourcesContext(ctx, basePath, page); err == nil {
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
	prevDrawParams := r.drawParams
	prevComposites := r.compositeGraphicUnits
	prevHasBase := r.hasBaseScope
	prevBaseDP := r.scopedDrawParams
	prevBaseComp := r.scopedComposites
	if !r.hasBaseScope {
		r.hasBaseScope = true
		r.scopedDrawParams = prevDrawParams
		r.scopedComposites = prevComposites
	}
	r.pageResources = res
	r.drawParams = r.mergeScopedDrawParams(r.scopedDrawParams, res)
	r.compositeGraphicUnits = r.mergeScopedComposites(r.scopedComposites, res)
	return func() {
		r.pageResources = prevRes
		r.drawParams = prevDrawParams
		r.compositeGraphicUnits = prevComposites
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
	if r.reader != nil {
		docIndex = r.reader.docDrawParams
		docFallback = r.reader.drawParamCache
	}
	return mergeScopedResources(base, docIndex, docFallback, r.reader != nil, locals)
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
	if r.reader != nil {
		docIndex = r.reader.docComposites
		docFallback = r.reader.compositeGraphicUnitCache
	}
	return mergeScopedResources(base, docIndex, docFallback, r.reader != nil, locals)
}

// hasCompositeOverride 判断外部基准表是否显式注入了覆盖文档定义的复合图元
// 入参: base 外部基准表, id 资源标识
// 返回: bool 是否存在外部显式覆盖
func (r *Renderer) hasCompositeOverride(base map[string]*CompositeGraphicUnit, id string) bool {
	existing, ok := resourceValue(base, id)
	if !ok || existing == nil {
		return false
	}
	if r.reader == nil {
		return true
	}
	var docVal *CompositeGraphicUnit
	if r.reader.docComposites != nil {
		docVal, _ = r.reader.docComposites.get(id)
	} else {
		docVal, _ = resourceValue(r.reader.compositeGraphicUnitCache, id)
	}
	return existing != docVal
}

// fontDefinition 按页面局部资源优先查找字体定义
// 入参: id 字体资源标识
// 返回: *Font 字体定义
func (r *Renderer) fontDefinition(id string) *Font {
	if r.reader == nil {
		return nil
	}
	if font, ok := r.reader.resolveFontScoped(r.pageResources, id); ok {
		return font
	}
	return r.reader.fontDefinition(id)
}

// compositeDefinition 按外部覆盖与页面局部资源优先查找复合图元定义
// 入参: id 复合图元资源标识
// 返回: *CompositeGraphicUnit 复合图元定义
func (r *Renderer) compositeDefinition(id string) *CompositeGraphicUnit {
	base := r.compositeGraphicUnits
	if r.hasBaseScope {
		base = r.scopedComposites
	}
	if r.hasCompositeOverride(base, id) {
		unit, _ := resourceValue(base, id)
		return unit
	}
	if r.reader != nil {
		if scoped, found := r.reader.resolveCompositeScoped(r.pageResources, id); found {
			return scoped
		}
	}
	unit, _ := resourceValue(r.compositeGraphicUnits, id)
	return unit
}

// deviceLineWidth 按设备分辨率计算描边宽度，零线宽为一像素，正线宽保留物理宽度
// 入参: width 页面空间线宽，单位为毫米
// 返回: float64 设备描边宽度，单位为毫米
func (r *Renderer) deviceLineWidth(width float64) float64 {
	if width == 0 {
		return 25.4 / r.dpi
	}
	return width
}

// childRenderer 继承配置并复用字体文件索引、候选与数据，文档解析与回退状态保持独立
// 入参: reader 子阅读器
// 返回: *Renderer 子渲染器
func (r *Renderer) childRenderer(reader *Reader) *Renderer {
	opts := []RendererOption{
		WithDPI(r.dpi),
		WithAnnotations(r.renderAnnotations),
		WithSVGPathMerge(r.svgPathMerge),
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
	if r.sharedBackendStates == nil {
		r.sharedBackendStates = make(map[any]any)
	}
	renderer.sharedBackendStates = r.sharedBackendStates
	renderer.fontPreparations = r.fontPreparations
	renderer.fontMetrics = r.fontMetrics
	renderer.glyphOutlines = r.glyphOutlines
	renderer.fontSourcesCache = r.fontSourcesCache.child()
	renderer.decodeImages = r.decodeImages
	renderer.pageContext = r.pageContext
	renderer.transparentBackground = r.transparentBackground
	return renderer
}

// syncChildRenderer 派生同协程同步子渲染器，复用父渲染器字体族缓存
// 入参: reader 子阅读器
// 返回: *Renderer 子渲染器
func (r *Renderer) syncChildRenderer(reader *Reader) *Renderer {
	renderer := r.childRenderer(reader)
	if r.reader != nil {
		renderer.canvasState().fontFamilyCache = r.canvasFontFamilies()
	}
	return renderer
}

// sharedTemplateCache 获取跨派生渲染器共享的模板页解析缓存
// 返回: *renderCache[string, *PageContent] 模板页缓存
func (r *Renderer) sharedTemplateCache() *renderCache[string, *PageContent] {
	if r.templatePages == nil {
		var resources *Resources
		if r.reader != nil {
			resources = r.reader.resources
		}
		r.templatePages = resourceCache[string, *PageContent](resources, 16<<20)
	}
	return r.templatePages
}

// loadTemplate 按标识数值加载模板页面，等价引用共用解析结果并跨Fork共享
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
	page, err := r.sharedTemplateCache().load(ctx, templateID, func() (*PageContent, int, error) {
		for _, tpl := range r.reader.doc.CommonData.TemplatePage {
			if !sameResourceID(tpl.ID, templateID) {
				continue
			}
			loaded, readErr := r.reader.readPageContent(ctx, Page{BaseLoc: tpl.BaseLoc}, false, true)
			if readErr != nil {
				return nil, 0, readErr
			}
			return loaded, 4096 + len(loaded.Content.Layer)*512, nil
		}
		return nil, 0, nil
	})
	if err != nil || page == nil {
		return nil
	}
	if r.templatePageCache == nil {
		r.templatePageCache = make(map[string]*PageContent)
	}
	r.templatePageCache[templateID] = page
	return page
}

// templateOrder 获取页面模板引用的绘制顺序，省略时继承文档模板页声明并回退背景层
// 入参: ref 页面模板引用
// 返回: int 绘制顺序
func (r *Renderer) templateOrder(ref Template) int {
	kind := ref.ZOrder
	if kind == "" && r.reader.doc != nil {
		for _, tpl := range r.reader.doc.CommonData.TemplatePage {
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
		return DefaultBackend{}
	}
	return nil
}
