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
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"math"

	canvasimage "github.com/tdewolff/canvas/image"
	_ "github.com/xiaoqidun/jbig2"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
)

// decodeImageData 解码图片数据
// 入参: data 图片数据
// 返回: image.Image 图片对象, string 图片格式, error 错误信息
func decodeImageData(data []byte) (image.Image, string, error) {
	if isJPEGData(data) {
		if img, err := canvasimage.NewJPEGImage(bytes.NewReader(data)); err == nil {
			return img, "jpeg", nil
		}
	}
	if isPNGData(data) {
		if img, err := canvasimage.NewPNGImage(bytes.NewReader(data)); err == nil {
			return img, "png", nil
		}
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err == nil {
		return img, normalizeSealType(format), nil
	}
	if img, err := decodeBMPImage(data); err == nil {
		return img, "bmp", nil
	}
	return nil, "", err
}

// isJPEGData 判断是否为JPEG图片数据
// 入参: data 图片数据
// 返回: bool 是否为JPEG图片数据
func isJPEGData(data []byte) bool {
	return len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8
}

// isPNGData 判断是否为PNG图片数据
// 入参: data 图片数据
// 返回: bool 是否为PNG图片数据
func isPNGData(data []byte) bool {
	return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
}

// decodeImageConfigData 解码图片尺寸
// 入参: data 图片数据
// 返回: image.Config 图片尺寸, string 图片格式, error 错误信息
func decodeImageConfigData(data []byte) (image.Config, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil {
		return cfg, normalizeSealType(format), nil
	}
	if cfg, err := decodeBMPConfig(data); err == nil {
		return cfg, "bmp", nil
	}
	return image.Config{}, "", err
}

// decodeBMPConfig 解码BMP尺寸
// 入参: data 图片数据
// 返回: image.Config 图片尺寸, error 错误信息
func decodeBMPConfig(data []byte) (image.Config, error) {
	offset, width, height, bpp, compression, err := parseBMPHeader(data)
	if err != nil {
		return image.Config{}, err
	}
	if offset <= 0 || width <= 0 || height == 0 || compression != 0 {
		return image.Config{}, fmt.Errorf("unsupported bmp")
	}
	if bpp != 16 && bpp != 24 && bpp != 32 {
		return image.Config{}, fmt.Errorf("unsupported bmp")
	}
	if height < 0 {
		height = -height
	}
	return image.Config{ColorModel: color.NRGBAModel, Width: width, Height: height}, nil
}

// decodeBMPImage 解码BMP图片
// 入参: data 图片数据
// 返回: image.Image 图片对象, error 错误信息
func decodeBMPImage(data []byte) (image.Image, error) {
	offset, width, height, bpp, compression, err := parseBMPHeader(data)
	if err != nil {
		return nil, err
	}
	if compression != 0 {
		return nil, fmt.Errorf("unsupported bmp compression")
	}
	topDown := height < 0
	if topDown {
		height = -height
	}
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid bmp size")
	}
	rowStride := ((width*int(bpp) + 31) / 32) * 4
	if offset+rowStride*height > len(data) {
		return nil, fmt.Errorf("truncated bmp")
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		row := data[offset+srcY*rowStride:]
		for x := 0; x < width; x++ {
			switch bpp {
			case 16:
				v := binary.LittleEndian.Uint16(row[x*2:])
				img.SetNRGBA(x, y, color.NRGBA{
					R: uint8(((v >> 10) & 0x1F) * 255 / 31),
					G: uint8(((v >> 5) & 0x1F) * 255 / 31),
					B: uint8((v & 0x1F) * 255 / 31),
					A: 255,
				})
			case 24:
				p := x * 3
				img.SetNRGBA(x, y, color.NRGBA{R: row[p+2], G: row[p+1], B: row[p], A: 255})
			case 32:
				p := x * 4
				img.SetNRGBA(x, y, color.NRGBA{R: row[p+2], G: row[p+1], B: row[p], A: 255})
			default:
				return nil, fmt.Errorf("unsupported bmp")
			}
		}
	}
	return img, nil
}

