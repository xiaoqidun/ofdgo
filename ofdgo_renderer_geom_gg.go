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

	"github.com/gogpu/gg"
)

// GGGeometryBackend 使用GG处理贝塞尔边界和变换，其他运算由显式配置的几何后端承担
// GeometryBackend不可为空，椭圆弧保持精确表示，不为接入GG而预先离散化
type GGGeometryBackend struct{ GeometryBackend }

// Name 返回实际几何提供者组合
// 返回: string 后端组合标识
func (b GGGeometryBackend) Name() string { return "gg+" + backendName(b.GeometryBackend) }

// Curves 通过显式配置的曲线适配器展开椭圆弧
// 入参: path 页面路径
// 返回: GeometryPath 贝塞尔路径, error 能力或路径错误
func (b GGGeometryBackend) Curves(path GeometryPath) (GeometryPath, error) {
	curves, ok := b.GeometryBackend.(GeometryCurves)
	if !ok {
		return nil, fmt.Errorf("geometry curves: %w", ErrBackendUnavailable)
	}
	return curves.Curves(path)
}

// Bounds 使用GG计算贝塞尔曲线极值，椭圆弧交给配置的几何后端
// 入参: path 页面路径
// 返回: Box 精确范围, error 路径或能力错误
func (b GGGeometryBackend) Bounds(path GeometryPath) (Box, error) {
	for _, s := range path {
		if s.Verb == GeometryArc {
			if b.GeometryBackend == nil {
				return Box{}, ErrBackendUnavailable
			}
			return b.GeometryBackend.Bounds(path)
		}
	}
	p, err := ggGeometryPath(path)
	if err != nil {
		return Box{}, err
	}
	if len(path) == 0 {
		return Box{}, nil
	}
	box := p.BoundingBox()
	return Box{X: box.Min.X, Y: box.Min.Y, W: box.Max.X - box.Min.X, H: box.Max.Y - box.Min.Y}, nil
}

// Transform 使用GG变换控制点，椭圆弧交给配置的几何后端保持弧线参数
// 入参: path 页面路径, matrix 页面变换
// 返回: GeometryPath 新路径, error 路径或变换错误
func (b GGGeometryBackend) Transform(path GeometryPath, matrix Matrix) (GeometryPath, error) {
	for _, v := range matrix.Values() {
		if !finite(v) {
			return nil, fmt.Errorf("invalid geometry transform")
		}
	}
	for _, s := range path {
		if s.Verb == GeometryArc {
			if b.GeometryBackend == nil {
				return nil, ErrBackendUnavailable
			}
			return b.GeometryBackend.Transform(path, matrix)
		}
	}
	p, err := ggGeometryPath(path)
	if err != nil {
		return nil, err
	}
	return geometryFromGG(p.Transform(gg.Matrix{A: matrix.a, B: matrix.c, C: matrix.e, D: matrix.b, E: matrix.d, F: matrix.f})), nil
}

// ggGeometryPath 转换贝塞尔路径，不舍入控制点或静默略过指令
// 入参: path 库路径
// 返回: *gg.Path GG路径, error 不支持的路径指令
func ggGeometryPath(path GeometryPath) (*gg.Path, error) {
	p := gg.NewPath()
	for _, s := range path {
		switch s.Verb {
		case GeometryMove:
			p.MoveTo(s.End.X, s.End.Y)
		case GeometryLine:
			p.LineTo(s.End.X, s.End.Y)
		case GeometryQuad:
			p.QuadraticTo(s.Control1.X, s.Control1.Y, s.End.X, s.End.Y)
		case GeometryCubic:
			p.CubicTo(s.Control1.X, s.Control1.Y, s.Control2.X, s.Control2.Y, s.End.X, s.End.Y)
		case GeometryClose:
			p.Close()
		default:
			return nil, fmt.Errorf("unsupported GG geometry command %d", s.Verb)
		}
	}
	return p, nil
}

// geometryFromGG 返回库自有路径，不保留GG对象
// 入参: path GG路径
// 返回: GeometryPath 库路径
func geometryFromGG(path *gg.Path) GeometryPath {
	var result GeometryPath
	path.Iterate(func(verb gg.PathVerb, p []float64) {
		s := GeometrySegment{}
		switch verb {
		case gg.MoveTo:
			s.Verb, s.End = GeometryMove, Point{p[0], p[1]}
		case gg.LineTo:
			s.Verb, s.End = GeometryLine, Point{p[0], p[1]}
		case gg.QuadTo:
			s.Verb, s.Control1, s.End = GeometryQuad, Point{p[0], p[1]}, Point{p[2], p[3]}
		case gg.CubicTo:
			s.Verb, s.Control1, s.Control2, s.End = GeometryCubic, Point{p[0], p[1]}, Point{p[2], p[3]}, Point{p[4], p[5]}
		case gg.Close:
			s.Verb = GeometryClose
		}
		result = append(result, s)
	})
	return result
}
