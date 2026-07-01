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

	"golang.org/x/text/encoding/simplifiedchinese"
)

// wrapCFFToOTF 将 CFF 裸数据包装为 OpenType 字体格式
// 入参: cffData CFF字体数据
// 返回: []byte OTF字体数据, map[rune]uint16 字符映射, error 错误信息
func wrapCFFToOTF(cffData []byte) ([]byte, map[rune]uint16, error) {
	numGlyphs, err := parseCFFAndCountGlyphs(cffData)
	if err != nil {
		return nil, nil, err
	}
	mapping := getCmapFromCFF(cffData, int(numGlyphs))
	sanitized, err := sanitizeCFF(cffData)
	if err == nil {
		cffData = sanitized
	}
	widths, err := parseCFFWidths(cffData, numGlyphs)
	if err != nil {
		widths = make([]uint16, numGlyphs)
		for i := range widths {
			widths[i] = 500
		}
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

// cffDict 使用 float64 存储所有数值，以统一处理整数和实数
type cffDict map[int][]float64

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
	topDict[18] = []float64{float64(privSize), 0}
	var newCFF bytes.Buffer
	newCFF.Write(data[:hdrSize])
	newCFF.Write(nameIndexData)
	dummyDict := make(map[int][]float64)
	for k, v := range topDict {
		dummyDict[k] = v
	}
	dummyDict[17] = []float64{0}
	dummyDict[18] = []float64{float64(privSize), 0}
	dummyTopData := encodeCFFDict(dummyDict)
	topIdxSize := 2 + 1 + 8 + len(dummyTopData)
	dataStart := hdrSize + len(nameIndexData) + topIdxSize + len(stringIndexData) + len(globalSubrIndexData)
	charStringsPos := dataStart
	privatePos := charStringsPos + len(charStringsData)
	privateLen := privSize
	var finalPrivData []byte
	if len(privDictData) > 0 {
		pDict := parseCFFDict(privDictData)
		if _, ok := pDict[19]; ok || len(localSubrData) > 0 {
			pDict[19] = []float64{float64(privateLen)}
		}
		finalPrivData = encodeCFFDict(pDict)
		privateLen = len(finalPrivData)
	}
	localSubrsPos := privatePos + privateLen
	topDict[17] = []float64{float64(charStringsPos)}
	topDict[18] = []float64{float64(privateLen), float64(privatePos)}
	finalTopData := encodeCFFDict(topDict)
	topIndex := encodeCFFIndex([]([]byte){finalTopData})
	newCFF.Reset()
	newCFF.Write(data[:hdrSize])
	newCFF.Write(nameIndexData)
	newCFF.Write(topIndex)
	newCFF.Write(stringIndexData)
	newCFF.Write(globalSubrIndexData)
	if newCFF.Len() != dataStart {
		diff := newCFF.Len() - dataStart
		charStringsPos += diff
		privatePos += diff
		localSubrsPos += diff
		topDict[17] = []float64{float64(charStringsPos)}
		topDict[18] = []float64{float64(privateLen), float64(privatePos)}
		finalTopData = encodeCFFDict(topDict)
		topIndex = encodeCFFIndex([]([]byte){finalTopData})
		newCFF.Reset()
		newCFF.Write(data[:hdrSize])
		newCFF.Write(nameIndexData)
		newCFF.Write(topIndex)
		newCFF.Write(stringIndexData)
		newCFF.Write(globalSubrIndexData)
	}
	newCFF.Write(charStringsData)
	newCFF.Write(finalPrivData)
	if len(localSubrData) > 0 {
		newCFF.Write(localSubrData)
	}
	return newCFF.Bytes(), nil
}

// sanitizeMultiFDCFF 清洗多 FD 的 CID CFF 数据
// 入参: data 原始CFF数据, hdrSize 头部大小, nameIndexData 名称索引, topDict 顶层字典
// 入参: stringIndexData 字符串索引, globalSubrIndexData 全局子程序索引, fdArrOff FDArray偏移, fdCount FD数量
// 返回: []byte 清洗后的CFF数据, error 错误信息
func sanitizeMultiFDCFF(data []byte, hdrSize int, nameIndexData []byte, topDict cffDict, stringIndexData []byte, globalSubrIndexData []byte, fdArrOff int, fdCount int) ([]byte, error) {
	charStringsOffs, ok := topDict[17]
	if !ok || len(charStringsOffs) == 0 {
		return nil, fmt.Errorf("missing charstrings")
	}
	charStringsOff := int(charStringsOffs[0])
	charStrings := readCFFIndexItems(data, charStringsOff)
	if len(charStrings) == 0 {
		return nil, fmt.Errorf("missing charstrings")
	}
	fdSelect := make([]int, len(charStrings))
	if fdSelectVals, ok := topDict[1237]; ok && len(fdSelectVals) > 0 {
		fdSelect = parseCFFFDSelect(data, int(fdSelectVals[0]), len(charStrings))
	}
	globalSubrs := readCFFIndexItems(globalSubrIndexData, 0)
	localSubrs, privateDicts := readCFFLocalSubrs(data, fdArrOff, fdCount)
	inlined := make([][]byte, len(charStrings))
	for gid, cs := range charStrings {
		fd := 0
		if gid < len(fdSelect) && fdSelect[gid] >= 0 && fdSelect[gid] < len(localSubrs) {
			fd = fdSelect[gid]
		}
		inlined[gid] = removeType2Hints(inlineType2CharString(cs, localSubrs[fd], globalSubrs, 0))
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
	finalPrivData := encodeCFFDict(privateDict)
	privateLen := len(finalPrivData)
	charStringsData := encodeCFFIndex(inlined)
	topDict[18] = []float64{float64(privateLen), 0}
	var newCFF bytes.Buffer
	dummyDict := make(cffDict)
	for k, v := range topDict {
		dummyDict[k] = v
	}
	dummyDict[17] = []float64{0}
	dummyDict[18] = []float64{float64(privateLen), 0}
	dummyTopData := encodeCFFDict(dummyDict)
	topIdxSize := 2 + 1 + 8 + len(dummyTopData)
	dataStart := hdrSize + len(nameIndexData) + topIdxSize + len(stringIndexData) + len(globalSubrIndexData)
	charStringsPos := dataStart
	privatePos := charStringsPos + len(charStringsData)
	topDict[17] = []float64{float64(charStringsPos)}
	topDict[18] = []float64{float64(privateLen), float64(privatePos)}
	finalTopData := encodeCFFDict(topDict)
	topIndex := encodeCFFIndex([][]byte{finalTopData})
	newCFF.Write(data[:hdrSize])
	newCFF.Write(nameIndexData)
	newCFF.Write(topIndex)
	newCFF.Write(stringIndexData)
	newCFF.Write(globalSubrIndexData)
	if newCFF.Len() != dataStart {
		diff := newCFF.Len() - dataStart
		charStringsPos += diff
		privatePos += diff
		topDict[17] = []float64{float64(charStringsPos)}
		topDict[18] = []float64{float64(privateLen), float64(privatePos)}
		finalTopData = encodeCFFDict(topDict)
		topIndex = encodeCFFIndex([][]byte{finalTopData})
		newCFF.Reset()
		newCFF.Write(data[:hdrSize])
		newCFF.Write(nameIndexData)
		newCFF.Write(topIndex)
		newCFF.Write(stringIndexData)
		newCFF.Write(globalSubrIndexData)
	}
	newCFF.Write(charStringsData)
	newCFF.Write(finalPrivData)
	return newCFF.Bytes(), nil
}

// parseCFFAndCountGlyphs 解析 CFF 头部并统计字形数量
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

// multiplyAffine 2x3 仿射矩阵乘法
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

// parseCFFDict 解析 CFF 字典数据
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

// parseCFFReal 解析 CFF 实数编码
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

// encodeCFFDict 编码 CFF 字典 (仅使用 float64 操作数)
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

// encodeNumberCFF 编码单个数值到 CFF 格式
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

// encodeCFFIndex 编码 CFF 索引结构
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

// getCFFIndexCount 读取 CFF 索引的计数和大小
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

// getCFFIndexData 读取 CFF 索引的数据块
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

// readCFFIndexItems 读取 CFF 索引中的所有数据项
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

// parseCFFFDSelect 解析 CID CFF 的 FDSelect
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

// readCFFLocalSubrs 读取 CID CFF 的本地子程序
// 入参: data CFF数据, fdArrOff FDArray偏移, fdCount FD数量
// 返回: [][][]byte 本地子程序列表, []cffDict Private字典列表
func readCFFLocalSubrs(data []byte, fdArrOff int, fdCount int) ([][][]byte, []cffDict) {
	fdItems := readCFFIndexItems(data, fdArrOff)
	localSubrs := make([][][]byte, fdCount)
	privateDicts := make([]cffDict, fdCount)
	for i := 0; i < fdCount && i < len(fdItems); i++ {
		fdDict := parseCFFDict(fdItems[i])
		privVals, ok := fdDict[18]
		if !ok || len(privVals) != 2 {
			privateDicts[i] = make(cffDict)
			continue
		}
		privSize := int(privVals[0])
		privOff := int(privVals[1])
		if privSize <= 0 || privOff < 0 || privOff+privSize > len(data) {
			privateDicts[i] = make(cffDict)
			continue
		}
		privData := data[privOff : privOff+privSize]
		privDict := parseCFFDict(privData)
		privateDicts[i] = privDict
		if subrVals, ok := privDict[19]; ok && len(subrVals) > 0 {
			subrOff := privOff + int(subrVals[0])
			if subrOff >= 0 && subrOff < len(data) {
				localSubrs[i] = readCFFIndexItems(data, subrOff)
			}
		}
	}
	return localSubrs, privateDicts
}

type type2Operand struct {
	value    int
	outStart int
}

// inlineType2CharString 内联 Type2 CharString 子程序调用
// 入参: data CharString数据, localSubrs 本地子程序, globalSubrs 全局子程序, depth 递归深度
// 返回: []byte 内联后的CharString数据
func inlineType2CharString(data []byte, localSubrs, globalSubrs [][]byte, depth int) []byte {
	if depth > 8 {
		return data
	}
	out := make([]byte, 0, len(data))
	var stack []type2Operand
	hintCount := 0
	for i := 0; i < len(data); {
		b := data[i]
		if b == 28 && i+2 < len(data) {
			start := len(out)
			out = append(out, data[i:i+3]...)
			stack = append(stack, type2Operand{value: int(parseShortInt(data, i)), outStart: start})
			i += 3
			continue
		}
		if b >= 32 && b <= 246 {
			start := len(out)
			out = append(out, b)
			stack = append(stack, type2Operand{value: int(parseNumberType2(data, i)), outStart: start})
			i++
			continue
		}
		if b >= 247 && b <= 254 && i+1 < len(data) {
			start := len(out)
			out = append(out, data[i:i+2]...)
			stack = append(stack, type2Operand{value: int(parseNumberType2(data, i)), outStart: start})
			i += 2
			continue
		}
		if b == 255 && i+4 < len(data) {
			start := len(out)
			out = append(out, data[i:i+5]...)
			stack = append(stack, type2Operand{value: int(parseNumberType2(data, i)), outStart: start})
			i += 5
			continue
		}
		if b == 10 || b == 29 {
			if len(stack) > 0 {
				operand := stack[len(stack)-1]
				out = out[:operand.outStart]
				subrs := localSubrs
				if b == 29 {
					subrs = globalSubrs
				}
				idx := operand.value + cffSubrBias(len(subrs))
				if idx >= 0 && idx < len(subrs) {
					subr := inlineType2CharString(subrs[idx], localSubrs, globalSubrs, depth+1)
					if len(subr) > 0 && subr[len(subr)-1] == 11 {
						subr = subr[:len(subr)-1]
					}
					out = append(out, subr...)
				}
				stack = nil
			}
			i++
			continue
		}
		if b == 11 && depth > 0 {
			return out
		}
		out = append(out, b)
		i++
		op := int(b)
		if b == 12 && i < len(data) {
			out = append(out, data[i])
			op = 1200 + int(data[i])
			i++
		}
		switch op {
		case 1, 3, 18, 23:
			hintCount += len(stack) / 2
		case 19, 20:
			hintCount += len(stack) / 2
			maskBytes := (hintCount + 7) / 8
			if i+maskBytes > len(data) {
				maskBytes = len(data) - i
			}
			out = append(out, data[i:i+maskBytes]...)
			i += maskBytes
		}
		stack = nil
	}
	return out
}

// removeType2Hints 移除 Type2 CharString 的 hint 指令
// 入参: data CharString数据
// 返回: []byte 移除hint后的CharString数据
func removeType2Hints(data []byte) []byte {
	out := make([]byte, 0, len(data))
	var stack []type2Operand
	hintCount := 0
	for i := 0; i < len(data); {
		b := data[i]
		if b == 28 && i+2 < len(data) {
			start := len(out)
			out = append(out, data[i:i+3]...)
			stack = append(stack, type2Operand{value: int(parseShortInt(data, i)), outStart: start})
			i += 3
			continue
		}
		if b >= 32 && b <= 246 {
			start := len(out)
			out = append(out, b)
			stack = append(stack, type2Operand{value: int(parseNumberType2(data, i)), outStart: start})
			i++
			continue
		}
		if b >= 247 && b <= 254 && i+1 < len(data) {
			start := len(out)
			out = append(out, data[i:i+2]...)
			stack = append(stack, type2Operand{value: int(parseNumberType2(data, i)), outStart: start})
			i += 2
			continue
		}
		if b == 255 && i+4 < len(data) {
			start := len(out)
			out = append(out, data[i:i+5]...)
			stack = append(stack, type2Operand{value: int(parseNumberType2(data, i)), outStart: start})
			i += 5
			continue
		}
		op := int(b)
		opLen := 1
		if b == 12 && i+1 < len(data) {
			op = 1200 + int(data[i+1])
			opLen = 2
		}
		switch op {
		case 1, 3, 18, 23:
			if len(stack) > 0 {
				out = out[:stack[0].outStart]
			}
			hintCount += len(stack) / 2
			stack = nil
			i += opLen
			continue
		case 19, 20:
			if len(stack) > 0 {
				out = out[:stack[0].outStart]
			}
			hintCount += len(stack) / 2
			i += opLen
			maskBytes := (hintCount + 7) / 8
			if i+maskBytes > len(data) {
				maskBytes = len(data) - i
			}
			i += maskBytes
			stack = nil
			continue
		}
		out = append(out, data[i:i+opLen]...)
		i += opLen
		stack = nil
	}
	return out
}

// cffSubrBias 获取 Type2 子程序偏移
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

// parseCFFWidths 从 CFF 数据中解析 Glyph 宽度
// 入参: data CFF数据, numGlyphs 字形数量
// 返回: []uint16 宽度列表, error 错误信息
func parseCFFWidths(data []byte, numGlyphs int) ([]uint16, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("data too short")
	}
	hdrSize := int(data[2])
	offset := hdrSize
	_, sz := getCFFIndexCount(data, offset)
	offset += sz
	topDictData, sz := getCFFIndexData(data, offset)
	offset += sz
	if topDictData == nil {
		return nil, fmt.Errorf("failed to read top dict")
	}
	topDict := parseCFFDict(topDictData)
	_, sz = getCFFIndexCount(data, offset)
	offset += sz
	_, sz = getCFFIndexCount(data, offset)
	offset += sz
	var nominalWidthX float64 = 0
	var defaultWidthX float64 = 0
	if vals, ok := topDict[18]; ok && len(vals) == 2 {
		privSize := int(vals[0])
		privOff := int(vals[1])
		if privSize > 0 && privOff+privSize <= len(data) {
			privData := data[privOff : privOff+privSize]
			privDict := parseCFFDict(privData)
			if v, ok := privDict[20]; ok && len(v) > 0 {
				defaultWidthX = v[0]
			}
			if v, ok := privDict[21]; ok && len(v) > 0 {
				nominalWidthX = v[0]
			}
		}
	}
	if vals, ok := topDict[17]; ok && len(vals) > 0 {
		charStrOff := int(vals[0])
		count, _ := getCFFIndexCount(data, charStrOff)
		limit := count
		if numGlyphs < limit {
			limit = numGlyphs
		}
		widths := make([]uint16, numGlyphs)
		for i := range widths {
			widths[i] = uint16(defaultWidthX)
		}
		for i := 0; i < limit; i++ {
			offSize := int(data[charStrOff+2])
			p1 := charStrOff + 3 + i*offSize
			p2 := p1 + offSize
			off1 := readCFFOffset(data, p1, offSize)
			off2 := readCFFOffset(data, p2, offSize)
			dataStartBase := charStrOff + 3 + (count+1)*offSize
			start := dataStartBase + (off1 - 1)
			length := off2 - off1
			if start < 0 || start+length > len(data) {
				widths[i] = uint16(defaultWidthX)
				continue
			}
			csData := data[start : start+length]
			w := scanCharStringWidth(csData, nominalWidthX, defaultWidthX)
			widths[i] = uint16(w)
		}
		return widths, nil
	}
	return nil, fmt.Errorf("no charstrings")
}

// scanCharStringWidth 扫描 CharString 获取宽度
// 入参: data CharString数据, nominal, def 默认宽度值
// 返回: float64 宽度值
func scanCharStringWidth(data []byte, nominal, def float64) float64 {
	stackDepth := 0
	i := 0
	firstVal := 0.0
	for i < len(data) {
		b := data[i]
		if b <= 31 {
			if b == 28 {
				i += 3
				stackDepth++
				if stackDepth == 1 {
					firstVal = parseShortInt(data, i-3)
				}
			} else if b == 29 {
				i += 5
				stackDepth++
			} else if b == 12 {
				i += 2
				if stackDepth%2 != 0 {
					return nominal + firstVal
				}
				return def
			} else if b == 19 || b == 20 {
				if stackDepth%2 != 0 {
					return nominal + firstVal
				}
				return def
			} else {
				if stackDepth%2 != 0 {
					return nominal + firstVal
				}
				return def
			}
		} else {
			stackDepth++
			if stackDepth == 1 {
				firstVal = parseNumberType2(data, i)
			}
			if b >= 32 && b <= 246 {
				i++
			} else if b >= 247 && b <= 250 {
				i += 2
			} else if b >= 251 && b <= 254 {
				i += 2
			} else if b == 255 {
				i += 5
			}
		}
	}
	return def
}

// parseShortInt 解析短整数 (Type 2 CharString)
// 入参: data 数据, idx 索引
// 返回: float64 浮点值
func parseShortInt(data []byte, idx int) float64 {
	return float64(int16(binary.BigEndian.Uint16(data[idx+1:])))
}

// parseNumberType2 解析 Number (Type 2)
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

// getCFFCharsetInfo 读取 CFF 字符集和 ROS 信息
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
	charsetOff := 0
	if vals, ok := td[15]; ok && len(vals) > 0 {
		charsetOff = int(vals[0])
	}
	sids := make([]int, numGlyphs)
	sids[0] = 0
	if charsetOff > 2 {
		sidsParsed := parseCFFCharset(data, charsetOff, numGlyphs)
		copy(sids[1:], sidsParsed)
	} else if charsetOff == 0 {
		count := 228
		if numGlyphs-1 < count {
			count = numGlyphs - 1
		}
		for i := 1; i <= count; i++ {
			sids[i] = i
		}
	} else {
		return nil, "", "", 0, false
	}
	return sids, registry, ordering, stringIndexOff, true
}

