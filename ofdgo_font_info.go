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
	"io"
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
	usage := r.fontUsage(doc, pages)
	fonts, err := r.Reader.Fonts()
	if err != nil {
		return nil, err
	}
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
		file, err := r.Reader.openFile(r.Reader.ResPath(font.FontFile))
		if err == nil {
			_, err = io.Copy(io.Discard, file)
			file.Close()
		}
		if err == nil {
			info.Status = FontStatusEmbedded
			info.Matched = path.Base(font.FontFile)
			info.Detail = "使用内嵌字体文件"
		} else {
			info.Status = FontStatusMissing
			info.Detail = "内嵌字体文件缺失"
		}
		return info
	}
	if source, ok := r.fontSourceMatch(font.ID, &font); ok {
		info.Matched = source.name
		if source.exact {
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
		if page := r.loadTemplate(tpl.ID); page != nil {
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
	if r.RenderAnnotations {
		for _, annot := range r.Reader.Annots[page.ID] {
			if annot.Visible != nil && !*annot.Visible {
				continue
			}
			for _, obj := range annot.Appearance.Objects {
				r.countObjectFonts(obj, usage, nil, nil)
			}
		}
	}
}

// countLayerFonts 统计图层字体使用次数
// 入参: layer 图层, usage 字体使用次数
func (r *Renderer) countLayerFonts(layer Layer, usage map[string]int) {
	defaults := r.drawParamDefaults(layer.DrawParam, nil)
	if len(layer.Objects) > 0 {
		for _, obj := range layer.Objects {
			r.countObjectFonts(obj, usage, defaults, nil)
		}
		return
	}
	for _, text := range layer.TextObject {
		r.countObjectFonts(GraphicObject{Type: "TextObject", TextObject: text}, usage, defaults, nil)
	}
	for _, path := range layer.PathObject {
		r.countObjectFonts(GraphicObject{Type: "PathObject", PathObject: path}, usage, defaults, nil)
	}
	for _, image := range layer.ImageObject {
		r.countObjectFonts(GraphicObject{Type: "ImageObject", ImageObject: image}, usage, defaults, nil)
	}
	for _, cgu := range layer.CompositeGraphicUnit {
		r.countCompositeFonts(cgu, usage, defaults, nil)
	}
}

// countObjectFonts 统计图元字体使用次数
// 入参: obj 图元对象, usage 字体使用次数, defaults 默认绘制参数, visited 已访问资源
func (r *Renderer) countObjectFonts(obj GraphicObject, usage map[string]int, defaults *DrawParam, visited map[string]bool) {
	var fill *FillColor
	var stroke *StrokeColor
	var shouldFill, shouldStroke bool
	switch obj.Type {
	case "TextObject":
		text := obj.TextObject
		r.countTextFont(text, usage)
		defaults = r.drawParamDefaults(text.DrawParam, defaults)
		fill, stroke = text.FillColor, text.StrokeColor
		shouldFill = text.Fill == nil || *text.Fill
		shouldStroke = text.Stroke != nil && *text.Stroke
	case "PathObject":
		path := obj.PathObject
		r.countClipFonts(path.Clips, usage)
		defaults = r.drawParamDefaults(path.DrawParam, defaults)
		fill, stroke = path.FillColor, path.StrokeColor
		shouldFill = path.Fill != nil && *path.Fill
		shouldStroke = path.Stroke == nil || *path.Stroke
	case "ImageObject":
		r.countClipFonts(obj.ImageObject.Clips, usage)
		if border := obj.ImageObject.Border; border != nil && (border.LineWidth == nil || *border.LineWidth != 0) {
			r.countPatternFonts((*FillColor)(border.BorderColor), usage, visited)
		}
		return
	case "CompositeGraphicUnit", "CompositeObject":
		r.countCompositeFonts(obj.CompositeGraphicUnit, usage, defaults, visited)
		return
	}
	if defaults != nil {
		if fill == nil {
			fill = defaults.FillColor
		}
		if stroke == nil {
			stroke = defaults.StrokeColor
		}
	}
	if shouldFill {
		r.countPatternFonts(fill, usage, visited)
	}
	if shouldStroke {
		r.countPatternFonts((*FillColor)(stroke), usage, visited)
	}
}

// countCompositeFonts 统计复合图元字体使用次数
// 入参: cgu 复合图元, usage 字体使用次数, defaults 默认绘制参数, visited 已访问资源
func (r *Renderer) countCompositeFonts(cgu CompositeGraphicUnit, usage map[string]int, defaults *DrawParam, visited map[string]bool) {
	r.countClipFonts(cgu.Clips, usage)
	defaults = r.drawParamDefaults(cgu.DrawParam, defaults)
	if cgu.ResourceID != "" {
		if visited == nil {
			visited = make(map[string]bool)
		}
		if visited[cgu.ResourceID] {
			return
		}
		visited[cgu.ResourceID] = true
		if ref := r.CompositeGraphicUnits[cgu.ResourceID]; ref != nil {
			r.countCompositeFonts(*ref, usage, defaults, visited)
		}
		defer delete(visited, cgu.ResourceID)
	}
	if len(cgu.Objects) > 0 {
		for _, obj := range cgu.Objects {
			r.countObjectFonts(obj, usage, defaults, visited)
		}
		return
	}
	for _, text := range cgu.TextObject {
		r.countObjectFonts(GraphicObject{Type: "TextObject", TextObject: text}, usage, defaults, visited)
	}
	for _, path := range cgu.PathObject {
		r.countObjectFonts(GraphicObject{Type: "PathObject", PathObject: path}, usage, defaults, visited)
	}
	for _, image := range cgu.ImageObject {
		r.countObjectFonts(GraphicObject{Type: "ImageObject", ImageObject: image}, usage, defaults, visited)
	}
	for _, sub := range cgu.CompositeGraphicUnit {
		r.countCompositeFonts(sub, usage, defaults, visited)
	}
}

// countPatternFonts 统计底纹单元字体使用次数
// 入参: fill 填充或描边颜色, usage 字体使用次数, visited 已访问资源
func (r *Renderer) countPatternFonts(fill *FillColor, usage map[string]int, visited map[string]bool) {
	if fill == nil || fill.Pattern == nil {
		return
	}
	for _, obj := range fill.Pattern.CellContent.Objects {
		r.countObjectFonts(obj, usage, nil, visited)
	}
}

// countTextFont 统计文本字体使用次数
// 入参: text 文本对象, usage 字体使用次数
func (r *Renderer) countTextFont(text TextObject, usage map[string]int) {
	if fontID := r.textObjectFontID(text); fontID != "" {
		usage[fontID]++
	}
	r.countClipFonts(text.Clips, usage)
}

// countClipFonts 统计裁剪文字字体使用次数
// 入参: clips 裁剪区域集合, usage 字体使用次数
func (r *Renderer) countClipFonts(clips *Clips, usage map[string]int) {
	if clips == nil {
		return
	}
	for _, clip := range clips.Clip {
		for _, area := range clip.Area {
			for _, text := range area.Text {
				r.countTextFont(text, usage)
			}
		}
	}
}
