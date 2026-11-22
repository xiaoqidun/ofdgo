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
	"compress/flate"
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
)

// archiveParallelMaxBytes 限制单条目并发预压缩的内存上限，超限文件回退主线程流式写出
const archiveParallelMaxBytes = 32 << 20

// archiveDefaultDeflaterPool 复用默认压缩等级编码器，不保留输出流引用
var archiveDefaultDeflaterPool = sync.Pool{
	New: func() any {
		writer, _ := flate.NewWriter(io.Discard, flate.DefaultCompression)
		return writer
	},
}

// ArchiveFile 描述已生成的归档文件，不关闭调用方提供的源读取器
type ArchiveFile struct {
	Name  string
	Input *Input
}

// ArchiveOptions 设置ZIP封装，压缩不重新编码归档内已生成的文档
type ArchiveOptions struct {
	Compression CompressionOptions
	OnProgress  func(completed, total int) error
}

// archiveEntryTask 保存单个待压缩归档条目的并发压缩结果
type archiveEntryTask struct {
	file ArchiveFile
	crc  uint32
	data []byte
	err  error
	done chan struct{}
}

// WriteArchive 分块归档文件，保持文件内容，有损选项在此仅提高ZIP压缩等级
// 入参: ctx 取消上下文, writer 输出流, files 文件列表, options 归档配置
// 返回: int64 已写字节数, error 读写错误，出错时应丢弃输出
func WriteArchive(ctx context.Context, writer io.Writer, files []ArchiveFile, options ArchiveOptions) (int64, error) {
	if err := options.Compression.Validate(); err != nil {
		return 0, err
	}
	names := make(map[string]bool, len(files))
	deflateCount := 0
	for _, file := range files {
		if !fs.ValidPath(file.Name) || file.Name == "." || strings.Contains(file.Name, "\\") || names[file.Name] || file.Input == nil {
			return 0, fmt.Errorf("invalid archive file %q", file.Name)
		}
		names[file.Name] = true
		if archiveMethod(file.Name) == zip.Deflate && file.Input.Size() <= archiveParallelMaxBytes {
			deflateCount++
		}
	}
	var count int64
	archive := zip.NewWriter(convertWriter{ctx: ctx, writer: writer, count: &count})
	configureArchive(archive, options.Compression)
	workers := min(deflateCount, conversionWorkers(ctx))
	if workers <= 1 {
		var buffer []byte
		for i, file := range files {
			if err := ctx.Err(); err != nil {
				return count, err
			}
			if options.OnProgress != nil {
				if err := options.OnProgress(i, len(files)); err != nil {
					return count, err
				}
			}
			if err := ctx.Err(); err != nil {
				return count, err
			}
			if buffer == nil {
				buffer = make([]byte, 32<<10)
			}
			if err := writeArchiveStream(ctx, archive, file, archiveMethod(file.Name), buffer); err != nil {
				return count, err
			}
		}
		if err := archive.Close(); err != nil {
			return count, err
		}
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if options.OnProgress != nil {
			if err := options.OnProgress(len(files), len(files)); err != nil {
				return count, err
			}
		}
		return count, ctx.Err()
	}
	level := flate.DefaultCompression
	if options.Compression.Mode != CompressionUnchanged {
		level = flate.BestCompression
	}
	tasks := make([]*archiveEntryTask, len(files))
	for i, file := range files {
		if archiveMethod(file.Name) == zip.Deflate && file.Input.Size() <= archiveParallelMaxBytes {
			tasks[i] = &archiveEntryTask{file: file, done: make(chan struct{})}
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tokens := make(chan struct{}, workers*2)
	jobs := make(chan *archiveEntryTask, workers)
	var workerWG sync.WaitGroup
	for range workers {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			var fw *flate.Writer
			if level == flate.BestCompression {
				fw = acquireOutputDeflater(io.Discard)
				defer releaseOutputDeflater(fw)
			} else {
				fw = archiveDefaultDeflaterPool.Get().(*flate.Writer)
				fw.Reset(io.Discard)
				defer func() {
					fw.Reset(io.Discard)
					archiveDefaultDeflaterPool.Put(fw)
				}()
			}
			copyBuf := make([]byte, 32<<10)
			for task := range jobs {
				if err := runCtx.Err(); err != nil {
					task.err = err
					close(task.done)
					continue
				}
				task.crc, task.data, task.err = compressArchiveEntry(runCtx, task.file.Input, fw, copyBuf)
				close(task.done)
			}
		}()
	}
	var producerWG sync.WaitGroup
	producerWG.Add(1)
	go func() {
		defer producerWG.Done()
		defer close(jobs)
		for i, task := range tasks {
			if task == nil {
				continue
			}
			select {
			case <-runCtx.Done():
				for _, remaining := range tasks[i:] {
					if remaining != nil {
						remaining.err = runCtx.Err()
						close(remaining.done)
					}
				}
				return
			case tokens <- struct{}{}:
			}
			select {
			case <-runCtx.Done():
				for _, remaining := range tasks[i:] {
					if remaining != nil {
						remaining.err = runCtx.Err()
						close(remaining.done)
					}
				}
				return
			case jobs <- task:
			}
		}
	}()
	finish := func() {
		cancel()
		producerWG.Wait()
		workerWG.Wait()
	}
	buffer := make([]byte, 32<<10)
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			finish()
			return count, err
		}
		if options.OnProgress != nil {
			if err := options.OnProgress(i, len(files)); err != nil {
				finish()
				return count, err
			}
		}
		if err := ctx.Err(); err != nil {
			finish()
			return count, err
		}
		if task := tasks[i]; task != nil {
			select {
			case <-task.done:
			case <-ctx.Done():
				finish()
				return count, ctx.Err()
			}
			if task.err != nil {
				finish()
				return count, task.err
			}
			header := zip.FileHeader{Name: file.Name}
			prepareOutputHeader(&header, zip.Deflate, uint64(file.Input.Size()), uint64(len(task.data)), task.crc)
			entry, err := archive.CreateRaw(&header)
			if err != nil {
				finish()
				return count, err
			}
			if _, err := io.CopyBuffer(entry, bytes.NewReader(task.data), buffer); err != nil {
				finish()
				return count, err
			}
			task.data = nil
			<-tokens
			continue
		}
		if err := writeArchiveStream(ctx, archive, file, archiveMethod(file.Name), buffer); err != nil {
			finish()
			return count, err
		}
	}
	finish()
	if err := archive.Close(); err != nil {
		return count, err
	}
	if err := ctx.Err(); err != nil {
		return count, err
	}
	if options.OnProgress != nil {
		if err := options.OnProgress(len(files), len(files)); err != nil {
			return count, err
		}
	}
	return count, ctx.Err()
}

