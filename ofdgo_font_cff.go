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
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// cffDict 使用float64存储所有数值，以统一处理整数和实数
type cffDict map[int][]float64

// wrapCFFToOTF 将CFF裸数据包装为OpenType字体格式
// 入参: cffData CFF字体数据
// 返回: []byte OTF字体数据, map[rune]uint16 字符映射, error 错误信息
func wrapCFFToOTF(cffData []byte) ([]byte, map[rune]uint16, error) {
	numGlyphs, err := parseCFFAndCountGlyphs(cffData)
	if err != nil {
		return nil, nil, err
	}
	mapping := getCmapFromCFF(cffData, int(numGlyphs))
	sanitized, err := sanitizeCFF(cffData)
	if err != nil {
		return nil, nil, err
	}
	cffData = sanitized
	cffData, err = normalizeCFFCharstrings(cffData)
	if err != nil {
		return nil, nil, err
	}
	widths, err := parseCFFWidths(cffData, numGlyphs)
	if err != nil {
		return nil, nil, fmt.Errorf("CFF glyph widths: %w", err)
	}
	var unitsPerEm uint16 = 1000
	if len(cffData) > 4 {
		hdrSize := int(cffData[2])
		off := hdrSize
		_, sz := getCFFIndexCount(cffData, off)
		off += sz
		topDictData, _ := getCFFIndexData(cffData, off)
		if topDictData != nil {
			td := parseCFFDict(topDictData)
			if mat, ok := td[1207]; ok && len(mat) > 0 {
				if mat[0] != 0 {
					val := 1.0 / mat[0]
					if val > 0 {
						unitsPerEm = uint16(math.Round(val))
					}
				}
			}
		}
	}
	tables := make(map[string][]byte)
	tables["CFF "] = cffData
	tables["head"] = buildHeadTable(unitsPerEm)
	tables["hhea"] = buildHheaTable(uint16(numGlyphs))
	tables["maxp"] = buildCFFMaxpTable(uint16(numGlyphs))
	tables["OS/2"] = buildOS2Table()
	tables["name"] = buildNameTable()
	tables["post"] = buildPostTable()
	tables["hmtx"] = buildHmtxTable(widths)
	tables["cmap"] = buildCmapTable(uint16(numGlyphs), mapping)
	data, err := serializeOTF(tables)
	return data, mapping, err
}

// normalizeCFFCharstrings 展开预定义字符集并规范化Type2提示编码，保留轮廓和字宽
// 入参: data CFF字体数据
// 返回: []byte 标准化后的CFF数据, error 错误信息
func normalizeCFFCharstrings(data []byte) ([]byte, error) {
	if len(data) < 4 || int(data[2]) >= len(data) {
		return nil, fmt.Errorf("invalid CFF header")
	}
	headerEnd := int(data[2])
	_, nameSize := getCFFIndexCount(data, headerEnd)
	topStart := headerEnd + nameSize
	topData, topSize := getCFFIndexData(data, topStart)
	if topData == nil || topStart+topSize > len(data) {
		return nil, fmt.Errorf("invalid CFF top dictionary")
	}
	dict := parseCFFDict(topData)
	charOffset := dict[17]
	if len(charOffset) != 1 {
		return nil, fmt.Errorf("missing CFF charstrings")
	}
	charStart := int(charOffset[0])
	_, charSize := getCFFIndexCount(data, charStart)
	if charStart < topStart+topSize || charSize == 0 || charStart+charSize > len(data) {
		return nil, fmt.Errorf("invalid CFF charstrings")
	}
	chars, changed, err := normalizeType2Programs(data)
	if err != nil {
		return nil, err
	}
	var explicitCharset []byte
	if charset := dict[15]; len(charset) == 1 && (charset[0] == 1 || charset[0] == 2) {
		sids, _, _, _, ok := getCFFCharsetInfo(data, len(chars))
		if !ok {
			return nil, fmt.Errorf("invalid CFF charset")
		}
		explicitCharset = make([]byte, 1+2*(len(sids)-1))
		for gid := 1; gid < len(sids); gid++ {
			binary.BigEndian.PutUint16(explicitCharset[1+2*(gid-1):], uint16(sids[gid]))
		}
		changed = true
	}
	if !changed {
		return data, nil
	}
	encodedChars := encodeCFFIndex(chars)
	topEnd := topStart + topSize
	originalDict := parseCFFDict(topData)
	newTop := encodeCFFIndex([][]byte{encodeCFFDict(dict)})
	for range 6 {
		shift := len(newTop) - topSize
		shiftTail := shift + len(encodedChars) - charSize
		for _, op := range []int{15, 16, 17, 1236, 1237} {
			if op == 15 && explicitCharset != nil {
				dict[op] = []float64{float64(len(data) + shiftTail)}
				continue
			}
			values := dict[op]
			if len(values) != 1 || op == 15 && values[0] <= 2 || op == 16 && values[0] <= 1 {
				continue
			}
			original := originalDict[op]
			offset := int(original[0])
			if offset >= charStart+charSize {
				dict[op] = []float64{float64(offset + shiftTail)}
			} else if offset >= topEnd {
				dict[op] = []float64{float64(offset + shift)}
			}
		}
		if values := dict[18]; len(values) == 2 {
			original := originalDict[18]
			offset := int(original[1])
			if offset >= charStart+charSize {
				dict[18] = []float64{values[0], float64(offset + shiftTail)}
			} else if offset >= topEnd {
				dict[18] = []float64{values[0], float64(offset + shift)}
			}
		}
		updated := encodeCFFIndex([][]byte{encodeCFFDict(dict)})
		if len(updated) == len(newTop) {
			newTop = updated
			break
		}
		newTop = updated
	}
	result := make([]byte, 0, len(data)+len(newTop)-topSize+len(encodedChars)-charSize+len(explicitCharset))
	result = append(result, data[:topStart]...)
	result = append(result, newTop...)
	result = append(result, data[topEnd:charStart]...)
	result = append(result, encodedChars...)
	result = append(result, data[charStart+charSize:]...)
	result = append(result, explicitCharset...)
	return result, nil
}

