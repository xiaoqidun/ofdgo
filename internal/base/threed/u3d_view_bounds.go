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
	"image"
	"math"
)

// viewImageBounds 计算采样器可能写入的像素范围，并在整数转换前裁至目标边界
// 入参: source 原始像素边界, m 像素变换, target 目标边界
// 返回: image.Rectangle 受影响的目标像素区域
func viewImageBounds(source image.Rectangle, m [6]float64, target image.Rectangle) image.Rectangle {
	if source.Empty() || target.Empty() {
		return image.Rectangle{}
	}
	for _, v := range m {
		if !finite(v) {
			return image.Rectangle{}
		}
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range [4]image.Point{source.Min, {X: source.Max.X, Y: source.Min.Y}, {X: source.Min.X, Y: source.Max.Y}, source.Max} {
		x := math.Floor(float64(m[0]*float64(p.X)) + float64(m[2]*float64(p.Y)) + m[4])
		y := math.Floor(float64(m[1]*float64(p.X)) + float64(m[3]*float64(p.Y)) + m[5])
		if !finite(x) || !finite(y) {
			return image.Rectangle{}
		}
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x+1), math.Max(maxY, y+1)
	}
	minX, minY = math.Max(minX, float64(target.Min.X)), math.Max(minY, float64(target.Min.Y))
	maxX, maxY = math.Min(maxX, float64(target.Max.X)), math.Min(maxY, float64(target.Max.Y))
	if !finite(minX) || !finite(minY) || !finite(maxX) || !finite(maxY) || minX >= maxX || minY >= maxY {
		return image.Rectangle{}
	}
	return image.Rect(int(minX), int(minY), int(maxX), int(maxY))
}
