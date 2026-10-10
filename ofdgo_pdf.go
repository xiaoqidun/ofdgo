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
	"context"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"

	"github.com/xiaoqidun/pdfgo"
)

// ErrPDFPassword 表示PDF需要正确的用户或所有者密码
var ErrPDFPassword = pdfgo.ErrPassword

// PDFImportOptions 指定PDF转换的密码、运行时后端、进度通知与检查策略
// Password默认使用PDF安全处理器编码，PasswordUTF8启用UTF-8输入及标准密码准备，空值尝试空密码
// Strict禁止恢复缺失资源或丢失内容语义，默认在报告中记录警告
// RendererOptions复用渲染器字体来源和后端配置，Backends优先于其中的后端设置
// RasterDPI指定局部合成与三维绘制精度，0沿用渲染器DPI，默认300dpi，Strict禁用局部合成
// OnProgress按open、pages、convert及write.*阶段报告进度，total为0表示总量未知
// ResolveFile读取PDF引用的外部媒体、附件和页面，默认不访问网络或本地路径
// ResolveReference可提供已解密的引用页面，目标阅读器由调用方维护
// Halftones提供只读设备命名网屏，所属阅读器由调用方维护，连续色调输出不网屏化
// Compression配置转换输出，有损模式按图片实际尺寸降采样，默认及无损保持完整采样
type PDFImportOptions struct {
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

// pdfPointBounds 逐点汇总边界，不保留文字或路径的临时坐标数组
type pdfPointBounds struct {
	minX, minY, maxX, maxY float64
	found                  bool
}

// pdfImporter 保存单次转换的对象与资源状态
type pdfImporter struct {
	ctx              context.Context
	reader           *pdfgo.Reader
	editor           *Editor
	renderer         *Renderer
	report           PDFImportReport
	matrix           pdfgo.Matrix
	page             int
	pageActionPage   int
	fontIDs          map[*pdfgo.Font]string
	fonts            map[*pdfgo.Font]*pdfImportedFont
	symbolFonts      map[*pdfgo.Font]*pdfImportedFont
	fontWarnings     map[string]bool
	clipTexts        map[*pdfgo.TextClip][]TextObject
	clipPaths        *renderCache[[32]byte, pdfClipPath]
	cmykSpace        string
	iccSpaces        map[pdfICCKey]string
	objects          *pdfObjectBuffer
	formResources    *pdfFormCache
	validation       *editorValidation
	pages            map[pdfgo.Reference]*pdfgo.Page
	pageOrder        []*pdfgo.Page
	pageIDs          map[pdfgo.Reference]string
	pageIndexes      map[pdfgo.Reference]int
	destinationBoxes map[pdfgo.Reference]Box
	imageIDs         map[pdfImageKey][]pdfImageResource
	imageContents    *renderCache[pdfImageContentKey, pdfImageContent]
	attachmentIDs    map[pdfAttachmentKey]string
	modelAttachments map[*pdfgo.Stream]string
	mediaResources   map[pdfMediaFileKey]pdfMediaResource
	mediaInstances   map[pdfMediaInstanceKey]pdfMediaResource
	movieBounds      map[*Movie]Box
	renditions       *pdfRenditionImport
	maskClips        map[*pdfgo.SoftMask]pdfgo.Path
	pageBox          pdfgo.Rectangle
	pageWidth        float64
	pageHeight       float64
	pendingPath      *pdfgo.PathMark
	warning          func(pdfgo.Diagnostic)
	rasterDPI        float64
	rasterWarned     bool
	compositeCache   *pdfCompositeCache
	compositeNodes   []pdfCompositeNode
	compositeSpace   *pdfgo.ColorSpace
	patternCells     *renderCache[pdfPatternCellKey, *Pattern]
	annotationData   []byte
	resolveFile      pdfgo.FileResolver
	resolveReference pdfgo.ReferenceResolver
	referenceReaders map[[32]byte]*pdfgo.Reader
	referenceStreams map[*pdfgo.Stream]*pdfgo.Reader
	referencePages   map[pdfReferencePageKey]*pdfgo.Page
	halftoneWarnings map[*pdfgo.Halftone]bool
	halftones        map[string]*pdfgo.Halftone
	transferBackdrop bool
	transferModel    pdfgo.Name
	compression      CompressionOptions
}

// ImportPDF 将PDF内容转换为独立OFD编辑文档，失败时不返回部分结果
// 入参: ctx 取消上下文, source PDF数据, size 字节数, options 转换选项
// 返回: *Editor 编辑文档, PDFImportReport 转换统计, error 错误信息
func ImportPDF(ctx context.Context, source io.ReaderAt, size int64, options PDFImportOptions) (*Editor, PDFImportReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, PDFImportReport{}, err
	}
	if err := options.Compression.Validate(); err != nil {
		return nil, PDFImportReport{}, err
	}
	if !finite(options.RasterDPI) || options.RasterDPI < 0 {
		return nil, PDFImportReport{}, fmt.Errorf("invalid PDF raster DPI")
	}
	renderer := NewRenderer(&Reader{}, options.RendererOptions...)
	if options.Backends != nil {
		WithRenderBackends(*options.Backends)(renderer)
	}
	if options.RasterDPI != 0 {
		renderer.DPI = options.RasterDPI
	}
	if !finite(renderer.DPI) || renderer.DPI <= 0 {
		return nil, PDFImportReport{}, fmt.Errorf("invalid PDF raster DPI")
	}
	if options.OnProgress != nil {
		if err := options.OnProgress("open", 0, 0); err != nil {
			return nil, PDFImportReport{}, err
		}
	}
	readOptions := pdfgo.ReaderOptions{Password: options.Password, PasswordUTF8: options.PasswordUTF8}
	var diagnostics []pdfgo.Diagnostic
	var readWarning func(pdfgo.Diagnostic)
	if !options.Strict {
		readWarning = func(diagnostic pdfgo.Diagnostic) { diagnostics = append(diagnostics, diagnostic) }
		readOptions.Warning = func(diagnostic pdfgo.Diagnostic) { readWarning(diagnostic) }
	}
	reader, err := pdfgo.NewReaderWithOptions(source, size, readOptions)
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	defer reader.Close()
	editor := NewEditor()
	editor.SetRenderBackends(renderer.Backends())
	editor.fontDirs, editor.fontFS = renderer.FontSources()
	editor.fontSourcesCache = renderer.fontSourcesCache
	importer := pdfImporter{ctx: ctx, reader: reader, editor: editor, renderer: renderer, rasterDPI: renderer.DPI, fontIDs: map[*pdfgo.Font]string{}, fonts: map[*pdfgo.Font]*pdfImportedFont{}, pages: map[pdfgo.Reference]*pdfgo.Page{}, pageIDs: map[pdfgo.Reference]string{}}
	importer.report.Warnings = diagnostics
	importer.pageIndexes = make(map[pdfgo.Reference]int)
	importer.resolveFile = options.ResolveFile
	importer.resolveReference = options.ResolveReference
	importer.halftones = options.Halftones
	importer.compression = options.Compression
	importer.referenceReaders = make(map[[32]byte]*pdfgo.Reader)
	importer.referenceStreams = make(map[*pdfgo.Stream]*pdfgo.Reader)
	importer.referencePages = make(map[pdfReferencePageKey]*pdfgo.Page)
	defer importer.closeReferenceReaders()
	importer.halftoneWarnings = make(map[*pdfgo.Halftone]bool)
	if security := reader.Encryption(); security != nil && !security.Owner && security.Permissions&0xf3c != 0xf3c {
		if options.Strict {
			return nil, PDFImportReport{}, &pdfgo.UnsupportedError{Feature: "PDF access permission conversion"}
		}
		importer.report.Warnings = append(importer.report.Warnings, pdfgo.Diagnostic{Message: "PDF access permissions not transferred to OFD"})
	}
	if !options.Strict {
		importer.warning = func(warning pdfgo.Diagnostic) {
			warning.Page = importer.page + 1
			importer.report.Warnings = append(importer.report.Warnings, warning)
		}
		readWarning = importer.warning
	}
	if options.OnProgress != nil {
		if err := options.OnProgress("pages", 0, 0); err != nil {
			return nil, PDFImportReport{}, err
		}
	}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		_, width, height := pdfPageMatrix(page)
		if _, err := editor.AddPage(width, height); err != nil {
			return err
		}
		importer.pages[page.Reference] = page
		importer.pageOrder = append(importer.pageOrder, page)
		importer.pageIDs[page.Reference] = editor.pages[index].ID
		importer.pageIndexes[page.Reference] = index
		if options.OnProgress != nil {
			return options.OnProgress("pages", index+1, 0)
		}
		return nil
	})
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	if len(editor.pages) != 0 {
		importer.page = -1
		if err := importer.documentAttachments(ctx); err != nil {
			return nil, PDFImportReport{}, fmt.Errorf("import PDF attachments: %w", err)
		}
		if err := importer.outlines(ctx); err != nil {
			return nil, PDFImportReport{}, fmt.Errorf("import PDF outlines: %w", err)
		}
		if err := importer.documentOpenActions(ctx); err != nil {
			return nil, PDFImportReport{}, fmt.Errorf("import PDF document actions: %w", err)
		}
		importer.page = 0
	}
	if options.OnProgress != nil {
		if err := options.OnProgress("convert", 0, len(editor.pages)); err != nil {
			return nil, PDFImportReport{}, err
		}
	}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		matrix, width, height := pdfPageMatrix(page)
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
		importer.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		importer.clipPaths = nil
		importer.annotationData = nil
		if err := importer.pageOpenActions(ctx, page); err != nil {
			return fmt.Errorf("import PDF page %d actions: %w", index+1, err)
		}
		if err := importer.compositePage(nil, func(visitor pdfgo.Visitor) error { return reader.WalkPage(ctx, page, visitor) }); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if err := importer.flushPath(); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if err := importer.annotations(ctx, page, options.Strict); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if len(importer.annotationData) != 0 {
			if err := editor.appendAnnotations(index, importer.annotationData, true); err != nil {
				return fmt.Errorf("import PDF page %d: %w", index+1, err)
			}
			importer.annotationData = nil
		}
		if err := importer.commitObjects(); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		importer.report.Pages++
		if options.OnProgress != nil {
			if err := options.OnProgress("convert", index+1, len(editor.pages)); err != nil {
				return err
			}
		}
		if options.Progress != nil {
			return options.Progress(index + 1)
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	if importer.report.Pages == 0 {
		return nil, PDFImportReport{}, fmt.Errorf("PDF contains no pages")
	}
	return editor, importer.report, nil
}

