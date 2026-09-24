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
	"strconv"

	"github.com/tdewolff/canvas"
)

// canvasNativeStroke 判断描边是否由Canvas提供
// 入参: geometry 几何后端
// 返回: bool 是否采用原生描边
func canvasNativeStroke(geometry GeometryBackend) bool {
	_, ok := geometry.(CanvasBackend)
	return ok
}

// Curves 在输出边界转换椭圆弧，其他曲线保持原样
// 入参: path 页面路径
// 返回: GeometryPath 贝塞尔路径, error 路径错误
func (CanvasBackend) Curves(path GeometryPath) (GeometryPath, error) {
	p, err := geometryToCanvasPath(&path)
	if err != nil {
		return nil, err
	}
	return *geometryFromCanvasPath(p.ReplaceArcs()), nil
}

// canvasStrokeOptions 转换Canvas描边样式，虚线由调用方传入
// 入参: width 描边宽度, cap 线帽, join 连接, tolerance 展开误差
// 返回: StrokeOptions 公共描边样式
func canvasStrokeOptions(width float64, cap canvas.Capper, join canvas.Joiner, tolerance float64) StrokeOptions {
	options := StrokeOptions{Width: width, Cap: "Butt", Join: "Miter", MiterLimit: defaultMiterLimit, Tolerance: tolerance}
	switch cap.(type) {
	case canvas.RoundCapper:
		options.Cap = "Round"
	case canvas.SquareCapper:
		options.Cap = "Square"
	}
	switch value := join.(type) {
	case canvas.RoundJoiner:
		options.Join = "Round"
	case canvas.BevelJoiner:
		options.Join = "Bevel"
	case canvas.MiterJoiner:
		options.MiterLimit = value.Limit
	}
	return options
}

// canvasStrokePath 通过配置的几何后端生成描边，不修改源路径
// 入参: geometry 几何后端, path Canvas路径, options 描边样式
// 返回: *canvas.Path 描边轮廓, error 几何错误
func canvasStrokePath(geometry GeometryBackend, path *canvas.Path, options StrokeOptions) (*canvas.Path, error) {
	outline, err := geometry.Stroke(*geometryFromCanvasPath(path), options)
	if err != nil {
		return nil, err
	}
	return geometryToCanvasPath(&outline)
}

// strokeCanvasPath 将文字和复杂画刷的描边交给配置的几何后端
// 入参: path Canvas路径, width 描边宽度, cap 线帽, join 连接
// 返回: *canvas.Path 描边轮廓
func (r *Renderer) strokeCanvasPath(path *canvas.Path, width float64, cap canvas.Capper, join canvas.Joiner) *canvas.Path {
	geometry, err := r.Geometry()
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	result, err := canvasStrokePath(geometry, path, canvasStrokeOptions(width, cap, join, canvas.Tolerance))
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	return result
}

// Path 解析对象的标准紧缩路径并转换到页面坐标
// 入参: object 路径对象
// 返回: GeometryPath 页面路径, error 无效路径错误
func (b CanvasBackend) Path(object PathObject) (GeometryPath, error) {
	return objectGeometryPath(b, object)
}

// Region 解析动作区域并保持椭圆弧及退化变换的既有处理
// 入参: region 动作区域, matrix 页面变换
// 返回: GeometryPath 页面路径, error 解析错误
func (CanvasBackend) Region(region *Region, matrix Matrix) (GeometryPath, error) {
	p := actionRegionPath(region, matrix)
	return *geometryFromCanvasPath(p.Transform(canvas.Matrix{{1, 0, 0}, {0, -1, 0}})), nil
}

// Bounds 计算曲线路径的精确范围
// 入参: path 页面路径
// 返回: Box 路径范围, error 无效路径错误
func (CanvasBackend) Bounds(path GeometryPath) (Box, error) {
	p, err := geometryToCanvasPath(&path)
	if err != nil {
		return Box{}, err
	}
	if p.Empty() {
		return Box{}, nil
	}
	box := p.Bounds()
	return Box{X: box.X0, Y: -box.Y1, W: box.W(), H: box.H()}, nil
}

