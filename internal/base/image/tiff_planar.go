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
	"image"
	"image/color"
	"io"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// tiffPlaneInput 将单分量目录附加到只读视图，像素仍从原始编码读取
type tiffPlaneInput struct {
	source, directory []byte
	position          int64
	order             binary.ByteOrder
	ctx               context.Context
}

// planarTIFFImage 组合颜色与Alpha分量，保留交错步长并按需映射调色板
type planarTIFFImage struct {
	planes         []image.Image
	bounds         image.Rectangle
	depth          uint32
	packed         [5]uint32
	offsets        [5]int
	alpha          uint32
	gray           bool
	cmyk           bool
	dotRange       [4][2]uint16
	palette        []color.NRGBA64
	stride         int
	invert         bool
	predictorWidth int
}

// decodeTIFFPixels 按存储方式解码指定目录并统一颜色和显示方向
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 目录布局
// 返回: image.Image 图像, error 解码或取消错误
func decodeTIFFPixels(ctx context.Context, data []byte, layout TIFFLayout) (image.Image, error) {
	profile, err := readTIFFColorProfile(ctx, data, layout)
	if err != nil {
		return nil, err
	}
	var ranges [][2]int64
	if layout.compression < 2 || layout.compression > 4 {
		ranges, err = tiffBitOrderRanges(ctx, data, layout)
		if err != nil {
			return nil, err
		}
	}
	var pixels image.Image
	if layout.compression >= 2 && layout.compression <= 4 {
		pixels, err = decodeTIFFFax(ctx, data, layout)
		if err != nil {
			return nil, err
		}
	} else if layout.needsSampleView() {
		result, directories, err := tiffPlaneDirectories(ctx, data, layout)
		if err != nil {
			return nil, err
		}
		for _, directory := range directories {
			input := &tiffPlaneInput{source: data, directory: directory, order: binary.LittleEndian, ctx: ctx}
			if data[0] == 'M' {
				input.order = binary.BigEndian
			}
			plane, err := decodeTIFFBitOrder(input, ranges)
			if err != nil {
				return nil, err
			}
			if result.predictorWidth != 0 && (result.stride > 1 || result.packed[len(result.planes)] != 0) {
				if err := restoreTIFFPrediction(ctx, plane, max(result.stride, 1), result.predictorWidth, result.packed[len(result.planes)]); err != nil {
					return nil, err
				}
			}
			result.planes = append(result.planes, plane)
		}
		pixels = result
	} else {
		input := newTIFFDirectoryInput(data, layout.Offset)
		input.ctx = ctx
		var err error
		pixels, err = decodeTIFFBitOrder(input, ranges)
		if err != nil {
			return nil, err
		}
	}
	if profile != nil {
		pixels, err = applyTIFFColorProfile(ctx, pixels, profile)
		if err != nil {
			return nil, err
		}
	} else if cmyk, ok := pixels.(*planarTIFFImage); ok && cmyk.cmyk {
		pixels, err = applyTIFFColorProfile(ctx, pixels, &pdfgo.ColorSpace{Model: "DeviceCMYK"})
		if err != nil {
			return nil, err
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if layout.Orientation != 1 {
		pixels = &orientedTIFFImage{source: pixels, TIFFLayout: layout}
	}
	return pixels, nil
}

// tiffPlaneDirectories 为颜色、Alpha或调色板索引构建单通道目录，跳过无显示用途的独立平面
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 已校验的主图布局
// 返回: *planarTIFFImage 像素布局, [][]byte 各分量目录, error 字段或布局错误
func tiffPlaneDirectories(ctx context.Context, data []byte, layout TIFFLayout) (*planarTIFFImage, [][]byte, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	if uint64(len(data))+65536*12+8 > math.MaxUint32 {
		return nil, nil, fmt.Errorf("TIFF plane directory offset exceeds format limit")
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
	samples, err := value(277, 1)
	if err != nil || samples < 1 || samples > math.MaxUint16 {
		return nil, nil, fmt.Errorf("invalid TIFF plane count")
	}
	photo, err := value(262, math.MaxUint32)
	if err != nil || photo > 5 {
		return nil, nil, fmt.Errorf("unsupported TIFF planar color model")
	}
	compression, err := value(259, 1)
	if err != nil {
		return nil, nil, err
	}
	fill, err := value(266, 1)
	if err != nil || fill != 1 && (fill != 2 || compression != 1 && (compression < 2 || compression > 4)) {
		return nil, nil, fmt.Errorf("unsupported TIFF plane fill order")
	}
	predictor, err := value(317, 1)
	if err != nil || predictor != 1 && predictor != 2 {
		return nil, nil, fmt.Errorf("unsupported TIFF plane predictor")
	}
	result := &planarTIFFImage{bounds: image.Rect(0, 0, layout.Width, layout.Height), gray: photo < 2 || photo == 4, invert: photo == 0, cmyk: photo == 5}
	if result.cmyk {
		for _, tag := range []uint16{332, 334} {
			want := uint32(1)
			if tag == 334 {
				want = 4
			}
			got, err := value(tag, want)
			if err != nil || got != want || entries[tag] != nil && order.Uint16(entries[tag][2:]) != 3 {
				return nil, nil, fmt.Errorf("unsupported TIFF ink configuration")
			}
		}
	}
	planes := samples
	if !layout.Planar && samples > 1 {
		result.stride = int(samples)
		planes = 1
	}
	colors := uint32(3)
	if result.cmyk {
		colors = 4
	} else if photo != 2 {
		colors = 1
	}
	if samples != colors {
		entry := entries[338]
		if samples < colors || entry == nil || order.Uint16(entry[2:]) != 3 || order.Uint32(entry[4:]) != samples-colors {
			return nil, nil, fmt.Errorf("invalid TIFF extra samples")
		}
		for i := uint32(0); i < samples-colors; i++ {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}
			alpha, err := tiffPlaneValue(data, order, entry, i)
			if err != nil || alpha > 2 || alpha != 0 && result.alpha != 0 {
				return nil, nil, fmt.Errorf("unsupported TIFF extra samples")
			}
			if alpha != 0 {
				result.alpha, result.offsets[colors] = alpha, int(i)
			}
		}
	} else if entries[338] != nil {
		return nil, nil, fmt.Errorf("unexpected TIFF extra samples")
	}
	width, err := value(322, 0)
	if err != nil {
		return nil, nil, err
	}
	blocks := uint64(0)
	locations, lengths := uint16(273), uint16(279)
	if width != 0 {
		height, err := value(323, 0)
		if err != nil || height == 0 || width%16 != 0 || height%16 != 0 {
			return nil, nil, fmt.Errorf("invalid TIFF tile dimensions")
		}
		blocks = ((uint64(layout.Width)-1)/uint64(width) + 1) * ((uint64(layout.Height)-1)/uint64(height) + 1)
		locations, lengths = 324, 325
	} else {
		rows, err := value(278, math.MaxUint32)
		if err != nil || rows == 0 {
			return nil, nil, fmt.Errorf("invalid TIFF rows per strip")
		}
		blocks = (uint64(layout.Height)-1)/uint64(rows) + 1
	}
	if blocks > math.MaxUint32/uint64(planes) {
		return nil, nil, fmt.Errorf("too many TIFF plane blocks")
	}
	for _, tag := range []uint16{locations, lengths} {
		entry := entries[tag]
		if entry == nil || uint64(order.Uint32(entry[4:])) != blocks*uint64(planes) || tag == 324 && order.Uint16(entry[2:]) != 4 {
			return nil, nil, fmt.Errorf("invalid TIFF plane block count")
		}
	}
	for i := uint32(0); i < uint32(blocks)*planes; i++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		at, err := tiffPlaneValue(data, order, entries[locations], i)
		if err != nil {
			return nil, nil, err
		}
		size, err := tiffPlaneValue(data, order, entries[lengths], i)
		if err != nil || size == 0 || uint64(at)+uint64(size) > uint64(len(data)) {
			return nil, nil, fmt.Errorf("truncated TIFF plane block")
		}
	}
	for _, tag := range []uint16{258, 339} {
		if entry := entries[tag]; entry != nil && (order.Uint16(entry[2:]) != 3 || order.Uint32(entry[4:]) != samples) {
			return nil, nil, fmt.Errorf("invalid TIFF sample array")
		}
	}
	var directories [][]byte
	var firstDepth uint32
	for plane := uint32(0); plane < samples; plane++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		slot := plane
		visible := plane < colors || result.alpha != 0 && plane == colors+uint32(result.offsets[colors])
		if !visible && result.stride == 0 {
			continue
		}
		if plane >= colors {
			slot = colors
		}
		depth := uint32(1)
		if entry := entries[258]; entry != nil {
			depth, err = tiffPlaneValue(data, order, entry, plane)
		}
		if err != nil || depth != 1 && depth != 2 && depth != 4 && depth != 8 && depth != 16 {
			return nil, nil, fmt.Errorf("unsupported TIFF plane depth")
		}
		if plane == 0 {
			firstDepth = depth
		} else if result.stride > 1 && depth != firstDepth {
			return nil, nil, fmt.Errorf("unsupported TIFF mixed interleaved sample depths")
		}
		if !visible {
			continue
		}
		result.depth = max(result.depth, depth)
		if result.cmyk && plane < 4 {
			low, high := uint32(0), uint32(1<<depth-1)
			if entry := entries[336]; entry != nil {
				n := order.Uint32(entry[4:])
				if kind := order.Uint16(entry[2:]); kind != 1 && kind != 3 || n != 2 && n != 2*samples {
					return nil, nil, fmt.Errorf("invalid TIFF dot range")
				}
				i := plane * 2
				if n == 2 {
					i = 0
				}
				if order.Uint16(entry[2:]) == 1 {
					values := entry[8:]
					if n > 4 {
						at := uint64(order.Uint32(values))
						if at+uint64(n) > uint64(len(data)) {
							return nil, nil, fmt.Errorf("truncated TIFF dot range")
						}
						values = data[at : at+uint64(n)]
					}
					low, high = uint32(values[i]), uint32(values[i+1])
				} else {
					low, err = tiffPlaneValue(data, order, entry, i)
					if err != nil {
						return nil, nil, err
					}
					high, err = tiffPlaneValue(data, order, entry, i+1)
				}
				if err != nil || low == high || low > 1<<depth-1 || high > 1<<depth-1 {
					return nil, nil, fmt.Errorf("invalid TIFF dot range")
				}
			}
			result.dotRange[plane] = [2]uint16{uint16(low * 65535 / (1<<depth - 1)), uint16(high * 65535 / (1<<depth - 1))}
		}
		factor := uint64(max(result.stride, 1))
		if depth == 1 && predictor == 2 {
			result.packed[slot] = 1
		}
		if depth == 2 || depth == 4 {
			result.packed[slot] = depth
			factor *= uint64(depth)
			if compression == 2 || compression == 3 || compression == 4 {
				return nil, nil, fmt.Errorf("unsupported TIFF packed plane compression")
			}
		}
		pixelBytes := factor
		if depth > 8 {
			pixelBytes *= 2
		}
		if uint64(layout.Width)*pixelBytes > uint64(math.MaxInt)/uint64(layout.Height) || uint64(layout.Width)*factor > math.MaxUint32 || uint64(width)*factor > math.MaxUint32 {
			return nil, nil, fmt.Errorf("TIFF plane dimensions exceed format limit")
		}
		if predictor == 2 && (result.stride > 1 || depth < 8) {
			result.predictorWidth = layout.Width
			if width != 0 {
				result.predictorWidth = int(width)
			}
		}
		if entry := entries[339]; entry != nil {
			format, err := tiffPlaneValue(data, order, entry, plane)
			if err != nil || format != 1 {
				return nil, nil, fmt.Errorf("unsupported TIFF sample format")
			}
		}
		if photo == 3 && plane == 0 {
			entry := entries[320]
			if depth > 8 || entry == nil || order.Uint16(entry[2:]) != 3 || order.Uint32(entry[4:]) != 3<<depth {
				return nil, nil, fmt.Errorf("invalid TIFF color map")
			}
			n := uint32(1) << depth
			result.palette = make([]color.NRGBA64, n)
			for i := range n {
				var components [3]uint16
				for c := range components {
					v, err := tiffPlaneValue(data, order, entry, uint32(c)*n+i)
					if err != nil {
						return nil, nil, err
					}
					components[c] = uint16(v)
					if v%257 != 0 {
						result.depth = 16
					}
				}
				result.palette[i] = color.NRGBA64{R: components[0], G: components[1], B: components[2], A: 65535}
			}
		}
		if result.stride > 1 && plane != 0 {
			continue
		}
		directory := make([]byte, 2, 6+count*12)
		for i := range count {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}
			entry := bytes.Clone(data[offset+2+i*12 : offset+14+i*12])
			tag := order.Uint16(entry)
			if tag == 338 || tag == 320 || tag >= 332 && tag <= 337 || tag == 34675 {
				continue
			}
			scalar := uint32(0)
			switch tag {
			case 258:
				scalar = depth
				if result.packed[slot] != 0 {
					scalar = 1
				}
			case 256, 322:
				if factor != 1 {
					original, err := tiffPlaneValue(data, order, entry, 0)
					if err != nil {
						return nil, nil, err
					}
					order.PutUint16(entry[2:], 4)
					order.PutUint32(entry[4:], 1)
					order.PutUint32(entry[8:], original*uint32(factor))
				}
			case 262:
				scalar = 1
			case 277, 284, 339:
				scalar = 1
			case 317:
				if predictor == 2 && (result.stride > 1 || depth < 8) {
					order.PutUint16(entry[8:], 1)
				}
			case locations, lengths:
				entry, err = tiffPlaneArray(data, order, entry, uint32(blocks)*plane, uint32(blocks))
				if err != nil {
					return nil, nil, err
				}
			}
			if tag == 258 || tag == 262 || tag == 277 || tag == 284 || tag == 339 {
				order.PutUint16(entry[2:], 3)
				order.PutUint32(entry[4:], 1)
				clear(entry[8:])
				order.PutUint16(entry[8:], uint16(scalar))
			}
			directory = append(directory, entry...)
		}
		order.PutUint16(directory, uint16((len(directory)-2)/12))
		directories = append(directories, append(directory, 0, 0, 0, 0))
	}
	return result, directories, nil
}