// getCmapFromCFF 从 CFF 数据中恢复 Unicode 映射
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

// getCFFCIDRuneMap 获取 CID 到包装字体字符的映射
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

// getCFFData 获取字体中的 CFF 数据
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

// parseCFFCharset 解析 CFF 字符集并返回 SID 列表
// 入参: data CFF数据, offset 偏移量, numGlyphs 字形数量
// 返回: []int SID列表
func parseCFFCharset(data []byte, offset int, numGlyphs int) []int {
	if offset >= len(data) {
		return nil
	}
	format := data[offset]
	var sids []int
	count := numGlyphs - 1
	pos := offset + 1
	switch format {
	case 0:
		for i := 0; i < count && pos+2 <= len(data); i++ {
			sid := int(binary.BigEndian.Uint16(data[pos:]))
			sids = append(sids, sid)
			pos += 2
		}
	case 1:
		for len(sids) < count && pos+3 <= len(data) {
			first := int(binary.BigEndian.Uint16(data[pos:]))
			nLeft := int(data[pos+2])
			pos += 3
			for j := 0; j <= nLeft; j++ {
				sids = append(sids, first+j)
			}
		}
	case 2:
		for len(sids) < count && pos+4 <= len(data) {
			first := int(binary.BigEndian.Uint16(data[pos:]))
			nLeft := int(binary.BigEndian.Uint16(data[pos+2:]))
			pos += 4
			for j := 0; j <= nLeft; j++ {
				sids = append(sids, first+j)
			}
		}
	}
	if len(sids) > count {
		sids = sids[:count]
	}
	return sids
}

