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
	"io/fs"
	"path/filepath"
	"runtime"

	"github.com/tdewolff/canvas"
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
	kind  fontSourceKind
	index int
	name  string
	exact bool
}

// fontSourceKey 字体来源去重键
type fontSourceKey struct {
	kind  fontSourceKind
	index int
	name  string
}

// fontCacheKey 字体加载缓存键
type fontCacheKey struct {
	fontSourceKey
	style canvas.FontStyle
}

// loadFont 加载字体
// 入参: fontID 字体ID
// 返回: *canvas.FontFamily 字体族
func (r *Renderer) loadFont(fontID string) *canvas.FontFamily {
	if ff, ok := r.FontMap[fontID]; ok {
		return ff
	}
	var defaultFont *canvas.FontFamily
	if r.defaultFontLoaded {
		defaultFont = r.fontFamily
	}
	of, ok := r.Reader.fontCache[fontID]
	if !ok {
		return defaultFont
	}
	fontStyle := canvasFontStyle(of)
	ff := canvas.NewFontFamily(of.FontName)
	if of.FontFile != "" {
		if fontData, err := r.Reader.ResData(of.FontFile); err == nil {
			if cidMap := getCFFCIDRuneMap(fontData); len(cidMap) > 0 {
				if r.FontCIDMap == nil {
					r.FontCIDMap = make(map[string]map[uint16]rune)
				}
				r.FontCIDMap[fontID] = cidMap
			}
			if _, fixedData, mapping, _, err := FixFontDataAggressive(fontData, true, true); err == nil {
				fontData = fixedData
				if mapping != nil {
					if r.FontGIDMap == nil {
						r.FontGIDMap = make(map[string]map[uint16]rune)
					}
					inv := make(map[uint16]rune)
					for k, v := range mapping {
						if k == packedGlyphRune(v) {
							inv[v] = k
						}
					}
					for k, v := range mapping {
						if _, ok := inv[v]; !ok {
							inv[v] = k
						}
					}
					r.FontGIDMap[fontID] = inv
				}
			}
			if err := ff.LoadFont(fontData, 0, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
			return nil
		}
		return nil
	}
	for _, source := range r.fontSources(fontID, of, fontStyle) {
		if loaded := r.loadFontSource(ff, source, fontStyle); loaded != nil {
			r.FontMap[fontID] = loaded
			r.fontSourceUsed[fontID] = source
			return loaded
		}
	}
	r.FontMap[fontID] = defaultFont
	return defaultFont
}

// canvasFontStyle 获取Canvas字体样式
// 入参: font OFD字体定义
// 返回: canvas.FontStyle Canvas字体样式
func canvasFontStyle(font *Font) canvas.FontStyle {
	var style canvas.FontStyle
	if font.Bold {
		style |= canvas.FontBold
	}
	if font.Italic {
		style |= canvas.FontItalic
	}
	return style
}

// fontSources 获取字体来源列表
// 入参: fontID 字体ID, font OFD字体定义, style Canvas字体样式
// 返回: []fontSource 字体来源列表
func (r *Renderer) fontSources(fontID string, font *Font, style canvas.FontStyle) []fontSource {
	if sources, ok := r.fontSourceCache[fontID]; ok {
		return sources
	}
	bold := style&canvas.FontBold != 0
	italic := style&canvas.FontItalic != 0
	patterns := fontFilePatterns(font.FontName, font.FamilyName)
	sources := make([]fontSource, 0)
	seen := make(map[fontSourceKey]bool)
	for _, dir := range r.fontDirs {
		for _, name := range r.matchFontFiles(dir, patterns, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFile, name: name, exact: true})
		}
	}
	for index, fsys := range r.fontFS {
		for _, name := range fontFSMatchesStyle(fsys, patterns, bold, italic) {
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: name, exact: true})
		}
	}
	if !canLoadSystemFonts() {
		for index, fsys := range r.fontFS {
			names, _ := fs.Glob(fsys, "*")
			for _, name := range names {
				sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFS, index: index, name: name})
			}
		}
		r.fontSourceCache[fontID] = sources
		return sources
	}
	for _, name := range []string{font.FamilyName, font.FontName} {
		for _, systemName := range fontSystemNames(name) {
			for _, match := range r.matchFontFiles(systemFontDir(), fontFilePatterns(systemName), bold, italic) {
				sources = appendFontSource(sources, seen, fontSource{kind: fontSourceFile, name: match, exact: true})
			}
			sources = appendFontSource(sources, seen, fontSource{kind: fontSourceSystem, name: systemName, exact: true})
		}
	}
	r.fontSourceCache[fontID] = sources
	return sources
}

