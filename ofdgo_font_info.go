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
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
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

// FontInfo 文档字体诊断信息，MatchedFace为实际匹配字体及原文件中的零起始索引，无法读取时为空
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
	fontPath    string
	bold        bool
	italic      bool
}

// FontInfoScanner 逐页统计字体用量，不保留页面图元，扫描期间不得修改阅读器或渲染器配置
type FontInfoScanner struct {
	renderer *Renderer
	doc      *Document
	index    int
	usage    map[string]int
	pages    []map[string]int
	pending  []int
}

// declaredFontEntry 关联字体声明、所属资源文件作用域键及其包内字体路径
type declaredFontEntry struct {
	key      string
	fontPath string
	font     Font
}

// fontUsagePage 逐图元统计字体的页面解码器
type fontUsagePage struct {
	renderer *Renderer
	usage    map[string]int
}

// fontUsageReferences 保存字体统计中的当前引用路径，不合并共享内容的用量
type fontUsageReferences struct {
	composites map[*CompositeGraphicUnit]bool
	patterns   map[*Pattern]bool
}

// fontUsageObject 仅读取图元的字体、画刷及裁剪依赖，不构造坐标和路径数据
type fontUsageObject struct {
	Font        string       `xml:"Font,attr"`
	DrawParam   string       `xml:"DrawParam,attr"`
	Fill        *bool        `xml:"Fill,attr"`
	Stroke      *bool        `xml:"Stroke,attr"`
	FillColor   *FillColor   `xml:"FillColor"`
	StrokeColor *StrokeColor `xml:"StrokeColor"`
	Clips       *Clips       `xml:"Clips"`
	Border      *ImageBorder `xml:"Border"`
}

// Refresh 为同一文档的局部编辑建立增量扫描，接续未完成扫描并重算已扫描的变化页
// 调用方须保证页序、模板和共享资源语义不变，并提供全部变化页码
// 入参: renderer 新快照的渲染器, pages 变化页码
// 返回: *FontInfoScanner 独立增量扫描器, error 页面不匹配
func (s *FontInfoScanner) Refresh(renderer *Renderer, pages []int) (*FontInfoScanner, error) {
	doc, err := renderer.fontInfoDocument()
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(s.doc.Pages, doc.Pages) || !reflect.DeepEqual(s.doc.CommonData.TemplatePage, doc.CommonData.TemplatePage) || s.renderer.renderAnnotations != renderer.renderAnnotations {
		return nil, fmt.Errorf("font info pages changed")
	}
	pending := slices.Clone(s.pending)
	for _, index := range pages {
		if index < 0 || index >= len(s.pages) {
			return nil, fmt.Errorf("font info page out of range: %d", index)
		}
		if index < s.index && !slices.Contains(pending, index) {
			pending = append(pending, index)
		}
	}
	return &FontInfoScanner{renderer: renderer, doc: doc, index: s.index, usage: maps.Clone(s.usage), pages: slices.Clone(s.pages), pending: pending}, nil
}

// Next 统计下一页的字体用量
// 返回: bool 是否处理了页面，false表示扫描结束
func (s *FontInfoScanner) Next() bool {
	if len(s.pending) != 0 {
		index := s.pending[0]
		s.pending = s.pending[1:]
		for id, count := range s.pages[index] {
			s.usage[id] -= count
			if s.usage[id] == 0 {
				delete(s.usage, id)
			}
		}
		s.pages[index] = nil
		if counts, err := s.renderer.readPageFontUsage(s.doc.Pages.Page[index]); err == nil {
			s.pages[index] = counts
			for id, count := range counts {
				s.usage[id] += count
			}
		}
		return true
	}
	if s.index >= len(s.doc.Pages.Page) {
		return false
	}
	if counts, err := s.renderer.readPageFontUsage(s.doc.Pages.Page[s.index]); err == nil {
		s.pages[s.index] = counts
		for id, count := range counts {
			s.usage[id] += count
		}
	}
	s.index++
	return true
}

