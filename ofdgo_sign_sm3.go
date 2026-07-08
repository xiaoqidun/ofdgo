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
	"hash"
	"math/bits"
)

const (
	sm3Size      = 32
	sm3BlockSize = 64
)

var sm3Init = [8]uint32{
	0x7380166f,
	0x4914b2b9,
	0x172442d7,
	0xda8a0600,
	0xa96f30bc,
	0x163138aa,
	0xe38dee4d,
	0xb0fb0e4e,
}

// sm3Digest SM3杂凑值计算器
type sm3Digest struct {
	h   [8]uint32
	x   [sm3BlockSize]byte
	nx  int
	len uint64
}

// newSM3 创建SM3杂凑值计算器
// 返回: hash.Hash SM3杂凑值计算器
func newSM3() hash.Hash {
	d := new(sm3Digest)
	d.Reset()
	return d
}

// Reset 重置SM3状态
func (d *sm3Digest) Reset() {
	d.h = sm3Init
	d.nx = 0
	d.len = 0
}

// Size 获取SM3杂凑值长度
// 返回: int 杂凑值长度
func (d *sm3Digest) Size() int {
	return sm3Size
}

// BlockSize 获取SM3分组长度
// 返回: int 分组长度
func (d *sm3Digest) BlockSize() int {
	return sm3BlockSize
}

// Write 写入待计算数据
// 入参: p 待计算数据
// 返回: int 写入长度, error 错误信息
func (d *sm3Digest) Write(p []byte) (int, error) {
	nn := len(p)
	d.len += uint64(nn)
	if d.nx > 0 {
		n := copy(d.x[d.nx:], p)
		d.nx += n
		if d.nx == sm3BlockSize {
			sm3Block(d, d.x[:])
			d.nx = 0
		}
		p = p[n:]
	}
	if len(p) >= sm3BlockSize {
		n := len(p) &^ (sm3BlockSize - 1)
		sm3Block(d, p[:n])
		p = p[n:]
	}
	if len(p) > 0 {
		d.nx = copy(d.x[:], p)
	}
	return nn, nil
}

// Sum 返回SM3杂凑值
// 入参: in 前缀数据
// 返回: []byte 杂凑值
func (d *sm3Digest) Sum(in []byte) []byte {
	dd := *d
	hash := dd.checkSum()
	return append(in, hash[:]...)
}

// checkSum 计算SM3最终杂凑值
// 返回: [sm3Size]byte 杂凑值
func (d *sm3Digest) checkSum() [sm3Size]byte {
	lenBits := d.len << 3
	var tmp [64]byte
	tmp[0] = 0x80
	if d.nx < 56 {
		d.Write(tmp[:56-d.nx])
	} else {
		d.Write(tmp[:64+56-d.nx])
	}
	binary.BigEndian.PutUint64(tmp[:8], lenBits)
	d.Write(tmp[:8])
	var digest [sm3Size]byte
	for i, v := range d.h {
		binary.BigEndian.PutUint32(digest[i*4:], v)
	}
	return digest
}

// sm3Block 处理SM3消息分组
// 入参: d SM3杂凑值计算器, p 消息分组数据
func sm3Block(d *sm3Digest, p []byte) {
	var w [68]uint32
	var w1 [64]uint32
	for len(p) >= sm3BlockSize {
		for i := 0; i < 16; i++ {
			w[i] = binary.BigEndian.Uint32(p[i*4:])
		}
		for i := 16; i < 68; i++ {
			x := w[i-16] ^ w[i-9] ^ bits.RotateLeft32(w[i-3], 15)
			w[i] = sm3P1(x) ^ bits.RotateLeft32(w[i-13], 7) ^ w[i-6]
		}
		for i := 0; i < 64; i++ {
			w1[i] = w[i] ^ w[i+4]
		}
		a, b, c, e := d.h[0], d.h[1], d.h[2], d.h[4]
		dd, f, g, hh := d.h[3], d.h[5], d.h[6], d.h[7]
		for i := 0; i < 64; i++ {
			t := uint32(0x7a879d8a)
			if i < 16 {
				t = 0x79cc4519
			}
			ss1 := bits.RotateLeft32(bits.RotateLeft32(a, 12)+e+bits.RotateLeft32(t, i), 7)
			ss2 := ss1 ^ bits.RotateLeft32(a, 12)
			tt1 := sm3FF(i, a, b, c) + dd + ss2 + w1[i]
			tt2 := sm3GG(i, e, f, g) + hh + ss1 + w[i]
			dd = c
			c = bits.RotateLeft32(b, 9)
			b = a
			a = tt1
			hh = g
			g = bits.RotateLeft32(f, 19)
			f = e
			e = sm3P0(tt2)
		}
		d.h[0] ^= a
		d.h[1] ^= b
		d.h[2] ^= c
		d.h[3] ^= dd
		d.h[4] ^= e
		d.h[5] ^= f
		d.h[6] ^= g
		d.h[7] ^= hh
		p = p[sm3BlockSize:]
	}
}

// sm3P0 计算SM3置换函数P0
// 入参: x 输入值
// 返回: uint32 置换结果
func sm3P0(x uint32) uint32 {
	return x ^ bits.RotateLeft32(x, 9) ^ bits.RotateLeft32(x, 17)
}

// sm3P1 计算SM3置换函数P1
// 入参: x 输入值
// 返回: uint32 置换结果
func sm3P1(x uint32) uint32 {
	return x ^ bits.RotateLeft32(x, 15) ^ bits.RotateLeft32(x, 23)
}

// sm3FF 计算SM3布尔函数FF
// 入参: i 轮数, x 输入值, y 输入值, z 输入值
// 返回: uint32 计算结果
func sm3FF(i int, x, y, z uint32) uint32 {
	if i < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (x & z) | (y & z)
}

// sm3GG 计算SM3布尔函数GG
// 入参: i 轮数, x 输入值, y 输入值, z 输入值
// 返回: uint32 计算结果
func sm3GG(i int, x, y, z uint32) uint32 {
	if i < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (^x & z)
}

var _ hash.Hash = (*sm3Digest)(nil)
