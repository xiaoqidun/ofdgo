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
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"reflect"

	"github.com/tdewolff/canvas"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"golang.org/x/image/draw"
)

// transparentEdgeResult 记录单页内复用的透明补边图像及边距宽度
type transparentEdgeResult struct {
	img image.Image
	pad int
}

// isOpaqueEncodedImage 判断惰性编码图像是否不含透明通道与额外色彩转换，避免SVG输出触发像素解码
// 入参: img 图像对象
// 返回: bool 是否为可直接输出的不透明编码图像
func isOpaqueEncodedImage(img image.Image) bool {
	encoded, ok := img.(*EncodedImage)
	if !ok {
		return false
	}
	switch encoded.ColorModel() {
	case color.YCbCrModel, color.GrayModel, color.Gray16Model, color.RGBAModel, color.RGBA64Model:
		return true
	default:
		return false
	}
}

// isDirectSVGEncodedImage 判断惰性编码图像是否无需色彩空间转换即可直接由浏览器SVG渲染
// 入参: img 图像对象
// 返回: bool 是否可零解码直出至SVG
func isDirectSVGEncodedImage(img image.Image) bool {
	encoded, ok := img.(*EncodedImage)
	if !ok {
		return false
	}
	switch encoded.ColorModel().(type) {
	case color.Palette:
		return true
	}
	switch encoded.ColorModel() {
	case color.YCbCrModel, color.GrayModel, color.Gray16Model, color.RGBAModel, color.RGBA64Model, color.NRGBAModel:
		return true
	default:
		return false
	}
}

// isAxisAlignedCanvasMatrix 判断仿射变换矩阵是否严格轴对齐且非退化
// 入参: m 变换矩阵
// 返回: bool 是否轴对齐
func isAxisAlignedCanvasMatrix(m canvas.Matrix) bool {
	return m[0][0] != 0 && m[1][1] != 0 && math.Abs(m[0][1]) <= 1e-9*math.Abs(m[0][0]) && math.Abs(m[1][0]) <= 1e-9*math.Abs(m[1][1])
}

// isPointerImage 判断图像实现是否为可安全按身份比较的指针类型
// 入参: img 图像对象
// 返回: bool 是否为指针图像
func isPointerImage(img image.Image) bool {
	return img != nil && reflect.ValueOf(img).Kind() == reflect.Pointer
}

// cachedTransparentEdge 按源图像指针在单页内缓存透明补边结果，避免重复全图扫描
// 入参: ctx 取消上下文, cache 页内补边缓存指针, img 源图像
// 返回: image.Image 补边后图像, int 补边像素数, error 解码或取消错误
func cachedTransparentEdge(ctx context.Context, cache *map[image.Image]transparentEdgeResult, img image.Image) (image.Image, int, error) {
	cacheable := cache != nil && isPointerImage(img)
	if cacheable && *cache != nil {
		if entry, ok := (*cache)[img]; ok {
			return entry.img, entry.pad, nil
		}
	}
	unwrapped := unwrapEncodedImage(img)
	padded, pad, err := baseimage.WithTransparentEdge(ctx, unwrapped)
	if err != nil {
		return nil, 0, err
	}
	if padded == unwrapped {
		padded = img
	} else {
		padded = wrapEncodedImage(padded)
	}
	if cacheable {
		if *cache == nil {
			*cache = make(map[image.Image]transparentEdgeResult)
		}
		(*cache)[img] = transparentEdgeResult{img: padded, pad: pad}
	}
	return padded, pad, nil
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
	if geometryConvexContains(*geometryFromCanvasPath(clipPath), *geometryFromCanvasPath(outline)) {
		return true
	}
	return clipPath.Contains(outline)
}

