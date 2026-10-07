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
	color := [4]float64{rgb[0], rgb[1], rgb[2], 1}
	r.order++
	for i := 0; i <= count; i++ {
		if i%1024 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		t := float64(i) / float64(count)
		px, py := math.Floor(x[0]*(1-t)+x[1]*t), math.Floor(y[0]*(1-t)+y[1]*t)
		if px < 0 || py < 0 || px >= float64(r.output.Rect.Dx()) || py >= float64(r.output.Rect.Dy()) {
			continue
		}
		depth := a.point[2]*(1-t) + b.point[2]*t
		if r.camera.Perspective {
			depth = 1 / (q[0]*(1-t) + q[1]*t)
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
		pixel := int(py)*r.output.Rect.Dx() + int(px)
		if depth <= r.frame[pixel].depth {
			if err := r.fragment(pixel, depth, color, 0x606); err != nil {
				return err
			}
		}
	}
	return nil
}
