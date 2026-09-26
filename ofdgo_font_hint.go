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
	"fmt"
	"maps"
	"math"
)

// ttPoint 保存TrueType当前坐标、原始坐标及点状态，坐标使用26.6定点数
type ttPoint struct {
	x, y, ox, oy int32
	on, tx, ty   bool
}

// ttGraphics 保存TrueType图形状态
type ttGraphics struct {
	projection, freedom, dual                      [2]float64
	zone                                           [3]int32
	reference                                      [3]int32
	loop, minimum, cutIn, singleCutIn, singleWidth int32
	deltaBase, deltaShift                          int32
	period, phase, threshold                       int32
	autoFlip                                       bool
	inhibit, reset                                 bool
}

// ttInterpreter 在设计单位精度执行TrueType字形程序，保留由指令构造的轮廓
type ttInterpreter struct {
	graphics            ttGraphics
	stack, storage, cvt []int32
	zones               [2][]ttPoint
	ends                []uint16
	functions           map[int32][]byte
	instructions        map[byte][]byte
	units               int32
	stackLimit          int
	steps               int
	err                 error
}

// newTTInterpreter 解析字体程序使用的存储容量和控制值
// 入参: tables 字体表, units 每字面设计单位数
// 返回: *ttInterpreter 指令执行器, error 字体表错误
func newTTInterpreter(tables map[string][]byte, units uint16) (*ttInterpreter, error) {
	maxp := tables["maxp"]
	if len(maxp) < 32 || units == 0 || len(tables["cvt "])%2 != 0 {
		return nil, fmt.Errorf("invalid TrueType instruction tables")
	}
	v := &ttInterpreter{
		units: int32(units), stackLimit: int(binary.BigEndian.Uint16(maxp[24:])),
		storage:   make([]int32, binary.BigEndian.Uint16(maxp[18:])),
		functions: map[int32][]byte{}, instructions: map[byte][]byte{},
	}
	v.zones[0] = make([]ttPoint, int(binary.BigEndian.Uint16(maxp[16:]))+4)
	for i := 0; i < len(tables["cvt "]); i += 2 {
		v.cvt = append(v.cvt, int32(int16(binary.BigEndian.Uint16(tables["cvt "][i:])))*64)
	}
	v.graphics = defaultTTGraphics()
	if err := v.run(tables["fpgm"]); err != nil {
		return nil, err
	}
	v.stack = nil
	v.graphics = defaultTTGraphics()
	if err := v.run(tables["prep"]); err != nil {
		return nil, err
	}
	v.stack = nil
	return v, nil
}

// defaultTTGraphics 返回规范规定的初始图形状态
// 返回: ttGraphics 图形状态
func defaultTTGraphics() ttGraphics {
	return ttGraphics{projection: [2]float64{1, 0}, freedom: [2]float64{1, 0}, dual: [2]float64{1, 0}, zone: [3]int32{1, 1, 1}, loop: 1, minimum: 64, cutIn: 68, deltaBase: 9, deltaShift: 3, period: 64, threshold: 32, autoFlip: true}
}

// clone 为单个字形创建独立存储，避免字形顺序和并发改变输出
// 返回: *ttInterpreter 独立执行器
func (v *ttInterpreter) clone() *ttInterpreter {
	c := *v
	c.stack = nil
	c.storage = append([]int32(nil), v.storage...)
	c.cvt = append([]int32(nil), v.cvt...)
	c.functions = maps.Clone(v.functions)
	c.instructions = maps.Clone(v.instructions)
	c.zones[0] = append([]ttPoint(nil), v.zones[0]...)
	c.steps = 0
	if c.graphics.reset {
		c.graphics = defaultTTGraphics()
	}
	c.graphics.projection, c.graphics.freedom, c.graphics.dual = [2]float64{1, 0}, [2]float64{1, 0}, [2]float64{1, 0}
	c.graphics.reference, c.graphics.zone, c.graphics.loop = [3]int32{}, [3]int32{1, 1, 1}, 1
	return &c
}

// pop 取出栈顶操作数并记录栈下溢
// 返回: int32 操作数
func (v *ttInterpreter) pop() int32 {
	if len(v.stack) == 0 {
		v.err = fmt.Errorf("TrueType instruction stack underflow")
		return 0
	}
	x := v.stack[len(v.stack)-1]
	v.stack = v.stack[:len(v.stack)-1]
	return x
}

