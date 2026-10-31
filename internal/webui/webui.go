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
	"bufio"
	"bytes"
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"io/fs"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xiaoqidun/ofdgo"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// sessionTextCacheBytes 限制会话文字索引的内存预算
const sessionTextCacheBytes = 32 << 20

// OpenOptions 打开OFD文档选项
type OpenOptions struct {
	Fonts             []FontFile
	RenderAnnotations bool
	ReaderOptions     []ofdgo.ReaderOption
}

// FontFile 字体文件
type FontFile = ofdgo.FontFile

// FontInfo 文档字体信息
type FontInfo = ofdgo.FontInfo

// Session WebUI文档会话
type Session struct {
	Reader          *ofdgo.Reader
	Renderer        *ofdgo.Renderer
	input           *ofdgo.Input
	fontFS          *ofdgo.FontFS
	doc             *ofdgo.Document
	pageCache       map[int]*ofdgo.PageContent
	boxCache        []pageBoxInfo
	textCache       *ofdgo.PageTextCache
	svgFonts        map[string][]byte
	fontScan        *ofdgo.FontInfoScanner
	fontInfos       []FontInfo
	fontsRead       bool
	fontAnnotations bool
	signatures      []SignatureInfo
	signatureError  error
	signaturesRead  bool
	editing         bool
	editor          *ofdgo.Editor
}

// pageBoxInfo 按页索引缓存真实区域，valid区分未读取与零值区域
type pageBoxInfo struct {
	box   ofdgo.Box
	valid bool
}

// DocumentMetadata 文档描述，供信息展示与包内文档管理共用
type DocumentMetadata struct {
	DocID          string             `json:"docId"`
	Title          string             `json:"title"`
	Author         string             `json:"author"`
	Subject        string             `json:"subject"`
	Abstract       string             `json:"abstract"`
	CreationDate   string             `json:"creationDate"`
	ModDate        string             `json:"modDate"`
	DocUsage       string             `json:"docUsage"`
	Cover          string             `json:"cover"`
	Keywords       []string           `json:"keywords,omitempty"`
	Creator        string             `json:"creator"`
	CreatorVersion string             `json:"creatorVersion"`
	CustomData     []ofdgo.CustomData `json:"customData,omitempty"`
}

// documentMetadata 复制文档描述，集合字段与读取器及编辑器互不影响
// 入参: info 文档描述
// 返回: DocumentMetadata 界面元数据
func documentMetadata(info ofdgo.DocInfo) DocumentMetadata {
	metadata := DocumentMetadata{
		DocID: info.DocID, Title: info.Title, Author: info.Author, Subject: info.Subject, Abstract: info.Abstract,
		CreationDate: info.CreationDate, ModDate: info.ModDate, DocUsage: info.DocUsage, Cover: info.Cover,
		Keywords: slices.Clone(info.Keywords), Creator: info.Creator, CreatorVersion: info.CreatorVersion,
	}
	if info.CustomDatas != nil {
		metadata.CustomData = slices.Clone(info.CustomDatas.CustomData)
	}
	return metadata
}

// DocumentInfo 文档信息
type DocumentInfo struct {
	DocumentMetadata
	DocumentIndex   int               `json:"documentIndex"`
	DocumentVersion string            `json:"documentVersion"`
	VersionCount    int               `json:"versionCount"`
	Documents       []DocumentEntry   `json:"documents,omitempty"`
	Encryption      EncryptionInfo    `json:"encryption"`
	Version         string            `json:"version"`
	DocType         string            `json:"docType"`
	PageCount       int               `json:"pageCount"`
	FontCount       int               `json:"fontCount"`
	SignatureCount  int               `json:"signatureCount"`
	SignatureError  string            `json:"signatureError,omitempty"`
	AttachmentError string            `json:"attachmentError,omitempty"`
	Attachments     []AttachmentInfo  `json:"attachments,omitempty"`
	Fonts           []FontInfo        `json:"fonts"`
	Signatures      []SignatureInfo   `json:"signatures"`
	Pages           []PageInfo        `json:"pages"`
	Outlines        []OutlineInfo     `json:"outlines,omitempty"`
	Structures      []ofdgo.Structure `json:"structures,omitempty"`
	StructureError  string            `json:"structureError,omitempty"`
	DetailsPending  bool              `json:"detailsPending,omitempty"`
}

// VersionEntry 版本管理列表中的入口，空ID表示主文档
type VersionEntry struct {
	ID      string `json:"id"`
	Index   int32  `json:"index"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// DocumentEntry 包内文档入口，管理列表按需补充元数据和页数
type DocumentEntry struct {
	DocumentMetadata
	Index     int    `json:"index"`
	Root      string `json:"root"`
	ID        string `json:"id"`
	PageCount *int   `json:"pageCount,omitempty"`
}

// DocumentDetails 不含页面数组的文档补充信息
type DocumentDetails struct {
	Structures      []ofdgo.Structure `json:"structures,omitempty"`
	StructureError  string            `json:"structureError,omitempty"`
	FontCount       int               `json:"fontCount"`
	SignatureCount  int               `json:"signatureCount"`
	SignatureError  string            `json:"signatureError,omitempty"`
	AttachmentError string            `json:"attachmentError,omitempty"`
	Attachments     []AttachmentInfo  `json:"attachments,omitempty"`
	Fonts           []FontInfo        `json:"fonts"`
	Signatures      []SignatureInfo   `json:"signatures"`
}

// OutlineInfo 目录节点信息
type OutlineInfo = ofdgo.OutlineInfo

// AttachmentInfo 附件信息，Size单位为KB
type AttachmentInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Format   string   `json:"format,omitempty"`
	Size     *float64 `json:"size,omitempty"`
	FileName string   `json:"fileName"`
	Visible  bool     `json:"visible"`
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
	Index       int                 `json:"index"`
	Number      int                 `json:"number"`
	ID          string              `json:"id"`
	Width       float64             `json:"width"`
	Height      float64             `json:"height"`
	SVG         string              `json:"svg"`
	Links       []ofdgo.PageLink    `json:"-"`
	Fonts       []ofdgo.SVGFont     `json:"-"`
	Images      []ofdgo.SVGImage    `json:"-"`
	Annotations []AnnotationPreview `json:"annotations,omitempty"`
}

// AnnotationPreview 注解预览信息及页面坐标中的交互范围
type AnnotationPreview struct {
	ofdgo.AnnotationInfo
	Bounds   ofdgo.Box             `json:"bounds"`
	Contours []ofdgo.ObjectContour `json:"contours,omitempty"`
}

// ExportFormat 导出格式
type ExportFormat = ofdgo.OutputFormat

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
	Encrypted      bool     `json:"encrypted"`
	Method         string   `json:"method,omitempty"`
	Users          []string `json:"users,omitempty"`
	Layers         int      `json:"layers,omitempty"`
	PolicyRequired bool     `json:"policyRequired,omitempty"`
}

// exportWriter 分块传递导出数据
type exportWriter struct {
	write platformValue
}

// browserReaderAt 分块读取浏览器文件，只缓存最近的一个数据块
type browserReaderAt struct {
	mu         sync.Mutex
	read       platformValue
	checkpoint platformValue
	size       int64
	offset     int64
	data       []byte
	checked    time.Time
}

// apiResult 浏览器接口返回结果
type apiResult struct {
	OK            bool                     `json:"ok"`
	Code          string                   `json:"code,omitempty"`
	Error         string                   `json:"error,omitempty"`
	Data          any                      `json:"data,omitempty"`
	MissingGlyphs *ofdgo.MissingGlyphError `json:"missingGlyphs,omitempty"`
	ReasonCode    ofdgo.EditReason         `json:"reasonCode,omitempty"`
}

// editorClipboard 保存选区标识及同文档快照或跨文档副本
type editorClipboard struct {
	token     string
	selection *ofdgo.ObjectSelection
	packet    *ofdgo.Editor
}

// editorInfo 编辑文档信息与操作状态
type editorInfo struct {
	DocumentInfo
	Revision         uint64              `json:"revision"`
	CanUndo          bool                `json:"canUndo"`
	CanRedo          bool                `json:"canRedo"`
	PageCapabilities []uint              `json:"pageCapabilities"`
	EditWarnings     []string            `json:"editWarnings,omitempty"`
	Changes          ofdgo.EditorChanges `json:"changes"`
}

// editorPageInfo 页面操作结果与目标页面
type editorPageInfo struct {
	editorInfo
	PageIndex int `json:"pageIndex"`
}

// editorSelectionInfo 对象复制结果及新选区
type editorSelectionInfo struct {
	editorInfo
	SelectedIDs []string `json:"selectedIDs"`
}

// editorOutlineInfo 目录操作后的位置
type editorOutlineInfo struct {
	editorInfo
	OutlinePath []int `json:"outlinePath"`
}

// invoiceRequest 发票操作参数
type invoiceRequest struct {
	Operation     string                     `json:"operation"`
	Operations    []invoiceRequest           `json:"operations"`
	Source        ofdgo.InvoiceSource        `json:"source"`
	Index         int                        `json:"index"`
	Digest        string                     `json:"digest"`
	Invoice       ofdgo.Invoice              `json:"invoice"`
	Changes       []ofdgo.InvoiceFieldChange `json:"changes"`
	Bindings      []ofdgo.InvoiceBinding     `json:"bindings"`
	XML           string                     `json:"xml"`
	Options       ofdgo.InvoiceCreateOptions `json:"options"`
	Layout        ofdgo.InvoiceLayout        `json:"layout"`
	FontData      []byte                     `json:"fontData"`
	Synchronize   bool                       `json:"synchronize"`
	DocumentIndex *int                       `json:"documentIndex"`
	Version       *string                    `json:"version"`
	Revision      *uint64                    `json:"revision"`
	indexProvided bool
}

// invoiceEditorInfo 发票操作结果与编辑状态
type invoiceEditorInfo struct {
	editorInfo
	InvoiceResult ofdgo.InvoiceUpdateResult `json:"invoiceResult"`
}

// annotationOptions 保存注解面板的输入，资源字节单独传输
type annotationOptions struct {
	Kind, ID, Text, Old, Creator, Pages, URI, Color string
	Base                                            string
	X, Y, Width, Height, Size, Angle                float64
	Alpha                                           int
	Tile                                            bool
	Keep                                            bool
	Target                                          int
	Area                                            *ofdgo.Box
}

// outputContext 定期让出浏览器线程，将取消请求传入库层长操作
type outputContext struct {
	context.Context
	mu         sync.Mutex
	cancel     context.CancelFunc
	checkpoint func() error
	checked    time.Time
	checks     atomic.Uint32
	interval   uint8
}

// pendingPDF 保存当前标签的增量转换、输入及操作回调
type pendingPDF struct {
	importer  *ofdgo.PDFImport
	retry     bool
	input     *ofdgo.Input
	page      int
	next      int
	progress  platformValue
	font      platformValue
	operation *outputContext
	report    func(string, int, int) error
	stage     string
	completed int
	total     int
}

// businessEngine 独立文档、编辑、剪贴板及操作上下文
type businessEngine struct {
	resources       *ofdgo.Resources
	currentSession  *Session
	currentEditor   *ofdgo.Editor
	pdf             *pendingPDF
	pdfWarnings     []byte
	saveSnapshot    *ofdgo.Editor
	printDocument   *ofdgo.PrintDocument
	pendingImport   *ofdgo.Reader
	copiedObjects   *editorClipboard
	portableObjects *editorClipboard
	copiedStyle     *ofdgo.GraphicObject
	ctx             context.Context
	resolveInput    func(context.Context, any) (io.ReaderAt, int64, error)
	conversionPool  *ofdgo.ConversionPool
}

// Open 打开浏览器内存中的OFD文档
// 入参: data OFD文件数据, opts 打开选项
// 返回: *Session 文档会话, error 错误信息
func Open(data []byte, opts OpenOptions) (*Session, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty ofd data")
	}
	input, err := ofdgo.NewInput(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	reader, err := ofdgo.NewReader(input, opts.ReaderOptions...)
	if err != nil {
		input.Close()
		return nil, err
	}
	session, err := newSession(reader, opts)
	if err != nil {
		input.Close()
		return nil, err
	}
	session.input = input
	return session, nil
}

// Close 关闭文档会话
// 返回: error 错误信息
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	reader := s.Reader
	input := s.input
	*s = Session{}
	var err error
	if reader != nil {
		err = reader.Close()
	}
	if input != nil {
		err = errors.Join(err, input.Close())
	}
	return err
}

// Document 创建指定文档的会话，保留编辑内容和字体配置，不复用页面缓存
// 不关闭原会话，输入文件的生命周期由原会话管理
// 入参: index 文档索引，从0开始
// 返回: *Session 文档会话, error 错误信息
func (s *Session) Document(index int) (*Session, error) {
	reader := s.Reader
	if s.editor != nil {
		var err error
		reader, err = s.editor.Reader(context.Background())
		if err != nil {
			return nil, err
		}
	}
	reader, err := reader.Document(index)
	if err != nil {
		return nil, err
	}
	next, err := newSession(reader, OpenOptions{RenderAnnotations: s.Renderer.RenderAnnotations})
	if err != nil {
		return nil, err
	}
	ofdgo.WithRenderBackends(s.Renderer.Backends())(next.Renderer)
	next.fontFS = s.fontFS
	if next.fontFS != nil {
		next.Renderer.SetFontFS(next.fontFS)
	}
	next.editor, err = reader.Editor()
	if err != nil {
		return nil, err
	}
	next.editor.SetHistoryLimit(100)
	if next.fontFS != nil {
		next.editor.SetFontFS(next.fontFS)
	}
	next.editor.SetRenderBackends(next.Renderer.Backends())
	next.editing = true
	return next, nil
}

// DocumentVersion 创建版本阅读会话，保留未保存内容和字体配置，不修改默认版本
// 入参: id 版本标识，空字符串选择主入口
// 返回: *Session 阅读会话, error 版本读取错误
func (s *Session) DocumentVersion(id string) (*Session, error) {
	reader := s.Reader
	if s.editor != nil {
		var err error
		reader, err = s.editor.Reader(context.Background())
		if err != nil {
			return nil, err
		}
	}
	reader, err := reader.DocumentVersion(id)
	if err != nil {
		return nil, err
	}
	next, err := newSession(reader, OpenOptions{RenderAnnotations: s.Renderer.RenderAnnotations})
	if err != nil {
		return nil, err
	}
	ofdgo.WithRenderBackends(s.Renderer.Backends())(next.Renderer)
	next.fontFS = s.fontFS
	if next.fontFS != nil {
		next.Renderer.SetFontFS(next.fontFS)
	}
	return next, nil
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
	s.textCache.Clear()
	clear(s.svgFonts)
	s.resetFontInfo()
	return nil
}

// PageText 获取指定页面文字，复用当前会话的文字索引
// 入参: index 页面索引
// 返回: *ofdgo.PageText 页面文字, error 错误信息
func (s *Session) PageText(index int) (*ofdgo.PageText, error) {
	text := s.cachedPageText(index)
	if text == nil {
		var err error
		text, err = s.readPageText(context.Background(), index)
		if err != nil {
			return nil, err
		}
		s.cachePageText(index, text)
	}
	return text, nil
}

// cachedPageText 获取缓存索引并更新最近访问顺序
// 入参: index 页面索引
// 返回: *ofdgo.PageText 文字索引，未缓存时为nil
func (s *Session) cachedPageText(index int) *ofdgo.PageText {
	return s.textCache.Get(index)
}

// cachePageText 按内存预算保留页面文字索引，复用阅读与搜索结果
// 入参: index 页面索引, text 文字索引
func (s *Session) cachePageText(index int, text *ofdgo.PageText) {
	s.textCache.Put(index, text)
}

// PageTextString 获取页面原文，不为全文复制保留额外的页面和文字索引
// 入参: index 页面索引
// 返回: string 页面原文, error 错误信息
func (s *Session) PageTextString(index int) (string, error) {
	text := s.cachedPageText(index)
	if text == nil {
		var err error
		text, err = s.readPageText(context.Background(), index)
		if err != nil {
			return "", err
		}
	}
	return text.String(), nil
}

// SearchPage 搜索指定页面，复用当前会话的文字索引
// 入参: index 页面索引, query 搜索文字
// 返回: []ofdgo.TextMatch 匹配结果, error 错误信息
func (s *Session) SearchPage(index int, query string) ([]ofdgo.TextMatch, error) {
	return s.SearchPageContext(context.Background(), index, query, 0)
}

// SearchPageContext 搜索页面文字并限制累计结果，零上限返回全部，取消时不返回部分结果
// 入参: ctx 取消上下文, index 页面索引, query 文字, limit 结果上限
// 返回: []ofdgo.TextMatch 匹配结果, error 取消或读取错误
func (s *Session) SearchPageContext(ctx context.Context, index int, query string, limit int) ([]ofdgo.TextMatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := s.cachedPageText(index)
	if text == nil {
		var err error
		text, err = s.readPageText(ctx, index)
		if err != nil {
			return nil, err
		}
		s.cachePageText(index, text)
	}
	var matches []ofdgo.TextMatch
	err := text.SearchEach(ctx, query, func(match ofdgo.TextMatch) error {
		matches = append(matches, match)
		if limit > 0 && len(matches) >= limit {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	return matches, nil
}

// Summary 获取首屏所需信息，不解析页面图元或执行验签
// 返回: DocumentInfo 文档信息
func (s *Session) Summary() DocumentInfo {
	info := DocumentInfo{
		DocumentIndex:  s.Reader.DocumentIndex(),
		Encryption:     s.encryptionInfo(),
		Version:        s.Reader.Version(),
		DocType:        s.Reader.DocType(),
		PageCount:      len(s.doc.Pages.Page),
		Pages:          s.pageInfos(),
		Outlines:       s.doc.OutlineInfos(),
		DetailsPending: true,
	}
	info.DocumentVersion, _ = s.Reader.DocumentVersionID()
	info.VersionCount, _ = s.Reader.DocumentVersionCount()
	for index, body := range s.Reader.OFD.DocBody {
		info.Documents = append(info.Documents, DocumentEntry{Index: index, DocumentMetadata: DocumentMetadata{Title: body.DocInfo.Title}, Root: body.DocRoot, ID: body.DocInfo.DocID})
	}
	if docInfo, err := s.Reader.DocInfo(); err == nil && docInfo != nil {
		info.DocumentMetadata = documentMetadata(*docInfo)
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

// Details 获取无需逐页扫描的标引、附件、签名及字体匹配结果
// 返回: DocumentDetails 文档补充信息
func (s *Session) Details() DocumentDetails {
	info := DocumentDetails{}
	if structures, err := s.Reader.Structure(); err == nil {
		info.Structures = structures
	} else {
		info.StructureError = err.Error()
	}
	if attachments, err := s.Reader.Attachments(); err == nil {
		for _, attachment := range attachments {
			if attachment.Visible || s.editing {
				info.Attachments = append(info.Attachments, AttachmentInfo{
					ID:       attachment.ID,
					Name:     attachment.Name,
					Format:   attachment.Format,
					Size:     attachment.Size,
					FileName: attachment.FileName(),
					Visible:  attachment.Visible,
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
		s.localizeFontInfos(fonts)
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
	info.Structures = details.Structures
	info.StructureError = details.StructureError
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
	return s.renderPage(index, backend, dpi, raster, false, nil)
}

// renderPage 按需生成预览及交互数据，省略调用方已持有的资源字节
// 入参: index 页面索引, backend 后端组合, dpi 光栅分辨率, raster 光栅显示, preview 仅预览, known 已持有资源
// 返回: PageSVG 页面结果, error 渲染错误
func (s *Session) renderPage(index int, backend string, dpi float64, raster, preview bool, known map[string]bool) (PageSVG, error) {
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
	if !preview && s.cachedPageText(index) == nil {
		renderer.OnPageText = func(_ *ofdgo.PageContent, text *ofdgo.PageText) {
			s.cachePageText(index, text)
		}
	}
	if raster {
		renderer.DPI = dpi
	}
	resources, err := renderer.RenderPreview(page, &buf, ofdgo.PreviewOptions{Raster: raster, Objects: !raster && s.editing && !preview, KnownResources: known})
	if err != nil {
		return PageSVG{}, err
	}
	clear(s.svgFonts)
	for i, font := range resources.Fonts {
		s.svgFonts[font.Name] = font.Data
		resources.Fonts[i].Data = nil
	}
	if preview {
		return PageSVG{Index: index, Number: index + 1, ID: page.ID, Width: box.W, Height: box.H, SVG: buf.String(), Fonts: resources.Fonts, Images: resources.Images}, nil
	}
	links, err := s.Renderer.PageLinks(page)
	if err != nil {
		return PageSVG{}, err
	}
	infos, err := s.Reader.AnnotationInfosByIndex(index)
	if err != nil {
		return PageSVG{}, err
	}
	annotations := make([]AnnotationPreview, len(infos))
	for i, info := range infos {
		annotations[i].AnnotationInfo = info
		if !renderer.RenderAnnotations || !info.Visible || strings.TrimSpace(info.Remark) == "" || info.Type == "Link" || info.Type == "Path" && info.Subtype == "Link" {
			continue
		}
		bounds, contours, err := renderer.AnnotationGeometry(s.Reader.Annots[page.ID][i])
		if err == nil {
			annotations[i].Bounds, annotations[i].Contours = bounds, contours
		}
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
		s.textCache.Clear()
	}
	ofdgo.WithRenderBackends(backends)(s.Renderer)
	return nil
}

// SVGFontData 获取最近渲染页面引用的字体数据
// 入参: name 字体资源标识
// 返回: []byte 字体数据, error 错误信息
func (s *Session) SVGFontData(name string) ([]byte, error) {
	data, ok := s.svgFonts[name]
	if !ok {
		return nil, fmt.Errorf("svg font not found: %s", name)
	}
	return data, nil
}

// ExportFormats 获取导出格式
// 返回: []ExportFormat 导出格式列表
func ExportFormats() []ExportFormat {
	return ofdgo.OutputFormats()[1:]
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
		textCache: ofdgo.NewPageTextCache(sessionTextCacheBytes),
		svgFonts:  make(map[string][]byte),
	}, nil
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
	s.localizeFontInfos(s.fontInfos)
	s.fontsRead = true
	s.fontScan = nil
	return true
}

// localizeFontInfos 统一内嵌字体在概要和完整统计中的中文名称
// 入参: fonts 当前文档的字体诊断信息
func (s *Session) localizeFontInfos(fonts []FontInfo) {
	for index := range fonts {
		if fonts[index].Embedded {
			if face, err := s.Reader.FontFace(fonts[index].ID, 3, 0x0804); err == nil {
				fonts[index].MatchedFace = face
			}
		}
	}
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

// readPageText 复用已加载页面，否则只解析文字所需图元
// 入参: ctx 取消上下文, index 页面索引
// 返回: *ofdgo.PageText 页面文字, error 错误信息
func (s *Session) readPageText(ctx context.Context, index int) (*ofdgo.PageText, error) {
	if page := s.pageCache[index]; page != nil {
		return s.Renderer.PageTextContext(ctx, page)
	}
	return s.Renderer.PageTextByIndexContext(ctx, index)
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
	ctx := s.Renderer.OutputContext
	if ctx == nil {
		ctx = context.Background()
	}
	page, err := s.Reader.PageContentByIndexContext(ctx, index)
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

// exportFormat 获取导出格式
// 入参: value 格式值
// 返回: ExportFormat 导出格式, bool 是否支持
func exportFormat(value string) (ExportFormat, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "jpeg" {
		value = "jpg"
	}
	for _, format := range ExportFormats() {
		if format.Value == value {
			return format, true
		}
	}
	return ExportFormat{}, false
}

// encryptionInfo 获取当前文档的保存策略，不包含凭据
// 返回: EncryptionInfo 加密状态
func (s *Session) encryptionInfo() EncryptionInfo {
	info := s.Reader.Encryption()
	if s.editor != nil {
		info = s.editor.Encryption()
	}
	return EncryptionInfo{Encrypted: info.Encrypted, Method: info.Method, Users: info.Users, Layers: info.Layers, PolicyRequired: info.PolicyRequired}
}

// signatureInfos 获取签名验证信息
// 返回: []SignatureInfo 签名验证信息, error 错误信息
func (s *Session) signatureInfos() ([]SignatureInfo, error) {
	if s.signaturesRead {
		return s.signatures, s.signatureError
	}
	s.signaturesRead = true
	reports, err := s.Reader.VerifyDocumentSignatures(s.Reader.DocumentIndex())
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

// Write 将数据块交给浏览器保存
// 入参: data 导出数据
// 返回: int 写入长度, error 错误信息
func (w exportWriter) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		n := min(len(data), 1<<20)
		if err := awaitExport(w.write, bytesToJS(data[:n])); err != nil {
			return size - len(data), err
		}
		data = data[n:]
	}
	return size, nil
}

// ReadAt 按需读取文件片段并定期交付取消检查点
// 入参: data 目标缓冲, offset 文件偏移
// 返回: int 读取字节数, error 读取错误
func (r *browserReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative file offset")
	}
	n := 0
	for len(data) > 0 {
		if offset >= r.size {
			return n, io.EOF
		}
		r.mu.Lock()
		check := r.checkpoint.Type() == platformTypeFunction && time.Since(r.checked) >= 50*time.Millisecond
		if check {
			r.checked = time.Now()
		}
		r.mu.Unlock()
		if check {
			if err := awaitExport(r.checkpoint); err != nil {
				return n, err
			}
		}
		copied, err := r.readChunk(data, offset)
		if err != nil {
			return n, err
		}
		n += copied
		offset += int64(copied)
		data = data[copied:]
	}
	return n, nil
}

// readChunk 串行读取和复用缓存块，仅保留完整读取的内容
// 入参: data 目标缓冲, offset 文件内偏移
// 返回: int 复制字节数, error 读取错误
func (r *browserReaderAt) readChunk(data []byte, offset int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.data) == 0 || offset < r.offset || offset-r.offset >= int64(len(r.data)) {
		start := offset / (256 << 10) * (256 << 10)
		size := int(min(int64(256<<10), r.size-start))
		buffer := r.data
		r.data = nil
		if cap(buffer) < size {
			buffer = make([]byte, size)
		} else {
			buffer = buffer[:size]
		}
		value := r.read.Invoke(float64(start), size)
		if platformCopyBytes(buffer, value) != size {
			return 0, io.ErrUnexpectedEOF
		}
		r.offset, r.data = start, buffer
	}
	return copy(data, r.data[offset-r.offset:]), nil
}

// renderBackends 返回库提供的内置后端能力
// 入参: args 浏览器参数
// 返回: any 后端能力, error 错误信息
func (e *businessEngine) renderBackends(args []platformValue) (any, error) {
	return ofdgo.RenderBackendInfos(), nil
}

// safeCall 调用浏览器回调并转换异常
// 入参: fn 回调函数, args 回调参数
// 返回: any 回调结果, error 错误信息
func (e *businessEngine) safeCall(fn func([]platformValue) (any, error), args []platformValue) (data any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if failure, ok := r.(error); ok {
				err = failure
			} else {
				err = fmt.Errorf("%v", r)
			}
		}
	}()
	return fn(args)
}

// detectDocumentFormat 通过库按需识别输入，不加载或替换文档会话
// 入参: args 随机读取回调、文件大小及检查点
// 返回: any 格式标识, error 读取或识别错误
func (e *businessEngine) detectDocumentFormat(args []platformValue) (any, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf("missing detection arguments")
	}
	reader := &browserReaderAt{read: args[0], size: int64(args[1].Float()), checkpoint: args[2]}
	input, err := ofdgo.NewInput(reader, reader.size)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	return ofdgo.DetectFormat(input)
}

// clearPDF 释放当前标签的未完成转换和输入
func (e *businessEngine) clearPDF() {
	e.pdfWarnings = nil
	if e.pdf == nil {
		return
	}
	e.pdf.importer.Close()
	e.pdf.input.Close()
	e.pdf = nil
}

// pdfContext 更新当前操作的取消、进度和按需字体读取回调
// 入参: args 进度及字体回调
// 返回: any 是否仍有待转换内容, error 参数错误
func (e *businessEngine) pdfContext(args []platformValue) (any, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("missing PDF callbacks")
	}
	if e.pdf != nil {
		e.pdf.progress, e.pdf.font = args[0], args[1]
	}
	return e.pdf != nil, nil
}

// openPDF 建立PDF索引，当前页转换由阅读请求触发
// 入参: args 输入、字体、密码、注解设置、字体目录、进度及字体回调
// 返回: any 文档索引, error 打开错误
func (e *businessEngine) openPDF(args []platformValue) (any, error) {
	if len(args) != 7 {
		return nil, fmt.Errorf("missing PDF input")
	}
	input, err := e.inputFromValue(args[0])
	if err != nil {
		return nil, err
	}
	pending := &pendingPDF{input: input, page: -1, progress: args[5], font: args[6]}
	keep := false
	var password []byte
	defer func() { clear(password) }()
	defer func() {
		if !keep {
			input.Close()
			if pending.importer != nil {
				pending.importer.Close()
			}
		}
	}()
	fonts, err := e.fontsFromJS(args[1])
	if err != nil {
		return nil, err
	}
	if !args[2].IsNull() && !args[2].IsUndefined() {
		password, err = e.bytesFromJS(args[2])
		if err != nil {
			return nil, err
		}
		password = slices.Clone(password)
	}
	e.bindPDFOperation(pending)
	options := ofdgo.PDFImportOptions{Resources: e.resources, PasswordUTF8: true, Password: password,
		OnProgress: func(stage string, completed, total int) error {
			return pending.report(stage, completed, total)
		},
	}
	sources := []fs.FS{ofdgo.NewFontFS(fonts)}
	if names := e.stringsFromJS(args[4]); len(names) != 0 {
		sources = append(sources, ofdgo.NewLazyFontFS(names, func(name string) ([]byte, error) {
			return e.loadConversionFont(pending.font, name)
		}))
	}
	options.RendererOptions = []ofdgo.RendererOption{ofdgo.WithFontFS(sources...)}
	pending.importer, err = ofdgo.NewPDFImport(pending.operation, input, options)
	if err != nil {
		return nil, err
	}
	if len(password) != 0 {
		if err := pending.importer.SetEncryption(&ofdgo.EncryptionOptions{Password: password}); err != nil {
			return nil, err
		}
	}
	reader, err := pending.importer.Reader(pending.operation, -1)
	if err != nil {
		return nil, err
	}
	session, err := newSession(reader, OpenOptions{Fonts: fonts, RenderAnnotations: args[3].Bool()})
	if err != nil {
		return nil, err
	}
	e.clearPDF()
	e.clearImport()
	e.currentSession.Close()
	e.currentSession, e.currentEditor = session, nil
	e.copiedObjects, e.copiedStyle = nil, nil
	e.pdf = pending
	keep = true
	return session.Summary(), nil
}

// pdfError 记录需要重建的取消状态，后续请求可继续使用原文档
// 入参: err 转换错误
// 返回: error 原错误
func (e *businessEngine) pdfError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		e.pdf.retry = true
	}
	return err
}

// replacePDFReader 切换独立阅读快照，保留当前字体和注解设置
// 入参: reader 已生成的阅读器
// 返回: error 会话创建错误
func (e *businessEngine) replacePDFReader(reader *ofdgo.Reader) error {
	previous := e.currentSession
	next, err := newSession(reader, OpenOptions{RenderAnnotations: previous.Renderer.RenderAnnotations})
	if err != nil {
		return err
	}
	dirs, sources := previous.Renderer.FontSources()
	ofdgo.WithFontDirs(dirs...)(next.Renderer)
	ofdgo.WithFontFS(sources...)(next.Renderer)
	ofdgo.WithRenderBackends(previous.Renderer.Backends())(next.Renderer)
	next.Renderer.OutputContext = e.pdf.operation
	next.fontFS = previous.fontFS
	next.svgFonts = previous.svgFonts
	e.currentSession = next
	return previous.Close()
}

// finishPDF 补全转换后直接创建阅读快照，不生成中间压缩包
// 返回: []byte 转换警告JSON, error 转换错误
func (e *businessEngine) finishPDF() ([]byte, error) {
	if e.pdf == nil {
		return e.pdfWarnings, nil
	}
	editor, report, err := e.pdf.importer.Finish(e.pdf.operation)
	if err != nil {
		return nil, e.pdfError(err)
	}
	reader, err := editor.Reader(e.pdf.operation)
	if err != nil {
		return nil, err
	}
	if err := e.replacePDFReader(reader); err != nil {
		return nil, err
	}
	warnings, err := json.Marshal(report.Warnings)
	e.clearPDF()
	e.pdfWarnings = warnings
	return warnings, err
}

// loadPDF 按时间片补全未转换页面，全部完成后交付完整会话
// 入参: args 空参数
// 返回: any 完成标识、文档信息及警告, error 转换错误
func (e *businessEngine) loadPDF(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if e.pdf != nil {
		deadline := time.Now().Add(32 * time.Millisecond)
		for e.pdf.next < e.pdf.importer.PageCount() {
			index := e.pdf.next
			if !e.pdf.importer.Ready(index) {
				if err := e.pdf.importer.ConvertPage(e.pdf.operation, index); err != nil {
					return nil, e.pdfError(err)
				}
			}
			e.pdf.next++
			if time.Now().After(deadline) {
				return map[string]any{"done": false}, nil
			}
		}
	}
	warnings, err := e.finishPDF()
	if err != nil {
		return nil, err
	}
	return map[string]any{"done": true, "doc": e.currentSession.Summary(), "warnings": string(warnings)}, nil
}

// checkDocument 检查命令所需内容，不隐式转换或产生输出
// 入参: name 命令名称
// 返回: error 文档尚未完整时返回ErrIncompleteDocument
func (e *businessEngine) checkDocument(name string) error {
	if e.pdf == nil {
		return nil
	}
	switch name {
	case "ofdgoFontFaces", "ofdgoFontMenu", "ofdgoFontPreferenceOrder", "ofdgoFontFace", "ofdgoCheckTextFont", "ofdgoImagePageSize":
		return nil
	case "ofdgoDiscardObjects", "ofdgoExportObjects", "ofdgoCheckDocument", "ofdgoPDFContext", "ofdgoOpenPDF", "ofdgoOpen", "ofdgoLoadPDF", "ofdgoConfigure", "ofdgoDocumentInfo", "ofdgoSVGFontData", "ofdgoRenderBackends", "ofdgoOutputFormats", "ofdgoExportFormats", "ofdgoParsePageRange", "ofdgoMatchFontFiles", "ofdgoOutlineLinks", "ofdgoDocumentNavigation", "ofdgoInvoices", "ofdgoRenderPage", "ofdgoSearchPage", "ofdgoPageTextString", "ofdgoOpenLinks", "ofdgoDiscardPrint", "ofdgoDiscardSave", "ofdgoDetectFormat", "ofdgoConvertFile", "ofdgoPackFiles", "ofdgoLoadImport", "ofdgoCreateDocument", "ofdgoLayoutPrint", "ofdgoPrintSide", "ofdgoPrintPDF":
		return nil
	default:
		return ofdgo.ErrIncompleteDocument
	}
}

// bindPDFOperation 更新取消上下文并保留转换阶段，时间片检查不重置进度
// 入参: pending 当前转换任务
func (e *businessEngine) bindPDFOperation(pending *pendingPDF) {
	operation, report := e.newEditorOperationContext(pending.progress)
	pending.operation = operation
	pending.report = func(stage string, completed, total int) error {
		pending.stage, pending.completed, pending.total = stage, completed, total
		return report(stage, completed, total)
	}
	operation.checkpoint = func() error {
		return report(pending.stage, pending.completed, pending.total)
	}
}

// preparePDF 只为页面读取和后台加载绑定转换上下文，不补全其他命令
// 入参: name 命令名称, args 命令参数
// 返回: error 转换或取消错误
func (e *businessEngine) preparePDF(name string, args []platformValue) error {
	if e.pdf == nil {
		return nil
	}
	switch name {
	case "ofdgoLoadPDF", "ofdgoRenderPage", "ofdgoSearchPage", "ofdgoPageTextString", "ofdgoOpenLinks":
	default:
		return nil
	}
	e.bindPDFOperation(e.pdf)
	e.pdf.operation.checked = time.Now()
	e.pdf.operation.interval = 255
	e.currentSession.Renderer.OutputContext = e.pdf.operation
	if e.pdf.retry {
		if err := e.pdf.importer.Reset(e.pdf.operation); err != nil {
			return err
		}
		e.pdf.page, e.pdf.next, e.pdf.retry = -1, 0, false
		reader, err := e.pdf.importer.Reader(e.pdf.operation, -1)
		if err != nil {
			return err
		}
		if err := e.replacePDFReader(reader); err != nil {
			return err
		}
	}
	switch name {
	case "ofdgoLoadPDF":
		return nil
	case "ofdgoOpenLinks":
		if len(args) == 0 {
			return nil
		}
		fallthrough
	case "ofdgoRenderPage", "ofdgoSearchPage", "ofdgoPageTextString":
		if len(args) == 0 {
			return fmt.Errorf("missing page index")
		}
		index := args[0].Int()
		if e.pdf.page == index {
			return nil
		}
		reader, err := e.pdf.importer.Reader(e.pdf.operation, index)
		if err != nil {
			return e.pdfError(err)
		}
		if err := e.replacePDFReader(reader); err != nil {
			return err
		}
		e.pdf.page = index
		return nil
	default:
		return nil
	}
}

// convertFile 使用库转换独立文件，不读取或修改当前阅读和编辑会话
// 入参: args 随机读取、文件大小、转换选项、写出、进度、文件边界、取消检查、字体及按需字体读取
// 返回: any 转换报告, error 转换错误
func (e *businessEngine) convertFile(args []platformValue) (any, error) {
	if len(args) != 9 {
		return nil, fmt.Errorf("missing conversion arguments")
	}
	settings := args[2]
	backends, err := ofdgo.NewRenderBackends(settings.Get("backend").String())
	if err != nil {
		return nil, err
	}
	fonts, err := e.fontsFromJS(args[7])
	if err != nil {
		return nil, err
	}
	options := ofdgo.ConvertOptions{
		Resources: e.resources,
		Format:    settings.Get("format").String(), PageRange: settings.Get("pages").String(),
		Pool:            e.conversionPool,
		SkipUnchanged:   true,
		Backends:        &backends,
		RendererOptions: []ofdgo.RendererOption{ofdgo.WithDPI(settings.Get("dpi").Float())},
		OnProgress: func(progress ofdgo.ConvertProgress) error {
			return awaitExport(args[4], progress.Stage, progress.Completed, progress.Total)
		},
	}
	if value := settings.Get("fontMode"); value.Type() == platformTypeNumber {
		mode := value.Int()
		if mode < 0 || mode > int(ofdgo.FontExternal) {
			return nil, fmt.Errorf("invalid font mode")
		}
		options.Fonts = ofdgo.FontMode(mode)
	}
	if value := settings.Get("compression"); !value.IsNull() && !value.IsUndefined() {
		if err := e.compressionFromJS(value, &options.Compression); err != nil {
			return nil, err
		}
	}
	fontSources := []fs.FS{ofdgo.NewFontFS(fonts)}
	if names := settings.Get("fontNames"); !names.IsUndefined() && names.Length() > 0 {
		fontSources = append(fontSources, ofdgo.NewLazyFontFS(e.stringsFromJS(names), func(name string) ([]byte, error) {
			return e.loadConversionFont(args[8], name)
		}))
	}
	options.RendererOptions = append(options.RendererOptions, ofdgo.WithFontFS(fontSources...))
	if value := settings.Get("credentials"); !value.IsNull() && !value.IsUndefined() {
		credentials, err := e.credentialsFromJS(value)
		if err != nil {
			return nil, err
		}
		defer clear(credentials.Password)
		options.ReaderOptions = append(options.ReaderOptions, ofdgo.WithCredentials(credentials))
		options.PDF.Password = credentials.Password
		options.PDF.PasswordUTF8 = true
	}
	var format ofdgo.OutputFormat
	for _, item := range ofdgo.OutputFormats() {
		if item.Value == options.Format {
			format = item
		}
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[3]}, 1<<20)
	if format.Paged {
		options.PageOutput = func(index int, export func(io.Writer) error) error {
			name := fmt.Sprintf("%06d.%s", index+1, format.Extension)
			if err := awaitExport(args[5], "open", name); err != nil {
				return err
			}
			if err := export(writer); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
			return awaitExport(args[5], "close", name)
		}
	} else if err := awaitExport(args[5], "open", "document."+format.Extension); err != nil {
		return nil, err
	}
	reader := &browserReaderAt{read: args[0], checkpoint: args[6], size: int64(args[1].Float())}
	input, err := ofdgo.NewInput(reader, reader.size)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	report, err := ofdgo.Convert(e.newOutputContext(args[6]), input, writer, options)
	if err != nil {
		return nil, err
	}
	if report.Unchanged && report.Bytes == 0 {
		return report, nil
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	if !format.Paged {
		if err := awaitExport(args[5], "close", "document."+format.Extension); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// packFiles 将已经成功转换的文件逐个打包，不重复转换或读取整个文件
// 入参: args 文件列表、分块读取、分块写出、进度及取消检查
// 返回: any 输出格式, error 打包错误
func (e *businessEngine) packFiles(args []platformValue) (any, error) {
	if len(args) < 5 {
		return nil, fmt.Errorf("missing archive arguments")
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[2]}, 1<<20)
	files := args[0]
	entries := make([]ofdgo.ArchiveFile, files.Length())
	inputs := make([]*browserReaderAt, files.Length())
	defer func() {
		for _, entry := range entries {
			if entry.Input != nil {
				entry.Input.Close()
			}
		}
	}()
	for i := 0; i < files.Length(); i++ {
		file := files.Index(i)
		name := file.Get("name").String()
		read := args[1].Invoke(i)
		reader := &browserReaderAt{read: read, checkpoint: args[4], size: int64(file.Get("size").Float())}
		input, err := ofdgo.NewInput(reader, reader.size)
		if err != nil {
			return nil, err
		}
		inputs[i] = reader
		entries[i] = ofdgo.ArchiveFile{Name: name, Input: input}
	}
	options := ofdgo.ArchiveOptions{OnProgress: func(completed, total int) error {
		if completed > 0 {
			inputs[completed-1].data = nil
		}
		return awaitExport(args[3], completed, total)
	}}
	if len(args) > 5 && !args[5].IsUndefined() && !args[5].IsNull() {
		if err := e.compressionFromJS(args[5], &options.Compression); err != nil {
			return nil, err
		}
	}
	if _, err := ofdgo.WriteArchive(e.newOutputContext(args[4]), writer, entries, options); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return map[string]any{"mime": "application/zip"}, nil
}

// openDocument 打开OFD文档
// 入参: args 浏览器参数
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) openDocument(args []platformValue) (any, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("missing open arguments")
	}
	input, err := e.inputFromValue(args[0])
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			input.Close()
		}
	}()
	fonts, err := e.fontsFromJS(args[1])
	if err != nil {
		return nil, err
	}
	renderAnnotations := args[2].Bool()
	options := OpenOptions{Fonts: fonts, RenderAnnotations: renderAnnotations, ReaderOptions: []ofdgo.ReaderOption{ofdgo.WithResources(e.resources)}}
	if len(args) > 3 && !args[3].IsNull() && !args[3].IsUndefined() {
		credentials, err := e.credentialsFromJS(args[3])
		if err != nil {
			return nil, err
		}
		defer clear(credentials.Password)
		options.ReaderOptions = append(options.ReaderOptions, ofdgo.WithCredentials(credentials))
	}
	reader, err := ofdgo.NewReader(input, options.ReaderOptions...)
	if err != nil {
		return nil, err
	}
	session, err := newSession(reader, options)
	if err != nil {
		return nil, err
	}
	e.clearPDF()
	e.clearImport()
	e.currentEditor = nil
	e.copiedObjects = nil
	e.copiedStyle = nil
	if e.currentSession != nil {
		_ = e.currentSession.Close()
		e.currentSession = nil
	}
	e.currentSession = session
	session.input = input
	keep = true
	return e.currentSession.Summary(), nil
}

// configureDocument 更新当前会话的字体和注解设置
// 入参: args 浏览器参数
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) configureDocument(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if !args[0].IsNull() && !args[0].IsUndefined() {
		fonts, err := e.fontsFromJS(args[0])
		if err != nil {
			return nil, err
		}
		if err := e.currentSession.SetFonts(fonts); err != nil {
			return nil, err
		}
		if e.currentEditor != nil {
			e.currentEditor.SetFontFS()
			if e.currentSession.fontFS != nil {
				e.currentEditor.SetFontFS(e.currentSession.fontFS)
			}
		}
	}
	if e.currentSession.Renderer.RenderAnnotations != args[1].Bool() {
		e.currentSession.Renderer.RenderAnnotations = args[1].Bool()
		e.currentSession.textCache.Clear()
	}
	return e.currentSession.Summary(), nil
}

// documentInfo 获取字体统计和验签结果，首个参数为true时分批扫描
// 入参: args 浏览器参数
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) documentInfo(args []platformValue) (any, error) {
	if e.pdf != nil {
		return map[string]any{"detailsPending": true}, nil
	}
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) > 0 && args[0].Bool() {
		if !e.currentSession.scanFontInfoBatch() {
			return map[string]bool{"detailsPending": true}, nil
		}
		return map[string]any{"fonts": e.currentSession.fontInfos, "fontCount": len(e.currentSession.fontInfos), "detailsPending": false}, nil
	}
	return e.currentSession.Details(), nil
}

// documentImages 读取当前文档的原始图片目录
// 入参: args 浏览器参数
// 返回: any 图片目录, error 读取错误
func (e *businessEngine) documentImages(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) != 0 {
		return e.currentSession.Reader.Image(e.operationContext(), args[0].String())
	}
	return e.currentSession.Reader.Images(e.operationContext())
}

// exportImages 分块保存单幅原始图片或图片归档
// 入参: args 图片标识和写出回调，空标识保存全部
// 返回: any 文件类型, error 读写错误
func (e *businessEngine) exportImages(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[1]}, 1<<20)
	mime := "application/octet-stream"
	if id := args[0].String(); id != "" {
		input, err := e.currentSession.Reader.OpenImage(e.operationContext(), id)
		if err != nil {
			return nil, err
		}
		defer input.Close()
		if _, err := io.Copy(writer, input); err != nil {
			return nil, err
		}
	} else {
		mime = "application/zip"
		if err := e.currentSession.Reader.WriteImages(e.operationContext(), writer); err != nil {
			return nil, err
		}
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"mime": mime}), nil
}

// exportAttachment 分块导出附件
// 入参: args 浏览器参数
// 返回: any 附件数据, error 错误信息
func (e *businessEngine) exportAttachment(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	stream, err := e.currentSession.Reader.OpenAttachment(args[0].String())
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	writer := bufio.NewWriterSize(exportWriter{write: args[1]}, 1<<20)
	if _, err := io.Copy(writer, stream); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{
		"mime": "application/octet-stream",
	}), nil
}

// exportMedia 分块读取文档音视频，播放由浏览器负责
// 入参: args 资源标识和分块接收函数
// 返回: any 媒体格式, error 资源或读取错误
func (e *businessEngine) exportMedia(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	media, err := e.currentSession.Reader.Media(args[0].String())
	if err != nil {
		return nil, err
	}
	stream, err := e.currentSession.Reader.OpenMedia(media.ID)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	writer := bufio.NewWriterSize(exportWriter{write: args[1]}, 1<<20)
	if _, err := io.Copy(writer, stream); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"format": media.Format, "kind": media.Type}), nil
}

// documentResources 读取当前文档和版本的资源摘要及引用总数，不执行媒体动作
// 入参: args 可选的取消检查回调
// 返回: any 资源摘要, error 读取错误
func (e *businessEngine) documentResources(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	ctx := e.operationContext()
	if len(args) == 1 {
		operation := e.newOutputContext(args[0])
		operation.interval = 255
		ctx = operation
	}
	return e.currentSession.Reader.ResourceSummaries(ctx)
}

// documentResourceReferences 分页读取选中资源的引用位置
// 入参: args 资源类型、标识、零基起始位置和最大数量
// 返回: any 引用列表, error 参数或读取错误
func (e *businessEngine) documentResourceReferences(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 4 {
		return nil, fmt.Errorf("invalid resource reference request")
	}
	return e.currentSession.Reader.ResourceReferences(e.operationContext(), args[0].String(), args[1].String(), args[2].Int(), args[3].Int())
}

// resourceTargets 列出当前页可绑定指定资源的图元及已有绑定状态
// 入参: args 零基页码、资源类型和标识
// 返回: any 图元列表, error 页面读取错误
func (e *businessEngine) resourceTargets(args []platformValue) (any, error) {
	if e.currentEditor == nil || len(args) != 3 {
		return nil, fmt.Errorf("no document is being edited")
	}
	return e.currentEditor.ResourceTargets(e.operationContext(), args[1].String(), args[2].String(), args[0].Int())
}

// documentInvoices 获取发票列表与格式能力
// 入参: args 空参数
// 返回: any 发票提取结果与格式列表, error 读取错误
func (e *businessEngine) documentInvoices(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 0 {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	r, close, err := e.currentInvoiceReader()
	if err != nil {
		return nil, err
	}
	defer close()
	report, err := r.Invoices()
	if err != nil {
		return nil, err
	}
	return map[string]any{"report": report, "formats": ofdgo.InvoiceFormats()}, nil
}

// currentInvoiceReader 获取当前发票读取器
// 返回: *ofdgo.Reader 当前读取器, func() 快照释放函数, error 快照错误
func (e *businessEngine) currentInvoiceReader() (*ofdgo.Reader, func(), error) {
	if e.currentEditor != nil {
		r, err := e.currentEditor.Reader(e.operationContext())
		if err != nil {
			return nil, nil, err
		}
		return r, func() { _ = r.Close() }, nil
	}
	if e.currentSession == nil {
		return nil, nil, fmt.Errorf("ofd document is not opened")
	}
	return e.currentSession.Reader, func() {}, nil
}

// decodeInvoiceRequest 解析发票参数并校验文档状态
// 入参: args 单个JSON操作对象
// 返回: invoiceRequest 操作参数, error 参数或过期状态错误
func (e *businessEngine) decodeInvoiceRequest(args []platformValue) (invoiceRequest, error) {
	var request invoiceRequest
	if e.currentSession == nil || len(args) != 1 || args[0].Type() != platformTypeString {
		return request, fmt.Errorf("invalid invoice arguments")
	}
	data := args[0].String()
	if len(data) > 64<<20 {
		return request, fmt.Errorf("invoice request exceeds size limit")
	}
	if err := json.Unmarshal([]byte(data), &request); err != nil {
		return request, err
	}
	var presence struct {
		Operations []struct {
			Index *int `json:"index"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(data), &presence); err != nil {
		return request, err
	}
	for i := range presence.Operations {
		request.Operations[i].indexProvided = presence.Operations[i].Index != nil
	}
	index := e.currentSession.Reader.DocumentIndex()
	if e.currentEditor != nil {
		index = e.currentEditor.DocumentIndex()
	}
	if request.DocumentIndex != nil && *request.DocumentIndex != index {
		return request, fmt.Errorf("invoice document has changed")
	}
	if request.Version != nil {
		version, err := e.currentSession.Reader.DocumentVersionID()
		if e.currentEditor != nil {
			version, err = e.currentEditor.DocumentVersionID()
		}
		if err != nil {
			return request, err
		}
		if *request.Version != version {
			return request, fmt.Errorf("invoice document version has changed")
		}
	}
	if request.Revision != nil && (e.currentEditor == nil || *request.Revision != e.currentEditor.Revision()) {
		return request, fmt.Errorf("invoice editing revision has changed")
	}
	return request, nil
}

