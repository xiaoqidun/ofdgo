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

	"github.com/xiaoqidun/pdfgo"
)

// pdfGlyphPath 将字形轮廓变换到PDF页面坐标，二次曲线精确转换为三次曲线
// 入参: outline 字形轮廓, matrix 字形到页面的变换
// 返回: pdfgo.Path 页面路径, error 非字体轮廓指令
func pdfGlyphPath(outline GeometryPath, matrix pdfgo.Matrix) (pdfgo.Path, error) {
	path := pdfgo.Path{Segments: make([]pdfgo.Segment, 0, len(outline))}
	var current, start Point
	point := func(p Point) pdfgo.Point { return matrix.Apply(pdfgo.Point{X: p.X, Y: p.Y}) }
	for _, segment := range outline {
		switch segment.Verb {
		case GeometryMove:
			start = segment.End
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "M", Points: []pdfgo.Point{point(segment.End)}})
		case GeometryLine:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "L", Points: []pdfgo.Point{point(segment.End)}})
		case GeometryQuad:
			c1 := Point{current.X + (segment.Control1.X-current.X)*2/3, current.Y + (segment.Control1.Y-current.Y)*2/3}
			c2 := Point{segment.End.X + (segment.Control1.X-segment.End.X)*2/3, segment.End.Y + (segment.Control1.Y-segment.End.Y)*2/3}
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "B", Points: []pdfgo.Point{point(c1), point(c2), point(segment.End)}})
		case GeometryCubic:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "B", Points: []pdfgo.Point{point(segment.Control1), point(segment.Control2), point(segment.End)}})
		case GeometryClose:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "C"})
			current = start
			continue
		default:
			return pdfgo.Path{}, fmt.Errorf("unsupported PDF glyph geometry verb %d", segment.Verb)
		}
		current = segment.End
	}
	return path, nil
}
