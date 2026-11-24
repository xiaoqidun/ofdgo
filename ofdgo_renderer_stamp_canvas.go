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
	"image"
	"sync"

	"github.com/tdewolff/canvas"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// cachedStampOFD 保存按内容摘要共享的OFD印章解析结果
type cachedStampOFD struct {
	mu     sync.Mutex
	reader *Reader
	pages  []*PageContent
	boxes  []Box
}

// cachedStampSession 保存当前渲染器独占的OFD印章子渲染器与预解析页面
type cachedStampSession struct {
	renderer *Renderer
	pages    []*PageContent
	boxes    []Box
}

// syncStampRendererState 同步宿主渲染器的会话配置至印章子渲染器并返回恢复闭包
// 入参: dst 印章子渲染器, src 宿主渲染器, ctx 取消上下文
// 返回: func() 状态恢复闭包
func syncStampRendererState(dst, src *Renderer, ctx context.Context) func() {
	prevDPI := dst.dpi
	prevPrinting := dst.printing
	prevPrintContents := dst.printContents
	prevAnnotations := dst.renderAnnotations
	prevDecodeImages := dst.decodeImages
	prevCompression := dst.compression
	prevCompactStrokes := dst.compactStrokes
	prevSVGMerge := dst.svgPathMerge
	prevSVGPrecision := dst.svgPrecision
	prevSVGPrefix := dst.svgIDPrefix
	prevPageCtx := dst.pageContext
	prevRenderErr := dst.renderError
	dst.dpi = src.dpi
	dst.printing = src.printing
	dst.printContents = src.printContents
	dst.renderAnnotations = src.renderAnnotations
	dst.decodeImages = src.decodeImages
	dst.compression = src.compression
	dst.compactStrokes = src.compactStrokes
	dst.svgPathMerge = src.svgPathMerge
	dst.svgPrecision = src.svgPrecision
	dst.svgIDPrefix = src.svgIDPrefix
	dst.pageContext = ctx
	dst.renderError = nil
	return func() {
		dst.dpi = prevDPI
		dst.printing = prevPrinting
		dst.printContents = prevPrintContents
		dst.renderAnnotations = prevAnnotations
		dst.decodeImages = prevDecodeImages
		dst.compression = prevCompression
		dst.compactStrokes = prevCompactStrokes
		dst.svgPathMerge = prevSVGMerge
		dst.svgPrecision = prevSVGPrecision
		dst.svgIDPrefix = prevSVGPrefix
		dst.pageContext = prevPageCtx
		dst.renderError = prevRenderErr
	}
}

// renderStamp 渲染印章并按内容摘要跨页复用OFD及图像印章资源
// 入参: ctx 画布上下文, s 印章对象, pageH 页面高度, parentClip 父级裁剪路径
func (r *Renderer) renderStamp(ctx *canvas.Context, s Stamp, pageH float64, parentClip *canvas.Path) {
	if r.textOnly {
		return
	}
	pageContext := r.pageContext
	if pageContext == nil {
		pageContext = context.Background()
	}
	defer func() {
		if err := pageContext.Err(); err != nil {
			r.renderError = err
		}
	}()
	if err := pageContext.Err(); err != nil {
		r.renderError = err
		return
	}
	validBox := rasterPositive(s.Box.W) && rasterPositive(s.Box.H) && (s.Clip == nil || rasterPositive(s.Clip.W) && rasterPositive(s.Clip.H))
	if s.Type == "ofd" && len(s.Data) > 0 {
		session, err := r.loadStampOFD(pageContext, s.Data)
		if err != nil {
			if pageContext.Err() == nil {
				r.renderError = err
			}
			return
		}
		if !validBox {
			return
		}
		for i, content := range session.pages {
			sealBox := session.boxes[i]
			childClip := canvas.Rectangle(sealBox.W, sealBox.H)
			if parentClip != nil {
				parentInSeal := parentClip.Copy().Translate(-s.Box.X, -(pageH-s.Box.Y-s.Box.H)).Scale(sealBox.W/s.Box.W, sealBox.H/s.Box.H)
				childClip = session.renderer.intersectCachedClipPath(childClip, parentInSeal)
			}
			if s.Clip != nil {
				clipW := s.Clip.W * sealBox.W / s.Box.W
				clipH := s.Clip.H * sealBox.H / s.Box.H
				clipX := s.Clip.X * sealBox.W / s.Box.W
				clipY := sealBox.H - (s.Clip.Y+s.Clip.H)*sealBox.H/s.Box.H
				rect := canvas.Rectangle(clipW, clipH).Translate(clipX, clipY)
				childClip = session.renderer.intersectCachedClipPath(childClip, rect)
			}
			if childClip.Empty() {
				continue
			}
			pageRenderer := session.renderer
			restoreState := syncStampRendererState(pageRenderer, r, pageContext)
			if scale := max(s.Box.W/sealBox.W, s.Box.H/sealBox.H); scale > 1 {
				limit := 4096 * 25.4 / max(sealBox.W, sealBox.H)
				if pageRenderer.dpi < limit {
					pageRenderer.dpi = min(pageRenderer.dpi*scale, limit)
				}
			}
			ctx.Push()
			ctx.Translate(s.Box.X, pageH-(s.Box.Y+s.Box.H))
			ctx.Scale(s.Box.W/sealBox.W, s.Box.H/sealBox.H)
			err = pageRenderer.renderCanvasPageClipped(ctx, content, false, childClip)
			ctx.Pop()
			restoreState()
			if err != nil {
				r.renderError = err
				return
			}
		}
		return
	}
	if len(s.Data) > 0 {
		digest := sha256.Sum256(s.Data)
		cache := r.stampImageCache()
		img, err := cache.load(pageContext, digest, func() (image.Image, int, error) {
			cloned := bytes.Clone(s.Data)
			decoded, _, decodeErr := baseimage.DecodeContext(pageContext, cloned)
			if decodeErr != nil {
				return nil, 0, decodeErr
			}
			return wrapEncodedImage(decoded), baseimage.StorageCost(decoded, cache.limit), nil
		})
		if err != nil {
			r.renderError = err
			return
		}
		if validBox {
			r.renderStampImage(ctx, img, s, pageH, parentClip)
		}
	}
}

