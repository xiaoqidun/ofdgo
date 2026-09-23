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
	"fmt"
	"image"
	"math"

	"github.com/tdewolff/canvas"
	canvasimage "github.com/tdewolff/canvas/image"
	"golang.org/x/image/draw"
)

// renderImage 渲染图片
// 入参: ctx 画布上下文, obj 图片对象, pageH 页面高度, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM, parentClip 父级裁剪路径
func (r *Renderer) renderImage(ctx *canvas.Context, obj ImageObject, pageH float64, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if r.textOnly {
		return
	}
	if _, ok := ctx.Renderer.(*boundsRenderer); ok {
		r.measureImage(ctx, obj, pageH, parentCTM, boundaryInCTM, parentClip)
		return
	}
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	resPath, ok := r.Reader.ResMap[obj.ResourceID]
	if !ok {
		return
	}
	img, err := r.decodeImageResource(resPath)
	if err != nil {
		return
	}
	box, _ := ParseBox(obj.Boundary)
	if maskPath, ok := r.Reader.ResMap[obj.ImageMask]; ok {
		if mask, err := r.decodeImageResource(maskPath); err == nil {
			img = imageWithMask(img, mask)
		}
	}
	img = imageWithAlpha(img, obj.Alpha)
	imgBounds := img.Bounds()
	imgW, imgH := float64(imgBounds.Dx()), float64(imgBounds.Dy())
	if imgW <= 0 || imgH <= 0 {
		return
	}
	ctm := NewMatrix(obj.CTM)
	if obj.CTM == "" {
		ctm = Matrix{a: box.W, d: box.H}
	}
	localCTM := ctm
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
	clipPath := intersectClipPath(parentClip, r.buildObjectClipPath(obj.Clips, pageH, box.X, box.Y, localCTM, parentCTM, boundaryInCTM))
	img = r.imageWithClip(img, clipPath, m, 96)
	m = m.Scale(imgW/float64(img.Bounds().Dx()), imgH/float64(img.Bounds().Dy()))
	img, pad := imageWithTransparentEdge(img)
	if pad > 0 {
		p := float64(pad)
		m[0][2] -= m[0][0]*p + m[0][1]*p
		m[1][2] -= m[1][0]*p + m[1][1]*p
	}
	ctx.RenderImage(r.canvasEncodedImage(img), ctx.CoordSystemView().Mul(ctx.View()).Mul(m))
	if obj.Border != nil {
		r.renderImageBorder(ctx, obj, box, pageH, parentCTM, boundaryInCTM, clipPath)
	}
}

// canvasEncodedImage 在Canvas输出边界保留原始编码，不重新压缩图片
// 入参: img 库图片
// 返回: image.Image Canvas可直接嵌入的图片
func (r *Renderer) canvasEncodedImage(img image.Image) image.Image {
	source, ok := img.(*EncodedImage)
	if !ok {
		return img
	}
	state := r.canvasState()
	if encoded, ok := state.images[source]; ok {
		return encoded
	}
	var encoded *canvasimage.Image
	if source.format == "jpeg" {
		encoded, _ = canvasimage.NewJPEGImage(bytes.NewReader(source.Bytes()))
	} else {
		encoded, _ = canvasimage.NewPNGImage(bytes.NewReader(source.Bytes()))
	}
	encoded.Bytes = source.Bytes()
	state.images[source] = encoded
	return encoded
}

// renderImageBorder 渲染图像边框
// 入参: ctx 画布上下文, obj 图片对象, box 图像边界, pageH 页面高度, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM, clipPath 裁剪路径
func (r *Renderer) renderImageBorder(ctx *canvas.Context, obj ImageObject, box Box, pageH float64, parentCTM *Matrix, boundaryInCTM bool, clipPath *canvas.Path) {
	border := obj.Border
	width := defaultPathLineWidth
	if border.LineWidth != nil {
		width = *border.LineWidth
	}
	if width == 0 {
		return
	}
	w, h := box.W, box.H
	data := fmt.Sprintf("M 0 0 L %g 0 L %g %g L 0 %g C", w, w, h, h)
	rx, ry := min(border.HorizonalCornerRadius, w/2), min(border.VerticalCornerRadius, h/2)
	if rx > 0 && ry > 0 {
		data = fmt.Sprintf("M 0 %g A %g %g 0 0 1 %g 0 L %g 0 A %g %g 0 0 1 %g %g L %g %g A %g %g 0 0 1 %g %g L %g %g A %g %g 0 0 1 0 %g C",
			ry, rx, ry, rx, w-rx, rx, ry, w, ry, w, h-ry, rx, ry, w-rx, h, rx, h, rx, ry, h-ry)
	}
	r.renderPath(ctx, PathObject{
		Boundary:        obj.Boundary,
		LineWidth:       width,
		DashOffset:      &border.DashOffset,
		DashPattern:     border.DashPattern,
		Alpha:           obj.Alpha,
		StrokeColor:     border.BorderColor,
		AbbreviatedData: data,
	}, pageH, nil, parentCTM, boundaryInCTM, clipPath)
}