// documentInvoice 获取发票详情与票面关联
// 入参: args 来源及来源内发票索引，新建时提供发票模型
// 返回: any 发票详情, error 来源、索引或读取错误
func (e *businessEngine) documentInvoice(args []platformValue) (any, error) {
	request, err := e.decodeInvoiceRequest(args)
	if err != nil {
		return nil, err
	}
	ctx := e.operationContext()
	if request.Source.Kind == "" {
		return map[string]any{"invoice": request.Invoice, "fields": ofdgo.InvoiceFields(request.Invoice)}, nil
	}
	r, close, err := e.currentInvoiceReader()
	if err != nil {
		return nil, err
	}
	defer close()
	data, err := r.ExportInvoice(ctx, request.Source)
	if err != nil {
		return nil, err
	}
	report, err := ofdgo.DecodeInvoices(ctx, data)
	if request.Source.Kind == "tag" && len(report.Invoices) == 0 {
		report, err = r.ReadInvoice(ctx, request.Source)
	}
	if err != nil {
		return nil, err
	}
	if request.Index < 0 || request.Index >= len(report.Invoices) {
		return nil, fmt.Errorf("invoice index out of range")
	}
	value := report.Invoices[request.Index]
	standardLabel := value.Standard
	switch value.Standard {
	case ofdgo.InvoiceStandardEDRM2019:
		standardLabel = "电子发票/2019"
	case ofdgo.InvoiceStandardObjectReference:
		standardLabel = "发票标引"
	}
	capabilities, err := r.InvoiceCapabilities(ctx, request.Source, request.Index)
	if err != nil {
		return nil, err
	}
	bindings, bindingErr := r.InvoiceBindings(ctx, request.Source, request.Index)
	if bindingErr != nil {
		report.Issues = append(report.Issues, ofdgo.InvoiceIssue{Code: "binding-error", Message: bindingErr.Error(), Source: request.Source})
	}
	return map[string]any{"invoice": value, "standardLabel": standardLabel, "fields": ofdgo.InvoiceFields(value), "capabilities": capabilities, "bindings": bindings, "digest": ofdgo.InvoiceDigest(data), "xml": string(data), "issues": report.Issues}, nil
}

// exportDocumentInvoice 导出发票原文
// 入参: args 发票来源
// 返回: any 原始字节, error 来源或读取错误
func (e *businessEngine) exportDocumentInvoice(args []platformValue) (any, error) {
	request, err := e.decodeInvoiceRequest(args)
	if err != nil {
		return nil, err
	}
	r, close, err := e.currentInvoiceReader()
	if err != nil {
		return nil, err
	}
	defer close()
	data, err := r.ExportInvoice(e.operationContext(), request.Source)
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(data)}), nil
}

// validateDocumentInvoice 校验发票草稿
// 入参: args 发票模型或来源、草稿操作与原始摘要
// 返回: any 字段与票面诊断, error 校验或读取错误
func (e *businessEngine) validateDocumentInvoice(args []platformValue) (any, error) {
	request, err := e.decodeInvoiceRequest(args)
	if err != nil {
		return nil, err
	}
	ctx := e.operationContext()
	if request.Operation == "" {
		switch {
		case len(request.Operations) != 0:
			request.Operation = "batch"
		case len(request.Changes) != 0:
			request.Operation = "patch"
		case request.XML != "":
			request.Operation = "replace"
			if request.Source.Kind == "" {
				request.Operation = "import"
			}
		}
	}
	if request.Operation != "" {
		return e.validateInvoiceDraft(ctx, request)
	}
	issues, err := ofdgo.ValidateInvoice(ctx, request.Invoice)
	if err != nil {
		return nil, err
	}
	var appearance []ofdgo.InvoiceIssue
	if request.Source.Kind != "" {
		r, close, err := e.currentInvoiceReader()
		if err != nil {
			return nil, err
		}
		defer close()
		appearance, err = r.CheckInvoiceAppearance(ctx, request.Source, request.Index)
		if err != nil {
			appearance = append(appearance, ofdgo.InvoiceIssue{Code: "appearance-error", Message: err.Error(), Source: request.Source})
		}
	}
	return map[string]any{"issues": issues, "appearance": appearance}, nil
}

// validateInvoiceDraft 在独立快照中校验发票修改
// 入参: ctx 取消上下文, request 已核对当前文档状态的操作
// 返回: any 草稿发票及诊断, error 演练或读取错误
func (e *businessEngine) validateInvoiceDraft(ctx context.Context, request invoiceRequest) (any, error) {
	r, close, err := e.currentInvoiceReader()
	if err != nil {
		return nil, err
	}
	defer close()
	editor, err := r.EditorContext(ctx)
	if err != nil {
		return nil, err
	}
	request.Operations = append([]invoiceRequest(nil), request.Operations...)
	for i := range request.Operations {
		operation := &request.Operations[i]
		if operation.Revision != nil && (e.currentEditor == nil || *operation.Revision != e.currentEditor.Revision()) {
			return nil, fmt.Errorf("invoice editing revision has changed")
		}
		operation.Revision = nil
	}
	var result ofdgo.InvoiceUpdateResult
	if err := editor.Transaction(func(next *ofdgo.Editor) error {
		var err error
		result, err = e.applyInvoiceRequest(ctx, next, request)
		return err
	}); err != nil {
		return nil, err
	}
	issues := append([]ofdgo.InvoiceIssue(nil), result.Issues...)
	var appearance []ofdgo.InvoiceIssue
	if request.Operation == "delete" {
		return map[string]any{"issues": issues, "appearance": appearance}, nil
	}
	source := result.Source
	if source.Kind == "" {
		source = request.Source
	}
	view, err := editor.Reader(ctx)
	if err != nil {
		return nil, err
	}
	defer view.Close()
	data, err := view.ExportInvoice(ctx, source)
	if err != nil {
		return nil, err
	}
	report, err := ofdgo.DecodeInvoices(ctx, data)
	if source.Kind == "tag" && len(report.Invoices) == 0 {
		report, err = view.ReadInvoice(ctx, source)
	}
	if err != nil {
		return nil, err
	}
	issues = append(issues, report.Issues...)
	for index, invoice := range report.Invoices {
		validation, err := ofdgo.ValidateInvoice(ctx, invoice)
		if err != nil {
			return nil, err
		}
		for i := range validation {
			validation[i].Source = source
		}
		issues = append(issues, validation...)
		checks, err := view.CheckInvoiceAppearance(ctx, source, index)
		if err != nil {
			appearance = append(appearance, ofdgo.InvoiceIssue{Code: "appearance-error", Message: err.Error(), Source: source})
		} else {
			appearance = append(appearance, checks...)
		}
	}
	return map[string]any{"issues": issues, "appearance": appearance, "invoices": report.Invoices}, nil
}

// changeDocumentInvoice 提交发票修改
// 入参: args 操作及业务参数
// 返回: any 编辑状态与签名诊断, error 校验或提交错误
func (e *businessEngine) changeDocumentInvoice(args []platformValue) (any, error) {
	request, err := e.decodeInvoiceRequest(args)
	if err != nil {
		return nil, err
	}
	ctx := e.operationContext()
	var result ofdgo.InvoiceUpdateResult
	info, err := e.changeAtomicObjects(func() error {
		return e.currentEditor.Transaction(func(editor *ofdgo.Editor) error {
			var err error
			result, err = e.applyInvoiceRequest(ctx, editor, request)
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return invoiceEditorInfo{editorInfo: info.(editorInfo), InvoiceResult: result}, nil
}

// prepareInvoiceLayout 在发票创建事务内导入票面字体，不修改调用方布局
// 入参: ctx 取消上下文, editor 事务编辑器, request 发票请求
// 返回: error 字体处理错误
func (e *businessEngine) prepareInvoiceLayout(ctx context.Context, editor *ofdgo.Editor, request *invoiceRequest) error {
	if request.Options.Layout == nil || len(request.FontData) == 0 {
		return nil
	}
	layout := *request.Options.Layout
	request.Options.Layout = &layout
	file := ofdgo.FontFile{Data: request.FontData}
	if layout.Font != "" {
		return editor.EmbedFont(ctx, layout.Font, file, 0)
	}
	id, err := editor.AddFont(file, 0)
	if err != nil {
		return err
	}
	layout.Font = id
	return nil
}

// applyInvoiceRequest 校验来源摘要并执行发票操作
// 入参: ctx 取消上下文, editor 事务编辑器, request 操作参数
// 返回: ofdgo.InvoiceUpdateResult 提交结果, error 校验或写入错误
func (e *businessEngine) applyInvoiceRequest(ctx context.Context, editor *ofdgo.Editor, request invoiceRequest) (ofdgo.InvoiceUpdateResult, error) {
	var result ofdgo.InvoiceUpdateResult
	if request.Operation == "batch" {
		return e.applyInvoiceBatch(ctx, editor, request)
	}
	if len(request.Operations) != 0 {
		return result, fmt.Errorf("invoice operations require batch action")
	}
	err := func() error {
		if request.Operation != "create" && request.Operation != "import" {
			data, err := editor.ExportInvoice(ctx, request.Source)
			if err != nil {
				return err
			}
			if request.Digest == "" || ofdgo.InvoiceDigest(data) != request.Digest {
				return fmt.Errorf("invoice source has changed")
			}
		}
		switch request.Operation {
		case "create", "import":
			var source ofdgo.InvoiceSource
			var err error
			if err = e.prepareInvoiceLayout(ctx, editor, &request); err != nil {
				return err
			}
			if request.Operation == "create" {
				source, err = editor.CreateInvoice(ctx, request.Invoice, request.Options)
			} else {
				source, err = editor.ImportInvoice(ctx, []byte(request.XML), request.Options)
			}
			result.Source = source
			return err
		case "set":
			var err error
			result, err = editor.SetInvoice(ctx, request.Source, request.Digest, request.Index, request.Invoice, request.Synchronize)
			return err
		case "patch":
			for i := range request.Changes {
				request.Changes[i].Source.Kind = request.Source.Kind
				request.Changes[i].Source.ID = request.Source.ID
				request.Changes[i].Source.FileLoc = request.Source.FileLoc
				if request.Source.NodePath != "" {
					request.Changes[i].Source.NodePath = request.Source.NodePath + request.Changes[i].Source.NodePath
				}
			}
			var err error
			result, err = editor.EditInvoice(ctx, request.Source, request.Digest, request.Changes, request.Synchronize)
			return err
		case "replace":
			var err error
			result, err = editor.UpdateInvoice(ctx, request.Source, request.Digest, []byte(request.XML), request.Synchronize)
			return err
		case "delete":
			return editor.DeleteInvoice(ctx, request.Source, request.Digest)
		case "bindings":
			return editor.SetInvoiceBindings(ctx, request.Source, request.Index, request.Bindings)
		case "generate":
			if len(request.FontData) != 0 {
				file := ofdgo.FontFile{Data: request.FontData}
				if request.Layout.Font == "" {
					id, err := editor.AddFont(file, 0)
					if err != nil {
						return err
					}
					request.Layout.Font = id
				} else if err := editor.EmbedFont(ctx, request.Layout.Font, file, 0); err != nil {
					return err
				}
			}
			return editor.GenerateInvoicePages(ctx, request.Source, request.Index, request.Layout)
		case "sync":
			data, err := editor.ExportInvoice(ctx, request.Source)
			if err != nil {
				return err
			}
			result, err = editor.UpdateInvoice(ctx, request.Source, request.Digest, data, true)
			return err
		default:
			return fmt.Errorf("unsupported invoice action")
		}
	}()
	if err != nil {
		return ofdgo.InvoiceUpdateResult{}, err
	}
	return result, nil
}

// applyInvoiceBatch 合并发票修改并同步票面
// 入参: ctx 取消上下文, editor 事务编辑器, request 批次及原始来源
// 返回: ofdgo.InvoiceUpdateResult 提交结果, error 冲突或写入错误
func (e *businessEngine) applyInvoiceBatch(ctx context.Context, editor *ofdgo.Editor, request invoiceRequest) (ofdgo.InvoiceUpdateResult, error) {
	var result ofdgo.InvoiceUpdateResult
	data, err := editor.ExportInvoice(ctx, request.Source)
	if err != nil {
		return result, err
	}
	if request.Digest == "" || ofdgo.InvoiceDigest(data) != request.Digest {
		return result, fmt.Errorf("invoice source has changed")
	}
	if len(request.Operations) == 0 || len(request.Operations) > 256 {
		return result, fmt.Errorf("invalid invoice batch size")
	}
	report, err := ofdgo.DecodeInvoices(ctx, data)
	if request.Source.Kind == "tag" && len(report.Invoices) == 0 {
		view, viewErr := editor.Reader(ctx)
		if viewErr != nil {
			return result, viewErr
		}
		defer view.Close()
		report, err = view.ReadInvoice(ctx, request.Source)
	}
	if err != nil {
		return result, err
	}
	if request.Index < 0 || request.Index >= len(report.Invoices) {
		return result, fmt.Errorf("invoice index out of range")
	}
	paths := make(map[string]bool)
	for _, fact := range report.Invoices[request.Index].Facts {
		paths[fact.Source.NodePath] = true
	}
	for _, item := range report.Invoices[request.Index].Items {
		for _, fact := range item.Fields {
			paths[fact.Source.NodePath] = true
		}
	}
	request.Operations = append([]invoiceRequest(nil), request.Operations...)
	for i := range request.Operations {
		operation := &request.Operations[i]
		if (operation.indexProvided || operation.Index != 0) && operation.Index != request.Index {
			return result, fmt.Errorf("invoice batch cannot span candidates")
		}
		operation.Index = request.Index
	}
	document := editor.DocumentIndex()
	version, err := editor.DocumentVersionID()
	if err != nil {
		return result, err
	}
	revision := editor.Revision()
	models, bindings := make(map[int]bool), make(map[int]bool)
	patched := make(map[string]bool)
	hasSet, hasPatch := false, false
	for _, operation := range request.Operations {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(operation.Operations) != 0 || operation.Operation == "batch" || operation.Synchronize {
			return result, fmt.Errorf("nested invoice batch or synchronization is not supported")
		}
		if operation.Source != (ofdgo.InvoiceSource{}) && operation.Source != request.Source {
			return result, fmt.Errorf("invoice batch cannot span sources")
		}
		if operation.Digest != "" && operation.Digest != request.Digest {
			return result, fmt.Errorf("invoice batch requires one original digest")
		}
		if operation.DocumentIndex != nil && *operation.DocumentIndex != document || operation.Version != nil && *operation.Version != version || operation.Revision != nil && *operation.Revision != revision {
			return result, fmt.Errorf("invoice batch scope has changed")
		}
		switch operation.Operation {
		case "set":
			if models[operation.Index] {
				return result, fmt.Errorf("duplicate invoice model update")
			}
			models[operation.Index], hasSet = true, true
		case "patch":
			if len(operation.Changes) == 0 {
				return result, fmt.Errorf("empty invoice field update")
			}
			hasPatch = true
			for _, change := range operation.Changes {
				if change.Source.Kind != "" && change.Source.Kind != request.Source.Kind || change.Source.ID != "" && change.Source.ID != request.Source.ID || change.Source.FileLoc != "" && change.Source.FileLoc != request.Source.FileLoc {
					return result, fmt.Errorf("invoice patch cannot span sources")
				}
				if change.Source.NodePath == "" || patched[change.Source.NodePath] {
					return result, fmt.Errorf("duplicate or missing invoice field path")
				}
				if !paths[change.Source.NodePath] {
					return result, fmt.Errorf("invoice patch cannot span candidates")
				}
				if change.Revision != "" && change.Revision != request.Digest {
					return result, fmt.Errorf("invoice field revision has changed")
				}
				patched[change.Source.NodePath] = true
			}
		case "bindings":
			if bindings[operation.Index] {
				return result, fmt.Errorf("duplicate invoice binding update")
			}
			bindings[operation.Index] = true
		default:
			return result, fmt.Errorf("unsupported invoice batch action")
		}
	}
	if hasSet && hasPatch {
		return result, fmt.Errorf("invoice batch cannot mix model and raw field updates")
	}
	var changes []ofdgo.InvoiceFieldChange
	for _, operation := range request.Operations {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		switch operation.Operation {
		case "set":
			data, err = ofdgo.UpdateInvoiceXML(ctx, data, operation.Index, operation.Invoice)
		case "patch":
			changes = append(changes, operation.Changes...)
		}
		if err != nil {
			return result, err
		}
	}
	if len(changes) != 0 {
		for i := range changes {
			changes[i].Revision = request.Digest
			changes[i].Source.Kind = ""
			changes[i].Source.ID = ""
			changes[i].Source.FileLoc = ""
		}
		data, err = ofdgo.PatchInvoice(ctx, data, changes)
		if err != nil {
			return result, err
		}
	}
	for _, operation := range request.Operations {
		if operation.Operation == "bindings" {
			if err := editor.SetInvoiceBindings(ctx, request.Source, operation.Index, operation.Bindings); err != nil {
				return result, err
			}
		}
	}
	return editor.UpdateInvoice(ctx, request.Source, request.Digest, data, request.Synchronize)
}

// resourceLocations 定位当前文档与版本中的资源引用实例
// 入参: args 资源类型、标识和引用索引
// 返回: any 页面图元范围, error 引用或度量错误
func (e *businessEngine) resourceLocations(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 3 {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return e.currentSession.Renderer.ResourceLocations(e.operationContext(), args[0].String(), args[1].String(), args[2].Int())
}

// fontPreview 使用当前文档和版本的字形轮廓生成独立预览
// 入参: args 字体资源标识及可选字形映射标记
// 返回: any SVG数据, error 字体或绘制错误
func (e *businessEngine) fontPreview(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 1 {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	var output bytes.Buffer
	if err := e.currentSession.Renderer.RenderFontPreview(e.operationContext(), args[0].String(), &output); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(output.Bytes())}), nil
}

// changeResource 调用库层资源编辑，浏览器不改写资源声明和包路径
// 入参: args 操作、类型、标识、格式、数据、页码和图元标识
// 返回: any 编辑状态, error 资源操作错误
func (e *businessEngine) changeResource(args []platformValue) (any, error) {
	if len(args) != 7 {
		return nil, fmt.Errorf("invalid resource arguments")
	}
	return e.changeAtomicObjects(func() error {
		ctx := e.operationContext()
		action, kind, id := args[0].String(), args[1].String(), args[2].String()
		switch action {
		case "delete":
			return e.currentEditor.DeleteResource(ctx, kind, id)
		case "bind", "unbind":
			if kind == "Attachment" {
				return e.currentEditor.BindAttachment(ctx, id, args[5].Int(), args[6].String(), action == "bind")
			}
			if kind != "Audio" && kind != "Video" {
				return fmt.Errorf("resource does not support actions")
			}
			return e.currentEditor.BindMedia(ctx, id, args[5].Int(), args[6].String(), action == "bind")
		case "add", "replace":
			data, err := e.bytesFromJS(args[4])
			if err != nil {
				return err
			}
			if action == "add" {
				_, err = e.currentEditor.ImportMedia(ctx, kind, args[3].String(), data)
				return err
			}
			return e.currentEditor.ReplaceResource(ctx, kind, id, args[3].String(), data)
		default:
			return fmt.Errorf("unsupported resource action")
		}
	})
}

// exportResource 分块导出当前资源的原始文件，不接收任意包路径
// 入参: args 类型及标识的JSON数组、分块接收函数
// 返回: any 输出格式, error 读取或写入错误
func (e *businessEngine) exportResource(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 2 {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	var key [2]string
	if err := json.Unmarshal([]byte(args[0].String()), &key); err != nil {
		return nil, err
	}
	stream, err := e.currentSession.Reader.OpenResource(e.operationContext(), key[0], key[1])
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	writer := bufio.NewWriterSize(exportWriter{write: args[1]}, 1<<20)
	if _, err := io.Copy(writer, stream); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"mime": "application/octet-stream"}), nil
}

// svgFontData 读取SVG字体资源
// 入参: args 浏览器参数
// 返回: any 字体数据, error 错误信息
func (e *businessEngine) svgFontData(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	data, err := e.currentSession.SVGFontData(args[0].String())
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(data)}), nil
}

// renderPage 渲染OFD页面
// 入参: args 页面索引、后端组合、DPI、光栅开关、已持有字体和图片、仅预览开关
// 返回: any 页面SVG结果, error 错误信息
func (e *businessEngine) renderPage(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("missing page index")
	}
	backend, dpi := "canvas", 150.0
	if len(args) > 1 {
		backend = args[1].String()
	}
	if len(args) > 2 {
		dpi = args[2].Float()
	}
	raster := len(args) > 3 && args[3].Bool()
	preview := len(args) > 6 && args[6].Bool()
	known := make(map[string]bool)
	for i := 4; i < min(len(args), 6); i++ {
		for j := 0; j < args[i].Length(); j++ {
			known[args[i].Index(j).String()] = true
		}
	}
	if e.currentEditor != nil {
		page, err := e.currentEditor.Page(args[0].Int())
		if err != nil {
			return nil, err
		}
		if err := e.syncEditorPageIDs(args[0].Int(), page); err != nil {
			return nil, err
		}
	}
	page, err := e.currentSession.renderPage(args[0].Int(), backend, dpi, raster, preview, known)
	if err != nil {
		return nil, err
	}
	if e.currentEditor != nil && !reflect.DeepEqual(e.currentEditor.Backends(), e.currentSession.Renderer.Backends()) {
		e.currentEditor.SetRenderBackends(e.currentSession.Renderer.Backends())
	}
	text := &ofdgo.PageText{}
	if !preview {
		text, err = e.currentSession.PageText(args[0].Int())
		if err != nil {
			return nil, err
		}
	}
	textData, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	var objects []any
	if !preview {
		objects, err = e.editorObjects(args[0].Int(), text)
		if err != nil {
			return nil, err
		}
	}
	links, err := e.linkResults(page.Links)
	if err != nil {
		return nil, err
	}
	fonts := make([]any, len(page.Fonts))
	for i, font := range page.Fonts {
		fonts[i] = map[string]any{"name": font.Name, "weight": font.Weight, "style": font.Style}
		if len(args) > 4 && !known[font.Name] {
			fonts[i].(map[string]any)["data"] = bytesToJS(e.currentSession.svgFonts[font.Name])
		}
	}
	annotations, err := json.Marshal(page.Annotations)
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{
		"previewOnly": preview,
		"annotations": string(annotations),
		"index":       page.Index,
		"number":      page.Number,
		"id":          page.ID,
		"width":       page.Width,
		"height":      page.Height,
		"svg":         page.SVG,
		"links":       links,
		"fonts":       fonts,
		"images":      e.svgImagesToJS(page.Images),
		"text":        string(textData),
		"objects":     objects,
	}), nil
}

