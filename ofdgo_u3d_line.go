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

// drawPrimitives 绘制作者点集及线集，独立着色且不参与面的背向剔除
// 入参: instance 场景实例, node 模型节点, mesh 属性数组, positions 世界位置, normals 世界法线, pass 绘制轮次, passIndex 轮次索引
// 返回: error 几何、资源或取消错误
func (r *u3dRender) drawPrimitives(instance u3dInstance, node *U3DNode, mesh *U3DMesh, positions, normals []u3dVector, pass U3DViewPass, passIndex int) error {
	if r.depthOnly {
		return nil
	}
	shaded := r.mode.shaded()
	for index := range len(mesh.Lines) + len(mesh.Points) {
		shadingIndex, corners := mesh.primitive(len(mesh.Faces) + index)
		point := index >= len(mesh.Lines)
		if index%128 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if uint64(shadingIndex) >= uint64(len(mesh.Shadings)) {
			return fmt.Errorf("invalid U3D render primitive shading index")
		}
		shading := mesh.Shadings[shadingIndex]
		if shading.Attributes > 3 || len(shading.TextureDimensions) > 8 {
			return fmt.Errorf("invalid U3D render primitive shading")
		}
		var world [2]u3dVector
		var vertices [2]u3dVertex
		for i, corner := range corners {
			if uint64(corner.Position) >= uint64(len(positions)) || shaded && (!mesh.ExcludeNormals && uint64(corner.Normal) >= uint64(len(normals)) || shading.Attributes&1 != 0 && uint64(corner.Diffuse) >= uint64(len(mesh.Diffuse)) || shading.Attributes&2 != 0 && uint64(corner.Specular) >= uint64(len(mesh.Specular))) {
				return fmt.Errorf("invalid U3D render primitive corner index")
			}
			world[i] = positions[corner.Position]
			vertices[i].point = r.toCamera.point(world[i])
			for _, value := range vertices[i].point {
				if !finite(value) {
					return fmt.Errorf("invalid U3D primitive camera position")
				}
			}
		}
		shaderNames := []string{""}
		shaders := node.LineShaders
		if point {
			shaders = node.PointShaders
		}
		if uint64(shadingIndex) < uint64(len(shaders)) {
			shaderNames = shaders[shadingIndex]
		}
		for _, name := range shaderNames {
			if err := r.ctx.Err(); err != nil {
				return err
			}
			style := u3dRenderStyle{shader: U3DShader{Blend: 0x606, RenderPass: math.MaxUint32}}
			var err error
			if shaded {
				style, err = r.style(name)
				if err != nil {
					return err
				}
			} else if index, ok := r.shaders[name]; ok {
				style.shader.RenderPass = r.model.Shaders[index].RenderPass
			}
			if style.shader.RenderPass&(1<<passIndex) == 0 {
				continue
			}
			if instance.opacity >= 0 || r.opacity >= 0 {
				style.shader.Blend = 0x606
			}
			for i, corner := range corners {
				vertices[i].color = [4]float64{r.auxiliaryColor[0], r.auxiliaryColor[1], r.auxiliaryColor[2], 1}
				if !shaded {
					continue
				}
				var normal u3dVector
				if !mesh.ExcludeNormals {
					normal = normals[corner.Normal]
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
				if r.opacity >= 0 {
					vertices[i].color[3] *= r.opacity
				}
			}
			if point {
				if err := r.modePoint(vertices[0], style.shader, pass); err != nil {
					return err
				}
				continue
			}
			if r.mode == U3DRenderVertices || r.mode == U3DRenderShadedVertices {
				for _, vertex := range vertices {
					fresh, err := r.uniquePrimitive(vertex, vertex, style.shader)
					if err != nil {
						return err
					}
					if fresh {
						if err := r.modePoint(vertex, style.shader, pass); err != nil {
							return err
						}
					}
				}
			} else if err := r.modeLine(vertices[0], vertices[1], u3dVector{}, u3dVector{}, style.shader, pass); err != nil {
				return err
			}
			for index, section := range r.sections {
				if index%256 == 0 {
					if err := r.ctx.Err(); err != nil {
						return err
					}
				}
				if section.intersection != nil {
					if err := r.lineIntersection(vertices, index); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// lineIntersection 绘制作者线段与剖切面的交点，不绘制共面线段
// 入参: line 相机空间线段, index 剖切面索引
// 返回: error 数值、资源或取消错误
func (r *u3dRender) lineIntersection(line [2]u3dVertex, index int) error {
	section := r.sections[index]
	a, b := u3dPlaneDistance(section.plane, line[0].point), u3dPlaneDistance(section.plane, line[1].point)
	if !finite(a) || !finite(b) {
		return fmt.Errorf("invalid U3D line intersection distance")
	}
	if a == 0 && b == 0 || a < 0 && b < 0 || a > 0 && b > 0 {
		return nil
	}
	point := u3dInterpolate(line[0], line[1], u3dClipFraction(a, b))
	for i, other := range r.sections {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if i != index {
			distance := u3dPlaneDistance(other.plane, point.point)
			if !finite(distance) {
				return fmt.Errorf("invalid U3D line intersection clip")
			}
			if distance < 0 {
				return nil
			}
		}
	}
	for i, plane := range r.frustumPlanes() {
		if i == 1 && r.camera.Far == 0 && !r.camera.ClipFar {
			continue
		}
		distance := u3dPlaneDistance(plane, point.point)
		if !finite(distance) {
			return fmt.Errorf("invalid U3D projected intersection")
		}
		if distance < 0 {
			return nil
		}
	}
	return r.rasterSectionLine(point, point, *section.intersection, u3dVector{}, u3dVector{})
}

// frustumPlanes 返回保留正侧的六个相机视锥面
// 返回: [6][4]float64 近、远、左右及上下平面
func (r *u3dRender) frustumPlanes() [6][4]float64 {
	w, h := r.viewport[2]/2, r.viewport[3]/2
	planes := [6][4]float64{{0, 0, 1, -r.camera.Near}, {0, 0, -1, r.camera.Far}, {r.camera.ScaleX, 0, 0, w + r.camera.ShiftX}, {-r.camera.ScaleX, 0, 0, w - r.camera.ShiftX}, {0, r.camera.ScaleY, 0, h - r.camera.ShiftY}, {0, -r.camera.ScaleY, 0, h + r.camera.ShiftY}}
	if r.camera.Perspective {
		for i := 2; i < 6; i++ {
			planes[i][2], planes[i][3] = planes[i][3], 0
		}
	}
	return planes
}

// u3dPlaneDistance 计算未归一化的平面有符号距离
// 入参: plane 平面方程, point 点坐标
// 返回: float64 有符号距离
func u3dPlaneDistance(plane [4]float64, point u3dVector) float64 {
	return plane[0]*point[0] + plane[1]*point[1] + plane[2]*point[2] + plane[3]
}

// u3dClipFraction 计算异侧有限距离的交点比例，避免绝对值求和溢出
// 入参: a 起点距离, b 终点距离
// 返回: float64 插值比例
func u3dClipFraction(a, b float64) float64 {
	a, b = math.Abs(a), math.Abs(b)
	scale := max(a, b)
	if scale == 0 {
		return 0
	}
	a, b = a/scale, b/scale
	return a / (a + b)
}

// u3dInterpolate 在相机空间插值坐标与未预乘颜色
// 入参: a 起点, b 终点, t 比例
// 返回: u3dVertex 插值顶点
func u3dInterpolate(a, b u3dVertex, t float64) u3dVertex {
	var result u3dVertex
	for i := range result.point {
		result.point[i] = a.point[i]*(1-t) + b.point[i]*t
	}
	for i := range result.color {
		result.color[i] = a.color[i]*(1-t) + b.color[i]*t
	}
	return result
}

// sectionIntersection 绘制原三角面与指定剖切面的交线，其他剖切面仍参与裁剪
// 入参: triangle 原三角面, index 剖切面索引
// 返回: error 数值、资源或取消错误
func (r *u3dRender) sectionIntersection(triangle [3]u3dVertex, index int) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	section := r.sections[index]
	var distances [3]float64
	for i, vertex := range triangle {
		distances[i] = u3dPlaneDistance(section.plane, vertex.point)
		if !finite(distances[i]) {
			return fmt.Errorf("invalid U3D intersection distance")
		}
	}
	if distances == ([3]float64{}) {
		return nil
	}
	var points [3]u3dVertex
	count := 0
	add := func(vertex u3dVertex) {
		for i := range count {
			if points[i].point == vertex.point {
				return
			}
		}
		if count < len(points) {
			points[count], count = vertex, count+1
		}
	}
	for i, vertex := range triangle {
		j := (i + 1) % 3
		if distances[i] == 0 {
			add(vertex)
		}
		if distances[i] < 0 && distances[j] > 0 || distances[i] > 0 && distances[j] < 0 {
			add(u3dInterpolate(vertex, triangle[j], u3dClipFraction(distances[i], distances[j])))
		}
	}
	if count < 2 {
		return nil
	}
	a, b := points[0], points[1]
	for i, other := range r.sections {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if i != index {
			keep, err := u3dClipLine(&a, &b, other.plane)
			if err != nil || !keep {
				return err
			}
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
	normal := u3dTriangleNormal([3]u3dVector{triangle[0].point, triangle[1].point, triangle[2].point})
	return r.rasterSectionLine(a, b, *section.intersection, normal, triangle[0].point)
}

// u3dClipLine 将线段裁至平面正侧
// 入参: a 起点, b 终点, plane 平面方程
// 返回: bool 是否保留, error 数值错误
func u3dClipLine(a, b *u3dVertex, plane [4]float64) (bool, error) {
	da, db := u3dPlaneDistance(plane, a.point), u3dPlaneDistance(plane, b.point)
	if !finite(da) || !finite(db) {
		return false, fmt.Errorf("invalid U3D line clip distance")
	}
	if da < 0 && db < 0 {
		return false, nil
	}
	if (da < 0) != (db < 0) {
		intersection := u3dInterpolate(*a, *b, u3dClipFraction(da, db))
		if da < 0 {
			*a = intersection
		} else {
			*b = intersection
		}
	}
	return true, nil
}

// rasterSectionLine 绘制单像素不透明交线，透视深度使用倒数插值
// 入参: a 已裁剪起点, b 已裁剪终点, rgb 交线颜色, normal 原面法线, origin 原面上一点
// 返回: error 数值、资源或取消错误
func (r *u3dRender) rasterSectionLine(a, b u3dVertex, rgb [3]float64, normal, origin u3dVector) error {
	a.color = [4]float64{rgb[0], rgb[1], rgb[2], 1}
	b.color = a.color
	return r.rasterLine(a, b, normal, origin, U3DShader{Blend: 0x606}, U3DViewPass{})
}

// rasterLine 绘制单像素线段，颜色与深度按透视插值，面深度偏移防止自身遮挡
// 入参: a 已裁剪起点, b 已裁剪终点, normal 面法线, origin 面上点, shader 着色器, pass 绘制轮次
// 返回: error 数值、资源或取消错误
func (r *u3dRender) rasterLine(a, b u3dVertex, normal, origin u3dVector, shader U3DShader, pass U3DViewPass) error {
	cx := r.viewport[0] + r.viewport[2]/2 + r.camera.ShiftX
	cy := r.viewport[1] + r.viewport[3]/2 + r.camera.ShiftY
	planes := [4][4]float64{{r.camera.ScaleX, 0, 0, cx}, {-r.camera.ScaleX, 0, 0, float64(r.output.Rect.Dx()) - cx}, {0, -r.camera.ScaleY, 0, cy}, {0, r.camera.ScaleY, 0, float64(r.output.Rect.Dy()) - cy}}
	for _, plane := range planes {
		if r.camera.Perspective {
			plane[2], plane[3] = plane[3], 0
		}
		keep, err := u3dClipLine(&a, &b, plane)
		if err != nil || !keep {
			return err
		}
	}
	var x, y, q [2]float64
	for i, vertex := range [2]u3dVertex{a, b} {
		q[i] = 1
		if r.camera.Perspective {
			if vertex.point[2] <= 0 {
				return fmt.Errorf("invalid U3D line perspective depth")
			}
			q[i] = 1 / vertex.point[2]
		}
		x[i] = cx + vertex.point[0]*r.camera.ScaleX*q[i]
		y[i] = cy - vertex.point[1]*r.camera.ScaleY*q[i]
		if !finite(x[i]) || !finite(y[i]) || !finite(q[i]) {
			return fmt.Errorf("invalid U3D projected line")
		}
	}
	steps := math.Ceil(max(math.Abs(x[1]-x[0]), math.Abs(y[1]-y[0])))
	if !finite(steps) || steps > float64(max(r.output.Rect.Dx(), r.output.Rect.Dy()))+2 {
		return fmt.Errorf("invalid U3D line span")
	}
	count := max(1, int(steps))
	r.order++
	previousPixel := -1
	for i := 0; i <= count; i++ {
		if i%1024 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		t := float64(i) / float64(count)
		px := math.Floor(x[0] + float64(i)*(x[1]-x[0])/float64(count))
		py := math.Floor(y[0] + float64(i)*(y[1]-y[0])/float64(count))
		if px < 0 || py < 0 || px >= float64(r.output.Rect.Dx()) || py >= float64(r.output.Rect.Dy()) {
			continue
		}
		pixel := int(py)*r.output.Rect.Dx() + int(px)
		if pixel == previousPixel {
			continue
		}
		previousPixel = pixel
		depth := a.point[2] + (b.point[2]-a.point[2])*t
		weight := t
		if r.camera.Perspective {
			depth = 1 / (q[0]*(1-t) + q[1]*t)
			weight = t * q[1] * depth
		}
		ray := u3dVector{(px + .5 - cx) / r.camera.ScaleX, (cy - py - .5) / r.camera.ScaleY, 1}
		denominator := normal[2]
		numerator := normal.dot(origin) - normal[0]*ray[0] - normal[1]*ray[1]
		if r.camera.Perspective {
			denominator, numerator = normal.dot(ray), normal.dot(origin)
		}
		if sampled := numerator / denominator; finite(sampled) && sampled >= r.camera.Near && (r.camera.Far == 0 && !r.camera.ClipFar || sampled <= r.camera.Far) {
			depth = sampled
		}
		depth -= 8 * math.Abs(depth-math.Nextafter(depth, math.Inf(-1)))
		if depth <= r.frame[pixel].depth {
			vertex := u3dInterpolate(a, b, weight)
			visible, err := u3dFragmentColor(&vertex.color, vertex.point, &shader, &pass)
			if err != nil {
				return err
			}
			if !visible {
				continue
			}
			if err := r.fragment(pixel, depth, vertex.color, shader.Blend); err != nil {
				return err
			}
		}
	}
	return nil
}
