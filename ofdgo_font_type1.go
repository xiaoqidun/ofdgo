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
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
)

// type1Program 保存解密后的Type1字形与子程序
type type1Program struct {
	glyphs []type1Glyph
	subrs  [][]byte
	lenIV  int
}

// type1Glyph 保存字形名称及指令
type type1Glyph struct {
	name string
	data []byte
}

// type1Scanner 按词项和显式长度读取Type1程序
type type1Scanner struct {
	data []byte
	pos  int
}

// parseType1Program 读取Type1字体的字形程序及子程序
// 入参: data PFA或PFB字体数据
// 返回: type1Program 字形和子程序, error 错误信息
func parseType1Program(data []byte) (type1Program, error) {
	var err error
	data, err = flattenType1(data)
	if err != nil {
		return type1Program{}, err
	}
	marker := []byte("eexec")
	start := bytes.Index(data, marker)
	if start < 0 {
		return type1Program{}, fmt.Errorf("Type1 font lacks eexec data")
	}
	start += len(marker)
	for start < len(data) && type1Space(data[start]) {
		start++
	}
	if start == len(data) {
		return type1Program{}, fmt.Errorf("empty Type1 eexec data")
	}
	encrypted := data[start:]
	if type1HexEncoded(encrypted) {
		end := bytes.Index(encrypted, []byte("cleartomark"))
		if end >= 0 {
			encrypted = encrypted[:end]
		}
		digits := make([]byte, 0, len(encrypted))
		for _, c := range encrypted {
			if type1Space(c) {
				continue
			}
			if !type1HexDigit(c) {
				break
			}
			digits = append(digits, c)
		}
		encrypted = make([]byte, hex.DecodedLen(len(digits)))
		if _, err := hex.Decode(encrypted, digits); err != nil {
			return type1Program{}, fmt.Errorf("invalid Type1 eexec encoding: %w", err)
		}
	}
	if len(encrypted) < 4 {
		return type1Program{}, fmt.Errorf("invalid Type1 eexec data")
	}
	decrypted := type1Decrypt(encrypted, 55665)[4:]
	scanner := type1Scanner{data: decrypted}
	program := type1Program{lenIV: 4}
	section := ""
	for {
		token := scanner.next()
		if token == "" || token == "closefile" {
			break
		}
		switch token {
		case "/lenIV":
			value, err := strconv.Atoi(scanner.next())
			if err != nil || value < -1 {
				return type1Program{}, fmt.Errorf("invalid Type1 lenIV")
			}
			program.lenIV = value
		case "/Subrs":
			section = "subrs"
		case "/CharStrings":
			section = "glyphs"
		case "dup":
			if section != "subrs" {
				continue
			}
			index, err := strconv.Atoi(scanner.next())
			if err != nil || index < 0 || index > len(decrypted) {
				return type1Program{}, fmt.Errorf("invalid Type1 subroutine index")
			}
			length, err := strconv.Atoi(scanner.next())
			if err != nil {
				return type1Program{}, fmt.Errorf("invalid Type1 subroutine length")
			}
			if !type1BinaryStart(scanner.next()) {
				return type1Program{}, fmt.Errorf("missing Type1 subroutine data")
			}
			raw, err := scanner.binary(length)
			if err != nil {
				return type1Program{}, err
			}
			for len(program.subrs) <= index {
				program.subrs = append(program.subrs, nil)
			}
			program.subrs[index] = raw
		default:
			if section != "glyphs" || len(token) < 2 || token[0] != '/' {
				continue
			}
			name := token[1:]
			next := scanner.next()
			length, err := strconv.Atoi(next)
			if err != nil {
				continue
			}
			if !type1BinaryStart(scanner.next()) {
				return type1Program{}, fmt.Errorf("missing Type1 glyph data")
			}
			raw, err := scanner.binary(length)
			if err != nil {
				return type1Program{}, err
			}
			program.glyphs = append(program.glyphs, type1Glyph{name: name, data: raw})
		}
	}
	if len(program.glyphs) == 0 {
		return type1Program{}, fmt.Errorf("Type1 font has no glyphs")
	}
	for index := range program.subrs {
		program.subrs[index], err = type1CharString(program.subrs[index], program.lenIV)
		if err != nil {
			return type1Program{}, err
		}
	}
	for index := range program.glyphs {
		program.glyphs[index].data, err = type1CharString(program.glyphs[index].data, program.lenIV)
		if err != nil {
			return type1Program{}, fmt.Errorf("Type1 glyph %s: %w", program.glyphs[index].name, err)
		}
	}
	return program, nil
}

