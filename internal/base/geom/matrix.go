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

package geom

import (
	"math"
)

// IdentityMatrix 单位矩阵
var IdentityMatrix = Matrix{1, 0, 0, 1, 0, 0}

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

// MatrixFromValues 从a、b、c、d、e、f构造矩阵，不经过文本格式化
// 入参: values 矩阵分量
// 返回: Matrix 变换矩阵
func MatrixFromValues(values [6]float64) Matrix {
	return Matrix{a: values[0], b: values[1], c: values[2], d: values[3], e: values[4], f: values[5]}
}

// TranslationMatrix 构造平移矩阵
// 入参: x X轴偏移, y Y轴偏移
// 返回: Matrix 平移矩阵
func TranslationMatrix(x, y float64) Matrix {
	return Matrix{a: 1, d: 1, e: x, f: y}
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

// TransformBox 获取矩形经仿射变换后的轴对齐边界
// 入参: box 原始矩形范围
// 返回: Box 变换后的轴对齐边界
func (m Matrix) TransformBox(box Box) Box {
	x, y := m.Transform(box.X, box.Y)
	left, top, right, bottom := x, y, x, y
	for _, point := range [][2]float64{{box.X + box.W, box.Y}, {box.X, box.Y + box.H}, {box.X + box.W, box.Y + box.H}} {
		x, y := m.Transform(point[0], point[1])
		left, top, right, bottom = math.Min(left, x), math.Min(top, y), math.Max(right, x), math.Max(bottom, y)
	}
	return Box{X: left, Y: top, W: right - left, H: bottom - top}
}

// Invert 求仿射矩阵的逆变换
// 返回: Matrix 逆矩阵，失败时返回单位矩阵, bool 是否存在有限逆矩阵
func (m Matrix) Invert() (Matrix, bool) {
	for _, value := range m.Values() {
		if !finite(value) {
			return IdentityMatrix, false
		}
	}
	det, ds := matrixProductDifference(m.a, m.d, m.b, m.c)
	x, xs := matrixProductDifference(m.c, m.f, m.d, m.e)
	y, ys := matrixProductDifference(m.b, m.e, m.a, m.f)
	if det != 0 && ds && xs && ys {
		n := Matrix{a: m.d / det, b: -m.b / det, c: -m.c / det, d: m.a / det, e: x / det, f: y / det}
		valid := true
		for _, value := range n.Values() {
			valid = valid && finite(value)
		}
		if valid {
			return n, true
		}
	}
	determinant, exponent := matrixProductSum(m.a, m.d, -m.b, m.c)
	if determinant == 0 {
		return IdentityMatrix, false
	}
	divide := func(value float64, power int) float64 {
		mantissa, shift := math.Frexp(value)
		return math.Ldexp(mantissa/determinant, power+shift-exponent)
	}
	x, xe := matrixProductSum(m.c, m.f, -m.d, m.e)
	y, ye := matrixProductSum(m.b, m.e, -m.a, m.f)
	n := Matrix{a: divide(m.d, 0), b: divide(-m.b, 0), c: divide(-m.c, 0), d: divide(m.a, 0), e: divide(x, xe), f: divide(y, ye)}
	for _, value := range n.Values() {
		if !finite(value) {
			return IdentityMatrix, false
		}
	}
	return n, true
}

// YScale 获取Y轴缩放比例
// 返回: float64 缩放比例
func (m Matrix) YScale() float64 {
	return math.Hypot(m.c, m.d)
}

// Equal 比较毫米坐标，使用固定绝对误差，不受绘图库全局精度影响
// 入参: a 第一个坐标, b 第二个坐标
// 返回: bool 是否在容差内相等
func Equal(a, b float64) bool {
	return math.Abs(a-b) <= 1e-10
}

// AxisAligned 判断变换是否保持坐标轴平行，含直角旋转与镜像
// 入参: m 仿射变换矩阵
// 返回: bool 是否为非退化的轴对齐变换
func AxisAligned(m Matrix) bool {
	return m.b == 0 && m.c == 0 && m.a != 0 && m.d != 0 || m.a == 0 && m.d == 0 && m.b != 0 && m.c != 0
}

// Vector 变换位移，不包含平移分量
// 入参: m 仿射变换矩阵, x 原始横向位移, y 原始纵向位移
// 返回: float64 变换后的X位移, float64 变换后的Y位移
func Vector(m Matrix, x, y float64) (float64, float64) {
	return m.a*x + m.c*y, m.b*x + m.d*y
}

// matrixProductDifference 补偿乘积相减的舍入残差，检查溢出、下溢及严重相消
// 入参: a 第一个乘数, b 第二个乘数, c 第三个乘数, d 第四个乘数
// 返回: float64 乘积之差, bool 是否可直接用于求逆
func matrixProductDifference(a, b, c, d float64) (float64, bool) {
	x, y := a*b, c*d
	value := x - y
	if !finite(value) || x == 0 && a != 0 && b != 0 || y == 0 && c != 0 && d != 0 {
		return value, false
	}
	if x != 0 && y != 0 {
		value = math.FMA(a, b, -y) + math.FMA(-c, d, y)
	}
	return value, x == 0 && y == 0 || math.Abs(value) >= 0x1p-1022 && math.Abs(value) >= 0x1p-48*math.Max(math.Abs(x), math.Abs(y))
}

// matrixProductSum 以尾数及指数累加两个乘积，保留乘法舍入残差
// 入参: a 第一个乘数, b 第二个乘数, c 第三个乘数, d 第四个乘数
// 返回: float64 结果尾数, int 二进制指数
func matrixProductSum(a, b, c, d float64) (float64, int) {
	a, ae := math.Frexp(a)
	b, be := math.Frexp(b)
	c, ce := math.Frexp(c)
	d, de := math.Frexp(d)
	x, y := a*b, c*d
	xe, ye := ae+be, ce+de
	power := max(xe, ye)
	if x == 0 {
		power = ye
	} else if y == 0 {
		power = xe
	}
	value := math.Ldexp(x, xe-power) + math.Ldexp(y, ye-power)
	value += math.Ldexp(math.FMA(a, b, -x), xe-power) + math.Ldexp(math.FMA(c, d, -y), ye-power)
	value, shift := math.Frexp(value)
	return value, power + shift
}
