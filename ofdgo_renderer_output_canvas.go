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
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/ascii85"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/ps"
	"github.com/tdewolff/canvas/renderers/svg"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfPageBaseLocKey 按页内容包路径与物理尺寸复用相同静态页面
type pdfPageBaseLocKey struct {
	baseLoc string
	box     Box
}

// pdfSharedPageRef 记录可跨页直接复用的PDF内容流与资源字典引用
type pdfSharedPageRef struct {
	contentRef   pdfgo.Reference
	resourcesRef pdfgo.Reference
}

// pdfCompiledPage 保存单页并发渲染与预压缩后的轻量输出结果
type pdfCompiledPage struct {
	digest        [32]byte
	compressed    []byte
	resources     *codecpdf.ContentResources
	contentForNav *PageContent
	err           error
}

// pdfDeferredPage 保存因含跨页视频或延迟音频需在文档末尾写出的页面字典骨架
type pdfDeferredPage struct {
	index int
	dict  pdfgo.Dictionary
}

// canvasEPSWriter 在EPS头部写入本库制作软件，不改动绘图内容
type canvasEPSWriter struct {
	io.Writer
}

// epsImageRenderer 在EPS输出端优化图片，不修改页面及渲染缓存
type epsImageRenderer struct {
	*ps.PS
	writer  io.Writer
	options CompressionOptions
	ctx     context.Context
	err     error
}

// formatPDFTimeWithZone 按ISO 32000-1日期语法将含时区的时间格式化为PDF日期字符串
// 入参: t 含时区的时间
// 返回: string PDF日期字符串
func formatPDFTimeWithZone(t time.Time) string {
	_, offset := t.Zone()
	base := "D:" + t.Format("20060102150405")
	if offset == 0 {
		return base + "Z"
	}
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	hours := offset / 3600
	mins := (offset % 3600) / 60
	return fmt.Sprintf("%s%c%02d'%02d'", base, sign, hours, mins)
}

// formatPDFDate 将OFD或ISO日期文本规范化为PDF标准日期字符串
// 入参: raw 原始日期文本
// 返回: string PDF日期字符串, bool 是否解析成功
func formatPDFDate(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, "D:") {
		if _, err := pdfgo.ParseDate(s); err == nil {
			return s, true
		}
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return "D:" + t.Format("20060102"), true
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return formatPDFTimeWithZone(t), true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "20060102150405"} {
		if t, err := time.Parse(layout, s); err == nil {
			return "D:" + t.Format("20060102150405"), true
		}
	}
	if t, err := time.Parse("20060102", s); err == nil {
		return "D:" + t.Format("20060102"), true
	}
	if t, err := pdfgo.ParseDate(s); err == nil && t.Year() > 0 && t.Year() <= 9999 {
		return formatPDFTimeWithZone(t), true
	}
	return "", false
}

// buildPDFInfoDict 构造符合PDF标准的文档信息字典
// 入参: r 渲染器
// 返回: pdfgo.Dictionary 文档信息字典, error 文本编码错误
func buildPDFInfoDict(r *Renderer) (pdfgo.Dictionary, error) {
	var info DocInfo
	if r.reader != nil {
		if docInfo, err := r.reader.DocInfo(); err == nil && docInfo != nil {
			info = *docInfo
		}
	}
	creator := info.Creator
	if creator == "" {
		creator = ofdCreator
	}
	var kwParts []string
	for _, kw := range info.Keywords {
		if trimmed := strings.TrimSpace(kw); trimmed != "" {
			kwParts = append(kwParts, trimmed)
		}
	}
	keywords := strings.Join(kwParts, ", ")
	dict := make(pdfgo.Dictionary)
	for _, field := range []struct {
		key   pdfgo.Name
		value string
	}{{"Title", info.Title}, {"Subject", info.Subject}, {"Author", info.Author}, {"Keywords", keywords}, {"Creator", creator}} {
		if field.value == "" {
			continue
		}
		encoded, err := pdfgo.EncodeTextString(field.value)
		if err != nil {
			return nil, err
		}
		dict[field.key] = encoded
	}
	dict["Producer"] = pdfgo.String(ofdCreator)
	if created, ok := formatPDFDate(info.CreationDate); ok {
		dict["CreationDate"] = pdfgo.String(created)
	} else {
		dict["CreationDate"] = pdfgo.String(time.Now().UTC().Format("D:20060102150405Z"))
	}
	if modified, ok := formatPDFDate(info.ModDate); ok {
		dict["ModDate"] = pdfgo.String(modified)
	}
	return dict, nil
}