// flattenType1 将PFB分段容器转换为连续的Type1程序
// 入参: data 字体数据
// 返回: []byte 连续程序, error 错误信息
func flattenType1(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x80 {
		return data, nil
	}
	var program []byte
	for len(data) >= 2 && data[0] == 0x80 {
		if data[1] == 3 {
			return program, nil
		}
		if len(data) < 6 || data[1] != 1 && data[1] != 2 {
			return nil, fmt.Errorf("invalid PFB segment")
		}
		length := binary.LittleEndian.Uint32(data[2:6])
		if uint64(length) > uint64(len(data)-6) {
			return nil, fmt.Errorf("truncated PFB segment")
		}
		program = append(program, data[6:6+length]...)
		data = data[6+length:]
	}
	return nil, fmt.Errorf("missing PFB end marker")
}

// type1Decrypt 解密Type1的eexec或字形程序
// 入参: data 密文, seed 初始密钥
// 返回: []byte 明文
func type1Decrypt(data []byte, seed uint16) []byte {
	out := make([]byte, len(data))
	for index, value := range data {
		out[index] = value ^ byte(seed>>8)
		seed = uint16((uint32(value)+uint32(seed))*52845 + 22719)
	}
	return out
}

// type1CharString 解密字形程序并移除lenIV随机前缀
// 入参: data 字形密文, lenIV 前缀长度
// 返回: []byte 指令数据, error 错误信息
func type1CharString(data []byte, lenIV int) ([]byte, error) {
	if lenIV < 0 {
		return data, nil
	}
	if len(data) < lenIV {
		return nil, fmt.Errorf("truncated Type1 charstring")
	}
	return type1Decrypt(data, 4330)[lenIV:], nil
}

// type1HexEncoded 区分eexec十六进制与二进制封装
// 入参: data eexec数据
// 返回: bool 是否为十六进制
func type1HexEncoded(data []byte) bool {
	count := 0
	for _, value := range data {
		if type1Space(value) {
			continue
		}
		if !type1HexDigit(value) {
			return false
		}
		count++
		if count == 8 {
			return true
		}
	}
	return false
}

// type1HexDigit 判断字节是否为十六进制数字
func type1HexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

// type1Space 判断PostScript空白字节
func type1Space(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f' || value == 0
}

// next 读取下一个PostScript词项，字形二进制块由binary处理
// 返回: string 词项
func (s *type1Scanner) next() string {
	for s.pos < len(s.data) {
		if type1Space(s.data[s.pos]) {
			s.pos++
			continue
		}
		if s.data[s.pos] == '%' {
			for s.pos < len(s.data) && s.data[s.pos] != '\n' && s.data[s.pos] != '\r' {
				s.pos++
			}
			continue
		}
		break
	}
	if s.pos == len(s.data) {
		return ""
	}
	start := s.pos
	if bytes.IndexByte([]byte("[]{}"), s.data[s.pos]) >= 0 {
		s.pos++
		return string(s.data[start:s.pos])
	}
	for s.pos < len(s.data) && !type1Space(s.data[s.pos]) && bytes.IndexByte([]byte("[]{}%"), s.data[s.pos]) < 0 {
		s.pos++
	}
	return string(s.data[start:s.pos])
}

