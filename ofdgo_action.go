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
	"strconv"
)

// Action 动作
type Action struct {
	Event  string  `xml:"Event,attr"`
	Region *Region `xml:"Region"`
	Goto   *Goto   `xml:"Goto"`
	URI    *URI    `xml:"URI"`
	GotoA  *GotoA  `xml:"GotoA"`
	Sound  *Sound  `xml:"Sound"`
	Movie  *Movie  `xml:"Movie"`
}

// Goto 文档内跳转动作
type Goto struct {
	Dest     *Dest         `xml:"Dest"`
	Bookmark *GotoBookmark `xml:"Bookmark"`
}

// Dest 文档内跳转目标
type Dest struct {
	Type   string  `xml:"Type,attr"`
	PageID string  `xml:"PageID,attr"`
	Left   float64 `xml:"Left"`
	Right  float64 `xml:"Right"`
	Top    float64 `xml:"Top"`
	Bottom float64 `xml:"Bottom"`
	Zoom   float64 `xml:"Zoom"`
}

// GotoBookmark 书签跳转目标
type GotoBookmark struct {
	Name string `xml:"Name,attr"`
}

// URI URI动作
type URI struct {
	URI  string `xml:"URI,attr"`
	Base string `xml:"Base,attr"`
}

// GotoA 附件动作
type GotoA struct {
	AttachID  string `xml:"AttachID,attr"`
	NewWindow *bool  `xml:"NewWindow,attr"`
}

// Sound 音频动作
type Sound struct {
	ResourceID  string `xml:"ResourceID,attr"`
	Volume      *int   `xml:"Volume,attr"`
	Repeat      bool   `xml:"Repeat,attr"`
	Synchronous bool   `xml:"Synchronous,attr"`
}

// Movie 视频动作
type Movie struct {
	ResourceID string `xml:"ResourceID,attr"`
	Operator   string `xml:"Operator,attr"`
}

// Region 动作区域
type Region struct {
	Area []RegionArea `xml:"Area"`
}

// RegionArea 动作区域分路径
type RegionArea struct {
	Start   string          `xml:"Start,attr"`
	Command []RegionCommand `xml:"-"`
}

// RegionCommand 动作区域绘制指令
type RegionCommand struct {
	Type           string
	Point1         string
	Point2         string
	Point3         string
	EllipseSize    string
	RotationAngle  string
	LargeArc       string
	SweepDirection string
	EndPoint       string
}

// UnmarshalXML 解析跳转目标
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (dest *Dest) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var value struct {
		Left   *float64 `xml:"Left"`
		Right  *float64 `xml:"Right"`
		Top    *float64 `xml:"Top"`
		Bottom *float64 `xml:"Bottom"`
		Zoom   *float64 `xml:"Zoom"`
	}
	dest.Type = attrValue(start, "Type")
	dest.PageID = attrValue(start, "PageID")
	dest.Left = actionFloatAttr(start, "Left")
	dest.Right = actionFloatAttr(start, "Right")
	dest.Top = actionFloatAttr(start, "Top")
	dest.Bottom = actionFloatAttr(start, "Bottom")
	dest.Zoom = actionFloatAttr(start, "Zoom")
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	if value.Left != nil {
		dest.Left = *value.Left
	}
	if value.Right != nil {
		dest.Right = *value.Right
	}
	if value.Top != nil {
		dest.Top = *value.Top
	}
	if value.Bottom != nil {
		dest.Bottom = *value.Bottom
	}
	if value.Zoom != nil {
		dest.Zoom = *value.Zoom
	}
	return nil
}

// UnmarshalXML 解析视频动作并应用默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (m *Movie) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type movie Movie
	value := movie{Operator: "Play"}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*m = Movie(value)
	return nil
}

// UnmarshalXML 解析动作区域分路径并保留指令顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *RegionArea) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = RegionArea{Start: attrValue(start, "Start")}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch node := tok.(type) {
		case xml.StartElement:
			command := RegionCommand{
				Type:           node.Name.Local,
				Point1:         attrValue(node, "Point1"),
				Point2:         attrValue(node, "Point2"),
				Point3:         attrValue(node, "Point3"),
				EllipseSize:    attrValue(node, "EllipseSize"),
				RotationAngle:  attrValue(node, "RotationAngle"),
				LargeArc:       attrValue(node, "LargeArc"),
				SweepDirection: attrValue(node, "SweepDirection"),
				EndPoint:       attrValue(node, "EndPoint"),
			}
			a.Command = append(a.Command, command)
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if node.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

// actionFloatAttr 获取动作浮点属性
// 入参: start 起始节点, name 属性名
// 返回: float64 属性值
func actionFloatAttr(start xml.StartElement, name string) float64 {
	value, _ := strconv.ParseFloat(attrValue(start, name), 64)
	return value
}