// ConvertPDF 将PDF转换并写入OFD，转换阶段失败时不写入目标
// 入参: ctx 取消上下文, source PDF数据, size 字节数, output OFD输出, options 转换选项
// 返回: PDFImportReport 转换统计, error 错误信息
func ConvertPDF(ctx context.Context, source io.ReaderAt, size int64, output io.Writer, options PDFImportOptions) (PDFImportReport, error) {
	editor, report, err := ImportPDF(ctx, source, size, options)
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
	if err := p.objectValidation().appendOwnedObjects(p.ctx, p.page, objects); err != nil {
		return err
	}
	p.objects = nil
	return nil
}

// pdfPageMatrix 将裁剪框、用户单位及页面旋转转换为OFD毫米坐标
// 入参: page PDF页面
// 返回: pdfgo.Matrix 坐标变换, float64 页面宽度, float64 页面高度
func pdfPageMatrix(page *pdfgo.Page) (pdfgo.Matrix, float64, float64) {
	b := page.CropBox
	scale := 25.4 / 72 * page.UserUnit
	width, height := (b.XMax-b.XMin)*scale, (b.YMax-b.YMin)*scale
	m := pdfgo.Matrix{scale, 0, 0, -scale, -b.XMin * scale, b.YMax * scale}
	switch page.Rotation {
	case 90:
		m = pdfgo.Matrix{0, 1, -1, 0, height, 0}.Mul(m)
		width, height = height, width
	case 180:
		m = pdfgo.Matrix{-1, 0, 0, -1, width, height}.Mul(m)
	case 270:
		m = pdfgo.Matrix{0, -1, 1, 0, 0, width}.Mul(m)
		width, height = height, width
	}
	return m, width, height
}

