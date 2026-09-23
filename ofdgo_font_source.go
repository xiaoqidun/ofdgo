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
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/tdewolff/font"
)

// fontSourceKind 字体来源类型
type fontSourceKind uint8

const (
	fontSourceFile fontSourceKind = iota
	fontSourceFS
	fontSourceSystem
)

// fontSource 字体来源
type fontSource struct {
	kind     fontSourceKind
	index    int
	face     int
	name     string
	exact    bool
	priority int
}

// fontSourceKey 字体来源去重键
type fontSourceKey struct {
	kind  fontSourceKind
	index int
	face  int
	name  string
}

// fontSourceCache 保存字体文件候选与原始数据，不包含绘图库对象
type fontSourceCache struct {
	candidates   map[string][]fontSource
	directories  map[string][]fontFileCandidate
	filesystems  map[int][]fontFileCandidate
	data         map[fontSourceKey]fontSourceData
	fallback     ResolvedFont
	fallbackRead bool
}

// fontSourceData 保存读取结果，避免重复打开同一字体文件
type fontSourceData struct {
	data []byte
	err  error
}

// newFontSourceCache 创建独立字体来源缓存
// 返回: *fontSourceCache 字体来源缓存
func newFontSourceCache() *fontSourceCache {
	return &fontSourceCache{candidates: make(map[string][]fontSource), directories: make(map[string][]fontFileCandidate), filesystems: make(map[int][]fontFileCandidate), data: make(map[fontSourceKey]fontSourceData)}
}

// systemFontIndex 串行初始化系统字体索引，不注册绘图后端
var systemFontIndex struct {
	sync.Mutex
	fonts *font.SystemFonts
}

// readFontSource 读取并拆出独立字体，保留集合中的名称和样式选择
// 入参: source 字体来源, definition 字体定义
// 返回: []byte 字体数据, error 读取或集合错误
func (r *Renderer) readFontSource(source fontSource, definition *Font) ([]byte, error) {
	key := fontSourceKey{kind: source.kind, index: source.index, face: source.face, name: source.name}
	if source.kind == fontSourceSystem {
		systemFontIndex.Lock()
		if systemFontIndex.fonts == nil {
			systemFontIndex.fonts, _ = font.FindSystemFonts(font.DefaultFontDirs())
		}
		weight := 400
		if definition.Bold {
			weight = 700
		}
		match, ok := systemFontIndex.fonts.Match(source.name, font.ParseStyleCSS(weight, definition.Italic))
		systemFontIndex.Unlock()
		if !ok {
			return nil, fmt.Errorf("font %q not found", source.name)
		}
		key = fontSourceKey{kind: fontSourceFile, name: match.Filename}
	}
	if cached, ok := r.fontSourcesCache.data[key]; ok {
		return cached.data, cached.err
	}
	var data []byte
	var err error
	if key.kind == fontSourceFS {
		data, err = fs.ReadFile(r.fontFS[key.index], key.name)
	} else {
		data, err = os.ReadFile(key.name)
	}
	if err == nil && bytes.HasPrefix(data, []byte("ttcf")) {
		data, err = extractCollectionFont(data, key.face)
	}
	r.fontSourcesCache.data[key] = fontSourceData{data, err}
	return data, err
}

// fontSourceMatch 匹配可被当前字体后端解析的来源
// 入参: backend 字体后端, fontID 字体ID, definition 字体定义, exact 是否禁止无关回退
// 返回: fontSource 字体来源, FontMetrics 独立字体度量
func (r *Renderer) fontSourceMatch(backend FontBackend, fontID string, definition *Font, exact bool) (fontSource, FontMetrics) {
	for _, source := range r.fontSources(fontID, definition) {
		if exact && !source.exact {
			continue
		}
		data, err := r.readFontSource(source, definition)
		if err != nil {
			continue
		}
		if metrics, err := backend.OpenFont(data); err == nil {
			return source, metrics
		}
	}
	return fontSource{}, nil
}

// resolveFontSource 按公共匹配规则读取字体，由指定后端验证外部字体
// 入参: backend 字体后端, id 字体ID, exact 是否禁止无关回退
// 返回: ResolvedFont 字体及来源, error 解析错误
func (r *Renderer) resolveFontSource(backend FontBackend, id string, exact bool) (ResolvedFont, error) {
	definition := r.Reader.fontCache[id]
	if definition == nil && !r.Reader.fontResourcesRead && r.Reader.OFD != nil {
		if _, err := r.Reader.Fonts(); err != nil {
			return ResolvedFont{}, err
		}
		definition = r.Reader.fontCache[id]
	}
	if definition != nil {
		if definition.FontFile != "" {
			data, err := r.Reader.FontData(id)
			if err != nil {
				return ResolvedFont{}, err
			}
			face, err := r.Reader.embeddedFontFace(*definition)
			if err != nil {
				return ResolvedFont{}, err
			}
			return ResolvedFont{Data: data, Source: path.Base(definition.FontFile), Face: face, Exact: true}, nil
		}
		if source, metrics := r.fontSourceMatch(backend, id, definition, exact); metrics != nil {
			data := metrics.Write()
			faces, err := (FontFile{Data: data}).Faces()
			if err != nil {
				return ResolvedFont{}, err
			}
			face := faces[0]
			face.Index = source.face
			return ResolvedFont{Data: data, Source: source.name, Face: &face, Exact: source.exact}, nil
		}
	}
	if exact {
		return ResolvedFont{}, fmt.Errorf("font %q is unavailable", id)
	}
	if !r.fontSourcesCache.fallbackRead {
		r.fontSourcesCache.fallbackRead = true
		if canLoadSystemFonts() {
			for _, name := range fontDefaultSystemNames() {
				data, err := r.readFontSource(fontSource{kind: fontSourceSystem, name: name}, &Font{})
				if err != nil {
					continue
				}
				metrics, err := backend.OpenFont(data)
				if err == nil {
					r.fontSourcesCache.fallback = ResolvedFont{Data: metrics.Write()}
					break
				}
			}
		}
	}
	return r.fontSourcesCache.fallback, nil
}

