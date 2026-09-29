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
	"encoding/xml"
	"hash/crc32"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

const (
	CompressionUnchanged = pdfgo.CompressionUnchanged
	CompressionLossless  = pdfgo.CompressionLossless
	CompressionLossy     = pdfgo.CompressionLossy
)

// CompressionMode 选择不变、无损或有损输出，不改变导出分辨率
type CompressionMode = pdfgo.CompressionMode

// CompressionOptions 配置输出压缩，零值保留现有行为，Quality为有损质量1至100，0使用85
// 无损表示不引入额外损失，不保证跨格式转换可逆；有损仅降低图片画质，不栅格化文字和矢量
type CompressionOptions = pdfgo.CompressionOptions

// WriteOptions 配置OFD输出副本，不修改编辑资源和撤销记录
type WriteOptions struct{ Compression CompressionOptions }

// outputOptimization 保存单次写出的配置及图片路径，不缓存资源数据
type outputOptimization struct {
	ctx       context.Context
	options   CompressionOptions
	images    map[string]bool
	protected bool
}

// WriteToWithOptions 按压缩策略写出OFD，签名文档不额外改写资源内容
// 入参: ctx 取消上下文, writer 输出流, options 输出配置
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WriteToWithOptions(ctx context.Context, writer io.Writer, options WriteOptions) (int64, error) {
	snapshot, err := e.outputSnapshot(ctx, options)
	if err != nil {
		return 0, err
	}
	var count int64
	return snapshot.WriteTo(convertWriter{context: ctx, writer: writer, count: &count})
}

// WritePagesToWithOptions 按指定顺序另存页面并应用输出压缩
// 入参: ctx 取消上下文, writer 输出流, indexes 零基页码, options 输出配置
// 返回: int64 已写入字节数, error 错误信息
func (e *Editor) WritePagesToWithOptions(ctx context.Context, writer io.Writer, indexes []int, options WriteOptions) (int64, error) {
	snapshot, err := e.outputSnapshot(ctx, options)
	if err != nil {
		return 0, err
	}
	var count int64
	return snapshot.WritePagesTo(convertWriter{context: ctx, writer: writer, count: &count}, indexes)
}

// outputSnapshot 创建只读输出配置，原签名存在时仅调整ZIP封装
// 入参: ctx 取消上下文, options 输出配置
// 返回: *Editor 输出快照, error 配置或资源错误
func (e *Editor) outputSnapshot(ctx context.Context, options WriteOptions) (*Editor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := options.Compression.Validate(); err != nil {
		return nil, err
	}
	snapshot := *e
	snapshot.OnWriteProgress = func(stage string, completed, total int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return editorProgress(e.OnWriteProgress).report(stage, completed, total)
	}
	snapshot.output = &outputOptimization{ctx: ctx, options: options.Compression, images: map[string]bool{}}
	if options.Compression.Mode == CompressionUnchanged {
		return &snapshot, nil
	}
	for _, resource := range e.resources {
		if resource.image != nil {
			snapshot.output.images[strings.ToLower(cleanPackagePath(resource.name))] = true
		}
	}
	if e.source != nil {
		protected, err := e.source.reader.hasOutputSignatures()
		if err != nil {
			return nil, err
		}
		snapshot.output.protected = protected
		if protected {
			snapshot.output.images = nil
			return &snapshot, nil
		}
		images, err := e.source.reader.Images(ctx)
		if err != nil {
			return nil, err
		}
		for _, img := range images {
			snapshot.output.images[strings.ToLower(cleanPackagePath(img.Location))] = true
		}
	}
	if options.Compression.Mode == CompressionLossy {
		reader, err := e.reader(editorProgress(snapshot.OnWriteProgress))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		images, err := reader.Images(ctx)
		if err != nil {
			return nil, err
		}
		masks, err := reader.compressionMaskFiles(ctx, images)
		if err != nil {
			return nil, err
		}
		for name := range masks {
			snapshot.output.images[name] = false
		}
	}
	return &snapshot, nil
}