// parseBMPHeader 解析BMP文件头
// 入参: data 图片数据
// 返回: int 像素偏移, int 宽度, int 高度, uint16 位数, uint32 压缩方式, error 错误信息
func parseBMPHeader(data []byte) (int, int, int, uint16, uint32, error) {
	if len(data) < 54 || string(data[:2]) != "BM" {
		return 0, 0, 0, 0, 0, fmt.Errorf("not bmp")
	}
	offset := int(binary.LittleEndian.Uint32(data[10:14]))
	dibSize := binary.LittleEndian.Uint32(data[14:18])
	if dibSize < 40 {
		return 0, 0, 0, 0, 0, fmt.Errorf("unsupported bmp")
	}
	width := int(int32(binary.LittleEndian.Uint32(data[18:22])))
	height := int(int32(binary.LittleEndian.Uint32(data[22:26])))
	planes := binary.LittleEndian.Uint16(data[26:28])
	bpp := binary.LittleEndian.Uint16(data[28:30])
	compression := binary.LittleEndian.Uint32(data[30:34])
	if planes != 1 {
		return 0, 0, 0, 0, 0, fmt.Errorf("invalid bmp")
	}
	return offset, width, height, bpp, compression, nil
}

// imageWithMask 应用图片蒙版
// 入参: img 图片对象, mask 蒙版图片
// 返回: image.Image 蒙版处理后的图片对象
func imageWithMask(img, mask image.Image) image.Image {
	if img == nil || mask == nil {
		return img
	}
	bounds := img.Bounds()
	if bounds.Empty() || mask.Bounds().Empty() {
		return img
	}
	source := imagePixelSource(img)
	maskSource := imagePixelSource(mask)
	maskBounds := maskSource.Bounds()
	var opacity image.Image = maskSource
	if bounds.Size() != maskBounds.Size() {
		resized := image.NewGray(bounds)
		draw.CatmullRom.Scale(resized, bounds, maskSource, maskBounds, draw.Src, nil)
		opacity = resized
		maskBounds = bounds
	}
	out := image.NewNRGBA(bounds)
	if src, ok := source.(*image.NRGBA); ok {
		for y := 0; y < bounds.Dy(); y++ {
			offset := src.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := out.Pix[y*out.Stride : (y+1)*out.Stride]
			copy(row, src.Pix[offset:offset+len(row)])
			for x := 3; x < len(row); x += 4 {
				a := imageGrayAt(opacity, maskBounds.Min.X+x/4, maskBounds.Min.Y+y).Y
				row[x] = uint8(int(row[x]) * int(a) / 255)
			}
		}
		return out
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			mx := maskBounds.Min.X + x - bounds.Min.X
			my := maskBounds.Min.Y + y - bounds.Min.Y
			a := imageGrayAt(opacity, mx, my).Y
			c := imageNRGBAAt(source, x, y)
			c.A = uint8(int(c.A) * int(a) / 255)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

// imageWithAlpha 合并图片透明度
// 入参: img 图片对象, alpha 对象透明度
// 返回: image.Image 合并后的图片对象
func imageWithAlpha(img image.Image, alpha *int) image.Image {
	if img == nil || alpha == nil {
		return img
	}
	a := clampColor(*alpha)
	if a == 255 {
		return img
	}
	bounds := img.Bounds()
	out := image.NewNRGBA(bounds)
	source := imagePixelSource(img)
	if src, ok := source.(*image.NRGBA); ok {
		for y := 0; y < bounds.Dy(); y++ {
			offset := src.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			row := out.Pix[y*out.Stride : (y+1)*out.Stride]
			copy(row, src.Pix[offset:offset+len(row)])
			for x := 3; x < len(row); x += 4 {
				row[x] = uint8(int(row[x]) * a / 255)
			}
		}
		return out
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := imageNRGBAAt(source, x, y)
			c.A = uint8(int(c.A) * a / 255)
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

// imagePixelSource 获取图片像素源
// 入参: img 图片对象
// 返回: image.Image 图片像素源
func imagePixelSource(img image.Image) image.Image {
	if src, ok := img.(interface{ Image() (image.Image, error) }); ok {
		if decoded, err := src.Image(); err == nil {
			return decoded
		}
	}
	return img
}

// imageGrayAt 获取图片灰度像素
// 入参: img 图片对象, x X坐标, y Y坐标
// 返回: color.Gray 灰度像素
func imageGrayAt(img image.Image, x, y int) color.Gray {
	if src, ok := img.(*image.Gray); ok {
		return src.GrayAt(x, y)
	}
	if src, ok := img.(*image.Paletted); ok {
		return color.GrayModel.Convert(src.At(x, y)).(color.Gray)
	}
	if src, ok := img.(image.RGBA64Image); ok {
		r, g, b, _ := src.RGBA64At(x, y).RGBA()
		return color.Gray{Y: uint8((19595*r + 38470*g + 7471*b + 1<<15) >> 24)}
	}
	return color.GrayModel.Convert(img.At(x, y)).(color.Gray)
}

// imageNRGBAAt 获取图片NRGBA像素
// 入参: img 图片对象, x X坐标, y Y坐标
// 返回: color.NRGBA NRGBA像素
func imageNRGBAAt(img image.Image, x, y int) color.NRGBA {
	if src, ok := img.(*image.NRGBA); ok {
		return src.NRGBAAt(x, y)
	}
	if src, ok := img.(*image.YCbCr); ok {
		r, g, b, _ := src.YCbCrAt(x, y).RGBA()
		return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
	}
	if src, ok := img.(*image.RGBA); ok {
		c := src.RGBA64At(x, y)
		if c.A == 0 {
			return color.NRGBA{}
		}
		r, g, b, a := uint32(c.R), uint32(c.G), uint32(c.B), uint32(c.A)
		if a != 0xffff {
			r = r * 0xffff / a
			g = g * 0xffff / a
			b = b * 0xffff / a
		}
		return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
	}
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
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
	if src, ok := img.(*canvasimage.Image); ok && src.Mimetype == "image/jpeg" && src.Mask == nil {
		return img, 0
	}
	source := imagePixelSource(img)
	if opaque, ok := source.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		return img, 0
	}
	hasZero, hasVisible := false, false
	src, ok := source.(*image.NRGBA)
	if ok {
		srcBounds := src.Bounds()
		hasZero = src.Pix[3] == 0
		hasVisible = !hasZero
	scan:
		for y := srcBounds.Min.Y; y < srcBounds.Max.Y; y++ {
			offset := src.PixOffset(srcBounds.Min.X, y) + 3
			for x := 0; x < w; x++ {
				if (src.Pix[offset] == 0) != hasZero {
					hasZero, hasVisible = true, true
					break scan
				}
				offset += 4
			}
		}
	} else {
		src = image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := imageNRGBAAt(source, bounds.Min.X+x, bounds.Min.Y+y)
				src.SetNRGBA(x, y, c)
				if c.A == 0 {
					hasZero = true
				} else {
					hasVisible = true
				}
			}
		}
	}
	if !hasZero || !hasVisible {
		return img, 0
	}
	srcBounds := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, w+2, h+2))
	draw.Draw(out, out.Bounds().Inset(1), src, srcBounds.Min, draw.Src)
	for y := 0; y < h; y++ {
		sy := srcBounds.Min.Y + y
		offset := src.PixOffset(srcBounds.Min.X, sy) + 3
		for x := 0; x < w; x++ {
			if src.Pix[offset] == 0 {
				if edge, ok := transparentEdgeColor(src, srcBounds.Min.X+x, sy); ok {
					out.SetNRGBA(x+1, y+1, edge)
				}
			}
			offset += 4
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