// sanitizeCFF 尝试清洗CFF数据，转换CID字体并合并FontMatrix
// 入参: data 原始CFF数据
// 返回: []byte 清洗后的CFF数据, error 错误信息
func sanitizeCFF(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("data too short")
	}
	hdrSize := int(data[2])
	offset := hdrSize
	if offset >= len(data) {
		return nil, fmt.Errorf("truncated")
	}
	nameCount, nameSz := getCFFIndexCount(data, offset)
	if nameCount != 1 {
		return nil, fmt.Errorf("multi-font cff not supported")
	}
	nameIndexData := data[offset : offset+nameSz]
	offset += nameSz
	if offset >= len(data) {
		return nil, fmt.Errorf("truncated")
	}
	topCount, topSz := getCFFIndexCount(data, offset)
	if topCount != 1 {
		return nil, fmt.Errorf("top dict count != 1")
	}
	topDictData, _ := getCFFIndexData(data, offset)
	offset += topSz
	if offset >= len(data) {
		return nil, fmt.Errorf("truncated")
	}
	_, strSz := getCFFIndexCount(data, offset)
	stringIndexData := data[offset : offset+strSz]
	offset += strSz
	if offset >= len(data) {
		return nil, fmt.Errorf("truncated")
	}
	_, glbSz := getCFFIndexCount(data, offset)
	globalSubrIndexData := data[offset : offset+glbSz]
	topDict := parseCFFDict(topDictData)
	if _, isCID := topDict[1230]; !isCID {
		return data, nil
	}
	fdArrOffs, ok := topDict[1236]
	if !ok || len(fdArrOffs) == 0 {
		return nil, fmt.Errorf("cid without fdarray")
	}
	fdArrOff := int(fdArrOffs[0])
	if fdArrOff >= len(data) {
		return nil, fmt.Errorf("fdarray offset oob")
	}
	fdCount, _ := getCFFIndexCount(data, fdArrOff)
	if fdCount != 1 {
		return sanitizeMultiFDCFF(data, hdrSize, nameIndexData, topDict, stringIndexData, globalSubrIndexData, fdArrOff, fdCount)
	}
	fontDictData, _ := getCFFIndexData(data, fdArrOff)
	fontDict := parseCFFDict(fontDictData)
	if fdMat, ok := fontDict[1207]; ok && len(fdMat) == 6 {
		topMat, hasTop := topDict[1207]
		if !hasTop || len(topMat) != 6 {
			topMat = []float64{0.001, 0, 0, 0.001, 0, 0}
		}
		newMat := multiplyAffine(topMat, fdMat)
		topDict[1207] = newMat
	}
	privVals, ok := fontDict[18]
	if !ok || len(privVals) != 2 {
		privVals = []float64{0, 0}
	}
	privSize := int(privVals[0])
	privOff := int(privVals[1])
	var localSubrData []byte
	var privDictData []byte
	if privSize > 0 && privOff < len(data) && privOff+privSize <= len(data) {
		privDictData = data[privOff : privOff+privSize]
	}
	var subrsOffRel int
	if len(privDictData) > 0 {
		pDict := parseCFFDict(privDictData)
		if sVals, ok := pDict[19]; ok && len(sVals) > 0 {
			subrsOffRel = int(sVals[0])
		}
	}
	if subrsOffRel > 0 {
		subrsAbs := privOff + subrsOffRel
		if subrsAbs < len(data) {
			_, subSz := getCFFIndexCount(data, subrsAbs)
			if subrsAbs+subSz <= len(data) {
				localSubrData = data[subrsAbs : subrsAbs+subSz]
			}
		}
	}
	charStringsOffs, ok := topDict[17]
	if !ok || len(charStringsOffs) == 0 {
		return nil, fmt.Errorf("missing charstrings")
	}
	charStringsOff := int(charStringsOffs[0])
	_, charStrSz := getCFFIndexCount(data, charStringsOff)
	charStringsData := data[charStringsOff : charStringsOff+charStrSz]
	delete(topDict, 1230)
	delete(topDict, 1236)
	delete(topDict, 1237)
	delete(topDict, 1234)
	delete(topDict, 15)
	delete(topDict, 16)
	var finalPrivData []byte
	if len(privDictData) > 0 {
		pDict := parseCFFDict(privDictData)
		if err := normalizeCFFPrivate(pDict); err != nil {
			return nil, err
		}
		if _, ok := pDict[19]; ok || len(localSubrData) > 0 {
			pDict[19] = []float64{0}
			for {
				length := len(encodeCFFDict(pDict))
				if pDict[19][0] == float64(length) {
					break
				}
				pDict[19][0] = float64(length)
			}
		}
		finalPrivData = encodeCFFDict(pDict)
	}
	return encodeSanitizedCFF(data[:hdrSize], nameIndexData, topDict, stringIndexData, globalSubrIndexData, charStringsData, finalPrivData, localSubrData), nil
}

// normalizeCFFPrivate 移除旧版CFF中不生效的缺省字段，拒绝需要额外解释的取值
// 入参: dict 私有字典
// 返回: error 不支持的旧版行为
func normalizeCFFPrivate(dict cffDict) error {
	for op, value := range map[int]float64{1215: 0, 1216: -1} {
		if values, ok := dict[op]; ok {
			if len(values) != 1 || values[0] != value {
				return fmt.Errorf("unsupported legacy CFF private operator %d", op)
			}
			delete(dict, op)
		}
	}
	return nil
}

