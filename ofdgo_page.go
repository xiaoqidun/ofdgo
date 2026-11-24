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
	"encoding/xml"
	"fmt"
)

// PageContent 页面内容
type PageContent struct {
	XMLName       xml.Name     `xml:"Page"`
	ID            string       `xml:"-"`
	Area          PageArea     `xml:"Area"`
	Template      []Template   `xml:"Template"`
	PageRes       []string     `xml:"PageRes"`
	Content       Content      `xml:"Content"`
	Actions       []Action     `xml:"Actions>Action"`
	pageResources []*parsedRes `xml:"-"`
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
// Objects非空时按其顺序渲染，图元修改应作用于Objects中的对象
type Layer struct {
	ID              string            `xml:"ID,attr"`
	Type            string            `xml:"Type,attr"`
	DrawParam       string            `xml:"DrawParam,attr"`
	Objects         []GraphicObject   `xml:"-"`
	TextObject      []TextObject      `xml:"TextObject"`
	PathObject      []PathObject      `xml:"PathObject"`
	ImageObject     []ImageObject     `xml:"ImageObject"`
	CompositeObject []CompositeObject `xml:"CompositeObject"`
	PageBlock       []PageBlock       `xml:"PageBlock"`
}

// PageBlock 页块对象
type PageBlock struct {
	ID              string            `xml:"ID,attr"`
	Objects         []GraphicObject   `xml:"-"`
	TextObject      []TextObject      `xml:"TextObject"`
	PathObject      []PathObject      `xml:"PathObject"`
	ImageObject     []ImageObject     `xml:"ImageObject"`
	CompositeObject []CompositeObject `xml:"CompositeObject"`
	PageBlock       []PageBlock       `xml:"PageBlock"`
}

// GraphicObject 图形对象
type GraphicObject struct {
	Type            string
	TextObject      TextObject
	PathObject      PathObject
	ImageObject     ImageObject
	CompositeObject CompositeObject
	origin          *editorObjectOrigin
	state           editorCompositeState
	richText        *richTextSnapshot
	pageBlocks      []string
}

// CompositeObject 复合图元引用对象
// MiterLimitSet为true时，零截断值覆盖继承值，否则零值表示未指定
type CompositeObject struct {
	ID             string   `xml:"ID,attr"`
	Name           string   `xml:"Name,attr"`
	Boundary       string   `xml:"Boundary,attr"`
	CTM            string   `xml:"CTM,attr"`
	ResourceID     string   `xml:"ResourceID,attr"`
	DrawParam      string   `xml:"DrawParam,attr"`
	LineWidth      float64  `xml:"LineWidth,attr"`
	Cap            string   `xml:"Cap,attr"`
	Join           string   `xml:"Join,attr"`
	MiterLimit     float64  `xml:"MiterLimit,attr"`
	DashOffset     *float64 `xml:"DashOffset,attr"`
	DashPattern    string   `xml:"DashPattern,attr"`
	Alpha          *int     `xml:"Alpha,attr"`
	Visible        *bool    `xml:"Visible,attr"`
	Clips          *Clips   `xml:"Clips"`
	Actions        []Action `xml:"Actions>Action"`
	LineWidthSet   bool     `xml:"-"`
	MiterLimitSet  bool     `xml:"-"`
	DashPatternSet bool     `xml:"-"`
	states         map[string]editorCompositeState
}

// Clips 相对于对象外接矩形的裁剪集合
// TransFlag仅兼容旧式扩展，写入时折算为Area的CTM
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
	DrawParam string       `xml:"DrawParam,attr"`
	CTM       string       `xml:"CTM,attr"`
	Path      []PathObject `xml:"Path"`
	Text      []TextObject `xml:"Text"`
}

