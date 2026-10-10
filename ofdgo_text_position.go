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
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// TextPositioner 按OFD坐标及增量定位连续字形，不依赖字体或绘图库
type TextPositioner struct {
	xs, ys, dxs, dys textPositionValues
	current          Point
	started          bool
}

// textPositionValues 按需读取定位数值及重复位移，不展开重复数组
type textPositionValues struct {
	data       string
	value      float64
	repeat     int
	compressed bool
	present    bool
}

// textCodeOrigins 按标准继承文本段原点，首段缺失坐标时以零横坐标和字体上升高度容错恢复
// 入参: codes 文本段, ascent 对象坐标系下的字体上升高度
// 返回: []TextCode 定位文本段，不修改原始数据
func textCodeOrigins(codes []TextCode, ascent float64) []TextCode {
	for i, code := range codes {
		if code.X != "" && code.Y != "" {
			continue
		}
		result := slices.Clone(codes)
		var x, y string
		if i > 0 {
			x, y = codes[i-1].X, codes[i-1].Y
		} else {
			x, y = "0", ofdNumber(ascent)
		}
		for j := i; j < len(result); j++ {
			if result[j].X == "" {
				result[j].X = x
			}
			if result[j].Y == "" {
				result[j].Y = y
			}
			x, y = result[j].X, result[j].Y
		}
		return result
	}
	return codes
}

// NewTextPositioner 创建单个TextCode的定位器，缺省位移不改变字形原点
// 入参: code 文本定位数据
// 返回: TextPositioner 字形定位器
func NewTextPositioner(code TextCode) TextPositioner {
	return TextPositioner{
		xs:  textPositionValues{data: code.X, compressed: strings.Contains(code.X, "g")},
		ys:  textPositionValues{data: code.Y, compressed: strings.Contains(code.Y, "g")},
		dxs: textPositionValues{data: code.DeltaX, compressed: strings.Contains(code.DeltaX, "g")},
		dys: textPositionValues{data: code.DeltaY, compressed: strings.Contains(code.DeltaY, "g")},
	}
}

// Next 根据坐标与位移返回下一个字形原点
// 返回: Point 字形原点
func (p *TextPositioner) Next() Point {
	if p.started {
		if p.dxs.repeat > 0 || p.dxs.data != "" {
			p.dxs.next()
		}
		if p.dys.repeat > 0 || p.dys.data != "" {
			p.dys.next()
		}
		if p.dxs.present {
			p.current.X += p.dxs.value
		}
		if p.dys.present {
			p.current.Y += p.dys.value
		}
	}
	if p.xs.repeat > 0 || p.xs.data != "" {
		if x, ok := p.xs.next(); ok {
			p.current.X = x
		}
	}
	if p.ys.repeat > 0 || p.ys.data != "" {
		if y, ok := p.ys.next(); ok {
			p.current.Y = y
		}
	}
	p.started = true
	return p.current
}

// next 读取下一个定位数值
// 返回: float64 数值, bool 是否存在
func (v *textPositionValues) next() (float64, bool) {
	if v.repeat > 0 {
		v.repeat--
		return v.value, true
	}
	if v.data == "" {
		return v.value, false
	}
	return v.read()
}

// read 解析剩余定位字段并保存重复计数
// 返回: float64 数值, bool 是否存在
func (v *textPositionValues) read() (float64, bool) {
	count, pending := 0, false
	for field := v.field(); field != ""; field = v.field() {
		if v.compressed && field == "g" {
			pending = true
			continue
		}
		if pending {
			var err error
			count, err = strconv.Atoi(field)
			if err != nil {
				count = 0
			}
			pending = false
			continue
		}
		value, err := strconv.ParseFloat(field, 64)
		if count > 0 {
			v.value, v.repeat, v.present = value, count-1, true
			return value, true
		}
		if err == nil {
			v.value, v.present = value, true
			return value, true
		}
	}
	return v.value, false
}

// field 读取定位数组中的下一个字段
// 返回: string 数值或重复标记，无剩余字段时为空
func (v *textPositionValues) field() string {
	start := -1
	for end, r := range v.data {
		if unicode.IsSpace(r) || !v.compressed && r == ',' {
			if start >= 0 {
				field := v.data[start:end]
				v.data = v.data[end:]
				return field
			}
		} else if start < 0 {
			start = end
		}
	}
	var field string
	if start >= 0 {
		field = v.data[start:]
	}
	v.data = ""
	return field
}