// sanitizeMultiFDCFF 清洗多FD的CID CFF数据
// 入参: data 原始CFF数据, hdrSize 头部大小, nameIndexData 名称索引, topDict 顶层字典
// 入参: stringIndexData 字符串索引, globalSubrIndexData 全局子程序索引, fdArrOff FDArray偏移, fdCount FD数量
// 返回: []byte 清洗后的CFF数据, error 错误信息
func sanitizeMultiFDCFF(data []byte, hdrSize int, nameIndexData []byte, topDict cffDict, stringIndexData []byte, globalSubrIndexData []byte, fdArrOff int, fdCount int) ([]byte, error) {
	charStringsOffs, ok := topDict[17]
	if !ok || len(charStringsOffs) == 0 {
		return nil, fmt.Errorf("missing charstrings")
	}
	charStringsOff := int(charStringsOffs[0])
	charStrings, _, err := readType2Index(data, charStringsOff)
	if err != nil {
		return nil, err
	}
	if len(charStrings) == 0 {
		return nil, fmt.Errorf("missing charstrings")
	}
	fdSelectVals := topDict[1237]
	if len(fdSelectVals) != 1 || !finite(fdSelectVals[0]) || fdSelectVals[0] < 0 || fdSelectVals[0] >= float64(len(data)) || fdSelectVals[0] != math.Trunc(fdSelectVals[0]) {
		return nil, fmt.Errorf("invalid CFF FDSelect offset")
	}
	fdSelect, err := readType2FDSelect(data, int(fdSelectVals[0]), len(charStrings), fdCount)
	if err != nil {
		return nil, err
	}
	globalSubrs, _, err := readType2Index(globalSubrIndexData, 0)
	if err != nil {
		return nil, err
	}
	fdItems, _, err := readType2Index(data, fdArrOff)
	if err != nil {
		return nil, err
	}
	if len(fdItems) != fdCount {
		return nil, fmt.Errorf("invalid CFF FDArray count")
	}
	fonts := make([]type2Font, fdCount)
	privateDicts := make([]cffDict, fdCount)
	for fd, item := range fdItems {
		fonts[fd] = type2Font{data: data, chars: charStrings, globals: globalSubrs, seed: 1}
		privateDicts[fd], err = readType2Private(data, parseCFFDict(item)[18], &fonts[fd])
		if err != nil {
			return nil, fmt.Errorf("CFF font dictionary %d: %w", fd, err)
		}
	}
	inlined := make([][]byte, len(charStrings))
	var stack [48]float64
	for gid, cs := range charStrings {
		font := &fonts[fdSelect[gid]]
		var output bytes.Buffer
		steps := 0
		state := type2State{font: font, args: stack[:0], widthTarget: &fonts[0], width: font.def, seed: font.seed + uint64(gid), steps: &steps, output: &output, stripHints: true}
		if len(cs) == 0 {
			cs = []byte{14}
		}
		if _, err := state.run(cs, 0); err != nil {
			return nil, fmt.Errorf("CFF glyph %d: %w", gid, err)
		}
		inlined[gid] = output.Bytes()
	}
	delete(topDict, 1230)
	delete(topDict, 1236)
	delete(topDict, 1237)
	delete(topDict, 1234)
	delete(topDict, 15)
	delete(topDict, 16)
	privateDict := make(cffDict)
	if len(privateDicts) > 0 {
		for k, v := range privateDicts[0] {
			privateDict[k] = v
		}
	}
	delete(privateDict, 19)
	if err := normalizeCFFPrivate(privateDict); err != nil {
		return nil, err
	}
	finalPrivData := encodeCFFDict(privateDict)
	charStringsData := encodeCFFIndex(inlined)
	return encodeSanitizedCFF(data[:hdrSize], nameIndexData, topDict, stringIndexData, encodeCFFIndex(nil), charStringsData, finalPrivData, nil), nil
}

// encodeSanitizedCFF 按字典编码长度收敛偏移并组装规范化字体
// 入参: header 头部, names 名称索引, dict 顶层字典, strings 字符串索引, globals 全局子程序索引
// 入参: chars 字形索引, private 私有字典, locals 局部子程序索引
// 返回: []byte CFF字体数据
func encodeSanitizedCFF(header, names []byte, dict cffDict, strings, globals, chars, private, locals []byte) []byte {
	dict[17] = []float64{0}
	dict[18] = []float64{float64(len(private)), 0}
	prefix := len(header) + len(names) + len(strings) + len(globals)
	var top []byte
	for {
		top = encodeCFFIndex([][]byte{encodeCFFDict(dict)})
		charOffset := prefix + len(top)
		privateOffset := charOffset + len(chars)
		if dict[17][0] == float64(charOffset) && dict[18][1] == float64(privateOffset) {
			break
		}
		dict[17][0] = float64(charOffset)
		dict[18][1] = float64(privateOffset)
	}
	var out bytes.Buffer
	out.Grow(prefix + len(top) + len(chars) + len(private) + len(locals))
	for _, part := range [][]byte{header, names, top, strings, globals, chars, private, locals} {
		out.Write(part)
	}
	return out.Bytes()
}

