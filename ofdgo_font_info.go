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
	"io/fs"
	"path"
	"sort"
)

const (
	// FontStatusEmbedded 内嵌字体可用
	FontStatusEmbedded = "embedded"
	// FontStatusMatched 外部字体匹配
	FontStatusMatched = "matched"
	// FontStatusFallback 外部字体回退
	FontStatusFallback = "fallback"
	// FontStatusMissing 字体资源缺失
	FontStatusMissing = "missing"
)

// FontInfo OFD字体诊断信息
type FontInfo struct {
	ID         string `json:"id"`
	FontName   string `json:"fontName"`
	FamilyName string `json:"familyName"`
	Charset    string `json:"charset"`
	FontFile   string `json:"fontFile"`
	Embedded   bool   `json:"embedded"`
	Status     string `json:"status"`
	Matched    string `json:"matched"`
	Detail     string `json:"detail"`
	Used       int    `json:"used"`
}

// Fonts 获取OFD声明的字体列表
// 返回: []Font 字体列表, error 错误信息
func (r *Reader) Fonts() ([]Font, error) {
	if _, err := r.Doc(); err != nil {
		return nil, err
	}
	fonts := make([]Font, 0, len(r.fontCache))
	for _, font := range r.fontCache {
		if font != nil {
			fonts = append(fonts, *font)
		}
	}
	sort.SliceStable(fonts, func(i, j int) bool {
		return fonts[i].ID < fonts[j].ID
	})
	return fonts, nil
}

// FontInfos 获取OFD字体诊断信息
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) FontInfos() ([]FontInfo, error) {
	doc, err := r.fontInfoDocument()
	if err != nil {
		return nil, err
	}
	return r.fontInfos(doc, nil)
}

// FontInfosFromPages 从页面内容获取OFD字体诊断信息
// 入参: pages 页面内容列表
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) FontInfosFromPages(pages []*PageContent) ([]FontInfo, error) {
	doc, err := r.fontInfoDocument()
	if err != nil {
		return nil, err
	}
	if pages == nil {
		pages = []*PageContent{}
	}
	return r.fontInfos(doc, pages)
}

// fontInfoDocument 获取字体诊断文档结构
// 返回: *Document 文档结构, error 错误信息
func (r *Renderer) fontInfoDocument() (*Document, error) {
	if r == nil || r.Reader == nil {
		return nil, fmt.Errorf("ofd renderer is not initialized")
	}
	doc, err := r.Reader.Doc()
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return doc, nil
}

// fontInfos 获取OFD字体诊断信息
// 入参: doc 文档结构, pages 页面内容列表
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) fontInfos(doc *Document, pages []*PageContent) ([]FontInfo, error) {
	fonts, err := r.Reader.Fonts()
	if err != nil {
		return nil, err
	}
	usage := r.fontUsage(doc, pages)
	infos := make([]FontInfo, 0, len(fonts)+len(usage))
	seen := make(map[string]bool)
	for _, font := range fonts {
		info := r.fontInfo(font)
		info.Used = usage[font.ID]
		infos = append(infos, info)
		seen[font.ID] = true
	}
	for id, used := range usage {
		if seen[id] {
			continue
		}
		infos = append(infos, FontInfo{
			ID:       id,
			FontName: id,
			Status:   FontStatusMissing,
			Detail:   "字体资源未被声明",
			Used:     used,
		})
	}
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].Used == 0 && infos[j].Used > 0 {
			return false
		}
		if infos[i].Used > 0 && infos[j].Used == 0 {
			return true
		}
		return infos[i].ID < infos[j].ID
	})
	return infos, nil
}

// fontInfo 获取单个字体诊断信息
// 入参: font 字体定义
// 返回: FontInfo 字体诊断信息
func (r *Renderer) fontInfo(font Font) FontInfo {
	info := FontInfo{
		ID:         font.ID,
		FontName:   font.FontName,
		FamilyName: font.FamilyName,
		Charset:    font.Charset,
		FontFile:   font.FontFile,
		Embedded:   font.FontFile != "",
	}
	if info.Embedded {
		if _, err := r.Reader.ResData(font.FontFile); err == nil {
			info.Status = FontStatusEmbedded
			info.Matched = path.Base(font.FontFile)
			info.Detail = "使用内嵌字体文件"
		} else {
			info.Status = FontStatusMissing
			info.Detail = "内嵌字体文件缺失"
		}
		return info
	}
	if matched, exact := r.matchFont(font.FontName, font.FamilyName); matched != "" {
		info.Matched = matched
		if exact {
			info.Status = FontStatusMatched
			info.Detail = "使用外部字体文件"
		} else {
			info.Status = FontStatusFallback
			info.Detail = "使用外部字体回退"
		}
		return info
	}
	info.Status = FontStatusMissing
	info.Detail = "可用字体文件缺失"
	return info
}

// matchFont 匹配外部字体
// 入参: names 字体名称列表
// 返回: string 匹配字体文件, bool 是否为名称匹配
func (r *Renderer) matchFont(names ...string) (string, bool) {
	patterns := fontFilePatterns(names...)
	for _, dir := range r.fontDirs {
		if matches := r.matchFontFiles(dir, patterns, false, false); len(matches) > 0 {
			return matches[0], true
		}
	}
	for _, fsys := range r.fontFS {
		if matcher, ok := fsys.(interface {
			Match(...string) (string, bool)
		}); ok {
			if matched, exact := matcher.Match(names...); matched != "" {
				return matched, exact
			}
			continue
		}
		if matched := matchFontFS(fsys, names...); matched != "" {
			return matched, true
		}
	}
	if !canLoadSystemFonts() {
		for _, fsys := range r.fontFS {
			if matched := fallbackFontFS(fsys); matched != "" {
				return matched, false
			}
		}
	}
	return "", false
}