// binary 按PostScript长度读取RD后的加密字节
// 入参: length 数据长度
// 返回: []byte 二进制数据, error 错误信息
func (s *type1Scanner) binary(length int) ([]byte, error) {
	if s.pos >= len(s.data) || !type1Space(s.data[s.pos]) || length < 0 {
		return nil, fmt.Errorf("invalid Type1 binary data")
	}
	if s.data[s.pos] == '\r' && s.pos+1 < len(s.data) && s.data[s.pos+1] == '\n' {
		s.pos += 2
	} else {
		s.pos++
	}
	if length > len(s.data)-s.pos {
		return nil, fmt.Errorf("truncated Type1 binary data")
	}
	data := s.data[s.pos : s.pos+length]
	s.pos += length
	return data, nil
}

// type1BinaryStart 判断Type1二进制数据起始操作符
func type1BinaryStart(token string) bool {
	return token == "RD" || token == "-|"
}

// type1Outline 保存字形指令的操作数栈及轮廓位置
type type1Outline struct {
	program    *type1Program
	args       []float64
	postscript []float64
	flexPoints [][2]float64
	width      float64
	x          float64
	y          float64
	pointX     float64
	pointY     float64
	open       bool
	defined    bool
	flex       bool
	path       bytes.Buffer
}

// outline 将Type1字形指令转换为保留三次曲线的Type2程序
// 入参: glyph 字形数据
// 返回: []byte Type2字形程序, error 错误信息
func (p *type1Program) outline(glyph type1Glyph) ([]byte, error) {
	state := type1Outline{program: p}
	ended, err := state.run(glyph.data, 0)
	if err != nil {
		return nil, fmt.Errorf("Type1 glyph %s: %w", glyph.name, err)
	}
	if !ended || !state.defined {
		return nil, fmt.Errorf("Type1 glyph %s has no complete outline", glyph.name)
	}
	var result bytes.Buffer
	if err := encodeType2Number(&result, state.width); err != nil {
		return nil, err
	}
	result.Write(state.path.Bytes())
	result.WriteByte(14)
	return result.Bytes(), nil
}

// run 执行Type1字形或子程序，遇到endchar返回完成状态
// 入参: data 指令数据, depth 子程序深度
// 返回: bool 是否遇到endchar, error 错误信息
func (s *type1Outline) run(data []byte, depth int) (bool, error) {
	if depth > 32 {
		return false, fmt.Errorf("Type1 subroutine recursion")
	}
	for index := 0; index < len(data); index++ {
		op := int(data[index])
		if op >= 32 {
			var value float64
			switch {
			case op <= 246:
				value = float64(op - 139)
			case op <= 250:
				if index+1 >= len(data) {
					return false, fmt.Errorf("truncated Type1 operand")
				}
				index++
				value = float64((op-247)*256 + int(data[index]) + 108)
			case op <= 254:
				if index+1 >= len(data) {
					return false, fmt.Errorf("truncated Type1 operand")
				}
				index++
				value = float64(-(op-251)*256 - int(data[index]) - 108)
			default:
				if index+4 >= len(data) {
					return false, fmt.Errorf("truncated Type1 operand")
				}
				value = float64(int32(binary.BigEndian.Uint32(data[index+1 : index+5])))
				index += 4
			}
			s.args = append(s.args, value)
			continue
		}
		if op == 12 {
			if index+1 >= len(data) {
				return false, fmt.Errorf("truncated Type1 operator")
			}
			index++
			op = 1200 + int(data[index])
		}
		if op == 10 {
			if len(s.args) == 0 {
				return false, fmt.Errorf("Type1 callsubr has no index")
			}
			value := s.args[len(s.args)-1]
			index := int(value)
			s.args = s.args[:len(s.args)-1]
			if value != float64(index) || index < 0 || index >= len(s.program.subrs) || s.program.subrs[index] == nil {
				return false, fmt.Errorf("invalid Type1 subroutine index %d", index)
			}
			ended, err := s.run(s.program.subrs[index], depth+1)
			if err != nil || ended {
				return ended, err
			}
			continue
		}
		if op == 11 {
			if depth == 0 {
				return false, fmt.Errorf("Type1 return outside subroutine")
			}
			return false, nil
		}
		if op == 14 {
			return true, nil
		}
		if err := s.operator(op); err != nil {
			return false, err
		}
	}
	return false, nil
}

