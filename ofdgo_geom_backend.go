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

import "fmt"

// GeometryOperation 指定路径填充区域的布尔运算
type GeometryOperation uint8

const (
	GeometryIntersect GeometryOperation = iota
	GeometryUnion
	GeometrySubtract
	GeometryXor
)

// StrokeOptions 描述描边参数，长度单位为毫米
type StrokeOptions struct {
	// Width为正有限描边宽度
	Width float64
	// Cap和Join使用OFD名称，空值分别采用Butt和Miter
	Cap, Join string
	// MiterLimit为尖角长度与半线宽的比值上限，0采用默认值
	MiterLimit float64
	// DashOffset为绝对长度的虚线相位，允许负值
	DashOffset float64
	// Tolerance为曲线展开误差目标，0采用后端默认精度
	Tolerance float64
	// Dashes为交替绘制和留空的绝对长度，奇数项按重复一遍处理
	Dashes []float64
}

// GeometryBackend 操作库自有路径，坐标以页面左上角为原点，不修改任何输入
// Clip返回nil表示不裁剪，空路径指针表示完全裁去，矩阵映射到页面坐标
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

// validateStroke 校验各后端共用的描边参数
// 入参: options 描边样式
// 返回: error 无效样式
func validateStroke(options StrokeOptions) error {
	if !finite(options.Width) || options.Width <= 0 || !finite(options.Tolerance) || options.Tolerance < 0 || !finite(options.DashOffset) || !finite(options.MiterLimit) || options.MiterLimit < 0 {
		return fmt.Errorf("invalid stroke width or tolerance")
	}
	if options.Cap != "" && options.Cap != "Butt" && options.Cap != "Round" && options.Cap != "Square" {
		return fmt.Errorf("invalid stroke cap %q", options.Cap)
	}
	if options.Join != "" && options.Join != "Miter" && options.Join != "Round" && options.Join != "Bevel" {
		return fmt.Errorf("invalid stroke join %q", options.Join)
	}
	for _, dash := range options.Dashes {
		if !finite(dash) || dash < 0 {
			return fmt.Errorf("invalid stroke dash")
		}
	}
	return nil
}

// Geometry 返回当前几何后端，不隐式恢复默认实现
// 返回: GeometryBackend 几何后端, error 未配置错误
func (r *Renderer) Geometry() (GeometryBackend, error) {
	if r.backends.Geometry == nil {
		return nil, fmt.Errorf("geometry: %w", ErrBackendUnavailable)
	}
	return r.backends.Geometry, nil
}

// Geometry 返回编辑器配置的几何后端
// 返回: GeometryBackend 几何后端, error 未配置错误
func (e *Editor) Geometry() (GeometryBackend, error) {
	if e.backends.Geometry == nil {
		return nil, fmt.Errorf("geometry: %w", ErrBackendUnavailable)
	}
	return e.backends.Geometry, nil
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

// geometryRectangle 构建页面坐标矩形，保留输入精度
// 入参: box 矩形范围
// 返回: GeometryPath 闭合路径
func geometryRectangle(box Box) GeometryPath {
	return GeometryPath{
		{Verb: GeometryMove, End: Point{X: box.X, Y: box.Y}},
		{Verb: GeometryLine, End: Point{X: box.X + box.W, Y: box.Y}},
		{Verb: GeometryLine, End: Point{X: box.X + box.W, Y: box.Y + box.H}},
		{Verb: GeometryLine, End: Point{X: box.X, Y: box.Y + box.H}},
		{Verb: GeometryClose, End: Point{X: box.X, Y: box.Y}},
	}
}
