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
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfObjectChunkLimit 限制导入暂存片段的容量，不限制页面对象数量
const pdfObjectChunkLimit = 256

// ErrPDFPassword 表示PDF需要正确的用户或所有者密码
var ErrPDFPassword = pdfgo.ErrPassword

// PDFImportOptions 指定PDF转换的密码、运行时后端、进度通知与检查策略
// Password默认使用PDF安全处理器编码，PasswordUTF8启用UTF-8输入及标准密码准备，空值尝试空密码
// Strict禁止日期兼容、恢复缺失资源或丢失内容语义，默认在报告中记录警告
// RendererOptions复用渲染器字体来源和后端配置，Backends优先于其中的后端设置
// RasterDPI指定局部合成与三维绘制精度，0沿用渲染器DPI，默认300dpi，Strict禁用局部合成
// OnProgress按open、pages、convert及write.*阶段报告进度，total为0表示总量未知
// ResolveFile读取PDF引用的外部媒体、附件、页面及三维纹理，默认不访问网络或本地路径
// ResolveReference可提供已解密的引用页面，目标阅读器由调用方维护
// Halftones提供只读设备命名网屏，所属阅读器由调用方维护，连续色调输出不网屏化
// Compression配置转换输出，有损模式按图片实际尺寸降采样，原始及无损模式保持完整采样
// Resources用于导入、局部合成与返回编辑器的共享资源预算
type PDFImportOptions struct {
	Resources        *Resources
	Password         []byte
	PasswordUTF8     bool
	Backends         *RenderBackends
	RendererOptions  []RendererOption
	Progress         func(int) error
	OnProgress       func(stage string, completed, total int) error
	Strict           bool
	RasterDPI        float64
	ResolveFile      pdfgo.FileResolver
	ResolveReference pdfgo.ReferenceResolver
	Halftones        map[string]*pdfgo.Halftone
	Compression      CompressionOptions
}

// PDFImportReport 汇总转换页数、对象数、链接数和转换警告
type PDFImportReport struct {
	Pages        int
	TextObjects  int
	PathObjects  int
	ImageObjects int
	Links        int
	Warnings     []pdfgo.Diagnostic
}

// PDFImport 按页转换PDF，实例需串行使用，输入源由调用方持有至Close
// 页面阅读快照独立于后续转换，Finish完成全局结构后交付独立编辑文档
// 转换失败后可用Reset重建，输入及回调在会话存续期间须保持有效
type PDFImport struct {
	input    *Input
	importer *pdfImporter
	options  PDFImportOptions
	pages    []PageContent
	ready    []bool
	previews *renderCache[string, []byte]
	err      error
	finished bool
}

// ConvertPDF 将PDF转换并写入OFD，转换阶段失败时不写入目标
// 入参: ctx 取消上下文, input 输入源, output OFD输出, options 转换选项
// 返回: PDFImportReport 转换统计, error 错误信息
func ConvertPDF(ctx context.Context, input *Input, output io.Writer, options PDFImportOptions) (PDFImportReport, error) {
	editor, report, err := ImportPDF(ctx, input, options)
	if err != nil {
		return PDFImportReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return PDFImportReport{}, err
	}
	if options.OnProgress != nil {
		if err := options.OnProgress("write", 0, 0); err != nil {
			return PDFImportReport{}, err
		}
	}
	editor.OnWriteProgress = func(stage string, completed, total int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.OnProgress != nil {
			return options.OnProgress("write."+stage, completed, total)
		}
		return nil
	}
	_, err = editor.WriteToWithOptions(ctx, output, WriteOptions{Compression: options.Compression})
	return report, err
}

