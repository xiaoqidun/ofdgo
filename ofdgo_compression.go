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
	"compress/flate"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

// outputOptimizationBufferLimit 限制压缩时的单个输出缓冲，单位为字节
const outputOptimizationBufferLimit = 64 << 20

// 输出压缩模式：原始、无损和有损
const (
	CompressionUnchanged CompressionMode = iota
	CompressionLossless
	CompressionLossy
)

// 有损压缩预设：轻压、均衡和强压
const (
	CompressionLight CompressionLevel = iota
	CompressionMedium
	CompressionStrong
)

// 并行预压缩条目的写出方式枚举
const (
	zipPreparedBytes zipPreparedKind = iota
	zipPreparedRawFile
	zipPreparedStoreFile
	zipPreparedDir
	zipPreparedStaged
	zipPreparedLargeEntry
)

// outputDeflaterPool 复用最高压缩等级编码器，不保留输出流引用
var outputDeflaterPool = sync.Pool{
	New: func() any {
		writer, _ := flate.NewWriter(io.Discard, flate.BestCompression)
		return writer
	},
}

// CompressionMode 选择原始、无损或有损输出策略
type CompressionMode uint8

// CompressionLevel 选择轻压、均衡或强压，仅用于有损模式
type CompressionLevel uint8

// CompressionOptions 配置输出压缩，原始模式沿用现有输出策略，不额外优化
// Quality为有损质量1至100，0按Level使用85、60或40
// MaxDPI为图片降采样目标1至9600，0按Level使用不限、150或96，不改变页面尺寸或整页导出精度
// 无损表示不引入额外损失，不保证跨格式转换可逆；有损不栅格化文字和矢量
type CompressionOptions struct {
	Mode    CompressionMode  `json:"mode"`
	Level   CompressionLevel `json:"level,omitempty"`
	Quality int              `json:"quality,omitempty"`
	MaxDPI  int              `json:"maxDPI,omitempty"`
}

// Validate 检查压缩档位、画质及精度范围
// 返回: error 参数错误
func (o CompressionOptions) Validate() error {
	if o.Mode > CompressionLossy {
		return fmt.Errorf("invalid compression mode %d", o.Mode)
	}
	if o.Level > CompressionStrong {
		return fmt.Errorf("invalid compression level %d", o.Level)
	}
	if o.Quality < 0 || o.Quality > 100 {
		return fmt.Errorf("compression quality must be between 0 and 100")
	}
	if o.MaxDPI < 0 || o.MaxDPI > 9600 {
		return fmt.Errorf("compression max DPI must be between 0 and 9600")
	}
	return nil
}

// ImageQuality 返回有损编码使用的画质
// 返回: int 编码画质
func (o CompressionOptions) ImageQuality() int {
	if o.Quality > 0 {
		return o.Quality
	}
	switch o.Level {
	case CompressionMedium:
		return 60
	case CompressionStrong:
		return 40
	default:
		return 85
	}
}

// ImageDPI 返回嵌入图片的降采样目标精度，0表示不降采样
// 返回: int 目标精度
func (o CompressionOptions) ImageDPI() int {
	if o.MaxDPI > 0 {
		return o.MaxDPI
	}
	switch o.Level {
	case CompressionMedium:
		return 150
	case CompressionStrong:
		return 96
	default:
		return 0
	}
}

// pdfOptions 转换为PDF底层压缩配置
// 返回: pdfgo.CompressionOptions PDF压缩选项
func (o CompressionOptions) pdfOptions() pdfgo.CompressionOptions {
	return pdfgo.CompressionOptions{
		Mode:    pdfgo.CompressionMode(o.Mode),
		Level:   pdfgo.CompressionLevel(o.Level),
		Quality: o.Quality,
		MaxDPI:  o.MaxDPI,
	}
}

// WriteOptions 配置OFD输出副本，不修改编辑资源和撤销记录
type WriteOptions struct{ Compression CompressionOptions }

// outputOptimization 保存单次写出的配置、图片路径及压缩进度，复用压缩器
type outputOptimization struct {
	ctx             context.Context
	options         CompressionOptions
	images          map[string]bool
	sizes           map[string]image.Point
	protectionReady bool
	protected       bool
	protectedDocs   map[int]bool
	protectedFiles  map[string]bool
	completed       int
	deflater        *flate.Writer
	resources       *editorResourceSelection
	references      *renderCache[editorScanKey, editorScanValue]
}

// outputCompressionBuffer 限制候选ZIP编码，不能缩小时终止本次压缩
type outputCompressionBuffer struct {
	buffer   *bytes.Buffer
	limit    int
	exceeded bool
}

// zipPreparedKind 标识并行预压缩条目的最终写出方式
type zipPreparedKind uint8

// zipPreparedEntry 保存单个ZIP条目的预压缩输入、结果与同步信号
type zipPreparedEntry struct {
	kind     zipPreparedKind
	header   zip.FileHeader
	data     []byte
	payload  []byte
	file     *zip.File
	staged   *editorStagedPage
	estBytes int64
	err      error
	done     chan struct{}
}

// zipParallelWriter 在有界滑动窗口与内存预算内并行预压缩ZIP条目，主线程按确定顺序写出
type zipParallelWriter struct {
	editor   *Editor
	archive  *zip.Writer
	ctx      context.Context
	cancel   context.CancelFunc
	jobs     chan *zipPreparedEntry
	pending  []*zipPreparedEntry
	window   int
	maxBytes int64
	inFlight int64
	copyBuf  []byte
	group    sync.WaitGroup
	closed   bool
}

