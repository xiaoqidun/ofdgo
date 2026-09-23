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
	"image"
	"io/fs"
)

// Renderer 渲染器实现
// 通过NewRenderer创建，实例及共享的Reader需串行使用
// OnPageText可选，接收页面绘制时同步提取的文字，不包含图案和签名外观
// OnExportProgress可选，同步回报文档导出的已处理页数及总页数，返回错误则停止，页数完成不代表写入成功
// TransparentBackground关闭页面白底，供嵌套图案和印章保持透明背景
type Renderer struct {
	Reader                *Reader
	DPI                   float64
	RenderAnnotations     bool
	OnPageText            func(*PageContent, *PageText)
	OnExportProgress      func(completed, total int) error
	DrawParams            map[string]*DrawParam
	CompositeGraphicUnits map[string]*CompositeGraphicUnit
	templatePageCache     map[string]*PageContent
	imageCache            map[string]image.Image
	fontDirs              []string
	fontFS                []fs.FS
	decodeImages          bool
	pageText              *PageText
	textOnly              bool
	backends              RenderBackends
	backendStates         map[any]any
	resolvedFonts         map[resolvedFontKey]resolvedFontResult
	preparedFonts         map[string]*PreparedFont
	fontSourcesCache      *fontSourceCache
	fontMetrics           renderCache[[32]byte, FontMetrics]
	glyphOutlines         renderCache[glyphOutlineKey, GeometryPath]
	renderError           error
	TransparentBackground bool
}

// RendererOption 渲染器配置选项
type RendererOption func(*Renderer)

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

// childRenderer 创建继承当前配置的子渲染器
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
	renderer.decodeImages = r.decodeImages
	renderer.TransparentBackground = r.TransparentBackground
	return renderer
}

// loadTemplate 加载模板页面并复用解析结果
// 入参: templateID 模板ID
// 返回: *PageContent 模板页面
func (r *Renderer) loadTemplate(templateID string) *PageContent {
	if page := r.templatePageCache[templateID]; page != nil {
		return page
	}
	for _, tpl := range r.Reader.doc.CommonData.TemplatePage {
		if tpl.ID != templateID {
			continue
		}
		page, err := r.Reader.PageContent(Page{BaseLoc: tpl.BaseLoc})
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
