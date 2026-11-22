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
	"sort"
)

// FamilyIndexes 按兼容字体族、版本及来源分组，同款式重复时整族保留独立入口
// 入参: faces 字体信息, sources 每项的来源标识，空列表表示同一来源
// 返回: [][]int 按首次出现顺序分组的候选索引
func FamilyIndexes(faces []Face, sources []string) [][]int {
	groups := make([][]int, 0, len(faces))
	keys := make([]string, len(faces))
	styles := make(map[string]map[[2]bool]bool)
	ambiguous := make(map[string]bool)
	for index, face := range faces {
		key := face.FamilyKey
		if len(sources) > index {
			key += "\x00" + sources[index]
		}
		keys[index] = key
		if styles[key] == nil {
			styles[key] = make(map[[2]bool]bool)
		}
		style := [2]bool{face.Bold, face.Italic}
		ambiguous[key] = ambiguous[key] || styles[key][style]
		styles[key][style] = true
	}
	indexes := make(map[string]int)
	for index, face := range faces {
		key := keys[index]
		if face.FamilyKey == "" || ambiguous[key] {
			groups = append(groups, []int{index})
			continue
		}
		group, exists := indexes[key]
		if !exists {
			group, indexes[key] = len(groups), len(groups)
			groups = append(groups, nil)
		}
		groups[group] = append(groups[group], index)
	}
	return groups
}

// StyleIndex 选择同族候选中最接近的真实款式，允许补充合成样式但不抵消固有样式
// 入参: faces 同来源同族候选, bold 粗体, italic 斜体
// 返回: int 候选索引，无可用款式时为-1
func StyleIndex(faces []Face, bold, italic bool) int {
	selected, best := -1, -1
	for index, face := range faces {
		if face.Bold && !bold || face.Italic && !italic {
			continue
		}
		score := 0
		if face.Bold == bold {
			score++
		}
		if face.Italic == italic {
			score++
		}
		if score > best {
			selected, best = index, score
		}
	}
	return selected
}

// PreferenceOrder 按字体族偏好排列候选索引，同族优先常规样式，其余保持原顺序
// 入参: faces 候选字体, families 按优先级排列的字体族别名组
// 返回: []int 候选字体的零起始索引，不修改输入列表
func PreferenceOrder(faces []Face, families [][]string) []int {
	priorities := make(map[string]int)
	for priority, names := range families {
		for _, name := range names {
			key := NormalizeName(name)
			if _, exists := priorities[key]; key != "" && !exists {
				priorities[key] = priority
			}
		}
	}
	order, ranks := make([]int, len(faces)), make([]int, len(faces))
	for i, face := range faces {
		order[i], ranks[i] = i, len(families)*2
		names := append([]string{face.Family, face.FullName, face.PostScriptName}, face.Names...)
		for _, name := range names {
			if priority, exists := priorities[NormalizeName(name)]; exists && priority*2 < ranks[i] {
				ranks[i] = priority * 2
			}
		}
		if ranks[i] < len(families)*2 {
			switch NormalizeName(face.Style) {
			case "", "regular", "normal", "roman", "常规", "正常", "标准":
			default:
				ranks[i]++
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return ranks[order[i]] < ranks[order[j]] })
	return order
}
