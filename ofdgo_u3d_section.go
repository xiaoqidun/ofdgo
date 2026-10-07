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

// U3DCrossSection 定义世界坐标中的剖切面，AxisU与AxisV须为非零正交方向
// 两轴叉积指向被裁去的一侧；平面方形边长取静态场景包围盒对角线
// Opacity为0时不显示平面，Intersection为空时不显示交线，颜色分量范围为0到1
type U3DCrossSection struct {
	Center, AxisU, AxisV [3]float64
	Opacity              float64
	Color                [3]float64
	Intersection         *[3]float64
}

// u3dSection 保存相机空间裁剪方程、平面四角与交线颜色
type u3dSection struct {
	plane        [4]float64
	corners      [4]u3dVertex
	intersection *[3]float64
	opacity      float64
}

// prepareSections 准备剖切缓存，不复制网格或修改调用方参数
// 入参: sections 世界坐标剖切面
// 返回: error 参数、场景、预算或取消错误
func (r *u3dRender) prepareSections(sections []U3DCrossSection) error {
	if len(sections) == 0 {
		return nil
	}
	if err := r.reserve(uint64(len(sections)), 352); err != nil {
		return err
	}
	if err := r.reserve(uint64(len(sections))+3, 112); err != nil {
		return err
	}
	r.sections = make([]u3dSection, len(sections))
	for i := range r.sectionScratch {
		r.sectionScratch[i] = make([]u3dVertex, len(sections)+3)
	}
	visible := false
	for i, section := range sections {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if !finite(section.Opacity) || section.Opacity < 0 || section.Opacity > 1 {
			return fmt.Errorf("invalid U3D section opacity")
		}
		visible = visible || section.Opacity > 0
	}
	diagonal := 0.0
	if visible {
		var err error
		diagonal, err = r.sceneDiagonal()
		if err != nil {
			return err
		}
	}
	world := u3dMatrix(r.camera.ToWorld)
	if world == (u3dMatrix{}) {
		world = u3dIdentity()
	}
	for i, section := range sections {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		for _, vector := range [][3]float64{section.Center, section.AxisU, section.AxisV} {
			for _, value := range vector {
				if !finite(value) {
					return fmt.Errorf("invalid U3D section coordinates")
				}
			}
		}
		for _, color := range []*[3]float64{&section.Color, section.Intersection} {
			if color != nil {
				for _, v := range color {
					if !finite(v) || v < 0 || v > 1 {
						return fmt.Errorf("invalid U3D section color")
					}
				}
			}
		}
		u, v := u3dVector(section.AxisU).unit(), u3dVector(section.AxisV).unit()
		if u == (u3dVector{}) || v == (u3dVector{}) || math.Abs(u.dot(v)) > 1e-12 {
			return fmt.Errorf("invalid U3D section axes")
		}
		normal := u.cross(v).unit()
		prepared := &r.sections[i]
		for j := range 3 {
			prepared.plane[j] = -(normal[0]*world[j*4] + normal[1]*world[j*4+1] + normal[2]*world[j*4+2])
		}
		prepared.plane[3] = normal.dot(u3dVector(section.Center).sub(u3dVector{world[12], world[13], world[14]}))
		for _, coefficient := range prepared.plane {
			if !finite(coefficient) {
				return fmt.Errorf("invalid U3D section plane")
			}
		}
		prepared.intersection, prepared.opacity = section.Intersection, section.Opacity
		for j, signs := range [4][2]float64{{-1, -1}, {1, -1}, {1, 1}, {-1, 1}} {
			point := u3dVector(section.Center)
			for k := range point {
				point[k] += diagonal / 2 * (signs[0]*u[k] + signs[1]*v[k])
			}
			prepared.corners[j] = u3dVertex{point: r.toCamera.point(point), color: [4]float64{section.Color[0], section.Color[1], section.Color[2], section.Opacity}}
			for _, coordinate := range prepared.corners[j].point {
				if !finite(coordinate) {
					return fmt.Errorf("invalid U3D section corner")
				}
			}
		}
	}
	return nil
}

