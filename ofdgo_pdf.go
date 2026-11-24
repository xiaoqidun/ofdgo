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
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"math"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfObjectChunkLimit 限制导入暂存片段容量，pdfWarningPageLimit 限制同类告警的页级展开条数
const (
	pdfObjectChunkLimit = 256
	pdfWarningPageLimit = 8
)

// pdfWarningKey 按页码和原始消息聚合重复告警
type pdfWarningKey struct {
	page    int
	message string
}

// pdfWarningEntry 保存带重复计数的原始告警，供并发与注解合并使用
type pdfWarningEntry struct {
	diagnostic PDFDiagnostic
	count      int
}

// pdfWarningState 保存告警聚合快照，支持页面取消或收尾失败时完整回滚
type pdfWarningState struct {
	length int
	total  int
	counts []int
}

// ErrPDFPassword 表示PDF需要正确的用户或所有者密码
var ErrPDFPassword = errors.New("pdf password required")

// PDFDiagnostic 记录PDF导入过程中可恢复的问题或未保留的语义，Page从1开始，未知页码为0
// Offset为解码后当前页面或表单内容流内的字节位置
type PDFDiagnostic struct {
	Page    int
	Offset  int64
	Message string
}

// PDFFileSpecification 描述PDF引用的外部文件，不自动访问网络或本地路径
type PDFFileSpecification struct {
	Name        string
	Description string
	FileSystem  string
}

// PDFFileResolver 由调用方按文件说明读取外部文件字节
type PDFFileResolver func(context.Context, PDFFileSpecification) ([]byte, error)

// PDFReference 描述PDF引用表单的目标文件、页面选择及文件标识
// HasPageLabel为true时按PageLabel选择页面，否则使用从0开始的PageIndex
type PDFReference struct {
	File         PDFFileSpecification
	PageIndex    int64
	PageLabel    string
	HasPageLabel bool
	ID           [][]byte
}

// PDFReferenceSource 提供外部引用PDF的原始字节及可选解密参数
// Data为空时按PDF标准保留代理外观
type PDFReferenceSource struct {
	Data         []byte
	Password     []byte
	PasswordUTF8 bool
}

// PDFReferenceResolver 由调用方按引用描述提供外部目标PDF数据
type PDFReferenceResolver func(context.Context, PDFReference) (PDFReferenceSource, error)

// PDFImportOptions 指定PDF转换的密码、运行时后端、进度通知与检查策略
// Password默认使用PDF安全处理器编码，PasswordUTF8启用UTF-8输入及标准密码准备，空值尝试空密码
// Strict禁止日期兼容、恢复缺失资源或丢失内容语义，默认在报告中记录警告
// RendererOptions复用渲染器字体来源和后端配置，可通过WithRenderBackends指定自定义后端
// RasterDPI指定局部合成与三维绘制精度，0沿用渲染器DPI，默认300dpi，Strict禁用局部合成
// OnProgress按open、pages、convert及write.*阶段报告进度，total为0表示总量未知
// ResolveFile读取PDF引用的外部媒体、附件、页面及三维纹理，默认不访问网络或本地路径
// ResolveReference提供外部引用PDF字节与可选解密密码，Data为空时按标准保留代理外观
// Compression配置转换输出，有损模式按图片实际尺寸降采样，原始及无损模式保持完整采样
// Resources用于导入、局部合成与返回编辑器的共享资源预算
type PDFImportOptions struct {
	Resources        *Resources
	Password         []byte
	PasswordUTF8     bool
	RendererOptions  []RendererOption
	OnProgress       func(stage string, completed, total int) error
	Strict           bool
	RasterDPI        float64
	ResolveFile      PDFFileResolver
	ResolveReference PDFReferenceResolver
	Compression      CompressionOptions
}

// PDFImportReport 汇总转换页数、对象数、链接数和转换警告
type PDFImportReport struct {
	Pages        int
	TextObjects  int
	PathObjects  int
	ImageObjects int
	Links        int
	Warnings     []PDFDiagnostic
}

// PDFImport 按页转换PDF，实例需串行使用，输入源由调用方持有至Close
// 页面阅读快照独立于后续转换，Finish完成全局结构后交付独立编辑文档
// 转换失败后可用Reset重建，输入及回调在会话存续期间须保持有效
type PDFImport struct {
	input             *Input
	importer          *pdfImporter
	options           PDFImportOptions
	pages             []PageContent
	ready             []bool
	dupTemplates      map[int]string
	pageCompositeMeta map[int]pdfPageCompositeMeta
	previews          *renderCache[string, []byte]
	syncMu            *sync.RWMutex
	live              *pdfLiveReader
	targetSync        *pdfLiveReader
	err               error
	finished          bool
}

// pdfLiveReader 维护渐进PDF转换的常驻增量阅读器状态，采用写时复制兼容并发只读视图
type pdfLiveReader struct {
	mu             sync.RWMutex
	reader         *Reader
	syncedResCount int
	syncedPages    map[int]bool
	readyPages     map[string]bool
	sourceFiles    map[string][]byte
	syncedDestLen  int
	pageLocs       map[string]bool
	docResPath     string
}

// pdfImportInput 将输入读取绑定到当前转换上下文
type pdfImportInput struct {
	input    *Input
	importer *pdfImporter
}

// pdfImporter 保存单次转换的对象与资源状态
type pdfImporter struct {
	resources               *Resources
	ctx                     context.Context
	reader                  *pdfgo.Reader
	editor                  *Editor
	renderer                *Renderer
	report                  PDFImportReport
	matrix                  pdfgo.Matrix
	page                    int
	pageActionPage          int
	fontIDs                 map[*pdfgo.Font]string
	fonts                   map[*pdfgo.Font]*pdfImportedFont
	fontPrograms            map[pdfFontProgramKey]*pdfImportedFont
	symbolFonts             map[*pdfgo.Font]*pdfImportedFont
	fontWarnings            map[string]bool
	clipTexts               map[*pdfgo.TextClip]pdfClipTextEntry
	textOrigins             map[*pdfgo.ReplacementText]pdfObjectOrigin
	clipPaths               *renderCache[[32]byte, pdfClipPath]
	cmykSpace               string
	rgbSpace                string
	iccSpaces               map[pdfICCKey]string
	mergedColorSpaces       map[[32]byte]string
	pageBodyCounts          map[int][3]int
	objects                 *pdfObjectBuffer
	structureBindings       *pdfStructureBindings
	annotationBindings      []pdfStructureObjectBinding
	formResources           *pdfFormCache
	validation              *editorValidation
	pages                   map[pdfgo.Reference]*pdfgo.Page
	pageOrder               []*pdfgo.Page
	pageIDs                 map[pdfgo.Reference]string
	pageIndexes             map[pdfgo.Reference]int
	destinationBoxes        map[pdfgo.Reference]Box
	imageIDs                map[pdfImageKey][]pdfImageResource
	imageContents           *renderCache[pdfImageContentKey, pdfImageContent]
	attachmentIDs           map[pdfAttachmentKey]string
	modelAttachments        map[*pdfgo.Stream]string
	mediaResources          map[pdfMediaFileKey]pdfMediaResource
	mediaInstances          map[pdfMediaInstanceKey]pdfMediaResource
	movieBounds             map[*Movie]Box
	renditions              *pdfRenditionImport
	maskClips               map[*pdfgo.SoftMask]pdfgo.Path
	pageBox                 pdfgo.Rectangle
	pageWidth               float64
	pageHeight              float64
	warning                 func(pdfgo.Diagnostic)
	warningIndex            map[pdfWarningKey]int
	warningMsgPages         map[string]int
	warningCounts           []int
	warningBases            []string
	warningTotal            int
	rasterDPI               float64
	rasterWarned            bool
	compositeCache          *pdfCompositeCache
	compositeNodes          []pdfCompositeNode
	compositeSpace          *pdfgo.ColorSpace
	compositeBackdropPage   *pdfgo.Page
	compositeBackdropLoaded bool
	compositeBackdropNodes  []pdfCompositeNode
	patternCells            *renderCache[pdfPatternCellKey, pdfPatternCell]
	patternOrigins          map[*Pattern]pdfObjectOrigin
	annotationData          []byte
	resolveFile             pdfgo.FileResolver
	resolveReference        codecpdf.ReferenceSourceResolver
	references              *codecpdf.ReferenceCache
	usedReferences          bool
	halftoneWarnings        map[*pdfgo.Halftone]bool
	halftoneWarningMessages map[string]bool
	transferBackdrop        bool
	transferModel           pdfgo.Name
	compression             CompressionOptions
	imageWorkers            int
	imageScaleBoost         float64
	sharedImages            *pdfSharedImageCache
	formRefCounts           map[pdfgo.Reference]int
	seenFormPages           map[pdfgo.Reference]int
	pageSeenForms           map[pdfgo.Reference]bool
	localFormCounts         map[pdfFormIdentity]int
	neighborFormRefs        map[pdfgo.Reference]bool
	recordForms             bool
	formRecords             map[string]pdfFormRecord
	isolatedGroupImages     map[pdfIsolatedGroupKey]pdfIsolatedGroupImage
}