// pdfNumbers 按OFD数值格式序列化坐标
// 入参: values 数值列表
// 返回: string 空格分隔的数值
func pdfNumbers(values ...float64) string {
	var buffer [128]byte
	data := buffer[:0]
	for n, v := range values {
		if n != 0 {
			data = append(data, ' ')
		}
		data = strconv.AppendFloat(data, v, 'g', -1, 64)
	}
	return string(data)
}

// pdfBoundary 序列化对象边界
// 入参: b 对象边界
// 返回: string OFD边界数据
func pdfBoundary(b Box) string { return pdfNumbers(b.X, b.Y, b.W, b.H) }

// pdfBounds 计算点集合的包围框
// 入参: points 坐标点集合
// 返回: Box 包围框
func pdfBounds(points []pdfgo.Point) Box {
	var bounds pdfPointBounds
	for _, point := range points {
		bounds.add(point)
	}
	return bounds.box()
}

// add 按原顺序累计一个控制点的最小和最大坐标
// 入参: point 坐标点
func (b *pdfPointBounds) add(point pdfgo.Point) {
	if !b.found {
		b.minX, b.minY, b.maxX, b.maxY = point.X, point.Y, point.X, point.Y
		b.found = true
	}
	b.minX, b.minY = math.Min(b.minX, point.X), math.Min(b.minY, point.Y)
	b.maxX, b.maxY = math.Max(b.maxX, point.X), math.Max(b.maxY, point.Y)
}