// fontSources 获取字体来源列表
// 入参: fontID 字体ID, font OFD字体定义
// 返回: []fontSource 字体来源列表
func (r *Renderer) fontSources(fontID string, font *Font) []fontSource {
	if sources, ok := r.fontSourcesCache.candidates[fontID]; ok {
		return sources
	}
	bold, italic := font.Bold, font.Italic
	names := []string{font.FontName, font.FamilyName}
	sources := make([]fontSource, 0)
	seen := make(map[fontSourceKey]bool)
	for _, dir := range r.fontDirs {
		for _, match := range r.matchFontFiles(dir, names, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFile, name: match.name, face: match.face, exact: true, priority: min(match.priority, 0)})
		}
	}
	for index := range r.fontFS {
		for _, match := range r.matchFontFS(index, names, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: match.name, face: match.face, exact: true, priority: min(match.priority, 0)})
		}
	}
	sortFontSources(sources)
	systemStart := len(sources)
	if canLoadSystemFonts() {
		for _, dir := range systemFontDirs() {
			for _, match := range r.matchFontFiles(dir, names, bold, italic) {
				sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFile, name: match.name, face: match.face, exact: true, priority: min(match.priority, 0)})
			}
		}
		for _, name := range fontSystemNames(names...) {
			source := fontSource{kind: fontSourceSystem, name: name, exact: true}
			for i, requested := range names {
				if fontNormalizeName(name) == fontNormalizeName(requested) {
					source.priority = (i - len(names)) * 2
					break
				}
			}
			sources = appendFontSource(sources, seen, source)
		}
	}
	sortFontSources(sources[systemStart:])
	for index := range r.fontFS {
		for _, match := range r.matchFontFS(index, nil, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: match.name, face: match.face})
		}
	}
	for index, fsys := range r.fontFS {
		names, _ := fs.Glob(fsys, "*")
		for _, name := range names {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: name})
		}
	}
	r.fontSourcesCache.candidates[fontID] = sources
	return sources
}

// sortFontSources 将完整名称匹配排在回退候选之前
// 入参: sources 字体来源列表
func sortFontSources(sources []fontSource) {
	sort.SliceStable(sources, func(i, j int) bool {
		return sources[i].priority < sources[j].priority
	})
}

// appendFontSource 追加字体来源
// 入参: sources 字体来源列表, seen 去重映射, source 字体来源
// 返回: []fontSource 字体来源列表
func appendFontSource(sources []fontSource, seen map[fontSourceKey]bool, source fontSource) []fontSource {
	if source.name == "" {
		return sources
	}
	key := fontSourceKey{kind: source.kind, index: source.index, name: source.name, face: source.face}
	if seen[key] {
		return sources
	}
	seen[key] = true
	return append(sources, source)
}

// matchFontFiles 查找字体文件
// 入参: dir 字体目录, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: []fontFileMatch 字体匹配列表
func (r *Renderer) matchFontFiles(dir string, names []string, bold, italic bool) []fontFileMatch {
	candidates, ok := r.fontSourcesCache.directories[dir]
	if !ok {
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		candidates = fontFileCandidates(files, filepath.Base)
		for i, candidate := range candidates {
			file, err := os.Open(candidate.name)
			if err != nil {
				continue
			}
			candidates = appendFontFileNames(candidates, i, fontFileNames(file))
			file.Close()
		}
		r.fontSourcesCache.directories[dir] = candidates
	}
	return fontFileMatches(candidates, names, bold, italic)
}

// matchFontFS 匹配字体文件系统中的字体名称
// 入参: index 字体文件系统索引, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: []fontFileMatch 字体匹配列表
func (r *Renderer) matchFontFS(index int, names []string, bold, italic bool) []fontFileMatch {
	fsys := r.fontFS[index]
	if fonts, ok := fsys.(*FontFS); ok {
		return fonts.matchStyle(names, bold, italic)
	}
	candidates, ok := r.fontSourcesCache.filesystems[index]
	if !ok {
		files, _ := fs.Glob(fsys, "*")
		candidates = fontFileCandidates(files, path.Base)
		for i, candidate := range candidates {
			file, err := fsys.Open(candidate.name)
			if err != nil {
				continue
			}
			reader, ok := file.(io.ReaderAt)
			if !ok {
				data, _ := io.ReadAll(file)
				reader = bytes.NewReader(data)
			}
			candidates = appendFontFileNames(candidates, i, fontFileNames(reader))
			file.Close()
		}
		r.fontSourcesCache.filesystems[index] = candidates
	}
	return fontFileMatches(candidates, names, bold, italic)
}

// systemFontDirs 获取系统字体目录
// 返回: []string 字体目录列表
func systemFontDirs() []string {
	switch runtime.GOOS {
	case "android":
		return []string{"/product/fonts", "/system/fonts"}
	case "darwin":
		return []string{`/Library/Fonts`}
	case "linux":
		return []string{`/usr/share/fonts`}
	case "windows":
		return []string{`C:\Windows\Fonts`}
	}
	return nil
}