// renderImage 渲染图片，图像像素受Boundary裁剪而边框豁免Boundary裁剪
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
	box, _ := ParseBox(obj.Boundary)
	hasBorder := obj.Border != nil
	if hasBorder {
		if border := imageBorderObject(obj, box); border.LineWidth == 0 {
			hasBorder = false
		}
	}
	useGroup := canCanvasTransparencyGroup(ctx.Renderer) && hasBorder && obj.Alpha != nil && clampColor(*obj.Alpha) < 255
	imgAlpha := obj.Alpha
	borderObj := obj
	if useGroup {
		if endGroup, ok := beginCanvasTransparencyGroup(ctx.Renderer, uint8(clampColor(*obj.Alpha))); ok {
			defer endGroup()
			imgAlpha = nil
			borderObj.Alpha = nil
		}
	}
	var img image.Image
	pdfOutput, isPDF := ctx.Renderer.(*pdfRenderer)
	if isPDF {
		if raw := pdfOutput.rawImageResource(r, obj); raw != nil {
			img = raw
		}
	}
	var effectKey imageEffectKey
	hasEffectKey := false
	if img == nil && !isPDF && r.decodeImages && obj.ImageMask == "" && r.pageContext != nil && r.reader != nil {
		if pf, ok := r.pageContext.Value(pageImagePrefetchKey{}).(*pageImagePrefetch); ok && pf != nil && len(pf.byPath) > 0 {
			primary := obj.ResourceID
			if obj.Substitution != "" && !sameResourceID(obj.Substitution, obj.ResourceID) && (r.printing || r.dpi >= 300) {
				primary = obj.Substitution
			}
			if resolved, ok := r.reader.resolveResPathScoped(r.pageResources, primary); ok {
				cleanPath := cleanPackagePath(r.reader.ResPath(resolved))
				if entry, hit := pf.byPath[cleanPath]; hit {
					effAlpha := uint8(255)
					if imgAlpha != nil {
						effAlpha = uint8(clampColor(*imgAlpha))
					}
					if effAlpha == 255 {
						img = entry.raw
					} else {
						ak := pagePrefetchedAlphaKey{path: cleanPath, alpha: effAlpha}
						if cached, ok := pf.byAlpha[ak]; ok {
							img = cached
						} else {
							applied, err := imageWithEffects(r.pageContext, entry.raw, nil, imgAlpha)
							if err != nil {
								r.renderError = err
								return
							}
							if pf.byAlpha == nil {
								pf.byAlpha = make(map[pagePrefetchedAlphaKey]image.Image)
							}
							pf.byAlpha[ak] = applied
							img = applied
						}
					}
				}
			}
		}
	}
	if img == nil {
		var err error
		img, effectKey, hasEffectKey, err = r.imageObjectWithEffects(obj, imgAlpha)
		if err != nil {
			r.renderError = err
			return
		}
	}
	if isPDF && hasEffectKey {
		pdfOutput.imageKey = effectKey
		defer func() { pdfOutput.imageKey = nil }()
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
	r.drawClippedImageInterpolate(ctx, img, clipPath, m, obj.interpolate)
	if r.renderError != nil {
		return
	}
	if hasBorder {
		r.renderImageBorder(ctx, borderObj, box, pageH, localCTM, parentCTM, boundaryInCTM, parentClip)
	}
}

// drawClippedImage 共用图像及签章的几何裁剪，编译、SVG和PDF输出保留原图及独立裁剪
// 入参: ctx 画布上下文, img 原图, clipPath 页面裁剪，可为空, m 像素到画布的变换
func (r *Renderer) drawClippedImage(ctx *canvas.Context, img image.Image, clipPath *canvas.Path, m canvas.Matrix) {
	r.drawClippedImageInterpolate(ctx, img, clipPath, m, nil)
}