// box 取得原精度边界，空集合仍返回零值
// 返回: Box 坐标范围
func (b *pdfPointBounds) box() Box {
	if !b.found {
		return Box{}
	}
	return Box{b.minX, b.minY, math.Max(b.maxX-b.minX, 1e-6), math.Max(b.maxY-b.minY, 1e-6)}
}

// pathBoundsContext 逐段计算变换后的控制点边界，不创建临时点集合
// 入参: ctx 取消上下文, path PDF路径
// 返回: Box 控制点边界，空路径返回零值, error 取消错误
func (p *pdfImporter) pathBoundsContext(ctx context.Context, path pdfgo.Path) (Box, error) {
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	found := false
	for _, segment := range path.Segments {
		if err := ctx.Err(); err != nil {
			return Box{}, err
		}
		for index, point := range segment.Points {
			if index&255 == 0 {
				if err := ctx.Err(); err != nil {
					return Box{}, err
				}
			}
			point = p.matrix.Apply(point)
			minX, minY = math.Min(minX, point.X), math.Min(minY, point.Y)
			maxX, maxY = math.Max(maxX, point.X), math.Max(maxY, point.Y)
			found = true
		}
	}
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	if !found {
		return Box{}, nil
	}
	return Box{minX, minY, math.Max(maxX-minX, 1e-6), math.Max(maxY-minY, 1e-6)}, nil
}

// color 保留设备色彩分量并复用文档颜色空间
// 入参: paint PDF颜色与不透明度
// 返回: *FillColor OFD颜色
func (p *pdfImporter) color(paint pdfgo.Paint) *FillColor {
	alpha := int(math.Round(paint.Alpha * 255))
	if paint.CMYK != nil && (paint.Space == nil || !paint.Space.Calibrated()) {
		if p.cmykSpace == "" {
			p.cmykSpace = p.editor.nextID()
			p.editor.resources = append(p.editor.resources, editorResource{space: &ColorSpace{ID: p.cmykSpace, Type: "CMYK", BitsPerComponent: 16}})
		}
		v := paint.CMYK
		return &FillColor{ColorSpace: p.cmykSpace, Value: pdfNumbers(math.Round(v[0]*65535), math.Round(v[1]*65535), math.Round(v[2]*65535), math.Round(v[3]*65535)), Alpha: &alpha}
	}
	return &FillColor{Value: pdfNumbers(math.Round(paint.RGB[0]*255), math.Round(paint.RGB[1]*255), math.Round(paint.RGB[2]*255)), Alpha: &alpha}
}

