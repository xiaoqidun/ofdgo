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
	"reflect"
	"strings"
	"time"

	"github.com/xiaoqidun/ofdgo"
)

// OpenOptions 打开OFD文档选项
type OpenOptions struct {
	Fonts             []FontFile
	RenderAnnotations bool
	ReaderOptions     []ofdgo.ReaderOption
}

// FontFile 字体文件
type FontFile = ofdgo.FontFile

// FontInfo OFD字体信息
type FontInfo = ofdgo.FontInfo

// Session WebUI文档会话
type Session struct {
	Reader          *ofdgo.Reader
	Renderer        *ofdgo.Renderer
	fontFS          *ofdgo.FontFS
	doc             *ofdgo.Document
	pageCache       map[int]*ofdgo.PageContent
	boxCache        []pageBoxInfo
	textCache       map[int]*ofdgo.PageText
	svgFonts        map[string][]byte
	fontScan        *ofdgo.FontInfoScanner
	fontInfos       []FontInfo
	fontsRead       bool
	fontAnnotations bool
	signatures      []SignatureInfo
	signatureError  error
	signaturesRead  bool
	editing         bool
}

// pageBoxInfo 按页索引缓存真实区域，valid区分未读取与零值区域
type pageBoxInfo struct {
	box   ofdgo.Box
	valid bool
}

// DocumentInfo 文档信息
type DocumentInfo struct {
	Encryption      EncryptionInfo     `json:"encryption"`
	Version         string             `json:"version"`
	DocType         string             `json:"docType"`
	Title           string             `json:"title"`
	Author          string             `json:"author"`
	Subject         string             `json:"subject"`
	CustomData      []ofdgo.CustomData `json:"customData,omitempty"`
	CreationDate    string             `json:"creationDate"`
	ModDate         string             `json:"modDate"`
	PageCount       int                `json:"pageCount"`
	FontCount       int                `json:"fontCount"`
	SignatureCount  int                `json:"signatureCount"`
	SignatureError  string             `json:"signatureError,omitempty"`
	AttachmentError string             `json:"attachmentError,omitempty"`
	Attachments     []AttachmentInfo   `json:"attachments,omitempty"`
	Fonts           []FontInfo         `json:"fonts"`
	Signatures      []SignatureInfo    `json:"signatures"`
	Pages           []PageInfo         `json:"pages"`
	Outlines        []OutlineInfo      `json:"outlines,omitempty"`
	DetailsPending  bool               `json:"detailsPending,omitempty"`
}

// DocumentDetails 不含页面数组的文档补充信息
type DocumentDetails struct {
	FontCount       int              `json:"fontCount"`
	SignatureCount  int              `json:"signatureCount"`
	SignatureError  string           `json:"signatureError,omitempty"`
	AttachmentError string           `json:"attachmentError,omitempty"`
	Attachments     []AttachmentInfo `json:"attachments,omitempty"`
	Fonts           []FontInfo       `json:"fonts"`
	Signatures      []SignatureInfo  `json:"signatures"`
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
	Index       int                    `json:"index"`
	Number      int                    `json:"number"`
	ID          string                 `json:"id"`
	Width       float64                `json:"width"`
	Height      float64                `json:"height"`
	SVG         string                 `json:"svg"`
	Links       []ofdgo.PageLink       `json:"-"`
	Fonts       []ofdgo.SVGFont        `json:"-"`
	Images      []ofdgo.SVGImage       `json:"-"`
	Annotations []ofdgo.AnnotationInfo `json:"annotations,omitempty"`
}

// ExportFormat 导出格式
type ExportFormat struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	Extension string `json:"extension"`
	MIME      string `json:"mime"`
}

