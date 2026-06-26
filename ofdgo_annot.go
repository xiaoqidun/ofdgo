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

import "encoding/xml"

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
	ID          string `xml:"ID,attr"`
	Type        string `xml:"Type,attr"`
	Subtype     string `xml:"Subtype,attr"`
	Creator     string `xml:"Creator,attr"`
	LastModDate string `xml:"LastModDate,attr"`
	Appearance  Appearance
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

// parseAnnotations 解析注释文件
// 入参: doc 文档结构
// 返回: error 错误信息
func (r *Reader) parseAnnotations(doc *Document) error {
	if doc.Annotations == "" {
		return nil
	}
	annPath := r.ResPath(doc.Annotations)
	f, err := r.openFile(annPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var annotations Annotations
	if err := xml.NewDecoder(f).Decode(&annotations); err != nil {
		return err
	}
	if r.Annots == nil {
		r.Annots = make(map[string][]Annotation)
	}
	for _, page := range annotations.Page {
		annotPath := resolveResourcePath(annPath, "", page.FileLoc)
		af, err := r.openFile(annotPath)
		if err != nil {
			continue
		}
		var pageAnnot PageAnnot
		err = xml.NewDecoder(af).Decode(&pageAnnot)
		_ = af.Close()
		if err != nil {
			continue
		}
		r.Annots[page.PageID] = append(r.Annots[page.PageID], pageAnnot.Annot...)
	}
	return nil
}