// TextObject 文本对象
// MiterLimitSet为true时，零截断值覆盖继承值，否则零值表示未指定
type TextObject struct {
	ID             string        `xml:"ID,attr"`
	Name           string        `xml:"Name,attr"`
	Boundary       string        `xml:"Boundary,attr"`
	DrawParam      string        `xml:"DrawParam,attr"`
	LineWidth      float64       `xml:"LineWidth,attr"`
	Cap            string        `xml:"Cap,attr"`
	DashPattern    string        `xml:"DashPattern,attr"`
	DashOffset     *float64      `xml:"DashOffset,attr"`
	Join           string        `xml:"Join,attr"`
	MiterLimit     float64       `xml:"MiterLimit,attr"`
	Font           string        `xml:"Font,attr"`
	Size           float64       `xml:"Size,attr"`
	Weight         int           `xml:"Weight,attr"`
	Italic         bool          `xml:"Italic,attr"`
	HScale         float64       `xml:"HScale,attr"`
	ReadDirection  int           `xml:"ReadDirection,attr"`
	CharDirection  int           `xml:"CharDirection,attr"`
	CTM            string        `xml:"CTM,attr"`
	Alpha          *int          `xml:"Alpha,attr"`
	Visible        *bool         `xml:"Visible,attr"`
	Fill           *bool         `xml:"Fill,attr"`
	Stroke         *bool         `xml:"Stroke,attr"`
	StrokeColor    *StrokeColor  `xml:"StrokeColor"`
	FillColor      *FillColor    `xml:"FillColor"`
	CGTransform    []CGTransform `xml:"CGTransform"`
	TextCode       []TextCode    `xml:"TextCode"`
	Clips          *Clips        `xml:"Clips"`
	Actions        []Action      `xml:"Actions>Action"`
	LineWidthSet   bool          `xml:"-"`
	MiterLimitSet  bool          `xml:"-"`
	WeightSet      bool          `xml:"-"`
	ItalicSet      bool          `xml:"-"`
	DashPatternSet bool          `xml:"-"`
	layout         *textLayout
}

// FillColor 填充颜色
type FillColor struct {
	Value        string        `xml:"Value,attr"`
	Index        *int          `xml:"Index,attr"`
	ColorSpace   string        `xml:"ColorSpace,attr"`
	Alpha        *int          `xml:"Alpha,attr"`
	Pattern      *Pattern      `xml:"Pattern"`
	AxialShd     *AxialShd     `xml:"AxialShd"`
	RadialShd    *RadialShd    `xml:"RadialShd"`
	GouraudShd   *GouraudShd   `xml:"GouraudShd"`
	LaGouraudShd *LaGouraudShd `xml:"LaGouraudShd"`
	unsupported  bool
}

// Pattern 图案填充
type Pattern struct {
	Width         float64        `xml:"Width,attr"`
	Height        float64        `xml:"Height,attr"`
	XStep         float64        `xml:"XStep,attr"`
	YStep         float64        `xml:"YStep,attr"`
	ReflectMethod string         `xml:"ReflectMethod,attr"`
	RelativeTo    string         `xml:"RelativeTo,attr"`
	CTM           string         `xml:"CTM,attr"`
	CellContent   PatternContent `xml:"CellContent"`
}

