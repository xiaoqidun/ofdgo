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

// geometryPolyline 保存展开后的子路径，独立记录闭合状态
type geometryPolyline struct {
	points []Point
	closed bool
}

// Stroke 将曲线、正长度虚线及线帽连接转换为非零填充轮廓
// 曲线按Tolerance展开，默认误差为0.001毫米，重叠轮廓保持同向绕数
// 结果未做布尔整理，按非零规则填充，区域运算需能处理重叠子路径的后端
// 入参: options 描边样式
// 返回: GeometryPath 描边轮廓, error 无效参数、不支持的零长度语义或精度限制
func (p GeometryPath) Stroke(options StrokeOptions) (GeometryPath, error) {
	if err := validateStroke(options); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	if options.Width/2 == 0 {
		return nil, fmt.Errorf("stroke width precision exhausted")
	}
	dashes := append([]float64(nil), options.Dashes...)
	total := 0.0
	for _, d := range dashes {
		total += d
	}
	if !finite(total) {
		return nil, fmt.Errorf("stroke dash period overflow")
	}
	if total == 0 {
		dashes = nil
	} else {
		for _, d := range dashes {
			if d == 0 {
				return nil, fmt.Errorf("zero-length stroke dash: %w", ErrBackendUnavailable)
			}
		}
		if len(dashes)%2 != 0 {
			dashes = append(dashes, dashes...)
			total *= 2
			if !finite(total) {
				return nil, fmt.Errorf("stroke dash period overflow")
			}
		}
	}
	tolerance := options.Tolerance
	if tolerance == 0 {
		tolerance = .001
	}
	lines, err := p.strokePolylines(tolerance / 2)
	if err != nil {
		return nil, err
	}
	var result GeometryPath
	for _, line := range lines {
		parts := []geometryPolyline{line}
		if len(dashes) != 0 {
			parts, err = line.dash(dashes, total, options.DashOffset)
			if err != nil {
				return nil, err
			}
		}
		for _, part := range parts {
			outline, err := part.stroke(options)
			if err != nil {
				return nil, err
			}
			result = append(result, outline...)
			if len(result) > 1<<20 {
				return nil, fmt.Errorf("stroke outline limit exceeded")
			}
		}
	}
	if err := result.validate(); err != nil {
		return nil, err
	}
	return result, nil
}

// strokePolylines 自适应展开贝塞尔，弦长亏损同时约束虚线累计长度误差
// 入参: tolerance 展开误差
// 返回: []geometryPolyline 子路径, error 展开限制
func (p GeometryPath) strokePolylines(tolerance float64) ([]geometryPolyline, error) {
	curves, err := p.Curves(tolerance / 2)
	if err != nil {
		return nil, err
	}
	var result []geometryPolyline
	var line geometryPolyline
	var current, start Point
	drawn := false
	flush := func() {
		if drawn {
			result = append(result, line)
		}
		line, drawn = geometryPolyline{}, false
	}
	count := 0
	var flatten func([]Point, float64, int) error
	flatten = func(points []Point, budget float64, depth int) error {
		first, last := points[0], points[len(points)-1]
		chord := math.Hypot(last.X-first.X, last.Y-first.Y)
		length, distance := 0.0, 0.0
		for i := 1; i < len(points); i++ {
			length += math.Hypot(points[i].X-points[i-1].X, points[i].Y-points[i-1].Y)
			if i == len(points)-1 {
				continue
			}
			if chord == 0 {
				distance = math.Max(distance, math.Hypot(points[i].X-first.X, points[i].Y-first.Y))
			} else {
				distance = math.Max(distance, math.Abs((points[i].X-first.X)*((last.Y-first.Y)/chord)-(points[i].Y-first.Y)*((last.X-first.X)/chord)))
			}
		}
		if !finite(length) || !finite(distance) {
			return fmt.Errorf("stroke curve overflow")
		}
		if distance <= tolerance && length-chord <= budget {
			if last != line.points[len(line.points)-1] {
				line.points = append(line.points, last)
				count++
			}
			if count > 1<<18 {
				return fmt.Errorf("stroke subdivision limit exceeded")
			}
			return nil
		}
		if depth == 24 {
			return fmt.Errorf("stroke curve tolerance cannot be met")
		}
		work := append([]Point(nil), points...)
		left, right := make([]Point, len(points)), make([]Point, len(points))
		for n := len(points); n > 0; n-- {
			left[len(points)-n], right[n-1] = work[0], work[n-1]
			for i := 0; i < n-1; i++ {
				work[i] = geometryLerp(work[i], work[i+1], .5)
			}
		}
		if err := flatten(left, budget/2, depth+1); err != nil {
			return err
		}
		return flatten(right, budget/2, depth+1)
	}
	for _, s := range curves {
		switch s.Verb {
		case GeometryMove:
			flush()
			current, start = s.End, s.End
			line.points = []Point{current}
		case GeometryClose:
			if drawn {
				if current != start {
					line.points = append(line.points, start)
				}
				line.closed = true
			}
			flush()
			current = start
			line.points = []Point{current}
		default:
			drawn = true
			points := []Point{current}
			if s.Verb == GeometryQuad || s.Verb == GeometryCubic {
				points = append(points, s.Control1)
			}
			if s.Verb == GeometryCubic {
				points = append(points, s.Control2)
			}
			points = append(points, s.End)
			if err := flatten(points, tolerance/float64(len(curves)), 0); err != nil {
				return nil, err
			}
			current = s.End
		}
	}
	flush()
	return result, nil
}

