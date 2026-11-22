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

package geom

import (
	"fmt"
	"math"
	"slices"
)

// UnsupportedError 表示当前几何实现无法展开的描边形态
type UnsupportedError string

// geometryPolyline 保存展开后的子路径，独立记录闭合状态
type geometryPolyline struct {
	points    []Point
	closed    bool
	direction Point
}

// Error 返回不支持的描边原因
// 返回: string 错误信息
func (err UnsupportedError) Error() string {
	return string(err)
}

// Stroke 按端点和转角样式将曲线、虚线转换为非零填充轮廓
// 曲线按Tolerance展开，默认误差为0.001毫米，重叠轮廓保持同向绕数
// 结果未做布尔整理，按非零规则填充，区域运算需能处理重叠子路径的后端
// 入参: options 描边样式
// 返回: Path 描边轮廓, error 无效参数或精度限制
func (p Path) Stroke(options StrokeOptions) (Path, error) {
	if err := ValidateStroke(options); err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
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
	var result Path
	for _, line := range lines {
		parts := []geometryPolyline{line}
		if len(dashes) != 0 {
			parts, err = line.dash(dashes, total, options.DashOffset)
			if err != nil {
				return nil, err
			}
		}
		for _, part := range parts {
			result, err = part.stroke(result, options)
			if err != nil {
				return nil, err
			}
			if len(result) > 1<<20 {
				return nil, fmt.Errorf("stroke outline limit exceeded")
			}
		}
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

// dash 按子路径重置相位并合并跨闭合接缝的连续虚线
// 入参: pattern 非负偶数虚线数组, total 周期, offset 偏移
// 返回: []geometryPolyline 虚线段, error 长度或细分限制
func (line geometryPolyline) dash(pattern []float64, total, offset float64) ([]geometryPolyline, error) {
	phase := math.Mod(offset, total)
	if phase < 0 {
		phase += total
	}
	index := 0
	for phase > 0 && phase >= pattern[index] {
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
	var direction Point
	flush := func() {
		if len(run) != 0 {
			result = append(result, geometryPolyline{points: run, direction: direction})
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
		for at := 0.0; ; {
			for remaining == 0 {
				steps++
				if steps > 1<<18 {
					return nil, fmt.Errorf("stroke dash subdivision limit exceeded")
				}
				if pattern[index] == 0 && index%2 == 0 && len(run) == 0 {
					result = append(result, geometryPolyline{points: []Point{Lerp(a, b, at/length)}, direction: Point{(b.X - a.X) / length, (b.Y - a.Y) / length}})
				}
				index = (index + 1) % len(pattern)
				remaining = pattern[index]
				if index%2 == 1 && remaining > 0 {
					flush()
				}
			}
			if at == length {
				break
			}
			step := math.Min(remaining, length-at)
			if at+step == at || steps > 1<<18 {
				return nil, fmt.Errorf("stroke dash subdivision limit exceeded")
			}
			steps++
			if index%2 == 0 {
				if len(run) == 0 {
					run = append(run, Lerp(a, b, at/length))
					direction = Point{(b.X - a.X) / length, (b.Y - a.Y) / length}
				}
				end := Lerp(a, b, (at+step)/length)
				if end != run[len(run)-1] {
					run = append(run, end)
				}
			}
			at += step
			remaining -= step
		}
	}
	flush()
	if line.closed && len(result) > 0 {
		first, last := &result[0], &result[len(result)-1]
		if first.points[0] == line.points[0] && last.points[len(last.points)-1] == line.points[0] {
			if len(result) == 1 {
				first.closed = len(first.points) > 1
			} else {
				first.points = append(last.points[:len(last.points)-1], first.points...)
				result = result[:len(result)-1]
			}
		}
	}
	return result, nil
}

// stroke 追加同向矩形、连接楔形及圆盘组成的非零填充区域
// 入参: result 已有轮廓, options 描边样式
// 返回: Path 填充轮廓, error 不支持的退化方头端点
func (line geometryPolyline) stroke(result Path, options StrokeOptions) (Path, error) {
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
		result = append(result, Segment{Verb: Move, End: points[0]})
		for _, p := range points[1:] {
			result = append(result, Segment{Verb: Line, End: p})
		}
		result = append(result, Segment{Verb: ClosePath})
	}
	disk := func(p Point) {
		result = append(result, Segment{Verb: Move, End: Point{p.X + half, p.Y}},
			Segment{Verb: Arc, RadiusX: half, RadiusY: half, Sweep: true, End: Point{p.X - half, p.Y}},
			Segment{Verb: Arc, RadiusX: half, RadiusY: half, Sweep: true, End: Point{p.X + half, p.Y}},
			Segment{Verb: ClosePath})
	}
	points := line.points
	if len(points) == 1 {
		if line.closed {
			return nil, UnsupportedError("directionless closed stroke")
		}
		if options.Cap == "Round" {
			disk(points[0])
		} else if options.Cap == "Square" {
			d := line.direction
			if d == (Point{}) {
				return nil, UnsupportedError("directionless square stroke cap")
			}
			p := points[0]
			u, v := Point{d.X * half, d.Y * half}, Point{-d.Y * half, d.X * half}
			polygon(Point{p.X - u.X - v.X, p.Y - u.Y - v.Y}, Point{p.X + u.X - v.X, p.Y + u.Y - v.Y}, Point{p.X + u.X + v.X, p.Y + u.Y + v.Y}, Point{p.X - u.X + v.X, p.Y - u.Y + v.Y})
		}
		return result, nil
	}
	segments := len(points) - 1
	joins := segments - 1
	caps := 0
	if line.closed {
		joins++
	} else if options.Cap == "Round" {
		caps = 8
	} else if options.Cap == "Square" {
		caps = 10
	}
	joinSize := 4
	if options.Join == "" || options.Join == "Miter" {
		joinSize = 5
	}
	result = slices.Grow(result, min(segments*5+joins*joinSize+caps, 1<<20-len(result)))
	directions := make([]Point, segments)
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
			result = append(result, Segment{Verb: Move, End: p},
				Segment{Verb: Line, End: x},
				Segment{Verb: Arc, RadiusX: half, RadiusY: half, Sweep: true, End: y},
				Segment{Verb: ClosePath})
			return
		}
		limit := options.MiterLimit
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

// strokePolylines 自适应展开贝塞尔，弦长亏损同时约束虚线累计长度误差
// 入参: tolerance 展开误差
// 返回: []geometryPolyline 子路径, error 展开限制
func (p Path) strokePolylines(tolerance float64) ([]geometryPolyline, error) {
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
		var work, left, right [4]Point
		copy(work[:], points)
		for n := len(points); n > 0; n-- {
			left[len(points)-n], right[n-1] = work[0], work[n-1]
			for i := 0; i < n-1; i++ {
				work[i] = Lerp(work[i], work[i+1], .5)
			}
		}
		if err := flatten(left[:len(points)], budget/2, depth+1); err != nil {
			return err
		}
		return flatten(right[:len(points)], budget/2, depth+1)
	}
	for _, s := range curves {
		switch s.Verb {
		case Move:
			flush()
			current, start = s.End, s.End
			line.points = []Point{current}
		case ClosePath:
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
			var buffer [4]Point
			points := append(buffer[:0], current)
			if s.Verb == Quad || s.Verb == Cubic {
				points = append(points, s.Control1)
			}
			if s.Verb == Cubic {
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
