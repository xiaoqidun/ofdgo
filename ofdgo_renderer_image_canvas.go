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
	"fmt"
	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
	"image"
	"math"

	"github.com/tdewolff/canvas"
	canvasimage "github.com/tdewolff/canvas/image"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
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
	if obj.Visible != nil && !*obj.Visible || obj.Alpha != nil && *obj.Alpha == 0 {
		return
	}
	img, err := r.imageObjectResource(obj)
	if err != nil {
		r.renderError = err
		return
	}
	box, _ := ParseBox(obj.Boundary)
	mask, err := r.imageObjectMask(obj, img)
	if err != nil {
		r.renderError = err
		return
	}
	img, err = imageWithEffects(r.pageContext, img, mask, obj.Alpha)
	if err != nil {
		r.renderError = err
		return
	}
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
	clipPath := r.intersectCachedClipPath(parentClip, r.buildObjectClipPath(obj.Clips, pageH, obj.Boundary, localCTM, parentCTM, boundaryInCTM))
	r.drawClippedImage(ctx, img, clipPath, m)
	if r.renderError != nil {
		return
	}
	if obj.Border != nil {
		r.renderImageBorder(ctx, obj, box, pageH, parentCTM, boundaryInCTM, clipPath)
	}
}

// drawClippedImage 共用图像及签章的几何裁剪，编译和PDF输出保留原图及独立裁剪
// 入参: ctx 画布上下文, img 原图, clipPath 页面裁剪，可为空, m 像素到画布的变换
func (r *Renderer) drawClippedImage(ctx *canvas.Context, img image.Image, clipPath *canvas.Path, m canvas.Matrix) {
	imgW, imgH := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
	imageClip := clipPath
	compiler, compiled := ctx.Renderer.(*canvasPageCompiler)
	pdfOutput, pdf := ctx.Renderer.(*pdfRenderer)
	if pdf && imageClip != nil && imageClip.Empty() {
		return
	}
	if (compiled || pdf) && imageClip != nil && imageClipContains(img, imageClip, m) {
		imageClip = nil
	}
	if !compiled && !pdf {
		img = r.imageWithClip(img, clipPath, m, 96)
	}
	if r.renderError != nil {
		return
	}
	m = m.Scale(imgW/float64(img.Bounds().Dx()), imgH/float64(img.Bounds().Dy()))
	if _, pdf := ctx.Renderer.(*pdfRenderer); !pdf {
		var pad int
		var err error
		img, pad, err = baseimage.WithTransparentEdge(r.pageContext, img)
		if err != nil {
			r.renderError = err
			return
		}
		if pad > 0 {
			p := float64(pad)
			m[0][2] -= m[0][0]*p + m[0][1]*p
			m[1][2] -= m[1][0]*p + m[1][1]*p
		}
	}
	if pdf {
		if imageClip != nil {
			pdfOutput.imageClip = imageClip.Copy().Transform(ctx.CoordSystemView().Mul(ctx.View()))
		}
		defer func() { pdfOutput.imageClip = nil }()
	}
	ctx.RenderImage(r.canvasEncodedImage(img), ctx.CoordSystemView().Mul(ctx.View()).Mul(m))
	if compiled && imageClip != nil && compiler.err == nil {
		view := (canvas.Matrix{{1, 0, 0}, {0, -1, compiler.page.Height}}).Mul(ctx.CoordSystemView()).Mul(ctx.View())
		segments, err := canvasRasterPath(imageClip.Copy().Transform(view))
		if err != nil {
			compiler.err = err
			return
		}
		compiler.page.Commands[len(compiler.page.Commands)-1].Clip = segments
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
	if encoded, ok := state.images.get(source); ok {
		return encoded
	}
	var encoded *canvasimage.Image
	if source.MIME() == "image/jpeg" {
		encoded, _ = canvasimage.NewJPEGImage(bytes.NewReader(source.Bytes()))
	} else {
		encoded, _ = canvasimage.NewPNGImage(bytes.NewReader(source.Bytes()))
	}
	encoded.Bytes = source.Bytes()
	state.images.put(source, encoded, 2*baseimage.StorageCost(source, state.images.limit/2))
	return encoded
}

// renderImageBorder 渲染图像边框
// 入参: ctx 画布上下文, obj 图片对象, box 图像边界, pageH 页面高度, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM, clipPath 裁剪路径
func (r *Renderer) renderImageBorder(ctx *canvas.Context, obj ImageObject, box Box, pageH float64, parentCTM *Matrix, boundaryInCTM bool, clipPath *canvas.Path) {
	border := imageBorderObject(obj, box)
	if border.LineWidth == 0 {
		return
	}
	r.renderPath(ctx, border, pageH, nil, parentCTM, boundaryInCTM, clipPath)
}

// imageClipContains 判断裁剪是否完整包含变换后的图像，避免重复削弱边缘覆盖率
// 入参: img 图片, clipPath 裁剪路径, m 图片到页面变换
// 返回: bool 是否完整包含
func imageClipContains(img image.Image, clipPath *canvas.Path, m canvas.Matrix) bool {
	b := img.Bounds()
	outline := canvas.Rectangle(float64(b.Dx()), float64(b.Dy())).Transform(m)
	if rect, ok := rectangularPath(clipPath); ok {
		return rect.Contains(outline.FastBounds())
	}
	if basegeom.ConvexContains(basegeom.Path(*geometryFromCanvasPath(clipPath)), basegeom.Path(*geometryFromCanvasPath(outline))) {
		return true
	}
	return clipPath.Contains(outline)
}

// imageWithClip 应用图片裁剪区域，保留原图采样精度
// 低分辨率图片按最低精度细化蒙版，细化后的像素上限16Mi，不降低原图分辨率
// 入参: img 图片对象, clipPath 裁剪路径, m 图片变换矩阵, dpi 最低蒙版分辨率，0保留原图精度
// 返回: image.Image 裁剪后的图片对象
func (r *Renderer) imageWithClip(img image.Image, clipPath *canvas.Path, m canvas.Matrix, dpi float64) image.Image {
	work := r.pageContext
	if work == nil {
		work = context.Background()
	}
	if err := work.Err(); err != nil {
		r.renderError = err
		return img
	}
	if img == nil || clipPath == nil || m.Det() == 0 {
		return img
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return img
	}
	if imageClipContains(img, clipPath, m) {
		return img
	}
	original := img
	source, err := baseimage.PixelDataContext(r.pageContext, img)
	if err != nil {
		r.renderError = err
		return img
	}
	img = source
	scale := math.Max(math.Hypot(m[0][0], m[1][0]), math.Hypot(m[0][1], m[1][1])) * dpi / 25.4
	scale = math.Min(scale, math.Sqrt((16<<20)/(float64(w)*float64(h))))
	if scale > 1 {
		nw, nh := int(float64(w)*scale), int(float64(h)*scale)
		var resized draw.Image
		if baseimage.HighPrecision(img) {
			resized = image.NewNRGBA64(image.Rect(0, 0, nw, nh))
		} else {
			resized = image.NewNRGBA(image.Rect(0, 0, nw, nh))
		}
		draw.CatmullRom.Scale(resized, resized.Bounds(), img, bounds, draw.Src, nil)
		img = resized
		m = m.Scale(float64(w)/float64(nw), float64(h)/float64(nh))
		bounds, w, h = resized.Bounds(), nw, nh
	}
	if err := work.Err(); err != nil {
		r.renderError = err
		return img
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
	if cancelErr := work.Err(); cancelErr != nil {
		err = cancelErr
	}
	if err != nil {
		r.renderError = err
		return img
	}
	if result == nil || result.Bounds().Size() != image.Pt(w, h) {
		r.renderError = fmt.Errorf("image clip raster dimensions differ from source")
		return img
	}
	result, err = baseimage.PixelDataContext(work, result)
	if err != nil {
		r.renderError = err
		return img
	}
	if baseimage.HighPrecision(img) || baseimage.HighPrecision(result) {
		out := image.NewNRGBA64(bounds)
		source64, _ := img.(*image.NRGBA64)
		maskBounds := result.Bounds()
		opaque := true
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			if err := work.Err(); err != nil {
				r.renderError = err
				return img
			}
			row := out.Pix[(y-bounds.Min.Y)*out.Stride:][:w*8]
			if source64 != nil {
				start := source64.PixOffset(bounds.Min.X, y)
				copy(row, source64.Pix[start:start+len(row)])
			}
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&255 == 0 {
					if err := work.Err(); err != nil {
						r.renderError = err
						return img
					}
				}
				alpha := baseimage.AlphaAt(result, maskBounds.Min.X+x-bounds.Min.X, maskBounds.Min.Y+y-bounds.Min.Y)
				opaque = opaque && alpha == 65535
				if source64 != nil {
					i := (x-bounds.Min.X)*8 + 6
					a := (uint32(row[i])<<8 | uint32(row[i+1])) * alpha / 65535
					row[i], row[i+1] = byte(a>>8), byte(a)
					continue
				}
				pixel := baseimage.NRGBA64At(img, x, y)
				pixel.A = uint16(uint32(pixel.A) * alpha / 65535)
				out.SetNRGBA64(x, y, pixel)
			}
		}
		if err := work.Err(); err != nil {
			r.renderError = err
			return img
		}
		if opaque {
			if scale <= 1 {
				return original
			}
			return img
		}
		return out
	}
	mask, ok := result.(*image.RGBA)
	if !ok {
		mask = image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; {
				if err := work.Err(); err != nil {
					r.renderError = err
					return img
				}
				end := x + min(256, w-x)
				draw.Draw(mask, image.Rect(x, y, end, y+1), result, result.Bounds().Min.Add(image.Pt(x, y)), draw.Src)
				x = end
			}
		}
	}
	if mask.Rect.Min != (image.Point{}) {
		normalized := *mask
		normalized.Rect = mask.Rect.Sub(mask.Rect.Min)
		mask = &normalized
	}
	opaque := true
	for y := 0; y < h && opaque; y++ {
		row := mask.Pix[y*mask.Stride : y*mask.Stride+w*4]
		for x := 3; x < len(row); x += 4 {
			if (x-3)&1023 == 0 {
				if err := work.Err(); err != nil {
					r.renderError = err
					return img
				}
			}
			if row[x] != 255 {
				opaque = false
				break
			}
		}
	}
	if err := work.Err(); err != nil {
		r.renderError = err
		return img
	}
	if opaque {
		if scale <= 1 {
			return original
		}
		return img
	}
	source = img
	out := &image.NRGBA{Pix: mask.Pix, Stride: mask.Stride, Rect: bounds}
	if src, ok := source.(*image.NRGBA); ok {
		for y := 0; y < h; y++ {
			if err := work.Err(); err != nil {
				r.renderError = err
				return img
			}
			offset := src.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := out.Pix[y*out.Stride : y*out.Stride+w*4]
			pixels := src.Pix[offset : offset+w*4]
			for x := 0; x < len(row); x += 4 {
				if x&1023 == 0 {
					if err := work.Err(); err != nil {
						r.renderError = err
						return img
					}
				}
				copy(row[x:x+3], pixels[x:x+3])
				row[x+3] = uint8(int(pixels[x+3]) * int(row[x+3]) / 255)
			}
		}
		if err := work.Err(); err != nil {
			r.renderError = err
			return img
		}
		return out
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := work.Err(); err != nil {
			r.renderError = err
			return img
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)&255 == 0 {
				if err := work.Err(); err != nil {
					r.renderError = err
					return img
				}
			}
			c := baseimage.NRGBAAt(source, x, y)
			a := mask.RGBAAt(x-bounds.Min.X, y-bounds.Min.Y).A
			c.A = uint8(int(c.A) * int(a) / 255)
			out.SetNRGBA(x, y, c)
		}
	}
	if err := work.Err(); err != nil {
		r.renderError = err
		return img
	}
	return out
}
