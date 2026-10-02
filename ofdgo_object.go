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
	"iter"
	"strconv"
	"strings"
)

// graphicObjectTarget 图形对象集合
type graphicObjectTarget struct {
	objects   *[]GraphicObject
	text      *[]TextObject
	path      *[]PathObject
	image     *[]ImageObject
	composite *[]CompositeGraphicUnit
}

// Actions 返回对象动作的独立副本，不修改原对象
// 返回: []Action 动作列表
func (o GraphicObject) Actions() []Action {
	switch o.Type {
	case "TextObject":
		return cloneEditorData(o.TextObject.Actions)
	case "PathObject", "Path":
		return cloneEditorData(o.PathObject.Actions)
	case "ImageObject":
		return cloneEditorData(o.ImageObject.Actions)
	case "CompositeObject", "CompositeGraphicUnit":
		return cloneEditorData(o.CompositeGraphicUnit.Actions)
	}
	return nil
}

// UnmarshalXML 解析路径并区分省略样式、显式实线与零线宽
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *PathObject) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain PathObject
	var value struct {
		plain
		DashPattern *string `xml:"DashPattern,attr"`
	}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*p = PathObject(value.plain)
	for _, attr := range start.Attr {
		if attr.Name.Local == "LineWidth" {
			p.LineWidthSet = p.LineWidth == 0
		}
	}
	if value.DashPattern != nil {
		p.DashPattern = *value.DashPattern
		p.dashPatternSet = p.DashPattern == ""
	}
	return nil
}

// UnmarshalXML 解析绘制参数并保留显式实线和零线宽对基础参数的覆盖
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *DrawParam) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain DrawParam
	var value struct {
		plain
		DashPattern *string `xml:"DashPattern,attr"`
	}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*p = DrawParam(value.plain)
	for _, attr := range start.Attr {
		if attr.Name.Local == "LineWidth" {
			p.LineWidthSet = p.LineWidth == 0
		}
	}
	if value.DashPattern != nil {
		p.DashPattern = *value.DashPattern
		p.dashPatternSet = p.DashPattern == ""
	}
	return nil
}

// UnmarshalXML 解析填充颜色并区分未支持的复杂颜色
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *FillColor) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain FillColor
	var value struct {
		plain
		LegacyMesh *LaGouraudShd `xml:"LaGourandShd"`
		Other      *struct{}     `xml:",any"`
	}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*c = FillColor(value.plain)
	if c.LaGouraudShd == nil {
		c.LaGouraudShd = value.LegacyMesh
	}
	c.unsupported = value.Other != nil
	return nil
}

// UnmarshalXML 解析勾边颜色并区分未支持的复杂颜色
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *StrokeColor) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return (*FillColor)(c).UnmarshalXML(d, start)
}

// UnmarshalXML 解析图层并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (l *Layer) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*l = Layer{}
	l.ID = attrValue(start, "ID")
	l.Type = attrValue(start, "Type")
	l.DrawParam = attrValue(start, "DrawParam")
	return decodeObjectContainer(d, start, l.decodeObject)
}

// UnmarshalXML 解析复合图元并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *CompositeGraphicUnit) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*c = CompositeGraphicUnit{}
	c.ID = attrValue(start, "ID")
	c.BaseLoc = attrValue(start, "BaseLoc")
	c.ResourceID = attrValue(start, "ResourceID")
	c.Boundary = attrValue(start, "Boundary")
	c.CTM = attrValue(start, "CTM")
	c.DrawParam = attrValue(start, "DrawParam")
	for _, dimension := range []struct {
		name  string
		value *float64
	}{{"Width", &c.Width}, {"Height", &c.Height}} {
		if value := attrValue(start, dimension.name); value != "" {
			c.extentSet = true
			var err error
			*dimension.value, err = strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				return fmt.Errorf("invalid vector %s: %w", dimension.name, err)
			}
		}
	}
	if value := attrValue(start, "Alpha"); value != "" {
		if alpha, err := strconv.Atoi(value); err == nil {
			c.Alpha = &alpha
		}
	}
	if value := attrValue(start, "Visible"); value != "" {
		if visible, err := strconv.ParseBool(value); err == nil {
			c.Visible = &visible
		}
	}
	return decodeObjectContainer(d, start, c.decodeObject)
}

// UnmarshalXML 解析注释外观并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *Appearance) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = Appearance{}
	a.Boundary = attrValue(start, "Boundary")
	return decodeObjectContainer(d, start, a.decodeObject)
}

// UnmarshalXML 解析图案单元内容并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *PatternContent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = PatternContent{Thumbnail: attrValue(start, "Thumbnail")}
	return decodeObjectContainer(d, start, p.decodeObject)
}

// objects 按绘制顺序访问底纹单元，Objects优先于分类数组
// 返回: iter.Seq[GraphicObject] 图形对象序列
func (p PatternContent) objects() iter.Seq[GraphicObject] {
	return func(yield func(GraphicObject) bool) {
		if len(p.Objects) != 0 {
			for _, object := range p.Objects {
				if !yield(object) {
					return
				}
			}
			return
		}
		for _, object := range p.TextObject {
			if !yield(GraphicObject{Type: "TextObject", TextObject: object}) {
				return
			}
		}
		for _, object := range p.PathObject {
			if !yield(GraphicObject{Type: "PathObject", PathObject: object}) {
				return
			}
		}
		for _, object := range p.ImageObject {
			if !yield(GraphicObject{Type: "ImageObject", ImageObject: object}) {
				return
			}
		}
		for _, object := range p.CompositeGraphicUnit {
			if !yield(GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: object}) {
				return
			}
		}
	}
}

