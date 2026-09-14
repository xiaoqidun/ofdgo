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
	"archive/zip"
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strconv"
	"strings"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/tdewolff/canvas/renderers/ps"
	"github.com/tdewolff/canvas/renderers/rasterizer"
	"github.com/tdewolff/canvas/renderers/svg"
	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// RenderTo 按格式渲染单页，JPEG使用95画质
// 入参: page 页面内容, writer 输出流, format svg、pdf、eps、png、jpg、jpeg或txt
// 返回: error 错误信息
func (r *Renderer) RenderTo(page *PageContent, writer io.Writer, format string) error {
	_, render, err := r.outputRenderer(format)
	if err != nil {
		return err
	}
	return render(page, writer)
}

// outputRenderer 获取格式扩展名和渲染方法
// 入参: format 输出格式
// 返回: string 扩展名, func 渲染方法, error 错误信息
func (r *Renderer) outputRenderer(format string) (string, func(*PageContent, io.Writer) error, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "svg":
		return "svg", r.RenderToSVG, nil
	case "pdf":
		return "pdf", r.RenderToPDF, nil
	case "txt":
		return "txt", r.RenderToText, nil
	case "eps":
		return "eps", r.RenderToEPS, nil
	case "png":
		return "png", r.RenderToPNG, nil
	case "jpg", "jpeg":
		return "jpg", func(page *PageContent, writer io.Writer) error {
			return r.RenderToJPEG(page, writer, &jpeg.Options{Quality: 95})
		}, nil
	default:
		return "", nil, fmt.Errorf("unsupported output format %s", format)
	}
}

// RenderToZIP 逐页渲染并写入ZIP，文件编号按总页数补零，出错时调用方应丢弃输出
// 入参: writer 输出流, format svg、pdf、eps、png、jpg、jpeg或txt, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 错误信息
func (r *Renderer) RenderToZIP(writer io.Writer, format string, indices ...int) error {
	extension, render, err := r.outputRenderer(format)
	if err != nil {
		return err
	}
	count, err := r.Reader.PageCount()
	if err != nil {
		return err
	}
	indices, err = exportPageIndices(count, indices)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(writer)
	method := zip.Deflate
	if extension == "png" || extension == "jpg" {
		method = zip.Store
	}
	width := len(strconv.Itoa(count))
	if err := r.exportProgress(0, len(indices)); err != nil {
		return err
	}
	for i, index := range indices {
		page, err := r.Reader.PageContentByIndex(index)
		if err != nil {
			return fmt.Errorf("failed to read page %d: %w", index+1, err)
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{
			Name:   fmt.Sprintf("%0*d.%s", width, index+1, extension),
			Method: method,
		})
		if err != nil {
			return err
		}
		if err := render(page, entry); err != nil {
			return fmt.Errorf("failed to export page %d: %w", index+1, err)
		}
		if err := r.exportProgress(i+1, len(indices)); err != nil {
			return err
		}
	}
	return archive.Close()
}

// RenderToText 导出页面原文为UTF-8文本，以换行分隔非空文本对象
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToText(page *PageContent, writer io.Writer) error {
	text, err := r.PageText(page)
	if err != nil {
		return err
	}
	_, err = io.WriteString(writer, text.String())
	return err
}

// RenderToMultiPageText 逐页导出UTF-8原文，以换行分隔非空页面，出错时调用方应丢弃输出
// 入参: writer 输出流, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 错误信息
func (r *Renderer) RenderToMultiPageText(writer io.Writer, indices ...int) error {
	count, err := r.Reader.PageCount()
	if err != nil {
		return err
	}
	indices, err = exportPageIndices(count, indices)
	if err != nil {
		return err
	}
	if err := r.exportProgress(0, len(indices)); err != nil {
		return err
	}
	written := false
	for i, index := range indices {
		text, err := r.PageTextByIndex(index)
		if err != nil {
			return fmt.Errorf("failed to extract page %d text: %w", index+1, err)
		}
		if value := text.String(); value != "" {
			if written {
				if _, err := io.WriteString(writer, "\n"); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(writer, value); err != nil {
				return err
			}
			written = true
		}
		if err := r.exportProgress(i+1, len(indices)); err != nil {
			return err
		}
	}
	return nil
}

// exportProgress 回报导出进度并传递调用方的停止原因
// 入参: completed 已处理页数, total 总页数
// 返回: error 停止原因
func (r *Renderer) exportProgress(completed, total int) error {
	if r.OnExportProgress != nil {
		return r.OnExportProgress(completed, total)
	}
	return nil
}

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

// RenderToPNG 渲染为PNG，像素尺寸由DPI决定
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToPNG(page *PageContent, writer io.Writer) error {
	img, err := r.RenderToImage(page)
	if err != nil {
		return err
	}
	return png.Encode(writer, img)
}

// RenderToJPEG 渲染为白底JPEG，像素尺寸由DPI决定
// 入参: page 页面内容, writer 输出流, options 编码选项，nil使用标准库默认值
// 返回: error 错误信息
func (r *Renderer) RenderToJPEG(page *PageContent, writer io.Writer, options *jpeg.Options) error {
	img, err := r.RenderToImage(page)
	if err != nil {
		return err
	}
	return jpeg.Encode(writer, fillWhiteBackground(img), options)
}

// fillWhiteBackground 填充白色背景，复用当前导出图片的RGBA像素
// 入参: img 图片对象
// 返回: image.Image 图片对象
func fillWhiteBackground(img image.Image) image.Image {
	bounds := img.Bounds()
	if rgba, ok := img.(*image.RGBA); ok {
		for y := 0; y < bounds.Dy(); y++ {
			row := rgba.Pix[y*rgba.Stride : y*rgba.Stride+bounds.Dx()*4]
			for i := 0; i < len(row); i += 4 {
				if alpha := 255 - row[i+3]; alpha != 0 {
					row[i] += alpha
					row[i+1] += alpha
					row[i+2] += alpha
					row[i+3] = 255
				}
			}
		}
		return rgba
	}
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, image.White, image.Point{}, draw.Src)
	draw.Draw(dst, bounds, img, bounds.Min, draw.Over)
	return dst
}

