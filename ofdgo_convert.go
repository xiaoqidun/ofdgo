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
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xiaoqidun/pdfgo"
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

// ConvertProgress 回报open、pages、convert、fonts、fonts.*、prepare、write、write.*及export阶段，Total为0表示总量未知
type ConvertProgress struct {
	Stage     string `json:"stage"`
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
}

// ConvertOptions 设置文档转换格式、页码、阅读和渲染选项
// Input为空时识别源格式，Format为输出格式，Pages使用零基索引，nil表示全部页面
// PageRange使用一基页码表达式，与Pages互斥，空字符串表示全部页面
// SkipUnchanged仅在未压缩、源格式与目标相同且未选页时不写出数据
// Pool可由多个独立转换共享，nil时不限制文件间并发
// Resources提供共享计算与缓存策略，显式Pool优先控制计算调度
// 整份PDF同格式输出直接重写PDF对象，其余PDF先转换为OFD对象再输出
// RendererOptions同时用于PDF局部合成和输出，在PDF.RendererOptions之后应用
// Backends统一配置转换与输出，优先于PDF和RendererOptions中的后端设置
// Compression统一配置输出及PDF图片导入，优先于PDF.Compression，文本输出不优化图片
// Fonts仅用于OFD输出，按字体处理全包引用，未处理原因通过报告返回
// PageOutput仅用于逐页格式，同步调用export写出一页并自行处理提交或回滚；此时output可为nil
// 未提供PageOutput时，逐页格式打包ZIP；所有回调返回错误均会停止转换
// DocumentIndex指定多文档OFD的目标文档索引，从0开始；VersionID指定历史版本标识，空字符串按默认入口打开
// PrimaryVersion显式打开目标文档主入口DocRoot，与VersionID互斥
// ExtractDocument仅用于OFD输出，显式提取目标文档为独立单文档OFD并保留附件、书签、标引与元数据
// 未设置ExtractDocument、VersionID、PrimaryVersion及选页时，OFD输出按整包写出
type ConvertOptions struct {
	Resources       *Resources
	Input           string
	Format          string
	DocumentIndex   int
	VersionID       string
	PrimaryVersion  bool
	ExtractDocument bool
	Pages           []int
	PageRange       string
	SkipUnchanged   bool
	Pool            *ConversionPool
	Compression     CompressionOptions
	Fonts           FontMode
	Backends        *RenderBackends
	ReaderOptions   []ReaderOption
	RendererOptions []RendererOption
	PDF             PDFImportOptions
	OnProgress      func(ConvertProgress) error
	PageOutput      func(index int, export func(io.Writer) error) error
}

// ConvertReport 汇总源格式、输出格式、页数、字节数及PDF转换诊断
// Unchanged表示无需转换或原样交付源文件，Bytes仅统计实际输出，不包含调用方额外封装
type ConvertReport struct {
	Input     string          `json:"input"`
	Format    OutputFormat    `json:"format"`
	Pages     int             `json:"pages"`
	Bytes     int64           `json:"bytes"`
	Unchanged bool            `json:"unchanged,omitempty"`
	PDF       PDFImportReport `json:"pdf"`
	Fonts     []FontChange    `json:"fonts,omitempty"`
}

// ConversionPoolOptions 设置共享转换并行度及图像工作集预算
// Parallelism为0时使用GOMAXPROCS，MemoryLimit为0时不指定固定预算
// AvailableMemory返回可用于新增任务的字节数，nil表示不探测，回调须支持并发调用
// 内存预算用于调度，不限制解析器及运行时的实际分配
type ConversionPoolOptions struct {
	Parallelism     int
	MemoryLimit     int64
	AvailableMemory func() int64
}

// ConversionPool 协调独立转换和图像任务，配置在创建后不变，可跨会话复用
type ConversionPool struct {
	options ConversionPoolOptions
	mu      sync.Mutex
	changed chan struct{}
	active  int
	images  int
	memory  int64
}

// convertPageTask 保存单页并发渲染结果与同步信号
type convertPageTask struct {
	index int
	buf   bytes.Buffer
	err   error
	done  chan struct{}
}

// convertReader 将取消上下文传递到随机读取
type convertReader struct {
	ctx   context.Context
	input io.ReaderAt
}