// restoreTIFFPrediction 在解码缓冲内恢复水平差分，各行及瓦片独立计算
// 入参: ctx 取消上下文，可为nil, pixels 单通道解码视图, stride 每像素分量数, width 原始条带或瓦片宽度, packed 展开为一位采样的原位深，零表示未展开
// 返回: error 图像类型或取消错误
func restoreTIFFPrediction(ctx context.Context, pixels image.Image, stride, width int, packed uint32) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var data []byte
	var pitch, step int
	switch pixels := pixels.(type) {
	case *image.Gray:
		data, pitch, step = pixels.Pix, pixels.Stride, max(int(packed), 1)
	case *image.Gray16:
		data, pitch, step = pixels.Pix, pixels.Stride, 2
	default:
		return fmt.Errorf("unsupported TIFF predictor image")
	}
	bounds := pixels.Bounds()
	columns := bounds.Dx() / max(int(packed), 1)
	block := width * stride
	for y := range bounds.Dy() {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := data[y*pitch : y*pitch+columns*step]
		for start := 0; start < columns; {
			end := start + min(block, columns-start)
			for x := start + stride; x < end; {
				if err := ctx.Err(); err != nil {
					return err
				}
				limit := x + min(4096, end-x)
				for ; x < limit; x++ {
					at, previous := x*step, (x-stride)*step
					if packed != 0 {
						var current, prior uint16
						for bit := range step {
							current = current<<1 | uint16(row[at+bit]>>7)
							prior = prior<<1 | uint16(row[previous+bit]>>7)
						}
						value := current + prior
						for bit := range step {
							row[at+bit] = byte(value>>uint(step-1-bit)&1) * 255
						}
					} else if step == 2 {
						value := binary.BigEndian.Uint16(row[at:]) + binary.BigEndian.Uint16(row[previous:])
						binary.BigEndian.PutUint16(row[at:], value)
					} else {
						row[at] += row[previous]
					}
				}
			}
			start = end
		}
	}
	return ctx.Err()
}

