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
	"fmt"
	"math"
	"slices"

	"github.com/tdewolff/canvas"
	"github.com/xiaoqidun/pdfgo"
)

// PDF链接区域细分上限与曲线逼近误差，误差单位为毫米
const (
	pdfLinkEdgeLimit = 4096
	pdfLinkQuadLimit = 65536
	pdfLinkTolerance = 0.01
)

// pdfLinkEdge 保存按纵坐标排序的边及原始绕行方向
type pdfLinkEdge struct {
	a, b    pdfgo.Point
	winding int
}

// pdfLinkCross 保存水平扫描线与区域边的交点
type pdfLinkCross struct {
	x    float64
	edge int
}

// pdfActionRegion 将非零绕行区域分解为PDF链接四边形，保留空洞和相交轮廓
// 入参: ctx 取消上下文, outline 页面SVG路径, pageH 页面高度
// 返回: *pdfgo.LinkRegion 链接区域，nil表示沿用矩形, error 解析或复杂度错误
func pdfActionRegion(ctx context.Context, outline string, pageH float64) (*pdfgo.LinkRegion, error) {
	if outline == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := canvas.ParseSVGPath(outline)
	if err != nil {
		return nil, err
	}
	if path.Len() > pdfLinkEdgeLimit {
		return nil, fmt.Errorf("PDF link region exceeds edge limit")
	}
	if _, ok := geometryRectangleBounds(*geometryFromCanvasPath(path)); ok {
		return nil, nil
	}
	path, err = flattenPDFLink(ctx, path)
	if err != nil {
		return nil, err
	}
	if quads := convexPDFLink(path, pageH); len(quads) != 0 {
		return pdfLinkRegionBounds(quads), nil
	}
	var edges []pdfLinkEdge
	var levels []float64
	var start, previous pdfgo.Point
	open := false
	add := func(a, b pdfgo.Point) {
		if a.Y == b.Y {
			return
		}
		edge := pdfLinkEdge{a: a, b: b, winding: 1}
		if a.Y > b.Y {
			edge.a, edge.b, edge.winding = b, a, -1
		}
		edges = append(edges, edge)
		levels = append(levels, edge.a.Y, edge.b.Y)
	}
	scanner := path.Scanner()
	for scanner.Scan() {
		p := scanner.End()
		point := pdfgo.Point{X: p.X * 72 / 25.4, Y: (pageH - p.Y) * 72 / 25.4}
		if !finite(point.X) || !finite(point.Y) {
			return nil, fmt.Errorf("invalid PDF link coordinate")
		}
		switch scanner.Cmd() {
		case canvas.MoveToCmd:
			if open {
				add(previous, start)
			}
			start, open = point, true
		case canvas.LineToCmd:
			if !open {
				start, open = previous, true
			}
			add(previous, point)
		case canvas.CloseCmd:
			add(previous, start)
			open = false
		}
		previous = point
	}
	if open {
		add(previous, start)
	}
	for i, a := range edges {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, b := range edges[i+1:] {
			low, high := max(a.a.Y, b.a.Y), min(a.b.Y, b.b.Y)
			if low >= high {
				continue
			}
			d0, d1 := a.x(low)-b.x(low), a.x(high)-b.x(high)
			if d0 < 0 && d1 > 0 || d0 > 0 && d1 < 0 {
				y := low + (high-low)*(d0/(d0-d1))
				if !finite(y) {
					return nil, fmt.Errorf("invalid PDF link intersection")
				}
				levels = append(levels, y)
				if len(levels) > pdfLinkQuadLimit {
					return nil, fmt.Errorf("PDF link region exceeds intersection limit")
				}
			}
		}
	}
	slices.Sort(levels)
	levels = slices.Compact(levels)
	region := &pdfgo.LinkRegion{}
	crossings := make([]pdfLinkCross, 0, len(edges))
	for i := 1; i < len(levels); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		low, high := levels[i-1], levels[i]
		mid := low + (high-low)/2
		if mid <= low || mid >= high {
			continue
		}
		crossings = crossings[:0]
		for j, edge := range edges {
			if edge.a.Y < mid && mid < edge.b.Y {
				crossings = append(crossings, pdfLinkCross{edge.x(mid), j})
			}
		}
		slices.SortFunc(crossings, func(a, b pdfLinkCross) int {
			if a.x < b.x {
				return -1
			}
			if a.x > b.x {
				return 1
			}
			return a.edge - b.edge
		})
		winding, left := 0, 0
		for _, cross := range crossings {
			before := winding
			winding += edges[cross.edge].winding
			if before == 0 && winding != 0 {
				left = cross.edge
			}
			if before != 0 && winding == 0 {
				a, b := edges[left], edges[cross.edge]
				quad := [4]pdfgo.Point{{X: a.x(low), Y: low}, {X: b.x(low), Y: low}, {X: b.x(high), Y: high}, {X: a.x(high), Y: high}}
				region.Quads = appendPDFLinkQuad(region.Quads, quad)
				if len(region.Quads) > pdfLinkQuadLimit {
					return nil, fmt.Errorf("PDF link region exceeds quadrilateral limit")
				}
			}
		}
	}
	if len(region.Quads) == 0 {
		return region, nil
	}
	return pdfLinkRegionBounds(region.Quads), nil
}

