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

// u3dOutline 保存单个模型实例的共享边索引，按插入顺序绘制
type u3dOutline struct {
	index map[[2]u3dVector]int
	edges []u3dOutlineEdge
}

// u3dOutlineEdge 保存正背面关系及正面法线角度区间，避免多面共享边的成对扫描
type u3dOutlineEdge struct {
	a, b                         u3dVector
	reference, tangent           u3dVector
	minimumNormal, maximumNormal u3dVector
	visibleNormal                u3dVector
	minimum, maximum             float64
	front, back                  int
	visible, frontVisible        bool
}

// outlineFace 收集当前绘制轮次中的原始网格边，正面法线在同一半圆内取角度极值
// 入参: outline 共享边索引, world 世界坐标, vertices 相机顶点, front 是否正面, visible 是否绘制面, shaders 着色列表, passIndex 轮次
// 返回: error 数值、预算或取消错误
func (r *u3dRender) outlineFace(outline *u3dOutline, world [3]u3dVector, vertices [3]u3dVertex, front, visible bool, shaders []string, passIndex int) error {
	active := false
	for i, name := range shaders {
		if i%128 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		index, ok := r.shaders[name]
		if !ok || r.model.Shaders[index].RenderPass&(1<<passIndex) != 0 {
			active = true
			break
		}
	}
	if !active {
		return nil
	}
	normal := u3dTriangleNormal(world)
	if normal == (u3dVector{}) {
		return nil
	}
	cameraNormal := u3dTriangleNormal([3]u3dVector{vertices[0].point, vertices[1].point, vertices[2].point})
	for i := range 3 {
		j := (i + 1) % 3
		a, b := world[i], world[j]
		pa, pb := vertices[i].point, vertices[j].point
		for c := range 3 {
			if a[c] != b[c] {
				if a[c] > b[c] {
					a, b, pa, pb = b, a, pb, pa
				}
				break
			}
		}
		key := [2]u3dVector{a, b}
		index, exists := outline.index[key]
		if !exists {
			if err := r.reserve(1, 1024); err != nil {
				return err
			}
			index = len(outline.edges)
			outline.edges = append(outline.edges, u3dOutlineEdge{a: pa, b: pb})
			outline.index[key] = index
		}
		edge := &outline.edges[index]
		if visible && !edge.visible {
			edge.visibleNormal = cameraNormal
		}
		edge.visible = edge.visible || visible
		if !front {
			edge.back++
			continue
		}
		edge.frontVisible = edge.frontVisible || visible
		if edge.front == 0 {
			edge.reference = normal
			edge.tangent = b.sub(a).unit().cross(normal).unit()
			edge.minimumNormal, edge.maximumNormal = cameraNormal, cameraNormal
		} else {
			angle := math.Atan2(normal.dot(edge.tangent), normal.dot(edge.reference))
			if !finite(angle) {
				return fmt.Errorf("invalid U3D silhouette angle")
			}
			if angle < edge.minimum {
				edge.minimum, edge.minimumNormal = angle, cameraNormal
			}
			if angle > edge.maximum {
				edge.maximum, edge.maximumNormal = angle, cameraNormal
			}
		}
		edge.front++
	}
	return nil
}

// drawOutline 绘制外缘、正背面交界和达到夹角的共享边，沿用像素深度去除遮挡
// 入参: outline 共享边索引, pass 绘制轮次
// 返回: error 数值、资源或取消错误
func (r *u3dRender) drawOutline(outline *u3dOutline, pass U3DViewPass) error {
	shader := U3DShader{Blend: 0x606}
	color := [4]float64{r.auxiliaryColor[0], r.auxiliaryColor[1], r.auxiliaryColor[2], 1}
	for i, edge := range outline.edges {
		if i%128 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if !edge.visible {
			continue
		}
		angle := (edge.maximum - edge.minimum) * 180 / math.Pi
		if edge.front+edge.back != 1 && !(edge.front > 0 && edge.back > 0) && !(edge.front > 1 && angle >= math.Nextafter(r.creaseAngle, math.Inf(-1))) {
			continue
		}
		a, b := u3dVertex{point: edge.a, color: color}, u3dVertex{point: edge.b, color: color}
		normals := [2]u3dVector{edge.visibleNormal, edge.visibleNormal}
		if edge.frontVisible {
			normals = [2]u3dVector{edge.minimumNormal, edge.maximumNormal}
		}
		for i, normal := range normals {
			if i == 1 && normal == normals[0] {
				continue
			}
			if err := r.modeLine(a, b, normal, edge.a, shader, pass); err != nil {
				return err
			}
		}
	}
	return nil
}
