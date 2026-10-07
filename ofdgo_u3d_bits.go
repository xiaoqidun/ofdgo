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
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/bits"
	"unicode/utf8"
)

// u3dValues 读取U3D未压缩字段，不允许越过当前块
type u3dValues struct {
	data []byte
	pos  int
	err  error
}

// u3dBits 按ECMA-363第10章逆解16位算术区间，字节内按低位优先读取
type u3dBits struct {
	data            []byte
	pos             uint64
	low, high, code uint32
	err             error
}

// u3dHistogram 保存一个动态压缩上下文的频次及前缀和
type u3dHistogram struct {
	counts []uint32
	tree   []uint32
	total  uint32
}

// take 读取定长字段，失败后不继续推进位置
// 入参: size 字节数
// 返回: []byte 字段数据，失败时为空
func (r *u3dValues) take(size int) []byte {
	if r.err != nil {
		return nil
	}
	if size < 0 || size > len(r.data)-r.pos {
		r.err = io.ErrUnexpectedEOF
		return nil
	}
	value := r.data[r.pos : r.pos+size]
	r.pos += size
	return value
}

// u8 读取一个未压缩字节
// 返回: byte 数值
func (r *u3dValues) u8() byte {
	value := r.take(1)
	if value == nil {
		return 0
	}
	return value[0]
}

// u32 读取小端32位整数
// 返回: uint32 数值
func (r *u3dValues) u32() uint32 {
	value := r.take(4)
	if value == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(value)
}

// f32 读取有限单精度数值
// 返回: float32 数值
func (r *u3dValues) f32() float32 {
	value := math.Float32frombits(r.u32())
	if !finite(float64(value)) && r.err == nil {
		r.err = fmt.Errorf("invalid U3D floating-point value")
	}
	return value
}

// text 读取带16位字节长度的UTF-8字符串
// 返回: string 文本
func (r *u3dValues) text() string {
	length := r.take(2)
	if length == nil {
		return ""
	}
	data := r.take(int(binary.LittleEndian.Uint16(length)))
	if r.err == nil && !utf8.Valid(data) {
		r.err = fmt.Errorf("invalid U3D UTF-8 string")
	}
	return string(data)
}

// done 检查字段是否恰好覆盖当前块
// 返回: error 读取或多余字段错误
func (r *u3dValues) done() error {
	if r.err != nil {
		return r.err
	}
	if r.pos != len(r.data) {
		return fmt.Errorf("unexpected U3D block data")
	}
	return nil
}

// newU3DBits 初始化当前块的独立算术解码状态
// 入参: data 块数据
// 返回: *u3dBits 解码器
func newU3DBits(data []byte) *u3dBits {
	r := &u3dBits{data: data, high: 0xffff}
	for range 16 {
		r.code = r.code<<1 | r.bit()
	}
	return r
}

// bit 读取低位优先比特，区间预读最多允许16个末尾零位
// 返回: uint32 比特
func (r *u3dBits) bit() uint32 {
	if r.err != nil {
		return 0
	}
	limit := uint64(len(r.data)) * 8
	var value uint32
	if r.pos < limit {
		value = uint32(r.data[r.pos/8]>>(r.pos%8)) & 1
	} else if r.pos >= limit+16 {
		r.err = io.ErrUnexpectedEOF
		return 0
	}
	r.pos++
	return value
}

// cumulative 求当前编码值在上下文累计频次中的位置
// 入参: total 总频次
// 返回: uint32 累计频次位置
func (r *u3dBits) cumulative(total uint32) uint32 {
	if r.err != nil {
		return 0
	}
	if total == 0 || r.code < r.low || r.code > r.high {
		r.err = fmt.Errorf("invalid U3D arithmetic interval")
		return 0
	}
	return uint32((uint64(r.code-r.low+1)*uint64(total) - 1) / uint64(r.high-r.low+1))
}