// operator 执行单个Type1绘制指令并清空已消费的操作数
// 入参: op 指令编号
// 返回: error 错误信息
func (s *type1Outline) operator(op int) error {
	a := s.args
	s.args = a[:0]
	switch op {
	case 1200:
		s.args = a
		return nil
	case 1, 3, 1201, 1202:
		return nil
	case 13:
		if s.defined || len(a) != 2 {
			return fmt.Errorf("invalid Type1 hsbw")
		}
		s.x, s.width, s.defined = a[0], a[1], true
	case 1207:
		if s.defined || len(a) != 4 || a[3] != 0 {
			return fmt.Errorf("invalid Type1 sbw")
		}
		s.x, s.y, s.width, s.defined = a[0], a[1], a[2], true
	case 4, 21, 22:
		if !s.defined || op == 4 && len(a) != 1 || op == 21 && len(a) != 2 || op == 22 && len(a) != 1 {
			return fmt.Errorf("invalid Type1 move")
		}
		if s.flex {
			point := [2]float64{}
			if op == 4 || op == 21 {
				point[1] = a[len(a)-1]
			}
			if op == 21 || op == 22 {
				point[0] = a[0]
			}
			s.flexPoints = append(s.flexPoints, point)
			return nil
		}
		if op == 4 || op == 21 {
			s.y += a[len(a)-1]
		}
		if op == 21 || op == 22 {
			s.x += a[0]
		}
		return s.move()
	case 5, 6, 7:
		if !s.defined || op == 5 && (len(a) == 0 || len(a)%2 != 0) || op != 5 && len(a) == 0 {
			return fmt.Errorf("invalid Type1 line")
		}
		if err := s.ensureMove(); err != nil {
			return err
		}
		for n := 0; n < len(a); {
			var dx, dy float64
			switch op {
			case 5:
				dx, dy = a[n], a[n+1]
				n += 2
			case 6:
				dx = a[n]
				n++
			case 7:
				dy = a[n]
				n++
			}
			if err := s.line(dx, dy); err != nil {
				return err
			}
		}
	case 8, 30, 31:
		count := 6
		if op != 8 {
			count = 4
		}
		if !s.defined || len(a) == 0 || len(a)%count != 0 {
			return fmt.Errorf("invalid Type1 curve")
		}
		if err := s.ensureMove(); err != nil {
			return err
		}
		for n := 0; n < len(a); n += count {
			var curve [6]float64
			switch op {
			case 8:
				copy(curve[:], a[n:n+6])
			case 30:
				curve = [6]float64{0, a[n], a[n+1], a[n+2], a[n+3], 0}
			case 31:
				curve = [6]float64{a[n], 0, a[n+1], a[n+2], 0, a[n+3]}
			}
			if err := s.curve(curve); err != nil {
				return err
			}
		}
	case 9:
		s.open = false
	case 1212:
		if len(a) < 2 || a[len(a)-1] == 0 {
			return fmt.Errorf("invalid Type1 division")
		}
		a[len(a)-2] /= a[len(a)-1]
		s.args = a[:len(a)-1]
	case 1216:
		if len(a) < 2 {
			return fmt.Errorf("invalid Type1 callothersubr")
		}
		count, number := int(a[len(a)-2]), int(a[len(a)-1])
		if float64(count) != a[len(a)-2] || float64(number) != a[len(a)-1] || count < 0 || count > len(a)-2 {
			return fmt.Errorf("invalid Type1 callothersubr arguments")
		}
		s.args = a[:len(a)-count-2]
		a = a[len(a)-count-2 : len(a)-2]
		switch number {
		case 0:
			if !s.flex || count != 3 || len(s.flexPoints) != 7 {
				return fmt.Errorf("invalid Type1 Flex end")
			}
			if err := s.ensureMove(); err != nil {
				return err
			}
			first := s.flexPoints
			if err := s.curve([6]float64{first[0][0] + first[1][0], first[0][1] + first[1][1], first[2][0], first[2][1], first[3][0], first[3][1]}); err != nil {
				return err
			}
			if err := s.curve([6]float64{first[4][0], first[4][1], first[5][0], first[5][1], first[6][0], first[6][1]}); err != nil {
				return err
			}
			if s.x != a[1] || s.y != a[2] {
				return fmt.Errorf("Type1 Flex endpoint differs from outline")
			}
			s.postscript = []float64{s.x, s.y}
			s.flexPoints = nil
			s.flex = false
		case 1:
			if count != 0 || s.flex {
				return fmt.Errorf("invalid Type1 Flex start")
			}
			s.flex = true
			s.flexPoints = nil
		case 2:
			if count != 0 || !s.flex {
				return fmt.Errorf("invalid Type1 Flex point")
			}
		case 3:
			if count != 1 {
				return fmt.Errorf("invalid Type1 hint replacement")
			}
			s.postscript = []float64{a[0]}
		default:
			return fmt.Errorf("unsupported Type1 OtherSubr %d", number)
		}
	case 1217:
		if len(s.postscript) == 0 {
			return fmt.Errorf("invalid Type1 pop")
		}
		s.args = append(a, s.postscript[0])
		s.postscript = s.postscript[1:]
	case 1233:
		if len(a) != 2 {
			return fmt.Errorf("invalid Type1 setcurrentpoint")
		}
		s.x, s.y = a[0], a[1]
	case 1206:
		return fmt.Errorf("unsupported Type1 seac")
	default:
		return fmt.Errorf("unsupported Type1 operator %d", op)
	}
	return nil
}