// RenderToSVG 渲染为SVG
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToSVG(page *PageContent, writer io.Writer) error {
	c, err := r.renderPage(page)
	if err != nil {
		return err
	}
	buffer := bufio.NewWriter(writer)
	renderer := svg.New(buffer, c.W, c.H, nil)
	c.RenderTo(renderer)
	if err := renderer.Close(); err != nil {
		return err
	}
	return buffer.Flush()
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
	return r.renderPDFPages(pages, writer, nil)
}

// RenderToEPS 渲染为EPS
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToEPS(page *PageContent, writer io.Writer) error {
	c, err := r.renderPage(page)
	if err != nil {
		return err
	}
	options := ps.DefaultOptions
	options.Format = ps.EncapsulatedPostScript
	buffer := bufio.NewWriter(writer)
	renderer := ps.New(buffer, c.W, c.H, &options)
	c.RenderTo(renderer)
	if err := renderer.Close(); err != nil {
		return err
	}
	return buffer.Flush()
}

// RenderToMultiPagePDF 将文档页面导出为多页PDF
// 入参: writer 输出流, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 错误信息
func (r *Renderer) RenderToMultiPagePDF(writer io.Writer, indices ...int) error {
	doc, err := r.Reader.Doc()
	if err != nil {
		return err
	}
	indices, err = exportPageIndices(len(doc.Pages.Page), indices)
	if err != nil {
		return err
	}
	if err := r.exportProgress(0, len(indices)); err != nil {
		return err
	}
	pages := make([]pdfPage, len(indices))
	for i, index := range indices {
		page, err := r.Reader.PageContent(doc.Pages.Page[index])
		if err != nil {
			return fmt.Errorf("failed to read page %d: %w", index+1, err)
		}
		box, err := r.GetPageBox(page)
		if err != nil {
			return fmt.Errorf("failed to read page %d area: %w", index+1, err)
		}
		pages[i] = pdfPage{Content: page, Box: box}
	}
	return r.renderPDFPages(pages, writer, r.OnExportProgress)
}

// RenderPagesToPDF 按指定顺序导出已解析的页面，复用调用方的页面缓存
// 入参: contents 页面内容列表, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderPagesToPDF(contents []*PageContent, writer io.Writer) error {
	if len(contents) == 0 {
		return fmt.Errorf("no pages found")
	}
	if err := r.exportProgress(0, len(contents)); err != nil {
		return err
	}
	pages := make([]pdfPage, len(contents))
	for i, page := range contents {
		box, err := r.GetPageBox(page)
		if err != nil {
			return fmt.Errorf("failed to read page %d area: %w", i+1, err)
		}
		pages[i] = pdfPage{Content: page, Box: box}
	}
	return r.renderPDFPages(pages, writer, r.OnExportProgress)
}

// renderPDFPages 渲染页面并复用调用方的字节缓冲
// 入参: pages 页面列表, writer 输出流, progress 页面处理进度
// 返回: error 错误信息
func (r *Renderer) renderPDFPages(pages []pdfPage, writer io.Writer, progress func(int, int) error) error {
	navigation := newPDFNavigation(r, r.Reader.doc, pages)
	buf, direct := writer.(*bytes.Buffer)
	if !direct {
		buf = &bytes.Buffer{}
	}
	start := buf.Len()
	p := pdf.New(buf, pages[0].Box.W, pages[0].Box.H, nil)
	renderer := &pdfRenderer{PDF: p, glyphPaths: make(map[*canvas.Path]*canvas.Path)}
	var info DocInfo
	if docInfo, err := r.Reader.DocInfo(); err == nil {
		info = *docInfo
	}
	p.SetInfo(info.Title, info.Subject, "", info.Author, "xiaoqidun/ofdgo")
	for i, page := range pages {
		if i > 0 {
			p.NewPage(page.Box.W, page.Box.H)
		}
		navigation.apply(p, i)
		if err := r.renderPageToContext(canvas.NewContext(renderer), page.Content, true); err != nil {
			buf.Truncate(start)
			return fmt.Errorf("failed to render page %d: %w", i+1, err)
		}
		pages[i].Content = nil
		if progress != nil {
			if err := progress(i+1, len(pages)); err != nil {
				buf.Truncate(start)
				return err
			}
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