// openLinks 获取文档或页面打开动作，不执行动作
// 入参: args 可选页面索引，省略时读取文档打开动作
// 返回: any 动作目标, error 文档或页码错误
func (e *businessEngine) openLinks(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	var links []ofdgo.PageLink
	var err error
	if len(args) == 0 {
		links, err = e.currentSession.Renderer.DocumentOpenLinks()
	} else if len(args) == 1 {
		page, readErr := e.currentSession.Reader.PageContentByIndex(args[0].Int())
		if readErr != nil {
			return nil, readErr
		}
		links, err = e.currentSession.Renderer.PageOpenLinks(page)
	} else {
		return nil, fmt.Errorf("unexpected open action arguments")
	}
	if err != nil {
		return nil, err
	}
	return e.linkResults(links)
}

// outlineLinks 获取目录节点的有序动作目标
// 入参: args 各级目录索引
// 返回: any 动作目标, error 目录或文档错误
func (e *businessEngine) outlineLinks(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("missing outline path")
	}
	links, err := e.currentSession.Renderer.OutlineLinks(e.indexesFromJS(args[0]))
	if err != nil {
		return nil, err
	}
	return e.linkResults(links)
}

// changeStructure 提交标引操作，XML、目标校验及历史维护均由库完成
// 入参: args JSON操作参数
// 返回: any 编辑状态, error 参数、校验或提交错误
func (e *businessEngine) changeStructure(args []platformValue) (any, error) {
	if len(args) != 1 || args[0].Type() != platformTypeString {
		return nil, fmt.Errorf("invalid structure arguments")
	}
	var request struct {
		Action     string                     `json:"action"`
		Index      int                        `json:"index"`
		Path       []int                      `json:"path"`
		TypeID     string                     `json:"typeId"`
		Node       ofdgo.StructureNode        `json:"node"`
		References []ofdgo.StructureReference `json:"references"`
	}
	if err := json.Unmarshal([]byte(args[0].String()), &request); err != nil {
		return nil, err
	}
	return e.changeObjects(func() error {
		if request.Action != "create" {
			structures, err := e.currentEditor.Structure()
			if err != nil {
				return err
			}
			if request.Index >= 0 && request.Index < len(structures) && structures[request.Index].Internal {
				return fmt.Errorf("internal text metadata must be edited through the text editor")
			}
		}
		switch request.Action {
		case "create":
			_, err := e.currentEditor.AddStructure(ofdgo.CustomTag{TypeID: request.TypeID, NameSpace: request.Node.Namespace}, request.Node)
			return err
		case "add":
			_, err := e.currentEditor.AddStructureNode(request.Index, request.Path, request.Node)
			return err
		case "update":
			return e.currentEditor.UpdateStructureNode(request.Index, request.Path, request.Node)
		case "delete":
			if len(request.Path) == 0 {
				return e.currentEditor.DeleteStructure(request.Index)
			}
			return e.currentEditor.DeleteStructureNode(request.Index, request.Path)
		case "bind":
			return e.currentEditor.SetStructureReferences(request.Index, request.Path, request.References)
		default:
			return fmt.Errorf("unsupported structure action %q", request.Action)
		}
	})
}

// structureObjects 获取当前编辑文档的绑定候选，不允许阅读模式调用编辑入口
// 入参: args 从0开始的页面索引
// 返回: any 图元标识和原文, error 参数或读取错误
func (e *businessEngine) structureObjects(args []platformValue) (any, error) {
	if e.currentEditor == nil || e.currentSession == nil || len(args) != 1 {
		return nil, fmt.Errorf("no document is being edited")
	}
	return e.currentSession.Renderer.StructureObjects(args[0].Int())
}

// structureTargets 查询当前文档标引节点的原文与定位范围
// 入参: args 标引清单索引和节点路径
// 返回: any 标引目标, error 参数或解析错误
func (e *businessEngine) structureTargets(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) != 2 {
		return nil, fmt.Errorf("missing structure path")
	}
	return e.currentSession.Renderer.StructureTargets(args[0].Int(), e.indexesFromJS(args[1]))
}

// linkResults 将库层动作目标转换为浏览器数据
// 入参: source 动作目标
// 返回: []any 浏览器动作数据, error 附件读取错误
func (e *businessEngine) linkResults(source []ofdgo.PageLink) ([]any, error) {
	links := make([]any, len(source))
	attachmentNames := make(map[string]string)
	for _, link := range source {
		if link.Attachment == "" {
			continue
		}
		attachments, err := e.currentSession.Reader.Attachments()
		if err != nil {
			return nil, err
		}
		for _, attachment := range attachments {
			attachmentNames[attachment.ID] = attachment.FileName()
		}
		break
	}
	for i, link := range source {
		item := map[string]any{
			"uri":    link.URI,
			"target": link.Target,
			"path":   link.Path,
			"group":  link.Group,
			"x":      link.Box.X,
			"y":      link.Box.Y,
			"width":  link.Box.W,
			"height": link.Box.H,
		}
		if link.Attachment != "" {
			item["attachment"] = link.Attachment
			item["fileName"] = attachmentNames[link.Attachment]
		}
		if sound := link.Sound; sound != nil {
			volume := 100
			if sound.Volume != nil {
				volume = *sound.Volume
			}
			item["sound"] = map[string]any{"resourceID": sound.ResourceID, "volume": volume, "repeat": sound.Repeat, "synchronous": sound.Synchronous}
		}
		if movie := link.Movie; movie != nil {
			item["movie"] = map[string]any{"resourceID": movie.ResourceID, "operator": movie.Operator}
			if box := link.PlaybackBox; box != nil {
				item["playbackBox"] = map[string]any{"x": box.X, "y": box.Y, "width": box.W, "height": box.H}
			}
		}
		if dest := link.Dest; dest != nil {
			item["dest"] = map[string]any{"type": dest.Type, "pageID": e.resolvedPageID(dest.PageID), "left": dest.Left, "top": dest.Top,
				"right": dest.Right, "bottom": dest.Bottom, "zoom": dest.Zoom,
				"omitLeft": dest.OmitLeft, "omitTop": dest.OmitTop, "omitZoom": dest.OmitZoom}
		}
		links[i] = item
	}
	return links, nil
}

// resolvedPageID 将跳转目标转换为界面页面列表中的标识，未找到时保留原值
// 入参: id 目标页面标识
// 返回: string 页面列表标识或原值
func (e *businessEngine) resolvedPageID(id string) string {
	if e.currentSession != nil {
		if index := e.currentSession.doc.PageIndex(id); index >= 0 {
			return e.currentSession.doc.Pages.Page[index].ID
		}
	}
	return id
}

// pageTextString 提取OFD页面原文
// 入参: args 页面索引
// 返回: any 页面原文, error 错误信息
func (e *businessEngine) pageTextString(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return e.currentSession.PageTextString(args[0].Int())
}

// searchPage 搜索OFD页面文字
// 入参: args 浏览器参数
// 返回: any 搜索结果, error 错误信息
func (e *businessEngine) searchPage(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	limit := 10001
	if len(args) > 2 {
		limit = max(1, min(limit, args[2].Int()))
	}
	return e.currentSession.SearchPageContext(e.operationContext(), args[0].Int(), args[1].String(), limit)
}

// exportFormats 获取导出格式
// 入参: args 浏览器参数
// 返回: any 导出格式, error 错误信息
func (e *businessEngine) exportFormats(args []platformValue) (any, error) {
	return ExportFormats(), nil
}

// parsePageRange 解析当前文档的导出页码
// 入参: args 浏览器参数
// 返回: any 页面索引, error 错误信息
func (e *businessEngine) parsePageRange(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	count, err := e.currentSession.Reader.PageCount()
	if err != nil {
		return nil, err
	}
	return ofdgo.ParsePageRange(args[0].String(), count)
}

// exportPage 导出OFD单页
// 入参: args 浏览器参数
// 返回: any 导出结果, error 错误信息
func (e *businessEngine) exportPage(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) < 4 {
		return nil, fmt.Errorf("missing export page arguments")
	}
	session, writerIndex, err := e.exportSession(args)
	if err != nil {
		return nil, err
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[writerIndex]}, 1<<20)
	format, err := session.ExportPage(args[0].Int(), args[1].String(), args[2].Float(), writer)
	if err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{
		"label": format.Label,
		"mime":  format.MIME,
	}), nil
}

// exportDocument 导出OFD文档
// 入参: args 浏览器参数
// 返回: any 导出结果, error 错误信息
func (e *businessEngine) exportDocument(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	session, writerIndex, err := e.exportSession(args)
	if err != nil {
		return nil, err
	}
	renderer := session.Renderer
	renderer.OnExportProgress = func(completed, total int) error {
		return awaitExport(args[writerIndex+1], completed, total)
	}
	defer func() { renderer.OnExportProgress = nil }()
	var indices []int
	if !args[2].IsNull() {
		indices = make([]int, args[2].Length())
		for i := range indices {
			indices[i] = args[2].Index(i).Int()
		}
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[writerIndex]}, 1<<20)
	format, err := session.ExportDocument(args[0].String(), args[1].Float(), writer, indices...)
	if err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{
		"label": format.Label,
		"mime":  format.MIME,
	}), nil
}

// exportSession 创建独立导出配置，写入回调前可指定光栅后端
// 入参: args 浏览器参数
// 返回: *Session 导出会话, int 写入回调位置, error 错误信息
func (e *businessEngine) exportSession(args []platformValue) (*Session, int, error) {
	session := *e.currentSession
	renderer := *session.Renderer
	session.Renderer = &renderer
	writerIndex := 3
	if args[3].Type() == platformTypeString {
		if err := session.SetRenderBackend(args[3].String()); err != nil {
			return nil, 0, err
		}
		writerIndex++
	}
	if args[writerIndex].Type() == platformTypeObject {
		if err := e.compressionFromJS(args[writerIndex], &renderer.Compression); err != nil {
			return nil, 0, err
		}
		writerIndex++
		if len(args) > writerIndex+1 {
			renderer.OutputContext = e.newOutputContext(args[len(args)-1])
		}
	}
	return &session, writerIndex, nil
}

// matchFontFiles 匹配文档所需的字体文件
// 入参: args 浏览器参数
// 返回: any 匹配的字体文件名称, error 错误信息
func (e *businessEngine) matchFontFiles(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	ids := make([]string, 0, len(e.currentSession.fontInfos))
	for _, info := range e.currentSession.fontInfos {
		if info.Used > 0 {
			ids = append(ids, info.ID)
		}
	}
	return e.currentSession.Reader.MatchFontFiles(e.stringsFromJS(args[0]), ids...)
}

// fontFaces 读取上传字体的名称和集合索引
// 入参: args 字体数据
// 返回: any 字体列表, error 错误信息
func (e *businessEngine) fontFaces(args []platformValue) (any, error) {
	data, err := e.bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	return (ofdgo.FontFile{Data: data}).LocalizedFaces(3, 0x0804)
}

// fontMenu 返回按来源隔离的字体族索引及所需款式候选
// 入参: args 字体信息、来源及可选目标款式的JSON
// 返回: any 分组和款式索引, error 参数错误
func (e *businessEngine) fontMenu(args []platformValue) (any, error) {
	var request struct {
		Faces   []ofdgo.FontFace `json:"faces"`
		Sources []string         `json:"sources"`
		Bold    bool             `json:"bold"`
		Italic  bool             `json:"italic"`
	}
	if err := json.Unmarshal([]byte(args[0].String()), &request); err != nil {
		return nil, err
	}
	return map[string]any{"groups": ofdgo.FontFamilyIndexes(request.Faces, request.Sources), "index": ofdgo.FontStyleIndex(request.Faces, request.Bold, request.Italic)}, nil
}

// fontPreferenceOrder 按字体族偏好排列编辑字体候选索引
// 入参: args 候选字体和字体族别名组的JSON数据
// 返回: any 候选索引列表, error 错误信息
func (e *businessEngine) fontPreferenceOrder(args []platformValue) (any, error) {
	var request struct {
		Faces    []ofdgo.FontFace `json:"faces"`
		Families [][]string       `json:"families"`
	}
	if err := json.Unmarshal([]byte(args[0].String()), &request); err != nil {
		return nil, err
	}
	return ofdgo.FontPreferenceOrder(request.Faces, request.Families), nil
}

// fontFace 提取编辑时选定的字体，供输入预览和保存共用
// 入参: args 字体数据和零起始索引
// 返回: any 独立字体数据, error 错误信息
func (e *businessEngine) fontFace(args []platformValue) (any, error) {
	data, err := e.bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	data, err = (ofdgo.FontFile{Data: data}).Face(args[1].Int())
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(data)}), nil
}

// fontsFromJS 从浏览器值读取字体文件
// 入参: value 浏览器值
// 返回: []FontFile 字体文件, error 错误信息
func (e *businessEngine) fontsFromJS(value platformValue) ([]FontFile, error) {
	if value.IsUndefined() || value.IsNull() {
		return nil, nil
	}
	length := value.Get("length").Int()
	fonts := make([]FontFile, 0, length)
	for i := 0; i < length; i++ {
		item := value.Index(i)
		data, err := e.bytesFromJS(item.Get("data"))
		if err != nil {
			return nil, err
		}
		fonts = append(fonts, FontFile{Name: item.Get("name").String(), Data: data})
	}
	return fonts, nil
}

// stringsFromJS 从浏览器值读取字符串列表
// 入参: value 浏览器值
// 返回: []string 字符串列表
func (e *businessEngine) stringsFromJS(value platformValue) []string {
	items := make([]string, value.Length())
	for i := range items {
		items[i] = value.Index(i).String()
	}
	return items
}

// encodeResult 编码浏览器接口返回结果
// 入参: result 返回结果
// 返回: string JSON字符串
func (e *businessEngine) encodeResult(result apiResult) string {
	data, err := json.Marshal(result)
	if err != nil {
		fallback, _ := json.Marshal(apiResult{OK: false, Error: err.Error()})
		return string(fallback)
	}
	return string(data)
}

// editorSummary 获取当前编辑文档状态
// 返回: editorInfo 文档信息
func (e *businessEngine) editorSummary() editorInfo {
	info := editorInfo{DocumentInfo: e.currentSession.Summary(), Revision: e.currentEditor.Revision(), CanUndo: e.currentEditor.CanUndo(), CanRedo: e.currentEditor.CanRedo(), Changes: e.currentEditor.Changes()}
	if !e.currentSession.doc.Permissions.Edit {
		info.EditWarnings = append(info.EditWarnings, "原文件声明不允许编辑")
	}
	if slices.ContainsFunc(e.currentSession.Reader.OFD.DocBody, func(body ofdgo.DocBody) bool { return body.Signatures != "" }) {
		info.EditWarnings = append(info.EditWarnings, "修改文档可能使原签名失效")
	}
	info.PageCapabilities = make([]uint, e.currentEditor.PageCount())
	for i := range info.PageCapabilities {
		capability, _ := e.currentEditor.PageCapabilities(i)
		info.PageCapabilities[i] = e.pageCapabilityMask(capability)
	}
	return info
}

// pageCapabilityMask 按插入、复制、删除、移动和尺寸的固定顺序编码页面能力，避免重复传输字段名
// 入参: capability 页面能力
// 返回: uint 能力位标记
func (e *businessEngine) pageCapabilityMask(capability ofdgo.PageCapabilities) uint {
	var mask uint
	for i, enabled := range [...]bool{capability.Insert, capability.Copy, capability.Delete, capability.Move, capability.Resize} {
		if enabled {
			mask |= 1 << i
		}
	}
	return mask
}

// restoreEditor 撤销或重做并更新预览
// 入参: redo 是否重做
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) restoreEditor(redo bool) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	apply := e.currentEditor.Undo
	if redo {
		apply = e.currentEditor.Redo
	}
	if !apply() {
		return e.editorSummary(), nil
	}
	return e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
}

// editorObjects 提供编辑画布的对象范围、文字排版及原始图片边界
// 入参: index 页面索引, text 页面文字
// 返回: []any 对象区域, error 错误信息
func (e *businessEngine) editorObjects(index int, text *ofdgo.PageText) ([]any, error) {
	objects := []any{}
	if e.currentEditor == nil {
		return objects, nil
	}
	page, err := e.currentEditor.Page(index)
	if err != nil {
		return nil, err
	}
	info, err := e.currentEditor.PageObjectInfo(index)
	if err != nil {
		return nil, err
	}
	textBoxes := make(map[string]ofdgo.Box)
	for _, run := range text.Runs {
		for _, box := range run.Boxes {
			if box.W <= 0 || box.H <= 0 {
				continue
			}
			prev, exists := textBoxes[run.ID]
			if !exists {
				textBoxes[run.ID] = box
				continue
			}
			x, y := math.Min(prev.X, box.X), math.Min(prev.Y, box.Y)
			textBoxes[run.ID] = ofdgo.Box{X: x, Y: y, W: math.Max(prev.X+prev.W, box.X+box.W) - x, H: math.Max(prev.Y+prev.H, box.Y+box.H) - y}
		}
	}
	fonts, err := e.currentSession.Reader.Fonts()
	if err != nil {
		return nil, err
	}
	fontNames := make(map[string]string, len(fonts))
	for _, font := range fonts {
		fontNames[font.ID] = font.FontName
	}
	for _, layer := range page.Content.Layer {
		for order, object := range layer.Objects {
			var id string
			switch object.Type {
			case "TextObject":
				id = object.TextObject.ID
			case "ImageObject":
				id = object.ImageObject.ID
			case "PathObject":
				id = object.PathObject.ID
			case "CompositeObject", "CompositeGraphicUnit":
				id = object.CompositeGraphicUnit.ID
			default:
				continue
			}
			capability := info[id].Capabilities
			var box ofdgo.Box
			var contours []ofdgo.ObjectContour
			switch object.Type {
			case "TextObject":
				box = textBoxes[id]
			default:
				box, contours, err = e.currentSession.Renderer.ObjectGeometry(object, layer.DrawParam)
			}
			if err != nil {
				continue
			}
			if box.W > 0 && box.H > 0 {
				position := info[id].Position
				item := map[string]any{"id": id, "type": object.Type, "x": box.X, "y": box.Y, "width": box.W, "height": box.H, "order": order, "position": position.Index, "count": position.Count, "container": position.Container,
					"capabilities": e.editorCapabilities(capability, object.Type)}
				e.editorAppearance(item, object, capability.Paint, e.editorPathScale(object.PathObject))
				if object.Type == "PathObject" {
					path := object.PathObject
					kind, geometry := path.Shape()
					if kind != "" && capability.Update {
						item["shape"] = string(kind)
						item["geometry"] = map[string]any{"x": geometry.X, "y": geometry.Y, "width": geometry.W, "height": geometry.H}
					} else if outline, err := e.currentSession.Renderer.PathOutline(path); err == nil {
						item["outline"] = outline
					}
					if kind == "" && capability.Update {
						if kind, box, matrix := path.ShapeFrame(); kind != "" {
							item["oriented"] = map[string]any{"kind": string(kind), "box": e.editorBox(box), "matrix": e.editorMatrix(matrix)}
						}
					}
				}
				if object.Type == "PathObject" || object.Type == "ImageObject" || object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
					paths := make([]any, len(contours))
					for i, contour := range contours {
						paths[i] = map[string]any{"path": contour.Path, "evenOdd": contour.EvenOdd}
					}
					item["contours"] = paths
				}
				if object.Type == "ImageObject" && capability.Update {
					full, err := object.ImageObject.ImageBounds()
					if err != nil {
						return nil, err
					}
					item["imageBounds"] = e.editorBox(full)
				}
				if object.Type == "ImageObject" {
					item["imageResource"] = object.ImageObject.ResourceID
				}
				if object.Type == "TextObject" {
					item["bounds"] = e.editorBox(box)
					value, layout := object.TextObject.TextLayout()
					item["text"], item["font"], item["fontName"], item["size"] = value, object.TextObject.Font, fontNames[object.TextObject.Font], object.TextObject.Size
					item["wrap"], item["align"], item["paragraphHeight"] = layout.Wrap, layout.Align, layout.LineHeight
					item["letterSpacing"] = layout.LetterSpacing
					item["leftIndent"], item["rightIndent"], item["firstLineIndent"] = layout.LeftIndent, layout.RightIndent, layout.FirstLineIndent
					if capability.Reflow {
						frame, err := object.TextObject.TextFrame()
						if err != nil {
							return nil, err
						}
						boundary, _ := ofdgo.ParseBox(object.TextObject.Boundary)
						matrix := ofdgo.TranslationMatrix(boundary.X, boundary.Y).Multiply(ofdgo.NewMatrix(object.TextObject.CTM))
						inverse, _ := matrix.Invert()
						local := inverse.TransformBox(box)
						width := math.Max(object.TextObject.Size, local.X+local.W)
						if capability.LayoutKnown {
							item["paragraphWidth"] = frame.W
							width = frame.W
						}
						frame = ofdgo.Box{W: width, H: math.Max(object.TextObject.Size, local.Y+local.H)}
						item["textFrame"] = map[string]any{"width": frame.W, "height": frame.H, "matrix": e.editorMatrix(matrix)}
						if capability.LayoutKnown {
							bounds := matrix.TransformBox(frame)
							item["x"], item["y"], item["width"], item["height"] = bounds.X, bounds.Y, bounds.W, bounds.H
						}
					}
					if codes := object.TextObject.TextCode; len(codes) > 1 {
						first, _ := strconv.ParseFloat(codes[0].Y, 64)
						second, _ := strconv.ParseFloat(codes[1].Y, 64)
						item["lineHeight"] = second - first
					}
				}
				if object.Type == "CompositeObject" {
					if value, richErr := e.currentEditor.RichText(index, id); richErr == nil {
						if local, matrix, err := e.currentEditor.RichTextFrame(index, id); err == nil {
							e.editorRichText(item, object, value, fontNames, local, matrix)
						}
					}
				}
				objects = append(objects, item)
			}
		}
	}
	for _, annotation := range e.currentSession.Reader.Annots[page.ID] {
		if annotation.Visible != nil && !*annotation.Visible {
			continue
		}
		box, contours, err := e.currentSession.Renderer.AnnotationGeometry(annotation)
		capabilities := map[string]any{"update": true, "transform": true, "arrange": true, "copy": true, "delete": true, "children": len(annotation.Appearance.Objects) != 0}
		if err != nil {
			capabilities["transform"], capabilities["arrange"], capabilities["children"] = false, false, false
			capabilities["reason"], capabilities["reasonCode"] = err.Error(), "invalidObject"
		}
		if box.W <= 0 || box.H <= 0 {
			box, _ = ofdgo.ParseBox(annotation.Appearance.Boundary)
		}
		paths := make([]any, len(contours))
		for i, contour := range contours {
			paths[i] = map[string]any{"path": contour.Path, "evenOdd": contour.EvenOdd}
		}
		alpha, mixed := 255, false
		for i, object := range annotation.Appearance.Objects {
			var value *int
			switch object.Type {
			case "TextObject":
				value = object.TextObject.Alpha
			case "PathObject":
				value = object.PathObject.Alpha
			case "ImageObject":
				value = object.ImageObject.Alpha
			case "CompositeObject", "CompositeGraphicUnit":
				value = object.CompositeGraphicUnit.Alpha
			}
			current := 255
			if value != nil {
				current = *value
			}
			if i == 0 {
				alpha = current
			} else if current != alpha {
				mixed = true
			}
		}
		item := map[string]any{"id": "annotation:" + annotation.ID, "type": "Annotation", "annotationType": annotation.Type,
			"x": box.X, "y": box.Y, "width": box.W, "height": box.H, "remark": annotation.Remark, "creator": annotation.Creator,
			"alpha": alpha, "alphaMixed": mixed, "capabilities": capabilities}
		if len(annotation.Appearance.Objects) == 0 && strings.TrimSpace(annotation.Remark) != "" {
			item["note"] = true
		} else {
			item["contours"] = paths
		}
		objects = append(objects, item)
	}
	return objects, nil
}

// changeAnnotations 修改注解说明、平移或删除同页注解，复用会话历史与预览
// 入参: args 页面索引、注解标识数组、操作及参数
// 返回: any 编辑状态, error 错误信息
func (e *businessEngine) changeAnnotations(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		ids := e.stringsFromJS(args[1])
		for i := range ids {
			ids[i] = strings.TrimPrefix(ids[i], "annotation:")
		}
		switch args[2].String() {
		case "update":
			if len(ids) != 1 {
				return fmt.Errorf("select one annotation to update")
			}
			return e.currentEditor.UpdateAnnotation(args[0].Int(), ids[0], args[3].String(), args[4].String())
		case "move":
			return e.currentEditor.MoveAnnotations(args[0].Int(), ids, args[3].Float(), args[4].Float())
		case "delete":
			return e.currentEditor.DeleteAnnotations(args[0].Int(), ids)
		default:
			return fmt.Errorf("unsupported annotation action %q", args[2].String())
		}
	})
}

// readAnnotation 获取编辑面板所需的注解内容，不修改文件
// 入参: args 页面索引与注解标识
// 返回: any 面板数据, error 错误信息
func (e *businessEngine) readAnnotation(args []platformValue) (any, error) {
	annotation, err := e.currentEditor.Annotation(args[0].Int(), strings.TrimPrefix(args[1].String(), "annotation:"))
	if err != nil {
		return nil, err
	}
	texts, err := e.currentEditor.AnnotationTexts(args[0].Int(), annotation.ID)
	if err != nil {
		return nil, err
	}
	info := map[string]any{"texts": texts, "remark": annotation.Remark, "creator": annotation.Creator}
	if annotation.Type == "Link" {
		info["linkKind"] = "keep"
		link, err := e.currentEditor.AnnotationLink(args[0].Int(), annotation.ID)
		if err == nil && link.URI != nil {
			info["linkKind"], info["linkURI"], info["linkBase"] = "uri", link.URI.URI, link.URI.Base
			info["linkTarget"] = link.URI.Target
		} else if err == nil && link.Goto != nil && link.Goto.Dest != nil {
			if index := e.currentSession.doc.PageIndex(link.Goto.Dest.PageID); index >= 0 {
				info["linkKind"], info["linkPage"] = "page", index+1
			}
		}
	}
	return info, nil
}

