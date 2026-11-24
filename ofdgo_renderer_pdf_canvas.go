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
	"bytes"
	"cmp"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/tdewolff/canvas"
	"github.com/xiaoqidun/j2kgo"
	"github.com/xiaoqidun/jbig2"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfRendererCtxKey 用于在页面渲染上下文中传递当前PDF渲染器以支持原生文字裁剪
type pdfRendererCtxKey struct{}

// pdfRawCacheKey 按只读底层源与规范化包路径跨派生阅读器复用原始图像流缓存
type pdfRawCacheKey struct {
	archiveID uint64
	zip       *zip.Reader
	encrypted *encryptedPackageSource
	memPtr    *byte
	memLen    int
	reader    *Reader
	path      string
}

// pdfImageCacheKey 按图像来源键、降采样目标尺寸与插值模式复用PDF图像XObject
type pdfImageCacheKey struct {
	key         any
	target      image.Point
	interpolate int8
}

// pdfTemplateFormKey 按模板页、页面尺寸、视口偏移、打印模式与高清替代策略复用PDF模板Form
type pdfTemplateFormKey struct {
	template      *PageContent
	width         float64
	height        float64
	offsetX       float64
	offsetY       float64
	printing      bool
	printContents string
	highRes       bool
}

// pdfStampFormKey 按印章OFD内容摘要、打印配置、注释开关、分辨率档位与页面缩放复用PDF印章Form集合
type pdfStampFormKey struct {
	digest        [32]byte
	printing      bool
	printContents string
	annotations   bool
	highRes       bool
	scaleStep     int
}

// pdfPageScopeNode 链式驻留页面局部资源作用域序列，确保多页并发缓存隔离且不被回收复用
type pdfPageScopeNode struct {
	res  *parsedRes
	prev *pdfPageScopeNode
}

// pdfCompositeFormKey 按复合图元定义、页面资源作用域、图案页变换、打印模式、分辨率、缩放档位与透明组隔离复用PDF Form
type pdfCompositeFormKey struct {
	unit           *CompositeGraphicUnit
	pageScope      *pdfPageScopeNode
	hasPatternPage bool
	patternPage    [6]float64
	printing       bool
	printContents  string
	highRes        bool
	scaleStep      int
	group          bool
}

// pdfEncodedImageEntry 保存并发去重编码后的PDF图像XObject待写出状态
type pdfEncodedImageEntry struct {
	done     chan struct{}
	name     pdfgo.Name
	ref      pdfgo.Reference
	smaskRef pdfgo.Reference
	encoded  codecpdf.EncodedImageStream
	err      error
}

// pdfFormEntry 保存并发去重编译后的PDF Form XObject待写出状态
type pdfFormEntry struct {
	done              chan struct{}
	waitingOn         *pdfFormEntry
	finished          bool
	name              pdfgo.Name
	ref               pdfgo.Reference
	width             float64
	height            float64
	transparencyGroup bool
	resources         *codecpdf.ContentResources
	data              []byte
	compressed        bool
	err               error
}

// pdfStampPageForm 保存单页OFD印章编译后的Form引用及原始物理尺寸
type pdfStampPageForm struct {
	name   pdfgo.Name
	ref    pdfgo.Reference
	width  float64
	height float64
}

// pdfStampFormGroup 保存整枚OFD印章的多页Form缓存项
type pdfStampFormGroup struct {
	done  chan struct{}
	forms []pdfStampPageForm
	err   error
}

// pdfStreamSession 管理整份PDF输出期间并发安全的共享字体、图像、Form及图形资源表
type pdfStreamSession struct {
	writer        *codecpdf.StreamWriter
	compression   CompressionOptions
	compress      bool
	compressLevel int
	fonts         *codecpdf.FontRegistry
	graphics      *codecpdf.GraphicResourceRegistry
	fontMu        sync.Mutex
	fontDigests   map[*canvas.Font][32]byte
	fontCanonical map[[32]byte]*canvas.Font
	rawMu         sync.Mutex
	rawCache      map[pdfRawCacheKey]*pdfRawImage
	rawDigest     map[[32]byte]*pdfRawImage
	imgMu         sync.Mutex
	imgEntries    map[pdfImageCacheKey]*pdfEncodedImageEntry
	imgSeq        int
	imgUniqueSeq  atomic.Uint64
	pendingImages []*pdfEncodedImageEntry
	formMu        sync.Mutex
	formSeq       int
	templateForms map[pdfTemplateFormKey]*pdfFormEntry
	stampForms    map[pdfStampFormKey]*pdfStampFormGroup
	compForms     map[pdfCompositeFormKey]*pdfFormEntry
	pageScopes    map[pdfPageScopeNode]*pdfPageScopeNode
	pendingForms  []*pdfFormEntry
	contentRefs   map[[32]byte]pdfgo.Reference
	templateHits  atomic.Int64
	stampHits     atomic.Int64
	compHits      atomic.Int64
	contentHits   atomic.Int64
}

// pdfRenderer PDF单遍流式渲染器，实现canvas.Renderer并支持原生Form复用与文字裁剪
type pdfRenderer struct {
	session          *pdfStreamSession
	ctx              context.Context
	activeComp       *pdfFormEntry
	width            float64
	height           float64
	content          *codecpdf.ContentBuilder
	contentStack     []*codecpdf.ContentBuilder
	sizeStack        [][2]float64
	groupAlphas      []uint8
	glyphPaths       renderCache[*canvas.Path, *canvas.Path]
	glyphUnicode     map[*canvas.Font]map[uint16]rune
	imageKey         any
	imageClip        *canvas.Path
	imageInterpolate *bool
	imageScale       float64
	imageError       error
	text             []codecpdf.TextPage
	textClip         *canvas.Path
	textClipFirst    int
	textClipEmpty    bool
}

// pdfRawImage 封装可直通写入PDF的JPEG、JPX或JBIG2原始图像流
type pdfRawImage struct {
	raw *codecpdf.RawImage
}

// pdfNavigation PDF导航信息
type pdfNavigation struct {
	Link          map[int][]pdfLink
	Outline       []pdfOutline
	Open          []pdfNavigationAction
	PageOpen      map[int][]pdfNavigationAction
	pageIndex     map[string]int
	bookmarks     map[string]Dest
	attachments   map[string]Attachment
	audio         map[string]*pdfgo.FileSpecification
	video         map[string]*pdfVideoResource
	mediaFiles    map[string]*pdfgo.FileSpecification
	pageResources []*parsedRes
	source        *Reader
	nativeWrite   bool
}

// pdfLink PDF链接
type pdfLink struct {
	Rect    canvas.Rect
	Region  *pdfgo.LinkRegion
	Actions []pdfNavigationAction
}

// pdfTarget 保存输出页索引及默认用户空间中的跳转参数
type pdfTarget struct {
	Page        int
	Destination pdfgo.Destination
}

// pdfOutline PDF大纲
type pdfOutline struct {
	Name    string
	Level   int
	Actions []pdfNavigationAction
}

// pdfNavigationAction 保存待写出的导航动作，输出时关联实际页面引用
type pdfNavigationAction struct {
	URI        string
	Target     *pdfTarget
	Attachment *pdfgo.AttachmentAction
	Sound      *pdfgo.SoundAction
	Movie      *pdfMovieAction
}

// newPDFStreamSession 创建支持多核并发资源登记的PDF流式写出共享会话
// 入参: writer 流式PDF写出器, compression 压缩选项
// 返回: *pdfStreamSession 共享会话实例
func newPDFStreamSession(writer *codecpdf.StreamWriter, compression CompressionOptions) *pdfStreamSession {
	level := zlib.DefaultCompression
	if compression.Mode != CompressionUnchanged {
		level = zlib.BestCompression
	}
	s := &pdfStreamSession{
		writer:        writer,
		compression:   compression,
		compress:      true,
		compressLevel: level,
		fontDigests:   make(map[*canvas.Font][32]byte),
		fontCanonical: make(map[[32]byte]*canvas.Font),
		rawCache:      make(map[pdfRawCacheKey]*pdfRawImage),
		rawDigest:     make(map[[32]byte]*pdfRawImage),
		imgEntries:    make(map[pdfImageCacheKey]*pdfEncodedImageEntry),
		templateForms: make(map[pdfTemplateFormKey]*pdfFormEntry),
		stampForms:    make(map[pdfStampFormKey]*pdfStampFormGroup),
		compForms:     make(map[pdfCompositeFormKey]*pdfFormEntry),
		pageScopes:    make(map[pdfPageScopeNode]*pdfPageScopeNode),
		contentRefs:   make(map[[32]byte]pdfgo.Reference),
	}
	s.fonts = codecpdf.NewFontRegistry(writer.AllocRef, true, true, level)
	s.graphics = codecpdf.NewGraphicResourceRegistry(writer.AllocRef)
	return s
}

// newPDFPageRenderer 为单页创建独占的流式PDF渲染器
// 入参: session 共享PDF会话, ctx 取消上下文, width 页面宽度(毫米), height 页面高度(毫米), resources 缓存配额来源
// 返回: *pdfRenderer 页面渲染器
func newPDFPageRenderer(session *pdfStreamSession, ctx context.Context, width, height float64, resources *Resources) *pdfRenderer {
	return &pdfRenderer{
		session:    session,
		ctx:        ctx,
		width:      width,
		height:     height,
		content:    codecpdf.NewContentBuilder(session),
		glyphPaths: *resourceCache[*canvas.Path, *canvas.Path](resources, 16<<20),
		text:       make([]codecpdf.TextPage, 1),
	}
}

