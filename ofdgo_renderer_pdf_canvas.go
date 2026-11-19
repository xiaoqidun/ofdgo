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
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"maps"
	"strconv"
	"time"

	"github.com/tdewolff/canvas"
	canvasimage "github.com/tdewolff/canvas/image"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/xiaoqidun/j2kgo"
	"github.com/xiaoqidun/jbig2"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfRenderer PDF渲染器
type pdfRenderer struct {
	*pdf.PDF
	information    pdfgo.Dictionary
	glyphPaths     renderCache[*canvas.Path, *canvas.Path]
	fonts          map[*canvas.Font]*canvas.Font
	fontData       map[*canvas.Font][]byte
	cffFonts       map[*canvas.Font][]*pdfFontSubset
	cffGlyphMap    map[*canvas.Font]map[uint16]*pdfFontSubset
	images         [][]image.Image
	placeholders   [][]bool
	rawImages      [][]*codecpdf.RawImage
	rawCache       map[string]*pdfRawImage
	placeholderMap map[any]*image.RGBA
	placeholderSeq uint32
	imageClips     [][]string
	imageClip      *canvas.Path
	exactImages    bool
	imageError     error
	navigation     *pdfNavigation
	text           []codecpdf.TextPage
	textFauxBold   [][]codecpdf.TextFauxBold
	exactText      bool
}

// RenderPath 在绘制渐变前重置透明度图形状态，避免前驱半透明对象污染不透明渐变
// 入参: path 几何路径, style 绘制样式, matrix 变换矩阵
func (r *pdfRenderer) RenderPath(path *canvas.Path, style canvas.Style, matrix canvas.Matrix) {
	if style.Fill.IsGradient() || style.Stroke.IsGradient() {
		r.resetOpacityState()
	}
	r.PDF.RenderPath(path, style, matrix)
}

// RenderImage 校验惰性图片并同步不透明画笔，直通JPEG/JPX/JBIG2并对阶段2替换图像使用轻量占位图消除双次解码
// 入参: img 图像, matrix 图像变换
func (r *pdfRenderer) RenderImage(img image.Image, matrix canvas.Matrix) {
	if r.imageError != nil {
		return
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		r.imageError = fmt.Errorf("PDF output image pixel bounds differ")
		return
	}
	var rawPass *codecpdf.RawImage
	if rawImg, ok := img.(*pdfRawImage); ok && rawImg != nil && rawImg.raw != nil {
		rawPass = rawImg.raw
	}
	encoded, isEncoded := img.(*EncodedImage)
	target := img
	drawMatrix := matrix
	jpegPassThrough := false
	usePlaceholder := false
	var original image.Image
	if rawPass != nil {
		ph := r.placeholderImage(rawPass.Key)
		target = ph
		drawMatrix = matrix.Scale(float64(w)/float64(ph.Bounds().Dx()), float64(h)/float64(ph.Bounds().Dy()))
		usePlaceholder = true
	} else if isEncoded {
		if encoded.MIME() == "image/jpeg" {
			raw := encoded.Bytes()
			if validJPEGStream(raw) {
				cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
				if err != nil {
					r.imageError = err
					return
				}
				if image.Rect(0, 0, cfg.Width, cfg.Height) != bounds {
					r.imageError = fmt.Errorf("PDF output image pixel bounds differ")
					return
				}
				if parsed, jpegErr := canvasimage.NewJPEGImage(bytes.NewReader(raw)); jpegErr == nil {
					parsed.Bytes = raw
					if img.ColorModel() == cfg.ColorModel && (cfg.ColorModel == color.YCbCrModel || cfg.ColorModel == color.GrayModel) {
						target = parsed
						jpegPassThrough = true
					}
				}
			}
		}
		if !jpegPassThrough {
			if len(r.images) != 0 {
				original = img
				ph := r.placeholderImage(original)
				target = ph
				drawMatrix = matrix.Scale(float64(w)/float64(ph.Bounds().Dx()), float64(h)/float64(ph.Bounds().Dy()))
				usePlaceholder = true
			} else {
				transient, err := baseimage.DecodeTransientContext(context.Background(), encoded)
				if err != nil {
					r.imageError = err
					return
				}
				if transient == nil || transient.Bounds() != bounds {
					r.imageError = fmt.Errorf("PDF output image pixel bounds differ")
					return
				}
				target = transient
			}
		}
	} else if len(r.images) != 0 {
		opaque := false
		if source, ok := img.(interface{ Opaque() bool }); ok {
			opaque = source.Opaque()
		}
		switch img.ColorModel() {
		case color.GrayModel, color.CMYKModel:
		case color.RGBAModel, color.NRGBAModel, color.AlphaModel:
			if !opaque {
				original = img
			}
		default:
			original = img
		}
		if original != nil {
			ph := r.placeholderImage(original)
			target = ph
			drawMatrix = matrix.Scale(float64(w)/float64(ph.Bounds().Dx()), float64(h)/float64(ph.Bounds().Dy()))
			usePlaceholder = true
		}
	}
	if len(r.images) != 0 {
		r.exactImages = r.exactImages || original != nil
		last := len(r.images) - 1
		r.images[last] = append(r.images[last], original)
		if usePlaceholder {
			for len(r.placeholders) <= last {
				r.placeholders = append(r.placeholders, nil)
			}
			for len(r.placeholders[last]) < len(r.images[last]) {
				r.placeholders[last] = append(r.placeholders[last], false)
			}
			r.placeholders[last][len(r.images[last])-1] = true
		}
		if rawPass != nil {
			for len(r.rawImages) <= last {
				r.rawImages = append(r.rawImages, nil)
			}
			for len(r.rawImages[last]) < len(r.images[last]) {
				r.rawImages[last] = append(r.rawImages[last], nil)
			}
			r.rawImages[last][len(r.images[last])-1] = rawPass
		}
		if r.imageClip != nil {
			view := matrix.Scale(float64(w), float64(h))
			if view.Det() != 0 {
				for len(r.imageClips) <= last {
					r.imageClips = append(r.imageClips, nil)
				}
				for len(r.imageClips[last]) < len(r.images[last]) {
					r.imageClips[last] = append(r.imageClips[last], "")
				}
				r.imageClips[last][len(r.images[last])-1] = r.imageClip.Copy().Transform(view.Inv()).ToPDF()
			}
		}
	}
	r.resetOpacityState()
	r.PDF.RenderImage(target, drawMatrix)
}

