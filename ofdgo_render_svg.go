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
	"context"
	"crypto/sha256"
	"fmt"
	"io"
)

// svgNamespace 表示SVG预览文档命名空间
const svgNamespace = "http://www.w3.org/2000/svg"

// SVGFont SVG引用的字体资源，Name为CSS字体族标识，Data为只读字体数据
type SVGFont struct {
	Name   string
	Weight int
	Style  string
	Data   []byte
}

// SVGImage SVG引用的图片资源，Name为图片地址，Data为只读图片数据
type SVGImage struct {
	Name string
	MIME string
	Data []byte
}

// SVGResources SVG引用的字体和图片资源
type SVGResources struct {
	Fonts  []SVGFont
	Images []SVGImage
}

// PreviewOptions 指定页面显示方式，光栅预览不提供SVG对象分组
type PreviewOptions struct {
	Raster  bool
	Objects bool
	// KnownResources 标识调用方仍持有的资源，返回引用但不重复返回数据
	KnownResources map[string]bool
}

// RenderToSVGWithFonts 渲染为SVG并返回页面引用的字体资源，不在SVG中嵌入字体
// 入参: page 页面内容, writer 输出流
// 返回: []SVGFont 字体资源，可按Name复用并按Weight、Style注册，error 错误信息
func (r *Renderer) RenderToSVGWithFonts(page *PageContent, writer io.Writer) ([]SVGFont, error) {
	resources, err := r.renderSVG(page, writer, SVGExternalFonts)
	return resources.Fonts, err
}

// RenderToSVGWithResources 渲染为SVG并分离字体和图片，调用方需注册字体并将图片Name映射为可访问地址
// 入参: page 页面内容, writer 输出流
// 返回: SVGResources 可跨页面复用的资源, error 错误信息
func (r *Renderer) RenderToSVGWithResources(page *PageContent, writer io.Writer) (SVGResources, error) {
	return r.renderSVG(page, writer, SVGExternalResources)
}

// RenderToSVGWithObjects 渲染SVG并为页面对象和注解添加data-ofd-object分组，注解标识使用annotation:前缀
// 入参: page 页面内容, writer 输出流
// 返回: SVGResources 字体和图片资源, error 错误信息
func (r *Renderer) RenderToSVGWithObjects(page *PageContent, writer io.Writer) (SVGResources, error) {
	return r.renderSVG(page, writer, SVGObjects)
}

// renderSVG 将SVG输出及对象分组交给指定后端
// 入参: page 页面内容, writer 输出流, mode 资源封装方式
// 返回: SVGResources 字体和图片资源, error 渲染、写入或取消错误
func (r *Renderer) renderSVG(page *PageContent, writer io.Writer, mode SVGMode) (SVGResources, error) {
	if err := r.Compression.Validate(); err != nil {
		return SVGResources{}, err
	}
	if r.backends.SVG == nil {
		return SVGResources{}, fmt.Errorf("svg: %w", ErrBackendUnavailable)
	}
	var written int64
	writer = convertWriter{ctx: r.outputContext(), writer: writer, count: &written}
	return r.backends.SVG.RenderSVG(r, page, writer, mode)
}

// RenderPreview 生成统一SVG容器及独立资源，原生程序与WebUI使用相同绘制入口
// 入参: page 页面内容, writer 输出流, options 显示方式
// 返回: SVGResources 需由调用方映射地址的资源, error 错误信息
func (r *Renderer) RenderPreview(page *PageContent, writer io.Writer, options PreviewOptions) (resources SVGResources, err error) {
	view := *r
	view.knownSVGResources = options.KnownResources
	r = &view
	defer func() {
		for i := range resources.Fonts {
			if options.KnownResources[resources.Fonts[i].Name] {
				resources.Fonts[i].Data = nil
			}
		}
		for i := range resources.Images {
			if options.KnownResources[resources.Images[i].Name] {
				resources.Images[i].Data = nil
			}
		}
	}()
	if !options.Raster {
		if options.Objects {
			return r.RenderToSVGWithObjects(page, writer)
		}
		return r.RenderToSVGWithResources(page, writer)
	}
	if options.Objects {
		return SVGResources{}, fmt.Errorf("raster preview does not provide SVG object groups")
	}
	box, err := r.GetPageBox(page)
	if err != nil {
		return SVGResources{}, err
	}
	var pixels bytes.Buffer
	if err := r.RenderToPNG(page, &pixels); err != nil {
		return SVGResources{}, err
	}
	name := fmt.Sprintf("ofdgo-image-%x", sha256.Sum256(pixels.Bytes()))
	_, err = fmt.Fprintf(writer, `<svg xmlns="`+svgNamespace+`" xmlns:xlink="`+XLinkNamespace+`" width="%gmm" height="%gmm" viewBox="0 0 %g %g"><image width="%g" height="%g" preserveAspectRatio="none" xlink:href="%s"/></svg>`, box.W, box.H, box.W, box.H, box.W, box.H, name)
	if err != nil {
		return SVGResources{}, err
	}
	return SVGResources{Images: []SVGImage{{Name: name, MIME: "image/png", Data: pixels.Bytes()}}}, nil
}

// RenderFontPreview 将指定字体的前32个可见字形绘制为独立SVG，不依赖字符映射或浏览器字体
// 排除缺字字形，外部字体须精确匹配，不修改原始资源或使用无关回退字体
// 入参: ctx 取消上下文, id 字体资源标识, writer 输出流
// 返回: error 字体、轮廓、取消或写入错误
func (r *Renderer) RenderFontPreview(ctx context.Context, id string, writer io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	view := *r
	view.pageContext = ctx
	resolved, err := view.ResolveFont(id, true)
	if err != nil {
		return err
	}
	font := &PreparedFont{ResolvedFont: resolved}
	if definition := view.Reader.fontDefinition(id); definition != nil && definition.FontFile != "" {
		font, err = view.PrepareFont(id)
		if err != nil {
			return err
		}
	}
	metrics, err := view.preparedMetrics(font)
	if err != nil {
		return err
	}
	outlines, ok := metrics.(FontOutlines)
	if !ok {
		return fmt.Errorf("font outlines: %w", ErrBackendUnavailable)
	}
	var paths bytes.Buffer
	count := 0
	for glyph := uint16(1); glyph < metrics.NumGlyphs() && count < 32; glyph++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		outline, err := outlines.GlyphOutline(glyph, 24)
		if err != nil {
			return err
		}
		box, err := outline.Bounds()
		if err != nil {
			return err
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		path, err := outline.SVG()
		if err != nil {
			return err
		}
		scale := min(1.0, 28/box.W, 32/box.H)
		x := float64(count%8)*36 + (36-box.W*scale)/2 - box.X*scale
		y := float64(count/8)*40 + (40-box.H*scale)/2 - box.Y*scale
		fmt.Fprintf(&paths, `<path transform="translate(%g %g) scale(%g)" d="%s"/>`, x, y, scale, path)
		count++
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("font has no visible glyphs")
	}
	width, height := min(count, 8)*36, (count+7)/8*40
	_, err = fmt.Fprintf(writer, `<svg xmlns="`+svgNamespace+`" width="%d" height="%d" viewBox="0 0 %d %d">%s</svg>`, width, height, width, height, paths.Bytes())
	return err
}
