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
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// FontFile 内存字体文件
type FontFile struct {
	Name string
	Data []byte
}

// FontFS 内存字体文件系统
type FontFS struct {
	files      map[string][]byte
	names      []string
	candidates []fontFileCandidate
}

// NewFontFS 创建内存字体文件系统
// 入参: fonts 字体文件列表
// 返回: *FontFS 内存字体文件系统
func NewFontFS(fonts []FontFile) *FontFS {
	fsys := &FontFS{
		files: make(map[string][]byte),
	}
	for _, font := range fonts {
		name := cleanFontName(font.Name)
		if name == "." || len(font.Data) == 0 {
			continue
		}
		if _, ok := fsys.files[name]; !ok {
			fsys.names = append(fsys.names, name)
		}
		fsys.files[name] = append([]byte(nil), font.Data...)
	}
	sort.Strings(fsys.names)
	fsys.candidates = fontFileCandidates(fsys.names, path.Base)
	return fsys
}

// Len 获取字体文件数量
// 返回: int 字体文件数量
func (fsys *FontFS) Len() int {
	if fsys == nil {
		return 0
	}
	return len(fsys.names)
}

// Open 打开字体文件
// 入参: name 字体文件名
// 返回: fs.File 字体文件, error 错误信息
func (fsys *FontFS) Open(name string) (fs.File, error) {
	name = cleanFontName(name)
	if name == "." {
		return &fontDir{entries: fsys.entries()}, nil
	}
	data, ok := fsys.files[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &fontMemFile{
		Reader: bytes.NewReader(data),
		info: fontFileInfo{
			name: name,
			size: int64(len(data)),
		},
	}, nil
}

// ReadDir 读取字体目录
// 入参: name 目录名
// 返回: []fs.DirEntry 目录条目, error 错误信息
func (fsys *FontFS) ReadDir(name string) ([]fs.DirEntry, error) {
	name = cleanFontName(name)
	if name != "." {
		return nil, fs.ErrNotExist
	}
	return fsys.entries(), nil
}

// Glob 匹配字体文件
// 入参: pattern 匹配模式
// 返回: []string 字体文件列表, error 错误信息
func (fsys *FontFS) Glob(pattern string) ([]string, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}
	return fsys.match(pattern), nil
}

// Match 匹配指定字体名称
// 入参: names 字体名称列表
// 返回: string 匹配字体文件, bool 是否为名称匹配
func (fsys *FontFS) Match(names ...string) (string, bool) {
	return fsys.MatchStyle(false, false, names...)
}

// MatchStyle 匹配指定样式的字体名称
// 入参: bold 是否粗体, italic 是否斜体, names 字体名称列表
// 返回: string 匹配字体文件, bool 是否为名称匹配
func (fsys *FontFS) MatchStyle(bold, italic bool, names ...string) (string, bool) {
	if matches := fsys.matchPatternsStyle(fontFilePatterns(names...), bold, italic); len(matches) > 0 {
		return matches[0], true
	}
	if matches := fsys.fallbackFonts(); len(matches) > 0 {
		return matches[0], false
	}
	return "", false
}

// match 匹配字体文件模式
// 入参: pattern 匹配模式
// 返回: []string 字体文件列表
func (fsys *FontFS) match(pattern string) []string {
	return fsys.matchStyle(pattern, false, false)
}

// matchStyle 匹配指定样式的字体文件模式
// 入参: pattern 匹配模式, bold 是否粗体, italic 是否斜体
// 返回: []string 字体文件列表
func (fsys *FontFS) matchStyle(pattern string, bold, italic bool) []string {
	return fsys.matchPatternsStyle([]string{pattern}, bold, italic)
}

// matchPatternsStyle 匹配指定样式的字体文件模式
// 入参: patterns 匹配模式列表, bold 是否粗体, italic 是否斜体
// 返回: []string 字体文件列表
func (fsys *FontFS) matchPatternsStyle(patterns []string, bold, italic bool) []string {
	matches := make([]fontFileMatch, 0, len(fsys.candidates))
	seen := make(map[string]int, len(fsys.candidates))
	for _, matcher := range newFontPatternMatchers(patterns) {
		for _, file := range fsys.candidates {
			rank := matcher.rankCandidate(file)
			appendFontFileMatch(&matches, seen, matcher, file, rank, bold, italic)
		}
	}
	sortFontFileMatches(matches)
	return fontFileMatchNames(matches)
}

