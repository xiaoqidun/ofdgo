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

package image

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"io"
	"math"
	"runtime"
	"sync"

	"github.com/xiaoqidun/pdfgo"
)

// TransformRGBA 按八位预乘颜色变换同范围像素，源和目标可为同一图像
// 入参: target 目标缓冲, source 同范围源图像, transform 单像素颜色变换
func TransformRGBA(target *image.RGBA, source image.Image, transform func(color.RGBA) color.RGBA) {
	if target == nil || source == nil || transform == nil {
		return
	}
	rect := target.Rect
	width, height := rect.Dx(), rect.Dy()
	if width <= 0 || height <= 0 {
		return
	}
	if colored, ok := source.(*colorImage); ok {
		source = colored.NRGBA64
	}
	if width*height >= 262144 {
		if workers := min(runtime.GOMAXPROCS(0), 4, height); workers > 1 {
			var wg sync.WaitGroup
			wg.Add(workers)
			for worker := range workers {
				startY := rect.Min.Y + worker*height/workers
				endY := rect.Min.Y + (worker+1)*height/workers
				go func(y0, y1 int) {
					defer wg.Done()
					transformRGBARows(target, source, transform, y0, y1)
				}(startY, endY)
			}
			wg.Wait()
			return
		}
	}
	transformRGBARows(target, source, transform, rect.Min.Y, rect.Max.Y)
}

// colorImage 保留源采样及独立的十六位显示像素
type colorImage struct {
	*image.NRGBA64
	source image.Image
}

// pngColor 保存PNG校准空间及缺省sRGB传递函数
type pngColor struct {
	space      *pdfgo.ColorSpace
	linearSRGB bool
}

// cmykGrayAt 从原始四色采样计算蒙版灰度，不应用显示配置
// 入参: x 横坐标, y 纵坐标
// 返回: color.Gray 蒙版灰度
func (p *planarTIFFImage) cmykGrayAt(x, y int) color.Gray {
	if !image.Pt(x, y).In(p.bounds) {
		return color.Gray{}
	}
	ink := p.inksAt(x, y)
	maximum := uint32(65535)
	if p.alpha == 1 {
		maximum = uint32(ink[4])
		if maximum == 0 {
			return color.Gray{}
		}
	}
	black := maximum - min(uint32(ink[3]), maximum)
	var rgb [3]uint32
	for i := range rgb {
		rgb[i] = (maximum - min(uint32(ink[i]), maximum)) * black / maximum
		if p.alpha != 1 {
			rgb[i] = rgb[i] * uint32(ink[4]) / 65535
		}
	}
	return color.Gray{Y: uint8((19595*rgb[0] + 38470*rgb[1] + 7471*rgb[2] + 1<<15) >> 24)}
}