// Infos 获取扫描结束后的字体诊断，不返回未完成的用量统计
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (s *FontInfoScanner) Infos() ([]FontInfo, error) {
	if s.index < len(s.doc.Pages.Page) || len(s.pending) != 0 {
		return nil, fmt.Errorf("font info scan is not complete")
	}
	return s.renderer.fontInfos(s.usage)
}

// Fonts 获取OFD声明的文档及页面局部字体列表，保留不同页面资源文件的同名局部字体且不加载注解或签名
// 返回: []Font 字体列表, error 错误信息
func (r *Reader) Fonts() ([]Font, error) {
	entries, err := r.declaredFonts()
	if err != nil {
		return nil, err
	}
	fonts := make([]Font, len(entries))
	for i, entry := range entries {
		fonts[i] = entry.font
	}
	return fonts, nil
}

// FontData 读取内嵌字体，集合按声明名称和样式提取为独立字体，其他格式保留原始字节
// 入参: id 字体资源标识
// 返回: []byte 字体数据, error 资源缺失或集合解析错误
func (r *Reader) FontData(id string) ([]byte, error) {
	return r.fontData(id, false)
}

// FontFace 读取指定内嵌资源的本地化字体信息，集合按资源声明选取，不依赖系统字体
// 入参: id 字体资源标识, platform 名称表平台标识, language 名称表语言标识
// 返回: *FontFace 独立字体信息, error 资源或解析错误
func (r *Reader) FontFace(id string, platform, language uint16) (*FontFace, error) {
	font, err := r.embeddedFontDefinition(id)
	if err != nil {
		return nil, err
	}
	if font == nil || font.FontFile == "" {
		return nil, fmt.Errorf("embedded font %q not found", id)
	}
	return r.localizedFontFaceAt(*font, r.ResPath(font.FontFile), platform, language)
}

// LocalizeFontInfos 按各自包内字体路径更新内嵌字体的本地化名称，消除同名局部字体串台
// 入参: fonts 字体诊断列表, platform 名称表平台标识, language 名称表语言标识
func (r *Renderer) LocalizeFontInfos(fonts []FontInfo, platform, language uint16) {
	if r == nil || r.reader == nil {
		return
	}
	for i := range fonts {
		if !fonts[i].Embedded {
			continue
		}
		fontPath := fonts[i].fontPath
		if fontPath == "" && fonts[i].FontFile != "" {
			fontPath = r.reader.ResPath(fonts[i].FontFile)
		}
		if fontPath == "" {
			continue
		}
		font := Font{ID: fonts[i].ID, FontName: fonts[i].FontName, FamilyName: fonts[i].FamilyName, FontFile: fonts[i].FontFile, Bold: fonts[i].bold, Italic: fonts[i].italic}
		if face, err := r.reader.localizedFontFaceAt(font, fontPath, platform, language); err == nil {
			fonts[i].MatchedFace = face
		}
	}
}

// FontInfos 获取文档字体诊断信息
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

// ScanFontInfos 创建分步字体诊断，页面读取失败的处理与FontInfos一致
// 返回: *FontInfoScanner 字体诊断扫描器, error 错误信息
func (r *Renderer) ScanFontInfos() (*FontInfoScanner, error) {
	doc, err := r.fontInfoDocument()
	if err != nil {
		return nil, err
	}
	return &FontInfoScanner{renderer: r, doc: doc, usage: make(map[string]int), pages: make([]map[string]int, len(doc.Pages.Page))}, nil
}

// DeclaredFontInfos 获取已声明字体的匹配结果，不扫描页面用量
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) DeclaredFontInfos() ([]FontInfo, error) {
	if r == nil || r.reader == nil {
		return nil, fmt.Errorf("ofd renderer is not initialized")
	}
	return r.fontInfos(nil)
}