// newPDFFormRenderer 为PDF Form XObject创建独占的流式渲染器，避免误假设继承自调用点的默认图形状态
// 入参: session 共享PDF会话, ctx 取消上下文, width 表单宽度(毫米), height 表单高度(毫米), resources 缓存配额来源
// 返回: *pdfRenderer 表单渲染器
func newPDFFormRenderer(session *pdfStreamSession, ctx context.Context, width, height float64, resources *Resources) *pdfRenderer {
	return &pdfRenderer{
		session:    session,
		ctx:        ctx,
		width:      width,
		height:     height,
		content:    codecpdf.NewFormContentBuilder(session),
		glyphPaths: *resourceCache[*canvas.Path, *canvas.Path](resources, 16<<20),
		text:       make([]codecpdf.TextPage, 1),
	}
}

// newPDFNavigation 建立文档导航与输出页索引，不读取页面图元
// 入参: renderer 渲染器, doc 文档结构, pages 输出页面描述
// 返回: *pdfNavigation 导航信息, error 资源或目标错误
func newPDFNavigation(renderer *Renderer, doc *Document, pages []RenderDocumentPage) (*pdfNavigation, error) {
	ctx := renderer.outputContext()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	navigation := &pdfNavigation{
		Link:      make(map[int][]pdfLink),
		PageOpen:  make(map[int][]pdfNavigationAction),
		pageIndex: make(map[string]int, len(pages)),
		bookmarks: make(map[string]Dest),
		source:    renderer.reader,
	}
	if doc != nil && (doc.Attachments.Path != "" || len(doc.Attachments.Attachment) != 0) {
		attachments, err := renderer.reader.AttachmentsContext(ctx)
		if err != nil {
			return nil, err
		}
		navigation.attachments = make(map[string]Attachment, len(attachments))
		for _, attachment := range attachments {
			if attachment.ID == "" {
				continue
			}
			if _, exists := navigation.attachments[attachment.ID]; exists {
				return nil, fmt.Errorf("duplicate attachment identifier: %s", attachment.ID)
			}
			navigation.attachments[attachment.ID] = attachment
		}
		for i, attachment := range attachments {
			if attachment.ID != "" {
				continue
			}
			key := fmt.Sprintf("attachment-%d", i+1)
			for {
				if _, exists := navigation.attachments[key]; !exists {
					break
				}
				key += "-"
			}
			navigation.attachments[key] = attachment
		}
		navigation.nativeWrite = len(attachments) != 0
	}
	pageIndex := navigation.pageIndex
	for i, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := page.Source.ID
		if page.Content != nil {
			id = page.Content.ID
		}
		pageIndex[id] = i
	}
	bookmarks := navigation.bookmarks
	if doc != nil {
		for _, bookmark := range doc.Bookmarks.Bookmark {
			bookmarks[bookmark.Name] = bookmark.Dest
		}
		if err := navigation.addLifecycleActions(renderer.outputContext(), actionSource{Actions: doc.Actions}, -1, bookmarks, pageIndex, pages); err != nil {
			return nil, err
		}
	}
	return navigation, nil
}

// pdfSourceRect 转换PDF动作区域
// 入参: box OFD区域, pageH 页面高度
// 返回: canvas.Rect PDF区域
func pdfSourceRect(box Box, pageH float64) canvas.Rect {
	return canvas.RectFromSize(box.X, pageH-box.Y-box.H, box.W, box.H)
}

// pdfDestination 按OFD目标语义及目标页物理区域转换PDF坐标、显示模式与缩放比例
// 入参: dest OFD跳转目标, box 目标页面物理区域
// 返回: pdfgo.Destination 待关联输出页的目标, error 目标参数错误
func pdfDestination(dest Dest, box Box) (pdfgo.Destination, error) {
	if _, err := dest.attributes(); err != nil {
		return pdfgo.Destination{}, err
	}
	val := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	left := val(dest.Left) - box.X
	top := val(dest.Top) - box.Y
	right := val(dest.Right) - box.X
	bottom := val(dest.Bottom) - box.Y
	const scale = 72.0 / 25.4
	result := pdfgo.Destination{Mode: pdfgo.Name(dest.Type)}
	switch dest.Type {
	case "XYZ":
		result.Parameters = pdfgo.Array{pdfgo.Real(left * scale), pdfgo.Real((box.H - top) * scale), pdfgo.Real(val(dest.Zoom))}
	case "FitH":
		result.Parameters = pdfgo.Array{pdfgo.Real((box.H - top) * scale)}
	case "FitV":
		result.Parameters = pdfgo.Array{pdfgo.Real(left * scale)}
	case "FitR":
		result.Parameters = pdfgo.Array{pdfgo.Real(left * scale), pdfgo.Real((box.H - bottom) * scale), pdfgo.Real(right * scale), pdfgo.Real((box.H - top) * scale)}
	}
	return result, nil
}

// validJPEGStream 按JPEG标记结构快速校验字节流完整性，不执行像素解码
// 入参: data JPEG原始字节
// 返回: bool 字节流是否完整包含SOI、SOF、SOS与EOI
func validJPEGStream(data []byte) bool {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return false
	}
	hasSOF := false
	hasSOS := false
	i := 2
	for i < len(data) {
		if data[i] != 0xFF {
			return false
		}
		for i < len(data) && data[i] == 0xFF {
			i++
		}
		if i >= len(data) {
			return false
		}
		marker := data[i]
		i++
		if marker == 0x00 || marker == 0xD8 {
			return false
		}
		if marker == 0xD9 {
			return hasSOF && hasSOS
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			continue
		}
		if i+2 > len(data) {
			return false
		}
		segLen := int(data[i])<<8 | int(data[i+1])
		if segLen < 2 || i+segLen > len(data) {
			return false
		}
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if segLen < 8 {
				return false
			}
			hasSOF = true
		}
		if marker == 0xDA {
			if !hasSOF || segLen < 6 {
				return false
			}
			hasSOS = true
			i += segLen
			for i < len(data) {
				idx := bytes.IndexByte(data[i:], 0xFF)
				if idx < 0 {
					return false
				}
				i += idx
				if i+1 >= len(data) {
					return false
				}
				if data[i+1] == 0x00 {
					i += 2
					continue
				}
				j := i + 1
				for j < len(data) && data[j] == 0xFF {
					j++
				}
				if j >= len(data) {
					return false
				}
				next := data[j]
				if next >= 0xD0 && next <= 0xD7 {
					i = j + 1
					continue
				}
				if next == 0x00 {
					return false
				}
				i = j - 1
				break
			}
			continue
		}
		i += segLen
	}
	return false
}

// isJP2Container 判断字节流是否为JP2/JPX容器格式
// 入参: data 原始图像字节
// 返回: bool 是否以JP2签名盒开头
func isJP2Container(data []byte) bool {
	return len(data) >= 12 && bytes.Equal(data[:12], []byte{0x00, 0x00, 0x00, 0x0C, 0x6A, 0x50, 0x20, 0x20, 0x0D, 0x0A, 0x87, 0x0A})
}

// isJ2KCodestream 判断字节流是否为原始JPEG2000码流
// 入参: data 原始图像字节
// 返回: bool 是否以SOC+SIZ标记开头
func isJ2KCodestream(data []byte) bool {
	return len(data) >= 4 && data[0] == 0xFF && data[1] == 0x4F && data[2] == 0xFF && data[3] == 0x51
}

// isJBIG2File 判断字节流是否为独立JBIG2文件
// 入参: data 原始图像字节
// 返回: bool 是否以JBIG2文件魔数开头
func isJBIG2File(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[:8], []byte{0x97, 0x4A, 0x42, 0x32, 0x0D, 0x0A, 0x1A, 0x0A})
}

