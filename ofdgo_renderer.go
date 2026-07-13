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
	"io/fs"

	"github.com/tdewolff/canvas"
)

// Renderer 渲染器实现
type Renderer struct {
	Reader                *Reader
	DPI                   float64
	RenderAnnotations     bool
	fontFamily            *canvas.FontFamily
	defaultFontLoaded     bool
	DrawParams            map[string]*DrawParam
	CompositeGraphicUnits map[string]*CompositeGraphicUnit
	FontMap               map[string]*canvas.FontFamily
	FontGIDMap            map[string]map[uint16]rune
	FontCIDMap            map[string]map[uint16]rune
	fontFSCache           map[fontFSKey]*canvas.FontFamily
	textGlyphPathCache    map[textGlyphPathCacheKey]textGlyphPathCacheValue
	fontDirs              []string
	fontFS                []fs.FS
}

// RendererOption 渲染器配置选项
type RendererOption func(*Renderer)

// RenderPage 渲染特定页面内容
// 入参: page 页面内容
// 返回: *canvas.Canvas 画布实例, error 错误信息
func (r *Renderer) RenderPage(page *PageContent) (*canvas.Canvas, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	width, height := box.W, box.H
	c := canvas.New(width, height)
	ctx := canvas.NewContext(c)
	if err := r.RenderPageToContext(ctx, page); err != nil {
		return nil, err
	}
	return c, nil
}

// GetPageBox 获取页面物理区域
// 入参: page 页面内容
// 返回: Box 区域, error 错误信息
func (r *Renderer) GetPageBox(page *PageContent) (Box, error) {
	boxStr := page.Area.PhysicalBox
	if boxStr == "" {
		boxStr = page.Area.ApplicationBox
	}
	if boxStr == "" {
		boxStr = page.Area.ContentBox
	}
	if boxStr == "" && r.Reader.doc != nil {
		boxStr = r.Reader.doc.CommonData.PageArea.PhysicalBox
	}
	if boxStr == "" {
		boxStr = "0 0 210 297"
	}
	return ParseBox(boxStr)
}

// RenderPageToContext 渲染页面到指定上下文
// 入参: ctx 画布上下文, page 页面内容
// 返回: error 错误信息
func (r *Renderer) RenderPageToContext(ctx *canvas.Context, page *PageContent) error {
	return r.renderPageToContext(ctx, page, true)
}

// renderPageToContext 渲染页面到指定上下文
// 入参: ctx 画布上下文, page 页面内容, drawBackground 是否绘制页面背景
// 返回: error 错误信息
func (r *Renderer) renderPageToContext(ctx *canvas.Context, page *PageContent, drawBackground bool) error {
	box, err := r.GetPageBox(page)
	if err != nil {
		return err
	}
	pageH := box.H
	if drawBackground {
		ctx.SetFillColor(canvas.White)
		ctx.DrawPath(0, 0, canvas.Rectangle(box.W, box.H))
	}
	if len(page.Template) > 0 && r.Reader.doc != nil {
		for _, tplRef := range page.Template {
			if tplRef.ZOrder != "Foreground" {
				r.renderTemplate(ctx, tplRef.TemplateID, pageH)
			}
		}
	}
	if page.Content.Layer != nil {
		for _, layer := range page.Content.Layer {
			r.renderLayer(ctx, layer, pageH, nil, nil, 0, nil)
		}
	}
	if len(page.Template) > 0 && r.Reader.doc != nil {
		for _, tplRef := range page.Template {
			if tplRef.ZOrder == "Foreground" {
				r.renderTemplate(ctx, tplRef.TemplateID, pageH)
			}
		}
	}
	if r.RenderAnnotations {
		r.renderAnnotations(ctx, page.ID, pageH)
	}
	if stamps, ok := r.Reader.Stamps[page.ID]; ok {
		for _, stamp := range stamps {
			r.renderStamp(ctx, stamp, pageH)
		}
	}
	return nil
}

// RenderPageByIndex 按索引渲染页面
// 入参: index 页面索引
// 返回: *canvas.Canvas 画布实例, error 错误信息
func (r *Renderer) RenderPageByIndex(index int) (*canvas.Canvas, error) {
	doc, err := r.Reader.Doc()
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Pages.Page) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	page, err := r.Reader.PageContent(doc.Pages.Page[index])
	if err != nil {
		return nil, err
	}
	return r.RenderPage(page)
}

// initCommon 初始化公共资源
func (r *Renderer) initCommon() {
	r.fontFamily = canvas.NewFontFamily("default")
	r.defaultFontLoaded = r.loadDefaultFonts()
}

// childRenderer 创建继承当前配置的子渲染器
// 入参: reader 子阅读器
// 返回: *Renderer 子渲染器
func (r *Renderer) childRenderer(reader *Reader) *Renderer {
	opts := []RendererOption{
		WithDPI(r.DPI),
		WithAnnotations(r.RenderAnnotations),
	}
	if len(r.fontDirs) > 0 {
		opts = append(opts, WithFontDirs(r.fontDirs...))
	}
	if len(r.fontFS) > 0 {
		opts = append(opts, WithFontFS(r.fontFS...))
	}
	return NewRenderer(reader, opts...)
}
