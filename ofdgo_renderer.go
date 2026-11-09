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
	backends              RenderBackends
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
	next := NewRenderer(reader, WithDPI(r.DPI), WithAnnotations(r.RenderAnnotations), WithRenderBackends(r.backends), WithFontDirs(r.fontDirs...), WithFontFS(r.fontFS...))
	next.Compression = r.Compression
	next.DrawParams = maps.Clone(r.DrawParams)
	next.CompositeGraphicUnits = maps.Clone(r.CompositeGraphicUnits)
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
	next.sharedBackendStates[canvasPathCacheKey{}] = r.canvasParsedPaths()
	next.sharedBackendStates[canvasClipCacheKey{}] = r.canvasClipIntersections()
	next.sharedBackendStates[canvasFillCacheKey{}] = r.canvasSettledPaths()
	next.sharedBackendStates[canvasDrawClipCacheKey{}] = r.canvasDrawClips()
	next.sharedBackendStates[canvasStrokeCacheKey{}] = r.canvasStrokeClips()
	next.sharedBackendStates[svgResourceCacheKey{}] = r.svgImageResources()
	return next, nil
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

// childRenderer 继承配置并复用字体文件索引与数据，文档匹配结果保持独立
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
	renderer.printing = r.printing
	renderer.sharedBackendStates = r.sharedBackendStates
	renderer.fontPreparations = r.fontPreparations
	renderer.fontDigests = r.fontDigests
	renderer.fontMetrics = r.fontMetrics
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