// FontInfosFromPages 从页面内容获取文档字体诊断信息
// 入参: pages 页面内容列表
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) FontInfosFromPages(pages []*PageContent) ([]FontInfo, error) {
	if _, err := r.fontInfoDocument(); err != nil {
		return nil, err
	}
	return r.fontInfos(r.fontUsage(pages))
}

// UnmarshalXML 解析页面内容中的图层字体
// 入参: d XML解码器, start 页面起始节点
// 返回: error 错误信息
func (p *fontUsagePage) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Page" {
		return fmt.Errorf("expected element type <Page> but have <%s>", start.Name.Local)
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Content" {
			return d.Skip()
		}
		return decodeOFDChildren(d, node, func(d *xml.Decoder, layer xml.StartElement) error {
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
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "PageBlock":
			return p.decodeObjects(d, node, defaults)
		case "TextObject", "PathObject", "ImageObject":
			var obj fontUsageObject
			if err := d.DecodeElement(&obj, &node); err != nil {
				return err
			}
			graphic := GraphicObject{Type: node.Name.Local}
			switch node.Name.Local {
			case "TextObject":
				graphic.TextObject = TextObject{Font: obj.Font, DrawParam: obj.DrawParam, Fill: obj.Fill, Stroke: obj.Stroke, FillColor: obj.FillColor, StrokeColor: obj.StrokeColor, Clips: obj.Clips}
			case "PathObject":
				graphic.PathObject = PathObject{DrawParam: obj.DrawParam, Fill: obj.Fill, Stroke: obj.Stroke, FillColor: obj.FillColor, StrokeColor: obj.StrokeColor, Clips: obj.Clips}
			case "ImageObject":
				graphic.ImageObject = ImageObject{Border: obj.Border, Clips: obj.Clips}
			}
			p.renderer.countObjectFonts(graphic, p.usage, defaults, nil)
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

// UnmarshalXML 读取标准命名空间内的字体依赖，跳过无关图元内容
// 入参: d XML解码器, start 图元起始节点
// 返回: error 解析错误
func (o *fontUsageObject) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain fontUsageObject
	var value plain
	if err := decodeGraphicAttributes(start, &value); err != nil {
		return err
	}
	*o = fontUsageObject(value)
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "FillColor":
			return d.DecodeElement(&o.FillColor, &node)
		case "StrokeColor":
			return d.DecodeElement(&o.StrokeColor, &node)
		case "Clips":
			return d.DecodeElement(&o.Clips, &node)
		case "Border":
			return d.DecodeElement(&o.Border, &node)
		}
		return d.Skip()
	})
}

// localizedFontFaceAt 按指定包内路径读取内嵌字体的本地化名称信息
// 入参: font 字体定义, fontPath 已解析的包内字体路径, platform 名称表平台标识, language 名称表语言标识
// 返回: *FontFace 独立字体信息, error 资源或解析错误
func (r *Reader) localizedFontFaceAt(font Font, fontPath string, platform, language uint16) (*FontFace, error) {
	name := cleanPackagePath(fontPath)
	key := fmt.Sprintf("%s\x00%d:%d", name, platform, language)
	faces, ok := r.fontFaces[key]
	if !ok {
		data, err := r.readFileView(name)
		if err != nil {
			return nil, err
		}
		values, err := (FontFile{Data: data}).LocalizedFaces(platform, language)
		if err != nil {
			return nil, err
		}
		faces = make([]*FontFace, len(values))
		for index := range values {
			faces[index] = &values[index]
		}
		r.fontFaces[key] = faces
	}
	if len(faces) == 0 {
		return nil, fmt.Errorf("font faces empty")
	}
	names := make([][]string, len(faces))
	for index, face := range faces {
		names[index] = face.Names
	}
	face := *faces[basefont.NameIndex(names, []string{font.FontName, font.FamilyName}, font.Bold, font.Italic)]
	face.Names = slices.Clone(face.Names)
	return &face, nil
}

