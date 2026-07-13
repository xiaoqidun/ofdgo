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

// fontFSKey 字体文件系统缓存键
type fontFSKey struct {
	index int
	name  string
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
			var fontStyle canvas.FontStyle
			if of.Bold {
				fontStyle |= canvas.FontBold
			}
			if of.Italic {
				fontStyle |= canvas.FontItalic
			}
			if err := ff.LoadFont(fontData, 0, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
			return nil
		}
		return nil
	}
	var fontStyle canvas.FontStyle
	if of.Bold {
		fontStyle |= canvas.FontBold
	}
	if of.Italic {
		fontStyle |= canvas.FontItalic
	}
	boldStyle := fontStyle&canvas.FontBold != 0
	italicStyle := fontStyle&canvas.FontItalic != 0
	patterns := fontFilePatterns(of.FontName, of.FamilyName)
	for _, dir := range r.fontDirs {
		for _, m := range r.matchFontFiles(dir, patterns, boldStyle, italicStyle) {
			if err := ff.LoadFontFile(m, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
		}
	}
	for index, fsys := range r.fontFS {
		for _, m := range fontFSMatchesStyle(fsys, patterns, boldStyle, italicStyle) {
			if loaded := r.loadFontFromFS(fontID, ff, index, fsys, m, fontStyle); loaded != nil {
				return loaded
			}
		}
	}
	if !canLoadSystemFonts() {
		for index, fsys := range r.fontFS {
			if matches, err := fs.Glob(fsys, "*"); err == nil {
				for _, m := range matches {
					if loaded := r.loadFontFromFS(fontID, ff, index, fsys, m, fontStyle); loaded != nil {
						return loaded
					}
				}
			}
		}
		r.FontMap[fontID] = defaultFont
		return defaultFont
	}
	names := []string{of.FamilyName, of.FontName}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, targetName := range fontSystemNames(name) {
			for _, m := range r.matchFontFiles(systemFontDir(), fontFilePatterns(targetName), boldStyle, italicStyle) {
				if err := ff.LoadFontFile(m, fontStyle); err == nil {
					r.FontMap[fontID] = ff
					return ff
				}
			}
			if err := ff.LoadSystemFont(targetName, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
		}
		for _, m := range r.matchFontFiles(systemFontDir(), fontFilePatterns(name), boldStyle, italicStyle) {
			if err := ff.LoadFontFile(m, fontStyle); err == nil {
				r.FontMap[fontID] = ff
				return ff
			}
		}
	}
	r.FontMap[fontID] = defaultFont
	return defaultFont
}

// loadFontFromFS 从字体文件系统加载字体
// 入参: fontID 字体ID, family 字体族, index 文件系统索引, fsys 字体文件系统, name 字体文件名, style 字体样式
// 返回: *canvas.FontFamily 字体族
func (r *Renderer) loadFontFromFS(fontID string, family *canvas.FontFamily, index int, fsys fs.FS, name string, style canvas.FontStyle) *canvas.FontFamily {
	key := fontFSKey{index: index, name: name, style: style}
	if cached := r.fontFSCache[key]; cached != nil {
		r.FontMap[fontID] = cached
		return cached
	}
	data, err := readFontData(fsys, name)
	if err != nil || family.LoadFont(data, 0, style) != nil {
		return nil
	}
	r.fontFSCache[key] = family
	r.FontMap[fontID] = family
	return family
}

// readFontData 读取字体文件数据
// 入参: fsys 字体文件系统, name 字体文件名
// 返回: []byte 字体文件数据, error 错误信息
func readFontData(fsys fs.FS, name string) ([]byte, error) {
	if fontFS, ok := fsys.(*FontFS); ok {
		data, ok := fontFS.files[cleanFontName(name)]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return data, nil
	}
	return fs.ReadFile(fsys, name)
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