// fallbackFonts 获取回退字体文件
// 返回: []string 字体文件列表
func (fsys *FontFS) fallbackFonts() []string {
	for _, item := range fontFallbackFiles {
		if matches := fsys.match(item); len(matches) > 0 {
			return []string{matches[0]}
		}
	}
	if len(fsys.names) > 0 {
		return []string{fsys.names[0]}
	}
	return nil
}

// entries 获取字体目录条目
// 返回: []fs.DirEntry 目录条目
func (fsys *FontFS) entries() []fs.DirEntry {
	entries := make([]fs.DirEntry, 0, len(fsys.names))
	for _, name := range fsys.names {
		entries = append(entries, fontDirEntry{info: fontFileInfo{
			name: name,
			size: int64(len(fsys.files[name])),
		}})
	}
	return entries
}

// cleanFontName 清理字体文件名
// 入参: name 字体文件名
// 返回: string 清理后的字体文件名
func cleanFontName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSpace(path.Clean(name))
	if name == "" || name == "/" {
		return "."
	}
	name = path.Base(name)
	if name == "." || name == "/" {
		return "."
	}
	return strings.ToLower(name)
}

type fontFileCandidate struct {
	name       string
	base       string
	lowerBase  string
	normalized string
	fold       string
}

// fontFileCandidates 筛选字体文件候选
// 入参: names 文件名列表, base 文件名提取函数
// 返回: []fontFileCandidate 字体文件候选列表
func fontFileCandidates(names []string, base func(string) string) []fontFileCandidate {
	files := make([]fontFileCandidate, 0, len(names))
	for _, name := range names {
		if isFontFileName(name) {
			baseName := base(name)
			files = append(files, fontFileCandidate{
				name:       name,
				base:       baseName,
				lowerBase:  strings.ToLower(baseName),
				normalized: fontNormalizeName(baseName),
				fold:       strings.ToLower(name),
			})
		}
	}
	return files
}

type fontPatternMatcher struct {
	pattern      string
	lowerPattern string
	stem         string
	aliases      []string
	priority     int
}

// newFontPatternMatcher 创建字体文件匹配器
// 入参: pattern 匹配模式
// 返回: fontPatternMatcher 字体文件匹配器
func newFontPatternMatcher(pattern string) fontPatternMatcher {
	matcher := fontPatternMatcher{
		pattern:      pattern,
		lowerPattern: strings.ToLower(pattern),
		stem:         fontPatternStem(pattern),
	}
	if matcher.stem != "" {
		for _, alias := range fontExactCandidateNames(matcher.stem) {
			alias = fontNormalizeName(alias)
			if alias != "" {
				matcher.aliases = append(matcher.aliases, alias)
			}
		}
	}
	return matcher
}

// newFontPatternMatchers 创建字体文件匹配器列表
// 入参: patterns 匹配模式列表
// 返回: []fontPatternMatcher 字体文件匹配器列表
func newFontPatternMatchers(patterns []string) []fontPatternMatcher {
	matchers := make([]fontPatternMatcher, 0, len(patterns))
	for priority, pattern := range patterns {
		matcher := newFontPatternMatcher(pattern)
		matcher.priority = priority
		matchers = append(matchers, matcher)
	}
	return matchers
}

// rank 获取字体文件匹配等级
// 入参: name 字体文件名
// 返回: int 匹配等级
func (m fontPatternMatcher) rank(name string) int {
	return m.rankCandidate(fontFileCandidate{
		base:       name,
		lowerBase:  strings.ToLower(name),
		normalized: fontNormalizeName(name),
	})
}

// rankCandidate 获取字体文件候选匹配等级
// 入参: file 字体文件候选
// 返回: int 匹配等级
func (m fontPatternMatcher) rankCandidate(file fontFileCandidate) int {
	exactPath, _ := path.Match(m.pattern, file.base)
	lowerPath, _ := path.Match(m.lowerPattern, file.lowerBase)
	if m.stem == "" {
		if exactPath || lowerPath {
			return fontMatchFuzzy
		}
		return fontMatchNone
	}
	name := file.normalized
	if name == m.stem {
		return fontMatchExact
	}
	for _, alias := range m.aliases {
		if name == alias {
			return fontMatchExact
		}
	}
	if fontFileKnownStyleSuffix(m.styleSuffixNormalized(name)) {
		return fontMatchExact
	}
	if strings.HasPrefix(name, m.stem) {
		return fontMatchPartial
	}
	for _, alias := range m.aliases {
		if strings.HasPrefix(name, alias) {
			return fontMatchPartial
		}
	}
	if exactPath || lowerPath || strings.Contains(name, m.stem) {
		return fontMatchFuzzy
	}
	for _, alias := range m.aliases {
		if strings.Contains(name, alias) {
			return fontMatchFuzzy
		}
	}
	return fontMatchNone
}