// convertWriter 将取消上下文传递到写入并统计实际字节数
type convertWriter struct {
	ctx    context.Context
	writer io.Writer
	count  *int64
}

// conversionPoolKey 标识单次转换使用的共享预算
type conversionPoolKey struct{}

// Convert 将PDF或OFD转换为指定格式，不关闭调用方的输入和输出
// 原始模式完整OFD另存保持源字节；选页和跨格式输出重新生成文档，压缩仅作用于输出副本
// 输入按需随机读取，输出分块写出；解析对象及后端可能缓存资源，不保证恒定内存
// 出错时报告保留已有诊断，调用方应丢弃未完成输出；只有返回nil才表示转换成功
// 入参: ctx 取消上下文, input 输入源, output 输出流, options 转换选项
// 返回: ConvertReport 转换报告, error 错误信息
func Convert(ctx context.Context, input *Input, output io.Writer, options ConvertOptions) (report ConvertReport, err error) {
	if input != nil {
		if err = sameOutputFile(input.info, output); err != nil {
			return report, err
		}
	}
	if options.Resources != nil {
		if options.Pool == nil {
			options.Pool = options.Resources.pool
		}
		options.ReaderOptions = append(slices.Clone(options.ReaderOptions), WithResources(options.Resources))
		options.PDF.Resources = options.Resources
	}
	if options.Pool != nil {
		if err = options.Pool.acquire(ctx, false, 0); err != nil {
			return report, err
		}
		defer options.Pool.release(false, 0)
		ctx = context.WithValue(ctx, conversionPoolKey{}, options.Pool)
	}
	if err = options.Compression.Validate(); err != nil {
		return report, err
	}
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
	if options.Fonts > FontExternal || options.Fonts != FontKeep && format != "ofd" {
		return report, fmt.Errorf("font processing requires OFD output and a valid mode")
	}
	if options.PageOutput != nil && !report.Format.Paged {
		return report, fmt.Errorf("page output requires a paged format")
	}
	if options.DocumentIndex < 0 {
		return report, fmt.Errorf("document index %d out of range", options.DocumentIndex)
	}
	if options.PrimaryVersion && options.VersionID != "" {
		return report, fmt.Errorf("conflicting version selections")
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
			if err := options.OnProgress(ConvertProgress{Stage: stage, Completed: completed, Total: total}); err != nil {
				return err
			}
		}
		return ctx.Err()
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
	if input == nil || input.Size() <= 0 {
		return report, fmt.Errorf("empty document input")
	}
	size := input.Size()
	operationInput := &Input{reader: convertReader{ctx: ctx, input: input}, size: size}
	report.Input = strings.ToLower(strings.TrimSpace(options.Input))
	if report.Input == "" {
		report.Input, err = DetectFormat(operationInput)
		if err != nil {
			return report, err
		}
	}
	var reader *Reader
	var editor *Editor
	var count int
	if report.Input != "pdf" && report.Input != "ofd" {
		return report, fmt.Errorf("unsupported input format")
	}
	if report.Input == "pdf" && (options.DocumentIndex != 0 || options.VersionID != "" || options.PrimaryVersion) {
		return report, fmt.Errorf("PDF input does not support document index or version selection")
	}
	extractDoc := report.Input == "ofd" && (options.ExtractDocument || options.VersionID != "" || options.PrimaryVersion)
	skipUnchanged := options.SkipUnchanged && !extractDoc && options.Fonts == FontKeep && options.Compression.Mode == CompressionUnchanged && report.Input == format && options.Pages == nil && strings.TrimSpace(options.PageRange) == ""
	if skipUnchanged && report.Input == "pdf" {
		report.Unchanged = true
		return report, nil
	}
	if !skipUnchanged && output == nil && options.PageOutput == nil {
		return report, fmt.Errorf("missing output writer")
	}
	if report.Input == "pdf" && format == "pdf" && options.Pages == nil && strings.TrimSpace(options.PageRange) == "" {
		pdfReader, openErr := pdfgo.NewReaderWithOptions(operationInput, size, pdfgo.ReaderOptions{Password: options.PDF.Password, PasswordUTF8: options.PDF.PasswordUTF8, Workers: conversionWorkers(ctx)})
		if openErr != nil {
			return report, wrapPDFOpenError(openErr)
		}
		defer pdfReader.Close()
		if pageErr := pdfReader.WalkPages(ctx, func(_ int, _ *pdfgo.Page) error { report.Pages++; return nil }); pageErr != nil {
			return report, pageErr
		}
		result, optimizeErr := pdfReader.OptimizeTo(ctx, output, pdfgo.OptimizeOptions{Compression: options.Compression.pdfOptions(), Creator: ofdCreator, OnProgress: writeProgress})
		report.Bytes, report.Unchanged = result.Bytes, result.Preserved
		return report, optimizeErr
	}
	switch report.Input {
	case "pdf":
		pdfOptions := options.PDF
		pdfOptions.Compression = options.Compression
		if format == "txt" {
			pdfOptions.Compression = CompressionOptions{}
		}
		pdfOptions.RendererOptions = append(append([]RendererOption(nil), pdfOptions.RendererOptions...), options.RendererOptions...)
		if options.Backends != nil {
			pdfOptions.RendererOptions = append(pdfOptions.RendererOptions, WithRenderBackends(*options.Backends))
		}
		options.RendererOptions = pdfOptions.RendererOptions
		pdfOptions.OnProgress = func(stage string, completed, total int) error {
			if err := progress(stage, completed, total); err != nil {
				return err
			}
			if options.PDF.OnProgress != nil {
				return options.PDF.OnProgress(stage, completed, total)
			}
			return nil
		}
		editor, report.PDF, err = ImportPDF(ctx, operationInput, pdfOptions)
		if err != nil {
			return report, err
		}
		defer editor.Close()
		count = editor.PageCount()
	case "ofd":
		readerOpts := slices.Clone(options.ReaderOptions)
		if options.DocumentIndex != 0 {
			readerOpts = append(readerOpts, WithDocumentIndex(options.DocumentIndex))
		}
		if options.VersionID != "" {
			readerOpts = append(readerOpts, WithVersion(options.VersionID))
		} else if options.PrimaryVersion {
			readerOpts = append(readerOpts, WithVersion(""))
		}
		reader, err = NewReader(operationInput, readerOpts...)
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
	writer := convertWriter{ctx: ctx, writer: output, count: &report.Bytes}
	if format == "ofd" {
		if !extractDoc && options.Pages == nil && editor == nil && options.Fonts == FontKeep && options.Compression.Mode == CompressionUnchanged {
			if options.SkipUnchanged {
				report.Unchanged = true
				return report, nil
			}
			if err = progress("write", 0, 0); err != nil {
				return report, err
			}
			_, err = io.Copy(writer, io.NewSectionReader(operationInput, 0, size))
			report.Unchanged = err == nil
			return report, err
		}
		if editor == nil {
			if extractDoc || options.Pages != nil {
				extracted, extractErr := extractConvertDocument(ctx, reader, options)
				if extractErr != nil {
					return report, extractErr
				}
				defer extracted.Close()
				editor, err = extracted.EditorContext(ctx)
			} else {
				editor, err = reader.EditorContext(ctx)
			}
			if err != nil {
				return report, err
			}
			defer editor.Close()
		}
		editor.OnWriteProgress = writeProgress
		if options.Fonts != FontKeep {
			if err = progress("fonts", 0, 0); err != nil {
				return report, err
			}
			fontResources := options.Resources
			if fontResources == nil {
				fontResources = editor.runtimeResources
			}
			configuration := NewRenderer(&Reader{resources: fontResources}, options.RendererOptions...)
			if options.Backends != nil {
				WithRenderBackends(*options.Backends)(configuration)
			}
			editor.SetRenderBackends(configuration.Backends())
			editor.SetFontDirs(configuration.fontDirs...)
			editor.SetFontFS(configuration.fontFS...)
			report.Fonts, err = editor.ProcessFontsWithOptions(ctx, FontProcessOptions{Mode: options.Fonts, OnProgress: func(stage string, completed, total int) error {
				if stage == "fonts" {
					return progress(stage, completed, total)
				}
				return progress("fonts."+stage, completed, total)
			}})
			if err != nil {
				return report, err
			}
			if !extractDoc && report.Input == "ofd" && options.Pages == nil && options.Compression.Mode == CompressionUnchanged && !slices.ContainsFunc(report.Fonts, func(change FontChange) bool { return change.Changed }) {
				if err = progress("write", 0, 0); err != nil {
					return report, err
				}
				_, err = io.Copy(writer, io.NewSectionReader(operationInput, 0, size))
				report.Unchanged = err == nil
				return report, err
			}
		}
		if options.Pages == nil {
			_, err = editor.WriteToWithOptions(ctx, writer, WriteOptions{Compression: options.Compression})
		} else {
			_, err = editor.WritePagesTo(ctx, writer, indices, WriteOptions{Compression: options.Compression})
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
	renderer.SetCompression(options.Compression)
	if options.Backends != nil {
		WithRenderBackends(*options.Backends)(renderer)
	}
	renderer.OnExportProgress = func(completed, total int) error {
		return progress("export", completed, total)
	}
	switch format {
	case "pdf":
		err = renderer.RenderToMultiPagePDFContext(ctx, writer, indices...)
	case "txt":
		err = renderer.RenderToMultiPageTextContext(ctx, writer, indices...)
	default:
		err = convertPagedOutput(ctx, renderer, indices, format, writer, &report, options.PageOutput, progress)
	}
	return report, err
}

// ConvertToOutput 通过输出事务将输入文档原子转换写出至目标，失败或取消时自动回滚事务
// 入参: ctx 取消上下文, input 输入源, target 输出事务目标, options 转换选项
// 返回: ConvertReport 转换报告, error 转换或提交错误
func ConvertToOutput(ctx context.Context, input *Input, target Output, options ConvertOptions) (report ConvertReport, err error) {
	if options.PageOutput != nil {
		return Convert(ctx, input, io.Discard, options)
	}
	if target == nil {
		return report, ErrSaveTarget
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		return report, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Abort())
		}
	}()
	report, err = Convert(ctx, input, tx, options)
	if err != nil {
		return report, err
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	err = tx.Commit()
	return report, err
}

// NewConversionPool 创建共享转换调度器，不启动后台任务
// 入参: options 并行及内存选项
// 返回: *ConversionPool 调度器, error 配置错误
func NewConversionPool(options ConversionPoolOptions) (*ConversionPool, error) {
	if options.Parallelism < 0 || options.MemoryLimit < 0 {
		return nil, fmt.Errorf("invalid conversion pool options")
	}
	if options.Parallelism == 0 {
		options.Parallelism = runtime.GOMAXPROCS(0)
	}
	return &ConversionPool{options: options, changed: make(chan struct{})}, nil
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

// DetectFormat 按文件内容识别PDF或OFD，不读取压缩内容或关闭输入
// 识别结果不代表文档完整有效，仍需通过对应阅读器解析
// 入参: input 输入源
// 返回: string 格式标识pdf或ofd, error 读取错误或不支持的格式
func DetectFormat(input *Input) (string, error) {
	if input == nil || input.Size() <= 0 {
		return "", fmt.Errorf("empty document input")
	}
	size := input.Size()
	var header [1024]byte
	data := header[:min(size, int64(len(header)))]
	n, err := input.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n != len(data) {
		return "", io.ErrUnexpectedEOF
	}
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return "pdf", nil
	}
	archive, err := zip.NewReader(input, size)
	if err == nil {
		reader := &Reader{zip: archive}
		if err := reader.indexPackage(); err != nil {
			return "", err
		}
		if reader.fileNames["OFD.xml"] || reader.fileNames["Encryptions.xml"] {
			return "ofd", nil
		}
	} else {
		if !errors.Is(err, zip.ErrFormat) {
			return "", err
		}
		if bytes.Contains(data, []byte("%PDF-")) {
			return "pdf", nil
		}
	}
	return "", fmt.Errorf("unsupported input format")
}

// convertPagedOutput 按页序执行逐页图像或矢量导出，多页时使用ConversionPool与Reader.Fork多核并发渲染
// 入参: ctx 取消上下文, renderer 渲染器, indices 导出页索引, format 输出格式, writer ZIP输出流, report 转换报告, pageOutput 逐页回调, progress 进度回调
// 返回: error 导出或取消错误
func convertPagedOutput(ctx context.Context, renderer *Renderer, indices []int, format string, writer io.Writer, report *ConvertReport, pageOutput func(int, func(io.Writer) error) error, progress func(string, int, int) error) error {
	if pageOutput == nil {
		report.Format = OutputFormat{Value: "zip", Label: "ZIP", Extension: "zip", MIME: "application/zip"}
		return renderer.RenderToZIPContext(ctx, writer, format, indices...)
	}
	workers := min(len(indices), conversionWorkers(ctx))
	if workers <= 1 {
		for i, index := range indices {
			if err := progress("export", i, len(indices)); err != nil {
				return err
			}
			page, readErr := renderer.reader.PageContentByIndexContext(ctx, index)
			if readErr != nil {
				return readErr
			}
			if err := pageOutput(index, func(output io.Writer) error {
				return renderer.RenderToContext(ctx, page, convertWriter{ctx: ctx, writer: output, count: &report.Bytes}, format)
			}); err != nil {
				return fmt.Errorf("export page %d: %w", index+1, err)
			}
		}
		return progress("export", len(indices), len(indices))
	}
	if _, err := renderer.reader.docContext(ctx); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	forked := make([]*Renderer, 0, workers)
	defer func() {
		for _, clone := range forked {
			_ = clone.Close()
		}
	}()
	for range workers {
		clone, err := renderer.Fork(runCtx)
		if err != nil {
			return err
		}
		forked = append(forked, clone)
	}
	pool, _ := ctx.Value(conversionPoolKey{}).(*ConversionPool)
	raster := report.Format.Raster
	window := workers * 2
	jobs := make(chan *convertPageTask, window)
	inflight := make(chan *convertPageTask, window)
	var wg sync.WaitGroup
	for _, workerRenderer := range forked {
		wg.Add(1)
		go func(r *Renderer) {
			defer wg.Done()
			for task := range jobs {
				if err := runCtx.Err(); err != nil {
					task.err = err
					close(task.done)
					continue
				}
				page, readErr := r.reader.PageContentByIndexContext(runCtx, task.index)
				if readErr != nil {
					task.err = readErr
					close(task.done)
					continue
				}
				var estimated int64
				acquired := false
				if pool != nil && raster {
					estimated = estimateRasterPageBytes(r, page)
					if err := pool.acquire(runCtx, true, estimated); err != nil {
						task.err = err
						close(task.done)
						continue
					}
					acquired = true
				}
				task.err = r.RenderToContext(runCtx, page, &task.buf, format)
				if acquired {
					pool.release(true, estimated)
				}
				close(task.done)
			}
		}(workerRenderer)
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
	for i := range indices {
		if err := progress("export", i, len(indices)); err != nil {
			finish()
			return err
		}
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
			return fmt.Errorf("export page %d: %w", task.index+1, task.err)
		}
		if err := pageOutput(task.index, func(output io.Writer) error {
			_, copyErr := io.Copy(convertWriter{ctx: ctx, writer: output, count: &report.Bytes}, bytes.NewReader(task.buf.Bytes()))
			return copyErr
		}); err != nil {
			finish()
			return fmt.Errorf("export page %d: %w", task.index+1, err)
		}
		task.buf.Reset()
	}
	finish()
	return progress("export", len(indices), len(indices))
}

// conversionWorkers 按当前文件数量分配内部计算任务
// 入参: ctx 转换上下文
// 返回: int 内部并行数
func conversionWorkers(ctx context.Context) int {
	p, _ := ctx.Value(conversionPoolKey{}).(*ConversionPool)
	if p == nil {
		return runtime.GOMAXPROCS(0)
	}
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	return max(1, p.parallelism()/max(1, active))
}

// estimateRasterPageBytes 按页面物理区域与渲染DPI估算单页RGBA光栅缓冲字节数
// 入参: r 渲染器, page 页面内容
// 返回: int64 估算字节数
func estimateRasterPageBytes(r *Renderer, page *PageContent) int64 {
	if r == nil || page == nil {
		return 0
	}
	box, err := r.GetPageBox(page)
	if err != nil {
		return 0
	}
	w, h, err := (&RasterPage{Width: box.W, Height: box.H, DPI: r.dpi}).PixelSize()
	if err != nil {
		return 0
	}
	return int64(w) * int64(h) * 4
}

// extractConvertDocument 按转换选项抽取目标文档及指定版本为独立单文档阅读器
// 保留附件、书签、标引、元数据及封面，移除其余文档与版本列表
// 入参: ctx 取消上下文, reader 源包阅读器, options 转换选项
// 返回: *Reader 独立单文档阅读器, error 读取、提取或取消错误
func extractConvertDocument(ctx context.Context, reader *Reader, options ConvertOptions) (*Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.VersionID != "" {
		return reader.ExtractVersionContext(ctx, options.VersionID)
	}
	entry, err := reader.DocumentRootContext(ctx)
	if err != nil {
		return nil, err
	}
	if reader.versionInfo != nil {
		return reader.ExtractVersionContext(ctx, reader.versionInfo.ID)
	}
	files, err := reader.versionFileDependencies(ctx, entry, nil, nil)
	if err != nil {
		return nil, err
	}
	ofdData, err := reader.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return nil, err
	}
	ofdRoot, err := parseEditorXMLContext(ctx, ofdData)
	if err != nil {
		return nil, err
	}
	body := ofdRoot.childAt("DocBody", reader.documentIndex)
	if body == nil {
		return nil, fmt.Errorf("document entry not found: %d", reader.documentIndex)
	}
	if info := body.child("DocInfo"); info != nil {
		if cover := info.child("Cover"); cover != nil {
			coverPath := cleanPackagePath(resolveResourcePath("OFD.xml", "", editorImportText(ofdData, cover)))
			if validPackagePath(coverPath) && reader.fileNames[coverPath] {
				files[coverPath] = true
			}
		}
	}
	var overrideFiles map[string][]byte
	if sigNode := body.child("Signatures"); sigNode != nil {
		sigPath := cleanPackagePath(resolveResourcePath("OFD.xml", "", editorImportText(ofdData, sigNode)))
		if validPackagePath(sigPath) && reader.fileNames[sigPath] {
			if reader.currentVersioned() {
				filteredList, keptSigFiles, filterErr := filterPrimarySignatureFiles(ctx, reader, entry, sigPath)
				if filterErr != nil {
					return nil, filterErr
				}
				if len(keptSigFiles) != 0 {
					files[sigPath] = true
					maps.Copy(files, keptSigFiles)
					if filteredList != nil {
						overrideFiles = map[string][]byte{sigPath: filteredList}
					}
				}
			} else if sigDeps, sigErr := reader.versionFileDependencies(ctx, sigPath, nil, nil); sigErr == nil {
				maps.Copy(files, sigDeps)
			} else if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	}
	root, err := reader.extractedVersionRoot(ctx, DocumentVersionInfo{DocRoot: entry}, files)
	if err != nil {
		return nil, err
	}
	next := &Reader{path: reader.path, sourceFile: reader.sourceFile, files: map[string][]byte{"OFD.xml": root}, encryption: reader.encryption, resources: reader.resources}
	for name := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name != "OFD.xml" {
			if data, ok := reader.files[name]; ok {
				next.files[name] = data
			}
		}
	}
	maps.Copy(next.files, overrideFiles)
	if reader.zip != nil {
		var kept []*zip.File
		for _, file := range reader.zip.File {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := cleanPackagePath(file.Name)
			if name != "OFD.xml" && files[name] && !file.FileInfo().IsDir() {
				kept = append(kept, file)
			}
		}
		next.zip = &zip.Reader{Comment: reader.zip.Comment, File: kept}
		next.archiveID = nextReaderArchiveID.Add(1)
		next.maxIDCache = &archiveMaxIDCache{}
	}
	if src := reader.encryptedSource(); src != nil {
		keep := make(map[string]bool, len(files))
		for name := range files {
			if name != "OFD.xml" {
				keep[name] = true
			}
		}
		next.setEncryptedSource(src.filter(keep))
	}
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.docStructureContext(ctx); err != nil {
		return nil, err
	}
	next.bindEncryptionHold()
	return next, nil
}

// filterPrimarySignatureFiles 过滤多版本文档主入口中仅归属历史版本的签章条目与文件
// 入参: ctx 取消上下文, reader 源阅读器, entry 主入口文档路径, listPath 签名列表路径
// 返回: []byte 过滤后的签名列表XML，未改动时为nil, map[string]bool 保留的签章文件集, error 读取或取消错误
func filterPrimarySignatureFiles(ctx context.Context, reader *Reader, entry, listPath string) ([]byte, map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	cleanEntry := cleanPackagePath(entry)
	cleanList := cleanPackagePath(listPath)
	listData, err := reader.readFileViewContext(ctx, cleanList)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, nil
	}
	listRoot, err := parseEditorXMLContext(ctx, listData)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, nil
	}
	versionRoots := make(map[string]bool)
	versionDeclaredFiles := make(map[string]bool)
	if versions, verErr := reader.DocumentVersionsContext(ctx); verErr == nil {
		for _, v := range versions {
			if loc := cleanPackagePath(v.Location); validPackagePath(loc) {
				versionRoots[loc] = true
			}
			vDocRoot := cleanPackagePath(v.DocRoot)
			if validPackagePath(vDocRoot) && vDocRoot != cleanEntry {
				versionRoots[vDocRoot] = true
			}
			for _, vf := range v.Files {
				if clean := cleanPackagePath(vf.Location); validPackagePath(clean) {
					versionDeclaredFiles[clean] = true
				}
			}
		}
	} else if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	primaryPages := make(map[string]bool)
	if docData, docErr := reader.readFileViewContext(ctx, cleanEntry); docErr == nil {
		var doc documentPageList
		if xml.Unmarshal(docData, &doc) == nil {
			for _, page := range doc.Pages.Page {
				if id := editorResourceID(page.ID); id != "" {
					primaryPages[id] = true
				}
			}
		}
	} else if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	keptFiles := make(map[string]bool)
	var dropPatches []editorXMLPatch
	keptCount := 0
	for _, node := range listRoot.children {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !node.matchesOFD("Signature") {
			continue
		}
		sigFilePath := cleanPackagePath(signatureRefPath(cleanList, node.attr("BaseLoc")))
		if !validPackagePath(sigFilePath) || versionDeclaredFiles[sigFilePath] {
			dropPatches = append(dropPatches, editorXMLPatch{start: node.start, end: node.end})
			continue
		}
		sigData, readErr := reader.readFileViewContext(ctx, sigFilePath)
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			keptCount++
			if reader.fileNames[sigFilePath] {
				keptFiles[sigFilePath] = true
			}
			continue
		}
		sigFile, parseErr := parseSignatureFile(sigData)
		if parseErr != nil {
			keptCount++
			keptFiles[sigFilePath] = true
			continue
		}
		ownValue := ""
		if sigFile.SignedValue != "" {
			if p := cleanPackagePath(signatureRefPath(sigFilePath, sigFile.SignedValue)); validPackagePath(p) {
				ownValue = p
			}
		}
		ownSeal := ""
		if sigFile.SignedInfo.Seal.BaseLoc != "" {
			if p := cleanPackagePath(signatureRefPath(sigFilePath, sigFile.SignedInfo.Seal.BaseLoc)); validPackagePath(p) {
				ownSeal = p
			}
		}
		hitsPrimaryRoot := false
		hitsVersionRoot := false
		for _, ref := range sigFile.SignedInfo.References.Reference {
			refPath := cleanPackagePath(signatureRefPath(sigFilePath, ref.FileRef))
			if !validPackagePath(refPath) || refPath == "OFD.xml" || refPath == cleanList || refPath == sigFilePath || refPath == ownValue || refPath == ownSeal {
				continue
			}
			if refPath == cleanEntry {
				hitsPrimaryRoot = true
			}
			if versionRoots[refPath] {
				hitsVersionRoot = true
			}
		}
		drop := false
		if hitsVersionRoot && !hitsPrimaryRoot {
			drop = true
		} else if !hitsPrimaryRoot && len(sigFile.SignedInfo.StampAnnot) > 0 && len(primaryPages) > 0 {
			matchedPage := false
			for _, stamp := range sigFile.SignedInfo.StampAnnot {
				if primaryPages[editorResourceID(stamp.PageRef)] {
					matchedPage = true
					break
				}
			}
			if !matchedPage {
				drop = true
			}
		}
		if drop {
			dropPatches = append(dropPatches, editorXMLPatch{start: node.start, end: node.end})
			continue
		}
		keptCount++
		keptFiles[sigFilePath] = true
		if ownValue != "" && reader.fileNames[ownValue] {
			keptFiles[ownValue] = true
		}
		if ownSeal != "" && reader.fileNames[ownSeal] {
			keptFiles[ownSeal] = true
		}
	}
	if keptCount == 0 {
		return nil, nil, ctx.Err()
	}
	if len(dropPatches) == 0 {
		return nil, keptFiles, ctx.Err()
	}
	return editorPatchXML(listData, dropPatches), keptFiles, ctx.Err()
}