// push 压入操作数并检查字体声明的栈容量
// 入参: values 操作数
func (v *ttInterpreter) push(values ...int32) {
	if len(v.stack)+len(values) > v.stackLimit {
		v.err = fmt.Errorf("TrueType instruction stack overflow")
		return
	}
	v.stack = append(v.stack, values...)
}

// point 获取指定区的点并检查引用范围
// 入参: zone 区号, index 点号
// 返回: *ttPoint 点
func (v *ttInterpreter) point(zone, index int32) *ttPoint {
	if zone < 0 || zone > 1 || index < 0 || int64(index) >= int64(len(v.zones[zone])) {
		v.err = fmt.Errorf("invalid TrueType point %d in zone %d", index, zone)
		return &ttPoint{}
	}
	return &v.zones[zone][index]
}

// read 读取控制值或存储区
// 入参: values 存储区, index 位置
// 返回: int32 存储值
func (v *ttInterpreter) read(values []int32, index int32) int32 {
	if index < 0 || int64(index) >= int64(len(values)) {
		v.err = fmt.Errorf("invalid TrueType storage reference")
		return 0
	}
	return values[index]
}

// write 写入控制值或存储区
// 入参: values 存储区, index 位置, value 存储值
func (v *ttInterpreter) write(values []int32, index, value int32) {
	if index < 0 || int64(index) >= int64(len(values)) {
		v.err = fmt.Errorf("invalid TrueType storage reference")
		return
	}
	values[index] = value
}

// ttProject 将定点坐标投影到单位向量
// 入参: x 横坐标, y 纵坐标, vector 单位向量
// 返回: int32 投影值
func ttProject(x, y int32, vector [2]float64) int32 {
	return int32(math.Round(float64(x)*vector[0] + float64(y)*vector[1]))
}

// move 沿自由向量移动点，使其投影改变指定距离
// 入参: point 点, distance 投影距离, touch 是否标记触及状态
func (v *ttInterpreter) move(point *ttPoint, distance int32, touch bool) {
	g := &v.graphics
	dot := g.freedom[0]*g.projection[0] + g.freedom[1]*g.projection[1]
	if math.Abs(dot) < 1.0/16384 {
		v.err = fmt.Errorf("perpendicular TrueType movement vectors")
		return
	}
	point.x += int32(math.Round(float64(distance) * g.freedom[0] / dot))
	point.y += int32(math.Round(float64(distance) * g.freedom[1] / dot))
	if touch {
		point.tx = point.tx || g.freedom[0] != 0
		point.ty = point.ty || g.freedom[1] != 0
	}
}

// round 按当前舍入状态调整有符号距离
// 入参: value 距离
// 返回: int32 舍入距离
func (v *ttInterpreter) round(value int32) int32 {
	g := v.graphics
	if g.period == 0 {
		return value
	}
	sign := int32(1)
	if value < 0 {
		sign = -1
		value = -value
	}
	result := (value-g.phase+g.threshold)/g.period*g.period + g.phase
	if result < 0 {
		result = g.phase
	}
	return sign * result
}

// ttInstructionEnd 跳过一条指令及其立即数
// 入参: code 字节码, position 指令位置
// 返回: int 结束位置, error 字节码错误
func ttInstructionEnd(code []byte, position int) (int, error) {
	if position < 0 || position >= len(code) {
		return 0, fmt.Errorf("invalid TrueType instruction offset")
	}
	op := code[position]
	position++
	n := 0
	if op == 0x40 || op == 0x41 {
		if position >= len(code) {
			return 0, fmt.Errorf("truncated TrueType push instruction")
		}
		n = int(code[position])
		position++
		if op == 0x41 {
			n *= 2
		}
	} else if op >= 0xb0 && op <= 0xbf {
		n = int(op&7) + 1
		if op >= 0xb8 {
			n *= 2
		}
	}
	if n > len(code)-position {
		return 0, fmt.Errorf("truncated TrueType push instruction")
	}
	return position + n, nil
}