// tiffPlaneValue 读取SHORT或LONG数组中的一项，校验完整数组范围
// 入参: data 原编码, order 字节序, entry 目录项, index 元素序号
// 返回: uint32 元素值, error 类型或越界错误
func tiffPlaneValue(data []byte, order binary.ByteOrder, entry []byte, index uint32) (uint32, error) {
	kind, count := order.Uint16(entry[2:]), order.Uint32(entry[4:])
	if kind != 3 && kind != 4 || index >= count {
		return 0, fmt.Errorf("invalid TIFF plane array")
	}
	size := uint64(2)
	if kind == 4 {
		size = 4
	}
	values := entry[8:]
	if size*uint64(count) > 4 {
		at := uint64(order.Uint32(values))
		if at+size*uint64(count) > uint64(len(data)) {
			return 0, fmt.Errorf("truncated TIFF plane array")
		}
		values = data[at:]
	}
	if size == 2 {
		return uint32(order.Uint16(values[uint64(index)*size:])), nil
	}
	return order.Uint32(values[uint64(index)*size:]), nil
}

// tiffPlaneArray 将块索引缩减为指定分量的范围，较长数组继续借用原编码
// 入参: data 原编码, order 字节序, entry 目录项副本, first 起始序号, count 元素数
// 返回: []byte 修改后的目录项, error 类型或越界错误
func tiffPlaneArray(data []byte, order binary.ByteOrder, entry []byte, first, count uint32) ([]byte, error) {
	if _, err := tiffPlaneValue(data, order, entry, first+count-1); err != nil {
		return nil, err
	}
	size := uint32(2)
	if order.Uint16(entry[2:]) == 4 {
		size = 4
	}
	if uint64(count)*uint64(size) <= 4 {
		var values [4]byte
		for i := uint32(0); i < count; i++ {
			value, _ := tiffPlaneValue(data, order, entry, first+i)
			if size == 2 {
				order.PutUint16(values[i*size:], uint16(value))
			} else {
				order.PutUint32(values[i*size:], value)
			}
		}
		copy(entry[8:], values[:])
	} else {
		order.PutUint32(entry[8:], order.Uint32(entry[8:])+first*size)
	}
	order.PutUint32(entry[4:], count)
	return entry, nil
}