// drawClippedImageInterpolate 透传图像插值开关并执行几何裁剪与渲染输出
// 入参: ctx 画布上下文, img 原图, clipPath 页面裁剪，可为空, m 像素到画布的变换, interpolate 可选插值开关
func (r *Renderer) drawClippedImageInterpolate(ctx *canvas.Context, img image.Image, clipPath *canvas.Path, m canvas.Matrix, interpolate *bool) {
	imgW, imgH := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
	imageClip := clipPath
	compiler, compiled := ctx.Renderer.(*canvasPageCompiler)
	pdfOutput, pdf := ctx.Renderer.(*pdfRenderer)
	svgOutput, isSVG := ctx.Renderer.(*svgResourceRenderer)
	if (pdf || isSVG) && imageClip != nil && imageClip.Empty() {
		return
	}
	if (compiled || pdf || isSVG) && imageClip != nil && imageClipContains(img, imageClip, m) {
		imageClip = nil
	}
	if !compiled && !pdf && !isSVG {
		v := ctx.CoordSystemView().Mul(ctx.View())
		dpi := 96 * max(1, math.Hypot(v[0][0], v[1][0]), math.Hypot(v[0][1], v[1][1]))
		img = r.imageWithClip(img, clipPath, m, dpi, interpolate)
	}
	if r.renderError != nil {
		return
	}
	m = m.Scale(imgW/float64(img.Bounds().Dx()), imgH/float64(img.Bounds().Dy()))
	total := ctx.CoordSystemView().Mul(ctx.View()).Mul(m)
	pxPerMM := r.dpi / 25.4
	pixelMatrix := [6]float64{total[0][0] * pxPerMM, total[1][0] * pxPerMM, total[0][1] * pxPerMM, total[1][1] * pxPerMM, 0, 0}
	nearestAxis := compiled && isAxisAlignedCanvasMatrix(total) && baseimage.UseNearestNeighbor(img.Bounds(), pixelMatrix, interpolate)
	skipTransparentEdge := pdf || nearestAxis || isSVG && (isOpaqueEncodedImage(img) || isAxisAlignedCanvasMatrix(total) && isDirectSVGEncodedImage(img))
	if !skipTransparentEdge {
		work := r.pageContext
		if work == nil {
			work = r.outputContext()
		}
		var edgeCache *map[image.Image]transparentEdgeResult
		if isSVG {
			edgeCache = &svgOutput.transparentEdges
		} else if r.transparentEdges != nil {
			edgeCache = &r.transparentEdges
		}
		var pad int
		var err error
		img, pad, err = cachedTransparentEdge(work, edgeCache, img)
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
	if compiled {
		compiler.imageInterpolate = interpolate
		defer func() { compiler.imageInterpolate = nil }()
	}
	if pdf {
		pdfOutput.imageInterpolate = interpolate
		if imageClip != nil {
			pdfOutput.imageClip = imageClip.Copy().Transform(ctx.CoordSystemView().Mul(ctx.View()))
		}
		defer func() {
			pdfOutput.imageClip = nil
			pdfOutput.imageInterpolate = nil
		}()
	}
	if isSVG {
		if imageClip != nil {
			svgOutput.imageClip = imageClip.Copy().Transform(ctx.CoordSystemView().Mul(ctx.View()))
		}
		defer func() { svgOutput.imageClip = nil }()
	}
	ctx.RenderImage(img, ctx.CoordSystemView().Mul(ctx.View()).Mul(m))
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

// renderImageBorder 渲染图像边框，仅受父级裁剪与显式Clips限制而不裁切外侧半边线宽
// 入参: ctx 画布上下文, obj 图片对象, box 图像边界, pageH 页面高度, localCTM 图像局部CTM, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM, parentClip 父级裁剪路径
func (r *Renderer) renderImageBorder(ctx *canvas.Context, obj ImageObject, box Box, pageH float64, localCTM Matrix, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	border := imageBorderObject(obj, box)
	if border.LineWidth == 0 {
		return
	}
	borderClip := parentClip
	if obj.Clips != nil {
		path, err := r.imageBorderClip(obj.Clips, obj.Boundary, localCTM, RenderState{Parent: parentCTM, BoundaryInCTM: boundaryInCTM})
		if err != nil {
			r.renderError = err
			return
		}
		canvasClip, err := geometryToCanvasPath(path)
		if err != nil {
			r.renderError = err
			return
		}
		if canvasClip != nil {
			canvasClip = canvasClip.Translate(0, pageH)
		}
		borderClip = r.intersectCachedClipPath(parentClip, canvasClip)
	}
	origin, _ := renderObjectMatrix(obj.Boundary, IdentityMatrix(), RenderState{Parent: parentCTM, BoundaryInCTM: boundaryInCTM})
	border.Boundary = ""
	r.renderPath(ctx, border, pageH, nil, &origin, true, borderClip)
}

// imageWithClip 应用图片裁剪区域，保留原图采样精度
// 低分辨率图片按最低精度细化蒙版，细化后的像素上限16Mi，不降低原图分辨率
// 入参: img 图片对象, clipPath 裁剪路径, m 图片变换矩阵, dpi 最低蒙版分辨率，0保留原图精度, interpolate 可选插值开关
// 返回: image.Image 裁剪后的图片对象
func (r *Renderer) imageWithClip(img image.Image, clipPath *canvas.Path, m canvas.Matrix, dpi float64, interpolate *bool) image.Image {
	work := r.pageContext
	if work == nil {
		work = r.outputContext()
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
	source, err := baseimage.PixelDataContext(work, img)
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
		if interpolate != nil && !*interpolate || interpolate == nil && w <= 8 && h <= 8 && scale >= 4 {
			draw.NearestNeighbor.Scale(resized, resized.Bounds(), img, bounds, draw.Src, nil)
		} else {
			draw.CatmullRom.Scale(resized, resized.Bounds(), img, bounds, draw.Src, nil)
		}
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
	raster := r.internalRasterBackend()
	if raster == nil {
		r.renderError = fmt.Errorf("image clip raster: %w", ErrBackendUnavailable)
		return img
	}
	result, err := raster.Render(work, compiler.page)
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
