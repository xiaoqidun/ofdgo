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
	"io"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/tdewolff/canvas/renderers/ps"
	"github.com/tdewolff/canvas/renderers/svg"
)

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
		renderer := svg.New(buffer, c.W, c.H, nil)
		io.WriteString(buffer, svgCreatorMetadata)
		c.RenderTo(renderer)
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
	c.RenderTo(renderer)
	if err := renderer.Close(); err != nil {
		return err
	}
	return buffer.Flush()
}

// canvasEPSWriter 在EPS头部写入本库制作软件，不改动绘图内容
type canvasEPSWriter struct {
	io.Writer
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
	p.SetInfo(info.Title, info.Subject, "", info.Author, ofdCreator)
	for i, page := range pages {
		if i > 0 {
			p.NewPage(page.Box.W, page.Box.H)
		}
		navigation.apply(p, i)
		if err := r.renderCanvasPageToContext(canvas.NewContext(renderer), page.Content, !r.TransparentBackground); err != nil {
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
	_, err = writer.Write(data)
	return err
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
