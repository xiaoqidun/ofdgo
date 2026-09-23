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

import "math"

// reflectShdPosition 获取镜像周期内的位置
// 入参: position 渐变位置
// 返回: float64 周期内的位置
func reflectShdPosition(position float64) float64 {
	index := math.Floor(position)
	position -= index
	if math.Mod(index, 2) != 0 {
		return 1 - position
	}
	return position
}

// shadingClip 按渐变延伸规则裁剪绘制与选区，避免只在采样时隐藏区间外像素
// 入参: path 页面路径, paint 渐变画刷, matrix 渐变父矩阵
// 返回: GeometryPath 可见路径, error 几何错误
func (c *semanticCompiler) shadingClip(path GeometryPath, paint Paint, matrix Matrix) (GeometryPath, error) {
	gradient, view := semanticGradient(paint, matrix)
	spread := gradient.Spread
	if spread == nil || spread.Extend == 3 {
		return path, nil
	}
	inverse, ok := view.Invert()
	if !ok {
		return nil, nil
	}
	local, err := c.geometry.Transform(path, inverse)
	if err != nil {
		return nil, err
	}
	if gradient.Kind == RasterLinear {
		dx, dy := gradient.End.X-gradient.Start.X, gradient.End.Y-gradient.Start.Y
		axis := Matrix{a: dx, b: dy, c: -dy, d: dx, e: gradient.Start.X, f: gradient.Start.Y}
		inv, _ := axis.Invert()
		box, err := c.geometry.Bounds(local)
		if err != nil {
			return nil, err
		}
		box = inv.TransformBox(box)
		end := box.X + box.W
		if spread.Extend&1 == 0 {
			box.X = math.Max(box.X, 0)
		}
		if spread.Extend&2 == 0 {
			end = math.Min(end, 1)
		}
		box.W = end - box.X
		if box.W <= 0 {
			return nil, nil
		}
		clip, err := c.geometry.Transform(geometryRectangle(box), axis)
		if err != nil {
			return nil, err
		}
		local, err = c.geometry.Combine(local, clip, GeometryIntersect)
		if err != nil {
			return nil, err
		}
	} else {
		inner, outer, r0, r1, extend := gradient.Start, gradient.End, gradient.R0, gradient.R1, spread.Extend
		if r1 < r0 {
			inner, outer, r0, r1 = outer, inner, r1, r0
			extend = (extend&1)<<1 | (extend&2)>>1
		}
		if extend&2 == 0 {
			local, err = c.geometry.Combine(local, geometryCircle(outer, r1), GeometryIntersect)
			if err != nil {
				return nil, err
			}
		}
		if extend&1 == 0 && r0 > 0 {
			local, err = c.geometry.Combine(local, geometryCircle(inner, r0), GeometrySubtract)
			if err != nil {
				return nil, err
			}
		}
	}
	return c.geometry.Transform(local, view)
}

// geometryCircle 构建标准圆弧，不提前折线化
// 入参: center 圆心, radius 半径
// 返回: GeometryPath 圆路径
func geometryCircle(center RasterPoint, radius float64) GeometryPath {
	return GeometryPath{
		{Verb: GeometryMove, End: Point{X: center.X + radius, Y: center.Y}},
		{Verb: GeometryArc, RadiusX: radius, RadiusY: radius, Sweep: true, End: Point{X: center.X - radius, Y: center.Y}},
		{Verb: GeometryArc, RadiusX: radius, RadiusY: radius, Sweep: true, End: Point{X: center.X + radius, Y: center.Y}},
		{Verb: GeometryClose},
	}
}
