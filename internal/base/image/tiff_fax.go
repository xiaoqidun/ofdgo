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
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math/bits"

	"github.com/xiaoqidun/pdfgo"
)

// tiffFaxImage 组合独立解码的传真条带或瓦片，保留紧凑的一位像素存储
type tiffFaxImage struct {
	blocks []image.Image
	bounds image.Rectangle
	width  int
	height int
	across int
}

// decodeTIFFFax 按TIFF传真选项解码各图像块，复用PDFGo的T.4与T.6解码器
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 已校验的主图布局
// 返回: image.Image 图像, error 字段、解码或取消错误
func decodeTIFFFax(ctx context.Context, data []byte, layout TIFFLayout) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if layout.Samples != 1 || layout.compression == 3 && layout.t4 & ^uint32(7) != 0 || layout.compression == 4 && layout.t6 & ^uint32(2) != 0 {
		return nil, fmt.Errorf("invalid TIFF fax options")
	}
	var order binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		order = binary.BigEndian
	}
	offset := int(layout.Offset)
	count := int(order.Uint16(data[offset:]))
	entries := make(map[uint16][]byte, count)
	for i := range count {
		entry := data[offset+2+i*12 : offset+14+i*12]
		entries[order.Uint16(entry)] = entry
	}
	value := func(tag uint16, fallback uint32) (uint32, error) {
		entry := entries[tag]
		if entry == nil {
			return fallback, nil
		}
		if order.Uint32(entry[4:]) != 1 {
			return 0, fmt.Errorf("invalid TIFF scalar field %d", tag)
		}
		return tiffPlaneValue(data, order, entry, 0)
	}
	for _, tag := range []uint16{258, 317, 339} {
		v, err := value(tag, 1)
		if err != nil || v != 1 || entries[tag] != nil && order.Uint16(entries[tag][2:]) != 3 {
			return nil, fmt.Errorf("invalid TIFF fax sample field %d", tag)
		}
	}
	photo, err := value(262, 2)
	if err != nil || photo > 1 && photo != 4 || entries[338] != nil {
		return nil, fmt.Errorf("invalid TIFF fax color model")
	}
	width := uint32(layout.Width)
	height, err := value(278, ^uint32(0))
	if err != nil || height == 0 {
		return nil, fmt.Errorf("invalid TIFF rows per strip")
	}
	height = min(height, uint32(layout.Height))
	locations, lengths := entries[273], entries[279]
	tiled := entries[322] != nil
	if tiled {
		width, err = value(322, 0)
		if err != nil || width == 0 || width%16 != 0 {
			return nil, fmt.Errorf("invalid TIFF tile width")
		}
		height, err = value(323, 0)
		if err != nil || height == 0 || height%16 != 0 {
			return nil, fmt.Errorf("invalid TIFF tile height")
		}
		locations, lengths = entries[324], entries[325]
		if locations != nil && order.Uint16(locations[2:]) != 4 {
			return nil, fmt.Errorf("invalid TIFF tile offsets")
		}
	}
	if uint64(width) > uint64(int(^uint(0)>>1)) || uint64(height) > uint64(int(^uint(0)>>1)) || locations == nil || lengths == nil {
		return nil, fmt.Errorf("invalid TIFF fax blocks")
	}
	across := (uint64(layout.Width)-1)/uint64(width) + 1
	down := (uint64(layout.Height)-1)/uint64(height) + 1
	blocks := across * down
	if blocks != uint64(order.Uint32(locations[4:])) || blocks != uint64(order.Uint32(lengths[4:])) {
		return nil, fmt.Errorf("invalid TIFF fax block count")
	}
	result := &tiffFaxImage{bounds: image.Rect(0, 0, layout.Width, layout.Height), width: int(width), height: int(height), across: int(across)}
	for i := uint64(0); i < blocks; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		at, err := tiffPlaneValue(data, order, locations, uint32(i))
		if err != nil {
			return nil, err
		}
		size, err := tiffPlaneValue(data, order, lengths, uint32(i))
		if err != nil || size == 0 || uint64(at)+uint64(size) > uint64(len(data)) {
			return nil, fmt.Errorf("truncated TIFF fax block")
		}
		encoded := data[uint64(at) : uint64(at)+uint64(size)]
		if layout.fillOrder == 2 {
			reversed := make([]byte, len(encoded))
			for start := 0; start < len(encoded); {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				end := start + min(32768, len(encoded)-start)
				for j := start; j < end; j++ {
					reversed[j] = bits.Reverse8(encoded[j])
				}
				start = end
			}
			encoded = reversed
		}
		rows := height
		if !tiled {
			rows = min(height, uint32(uint64(layout.Height)-i*uint64(height)))
		}
		k := pdfgo.Integer(0)
		if layout.compression == 4 {
			k = -1
		} else if layout.compression == 3 && layout.t4&1 != 0 {
			k = 1
		}
		params := pdfgo.Dictionary{
			"K": k, "Columns": pdfgo.Integer(width), "Rows": pdfgo.Integer(rows),
			"EndOfBlock": pdfgo.Boolean(false), "EndOfLine": pdfgo.Boolean(layout.compression == 3),
			"EncodedByteAlign": pdfgo.Boolean(layout.compression == 2), "BlackIs1": pdfgo.Boolean(photo == 1 || photo == 4),
		}
		object := &pdfgo.Image{Width: int(width), Height: int(rows), BitsPerComponent: 1, ColorSpace: pdfgo.Name("DeviceGray"), Stream: &pdfgo.Stream{Dictionary: pdfgo.Dictionary{"Filter": pdfgo.Name("CCITTFaxDecode"), "DecodeParms": params}, Data: encoded}}
		pixels, err := object.DecodeSamplesContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("TIFF fax block %d: %w", i, err)
		}
		result.blocks = append(result.blocks, pixels)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Bounds 返回主图显示区域，不包含边缘瓦片的填充像素
// 返回: image.Rectangle 图像区域
func (s *tiffFaxImage) Bounds() image.Rectangle { return s.bounds }

// ColorModel 返回二值图像使用的灰度颜色模型
// 返回: color.Model 灰度模型
func (s *tiffFaxImage) ColorModel() color.Model { return color.GrayModel }

// At 读取所属条带或瓦片中的像素，区域外返回黑色
// 入参: x 横向像素坐标, y 纵向像素坐标
// 返回: color.Color 像素颜色
func (s *tiffFaxImage) At(x, y int) color.Color {
	if !image.Pt(x, y).In(s.bounds) {
		return color.Gray{}
	}
	block := s.blocks[(y/s.height)*s.across+x/s.width]
	return block.At(x%s.width, y%s.height)
}
