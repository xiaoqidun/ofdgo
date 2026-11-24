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
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Annotations 注释集合
type Annotations struct {
	XMLName xml.Name  `xml:"Annotations"`
	Page    []AnnPage `xml:"Page"`
}

// AnnPage 页面注释引用
type AnnPage struct {
	PageID  string `xml:"PageID,attr"`
	FileLoc string `xml:"FileLoc"`
}

// PageAnnot 页面注释集合
type PageAnnot struct {
	XMLName xml.Name     `xml:"PageAnnot"`
	Annot   []Annotation `xml:"Annot"`
}

// Annotation 页面注释
type Annotation struct {
	ID          string                `xml:"ID,attr"`
	Type        string                `xml:"Type,attr"`
	Subtype     string                `xml:"Subtype,attr"`
	Creator     string                `xml:"Creator,attr"`
	LastModDate string                `xml:"LastModDate,attr"`
	Visible     *bool                 `xml:"Visible,attr"`
	Print       *bool                 `xml:"Print,attr"`
	NoZoom      bool                  `xml:"NoZoom,attr"`
	NoRotate    bool                  `xml:"NoRotate,attr"`
	ReadOnly    *bool                 `xml:"ReadOnly,attr"`
	Remark      string                `xml:"Remark"`
	Parameters  *AnnotationParameters `xml:"Parameters,omitempty"`
	Appearance  *Appearance           `xml:"Appearance,omitempty"`
}

// AnnotationParameters 保存注释参数集合
type AnnotationParameters struct {
	Parameter []AnnotationParameter `xml:"Parameter"`
}

// AnnotationParameter 保存标准注释参数的名称和值
type AnnotationParameter struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
}