// archiveMethod 按扩展名选择归档存储或压缩方式
// 入参: name 归档条目名称
// 返回: uint16 ZIP压缩方法
func archiveMethod(name string) uint16 {
	switch strings.ToLower(path.Ext(name)) {
	case ".ofd", ".pdf", ".png", ".jpg", ".jpeg":
		return zip.Store
	default:
		return zip.Deflate
	}
}

// compressArchiveEntry 单次读取输入并同时计算CRC32与Flate压缩流
// 入参: ctx 取消上下文, input 输入源, fw 可复用Flate写入器, buffer 读取缓冲
// 返回: uint32 IEEE校验值, []byte 压缩数据, error 读取或压缩错误
func compressArchiveEntry(ctx context.Context, input *Input, fw *flate.Writer, buffer []byte) (uint32, []byte, error) {
	size := input.Size()
	var output bytes.Buffer
	fw.Reset(&output)
	hash := crc32.NewIEEE()
	src := io.NewSectionReader(convertReader{ctx: ctx, input: input}, 0, size)
	written, err := io.CopyBuffer(io.MultiWriter(fw, hash), src, buffer)
	if err != nil {
		return 0, nil, err
	}
	if written != size {
		return 0, nil, io.ErrUnexpectedEOF
	}
	if err := fw.Close(); err != nil {
		return 0, nil, err
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	return hash.Sum32(), output.Bytes(), nil
}

// writeArchiveStream 以流式方式将单个条目写入ZIP归档
// 入参: ctx 取消上下文, archive 归档写入器, file 归档文件, method ZIP压缩方法, buffer 复制缓冲
// 返回: error 读写错误
func writeArchiveStream(ctx context.Context, archive *zip.Writer, file ArchiveFile, method uint16, buffer []byte) error {
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: file.Name, Method: method})
	if err != nil {
		return err
	}
	written, err := io.CopyBuffer(entry, io.NewSectionReader(convertReader{ctx: ctx, input: file.Input}, 0, file.Input.Size()), buffer)
	if err != nil {
		return err
	}
	if written != file.Input.Size() {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// configureArchive 统一归档压缩等级，在单个归档内复用压缩器
// 入参: archive 归档写入器, options 压缩配置
func configureArchive(archive *zip.Writer, options CompressionOptions) {
	if options.Mode != CompressionUnchanged {
		var compressor *flate.Writer
		archive.RegisterCompressor(zip.Deflate, func(writer io.Writer) (io.WriteCloser, error) {
			if compressor == nil {
				var err error
				compressor, err = flate.NewWriter(writer, flate.BestCompression)
				return compressor, err
			}
			compressor.Reset(writer)
			return compressor, nil
		})
	}
}
