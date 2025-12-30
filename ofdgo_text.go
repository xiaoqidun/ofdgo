// Copyright 2025 肖其顿 (XIAO QI DUN)
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
	"image/color"
	"strconv"
	"strings"
)

// parseColor 解析颜色字符串
// 入参: val 颜色值(R G B)
// 返回: color.Color 颜色对象
func parseColor(val string) color.Color {
	parts := strings.Fields(val)
	if len(parts) >= 3 {
		r, _ := strconv.Atoi(parts[0])
		g, _ := strconv.Atoi(parts[1])
		b, _ := strconv.Atoi(parts[2])
		return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
	}
	return color.Black
}

// GetDeltaX 获取X轴偏移量数组
// 返回: []float64 偏移量数组
func (tc *TextCode) GetDeltaX() []float64 {
	return parseFloats(tc.DeltaX)
}

// GetDeltaY 获取Y轴偏移量数组
// 返回: []float64 偏移量数组
func (tc *TextCode) GetDeltaY() []float64 {
	return parseFloats(tc.DeltaY)
}