// objects 按绘制顺序访问矢量内容，Objects优先于分类数组
// 返回: iter.Seq[GraphicObject] 图形对象序列
func (c CompositeGraphicUnit) objects() iter.Seq[GraphicObject] {
	return (PatternContent{Objects: c.Objects, TextObject: c.TextObject, PathObject: c.PathObject, ImageObject: c.ImageObject, CompositeGraphicUnit: c.CompositeGraphicUnit}).objects()
}

// empty 检查底纹单元是否没有绘制内容，缩略图不参与绘制
// 返回: bool 是否为空
func (p PatternContent) empty() bool {
	return len(p.Objects) == 0 && len(p.TextObject) == 0 && len(p.PathObject) == 0 && len(p.ImageObject) == 0 && len(p.CompositeGraphicUnit) == 0
}

// UnmarshalXML 解析渐变分段并保留位置的缺省状态
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (s *ShdSegment) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type segment ShdSegment
	*s = ShdSegment{positionMissing: attrValue(start, "Position") == ""}
	return d.DecodeElement((*segment)(s), &start)
}

// decodeGraphicObject 解析图形对象
// 入参: d XML解码器, start 起始节点, target 图形对象集合
// 返回: bool 是否为图形对象, error 错误信息
func decodeGraphicObject(d *xml.Decoder, start xml.StartElement, target graphicObjectTarget) (bool, error) {
	switch start.Name.Local {
	case "TextObject":
		var obj TextObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.text = append(*target.text, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: start.Name.Local, TextObject: obj})
	case "PathObject":
		var obj PathObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.path = append(*target.path, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: start.Name.Local, PathObject: obj})
	case "ImageObject":
		var obj ImageObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.image = append(*target.image, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: start.Name.Local, ImageObject: obj})
	case "CompositeGraphicUnit", "CompositeObject":
		var obj CompositeGraphicUnit
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.composite = append(*target.composite, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: start.Name.Local, CompositeGraphicUnit: obj})
	default:
		return false, nil
	}
	return true, nil
}

// decodeObjectContainer 解析图形对象容器
// 入参: d XML解码器, start 起始节点, decode 对象解码函数
// 返回: error 错误信息
func decodeObjectContainer(d *xml.Decoder, start xml.StartElement, decode func(*xml.Decoder, xml.StartElement) error) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch node := tok.(type) {
		case xml.StartElement:
			if err := decode(d, node); err != nil {
				return err
			}
		case xml.EndElement:
			if node.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

// decodeObject 解析图层子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (l *Layer) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &l.Objects,
		text:      &l.TextObject,
		path:      &l.PathObject,
		image:     &l.ImageObject,
		composite: &l.CompositeGraphicUnit,
	}
	if decoded, err := decodeGraphicObject(d, start, target); decoded || err != nil {
		return err
	}
	if start.Name.Local == "PageBlock" {
		return decodeObjectContainer(d, start, l.decodeObject)
	}
	return d.Skip()
}

// decodeObject 解析复合图元子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *CompositeGraphicUnit) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &c.Objects,
		text:      &c.TextObject,
		path:      &c.PathObject,
		image:     &c.ImageObject,
		composite: &c.CompositeGraphicUnit,
	}
	if decoded, err := decodeGraphicObject(d, start, target); decoded || err != nil {
		return err
	}
	switch start.Name.Local {
	case "Thumbnail", "Substitution":
		var reference string
		if err := d.DecodeElement(&reference, &start); err != nil {
			return err
		}
		if start.Name.Local == "Thumbnail" {
			c.Thumbnail = strings.TrimSpace(reference)
		} else {
			c.Substitution = strings.TrimSpace(reference)
		}
	case "Clips":
		var clips Clips
		if err := d.DecodeElement(&clips, &start); err != nil {
			return err
		}
		c.Clips = &clips
	case "Actions":
		var actions struct {
			Action []Action `xml:"Action"`
		}
		if err := d.DecodeElement(&actions, &start); err != nil {
			return err
		}
		c.Actions = actions.Action
	case "Content", "PageBlock":
		return decodeObjectContainer(d, start, c.decodeObject)
	default:
		return d.Skip()
	}
	return nil
}

// decodeObject 解析注释外观子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *Appearance) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &a.Objects,
		text:      &a.TextObject,
		path:      &a.PathObject,
		image:     &a.ImageObject,
		composite: &a.CompositeGraphicUnit,
	}
	if decoded, err := decodeGraphicObject(d, start, target); decoded || err != nil {
		return err
	}
	if start.Name.Local == "PageBlock" {
		return decodeObjectContainer(d, start, a.decodeObject)
	}
	return d.Skip()
}

// decodeObject 解析图案单元内容子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *PatternContent) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &p.Objects,
		text:      &p.TextObject,
		path:      &p.PathObject,
		image:     &p.ImageObject,
		composite: &p.CompositeGraphicUnit,
	}
	if decoded, err := decodeGraphicObject(d, start, target); decoded || err != nil {
		return err
	}
	if start.Name.Local == "PageBlock" {
		return decodeObjectContainer(d, start, p.decodeObject)
	}
	return d.Skip()
}

// attrValue 获取XML属性值
// 入参: start 起始节点, name 属性名
// 返回: string 属性值
func attrValue(start xml.StartElement, name string) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}
