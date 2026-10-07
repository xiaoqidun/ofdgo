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

// U3DRenderMode 定义静态几何的呈现方式，边线和顶点使用单像素覆盖
type U3DRenderMode uint8

const (
	U3DRenderSolid              U3DRenderMode = iota // 实体
	U3DRenderSolidWireframe                          // 实体线框
	U3DRenderWireframe                               // 线框
	U3DRenderShadedWireframe                         // 着色线框
	U3DRenderHiddenWireframe                         // 隐藏线框
	U3DRenderVertices                                // 顶点
	U3DRenderShadedVertices                          // 着色顶点
	U3DRenderBoundingBox                             // 包围盒线框
	U3DRenderBoundingBoxFaces                        // 包围盒面
	U3DRenderBoundingBoxOutline                      // 包围盒面及线框
	U3DRenderIllustration                            // 插图
	U3DRenderSolidOutline                            // 实体轮廓
	U3DRenderShadedIllustration                      // 着色插图
)

// u3dPrimitiveKey 区分共享点线的颜色及混合状态，避免透明边点重复叠加
type u3dPrimitiveKey struct {
	a, b      u3dVertex
	blend     uint32
	function  uint32
	reference float32
}

// uniquePrimitive 在当前网格内去重着色边点，缓存按条目计入绘制预算
// 入参: a 起点或顶点, b 终点或相同顶点, shader 着色器
// 返回: bool 是否首次绘制, error 预算或取消错误
func (r *u3dRender) uniquePrimitive(a, b u3dVertex, shader U3DShader) (bool, error) {
	if r.primitives == nil {
		return true, nil
	}
	left := [7]float64{a.point[0], a.point[1], a.point[2], a.color[0], a.color[1], a.color[2], a.color[3]}
	right := [7]float64{b.point[0], b.point[1], b.point[2], b.color[0], b.color[1], b.color[2], b.color[3]}
	for i := range left {
		if left[i] != right[i] {
			if left[i] > right[i] {
				a, b = b, a
			}
			break
		}
	}
	key := u3dPrimitiveKey{a: a, b: b, blend: shader.Blend}
	if shader.Attributes&2 != 0 {
		key.function, key.reference = shader.AlphaFunction, shader.AlphaReference
	}
	if _, ok := r.primitives[key]; ok {
		return false, nil
	}
	if err := r.reserve(1, 512); err != nil {
		return false, err
	}
	r.primitives[key] = struct{}{}
	return true, nil
}

// shaded 判断绘制模式是否使用材质、法线和灯光
// 返回: bool 是否着色
func (mode U3DRenderMode) shaded() bool {
	return mode == U3DRenderSolid || mode == U3DRenderSolidWireframe || mode == U3DRenderShadedWireframe || mode == U3DRenderShadedVertices || mode == U3DRenderSolidOutline || mode == U3DRenderShadedIllustration
}

// silhouette 判断绘制模式是否仅描绘轮廓边
// 返回: bool 是否使用轮廓边
func (mode U3DRenderMode) silhouette() bool {
	return mode >= U3DRenderIllustration && mode <= U3DRenderShadedIllustration
}

