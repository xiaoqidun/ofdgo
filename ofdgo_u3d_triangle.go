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
	"slices"
)

// u3dVertex 保存裁剪前后的相机坐标及未预乘颜色
type u3dVertex struct {
	point u3dVector
	color [4]float64
}

// drawMesh 按实例复用顶点变换，着色列表按指定顺序绘制
// 入参: instance 场景实例, node 模型节点, mesh 网格, pass 绘制轮次, passIndex 轮次索引
// 返回: error 几何、资源或取消错误
func (r *u3dRender) drawMesh(instance u3dInstance, node *U3DNode, mesh *U3DMesh, pass U3DViewPass, passIndex int) error {
	count := uint64(len(mesh.Positions)) + uint64(len(mesh.Normals))
	if err := r.reserve(count, 24); err != nil {
		return err
	}
	defer func() { r.remaining += count * 24 }()
	positions := make([]u3dVector, len(mesh.Positions))
	normals := make([]u3dVector, len(mesh.Normals))
	inverse, invertible := instance.world.inverse()
	for i, p := range mesh.Positions {
		if i%1024 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		positions[i] = instance.world.point(u3dVector{float64(p[0]), float64(p[1]), float64(p[2])})
		for _, v := range positions[i] {
			if !finite(v) {
				return fmt.Errorf("invalid U3D transformed position")
			}
		}
	}
	for i, n := range mesh.Normals {
		if i%1024 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		for _, v := range n {
			if !finite(float64(v)) {
				return fmt.Errorf("invalid U3D normal")
			}
		}
		if invertible {
			normals[i] = inverse.normal(u3dVector{float64(n[0]), float64(n[1]), float64(n[2])})
		}
	}
	for index, face := range mesh.Faces {
		if index%128 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if uint64(face.Shading) >= uint64(len(mesh.Shadings)) {
			return fmt.Errorf("invalid U3D render shading index")
		}
		shading := mesh.Shadings[face.Shading]
		if shading.Attributes > 3 || len(shading.TextureDimensions) > 8 {
			return fmt.Errorf("invalid U3D render shading")
		}
		var world [3]u3dVector
		var vertices [3]u3dVertex
		for i, corner := range face.Corners {
			if uint64(corner.Position) >= uint64(len(positions)) || !mesh.ExcludeNormals && uint64(corner.Normal) >= uint64(len(normals)) || shading.Attributes&1 != 0 && uint64(corner.Diffuse) >= uint64(len(mesh.Diffuse)) || shading.Attributes&2 != 0 && uint64(corner.Specular) >= uint64(len(mesh.Specular)) {
				return fmt.Errorf("invalid U3D render corner index")
			}
			world[i] = positions[corner.Position]
			vertices[i].point = r.toCamera.point(world[i])
			for _, v := range vertices[i].point {
				if !finite(v) {
					return fmt.Errorf("invalid U3D camera position")
				}
			}
		}
		var faceNormal u3dVector
		if mesh.ExcludeNormals || !invertible {
			faceNormal = u3dTriangleNormal(world)
		}
		p0, p1, p2 := vertices[0].point, vertices[1].point, vertices[2].point
		facing := (p1[0]-p0[0])*(p2[1]-p0[1]) - (p1[1]-p0[1])*(p2[0]-p0[0])
		if r.camera.Perspective {
			facing = p0.unit().dot(p1.unit().cross(p2.unit()))
		}
		if !finite(facing) {
			return fmt.Errorf("invalid U3D face orientation")
		}
		front := facing < 0
		visibility := instance.modelVisibility(node)
		if facing == 0 || front && visibility&1 == 0 || !front && visibility&2 == 0 {
			continue
		}
		shaderNames := []string{""}
		if uint64(face.Shading) < uint64(len(node.Shaders)) {
			shaderNames = node.Shaders[face.Shading]
		}
		for shaderIndex, name := range shaderNames {
			if shaderIndex%128 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			style, err := r.style(name)
			if err != nil {
				return err
			}
			if style.shader.RenderPass&(1<<passIndex) == 0 {
				continue
			}
			if instance.opacity >= 0 {
				style.shader.Blend = 0x606
			}
			for i, corner := range face.Corners {
				normal := faceNormal
				if !mesh.ExcludeNormals && invertible {
					normal = normals[corner.Normal]
				}
				if !front {
					for j := range normal {
						normal[j] = -normal[j]
					}
				}
				var diffuse, specular *[4]float32
				if shading.Attributes&1 != 0 {
					diffuse = &mesh.Diffuse[corner.Diffuse]
				}
				if shading.Attributes&2 != 0 {
					specular = &mesh.Specular[corner.Specular]
				}
				vertices[i].color, err = r.shade(world[i], normal, style, diffuse, specular)
				if err != nil {
					return err
				}
				if instance.opacity >= 0 {
					vertices[i].color[3] = instance.opacity
				}
			}
			r.order++
			if err := r.sectionTriangle(vertices, style.shader, pass); err != nil {
				return err
			}
		}
	}
	return nil
}

