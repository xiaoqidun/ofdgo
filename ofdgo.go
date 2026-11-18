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

// Package ofdgo 首个原生、全平台、纯 Go 语言高性能 OFD 引擎
package ofdgo

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"os"
	"runtime"
	"sync"

	basefile "github.com/xiaoqidun/ofdgo/internal/base/file"
)

// ErrSameFile 表示输出流指向当前正在读取的输入文件
var ErrSameFile = errors.New("output refers to the active input file")

// Input 统一文件、内存及顺序流的随机读取，可并发读取和关闭，不可复制
// 调用方应在阅读、编辑及转换结束后调用Close，借入的数据需在此期间保持不变
// 借入的随机读取器应支持并发ReadAt，Close不会关闭调用方传入的读取器
type Input struct {
	mu     sync.RWMutex
	reader io.ReaderAt
	size   int64
	path   string
	file   *os.File
	info   os.FileInfo
	temp   bool
}

// InputOptions 设置顺序输入的缓存及大小限制，单位为字节
// MemoryLimit为0时使用8MiB，浏览器WASM仅用内存；负值表示仅用内存
// TempDir为空时使用系统临时目录，不自动创建调用方指定的目录
// MaxSize为0时不限制输入大小，OpenFile和ReadInput均检查此限制
// 无临时文件系统的平台可显式使用仅内存模式，并通过MaxSize限制输入
type InputOptions struct {
	MemoryLimit int64
	MaxSize     int64
	TempDir     string
}

// OpenFile 打开文件输入，不复制文件内容，Close释放文件句柄
// 入参: ctx 取消上下文, path 文件路径, options 输入选项
// 返回: *Input 输入源, error 打开或大小错误
func OpenFile(ctx context.Context, path string, options InputOptions) (*Input, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := basefile.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("input is not a regular file")
	}
	size := info.Size()
	if size < 0 {
		f.Close()
		return nil, fmt.Errorf("invalid input")
	}
	input := &Input{reader: f, size: size, path: path, file: f, info: info}
	if err := input.checkOptions(ctx, options); err != nil {
		input.Close()
		return nil, err
	}
	return input, nil
}

// NewInput 借用指定长度的随机读取源，不复制数据或接管其生命周期
// 入参: reader 随机读取器, size 数据大小
// 返回: *Input 输入源, error 参数错误
func NewInput(reader io.ReaderAt, size int64) (*Input, error) {
	if reader == nil || size < 0 {
		return nil, fmt.Errorf("invalid input")
	}
	var fileInfo os.FileInfo
	if stated, ok := reader.(interface{ Stat() (os.FileInfo, error) }); ok {
		if info, err := stated.Stat(); err == nil && info.Mode().IsRegular() {
			fileInfo = info
		}
	}
	return &Input{reader: reader, size: size, info: fileInfo}, nil
}