// declaredFonts 收集文档级与页面局部资源中声明的独立字体条目
// 返回: []declaredFontEntry 绑定作用域键的字体列表, error 结构读取错误
func (r *Reader) declaredFonts() ([]declaredFontEntry, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	if !r.fontResourcesRead {
		for _, page := range doc.Pages.Page {
			header, err := r.readPageHeaderContext(context.Background(), page)
			if err == nil {
				_, _ = r.collectPageResourcesContext(context.Background(), r.ResPath(page.BaseLoc), &header)
			}
		}
		for _, template := range doc.CommonData.TemplatePage {
			ref := Page{BaseLoc: template.BaseLoc}
			header, err := r.readPageHeaderContext(context.Background(), ref)
			if err == nil {
				_, _ = r.collectPageResourcesContext(context.Background(), r.ResPath(ref.BaseLoc), &header)
			}
		}
		r.fontResourcesRead = true
	}
	docResFiles := make(map[string]bool, len(doc.CommonData.PublicRes)+len(doc.CommonData.DocumentRes)+len(r.resourcesRead))
	for _, loc := range doc.CommonData.PublicRes {
		docResFiles[cleanPackagePath(r.ResPath(loc))] = true
	}
	for _, loc := range doc.CommonData.DocumentRes {
		docResFiles[cleanPackagePath(r.ResPath(loc))] = true
	}
	for name, ok := range r.resourcesRead {
		if ok {
			docResFiles[cleanPackagePath(name)] = true
		}
	}
	seen := make(map[string]bool, len(r.fontCache))
	entries := make([]declaredFontEntry, 0, len(r.fontCache))
	addEntry := func(prefix string, font *Font) {
		if font == nil {
			return
		}
		key := prefix + "\x00" + imageResourceKey(font.ID)
		if seen[key] {
			return
		}
		seen[key] = true
		fontPath := ""
		if font.FontFile != "" {
			fontPath = r.ResPath(font.FontFile)
		}
		entries = append(entries, declaredFontEntry{key: key, fontPath: fontPath, font: *font})
	}
	for _, id := range slices.Sorted(maps.Keys(r.fontCache)) {
		addEntry("", r.fontCache[id])
	}
	for _, name := range slices.Sorted(maps.Keys(r.resFileCache)) {
		cleanName := cleanPackagePath(name)
		if docResFiles[cleanName] {
			continue
		}
		parsed := r.resFileCache[name]
		if parsed == nil {
			continue
		}
		for _, id := range slices.Sorted(maps.Keys(parsed.fonts.exact)) {
			addEntry(cleanName, parsed.fonts.exact[id])
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].font.ID < entries[j].font.ID
	})
	return entries, nil
}

// fontData 按调用方所有权读取内嵌字体，集合字体始终独立提取
// 入参: id 字体资源标识, readonly 是否允许借用只读资源
// 返回: []byte 字体数据, error 资源或集合错误
func (r *Reader) fontData(id string, readonly bool) ([]byte, error) {
	f, err := r.embeddedFontDefinition(id)
	if err != nil {
		return nil, err
	}
	if f == nil || f.FontFile == "" {
		return nil, fmt.Errorf("embedded font %q not found", id)
	}
	return r.readFontData(f, readonly)
}