// sceneDiagonal 计算静态场景世界包围盒的对角线，包含隐藏节点的几何
// 返回: float64 对角线长度, error 几何、数值或取消错误
func (r *u3dRender) sceneDiagonal() (float64, error) {
	minimum := u3dVector{math.Inf(1), math.Inf(1), math.Inf(1)}
	maximum := u3dVector{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, instance := range r.scene {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		node := r.model.Nodes[instance.node]
		index, exists := r.meshes[node.Resource]
		if node.Kind != "Model" || !exists {
			continue
		}
		mesh := &r.model.Meshes[index]
		for i := range len(mesh.Faces) + len(mesh.Lines) {
			_, corners := mesh.primitive(i)
			if i%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return 0, err
				}
			}
			for _, corner := range corners {
				if uint64(corner.Position) >= uint64(len(mesh.Positions)) {
					return 0, fmt.Errorf("invalid U3D position index")
				}
				p := mesh.Positions[corner.Position]
				world := instance.world.point(u3dVector{float64(p[0]), float64(p[1]), float64(p[2])})
				for j, v := range world {
					if !finite(v) {
						return 0, fmt.Errorf("invalid U3D scene bounds")
					}
					minimum[j], maximum[j] = min(minimum[j], v), max(maximum[j], v)
				}
			}
		}
	}
	if math.IsInf(minimum[0], 1) {
		return 0, nil
	}
	delta := maximum.sub(minimum)
	diagonal := math.Hypot(math.Hypot(delta[0], delta[1]), delta[2])
	if !finite(diagonal) {
		return 0, fmt.Errorf("invalid U3D scene diagonal")
	}
	return diagonal, nil
}

// sectionTriangle 用复用的凸多边形缓存应用全部剖切面，再进行视锥裁剪
// 入参: triangle 三角面, shader 着色器, pass 绘制轮次
// 返回: error 数值、资源或取消错误
func (r *u3dRender) sectionTriangle(triangle [3]u3dVertex, shader U3DShader, pass U3DViewPass) error {
	if len(r.sections) == 0 {
		return r.clipTriangle(triangle, shader, pass)
	}
	copy(r.sectionScratch[0], triangle[:])
	size, current := 3, 0
	for _, section := range r.sections {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		next, count := 1-current, 0
		previous := r.sectionScratch[current][size-1]
		previousDistance := u3dPlaneDistance(section.plane, previous.point)
		for i, vertex := range r.sectionScratch[current][:size] {
			if i%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			distance := u3dPlaneDistance(section.plane, vertex.point)
			if !finite(distance) || !finite(previousDistance) {
				return fmt.Errorf("invalid U3D section distance")
			}
			if (distance >= 0) != (previousDistance >= 0) {
				var err error
				count, err = u3dAppendSectionVertex(r.sectionScratch[next], count, u3dInterpolate(previous, vertex, u3dClipFraction(previousDistance, distance)))
				if err != nil {
					return err
				}
			}
			if distance >= 0 {
				var err error
				count, err = u3dAppendSectionVertex(r.sectionScratch[next], count, vertex)
				if err != nil {
					return err
				}
			}
			previous, previousDistance = vertex, distance
		}
		if count > 1 && r.sectionScratch[next][0].point == r.sectionScratch[next][count-1].point {
			count--
		}
		size, current = count, next
		if size < 3 {
			break
		}
	}
	if size < 2 {
		return nil
	}
	for i := 1; i+1 < size; i++ {
		if err := r.clipTriangle([3]u3dVertex{r.sectionScratch[current][0], r.sectionScratch[current][i], r.sectionScratch[current][i+1]}, shader, pass); err != nil {
			return err
		}
	}
	if r.depthOnly {
		return nil
	}
	for i, section := range r.sections {
		if section.intersection != nil {
			if err := r.sectionIntersection(triangle, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// u3dAppendSectionVertex 合并连续重复交点，维持凸多边形缓存上限
// 入参: buffer 固定缓存, count 已有顶点数, vertex 新顶点
// 返回: int 顶点数, error 缓存越界错误
func u3dAppendSectionVertex(buffer []u3dVertex, count int, vertex u3dVertex) (int, error) {
	if count > 0 && buffer[count-1].point == vertex.point {
		return count, nil
	}
	if count >= len(buffer) {
		return count, fmt.Errorf("invalid U3D section polygon")
	}
	buffer[count] = vertex
	return count + 1, nil
}

// drawSectionPlanes 在最后一轮绘制平面方形，不把平面本身当作待剖切几何
// 返回: error 数值、资源或取消错误
func (r *u3dRender) drawSectionPlanes() error {
	for _, section := range r.sections {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if section.opacity == 0 {
			continue
		}
		for i := 1; i < 3; i++ {
			r.order++
			if err := r.clipTriangle([3]u3dVertex{section.corners[0], section.corners[i], section.corners[i+1]}, U3DShader{Blend: 0x606}, U3DViewPass{}); err != nil {
				return err
			}
		}
	}
	return nil
}