// SignatureInfo 签名验证信息
type SignatureInfo struct {
	DocIndex             int                  `json:"docIndex"`
	DocRoot              string               `json:"docRoot,omitempty"`
	ID                   string               `json:"id"`
	Type                 string               `json:"type"`
	Status               string               `json:"status"`
	IntegrityValid       bool                 `json:"integrityValid"`
	TrustedValid         bool                 `json:"trustedValid"`
	Error                string               `json:"error,omitempty"`
	Signer               string               `json:"signer,omitempty"`
	SignatureDateTime    string               `json:"signatureDateTime,omitempty"`
	SealName             string               `json:"sealName,omitempty"`
	Provider             string               `json:"provider,omitempty"`
	Company              string               `json:"company,omitempty"`
	SealType             string               `json:"sealType,omitempty"`
	SealID               string               `json:"sealId,omitempty"`
	SealVendor           string               `json:"sealVendor,omitempty"`
	Version              string               `json:"version,omitempty"`
	SignSubject          string               `json:"signSubject,omitempty"`
	SignIssuer           string               `json:"signIssuer,omitempty"`
	SignSerial           string               `json:"signSerial,omitempty"`
	SealSubject          string               `json:"sealSubject,omitempty"`
	SignatureMethod      string               `json:"signatureMethod,omitempty"`
	DigestMethod         string               `json:"digestMethod,omitempty"`
	ReferenceCount       int                  `json:"referenceCount"`
	ReferenceChecked     int                  `json:"referenceChecked"`
	ReferencePassed      int                  `json:"referencePassed"`
	Stamps               []SignatureStampInfo `json:"stamps,omitempty"`
	DataHashChecked      bool                 `json:"dataHashChecked"`
	DataHashOK           bool                 `json:"dataHashOK"`
	SignedValueChecked   bool                 `json:"signedValueChecked"`
	SignedValueOK        bool                 `json:"signedValueOK"`
	CertChecked          bool                 `json:"certChecked"`
	CertOK               bool                 `json:"certOK"`
	CertTimeChecked      bool                 `json:"certTimeChecked,omitempty"`
	CertTimeOK           bool                 `json:"certTimeOK,omitempty"`
	CertTrustChecked     bool                 `json:"certTrustChecked,omitempty"`
	CertTrustOK          bool                 `json:"certTrustOK,omitempty"`
	CertTrustError       string               `json:"certTrustError,omitempty"`
	SignatureTimeChecked bool                 `json:"signatureTimeChecked,omitempty"`
	SignatureTimeOK      bool                 `json:"signatureTimeOK,omitempty"`
	SealChecked          bool                 `json:"sealChecked"`
	SealOK               bool                 `json:"sealOK"`
	SealMatchChecked     bool                 `json:"sealMatchChecked"`
	SealMatchOK          bool                 `json:"sealMatchOK"`
	SealTimeChecked      bool                 `json:"sealTimeChecked,omitempty"`
	SealTimeOK           bool                 `json:"sealTimeOK,omitempty"`
	SealCertTimeChecked  bool                 `json:"sealCertTimeChecked,omitempty"`
	SealCertTimeOK       bool                 `json:"sealCertTimeOK,omitempty"`
	CoverageChecked      bool                 `json:"coverageChecked"`
	CoverageOK           bool                 `json:"coverageOK"`
	CoverageError        string               `json:"coverageError,omitempty"`
	UncoveredFiles       []string             `json:"uncoveredFiles,omitempty"`
	PolicyChecked        bool                 `json:"policyChecked"`
	PolicyOK             bool                 `json:"policyOK"`
	PolicyError          string               `json:"policyError,omitempty"`
	TimestampChecked     bool                 `json:"timestampChecked"`
	TimestampOK          bool                 `json:"timestampOK"`
	Timestamps           []TimestampInfo      `json:"timestamps,omitempty"`
	RevocationChecked    bool                 `json:"revocationChecked"`
	RevocationOK         bool                 `json:"revocationOK"`
	Revocations          []RevocationInfo     `json:"revocations,omitempty"`
}

// TimestampInfo 独立展示时间戳绑定、签名、信任和证书时效
type TimestampInfo struct {
	Time               string `json:"time,omitempty"`
	Valid              bool   `json:"valid"`
	BindingChecked     bool   `json:"bindingChecked"`
	BindingOK          bool   `json:"bindingOK"`
	SignedValueChecked bool   `json:"signedValueChecked"`
	SignedValueOK      bool   `json:"signedValueOK"`
	CertTrustChecked   bool   `json:"certTrustChecked"`
	CertTrustOK        bool   `json:"certTrustOK"`
	CertTimeChecked    bool   `json:"certTimeChecked"`
	CertTimeOK         bool   `json:"certTimeOK"`
	Error              string `json:"error,omitempty"`
}

