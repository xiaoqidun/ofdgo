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

// UnmarshalXML 解析图层并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (l *Layer) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*l = Layer{}
	l.ID = attrValue(start, "ID")
	l.DrawParam = attrValue(start, "DrawParam")
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch node := tok.(type) {
		case xml.StartElement:
			if err := l.decodeObject(d, node); err != nil {
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
	switch start.Name.Local {
	case "TextObject":
		var obj TextObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		l.TextObject = append(l.TextObject, obj)
		l.Objects = append(l.Objects, GraphicObject{Type: start.Name.Local, TextObject: obj})
	case "PathObject":
		var obj PathObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		l.PathObject = append(l.PathObject, obj)
		l.Objects = append(l.Objects, GraphicObject{Type: start.Name.Local, PathObject: obj})
	case "ImageObject":
		var obj ImageObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		l.ImageObject = append(l.ImageObject, obj)
		l.Objects = append(l.Objects, GraphicObject{Type: start.Name.Local, ImageObject: obj})
	case "CompositeGraphicUnit", "CompositeObject":
		var obj CompositeGraphicUnit
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		l.CompositeGraphicUnit = append(l.CompositeGraphicUnit, obj)
		l.Objects = append(l.Objects, GraphicObject{Type: start.Name.Local, CompositeGraphicUnit: obj})
	default:
		return d.Skip()
	}
	return nil
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
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch node := tok.(type) {
		case xml.StartElement:
			if err := c.decodeObject(d, node); err != nil {
				return err
			}
		case xml.EndElement:
			if node.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

// decodeObject 解析复合图元子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *CompositeGraphicUnit) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	switch start.Name.Local {
	case "TextObject":
		var obj TextObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		c.TextObject = append(c.TextObject, obj)
		c.Objects = append(c.Objects, GraphicObject{Type: start.Name.Local, TextObject: obj})
	case "PathObject":
		var obj PathObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		c.PathObject = append(c.PathObject, obj)
		c.Objects = append(c.Objects, GraphicObject{Type: start.Name.Local, PathObject: obj})
	case "ImageObject":
		var obj ImageObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		c.ImageObject = append(c.ImageObject, obj)
		c.Objects = append(c.Objects, GraphicObject{Type: start.Name.Local, ImageObject: obj})
	case "CompositeGraphicUnit", "CompositeObject":
		var obj CompositeGraphicUnit
		if err := d.DecodeElement(&obj, &start); err != nil {
			return err
		}
		c.CompositeGraphicUnit = append(c.CompositeGraphicUnit, obj)
		c.Objects = append(c.Objects, GraphicObject{Type: start.Name.Local, CompositeGraphicUnit: obj})
	case "Clips":
		var clips Clips
		if err := d.DecodeElement(&clips, &start); err != nil {
			return err
		}
		c.Clips = &clips
	default:
		return d.Skip()
	}
	return nil
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