// embeddedFontDefinition 按标准优先级定位含有效内嵌文件的字体定义，不被无文件外部声明遮蔽
// 入参: id 字体资源标识
// 返回: *Font 内嵌字体定义, error 结构或资源解析错误
func (r *Reader) embeddedFontDefinition(id string) (*Font, error) {
	if id == "" {
		return nil, nil
	}
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	var fallback *Font
	consider := func(f *Font) bool {
		if f == nil || f.FontFile == "" {
			return false
		}
		if r.hasPackageFile(r.ResPath(f.FontFile)) {
			fallback = f
			return true
		}
		if fallback == nil {
			fallback = f
		}
		return false
	}
	if f, ok := r.docFonts.get(id); ok && consider(f) {
		return fallback, nil
	}
	if f, ok := resourceValue(r.fontCache, id); ok && consider(f) {
		return fallback, nil
	}
	for _, list := range [][]string{doc.CommonData.DocumentRes, doc.CommonData.PublicRes} {
		for _, loc := range slices.Backward(list) {
			parsed, err := r.parseResFileContext(context.Background(), r.ResPath(loc))
			if err != nil {
				return nil, err
			}
			if parsed != nil {
				if f, ok := parsed.fonts.get(id); ok && consider(f) {
					return fallback, nil
				}
			}
		}
	}
	if !r.fontResourcesRead {
		if _, err := r.declaredFonts(); err != nil {
			return nil, err
		}
	}
	seenRes := make(map[string]bool)
	checkPage := func(p Page) *Font {
		hdr, err := r.readPageHeaderContext(context.Background(), p)
		if err != nil {
			return nil
		}
		pagePath := r.ResPath(p.BaseLoc)
		for _, res := range slices.Backward(hdr.PageRes) {
			resFull := cleanPackagePath(resolveResourcePath(pagePath, "", res))
			if resFull == "" || resFull == "." || seenRes[resFull] {
				continue
			}
			seenRes[resFull] = true
			if parsed := r.resFileCache[resFull]; parsed != nil {
				if f, ok := parsed.fonts.get(id); ok && consider(f) {
					return fallback
				}
			}
		}
		return nil
	}
	for _, p := range doc.Pages.Page {
		if hit := checkPage(p); hit != nil {
			return hit, nil
		}
	}
	for _, tpl := range doc.CommonData.TemplatePage {
		if hit := checkPage(Page{BaseLoc: tpl.BaseLoc}); hit != nil {
			return hit, nil
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r.resFileCache)) {
		if seenRes[cleanPackagePath(name)] {
			continue
		}
		if parsed := r.resFileCache[name]; parsed != nil {
			if f, ok := parsed.fonts.get(id); ok && consider(f) {
				return fallback, nil
			}
		}
	}
	return fallback, nil
}

// readFontData 按指定字体定义与调用方所有权读取内嵌字体，集合字体独立提取
// 入参: f 字体定义, readonly 是否允许借用只读资源
// 返回: []byte 字体数据, error 资源或集合错误
func (r *Reader) readFontData(f *Font, readonly bool) ([]byte, error) {
	if f == nil || f.FontFile == "" {
		return nil, fmt.Errorf("embedded font not found")
	}
	read := r.readFile
	if readonly {
		read = r.readFileView
	}
	data, err := read(r.ResPath(f.FontFile))
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("ttcf")) {
		index := basefont.CollectionIndex(data, []string{f.FontName, f.FamilyName}, f.Bold, f.Italic)
		return basefont.ExtractCollectionFont(data, index)
	}
	return data, nil
}

// fontDefinition 按文档级优先查找已加载的字体，未命中时兼容已解析局部资源
// 入参: id 字体资源标识
// 返回: *Font 字体定义，未找到或别名冲突时为nil
func (r *Reader) fontDefinition(id string) *Font {
	if id == "" {
		return nil
	}
	if font, ok := r.docFonts.get(id); ok && font != nil {
		return font
	}
	if font, ok := resourceValue(r.fontCache, id); ok && font != nil {
		return font
	}
	for _, name := range slices.Sorted(maps.Keys(r.resFileCache)) {
		if font, ok := r.resFileCache[name].fonts.get(id); ok && font != nil {
			return font
		}
	}
	return nil
}

// embeddedFontFace 按资源缓存内嵌字体名称并返回独立副本，不解析字形轮廓
// 入参: of OFD字体定义
// 返回: *FontFace 字体信息, error 资源读取错误
func (r *Reader) embeddedFontFace(of Font) (*FontFace, error) {
	return r.embeddedFontFaceAt(of, r.ResPath(of.FontFile))
}

