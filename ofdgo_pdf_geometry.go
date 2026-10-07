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
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image/color"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// pdfGeometryPath 直接转换PDF路径坐标，不经文本编码或曲线展平
// 入参: ctx 取消上下文, source PDF路径, matrix 坐标变换
// 返回: GeometryPath 独立几何路径, error 指令、坐标或取消错误
func pdfGeometryPath(ctx context.Context, source pdfgo.Path, matrix pdfgo.Matrix) (GeometryPath, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := make(GeometryPath, len(source.Segments))
	for i, segment := range source.Segments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var count int
		switch segment.Operator {
		case "M":
			path[i].Verb, count = GeometryMove, 1
		case "L":
			path[i].Verb, count = GeometryLine, 1
		case "B":
			path[i].Verb, count = GeometryCubic, 3
		case "C":
			path[i].Verb = GeometryClose
		default:
			return nil, fmt.Errorf("invalid PDF path operator %q", segment.Operator)
		}
		if len(segment.Points) != count {
			return nil, fmt.Errorf("invalid PDF path operands for %s", segment.Operator)
		}
		var points [3]Point
		for j, point := range segment.Points {
			point = matrix.Apply(point)
			points[j] = Point{X: point.X, Y: point.Y}
		}
		if count != 0 {
			path[i].End = points[count-1]
		}
		if count == 3 {
			path[i].Control1, path[i].Control2 = points[0], points[1]
		}
	}
	if err := path.validate(); err != nil {
		return nil, err
	}
	return path, ctx.Err()
}

// compileCoverage 直接编译无文字裁剪的填充覆盖，其他语义交给配置的页面编译器
// 入参: node 图元, stroke 是否描边
// 返回: *RasterPage 覆盖场景, Box 页面区域, error 编译错误
func (p *pdfImporter) compileCoverage(node pdfCompositeNode, stroke bool) (*RasterPage, Box, error) {
	fallback := func() (*RasterPage, Box, error) {
		return p.compileGroup(func(local *pdfImporter) error { return node.coverage(local, stroke) })
	}
	if stroke || node.path == nil && node.image == nil {
		return fallback()
	}
	deferredClip := false
	switch p.editor.Backends().Compiler.(type) {
	case CanvasBackend, *CanvasBackend:
	case OFDCompiler, *OFDCompiler:
		deferredClip = true
	default:
		return fallback()
	}
	style, _, _ := node.style()
	for _, clip := range style.Clips {
		if len(clip.Text) != 0 {
			return fallback()
		}
	}
	geometry, err := p.renderer.Geometry()
	if err != nil {
		return nil, Box{}, err
	}
	switch geometry.(type) {
	case CanvasBackend, *CanvasBackend:
	default:
		return fallback()
	}
	var path GeometryPath
	evenOdd := false
	if node.path != nil {
		path, err = pdfGeometryPath(p.ctx, node.path.Path, p.matrix)
		evenOdd = node.path.Path.EvenOdd
	} else {
		path, err = geometry.Transform(geometryRectangle(Box{W: 1, H: 1}), MatrixFromValues([6]float64(p.matrix.Mul(node.image.Matrix))))
	}
	if err != nil {
		return nil, Box{}, err
	}
	if len(path) == 0 {
		return nil, Box{}, fmt.Errorf("empty PDF painted path")
	}
	path = closedGeometry(path)
	clip, err := p.coverageClip(style.Clips, geometry)
	if err != nil || len(clip) == 0 {
		return nil, Box{}, err
	}
	compiler := semanticCompiler{geometry: geometry, page: &RasterPage{Width: p.pageWidth, Height: p.pageHeight, DPI: p.rasterDPI}}
	var bounds pdfPointBounds
	for _, segment := range path {
		if segment.Verb != GeometryClose {
			bounds.add(pdfgo.Point{X: segment.End.X, Y: segment.End.Y})
		}
		if segment.Verb == GeometryCubic {
			bounds.add(pdfgo.Point{X: segment.Control1.X, Y: segment.Control1.Y})
			bounds.add(pdfgo.Point{X: segment.Control2.X, Y: segment.Control2.Y})
		}
	}
	boundary := geometryRectangle(bounds.box())
	clip, err = clipGeometry(geometry, clip, &boundary)
	if err != nil {
		return nil, Box{}, err
	}
	if deferredClip {
		if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, &clip, IdentityMatrix, nil); err != nil {
			return nil, Box{}, err
		}
		return p.groupSceneBounds(compiler.page)
	}
	box, err := geometry.Bounds(path)
	if err != nil {
		return nil, Box{}, err
	}
	rect, rectangle := geometryRectangleBounds(clip)
	if !rectangle || box.X < rect.X || box.Y < rect.Y || box.X+box.W > rect.X+rect.W || box.Y+box.H > rect.Y+rect.H {
		if evenOdd {
			path, err = geometry.Normalize(path, true)
			if err != nil {
				return nil, Box{}, err
			}
			evenOdd = false
		}
		var clipped GeometryPath
		var linear bool
		var clipErr error
		if rectangle {
			clipped, linear, clipErr = clipLinearFillGeometry(path, clip)
		}
		if clipErr != nil {
			return nil, Box{}, clipErr
		}
		if linear {
			path = clipped
		} else {
			path, err = clipGeometry(geometry, path, &clip)
			if err != nil {
				return nil, Box{}, err
			}
		}
	}
	if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, nil, IdentityMatrix, nil); err != nil {
		return nil, Box{}, err
	}
	return p.groupSceneBounds(compiler.page)
}

// coverageClip 按完整路径、变换和页面范围复用只读裁剪，不缓存失败结果
// 入参: paths 无文字裁剪的路径集合, geometry 几何后端
// 返回: GeometryPath 共享只读轮廓, error 几何或取消错误
func (p *pdfImporter) coverageClip(paths []pdfgo.Path, geometry GeometryBackend) (GeometryPath, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	clip := geometryRectangle(Box{W: p.pageWidth, H: p.pageHeight})
	if len(paths) == 0 {
		return clip, nil
	}
	hash := sha256.New()
	var size [16]byte
	binary.LittleEndian.PutUint64(size[:8], math.Float64bits(p.pageWidth))
	binary.LittleEndian.PutUint64(size[8:], math.Float64bits(p.pageHeight))
	hash.Write(size[:])
	for _, path := range paths {
		key, err := pdfClipPathKey(p.ctx, path, p.matrix)
		if err != nil {
			return nil, err
		}
		hash.Write(key[:])
	}
	var key [32]byte
	hash.Sum(key[:0])
	cache := p.compositingCache().clipCache()
	if cached, ok := cache.get(key); ok {
		return cached, nil
	}
	for _, source := range paths {
		part, err := pdfGeometryPath(p.ctx, source, p.matrix)
		if err != nil {
			return nil, err
		}
		part = closedGeometry(part)
		if _, rectangle := geometryRectangleBounds(part); source.EvenOdd && !rectangle {
			part, err = geometry.Normalize(part, true)
			if err != nil {
				return nil, err
			}
		}
		clip, err = clipGeometry(geometry, part, &clip)
		if err != nil {
			return nil, err
		}
		if len(clip) == 0 {
			break
		}
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if len(clip) <= (cache.limit-128)/112 {
		cache.put(key, clip, 128+len(clip)*112)
	}
	return clip, nil
}