// pathDataContext 转换路径并在分段处理时检查取消
// 入参: ctx 取消上下文, path PDF路径, origin 对象边界
// 返回: string OFD路径数据, error 取消错误
func (p *pdfImporter) pathDataContext(ctx context.Context, path pdfgo.Path, origin Box) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data := make([]byte, 0, len(path.Segments)*48)
	for index, segment := range path.Segments {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if index != 0 {
			data = append(data, ' ')
		}
		data = append(data, segment.Operator...)
		for _, point := range segment.Points {
			point = p.matrix.Apply(point)
			data = append(data, ' ')
			data = strconv.AppendFloat(data, point.X-origin.X, 'g', -1, 64)
			data = append(data, ' ')
			data = strconv.AppendFloat(data, point.Y-origin.Y, 'g', -1, 64)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(data), nil
}

// clips 省略覆盖对象边界的矩形裁剪，复用其余几何并保留位移及路径交集
// 入参: paths 裁剪路径, origin 对象边界
// 返回: *Clips OFD裁剪区域，无裁剪时为空, error 未解析的裁剪字形
func (p *pdfImporter) clips(paths []pdfgo.Path, origin Box) (*Clips, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var clips *Clips
	var areas []ClipArea
	translation := ""
	for _, path := range paths {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		if pdfClipContainsBoundary(path, p.matrix, origin) {
			continue
		}
		if clips == nil {
			clips = &Clips{Clip: make([]Clip, 0, len(paths))}
			areas = make([]ClipArea, len(paths))
			if origin.X != 0 || origin.Y != 0 {
				translation = pdfNumbers(1, 0, 0, 1, -origin.X, -origin.Y)
			}
		}
		index := len(clips.Clip)
		area := &areas[index]
		area.CTM = translation
		if len(path.Segments) != 0 {
			encoded, err := p.clipPath(path)
			if err != nil {
				return nil, err
			}
			area.Path = encoded.path[:1:1]
		}
		for _, mark := range path.Text {
			objects, ok := p.clipTexts[mark]
			if !ok {
				if err := p.flushPath(); err != nil {
					return nil, err
				}
				count := p.objects.len()
				textCount := p.report.TextObjects
				if err := p.text(pdfgo.TextMark{Font: mark.Font, Glyphs: mark.Glyphs, Positions: mark.Positions, Matrix: mark.Matrix, Size: mark.Size, HorizontalScale: mark.HorizontalScale, Mode: 7, Clip: mark, Style: pdfgo.Style{Fill: pdfgo.Paint{Alpha: 1}}}); err != nil {
					return nil, err
				}
				p.objects.truncate(count)
				p.report.TextObjects = textCount
				objects = p.clipTexts[mark]
			}
			if len(path.Text) == 1 {
				area.Text = objects[:len(objects):len(objects)]
			} else {
				area.Text = append(area.Text, objects...)
			}
		}
		if len(area.Path)+len(area.Text) == 0 {
			fill, stroke := true, false
			area.Path = []PathObject{{Boundary: "0 0 1 1", AbbreviatedData: "M 0 0 C", Fill: &fill, Stroke: &stroke}}
		}
		clips.Clip = append(clips.Clip, Clip{Area: areas[index : index+1 : index+1]})
	}
	return clips, nil
}

// path 合并相同状态下的不透明描边，保留独立子路径与绘制顺序
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) path(mark pdfgo.PathMark) error {
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "path blend mode " + string(mark.Style.BlendMode)}
	}
	if !mark.Stroke || mark.Fill || mark.Style.Stroke.Alpha != 1 {
		if err := p.flushPath(); err != nil {
			return err
		}
		return p.appendPath(mark)
	}
	if p.pendingPath != nil && (p.pendingPath.StrokeMatrix != mark.StrokeMatrix || !reflect.DeepEqual(p.pendingPath.Style, mark.Style)) {
		if err := p.flushPath(); err != nil {
			return err
		}
	}
	if p.pendingPath == nil {
		p.pendingPath = &mark
	} else {
		p.pendingPath.Path.Segments = append(p.pendingPath.Path.Segments, mark.Path.Segments...)
	}
	if len(p.pendingPath.Path.Segments) >= 512 {
		return p.flushPath()
	}
	return nil
}

// flushPath 写入连续描边批次，保持后续图元的叠放位置
// 返回: error 转换错误
func (p *pdfImporter) flushPath() error {
	if p.pendingPath == nil {
		return nil
	}
	mark := *p.pendingPath
	p.pendingPath = nil
	return p.appendPath(mark)
}

