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
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"math"
	"math/big"
)

// PNGDensityChunkSize 为PNG像素密度块占用的字节数
const PNGDensityChunkSize = 21

// Density 保存横纵像素密度，Unit为0表示比例、1表示每米像素数
type Density struct {
	X, Y float64
	Unit byte
}

// PageInfo 图片方向与像素密度，不改变原始编码
type PageInfo struct {
	Orientation int
	XDPI, YDPI  float64
}

// BMPDensity 提取已校验BMP头中的每米像素数
// 入参: data 含至少40字节信息头的BMP编码
// 返回: Density 像素密度, bool 密度是否可保留
func BMPDensity(data []byte) (Density, bool) {
	xdpi, ydpi := int32(binary.LittleEndian.Uint32(data[38:42])), int32(binary.LittleEndian.Uint32(data[42:46]))
	if xdpi < 0 || ydpi < 0 || (xdpi == 0) != (ydpi == 0) {
		return Density{}, false
	}
	return Density{X: float64(xdpi), Y: float64(ydpi), Unit: 1}, true
}

// TIFFConversionDensity 校验可转换的TIFF字段并提取规范化后的像素密度
// 无损模式保留色彩配置、子目录与CMYK原始编码，有损模式允许常见非关键标签、子IFD、CMYK、ICC及预乘透明度转换为显示格式
// 入参: data TIFF编码, layout 已校验的主图布局, allowColorConversion 是否允许颜色空间转换
// 返回: Density PNG像素密度, bool 是否可完整转换
func TIFFConversionDensity(data []byte, layout TIFFLayout, allowColorConversion bool) (Density, bool) {
	var density Density
	var order binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		order = binary.BigEndian
	}
	if layout.Reduced || !allowColorConversion && (layout.Next != 0 || layout.Offset != order.Uint32(data[4:])) {
		return density, false
	}
	offset := int(layout.Offset)
	count := int(order.Uint16(data[offset:]))
	unit := uint32(2)
	var resolutions [2][2]uint32
	for i := range count {
		entry := data[offset+2+i*12 : offset+14+i*12]
		tag, kind, number := order.Uint16(entry), order.Uint16(entry[2:]), order.Uint32(entry[4:])
		value := order.Uint32(entry[8:])
		if kind == 3 && number == 1 {
			value = uint32(order.Uint16(entry[8:]))
		}
		switch tag {
		case 256, 257, 258, 259, 273, 274, 277, 278, 279, 320, 322, 323, 324, 325:
		case 262:
			if value == 5 && !allowColorConversion {
				return density, false
			}
		case 269, 270, 271, 272, 280, 281, 285, 286, 287, 297, 305, 306, 315, 316, 318, 319, 330, 332, 334, 336, 700, 33432, 33723, 34377, 34665, 34675, 34853, 37724:
			if !allowColorConversion {
				return density, false
			}
		case 254:
			if value != 0 {
				return density, false
			}
		case 255:
			if value != 1 {
				return density, false
			}
		case 266:
			if kind != 3 || number != 1 || value != 1 && value != 2 {
				return density, false
			}
		case 284:
			if kind != 3 || number != 1 || value != 1 && value != 2 {
				return density, false
			}
		case 292, 293:
			mask := uint32(7)
			if tag == 293 {
				mask = 2
			}
			if kind != 4 || number != 1 || value & ^mask != 0 {
				return density, false
			}
		case 317:
			if kind != 3 || number != 1 || value != 1 && value != 2 {
				return density, false
			}
		case 338:
			if kind != 3 || number != 1 || value != 2 && (!allowColorConversion || value != 1) {
				return density, false
			}
		case 339:
			maxChannels := uint32(4)
			if allowColorConversion {
				maxChannels = 5
			}
			if kind != 3 || number == 0 || number > maxChannels {
				return density, false
			}
			values := entry[8:]
			if number > 2 {
				if uint64(value)+uint64(number)*2 > uint64(len(data)) {
					return density, false
				}
				values = data[value:]
			}
			for j := uint32(0); j < number; j++ {
				if order.Uint16(values[j*2:]) != 1 {
					return density, false
				}
			}
		case 282, 283:
			if kind != 5 || number != 1 || uint64(value)+8 > uint64(len(data)) {
				return density, false
			}
			resolutions[tag-282] = [2]uint32{order.Uint32(data[value:]), order.Uint32(data[value+4:])}
		case 296:
			if kind != 3 || number != 1 || value < 1 || value > 3 {
				return density, false
			}
			unit = value
		default:
			return density, false
		}
	}
	if resolutions == [2][2]uint32{} {
		return density, true
	}
	x, y := resolutions[0], resolutions[1]
	if x[0] == 0 || x[1] == 0 || y[0] == 0 || y[1] == 0 {
		return density, false
	}
	if unit == 1 {
		a, b := uint64(x[0])*uint64(y[1]), uint64(y[0])*uint64(x[1])
		divisor, next := a, b
		for next != 0 {
			divisor, next = next, divisor%next
		}
		density.X, density.Y = float64(a/divisor), float64(b/divisor)
	} else {
		factor := 1 / 0.0254
		if unit == 3 {
			factor = 100
		}
		density = Density{X: float64(x[0]) / float64(x[1]) * factor, Y: float64(y[0]) / float64(y[1]) * factor, Unit: 1}
	}
	if math.Round(density.X) < 1 || math.Round(density.Y) < 1 || math.Round(density.X) > math.MaxInt32 || math.Round(density.Y) > math.MaxInt32 {
		return density, false
	}
	if layout.Orientation >= 5 {
		density.X, density.Y = density.Y, density.X
	}
	return density.Scale(layout.size(), layout.DisplaySize())
}

