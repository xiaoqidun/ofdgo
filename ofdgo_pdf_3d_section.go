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
	"context"
	"fmt"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// pdfThreeDSections 按XYZ顺序构造剖切方向，中心保持为世界坐标固定点
// 入参: ctx 取消上下文, sections PDF剖切面, options 绘制选项
// 返回: error 方向、预算或取消错误
func pdfThreeDSections(ctx context.Context, sections []pdfgo.ThreeDCrossSection, options *U3DRenderOptions) error {
	if len(sections) == 0 {
		return nil
	}
	limit := options.MaxMemoryBytes
	if limit == 0 {
		limit = u3dDefaultBytes
	}
	if limit <= 0 || uint64(len(sections)) >= uint64(limit)/136 {
		return fmt.Errorf("PDF 3D section memory exceeds limit")
	}
	options.MaxMemoryBytes = limit - int64(len(sections))*136
	options.Sections = make([]U3DCrossSection, len(sections))
	for i, source := range sections {
		if err := ctx.Err(); err != nil {
			return err
		}
		if source.Axis < 0 || source.Axis > 2 {
			return fmt.Errorf("invalid PDF 3D section axis")
		}
		section := U3DCrossSection{Center: source.Center, Color: source.Color, Opacity: source.Opacity}
		section.AxisU[(source.Axis+1)%3], section.AxisV[(source.Axis+2)%3] = 1, 1
		for axis, angle := range source.Rotation {
			if axis == source.Axis {
				continue
			}
			if !finite(angle) {
				return fmt.Errorf("invalid PDF 3D section rotation")
			}
			sin, cos := math.Sincos(math.Remainder(angle, 360) * math.Pi / 180)
			a, b := (axis+1)%3, (axis+2)%3
			for _, vector := range []*[3]float64{&section.AxisU, &section.AxisV} {
				vector[a], vector[b] = cos*vector[a]-sin*vector[b], sin*vector[a]+cos*vector[b]
			}
		}
		if source.IntersectionVisible {
			color := source.IntersectionColor
			section.Intersection = &color
		}
		options.Sections[i] = section
	}
	return nil
}
