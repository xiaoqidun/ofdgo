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
	"image"

	"github.com/tdewolff/canvas"
)

// renderStamp 渲染印章
// 入参: ctx 画布上下文, s 印章对象, pageH 页面高度
func (r *Renderer) renderStamp(ctx *canvas.Context, s Stamp, pageH float64) {
	if r.textOnly {
		return
	}
	if s.Type == "ofd" && len(s.Data) > 0 {
		if s.Clip != nil {
			if img := r.renderOFDStampImage(s.Data); img != nil {
				r.renderStampImage(ctx, stampImageWithTransparentWhite(img), s, pageH)
			}
			return
		}
		reader, err := NewReader(bytes.NewReader(s.Data), int64(len(s.Data)))
		if err == nil {
			defer reader.Close()
			doc, err := reader.Doc()
			if err == nil {
				renderer := r.childRenderer(reader)
				for _, pageRef := range doc.Pages.Page {
					content, err := reader.PageContent(pageRef)
					if err != nil {
						continue
					}
					sealBox, err := renderer.GetPageBox(content)
					if err != nil {
						continue
					}
					ctx.Push()
					ctx.Translate(s.Box.X, pageH-(s.Box.Y+s.Box.H))
					ctx.Scale(s.Box.W/sealBox.W, s.Box.H/sealBox.H)
					err = renderer.renderCanvasPageToContext(ctx, content, false)
					ctx.Pop()
					if err != nil {
						r.renderError = err
						return
					}
				}
				return
			}
		}
	}
	if len(s.Data) > 0 {
		img, _, err := decodeImageData(s.Data)
		if err == nil {
			if r.decodeImages {
				img = imagePixelSource(img)
			}
			r.renderStampImage(ctx, stampImageWithTransparentWhite(img), s, pageH)
			return
		}
	}
}

// renderOFDStampImage 渲染OFD印章图像
// 入参: data OFD印章数据
// 返回: image.Image 印章图像
func (r *Renderer) renderOFDStampImage(data []byte) image.Image {
	reader, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	defer reader.Close()
	doc, err := reader.Doc()
	if err != nil {
		return nil
	}
	renderer := r.childRenderer(reader)
	renderer.decodeImages = true
	renderer.TransparentBackground = true
	for _, pageRef := range doc.Pages.Page {
		content, err := reader.PageContent(pageRef)
		if err != nil {
			continue
		}
		img, err := renderer.RenderToImage(content)
		if err != nil {
			r.renderError = err
			return nil
		}
		return img
	}
	return nil
}

// renderStampImage 渲染印章图像
// 入参: ctx 画布上下文, img 印章图像, s 印章对象, pageH 页面高度
func (r *Renderer) renderStampImage(ctx *canvas.Context, img image.Image, s Stamp, pageH float64) {
	box := s.Box
	if s.Clip != nil {
		img = clipStampImage(img, box, *s.Clip)
		box.X += s.Clip.X
		box.Y += s.Clip.Y
		box.W = s.Clip.W
		box.H = s.Clip.H
	}
	ctx.Push()
	ctx.Translate(box.X, pageH-(box.Y+box.H))
	ctx.Scale(box.W/float64(img.Bounds().Dx()), box.H/float64(img.Bounds().Dy()))
	ctx.DrawImage(0, 0, r.canvasEncodedImage(img), canvas.DPMM(1.0))
	ctx.Pop()
}