// newZipParallelWriter 在压缩写包且具备多核预算时创建有界并行预压缩流水线
// 入参: editor 编辑器快照, archive 输出归档, ctx 取消上下文
// 返回: *zipParallelWriter 并行预压缩流水线，无需并行时返回nil
func newZipParallelWriter(editor *Editor, archive *zip.Writer, ctx context.Context) *zipParallelWriter {
	if editor.output == nil || editor.output.options.Mode == CompressionUnchanged {
		return nil
	}
	workers := conversionWorkers(ctx)
	if workers <= 1 {
		return nil
	}
	window := min(workers*2, 32)
	maxBytes := int64(min(int64(workers)*(8<<20), 64<<20))
	if pool, _ := ctx.Value(conversionPoolKey{}).(*ConversionPool); pool != nil {
		if available := pool.memoryLimit(); available > 0 {
			maxBytes = max(int64(8<<20), min(maxBytes, available/2))
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	writer := &zipParallelWriter{
		editor:   editor,
		archive:  archive,
		ctx:      runCtx,
		cancel:   cancel,
		jobs:     make(chan *zipPreparedEntry, window),
		pending:  make([]*zipPreparedEntry, 0, window),
		window:   window,
		maxBytes: maxBytes,
		copyBuf:  make([]byte, 32*1024),
	}
	for range workers {
		writer.group.Go(writer.workerLoop)
	}
	return writer
}

// acquireOutputDeflater 从对象池获取并重置最高等级压缩器
// 入参: writer 编码目标
// 返回: *flate.Writer 压缩器
func acquireOutputDeflater(writer io.Writer) *flate.Writer {
	deflater := outputDeflaterPool.Get().(*flate.Writer)
	deflater.Reset(writer)
	return deflater
}

// releaseOutputDeflater 清理输出引用并归还压缩器到对象池
// 入参: deflater 压缩器
func releaseOutputDeflater(deflater *flate.Writer) {
	if deflater == nil {
		return
	}
	deflater.Reset(io.Discard)
	outputDeflaterPool.Put(deflater)
}

// isPrecompressedOutputExt 判断条目扩展名是否属于已高度压缩的媒体或归档格式
// 入参: name 条目路径
// 返回: bool 是否属于已压缩后缀
func isPrecompressedOutputExt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".jp2", ".j2k", ".jb2", ".webp", ".zip", ".pdf", ".ofd":
		return true
	}
	return false
}