// pdfObjectBuffer 分块收集导入对象，提交前只整理一次连续数据
type pdfObjectBuffer struct {
	parts     [][]GraphicObject
	sequences []pdfObjectSequence
	size      int
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

// pdfOriginSet 按首次出现顺序收集来源，仅在出现不同区段时分配去重表
type pdfOriginSet struct {
	origin  pdfObjectOrigin
	seen    map[*pdfgo.ContentSequence]bool
	started bool
}

// pdfWorkerInput 为并发工作视图提供带取消检查的独立输入包装
type pdfWorkerInput struct {
	input *Input
	ctx   context.Context
}

// pdfPageCompositeMeta 保存页面正文混合空间及设备传递状态，供并行提交与重复页复用
type pdfPageCompositeMeta struct {
	space            *pdfgo.ColorSpace
	transferModel    pdfgo.Name
	transferBackdrop bool
}

// pdfPageWorkResult 保存单个工作协程完成的页面正文转换及增量资源
type pdfPageWorkResult struct {
	pageIndex        int
	workerSlot       int
	objects          []GraphicObject
	bindings         []pdfStructureBinding
	observed         []*pdfgo.ContentSequence
	addedResources   []editorResource
	addedFonts       map[string]*FontResource
	addedMetrics     map[string]FontMetrics
	addedForms       map[string]pdfFormRecord
	optimizedImages  map[[32]byte]CompressionOptions
	compositeSpace   *pdfgo.ColorSpace
	transferModel    pdfgo.Name
	transferBackdrop bool
	report           PDFImportReport
	warnings         []pdfWarningEntry
	needsSerial      bool
	err              error
}

// ConvertPDF 将PDF转换并写入OFD，转换阶段失败时不写入目标
// 入参: ctx 取消上下文, input 输入源, output OFD输出, options 转换选项
// 返回: PDFImportReport 转换统计, error 错误信息
func ConvertPDF(ctx context.Context, input *Input, output io.Writer, options PDFImportOptions) (PDFImportReport, error) {
	editor, report, err := ImportPDF(ctx, input, options)
	if err != nil {
		return PDFImportReport{}, err
	}
	defer editor.Close()
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
	_, err = editor.writeConvertedPDF(ctx, output, options.Compression)
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
	if options.RasterDPI != 0 {
		renderer.dpi = options.RasterDPI
	}
	if !finite(renderer.dpi) || renderer.dpi <= 0 {
		return nil, fmt.Errorf("invalid PDF raster DPI")
	}
	rasterDPI := renderer.dpi
	if options.RasterDPI == 0 && options.Compression.Mode == CompressionLossy && options.Compression.ImageDPI() > 0 {
		rasterDPI = min(rasterDPI, float64(options.Compression.ImageDPI()))
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
	importer := &pdfImporter{resources: options.Resources, ctx: ctx, editor: editor, renderer: renderer, rasterDPI: rasterDPI, page: -1, fontIDs: map[*pdfgo.Font]string{}, fonts: map[*pdfgo.Font]*pdfImportedFont{}, pages: map[pdfgo.Reference]*pdfgo.Page{}, pageIDs: map[pdfgo.Reference]string{}, structureBindings: &pdfStructureBindings{}}
	readOptions := pdfgo.ReaderOptions{Password: options.Password, PasswordUTF8: options.PasswordUTF8, ShapeText: importer.shapePDFText, Workers: conversionWorkers(ctx)}
	if !options.Strict {
		importer.warning = func(warning pdfgo.Diagnostic) {
			warning.Page = importer.page + 1
			importer.addWarning(warning)
		}
		readOptions.Warning = importer.warning
	}
	reader, err := pdfgo.NewReaderWithOptions(&pdfImportInput{input: input, importer: importer}, input.Size(), readOptions)
	if err != nil {
		return nil, wrapPDFOpenError(err)
	}
	defer func() {
		if !keep {
			reader.Close()
		}
	}()
	importer.reader = reader
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
			importer.addWarning(pdfgo.Diagnostic{Message: "PDF output intents preserved as metadata; not applied to rendering"})
		}
	}
	importer.pageIndexes = make(map[pdfgo.Reference]int)
	importer.resolveFile = adaptPDFFileResolver(options.ResolveFile)
	importer.resolveReference = adaptPDFReferenceResolver(options.ResolveReference)
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
		if options.Strict {
			if err := importer.prepareStructure(ctx); err != nil {
				return nil, fmt.Errorf("import PDF structure: %w", err)
			}
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
	return &PDFImport{input: input, importer: importer, options: options, pages: cloneEditorData(editor.pages), ready: make([]bool, len(editor.pages)), syncMu: &sync.RWMutex{}}, nil
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

// hashPDFObject 按确定性规范序列化PDF对象写入哈希，间接引用保留对象编号
// 入参: h 目标哈希器, obj PDF对象, depth 递归深度
// 返回: bool 是否可安全指纹化
func hashPDFObject(h hash.Hash, obj pdfgo.Object, depth int) bool {
	if depth > 16 {
		return false
	}
	var buf [8]byte
	writeU64 := func(v uint64) {
		binary.LittleEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	switch v := obj.(type) {
	case nil:
		h.Write([]byte{'N'})
	case pdfgo.Boolean:
		if v {
			h.Write([]byte{'B', 1})
		} else {
			h.Write([]byte{'B', 0})
		}
	case pdfgo.Integer:
		h.Write([]byte{'I'})
		writeU64(uint64(v))
	case pdfgo.Real:
		h.Write([]byte{'F'})
		writeU64(math.Float64bits(float64(v)))
	case pdfgo.Name:
		h.Write([]byte{'K'})
		writeU64(uint64(len(v)))
		h.Write([]byte(v))
	case pdfgo.String:
		h.Write([]byte{'S'})
		writeU64(uint64(len(v)))
		h.Write(v)
	case pdfgo.Reference:
		h.Write([]byte{'R'})
		writeU64(uint64(v.Number))
		writeU64(uint64(v.Generation))
	case pdfgo.Array:
		h.Write([]byte{'A'})
		writeU64(uint64(len(v)))
		for _, item := range v {
			if !hashPDFObject(h, item, depth+1) {
				return false
			}
		}
	case pdfgo.Dictionary:
		h.Write([]byte{'D'})
		writeU64(uint64(len(v)))
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeU64(uint64(len(k)))
			h.Write([]byte(k))
			if !hashPDFObject(h, v[pdfgo.Name(k)], depth+1) {
				return false
			}
		}
	case *pdfgo.Stream:
		if v == nil {
			h.Write([]byte{'N'})
			return true
		}
		h.Write([]byte{'M'})
		if !hashPDFObject(h, v.Dictionary, depth+1) {
			return false
		}
		writeU64(uint64(len(v.Data)))
		h.Write(v.Data)
	default:
		return false
	}
	return true
}

// rebindSequences 将工作视图采集的内容区段归属还原为主阅读器与原页面
// 入参: list 区段切片, from 工作阅读器, to 主阅读器, fromPage 工作页面, toPage 原页面
func rebindSequences(list []*pdfgo.ContentSequence, from, to *pdfgo.Reader, fromPage, toPage *pdfgo.Page) {
	seen := make(map[*pdfgo.ContentSequence]bool, len(list))
	for _, seq := range list {
		for cur := seq; cur != nil && !seen[cur]; cur = cur.Parent {
			seen[cur] = true
			if cur.Reader == from {
				cur.Reader = to
			}
			if cur.Page == fromPage {
				cur.Page = toPage
			}
		}
	}
}

// rebindSequence 沿父链还原单个内容区段的阅读器与页面归属
// 入参: seq 内容区段, from 工作阅读器, to 主阅读器, fromPage 工作页面, toPage 原页面
func rebindSequence(seq *pdfgo.ContentSequence, from, to *pdfgo.Reader, fromPage, toPage *pdfgo.Page) {
	for cur := seq; cur != nil; cur = cur.Parent {
		if cur.Reader == from {
			cur.Reader = to
		}
		if cur.Page == fromPage {
			cur.Page = toPage
		}
	}
}

// clearGraphicObjectID 递归清空对象及嵌套复合对象内部标识以便作为独立模板对象重新分配标识
// 入参: object 图形对象指针
func clearGraphicObjectID(object *GraphicObject) {
	switch object.Type {
	case "TextObject", "Text":
		object.TextObject.ID = ""
	case "PathObject", "Path":
		object.PathObject.ID = ""
	case "ImageObject":
		object.ImageObject.ID = ""
	case "CompositeObject":
		object.CompositeObject.ID = ""
	}
}

// colorSpaceResourceDigest 计算颜色空间类型、位深、调色板与ICC数据的确定性摘要
// 入参: space 颜色空间定义, profileData ICC配置文件字节
// 返回: [32]byte 颜色空间特征摘要
func colorSpaceResourceDigest(space ColorSpace, profileData []byte) [32]byte {
	h := sha256.New()
	var buf [8]byte
	writeU64 := func(v uint64) {
		binary.LittleEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	writeU64(uint64(len(space.Type)))
	h.Write([]byte(space.Type))
	writeU64(uint64(space.BitsPerComponent))
	writeU64(uint64(len(space.Palette)))
	for _, entry := range space.Palette {
		writeU64(uint64(len(entry)))
		h.Write([]byte(entry))
	}
	writeU64(uint64(len(profileData)))
	h.Write(profileData)
	var sum [32]byte
	h.Sum(sum[:0])
	return sum
}

// isPDFHalftoneDedupWarning 判断诊断信息是否为需要文档级去重的半色调告警
// 入参: message 诊断消息
// 返回: bool 是否需要文档级去重
func isPDFHalftoneDedupWarning(message string) bool {
	return message == "PDF continuous-tone appearance retained; device halftone not transferred to OFD" ||
		message == "PDF continuous-tone appearance retained; device halftone origin not transferred to OFD"
}

// isPDFFontDedupWarning 判断诊断信息是否为需要文档级去重的字体告警
// 入参: message 诊断消息
// 返回: bool 是否需要文档级去重
func isPDFFontDedupWarning(message string) bool {
	return strings.HasPrefix(message, "PDF font ") && strings.HasSuffix(message, " has invalid hinting instructions; design outlines and original font retained") ||
		strings.HasPrefix(message, "PDF embedded font ") && strings.HasSuffix(message, " uses glyph with reconstructed side bearing")
}

// formatPDFWarningMessage 按重复次数格式化告警消息
// 入参: base 原始告警文本, count 累计次数
// 返回: string 展示文本
func formatPDFWarningMessage(base string, count int) string {
	if count > 1 {
		return fmt.Sprintf("%s (repeated %d times)", base, count)
	}
	return base
}

// wrapPDFOpenError 将底层PDF密码错误包装为包级ErrPDFPassword
// 入参: err 打开PDF返回的错误
// 返回: error 包装后的错误
func wrapPDFOpenError(err error) error {
	if errors.Is(err, pdfgo.ErrPassword) {
		return fmt.Errorf("%w: %v", ErrPDFPassword, err)
	}
	return err
}

// pdfFileSpecificationFrom 将底层PDF文件说明转换为公开结构
// 入参: spec 底层文件说明
// 返回: PDFFileSpecification 公开文件说明
func pdfFileSpecificationFrom(spec pdfgo.FileSpecification) PDFFileSpecification {
	return PDFFileSpecification{
		Name:        spec.Name,
		Description: spec.Description,
		FileSystem:  string(spec.FileSystem),
	}
}

// adaptPDFFileResolver 将公开外部文件回调适配为底层PDF文件解析器
// 入参: resolver 公开外部文件回调
// 返回: pdfgo.FileResolver 底层文件解析器
func adaptPDFFileResolver(resolver PDFFileResolver) pdfgo.FileResolver {
	if resolver == nil {
		return nil
	}
	return func(ctx context.Context, spec pdfgo.FileSpecification) ([]byte, error) {
		return resolver(ctx, pdfFileSpecificationFrom(spec))
	}
}

// adaptPDFReferenceResolver 将公开引用解析回调适配为内部引用字节解析器
// 入参: resolver 公开引用解析回调
// 返回: codecpdf.ReferenceSourceResolver 内部引用字节解析器
func adaptPDFReferenceResolver(resolver PDFReferenceResolver) codecpdf.ReferenceSourceResolver {
	if resolver == nil {
		return nil
	}
	return func(ctx context.Context, reference pdfgo.ReferenceXObject) (codecpdf.ReferenceSource, error) {
		converted := PDFReference{
			File:      pdfFileSpecificationFrom(reference.File),
			PageIndex: reference.PageIndex,
		}
		if reference.PageLabel != nil {
			converted.PageLabel = *reference.PageLabel
			converted.HasPageLabel = true
		}
		if len(reference.ID) != 0 {
			converted.ID = make([][]byte, len(reference.ID))
			for i, id := range reference.ID {
				converted.ID[i] = bytes.Clone([]byte(id))
			}
		}
		source, err := resolver(ctx, converted)
		if err != nil {
			return codecpdf.ReferenceSource{}, err
		}
		return codecpdf.ReferenceSource{
			Data:         source.Data,
			Password:     source.Password,
			PasswordUTF8: source.PasswordUTF8,
		}, nil
	}
}

// newBaseVisitor 创建统一携带告警、引用解析与PostScript策略的基础访问器
// 返回: pdfgo.Visitor 基础访问器
func (p *pdfImporter) newBaseVisitor() pdfgo.Visitor {
	v := pdfgo.Visitor{Warning: p.warning, Reference: p.referencePage}
	if p.warning != nil {
		v.PostScript = func(pdfgo.PostScriptMark) error {
			p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PostScript XObject skipped in OFD conversion"})
			return nil
		}
	}
	return v
}

// addWarning 记录单次告警并自动聚合同类重复条目
// 入参: warning 诊断条目
func (p *pdfImporter) addWarning(warning pdfgo.Diagnostic) {
	p.addWarningCount(PDFDiagnostic{Page: warning.Page, Offset: warning.Offset, Message: warning.Message}, 1)
}

// addWarningCount 按指定次数累计同类告警，超页阈值时折叠至文档级汇总条目
// 入参: warning 原始诊断条目, count 出现次数
func (p *pdfImporter) addWarningCount(warning PDFDiagnostic, count int) {
	if count <= 0 {
		count = 1
	}
	base := warning.Message
	p.warningTotal += count
	key := pdfWarningKey{page: warning.Page, message: base}
	if idx, ok := p.warningIndex[key]; ok {
		p.warningCounts[idx] += count
		p.report.Warnings[idx].Message = formatPDFWarningMessage(p.warningBases[idx], p.warningCounts[idx])
		return
	}
	if warning.Page > 0 && p.warningMsgPages[base] >= pdfWarningPageLimit {
		summaryKey := pdfWarningKey{page: 0, message: base}
		if idx, ok := p.warningIndex[summaryKey]; ok {
			p.warningCounts[idx] += count
			p.report.Warnings[idx].Message = formatPDFWarningMessage(p.warningBases[idx], p.warningCounts[idx])
			return
		}
		warning.Page = 0
		key = summaryKey
	}
	if p.warningIndex == nil {
		p.warningIndex = make(map[pdfWarningKey]int)
	}
	if warning.Page > 0 {
		if p.warningMsgPages == nil {
			p.warningMsgPages = make(map[string]int)
		}
		p.warningMsgPages[base]++
	}
	idx := len(p.report.Warnings)
	p.warningIndex[key] = idx
	p.warningBases = append(p.warningBases, base)
	p.warningCounts = append(p.warningCounts, count)
	warning.Message = formatPDFWarningMessage(base, count)
	p.report.Warnings = append(p.report.Warnings, warning)
}

// warningEntries 导出带原始消息及计数的告警切片，供并发或注解合并复用
// 返回: []pdfWarningEntry 告警条目
func (p *pdfImporter) warningEntries() []pdfWarningEntry {
	if len(p.report.Warnings) == 0 {
		return nil
	}
	entries := make([]pdfWarningEntry, len(p.report.Warnings))
	for i, w := range p.report.Warnings {
		count := 1
		if i < len(p.warningCounts) {
			count = p.warningCounts[i]
		}
		if i < len(p.warningBases) {
			w.Message = p.warningBases[i]
		}
		entries[i] = pdfWarningEntry{diagnostic: w, count: count}
	}
	return entries
}

// resetWarnings 清空局部或工作Goroutine的告警聚合状态
func (p *pdfImporter) resetWarnings() {
	p.report.Warnings = nil
	p.warningIndex = nil
	p.warningMsgPages = nil
	p.warningCounts = nil
	p.warningBases = nil
	p.warningTotal = 0
}

// saveWarningState 保存当前告警列表及计数快照
// 返回: pdfWarningState 快照状态
func (p *pdfImporter) saveWarningState() pdfWarningState {
	return pdfWarningState{
		length: len(p.report.Warnings),
		total:  p.warningTotal,
		counts: slices.Clone(p.warningCounts),
	}
}

// restoreWarningState 按快照还原告警列表、计数及索引
// 入参: state 快照状态
func (p *pdfImporter) restoreWarningState(state pdfWarningState) {
	p.report.Warnings = p.report.Warnings[:state.length:state.length]
	if state.length <= len(p.warningBases) {
		p.warningBases = p.warningBases[:state.length:state.length]
	}
	p.warningCounts = state.counts
	p.warningTotal = state.total
	if state.length == 0 {
		p.warningIndex = nil
		p.warningMsgPages = nil
		return
	}
	p.warningIndex = make(map[pdfWarningKey]int, state.length)
	p.warningMsgPages = make(map[string]int)
	for i := range state.length {
		base := p.warningBases[i]
		count := p.warningCounts[i]
		p.report.Warnings[i].Message = formatPDFWarningMessage(base, count)
		p.warningIndex[pdfWarningKey{page: p.report.Warnings[i].Page, message: base}] = i
		if p.report.Warnings[i].Page > 0 {
			p.warningMsgPages[base]++
		}
	}
}

// remapGraphicObjects 递归重映射图形对象中的字体、图像、颜色空间及复合图形资源标识
// 入参: objects 待重映射对象切片, idMap 旧标识到新标识映射表
func remapGraphicObjects(objects []GraphicObject, idMap map[string]string) {
	remapID := func(id *string) {
		if id != nil && *id != "" {
			if next, ok := idMap[*id]; ok {
				*id = next
			}
		}
	}
	var remapColor func(color *FillColor)
	var remapClips func(clips *Clips)
	var remapOne func(obj *GraphicObject)
	remapShdColor := func(color *ShdColor) {
		if color != nil {
			remapID(&color.ColorSpace)
		}
	}
	remapColor = func(color *FillColor) {
		if color == nil {
			return
		}
		remapID(&color.ColorSpace)
		if color.AxialShd != nil {
			for i := range color.AxialShd.Segment {
				remapShdColor(&color.AxialShd.Segment[i].Color)
			}
		}
		if color.RadialShd != nil {
			for i := range color.RadialShd.Segment {
				remapShdColor(&color.RadialShd.Segment[i].Color)
			}
		}
		if color.GouraudShd != nil {
			for i := range color.GouraudShd.Point {
				remapShdColor(&color.GouraudShd.Point[i].Color)
			}
			remapShdColor(color.GouraudShd.BackColor)
		}
		if color.LaGouraudShd != nil {
			for i := range color.LaGouraudShd.Point {
				remapShdColor(&color.LaGouraudShd.Point[i].Color)
			}
			remapShdColor(color.LaGouraudShd.BackColor)
		}
		if color.Pattern != nil {
			cell := &color.Pattern.CellContent
			for i := range cell.Objects {
				remapOne(&cell.Objects[i])
			}
			for i := range cell.TextObject {
				obj := GraphicObject{Type: "TextObject", TextObject: cell.TextObject[i]}
				remapOne(&obj)
				cell.TextObject[i] = obj.TextObject
			}
			for i := range cell.PathObject {
				obj := GraphicObject{Type: "PathObject", PathObject: cell.PathObject[i]}
				remapOne(&obj)
				cell.PathObject[i] = obj.PathObject
			}
			for i := range cell.ImageObject {
				obj := GraphicObject{Type: "ImageObject", ImageObject: cell.ImageObject[i]}
				remapOne(&obj)
				cell.ImageObject[i] = obj.ImageObject
			}
			for i := range cell.CompositeObject {
				obj := GraphicObject{Type: "CompositeObject", CompositeObject: cell.CompositeObject[i]}
				remapOne(&obj)
				cell.CompositeObject[i] = obj.CompositeObject
			}
		}
	}
	remapClips = func(clips *Clips) {
		if clips == nil {
			return
		}
		for i := range clips.Clip {
			for j := range clips.Clip[i].Area {
				area := &clips.Clip[i].Area[j]
				remapID(&area.DrawParam)
				for k := range area.Text {
					remapID(&area.Text[k].Font)
					remapID(&area.Text[k].DrawParam)
					remapColor(area.Text[k].FillColor)
					remapColor((*FillColor)(area.Text[k].StrokeColor))
					remapClips(area.Text[k].Clips)
				}
				for k := range area.Path {
					remapID(&area.Path[k].DrawParam)
					remapColor(area.Path[k].FillColor)
					remapColor((*FillColor)(area.Path[k].StrokeColor))
					remapClips(area.Path[k].Clips)
				}
			}
		}
	}
	remapOne = func(obj *GraphicObject) {
		switch obj.Type {
		case "TextObject", "Text":
			remapID(&obj.TextObject.Font)
			remapID(&obj.TextObject.DrawParam)
			remapColor(obj.TextObject.FillColor)
			remapColor((*FillColor)(obj.TextObject.StrokeColor))
			remapClips(obj.TextObject.Clips)
		case "PathObject", "Path":
			remapID(&obj.PathObject.DrawParam)
			remapColor(obj.PathObject.FillColor)
			remapColor((*FillColor)(obj.PathObject.StrokeColor))
			remapClips(obj.PathObject.Clips)
		case "ImageObject":
			remapID(&obj.ImageObject.ResourceID)
			remapID(&obj.ImageObject.ImageMask)
			remapID(&obj.ImageObject.Substitution)
			remapID(&obj.ImageObject.DrawParam)
			remapClips(obj.ImageObject.Clips)
			if obj.ImageObject.Border != nil {
				remapColor((*FillColor)(obj.ImageObject.Border.BorderColor))
			}
		case "CompositeObject":
			remapID(&obj.CompositeObject.ResourceID)
			remapID(&obj.CompositeObject.DrawParam)
			remapClips(obj.CompositeObject.Clips)
		}
	}
	for i := range objects {
		remapOne(&objects[i])
	}
}

// cloneMemoryPageContent 深拷贝单页内容，仅保留Objects供常驻阅读器零XML往返直挂
// 入参: ctx 取消上下文, page 编辑器内存页面
// 返回: *PageContent 独立隔离的页面内容副本, error 取消错误
func cloneMemoryPageContent(ctx context.Context, page PageContent) (*PageContent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	copied := page
	copied.XMLName = xml.Name{Space: ofdNamespace2016, Local: "Page"}
	copied.pageResources = nil
	copied.Template = slices.Clone(page.Template)
	copied.PageRes = slices.Clone(page.PageRes)
	copied.Actions = cloneEditorData(page.Actions)
	if len(page.Content.Layer) != 0 {
		layers := make([]Layer, len(page.Content.Layer))
		for i, srcLayer := range page.Content.Layer {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			layer := Layer{ID: srcLayer.ID, Type: srcLayer.Type, DrawParam: srcLayer.DrawParam}
			if len(srcLayer.Objects) != 0 {
				objects := make([]GraphicObject, len(srcLayer.Objects))
				for j, object := range srcLayer.Objects {
					if j&255 == 0 {
						if err := ctx.Err(); err != nil {
							return nil, err
						}
					}
					objects[j] = copyEditorGraphicObject(object)
				}
				layer.Objects = objects
			} else {
				layer = copyEditorLayer(srcLayer)
			}
			layers[i] = layer
		}
		copied.Content.Layer = layers
	} else {
		copied.Content.Layer = nil
	}
	return &copied, ctx.Err()
}

// isPDFImportCanceled 判断导入错误是否源于上下文取消或超时
// 入参: ctx 取消上下文, err 返回错误
// 返回: bool 是否为取消或超时错误
func isPDFImportCanceled(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx != nil && ctx.Err() != nil)
}

// pdfUsesDefaultBackends 判断渲染器全部后端是否均为默认实现，决定是否启用并行页面导入
// 入参: b 渲染器后端组合
// 返回: bool 是否全为默认后端
func pdfUsesDefaultBackends(b RenderBackends) bool {
	def := defaultRenderBackends()
	return sameBackend(b.FontResources, def.FontResources) &&
		sameBackend(b.Fonts, def.Fonts) &&
		sameBackend(b.Geometry, def.Geometry) &&
		sameBackend(b.Compiler, def.Compiler) &&
		sameBackend(b.Raster, def.Raster) &&
		sameBackend(b.SVG, def.SVG) &&
		sameBackend(b.PDF, def.PDF) &&
		sameBackend(b.EPS, def.EPS)
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
	importer := p.importer
	savedPage := cloneEditorData(importer.editor.pages[index])
	savedSource := importer.editor.source
	savedOwnedOwner := importer.editor.ownedOwner
	if savedSource != nil {
		importer.editor.ownedOwner = nil
	}
	savedOrigins := maps.Clone(importer.editor.origins)
	savedDestinations := importer.editor.pdfDestinations.clone()
	savedAttachmentIDs := maps.Clone(importer.attachmentIDs)
	savedMediaResources := maps.Clone(importer.mediaResources)
	savedMediaInstances := maps.Clone(importer.mediaInstances)
	savedModelAttachments := maps.Clone(importer.modelAttachments)
	savedFontWarnings := maps.Clone(importer.fontWarnings)
	savedHalftoneWarnings := maps.Clone(importer.halftoneWarnings)
	savedHalftoneMessages := maps.Clone(importer.halftoneWarningMessages)
	savedIsolatedGroupImages := maps.Clone(importer.isolatedGroupImages)
	savedUsedReferences := importer.usedReferences
	savedRenditionsHad := importer.renditions != nil
	var savedRenditionMedia map[pdfgo.Reference][]pdfRenditionResource
	if savedRenditionsHad {
		savedRenditionMedia = maps.Clone(importer.renditions.media)
	}
	savedReport := importer.report
	savedWarnings := importer.saveWarningState()
	_, hadBodyCounts := importer.pageBodyCounts[index]
	savedStructure := importer.structureBindings.snapshot()
	defer func() {
		if err == nil {
			if importer.editor.source == savedSource {
				importer.editor.ownedOwner = savedOwnedOwner
			}
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			importer.editor.pages[index] = savedPage
			importer.editor.source = savedSource
			importer.editor.origins = savedOrigins
			importer.editor.ownedOwner = savedOwnedOwner
			importer.editor.pdfDestinations = savedDestinations
			importer.editor.previewPages = nil
			importer.attachmentIDs = savedAttachmentIDs
			importer.mediaResources = savedMediaResources
			importer.mediaInstances = savedMediaInstances
			importer.modelAttachments = savedModelAttachments
			importer.fontWarnings = savedFontWarnings
			importer.halftoneWarnings = savedHalftoneWarnings
			importer.halftoneWarningMessages = savedHalftoneMessages
			importer.isolatedGroupImages = savedIsolatedGroupImages
			importer.usedReferences = savedUsedReferences
			if !savedRenditionsHad {
				importer.renditions = nil
			} else if importer.renditions != nil {
				importer.renditions.media = savedRenditionMedia
			}
			importer.clearPageState()
			importer.report = savedReport
			importer.restoreWarningState(savedWarnings)
			if !hadBodyCounts {
				delete(importer.pageBodyCounts, index)
			}
			if importer.structureBindings != nil {
				importer.structureBindings.rollbackPage(savedPage.ID, importer.pageOrder[index].Reference, savedStructure)
			}
			p.ready[index] = false
			return
		}
		p.err = err
	}()
	ctx = importer.resources.context(ctx)
	page := importer.pageOrder[index]
	importer.beginPage(ctx, index, page)
	if err := importer.pageOpenActions(ctx, page); err != nil {
		return fmt.Errorf("import PDF page %d actions: %w", index+1, err)
	}
	baseT, baseP, baseI := importer.report.TextObjects, importer.report.PathObjects, importer.report.ImageObjects
	if err := importer.compositePage(nil, func(visitor pdfgo.Visitor) error { return importer.reader.WalkPage(ctx, page, visitor) }); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	bodyMeta := pdfPageCompositeMeta{space: importer.compositeSpace, transferModel: importer.transferModel, transferBackdrop: importer.transferBackdrop}
	if importer.pageBodyCounts == nil {
		importer.pageBodyCounts = make(map[int][3]int)
	}
	importer.pageBodyCounts[index] = [3]int{
		importer.report.TextObjects - baseT,
		importer.report.PathObjects - baseP,
		importer.report.ImageObjects - baseI,
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
	importer.commitSeenForms(index)
	if p.pageCompositeMeta == nil {
		p.pageCompositeMeta = make(map[int]pdfPageCompositeMeta)
	}
	p.pageCompositeMeta[index] = bodyMeta
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
	if p.live == nil {
		skeleton, err := p.importer.editor.skeletonReader(ctx)
		if err != nil {
			return nil, err
		}
		next := &pdfLiveReader{}
		next.bind(skeleton)
		if p.syncMu != nil {
			p.syncMu.Lock()
			p.live = next
			p.syncMu.Unlock()
		} else {
			p.live = next
		}
	}
	if err := p.live.sync(ctx, p.importer.editor, index); err != nil {
		return nil, err
	}
	return p.live.snapshot(index), nil
}

// SyncReader 按页增量同步已转换页面与新增资源到常驻阅读器，保留渲染器已有缓存
// 入参: ctx 取消上下文, target 常驻阅读器, index 目标页面索引，-1仅同步文档级新增资源
// 返回: error 转换或同步错误
func (p *PDFImport) SyncReader(ctx context.Context, target *Reader, index int) error {
	if p.importer == nil {
		return fmt.Errorf("PDF import is closed")
	}
	if p.err != nil {
		return p.err
	}
	if target == nil || target.doc == nil {
		return fmt.Errorf("target reader is not initialized")
	}
	if index != -1 {
		if err := p.ConvertPage(ctx, index); err != nil {
			return err
		}
	}
	if p.targetSync == nil || p.targetSync.reader != target {
		next := &pdfLiveReader{}
		next.bind(target)
		if p.syncMu != nil {
			p.syncMu.Lock()
			p.targetSync = next
			p.syncMu.Unlock()
		} else {
			p.targetSync = next
		}
	}
	return p.targetSync.sync(ctx, p.importer.editor, index)
}

// PageSynced 返回指定页面是否已增量同步到目标常驻阅读器
// 入参: target 常驻阅读器, index 页面索引，从0开始
// 返回: bool 页面是否已同步
func (p *PDFImport) PageSynced(target *Reader, index int) bool {
	if p == nil || target == nil || target.doc == nil || index < 0 || index >= len(target.doc.Pages.Page) {
		return false
	}
	if p.syncMu != nil {
		p.syncMu.RLock()
		defer p.syncMu.RUnlock()
	}
	if matched, synced := p.targetSync.pageSynced(target, index); matched {
		return synced
	}
	return target.pageReady != nil && target.pageReady[target.doc.Pages.Page[index].ID]
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
	if p.live == nil {
		skeleton, err := p.importer.editor.skeletonReader(ctx)
		if err != nil {
			return nil, err
		}
		next := &pdfLiveReader{}
		next.bind(skeleton)
		if p.syncMu != nil {
			p.syncMu.Lock()
			p.live = next
			p.syncMu.Unlock()
		} else {
			p.live = next
		}
	}
	if err := p.live.sync(ctx, p.importer.editor, index); err != nil {
		return nil, err
	}
	if renderer == nil {
		renderer = p.importer.renderer
	}
	renderer = renderer.childRenderer(p.live.reader)
	return renderer.PageTextContext(ctx, &p.importer.editor.pages[index])
}

// PDFDestination 准备链接来源页并查询实时PDF定位语义，不修改已交付的阅读快照
// 入参: ctx 取消上下文, index 链接来源页，-1仅查询文档动作, dest OFD动作目标
// 返回: PDFDestinationTarget 定位目标, bool 是否存在, error 页面转换或关闭错误
func (p *PDFImport) PDFDestination(ctx context.Context, index int, dest Dest) (PDFDestinationTarget, bool, error) {
	if p.importer == nil {
		return PDFDestinationTarget{}, false, fmt.Errorf("PDF import is closed")
	}
	if p.err != nil {
		return PDFDestinationTarget{}, false, p.err
	}
	if err := ctx.Err(); err != nil {
		return PDFDestinationTarget{}, false, err
	}
	if index != -1 {
		if err := p.ConvertPage(ctx, index); err != nil {
			return PDFDestinationTarget{}, false, err
		}
	}
	if targets := p.importer.editor.pdfDestinations; targets != nil {
		target, ok := targets.targets[pdfDestinationKey(dest)]
		return target, ok, nil
	}
	return PDFDestinationTarget{}, false, nil
}

// Finish 转换尚未就绪的页面并补全文档结构，返回独立编辑文档
// 入参: ctx 取消上下文
// 返回: *Editor 编辑文档, PDFImportReport 转换统计, error 转换错误
func (p *PDFImport) Finish(ctx context.Context) (*Editor, PDFImportReport, error) {
	if p.importer == nil {
		return nil, PDFImportReport{}, fmt.Errorf("PDF import is closed")
	}
	if p.err != nil {
		return nil, PDFImportReport{}, p.err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, PDFImportReport{}, err
	}
	if !p.finished {
		p.importer.ctx = p.importer.resources.context(ctx)
		if err := p.importer.prepareStructure(p.importer.ctx); err != nil {
			if !isPDFImportCanceled(ctx, err) {
				p.err = err
			}
			return nil, PDFImportReport{}, fmt.Errorf("import PDF structure: %w", err)
		}
	}
	if err := p.finishPages(ctx); err != nil {
		if !isPDFImportCanceled(ctx, err) {
			p.err = err
		}
		return nil, PDFImportReport{}, err
	}
	if !p.finished {
		importer := p.importer
		editor := importer.editor
		savedPages := cloneEditorData(editor.pages)
		savedResources := slices.Clone(editor.resources)
		savedMaxID := editor.maxID
		savedSource := editor.source
		savedOwnedReader := editor.ownedReader
		savedOwnedOwner := editor.ownedOwner
		savedOwnedTx := editor.ownedTx
		savedOrigins := maps.Clone(editor.origins)
		savedReport := importer.report
		savedWarnings := importer.saveWarningState()
		savedStructure := importer.structureBindings.snapshotFinish()
		if savedSource != nil {
			editor.ownedOwner = nil
		}
		rollbackFinish := func() {
			editor.pages = savedPages
			editor.resources = savedResources
			editor.maxID = savedMaxID
			editor.source = savedSource
			editor.ownedReader = savedOwnedReader
			editor.ownedOwner = savedOwnedOwner
			editor.ownedTx = savedOwnedTx
			editor.origins = savedOrigins
			editor.previewPages = nil
			editor.nameCache = nil
			importer.report = savedReport
			importer.restoreWarningState(savedWarnings)
			if importer.structureBindings != nil {
				importer.structureBindings.rollbackFinish(savedStructure)
			}
			p.invalidateLiveAllPages()
		}
		importer.ctx = importer.resources.context(ctx)
		if err := importer.promoteTemplatePages(importer.ctx); err != nil {
			if isPDFImportCanceled(ctx, err) {
				rollbackFinish()
			} else {
				p.err = err
			}
			return nil, PDFImportReport{}, fmt.Errorf("promote PDF templates: %w", err)
		}
		p.invalidateLiveAllPages()
		if err := importer.documentStructures(importer.ctx); err != nil {
			if isPDFImportCanceled(ctx, err) {
				rollbackFinish()
			} else {
				p.err = err
			}
			return nil, PDFImportReport{}, fmt.Errorf("import PDF structure: %w", err)
		}
		if editor.source == savedSource {
			editor.ownedOwner = savedOwnedOwner
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
	next.importer.editor.syncEncryptionHold()
	p.Close()
	*p = *next
	return nil
}

// Close 释放PDF索引、引用缓存和转换状态，不关闭调用方的输入源
// 返回: error 关闭错误
func (p *PDFImport) Close() error {
	if p == nil || p.importer == nil {
		return nil
	}
	if p.live != nil && p.live.reader != nil {
		_ = p.live.reader.Close()
	}
	if !p.finished && p.importer.editor != nil {
		_ = p.importer.editor.Close()
	}
	p.importer.references.Close()
	err := p.importer.reader.Close()
	p.importer = nil
	p.pages, p.ready, p.dupTemplates, p.pageCompositeMeta = nil, nil, nil, nil
	p.previews.clear()
	p.previews = nil
	if p.syncMu != nil {
		p.syncMu.Lock()
		p.live, p.targetSync = nil, nil
		p.syncMu.Unlock()
	} else {
		p.live, p.targetSync = nil, nil
	}
	clear(p.options.Password)
	p.options = PDFImportOptions{}
	p.input = nil
	return err
}

// bind 为常驻阅读器初始化增量同步状态
// 入参: reader 常驻阅读器
func (l *pdfLiveReader) bind(reader *Reader) {
	pageLocs := make(map[string]bool, len(reader.doc.Pages.Page))
	for _, page := range reader.doc.Pages.Page {
		pageLocs[cleanPackagePath(reader.ResPath(page.BaseLoc))] = true
	}
	l.mu.Lock()
	l.reader = reader
	l.syncedPages = make(map[int]bool)
	l.readyPages = maps.Clone(reader.pageReady)
	l.sourceFiles = make(map[string][]byte)
	l.pageLocs = pageLocs
	l.docResPath = reader.ResPath("DocumentRes.xml")
	l.mu.Unlock()
}

// isSynced 线程安全地检查指定页面是否已在常驻阅读器中完成同步
// 入参: index 零基页面索引
// 返回: bool 是否已同步
func (l *pdfLiveReader) isSynced(index int) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	synced := l.syncedPages[index]
	l.mu.RUnlock()
	return synced
}

// pageSynced 线程安全地校验目标阅读器匹配关系及页面同步状态
// 入参: target 常驻阅读器, index 零基页面索引
// 返回: bool 目标阅读器是否匹配, bool 页面是否已同步或就绪
func (l *pdfLiveReader) pageSynced(target *Reader, index int) (bool, bool) {
	if l == nil {
		return false, false
	}
	l.mu.RLock()
	if l.reader != target {
		l.mu.RUnlock()
		return false, false
	}
	synced := l.syncedPages[index] || (l.readyPages != nil && l.readyPages[target.doc.Pages.Page[index].ID])
	l.mu.RUnlock()
	return true, synced
}

// invalidatePages 线程安全地移除受模板变更影响的页面同步标记
// 入参: pages 受影响的零基页面索引列表
func (l *pdfLiveReader) invalidatePages(pages ...int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	for _, idx := range pages {
		delete(l.syncedPages, idx)
	}
	l.mu.Unlock()
}

// invalidateAll 线程安全地清空全部已同步页面标记
func (l *pdfLiveReader) invalidateAll() {
	if l == nil {
		return
	}
	l.mu.Lock()
	clear(l.syncedPages)
	l.mu.Unlock()
}

// sync 将新增页面、资源、注解、标引及目标映射以准备和原子提交两阶段同步到常驻阅读器
// 入参: ctx 取消上下文, editor 导入编辑器, index 目标页面索引，-1仅同步资源与元信息
// 返回: error 编码、加载或取消错误
func (l *pdfLiveReader) sync(ctx context.Context, editor *Editor, index int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target := l.reader
	needPage := index >= 0 && !l.isSynced(index)
	needRes := len(editor.resources) > l.syncedResCount
	needSource := editor.source != nil && editor.source.reader != nil && (len(editor.source.reader.files) > 0 || editor.source.document != nil && len(editor.source.document.CommonData.TemplatePage) != len(target.doc.CommonData.TemplatePage))
	needDest := editor.pdfDestinations != nil && len(editor.pdfDestinations.targets) != l.syncedDestLen
	if !needPage && !needRes && !needSource && !needDest && target.encryption == editor.encryption {
		return nil
	}
	var nextFiles map[string][]byte
	var nextNames map[string]bool
	var nextReady map[string]bool
	ensureMaps := func() {
		if nextFiles == nil {
			nextFiles = maps.Clone(target.files)
			if nextFiles == nil {
				nextFiles = make(map[string][]byte)
			}
		}
		if nextNames == nil {
			nextNames = maps.Clone(target.fileNames)
			if nextNames == nil {
				nextNames = make(map[string]bool)
			}
		}
		if nextReady == nil {
			nextReady = maps.Clone(target.pageReady)
			if nextReady == nil {
				nextReady = make(map[string]bool)
			}
		}
	}
	var pendingFonts []*Font
	var pendingMedias []MultiMedia
	var pendingSpaces []*ColorSpace
	var pendingTemplates []TemplatePage
	var defPaths []string
	nextResCount := l.syncedResCount
	if needRes {
		ensureMaps()
		nextResCount = len(editor.resources)
		for _, res := range editor.resources[l.syncedResCount:nextResCount] {
			if err := ctx.Err(); err != nil {
				return err
			}
			clean := ""
			if res.name != "" {
				clean = cleanPackagePath(res.name)
				if res.data != nil {
					nextFiles[clean] = res.data
					nextNames[clean] = true
				}
			}
			if tpl, ok := parseOwnedTemplateResource(res); ok {
				pendingTemplates = append(pendingTemplates, tpl)
			}
			if res.font != nil {
				f := *res.font
				if f.FontFile != "" {
					f.FontFile = target.resourceLocation(l.docResPath, "Res", f.FontFile)
				}
				pendingFonts = append(pendingFonts, &f)
			}
			if res.image != nil {
				mm := *res.image
				if mm.MediaFile != "" {
					if finalPath := target.resourceLocation(l.docResPath, "Res", mm.MediaFile); finalPath != "" {
						mm.MediaFile = finalPath
						pendingMedias = append(pendingMedias, mm)
					}
				}
			}
			if res.space != nil {
				cs := *res.space
				if cs.Profile != "" {
					cs.Profile = target.resourceLocation(l.docResPath, "Res", cs.Profile)
				}
				pendingSpaces = append(pendingSpaces, &cs)
			}
			if res.definition() != "" && clean != "" {
				defPaths = append(defPaths, "/"+clean)
			}
		}
	}
	var pendingSourceFiles map[string][]byte
	updateAnnotations := false
	var pendingAnnotations string
	updateAttachments := false
	var pendingAttachments string
	updateCustomTags := false
	var pendingCustomTags string
	updateActions := false
	var pendingActions []Action
	if needSource {
		srcChanged := false
		for name, data := range editor.source.reader.files {
			if err := ctx.Err(); err != nil {
				return err
			}
			if l.pageLocs[name] || name == "OFD.xml" || name == "Doc_0/Document.xml" || name == target.documentRoot {
				continue
			}
			if prev, ok := l.sourceFiles[name]; ok && sameByteSlice(prev, data) {
				continue
			}
			ensureMaps()
			nextFiles[name] = data
			nextNames[name] = true
			if pendingSourceFiles == nil {
				pendingSourceFiles = make(map[string][]byte)
			}
			pendingSourceFiles[name] = data
			srcChanged = true
		}
		if srcDoc := editor.source.document; srcDoc != nil {
			if len(srcDoc.CommonData.TemplatePage) != len(target.doc.CommonData.TemplatePage) {
				pendingTemplates = append(pendingTemplates, srcDoc.CommonData.TemplatePage...)
			}
			if srcDoc.Annotations != "" && (target.doc.Annotations != srcDoc.Annotations || srcChanged) {
				updateAnnotations = true
				pendingAnnotations = srcDoc.Annotations
			}
			if srcDoc.Attachments.Path != "" && (target.doc.Attachments.Path != srcDoc.Attachments.Path || srcChanged) {
				updateAttachments = true
				pendingAttachments = srcDoc.Attachments.Path
			}
			if srcDoc.CustomTags.Path != "" && (target.doc.CustomTags.Path != srcDoc.CustomTags.Path || srcChanged) {
				updateCustomTags = true
				pendingCustomTags = srcDoc.CustomTags.Path
			}
			if len(srcDoc.Actions) != len(target.doc.Actions) {
				updateActions = true
				pendingActions = cloneEditorData(srcDoc.Actions)
			}
		}
	}
	var pendingMemPath string
	var pendingMemPage *PageContent
	var pendingHeader PageContent
	if needPage {
		page := target.doc.Pages.Page[index]
		pagePath := cleanPackagePath(target.ResPath(page.BaseLoc))
		memPage, err := cloneMemoryPageContent(ctx, editor.pages[index])
		if err != nil {
			return err
		}
		ensureMaps()
		delete(nextFiles, pagePath)
		nextNames[pagePath] = true
		nextReady[page.ID] = true
		pendingMemPath = pagePath
		pendingMemPage = memPage
		pendingHeader = PageContent{Area: memPage.Area, Template: slices.Clone(memPage.Template), PageRes: slices.Clone(memPage.PageRes)}
	}
	var pendingDest *pdfDestinations
	pendingDestLen := l.syncedDestLen
	if needDest {
		pendingDest = editor.pdfDestinations.clone()
		pendingDestLen = len(editor.pdfDestinations.targets)
	}
	var pendingParsed []*parsedRes
	if len(defPaths) > 0 {
		parseView := Reader{
			ofd:           target.ofd,
			rootDir:       target.rootDir,
			documentIndex: target.documentIndex,
			documentRoot:  target.documentRoot,
			files:         nextFiles,
			fileIndex:     target.fileIndex,
			encrypted:     target.encryptedSource(),
			resources:     target.resources,
			resFileCache:  make(map[string]*parsedRes, len(defPaths)),
		}
		for _, defPath := range defPaths {
			fullPath := target.ResPath(defPath)
			if target.resourcesRead[fullPath] {
				continue
			}
			parsed, err := parseView.parseResFileContext(ctx, fullPath)
			if err != nil {
				return err
			}
			if parsed != nil {
				pendingParsed = append(pendingParsed, parsed)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pendingFonts) != 0 || len(pendingMedias) != 0 || len(pendingSpaces) != 0 {
		if target.resourceFilesByKind == nil {
			target.resourceFilesByKind = make(map[string]map[string]string)
		}
		recordKind := func(kind, id string) {
			bucket := target.resourceFilesByKind[kind]
			if bucket == nil {
				bucket = make(map[string]string)
				target.resourceFilesByKind[kind] = bucket
			}
			bucket[id] = l.docResPath
		}
		if len(pendingFonts) != 0 {
			if target.docFonts == nil {
				target.docFonts = &resourceIndex[*Font]{}
			}
			if target.fontCache == nil {
				target.fontCache = make(map[string]*Font, len(pendingFonts))
			}
			for _, f := range pendingFonts {
				target.docFonts.put(f.ID, f)
				target.fontCache[f.ID] = f
				recordKind("Font", f.ID)
			}
		}
		if len(pendingMedias) != 0 {
			if target.docMedias == nil {
				target.docMedias = &resourceIndex[MultiMedia]{}
			}
			if target.mediaCache == nil {
				target.mediaCache = make(map[string]MultiMedia, len(pendingMedias))
			}
			if target.docResPaths == nil {
				target.docResPaths = &resourceIndex[string]{}
			}
			if target.resMap == nil {
				target.resMap = make(map[string]string, len(pendingMedias))
			}
			for _, mm := range pendingMedias {
				target.docMedias.put(mm.ID, mm)
				target.mediaCache[mm.ID] = mm
				target.docResPaths.put(mm.ID, mm.MediaFile)
				target.resMap[mm.ID] = mm.MediaFile
				recordKind("MultiMedia", mm.ID)
			}
		}
		if len(pendingSpaces) != 0 {
			if target.docColorSpaces == nil {
				target.docColorSpaces = &resourceIndex[*ColorSpace]{}
			}
			if target.colorSpaceCache == nil {
				target.colorSpaceCache = make(map[string]*ColorSpace, len(pendingSpaces))
			}
			for _, cs := range pendingSpaces {
				target.docColorSpaces.put(cs.ID, cs)
				target.colorSpaceCache[cs.ID] = cs
				recordKind("ColorSpace", cs.ID)
			}
		}
	}
	if len(pendingParsed) != 0 {
		loadState := &resLoadState{}
		for _, parsed := range pendingParsed {
			target.applyParsedRes(parsed, loadState)
		}
	}
	if len(pendingSourceFiles) != 0 {
		maps.Copy(l.sourceFiles, pendingSourceFiles)
	}
	if len(pendingTemplates) != 0 {
		existingTpl := make(map[string]bool, len(target.doc.CommonData.TemplatePage))
		for _, tpl := range target.doc.CommonData.TemplatePage {
			existingTpl[tpl.ID] = true
		}
		for _, tpl := range pendingTemplates {
			if !existingTpl[tpl.ID] {
				target.doc.CommonData.TemplatePage = append(target.doc.CommonData.TemplatePage, tpl)
				existingTpl[tpl.ID] = true
			}
		}
	}
	if updateAnnotations {
		target.doc.Annotations = pendingAnnotations
		target.relatedPending = true
		target.annotIndexLoaded = false
		target.annots = nil
		target.pageAnnotsLoaded = nil
		target.annotationFiles = nil
		target.pageAnnotFiles = nil
		target.pageAnnotCanon = nil
		target.annotCanon = nil
		target.annotCanonLen = 0
	}
	if updateAttachments {
		target.doc.Attachments = Attachments{Path: pendingAttachments}
	}
	if updateCustomTags {
		target.doc.CustomTags = CustomTags{Path: pendingCustomTags}
	}
	if updateActions {
		target.doc.Actions = pendingActions
	}
	if needDest {
		target.pdfDestinations = pendingDest
		l.syncedDestLen = pendingDestLen
	}
	target.encryption = editor.encryption
	target.bindEncryptionHold()
	if pendingMemPage != nil {
		target.injectMemoryPage(pendingMemPath, pendingMemPage)
		delete(target.pageHeaderCache, pendingMemPath)
		if target.pageHeaders != nil {
			target.pageHeaders.put(pendingMemPath, pendingHeader, nil)
		}
	}
	if nextFiles != nil {
		target.files = nextFiles
		target.fileNames = nextNames
		target.pageReady = nextReady
		target.imageCatalog = nil
		target.resourceInfoCache = nil
		target.fontResourcesRead = false
	}
	if pendingMemPage != nil || nextFiles != nil {
		l.mu.Lock()
		if pendingMemPage != nil {
			l.syncedPages[index] = true
		}
		if nextFiles != nil {
			l.readyPages = nextReady
		}
		l.mu.Unlock()
	}
	if needRes {
		l.syncedResCount = nextResCount
	}
	return nil
}

// snapshot 从常驻阅读器派生单页就绪的独立只读视图
// 入参: index 就绪页面索引，-1表示无就绪页面
// 返回: *Reader 独立阅读快照
func (l *pdfLiveReader) snapshot(index int) *Reader {
	base := l.reader
	next := *base
	doc := *base.doc
	next.doc = &doc
	next.closer = nil
	next.encHold = nil
	base.cloneMutableState(&next)
	next.files = maps.Clone(base.files)
	next.fileNames = maps.Clone(base.fileNames)
	next.pageReady = make(map[string]bool)
	if index >= 0 {
		next.pageReady[next.doc.Pages.Page[index].ID] = true
	}
	next.bindEncryptionHold()
	return &next
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

// beginPage 初始化当前页面的坐标变换及全部逐页临时状态
// 入参: ctx 取消上下文, index 页面索引, page PDF页面
func (p *pdfImporter) beginPage(ctx context.Context, index int, page *pdfgo.Page) {
	p.ctx = ctx
	p.renderer.pageContext = ctx
	matrix, width, height := codecpdf.PageMatrix(page)
	p.matrix = matrix
	p.page = index
	p.rasterWarned = false
	p.pageBox = page.CropBox
	p.pageWidth = width
	p.pageHeight = height
	p.objects = nil
	p.maskClips = make(map[*pdfgo.SoftMask]pdfgo.Path)
	p.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
	p.compositeCache = nil
	p.compositeNodes = nil
	p.compositeSpace = nil
	p.compositeBackdropPage = nil
	p.compositeBackdropLoaded = false
	p.compositeBackdropNodes = nil
	p.transferModel = ""
	p.transferBackdrop = false
	p.patternCells = nil
	p.patternOrigins = nil
	p.textOrigins = nil
	p.clipPaths = nil
	p.localFormCounts = nil
	p.neighborFormRefs = nil
	p.pageSeenForms = nil
	p.imageScaleBoost = 0
	p.annotationData = nil
	p.annotationBindings = nil
	p.movieBounds = nil
}

// clearPageState 清空当前页面的临时状态，避免取消回滚后残留跨页数据
func (p *pdfImporter) clearPageState() {
	p.movieBounds = nil
	p.objects = nil
	p.annotationData = nil
	p.annotationBindings = nil
	p.compositeCache = nil
	p.compositeNodes = nil
	p.compositeSpace = nil
	p.compositeBackdropPage = nil
	p.compositeBackdropLoaded = false
	p.compositeBackdropNodes = nil
	p.transferModel = ""
	p.transferBackdrop = false
	p.maskClips = nil
	p.patternCells = nil
	p.patternOrigins = nil
	p.clipTexts = nil
	p.textOrigins = nil
	p.clipPaths = nil
	p.localFormCounts = nil
	p.neighborFormRefs = nil
	p.pageSeenForms = nil
	p.imageScaleBoost = 0
}

// hasReferenceDocuments 判断当前导入器是否可能存在来自非主文档的引用对象
// 返回: bool 是否使用外部或内嵌引用文档
func (p *pdfImporter) hasReferenceDocuments() bool {
	return p.resolveFile != nil || p.resolveReference != nil || p.usedReferences
}

// referencePage 衔接PDF访问器与当前导入会话的引用文档缓存
// 入参: ctx 取消上下文, source 来源文档, reference 引用目标
// 返回: *pdfgo.Page 目标页面，不可用时为空, error 取消或调用方解析错误
func (p *pdfImporter) referencePage(ctx context.Context, source *pdfgo.Reader, reference pdfgo.ReferenceXObject) (*pdfgo.Page, error) {
	if p.references == nil {
		p.references = &codecpdf.ReferenceCache{}
	}
	page, err := p.references.Resolve(ctx, source, reference, p.resolveFile, p.resolveReference, p.warning)
	if page != nil {
		p.usedReferences = true
	}
	return page, err
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

// mergeColorSpaceResource 将工作协程产生的颜色空间资源按特征去重合并到主编辑器
// 入参: resource 工作协程颜色空间资源
// 返回: string 主编辑器颜色空间标识, error 注册错误
func (p *pdfImporter) mergeColorSpaceResource(resource editorResource) (string, error) {
	space := resource.space
	if space.Profile == "" && len(resource.data) == 0 && len(space.Palette) == 0 {
		if space.Type == "CMYK" && space.BitsPerComponent == 16 {
			if p.cmykSpace == "" {
				cmykID, err := p.editor.nextID()
				if err != nil {
					return "", err
				}
				p.cmykSpace = cmykID
				p.editor.resources = append(p.editor.resources, editorResource{space: &ColorSpace{ID: p.cmykSpace, Type: "CMYK", BitsPerComponent: 16}})
			}
			return p.cmykSpace, nil
		}
		if space.Type == "RGB" && space.BitsPerComponent == 16 {
			if p.rgbSpace == "" {
				id, err := p.editor.AddColorSpace(ColorSpace{Type: "RGB", BitsPerComponent: 16}, nil)
				if err != nil {
					return "", err
				}
				p.rgbSpace = id
			}
			return p.rgbSpace, nil
		}
	}
	if p.mergedColorSpaces == nil {
		p.mergedColorSpaces = make(map[[32]byte]string)
		for _, existing := range p.editor.resources {
			if existing.space != nil {
				p.mergedColorSpaces[colorSpaceResourceDigest(*existing.space, existing.data)] = existing.space.ID
			}
		}
	}
	key := colorSpaceResourceDigest(*space, resource.data)
	if id, ok := p.mergedColorSpaces[key]; ok {
		return id, nil
	}
	id, err := p.editor.AddColorSpace(ColorSpace{Type: space.Type, BitsPerComponent: space.BitsPerComponent, Palette: space.Palette}, resource.data)
	if err != nil {
		return "", err
	}
	p.mergedColorSpaces[key] = id
	return id, nil
}

// registerMergedForm 对重映射后的复合图形对象按内容去重并注册到主编辑器
// 入参: ctx 取消上下文, objects 已重映射的局部对象, width 资源宽度, height 资源高度
// 返回: string 主编辑器复合图形标识, error 编码或注册错误
func (p *pdfImporter) registerMergedForm(ctx context.Context, objects []GraphicObject, width, height float64) (string, error) {
	if p.formResources == nil || p.formResources.editor != p.editor {
		p.formResources = &pdfFormCache{editor: p.editor, entries: *resourceCache[[32]byte, pdfFormResource](p.resources, 32<<20)}
	}
	cache := p.formResources
	defer cache.reset()
	content, err := cache.encodeSlice(ctx, objects, width, height)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(content)
	resource, found := cache.entries.get(key)
	if !found || !bytes.Equal(resource.content, content) {
		id, err := p.objectValidation().addOwnedVector(ctx, objects, width, height)
		if err != nil {
			return "", err
		}
		resource = pdfFormResource{id: id}
		if len(content) <= cache.entries.limit-len(id)-128 {
			resource.content = bytes.Clone(content)
			cache.entries.put(key, resource, cap(resource.content)+len(id)+128)
		}
	}
	return resource.id, nil
}

// hasDocumentStructure 判断源PDF文档目录是否声明了有效逻辑结构树
// 返回: bool 文档是否存在逻辑结构树
func (p *pdfImporter) hasDocumentStructure() bool {
	if p.reader == nil {
		return false
	}
	if p.structureBindings != nil && p.structureBindings.invalid[p.reader] {
		return false
	}
	raw, err := p.pdfCatalogObject("StructTreeRoot")
	if err != nil {
		return true
	}
	if raw == nil {
		return false
	}
	value, err := p.reader.Resolve(raw)
	return err != nil || value != nil
}

// pageHasStructureMarks 检查结构绑定表中是否已记录指定页面的标记内容
// 入参: pageRef 页面间接引用
// 返回: bool 页面是否包含结构标记
func (p *pdfImporter) pageHasStructureMarks(pageRef pdfgo.Reference) bool {
	if p.structureBindings == nil || pageRef == (pdfgo.Reference{}) {
		return false
	}
	for key := range p.structureBindings.marked {
		if key.reader == p.reader && key.page == pageRef {
			return true
		}
	}
	return false
}

// resourcesHaveStructure 递归检查页面资源中的XObject、Pattern与软蒙版组是否携带结构绑定字段
// 入参: ctx 取消上下文, resources 资源字典
// 返回: bool 是否存在带StructParents或StructParent的资源流
func (p *pdfImporter) resourcesHaveStructure(ctx context.Context, resources pdfgo.Dictionary) bool {
	if len(resources) == 0 || p.reader == nil {
		return false
	}
	seen := make(map[pdfgo.Reference]bool)
	var inspect func(dict pdfgo.Dictionary, depth int) bool
	inspectStream := func(entry pdfgo.Object, requireForm bool, depth int) bool {
		if ctx.Err() != nil {
			return true
		}
		if ref, ok := entry.(pdfgo.Reference); ok {
			if seen[ref] {
				return false
			}
			seen[ref] = true
		}
		resolved, err := p.reader.Resolve(entry)
		if err != nil {
			return true
		}
		stream, ok := resolved.(*pdfgo.Stream)
		if !ok || stream == nil {
			return false
		}
		if stream.Dictionary["StructParents"] != nil || stream.Dictionary["StructParent"] != nil {
			return true
		}
		if requireForm {
			subtype, err := p.reader.Resolve(stream.Dictionary["Subtype"])
			if err != nil {
				return true
			}
			if subtype != pdfgo.Name("Form") {
				return false
			}
		}
		resVal, err := p.reader.Resolve(stream.Dictionary["Resources"])
		if err != nil {
			return true
		}
		if resDict, ok := resVal.(pdfgo.Dictionary); ok && inspect(resDict, depth+1) {
			return true
		}
		return false
	}
	inspect = func(dict pdfgo.Dictionary, depth int) bool {
		if dict == nil || depth > 8 || ctx.Err() != nil {
			return depth > 8 || ctx.Err() != nil
		}
		xobjVal, err := p.reader.Resolve(dict["XObject"])
		if err != nil {
			return true
		}
		if xobjs, ok := xobjVal.(pdfgo.Dictionary); ok {
			for _, entry := range xobjs {
				if inspectStream(entry, true, depth) {
					return true
				}
			}
		}
		patVal, err := p.reader.Resolve(dict["Pattern"])
		if err != nil {
			return true
		}
		if pats, ok := patVal.(pdfgo.Dictionary); ok {
			for _, entry := range pats {
				if inspectStream(entry, false, depth) {
					return true
				}
			}
		}
		gsVal, err := p.reader.Resolve(dict["ExtGState"])
		if err != nil {
			return true
		}
		if states, ok := gsVal.(pdfgo.Dictionary); ok {
			for _, entry := range states {
				if ctx.Err() != nil {
					return true
				}
				if ref, ok := entry.(pdfgo.Reference); ok {
					if seen[ref] {
						continue
					}
					seen[ref] = true
				}
				resolved, err := p.reader.Resolve(entry)
				if err != nil {
					return true
				}
				gsDict, ok := resolved.(pdfgo.Dictionary)
				if !ok || gsDict == nil {
					continue
				}
				smaskObj := gsDict["SMask"]
				if smaskObj == nil {
					continue
				}
				if ref, ok := smaskObj.(pdfgo.Reference); ok {
					if seen[ref] {
						continue
					}
					seen[ref] = true
				}
				smaskVal, err := p.reader.Resolve(smaskObj)
				if err != nil {
					return true
				}
				if smaskDict, ok := smaskVal.(pdfgo.Dictionary); ok && smaskDict["G"] != nil && inspectStream(smaskDict["G"], false, depth) {
					return true
				}
			}
		}
		return false
	}
	return inspect(resources, 0)
}

// pageContentFingerprint 计算页面正文内容流、资源、尺寸及旋转的确定性指纹，含结构绑定或直接流时跳过复用
// 入参: ctx 取消上下文, page PDF页面
// 返回: [32]byte 内容指纹, bool 是否满足安全复用条件
func (p *pdfImporter) pageContentFingerprint(ctx context.Context, page *pdfgo.Page) ([32]byte, bool) {
	if page == nil || page.Dictionary["StructParents"] != nil || ctx.Err() != nil {
		return [32]byte{}, false
	}
	if p.hasDocumentStructure() && (p.pageHasStructureMarks(page.Reference) || p.resourcesHaveStructure(ctx, page.Resources)) {
		return [32]byte{}, false
	}
	h := sha256.New()
	var buf [8]byte
	writeU64 := func(v uint64) {
		binary.LittleEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	contents := page.Dictionary["Contents"]
	if contents == nil {
		h.Write([]byte("C0"))
	} else {
		var refs []pdfgo.Reference
		if ref, ok := contents.(pdfgo.Reference); ok {
			resolved, err := p.reader.Resolve(ref)
			if err != nil {
				return [32]byte{}, false
			}
			if arr, ok := resolved.(pdfgo.Array); ok {
				for _, item := range arr {
					r, ok := item.(pdfgo.Reference)
					if !ok {
						return [32]byte{}, false
					}
					refs = append(refs, r)
				}
			} else if _, ok := resolved.(*pdfgo.Stream); ok {
				refs = append(refs, ref)
			} else {
				return [32]byte{}, false
			}
		} else if arr, ok := contents.(pdfgo.Array); ok {
			for _, item := range arr {
				r, ok := item.(pdfgo.Reference)
				if !ok {
					return [32]byte{}, false
				}
				refs = append(refs, r)
			}
		} else {
			return [32]byte{}, false
		}
		h.Write([]byte("CR"))
		writeU64(uint64(len(refs)))
		for _, ref := range refs {
			writeU64(uint64(ref.Number))
			writeU64(uint64(ref.Generation))
		}
	}
	for _, value := range []float64{page.CropBox.XMin, page.CropBox.YMin, page.CropBox.XMax, page.CropBox.YMax, page.MediaBox.XMin, page.MediaBox.YMin, page.MediaBox.XMax, page.MediaBox.YMax, page.UserUnit} {
		writeU64(math.Float64bits(value))
	}
	writeU64(uint64(page.Rotation))
	h.Write([]byte("RS"))
	if !hashPDFObject(h, page.Resources, 0) {
		return [32]byte{}, false
	}
	h.Write([]byte("GR"))
	if !hashPDFObject(h, page.Dictionary["Group"], 0) {
		return [32]byte{}, false
	}
	h.Write([]byte("TR"))
	if !hashPDFObject(h, page.Dictionary["Trans"], 0) {
		return [32]byte{}, false
	}
	var sum [32]byte
	h.Sum(sum[:0])
	return sum, true
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

// ReadAt 并发安全地读取PDF原始字节，响应当前工作上下文取消
// 入参: data 目标缓冲, offset 文件偏移
// 返回: int 读取字节数, error 读取或取消错误
func (w *pdfWorkerInput) ReadAt(data []byte, offset int64) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.input.ReadAt(data, offset)
}

// finishPages 按页分组去重相同内容流并调度多核工作池并发转换，主协程按页序确定性合并
// 入参: ctx 取消上下文
// 返回: error 转换或取消错误
func (p *PDFImport) finishPages(ctx context.Context) error {
	var pending []int
	for index := range p.pages {
		if !p.ready[index] {
			pending = append(pending, index)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	importer := p.importer
	importer.ctx = importer.resources.context(ctx)
	ctx = importer.ctx
	if len(pending) >= 2 && importer.formRefCounts == nil {
		importer.formRefCounts = importer.scanFormRefCounts(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	fingerprintLeaders := make(map[[32]byte]int)
	duplicateOf := make(map[int]int)
	duplicateGroups := make(map[int][]int)
	var duplicateOrder []int
	var leaders []int
	for index, page := range importer.pageOrder {
		if !p.ready[index] {
			continue
		}
		if fp, ok := importer.pageContentFingerprint(ctx, page); ok {
			if _, exists := fingerprintLeaders[fp]; !exists {
				fingerprintLeaders[fp] = index
			}
		}
	}
	for _, index := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if fp, ok := importer.pageContentFingerprint(ctx, importer.pageOrder[index]); ok {
			if leader, exists := fingerprintLeaders[fp]; exists {
				duplicateOf[index] = leader
				if len(duplicateGroups[leader]) == 0 {
					duplicateOrder = append(duplicateOrder, leader)
				}
				duplicateGroups[leader] = append(duplicateGroups[leader], index)
				continue
			}
			fingerprintLeaders[fp] = index
		}
		leaders = append(leaders, index)
	}
	workers := 1
	if runtime.GOOS != "js" && runtime.GOARCH != "wasm" && pdfUsesDefaultBackends(importer.renderer.Backends()) && p.options.ResolveFile == nil && p.options.ResolveReference == nil && len(leaders) > 1 {
		workers = min(len(leaders), conversionWorkers(ctx))
	}
	if workers <= 1 {
		for _, index := range pending {
			if leader, dup := duplicateOf[index]; dup {
				if err := p.convertDuplicatePage(ctx, index, leader); err != nil {
					return err
				}
				continue
			}
			if err := p.ConvertPage(ctx, index); err != nil {
				return err
			}
		}
	} else {
		if err := p.finishPagesParallel(ctx, pending, leaders, duplicateOf, workers); err != nil {
			return err
		}
	}
	if len(duplicateOrder) != 0 {
		if err := p.applyDuplicatePageTemplates(ctx, duplicateOrder, duplicateGroups); err != nil {
			return err
		}
	}
	return nil
}

// finishPagesParallel 启动有界工作协程池并发执行代表页主体转换，主协程按页序确定性归并资源与注释
// 入参: ctx 取消上下文, pending 待转换页序, leaders 唯一内容代表页, duplicateOf 重复页到代表页映射, workers 工作协程数
// 返回: error 转换或取消错误
func (p *PDFImport) finishPagesParallel(ctx context.Context, pending, leaders []int, duplicateOf map[int]int, workers int) error {
	importer := p.importer
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sharedImages := newPDFSharedImageCache()
	var pageMarked map[pdfgo.Reference][]pdfStructureContent
	if importer.structureBindings != nil && len(importer.structureBindings.marked) != 0 {
		pageMarked = make(map[pdfgo.Reference][]pdfStructureContent)
		for key := range importer.structureBindings.marked {
			if key.reader == importer.reader {
				pageMarked[key.page] = append(pageMarked[key.page], key)
			}
		}
	}
	window := min(len(leaders), workers*2)
	jobs := make(chan int, window)
	results := make(chan pdfPageWorkResult, window)
	var wg sync.WaitGroup
	for slot := range workers {
		wg.Add(1)
		go func(workerSlot int) {
			defer wg.Done()
			p.runPageWorker(workCtx, workerSlot, jobs, results, sharedImages, pageMarked)
		}(slot)
	}
	workerIDMaps := make([]map[string]string, workers)
	for i := range workerIDMaps {
		workerIDMaps[i] = make(map[string]string)
	}
	readyResults := make(map[int]pdfPageWorkResult)
	dispatched := 0
	committedLeaders := 0
	received := 0
	nextPos := 0
	jobsClosed := false
	closeJobs := func() {
		if !jobsClosed {
			close(jobs)
			jobsClosed = true
		}
	}
	var firstErr error
	var rootErr error
	recordWorkerErr := func(err error) {
		if err != nil && rootErr == nil && (!errors.Is(err, context.Canceled) || ctx.Err() != nil) {
			rootErr = err
		}
	}
	drainWorkers := func() {
		cancel()
		closeJobs()
		for received < dispatched {
			drained := <-results
			received++
			recordWorkerErr(drained.err)
		}
		wg.Wait()
	}
	for nextPos < len(pending) {
		for !jobsClosed && rootErr == nil && dispatched < len(leaders) && dispatched-committedLeaders < window {
			jobs <- leaders[dispatched]
			dispatched++
		}
		if dispatched == len(leaders) {
			closeJobs()
		}
		index := pending[nextPos]
		if leader, dup := duplicateOf[index]; dup && p.ready[leader] {
			if err := p.convertDuplicatePage(ctx, index, leader); err != nil {
				firstErr = err
				break
			}
			nextPos++
			continue
		}
		if res, ok := readyResults[index]; ok {
			delete(readyResults, index)
			if res.err != nil {
				firstErr = res.err
				break
			}
			if res.needsSerial {
				if err := p.ConvertPage(ctx, index); err != nil {
					firstErr = err
					break
				}
			} else if err := p.commitParallelPageResult(ctx, index, res, workerIDMaps[res.workerSlot]); err != nil {
				firstErr = err
				break
			}
			committedLeaders++
			nextPos++
			continue
		}
		res := <-results
		received++
		if res.err != nil {
			recordWorkerErr(res.err)
			cancel()
			closeJobs()
		}
		readyResults[res.pageIndex] = res
	}
	if firstErr != nil {
		drainWorkers()
		if ctx.Err() == nil && errors.Is(firstErr, context.Canceled) && rootErr != nil {
			return rootErr
		}
		return firstErr
	}
	closeJobs()
	wg.Wait()
	return ctx.Err()
}

// runPageWorker 在独立阅读器与编辑器上下文中并发执行单页PDF正文图元转换
// 入参: ctx 工作上下文, slot 工作协程编号, jobs 页码任务通道, results 结果通道, sharedImages 共享图像编码缓存, pageMarked 结构树标记索引
func (p *PDFImport) runPageWorker(ctx context.Context, slot int, jobs <-chan int, results chan<- pdfPageWorkResult, sharedImages *pdfSharedImageCache, pageMarked map[pdfgo.Reference][]pdfStructureContent) {
	mainImporter := p.importer
	workerRenderer := NewRenderer(&Reader{resources: mainImporter.resources}, p.options.RendererOptions...)
	workerRenderer.dpi = mainImporter.renderer.dpi
	workerRenderer.fontSourcesCache = mainImporter.renderer.fontSourcesCache.child()
	workerEditor := NewEditor()
	defer workerEditor.Close()
	workerEditor.runtimeResources = mainImporter.resources
	workerEditor.SetRenderBackends(workerRenderer.Backends())
	workerEditor.fontDirs, workerEditor.fontFS = workerRenderer.FontSources()
	workerEditor.fontSourcesCache = workerRenderer.fontSourcesCache
	_, _ = workerEditor.AddPage(1, 1)
	workerImporter := &pdfImporter{
		resources:     mainImporter.resources,
		ctx:           ctx,
		editor:        workerEditor,
		renderer:      workerRenderer,
		rasterDPI:     mainImporter.rasterDPI,
		page:          -1,
		fontIDs:       map[*pdfgo.Font]string{},
		fonts:         map[*pdfgo.Font]*pdfImportedFont{},
		pages:         mainImporter.pages,
		pageOrder:     mainImporter.pageOrder,
		pageIDs:       mainImporter.pageIDs,
		pageIndexes:   mainImporter.pageIndexes,
		compression:   mainImporter.compression,
		imageWorkers:  1,
		sharedImages:  sharedImages,
		formRefCounts: mainImporter.formRefCounts,
		recordForms:   true,
	}
	var workerWarning func(pdfgo.Diagnostic)
	if !p.options.Strict {
		workerWarning = func(warning pdfgo.Diagnostic) {
			warning.Page = workerImporter.page + 1
			workerImporter.addWarning(warning)
		}
		workerImporter.warning = workerWarning
	}
	workerReader := mainImporter.reader.Fork(&pdfWorkerInput{input: p.input, ctx: ctx}, workerWarning, workerImporter.shapePDFText)
	workerImporter.reader = workerReader
	defer func() {
		if workerImporter.references != nil {
			workerImporter.references.Close()
		}
	}()
	serialOnly := false
	for pageIndex := range jobs {
		res := pdfPageWorkResult{pageIndex: pageIndex, workerSlot: slot}
		if err := ctx.Err(); err != nil {
			res.err = err
			results <- res
			continue
		}
		if workerEditor.source != nil || serialOnly {
			res.needsSerial = true
			results <- res
			continue
		}
		mainPage := mainImporter.pageOrder[pageIndex]
		page := workerReader.BindPage(mainPage)
		workerImporter.beginPage(ctx, pageIndex, page)
		workerImporter.report = PDFImportReport{}
		workerImporter.resetWarnings()
		workerImporter.halftoneWarnings = make(map[*pdfgo.Halftone]bool)
		workerImporter.halftoneWarningMessages = make(map[string]bool)
		if mainImporter.structureBindings != nil {
			wb := &pdfStructureBindings{prepared: map[*pdfgo.Reader]bool{workerReader: true}}
			if list := pageMarked[mainPage.Reference]; len(list) != 0 {
				wb.marked = make(map[pdfStructureContent]bool, len(list))
				for _, key := range list {
					key.reader = workerReader
					wb.marked[key] = true
				}
			}
			workerImporter.structureBindings = wb
		} else {
			workerImporter.structureBindings = nil
		}
		resStart := len(workerEditor.resources)
		if err := workerImporter.compositePage(nil, func(visitor pdfgo.Visitor) error {
			return workerReader.WalkPage(ctx, page, visitor)
		}); err != nil {
			res.err = fmt.Errorf("import PDF page %d: %w", pageIndex+1, err)
			results <- res
			continue
		}
		if workerEditor.source != nil || workerImporter.references != nil {
			if workerImporter.references != nil {
				workerImporter.references.Close()
				workerImporter.references = nil
			}
			workerImporter.usedReferences = false
			if workerEditor.source != nil || len(workerEditor.resources) > resStart {
				serialOnly = true
			}
			res.needsSerial = true
			results <- res
			continue
		}
		var objects []GraphicObject
		var bindings []pdfStructureBinding
		if workerImporter.objects.len() != 0 {
			var err error
			objects, err = workerImporter.objects.data(ctx)
			if err != nil {
				res.err = fmt.Errorf("import PDF page %d: %w", pageIndex+1, err)
				results <- res
				continue
			}
			objects, err = workerImporter.sharedClipObjects(objects)
			if err != nil {
				res.err = fmt.Errorf("import PDF page %d: %w", pageIndex+1, err)
				results <- res
				continue
			}
			if workerImporter.structureBindings != nil {
				bindings, err = workerImporter.objects.structureBindings(ctx)
				if err != nil {
					res.err = fmt.Errorf("import PDF page %d: %w", pageIndex+1, err)
					results <- res
					continue
				}
			}
		}
		var observed []*pdfgo.ContentSequence
		if workerImporter.structureBindings != nil {
			observed = workerImporter.structureBindings.contents
			rebindSequences(observed, workerReader, mainImporter.reader, page, mainPage)
			for _, binding := range bindings {
				rebindSequence(binding.sequence, workerReader, mainImporter.reader, page, mainPage)
			}
		}
		if len(workerEditor.resources) > resStart {
			added := slices.Clone(workerEditor.resources[resStart:])
			res.addedResources = added
			for _, item := range added {
				if item.font != nil {
					if parsed := workerEditor.fonts[item.font.ID]; parsed != nil {
						if res.addedFonts == nil {
							res.addedFonts = make(map[string]*FontResource)
						}
						res.addedFonts[item.font.ID] = parsed
					}
					if metrics, ok := workerEditor.fontMetrics[item.font.ID]; ok {
						if res.addedMetrics == nil {
							res.addedMetrics = make(map[string]FontMetrics)
						}
						res.addedMetrics[item.font.ID] = metrics
					}
				}
				if item.composite != "" && workerImporter.formRecords != nil {
					if rec, ok := workerImporter.formRecords[item.composite]; ok {
						if res.addedForms == nil {
							res.addedForms = make(map[string]pdfFormRecord)
						}
						res.addedForms[item.composite] = rec
					}
				}
			}
			if len(workerEditor.optimizedImages) != 0 {
				res.optimizedImages = maps.Clone(workerEditor.optimizedImages)
			}
		}
		res.objects = objects
		res.bindings = bindings
		res.observed = observed
		res.compositeSpace = workerImporter.compositeSpace
		res.transferModel = workerImporter.transferModel
		res.transferBackdrop = workerImporter.transferBackdrop
		res.report = workerImporter.report
		res.warnings = workerImporter.warningEntries()
		results <- res
	}
}

// commitParallelPageResult 在主协程按页序确定性注册增量资源、重映射对象标识并提交页面与注解
// 入参: ctx 取消上下文, index 页面索引, res 工作协程产出, idMap 该工作协程的资源标识映射表
// 返回: error 合并或注解转换错误
func (p *PDFImport) commitParallelPageResult(ctx context.Context, index int, res pdfPageWorkResult, idMap map[string]string) (err error) {
	importer := p.importer
	savedPage := cloneEditorData(importer.editor.pages[index])
	savedSource := importer.editor.source
	savedOwnedOwner := importer.editor.ownedOwner
	if savedSource != nil {
		importer.editor.ownedOwner = nil
	}
	savedOrigins := maps.Clone(importer.editor.origins)
	savedDestinations := importer.editor.pdfDestinations.clone()
	savedAttachmentIDs := maps.Clone(importer.attachmentIDs)
	savedMediaResources := maps.Clone(importer.mediaResources)
	savedMediaInstances := maps.Clone(importer.mediaInstances)
	savedModelAttachments := maps.Clone(importer.modelAttachments)
	savedFontWarnings := maps.Clone(importer.fontWarnings)
	savedHalftoneWarnings := maps.Clone(importer.halftoneWarnings)
	savedHalftoneMessages := maps.Clone(importer.halftoneWarningMessages)
	savedIsolatedGroupImages := maps.Clone(importer.isolatedGroupImages)
	savedUsedReferences := importer.usedReferences
	savedRenditionsHad := importer.renditions != nil
	var savedRenditionMedia map[pdfgo.Reference][]pdfRenditionResource
	if savedRenditionsHad {
		savedRenditionMedia = maps.Clone(importer.renditions.media)
	}
	savedReport := importer.report
	savedWarnings := importer.saveWarningState()
	_, hadBodyCounts := importer.pageBodyCounts[index]
	savedStructure := importer.structureBindings.snapshot()
	defer func() {
		if err == nil {
			if importer.editor.source == savedSource {
				importer.editor.ownedOwner = savedOwnedOwner
			}
			return
		}
		if isPDFImportCanceled(ctx, err) {
			importer.editor.pages[index] = savedPage
			importer.editor.source = savedSource
			importer.editor.origins = savedOrigins
			importer.editor.ownedOwner = savedOwnedOwner
			importer.editor.pdfDestinations = savedDestinations
			importer.editor.previewPages = nil
			importer.attachmentIDs = savedAttachmentIDs
			importer.mediaResources = savedMediaResources
			importer.mediaInstances = savedMediaInstances
			importer.modelAttachments = savedModelAttachments
			importer.fontWarnings = savedFontWarnings
			importer.halftoneWarnings = savedHalftoneWarnings
			importer.halftoneWarningMessages = savedHalftoneMessages
			importer.isolatedGroupImages = savedIsolatedGroupImages
			importer.usedReferences = savedUsedReferences
			if !savedRenditionsHad {
				importer.renditions = nil
			} else if importer.renditions != nil {
				importer.renditions.media = savedRenditionMedia
			}
			importer.clearPageState()
			importer.report = savedReport
			importer.restoreWarningState(savedWarnings)
			if !hadBodyCounts {
				delete(importer.pageBodyCounts, index)
			}
			if importer.structureBindings != nil {
				importer.structureBindings.rollbackPage(savedPage.ID, importer.pageOrder[index].Reference, savedStructure)
			}
			p.ready[index] = false
		}
	}()
	ctx = importer.resources.context(ctx)
	page := importer.pageOrder[index]
	importer.beginPage(ctx, index, page)
	importer.compositeSpace = res.compositeSpace
	importer.transferModel = res.transferModel
	importer.transferBackdrop = res.transferBackdrop
	importer.compositeBackdropPage = page
	for _, resource := range res.addedResources {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case resource.font != nil:
			var mainID string
			var err error
			if resource.font.FontFile != "" && len(resource.data) != 0 {
				parsed := res.addedFonts[resource.font.ID]
				if parsed == nil {
					parsed, err = importer.editor.backends.FontResources.OpenFontResource(FontFile{Data: resource.data}, 0)
					if err != nil {
						return err
					}
				}
				mainID, err = importer.editor.addFontResource(parsed, sha256.Sum256(resource.data))
				if err == nil {
					if metrics, ok := res.addedMetrics[resource.font.ID]; ok {
						importer.editor.fontMetrics[mainID] = metrics
					}
				}
			} else {
				mainID, err = importer.editor.addExternalFont(resource.font.FontName, resource.font.FamilyName, resource.font.Charset, resource.font.Bold, resource.font.Italic)
			}
			if err != nil {
				return err
			}
			idMap[resource.font.ID] = mainID
		case resource.image != nil:
			mainID, err := importer.editor.addDecodedImageContext(ctx, resource.data, nil)
			if err != nil {
				return err
			}
			if len(res.optimizedImages) != 0 {
				if opt, ok := res.optimizedImages[sha256.Sum256(resource.data)]; ok {
					importer.editor.markOptimizedImage(mainID, opt)
				}
			}
			idMap[resource.image.ID] = mainID
		case resource.space != nil:
			mainID, err := importer.mergeColorSpaceResource(resource)
			if err != nil {
				return err
			}
			idMap[resource.space.ID] = mainID
		case resource.composite != "":
			rec, ok := res.addedForms[resource.composite]
			if !ok {
				return fmt.Errorf("missing parallel composite form %s", resource.composite)
			}
			remapped := cloneEditorData(rec.objects)
			remapGraphicObjects(remapped, idMap)
			mainID, err := importer.registerMergedForm(ctx, remapped, rec.width, rec.height)
			if err != nil {
				return err
			}
			idMap[resource.composite] = mainID
		}
	}
	if err := importer.pageOpenActions(ctx, page); err != nil {
		return fmt.Errorf("import PDF page %d actions: %w", index+1, err)
	}
	if len(res.objects) != 0 {
		remapGraphicObjects(res.objects, idMap)
		if err := importer.objectValidation().appendOwnedObjects(ctx, index, res.objects); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if importer.structureBindings != nil {
			for _, seq := range res.observed {
				importer.structureBindings.observe(ctx, seq)
			}
			importer.structureBindings.append(importer.editor.pages[index].ID, res.objects, res.bindings)
		}
	} else if importer.structureBindings != nil {
		for _, seq := range res.observed {
			importer.structureBindings.observe(ctx, seq)
		}
	}
	if err := importer.annotations(ctx, page, p.options.Strict); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	if err := importer.commitAnnotations(); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	if importer.pageBodyCounts == nil {
		importer.pageBodyCounts = make(map[int][3]int)
	}
	importer.pageBodyCounts[index] = [3]int{res.report.TextObjects, res.report.PathObjects, res.report.ImageObjects}
	importer.report.TextObjects += res.report.TextObjects
	importer.report.PathObjects += res.report.PathObjects
	importer.report.ImageObjects += res.report.ImageObjects
	for _, entry := range res.warnings {
		w := entry.diagnostic
		if isPDFFontDedupWarning(w.Message) {
			if importer.fontWarnings == nil {
				importer.fontWarnings = make(map[string]bool)
			}
			if importer.fontWarnings[w.Message] {
				continue
			}
			importer.fontWarnings[w.Message] = true
		}
		if isPDFHalftoneDedupWarning(w.Message) {
			if importer.halftoneWarningMessages == nil {
				importer.halftoneWarningMessages = make(map[string]bool)
			}
			if importer.halftoneWarningMessages[w.Message] {
				continue
			}
			importer.halftoneWarningMessages[w.Message] = true
		}
		importer.addWarningCount(w, entry.count)
	}
	importer.report.Pages++
	if err := editorProgress(p.options.OnProgress).withContext(ctx).report("convert", importer.report.Pages, len(p.pages)); err != nil {
		return err
	}
	if p.pageCompositeMeta == nil {
		p.pageCompositeMeta = make(map[int]pdfPageCompositeMeta)
	}
	p.pageCompositeMeta[index] = pdfPageCompositeMeta{space: res.compositeSpace, transferModel: res.transferModel, transferBackdrop: res.transferBackdrop}
	p.ready[index] = true
	return nil
}

// ensureDuplicatePageTemplate 确保代表页正文已抽离为共享OFD模板页并返回其标识
// 入参: ctx 取消上下文, leader 代表页索引
// 返回: string 模板页标识，空表示无正文对象, error 创建或取消错误
func (p *PDFImport) ensureDuplicatePageTemplate(ctx context.Context, leader int) (string, error) {
	if p.dupTemplates != nil {
		if tplID, ok := p.dupTemplates[leader]; ok {
			return tplID, nil
		}
	}
	importer := p.importer
	page := &importer.editor.pages[leader]
	if len(page.Content.Layer) == 0 || len(page.Content.Layer[0].Objects) == 0 {
		if p.dupTemplates == nil {
			p.dupTemplates = make(map[int]string)
		}
		p.dupTemplates[leader] = ""
		return "", nil
	}
	objs := cloneEditorData(page.Content.Layer[0].Objects)
	for i := range objs {
		clearGraphicObjectID(&objs[i])
	}
	tplID, err := importer.editor.addOwnedTemplatePage(ctx, page.Area, objs, "Background", "")
	if err != nil {
		return "", err
	}
	page.Content.Layer[0].Objects = nil
	page.Template = append(page.Template, Template{TemplateID: tplID, ZOrder: "Background"})
	if p.dupTemplates == nil {
		p.dupTemplates = make(map[int]string)
	}
	p.dupTemplates[leader] = tplID
	p.invalidateLivePages(leader)
	return tplID, nil
}

// convertDuplicatePage 跳过重复内容流页面的图元解释，复用代表页共享模板并独立导入动作与注解
// 入参: ctx 取消上下文, index 当前页面索引, leader 共享内容代表页索引
// 返回: error 动作、注解或取消错误
func (p *PDFImport) convertDuplicatePage(ctx context.Context, index, leader int) (err error) {
	importer := p.importer
	tplID, err := p.ensureDuplicatePageTemplate(ctx, leader)
	if err != nil {
		return err
	}
	savedPage := cloneEditorData(importer.editor.pages[index])
	savedSource := importer.editor.source
	savedOwnedOwner := importer.editor.ownedOwner
	if savedSource != nil {
		importer.editor.ownedOwner = nil
	}
	savedOrigins := maps.Clone(importer.editor.origins)
	savedDestinations := importer.editor.pdfDestinations.clone()
	savedAttachmentIDs := maps.Clone(importer.attachmentIDs)
	savedMediaResources := maps.Clone(importer.mediaResources)
	savedMediaInstances := maps.Clone(importer.mediaInstances)
	savedModelAttachments := maps.Clone(importer.modelAttachments)
	savedFontWarnings := maps.Clone(importer.fontWarnings)
	savedHalftoneWarnings := maps.Clone(importer.halftoneWarnings)
	savedHalftoneMessages := maps.Clone(importer.halftoneWarningMessages)
	savedIsolatedGroupImages := maps.Clone(importer.isolatedGroupImages)
	savedUsedReferences := importer.usedReferences
	savedRenditionsHad := importer.renditions != nil
	var savedRenditionMedia map[pdfgo.Reference][]pdfRenditionResource
	if savedRenditionsHad {
		savedRenditionMedia = maps.Clone(importer.renditions.media)
	}
	savedReport := importer.report
	savedWarnings := importer.saveWarningState()
	savedStructure := importer.structureBindings.snapshot()
	defer func() {
		if err == nil {
			if importer.editor.source == savedSource {
				importer.editor.ownedOwner = savedOwnedOwner
			}
			return
		}
		if isPDFImportCanceled(ctx, err) {
			importer.editor.pages[index] = savedPage
			importer.editor.source = savedSource
			importer.editor.origins = savedOrigins
			importer.editor.ownedOwner = savedOwnedOwner
			importer.editor.pdfDestinations = savedDestinations
			importer.editor.previewPages = nil
			importer.attachmentIDs = savedAttachmentIDs
			importer.mediaResources = savedMediaResources
			importer.mediaInstances = savedMediaInstances
			importer.modelAttachments = savedModelAttachments
			importer.fontWarnings = savedFontWarnings
			importer.halftoneWarnings = savedHalftoneWarnings
			importer.halftoneWarningMessages = savedHalftoneMessages
			importer.isolatedGroupImages = savedIsolatedGroupImages
			importer.usedReferences = savedUsedReferences
			if !savedRenditionsHad {
				importer.renditions = nil
			} else if importer.renditions != nil {
				importer.renditions.media = savedRenditionMedia
			}
			importer.clearPageState()
			importer.report = savedReport
			importer.restoreWarningState(savedWarnings)
			if importer.structureBindings != nil {
				importer.structureBindings.rollbackPage(savedPage.ID, importer.pageOrder[index].Reference, savedStructure)
			}
			p.ready[index] = false
		}
	}()
	if tplID != "" {
		target := &importer.editor.pages[index]
		target.Template = append(target.Template, Template{TemplateID: tplID, ZOrder: "Background"})
	}
	ctx = importer.resources.context(ctx)
	page := importer.pageOrder[index]
	importer.beginPage(ctx, index, page)
	if meta, ok := p.pageCompositeMeta[leader]; ok {
		importer.compositeSpace = meta.space
		importer.transferModel = meta.transferModel
		importer.transferBackdrop = meta.transferBackdrop
	}
	importer.compositeBackdropPage = page
	if err := importer.pageOpenActions(ctx, page); err != nil {
		return fmt.Errorf("import PDF page %d actions: %w", index+1, err)
	}
	if err := importer.annotations(ctx, page, p.options.Strict); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	if err := importer.commitAnnotations(); err != nil {
		return fmt.Errorf("import PDF page %d: %w", index+1, err)
	}
	if counts, ok := importer.pageBodyCounts[leader]; ok {
		importer.report.TextObjects += counts[0]
		importer.report.PathObjects += counts[1]
		importer.report.ImageObjects += counts[2]
	}
	importer.report.Pages++
	if err := editorProgress(p.options.OnProgress).withContext(ctx).report("convert", importer.report.Pages, len(p.pages)); err != nil {
		return err
	}
	p.ready[index] = true
	p.invalidateLivePages(index)
	return nil
}

// applyDuplicatePageTemplates 确保全部重复内容流分组均已绑定共享模板页并失效受影响的实时阅读页
// 入参: ctx 取消上下文, leaders 代表页顺序, groups 代表页到重复页切片映射
// 返回: error 模板页创建错误
func (p *PDFImport) applyDuplicatePageTemplates(ctx context.Context, leaders []int, groups map[int][]int) error {
	for _, leader := range leaders {
		followers := groups[leader]
		if len(followers) == 0 {
			continue
		}
		tplID, err := p.ensureDuplicatePageTemplate(ctx, leader)
		if err != nil {
			return err
		}
		if tplID == "" {
			continue
		}
		for _, follower := range followers {
			target := &p.importer.editor.pages[follower]
			if !slices.ContainsFunc(target.Template, func(item Template) bool { return item.TemplateID == tplID }) {
				target.Template = append(target.Template, Template{TemplateID: tplID, ZOrder: "Background"})
				p.invalidateLivePages(follower)
			}
		}
	}
	return nil
}

// invalidateLivePages 使常驻增量阅读器中受模板抽离影响的页面重新同步
// 入参: pages 受影响的页面索引列表
func (p *PDFImport) invalidateLivePages(pages ...int) {
	var live, targetSync *pdfLiveReader
	if p.syncMu != nil {
		p.syncMu.RLock()
		live, targetSync = p.live, p.targetSync
		p.syncMu.RUnlock()
	} else {
		live, targetSync = p.live, p.targetSync
	}
	live.invalidatePages(pages...)
	targetSync.invalidatePages(pages...)
}

// invalidateLiveAllPages 使常驻增量阅读器全部已同步页面重新同步以对齐模板提升变更
func (p *PDFImport) invalidateLiveAllPages() {
	var live, targetSync *pdfLiveReader
	if p.syncMu != nil {
		p.syncMu.RLock()
		live, targetSync = p.live, p.targetSync
		p.syncMu.RUnlock()
	} else {
		live, targetSync = p.live, p.targetSync
	}
	live.invalidateAll()
	targetSync.invalidateAll()
}
