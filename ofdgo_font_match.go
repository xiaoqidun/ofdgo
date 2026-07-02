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

const (
	fontMatchNone = iota
	fontMatchExact
	fontMatchPartial
	fontMatchFuzzy
)

var fontMatchRules = []fontMatchRule{
	{
		Keys:            []string{"小标宋", "方正小标宋", "xiaobiaosong", "fzxiaobiaosong", "fzxbs"},
		Names:           []string{"小标宋体", "方正小标宋简体", "FZXiaoBiaoSong-B05", "FZXiaoBiaoSong-B05S"},
		Files:           []string{"FZXBSJW.TTF", "fzxbs*.ttf", "fzxiaobiaosong*.ttf", "xiaobiaosong*.ttf"},
		System:          "FZXiaoBiaoSong-B05S",
		NoSyntheticBold: true,
	},
	{
		Keys:   []string{"宋体", "新宋体", "simsun", "nsimsun", "simsunextb", "song", "songti", "stsong", "书宋", "方正书宋"},
		Names:  []string{"宋体", "新宋体", "SimSun", "NSimSun", "SongTi", "STSong"},
		Files:  []string{"simsun.ttc", "simsun.ttf", "nsimsun.ttf", "STSONG.TTF"},
		System: "SimSun",
	},
	{
		Keys:   []string{"黑体", "hei", "simhei", "heiti", "sthei", "方正黑体"},
		Names:  []string{"黑体", "SimHei", "HeiTi", "STHeiti", "方正黑体简体"},
		Files:  []string{"simhei.ttf", "STXIHEI.TTF", "FZHT_GB18030.TTF", "fzht*.ttf"},
		System: "SimHei",
	},
	{
		Keys:   []string{"楷体", "kai", "kaiti", "simkai", "stkaiti", "方正楷体"},
		Names:  []string{"楷体", "KaiTi", "SimKai", "STKaiti", "方正楷体简体"},
		Files:  []string{"simkai.ttf", "kaiti.ttf", "STKAITI.TTF", "FZKTJ.TTF", "fzkai*.ttf", "fzkt*.ttf"},
		System: "KaiTi",
	},
	{
		Keys:   []string{"仿宋", "fang", "fangsong", "simfang", "stfangsong", "方正仿宋"},
		Names:  []string{"仿宋", "FangSong", "SimFang", "STFangsong", "方正仿宋简体"},
		Files:  []string{"simfang.ttf", "fangsong.ttf", "STFANGSO.TTF", "FZFSJ.TTF", "fzfs*.ttf"},
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
		Keys:   []string{"helvetica"},
		Names:  []string{"Helvetica", "Arial"},
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

var fontFallbackFiles = []string{
	"simsun.ttc", "simsun.ttf", "nsimsun.ttf",
	"msyh.ttc", "msyh.ttf", "simhei.ttf",
	"NotoSansCJK*.ttc", "NotoSansSC*.otf", "SourceHanSansSC*.otf",
	"arial.ttf",
}

// FontNormalizeName 规范化字体名称
// 入参: name 字体名称
// 返回: string 规范化后的字体名称
func FontNormalizeName(name string) string {
	return fontNormalizeName(name)
}

// FontCandidateNames 获取字体候选名称
// 入参: names 字体名称列表
// 返回: []string 字体候选名称列表
func FontCandidateNames(names ...string) []string {
	return fontCandidateNames(names...)
}

// FontFilePatterns 获取字体文件匹配模式
// 入参: names 字体名称列表
// 返回: []string 字体文件匹配模式
func FontFilePatterns(names ...string) []string {
	return fontFilePatterns(names...)
}

// FontSystemNames 获取系统字体名称
// 入参: names 字体名称列表
// 返回: []string 系统字体名称
func FontSystemNames(names ...string) []string {
	return fontSystemNames(names...)
}

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
	return fontCandidateNamesByLevel(fontMatchFuzzy, names...)
}

// fontExactCandidateNames 获取精确字体候选名称
// 入参: names 字体名称列表
// 返回: []string 精确字体候选名称
func fontExactCandidateNames(names ...string) []string {
	return fontCandidateNamesByLevel(fontMatchExact, names...)
}

// fontCandidateNamesByLevel 获取指定等级内的字体候选名称
// 入参: maxLevel 最大匹配等级, names 字体名称列表
// 返回: []string 字体候选名称
func fontCandidateNamesByLevel(maxLevel int, names ...string) []string {
	var result []string
	seen := make(map[string]bool)
	var bases []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		result = appendFontName(result, seen, name)
		bases = append(bases, name)
	}
	for level := fontMatchExact; level <= maxLevel; level++ {
		for _, name := range bases {
			for _, rule := range fontMatchRules {
				if fontRuleMatchLevel(rule, name) == level {
					result = appendFontName(result, seen, rule.System)
					for _, item := range rule.Names {
						result = appendFontName(result, seen, item)
					}
				}
			}
		}
	}
	return result
}

