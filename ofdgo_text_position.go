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
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// TextPositioner 按OFD坐标、阅读方向及增量定位连续字形，不依赖字体或绘图库
type TextPositioner struct {
	xs, ys, dxs, dys textPositionValues
	xSign, ySign     float64
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

// textLineScanner 顺序判断文本段换行，复用继承坐标和字符到字形的变换
type textLineScanner struct {
	size       float64
	enabled    bool
	started    bool
	firstX     string
	previous   TextCode
	offset     int
	pending    []CGTransform
	transforms map[int][2]int
}

// NewTextPositioner 创建单个TextCode的定位器，按阅读方向应用增量位移
// 入参: code 文本定位数据, direction 阅读方向角度
// 返回: TextPositioner 字形定位器
func NewTextPositioner(code TextCode, direction int) TextPositioner {
	xSign, ySign := 1.0, 1.0
	if direction == 180 {
		xSign = -1
	} else if direction == 270 {
		ySign = -1
	}
	return TextPositioner{
		xs:    textPositionValues{data: code.X, compressed: strings.Contains(code.X, "g")},
		ys:    textPositionValues{data: code.Y, compressed: strings.Contains(code.Y, "g")},
		dxs:   textPositionValues{data: code.DeltaX, compressed: strings.Contains(code.DeltaX, "g")},
		dys:   textPositionValues{data: code.DeltaY, compressed: strings.Contains(code.DeltaY, "g")},
		xSign: xSign,
		ySign: ySign,
	}
}

// newTextLineScanner 创建独立的换行扫描器，不修改文字对象
// 入参: obj 定位文字
// 返回: textLineScanner 扫描状态
func newTextLineScanner(obj TextObject) textLineScanner {
	return textLineScanner{size: obj.Size, enabled: len(obj.TextCode) > 1 && obj.ReadDirection == 0 && obj.CharDirection == 0, previous: TextCode{X: "0", Y: "0"}, pending: obj.CGTransform}
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
			x, y = firstPositionCoord(codes[i-1].X), firstPositionCoord(codes[i-1].Y)
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
			x, y = firstPositionCoord(result[j].X), firstPositionCoord(result[j].Y)
		}
		return result
	}
	return codes
}

// firstPositionCoord 提取坐标序列的首个原点分量
// 入参: value 坐标属性文本
// 返回: string 首个坐标值
func firstPositionCoord(value string) string {
	values := textPositionValues{data: value, compressed: strings.Contains(value, "g")}
	if number, ok := values.next(); ok {
		return ofdNumber(number)
	}
	return value
}

// textCodeGlyphCount 按文本段的字符起点统计实际字形数量
// 入参: code 文本段, offset 对象内字符起点, transforms 已解析的字符到字形变换
// 返回: int 字形数量
func textCodeGlyphCount(code TextCode, offset int, transforms map[int][2]int) int {
	if code.Index != "" {
		count := 0
		for _, field := range strings.Fields(code.Index) {
			first, last, interval := strings.Cut(field, "-")
			start, err := strconv.ParseUint(first, 10, 16)
			if err != nil {
				continue
			}
			if !interval {
				count++
				continue
			}
			end, err := strconv.ParseUint(last, 10, 16)
			if err == nil && end >= start {
				count += int(end-start) + 1
			}
		}
		return count
	}
	count := textCodeRuneCount(code.Value)
	if len(transforms) == 0 || count == 0 {
		return count
	}
	glyphs := 0
	for i := 0; i < count; {
		if transform, ok := transforms[offset+i]; ok && transform[0] <= count-i {
			i += transform[0]
			glyphs += transform[1]
		} else {
			i++
			glyphs++
		}
	}
	return glyphs
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
			p.current.X += p.xSign * p.dxs.value
		}
		if p.dys.present {
			p.current.Y += p.ySign * p.dys.value
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
			repeat := count
			count = 0
			if err == nil && finite(value) {
				v.value, v.repeat, v.present = value, repeat-1, true
				return value, true
			}
			continue
		}
		if err == nil && finite(value) {
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
		if unicode.IsSpace(r) || r == ',' {
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

// glyphCount 首次需要字形范围时解析变换，后续文本段复用结果
// 入参: code 文本段, offset 对象内字符起点
// 返回: int 字形数量
func (s *textLineScanner) glyphCount(code TextCode, offset int) int {
	if len(s.pending) != 0 {
		s.transforms = make(map[int][2]int, len(s.pending))
	}
	for _, transform := range s.pending {
		if transform.CodePosition < 0 {
			continue
		}
		glyphs := len(parseInts(transform.Glyphs))
		if transform.GlyphCount > 0 {
			glyphs = min(glyphs, transform.GlyphCount)
		}
		if glyphs > 0 {
			s.transforms[transform.CodePosition] = [2]int{max(1, transform.CodeCount), glyphs}
		}
	}
	s.pending = nil
	return textCodeGlyphCount(code, offset, s.transforms)
}

// next 继承文本段原点并根据实际字形范围判断是否换行
// 入参: code 按文档顺序传入的文本段
// 返回: bool 当前段前是否换行
func (s *textLineScanner) next(code TextCode) bool {
	if !s.enabled {
		return false
	}
	previous, offset := s.previous, s.offset
	if s.started && (len(s.pending) != 0 || len(s.transforms) != 0) {
		s.offset += textCodeRuneCount(previous.Value)
	}
	if code.X == "" {
		code.X = previous.X
	}
	if code.Y == "" {
		code.Y = previous.Y
	}
	s.previous = code
	if !s.started {
		s.started, s.firstX = true, code.X
		return false
	}
	x, ex := strconv.ParseFloat(code.X, 64)
	start, es := strconv.ParseFloat(s.firstX, 64)
	y, ey := strconv.ParseFloat(code.Y, 64)
	prev, ep := strconv.ParseFloat(previous.Y, 64)
	if ex != nil || es != nil || ey != nil || ep != nil {
		return false
	}
	if x == start && y != prev {
		return true
	}
	left, err := strconv.ParseFloat(previous.X, 64)
	if err != nil || s.size <= 0 || y-prev < s.size/2 || previous.DeltaY != "" || code.DeltaY != "" {
		return false
	}
	count := s.glyphCount(previous, offset)
	if count == 0 {
		return false
	}
	right := left + s.size
	positioner := NewTextPositioner(previous, 0)
	for range count {
		right = math.Max(right, positioner.Next().X+s.size)
	}
	return x <= right
}
