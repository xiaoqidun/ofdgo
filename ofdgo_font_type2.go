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
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

// type2Font 保存Type2字形程序、子程序及私有字宽
type type2Font struct {
	data         []byte
	chars        [][]byte
	locals       [][]byte
	globals      [][]byte
	names        map[string]int
	nominal, def float64
	seed         uint64
}

// type2State 按Type2规范保存跨子程序共享的执行状态
type type2State struct {
	font             *type2Font
	widthTarget      *type2Font
	args             []float64
	transient        [32]float64
	stored           uint32
	width            float64
	widthSet         bool
	widthOnly        bool
	hints            int
	x, y             float64
	outX, outY       float64
	originX, originY float64
	component        bool
	stripHints       bool
	pathStarted      bool
	changed          bool
	output           *bytes.Buffer
	seed             uint64
	steps            *int
}

// readType2Index 读取并校验CFF索引的全部偏移，不复制程序字节
// 入参: data 字体数据, offset 索引偏移
// 返回: [][]byte 索引项, int 索引结束位置, error 错误信息
func readType2Index(data []byte, offset int) ([][]byte, int, error) {
	if offset < 0 || offset > len(data)-2 {
		return nil, 0, fmt.Errorf("truncated CFF index")
	}
	count := int(binary.BigEndian.Uint16(data[offset:]))
	if count == 0 {
		return nil, offset + 2, nil
	}
	if offset > len(data)-3 {
		return nil, 0, fmt.Errorf("truncated CFF index header")
	}
	width := int(data[offset+2])
	if width < 1 || width > 4 || count+1 > (len(data)-offset-3)/width {
		return nil, 0, fmt.Errorf("invalid CFF index offsets")
	}
	base := offset + 3 + (count+1)*width
	items := make([][]byte, count)
	previous := uint64(0)
	for index := 0; index <= count; index++ {
		value := uint64(0)
		for _, b := range data[offset+3+index*width : offset+3+(index+1)*width] {
			value = value<<8 | uint64(b)
		}
		if value < 1 || value-1 > uint64(len(data)-base) || value < previous || index == 0 && value != 1 {
			return nil, 0, fmt.Errorf("invalid CFF index range")
		}
		if index != 0 {
			items[index-1] = data[base+int(previous)-1 : base+int(value)-1]
		}
		previous = value
	}
	return items, base + int(previous) - 1, nil
}

// readType2Font 读取单FD字体的Type2执行资源，CID字体先经字体规范化
// 入参: data CFF字体数据
// 返回: *type2Font 执行资源, error 错误信息
func readType2Font(data []byte) (*type2Font, error) {
	if !isBareCFFData(data) {
		return nil, fmt.Errorf("invalid CFF header")
	}
	names, end, err := readType2Index(data, int(data[2]))
	if err != nil {
		return nil, fmt.Errorf("invalid CFF name index: %w", err)
	}
	if len(names) != 1 {
		return nil, fmt.Errorf("invalid CFF font count")
	}
	tops, end, err := readType2Index(data, end)
	if err != nil {
		return nil, fmt.Errorf("invalid CFF top index: %w", err)
	}
	if len(tops) != 1 {
		return nil, fmt.Errorf("invalid CFF top dictionary count")
	}
	_, end, err = readType2Index(data, end)
	if err != nil {
		return nil, err
	}
	globals, _, err := readType2Index(data, end)
	if err != nil {
		return nil, err
	}
	dict := parseCFFDict(tops[0])
	if value, ok := dict[1206]; ok && (len(value) != 1 || value[0] != 2) {
		return nil, fmt.Errorf("unsupported CFF charstring type")
	}
	if _, cid := dict[1230]; cid {
		return nil, fmt.Errorf("CID CFF requires FD normalization")
	}
	offset := dict[17]
	if len(offset) != 1 || !finite(offset[0]) || offset[0] < 0 || offset[0] > float64(len(data)) || offset[0] != math.Trunc(offset[0]) {
		return nil, fmt.Errorf("invalid CFF charstrings offset")
	}
	chars, _, err := readType2Index(data, int(offset[0]))
	if err != nil {
		return nil, fmt.Errorf("invalid CFF charstrings: %w", err)
	}
	if len(chars) == 0 {
		return nil, fmt.Errorf("empty CFF charstrings")
	}
	font := &type2Font{data: data, chars: chars, globals: globals, seed: 1}
	if _, err := readType2Private(data, dict[18], font); err != nil {
		return nil, err
	}
	return font, nil
}