// styleSuffixNormalized 获取规范字体名称的样式后缀
// 入参: name 规范字体名称
// 返回: string 样式后缀
func (m fontPatternMatcher) styleSuffixNormalized(name string) string {
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

type fontFileMatch struct {
	name      string
	priority  int
	rank      int
	styleRank int
	sortName  string
}

// appendFontFileMatch 追加字体文件匹配结果
// 入参: matches 匹配结果, seen 已匹配文件, matcher 匹配器, file 字体文件候选, rank 匹配等级, bold 是否粗体, italic 是否斜体
func appendFontFileMatch(matches *[]fontFileMatch, seen map[string]int, matcher fontPatternMatcher, file fontFileCandidate, rank int, bold, italic bool) {
	if rank == fontMatchNone {
		return
	}
	next := fontFileMatch{
		name:      file.name,
		priority:  matcher.priority,
		rank:      rank,
		styleRank: fontFileStyleRank(matcher.styleSuffixNormalized(file.normalized), bold, italic),
		sortName:  file.lowerBase,
	}
	if index, ok := seen[file.fold]; ok {
		if fontFileMatchLess(next, (*matches)[index]) {
			(*matches)[index] = next
		}
		return
	}
	seen[file.fold] = len(*matches)
	*matches = append(*matches, next)
}

// sortFontFileMatches 排序字体文件匹配结果
// 入参: matches 匹配结果
func sortFontFileMatches(matches []fontFileMatch) {
	sort.SliceStable(matches, func(i, j int) bool {
		return fontFileMatchLess(matches[i], matches[j])
	})
}

// fontFileMatchLess 判断字体文件匹配结果优先级
// 入参: left 左侧匹配结果, right 右侧匹配结果
// 返回: bool 左侧是否优先
func fontFileMatchLess(left, right fontFileMatch) bool {
	if left.priority != right.priority {
		return left.priority < right.priority
	}
	if left.rank != right.rank {
		return left.rank < right.rank
	}
	if left.styleRank != right.styleRank {
		return left.styleRank < right.styleRank
	}
	return left.sortName < right.sortName
}

// fontFileMatchNames 获取字体文件匹配名称
// 入参: matches 匹配结果
// 返回: []string 字体文件列表
func fontFileMatchNames(matches []fontFileMatch) []string {
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match.name)
	}
	return names
}

// fontFileStyleRank 获取字体文件样式匹配等级
// 入参: suffix 样式后缀, bold 是否粗体, italic 是否斜体
// 返回: int 样式匹配等级
func fontFileStyleRank(suffix string, bold, italic bool) int {
	fileBold, fileItalic := fontFileStyleFromSuffix(suffix)
	rank := 0
	if fileBold != bold {
		rank += 2
	}
	if fileItalic != italic {
		rank += 2
	}
	if !bold && !italic && !fileBold && !fileItalic && suffix != "" && !fontFileRegularSuffix(suffix) {
		rank++
	}
	return rank
}

// fontFileStyleFromSuffix 获取样式后缀对应的字体样式
// 入参: suffix 样式后缀
// 返回: bool 是否粗体, bool 是否斜体
func fontFileStyleFromSuffix(suffix string) (bool, bool) {
	bold := suffix == "b" ||
		suffix == "bd" ||
		suffix == "bold" ||
		suffix == "bi" ||
		suffix == "bolditalic" ||
		suffix == "boldoblique"
	italic := suffix == "i" ||
		suffix == "it" ||
		suffix == "italic" ||
		suffix == "oblique" ||
		suffix == "bi" ||
		suffix == "bolditalic" ||
		suffix == "boldoblique"
	return bold, italic
}