// matchFontFS 从字体文件系统匹配字体
// 入参: fsys 字体文件系统, names 字体名称列表
// 返回: string 匹配字体文件
func matchFontFS(fsys fs.FS, names ...string) string {
	if matches := fontFSMatches(fsys, fontFilePatterns(names...)); len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// fallbackFontFS 从字体文件系统获取回退字体
// 入参: fsys 字体文件系统
// 返回: string 回退字体文件
func fallbackFontFS(fsys fs.FS) string {
	for _, pattern := range fontFallbackFiles {
		if matches := fontFSMatches(fsys, []string{pattern}); len(matches) > 0 {
			return matches[0]
		}
	}
	all, _ := fs.Glob(fsys, "*")
	for _, name := range all {
		if isFontFileName(name) {
			return name
		}
	}
	return ""
}

// fontFSMatches 匹配字体文件系统中的字体文件
// 入参: fsys 字体文件系统, patterns 匹配模式列表
// 返回: []string 字体文件列表
func fontFSMatches(fsys fs.FS, patterns []string) []string {
	return fontFSMatchesStyle(fsys, patterns, false, false)
}

// fontFSMatchesStyle 匹配字体文件系统中的指定样式字体文件
// 入参: fsys 字体文件系统, patterns 匹配模式列表, bold 是否粗体, italic 是否斜体
// 返回: []string 字体文件列表
func fontFSMatchesStyle(fsys fs.FS, patterns []string, bold, italic bool) []string {
	names, _ := fs.Glob(fsys, "*")
	candidates := fontFileCandidates(names, path.Base)
	matches := make([]fontFileMatch, 0, len(candidates))
	seen := make(map[string]int, len(candidates))
	for _, matcher := range newFontPatternMatchers(patterns) {
		for _, file := range candidates {
			rank := matcher.rankCandidate(file)
			appendFontFileMatch(&matches, seen, matcher, file, rank, bold, italic)
		}
	}
	sortFontFileMatches(matches)
	return fontFileMatchNames(matches)
}

// fontUsage 统计文档字体使用次数
// 入参: doc 文档结构, pages 页面内容列表, nil表示读取文档页面
// 返回: map[string]int 字体使用次数
func (r *Renderer) fontUsage(doc *Document, pages []*PageContent) map[string]int {
	usage := make(map[string]int)
	if pages == nil {
		for _, pageRef := range doc.Pages.Page {
			if page, err := r.Reader.PageContent(pageRef); err == nil {
				r.countPageFonts(page, usage)
			}
		}
	} else {
		for _, page := range pages {
			if page != nil {
				r.countPageFonts(page, usage)
			}
		}
	}
	for _, tpl := range doc.CommonData.TemplatePage {
		page, err := r.Reader.PageContent(Page{BaseLoc: tpl.BaseLoc})
		if err == nil {
			r.countPageFonts(page, usage)
		}
	}
	return usage
}

// countPageFonts 统计页面字体使用次数
// 入参: page 页面内容, usage 字体使用次数
func (r *Renderer) countPageFonts(page *PageContent, usage map[string]int) {
	for _, layer := range page.Content.Layer {
		r.countLayerFonts(layer, usage)
	}
}

// countLayerFonts 统计图层字体使用次数
// 入参: layer 图层, usage 字体使用次数
func (r *Renderer) countLayerFonts(layer Layer, usage map[string]int) {
	if len(layer.Objects) > 0 {
		for _, obj := range layer.Objects {
			r.countObjectFonts(obj, usage)
		}
		return
	}
	for _, text := range layer.TextObject {
		r.countTextFont(text, usage)
	}
	for _, cgu := range layer.CompositeGraphicUnit {
		r.countCompositeFonts(cgu, usage, nil)
	}
}

// countObjectFonts 统计图元字体使用次数
// 入参: obj 图元对象, usage 字体使用次数
func (r *Renderer) countObjectFonts(obj GraphicObject, usage map[string]int) {
	switch obj.Type {
	case "TextObject":
		r.countTextFont(obj.TextObject, usage)
	case "CompositeGraphicUnit", "CompositeObject":
		r.countCompositeFonts(obj.CompositeGraphicUnit, usage, nil)
	}
}

// countCompositeFonts 统计复合图元字体使用次数
// 入参: cgu 复合图元, usage 字体使用次数, visited 已访问资源
func (r *Renderer) countCompositeFonts(cgu CompositeGraphicUnit, usage map[string]int, visited map[string]bool) {
	if cgu.ResourceID != "" {
		if visited == nil {
			visited = make(map[string]bool)
		}
		if visited[cgu.ResourceID] {
			return
		}
		visited[cgu.ResourceID] = true
		if ref := r.CompositeGraphicUnits[cgu.ResourceID]; ref != nil {
			r.countCompositeFonts(*ref, usage, visited)
		}
	}
	if len(cgu.Objects) > 0 {
		for _, obj := range cgu.Objects {
			r.countObjectFonts(obj, usage)
		}
		return
	}
	for _, text := range cgu.TextObject {
		r.countTextFont(text, usage)
	}
	for _, sub := range cgu.CompositeGraphicUnit {
		r.countCompositeFonts(sub, usage, visited)
	}
}

// countTextFont 统计文本字体使用次数
// 入参: text 文本对象, usage 字体使用次数
func (r *Renderer) countTextFont(text TextObject, usage map[string]int) {
	if fontID := r.textObjectFontID(text); fontID != "" {
		usage[fontID]++
	}
}