// parseCFFAndCountGlyphs 解析CFF头部并统计字形数量
// 入参: data CFF数据
// 返回: int 字形数量, error 错误信息
func parseCFFAndCountGlyphs(data []byte) (int, error) {
	if len(data) < 4 {
		return 0, fmt.Errorf("data too short")
	}
	hdrSize := int(data[2])
	offset := hdrSize
	if offset >= len(data) {
		return 0, fmt.Errorf("truncated")
	}
	count, sz := getCFFIndexCount(data, offset)
	if count != 1 {
		return 0, fmt.Errorf("multi-font cff not supported")
	}
	offset += sz
	if offset >= len(data) {
		return 0, fmt.Errorf("truncated")
	}
	count, _ = getCFFIndexCount(data, offset)
	if count != 1 {
		return 0, fmt.Errorf("top dict count mismatch")
	}
	topDictData, _ := getCFFIndexData(data, offset)
	if topDictData != nil {
		dict := parseCFFDict(topDictData)
		if offsetVals, ok := dict[17]; ok && len(offsetVals) > 0 {
			charStrOff := int(offsetVals[0])
			if charStrOff > 0 && charStrOff < len(data) {
				count, _ := getCFFIndexCount(data, charStrOff)
				return count, nil
			}
		}
	}
	return 0, fmt.Errorf("failed to parse top dict")
}

// multiplyAffine 2x3仿射矩阵乘法
// 入参: a 矩阵A, b 矩阵B
// 返回: []float64 结果矩阵
func multiplyAffine(a, b []float64) []float64 {
	return []float64{
		a[0]*b[0] + a[2]*b[1],
		a[1]*b[0] + a[3]*b[1],
		a[0]*b[2] + a[2]*b[3],
		a[1]*b[2] + a[3]*b[3],
		a[0]*b[4] + a[2]*b[5] + a[4],
		a[1]*b[4] + a[3]*b[5] + a[5],
	}
}

// parseCFFDict 解析CFF字典数据
// 入参: data 字典数据
// 返回: cffDict 解析后的字典映射
func parseCFFDict(data []byte) cffDict {
	dict := make(cffDict)
	var operands []float64
	i := 0
	for i < len(data) {
		b := data[i]
		i++
		if b <= 27 {
			op := int(b)
			if b == 12 {
				if i >= len(data) {
					break
				}
				op = 1200 + int(data[i])
				i++
			}
			dict[op] = operands
			operands = nil
		} else if b == 28 {
			if i+1 < len(data) {
				val := int(int16(binary.BigEndian.Uint16(data[i:])))
				operands = append(operands, float64(val))
				i += 2
			}
		} else if b == 29 {
			if i+3 < len(data) {
				val := int(int32(binary.BigEndian.Uint32(data[i:])))
				operands = append(operands, float64(val))
				i += 4
			}
		} else if b == 30 {
			s, n := parseCFFReal(data[i:])
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				operands = append(operands, f)
			}
			i += n
		} else if b >= 32 && b <= 246 {
			operands = append(operands, float64(int(b)-139))
		} else if b >= 247 && b <= 250 {
			if i < len(data) {
				b1 := int(data[i])
				i++
				operands = append(operands, float64((int(b)-247)*256+b1+108))
			}
		} else if b >= 251 && b <= 254 {
			if i < len(data) {
				b1 := int(data[i])
				i++
				operands = append(operands, float64(-(int(b)-251)*256-b1-108))
			}
		}
	}
	return dict
}

// parseCFFReal 解析CFF实数编码
// 入参: data 数据切片
// 返回: string 实数字符串, int 消耗字节数
func parseCFFReal(data []byte) (string, int) {
	var sb strings.Builder
	i := 0
	done := false
	for i < len(data) && !done {
		b := data[i]
		i++
		nibbles := []byte{b >> 4, b & 0x0F}
		for _, n := range nibbles {
			if n == 0xF {
				done = true
				break
			}
			if n <= 9 {
				sb.WriteString(strconv.Itoa(int(n)))
			}
			if n == 0xA {
				sb.WriteString(".")
			}
			if n == 0xB {
				sb.WriteString("E")
			}
			if n == 0xC {
				sb.WriteString("E-")
			}
			if n == 0xE {
				sb.WriteString("-")
			}
		}
	}
	return sb.String(), i
}

// encodeCFFDict 编码CFF字典 (仅使用float64操作数)
// 入参: dict CFF字典映射
// 返回: []byte 编码后的字典数据
func encodeCFFDict(dict cffDict) []byte {
	buf := new(bytes.Buffer)
	var keys []int
	for k := range dict {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, op := range keys {
		vals := dict[op]
		for _, val := range vals {
			encodeNumberCFF(buf, val)
		}
		if op >= 1200 {
			buf.WriteByte(12)
			buf.WriteByte(byte(op - 1200))
		} else {
			buf.WriteByte(byte(op))
		}
	}
	return buf.Bytes()
}

// encodeNumberCFF 编码单个数值到CFF格式
// 入参: buf 缓冲区, val 数值
func encodeNumberCFF(buf *bytes.Buffer, val float64) {
	if val == math.Trunc(val) {
		iv := int(val)
		if iv >= -107 && iv <= 107 {
			buf.WriteByte(byte(iv + 139))
		} else if iv >= 108 && iv <= 1131 {
			iv -= 108
			buf.WriteByte(byte((iv >> 8) + 247))
			buf.WriteByte(byte(iv & 0xFF))
		} else if iv >= -1131 && iv <= -108 {
			iv = -iv - 108
			buf.WriteByte(byte((iv >> 8) + 251))
			buf.WriteByte(byte(iv & 0xFF))
		} else if iv >= -32768 && iv <= 32767 {
			buf.WriteByte(28)
			binary.Write(buf, binary.BigEndian, int16(iv))
		} else {
			buf.WriteByte(29)
			binary.Write(buf, binary.BigEndian, int32(iv))
		}
	} else {
		s := fmt.Sprintf("%g", val)
		buf.WriteByte(30)
		var nibbles []byte
		for _, c := range s {
			var n byte
			switch c {
			case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				n = byte(c - '0')
			case '.':
				n = 0xA
			case 'E', 'e':
				n = 0xB
			case '-':
				n = 0xE
			}
			nibbles = append(nibbles, n)
		}
		nibbles = append(nibbles, 0xF)
		if len(nibbles)%2 != 0 {
			nibbles = append(nibbles, 0xF)
		}
		for i := 0; i < len(nibbles); i += 2 {
			b := nibbles[i] << 4
			if i+1 < len(nibbles) {
				b |= nibbles[i+1]
			}
			buf.WriteByte(b)
		}
	}
}

// encodeCFFIndex 编码CFF索引结构
// 入参: items 数据项列表
// 返回: []byte 编码后的索引数据
func encodeCFFIndex(items []([]byte)) []byte {
	count := len(items)
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(count))
	if count == 0 {
		return buf.Bytes()
	}
	totalSize := 0
	for _, item := range items {
		totalSize += len(item)
	}
	offSize := 1
	if totalSize+1 > 255 {
		offSize = 2
	}
	if totalSize+1 > 65535 {
		offSize = 3
	}
	if totalSize+1 > 16777215 {
		offSize = 4
	}
	buf.WriteByte(byte(offSize))
	offset := 1
	putOffset(buf, offset, offSize)
	for _, item := range items {
		offset += len(item)
		putOffset(buf, offset, offSize)
	}
	for _, item := range items {
		buf.Write(item)
	}
	return buf.Bytes()
}

