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
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math/bits"
	"slices"
	"sort"

	"golang.org/x/image/tiff"
)

// tiffBitOrderInput 仅在读取像素块时反转字节内位序，保留目录和原始数据
type tiffBitOrderInput struct {
	source   io.ReaderAt
	ranges   [][2]int64
	position int64
}

// tiffBitOrderRanges 收集低位优先的未压缩一位图像块，压缩传真位序由解码器处理
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 已校验的主图布局
// 返回: [][2]int64 按偏移排序且不重叠的字节区间, error 字段或布局错误
func tiffBitOrderRanges(ctx context.Context, data []byte, layout TIFFLayout) ([][2]int64, error) {
	if layout.fillOrder != 2 {
		return nil, nil
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
	compression := entries[259]
	method := uint16(1)
	if compression != nil {
		if order.Uint16(compression[2:]) != 3 || order.Uint32(compression[4:]) != 1 {
			return nil, fmt.Errorf("invalid TIFF compression field")
		}
		method = order.Uint16(compression[8:])
	}
	if method == 3 || method == 4 {
		return nil, nil
	}
	if method != 1 {
		return nil, fmt.Errorf("unsupported TIFF fill order for compression %d", method)
	}
	if entry := entries[258]; entry != nil {
		if order.Uint16(entry[2:]) != 3 || order.Uint32(entry[4:]) != layout.Samples {
			return nil, fmt.Errorf("invalid TIFF sample depths")
		}
		for i := uint32(0); i < layout.Samples; i++ {
			depth, err := tiffPlaneValue(data, order, entry, i)
			if err != nil || depth != 1 {
				return nil, fmt.Errorf("unsupported TIFF fill order for sample depth")
			}
		}
	}
	locations, lengths := entries[273], entries[279]
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
	rows, err := value(278, ^uint32(0))
	if err != nil || rows == 0 {
		return nil, fmt.Errorf("invalid TIFF rows per strip")
	}
	expected := (uint64(layout.Height)-1)/uint64(rows) + 1
	if entries[322] != nil {
		locations, lengths = entries[324], entries[325]
		width, err := value(322, 0)
		if err != nil || width == 0 || width%16 != 0 {
			return nil, fmt.Errorf("invalid TIFF tile width")
		}
		height, err := value(323, 0)
		if err != nil || height == 0 || height%16 != 0 {
			return nil, fmt.Errorf("invalid TIFF tile height")
		}
		expected = ((uint64(layout.Width)-1)/uint64(width) + 1) * ((uint64(layout.Height)-1)/uint64(height) + 1)
		if locations != nil && order.Uint16(locations[2:]) != 4 {
			return nil, fmt.Errorf("invalid TIFF tile offsets")
		}
	}
	if layout.Planar {
		if expected > uint64(^uint32(0))/uint64(layout.Samples) {
			return nil, fmt.Errorf("too many TIFF image blocks")
		}
		expected *= uint64(layout.Samples)
	}
	if locations == nil || lengths == nil {
		return nil, fmt.Errorf("missing TIFF block arrays")
	}
	blocks := order.Uint32(locations[4:])
	if blocks == 0 || uint64(blocks) != expected || order.Uint32(lengths[4:]) != blocks {
		return nil, fmt.Errorf("invalid TIFF block count")
	}
	var ranges [][2]int64
	for i := uint32(0); i < blocks; i++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		at, err := tiffPlaneValue(data, order, locations, i)
		if err != nil {
			return nil, err
		}
		size, err := tiffPlaneValue(data, order, lengths, i)
		if err != nil || size == 0 || uint64(at)+uint64(size) > uint64(len(data)) {
			return nil, fmt.Errorf("truncated TIFF image block")
		}
		ranges = append(ranges, [2]int64{int64(at), int64(at) + int64(size)})
	}
	slices.SortFunc(ranges, func(a, b [2]int64) int { return cmp.Compare(a[0], b[0]) })
	merged := ranges[:0]
	for _, span := range ranges {
		if len(merged) != 0 && span[0] <= merged[len(merged)-1][1] {
			merged[len(merged)-1][1] = max(merged[len(merged)-1][1], span[1])
		} else {
			merged = append(merged, span)
		}
	}
	return merged, nil
}

// decodeTIFFBitOrder 使用只读视图校正位序，不分配整幅编码的副本
// 入参: input 原始解码视图, ranges 需要反转位序的像素区间
// 返回: image.Image 解码图像, error 解码错误
func decodeTIFFBitOrder(input interface {
	io.Reader
	io.ReaderAt
}, ranges [][2]int64) (image.Image, error) {
	if len(ranges) == 0 {
		return tiff.Decode(input)
	}
	return tiff.Decode(&tiffBitOrderInput{source: input, ranges: ranges})
}

// Read 顺序读取并校正像素字节的位序
// 入参: data 接收缓冲
// 返回: int 读取字节数, error 读取错误
func (r *tiffBitOrderInput) Read(data []byte) (int, error) {
	n, err := r.ReadAt(data, r.position)
	r.position += int64(n)
	return n, err
}

// ReadAt 按文件偏移读取，只反转实际读取到的像素字节
// 入参: data 接收缓冲, offset 文件偏移
// 返回: int 读取字节数, error 读取错误
func (r *tiffBitOrderInput) ReadAt(data []byte, offset int64) (int, error) {
	n, err := r.source.ReadAt(data, offset)
	index := sort.Search(len(r.ranges), func(i int) bool { return r.ranges[i][1] > offset })
	for ; index < len(r.ranges) && r.ranges[index][0] < offset+int64(n); index++ {
		span := r.ranges[index]
		for i := max(int64(0), span[0]-offset); i < min(int64(n), span[1]-offset); i++ {
			data[i] = bits.Reverse8(data[i])
		}
	}
	return n, err
}
