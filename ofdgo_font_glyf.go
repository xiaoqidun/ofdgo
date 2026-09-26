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
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/tdewolff/font"
)

// sfntOutliner 缓存字体程序并提取独立字形轮廓
type sfntOutliner struct {
	font     *font.SFNT
	once     sync.Once
	program  *ttInterpreter
	err      error
	mu       sync.Mutex
	warnings map[uint16]error
}

// ttGlyph 保存字形轮廓及四个度量虚拟点
type ttGlyph struct {
	points  []ttPoint
	ends    []uint16
	metrics *[4]ttPoint
	warning error
}

// path 按TrueType指令和隐含点规则生成闭合轮廓，CFF保留原有解析
// 入参: path 路径接收器, glyph 字形编号, scale 字体单位缩放
// 返回: error 字形解析错误
func (f *sfntOutliner) path(path font.Pather, glyph uint16, scale float64) error {
	if !f.font.IsTrueType {
		return f.font.GlyphPath(path, glyph, 0, 0, 0, scale, font.NoHinting)
	}
	contour, err := f.glyph(glyph, make(map[uint16]bool))
	if err != nil {
		return err
	}
	points := contour.points
	first := 0
	for _, endpoint := range contour.ends {
		last := int(endpoint)
		if last < first || last >= len(points)-4 {
			return fmt.Errorf("invalid TrueType contour endpoints")
		}
		if last == first {
			first = last + 1
			continue
		}
		point := func(i int) Point {
			return Point{X: float64(points[i].x) * scale / 64, Y: float64(points[i].y) * scale / 64}
		}
		start, end := point(first), point(last)
		begin, stop := first, last
		if points[first].on {
			begin++
		} else if points[last].on {
			start, stop = end, last-1
		} else {
			start = Point{X: (start.X + end.X) / 2, Y: (start.Y + end.Y) / 2}
		}
		path.MoveTo(start.X, start.Y)
		var control Point
		pending := false
		for i := begin; i <= stop; i++ {
			p := point(i)
			if points[i].on {
				if pending {
					path.QuadTo(control.X, control.Y, p.X, p.Y)
				} else {
					path.LineTo(p.X, p.Y)
				}
				pending = false
			} else {
				if pending {
					path.QuadTo(control.X, control.Y, (control.X+p.X)/2, (control.Y+p.Y)/2)
				}
				control, pending = p, true
			}
		}
		if pending {
			path.QuadTo(control.X, control.Y, start.X, start.Y)
		}
		path.Close()
		first = last + 1
	}
	return nil
}

// glyph 解码简单或复合字形并执行对应字节码，不改写字体资源
// 入参: id 字形编号, active 递归访问集合
// 返回: ttGlyph 字形轮廓, error 字形错误
func (f *sfntOutliner) glyph(id uint16, active map[uint16]bool) (ttGlyph, error) {
	var result ttGlyph
	if id >= f.font.NumGlyphs() || active[id] {
		return result, fmt.Errorf("invalid TrueType component reference %d", id)
	}
	active[id] = true
	defer delete(active, id)
	data := f.font.Glyf.Get(id)
	if len(data) == 0 {
		data = make([]byte, 12)
	}
	if len(data) < 10 {
		return result, fmt.Errorf("truncated TrueType glyph %d", id)
	}
	n := int(int16(binary.BigEndian.Uint16(data)))
	var program []byte
	var err error
	if n >= 0 {
		result, program, err = readTTSimple(data, n)
	} else {
		result, program, err = f.composite(data, active)
	}
	if err != nil {
		return result, err
	}
	if result.warning != nil {
		f.recordGlyphWarning(id, result.warning)
	}
	left := int32(int16(binary.BigEndian.Uint16(data[2:]))) - int32(f.font.Hmtx.LeftSideBearing(id))
	top := int32(int16(binary.BigEndian.Uint16(data[8:])))
	if result.metrics != nil {
		result.points = append(result.points, result.metrics[:]...)
	} else {
		result.points = append(result.points, ttPoint{x: left * 64, ox: left * 64}, ttPoint{x: (left + int32(f.font.GlyphAdvance(id))) * 64, ox: (left + int32(f.font.GlyphAdvance(id))) * 64}, ttPoint{y: top * 64, oy: top * 64}, ttPoint{y: (top - int32(f.font.GlyphVerticalAdvance(id))) * 64, oy: (top - int32(f.font.GlyphVerticalAdvance(id))) * 64})
	}
	if len(program) == 0 {
		return result, nil
	}
	outlineOnly, err := ttOutlineOnly(f.font.Tables, program)
	if err != nil {
		return result, err
	}
	if outlineOnly {
		return result, nil
	}
	f.once.Do(func() { f.program, f.err = newTTInterpreter(f.font.Tables, f.font.UnitsPerEm()) })
	if f.err != nil {
		return result, f.err
	}
	if f.program.graphics.inhibit {
		return result, nil
	}
	vm := f.program.clone()
	vm.zones[1], vm.ends = append([]ttPoint(nil), result.points...), result.ends
	if err := vm.run(program); err != nil {
		if errors.Is(err, errTTStackUnderflow) {
			result.warning = err
			f.recordGlyphWarning(id, err)
			return result, nil
		}
		return result, fmt.Errorf("TrueType glyph %d: %w", id, err)
	}
	result.points = vm.zones[1]
	return result, nil
}