// readType2Private 读取FD私有字宽、随机种子和局部子程序
// 入参: data 字体数据, values Private范围, font 执行资源
// 返回: cffDict 私有字典, error 范围或字段错误
func readType2Private(data []byte, values []float64, font *type2Font) (cffDict, error) {
	private := cffDict{}
	var err error
	if values != nil {
		if len(values) != 2 || !finite(values[0]) || !finite(values[1]) || values[0] < 0 || values[1] < 0 || values[0] > float64(len(data)) || values[1] > float64(len(data))-values[0] || values[0] != math.Trunc(values[0]) || values[1] != math.Trunc(values[1]) {
			return nil, fmt.Errorf("invalid CFF private range")
		}
		start := int(values[1])
		private = parseCFFDict(data[start : start+int(values[0])])
		for op, target := range map[int]*float64{20: &font.def, 21: &font.nominal} {
			if value, ok := private[op]; ok {
				if len(value) != 1 || !finite(value[0]) {
					return nil, fmt.Errorf("invalid CFF private width")
				}
				*target = value[0]
			}
		}
		if value, ok := private[1219]; ok {
			if len(value) != 1 || !finite(value[0]) || value[0] < math.MinInt32 || value[0] > math.MaxInt32 || value[0] != math.Trunc(value[0]) {
				return nil, fmt.Errorf("invalid CFF random seed")
			}
			font.seed = uint64(int64(value[0])) | 1
		}
		if value, ok := private[19]; ok {
			if len(value) != 1 || !finite(value[0]) || value[0] < 0 || value[0] > float64(len(data)-start) || value[0] != math.Trunc(value[0]) {
				return nil, fmt.Errorf("invalid CFF local subroutine offset")
			}
			font.locals, _, err = readType2Index(data, start+int(value[0]))
			if err != nil {
				return nil, err
			}
		}
	}
	return private, nil
}

// normalizeType2Programs 展开组合字形和求值指令，普通字形保留原程序及提示
// 无法规范化的字形保留原程序，由实际使用时的轮廓解析校验
// 入参: data CFF字体数据
// 返回: [][]byte 字形程序, bool 是否变更, error 错误信息
func normalizeType2Programs(data []byte) ([][]byte, bool, error) {
	font, err := readType2Font(data)
	if err != nil {
		return nil, false, err
	}
	result := slices.Clone(font.chars)
	changed := false
	var stack [48]float64
	for gid, program := range font.chars {
		if len(program) == 0 {
			continue
		}
		steps := 0
		state := type2State{font: font, args: stack[:0], width: font.def, seed: font.seed + uint64(gid), steps: &steps}
		_, err := state.run(program, 0)
		if err != nil {
			continue
		}
		if !state.changed {
			continue
		}
		var output bytes.Buffer
		steps = 0
		state = type2State{font: font, args: stack[:0], width: font.def, seed: font.seed + uint64(gid), steps: &steps, output: &output}
		if _, err := state.run(program, 0); err != nil {
			return nil, false, fmt.Errorf("CFF glyph %d: %w", gid, err)
		}
		result[gid] = output.Bytes()
		changed = true
	}
	return result, changed, nil
}

// push 按Type2规范附录B校验并追加操作数
// 入参: value 操作数
// 返回: error 栈或数值错误
func (s *type2State) push(value float64) error {
	if !finite(value) || len(s.args) >= 48 {
		return fmt.Errorf("invalid Type2 operand stack")
	}
	s.args = append(s.args, value)
	return nil
}

