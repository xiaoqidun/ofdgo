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
	"fmt"
	"image"
	"io"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/tdewolff/canvas/renderers/rasterizer"
	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// rasterRenderer 保留页面物理尺寸的光栅渲染器
type rasterRenderer struct {
	*rasterizer.Rasterizer
	width, height float64
	dpmm          float64
	linear        bool
}

// Size 返回页面物理尺寸
// 返回: float64 宽度, float64 高度
func (r *rasterRenderer) Size() (float64, float64) {
	return r.width, r.height
}

// RenderImage 使用底层图片完成平移和缩放
// 入参: img 图片对象, m 图片变换矩阵
func (r *rasterRenderer) RenderImage(img image.Image, m canvas.Matrix) {
	if !r.linear || m[0][1] != 0 || m[1][0] != 0 {
		r.Rasterizer.RenderImage(img, m)
		return
	}
	origin := m.Dot(canvas.Point{Y: float64(img.Bounds().Dy())}).Mul(r.dpmm)
	m = m.Scale(r.dpmm, r.dpmm)
	transform := f64.Aff3{m[0][0], -m[0][1], origin.X, -m[1][0], m[1][1], float64(r.Bounds().Dy()) - origin.Y}
	draw.CatmullRom.Transform(r.Image, transform, img, img.Bounds(), draw.Over, nil)
}

// RenderToImage 渲染为光栅图
// 入参: page 页面内容
// 返回: image.Image 图像对象, error 错误信息
func (r *Renderer) RenderToImage(page *PageContent) (image.Image, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	dpmm := r.DPI / 25.4
	colorSpace := canvas.DefaultColorSpace
	_, linear := colorSpace.(canvas.LinearColorSpace)
	raster := &rasterRenderer{
		Rasterizer: rasterizer.New(box.W, box.H, canvas.DPMM(dpmm), colorSpace),
		width:      box.W,
		height:     box.H,
		dpmm:       dpmm,
		linear:     linear,
	}
	if err := r.RenderPageToContext(canvas.NewContext(raster), page); err != nil {
		return nil, err
	}
	raster.Close()
	return raster.Image, nil
}

// RenderToSVG 渲染为SVG
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToSVG(page *PageContent, writer io.Writer) error {
	c, err := r.renderPage(page)
	if err != nil {
		return err
	}
	return c.Write(writer, renderers.SVG())
}

// replacePDFProducer 替换PDF的Producer属性
// 入参: data PDF字节数据
// 返回: []byte 替换后的PDF字节数据
func replacePDFProducer(data []byte) []byte {
	old := []byte("/Producer(tdewolff/canvas)")
	idx := bytes.LastIndex(data, old)
	if idx < 0 {
		return data
	}
	dst := []byte("/Producer(xiaoqidun/ofdgo)")
	copy(data[idx:idx+len(old)], dst)
	return data
}

// RenderToPDF 渲染为PDF
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToPDF(page *PageContent, writer io.Writer) error {
	box, err := r.GetPageBox(page)
	if err != nil {
		return err
	}
	pages := []pdfPage{{Content: page, Box: box}}
	return r.renderPDFPages(pages, writer)
}

// RenderToEPS 渲染为EPS
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToEPS(page *PageContent, writer io.Writer) error {
	c, err := r.renderPage(page)
	if err != nil {
		return err
	}
	return c.Write(writer, renderers.EPS())
}

// RenderToMultiPagePDF 将整个文档导出为多页PDF
// 入参: writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToMultiPagePDF(writer io.Writer) error {
	doc, err := r.Reader.Doc()
	if err != nil {
		return err
	}
	if len(doc.Pages.Page) == 0 {
		return fmt.Errorf("no pages found")
	}
	pages := make([]pdfPage, len(doc.Pages.Page))
	for i, pageRef := range doc.Pages.Page {
		page, err := r.Reader.PageContent(pageRef)
		if err != nil {
			return fmt.Errorf("failed to read page %d: %w", i+1, err)
		}
		box, err := r.GetPageBox(page)
		if err != nil {
			return fmt.Errorf("failed to read page %d area: %w", i+1, err)
		}
		pages[i] = pdfPage{Content: page, Box: box}
	}
	return r.renderPDFPages(pages, writer)
}

// RenderPagesToPDF 按指定顺序导出已解析的页面，复用调用方的页面缓存
// 入参: contents 页面内容列表, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderPagesToPDF(contents []*PageContent, writer io.Writer) error {
	if len(contents) == 0 {
		return fmt.Errorf("no pages found")
	}
	pages := make([]pdfPage, len(contents))
	for i, page := range contents {
		box, err := r.GetPageBox(page)
		if err != nil {
			return fmt.Errorf("failed to read page %d area: %w", i+1, err)
		}
		pages[i] = pdfPage{Content: page, Box: box}
	}
	return r.renderPDFPages(pages, writer)
}

// renderPDFPages 渲染页面并复用调用方的字节缓冲
// 入参: pages 页面列表, writer 输出流
// 返回: error 错误信息
func (r *Renderer) renderPDFPages(pages []pdfPage, writer io.Writer) error {
	navigation := newPDFNavigation(r, r.Reader.doc, pages)
	buf, direct := writer.(*bytes.Buffer)
	if !direct {
		buf = &bytes.Buffer{}
	}
	start := buf.Len()
	p := pdf.New(buf, pages[0].Box.W, pages[0].Box.H, nil)
	p.SetInfo("", "", "", "", "xiaoqidun/ofdgo")
	for i, page := range pages {
		if i > 0 {
			p.NewPage(page.Box.W, page.Box.H)
		}
		navigation.apply(p, i)
		if err := r.renderPageToContext(canvas.NewContext(p), page.Content, true); err != nil {
			buf.Truncate(start)
			return fmt.Errorf("failed to render page %d: %w", i+1, err)
		}
	}
	if err := p.Close(); err != nil {
		buf.Truncate(start)
		return err
	}
	data := replacePDFProducer(buf.Bytes()[start:])
	if direct {
		return nil
	}
	_, err := writer.Write(data)
	return err
}