// writeAnnotation 原子创建多页注解或修改现有链接、重复文字
// 入参: args 当前页面、JSON选项、字体字节、图片字节
// 返回: any 文档信息及新选区, error 错误信息
func (e *businessEngine) writeAnnotation(args []platformValue) (any, error) {
	var options annotationOptions
	if err := json.Unmarshal([]byte(args[1].String()), &options); err != nil {
		return nil, err
	}
	if options.Kind == "link" && options.ID != "" && options.Keep {
		return e.editorSummary(), nil
	}
	page := args[0].Int()
	var ids []string
	result, err := e.changeObjects(func() error {
		font, resource := "", ""
		if !args[2].IsNull() {
			data, err := e.bytesFromJS(args[2])
			if err != nil {
				return err
			}
			font, err = e.currentEditor.AddFont(FontFile{Data: data}, 0)
			if err != nil {
				return err
			}
		}
		if !args[3].IsNull() {
			data, err := e.bytesFromJS(args[3])
			if err != nil {
				return err
			}
			resource, err = e.currentEditor.AddImage(data)
			if err != nil {
				return err
			}
			size, err := e.currentEditor.ImageSize(resource)
			if err != nil {
				return err
			}
			options.Height = options.Width * float64(size.Y) / float64(size.X)
		}
		if options.Kind == "replace" {
			style := ofdgo.TextStyle{Font: font}
			_, err := e.currentEditor.ReplaceAnnotationText(page, options.ID, options.Old, options.Text, style)
			return err
		}
		target := ofdgo.AnnotationLink{URI: &ofdgo.URI{URI: options.URI, Base: options.Base}}
		if options.Target > 0 {
			if options.Target > e.currentEditor.PageCount() {
				return fmt.Errorf("destination page is out of range")
			}
			page, err := e.currentEditor.Page(options.Target - 1)
			if err != nil {
				return err
			}
			target = ofdgo.AnnotationLink{Dest: &ofdgo.Dest{Type: "Fit", PageID: page.ID}}
		}
		if options.Kind == "link" && options.ID != "" {
			if target.URI != nil {
				previous, err := e.currentEditor.AnnotationLink(page, options.ID)
				if err != nil {
					return err
				}
				if previous.URI != nil {
					target.URI.Target = previous.URI.Target
				}
			}
			return e.currentEditor.UpdateAnnotationLink(page, options.ID, target)
		}
		if options.Width <= 0 || options.Height <= 0 || options.Alpha < 0 || options.Alpha > 255 {
			return fmt.Errorf("invalid annotation dimensions or opacity")
		}
		pages, err := ofdgo.ParsePageRange(options.Pages, e.currentEditor.PageCount())
		if err != nil {
			return err
		}
		if options.Kind == "watermark" {
			boundary := fmt.Sprintf("0 0 %g %g", options.Width, options.Height)
			var object ofdgo.GraphicObject
			if resource != "" {
				object = ofdgo.GraphicObject{Type: "ImageObject", ImageObject: ofdgo.ImageObject{ResourceID: resource, Boundary: boundary, Alpha: &options.Alpha}}
			} else {
				text := ofdgo.TextObject{Font: font, Size: options.Size, Boundary: boundary, Alpha: &options.Alpha}
				if err := e.currentEditor.LayoutText(&text, options.Text, ofdgo.TextLayout{Align: "center"}); err != nil {
					return err
				}
				if err := e.setTextColor(&text, options.Color); err != nil {
					return err
				}
				object = ofdgo.GraphicObject{Type: "TextObject", TextObject: text}
			}
			added, err := e.currentEditor.AddWatermarks(e.operationContext(), pages, object, ofdgo.WatermarkOptions{Creator: options.Creator, Angle: options.Angle, Tile: options.Tile, Area: options.Area})
			if err != nil {
				return err
			}
			for i, index := range pages {
				if index == page {
					ids = append(ids, "annotation:"+added[i])
				}
			}
			return nil
		}
		for _, index := range pages {
			box := ofdgo.Box{X: options.X, Y: options.Y, W: options.Width, H: options.Height}
			var id string
			if options.Kind == "link" {
				id, err = e.currentEditor.AddLinkAnnotation(index, box, target, options.Creator)
			} else {
				annotation := ofdgo.Annotation{Creator: options.Creator, Appearance: ofdgo.Appearance{Boundary: fmt.Sprintf("%g %g %g %g", box.X, box.Y, box.W, box.H)}}
				switch options.Kind {
				case "note":
					annotation.Type, annotation.Remark = "Path", options.Text
				case "stamp":
					annotation.Type = "Stamp"
					if resource == "" {
						return fmt.Errorf("stamp requires an image")
					}
					object := ofdgo.GraphicObject{Type: "ImageObject", ImageObject: ofdgo.ImageObject{ResourceID: resource, Boundary: fmt.Sprintf("0 0 %g %g", box.W, box.H), CTM: fmt.Sprintf("%g 0 0 %g 0 0", box.W, box.H), Alpha: &options.Alpha}}
					angle := options.Angle * math.Pi / 180
					matrix := ofdgo.NewMatrix(fmt.Sprintf("%g %g %g %g 0 0", math.Cos(angle), math.Sin(angle), -math.Sin(angle), math.Cos(angle)))
					matrix = ofdgo.TranslationMatrix(box.W/2, box.H/2).Multiply(matrix).Multiply(ofdgo.TranslationMatrix(-box.W/2, -box.H/2))
					object.ImageObject.CTM = matrix.Multiply(ofdgo.NewMatrix(object.ImageObject.CTM)).String()
					annotation.Appearance.Objects = []ofdgo.GraphicObject{object}
				default:
					return fmt.Errorf("unsupported annotation kind %q", options.Kind)
				}
				id, err = e.currentEditor.AddAnnotation(index, annotation)
			}
			if err != nil {
				return err
			}
			if index == page {
				ids = append(ids, "annotation:"+id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return editorSelectionInfo{editorInfo: result.(editorInfo), SelectedIDs: ids}, nil
}

// insertInk 将一笔采样写为一个标准矢量对象，复用内部插入与撤销逻辑
// 入参: args 页面、JSON采样、线宽、压感开关、颜色及可选父路径
// 返回: any 文档和选区, error 错误信息
func (e *businessEngine) insertInk(args []platformValue) (any, error) {
	var points []ofdgo.InkPoint
	if err := json.Unmarshal([]byte(args[1].String()), &points); err != nil {
		return nil, err
	}
	path, err := ofdgo.NewInk(points, args[2].Float(), args[3].Bool())
	if err != nil {
		return nil, err
	}
	if path.Fill != nil && *path.Fill {
		err = e.setEditorColor(path.FillColor, args[4].String())
	} else {
		err = e.setEditorColor((*ofdgo.FillColor)(path.StrokeColor), args[4].String())
	}
	if err != nil {
		return nil, err
	}
	return e.insertEditorObject(args[0].Int(), ofdgo.GraphicObject{Type: "PathObject", PathObject: path}, args[5:])
}

// splitEditorSelection 区分页面对象与注解标识，不以外观大小或类型猜测
// 入参: ids 画布选区
// 返回: []string 正文标识, []string 注解标识
func (e *businessEngine) splitEditorSelection(ids []string) ([]string, []string) {
	var objects, annotations []string
	for _, id := range ids {
		if value, ok := strings.CutPrefix(id, "annotation:"); ok {
			annotations = append(annotations, value)
		} else {
			objects = append(objects, id)
		}
	}
	return objects, annotations
}

// changeSelection 原子处理正文、注解及内部对象混选，保持统一历史
// 入参: args 页面索引、标识列表、操作与参数
// 返回: any 文档与新选区, error 错误信息
func (e *businessEngine) changeSelection(args []platformValue) (any, error) {
	var selected []string
	result, err := e.changeAtomicObjects(func() error {
		page, operation := args[0].Int(), args[2].String()
		paths, err := e.editorTargetPaths(e.stringsFromJS(args[1]))
		if err != nil {
			return err
		}
		switch operation {
		case "align", "distribute":
			return e.arrangeEditorSelection(page, e.stringsFromJS(args[1]), operation, args[3].String())
		case "delete":
			return e.currentEditor.DeleteTargets(page, paths)
		case "copy":
			clipboard, err := e.captureEditorSelection(page, e.stringsFromJS(args[1]))
			if err != nil {
				return err
			}
			selected, err = e.pasteEditorSelection(page, clipboard, args[3].Float(), args[4].Float())
			return err
		case "pasteStyle":
			if e.copiedStyle == nil {
				return fmt.Errorf("style clipboard is empty")
			}
			return e.currentEditor.CopyTargetsStyle(page, paths, *e.copiedStyle)
		case "paint", "textColor":
			style, err := e.editorPaintStyle(operation, args[3:])
			if err != nil {
				return err
			}
			return e.currentEditor.StyleTargets(page, paths, style)
		case "style":
			return e.currentEditor.StyleTargets(page, paths, e.objectStyle(args[3]))
		case "erase":
			return e.currentEditor.EraseTargets(page, paths, ofdgo.Box{
				X: args[3].Float(), Y: args[4].Float(), W: args[5].Float(), H: args[6].Float(),
			})
		case "erasePath":
			return e.currentEditor.EraseTargetsPath(page, paths, e.pointsFromJS(args[3]))
		case "rotate":
			return e.currentEditor.RotateTargets(page, paths, args[3].Int())
		case "flip":
			return e.currentEditor.FlipTargets(page, paths, args[3].String())
		case "resize":
			return e.currentEditor.ResizeTargets(page, paths, ofdgo.Box{
				X: args[3].Float(), Y: args[4].Float(), W: args[5].Float(), H: args[6].Float(),
			})
		case "transform":
			dx, dy, scale := args[3].Float(), args[4].Float(), args[5].Float()
			if scale <= 0 {
				return fmt.Errorf("scale must be positive")
			}
			matrix := ofdgo.NewMatrix(fmt.Sprintf("%g 0 0 %g %g %g", scale, scale, dx, dy))
			return e.currentEditor.TransformTargets(page, paths, matrix)
		default:
			return fmt.Errorf("unsupported mixed selection operation %q", operation)
		}
	})
	if err != nil || selected == nil {
		return result, err
	}
	return editorSelectionInfo{editorInfo: result.(editorInfo), SelectedIDs: selected}, nil
}

// arrangeEditorSelection 对跨容器选区统一对齐或分布，不改变各自绘制层级
// 入参: page 页面, ids 标识, operation 操作, direction 方向
// 返回: error 错误信息
func (e *businessEngine) arrangeEditorSelection(page int, ids []string, operation, direction string) error {
	paths, err := e.editorTargetPaths(ids)
	if err != nil {
		return err
	}
	return e.currentEditor.ArrangeTargets(page, paths, operation == "distribute", direction)
}

// editorTargetPaths 将画布标识解析为完整对象路径
// 入参: ids 画布标识
// 返回: []ofdgo.ObjectPath 对象路径, error 路径错误
func (e *businessEngine) editorTargetPaths(ids []string) ([]ofdgo.ObjectPath, error) {
	paths := make([]ofdgo.ObjectPath, len(ids))
	for i, id := range ids {
		path, err := e.compositePath(id)
		if err != nil {
			return nil, err
		}
		paths[i] = path
	}
	return paths, nil
}

// captureEditorSelection 捕获正文、注解及内部对象的独立副本
// 入参: page 页面索引, ids 画布标识
// 返回: *editorClipboard 快照, error 错误信息
func (e *businessEngine) captureEditorSelection(page int, ids []string) (*editorClipboard, error) {
	paths, err := e.editorTargetPaths(ids)
	if err != nil {
		return nil, err
	}
	selection, err := e.currentEditor.CaptureTargets(page, paths)
	return &editorClipboard{selection: selection}, err
}

// pasteEditorSelection 原子粘贴正文和注解，共享字体与图片数据
// 入参: page 目标页, clipboard 快照, dx 横向位移, dy 纵向位移
// 返回: []string 新画布标识, error 错误信息
func (e *businessEngine) pasteEditorSelection(page int, clipboard *editorClipboard, dx, dy float64) ([]string, error) {
	ids, annotations, err := e.currentEditor.PasteSelection(page, clipboard.selection, dx, dy)
	for _, id := range annotations {
		ids = append(ids, "annotation:"+id)
	}
	return ids, err
}

// editorCapabilities 统一顶层与内部对象的操作能力和受限原因
// 入参: capability 库层能力, kind 对象类型
// 返回: map[string]any 前端能力
func (e *businessEngine) editorCapabilities(capability ofdgo.ObjectCapabilities, kind string) map[string]any {
	result := map[string]any{"update": capability.Update, "paint": capability.Paint, "replaceFont": capability.ReplaceFont, "reflow": capability.Reflow, "layoutKnown": capability.LayoutKnown, "transform": capability.Transform, "arrange": capability.Arrange, "copy": capability.Copy, "delete": capability.Delete, "order": capability.Order, "reason": capability.Reason, "reasonCode": string(capability.ReasonCode),
		"replaceImage": capability.ReplaceImage, "cropImage": capability.CropImage, "fitImage": capability.FitImage, "resetCrop": capability.ResetCrop, "ungroup": capability.Ungroup, "stretch": capability.Stretch, "children": capability.Transform && (kind == "CompositeObject" || kind == "CompositeGraphicUnit")}
	result["textContent"] = capability.TextContent
	result["rewriteText"] = capability.RewriteText
	if missing := capability.MissingGlyphs; missing != nil {
		result["missingGlyphs"] = map[string]any{"fontID": missing.FontID, "characters": missing.Characters}
	}
	return result
}

// editorAppearance 提供对象自身透明度及有效绘制外观，不混入父对象透明度
// 入参: item 前端对象, object 有效样式, paint 是否可改色, scale 描边页面倍率
func (e *businessEngine) editorAppearance(item map[string]any, object ofdgo.GraphicObject, paint bool, scale float64) {
	links := 0
	for _, action := range object.Actions() {
		if action.Event == "CLICK" && (action.URI != nil || action.Goto != nil) {
			links++
			if action.Goto != nil && action.Goto.Dest != nil {
				gotoAction, dest := *action.Goto, *action.Goto.Dest
				dest.PageID = e.resolvedPageID(dest.PageID)
				gotoAction.Dest, action.Goto = &dest, &gotoAction
			}
			if data, err := json.Marshal(action); err == nil {
				item["link"] = string(data)
			}
		}
	}
	item["linkCount"] = links
	var alpha *int
	var fill, stroke *ofdgo.FillColor
	switch object.Type {
	case "TextObject":
		item["bold"], item["italic"] = object.TextObject.Weight >= 700, object.TextObject.Italic
		fill = object.TextObject.FillColor
		stroke = (*ofdgo.FillColor)(object.TextObject.StrokeColor)
		alpha = object.TextObject.Alpha
		if paint {
			item["color"] = e.editorColorHex(object.TextObject.FillColor)
		}
	case "ImageObject":
		alpha = object.ImageObject.Alpha
		item["imageBorder"] = object.ImageObject.Border != nil
		item["borderColor"] = "#000000"
		if border := object.ImageObject.Border; border != nil {
			item["borderColor"] = e.editorColorHex((*ofdgo.FillColor)(border.BorderColor))
		}
		if data, err := json.Marshal(object.ImageObject.Border); err == nil {
			item["borderStyle"] = string(data)
		}
	case "PathObject":
		path := object.PathObject
		fill = path.FillColor
		stroke = (*ofdgo.FillColor)(path.StrokeColor)
		if box, err := ofdgo.ParseBox(path.Boundary); err == nil {
			item["paintSize"] = []any{box.W, box.H}
		}
		alpha = path.Alpha
		item["dashPattern"], item["cap"], item["join"], item["dashOffset"] = path.DashPattern, path.Cap, path.Join, 0.0
		if path.DashOffset != nil {
			item["dashOffset"] = *path.DashOffset
		}
		if item["scoped"] == true {
			item["dashPattern"] = e.editorDashPattern(path.DashPattern, scale)
			if path.DashOffset != nil {
				item["dashOffset"] = *path.DashOffset * scale
			}
		}
		if paint {
			item["fill"], item["stroke"] = path.Fill != nil && *path.Fill, path.Stroke == nil || *path.Stroke
			item["fillColor"], item["strokeColor"] = e.editorColorHex(path.FillColor), e.editorColorHex((*ofdgo.FillColor)(path.StrokeColor))
			item["lineWidth"] = path.LineWidth * scale
		}
	case "CompositeObject", "CompositeGraphicUnit":
		alpha = object.CompositeGraphicUnit.Alpha
	}
	item["alpha"] = 255
	if alpha != nil {
		item["alpha"] = *alpha
	}
	for key, paint := range map[string]*ofdgo.FillColor{"fill": fill, "stroke": stroke} {
		if paint == nil {
			continue
		}
		if p := paint.Pattern; p != nil {
			item[key+"Pattern"] = map[string]any{"width": p.Width, "height": p.Height, "xStep": p.XStep, "yStep": p.YStep, "ctm": p.CTM}
		}
		var segments []ofdgo.ShdSegment
		if paint.AxialShd != nil {
			segments = paint.AxialShd.Segment
		} else if paint.RadialShd != nil {
			segments = paint.RadialShd.Segment
		}
		if len(segments) == 0 {
			continue
		}
		stops, err := e.currentEditor.GradientStops(segments, nil)
		if err != nil {
			continue
		}
		values := make([]any, len(stops))
		for i, stop := range stops {
			c := segments[i].Color
			values[i] = map[string]any{"position": stop.Offset * 100, "color": e.editorColorHex(&ofdgo.FillColor{Value: c.Value, Index: c.Index, ColorSpace: c.ColorSpace}), "transparency": (1 - float64(stop.Color.A)/255) * 100}
		}
		if data, err := json.Marshal(paint); err == nil {
			item[key+"Gradient"] = map[string]any{"paint": string(data), "stops": values}
		}
	}
}

// compositePath 解析画布复合对象路径
// 入参: value 顶层标识与逐层序号
// 返回: ofdgo.ObjectPath 库层路径, error 错误信息
func (e *businessEngine) compositePath(value string) (ofdgo.ObjectPath, error) {
	parts := strings.Split(value, "/")
	path := ofdgo.ObjectPath{ID: parts[0]}
	if id, ok := strings.CutPrefix(path.ID, "annotation:"); ok {
		path.ID, path.Annotation = "", id
	}
	for _, part := range parts[1:] {
		index, err := strconv.Atoi(part)
		if err != nil || index < 0 {
			return path, fmt.Errorf("invalid composite path")
		}
		path.Children = append(path.Children, index)
	}
	return path, nil
}

// compositeObjects 获取内部编辑范围，不重新渲染页面或传输图片
// 入参: args 页面索引与复合路径
// 返回: any 画布对象列表, error 错误信息
func (e *businessEngine) compositeObjects(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	key := args[1].String()
	path, err := e.compositePath(key)
	if err != nil {
		return nil, err
	}
	members, err := e.currentEditor.CompositeObjects(args[0].Int(), path)
	if err != nil {
		return nil, err
	}
	fonts, err := e.currentSession.Reader.Fonts()
	if err != nil {
		return nil, err
	}
	fontNames := make(map[string]string, len(fonts))
	for _, font := range fonts {
		fontNames[font.ID] = font.FontName
	}
	objects := make([]any, 0, len(members))
	for i, member := range members {
		box := member.Bounds
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		item := map[string]any{"id": fmt.Sprintf("%s/%d", key, i), "type": member.Object.Type, "x": box.X, "y": box.Y, "width": box.W, "height": box.H, "order": i, "position": member.Position.Index, "count": member.Position.Count, "container": key + ":" + member.Position.Container, "scoped": true, "parent": key, "contours": member.Contours,
			"capabilities": e.editorCapabilities(member.Capabilities, member.Object.Type)}
		e.editorAppearance(item, member.Object, member.Capabilities.Paint, member.StrokeScale)
		if member.Object.Type == "ImageObject" {
			item["imageResource"] = member.Object.ImageObject.ResourceID
		}
		if kind, geometry := member.Shape(); kind != "" {
			item["shape"], item["geometry"] = string(kind), e.editorBox(geometry)
		} else if kind, box, matrix := member.ShapeFrame(); kind != "" {
			item["oriented"] = map[string]any{"kind": string(kind), "box": e.editorBox(box), "matrix": e.editorMatrix(matrix)}
		}
		if member.Object.Type == "TextObject" {
			object := member.Object.TextObject
			value, layout := object.TextLayout()
			item["text"] = value
			item["wrap"], item["align"], item["paragraphHeight"] = layout.Wrap, layout.Align, layout.LineHeight
			item["letterSpacing"] = layout.LetterSpacing
			item["leftIndent"], item["rightIndent"], item["firstLineIndent"] = layout.LeftIndent, layout.RightIndent, layout.FirstLineIndent
			item["font"], item["fontName"], item["size"] = object.Font, fontNames[object.Font], object.Size
			item["bounds"] = e.editorBox(box)
			if member.Capabilities.Reflow {
				inverse, _ := member.Matrix.Invert()
				local := inverse.TransformBox(box)
				width := math.Max(object.Size, local.X+local.W)
				if member.Capabilities.LayoutKnown {
					frame, err := object.TextFrame()
					if err != nil {
						return nil, err
					}
					item["paragraphWidth"] = frame.W
					width = frame.W
				}
				frame := ofdgo.Box{W: width, H: math.Max(object.Size, local.Y+local.H)}
				item["textFrame"] = map[string]any{"width": frame.W, "height": frame.H, "matrix": e.editorMatrix(member.Matrix)}
				if member.Capabilities.LayoutKnown {
					bounds := member.Matrix.TransformBox(frame)
					item["x"], item["y"], item["width"], item["height"] = bounds.X, bounds.Y, bounds.W, bounds.H
				}
			}
		}
		if member.Capabilities.CropImage {
			item["imageBounds"] = e.editorBox(box)
			item["cropped"] = member.Cropped
		}
		if member.Object.Type == "CompositeObject" {
			if value, frame, matrix, err := e.currentEditor.CompositeRichText(args[0].Int(), member); err == nil {
				e.editorRichText(item, member.Object, value, fontNames, frame, matrix)
			}
		}
		objects = append(objects, item)
	}
	return objects, nil
}

// changeCompositeObjects 提交内部选区操作，结构变化后返回新的选区路径
// 入参: args 页面索引、父路径、成员路径列表、操作及参数
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) changeCompositeObjects(args []platformValue) (any, error) {
	var selected []int
	result, err := e.changeObjects(func() error {
		key := args[1].String()
		path, err := e.compositePath(key)
		if err != nil {
			return err
		}
		indexes, err := e.compositeIndexes(key, e.stringsFromJS(args[2]))
		if err != nil {
			return err
		}
		page := args[0].Int()
		operation := args[3].String()
		switch operation {
		case "group":
			if args[4].Bool() {
				if len(indexes) != 1 {
					return fmt.Errorf("ungrouping requires one member")
				}
				selected, err = e.currentEditor.UngroupCompositeObject(page, path, indexes[0])
			} else {
				var index int
				index, err = e.currentEditor.GroupCompositeObjects(page, path, indexes)
				selected = []int{index}
			}
			return err
		case "pasteStyle":
			if e.copiedStyle == nil {
				return fmt.Errorf("style clipboard is empty")
			}
			return e.currentEditor.CopyCompositeStyle(page, path, indexes, *e.copiedStyle)
		case "reshape", "line":
			if len(indexes) != 1 {
				return fmt.Errorf("shape editing requires one member")
			}
			box := ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()}
			if operation == "line" {
				return e.currentEditor.ReshapeCompositeLine(page, path, indexes[0], ofdgo.ShapeKind(args[8].String()), box)
			}
			if len(args) > 8 && args[8].Bool() {
				return e.currentEditor.ReshapeCompositeFrame(page, path, indexes[0], box)
			}
			return e.currentEditor.ReshapeCompositeObject(page, path, indexes[0], box)
		case "erase":
			return e.currentEditor.EraseCompositeObjects(page, path, indexes, ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()})
		case "erasePath":
			return e.currentEditor.EraseCompositeObjectsPath(page, path, indexes, e.pointsFromJS(args[4]))
		case "delete":
			return e.currentEditor.DeleteCompositeObjects(page, path, indexes)
		case "copy":
			selected, err = e.currentEditor.CopyCompositeObjects(page, path, indexes, args[4].Float(), args[5].Float())
			return err
		case "order":
			selected, err = e.currentEditor.OrderCompositeObjects(page, path, indexes, args[4].String())
			return err
		case "text", "textStyle":
			return e.changeCompositeText(page, path, indexes, operation, args[4:])
		case "layout":
			if len(indexes) != 1 {
				return fmt.Errorf("text layout requires one member")
			}
			path.Children = append(slices.Clone(path.Children), indexes[0])
			return e.layoutEditorText(page, path, args[4:])
		case "fit", "resetCrop":
			if len(indexes) != 1 {
				return fmt.Errorf("image operation requires one member")
			}
			if operation == "fit" {
				return e.currentEditor.FitCompositeImage(page, path, indexes[0], args[4].String())
			}
			return e.currentEditor.ResetCompositeImageCrop(page, path, indexes[0])
		case "crop":
			if len(indexes) != 1 {
				return fmt.Errorf("image crop requires one member")
			}
			return e.currentEditor.CropCompositeImage(page, path, indexes[0], ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()})
		case "image":
			if len(indexes) != 1 {
				return fmt.Errorf("image replacement requires one member")
			}
			data, err := e.bytesFromJS(args[4])
			if err != nil {
				return err
			}
			return e.currentEditor.ReplaceCompositeImage(page, path, indexes[0], data)
		case "transform":
			return e.currentEditor.TransformCompositeObjects(page, path, indexes, args[4].Float(), args[5].Float(), args[6].Float())
		case "rotate":
			return e.currentEditor.RotateCompositeObjects(page, path, indexes, args[4].Int())
		case "flip":
			return e.currentEditor.FlipCompositeObjects(page, path, indexes, args[4].String())
		case "resize":
			return e.currentEditor.ResizeCompositeObjects(page, path, indexes, ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()})
		case "align":
			return e.currentEditor.AlignCompositeObjects(page, path, indexes, args[4].String())
		case "distribute":
			return e.currentEditor.DistributeCompositeObjects(page, path, indexes, args[4].String())
		case "style":
			return e.currentEditor.StyleCompositeObjects(page, path, indexes, e.objectStyle(args[4]))
		case "paint", "textColor":
			style, err := e.editorPaintStyle(operation, args[4:])
			if err != nil {
				return err
			}
			return e.currentEditor.StyleCompositeObjects(page, path, indexes, style)
		default:
			return fmt.Errorf("unsupported composite operation")
		}
	})
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return result, nil
	}
	doc := editorSelectionInfo{editorInfo: result.(editorInfo)}
	for _, index := range selected {
		doc.SelectedIDs = append(doc.SelectedIDs, fmt.Sprintf("%s/%d", args[1].String(), index))
	}
	return doc, nil
}

// editorPaintStyle 解析绘制颜色与描边增量，供顶层和内部选区共用
// 入参: operation 操作, args 绘制参数
// 返回: ofdgo.ObjectStyle 样式增量, error 颜色错误
func (e *businessEngine) editorPaintStyle(operation string, args []platformValue) (ofdgo.ObjectStyle, error) {
	style := ofdgo.ObjectStyle{}
	if operation == "textColor" {
		style.FillColor = &ofdgo.FillColor{}
		if err := e.setEditorColor(style.FillColor, args[0].String()); err != nil {
			return style, err
		}
	} else {
		if !args[0].IsNull() {
			value := args[0].Bool()
			style.Fill = &value
		}
		if !args[2].IsNull() {
			value := args[2].Bool()
			style.Stroke = &value
		}
		var colors [2]*ofdgo.FillColor
		for i := range colors {
			if !args[1+i*2].IsNull() {
				colors[i] = &ofdgo.FillColor{}
				if err := e.setEditorColor(colors[i], args[1+i*2].String()); err != nil {
					return style, err
				}
			}
		}
		style.FillColor, style.StrokeColor = colors[0], (*ofdgo.StrokeColor)(colors[1])
		if !args[4].IsNull() {
			value := args[4].Float()
			style.LineWidth = &value
		}
	}
	return style, nil
}

// compositeIndexes 将画布成员路径转换为同一范围的序号
// 入参: key 父路径, ids 成员路径
// 返回: []int 成员序号, error 错误信息
func (e *businessEngine) compositeIndexes(key string, ids []string) ([]int, error) {
	indexes := make([]int, 0, len(ids))
	for _, id := range ids {
		part, found := strings.CutPrefix(id, key+"/")
		if !found {
			return nil, fmt.Errorf("selection is outside composite")
		}
		i, err := strconv.Atoi(part)
		if err != nil || i < 0 {
			return nil, fmt.Errorf("invalid composite member")
		}
		indexes = append(indexes, i)
	}
	return indexes, nil
}

// changeCompositeText 修改内部文字，嵌入字体前校验选区用字
// 入参: page 页面索引, path 父路径, indexes 成员序号, operation 操作, args 内容及样式
// 返回: error 错误信息
func (e *businessEngine) changeCompositeText(page int, path ofdgo.ObjectPath, indexes []int, operation string, args []platformValue) error {
	var value *string
	if operation == "text" {
		if len(indexes) != 1 {
			return fmt.Errorf("text editing requires one member")
		}
		if !args[0].IsNull() {
			text := args[0].String()
			value = &text
		}
		args = args[1:]
	}
	style := ofdgo.TextStyle{Size: args[1].Float()}
	if len(args) > 3 {
		style.Reflow = args[3].Bool()
	}
	if !args[0].IsNull() {
		data, err := e.bytesFromJS(args[0])
		if err != nil {
			return err
		}
		members, err := e.currentEditor.CompositeObjectsAt(page, path, indexes)
		if err != nil {
			return err
		}
		var text strings.Builder
		for _, member := range members {
			if !member.Capabilities.ReplaceFont {
				return fmt.Errorf("composite font replacement is not supported")
			}
			if value != nil {
				text.WriteString(*value)
			} else {
				text.WriteString(member.Object.TextObject.Text())
			}
		}
		if err := e.checkFontGlyphs(data, text.String()); err != nil {
			return err
		}
		style.Font, err = e.currentEditor.AddFont(FontFile{Data: data}, 0)
		if err != nil {
			return err
		}
	}
	if !args[2].IsNull() {
		var fill ofdgo.FillColor
		if err := e.setEditorColor(&fill, args[2].String()); err != nil {
			return err
		}
		style.Color = fill.Value
	}
	if value != nil {
		return e.currentEditor.UpdateCompositeText(page, path, indexes[0], *value, style)
	}
	return e.currentEditor.StyleCompositeText(page, path, indexes, style)
}

// editorFont 读取对象编辑使用的字体，匹配的外部字体不嵌入文档
// 入参: args 字体资源标识、可选的原字形映射标记
// 返回: any 字体数据, error 错误信息
func (e *businessEngine) editorFont(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := e.currentEditor.FontData(args[0].String())
	if err != nil {
		return nil, err
	}
	mapped := len(args) > 1 && args[1].Bool()
	_, data, _, _, err = ofdgo.FixFontDataAggressive(data, mapped, false)
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(data)}), nil
}

// editorObjectAt 读取页面对象或复合对象成员的有效样式快照
// 入参: page 页面索引, key 界面对象路径
// 返回: ofdgo.GraphicObject 对象快照, error 错误信息
func (e *businessEngine) editorObjectAt(page int, key string) (ofdgo.GraphicObject, error) {
	if e.currentEditor == nil {
		return ofdgo.GraphicObject{}, fmt.Errorf("no document is being edited")
	}
	if separator := strings.LastIndex(key, "/"); separator >= 0 {
		path, err := e.compositePath(key[:separator])
		if err != nil {
			return ofdgo.GraphicObject{}, err
		}
		member, err := strconv.Atoi(key[separator+1:])
		if err != nil {
			return ofdgo.GraphicObject{}, err
		}
		members, err := e.currentEditor.CompositeObjectsAt(page, path, []int{member})
		if err != nil {
			return ofdgo.GraphicObject{}, err
		}
		return members[0].Object, nil
	}
	return e.currentEditor.Object(page, key)
}

// previewText 生成保留原定位的输入预览，不修改文档或历史
// 入参: args 页码、对象路径、新文字、可选字体数据、字号及重排标记
// 返回: any 定位段及字体样式, error 错误信息
func (e *businessEngine) previewText(args []platformValue) (any, error) {
	key := args[1].String()
	object, err := e.editorObjectAt(args[0].Int(), key)
	if err != nil {
		return nil, err
	}
	text := object.TextObject
	if object.Type != "TextObject" {
		return nil, fmt.Errorf("object is not text")
	}
	editor := e.currentEditor
	fontID := ""
	if len(args) > 3 && !args[3].IsNull() {
		data, err := e.bytesFromJS(args[3])
		if err != nil {
			return nil, err
		}
		editor = ofdgo.NewEditor()
		editor.SetResources(e.resources)
		fontID, err = editor.AddFont(FontFile{Data: data}, 0)
		if err != nil {
			return nil, err
		}
		text.Font = ""
	}
	if len(args) > 4 && args[4].Float() != text.Size || len(args) > 5 && args[5].Bool() {
		if fontID != "" {
			text.Font = fontID
		}
		var x, y float64
		if strings.Contains(key, "/") {
			x, _ = strconv.ParseFloat(text.TextCode[0].X, 64)
			y, _ = strconv.ParseFloat(text.TextCode[0].Y, 64)
		}
		text.Size = args[4].Float()
		_, layout := text.TextLayout()
		if err := editor.LayoutText(&text, args[2].String(), layout); err != nil {
			return nil, err
		}
		if strings.Contains(key, "/") {
			baseline, _ := strconv.ParseFloat(text.TextCode[0].Y, 64)
			for i := range text.TextCode {
				code := &text.TextCode[i]
				cx, _ := strconv.ParseFloat(code.X, 64)
				cy, _ := strconv.ParseFloat(code.Y, 64)
				code.X, code.Y = fmt.Sprint(cx+x), fmt.Sprint(cy+y-baseline)
			}
		}
	} else if err := editor.RewriteTextWithFont(&text, args[2].String(), fontID); err != nil {
		return nil, err
	}
	glyphs := make(map[int]string)
	glyphCounts := make(map[int]int)
	if fontID == "" {
		for _, transform := range text.CGTransform {
			if transform.CodeCount != transform.GlyphCount && transform.GlyphCount != 1 {
				continue
			}
			for i, value := range strings.Fields(transform.Glyphs) {
				glyph, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return nil, err
				}
				glyphs[transform.CodePosition+i] = string(rune(0xF0000 + glyph))
			}
			if transform.GlyphCount == 1 && transform.CodeCount > 1 {
				glyphCounts[transform.CodePosition] = transform.CodeCount
			}
		}
	}
	var runs []any
	var x, y float64
	value, offset, glyphOffset := []rune(text.Text()), 0, 0
	for _, code := range text.TextCode {
		if code.X != "" {
			x, _ = strconv.ParseFloat(code.X, 64)
		}
		if code.Y != "" {
			y, _ = strconv.ParseFloat(code.Y, 64)
		}
		line := offset < len(value) && value[offset] == '\n'
		if line {
			offset++
		}
		content := (ofdgo.TextObject{TextCode: []ofdgo.TextCode{code}}).Text()
		code.X, code.Y = fmt.Sprint(x), fmt.Sprint(y)
		positioner := ofdgo.NewTextPositioner(code)
		positions := make([][2]float64, len([]rune(content)))
		var point ofdgo.Point
		remaining := 0
		for i := range positions {
			if remaining == 0 {
				point = positioner.Next()
				remaining = max(1, glyphCounts[glyphOffset+i])
			}
			positions[i] = [2]float64{point.X, point.Y}
			remaining--
		}
		glyphOffset += len(positions)
		runs = append(runs, map[string]any{"text": content, "x": x, "y": y, "positions": positions, "line": line})
		offset += len(positions)
	}
	scale := text.HScale
	if scale == 0 {
		scale = 1
	}
	return map[string]any{"runs": runs, "glyphs": glyphs, "glyphCounts": glyphCounts, "scale": scale, "vscale": text.VScale, "rotation": text.CharDirection, "weight": text.Weight, "italic": text.Italic}, nil
}