// DecodedPixelBytes 估算每像素解码缓冲，低位深分量按逐位展开后的字节数计量
// 返回: uint64 每像素预算字节数
func (layout TIFFLayout) DecodedPixelBytes() uint64 {
	samples := uint64(layout.Samples)
	profile := uint64(0)
	if layout.profileSize != 0 {
		profile = 8
	}
	if layout.Planar {
		if layout.photometric == 5 && samples > 4 {
			return 20 + profile
		}
		return 16 + profile
	}
	bytes := uint64(1)
	if layout.depth == 2 || layout.depth == 4 {
		bytes = uint64(layout.depth)
	} else if layout.depth > 8 {
		bytes = 2
	}
	return max(uint64(8), samples*bytes) + profile
}

// Read 从组合视图顺序读取
// 入参: data 接收缓冲
// 返回: int 字节数, error 读取或取消错误
func (r *tiffPlaneInput) Read(data []byte) (int, error) {
	n, err := r.ReadAt(data, r.position)
	r.position += int64(n)
	return n, err
}

// ReadAt 从原始编码或附加目录读取并替换首目录指针
// 入参: data 接收缓冲, offset 文件偏移
// 返回: int 字节数, error 读取或取消错误
func (r *tiffPlaneInput) ReadAt(data []byte, offset int64) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	if offset < 0 {
		return 0, fmt.Errorf("negative TIFF read offset")
	}
	start := int64(len(r.source))
	directory := start + start%2
	written := 0
	if offset < start {
		written = copy(data, r.source[offset:])
	}
	if offset+int64(written) == start && start != directory && written < len(data) {
		data[written] = 0
		written++
	}
	if at := offset + int64(written) - directory; at >= 0 && at < int64(len(r.directory)) {
		written += copy(data[written:], r.directory[at:])
	}
	var pointer [4]byte
	r.order.PutUint32(pointer[:], uint32(directory))
	for i, value := range pointer {
		if at := int64(4+i) - offset; at >= 0 && at < int64(written) {
			data[at] = value
		}
	}
	if written != len(data) {
		return written, io.EOF
	}
	return written, nil
}

