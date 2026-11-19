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
	"errors"
)

var (
	// ErrCameraNotFound 视图相机路径缺失错误
	ErrCameraNotFound = errors.New("missing U3D camera path")
	// ErrAmbiguousCamera 视图相机路径存在多实例歧义错误
	ErrAmbiguousCamera = errors.New("ambiguous U3D camera instance")
)

// ViewCamera 返回视图节点指定父路径的世界变换，不修改绘制会话
// 入参: selected 视图节点索引, path 从根至节点的路径, exactRoot 是否要求路径始于根
// 返回: [16]float64 世界变换, error 缺失、歧义或取消错误
func (r *Renderer) ViewCamera(selected int, path []string, exactRoot bool) ([16]float64, error) {
	var transform [16]float64
	if selected < 0 || selected >= len(r.model.Nodes) || r.model.Nodes[selected].Kind != "View" {
		return transform, ErrCameraNotFound
	}
	found := false
	for _, index := range r.byNode[selected] {
		if err := r.ctx.Err(); err != nil {
			return transform, err
		}
		current, match := index, true
		for j := len(path) - 1; j >= 0; j-- {
			if j%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return transform, err
				}
			}
			if current < 0 || r.model.Nodes[r.scene[current].node].Name != path[j] {
				match = false
				break
			}
			current = r.scene[current].parent
		}
		if exactRoot && current >= 0 {
			match = false
		}
		if !match {
			continue
		}
		if found {
			return transform, ErrAmbiguousCamera
		}
		transform, found = r.scene[index].world, true
	}
	if !found {
		return transform, ErrCameraNotFound
	}
	return transform, nil
}
