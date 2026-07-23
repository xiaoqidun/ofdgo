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

// graphicObjectTarget 图形对象集合
type graphicObjectTarget struct {
	objects   *[]GraphicObject
	text      *[]TextObject
	path      *[]PathObject
	image     *[]ImageObject
	composite *[]CompositeGraphicUnit
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

// UnmarshalXML 解析图层并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (l *Layer) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*l = Layer{}
	l.ID = attrValue(start, "ID")
	l.DrawParam = attrValue(start, "DrawParam")
	return decodeObjectContainer(d, start, l.decodeObject)
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

// UnmarshalXML 解析注释外观并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *Appearance) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = Appearance{}
	a.Boundary = attrValue(start, "Boundary")
	return decodeObjectContainer(d, start, a.decodeObject)
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

// UnmarshalXML 解析图案单元内容并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *PatternContent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = PatternContent{}
	return decodeObjectContainer(d, start, p.decodeObject)
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
