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
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"

	"github.com/tdewolff/font"
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

// FontInfo OFD字体诊断信息，MatchedFace为实际匹配字体及原文件中的零起始索引，无法读取时为空
type FontInfo struct {
	ID          string    `json:"id"`
	FontName    string    `json:"fontName"`
	FamilyName  string    `json:"familyName"`
	Charset     string    `json:"charset"`
	FontFile    string    `json:"fontFile"`
	Embedded    bool      `json:"embedded"`
	Status      string    `json:"status"`
	Matched     string    `json:"matched"`
	MatchedFace *FontFace `json:"matchedFace,omitempty"`
	Detail      string    `json:"detail"`
	Used        int       `json:"used"`
}

// Fonts 获取OFD声明的字体列表
// 返回: []Font 字体列表, error 错误信息
func (r *Reader) Fonts() ([]Font, error) {
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	if !r.fontResourcesRead {
		for _, page := range doc.Pages.Page {
			r.loadPageResources(page)
		}
		for _, template := range doc.CommonData.TemplatePage {
			r.loadPageResources(Page{BaseLoc: template.BaseLoc})
		}
		r.fontResourcesRead = true
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

// FontData 读取内嵌字体，集合按声明名称和样式提取为独立字体，其他格式保留原始字节
// 入参: id 字体资源标识
// 返回: []byte 字体数据, error 资源缺失或集合解析错误
func (r *Reader) FontData(id string) ([]byte, error) {
	if r.fontCache[id] == nil {
		if _, err := r.Fonts(); err != nil {
			return nil, err
		}
	}
	f := r.fontCache[id]
	if f == nil || f.FontFile == "" {
		return nil, fmt.Errorf("embedded font %q not found", id)
	}
	data, err := r.ResData(f.FontFile)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("ttcf")) {
		index := fontCollectionIndex(data, []string{f.FontName, f.FamilyName}, f.Bold, f.Italic)
		return extractCollectionFont(data, index)
	}
	return data, nil
}

// embeddedFontFace 按资源缓存内嵌字体名称并返回独立副本，不解析字形轮廓
// 入参: of OFD字体定义
// 返回: *FontFace 字体信息, error 资源读取错误
func (r *Reader) embeddedFontFace(of Font) (*FontFace, error) {
	name := r.ResPath(of.FontFile)
	faces, ok := r.fontFaces[name]
	if !ok {
		data, err := r.readFile(name)
		if err != nil {
			return nil, err
		}
		if data, err = font.ToSFNT(data); err == nil {
			if count, err := fontFileCount(data); err == nil {
				faces = make([]*FontFace, count)
				for index := range faces {
					if tables, err := fontFileTables(data, index); err == nil {
						info := fontFaceInfo(tables["name"], index)
						faces[index] = &info
					}
				}
			}
		}
		r.fontFaces[name] = faces
	}
	if len(faces) == 0 {
		return nil, nil
	}
	names := make([][]string, len(faces))
	for index, face := range faces {
		if face != nil {
			names[index] = face.Names
		}
	}
	index := fontNameIndex(names, []string{of.FontName, of.FamilyName}, of.Bold, of.Italic)
	if faces[index] == nil {
		return nil, nil
	}
	face := *faces[index]
	face.Names = slices.Clone(face.Names)
	return &face, nil
}

// FontInfos 获取OFD字体诊断信息
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) FontInfos() ([]FontInfo, error) {
	scanner, err := r.ScanFontInfos()
	if err != nil {
		return nil, err
	}
	for scanner.Next() {
	}
	return scanner.Infos()
}

// FontInfoScanner 逐页统计字体用量，不保留页面图元，扫描期间不得修改阅读器或渲染器配置
type FontInfoScanner struct {
	renderer *Renderer
	doc      *Document
	index    int
	usage    map[string]int
}

// ScanFontInfos 创建分步字体诊断，页面读取失败的处理与FontInfos一致
// 返回: *FontInfoScanner 字体诊断扫描器, error 错误信息
func (r *Renderer) ScanFontInfos() (*FontInfoScanner, error) {
	doc, err := r.fontInfoDocument()
	if err != nil {
		return nil, err
	}
	return &FontInfoScanner{renderer: r, doc: doc, usage: make(map[string]int)}, nil
}

// Next 统计下一页或模板的字体用量
// 返回: bool 是否处理了页面，false表示扫描结束
func (s *FontInfoScanner) Next() bool {
	pages := len(s.doc.Pages.Page)
	if s.index < pages {
		if counts, err := s.renderer.readPageFontUsage(s.doc.Pages.Page[s.index]); err == nil {
			for id, count := range counts {
				s.usage[id] += count
			}
		}
	} else if index := s.index - pages; index < len(s.doc.CommonData.TemplatePage) {
		if page := s.renderer.loadTemplate(s.doc.CommonData.TemplatePage[index].ID); page != nil {
			s.renderer.countPageFonts(page, s.usage)
		}
	} else {
		return false
	}
	s.index++
	return true
}

// Infos 获取扫描结束后的字体诊断，不返回未完成的用量统计
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (s *FontInfoScanner) Infos() ([]FontInfo, error) {
	if s.index < len(s.doc.Pages.Page)+len(s.doc.CommonData.TemplatePage) {
		return nil, fmt.Errorf("font info scan is not complete")
	}
	return s.renderer.fontInfos(s.usage)
}