// Bounds 返回存储坐标范围
// 返回: image.Rectangle 图像范围
func (p *planarTIFFImage) Bounds() image.Rectangle { return p.bounds }

// ColorModel 按透明度类型和最高分量精度返回颜色模型
// 返回: color.Model 颜色模型
func (p *planarTIFFImage) ColorModel() color.Model {
	if p.gray && p.alpha == 0 {
		if p.depth > 8 {
			return color.Gray16Model
		}
		return color.GrayModel
	}
	if p.alpha == 1 {
		if p.depth > 8 || p.cmyk {
			return color.RGBA64Model
		}
		return color.RGBAModel
	}
	if p.depth > 8 || p.cmyk {
		return color.NRGBA64Model
	}
	return color.NRGBAModel
}

// At 返回组合后的颜色，关联透明度保留预乘语义
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 像素颜色
func (p *planarTIFFImage) At(x, y int) color.Color {
	values := p.components(x, y)
	if p.alpha == 1 {
		return color.RGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
	}
	return color.NRGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
}

// NRGBA64At 读取独立透明度颜色，保留非预乘分量中的隐藏颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.NRGBA64 像素颜色
func (p *planarTIFFImage) NRGBA64At(x, y int) color.NRGBA64 {
	values := p.components(x, y)
	if p.alpha == 1 && values[3] != 65535 {
		if values[3] == 0 {
			return color.NRGBA64{}
		}
		for i := range 3 {
			values[i] = uint16(uint32(values[i]) * 65535 / uint32(values[3]))
		}
	}
	return color.NRGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
}