// readTextSelection 将画布路径转换为同一容器的库层文字选区
// 入参: ids 图元路径列表
// 返回: ofdgo.TextSelection 文字选区, error 路径错误
func (e *businessEngine) readTextSelection(ids []string) (ofdgo.TextSelection, error) {
	selection := ofdgo.TextSelection{IDs: ids}
	if e.currentEditor == nil {
		return selection, fmt.Errorf("no document is being edited")
	}
	if len(ids) == 0 {
		return selection, fmt.Errorf("text selection is empty")
	}
	if index := strings.LastIndex(ids[0], "/"); index >= 0 {
		key := ids[0][:index]
		path, err := e.compositePath(key)
		if err != nil {
			return selection, err
		}
		indexes, err := e.compositeIndexes(key, ids)
		return ofdgo.TextSelection{Root: &path, Indexes: indexes}, err
	}
	for _, id := range ids {
		if strings.Contains(id, "/") {
			return selection, fmt.Errorf("text selection crosses containers")
		}
	}
	return selection, nil
}

// textSelection 分析联合文字并加载预览所需字体，不改变文档资源
// 入参: args 页码和图元路径列表
// 返回: any 文字选区、定位预览和字体, error 分析错误
func (e *businessEngine) textSelection(args []platformValue) (any, error) {
	selection, err := e.readTextSelection(e.stringsFromJS(args[1]))
	if err != nil {
		return nil, err
	}
	analysis, err := e.currentEditor.AnalyzeTextSelection(args[0].Int(), selection)
	if err != nil {
		return nil, err
	}
	source, err := e.textSelectionSource(analysis.Parts, analysis.Bounds, analysis.Text, true)
	if err != nil {
		return nil, err
	}
	first := analysis.Parts[0].TextObject
	_, origin := analysis.Parts[0].Matrix.Transform(0, 0)
	_, unit := analysis.Parts[0].Matrix.Transform(0, 1)
	item := e.editorBox(analysis.Bounds)
	item["text"], item["font"], item["size"] = analysis.Text, first.Font, first.Size*(unit-origin)
	item["color"] = e.richTextColorHex(analysis.Parts[0].Color)
	item["alpha"] = 255
	if first.Alpha != nil {
		item["alpha"] = *first.Alpha
	}
	item["capabilities"] = map[string]any{"textContent": true, "rewriteText": analysis.Replace, "replaceFont": true, "paint": true, "reflow": analysis.Reflow, "layoutKnown": false}
	item["textFrame"] = map[string]any{"width": analysis.Bounds.W, "height": analysis.Bounds.H, "matrix": e.editorMatrix(ofdgo.TranslationMatrix(analysis.Bounds.X, analysis.Bounds.Y))}
	available, err := e.currentSession.Reader.Fonts()
	if err != nil {
		return nil, err
	}
	for _, font := range available {
		if font.ID == first.Font {
			item["fontName"] = font.FontName
			break
		}
	}
	data, err := json.Marshal(map[string]any{"item": item, "source": source})
	if err != nil {
		return nil, err
	}
	result := platformParseJSON(data)
	fonts := []any{}
	seen := make(map[string]bool)
	for _, part := range analysis.Parts {
		id := part.TextObject.Font
		if seen[id] {
			continue
		}
		seen[id] = true
		data, err := e.currentEditor.FontData(id)
		if err != nil {
			return nil, err
		}
		_, data, _, _, err = ofdgo.FixFontDataAggressive(data, true, false)
		if err != nil {
			return nil, err
		}
		font := map[string]any{}
		font["id"] = id
		font["bytes"] = bytesToJS(data)
		fonts = append(fonts, font)
	}
	result["fonts"] = fonts
	return successResult(result), nil
}

// textSelectionSource 将库层逐字定位转换为选区局部坐标及分段字体样式
// 入参: parts 预览文字图元, box 原始选区边界, value 完整原文, mapped 是否使用原字形映射
// 返回: map[string]any 定位预览, error 字形映射错误
func (e *businessEngine) textSelectionSource(parts []ofdgo.TextSelectionPart, box ofdgo.Box, value string, mapped bool) (map[string]any, error) {
	runs := make([]any, 0, len(parts))
	glyphs := make(map[int]string)
	offset := 0
	logical, cursor := []rune(value), 0
	for _, part := range parts {
		if part.Deleted {
			continue
		}
		text := part.TextObject
		originX, originY := part.Matrix.Transform(0, 0)
		xAxis, _ := part.Matrix.Transform(1, 0)
		_, yAxis := part.Matrix.Transform(0, 1)
		sx, sy := xAxis-originX, yAxis-originY
		scale := text.HScale
		if scale == 0 {
			scale = 1
		}
		for _, transform := range text.CGTransform {
			if !mapped {
				break
			}
			if transform.CodeCount != transform.GlyphCount {
				return nil, fmt.Errorf("text selection contains ambiguous glyph mapping")
			}
			for i, value := range strings.Fields(transform.Glyphs) {
				glyph, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return nil, err
				}
				glyphs[offset+transform.CodePosition+i] = string(rune(0xF0000 + glyph))
			}
		}
		alpha := 255
		if text.Alpha != nil {
			alpha = int(*text.Alpha)
		}
		chars := []rune(strings.ReplaceAll(text.Text(), "\n", ""))
		if len(chars) != len(part.Points) {
			return nil, fmt.Errorf("text selection positions do not match characters")
		}
		for i, char := range chars {
			x, y := part.Matrix.Transform(part.Points[i].X, part.Points[i].Y)
			line := false
			for cursor < len(logical) && logical[cursor] == '\n' {
				if line || len(runs) == 0 {
					runs = append(runs, map[string]any{"text": "", "x": x - box.X, "y": y - box.Y - text.Size*sy,
						"positions": [][2]float64{}, "line": line, "font": text.Font, "size": text.Size * sy})
				}
				line = true
				cursor++
			}
			if cursor >= len(logical) || logical[cursor] != char {
				return nil, fmt.Errorf("text selection preview does not match content")
			}
			cursor++
			runs = append(runs, map[string]any{"text": string(char), "x": x - box.X, "y": y - box.Y,
				"positions": [][2]float64{{x - box.X, y - box.Y}}, "line": line, "font": text.Font,
				"size": text.Size * sy, "scale": scale * sx / sy, "vscale": 1, "rotation": 0,
				"weight": text.Weight, "italic": text.Italic, "alpha": alpha, "color": e.richTextColorHex(part.Color)})
		}
		offset += len(chars)
	}
	for cursor < len(logical) && logical[cursor] == '\n' && len(runs) > 0 {
		last := runs[len(runs)-1].(map[string]any)
		runs = append(runs, map[string]any{"text": "", "x": float64(0), "y": last["y"].(float64) + last["size"].(float64),
			"positions": [][2]float64{}, "line": true, "font": last["font"], "size": last["size"]})
		cursor++
	}
	if cursor != len(logical) {
		return nil, fmt.Errorf("text selection preview is incomplete")
	}
	return map[string]any{"runs": runs, "glyphs": glyphs}, nil
}

// readTextSelectionEdit 将完整草稿转为码点范围修改或显式富文本重排
// 入参: args 编辑参数, analysis 原始选区
// 返回: ofdgo.TextSelectionEdit 修改内容, error 参数错误
func (e *businessEngine) readTextSelectionEdit(args []platformValue, analysis ofdgo.TextSelectionAnalysis) (ofdgo.TextSelectionEdit, error) {
	old, next := []rune(analysis.Text), []rune(args[2].String())
	start, end, tail := 0, len(old), len(next)
	for start < end && start < tail && old[start] == next[start] {
		start++
	}
	for end > start && tail > start && old[end-1] == next[tail-1] {
		end--
		tail--
	}
	edit := ofdgo.TextSelectionEdit{Start: start, End: end, Text: string(next[start:tail])}
	if !args[3].IsNull() {
		var err error
		edit.FontData, err = e.bytesFromJS(args[3])
		if err != nil {
			return edit, err
		}
	}
	if !args[4].IsNull() {
		edit.Size = args[4].Float()
	}
	if !args[5].IsNull() {
		value := args[5].String()
		if len(value) != 7 || value[0] != '#' {
			return edit, fmt.Errorf("invalid RGB color %q", value)
		}
		rgb, err := hex.DecodeString(value[1:])
		if err != nil {
			return edit, err
		}
		edit.Color = fmt.Sprintf("%d %d %d", rgb[0], rgb[1], rgb[2])
	}
	if args[6].Bool() && len(next) > 0 {
		value := analysis.RichText
		var replacement []ofdgo.RichTextRun
		if tail > start {
			part, err := value.Slice(start, start)
			if tail-start == end-start {
				part, err = value.Slice(start, end)
			}
			if err != nil {
				return edit, err
			}
			if tail-start == end-start {
				offset := start
				for _, run := range part.Runs {
					count := len([]rune(run.Text))
					replacement = append(replacement, ofdgo.RichTextRun{Text: string(next[offset : offset+count]), Format: run.Format})
					offset += count
				}
			} else {
				replacement = []ofdgo.RichTextRun{{Text: edit.Text, Format: part.Runs[0].Format}}
			}
		}
		if err := value.Replace(start, end, replacement); err != nil {
			return edit, err
		}
		value.Layout.AutoBidi = true
		edit.Reflow = &value
	}
	return edit, nil
}

// previewTextSelection 预览联合修改，不改变文档资源及撤销记录
// 入参: args 页码、图元路径、内容、字体数据、字号、颜色和重排标记
// 返回: any 定位预览, error 编辑错误
func (e *businessEngine) previewTextSelection(args []platformValue) (any, error) {
	selection, err := e.readTextSelection(e.stringsFromJS(args[1]))
	if err != nil {
		return nil, err
	}
	analysis, err := e.currentEditor.AnalyzeTextSelection(args[0].Int(), selection)
	if err != nil {
		return nil, err
	}
	edit, err := e.readTextSelectionEdit(args, analysis)
	if err != nil {
		return nil, err
	}
	preview, err := e.currentEditor.PreviewTextSelection(args[0].Int(), analysis.Selection, edit)
	if err != nil {
		return nil, err
	}
	return e.textSelectionSource(preview.Parts, analysis.Bounds, preview.Text, len(edit.FontData) == 0)
}

// updateTextSelection 原子提交联合修改并恢复删除或重排后的选区
// 入参: args 页码、图元路径、内容、字体数据、字号、颜色和重排标记
// 返回: any 文档及选区信息, error 编辑错误
func (e *businessEngine) updateTextSelection(args []platformValue) (any, error) {
	ids := e.stringsFromJS(args[1])
	selection, err := e.readTextSelection(ids)
	if err != nil {
		return nil, err
	}
	page := args[0].Int()
	analysis, err := e.currentEditor.AnalyzeTextSelection(page, selection)
	if err != nil {
		return nil, err
	}
	edit, err := e.readTextSelectionEdit(args, analysis)
	if err != nil {
		return nil, err
	}
	preview, err := e.currentEditor.PreviewTextSelection(page, analysis.Selection, edit)
	if err != nil {
		return nil, err
	}
	result, err := e.changeObjects(func() error { return e.currentEditor.UpdateTextSelection(page, analysis.Selection, edit) })
	if err != nil {
		return nil, err
	}
	selected := append([]string{}, preview.Selection.IDs...)
	for _, index := range preview.Selection.Indexes {
		key := ids[0][:strings.LastIndex(ids[0], "/")]
		selected = append(selected, fmt.Sprintf("%s/%d", key, index))
	}
	return editorSelectionInfo{editorInfo: result.(editorInfo), SelectedIDs: selected}, nil
}

// previewPositionedText 预览保留定位的文字替换，不修改编辑器或撤销记录
// 入参: args 页面、对象路径和内容
// 返回: any SVG预览, error 错误信息
func (e *businessEngine) previewPositionedText(args []platformValue) (any, error) {
	object, err := e.editorObjectAt(args[0].Int(), args[1].String())
	if err != nil {
		return nil, err
	}
	if object.Type != "TextObject" {
		return nil, fmt.Errorf("object is not text")
	}
	if err := e.currentEditor.RewriteText(&object.TextObject, args[2].String()); err != nil {
		return nil, err
	}
	measured, err := e.currentSession.Renderer.MeasureObject(object, ofdgo.MeasureOptions{})
	if err != nil {
		return nil, err
	}
	box := measured.Bounds
	margin := object.TextObject.Size / 4
	box.X, box.Y, box.W, box.H = box.X-margin, box.Y-margin, box.W+2*margin, box.H+2*margin
	boundary, err := ofdgo.ParseBox(object.TextObject.Boundary)
	if err != nil {
		return nil, err
	}
	object.TextObject.Boundary = fmt.Sprintf("%g %g %g %g", boundary.X-box.X, boundary.Y-box.Y, boundary.W, boundary.H)
	page := &ofdgo.PageContent{Area: ofdgo.PageArea{PhysicalBox: fmt.Sprintf("0 0 %g %g", box.W, box.H)}, Content: ofdgo.Content{Layer: []ofdgo.Layer{{Objects: []ofdgo.GraphicObject{object}}}}}
	var output bytes.Buffer
	if err := e.currentSession.Renderer.RenderToSVG(page, &output); err != nil {
		return nil, err
	}
	return output.String(), nil
}

// updateText 修改文字，null内容保留定位且允许替换字体，null字体数据和颜色沿用对象属性
// 入参: args 页码、对象标识、内容、字体数据、字号、颜色及可选重排标记
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) updateText(args []platformValue) (any, error) {
	return e.changeObjects(func() error { return e.applyTextUpdate(args) })
}

// applyTextUpdate 在当前事务中修改文字及字体，不刷新会话
// 入参: args 页码、对象标识、内容、字体数据、字号、颜色及可选重排标记
// 返回: error 字体、排版或提交错误
func (e *businessEngine) applyTextUpdate(args []platformValue) error {
	page, id := args[0].Int(), args[1].String()
	object, err := e.currentEditor.Object(page, id)
	if err != nil {
		return err
	}
	if object.Type != "TextObject" {
		return fmt.Errorf("object %q is not text", id)
	}
	if !args[3].IsNull() {
		data, err := e.bytesFromJS(args[3])
		if err != nil {
			return err
		}
		value := object.TextObject.Text()
		if !args[2].IsNull() {
			value = args[2].String()
		}
		if err := e.checkFontGlyphs(data, value); err != nil {
			return err
		}
		object.TextObject.Font, err = e.currentEditor.AddFont(FontFile{Data: data}, 0)
		if err != nil {
			return err
		}
	}
	if !args[2].IsNull() {
		if args[4].Float() == object.TextObject.Size || len(args) > 6 && args[6].Bool() {
			style := ofdgo.TextStyle{Reflow: len(args) > 6 && args[6].Bool()}
			if style.Reflow {
				style.Size = args[4].Float()
			}
			if !args[3].IsNull() {
				style.Font = object.TextObject.Font
			}
			if !args[5].IsNull() {
				var fill ofdgo.FillColor
				if err := e.setEditorColor(&fill, args[5].String()); err != nil {
					return err
				}
				style.Color = fill.Value
			}
			return e.currentEditor.UpdateTextContent(page, id, args[2].String(), style)
		}
		object.TextObject.Size = args[4].Float()
		_, layout := object.TextObject.TextLayout()
		if err := e.currentEditor.LayoutText(&object.TextObject, args[2].String(), layout); err != nil {
			return err
		}
	}
	if !args[5].IsNull() {
		if err := e.setTextColor(&object.TextObject, args[5].String()); err != nil {
			return err
		}
	}
	return e.currentEditor.UpdateObject(page, id, object)
}

// checkFontGlyphs 只检查候选字体，不注册资源或改变文档
// 入参: data 字体数据, value 待替换文字
// 返回: error 缺字或字体格式错误
func (e *businessEngine) checkFontGlyphs(data []byte, value string) error {
	missing, err := (FontFile{Data: data}).MissingGlyphs(0, value)
	if err != nil {
		return err
	}
	if missing != "" {
		return &ofdgo.MissingGlyphError{Characters: missing}
	}
	return nil
}

// checkTextFont 为画布输入预检候选字体
// 入参: args 字体数据和输入文字
// 返回: any 空结果, error 缺字或字体格式错误
func (e *businessEngine) checkTextFont(args []platformValue) (any, error) {
	data, err := e.bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	return nil, e.checkFontGlyphs(data, args[1].String())
}

// styleText 批量修改文字样式，字体预检通过后交由库原子提交
// 入参: args 页码、对象标识、字体数据、字号和颜色，null字体与颜色保持原值
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) styleText(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		page, ids := args[0].Int(), e.stringsFromJS(args[1])
		style := ofdgo.TextStyle{Size: args[3].Float()}
		if !args[2].IsNull() {
			data, err := e.bytesFromJS(args[2])
			if err != nil {
				return err
			}
			var value strings.Builder
			for _, id := range ids {
				object, err := e.editorObjectAt(page, id)
				if err != nil {
					return err
				}
				if object.Type == "TextObject" {
					value.WriteString(object.TextObject.Text())
				} else {
					path, err := e.compositePath(id)
					if err != nil {
						return err
					}
					text, err := e.currentEditor.TargetRichText(page, path)
					if err != nil {
						return err
					}
					value.WriteString(text.Text())
				}
			}
			if err := e.checkFontGlyphs(data, value.String()); err != nil {
				return err
			}
			style.Font, err = e.currentEditor.AddFont(FontFile{Data: data}, 0)
			if err != nil {
				return err
			}
		}
		if !args[4].IsNull() {
			var color ofdgo.FillColor
			if err := e.setEditorColor(&color, args[4].String()); err != nil {
				return err
			}
			style.Color = color.Value
		}
		paths, err := e.editorTargetPaths(ids)
		if err != nil {
			return err
		}
		return e.currentEditor.StyleTargetsText(page, paths, style)
	})
}

// formatText 批量修改选区格式，未指定的属性保持各自原值
// 入参: args 页面索引、完整路径数组、格式JSON及可选显式重排标志
// 返回: any 文档信息, error 校验或提交错误
func (e *businessEngine) formatText(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		paths, err := e.editorTargetPaths(e.stringsFromJS(args[1]))
		if err != nil {
			return err
		}
		var patch ofdgo.TextFormatPatch
		if err := json.Unmarshal([]byte(args[2].String()), &patch); err != nil {
			return err
		}
		for _, value := range []*string{patch.Color, patch.Background} {
			if value == nil || *value == "" {
				continue
			}
			var color ofdgo.FillColor
			if err := e.setEditorColor(&color, *value); err != nil {
				return err
			}
			*value = color.Value
		}
		if len(args) > 3 && args[3].Bool() {
			return e.currentEditor.ReflowTargetsText(args[0].Int(), paths, patch)
		}
		return e.currentEditor.FormatTargetsText(args[0].Int(), paths, patch)
	})
}

// styleTextFonts 预检并原子导入各款式字体，批量替换文字字体及粗斜体
// 入参: args 页面索引、完整路径、款式映射JSON、字体数据集合及重排标记
// 返回: any 文档信息, error 字体或编辑错误
func (e *businessEngine) styleTextFonts(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		paths, err := e.editorTargetPaths(e.stringsFromJS(args[1]))
		if err != nil {
			return err
		}
		var changes []ofdgo.TextFontChange
		if err := json.Unmarshal([]byte(args[2].Get("changes").String()), &changes); err != nil {
			return err
		}
		fonts := make(map[string]string)
		input := args[2].Get("fonts")
		for index := 0; index < input.Length(); index++ {
			entry := input.Index(index)
			data, err := e.bytesFromJS(entry.Get("data"))
			if err != nil {
				return err
			}
			if err := e.checkFontGlyphs(data, entry.Get("text").String()); err != nil {
				return err
			}
			id, err := e.currentEditor.AddFont(FontFile{Data: data}, 0)
			if err != nil {
				return err
			}
			fonts[entry.Get("key").String()] = id
		}
		for index := range changes {
			if id, ok := fonts[changes[index].To.Font]; ok {
				changes[index].To.Font = id
			}
		}
		return e.currentEditor.StyleTargetsFonts(args[0].Int(), paths, changes, args[2].Get("reflow").Bool())
	})
}

// layoutTexts 批量修改段落增量，未指定的选项沿用各对象原值
// 入参: args 页面索引、完整路径数组和段落JSON
// 返回: any 文档信息, error 校验或提交错误
func (e *businessEngine) layoutTexts(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		paths, err := e.editorTargetPaths(e.stringsFromJS(args[1]))
		if err != nil {
			return err
		}
		var patch struct {
			ofdgo.RichTextParagraph
			Wrap *bool `json:"wrap"`
		}
		if err := json.Unmarshal([]byte(args[2].String()), &patch); err != nil {
			return err
		}
		return e.currentEditor.LayoutTargetsText(args[0].Int(), paths, patch.RichTextParagraph, patch.Wrap)
	})
}

// editorRichText 将富文本复合图元作为一个文本框提供给编辑器
// 入参: item 浏览器对象, object 标准复合图元, value 富文本, fonts 字体名称, local 局部编辑框, matrix 页面变换
func (e *businessEngine) editorRichText(item map[string]any, object ofdgo.GraphicObject, value ofdgo.RichText, fonts map[string]string, local ofdgo.Box, matrix ofdgo.Matrix) {
	if len(value.Runs) == 0 || object.CompositeGraphicUnit.Clips != nil {
		return
	}
	width := local.W
	_, contentBounds, err := e.currentEditor.LayoutRichText(value, width)
	if err != nil {
		return
	}
	height := contentBounds.Y + contentBounds.H
	format, layout := value.Runs[0].Format, value.Layout
	selection := contentBounds
	if !value.AutoWidth || layout.Wrap {
		selection = ofdgo.Box{W: width, H: height}
	}
	frame := matrix.TransformBox(selection)
	var content strings.Builder
	for _, run := range value.Runs {
		content.WriteString(run.Text)
	}
	item["actualType"], item["type"], item["richText"] = object.Type, "TextObject", e.browserRichText(value)
	item["text"], item["font"], item["fontName"], item["size"] = content.String(), format.Font, fonts[format.Font], format.Size
	item["color"] = e.richTextColorHex(format.Color)
	item["bold"], item["italic"], item["underline"], item["strikeout"], item["shadow"] = format.Bold, format.Italic, format.Underline, format.Strikeout, format.Shadow
	item["background"] = ""
	if format.Background != "" {
		item["background"] = e.richTextColorHex(format.Background)
	}
	item["wrap"], item["align"], item["paragraphHeight"] = layout.Wrap, layout.Align, layout.LineHeight
	item["letterSpacing"], item["leftIndent"], item["rightIndent"], item["firstLineIndent"] = layout.LetterSpacing, layout.LeftIndent, layout.RightIndent, layout.FirstLineIndent
	item["paragraphWidth"], item["bounds"] = width, e.editorBox(frame)
	item["x"], item["y"], item["width"], item["height"] = frame.X, frame.Y, frame.W, frame.H
	item["textFrame"] = map[string]any{"width": width, "height": height, "matrix": e.editorMatrix(matrix)}
	capabilities := item["capabilities"].(map[string]any)
	for _, key := range []string{"textContent", "rewriteText", "replaceFont", "reflow", "layoutKnown", "paint"} {
		capabilities[key] = true
	}
	capabilities["children"], capabilities["ungroup"] = false, false
	capabilities["stretch"] = false
	if capabilities["reasonCode"] == string(ofdgo.EditUnsupportedObject) && capabilities["transform"] == true && capabilities["copy"] == true && object.CompositeGraphicUnit.Clips == nil {
		delete(capabilities, "reason")
		delete(capabilities, "reasonCode")
	}
}

// getRichText 读取文字分段与段落选项，颜色转换为浏览器RGB格式
// 入参: args 页码、对象标识和可选显式重排标记
// 返回: any 富文本快照及可选编辑框，无法提升时返回unsupported, error 读取或排版错误
func (e *businessEngine) getRichText(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	path, err := e.compositePath(args[1].String())
	if err != nil {
		return nil, err
	}
	value, frame, matrix, err := e.currentEditor.PrepareRichText(args[0].Int(), path, len(args) > 2 && args[2].Bool())
	var editError *ofdgo.EditError
	if len(args) > 2 && (errors.Is(err, ofdgo.ErrNotRichText) || errors.As(err, &editError) && editError.Code == ofdgo.EditLayoutRequired) {
		return map[string]any{"unsupported": true}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(args) > 2 {
		return map[string]any{"model": e.browserRichText(value), "frame": map[string]any{
			"width": frame.W, "height": frame.H, "matrix": e.editorMatrix(matrix),
		}}, nil
	}
	return e.browserRichText(value), nil
}

// richTextFonts 提供跨文档文字复制所需的独立字体数据
// 入参: args 富文本JSON
// 返回: any 字体集合, error 读取错误
func (e *businessEngine) richTextFonts(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	value, err := e.readRichText(args[0])
	if err != nil {
		return nil, err
	}
	fonts, err := e.currentEditor.RichTextFonts(value)
	if err != nil {
		return nil, err
	}
	result := make([]any, 0, len(fonts))
	for key, data := range fonts {
		_, data, _, _, err = ofdgo.FixFontDataAggressive(data, false, false)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"key": key, "data": bytesToJS(data)})
	}
	return successResult(result), nil
}

// browserRichText 提供独立富文本快照和浏览器颜色，不改变库层样式
// 入参: value 库层富文本
// 返回: map[string]any 文字分段和段落选项
func (e *businessEngine) browserRichText(value ofdgo.RichText) map[string]any {
	runs := make([]any, len(value.Runs))
	for i, run := range value.Runs {
		format := run.Format
		background := ""
		if format.Background != "" {
			background = e.richTextColorHex(format.Background)
		}
		runs[i] = map[string]any{"text": run.Text, "format": map[string]any{
			"font": format.Font, "size": format.Size, "color": e.richTextColorHex(format.Color),
			"bold": format.Bold, "italic": format.Italic, "underline": format.Underline,
			"strikeout": format.Strikeout, "shadow": format.Shadow, "background": background,
		}}
	}
	paragraphs := make([]any, len(value.Paragraphs))
	for i, paragraph := range value.Paragraphs {
		entry := map[string]any{"align": paragraph.Align}
		for key, value := range map[string]*float64{"lineHeight": paragraph.LineHeight, "letterSpacing": paragraph.LetterSpacing, "leftIndent": paragraph.LeftIndent, "rightIndent": paragraph.RightIndent, "firstLineIndent": paragraph.FirstLineIndent} {
			if value != nil {
				entry[key] = *value
			}
		}
		paragraphs[i] = entry
	}
	layout := value.Layout
	return map[string]any{"autoWidth": value.AutoWidth, "runs": runs, "paragraphs": paragraphs, "layout": map[string]any{
		"autoBidi": layout.AutoBidi, "shape": layout.Shape,
		"shaping": map[string]any{
			"Bidi": layout.Shaping.Bidi, "Direction": layout.Shaping.Direction,
			"Script": layout.Shaping.Script, "Language": layout.Shaping.Language, "Features": layout.Shaping.Features,
		},
		"wrap": layout.Wrap, "align": layout.Align, "lineHeight": layout.LineHeight,
		"letterSpacing": layout.LetterSpacing, "leftIndent": layout.LeftIndent,
		"rightIndent": layout.RightIndent, "firstLineIndent": layout.FirstLineIndent,
	}}
}

// richTextColorHex 将富文本的RGB分量转换为浏览器颜色，不套用文档颜色空间
// 入参: value RGB分量
// 返回: string 十六进制颜色
func (e *businessEngine) richTextColorHex(value string) string {
	var r, g, b int
	fmt.Sscan(value, &r, &g, &b)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// readRichText 读取浏览器富文本并转换显式RGB颜色
// 入参: value 富文本JSON
// 返回: ofdgo.RichText 库层快照, error 数据错误
func (e *businessEngine) readRichText(value platformValue) (ofdgo.RichText, error) {
	var text ofdgo.RichText
	if err := json.Unmarshal([]byte(value.String()), &text); err != nil {
		return text, err
	}
	for i := range text.Runs {
		format := &text.Runs[i].Format
		for _, field := range []*string{&format.Color, &format.Background} {
			if *field == "" {
				continue
			}
			if len(*field) != 7 || (*field)[0] != '#' {
				return text, fmt.Errorf("invalid RGB color %q", *field)
			}
			rgb, err := hex.DecodeString((*field)[1:])
			if err != nil {
				return text, err
			}
			*field = fmt.Sprintf("%d %d %d", rgb[0], rgb[1], rgb[2])
		}
	}
	return text, nil
}

// saveRichText 原子保存文字、样式和新增字体，空标识创建文本框
// 入参: args 页码、标识、富文本JSON、边界及重排标记、可选字体集合和父路径
// 返回: any 文档信息及文字选区, error 错误信息
func (e *businessEngine) saveRichText(args []platformValue) (any, error) {
	value, err := e.readRichText(args[2])
	if err != nil {
		return nil, err
	}
	page, id := args[0].Int(), args[1].String()
	result, err := e.changeObjects(func() error {
		if len(args) > 4 {
			if err := e.importRichTextFonts(&value, args[4]); err != nil {
				return err
			}
		}
		box := args[3]
		if !box.IsNull() && !box.IsUndefined() && box.Get("reflow").Type() == platformTypeBoolean && box.Get("reflow").Bool() {
			path, err := e.compositePath(id)
			if err != nil {
				return err
			}
			return e.currentEditor.ReflowRichText(page, path, value)
		}
		if id != "" {
			path, err := e.compositePath(id)
			if err != nil {
				return err
			}
			offset, width := 0.0, 0.0
			if !box.IsNull() && !box.IsUndefined() && box.Get("frameWidth").Type() == platformTypeNumber {
				width = box.Get("frameWidth").Float()
				if box.Get("frameOffset").Type() == platformTypeNumber {
					offset = box.Get("frameOffset").Float()
				}
			}
			return e.currentEditor.UpdateTargetRichText(page, path, value, offset, width)
		}
		if len(args) > 5 && args[5].String() != "" {
			key := args[5].String()
			path, err := e.compositePath(key)
			if err != nil {
				return err
			}
			index, err := e.currentEditor.AddCompositeRichText(page, path, ofdgo.Box{X: box.Get("x").Float(), Y: box.Get("y").Float(), W: box.Get("width").Float(), H: box.Get("height").Float()}, value)
			id = fmt.Sprintf("%s/%d", key, index)
			return err
		}
		id, err = e.currentEditor.AddRichText(page, ofdgo.Box{X: box.Get("x").Float(), Y: box.Get("y").Float(), W: box.Get("width").Float(), H: box.Get("height").Float()}, value)
		return err
	})
	if err != nil {
		return nil, err
	}
	info, ok := result.(editorInfo)
	if !ok {
		return result, nil
	}
	return editorSelectionInfo{editorInfo: info, SelectedIDs: []string{id}}, nil
}

// importRichTextFonts 在当前事务内注册实际使用的新字体并替换临时标识
// 入参: value 富文本快照, fonts 浏览器字体集合
// 返回: error 数据或字体错误
func (e *businessEngine) importRichTextFonts(value *ofdgo.RichText, fonts platformValue) error {
	if fonts.IsNull() || fonts.IsUndefined() {
		return nil
	}
	data := make(map[string][]byte)
	for i := 0; i < fonts.Length(); i++ {
		entry := fonts.Index(i)
		key := entry.Get("key").String()
		if key == "" || data[key] != nil {
			return fmt.Errorf("invalid rich text font key")
		}
		bytes, err := e.bytesFromJS(entry.Get("data"))
		if err != nil {
			return err
		}
		data[key] = bytes
	}
	imported, err := e.currentEditor.ImportRichText(*value, data)
	if err != nil {
		return err
	}
	*value = imported
	return nil
}

// previewRichText 使用库层排版校验富文本并测量边界，临时资源及历史始终回滚
// 入参: args 页码、对象标识、富文本JSON、局部宽度和可选字体集合
// 返回: any 局部边界, error 排版错误
func (e *businessEngine) previewRichText(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	value, err := e.readRichText(args[2])
	if err != nil {
		return nil, err
	}
	var result map[string]any
	finished := errors.New("rich text preview complete")
	err = e.currentEditor.Transaction(func(editor *ofdgo.Editor) error {
		if len(args) > 4 {
			if err := e.importRichTextFonts(&value, args[4]); err != nil {
				return err
			}
		}
		width := args[3].Float()
		_, bounds, err := editor.LayoutRichText(value, width)
		if err != nil {
			return err
		}
		result = map[string]any{"x": bounds.X, "y": bounds.Y, "width": bounds.W, "height": bounds.H}
		return finished
	})
	if !errors.Is(err, finished) {
		return nil, err
	}
	return result, nil
}

// setTextColor 将浏览器RGB色值写入文字对象，保留颜色透明度
// 入参: object 文字对象, value 十六进制色值
// 返回: error 错误信息
func (e *businessEngine) setTextColor(object *ofdgo.TextObject, value string) error {
	if object.FillColor == nil {
		object.FillColor = &ofdgo.FillColor{}
	}
	return e.setEditorColor(object.FillColor, value)
}

// insertText 使用选定字体创建文字对象
// 入参: args 页码、内容、字体数据、横纵坐标、字号、颜色、框宽、折行、对齐、行距、字距及左右和首行缩进
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) insertText(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := e.bytesFromJS(args[2])
	if err != nil {
		return nil, err
	}
	fontID, err := e.currentEditor.AddFont(FontFile{Data: data}, 0)
	if err != nil {
		return nil, err
	}
	page := args[0].Int()
	content, err := e.currentSession.pageContent(page)
	if err != nil {
		return nil, err
	}
	box, err := e.currentSession.pageBox(page, content)
	if err != nil {
		return nil, err
	}
	x, y := args[3].Float(), args[4].Float()
	width := args[7].Float()
	if width == 0 {
		width = 80
	}
	box = ofdgo.Box{X: x, Y: y, W: math.Min(width, box.W-x), H: box.H - y}
	object := ofdgo.GraphicObject{Type: "TextObject", TextObject: ofdgo.TextObject{
		Boundary: fmt.Sprintf("%g %g %g %g", box.X, box.Y, box.W, box.H),
		Font:     fontID, Size: args[5].Float(),
	}}
	if err := e.currentEditor.LayoutText(&object.TextObject, args[1].String(), ofdgo.TextLayout{AutoBidi: true, Wrap: args[8].Bool(), Align: args[9].String(), LineHeight: args[10].Float(), LetterSpacing: args[11].Float(), LeftIndent: args[12].Float(), RightIndent: args[13].Float(), FirstLineIndent: args[14].Float()}); err != nil {
		return nil, err
	}
	if err := e.setTextColor(&object.TextObject, args[6].String()); err != nil {
		return nil, err
	}
	return e.insertEditorObject(page, object, args[15:])
}