// ReadInput 从当前位置接收顺序输入，超过内存阈值后转入临时文件
// 已知大小且支持随机访问的可定位读取器直接复用，不移动其当前位置
// 不关闭传入流；取消无法中断阻塞的Read，调用方应自行设置超时或关闭流
// 入参: ctx 取消上下文, reader 输入流, options 输入选项
// 返回: *Input 输入源, error 读取、缓存或取消错误
func ReadInput(ctx context.Context, reader io.Reader, options InputOptions) (*Input, error) {
	if reader == nil || options.MaxSize < 0 {
		return nil, fmt.Errorf("invalid input options")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if random, ok := reader.(io.ReaderAt); ok {
		if seeker, ok := reader.(io.Seeker); ok {
			var size int64 = -1
			var fileInfo os.FileInfo
			if sized, ok := reader.(interface{ Size() int64 }); ok {
				size = sized.Size()
			} else if stated, ok := reader.(interface{ Stat() (os.FileInfo, error) }); ok {
				info, err := stated.Stat()
				if err != nil {
					return nil, err
				}
				if info.Mode().IsRegular() {
					size = info.Size()
					fileInfo = info
				}
			}
			if size >= 0 {
				offset, err := seeker.Seek(0, io.SeekCurrent)
				if err != nil {
					return nil, err
				}
				if offset < 0 || offset > size {
					return nil, fmt.Errorf("invalid input offset")
				}
				source := random
				if offset != 0 {
					source = io.NewSectionReader(random, offset, size-offset)
					fileInfo = nil
				}
				input := &Input{reader: source, size: size - offset, info: fileInfo}
				if err := input.checkOptions(ctx, options); err != nil {
					return nil, err
				}
				return input, nil
			}
		}
	}
	limit := options.MemoryLimit
	if limit == 0 {
		limit = 8 << 20
		if runtime.GOOS == "js" {
			limit = -1
		}
	}
	input := &Input{}
	success := false
	defer func() {
		if !success {
			input.Close()
		}
	}()
	var memory bytes.Buffer
	buffer := make([]byte, 64<<10)
	empty := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := reader.Read(buffer)
		if n < 0 || n > len(buffer) {
			return nil, fmt.Errorf("invalid input read count")
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if failure := ctx.Err(); failure != nil {
			return nil, failure
		}
		if n > 0 {
			empty = 0
			if input.size > int64(^uint64(0)>>1)-int64(n) || options.MaxSize > 0 && int64(n) > options.MaxSize-input.size {
				return nil, fmt.Errorf("input exceeds size limit")
			}
			if input.file == nil && limit >= 0 && input.size+int64(n) > limit {
				file, failure := basefile.NewTemp(options.TempDir)
				if failure != nil {
					return nil, failure
				}
				input.file, input.temp = file, true
				if _, failure = file.Write(memory.Bytes()); failure != nil {
					return nil, failure
				}
				memory = bytes.Buffer{}
			}
			var output io.Writer = &memory
			if input.file != nil {
				output = input.file
			}
			if _, failure := output.Write(buffer[:n]); failure != nil {
				return nil, failure
			}
			input.size += int64(n)
		} else if err == nil {
			empty++
			if empty >= 100 {
				return nil, io.ErrNoProgress
			}
		}
		if err != nil {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input.reader = bytes.NewReader(memory.Bytes())
	if input.file != nil {
		input.reader = input.file
	}
	success = true
	return input, nil
}

// Size 返回输入字节数，关闭后仍可查询
// 返回: int64 数据大小
func (s *Input) Size() int64 {
	return s.size
}

// ReadAt 读取指定位置的数据，关闭后返回os.ErrClosed
// 入参: data 目标缓冲区, offset 字节偏移
// 返回: int 读取字节数, error 读取错误
func (s *Input) ReadAt(data []byte, offset int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.reader == nil {
		return 0, os.ErrClosed
	}
	return readBoundedAt(s.reader, s.size, data, offset)
}

// Close 释放自有文件和缓存，等待正在进行的读取结束，可重复调用
// 返回: error 文件关闭或临时文件删除错误
func (s *Input) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reader = nil
	if s.file == nil {
		return nil
	}
	file := s.file
	s.file = nil
	err := file.Close()
	if s.temp {
		err = errors.Join(err, os.Remove(file.Name()))
	}
	return err
}

// NewReader 从统一输入源创建OFD阅读器，不接管输入源生命周期
// 阅读器及其派生编辑器使用结束后，调用方再关闭输入源
// 入参: input 输入源, options 阅读选项
// 返回: *Reader 阅读器实例, error 错误信息
func NewReader(input *Input, options ...ReaderOption) (*Reader, error) {
	if input == nil {
		return nil, fmt.Errorf("missing input")
	}
	zr, err := zip.NewReader(input, input.Size())
	if err != nil {
		return nil, err
	}
	reader := &Reader{
		Path:       input.path,
		Zip:        zr,
		sourceFile: input.info,
	}
	if err := reader.open(options); err != nil {
		return nil, err
	}
	return reader, nil
}

// NewRenderer 创建渲染器
// 入参: reader 阅读器, opts 渲染选项
// 返回: *Renderer 渲染器实例
func NewRenderer(reader *Reader, opts ...RendererOption) *Renderer {
	if reader.resources == nil {
		reader.resources, _ = NewResources(ResourceOptions{})
	}
	r := &Renderer{
		Reader:                reader,
		DPI:                   300.0,
		RenderAnnotations:     true,
		DrawParams:            reader.drawParamCache,
		CompositeGraphicUnits: reader.compositeGraphicUnitCache,
		templatePageCache:     make(map[string]*PageContent),
		imageCache:            resourceCache[string, image.Image](reader.resources, imageCacheLimit),
		backends:              defaultRenderBackends(),
		backendStates:         make(map[any]any),
	}
	r.resetFontCache()
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithDPI 设置渲染DPI
// 入参: dpi DPI值
// 返回: RendererOption 渲染选项
func WithDPI(dpi float64) RendererOption {
	return func(r *Renderer) {
		r.DPI = dpi
	}
}

// WithAnnotations 设置是否渲染注释外观
// 入参: enable 是否渲染
// 返回: RendererOption 渲染选项
func WithAnnotations(enable bool) RendererOption {
	return func(r *Renderer) {
		r.RenderAnnotations = enable
	}
}

// WithFontDirs 设置外部字体查找目录
// 入参: dirs 字体目录列表
// 返回: RendererOption 渲染选项
func WithFontDirs(dirs ...string) RendererOption {
	return func(r *Renderer) {
		r.fontDirs = append(r.fontDirs, dirs...)
	}
}

// WithFontFS 设置外部字体文件系统
// 入参: fs 字体文件系统
// 返回: RendererOption 渲染选项
func WithFontFS(fs ...fs.FS) RendererOption {
	return func(r *Renderer) {
		r.fontFS = append(r.fontFS, fs...)
	}
}

// checkOptions 检查取消状态及输入大小
// 入参: ctx 取消上下文, options 输入选项
// 返回: error 取消或大小错误
func (s *Input) checkOptions(ctx context.Context, options InputOptions) error {
	if options.MaxSize < 0 || options.MaxSize > 0 && s.size > options.MaxSize {
		return fmt.Errorf("input exceeds size limit")
	}
	return ctx.Err()
}

// readBoundedAt 在固定长度区间内执行随机读取，避免构造临时SectionReader
// 入参: reader 底层随机读取器, limit 数据长度上限, data 目标缓冲区, offset 字节偏移
// 返回: int 读取字节数, error 读取或越界错误
func readBoundedAt(reader io.ReaderAt, limit int64, data []byte, offset int64) (int, error) {
	if offset < 0 || offset >= limit {
		return 0, io.EOF
	}
	maxLen := limit - offset
	if int64(len(data)) > maxLen {
		n, err := reader.ReadAt(data[:maxLen], offset)
		if err == nil {
			err = io.EOF
		}
		return n, err
	}
	return reader.ReadAt(data, offset)
}

// sameOutputFile 检查输出流是否与活动输入指向同一底层文件
// 入参: source 源文件信息, writer 输出流
// 返回: error 指向同一文件时返回ErrSameFile
func sameOutputFile(source os.FileInfo, writer io.Writer) error {
	if source == nil || writer == nil {
		return nil
	}
	stated, ok := writer.(interface{ Stat() (os.FileInfo, error) })
	if !ok {
		return nil
	}
	target, err := stated.Stat()
	if err != nil || target == nil || !target.Mode().IsRegular() {
		return nil
	}
	if os.SameFile(source, target) {
		return ErrSameFile
	}
	return nil
}