// parallelism 返回有效的并发上限，零值配置回退到GOMAXPROCS
// 返回: int 有效并发上限
func (p *ConversionPool) parallelism() int {
	if p != nil && p.options.Parallelism > 0 {
		return p.options.Parallelism
	}
	return max(1, runtime.GOMAXPROCS(0))
}

// Parallelism 返回同时运行的转换数量上限
// 返回: int 并行上限
func (p *ConversionPool) Parallelism() int { return p.parallelism() }

// ReadAt 读取前后响应取消，保留读取字节数和源错误
// 入参: data 目标缓冲, offset 源偏移
// 返回: int 读取字节数, error 读取错误
func (r convertReader) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.input.ReadAt(data, offset)
	if err == nil {
		err = r.ctx.Err()
	}
	return n, err
}

// Write 写入前后响应取消，保留写入及短写错误
// 入参: data 待写数据
// 返回: int 写入字节数, error 写入错误
func (w convertWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(data)
	*w.count += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = w.ctx.Err()
	}
	return n, err
}

// WriteString 写入字符串并保留底层原生写入能力，前后响应取消
// 入参: data 待写文本
// 返回: int 写入字节数, error 写入、短写或取消错误
func (w convertWriter) WriteString(data string) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := io.WriteString(w.writer, data)
	*w.count += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = w.ctx.Err()
	}
	return n, err
}