// emit 写入已经求值的操作数及指令，分析阶段不分配输出
// 入参: op 操作符, values 操作数
// 返回: error 数值编码或程序长度错误
func (s *type2State) emit(op int, values []float64) error {
	if s.output == nil {
		return nil
	}
	for _, value := range values {
		if err := encodeType2Number(s.output, value); err != nil {
			return err
		}
	}
	if op >= 1200 {
		s.output.WriteByte(12)
		s.output.WriteByte(byte(op - 1200))
	} else {
		s.output.WriteByte(byte(op))
	}
	if s.output.Len() > 65535 {
		return fmt.Errorf("expanded Type2 charstring exceeds 65535 bytes")
	}
	return nil
}

// takeWidth 在首次清栈指令前提取可选字宽，组件不写入自身字宽
// 入参: explicit 是否含字宽
// 返回: error 字宽编码错误
func (s *type2State) takeWidth(explicit bool) error {
	if s.widthSet {
		return nil
	}
	s.widthSet = true
	if explicit {
		s.width = s.font.nominal + s.args[0]
		if !finite(s.width) {
			return fmt.Errorf("invalid Type2 width")
		}
		if s.output != nil && !s.component && s.widthTarget == nil {
			if err := encodeType2Number(s.output, s.args[0]); err != nil {
				return err
			}
		}
		copy(s.args, s.args[1:])
		s.args = s.args[:len(s.args)-1]
	}
	if s.output != nil && !s.component && s.widthTarget != nil && s.width != s.widthTarget.def {
		return encodeType2Number(s.output, s.width-s.widthTarget.nominal)
	}
	return nil
}

// run 执行Type2程序，操作数和暂存数组在子程序间共享
// 入参: data 字形或子程序, depth 调用深度
// 返回: bool 是否结束字形, error 指令错误
func (s *type2State) run(data []byte, depth int) (bool, error) {
	if depth > 10 || len(data) > 65535 {
		return false, fmt.Errorf("Type2 implementation limit exceeded")
	}
	for index := 0; index < len(data); {
		(*s.steps)++
		if *s.steps > 1<<20 {
			return false, fmt.Errorf("Type2 execution limit exceeded")
		}
		op := int(data[index])
		start := index
		index++
		if op == 28 || op >= 32 {
			length := 1
			if op == 28 {
				length = 3
			} else if op == 255 {
				length = 5
			} else if op >= 247 {
				length = 2
			}
			if length > len(data)-start {
				return false, fmt.Errorf("truncated Type2 number")
			}
			index = start + length
			if err := s.push(parseNumberType2(data, start)); err != nil {
				return false, err
			}
			continue
		}
		if op == 12 {
			if index == len(data) {
				return false, fmt.Errorf("truncated Type2 operator")
			}
			op = 1200 + int(data[index])
			index++
		}
		switch op {
		case 10, 29:
			if len(s.args) == 0 {
				return false, fmt.Errorf("missing Type2 subroutine number")
			}
			value := s.args[len(s.args)-1]
			s.args = s.args[:len(s.args)-1]
			subrs := s.font.locals
			if op == 29 {
				subrs = s.font.globals
			}
			value += float64(cffSubrBias(len(subrs)))
			if value < 0 || value >= float64(len(subrs)) || value != math.Trunc(value) {
				return false, fmt.Errorf("invalid Type2 subroutine number")
			}
			ended, err := s.run(subrs[int(value)], depth+1)
			if ended || err != nil {
				return ended, err
			}
		case 11:
			if depth == 0 {
				return false, fmt.Errorf("Type2 return outside subroutine")
			}
			return false, nil
		case 1, 3, 18, 23, 19, 20:
			if err := s.takeWidth(len(s.args)%2 == 1); err != nil {
				return false, err
			}
			if s.widthOnly {
				return true, nil
			}
			if len(s.args)%2 != 0 || s.hints+len(s.args)/2 > 96 {
				return false, fmt.Errorf("invalid Type2 stem hints")
			}
			s.hints += len(s.args) / 2
			if op == 19 || op == 20 {
				if s.hints == 0 {
					return false, fmt.Errorf("Type2 mask has no stem hints")
				}
				length := (s.hints + 7) / 8
				if length > len(data)-index {
					return false, fmt.Errorf("truncated Type2 hint mask")
				}
				if !s.component && !s.stripHints && len(s.args) > 0 {
					if err := s.emit(23, s.args); err != nil {
						return false, err
					}
					if op == 20 {
						s.changed = true
					}
				}
				if !s.component && !s.stripHints {
					if err := s.emit(op, nil); err != nil {
						return false, err
					}
					if s.output != nil {
						s.output.Write(data[index : index+length])
					}
				}
				index += length
			} else if !s.component && !s.stripHints {
				if err := s.emit(op, s.args); err != nil {
					return false, err
				}
			}
			s.args = s.args[:0]
		case 4, 21, 22:
			count := 1
			if op == 21 {
				count = 2
			}
			if err := s.takeWidth(len(s.args) == count+1); err != nil {
				return false, err
			}
			if len(s.args) != count {
				return false, fmt.Errorf("invalid Type2 moveto operands")
			}
			if s.widthOnly {
				return true, nil
			}
			if op != 4 {
				s.x += s.args[0]
			}
			if op != 22 {
				s.y += s.args[count-1]
			}
			if !finite(s.x) || !finite(s.y) {
				return false, fmt.Errorf("invalid Type2 current point")
			}
			if s.component {
				if err := s.emit(21, []float64{s.x + s.originX - s.outX, s.y + s.originY - s.outY}); err != nil {
					return false, err
				}
			} else if err := s.emit(op, s.args); err != nil {
				return false, err
			}
			s.outX, s.outY = s.x+s.originX, s.y+s.originY
			s.pathStarted = true
			s.args = s.args[:0]
		case 14:
			if err := s.takeWidth(len(s.args) == 1 || len(s.args) == 5); err != nil {
				return false, err
			}
			if s.widthOnly {
				return true, nil
			}
			if len(s.args) == 4 {
				if s.component {
					return false, fmt.Errorf("nested Type2 composite glyph")
				}
				if err := s.appendComponent(s.args[2], 0, 0); err != nil {
					return false, err
				}
				if err := s.appendComponent(s.args[3], s.args[0], s.args[1]); err != nil {
					return false, err
				}
				s.changed = true
			} else if len(s.args) != 0 {
				return false, fmt.Errorf("invalid Type2 endchar operands")
			}
			if !s.component {
				if err := s.emit(14, nil); err != nil {
					return false, err
				}
			}
			return true, nil
		case 1200:
			s.changed = true
		case 5, 6, 7, 8, 24, 25, 26, 27, 30, 31, 1234, 1235, 1236, 1237:
			if !s.pathStarted {
				return false, fmt.Errorf("Type2 path has no moveto")
			}
			if err := s.draw(op); err != nil {
				return false, err
			}
		default:
			if err := s.calculate(op); err != nil {
				return false, err
			}
			s.changed = true
		}
	}
	return false, fmt.Errorf("Type2 program has no terminator")
}