// recordGlyphWarning 记录字形恢复提示，允许并发提取轮廓
// 入参: id 字形编号, warning 恢复原因
func (f *sfntOutliner) recordGlyphWarning(id uint16, warning error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.warnings == nil {
		f.warnings = make(map[uint16]error)
	}
	f.warnings[id] = warning
}

// glyphWarning 返回字形恢复为执行前轮廓的原因
// 入参: id 字形编号
// 返回: error 恢复原因，无恢复时为空
func (f *sfntOutliner) glyphWarning(id uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.warnings[id]
}

// readTTSimple 读取简单字形的重复标志、差分坐标和字节码
// 入参: data 字形数据, count 轮廓数
// 返回: ttGlyph 字形轮廓, []byte 字节码, error 数据错误
func readTTSimple(data []byte, count int) (ttGlyph, []byte, error) {
	var result ttGlyph
	if len(data) < 12+2*count {
		return result, nil, fmt.Errorf("truncated TrueType contour endpoints")
	}
	pos := 10
	for i := 0; i < count; i++ {
		end := binary.BigEndian.Uint16(data[pos:])
		pos += 2
		if i > 0 && end <= result.ends[i-1] {
			return result, nil, fmt.Errorf("unordered TrueType contour endpoints")
		}
		result.ends = append(result.ends, end)
	}
	length := int(binary.BigEndian.Uint16(data[pos:]))
	pos += 2
	if length > len(data)-pos {
		return result, nil, fmt.Errorf("truncated TrueType glyph instructions")
	}
	program := data[pos : pos+length]
	pos += length
	points := 0
	if count > 0 {
		points = int(result.ends[count-1]) + 1
	}
	flags := make([]byte, 0, points)
	for len(flags) < points {
		if pos >= len(data) {
			return result, nil, fmt.Errorf("truncated TrueType point flags")
		}
		flag := data[pos]
		pos++
		repeat := 1
		if flag&8 != 0 {
			if pos >= len(data) {
				return result, nil, fmt.Errorf("truncated TrueType flag repeat")
			}
			repeat += int(data[pos])
			pos++
		}
		if repeat > points-len(flags) {
			return result, nil, fmt.Errorf("excessive TrueType flag repeat")
		}
		for i := 0; i < repeat; i++ {
			flags = append(flags, flag)
		}
	}
	result.points = make([]ttPoint, points)
	for axis := 0; axis < 2; axis++ {
		coordinate := int32(0)
		short, same := byte(2<<axis), byte(16<<axis)
		for i, flag := range flags {
			if flag&short != 0 {
				if pos >= len(data) {
					return result, nil, fmt.Errorf("truncated TrueType short coordinate")
				}
				delta := int32(data[pos])
				pos++
				if flag&same == 0 {
					delta = -delta
				}
				coordinate += delta
			} else if flag&same == 0 {
				if len(data)-pos < 2 {
					return result, nil, fmt.Errorf("truncated TrueType coordinate")
				}
				coordinate += int32(int16(binary.BigEndian.Uint16(data[pos:])))
				pos += 2
			}
			p := &result.points[i]
			p.on = flag&1 != 0
			if axis == 0 {
				p.x, p.ox = coordinate*64, coordinate*64
			} else {
				p.y, p.oy = coordinate*64, coordinate*64
			}
		}
	}
	return result, program, nil
}

