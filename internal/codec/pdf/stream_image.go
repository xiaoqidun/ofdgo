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

package pdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// EncodedImageStream 保存已编码待流式写出的PDF图像及可选软蒙版数据
type EncodedImageStream struct {
	Dict      pdfgo.Dictionary
	Data      []byte
	SMaskDict pdfgo.Dictionary
	SMaskData []byte
}

// EncodeRawImageStream 按压缩配置编码直通JPEG、JPX或JBIG2原始流，无更小候选时零重编码直通
// 入参: ctx 取消上下文, raw 原始图像流, compression 压缩配置, target 目标降采样尺寸
// 返回: EncodedImageStream 编码结果, error 编码或取消错误
func EncodeRawImageStream(ctx context.Context, raw *RawImage, compression pdfgo.CompressionOptions, target image.Point) (EncodedImageStream, error) {
	if err := ctx.Err(); err != nil {
		return EncodedImageStream{}, err
	}
	data := raw.Data
	width, height := raw.Width, raw.Height
	bpc := raw.BitsPerComponent
	colorSpace := raw.ColorSpace
	filter := raw.Filter
	if filter == "DCTDecode" && compression.Mode != pdfgo.CompressionUnchanged {
		if compression.Mode == pdfgo.CompressionLossless {
			if optimized, err := pdfgo.OptimizeJPEG(ctx, data); err == nil && len(optimized) < len(data) {
				data = optimized
			}
		} else if compression.Mode == pdfgo.CompressionLossy {
			needsResize := target.X > 0 && target.Y > 0 && (target.X < width || target.Y < height)
			decoded, err := jpeg.Decode(bytes.NewReader(data))
			if err == nil {
				candidateImg := decoded
				if needsResize {
					if resized, rerr := pdfgo.ResizeImage(ctx, decoded, target); rerr == nil && resized != nil {
						candidateImg = resized
					}
				}
				var jbuf bytes.Buffer
				if jerr := jpeg.Encode(&jbuf, candidateImg, &jpeg.Options{Quality: compression.ImageQuality()}); jerr == nil {
					cand := jbuf.Bytes()
					if opt, oerr := pdfgo.OptimizeJPEG(ctx, cand); oerr == nil && len(opt) < len(cand) {
						cand = opt
					}
					if len(cand) < len(data) {
						data = cand
						b := candidateImg.Bounds()
						width, height = b.Dx(), b.Dy()
						if _, ok := candidateImg.(*image.Gray); ok {
							colorSpace = "DeviceGray"
						} else {
							colorSpace = "DeviceRGB"
						}
						bpc = 8
					}
				}
			}
		}
	}
	dict := pdfgo.Dictionary{
		"Type":    pdfgo.Name("XObject"),
		"Subtype": pdfgo.Name("Image"),
		"Width":   pdfgo.Integer(width),
		"Height":  pdfgo.Integer(height),
		"Filter":  pdfgo.Name(filter),
	}
	if bpc != 1 && filter != "JBIG2Decode" {
		dict["Interpolate"] = pdfgo.Boolean(true)
	}
	if bpc > 0 {
		dict["BitsPerComponent"] = pdfgo.Integer(bpc)
	}
	if colorSpace != "" {
		dict["ColorSpace"] = pdfgo.Name(colorSpace)
	}
	return EncodedImageStream{Dict: dict, Data: data}, ctx.Err()
}

