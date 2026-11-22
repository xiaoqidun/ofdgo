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

package threed

import (
	"fmt"
	"math"
)

// u3dTextureInverse 计算包裹变换的逆矩阵，保留非仿射齐次分量
// 入参: matrix 按列排列的包裹变换
// 返回: [16]float64 逆矩阵, error 奇异矩阵或非有限结果
func u3dTextureInverse(matrix [16]float32) ([16]float64, error) {
	var rows [4][8]float64
	for row := range 4 {
		for col := range 4 {
			rows[row][col] = float64(matrix[col*4+row])
			if !finite(rows[row][col]) {
				return [16]float64{}, fmt.Errorf("invalid U3D texture wrap transform")
			}
		}
		rows[row][row+4] = 1
	}
	for col := range 4 {
		pivot := col
		for row := col + 1; row < 4; row++ {
			if math.Abs(rows[row][col]) > math.Abs(rows[pivot][col]) {
				pivot = row
			}
		}
		if rows[pivot][col] == 0 {
			return [16]float64{}, fmt.Errorf("singular U3D texture wrap transform")
		}
		rows[col], rows[pivot] = rows[pivot], rows[col]
		divisor := rows[col][col]
		for i := range 8 {
			rows[col][i] /= divisor
		}
		for row := range 4 {
			if row == col {
				continue
			}
			factor := rows[row][col]
			for i := range 8 {
				rows[row][i] -= factor * rows[col][i]
			}
		}
	}
	var result [16]float64
	for row := range 4 {
		for col := range 4 {
			value := rows[row][col+4]
			if !finite(value) {
				return [16]float64{}, fmt.Errorf("invalid U3D inverse texture wrap transform")
			}
			result[col*4+row] = value
		}
	}
	return result, nil
}

// generated 将插值后的位置或法线映射到纹理，再应用纹理变换
// 经线未定义时取0，球心纬度取1/2，反射方向奇点取图像中心
// 入参: channel 通道索引, input 模型位置或相机法线, position 相机空间位置
// 返回: [3]float64 齐次纹理坐标, error 非有限值或无效投影
func (s *u3dTextureSet) generated(channel int, input [3]float64, position u3dVector) ([3]float64, error) {
	layer := s.layers[channel]
	for _, value := range input {
		if !finite(value) {
			return [3]float64{}, fmt.Errorf("invalid U3D texture mapping input")
		}
	}
	uv := [4]float64{0, 0, 0, 1}
	if layer.Mode == 4 {
		incident := u3dVector{0, 0, 1}
		if s.perspective {
			incident = position.unit()
		}
		normal := u3dVector(input).unit()
		factor := 2 * normal.dot(incident)
		reflected := incident.sub(u3dVector{factor * normal[0], factor * normal[1], factor * normal[2]})
		denominator := 2 * math.Hypot(math.Hypot(reflected[0], reflected[1]), 1-reflected[2])
		uv[0], uv[1] = .5, .5
		if denominator != 0 {
			uv[0] += reflected[0] / denominator
			uv[1] -= reflected[1] / denominator
		}
	} else {
		var point [4]float64
		for row := range 4 {
			point[row] = s.wrap[channel][12+row]
			for col, value := range input {
				point[row] += s.wrap[channel][col*4+row] * value
			}
		}
		if !finite(point[3]) || point[3] == 0 {
			return [3]float64{}, fmt.Errorf("invalid U3D texture wrap projection")
		}
		for i := range 3 {
			point[i] /= point[3]
			if !finite(point[i]) {
				return [3]float64{}, fmt.Errorf("invalid U3D texture wrap projection")
			}
		}
		switch layer.Mode {
		case 1:
			uv[0], uv[1] = point[0], point[1]
		case 2, 3:
			if point[0] != 0 || point[1] != 0 {
				uv[0] = math.Atan2(point[1], point[0]) / (2 * math.Pi)
				if uv[0] < 0 {
					uv[0]++
				}
			}
			uv[1] = point[2]
			if layer.Mode == 3 {
				direction := (u3dVector{point[0], point[1], point[2]}).unit()
				uv[1] = math.Acos(min(1, max(-1, direction[2]))) / math.Pi
			}
		}
	}
	var result [3]float64
	for component, row := range [3]int{0, 1, 3} {
		for col, value := range uv {
			result[component] += float64(layer.Transform[col*4+row]) * value
		}
		if !finite(result[component]) {
			return [3]float64{}, fmt.Errorf("invalid U3D generated texture coordinate")
		}
	}
	return result, nil
}
