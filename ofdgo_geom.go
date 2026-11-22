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
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
)

// maxCompressedFloats 限制单个带g数组展开后的最大元素数量
const maxCompressedFloats = 1 << 20

// Box 矩形区域
type Box struct {
	X, Y, W, H float64
}

// Point 平面坐标点
type Point struct {
	X, Y float64
}

// Matrix 2D仿射变换矩阵
type Matrix struct {
	a, b, c, d, e, f float64
}

// IdentityMatrix 返回2D单位仿射变换矩阵
// 返回: Matrix 单位矩阵
func IdentityMatrix() Matrix {
	return Matrix{1, 0, 0, 1, 0, 0}
}

// ParseBox 解析Box字符串
// 入参: s 字符串
// 返回: Box 矩形对象, error 错误信息
func ParseBox(s string) (Box, error) {
	var values [4]float64
	i := 0
	for part := range strings.FieldsSeq(s) {
		if i == len(values) {
			return Box{}, fmt.Errorf("invalid box: %s", s)
		}
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return Box{}, fmt.Errorf("invalid box: %s", s)
		}
		values[i] = value
		i++
	}
	if i != len(values) {
		return Box{}, fmt.Errorf("invalid box: %s", s)
	}
	return Box{X: values[0], Y: values[1], W: values[2], H: values[3]}, nil
}

// MatrixFromValues 从a、b、c、d、e、f构造矩阵，不经过文本格式化
// 入参: values 矩阵分量
// 返回: Matrix 变换矩阵
func MatrixFromValues(values [6]float64) Matrix {
	return Matrix{a: values[0], b: values[1], c: values[2], d: values[3], e: values[4], f: values[5]}
}

// NewMatrix 解析CTM字符串
// 入参: s 字符串
// 返回: Matrix 矩阵对象
func NewMatrix(s string) Matrix {
	floats := parseFloats(s)
	if len(floats) != 6 {
		return IdentityMatrix()
	}
	return Matrix{
		a: floats[0], b: floats[1],
		c: floats[2], d: floats[3],
		e: floats[4], f: floats[5],
	}
}

// TranslationMatrix 构造平移矩阵
// 入参: x X轴偏移, y Y轴偏移
// 返回: Matrix 平移矩阵
func TranslationMatrix(x, y float64) Matrix {
	return MatrixFromValues(basegeom.TranslationMatrix(x, y).Values())
}

// parseFloats 解析浮点数数组，支持空格与逗号分隔并过滤非有限数
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
		if v, err := strconv.ParseFloat(p, 64); err == nil && finite(v) {
			result = append(result, v)
		}
	}
	return result
}

// parseFloatsWithG 解析带g的压缩浮点数数组，支持空格与逗号分隔并过滤非有限数
// 入参: s 字符串
// 返回: []float64 浮点数数组
func parseFloatsWithG(s string) []float64 {
	var result []float64
	s = strings.ReplaceAll(s, ",", " ")
	gFlag := false
	gCount := 0
	for p := range strings.FieldsSeq(s) {
		if len(result) >= maxCompressedFloats {
			break
		}
		if p == "g" {
			gFlag = true
			continue
		}
		if gFlag {
			var err error
			gCount, err = strconv.Atoi(p)
			if err != nil || gCount < 0 {
				gCount = 0
			}
			gFlag = false
			continue
		}
		if gCount > 0 {
			if v, err := strconv.ParseFloat(p, 64); err == nil && finite(v) {
				count := min(gCount, maxCompressedFloats-len(result))
				result = slices.Grow(result, count)
				for j := 0; j < count; j++ {
					result = append(result, v)
				}
			}
			gCount = 0
		} else {
			if v, err := strconv.ParseFloat(p, 64); err == nil && finite(v) {
				result = append(result, v)
			}
		}
	}
	return result
}

// parseInts 解析整数数组
// 入参: s 字符串
// 返回: []int 整数数组
func parseInts(s string) []int {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, ",", " ")
	parts := strings.Fields(s)
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		if v, err := strconv.Atoi(p); err == nil {
			result = append(result, v)
		}
	}
	return result
}

// parsePageAreaBox 按OFD页面区域回退规则解析正有限尺寸的页面边界框
// 入参: area 页面区域声明
// 返回: Box 页面边界框, error 解析或尺寸无效错误
func parsePageAreaBox(area PageArea) (Box, error) {
	raw := cmp.Or(strings.TrimSpace(area.PhysicalBox), strings.TrimSpace(area.ApplicationBox), strings.TrimSpace(area.ContentBox), "0 0 210 297")
	box, err := ParseBox(raw)
	if err != nil || box.W <= 0 || box.H <= 0 {
		return Box{}, fmt.Errorf("invalid page box: %s", raw)
	}
	return box, nil
}

// Values 返回a、b、c、d、e、f的独立副本，保留原始浮点精度
// 返回: [6]float64 矩阵分量
func (m Matrix) Values() [6]float64 {
	return [6]float64{m.a, m.b, m.c, m.d, m.e, m.f}
}

// Multiply 矩阵乘法 (m * o)
// 入参: o 右侧矩阵
// 返回: Matrix 结果矩阵
func (m Matrix) Multiply(o Matrix) Matrix {
	return MatrixFromValues(basegeom.MatrixFromValues(m.Values()).Multiply(basegeom.MatrixFromValues(o.Values())).Values())
}

// Transform 应用变换矩阵
// 入参: x X坐标, y Y坐标
// 返回: float64 变换后X, float64 变换后Y
func (m Matrix) Transform(x, y float64) (float64, float64) {
	return basegeom.MatrixFromValues(m.Values()).Transform(x, y)
}

// TransformBox 获取矩形经仿射变换后的轴对齐边界
// 入参: box 原始矩形范围
// 返回: Box 变换后的轴对齐边界
func (m Matrix) TransformBox(box Box) Box {
	return Box(basegeom.MatrixFromValues(m.Values()).TransformBox(basegeom.Box(box)))
}

// String 返回标准CTM属性值
// 返回: string 六个空格分隔的矩阵分量
func (m Matrix) String() string {
	return fmt.Sprintf("%s %s %s %s %s %s", ofdNumber(m.a), ofdNumber(m.b), ofdNumber(m.c), ofdNumber(m.d), ofdNumber(m.e), ofdNumber(m.f))
}

// Invert 求仿射矩阵的逆变换
// 返回: Matrix 逆矩阵，失败时返回单位矩阵, bool 是否存在有限逆矩阵
func (m Matrix) Invert() (Matrix, bool) {
	result, ok := basegeom.MatrixFromValues(m.Values()).Invert()
	if !ok {
		return IdentityMatrix(), false
	}
	return MatrixFromValues(result.Values()), true
}

// YScale 获取Y轴缩放比例
// 返回: float64 缩放比例
func (m Matrix) YScale() float64 {
	return basegeom.MatrixFromValues(m.Values()).YScale()
}