// EncodePDFImageStream 将通用内存或惰性图像编码为紧凑PDF图像流及可选软蒙版
// 入参: ctx 取消上下文, src 源图像, compression 压缩配置, target 目标降采样尺寸, level zlib级别
// 返回: EncodedImageStream 编码结果, error 解码、编码或取消错误
func EncodePDFImageStream(ctx context.Context, src image.Image, compression pdfgo.CompressionOptions, target image.Point, level int) (EncodedImageStream, error) {
	if err := ctx.Err(); err != nil {
		return EncodedImageStream{}, err
	}
	lossy := compression.Mode == pdfgo.CompressionLossy
	pixels, err := decodeExportImage(ctx, src, lossy)
	if err != nil {
		return EncodedImageStream{}, err
	}
	b := pixels.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return EncodedImageStream{}, fmt.Errorf("PDF output image pixel bounds differ")
	}
	if target.X > 0 && target.Y > 0 && (target.X < w || target.Y < h) {
		resized, rerr := pdfgo.ResizeImage(ctx, pixels, target)
		if rerr != nil {
			return EncodedImageStream{}, rerr
		}
		pixels = resized
		b = pixels.Bounds()
		w, h = b.Dx(), b.Dy()
	}
	switch img := pixels.(type) {
	case *image.CMYK:
		return encodeCMYKPDFImage(ctx, img, level)
	case *image.Gray16:
		if !lossy && gray16Needs16Bit(img) {
			return encodeGray16PDFImage(ctx, img, level)
		}
	case *image.NRGBA64:
		if !lossy && nrgba64Needs16Bit(img) {
			return encodeNRGBA64PDFImage(ctx, img, level)
		}
	}
	if !lossy && (pixels.ColorModel() == color.NRGBA64Model || pixels.ColorModel() == color.RGBA64Model) {
		if n64, ok := pixels.(interface{ NRGBA64At(int, int) color.NRGBA64 }); ok {
			if has16BitPrecision(ctx, b, n64) {
				return encodeGeneric16PDFImage(ctx, b, n64, level)
			}
		}
	}
	return encode8BitPDFImage(ctx, pixels, compression, level)
}

// TargetImageSize 按图像在页面上的毫米渲染尺寸与目标DPI计算降采样像素尺寸
// 入参: widthMM 渲染宽度(毫米), heightMM 渲染高度(毫米), dpi 目标DPI
// 返回: image.Point 目标像素尺寸，无需降采样时为零值
func TargetImageSize(widthMM, heightMM float64, dpi int) image.Point {
	if dpi <= 0 || !finite(widthMM) || !finite(heightMM) || widthMM <= 0 || heightMM <= 0 {
		return image.Point{}
	}
	tw := int(math.Ceil(widthMM * float64(dpi) / 25.4))
	th := int(math.Ceil(heightMM * float64(dpi) / 25.4))
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	return image.Point{X: tw, Y: th}
}

// encodeCMYKPDFImage 将CMYK图像按原始四通道无损编码为PDF图像流
// 入参: ctx 取消上下文, img CMYK图像, level zlib级别
// 返回: EncodedImageStream 编码结果, error 压缩或取消错误
func encodeCMYKPDFImage(ctx context.Context, img *image.CMYK, level int) (EncodedImageStream, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	raw := make([]byte, w*h*4)
	for y := range h {
		if err := ctx.Err(); err != nil {
			return EncodedImageStream{}, err
		}
		srcRow := img.Pix[img.PixOffset(b.Min.X, b.Min.Y+y):]
		copy(raw[y*w*4:(y+1)*w*4], srcRow[:w*4])
	}
	data, parms, err := compressImageSamples(raw, w, h, 4, 8, level)
	if err != nil {
		return EncodedImageStream{}, err
	}
	dict := makeImageDict(w, h, "DeviceCMYK", 8, "FlateDecode", parms)
	return EncodedImageStream{Dict: dict, Data: data}, nil
}

// encodeGray16PDFImage 保留16位灰度精度编码为PDF图像流
// 入参: ctx 取消上下文, img 16位灰度图, level zlib级别
// 返回: EncodedImageStream 编码结果, error 压缩或取消错误
func encodeGray16PDFImage(ctx context.Context, img *image.Gray16, level int) (EncodedImageStream, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	raw := make([]byte, w*h*2)
	for y := range h {
		if err := ctx.Err(); err != nil {
			return EncodedImageStream{}, err
		}
		srcRow := img.Pix[img.PixOffset(b.Min.X, b.Min.Y+y):]
		copy(raw[y*w*2:(y+1)*w*2], srcRow[:w*2])
	}
	data, parms, err := compressImageSamples(raw, w, h, 1, 16, level)
	if err != nil {
		return EncodedImageStream{}, err
	}
	dict := makeImageDict(w, h, "DeviceGray", 16, "FlateDecode", parms)
	return EncodedImageStream{Dict: dict, Data: data}, nil
}

