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

// PageContent 页面内容
type PageContent struct {
	XMLName  xml.Name   `xml:"Page"`
	ID       string     `xml:"-"`
	Area     PageArea   `xml:"Area"`
	Template []Template `xml:"Template"`
	Content  Content    `xml:"Content"`
}

// Template 页面模板引用
type Template struct {
	TemplateID string `xml:"TemplateID,attr"`
	ZOrder     string `xml:"ZOrder,attr"`
}

// Content 页面内容节点
type Content struct {
	Layer []Layer `xml:"Layer"`
}

// Layer 图层
type Layer struct {
	ID                   string                 `xml:"ID,attr"`
	DrawParam            string                 `xml:"DrawParam,attr"`
	TextObject           []TextObject           `xml:"TextObject"`
	PathObject           []PathObject           `xml:"PathObject"`
	ImageObject          []ImageObject          `xml:"ImageObject"`
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
}

// Clips 裁剪区域集合
type Clips struct {
	Clip []Clip `xml:"Clip"`
}

// Clip 裁剪
type Clip struct {
	Area []ClipArea `xml:"Area"`
}

// ClipArea 裁剪区域
type ClipArea struct {
	Path []PathObject `xml:"Path"`
	Text []TextObject `xml:"Text"`
}

// TextObject 文本对象
type TextObject struct {
	ID          string       `xml:"ID,attr"`
	Boundary    string       `xml:"Boundary,attr"`
	DrawParam   string       `xml:"DrawParam,attr"`
	LineWidth   float64      `xml:"LineWidth,attr"`
	Font        string       `xml:"Font,attr"`
	Size        float64      `xml:"Size,attr"`
	Weight      int          `xml:"Weight,attr"`
	Italic      bool         `xml:"Italic,attr"`
	Decoration  string       `xml:"Decoration,attr"`
	HScale      float64      `xml:"HScale,attr"`
	VScale      float64      `xml:"VScale,attr"`
	CTM         string       `xml:"CTM,attr"`
	Fill        *bool        `xml:"Fill,attr"`
	Stroke      *bool        `xml:"Stroke,attr"`
	StrokeColor *StrokeColor `xml:"StrokeColor"`
	FillColor   *FillColor   `xml:"FillColor"`
	TextCode    []TextCode   `xml:"TextCode"`
	Clips       *Clips       `xml:"Clips"`
}

// FillColor 填充颜色
type FillColor struct {
	Value string `xml:"Value,attr"`
	Alpha *int   `xml:"Alpha,attr"`
}

// TextCode 文本内容节点
type TextCode struct {
	X      string `xml:"X,attr"`
	Y      string `xml:"Y,attr"`
	DeltaX string `xml:"DeltaX,attr"`
	DeltaY string `xml:"DeltaY,attr"`
	Index  string `xml:"Index,attr"`
	Value  string `xml:",chardata"`
}

// PathObject 路径对象
type PathObject struct {
	ID              string       `xml:"ID,attr"`
	Boundary        string       `xml:"Boundary,attr"`
	DrawParam       string       `xml:"DrawParam,attr"`
	LineWidth       float64      `xml:"LineWidth,attr"`
	Join            string       `xml:"Join,attr"`
	Cap             string       `xml:"Cap,attr"`
	DashOffset      float64      `xml:"DashOffset,attr"`
	DashPattern     string       `xml:"DashPattern,attr"`
	MiterLimit      float64      `xml:"MiterLimit,attr"`
	CTM             string       `xml:"CTM,attr"`
	Stroke          *bool        `xml:"Stroke,attr"`
	Fill            *bool        `xml:"Fill,attr"`
	StrokeColor     *StrokeColor `xml:"StrokeColor"`
	FillColor       *FillColor   `xml:"FillColor"`
	AbbreviatedData string       `xml:"AbbreviatedData"`
	Clips           *Clips       `xml:"Clips"`
}

// StrokeColor 勾边颜色
type StrokeColor struct {
	Value string `xml:"Value,attr"`
	Alpha *int   `xml:"Alpha,attr"`
}

// ImageObject 图片对象
type ImageObject struct {
	ID         string `xml:"ID,attr"`
	Boundary   string `xml:"Boundary,attr"`
	ResourceID string `xml:"ResourceID,attr"`
	CTM        string `xml:"CTM,attr"`
	Clips      *Clips `xml:"Clips"`
}