// imageWithClip 应用图片裁剪区域
// 低分辨率图片按最低精度细化蒙版，细化后的像素上限16Mi，不降低原图分辨率
// 入参: img 图片对象, clipPath 裁剪路径, m 图片变换矩阵, dpi 最低蒙版分辨率，0保留原图精度
// 返回: image.Image 裁剪后的图片对象
func (r *Renderer) imageWithClip(img image.Image, clipPath *canvas.Path, m canvas.Matrix, dpi float64) image.Image {
	if img == nil || clipPath == nil || m.Det() == 0 {
		return img
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return img
	}
	p0 := m.Dot(canvas.Point{})
	p1 := m.Dot(canvas.Point{X: float64(w)})
	p2 := m.Dot(canvas.Point{X: float64(w), Y: float64(h)})
	p3 := m.Dot(canvas.Point{Y: float64(h)})
	imagePath := &canvas.Path{}
	imagePath.MoveTo(p0.X, p0.Y)
	imagePath.LineTo(p1.X, p1.Y)
	imagePath.LineTo(p2.X, p2.Y)
	imagePath.LineTo(p3.X, p3.Y)
	imagePath.Close()
	if rect, ok := rectangularPath(clipPath); ok {
		if rect.Contains(imagePath.FastBounds()) {
			return img
		}
	} else if clipPath.Contains(imagePath) {
		return img
	}
	scale := math.Max(math.Hypot(m[0][0], m[1][0]), math.Hypot(m[0][1], m[1][1])) * dpi / 25.4
	scale = math.Min(scale, math.Sqrt((16<<20)/(float64(w)*float64(h))))
	if scale > 1 {
		nw, nh := int(float64(w)*scale), int(float64(h)*scale)
		resized := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(resized, resized.Bounds(), imagePixelSource(img), bounds, draw.Src, nil)
		img = resized
		m = m.Scale(float64(w)/float64(nw), float64(h)/float64(nh))
		bounds, w, h = resized.Bounds(), nw, nh
	}
	clip := clipPath.Copy().Transform(m.Inv())
	compiler := &canvasPageCompiler{page: &RasterPage{Width: float64(w), Height: float64(h), DPI: 25.4}, geometry: r.backends.Geometry}
	ctx := canvas.NewContext(compiler)
	ctx.SetFillColor(canvas.White)
	ctx.SetStrokeColor(canvas.Transparent)
	ctx.DrawPath(0, 0, clip)
	if compiler.err != nil {
		r.renderError = compiler.err
		return img
	}
	if r.backends.Raster == nil {
		r.renderError = fmt.Errorf("image clip raster: %w", ErrBackendUnavailable)
		return img
	}
	result, err := r.backends.Raster.Render(compiler.page)
	if err != nil {
		r.renderError = err
		return img
	}
	mask, ok := result.(*image.RGBA)
	if !ok {
		mask = image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(mask, mask.Bounds(), result, result.Bounds().Min, draw.Src)
	}
	if mask.Opaque() {
		return img
	}
	source := imagePixelSource(img)
	out := &image.NRGBA{Pix: mask.Pix, Stride: mask.Stride, Rect: bounds}
	if src, ok := source.(*image.NRGBA); ok {
		for y := 0; y < h; y++ {
			offset := src.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := out.Pix[y*out.Stride : y*out.Stride+w*4]
			pixels := src.Pix[offset : offset+w*4]
			for x := 0; x < len(row); x += 4 {
				copy(row[x:x+3], pixels[x:x+3])
				row[x+3] = uint8(int(pixels[x+3]) * int(row[x+3]) / 255)
			}
		}
		return out
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := imageNRGBAAt(source, x, y)
			a := mask.RGBAAt(x-bounds.Min.X, y-bounds.Min.Y).A
			c.A = uint8(int(c.A) * int(a) / 255)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}
