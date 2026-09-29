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
	"compress/flate"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// ArchiveFile 描述已生成的归档文件，不关闭调用方提供的源读取器
type ArchiveFile struct {
	Name   string
	Source io.ReaderAt
	Size   int64
}

// ArchiveOptions 设置ZIP封装，压缩不重新编码归档内已生成的文档
type ArchiveOptions struct {
	Compression CompressionOptions
	OnProgress  func(completed, total int) error
}

// WriteArchive 分块归档文件，保持文件内容，有损选项在此仅提高ZIP压缩等级
// 入参: ctx 取消上下文, writer 输出流, files 文件列表, options 归档配置
// 返回: int64 已写字节数, error 读写错误，出错时应丢弃输出
func WriteArchive(ctx context.Context, writer io.Writer, files []ArchiveFile, options ArchiveOptions) (int64, error) {
	if err := options.Compression.Validate(); err != nil {
		return 0, err
	}
	names := map[string]bool{}
	for _, file := range files {
		if !fs.ValidPath(file.Name) || file.Name == "." || strings.Contains(file.Name, "\\") || names[file.Name] || file.Source == nil || file.Size < 0 {
			return 0, fmt.Errorf("invalid archive file %q", file.Name)
		}
		names[file.Name] = true
	}
	var count int64
	archive := zip.NewWriter(convertWriter{context: ctx, writer: writer, count: &count})
	configureArchive(archive, options.Compression)
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if options.OnProgress != nil {
			if err := options.OnProgress(i, len(files)); err != nil {
				return count, err
			}
		}
		method := uint16(zip.Deflate)
		switch strings.ToLower(path.Ext(file.Name)) {
		case ".ofd", ".pdf", ".png", ".jpg", ".jpeg":
			method = zip.Store
		}
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: file.Name, Method: method})
		if err != nil {
			return count, err
		}
		input := convertReader{context: ctx, source: file.Source}
		written, err := io.Copy(entry, io.NewSectionReader(input, 0, file.Size))
		if err != nil {
			return count, err
		}
		if written != file.Size {
			return count, io.ErrUnexpectedEOF
		}
	}
	if err := archive.Close(); err != nil {
		return count, err
	}
	if options.OnProgress != nil {
		return count, options.OnProgress(len(files), len(files))
	}
	return count, ctx.Err()
}

// configureArchive 统一逐页导出与批量归档的压缩等级
// 入参: archive 归档写入器, options 压缩配置
func configureArchive(archive *zip.Writer, options CompressionOptions) {
	if options.Mode != CompressionUnchanged {
		archive.RegisterCompressor(zip.Deflate, func(writer io.Writer) (io.WriteCloser, error) { return flate.NewWriter(writer, flate.BestCompression) })
	}
}