// encodeNRGBA64PDFImage 保留16位非预乘RGB与软蒙版精度编码为PDF图像流
// 入参: ctx 取消上下文, img 16位非预乘图, level zlib级别
// 返回: EncodedImageStream 编码结果, error 压缩或取消错误
func encodeNRGBA64PDFImage(ctx context.Context, img *image.NRGBA64, level int) (EncodedImageStream, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rgb := make([]byte, w*h*6)
	alpha := make([]byte, w*h*2)
	hasAlpha := false
	for y := range h {
		if err := ctx.Err(); err != nil {
			return EncodedImageStream{}, err
		}
		srcRow := img.Pix[img.PixOffset(b.Min.X, b.Min.Y+y):]
		rgbRow := rgb[y*w*6:]
		aRow := alpha[y*w*2:]
		for x := range w {
			si, ri, ai := x*8, x*6, x*2
			aHi, aLo := srcRow[si+6], srcRow[si+7]
			if aHi != 255 || aLo != 255 {
				hasAlpha = true
			}
			aRow[ai], aRow[ai+1] = aHi, aLo
			if aHi != 0 || aLo != 0 {
				copy(rgbRow[ri:ri+6], srcRow[si:si+6])
			}
		}
	}
	rgbData, rgbParms, err := compressImageSamples(rgb, w, h, 3, 16, level)
	if err != nil {
		return EncodedImageStream{}, err
	}
	res := EncodedImageStream{
		Dict: makeImageDict(w, h, "DeviceRGB", 16, "FlateDecode", rgbParms),
		Data: rgbData,
	}
	if hasAlpha {
		aData, aParms, err := compressImageSamples(alpha, w, h, 1, 16, level)
		if err != nil {
			return EncodedImageStream{}, err
		}
		res.SMaskDict = makeImageDict(w, h, "DeviceGray", 16, "FlateDecode", aParms)
		res.SMaskData = aData
	}
	return res, nil
}

// encodeGeneric16PDFImage 保留任意16位非预乘图像视图精度编码为PDF图像流
// 入参: ctx 取消上下文, b 图像范围, src 16位像素访问器, level zlib级别
// 返回: EncodedImageStream 编码结果, error 压缩或取消错误
func encodeGeneric16PDFImage(ctx context.Context, b image.Rectangle, src interface{ NRGBA64At(int, int) color.NRGBA64 }, level int) (EncodedImageStream, error) {
	w, h := b.Dx(), b.Dy()
	rgb := make([]byte, w*h*6)
	alpha := make([]byte, w*h*2)
	hasAlpha := false
	for y := range h {
		if err := ctx.Err(); err != nil {
			return EncodedImageStream{}, err
		}
		rgbRow := rgb[y*w*6:]
		aRow := alpha[y*w*2:]
		for x := range w {
			c := src.NRGBA64At(b.Min.X+x, b.Min.Y+y)
			if c.A != 65535 {
				hasAlpha = true
			}
			ai := x * 2
			aRow[ai], aRow[ai+1] = byte(c.A>>8), byte(c.A)
			if c.A != 0 {
				ri := x * 6
				rgbRow[ri], rgbRow[ri+1] = byte(c.R>>8), byte(c.R)
				rgbRow[ri+2], rgbRow[ri+3] = byte(c.G>>8), byte(c.G)
				rgbRow[ri+4], rgbRow[ri+5] = byte(c.B>>8), byte(c.B)
			}
		}
	}
	rgbData, rgbParms, err := compressImageSamples(rgb, w, h, 3, 16, level)
	if err != nil {
		return EncodedImageStream{}, err
	}
	res := EncodedImageStream{
		Dict: makeImageDict(w, h, "DeviceRGB", 16, "FlateDecode", rgbParms),
		Data: rgbData,
	}
	if hasAlpha {
		aData, aParms, err := compressImageSamples(alpha, w, h, 1, 16, level)
		if err != nil {
			return EncodedImageStream{}, err
		}
		res.SMaskDict = makeImageDict(w, h, "DeviceGray", 16, "FlateDecode", aParms)
		res.SMaskData = aData
	}
	return res, nil
}

