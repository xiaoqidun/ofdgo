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
	"bufio"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math/bits"
)

// DecodeConfig 读取图片尺寸，BMP和TIFF只读取布局信息，不解码像素
// 入参: input 图片输入流
// 返回: image.Config 图片配置, string 图片格式, error 头部错误
func DecodeConfig(input io.Reader) (image.Config, string, error) {
	buffer := bufio.NewReader(input)
	prefix, _ := buffer.Peek(34)
	if IsTIFF(prefix) {
		layout, err := readTIFFLayout(buffer)
		if err != nil {
			return image.Config{}, "tiff", err
		}
		size := layout.DisplaySize()
		return image.Config{Width: size.X, Height: size.Y}, "tiff", nil
	}
	if len(prefix) == 34 && string(prefix[:2]) == "BM" {
		header := binary.LittleEndian.Uint32(prefix[14:18])
		bpp := binary.LittleEndian.Uint16(prefix[28:30])
		compression := binary.LittleEndian.Uint32(prefix[30:34])
		if (bpp == 16 || bpp == 32 && compression == 3) && (header == 40 || header == 52 || header == 56 || header == 108 || header == 124) {
			size := int(header) + 14
			if header == 40 && compression == 3 {
				size += 12
			}
			data := make([]byte, size)
			if _, err := io.ReadFull(buffer, data); err != nil {
				return image.Config{}, "bmp", err
			}
			config, err := decodeBMPConfig(data)
			return config, "bmp", err
		}
	}
	return image.DecodeConfig(buffer)
}

// BMPColorMasks 读取BMP真彩色通道布局，校验位域连续性、重叠及像素范围
// 入参: data 已校验头部的BMP数据, offset 像素偏移, bpp 像素位数, compression 编码方式
// 返回: [4]bmpColorMask 红绿蓝和透明度通道, error 格式错误
func BMPColorMasks(data []byte, offset int, bpp uint16, compression uint32) ([4]bmpColorMask, error) {
	var result [4]bmpColorMask
	if bpp != 16 && bpp != 24 && bpp != 32 {
		return result, fmt.Errorf("unsupported bmp depth")
	}
	masks := [4]uint32{0xff0000, 0xff00, 0xff}
	if bpp == 16 {
		masks = [4]uint32{0x7c00, 0x3e0, 0x1f}
	}
	switch compression {
	case 0:
	case 3:
		if bpp == 24 {
			return result, fmt.Errorf("invalid bmp bitfield depth")
		}
		header := binary.LittleEndian.Uint32(data[14:18])
		if offset < 66 || len(data) < 66 || header != 40 && header < 52 {
			return result, fmt.Errorf("truncated bmp color masks")
		}
		for i := range 3 {
			masks[i] = binary.LittleEndian.Uint32(data[54+i*4:])
		}
		if header >= 56 {
			masks[3] = binary.LittleEndian.Uint32(data[66:70])
		}
	default:
		return result, fmt.Errorf("unsupported bmp compression")
	}
	var used uint32
	for i, mask := range masks {
		if mask == 0 {
			if i < 3 {
				return result, fmt.Errorf("empty bmp color mask")
			}
			continue
		}
		shift := uint(bits.TrailingZeros32(mask))
		maximum := mask >> shift
		if mask&used != 0 || maximum&(maximum+1) != 0 || uint64(mask)>>bpp != 0 {
			return result, fmt.Errorf("invalid bmp color mask")
		}
		result[i] = bmpColorMask{mask: mask, shift: shift, Max: maximum}
		used |= mask
	}
	return result, nil
}

// bmpColorMask 保存一个BMP颜色分量的位域位置和取值范围
type bmpColorMask struct {
	mask  uint32
	shift uint
	Max   uint32
}

// sample 将位域采样值映射到八位通道，缺省透明度为不透明
// 入参: value 编码像素
// 返回: uint8 通道值
func (m bmpColorMask) sample(value uint32) uint8 {
	if m.mask == 0 {
		return 255
	}
	return uint8(uint64((value&m.mask)>>m.shift) * 255 / uint64(m.Max))
}
