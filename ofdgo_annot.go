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
	"fmt"
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
	NoZoom      bool                  `xml:"NoZoom,attr"`
	NoRotate    bool                  `xml:"NoRotate,attr"`
	Remark      string                `xml:"Remark"`
	Parameters  *AnnotationParameters `xml:"Parameters,omitempty"`
	Appearance  Appearance
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
	NoZoom      bool    `json:"noZoom,omitempty"`
	NoRotate    bool    `json:"noRotate,omitempty"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
}

// Appearance 注释外观
type Appearance struct {
	Boundary             string                 `xml:"Boundary,attr"`
	Objects              []GraphicObject        `xml:"-"`
	TextObject           []TextObject           `xml:"TextObject"`
	PathObject           []PathObject           `xml:"PathObject"`
	ImageObject          []ImageObject          `xml:"ImageObject"`
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
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
		case "NoZoom":
			target = &a.NoZoom
		case "NoRotate":
			target = &a.NoRotate
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
	doc, err := r.DocContext(ctx)
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
		for _, annotation := range r.Annots[pageID] {
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
	doc, err := r.Doc()
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Pages.Page) {
		return nil, fmt.Errorf("page index out of range: %d", index)
	}
	return r.annotationInfos(index, doc.Pages.Page[index].ID), nil
}

// annotationInfos 提取指定页面的注释信息
// 入参: index 页面索引, pageID 页面标识
// 返回: []AnnotationInfo 注释信息
func (r *Reader) annotationInfos(index int, pageID string) []AnnotationInfo {
	var infos []AnnotationInfo
	for _, annotation := range r.Annots[pageID] {
		infos = append(infos, annotationInfo(index, pageID, annotation))
	}
	return infos
}

// annotationInfo 提取单条注释摘要，不保留外观图元引用
// 入参: index 页面索引, pageID 页面标识, annotation 注释
// 返回: AnnotationInfo 注释摘要
func annotationInfo(index int, pageID string, annotation Annotation) AnnotationInfo {
	box, _ := ParseBox(annotation.Appearance.Boundary)
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
		NoZoom:      annotation.NoZoom,
		NoRotate:    annotation.NoRotate,
		X:           box.X,
		Y:           box.Y,
		Width:       box.W,
		Height:      box.H,
	}
}

// parseAnnotations 解析注释文件
// 入参: ctx 取消上下文, doc 文档结构
// 返回: error 错误信息
func (r *Reader) parseAnnotations(ctx context.Context, doc *Document) error {
	if doc.Annotations == "" {
		return nil
	}
	annotations, err := r.annotationIndexContext(ctx, doc)
	if err != nil {
		return err
	}
	if r.Annots == nil {
		r.Annots = make(map[string][]Annotation)
	}
	annPath := r.ResPath(doc.Annotations)
	for _, page := range annotations.Page {
		if err := ctx.Err(); err != nil {
			return err
		}
		annotPath := resolveResourcePath(annPath, "", page.FileLoc)
		af, err := r.openFile(annotPath)
		if err != nil {
			continue
		}
		var pageAnnot PageAnnot
		err = xml.NewDecoder(imageInput{ReadCloser: af, ctx: ctx}).Decode(&pageAnnot)
		_ = af.Close()
		if err != nil {
			continue
		}
		r.Annots[page.PageID] = append(r.Annots[page.PageID], pageAnnot.Annot...)
	}
	return ctx.Err()
}

// annotationIndex 读取注解索引，将页引用对应到文档声明的标识，不解析页面外观
// 入参: doc 文档结构
// 返回: Annotations 注解索引, error 错误信息
func (r *Reader) annotationIndex(doc *Document) (Annotations, error) {
	return r.annotationIndexContext(context.Background(), doc)
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
	}
	if err := ctx.Err(); err != nil {
		return Annotations{}, err
	}
	r.annotationFiles = files
	return annotations, nil
}