// embeddedFontFaceAt 按指定包内字体路径读取并缓存内嵌字体名称，消除多页同名局部字体串台
// 入参: of OFD字体定义, fontPath 已解析的包内字体文件路径
// 返回: *FontFace 字体信息, error 资源读取错误
func (r *Reader) embeddedFontFaceAt(of Font, fontPath string) (*FontFace, error) {
	name := cleanPackagePath(fontPath)
	faces, ok := r.fontFaces[name]
	if !ok {
		data, err := r.readFileView(name)
		if err != nil {
			return nil, err
		}
		if data, err = decodeFontContainer(data); err == nil {
			if count, err := basefont.FileCount(data); err == nil {
				faces = make([]*FontFace, count)
				for index := range faces {
					if tables, err := basefont.FileTables(data, index); err == nil {
						info := FontFace(basefont.TableFaceInfo(tables, index, 0xffff, 0xffff))
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
	index := basefont.NameIndex(names, []string{of.FontName, of.FamilyName}, of.Bold, of.Italic)
	if faces[index] == nil {
		return nil, nil
	}
	face := *faces[index]
	face.Names = slices.Clone(face.Names)
	return &face, nil
}

// fontInfoDocument 获取字体诊断文档结构
// 返回: *Document 文档结构, error 错误信息
func (r *Renderer) fontInfoDocument() (*Document, error) {
	if r == nil || r.reader == nil {
		return nil, fmt.Errorf("ofd renderer is not initialized")
	}
	doc, err := r.reader.docInternal()
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return doc, nil
}

// fontInfos 获取文档字体诊断信息
// 入参: usage 字体使用次数
// 返回: []FontInfo 字体诊断列表, error 错误信息
func (r *Renderer) fontInfos(usage map[string]int) ([]FontInfo, error) {
	entries, err := r.reader.declaredFonts()
	if err != nil {
		return nil, err
	}
	infos := make([]FontInfo, 0, len(entries)+len(usage))
	for _, entry := range entries {
		info := r.fontInfo(entry.font, entry.fontPath)
		info.Used = usage[entry.key]
		infos = append(infos, info)
	}
	for _, id := range slices.Sorted(maps.Keys(usage)) {
		used := usage[id]
		if used <= 0 || strings.Contains(id, "\x00") {
			continue
		}
		info := r.fontInfo(Font{ID: id}, "")
		info.FontName, info.Used = id, used
		info.Detail = "字体资源未被声明"
		if info.Status == FontStatusFallback {
			info.Detail += "，使用外部字体回退"
		}
		infos = append(infos, info)
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

// fontInfo 按条目自身的包内字体路径获取字体诊断信息
// 入参: font 字体定义, fontPath 已解析的包内字体文件路径
// 返回: FontInfo 字体诊断信息
func (r *Renderer) fontInfo(font Font, fontPath string) FontInfo {
	info := FontInfo{
		ID:         font.ID,
		FontName:   font.FontName,
		FamilyName: font.FamilyName,
		Charset:    font.Charset,
		FontFile:   font.FontFile,
		Embedded:   font.FontFile != "",
	}
	if info.Embedded {
		if fontPath == "" {
			fontPath = r.reader.ResPath(font.FontFile)
		}
		info.fontPath = fontPath
		info.bold = font.Bold
		info.italic = font.Italic
		face, err := r.reader.embeddedFontFaceAt(font, fontPath)
		if err == nil {
			info.Status = FontStatusEmbedded
			info.Matched = path.Base(fontPath)
			info.MatchedFace = face
			info.Detail = "使用内嵌字体文件"
		} else {
			info.Status = FontStatusMissing
			info.Detail = "内嵌字体文件缺失"
		}
		return info
	}
	if r.backends.Fonts != nil {
		if source, metrics := r.fontSourceMatch(r.backends.Fonts, font.ID, &font, false); metrics != nil {
			data, err := r.readFontSource(source, &font)
			if err == nil {
				faces, err := (FontFile{Data: data}).Faces()
				if err == nil && len(faces) != 0 {
					face := faces[0]
					face.Index = source.face
					info.Matched = source.name
					info.MatchedFace = &face
					if source.exact {
						info.Status = FontStatusMatched
						info.Detail = "使用外部字体文件"
					} else {
						info.Status = FontStatusFallback
						info.Detail = "使用外部字体回退"
					}
					return info
				}
			}
		}
	}
	info.Status = FontStatusMissing
	info.Detail = "可用字体文件缺失"
	return info
}

// fontUsage 统计文档字体使用次数
// 入参: pages 页面内容列表
// 返回: map[string]int 字体使用次数
func (r *Renderer) fontUsage(pages []*PageContent) map[string]int {
	usage := make(map[string]int)
	for _, page := range pages {
		if page != nil {
			r.countPageFonts(page, usage)
		}
	}
	return usage
}

// readPageFontUsage 读取页面及实际绑定模板的字体用量并隔离各自局部资源作用域，不保留页面图元
// 入参: page 页面引用
// 返回: map[string]int 字体使用次数, error 错误信息
func (r *Renderer) readPageFontUsage(page Page) (map[string]int, error) {
	header, hdrErr := r.reader.readPageHeader(page)
	var pageRes []*parsedRes
	if hdrErr == nil {
		pageRes, _ = r.reader.collectPageResourcesContext(context.Background(), r.reader.ResPath(page.BaseLoc), &header)
	}
	restore := r.bindParsedResources(pageRes)
	f, err := r.reader.openFile(r.reader.ResPath(page.BaseLoc))
	if err != nil {
		restore()
		return nil, err
	}
	content := fontUsagePage{renderer: r, usage: make(map[string]int)}
	decErr := xml.NewDecoder(f).Decode(&content)
	_, copyErr := io.Copy(io.Discard, f)
	closeErr := f.Close()
	if decErr != nil {
		restore()
		return nil, decErr
	}
	if copyErr != nil {
		restore()
		return nil, copyErr
	}
	if closeErr != nil {
		restore()
		return nil, closeErr
	}
	r.countAnnotationFonts(page.ID, content.usage)
	restore()
	if hdrErr == nil {
		for _, ref := range header.Template {
			if tpl := r.loadTemplate(ref.TemplateID); tpl != nil {
				r.countTemplateFonts(tpl, content.usage)
			}
		}
	}
	return content.usage, nil
}

// countPageFonts 统计页面及其实际绑定模板的字体使用次数
// 入参: page 页面内容, usage 字体使用次数
func (r *Renderer) countPageFonts(page *PageContent, usage map[string]int) {
	restore := r.bindPageResources(page)
	for _, layer := range page.Content.Layer {
		r.countLayerFonts(layer, usage)
	}
	r.countAnnotationFonts(page.ID, usage)
	restore()
	for _, ref := range page.Template {
		if tpl := r.loadTemplate(ref.TemplateID); tpl != nil {
			r.countTemplateFonts(tpl, usage)
		}
	}
}

// countTemplateFonts 绑定模板页自身局部资源作用域并统计其图层字体使用次数
// 入参: tpl 模板页面内容, usage 字体使用次数
func (r *Renderer) countTemplateFonts(tpl *PageContent, usage map[string]int) {
	defer r.bindParsedResources(tpl.pageResources)()
	for _, layer := range tpl.Content.Layer {
		r.countLayerFonts(layer, usage)
	}
}

// countAnnotationFonts 统计页面注释中的字体使用次数
// 入参: pageID 页面标识, usage 字体使用次数
func (r *Renderer) countAnnotationFonts(pageID string, usage map[string]int) {
	if r.renderAnnotations && r.reader != nil {
		annots, _ := r.reader.pageAnnotationsContext(r.outputContext(), pageID)
		for _, annot := range annots {
			if r.printing && annot.Print != nil && !*annot.Print {
				continue
			}
			if annot.Visible != nil && !*annot.Visible {
				continue
			}
			for obj := range annot.Appearance.objects() {
				r.countObjectFonts(*obj, usage, nil, nil)
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
	for _, obj := range layer.CompositeObject {
		r.countCompositeFonts(obj, usage, defaults, nil)
	}
}

// countObjectFonts 统计图元字体使用次数
// 入参: obj 图元对象, usage 字体使用次数, defaults 默认绘制参数, visited 当前引用路径
func (r *Renderer) countObjectFonts(obj GraphicObject, usage map[string]int, defaults *DrawParam, visited *fontUsageReferences) {
	var fill *FillColor
	var stroke *StrokeColor
	var shouldFill, shouldStroke bool
	switch obj.Type {
	case "TextObject":
		text := obj.TextObject
		defaults = r.drawParamDefaults(text.DrawParam, defaults)
		r.countTextFont(text, usage)
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
	case "CompositeObject":
		r.countCompositeFonts(obj.CompositeObject, usage, defaults, visited)
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
// 入参: obj 复合图元对象, usage 字体使用次数, defaults 默认绘制参数, visited 当前引用路径
func (r *Renderer) countCompositeFonts(obj CompositeObject, usage map[string]int, defaults *DrawParam, visited *fontUsageReferences) {
	r.countClipFonts(obj.Clips, usage)
	defaults = r.compositeObjectDefaults(obj, defaults)
	if obj.ResourceID == "" {
		return
	}
	ref := r.compositeDefinition(obj.ResourceID)
	if ref == nil {
		return
	}
	if visited == nil {
		visited = &fontUsageReferences{}
	}
	if visited.composites[ref] {
		return
	}
	if visited.composites == nil {
		visited.composites = make(map[*CompositeGraphicUnit]bool)
	}
	visited.composites[ref] = true
	defer delete(visited.composites, ref)
	for object := range ref.objects() {
		r.countObjectFonts(object, usage, defaults, visited)
	}
}

// countPatternFonts 统计底纹单元字体使用次数
// 入参: fill 填充或描边颜色, usage 字体使用次数, visited 当前引用路径
func (r *Renderer) countPatternFonts(fill *FillColor, usage map[string]int, visited *fontUsageReferences) {
	if fill == nil || fill.Pattern == nil {
		return
	}
	if visited == nil {
		visited = &fontUsageReferences{}
	}
	if visited.patterns[fill.Pattern] {
		return
	}
	if visited.patterns == nil {
		visited.patterns = make(map[*Pattern]bool)
	}
	visited.patterns[fill.Pattern] = true
	defer delete(visited.patterns, fill.Pattern)
	for obj := range fill.Pattern.CellContent.objects() {
		r.countObjectFonts(obj, usage, nil, visited)
	}
}

// scopedFontUsageKey 按当前页面局部资源优先的作用域解析字体统计键
// 入参: id 字体引用标识
// 返回: string 绑定声明文件的作用域键或未声明原始标识
func (r *Renderer) scopedFontUsageKey(id string) string {
	if r.reader == nil || id == "" {
		return id
	}
	for i := len(r.pageResources) - 1; i >= 0; i-- {
		if font, ok := r.pageResources[i].fonts.get(id); ok && font != nil {
			return cleanPackagePath(r.pageResources[i].path) + "\x00" + imageResourceKey(font.ID)
		}
	}
	if font, ok := r.reader.docFonts.get(id); ok && font != nil {
		return "\x00" + imageResourceKey(font.ID)
	}
	if font, ok := resourceValue(r.reader.fontCache, id); ok && font != nil {
		return "\x00" + imageResourceKey(font.ID)
	}
	return id
}

// countTextFont 统计文本字体使用次数
// 入参: text 文本对象, usage 字体使用次数
func (r *Renderer) countTextFont(text TextObject, usage map[string]int) {
	if fontID := r.textObjectFontID(text); fontID != "" {
		usage[r.scopedFontUsageKey(fontID)]++
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