// compilePDFPage 在独立协程或主协程中完成单页内容读取、单遍内容流生成与Flate预压缩
// 入参: ctx 取消上下文, r 当前工作渲染器, session 共享PDF会话, page 页面描述, checkNav 是否检查页面导航, zw 复用zlib压缩器, zbuf 复用压缩缓冲
// 返回: pdfCompiledPage 编译结果, *zlib.Writer 复用压缩器
func compilePDFPage(ctx context.Context, r *Renderer, session *pdfStreamSession, page RenderDocumentPage, checkNav bool, zw *zlib.Writer, zbuf *bytes.Buffer) (pdfCompiledPage, *zlib.Writer) {
	content, err := page.ReadContent(ctx, r.reader)
	if err != nil {
		return pdfCompiledPage{err: fmt.Errorf("failed to read page: %w", err)}, zw
	}
	var resources *Resources
	if r.reader != nil {
		resources = r.reader.resources
	}
	pdfOut := newPDFPageRenderer(session, ctx, page.Box.W, page.Box.H, resources)
	prevCtx, prevErr := r.pageContext, r.renderError
	r.pageContext, r.renderError = context.WithValue(ctx, pdfRendererCtxKey{}, pdfOut), nil
	renderErr := r.renderCanvasPageToContext(canvas.NewContext(pdfOut), content, !r.transparentBackground)
	r.pageContext, r.renderError = prevCtx, prevErr
	if renderErr != nil {
		return pdfCompiledPage{err: renderErr}, zw
	}
	if pdfOut.imageError != nil {
		return pdfCompiledPage{err: pdfOut.imageError}, zw
	}
	raw := pdfOut.content.Bytes()
	digest := sha256.Sum256(raw)
	compressed, zw, err := codecpdf.CompressBytes(zw, zbuf, session.compressLevel, raw)
	if err != nil {
		return pdfCompiledPage{err: err}, zw
	}
	var navContent *PageContent
	if checkNav && pageNeedsPDFNavigation(r, content) {
		navContent = content
	}
	return pdfCompiledPage{
		digest:        digest,
		compressed:    compressed,
		resources:     pdfOut.content.Resources(),
		contentForNav: navContent,
	}, zw
}

// commitCompiledPDFPage 按内容摘要与资源签名去重并流式写出单页内容流与资源字典
// 入参: sw 流式PDF写出器, session 共享PDF会话, res 已编译页面数据
// 返回: pdfgo.Reference 内容流引用, pdfgo.Reference 资源字典引用, error 写入错误
func commitCompiledPDFPage(sw *codecpdf.StreamWriter, session *pdfStreamSession, res pdfCompiledPage) (pdfgo.Reference, pdfgo.Reference, error) {
	contentRef, exists := session.contentRefs[res.digest]
	if exists {
		session.contentHits.Add(1)
	} else {
		contentRef = sw.AllocRef()
		if err := sw.WritePreencodedStream(contentRef, pdfgo.Dictionary{"Filter": pdfgo.Name("FlateDecode")}, res.compressed); err != nil {
			return pdfgo.Reference{}, pdfgo.Reference{}, err
		}
		session.contentRefs[res.digest] = contentRef
	}
	resRef, err := session.graphics.ResourceDictRef(sw, res.resources)
	if err != nil {
		return pdfgo.Reference{}, pdfgo.Reference{}, err
	}
	return contentRef, resRef, nil
}

