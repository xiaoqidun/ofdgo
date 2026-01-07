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
)

// FixFontDataAggressive 激进修复字体数据
// 尝试修复缺失表(OS/2, cmap等)的TrueType字体或包装CFF裸数据
// 入参: data 原始字体数据, fixCmap 是否修复cmap, fixName 是否修复name
// 返回: bool 是否修复, []byte 修复后数据, map[rune]uint16 字符映射, bool 是否缺失cmap, error 错误信息
func FixFontDataAggressive(data []byte, fixCmap, fixName bool) (bool, []byte, map[rune]uint16, bool, error) {
	if len(data) < 4 {
		return false, data, nil, false, nil
	}
	tag := string(data[0:4])
	u32Tag := binary.BigEndian.Uint32(data[0:4])
	isTrueType := u32Tag == 0x00010000 || tag == "true"
	isOpenType := tag == "OTTO"
	isTTC := tag == "ttcf"
	if isTTC {
		return false, data, nil, false, nil
	}
	if !isTrueType && !isOpenType {
		if data[0] == 1 && data[1] == 0 && data[2] == 4 {
			otf, mapping, err := wrapCFFToOTF(data)
			if err == nil {
				return true, otf, mapping, false, nil
			}
		}
	}
	fixed, newData, mapping, mc, err := fixTrueType(data, fixCmap, fixName)
	return fixed, newData, mapping, mc, err
}
