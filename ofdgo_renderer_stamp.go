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
	"image/draw"
	"math"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/rasterizer"
)

// renderStamp 渲染印章
// 入参: ctx 画布上下文, s 印章对象, pageH 页面高度
func (r *Renderer) renderStamp(ctx *canvas.Context, s Stamp, pageH float64) {
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
	for _, pageRef := range doc.Pages.Page {
		content, err := reader.PageContent(pageRef)
		if err != nil {
			continue
		}
		sealBox, err := renderer.GetPageBox(content)
		if err != nil {
			continue
		}
		c := canvas.New(sealBox.W, sealBox.H)
		if err := renderer.renderPageToContext(canvas.NewContext(c), content, false); err != nil {
			continue
		}
		return rasterizer.Draw(c, canvas.DPMM(r.DPI/25.4), canvas.DefaultColorSpace)
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
	ctx.DrawImage(0, 0, img, canvas.DPMM(1.0))
	ctx.Pop()
}

// clipStampImage 裁剪印章图像
// 入参: img 印章图像, box 印章区域, clip 裁剪区域
// 返回: image.Image 裁剪后的印章图像
func clipStampImage(img image.Image, box, clip Box) image.Image {
	bounds := img.Bounds()
	x0 := int(math.Floor(clip.X / box.W * float64(bounds.Dx())))
	y0 := int(math.Floor(clip.Y / box.H * float64(bounds.Dy())))
	x1 := int(math.Ceil((clip.X + clip.W) / box.W * float64(bounds.Dx())))
	y1 := int(math.Ceil((clip.Y + clip.H) / box.H * float64(bounds.Dy())))
	out := image.NewNRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	draw.Draw(out, out.Bounds(), img, image.Pt(bounds.Min.X+x0, bounds.Min.Y+y0), draw.Src)
	return out
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
