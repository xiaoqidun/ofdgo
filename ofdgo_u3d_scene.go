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

// u3dVector 保存计算过程中的三维双精度坐标
type u3dVector [3]float64

// u3dMatrix 保存按列排列的仿射矩阵
type u3dMatrix [16]float64

// u3dInstance 保存一个父路径产生的世界实例，parent为负值时连接默认根
type u3dInstance struct {
	node, parent int
	world        u3dMatrix
	opacity      float64
	visibility   int8
}

// u3dIdentity 创建单位矩阵
// 返回: u3dMatrix 单位矩阵
func u3dIdentity() u3dMatrix { return u3dMatrix{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1} }

// multiply 连接子节点到父节点的变换
// 入参: other 子变换
// 返回: u3dMatrix 组合变换
func (m u3dMatrix) multiply(other u3dMatrix) u3dMatrix {
	var result u3dMatrix
	for column := range 4 {
		for row := range 4 {
			for k := range 4 {
				result[column*4+row] += m[k*4+row] * other[column*4+k]
			}
		}
	}
	return result
}

// valid 判断矩阵是否为有限仿射变换
// 返回: bool 是否可用于静态绘制
func (m u3dMatrix) valid() bool {
	for _, v := range m {
		if !finite(v) {
			return false
		}
	}
	return m[3] == 0 && m[7] == 0 && m[11] == 0 && m[15] == 1
}

// point 将点从局部坐标映射到目标坐标
// 入参: p 局部点
// 返回: u3dVector 目标点
func (m u3dMatrix) point(p u3dVector) u3dVector {
	return u3dVector{m[0]*p[0] + m[4]*p[1] + m[8]*p[2] + m[12], m[1]*p[0] + m[5]*p[1] + m[9]*p[2] + m[13], m[2]*p[0] + m[6]*p[1] + m[10]*p[2] + m[14]}
}

// inverse 通过带主元消元计算仿射逆矩阵
// 返回: u3dMatrix 逆矩阵, bool 是否存在有限逆矩阵
func (m u3dMatrix) inverse() (u3dMatrix, bool) {
	if !m.valid() {
		return u3dMatrix{}, false
	}
	var rows [3][6]float64
	for i := range 3 {
		for j := range 3 {
			rows[i][j] = m[j*4+i]
		}
		rows[i][i+3] = 1
	}
	for column := range 3 {
		pivot := column
		for row := column + 1; row < 3; row++ {
			if math.Abs(rows[row][column]) > math.Abs(rows[pivot][column]) {
				pivot = row
			}
		}
		if rows[pivot][column] == 0 {
			return u3dMatrix{}, false
		}
		rows[pivot], rows[column] = rows[column], rows[pivot]
		divisor := rows[column][column]
		for j := range 6 {
			rows[column][j] /= divisor
		}
		for row := range 3 {
			if row != column {
				factor := rows[row][column]
				for j := range 6 {
					rows[row][j] -= factor * rows[column][j]
				}
			}
		}
	}
	result := u3dIdentity()
	for row := range 3 {
		for col := range 3 {
			result[col*4+row] = rows[row][col+3]
		}
		result[12+row] = -(result[row]*m[12] + result[4+row]*m[13] + result[8+row]*m[14])
	}
	return result, result.valid()
}

// normal 以逆矩阵的转置变换并归一化法线
// 入参: n 局部法线，接收者为局部变换的逆矩阵
// 返回: u3dVector 目标法线
func (m u3dMatrix) normal(n u3dVector) u3dVector {
	return (u3dVector{m[0]*n[0] + m[1]*n[1] + m[2]*n[2], m[4]*n[0] + m[5]*n[1] + m[6]*n[2], m[8]*n[0] + m[9]*n[1] + m[10]*n[2]}).unit()
}

// unit 归一化向量，零向量保持为零
// 返回: u3dVector 单位向量
func (v u3dVector) unit() u3dVector {
	scale := max(math.Abs(v[0]), math.Abs(v[1]), math.Abs(v[2]))
	if scale == 0 || !finite(scale) {
		return u3dVector{}
	}
	for i := range v {
		v[i] /= scale
	}
	length := math.Sqrt(v.dot(v))
	return u3dVector{v[0] / length, v[1] / length, v[2] / length}
}

// sub 求向量差
// 入参: other 被减向量
// 返回: u3dVector 差向量
func (v u3dVector) sub(other u3dVector) u3dVector {
	return u3dVector{v[0] - other[0], v[1] - other[1], v[2] - other[2]}
}

