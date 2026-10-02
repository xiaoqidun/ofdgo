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
)

// readType2FDSelect 校验CID字体的逐字形FD归属和范围终点
// 入参: data 字体数据, offset FDSelect偏移, count 字形数, fonts FD数量
// 返回: []int FD索引, error 格式或范围错误
func readType2FDSelect(data []byte, offset, count, fonts int) ([]int, error) {
	if offset < 0 || offset >= len(data) || count <= 0 || fonts <= 0 {
		return nil, fmt.Errorf("invalid CFF FDSelect range")
	}
	result := make([]int, count)
	switch data[offset] {
	case 0:
		if count > len(data)-offset-1 {
			return nil, fmt.Errorf("truncated CFF FDSelect")
		}
		for gid, fd := range data[offset+1 : offset+1+count] {
			if int(fd) >= fonts {
				return nil, fmt.Errorf("invalid CFF font dictionary index")
			}
			result[gid] = int(fd)
		}
	case 3:
		if offset > len(data)-3 {
			return nil, fmt.Errorf("truncated CFF FDSelect header")
		}
		ranges := int(binary.BigEndian.Uint16(data[offset+1:]))
		start := offset + 3
		if ranges == 0 || ranges > (len(data)-start-2)/3 {
			return nil, fmt.Errorf("invalid CFF FDSelect ranges")
		}
		if binary.BigEndian.Uint16(data[start:]) != 0 || int(binary.BigEndian.Uint16(data[start+3*ranges:])) != count {
			return nil, fmt.Errorf("invalid CFF FDSelect endpoints")
		}
		for index := 0; index < ranges; index++ {
			pos := start + 3*index
			first := int(binary.BigEndian.Uint16(data[pos:]))
			last := int(binary.BigEndian.Uint16(data[pos+3:]))
			fd := int(data[pos+2])
			if last <= first || last > count || fd >= fonts {
				return nil, fmt.Errorf("invalid CFF FDSelect interval")
			}
			for gid := first; gid < last; gid++ {
				result[gid] = fd
			}
		}
	default:
		return nil, fmt.Errorf("unsupported CFF FDSelect format %d", data[offset])
	}
	return result, nil
}