// PageMetadataContext 读取图片方向与密度，扫描目录或数据段时响应取消
// TIFF方向由解码器处理，JPEG的EXIF密度优先于JFIF
// 入参: ctx 取消上下文, data 图片数据, format 图像格式, dpi 缺少物理密度时使用的DPI
// 返回: PageInfo 方向与横纵DPI, error 元数据或取消错误
func PageMetadataContext(ctx context.Context, data []byte, format string, dpi float64) (PageInfo, error) {
	info := PageInfo{Orientation: 1, XDPI: dpi, YDPI: dpi}
	if err := ctx.Err(); err != nil {
		return info, err
	}
	var exif []byte
	var directory uint32
	scaleX, scaleY := 1.0, 1.0
	if format == "tiff" {
		layout, err := ReadTIFFLayoutContext(ctx, bytes.NewReader(data))
		if err != nil {
			return info, err
		}
		directory = layout.Offset
		exif = data
		original, display := layout.size(), layout.DisplaySize()
		scaleX, scaleY = float64(display.X)/float64(original.X), float64(display.Y)/float64(original.Y)
	}
	if format == "bmp" && len(data) >= 54 && binary.LittleEndian.Uint32(data[14:18]) >= 40 {
		x := int32(binary.LittleEndian.Uint32(data[38:42]))
		y := int32(binary.LittleEndian.Uint32(data[42:46]))
		if x > 0 && y > 0 {
			info.XDPI, info.YDPI = float64(x)*0.0254, float64(y)*0.0254
		}
	}
	if format == "png" {
		for offset := 8; offset+12 <= len(data); {
			if err := ctx.Err(); err != nil {
				return info, err
			}
			n := uint64(binary.BigEndian.Uint32(data[offset:]))
			if n > uint64(len(data)-offset-12) {
				return info, fmt.Errorf("truncated PNG chunk")
			}
			end := offset + 8 + int(n)
			kind, payload := string(data[offset+4:offset+8]), data[offset+8:end]
			if kind == "pHYs" || kind == "eXIf" {
				if crc32.ChecksumIEEE(data[offset+4:end]) != binary.BigEndian.Uint32(data[end:]) {
					return info, fmt.Errorf("invalid PNG %s checksum", kind)
				}
			}
			if kind == "pHYs" && len(payload) == 9 {
				x, y := float64(binary.BigEndian.Uint32(payload)), float64(binary.BigEndian.Uint32(payload[4:]))
				if x > 0 && y > 0 {
					if payload[8] == 1 {
						info.XDPI, info.YDPI = x*0.0254, y*0.0254
					} else if payload[8] == 0 {
						info.YDPI = dpi * y / x
					}
				}
			} else if kind == "eXIf" {
				exif = payload
			}
			offset = end + 4
			if kind == "IEND" {
				break
			}
		}
	} else if format == "jpeg" {
		for offset := 2; offset < len(data); {
			if err := ctx.Err(); err != nil {
				return info, err
			}
			if data[offset] != 0xff {
				return info, fmt.Errorf("invalid JPEG marker")
			}
			for offset < len(data) && data[offset] == 0xff {
				offset++
			}
			if offset >= len(data) {
				break
			}
			marker := data[offset]
			offset++
			if marker == 0xda || marker == 0xd9 {
				break
			}
			if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
				continue
			}
			if offset+2 > len(data) {
				return info, fmt.Errorf("truncated JPEG marker")
			}
			n := int(binary.BigEndian.Uint16(data[offset:]))
			if n < 2 || n > len(data)-offset {
				return info, fmt.Errorf("truncated JPEG segment")
			}
			payload := data[offset+2 : offset+n]
			if marker == 0xe0 && len(payload) >= 14 && bytes.Equal(payload[:5], []byte("JFIF\x00")) {
				x, y := float64(binary.BigEndian.Uint16(payload[8:])), float64(binary.BigEndian.Uint16(payload[10:]))
				if x > 0 && y > 0 {
					switch payload[7] {
					case 0:
						info.YDPI = dpi * y / x
					case 1:
						info.XDPI, info.YDPI = x, y
					case 2:
						info.XDPI, info.YDPI = x*2.54, y*2.54
					}
				}
			} else if marker == 0xe1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
				exif = payload[6:]
			}
			offset += n
		}
	}
	if len(exif) != 0 {
		orientation, x, y, err := imageExifMetadata(exif, dpi, directory)
		if err != nil {
			return info, err
		}
		info.Orientation = orientation
		if (format == "jpeg" || format == "tiff") && x > 0 && y > 0 {
			info.XDPI, info.YDPI = x, y
		}
		if format == "tiff" {
			if orientation >= 5 {
				info.XDPI, info.YDPI = info.YDPI, info.XDPI
			}
			info.Orientation = 1
		}
	}
	info.XDPI, info.YDPI = info.XDPI*scaleX, info.YDPI*scaleY
	return info, ctx.Err()
}