// placeholderImage 为阶段2替换图像分配唯一轻量占位图，避免阶段1像素解码及后端XObject去重碰撞
// 入参: key 图像去重键
// 返回: *image.RGBA 轻量占位图
func (r *pdfRenderer) placeholderImage(key any) *image.RGBA {
	if key != nil && r.placeholderMap != nil {
		if existing, ok := r.placeholderMap[key]; ok {
			return existing
		}
	}
	r.placeholderSeq++
	seq := r.placeholderSeq
	var img *image.RGBA
	if seq < 1<<24 {
		img = image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = uint8(seq>>16), uint8(seq>>8), uint8(seq), 255
	} else {
		img = image.NewRGBA(image.Rect(0, 0, 2, 1))
		hi, lo := seq>>24, seq&0xFFFFFF
		img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = uint8(hi>>16), uint8(hi>>8), uint8(hi), 255
		img.Pix[4], img.Pix[5], img.Pix[6], img.Pix[7] = uint8(lo>>16), uint8(lo>>8), uint8(lo), 255
	}
	if key != nil {
		if r.placeholderMap == nil {
			r.placeholderMap = make(map[any]*image.RGBA)
		}
		r.placeholderMap[key] = img
	}
	return img
}

// resetOpacityState 重置PDF透明度图形状态，避免前驱半透明对象污染后续绘制
func (r *pdfRenderer) resetOpacityState() {
	reset := canvas.DefaultStyle
	reset.Fill = canvas.Paint{Color: canvas.White}
	reset.Stroke = canvas.Paint{Color: canvas.Black}
	r.PDF.RenderPath(&canvas.Path{}, reset, canvas.Identity)
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

// writePDF 传递后端资源记录及OFD导航修正，写出最终PDF
// 入参: ctx 取消上下文, data 后端PDF源, size 数据长度, writer 输出流, optimization 优化配置
// 返回: error 资源、压缩或写入错误
func (r *pdfRenderer) writePDF(ctx context.Context, data io.ReaderAt, size int64, writer io.Writer, optimization pdfgo.OptimizeOptions) error {
	if r.imageError != nil {
		return r.imageError
	}
	information := maps.Clone(r.information)
	if information == nil {
		information = make(pdfgo.Dictionary)
	}
	information["Producer"] = pdfgo.String(ofdCreator)
	information["CreationDate"] = pdfgo.String(time.Now().UTC().Format("D:20060102150405Z"))
	options := codecpdf.WriteOptions{
		Information:  information,
		Images:       r.images,
		Placeholders: r.placeholders,
		RawImages:    r.rawImages,
		ImageClips:   r.imageClips,
		Text:         r.text,
		TextFauxBold: r.textFauxBold,
		ExactImages:  r.exactImages,
		ExactText:    r.exactText,
		Optimization: optimization,
	}
	if r.navigation != nil && r.navigation.nativeWrite {
		options.Links = make(map[int][]*pdfgo.LinkRegion, len(r.navigation.Link))
		for page, links := range r.navigation.Link {
			regions := make([]*pdfgo.LinkRegion, len(links))
			for i, link := range links {
				regions[i] = link.Region
			}
			options.Links[page] = regions
		}
		options.PrepareRewrite = r.navigation.prepareRewrite
	}
	return codecpdf.WriteSource(ctx, data, size, writer, options)
}

// rawImageResource 在无蒙版且不透明时读取并缓存JPX或JBIG2原始流直通对象
// 入参: renderer 渲染器, obj 图像对象
// 返回: *pdfRawImage 可直通图像，不满足条件时为nil
func (r *pdfRenderer) rawImageResource(renderer *Renderer, obj ImageObject) *pdfRawImage {
	if renderer == nil || renderer.Reader == nil || obj.ImageMask != "" {
		return nil
	}
	if obj.Alpha != nil && clampColor(*obj.Alpha) < 255 {
		return nil
	}
	primary := obj.ResourceID
	if obj.Substitution != "" && !sameResourceID(obj.Substitution, obj.ResourceID) && (renderer.printing || renderer.DPI >= 300) {
		primary = obj.Substitution
	}
	resPath, ok := renderer.Reader.resolveResPathScoped(renderer.pageResources, primary)
	if !ok {
		return nil
	}
	clean := cleanPackagePath(renderer.Reader.ResPath(resPath))
	if r.rawCache != nil {
		if cached, found := r.rawCache[clean]; found {
			return cached
		}
	}
	ctx := renderer.pageContext
	if ctx == nil {
		ctx = renderer.outputContext()
	}
	data, err := renderer.Reader.readFileViewContext(ctx, clean)
	if err != nil {
		return nil
	}
	raw := tryPDFRawImage(ctx, data)
	if raw != nil {
		raw.raw.Key = raw
	}
	if r.rawCache == nil {
		r.rawCache = make(map[string]*pdfRawImage)
	}
	r.rawCache[clean] = raw
	return raw
}

// pdfRawImage 封装可直通写入PDF的JPX或JBIG2原始图像流
type pdfRawImage struct {
	raw *codecpdf.RawImage
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
		source:    renderer.Reader,
	}
	if doc != nil && (doc.Attachments.Path != "" || len(doc.Attachments.Attachment) != 0) {
		attachments, err := renderer.Reader.AttachmentsContext(ctx)
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

// addPage 收集当前页面及模板、注释中的导航，不保留页面图元
// 入参: renderer 渲染器, index 输出页索引, page 页面内容, pages 输出页面描述
// 返回: error 区域、目标或取消错误
func (n *pdfNavigation) addPage(renderer *Renderer, index int, page *PageContent, pages []RenderDocumentPage) error {
	ctx := renderer.outputContext()
	if err := ctx.Err(); err != nil {
		return err
	}
	if renderer.Reader != nil && renderer.Reader.doc == nil && renderer.Reader.OFD != nil {
		if _, err := renderer.Reader.docStructureContext(ctx); err != nil {
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
	if renderer.RenderAnnotations && renderer.Reader != nil {
		annotations, err := renderer.Reader.pageAnnotationsContext(ctx, page.ID)
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

// apply 生成当前页链接，内部目标在原生写入时关联
// 入参: renderer PDF渲染器, page 页面索引
func (n *pdfNavigation) apply(renderer *pdf.PDF, page int) {
	for _, link := range n.Link[page] {
		renderer.AddLink("", link.Rect)
	}
}

// prepareRewrite 关联输出页引用并准备附件、事件、链接及先序大纲的替换
// 入参: ctx 取消上下文, reader 输出PDF, pages 输出页引用, options 替换配置
// 返回: error 资源、目录结构或取消错误
func (n *pdfNavigation) prepareRewrite(ctx context.Context, reader *pdfgo.Reader, pages []pdfgo.Reference, options *pdfgo.RewriteOptions) error {
	if err := n.writeAttachments(ctx, options); err != nil {
		return err
	}
	if err := n.loadAudio(ctx); err != nil {
		return err
	}
	if err := n.writeVideo(ctx, pages, options); err != nil {
		return err
	}
	if len(n.Open) != 0 {
		options.OpenActions = pdfNavigationValues(n.Open, pages)
	}
	if len(n.PageOpen) != 0 {
		options.PageOpenActions = make(map[pdfgo.Reference][]pdfgo.NavigationAction, len(n.PageOpen))
		for page, actions := range n.PageOpen {
			options.PageOpenActions[pages[page]] = pdfNavigationValues(actions, pages)
		}
	}
	options.LinkDestinations = make(map[pdfgo.AnnotationLocation]pdfgo.Destination)
	options.LinkActions = make(map[pdfgo.AnnotationLocation][]pdfgo.NavigationAction)
	for page, links := range n.Link {
		for index, link := range links {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(link.Actions) == 1 && link.Actions[0].Target != nil {
				target := link.Actions[0].Target
				dest := target.Destination
				dest.Page = pages[target.Page]
				options.LinkDestinations[pdfgo.AnnotationLocation{Page: pages[page], Index: index}] = dest
			} else {
				options.LinkActions[pdfgo.AnnotationLocation{Page: pages[page], Index: index}] = pdfNavigationValues(link.Actions, pages)
			}
		}
	}
	if len(n.Outline) == 0 {
		return nil
	}
	options.OutlineDestinations = make(map[pdfgo.Reference]pdfgo.Destination, len(n.Outline))
	options.OutlineTitles = make(map[pdfgo.Reference]string, len(n.Outline))
	options.OutlineActions = make(map[pdfgo.Reference][]pdfgo.NavigationAction)
	count := 0
	if err := reader.WalkOutlines(ctx, func(level int, item pdfgo.OutlineItem) error {
		if count >= len(n.Outline) || n.Outline[count].Level != level || strconv.Itoa(count) != item.Title {
			return fmt.Errorf("PDF output outline differs")
		}
		actions := n.Outline[count].Actions
		if len(actions) == 1 && actions[0].Target != nil {
			target := actions[0].Target
			dest := target.Destination
			dest.Page = pages[target.Page]
			options.OutlineDestinations[item.Reference] = dest
		} else {
			options.OutlineActions[item.Reference] = pdfNavigationValues(actions, pages)
		}
		options.OutlineTitles[item.Reference] = n.Outline[count].Name
		count++
		return nil
	}); err != nil {
		return err
	}
	if count != len(n.Outline) {
		return fmt.Errorf("PDF output outline missing")
	}
	return nil
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
	dest = dest.effective()
	if _, err := dest.attributes(); err != nil {
		return pdfgo.Destination{}, err
	}
	left := dest.Left - box.X
	top := dest.Top - box.Y
	right := dest.Right - box.X
	bottom := dest.Bottom - box.Y
	const scale = 72.0 / 25.4
	result := pdfgo.Destination{Mode: pdfgo.Name(dest.Type)}
	switch dest.Type {
	case "XYZ":
		result.Parameters = pdfgo.Array{pdfgo.Real(left * scale), pdfgo.Real((box.H - top) * scale), pdfgo.Real(dest.Zoom)}
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

// tryPDFRawImage 检测不透明JP2/JPX与JBIG2原始流并构造零像素重编码直通对象
// 入参: ctx 取消上下文, data 原始图像字节
// 返回: *pdfRawImage 可直通图像，不满足条件时为nil
func tryPDFRawImage(ctx context.Context, data []byte) *pdfRawImage {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || len(data) == 0 {
		return nil
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
			switch len(info.Components) {
			case 1:
				raw.ColorSpace = "DeviceGray"
			case 3:
				raw.ColorSpace = "DeviceRGB"
			case 4:
				raw.ColorSpace = "DeviceCMYK"
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
	if len(data) < 9 || !isJBIG2File(data) {
		return nil, false
	}
	flags := data[8]
	if flags&1 == 0 {
		return nil, false
	}
	pos := 9
	if flags&2 == 0 {
		if len(data) < 13 {
			return nil, false
		}
		if binary.BigEndian.Uint32(data[9:13]) != 1 {
			return nil, false
		}
		pos = 13
	}
	var out bytes.Buffer
	out.Grow(len(data) - pos)
	pageInfos := 0
	for pos < len(data) {
		segStart := pos
		if pos+6 > len(data) {
			return nil, false
		}
		segNum := binary.BigEndian.Uint32(data[pos : pos+4])
		segFlags := data[pos+4]
		segType := segFlags & 0x3F
		pageAssocLong := segFlags&0x40 != 0
		pos += 5
		refByte := data[pos]
		refCount := int(refByte >> 5)
		if refCount <= 4 {
			pos++
		} else if refCount == 7 {
			if pos+4 > len(data) {
				return nil, false
			}
			refCount = int(binary.BigEndian.Uint32(data[pos:pos+4]) & 0x1FFFFFFF)
			retBytes := (refCount + 1 + 7) / 8
			pos += 4 + retBytes
		} else {
			return nil, false
		}
		refSize := 4
		if segNum <= 256 {
			refSize = 1
		} else if segNum <= 65536 {
			refSize = 2
		}
		pageAssocSize := 1
		if pageAssocLong {
			pageAssocSize = 4
		}
		if pos+refCount*refSize+pageAssocSize+4 > len(data) {
			return nil, false
		}
		pos += refCount*refSize + pageAssocSize
		dataLen := binary.BigEndian.Uint32(data[pos : pos+4])
		pos += 4
		if dataLen == 0xFFFFFFFF || uint64(pos)+uint64(dataLen) > uint64(len(data)) {
			return nil, false
		}
		pos += int(dataLen)
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