// layoutText 调整文字框和段落排版，保留软换行前的原文
// 入参: args 页码、对象标识、本地左侧偏移、宽度、折行、对齐、行距、字距及左右和首行缩进
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) layoutText(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	return e.changeObjects(func() error {
		return e.layoutEditorText(args[0].Int(), ofdgo.ObjectPath{ID: args[1].String()}, args[2:])
	})
}

// layoutEditorText 将正文和组合内文字的段落调整交给库统一处理
// 入参: page 页面索引, path 完整对象路径, args 偏移、宽度及段落选项
// 返回: error 排版或提交错误
func (e *businessEngine) layoutEditorText(page int, path ofdgo.ObjectPath, args []platformValue) error {
	offset, width := 0.0, 0.0
	if !args[0].IsNull() {
		offset, width = args[0].Float(), args[1].Float()
	}
	layout := ofdgo.TextLayout{AutoBidi: true, Wrap: args[2].Bool(), Align: args[3].String(), LineHeight: args[4].Float(), LetterSpacing: args[5].Float(), LeftIndent: args[6].Float(), RightIndent: args[7].Float(), FirstLineIndent: args[8].Float()}
	return e.currentEditor.LayoutTextFrame(page, path, layout, offset, width)
}

// replaceImage 替换图片资源并更新预览
// 入参: args 页码、对象标识、图片数据和适应方式
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) replaceImage(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := e.bytesFromJS(args[2])
	if err != nil {
		return nil, err
	}
	revision := e.currentEditor.Revision()
	if err := e.currentEditor.ReplaceImage(args[0].Int(), args[1].String(), data, args[3].String()); err != nil {
		return nil, err
	}
	if e.currentEditor.Revision() == revision {
		return e.editorSummary(), nil
	}
	return e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
}

// alignObject 将对象对齐页面并更新预览
// 入参: args 页码、对象标识和对齐方式
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) alignObject(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	revision := e.currentEditor.Revision()
	if err := e.currentEditor.AlignObject(args[0].Int(), args[1].String(), args[2].String()); err != nil {
		return nil, err
	}
	if e.currentEditor.Revision() == revision {
		return e.editorSummary(), nil
	}
	return e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
}

// transformObject 等比缩放后平移对象
// 入参: args 页码、对象标识、位移和缩放比例
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) transformObject(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, id := args[0].Int(), args[1].String()
	revision := e.currentEditor.Revision()
	if err := e.currentEditor.TransformObject(page, id, args[2].Float(), args[3].Float(), args[4].Float()); err != nil {
		return nil, err
	}
	if e.currentEditor.Revision() == revision {
		return e.editorSummary(), nil
	}
	return e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
}

// reshapeObject 调整基本图形的尺寸或直线端点，保留绘制样式
// 入参: args 页码、对象标识和页面几何范围
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) reshapeObject(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, id := args[0].Int(), args[1].String()
	object, err := e.currentEditor.Object(page, id)
	if err != nil {
		return nil, err
	}
	if object.Type != "PathObject" {
		return nil, fmt.Errorf("object is not a path")
	}
	box := ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()}
	if len(args) > 6 && args[6].Bool() {
		object.PathObject, err = object.PathObject.ReshapeFrame(box)
	} else {
		object.PathObject, err = object.PathObject.Reshape(box)
	}
	if err != nil {
		return nil, err
	}
	revision := e.currentEditor.Revision()
	if err := e.currentEditor.UpdateObject(page, id, object); err != nil {
		return nil, err
	}
	if e.currentEditor.Revision() == revision {
		return e.editorSummary(), nil
	}
	return e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
}

// reshapeLine 修改直线端点与箭头类型，复用库层样式保留逻辑
// 入参: args 页码、对象标识、页面端点及箭头类型
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) reshapeLine(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		page, id := args[0].Int(), args[1].String()
		object, err := e.currentEditor.Object(page, id)
		if err != nil {
			return err
		}
		if object.Type != "PathObject" {
			return fmt.Errorf("object is not a path")
		}
		object.PathObject, err = object.PathObject.ReshapeLine(ofdgo.ShapeKind(args[6].String()), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
		if err != nil {
			return err
		}
		return e.currentEditor.UpdateObject(page, id, object)
	})
}

// copyObjects 复制选区并返回新对象标识，复用字体和图片资源
// 入参: args 页码、对象标识列表和位移
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) copyObjects(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	objects, err := e.currentEditor.Objects(args[0].Int(), e.stringsFromJS(args[1]))
	if err != nil {
		return nil, err
	}
	return e.copyEditorObjects(args[0].Int(), objects, args[2].Float(), args[3].Float())
}

// captureObjects 保存独立选区快照，不修改文档或历史
// 入参: args 页面索引、对象路径数组和剪贴板标识
// 返回: any 捕获结果, error 错误信息
func (e *businessEngine) captureObjects(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	clipboard, err := e.captureEditorSelection(args[0].Int(), e.stringsFromJS(args[1]))
	if err != nil {
		return nil, err
	}
	clipboard.token = args[2].String()
	paths, err := e.editorTargetPaths(e.stringsFromJS(args[1]))
	if err != nil {
		return nil, err
	}
	packet, err := e.currentEditor.PrepareTargets(e.operationContext(), args[0].Int(), paths, true)
	if err != nil {
		return nil, err
	}
	e.copiedObjects = clipboard
	e.portableObjects = &editorClipboard{token: clipboard.token, packet: packet}
	return successResult(nil), nil
}

// exportObjects 按需压缩已准备的独立选区，切换文档或版本不影响结果
// 入参: args 剪贴板标识
// 返回: any 可跨文档导入的选区包, error 标识或写入错误
func (e *businessEngine) exportObjects(args []platformValue) (any, error) {
	clipboard := e.portableObjects
	if clipboard == nil || clipboard.token != args[0].String() {
		return nil, fmt.Errorf("object clipboard is no longer available")
	}
	var output bytes.Buffer
	if _, err := clipboard.packet.WriteToWithOptions(e.operationContext(), &output, ofdgo.WriteOptions{}); err != nil {
		return nil, err
	}
	e.portableObjects = nil
	clipboard.packet = nil
	return successResult(map[string]any{"bytes": bytesToJS(output.Bytes())}), nil
}

// discardObjects 释放被新剪贴板替代的选区，不影响后续捕获
// 入参: args 剪贴板标识
// 返回: any 清理结果, error 始终为nil
func (e *businessEngine) discardObjects(args []platformValue) (any, error) {
	token := args[0].String()
	if e.portableObjects != nil && e.portableObjects.token == token {
		e.portableObjects = nil
	}
	if e.copiedObjects != nil && e.copiedObjects.token == token {
		e.copiedObjects = nil
	}
	return successResult(nil), nil
}

// pasteObjects 将当前文档的对象快照粘贴到目标页
// 入参: args 目标页、剪贴板标识和横纵位移
// 返回: any 文档信息及新选区, error 错误信息
func (e *businessEngine) pasteObjects(args []platformValue) (any, error) {
	if e.currentEditor == nil || e.copiedObjects == nil || e.copiedObjects.token != args[1].String() {
		return nil, fmt.Errorf("object clipboard is no longer available")
	}
	ids, err := e.pasteEditorSelection(args[0].Int(), e.copiedObjects, args[2].Float(), args[3].Float())
	if err != nil {
		return nil, err
	}
	info, err := e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
	return editorSelectionInfo{info, ids}, err
}

// importObjects 将独立选区包导入当前文档，来源标签无需保持打开
// 入参: args 目标页、选区包和横纵位移
// 返回: any 文档信息及新选区, error 读取或导入错误
func (e *businessEngine) importObjects(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := e.bytesFromJS(args[1])
	if err != nil {
		return nil, err
	}
	input, err := ofdgo.NewInput(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	defer input.Close()
	reader, err := ofdgo.NewReader(input)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	objects, annotations, err := e.currentEditor.ImportSelection(context.Background(), reader, args[0].Int(), ofdgo.ObjectImportOptions{DX: args[2].Float(), DY: args[3].Float(), AllowDecrypted: true})
	if err != nil {
		return nil, err
	}
	for _, id := range annotations {
		objects = append(objects, "annotation:"+id)
	}
	info, err := e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
	return editorSelectionInfo{info, objects}, err
}

// copyEditorObjects 复制快照并更新预览，返回新对象标识
// 入参: page 目标页, objects 对象快照, dx 横向位移，单位为毫米, dy 纵向位移，单位为毫米
// 返回: any 文档信息及新选区, error 错误信息
func (e *businessEngine) copyEditorObjects(page int, objects []ofdgo.GraphicObject, dx, dy float64) (any, error) {
	ids, err := e.currentEditor.CopyObjects(page, objects, dx, dy)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return e.editorSummary(), nil
	}
	info, err := e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
	return editorSelectionInfo{info, ids}, err
}

// groupObjects 组合或解组并返回新的选区，复用单次历史提交
// 入参: args 页面索引、对象标识列表和是否解组
// 返回: any 文档及选区, error 错误信息
func (e *businessEngine) groupObjects(args []platformValue) (any, error) {
	var ids []string
	info, err := e.changeObjects(func() error {
		page, selected := args[0].Int(), e.stringsFromJS(args[1])
		var err error
		if args[2].Bool() {
			if len(selected) != 1 {
				return fmt.Errorf("ungrouping requires one object")
			}
			ids, err = e.currentEditor.UngroupObject(page, selected[0])
		} else {
			var id string
			id, err = e.currentEditor.GroupObjects(page, selected)
			ids = []string{id}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return editorSelectionInfo{info.(editorInfo), ids}, nil
}

// orderObjects 调整选区的绘制顺序
// 入参: args 页码、对象标识列表和层级动作
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) orderObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.OrderObjects(args[0].Int(), e.stringsFromJS(args[1]), args[2].String())
	})
}

// distributionAxes 获取画布选区可用的等间距分布方向
// 入参: args 页面索引和画布对象标识数组
// 返回: any 可用方向, error 路径或度量错误
func (e *businessEngine) distributionAxes(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	paths, err := e.editorTargetPaths(e.stringsFromJS(args[1]))
	if err != nil {
		return nil, err
	}
	return e.currentEditor.DistributionAxes(args[0].Int(), paths)
}

// distributeObjects 按指定轴等距分布选区
// 入参: args 页面索引、对象标识数组和分布轴
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) distributeObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.DistributeObjects(args[0].Int(), e.stringsFromJS(args[1]), args[2].String())
	})
}

// deleteObject 删除创作画布中选中的对象
// 入参: args 页码和对象标识
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) deleteObject(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	if err := e.currentEditor.DeleteObject(args[0].Int(), args[1].String()); err != nil {
		return nil, err
	}
	return e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
}

// changePage 管理创作文档的页面
// 入参: args 操作、页面索引及尺寸或目标索引
// 返回: any 文档信息和目标页面, error 错误信息
func (e *businessEngine) changePage(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	index := args[1].Int()
	revision := e.currentEditor.Revision()
	var err error
	switch action := args[0].String(); action {
	case "add":
		index, err = e.currentEditor.AddPage(args[2].Float(), args[3].Float())
	case "copy":
		index, err = e.currentEditor.CopyPage(index)
	case "delete":
		if e.currentEditor.PageCount() == 1 {
			return nil, fmt.Errorf("the document must contain at least one page")
		}
		err = e.currentEditor.DeletePage(index)
		index = min(index, e.currentEditor.PageCount()-1)
	case "move":
		target := args[2].Int()
		err = e.currentEditor.MovePage(index, target)
		index = target
	case "resize":
		err = e.currentEditor.ResizePage(index, args[2].Float(), args[3].Float())
	default:
		return nil, fmt.Errorf("unsupported page action %q", action)
	}
	if err != nil {
		return nil, err
	}
	if e.currentEditor.Revision() == revision {
		return editorPageInfo{e.editorSummary(), index}, nil
	}
	info, err := e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
	if err != nil {
		return nil, err
	}
	return editorPageInfo{info, index}, nil
}

// batchPages 按页码范围批量复制、删除、移动或调整页面尺寸
// 入参: args 操作、页码表达式及插入位置或宽高
// 返回: any 文档信息及目标页, error 错误信息
func (e *businessEngine) batchPages(args []platformValue) (any, error) {
	index := 0
	info, err := e.changeObjects(func() error {
		indexes, err := ofdgo.ParsePageRange(args[1].String(), e.currentEditor.PageCount())
		if err != nil {
			return err
		}
		switch args[0].String() {
		case "copy":
			copies, err := e.currentEditor.CopyPages(indexes)
			if err == nil && len(copies) > 0 {
				index = copies[0]
			}
			return err
		case "delete":
			if len(indexes) == e.currentEditor.PageCount() {
				return fmt.Errorf("the document must contain at least one page")
			}
			if err := e.currentEditor.DeletePages(indexes); err != nil {
				return err
			}
			index = min(indexes[0], e.currentEditor.PageCount()-1)
			return nil
		case "move":
			at := args[2].Int()
			if at < 0 || at > e.currentEditor.PageCount() {
				return fmt.Errorf("page index %d out of range", at)
			}
			index = at
			for _, selected := range indexes {
				if selected < at {
					index--
				}
			}
			return e.currentEditor.MovePages(indexes, index)
		case "resize":
			index = indexes[0]
			return e.currentEditor.ResizePages(indexes, args[2].Float(), args[3].Float())
		default:
			return fmt.Errorf("unsupported page action %q", args[0].String())
		}
	})
	if err != nil {
		return nil, err
	}
	return editorPageInfo{info.(editorInfo), index}, nil
}

// indexesFromJS 读取整数索引列表
// 入参: value JavaScript数组
// 返回: []int 索引列表
func (e *businessEngine) indexesFromJS(value platformValue) []int {
	indexes := make([]int, value.Length())
	for i := range indexes {
		indexes[i] = value.Index(i).Int()
	}
	return indexes
}

// moveOutline 调整目录顺序或层级
// 入参: args 源路径、目标父路径及插入索引
// 返回: any 文档及新路径, error 错误信息
func (e *businessEngine) moveOutline(args []platformValue) (any, error) {
	var path []int
	info, err := e.changeObjects(func() error {
		var err error
		path, err = e.currentEditor.MoveOutline(e.indexesFromJS(args[0]), e.indexesFromJS(args[1]), args[2].Int())
		return err
	})
	if err != nil {
		return nil, err
	}
	return editorOutlineInfo{info.(editorInfo), path}, nil
}

// captureStyle 保存单个对象样式来源，不改写文档
// 入参: args 页面索引、对象标识及可选内部范围
// 返回: any 空结果, error 错误信息
func (e *businessEngine) captureStyle(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	if len(args) > 2 && args[2].String() != "" {
		key := args[2].String()
		path, err := e.compositePath(key)
		if err != nil {
			return nil, err
		}
		indexes, err := e.compositeIndexes(key, []string{args[1].String()})
		if err != nil {
			return nil, err
		}
		member, err := e.currentEditor.CompositeObject(args[0].Int(), path, indexes[0])
		if err != nil {
			return nil, err
		}
		object := member.Style()
		e.copiedStyle = &object
		return nil, nil
	}
	object, err := e.currentEditor.Object(args[0].Int(), args[1].String())
	if err != nil {
		return nil, err
	}
	e.copiedStyle = &object
	return nil, nil
}

// pasteStyle 将保存的样式应用到同类型选区
// 入参: args 页面索引和对象标识列表
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) pasteStyle(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		if e.copiedStyle == nil {
			return fmt.Errorf("style clipboard is empty")
		}
		return e.currentEditor.CopyStyle(args[0].Int(), e.stringsFromJS(args[1]), *e.copiedStyle)
	})
}

// resizeObjects 设置选区位置及尺寸
// 入参: args 页面索引、对象标识、横纵坐标及宽高
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) resizeObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.ResizeObjects(args[0].Int(), e.stringsFromJS(args[1]), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
	})
}

// changeOutline 新增、修改或删除层级目录
// 入参: args 操作、层级索引、标题及目标页面索引
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) changeOutline(args []platformValue) (any, error) {
	path := e.indexesFromJS(args[1])
	info, err := e.changeObjects(func() error {
		switch args[0].String() {
		case "add":
			var err error
			path, err = e.currentEditor.AddOutline(path, args[2].String(), args[3].Int())
			return err
		case "update":
			return e.currentEditor.UpdateOutline(path, args[2].String(), args[3].Int())
		case "delete":
			err := e.currentEditor.DeleteOutline(path)
			path = nil
			return err
		default:
			return fmt.Errorf("unsupported outline action %q", args[0].String())
		}
	})
	if err != nil {
		return nil, err
	}
	return editorOutlineInfo{info.(editorInfo), path}, nil
}

// styleObjects 原子更新对象外观和图案布局
// 入参: args 页面索引、对象标识数组及样式，省略字段保持原值
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) styleObjects(args []platformValue) (any, error) {
	style := e.objectStyle(args[2])
	linkField := args[2].Get("link")
	var link *ofdgo.AnnotationLink
	if !linkField.IsUndefined() && !linkField.IsNull() {
		if err := json.Unmarshal([]byte(linkField.String()), &link); err != nil {
			return nil, err
		}
	}
	var border *ofdgo.ImageBorderStyle
	if field := args[2].Get("borderStyle"); !field.IsUndefined() {
		if err := json.Unmarshal([]byte(field.String()), &border); err != nil {
			return nil, err
		}
	}
	appearance := ofdgo.ObjectAppearance{SetLink: !linkField.IsUndefined(), Link: link, Border: border}
	for i, key := range []string{"fillPatternStyle", "strokePatternStyle"} {
		if field := args[2].Get(key); !field.IsUndefined() {
			var pattern ofdgo.PatternStyle
			if err := json.Unmarshal([]byte(field.String()), &pattern); err != nil {
				return nil, err
			}
			if i == 0 {
				appearance.FillPattern = &pattern
			} else {
				appearance.StrokePattern = &pattern
			}
		}
	}
	if field := args[2].Get("fillPaint"); !field.IsUndefined() {
		if err := json.Unmarshal([]byte(field.String()), &style.FillColor); err != nil {
			return nil, err
		}
	}
	if field := args[2].Get("strokePaint"); !field.IsUndefined() {
		if err := json.Unmarshal([]byte(field.String()), &style.StrokeColor); err != nil {
			return nil, err
		}
	}
	return e.changeObjects(func() error {
		if border != nil {
			if err := e.resolveStylePaint((*ofdgo.FillColor)(border.Color)); err != nil {
				return err
			}
		}
		for _, paint := range []*ofdgo.FillColor{style.FillColor, (*ofdgo.FillColor)(style.StrokeColor)} {
			if err := e.resolveStylePaint(paint); err != nil {
				return err
			}
		}
		appearance.Style = style
		return e.currentEditor.ApplyAppearance(args[0].Int(), e.stringsFromJS(args[1]), appearance)
	})
}

// resolveStylePaint 将渐变色标中的浏览器色值转换为文档颜色空间
// 入参: paint 颜色数据
// 返回: error 错误信息
func (e *businessEngine) resolveStylePaint(paint *ofdgo.FillColor) error {
	if paint == nil {
		return nil
	}
	if strings.HasPrefix(paint.Value, "#") {
		return e.setEditorColor(paint, paint.Value)
	}
	var segments []ofdgo.ShdSegment
	if paint.AxialShd != nil {
		segments = paint.AxialShd.Segment
	} else if paint.RadialShd != nil {
		segments = paint.RadialShd.Segment
	}
	for i := range segments {
		stop := &segments[i].Color
		if strings.HasPrefix(stop.Value, "#") {
			color := ofdgo.FillColor{Alpha: stop.Alpha}
			if err := e.setEditorColor(&color, stop.Value); err != nil {
				return err
			}
			*stop = ofdgo.ShdColor{Value: color.Value, ColorSpace: color.ColorSpace, Alpha: color.Alpha}
		}
	}
	return nil
}

// editorDashPattern 将内部对象的虚线长度格式化为页面毫米
// 入参: value 对象空间虚线长度, scale 页面描边倍率
// 返回: string 页面空间虚线长度，无法解析时保留原文
func (e *businessEngine) editorDashPattern(value string, scale float64) string {
	if scale == 1 || value == "" {
		return value
	}
	fields := strings.Fields(value)
	for i, field := range fields {
		number, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsNaN(number*scale) || math.IsInf(number*scale, 0) {
			return value
		}
		fields[i] = strconv.FormatFloat(number*scale, 'f', -1, 64)
	}
	return strings.Join(fields, " ")
}

// objectStyle 读取高级面板提交的外观字段，省略字段保持原值
// 入参: value 前端样式
// 返回: ofdgo.ObjectStyle 库层样式
func (e *businessEngine) objectStyle(value platformValue) ofdgo.ObjectStyle {
	style := ofdgo.ObjectStyle{}
	if field := value.Get("fill"); !field.IsUndefined() {
		v := field.Bool()
		style.Fill = &v
	}
	if field := value.Get("stroke"); !field.IsUndefined() {
		v := field.Bool()
		style.Stroke = &v
	}
	if field := value.Get("alpha"); !field.IsUndefined() {
		v := field.Int()
		style.Alpha = &v
	}
	if field := value.Get("dashPattern"); !field.IsUndefined() {
		v := field.String()
		style.DashPattern = &v
	}
	if field := value.Get("dashOffset"); !field.IsUndefined() {
		v := field.Float()
		style.DashOffset = &v
	}
	if field := value.Get("cap"); !field.IsUndefined() {
		v := field.String()
		style.Cap = &v
	}
	if field := value.Get("join"); !field.IsUndefined() {
		v := field.String()
		style.Join = &v
	}
	return style
}

// clearImport 释放待导入文档
func (e *businessEngine) clearImport() {
	if e.pendingImport != nil {
		_ = e.pendingImport.Close()
		e.pendingImport = nil
	}
}

// loadImport 加载待导入文件的索引，null参数清除待处理文件
// 入参: args 文档输入或null及可选凭据
// 返回: any 文档列表、页数及签名提示, error 错误信息
func (e *businessEngine) loadImport(args []platformValue) (any, error) {
	e.clearImport()
	if args[0].IsNull() {
		return nil, nil
	}
	input, err := e.inputFromValue(args[0])
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			input.Close()
		}
	}()
	var options []ofdgo.ReaderOption
	options = append(options, ofdgo.WithResources(e.resources))
	if len(args) > 1 && !args[1].IsNull() && !args[1].IsUndefined() {
		credentials, err := e.credentialsFromJS(args[1])
		if err != nil {
			return nil, err
		}
		defer clear(credentials.Password)
		options = append(options, ofdgo.WithCredentials(credentials))
	}
	reader, err := ofdgo.NewReader(input, options...)
	if err != nil {
		return nil, err
	}
	reader.Closer = input
	doc, err := reader.Doc()
	if err != nil {
		reader.Close()
		return nil, err
	}
	if len(doc.Pages.Page) == 0 {
		reader.Close()
		return nil, fmt.Errorf("document has no pages")
	}
	e.pendingImport = reader
	keep = true
	entries := make([]DocumentEntry, reader.DocumentCount())
	signed := doc.Signatures != ""
	for index, body := range reader.OFD.DocBody {
		entries[index] = DocumentEntry{Index: index, DocumentMetadata: DocumentMetadata{Title: body.DocInfo.Title}, Root: body.DocRoot, ID: body.DocInfo.DocID}
		signed = signed || body.Signatures != ""
	}
	return map[string]any{"pageCount": len(doc.Pages.Page), "signed": doc.Signatures != "", "documentsSigned": signed, "encrypted": reader.Encryption().Encrypted, "documents": entries, "documentCount": len(entries)}, nil
}

// importPages 插入指定来源页面并保持目标文档元数据
// 入参: args 页码表达式，空值为全部、目标零基插入位置、是否导入对应目录和可选进度回调
// 返回: any 文档信息及首个插入页, error 错误信息
func (e *businessEngine) importPages(args []platformValue) (any, error) {
	if e.currentEditor == nil || e.pendingImport == nil {
		return nil, fmt.Errorf("no document is ready for page import")
	}
	doc, err := e.pendingImport.Doc()
	if err != nil {
		return nil, err
	}
	var indexes []int
	if value := strings.TrimSpace(args[0].String()); value != "" {
		indexes, err = ofdgo.ParsePageRange(value, len(doc.Pages.Page))
		if err != nil {
			return nil, err
		}
	} else {
		for i := range doc.Pages.Page {
			indexes = append(indexes, i)
		}
	}
	at := args[1].Int()
	options := ofdgo.PageImportOptions{Outlines: len(args) > 2 && args[2].Bool()}
	ctx := e.operationContext()
	progressIndex := 3
	if len(args) > 3 && args[3].Type() == platformTypeBoolean {
		options.AllowDecrypted = args[3].Bool()
		progressIndex = 4
	}
	if len(args) > progressIndex {
		ctx, options.OnProgress = e.newEditorOperationContext(args[progressIndex])
	}
	if _, err := e.currentEditor.ImportPagesContext(ctx, e.pendingImport, indexes, at, options); err != nil {
		return nil, err
	}
	e.clearImport()
	info, err := e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
	return editorPageInfo{info, at}, err
}

// importDocuments 导入指定文档及历史版本并选中首个导入文档
// 入参: args 文档序号表达式、插入位置、解密确认和可选进度回调
// 返回: any 文档信息及当前页, error 导入错误
func (e *businessEngine) importDocuments(args []platformValue) (any, error) {
	if e.currentEditor == nil || e.pendingImport == nil {
		return nil, fmt.Errorf("no document is ready for document import")
	}
	if len(args) < 3 {
		return nil, fmt.Errorf("invalid document import arguments")
	}
	var indexes []int
	var err error
	if value := strings.TrimSpace(args[0].String()); value != "" {
		indexes, err = ofdgo.ParsePageRange(value, e.pendingImport.DocumentCount())
		if err != nil {
			return nil, err
		}
	}
	options := ofdgo.DocumentImportOptions{AllowDecrypted: args[2].Bool()}
	ctx := e.operationContext()
	if len(args) > 3 {
		ctx, options.OnProgress = e.newEditorOperationContext(args[3])
	}
	if _, err := e.currentEditor.ImportDocumentsContext(ctx, e.pendingImport, indexes, args[1].Int(), options); err != nil {
		return nil, err
	}
	e.clearImport()
	info, err := e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
	return editorPageInfo{info, 0}, err
}

// importImages 逐张读取图片并建页，负数位置表示新建文档
// 入参: args 文件名、选项、插入位置、标题、注解设置、读取与进度回调
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) importImages(args []platformValue) (any, error) {
	var options ofdgo.ImagePageOptions
	if err := json.Unmarshal([]byte(platformJSON(args[1])), &options); err != nil {
		return nil, err
	}
	at := args[2].Int()
	editor := e.currentEditor
	if at < 0 {
		editor = ofdgo.NewEditor()
		editor.SetResources(e.resources)
		editor.Info.Title = args[3].String()
	} else if editor == nil {
		return nil, fmt.Errorf("document is not editable")
	}
	if args[0].Length() == 0 {
		return nil, fmt.Errorf("请选择图片")
	}
	sources := make([]ofdgo.ImagePageSource, args[0].Length())
	for i := range sources {
		sources[i] = ofdgo.ImagePageSource{Name: args[0].Index(i).String(), Read: func(context.Context) ([]byte, error) {
			result := args[5].Invoke(i)
			if message := result.Get("error"); message.Type() == platformTypeString {
				return nil, fmt.Errorf("%s", message.String())
			}
			return e.bytesFromJS(result.Get("bytes"))
		}}
	}
	options.OnProgress = e.editorOperationProgress(args[6])
	position := max(at, 0)
	if err := editor.ImportImages(e.operationContext(), sources, position, options); err != nil {
		return nil, err
	}
	if at < 0 {
		editor.SetHistoryLimit(100)
	}
	e.clearImport()
	info, err := e.previewEditor(editor, args[4].Bool())
	return editorPageInfo{info, position}, err
}

// imagePageSize 读取图片建页使用的物理尺寸和方向，不改变文档状态
// 入参: args 图片数据
// 返回: any 图片宽高，单位为毫米, error 错误信息
func (e *businessEngine) imagePageSize(args []platformValue) (any, error) {
	data, err := e.bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	ctx := e.operationContext()
	config, format, err := baseimage.DecodeConfigDataContext(ctx, data)
	if err != nil {
		return nil, err
	}
	metadata, err := baseimage.PageMetadataContext(ctx, data, format, 96)
	if err != nil {
		return nil, err
	}
	width, height := float64(config.Width)*25.4/metadata.XDPI, float64(config.Height)*25.4/metadata.YDPI
	if metadata.Orientation >= 5 {
		width, height = height, width
	}
	return successResult(map[string]any{"width": width, "height": height}), nil
}

// createDocument 新建单页文档
// 入参: args 标题、纸张宽高和注解设置
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) createDocument(args []platformValue) (any, error) {
	e.clearImport()
	editor := ofdgo.NewEditor()
	editor.SetResources(e.resources)
	editor.Info.Title = args[0].String()
	if _, err := editor.AddPage(args[1].Float(), args[2].Float()); err != nil {
		return nil, err
	}
	editor.SetHistoryLimit(100)
	info, err := e.previewEditor(editor, args[3].Bool())
	if err == nil {
		e.clearPDF()
	}
	return info, err
}

// updateInfo 修改完整文档描述，按字段来源保留自定义内容的扩展属性
// 入参: args 文档描述JSON及文档索引，未提交字段保持原值
// 返回: any 编辑状态, error 错误信息
func (e *businessEngine) updateInfo(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		if len(args) != 2 {
			return fmt.Errorf("invalid document info arguments")
		}
		index := args[1].Int()
		info, err := e.currentEditor.DocumentInfo(index)
		if err != nil {
			return err
		}
		var submitted struct {
			ofdgo.DocInfo
			CustomData []struct {
				ofdgo.CustomData
				Source *int
			}
		}
		submitted.DocInfo = info
		if err := json.Unmarshal([]byte(args[0].String()), &submitted); err != nil {
			return err
		}
		var previous []ofdgo.CustomData
		if info.CustomDatas != nil {
			previous = info.CustomDatas.CustomData
		}
		fields := make([]ofdgo.CustomData, len(submitted.CustomData))
		for i, field := range submitted.CustomData {
			if field.Source != nil {
				if *field.Source < 0 || *field.Source >= len(previous) {
					return fmt.Errorf("invalid custom data source")
				}
				fields[i] = previous[*field.Source]
			}
			fields[i].Name, fields[i].Value = field.Name, field.Value
		}
		submitted.CustomDatas = info.CustomDatas
		if submitted.CustomData != nil && !slices.Equal(previous, fields) {
			submitted.CustomDatas = &ofdgo.CustomDatas{CustomData: fields}
		}
		if slices.Equal(info.Keywords, submitted.Keywords) {
			submitted.Keywords = info.Keywords
		}
		return e.currentEditor.SetDocumentInfo(index, submitted.DocInfo)
	})
}

// packageDocuments 按需读取管理列表的元数据和页数，不切换当前文档或解析页面内容
// 入参: args 保留参数
// 返回: any 文档列表, error 编辑状态错误
func (e *businessEngine) packageDocuments(args []platformValue) (any, error) {
	if e.currentSession == nil || e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	entries := make([]DocumentEntry, e.currentEditor.DocumentCount())
	for i := range entries {
		info, err := e.currentEditor.DocumentInfo(i)
		if err != nil {
			return nil, err
		}
		entry := &entries[i]
		body := e.currentSession.Reader.OFD.DocBody[i]
		entry.Index, entry.Root, entry.ID = i, body.DocRoot, info.DocID
		entry.DocumentMetadata = documentMetadata(info)
		if i == e.currentEditor.DocumentIndex() {
			count := e.currentEditor.PageCount()
			entry.PageCount = &count
		} else if count, err := e.currentSession.Reader.DocumentPageCount(i); err == nil {
			entry.PageCount = &count
		}
	}
	return entries, nil
}

// documentVersions 读取当前文档的版本列表，包含尚未保存的编辑内容
// 入参: args 保留参数
// 返回: any 版本列表, error 读取错误
func (e *businessEngine) documentVersions(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	reader := e.currentSession.Reader
	if e.currentEditor != nil {
		var err error
		reader, err = e.currentEditor.Reader(e.operationContext())
		if err != nil {
			return nil, err
		}
	}
	versions, err := reader.DocumentVersionsContext(e.operationContext())
	if err != nil {
		return nil, err
	}
	entries := make([]VersionEntry, 0, len(versions)+1)
	hasMain := reader.OFD.DocBody[reader.DocumentIndex()].DocRoot != ""
	if hasMain {
		entries = append(entries, VersionEntry{Default: true})
	}
	for _, version := range versions {
		entries = append(entries, VersionEntry{ID: version.ID, Index: version.Index, Name: version.Name, Default: version.Current})
		if version.Current && hasMain {
			entries[0].Default = false
		}
	}
	return entries, nil
}

