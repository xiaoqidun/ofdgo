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
	"io"
	"path"
	"strings"
	"time"

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
	ID                   string               `json:"id"`
	Type                 string               `json:"type"`
	Status               string               `json:"status"`
	Version              string               `json:"version,omitempty"`
	SealType             string               `json:"sealType,omitempty"`
	SealID               string               `json:"sealId,omitempty"`
	SealName             string               `json:"sealName,omitempty"`
	SealVendor           string               `json:"sealVendor,omitempty"`
	Signer               string               `json:"signer,omitempty"`
	SignatureDateTime    string               `json:"signatureDateTime,omitempty"`
	Provider             string               `json:"provider,omitempty"`
	Company              string               `json:"company,omitempty"`
	DataHashChecked      bool                 `json:"dataHashChecked"`
	DataHashOK           bool                 `json:"dataHashOK"`
	SignedValueChecked   bool                 `json:"signedValueChecked"`
	SignedValueOK        bool                 `json:"signedValueOK"`
	SealChecked          bool                 `json:"sealChecked"`
	SealOK               bool                 `json:"sealOK"`
	SealMatchChecked     bool                 `json:"sealMatchChecked"`
	SealMatchOK          bool                 `json:"sealMatchOK"`
	CertChecked          bool                 `json:"certChecked"`
	CertOK               bool                 `json:"certOK"`
	SignatureTimeChecked bool                 `json:"signatureTimeChecked,omitempty"`
	SignatureTimeOK      bool                 `json:"signatureTimeOK,omitempty"`
	SealTimeChecked      bool                 `json:"sealTimeChecked,omitempty"`
	SealTimeOK           bool                 `json:"sealTimeOK,omitempty"`
	SealCertTimeChecked  bool                 `json:"sealCertTimeChecked,omitempty"`
	SealCertTimeOK       bool                 `json:"sealCertTimeOK,omitempty"`
	CertTimeChecked      bool                 `json:"certTimeChecked,omitempty"`
	CertTimeOK           bool                 `json:"certTimeOK,omitempty"`
	CertTrustChecked     bool                 `json:"certTrustChecked,omitempty"`
	CertTrustOK          bool                 `json:"certTrustOK,omitempty"`
	ReferenceCount       int                  `json:"referenceCount"`
	ReferenceChecked     int                  `json:"referenceChecked"`
	ReferencePassed      int                  `json:"referencePassed"`
	SignatureMethod      string               `json:"signatureMethod,omitempty"`
	DigestMethod         string               `json:"digestMethod,omitempty"`
	SignSerial           string               `json:"signSerial,omitempty"`
	SignSubject          string               `json:"signSubject,omitempty"`
	SignIssuer           string               `json:"signIssuer,omitempty"`
	SealSubject          string               `json:"sealSubject,omitempty"`
	Stamps               []SignatureStampInfo `json:"stamps,omitempty"`
	Error                string               `json:"error,omitempty"`
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
	Reader         *ofdgo.Reader
	Renderer       *ofdgo.Renderer
	fontFS         *ofdgo.FontFS
	doc            *ofdgo.Document
	pageCache      map[int]*ofdgo.PageContent
	boxCache       map[int]ofdgo.Box
	textCache      map[int]*ofdgo.PageText
	svgFonts       map[string][]byte
	signatures     []SignatureInfo
	signatureError error
	signaturesRead bool
	editing        bool
}

// DocumentInfo 文档信息
type DocumentInfo struct {
	Version         string                 `json:"version"`
	DocType         string                 `json:"docType"`
	Title           string                 `json:"title"`
	Author          string                 `json:"author"`
	Subject         string                 `json:"subject"`
	CreationDate    string                 `json:"creationDate"`
	ModDate         string                 `json:"modDate"`
	PageCount       int                    `json:"pageCount"`
	FontCount       int                    `json:"fontCount"`
	SignatureCount  int                    `json:"signatureCount"`
	SignatureError  string                 `json:"signatureError,omitempty"`
	AttachmentError string                 `json:"attachmentError,omitempty"`
	Attachments     []AttachmentInfo       `json:"attachments,omitempty"`
	Fonts           []FontInfo             `json:"fonts"`
	Signatures      []SignatureInfo        `json:"signatures"`
	Annotations     []ofdgo.AnnotationInfo `json:"annotations,omitempty"`
	Pages           []PageInfo             `json:"pages"`
	Outlines        []OutlineInfo          `json:"outlines,omitempty"`
	DetailsPending  bool                   `json:"detailsPending,omitempty"`
}

