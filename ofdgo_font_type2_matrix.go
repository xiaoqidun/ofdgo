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
	"fmt"
	"math"
)

// readCFFMatrix 读取有限且可逆的字体矩阵，缺省时使用指定矩阵
// 入参: dict 字体字典, fallback 缺省矩阵
// 返回: [6]float64 字体矩阵, error 矩阵错误
func readCFFMatrix(dict cffDict, fallback [6]float64) ([6]float64, error) {
	values, ok := dict[1207]
	if !ok {
		return fallback, nil
	}
	if len(values) != 6 {
		return [6]float64{}, fmt.Errorf("invalid CFF FontMatrix length")
	}
	for _, value := range values {
		if !finite(value) {
			return [6]float64{}, fmt.Errorf("invalid CFF FontMatrix value")
		}
	}
	determinant := values[0]*values[3] - values[1]*values[2]
	if !finite(determinant) || determinant == 0 {
		return [6]float64{}, fmt.Errorf("singular CFF FontMatrix")
	}
	return [6]float64(values), nil
}

// cffUnitsPerEm 保留有效的等比设计单位，其他矩阵使用1000单位封装
// 入参: matrix 字体矩阵
// 返回: uint16 OpenType设计单位
func cffUnitsPerEm(matrix [6]float64) uint16 {
	if matrix[0] > 0 && matrix[0] == matrix[3] && matrix[1] == 0 && matrix[2] == 0 {
		units := 1 / matrix[0]
		if units >= 16 && units <= 16384 && math.Abs(units-math.Round(units)) < 1e-8 {
			return uint16(math.Round(units))
		}
	}
	return 1000
}

// transformCFFBounds 按变换后的四角更新字体整体边界
// 入参: dict 顶层字典, matrix 设计坐标变换
// 返回: error 边界或变换溢出错误
func transformCFFBounds(dict cffDict, matrix [6]float64) error {
	box := dict[5]
	if box == nil {
		return nil
	}
	if len(box) != 4 {
		return fmt.Errorf("invalid CFF font bounds")
	}
	left, bottom, right, top := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, x := range []float64{box[0], box[2]} {
		for _, y := range []float64{box[1], box[3]} {
			tx, ty := matrix[0]*x+matrix[2]*y+matrix[4], matrix[1]*x+matrix[3]*y+matrix[5]
			if !finite(tx) || !finite(ty) {
				return fmt.Errorf("invalid CFF transformed bounds")
			}
			left, bottom, right, top = min(left, tx), min(bottom, ty), max(right, tx), max(top, ty)
		}
	}
	dict[5] = []float64{left, bottom, right, top}
	return nil
}

// transformedPoint 将字形当前位置与组件原点变换到输出坐标
// 返回: float64 横坐标, float64 纵坐标
func (s *type2State) transformedPoint() (float64, float64) {
	x, y := s.x+s.originX, s.y+s.originY
	if s.matrix == nil {
		return x, y
	}
	m := s.matrix
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// emitTransformedPath 展开紧凑路径指令并保留变换后的三次贝塞尔控制点
// 入参: op 已校验的路径指令, a 操作数
// 返回: error 输出编码错误
func (s *type2State) emitTransformedPath(op int, a []float64) error {
	emit := func(op int, values ...float64) error {
		m := s.matrix
		var transformed [12]float64
		for i := 0; i < len(values); i += 2 {
			x, y := values[i], values[i+1]
			transformed[i], transformed[i+1] = m[0]*x+m[2]*y, m[1]*x+m[3]*y
		}
		return s.emit(op, transformed[:len(values)])
	}
	switch op {
	case 5:
		for i := 0; i < len(a); i += 2 {
			if err := emit(5, a[i], a[i+1]); err != nil {
				return err
			}
		}
	case 6, 7:
		for i, value := range a {
			x, y := 0.0, 0.0
			if (i%2 == 0) == (op == 6) {
				x = value
			} else {
				y = value
			}
			if err := emit(5, x, y); err != nil {
				return err
			}
		}
	case 8, 24, 25:
		start, end := 0, len(a)
		if op == 24 {
			end -= 2
		} else if op == 25 {
			start = len(a) - 6
		}
		for i := 0; i < start; i += 2 {
			if err := emit(5, a[i], a[i+1]); err != nil {
				return err
			}
		}
		for i := start; i < end; i += 6 {
			if err := emit(8, a[i:i+6]...); err != nil {
				return err
			}
		}
		if end < len(a) {
			return emit(5, a[end], a[end+1])
		}
	case 26, 27:
		first := 0.0
		if len(a)%4 == 1 {
			first, a = a[0], a[1:]
		}
		for i := 0; i < len(a); i += 4 {
			var err error
			if op == 26 {
				err = emit(8, first, a[i], a[i+1], a[i+2], 0, a[i+3])
			} else {
				err = emit(8, a[i], first, a[i+1], a[i+2], a[i+3], 0)
			}
			if err != nil {
				return err
			}
			first = 0
		}
	case 30, 31:
		horizontal := op == 31
		for i := 0; i+4 <= len(a); i += 4 {
			last := 0.0
			if i+5 == len(a) {
				last = a[i+4]
			}
			var err error
			if horizontal {
				err = emit(8, a[i], 0, a[i+1], a[i+2], last, a[i+3])
			} else {
				err = emit(8, 0, a[i], a[i+1], a[i+2], a[i+3], last)
			}
			if err != nil {
				return err
			}
			horizontal = !horizontal
		}
	case 1234:
		if err := emit(8, a[0], 0, a[1], a[2], a[3], 0); err != nil {
			return err
		}
		return emit(8, a[4], 0, a[5], -a[2], a[6], 0)
	case 1235:
		if err := emit(8, a[:6]...); err != nil {
			return err
		}
		return emit(8, a[6:12]...)
	case 1236:
		if err := emit(8, a[0], a[1], a[2], a[3], a[4], 0); err != nil {
			return err
		}
		return emit(8, a[5], 0, a[6], a[7], a[8], -a[1]-a[3]-a[7])
	case 1237:
		x, y := 0.0, 0.0
		for i := 0; i < 10; i += 2 {
			x, y = x+a[i], y+a[i+1]
		}
		lastX, lastY := -x, a[10]
		if math.Abs(x) > math.Abs(y) {
			lastX, lastY = a[10], -y
		}
		if err := emit(8, a[:6]...); err != nil {
			return err
		}
		return emit(8, a[6], a[7], a[8], a[9], lastX, lastY)
	}
	return nil
}