// RevocationInfo 仅表示签者或制章证书的离线撤销状态
type RevocationInfo struct {
	Subject    string `json:"subject,omitempty"`
	Source     string `json:"source,omitempty"`
	Status     string `json:"status"`
	Checked    bool   `json:"checked"`
	OK         bool   `json:"ok"`
	ThisUpdate string `json:"thisUpdate,omitempty"`
	NextUpdate string `json:"nextUpdate,omitempty"`
	RevokedAt  string `json:"revokedAt,omitempty"`
	Error      string `json:"error,omitempty"`
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

// EncryptionInfo 仅向界面传递非敏感加密状态
type EncryptionInfo struct {
	Encrypted bool     `json:"encrypted"`
	Method    string   `json:"method,omitempty"`
	Users     []string `json:"users,omitempty"`
	Layers    int      `json:"layers,omitempty"`
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

// Open 打开浏览器内存中的OFD文档
// 入参: data OFD文件数据, opts 打开选项
// 返回: *Session 文档会话, error 错误信息
func Open(data []byte, opts OpenOptions) (*Session, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty ofd data")
	}
	reader, err := ofdgo.NewReader(bytes.NewReader(data), int64(len(data)), opts.ReaderOptions...)
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
		boxCache:  make([]pageBoxInfo, len(doc.Pages.Page)),
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
	s.resetFontInfo()
	return nil
}

// resetFontInfo 清除字体统计及扫描状态，供字体或注解配置变更后重算
func (s *Session) resetFontInfo() {
	s.fontScan = nil
	s.fontInfos = nil
	s.fontsRead = false
}

// scanFontInfo 分步统计字体，每次最多处理一页，完成后保留当前会话的诊断
// 返回: bool 是否完成
func (s *Session) scanFontInfo() bool {
	if s.fontAnnotations != s.Renderer.RenderAnnotations {
		s.resetFontInfo()
		s.fontAnnotations = s.Renderer.RenderAnnotations
	}
	if s.fontsRead {
		return true
	}
	if s.fontScan == nil {
		var err error
		s.fontScan, err = s.Renderer.ScanFontInfos()
		if err != nil {
			s.fontsRead = true
			return true
		}
	}
	if s.fontScan.Next() {
		return false
	}
	s.fontInfos, _ = s.fontScan.Infos()
	s.fontsRead = true
	s.fontScan = nil
	return true
}

// scanFontInfoBatch 为大文档批量统计字体，限制每次处理的页数与耗时
// 返回: bool 是否完成
func (s *Session) scanFontInfoBatch() bool {
	limit := 1
	if len(s.doc.Pages.Page) > 256 {
		limit = 64
	}
	start := time.Now()
	for range limit {
		if s.scanFontInfo() {
			return true
		}
		if time.Since(start) >= 8*time.Millisecond {
			break
		}
	}
	return false
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
		Encryption:     s.encryptionInfo(),
		Version:        s.Reader.Version(),
		DocType:        s.Reader.DocType(),
		PageCount:      len(s.doc.Pages.Page),
		Pages:          s.pageInfos(),
		Outlines:       s.doc.OutlineInfos(),
		DetailsPending: true,
	}
	if docInfo, err := s.Reader.DocInfo(); err == nil && docInfo != nil {
		info.Title = docInfo.Title
		info.Author = docInfo.Author
		info.Subject = docInfo.Subject
		if docInfo.CustomDatas != nil {
			info.CustomData = append([]ofdgo.CustomData(nil), docInfo.CustomDatas.CustomData...)
		}
		info.CreationDate = docInfo.CreationDate
		info.ModDate = docInfo.ModDate
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

// pageInfos 获取全部真实页面尺寸，首屏、配置及详情共用区域缓存
// 返回: []PageInfo 独立页面信息列表
func (s *Session) pageInfos() []PageInfo {
	pages := make([]PageInfo, len(s.doc.Pages.Page))
	for index, page := range s.doc.Pages.Page {
		cached := s.boxCache[index]
		box := cached.box
		if !cached.valid {
			if area, err := s.Reader.PageArea(page); err == nil {
				box, _ = s.pageBox(index, &ofdgo.PageContent{Area: area})
			}
		}
		pages[index] = PageInfo{Index: index, ID: page.ID, Width: box.W, Height: box.H}
	}
	return pages
}

// Details 获取无需逐页扫描的附件、签名及字体匹配结果
// 返回: DocumentDetails 文档补充信息
func (s *Session) Details() DocumentDetails {
	info := DocumentDetails{}
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
	if fonts, err := s.Renderer.DeclaredFontInfos(); err == nil {
		info.Fonts = fonts
		info.FontCount = len(fonts)
	}
	return info
}

// Info 获取完整文档信息及逐页字体用量
// 返回: DocumentInfo 文档信息
func (s *Session) Info() DocumentInfo {
	info := s.Summary()
	details := s.Details()
	info.Attachments = details.Attachments
	info.AttachmentError = details.AttachmentError
	info.Signatures = details.Signatures
	info.SignatureCount = details.SignatureCount
	info.SignatureError = details.SignatureError
	for !s.scanFontInfo() {
	}
	info.Fonts = s.fontInfos
	info.FontCount = len(info.Fonts)
	info.DetailsPending = false
	return info
}

// RenderPageSVG 渲染页面为SVG
// 入参: index 页面索引
// 返回: PageSVG 页面SVG结果, error 错误信息
func (s *Session) RenderPageSVG(index int) (PageSVG, error) {
	return s.RenderPage(index, "", 0, false)
}

// RenderPage 按显示后端生成页面，光栅结果复用SVG容器和独立交互信息
// 入参: index 页面索引, backend 后端组合，空值沿用会话配置, dpi 光栅分辨率, raster 是否光栅显示
// 返回: PageSVG 页面结果, error 错误信息
func (s *Session) RenderPage(index int, backend string, dpi float64, raster bool) (PageSVG, error) {
	if backend != "" {
		if err := s.SetRenderBackend(backend); err != nil {
			return PageSVG{}, err
		}
	}
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
	if raster {
		renderer.DPI = dpi
	}
	resources, err := renderer.RenderPreview(page, &buf, ofdgo.PreviewOptions{Raster: raster, Objects: !raster && s.editing})
	if err != nil {
		return PageSVG{}, err
	}
	links, err := s.Renderer.PageLinks(page)
	if err != nil {
		return PageSVG{}, err
	}
	annotations, err := s.Reader.AnnotationInfosByIndex(index)
	if err != nil {
		return PageSVG{}, err
	}
	for i, font := range resources.Fonts {
		s.svgFonts[font.Name] = font.Data
		resources.Fonts[i].Data = nil
	}
	return PageSVG{Index: index, Number: index + 1, ID: page.ID, Width: box.W, Height: box.H, SVG: buf.String(), Links: links, Fonts: resources.Fonts, Images: resources.Images, Annotations: annotations}, nil
}

// SetRenderBackend 设置库提供的后端组合，不改变文档数据
// 入参: name 后端名称
// 返回: error 错误信息
func (s *Session) SetRenderBackend(name string) error {
	backends, err := ofdgo.NewRenderBackends(name)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(s.Renderer.Backends(), backends) {
		return nil
	}
	previous, next := s.Renderer.Backends().Info(), backends.Info()
	if previous.Compiler != next.Compiler || previous.Fonts != next.Fonts || previous.Geometry != next.Geometry {
		clear(s.textCache)
	}
	ofdgo.WithRenderBackends(backends)(s.Renderer)
	return nil
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
	if cached := s.boxCache[index]; cached.valid {
		return cached.box, nil
	}
	box, err := s.Renderer.GetPageBox(page)
	if err != nil {
		return ofdgo.Box{}, err
	}
	s.boxCache[index] = pageBoxInfo{box: box, valid: true}
	return box, nil
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

// encryptionInfo 获取当前文档的加密来源，不包含凭据
// 返回: EncryptionInfo 加密状态
func (s *Session) encryptionInfo() EncryptionInfo {
	info := s.Reader.Encryption()
	return EncryptionInfo{Encrypted: info.Encrypted, Method: info.Method, Users: info.Users, Layers: info.Layers}
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
	info := SignatureInfo{
		DocIndex:             report.DocIndex,
		DocRoot:              report.DocRoot,
		ID:                   report.ID,
		Type:                 string(report.Type),
		Status:               status,
		IntegrityValid:       report.IntegrityValid(),
		TrustedValid:         report.TrustedValid(),
		Error:                signatureReportError(report),
		Signer:               signatureSigner(report),
		SignatureDateTime:    dateTime,
		SealName:             report.SealInfo.Name,
		Provider:             report.Provider.ProviderName,
		Company:              report.Provider.Company,
		SealType:             report.SealType,
		SealID:               report.SealInfo.ID,
		SealVendor:           report.SealInfo.VendorID,
		Version:              report.Provider.Version,
		SignSubject:          report.SignCert.Subject,
		SignIssuer:           report.SignCert.Issuer,
		SignSerial:           report.SignCert.SerialNumber,
		SealSubject:          report.SealCert.Subject,
		SignatureMethod:      report.SignatureMethod,
		DigestMethod:         report.DigestMethod,
		ReferenceCount:       len(report.References),
		ReferenceChecked:     checked,
		ReferencePassed:      passed,
		Stamps:               signatureStampInfos(report.StampPositions),
		DataHashChecked:      report.DataHashChecked,
		DataHashOK:           report.DataHashOK,
		SignedValueChecked:   report.SignedValueChecked,
		SignedValueOK:        report.SignedValueOK,
		CertChecked:          report.CertChecked,
		CertOK:               report.CertOK,
		CertTimeChecked:      report.CertTimeChecked,
		CertTimeOK:           report.CertTimeOK,
		CertTrustChecked:     report.CertTrustChecked,
		CertTrustOK:          report.CertTrustOK,
		CertTrustError:       report.CertTrustError,
		SignatureTimeChecked: report.SignatureTimeChecked,
		SignatureTimeOK:      report.SignatureTimeOK,
		SealChecked:          report.SealChecked,
		SealOK:               report.SealOK,
		SealMatchChecked:     report.SealMatchChecked,
		SealMatchOK:          report.SealMatchOK,
		SealTimeChecked:      report.SealTimeChecked,
		SealTimeOK:           report.SealTimeOK,
		SealCertTimeChecked:  report.SealCertTimeChecked,
		SealCertTimeOK:       report.SealCertTimeOK,
		CoverageChecked:      report.CoverageChecked,
		CoverageOK:           report.CoverageOK,
		CoverageError:        report.CoverageError,
		UncoveredFiles:       report.UncoveredFiles,
		PolicyChecked:        report.PolicyChecked,
		PolicyOK:             report.PolicyOK,
		PolicyError:          report.PolicyError,
		TimestampChecked:     report.TimestampChecked,
		TimestampOK:          report.TimestampOK,
		RevocationChecked:    report.RevocationChecked,
		RevocationOK:         report.RevocationOK,
	}
	for _, stamp := range report.Timestamps {
		info.Timestamps = append(info.Timestamps, TimestampInfo{
			Time: securityTime(stamp.Time), Valid: stamp.Valid,
			BindingChecked: stamp.BindingChecked, BindingOK: stamp.BindingOK,
			SignedValueChecked: stamp.SignedValueChecked, SignedValueOK: stamp.SignedValueOK,
			CertTrustChecked: stamp.CertTrustChecked, CertTrustOK: stamp.CertTrustOK,
			CertTimeChecked: stamp.CertTimeChecked, CertTimeOK: stamp.CertTimeOK, Error: stamp.Error,
		})
	}
	for _, revocation := range report.Revocations {
		info.Revocations = append(info.Revocations, RevocationInfo{
			Subject: revocation.Certificate.Subject, Source: revocation.Source, Status: revocation.Status,
			Checked: revocation.Checked, OK: revocation.OK, ThisUpdate: securityTime(revocation.ThisUpdate),
			NextUpdate: securityTime(revocation.NextUpdate), RevokedAt: securityTime(revocation.RevokedAt), Error: revocation.Error,
		})
	}
	return info
}

// securityTime 格式化已提供的安全证据时间，零值不展示
// 入参: value 时间
// 返回: string 标准时间文本
func securityTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
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