// convexPDFLink 直接保留单个凸四边形，三角形只分成三个四边形
// 入参: path 已展开路径, pageH 页面高度
// 返回: [][4]pdfgo.Point 逆时针四边形，不适用时为空
func convexPDFLink(path *canvas.Path, pageH float64) [][4]pdfgo.Point {
	if path.HasSubpaths() || path.Len() > 6 {
		return nil
	}
	points := path.Coords()
	if len(points) > 1 && points[0] == points[len(points)-1] {
		points = points[:len(points)-1]
	}
	if len(points) < 3 || len(points) > 4 {
		return nil
	}
	var storage [4]pdfgo.Point
	vertices := storage[:len(points)]
	for i, p := range points {
		vertices[i] = pdfgo.Point{X: p.X * 72 / 25.4, Y: (pageH - p.Y) * 72 / 25.4}
	}
	direction := 0.0
	for i, a := range vertices {
		b, c := vertices[(i+1)%len(vertices)], vertices[(i+2)%len(vertices)]
		cross := pdfLinkTurn(a, b, c)
		if !finite(cross) || cross == 0 || direction != 0 && (direction < 0) != (cross < 0) {
			return nil
		}
		direction = cross
	}
	if direction < 0 {
		slices.Reverse(vertices)
	}
	if len(vertices) == 4 {
		return [][4]pdfgo.Point{storage}
	}
	return appendPDFLinkTriangle(nil, [3]pdfgo.Point{vertices[0], vertices[1], vertices[2]})
}

// pdfLinkRegionBounds 计算四边形共同的注解矩形，保证全部顶点落在矩形内
// 入参: quads 链接四边形
// 返回: *pdfgo.LinkRegion 链接区域
func pdfLinkRegionBounds(quads [][4]pdfgo.Point) *pdfgo.LinkRegion {
	region := &pdfgo.LinkRegion{Quads: quads, Rect: pdfgo.Rectangle{XMin: math.Inf(1), YMin: math.Inf(1), XMax: math.Inf(-1), YMax: math.Inf(-1)}}
	for _, quad := range quads {
		for _, p := range quad {
			region.Rect.XMin, region.Rect.YMin = min(region.Rect.XMin, p.X), min(region.Rect.YMin, p.Y)
			region.Rect.XMax, region.Rect.YMax = max(region.Rect.XMax, p.X), max(region.Rect.YMax, p.Y)
		}
	}
	return region
}

// flattenPDFLink 在取消和数量限制内展开动作曲线，复用标准椭圆弧转换
// 入参: ctx 取消上下文, path 页面SVG路径
// 返回: *canvas.Path 闭合折线路径, error 精度或复杂度错误
func flattenPDFLink(ctx context.Context, path *canvas.Path) (*canvas.Path, error) {
	geometry := geometryFromCanvasPath(path)
	result := &canvas.Path{}
	var current, start Point
	count := 0
	var flatten func([]Point, int) error
	flatten = func(points []Point, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		first, last := points[0], points[len(points)-1]
		dx, dy := last.X-first.X, last.Y-first.Y
		length := math.Hypot(dx, dy)
		distance := 0.0
		for _, point := range points[1 : len(points)-1] {
			t := 0.0
			if length > 0 {
				t = min(max((point.X-first.X)*(dx/length)+(point.Y-first.Y)*(dy/length), 0), length) / length
			}
			distance = max(distance, math.Hypot(point.X-(first.X*(1-t)+last.X*t), point.Y-(first.Y*(1-t)+last.Y*t)))
		}
		if !finite(length) || !finite(distance) {
			return fmt.Errorf("invalid PDF link curve")
		}
		if distance <= pdfLinkTolerance/2 {
			count++
			if count > pdfLinkEdgeLimit {
				return fmt.Errorf("PDF link region exceeds edge limit")
			}
			result.LineTo(last.X, -last.Y)
			return nil
		}
		if depth == 32 {
			return fmt.Errorf("PDF link curve tolerance cannot be met")
		}
		var work, left, right [4]Point
		copy(work[:], points)
		for n := len(points); n > 0; n-- {
			left[len(points)-n], right[n-1] = work[0], work[n-1]
			for i := 0; i < n-1; i++ {
				work[i] = geometryLerp(work[i], work[i+1], .5)
			}
		}
		if err := flatten(left[:len(points)], depth+1); err != nil {
			return err
		}
		return flatten(right[:len(points)], depth+1)
	}
	for _, segment := range *geometry {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch segment.Verb {
		case GeometryMove:
			result.MoveTo(segment.End.X, -segment.End.Y)
			current, start = segment.End, segment.End
		case GeometryClose:
			result.Close()
			current = start
		default:
			curves := GeometryPath{segment}
			if segment.Verb == GeometryArc {
				var err error
				curves, err = arcCurves(current, segment, pdfLinkTolerance/2)
				if err != nil {
					return nil, err
				}
				if len(curves) > pdfLinkEdgeLimit-count {
					return nil, fmt.Errorf("PDF link region exceeds edge limit")
				}
			}
			for _, curve := range curves {
				var storage [4]Point
				points := append(storage[:0], current)
				if curve.Verb == GeometryQuad || curve.Verb == GeometryCubic {
					points = append(points, curve.Control1)
				}
				if curve.Verb == GeometryCubic {
					points = append(points, curve.Control2)
				}
				points = append(points, curve.End)
				if err := flatten(points, 0); err != nil {
					return nil, err
				}
				current = curve.End
			}
		}
	}
	return result, nil
}