// skipTTConditional 定位当前条件分支的替代分支或结束位置
// 入参: code 字节码, position 起始位置, alternate 是否停在ELSE
// 返回: int 后续指令位置, error 分支错误
func skipTTConditional(code []byte, position int, alternate bool) (int, error) {
	depth := 0
	for position < len(code) {
		op := code[position]
		if op == 0x58 {
			depth++
		}
		if op == 0x59 {
			if depth == 0 {
				return position + 1, nil
			}
			depth--
		}
		if op == 0x1b && depth == 0 && alternate {
			return position + 1, nil
		}
		var err error
		position, err = ttInstructionEnd(code, position)
		if err != nil {
			return 0, err
		}
	}
	return 0, fmt.Errorf("unterminated TrueType conditional")
}

// run 执行字体、预处理或字形程序
// 入参: code 字节码
// 返回: error 指令错误
func (v *ttInterpreter) run(code []byte) error {
	return v.execute(code, 0)
}

// execute 执行字节码并限制递归与无限循环
// 入参: code 字节码, depth 调用深度
// 返回: error 指令错误
func (v *ttInterpreter) execute(code []byte, depth int) error {
	if depth > 128 {
		return fmt.Errorf("TrueType instruction call depth exceeded")
	}
	g := &v.graphics
	for pc := 0; pc < len(code); {
		v.steps++
		if v.steps > 1000000 {
			return fmt.Errorf("TrueType instruction execution limit exceeded")
		}
		start, op := pc, code[pc]
		end, err := ttInstructionEnd(code, pc)
		if err != nil {
			return err
		}
		pc++
		if op == 0x40 || op == 0x41 || op >= 0xb0 && op <= 0xbf {
			word := op == 0x41 || op >= 0xb8
			if op == 0x40 || op == 0x41 {
				pc++
			}
			for pc < end {
				if word {
					v.push(int32(int16(binary.BigEndian.Uint16(code[pc:]))))
					pc += 2
				} else {
					v.push(int32(code[pc]))
					pc++
				}
			}
		} else {
			switch {
			case op <= 5:
				axis := [2]float64{0, 1}
				if op&1 != 0 {
					axis = [2]float64{1, 0}
				}
				if op < 4 {
					g.projection, g.dual = axis, axis
				}
				if op < 2 || op >= 4 {
					g.freedom = axis
				}
			case op >= 6 && op <= 9 || op == 0x86 || op == 0x87:
				p2, p1 := v.point(g.zone[2], v.pop()), v.point(g.zone[1], v.pop())
				direction := func(x, y int32) [2]float64 {
					a, b := float64(x), float64(y)
					if op&1 != 0 {
						a, b = -b, a
					}
					n := math.Hypot(a, b)
					if n == 0 {
						return [2]float64{1, 0}
					}
					return [2]float64{a / n, b / n}
				}
				axis := direction(p2.x-p1.x, p2.y-p1.y)
				if op == 8 || op == 9 {
					g.freedom = axis
				} else {
					g.projection, g.dual = axis, axis
					if op >= 0x86 {
						g.dual = direction(p2.ox-p1.ox, p2.oy-p1.oy)
					}
				}
			case op == 0x0a || op == 0x0b:
				y, x := float64(int16(v.pop())), float64(int16(v.pop()))
				length := math.Hypot(x, y)
				if length == 0 {
					return fmt.Errorf("zero TrueType vector")
				}
				axis := [2]float64{x / length, y / length}
				if op == 0x0a {
					g.projection, g.dual = axis, axis
				} else {
					g.freedom = axis
				}
			case op == 0x0c || op == 0x0d:
				axis := g.projection
				if op == 0x0d {
					axis = g.freedom
				}
				v.push(int32(math.Round(axis[0]*16384)), int32(math.Round(axis[1]*16384)))
			case op == 0x0e:
				g.freedom = g.projection
			case op == 0x0f:
				v.intersect()
			case op >= 0x10 && op <= 0x12:
				g.reference[op-0x10] = v.pop()
			case op >= 0x13 && op <= 0x16:
				z := v.pop()
				if z < 0 || z > 1 {
					return fmt.Errorf("invalid TrueType zone")
				}
				if op == 0x16 {
					g.zone = [3]int32{z, z, z}
				} else {
					g.zone[op-0x13] = z
				}
			case op == 0x17:
				g.loop = v.pop()
				if g.loop < 0 || int64(g.loop) > int64(v.stackLimit) {
					return fmt.Errorf("invalid TrueType loop count")
				}
			case op == 0x18:
				g.period, g.phase, g.threshold = 64, 0, 32
			case op == 0x19:
				g.period, g.phase, g.threshold = 64, 32, 32
			case op == 0x1a:
				g.minimum = v.pop()
			case op == 0x1b:
				pc, err = skipTTConditional(code, pc, false)
			case op == 0x1c:
				pc = start + int(v.pop())
			case op == 0x1d:
				g.cutIn = v.pop()
			case op == 0x1e:
				g.singleCutIn = v.pop()
			case op == 0x1f:
				g.singleWidth = v.pop() * 64
			case op == 0x20:
				a := v.pop()
				v.push(a, a)
			case op == 0x21:
				v.pop()
			case op == 0x22:
				v.stack = v.stack[:0]
			case op == 0x23:
				a, b := v.pop(), v.pop()
				v.push(a, b)
			case op == 0x24:
				v.push(int32(len(v.stack)))
			case op == 0x25 || op == 0x26:
				n := v.pop()
				if n <= 0 || int64(n) > int64(len(v.stack)) {
					return fmt.Errorf("invalid TrueType stack index")
				}
				i := len(v.stack) - int(n)
				a := v.stack[i]
				if op == 0x26 {
					v.stack = append(v.stack[:i], v.stack[i+1:]...)
				}
				v.push(a)
			case op == 0x27:
				p2, p1 := v.point(g.zone[1], v.pop()), v.point(g.zone[0], v.pop())
				d := ttProject(p2.x-p1.x, p2.y-p1.y, g.projection) / 2
				v.move(p1, d, true)
				v.move(p2, -d, true)
			case op == 0x29:
				p := v.point(g.zone[0], v.pop())
				if g.freedom[0] != 0 {
					p.tx = false
				}
				if g.freedom[1] != 0 {
					p.ty = false
				}
			case op == 0x2a || op == 0x2b:
				id := v.pop()
				count := int32(1)
				if op == 0x2a {
					count = v.pop()
				}
				body, ok := v.functions[id]
				if !ok || count < 0 || count > 1000000 {
					return fmt.Errorf("invalid TrueType function call %d", id)
				}
				for n := int32(0); n < count; n++ {
					if err = v.execute(body, depth+1); err != nil {
						return err
					}
				}
			case op == 0x2c || op == 0x89:
				id := v.pop()
				bodyStart := pc
				for pc < len(code) && code[pc] != 0x2d {
					pc, err = ttInstructionEnd(code, pc)
					if err != nil {
						return err
					}
				}
				if pc == len(code) || id < 0 || op == 0x89 && id > 255 {
					return fmt.Errorf("invalid TrueType function definition")
				}
				if op == 0x2c {
					v.functions[id] = code[bodyStart:pc]
				} else {
					v.instructions[byte(id)] = code[bodyStart:pc]
				}
				pc++
			case op == 0x2d:
				return fmt.Errorf("unexpected TrueType function terminator")
			case op == 0x2e || op == 0x2f:
				i := v.pop()
				p := v.point(g.zone[0], i)
				pos := ttProject(p.x, p.y, g.projection)
				target := pos
				if op&1 != 0 {
					target = v.round(pos)
				}
				v.move(p, target-pos, true)
				g.reference[0], g.reference[1] = i, i
			case op == 0x30 || op == 0x31:
				v.interpolateUntouched(op&1 != 0)
			case op >= 0x32 && op <= 0x37:
				v.shift(op)
			case op == 0x38:
				d := v.pop()
				for n := int32(0); n < g.loop; n++ {
					p := v.point(g.zone[2], v.pop())
					p.x += int32(math.Round(float64(d) * g.freedom[0]))
					p.y += int32(math.Round(float64(d) * g.freedom[1]))
					p.tx = p.tx || g.freedom[0] != 0
					p.ty = p.ty || g.freedom[1] != 0
				}
				g.loop = 1
			case op == 0x39:
				v.interpolatePoints()
			case op == 0x3a || op == 0x3b:
				d, i := v.pop(), v.pop()
				p, r := v.point(g.zone[1], i), v.point(g.zone[0], g.reference[0])
				if g.zone[1] == 0 {
					p.ox = r.ox + int32(math.Round(float64(d)*g.freedom[0]))
					p.oy = r.oy + int32(math.Round(float64(d)*g.freedom[1]))
					p.x, p.y = p.ox, p.oy
				}
				v.move(p, d-ttProject(p.x-r.x, p.y-r.y, g.projection), true)
				g.reference[1], g.reference[2] = g.reference[0], i
				if op&1 != 0 {
					g.reference[0] = i
				}
			case op == 0x3c:
				r := v.point(g.zone[0], g.reference[0])
				for n := int32(0); n < g.loop; n++ {
					p := v.point(g.zone[1], v.pop())
					v.move(p, ttProject(r.x-p.x, r.y-p.y, g.projection), true)
				}
				g.loop = 1
			case op == 0x3d:
				g.period, g.phase, g.threshold = 32, 0, 16
			case op == 0x3e || op == 0x3f:
				cv, i := v.pop(), v.pop()
				p := v.point(g.zone[0], i)
				target := v.read(v.cvt, cv)
				if g.zone[0] == 0 {
					p.x = int32(math.Round(float64(target) * g.freedom[0]))
					p.y = int32(math.Round(float64(target) * g.freedom[1]))
					p.ox, p.oy = p.x, p.y
				}
				pos := ttProject(p.x, p.y, g.projection)
				if op&1 != 0 {
					if ttAbs(target-pos) > g.cutIn {
						target = pos
					}
					target = v.round(target)
				}
				v.move(p, target-pos, true)
				g.reference[0], g.reference[1] = i, i
			case op == 0x42 || op == 0x44 || op == 0x70:
				value, index := v.pop(), v.pop()
				target := v.cvt
				if op == 0x42 {
					target = v.storage
				}
				if op == 0x70 {
					value *= 64
				}
				v.write(target, index, value)
			case op == 0x43:
				v.push(v.read(v.storage, v.pop()))
			case op == 0x45:
				v.push(v.read(v.cvt, v.pop()))
			case op == 0x46 || op == 0x47:
				p := v.point(g.zone[2], v.pop())
				if op&1 != 0 {
					v.push(ttProject(p.ox, p.oy, g.dual))
				} else {
					v.push(ttProject(p.x, p.y, g.projection))
				}
			case op == 0x48:
				value := v.pop()
				p := v.point(g.zone[2], v.pop())
				v.move(p, value-ttProject(p.x, p.y, g.projection), true)
				if g.zone[2] == 0 {
					p.ox, p.oy = p.x, p.y
				}
			case op == 0x49 || op == 0x4a:
				p1, p2 := v.point(g.zone[1], v.pop()), v.point(g.zone[0], v.pop())
				if op&1 == 0 {
					v.push(ttProject(p1.ox-p2.ox, p1.oy-p2.oy, g.dual))
				} else {
					v.push(ttProject(p1.x-p2.x, p1.y-p2.y, g.projection))
				}
			case op == 0x4b:
				v.push(v.units)
			case op == 0x4c:
				v.push(v.units * 64)
			case op == 0x4d:
				g.autoFlip = true
			case op == 0x4e:
				g.autoFlip = false
			case op == 0x4f:
				v.pop()
			case op >= 0x50 && op <= 0x55:
				b, a := v.pop(), v.pop()
				result := false
				switch op {
				case 0x50:
					result = a < b
				case 0x51:
					result = a <= b
				case 0x52:
					result = a > b
				case 0x53:
					result = a >= b
				case 0x54:
					result = a == b
				case 0x55:
					result = a != b
				}
				v.push(ttBool(result))
			case op == 0x56 || op == 0x57:
				r := v.round(v.pop()) / 64 & 1
				v.push(ttBool(r == int32(0x57-op)))
			case op == 0x58:
				if v.pop() == 0 {
					pc, err = skipTTConditional(code, pc, true)
				}
			case op == 0x59:
			case op == 0x5a || op == 0x5b:
				b, a := v.pop() != 0, v.pop() != 0
				if op == 0x5a {
					v.push(ttBool(a && b))
				} else {
					v.push(ttBool(a || b))
				}
			case op == 0x5c:
				v.push(ttBool(v.pop() == 0))
			case op == 0x5d || op >= 0x71 && op <= 0x75:
				v.delta(op)
			case op == 0x5e:
				g.deltaBase = v.pop()
			case op == 0x5f:
				g.deltaShift = v.pop()
				if g.deltaShift < 0 || g.deltaShift > 6 {
					return fmt.Errorf("invalid TrueType delta shift")
				}
			case op >= 0x60 && op <= 0x63:
				b, a := v.pop(), v.pop()
				switch op {
				case 0x60:
					v.push(a + b)
				case 0x61:
					v.push(a - b)
				case 0x62:
					if b == 0 {
						return fmt.Errorf("TrueType division by zero")
					}
					v.push(int32(int64(a) * 64 / int64(b)))
				case 0x63:
					v.push(int32(int64(a) * int64(b) / 64))
				}
			case op == 0x64:
				v.push(ttAbs(v.pop()))
			case op == 0x65:
				v.push(-v.pop())
			case op == 0x66:
				v.push(v.pop() &^ 63)
			case op == 0x67:
				v.push((v.pop() + 63) &^ 63)
			case op >= 0x68 && op <= 0x6b:
				v.push(v.round(v.pop()))
			case op >= 0x6c && op <= 0x6f:
				v.push(v.pop())
			case op == 0x76 || op == 0x77:
				s := v.pop()
				period := 64.0
				if op == 0x77 {
					period = math.Sqrt(0.5) * 64
				}
				switch (s >> 6) & 3 {
				case 0:
					period /= 2
				case 2:
					period *= 2
				case 3:
					return fmt.Errorf("invalid TrueType rounding period")
				}
				g.period = int32(math.Round(period))
				g.phase = int32(math.Round(float64((s>>4)&3) * period / 4))
				if s&15 == 0 {
					g.threshold = g.period - 1
				} else {
					g.threshold = int32(math.Round(float64((s&15)-4) * period / 8))
				}
			case op == 0x78 || op == 0x79:
				condition, offset := v.pop(), v.pop()
				if (condition != 0) == (op == 0x78) {
					pc = start + int(offset)
				}
			case op == 0x7a:
				g.period, g.phase, g.threshold = 0, 0, 0
			case op == 0x7c:
				g.period, g.phase, g.threshold = 64, 0, 63
			case op == 0x7d:
				g.period, g.phase, g.threshold = 64, 0, 0
			case op == 0x7e || op == 0x7f:
				v.pop()
			case op == 0x80:
				for n := int32(0); n < g.loop; n++ {
					p := v.point(g.zone[0], v.pop())
					p.on = !p.on
				}
				g.loop = 1
			case op == 0x81 || op == 0x82:
				hi, lo := v.pop(), v.pop()
				if lo < 0 || hi < lo || int64(hi) >= int64(len(v.zones[g.zone[0]])) {
					return fmt.Errorf("invalid TrueType point range")
				}
				for i := lo; i <= hi; i++ {
					v.point(g.zone[0], i).on = op == 0x81
				}
			case op == 0x85 || op == 0x8d:
				v.pop()
			case op == 0x88:
				selector := v.pop()
				result := int32(0)
				if selector&1 != 0 {
					result = 35
				}
				if selector&32 != 0 {
					result |= 1 << 12
				}
				v.push(result)
			case op == 0x8a:
				c, b, a := v.pop(), v.pop(), v.pop()
				v.push(b, c, a)
			case op == 0x8b:
				b, a := v.pop(), v.pop()
				v.push(max(a, b))
			case op == 0x8c:
				b, a := v.pop(), v.pop()
				v.push(min(a, b))
			case op == 0x8e:
				selector, value := v.pop(), v.pop()
				if selector == 1 {
					g.inhibit = value != 0
				} else if selector == 2 {
					g.reset = value != 0
				} else if selector != 3 {
					return fmt.Errorf("invalid TrueType instruction control")
				}
			case op >= 0xc0:
				v.relativeMove(op)
			default:
				body, ok := v.instructions[op]
				if !ok {
					return fmt.Errorf("unsupported TrueType instruction 0x%02x", op)
				}
				err = v.execute(body, depth+1)
			}
		}
		if err != nil {
			return err
		}
		if v.err != nil {
			return fmt.Errorf("TrueType instruction 0x%02x at %d: %w", op, start, v.err)
		}
		if pc < 0 || pc > len(code) {
			return fmt.Errorf("invalid TrueType jump target")
		}
	}
	return nil
}