// OutlineInfo 目录节点信息
type OutlineInfo = ofdgo.OutlineInfo

// AttachmentInfo 可见附件信息，Size单位为KB
type AttachmentInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Format   string   `json:"format,omitempty"`
	Size     *float64 `json:"size,omitempty"`
	FileName string   `json:"fileName"`
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
	Index  int              `json:"index"`
	Number int              `json:"number"`
	ID     string           `json:"id"`
	Width  float64          `json:"width"`
	Height float64          `json:"height"`
	SVG    string           `json:"svg"`
	Links  []ofdgo.PageLink `json:"-"`
	Fonts  []ofdgo.SVGFont  `json:"-"`
	Images []ofdgo.SVGImage `json:"-"`
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
	{Value: "pdf", Label: "PDF", Extension: "pdf", MIME: "application/pdf"},
	{Value: "svg", Label: "SVG", Extension: "svg", MIME: "image/svg+xml"},
	{Value: "eps", Label: "EPS", Extension: "eps", MIME: "application/postscript"},
	{Value: "png", Label: "PNG", Extension: "png", MIME: "image/png"},
	{Value: "jpg", Label: "JPG", Extension: "jpg", MIME: "image/jpeg"},
	{Value: "txt", Label: "TXT", Extension: "txt", MIME: "text/plain"},
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
	return newSession(reader, opts)
}