// x 计算非水平边在指定纵坐标处的横坐标
// 入参: y 扫描线纵坐标
// 返回: float64 交点横坐标
func (e pdfLinkEdge) x(y float64) float64 {
	t := (y - e.a.Y) / (e.b.Y - e.a.Y)
	return e.a.X*(1-t) + e.b.X*t
}

// appendPDFLinkQuad 收集梯形，尖端退化为三角形时细分为三个非退化四边形
// 入参: quads 已有四边形, quad 扫描带梯形
// 返回: [][4]pdfgo.Point 四边形集合
func appendPDFLinkQuad(quads [][4]pdfgo.Point, quad [4]pdfgo.Point) [][4]pdfgo.Point {
	epsilon := 1e-12
	for _, point := range quad {
		epsilon = max(epsilon, math.Abs(point.X)*1e-12, math.Abs(point.Y)*1e-12)
	}
	if quad[2].Y-quad[0].Y <= epsilon {
		return quads
	}
	if quad[1].X-quad[0].X <= epsilon {
		quad[0].X, quad[1].X = (quad[0].X+quad[1].X)/2, (quad[0].X+quad[1].X)/2
	}
	if quad[2].X-quad[3].X <= epsilon {
		quad[3].X, quad[2].X = (quad[3].X+quad[2].X)/2, (quad[3].X+quad[2].X)/2
	}
	var triangle [3]pdfgo.Point
	if quad[0] == quad[1] {
		if quad[2] == quad[3] {
			return quads
		}
		triangle = [3]pdfgo.Point{quad[0], quad[2], quad[3]}
	} else if quad[2] == quad[3] {
		triangle = [3]pdfgo.Point{quad[0], quad[1], quad[2]}
	} else {
		return appendValidPDFLinkQuad(quads, quad)
	}
	return appendPDFLinkTriangle(quads, triangle)
}

// appendPDFLinkTriangle 连接三角形重心和边中点，生成覆盖相同区域的凸四边形
// 入参: quads 已有四边形, triangle 逆时针三角形
// 返回: [][4]pdfgo.Point 四边形集合
func appendPDFLinkTriangle(quads [][4]pdfgo.Point, triangle [3]pdfgo.Point) [][4]pdfgo.Point {
	center := pdfgo.Point{X: (triangle[0].X + triangle[1].X + triangle[2].X) / 3, Y: (triangle[0].Y + triangle[1].Y + triangle[2].Y) / 3}
	for i, a := range triangle {
		b, c := triangle[(i+1)%3], triangle[(i+2)%3]
		quads = appendValidPDFLinkQuad(quads, [4]pdfgo.Point{a, {X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2}, center, {X: (a.X + c.X) / 2, Y: (a.Y + c.Y) / 2}})
	}
	return quads
}

// pdfLinkTurn 计算相邻边的转向，消除近共线乘积相减产生的舍入噪声
// 入参: a 前顶点, b 当前顶点, c 后顶点
// 返回: float64 有向面积的两倍，数值退化时为零
func pdfLinkTurn(a, b, c pdfgo.Point) float64 {
	u, v := (b.X-a.X)*(c.Y-b.Y), (b.Y-a.Y)*(c.X-b.X)
	cross := u - v
	if math.Abs(cross) <= (math.Abs(u)+math.Abs(v))*1e-12 {
		return 0
	}
	return cross
}

// appendValidPDFLinkQuad 仅保留有限且非退化的逆时针凸四边形
// 入参: quads 已有四边形, quad 候选四边形
// 返回: [][4]pdfgo.Point 四边形集合
func appendValidPDFLinkQuad(quads [][4]pdfgo.Point, quad [4]pdfgo.Point) [][4]pdfgo.Point {
	for i, a := range quad {
		cross := pdfLinkTurn(a, quad[(i+1)%4], quad[(i+2)%4])
		if !finite(cross) || cross <= 0 {
			return quads
		}
	}
	return append(quads, quad)
}