// tryPDFRawImage 检测不透明JPEG、JP2/JPX与JBIG2原始流并构造零像素重编码直通对象
// 入参: ctx 取消上下文, data 原始图像字节
// 返回: *pdfRawImage 可直通图像，不满足条件时为nil
func tryPDFRawImage(ctx context.Context, data []byte) *pdfRawImage {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || len(data) == 0 {
		return nil
	}
	if baseimage.IsJPEG(data) && validJPEGStream(data) {
		enc, _, err := baseimage.DecodeContext(ctx, data)
		if err != nil {
			return nil
		}
		encoded, ok := wrapEncodedImage(enc).(*EncodedImage)
		if !ok {
			return nil
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || encoded.Bounds() != image.Rect(0, 0, cfg.Width, cfg.Height) {
			return nil
		}
		if encoded.ColorModel() != cfg.ColorModel {
			return nil
		}
		colorSpace := ""
		switch cfg.ColorModel {
		case color.YCbCrModel:
			colorSpace = "DeviceRGB"
		case color.GrayModel:
			colorSpace = "DeviceGray"
		default:
			return nil
		}
		return &pdfRawImage{raw: &codecpdf.RawImage{Filter: "DCTDecode", Width: cfg.Width, Height: cfg.Height, BitsPerComponent: 8, ColorSpace: colorSpace, Data: data}}
	}
	if isJP2Container(data) || isJ2KCodestream(data) {
		decoder, err := j2kgo.NewDecoder(ctx, bytes.NewReader(data), nil)
		if err != nil {
			return nil
		}
		info := decoder.Info()
		_ = decoder.Close()
		w, h := info.Bounds.Dx(), info.Bounds.Dy()
		if w <= 0 || h <= 0 || len(info.Components) == 0 {
			return nil
		}
		for _, ch := range info.Channels {
			if ch.Type == j2kgo.ChannelOpacity || ch.Type == j2kgo.ChannelPremultiplied {
				return nil
			}
		}
		bpc := info.Components[0].Precision
		if bpc == 0 || bpc > 16 || info.Components[0].Signed {
			return nil
		}
		for _, comp := range info.Components[1:] {
			if comp.Precision != bpc || comp.Signed {
				return nil
			}
		}
		raw := &codecpdf.RawImage{Filter: "JPXDecode", Width: w, Height: h, Data: data}
		if !isJP2Container(data) {
			switch bpc {
			case 1, 2, 4, 8, 16:
			default:
				return nil
			}
			switch len(info.Components) {
			case 1:
				raw.ColorSpace = "DeviceGray"
			case 3:
				raw.ColorSpace = "DeviceRGB"
			default:
				return nil
			}
			raw.BitsPerComponent = int(bpc)
		}
		return &pdfRawImage{raw: raw}
	}
	if isJBIG2File(data) {
		cfg, err := jbig2.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.ColorModel != color.GrayModel {
			return nil
		}
		embedded, ok := extractEmbeddedJBIG2(data)
		if !ok {
			decoded, decErr := jbig2.Decode(bytes.NewReader(data))
			if decErr != nil {
				return nil
			}
			var out bytes.Buffer
			if encErr := jbig2.EncodeEmbeddedContext(ctx, &out, decoded, nil); encErr != nil {
				return nil
			}
			embedded = out.Bytes()
		}
		return &pdfRawImage{raw: &codecpdf.RawImage{Filter: "JBIG2Decode", Width: cfg.Width, Height: cfg.Height, BitsPerComponent: 1, ColorSpace: "DeviceGray", Data: embedded}}
	}
	return nil
}

// extractEmbeddedJBIG2 从单页顺序组织JBIG2文件剥离文件头与结束段得到PDF嵌入式流
// 入参: data 独立JBIG2文件字节
// 返回: []byte PDF嵌入式JBIG2流, bool 是否成功剥离
func extractEmbeddedJBIG2(data []byte) ([]byte, bool) {
	dataLen := uint64(len(data))
	if dataLen < 9 || !isJBIG2File(data) {
		return nil, false
	}
	flags := data[8]
	if flags&1 == 0 {
		return nil, false
	}
	pos := uint64(9)
	if flags&2 == 0 {
		if dataLen < 13 {
			return nil, false
		}
		if binary.BigEndian.Uint32(data[9:13]) != 1 {
			return nil, false
		}
		pos = 13
	}
	var out bytes.Buffer
	out.Grow(len(data) - int(pos))
	pageInfos := 0
	for pos < dataLen {
		segStart := pos
		if dataLen-pos < 6 {
			return nil, false
		}
		segNum := binary.BigEndian.Uint32(data[pos : pos+4])
		segFlags := data[pos+4]
		segType := segFlags & 0x3F
		pageAssocLong := segFlags&0x40 != 0
		pos += 5
		refByte := data[pos]
		refCount := uint64(refByte >> 5)
		if refCount <= 4 {
			pos++
		} else if refCount == 7 {
			if dataLen-pos < 4 {
				return nil, false
			}
			refCount = uint64(binary.BigEndian.Uint32(data[pos:pos+4]) & 0x1FFFFFFF)
			retBytes := (refCount + 8) / 8
			if dataLen-pos < 4+retBytes {
				return nil, false
			}
			pos += 4 + retBytes
		} else {
			return nil, false
		}
		refSize := uint64(4)
		if segNum <= 256 {
			refSize = 1
		} else if segNum <= 65536 {
			refSize = 2
		}
		pageAssocSize := uint64(1)
		if pageAssocLong {
			pageAssocSize = 4
		}
		refBytes := refCount * refSize
		if dataLen-pos < refBytes+pageAssocSize+4 {
			return nil, false
		}
		pos += refBytes + pageAssocSize
		segDataLen := binary.BigEndian.Uint32(data[pos : pos+4])
		pos += 4
		if segDataLen == 0xFFFFFFFF || dataLen-pos < uint64(segDataLen) {
			return nil, false
		}
		pos += uint64(segDataLen)
		if segType == 48 {
			pageInfos++
			if pageInfos > 1 {
				return nil, false
			}
		}
		if segType == 49 || segType == 51 {
			continue
		}
		out.Write(data[segStart:pos])
	}
	if out.Len() == 0 {
		return nil, false
	}
	return out.Bytes(), true
}

// pdfContextDone 返回上下文取消通道，空上下文返回nil
// 入参: ctx 取消上下文
// 返回: <-chan struct{} 取消通道
func pdfContextDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

// pdfContextErr 返回上下文取消错误，空上下文返回nil
// 入参: ctx 取消上下文
// 返回: error 取消错误
func pdfContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// isPDFCancelError 判断错误是否属于取消或超时导致的失败，避免将其缓存到共享单飞表
// 入参: ctx 取消上下文, err 错误信息
// 返回: bool 是否为取消或超时错误
func isPDFCancelError(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() != nil))
}

// FontResource 实现ContentResourceSink，按字体内容摘要跨协程共享同一PDF字体对象
// 入参: font 画布字体, vertical 是否竖排
// 返回: pdfgo.Name 资源名, pdfgo.Reference 对象引用, *codecpdf.FontEntry 字体项
func (s *pdfStreamSession) FontResource(font *canvas.Font, vertical bool) (pdfgo.Name, pdfgo.Reference, *codecpdf.FontEntry) {
	if font != nil {
		font = s.canonicalFont(font)
	}
	return s.fonts.Register(font, vertical)
}

// OpacityGS 实现ContentResourceSink，登记或复用透明度ExtGState资源
// 入参: alpha 八位透明度
// 返回: pdfgo.Name 状态名, pdfgo.Reference 对象引用
func (s *pdfStreamSession) OpacityGS(alpha uint8) (pdfgo.Name, pdfgo.Reference) {
	return s.graphics.OpacityGS(alpha)
}

// OpacityPairGS 实现ContentResourceSink，登记或复用独立填充与描边透明度ExtGState资源
// 入参: fillAlpha 填充透明度, strokeAlpha 描边透明度
// 返回: pdfgo.Name 状态名, pdfgo.Reference 对象引用
func (s *pdfStreamSession) OpacityPairGS(fillAlpha, strokeAlpha uint8) (pdfgo.Name, pdfgo.Reference) {
	return s.graphics.OpacityPairGS(fillAlpha, strokeAlpha)
}

// StandardType1Font 实现ContentResourceSink，登记或复用标准Type1字体资源
// 入参: baseFont 标准字体名
// 返回: pdfgo.Name 字体资源名, pdfgo.Reference 对象引用
func (s *pdfStreamSession) StandardType1Font(baseFont string) (pdfgo.Name, pdfgo.Reference) {
	return s.graphics.StandardType1Font(baseFont)
}

// ShadingPattern 实现ContentResourceSink，登记或复用渐变Pattern资源
// 入参: gradient 画布渐变, m 变换矩阵
// 返回: pdfgo.Name Pattern名, pdfgo.Reference 对象引用
func (s *pdfStreamSession) ShadingPattern(gradient canvas.Gradient, m canvas.Matrix) (pdfgo.Name, pdfgo.Reference) {
	return s.graphics.ShadingPattern(gradient, m)
}

// canonicalFont 按只读SFNT表摘要将来自不同派生渲染器的相同字体归一化为单一实例，零拷贝不分配完整字体字节
// 入参: font 画布字体
// 返回: *canvas.Font 归一化字体实例
func (s *pdfStreamSession) canonicalFont(font *canvas.Font) *canvas.Font {
	s.fontMu.Lock()
	if digest, ok := s.fontDigests[font]; ok {
		canon := s.fontCanonical[digest]
		s.fontMu.Unlock()
		return canon
	}
	s.fontMu.Unlock()
	h := sha256.New()
	tags := make([]string, 0, len(font.Tables))
	for tag := range font.Tables {
		tags = append(tags, tag)
	}
	slices.SortFunc(tags, cmp.Compare[string])
	var lenBuf [8]byte
	for _, tag := range tags {
		h.Write([]byte(tag))
		table := font.Tables[tag]
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(table)))
		h.Write(lenBuf[:])
		h.Write(table)
	}
	var digest [32]byte
	h.Sum(digest[:0])
	s.fontMu.Lock()
	defer s.fontMu.Unlock()
	s.fontDigests[font] = digest
	if canon, ok := s.fontCanonical[digest]; ok {
		return canon
	}
	s.fontCanonical[digest] = font
	return font
}