// appendPath 添加独立可编辑路径
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) appendPath(mark pdfgo.PathMark) error {
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Path(mark) })
		}
		return err
	}
	if mark.Fill && mark.Style.FillOverprint && pdfOverprintNeedsSeparation(mark.Style.Fill) || mark.Stroke && mark.Style.StrokeOverprint && pdfOverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "color separation overprint"}
	}
	if p.warning != nil && (mark.Fill && pdfGradientError(mark.Style.Fill) != nil || mark.Stroke && pdfGradientError(mark.Style.Stroke) != nil) {
		return p.gradientPath(mark)
	}
	box, err := p.pathBoundsContext(p.ctx, mark.Path)
	if err != nil {
		return err
	}
	if box == (Box{}) {
		return fmt.Errorf("empty PDF painted path")
	}
	scale := math.Hypot(p.matrix[0], p.matrix[1])
	drawing, origin, ctm := *p, box, ""
	marginScale := scale
	if mark.StrokeMatrix != (pdfgo.Matrix{}) {
		inverse, ok := mark.StrokeMatrix.Inverse()
		if !ok {
			return fmt.Errorf("invalid PDF stroke matrix")
		}
		drawing.matrix, origin, scale = inverse, Box{}, 1
		m := p.matrix.Mul(mark.StrokeMatrix)
		marginScale = math.Max(math.Hypot(m[0], m[2]), math.Hypot(m[1], m[3]))
	}
	if mark.Stroke {
		margin := mark.Style.LineWidth * marginScale / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{box.X - margin, box.Y - margin, box.W + 2*margin, box.H + 2*margin}
	}
	if mark.StrokeMatrix == (pdfgo.Matrix{}) {
		origin = box
	} else {
		m := p.matrix.Mul(mark.StrokeMatrix)
		m[4], m[5] = m[4]-box.X, m[5]-box.Y
		ctm = pdfNumbers(m[:]...)
	}
	paint := func(value pdfgo.Paint) (*FillColor, error) {
		if value.Tiling != nil {
			return p.paintColor(value, box)
		}
		return drawing.paintColor(value, origin)
	}
	fillColor, strokePaint := p.color(mark.Style.Fill), p.color(mark.Style.Stroke)
	if mark.Fill {
		fillColor, err = paint(mark.Style.Fill)
		if err != nil {
			return err
		}
	}
	if mark.Stroke {
		strokePaint, err = paint(mark.Style.Stroke)
		if err != nil {
			return err
		}
	}
	strokeColor := StrokeColor(*strokePaint)
	clips, err := p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	data, err := drawing.pathDataContext(p.ctx, mark.Path, origin)
	if err != nil {
		return err
	}
	object := PathObject{Boundary: pdfBoundary(box), CTM: ctm, AbbreviatedData: data, Fill: &mark.Fill, Stroke: &mark.Stroke, FillColor: fillColor, StrokeColor: &strokeColor, LineWidth: mark.Style.LineWidth * scale, Cap: []string{"Butt", "Round", "Square"}[mark.Style.Cap], Join: []string{"Miter", "Round", "Bevel"}[mark.Style.Join], MiterLimit: mark.Style.MiterLimit, Clips: clips}
	object.LineWidthSet = object.LineWidth == 0
	if mark.Path.EvenOdd {
		object.Rule = "Even-Odd"
	}
	if len(mark.Style.Dash) > 0 {
		values := make([]float64, len(mark.Style.Dash))
		for n, v := range mark.Style.Dash {
			values[n] = v * scale
		}
		object.DashPattern = pdfNumbers(values...)
		phase := mark.Style.DashPhase * scale
		object.DashOffset = &phase
	}
	p.appendObject(GraphicObject{Type: "PathObject", PathObject: object})
	p.report.PathObjects++
	return nil
}

