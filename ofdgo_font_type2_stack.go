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
	"slices"
)

// calculate 求值Type2算术、条件、栈及暂存指令，不写入输出程序
// 入参: op 操作符
// 返回: error 操作数或运算错误
func (s *type2State) calculate(op int) error {
	count := 0
	switch op {
	case 1223:
		s.seed = uint64(uint32(s.seed*1664525 + 1013904223))
		return s.push(float64(s.seed+1) / 4294967296)
	case 1205, 1209, 1214, 1218, 1221, 1226, 1227, 1229:
		count = 1
	case 1203, 1204, 1210, 1211, 1212, 1215, 1220, 1224, 1228, 1230:
		count = 2
	case 1222:
		count = 4
	default:
		return fmt.Errorf("unsupported Type2 operator %d", op)
	}
	if len(s.args) < count {
		return fmt.Errorf("missing Type2 operands for operator %d", op)
	}
	values := s.args[len(s.args)-count:]
	value := values[0]
	switch op {
	case 1203:
		value = 0
		if values[0] != 0 && values[1] != 0 {
			value = 1
		}
	case 1204:
		value = 0
		if values[0] != 0 || values[1] != 0 {
			value = 1
		}
	case 1205:
		value = 0
		if values[0] == 0 {
			value = 1
		}
	case 1209:
		value = math.Abs(value)
	case 1210:
		value += values[1]
	case 1211:
		value -= values[1]
	case 1212:
		value /= values[1]
	case 1214:
		value = -value
	case 1215:
		value = 0
		if values[0] == values[1] {
			value = 1
		}
	case 1218:
		s.args = s.args[:len(s.args)-1]
		return nil
	case 1220, 1221:
		index := values[count-1]
		if index < 0 || index >= float64(len(s.transient)) || index != math.Trunc(index) {
			return fmt.Errorf("invalid Type2 transient index")
		}
		if op == 1220 {
			s.transient[int(index)] = value
			s.stored |= 1 << uint(index)
			s.args = s.args[:len(s.args)-2]
			return nil
		}
		if s.stored&(1<<uint(index)) == 0 {
			return fmt.Errorf("uninitialized Type2 transient value")
		}
		value = s.transient[int(index)]
	case 1222:
		if values[2] > values[3] {
			value = values[1]
		}
	case 1224:
		value *= values[1]
	case 1226:
		value = math.Sqrt(value)
	case 1227:
		return s.push(value)
	case 1228:
		values[0], values[1] = values[1], values[0]
		return nil
	case 1229:
		if value != math.Trunc(value) || value >= float64(len(s.args)-1) {
			return fmt.Errorf("invalid Type2 stack index")
		}
		index := max(0, value)
		if len(s.args) < 2 {
			return fmt.Errorf("empty Type2 indexed stack")
		}
		value = s.args[len(s.args)-2-int(index)]
	case 1230:
		n, j := values[0], values[1]
		if n < 0 || n > float64(len(s.args)-2) || n != math.Trunc(n) || j != math.Trunc(j) {
			return fmt.Errorf("invalid Type2 stack roll")
		}
		s.args = s.args[:len(s.args)-2]
		if n == 0 {
			return nil
		}
		shift := int(math.Mod(j, n))
		if shift < 0 {
			shift += int(n)
		}
		tail := s.args[len(s.args)-int(n):]
		slices.Reverse(tail)
		slices.Reverse(tail[:shift])
		slices.Reverse(tail[shift:])
		return nil
	}
	s.args = s.args[:len(s.args)-count]
	return s.push(value)
}

// draw 校验Type2路径指令并推进当前点，保留曲线及Flex编码
// 入参: op 操作符
// 返回: error 路径操作数或输出错误
func (s *type2State) draw(op int) error {
	a := s.args
	valid := true
	dx, dy := 0.0, 0.0
	switch op {
	case 5:
		valid = len(a) >= 2 && len(a)%2 == 0
		if valid {
			for i := 0; i < len(a); i += 2 {
				dx, dy = dx+a[i], dy+a[i+1]
			}
		}
	case 6, 7:
		valid = len(a) > 0
		for i, value := range a {
			if (i%2 == 0) == (op == 6) {
				dx += value
			} else {
				dy += value
			}
		}
	case 8, 24, 25:
		start, end := 0, len(a)
		if op == 24 {
			end -= 2
			valid = len(a) >= 8 && end%6 == 0
		} else if op == 25 {
			start = len(a) - 6
			valid = len(a) >= 8 && start%2 == 0
		} else {
			valid = len(a) >= 6 && len(a)%6 == 0
		}
		if valid {
			for i := start; i < end; i += 2 {
				dx, dy = dx+a[i], dy+a[i+1]
			}
			for i := 0; i < start; i += 2 {
				dx, dy = dx+a[i], dy+a[i+1]
			}
			if end < len(a) {
				dx, dy = dx+a[end], dy+a[end+1]
			}
		}
	case 26, 27:
		valid = len(a) >= 4 && (len(a)%4 == 0 || len(a)%4 == 1)
		if valid {
			i := 0
			if len(a)%4 == 1 {
				if op == 26 {
					dx = a[0]
				} else {
					dy = a[0]
				}
				i++
			}
			for ; i < len(a); i += 4 {
				if op == 26 {
					dx, dy = dx+a[i+1], dy+a[i]+a[i+2]+a[i+3]
				} else {
					dx, dy = dx+a[i]+a[i+1]+a[i+3], dy+a[i+2]
				}
			}
		}
	case 30, 31:
		valid = len(a) >= 4 && (len(a)%4 == 0 || len(a)%4 == 1)
		if valid {
			horizontal := op == 31
			for i := 0; i+4 <= len(a); i += 4 {
				if horizontal {
					dx, dy = dx+a[i]+a[i+1], dy+a[i+2]+a[i+3]
				} else {
					dx, dy = dx+a[i+1]+a[i+3], dy+a[i]+a[i+2]
				}
				if i+5 == len(a) {
					if horizontal {
						dx += a[i+4]
					} else {
						dy += a[i+4]
					}
				}
				horizontal = !horizontal
			}
		}
	case 1234:
		valid = len(a) == 7
		if valid {
			dx = a[0] + a[1] + a[3] + a[4] + a[5] + a[6]
		}
	case 1235:
		valid = len(a) == 13
		if valid {
			for i := 0; i < 12; i += 2 {
				dx, dy = dx+a[i], dy+a[i+1]
			}
		}
	case 1236:
		valid = len(a) == 9
		if valid {
			dx = a[0] + a[2] + a[4] + a[5] + a[6] + a[8]
		}
	case 1237:
		valid = len(a) == 11
		if valid {
			for i := 0; i < 10; i += 2 {
				dx, dy = dx+a[i], dy+a[i+1]
			}
			if math.Abs(dx) > math.Abs(dy) {
				dx, dy = dx+a[10], 0
			} else {
				dx, dy = 0, dy+a[10]
			}
		}
	default:
		valid = false
	}
	if !valid || !finite(s.x+dx) || !finite(s.y+dy) {
		return fmt.Errorf("invalid Type2 path operands for operator %d", op)
	}
	var err error
	if s.matrix != nil {
		err = s.emitTransformedPath(op, a)
	} else {
		err = s.emit(op, a)
	}
	if err != nil {
		return err
	}
	s.x, s.y = s.x+dx, s.y+dy
	s.outX, s.outY = s.transformedPoint()
	s.args = s.args[:0]
	return nil
}