// encode8BitPDFImage 单遍提取8位颜色与透明度样本，按压缩档位择优输出二值、灰度、RGB或JPEG流
// 入参: ctx 取消上下文, pixels 源图像像素, compression 压缩配置, level zlib级别
// 返回: EncodedImageStream 编码结果, error 编码或取消错误
func encode8BitPDFImage(ctx context.Context, pixels image.Image, compression pdfgo.CompressionOptions, level int) (EncodedImageStream, error) {
	b := pixels.Bounds()
	w, h := b.Dx(), b.Dy()
	n := w * h
	rgb := make([]byte, n*3)
	alpha := make([]byte, n)
	hasAlpha := false
	isGray := true
	isBinary := true
	alphaBinary := true
	switch src := pixels.(type) {
	case *image.Gray:
		for y := range h {
			if err := ctx.Err(); err != nil {
				return EncodedImageStream{}, err
			}
			srcRow := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			dstOff := y * w
			for x := range w {
				v := srcRow[x]
				if v != 0 && v != 255 {
					isBinary = false
				}
				alpha[dstOff+x] = 255
				rgb[(dstOff+x)*3] = v
			}
		}
	case *image.NRGBA:
		for y := range h {
			if err := ctx.Err(); err != nil {
				return EncodedImageStream{}, err
			}
			srcRow := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			dstOff := y * w
			for x := range w {
				si := x * 4
				r, g, bl, a := srcRow[si], srcRow[si+1], srcRow[si+2], srcRow[si+3]
				if a != 255 {
					hasAlpha = true
					if a != 0 {
						alphaBinary = false
					}
				}
				alpha[dstOff+x] = a
				if a != 0 {
					ri := (dstOff + x) * 3
					rgb[ri], rgb[ri+1], rgb[ri+2] = r, g, bl
					if r != g || g != bl {
						isGray = false
						isBinary = false
					} else if r != 0 && r != 255 {
						isBinary = false
					}
				}
			}
		}
	case *image.RGBA:
		for y := range h {
			if err := ctx.Err(); err != nil {
				return EncodedImageStream{}, err
			}
			srcRow := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			dstOff := y * w
			for x := range w {
				si := x * 4
				pr, pg, pb, a := srcRow[si], srcRow[si+1], srcRow[si+2], srcRow[si+3]
				if a != 255 {
					hasAlpha = true
					if a != 0 {
						alphaBinary = false
					}
				}
				alpha[dstOff+x] = a
				if a != 0 {
					var r, g, bl byte
					if a == 255 {
						r, g, bl = pr, pg, pb
					} else {
						r = byte(min(255, (uint32(pr)*255+uint32(a)/2)/uint32(a)))
						g = byte(min(255, (uint32(pg)*255+uint32(a)/2)/uint32(a)))
						bl = byte(min(255, (uint32(pb)*255+uint32(a)/2)/uint32(a)))
					}
					ri := (dstOff + x) * 3
					rgb[ri], rgb[ri+1], rgb[ri+2] = r, g, bl
					if r != g || g != bl {
						isGray = false
						isBinary = false
					} else if r != 0 && r != 255 {
						isBinary = false
					}
				}
			}
		}
	default:
		for y := range h {
			if err := ctx.Err(); err != nil {
				return EncodedImageStream{}, err
			}
			dstOff := y * w
			for x := range w {
				if x&4095 == 4095 {
					if err := ctx.Err(); err != nil {
						return EncodedImageStream{}, err
					}
				}
				c := baseimage.NRGBAAt(pixels, b.Min.X+x, b.Min.Y+y)
				if c.A != 255 {
					hasAlpha = true
					if c.A != 0 {
						alphaBinary = false
					}
				}
				alpha[dstOff+x] = c.A
				if c.A != 0 {
					ri := (dstOff + x) * 3
					rgb[ri], rgb[ri+1], rgb[ri+2] = c.R, c.G, c.B
					if c.R != c.G || c.G != c.B {
						isGray = false
						isBinary = false
					} else if c.R != 0 && c.R != 255 {
						isBinary = false
					}
				}
			}
		}
	}
	var mainData []byte
	var mainDict pdfgo.Dictionary
	if isGray {
		gray := make([]byte, n)
		for i := range n {
			gray[i] = rgb[i*3]
		}
		if isBinary {
			packed := pack1BitSamples(gray, w, h)
			comp, _, err := CompressBytes(nil, nil, level, packed)
			if err != nil {
				return EncodedImageStream{}, err
			}
			mainData = comp
			mainDict = makeImageDict(w, h, "DeviceGray", 1, "FlateDecode", nil)
		} else {
			comp, parms, err := compressImageSamples(gray, w, h, 1, 8, level)
			if err != nil {
				return EncodedImageStream{}, err
			}
			mainData = comp
			mainDict = makeImageDict(w, h, "DeviceGray", 8, "FlateDecode", parms)
			if compression.Mode == pdfgo.CompressionLossy && !hasAlpha {
				grayImg := &image.Gray{Pix: gray, Stride: w, Rect: image.Rect(0, 0, w, h)}
				if jpg, ok := tryLossyJPEG(ctx, grayImg, compression.ImageQuality(), len(mainData)); ok {
					mainData = jpg
					mainDict = makeImageDict(w, h, "DeviceGray", 8, "DCTDecode", nil)
				}
			}
		}
	} else {
		comp, parms, err := compressImageSamples(rgb, w, h, 3, 8, level)
		if err != nil {
			return EncodedImageStream{}, err
		}
		mainData = comp
		mainDict = makeImageDict(w, h, "DeviceRGB", 8, "FlateDecode", parms)
		if compression.Mode == pdfgo.CompressionLossy && !hasAlpha {
			if jpg, ok := tryLossyJPEG(ctx, pixels, compression.ImageQuality(), len(mainData)); ok {
				mainData = jpg
				mainDict = makeImageDict(w, h, "DeviceRGB", 8, "DCTDecode", nil)
			}
		}
	}
	res := EncodedImageStream{Dict: mainDict, Data: mainData}
	if hasAlpha {
		if alphaBinary {
			packed := pack1BitSamples(alpha, w, h)
			comp, _, err := CompressBytes(nil, nil, level, packed)
			if err != nil {
				return EncodedImageStream{}, err
			}
			res.SMaskDict = makeImageDict(w, h, "DeviceGray", 1, "FlateDecode", nil)
			res.SMaskData = comp
		} else {
			comp, parms, err := compressImageSamples(alpha, w, h, 1, 8, level)
			if err != nil {
				return EncodedImageStream{}, err
			}
			res.SMaskDict = makeImageDict(w, h, "DeviceGray", 8, "FlateDecode", parms)
			res.SMaskData = comp
		}
	}
	return res, nil
}