// clipTriangle 在透视除法前裁剪六个视锥面，避免近面穿越或屏外巨大坐标
// 入参: triangle 三角面, shader 着色器, pass 绘制轮次
// 返回: error 数值、资源或取消错误
func (r *u3dRender) clipTriangle(triangle [3]u3dVertex, shader U3DShader, pass U3DViewPass) error {
	var storage [2][12]u3dVertex
	copy(storage[0][:], triangle[:])
	size, current := 3, 0
	planes := r.frustumPlanes()
	for planeIndex, plane := range planes {
		if planeIndex == 1 && r.camera.Far == 0 && !r.camera.ClipFar {
			continue
		}
		output, next := 0, 1-current
		previous := storage[current][size-1]
		distance := func(v u3dVertex) float64 {
			return plane[0]*v.point[0] + plane[1]*v.point[1] + plane[2]*v.point[2] + plane[3]
		}
		previousDistance := distance(previous)
		for _, vertex := range storage[current][:size] {
			d := distance(vertex)
			if !finite(d) || !finite(previousDistance) {
				return fmt.Errorf("invalid U3D clip distance")
			}
			if (d >= 0) != (previousDistance >= 0) {
				t := u3dClipFraction(previousDistance, d)
				var intersection u3dVertex
				for i := range intersection.point {
					intersection.point[i] = previous.point[i]*(1-t) + vertex.point[i]*t
				}
				for i := range intersection.color {
					intersection.color[i] = previous.color[i]*(1-t) + vertex.color[i]*t
				}
				if output == len(storage[next]) {
					return fmt.Errorf("invalid U3D clipped polygon")
				}
				storage[next][output] = intersection
				output++
			}
			if d >= 0 {
				if output == len(storage[next]) {
					return fmt.Errorf("invalid U3D clipped polygon")
				}
				storage[next][output] = vertex
				output++
			}
			previous, previousDistance = vertex, d
		}
		if output < 3 {
			return nil
		}
		current, size = next, output
	}
	for i := 1; i+1 < size; i++ {
		if err := r.rasterTriangle([3]u3dVertex{storage[current][0], storage[current][i], storage[current][i+1]}, shader, pass); err != nil {
			return err
		}
	}
	return nil
}

