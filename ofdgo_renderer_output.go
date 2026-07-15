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
)

// RenderToImage 渲染为光栅图
// 入参: page 页面内容
// 返回: image.Image 图像对象, error 错误信息
func (r *Renderer) RenderToImage(page *PageContent) (image.Image, error) {
	renderer := *r
	renderer.decodeImages = true
	c, err := renderer.RenderPage(page)
	if err != nil {
		return nil, err
	}
	dpmm := r.DPI / 25.4
	return rasterizer.Draw(c, canvas.DPMM(dpmm), canvas.DefaultColorSpace), nil
}

// RenderToSVG 渲染为SVG
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToSVG(page *PageContent, writer io.Writer) error {
	c, err := r.RenderPage(page)
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
	c, err := r.RenderPage(page)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	p := pdf.New(&buf, c.W, c.H, nil)
	p.SetInfo("", "", "", "", "xiaoqidun/ofdgo")
	c.RenderTo(p)
	if err := p.Close(); err != nil {
		return err
	}
	_, err = writer.Write(replacePDFProducer(buf.Bytes()))
	return err
}

// RenderToEPS 渲染为EPS
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToEPS(page *PageContent, writer io.Writer) error {
	c, err := r.RenderPage(page)
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
	var buf bytes.Buffer
	var p *pdf.PDF
	for _, pgRef := range doc.Pages.Page {
		page, err := r.Reader.PageContent(pgRef)
		if err != nil {
			continue
		}
		c, err := r.RenderPage(page)
		if err != nil {
			continue
		}
		if p == nil {
			p = pdf.New(&buf, c.W, c.H, nil)
			p.SetInfo("", "", "", "", "xiaoqidun/ofdgo")
		} else {
			p.NewPage(c.W, c.H)
		}
		c.RenderTo(p)
	}
	if p == nil {
		return fmt.Errorf("failed to render any page")
	}
	if err := p.Close(); err != nil {
		return err
	}
	_, err = writer.Write(replacePDFProducer(buf.Bytes()))
	return err
}