// compressOutputBytesWithDeflater 使用指定压缩器压缩内存数据并限制候选体积
// 入参: ctx 取消上下文, deflater 压缩器, data 未压缩数据, limit 候选体积上限
// 返回: []byte 更小的DEFLATE编码, error 取消或编码错误
func compressOutputBytesWithDeflater(ctx context.Context, deflater *flate.Writer, data []byte, limit int) ([]byte, error) {
	var compressed bytes.Buffer
	output := &outputCompressionBuffer{buffer: &compressed, limit: min(limit, outputOptimizationBufferLimit)}
	deflater.Reset(output)
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			deflater.Reset(io.Discard)
			return nil, err
		}
		end := min(offset+64<<10, len(data))
		if _, err := deflater.Write(data[offset:end]); err != nil {
			deflater.Reset(io.Discard)
			if err == io.ErrShortBuffer {
				return nil, ctx.Err()
			}
			return nil, err
		}
		offset = end
	}
	if err := deflater.Close(); err != nil {
		deflater.Reset(io.Discard)
		if err == io.ErrShortBuffer {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if compressed.Len() >= limit {
		return nil, ctx.Err()
	}
	return compressed.Bytes(), ctx.Err()
}

// compressOutputFileWithDeflater 分块重压缩ZIP条目，超过体积上限时立即早停
// 入参: ctx 取消上下文, deflater 压缩器, file 原始条目, buffer 读取缓冲, limit 候选体积上限
// 返回: []byte 更小的DEFLATE编码, error 读取、编码或取消错误
func compressOutputFileWithDeflater(ctx context.Context, deflater *flate.Writer, file *zip.File, buffer []byte, limit int) ([]byte, error) {
	input, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	var compressed bytes.Buffer
	output := &outputCompressionBuffer{buffer: &compressed, limit: min(limit, outputOptimizationBufferLimit)}
	deflater.Reset(output)
	source := imageInput{ReadCloser: input, ctx: ctx}
	_, err = io.CopyBuffer(deflater, source, buffer)
	if output.exceeded {
		deflater.Reset(io.Discard)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	closeErr := deflater.Close()
	if ctxErr := ctx.Err(); ctxErr != nil {
		deflater.Reset(io.Discard)
		return nil, ctxErr
	}
	if err != nil {
		deflater.Reset(io.Discard)
		return nil, err
	}
	if output.exceeded {
		deflater.Reset(io.Discard)
		return nil, nil
	}
	if closeErr != nil {
		deflater.Reset(io.Discard)
		return nil, closeErr
	}
	if compressed.Len() >= limit {
		return nil, nil
	}
	return compressed.Bytes(), nil
}

// prepareOutputHeader 更新原始ZIP编码属性，保留名称、备注和字符集标记
// 入参: header 条目属性, method 编码方法, size 原始尺寸, packed 编码尺寸, checksum 校验值
func prepareOutputHeader(header *zip.FileHeader, method uint16, size, packed uint64, checksum uint32) {
	header.Method, header.ReaderVersion = method, 20
	header.CreatorVersion = header.CreatorVersion&0xff00 | 20
	header.CRC32, header.UncompressedSize64, header.CompressedSize64 = checksum, size, packed
	header.Flags &^= 8
	header.Extra = outputZIPExtra(header.Extra)
	if header.NonUTF8 {
		header.Flags &^= 0x800
	} else if utf8.ValidString(header.Name) && utf8.ValidString(header.Comment) {
		for _, value := range header.Name + header.Comment {
			if value < 0x20 || value > 0x7d || value == 0x5c {
				header.Flags |= 0x800
				break
			}
		}
	}
}

// outputZIPExtra 保留扩展属性，移除由新归档重新生成的ZIP64尺寸及偏移
// 入参: extra 原始扩展属性
// 返回: []byte 输出扩展属性，不修改原始缓冲
func outputZIPExtra(extra []byte) []byte {
	var output []byte
	start := 0
	for offset := 0; offset+4 <= len(extra); {
		size := 4 + int(binary.LittleEndian.Uint16(extra[offset+2:]))
		if size > len(extra)-offset {
			break
		}
		if binary.LittleEndian.Uint16(extra[offset:]) == 1 {
			if output == nil {
				output = make([]byte, 0, len(extra))
			}
			output = append(output, extra[start:offset]...)
			start = offset + size
		}
		offset += size
	}
	if output == nil {
		return extra
	}
	return append(output, extra[start:]...)
}

// WriteToWithOptions 按压缩策略写出OFD，签名文档不额外改写资源内容
// 入参: ctx 取消上下文, writer 输出流, options 输出配置
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WriteToWithOptions(ctx context.Context, writer io.Writer, options WriteOptions) (int64, error) {
	if err := sameOutputFile(e.sourceFile, writer); err != nil {
		return 0, err
	}
	snapshot, err := e.outputSnapshot(ctx, options)
	if err != nil {
		return 0, err
	}
	defer snapshot.output.release()
	var count int64
	return snapshot.WriteTo(convertWriter{ctx: ctx, writer: writer, count: &count})
}

// WritePagesTo 按指定顺序另存页面，仅为所选页面准备资源和压缩计划
// 保留关联资源、目录、注释和签章外观，生成新文档标识，不改变原文档和撤销记录
// 签章外观不代表原签名在导出文档中仍有效，页码不可重复或为空
// 入参: ctx 取消上下文, writer 输出流, indexes 零基页码, options 输出配置
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WritePagesTo(ctx context.Context, writer io.Writer, indexes []int, options WriteOptions) (int64, error) {
	if err := sameOutputFile(e.sourceFile, writer); err != nil {
		return 0, err
	}
	ctx = e.runtimeResources.context(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := options.Compression.Validate(); err != nil {
		return 0, err
	}
	snapshot := *e
	snapshot.OnWriteProgress = editorProgress(e.OnWriteProgress).withContext(ctx)
	snapshot.output = &outputOptimization{ctx: ctx, options: options.Compression}
	defer snapshot.output.release()
	var count int64
	return snapshot.writePagesTo(convertWriter{ctx: ctx, writer: writer, count: &count}, indexes)
}

// WritePagesToOutput 通过输出事务原子另存指定页面，取消或失败时自动回滚事务
// 入参: ctx 取消上下文, target 保存目标, indexes 零基页码, options 输出配置
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WritePagesToOutput(ctx context.Context, target Output, indexes []int, options WriteOptions) (n int64, err error) {
	if target == nil {
		return 0, ErrSaveTarget
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tx, err := target.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Abort())
		}
	}()
	n, err = e.WritePagesTo(ctx, tx, indexes, options)
	if err != nil {
		return n, err
	}
	if err = ctx.Err(); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// compressor 获取当前输出独占的压缩器，重置后不复用上一次编码状态
// 入参: writer 编码目标
// 返回: *flate.Writer 压缩器
func (o *outputOptimization) compressor(writer io.Writer) *flate.Writer {
	if o.deflater == nil {
		o.deflater = acquireOutputDeflater(writer)
		return o.deflater
	}
	o.deflater.Reset(writer)
	return o.deflater
}

// release 释放输出目标并归还压缩器，由对象池按需复用或回收
func (o *outputOptimization) release() {
	if o.deflater == nil {
		return
	}
	releaseOutputDeflater(o.deflater)
	o.deflater = nil
}

// Write 写入有界候选编码，超过体积上限时返回短缓冲错误
// 入参: data 编码数据
// 返回: int 写入字节数, error 容量错误
func (b *outputCompressionBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		b.exceeded = true
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(data)
}

// workerLoop 复用压缩器与读取缓冲并行处理预压缩任务
func (w *zipParallelWriter) workerLoop() {
	deflater := acquireOutputDeflater(io.Discard)
	defer releaseOutputDeflater(deflater)
	buffer := make([]byte, 32*1024)
	for entry := range w.jobs {
		if err := w.ctx.Err(); err != nil {
			entry.err = err
			close(entry.done)
			continue
		}
		w.prepareEntry(entry, deflater, buffer)
		close(entry.done)
	}
}

// prepareEntry 执行单个内存或原ZIP条目的预压缩与校验和计算
// 入参: entry 任务条目, deflater 复用压缩器, buffer 读取缓冲
func (w *zipParallelWriter) prepareEntry(entry *zipPreparedEntry, deflater *flate.Writer, buffer []byte) {
	if entry.file != nil {
		file := entry.file
		entry.header.Extra = outputZIPExtra(entry.header.Extra)
		limit := min(file.CompressedSize64, file.UncompressedSize64)
		compressed, err := compressOutputFileWithDeflater(w.ctx, deflater, file, buffer, int(limit))
		if err != nil {
			entry.err = err
			return
		}
		if compressed != nil {
			prepareOutputHeader(&entry.header, zip.Deflate, file.UncompressedSize64, uint64(len(compressed)), file.CRC32)
			entry.kind = zipPreparedBytes
			entry.payload = compressed
			entry.file = nil
		} else if file.UncompressedSize64 < file.CompressedSize64 {
			entry.kind = zipPreparedStoreFile
		} else {
			entry.kind = zipPreparedRawFile
		}
		return
	}
	data := entry.data
	payload := data
	method := uint16(zip.Store)
	if !isPrecompressedOutputExt(entry.header.Name) {
		compressed, err := compressOutputBytesWithDeflater(w.ctx, deflater, data, len(data))
		if err != nil {
			entry.err = err
			return
		}
		if compressed != nil {
			payload = compressed
			method = zip.Deflate
		}
	}
	prepareOutputHeader(&entry.header, method, uint64(len(data)), uint64(len(payload)), crc32.ChecksumIEEE(data))
	entry.kind = zipPreparedBytes
	entry.payload = payload
	entry.data = nil
}

// drainReady 非阻塞写出队列头部所有已就绪条目并释放内存
// 返回: error 写出或取消错误
func (w *zipParallelWriter) drainReady() error {
	for len(w.pending) > 0 {
		head := w.pending[0]
		if head.done != nil {
			select {
			case <-head.done:
			default:
				return nil
			}
		}
		if err := w.writeHead(); err != nil {
			return err
		}
	}
	return nil
}

// waitCapacity 按滑动窗口与在途字节预算等待队首写出，避免无界排队与内存膨胀
// 入参: estBytes 新任务估算字节数
// 返回: error 写出或取消错误
func (w *zipParallelWriter) waitCapacity(estBytes int64) error {
	if err := w.drainReady(); err != nil {
		return err
	}
	for len(w.pending) >= w.window || len(w.pending) > 0 && w.inFlight+estBytes > w.maxBytes {
		if err := w.writeHead(); err != nil {
			return err
		}
		if err := w.drainReady(); err != nil {
			return err
		}
	}
	return w.ctx.Err()
}

// writeHead 等待队首条目完成并按原序写入归档
// 返回: error 写出或取消错误
func (w *zipParallelWriter) writeHead() error {
	entry := w.pending[0]
	w.pending[0] = nil
	w.pending = w.pending[1:]
	if entry.done != nil {
		<-entry.done
	}
	w.inFlight -= entry.estBytes
	if entry.err != nil {
		return entry.err
	}
	if err := w.editor.output.ctx.Err(); err != nil {
		return err
	}
	switch entry.kind {
	case zipPreparedDir:
		_, err := w.archive.CreateHeader(&entry.header)
		return err
	case zipPreparedStaged:
		return w.editor.writeStagedPage(w.archive, entry.header, entry.staged)
	case zipPreparedLargeEntry:
		out, err := w.archive.CreateHeader(&entry.header)
		if err != nil {
			return err
		}
		err = w.editor.writeOutputBytes(out, entry.data)
		entry.data = nil
		return err
	case zipPreparedBytes:
		if err := editorProgress(w.editor.OnWriteProgress).report("compress", w.editor.output.completed, 0); err != nil {
			return err
		}
		out, err := w.archive.CreateRaw(&entry.header)
		if err != nil {
			return err
		}
		err = w.editor.writeOutputBytes(out, entry.payload)
		entry.payload = nil
		if err != nil {
			return err
		}
		w.editor.output.completed++
		return editorProgress(w.editor.OnWriteProgress).report("compress", w.editor.output.completed, 0)
	case zipPreparedRawFile:
		if err := editorProgress(w.editor.OnWriteProgress).report("compress", w.editor.output.completed, 0); err != nil {
			return err
		}
		payload, err := entry.file.OpenRaw()
		if err != nil {
			return err
		}
		out, err := w.archive.CreateRaw(&entry.header)
		if err != nil {
			return err
		}
		if _, err := io.CopyBuffer(out, imageInput{ReadCloser: io.NopCloser(payload), ctx: w.editor.output.ctx}, w.copyBuf); err != nil {
			return err
		}
		w.editor.output.completed++
		return editorProgress(w.editor.OnWriteProgress).report("compress", w.editor.output.completed, 0)
	case zipPreparedStoreFile:
		if err := editorProgress(w.editor.OnWriteProgress).report("compress", w.editor.output.completed, 0); err != nil {
			return err
		}
		input, err := entry.file.Open()
		if err != nil {
			return err
		}
		defer input.Close()
		prepareOutputHeader(&entry.header, zip.Store, entry.file.UncompressedSize64, entry.file.UncompressedSize64, entry.file.CRC32)
		out, err := w.archive.CreateRaw(&entry.header)
		if err != nil {
			return err
		}
		if _, err := io.CopyBuffer(out, imageInput{ReadCloser: io.NopCloser(input), ctx: w.editor.output.ctx}, w.copyBuf); err != nil {
			return err
		}
		w.editor.output.completed++
		return editorProgress(w.editor.OnWriteProgress).report("compress", w.editor.output.completed, 0)
	}
	return nil
}

// submitDir 按序提交目录条目，不阻塞后台预压缩
// 入参: header 目录条目属性
// 返回: error 写出或取消错误
func (w *zipParallelWriter) submitDir(header zip.FileHeader) error {
	if err := w.waitCapacity(0); err != nil {
		return err
	}
	w.pending = append(w.pending, &zipPreparedEntry{kind: zipPreparedDir, header: header})
	return nil
}

// submitStaged 按序提交已预压缩的自产页面条目
// 入参: header 条目属性, staged 暂存页面
// 返回: error 写出或取消错误
func (w *zipParallelWriter) submitStaged(header zip.FileHeader, staged *editorStagedPage) error {
	if err := w.waitCapacity(0); err != nil {
		return err
	}
	w.pending = append(w.pending, &zipPreparedEntry{kind: zipPreparedStaged, header: header, staged: staged})
	return nil
}

// submitEntry 按序提交内存条目，已压缩后缀直接计算校验和，其余交由worker并行预压缩
// 入参: header 条目属性, data 原始数据
// 返回: error 写出或取消错误
func (w *zipParallelWriter) submitEntry(header zip.FileHeader, data []byte) error {
	if len(data) > outputOptimizationBufferLimit {
		if err := w.waitCapacity(0); err != nil {
			return err
		}
		w.pending = append(w.pending, &zipPreparedEntry{kind: zipPreparedLargeEntry, header: header, data: data})
		return nil
	}
	est := int64(len(data)) + 4096
	if err := w.waitCapacity(est); err != nil {
		return err
	}
	entry := &zipPreparedEntry{
		kind:     zipPreparedBytes,
		header:   header,
		data:     data,
		estBytes: est,
		done:     make(chan struct{}),
	}
	w.pending = append(w.pending, entry)
	w.inFlight += est
	w.jobs <- entry
	return nil
}

// submitFile 按序提交未修改的原ZIP条目，已压缩后缀零解压直通，其余并行预压缩
// 入参: file 原始ZIP条目
// 返回: error 写出或取消错误
func (w *zipParallelWriter) submitFile(file *zip.File) error {
	header := file.FileHeader
	header.Extra = outputZIPExtra(header.Extra)
	if file.UncompressedSize64 > outputOptimizationBufferLimit {
		if err := w.waitCapacity(0); err != nil {
			return err
		}
		w.pending = append(w.pending, &zipPreparedEntry{kind: zipPreparedRawFile, header: header, file: file})
		return nil
	}
	if isPrecompressedOutputExt(file.Name) {
		if err := w.waitCapacity(0); err != nil {
			return err
		}
		kind := zipPreparedRawFile
		if file.UncompressedSize64 < file.CompressedSize64 {
			kind = zipPreparedStoreFile
		}
		w.pending = append(w.pending, &zipPreparedEntry{kind: kind, header: header, file: file})
		return nil
	}
	est := int64(min(file.UncompressedSize64, file.CompressedSize64)) + 4096
	if err := w.waitCapacity(est); err != nil {
		return err
	}
	entry := &zipPreparedEntry{
		kind:     zipPreparedRawFile,
		header:   header,
		file:     file,
		estBytes: est,
		done:     make(chan struct{}),
	}
	w.pending = append(w.pending, entry)
	w.inFlight += est
	w.jobs <- entry
	return nil
}

// flush 排空并按序写出队列中全部剩余条目
// 返回: error 写出或取消错误
func (w *zipParallelWriter) flush() error {
	for len(w.pending) > 0 {
		if err := w.writeHead(); err != nil {
			return err
		}
	}
	return w.editor.output.ctx.Err()
}

// close 取消并等待全部后台预压缩worker退出，可重复调用
func (w *zipParallelWriter) close() {
	if w == nil || w.closed {
		return
	}
	w.closed = true
	w.cancel()
	close(w.jobs)
	w.group.Wait()
}

// outputSnapshot 创建只读输出配置，原签名存在时仅调整ZIP封装
// 入参: ctx 取消上下文, options 输出配置
// 返回: *Editor 输出快照, error 配置或资源错误
func (e *Editor) outputSnapshot(ctx context.Context, options WriteOptions) (*Editor, error) {
	ctx = e.runtimeResources.context(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := options.Compression.Validate(); err != nil {
		return nil, err
	}
	snapshot := *e
	snapshot.OnWriteProgress = editorProgress(e.OnWriteProgress).withContext(ctx)
	snapshot.output = &outputOptimization{ctx: ctx, options: options.Compression}
	if options.Compression.Mode == CompressionUnchanged {
		return &snapshot, nil
	}
	if e.source != nil {
		protectedDocs, protectedFiles, protected, err := e.source.reader.outputSignatureProtection(ctx)
		if err != nil {
			return nil, err
		}
		snapshot.output.protected = protected
		snapshot.output.protectedDocs = protectedDocs
		snapshot.output.protectedFiles = protectedFiles
		if protected {
			return &snapshot, nil
		}
	}
	if options.Compression.Mode == CompressionLossy {
		reader, err := e.readerContext(ctx, editorProgress(snapshot.OnWriteProgress))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		snapshot.output.images, snapshot.output.sizes, err = reader.compressionImagePlanProtected(ctx, options.Compression, snapshot.output.protectedDocs, snapshot.output.protectedFiles)
		if err != nil {
			return nil, err
		}
	}
	if err := snapshot.compressPatternPaints(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// compressionImageSafety 按当前入口保护蒙版及其原图和替换图像，不完整的文档结构不参与降采样
// 入参: ctx 取消上下文, images 图片资源
// 返回: map[string]bool 只能无损处理的资源路径, bool 是否可分析尺寸, error 读取或取消错误
func (r *Reader) compressionImageSafety(ctx context.Context, images []ImageInfo) (map[string]bool, bool, error) {
	names := make(map[string]bool)
	safe := true
	structured := len(r.ofd.DocBody) != 0
	if structured {
		entry, err := r.DocumentRootContext(ctx)
		if err != nil {
			return nil, false, err
		}
		dependencies, structures, err := r.versionDependencies(ctx, entry, nil, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			safe = false
		}
		for name := range dependencies {
			if !r.fileNames[name] {
				safe = false
				break
			}
		}
		names = structures
	} else {
		for name := range r.fileIndex {
			names[name] = true
		}
		for name := range r.files {
			names[name] = true
		}
	}
	masks := make(map[string]bool)
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if !safe {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		input, err := r.openFile(name)
		if err != nil {
			return nil, false, err
		}
		buffer := bufio.NewReader(imageInput{ReadCloser: input, ctx: ctx})
		found, err := editorXMLPrefix(buffer)
		if err != nil || !found && (structured || strings.EqualFold(path.Ext(name), ".xml")) {
			safe = false
		}
		if safe && found {
			decoder := xml.NewDecoder(buffer)
			var stack []xml.Name
			seen := false
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					safe = seen && len(stack) == 0
					break
				}
				if err != nil {
					safe = false
					break
				}
				switch element := token.(type) {
				case xml.StartElement:
					if len(stack) == 0 && seen {
						safe = false
						break
					}
					seen = true
					if len(stack) != 0 && classifyOFDNamespace(element.Name.Space) == ofdXMLUnknown && element.Name.Space != stack[len(stack)-1].Space {
						safe = false
						break
					}
					if len(stack) != 0 && stack[len(stack)-1].Local == "Extension" && element.Name.Local == "Data" {
						if err := decoder.Skip(); err != nil {
							safe = false
						}
						break
					}
					stack = append(stack, element.Name)
					resource, substitution, masked := "", "", false
					for _, attr := range element.Attr {
						if attr.Name.Space != "" {
							continue
						}
						if attr.Name.Local == "ResourceID" {
							resource = editorResourceID(attr.Value)
						}
						if attr.Name.Local == "Substitution" {
							substitution = editorResourceID(attr.Value)
						}
						if attr.Name.Local == "ImageMask" {
							masks[editorResourceID(attr.Value)] = true
							masked = true
						}
					}
					if masked {
						masks[resource] = true
						if substitution != "" {
							masks[substitution] = true
						}
					}
				case xml.EndElement:
					stack = stack[:len(stack)-1]
				case xml.CharData:
					if len(stack) == 0 && len(bytes.Trim(element, " \t\r\n")) != 0 {
						safe = false
					}
				case xml.Directive:
					safe = false
				}
				if !safe {
					break
				}
			}
		}
		input.Close()
		if !safe {
			break
		}
	}
	paths := make(map[string]bool)
	for _, img := range images {
		if !safe || masks[editorResourceID(img.ID)] {
			paths[cleanPackagePath(img.Location)] = true
		}
	}
	return paths, safe, ctx.Err()
}

// signatureReferenceFiles 收集包内签名列表中全部签名文件、签名值、印章及Reference保护集路径
// excludeDoc大于等于0时跳过该索引对应的文档
// 入参: ctx 取消上下文, excludeDoc 排除文档索引
// 返回: map[string]bool 受保护文件路径, error 读取或取消错误
func (r *Reader) signatureReferenceFiles(ctx context.Context, excludeDoc int) (map[string]bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.ofd == nil {
		return nil, nil
	}
	protected := make(map[string]bool)
	for docIndex, body := range r.ofd.DocBody {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if docIndex == excludeDoc || strings.TrimSpace(body.Signatures) == "" {
			continue
		}
		sigListPath := cleanPackagePath(resolveResourcePath("OFD.xml", "", body.Signatures))
		if !validPackagePath(sigListPath) {
			return nil, fmt.Errorf("invalid signatures path: %s", body.Signatures)
		}
		sigListData, err := r.readFileViewContext(ctx, sigListPath)
		if err != nil {
			return nil, err
		}
		var sigs Signatures
		if err := xml.Unmarshal(sigListData, &sigs); err != nil {
			return nil, err
		}
		protected[sigListPath] = true
		for _, item := range sigs.List {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sigPath := cleanPackagePath(resolveResourcePath(sigListPath, "", item.BaseLoc))
			if !validPackagePath(sigPath) {
				return nil, fmt.Errorf("invalid signature path: %s", item.BaseLoc)
			}
			sigData, err := r.readFileViewContext(ctx, sigPath)
			if err != nil {
				return nil, err
			}
			sigFile, err := parseSignatureFile(sigData)
			if err != nil {
				return nil, err
			}
			protected[sigPath] = true
			if strings.TrimSpace(sigFile.SignedValue) != "" {
				if target := cleanPackagePath(resolveResourcePath(sigPath, "", sigFile.SignedValue)); validPackagePath(target) {
					protected[target] = true
				}
			}
			if strings.TrimSpace(sigFile.SignedInfo.Seal.BaseLoc) != "" {
				if target := cleanPackagePath(resolveResourcePath(sigPath, "", sigFile.SignedInfo.Seal.BaseLoc)); validPackagePath(target) {
					protected[target] = true
				}
			}
			for _, ref := range sigFile.SignedInfo.References.Reference {
				if strings.TrimSpace(ref.FileRef) == "" {
					continue
				}
				if target := cleanPackagePath(resolveResourcePath(sigPath, "", ref.FileRef)); validPackagePath(target) {
					protected[target] = true
				}
			}
		}
	}
	return protected, ctx.Err()
}

// hasOutputSignatures 检查全部文档的签名入口，不受当前文档或版本选择影响
// 入参: ctx 取消上下文
// 返回: bool 是否声明签名入口, error 取消错误
func (r *Reader) hasOutputSignatures(ctx context.Context) (bool, error) {
	for _, body := range r.ofd.DocBody {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if body.Signatures != "" {
			return true, nil
		}
	}
	return false, ctx.Err()
}

// outputSignatureProtection 细化多文档包的签名保护范围，未签名子文档允许正常压缩
// 入参: ctx 取消上下文
// 返回: map[int]bool 受签名文档索引, map[string]bool 受保护文件路径, bool 是否整包受保护, error 取消错误
func (r *Reader) outputSignatureProtection(ctx context.Context) (map[int]bool, map[string]bool, bool, error) {
	if r == nil || r.ofd == nil {
		return nil, nil, false, ctx.Err()
	}
	var protectedDocs map[int]bool
	for index, body := range r.ofd.DocBody {
		if err := ctx.Err(); err != nil {
			return nil, nil, false, err
		}
		if body.Signatures != "" {
			if protectedDocs == nil {
				protectedDocs = make(map[int]bool)
			}
			protectedDocs[index] = true
		}
	}
	if len(protectedDocs) == 0 {
		return nil, nil, false, ctx.Err()
	}
	if len(protectedDocs) == len(r.ofd.DocBody) {
		return protectedDocs, nil, true, ctx.Err()
	}
	protectedFiles := make(map[string]bool)
	signedRefs := make(map[string]bool)
	for docIndex := range protectedDocs {
		if err := ctx.Err(); err != nil {
			return nil, nil, false, err
		}
		body := r.ofd.DocBody[docIndex]
		sigListPath := cleanPackagePath(resolveResourcePath("OFD.xml", "", body.Signatures))
		sigListData, err := r.readFileViewContext(ctx, sigListPath)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			return protectedDocs, nil, true, nil
		}
		var sigs Signatures
		if err := xml.Unmarshal(sigListData, &sigs); err != nil {
			return protectedDocs, nil, true, nil
		}
		protectedFiles[sigListPath] = true
		signedRefs[sigListPath] = true
		for _, item := range sigs.List {
			if err := ctx.Err(); err != nil {
				return nil, nil, false, err
			}
			sigPath := cleanPackagePath(resolveResourcePath(sigListPath, "", item.BaseLoc))
			sigData, err := r.readFileViewContext(ctx, sigPath)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, false, ctx.Err()
				}
				return protectedDocs, nil, true, nil
			}
			var sigFile SignatureFile
			if err := xml.Unmarshal(sigData, &sigFile); err != nil {
				return protectedDocs, nil, true, nil
			}
			protectedFiles[sigPath] = true
			signedRefs[sigPath] = true
			if sigFile.SignedValue != "" {
				target := cleanPackagePath(resolveResourcePath(sigPath, "", sigFile.SignedValue))
				protectedFiles[target] = true
				signedRefs[target] = true
			}
			if sigFile.SignedInfo.Seal.BaseLoc != "" {
				target := cleanPackagePath(resolveResourcePath(sigPath, "", sigFile.SignedInfo.Seal.BaseLoc))
				protectedFiles[target] = true
				signedRefs[target] = true
			}
			for _, ref := range sigFile.SignedInfo.References.Reference {
				if ref.FileRef == "" {
					continue
				}
				target := cleanPackagePath(resolveResourcePath(sigPath, "", ref.FileRef))
				protectedFiles[target] = true
				signedRefs[target] = true
			}
		}
		entry, err := r.documentEntryContext(ctx, docIndex)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			return protectedDocs, nil, true, nil
		}
		deps, structures, err := r.versionDependencies(ctx, entry, nil, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
			return protectedDocs, nil, true, nil
		}
		for dep := range deps {
			protectedFiles[cleanPackagePath(dep)] = true
		}
		for name := range structures {
			clean := cleanPackagePath(name)
			if !signedRefs[clean] {
				continue
			}
			data, err := r.readFileViewContext(ctx, clean)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, false, ctx.Err()
				}
				return protectedDocs, nil, true, nil
			}
			root, err := parseEditorXMLContext(ctx, data)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, false, ctx.Err()
				}
				return protectedDocs, nil, true, nil
			}
			if root.name.Local != "Res" {
				continue
			}
			baseLoc := root.attr("BaseLoc")
			for _, group := range root.children {
				for _, node := range group.children {
					for _, field := range [2]string{"FontFile", "MediaFile"} {
						child := node.child(field)
						if child == nil {
							continue
						}
						var value string
						if err := xml.Unmarshal(data[child.start:child.end], &value); err != nil {
							return protectedDocs, nil, true, nil
						}
						target := editorResourceLocation(clean, baseLoc, value)
						if target != "" && !signedRefs[target] {
							return protectedDocs, nil, true, nil
						}
					}
				}
			}
		}
	}
	return protectedDocs, protectedFiles, false, ctx.Err()
}