// paintColor 将页面渐变转换为对象局部坐标
// 入参: paint PDF画刷, box 对象边界
// 返回: *FillColor OFD颜色或渐变, error 图案转换错误
func (p *pdfImporter) paintColor(paint pdfgo.Paint, box Box) (*FillColor, error) {
	if err := pdfGradientError(paint); err != nil {
		return nil, err
	}
	if paint.Mesh != nil {
		return p.meshColor(paint, box)
	}
	color := p.color(paint)
	if paint.Tiling != nil {
		pattern, err := p.tilingPattern(paint.Tiling, paint)
		if err != nil {
			return nil, err
		}
		color.Pattern = pattern
		return color, nil
	}
	if paint.Radial != nil {
		gradient := paint.Radial
		matrix := p.matrix
		if gradient.Matrix != (pdfgo.Matrix{}) {
			matrix = matrix.Mul(gradient.Matrix)
		}
		start, end := matrix.Apply(gradient.Start), matrix.Apply(gradient.End)
		xx, yy, xy := matrix[0]*matrix[0]+matrix[2]*matrix[2], matrix[1]*matrix[1]+matrix[3]*matrix[3], matrix[0]*matrix[1]+matrix[2]*matrix[3]
		major := (xx + yy + math.Hypot(xx-yy, 2*xy)) / 2
		determinant := matrix[0]*matrix[3] - matrix[1]*matrix[2]
		if major <= 0 || determinant == 0 {
			return nil, fmt.Errorf("invalid PDF radial gradient matrix")
		}
		scale := math.Sqrt(major)
		eccentricity := math.Sqrt(math.Max(0, 1-determinant*determinant/(major*major)))
		angle := math.Atan2(2*xy, xx-yy) * 90 / math.Pi
		extend := 0
		if gradient.Extend[0] {
			extend |= 1
		}
		if gradient.Extend[1] {
			extend |= 2
		}
		shading := &RadialShd{StartPoint: pdfNumbers(start.X-box.X, start.Y-box.Y), EndPoint: pdfNumbers(end.X-box.X, end.Y-box.Y), StartRadius: gradient.StartRadius * scale, EndRadius: gradient.EndRadius * scale, Eccentricity: eccentricity, Angle: angle, Extend: strconv.Itoa(extend)}
		var err error
		shading.Segment, err = p.gradientSegments(gradient.Stops, gradient.Space, gradient.Intent)
		if err != nil {
			return nil, err
		}
		color.Value = ""
		color.RadialShd = shading
		return color, nil
	}
	if paint.Axial == nil {
		return color, nil
	}
	gradient := paint.Axial
	start := p.matrix.Apply(gradient.Start)
	inverse, ok := p.matrix.Inverse()
	if !ok {
		return nil, fmt.Errorf("invalid PDF gradient matrix")
	}
	dx, dy := gradient.End.X-gradient.Start.X, gradient.End.Y-gradient.Start.Y
	gx, gy := inverse[0]*dx+inverse[1]*dy, inverse[2]*dx+inverse[3]*dy
	length := gx*gx + gy*gy
	if length == 0 {
		return nil, fmt.Errorf("invalid PDF axial gradient axis")
	}
	factor := (dx*dx + dy*dy) / length
	end := pdfgo.Point{X: start.X + gx*factor, Y: start.Y + gy*factor}
	extend := 0
	if gradient.Extend[0] {
		extend |= 1
	}
	if gradient.Extend[1] {
		extend |= 2
	}
	shading := &AxialShd{StartPoint: pdfNumbers(start.X-box.X, start.Y-box.Y), EndPoint: pdfNumbers(end.X-box.X, end.Y-box.Y), Extend: strconv.Itoa(extend)}
	var err error
	shading.Segment, err = p.gradientSegments(gradient.Stops, gradient.Space, gradient.Intent)
	if err != nil {
		return nil, err
	}
	color.Value = ""
	color.AxialShd = shading
	return color, nil
}