// AnnotationInfo 注释信息，Page从1开始，位置和尺寸使用页面毫米坐标
type AnnotationInfo struct {
	ID          string  `json:"id"`
	Page        int     `json:"page"`
	PageID      string  `json:"pageId"`
	Type        string  `json:"type"`
	Subtype     string  `json:"subtype,omitempty"`
	Creator     string  `json:"creator,omitempty"`
	LastModDate string  `json:"lastModDate,omitempty"`
	Remark      string  `json:"remark,omitempty"`
	Visible     bool    `json:"visible"`
	Print       bool    `json:"print"`
	NoZoom      bool    `json:"noZoom,omitempty"`
	NoRotate    bool    `json:"noRotate,omitempty"`
	ReadOnly    bool    `json:"readOnly"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
}

// Appearance 注释外观
type Appearance struct {
	Boundary        string            `xml:"Boundary,attr"`
	Objects         []GraphicObject   `xml:"-"`
	TextObject      []TextObject      `xml:"TextObject"`
	PathObject      []PathObject      `xml:"PathObject"`
	ImageObject     []ImageObject     `xml:"ImageObject"`
	CompositeObject []CompositeObject `xml:"CompositeObject"`
	PageBlock       []PageBlock       `xml:"PageBlock"`
}

// annotationInfo 提取单条注释摘要，不保留外观图元引用
// 入参: index 页面索引, pageID 页面标识, annotation 注释
// 返回: AnnotationInfo 注释摘要
func annotationInfo(index int, pageID string, annotation Annotation) AnnotationInfo {
	var box Box
	if annotation.Appearance != nil {
		box, _ = ParseBox(annotation.Appearance.Boundary)
	}
	return AnnotationInfo{
		ID:          annotation.ID,
		Page:        index + 1,
		PageID:      pageID,
		Type:        annotation.Type,
		Subtype:     annotation.Subtype,
		Creator:     annotation.Creator,
		LastModDate: annotation.LastModDate,
		Remark:      annotation.Remark,
		Visible:     annotation.Visible == nil || *annotation.Visible,
		Print:       annotation.Print == nil || *annotation.Print,
		NoZoom:      annotation.NoZoom,
		NoRotate:    annotation.NoRotate,
		ReadOnly:    annotation.ReadOnly == nil || *annotation.ReadOnly,
		X:           box.X,
		Y:           box.Y,
		Width:       box.W,
		Height:      box.H,
	}
}

// UnmarshalXML 解析注释入口，按命名空间读取直接页面引用
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (a *Annotations) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Annotations" {
		return fmt.Errorf("expected Annotations root, got %s", start.Name.Local)
	}
	*a = Annotations{XMLName: start.Name}
	return decodeOFDCollection(d, start, "Page", &a.Page)
}

// UnmarshalXML 读取注释所属页面及文件路径，忽略扩展中的同名字段
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (page *AnnPage) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*page = AnnPage{PageID: attrValue(start, "PageID")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "FileLoc" {
			return d.DecodeElement(&page.FileLoc, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析分页注释文件，不将外部同名节点作为注释
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (page *PageAnnot) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "PageAnnot" {
		return fmt.Errorf("expected PageAnnot root, got %s", start.Name.Local)
	}
	*page = PageAnnot{XMLName: start.Name}
	return decodeOFDCollection(d, start, "Annot", &page.Annot)
}

// UnmarshalXML 解析注释属性、说明、参数和外观，保留未指定可见性的默认语义
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (a *Annotation) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = Annotation{
		ID: attrValue(start, "ID"), Type: attrValue(start, "Type"), Subtype: attrValue(start, "Subtype"),
		Creator: attrValue(start, "Creator"), LastModDate: attrValue(start, "LastModDate"),
	}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target *bool
		switch attr.Name.Local {
		case "Visible":
			a.Visible = new(bool)
			target = a.Visible
		case "Print":
			a.Print = new(bool)
			target = a.Print
		case "NoZoom":
			target = &a.NoZoom
		case "NoRotate":
			target = &a.NoRotate
		case "ReadOnly":
			a.ReadOnly = new(bool)
			target = a.ReadOnly
		default:
			continue
		}
		value, err := strconv.ParseBool(strings.TrimSpace(attr.Value))
		if err != nil {
			return err
		}
		*target = value
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Remark":
			return d.DecodeElement(&a.Remark, &node)
		case "Parameters":
			return d.DecodeElement(&a.Parameters, &node)
		case "Appearance":
			return d.DecodeElement(&a.Appearance, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 读取标准注释参数，保留参数顺序和重复名称
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (p *AnnotationParameters) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = AnnotationParameters{}
	return decodeOFDCollection(d, start, "Parameter", &p.Parameter)
}

// UnmarshalXML 读取参数名称与文本，不采用带命名空间的名称属性
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (p *AnnotationParameter) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain AnnotationParameter
	var value plain
	start = ofdXMLStart(start)
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*p = AnnotationParameter(value)
	return nil
}

// AnnotationInfos 按页面顺序获取注释信息，包含不可见注释
// 返回: []AnnotationInfo 注释信息, error 错误信息
func (r *Reader) AnnotationInfos() ([]AnnotationInfo, error) {
	var infos []AnnotationInfo
	err := r.WalkAnnotationInfos(context.Background(), func(info AnnotationInfo) error {
		infos = append(infos, info)
		return nil
	})
	return infos, err
}

// WalkAnnotationInfos 按指定页面顺序访问注释摘要，不解析页面图元，空索引表示全部页面
// 入参: ctx 取消上下文, visit 摘要回调，返回错误即停止, indexes 零基页面索引
// 返回: error 读取、回调或取消错误
func (r *Reader) WalkAnnotationInfos(ctx context.Context, visit func(AnnotationInfo) error, indexes ...int) error {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return err
	}
	if len(indexes) == 0 {
		indexes = make([]int, len(doc.Pages.Page))
		for index := range indexes {
			indexes[index] = index
		}
	}
	for _, index := range indexes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if index < 0 || index >= len(doc.Pages.Page) {
			return fmt.Errorf("page index out of range: %d", index)
		}
		pageID := doc.Pages.Page[index].ID
		annotations, err := r.pageAnnotationsContext(ctx, pageID)
		if err != nil {
			return err
		}
		for _, annotation := range annotations {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(annotationInfo(index, pageID, annotation)); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

// AnnotationInfosByIndex 获取指定页面的注释信息，包含不可见注释，不解析页面图元
// 入参: index 页面索引，从0开始
// 返回: []AnnotationInfo 注释信息, error 错误信息
func (r *Reader) AnnotationInfosByIndex(index int) ([]AnnotationInfo, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Pages.Page) {
		return nil, fmt.Errorf("page index out of range: %d", index)
	}
	pageID := doc.Pages.Page[index].ID
	annotations, err := r.pageAnnotationsContext(context.Background(), pageID)
	if err != nil {
		return nil, err
	}
	var infos []AnnotationInfo
	for _, annotation := range annotations {
		infos = append(infos, annotationInfo(index, pageID, annotation))
	}
	return infos, nil
}

// PageAnnotations 按页面标识获取页面注释列表，按需惰性读取分页注释文件
// 入参: pageID 页面标识
// 返回: []Annotation 页面注释列表, error 错误信息
func (r *Reader) PageAnnotations(pageID string) ([]Annotation, error) {
	return r.PageAnnotationsContext(context.Background(), pageID)
}

// PageAnnotationsContext 按页面标识获取页面注释列表并响应取消
// 入参: ctx 取消上下文, pageID 页面标识
// 返回: []Annotation 页面注释列表, error 读取或取消错误
func (r *Reader) PageAnnotationsContext(ctx context.Context, pageID string) ([]Annotation, error) {
	if _, err := r.docStructureContext(ctx); err != nil {
		return nil, err
	}
	items, err := r.pageAnnotationsContext(ctx, pageID)
	if err != nil {
		return nil, err
	}
	return cloneEditorData(items), nil
}

// objects 按绘制顺序访问注释外观图元，Objects优先于分类数组
// 返回: iter.Seq[*GraphicObject] 图形对象指针序列
func (a *Appearance) objects() iter.Seq[*GraphicObject] {
	return func(yield func(*GraphicObject) bool) {
		if a == nil {
			return
		}
		if len(a.Objects) != 0 {
			for i := range a.Objects {
				if !yield(&a.Objects[i]) {
					return
				}
			}
			return
		}
		for _, object := range a.TextObject {
			item := GraphicObject{Type: "TextObject", TextObject: object}
			if !yield(&item) {
				return
			}
		}
		for _, object := range a.PathObject {
			item := GraphicObject{Type: "PathObject", PathObject: object}
			if !yield(&item) {
				return
			}
		}
		for _, object := range a.ImageObject {
			item := GraphicObject{Type: "ImageObject", ImageObject: object}
			if !yield(&item) {
				return
			}
		}
		for _, object := range a.CompositeObject {
			item := GraphicObject{Type: "CompositeObject", CompositeObject: object}
			if !yield(&item) {
				return
			}
		}
		for i := range a.PageBlock {
			if !yieldPageBlockPtrs(&a.PageBlock[i], nil, yield) {
				return
			}
		}
	}
}

// yieldPageBlockPtrs 递归按绘制顺序遍历页块中的图形对象指针
// 入参: block 页块, prefix 父级页块标识栈, yield 回调函数
// 返回: bool 是否继续遍历
func yieldPageBlockPtrs(block *PageBlock, prefix []string, yield func(*GraphicObject) bool) bool {
	stack := append(slices.Clone(prefix), block.ID)
	if len(block.Objects) != 0 {
		for i := range block.Objects {
			item := block.Objects[i]
			if len(item.pageBlocks) == 0 {
				item.pageBlocks = stack
			}
			if !yield(&item) {
				return false
			}
		}
		return true
	}
	for _, object := range block.TextObject {
		item := GraphicObject{Type: "TextObject", TextObject: object, pageBlocks: stack}
		if !yield(&item) {
			return false
		}
	}
	for _, object := range block.PathObject {
		item := GraphicObject{Type: "PathObject", PathObject: object, pageBlocks: stack}
		if !yield(&item) {
			return false
		}
	}
	for _, object := range block.ImageObject {
		item := GraphicObject{Type: "ImageObject", ImageObject: object, pageBlocks: stack}
		if !yield(&item) {
			return false
		}
	}
	for _, object := range block.CompositeObject {
		item := GraphicObject{Type: "CompositeObject", CompositeObject: object, pageBlocks: stack}
		if !yield(&item) {
			return false
		}
	}
	for i := range block.PageBlock {
		if !yieldPageBlockPtrs(&block.PageBlock[i], stack, yield) {
			return false
		}
	}
	return true
}

// annotationInfos 提取已加载页面的注释摘要列表
// 入参: index 页面索引, pageID 页面标识
// 返回: []AnnotationInfo 注释信息
func (r *Reader) annotationInfos(index int, pageID string) []AnnotationInfo {
	var infos []AnnotationInfo
	for _, annotation := range r.annots[r.resolveAnnotPageID(pageID)] {
		infos = append(infos, annotationInfo(index, pageID, annotation))
	}
	return infos
}

// ensureAnnotationIndexContext 按需读取文档注释入口索引，不解析分页外观内容
// 入参: ctx 取消上下文
// 返回: error 结构读取或取消错误
func (r *Reader) ensureAnnotationIndexContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || (r.doc == nil && r.ofd == nil) {
		return nil
	}
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return err
	}
	if r.annotIndexLoaded {
		return nil
	}
	if err := r.parseAnnotations(ctx, doc); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.annotIndexLoaded = true
	return nil
}

// parseAnnotations 解析注释入口索引，分页注释文件推迟至访问对应页面时按需读取
// 入参: ctx 取消上下文, doc 文档结构
// 返回: error 错误信息
func (r *Reader) parseAnnotations(ctx context.Context, doc *Document) error {
	if doc.Annotations == "" {
		return nil
	}
	if _, err := r.annotationIndexContext(ctx, doc); err != nil {
		return err
	}
	if r.annots == nil {
		r.annots = make(map[string][]Annotation)
	}
	if r.pageAnnotsLoaded == nil {
		r.pageAnnotsLoaded = make(map[string]bool)
	}
	return ctx.Err()
}

// resolveAnnotPageID 按原文与规范资源标识匹配注释页面键
// 入参: pageID 页面标识
// 返回: string 匹配的页面键
func (r *Reader) resolveAnnotPageID(pageID string) string {
	if _, ok := r.pageAnnotFiles[pageID]; ok {
		return pageID
	}
	if _, ok := r.annots[pageID]; ok {
		return pageID
	}
	if key := editorResourceID(pageID); key != "" {
		if len(r.pageAnnotFiles) > 0 {
			if candidate, ok := r.resolvePageAnnotFileByCanon(key); ok {
				return candidate
			}
		}
		if len(r.annots) > 0 {
			if candidate, ok := r.resolveAnnotMapByCanon(key); ok {
				return candidate
			}
		}
		if id, ok := r.resolveDocPageIDByCanon(key); ok {
			return id
		}
	}
	return pageID
}

// resolvePageAnnotFileByCanon 按规范标识常数时间查找注释文件索引中的页面键
// 入参: key 规范化十进制页面标识
// 返回: string 匹配的页面键, bool 是否命中
func (r *Reader) resolvePageAnnotFileByCanon(key string) (string, bool) {
	if r.pageAnnotCanon == nil {
		index := make(map[string]string, len(r.pageAnnotFiles))
		for candidate := range r.pageAnnotFiles {
			if canon := editorResourceID(candidate); canon != "" {
				if prev, exists := index[canon]; !exists || candidate < prev {
					index[canon] = candidate
				}
			}
		}
		r.pageAnnotCanon = index
	}
	candidate, ok := r.pageAnnotCanon[key]
	if ok {
		if _, exists := r.pageAnnotFiles[candidate]; exists {
			return candidate, true
		}
		r.pageAnnotCanon = nil
		return r.resolvePageAnnotFileByCanon(key)
	}
	return "", false
}

// resolveAnnotMapByCanon 按规范标识常数时间查找已加载注释映射中的页面键
// 入参: key 规范化十进制页面标识
// 返回: string 匹配的页面键, bool 是否命中
func (r *Reader) resolveAnnotMapByCanon(key string) (string, bool) {
	rebuild := func() {
		index := make(map[string]string, len(r.annots))
		for candidate := range r.annots {
			if canon := editorResourceID(candidate); canon != "" {
				if prev, exists := index[canon]; !exists || candidate < prev {
					index[canon] = candidate
				}
			}
		}
		r.annotCanon = index
		r.annotCanonLen = len(r.annots)
	}
	rebuilt := false
	if r.annotCanon == nil || r.annotCanonLen != len(r.annots) {
		rebuild()
		rebuilt = true
	}
	candidate, ok := r.annotCanon[key]
	if ok {
		if _, exists := r.annots[candidate]; exists {
			return candidate, true
		}
		if !rebuilt {
			rebuild()
			if candidate, ok = r.annotCanon[key]; ok {
				if _, exists := r.annots[candidate]; exists {
					return candidate, true
				}
			}
		}
		return "", false
	}
	if !rebuilt && len(r.annots) > 0 {
		rebuild()
		if candidate, ok = r.annotCanon[key]; ok {
			if _, exists := r.annots[candidate]; exists {
				return candidate, true
			}
		}
	}
	return "", false
}

// pageAnnotationsContext 按页面标识惰性加载并缓存单页注释
// 入参: ctx 取消上下文, pageID 页面标识
// 返回: []Annotation 页面注释, error 文档读取或取消错误
func (r *Reader) pageAnnotationsContext(ctx context.Context, pageID string) ([]Annotation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	if r.doc != nil || r.ofd != nil {
		if err := r.ensureAnnotationIndexContext(ctx); err != nil {
			return nil, err
		}
	}
	resolvedID := r.resolveAnnotPageID(pageID)
	if r.doc == nil && r.ofd == nil && r.pageAnnotFiles == nil {
		return r.annots[resolvedID], nil
	}
	if r.pageAnnotsLoaded != nil && (r.pageAnnotsLoaded[resolvedID] || r.pageAnnotsLoaded[pageID]) {
		r.pageAnnotsLoaded[resolvedID] = true
		r.pageAnnotsLoaded[pageID] = true
		return r.annots[resolvedID], nil
	}
	paths := r.pageAnnotFiles[resolvedID]
	var loaded []Annotation
	for _, annotPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		af, err := r.openFile(annotPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("open page annotation %s: %w", annotPath, err)
		}
		var pageAnnot PageAnnot
		err = xml.NewDecoder(imageInput{ReadCloser: af, ctx: ctx}).Decode(&pageAnnot)
		_ = af.Close()
		if err != nil {
			return nil, fmt.Errorf("parse page annotation %s: %w", annotPath, err)
		}
		loaded = append(loaded, pageAnnot.Annot...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.annots == nil {
		r.annots = make(map[string][]Annotation)
	}
	if len(loaded) > 0 {
		r.annots[resolvedID] = append(r.annots[resolvedID], loaded...)
		if r.annotCanon != nil {
			if canon := editorResourceID(resolvedID); canon != "" {
				if prev, exists := r.annotCanon[canon]; !exists || resolvedID < prev {
					r.annotCanon[canon] = resolvedID
				}
			}
			r.annotCanonLen = len(r.annots)
		}
	}
	if r.pageAnnotsLoaded == nil {
		r.pageAnnotsLoaded = make(map[string]bool)
	}
	r.pageAnnotsLoaded[resolvedID] = true
	r.pageAnnotsLoaded[pageID] = true
	return r.annots[resolvedID], nil
}

// ensureAllAnnotationsContext 补齐当前文档全部尚未加载的分页注释
// 入参: ctx 取消上下文
// 返回: error 结构读取或取消错误
func (r *Reader) ensureAllAnnotationsContext(ctx context.Context) error {
	if err := r.ensureAnnotationIndexContext(ctx); err != nil {
		return err
	}
	for _, pageID := range slices.Sorted(maps.Keys(r.pageAnnotFiles)) {
		if _, err := r.pageAnnotationsContext(ctx, pageID); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// annotationIndexContext 读取注解索引并规范页引用，取消时不提交文件索引
// 入参: ctx 取消上下文, doc 文档结构
// 返回: Annotations 注解索引, error 读取或取消错误
func (r *Reader) annotationIndexContext(ctx context.Context, doc *Document) (Annotations, error) {
	if err := ctx.Err(); err != nil {
		return Annotations{}, err
	}
	if doc.Annotations == "" {
		return Annotations{}, nil
	}
	annPath := r.ResPath(doc.Annotations)
	f, err := r.openFile(annPath)
	if err != nil {
		return Annotations{}, err
	}
	defer f.Close()
	var annotations Annotations
	if err := xml.NewDecoder(imageInput{ReadCloser: f, ctx: ctx}).Decode(&annotations); err != nil {
		return Annotations{}, err
	}
	pageIDs := make(map[string]string, len(doc.Pages.Page))
	for _, page := range doc.Pages.Page {
		if err := ctx.Err(); err != nil {
			return Annotations{}, err
		}
		if id := editorResourceID(page.ID); id != "" {
			pageIDs[id] = page.ID
		}
	}
	files := make(map[string][]string)
	byPage := make(map[string][]string)
	for i := range annotations.Page {
		if err := ctx.Err(); err != nil {
			return Annotations{}, err
		}
		page := &annotations.Page[i]
		if id := pageIDs[editorResourceID(page.PageID)]; id != "" {
			page.PageID = id
		}
		annotPath := resolveResourcePath(annPath, "", page.FileLoc)
		if file, ok := r.packageFile(annotPath); ok {
			annotPath = cleanPackagePath(file.Name)
		}
		files[annotPath] = append(files[annotPath], page.PageID)
		byPage[page.PageID] = append(byPage[page.PageID], annotPath)
	}
	if err := ctx.Err(); err != nil {
		return Annotations{}, err
	}
	r.annotationFiles = files
	r.pageAnnotFiles = byPage
	r.pageAnnotCanon = nil
	return annotations, nil
}