// NewPDFImport 建立PDF页面索引和文档信息，不转换页面图元
// 内部复制输入密码供Reset使用，Close时清除；其他选项引用需保持有效
// 入参: ctx 取消上下文, input 调用方持有的输入源, options 转换选项
// 返回: *PDFImport 增量转换会话, error 读取错误
func NewPDFImport(ctx context.Context, input *Input, options PDFImportOptions) (*PDFImport, error) {
	if options.Resources == nil {
		options.Resources, _ = NewResources(ResourceOptions{})
	}
	ctx = options.Resources.context(ctx)
	if input == nil {
		return nil, fmt.Errorf("missing source")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := options.Compression.Validate(); err != nil {
		return nil, err
	}
	if !finite(options.RasterDPI) || options.RasterDPI < 0 {
		return nil, fmt.Errorf("invalid PDF raster DPI")
	}
	renderer := NewRenderer(&Reader{resources: options.Resources}, options.RendererOptions...)
	if options.Backends != nil {
		WithRenderBackends(*options.Backends)(renderer)
	}
	if options.RasterDPI != 0 {
		renderer.DPI = options.RasterDPI
	}
	if !finite(renderer.DPI) || renderer.DPI <= 0 {
		return nil, fmt.Errorf("invalid PDF raster DPI")
	}
	keep := false
	options.Password = slices.Clone(options.Password)
	defer func() {
		if !keep {
			clear(options.Password)
		}
	}()
	progress := editorProgress(options.OnProgress).withContext(ctx)
	if err := progress.report("open", 0, 0); err != nil {
		return nil, err
	}
	editor := NewEditor()
	editor.runtimeResources = options.Resources
	editor.SetRenderBackends(renderer.Backends())
	editor.fontDirs, editor.fontFS = renderer.FontSources()
	editor.fontSourcesCache = renderer.fontSourcesCache
	importer := &pdfImporter{resources: options.Resources, ctx: ctx, editor: editor, renderer: renderer, rasterDPI: renderer.DPI, page: -1, fontIDs: map[*pdfgo.Font]string{}, fonts: map[*pdfgo.Font]*pdfImportedFont{}, pages: map[pdfgo.Reference]*pdfgo.Page{}, pageIDs: map[pdfgo.Reference]string{}, structureBindings: &pdfStructureBindings{}}
	readOptions := pdfgo.ReaderOptions{Password: options.Password, PasswordUTF8: options.PasswordUTF8, ShapeText: importer.shapePDFText}
	if !options.Strict {
		importer.warning = func(warning pdfgo.Diagnostic) {
			warning.Page = importer.page + 1
			importer.report.Warnings = append(importer.report.Warnings, warning)
		}
		readOptions.Warning = importer.warning
	}
	reader, err := pdfgo.NewReaderWithOptions(&pdfImportInput{input: input, importer: importer}, input.Size(), readOptions)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !keep {
			reader.Close()
		}
	}()
	importer.reader = reader
	if err := importer.prepareStructure(ctx); err != nil {
		return nil, fmt.Errorf("import PDF structure: %w", err)
	}
	outputIntents, err := reader.OutputIntents()
	if err != nil {
		return nil, fmt.Errorf("import PDF output intents: %w", err)
	}
	if len(outputIntents) != 0 {
		var profile []byte
		applicable := true
		for _, intent := range outputIntents {
			if intent.Subtype != "GTS_PDFA1" && intent.Subtype != "GTS_PDFX" || intent.Space == nil || len(intent.Profile) == 0 || profile != nil && !bytes.Equal(profile, intent.Profile) {
				applicable = false
				break
			}
			profile = intent.Profile
		}
		if applicable {
			if err := reader.SetDeviceColorProfile(profile); err != nil {
				return nil, fmt.Errorf("import PDF device color profile: %w", err)
			}
		} else {
			if options.Strict {
				return nil, &pdfgo.UnsupportedError{Feature: "PDF output intent conversion"}
			}
			importer.report.Warnings = append(importer.report.Warnings, pdfgo.Diagnostic{Message: "PDF output intents preserved as metadata; not applied to rendering"})
		}
	}
	importer.pageIndexes = make(map[pdfgo.Reference]int)
	importer.resolveFile = options.ResolveFile
	importer.resolveReference = options.ResolveReference
	importer.halftones = options.Halftones
	importer.compression = options.Compression
	importer.references = &codecpdf.ReferenceCache{}
	defer func() {
		if !keep {
			importer.references.Close()
		}
	}()
	importer.halftoneWarnings = make(map[*pdfgo.Halftone]bool)
	if err := progress.report("pages", 0, 0); err != nil {
		return nil, err
	}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		_, width, height := codecpdf.PageMatrix(page)
		if _, err := editor.AddPage(width, height); err != nil {
			return err
		}
		importer.pages[page.Reference] = page
		importer.pageOrder = append(importer.pageOrder, page)
		importer.pageIDs[page.Reference] = editor.pages[index].ID
		importer.pageIndexes[page.Reference] = index
		return progress.report("pages", index+1, 0)
	})
	if err != nil {
		return nil, err
	}
	if len(editor.pages) != 0 {
		importer.page = -1
		if err := importer.documentMetadata(ctx, options.Strict); err != nil {
			return nil, fmt.Errorf("import PDF metadata: %w", err)
		}
		if err := importer.documentAttachments(ctx); err != nil {
			return nil, fmt.Errorf("import PDF attachments: %w", err)
		}
		if err := importer.outlines(ctx); err != nil {
			return nil, fmt.Errorf("import PDF outlines: %w", err)
		}
		if err := importer.documentOpenActions(ctx); err != nil {
			return nil, fmt.Errorf("import PDF document actions: %w", err)
		}
		importer.page = 0
	}
	if err := progress.report("convert", 0, len(editor.pages)); err != nil {
		return nil, err
	}
	if len(editor.pages) == 0 {
		return nil, fmt.Errorf("PDF contains no pages")
	}
	keep = true
	return &PDFImport{input: input, importer: importer, options: options, pages: cloneEditorData(editor.pages), ready: make([]bool, len(editor.pages))}, nil
}

