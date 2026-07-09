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
	"image/color"

	"github.com/tdewolff/canvas"
)

// renderStamp 渲染印章
// 入参: ctx 画布上下文, s 印章对象, pageH 页面高度
func (r *Renderer) renderStamp(ctx *canvas.Context, s Stamp, pageH float64) {
	x, y, w, h := s.Box.X, s.Box.Y, s.Box.W, s.Box.H
	screenY := pageH - (y + h)
	if s.Type == "ofd" && len(s.Data) > 0 {
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
					ctx.Translate(x, screenY)
					ctx.Scale(w/sealBox.W, h/sealBox.H)
					renderer.renderPageToContext(ctx, content, false)
					ctx.Pop()
				}
				return
			}
		}
	}
	if len(s.Data) > 0 {
		img, _, err := decodeImageData(s.Data)
		if err == nil {
			img = stampImageWithTransparentWhite(img)
			ctx.Push()
			ctx.Translate(x, screenY)
			ctx.Scale(w/float64(img.Bounds().Dx()), h/float64(img.Bounds().Dy()))
			ctx.DrawImage(0, 0, img, canvas.DPMM(1.0))
			ctx.Pop()
			return
		}
	}
}

// stampImageWithTransparentWhite 处理印章图片白色底色
// 入参: img 印章图片对象
// 返回: image.Image 处理后的印章图片对象
func stampImageWithTransparentWhite(img image.Image) image.Image {
	if opaque, ok := img.(interface{ Opaque() bool }); ok && !opaque.Opaque() {
		return img
	}
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	hasAlpha := false
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.A < 255 {
				hasAlpha = true
			}
			out.SetNRGBA(x, y, c)
		}
	}
	if hasAlpha {
		return img
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := out.NRGBAAt(x, y)
			if c.R >= 250 && c.G >= 250 && c.B >= 250 {
				c.A = 0
				out.SetNRGBA(x, y, c)
			}
		}
	}
	return out
}