// loadStampOFD 按内容摘要缓存并复用OFD印章解析结果与当前渲染器独占子渲染器
// 入参: ctx 取消上下文, data 印章OFD字节
// 返回: *cachedStampSession 印章渲染会话, error 解析或派生错误
func (r *Renderer) loadStampOFD(ctx context.Context, data []byte) (*cachedStampSession, error) {
	digest := sha256.Sum256(data)
	state := r.canvasState()
	if session, ok := state.stampSessions[digest]; ok {
		return session, nil
	}
	cache := r.stampOFDCache()
	shared, err := cache.load(ctx, digest, func() (*cachedStampOFD, int, error) {
		cloned := bytes.Clone(data)
		var opts []ReaderOption
		if r.reader != nil && r.reader.resources != nil {
			opts = append(opts, WithResources(r.reader.resources))
		}
		reader, openErr := NewReader(&Input{reader: bytes.NewReader(cloned), size: int64(len(cloned))}, opts...)
		if openErr != nil {
			return nil, 0, openErr
		}
		doc, docErr := reader.docContext(ctx)
		if docErr != nil {
			_ = reader.Close()
			return nil, 0, docErr
		}
		probe := r.childRenderer(reader)
		pages := make([]*PageContent, 0, len(doc.Pages.Page))
		boxes := make([]Box, 0, len(doc.Pages.Page))
		for _, pageRef := range doc.Pages.Page {
			content, pageErr := reader.PageContentContext(ctx, pageRef)
			if pageErr != nil {
				_ = reader.Close()
				return nil, 0, pageErr
			}
			box, boxErr := probe.GetPageBox(content)
			if boxErr != nil {
				_ = reader.Close()
				return nil, 0, boxErr
			}
			pages = append(pages, content)
			boxes = append(boxes, box)
		}
		cost := min(len(cloned)+len(pages)*4096+1024, cache.limit)
		return &cachedStampOFD{reader: reader, pages: pages, boxes: boxes}, cost, nil
	})
	if err != nil {
		return nil, err
	}
	shared.mu.Lock()
	forkReader, forkErr := shared.reader.Fork(ctx)
	shared.mu.Unlock()
	if forkErr != nil {
		return nil, forkErr
	}
	child := r.syncChildRenderer(forkReader)
	child.transparentBackground = true
	session := &cachedStampSession{renderer: child, pages: shared.pages, boxes: shared.boxes}
	state.stampSessions[digest] = session
	return session, nil
}

// renderStampImage 渲染印章图像
// 入参: ctx 画布上下文, img 印章图像, s 印章对象, pageH 页面高度, parentClip 父级裁剪路径
func (r *Renderer) renderStampImage(ctx *canvas.Context, img image.Image, s Stamp, pageH float64, parentClip *canvas.Path) {
	pixels, err := baseimage.PixelDataContext(r.pageContext, img)
	if err != nil {
		r.renderError = err
		return
	}
	if r.decodeImages {
		img = pixels
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		return
	}
	box := s.Box
	clip := parentClip
	if s.Clip != nil {
		rect := canvas.Rectangle(s.Clip.W, s.Clip.H).Translate(box.X+s.Clip.X, pageH-box.Y-s.Clip.Y-s.Clip.H)
		clip = r.intersectCachedClipPath(clip, rect)
	}
	matrix := canvas.Identity.Translate(box.X, pageH-box.Y-box.H).Scale(box.W/float64(img.Bounds().Dx()), box.H/float64(img.Bounds().Dy()))
	r.drawClippedImage(ctx, img, clip, matrix)
}
