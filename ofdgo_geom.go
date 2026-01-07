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
	"strconv"
	"strings"
)

// Box 矩形区域
type Box struct {
	X, Y, W, H float64
}

// ParseBox 解析Box字符串
// 入参: s 字符串
// 返回: Box 矩形对象, error 错误信息
func ParseBox(s string) (Box, error) {
	parts := strings.Fields(s)
	if len(parts) < 4 {
		return Box{}, nil
	}
	x, _ := strconv.ParseFloat(parts[0], 64)
	y, _ := strconv.ParseFloat(parts[1], 64)
	w, _ := strconv.ParseFloat(parts[2], 64)
	h, _ := strconv.ParseFloat(parts[3], 64)
	return Box{X: x, Y: y, W: w, H: h}, nil
}

// Matrix 2D仿射变换矩阵
type Matrix struct {
	a, b, c, d, e, f float64
}

// IdentityMatrix 单位矩阵
var IdentityMatrix = Matrix{1, 0, 0, 1, 0, 0}

// NewMatrix 解析CTM字符串
// 入参: s 字符串
// 返回: Matrix 矩阵对象
func NewMatrix(s string) Matrix {
	floats := parseFloats(s)
	if len(floats) != 6 {
		return IdentityMatrix
	}
	return Matrix{
		a: floats[0], b: floats[1],
		c: floats[2], d: floats[3],
		e: floats[4], f: floats[5],
	}
}

// Multiply 矩阵乘法 (m * o)
// 入参: o 右侧矩阵
// 返回: Matrix 结果矩阵
func (m Matrix) Multiply(o Matrix) Matrix {
	return Matrix{
		a: m.a*o.a + m.c*o.b,
		b: m.b*o.a + m.d*o.b,
		c: m.a*o.c + m.c*o.d,
		d: m.b*o.c + m.d*o.d,
		e: m.a*o.e + m.c*o.f + m.e,
		f: m.b*o.e + m.d*o.f + m.f,
	}
}

// Transform 应用变换矩阵
// 入参: x X坐标, y Y坐标
// 返回: float64 变换后X, float64 变换后Y
func (m Matrix) Transform(x, y float64) (float64, float64) {
	nx := m.a*x + m.c*y + m.e
	ny := m.b*x + m.d*y + m.f
	return nx, ny
}

// YScale 获取Y轴缩放比例
// 返回: float64 缩放比例
func (m Matrix) YScale() float64 {
	return math.Sqrt(m.c*m.c + m.d*m.d)
}

// parseFloats 解析浮点数数组
// 入参: s 字符串
// 返回: []float64 浮点数数组
func parseFloats(s string) []float64 {
	if s == "" {
		return nil
	}
	if strings.Contains(s, "g") {
		return parseFloatsWithG(s)
	}
	s = strings.ReplaceAll(s, ",", " ")
	parts := strings.Fields(s)
	result := make([]float64, 0, len(parts))
	for _, p := range parts {
		if v, err := strconv.ParseFloat(p, 64); err == nil {
			result = append(result, v)
		}
	}
	return result
}

// parseFloatsWithG 解析带g的压缩浮点数数组
// 入参: s 字符串
// 返回: []float64 浮点数数组
func parseFloatsWithG(s string) []float64 {
	parts := strings.Fields(s)
	var result []float64
	gFlag := false
	gCount := 0
	for _, p := range parts {
		if p == "g" {
			gFlag = true
			continue
		}
		if gFlag {
			gCount, _ = strconv.Atoi(p)
			gFlag = false
			continue
		}
		if gCount > 0 {
			v, _ := strconv.ParseFloat(p, 64)
			for j := 0; j < gCount; j++ {
				result = append(result, v)
			}
			gCount = 0
		} else {
			if v, err := strconv.ParseFloat(p, 64); err == nil {
				result = append(result, v)
			}
		}
	}
	return result
}
