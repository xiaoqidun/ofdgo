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
	"path"
	"slices"
	"sort"
	"strings"
)

// 字体匹配按精确、部分和模糊匹配排列，零值表示未匹配
const (
	MatchNone = iota
	MatchExact
	MatchPartial
	MatchFuzzy
)

// matchRules 保存常用字体的名称、文件名及系统字体匹配规则
var matchRules = normalizeMatchRules([]matchRule{
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
})

// fontNameReplacer 移除字体名称匹配时忽略的分隔符
var fontNameReplacer = strings.NewReplacer(" ", "", "-", "", "_", "", ",", "", "(", "", ")", "", "（", "", "）", "")

// FileCandidate 字体文件候选
type FileCandidate struct {
	Name       string
	Face       int
	base       string
	lowerBase  string
	filename   string
	normalized string
	names      []string
	fold       string
}

// FileMatch 字体文件匹配结果
type FileMatch struct {
	Name      string
	Face      int
	Priority  int
	Rank      int
	styleRank int
	sortName  string
}

// matchRule 字体匹配规则
type matchRule struct {
	Keys            []string
	Names           []string
	Files           []string
	System          string
	NoSyntheticBold bool
}

// patternMatcher 字体文件匹配器
type patternMatcher struct {
	pattern      string
	lowerPattern string
	stem         string
	aliases      []string
	priority     int
}

// fileKey 字体文件与集合索引的去重键
type fileKey struct {
	Name string
	Face int
}

// NormalizeName 规范化字体名称
// 入参: name 字体名称
// 返回: string 规范化后的字体名称
func NormalizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(strings.TrimSpace(name))
	if IsFontFileName(name) {
		name = strings.TrimSuffix(name, path.Ext(name))
	}
	name = strings.ToLower(strings.TrimSpace(name))
	return fontNameReplacer.Replace(name)
}

// SplitNameStyle 从字体名称提取基础家族名称与粗斜体样式，兼容逗号分隔与Oblique/Slanted命名
// 入参: name 字体名称
// 返回: string 去除尾部样式词的基础名称, bool 是否粗体, bool 是否斜体
func SplitNameStyle(name string) (string, bool, bool) {
	base := strings.TrimSpace(name)
	if base == "" {
		return "", false, false
	}
	var bold, italic bool
	for {
		next, tokenBold, tokenItalic, ok := trimSeparatedStyleToken(base)
		if !ok {
			next, tokenBold, tokenItalic, ok = trimCompactStyleSuffix(base)
		}
		if !ok {
			break
		}
		base = next
		bold = bold || tokenBold
		italic = italic || tokenItalic
	}
	return base, bold, italic
}

// IsFontFileName 判断是否为字体文件名
// 入参: name 文件名
// 返回: bool 是否为字体文件
func IsFontFileName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".ttf", ".otf", ".ttc", ".otc":
		return true
	default:
		return false
	}
}

// appendFontName 追加字体名称
// 入参: names 字体名称列表, seen 去重映射, name 字体名称
// 返回: []string 字体名称列表
func appendFontName(names []string, seen map[string]bool, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return names
	}
	key := NormalizeName(name)
	if key == "" || seen[key] {
		return names
	}
	seen[key] = true
	return append(names, name)
}

// CandidateNames 获取字体候选名称
// 入参: names 字体名称列表
// 返回: []string 字体候选名称
func CandidateNames(names ...string) []string {
	return candidateNamesByLevel(MatchFuzzy, names...)
}

// ExactCandidateNames 获取精确字体候选名称
// 入参: names 字体名称列表
// 返回: []string 精确字体候选名称
func ExactCandidateNames(names ...string) []string {
	return candidateNamesByLevel(MatchExact, names...)
}