// fontPatternStem 获取字体匹配模式主干
// 入参: pattern 字体文件匹配模式
// 返回: string 字体匹配模式主干
func fontPatternStem(pattern string) string {
	stem := strings.TrimSuffix(pattern, "*")
	stem = strings.TrimSuffix(stem, path.Ext(stem))
	if index := strings.IndexAny(stem, "*?["); index >= 0 {
		stem = stem[:index]
	}
	return fontNormalizeName(stem)
}

// fontFilePatterns 获取字体文件匹配模式
// 入参: names 字体名称列表
// 返回: []string 字体文件匹配模式
func fontFilePatterns(names ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for level := fontMatchExact; level <= fontMatchFuzzy; level++ {
		for _, name := range fontCandidateNamesAtLevel(level, names...) {
			result = appendFontPattern(result, seen, name+"*")
		}
		for _, name := range names {
			for _, rule := range fontMatchRules {
				if fontRuleMatchLevel(rule, name) == level {
					for _, item := range rule.Files {
						result = appendFontPattern(result, seen, item)
					}
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
		for _, item := range fontCandidateNamesByLevel(fontMatchPartial, name) {
			result = appendFontName(result, seen, item)
		}
	}
	return result
}

// fontCandidateNamesAtLevel 获取指定等级的字体候选名称
// 入参: level 匹配等级, names 字体名称列表
// 返回: []string 字体候选名称
func fontCandidateNamesAtLevel(level int, names ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if level == fontMatchExact {
			result = appendFontName(result, seen, name)
		}
		for _, rule := range fontMatchRules {
			if fontRuleMatchLevel(rule, name) == level {
				result = appendFontName(result, seen, rule.System)
				for _, item := range rule.Names {
					result = appendFontName(result, seen, item)
				}
			}
		}
	}
	return result
}

// fontDefaultSystemNames 获取默认系统字体名称
// 返回: []string 默认系统字体名称
func fontDefaultSystemNames() []string {
	return []string{
		"SimSun", "Microsoft YaHei", "SimHei", "KaiTi", "FangSong",
		"Noto Sans CJK SC", "Source Han Sans SC", "Arial", "Segoe UI", "Times New Roman",
	}
}

// fontNoSyntheticBold 判断字体是否禁用合成粗体
// 入参: names 字体名称列表
// 返回: bool 是否禁用合成粗体
func fontNoSyntheticBold(names ...string) bool {
	for _, name := range names {
		for _, rule := range fontMatchRules {
			level := fontRuleMatchLevel(rule, name)
			if rule.NoSyntheticBold && level >= fontMatchExact && level <= fontMatchPartial {
				return true
			}
		}
	}
	return false
}

// isFontFileName 判断是否为字体文件名
// 入参: name 文件名
// 返回: bool 是否为字体文件
func isFontFileName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".ttf", ".otf", ".ttc":
		return true
	default:
		return false
	}
}

// fontRuleMatch 判断字体规则是否匹配
// 入参: rule 字体匹配规则, name 字体名称
// 返回: bool 是否匹配
func fontRuleMatch(rule fontMatchRule, name string) bool {
	return fontRuleMatchLevel(rule, name) != fontMatchNone
}

// fontRuleMatchLevel 获取字体规则匹配等级
// 入参: rule 字体匹配规则, name 字体名称
// 返回: int 匹配等级
func fontRuleMatchLevel(rule fontMatchRule, name string) int {
	name = fontNormalizeName(name)
	if name == "" {
		return fontMatchNone
	}
	items := fontRuleNames(rule)
	for _, item := range items {
		item = fontNormalizeName(item)
		if item != "" && name == item {
			return fontMatchExact
		}
	}
	for _, item := range items {
		item = fontNormalizeName(item)
		if item != "" && (strings.HasPrefix(name, item) || strings.HasPrefix(item, name)) {
			return fontMatchPartial
		}
	}
	for _, item := range items {
		item = fontNormalizeName(item)
		if item != "" && (strings.Contains(name, item) || strings.Contains(item, name)) {
			return fontMatchFuzzy
		}
	}
	return fontMatchNone
}

// fontRuleNames 获取字体规则名称列表
// 入参: rule 字体匹配规则
// 返回: []string 字体规则名称列表
func fontRuleNames(rule fontMatchRule) []string {
	var result []string
	result = append(result, rule.Keys...)
	result = append(result, rule.Names...)
	if rule.System != "" {
		result = append(result, rule.System)
	}
	return result
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
