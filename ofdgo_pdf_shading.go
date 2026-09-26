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
	"math"
	"slices"

	"github.com/xiaoqidun/pdfgo"
)

// pdfGradientSegments 将线性源颜色及设备四色的截断点表达为OFD分段
// 入参: stops 源颜色分段, space 源颜色空间
// 返回: []ShdSegment 等价RGB分段, error 不可线性表达的变换
func pdfGradientSegments(stops []pdfgo.GradientStop, space *pdfgo.ColorSpace) ([]ShdSegment, error) {
	if len(stops) == 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "nonlinear gradient conversion"}
	}
	calibrated := space != nil && space.Calibrated() && !space.SRGBEquivalent()
	cmyk := space != nil && space.Model == "DeviceCMYK" && !space.Calibrated()
	segments := make([]ShdSegment, 0, len(stops))
	appendStop := func(position float64, rgb [3]float64) {
		segments = append(segments, ShdSegment{Position: position, Color: ShdColor{Value: pdfNumbers(math.Round(rgb[0]*255), math.Round(rgb[1]*255), math.Round(rgb[2]*255))}})
	}
	for i, b := range stops {
		if i > 0 {
			a := stops[i-1]
			if a.Position != b.Position && a.Values != b.Values {
				if calibrated {
					return nil, &pdfgo.UnsupportedError{Feature: "nonlinear ICC gradient conversion"}
				}
				if cmyk {
					var crossings []float64
					for c := 0; c < 3; c++ {
						start, end := a.Values[c]+a.Values[3], b.Values[c]+b.Values[3]
						if start != end {
							if t := (1 - start) / (end - start); t > 0 && t < 1 {
								crossings = append(crossings, t)
							}
						}
					}
					slices.Sort(crossings)
					for _, t := range slices.Compact(crossings) {
						var values [4]float64
						for c := range values {
							values[c] = a.Values[c]*(1-t) + b.Values[c]*t
						}
						rgb, err := space.RGB(values[:], "RelativeColorimetric")
						if err != nil {
							return nil, err
						}
						appendStop(a.Position+(b.Position-a.Position)*t, rgb)
					}
				}
			}
		}
		appendStop(b.Position, b.RGB)
	}
	return segments, nil
}

// pdfGradientError 检查渐变能否精确映射为OFD线性分段
// 入参: paint 画刷
// 返回: error 不可等价表达的渐变
func pdfGradientError(paint pdfgo.Paint) error {
	if paint.Axial != nil {
		_, err := pdfGradientSegments(paint.Axial.Stops, paint.Axial.Space)
		return err
	}
	if paint.Radial != nil {
		_, err := pdfGradientSegments(paint.Radial.Stops, paint.Radial.Space)
		return err
	}
	return nil
}

// gradientPath 按原始函数合成渐变路径，保留填充描边的单一对象语义
// 入参: mark 路径绘制信息
// 返回: error 几何或着色错误
func (p *pdfImporter) gradientPath(mark pdfgo.PathMark) error {
	return p.compositeRegion(nil, pdfCompositeNode{path: &mark}, &pdfgo.ColorSpace{Model: "DeviceRGB"}, false)
}

// pdfShadingPosition 计算渐变参数，未延伸区域及不存在非负圆半径的位置不着色
// 入参: paint 渐变画刷, point 页面坐标
// 返回: float64 归一化参数, bool 是否着色
func pdfShadingPosition(paint pdfgo.Paint, point pdfgo.Point) (float64, bool) {
	if g := paint.Axial; g != nil {
		dx, dy := g.End.X-g.Start.X, g.End.Y-g.Start.Y
		t := ((point.X-g.Start.X)*dx + (point.Y-g.Start.Y)*dy) / (dx*dx + dy*dy)
		return t, finite(t) && (t >= 0 || g.Extend[0]) && (t <= 1 || g.Extend[1])
	}
	g := paint.Radial
	dx, dy, dr := g.End.X-g.Start.X, g.End.Y-g.Start.Y, g.EndRadius-g.StartRadius
	px, py := point.X-g.Start.X, point.Y-g.Start.Y
	a, b, c := dx*dx+dy*dy-dr*dr, -2*(px*dx+py*dy+g.StartRadius*dr), px*px+py*py-g.StartRadius*g.StartRadius
	roots := [2]float64{math.NaN(), math.NaN()}
	if a == 0 {
		if b != 0 {
			roots[0] = -c / b
		}
	} else if discriminant := b*b - 4*a*c; discriminant >= 0 {
		q := -.5 * (b + math.Copysign(math.Sqrt(discriminant), b))
		if q == 0 {
			roots[0] = -b / (2 * a)
		} else {
			roots[0], roots[1] = q/a, c/q
		}
	}
	t := math.Inf(-1)
	for _, root := range roots {
		if finite(root) && g.StartRadius+root*dr >= 0 && (root >= 0 || g.Extend[0]) && (root <= 1 || g.Extend[1]) && root > t {
			t = root
		}
	}
	return t, finite(t)
}