// putOffset 写入指定大小的偏移量
// 入参: buf 缓冲区, val 偏移值, size 字节大小
func putOffset(buf *bytes.Buffer, val int, size int) {
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, uint32(val))
	buf.Write(tmp[4-size:])
}

// getCFFIndexCount 读取CFF索引的计数和大小
// 入参: data CFF数据, offset 偏移量
// 返回: int 数量, int 索引结构总大小
func getCFFIndexCount(data []byte, offset int) (int, int) {
	if offset+2 > len(data) {
		return 0, 0
	}
	count := int(binary.BigEndian.Uint16(data[offset:]))
	if count == 0 {
		return 0, 2
	}
	if offset+3 > len(data) {
		return 0, 0
	}
	offSize := int(data[offset+2])
	if offSize < 1 || offSize > 4 {
		return 0, 0
	}
	dataSizeLen := (count + 1) * offSize
	if offset+3+dataSizeLen > len(data) {
		return 0, 0
	}
	endOffsetPos := offset + 3 + count*offSize
	if endOffsetPos+offSize > len(data) {
		return 0, 0
	}
	dataEnd := readCFFOffset(data, endOffsetPos, offSize)
	if dataEnd < 1 {
		return 0, 0
	}
	return count, 3 + (count+1)*offSize + (dataEnd - 1)
}

// getCFFIndexData 读取CFF索引的数据块
// 入参: data CFF数据, offset 偏移量
// 返回: []byte 索引数据(已去除offsets), int 索引结构总大小
func getCFFIndexData(data []byte, offset int) ([]byte, int) {
	count, size := getCFFIndexCount(data, offset)
	if count == 0 {
		return nil, size
	}
	if offset+3 > len(data) {
		return nil, size
	}
	offSize := int(data[offset+2])
	if offset+3+offSize > len(data) {
		return nil, size
	}
	off0 := readCFFOffset(data, offset+3, offSize)
	if offset+3+offSize*2 > len(data) {
		return nil, size
	}
	off1 := readCFFOffset(data, offset+3+offSize, offSize)
	dataStartRel := 3 + (count+1)*offSize
	dataStartAbs := offset + dataStartRel
	start := dataStartAbs + (off0 - 1)
	length := off1 - off0
	if start < 0 || length < 0 || start+length > len(data) {
		return nil, size
	}
	return data[start : start+length], size
}

// readCFFIndexItems 读取CFF索引中的所有数据项
// 入参: data CFF数据, offset 索引偏移
// 返回: [][]byte 数据项列表
func readCFFIndexItems(data []byte, offset int) [][]byte {
	count, _ := getCFFIndexCount(data, offset)
	if count == 0 || offset+3 > len(data) {
		return nil
	}
	offSize := int(data[offset+2])
	if offSize < 1 || offSize > 4 {
		return nil
	}
	dataStart := offset + 3 + (count+1)*offSize
	items := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		p1 := offset + 3 + i*offSize
		p2 := p1 + offSize
		if p2+offSize > len(data) {
			return items
		}
		off1 := readCFFOffset(data, p1, offSize)
		off2 := readCFFOffset(data, p2, offSize)
		start := dataStart + off1 - 1
		length := off2 - off1
		if start < 0 || length < 0 || start+length > len(data) {
			items = append(items, nil)
			continue
		}
		items = append(items, data[start:start+length])
	}
	return items
}