// ttAbs 返回定点数绝对值
// 入参: value 定点数
// 返回: int32 绝对值
func ttAbs(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

// ttBool 将逻辑值转换为TrueType栈值
// 入参: value 逻辑值
// 返回: int32 栈值
func ttBool(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

// relativeMove 执行直接或控制值驱动的相对移动
// 入参: op 移动指令
func (v *ttInterpreter) relativeMove(op byte) {
	g := &v.graphics
	var target int32
	if op >= 0xe0 {
		target = v.read(v.cvt, v.pop())
	}
	index := v.pop()
	p, r := v.point(g.zone[1], index), v.point(g.zone[0], g.reference[0])
	if op >= 0xe0 && g.zone[1] == 0 {
		p.ox = r.ox + int32(math.Round(float64(target)*g.freedom[0]))
		p.oy = r.oy + int32(math.Round(float64(target)*g.freedom[1]))
		p.x, p.y = p.ox, p.oy
	}
	original := ttProject(p.ox-r.ox, p.oy-r.oy, g.dual)
	if op < 0xe0 {
		target = original
	} else if g.autoFlip && (original < 0) != (target < 0) {
		target = -target
	}
	if ttAbs(ttAbs(target)-g.singleWidth) < g.singleCutIn {
		target = g.singleWidth
		if original < 0 {
			target = -target
		}
	}
	if op&4 != 0 {
		if op >= 0xe0 && g.zone[0] == g.zone[1] && ttAbs(target-original) > g.cutIn {
			target = original
		}
		target = v.round(target)
	}
	if op&8 != 0 {
		if original >= 0 {
			target = max(target, g.minimum)
		} else {
			target = min(target, -g.minimum)
		}
	}
	v.move(p, target-ttProject(p.x-r.x, p.y-r.y, g.projection), true)
	g.reference[1], g.reference[2] = g.reference[0], index
	if op&16 != 0 {
		g.reference[0] = index
	}
}

// shift 根据参考点位移平移指定点、轮廓或整个区
// 入参: op 平移指令
func (v *ttInterpreter) shift(op byte) {
	g := &v.graphics
	zone, index := g.zone[1], g.reference[2]
	if op&1 != 0 {
		zone, index = g.zone[0], g.reference[1]
	}
	r := v.point(zone, index)
	distance := ttProject(r.x-r.ox, r.y-r.oy, g.projection)
	if op <= 0x33 {
		for i := int32(0); i < g.loop; i++ {
			v.move(v.point(g.zone[2], v.pop()), distance, true)
		}
		g.loop = 1
		return
	}
	from, to := 0, 0
	target := g.zone[2]
	if op <= 0x35 {
		contour := v.pop()
		if contour < 0 || int64(contour) >= int64(len(v.ends)) {
			v.err = fmt.Errorf("invalid TrueType contour reference")
			return
		}
		if contour > 0 {
			from = int(v.ends[contour-1]) + 1
		}
		to = int(v.ends[contour]) + 1
	} else {
		target = v.pop()
		if target < 0 || target > 1 {
			v.err = fmt.Errorf("invalid TrueType zone")
			return
		}
		to = len(v.zones[target])
		if target == 1 {
			to -= 4
		}
	}
	for i := from; i < to; i++ {
		if target == zone && i == int(index) {
			continue
		}
		v.move(v.point(target, int32(i)), distance, op <= 0x35)
	}
}

// interpolatePoints 保持点在两个参考点之间的原始相对比例
func (v *ttInterpreter) interpolatePoints() {
	g := &v.graphics
	r1, r2 := v.point(g.zone[0], g.reference[1]), v.point(g.zone[1], g.reference[2])
	original := ttProject(r2.ox-r1.ox, r2.oy-r1.oy, g.dual)
	current := ttProject(r2.x-r1.x, r2.y-r1.y, g.projection)
	for i := int32(0); i < g.loop; i++ {
		p := v.point(g.zone[2], v.pop())
		d := ttProject(p.ox-r1.ox, p.oy-r1.oy, g.dual)
		if original != 0 {
			d = int32(math.Round(float64(d) * float64(current) / float64(original)))
		}
		v.move(p, d-ttProject(p.x-r1.x, p.y-r1.y, g.projection), true)
	}
	g.loop = 1
}

// interpolateUntouched 按轮廓循环插值未触及点，不改变独立轮廓
// 入参: horizontal 是否沿横轴插值
func (v *ttInterpreter) interpolateUntouched(horizontal bool) {
	if v.graphics.zone[2] != 1 {
		v.err = fmt.Errorf("invalid TrueType interpolation zone")
		return
	}
	points := v.zones[1]
	original := func(p ttPoint) int32 {
		if horizontal {
			return p.ox
		}
		return p.oy
	}
	current := func(p ttPoint) int32 {
		if horizontal {
			return p.x
		}
		return p.y
	}
	touched := func(p ttPoint) bool {
		if horizontal {
			return p.tx
		}
		return p.ty
	}
	set := func(p *ttPoint, value int32) {
		if horizontal {
			p.x = value
		} else {
			p.y = value
		}
	}
	first := 0
	for _, endpoint := range v.ends {
		last := int(endpoint)
		if last < first || last >= len(points) {
			v.err = fmt.Errorf("invalid TrueType contour endpoints")
			return
		}
		anchors := []int{}
		for i := first; i <= last; i++ {
			if touched(points[i]) {
				anchors = append(anchors, i)
			}
		}
		for n, left := range anchors {
			right := anchors[(n+1)%len(anchors)]
			a, b := points[left], points[right]
			if original(a) > original(b) {
				a, b = b, a
			}
			for i := (left-first+1)%(last-first+1) + first; i != right; i = (i-first+1)%(last-first+1) + first {
				x := original(points[i])
				var value int32
				if len(anchors) == 1 || x <= original(a) {
					value = x + current(a) - original(a)
				} else if x >= original(b) {
					value = x + current(b) - original(b)
				} else {
					value = current(a) + int32(math.Round(float64(x-original(a))*float64(current(b)-current(a))/float64(original(b)-original(a))))
				}
				set(&points[i], value)
			}
		}
		first = last + 1
	}
}

// delta 对指定像素尺寸应用局部点位或控制值修正
// 入参: op 修正指令
func (v *ttInterpreter) delta(op byte) {
	n := v.pop()
	if n < 0 || int64(n)*2 > int64(len(v.stack)) {
		v.err = fmt.Errorf("invalid TrueType delta count")
		return
	}
	base := v.graphics.deltaBase
	if op == 0x71 || op == 0x74 {
		base += 16
	}
	if op == 0x72 || op == 0x75 {
		base += 32
	}
	for i := int32(0); i < n; i++ {
		index, arg := v.pop(), v.pop()
		if base+((arg>>4)&15) != v.units {
			continue
		}
		step := (arg & 15) - 8
		if step >= 0 {
			step++
		}
		distance := step * (64 >> v.graphics.deltaShift)
		if op >= 0x73 {
			v.write(v.cvt, index, v.read(v.cvt, index)+distance)
		} else {
			v.move(v.point(v.graphics.zone[0], index), distance, true)
		}
	}
}

// intersect 将目标点移到两条直线的交点，平行线使用端点平均值
func (v *ttInterpreter) intersect() {
	g := &v.graphics
	b1, b0 := v.point(g.zone[0], v.pop()), v.point(g.zone[0], v.pop())
	a1, a0 := v.point(g.zone[1], v.pop()), v.point(g.zone[1], v.pop())
	p := v.point(g.zone[2], v.pop())
	ax, ay, bx, by := float64(a1.x-a0.x), float64(a1.y-a0.y), float64(b1.x-b0.x), float64(b1.y-b0.y)
	det := ax*by - ay*bx
	if math.Abs(det) < 1 {
		p.x = int32((int64(a0.x) + int64(a1.x) + int64(b0.x) + int64(b1.x)) / 4)
		p.y = int32((int64(a0.y) + int64(a1.y) + int64(b0.y) + int64(b1.y)) / 4)
	} else {
		t := (float64(b0.x-a0.x)*by - float64(b0.y-a0.y)*bx) / det
		p.x = a0.x + int32(math.Round(t*ax))
		p.y = a0.y + int32(math.Round(t*ay))
	}
	p.tx, p.ty = true, true
}
