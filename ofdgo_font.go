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
	"path"
	"sort"
	"unicode/utf8"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// FontFace 字体文件中的字体名称与零起始索引，不依赖文件名
// Family和Style保留排版族，MenuFamily和MenuStyle提供兼容四款式的菜单名称
// FamilyKey用于同来源内的族关联，Bold和Italic表示字体固有样式
type FontFace = basefont.Face

// Faces 读取字体名称表，集合文件按原顺序列出各项，不解析字形轮廓
// 返回: []FontFace 字体列表, error 格式或名称表错误
func (f FontFile) Faces() ([]FontFace, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	return basefont.ReadFaces(data)
}

// LocalizedFaces 读取指定语言的显示名称，保留PostScript名称、全部别名和集合索引
// 未找到对应名称时沿用Faces的名称选择规则，不修改字体数据
// 入参: platform OpenType名称表平台标识, language 该平台的语言标识
// 返回: []FontFace 字体列表, error 格式或名称表错误
func (f FontFile) LocalizedFaces(platform, language uint16) ([]FontFace, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	return basefont.ReadLocalizedFaces(data, platform, language)
}

// Face 提取指定字体，返回独立的OpenType数据，可用于预览或AddFont，且不修改原文件
// 入参: index 零起始字体索引，非集合文件只能为0
// 返回: []byte 独立字体数据, error 格式或索引错误
func (f FontFile) Face(index int) ([]byte, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	return basefont.ExtractFace(data, index)
}

// MissingGlyphs 检查指定字体的字符覆盖，不注册资源或修改文档，忽略段落换行
// 入参: index 零起始字体索引, text 待检查文字
// 返回: string 按首次出现顺序去重的缺失字符, error 字体或文字格式错误
func (f FontFile) MissingGlyphs(index int, text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("text must be UTF-8")
	}
	data, err := f.Face(index)
	if err != nil {
		return "", err
	}
	return basefont.MissingGlyphs(data, text)
}

// FontFamilyIndexes 按兼容字体族、版本及来源分组，同款式重复时整族保留独立入口
// 入参: faces 字体信息, sources 每项的来源标识，空列表表示同一来源
// 返回: [][]int 按首次出现顺序分组的候选索引
func FontFamilyIndexes(faces []FontFace, sources []string) [][]int {
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

// FontStyleIndex 选择同族候选中最接近的真实款式，允许补充合成样式但不抵消固有样式
// 入参: faces 同来源同族候选, bold 粗体, italic 斜体
// 返回: int 候选索引，无可用款式时为-1
func FontStyleIndex(faces []FontFace, bold, italic bool) int {
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

// FontPreferenceOrder 按字体族偏好排列候选索引，同族优先常规样式，其余保持原顺序
// 入参: faces 候选字体, families 按优先级排列的字体族别名组
// 返回: []int 候选字体的零起始索引，不修改输入列表
func FontPreferenceOrder(faces []FontFace, families [][]string) []int {
	priorities := make(map[string]int)
	for priority, names := range families {
		for _, name := range names {
			key := FontNormalizeName(name)
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
			if priority, exists := priorities[FontNormalizeName(name)]; exists && priority*2 < ranks[i] {
				ranks[i] = priority * 2
			}
		}
		if ranks[i] < len(families)*2 {
			switch FontNormalizeName(face.Style) {
			case "", "regular", "normal", "roman", "常规", "正常", "标准":
			default:
				ranks[i]++
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return ranks[order[i]] < ranks[order[j]] })
	return order
}

// FixFontDataAggressive 激进修复字体数据
// 尝试修复缺失表(OS/2, cmap等)的TrueType字体或包装CFF裸数据
// 对显式字形映射补充私有字符cmap，便于按glyph id渲染
// 入参: data 原始字体数据, fixCmap 是否修复cmap, fixName 是否修复name
// 返回: bool 是否修复, []byte 修复后数据, map[rune]uint16 字符映射, bool 是否缺失cmap, error 错误信息
func FixFontDataAggressive(data []byte, fixCmap, fixName bool) (bool, []byte, map[rune]uint16, bool, error) {
	return basefont.FixDataAggressive(data, fixCmap, fixName)
}

// FontNormalizeName 规范化字体名称
// 入参: name 字体名称
// 返回: string 规范化后的字体名称
func FontNormalizeName(name string) string {
	return basefont.NormalizeName(name)
}

// FontCandidateNames 获取字体候选名称
// 入参: names 字体名称列表
// 返回: []string 字体候选名称列表
func FontCandidateNames(names ...string) []string {
	return basefont.CandidateNames(names...)
}

// FontFilePatterns 获取字体文件匹配模式
// 入参: names 字体名称列表
// 返回: []string 字体文件匹配模式
func FontFilePatterns(names ...string) []string {
	return basefont.FilePatterns(names...)
}

// FontSystemNames 获取系统字体名称
// 入参: names 字体名称列表
// 返回: []string 系统字体名称
func FontSystemNames(names ...string) []string {
	return basefont.SystemNames(names...)
}

// FontFileMatches 匹配指定样式的字体文件名称
// 入参: files 可用字体文件名, bold 是否粗体, italic 是否斜体, names 字体名称，留空匹配默认回退字体
// 返回: []string 按优先级排列的匹配文件名
func FontFileMatches(files []string, bold, italic bool, names ...string) []string {
	return basefont.FileMatchNames(basefont.FileMatches(basefont.FileCandidates(files, path.Base), names, bold, italic))
}
