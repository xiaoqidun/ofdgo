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

// TextPositioner 按OFD坐标及增量定位连续字形，不依赖字体或绘图库
type TextPositioner struct {
	xs, ys, dxs, dys []float64
	direction        Point
	current          Point
	previous         float64
	index            int
}

// NewTextPositioner 创建单个TextCode的定位器，方向采用OFD角度
// 入参: code 文本定位数据, direction 阅读方向
// 返回: TextPositioner 字形定位器
func NewTextPositioner(code TextCode, direction int) TextPositioner {
	result := TextPositioner{xs: parseFloats(code.X), ys: parseFloats(code.Y), dxs: parseFloats(code.DeltaX), dys: parseFloats(code.DeltaY), direction: Point{X: 1}}
	switch direction {
	case 90:
		result.direction = Point{Y: 1}
	case 180:
		result.direction = Point{X: -1}
	case 270:
		result.direction = Point{Y: -1}
	}
	return result
}

// Next 返回下一个字形位置并保存其推进长度，显式坐标和增量优先
// 入参: advance 当前字形推进长度，单位为毫米
// 返回: Point 字形原点
func (p *TextPositioner) Next(advance float64) Point {
	i := p.index
	if i < len(p.xs) {
		p.current.X = p.xs[i]
	} else if i > 0 {
		if dx, ok := textDelta(p.dxs, i-1); ok {
			p.current.X += dx
		} else if len(p.dys) == 0 {
			p.current.X += p.previous * p.direction.X
		}
	}
	if i < len(p.ys) {
		p.current.Y = p.ys[i]
	} else if i > 0 {
		if dy, ok := textDelta(p.dys, i-1); ok {
			p.current.Y += dy
		} else if len(p.dxs) == 0 {
			p.current.Y += p.previous * p.direction.Y
		}
	}
	p.previous = advance
	p.index++
	return p.current
}