// move 在Type2字形程序中建立新轮廓
// 返回: error 错误信息
func (s *type1Outline) move() error {
	if err := encodeType2Number(&s.path, s.x-s.pointX); err != nil {
		return err
	}
	if err := encodeType2Number(&s.path, s.y-s.pointY); err != nil {
		return err
	}
	s.path.WriteByte(21)
	s.pointX, s.pointY, s.open = s.x, s.y, true
	return nil
}

// ensureMove 在首次描绘或关闭轮廓后建立当前位置
// 返回: error 错误信息
func (s *type1Outline) ensureMove() error {
	if !s.open || s.x != s.pointX || s.y != s.pointY {
		return s.move()
	}
	return nil
}

// line 保留Type1线段的实际终点
// 入参: dx 横向增量, dy 纵向增量
// 返回: error 错误信息
func (s *type1Outline) line(dx, dy float64) error {
	if err := encodeType2Number(&s.path, dx); err != nil {
		return err
	}
	if err := encodeType2Number(&s.path, dy); err != nil {
		return err
	}
	s.path.WriteByte(5)
	s.x += dx
	s.y += dy
	s.pointX, s.pointY = s.x, s.y
	return nil
}

// curve 保留Type1三次贝塞尔控制点
// 入参: values 六个相对坐标
// 返回: error 错误信息
func (s *type1Outline) curve(values [6]float64) error {
	for _, value := range values {
		if err := encodeType2Number(&s.path, value); err != nil {
			return err
		}
	}
	s.path.WriteByte(8)
	s.x += values[0] + values[2] + values[4]
	s.y += values[1] + values[3] + values[5]
	s.pointX, s.pointY = s.x, s.y
	return nil
}