// writeOutputEntry 优化ZIP编码，保留资源名称及条目的未压缩字节
// 入参: archive 归档写入器, header 条目属性, data 原始数据
// 返回: error 优化或写入错误
func (e *Editor) writeOutputEntry(archive *zip.Writer, header zip.FileHeader, data []byte) error {
	if e.output == nil || e.output.options.Mode == CompressionUnchanged || len(data) > outputOptimizationBufferLimit {
		entry, err := archive.CreateHeader(&header)
		if err != nil {
			return err
		}
		if e.output != nil {
			return e.writeOutputBytes(entry, data)
		}
		_, err = entry.Write(data)
		return err
	}
	if err := editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0); err != nil {
		return err
	}
	payload := data
	header.Method = zip.Store
	if !isPrecompressedOutputExt(header.Name) {
		compressed, err := e.compressOutputBytes(data, len(data))
		if err != nil {
			return err
		}
		if compressed != nil {
			payload = compressed
			header.Method = zip.Deflate
		}
	}
	prepareOutputHeader(&header, header.Method, uint64(len(data)), uint64(len(payload)), crc32.ChecksumIEEE(data))
	entry, err := archive.CreateRaw(&header)
	if err != nil {
		return err
	}
	if err = e.writeOutputBytes(entry, payload); err != nil {
		return err
	}
	e.output.completed++
	return editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0)
}