// styleResource 读取当前文档中的颜色空间或共享绘制参数
// 入参: args 类型和资源标识
// 返回: any 资源定义, error 读取错误
func (e *businessEngine) styleResource(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 2 {
		return nil, fmt.Errorf("invalid style resource")
	}
	ctx := e.operationContext()
	switch args[0].String() {
	case "ColorSpace":
		return e.currentSession.Reader.ColorSpace(ctx, args[1].String())
	case "DrawParam":
		return e.currentSession.Reader.DrawParam(ctx, args[1].String(), false)
	default:
		return nil, fmt.Errorf("invalid style resource kind")
	}
}

// changeStyleResource 注册或替换共享样式资源，未重新选择ICC时保留既有配置
// 入参: args 类型、标识、定义JSON、可选ICC字节和RGB颜色增量JSON
// 返回: any 编辑状态, error 校验或提交错误
func (e *businessEngine) changeStyleResource(args []platformValue) (any, error) {
	if len(args) != 5 {
		return nil, fmt.Errorf("invalid style resource arguments")
	}
	return e.changeAtomicObjects(func() error {
		ctx := e.operationContext()
		kind, id := args[0].String(), args[1].String()
		if kind == "ColorSpace" {
			var space ofdgo.ColorSpace
			if err := json.Unmarshal([]byte(args[2].String()), &space); err != nil {
				return err
			}
			var profile []byte
			var err error
			if !args[3].IsNull() && !args[3].IsUndefined() {
				profile, err = e.bytesFromJS(args[3])
			} else if id != "" {
				var original *ofdgo.ColorSpace
				original, err = e.currentEditor.ColorSpace(id)
				if err == nil && original.Profile != "" {
					var stream io.ReadCloser
					stream, err = e.currentSession.Reader.OpenResource(ctx, kind, id)
					if err == nil {
						defer stream.Close()
						profile, err = io.ReadAll(stream)
					}
				}
			}
			if err != nil {
				return err
			}
			if id == "" {
				_, err = e.currentEditor.ImportColorSpace(ctx, space, profile)
				return err
			}
			return e.currentEditor.ReplaceColorSpace(ctx, id, space, profile)
		}
		if kind != "DrawParam" {
			return fmt.Errorf("invalid style resource kind")
		}
		var draw ofdgo.DrawParam
		var colors map[string]string
		if err := json.Unmarshal([]byte(args[2].String()), &draw); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(args[4].String()), &colors); err != nil {
			return err
		}
		for key, value := range colors {
			paint := &ofdgo.FillColor{}
			if key == "FillColor" && draw.FillColor != nil {
				paint.Alpha = draw.FillColor.Alpha
			} else if key == "StrokeColor" && draw.StrokeColor != nil {
				paint.Alpha = draw.StrokeColor.Alpha
			}
			if err := e.setEditorColor(paint, value); err != nil {
				return err
			}
			if key == "FillColor" {
				draw.FillColor = paint
			} else if key == "StrokeColor" {
				draw.StrokeColor = (*ofdgo.StrokeColor)(paint)
			} else {
				return fmt.Errorf("invalid paint target")
			}
		}
		if id == "" {
			_, err := e.currentEditor.ImportDrawParam(ctx, draw)
			return err
		}
		return e.currentEditor.ReplaceDrawParam(ctx, id, draw)
	})
}

// applyStyleResource 为指定图元设置共享绘制参数或颜色，库层负责颜色及对象校验
// 入参: args 类型、资源标识、页码、图元标识及颜色JSON
// 返回: any 编辑状态, error 校验或提交错误
func (e *businessEngine) applyStyleResource(args []platformValue) (any, error) {
	if len(args) != 5 {
		return nil, fmt.Errorf("invalid style application")
	}
	return e.changeAtomicObjects(func() error {
		page, ids := args[2].Int(), []string{args[3].String()}
		if args[0].String() == "DrawParam" {
			return e.currentEditor.SetObjectDrawParam(page, ids, args[1].String())
		}
		if args[0].String() != "ColorSpace" || args[1].String() == "" {
			return fmt.Errorf("invalid color space")
		}
		var paint struct{ Target, Mode, Value string }
		if err := json.Unmarshal([]byte(args[4].String()), &paint); err != nil {
			return err
		}
		fill := &ofdgo.FillColor{ColorSpace: args[1].String()}
		if paint.Mode == "index" {
			index, err := strconv.Atoi(paint.Value)
			if err != nil {
				return err
			}
			fill.Index = &index
		} else if paint.Mode == "value" {
			fill.Value = paint.Value
		} else {
			return fmt.Errorf("invalid color mode")
		}
		style := ofdgo.ObjectStyle{}
		if paint.Target == "fill" {
			style.FillColor = fill
		} else if paint.Target == "stroke" {
			style.StrokeColor = (*ofdgo.StrokeColor)(fill)
		} else {
			return fmt.Errorf("invalid paint target")
		}
		return e.currentEditor.StyleObjects(page, ids, style)
	})
}

// pageSettings 获取当前页设置，图层视图跳过全部普通页模板引用查询，不修改文档
// 入参: args 页面索引及可选类型layers或templates，省略类型时返回全量数据
// 返回: any 页面设置, error 读取错误
func (e *businessEngine) pageSettings(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) < 1 || len(args) > 2 {
		return nil, fmt.Errorf("invalid page settings")
	}
	kind := "templates"
	if len(args) == 2 {
		kind = args[1].String()
		if kind != "layers" && kind != "templates" {
			return nil, fmt.Errorf("invalid page settings type %q", kind)
		}
	}
	reader := e.currentSession.Reader
	layers, err := reader.Layers(args[0].Int())
	if err != nil {
		return nil, err
	}
	templates, err := reader.Templates()
	if err != nil {
		return nil, err
	}
	page, err := reader.PageContentByIndex(args[0].Int())
	if err != nil {
		return nil, err
	}
	references := []ofdgo.TemplateReference{}
	if kind == "templates" {
		references, err = reader.TemplateReferences()
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"layers": layers, "templates": templates, "bindings": page.Template, "templateReferences": references}, nil
}

// changeLayer 调用图层管理接口，组合修改使用同一次事务
// 入参: args 操作、页码、图层标识、类型、绘制参数、顺序及图元标识
// 返回: any 编辑状态, error 校验或提交错误
func (e *businessEngine) changeLayer(args []platformValue) (any, error) {
	if len(args) != 7 {
		return nil, fmt.Errorf("invalid layer arguments")
	}
	return e.changeAtomicObjects(func() error {
		page, id := args[1].Int(), args[2].String()
		switch args[0].String() {
		case "add":
			_, err := e.currentEditor.AddLayer(page, args[3].String(), args[4].String())
			return err
		case "update":
			return e.currentEditor.Transaction(func(editor *ofdgo.Editor) error {
				if err := editor.SetLayerType(page, id, args[3].String()); err != nil {
					return err
				}
				return editor.SetLayerDrawParam(page, id, args[4].String())
			})
		case "delete":
			return e.currentEditor.DeleteLayer(page, id)
		case "move":
			return e.currentEditor.MoveLayer(page, id, args[5].Int())
		case "objects":
			return e.currentEditor.MoveObjectsToLayer(page, e.stringsFromJS(args[6]), id)
		default:
			return fmt.Errorf("invalid layer operation")
		}
	})
}

// changeTemplate 调用模板及页面绑定接口，组合修改使用同一次事务
// 入参: args 操作、页码、模板标识、名称、层次及顺序
// 返回: any 编辑状态, error 校验或提交错误
func (e *businessEngine) changeTemplate(args []platformValue) (any, error) {
	if len(args) != 6 {
		return nil, fmt.Errorf("invalid template arguments")
	}
	return e.changeAtomicObjects(func() error {
		page, id, order := args[1].Int(), args[2].String(), args[4].String()
		switch args[0].String() {
		case "add":
			_, err := e.currentEditor.CreateTemplateFromPage(page, args[3].String(), order)
			return err
		case "update":
			return e.currentEditor.Transaction(func(editor *ofdgo.Editor) error {
				if err := editor.RenameTemplate(id, args[3].String()); err != nil {
					return err
				}
				return editor.SetTemplateZOrder(id, order)
			})
		case "replace":
			return e.currentEditor.ReplaceTemplateFromPage(id, page)
		case "delete":
			return e.currentEditor.DeleteTemplate(id)
		case "bind":
			return e.currentEditor.BindTemplate(page, id, order)
		case "unbind":
			return e.currentEditor.UnbindTemplate(page, id)
		case "order":
			return e.currentEditor.SetPageTemplateZOrder(page, id, order)
		case "move":
			return e.currentEditor.MovePageTemplate(page, id, args[5].Int())
		default:
			return fmt.Errorf("invalid template operation")
		}
	})
}

// documentNavigation 按需读取命名书签或通用扩展，不解释业务数据
// 入参: args 设置类型
// 返回: any 文档导航和扩展信息, error 读取错误
func (e *businessEngine) documentNavigation(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 1 {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	switch args[0].String() {
	case "bookmarks", "actions":
		bookmarks, err := e.currentSession.Reader.Bookmarks()
		return map[string]any{"bookmarks": bookmarks}, err
	case "extensions":
		extensions, err := e.currentSession.Reader.Extensions()
		return map[string]any{"extensions": extensions}, err
	default:
		return nil, fmt.Errorf("invalid navigation kind")
	}
}

// changeBookmark 调用库层书签操作，引用更新和版本隔离由编辑器完成
// 入参: args 操作、原名称及书签JSON
// 返回: any 编辑状态, error 校验或提交错误
func (e *businessEngine) changeBookmark(args []platformValue) (any, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf("invalid bookmark arguments")
	}
	var bookmark ofdgo.Bookmark
	if err := json.Unmarshal([]byte(args[2].String()), &bookmark); err != nil {
		return nil, err
	}
	return e.changeAtomicObjects(func() error {
		switch args[0].String() {
		case "add":
			return e.currentEditor.AddBookmark(bookmark.Name, bookmark.Dest)
		case "update":
			return e.currentEditor.UpdateBookmark(args[1].String(), bookmark)
		case "delete":
			return e.currentEditor.DeleteBookmark(args[1].String())
		default:
			return fmt.Errorf("invalid bookmark operation")
		}
	})
}

// actionList 获取编辑对象的动作序列，返回独立数据，不触发动作
// 入参: args 归属、页码、图元标识和目录路径
// 返回: any 动作列表, error 归属或读取错误
func (e *businessEngine) actionList(args []platformValue) (any, error) {
	if e.currentEditor == nil || len(args) != 4 {
		return nil, fmt.Errorf("invalid action selection")
	}
	switch args[0].String() {
	case "document":
		return e.currentEditor.DocumentActions(), nil
	case "page":
		return e.currentEditor.PageActions(args[1].Int())
	case "object":
		return e.currentEditor.ObjectActions(args[1].Int(), args[2].String())
	case "outline":
		return e.currentEditor.OutlineActions(e.indexesFromJS(args[3]))
	default:
		return nil, fmt.Errorf("invalid action scope")
	}
}

// changeActions 将动作列表交给库层校验和原子提交
// 入参: args 归属、页码、图元标识、目录路径及动作JSON
// 返回: any 编辑状态, error 校验或提交错误
func (e *businessEngine) changeActions(args []platformValue) (any, error) {
	if len(args) != 5 {
		return nil, fmt.Errorf("invalid action arguments")
	}
	var actions []ofdgo.Action
	if err := json.Unmarshal([]byte(args[4].String()), &actions); err != nil {
		return nil, err
	}
	return e.changeAtomicObjects(func() error {
		if err := e.currentEditor.ValidateActionTargets(e.operationContext(), actions); err != nil {
			return err
		}
		switch args[0].String() {
		case "document":
			return e.currentEditor.SetDocumentActions(actions)
		case "page":
			return e.currentEditor.SetPageActions(args[1].Int(), actions)
		case "object":
			return e.currentEditor.SetObjectActions(args[1].Int(), []string{args[2].String()}, actions)
		case "outline":
			return e.currentEditor.SetOutlineActions(e.indexesFromJS(args[3]), actions)
		default:
			return fmt.Errorf("invalid action scope")
		}
	})
}

// annotationInfos 读取当前文档及版本的注释摘要，不解析页面图元
// 入参: args 保留参数
// 返回: any 注释摘要, error 读取错误
func (e *businessEngine) annotationInfos(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return e.currentSession.Reader.AnnotationInfos()
}

// exportVersion 提取指定历史版本并另存，不切换当前视图或修改编辑历史
// 入参: args 版本标识、写出回调、进度回调及取消检查
// 返回: any 保存结果, error 提取或写入错误
func (e *businessEngine) exportVersion(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 4 || args[0].Type() != platformTypeString || args[0].String() == "" {
		return nil, fmt.Errorf("invalid version export")
	}
	ctx := e.newOutputContext(args[3])
	reader := e.currentSession.Reader
	if e.currentEditor != nil {
		var err error
		reader, err = e.currentEditor.Reader(ctx)
		if err != nil {
			return nil, err
		}
	}
	version, err := reader.ExtractVersionContext(ctx, args[0].String())
	if err != nil {
		return nil, err
	}
	defer version.Close()
	editor, err := version.Editor()
	if err != nil {
		return nil, err
	}
	return e.saveEditor(editor, []platformValue{args[1], args[2], platformNull(), platformValueOf(map[string]any{"mode": 0}), args[3]})
}

// selectDocumentVersion 切换阅读版本，已有编辑器时保留修改及历史，否则直接创建阅读视图
// 入参: args 版本标识
// 返回: any 文档信息, error 切换错误
func (e *businessEngine) selectDocumentVersion(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 1 || args[0].Type() != platformTypeString {
		return nil, fmt.Errorf("invalid version selection")
	}
	id := args[0].String()
	if e.currentEditor != nil {
		return e.changeAtomicObjects(func() error { return e.currentEditor.SelectVersion(id) })
	}
	session, err := e.currentSession.DocumentVersion(id)
	if err != nil {
		return nil, err
	}
	session.input, e.currentSession.input = e.currentSession.input, nil
	_ = e.currentSession.Close()
	e.currentSession = session
	e.clearImport()
	e.copiedObjects, e.copiedStyle = nil, nil
	return session.Summary(), nil
}

// changeDocumentVersion 调用库层版本操作，沿用编辑历史和预览更新流程
// 入参: args 操作、版本标识及可选名称
// 返回: any 文档信息, error 操作错误
func (e *businessEngine) changeDocumentVersion(args []platformValue) (any, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf("invalid version arguments")
	}
	return e.changeAtomicObjects(func() error {
		id, name := args[1].String(), args[2].String()
		switch args[0].String() {
		case "add":
			return e.currentEditor.Transaction(func(editor *ofdgo.Editor) error {
				if err := editor.SelectVersion(id); err != nil {
					return err
				}
				_, err := editor.AddVersion(name)
				return err
			})
		case "rename":
			return e.currentEditor.RenameVersion(id, name)
		case "delete":
			return e.currentEditor.DeleteVersion(id)
		case "default":
			return e.currentEditor.SetDefaultVersion(id)
		case "select":
			return e.currentEditor.SelectVersion(id)
		default:
			return fmt.Errorf("invalid version operation")
		}
	})
}

// changeAttachment 编辑附件，逐个读取文件并复用库层事务和历史
// 入参: args 操作、附件标识、名称列表、文件读取回调
// 返回: any 编辑状态, error 错误信息
func (e *businessEngine) changeAttachment(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		action, id := args[0].String(), args[1].String()
		switch action {
		case "rename":
			return e.currentEditor.RenameAttachment(id, args[2].Index(0).String())
		case "delete":
			return e.currentEditor.DeleteAttachment(id)
		case "add", "replace":
			count := args[2].Length()
			if count == 0 || action == "replace" && count != 1 {
				return fmt.Errorf("请选择附件")
			}
			for i := range count {
				result := args[3].Invoke(i)
				if message := result.Get("error"); message.Type() == platformTypeString {
					return fmt.Errorf("%s", message.String())
				}
				data, err := e.bytesFromJS(result.Get("bytes"))
				if err != nil {
					return err
				}
				if action == "replace" {
					return e.currentEditor.ReplaceAttachment(id, data)
				}
				if _, err := e.currentEditor.AddAttachment(args[2].Index(i).String(), data); err != nil {
					return err
				}
			}
			return nil
		default:
			return fmt.Errorf("unsupported attachment action %q", action)
		}
	})
}

// changeSignature 移除当前文档签名，使用库层依赖检查和撤销历史
// 入参: args 操作及签名标识
// 返回: any 编辑状态, error 移除错误
func (e *businessEngine) changeSignature(args []platformValue) (any, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("签名参数无效")
	}
	return e.changeAtomicObjects(func() error {
		switch args[0].String() {
		case "remove":
			return e.currentEditor.RemoveSignatureContext(e.operationContext(), args[1].String())
		case "clear":
			return e.currentEditor.ClearSignaturesContext(e.operationContext())
		default:
			return fmt.Errorf("签名操作无效")
		}
	})
}

// changeFontBinding 调用库层字体资源操作，统一更新预览与撤销状态
// 入参: args 字体标识、保存方式及可选字体数据
// 返回: any 编辑状态, error 字体处理错误
func (e *businessEngine) changeFontBinding(args []platformValue) (any, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("请选择字体")
	}
	return e.changeAtomicObjects(func() error {
		id, mode := args[0].String(), args[1].Int()
		if mode == int(ofdgo.FontExternal) {
			return e.currentEditor.ExternalizeFont(e.operationContext(), id)
		}
		if mode != int(ofdgo.FontEmbed) {
			return fmt.Errorf("字体方式无效")
		}
		var file ofdgo.FontFile
		if len(args) > 2 && !args[2].IsNull() && !args[2].IsUndefined() {
			data, err := e.bytesFromJS(args[2])
			if err != nil {
				return err
			}
			file.Data = data
		}
		return e.currentEditor.EmbedFont(e.operationContext(), id, file, 0)
	})
}

// changePackageDocument 调用库层文档增删、改名或排序，不在浏览器内重写包结构
// 入参: args 操作及标题、页面尺寸、文档索引或移动位置
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) changePackageDocument(args []platformValue) (any, error) {
	if e.currentEditor == nil || len(args) == 0 {
		return nil, fmt.Errorf("no document is being edited")
	}
	return e.changeAtomicObjects(func() error {
		var err error
		switch args[0].String() {
		case "add":
			if len(args) != 5 {
				return fmt.Errorf("invalid document arguments")
			}
			_, err = e.currentEditor.AddDocument(args[1].String(), args[2].Float(), args[3].Float(), args[4].Int())
		case "delete":
			if len(args) != 2 {
				return fmt.Errorf("invalid document arguments")
			}
			err = e.currentEditor.DeleteDocument(args[1].Int())
		case "move":
			if len(args) != 3 {
				return fmt.Errorf("invalid document arguments")
			}
			err = e.currentEditor.MoveDocument(args[1].Int(), args[2].Int())
		case "rename":
			if len(args) != 3 {
				return fmt.Errorf("invalid document arguments")
			}
			err = e.currentEditor.RenameDocument(args[1].Int(), args[2].String())
		default:
			return fmt.Errorf("invalid document operation")
		}
		if err == nil {
			e.copiedObjects, e.copiedStyle = nil, nil
			e.clearImport()
		}
		return err
	})
}

// selectDocument 切换包内文档，保留编辑快照并清除跨文档剪贴板
// 入参: args 文档索引
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) selectDocument(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) == 0 {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	session, err := e.currentSession.Document(args[0].Int())
	if err != nil {
		return nil, err
	}
	session.input, e.currentSession.input = e.currentSession.input, nil
	_ = e.currentSession.Close()
	e.currentSession, e.currentEditor = session, session.editor
	if e.pendingImport != nil {
		_ = e.pendingImport.Close()
		e.pendingImport = nil
	}
	e.copiedObjects, e.copiedStyle = nil, nil
	return e.editorSummary(), nil
}

// editDocument 将已打开文档接入编辑器，沿用页面、资源及字体配置
// 入参: args 当前页索引，省略时使用首页
// 返回: any 编辑状态, error 错误信息
func (e *businessEngine) editDocument(args []platformValue) (any, error) {
	if e.currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if e.currentEditor != nil {
		e.currentSession.editing = true
		return e.editorSummary(), nil
	}
	ctx := e.operationContext()
	editor, err := e.currentSession.Reader.EditorContext(ctx)
	if err != nil {
		return nil, err
	}
	editor.SetHistoryLimit(100)
	if e.currentSession.fontFS != nil {
		editor.SetFontFS(e.currentSession.fontFS)
	}
	editor.SetRenderBackends(e.currentSession.Renderer.Backends())
	page := 0
	if len(args) != 0 {
		page = args[0].Int()
	}
	content, err := editor.PageContext(ctx, page)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.syncEditorPageIDs(page, content); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.currentEditor = editor
	e.currentSession.editor = editor
	e.currentSession.editing = true
	e.copiedObjects = nil
	e.copiedStyle = nil
	return e.editorSummary(), nil
}

// syncEditorPageIDs 同步编辑副本的对象编号，不改变原渲染属性或丢弃正常页面缓存
// 入参: index 页面索引, content 编辑页面
// 返回: error 页面读取错误
func (e *businessEngine) syncEditorPageIDs(index int, content *ofdgo.PageContent) error {
	page, err := e.currentSession.pageContent(index)
	if err != nil {
		return err
	}
	for i, layer := range content.Content.Layer {
		for j, object := range layer.Objects {
			before := &page.Content.Layer[i].Objects[j]
			if before.TextObject.ID != object.TextObject.ID || before.PathObject.ID != object.PathObject.ID || before.ImageObject.ID != object.ImageObject.ID || before.CompositeGraphicUnit.ID != object.CompositeGraphicUnit.ID {
				before.TextObject.ID = object.TextObject.ID
				before.PathObject.ID = object.PathObject.ID
				before.ImageObject.ID = object.ImageObject.ID
				before.CompositeGraphicUnit.ID = object.CompositeGraphicUnit.ID
				e.currentSession.textCache.Delete(index)
			}
		}
	}
	return nil
}

// previewEditor 通过内存快照更新预览，不生成中间压缩包
// 入参: editor 编辑文档, annotations 是否显示注解
// 返回: editorInfo 文档信息, error 错误信息
func (e *businessEngine) previewEditor(editor *ofdgo.Editor, annotations bool) (editorInfo, error) {
	reader, err := editor.Reader(e.operationContext())
	if err != nil {
		return editorInfo{}, err
	}
	session, err := newSession(reader, OpenOptions{RenderAnnotations: annotations})
	if err != nil {
		return editorInfo{}, err
	}
	editor.SetFontFS()
	if e.currentSession != nil {
		previous := e.currentSession.Reader
		oldBody, newBody := previous.OFD.DocBody[previous.DocumentIndex()], reader.OFD.DocBody[reader.DocumentIndex()]
		oldVersion, _ := previous.DocumentVersionID()
		newVersion, _ := reader.DocumentVersionID()
		if oldBody.DocRoot != newBody.DocRoot || oldBody.DocInfo.DocID != newBody.DocInfo.DocID || oldVersion != newVersion {
			e.copiedObjects, e.copiedStyle = nil, nil
			e.clearImport()
		}
		ofdgo.WithRenderBackends(e.currentSession.Renderer.Backends())(session.Renderer)
		if e.currentSession.fontFS != nil {
			session.fontFS = e.currentSession.fontFS
			session.Renderer.SetFontFS(session.fontFS)
			editor.SetFontFS(session.fontFS)
		}
		changes := editor.Changes()
		if editor == e.currentEditor && !changes.All && len(session.boxCache) == len(e.currentSession.boxCache) {
			changed := changes.Pages
			for index, page := range e.currentSession.pageCache {
				if !slices.Contains(changed, index) {
					session.pageCache[index] = page
				}
			}
			e.currentSession.textCache.Range(func(index int, text *ofdgo.PageText) bool {
				if !slices.Contains(changed, index) {
					session.cachePageText(index, text)
				}
				return true
			})
			for index, box := range e.currentSession.boxCache {
				if !slices.Contains(changed, index) {
					session.boxCache[index] = box
				}
			}
		}
		if editor == e.currentEditor {
			session.input, e.currentSession.input = e.currentSession.input, nil
		}
		_ = e.currentSession.Close()
	}
	editor.SetRenderBackends(session.Renderer.Backends())
	e.currentSession = session
	session.editor = editor
	session.editing = true
	if e.currentEditor != editor {
		e.copiedObjects = nil
		e.copiedStyle = nil
	}
	e.currentEditor = editor
	return e.editorSummary(), nil
}

// setEditorColor 写入当前文档的RGB纯色，保留透明度并替换原颜色空间和索引
// 入参: fill 颜色, value 十六进制色值
// 返回: error 错误信息
func (e *businessEngine) setEditorColor(fill *ofdgo.FillColor, value string) error {
	if len(value) != 7 || value[0] != '#' {
		return fmt.Errorf("invalid RGB color %q", value)
	}
	rgb, err := hex.DecodeString(value[1:])
	if err != nil {
		return err
	}
	converted, err := e.currentEditor.RGBColor(color.NRGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 255})
	if err != nil {
		return err
	}
	converted.Alpha = fill.Alpha
	*fill = *converted
	return nil
}

// editorColorHex 将当前文档纯色转换为浏览器色值
// 入参: value 颜色
// 返回: string 十六进制色值
func (e *businessEngine) editorColorHex(value *ofdgo.FillColor) string {
	converted, err := e.currentEditor.Color(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", converted.R, converted.G, converted.B)
}

// editorPathScale 获取路径线宽的页面缩放比例
// 入参: object 路径对象
// 返回: float64 缩放比例
func (e *businessEngine) editorPathScale(object ofdgo.PathObject) float64 {
	m := ofdgo.NewMatrix(object.CTM)
	x, y := m.Transform(0, 0)
	ax, ay := m.Transform(1, 0)
	bx, by := m.Transform(0, 1)
	return math.Sqrt(math.Abs((ax-x)*(by-y) - (ay-y)*(bx-x)))
}

// setPathStyle 应用图形填充、描边和线宽
// 入参: object 路径对象, args 填充开关及色值、描边开关及色值、线宽，null保持原值
// 返回: error 错误信息
func (e *businessEngine) setPathStyle(object *ofdgo.PathObject, args []platformValue) error {
	fill, stroke := object.Fill != nil && *object.Fill, object.Stroke == nil || *object.Stroke
	if !args[0].IsNull() {
		fill = args[0].Bool()
	}
	if !args[2].IsNull() {
		stroke = args[2].Bool()
	}
	if kind, _ := object.Shape(); kind == ofdgo.ShapeLine || kind == ofdgo.ShapeArrow || kind == ofdgo.ShapeDoubleArrow {
		fill, stroke = false, true
	}
	if !fill && !stroke {
		if args[0].IsNull() || args[2].IsNull() {
			stroke = true
		} else {
			return fmt.Errorf("fill or stroke must be enabled")
		}
	}
	if !args[0].IsNull() || fill != (object.Fill != nil && *object.Fill) {
		object.Fill = &fill
	}
	if !args[2].IsNull() || stroke != (object.Stroke == nil || *object.Stroke) {
		object.Stroke = &stroke
	}
	if !args[4].IsNull() {
		width := args[4].Float()
		if width < 0 || math.IsNaN(width) || math.IsInf(width, 0) {
			return fmt.Errorf("line width must be nonnegative and finite")
		}
		if scale := e.editorPathScale(*object); width != object.LineWidth*scale {
			object.LineWidth = width / scale
		}
		object.LineWidthSet = width == 0
	}
	if !args[1].IsNull() {
		if object.FillColor == nil {
			object.FillColor = &ofdgo.FillColor{}
		}
		if err := e.setEditorColor(object.FillColor, args[1].String()); err != nil {
			return err
		}
	}
	if !args[3].IsNull() {
		if object.StrokeColor == nil {
			object.StrokeColor = &ofdgo.StrokeColor{}
		}
		if err := e.setEditorColor((*ofdgo.FillColor)(object.StrokeColor), args[3].String()); err != nil {
			return err
		}
	}
	return nil
}

// insertShape 创建标准路径图形
// 入参: args 页码、图形类型、范围和绘制样式
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) insertShape(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	path, err := ofdgo.NewShape(ofdgo.ShapeKind(args[1].String()), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
	if err != nil {
		return nil, err
	}
	if err := e.setPathStyle(&path, args[6:]); err != nil {
		return nil, err
	}
	return e.insertEditorObject(args[0].Int(), ofdgo.GraphicObject{Type: "PathObject", PathObject: path}, args[11:])
}

// updatePathStyle 原子更新选中路径的绘制样式，保留未修改属性
// 入参: args 页码、对象标识列表和绘制样式
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) updatePathStyle(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		page, ids := args[0].Int(), e.stringsFromJS(args[1])
		objects := make([]ofdgo.GraphicObject, len(ids))
		for i, id := range ids {
			object, err := e.currentEditor.Object(page, id)
			if err != nil {
				return err
			}
			if object.Type != "PathObject" {
				return fmt.Errorf("object is not a path")
			}
			if err := e.setPathStyle(&object.PathObject, args[2:]); err != nil {
				return err
			}
			objects[i] = object
		}
		return e.currentEditor.UpdateObjects(page, objects)
	})
}

// editorBox 将库层毫米边界传递给画布交互层
// 入参: box 毫米坐标中的矩形范围
// 返回: map[string]any 可直接传递给JavaScript的横纵坐标和宽高
func (e *businessEngine) editorBox(box ofdgo.Box) map[string]any {
	return map[string]any{"x": box.X, "y": box.Y, "width": box.W, "height": box.H}
}

// editorMatrix 将标准CTM转换为浏览器仿射矩阵分量
// 入参: matrix 库层仿射矩阵
// 返回: []any 可直接传递给JavaScript的a、b、c、d、e、f分量，位移单位为毫米
func (e *businessEngine) editorMatrix(matrix ofdgo.Matrix) []any {
	x, y := matrix.Transform(0, 0)
	ax, ay := matrix.Transform(1, 0)
	bx, by := matrix.Transform(0, 1)
	return []any{ax - x, ay - y, bx - x, by - y, x, y}
}

// rotateObjects 旋转同页选区，通用几何与历史记录由库负责
// 入参: args 页面索引、对象标识数组和顺时针角度
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) rotateObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.RotateObjects(args[0].Int(), e.stringsFromJS(args[1]), args[2].Int())
	})
}

// flipObjects 镜像同页选区
// 入参: args 页面索引、对象标识数组和镜像轴
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) flipObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.FlipObjects(args[0].Int(), e.stringsFromJS(args[1]), args[2].String())
	})
}

// cropImage 设置图片裁剪范围，保留原始图片数据
// 入参: args 页面索引、对象标识及页面毫米坐标中的横纵坐标和宽高
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) cropImage(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.CropImage(args[0].Int(), args[1].String(), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
	})
}

// resetImageCrop 还原跨范围复制图片的会话裁剪，不移除原始及父级裁剪
// 入参: args 页面索引及对象标识
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) resetImageCrop(args []platformValue) (any, error) {
	return e.changeObjects(func() error { return e.currentEditor.ResetImageCrop(args[0].Int(), args[1].String()) })
}

// fitImage 按原始比例适应或填充图片框
// 入参: args 页面索引、对象标识和适应方式
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) fitImage(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.FitImage(args[0].Int(), args[1].String(), args[2].String())
	})
}

// previewImage 在页面副本中显示指定图片的完整内容，不改变正文、历史或缓存
// 入参: args 页面索引和图片对象标识
// 返回: any 页面SVG和分离的图片资源, error 错误信息
func (e *businessEngine) previewImage(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, err := e.currentEditor.Page(args[0].Int())
	if err != nil {
		return nil, err
	}
	id := args[1].String()
	found := false
	for i := range page.Content.Layer {
		for j := range page.Content.Layer[i].Objects {
			object := &page.Content.Layer[i].Objects[j]
			if object.Type == "ImageObject" && object.ImageObject.ID == id {
				object.ImageObject.Clips = nil
				found = true
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("image object %q not found", id)
	}
	var out bytes.Buffer
	resources, err := e.currentSession.Renderer.RenderToSVGWithResources(page, &out)
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"svg": out.String(), "images": e.svgImagesToJS(resources.Images)}), nil
}

// svgImagesToJS 将图片资源以二进制数组传给前端，不使用base64
// 入参: images SVG引用的图片资源
// 返回: []any 图片标识、类型和数据
func (e *businessEngine) svgImagesToJS(images []ofdgo.SVGImage) []any {
	items := make([]any, len(images))
	for i, image := range images {
		items[i] = map[string]any{"name": image.Name, "mime": image.MIME, "bytes": bytesToJS(image.Data)}
	}
	return items
}

// transformObjects 统一移动或缩放选区，生成一次撤销记录
// 入参: args 页面索引、对象标识数组、横纵位移和正缩放比例
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) transformObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.TransformObjects(args[0].Int(), e.stringsFromJS(args[1]), args[2].Float(), args[3].Float(), args[4].Float())
	})
}

// alignObjects 对齐选区中的对象
// 入参: args 页面索引、对象标识数组和对齐方式
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) alignObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.AlignObjects(args[0].Int(), e.stringsFromJS(args[1]), args[2].String())
	})
}

// deleteObjects 一次删除选区中的对象
// 入参: args 页面索引和对象标识数组
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) deleteObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.DeleteObjects(args[0].Int(), e.stringsFromJS(args[1]))
	})
}

// eraseObjectsPath 按页面闭合折线范围擦除对象，保留局部裁剪之外的内容
// 入参: args 页面索引、对象标识数组和坐标点数组
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) eraseObjectsPath(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.EraseObjectsPath(args[0].Int(), e.stringsFromJS(args[1]), e.pointsFromJS(args[2]))
	})
}

// pointsFromJS 读取前端页面坐标点数组
// 入参: value 坐标点数组
// 返回: []ofdgo.Point 页面毫米坐标
func (e *businessEngine) pointsFromJS(value platformValue) []ofdgo.Point {
	points := make([]ofdgo.Point, value.Length())
	for i := range points {
		point := value.Index(i)
		points[i] = ofdgo.Point{X: point.Get("x").Float(), Y: point.Get("y").Float()}
	}
	return points
}