// PageCount 返回文档页数
// 返回: int 页面总数
func (p *PDFImport) PageCount() int { return len(p.pages) }

// Ready 返回指定页是否转换完成
// 入参: index 页面索引，从0开始
// 返回: bool 页面是否就绪
func (p *PDFImport) Ready(index int) bool {
	return index >= 0 && index < len(p.ready) && p.ready[index]
}

// SetEncryption 设置转换文档的输出加密方式，不改变PDF读取凭据
// 入参: options 加密选项，nil取消输出加密
// 返回: error 加密配置错误
func (p *PDFImport) SetEncryption(options *EncryptionOptions) error {
	if p.importer == nil {
		return fmt.Errorf("PDF import is closed")
	}
	return p.importer.editor.SetEncryption(options)
}

// ConvertPage 转换指定页面，已完成的页面不重复处理
// 入参: ctx 取消上下文, index 页面索引，从0开始
// 返回: error 转换或取消错误
func (p *PDFImport) ConvertPage(ctx context.Context, index int) (err error) {
	if p.importer == nil {
		return fmt.Errorf("PDF import is closed")
	}
	if p.err != nil {
		return p.err
	}
	if index < 0 || index >= len(p.pages) {
		return fmt.Errorf("invalid page index: %d", index)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.ready[index] {
		return nil
	}
	defer func() {
		if err != nil {
			p.err = err
		}
	}()
	importer := p.importer
	importer.ctx = importer.resources.context(ctx)
	ctx = importer.ctx
	importer.renderer.OutputContext = ctx
	page := importer.pageOrder[index]
	matrix, width, height := codecpdf.PageMatrix(page)
	importer.matrix = matrix
	importer.page = index
	importer.rasterWarned = false
	importer.pageBox = page.CropBox
	importer.pageWidth = width
	importer.pageHeight = height
	importer.maskClips = make(map[*pdfgo.SoftMask]pdfgo.Path)
	importer.compositeCache = nil
	importer.compositeNodes = nil
	importer.compositeSpace = nil
	importer.patternCells = nil
	importer.patternOrigins = nil
	importer.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
	importer.textOrigins = nil
	importer.clipPaths = nil
	importer.annotationData = nil
	importer.annotationBindings = nil
	if err := importer.pageOpenActions(ctx, page); err != nil {
		return fmt.Errorf("import PDF page %d actions: %w", index+1, err)
	}
	if err := importer.compositePage(nil, func(visitor pdfgo.Visitor) error { return importer.reader.WalkPage(ctx, page, visitor) }); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	if err := importer.annotations(ctx, page, p.options.Strict); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	if err := importer.commitAnnotations(); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	if err := importer.commitObjects(); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	importer.report.Pages++
	if err := editorProgress(p.options.OnProgress).withContext(ctx).report("convert", importer.report.Pages, len(p.pages)); err != nil {
		return err
	}
	if p.options.Progress != nil {
		if err := p.options.Progress(importer.report.Pages); err != nil {
			return err
		}
	}
	p.ready[index] = true
	return nil
}

// Reader 创建指定页的独立阅读快照，其他页仅保留尺寸和标识
// 未包含的页面返回ErrIncompleteDocument；index为-1时仅返回索引，完整编辑或导出须使用Finish
// 入参: ctx 取消上下文, index 页面索引
// 返回: *Reader 阅读快照, error 转换或构建错误
func (p *PDFImport) Reader(ctx context.Context, index int) (*Reader, error) {
	if p.importer == nil {
		return nil, fmt.Errorf("PDF import is closed")
	}
	if p.err != nil {
		return nil, p.err
	}
	if index != -1 {
		if err := p.ConvertPage(ctx, index); err != nil {
			return nil, err
		}
	}
	snapshot := *p.importer.editor
	snapshot.pages = slices.Clone(p.pages)
	if p.previews == nil {
		p.previews = resourceCache[string, []byte](p.options.Resources, 16<<20)
	}
	snapshot.previewPages = make(map[string][]byte, len(p.pages))
	for i, page := range p.pages {
		if i == index {
			continue
		}
		data, ok := p.previews.get(page.ID)
		if !ok {
			var err error
			data, err = encodeOFDXMLContext(ctx, func(x *ofdXML) { x.page(page) })
			if err != nil {
				return nil, err
			}
			p.previews.put(page.ID, data, len(data)+128)
		}
		snapshot.previewPages[page.ID] = data
	}
	if index >= 0 {
		snapshot.pages[index] = p.importer.editor.pages[index]
	}
	reader, err := snapshot.Reader(ctx)
	if err != nil {
		return nil, err
	}
	reader.pageReady = make(map[string]bool)
	if index >= 0 {
		reader.pageReady[reader.doc.Pages.Page[index].ID] = true
	}
	return reader, nil
}

// PageText 直接读取已转换页面的文字，不创建整本文档的阅读快照，不包含注解外观
// 返回结果独立于转换会话，转换及提取失败时不返回部分文字
// renderer的Reader不参与提取，提供的渲染器须与本次提取串行使用
// 入参: ctx 取消上下文, index 零基页面索引, renderer 提取配置及字体来源缓存，nil沿用导入配置
// 返回: *PageText 页面文字, error 转换、提取或关闭错误
func (p *PDFImport) PageText(ctx context.Context, index int, renderer *Renderer) (*PageText, error) {
	if err := p.ConvertPage(ctx, index); err != nil {
		return nil, err
	}
	snapshot := *p.importer.editor
	snapshot.source = nil
	snapshot.pages = []PageContent{snapshot.pages[index]}
	reader, err := snapshot.resourceReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if renderer == nil {
		renderer = p.importer.renderer
	}
	renderer = renderer.childRenderer(reader)
	renderer.OutputContext = ctx
	return renderer.PageTextContext(ctx, &snapshot.pages[0])
}

// PDFDestination 准备链接来源页并查询实时PDF定位语义，不修改已交付的阅读快照
// 入参: ctx 取消上下文, index 链接来源页，-1仅查询文档动作, dest OFD动作目标
// 返回: pdfgo.DestinationTarget 定位目标, bool 是否存在, error 页面转换或关闭错误
func (p *PDFImport) PDFDestination(ctx context.Context, index int, dest Dest) (pdfgo.DestinationTarget, bool, error) {
	if p.importer == nil {
		return pdfgo.DestinationTarget{}, false, fmt.Errorf("PDF import is closed")
	}
	if p.err != nil {
		return pdfgo.DestinationTarget{}, false, p.err
	}
	if err := ctx.Err(); err != nil {
		return pdfgo.DestinationTarget{}, false, err
	}
	if index != -1 {
		if err := p.ConvertPage(ctx, index); err != nil {
			return pdfgo.DestinationTarget{}, false, err
		}
	}
	if targets := p.importer.editor.pdfDestinations; targets != nil {
		target, ok := targets.targets[pdfDestinationKey(dest)]
		return target, ok, nil
	}
	return pdfgo.DestinationTarget{}, false, nil
}

// Finish 转换尚未就绪的页面并补全文档结构，返回独立编辑文档
// 入参: ctx 取消上下文
// 返回: *Editor 编辑文档, PDFImportReport 转换统计, error 转换错误
func (p *PDFImport) Finish(ctx context.Context) (*Editor, PDFImportReport, error) {
	if p.importer == nil {
		return nil, PDFImportReport{}, fmt.Errorf("PDF import is closed")
	}
	for index := range p.pages {
		if err := p.ConvertPage(ctx, index); err != nil {
			return nil, PDFImportReport{}, err
		}
	}
	if !p.finished {
		p.importer.ctx = p.importer.resources.context(ctx)
		if err := p.importer.documentStructures(p.importer.ctx); err != nil {
			p.err = err
			return nil, PDFImportReport{}, fmt.Errorf("import PDF structure: %w", err)
		}
		p.finished = true
	}
	return p.importer.editor, p.importer.report, nil
}

// Reset 放弃已转换内容并重建索引，保留文档标识及输出加密策略
// 失败时保留原会话，已交付的阅读快照不受影响；输入源仍由调用方持有
// 入参: ctx 本次重建的取消上下文
// 返回: error 重建错误
func (p *PDFImport) Reset(ctx context.Context) error {
	if p.importer == nil {
		return fmt.Errorf("PDF import is closed")
	}
	next, err := NewPDFImport(ctx, p.input, p.options)
	if err != nil {
		return err
	}
	next.importer.editor.Info.DocID = p.importer.editor.Info.DocID
	next.importer.editor.encryption = p.importer.editor.encryption
	p.Close()
	*p = *next
	return nil
}

// Close 释放PDF索引、引用缓存和转换状态，不关闭调用方的输入源
// 返回: error 关闭错误
func (p *PDFImport) Close() error {
	if p.importer == nil {
		return nil
	}
	p.importer.references.Close()
	err := p.importer.reader.Close()
	p.importer = nil
	p.pages, p.ready = nil, nil
	p.previews.clear()
	p.previews = nil
	clear(p.options.Password)
	p.options = PDFImportOptions{}
	p.input = nil
	return err
}

// ImportPDF 将PDF内容转换为独立OFD编辑文档，失败时不返回部分结果
// 返回的编辑文档独立于输入源，函数返回后可关闭输入源
// 入参: ctx 取消上下文, input 输入源, options 转换选项
// 返回: *Editor 编辑文档, PDFImportReport 转换统计, error 错误信息
func ImportPDF(ctx context.Context, input *Input, options PDFImportOptions) (*Editor, PDFImportReport, error) {
	pending, err := NewPDFImport(ctx, input, options)
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	defer pending.Close()
	return pending.Finish(ctx)
}

// pdfImportInput 将输入读取绑定到当前转换上下文
type pdfImportInput struct {
	input    *Input
	importer *pdfImporter
}

// ReadAt 读取当前操作需要的数据，不取得输入所有权
// 入参: data 目标缓冲, offset 文件偏移
// 返回: int 读取字节数, error 读取或取消错误
func (r *pdfImportInput) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.importer.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.ReadAt(data, offset)
}

