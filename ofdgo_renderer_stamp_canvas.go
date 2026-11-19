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
	"image"

	"github.com/tdewolff/canvas"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// renderStamp 渲染印章
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
		reader, err := NewReader(&Input{reader: bytes.NewReader(s.Data), size: int64(len(s.Data))})
		if err != nil {
			r.renderError = err
			return
		}
		defer reader.Close()
		doc, err := reader.DocContext(pageContext)
		if err != nil {
			r.renderError = err
			return
		}
		if !validBox {
			return
		}
		renderer := r.childRenderer(reader)
		renderer.TransparentBackground = true
		for _, pageRef := range doc.Pages.Page {
			content, err := reader.PageContentContext(pageContext, pageRef)
			if err != nil {
				if pageContext.Err() != nil {
					return
				}
				r.renderError = err
				return
			}
			sealBox, err := renderer.GetPageBox(content)
			if err != nil {
				r.renderError = err
				return
			}
			childClip := canvas.Rectangle(sealBox.W, sealBox.H)
			if parentClip != nil {
				parentInSeal := parentClip.Copy().Translate(-s.Box.X, -(pageH-s.Box.Y-s.Box.H)).Scale(sealBox.W/s.Box.W, sealBox.H/s.Box.H)
				childClip = renderer.intersectCachedClipPath(childClip, parentInSeal)
			}
			if s.Clip != nil {
				clipW := s.Clip.W * sealBox.W / s.Box.W
				clipH := s.Clip.H * sealBox.H / s.Box.H
				clipX := s.Clip.X * sealBox.W / s.Box.W
				clipY := sealBox.H - (s.Clip.Y+s.Clip.H)*sealBox.H/s.Box.H
				rect := canvas.Rectangle(clipW, clipH).Translate(clipX, clipY)
				childClip = renderer.intersectCachedClipPath(childClip, rect)
			}
			if childClip.Empty() {
				continue
			}
			pageRenderer := *renderer
			if scale := max(s.Box.W/sealBox.W, s.Box.H/sealBox.H); scale > 1 {
				limit := 4096 * 25.4 / max(sealBox.W, sealBox.H)
				if pageRenderer.DPI < limit {
					pageRenderer.DPI = min(pageRenderer.DPI*scale, limit)
				}
			}
			ctx.Push()
			ctx.Translate(s.Box.X, pageH-(s.Box.Y+s.Box.H))
			ctx.Scale(s.Box.W/sealBox.W, s.Box.H/sealBox.H)
			err = pageRenderer.renderCanvasPageClipped(ctx, content, false, childClip)
			ctx.Pop()
			if err != nil {
				r.renderError = err
				return
			}
		}
		return
	}
	if len(s.Data) > 0 {
		img, _, err := baseimage.DecodeContext(pageContext, s.Data)
		if err != nil {
			r.renderError = err
			return
		}
		if validBox {
			r.renderStampImage(ctx, img, s, pageH, parentClip)
		}
	}
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