// encodeType2Number 编码Type2字形指令的有符号定点操作数
// 入参: target 目标字节流, value 操作数
// 返回: error 错误信息
func encodeType2Number(target *bytes.Buffer, value float64) error {
	if !finite(value) || value < -32768 || value >= 32768 {
		return fmt.Errorf("Type2 operand out of range")
	}
	if value == math.Trunc(value) {
		integer := int(value)
		switch {
		case integer >= -107 && integer <= 107:
			target.WriteByte(byte(integer + 139))
		case integer >= 108 && integer <= 1131:
			integer -= 108
			target.WriteByte(byte(247 + integer/256))
			target.WriteByte(byte(integer % 256))
		case integer <= -108 && integer >= -1131:
			integer = -integer - 108
			target.WriteByte(byte(251 + integer/256))
			target.WriteByte(byte(integer % 256))
		default:
			target.WriteByte(28)
			_ = binary.Write(target, binary.BigEndian, int16(integer))
		}
		return nil
	}
	target.WriteByte(255)
	_ = binary.Write(target, binary.BigEndian, int32(math.Round(value*65536)))
	return nil
}

// toCFF 将Type1字形转换为保持三次轮廓的CFF字体
// 入参: name 字体名称
// 返回: []byte CFF数据, error 错误信息
func (p *type1Program) toCFF(name string) ([]byte, error) {
	if len(p.glyphs) > math.MaxUint16 || name == "" {
		return nil, fmt.Errorf("invalid Type1 font identity")
	}
	ordered := make([]type1Glyph, 0, len(p.glyphs))
	for _, glyph := range p.glyphs {
		if glyph.name == ".notdef" {
			ordered = append(ordered, glyph)
			break
		}
	}
	if len(ordered) == 0 {
		return nil, fmt.Errorf("Type1 font lacks .notdef glyph")
	}
	for _, glyph := range p.glyphs {
		if glyph.name != ".notdef" {
			ordered = append(ordered, glyph)
		}
	}
	chars := make([][]byte, len(ordered))
	strings := make([][]byte, 0, len(ordered)-1)
	charset := make([]byte, 1+2*(len(ordered)-1))
	for index, glyph := range ordered {
		charstring, err := p.outline(glyph)
		if err != nil {
			return nil, err
		}
		chars[index] = charstring
		if index == 0 {
			continue
		}
		strings = append(strings, []byte(glyph.name))
		sid := 391 + index - 1
		if sid > math.MaxUint16 {
			return nil, fmt.Errorf("Type1 font has too many glyph names")
		}
		binary.BigEndian.PutUint16(charset[1+(index-1)*2:], uint16(sid))
	}
	nameIndex := encodeCFFIndex([][]byte{[]byte(name)})
	stringIndex := encodeCFFIndex(strings)
	globalSubrs := encodeCFFIndex(nil)
	charIndex := encodeCFFIndex(chars)
	topDict := make([]byte, 12)
	topDict[0], topDict[5], topDict[6], topDict[11] = 29, 15, 29, 17
	topIndex := encodeCFFIndex([][]byte{topDict})
	charsetOffset := 4 + len(nameIndex) + len(topIndex) + len(stringIndex) + len(globalSubrs)
	charOffset := charsetOffset + len(charset)
	binary.BigEndian.PutUint32(topDict[1:5], uint32(charsetOffset))
	binary.BigEndian.PutUint32(topDict[7:11], uint32(charOffset))
	topIndex = encodeCFFIndex([][]byte{topDict})
	data := make([]byte, 0, charOffset+len(charIndex))
	data = append(data, 1, 0, 4, 4)
	data = append(data, nameIndex...)
	data = append(data, topIndex...)
	data = append(data, stringIndex...)
	data = append(data, globalSubrs...)
	data = append(data, charset...)
	data = append(data, charIndex...)
	return data, nil
}

// glyphIDs 返回Type1名称到转换后字形编号的对应关系
// 返回: map[string]uint16 字形编号
func (p *type1Program) glyphIDs() map[string]uint16 {
	ids := map[string]uint16{".notdef": 0}
	next := uint16(1)
	for _, glyph := range p.glyphs {
		if glyph.name != ".notdef" {
			ids[glyph.name] = next
			next++
		}
	}
	return ids
}