// Transform 变换路径，保持曲线与椭圆弧，不修改源路径
// 入参: path 页面路径, matrix 页面变换
// 返回: GeometryPath 新路径, error 无效路径错误
func (CanvasBackend) Transform(path GeometryPath, matrix Matrix) (GeometryPath, error) {
	p, err := geometryToCanvasPath(&path)
	if err != nil {
		return nil, err
	}
	m := matrix.Values()
	for _, value := range m {
		if !finite(value) {
			return nil, fmt.Errorf("invalid geometry transform")
		}
	}
	if geometryEqual(m[0]*m[3]-m[1]*m[2], 0) {
		p = p.ReplaceArcs()
	}
	p = p.Transform(canvas.Matrix{{m[0], -m[2], m[4]}, {-m[1], m[3], -m[5]}})
	return *geometryFromCanvasPath(p), nil
}

// Normalize 按填充规则整理自交区域
// 入参: path 页面路径, evenOdd 是否采用奇偶规则
// 返回: GeometryPath 非零填充路径, error 无效路径错误
func (CanvasBackend) Normalize(path GeometryPath, evenOdd bool) (GeometryPath, error) {
	p, err := geometryToCanvasPath(&path)
	if err != nil {
		return nil, err
	}
	rule := canvas.NonZero
	if evenOdd {
		rule = canvas.EvenOdd
	}
	p = p.Settle(rule)
	if p.Empty() {
		return nil, nil
	}
	return *geometryFromCanvasPath(p), nil
}

// Combine 计算两个非零填充区域的布尔运算
// 入参: left 左路径, right 右路径, operation 运算类型
// 返回: GeometryPath 结果路径, error 运算错误
func (CanvasBackend) Combine(left, right GeometryPath, operation GeometryOperation) (GeometryPath, error) {
	a, err := geometryToCanvasPath(&left)
	if err != nil {
		return nil, err
	}
	b, err := geometryToCanvasPath(&right)
	if err != nil {
		return nil, err
	}
	var result *canvas.Path
	switch operation {
	case GeometryIntersect:
		result = a.And(b)
	case GeometryUnion:
		result = a.Or(b)
	case GeometrySubtract:
		result = a.Not(b)
	case GeometryXor:
		result = a.Xor(b)
	default:
		return nil, fmt.Errorf("unsupported geometry operation %d", operation)
	}
	if result.Empty() {
		return nil, nil
	}
	return *geometryFromCanvasPath(result), nil
}

// Stroke 将绝对长度虚线与描边转换为填充区域
// 入参: path 页面路径, options 描边样式
// 返回: GeometryPath 描边区域, error 样式或路径错误
func (CanvasBackend) Stroke(path GeometryPath, options StrokeOptions) (GeometryPath, error) {
	if err := validateStroke(options); err != nil {
		return nil, err
	}
	p, err := geometryToCanvasPath(&path)
	if err != nil {
		return nil, err
	}
	tolerance := options.Tolerance
	if tolerance == 0 {
		tolerance = canvas.Tolerance
	}
	style := pathStyle{lineJoin: canvas.MiterJoin, lineCap: canvas.ButtCap, miterLimit: defaultMiterLimit}
	style.applyLineJoin(options.Join, options.MiterLimit)
	style.lineCap = pathLineCap(options.Cap, style.lineCap)
	p = p.Dash(options.DashOffset, options.Dashes...).Stroke(options.Width, style.lineCap, style.lineJoin, tolerance)
	return *geometryFromCanvasPath(p), nil
}

