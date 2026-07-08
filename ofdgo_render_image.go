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
	"image"
	"image/color"

	"github.com/tdewolff/canvas"
)

// renderImage 渲染图片
// 入参: ctx 画布上下文, obj 图片对象, pageH 页面高度, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM
func (r *Renderer) renderImage(ctx *canvas.Context, obj ImageObject, pageH float64, parentCTM *Matrix, boundaryInCTM bool) {
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	resPath, ok := r.Reader.ResMap[obj.ResourceID]
	if !ok {
		return
	}
	imgData, err := r.Reader.ResData(resPath)
	if err != nil {
		return
	}
	img, _, err := decodeImageData(imgData)
	if err != nil {
		return
	}
	box, _ := ParseBox(obj.Boundary)
	imgBounds := img.Bounds()
	imgW, imgH := float64(imgBounds.Dx()), float64(imgBounds.Dy())
	if imgW <= 0 || imgH <= 0 {
		return
	}
	img = imageWithAlpha(img, obj.Alpha)
	pad := 0
	img, pad = imageWithTransparentEdge(img)
	ctm := NewMatrix(obj.CTM)
	if obj.CTM == "" {
		ctm = Matrix{a: box.W, d: box.H}
	}
	var m canvas.Matrix
	if boundaryInCTM && parentCTM != nil {
		x0 := box.X + ctm.c + ctm.e
		y0 := box.Y + ctm.d + ctm.f
		m = canvas.Matrix{
			{(parentCTM.a*ctm.a + parentCTM.c*ctm.b) / imgW, -(parentCTM.a*ctm.c + parentCTM.c*ctm.d) / imgH, parentCTM.a*x0 + parentCTM.c*y0 + parentCTM.e},
			{-(parentCTM.b*ctm.a + parentCTM.d*ctm.b) / imgW, (parentCTM.b*ctm.c + parentCTM.d*ctm.d) / imgH, pageH - (parentCTM.b*x0 + parentCTM.d*y0 + parentCTM.f)},
		}
	} else {
		if parentCTM != nil {
			ctm = parentCTM.Multiply(ctm)
		}
		m = canvas.Matrix{
			{ctm.a / imgW, -ctm.c / imgH, box.X + ctm.c + ctm.e},
			{-ctm.b / imgW, ctm.d / imgH, pageH - box.Y - ctm.d - ctm.f},
		}
	}
	if pad > 0 {
		p := float64(pad)
		m[0][2] -= m[0][0]*p + m[0][1]*p
		m[1][2] -= m[1][0]*p + m[1][1]*p
	}
	ctx.RenderImage(img, ctx.CoordSystemView().Mul(ctx.View()).Mul(m))
}

// imageWithAlpha 合并图片透明度
// 入参: img 图片对象, alpha 对象透明度
// 返回: image.Image 合并后的图片对象
func imageWithAlpha(img image.Image, alpha *int) image.Image {
	if img == nil || alpha == nil {
		return img
	}
	a := clampColor(*alpha)
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			c.A = uint8(int(c.A) * a / 255)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

// imageWithTransparentEdge 补齐透明图片边缘颜色
// 入参: img 图片对象
// 返回: image.Image 补齐后的图片对象, int 补齐像素数
func imageWithTransparentEdge(img image.Image) (image.Image, int) {
	if img == nil {
		return img, 0
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return img, 0
	}
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	hasZero, hasVisible := false, false
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			src.SetNRGBA(x, y, c)
			if c.A == 0 {
				hasZero = true
			} else {
				hasVisible = true
			}
		}
	}
	if !hasZero || !hasVisible {
		return img, 0
	}
	out := image.NewNRGBA(image.Rect(0, 0, w+2, h+2))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.NRGBAAt(x, y)
			if c.A == 0 {
				if edge, ok := transparentEdgeColor(src, x, y); ok {
					c = edge
				}
			}
			out.SetNRGBA(x+1, y+1, c)
		}
	}
	for x := 0; x < w; x++ {
		out.SetNRGBA(x+1, 0, transparentPaddingColor(out.NRGBAAt(x+1, 1)))
		out.SetNRGBA(x+1, h+1, transparentPaddingColor(out.NRGBAAt(x+1, h)))
	}
	for y := 0; y < h; y++ {
		out.SetNRGBA(0, y+1, transparentPaddingColor(out.NRGBAAt(1, y+1)))
		out.SetNRGBA(w+1, y+1, transparentPaddingColor(out.NRGBAAt(w, y+1)))
	}
	out.SetNRGBA(0, 0, transparentPaddingColor(out.NRGBAAt(1, 1)))
	out.SetNRGBA(w+1, 0, transparentPaddingColor(out.NRGBAAt(w, 1)))
	out.SetNRGBA(0, h+1, transparentPaddingColor(out.NRGBAAt(1, h)))
	out.SetNRGBA(w+1, h+1, transparentPaddingColor(out.NRGBAAt(w, h)))
	return out, 1
}

// transparentEdgeColor 获取透明像素相邻的可见颜色
// 入参: img 图片对象, x X坐标, y Y坐标
// 返回: color.NRGBA 颜色, bool 是否存在
func transparentEdgeColor(img *image.NRGBA, x, y int) (color.NRGBA, bool) {
	bounds := img.Bounds()
	var best color.NRGBA
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx, ny := x+dx, y+dy
			if nx < bounds.Min.X || nx >= bounds.Max.X || ny < bounds.Min.Y || ny >= bounds.Max.Y {
				continue
			}
			c := img.NRGBAAt(nx, ny)
			if c.A > best.A {
				best = c
			}
		}
	}
	if best.A == 0 {
		return color.NRGBA{}, false
	}
	best.A = 1
	return best, true
}

// transparentPaddingColor 获取透明补齐颜色
// 入参: c 边缘颜色
// 返回: color.NRGBA 补齐颜色
func transparentPaddingColor(c color.NRGBA) color.NRGBA {
	c.A = 1
	return c
}