// pdfImporter 保存单次转换的对象与资源状态
type pdfImporter struct {
	resources          *Resources
	ctx                context.Context
	reader             *pdfgo.Reader
	editor             *Editor
	renderer           *Renderer
	report             PDFImportReport
	matrix             pdfgo.Matrix
	page               int
	pageActionPage     int
	fontIDs            map[*pdfgo.Font]string
	fonts              map[*pdfgo.Font]*pdfImportedFont
	fontPrograms       map[pdfFontProgramKey]*pdfImportedFont
	symbolFonts        map[*pdfgo.Font]*pdfImportedFont
	fontWarnings       map[string]bool
	clipTexts          map[*pdfgo.TextClip][]TextObject
	textOrigins        map[*pdfgo.ReplacementText]pdfObjectOrigin
	clipPaths          *renderCache[[32]byte, pdfClipPath]
	cmykSpace          string
	rgbSpace           string
	iccSpaces          map[pdfICCKey]string
	objects            *pdfObjectBuffer
	structureBindings  *pdfStructureBindings
	annotationBindings []pdfStructureObjectBinding
	formResources      *pdfFormCache
	validation         *editorValidation
	pages              map[pdfgo.Reference]*pdfgo.Page
	pageOrder          []*pdfgo.Page
	pageIDs            map[pdfgo.Reference]string
	pageIndexes        map[pdfgo.Reference]int
	destinationBoxes   map[pdfgo.Reference]Box
	imageIDs           map[pdfImageKey][]pdfImageResource
	imageContents      *renderCache[pdfImageContentKey, pdfImageContent]
	attachmentIDs      map[pdfAttachmentKey]string
	modelAttachments   map[*pdfgo.Stream]string
	mediaResources     map[pdfMediaFileKey]pdfMediaResource
	mediaInstances     map[pdfMediaInstanceKey]pdfMediaResource
	movieBounds        map[*Movie]Box
	renditions         *pdfRenditionImport
	maskClips          map[*pdfgo.SoftMask]pdfgo.Path
	pageBox            pdfgo.Rectangle
	pageWidth          float64
	pageHeight         float64
	warning            func(pdfgo.Diagnostic)
	rasterDPI          float64
	rasterWarned       bool
	compositeCache     *pdfCompositeCache
	compositeNodes     []pdfCompositeNode
	compositeSpace     *pdfgo.ColorSpace
	patternCells       *renderCache[pdfPatternCellKey, pdfPatternCell]
	patternOrigins     map[*Pattern]pdfObjectOrigin
	annotationData     []byte
	resolveFile        pdfgo.FileResolver
	resolveReference   pdfgo.ReferenceResolver
	references         *codecpdf.ReferenceCache
	halftoneWarnings   map[*pdfgo.Halftone]bool
	halftones          map[string]*pdfgo.Halftone
	transferBackdrop   bool
	transferModel      pdfgo.Name
	compression        CompressionOptions
}