// tryLossyJPEG 尝试以有损JPEG编码并优化，仅当体积小于既有无损流时采纳
// 入参: ctx 取消上下文, img 源图像, quality JPEG质量, maxBytes 现有无损流字节数
// 返回: []byte JPEG字节, bool 是否更小可采纳
func tryLossyJPEG(ctx context.Context, img image.Image, quality, maxBytes int) ([]byte, bool) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, false
	}
	cand := buf.Bytes()
	if opt, err := pdfgo.OptimizeJPEG(ctx, cand); err == nil && len(opt) < len(cand) {
		cand = opt
	}
	if len(cand) < maxBytes {
		return cand, true
	}
	return nil, false
}

// compressImageSamples 比较PNG Up预测器与纯Flate压缩体积，择优返回最小载荷与解码参数
// 入参: raw 紧凑样本字节, width 宽度, height 高度, colors 通道数, bpc 位深, level zlib级别
// 返回: []byte 压缩载荷, pdfgo.Dictionary 解码参数(无预测器时为nil), error 压缩错误
func compressImageSamples(raw []byte, width, height, colors, bpc, level int) ([]byte, pdfgo.Dictionary, error) {
	rowBytes := (width*colors*bpc + 7) / 8
	var zw *zlib.Writer
	var zbuf bytes.Buffer
	plain, zw, err := CompressBytes(zw, &zbuf, level, raw)
	if err != nil {
		return nil, nil, err
	}
	if height <= 1 || rowBytes == 0 || len(raw) < 64 {
		return plain, nil, nil
	}
	filtered := make([]byte, height*(rowBytes+1))
	var prev []byte
	for y := range height {
		src := raw[y*rowBytes : (y+1)*rowBytes]
		dst := filtered[y*(rowBytes+1):]
		dst[0] = 2
		if prev == nil {
			copy(dst[1:], src)
		} else {
			for j := range rowBytes {
				dst[1+j] = src[j] - prev[j]
			}
		}
		prev = src
	}
	pred, _, err := CompressBytes(zw, &zbuf, level, filtered)
	if err != nil {
		return nil, nil, err
	}
	if len(pred) < len(plain) {
		parms := pdfgo.Dictionary{
			"Predictor":        pdfgo.Integer(12),
			"Colors":           pdfgo.Integer(colors),
			"BitsPerComponent": pdfgo.Integer(bpc),
			"Columns":          pdfgo.Integer(width),
		}
		return pred, parms, nil
	}
	return plain, nil, nil
}

