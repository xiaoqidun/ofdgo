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

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
)

// 路径填充区域的交集、并集、差集和异或运算
const (
	GeometryIntersect GeometryOperation = iota
	GeometryUnion
	GeometrySubtract
	GeometryXor
)

// GeometryOperation 指定路径填充区域的布尔运算
type GeometryOperation uint8

// StrokeOptions 描述描边参数，长度单位为毫米
// Width须为正有限数；Cap和Join使用OFD名称，空值分别采用Butt和Miter
// MiterLimit为尖角长度与半线宽的比值上限，MiterLimitSet区分显式零值与默认值
// DashOffset为虚线起始偏移，允许负值；Dashes交替指定实线段和间隔长度，奇数项按重复一遍处理
// Tolerance为曲线展开的误差目标，0采用后端默认精度
type StrokeOptions struct {
	Width         float64
	Cap, Join     string
	MiterLimit    float64
	MiterLimitSet bool
	DashOffset    float64
	Tolerance     float64
	Dashes        []float64
}

// GeometryBackend 操作库自有路径，坐标以页面左上角为原点，不修改任何输入
// Clip返回nil表示不裁剪，空路径指针表示完全裁去，矩阵将裁剪坐标映射到页面坐标
type GeometryBackend interface {
	Backend
	Path(object PathObject) (GeometryPath, error)
	Region(region *Region, matrix Matrix) (GeometryPath, error)
	Bounds(path GeometryPath) (Box, error)
	Transform(path GeometryPath, matrix Matrix) (GeometryPath, error)
	Normalize(path GeometryPath, evenOdd bool) (GeometryPath, error)
	Combine(left, right GeometryPath, operation GeometryOperation) (GeometryPath, error)
	Stroke(path GeometryPath, options StrokeOptions) (GeometryPath, error)
	Clip(renderer *Renderer, clips *Clips, matrix Matrix, parent *GeometryPath) (*GeometryPath, error)
}

// GeometryCurves 将椭圆弧转换为光栅后端可消费的贝塞尔曲线，不改变源路径
type GeometryCurves interface {
	Curves(path GeometryPath) (GeometryPath, error)
}

// clipGeometry 将非零填充路径与页面裁剪相交
// 入参: geometry 几何后端, path 页面路径, clip 页面裁剪
// 返回: GeometryPath 裁剪路径, error 几何错误
func clipGeometry(geometry GeometryBackend, path GeometryPath, clip *GeometryPath) (GeometryPath, error) {
	if clip == nil {
		return path, nil
	}
	if len(*clip) == 0 {
		return nil, nil
	}
	if rect, ok := geometryRectangleBounds(*clip); ok {
		if bounds, ok := geometryRectangleBounds(path); ok {
			left, top := max(rect.X, bounds.X), max(rect.Y, bounds.Y)
			right, bottom := min(rect.X+rect.W, bounds.X+bounds.W), min(rect.Y+rect.H, bounds.Y+bounds.H)
			if right <= left || bottom <= top {
				return nil, nil
			}
			return geometryRectangle(Box{X: left, Y: top, W: right - left, H: bottom - top}), nil
		}
		if err := path.validate(); err != nil {
			return nil, err
		}
		contained := true
		for _, segment := range path {
			if segment.Verb == GeometryArc {
				contained = false
				break
			}
			points := [3]Point{segment.End, segment.Control1, segment.Control2}
			count := 1
			if segment.Verb == GeometryQuad {
				count = 2
			} else if segment.Verb == GeometryCubic {
				count = 3
			} else if segment.Verb == GeometryClose {
				continue
			}
			for _, point := range points[:count] {
				if point.X < rect.X || point.Y < rect.Y || point.X > rect.X+rect.W || point.Y > rect.Y+rect.H {
					contained = false
					break
				}
			}
			if !contained {
				break
			}
		}
		if contained {
			return path, nil
		}
	}
	if rect, ok := geometryRectangleBounds(path); ok {
		bounds, err := geometry.Bounds(*clip)
		if err != nil {
			return nil, err
		}
		if bounds.X >= rect.X && bounds.Y >= rect.Y && bounds.X+bounds.W <= rect.X+rect.W && bounds.Y+bounds.H <= rect.Y+rect.H {
			return *clip, nil
		}
	}
	return geometry.Combine(path, *clip, GeometryIntersect)
}

// validateStrokeOptions 校验描边选项的合法性
// 入参: options 描边样式
// 返回: error 参数错误
func validateStrokeOptions(options StrokeOptions) error {
	return basegeom.ValidateStroke(basegeom.StrokeOptions(options))
}

// Geometry 返回当前几何后端，不隐式恢复默认实现
// 返回: GeometryBackend 几何后端, error 未配置错误
func (r *Renderer) Geometry() (GeometryBackend, error) {
	if r.backends.Geometry == nil {
		return nil, fmt.Errorf("geometry: %w", ErrBackendUnavailable)
	}
	return r.backends.Geometry, nil
}

// PathOutline 使用配置的几何后端生成页面坐标中的SVG轮廓
// 入参: object 路径对象
// 返回: string SVG路径, error 几何或编码错误
func (r *Renderer) PathOutline(object PathObject) (string, error) {
	geometry, err := r.Geometry()
	if err != nil {
		return "", err
	}
	path, err := geometry.Path(object)
	if err != nil {
		return "", err
	}
	return path.SVG()
}

// Geometry 返回编辑器配置的几何后端
// 返回: GeometryBackend 几何后端, error 未配置错误
func (e *Editor) Geometry() (GeometryBackend, error) {
	if e.backends.Geometry == nil {
		return nil, fmt.Errorf("geometry: %w", ErrBackendUnavailable)
	}
	return e.backends.Geometry, nil
}
