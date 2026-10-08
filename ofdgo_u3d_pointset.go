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

// pointNormalPrediction 按ECMA-363第9.6.2.2.4.4节计算点集球面法线预测
// 入参: mesh 属性数组, points 分裂位置的点
// 返回: u3dVector 预测方向, error 不支持的多方向预测或取消错误
func (d *u3dDecoder) pointNormalPrediction(mesh *U3DMesh, points []U3DPoint) (u3dVector, error) {
	var directions [2]u3dVector
	var counts [2]int
	var previous [3]float32
	var direction u3dVector
	for i, point := range points {
		if i%256 == 0 {
			if err := d.ctx.Err(); err != nil {
				return u3dVector{}, err
			}
		}
		normal := mesh.Normals[point.Corners[0].Normal]
		if i == 0 || normal != previous {
			direction = (u3dVector{float64(normal[0]), float64(normal[1]), float64(normal[2])}).unit()
			previous = normal
		}
		if counts[0] == 0 || direction == directions[0] {
			directions[0] = direction
			counts[0]++
		} else if counts[1] == 0 || direction == directions[1] {
			directions[1] = direction
			counts[1]++
		} else {
			return u3dVector{}, fmt.Errorf("unsupported U3D point normal prediction with multiple directions")
		}
	}
	if counts[1] == 0 {
		return directions[0], nil
	}
	a, b := directions[0], directions[1]
	if a == (u3dVector{}) || b == (u3dVector{}) {
		return u3dVector{}, fmt.Errorf("unsupported U3D point normal prediction with zero direction")
	}
	dot := max(-1, min(1, a.dot(b)))
	tangent := b.sub(u3dVector{a[0] * dot, a[1] * dot, a[2] * dot})
	length := math.Sqrt(tangent.dot(tangent))
	if length == 0 {
		return u3dVector{}, fmt.Errorf("unsupported U3D antipodal point normal prediction")
	}
	angle := math.Atan2(length, dot) * (float64(counts[1]) / float64(len(points)))
	sine, cosine := math.Sincos(angle)
	var result u3dVector
	for i := range result {
		result[i] = a[i]*cosine + tangent[i]/length*sine
	}
	return result.unit(), nil
}