// RGBA64At 返回预乘十六位颜色，不分配临时颜色对象
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA64 像素颜色
func (p *planarTIFFImage) RGBA64At(x, y int) color.RGBA64 {
	values := p.components(x, y)
	if p.alpha != 1 && values[3] != 65535 {
		for i := range 3 {
			values[i] = uint16(uint32(values[i]) * uint32(values[3]) / 65535)
		}
	}
	return color.RGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
}

// components 直接读取灰度分量，保留原始透明度语义
// 入参: x 横坐标, y 纵坐标
// 返回: [4]uint16 红、绿、蓝和透明度分量
func (p *planarTIFFImage) components(x, y int) [4]uint16 {
	if !image.Pt(x, y).In(p.bounds) {
		return [4]uint16{}
	}
	if p.cmyk {
		ink := p.inksAt(x, y)
		maximum := uint32(65535)
		if p.alpha == 1 {
			maximum = uint32(ink[4])
			if maximum == 0 {
				return [4]uint16{}
			}
		}
		var components [4]float64
		for i := range components {
			components[i] = float64(min(uint32(ink[i]), maximum)) / float64(maximum)
		}
		rgb := pdfgo.DeviceCMYKToSRGB(components[0], components[1], components[2], components[3])
		var values [4]uint16
		for i := range 3 {
			values[i] = uint16(math.Round(rgb[i] * float64(maximum)))
		}
		values[3] = ink[4]
		return values
	}
	values := [4]uint16{0, 0, 0, 65535}
	count := 3
	if p.gray || len(p.palette) != 0 {
		count = 1
	}
	if p.alpha != 0 {
		count++
	}
	for i := range count {
		value := p.sampleAt(i, x, y)
		if i == 0 && p.invert {
			value = 65535 - value
		}
		if len(p.palette) != 0 && i == 0 {
			c := p.palette[uint32(value)*uint32(len(p.palette)-1)/65535]
			values = [4]uint16{c.R, c.G, c.B, c.A}
		} else if p.gray && i == 0 {
			values[0], values[1], values[2] = value, value, value
		} else if p.gray || len(p.palette) != 0 {
			values[3] = value
		} else {
			values[i] = value
		}
	}
	if p.alpha == 1 && values[3] == 0 {
		return [4]uint16{}
	}
	return values
}