// dash 按子路径重置相位并合并跨闭合接缝的连续虚线
// 入参: pattern 正长度偶数虚线数组, total 周期, offset 偏移
// 返回: []geometryPolyline 虚线段, error 长度或细分限制
func (line geometryPolyline) dash(pattern []float64, total, offset float64) ([]geometryPolyline, error) {
	phase := math.Mod(offset, total)
	if phase < 0 {
		phase += total
	}
	index := 0
	for phase >= pattern[index] {
		phase -= pattern[index]
		index = (index + 1) % len(pattern)
	}
	if len(line.points) == 1 {
		if index%2 == 0 {
			return []geometryPolyline{line}, nil
		}
		return nil, nil
	}
	remaining := pattern[index] - phase
	var result []geometryPolyline
	var run []Point
	flush := func() {
		if len(run) != 0 {
			result = append(result, geometryPolyline{points: run})
			run = nil
		}
	}
	steps := 0
	for i := 1; i < len(line.points); i++ {
		a, b := line.points[i-1], line.points[i]
		length := math.Hypot(b.X-a.X, b.Y-a.Y)
		if !finite(length) {
			return nil, fmt.Errorf("stroke dash length overflow")
		}
		for at := 0.0; at < length; {
			step := math.Min(remaining, length-at)
			if at+step == at || steps > 1<<18 {
				return nil, fmt.Errorf("stroke dash subdivision limit exceeded")
			}
			steps++
			if index%2 == 0 {
				if len(run) == 0 {
					run = append(run, geometryLerp(a, b, at/length))
				}
				run = append(run, geometryLerp(a, b, (at+step)/length))
			}
			at += step
			remaining -= step
			if remaining == 0 {
				flush()
				index = (index + 1) % len(pattern)
				remaining = pattern[index]
			}
		}
	}
	flush()
	if line.closed && len(result) > 0 {
		first, last := &result[0], &result[len(result)-1]
		if first.points[0] == line.points[0] && last.points[len(last.points)-1] == line.points[0] {
			if len(result) == 1 {
				first.closed = true
			} else {
				first.points = append(last.points[:len(last.points)-1], first.points...)
				result = result[:len(result)-1]
			}
		}
	}
	return result, nil
}

