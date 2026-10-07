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
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// pdfClipPath 保存导入器只读的裁剪几何，不缓存对象位移或文字裁剪
type pdfClipPath struct {
	box  Box
	data string
	path *[1]PathObject
}

// pdfClipContainsBoundary 判断单一矩形裁剪是否完整覆盖对象边界，不近似曲线或斜边
// 入参: path PDF裁剪路径, matrix 页面变换, boundary OFD对象边界
// 返回: bool 是否可由对象边界代替裁剪
func pdfClipContainsBoundary(path pdfgo.Path, matrix pdfgo.Matrix, boundary Box) bool {
	if len(path.Text) != 0 || len(path.Segments) < 4 || len(path.Segments) > 6 || !finite(boundary.X) || !finite(boundary.Y) || !finite(boundary.W) || !finite(boundary.H) || boundary.W <= 0 || boundary.H <= 0 {
		return false
	}
	var points [4]pdfgo.Point
	for index := range 4 {
		segment := path.Segments[index]
		operator := "L"
		if index == 0 {
			operator = "M"
		}
		if segment.Operator != operator || len(segment.Points) != 1 {
			return false
		}
		points[index] = matrix.Apply(segment.Points[0])
		if !finite(points[index].X) || !finite(points[index].Y) {
			return false
		}
	}
	end := 4
	if end < len(path.Segments) && path.Segments[end].Operator == "L" {
		segment := path.Segments[end]
		if len(segment.Points) != 1 || matrix.Apply(segment.Points[0]) != points[0] {
			return false
		}
		end++
	}
	if end < len(path.Segments) {
		segment := path.Segments[end]
		if segment.Operator != "C" || len(segment.Points) != 0 {
			return false
		}
		end++
	}
	if end != len(path.Segments) {
		return false
	}
	a, b, c, d := points[0], points[1], points[2], points[3]
	if !(a.Y == b.Y && b.X == c.X && c.Y == d.Y && d.X == a.X || a.X == b.X && b.Y == c.Y && c.X == d.X && d.Y == a.Y) {
		return false
	}
	left, right := math.Min(a.X, c.X), math.Max(a.X, c.X)
	top, bottom := math.Min(a.Y, c.Y), math.Max(a.Y, c.Y)
	return left < right && top < bottom && boundary.X >= left && boundary.Y >= top && boundary.X+boundary.W <= right && boundary.Y+boundary.H <= bottom
}

// pdfClipPathKey 按路径完整内容与变换生成键，不依赖可变切片的地址
// 入参: ctx 取消上下文, path 裁剪路径, matrix 页面变换
// 返回: [32]byte 内容摘要, error 取消错误
func pdfClipPathKey(ctx context.Context, path pdfgo.Path, matrix pdfgo.Matrix) ([32]byte, error) {
	hash := sha256.New()
	var value [16]byte
	for _, number := range matrix {
		binary.LittleEndian.PutUint64(value[:8], math.Float64bits(number))
		hash.Write(value[:8])
	}
	binary.LittleEndian.PutUint64(value[:8], uint64(len(path.Segments)))
	value[8] = 0
	if path.EvenOdd {
		value[8] = 1
	}
	hash.Write(value[:9])
	for _, segment := range path.Segments {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		binary.LittleEndian.PutUint64(value[:8], uint64(len(segment.Operator)))
		binary.LittleEndian.PutUint64(value[8:], uint64(len(segment.Points)))
		hash.Write(value[:])
		hash.Write([]byte(segment.Operator))
		for index, point := range segment.Points {
			if index&255 == 0 {
				if err := ctx.Err(); err != nil {
					return [32]byte{}, err
				}
			}
			binary.LittleEndian.PutUint64(value[:8], math.Float64bits(point.X))
			binary.LittleEndian.PutUint64(value[8:], math.Float64bits(point.Y))
			hash.Write(value[:])
		}
	}
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	var key [32]byte
	hash.Sum(key[:0])
	return key, nil
}

// clipPath 复用页面内重复路径编码，按估算字节预算淘汰且不缓存错误
// 入参: path 裁剪路径
// 返回: pdfClipPath 变换后的边界及编码, error 取消错误
func (p *pdfImporter) clipPath(path pdfgo.Path) (pdfClipPath, error) {
	key, err := pdfClipPathKey(p.ctx, path, p.matrix)
	if err != nil {
		return pdfClipPath{}, err
	}
	if p.clipPaths == nil {
		p.clipPaths = &renderCache[[32]byte, pdfClipPath]{limit: 8 << 20}
	}
	if cached, ok := p.clipPaths.get(key); ok {
		return cached, nil
	}
	box, err := p.pathBoundsContext(p.ctx, path)
	if err != nil {
		return pdfClipPath{}, err
	}
	data, err := p.pathDataContext(p.ctx, path, box)
	if err != nil {
		return pdfClipPath{}, err
	}
	rule := "NonZero"
	if path.EvenOdd {
		rule = "Even-Odd"
	}
	boundary := pdfBoundary(box)
	fill, stroke := true, false
	result := pdfClipPath{box: box, data: data, path: &[1]PathObject{{Boundary: boundary, AbbreviatedData: data, Rule: rule, Fill: &fill, Stroke: &stroke}}}
	p.clipPaths.put(key, result, len(data)+len(boundary)+512)
	return result, nil
}
