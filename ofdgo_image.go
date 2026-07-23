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

	// 注册GIF图片解码器
	_ "image/gif"

	canvasimage "github.com/tdewolff/canvas/image"

	// 注册扩展图片解码器
	_ "github.com/xiaoqidun/jbig2"
	_ "golang.org/x/image/bmp"
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

// decodeBMPConfig 解码 BMP 尺寸
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

// decodeBMPImage 解码 BMP 图片
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

// parseBMPHeader 解析 BMP 文件头
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