// buildTilingPattern 保留PDF图案单元与页面坐标之间的仿射关系
// 入参: source 平铺图案, base 无色图案基色
// 返回: *Pattern OFD图案, error 转换错误
func (p *pdfImporter) buildTilingPattern(source *pdfgo.TilingPattern, base pdfgo.Paint) (*Pattern, error) {
	const unit = 25.4 / 72
	box := source.BBox
	width, height := box.XMax-box.XMin, box.YMax-box.YMin
	xstep, ystep := math.Abs(source.XStep), math.Abs(source.YStep)
	if width <= 0 || height <= 0 || !finite(xstep) || !finite(ystep) || xstep == 0 || ystep == 0 {
		return nil, fmt.Errorf("invalid PDF tiling pattern dimensions")
	}
	localToPDF := pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, box.XMin, box.YMax}
	m := p.matrix.Mul(source.Matrix).Mul(localToPDF)
	pattern := &Pattern{Width: math.Min(width, xstep) * unit, Height: math.Min(height, ystep) * unit, XStep: xstep * unit, YStep: ystep * unit, RelativeTo: "Page", CTM: pdfNumbers(m[:]...)}
	cell := *p
	cell.matrix = pdfgo.Matrix{unit, 0, 0, -unit, -box.XMin * unit, box.YMax * unit}
	cell.pageWidth, cell.pageHeight = width*unit, height*unit
	cell.rasterDPI *= m.MaxScale()
	if !finite(cell.rasterDPI) || cell.rasterDPI < 0 {
		return nil, fmt.Errorf("invalid PDF tiling pattern raster scale")
	}
	cell.objects, cell.pendingPath = nil, nil
	if err := source.Walk(p.ctx, base, cell.visitor()); err != nil {
		return nil, err
	}
	if err := cell.flushPath(); err != nil {
		return nil, err
	}
	objects, err := cell.objects.data(p.ctx)
	if err != nil {
		return nil, err
	}
	pattern.CellContent.Objects = objects
	if len(objects) != 0 && (width > xstep || height > ystep) {
		columns, rows := math.Ceil(width/xstep), math.Ceil(height/ystep)
		if !finite(columns*rows) || columns*rows >= float64(uint(^uint(0)>>1)+1) {
			return nil, fmt.Errorf("PDF tiling pattern instance count overflow")
		}
		bounds := make([]Box, len(objects))
		for index, object := range objects {
			boundary := ""
			if object.Type == "PathObject" && object.PathObject.Stroke != nil && !*object.PathObject.Stroke {
				boundary = object.PathObject.Boundary
			} else if object.Type == "ImageObject" && object.ImageObject.Border == nil {
				boundary = object.ImageObject.Boundary
			}
			if boundary != "" {
				var err error
				bounds[index], err = ParseBox(boundary)
				if err != nil {
					return nil, err
				}
			}
		}
		left, right, top, bottom := pdfPatternInstanceRange(columns, rows, bounds, pattern.Width, pattern.Height, xstep*unit, ystep*unit)
		var expanded pdfObjectBuffer
		for row := top; row <= bottom; row++ {
			for column := left; column <= right; column++ {
				if err := p.ctx.Err(); err != nil {
					return nil, err
				}
				dx, dy := float64(column)*xstep*unit, float64(row)*ystep*unit
				for index, object := range objects {
					if index%pdfObjectChunkLimit == 0 {
						if err := p.ctx.Err(); err != nil {
							return nil, err
						}
					}
					b := bounds[index]
					if b.W > 0 && b.H > 0 && (b.X+dx+b.W < 0 || b.Y+dy+b.H < 0 || b.X+dx > pattern.Width || b.Y+dy > pattern.Height) {
						continue
					}
					translated, err := transformEditorObject(object, dx, dy, 1)
					if err != nil {
						return nil, err
					}
					expanded.append(translated)
				}
			}
		}
		pattern.CellContent.Objects, err = expanded.data(p.ctx)
		if err != nil {
			return nil, err
		}
	}
	return pattern, nil
}

// pdfOpaqueBlack 判断不受底层分色影响的全黑设备色
// 入参: paint PDF颜色与不透明度
// 返回: bool 是否为不透明全黑CMYK颜色
func pdfOpaqueBlack(paint pdfgo.Paint) bool {
	return paint.Alpha == 1 && paint.CMYK != nil && paint.CMYK[3] == 1
}

// pdfOverprintNeedsSeparation 判断彩色CMYK套印是否依赖分色输出
// 入参: paint PDF颜色与不透明度
// 返回: bool 是否需要保留分色语义
func pdfOverprintNeedsSeparation(paint pdfgo.Paint) bool {
	_, process := paint.Process.CMYKMask()
	return (paint.CMYK != nil || process) && !pdfOpaqueBlack(paint)
}