// Clip 解析对象裁剪并与父裁剪相交，保留未裁剪与完全裁去的区别
// 入参: r 渲染器, clips OFD裁剪, matrix 页面变换, parent 父裁剪
// 返回: *GeometryPath 页面裁剪, error 无效父路径错误
func (CanvasBackend) Clip(r *Renderer, clips *Clips, matrix Matrix, parent *GeometryPath) (*GeometryPath, error) {
	p, err := geometryToCanvasPath(parent)
	if err != nil {
		return nil, err
	}
	renderer := *r
	renderer.renderError = nil
	clip := renderer.buildClipPath(clips, 0, 0, 0, matrix)
	if renderer.renderError != nil {
		return nil, renderer.renderError
	}
	return geometryFromCanvasPath(intersectClipPath(p, clip)), nil
}

// geometryToCanvasPath 将页面坐标路径转换为默认几何引擎的向上纵轴
// 入参: path 独立几何路径，nil表示不裁剪
// 返回: *canvas.Path 默认引擎路径, error 不支持的路径指令
func geometryToCanvasPath(path *GeometryPath) (*canvas.Path, error) {
	if path == nil {
		return nil, nil
	}
	p := &canvas.Path{}
	for _, s := range *path {
		switch s.Verb {
		case GeometryMove:
			p.MoveTo(s.End.X, -s.End.Y)
		case GeometryLine:
			p.LineTo(s.End.X, -s.End.Y)
		case GeometryQuad:
			p.QuadTo(s.Control1.X, -s.Control1.Y, s.End.X, -s.End.Y)
		case GeometryCubic:
			p.CubeTo(s.Control1.X, -s.Control1.Y, s.Control2.X, -s.Control2.Y, s.End.X, -s.End.Y)
		case GeometryArc:
			p.ArcTo(s.RadiusX, s.RadiusY, -s.Rotation, s.Large, !s.Sweep, s.End.X, -s.End.Y)
		case GeometryClose:
			p.Close()
		default:
			return nil, fmt.Errorf("unsupported geometry command %d", s.Verb)
		}
	}
	return p, nil
}

// canvasObjectPath 建立对象局部路径，保留折返线和退化贝塞尔的原始端点
// 入参: path 独立路径
// 返回: *canvas.Path 适配路径, error 不支持的指令
func canvasObjectPath(path GeometryPath) (*canvas.Path, error) {
	var data []float64
	for _, s := range path {
		end := s.End
		switch s.Verb {
		case GeometryMove:
			data = append(data, canvas.MoveToCmd, end.X, end.Y, canvas.MoveToCmd)
		case GeometryLine:
			data = append(data, canvas.LineToCmd, end.X, end.Y, canvas.LineToCmd)
		case GeometryQuad:
			data = append(data, canvas.QuadToCmd, s.Control1.X, s.Control1.Y, end.X, end.Y, canvas.QuadToCmd)
		case GeometryCubic:
			data = append(data, canvas.CubeToCmd, s.Control1.X, s.Control1.Y, s.Control2.X, s.Control2.Y, end.X, end.Y, canvas.CubeToCmd)
		case GeometryArc:
			p := canvas.NewPathFromData(data)
			p.ArcTo(s.RadiusX, s.RadiusY, s.Rotation, s.Large, s.Sweep, end.X, end.Y)
			data = p.Data()
		case GeometryClose:
			p := canvas.NewPathFromData(data)
			p.Close()
			data = p.Data()
		default:
			return nil, fmt.Errorf("unsupported geometry command %d", s.Verb)
		}
	}
	return canvas.NewPathFromData(data), nil
}

