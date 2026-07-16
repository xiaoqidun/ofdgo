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
	Objects              []GraphicObject        `xml:"-"`
	TextObject           []TextObject           `xml:"TextObject"`
	PathObject           []PathObject           `xml:"PathObject"`
	ImageObject          []ImageObject          `xml:"ImageObject"`
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
}

// GraphicObject 图形对象
type GraphicObject struct {
	Type                 string
	TextObject           TextObject
	PathObject           PathObject
	ImageObject          ImageObject
	CompositeGraphicUnit CompositeGraphicUnit
}

// Clips 裁剪区域集合
type Clips struct {
	TransFlag *bool  `xml:"TransFlag,attr"`
	Clip      []Clip `xml:"Clip"`
}

// Clip 裁剪
type Clip struct {
	Area []ClipArea `xml:"Area"`
}

// ClipArea 裁剪区域
type ClipArea struct {
	CTM  string       `xml:"CTM,attr"`
	Path []PathObject `xml:"Path"`
	Text []TextObject `xml:"Text"`
}

// TextObject 文本对象
type TextObject struct {
	ID          string        `xml:"ID,attr"`
	Boundary    string        `xml:"Boundary,attr"`
	DrawParam   string        `xml:"DrawParam,attr"`
	LineWidth   float64       `xml:"LineWidth,attr"`
	Font        string        `xml:"Font,attr"`
	Size        float64       `xml:"Size,attr"`
	Weight      int           `xml:"Weight,attr"`
	Italic      bool          `xml:"Italic,attr"`
	Decoration  string        `xml:"Decoration,attr"`
	HScale      float64       `xml:"HScale,attr"`
	VScale      float64       `xml:"VScale,attr"`
	CTM         string        `xml:"CTM,attr"`
	Alpha       *int          `xml:"Alpha,attr"`
	Visible     *bool         `xml:"Visible,attr"`
	Fill        *bool         `xml:"Fill,attr"`
	Stroke      *bool         `xml:"Stroke,attr"`
	StrokeColor *StrokeColor  `xml:"StrokeColor"`
	FillColor   *FillColor    `xml:"FillColor"`
	CGTransform []CGTransform `xml:"CGTransform"`
	TextCode    []TextCode    `xml:"TextCode"`
	Clips       *Clips        `xml:"Clips"`
}

// FillColor 填充颜色
type FillColor struct {
	Value     string     `xml:"Value,attr"`
	Alpha     *int       `xml:"Alpha,attr"`
	Pattern   *Pattern   `xml:"Pattern"`
	AxialShd  *AxialShd  `xml:"AxialShd"`
	RadialShd *RadialShd `xml:"RadialShd"`
}

// Pattern 图案填充
type Pattern struct {
	Width       float64        `xml:"Width,attr"`
	Height      float64        `xml:"Height,attr"`
	XStep       float64        `xml:"XStep,attr"`
	YStep       float64        `xml:"YStep,attr"`
	CTM         string         `xml:"CTM,attr"`
	CellContent PatternContent `xml:"CellContent"`
}

// PatternContent 图案单元内容
type PatternContent struct {
	Objects              []GraphicObject        `xml:"-"`
	TextObject           []TextObject           `xml:"TextObject"`
	PathObject           []PathObject           `xml:"PathObject"`
	ImageObject          []ImageObject          `xml:"ImageObject"`
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
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

// CGTransform 字符到字形的映射
type CGTransform struct {
	CodePosition int    `xml:"CodePosition,attr"`
	CodeCount    int    `xml:"CodeCount,attr"`
	GlyphCount   int    `xml:"GlyphCount,attr"`
	Glyphs       string `xml:"Glyphs"`
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
	Alpha           *int         `xml:"Alpha,attr"`
	Visible         *bool        `xml:"Visible,attr"`
	Stroke          *bool        `xml:"Stroke,attr"`
	Fill            *bool        `xml:"Fill,attr"`
	StrokeColor     *StrokeColor `xml:"StrokeColor"`
	FillColor       *FillColor   `xml:"FillColor"`
	AbbreviatedData string       `xml:"AbbreviatedData"`
	Clips           *Clips       `xml:"Clips"`
}

// StrokeColor 勾边颜色
type StrokeColor struct {
	Value     string     `xml:"Value,attr"`
	Alpha     *int       `xml:"Alpha,attr"`
	AxialShd  *AxialShd  `xml:"AxialShd"`
	RadialShd *RadialShd `xml:"RadialShd"`
}

// AxialShd 轴向渐变
type AxialShd struct {
	Extend     string       `xml:"Extend,attr"`
	StartPoint string       `xml:"StartPoint,attr"`
	EndPoint   string       `xml:"EndPoint,attr"`
	Segment    []ShdSegment `xml:"Segment"`
}

// RadialShd 径向渐变
type RadialShd struct {
	Extend      string       `xml:"Extend,attr"`
	StartPoint  string       `xml:"StartPoint,attr"`
	StartRadius float64      `xml:"StartRadius,attr"`
	EndPoint    string       `xml:"EndPoint,attr"`
	EndRadius   float64      `xml:"EndRadius,attr"`
	Segment     []ShdSegment `xml:"Segment"`
}

// ShdSegment 渐变分段
type ShdSegment struct {
	Position float64  `xml:"Position,attr"`
	Color    ShdColor `xml:"Color"`
}

// ShdColor 渐变颜色
type ShdColor struct {
	Value string `xml:"Value,attr"`
	Alpha *int   `xml:"Alpha,attr"`
}

// ImageObject 图片对象
type ImageObject struct {
	ID         string `xml:"ID,attr"`
	Boundary   string `xml:"Boundary,attr"`
	ResourceID string `xml:"ResourceID,attr"`
	ImageMask  string `xml:"ImageMask,attr"`
	CTM        string `xml:"CTM,attr"`
	Alpha      *int   `xml:"Alpha,attr"`
	Visible    *bool  `xml:"Visible,attr"`
	Clips      *Clips `xml:"Clips"`
}