// registerImage 线程安全地按去重键并发编码单张PDF图像，首次编码完成后进入待写出队列
// 入参: ctx 取消上下文, img 图像, rawPass 可选直通原始流, imageKey 可选外部去重键, target 目标降采样尺寸, interpolate 可选插值开关
// 返回: pdfgo.Name XObject资源名, pdfgo.Reference 对象引用, error 编码错误
func (s *pdfStreamSession) registerImage(ctx context.Context, img image.Image, rawPass *codecpdf.RawImage, imageKey any, target image.Point, interpolate *bool) (pdfgo.Name, pdfgo.Reference, error) {
	unwrapped := unwrapEncodedImage(img)
	var dedupKey any
	if rawPass != nil && rawPass.Key != nil {
		dedupKey = rawPass.Key
	} else if imageKey != nil {
		dedupKey = imageKey
	} else if unwrapped != nil && reflect.TypeOf(unwrapped).Comparable() {
		dedupKey = unwrapped
	} else {
		dedupKey = s.imgUniqueSeq.Add(1)
	}
	var interpKey int8
	if interpolate != nil {
		if *interpolate {
			interpKey = 1
		} else {
			interpKey = -1
		}
	}
	cacheKey := pdfImageCacheKey{key: dedupKey, target: target, interpolate: interpKey}
	s.imgMu.Lock()
	if existing, ok := s.imgEntries[cacheKey]; ok {
		s.imgMu.Unlock()
		select {
		case <-pdfContextDone(ctx):
			return "", pdfgo.Reference{}, pdfContextErr(ctx)
		case <-existing.done:
			return existing.name, existing.ref, existing.err
		}
	}
	entry := &pdfEncodedImageEntry{
		done: make(chan struct{}),
		name: pdfgo.Name("Im" + strconv.Itoa(s.imgSeq)),
		ref:  s.writer.AllocRef(),
	}
	s.imgSeq++
	s.imgEntries[cacheKey] = entry
	s.imgMu.Unlock()
	completed := false
	var err error
	defer func() {
		if !completed {
			entry.err = fmt.Errorf("PDF image encoding aborted")
		}
		if !completed || isPDFCancelError(ctx, err) {
			s.imgMu.Lock()
			if s.imgEntries[cacheKey] == entry {
				delete(s.imgEntries, cacheKey)
			}
			s.imgMu.Unlock()
		}
		close(entry.done)
	}()
	var encoded codecpdf.EncodedImageStream
	if rawPass != nil {
		rawCopy := *rawPass
		rawCopy.Interpolate = interpolate
		encoded, err = codecpdf.EncodeRawImageStream(ctx, &rawCopy, s.compression.pdfOptions(), target)
	} else {
		encoded, err = codecpdf.EncodePDFImageStreamInterpolate(ctx, unwrapped, s.compression.pdfOptions(), target, s.compressLevel, interpolate)
	}
	if err == nil && len(encoded.SMaskData) > 0 {
		entry.smaskRef = s.writer.AllocRef()
		encoded.Dict["SMask"] = entry.smaskRef
	}
	entry.encoded = encoded
	entry.err = err
	if err == nil {
		s.imgMu.Lock()
		s.pendingImages = append(s.pendingImages, entry)
		s.imgMu.Unlock()
	}
	completed = true
	return entry.name, entry.ref, err
}

// allocFormNameRef 线程安全地分配新的Form XObject资源名与间接引用
// 返回: pdfgo.Name Form名称, pdfgo.Reference 对象引用
func (s *pdfStreamSession) allocFormNameRef() (pdfgo.Name, pdfgo.Reference) {
	s.formMu.Lock()
	name := pdfgo.Name("Fm" + strconv.Itoa(s.formSeq))
	s.formSeq++
	s.formMu.Unlock()
	return name, s.writer.AllocRef()
}

// enqueueForm 将已完成编译压缩的Form XObject加入主协程待写出队列
// 入参: entry Form缓存项
func (s *pdfStreamSession) enqueueForm(entry *pdfFormEntry) {
	s.formMu.Lock()
	s.pendingForms = append(s.pendingForms, entry)
	s.formMu.Unlock()
}

// flushPending 在主协程中流式写出所有新增图形字典、图像流与Form流并立即释放其内存缓冲
// 返回: error 写入错误
func (s *pdfStreamSession) flushPending() error {
	if err := s.graphics.FlushPending(s.writer); err != nil {
		return err
	}
	s.imgMu.Lock()
	imgs := s.pendingImages
	s.pendingImages = nil
	s.imgMu.Unlock()
	for _, entry := range imgs {
		if len(entry.encoded.SMaskData) > 0 {
			if err := s.writer.WritePreencodedStream(entry.smaskRef, entry.encoded.SMaskDict, entry.encoded.SMaskData); err != nil {
				return err
			}
			entry.encoded.SMaskData = nil
			entry.encoded.SMaskDict = nil
		}
		if err := s.writer.WritePreencodedStream(entry.ref, entry.encoded.Dict, entry.encoded.Data); err != nil {
			return err
		}
		entry.encoded.Data = nil
		entry.encoded.Dict = nil
	}
	s.formMu.Lock()
	forms := s.pendingForms
	s.pendingForms = nil
	s.formMu.Unlock()
	for _, form := range forms {
		resRef, err := s.graphics.ResourceDictRef(s.writer, form.resources)
		if err != nil {
			return err
		}
		dict := codecpdf.FormXObjectDict(form.width, form.height, resRef, form.transparencyGroup)
		if form.compressed {
			dict["Filter"] = pdfgo.Name("FlateDecode")
			if err := s.writer.WritePreencodedStream(form.ref, dict, form.data); err != nil {
				return err
			}
		} else {
			if err := s.writer.WriteStream(form.ref, dict, form.data, true); err != nil {
				return err
			}
		}
		form.data = nil
		form.resources = nil
	}
	return nil
}

// allocFormNameRefUnlocked 在已持有formMu时分配Form资源名与间接对象引用
// 返回: pdfgo.Name Form名称, pdfgo.Reference 对象引用
func (s *pdfStreamSession) allocFormNameRefUnlocked() (pdfgo.Name, pdfgo.Reference) {
	name := pdfgo.Name("Fm" + strconv.Itoa(s.formSeq))
	s.formSeq++
	return name, s.writer.AllocRef()
}

// Size 返回当前PDF页面或Form画布尺寸(毫米)
// 返回: float64 宽度, float64 高度
func (r *pdfRenderer) Size() (float64, float64) {
	return r.width, r.height
}

// RenderPath 直接向单遍内容流写入路径绘图指令
// 入参: path 几何路径, style 绘制样式, matrix 变换矩阵
func (r *pdfRenderer) RenderPath(path *canvas.Path, style canvas.Style, matrix canvas.Matrix) {
	if r.imageError != nil {
		return
	}
	r.content.RenderPath(path, style, matrix)
}

// RenderImage 单遍登记并编码图像XObject，直接向内容流写入图像绘制与独立裁剪指令
// 入参: img 图像, matrix 图像变换
func (r *pdfRenderer) RenderImage(img image.Image, matrix canvas.Matrix) {
	if r.imageError != nil {
		return
	}
	var rawPass *codecpdf.RawImage
	if rawImg, ok := img.(*pdfRawImage); ok && rawImg != nil && rawImg.raw != nil {
		rawPass = rawImg.raw
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		r.imageError = fmt.Errorf("PDF output image pixel bounds differ")
		return
	}
	if rawPass == nil {
		if encoded, ok := img.(*EncodedImage); ok && encoded.MIME() == "image/jpeg" {
			if rawImg := r.rawJPEGFromEncoded(encoded); rawImg != nil {
				rawPass = rawImg.raw
			}
		}
	}
	var target image.Point
	if dpi := r.session.compression.ImageDPI(); dpi > 0 {
		widthMM := math.Hypot(matrix[0][0], matrix[1][0]) * float64(w)
		heightMM := math.Hypot(matrix[0][1], matrix[1][1]) * float64(h)
		if r.imageScale > 1 {
			widthMM *= r.imageScale
			heightMM *= r.imageScale
		}
		target = codecpdf.TargetImageSize(widthMM, heightMM, dpi)
		if target.X >= w && target.Y >= h {
			target = image.Point{}
		}
	}
	interpolate := r.imageInterpolate
	if interpolate == nil && w <= 8 && h <= 8 {
		sx := math.Hypot(matrix[0][0], matrix[1][0]) * codecpdf.PtPerMM
		sy := math.Hypot(matrix[0][1], matrix[1][1]) * codecpdf.PtPerMM
		if sx >= 4 && sy >= 4 {
			disabled := false
			interpolate = &disabled
		}
	}
	name, ref, err := r.session.registerImage(r.ctx, img, rawPass, r.imageKey, target, interpolate)
	if err != nil {
		r.imageError = err
		return
	}
	r.content.DrawImage(name, ref, w, h, matrix, r.imageClip)
}