// pageNeedsPDFNavigation 快速判断页面是否存在动作、带动作模板或页面注释，避免无导航页滞留内存
// 入参: r 渲染器, page 页面内容
// 返回: bool 是否需要执行导航收集
func pageNeedsPDFNavigation(r *Renderer, page *PageContent) bool {
	if page == nil {
		return false
	}
	if len(page.Actions) > 0 {
		return true
	}
	if len(r.pageActionSources(page)) > 0 {
		return true
	}
	if len(r.pageOpenActionSources(page)) > 0 {
		return true
	}
	if r.renderAnnotations && r.reader != nil {
		ctx := r.pageContext
		if ctx == nil {
			ctx = r.outputContext()
		}
		if r.reader.doc != nil || r.reader.ofd != nil {
			if err := r.reader.ensureAnnotationIndexContext(ctx); err != nil {
				return true
			}
		}
		if readerMayHavePageAnnotations(r.reader, page.ID) {
			if annots, err := r.reader.pageAnnotationsContext(ctx, page.ID); err == nil && len(annots) > 0 {
				return true
			}
		}
	}
	return false
}

// canReusePDFBaseLoc 判断页面是否不含专属注解与签章，允许按BaseLoc直接跳过读取与渲染
// 入参: r 渲染器, page 页面描述
// 返回: bool 是否允许BaseLoc快速复用
func canReusePDFBaseLoc(r *Renderer, page RenderDocumentPage) bool {
	if r.OnPageText != nil || page.Source.BaseLoc == "" || page.Content != nil || r.reader == nil {
		return false
	}
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	pageID := page.Source.ID
	if r.renderAnnotations {
		if r.reader.doc != nil || r.reader.ofd != nil {
			if err := r.reader.ensureAnnotationIndexContext(ctx); err != nil {
				return false
			}
		}
		if readerMayHavePageAnnotations(r.reader, pageID) {
			if annots, err := r.reader.pageAnnotationsContext(ctx, pageID); err != nil || len(annots) > 0 {
				return false
			}
		}
	}
	if err := r.reader.ensureSignatureIndexContext(ctx); err != nil {
		return false
	}
	if readerMayHavePageStamps(r.reader, pageID) {
		if err := r.reader.ensurePageStampsContext(ctx, pageID); err != nil {
			return false
		}
		return len(r.reader.stamps[r.reader.resolveStampPageID(pageID)]) == 0
	}
	return true
}

// RenderSVG 使用canvas输出SVG，按模式保留资源和对象分组
// 入参: ctx 取消上下文, r 渲染器, page 页面内容, writer 输出流, mode 资源模式
// 返回: SVGResources 外部资源, error 错误信息
func (DefaultBackend) RenderSVG(ctx context.Context, r *Renderer, page *PageContent, writer io.Writer, mode SVGMode) (SVGResources, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SVGResources{}, err
	}
	prevCtx := r.pageContext
	r.pageContext = ctx
	defer func() { r.pageContext = prevCtx }()
	switch mode {
	case SVGExternalFonts:
		return r.renderSVGResources(page, writer, false, false)
	case SVGExternalResources:
		return r.renderSVGResources(page, writer, true, false)
	case SVGObjects:
		return r.renderSVGResources(page, writer, true, true)
	case SVGEmbedded:
		box, err := r.GetPageBoxContext(ctx, page)
		if err != nil {
			return SVGResources{}, err
		}
		buffer := bufio.NewWriter(writer)
		renderer := &svgResourceRenderer{SVG: svg.New(buffer, box.W, box.H, nil), renderer: r, writer: buffer, seen: make(map[string]bool), embeddedFonts: true, pathMerge: r.SVGPathMerge(), precision: r.svgPrecision}
		if _, err := io.WriteString(buffer, svgCreatorMetadata); err != nil {
			return SVGResources{}, err
		}
		if _, err := io.WriteString(buffer, `<g style="image-orientation:none">`); err != nil {
			return SVGResources{}, err
		}
		if err := r.renderCanvasPageToContext(canvas.NewContext(renderer), page, !r.transparentBackground); err != nil {
			return SVGResources{}, err
		}
		renderer.flushPendingPath()
		if renderer.err != nil {
			return SVGResources{}, renderer.err
		}
		if _, err := io.WriteString(buffer, `</g>`); err != nil {
			return SVGResources{}, err
		}
		if err := renderer.Close(); err != nil {
			return SVGResources{}, err
		}
		return SVGResources{}, buffer.Flush()
	default:
		return SVGResources{}, fmt.Errorf("unsupported SVG mode %d", mode)
	}
}