// composite 合成分量变换和点匹配，保留分量顺序及父级字节码
// 入参: data 复合字形数据, active 递归访问集合
// 返回: ttGlyph 字形轮廓, []byte 字节码, error 数据错误
func (f *sfntOutliner) composite(data []byte, active map[uint16]bool) (ttGlyph, []byte, error) {
	var result ttGlyph
	pos := 10
	hasInstructions := false
	for {
		if len(data)-pos < 6 {
			return result, nil, fmt.Errorf("truncated TrueType component")
		}
		flags, id := binary.BigEndian.Uint16(data[pos:]), binary.BigEndian.Uint16(data[pos+2:])
		pos += 4
		a, b := int32(data[pos]), int32(data[pos+1])
		pos += 2
		if flags&1 != 0 {
			pos -= 2
			if len(data)-pos < 4 {
				return result, nil, fmt.Errorf("truncated TrueType component arguments")
			}
			a, b = int32(binary.BigEndian.Uint16(data[pos:])), int32(binary.BigEndian.Uint16(data[pos+2:]))
			pos += 4
		}
		if flags&2 != 0 {
			if flags&1 != 0 {
				a, b = int32(int16(a)), int32(int16(b))
			} else {
				a, b = int32(int8(a)), int32(int8(b))
			}
		}
		matrix := [4]float64{1, 0, 0, 1}
		indices := []int(nil)
		switch flags & 0xc8 {
		case 8:
			indices = []int{0}
		case 0x40:
			indices = []int{0, 3}
		case 0x80:
			indices = []int{0, 1, 2, 3}
		case 0:
		default:
			return result, nil, fmt.Errorf("conflicting TrueType component transforms")
		}
		for _, index := range indices {
			if len(data)-pos < 2 {
				return result, nil, fmt.Errorf("truncated TrueType component transform")
			}
			matrix[index] = float64(int16(binary.BigEndian.Uint16(data[pos:]))) / 16384
			pos += 2
		}
		if flags&8 != 0 {
			matrix[3] = matrix[0]
		}
		part, err := f.glyph(id, active)
		if err != nil {
			return result, nil, err
		}
		if part.warning != nil {
			result.warning = fmt.Errorf("TrueType component %d: %w", id, part.warning)
		}
		if flags&0x200 != 0 {
			result.metrics = new([4]ttPoint)
			copy(result.metrics[:], part.points[len(part.points)-4:])
		}
		part.points = part.points[:len(part.points)-4]
		for i := range part.points {
			p := &part.points[i]
			x, y := float64(p.x), float64(p.y)
			p.x = int32(math.Round(matrix[0]*x + matrix[2]*y))
			p.y = int32(math.Round(matrix[1]*x + matrix[3]*y))
		}
		dx, dy := a*64, b*64
		if flags&2 == 0 {
			if a < 0 || b < 0 || int64(a) >= int64(len(result.points)) || int64(b) >= int64(len(part.points)) {
				return result, nil, fmt.Errorf("invalid TrueType component point matching")
			}
			dx, dy = result.points[a].x-part.points[b].x, result.points[a].y-part.points[b].y
		} else if flags&0x1800 == 0x800 {
			x, y := float64(dx), float64(dy)
			dx, dy = int32(math.Round(matrix[0]*x+matrix[2]*y)), int32(math.Round(matrix[1]*x+matrix[3]*y))
		}
		if flags&4 != 0 {
			dx, dy = int32(math.Round(float64(dx)/64))*64, int32(math.Round(float64(dy)/64))*64
		}
		if len(result.points)+len(part.points) > 65536 {
			return result, nil, fmt.Errorf("excessive TrueType component points")
		}
		for _, end := range part.ends {
			result.ends = append(result.ends, uint16(len(result.points)+int(end)))
		}
		for i := range part.points {
			p := &part.points[i]
			p.x += dx
			p.y += dy
			p.ox, p.oy = p.x, p.y
			p.tx, p.ty = false, false
		}
		result.points = append(result.points, part.points...)
		hasInstructions = hasInstructions || flags&0x100 != 0
		if flags&0x20 == 0 {
			break
		}
	}
	var program []byte
	if hasInstructions {
		if len(data)-pos < 2 {
			return result, nil, fmt.Errorf("truncated TrueType composite instructions")
		}
		n := int(binary.BigEndian.Uint16(data[pos:]))
		pos += 2
		if n > len(data)-pos {
			return result, nil, fmt.Errorf("truncated TrueType composite instructions")
		}
		program = data[pos : pos+n]
	}
	return result, program, nil
}

// ttOutlineOnly 识别已剥离提示表但仍引用字体函数或控制值的子集字形，使用设计轮廓
// 入参: tables 字体表, code 字形程序
// 返回: bool 是否只提取轮廓, error 字节码错误
func ttOutlineOnly(tables map[string][]byte, code []byte) (bool, error) {
	if len(tables["fpgm"])+len(tables["prep"])+len(tables["cvt "]) != 0 {
		return false, nil
	}
	for pc := 0; pc < len(code); {
		op := code[pc]
		if op == 0x2a || op == 0x2b || op == 0x3e || op == 0x3f || op == 0x44 || op == 0x45 || op == 0x70 || op >= 0x73 && op <= 0x75 || op >= 0xe0 {
			return true, nil
		}
		var err error
		pc, err = ttInstructionEnd(code, pc)
		if err != nil {
			return false, err
		}
	}
	return false, nil
}