// referencePage 衔接PDF访问器与当前导入会话的引用文档缓存
// 入参: ctx 取消上下文, source 来源文档, reference 引用目标
// 返回: *pdfgo.Page 目标页面，不可用时为空, error 取消或调用方解析错误
func (p *pdfImporter) referencePage(ctx context.Context, source *pdfgo.Reader, reference pdfgo.ReferenceXObject) (*pdfgo.Page, error) {
	if p.references == nil {
		p.references = &codecpdf.ReferenceCache{}
	}
	return p.references.Resolve(ctx, source, reference, p.resolveFile, p.resolveReference, p.warning)
}

// pageAnnotations 保留不可显示的源注解声明，其他注解仍按原顺序读取
// 入参: ctx 取消上下文, page PDF页面
// 返回: []pdfgo.Annotation 可显示的注解, error 输入、取消或严格校验错误
func (p *pdfImporter) pageAnnotations(ctx context.Context, page *pdfgo.Page) ([]pdfgo.Annotation, error) {
	annotations, err := page.AnnotationsContext(ctx)
	if err == nil || p.warning == nil || !pdfAuxiliaryError(err) {
		return annotations, err
	}
	object := page.Dictionary["Annots"]
	if err := p.recoverPDFObject(ctx, "Page.Annots", object, err); err != nil {
		return nil, err
	}
	value, err := p.reader.Resolve(object)
	if err != nil {
		return nil, p.recoverPDFObject(ctx, "Page.Annots", object, err)
	}
	array, ok := value.(pdfgo.Array)
	if !ok {
		return nil, ctx.Err()
	}
	for index, object := range array {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := *page
		candidate.Dictionary = maps.Clone(page.Dictionary)
		candidate.Dictionary["Annots"] = pdfgo.Array{object}
		items, err := candidate.AnnotationsContext(ctx)
		if err != nil && pdfAuxiliaryError(err) {
			value, resolveErr := p.reader.Resolve(object)
			if resolveErr != nil && !pdfAuxiliaryError(resolveErr) {
				return nil, resolveErr
			}
			if dict, ok := value.(pdfgo.Dictionary); ok && dict["AP"] != nil {
				subtype, resolveErr := p.reader.Resolve(dict["Subtype"])
				if resolveErr != nil && !pdfAuxiliaryError(resolveErr) {
					return nil, resolveErr
				}
				if _, named := subtype.(pdfgo.Name); !named && resolveErr == nil {
					dict = maps.Clone(dict)
					dict["Subtype"] = pdfgo.Name("Stamp")
					candidate.Dictionary["Annots"] = pdfgo.Array{dict}
					items, err = candidate.AnnotationsContext(ctx)
				}
			}
		}
		if err != nil {
			if !pdfAuxiliaryError(err) {
				return nil, err
			}
			p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF annotation %d retained without appearance: %v", index+1, err)})
			continue
		}
		if len(items) == 1 && items[0].Reference == (pdfgo.Reference{}) {
			items[0].Reference, _ = object.(pdfgo.Reference)
		}
		annotations = append(annotations, items...)
	}
	return annotations, ctx.Err()
}

