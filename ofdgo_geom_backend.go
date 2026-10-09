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

// geometryRectangleBounds 识别单个轴对齐矩形，不用包围盒代替实际路径
// 入参: path 填充路径
// 返回: Box 矩形范围, bool 是否为非退化矩形
func geometryRectangleBounds(path GeometryPath) (Box, bool) {
	if len(path) > 0 && path[len(path)-1].Verb == GeometryClose {
		path = path[:len(path)-1]
	}
	if len(path) == 5 && path[4].Verb == GeometryLine && path[4].End == path[0].End {
		path = path[:4]
	}
	if len(path) != 4 || path[0].Verb != GeometryMove || path[1].Verb != GeometryLine || path[2].Verb != GeometryLine || path[3].Verb != GeometryLine {
		return Box{}, false
	}
	a, b, c, d := path[0].End, path[1].End, path[2].End, path[3].End
	for _, point := range [...]Point{a, b, c, d} {
		if !finite(point.X) || !finite(point.Y) {
			return Box{}, false
		}
	}
	if a.X == c.X || a.Y == c.Y {
		return Box{}, false
	}
	if !(a.X == b.X && b.Y == c.Y && c.X == d.X && d.Y == a.Y || a.Y == b.Y && b.X == c.X && c.Y == d.Y && d.X == a.X) {
		return Box{}, false
	}
	width, height := math.Abs(c.X-a.X), math.Abs(c.Y-a.Y)
	if !finite(width) || !finite(height) {
		return Box{}, false
	}
	return Box{X: min(a.X, c.X), Y: min(a.Y, c.Y), W: width, H: height}, true
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