// transformRGBARows 按具体像素切片紧凑转换指定行区间，避免逐像素类型分发
// 入参: target 目标缓冲, source 源图像, transform 单像素颜色变换, startY 起始行, endY 结束行
func transformRGBARows(target *image.RGBA, source image.Image, transform func(color.RGBA) color.RGBA, startY, endY int) {
	rect := target.Rect
	width := rect.Dx()
	rowBytes := width * 4
	switch src := source.(type) {
	case *image.RGBA:
		if rect.In(src.Rect) {
			for y := startY; y < endY; y++ {
				dstRow := target.Pix[target.PixOffset(rect.Min.X, y):][:rowBytes]
				srcRow := src.Pix[src.PixOffset(rect.Min.X, y):][:rowBytes]
				for i := 0; i < rowBytes; i += 4 {
					p := transform(color.RGBA{R: srcRow[i], G: srcRow[i+1], B: srcRow[i+2], A: srcRow[i+3]})
					dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = p.R, p.G, p.B, p.A
				}
			}
			return
		}
	case *image.NRGBA:
		if rect.In(src.Rect) {
			for y := startY; y < endY; y++ {
				dstRow := target.Pix[target.PixOffset(rect.Min.X, y):][:rowBytes]
				srcRow := src.Pix[src.PixOffset(rect.Min.X, y):][:rowBytes]
				for i := 0; i < rowBytes; i += 4 {
					a := uint32(srcRow[i+3])
					var pixel color.RGBA
					if a == 255 {
						pixel = color.RGBA{R: srcRow[i], G: srcRow[i+1], B: srcRow[i+2], A: 255}
					} else if a != 0 {
						pixel = color.RGBA{
							R: uint8(uint32(srcRow[i]) * 257 * a / 255 >> 8),
							G: uint8(uint32(srcRow[i+1]) * 257 * a / 255 >> 8),
							B: uint8(uint32(srcRow[i+2]) * 257 * a / 255 >> 8),
							A: uint8(a),
						}
					}
					p := transform(pixel)
					dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = p.R, p.G, p.B, p.A
				}
			}
			return
		}
	case *image.RGBA64:
		if rect.In(src.Rect) {
			srcBytes := width * 8
			for y := startY; y < endY; y++ {
				dstRow := target.Pix[target.PixOffset(rect.Min.X, y):][:rowBytes]
				srcRow := src.Pix[src.PixOffset(rect.Min.X, y):][:srcBytes]
				for i, j := 0, 0; i < rowBytes; i, j = i+4, j+8 {
					p := transform(color.RGBA{R: srcRow[j], G: srcRow[j+2], B: srcRow[j+4], A: srcRow[j+6]})
					dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = p.R, p.G, p.B, p.A
				}
			}
			return
		}
	case *image.NRGBA64:
		if rect.In(src.Rect) {
			srcBytes := width * 8
			for y := startY; y < endY; y++ {
				dstRow := target.Pix[target.PixOffset(rect.Min.X, y):][:rowBytes]
				srcRow := src.Pix[src.PixOffset(rect.Min.X, y):][:srcBytes]
				for i, j := 0, 0; i < rowBytes; i, j = i+4, j+8 {
					a := uint32(srcRow[j+6])<<8 | uint32(srcRow[j+7])
					var pixel color.RGBA
					if a == 0xffff {
						pixel = color.RGBA{R: srcRow[j], G: srcRow[j+2], B: srcRow[j+4], A: 255}
					} else if a != 0 {
						r := uint32(srcRow[j])<<8 | uint32(srcRow[j+1])
						g := uint32(srcRow[j+2])<<8 | uint32(srcRow[j+3])
						b := uint32(srcRow[j+4])<<8 | uint32(srcRow[j+5])
						pixel = color.RGBA{
							R: uint8((r * a / 0xffff) >> 8),
							G: uint8((g * a / 0xffff) >> 8),
							B: uint8((b * a / 0xffff) >> 8),
							A: uint8(a >> 8),
						}
					}
					p := transform(pixel)
					dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = p.R, p.G, p.B, p.A
				}
			}
			return
		}
	case *image.Gray:
		if rect.In(src.Rect) {
			for y := startY; y < endY; y++ {
				dstRow := target.Pix[target.PixOffset(rect.Min.X, y):][:rowBytes]
				srcRow := src.Pix[src.PixOffset(rect.Min.X, y):][:width]
				for x := 0; x < width; x++ {
					v := srcRow[x]
					p := transform(color.RGBA{R: v, G: v, B: v, A: 255})
					i := x * 4
					dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = p.R, p.G, p.B, p.A
				}
			}
			return
		}
	}
	if src64, ok := source.(image.RGBA64Image); ok {
		for y := startY; y < endY; y++ {
			dstRow := target.Pix[target.PixOffset(rect.Min.X, y):][:rowBytes]
			for x := rect.Min.X; x < rect.Max.X; x++ {
				v := src64.RGBA64At(x, y)
				p := transform(color.RGBA{R: uint8(v.R >> 8), G: uint8(v.G >> 8), B: uint8(v.B >> 8), A: uint8(v.A >> 8)})
				i := (x - rect.Min.X) * 4
				dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = p.R, p.G, p.B, p.A
			}
		}
		return
	}
	for y := startY; y < endY; y++ {
		dstRow := target.Pix[target.PixOffset(rect.Min.X, y):][:rowBytes]
		for x := rect.Min.X; x < rect.Max.X; x++ {
			p := transform(color.RGBAModel.Convert(source.At(x, y)).(color.RGBA))
			i := (x - rect.Min.X) * 4
			dstRow[i], dstRow[i+1], dstRow[i+2], dstRow[i+3] = p.R, p.G, p.B, p.A
		}
	}
}