// writeOutputBytes 分块写出已编码数据，不额外分配复制缓冲
// 入参: writer 输出流, data 已编码字节
// 返回: error 写入或取消错误
func (e *Editor) writeOutputBytes(writer io.Writer, data []byte) error {
	for offset := 0; offset < len(data); {
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		end := min(offset+64<<10, len(data))
		n, err := writer.Write(data[offset:end])
		if err != nil {
			return err
		}
		if n != end-offset {
			return io.ErrShortWrite
		}
		offset = end
	}
	return e.output.ctx.Err()
}

// compressOutputBytes 复用压缩器并限制候选体积，无收益时返回空值
// 入参: data 未压缩数据, limit 候选体积上限
// 返回: []byte 更小的DEFLATE编码, error 取消或编码错误
func (e *Editor) compressOutputBytes(data []byte, limit int) ([]byte, error) {
	return compressOutputBytesWithDeflater(e.output.ctx, e.output.compressor(io.Discard), data, limit)
}

// compressOutputFile 分块重压缩ZIP条目，超过体积上限且可回退时立即早停
// 入参: file 原始条目, buffer 读取缓冲, limit 候选体积上限
// 返回: []byte 更小的DEFLATE编码, error 读取、编码或取消错误
func (e *Editor) compressOutputFile(file *zip.File, buffer []byte, limit int) ([]byte, error) {
	return compressOutputFileWithDeflater(e.output.ctx, e.output.compressor(io.Discard), file, buffer, limit)
}