// advance 收缩符号区间并执行同位及下溢归一化
// 入参: start 累计频次, count 符号频次, total 总频次
func (r *u3dBits) advance(start, count, total uint32) {
	if r.err != nil {
		return
	}
	if count == 0 || start >= total || count > total-start {
		r.err = fmt.Errorf("invalid U3D symbol frequency")
		return
	}
	width := uint64(r.high-r.low) + 1
	r.high = r.low + uint32(width*uint64(start+count)/uint64(total)) - 1
	r.low += uint32(width * uint64(start) / uint64(total))
	for r.err == nil {
		var offset uint32
		switch {
		case r.high < 0x8000:
		case r.low >= 0x8000:
			offset = 0x8000
		case r.low >= 0x4000 && r.high < 0xc000:
			offset = 0x4000
		default:
			return
		}
		r.low = (r.low - offset) << 1
		r.high = (r.high-offset)<<1 | 1
		r.code = (r.code-offset)<<1 | r.bit()
	}
}

// byteValue 逆解静态256符号上下文中的反序字节
// 返回: byte 原始字节
func (r *u3dBits) byteValue() byte {
	value := r.cumulative(256)
	r.advance(value, 1, 256)
	return bits.Reverse8(byte(value))
}

// u32 逆解四个小端字节，不重置压缩区间
// 返回: uint32 原始整数
func (r *u3dBits) u32() uint32 {
	var value uint32
	for shift := range 4 {
		value |= uint32(r.byteValue()) << (8 * shift)
	}
	return value
}

// index 读取静态上下文的网格索引，大范围按标准使用未压缩整数
// 入参: count 索引范围
// 返回: uint32 索引
func (r *u3dBits) index(count uint32) uint32 {
	if count > 0x3ffe {
		return r.u32()
	}
	value := r.cumulative(count)
	r.advance(value, 1, count)
	return value
}

// dynamic 读取动态上下文数值，逃逸后的字面量仍使用当前算术区间
// 入参: h 频次上下文
// 返回: uint32 原始整数
func (r *u3dBits) dynamic(h *u3dHistogram) uint32 {
	if h.total == 0 {
		h.counts = make([]uint32, 1)
		h.tree = make([]uint32, 2)
		h.add(0)
	}
	index := r.cumulative(h.total)
	symbol, start := h.find(index)
	if r.err != nil {
		return 0
	}
	r.advance(start, h.counts[symbol], h.total)
	h.add(symbol)
	if symbol != 0 {
		return symbol - 1
	}
	value := r.u32()
	if value < 0xffff && r.err == nil {
		h.add(value + 1)
	}
	return value
}

// find 以树状前缀和定位符号，避免按符号数量线性扫描
// 入参: position 累计频次位置
// 返回: uint32 符号, uint32 符号前缀频次
func (h *u3dHistogram) find(position uint32) (uint32, uint32) {
	index, prefix := 0, uint32(0)
	for step := 1 << (bits.Len(uint(len(h.tree))) - 1); step != 0; step >>= 1 {
		next := index + step
		if next < len(h.tree) && h.tree[next] <= position-prefix {
			prefix += h.tree[next]
			index = next
		}
	}
	return uint32(index), prefix
}

// add 更新动态频次，并按ECMA-363阈值缩减历史频次
// 入参: symbol 符号，超过16位的值不加入上下文
func (h *u3dHistogram) add(symbol uint32) {
	if symbol > 0xffff {
		return
	}
	if h.total >= 0x1fff {
		for index := range h.counts {
			h.counts[index] >>= 1
		}
		h.counts[0]++
		h.rebuild()
	}
	if int(symbol) >= len(h.counts) {
		length := min(65536, max(int(symbol)+1, len(h.counts)*2))
		h.counts = append(h.counts, make([]uint32, length-len(h.counts))...)
		h.rebuild()
	}
	h.counts[symbol]++
	h.total++
	for index := int(symbol) + 1; index < len(h.tree); index += index & -index {
		h.tree[index]++
	}
}

// rebuild 在线性时间内重建动态频次索引
func (h *u3dHistogram) rebuild() {
	h.tree = make([]uint32, len(h.counts)+1)
	h.total = 0
	for index, count := range h.counts {
		h.total += count
		i := index + 1
		h.tree[i] += count
		if next := i + (i & -i); next < len(h.tree) {
			h.tree[next] += h.tree[i]
		}
	}
}
