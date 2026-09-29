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
	"compress/zlib"
	"context"
	"encoding/ascii85"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/ps"
	"github.com/xiaoqidun/pdfgo"
)

// epsImageRenderer 在EPS输出端优化图片，不修改页面及渲染缓存
type epsImageRenderer struct {
	*ps.PS
	writer  io.Writer
	options CompressionOptions
	ctx     context.Context
	err     error
}

// RenderImage 写出Flate或更小的JPEG图片，保留显示尺寸与变换
// 入参: img 图片, matrix 绘制变换
func (r *epsImageRenderer) RenderImage(img image.Image, matrix canvas.Matrix) {
	if r.err != nil {
		return
	}
	if dpi := r.options.ImageDPI(); dpi > 0 {
		original := img.Bounds().Size()
		target := compressionImageSize(math.Hypot(matrix[0][0], matrix[1][0])*float64(original.X), math.Hypot(matrix[0][1], matrix[1][1])*float64(original.Y), dpi)
		img, r.err = pdfgo.ResizeImage(r.ctx, img, target)
		if r.err != nil {
			return
		}
		size := img.Bounds().Size()
		if size.X > 0 && size.Y > 0 {
			matrix = matrix.Scale(float64(original.X)/float64(size.X), float64(original.Y)/float64(size.Y))
		}
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	pixels := make([]byte, width*3)
	var encoded bytes.Buffer
	compressor, _ := zlib.NewWriterLevel(&encoded, zlib.BestCompression)
	opaque := true
	for y := 0; y < height; y++ {
		if r.err = r.ctx.Err(); r.err != nil {
			return
		}
		clear(pixels)
		for x := 0; x < width; x++ {
			red, green, blue, alpha := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			if alpha != 65535 {
				opaque = false
			}
			if alpha != 0 {
				offset := x * 3
				pixels[offset], pixels[offset+1], pixels[offset+2] = byte(red*65535/alpha>>8), byte(green*65535/alpha>>8), byte(blue*65535/alpha>>8)
			}
		}
		if _, r.err = compressor.Write(pixels); r.err != nil {
			return
		}
	}
	if r.err = compressor.Close(); r.err != nil {
		return
	}
	data := encoded.Bytes()
	filter := "FlateDecode"
	if opaque && r.options.Mode == CompressionLossy {
		var candidate bytes.Buffer
		if r.err = jpeg.Encode(&candidate, img, &jpeg.Options{Quality: r.options.ImageQuality()}); r.err != nil {
			return
		}
		optimized, err := pdfgo.OptimizeJPEG(r.ctx, candidate.Bytes())
		if err != nil {
			r.err = err
			return
		}
		if len(optimized) < len(data) {
			data, filter = optimized, "DCTDecode"
		}
	}
	m := matrix.Scale(float64(width), float64(height))
	_, r.err = fmt.Fprintf(r.writer, " gsave /DeviceRGB setcolorspace [%s %s %s %s %s %s] concat <</ImageType 1 /BitsPerComponent 8 /Decode [0 1 0 1 0 1] /Interpolate true /Width %d /Height %d /ImageMatrix [%d 0 0 -%d 0 %d] /DataSource currentfile /ASCII85Decode filter /%s filter>>image\n", ofdNumber(m[0][0]), ofdNumber(m[1][0]), ofdNumber(m[0][1]), ofdNumber(m[1][1]), ofdNumber(m[0][2]), ofdNumber(m[1][2]), width, height, width, height, height, filter)
	if r.err != nil {
		return
	}
	writer := ascii85.NewEncoder(r.writer)
	if _, r.err = writer.Write(data); r.err != nil {
		return
	}
	if r.err = writer.Close(); r.err != nil {
		return
	}
	_, r.err = io.WriteString(r.writer, "~>\n grestore")
}
