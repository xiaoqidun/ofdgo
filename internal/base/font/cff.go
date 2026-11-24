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

package font

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// cffDict 使用float64存储所有数值，以统一处理整数和实数
type cffDict map[int][]float64

// WrapCFFToOTF 将CFF裸数据包装为OpenType字体格式
// 入参: cffData CFF字体数据
// 返回: []byte OTF字体数据, map[rune]uint16 字符映射, error 错误信息
func WrapCFFToOTF(cffData []byte) ([]byte, map[rune]uint16, error) {
	numGlyphs, err := parseCFFAndCountGlyphs(cffData)
	if err != nil {
		return nil, nil, err
	}
	mapping := getCmapFromCFF(cffData, int(numGlyphs))
	sanitized, err := SanitizeCFF(cffData)
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
	_, nameSize := getCFFIndexCount(cffData, int(cffData[2]))
	topData, _ := getCFFIndexData(cffData, int(cffData[2])+nameSize)
	top, err := readCFFDict(topData)
	if err != nil {
		return nil, nil, err
	}
	matrix, err := readCFFMatrix(top, [6]float64{0.001, 0, 0, 0.001, 0, 0})
	if err != nil {
		return nil, nil, err
	}
	unitsPerEm := cffUnitsPerEm(matrix)
	if mapping == nil {
		mapping = make(map[rune]uint16)
	}
	maxGlyphs := uint16(min(numGlyphs, 0xFFFF))
	tables := make(map[string][]byte, 9)
	tables["CFF "] = cffData
	tables["head"] = buildHeadTable(unitsPerEm)
	tables["hhea"] = buildHheaTable(maxGlyphs)
	tables["maxp"] = buildCFFMaxpTable(maxGlyphs)
	tables["OS/2"] = buildOS2Table()
	tables["name"] = buildNameTable()
	tables["post"] = BuildPostTable()
	tables["hmtx"] = buildHmtxTable(widths)
	tables["cmap"] = buildPackedCmapTable(maxGlyphs, mapping)
	AddPackedGlyphMapping(mapping, maxGlyphs)
	data, err := SerializeOTF(tables)
	return data, mapping, err
}