// parseCFFFDSelect 解析CID CFF的FDSelect
// 入参: data CFF数据, offset FDSelect偏移, numGlyphs 字形数量
// 返回: []int 字形对应的FD索引
func parseCFFFDSelect(data []byte, offset int, numGlyphs int) []int {
	result := make([]int, numGlyphs)
	if offset <= 0 || offset >= len(data) {
		return result
	}
	format := data[offset]
	pos := offset + 1
	switch format {
	case 0:
		for i := 0; i < numGlyphs && pos+i < len(data); i++ {
			result[i] = int(data[pos+i])
		}
	case 3:
		if pos+2 > len(data) {
			return result
		}
		nRanges := int(binary.BigEndian.Uint16(data[pos:]))
		pos += 2
		ranges := make([]struct {
			first int
			fd    int
		}, 0, nRanges)
		for i := 0; i < nRanges && pos+3 <= len(data); i++ {
			first := int(binary.BigEndian.Uint16(data[pos:]))
			fd := int(data[pos+2])
			ranges = append(ranges, struct {
				first int
				fd    int
			}{first: first, fd: fd})
			pos += 3
		}
		if pos+2 > len(data) {
			return result
		}
		sentinel := int(binary.BigEndian.Uint16(data[pos:]))
		for i, item := range ranges {
			end := sentinel
			if i+1 < len(ranges) {
				end = ranges[i+1].first
			}
			if end > numGlyphs {
				end = numGlyphs
			}
			for gid := item.first; gid < end; gid++ {
				if gid >= 0 && gid < numGlyphs {
					result[gid] = item.fd
				}
			}
		}
	}
	return result
}

// cffSubrBias 获取Type2子程序偏移
// 入参: count 子程序数量
// 返回: int 偏移量
func cffSubrBias(count int) int {
	if count < 1240 {
		return 107
	}
	if count < 33900 {
		return 1131
	}
	return 32768
}

// parseCFFWidths 从Type2执行状态获取字宽，保留首个清栈指令的宽度语义
// 入参: data CFF数据, numGlyphs 字形数量
// 返回: []uint16 宽度列表, error 字体或执行错误
func parseCFFWidths(data []byte, numGlyphs int) ([]uint16, error) {
	font, err := readType2Font(data)
	if err != nil {
		return nil, err
	}
	if len(font.chars) != numGlyphs {
		return nil, fmt.Errorf("CFF glyph count differs from metrics")
	}
	widths := make([]uint16, numGlyphs)
	var stack [48]float64
	for gid, program := range font.chars {
		if len(program) == 0 {
			program = []byte{14}
		}
		steps := 0
		state := type2State{font: font, args: stack[:0], width: font.def, widthOnly: true, seed: font.seed + uint64(gid), steps: &steps}
		if _, err := state.run(program, 0); err != nil && !state.widthSet {
			state.width = font.def
		}
		if gid == 0 && state.width < 0 {
			steps = 0
			empty := type2State{font: font, args: stack[:0], width: font.def, seed: font.seed, steps: &steps}
			_, err := empty.run(program, 0)
			if err == nil && !empty.pathStarted && !empty.changed {
				state.width = 0
			}
		}
		if !finite(state.width) || state.width < 0 || state.width > math.MaxUint16 {
			return nil, fmt.Errorf("CFF glyph %d width cannot be represented", gid)
		}
		widths[gid] = uint16(math.Round(state.width))
	}
	return widths, nil
}

// parseNumberType2 解析Number (Type 2)
// 入参: data 数据, idx 索引
// 返回: float64 浮点值
func parseNumberType2(data []byte, idx int) float64 {
	b := data[idx]
	if b >= 32 && b <= 246 {
		return float64(int(b) - 139)
	}
	if b >= 247 && b <= 250 {
		return float64((int(b)-247)*256 + int(data[idx+1]) + 108)
	}
	if b >= 251 && b <= 254 {
		return float64(-(int(b)-251)*256 - int(data[idx+1]) - 108)
	}
	if b == 28 {
		return float64(int16(binary.BigEndian.Uint16(data[idx+1:])))
	}
	if b == 255 {
		return float64(int16(binary.BigEndian.Uint16(data[idx+1:]))) + float64(binary.BigEndian.Uint16(data[idx+3:]))/65536.0
	}
	return 0
}

// readCFFOffset 读取指定大小的偏移量
// 入参: data 数据, pos 位置, size 大小
// 返回: int 偏移量
func readCFFOffset(data []byte, pos, size int) int {
	var val int
	for i := 0; i < size; i++ {
		if pos+i < len(data) {
			val = (val << 8) | int(data[pos+i])
		}
	}
	return val
}

// getCFFCharsetInfo 读取CFF字符集和ROS信息
// 入参: data CFF数据, numGlyphs 字形数量
// 返回: []int SID或CID列表, string Registry, string Ordering, int 字符串索引偏移, bool 是否成功
func getCFFCharsetInfo(data []byte, numGlyphs int) ([]int, string, string, int, bool) {
	if len(data) < 4 {
		return nil, "", "", 0, false
	}
	hdrSize := int(data[2])
	offset := hdrSize
	_, sz := getCFFIndexCount(data, offset)
	offset += sz
	_, szTD := getCFFIndexCount(data, offset)
	topDictData, _ := getCFFIndexData(data, offset)
	offset += szTD
	stringIndexOff := offset
	if topDictData == nil {
		return nil, "", "", 0, false
	}
	td := parseCFFDict(topDictData)
	registry, ordering := getCFFROS(data, stringIndexOff, td)
	actual, err := parseCFFAndCountGlyphs(data)
	if err != nil || numGlyphs < 1 || actual != numGlyphs {
		return nil, "", "", 0, false
	}
	charsetOff := 0
	if values, ok := td[15]; ok {
		if len(values) != 1 || !finite(values[0]) || values[0] < 0 || values[0] > float64(len(data)) || values[0] != math.Trunc(values[0]) {
			return nil, "", "", 0, false
		}
		charsetOff = int(values[0])
	}
	if values, cid := td[1230]; cid && (len(values) != 3 || charsetOff <= 2) {
		return nil, "", "", 0, false
	}
	sids, err := parseCFFCharset(data, charsetOff, numGlyphs)
	if err != nil {
		return nil, "", "", 0, false
	}
	return sids, registry, ordering, stringIndexOff, true
}