// glyphPath 在缓存预算内复用相似变换下的PDF字形圆弧转换
// 入参: path 缓存字形路径, matrix 字形变换
// 返回: *canvas.Path PDF字形路径
func (r *pdfRenderer) glyphPath(path *canvas.Path, matrix canvas.Matrix) *canvas.Path {
	if !matrix.IsSimilarity() {
		return path
	}
	if cached, ok := r.glyphPaths.get(path); ok {
		return cached
	}
	converted := path.ReplaceArcs()
	cost := 256 + cap(path.Data())*8
	if converted != path {
		cost += cap(converted.Data()) * 8
	}
	r.glyphPaths.put(path, converted, cost)
	return converted
}

// pushForm 压栈切换到一个独立Form XObject内容流构建上下文
// 入参: width 表单宽度(毫米), height 表单高度(毫米)
func (r *pdfRenderer) pushForm(width, height float64) {
	r.contentStack = append(r.contentStack, r.content)
	r.sizeStack = append(r.sizeStack, [2]float64{r.width, r.height})
	r.text = append(r.text, codecpdf.TextPage{})
	r.content = codecpdf.NewFormContentBuilder(r.session)
	r.width, r.height = width, height
}

// popForm 弹出当前Form上下文，压缩其内容流并恢复父级内容流构建上下文
// 入参: transparencyGroup 是否作为隔离透明组Form
// 返回: *pdfFormEntry 编译后的Form对象, error 压缩错误
func (r *pdfRenderer) popForm(transparencyGroup bool) (*pdfFormEntry, error) {
	builder := r.content
	w, h := r.width, r.height
	top := len(r.contentStack) - 1
	r.content = r.contentStack[top]
	r.contentStack = r.contentStack[:top]
	r.width, r.height = r.sizeStack[top][0], r.sizeStack[top][1]
	r.sizeStack = r.sizeStack[:top]
	r.text = r.text[:len(r.text)-1]
	raw := builder.Bytes()
	data := raw
	compressed := false
	if r.session.compress {
		comp, _, err := codecpdf.CompressBytes(nil, nil, r.session.compressLevel, raw)
		if err != nil {
			return nil, err
		}
		data = comp
		compressed = true
	}
	name, ref := r.session.allocFormNameRef()
	entry := &pdfFormEntry{
		name:              name,
		ref:               ref,
		width:             w,
		height:            h,
		transparencyGroup: transparencyGroup,
		resources:         builder.Resources(),
		data:              data,
		compressed:        compressed,
	}
	return entry, nil
}

// beginTransparencyGroup 开启PDF原生隔离透明组Form上下文
// 入参: alpha 透明组整体不透明度
// 返回: bool 是否开启成功
func (r *pdfRenderer) beginTransparencyGroup(alpha uint8) bool {
	if r.imageError != nil {
		return false
	}
	r.groupAlphas = append(r.groupAlphas, alpha)
	r.pushForm(r.width, r.height)
	return true
}

// endTransparencyGroup 闭合当前透明组Form并在父级内容流中按整体透明度合成调用
func (r *pdfRenderer) endTransparencyGroup() {
	if len(r.groupAlphas) == 0 {
		return
	}
	alpha := r.groupAlphas[len(r.groupAlphas)-1]
	r.groupAlphas = r.groupAlphas[:len(r.groupAlphas)-1]
	entry, err := r.popForm(true)
	if err != nil {
		if r.imageError == nil {
			r.imageError = err
		}
		return
	}
	r.session.enqueueForm(entry)
	inv := 1.0 / codecpdf.PtPerMM
	r.content.DrawForm(entry.name, entry.ref, canvas.Identity.Scale(inv, inv), nil, alpha)
}

// rawImageResource 在无蒙版且不透明时读取并按路径与内容摘要去重缓存JPEG、JPX或JBIG2原始流直通对象
// 入参: renderer 渲染器, obj 图像对象
// 返回: *pdfRawImage 可直通图像，不满足条件时为nil
func (r *pdfRenderer) rawImageResource(renderer *Renderer, obj ImageObject) *pdfRawImage {
	if renderer == nil || renderer.reader == nil || obj.ImageMask != "" {
		return nil
	}
	if obj.Alpha != nil && clampColor(*obj.Alpha) < 255 {
		return nil
	}
	primary := obj.ResourceID
	if obj.Substitution != "" && !sameResourceID(obj.Substitution, obj.ResourceID) && (renderer.printing || renderer.dpi >= 300) {
		primary = obj.Substitution
	}
	resPath, ok := renderer.reader.resolveResPathScoped(renderer.pageResources, primary)
	if !ok {
		return nil
	}
	clean := cleanPackagePath(renderer.reader.ResPath(resPath))
	var cacheKey pdfRawCacheKey
	if memData, inMem := renderer.reader.files[clean]; inMem {
		if len(memData) == 0 {
			return nil
		}
		cacheKey = pdfRawCacheKey{memPtr: &memData[0], memLen: len(memData), path: clean}
	} else if renderer.reader.archiveID != 0 {
		cacheKey = pdfRawCacheKey{archiveID: renderer.reader.archiveID, path: clean}
	} else if renderer.reader.zip != nil {
		cacheKey = pdfRawCacheKey{zip: renderer.reader.zip, path: clean}
	} else if src := renderer.reader.encryptedSource(); src != nil {
		cacheKey = pdfRawCacheKey{encrypted: src, path: clean}
	} else {
		cacheKey = pdfRawCacheKey{reader: renderer.reader, path: clean}
	}
	r.session.rawMu.Lock()
	if cached, found := r.session.rawCache[cacheKey]; found {
		r.session.rawMu.Unlock()
		return cached
	}
	r.session.rawMu.Unlock()
	ctx := renderer.pageContext
	if ctx == nil {
		ctx = renderer.outputContext()
	}
	data, err := renderer.reader.readFileViewContext(ctx, clean)
	if err != nil {
		return nil
	}
	digest := sha256.Sum256(data)
	r.session.rawMu.Lock()
	if cached, found := r.session.rawDigest[digest]; found {
		r.session.rawCache[cacheKey] = cached
		r.session.rawMu.Unlock()
		return cached
	}
	r.session.rawMu.Unlock()
	raw := tryPDFRawImage(ctx, data)
	if raw == nil && ctx.Err() != nil {
		return nil
	}
	if raw != nil {
		raw.raw.Key = raw
	}
	r.session.rawMu.Lock()
	if existing, found := r.session.rawDigest[digest]; found {
		r.session.rawCache[cacheKey] = existing
		r.session.rawMu.Unlock()
		return existing
	}
	r.session.rawDigest[digest] = raw
	r.session.rawCache[cacheKey] = raw
	r.session.rawMu.Unlock()
	return raw
}

// rawJPEGFromEncoded 将不透明且无额外色彩转换的惰性JPEG图像转入内容去重的DCTDecode直通对象
// 入参: encoded 惰性编码JPEG图像
// 返回: *pdfRawImage 可直通JPEG对象，不满足条件时为nil
func (r *pdfRenderer) rawJPEGFromEncoded(encoded *EncodedImage) *pdfRawImage {
	data := encoded.Bytes()
	if !validJPEGStream(data) {
		return nil
	}
	digest := sha256.Sum256(data)
	r.session.rawMu.Lock()
	if cached, found := r.session.rawDigest[digest]; found {
		r.session.rawMu.Unlock()
		return cached
	}
	r.session.rawMu.Unlock()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || encoded.Bounds() != image.Rect(0, 0, cfg.Width, cfg.Height) {
		return nil
	}
	if encoded.ColorModel() != cfg.ColorModel {
		return nil
	}
	colorSpace := ""
	switch cfg.ColorModel {
	case color.YCbCrModel:
		colorSpace = "DeviceRGB"
	case color.GrayModel:
		colorSpace = "DeviceGray"
	default:
		return nil
	}
	raw := &pdfRawImage{raw: &codecpdf.RawImage{Filter: "DCTDecode", Width: cfg.Width, Height: cfg.Height, BitsPerComponent: 8, ColorSpace: colorSpace, Data: data}}
	raw.raw.Key = raw
	r.session.rawMu.Lock()
	if existing, found := r.session.rawDigest[digest]; found {
		r.session.rawMu.Unlock()
		return existing
	}
	r.session.rawDigest[digest] = raw
	r.session.rawMu.Unlock()
	return raw
}

// ColorModel 返回直通图像的占位颜色模型
// 返回: color.Model 颜色模型
func (p *pdfRawImage) ColorModel() color.Model {
	if p.raw.ColorSpace == "DeviceGray" {
		return color.GrayModel
	}
	return color.RGBAModel
}

// Bounds 返回直通图像的真实像素尺寸
// 返回: image.Rectangle 图像边界
func (p *pdfRawImage) Bounds() image.Rectangle {
	return image.Rect(0, 0, p.raw.Width, p.raw.Height)
}

// At 返回直通图像占位像素
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 透明占位色
func (p *pdfRawImage) At(x, y int) color.Color {
	return color.Transparent
}