// NormalizeCFFCharstringsAt 统一CFF设计坐标，现有OpenType字体使用head表的设计单位
// 入参: data CFF字体数据, unitsPerEm 设计单位，0时由字体矩阵确定
// 返回: []byte 标准化后的CFF数据, error 错误信息
func NormalizeCFFCharstringsAt(data []byte, unitsPerEm uint16) ([]byte, error) {
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
	dict, err := readCFFDict(topData)
	if err != nil {
		return nil, err
	}
	charStart, err := cffDictOffset(data, dict, 17)
	if err != nil {
		return nil, err
	}
	_, charSize := getCFFIndexCount(data, charStart)
	if charStart < topStart+topSize || charSize == 0 || charStart+charSize > len(data) {
		return nil, fmt.Errorf("invalid CFF charstrings")
	}
	matrix, err := readCFFMatrix(dict, [6]float64{0.001, 0, 0, 0.001, 0, 0})
	if err != nil {
		return nil, err
	}
	if unitsPerEm == 0 {
		unitsPerEm = cffUnitsPerEm(matrix)
	}
	if unitsPerEm < 16 || unitsPerEm > 16384 {
		return nil, fmt.Errorf("invalid CFF design units")
	}
	var transform *[6]float64
	for i := range matrix {
		matrix[i] *= float64(unitsPerEm)
	}
	if matrix != [6]float64{1, 0, 0, 1, 0, 0} {
		transform = &matrix
	}
	chars, changed, err := normalizeType2ProgramsMatrix(data, transform)
	if err != nil {
		return nil, err
	}
	if _, cid := dict[1230]; !cid {
		for op := 1231; op <= 1238; op++ {
			if _, exists := dict[op]; exists {
				delete(dict, op)
				changed = true
			}
		}
	}
	if transform != nil {
		scale := 1 / float64(unitsPerEm)
		dict[1207] = []float64{scale, 0, 0, scale, 0, 0}
		if _, cid := dict[1230]; !cid {
			dict[18] = []float64{0, 0}
		}
		if err := transformCFFBounds(dict, matrix); err != nil {
			return nil, err
		}
		changed = true
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
	if ros, cid := dict[1230]; cid {
		fdArrOff, err := cffDictOffset(data, dict, 1236)
		if err != nil {
			return nil, err
		}
		fds, _, err := readType2Index(data, fdArrOff)
		if err != nil || len(fds) != 1 {
			return nil, fmt.Errorf("CID CFF requires FD normalization")
		}
		fdDict, err := readCFFDict(fds[0])
		if err != nil {
			return nil, err
		}
		var privData, localData []byte
		if transform == nil && len(fdDict[18]) == 2 && fdDict[18][0] > 0 {
			pDict, err := readType2Private(data, fdDict[18], &type2Font{})
			if err != nil {
				return nil, err
			}
			if values := pDict[19]; len(values) != 0 {
				start := int(fdDict[18][1]) + int(values[0])
				_, end, err := readType2Index(data, start)
				if err != nil {
					return nil, err
				}
				localData = data[start:end]
				pDict[19] = []float64{0}
				for {
					length := len(encodeCFFDict(pDict))
					if pDict[19][0] == float64(length) {
						break
					}
					pDict[19][0] = float64(length)
				}
			}
			privData = encodeCFFDict(pDict)
		}
		_, stringEnd, err := readType2Index(data, topStart+topSize)
		if err != nil {
			return nil, err
		}
		_, globalEnd, err := readType2Index(data, stringEnd)
		if err != nil {
			return nil, err
		}
		encodedChars, err := encodeCFFIndexChecked(chars)
		if err != nil {
			return nil, err
		}
		return encodeSanitizedCIDCFF(data[:headerEnd], data[headerEnd:topStart], dict, slices.Clone(ros), data[topStart+topSize:stringEnd], data[stringEnd:globalEnd], len(chars), encodedChars, privData, localData)
	}
	return replaceCFFCharstrings(data, dict, topStart, topSize, charStart, charSize, chars, explicitCharset)
}

// SanitizeCFF 尝试清洗CFF数据，转换CID字体并合并FontMatrix
// 入参: data 原始CFF数据
// 返回: []byte 清洗后的CFF数据, error 错误信息
func SanitizeCFF(data []byte) ([]byte, error) {
	if !isBareCFFData(data) {
		return nil, fmt.Errorf("invalid CFF header")
	}
	hdrSize := int(data[2])
	offset := hdrSize
	names, end, err := readType2Index(data, offset)
	if err != nil {
		return nil, err
	}
	if len(names) != 1 {
		return nil, fmt.Errorf("multi-font cff not supported")
	}
	nameIndexData := data[offset:end]
	tops, end, err := readType2Index(data, end)
	if err != nil {
		return nil, err
	}
	if len(tops) != 1 {
		return nil, fmt.Errorf("top dict count != 1")
	}
	_, stringEnd, err := readType2Index(data, end)
	if err != nil {
		return nil, err
	}
	stringIndexData := data[end:stringEnd]
	_, globalEnd, err := readType2Index(data, stringEnd)
	if err != nil {
		return nil, err
	}
	globalSubrIndexData := data[stringEnd:globalEnd]
	topDict, err := readCFFDict(tops[0])
	if err != nil {
		return nil, err
	}
	kind, err := cffCharstringType(topDict)
	if err != nil {
		return nil, err
	}
	if _, isCID := topDict[1230]; !isCID {
		result := data
		if kind == 1 {
			result, err = normalizeCFFType1(data, topDict)
			if err != nil {
				return nil, err
			}
		} else {
			private, err := readType2Private(data, topDict[18], &type2Font{})
			if err != nil {
				return nil, err
			}
			count := len(private)
			if err := normalizeCFFPrivate(private); err != nil {
				return nil, err
			}
			if len(private) != count {
				font, err := readType2Font(data)
				if err != nil {
					return nil, err
				}
				result, err = rebuildNamedCFF(data, font, font.chars, nil, false)
				if err != nil {
					return nil, err
				}
			}
		}
		if int(result[2]) != 4 {
			return normalizeCFFHeader(result)
		}
		return result, nil
	}
	fdArrOff, err := cffDictOffset(data, topDict, 1236)
	if err != nil {
		return nil, err
	}
	fdItems, _, err := readType2Index(data, fdArrOff)
	if err != nil {
		return nil, err
	}
	fdCount := len(fdItems)
	if fdCount != 1 {
		return sanitizeMultiFDCFF(data, hdrSize, nameIndexData, topDict, stringIndexData, globalSubrIndexData, fdArrOff, fdCount)
	}
	fontDict, err := readCFFDict(fdItems[0])
	if err != nil {
		return nil, err
	}
	fdMatrix, err := readCFFMatrix(fontDict, [6]float64{1, 0, 0, 1, 0, 0})
	if err != nil {
		return nil, err
	}
	_, topHasMatrix := topDict[1207]
	_, fdHasMatrix := fontDict[1207]
	if kind == 1 || fdMatrix != [6]float64{1, 0, 0, 1, 0, 0} || !topHasMatrix && fdHasMatrix {
		return sanitizeMultiFDCFF(data, hdrSize, nameIndexData, topDict, stringIndexData, globalSubrIndexData, fdArrOff, fdCount)
	}
	charStringsOff, err := cffDictOffset(data, topDict, 17)
	if err != nil {
		return nil, err
	}
	chars, charEnd, err := readType2Index(data, charStringsOff)
	if err != nil || len(chars) == 0 {
		return nil, fmt.Errorf("invalid CFF charstrings")
	}
	selectOffset, err := cffDictOffset(data, topDict, 1237)
	if err != nil {
		return nil, err
	}
	if _, err := readType2FDSelect(data, selectOffset, len(chars), 1); err != nil {
		return nil, err
	}
	pDict, err := readType2Private(data, fontDict[18], &type2Font{})
	if err != nil {
		return nil, err
	}
	var localSubrData []byte
	if values := pDict[19]; len(values) != 0 {
		start := int(fontDict[18][1]) + int(values[0])
		_, end, err := readType2Index(data, start)
		if err != nil {
			return nil, err
		}
		localSubrData = data[start:end]
	}
	charStringsData := data[charStringsOff:charEnd]
	ros := slices.Clone(topDict[1230])
	for op := 1230; op <= 1238; op++ {
		delete(topDict, op)
	}
	delete(topDict, 15)
	delete(topDict, 16)
	var finalPrivData []byte
	if len(pDict) > 0 {
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
	if len(chars)-1 > 64999 && len(ros) == 3 {
		return encodeSanitizedCIDCFF([]byte{1, 0, 4, 4}, nameIndexData, topDict, ros, stringIndexData, globalSubrIndexData, len(chars), charStringsData, finalPrivData, localSubrData)
	}
	stringIndexData, charsetData, err := sanitizedCFFCharset(stringIndexData, len(chars))
	if err != nil {
		return nil, err
	}
	return encodeSanitizedCFF([]byte{1, 0, 4, 4}, nameIndexData, topDict, stringIndexData, globalSubrIndexData, charsetData, charStringsData, finalPrivData, localSubrData)
}

// GetCFFCIDRuneMap 获取CID到包装字体字符的映射
// 入参: data CFF或OpenType字体数据
// 返回: map[uint16]rune CID映射
func GetCFFCIDRuneMap(data []byte) map[uint16]rune {
	cffData := getCFFData(data)
	if cffData == nil {
		return nil
	}
	numGlyphs, err := parseCFFAndCountGlyphs(cffData)
	if err != nil {
		return nil
	}
	sids, registry, ordering, stringIndexOff, ok := getCFFCharsetInfo(cffData, numGlyphs)
	if !ok || registry == "" {
		return nil
	}
	mapping := cffCharsetMapping(cffData, sids, registry, ordering, stringIndexOff)
	var gidRunes map[uint16]rune
	if len(mapping) != 0 {
		gidRunes = make(map[uint16]rune, len(mapping))
		for run, gid := range mapping {
			if run != PackedGlyphRune(gid) {
				gidRunes[gid] = run
			}
		}
	}
	result := make(map[uint16]rune, max(0, len(sids)-1))
	for gid, cid := range sids {
		if gid == 0 || cid < 0 || cid > 0xFFFF {
			continue
		}
		if run, ok := gidRunes[uint16(gid)]; ok {
			result[uint16(cid)] = run
		} else {
			result[uint16(cid)] = PackedGlyphRune(uint16(gid))
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// CFFCIDToGID 返回CID编码CFF字体的CID到GID反查表，非CID字体返回nil
// 入参: data CFF或OpenType字体数据
// 返回: map[uint16]uint16 CID到GID映射
func CFFCIDToGID(data []byte) map[uint16]uint16 {
	return cffCIDToGIDFromCFF(getCFFData(data))
}

// cffCIDToGIDFromCFF 从已提取的CFF表构造CID到GID反查表，非CID字体返回nil
// 入参: cffData 裸CFF数据
// 返回: map[uint16]uint16 CID到GID映射
func cffCIDToGIDFromCFF(cffData []byte) map[uint16]uint16 {
	if cffData == nil {
		return nil
	}
	numGlyphs, err := parseCFFAndCountGlyphs(cffData)
	if err != nil || numGlyphs <= 0 {
		return nil
	}
	sids, registry, _, _, ok := getCFFCharsetInfo(cffData, numGlyphs)
	if !ok || registry == "" {
		return nil
	}
	result := make(map[uint16]uint16, len(sids))
	for gid, cid := range sids {
		if gid > 0 && gid <= 0xFFFF && cid >= 0 && cid <= 0xFFFF {
			result[uint16(cid)] = uint16(gid)
		}
	}
	return result
}

// normalizeCFFCharstrings 展开预定义字符集并规范化Type2提示编码，保留轮廓和字宽
// 入参: data CFF字体数据
// 返回: []byte 标准化后的CFF数据, error 错误信息
func normalizeCFFCharstrings(data []byte) ([]byte, error) {
	return NormalizeCFFCharstringsAt(data, 0)
}

// replaceCFFCharstrings 替换字形索引并调整后续数据及顶层字典的偏移
// 入参: data 原始字体, dict 输出字典, topStart 顶层索引起点, topSize 顶层索引大小, charStart 字形索引起点, charSize 字形索引大小, chars 字形程序, explicitCharset 追加字符集
// 返回: []byte 重建后的字体数据, error 索引编码错误
func replaceCFFCharstrings(data []byte, dict cffDict, topStart, topSize, charStart, charSize int, chars [][]byte, explicitCharset []byte) ([]byte, error) {
	encodedChars, err := encodeCFFIndexChecked(chars)
	if err != nil {
		return nil, err
	}
	topEnd := topStart + topSize
	topData, _ := getCFFIndexData(data, topStart)
	originalDict := parseCFFDict(topData)
	newTop, err := encodeCFFIndexChecked([][]byte{encodeCFFDict(dict)})
	if err != nil {
		return nil, err
	}
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
		if values := dict[18]; len(values) == 2 && values[0] != 0 {
			original := originalDict[18]
			offset := int(original[1])
			if offset >= charStart+charSize {
				dict[18] = []float64{values[0], float64(offset + shiftTail)}
			} else if offset >= topEnd {
				dict[18] = []float64{values[0], float64(offset + shift)}
			}
		}
		updated, err := encodeCFFIndexChecked([][]byte{encodeCFFDict(dict)})
		if err != nil {
			return nil, err
		}
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

// normalizeCFFPrivate 移除废弃的加粗阈值及未加密标记，拒绝加密字形程序
// 入参: dict 私有字典
// 返回: error 不支持的旧版行为
func normalizeCFFPrivate(dict cffDict) error {
	if values, ok := dict[1215]; ok {
		if len(values) != 1 || !finite(values[0]) {
			return fmt.Errorf("invalid legacy CFF bold threshold")
		}
		delete(dict, 1215)
	}
	if values, ok := dict[1216]; ok {
		if len(values) != 1 || values[0] != -1 {
			return fmt.Errorf("unsupported legacy CFF private operator 1216")
		}
		delete(dict, 1216)
	}
	return nil
}

// sanitizeMultiFDCFF 清洗多FD的CID CFF数据
// 入参: data 原始CFF数据, hdrSize 头部大小, nameIndexData 名称索引, topDict 顶层字典, stringIndexData 字符串索引, globalSubrIndexData 全局子程序索引, fdArrOff FDArray偏移, fdCount FD数量
// 返回: []byte 清洗后的CFF数据, error 错误信息
func sanitizeMultiFDCFF(data []byte, hdrSize int, nameIndexData []byte, topDict cffDict, stringIndexData []byte, globalSubrIndexData []byte, fdArrOff int, fdCount int) ([]byte, error) {
	kind, err := cffCharstringType(topDict)
	if err != nil {
		return nil, err
	}
	charStringsOff, err := cffDictOffset(data, topDict, 17)
	if err != nil {
		return nil, err
	}
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
	var type1Fonts []*Type1Program
	if kind == 1 {
		type1Fonts = make([]*Type1Program, fdCount)
	}
	privateDicts := make([]cffDict, fdCount)
	matrices := make([][6]float64, fdCount)
	transformed := false
	topMatrix, err := readCFFMatrix(topDict, [6]float64{0.001, 0, 0, 0.001, 0, 0})
	if err != nil {
		return nil, err
	}
	units := cffUnitsPerEm(topMatrix)
	_, topHasMatrix := topDict[1207]
	for i := range topMatrix {
		topMatrix[i] *= float64(units)
	}
	for fd, item := range fdItems {
		fonts[fd] = type2Font{data: data, chars: charStrings, globals: globalSubrs, seed: 1}
		fontDict, dictErr := readCFFDict(item)
		if dictErr != nil {
			return nil, dictErr
		}
		fdMatrix, matrixErr := readCFFMatrix(fontDict, [6]float64{1, 0, 0, 1, 0, 0})
		if matrixErr != nil {
			return nil, fmt.Errorf("CFF font dictionary %d: %w", fd, matrixErr)
		}
		baseMatrix := topMatrix
		if _, fdHasMatrix := fontDict[1207]; !topHasMatrix && fdHasMatrix {
			baseMatrix = [6]float64{float64(units), 0, 0, float64(units), 0, 0}
		}
		matrices[fd], err = readCFFMatrix(cffDict{1207: multiplyAffine(baseMatrix[:], fdMatrix[:])}, [6]float64{})
		if err != nil {
			return nil, fmt.Errorf("CFF font dictionary %d: %w", fd, err)
		}
		transformed = transformed || matrices[fd] != [6]float64{1, 0, 0, 1, 0, 0}
		privateDicts[fd], err = readType2Private(data, fontDict[18], &fonts[fd])
		if err != nil {
			return nil, fmt.Errorf("CFF font dictionary %d: %w", fd, err)
		}
		if err := normalizeCFFPrivate(privateDicts[fd]); err != nil {
			return nil, fmt.Errorf("CFF font dictionary %d: %w", fd, err)
		}
		if kind == 1 {
			type1Fonts[fd], err = readCFFType1Program(data, charStrings, fontDict[18], true)
			if err != nil {
				return nil, fmt.Errorf("CFF font dictionary %d: %w", fd, err)
			}
			fonts[fd].def, fonts[fd].nominal = 0, 0
		}
	}
	target := &fonts[0]
	if transformed {
		target = &type2Font{}
	}
	inlined := make([][]byte, len(charStrings))
	var stack [48]float64
	for gid, cs := range charStrings {
		font := &fonts[fdSelect[gid]]
		if kind == 1 {
			cs, err = type1Fonts[fdSelect[gid]].outline(type1Glyph{name: fmt.Sprintf("gid%d", gid), data: cs})
			if err != nil {
				return nil, err
			}
		}
		var output bytes.Buffer
		steps := 0
		state := type2State{font: font, args: stack[:0], widthTarget: target, width: font.def, seed: font.seed + uint64(gid), steps: &steps, output: &output, stripHints: true}
		if matrices[fdSelect[gid]] != [6]float64{1, 0, 0, 1, 0, 0} {
			state.matrix = &matrices[fdSelect[gid]]
		}
		if len(cs) == 0 {
			cs = []byte{14}
		}
		if _, err := state.run(cs, 0); err != nil {
			return nil, fmt.Errorf("CFF glyph %d: %w", gid, err)
		}
		inlined[gid] = output.Bytes()
	}
	ros := slices.Clone(topDict[1230])
	for op := 1230; op <= 1238; op++ {
		delete(topDict, op)
	}
	delete(topDict, 15)
	delete(topDict, 16)
	delete(topDict, 1206)
	if transformed {
		scale := 1 / float64(units)
		topDict[1207] = []float64{scale, 0, 0, scale, 0, 0}
		if err := transformCFFBounds(topDict, topMatrix); err != nil {
			return nil, err
		}
	}
	privateDict := make(cffDict)
	for _, pd := range privateDicts[:min(1, len(privateDicts))] {
		for k, v := range pd {
			privateDict[k] = v
		}
	}
	delete(privateDict, 19)
	if transformed || kind == 1 {
		privateDict = cffDict{20: {0}, 21: {0}}
	}
	if err := normalizeCFFPrivate(privateDict); err != nil {
		return nil, err
	}
	finalPrivData := encodeCFFDict(privateDict)
	charStringsData, err := encodeCFFIndexChecked(inlined)
	if err != nil {
		return nil, err
	}
	if len(inlined)-1 > 64999 && len(ros) == 3 {
		return encodeSanitizedCIDCFF([]byte{1, 0, 4, 4}, nameIndexData, topDict, ros, stringIndexData, []byte{0, 0}, len(inlined), charStringsData, finalPrivData, nil)
	}
	stringIndexData, charsetData, err := sanitizedCFFCharset(stringIndexData, len(inlined))
	if err != nil {
		return nil, err
	}
	return encodeSanitizedCFF([]byte{1, 0, 4, 4}, nameIndexData, topDict, stringIndexData, []byte{0, 0}, charsetData, charStringsData, finalPrivData, nil)
}

// normalizeCFFHeader 将带扩展字节的合法非CID CFF头部规范化为标准4字节头并重算顶层偏移
// 入参: data 原始非CID CFF数据
// 返回: []byte 规范化后的CFF数据, error 错误信息
func normalizeCFFHeader(data []byte) ([]byte, error) {
	if !isBareCFFData(data) {
		return nil, fmt.Errorf("invalid CFF header")
	}
	hdrSize := int(data[2])
	_, nameSize := getCFFIndexCount(data, hdrSize)
	if nameSize <= 0 {
		return nil, fmt.Errorf("invalid CFF name index")
	}
	topStart := hdrSize + nameSize
	topData, topSize := getCFFIndexData(data, topStart)
	if topData == nil || topStart+topSize > len(data) {
		return nil, fmt.Errorf("invalid CFF top dictionary")
	}
	originalDict, err := readCFFDict(topData)
	if err != nil {
		return nil, err
	}
	dict := make(cffDict, len(originalDict))
	for op, vals := range originalDict {
		dict[op] = append([]float64(nil), vals...)
	}
	topEnd := topStart + topSize
	newTop := encodeCFFIndex([][]byte{encodeCFFDict(dict)})
	for range 6 {
		shift := (4 - hdrSize) + (len(newTop) - topSize)
		for _, op := range []int{15, 16, 17} {
			values := originalDict[op]
			if len(values) != 1 || op == 15 && values[0] <= 2 || op == 16 && values[0] <= 1 {
				continue
			}
			offset := int(values[0])
			if offset >= topEnd {
				dict[op] = []float64{float64(offset + shift)}
			}
		}
		if values := originalDict[18]; len(values) == 2 && values[0] != 0 {
			offset := int(values[1])
			if offset >= topEnd {
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
	result := make([]byte, 0, 4+nameSize+len(newTop)+len(data)-topEnd)
	result = append(result, 1, 0, 4, 4)
	result = append(result, data[hdrSize:topStart]...)
	result = append(result, newTop...)
	result = append(result, data[topEnd:]...)
	return result, nil
}

// sanitizedCFFCharset 为转换后的非CID字体生成显式字符集并按需补充字符串索引
// 入参: stringIndexData 原始字符串索引, numGlyphs 字形总数
// 返回: []byte 字符串索引数据, []byte 字符集数据, error 超限或索引错误
func sanitizedCFFCharset(stringIndexData []byte, numGlyphs int) ([]byte, []byte, error) {
	if numGlyphs <= 1 {
		return stringIndexData, nil, nil
	}
	const maxCFFSID = 64999
	if numGlyphs-1 > maxCFFSID {
		return nil, nil, fmt.Errorf("CFF glyph count %d exceeds maximum SID %d", numGlyphs, maxCFFSID)
	}
	nLeft := numGlyphs - 2
	singleRange := []byte{2, 0, 1, byte(nLeft >> 8), byte(nLeft)}
	maxStd := len(cffStandardStrings) - 1
	if numGlyphs-1 <= maxStd {
		return stringIndexData, singleRange, nil
	}
	origStrings, _, err := readType2Index(stringIndexData, 0)
	if err != nil {
		return nil, nil, err
	}
	if len(origStrings) > maxCFFSID-maxStd {
		return nil, nil, fmt.Errorf("CFF string index exceeds maximum SID %d", maxCFFSID)
	}
	extendStrings := func(startSID, count int) ([]byte, error) {
		combined := make([][]byte, len(origStrings)+count)
		copy(combined, origStrings)
		for i := range count {
			combined[len(origStrings)+i] = strconv.AppendInt([]byte{'g'}, int64(startSID+i), 10)
		}
		return encodeCFFIndexChecked(combined)
	}
	if numGlyphs-1+len(origStrings) <= maxCFFSID {
		extended, err := extendStrings(maxStd+1, numGlyphs-1-maxStd)
		if err != nil {
			return nil, nil, err
		}
		if len(origStrings) == 0 {
			return extended, singleRange, nil
		}
		stdLeft := maxStd - 1
		first2 := maxStd + 1 + len(origStrings)
		nLeft2 := numGlyphs - 2 - maxStd
		return extended, []byte{2, 0, 1, byte(stdLeft >> 8), byte(stdLeft), byte(first2 >> 8), byte(first2), byte(nLeft2 >> 8), byte(nLeft2)}, nil
	}
	if need := numGlyphs - 1 - maxStd - len(origStrings); need > 0 {
		extended, err := extendStrings(maxStd+1+len(origStrings), need)
		if err != nil {
			return nil, nil, err
		}
		stringIndexData = extended
	}
	return stringIndexData, singleRange, nil
}

// encodeSanitizedCFF 按字典编码长度收敛偏移并组装规范化字体
// 入参: header 头部, names 名称索引, dict 顶层字典, strings 字符串索引, globals 全局子程序索引, charset 字符集, chars 字形索引, private 私有字典, locals 局部子程序索引
// 返回: []byte CFF字体数据, error 索引编码错误
func encodeSanitizedCFF(header, names []byte, dict cffDict, strings, globals, charset, chars, private, locals []byte) ([]byte, error) {
	if len(charset) > 0 {
		dict[15] = []float64{0}
	}
	dict[17] = []float64{0}
	dict[18] = []float64{float64(len(private)), 0}
	prefix := len(header) + len(names) + len(strings) + len(globals)
	var top []byte
	for {
		var err error
		top, err = encodeCFFIndexChecked([][]byte{encodeCFFDict(dict)})
		if err != nil {
			return nil, err
		}
		charsetOffset := prefix + len(top)
		charOffset := charsetOffset + len(charset)
		privateOffset := charOffset + len(chars)
		if (len(charset) == 0 || dict[15][0] == float64(charsetOffset)) && dict[17][0] == float64(charOffset) && dict[18][1] == float64(privateOffset) {
			break
		}
		if len(charset) > 0 {
			dict[15][0] = float64(charsetOffset)
		}
		dict[17][0] = float64(charOffset)
		dict[18][1] = float64(privateOffset)
	}
	var out bytes.Buffer
	out.Grow(prefix + len(top) + len(charset) + len(chars) + len(private) + len(locals))
	for _, part := range [][]byte{header, names, top, strings, globals, charset, chars, private, locals} {
		out.Write(part)
	}
	return out.Bytes(), nil
}

// encodeSanitizedCIDCFF 按单字体字典格式组装字形数超SID上限的标准CID CFF字体
// 入参: header 头部, names 名称索引, dict 顶层字典, ros 注册排序补充信息, strings 字符串索引, globals 全局子程序索引, numGlyphs 字形总数, chars 字形索引, private 私有字典, locals 局部子程序索引
// 返回: []byte CID CFF字体数据, error 字形数或偏移错误
func encodeSanitizedCIDCFF(header, names []byte, dict cffDict, ros []float64, strings, globals []byte, numGlyphs int, chars, private, locals []byte) ([]byte, error) {
	if numGlyphs < 2 || numGlyphs > 65535 {
		return nil, fmt.Errorf("invalid CID CFF glyph count %d", numGlyphs)
	}
	delete(dict, 18)
	dict[1230] = ros
	nLeft := numGlyphs - 2
	charset := []byte{2, 0, 1, byte(nLeft >> 8), byte(nLeft)}
	fdSelect := []byte{3, 0, 1, 0, 0, 0, byte(numGlyphs >> 8), byte(numGlyphs)}
	fdDict := cffDict{18: {float64(len(private)), 0}}
	prefix := len(header) + len(names) + len(strings) + len(globals)
	for range 16 {
		top, err := encodeCFFIndexChecked([][]byte{encodeCFFDict(dict)})
		if err != nil {
			return nil, err
		}
		fdArray, err := encodeCFFIndexChecked([][]byte{encodeCFFDict(fdDict)})
		if err != nil {
			return nil, err
		}
		charsetOff := prefix + len(top)
		selectOff := charsetOff + len(charset)
		charOff := selectOff + len(fdSelect)
		arrayOff := charOff + len(chars)
		privOff := arrayOff + len(fdArray)
		dict[15] = []float64{float64(charsetOff)}
		dict[1237] = []float64{float64(selectOff)}
		dict[17] = []float64{float64(charOff)}
		dict[1236] = []float64{float64(arrayOff)}
		fdDict[18] = []float64{float64(len(private)), float64(privOff)}
		nextTop, err := encodeCFFIndexChecked([][]byte{encodeCFFDict(dict)})
		if err != nil {
			return nil, err
		}
		nextArray, err := encodeCFFIndexChecked([][]byte{encodeCFFDict(fdDict)})
		if err != nil {
			return nil, err
		}
		if len(nextTop) == len(top) && len(nextArray) == len(fdArray) {
			var out bytes.Buffer
			out.Grow(privOff + len(private) + len(locals))
			for _, part := range [][]byte{header, names, nextTop, strings, globals, charset, fdSelect, chars, nextArray, private, locals} {
				out.Write(part)
			}
			return out.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("CID CFF offsets did not converge")
}

// parseCFFAndCountGlyphs 解析CFF头部并统计字形数量
// 入参: data CFF数据
// 返回: int 字形数量, error 错误信息
func parseCFFAndCountGlyphs(data []byte) (int, error) {
	if !isBareCFFData(data) {
		return 0, fmt.Errorf("invalid CFF header")
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
		dict, err := readCFFDict(topDictData)
		if err != nil {
			return 0, err
		}
		charStrOff, err := cffDictOffset(data, dict, 17)
		if err != nil {
			return 0, err
		}
		count, size := getCFFIndexCount(data, charStrOff)
		if count > 0 && size > 0 {
			return count, nil
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

// parseCFFDict 读取可选CFF字典，编码无效时返回nil
// 入参: data 字典数据
// 返回: cffDict 解析后的字典映射
func parseCFFDict(data []byte) cffDict {
	dict, _ := readCFFDict(data)
	return dict
}

// readCFFDict 校验CFF字典编码、数值和48项操作数上限
// 入参: data 字典数据
// 返回: cffDict 字典映射, error 编码或操作数错误
func readCFFDict(data []byte) (cffDict, error) {
	return readCFFDictionary(data, false)
}

// readCFFDictionary 读取字体字典，CFF2顶层字典允许变化存储偏移操作符
// 入参: data 字典数据, cff2 是否为CFF2顶层字典
// 返回: cffDict 字典映射, error 编码或操作数错误
func readCFFDictionary(data []byte, cff2 bool) (cffDict, error) {
	dict := make(cffDict)
	var operands []float64
	i := 0
	for i < len(data) {
		b := data[i]
		i++
		if b <= 21 || cff2 && b == 24 {
			op := int(b)
			if b == 12 {
				if i >= len(data) {
					return nil, fmt.Errorf("truncated CFF operator")
				}
				op = 1200 + int(data[i])
				i++
			}
			if len(operands) == 0 && !(op >= 6 && op <= 9 || op == 1212 || op == 1213) {
				return nil, fmt.Errorf("missing CFF dictionary operands")
			}
			dict[op] = operands
			operands = nil
		} else if b == 28 {
			if len(data)-i < 2 {
				return nil, fmt.Errorf("truncated CFF integer")
			}
			val := int(int16(binary.BigEndian.Uint16(data[i:])))
			operands = append(operands, float64(val))
			i += 2
		} else if b == 29 {
			if len(data)-i < 4 {
				return nil, fmt.Errorf("truncated CFF integer")
			}
			val := int(int32(binary.BigEndian.Uint32(data[i:])))
			operands = append(operands, float64(val))
			i += 4
		} else if b == 30 {
			s, n := parseCFFReal(data[i:])
			f, err := strconv.ParseFloat(s, 64)
			if n == 0 || err != nil || !finite(f) {
				return nil, fmt.Errorf("invalid CFF real")
			}
			operands = append(operands, f)
			i += n
		} else if b >= 32 && b <= 246 {
			operands = append(operands, float64(int(b)-139))
		} else if b >= 247 && b <= 250 {
			if i == len(data) {
				return nil, fmt.Errorf("truncated CFF integer")
			}
			b1 := int(data[i])
			i++
			operands = append(operands, float64((int(b)-247)*256+b1+108))
		} else if b >= 251 && b <= 254 {
			if i == len(data) {
				return nil, fmt.Errorf("truncated CFF integer")
			}
			b1 := int(data[i])
			i++
			operands = append(operands, float64(-(int(b)-251)*256-b1-108))
		} else {
			return nil, fmt.Errorf("invalid CFF dictionary byte")
		}
		if len(operands) > 48 {
			return nil, fmt.Errorf("CFF dictionary operand limit exceeded")
		}
	}
	if len(operands) != 0 {
		return nil, fmt.Errorf("unused CFF dictionary operands")
	}
	return dict, nil
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
				if b>>4 == 15 && b&15 != 15 {
					return "", 0
				}
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
			if n == 0xD {
				return "", 0
			}
		}
	}
	if !done {
		return "", 0
	}
	return sb.String(), i
}

// encodeCFFDict 编码CFF字典，将字体类型标识置于首项
// 入参: dict CFF字典映射
// 返回: []byte 编码后的字典数据
func encodeCFFDict(dict cffDict) []byte {
	buf := new(bytes.Buffer)
	var keys []int
	for k := range dict {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for index, op := range keys {
		if op == 1220 || op == 1230 {
			copy(keys[1:index+1], keys[:index])
			keys[0] = op
			break
		}
	}
	for _, op := range keys {
		vals := dict[op]
		if len(vals) == 0 && (op >= 6 && op <= 9 || op == 1212 || op == 1213) {
			continue
		}
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
	if val == math.Trunc(val) && val >= math.MinInt32 && val <= math.MaxInt32 {
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
		s := strconv.FormatFloat(val, 'g', -1, 64)
		buf.WriteByte(30)
		var nibbles []byte
		for i := 0; i < len(s); i++ {
			c := s[i]
			var n byte
			switch c {
			case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				n = byte(c - '0')
			case '.':
				n = 0xA
			case 'E', 'e':
				n = 0xB
				if i+1 < len(s) && s[i+1] == '-' {
					n = 0xC
					i++
				} else if i+1 < len(s) && s[i+1] == '+' {
					i++
				}
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

// encodeCFFIndexChecked 校验项数和偏移范围后编码CFF索引结构
// 入参: items 数据项列表
// 返回: []byte 编码后的索引数据, error 项数或大小超限错误
func encodeCFFIndexChecked(items [][]byte) ([]byte, error) {
	count := len(items)
	if count == 0 {
		return []byte{0, 0}, nil
	}
	if count > math.MaxUint16 {
		return nil, fmt.Errorf("CFF index count %d exceeds %d", count, math.MaxUint16)
	}
	var totalSize uint64
	for _, item := range items {
		totalSize += uint64(len(item))
	}
	if totalSize+1 > math.MaxUint32 {
		return nil, fmt.Errorf("CFF index size %d exceeds uint32 limit", totalSize)
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
	base := uint64(3) + uint64(count+1)*uint64(offSize)
	if base+totalSize > uint64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("CFF index buffer exceeds capacity")
	}
	data := make([]byte, int(base+totalSize))
	binary.BigEndian.PutUint16(data, uint16(count))
	data[2] = byte(offSize)
	offset := uint64(1)
	for i := 0; i <= count; i++ {
		for j := 0; j < offSize; j++ {
			data[3+i*offSize+offSize-1-j] = byte(offset >> (8 * j))
		}
		if i < count {
			copy(data[base+offset-1:], items[i])
			offset += uint64(len(items[i]))
		}
	}
	return data, nil
}

// encodeCFFIndex 编码CFF索引结构，越界时直接拒绝而非静默截断
// 入参: items 数据项列表
// 返回: []byte 编码后的索引数据
func encodeCFFIndex(items [][]byte) []byte {
	data, err := encodeCFFIndexChecked(items)
	if err != nil {
		panic(err)
	}
	return data
}

// getCFFIndexCount 读取CFF索引的计数和大小
// 入参: data CFF数据, offset 偏移量
// 返回: int 数量, int 索引结构总大小
func getCFFIndexCount(data []byte, offset int) (int, int) {
	count, _, end, err := scanCFFIndex(data, offset)
	if err != nil {
		return 0, 0
	}
	return count, end - offset
}

// scanCFFIndex 无分配校验索引偏移、顺序和全部数据范围
// 入参: data CFF数据, offset 索引偏移
// 返回: int 项数, int 数据起点, int 索引终点, error 索引错误
func scanCFFIndex(data []byte, offset int) (int, int, int, error) {
	if offset < 0 || offset > len(data)-2 {
		return 0, 0, 0, fmt.Errorf("truncated CFF index")
	}
	count := int(binary.BigEndian.Uint16(data[offset:]))
	if count == 0 {
		return 0, offset + 2, offset + 2, nil
	}
	if offset > len(data)-3 {
		return 0, 0, 0, fmt.Errorf("truncated CFF index header")
	}
	width := int(data[offset+2])
	if width < 1 || width > 4 || count+1 > (len(data)-offset-3)/width {
		return 0, 0, 0, fmt.Errorf("invalid CFF index offsets")
	}
	base := offset + 3 + (count+1)*width
	previous := uint64(0)
	for index := 0; index <= count; index++ {
		value := uint64(0)
		for _, b := range data[offset+3+index*width : offset+3+(index+1)*width] {
			value = value<<8 | uint64(b)
		}
		if value < 1 || value-1 > uint64(len(data)-base) || value < previous || index == 0 && value != 1 {
			return 0, 0, 0, fmt.Errorf("invalid CFF index range")
		}
		previous = value
	}
	return count, base, base + int(previous) - 1, nil
}

// cffDictOffset 读取必须为单个整数且位于字体数据内的字典偏移
// 入参: data 字体数据, dict 字典, op 偏移操作符
// 返回: int 偏移值, error 字典或范围错误
func cffDictOffset(data []byte, dict cffDict, op int) (int, error) {
	values := dict[op]
	if len(values) != 1 || !finite(values[0]) || values[0] < 0 || values[0] >= float64(len(data)) || values[0] != math.Trunc(values[0]) {
		return 0, fmt.Errorf("invalid CFF dictionary offset %d", op)
	}
	return int(values[0]), nil
}

// getCFFIndexData 读取CFF索引的数据块
// 入参: data CFF数据, offset 偏移量
// 返回: []byte 索引数据(已去除offsets), int 索引结构总大小
func getCFFIndexData(data []byte, offset int) ([]byte, int) {
	count, base, end, err := scanCFFIndex(data, offset)
	if err != nil {
		return nil, 0
	}
	if count == 0 {
		return nil, end - offset
	}
	offSize := int(data[offset+2])
	off1 := readCFFOffset(data, offset+3+offSize, offSize)
	return data[base : base+off1-1], end - offset
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
	if _, _, _, err := scanCFFIndex(data, stringIndexOff); err != nil {
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
	return cffCharsetMapping(data, sids, registry, ordering, stringIndexOff)
}

// cffCharsetMapping 根据已解析的CFF字符集恢复Unicode及字形私有映射
// 入参: data CFF数据, sids 字符标识, registry 注册机构, ordering 字符集, stringIndexOff 字符串索引偏移
// 返回: map[rune]uint16 恢复的映射表
func cffCharsetMapping(data []byte, sids []int, registry, ordering string, stringIndexOff int) map[rune]uint16 {
	numGlyphs := uint16(min(len(sids), 0xFFFF))
	if registry == "Adobe" && ordering == "GB1" {
		mapping := make(map[rune]uint16, len(sids))
		for gid, cid := range sids {
			if gid == 0 {
				continue
			}
			if r, ok := adobeGB1CIDToUnicode(cid); ok && r != 0 {
				mapping[r] = uint16(gid)
			}
		}
		AddPackedGlyphMapping(mapping, numGlyphs)
		return mapping
	}
	if registry != "" {
		if numGlyphs == 0 {
			return nil
		}
		mapping := make(map[rune]uint16, numGlyphs)
		AddPackedGlyphMapping(mapping, numGlyphs)
		return mapping
	}
	mapping := make(map[rune]uint16, len(sids))
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
			name = string(readStringIndexItem(data, stringIndexOff, idx))
		}
		if name != "" {
			if r := getUnicodeFromName(name); r != 0 {
				mapping[r] = uint16(gid)
			}
		}
	}
	AddPackedGlyphMapping(mapping, numGlyphs)
	return mapping
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
		if offset < 0 || length < 0 || offset > len(data) || length > len(data)-offset {
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
// 返回: []byte 共享原始数据的只读字符串，索引无效时为nil
func readStringIndexItem(data []byte, offset int, idx int) []byte {
	if offset < 0 || offset > len(data)-2 || idx < 0 {
		return nil
	}
	count := int(binary.BigEndian.Uint16(data[offset:]))
	if idx >= count || offset > len(data)-3 {
		return nil
	}
	offSize := int(data[offset+2])
	if offSize < 1 || offSize > 4 || count+1 > (len(data)-offset-3)/offSize {
		return nil
	}
	offArrayStart := offset + 3
	p1 := offArrayStart + idx*offSize
	p2 := p1 + offSize
	loc1 := readCFFOffset(data, p1, offSize)
	loc2 := readCFFOffset(data, p2, offSize)
	dataStart := offArrayStart + (count+1)*offSize
	if loc1 < 1 || loc2 < loc1 || loc2-1 > len(data)-dataStart {
		return nil
	}
	return data[dataStart+loc1-1 : dataStart+loc2-1]
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
		return string(readStringIndexItem(data, stringIndexOff, sid-391))
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
	gbk := [2]byte{byte(row + 0xA0), byte(cell + 0xA0)}
	var decoded [2 * utf8.UTFMax]byte
	nDst, nSrc, err := simplifiedchinese.GBK.NewDecoder().Transform(decoded[:], gbk[:], true)
	if err != nil || nDst == 0 || nSrc != len(gbk) {
		return 0, false
	}
	r, size := utf8.DecodeRune(decoded[:nDst])
	if size != nDst {
		return 0, false
	}
	return r, true
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
