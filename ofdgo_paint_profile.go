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
	"image/color"
	"math"
)

// profileGradient 保存独立色标和只读配置，延伸及重复由几何采样器处理
type profileGradient struct {
	positions []float64
	colors    []MeshVertex
}

// profileGradient 准备ICC渐变的源分量采样，不用转换后的端点近似非线性色彩变换
// 入参: segments 原色标, alpha 外层透明度
// 返回: func(float64) color.RGBA 单位区间采样器，无ICC时为nil, error 配置错误
func (r *Renderer) profileGradient(segments []ShdSegment, alpha *int) (func(float64) color.RGBA, error) {
	profiled := false
	for _, segment := range segments {
		if space := r.colorDefinition(segment.Color.ColorSpace); space != nil && space.Profile != "" {
			profiled = true
			break
		}
	}
	if !profiled {
		return nil, nil
	}
	gradient := &profileGradient{positions: gradientPositions(segments), colors: make([]MeshVertex, len(segments))}
	for i, segment := range segments {
		kind, values := r.colorComponents(segment.Color.Value, segment.Color.Index, segment.Color.ColorSpace)
		profile, err := r.Reader.colorProfile(r.colorDefinition(segment.Color.ColorSpace))
		if err != nil {
			return nil, err
		}
		opacity := 1.0
		if value := mergeAlpha(segment.Color.Alpha, alpha); value != nil {
			opacity = float64(clampColor(*value)) / 255
		}
		gradient.colors[i] = MeshVertex{Space: kind, Values: values, Alpha: opacity, profile: profile}
	}
	return gradient.At, nil
}

// At 在相同源空间内插值，不同空间先转换为sRGB，保留重复位置的右侧色标
// 入参: t 单位区间位置
// 返回: color.RGBA 预乘采样颜色
func (g *profileGradient) At(t float64) color.RGBA {
	if len(g.colors) == 0 || math.IsNaN(t) {
		return color.RGBA{}
	}
	first := g.colors[0]
	if t <= g.positions[0] {
		r, green, b := first.rgb(first.Values)
		return profileColor([3]float64{r, green, b}, first.Alpha)
	}
	for i := 1; i < len(g.colors); i++ {
		if t < g.positions[i] {
			before, after := g.colors[i-1], g.colors[i]
			x := (t - g.positions[i-1]) / (g.positions[i] - g.positions[i-1])
			if before.sameSpace(after) {
				var values [4]float64
				for j := range values {
					values[j] = before.Values[j]*(1-x) + after.Values[j]*x
				}
				r, green, b := before.rgb(values)
				return profileColor([3]float64{r, green, b}, before.Alpha*(1-x)+after.Alpha*x)
			}
			r0, g0, b0 := before.rgb(before.Values)
			r1, g1, b1 := after.rgb(after.Values)
			return color.RGBA{meshByte(r0*before.Alpha*(1-x) + r1*after.Alpha*x), meshByte(g0*before.Alpha*(1-x) + g1*after.Alpha*x), meshByte(b0*before.Alpha*(1-x) + b1*after.Alpha*x), meshByte(before.Alpha*(1-x) + after.Alpha*x)}
		}
	}
	last := g.colors[len(g.colors)-1]
	r, green, b := last.rgb(last.Values)
	return profileColor([3]float64{r, green, b}, last.Alpha)
}