// sampleAt 读取一个分量并扩展为十六位，交错布局复用同一像素缓冲
// 入参: i 分量序号, x 横坐标, y 纵坐标
// 返回: uint16 归一化采样值
func (p *planarTIFFImage) sampleAt(i, x, y int) uint16 {
	plane, sx := p.planes[0], x
	if p.stride > 1 {
		sx = x*p.stride + i + p.offsets[i]
	} else {
		plane = p.planes[i]
	}
	switch plane := plane.(type) {
	case *image.Gray:
		if bits := p.packed[i]; bits != 0 {
			var value uint16
			for bit := range int(bits) {
				value = value<<1 | uint16(plane.GrayAt(sx*int(bits)+bit, y).Y>>7)
			}
			return value * (65535 / uint16(1<<bits-1))
		}
		return uint16(plane.GrayAt(sx, y).Y) * 257
	case *image.Gray16:
		return plane.Gray16At(sx, y).Y
	default:
		v, _, _, _ := plane.At(sx, y).RGBA()
		return uint16(v)
	}
}

// inksAt 按网点范围读取CMYK覆盖量，范围外采样按端点截断，透明度独立归一化
// 入参: x 横坐标, y 纵坐标
// 返回: [5]uint16 青、品红、黄、黑及透明度
func (p *planarTIFFImage) inksAt(x, y int) [5]uint16 {
	values := [5]uint16{0, 0, 0, 0, 65535}
	for i := range 4 {
		value := int64(p.sampleAt(i, x, y))
		low, high := int64(p.dotRange[i][0]), int64(p.dotRange[i][1])
		values[i] = uint16(max(0, min(65535, (value-low)*65535/(high-low))))
	}
	if p.alpha != 0 {
		values[4] = p.sampleAt(4, x, y)
	}
	return values
}

// needsSampleView 判断是否需通过分量视图处理存储布局，供尺寸读取和像素解码共用
// 返回: bool 是否需要分量视图
func (layout TIFFLayout) needsSampleView() bool {
	return layout.mask || layout.auxiliary || layout.photometric == 5 || layout.Planar && layout.Samples > 1 || layout.Samples == 2 || (layout.lowDepth || layout.depth < 8) && (layout.Samples > 1 || layout.depth > 1 || layout.predictor == 2)
}