// appendComponent 按StandardEncoding查找组合组件，保持源字宽与独立暂存状态
// 入参: code 标准字符码, x 横向偏移, y 纵向偏移
// 返回: error 组件或指令错误
func (s *type2State) appendComponent(code, x, y float64) error {
	if code < 0 || code > 255 || code != math.Trunc(code) || type1StandardNames[int(code)] == "" {
		return fmt.Errorf("invalid Type2 component code")
	}
	if s.font.names == nil {
		sids, _, _, index, ok := getCFFCharsetInfo(s.font.data, len(s.font.chars))
		if !ok {
			return fmt.Errorf("invalid Type2 composite charset")
		}
		s.font.names = make(map[string]int, len(sids))
		for gid, sid := range sids {
			s.font.names[getCFFSIDString(s.font.data, index, sid)] = gid
		}
	}
	name := type1StandardNames[int(code)]
	gid, ok := s.font.names[name]
	if !ok {
		return fmt.Errorf("missing Type2 component %s", name)
	}
	child := type2State{font: s.font, width: s.font.def, component: true, output: s.output, originX: x, originY: y, outX: s.outX, outY: s.outY, seed: s.font.seed + uint64(gid), steps: s.steps}
	_, err := child.run(s.font.chars[gid], 0)
	if err != nil {
		return fmt.Errorf("invalid Type2 component %s: %w", name, err)
	}
	s.outX, s.outY = child.outX, child.outY
	return nil
}
