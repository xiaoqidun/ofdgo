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
	"path"
	"strings"
)

type fontMatchRule struct {
	Keys            []string
	Names           []string
	Files           []string
	System          string
	NoSyntheticBold bool
}

var fontMatchRules = []fontMatchRule{
	{
		Keys:            []string{"小标宋", "方正小标宋", "xiaobiaosong", "fzxiaobiaosong", "fzxbs"},
		Names:           []string{"小标宋体", "方正小标宋简体", "FZXiaoBiaoSong-B05", "FZXiaoBiaoSong-B05S"},
		Files:           []string{"FZXBSJW.TTF", "fzxbs*.ttf", "xiaobiaosong*.ttf"},
		System:          "FZXiaoBiaoSong-B05S",
		NoSyntheticBold: true,
	},
	{
		Keys:   []string{"宋体", "新宋体", "simsun", "nsimsun", "songti", "书宋", "方正书宋"},
		Names:  []string{"宋体", "新宋体", "SimSun", "NSimSun", "SongTi"},
		Files:  []string{"simsun.ttc", "simsun.ttf", "nsimsun.ttf"},
		System: "SimSun",
	},
	{
		Keys:   []string{"黑体", "simhei", "heiti"},
		Names:  []string{"黑体", "SimHei", "HeiTi"},
		Files:  []string{"simhei.ttf"},
		System: "SimHei",
	},
	{
		Keys:   []string{"楷体", "kaiti", "simkai"},
		Names:  []string{"楷体", "KaiTi", "SimKai"},
		Files:  []string{"simkai.ttf", "kaiti.ttf"},
		System: "KaiTi",
	},
	{
		Keys:   []string{"仿宋", "fangsong", "simfang"},
		Names:  []string{"仿宋", "FangSong", "SimFang"},
		Files:  []string{"simfang.ttf", "fangsong.ttf"},
		System: "FangSong",
	},
	{
		Keys:   []string{"微软雅黑", "microsoftyahei", "yahei", "msyh"},
		Names:  []string{"微软雅黑", "Microsoft YaHei", "YaHei", "MSYH"},
		Files:  []string{"msyh.ttc", "msyh.ttf"},
		System: "Microsoft YaHei",
	},
	{
		Keys:   []string{"arialblack"},
		Names:  []string{"Arial Black", "ArialBlack"},
		Files:  []string{"ariblk.ttf", "arial black*.ttf", "arialblack*.ttf"},
		System: "Arial Black",
	},
	{
		Keys:   []string{"arial"},
		Names:  []string{"Arial"},
		Files:  []string{"arial.ttf", "arial*.ttf"},
		System: "Arial",
	},
	{
		Keys:   []string{"couriernew", "courier"},
		Names:  []string{"Courier New", "CourierNew", "Courier"},
		Files:  []string{"cour.ttf", "courier new*.ttf", "courier*.ttf"},
		System: "Courier New",
	},
	{
		Keys:   []string{"segoeui"},
		Names:  []string{"Segoe UI", "SegoeUI"},
		Files:  []string{"segoeui.ttf", "segoe*.ttf"},
		System: "Segoe UI",
	},
	{
		Keys:   []string{"timesnewroman", "times"},
		Names:  []string{"Times New Roman", "TimesNewRoman"},
		Files:  []string{"times.ttf", "times new roman*.ttf", "times*.ttf"},
		System: "Times New Roman",
	},
}

var fontFallbackFiles = []string{"simsun.ttc", "msyh.ttc", "simhei.ttf"}

// fontNormalizeName 规范化字体名称
// 入参: name 字体名称
// 返回: string 规范化后的字体名称
func fontNormalizeName(name string) string {
	name = strings.TrimSuffix(path.Base(name), path.Ext(name))
	name = strings.ToLower(strings.TrimSpace(name))
	replacer := strings.NewReplacer(" ", "", "-", "", "_", "", "(", "", ")", "", "（", "", "）", "")
	return replacer.Replace(name)
}

// fontCandidateNames 获取字体候选名称
// 入参: names 字体名称列表
// 返回: []string 字体候选名称
func fontCandidateNames(names ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		result = appendFontName(result, seen, name)
		for _, rule := range fontMatchRules {
			if fontRuleMatch(rule, name) {
				result = appendFontName(result, seen, rule.System)
				for _, item := range rule.Names {
					result = appendFontName(result, seen, item)
				}
			}
		}
	}
	return result
}

// fontFilePatterns 获取字体文件匹配模式
// 入参: names 字体名称列表
// 返回: []string 字体文件匹配模式
func fontFilePatterns(names ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, name := range fontCandidateNames(names...) {
		result = appendFontPattern(result, seen, name+"*")
	}
	for _, name := range names {
		for _, rule := range fontMatchRules {
			if fontRuleMatch(rule, name) {
				for _, item := range rule.Files {
					result = appendFontPattern(result, seen, item)
				}
			}
		}
	}
	return result
}

// fontSystemNames 获取系统字体名称
// 入参: names 字体名称列表
// 返回: []string 系统字体名称
func fontSystemNames(names ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, name := range names {
		for _, item := range fontCandidateNames(name) {
			result = appendFontName(result, seen, item)
		}
	}
	return result
}

// fontDefaultSystemNames 获取默认系统字体名称
// 返回: []string 默认系统字体名称
func fontDefaultSystemNames() []string {
	return []string{
		"SimHei", "Microsoft YaHei", "SimSun", "KaiTi", "FangSong",
		"Arial", "Segoe UI", "Times New Roman",
	}
}

// fontNoSyntheticBold 判断字体是否禁用合成粗体
// 入参: names 字体名称列表
// 返回: bool 是否禁用合成粗体
func fontNoSyntheticBold(names ...string) bool {
	for _, name := range names {
		for _, rule := range fontMatchRules {
			if rule.NoSyntheticBold && fontRuleMatch(rule, name) {
				return true
			}
		}
	}
	return false
}

// fontRuleMatch 判断字体规则是否匹配
// 入参: rule 字体匹配规则, name 字体名称
// 返回: bool 是否匹配
func fontRuleMatch(rule fontMatchRule, name string) bool {
	name = fontNormalizeName(name)
	if name == "" {
		return false
	}
	for _, key := range rule.Keys {
		key = fontNormalizeName(key)
		if key != "" && (name == key || strings.Contains(name, key)) {
			return true
		}
	}
	return false
}

// appendFontName 追加字体名称
// 入参: names 字体名称列表, seen 去重映射, name 字体名称
// 返回: []string 字体名称列表
func appendFontName(names []string, seen map[string]bool, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return names
	}
	key := fontNormalizeName(name)
	if key == "" || seen[key] {
		return names
	}
	seen[key] = true
	return append(names, name)
}

// appendFontPattern 追加字体文件匹配模式
// 入参: patterns 匹配模式列表, seen 去重映射, pattern 匹配模式
// 返回: []string 匹配模式列表
func appendFontPattern(patterns []string, seen map[string]bool, pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return patterns
	}
	key := strings.ToLower(pattern)
	if seen[key] {
		return patterns
	}
	seen[key] = true
	return append(patterns, pattern)
}