// eraseObjects 按页面矩形范围擦除对象，保留局部裁剪之外的内容
// 入参: args 页面索引、对象标识数组和毫米坐标范围
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) eraseObjects(args []platformValue) (any, error) {
	return e.changeObjects(func() error {
		return e.currentEditor.EraseObjects(args[0].Int(), e.stringsFromJS(args[1]), ofdgo.Box{
			X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float(),
		})
	})
}

// changeObjects 提交库层批量操作，无修改时复用当前预览
// 入参: apply 待执行的编辑操作
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) changeObjects(apply func() error) (any, error) {
	return e.changeAtomicObjects(func() error {
		return e.currentEditor.Transaction(func(*ofdgo.Editor) error { return apply() })
	})
}

// changeAtomicObjects 执行库层原子操作，按文档版本更新预览
// 入参: apply 已具备事务保证的编辑操作
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) changeAtomicObjects(apply func() error) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	revision := e.currentEditor.Revision()
	if err := apply(); err != nil {
		return nil, err
	}
	if e.currentEditor.Revision() == revision {
		return e.editorSummary(), nil
	}
	return e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
}

// insertImage 按原始比例创建图片对象
// 入参: args 页码、图片数据、横纵坐标和宽度
// 返回: any 文档信息, error 错误信息
func (e *businessEngine) insertImage(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := e.bytesFromJS(args[1])
	if err != nil {
		return nil, err
	}
	resourceID, err := e.currentEditor.AddImage(data)
	if err != nil {
		return nil, err
	}
	size, err := e.currentEditor.ImageSize(resourceID)
	if err != nil {
		return nil, err
	}
	width := args[4].Float()
	box := ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: width, H: width * float64(size.Y) / float64(size.X)}
	content, err := e.currentSession.pageContent(args[0].Int())
	if err != nil {
		return nil, err
	}
	pageBox, err := e.currentSession.pageBox(args[0].Int(), content)
	if err != nil {
		return nil, err
	}
	if height := pageBox.H - box.Y; box.H > height {
		box.W *= height / box.H
		box.H = height
	}
	object := ofdgo.GraphicObject{Type: "ImageObject", ImageObject: ofdgo.ImageObject{
		ResourceID: resourceID, Boundary: fmt.Sprintf("%g %g %g %g", box.X, box.Y, box.W, box.H),
	}}
	return e.insertEditorObject(args[0].Int(), object, args[5:])
}

// insertEditorObject 复用顶层和内部范围插入流程，返回稳定选区
// 入参: page 页码, object 新对象, scope 可选父路径
// 返回: any 文档信息及新选区, error 错误信息
func (e *businessEngine) insertEditorObject(page int, object ofdgo.GraphicObject, scope []platformValue) (any, error) {
	var id string
	var err error
	if len(scope) > 0 && scope[0].String() != "" {
		key := scope[0].String()
		path, parseErr := e.compositePath(key)
		if parseErr != nil {
			return nil, parseErr
		}
		var index int
		index, err = e.currentEditor.AddCompositeObject(page, path, object)
		id = fmt.Sprintf("%s/%d", key, index)
	} else {
		id, err = e.currentEditor.AddObject(page, object)
	}
	if err != nil {
		return nil, err
	}
	info, err := e.previewEditor(e.currentEditor, e.currentSession.Renderer.RenderAnnotations)
	return editorSelectionInfo{info, []string{id}}, err
}

// editorOperationProgress 节流长操作检查点并向浏览器让出执行，保证阶段开始和结束可取消
// 入参: callback 浏览器进度回调
// 返回: func(string, int, int) error 进度回调
func (e *businessEngine) editorOperationProgress(callback platformValue) func(string, int, int) error {
	var lastStage string
	var lastProgress time.Time
	return func(stage string, completed, total int) error {
		if stage == lastStage && (total == 0 || completed != total) && time.Since(lastProgress) < 32*time.Millisecond {
			return nil
		}
		lastStage, lastProgress = stage, time.Now()
		return awaitExport(callback, stage, completed, total)
	}
}

// credentialsFromJS 解析本次打开的口令或本机证书私钥
// 入参: value 解锁表单数据
// 返回: ofdgo.Credentials 会话凭据, error 错误信息
func (e *businessEngine) credentialsFromJS(value platformValue) (ofdgo.Credentials, error) {
	credentials := ofdgo.Credentials{UserName: value.Get("userName").String()}
	if keyValue := value.Get("key"); !keyValue.IsNull() && !keyValue.IsUndefined() {
		key, err := e.bytesFromJS(keyValue)
		if err != nil {
			return credentials, err
		}
		defer clear(key)
		certificate, err := e.bytesFromJS(value.Get("certificate"))
		if err != nil {
			return credentials, err
		}
		var password []byte
		if field := value.Get("keyPassword"); !field.IsUndefined() && !field.IsNull() {
			password, err = e.bytesFromJS(field)
			if err != nil {
				return credentials, err
			}
			defer clear(password)
		}
		signer, certificate, err := ofdgo.ParseSignatureIdentity(certificate, key, password)
		if err != nil {
			return credentials, ofdgo.ErrInvalidCredentials
		}
		decrypter, ok := signer.(crypto.Decrypter)
		if !ok {
			return credentials, ofdgo.ErrInvalidCredentials
		}
		credentials.Certificate, credentials.Decrypter = certificate, decrypter
		return credentials, nil
	}
	var err error
	credentials.Password, err = e.bytesFromJS(value.Get("password"))
	return credentials, err
}

// securityFilesFromJS 读取会话内的本机证据文件
// 入参: value 二进制数组列表
// 返回: [][]byte 文件内容, error 错误信息
func (e *businessEngine) securityFilesFromJS(value platformValue) ([][]byte, error) {
	if value.IsUndefined() || value.IsNull() {
		return nil, nil
	}
	files := make([][]byte, value.Length())
	for i := range files {
		var err error
		files[i], err = e.bytesFromJS(value.Index(i))
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// verifySignatures 按本机信任锚和离线证据重新验签，不设置联网回调
// 入参: args 信任根、辅助证书、时间戳及撤销证据
// 返回: any 分项报告, error 错误信息
func (e *businessEngine) verifySignatures(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) == 0 {
		return nil, fmt.Errorf("missing signature verification arguments")
	}
	input := args[0]
	files := make(map[string][][]byte)
	for _, name := range []string{"roots", "certificates", "timestampRoots", "tokens", "crls", "ocsp"} {
		data, err := e.securityFilesFromJS(input.Get(name))
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	now := time.Now()
	policy := ofdgo.SignaturePolicy{RejectWeakAlgorithms: true, RequireDocumentCoverage: true,
		RequireTimestamp: input.Get("requireTimestamp").Bool(), RequireRevocation: input.Get("requireRevocation").Bool()}
	options := []ofdgo.SignatureVerifyOption{
		ofdgo.WithSignatureVerifyTime(now), ofdgo.WithSignaturePolicy(policy),
		ofdgo.WithSignatureTrustCerts(files["roots"]...), ofdgo.WithSignatureCerts(files["certificates"]...),
	}
	if policy.RequireTimestamp || len(files["timestampRoots"])+len(files["tokens"]) > 0 {
		options = append(options, ofdgo.WithSignatureTimestamp(ofdgo.SignatureTimestampOptions{
			TrustCerts: files["timestampRoots"], Certificates: files["certificates"], Tokens: files["tokens"], Required: policy.RequireTimestamp, VerifyTime: &now,
		}))
	}
	if policy.RequireRevocation || len(files["crls"])+len(files["ocsp"]) > 0 {
		options = append(options, ofdgo.WithSignatureRevocation(ofdgo.SignatureRevocationOptions{
			SignatureRevocationEvidence: ofdgo.SignatureRevocationEvidence{CRLs: files["crls"], OCSPResponses: files["ocsp"]},
			Certificates:                append(files["certificates"], files["roots"]...), Required: policy.RequireRevocation, VerifyTime: &now,
		}))
	}
	reports, err := e.currentSession.Reader.VerifyDocumentSignatures(e.currentSession.Reader.DocumentIndex(), options...)
	if err != nil {
		return nil, err
	}
	infos := make([]SignatureInfo, len(reports))
	for i, report := range reports {
		infos[i] = signatureInfo(report)
	}
	e.currentSession.signatures, e.currentSession.signatureError, e.currentSession.signaturesRead = infos, nil, true
	return map[string]any{"signatures": infos, "signatureCount": len(infos), "signatureError": ""}, nil
}

// setEncryption 设置当前文档的保存策略，不修改页面或撤销历史
// 入参: args 加密选项，null选择明文保存
// 返回: any 编辑状态, error 错误信息
func (e *businessEngine) setEncryption(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) != 1 {
		return nil, fmt.Errorf("missing encryption arguments")
	}
	options, err := e.encryptionOptionsFromJS(args[0])
	if err != nil {
		return nil, err
	}
	if options != nil {
		defer clear(options.Password)
	}
	if e.currentEditor == nil {
		if _, err := e.editDocument(nil); err != nil {
			return nil, err
		}
	}
	if err := e.currentEditor.SetEncryption(options); err != nil {
		return nil, err
	}
	return e.editorSummary(), nil
}

// encryptionOptionsFromJS 读取加密选项并校验接收者证书
// 入参: value 加密选项，null选择明文保存
// 返回: *ofdgo.EncryptionOptions 加密选项, error 错误信息
func (e *businessEngine) encryptionOptionsFromJS(value platformValue) (*ofdgo.EncryptionOptions, error) {
	if value.IsNull() {
		return nil, nil
	}
	if value.Type() != platformTypeObject {
		return nil, ofdgo.ErrEncryptionPolicyRequired
	}
	var password []byte
	if field := value.Get("password"); !field.IsNull() && !field.IsUndefined() {
		var err error
		password, err = e.bytesFromJS(field)
		if err != nil {
			return nil, err
		}
	}
	options := &ofdgo.EncryptionOptions{Password: password}
	success := false
	defer func() {
		if !success {
			clear(password)
		}
	}()
	if user := value.Get("userName"); user.Type() == platformTypeString {
		options.UserName = user.String()
	}
	if recipients := value.Get("recipients"); !recipients.IsUndefined() && !recipients.IsNull() {
		for i := 0; i < recipients.Length(); i++ {
			item := recipients.Index(i)
			data, err := e.bytesFromJS(item.Get("certificate"))
			if err != nil {
				return nil, err
			}
			certificate, err := ofdgo.ParseEncryptionCertificate(data)
			if err != nil {
				return nil, fmt.Errorf("接收者证书无效或不支持SM2加密")
			}
			options.Recipients = append(options.Recipients, ofdgo.EncryptionRecipient{UserName: item.Get("userName").String(), Certificate: certificate})
		}
	}
	if len(password) == 0 && len(options.Recipients) == 0 {
		return nil, ofdgo.ErrEncryptionPolicyRequired
	}
	success = true
	return options, nil
}

// previewSignatureSeal 渲染独立印章预览并读取所选页面区域，不接触私钥或修改文档
// 入参: args 印章数据和页码范围
// 返回: any 印章图像、声明尺寸和页面区域, error 错误信息
func (e *businessEngine) previewSignatureSeal(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) < 2 {
		return nil, fmt.Errorf("missing seal preview arguments")
	}
	data, err := e.bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	seal, err := ofdgo.ParseSignatureSeal(data)
	if err != nil {
		return nil, err
	}
	indices, err := ofdgo.ParsePageRange(args[1].String(), len(e.currentSession.doc.Pages.Page))
	if err != nil {
		return nil, err
	}
	pages := make([]map[string]any, 0, len(indices))
	for _, index := range indices {
		area, err := e.currentSession.Reader.PageArea(e.currentSession.doc.Pages.Page[index])
		if err != nil {
			return nil, err
		}
		box, err := ofdgo.ParseBox(area.PhysicalBox)
		if err != nil {
			return nil, err
		}
		pages = append(pages, map[string]any{"index": index, "x": box.X, "y": box.Y, "width": box.W, "height": box.H})
	}
	editor := ofdgo.NewEditor()
	editor.SetResources(e.resources)
	if _, err := editor.AddPage(seal.Width, seal.Height); err != nil {
		return nil, err
	}
	reader, err := editor.Reader(e.operationContext())
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	doc, err := reader.Doc()
	if err != nil {
		return nil, err
	}
	reader.Stamps = map[string][]ofdgo.Stamp{doc.Pages.Page[0].ID: {{Box: ofdgo.Box{W: seal.Width, H: seal.Height}, Type: seal.Type, Data: seal.Data}}}
	renderer := ofdgo.NewRenderer(reader, ofdgo.WithRenderBackends(e.currentSession.Renderer.Backends()))
	renderer.TransparentBackground = true
	if e.currentSession.fontFS != nil {
		renderer.SetFontFS(e.currentSession.fontFS)
	}
	page, err := reader.PageContentByIndex(0)
	if err != nil {
		return nil, err
	}
	var svg bytes.Buffer
	if err := renderer.RenderToSVG(page, &svg); err != nil {
		return nil, err
	}
	return map[string]any{"svg": svg.String(), "width": seal.Width, "height": seal.Height, "pages": pages}, nil
}

// signatureStampsFromJS 复用库层坐标校验生成页面或骑缝印章位置
// 入参: input 页面和毫米坐标参数
// 返回: []ofdgo.SignatureStamp 印章位置, error 校验错误
func (e *businessEngine) signatureStampsFromJS(input platformValue) ([]ofdgo.SignatureStamp, error) {
	count, err := e.currentSession.Reader.PageCount()
	if err != nil {
		return nil, err
	}
	indices, err := ofdgo.ParsePageRange(input.Get("pages").String(), count)
	if err != nil {
		return nil, err
	}
	pages := make([]int, len(indices))
	for i, index := range indices {
		pages[i] = index + 1
	}
	box := ofdgo.Box{X: input.Get("x").Float(), Y: input.Get("y").Float(), W: input.Get("width").Float(), H: input.Get("height").Float()}
	switch input.Get("placement").String() {
	case "normal":
		return e.currentSession.Reader.SignaturePageStamps(pages, box)
	case "seam":
		return e.currentSession.Reader.SignatureSeamStamps(pages, box)
	default:
		return nil, fmt.Errorf("invalid stamp placement")
	}
}

// signaturePlacement 校验预览确认的印章范围，不生成签名
// 入参: args 页面和毫米坐标参数
// 返回: any 校验结果, error 错误信息
func (e *businessEngine) signaturePlacement(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) == 0 {
		return nil, fmt.Errorf("missing stamp placement arguments")
	}
	_, err := e.signatureStampsFromJS(args[0])
	return successResult(nil), err
}

// saveSigned 调用库层最终包签署并在回验成功后交付浏览器
// 入参: args 本机证书私钥、信任根、印章位置、写出回调及进度回调
// 返回: any 保存结果, error 错误信息
func (e *businessEngine) saveSigned(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) < 3 {
		return nil, fmt.Errorf("missing signature arguments")
	}
	input := args[0]
	key, err := e.bytesFromJS(input.Get("key"))
	if err != nil {
		return nil, err
	}
	defer clear(key)
	certificate, err := e.bytesFromJS(input.Get("certificate"))
	if err != nil {
		return nil, err
	}
	var password []byte
	if field := input.Get("keyPassword"); !field.IsUndefined() && !field.IsNull() {
		password, err = e.bytesFromJS(field)
		if err != nil {
			return nil, err
		}
		defer clear(password)
	}
	signer, certificate, err := ofdgo.ParseSignatureIdentity(certificate, key, password)
	if err != nil {
		return nil, fmt.Errorf("证书、私钥或口令无效")
	}
	roots, err := e.securityFilesFromJS(input.Get("roots"))
	if err != nil {
		return nil, err
	}
	intermediates, err := e.securityFilesFromJS(input.Get("intermediates"))
	if err != nil {
		return nil, err
	}
	options := ofdgo.SignatureWriteOptions{Signer: signer, Certificate: certificate, TrustRoots: roots,
		Intermediates: intermediates, Lock: input.Get("lock").Bool()}
	if compression := input.Get("compression"); !compression.IsUndefined() && !compression.IsNull() {
		if err := e.compressionFromJS(compression, &options.Compression); err != nil {
			return nil, err
		}
	}
	switch input.Get("mode").String() {
	case "append":
		options.Mode = ofdgo.SignatureAppend
	case "replace":
		options.Mode = ofdgo.SignatureReplace
	default:
		return nil, fmt.Errorf("invalid signature mode")
	}
	if seal := input.Get("seal"); !seal.IsNull() && !seal.IsUndefined() {
		options.Seal, err = e.bytesFromJS(seal)
		if err != nil {
			return nil, err
		}
		options.Stamps, err = e.signatureStampsFromJS(input)
		if err != nil {
			return nil, err
		}
	}
	progress := e.editorOperationProgress(args[2])
	if err := progress("sign", 0, 1); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if e.currentEditor != nil {
		e.currentEditor.OnWriteProgress = progress
		defer func() { e.currentEditor.OnWriteProgress = nil }()
		_, err = ofdgo.SignEditorTo(&output, e.currentEditor, options)
	} else {
		_, err = e.currentSession.Reader.SignTo(&output, options)
	}
	if err != nil {
		return nil, err
	}
	if err := progress("sign", 1, 1); err != nil {
		return nil, err
	}
	if _, err := (exportWriter{write: args[1]}).Write(output.Bytes()); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"mime": "application/ofd", "label": "OFD"}), nil
}

// prepareSave 创建独立保存快照，后续写出不占用当前编辑文档
// 入参: args 取消检查回调
// 返回: any 快照修订, error 准备错误
func (e *businessEngine) prepareSave(args []platformValue) (any, error) {
	e.saveSnapshot = nil
	editor := e.currentEditor
	if editor == nil {
		if e.currentSession == nil {
			return nil, fmt.Errorf("ofd document is not opened")
		}
		var err error
		editor, err = e.currentSession.Reader.Editor()
		if err != nil {
			return nil, err
		}
	}
	ctx := e.operationContext()
	if len(args) > 0 {
		ctx = e.newOutputContext(args[0])
	}
	snapshot, err := editor.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	e.saveSnapshot = snapshot
	return successResult(map[string]any{"revision": editor.Revision()}), nil
}

// savePrepared 写出并释放已准备的快照，不修改当前编辑状态
// 入参: args 写出回调、进度回调及输出配置
// 返回: any 保存结果, error 保存错误
func (e *businessEngine) savePrepared(args []platformValue) (any, error) {
	snapshot := e.saveSnapshot
	e.saveSnapshot = nil
	if snapshot == nil {
		return nil, fmt.Errorf("save snapshot is not prepared")
	}
	return e.saveEditor(snapshot, args)
}

// discardSave 释放尚未写出的保存快照
// 入参: args 空参数
// 返回: any 操作结果, error 错误信息
func (e *businessEngine) discardSave(args []platformValue) (any, error) {
	e.saveSnapshot = nil
	return successResult(nil), nil
}

// saveDocument 分块写出当前编辑文档或指定页面
// 入参: args 数据写出回调、可选准备进度回调和可选页面索引
// 返回: any 保存结果, error 错误信息
func (e *businessEngine) saveDocument(args []platformValue) (any, error) {
	if e.currentEditor == nil {
		if e.currentSession == nil {
			return nil, fmt.Errorf("ofd document is not opened")
		}
		editor, err := e.currentSession.Reader.EditorContext(e.operationContext())
		if err != nil {
			return nil, err
		}
		return e.saveEditor(editor, args)
	}
	return e.saveEditor(e.currentEditor, args)
}

// saveEditor 统一分块写出及进度取消，加密策略由库层执行
// 入参: editor 待写出文档, args 写出回调、进度回调及可选页面索引、压缩配置、取消检查
// 返回: any 保存结果, error 错误信息
func (e *businessEngine) saveEditor(editor *ofdgo.Editor, args []platformValue) (any, error) {
	if len(args) == 0 || args[0].Type() != platformTypeFunction {
		return nil, fmt.Errorf("missing output callback")
	}
	if len(args) > 1 {
		editor.OnWriteProgress = e.editorOperationProgress(args[1])
		defer func() { editor.OnWriteProgress = nil }()
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[0]}, 1<<20)
	var err error
	var options ofdgo.WriteOptions
	ctx := e.operationContext()
	if len(args) > 3 {
		if err := e.compressionFromJS(args[3], &options.Compression); err != nil {
			return nil, err
		}
	}
	if len(args) > 4 {
		ctx = e.newOutputContext(args[4])
	}
	if len(args) > 2 && !args[2].IsNull() && !args[2].IsUndefined() {
		_, err = editor.WritePagesTo(ctx, writer, e.indexesFromJS(args[2]), options)
	} else {
		_, err = editor.WriteToWithOptions(ctx, writer, options)
	}
	if err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"mime": "application/ofd", "label": "OFD"}), nil
}

// compressionFromJS 读取压缩配置并交给库层校验
// 入参: value 浏览器配置, options 目标配置
// 返回: error 配置错误
func (e *businessEngine) compressionFromJS(value platformValue, options *ofdgo.CompressionOptions) error {
	if err := json.Unmarshal([]byte(platformJSON(value)), options); err != nil {
		return err
	}
	return options.Validate()
}

// newOutputContext 创建单次串行操作的取消上下文
// 入参: checkpoint 浏览器取消检查回调
// 返回: *outputContext 操作上下文
func (e *businessEngine) newOutputContext(checkpoint platformValue) *outputContext {
	ctx, cancel := context.WithCancel(e.operationContext())
	return &outputContext{Context: ctx, cancel: cancel, checkpoint: func() error { return awaitExport(checkpoint) }}
}

// newEditorOperationContext 在底层取消检查时保持最近的进度，避免版本处理期间界面无响应
// 入参: callback 浏览器进度及取消回调
// 返回: *outputContext 操作上下文, func(string, int, int) error 阶段进度回调
func (e *businessEngine) newEditorOperationContext(callback platformValue) (*outputContext, func(string, int, int) error) {
	ctx, cancel := context.WithCancel(e.operationContext())
	stage, completed, total := "ids", 0, 0
	progress := e.editorOperationProgress(callback)
	operation := &outputContext{Context: ctx, cancel: cancel, checkpoint: func() error { return progress(stage, completed, total) }}
	return operation, func(next string, done, count int) error {
		stage, completed, total = next, done, count
		return progress(stage, completed, total)
	}
}

// Err 按操作间隔检查时间并处理浏览器消息，每次调用均检查已有取消状态
// 返回: error 取消错误
func (c *outputContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	interval := uint32(c.interval) + 1
	if c.interval == 0 {
		interval = 16
	}
	if (c.checks.Add(1)-1)%interval != 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.checked) >= 50*time.Millisecond {
		c.checked = time.Now()
		if err := c.checkpoint(); err != nil {
			c.cancel()
		}
	}
	return c.Context.Err()
}

// businessCommands 共用业务命令表
func (e *businessEngine) businessCommands() map[string]func([]platformValue) (any, error) {
	return map[string]func([]platformValue) (any, error){
		"ofdgoCheckDocument": func(args []platformValue) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("missing operation name")
			}
			return nil, e.checkDocument(args[0].String())
		},
		"ofdgoPreparePrint":           e.preparePrint,
		"ofdgoLayoutPrint":            e.layoutPrint,
		"ofdgoPrintSide":              e.printSide,
		"ofdgoPrintPDF":               e.printPDF,
		"ofdgoDiscardPrint":           e.discardPrint,
		"ofdgoDetectFormat":           e.detectDocumentFormat,
		"ofdgoOpen":                   e.openDocument,
		"ofdgoOpenPDF":                e.openPDF,
		"ofdgoLoadPDF":                e.loadPDF,
		"ofdgoPDFContext":             e.pdfContext,
		"ofdgoSelectDocument":         e.selectDocument,
		"ofdgoDocumentVersions":       e.documentVersions,
		"ofdgoExportVersion":          e.exportVersion,
		"ofdgoAnnotationInfos":        e.annotationInfos,
		"ofdgoDocumentNavigation":     e.documentNavigation,
		"ofdgoInvoices":               e.documentInvoices,
		"ofdgoInvoice":                e.documentInvoice,
		"ofdgoExportInvoice":          e.exportDocumentInvoice,
		"ofdgoValidateInvoice":        e.validateDocumentInvoice,
		"ofdgoChangeInvoice":          e.changeDocumentInvoice,
		"ofdgoChangeBookmark":         e.changeBookmark,
		"ofdgoActionList":             e.actionList,
		"ofdgoChangeActions":          e.changeActions,
		"ofdgoStyleResource":          e.styleResource,
		"ofdgoChangeStyleResource":    e.changeStyleResource,
		"ofdgoApplyStyleResource":     e.applyStyleResource,
		"ofdgoPageSettings":           e.pageSettings,
		"ofdgoChangeLayer":            e.changeLayer,
		"ofdgoChangeTemplate":         e.changeTemplate,
		"ofdgoSelectVersion":          e.selectDocumentVersion,
		"ofdgoChangeVersion":          e.changeDocumentVersion,
		"ofdgoChangeDocument":         e.changePackageDocument,
		"ofdgoPackageDocuments":       e.packageDocuments,
		"ofdgoConvertFile":            e.convertFile,
		"ofdgoPackFiles":              e.packFiles,
		"ofdgoOutputFormats":          func([]platformValue) (any, error) { return ofdgo.OutputFormats(), nil },
		"ofdgoConfigure":              e.configureDocument,
		"ofdgoDocumentInfo":           e.documentInfo,
		"ofdgoVerifySignatures":       e.verifySignatures,
		"ofdgoRenderPage":             e.renderPage,
		"ofdgoOutlineLinks":           e.outlineLinks,
		"ofdgoStructureTargets":       e.structureTargets,
		"ofdgoStructureObjects":       e.structureObjects,
		"ofdgoChangeStructure":        e.changeStructure,
		"ofdgoOpenLinks":              e.openLinks,
		"ofdgoRenderBackends":         e.renderBackends,
		"ofdgoSVGFontData":            e.svgFontData,
		"ofdgoSearchPage":             e.searchPage,
		"ofdgoPageTextString":         e.pageTextString,
		"ofdgoExportFormats":          e.exportFormats,
		"ofdgoParsePageRange":         e.parsePageRange,
		"ofdgoExportPage":             e.exportPage,
		"ofdgoExportDocument":         e.exportDocument,
		"ofdgoExportAttachment":       e.exportAttachment,
		"ofdgoExportMedia":            e.exportMedia,
		"ofdgoResources":              e.documentResources,
		"ofdgoResourceReferences":     e.documentResourceReferences,
		"ofdgoResourceTargets":        e.resourceTargets,
		"ofdgoResourceLocations":      e.resourceLocations,
		"ofdgoFontPreview":            e.fontPreview,
		"ofdgoChangeResource":         e.changeResource,
		"ofdgoExportResource":         e.exportResource,
		"ofdgoImages":                 e.documentImages,
		"ofdgoExportImages":           e.exportImages,
		"ofdgoMatchFontFiles":         e.matchFontFiles,
		"ofdgoChangeFontBinding":      e.changeFontBinding,
		"ofdgoFontFaces":              e.fontFaces,
		"ofdgoFontMenu":               e.fontMenu,
		"ofdgoFontFace":               e.fontFace,
		"ofdgoCreateDocument":         e.createDocument,
		"ofdgoImportImages":           e.importImages,
		"ofdgoImagePageSize":          e.imagePageSize,
		"ofdgoEditDocument":           e.editDocument,
		"ofdgoUpdateInfo":             e.updateInfo,
		"ofdgoChangeAttachment":       e.changeAttachment,
		"ofdgoChangeSignature":        e.changeSignature,
		"ofdgoChangePage":             e.changePage,
		"ofdgoBatchPages":             e.batchPages,
		"ofdgoChangeOutline":          e.changeOutline,
		"ofdgoChangeAnnotations":      e.changeAnnotations,
		"ofdgoChangeSelection":        e.changeSelection,
		"ofdgoWriteAnnotation":        e.writeAnnotation,
		"ofdgoReadAnnotation":         e.readAnnotation,
		"ofdgoInsertInk":              e.insertInk,
		"ofdgoStyleObjects":           e.styleObjects,
		"ofdgoPreviewPositionedText":  e.previewPositionedText,
		"ofdgoMoveOutline":            e.moveOutline,
		"ofdgoCaptureStyle":           e.captureStyle,
		"ofdgoGroupObjects":           e.groupObjects,
		"ofdgoPasteStyle":             e.pasteStyle,
		"ofdgoResizeObjects":          e.resizeObjects,
		"ofdgoLoadImport":             e.loadImport,
		"ofdgoImportPages":            e.importPages,
		"ofdgoImportDocuments":        e.importDocuments,
		"ofdgoInsertText":             e.insertText,
		"ofdgoUpdateText":             e.updateText,
		"ofdgoPreviewText":            e.previewText,
		"ofdgoTextSelection":          e.textSelection,
		"ofdgoPreviewTextSelection":   e.previewTextSelection,
		"ofdgoUpdateTextSelection":    e.updateTextSelection,
		"ofdgoStyleText":              e.styleText,
		"ofdgoFormatText":             e.formatText,
		"ofdgoStyleTextFonts":         e.styleTextFonts,
		"ofdgoGetRichText":            e.getRichText,
		"ofdgoRichTextFonts":          e.richTextFonts,
		"ofdgoSaveRichText":           e.saveRichText,
		"ofdgoPreviewRichText":        e.previewRichText,
		"ofdgoCheckTextFont":          e.checkTextFont,
		"ofdgoFontPreferenceOrder":    e.fontPreferenceOrder,
		"ofdgoInsertImage":            e.insertImage,
		"ofdgoInsertShape":            e.insertShape,
		"ofdgoUpdatePathStyle":        e.updatePathStyle,
		"ofdgoReplaceImage":           e.replaceImage,
		"ofdgoEditorFont":             e.editorFont,
		"ofdgoAlignObject":            e.alignObject,
		"ofdgoTransformObject":        e.transformObject,
		"ofdgoReshapeObject":          e.reshapeObject,
		"ofdgoReshapeLine":            e.reshapeLine,
		"ofdgoResetImageCrop":         e.resetImageCrop,
		"ofdgoLayoutText":             e.layoutText,
		"ofdgoLayoutTexts":            e.layoutTexts,
		"ofdgoTransformObjects":       e.transformObjects,
		"ofdgoCompositeObjects":       e.compositeObjects,
		"ofdgoChangeCompositeObjects": e.changeCompositeObjects,
		"ofdgoAlignObjects":           e.alignObjects,
		"ofdgoDeleteObjects":          e.deleteObjects,
		"ofdgoEraseObjects":           e.eraseObjects,
		"ofdgoEraseObjectsPath":       e.eraseObjectsPath,
		"ofdgoCopyObjects":            e.copyObjects,
		"ofdgoCaptureObjects":         e.captureObjects,
		"ofdgoExportObjects":          e.exportObjects,
		"ofdgoDiscardObjects":         e.discardObjects,
		"ofdgoPasteObjects":           e.pasteObjects,
		"ofdgoImportObjects":          e.importObjects,
		"ofdgoOrderObjects":           e.orderObjects,
		"ofdgoDistributeObjects":      e.distributeObjects,
		"ofdgoDistributionAxes":       e.distributionAxes,
		"ofdgoRotateObjects":          e.rotateObjects,
		"ofdgoFlipObjects":            e.flipObjects,
		"ofdgoCropImage":              e.cropImage,
		"ofdgoFitImage":               e.fitImage,
		"ofdgoPreviewImage":           e.previewImage,
		"ofdgoDeleteObject":           e.deleteObject,
		"ofdgoUndo":                   func([]platformValue) (any, error) { return e.restoreEditor(false) },
		"ofdgoRedo":                   func([]platformValue) (any, error) { return e.restoreEditor(true) },
		"ofdgoSaveDocument":           e.saveDocument,
		"ofdgoPrepareSave":            e.prepareSave,
		"ofdgoSaveSnapshot":           e.savePrepared,
		"ofdgoDiscardSave":            e.discardSave,
		"ofdgoSetEncryption":          e.setEncryption,
		"ofdgoSaveSigned":             e.saveSigned,
		"ofdgoPreviewSignatureSeal":   e.previewSignatureSeal,
		"ofdgoSignaturePlacement":     e.signaturePlacement,
	}
}

// operationContext 返回当前操作上下文
func (e *businessEngine) operationContext() context.Context {
	if e.ctx != nil {
		return e.ctx
	}
	return context.Background()
}

// failureResult 统一原生及浏览器业务错误协议
func failureResult(err error) apiResult {
	result := apiResult{Error: err.Error()}
	switch {
	case errors.Is(err, ofdgo.ErrSignatureDependency):
		result.Code, result.Error = "signatureDependency", "存在签名或版本依赖，无法移除"
	case errors.Is(err, ofdgo.ErrIncompleteDocument):
		result.Code, result.Error = "documentPending", "文档尚未准备完成"
	case errors.Is(err, ofdgo.ErrPrintDenied):
		result.Code, result.Error = "printDenied", "文档禁止打印"
	case errors.Is(err, ofdgo.ErrPDFPassword):
		result.Code, result.Error = "pdfPassword", "PDF密码缺失或错误"
	case errors.Is(err, ofdgo.ErrCredentialsRequired):
		result.Code, result.Error = "credentialsRequired", "文档已加密"
	case errors.Is(err, ofdgo.ErrInvalidCredentials):
		result.Code, result.Error = "invalidCredentials", "凭据无效或文件损坏"
	case errors.Is(err, ofdgo.ErrUnsupportedEncryption):
		result.Code, result.Error = "unsupportedEncryption", "不支持此加密方式"
	case errors.Is(err, ofdgo.ErrEncryptionPolicyRequired):
		result.Code, result.Error = "encryptionPolicyRequired", "请设置加密方式"
	}
	var failure *ofdgo.EditError
	if errors.As(err, &failure) {
		result.ReasonCode = failure.Code
	}
	if errors.As(err, &result.MissingGlyphs) {
		result.ReasonCode = ofdgo.EditMissingGlyphs
	}
	return result
}