// FilePatterns 获取字体文件匹配模式
// 入参: names 字体名称列表
// 返回: []string 字体文件匹配模式
func FilePatterns(names ...string) []string {
	names = expandPatternNames(names)
	var result []string
	seen := make(map[string]bool)
	levels := matchLevels(names)
	for level := MatchExact; level <= MatchFuzzy; level++ {
		for _, name := range candidateNamesAtLevel(level, names, levels) {
			result = appendPattern(result, seen, name+"*")
		}
		for i := range names {
			for j, rule := range matchRules {
				if levels[i*len(matchRules)+j] == level {
					for _, item := range rule.Files {
						result = appendPattern(result, seen, item)
					}
				}
			}
		}
	}
	return result
}

// SystemNames 获取系统字体名称
// 入参: names 字体名称列表
// 返回: []string 系统字体名称
func SystemNames(names ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, name := range names {
		for _, item := range candidateNamesByLevel(MatchPartial, name) {
			result = appendFontName(result, seen, item)
		}
	}
	return result
}

// DefaultSystemNames 获取默认系统字体名称
// 返回: []string 默认系统字体名称
func DefaultSystemNames() []string {
	return []string{
		"SimSun", "Microsoft YaHei", "SimHei", "KaiTi", "FangSong",
		"Noto Sans CJK SC", "Source Han Sans SC", "Arial", "Segoe UI", "Times New Roman",
	}
}

// NoSyntheticBold 判断字体是否禁用合成粗体
// 入参: names 字体名称列表
// 返回: bool 是否禁用合成粗体
func NoSyntheticBold(names ...string) bool {
	for _, name := range names {
		name = NormalizeName(name)
		for _, rule := range matchRules {
			if !rule.NoSyntheticBold {
				continue
			}
			level := ruleMatchLevel(rule, name)
			if level >= MatchExact && level <= MatchPartial {
				return true
			}
		}
	}
	return false
}

// AppendFileNames 建立各字体的内部名称与文件别名匹配候选
// 入参: candidates 字体候选, index 文件候选索引, faces 各字体内部名称
// 返回: []FileCandidate 字体候选列表
func AppendFileNames(candidates []FileCandidate, index int, faces [][]string) []FileCandidate {
	file := candidates[index]
	replaced := false
	for face, names := range faces {
		file.Face = face
		file.names = make([]string, len(names))
		for i, name := range names {
			file.names[i] = NormalizeName(name)
		}
		for _, name := range file.names {
			file.normalized = name
			if !replaced {
				candidates[index] = file
				replaced = true
			} else {
				candidates = append(candidates, file)
			}
		}
	}
	return candidates
}

// CollectionIndex 按内部名称与样式选择集合项，未匹配时保留首项
// 入参: data 集合数据, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: int 零起始字体索引
func CollectionIndex(data []byte, names []string, bold, italic bool) int {
	return NameIndex(FileNames(bytes.NewReader(data)), names, bold, italic)
}

// NameIndex 按名称与样式选择字体索引，未匹配时保留首项
// 入参: faces 各字体名称, names 目标名称, bold 是否粗体, italic 是否斜体
// 返回: int 零起始字体索引
func NameIndex(faces [][]string, names []string, bold, italic bool) int {
	candidates := AppendFileNames([]FileCandidate{{}}, 0, faces)
	if matches := FileMatches(candidates, names, bold, italic); len(matches) > 0 {
		return matches[0].Face
	}
	return 0
}

