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
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strings"

	"github.com/xiaoqidun/ofdgo"
)

// OpenOptions 打开OFD文档选项
type OpenOptions struct {
	Fonts             []FontFile
	RenderAnnotations bool
}

// FontFile 字体文件
type FontFile = ofdgo.FontFile

// FontInfo OFD字体信息
type FontInfo = ofdgo.FontInfo

// SignatureInfo 签名验证信息
type SignatureInfo struct {
	ID                string               `json:"id"`
	Type              string               `json:"type"`
	Valid             bool                 `json:"valid"`
	Version           string               `json:"version,omitempty"`
	SealType          string               `json:"sealType,omitempty"`
	Signer            string               `json:"signer,omitempty"`
	SignatureDateTime string               `json:"signatureDateTime,omitempty"`
	Provider          string               `json:"provider,omitempty"`
	Company           string               `json:"company,omitempty"`
	DataHashOK        bool                 `json:"dataHashOK"`
	SignedValueOK     bool                 `json:"signedValueOK"`
	SealOK            bool                 `json:"sealOK"`
	SealMatchOK       bool                 `json:"sealMatchOK"`
	CertOK            bool                 `json:"certOK"`
	ReferenceCount    int                  `json:"referenceCount"`
	ReferencePassed   int                  `json:"referencePassed"`
	SignSerial        string               `json:"signSerial,omitempty"`
	SignatureMethod   string               `json:"signatureMethod,omitempty"`
	DigestMethod      string               `json:"digestMethod,omitempty"`
	SignSubject       string               `json:"signSubject,omitempty"`
	SignIssuer        string               `json:"signIssuer,omitempty"`
	SealSubject       string               `json:"sealSubject,omitempty"`
	Stamps            []SignatureStampInfo `json:"stamps,omitempty"`
	Error             string               `json:"error,omitempty"`
}

