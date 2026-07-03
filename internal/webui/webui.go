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

package webui

import (
	"bytes"
	"fmt"

	"github.com/xiaoqidun/ofdgo"
)

// OpenOptions 打开OFD文档选项
type OpenOptions struct {
	Fonts []FontFile
}

// FontFile 字体文件
type FontFile = ofdgo.FontFile

// FontInfo OFD字体信息
type FontInfo = ofdgo.FontInfo

// Session WebUI文档会话
type Session struct {
	Reader   *ofdgo.Reader
	Renderer *ofdgo.Renderer
	doc      *ofdgo.Document
}

// DocumentInfo 文档信息
type DocumentInfo struct {
	Version      string     `json:"version"`
	DocType      string     `json:"docType"`
	Title        string     `json:"title"`
	Author       string     `json:"author"`
	Subject      string     `json:"subject"`
	CreationDate string     `json:"creationDate"`
	ModDate      string     `json:"modDate"`
	PageCount    int        `json:"pageCount"`
	FontCount    int        `json:"fontCount"`
	Fonts        []FontInfo `json:"fonts"`
	Pages        []PageInfo `json:"pages"`
}

// PageInfo 页面信息
type PageInfo struct {
	Index  int     `json:"index"`
	ID     string  `json:"id"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// PageSVG 页面SVG结果
type PageSVG struct {
	Index  int     `json:"index"`
	Number int     `json:"number"`
	ID     string  `json:"id"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	SVG    string  `json:"svg"`
}

// Open 打开浏览器内存中的OFD文档
// 入参: data OFD文件数据, opts 打开选项
// 返回: *Session 文档会话, error 错误信息
func Open(data []byte, opts OpenOptions) (*Session, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty ofd data")
	}
	reader, err := ofdgo.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	doc, err := reader.Doc()
	if err != nil {
		reader.Close()
		return nil, err
	}
	var rendererOptions []ofdgo.RendererOption
	if len(opts.Fonts) > 0 {
		fontFS := ofdgo.NewFontFS(opts.Fonts)
		if fontFS.Len() == 0 {
			reader.Close()
			return nil, fmt.Errorf("invalid font file")
		}
		rendererOptions = append(rendererOptions, ofdgo.WithFontFS(fontFS))
	}
	return &Session{Reader: reader, Renderer: ofdgo.NewRenderer(reader, rendererOptions...), doc: doc}, nil
}

// Close 关闭文档会话
// 返回: error 错误信息
func (s *Session) Close() error {
	if s == nil || s.Reader == nil {
		return nil
	}
	return s.Reader.Close()
}

// Info 获取文档信息
// 返回: DocumentInfo 文档信息
func (s *Session) Info() DocumentInfo {
	info := DocumentInfo{
		Version:   s.Reader.Version(),
		DocType:   s.Reader.DocType(),
		PageCount: len(s.doc.Pages.Page),
		Pages:     make([]PageInfo, 0, len(s.doc.Pages.Page)),
	}
	if fonts, err := s.Renderer.FontInfos(); err == nil {
		info.Fonts = fonts
	}
	info.FontCount = len(info.Fonts)
	if docInfo, err := s.Reader.DocInfo(); err == nil && docInfo != nil {
		info.Title = docInfo.Title
		info.Author = docInfo.Author
		info.Subject = docInfo.Subject
		info.CreationDate = docInfo.CreationDate
		info.ModDate = docInfo.ModDate
	}
	for index, pageRef := range s.doc.Pages.Page {
		pageInfo := PageInfo{Index: index, ID: pageRef.ID}
		if page, err := s.Reader.PageContent(pageRef); err == nil {
			if box, err := s.Renderer.GetPageBox(page); err == nil {
				pageInfo.Width = box.W
				pageInfo.Height = box.H
			}
		}
		info.Pages = append(info.Pages, pageInfo)
	}
	return info
}

// RenderPageSVG 渲染页面为SVG
// 入参: index 页面索引
// 返回: PageSVG 页面SVG结果, error 错误信息
func (s *Session) RenderPageSVG(index int) (PageSVG, error) {
	if s == nil || s.Reader == nil || s.Renderer == nil || s.doc == nil {
		return PageSVG{}, fmt.Errorf("ofd document is not opened")
	}
	if index < 0 || index >= len(s.doc.Pages.Page) {
		return PageSVG{}, fmt.Errorf("page index %d out of range", index)
	}
	pageRef := s.doc.Pages.Page[index]
	page, err := s.Reader.PageContent(pageRef)
	if err != nil {
		return PageSVG{}, err
	}
	box, err := s.Renderer.GetPageBox(page)
	if err != nil {
		return PageSVG{}, err
	}
	var buf bytes.Buffer
	if err := s.Renderer.RenderToSVG(page, &buf); err != nil {
		return PageSVG{}, err
	}
	return PageSVG{Index: index, Number: index + 1, ID: pageRef.ID, Width: box.W, Height: box.H, SVG: buf.String()}, nil
}

// ExportPDF 导出整个文档为PDF
// 返回: []byte PDF文件数据, error 错误信息
func (s *Session) ExportPDF() ([]byte, error) {
	if s == nil || s.Reader == nil || s.Renderer == nil || s.doc == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	var buf bytes.Buffer
	if err := s.Renderer.RenderToMultiPagePDF(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