// PatternContent 图案单元内容
// Objects非空时保留绘制顺序，否则依次使用文字、路径、图片、复合图元和页块数组
type PatternContent struct {
	Thumbnail       string            `xml:"Thumbnail,attr"`
	Objects         []GraphicObject   `xml:"-"`
	TextObject      []TextObject      `xml:"TextObject"`
	PathObject      []PathObject      `xml:"PathObject"`
	ImageObject     []ImageObject     `xml:"ImageObject"`
	CompositeObject []CompositeObject `xml:"CompositeObject"`
	PageBlock       []PageBlock       `xml:"PageBlock"`
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
// CodePosition在内存中为整个TextObject的字符索引，保存时转换为所在TextCode的局部索引
type CGTransform struct {
	CodePosition int    `xml:"CodePosition,attr"`
	CodeCount    int    `xml:"CodeCount,attr"`
	GlyphCount   int    `xml:"GlyphCount,attr"`
	Glyphs       string `xml:"Glyphs"`
}

// PathObject 路径对象
// MiterLimitSet为true时，零截断值覆盖继承值，否则零值表示未指定
type PathObject struct {
	ID              string       `xml:"ID,attr"`
	Name            string       `xml:"Name,attr"`
	Boundary        string       `xml:"Boundary,attr"`
	DrawParam       string       `xml:"DrawParam,attr"`
	LineWidth       float64      `xml:"LineWidth,attr"`
	Join            string       `xml:"Join,attr"`
	Cap             string       `xml:"Cap,attr"`
	DashOffset      *float64     `xml:"DashOffset,attr"`
	DashPattern     string       `xml:"DashPattern,attr"`
	MiterLimit      float64      `xml:"MiterLimit,attr"`
	CTM             string       `xml:"CTM,attr"`
	Alpha           *int         `xml:"Alpha,attr"`
	Visible         *bool        `xml:"Visible,attr"`
	Stroke          *bool        `xml:"Stroke,attr"`
	Fill            *bool        `xml:"Fill,attr"`
	Rule            string       `xml:"Rule,attr"`
	StrokeColor     *StrokeColor `xml:"StrokeColor"`
	FillColor       *FillColor   `xml:"FillColor"`
	AbbreviatedData string       `xml:"AbbreviatedData"`
	Clips           *Clips       `xml:"Clips"`
	Actions         []Action     `xml:"Actions>Action"`
	LineWidthSet    bool         `xml:"-"`
	MiterLimitSet   bool         `xml:"-"`
	DashPatternSet  bool         `xml:"-"`
}

// StrokeColor 勾边颜色
type StrokeColor FillColor

// AxialShd 轴向渐变
type AxialShd struct {
	MapType    string       `xml:"MapType,attr"`
	MapUnit    float64      `xml:"MapUnit,attr"`
	Extend     string       `xml:"Extend,attr"`
	StartPoint string       `xml:"StartPoint,attr"`
	EndPoint   string       `xml:"EndPoint,attr"`
	Segment    []ShdSegment `xml:"Segment"`
}

// RadialShd 径向渐变
type RadialShd struct {
	MapType      string       `xml:"MapType,attr"`
	MapUnit      float64      `xml:"MapUnit,attr"`
	Eccentricity float64      `xml:"Eccentricity,attr"`
	Angle        float64      `xml:"Angle,attr"`
	Extend       string       `xml:"Extend,attr"`
	StartPoint   string       `xml:"StartPoint,attr"`
	StartRadius  float64      `xml:"StartRadius,attr"`
	EndPoint     string       `xml:"EndPoint,attr"`
	EndRadius    float64      `xml:"EndRadius,attr"`
	Segment      []ShdSegment `xml:"Segment"`
}

// ShdSegment 渐变分段
type ShdSegment struct {
	Position        float64  `xml:"Position,attr"`
	Color           ShdColor `xml:"Color"`
	positionMissing bool
}

// ShdColor 渐变颜色
type ShdColor struct {
	Value      string `xml:"Value,attr"`
	Index      *int   `xml:"Index,attr"`
	ColorSpace string `xml:"ColorSpace,attr"`
	Alpha      *int   `xml:"Alpha,attr"`
}

// ImageObject 图片对象
type ImageObject struct {
	ID             string       `xml:"ID,attr"`
	Name           string       `xml:"Name,attr"`
	Boundary       string       `xml:"Boundary,attr"`
	DrawParam      string       `xml:"DrawParam,attr"`
	LineWidth      float64      `xml:"LineWidth,attr"`
	Cap            string       `xml:"Cap,attr"`
	Join           string       `xml:"Join,attr"`
	MiterLimit     float64      `xml:"MiterLimit,attr"`
	DashOffset     *float64     `xml:"DashOffset,attr"`
	DashPattern    string       `xml:"DashPattern,attr"`
	ResourceID     string       `xml:"ResourceID,attr"`
	Substitution   string       `xml:"Substitution,attr"`
	ImageMask      string       `xml:"ImageMask,attr"`
	CTM            string       `xml:"CTM,attr"`
	Alpha          *int         `xml:"Alpha,attr"`
	Visible        *bool        `xml:"Visible,attr"`
	Border         *ImageBorder `xml:"Border"`
	Clips          *Clips       `xml:"Clips"`
	Actions        []Action     `xml:"Actions>Action"`
	LineWidthSet   bool         `xml:"-"`
	MiterLimitSet  bool         `xml:"-"`
	DashPatternSet bool         `xml:"-"`
	interpolate    *bool
}

// ImageBorder 图像边框
type ImageBorder struct {
	LineWidth             *float64     `xml:"LineWidth,attr"`
	HorizonalCornerRadius float64      `xml:"HorizonalCornerRadius,attr"`
	VerticalCornerRadius  float64      `xml:"VerticalCornerRadius,attr"`
	DashOffset            float64      `xml:"DashOffset,attr"`
	DashPattern           string       `xml:"DashPattern,attr"`
	BorderColor           *StrokeColor `xml:"BorderColor"`
}

// UnmarshalXML 读取页面直接子节点，扩展内容不作为页面区域、资源或图层使用
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (page *PageContent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Page" {
		return fmt.Errorf("expected Page root, got %s", start.Name.Local)
	}
	*page = PageContent{XMLName: start.Name}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Area":
			return d.DecodeElement(&page.Area, &node)
		case "Template":
			var template Template
			if err := d.DecodeElement(&template, &node); err != nil {
				return err
			}
			page.Template = append(page.Template, template)
		case "PageRes":
			var location string
			if err := d.DecodeElement(&location, &node); err != nil {
				return err
			}
			page.PageRes = append(page.PageRes, location)
		case "Content":
			return d.DecodeElement(&page.Content, &node)
		case "Actions":
			return decodeOFDActions(d, node, &page.Actions)
		default:
			return d.Skip()
		}
		return nil
	})
}

// UnmarshalXML 读取页面图层，不将外部命名空间中的Layer作为图层
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (content *Content) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*content = Content{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Layer" {
			return d.Skip()
		}
		var layer Layer
		if err := d.DecodeElement(&layer, &node); err != nil {
			return err
		}
		content.Layer = append(content.Layer, layer)
		return nil
	})
}