// pngColorProfile 读取PNG颜色元数据，ICC优先于sRGB及独立校准参数
// 入参: ctx 取消上下文, data 原始编码
// 返回: []byte ICC编码, *pngColor 校准参数, error 格式或配置错误
func pngColorProfile(ctx context.Context, data []byte) ([]byte, *pngColor, error) {
	const profileLimit = 16 << 20
	chunks := make(map[string][]byte)
	for at := 8; at < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if len(data)-at < 8 {
			return nil, nil, fmt.Errorf("truncated PNG chunk")
		}
		kind := string(data[at+4 : at+8])
		if kind == "IDAT" || kind == "PLTE" {
			break
		}
		if len(data)-at < 12 {
			return nil, nil, fmt.Errorf("truncated PNG chunk")
		}
		size := uint64(binary.BigEndian.Uint32(data[at:]))
		if size > uint64(len(data)-at-12) {
			return nil, nil, fmt.Errorf("invalid PNG chunk length")
		}
		end := at + 8 + int(size)
		switch kind {
		case "iCCP", "sRGB", "gAMA", "cHRM":
			if _, ok := chunks[kind]; ok {
				return nil, nil, fmt.Errorf("invalid PNG %s chunk order", kind)
			}
			if crc32.ChecksumIEEE(data[at+4:end]) != binary.BigEndian.Uint32(data[end:]) {
				return nil, nil, fmt.Errorf("invalid PNG %s checksum", kind)
			}
			chunks[kind] = data[at+8 : end]
		}
		at = end + 4
		if kind == "IEND" {
			break
		}
	}
	if chunk, ok := chunks["iCCP"]; ok {
		name := bytes.IndexByte(chunk, 0)
		if name < 1 || name > 79 || len(chunk) < name+3 || chunk[name+1] != 0 {
			return nil, nil, fmt.Errorf("invalid PNG ICC header")
		}
		for i, c := range chunk[:name] {
			if c < 32 || c > 126 && c < 161 || c == 32 && (i == 0 || i == name-1 || chunk[i-1] == 32) {
				return nil, nil, fmt.Errorf("invalid PNG ICC name")
			}
		}
		input := bytes.NewReader(chunk[name+2:])
		reader, err := zlib.NewReader(input)
		if err != nil {
			return nil, nil, err
		}
		profile, err := io.ReadAll(io.LimitReader(contextInput{ReadCloser: reader, ctx: ctx}, profileLimit+1))
		closeErr := reader.Close()
		if err != nil {
			return nil, nil, err
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		if len(profile) > profileLimit {
			return nil, nil, fmt.Errorf("PNG ICC profile exceeds size limit")
		}
		if input.Len() != 0 {
			return nil, nil, fmt.Errorf("trailing PNG ICC data")
		}
		kind := "RGB "
		if len(data) > 25 && (data[25] == 0 || data[25] == 4) {
			kind = "GRAY"
		}
		if len(profile) < 132 || string(profile[16:20]) != kind {
			return nil, nil, fmt.Errorf("ICC source model does not match PNG color space %q", kind)
		}
		return profile, nil, nil
	}
	if chunk, ok := chunks["sRGB"]; ok {
		if len(chunk) != 1 || chunk[0] > 3 {
			return nil, nil, fmt.Errorf("invalid PNG sRGB intent")
		}
		return nil, nil, nil
	}
	if len(chunks) == 0 {
		return nil, nil, nil
	}
	var rawChrm [8]uint32
	hasChrm := false
	coordinates := [8]float64{0.3127, 0.3290, 0.6400, 0.3300, 0.3000, 0.6000, 0.1500, 0.0600}
	if chunk, ok := chunks["cHRM"]; ok {
		if len(chunk) != 32 {
			return nil, nil, fmt.Errorf("invalid PNG chromaticities")
		}
		hasChrm = true
		for i := range coordinates {
			rawChrm[i] = binary.BigEndian.Uint32(chunk[i*4:])
			coordinates[i] = float64(rawChrm[i]) / 100000
		}
	}
	var rawGamma uint32
	hasGamma := false
	if chunk, ok := chunks["gAMA"]; ok {
		if len(chunk) != 4 || binary.BigEndian.Uint32(chunk) == 0 {
			return nil, nil, fmt.Errorf("invalid PNG gamma")
		}
		hasGamma = true
		rawGamma = binary.BigEndian.Uint32(chunk)
	}
	if !hasChrm && hasGamma && rawGamma == 45455 {
		return nil, nil, nil
	}
	if hasChrm && rawChrm == [8]uint32{31270, 32900, 64000, 33000, 30000, 60000, 15000, 6000} && (!hasGamma || rawGamma == 45455) {
		return nil, nil, nil
	}
	var xyz [4][3]float64
	for i := range xyz {
		x, y := coordinates[2*i], coordinates[2*i+1]
		if y <= 0 || x < 0 || x+y > 1 {
			return nil, nil, fmt.Errorf("invalid PNG chromaticities")
		}
		xyz[i] = [3]float64{x / y, 1, (1 - x - y) / y}
	}
	white := xyz[0]
	det := func(a, b, c [3]float64) float64 {
		return a[0]*(b[1]*c[2]-b[2]*c[1]) - b[0]*(a[1]*c[2]-a[2]*c[1]) + c[0]*(a[1]*b[2]-a[2]*b[1])
	}
	d := det(xyz[1], xyz[2], xyz[3])
	if math.Abs(d) < 1e-12 {
		return nil, nil, fmt.Errorf("degenerate PNG chromaticities")
	}
	scale := [3]float64{det(white, xyz[2], xyz[3]) / d, det(xyz[1], white, xyz[3]) / d, det(xyz[1], xyz[2], white) / d}
	var matrix [9]float64
	for i := range 3 {
		for j := range 3 {
			matrix[3*i+j] = xyz[i+1][j] * scale[i]
		}
	}
	gamma := [3]float64{1, 1, 1}
	linear := true
	if hasGamma {
		g := 100000 / float64(rawGamma)
		gamma = [3]float64{g, g, g}
		linear = false
	}
	space, err := pdfgo.NewCalRGBColorSpace(white, [3]float64{}, gamma, matrix)
	if err != nil {
		return nil, nil, err
	}
	return nil, &pngColor{space: space, linearSRGB: linear}, nil
}