// objectValidation 在单次导入中共享不可变资源的校验结果，隔离局部编辑器
// 返回: *editorValidation 当前编辑器的构建校验会话
func (p *pdfImporter) objectValidation() *editorValidation {
	if p.validation == nil || p.validation.Editor != p.editor {
		p.validation = &editorValidation{Editor: p.editor}
	}
	return p.validation
}

// appendSequenceObject 追加对象并保留来源区段，供复合对象优化核对边界
// 入参: object 独立导入对象, sequence 来源区段
func (p *pdfImporter) appendSequenceObject(object GraphicObject, sequence *pdfgo.ContentSequence) {
	p.appendOriginObject(object, pdfObjectOrigin{sequence: sequence})
}

// appendOriginObject 追加对象并保留全部来源区段
// 入参: object 独立导入对象, origin 来源区段
func (p *pdfImporter) appendOriginObject(object GraphicObject, origin pdfObjectOrigin) {
	if len(p.patternOrigins) != 0 {
		var fill *FillColor
		var stroke *StrokeColor
		switch object.Type {
		case "PathObject":
			fill, stroke = object.PathObject.FillColor, object.PathObject.StrokeColor
		case "TextObject":
			fill, stroke = object.TextObject.FillColor, object.TextObject.StrokeColor
		}
		if fill != nil {
			if source, ok := p.patternOrigins[fill.Pattern]; ok {
				origin = origin.merge(source)
			}
		}
		if stroke != nil {
			if source, ok := p.patternOrigins[stroke.Pattern]; ok {
				origin = origin.merge(source)
			}
		}
	}
	if p.objects == nil {
		p.objects = &pdfObjectBuffer{}
	}
	p.objects.appendOrigin(object, origin)
}