// pack1BitSamples 将每像素0或255的灰度切片按行对齐打包为1位大端位图
// 入参: samples 灰度样本, width 宽度, height 高度
// 返回: []byte 1位打包字节
func pack1BitSamples(samples []byte, width, height int) []byte {
	rowBytes := (width + 7) / 8
	out := make([]byte, rowBytes*height)
	for y := range height {
		src := samples[y*width:]
		dst := out[y*rowBytes:]
		for x := range width {
			if src[x] != 0 {
				dst[x>>3] |= 0x80 >> uint(x&7)
			}
		}
	}
	return out
}

// makeImageDict 构造标准PDF图像XObject字典
// 入参: width 宽度, height 高度, colorSpace 色彩空间名, bpc 位深, filter 过滤器名, decodeParms 可选解码参数
// 返回: pdfgo.Dictionary 图像字典
func makeImageDict(width, height int, colorSpace string, bpc int, filter string, decodeParms pdfgo.Dictionary) pdfgo.Dictionary {
	dict := pdfgo.Dictionary{
		"Type":             pdfgo.Name("XObject"),
		"Subtype":          pdfgo.Name("Image"),
		"Width":            pdfgo.Integer(width),
		"Height":           pdfgo.Integer(height),
		"ColorSpace":       pdfgo.Name(colorSpace),
		"BitsPerComponent": pdfgo.Integer(bpc),
		"Filter":           pdfgo.Name(filter),
	}
	if bpc != 1 {
		dict["Interpolate"] = pdfgo.Boolean(true)
	}
	if len(decodeParms) > 0 {
		dict["DecodeParms"] = decodeParms
	}
	return dict
}