// RenderEPS 使用canvas输出单页EPS
// 入参: ctx 取消上下文, r 渲染器, page 页面内容, writer 输出流
// 返回: error 错误信息
func (DefaultBackend) RenderEPS(ctx context.Context, r *Renderer, page *PageContent, writer io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prevCtx := r.pageContext
	r.pageContext = ctx
	defer func() { r.pageContext = prevCtx }()
	c, err := r.renderCanvasPage(page)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	options := ps.DefaultOptions
	options.Format = ps.EncapsulatedPostScript
	buffer := bufio.NewWriter(writer)
	renderer := ps.New(canvasEPSWriter{buffer}, c.W, c.H, &options)
	if r.compression.Mode == CompressionUnchanged {
		c.RenderTo(renderer)
	} else {
		optimized := &epsImageRenderer{PS: renderer, writer: buffer, options: r.compression, ctx: ctx}
		c.RenderTo(optimized)
		if optimized.err != nil {
			return optimized.err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := renderer.Close(); err != nil {
		return err
	}
	return buffer.Flush()
}

// RenderPDF 使用原生流式PDF写出器单遍多核并发导出PDF，支持Form复用、内容流去重与恒定内存写出
// 入参: ctx 取消上下文, r 渲染器, pages 页面列表, writer 输出流, progress 页面处理进度
// 返回: error 错误信息
func (DefaultBackend) RenderPDF(ctx context.Context, r *Renderer, pages []RenderDocumentPage, writer io.Writer, progress func(int, int) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pages) == 0 {
		return fmt.Errorf("no pages found")
	}
	for i, page := range pages {
		if !rasterPositive(page.Box.W) || !rasterPositive(page.Box.H) {
			return fmt.Errorf("invalid PDF page %d size: %g x %g", i+1, page.Box.W, page.Box.H)
		}
	}
	prevCtx, prevCompact := r.pageContext, r.compactStrokes
	r.pageContext, r.compactStrokes = ctx, true
	defer func() {
		r.pageContext, r.compactStrokes = prevCtx, prevCompact
	}()
	doc := r.reader.doc
	navigation := &pdfNavigation{}
	if !r.printing {
		var err error
		navigation, err = newPDFNavigation(r, doc, pages)
		if err != nil {
			return err
		}
	}
	compressLevel := zlib.DefaultCompression
	if r.compression.Mode != CompressionUnchanged {
		compressLevel = zlib.BestCompression
	}
	sw, err := codecpdf.NewStreamWriter(ctx, writer, len(pages), true, compressLevel)
	if err != nil {
		return err
	}
	session := newPDFStreamSession(sw, r.compression)
	navWriter := codecpdf.NewNavWriter(sw)
	pageGroupRef := sw.AllocRef()
	if err := sw.WriteObject(pageGroupRef, pdfgo.Dictionary{
		"Type": pdfgo.Name("Group"),
		"S":    pdfgo.Name("Transparency"),
		"I":    pdfgo.Boolean(true),
		"CS":   pdfgo.Name("DeviceRGB"),
	}); err != nil {
		return err
	}
	info, err := buildPDFInfoDict(r)
	if err != nil {
		return err
	}
	workers := 1
	if len(pages) > 2 && r.OnPageText == nil && r.reader != nil {
		w := conversionWorkers(r.reader.resources.context(ctx))
		if w > 1 {
			workers = min(len(pages)-1, min(w, 8))
		}
	}
	baseLocCache := make(map[pdfPageBaseLocKey]pdfSharedPageRef)
	var deferredPages []pdfDeferredPage
	pageRefs := sw.PageRefs()
	writeSinglePage := func(i int, contentRef, resRef pdfgo.Reference, contentForNav *PageContent) error {
		if err := session.flushPending(); err != nil {
			return err
		}
		if !r.printing && contentForNav != nil {
			if err := navigation.addPage(r, i, contentForNav, pages); err != nil {
				return fmt.Errorf("failed to read page %d navigation: %w", i+1, err)
			}
		}
		pageDict := pdfgo.Dictionary{
			"Type":      pdfgo.Name("Page"),
			"Parent":    sw.PageParent(i),
			"MediaBox":  pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Real(pages[i].Box.W * codecpdf.PtPerMM), pdfgo.Real(pages[i].Box.H * codecpdf.PtPerMM)},
			"Resources": resRef,
			"Group":     pageGroupRef,
			"Contents":  contentRef,
		}
		deferThis := !r.printing && (i == 0 || navigation.hasDeferredMedia())
		if deferThis {
			deferredPages = append(deferredPages, pdfDeferredPage{index: i, dict: pageDict})
		} else {
			if !r.printing {
				if links := navigation.pageNavLinks(i, pageRefs); len(links) > 0 {
					annots, err := navWriter.WritePageLinks(ctx, links)
					if err != nil {
						return err
					}
					if len(annots) > 0 {
						pageDict["Annots"] = annots
					}
					delete(navigation.Link, i)
				}
				if actions := navigation.PageOpen[i]; len(actions) > 0 {
					act, err := navWriter.BuildActionChain(ctx, pdfNavigationValues(actions, pageRefs))
					if err != nil {
						return err
					}
					if act != nil {
						pageDict["AA"] = pdfgo.Dictionary{"O": act}
					}
					delete(navigation.PageOpen, i)
				}
			}
			if err := sw.WriteObject(pageRefs[i], pageDict); err != nil {
				return err
			}
		}
		pages[i].Content = nil
		if err := ctx.Err(); err != nil {
			return err
		}
		if progress != nil {
			if err := progress(i+1, len(pages)); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	var zw *zlib.Writer
	var zbuf bytes.Buffer
	firstRes, nextZW := compilePDFPage(ctx, r, session, pages[0], !r.printing, zw, &zbuf)
	zw = nextZW
	if firstRes.err != nil {
		return fmt.Errorf("failed to render page 1: %w", firstRes.err)
	}
	firstContentRef, firstResRef, err := commitCompiledPDFPage(sw, session, firstRes)
	if err != nil {
		return err
	}
	if firstRes.contentForNav == nil && pages[0].Source.BaseLoc != "" && canReusePDFBaseLoc(r, pages[0]) {
		baseLocCache[pdfPageBaseLocKey{baseLoc: pages[0].Source.BaseLoc, box: pages[0].Box}] = pdfSharedPageRef{contentRef: firstContentRef, resourcesRef: firstResRef}
	}
	if err := writeSinglePage(0, firstContentRef, firstResRef, firstRes.contentForNav); err != nil {
		return err
	}
	var forkedRenderers []*Renderer
	var cleanupForks []*Renderer
	defer func() {
		for _, forked := range cleanupForks {
			if forked != nil && forked.reader != nil {
				_ = forked.reader.Close()
			}
		}
	}()
	if workers > 1 && len(pages) > 1 {
		workerCtx, cancelWorkers := context.WithCancel(ctx)
		defer cancelWorkers()
		forkedRenderers = make([]*Renderer, 0, workers)
		for range workers {
			forked, forkErr := r.Fork(workerCtx)
			if forkErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				forkedRenderers = nil
				break
			}
			forked.compactStrokes = true
			forkedRenderers = append(forkedRenderers, forked)
			cleanupForks = append(cleanupForks, forked)
		}
		if len(forkedRenderers) == 0 {
			workers = 1
		}
	}
	if len(pages) > 1 && workers == 1 {
		for i := 1; i < len(pages); i++ {
			page := pages[i]
			locKey := pdfPageBaseLocKey{baseLoc: page.Source.BaseLoc, box: page.Box}
			if canReusePDFBaseLoc(r, page) {
				if shared, ok := baseLocCache[locKey]; ok {
					session.contentHits.Add(1)
					if err := writeSinglePage(i, shared.contentRef, shared.resourcesRef, nil); err != nil {
						return err
					}
					continue
				}
			}
			res, nextZW := compilePDFPage(ctx, r, session, page, !r.printing, zw, &zbuf)
			zw = nextZW
			if res.err != nil {
				return fmt.Errorf("failed to render page %d: %w", i+1, res.err)
			}
			contentRef, resRef, err := commitCompiledPDFPage(sw, session, res)
			if err != nil {
				return err
			}
			if res.contentForNav == nil && page.Source.BaseLoc != "" && canReusePDFBaseLoc(r, page) {
				baseLocCache[locKey] = pdfSharedPageRef{contentRef: contentRef, resourcesRef: resRef}
			}
			if err := writeSinglePage(i, contentRef, resRef, res.contentForNav); err != nil {
				return err
			}
		}
	} else if len(pages) > 1 {
		workerCtx, cancelWorkers := context.WithCancel(ctx)
		remaining := len(pages) - 1
		window := min(remaining, len(forkedRenderers)*2)
		jobs := make(chan int, window)
		slots := make([]chan pdfCompiledPage, window)
		for k := range window {
			slots[k] = make(chan pdfCompiledPage, 1)
		}
		var wg sync.WaitGroup
		for _, forked := range forkedRenderers {
			wg.Add(1)
			go func(workerRenderer *Renderer) {
				defer wg.Done()
				var workerZW *zlib.Writer
				var workerZBuf bytes.Buffer
				for idx := range jobs {
					if workerCtx.Err() != nil {
						slots[(idx-1)%window] <- pdfCompiledPage{err: workerCtx.Err()}
						continue
					}
					res, nextZW := compilePDFPage(workerCtx, workerRenderer, session, pages[idx], !r.printing, workerZW, &workerZBuf)
					workerZW = nextZW
					slots[(idx-1)%window] <- res
				}
			}(forked)
		}
		nextSend := 1
		for nextSend <= window {
			jobs <- nextSend
			nextSend++
		}
		var closeJobsOnce sync.Once
		closeJobs := func() {
			closeJobsOnce.Do(func() {
				close(jobs)
			})
		}
		stopWorkers := func() {
			cancelWorkers()
			closeJobs()
			wg.Wait()
		}
		var loopErr error
		for i := 1; i < len(pages); i++ {
			res := <-slots[(i-1)%window]
			if res.err != nil {
				loopErr = fmt.Errorf("failed to render page %d: %w", i+1, res.err)
			} else {
				if nextSend < len(pages) {
					jobs <- nextSend
					nextSend++
				}
				contentRef, resRef, err := commitCompiledPDFPage(sw, session, res)
				if err != nil {
					loopErr = err
				} else if err := writeSinglePage(i, contentRef, resRef, res.contentForNav); err != nil {
					loopErr = err
				}
			}
			if loopErr != nil {
				cancelWorkers()
				closeJobs()
				for k := i + 1; k < nextSend; k++ {
					<-slots[(k-1)%window]
				}
				break
			}
		}
		stopWorkers()
		if loopErr != nil {
			return loopErr
		}
	}
	if err := session.flushPending(); err != nil {
		return err
	}
	fontWorkers := 1
	if r.reader != nil {
		fontWorkers = max(1, conversionWorkers(r.reader.resources.context(ctx)))
	}
	if err := session.fonts.WriteAll(ctx, sw, fontWorkers); err != nil {
		return err
	}
	catalog := make(pdfgo.Dictionary)
	if !r.printing {
		if doc != nil {
			if err := navigation.addOutlines(doc.Outlines.OutlineElem, 0, navigation.bookmarks, navigation.pageIndex, pages); err != nil {
				return err
			}
		}
		var navOpts pdfgo.RewriteOptions
		if err := navigation.writeAttachments(ctx, &navOpts); err != nil {
			return err
		}
		if err := navigation.loadAudio(ctx); err != nil {
			return err
		}
		if err := navigation.writeVideo(ctx, pageRefs, &navOpts); err != nil {
			return err
		}
		if len(navOpts.Attachments) > 0 {
			efTree, err := navWriter.WriteAttachments(ctx, navOpts.Attachments)
			if err != nil {
				return err
			}
			if len(efTree) > 0 {
				catalog["Names"] = pdfgo.Dictionary{"EmbeddedFiles": efTree}
			}
		}
		screensByPage := make(map[int][]*pdfgo.ScreenAnnotation)
		if len(navOpts.ScreenAnnotations) > 0 {
			refToIdx := make(map[pdfgo.Reference]int, len(pageRefs))
			for idx, ref := range pageRefs {
				refToIdx[ref] = idx
			}
			for ref, list := range navOpts.ScreenAnnotations {
				if idx, ok := refToIdx[ref]; ok {
					screensByPage[idx] = list
				}
			}
			navWriter.ReserveScreenAnnotations(screensByPage)
		}
		for _, dp := range deferredPages {
			var annots pdfgo.Array
			if links := navigation.pageNavLinks(dp.index, pageRefs); len(links) > 0 {
				linkRefs, err := navWriter.WritePageLinks(ctx, links)
				if err != nil {
					return err
				}
				annots = append(annots, linkRefs...)
			}
			if screens := screensByPage[dp.index]; len(screens) > 0 {
				screenRefs, err := navWriter.WriteScreenAnnotations(ctx, pageRefs[dp.index], screens)
				if err != nil {
					return err
				}
				annots = append(annots, screenRefs...)
			}
			if len(annots) > 0 {
				dp.dict["Annots"] = annots
			}
			if actions := navigation.PageOpen[dp.index]; len(actions) > 0 {
				act, err := navWriter.BuildActionChain(ctx, pdfNavigationValues(actions, pageRefs))
				if err != nil {
					return err
				}
				if act != nil {
					dp.dict["AA"] = pdfgo.Dictionary{"O": act}
				}
			}
			if err := sw.WriteObject(pageRefs[dp.index], dp.dict); err != nil {
				return err
			}
		}
		if len(navigation.Outline) > 0 {
			outlines := make([]codecpdf.NavOutline, len(navigation.Outline))
			for i, item := range navigation.Outline {
				outlines[i] = codecpdf.NavOutline{
					Title:   item.Name,
					Level:   item.Level,
					Actions: pdfNavigationValues(item.Actions, pageRefs),
				}
			}
			outlineRef, ok, err := navWriter.WriteOutlines(ctx, outlines)
			if err != nil {
				return err
			}
			if ok {
				catalog["Outlines"] = outlineRef
			}
		}
		if len(navigation.Open) > 0 {
			act, err := navWriter.BuildActionChain(ctx, pdfNavigationValues(navigation.Open, pageRefs))
			if err != nil {
				return err
			}
			if act != nil {
				catalog["OpenAction"] = act
			}
		}
	}
	return sw.Finish(catalog, info)
}

// Write 写入EPS片段并替换独立的制作软件声明
// 入参: data EPS片段
// 返回: int 已处理字节数, error 写入错误
func (w canvasEPSWriter) Write(data []byte) (int, error) {
	if bytes.Equal(data, []byte("%%Creator: tdewolff/canvas\n")) {
		if _, err := io.WriteString(w.Writer, "%%Creator: "+ofdCreator+"\n"); err != nil {
			return 0, err
		}
		return len(data), nil
	}
	return w.Writer.Write(data)
}

// RenderImage 写出Flate或更小的JPEG图片，保留显示尺寸与变换
// 入参: img 图片, matrix 绘制变换
func (r *epsImageRenderer) RenderImage(img image.Image, matrix canvas.Matrix) {
	if r.err != nil {
		return
	}
	if dpi := r.options.ImageDPI(); dpi > 0 {
		original := img.Bounds().Size()
		target := compressionImageSize(math.Hypot(matrix[0][0], matrix[1][0])*float64(original.X), math.Hypot(matrix[0][1], matrix[1][1])*float64(original.Y), dpi)
		img, r.err = pdfgo.ResizeImage(r.ctx, img, target)
		if r.err != nil {
			return
		}
		size := img.Bounds().Size()
		if size.X > 0 && size.Y > 0 {
			matrix = matrix.Scale(float64(original.X)/float64(size.X), float64(original.Y)/float64(size.Y))
		}
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	pixels := make([]byte, width*3)
	var encoded bytes.Buffer
	compressor, _ := zlib.NewWriterLevel(&encoded, zlib.BestCompression)
	opaque := true
	native, _ := img.(image.RGBA64Image)
	for y := 0; y < height; y++ {
		if r.err = r.ctx.Err(); r.err != nil {
			return
		}
		clear(pixels)
		for x := 0; x < width; x++ {
			if x&4095 == 0 {
				if r.err = r.ctx.Err(); r.err != nil {
					return
				}
			}
			var red, green, blue, alpha uint32
			if native != nil {
				pixel := native.RGBA64At(bounds.Min.X+x, bounds.Min.Y+y)
				red, green, blue, alpha = uint32(pixel.R), uint32(pixel.G), uint32(pixel.B), uint32(pixel.A)
			} else {
				red, green, blue, alpha = img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			}
			if alpha != 65535 {
				opaque = false
			}
			if alpha == 65535 {
				offset := x * 3
				pixels[offset], pixels[offset+1], pixels[offset+2] = byte(red>>8), byte(green>>8), byte(blue>>8)
			} else if alpha != 0 {
				offset := x * 3
				pixels[offset], pixels[offset+1], pixels[offset+2] = byte(red*65535/alpha>>8), byte(green*65535/alpha>>8), byte(blue*65535/alpha>>8)
			}
		}
		if r.err = r.ctx.Err(); r.err != nil {
			return
		}
		if _, r.err = compressor.Write(pixels); r.err != nil {
			return
		}
	}
	if r.err = compressor.Close(); r.err != nil {
		return
	}
	data := encoded.Bytes()
	filter := "FlateDecode"
	if opaque && r.options.Mode == CompressionLossy {
		var candidate bytes.Buffer
		if r.err = jpeg.Encode(&candidate, img, &jpeg.Options{Quality: r.options.ImageQuality()}); r.err != nil {
			return
		}
		optimized, err := pdfgo.OptimizeJPEG(r.ctx, candidate.Bytes())
		if err != nil {
			r.err = err
			return
		}
		if len(optimized) < len(data) {
			data, filter = optimized, "DCTDecode"
		}
	}
	if r.err = r.ctx.Err(); r.err != nil {
		return
	}
	interpolate := true
	if width <= 8 && height <= 8 && math.Hypot(matrix[0][0], matrix[1][0])*codecpdf.PtPerMM >= 4 && math.Hypot(matrix[0][1], matrix[1][1])*codecpdf.PtPerMM >= 4 {
		interpolate = false
	}
	m := matrix.Scale(float64(width), float64(height))
	_, r.err = fmt.Fprintf(r.writer, " gsave /DeviceRGB setcolorspace [%s %s %s %s %s %s] concat <</ImageType 1 /BitsPerComponent 8 /Decode [0 1 0 1 0 1] /Interpolate %t /Width %d /Height %d /ImageMatrix [%d 0 0 -%d 0 %d] /DataSource currentfile /ASCII85Decode filter /%s filter>>image\n", ofdNumber(m[0][0]), ofdNumber(m[1][0]), ofdNumber(m[0][1]), ofdNumber(m[1][1]), ofdNumber(m[0][2]), ofdNumber(m[1][2]), interpolate, width, height, width, height, height, filter)
	if r.err != nil {
		return
	}
	writer := ascii85.NewEncoder(r.writer)
	if _, r.err = writer.Write(data); r.err != nil {
		return
	}
	if r.err = writer.Close(); r.err != nil {
		return
	}
	_, r.err = io.WriteString(r.writer, "~>\n grestore")
}