// rasterTriangle 使用像素中心和左上边规则光栅化，透视插值保持颜色与深度一致
// 入参: triangle 已裁剪三角面, shader 着色器, pass 绘制轮次
// 返回: error 数值、预算或取消错误
func (r *u3dRender) rasterTriangle(triangle [3]u3dVertex, shader U3DShader, pass U3DViewPass) error {
	var x, y, reciprocal [3]float64
	for i, v := range triangle {
		reciprocal[i] = 1
		if r.camera.Perspective {
			reciprocal[i] = 1 / v.point[2]
		}
		x[i] = r.viewport[0] + r.viewport[2]/2 + r.camera.ShiftX + v.point[0]*r.camera.ScaleX*reciprocal[i]
		y[i] = r.viewport[1] + r.viewport[3]/2 + r.camera.ShiftY - v.point[1]*r.camera.ScaleY*reciprocal[i]
		if !finite(x[i]) || !finite(y[i]) || !finite(reciprocal[i]) {
			return fmt.Errorf("invalid U3D projected point")
		}
	}
	area := (x[1]-x[0])*(y[2]-y[0]) - (y[1]-y[0])*(x[2]-x[0])
	if !finite(area) {
		return fmt.Errorf("invalid U3D projected area")
	}
	if area == 0 {
		return nil
	}
	if area < 0 {
		triangle[1], triangle[2] = triangle[2], triangle[1]
		x[1], x[2] = x[2], x[1]
		y[1], y[2] = y[2], y[1]
		reciprocal[1], reciprocal[2] = reciprocal[2], reciprocal[1]
		area = -area
	}
	width, height := r.output.Rect.Dx(), r.output.Rect.Dy()
	left := max(0, math.Ceil(min(x[0], x[1], x[2])-.5))
	right := min(float64(width-1), math.Floor(max(x[0], x[1], x[2])-.5))
	top := max(0, math.Ceil(min(y[0], y[1], y[2])-.5))
	bottom := min(float64(height-1), math.Floor(max(y[0], y[1], y[2])-.5))
	if left > right || top > bottom {
		return nil
	}
	var inclusive [3]bool
	for i := range 3 {
		j := (i + 1) % 3
		inclusive[i] = y[j] < y[i] || y[j] == y[i] && x[j] > x[i]
	}
	for row := int(top); row <= int(bottom); row++ {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		for column := int(left); column <= int(right); column++ {
			if column%1024 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			px, py := float64(column)+.5, float64(row)+.5
			var edge [3]float64
			inside := true
			for i := range 3 {
				j := (i + 1) % 3
				edge[i] = (x[j]-x[i])*(py-y[i]) - (y[j]-y[i])*(px-x[i])
				if edge[i] < 0 || edge[i] == 0 && !inclusive[i] {
					inside = false
					break
				}
			}
			if !inside {
				continue
			}
			w1, w2 := edge[2]/area, edge[0]/area
			depth := triangle[0].point[2] + w1*(triangle[1].point[2]-triangle[0].point[2]) + w2*(triangle[2].point[2]-triangle[0].point[2])
			if r.camera.Perspective {
				q := edge[1]/area*reciprocal[0] + w1*reciprocal[1] + w2*reciprocal[2]
				if !finite(q) || q <= 0 {
					return fmt.Errorf("invalid U3D perspective depth")
				}
				w1 = w1 * reciprocal[1] / q
				w2 = w2 * reciprocal[2] / q
				depth = 1 / q
			}
			pixel := row*width + column
			if depth > r.frame[pixel].depth {
				continue
			}
			var color [4]float64
			for c := range color {
				color[c] = triangle[0].color[c] + w1*(triangle[1].color[c]-triangle[0].color[c]) + w2*(triangle[2].color[c]-triangle[0].color[c])
				if !finite(color[c]) {
					return fmt.Errorf("invalid U3D interpolated color")
				}
			}
			color[3] = min(1, max(0, color[3]))
			if shader.Attributes&2 != 0 && !u3dAlphaTest(shader.AlphaFunction, color[3], float64(shader.AlphaReference)) {
				continue
			}
			if pass.Attributes&1 != 0 {
				position := u3dVector{(px - r.viewport[0] - r.viewport[2]/2 - r.camera.ShiftX) / r.camera.ScaleX, (r.viewport[1] + r.viewport[3]/2 + r.camera.ShiftY - py) / r.camera.ScaleY, depth}
				if r.camera.Perspective {
					position[0] *= depth
					position[1] *= depth
				}
				distance := math.Hypot(math.Hypot(position[0], position[1]), position[2])
				factor := (float64(pass.FogFar) - distance) / (float64(pass.FogFar) - float64(pass.FogNear))
				if pass.FogMode != 0 {
					amount := distance * math.Log(100) / float64(pass.FogFar)
					if pass.FogMode == 2 {
						amount *= amount
					}
					factor = math.Exp(-amount)
				}
				factor = min(1, max(0, factor))
				for c := range 3 {
					color[c] = color[c]*factor + float64(pass.FogColor[c])*(1-factor)
				}
			}
			for c := range 3 {
				color[c] = min(1, max(0, color[c]))
			}
			if err := r.fragment(pixel, depth, color, shader.Blend); err != nil {
				return err
			}
		}
	}
	return nil
}