// SignatureStampInfo 签名外观信息
type SignatureStampInfo struct {
	ID       string  `json:"id,omitempty"`
	Page     int     `json:"page,omitempty"`
	PageID   string  `json:"pageId,omitempty"`
	Boundary string  `json:"boundary,omitempty"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	Width    float64 `json:"width,omitempty"`
	Height   float64 `json:"height,omitempty"`
}

// Session WebUI文档会话
type Session struct {
	Reader    *ofdgo.Reader
	Renderer  *ofdgo.Renderer
	doc       *ofdgo.Document
	pageCache map[int]*ofdgo.PageContent
	boxCache  map[int]ofdgo.Box
}

// DocumentInfo 文档信息
type DocumentInfo struct {
	Version        string          `json:"version"`
	DocType        string          `json:"docType"`
	Title          string          `json:"title"`
	Author         string          `json:"author"`
	Subject        string          `json:"subject"`
	CreationDate   string          `json:"creationDate"`
	ModDate        string          `json:"modDate"`
	PageCount      int             `json:"pageCount"`
	FontCount      int             `json:"fontCount"`
	SignatureCount int             `json:"signatureCount"`
	SignatureError string          `json:"signatureError,omitempty"`
	Fonts          []FontInfo      `json:"fonts"`
	Signatures     []SignatureInfo `json:"signatures"`
	Pages          []PageInfo      `json:"pages"`
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

// ExportFormat 导出格式
type ExportFormat struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	Extension string `json:"extension"`
	MIME      string `json:"mime"`
}

// supportedExportFormats 导出格式列表
var supportedExportFormats = []ExportFormat{
	{Value: "svg", Label: "SVG", Extension: "svg", MIME: "image/svg+xml"},
	{Value: "pdf", Label: "PDF", Extension: "pdf", MIME: "application/pdf"},
	{Value: "eps", Label: "EPS", Extension: "eps", MIME: "application/postscript"},
	{Value: "png", Label: "PNG", Extension: "png", MIME: "image/png"},
	{Value: "jpg", Label: "JPEG", Extension: "jpg", MIME: "image/jpeg"},
}

// ExportFormats 获取导出格式
// 返回: []ExportFormat 导出格式列表
func ExportFormats() []ExportFormat {
	formats := make([]ExportFormat, len(supportedExportFormats))
	copy(formats, supportedExportFormats)
	return formats
}

// exportFormat 获取导出格式
// 入参: value 格式值
// 返回: ExportFormat 导出格式, bool 是否支持
func exportFormat(value string) (ExportFormat, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "jpeg" {
		value = "jpg"
	}
	for _, format := range supportedExportFormats {
		if format.Value == value {
			return format, true
		}
	}
	return ExportFormat{}, false
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
	rendererOptions = append(rendererOptions, ofdgo.WithAnnotations(opts.RenderAnnotations))
	return &Session{
		Reader:    reader,
		Renderer:  ofdgo.NewRenderer(reader, rendererOptions...),
		doc:       doc,
		pageCache: make(map[int]*ofdgo.PageContent),
		boxCache:  make(map[int]ofdgo.Box),
	}, nil
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
	if signatures, err := s.signatureInfos(); err == nil {
		info.Signatures = signatures
	} else {
		info.SignatureError = err.Error()
	}
	info.SignatureCount = len(info.Signatures)
	if docInfo, err := s.Reader.DocInfo(); err == nil && docInfo != nil {
		info.Title = docInfo.Title
		info.Author = docInfo.Author
		info.Subject = docInfo.Subject
		info.CreationDate = docInfo.CreationDate
		info.ModDate = docInfo.ModDate
	}
	for index, pageRef := range s.doc.Pages.Page {
		pageInfo := PageInfo{Index: index, ID: pageRef.ID}
		if _, page, err := s.pageContent(index); err == nil {
			if box, err := s.pageBox(index, page); err == nil {
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
	pageRef, page, err := s.pageContent(index)
	if err != nil {
		return PageSVG{}, err
	}
	box, err := s.pageBox(index, page)
	if err != nil {
		return PageSVG{}, err
	}
	var buf bytes.Buffer
	if err := s.Renderer.RenderToSVG(page, &buf); err != nil {
		return PageSVG{}, err
	}
	return PageSVG{Index: index, Number: index + 1, ID: pageRef.ID, Width: box.W, Height: box.H, SVG: buf.String()}, nil
}

// ExportPage 导出单页
// 入参: index 页面索引, value 导出格式
// 返回: []byte 文件数据, ExportFormat 导出格式, error 错误信息
func (s *Session) ExportPage(index int, value string) ([]byte, ExportFormat, error) {
	format, ok := exportFormat(value)
	if !ok {
		return nil, ExportFormat{}, fmt.Errorf("unsupported export format %s", value)
	}
	_, page, err := s.pageContent(index)
	if err != nil {
		return nil, ExportFormat{}, err
	}
	var buf bytes.Buffer
	switch format.Value {
	case "svg":
		err = s.Renderer.RenderToSVG(page, &buf)
	case "pdf":
		err = s.Renderer.RenderToPDF(page, &buf)
	case "eps":
		err = s.Renderer.RenderToEPS(page, &buf)
	case "png":
		img, renderErr := s.Renderer.RenderToImage(page)
		if renderErr != nil {
			return nil, ExportFormat{}, renderErr
		}
		err = png.Encode(&buf, img)
	case "jpg":
		img, renderErr := s.Renderer.RenderToImage(page)
		if renderErr != nil {
			return nil, ExportFormat{}, renderErr
		}
		err = jpeg.Encode(&buf, imageWithWhiteBackground(img), &jpeg.Options{Quality: 95})
	}
	if err != nil {
		return nil, ExportFormat{}, err
	}
	return buf.Bytes(), format, nil
}

// signatureInfos 获取签名验证信息
// 返回: []SignatureInfo 签名验证信息, error 错误信息
func (s *Session) signatureInfos() ([]SignatureInfo, error) {
	reports, err := s.Reader.VerifySignatures()
	if err != nil {
		return nil, err
	}
	infos := make([]SignatureInfo, 0, len(reports))
	for _, report := range reports {
		positions, err := s.Reader.SignatureStampPositions(report.Stamps)
		if err != nil {
			return nil, err
		}
		infos = append(infos, signatureInfo(report, positions))
	}
	return infos, nil
}

// signatureInfo 转换签名验证信息
// 入参: report 签名验证报告, positions 签名外观位置
// 返回: SignatureInfo 签名验证信息
func signatureInfo(report ofdgo.SignatureVerifyReport, positions []ofdgo.SignatureStampPosition) SignatureInfo {
	return SignatureInfo{
		ID:                report.ID,
		Type:              string(report.Type),
		Valid:             report.Valid,
		Version:           report.Provider.Version,
		SealType:          report.SealType,
		Signer:            signatureSigner(report),
		SignatureDateTime: report.SignatureDateTime,
		Provider:          report.Provider.ProviderName,
		Company:           report.Provider.Company,
		DataHashOK:        report.DataHashOK,
		SignedValueOK:     report.SignedValueOK,
		SealOK:            report.SealOK,
		SealMatchOK:       report.SealMatchOK,
		CertOK:            report.CertOK,
		ReferenceCount:    len(report.References),
		ReferencePassed:   signatureReferencePassed(report.References),
		SignSerial:        report.SignCert.SerialNumber,
		SignatureMethod:   report.SignatureMethod,
		DigestMethod:      report.DigestMethod,
		SignSubject:       report.SignCert.Subject,
		SignIssuer:        report.SignCert.Issuer,
		SealSubject:       report.SealCert.Subject,
		Stamps:            signatureStampInfos(positions),
		Error:             report.Error,
	}
}

// signatureSigner 获取签名人名称
// 入参: report 签名验证报告
// 返回: string 签名人名称
func signatureSigner(report ofdgo.SignatureVerifyReport) string {
	if report.Signer != "" {
		return report.Signer
	}
	if report.SignCert.CommonName != "" {
		return report.SignCert.CommonName
	}
	if report.SignCert.Organization != "" {
		return report.SignCert.Organization
	}
	return report.SignCert.Subject
}

// signatureReferencePassed 获取已通过保护文件数量
// 入参: refs 保护文件验证结果
// 返回: int 已通过保护文件数量
func signatureReferencePassed(refs []ofdgo.SignatureReferenceVerify) int {
	count := 0
	for _, ref := range refs {
		if ref.OK {
			count++
		}
	}
	return count
}

// signatureStampInfos 转换签名外观位置
// 入参: positions 签名外观位置
// 返回: []SignatureStampInfo 签名外观信息
func signatureStampInfos(positions []ofdgo.SignatureStampPosition) []SignatureStampInfo {
	infos := make([]SignatureStampInfo, 0, len(positions))
	for _, position := range positions {
		infos = append(infos, SignatureStampInfo{
			ID:       position.ID,
			Page:     position.Page,
			PageID:   position.PageID,
			Boundary: position.Boundary,
			X:        position.Box.X,
			Y:        position.Box.Y,
			Width:    position.Box.W,
			Height:   position.Box.H,
		})
	}
	return infos
}

// ExportPDF 导出文档为PDF
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

// imageWithWhiteBackground 填充图片白色背景
// 入参: img 图片对象
// 返回: image.Image 图片对象
func imageWithWhiteBackground(img image.Image) image.Image {
	bounds := img.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, bounds, img, bounds.Min, draw.Over)
	return dst
}

// pageContent 获取页面内容
// 入参: index 页面索引
// 返回: ofdgo.Page 页面引用, *ofdgo.PageContent 页面内容, error 错误信息
func (s *Session) pageContent(index int) (ofdgo.Page, *ofdgo.PageContent, error) {
	if s == nil || s.Reader == nil || s.Renderer == nil || s.doc == nil {
		return ofdgo.Page{}, nil, fmt.Errorf("ofd document is not opened")
	}
	if index < 0 || index >= len(s.doc.Pages.Page) {
		return ofdgo.Page{}, nil, fmt.Errorf("page index %d out of range", index)
	}
	pageRef := s.doc.Pages.Page[index]
	if page, ok := s.pageCache[index]; ok {
		return pageRef, page, nil
	}
	page, err := s.Reader.PageContent(pageRef)
	if err != nil {
		return ofdgo.Page{}, nil, err
	}
	s.pageCache[index] = page
	return pageRef, page, nil
}

// pageBox 获取页面物理区域
// 入参: index 页面索引, page 页面内容
// 返回: ofdgo.Box 页面物理区域, error 错误信息
func (s *Session) pageBox(index int, page *ofdgo.PageContent) (ofdgo.Box, error) {
	if box, ok := s.boxCache[index]; ok {
		return box, nil
	}
	box, err := s.Renderer.GetPageBox(page)
	if err != nil {
		return ofdgo.Box{}, err
	}
	s.boxCache[index] = box
	return box, nil
}