// FileMatches 匹配字体文件候选
// 入参: candidates 字体候选, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: []FileMatch 按优先级排列的字体匹配
func FileMatches(candidates []FileCandidate, names []string, bold, italic bool) []FileMatch {
	matches := make([]FileMatch, 0, len(candidates))
	seen := make(map[fileKey]int, len(candidates))
	if len(names) == 0 {
		for _, name := range DefaultSystemNames() {
			matches = append(matches, FileMatches(candidates, []string{name}, bold, italic)...)
		}
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].Rank < matches[j].Rank
		})
		unique := matches[:0]
		for _, match := range matches {
			key := fileKey{strings.ToLower(match.Name), match.Face}
			if _, ok := seen[key]; !ok {
				seen[key] = len(unique)
				unique = append(unique, match)
			}
		}
		return unique
	}
	wantBold, wantItalic := bold, italic
	for _, name := range names {
		if _, nameBold, nameItalic := SplitNameStyle(name); nameBold || nameItalic {
			wantBold = wantBold || nameBold
			wantItalic = wantItalic || nameItalic
		}
	}
	for i, name := range names {
		if name == "" {
			continue
		}
		basePriority := (i - len(names)) * 4
		base, _, _ := SplitNameStyle(name)
		origNorm := NormalizeName(name)
		matchExact := func(stem string, priority int) {
			if stem == "" {
				return
			}
			matcher := patternMatcher{stem: stem, priority: priority}
			for _, file := range candidates {
				if rank := matcher.rankCandidate(file); rank == MatchExact {
					appendFileMatch(&matches, seen, matcher, file, rank, wantBold, wantItalic)
				}
			}
		}
		matchExact(origNorm, basePriority)
		for _, variant := range styleNameVariants(base, wantBold, wantItalic) {
			if variant != origNorm {
				matchExact(variant, basePriority+1)
			}
		}
		if baseNorm := NormalizeName(base); baseNorm != "" && baseNorm != origNorm {
			matchExact(baseNorm, basePriority+2)
		}
		for _, alias := range qualifiedNames(name) {
			matchExact(NormalizeName(alias), basePriority+3)
		}
	}
	for _, matcher := range newPatternMatchers(FilePatterns(names...)) {
		for _, file := range candidates {
			rank := matcher.rankCandidate(file)
			appendFileMatch(&matches, seen, matcher, file, rank, wantBold, wantItalic)
		}
	}
	sortFileMatches(matches)
	return matches
}

// FileCandidates 筛选字体文件候选
// 入参: names 文件名列表, base 文件名提取函数
// 返回: []FileCandidate 字体文件候选列表
func FileCandidates(names []string, base func(string) string) []FileCandidate {
	files := make([]FileCandidate, 0, len(names))
	for _, name := range names {
		if IsFontFileName(name) {
			baseName := base(name)
			normalized := NormalizeName(strings.TrimSuffix(baseName, path.Ext(baseName)))
			files = append(files, FileCandidate{
				Name:       name,
				base:       baseName,
				lowerBase:  strings.ToLower(baseName),
				filename:   normalized,
				normalized: normalized,
				fold:       strings.ToLower(name),
			})
		}
	}
	return files
}

// FileMatchNames 获取字体文件匹配名称
// 入参: matches 匹配结果
// 返回: []string 字体文件列表
func FileMatchNames(matches []FileMatch) []string {
	names := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		key := strings.ToLower(match.Name)
		if !seen[key] {
			seen[key] = true
			names = append(names, match.Name)
		}
	}
	return names
}

// newPatternMatcher 创建字体文件匹配器
// 入参: pattern 匹配模式
// 返回: patternMatcher 字体文件匹配器
func newPatternMatcher(pattern string) patternMatcher {
	matcher := patternMatcher{
		pattern:      pattern,
		lowerPattern: strings.ToLower(pattern),
		stem:         patternStem(pattern),
	}
	if matcher.stem != "" {
		for _, alias := range ExactCandidateNames(matcher.stem) {
			alias = NormalizeName(alias)
			if alias != "" {
				matcher.aliases = append(matcher.aliases, alias)
			}
		}
	}
	return matcher
}

// normalizeMatchRules 预处理固定字体规则的匹配名称，保留候选名称及顺序
// 入参: rules 字体匹配规则
// 返回: []matchRule 预处理后的规则
func normalizeMatchRules(rules []matchRule) []matchRule {
	for i := range rules {
		rule := &rules[i]
		keys := append(append(rule.Keys, rule.Names...), rule.System)
		rule.Keys = nil
		for _, key := range keys {
			key = NormalizeName(key)
			if key != "" && !slices.Contains(rule.Keys, key) {
				rule.Keys = append(rule.Keys, key)
			}
		}
	}
	return rules
}

