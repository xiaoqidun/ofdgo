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

import "slices"

// TextPositioner 按OFD坐标及增量定位连续字形，不依赖字体或绘图库
type TextPositioner struct {
	xs, ys, dxs, dys []float64
	current          Point
	index            int
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
	return TextPositioner{xs: parseFloats(code.X), ys: parseFloats(code.Y), dxs: parseFloats(code.DeltaX), dys: parseFloats(code.DeltaY)}
}

// Next 根据坐标与位移返回下一个字形原点
// 返回: Point 字形原点
func (p *TextPositioner) Next() Point {
	i := p.index
	if i < len(p.xs) {
		p.current.X = p.xs[i]
	} else if i > 0 {
		if dx, ok := textDelta(p.dxs, i-1); ok {
			p.current.X += dx
		}
	}
	if i < len(p.ys) {
		p.current.Y = p.ys[i]
	} else if i > 0 {
		if dy, ok := textDelta(p.dys, i-1); ok {
			p.current.Y += dy
		}
	}
	p.index++
	return p.current
}
