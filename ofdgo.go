// Copyright 2025 肖其顿 (XIAO QI DUN)
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

// Package ofdgo 首个原生、全平台兼容的纯 Go 语言 OFD 渲染库
package ofdgo

import (
	"archive/zip"
	"io"
	"io/fs"

	"github.com/tdewolff/canvas"
)

// Open 打开OFD文件
// 入参: path 文件路径
// 返回: *Reader 阅读器实例, error 错误信息
func Open(path string) (*Reader, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	reader := &Reader{
		Path:   path,
		Zip:    &r.Reader,
		Closer: r,
	}
	if err := reader.initRoot(); err != nil {
		reader.Close()
		return nil, err
	}
	return reader, nil
}

// NewReader 从流创建一个 OFD 阅读器
// 入参: r IO读取器, size 数据大小
// 返回: *Reader 阅读器实例, error 错误信息
func NewReader(r io.ReaderAt, size int64) (*Reader, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	reader := &Reader{
		Zip: zr,
	}
	if err := reader.initRoot(); err != nil {
		return nil, err
	}
	return reader, nil
}

// NewRenderer 创建渲染器
// 入参: reader 阅读器, opts 渲染选项
// 返回: *Renderer 渲染器实例
func NewRenderer(reader *Reader, opts ...RendererOption) *Renderer {
	r := &Renderer{
		Reader:     reader,
		DPI:        300.0,
		DrawParams: reader.drawParamCache,
		FontMap:    make(map[string]*canvas.FontFamily),
	}
	for _, opt := range opts {
		opt(r)
	}
	r.initCommon()
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

// PageCount 获取文档总页数
// 入参: reader 阅读器
// 返回: int 页数
func PageCount(reader *Reader) int {
	doc, _ := reader.Doc()
	if doc == nil {
		return 0
	}
	return len(doc.Pages.Page)
}