// dot 求向量内积
// 入参: other 另一向量
// 返回: float64 内积
func (v u3dVector) dot(other u3dVector) float64 { return v[0]*other[0] + v[1]*other[1] + v[2]*other[2] }

// cross 求向量叉积
// 入参: other 另一向量
// 返回: u3dVector 叉积
func (v u3dVector) cross(other u3dVector) u3dVector {
	return u3dVector{v[1]*other[2] - v[2]*other[1], v[2]*other[0] - v[0]*other[2], v[0]*other[1] - v[1]*other[0]}
}

// u3dTriangleNormal 选取夹角更稳定的顶点计算法线，避免长瘦三角面的相消
// 入参: points 按绕序排列的顶点
// 返回: u3dVector 单位法线，退化三角面返回零向量
func u3dTriangleNormal(points [3]u3dVector) u3dVector {
	var normal u3dVector
	score := 0.0
	for i := range 3 {
		a := points[(i+1)%3].sub(points[i]).unit()
		b := points[(i+2)%3].sub(points[i]).unit()
		candidate := a.cross(b)
		if length := max(math.Abs(candidate[0]), math.Abs(candidate[1]), math.Abs(candidate[2])); length > score {
			normal, score = candidate, length
		}
	}
	return normal.unit()
}

// instances 按拓扑顺序构建全部父路径实例，限制共享层级的指数展开
// 入参: limit 最大实例数
// 返回: error 结构、预算、实例上限或取消错误
func (r *u3dRender) instances(limit int) error {
	nodes := r.model.Nodes
	if err := r.reserve(uint64(len(nodes)), 160); err != nil {
		return err
	}
	index := make(map[string]int, len(nodes))
	for i, node := range nodes {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if _, exists := index[node.Name]; exists || node.Name == "" {
			return fmt.Errorf("invalid U3D render node name")
		}
		switch node.Kind {
		case "Group", "Model", "Light", "View":
		default:
			return fmt.Errorf("unsupported U3D render node kind")
		}
		index[node.Name] = i
	}
	counts := make([]int, len(nodes))
	children := make([][]int, len(nodes))
	r.byNode = make([][]int, len(nodes))
	for i, node := range nodes {
		if err := r.reserve(uint64(len(node.Parents)), 16); err != nil {
			return err
		}
		for k, parent := range node.Parents {
			if k%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			if j, ok := index[parent.Name]; ok {
				counts[i]++
				children[j] = append(children[j], i)
			}
		}
	}
	queue := make([]int, 0, len(nodes))
	for i, n := range counts {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if n == 0 {
			queue = append(queue, i)
		}
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		i := queue[cursor]
		state := r.states[nodes[i].Name]
		for parentIndex, parent := range nodes[i].Parents {
			if parentIndex%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			var local u3dMatrix
			for k, v := range parent.Transform {
				local[k] = float64(v)
			}
			if state.Matrix != nil {
				local = *state.Matrix
			}
			if !local.valid() {
				return fmt.Errorf("unsupported U3D non-affine node transform")
			}
			parents := []int{-1}
			if j, ok := index[parent.Name]; ok {
				parents = r.byNode[j]
			}
			for _, p := range parents {
				if len(r.scene) >= limit {
					return fmt.Errorf("U3D instance count exceeds limit")
				}
				if err := r.reserve(1, 384); err != nil {
					return err
				}
				world := local
				opacity, visibility := -1.0, int8(-1)
				if p >= 0 {
					world = r.scene[p].world.multiply(local)
					opacity, visibility = r.scene[p].opacity, r.scene[p].visibility
				}
				if state.Opacity != nil {
					opacity = *state.Opacity
				}
				if state.Visible != nil {
					visibility = 0
					if *state.Visible {
						visibility = 1
					}
				}
				if !world.valid() {
					return fmt.Errorf("invalid U3D world transform")
				}
				r.byNode[i] = append(r.byNode[i], len(r.scene))
				r.scene = append(r.scene, u3dInstance{node: i, parent: p, world: world, opacity: opacity, visibility: visibility})
			}
		}
		for childIndex, child := range children[i] {
			if childIndex%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			counts[child]--
			if counts[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if len(queue) != len(nodes) {
		return fmt.Errorf("cyclic U3D node parents")
	}
	return nil
}