// WithJPEGDensity 在JPEG头后写入或更新JFIF密度段，保留源编码不变
// 入参: data 完整JPEG编码, density 像素密度
// 返回: []byte 带密度的编码, bool 密度是否可在JFIF范围内表示
func WithJPEGDensity(data []byte, density Density) ([]byte, bool) {
	if !IsJPEG(data) {
		return nil, false
	}
	rx, ry := math.Round(density.X), math.Round(density.Y)
	if !(rx >= 1 && ry >= 1 && rx <= math.MaxInt32 && ry <= math.MaxInt32) {
		return nil, false
	}
	unit := byte(0)
	var dx, dy uint64
	if density.Unit == 0 {
		ratio := new(big.Rat).SetFrac64(int64(rx), int64(ry))
		dx, dy = ratio.Num().Uint64(), ratio.Denom().Uint64()
	} else {
		ix, iy := uint64(rx), uint64(ry)
		if ix%100 == 0 && iy%100 == 0 {
			unit, dx, dy = 2, ix/100, iy/100
		} else {
			unit = 1
			dx = max(1, uint64(math.Round(density.X*0.0254)))
			dy = max(1, uint64(math.Round(density.Y*0.0254)))
		}
	}
	if dx > 65535 || dy > 65535 {
		return nil, false
	}
	if len(data) >= 20 && data[2] == 0xff && data[3] == 0xe0 && binary.BigEndian.Uint16(data[4:6]) >= 16 && bytes.Equal(data[6:11], []byte("JFIF\x00")) {
		output := bytes.Clone(data)
		output[13] = unit
		binary.BigEndian.PutUint16(output[14:16], uint16(dx))
		binary.BigEndian.PutUint16(output[16:18], uint16(dy))
		return output, true
	}
	segment := []byte{0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, unit, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(segment[12:14], uint16(dx))
	binary.BigEndian.PutUint16(segment[14:16], uint16(dy))
	output := make([]byte, 0, len(data)+len(segment))
	output = append(output, data[:2]...)
	output = append(output, segment...)
	output = append(output, data[2:]...)
	return output, true
}

// WithPNGDensity 在无密度块的PNG头后插入pHYs，保留源编码不变
// 入参: data 完整PNG编码, density 像素密度
// 返回: []byte 带密度的编码, bool 密度是否可完整保留
func WithPNGDensity(data []byte, density Density) ([]byte, bool) {
	x, y := math.Round(density.X), math.Round(density.Y)
	if !(x >= 1 && y >= 1 && x <= math.MaxInt32 && y <= math.MaxInt32) {
		return nil, false
	}
	chunk := make([]byte, PNGDensityChunkSize)
	binary.BigEndian.PutUint32(chunk, 9)
	copy(chunk[4:], "pHYs")
	binary.BigEndian.PutUint32(chunk[8:], uint32(x))
	binary.BigEndian.PutUint32(chunk[12:], uint32(y))
	chunk[16] = density.Unit
	binary.BigEndian.PutUint32(chunk[17:], crc32.ChecksumIEEE(chunk[4:17]))
	output := make([]byte, 0, len(data)+PNGDensityChunkSize)
	output = append(output, data[:33]...)
	output = append(output, chunk...)
	output = append(output, data[33:]...)
	return output, true
}

// imageExifMetadata 只读取主图IFD中的方向和密度，不跟随缩略图与其他目录
// 入参: data TIFF或EXIF数据, dpi 无绝对密度单位时的横向DPI，零值不换算, directory 目录偏移，零值使用首目录
// 返回: int EXIF方向值，未声明时为1, float64 横向DPI, float64 纵向DPI, error 元数据错误
func imageExifMetadata(data []byte, dpi float64, directory uint32) (int, float64, float64, error) {
	invalid := fmt.Errorf("invalid EXIF image metadata")
	if len(data) < 8 {
		return 0, 0, 0, invalid
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, 0, 0, invalid
	}
	if order.Uint16(data[2:]) != 42 {
		return 0, 0, 0, invalid
	}
	offset := uint64(order.Uint32(data[4:]))
	if directory != 0 {
		offset = uint64(directory)
	}
	if offset < 8 || offset+2 > uint64(len(data)) {
		return 0, 0, 0, invalid
	}
	count := uint64(order.Uint16(data[offset:]))
	if count*12+offset+6 > uint64(len(data)) {
		return 0, 0, 0, invalid
	}
	orientation, unit := 1, 2
	var x, y float64
	for i := uint64(0); i < count; i++ {
		entry := data[offset+2+i*12 : offset+14+i*12]
		tag, kind, count := order.Uint16(entry), order.Uint16(entry[2:]), order.Uint32(entry[4:])
		switch tag {
		case 0x112, 0x128:
			if kind != 3 || count != 1 {
				return 0, 0, 0, invalid
			}
			value := int(order.Uint16(entry[8:]))
			if tag == 0x112 {
				if value < 1 || value > 8 {
					return 0, 0, 0, invalid
				}
				orientation = value
			} else {
				unit = value
			}
		case 0x11a, 0x11b:
			at := uint64(order.Uint32(entry[8:]))
			if kind != 5 || count != 1 || at+8 > uint64(len(data)) {
				return 0, 0, 0, invalid
			}
			n, d := order.Uint32(data[at:]), order.Uint32(data[at+4:])
			if n > 0 && d > 0 {
				if tag == 0x11a {
					x = float64(n) / float64(d)
				} else {
					y = float64(n) / float64(d)
				}
			}
		}
	}
	if unit == 3 {
		x, y = x*2.54, y*2.54
	} else if unit == 1 && dpi > 0 && x > 0 && y > 0 {
		x, y = dpi, dpi*y/x
	} else if unit != 2 {
		x, y = 0, 0
	}
	return orientation, x, y, nil
}

// Scale 按最终像素尺寸保留绝对密度或精确相对比例
// 入参: before 原始像素尺寸, after 最终像素尺寸
// 返回: Density 缩放后密度, bool 相对比例是否可完整保留
func (density Density) Scale(before, after image.Point) (Density, bool) {
	hasDensity := density.X != 0 || density.Y != 0
	if hasDensity && before != after {
		if before.X <= 0 || before.Y <= 0 || after.X <= 0 || after.Y <= 0 || !(density.X > 0 && density.Y > 0) || math.IsInf(density.X, 0) || math.IsInf(density.Y, 0) {
			return Density{}, false
		}
		if density.Unit == 0 {
			ratio := new(big.Rat).Quo(new(big.Rat).SetFloat64(density.X), new(big.Rat).SetFloat64(density.Y))
			ratio.Mul(ratio, new(big.Rat).SetFrac64(int64(after.X), int64(after.Y)))
			ratio.Mul(ratio, new(big.Rat).SetFrac64(int64(before.Y), int64(before.X)))
			if ratio.Num().BitLen() > 31 || ratio.Denom().BitLen() > 31 {
				return Density{}, false
			}
			density.X, density.Y = float64(ratio.Num().Int64()), float64(ratio.Denom().Int64())
		} else {
			density.X *= float64(after.X) / float64(before.X)
			density.Y *= float64(after.Y) / float64(before.Y)
		}
	}
	return density, true
}