// geometryFromCanvasPath 将默认引擎路径转换为独立的页面坐标路径，保留曲线和椭圆弧
// 入参: path 默认引擎路径
// 返回: *GeometryPath 独立路径，nil表示不裁剪
func geometryFromCanvasPath(path *canvas.Path) *GeometryPath {
	if path == nil {
		return nil
	}
	result := make(GeometryPath, 0, path.Len())
	scanner := path.Scanner()
	for scanner.Scan() {
		end := scanner.End()
		segment := GeometrySegment{End: Point{X: end.X, Y: -end.Y}}
		switch scanner.Cmd() {
		case canvas.MoveToCmd:
			segment.Verb = GeometryMove
		case canvas.LineToCmd:
			segment.Verb = GeometryLine
		case canvas.QuadToCmd:
			control := scanner.CP1()
			segment.Verb, segment.Control1 = GeometryQuad, Point{X: control.X, Y: -control.Y}
		case canvas.CubeToCmd:
			c1, c2 := scanner.CP1(), scanner.CP2()
			segment.Verb = GeometryCubic
			segment.Control1, segment.Control2 = Point{X: c1.X, Y: -c1.Y}, Point{X: c2.X, Y: -c2.Y}
		case canvas.ArcToCmd:
			segment.Verb = GeometryArc
			segment.RadiusX, segment.RadiusY, segment.Rotation, segment.Large, segment.Sweep = scanner.Arc()
			segment.Rotation, segment.Sweep = -segment.Rotation, !segment.Sweep
		case canvas.CloseCmd:
			segment.Verb = GeometryClose
		}
		result = append(result, segment)
	}
	return &result
}

// actionCanvasMatrix 转换页面坐标矩阵，不翻转Y轴
// 入参: matrix 页面变换
// 返回: canvas.Matrix 路径变换
func actionCanvasMatrix(matrix Matrix) canvas.Matrix {
	return canvas.Matrix{{matrix.a, matrix.c, matrix.e}, {matrix.b, matrix.d, matrix.f}}
}

// actionRegionPath 解析复杂点击区域并转换为页面坐标
// 入参: region 动作区域, matrix 坐标变换
// 返回: *canvas.Path 点击路径
func actionRegionPath(region *Region, matrix Matrix) *canvas.Path {
	path := &canvas.Path{}
	for _, area := range region.Area {
		start := parseFloats(area.Start)
		if len(start) != 2 {
			continue
		}
		part := &canvas.Path{}
		part.MoveTo(start[0], start[1])
		for _, command := range area.Command {
			p1, p2, p3 := parseFloats(command.Point1), parseFloats(command.Point2), parseFloats(command.Point3)
			switch command.Type {
			case "Move":
				if len(p1) == 2 {
					part.Close()
					part.MoveTo(p1[0], p1[1])
				}
			case "Line":
				if len(p1) == 2 {
					part.LineTo(p1[0], p1[1])
				}
			case "QuadraticBezier":
				if len(p1) == 2 && len(p2) == 2 {
					part.QuadTo(p1[0], p1[1], p2[0], p2[1])
				}
			case "CubicBezier":
				if len(p3) == 2 {
					first, second := part.Pos(), canvas.Point{X: p3[0], Y: p3[1]}
					if len(p1) == 2 {
						first = canvas.Point{X: p1[0], Y: p1[1]}
					}
					if len(p2) == 2 {
						second = canvas.Point{X: p2[0], Y: p2[1]}
					}
					part.CubeTo(first.X, first.Y, second.X, second.Y, p3[0], p3[1])
				}
			case "Arc":
				size, end := parseFloats(command.EllipseSize), parseFloats(command.EndPoint)
				angle, err := strconv.ParseFloat(command.RotationAngle, 64)
				large, largeErr := strconv.ParseBool(command.LargeArc)
				sweep, sweepErr := strconv.ParseBool(command.SweepDirection)
				if len(end) == 2 && err == nil && largeErr == nil && sweepErr == nil {
					rx, ry := 0.0, 0.0
					if len(size) > 0 {
						rx, ry = size[0], size[0]
					}
					if len(size) > 1 {
						ry = size[1]
					}
					part.ArcTo(rx, ry, angle, large, sweep, end[0], end[1])
				}
			case "Close":
				part.Close()
			}
		}
		part.Close()
		path = path.Append(part)
	}
	if canvas.Equal(matrix.a*matrix.d-matrix.b*matrix.c, 0) {
		path = path.ReplaceArcs()
	}
	return path.Transform(actionCanvasMatrix(matrix))
}
