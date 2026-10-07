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

import "fmt"

// U3DNodeState 按名称覆盖节点，未指定透明度或可见性时沿父路径继承
// Opacity范围为0到1，采用标准透明混合；Matrix为相对父节点的列式仿射矩阵
// 未匹配的名称不影响场景，矩阵为空时保留每条父路径的原始变换
type U3DNodeState struct {
	Name    string
	Opacity *float64
	Visible *bool
	Matrix  *[16]float64
}

// nodeStates 建立有预算限制的覆盖索引，重复名称整体替换而不合并字段
// 入参: states 节点覆盖
// 返回: error 数值、预算或取消错误
func (r *u3dRender) nodeStates(states []U3DNodeState) error {
	if len(states) == 0 {
		return nil
	}
	if err := r.reserve(uint64(len(states)), 128); err != nil {
		return err
	}
	r.states = make(map[string]U3DNodeState, len(states))
	for i, state := range states {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		r.states[state.Name] = state
	}
	for _, state := range r.states {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if state.Opacity != nil && (!finite(*state.Opacity) || *state.Opacity < 0 || *state.Opacity > 1) {
			return fmt.Errorf("invalid U3D node opacity")
		}
		if state.Matrix != nil && !u3dMatrix(*state.Matrix).valid() {
			return fmt.Errorf("invalid U3D node matrix")
		}
	}
	return nil
}

// modelVisibility 在节点覆盖生效时恢复隐藏模型，保留原有正反面选择
// 入参: node 模型节点
// 返回: uint32 可见面标志
func (instance u3dInstance) modelVisibility(node *U3DNode) uint32 {
	if instance.visibility == 0 {
		return 0
	}
	if instance.visibility == 1 && node.Visibility == 0 {
		return 3
	}
	return node.Visibility
}