// readStringIndexItem 读取 CFF 字符串索引项
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

// getCFFROS 读取 CID 字体 ROS 信息
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

// getCFFSIDString 读取 CFF SID 字符串
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

// adobeGB1CIDToUnicode 将 Adobe-GB1 CID 转为 Unicode
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
	if strings.HasPrefix(name, "uni") && len(name) == 7 {
		hexStr := strings.ToUpper(name[3:])
		if val, err := strconv.ParseInt(hexStr, 16, 32); err == nil {
			return rune(val)
		}
	}
	if strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7 && !strings.HasPrefix(name, "uni") {
		hexStr := strings.ToUpper(name[1:])
		if val, err := strconv.ParseInt(hexStr, 16, 32); err == nil {
			return rune(val)
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

// cffStandardStrings CFF 标准字符串表
var cffStandardStrings = []string{
	".notdef", "space", "exclam", "quotedbl", "numbersign", "dollar", "percent", "ampersand", "quoteright", "parenleft", "parenright", "asterisk", "plus", "comma", "hyphen", "period", "slash", "zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "colon", "semicolon", "less", "equal", "greater", "question", "at", "A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z", "bracketleft", "backslash", "bracketright", "asciicircum", "underscore", "quoteleft", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z", "braceleft", "bar", "braceright", "asciitilde", "exclamdown", "cent", "sterling", "fraction", "yen", "florin", "section", "currency", "quotesingle", "quotedblleft", "quotedblright", "guillemotleft", "guillemotright", "dagger", "daggerdbl", "fi", "fl", "endash", "emdash", "paragraph", "bullet", "quotesinglbase", "quotedblbase", "second", "circumflex", "breve", "dotaccent", "dieresis", "grave", "ring", "cedilla", "hungarumlaut", "ogonek", "caron", "emspace",
	"AE", "ordfeminine", "Lslash", "Oslash", "OE", "ordmasculine", "ae", "dotlessi", "lslash", "oslash", "oe", "germandbls", "onesuperior", "logicalnot", "mu", "trademark", "Eth", "onehalf", "plusminus", "Thorn", "onequarter", "divide", "brokenbar", "degree", "thorn", "threequarters", "twosuperior", "registered", "minus", "eth", "multiply", "threesuperior", "copyright", "Aacute", "Acircumflex", "Adieresis", "Agrave", "Aring", "Atilde", "Ccedilla", "Eacute", "Ecircumflex", "Edieresis", "Egrave", "Iacute", "Icircumflex", "Idieresis", "Igrave", "Ntilde", "Oacute", "Ocircumflex", "Odieresis", "Ograve", "Otilde", "Scaron", "Uacute", "Ucircumflex", "Udieresis", "Ugrave", "Yacute", "Ydieresis", "Zcaron", "aacute", "acircumflex", "adieresis", "agrave", "aring", "atilde", "ccedilla", "eacute", "ecircumflex", "edieresis", "egrave", "iacute", "icircumflex", "idieresis", "igrave", "ntilde", "oacute", "ocircumflex", "odieresis", "ograve", "otilde", "scaron", "uacute", "ucircumflex", "udieresis", "ugrave", "yacute", "ydieresis", "zcaron", "exclamsmall", "Hungarumlautsmall", "dollaroldstyle", "dollarsuperior", "ampersandsmall", "Acutesmall", "parenleftsuperior", "parenrightsuperior", "2dotlead", "nbspace", "1dotlead", "zerooldstyle", "oneoldstyle", "twooldstyle", "threeoldstyle", "fouroldstyle", "fiveoldstyle", "sixoldstyle", "sevenoldstyle", "eightoldstyle", "nineoldstyle", "commasuperior", "threequartersemdash", "periodsuperior", "questionsmall", "asuperior", "bsuperior", "centsuperior", "dsuperior", "esuperior", "isuperior", "lsuperior", "msuperior", "nsuperior", "osuperior", "rsuperior", "ssuperior", "tsuperior", "ff", "ffi", "ffl", "parenleftinferior", "parenrightinferior", "Circumflexsmall", "hyphensuperior", "Gravesmall", "Asmall", "Bsmall", "Csmall", "Dsmall", "Esmall", "Fsmall", "Gsmall", "Hsmall", "Ismall", "Jsmall", "Ksmall", "Lsmall", "Msmall", "Nsmall", "Osmall", "Psmall", "Qsmall", "Rsmall", "Ssmall", "Tsmall", "Usmall", "Vsmall", "Wsmall", "Xsmall", "Ysmall", "Zsmall", "colonmonetary", "onefitted", "rupiah", "Tildesmall", "exclamdownsmall", "centoldstyle", "Lslashsmall", "Scaronsmall", "Zcaronsmall", "Dieresissmall", "Brevesmall", "Caronsmall", "Dotaccentsmall", "Macronsmall", "figuredash", "hypheninferior", "Ogoneksmall", "Ringsmall", "Cedillasmall", "questiondownsmall", "oneeighth", "threeeighths", "fiveeighths", "seveneighths", "onethird", "twothirds", "zerosuperior", "foursuperior", "fivesuperior", "sixsuperior", "sevensuperior", "eightsuperior", "ninesuperior", "zeroinferior", "oneinferior", "twoinferior", "threeinferior", "fourinferior", "fiveinferior", "sixinferior", "seveninferior", "eightinferior", "nineinferior", "centinferior", "dollarinferior", "periodinferior", "commainferior", "Agravesmall", "Aacutesmall", "Acircumflexsmall", "Atildesmall", "Adieresissmall", "Aringsmall", "AEsmall", "Ccedillasmall", "Egravesmall", "Eacutesmall", "Ecircumflexsmall", "Edieresissmall", "Igravesmall", "Iacutesmall", "Icircumflexsmall", "Idieresissmall", "Ethsmall", "Ntildesmall", "Ogravesmall", "Oacutesmall", "Ocircumflexsmall", "Otildesmall", "Odieresissmall", "OEsmall", "Oslashsmall", "Ugravesmall", "Uacutesmall", "Ucircumflexsmall", "Udieresissmall", "Yacutesmall", "Thornsmall", "Ydieresissmall", "001.000", "001.001", "001.002", "001.003", "Black", "Bold", "Book", "Light", "Medium", "Regular", "Roman", "Semibold",
}