// modeTriangle 根据当前模式绘制原三角面、原始边或顶点，不为裁剪边界生成网格边
// 入参: triangle 相机空间顶点, shader 着色器, pass 绘制轮次
// 返回: error 数值、资源或取消错误
func (r *u3dRender) modeTriangle(triangle [3]u3dVertex, shader U3DShader, pass U3DViewPass) error {
	if r.depthOnly || r.mode == U3DRenderSolid || r.mode.silhouette() {
		return r.sectionTriangle(triangle, shader, pass)
	}
	if r.mode == U3DRenderSolidWireframe {
		if err := r.sectionTriangle(triangle, shader, pass); err != nil {
			return err
		}
	}
	if r.mode != U3DRenderShadedWireframe && r.mode != U3DRenderShadedVertices {
		for i := range triangle {
			triangle[i].color = [4]float64{r.auxiliaryColor[0], r.auxiliaryColor[1], r.auxiliaryColor[2], 1}
		}
		shader = U3DShader{Blend: 0x606}
	}
	normal := u3dTriangleNormal([3]u3dVector{triangle[0].point, triangle[1].point, triangle[2].point})
	for i, vertex := range triangle {
		var err error
		if r.mode == U3DRenderVertices || r.mode == U3DRenderShadedVertices {
			fresh, uniqueErr := r.uniquePrimitive(vertex, vertex, shader)
			if uniqueErr != nil {
				return uniqueErr
			}
			if !fresh {
				continue
			}
			err = r.modePoint(vertex, shader, pass)
		} else {
			fresh, uniqueErr := r.uniquePrimitive(vertex, triangle[(i+1)%3], shader)
			if uniqueErr != nil {
				return uniqueErr
			}
			if !fresh {
				continue
			}
			err = r.modeLine(vertex, triangle[(i+1)%3], normal, triangle[0].point, shader, pass)
		}
		if err != nil {
			return err
		}
	}
	if r.mode != U3DRenderSolidWireframe {
		for i, section := range r.sections {
			if section.intersection != nil {
				if err := r.sectionIntersection(triangle, i); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// modeLine 对原始网格边应用剖切与视锥裁剪，再按像素深度绘制
// 入参: a 起点, b 终点, normal 面法线, origin 面上点, shader 着色器, pass 绘制轮次
// 返回: error 数值、资源或取消错误
func (r *u3dRender) modeLine(a, b u3dVertex, normal, origin u3dVector, shader U3DShader, pass U3DViewPass) error {
	for i, section := range r.sections {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		keep, err := u3dClipLine(&a, &b, section.plane)
		if err != nil || !keep {
			return err
		}
	}
	for i, plane := range r.frustumPlanes() {
		if i == 1 && r.camera.Far == 0 && !r.camera.ClipFar {
			continue
		}
		keep, err := u3dClipLine(&a, &b, plane)
		if err != nil || !keep {
			return err
		}
	}
	return r.rasterLine(a, b, normal, origin, shader, pass)
}

// modePoint 绘制原始顶点，不在裁剪边界补充顶点
// 入参: vertex 顶点, shader 着色器, pass 绘制轮次
// 返回: error 数值、资源或取消错误
func (r *u3dRender) modePoint(vertex u3dVertex, shader U3DShader, pass U3DViewPass) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	for i, section := range r.sections {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		distance := u3dPlaneDistance(section.plane, vertex.point)
		if !finite(distance) {
			return fmt.Errorf("invalid U3D point clip distance")
		}
		if distance < 0 {
			return nil
		}
	}
	for i, plane := range r.frustumPlanes() {
		if i == 1 && r.camera.Far == 0 && !r.camera.ClipFar {
			continue
		}
		distance := u3dPlaneDistance(plane, vertex.point)
		if !finite(distance) {
			return fmt.Errorf("invalid U3D point clip distance")
		}
		if distance < 0 {
			return nil
		}
	}
	q := 1.0
	if r.camera.Perspective {
		q = 1 / vertex.point[2]
	}
	x := math.Floor(r.viewport[0] + r.viewport[2]/2 + r.camera.ShiftX + vertex.point[0]*r.camera.ScaleX*q)
	y := math.Floor(r.viewport[1] + r.viewport[3]/2 + r.camera.ShiftY - vertex.point[1]*r.camera.ScaleY*q)
	if !finite(x) || !finite(y) {
		return fmt.Errorf("invalid U3D projected vertex")
	}
	if x < 0 || y < 0 || x >= float64(r.output.Rect.Dx()) || y >= float64(r.output.Rect.Dy()) {
		return nil
	}
	pixel := int(y)*r.output.Rect.Dx() + int(x)
	if vertex.point[2] > r.frame[pixel].depth {
		return nil
	}
	visible, err := u3dFragmentColor(&vertex.color, vertex.point, &shader, &pass)
	if err != nil || !visible {
		return err
	}
	r.order++
	return r.fragment(pixel, vertex.point[2], vertex.color, shader.Blend)
}

// drawBounds 在模型局部空间计算包围盒，再应用实例变换，不以世界坐标轴重建包围盒
// 入参: instance 场景实例, node 模型节点, mesh 网格, pass 绘制轮次, passIndex 轮次索引
// 返回: error 几何、数值、预算或取消错误
func (r *u3dRender) drawBounds(instance u3dInstance, node *U3DNode, mesh *U3DMesh, pass U3DViewPass, passIndex int) error {
	minimum := u3dVector{math.Inf(1), math.Inf(1), math.Inf(1)}
	maximum := u3dVector{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	visible := false
	for i := range len(mesh.Faces) + len(mesh.Lines) {
		shading, corners := mesh.primitive(i)
		shaders := node.Shaders
		if i >= len(mesh.Faces) {
			shaders = node.LineShaders
		}
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if uint64(shading) >= uint64(len(shaders)) {
			visible = true
		} else if !visible {
			for _, name := range shaders[shading] {
				index, ok := r.shaders[name]
				if !ok || r.model.Shaders[index].RenderPass&(1<<passIndex) != 0 {
					visible = true
					break
				}
			}
		}
		for _, corner := range corners {
			if uint64(corner.Position) >= uint64(len(mesh.Positions)) {
				return fmt.Errorf("invalid U3D bounds position index")
			}
			for j, value := range mesh.Positions[corner.Position] {
				v := float64(value)
				if !finite(v) {
					return fmt.Errorf("invalid U3D bounds position")
				}
				minimum[j], maximum[j] = min(minimum[j], v), max(maximum[j], v)
			}
		}
	}
	if !visible || math.IsInf(minimum[0], 1) {
		return nil
	}
	opacity := 1.0
	if instance.opacity >= 0 {
		opacity = instance.opacity
	}
	if r.opacity >= 0 {
		opacity *= r.opacity
	}
	if r.mode == U3DRenderBoundingBox {
		opacity = 0
	}
	var vertices [8]u3dVertex
	for i := range vertices {
		point := minimum
		for j := range 3 {
			if i&(1<<j) != 0 {
				point[j] = maximum[j]
			}
		}
		vertices[i].point = r.toCamera.point(instance.world.point(point))
		for _, v := range vertices[i].point {
			if !finite(v) {
				return fmt.Errorf("invalid U3D transformed bounds")
			}
		}
		vertices[i].color = [4]float64{r.faceColor[0], r.faceColor[1], r.faceColor[2], opacity}
	}
	shader := U3DShader{Blend: 0x606}
	var edges [12][2]u3dVector
	edgeCount := 0
	for index, face := range [6][4]int{{0, 2, 3, 1}, {4, 5, 7, 6}, {0, 1, 5, 4}, {2, 6, 7, 3}, {0, 4, 6, 2}, {1, 3, 7, 5}} {
		if index%2 == 1 && minimum[2-index/2] == maximum[2-index/2] {
			continue
		}
		for i := 1; i < 3; i++ {
			triangle := [3]u3dVertex{vertices[face[0]], vertices[face[i]], vertices[face[i+1]]}
			r.order++
			if err := r.sectionTriangle(triangle, shader, pass); err != nil {
				return err
			}
		}
		if r.mode == U3DRenderBoundingBoxFaces {
			continue
		}
		normal := u3dTriangleNormal([3]u3dVector{vertices[face[0]].point, vertices[face[1]].point, vertices[face[2]].point})
		for i, index := range face {
			a, b := vertices[index], vertices[face[(i+1)%4]]
			alpha := 1.0
			if r.mode == U3DRenderBoundingBoxOutline {
				alpha = opacity
				duplicate := a.point == b.point
				for _, edge := range edges[:edgeCount] {
					if edge == ([2]u3dVector{a.point, b.point}) || edge == ([2]u3dVector{b.point, a.point}) {
						duplicate = true
						break
					}
				}
				if duplicate {
					continue
				}
				edges[edgeCount] = [2]u3dVector{a.point, b.point}
				edgeCount++
			}
			a.color = [4]float64{r.auxiliaryColor[0], r.auxiliaryColor[1], r.auxiliaryColor[2], alpha}
			b.color = a.color
			if err := r.modeLine(a, b, normal, a.point, shader, pass); err != nil {
				return err
			}
		}
	}
	return nil
}