// getCmapFromCFF 从CFF数据中恢复Unicode映射
// 入参: data CFF数据, numGlyphs 字形数量
// 返回: map[rune]uint16 恢复的映射表
func getCmapFromCFF(data []byte, numGlyphs int) map[rune]uint16 {
	sids, registry, ordering, stringIndexOff, ok := getCFFCharsetInfo(data, numGlyphs)
	if !ok {
		return nil
	}
	mapping := make(map[rune]uint16)
	if registry == "Adobe" && ordering == "GB1" {
		for gid, cid := range sids {
			if gid == 0 {
				continue
			}
			mapping[packedGlyphRune(uint16(gid))] = uint16(gid)
			if r, ok := adobeGB1CIDToUnicode(cid); ok {
				mapping[r] = uint16(gid)
			}
		}
		return mapping
	}
	if registry != "" {
		for gid := range sids {
			if gid == 0 {
				continue
			}
			mapping[packedGlyphRune(uint16(gid))] = uint16(gid)
		}
		return mapping
	}
	for gid, sid := range sids {
		if gid == 0 {
			continue
		}
		var name string
		if sid <= 390 {
			if sid >= 0 && sid < len(cffStandardStrings) {
				name = cffStandardStrings[sid]
			}
		} else {
			idx := sid - 391
			name = readStringIndexItem(data, stringIndexOff, idx)
		}
		r := rune(0)
		if name != "" {
			r = getUnicodeFromName(name)
		}
		if r == 0 {
			r = packedGlyphRune(uint16(gid))
		}
		mapping[packedGlyphRune(uint16(gid))] = uint16(gid)
		mapping[r] = uint16(gid)
	}
	return mapping
}

