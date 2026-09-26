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
	"fmt"
	"io"
	"strings"
)

// OutputFormat 描述文档输出格式，Paged表示逐页文件，Raster表示受DPI控制的位图
type OutputFormat struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	Extension string `json:"extension"`
	MIME      string `json:"mime"`
	Paged     bool   `json:"paged,omitempty"`
	Raster    bool   `json:"raster,omitempty"`
}

// ConvertProgress 回报open、pages、convert、prepare、write、write.*及export阶段，Total为0表示总量未知
type ConvertProgress struct {
	Stage     string `json:"stage"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
}

// ConvertOptions 设置文档转换格式、页码、阅读和渲染选项
// Input为空时识别源格式，Format为输出格式，Pages使用零基索引，nil表示全部页面
// PageRange使用一基页码表达式，与Pages互斥，空字符串表示全部页面
// SkipUnchanged在源格式与目标相同且未选页时不写出数据，不校验或重新生成源文档
// PDF先转换为OFD对象再输出，PDF选项中的进度回调仍会调用
// RendererOptions同时用于PDF局部合成和输出，在PDF.RendererOptions之后应用
// Backends统一配置转换与输出，优先于PDF和RendererOptions中的后端设置
// PageOutput仅用于逐页格式，同步调用export写出一页并自行处理提交或回滚；此时output可为nil
// 未提供PageOutput时，逐页格式打包ZIP；所有回调返回错误均会停止转换
type ConvertOptions struct {
	Input           string
	Format          string
	Pages           []int
	PageRange       string
	SkipUnchanged   bool
	Backends        *RenderBackends
	ReaderOptions   []ReaderOption
	RendererOptions []RendererOption
	PDF             PDFImportOptions
	OnProgress      func(ConvertProgress) error
	PageOutput      func(index int, export func(io.Writer) error) error
}

// ConvertReport 汇总源格式、输出格式、页数、字节数及PDF转换诊断
// Unchanged表示无需转换或完整OFD原样复制，Bytes仅统计实际输出，不包含调用方额外封装
type ConvertReport struct {
	Input     string          `json:"input"`
	Format    OutputFormat    `json:"format"`
	Pages     int             `json:"pages"`
	Bytes     int64           `json:"bytes"`
	Unchanged bool            `json:"unchanged,omitempty"`
	PDF       PDFImportReport `json:"pdf"`
}

// convertReader 将取消上下文传递到随机读取
type convertReader struct {
	context context.Context
	source  io.ReaderAt
}

// convertWriter 将取消上下文传递到写入并统计实际字节数
type convertWriter struct {
	context context.Context
	writer  io.Writer
	count   *int64
}

// OutputFormats 返回独立的输出格式列表，不依赖WebUI或具体渲染后端
// 返回: []OutputFormat 输出格式列表
func OutputFormats() []OutputFormat {
	return []OutputFormat{
		{Value: "ofd", Label: "OFD", Extension: "ofd", MIME: "application/ofd"},
		{Value: "pdf", Label: "PDF", Extension: "pdf", MIME: "application/pdf"},
		{Value: "svg", Label: "SVG", Extension: "svg", MIME: "image/svg+xml", Paged: true},
		{Value: "eps", Label: "EPS", Extension: "eps", MIME: "application/postscript", Paged: true},
		{Value: "png", Label: "PNG", Extension: "png", MIME: "image/png", Paged: true, Raster: true},
		{Value: "jpg", Label: "JPG", Extension: "jpg", MIME: "image/jpeg", Paged: true, Raster: true},
		{Value: "txt", Label: "TXT", Extension: "txt", MIME: "text/plain"},
	}
}

// Convert 将PDF或OFD转换为指定格式，不关闭调用方的输入和输出
// 完整OFD另存保持源字节，包括制作软件、签名及加密；选页和跨格式输出重新生成文档
// 输入按需随机读取，输出分块写出；解析对象及后端可能缓存资源，不保证恒定内存
// 出错时报告保留已有诊断，调用方应丢弃未完成输出；只有返回nil才表示转换成功
// 入参: ctx 取消上下文, source 源数据, size 源字节数, output 输出流, options 转换选项
// 返回: ConvertReport 转换报告, error 错误信息
func Convert(ctx context.Context, source io.ReaderAt, size int64, output io.Writer, options ConvertOptions) (report ConvertReport, err error) {
	if err = ctx.Err(); err != nil {
		return report, err
	}
	format := strings.ToLower(strings.TrimSpace(options.Format))
	if format == "jpeg" {
		format = "jpg"
	}
	for _, candidate := range OutputFormats() {
		if candidate.Value == format {
			report.Format = candidate
			break
		}
	}
	if report.Format.Value == "" {
		return report, fmt.Errorf("unsupported output format %s", options.Format)
	}
	if options.PageOutput != nil && !report.Format.Paged {
		return report, fmt.Errorf("page output requires a paged format")
	}
	if options.Pages != nil && len(options.Pages) == 0 {
		return report, fmt.Errorf("no pages selected")
	}
	if options.Pages != nil && strings.TrimSpace(options.PageRange) != "" {
		return report, fmt.Errorf("conflicting page selections")
	}
	progress := func(stage string, completed, total int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.OnProgress != nil {
			return options.OnProgress(ConvertProgress{Stage: stage, Completed: completed, Total: total})
		}
		return nil
	}
	writeProgress := func(stage string, completed, total int) error {
		if stage == "write" {
			return progress(stage, completed, total)
		}
		return progress("write."+stage, completed, total)
	}
	if err = progress("open", 0, 0); err != nil {
		return report, err
	}
	source = convertReader{context: ctx, source: source}
	report.Input = strings.ToLower(strings.TrimSpace(options.Input))
	if report.Input == "" {
		var header [1024]byte
		n, readErr := source.ReadAt(header[:], 0)
		if readErr != nil && readErr != io.EOF {
			return report, readErr
		}
		switch {
		case bytes.HasPrefix(header[:n], []byte("PK\x03\x04")):
			report.Input = "ofd"
		case bytes.Contains(header[:n], []byte("%PDF-")):
			report.Input = "pdf"
		}
	}
	var reader *Reader
	var editor *Editor
	var count int
	if report.Input != "pdf" && report.Input != "ofd" {
		return report, fmt.Errorf("unsupported input format")
	}
	if options.SkipUnchanged && report.Input == format && options.Pages == nil && strings.TrimSpace(options.PageRange) == "" {
		report.Unchanged = true
		return report, nil
	}
	if output == nil && options.PageOutput == nil {
		return report, fmt.Errorf("missing output writer")
	}
	switch report.Input {
	case "pdf":
		pdfOptions := options.PDF
		pdfOptions.RendererOptions = append(append([]RendererOption(nil), pdfOptions.RendererOptions...), options.RendererOptions...)
		if options.Backends != nil {
			pdfOptions.Backends = options.Backends
		}
		options.RendererOptions = pdfOptions.RendererOptions
		options.Backends = pdfOptions.Backends
		pdfOptions.OnProgress = func(stage string, completed, total int) error {
			if err := progress(stage, completed, total); err != nil {
				return err
			}
			if options.PDF.OnProgress != nil {
				return options.PDF.OnProgress(stage, completed, total)
			}
			return nil
		}
		editor, report.PDF, err = ImportPDF(ctx, source, size, pdfOptions)
		if err != nil {
			return report, err
		}
		count = editor.PageCount()
	case "ofd":
		reader, err = NewReader(source, size, options.ReaderOptions...)
		if err != nil {
			return report, err
		}
		defer reader.Close()
		count, err = reader.PageCount()
		if err != nil {
			return report, err
		}
	}
	if strings.TrimSpace(options.PageRange) != "" {
		options.Pages, err = ParsePageRange(options.PageRange, count)
		if err != nil {
			return report, err
		}
	}
	indices, err := exportPageIndices(count, options.Pages)
	if err != nil {
		return report, err
	}
	report.Pages = len(indices)
	writer := convertWriter{context: ctx, writer: output, count: &report.Bytes}
	if format == "ofd" {
		if options.Pages == nil && editor == nil {
			if err = progress("write", 0, 0); err != nil {
				return report, err
			}
			_, err = io.Copy(writer, io.NewSectionReader(source, 0, size))
			report.Unchanged = err == nil
			return report, err
		}
		if editor == nil {
			editor, err = reader.Editor()
			if err != nil {
				return report, err
			}
		}
		editor.OnWriteProgress = writeProgress
		if options.Pages == nil {
			_, err = editor.WriteTo(writer)
		} else {
			_, err = editor.WritePagesTo(writer, indices)
		}
		return report, err
	}
	if reader == nil {
		if err = progress("prepare", 0, count); err != nil {
			return report, err
		}
		reader, err = editor.reader(progress)
		if err != nil {
			return report, err
		}
		defer reader.Close()
	}
	renderer := NewRenderer(reader, options.RendererOptions...)
	if options.Backends != nil {
		WithRenderBackends(*options.Backends)(renderer)
	}
	renderer.OnExportProgress = func(completed, total int) error {
		return progress("export", completed, total)
	}
	switch format {
	case "pdf":
		err = renderer.RenderToMultiPagePDF(writer, indices...)
	case "txt":
		err = renderer.RenderToMultiPageText(writer, indices...)
	default:
		if options.PageOutput == nil {
			err = renderer.RenderToZIP(writer, format, indices...)
			report.Format = OutputFormat{Value: "zip", Label: "ZIP", Extension: "zip", MIME: "application/zip"}
			break
		}
		for i, index := range indices {
			if err = progress("export", i, len(indices)); err != nil {
				return report, err
			}
			page, readErr := reader.PageContentByIndex(index)
			if readErr != nil {
				return report, readErr
			}
			if err = options.PageOutput(index, func(output io.Writer) error {
				return renderer.RenderTo(page, convertWriter{context: ctx, writer: output, count: &report.Bytes}, format)
			}); err != nil {
				return report, fmt.Errorf("export page %d: %w", index+1, err)
			}
		}
		err = progress("export", len(indices), len(indices))
	}
	return report, err
}

// ReadAt 读取指定位置的数据并响应取消
// 入参: data 目标缓冲, offset 源偏移
// 返回: int 读取字节数, error 读取错误
func (r convertReader) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.source.ReadAt(data, offset)
}

// Write 写出数据并响应取消及短写
// 入参: data 待写数据
// 返回: int 写入字节数, error 写入错误
func (w convertWriter) Write(data []byte) (int, error) {
	if err := w.context.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(data)
	*w.count += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}