// stroke 用同向矩形、连接楔形及圆盘组成非零填充区域
// 入参: options 描边样式
// 返回: GeometryPath 填充轮廓, error 不支持的退化方形线帽
func (line geometryPolyline) stroke(options StrokeOptions) (GeometryPath, error) {
	var result GeometryPath
	half := options.Width / 2
	polygon := func(points ...Point) {
		scale := 0.0
		for _, p := range points {
			scale = math.Max(scale, math.Max(math.Abs(p.X-points[0].X), math.Abs(p.Y-points[0].Y)))
		}
		if scale == 0 {
			return
		}
		area := 0.0
		for i, p := range points {
			q := points[(i+1)%len(points)]
			area += ((p.X-points[0].X)/scale)*((q.Y-points[0].Y)/scale) - ((p.Y-points[0].Y)/scale)*((q.X-points[0].X)/scale)
		}
		if area == 0 {
			return
		}
		if area < 0 {
			for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
				points[i], points[j] = points[j], points[i]
			}
		}
		result = append(result, GeometrySegment{Verb: GeometryMove, End: points[0]})
		for _, p := range points[1:] {
			result = append(result, GeometrySegment{Verb: GeometryLine, End: p})
		}
		result = append(result, GeometrySegment{Verb: GeometryClose})
	}
	disk := func(p Point) {
		result = append(result, GeometrySegment{Verb: GeometryMove, End: Point{p.X + half, p.Y}},
			GeometrySegment{Verb: GeometryArc, RadiusX: half, RadiusY: half, Sweep: true, End: Point{p.X - half, p.Y}},
			GeometrySegment{Verb: GeometryArc, RadiusX: half, RadiusY: half, Sweep: true, End: Point{p.X + half, p.Y}},
			GeometrySegment{Verb: GeometryClose})
	}
	points := line.points
	if len(points) == 1 {
		if line.closed {
			return nil, fmt.Errorf("directionless closed stroke: %w", ErrBackendUnavailable)
		}
		if options.Cap == "Round" {
			disk(points[0])
		} else if options.Cap == "Square" {
			return nil, fmt.Errorf("directionless square stroke cap: %w", ErrBackendUnavailable)
		}
		return result, nil
	}
	directions := make([]Point, len(points)-1)
	for i := range directions {
		a, b := points[i], points[i+1]
		length := math.Hypot(b.X-a.X, b.Y-a.Y)
		if length == 0 || !finite(length) {
			return nil, fmt.Errorf("invalid stroke segment length")
		}
		d := Point{(b.X - a.X) / length, (b.Y - a.Y) / length}
		directions[i] = d
		n := Point{-half * d.Y, half * d.X}
		polygon(Point{a.X + n.X, a.Y + n.Y}, Point{b.X + n.X, b.Y + n.Y}, Point{b.X - n.X, b.Y - n.Y}, Point{a.X - n.X, a.Y - n.Y})
	}
	join := func(p, a, b Point) {
		cross := a.X*b.Y - a.Y*b.X
		if cross == 0 {
			if options.Join == "Round" && a.X*b.X+a.Y*b.Y < 0 {
				disk(p)
			}
			return
		}
		side := -math.Copysign(half, cross)
		x := Point{p.X - side*a.Y, p.Y + side*a.X}
		y := Point{p.X - side*b.Y, p.Y + side*b.X}
		if options.Join == "Round" {
			if cross < 0 {
				x, y = y, x
			}
			result = append(result, GeometrySegment{Verb: GeometryMove, End: p},
				GeometrySegment{Verb: GeometryLine, End: x},
				GeometrySegment{Verb: GeometryArc, RadiusX: half, RadiusY: half, Sweep: true, End: y},
				GeometrySegment{Verb: GeometryClose})
			return
		}
		limit := options.MiterLimit
		if limit == 0 {
			limit = defaultMiterLimit
		}
		if options.Join == "" || options.Join == "Miter" {
			t := ((y.X-x.X)*b.Y - (y.Y-x.Y)*b.X) / cross
			m := Point{x.X + t*a.X, x.Y + t*a.Y}
			if math.Hypot(m.X-p.X, m.Y-p.Y) <= half*limit {
				polygon(p, x, m, y)
				return
			}
		}
		polygon(p, x, y)
	}
	for i := 1; i < len(points)-1; i++ {
		join(points[i], directions[i-1], directions[i])
	}
	if line.closed {
		join(points[0], directions[len(directions)-1], directions[0])
	} else {
		for _, end := range []struct{ p, d Point }{{points[0], Point{-directions[0].X, -directions[0].Y}}, {points[len(points)-1], directions[len(directions)-1]}} {
			if options.Cap == "Round" {
				disk(end.p)
			} else if options.Cap == "Square" {
				p, d := end.p, end.d
				n := Point{-half * d.Y, half * d.X}
				polygon(Point{p.X + n.X, p.Y + n.Y}, Point{p.X + n.X + half*d.X, p.Y + n.Y + half*d.Y}, Point{p.X - n.X + half*d.X, p.Y - n.Y + half*d.Y}, Point{p.X - n.X, p.Y - n.Y})
			}
		}
	}
	return result, nil
}