// writeOutputFile 重压缩未修改条目，已压缩媒体后缀直通，只采用优于原ZIP编码的结果
// 入参: archive 输出归档, file 原始条目, buffer 流式复制缓冲
// 返回: error 读取、写入或取消错误
func (e *Editor) writeOutputFile(archive *zip.Writer, file *zip.File, buffer []byte) error {
	if err := editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0); err != nil {
		return err
	}
	header := file.FileHeader
	header.Extra = outputZIPExtra(header.Extra)
	var payload io.Reader
	if file.UncompressedSize64 <= outputOptimizationBufferLimit {
		if isPrecompressedOutputExt(file.Name) {
			if file.UncompressedSize64 < file.CompressedSize64 {
				input, err := file.Open()
				if err != nil {
					return err
				}
				defer input.Close()
				prepareOutputHeader(&header, zip.Store, file.UncompressedSize64, file.UncompressedSize64, file.CRC32)
				payload = input
			}
		} else {
			limit := min(file.CompressedSize64, file.UncompressedSize64)
			compressed, err := e.compressOutputFile(file, buffer, int(limit))
			if err != nil {
				return err
			}
			if compressed != nil {
				prepareOutputHeader(&header, zip.Deflate, file.UncompressedSize64, uint64(len(compressed)), file.CRC32)
				payload = bytes.NewReader(compressed)
			} else if file.UncompressedSize64 < file.CompressedSize64 {
				input, err := file.Open()
				if err != nil {
					return err
				}
				defer input.Close()
				prepareOutputHeader(&header, zip.Store, file.UncompressedSize64, file.UncompressedSize64, file.CRC32)
				payload = input
			}
		}
	}
	if payload == nil {
		var err error
		payload, err = file.OpenRaw()
		if err != nil {
			return err
		}
	}
	entry, err := archive.CreateRaw(&header)
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(entry, imageInput{ReadCloser: io.NopCloser(payload), ctx: e.output.ctx}, buffer); err != nil {
		return err
	}
	e.output.completed++
	return editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0)
}

// outputContext 返回当前渲染上下文，未设置时返回后台上下文
// 返回: context.Context 取消上下文
func (r *Renderer) outputContext() context.Context {
	if r != nil && r.pageContext != nil {
		return r.pageContext
	}
	return context.Background()
}
