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

	"github.com/xiaoqidun/pdfgo"
)

// gradientSegments 保留线性源颜色的设备分量，校准颜色经源空间转换
// 入参: stops 源颜色分段, space 源颜色空间
// 返回: []ShdSegment OFD颜色分段, error 不可线性表达的变换
func (p *pdfImporter) gradientSegments(stops []pdfgo.GradientStop, space *pdfgo.ColorSpace) ([]ShdSegment, error) {
	if err := pdfGradientStopsError(stops, space); err != nil {
		return nil, err
	}
	cmyk := space != nil && space.Model == "DeviceCMYK" && !space.Calibrated()
	segments := make([]ShdSegment, len(stops))
	for i, stop := range stops {
		paint := pdfgo.Paint{RGB: stop.RGB, Alpha: 1}
		if cmyk {
			paint.CMYK = &stop.Values
		}
		color := p.color(paint)
		segments[i] = ShdSegment{Position: stop.Position, Color: ShdColor{Value: color.Value, ColorSpace: color.ColorSpace}}
	}
	return segments, nil
}

// pdfGradientStopsError 检查源颜色分段是否可由OFD基本颜色表达
// 入参: stops 源颜色分段, space 源颜色空间
// 返回: error 不可线性表达的变换
func pdfGradientStopsError(stops []pdfgo.GradientStop, space *pdfgo.ColorSpace) error {
	if len(stops) == 0 {
		return &pdfgo.UnsupportedError{Feature: "nonlinear gradient conversion"}
	}
	calibrated := space != nil && space.Calibrated() && !space.SRGBEquivalent()
	if calibrated {
		for i := 1; i < len(stops); i++ {
			a, b := stops[i-1], stops[i]
			if a.Position != b.Position && a.Values != b.Values {
				return &pdfgo.UnsupportedError{Feature: "nonlinear ICC gradient conversion"}
			}
		}
	}
	return nil
}

// pdfGradientError 检查渐变能否精确映射为OFD线性分段
// 入参: paint 画刷
// 返回: error 不可等价表达的渐变
func pdfGradientError(paint pdfgo.Paint) error {
	if paint.Mesh != nil {
		mesh := paint.Mesh
		if len(mesh.Patches) != 0 || len(mesh.Triangles) == 0 || mesh.UsesFunction() {
			return &pdfgo.UnsupportedError{Feature: "nonlinear mesh conversion"}
		}
		if mesh.Space.Calibrated() && !mesh.Space.SRGBEquivalent() {
			return &pdfgo.UnsupportedError{Feature: "nonlinear ICC mesh conversion"}
		}
	}
	if paint.Axial != nil {
		return pdfGradientStopsError(paint.Axial.Stops, paint.Axial.Space)
	}
	if paint.Radial != nil {
		return pdfGradientStopsError(paint.Radial.Stops, paint.Radial.Space)
	}
	return nil
}

// gradientPath 按原始函数合成渐变路径，保留填充描边的单一对象语义
// 入参: mark 路径绘制信息
// 返回: error 几何或着色错误
func (p *pdfImporter) gradientPath(mark pdfgo.PathMark) error {
	cmyk := true
	for i, paint := range []pdfgo.Paint{mark.Style.Fill, mark.Style.Stroke} {
		if i == 0 && !mark.Fill || i == 1 && !mark.Stroke {
			continue
		}
		space := paint.Space
		switch {
		case paint.Axial != nil:
			space = paint.Axial.Space
		case paint.Radial != nil:
			space = paint.Radial.Space
		case paint.Mesh != nil:
			space = paint.Mesh.Space
		}
		if paint.CMYK == nil && (space == nil || space.Model != "DeviceCMYK" || space.Calibrated()) {
			cmyk = false
			break
		}
	}
	space := &pdfgo.ColorSpace{Model: "DeviceRGB"}
	if cmyk {
		space.Model = "DeviceCMYK"
	}
	return p.compositeRegion(nil, pdfCompositeNode{path: &mark}, space, false)
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
	if g.Matrix != (pdfgo.Matrix{}) {
		inverse, ok := g.Matrix.Inverse()
		if !ok {
			return 0, false
		}
		point = inverse.Apply(point)
	}
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