// getCFFCIDRuneMap 获取CID到包装字体字符的映射
// 入参: data CFF或OpenType字体数据
// 返回: map[uint16]rune CID映射
func getCFFCIDRuneMap(data []byte) map[uint16]rune {
	cffData := getCFFData(data)
	if cffData == nil {
		return nil
	}
	numGlyphs, err := parseCFFAndCountGlyphs(cffData)
	if err != nil {
		return nil
	}
	sids, registry, _, _, ok := getCFFCharsetInfo(cffData, numGlyphs)
	if !ok || registry == "" {
		return nil
	}
	mapping := getCmapFromCFF(cffData, numGlyphs)
	if len(mapping) == 0 {
		return nil
	}
	gidRunes := make(map[uint16]rune)
	for run, gid := range mapping {
		if run != packedGlyphRune(gid) {
			gidRunes[gid] = run
		}
	}
	for run, gid := range mapping {
		if _, ok := gidRunes[gid]; !ok {
			gidRunes[gid] = run
		}
	}
	result := make(map[uint16]rune)
	for gid, cid := range sids {
		if gid == 0 || cid < 0 || cid > 0xFFFF {
			continue
		}
		if run, ok := gidRunes[uint16(gid)]; ok {
			result[uint16(cid)] = run
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// getCFFData 获取字体中的CFF数据
// 入参: data 字体数据
// 返回: []byte CFF数据
func getCFFData(data []byte) []byte {
	if isBareCFFData(data) {
		return data
	}
	if len(data) < 12 {
		return nil
	}
	tag := string(data[0:4])
	u32Tag := binary.BigEndian.Uint32(data[0:4])
	if tag != "OTTO" && tag != "true" && u32Tag != 0x00010000 {
		return nil
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	for i := 0; i < numTables; i++ {
		pos := 12 + i*16
		if pos+16 > len(data) {
			return nil
		}
		if string(data[pos:pos+4]) != "CFF " {
			continue
		}
		offset := int(binary.BigEndian.Uint32(data[pos+8 : pos+12]))
		length := int(binary.BigEndian.Uint32(data[pos+12 : pos+16]))
		if offset < 0 || length < 0 || offset+length > len(data) {
			return nil
		}
		return data[offset : offset+length]
	}
	return nil
}

// parseCFFCharset 按CFF规范第13节及附录C读取字符集，保留GID顺序
// 入参: data CFF数据, offset 字符集预定义值或偏移, count 字形数量
// 返回: []int SID或CID列表, error 错误信息
func parseCFFCharset(data []byte, offset, count int) ([]int, error) {
	charset := make([]int, count)
	if offset <= 2 {
		if offset == 0 {
			if count > 229 {
				return nil, fmt.Errorf("invalid ISOAdobe charset length")
			}
			for gid := range charset {
				charset[gid] = gid
			}
		} else {
			predefined := cffExpertCharset[:]
			if offset == 2 {
				predefined = cffExpertSubsetCharset[:]
			}
			if count > len(predefined) {
				return nil, fmt.Errorf("invalid predefined CFF charset length")
			}
			for gid := range charset {
				charset[gid] = int(predefined[gid])
			}
		}
		return charset, nil
	}
	if offset >= len(data) {
		return nil, fmt.Errorf("invalid CFF charset offset")
	}
	format, pos := data[offset], offset+1
	if format > 2 {
		return nil, fmt.Errorf("invalid CFF charset format")
	}
	seen := map[int]bool{0: true}
	for gid := 1; gid < count; {
		if pos+2 > len(data) {
			return nil, fmt.Errorf("truncated CFF charset")
		}
		first := int(binary.BigEndian.Uint16(data[pos:]))
		pos += 2
		run := 1
		switch format {
		case 1:
			if pos == len(data) {
				return nil, fmt.Errorf("truncated CFF charset range")
			}
			run += int(data[pos])
			pos++
		case 2:
			if pos+2 > len(data) {
				return nil, fmt.Errorf("truncated CFF charset range")
			}
			run += int(binary.BigEndian.Uint16(data[pos:]))
			pos += 2
		}
		if run > count-gid || first+run > 65536 {
			return nil, fmt.Errorf("invalid CFF charset range")
		}
		for n := 0; n < run; n++ {
			sid := first + n
			if seen[sid] {
				return nil, fmt.Errorf("duplicate CFF charset identifier")
			}
			seen[sid] = true
			charset[gid] = sid
			gid++
		}
	}
	return charset, nil
}

// readStringIndexItem 读取CFF字符串索引项
// 入参: data CFF数据, offset 索引偏移, idx 索引号
// 返回: string 读取的字符串
func readStringIndexItem(data []byte, offset int, idx int) string {
	if offset >= len(data) {
		return ""
	}
	count := int(binary.BigEndian.Uint16(data[offset:]))
	offSize := int(data[offset+2])
	if idx >= count {
		return ""
	}
	offArrayStart := offset + 3
	p1 := offArrayStart + idx*offSize
	p2 := p1 + offSize
	if p2+offSize > len(data) {
		return ""
	}
	loc1 := readCFFOffset(data, p1, offSize)
	loc2 := readCFFOffset(data, p2, offSize)
	dataStart := offArrayStart + (count+1)*offSize
	start := dataStart + loc1 - 1
	length := loc2 - loc1
	if start < 0 || start+length > len(data) {
		return ""
	}
	return string(data[start : start+length])
}

// getCFFROS 读取CID字体ROS信息
// 入参: data CFF数据, stringIndexOff 字符串索引偏移, td 顶层字典
// 返回: string Registry, string Ordering
func getCFFROS(data []byte, stringIndexOff int, td cffDict) (string, string) {
	vals, ok := td[1230]
	if !ok || len(vals) < 2 {
		return "", ""
	}
	registry := getCFFSIDString(data, stringIndexOff, int(vals[0]))
	ordering := getCFFSIDString(data, stringIndexOff, int(vals[1]))
	return registry, ordering
}

// getCFFSIDString 读取CFF SID字符串
// 入参: data CFF数据, stringIndexOff 字符串索引偏移, sid 字符串ID
// 返回: string 字符串内容
func getCFFSIDString(data []byte, stringIndexOff int, sid int) string {
	if sid >= 0 && sid < len(cffStandardStrings) {
		return cffStandardStrings[sid]
	}
	if sid > 390 {
		return readStringIndexItem(data, stringIndexOff, sid-391)
	}
	return ""
}

// adobeGB1CIDToUnicode 将Adobe-GB1 CID转为Unicode
// 入参: cid 字符CID
// 返回: rune Unicode字符, bool 是否成功
func adobeGB1CIDToUnicode(cid int) (rune, bool) {
	switch cid {
	case 329:
		return '“', true
	case 330:
		return '”', true
	case 821:
		return '、', true
	case 822:
		return '。', true
	case 829:
		return '《', true
	case 830:
		return '》', true
	}
	n := cid + 471
	if n <= 0 {
		return 0, false
	}
	row := (n-1)/94 + 1
	cell := (n-1)%94 + 1
	if row < 16 || row > 87 || cell < 1 || cell > 94 {
		return 0, false
	}
	gbk := []byte{byte(row + 0xA0), byte(cell + 0xA0)}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(gbk)
	if err != nil || len(decoded) == 0 {
		return 0, false
	}
	rs := []rune(string(decoded))
	if len(rs) != 1 {
		return 0, false
	}
	return rs[0], true
}

// getUnicodeFromName 根据字形名称获取对应的Unicode字符
// 入参: name 字形名称
// 返回: rune Unicode字符
func getUnicodeFromName(name string) rune {
	name, _, _ = strings.Cut(name, ".")
	digits := ""
	if strings.HasPrefix(name, "uni") && len(name) == 7 {
		digits = name[3:]
	} else if strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7 {
		digits = name[1:]
	}
	if digits != "" {
		value, err := strconv.ParseUint(digits, 16, 32)
		if err == nil && value <= utf8.MaxRune && utf8.ValidRune(rune(value)) {
			return rune(value)
		}
	}
	switch name {
	case "space":
		return ' '
	case "exclam":
		return '!'
	case "quotedbl":
		return '"'
	case "numbersign":
		return '#'
	case "dollar":
		return '$'
	case "percent":
		return '%'
	case "ampersand":
		return '&'
	case "quotesingle":
		return '\''
	case "parenleft":
		return '('
	case "parenright":
		return ')'
	case "asterisk":
		return '*'
	case "plus":
		return '+'
	case "comma":
		return ','
	case "hyphen":
		return '-'
	case "period":
		return '.'
	case "slash":
		return '/'
	case "zero":
		return '0'
	case "one":
		return '1'
	case "two":
		return '2'
	case "three":
		return '3'
	case "four":
		return '4'
	case "five":
		return '5'
	case "six":
		return '6'
	case "seven":
		return '7'
	case "eight":
		return '8'
	case "nine":
		return '9'
	case "colon":
		return ':'
	case "semicolon":
		return ';'
	case "less":
		return '<'
	case "equal":
		return '='
	case "greater":
		return '>'
	case "question":
		return '?'
	case "at":
		return '@'
	case "bracketleft":
		return '['
	case "backslash":
		return '\\'
	case "bracketright":
		return ']'
	case "asciicircum":
		return '^'
	case "underscore":
		return '_'
	case "grave":
		return '`'
	case "braceleft":
		return '{'
	case "bar":
		return '|'
	case "braceright":
		return '}'
	case "asciitilde":
		return '~'
	}
	if len(name) == 1 {
		return rune(name[0])
	}
	return 0
}
