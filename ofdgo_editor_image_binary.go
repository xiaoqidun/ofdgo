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
	"encoding/binary"
	"image"
	"io"

	"github.com/xiaoqidun/jbig2"
)

// pngBinaryProbeLimit 限制黑白检查读取的压缩数据与块头总量
const pngBinaryProbeLimit = 4 << 20

// pngBinaryProbe 按需读取连续的IDAT块，不复制压缩数据
type pngBinaryProbe struct {
	data      []byte
	payload   []byte
	remaining int
}

// editorBinaryImage 尝试纯黑白无损编码，不二值化，不改变透明度，无体积收益时保留原图
// 入参: data PNG图片数据, img 已解码像素，为nil时读取原图
// 返回: []byte 更小的JBIG2数据，不适用时为nil
func editorBinaryImage(data []byte, img image.Image) []byte {
	if img == nil {
		if pngNonBinaryImage(data) {
			return nil
		}
		var err error
		img, _, err = image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil
		}
	}
	var output bytes.Buffer
	if err := jbig2.Encode(&output, img, &jbig2.Options{MaxPageBytes: uint64(len(data))}); err != nil || output.Len() >= len(data) {
		return nil
	}
	return output.Bytes()
}

// pngNonBinaryImage 检查PNG前部像素是否包含灰阶、彩色或透明度，仅用于排除黑白压缩
// 入参: data PNG图片数据
// 返回: bool 已发现非不透明黑白像素，无法判定时为false
func pngNonBinaryImage(data []byte) bool {
	if len(data) < 33 || string(data[:8]) != "\x89PNG\r\n\x1a\n" || binary.BigEndian.Uint32(data[8:]) != 13 || string(data[12:16]) != "IHDR" {
		return false
	}
	header := data[16:29]
	width, height := binary.BigEndian.Uint32(header), binary.BigEndian.Uint32(header[4:])
	depth, kind := header[8], header[9]
	if width == 0 || height == 0 || (depth != 8 && depth != 16) || header[10] != 0 || header[11] != 0 || header[12] != 0 {
		return false
	}
	colors, channels := 0, 0
	switch kind {
	case 0:
		colors, channels = 1, 1
	case 2:
		colors, channels = 3, 3
	case 4:
		colors, channels = 1, 2
	case 6:
		colors, channels = 3, 4
	default:
		return false
	}
	colors *= int(depth / 8)
	channels *= int(depth / 8)
	size := uint64(width) * uint64(channels)
	if size > 256<<10 {
		return false
	}
	probe := pngBinaryProbe{data: data[33:], remaining: pngBinaryProbeLimit}
	for len(probe.data) >= 12 && string(probe.data[4:8]) != "IDAT" {
		length := uint64(binary.BigEndian.Uint32(probe.data)) + 12
		if length > uint64(len(probe.data)) || length > uint64(probe.remaining) {
			return false
		}
		probe.data = probe.data[int(length):]
		probe.remaining -= int(length)
	}
	stream, err := zlib.NewReader(&probe)
	if err != nil {
		return false
	}
	defer stream.Close()
	row := make([]byte, int(size)+1)
	previous := make([]byte, int(size))
	rows := min(int64(height), 64, (1<<20)/int64(len(row)))
	for index := int64(0); index < rows; index++ {
		if _, err := io.ReadFull(stream, row); err != nil || !pngBinaryProbeFilter(row[1:], previous, channels, row[0]) {
			return false
		}
		for offset := 1; offset < len(row); offset += channels {
			value := row[offset]
			if value != 0 && value != 255 {
				return true
			}
			for _, sample := range row[offset : offset+colors] {
				if sample != value {
					return true
				}
			}
			for _, sample := range row[offset+colors : offset+channels] {
				if sample != 255 {
					return true
				}
			}
		}
		copy(previous, row[1:])
	}
	return false
}

// Read 读取有限的IDAT数据，边界或长度无效时停止检查
// 入参: output 读取缓冲区
// 返回: int 读取字节数, error 读取错误
func (p *pngBinaryProbe) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	for len(p.payload) == 0 {
		if len(p.data) < 12 || p.remaining < 12 || string(p.data[4:8]) != "IDAT" {
			return 0, io.EOF
		}
		length := uint64(binary.BigEndian.Uint32(p.data))
		if length > uint64(len(p.data)-12) {
			return 0, io.ErrUnexpectedEOF
		}
		p.payload = p.data[8 : 8+int(length)]
		p.data = p.data[12+int(length):]
		p.remaining -= 12
	}
	if p.remaining == 0 {
		return 0, io.EOF
	}
	count := copy(output[:min(len(output), p.remaining)], p.payload)
	p.payload = p.payload[count:]
	p.remaining -= count
	return count, nil
}

// pngBinaryProbeFilter 还原黑白检查所需的扫描行
// 入参: row 当前行, previous 前一行, stride 像素字节数, filter PNG过滤方式
// 返回: bool 过滤方式有效
func pngBinaryProbeFilter(row, previous []byte, stride int, filter byte) bool {
	if filter > 4 {
		return false
	}
	for index := range row {
		var left, upperLeft byte
		if index >= stride {
			left, upperLeft = row[index-stride], previous[index-stride]
		}
		up := previous[index]
		switch filter {
		case 1:
			row[index] += left
		case 2:
			row[index] += up
		case 3:
			row[index] += byte((uint16(left) + uint16(up)) / 2)
		case 4:
			a, b := int(up)-int(upperLeft), int(left)-int(upperLeft)
			c := a + b
			if a < 0 {
				a = -a
			}
			if b < 0 {
				b = -b
			}
			if c < 0 {
				c = -c
			}
			switch {
			case a <= b && a <= c:
				row[index] += left
			case b <= c:
				row[index] += up
			default:
				row[index] += upperLeft
			}
		}
	}
	return true
}