// memoryLimit 读取当前可分配的图像工作量预算
// 返回: int64 可用字节数，负值表示未设置预算
func (p *ConversionPool) memoryLimit() int64 {
	limit := int64(-1)
	if p.options.MemoryLimit > 0 {
		limit = p.options.MemoryLimit
	}
	if p.options.AvailableMemory != nil {
		available := max(0, p.options.AvailableMemory())
		if limit < 0 || available < limit {
			limit = available
		}
	}
	return limit
}

// acquire 等待转换或图像预算，超预算图像可独占运行
// 入参: ctx 取消上下文, image 是否为图像任务, size 图像工作量
// 返回: error 取消错误
func (p *ConversionPool) acquire(ctx context.Context, image bool, size int64) error {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		limit := p.memoryLimit()
		parallelism := p.parallelism()
		p.mu.Lock()
		allowed := p.active < parallelism && (p.active == 0 || limit != 0)
		if image {
			allowed = p.images < parallelism && (p.images == 0 || limit < 0 || size <= limit-p.memory)
		}
		if allowed {
			if image {
				p.images++
				p.memory += size
			} else {
				p.active++
			}
			p.mu.Unlock()
			return nil
		}
		if p.changed == nil {
			p.changed = make(chan struct{})
		}
		changed := p.changed
		p.mu.Unlock()
		var tick <-chan time.Time
		if p.options.AvailableMemory != nil {
			if timer == nil {
				timer = time.NewTimer(250 * time.Millisecond)
			} else {
				timer.Reset(250 * time.Millisecond)
			}
			tick = timer.C
		}
		select {
		case <-ctx.Done():
		case <-changed:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// release 归还任务预算并唤醒等待者
// 入参: image 是否为图像任务, size 图像工作量
func (p *ConversionPool) release(image bool, size int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if image {
		p.images--
		p.memory -= size
	} else {
		p.active--
	}
	if p.changed != nil {
		close(p.changed)
	}
	p.changed = make(chan struct{})
}
