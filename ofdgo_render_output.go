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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/xiaoqidun/pdfgo"
	"golang.org/x/image/draw"
)

// xmpNamespace 表示XMP元数据命名空间
const xmpNamespace = "http://ns.adobe.com/xap/1.0/"

// creatorXMP 记录独立导出文件的制作软件
const creatorXMP = `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:xmp="` + xmpNamespace + `"><xmp:CreatorTool>` + ofdCreator + `</xmp:CreatorTool></rdf:Description></rdf:RDF>`

// imageMetadataWriter 在图像固定头之后插入元数据，不缓存图像主体
type imageMetadataWriter struct {
	writer io.Writer
	skip   int
	data   []byte
}

// fillWhiteBackground 填充白色背景，复用当前导出图片的RGBA像素
// 入参: img 图片对象
// 返回: image.Image 图片对象
func fillWhiteBackground(img image.Image) image.Image {
	bounds := img.Bounds()
	if rgba, ok := img.(*image.RGBA); ok {
		width := bounds.Dx() * 4
		for y := 0; y < bounds.Dy(); y++ {
			start := rgba.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := rgba.Pix[start : start+width]
			for i := 0; i < len(row); i += 4 {
				if alpha := 255 - row[i+3]; alpha != 0 {
					row[i] = uint8(min(255, int(row[i])+int(alpha)))
					row[i+1] = uint8(min(255, int(row[i+1])+int(alpha)))
					row[i+2] = uint8(min(255, int(row[i+2])+int(alpha)))
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

// RenderTo 按格式渲染单页，JPEG使用95画质
// 入参: page 页面内容, writer 输出流, format svg、pdf、eps、png、jpg、jpeg或txt
// 返回: error 错误信息
func (r *Renderer) RenderTo(page *PageContent, writer io.Writer, format string) error {
	return r.RenderToContext(context.Background(), page, writer, format)
}

// RenderToContext 按取消上下文与格式渲染单页，JPEG使用95画质
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容, writer 输出流, format svg、pdf、eps、png、jpg、jpeg或txt
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToContext(ctx context.Context, page *PageContent, writer io.Writer, format string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, render, err := r.outputRenderer(format)
	if err != nil {
		return err
	}
	return render(ctx, page, writer)
}

// RenderToZIP 逐页渲染并写入ZIP，文件编号按总页数补零，出错时调用方应丢弃输出
// TXT只解析文字提取所需的图元
// 入参: writer 输出流, format svg、pdf、eps、png、jpg、jpeg或txt, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 错误信息
func (r *Renderer) RenderToZIP(writer io.Writer, format string, indices ...int) error {
	return r.RenderToZIPContext(context.Background(), writer, format, indices...)
}

// RenderToZIPContext 按取消上下文逐页渲染并写入ZIP，文件编号按总页数补零
// 入参: ctx 取消上下文，nil使用后台上下文, writer 输出流, format svg、pdf、eps、png、jpg、jpeg或txt, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToZIPContext(ctx context.Context, writer io.Writer, format string, indices ...int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.reader != nil {
		ctx = r.reader.resources.context(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prevCtx := r.pageContext
	r.pageContext = ctx
	defer func() { r.pageContext = prevCtx }()
	extension, render, err := r.outputRenderer(format)
	if err != nil {
		return err
	}
	if err := r.compression.Validate(); err != nil {
		return err
	}
	doc, err := r.reader.docContext(ctx)
	if err != nil {
		return err
	}
	count := len(doc.Pages.Page)
	indices, err = exportPageIndices(count, indices)
	if err != nil {
		return err
	}
	method := zip.Deflate
	if extension == "png" || extension == "jpg" {
		method = zip.Store
	}
	width := len(strconv.Itoa(count))
	if err := r.exportProgress(0, len(indices)); err != nil {
		return err
	}
	workers := min(len(indices), conversionWorkers(ctx))
	if workers > 1 {
		return r.renderToZIPParallel(ctx, writer, doc, format, extension, method, width, indices, workers)
	}
	var countWritten int64
	archive := zip.NewWriter(convertWriter{ctx: ctx, writer: writer, count: &countWritten})
	configureArchive(archive, r.compression)
	for i, index := range indices {
		page, err := r.reader.readPageContent(ctx, doc.Pages.Page[index], extension == "txt", false)
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
		if err := render(ctx, page, entry); err != nil {
			return fmt.Errorf("failed to export page %d: %w", index+1, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.exportProgress(i+1, len(indices)); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return ctx.Err()
}

// RenderToText 导出页面原文为UTF-8文本，以换行分隔非空文本对象
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToText(page *PageContent, writer io.Writer) error {
	return r.RenderToTextContext(context.Background(), page, writer)
}

// RenderToTextContext 按取消上下文导出页面原文为UTF-8文本
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容, writer 输出流
// 返回: error 提取、写入或取消错误
func (r *Renderer) RenderToTextContext(ctx context.Context, page *PageContent, writer io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var canvasCompiler bool
	switch r.backends.Compiler.(type) {
	case DefaultBackend, *DefaultBackend:
		canvasCompiler = true
	}
	prevContentOnly := r.textContentOnly
	r.textContentOnly = canvasCompiler
	defer func() { r.textContentOnly = prevContentOnly }()
	text, err := r.PageTextContext(ctx, page)
	if err != nil {
		return err
	}
	var written int64
	writer = convertWriter{ctx: ctx, writer: writer, count: &written}
	_, err = text.WriteTo(writer)
	if err != nil {
		return err
	}
	return ctx.Err()
}

// RenderToMultiPageText 逐页导出UTF-8原文，以换行分隔非空页面，出错时调用方应丢弃输出
// 入参: writer 输出流, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 错误信息
func (r *Renderer) RenderToMultiPageText(writer io.Writer, indices ...int) error {
	return r.RenderToMultiPageTextContext(context.Background(), writer, indices...)
}

// RenderToMultiPageTextContext 按取消上下文逐页导出UTF-8原文
// 入参: ctx 取消上下文，nil使用后台上下文, writer 输出流, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 提取、写入或取消错误
func (r *Renderer) RenderToMultiPageTextContext(ctx context.Context, writer io.Writer, indices ...int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var canvasCompiler bool
	switch r.backends.Compiler.(type) {
	case DefaultBackend, *DefaultBackend:
		canvasCompiler = true
	}
	prevCtx, prevContentOnly := r.pageContext, r.textContentOnly
	r.pageContext, r.textContentOnly = ctx, canvasCompiler
	defer func() { r.pageContext, r.textContentOnly = prevCtx, prevContentOnly }()
	doc, err := r.reader.docContext(ctx)
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
	var count int64
	writer = convertWriter{ctx: ctx, writer: writer, count: &count}
	written := false
	for i, index := range indices {
		text, err := r.PageTextByIndexContext(ctx, index)
		if err != nil {
			return fmt.Errorf("failed to extract page %d text: %w", index+1, err)
		}
		n, err := text.writeTo(writer, written)
		if err != nil {
			return err
		}
		written = written || n > 0
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.exportProgress(i+1, len(indices)); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// RenderToImage 渲染为光栅图
// 入参: page 页面内容
// 返回: image.Image 图像对象, error 错误信息
func (r *Renderer) RenderToImage(page *PageContent) (image.Image, error) {
	return r.RenderToImageContext(context.Background(), page)
}

// RenderToImageContext 按取消上下文渲染为光栅图
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容
// 返回: image.Image 图像对象, error 渲染或取消错误
func (r *Renderer) RenderToImageContext(ctx context.Context, page *PageContent) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.backends.Raster == nil {
		return nil, fmt.Errorf("raster scene: %w", ErrBackendUnavailable)
	}
	_, hasReuse := r.backends.Raster.(RasterSceneReuseBackend)
	_, hasScene := r.backends.Raster.(RasterSceneBackend)
	if !hasReuse && !hasScene {
		compiled, err := r.CompilePageContext(ctx, page)
		if err != nil {
			return nil, err
		}
		if _, _, err := compiled.PixelSize(); err != nil {
			return nil, err
		}
		img, err := r.backends.Raster.Render(ctx, compiled)
		if cancelErr := ctx.Err(); cancelErr != nil {
			return nil, cancelErr
		}
		return img, err
	}
	scene, err := r.PreparePageContext(ctx, page)
	if err != nil {
		return nil, err
	}
	img, err := scene.Render()
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	return img, err
}

// RenderToPNG 渲染为PNG，像素尺寸由DPI决定
// 入参: page 页面内容, writer 输出流
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToPNG(page *PageContent, writer io.Writer) error {
	return r.RenderToPNGContext(context.Background(), page, writer)
}

// RenderToPNGContext 按取消上下文渲染为PNG，像素尺寸由DPI决定
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容, writer 输出流
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToPNGContext(ctx context.Context, page *PageContent, writer io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.compression.Validate(); err != nil {
		return err
	}
	var written int64
	writer = convertWriter{ctx: ctx, writer: writer, count: &written}
	img, err := r.RenderToImageContext(ctx, page)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	text := []byte("Software\x00" + ofdCreator)
	chunk := make([]byte, len(text)+12)
	binary.BigEndian.PutUint32(chunk, uint32(len(text)))
	copy(chunk[4:], "tEXt")
	copy(chunk[8:], text)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	if r.compression.Mode == CompressionUnchanged {
		return png.Encode(&imageMetadataWriter{writer: writer, skip: 33, data: chunk}, img)
	}
	optimized, err := pdfgo.OptimizeImageFromPixels(ctx, img, r.compression.pdfOptions())
	if errors.Is(err, io.ErrShortBuffer) {
		return png.Encode(&imageMetadataWriter{writer: writer, skip: 33, data: chunk}, img)
	}
	if err != nil {
		return err
	}
	_, err = io.Copy(&imageMetadataWriter{writer: writer, skip: 33, data: chunk}, bytes.NewReader(optimized))
	return err
}

// RenderToJPEG 渲染为白底JPEG，像素尺寸由DPI决定
// 入参: page 页面内容, writer 输出流, options 编码选项，nil使用标准库默认值
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToJPEG(page *PageContent, writer io.Writer, options *jpeg.Options) error {
	return r.RenderToJPEGContext(context.Background(), page, writer, options)
}

// RenderToJPEGContext 按取消上下文渲染为白底JPEG，像素尺寸由DPI决定
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容, writer 输出流, options 编码选项，nil使用标准库默认值
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToJPEGContext(ctx context.Context, page *PageContent, writer io.Writer, options *jpeg.Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.compression.Validate(); err != nil {
		return err
	}
	var written int64
	writer = convertWriter{ctx: ctx, writer: writer, count: &written}
	img, err := r.RenderToImageContext(ctx, page)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := []byte(xmpNamespace + "\x00" + creatorXMP)
	segment := make([]byte, len(metadata)+4)
	segment[0], segment[1] = 0xff, 0xe1
	binary.BigEndian.PutUint16(segment[2:], uint16(len(metadata)+2))
	copy(segment[4:], metadata)
	if r.compression.Mode == CompressionUnchanged {
		return jpeg.Encode(&imageMetadataWriter{writer: writer, skip: 2, data: segment}, fillWhiteBackground(img), options)
	}
	if r.compression.Mode == CompressionLossy {
		quality := jpeg.DefaultQuality
		if options != nil {
			quality = max(1, min(100, options.Quality))
		}
		options = &jpeg.Options{Quality: min(quality, r.compression.ImageQuality())}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&imageMetadataWriter{writer: &encoded, skip: 2, data: segment}, fillWhiteBackground(img), options); err != nil {
		return err
	}
	optimized, err := pdfgo.OptimizeJPEG(ctx, encoded.Bytes())
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, bytes.NewReader(optimized))
	return err
}

// RenderToSVG 渲染为SVG
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToSVG(page *PageContent, writer io.Writer) error {
	return r.RenderToSVGContext(context.Background(), page, writer)
}

// RenderToSVGContext 按取消上下文渲染为SVG
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容, writer 输出流
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToSVGContext(ctx context.Context, page *PageContent, writer io.Writer) error {
	_, err := r.renderSVGContext(ctx, page, writer, SVGEmbedded)
	return err
}

// RenderToPDF 渲染为PDF
// 入参: page 页面内容, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderToPDF(page *PageContent, writer io.Writer) error {
	return r.RenderToPDFContext(context.Background(), page, writer)
}

// RenderToPDFContext 按取消上下文渲染为PDF
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容, writer 输出流
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToPDFContext(ctx context.Context, page *PageContent, writer io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	box, err := r.GetPageBoxContext(ctx, page)
	if err != nil {
		return err
	}
	pages := []RenderDocumentPage{{Content: page, Box: box}}
	return r.renderPDFPagesContext(ctx, pages, writer, nil)
}

// RenderToEPS 渲染为EPS
// 入参: page 页面内容, writer 输出流
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToEPS(page *PageContent, writer io.Writer) error {
	return r.RenderToEPSContext(context.Background(), page, writer)
}

// RenderToEPSContext 按取消上下文渲染为EPS
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容, writer 输出流
// 返回: error 渲染、写入或取消错误
func (r *Renderer) RenderToEPSContext(ctx context.Context, page *PageContent, writer io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.compression.Validate(); err != nil {
		return err
	}
	if r.backends.EPS == nil {
		return fmt.Errorf("eps: %w", ErrBackendUnavailable)
	}
	prevCtx := r.pageContext
	r.pageContext = ctx
	defer func() { r.pageContext = prevCtx }()
	var written int64
	writer = convertWriter{ctx: ctx, writer: writer, count: &written}
	if err := r.backends.EPS.RenderEPS(ctx, r, page, writer); err != nil {
		return err
	}
	return ctx.Err()
}

// RenderToMultiPagePDF 将文档页面导出为多页PDF，仅预读页面区域和资源索引
// 入参: writer 输出流, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 错误信息
func (r *Renderer) RenderToMultiPagePDF(writer io.Writer, indices ...int) error {
	return r.RenderToMultiPagePDFContext(context.Background(), writer, indices...)
}

// RenderToMultiPagePDFContext 按取消上下文将文档页面导出为多页PDF
// 入参: ctx 取消上下文，nil使用后台上下文, writer 输出流, indices 零基页面索引，省略则全部，按原页序去重
// 返回: error 导出或取消错误
func (r *Renderer) RenderToMultiPagePDFContext(ctx context.Context, writer io.Writer, indices ...int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	doc, err := r.reader.docContext(ctx)
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
	pages := make([]RenderDocumentPage, len(indices))
	for i, index := range indices {
		ref := doc.Pages.Page[index]
		page, err := r.reader.readPageHeaderContext(ctx, ref)
		if err != nil {
			return fmt.Errorf("failed to read page %d: %w", index+1, err)
		}
		box, err := r.GetPageBoxContext(ctx, &page)
		if err != nil {
			return fmt.Errorf("failed to read page %d area: %w", index+1, err)
		}
		pages[i] = RenderDocumentPage{Source: ref, Box: box}
	}
	return r.renderPDFPagesContext(ctx, pages, writer, r.OnExportProgress)
}

// RenderPagesToPDF 按指定顺序导出已解析的页面，复用调用方的页面缓存
// 入参: contents 页面内容列表, writer 输出流
// 返回: error 错误信息
func (r *Renderer) RenderPagesToPDF(contents []*PageContent, writer io.Writer) error {
	return r.RenderPagesToPDFContext(context.Background(), contents, writer)
}

// RenderPagesToPDFContext 按取消上下文导出已解析的页面列表
// 入参: ctx 取消上下文，nil使用后台上下文, contents 页面内容列表, writer 输出流
// 返回: error 导出或取消错误
func (r *Renderer) RenderPagesToPDFContext(ctx context.Context, contents []*PageContent, writer io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(contents) == 0 {
		return fmt.Errorf("no pages found")
	}
	if err := r.exportProgress(0, len(contents)); err != nil {
		return err
	}
	pages := make([]RenderDocumentPage, len(contents))
	for i, page := range contents {
		if err := ctx.Err(); err != nil {
			return err
		}
		box, err := r.GetPageBoxContext(ctx, page)
		if err != nil {
			return fmt.Errorf("failed to read page %d area: %w", i+1, err)
		}
		pages[i] = RenderDocumentPage{Content: page, Box: box}
	}
	return r.renderPDFPagesContext(ctx, pages, writer, r.OnExportProgress)
}

// Write 交付图像数据并在头部边界写入元数据
// 入参: data 编码后的图像片段
// 返回: int 已处理字节数, error 写入错误
func (w *imageMetadataWriter) Write(data []byte) (int, error) {
	n := 0
	if w.skip > 0 {
		size := min(w.skip, len(data))
		written, err := w.writer.Write(data[:size])
		n, w.skip = written, w.skip-written
		data = data[written:]
		if err != nil {
			return n, err
		}
		if written != size {
			return n, io.ErrShortWrite
		}
	}
	if w.skip == 0 && w.data != nil {
		written, err := w.writer.Write(w.data)
		if err != nil {
			return n, err
		}
		if written != len(w.data) {
			return n, io.ErrShortWrite
		}
		w.data = nil
	}
	if len(data) == 0 {
		return n, nil
	}
	written, err := w.writer.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	return n + written, err
}

// renderToZIPParallel 使用有界多核Renderer.Fork流水线并行渲染页面并按页序写入ZIP
// 入参: ctx 取消上下文, writer 输出流, doc 文档对象, format 原始格式, extension 扩展名, method ZIP压缩方法, width 页码补零宽度, indices 零基页索引, workers 并行度
// 返回: error 渲染、写入或取消错误
func (r *Renderer) renderToZIPParallel(ctx context.Context, writer io.Writer, doc *Document, format, extension string, method uint16, width int, indices []int, workers int) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type zipWorker struct {
		renderer *Renderer
		render   func(context.Context, *PageContent, io.Writer) error
	}
	poolWorkers := make([]zipWorker, 0, workers)
	defer func() {
		for _, w := range poolWorkers {
			if w.renderer != nil && w.renderer.reader != nil {
				_ = w.renderer.reader.Close()
			}
		}
	}()
	for range workers {
		clone, err := r.Fork(runCtx)
		if err != nil {
			return err
		}
		_, fn, err := clone.outputRenderer(format)
		if err != nil {
			if clone.reader != nil {
				_ = clone.reader.Close()
			}
			return err
		}
		poolWorkers = append(poolWorkers, zipWorker{renderer: clone, render: fn})
	}
	pool, _ := ctx.Value(conversionPoolKey{}).(*ConversionPool)
	raster := extension == "png" || extension == "jpg"
	window := workers * 2
	jobs := make(chan *convertPageTask, window)
	inflight := make(chan *convertPageTask, window)
	var wg sync.WaitGroup
	for _, worker := range poolWorkers {
		wg.Add(1)
		go func(w zipWorker) {
			defer wg.Done()
			for task := range jobs {
				if err := runCtx.Err(); err != nil {
					task.err = err
					close(task.done)
					continue
				}
				page, readErr := w.renderer.reader.readPageContent(runCtx, doc.Pages.Page[task.index], extension == "txt", false)
				if readErr != nil {
					task.err = fmt.Errorf("failed to read page %d: %w", task.index+1, readErr)
					close(task.done)
					continue
				}
				var estimated int64
				acquired := false
				if pool != nil && raster {
					estimated = estimateRasterPageBytes(w.renderer, page)
					if err := pool.acquire(runCtx, true, estimated); err != nil {
						task.err = err
						close(task.done)
						continue
					}
					acquired = true
				}
				if renderErr := w.render(runCtx, page, &task.buf); renderErr != nil {
					task.err = fmt.Errorf("failed to export page %d: %w", task.index+1, renderErr)
				}
				if acquired {
					pool.release(true, estimated)
				}
				close(task.done)
			}
		}(worker)
	}
	var producerWG sync.WaitGroup
	producerWG.Add(1)
	go func() {
		defer producerWG.Done()
		defer close(jobs)
		defer close(inflight)
		for _, index := range indices {
			task := &convertPageTask{index: index, done: make(chan struct{})}
			select {
			case <-runCtx.Done():
				return
			case inflight <- task:
			}
			select {
			case <-runCtx.Done():
				task.err = runCtx.Err()
				close(task.done)
				return
			case jobs <- task:
			}
		}
	}()
	finish := func() {
		cancel()
		producerWG.Wait()
		wg.Wait()
	}
	var countWritten int64
	archive := zip.NewWriter(convertWriter{ctx: ctx, writer: writer, count: &countWritten})
	configureArchive(archive, r.compression)
	for i := range indices {
		task, ok := <-inflight
		if !ok {
			finish()
			return runCtx.Err()
		}
		select {
		case <-task.done:
		case <-ctx.Done():
			finish()
			return ctx.Err()
		}
		if task.err != nil {
			finish()
			return task.err
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{
			Name:   fmt.Sprintf("%0*d.%s", width, task.index+1, extension),
			Method: method,
		})
		if err != nil {
			finish()
			return err
		}
		if _, err := io.Copy(entry, bytes.NewReader(task.buf.Bytes())); err != nil {
			finish()
			return err
		}
		task.buf.Reset()
		if err := ctx.Err(); err != nil {
			finish()
			return err
		}
		if err := r.exportProgress(i+1, len(indices)); err != nil {
			finish()
			return err
		}
	}
	finish()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return ctx.Err()
}

// outputRenderer 获取格式扩展名和渲染方法
// 入参: format 输出格式
// 返回: string 扩展名, func 渲染方法, error 错误信息
func (r *Renderer) outputRenderer(format string) (string, func(context.Context, *PageContent, io.Writer) error, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "svg":
		return "svg", r.RenderToSVGContext, nil
	case "pdf":
		return "pdf", r.RenderToPDFContext, nil
	case "txt":
		return "txt", r.RenderToTextContext, nil
	case "eps":
		return "eps", r.RenderToEPSContext, nil
	case "png":
		return "png", r.RenderToPNGContext, nil
	case "jpg", "jpeg":
		return "jpg", func(ctx context.Context, page *PageContent, writer io.Writer) error {
			return r.RenderToJPEGContext(ctx, page, writer, &jpeg.Options{Quality: 95})
		}, nil
	default:
		return "", nil, fmt.Errorf("unsupported output format %s", format)
	}
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

// renderPDFPagesContext 按取消上下文将已解析页面交给指定PDF后端
// 入参: ctx 取消上下文, pages 页面列表, writer 输出流, progress 页面处理进度
// 返回: error 错误信息
func (r *Renderer) renderPDFPagesContext(ctx context.Context, pages []RenderDocumentPage, writer io.Writer, progress func(int, int) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.compression.Validate(); err != nil {
		return err
	}
	if r.backends.PDF == nil {
		return fmt.Errorf("pdf: %w", ErrBackendUnavailable)
	}
	prevCtx := r.pageContext
	r.pageContext = ctx
	defer func() { r.pageContext = prevCtx }()
	if err := r.backends.PDF.RenderPDF(ctx, r, pages, writer, progress); err != nil {
		return err
	}
	return ctx.Err()
}
