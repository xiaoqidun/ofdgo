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
	"hash/crc32"
	"image"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

const (
	CompressionUnchanged = pdfgo.CompressionUnchanged
	CompressionLossless  = pdfgo.CompressionLossless
	CompressionLossy     = pdfgo.CompressionLossy
	CompressionLight     = pdfgo.CompressionLight
	CompressionMedium    = pdfgo.CompressionMedium
	CompressionStrong    = pdfgo.CompressionStrong
)

const outputOptimizationBufferLimit = 64 << 20

// CompressionMode 选择默认、无损或有损输出策略
type CompressionMode = pdfgo.CompressionMode

// CompressionLevel 选择轻压、均衡或强压，仅用于有损模式
type CompressionLevel = pdfgo.CompressionLevel

// CompressionOptions 配置输出压缩，默认模式沿用现有输出策略，不额外优化
// Quality和MaxDPI为零时按Level使用预设，不改变页面尺寸或整页导出精度
// 无损表示不引入额外损失，不保证跨格式转换可逆；有损不栅格化文字和矢量
type CompressionOptions = pdfgo.CompressionOptions

// WriteOptions 配置OFD输出副本，不修改编辑资源和撤销记录
type WriteOptions struct{ Compression CompressionOptions }

// outputOptimization 保存单次写出的配置、图片路径及压缩进度，复用压缩器
type outputOptimization struct {
	ctx       context.Context
	options   CompressionOptions
	images    map[string]bool
	sizes     map[string]image.Point
	protected bool
	completed int
	deflater  *flate.Writer
}

// outputCompressionBuffer 限制候选ZIP编码，不能缩小时终止本次压缩
type outputCompressionBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

// Write 写入有界候选编码，超过体积上限时返回短缓冲错误
// 入参: data 编码数据
// 返回: int 写入字节数, error 容量错误
func (b outputCompressionBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(data)
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
		snapshot.output.images, snapshot.output.sizes, err = reader.compressionImagePlan(ctx, options.Compression)
		if err != nil {
			return nil, err
		}
	}
	if err := snapshot.compressPatternPaints(); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// compressionImageSafety 保护蒙版资源，识别不能可靠降采样的图案及隐藏内容
// 入参: ctx 取消上下文, images 图片资源
// 返回: map[string]bool 只能无损处理的资源路径, bool 是否可分析尺寸, error 读取或取消错误
func (r *Reader) compressionImageSafety(ctx context.Context, images []ImageInfo) (map[string]bool, bool, error) {
	names := make(map[string]bool)
	safe := true
	if len(r.OFD.DocBody) != 0 {
		dependencies, err := r.documentFiles(ctx, r.DocumentIndex())
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			safe = false
		}
		for key := range dependencies {
			name, exists := r.fileNamesFold[key]
			if !exists {
				safe = false
				break
			}
			names[name] = true
		}
	} else {
		for name := range r.fileIndex {
			names[name] = true
		}
		for name := range r.files {
			names[name] = true
		}
	}
	masks := make(map[string]bool)
	geometry := true
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
					if element.Name.Space != "" && element.Name.Space != ofdNamespace && element.Name.Space != "http://www.ofdspec.org" {
						safe = false
						break
					}
					if element.Name.Local == "Pattern" {
						geometry = false
					}
					resource, masked := "", false
					for _, attr := range element.Attr {
						if attr.Name.Local == "Visible" && (attr.Value == "false" || attr.Value == "0") {
							geometry = false
						}
						if attr.Name.Local == "ResourceID" {
							resource = editorResourceID(attr.Value)
						}
						if attr.Name.Local == "ImageMask" {
							masks[editorResourceID(attr.Value)] = true
							masked = true
						}
					}
					if masked {
						masks[resource] = true
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
		if !safe || masks[editorResourceID(img.ID)] {
			paths[strings.ToLower(cleanPackagePath(img.Location))] = true
		}
	}
	return paths, safe && geometry, ctx.Err()
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
	compressed, err := e.compressOutputBytes(data, len(data))
	if err != nil {
		return err
	}
	payload := data
	header.Method = zip.Store
	if compressed != nil {
		payload = compressed
		header.Method = zip.Deflate
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
	var compressed bytes.Buffer
	output := outputCompressionBuffer{buffer: &compressed, limit: min(limit, outputOptimizationBufferLimit)}
	deflater := e.output.deflater
	if deflater == nil {
		deflater, _ = flate.NewWriter(output, flate.BestCompression)
		e.output.deflater = deflater
	} else {
		deflater.Reset(output)
	}
	defer deflater.Close()
	for offset := 0; offset < len(data); {
		if err := e.output.ctx.Err(); err != nil {
			return nil, err
		}
		end := min(offset+64<<10, len(data))
		if _, err := deflater.Write(data[offset:end]); err != nil {
			if err == io.ErrShortBuffer {
				return nil, e.output.ctx.Err()
			}
			return nil, err
		}
		offset = end
	}
	if err := deflater.Close(); err != nil {
		if err == io.ErrShortBuffer {
			return nil, e.output.ctx.Err()
		}
		return nil, err
	}
	if compressed.Len() >= limit {
		return nil, e.output.ctx.Err()
	}
	return compressed.Bytes(), e.output.ctx.Err()
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

// writeOutputFile 重压缩未修改条目，只采用优于原ZIP编码的结果
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
		input, err := file.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(imageInput{ReadCloser: input, context: e.output.ctx}, outputOptimizationBufferLimit+1))
		input.Close()
		if err != nil {
			return err
		}
		if uint64(len(data)) != file.UncompressedSize64 {
			return io.ErrUnexpectedEOF
		}
		limit := min(file.CompressedSize64, uint64(len(data)))
		compressed, err := e.compressOutputBytes(data, int(min(limit, outputOptimizationBufferLimit)))
		if err != nil {
			return err
		}
		if compressed != nil {
			prepareOutputHeader(&header, zip.Deflate, uint64(len(data)), uint64(len(compressed)), file.CRC32)
			payload = bytes.NewReader(compressed)
		} else if uint64(len(data)) < file.CompressedSize64 {
			prepareOutputHeader(&header, zip.Store, uint64(len(data)), uint64(len(data)), file.CRC32)
			payload = bytes.NewReader(data)
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
	if _, err := io.CopyBuffer(entry, imageInput{ReadCloser: io.NopCloser(payload), context: e.output.ctx}, buffer); err != nil {
		return err
	}
	e.output.completed++
	return editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0)
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