// commitObjects 将当前页面待转换对象写入编辑文档
// 返回: error 对象写入错误
func (p *pdfImporter) commitObjects() error {
	if p.objects.len() == 0 {
		return nil
	}
	objects, err := p.objects.data(p.ctx)
	if err != nil {
		return err
	}
	objects, err = p.sharedClipObjects(objects)
	if err != nil {
		return err
	}
	var bindings []pdfStructureBinding
	if p.structureBindings != nil {
		bindings, err = p.objects.structureBindings(p.ctx)
		if err != nil {
			return err
		}
	}
	if err := p.objectValidation().appendOwnedObjects(p.ctx, p.page, objects); err != nil {
		return err
	}
	if p.structureBindings != nil {
		p.structureBindings.append(p.editor.pages[p.page].ID, objects, bindings)
	}
	p.objects = nil
	return nil
}

// pdfObjectBuffer 分块收集导入对象，提交前只整理一次连续数据
type pdfObjectBuffer struct {
	parts     [][]GraphicObject
	sequences []pdfObjectSequence
	size      int
}

// append 追加对象，小批量连续存储，超过阈值后不复制前序片段
// 入参: object 独立对象
func (b *pdfObjectBuffer) append(object GraphicObject) {
	b.appendSequence(object, nil)
}

// appendSequence 按区段连续存储对象，不为未标记内容分配区段记录
// 入参: object 独立对象, sequence 来源区段
func (b *pdfObjectBuffer) appendSequence(object GraphicObject, sequence *pdfgo.ContentSequence) {
	b.appendOrigin(object, pdfObjectOrigin{sequence: sequence})
}

// appendOrigin 按来源连续存储对象，混合区域保留多个区段
// 入参: object 独立对象, origin 来源区段
func (b *pdfObjectBuffer) appendOrigin(object GraphicObject, origin pdfObjectOrigin) {
	b.recordOrigin(b.size, origin)
	index := len(b.parts) - 1
	if index == 0 && len(b.parts[0]) == cap(b.parts[0]) && cap(b.parts[0]) < pdfObjectChunkLimit {
		part := b.parts[0]
		next := make([]GraphicObject, len(part), min(pdfObjectChunkLimit, max(1, cap(part)*2)))
		copy(next, part)
		b.parts[0] = next
	}
	if index < 0 || len(b.parts[index]) == cap(b.parts[index]) {
		capacity := pdfObjectChunkLimit
		if index < 0 {
			capacity = 1
		}
		b.parts = append(b.parts, make([]GraphicObject, 0, capacity))
		index++
	}
	b.parts[index] = append(b.parts[index], object)
	b.size++
}

// recordOrigin 按递增对象位置记录完整来源变化
// 入参: index 对象位置, origin 来源区段
func (b *pdfObjectBuffer) recordOrigin(index int, origin pdfObjectOrigin) {
	var previous pdfObjectOrigin
	if len(b.sequences) != 0 {
		previous = b.sequences[len(b.sequences)-1].pdfObjectOrigin
	}
	if !previous.equal(origin) {
		b.sequences = append(b.sequences, pdfObjectSequence{start: index, pdfObjectOrigin: origin})
	}
}

// origin 获取暂存对象的全部来源，未标记对象或空缓冲返回空来源
// 入参: index 对象位置
// 返回: pdfObjectOrigin 来源区段
func (b *pdfObjectBuffer) origin(index int) pdfObjectOrigin {
	if b == nil {
		return pdfObjectOrigin{}
	}
	next := sort.Search(len(b.sequences), func(i int) bool { return b.sequences[i].start > index })
	if next == 0 {
		return pdfObjectOrigin{}
	}
	return b.sequences[next-1].pdfObjectOrigin
}