// jpegColorProfile 读取JPEG分段ICC配置，拒绝缺段、重复及模型不匹配
// 入参: ctx 取消上下文, data JPEG编码, model 源颜色模型
// 返回: []byte 独立配置编码，未声明时为nil, error 配置或取消错误
func jpegColorProfile(ctx context.Context, data []byte, model color.Model) ([]byte, error) {
	var parts [255][]byte
	count, found, total := 0, 0, 0
	for at := 2; at < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if data[at] != 0xff {
			return nil, fmt.Errorf("invalid JPEG marker")
		}
		for at < len(data) && data[at] == 0xff {
			at++
		}
		if at == len(data) {
			return nil, fmt.Errorf("truncated JPEG marker")
		}
		marker := data[at]
		at++
		if marker == 0xda || marker == 0xd9 {
			break
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if len(data)-at < 2 {
			return nil, fmt.Errorf("truncated JPEG segment")
		}
		size := int(binary.BigEndian.Uint16(data[at:]))
		if size < 2 || size > len(data)-at {
			return nil, fmt.Errorf("invalid JPEG segment length")
		}
		segment := data[at+2 : at+size]
		at += size
		if marker != 0xe2 || !bytes.HasPrefix(segment, []byte("ICC_PROFILE\x00")) {
			continue
		}
		if len(segment) <= 14 {
			return nil, fmt.Errorf("truncated JPEG color profile")
		}
		index, n := int(segment[12]), int(segment[13])
		if index == 0 || index > n || count != 0 && count != n || parts[index-1] != nil {
			return nil, fmt.Errorf("invalid JPEG color profile sequence")
		}
		count, found = n, found+1
		parts[index-1] = segment[14:]
		total += len(segment) - 14
	}
	if found == 0 {
		return nil, nil
	}
	if found != count {
		return nil, fmt.Errorf("incomplete JPEG color profile")
	}
	profile := make([]byte, 0, total)
	for _, part := range parts[:count] {
		profile = append(profile, part...)
	}
	kind := "RGB "
	if model == color.CMYKModel {
		kind = "CMYK"
	} else if model == color.GrayModel {
		kind = "GRAY"
	}
	if len(profile) < 132 || string(profile[16:20]) != kind {
		return nil, fmt.Errorf("ICC source model does not match JPEG color space %q", kind)
	}
	return profile, nil
}