// qualifiedNames 获取保留名称后缀的字体候选
// 入参: name 字体名称
// 返回: []string 保留后缀的候选名称
func qualifiedNames(name string) []string {
	base, bold, italic := SplitNameStyle(name)
	norm := NormalizeName(name)
	baseNorm := NormalizeName(base)
	var names []string
	seen := make(map[string]bool)
	for _, rule := range matchRules {
		level := ruleMatchLevel(rule, norm)
		if level != MatchPartial && (baseNorm == "" || baseNorm == norm || ruleMatchLevel(rule, baseNorm) != MatchExact) {
			continue
		}
		prefix := ""
		for _, alias := range rule.Keys {
			if len(alias) > len(prefix) && strings.HasPrefix(norm, alias) {
				prefix = alias
			}
		}
		suffix := strings.TrimPrefix(norm, prefix)
		suffixes := equivalentStyleSuffixes(suffix, bold, italic)
		for _, alias := range append([]string{rule.System}, rule.Names...) {
			aliasNorm := NormalizeName(alias)
			for _, item := range suffixes {
				names = appendFontName(names, seen, aliasNorm+item)
			}
		}
	}
	return names
}

// candidateNamesByLevel 获取指定等级内的字体候选名称
// 入参: maxLevel 最大匹配等级, names 字体名称列表
// 返回: []string 字体候选名称
func candidateNamesByLevel(maxLevel int, names ...string) []string {
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
	levels := matchLevels(bases)
	for level := MatchExact; level <= maxLevel; level++ {
		for i := range bases {
			for j, rule := range matchRules {
				if levels[i*len(matchRules)+j] == level {
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

// patternStem 获取字体匹配模式主干
// 入参: pattern 字体文件匹配模式
// 返回: string 字体匹配模式主干
func patternStem(pattern string) string {
	stem := strings.TrimSuffix(pattern, "*")
	if IsFontFileName(stem) {
		stem = strings.TrimSuffix(stem, path.Ext(stem))
	}
	if index := strings.IndexAny(stem, "*?["); index >= 0 {
		stem = stem[:index]
	}
	return NormalizeName(stem)
}

// candidateNamesAtLevel 获取指定等级的字体候选名称
// 入参: level 匹配等级, names 字体名称列表, levels 名称与规则的匹配等级
// 返回: []string 字体候选名称
func candidateNamesAtLevel(level int, names []string, levels []int) []string {
	var result []string
	seen := make(map[string]bool)
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if level == MatchExact {
			result = appendFontName(result, seen, name)
		}
		for j, rule := range matchRules {
			if levels[i*len(matchRules)+j] == level {
				result = appendFontName(result, seen, rule.System)
				for _, item := range rule.Names {
					result = appendFontName(result, seen, item)
				}
			}
		}
	}
	return result
}

// matchLevels 按名称和规则顺序计算匹配等级
// 入参: names 字体名称列表
// 返回: []int 名称与规则的匹配等级
func matchLevels(names []string) []int {
	levels := make([]int, len(names)*len(matchRules))
	for i, name := range names {
		name = NormalizeName(name)
		for j, rule := range matchRules {
			levels[i*len(matchRules)+j] = ruleMatchLevel(rule, name)
		}
	}
	return levels
}

// ruleMatchLevel 获取字体规则匹配等级
// 入参: rule 字体匹配规则, name 规范化后的字体名称
// 返回: int 匹配等级
func ruleMatchLevel(rule matchRule, name string) int {
	if name == "" {
		return MatchNone
	}
	level := MatchNone
	for _, item := range rule.Keys {
		if name == item {
			return MatchExact
		}
		if strings.HasPrefix(name, item) {
			level = MatchPartial
		} else if level == MatchNone && strings.Contains(name, item) {
			level = MatchFuzzy
		}
	}
	return level
}

// appendPattern 追加字体文件匹配模式
// 入参: patterns 匹配模式列表, seen 去重映射, pattern 匹配模式
// 返回: []string 匹配模式列表
func appendPattern(patterns []string, seen map[string]bool, pattern string) []string {
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

// newPatternMatchers 创建字体文件匹配器列表
// 入参: patterns 匹配模式列表
// 返回: []patternMatcher 字体文件匹配器列表
func newPatternMatchers(patterns []string) []patternMatcher {
	matchers := make([]patternMatcher, 0, len(patterns))
	for priority, pattern := range patterns {
		matcher := newPatternMatcher(pattern)
		matcher.priority = priority
		matchers = append(matchers, matcher)
	}
	return matchers
}

// appendFileMatch 追加字体文件匹配结果
// 入参: matches 匹配结果, seen 已匹配文件, matcher 匹配器, file 字体文件候选, rank 匹配等级, bold 是否粗体, italic 是否斜体
func appendFileMatch(matches *[]FileMatch, seen map[fileKey]int, matcher patternMatcher, file FileCandidate, rank int, bold, italic bool) {
	if rank == MatchNone {
		return
	}
	suffix := matcher.styleSuffixNormalized(matcher.candidateName(file))
	if rank == MatchFuzzy {
		suffix = matcher.styleSuffixNormalized(NormalizeName(file.base))
	}
	next := FileMatch{
		Name:      file.Name,
		Face:      file.Face,
		Priority:  matcher.priority,
		Rank:      rank,
		styleRank: fileStyleRank(suffix, bold, italic),
		sortName:  file.lowerBase,
	}
	key := fileKey{file.fold, file.Face}
	if index, ok := seen[key]; ok {
		if fileMatchLess(next, (*matches)[index]) {
			(*matches)[index] = next
		}
		return
	}
	seen[key] = len(*matches)
	*matches = append(*matches, next)
}

// sortFileMatches 排序字体文件匹配结果
// 入参: matches 匹配结果
func sortFileMatches(matches []FileMatch) {
	sort.SliceStable(matches, func(i, j int) bool {
		return fileMatchLess(matches[i], matches[j])
	})
}

// fileMatchLess 判断字体文件匹配结果优先级
// 入参: left 左侧匹配结果, right 右侧匹配结果
// 返回: bool 左侧是否优先
func fileMatchLess(left, right FileMatch) bool {
	if left.Priority != right.Priority {
		return left.Priority < right.Priority
	}
	if left.Rank != right.Rank {
		return left.Rank < right.Rank
	}
	if left.styleRank != right.styleRank {
		return left.styleRank < right.styleRank
	}
	return left.sortName < right.sortName
}

// fileStyleRank 获取字体文件样式匹配等级
// 入参: suffix 样式后缀, bold 是否粗体, italic 是否斜体
// 返回: int 样式匹配等级
func fileStyleRank(suffix string, bold, italic bool) int {
	fileBold, fileItalic := fileStyleFromSuffix(suffix)
	rank := 0
	if fileBold != bold {
		rank += 2
	}
	if fileItalic != italic {
		rank += 2
	}
	if !bold && !italic && !fileBold && !fileItalic && suffix != "" && !fileRegularSuffix(suffix) {
		rank++
	}
	return rank
}

// fileStyleFromSuffix 获取样式后缀对应的字体样式
// 入参: suffix 样式后缀
// 返回: bool 是否粗体, bool 是否斜体
func fileStyleFromSuffix(suffix string) (bool, bool) {
	bold := suffix == "b" ||
		suffix == "bd" ||
		suffix == "bold" ||
		suffix == "bi" ||
		suffix == "bolditalic" ||
		suffix == "boldoblique" ||
		suffix == "boldslanted" ||
		suffix == "italicbold" ||
		suffix == "obliquebold" ||
		suffix == "slantedbold"
	italic := suffix == "i" ||
		suffix == "it" ||
		suffix == "italic" ||
		suffix == "oblique" ||
		suffix == "slanted" ||
		suffix == "bi" ||
		suffix == "bolditalic" ||
		suffix == "boldoblique" ||
		suffix == "boldslanted" ||
		suffix == "italicbold" ||
		suffix == "obliquebold" ||
		suffix == "slantedbold"
	return bold, italic
}

// fileRegularSuffix 判断是否为常规样式后缀
// 入参: suffix 样式后缀
// 返回: bool 是否为常规样式后缀
func fileRegularSuffix(suffix string) bool {
	switch suffix {
	case "r", "regular", "normal", "roman", "常规":
		return true
	default:
		return false
	}
}

// fileKnownStyleSuffix 判断是否为已知样式后缀
// 入参: suffix 样式后缀
// 返回: bool 是否为已知样式后缀
func fileKnownStyleSuffix(suffix string) bool {
	switch suffix {
	case "r", "regular", "normal", "roman", "常规",
		"b", "bd", "bold",
		"i", "it", "italic", "oblique", "slanted",
		"bi", "bolditalic", "boldoblique", "boldslanted",
		"italicbold", "obliquebold", "slantedbold":
		return true
	default:
		return false
	}
}

// trimSeparatedStyleToken 剥离名称末尾由分隔符引出的样式词
// 入参: name 字体名称
// 返回: string 去除样式词后的基础名称, bool 是否粗体, bool 是否斜体, bool 是否成功剥离
func trimSeparatedStyleToken(name string) (string, bool, bool, bool) {
	trimmed := strings.TrimRight(name, " -_,")
	if trimmed == "" {
		return name, false, false, false
	}
	idx := strings.LastIndexAny(trimmed, " -_,")
	if idx < 0 {
		return name, false, false, false
	}
	prefix := strings.TrimRight(trimmed[:idx], " -_,")
	if prefix == "" {
		return name, false, false, false
	}
	bold, italic, ok := parseStyleToken(strings.ToLower(trimmed[idx+1:]))
	if !ok {
		return name, false, false, false
	}
	return prefix, bold, italic, true
}

// trimCompactStyleSuffix 剥离紧凑拼接在名称末尾的完整样式词
// 入参: name 字体名称
// 返回: string 去除样式词后的基础名称, bool 是否粗体, bool 是否斜体, bool 是否成功剥离
func trimCompactStyleSuffix(name string) (string, bool, bool, bool) {
	trimmed := strings.TrimRight(name, " -_,")
	lower := strings.ToLower(trimmed)
	for _, suffix := range []struct {
		text   string
		bold   bool
		italic bool
	}{
		{"bolditalic", true, true},
		{"boldoblique", true, true},
		{"boldslanted", true, true},
		{"italicbold", true, true},
		{"obliquebold", true, true},
		{"slantedbold", true, true},
		{"bold", true, false},
		{"italic", false, true},
		{"oblique", false, true},
		{"slanted", false, true},
	} {
		if strings.HasSuffix(lower, suffix.text) {
			prefix := strings.TrimRight(trimmed[:len(trimmed)-len(suffix.text)], " -_,")
			if prefix != "" {
				return prefix, suffix.bold, suffix.italic, true
			}
		}
	}
	return name, false, false, false
}

// parseStyleToken 解析独立样式词对应的粗体与斜体标志
// 入参: token 小写样式词
// 返回: bool 是否粗体, bool 是否斜体, bool 是否为已识别样式词
func parseStyleToken(token string) (bool, bool, bool) {
	switch token {
	case "bolditalic", "boldoblique", "boldslanted", "italicbold", "obliquebold", "slantedbold", "bolditalicmt", "boldobliquemt", "boldslantedmt":
		return true, true, true
	case "bold", "boldmt":
		return true, false, true
	case "italic", "oblique", "slanted", "italicmt", "obliquemt", "slantedmt":
		return false, true, true
	case "regular", "normal", "常规", "regularmt":
		return false, false, true
	default:
		return false, false, false
	}
}

// styleNameVariants 生成同族等价斜体样式名称，将Oblique与Slanted视作与Italic等价
// 入参: base 基础字体名称, bold 是否粗体, italic 是否斜体
// 返回: []string 同族等价样式名称列表
func styleNameVariants(base string, bold, italic bool) []string {
	baseNorm := NormalizeName(base)
	if baseNorm == "" || !italic {
		return nil
	}
	if bold {
		return []string{baseNorm + "bolditalic", baseNorm + "boldoblique", baseNorm + "boldslanted"}
	}
	return []string{baseNorm + "italic", baseNorm + "oblique", baseNorm + "slanted"}
}

// equivalentStyleSuffixes 扩展样式后缀的等价斜体变体，保留原始后缀优先顺序
// 入参: suffix 原始后缀, bold 是否粗体, italic 是否斜体
// 返回: []string 等价样式后缀列表
func equivalentStyleSuffixes(suffix string, bold, italic bool) []string {
	result := make([]string, 0, 4)
	if suffix != "" {
		result = append(result, suffix)
	}
	var extra []string
	switch {
	case bold && italic:
		extra = []string{"bolditalic", "boldoblique", "boldslanted"}
	case bold:
		extra = []string{"bold"}
	case italic:
		extra = []string{"italic", "oblique", "slanted"}
	}
	for _, item := range extra {
		if !slices.Contains(result, item) {
			result = append(result, item)
		}
	}
	return result
}

// expandPatternNames 保留原始字体名称并追加剥离样式后的基础家族名称
// 入参: names 原始字体名称列表
// 返回: []string 扩展后的去重名称列表
func expandPatternNames(names []string) []string {
	expanded := make([]string, 0, len(names)*2)
	seen := make(map[string]bool, len(names)*2)
	for _, name := range names {
		expanded = appendFontName(expanded, seen, name)
		if base, _, _ := SplitNameStyle(name); base != "" {
			expanded = appendFontName(expanded, seen, base)
		}
	}
	return expanded
}

// rankCandidate 获取字体文件候选匹配等级
// 入参: file 字体文件候选
// 返回: int 匹配等级
func (m patternMatcher) rankCandidate(file FileCandidate) int {
	exactPath, _ := path.Match(m.pattern, file.base)
	lowerPath, _ := path.Match(m.lowerPattern, file.lowerBase)
	if m.stem == "" {
		if exactPath || lowerPath {
			return MatchFuzzy
		}
		return MatchNone
	}
	name := m.candidateName(file)
	if name == m.stem {
		return MatchExact
	}
	for _, alias := range m.aliases {
		if name == alias {
			return MatchExact
		}
	}
	if fileKnownStyleSuffix(m.styleSuffixNormalized(name)) {
		return MatchExact
	}
	if strings.HasPrefix(name, m.stem) {
		return MatchPartial
	}
	for _, alias := range m.aliases {
		if strings.HasPrefix(name, alias) {
			return MatchPartial
		}
	}
	if exactPath || lowerPath || strings.Contains(name, m.stem) {
		return MatchFuzzy
	}
	for _, alias := range m.aliases {
		if strings.Contains(name, alias) {
			return MatchFuzzy
		}
	}
	return MatchNone
}

// candidateName 保留同族内部名称的区分，只有无关联时使用调用方文件别名
// 入参: file 字体文件候选
// 返回: string 用于统一匹配名称与样式的规范名称
func (m patternMatcher) candidateName(file FileCandidate) string {
	if len(file.names) == 0 || file.base == "" || m.stem == "" {
		return file.normalized
	}
	for _, name := range file.names {
		if strings.HasPrefix(name, m.stem) {
			return file.normalized
		}
		for _, alias := range m.aliases {
			if strings.HasPrefix(name, alias) {
				return file.normalized
			}
		}
	}
	return file.filename
}

// styleSuffixNormalized 获取规范字体名称的样式后缀
// 入参: name 规范字体名称
// 返回: string 样式后缀
func (m patternMatcher) styleSuffixNormalized(name string) string {
	if m.stem == "" || name == "" {
		return ""
	}
	if strings.HasPrefix(name, m.stem) {
		return strings.TrimPrefix(name, m.stem)
	}
	for _, alias := range m.aliases {
		if strings.HasPrefix(name, alias) {
			return strings.TrimPrefix(name, alias)
		}
	}
	return ""
}