// decodeExportImage 单次临时解码待导出图像，有损模式规范化为8位格式，不在惰性源上留存像素缓存
// 入参: ctx 取消上下文, original 源图像, lossy 是否处于有损模式
// 返回: image.Image 解码像素, error 解码或范围错误
func decodeExportImage(ctx context.Context, original image.Image, lossy bool) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pixels, err := baseimage.DecodeTransientContext(ctx, original)
	if err != nil {
		return nil, err
	}
	if pixels == nil || pixels.Bounds() != original.Bounds() {
		return nil, fmt.Errorf("PDF output image pixel bounds differ")
	}
	if lossy {
		return normalizeLossyImage(ctx, pixels)
	}
	return pixels, nil
}

// normalizeLossyImage 将高精度图像降至8位以启用有损压缩优化
// 入参: ctx 取消上下文, pixels 源像素
// 返回: image.Image 8位像素, error 取消错误
func normalizeLossyImage(ctx context.Context, pixels image.Image) (image.Image, error) {
	switch src := pixels.(type) {
	case *image.NRGBA, *image.RGBA, *image.Gray, *image.YCbCr, *image.CMYK:
		return pixels, ctx.Err()
	case *image.Gray16:
		b := src.Bounds()
		w, h := b.Dx(), b.Dy()
		dst := image.NewGray(image.Rect(0, 0, w, h))
		for y := range h {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			srcRow := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			dstRow := dst.Pix[y*dst.Stride:]
			for x := range w {
				dstRow[x] = srcRow[x*2]
			}
		}
		return dst, ctx.Err()
	case *image.NRGBA64:
		b := src.Bounds()
		w, h := b.Dx(), b.Dy()
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			srcRow := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			dstRow := dst.Pix[y*dst.Stride:]
			for x := range w {
				si, di := x*8, x*4
				dstRow[di], dstRow[di+1], dstRow[di+2], dstRow[di+3] = srcRow[si], srcRow[si+2], srcRow[si+4], srcRow[si+6]
			}
		}
		return dst, ctx.Err()
	}
	b := pixels.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dstRow := dst.Pix[y*dst.Stride:]
		for x := range w {
			c := baseimage.NRGBAAt(pixels, b.Min.X+x, b.Min.Y+y)
			di := x * 4
			dstRow[di], dstRow[di+1], dstRow[di+2], dstRow[di+3] = c.R, c.G, c.B, c.A
		}
	}
	return dst, ctx.Err()
}

// gray16Needs16Bit 检测16位灰度图是否包含无法无损表示为8位的样本
// 入参: img 16位灰度图
// 返回: bool 是否需保留16位精度
func gray16Needs16Bit(img *image.Gray16) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	for y := range h {
		row := img.Pix[img.PixOffset(b.Min.X, b.Min.Y+y):]
		for x := range w {
			if row[x*2] != row[x*2+1] {
				return true
			}
		}
	}
	return false
}

// nrgba64Needs16Bit 检测16位非预乘图是否包含无法无损表示为8位的样本
// 入参: img 16位非预乘图
// 返回: bool 是否需保留16位精度
func nrgba64Needs16Bit(img *image.NRGBA64) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	for y := range h {
		row := img.Pix[img.PixOffset(b.Min.X, b.Min.Y+y):]
		for x := range w {
			i := x * 8
			if row[i] != row[i+1] || row[i+2] != row[i+3] || row[i+4] != row[i+5] || row[i+6] != row[i+7] {
				return true
			}
		}
	}
	return false
}

// has16BitPrecision 检测通用16位像素访问器是否包含超出8位精度的样本
// 入参: ctx 取消上下文, b 图像范围, src 16位像素访问器
// 返回: bool 是否需保留16位精度
func has16BitPrecision(ctx context.Context, b image.Rectangle, src interface{ NRGBA64At(int, int) color.NRGBA64 }) bool {
	w, h := b.Dx(), b.Dy()
	for y := range h {
		if ctx.Err() != nil {
			return false
		}
		for x := range w {
			c := src.NRGBA64At(b.Min.X+x, b.Min.Y+y)
			if c.R%257 != 0 || c.G%257 != 0 || c.B%257 != 0 || c.A%257 != 0 {
				return true
			}
		}
	}
	return false
}