// fragment 直接保存不透明覆盖，透明及其他混合方式保留逐像素深度
// 入参: pixel 像素索引, depth 深度, color 颜色, blend 混合方式
// 返回: error 预算或取消错误
func (r *u3dRender) fragment(pixel int, depth float64, color [4]float64, blend uint32) error {
	if !finite(depth) {
		return fmt.Errorf("invalid U3D fragment depth")
	}
	p := &r.frame[pixel]
	if blend == 0x606 && color[3] == 1 || blend == 0x607 && color[3] == 0 {
		p.depth = depth
		p.order = r.order
		copy(p.color[:], color[:3])
		return nil
	}
	if blend == 0x606 && color[3] == 0 || blend == 0x607 && color[3] == 1 {
		return nil
	}
	if r.fragments >= math.MaxInt32 {
		return fmt.Errorf("U3D fragment count exceeds limit")
	}
	chunk := r.fragments / 512
	if chunk == len(r.chunks) {
		if err := r.reserve(1, 512*56+48); err != nil {
			return err
		}
		r.chunks = append(r.chunks, make([]u3dFragment, 512))
	}
	r.chunks[chunk][r.fragments%512] = u3dFragment{color: color, depth: depth, order: r.order, blend: blend, next: p.head}
	p.head = int32(r.fragments)
	r.fragments++
	return nil
}

// resolveFragments 按每个像素的远近排序，正确处理相交透明面和着色器叠加
// 返回: error 预算或取消错误
func (r *u3dRender) resolveFragments() error {
	if r.fragments == 0 {
		return r.ctx.Err()
	}
	get := func(i int) *u3dFragment { return &r.chunks[i/512][i%512] }
	for i := range r.frame {
		if i%1024 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		pixel := &r.frame[i]
		r.scratch = r.scratch[:0]
		for index := pixel.head; index >= 0; {
			if len(r.scratch)%1024 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			current := int(index)
			fragment := get(current)
			index = fragment.next
			if fragment.depth > pixel.depth || fragment.depth == pixel.depth && fragment.order < pixel.order {
				continue
			}
			if len(r.scratch) == cap(r.scratch) {
				capacity := max(8, cap(r.scratch)*2)
				if err := r.reserve(uint64(capacity), 8); err != nil {
					return err
				}
				values := make([]int, len(r.scratch), capacity)
				copy(values, r.scratch)
				r.remaining += uint64(cap(r.scratch)) * 8
				r.scratch = values
			}
			r.scratch = append(r.scratch, current)
		}
		slices.SortFunc(r.scratch, func(a, b int) int {
			x, y := get(a), get(b)
			if x.depth > y.depth {
				return -1
			}
			if x.depth < y.depth {
				return 1
			}
			if x.order < y.order {
				return -1
			}
			if x.order > y.order {
				return 1
			}
			return 0
		})
		for layer, index := range r.scratch {
			if layer%1024 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			fragment := get(index)
			alpha := fragment.color[3]
			if fragment.blend == 0x607 {
				alpha = 1 - alpha
			}
			for c := range 3 {
				switch fragment.blend {
				case 0x604:
					pixel.color[c] += fragment.color[c]
				case 0x605:
					pixel.color[c] *= fragment.color[c]
				default:
					pixel.color[c] = fragment.color[c]*alpha + pixel.color[c]*(1-alpha)
				}
				pixel.color[c] = min(1, max(0, pixel.color[c]))
			}
		}
	}
	return r.ctx.Err()
}
