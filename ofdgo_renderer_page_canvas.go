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
		ctx.DrawPath(0, 0, canvas.Rectangle(box.W, box.H))
	}
	for order := range 3 {
		if r.Reader.doc != nil {
			for _, tplRef := range page.Template {
				kind := tplRef.ZOrder
				if kind == "" {
					kind = "Background"
				}
				if layerOrder(kind) == order {
					r.renderTemplate(ctx, tplRef.TemplateID, pageH)
				}
			}
		}
		r.renderLayers(ctx, page.Content.Layer, pageH, order)
	}
	if r.RenderAnnotations {
		r.renderAnnotations(ctx, page.ID, pageH)
	}
	if stamps, ok := r.Reader.Stamps[page.ID]; ok {
		for _, stamp := range stamps {
			r.renderStamp(ctx, stamp, pageH)
		}
	}
	if r.renderError != nil {
		return r.renderError
	}
	if r.OnPageText != nil {
		r.OnPageText(page, r.pageText)
	}
	return nil
}