// FontInfosFromPages 从页面内容获取OFD字体诊断信息
// 入参: pages 页面内容列表
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) FontInfosFromPages(pages []*PageContent) ([]FontInfo, error) {
	doc, err := r.fontInfoDocument()
	if err != nil {
		return nil, err
	}
	return r.fontInfos(r.fontUsage(doc, pages))
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
// 入参: usage 字体使用次数
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) fontInfos(usage map[string]int) ([]FontInfo, error) {
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
		face, err := r.Reader.embeddedFontFace(font)
		if err == nil {
			info.Status = FontStatusEmbedded
			info.Matched = path.Base(font.FontFile)
			info.MatchedFace = face
			info.Detail = "使用内嵌字体文件"
		} else {
			info.Status = FontStatusMissing
			info.Detail = "内嵌字体文件缺失"
		}
		return info
	}
	if resolved, err := r.ResolveFont(font.ID, false); err == nil && resolved.Source != "" {
		info.Matched = resolved.Source
		info.MatchedFace = resolved.Face
		if resolved.Exact {
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
// 入参: doc 文档结构, pages 页面内容列表
// 返回: map[string]int 字体使用次数
func (r *Renderer) fontUsage(doc *Document, pages []*PageContent) map[string]int {
	usage := make(map[string]int)
	for _, page := range pages {
		if page != nil {
			r.countPageFonts(page, usage)
		}
	}
	for _, tpl := range doc.CommonData.TemplatePage {
		if page := r.loadTemplate(tpl.ID); page != nil {
			r.countPageFonts(page, usage)
		}
	}
	return usage
}

// fontUsagePage 逐图元统计字体的页面解码器
type fontUsagePage struct {
	renderer *Renderer
	usage    map[string]int
}

// readPageFontUsage 读取页面字体用量，不保留页面图元
// 入参: page 页面引用
// 返回: map[string]int 字体使用次数, error 错误信息
func (r *Renderer) readPageFontUsage(page Page) (map[string]int, error) {
	r.Reader.loadPageResources(page)
	f, err := r.Reader.openFile(r.Reader.ResPath(page.BaseLoc))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content := fontUsagePage{renderer: r, usage: make(map[string]int)}
	if err := xml.NewDecoder(f).Decode(&content); err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, f); err != nil {
		return nil, err
	}
	r.countAnnotationFonts(page.ID, content.usage)
	return content.usage, nil
}

// UnmarshalXML 解析页面内容中的图层字体
// 入参: d XML解码器, start 页面起始节点
// 返回: error 错误信息
func (p *fontUsagePage) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Page" {
		return fmt.Errorf("expected element type <Page> but have <%s>", start.Name.Local)
	}
	return decodeObjectContainer(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Content" {
			return d.Skip()
		}
		return decodeObjectContainer(d, node, func(d *xml.Decoder, layer xml.StartElement) error {
			if layer.Name.Local != "Layer" {
				return d.Skip()
			}
			return p.decodeObjects(d, layer, p.renderer.drawParamDefaults(attrValue(layer, "DrawParam"), nil))
		})
	})
}

// decodeObjects 逐个读取图元并复用字体统计规则
// 入参: d XML解码器, start 容器起始节点, defaults 默认绘制参数
// 返回: error 错误信息
func (p *fontUsagePage) decodeObjects(d *xml.Decoder, start xml.StartElement, defaults *DrawParam) error {
	return decodeObjectContainer(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "PageBlock":
			return p.decodeObjects(d, node, defaults)
		case "TextObject":
			var obj TextObject
			if err := d.DecodeElement(&obj, &node); err != nil {
				return err
			}
			p.renderer.countObjectFonts(GraphicObject{Type: "TextObject", TextObject: obj}, p.usage, defaults, nil)
			return nil
		case "PathObject":
			var obj struct {
				PathObject
				AbbreviatedData struct{} `xml:"AbbreviatedData"`
			}
			if err := d.DecodeElement(&obj, &node); err != nil {
				return err
			}
			p.renderer.countObjectFonts(GraphicObject{Type: "PathObject", PathObject: obj.PathObject}, p.usage, defaults, nil)
			return nil
		case "ImageObject":
			var obj ImageObject
			if err := d.DecodeElement(&obj, &node); err != nil {
				return err
			}
			p.renderer.countObjectFonts(GraphicObject{Type: "ImageObject", ImageObject: obj}, p.usage, defaults, nil)
			return nil
		}
		var layer Layer
		if err := layer.decodeObject(d, node); err != nil {
			return err
		}
		for _, obj := range layer.Objects {
			p.renderer.countObjectFonts(obj, p.usage, defaults, nil)
		}
		return nil
	})
}

// countPageFonts 统计页面字体使用次数
// 入参: page 页面内容, usage 字体使用次数
func (r *Renderer) countPageFonts(page *PageContent, usage map[string]int) {
	for _, layer := range page.Content.Layer {
		r.countLayerFonts(layer, usage)
	}
	r.countAnnotationFonts(page.ID, usage)
}

// countAnnotationFonts 统计页面注释中的字体使用次数
// 入参: pageID 页面标识, usage 字体使用次数
func (r *Renderer) countAnnotationFonts(pageID string, usage map[string]int) {
	if r.RenderAnnotations {
		for _, annot := range r.Reader.Annots[pageID] {
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