// appendFontSource 追加字体来源
// 入参: sources 字体来源列表, seen 去重映射, source 字体来源
// 返回: []fontSource 字体来源列表
func appendFontSource(sources []fontSource, seen map[fontSourceKey]bool, source fontSource) []fontSource {
	if source.name == "" {
		return sources
	}
	key := fontSourceKey{kind: source.kind, index: source.index, name: source.name}
	if seen[key] {
		return sources
	}
	seen[key] = true
	return append(sources, source)
}

// fontSourceMatch 获取匹配的字体来源
// 入参: fontID 字体ID, font OFD字体定义
// 返回: fontSource 字体来源, bool 是否匹配
func (r *Renderer) fontSourceMatch(fontID string, font *Font) (fontSource, bool) {
	if source, ok := r.fontSourceUsed[fontID]; ok {
		return source, true
	}
	style := canvasFontStyle(font)
	for _, source := range r.fontSources(fontID, font, style) {
		key := fontCacheKey{fontSourceKey: fontSourceKey{kind: source.kind, index: source.index, name: source.name}, style: style}
		if cached, ok := r.fontCache[key]; ok {
			if cached != nil {
				return source, true
			}
			continue
		}
		family := canvas.NewFontFamily(font.FontName)
		if r.loadFontSource(family, source, style) != nil {
			return source, true
		}
	}
	return fontSource{}, false
}

// loadFontSource 加载字体来源
// 入参: family 字体族, source 字体来源, style Canvas字体样式
// 返回: *canvas.FontFamily 字体族
func (r *Renderer) loadFontSource(family *canvas.FontFamily, source fontSource, style canvas.FontStyle) *canvas.FontFamily {
	key := fontCacheKey{fontSourceKey: fontSourceKey{kind: source.kind, index: source.index, name: source.name}, style: style}
	if cached, ok := r.fontCache[key]; ok {
		return cached
	}
	var err error
	switch source.kind {
	case fontSourceFile:
		err = family.LoadFontFile(source.name, style)
	case fontSourceFS:
		var data []byte
		data, err = fs.ReadFile(r.fontFS[source.index], source.name)
		if err == nil {
			err = family.LoadFont(data, 0, style)
		}
	case fontSourceSystem:
		err = family.LoadSystemFont(source.name, style)
	}
	if err != nil {
		r.fontCache[key] = nil
		return nil
	}
	r.fontCache[key] = family
	return family
}

// matchFontFiles 查找字体文件
// 入参: dir 目录, patterns 模式列表, bold 是否粗体, italic 是否斜体
// 返回: []string 文件列表
func (r *Renderer) matchFontFiles(dir string, patterns []string, bold, italic bool) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	candidates := fontFileCandidates(files, filepath.Base)
	matches := make([]fontFileMatch, 0, len(candidates))
	index := make(map[string]int, len(candidates))
	for _, matcher := range newFontPatternMatchers(patterns) {
		for _, file := range candidates {
			rank := matcher.rankCandidate(file)
			appendFontFileMatch(&matches, index, matcher, file, rank, bold, italic)
		}
	}
	sortFontFileMatches(matches)
	return fontFileMatchNames(matches)
}

// systemFontDir 获取系统字体目录
// 返回: string 字体目录
func systemFontDir() string {
	switch runtime.GOOS {
	case "linux":
		return `/usr/share/fonts`
	case "darwin":
		return `/Library/Fonts`
	default:
		return `C:\Windows\Fonts`
	}
}
