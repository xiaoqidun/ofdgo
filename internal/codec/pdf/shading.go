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

package pdf

import (
	"github.com/xiaoqidun/pdfgo"
)

// ShadingPixel 保存抗锯齿覆盖率与混合空间分量
type ShadingPixel struct {
	Values [4]float64
	Shape  float64
}

// ShadingColorants 复用原始浓度缓冲及原生通道映射，不跨并发采样共享
type ShadingColorants struct {
	paint   pdfgo.Paint
	Process *pdfgo.ColorantProcess
	tints   []float64
	mesh    *pdfgo.MeshSampler
}

// PrepareShadingColorants 为当前组建立原生过程采样，备用色保留原路径
// 入参: paint 渐变画刷, space 组空间, softMask 是否用于软蒙版
// 返回: *ShadingColorants 独立采样状态，不可原生映射时为nil
func PrepareShadingColorants(paint pdfgo.Paint, space *pdfgo.ColorSpace, softMask bool) *ShadingColorants {
	colorants := ShadingColorantSpace(paint)
	process := colorants.PrepareProcess(&pdfgo.ColorantDevice{Space: space}, space, softMask)
	if process == nil || process.Mask() == ([4]bool{}) {
		return nil
	}
	s := &ShadingColorants{paint: paint, Process: process, tints: make([]float64, len(colorants.Names))}
	if paint.Mesh != nil {
		s.mesh = paint.Mesh.NewSampler()
	}
	return s
}

// At 在归一化位置求值轴向或径向原始浓度
// 入参: position 归一化渐变参数
// 返回: [4]float64 原生过程分量, error 求值错误
func (s *ShadingColorants) At(position float64) ([4]float64, error) {
	var err error
	if s.paint.Axial != nil {
		err = s.paint.Axial.ColorantValuesAt(position, s.tints)
	} else {
		err = s.paint.Radial.ColorantValuesAt(position, s.tints)
	}
	if err != nil {
		return [4]float64{}, err
	}
	return s.Process.Convert(s.tints)
}

// PointAt 在函数定义域内求值原始浓度
// 入参: point 函数坐标
// 返回: [4]float64 原生过程分量, error 求值错误
func (s *ShadingColorants) PointAt(point pdfgo.Point) ([4]float64, error) {
	if err := s.paint.Function.ColorantValuesAt(point, s.tints); err != nil {
		return [4]float64{}, err
	}
	return s.Process.Convert(s.tints)
}

// MeshAt 插值网格原始浓度并复用函数结果
// 入参: patch 网格序号, u 横向参数, v 纵向参数
// 返回: [4]float64 原生过程分量, error 求值错误
func (s *ShadingColorants) MeshAt(patch int, u, v float64) ([4]float64, error) {
	if err := s.mesh.ColorantValuesAt(patch, u, v, s.tints); err != nil {
		return [4]float64{}, err
	}
	return s.Process.Convert(s.tints)
}

// ShadingColorantSpace 返回渐变的源色料定义，不使用备用颜色推断通道
// 入参: paint 渐变画刷
// 返回: *pdfgo.ColorantSpace 原始色料定义，普通渐变为nil
func ShadingColorantSpace(paint pdfgo.Paint) *pdfgo.ColorantSpace {
	switch {
	case paint.Axial != nil:
		return paint.Axial.ColorantSpace()
	case paint.Radial != nil:
		return paint.Radial.ColorantSpace()
	case paint.Function != nil:
		return paint.Function.ColorantSpace()
	case paint.Mesh != nil:
		return paint.Mesh.ColorantSpace()
	}
	return nil
}

// ShadingPosition 计算渐变参数，未延伸区域及不存在非负圆半径的位置不着色
// 入参: paint 渐变画刷, point 页面坐标
// 返回: float64 归一化参数, bool 是否着色
func ShadingPosition(paint pdfgo.Paint, point pdfgo.Point) (float64, bool) {
	var geometry pdfgo.GradientPosition
	var err error
	if g := paint.Axial; g != nil {
		geometry, err = g.PreparePosition()
	} else {
		geometry, err = paint.Radial.PreparePosition()
	}
	if err != nil {
		return 0, false
	}
	return geometry.PositionAt(point)
}