// combinedOrigin 收集局部合成的来源区段，按首次出现顺序去重
// 入参: ctx 取消上下文
// 返回: pdfObjectOrigin 合并来源, error 取消错误
func (b *pdfObjectBuffer) combinedOrigin(ctx context.Context) (pdfObjectOrigin, error) {
	if err := ctx.Err(); err != nil {
		return pdfObjectOrigin{}, err
	}
	if b.len() == 0 {
		return pdfObjectOrigin{}, nil
	}
	origin := b.origin(0)
	if len(b.sequences) <= 1 && (len(b.sequences) == 0 || b.sequences[0].start == 0) {
		return origin, nil
	}
	var result pdfOriginSet
	result.add(origin.sequence)
	for _, sequence := range origin.related {
		result.add(sequence)
	}
	for _, run := range b.sequences {
		if err := ctx.Err(); err != nil {
			return pdfObjectOrigin{}, err
		}
		result.add(run.sequence)
		for _, sequence := range run.related {
			if err := ctx.Err(); err != nil {
				return pdfObjectOrigin{}, err
			}
			result.add(sequence)
		}
	}
	return result.origin, nil
}

// len 获取暂存对象数量，空缓冲返回零
// 返回: int 对象数量
func (b *pdfObjectBuffer) len() int {
	if b == nil {
		return 0
	}
	return b.size
}

// truncate 回滚到已有对象位置，清理丢弃对象及片段引用
// 入参: size 保留对象数量
func (b *pdfObjectBuffer) truncate(size int) {
	if size < 0 || size > b.len() {
		panic("invalid PDF object buffer size")
	}
	if size == b.len() {
		return
	}
	for len(b.sequences) > 0 && b.sequences[len(b.sequences)-1].start >= size {
		last := len(b.sequences) - 1
		b.sequences[last] = pdfObjectSequence{}
		b.sequences = b.sequences[:last]
	}
	remaining := size
	for index, part := range b.parts {
		if remaining <= len(part) {
			clear(part[remaining:])
			b.parts[index] = part[:remaining]
			clear(b.parts[index+1:])
			b.parts = b.parts[:index+1]
			b.size = size
			return
		}
		remaining -= len(part)
	}
}

// data 按绘制顺序整理独立对象数组，取消时保留原片段供重试
// 入参: ctx 取消上下文
// 返回: []GraphicObject 暂存数据, error 取消错误
func (b *pdfObjectBuffer) data(ctx context.Context) ([]GraphicObject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.len() == 0 {
		return nil, nil
	}
	if len(b.parts) == 1 {
		return b.parts[0][:b.size:b.size], nil
	}
	objects := make([]GraphicObject, b.size)
	offset := 0
	for _, part := range b.parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset += copy(objects[offset:], part)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.parts[0] = objects
	clear(b.parts[1:])
	b.parts = b.parts[:1]
	return objects, nil
}

// pdfObjectSequence 记录连续对象的来源区段，仅在区段变化时追加
type pdfObjectSequence struct {
	start int
	pdfObjectOrigin
}

// pdfObjectOrigin 保存图元的来源区段，混合区域同时保留参与合成的区段
type pdfObjectOrigin struct {
	sequence *pdfgo.ContentSequence
	related  []*pdfgo.ContentSequence
}

// equal 比较来源及其顺序，不合并不同来源的图元
// 入参: other 待比较来源
// 返回: bool 是否相同
func (o pdfObjectOrigin) equal(other pdfObjectOrigin) bool {
	return o.sequence == other.sequence && slices.Equal(o.related, other.related)
}

// merge 合并参与混色的来源，不修改已有区段切片
// 入参: other 另一来源
// 返回: pdfObjectOrigin 去重后的来源
func (o pdfObjectOrigin) merge(other pdfObjectOrigin) pdfObjectOrigin {
	if o.equal(other) {
		return o
	}
	result := o
	copied := false
	appendSequence := func(sequence *pdfgo.ContentSequence) {
		if sequence != result.sequence && !slices.Contains(result.related, sequence) {
			if !copied {
				result.related = slices.Clone(result.related)
				copied = true
			}
			result.related = append(result.related, sequence)
		}
	}
	appendSequence(other.sequence)
	for _, sequence := range other.related {
		appendSequence(sequence)
	}
	return result
}

// pdfOriginSet 按首次出现顺序收集来源，仅在出现不同区段时分配去重表
type pdfOriginSet struct {
	origin  pdfObjectOrigin
	seen    map[*pdfgo.ContentSequence]bool
	started bool
}

// add 记录参与绘制的区段，空区段表示未标记内容
// 入参: sequence 来源区段
func (s *pdfOriginSet) add(sequence *pdfgo.ContentSequence) {
	if !s.started {
		s.origin.sequence, s.started = sequence, true
		return
	}
	if sequence == s.origin.sequence || s.seen[sequence] {
		return
	}
	if s.seen == nil {
		s.seen = make(map[*pdfgo.ContentSequence]bool)
	}
	s.seen[sequence] = true
	s.origin.related = append(s.origin.related, sequence)
}