// compressionMaskFiles 查找包括模板和注释在内的独立蒙版资源，解析不完整时保留所有图片像素
// 入参: ctx 取消上下文, images 图片资源
// 返回: map[string]bool 只能无损处理的资源路径, error 读取或取消错误
func (r *Reader) compressionMaskFiles(ctx context.Context, images []ImageInfo) (map[string]bool, error) {
	names := make(map[string]bool)
	for name := range r.fileIndex {
		names[name] = true
	}
	for name := range r.files {
		names[name] = true
	}
	masks := make(map[string]bool)
	safe := true
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		input, err := r.openFile(name)
		if err != nil {
			return nil, err
		}
		buffer := bufio.NewReader(imageInput{ReadCloser: input, context: ctx})
		header, _ := buffer.Peek(512)
		header = bytes.TrimSpace(bytes.TrimPrefix(header, []byte{0xef, 0xbb, 0xbf}))
		if len(header) > 0 && header[0] == '<' {
			decoder := xml.NewDecoder(buffer)
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					safe = false
					break
				}
				if element, ok := token.(xml.StartElement); ok {
					for _, attr := range element.Attr {
						if attr.Name.Local == "ImageMask" {
							masks[strings.TrimSpace(attr.Value)] = true
						}
					}
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
		if !safe || masks[img.ID] {
			paths[strings.ToLower(cleanPackagePath(img.Location))] = true
		}
	}
	return paths, ctx.Err()
}

// hasOutputSignatures 检查包中各文档，避免改写其他文档签名保护的共享资源
// 返回: bool 是否存在签名, error 文档读取错误
func (r *Reader) hasOutputSignatures() (bool, error) {
	for _, body := range r.OFD.DocBody {
		if body.Signatures != "" {
			return true, nil
		}
		data, err := r.readFile(body.DocRoot)
		if err != nil {
			return false, err
		}
		var doc struct {
			Signatures string `xml:"Signatures"`
		}
		if err := xml.Unmarshal(data, &doc); err != nil {
			return false, err
		}
		if doc.Signatures != "" {
			return true, nil
		}
	}
	return false, nil
}

// writeOutputEntry 逐项优化图片与ZIP编码，保留资源名称及其他文件的原始字节
// 入参: archive 归档写入器, header 条目属性, data 原始数据
// 返回: error 优化或写入错误
func (e *Editor) writeOutputEntry(archive *zip.Writer, header zip.FileHeader, data []byte) error {
	if e.output == nil || e.output.options.Mode == CompressionUnchanged || len(data) > 64<<20 {
		entry, err := archive.CreateHeader(&header)
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}
	if err := editorProgress(e.OnWriteProgress).report("compress", 0, 0); err != nil {
		return err
	}
	if lossy, ok := e.output.images[strings.ToLower(cleanPackagePath(header.Name))]; ok {
		options := e.output.options
		if !lossy {
			options.Mode = CompressionLossless
		}
		candidate, err := pdfgo.OptimizeImage(e.output.ctx, data, options)
		if err != nil {
			if e.output.ctx.Err() != nil {
				return e.output.ctx.Err()
			}
		} else {
			data = candidate
		}
	}
	var compressed bytes.Buffer
	deflater, _ := flate.NewWriter(&compressed, flate.BestCompression)
	for offset := 0; offset < len(data); {
		if err := e.output.ctx.Err(); err != nil {
			deflater.Close()
			return err
		}
		end := min(offset+64<<10, len(data))
		if _, err := deflater.Write(data[offset:end]); err != nil {
			return err
		}
		offset = end
	}
	if err := deflater.Close(); err != nil {
		return err
	}
	payload := data
	header.Method = zip.Store
	if compressed.Len() < len(data) {
		payload = compressed.Bytes()
		header.Method = zip.Deflate
	}
	header.CRC32 = crc32.ChecksumIEEE(data)
	header.UncompressedSize64 = uint64(len(data))
	header.CompressedSize64 = uint64(len(payload))
	header.Flags &^= 8
	entry, err := archive.CreateRaw(&header)
	if err != nil {
		return err
	}
	_, err = entry.Write(payload)
	return err
}

// WithCompression 设置导出策略，不修改文档资源和渲染缓存
// 入参: options 压缩配置，写出时校验
// 返回: RendererOption 渲染器配置
func WithCompression(options CompressionOptions) RendererOption {
	return func(r *Renderer) { r.Compression = options }
}

// writeCompressedImage 优化已经编码的图片，再交付调用方输出流
// 入参: writer 输出流, data 图片数据
// 返回: error 优化或写入错误
func (r *Renderer) writeCompressedImage(writer io.Writer, data []byte) error {
	optimized, err := pdfgo.OptimizeImage(r.outputContext(), data, r.Compression)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, bytes.NewReader(optimized))
	return err
}

// outputContext 获取仅用于输出的取消上下文
// 返回: context.Context 取消上下文
func (r *Renderer) outputContext() context.Context {
	if r.OutputContext != nil {
		return r.OutputContext
	}
	return context.Background()
}