// fontFileRegularSuffix 判断是否为常规样式后缀
// 入参: suffix 样式后缀
// 返回: bool 是否为常规样式后缀
func fontFileRegularSuffix(suffix string) bool {
	switch suffix {
	case "r", "regular", "normal", "常规":
		return true
	default:
		return false
	}
}

// fontFileKnownStyleSuffix 判断是否为已知样式后缀
// 入参: suffix 样式后缀
// 返回: bool 是否为已知样式后缀
func fontFileKnownStyleSuffix(suffix string) bool {
	switch suffix {
	case "r", "regular", "normal", "常规",
		"b", "bd", "bold",
		"i", "it", "italic", "oblique",
		"bi", "bolditalic", "boldoblique":
		return true
	default:
		return false
	}
}

// fontMemFile 内存字体文件
type fontMemFile struct {
	*bytes.Reader
	info fontFileInfo
}

// Stat 获取字体文件信息
// 返回: fs.FileInfo 文件信息, error 错误信息
func (f *fontMemFile) Stat() (fs.FileInfo, error) {
	return f.info, nil
}

// Close 关闭字体文件
// 返回: error 错误信息
func (f *fontMemFile) Close() error {
	return nil
}

// fontDir 字体目录
type fontDir struct {
	offset  int
	entries []fs.DirEntry
}

// Stat 获取字体目录信息
// 返回: fs.FileInfo 文件信息, error 错误信息
func (d *fontDir) Stat() (fs.FileInfo, error) {
	return fontFileInfo{name: ".", mode: fs.ModeDir}, nil
}

// Read 读取字体目录数据
// 返回: int 读取字节数, error 错误信息
func (d *fontDir) Read([]byte) (int, error) {
	return 0, io.EOF
}

// Close 关闭字体目录
// 返回: error 错误信息
func (d *fontDir) Close() error {
	return nil
}

// ReadDir 读取字体目录条目
// 入参: count 读取数量
// 返回: []fs.DirEntry 目录条目, error 错误信息
func (d *fontDir) ReadDir(count int) ([]fs.DirEntry, error) {
	if d.offset >= len(d.entries) {
		return nil, io.EOF
	}
	if count <= 0 || d.offset+count > len(d.entries) {
		count = len(d.entries) - d.offset
	}
	entries := d.entries[d.offset : d.offset+count]
	d.offset += count
	return entries, nil
}

// fontDirEntry 字体目录条目
type fontDirEntry struct {
	info fontFileInfo
}

// Name 获取目录条目名称
// 返回: string 目录条目名称
func (e fontDirEntry) Name() string {
	return e.info.Name()
}

// IsDir 判断是否为目录
// 返回: bool 是否为目录
func (e fontDirEntry) IsDir() bool {
	return false
}

// Type 获取目录条目类型
// 返回: fs.FileMode 文件模式
func (e fontDirEntry) Type() fs.FileMode {
	return e.info.Mode().Type()
}

// Info 获取目录条目信息
// 返回: fs.FileInfo 文件信息, error 错误信息
func (e fontDirEntry) Info() (fs.FileInfo, error) {
	return e.info, nil
}

// fontFileInfo 字体文件信息
type fontFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

// Name 获取文件名
// 返回: string 文件名
func (i fontFileInfo) Name() string {
	if i.name == "" {
		return "."
	}
	return i.name
}

// Size 获取文件大小
// 返回: int64 文件大小
func (i fontFileInfo) Size() int64 {
	return i.size
}

// Mode 获取文件模式
// 返回: fs.FileMode 文件模式
func (i fontFileInfo) Mode() fs.FileMode {
	if i.mode != 0 {
		return i.mode
	}
	return 0444
}

// ModTime 获取文件修改时间
// 返回: time.Time 修改时间
func (i fontFileInfo) ModTime() time.Time {
	return time.Time{}
}

// IsDir 判断是否为目录
// 返回: bool 是否为目录
func (i fontFileInfo) IsDir() bool {
	return i.mode.IsDir()
}

// Sys 获取底层文件信息
// 返回: any 底层文件信息
func (i fontFileInfo) Sys() any {
	return nil
}

var _ fs.FS = (*FontFS)(nil)
var _ fs.ReadDirFS = (*FontFS)(nil)
var _ fs.GlobFS = (*FontFS)(nil)
var _ fs.File = (*fontMemFile)(nil)
var _ fs.ReadDirFile = (*fontDir)(nil)
var _ fs.DirEntry = (*fontDirEntry)(nil)