// addPage 收集当前页面及模板、注释中的导航，不保留页面图元
// 入参: renderer 渲染器, index 输出页索引, page 页面内容, pages 输出页面描述
// 返回: error 区域、目标或取消错误
func (n *pdfNavigation) addPage(renderer *Renderer, index int, page *PageContent, pages []RenderDocumentPage) error {
	ctx := renderer.outputContext()
	if err := ctx.Err(); err != nil {
		return err
	}
	if renderer.reader != nil && renderer.reader.doc == nil && renderer.reader.ofd != nil {
		if _, err := renderer.reader.docStructureContext(ctx); err != nil {
			return err
		}
	}
	defer renderer.bindPageResources(page)()
	prevResources := n.pageResources
	n.pageResources = renderer.pageResources
	defer func() { n.pageResources = prevResources }()
	for _, source := range renderer.pageOpenActionSources(page) {
		if err := n.addLifecycleActions(ctx, source, index, n.bookmarks, n.pageIndex, pages); err != nil {
			return err
		}
	}
	sources := renderer.pageActionSources(page)
	if renderer.renderAnnotations && renderer.reader != nil {
		annotations, err := renderer.reader.pageAnnotationsContext(ctx, page.ID)
		if err != nil {
			return err
		}
		if renderer.printing {
			filtered := make([]Annotation, 0, len(annotations))
			for _, annot := range annotations {
				if annot.Print != nil && !*annot.Print {
					continue
				}
				filtered = append(filtered, annot)
			}
			annotations = filtered
		}
		sources = append(sources, renderer.annotationActionSources(page, annotations)...)
	}
	sources = renderer.translateActionSources(page, sources)
	for _, source := range sources {
		if err := n.addActions(renderer, index, source, n.bookmarks, n.pageIndex, pages); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// actionTarget 转换动作及资源引用，选页未包含的目标不写入
// 入参: action 动作, page 来源页索引，负值表示文档, source 动作来源, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
// 返回: *pdfNavigationAction 导航动作，nil表示无输出动作, error 目标参数错误
func (n *pdfNavigation) actionTarget(action Action, page int, source actionSource, bookmarks map[string]Dest, pageIndex map[string]int, pages []RenderDocumentPage) (*pdfNavigationAction, error) {
	if action.Goto != nil {
		dest := gotoDest(action.Goto, bookmarks)
		if dest == nil {
			return nil, nil
		}
		target, ok := resourceValue(pageIndex, dest.PageID)
		if !ok {
			return nil, nil
		}
		destination, err := pdfDestination(*dest, pages[target].Box)
		if err != nil {
			return nil, err
		}
		view, _ := n.source.PDFDestination(*dest)
		switch destination.Mode {
		case "XYZ":
			if view.KeepLeft {
				destination.Parameters[0] = nil
			}
			if view.KeepTop {
				destination.Parameters[1] = nil
			}
		case "FitH":
			if view.KeepTop {
				destination.Parameters[0] = nil
			}
		case "FitV":
			if view.KeepLeft {
				destination.Parameters[0] = nil
			}
		}
		return &pdfNavigationAction{Target: &pdfTarget{Page: target, Destination: destination}}, nil
	}
	if action.URI != nil && action.URI.URI != "" {
		return &pdfNavigationAction{URI: resolveActionURI(*action.URI)}, nil
	}
	if action.GotoA != nil {
		if attachment, exists := n.attachments[action.GotoA.AttachID]; !exists || attachment.ID != action.GotoA.AttachID {
			return nil, fmt.Errorf("attachment not found: %s", action.GotoA.AttachID)
		}
		window := true
		if action.GotoA.NewWindow != nil {
			window = *action.GotoA.NewWindow
		}
		return &pdfNavigationAction{Attachment: &pdfgo.AttachmentAction{Key: action.GotoA.AttachID, NewWindow: &window}}, nil
	}
	if action.Sound != nil {
		return n.soundAction(*action.Sound)
	}
	if action.Movie != nil {
		return n.movieAction(*action.Movie, page, source, pages)
	}
	return nil, nil
}

// addOutlines 保留目录层级及节点自身的导航动作，选页时移除无保留子项的失效跳转节点
// 入参: outlines 大纲节点, level 节点层级, bookmarks 书签, pageIndex 页面索引表, pages 页面数据
// 返回: error 目标参数错误
func (n *pdfNavigation) addOutlines(outlines []OutlineElem, level int, bookmarks map[string]Dest, pageIndex map[string]int, pages []RenderDocumentPage) error {
	for _, outline := range outlines {
		entry := pdfOutline{Name: outline.Title, Level: level}
		onlyMissingTargets := len(outline.Actions) != 0
		for _, action := range outline.Actions {
			if action.Goto == nil {
				onlyMissingTargets = false
			}
			value, err := n.actionTarget(action, -1, actionSource{}, bookmarks, pageIndex, pages)
			if err != nil {
				return err
			}
			if value != nil {
				entry.Actions = append(entry.Actions, *value)
			}
		}
		index := len(n.Outline)
		n.Outline = append(n.Outline, entry)
		if err := n.addOutlines(outline.OutlineElem, level+1, bookmarks, pageIndex, pages); err != nil {
			return err
		}
		if onlyMissingTargets && len(entry.Actions) == 0 && len(n.Outline) == index+1 {
			n.Outline = n.Outline[:index]
		}
	}
	n.nativeWrite = n.nativeWrite || len(n.Outline) != 0
	return nil
}

// pageNavLinks 将指定页面的链接定义转换为点空间NavLink列表
// 入参: page 页面索引, pageRefs 全部预分配页面对象引用
// 返回: []codecpdf.NavLink 点空间链接列表
func (n *pdfNavigation) pageNavLinks(page int, pageRefs []pdfgo.Reference) []codecpdf.NavLink {
	links := n.Link[page]
	if len(links) == 0 {
		return nil
	}
	result := make([]codecpdf.NavLink, len(links))
	for i, link := range links {
		result[i] = codecpdf.NavLink{
			Rect: pdfgo.Rectangle{
				XMin: link.Rect.X0 * codecpdf.PtPerMM,
				YMin: link.Rect.Y0 * codecpdf.PtPerMM,
				XMax: link.Rect.X1 * codecpdf.PtPerMM,
				YMax: link.Rect.Y1 * codecpdf.PtPerMM,
			},
			Region:  link.Region,
			Actions: pdfNavigationValues(link.Actions, pageRefs),
		}
	}
	return result
}

// hasDeferredMedia 判断当前导航状态是否包含可能跨页添加屏幕注解或延迟加载媒体的动作
// 返回: bool 是否含视频或音频动作
func (n *pdfNavigation) hasDeferredMedia() bool {
	return len(n.video) > 0 || len(n.audio) > 0
}

// drawPDFTemplatePage 将OFD模板页编译为全局复用的PDF Form XObject并在当前页调用
// 入参: pdfOut PDF渲染器, template 模板页内容, offsetX 视口横偏移, offsetY 视口纵偏移, pageOrigin 页面原点变换, pageH 页面高度
// 返回: error 渲染或压缩错误
func (r *Renderer) drawPDFTemplatePage(pdfOut *pdfRenderer, template *PageContent, offsetX, offsetY float64, pageOrigin *Matrix, pageH float64) error {
	key := pdfTemplateFormKey{
		template:      template,
		width:         pdfOut.width,
		height:        pdfOut.height,
		offsetX:       offsetX,
		offsetY:       offsetY,
		printing:      r.printing,
		printContents: r.printContents,
		highRes:       r.dpi >= 300,
	}
	pdfOut.session.formMu.Lock()
	if existing, ok := pdfOut.session.templateForms[key]; ok {
		pdfOut.session.formMu.Unlock()
		select {
		case <-pdfContextDone(pdfOut.ctx):
			return pdfContextErr(pdfOut.ctx)
		case <-existing.done:
		}
		if existing.err != nil {
			return existing.err
		}
		pdfOut.session.templateHits.Add(1)
		inv := 1.0 / codecpdf.PtPerMM
		pdfOut.content.DrawForm(existing.name, existing.ref, canvas.Identity.Scale(inv, inv), nil, 255)
		return nil
	}
	name, ref := pdfOut.session.allocFormNameRefUnlocked()
	entry := &pdfFormEntry{
		done:   make(chan struct{}),
		name:   name,
		ref:    ref,
		width:  pdfOut.width,
		height: pdfOut.height,
	}
	pdfOut.session.templateForms[key] = entry
	pdfOut.session.formMu.Unlock()
	var walkErr error
	completed := false
	defer func() {
		if !completed {
			entry.err = fmt.Errorf("PDF template form aborted")
		}
		if !completed || isPDFCancelError(pdfOut.ctx, walkErr) {
			pdfOut.session.formMu.Lock()
			if pdfOut.session.templateForms[key] == entry {
				delete(pdfOut.session.templateForms, key)
			}
			pdfOut.session.formMu.Unlock()
		}
		close(entry.done)
	}()
	subPDF := newPDFFormRenderer(pdfOut.session, pdfOut.ctx, pdfOut.width, pdfOut.height, r.reader.resources)
	subPDF.activeComp = pdfOut.activeComp
	var subErr error
	func() {
		prevCtx, prevErr, prevRes := r.pageContext, r.renderError, r.pageResources
		r.pageContext, r.renderError, r.pageResources = context.WithValue(pdfOut.ctx, pdfRendererCtxKey{}, subPDF), nil, nil
		defer func() {
			r.pageContext, r.renderError, r.pageResources = prevCtx, prevErr, prevRes
		}()
		subVisitor := &canvasPageVisitor{renderer: r, ctx: canvas.NewContext(subPDF), height: pageH}
		restoreTpl := r.bindParsedResources(template.pageResources)
		defer restoreTpl()
		for templateOrder := range 3 {
			if walkErr = r.walkLayers(template.Content.Layer, templateOrder, pageOrigin, subVisitor); walkErr != nil {
				break
			}
		}
		subErr = r.renderError
	}()
	if walkErr == nil {
		if subPDF.imageError != nil {
			walkErr = subPDF.imageError
		} else if subErr != nil {
			walkErr = subErr
		}
	}
	if walkErr == nil {
		raw := subPDF.content.Bytes()
		data := raw
		compressed := false
		if pdfOut.session.compress {
			comp, _, compErr := codecpdf.CompressBytes(nil, nil, pdfOut.session.compressLevel, raw)
			if compErr != nil {
				walkErr = compErr
			} else {
				data = comp
				compressed = true
			}
		}
		if walkErr == nil {
			entry.resources = subPDF.content.Resources()
			entry.data = data
			entry.compressed = compressed
			pdfOut.session.enqueueForm(entry)
		}
	}
	entry.err = walkErr
	completed = true
	if walkErr != nil {
		return walkErr
	}
	inv := 1.0 / codecpdf.PtPerMM
	pdfOut.content.DrawForm(entry.name, entry.ref, canvas.Identity.Scale(inv, inv), nil, 255)
	return nil
}

// drawPDFOFDStamp 将OFD矢量印章编译为全局复用的PDF Form XObject，骑缝章每页仅施加裁剪与变换
// 入参: pdfOut PDF渲染器, s 印章定义, pageH 页面高度, parentClip 父裁剪路径
// 返回: error 渲染或压缩错误
func (r *Renderer) drawPDFOFDStamp(pdfOut *pdfRenderer, s Stamp, pageH float64, parentClip *canvas.Path) error {
	ctx := r.pageContext
	if ctx == nil {
		ctx = pdfOut.ctx
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	validBox := rasterPositive(s.Box.W) && rasterPositive(s.Box.H) && (s.Clip == nil || rasterPositive(s.Clip.W) && rasterPositive(s.Clip.H))
	if !validBox {
		return nil
	}
	session, err := r.loadStampOFD(ctx, s.Data)
	if err != nil {
		return err
	}
	maxScale := 1.0
	for _, sealBox := range session.boxes {
		if rasterPositive(sealBox.W) && rasterPositive(sealBox.H) {
			maxScale = max(maxScale, s.Box.W/sealBox.W, s.Box.H/sealBox.H)
		}
	}
	if pdfOut.imageScale > 1 {
		maxScale *= pdfOut.imageScale
	}
	scaleStep := 0
	var subScale float64
	if maxScale > 1 {
		scaleStep = int(math.Ceil(maxScale * 4))
		subScale = float64(scaleStep) / 4
	}
	key := pdfStampFormKey{
		digest:        sha256.Sum256(s.Data),
		printing:      r.printing,
		printContents: r.printContents,
		annotations:   r.renderAnnotations,
		highRes:       r.dpi >= 300,
		scaleStep:     scaleStep,
	}
	pdfOut.session.formMu.Lock()
	group, exists := pdfOut.session.stampForms[key]
	if !exists {
		group = &pdfStampFormGroup{done: make(chan struct{})}
		pdfOut.session.stampForms[key] = group
		pdfOut.session.formMu.Unlock()
		func() {
			var compileErr error
			completed := false
			defer func() {
				if !completed {
					group.err = fmt.Errorf("PDF stamp form aborted")
				}
				if !completed || isPDFCancelError(ctx, compileErr) {
					pdfOut.session.formMu.Lock()
					if pdfOut.session.stampForms[key] == group {
						delete(pdfOut.session.stampForms, key)
					}
					pdfOut.session.formMu.Unlock()
				}
				close(group.done)
			}()
			forms := make([]pdfStampPageForm, 0, len(session.pages))
			for i, content := range session.pages {
				sealBox := session.boxes[i]
				if !rasterPositive(sealBox.W) || !rasterPositive(sealBox.H) {
					continue
				}
				subPDF := newPDFFormRenderer(pdfOut.session, ctx, sealBox.W, sealBox.H, r.reader.resources)
				subPDF.activeComp = pdfOut.activeComp
				subPDF.imageScale = subScale
				pageRenderer := session.renderer
				var renderErr error
				func() {
					restoreState := syncStampRendererState(pageRenderer, r, context.WithValue(ctx, pdfRendererCtxKey{}, subPDF))
					defer restoreState()
					if scale := max(s.Box.W/sealBox.W, s.Box.H/sealBox.H); scale > 1 {
						limit := 4096 * 25.4 / max(sealBox.W, sealBox.H)
						if pageRenderer.dpi < limit {
							pageRenderer.dpi = min(pageRenderer.dpi*scale, limit)
						}
					}
					renderErr = pageRenderer.renderCanvasPageClipped(canvas.NewContext(subPDF), content, false, nil)
				}()
				if renderErr != nil {
					compileErr = renderErr
					break
				}
				if subPDF.imageError != nil {
					compileErr = subPDF.imageError
					break
				}
				raw := subPDF.content.Bytes()
				data := raw
				compressed := false
				if pdfOut.session.compress {
					comp, _, compErr := codecpdf.CompressBytes(nil, nil, pdfOut.session.compressLevel, raw)
					if compErr != nil {
						compileErr = compErr
						break
					}
					data = comp
					compressed = true
				}
				name, ref := pdfOut.session.allocFormNameRef()
				pdfOut.session.enqueueForm(&pdfFormEntry{
					name:       name,
					ref:        ref,
					width:      sealBox.W,
					height:     sealBox.H,
					resources:  subPDF.content.Resources(),
					data:       data,
					compressed: compressed,
				})
				forms = append(forms, pdfStampPageForm{name: name, ref: ref, width: sealBox.W, height: sealBox.H})
			}
			group.forms = forms
			group.err = compileErr
			completed = true
		}()
	} else {
		pdfOut.session.formMu.Unlock()
		select {
		case <-pdfContextDone(ctx):
			return pdfContextErr(ctx)
		case <-group.done:
		}
		if group.err == nil {
			pdfOut.session.stampHits.Add(1)
		}
	}
	if group.err != nil {
		return group.err
	}
	var clip *canvas.Path
	if parentClip != nil || s.Clip != nil {
		clip = canvas.Rectangle(s.Box.W, s.Box.H).Translate(s.Box.X, pageH-s.Box.Y-s.Box.H)
		if parentClip != nil {
			clip = r.intersectCachedClipPath(clip, parentClip)
		}
		if s.Clip != nil {
			rect := canvas.Rectangle(s.Clip.W, s.Clip.H).Translate(s.Box.X+s.Clip.X, pageH-s.Box.Y-s.Clip.Y-s.Clip.H)
			clip = r.intersectCachedClipPath(clip, rect)
		}
		if clip.Empty() {
			return nil
		}
	}
	for _, pf := range group.forms {
		m := canvas.Identity.Translate(s.Box.X, pageH-s.Box.Y-s.Box.H).Scale((s.Box.W/pf.width)/codecpdf.PtPerMM, (s.Box.H/pf.height)/codecpdf.PtPerMM)
		pdfOut.content.DrawForm(pf.name, pf.ref, m, clip, 255)
	}
	return nil
}

// internPageScopeUnlocked 在formMu持锁期间驻留页面局部资源序列，返回可比较的唯一作用域节点
// 入参: res 页面局部资源列表
// 返回: *pdfPageScopeNode 唯一作用域节点
func (s *pdfStreamSession) internPageScopeUnlocked(res []*parsedRes) *pdfPageScopeNode {
	var cur *pdfPageScopeNode
	for _, item := range res {
		if item == nil {
			continue
		}
		k := pdfPageScopeNode{res: item, prev: cur}
		if existing, ok := s.pageScopes[k]; ok {
			cur = existing
			continue
		}
		node := &pdfPageScopeNode{res: item, prev: cur}
		s.pageScopes[k] = node
		cur = node
	}
	return cur
}

// compositeUsesPagePattern 检查复合图元或其嵌套成员是否包含相对页面定位的底纹
// 入参: unit 复合图元定义, outer 外层已访问复合图元集合
// 返回: bool 是否包含页面相对底纹
func (r *Renderer) compositeUsesPagePattern(unit *CompositeGraphicUnit, outer map[*CompositeGraphicUnit]bool) bool {
	if unit == nil {
		return false
	}
	var seen map[*CompositeGraphicUnit]bool
	var seenPatterns map[*Pattern]bool
	var hasPagePatternColor func(*FillColor) bool
	var checkObjects func(func(func(GraphicObject) bool)) bool
	hasPagePatternColor = func(fc *FillColor) bool {
		if fc == nil || fc.Pattern == nil {
			return false
		}
		if fc.Pattern.RelativeTo == "Page" {
			return true
		}
		if seenPatterns[fc.Pattern] {
			return false
		}
		if seenPatterns == nil {
			seenPatterns = make(map[*Pattern]bool)
		}
		seenPatterns[fc.Pattern] = true
		defer delete(seenPatterns, fc.Pattern)
		return checkObjects(fc.Pattern.CellContent.objects())
	}
	checkDrawParam := func(id string) bool {
		if id == "" {
			return false
		}
		dp := r.drawParamDefaults(id, nil)
		if dp == nil {
			return false
		}
		return hasPagePatternColor(dp.FillColor) || hasPagePatternColor((*FillColor)(dp.StrokeColor))
	}
	checkObjects = func(seq func(func(GraphicObject) bool)) bool {
		for member := range seq {
			switch member.Type {
			case "PathObject":
				if hasPagePatternColor(member.PathObject.FillColor) || hasPagePatternColor((*FillColor)(member.PathObject.StrokeColor)) || checkDrawParam(member.PathObject.DrawParam) {
					return true
				}
			case "TextObject":
				if hasPagePatternColor(member.TextObject.FillColor) || hasPagePatternColor((*FillColor)(member.TextObject.StrokeColor)) || checkDrawParam(member.TextObject.DrawParam) {
					return true
				}
			case "ImageObject":
				if member.ImageObject.Border != nil && hasPagePatternColor((*FillColor)(member.ImageObject.Border.BorderColor)) {
					return true
				}
			case "CompositeObject":
				if member.CompositeObject.ResourceID != "" {
					if child := r.compositeDefinition(member.CompositeObject.ResourceID); child != nil && !outer[child] && !seen[child] {
						if seen == nil {
							seen = make(map[*CompositeGraphicUnit]bool)
						}
						seen[child] = true
						found := checkObjects(child.objects())
						delete(seen, child)
						if found {
							return true
						}
					}
				}
			}
		}
		return false
	}
	seen = map[*CompositeGraphicUnit]bool{unit: true}
	return checkObjects(unit.objects())
}

// drawPDFCompositeForm 将文档级复用复合图元编译为全局共享PDF Form XObject并按实例变换调用
// 入参: pdfOut PDF渲染器, ref 复合图元定义, matrix 实例到页面矩阵, stateClip 页面几何裁剪, cvClip 画布裁剪, pageH 页面高度, alpha 整体不透明度, needGroup 是否需透明组隔离, references 当前引用链
// 返回: error 渲染或压缩错误
func (r *Renderer) drawPDFCompositeForm(pdfOut *pdfRenderer, ref *CompositeGraphicUnit, matrix Matrix, stateClip *GeometryPath, cvClip *canvas.Path, pageH float64, alpha uint8, needGroup bool, references map[*CompositeGraphicUnit]bool) error {
	if alpha == 0 {
		return nil
	}
	var clip *canvas.Path
	if stateClip != nil {
		var err error
		clip, err = geometryToCanvasPath(stateClip)
		if err != nil {
			return err
		}
		if clip != nil {
			clip = clip.Translate(0, pageH)
		}
	}
	clip = r.intersectCachedClipPath(cvClip, clip)
	if clip != nil && clip.Empty() {
		return nil
	}
	if err := pdfContextErr(pdfOut.ctx); err != nil {
		return err
	}
	scaleStep := 0
	var subScale float64
	if pdfOut.session.compression.ImageDPI() > 0 {
		scale := max(math.Hypot(matrix.a, matrix.b), math.Hypot(matrix.c, matrix.d))
		if pdfOut.imageScale > 1 {
			scale *= pdfOut.imageScale
		}
		if scale > 1 {
			scaleStep = int(math.Ceil(scale * 4))
			subScale = float64(scaleStep) / 4
		}
	}
	key := pdfCompositeFormKey{unit: ref, printing: r.printing, printContents: r.printContents, highRes: r.dpi >= 300, scaleStep: scaleStep, group: needGroup}
	if r.patternPage != nil {
		key.hasPatternPage = true
		key.patternPage = r.patternPage.Values()
	}
	pdfOut.session.formMu.Lock()
	key.pageScope = pdfOut.session.internPageScopeUnlocked(r.pageResources)
	if existing, ok := pdfOut.session.compForms[key]; ok {
		waitingSet := false
		if !existing.finished && pdfOut.activeComp != nil {
			for cur := existing; cur != nil; cur = cur.waitingOn {
				if cur == pdfOut.activeComp {
					pdfOut.session.formMu.Unlock()
					return fmt.Errorf("cyclic composite resource %q", ref.ID)
				}
			}
			pdfOut.activeComp.waitingOn = existing
			waitingSet = true
		}
		pdfOut.session.formMu.Unlock()
		var waitErr error
		select {
		case <-pdfContextDone(pdfOut.ctx):
			waitErr = pdfContextErr(pdfOut.ctx)
		case <-existing.done:
		}
		if waitingSet {
			pdfOut.session.formMu.Lock()
			if pdfOut.activeComp.waitingOn == existing {
				pdfOut.activeComp.waitingOn = nil
			}
			pdfOut.session.formMu.Unlock()
		}
		if waitErr != nil {
			return waitErr
		}
		if existing.err != nil {
			return existing.err
		}
		pdfOut.session.compHits.Add(1)
		inv := 1.0 / codecpdf.PtPerMM
		m := canvas.Matrix{
			{matrix.a * inv, -matrix.c * inv, matrix.e + matrix.c*ref.Height},
			{-matrix.b * inv, matrix.d * inv, pageH - matrix.f - matrix.d*ref.Height},
		}
		pdfOut.content.DrawForm(existing.name, existing.ref, m, clip, alpha)
		return nil
	}
	name, formRef := pdfOut.session.allocFormNameRefUnlocked()
	entry := &pdfFormEntry{
		done:              make(chan struct{}),
		name:              name,
		ref:               formRef,
		width:             ref.Width,
		height:            ref.Height,
		transparencyGroup: needGroup,
	}
	pdfOut.session.compForms[key] = entry
	var prevWaiting *pdfFormEntry
	if pdfOut.activeComp != nil {
		prevWaiting = pdfOut.activeComp.waitingOn
		pdfOut.activeComp.waitingOn = entry
	}
	pdfOut.session.formMu.Unlock()
	var walkErr error
	completed := false
	defer func() {
		if !completed {
			entry.err = fmt.Errorf("PDF composite form aborted")
		}
		pdfOut.session.formMu.Lock()
		entry.finished = true
		if pdfOut.activeComp != nil && pdfOut.activeComp.waitingOn == entry {
			pdfOut.activeComp.waitingOn = prevWaiting
		}
		if (!completed || isPDFCancelError(pdfOut.ctx, walkErr)) && pdfOut.session.compForms[key] == entry {
			delete(pdfOut.session.compForms, key)
		}
		pdfOut.session.formMu.Unlock()
		close(entry.done)
	}()
	subPDF := newPDFFormRenderer(pdfOut.session, pdfOut.ctx, ref.Width, ref.Height, r.reader.resources)
	subPDF.activeComp = entry
	subPDF.imageScale = subScale
	var subErr error
	func() {
		prevCtx, prevErr, prevPattern := r.pageContext, r.renderError, r.patternPage
		r.pageContext, r.renderError = context.WithValue(pdfOut.ctx, pdfRendererCtxKey{}, subPDF), nil
		defer func() {
			r.pageContext, r.renderError, r.patternPage = prevCtx, prevErr, prevPattern
		}()
		subVisitor := &canvasPageVisitor{renderer: r, ctx: canvas.NewContext(subPDF), height: ref.Height}
		nextRefs := make(map[*CompositeGraphicUnit]bool, len(references)+1)
		for k, v := range references {
			nextRefs[k] = v
		}
		nextRefs[ref] = true
		identity := IdentityMatrix()
		var formClip *GeometryPath
		if !pageSkipsClips(subVisitor) {
			formClip, walkErr = r.compositeExtentClip(*ref, identity, nil)
		}
		if walkErr == nil {
			formState := RenderState{Parent: &identity, BoundaryInCTM: true, Clip: formClip}
			for member := range ref.objects() {
				if err := r.walkObject(&member, formState, subVisitor, nextRefs); err != nil {
					walkErr = err
					break
				}
			}
		}
		subErr = r.renderError
	}()
	if walkErr == nil {
		if subPDF.imageError != nil {
			walkErr = subPDF.imageError
		} else if subErr != nil {
			walkErr = subErr
		}
	}
	if walkErr == nil {
		raw := subPDF.content.Bytes()
		data := raw
		compressed := false
		if pdfOut.session.compress {
			comp, _, compErr := codecpdf.CompressBytes(nil, nil, pdfOut.session.compressLevel, raw)
			if compErr != nil {
				walkErr = compErr
			} else {
				data = comp
				compressed = true
			}
		}
		if walkErr == nil {
			entry.resources = subPDF.content.Resources()
			entry.data = data
			entry.compressed = compressed
			pdfOut.session.enqueueForm(entry)
		}
	}
	entry.err = walkErr
	completed = true
	if walkErr != nil {
		return walkErr
	}
	inv := 1.0 / codecpdf.PtPerMM
	m := canvas.Matrix{
		{matrix.a * inv, -matrix.c * inv, matrix.e + matrix.c*ref.Height},
		{-matrix.b * inv, matrix.d * inv, pageH - matrix.f - matrix.d*ref.Height},
	}
	pdfOut.content.DrawForm(entry.name, entry.ref, m, clip, alpha)
	return nil
}
