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

// DepthRange 计算可见模型几何的相机深度范围，不为自动裁剪复制顶点
// 返回: float64 最近深度, float64 最远深度, bool 是否含几何, error 几何或取消错误
func (r *Renderer) DepthRange() (float64, float64, bool, error) {
	minimum, maximum := math.Inf(1), math.Inf(-1)
	for i, instance := range r.scene {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return 0, 0, false, err
			}
		}
		node := &r.model.Nodes[instance.node]
		mesh := r.nodeMesh(node)
		if node.Kind != "Model" || instance.modelVisibility(node) == 0 || instance.opacity == 0 || mesh == nil {
			continue
		}
		matrix := r.toCamera.multiply(instance.world)
		for j := range len(mesh.Faces) + len(mesh.Lines) + len(mesh.Points) {
			_, corners := mesh.primitive(j)
			if j%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return 0, 0, false, err
				}
			}
			for _, corner := range corners {
				if uint64(corner.Position) >= uint64(len(mesh.Positions)) {
					return 0, 0, false, fmt.Errorf("invalid U3D position index")
				}
				p := mesh.Positions[corner.Position]
				z := matrix[2]*float64(p[0]) + matrix[6]*float64(p[1]) + matrix[10]*float64(p[2]) + matrix[14]
				if !finite(z) {
					return 0, 0, false, fmt.Errorf("invalid U3D camera depth")
				}
				minimum, maximum = min(minimum, z), max(maximum, z)
			}
		}
	}
	for i, section := range r.sections {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return 0, 0, false, err
			}
		}
		if section.opacity > 0 {
			for _, corner := range section.corners {
				minimum, maximum = min(minimum, corner.point[2]), max(maximum, corner.point[2])
			}
		}
	}
	if math.IsInf(minimum, 1) {
		return 0, 0, false, nil
	}
	return minimum, maximum, true, nil
}