// jpegDisplayImage 统一JPEG显示变换，保留源分量并精确复用相邻同色的ICC结果
// 入参: ctx 取消上下文, source 源像素, profile 颜色配置，可为nil
// 返回: image.Image 显示像素, error 变换或取消错误
func jpegDisplayImage(ctx context.Context, source image.Image, profile *pdfgo.ColorSpace) (image.Image, error) {
	cmyk, ok := source.(*image.CMYK)
	if !ok {
		if profile != nil {
			return applyTIFFColorProfile(ctx, source, profile)
		}
		return source, nil
	}
	bounds := cmyk.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 || bounds.Dx() > math.MaxInt/8/bounds.Dy() {
		return nil, fmt.Errorf("JPEG color image dimensions exceed capacity")
	}
	pixels := image.NewNRGBA64(bounds)
	var intent pdfgo.Name
	if profile != nil {
		intent = profile.DefaultIntent()
	}
	var previous color.CMYK
	var previousPixel color.NRGBA64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			ink := cmyk.CMYKAt(x, y)
			if profile == nil {
				white := 255 - int(ink.K)
				pixels.SetNRGBA64(x, y, color.NRGBA64{
					R: uint16(max(0, white-int(ink.C))) * 257,
					G: uint16(max(0, white-int(ink.M))) * 257,
					B: uint16(max(0, white-int(ink.Y))) * 257,
					A: 65535,
				})
				continue
			}
			if previousPixel.A != 0 && ink == previous {
				pixels.SetNRGBA64(x, y, previousPixel)
				continue
			}
			values := [4]float64{float64(ink.C) / 255, float64(ink.M) / 255, float64(ink.Y) / 255, float64(ink.K) / 255}
			rgb, err := profile.SRGB(values[:], intent)
			if err != nil {
				return nil, err
			}
			previous = ink
			previousPixel = color.NRGBA64{R: colorWord(rgb[0]), G: colorWord(rgb[1]), B: colorWord(rgb[2]), A: 65535}
			pixels.SetNRGBA64(x, y, previousPixel)
		}
	}
	return &colorImage{NRGBA64: pixels, source: source}, nil
}

// colorWord 将归一化颜色舍入为十六位分量
// 入参: value 颜色分量
// 返回: uint16 十六位分量
func colorWord(value float64) uint16 {
	return uint16(math.Round(max(0, min(1, value)) * 65535))
}