// newSession 从文档快照创建阅读会话
// 入参: reader 文档读取器, opts 打开选项
// 返回: *Session 文档会话, error 错误信息
func newSession(reader *ofdgo.Reader, opts OpenOptions) (*Session, error) {
	doc, err := reader.Doc()
	if err != nil {
		reader.Close()
		return nil, err
	}
	var rendererOptions []ofdgo.RendererOption
	var fontFS *ofdgo.FontFS
	if len(opts.Fonts) > 0 {
		fontFS = ofdgo.NewFontFS(opts.Fonts)
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
		fontFS:    fontFS,
		doc:       doc,
		pageCache: make(map[int]*ofdgo.PageContent),
		boxCache:  make(map[int]ofdgo.Box),
		textCache: make(map[int]*ofdgo.PageText),
		svgFonts:  make(map[string][]byte),
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

// SetFonts 更新字体配置并保留文档、页面和验签结果
// 入参: fonts 字体文件列表
// 返回: error 错误信息
func (s *Session) SetFonts(fonts []FontFile) error {
	if len(fonts) > 0 {
		fontFS := ofdgo.NewFontFS(fonts)
		if fontFS.Len() == 0 {
			return fmt.Errorf("invalid font file")
		}
		s.Renderer.SetFontFS(fontFS)
		s.fontFS = fontFS
	} else {
		s.Renderer.SetFontFS()
		s.fontFS = nil
	}
	clear(s.textCache)
	clear(s.svgFonts)
	return nil
}

// PageText 获取指定页面文字，复用当前会话的文字索引
// 入参: index 页面索引
// 返回: *ofdgo.PageText 页面文字, error 错误信息
func (s *Session) PageText(index int) (*ofdgo.PageText, error) {
	text := s.textCache[index]
	if text == nil {
		var err error
		text, err = s.readPageText(index)
		if err != nil {
			return nil, err
		}
		s.textCache[index] = text
	}
	return text, nil
}

// PageTextString 获取页面原文，不为全文复制保留额外的页面和文字索引
// 入参: index 页面索引
// 返回: string 页面原文, error 错误信息
func (s *Session) PageTextString(index int) (string, error) {
	text := s.textCache[index]
	if text == nil {
		var err error
		text, err = s.readPageText(index)
		if err != nil {
			return "", err
		}
	}
	return text.String(), nil
}

// readPageText 复用已加载页面，否则只解析文字所需图元
// 入参: index 页面索引
// 返回: *ofdgo.PageText 页面文字, error 错误信息
func (s *Session) readPageText(index int) (*ofdgo.PageText, error) {
	if page := s.pageCache[index]; page != nil {
		return s.Renderer.PageText(page)
	}
	return s.Renderer.PageTextByIndex(index)
}

// SearchPage 搜索指定页面，复用当前会话的文字索引
// 入参: index 页面索引, query 搜索文字
// 返回: []ofdgo.TextMatch 匹配结果, error 错误信息
func (s *Session) SearchPage(index int, query string) ([]ofdgo.TextMatch, error) {
	text, err := s.PageText(index)
	if err != nil {
		return nil, err
	}
	return text.Search(query), nil
}

// Summary 获取首屏所需信息，不解析页面图元或执行验签
// 返回: DocumentInfo 文档信息
func (s *Session) Summary() DocumentInfo {
	info := DocumentInfo{
		Version:        s.Reader.Version(),
		DocType:        s.Reader.DocType(),
		PageCount:      len(s.doc.Pages.Page),
		Pages:          make([]PageInfo, 0, len(s.doc.Pages.Page)),
		Outlines:       s.doc.OutlineInfos(),
		DetailsPending: true,
	}
	if docInfo, err := s.Reader.DocInfo(); err == nil && docInfo != nil {
		info.Title = docInfo.Title
		info.Author = docInfo.Author
		info.Subject = docInfo.Subject
		info.CreationDate = docInfo.CreationDate
		info.ModDate = docInfo.ModDate
	}
	for index, page := range s.doc.Pages.Page {
		box, ok := s.boxCache[index]
		if !ok {
			if area, err := s.Reader.PageArea(page); err == nil {
				box, _ = s.pageBox(index, &ofdgo.PageContent{Area: area})
			}
		}
		info.Pages = append(info.Pages, PageInfo{Index: index, ID: page.ID, Width: box.W, Height: box.H})
	}
	if fonts, err := s.Reader.Fonts(); err == nil {
		for _, font := range fonts {
			info.Fonts = append(info.Fonts, FontInfo{
				ID:         font.ID,
				FontName:   font.FontName,
				FamilyName: font.FamilyName,
				Charset:    font.Charset,
				FontFile:   font.FontFile,
				Embedded:   font.FontFile != "",
				Status:     "pending",
			})
		}
	}
	info.FontCount = len(info.Fonts)
	return info
}

// Info 获取完整文档信息
// 返回: DocumentInfo 文档信息
func (s *Session) Info() DocumentInfo {
	info := DocumentInfo{
		Version:   s.Reader.Version(),
		DocType:   s.Reader.DocType(),
		PageCount: len(s.doc.Pages.Page),
		Pages:     make([]PageInfo, 0, len(s.doc.Pages.Page)),
		Outlines:  s.doc.OutlineInfos(),
	}
	info.Annotations, _ = s.Reader.AnnotationInfos()
	if attachments, err := s.Reader.Attachments(); err == nil {
		for _, attachment := range attachments {
			if attachment.Visible {
				info.Attachments = append(info.Attachments, AttachmentInfo{
					ID:       attachment.ID,
					Name:     attachment.Name,
					Format:   attachment.Format,
					Size:     attachment.Size,
					FileName: path.Base(s.Reader.ResPath(attachment.FileLoc)),
				})
			}
		}
	} else {
		info.AttachmentError = err.Error()
	}
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
		if area, err := s.Reader.PageArea(pageRef); err == nil {
			if box, err := s.pageBox(index, &ofdgo.PageContent{Area: area}); err == nil {
				pageInfo.Width = box.W
				pageInfo.Height = box.H
			}
		}
		info.Pages = append(info.Pages, pageInfo)
	}
	if fonts, err := s.Renderer.FontInfos(); err == nil {
		info.Fonts = fonts
	}
	info.FontCount = len(info.Fonts)
	return info
}

// RenderPageSVG 渲染页面为SVG
// 入参: index 页面索引
// 返回: PageSVG 页面SVG结果, error 错误信息
func (s *Session) RenderPageSVG(index int) (PageSVG, error) {
	page, err := s.pageContent(index)
	if err != nil {
		return PageSVG{}, err
	}
	box, err := s.pageBox(index, page)
	if err != nil {
		return PageSVG{}, err
	}
	var buf bytes.Buffer
	renderer := *s.Renderer
	if s.textCache[index] == nil {
		renderer.OnPageText = func(_ *ofdgo.PageContent, text *ofdgo.PageText) {
			s.textCache[index] = text
		}
	}
	renderSVG := renderer.RenderToSVGWithResources
	if s.editing {
		renderSVG = renderer.RenderToSVGWithObjects
	}
	resources, err := renderSVG(page, &buf)
	if err != nil {
		return PageSVG{}, err
	}
	links, err := s.Renderer.PageLinks(page)
	if err != nil {
		return PageSVG{}, err
	}
	for i, font := range resources.Fonts {
		s.svgFonts[font.Name] = font.Data
		resources.Fonts[i].Data = nil
	}
	return PageSVG{Index: index, Number: index + 1, ID: page.ID, Width: box.W, Height: box.H, SVG: buf.String(), Links: links, Fonts: resources.Fonts, Images: resources.Images}, nil
}

// SVGFontData 获取已渲染页面引用的字体数据
// 入参: name 字体资源标识
// 返回: []byte 字体数据, error 错误信息
func (s *Session) SVGFontData(name string) ([]byte, error) {
	data, ok := s.svgFonts[name]
	if !ok {
		return nil, fmt.Errorf("svg font not found: %s", name)
	}
	return data, nil
}

// ExportPage 导出单页
// 入参: index 页面索引, value 导出格式, dpi 图片DPI, writer 输出流
// 返回: ExportFormat 导出格式, error 错误信息
func (s *Session) ExportPage(index int, value string, dpi float64, writer io.Writer) (ExportFormat, error) {
	format, ok := exportFormat(value)
	if !ok {
		return ExportFormat{}, fmt.Errorf("unsupported export format %s", value)
	}
	page, err := s.pageContent(index)
	if err != nil {
		return ExportFormat{}, err
	}
	renderer := *s.Renderer
	if dpi > 0 && (format.Value == "png" || format.Value == "jpg") {
		renderer.DPI = dpi
	}
	return format, renderer.RenderTo(page, writer, format.Value)
}

// signatureInfos 获取签名验证信息
// 返回: []SignatureInfo 签名验证信息, error 错误信息
func (s *Session) signatureInfos() ([]SignatureInfo, error) {
	if s.signaturesRead {
		return s.signatures, s.signatureError
	}
	s.signaturesRead = true
	reports, err := s.Reader.VerifySignatures()
	if err != nil {
		s.signatureError = err
		return nil, err
	}
	infos := make([]SignatureInfo, 0, len(reports))
	for _, report := range reports {
		infos = append(infos, signatureInfo(report))
	}
	s.signatures = infos
	return infos, nil
}

// signatureInfo 转换签名验证信息
// 入参: report 签名验证报告
// 返回: SignatureInfo 签名验证信息
func signatureInfo(report ofdgo.SignatureVerifyReport) SignatureInfo {
	status := "unchecked"
	if report.Valid {
		status = "valid"
	} else if report.HasFailure() {
		status = "invalid"
	}
	dateTime := report.SignatureDateTime
	if !report.SignatureTime.IsZero() {
		dateTime = report.SignatureTime.Format(time.RFC3339Nano)
	}
	checked, passed := signatureReferenceCounts(report.References)
	return SignatureInfo{
		ID:                   report.ID,
		Type:                 string(report.Type),
		Status:               status,
		Version:              report.Provider.Version,
		SealType:             report.SealType,
		SealID:               report.SealInfo.ID,
		SealName:             report.SealInfo.Name,
		SealVendor:           report.SealInfo.VendorID,
		Signer:               signatureSigner(report),
		SignatureDateTime:    dateTime,
		Provider:             report.Provider.ProviderName,
		Company:              report.Provider.Company,
		DataHashChecked:      report.DataHashChecked,
		DataHashOK:           report.DataHashOK,
		SignedValueChecked:   report.SignedValueChecked,
		SignedValueOK:        report.SignedValueOK,
		SealChecked:          report.SealChecked,
		SealOK:               report.SealOK,
		SealMatchChecked:     report.SealMatchChecked,
		SealMatchOK:          report.SealMatchOK,
		CertChecked:          report.CertChecked,
		CertOK:               report.CertOK,
		SignatureTimeChecked: report.SignatureTimeChecked,
		SignatureTimeOK:      report.SignatureTimeOK,
		SealTimeChecked:      report.SealTimeChecked,
		SealTimeOK:           report.SealTimeOK,
		SealCertTimeChecked:  report.SealCertTimeChecked,
		SealCertTimeOK:       report.SealCertTimeOK,
		CertTimeChecked:      report.CertTimeChecked,
		CertTimeOK:           report.CertTimeOK,
		CertTrustChecked:     report.CertTrustChecked,
		CertTrustOK:          report.CertTrustOK,
		ReferenceCount:       len(report.References),
		ReferenceChecked:     checked,
		ReferencePassed:      passed,
		SignatureMethod:      report.SignatureMethod,
		DigestMethod:         report.DigestMethod,
		SignSerial:           report.SignCert.SerialNumber,
		SignSubject:          report.SignCert.Subject,
		SignIssuer:           report.SignCert.Issuer,
		SealSubject:          report.SealCert.Subject,
		Stamps:               signatureStampInfos(report.StampPositions),
		Error:                signatureReportError(report),
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

// signatureReferenceCounts 获取保护文件检查数量
// 入参: refs 保护文件验证结果
// 返回: int 已检查数量, int 已通过数量
func signatureReferenceCounts(refs []ofdgo.SignatureReferenceVerify) (int, int) {
	checked, passed := 0, 0
	for _, ref := range refs {
		if ref.Checked {
			checked++
		}
		if ref.OK {
			passed++
		}
	}
	return checked, passed
}

// signatureReportError 获取签名错误信息
// 入参: report 签名验证报告
// 返回: string 错误信息
func signatureReportError(report ofdgo.SignatureVerifyReport) string {
	if report.Error != "" && report.StampPositionError != "" {
		return report.Error + "; " + report.StampPositionError
	}
	if report.Error != "" {
		return report.Error
	}
	return report.StampPositionError
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

// ExportDocument 导出文档为PDF、TXT或逐页打包ZIP
// 入参: value 导出格式, dpi 图片DPI, writer 输出流, indices 零基页面索引，省略则全部
// 返回: ExportFormat 导出格式, error 错误信息
func (s *Session) ExportDocument(value string, dpi float64, writer io.Writer, indices ...int) (ExportFormat, error) {
	format, ok := exportFormat(value)
	if !ok {
		return ExportFormat{}, fmt.Errorf("unsupported export format %s", value)
	}
	if format.Value == "pdf" {
		return format, s.ExportPDF(writer, indices...)
	}
	if s == nil || s.Reader == nil || s.Renderer == nil || s.doc == nil {
		return ExportFormat{}, fmt.Errorf("ofd document is not opened")
	}
	if format.Value == "txt" {
		return format, s.Renderer.RenderToMultiPageText(writer, indices...)
	}
	renderer := *s.Renderer
	if dpi > 0 && (format.Value == "png" || format.Value == "jpg") {
		renderer.DPI = dpi
	}
	err := renderer.RenderToZIP(writer, format.Value, indices...)
	return ExportFormat{Value: "zip", Label: "ZIP", Extension: "zip", MIME: "application/zip"}, err
}

// ExportPDF 导出文档为PDF
// 入参: writer 输出流, indices 零基页面索引，省略则全部
// 返回: error 错误信息
func (s *Session) ExportPDF(writer io.Writer, indices ...int) error {
	if s == nil || s.Reader == nil || s.Renderer == nil || s.doc == nil {
		return fmt.Errorf("ofd document is not opened")
	}
	return s.Renderer.RenderToMultiPagePDF(writer, indices...)
}

// pageContent 读取页面，复用最近一次解析结果
// 入参: index 页面索引
// 返回: *ofdgo.PageContent 页面内容, error 错误信息
func (s *Session) pageContent(index int) (*ofdgo.PageContent, error) {
	if s == nil || s.Reader == nil || s.Renderer == nil || s.doc == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if page, ok := s.pageCache[index]; ok {
		return page, nil
	}
	clear(s.pageCache)
	page, err := s.Reader.PageContentByIndex(index)
	if err != nil {
		return nil, err
	}
	s.pageCache[index] = page
	return page, nil
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
