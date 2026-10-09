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
	"bufio"
	"bytes"
	"fmt"
	"image"
	"io"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/tdewolff/canvas/renderers/ps"
	"github.com/tdewolff/canvas/renderers/svg"
	"github.com/xiaoqidun/pdfgo"
)

// canvasEPSWriter 在EPS头部写入本库制作软件，不改动绘图内容
type canvasEPSWriter struct {
	io.Writer
}

// RenderSVG 使用canvas输出SVG，按模式保留资源和对象分组
// 入参: r 渲染器, page 页面内容, writer 输出流, mode 资源模式
// 返回: SVGResources 外部资源, error 错误信息
func (CanvasBackend) RenderSVG(r *Renderer, page *PageContent, writer io.Writer, mode SVGMode) (SVGResources, error) {
	switch mode {
	case SVGExternalFonts:
		return r.renderSVGResources(page, writer, false, false)
	case SVGExternalResources:
		return r.renderSVGResources(page, writer, true, false)
	case SVGObjects:
		return r.renderSVGResources(page, writer, true, true)
	case SVGEmbedded:
		c, err := r.renderCanvasPage(page)
		if err != nil {
			return SVGResources{}, err
		}
		buffer := bufio.NewWriter(writer)
		renderer := &svgResourceRenderer{SVG: svg.New(buffer, c.W, c.H, nil), renderer: r, writer: buffer, embeddedFonts: true}
		io.WriteString(buffer, svgCreatorMetadata)
		io.WriteString(buffer, `<g style="image-orientation:none">`)
		c.RenderTo(renderer)
		if renderer.err != nil {
			return SVGResources{}, renderer.err
		}
		io.WriteString(buffer, `</g>`)
		if err := renderer.Close(); err != nil {
			return SVGResources{}, err
		}
		return SVGResources{}, buffer.Flush()
	default:
		return SVGResources{}, fmt.Errorf("unsupported SVG mode %d", mode)
	}
}

// RenderEPS 使用canvas输出单页EPS
// 入参: r 渲染器, page 页面内容, writer 输出流
// 返回: error 错误信息
func (CanvasBackend) RenderEPS(r *Renderer, page *PageContent, writer io.Writer) error {
	c, err := r.renderCanvasPage(page)
	if err != nil {
		return err
	}
	options := ps.DefaultOptions
	options.Format = ps.EncapsulatedPostScript
	buffer := bufio.NewWriter(writer)
	renderer := ps.New(canvasEPSWriter{buffer}, c.W, c.H, &options)
	if r.Compression.Mode == CompressionUnchanged {
		c.RenderTo(renderer)
	} else {
		optimized := &epsImageRenderer{PS: renderer, writer: buffer, options: r.Compression, ctx: r.outputContext()}
		c.RenderTo(optimized)
		if optimized.err != nil {
			return optimized.err
		}
	}
	if err := renderer.Close(); err != nil {
		return err
	}
	return buffer.Flush()
}

// Write 写入EPS片段并替换独立的制作软件声明
// 入参: data EPS片段
// 返回: int 已处理字节数, error 写入错误
func (w canvasEPSWriter) Write(data []byte) (int, error) {
	if bytes.Equal(data, []byte("%%Creator: tdewolff/canvas\n")) {
		if _, err := io.WriteString(w.Writer, "%%Creator: "+ofdCreator+"\n"); err != nil {
			return 0, err
		}
		return len(data), nil
	}
	return w.Writer.Write(data)
}

// RenderPDF 使用canvas输出PDF，保留导航与文字并复用输出缓冲
// 入参: r 渲染器, pages 页面列表, writer 输出流, progress 页面处理进度
// 返回: error 错误信息
func (CanvasBackend) RenderPDF(r *Renderer, pages []RenderDocumentPage, writer io.Writer, progress func(int, int) error) error {
	if len(pages) == 0 {
		return fmt.Errorf("no pages found")
	}
	navigation, err := newPDFNavigation(r, r.Reader.doc, pages)
	if err != nil {
		return err
	}
	buf, direct := writer.(*bytes.Buffer)
	if r.Compression.Mode != CompressionUnchanged {
		direct = false
	}
	if !direct {
		buf = &bytes.Buffer{}
	}
	start := buf.Len()
	p := pdf.New(buf, pages[0].Box.W, pages[0].Box.H, nil)
	renderer := &pdfRenderer{PDF: p, glyphPaths: make(map[*canvas.Path]*canvas.Path), images: make([][]image.Image, 1), navigation: navigation}
	defer func() {
		if renderer.imageError != nil {
			r.canvasState().images = renderCache[*EncodedImage, image.Image]{limit: imageCacheLimit}
		}
	}()
	var info DocInfo
	if docInfo, err := r.Reader.DocInfo(); err == nil {
		info = *docInfo
	}
	p.SetInfo(info.Title, info.Subject, "", info.Author, ofdCreator)
	for i, page := range pages {
		if i > 0 {
			p.NewPage(page.Box.W, page.Box.H)
			renderer.images = append(renderer.images, nil)
		}
		navigation.apply(p, i)
		if err := r.renderCanvasPageToContext(canvas.NewContext(renderer), page.Content, !r.TransparentBackground); err != nil {
			buf.Truncate(start)
			return fmt.Errorf("failed to render page %d: %w", i+1, err)
		}
		if renderer.imageError != nil {
			buf.Truncate(start)
			return fmt.Errorf("failed to render page %d: %w", i+1, renderer.imageError)
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
	options := pdfgo.OptimizeOptions{Compression: r.Compression}
	if r.Compression.Mode != CompressionUnchanged && progress != nil {
		options.OnProgress = func(_ string, _, _ int) error {
			return progress(len(pages), len(pages))
		}
	}
	if direct {
		if !renderer.exactImages && !navigation.exactLinks {
			if err := r.outputContext().Err(); err != nil {
				buf.Truncate(start)
				return err
			}
			return nil
		}
		var result bytes.Buffer
		err := renderer.writePDF(r.outputContext(), data, &result, options)
		buf.Truncate(start)
		if err != nil {
			return err
		}
		_, err = buf.Write(result.Bytes())
		return err
	}
	return renderer.writePDF(r.outputContext(), data, writer, options)
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
	dst := []byte("/Producer(" + ofdCreator + ")")
	copy(data[idx:idx+len(old)], dst)
	return data
}
