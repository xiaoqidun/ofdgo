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
	"io"
	"iter"
	"slices"
	"strconv"
	"strings"
)

// graphicObjectTarget 图形对象集合
type graphicObjectTarget struct {
	objects   *[]GraphicObject
	text      *[]TextObject
	path      *[]PathObject
	image     *[]ImageObject
	composite *[]CompositeObject
	blocks    *[]PageBlock
	stack     []string
}

// graphicAttributeTokens 提供图形节点的起止令牌，仅用于解码无命名空间属性
type graphicAttributeTokens struct {
	start xml.StartElement
	step  int
}

// decodeGraphicAttributes 按结构字段解析图形属性，忽略带命名空间的扩展属性
// 入参: start 起始节点, value 不含自身解码方法的接收结构
// 返回: error 解码错误
func decodeGraphicAttributes(start xml.StartElement, value any) error {
	return xml.NewTokenDecoder(&graphicAttributeTokens{start: ofdXMLStart(start)}).Decode(value)
}

// decodeGraphicObject 解析图形对象
// 入参: d XML解码器, start 起始节点, target 解码目标
// 返回: bool 是否命中图形对象, error 解码错误
func decodeGraphicObject(d *xml.Decoder, start xml.StartElement, target graphicObjectTarget) (bool, error) {
	var blocks []string
	if len(target.stack) != 0 {
		blocks = slices.Clone(target.stack)
	}
	switch start.Name.Local {
	case "TextObject":
		var obj TextObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.text = append(*target.text, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: start.Name.Local, TextObject: obj, pageBlocks: blocks})
	case "PathObject":
		var obj PathObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.path = append(*target.path, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: start.Name.Local, PathObject: obj, pageBlocks: blocks})
	case "ImageObject":
		var obj ImageObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.image = append(*target.image, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: start.Name.Local, ImageObject: obj, pageBlocks: blocks})
	case "CompositeObject":
		var obj CompositeObject
		if err := d.DecodeElement(&obj, &start); err != nil {
			return true, err
		}
		*target.composite = append(*target.composite, obj)
		*target.objects = append(*target.objects, GraphicObject{Type: "CompositeObject", CompositeObject: obj, pageBlocks: blocks})
	default:
		return false, nil
	}
	return true, nil
}

// decodePageBlock 递归解析页块并保留嵌套层级标识与扁平对象序列
// 入参: d XML解码器, start 起始节点, target 解码目标
// 返回: error 解码错误
func decodePageBlock(d *xml.Decoder, start xml.StartElement, target graphicObjectTarget) error {
	block := PageBlock{ID: attrValue(start, "ID")}
	nextStack := append(slices.Clone(target.stack), block.ID)
	nested := target
	nested.blocks = &block.PageBlock
	nested.stack = nextStack
	err := decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		before := len(*nested.objects)
		decoded, err := decodeGraphicObject(d, node, nested)
		if err != nil {
			return err
		}
		if decoded {
			obj := (*nested.objects)[before]
			block.Objects = append(block.Objects, obj)
			switch obj.Type {
			case "TextObject":
				block.TextObject = append(block.TextObject, obj.TextObject)
			case "PathObject":
				block.PathObject = append(block.PathObject, obj.PathObject)
			case "ImageObject":
				block.ImageObject = append(block.ImageObject, obj.ImageObject)
			case "CompositeObject":
				block.CompositeObject = append(block.CompositeObject, obj.CompositeObject)
			}
			return nil
		}
		if node.Name.Local == "PageBlock" {
			beforeChild := len(*nested.objects)
			if err := decodePageBlock(d, node, nested); err != nil {
				return err
			}
			for i := beforeChild; i < len(*nested.objects); i++ {
				obj := (*nested.objects)[i]
				block.Objects = append(block.Objects, obj)
			}
			return nil
		}
		return d.Skip()
	})
	if err != nil {
		return err
	}
	if target.blocks != nil {
		*target.blocks = append(*target.blocks, block)
	}
	return nil
}

// UnmarshalXML 解析页块及其嵌套图元与子页块
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (b *PageBlock) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*b = PageBlock{ID: attrValue(start, "ID")}
	target := graphicObjectTarget{
		objects:   &b.Objects,
		text:      &b.TextObject,
		path:      &b.PathObject,
		image:     &b.ImageObject,
		composite: &b.CompositeObject,
		blocks:    &b.PageBlock,
		stack:     []string{b.ID},
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if decoded, err := decodeGraphicObject(d, node, target); decoded || err != nil {
			return err
		}
		if node.Name.Local == "PageBlock" {
			return decodePageBlock(d, node, target)
		}
		return d.Skip()
	})
}

// attrValue 获取无命名空间的XML属性值
// 入参: start 起始节点, name 属性名
// 返回: string 属性值
func attrValue(start xml.StartElement, name string) string {
	for _, attr := range start.Attr {
		if attr.Name.Space == "" && attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
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
	case "CompositeObject":
		return cloneEditorData(o.CompositeObject.Actions)
	}
	return nil
}

// UnmarshalXML 兼容旧命名空间的对象变换裁剪，显式TransFlag优先
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (clips *Clips) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*clips = Clips{}
	if classifyOFDNamespace(start.Name.Space) == ofdXMLLegacy {
		transform := true
		clips.TransFlag = &transform
	}
	for _, attr := range start.Attr {
		if attr.Name.Space == "" && attr.Name.Local == "TransFlag" {
			transform, err := strconv.ParseBool(strings.TrimSpace(attr.Value))
			if err != nil {
				return err
			}
			clips.TransFlag = &transform
		}
	}
	return decodeOFDCollection(d, start, "Clip", &clips.Clip)
}

// UnmarshalXML 解析裁剪分区，不将外部同名节点作为裁剪区域
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (clip *Clip) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*clip = Clip{}
	return decodeOFDCollection(d, start, "Area", &clip.Area)
}

// UnmarshalXML 解析裁剪区域的变换和路径或文字内容
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (area *ClipArea) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*area = ClipArea{DrawParam: attrValue(start, "DrawParam"), CTM: attrValue(start, "CTM")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Path":
			return d.DecodeElement(&area.Path, &node)
		case "Text":
			return d.DecodeElement(&area.Text, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析路径并区分省略样式、显式实线与零值描边参数
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *PathObject) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain PathObject
	var value plain
	if err := decodeGraphicAttributes(start, &value); err != nil {
		return err
	}
	*p = PathObject(value)
	for _, attr := range ofdXMLStart(start).Attr {
		switch attr.Name.Local {
		case "LineWidth":
			p.LineWidthSet = p.LineWidth == 0
		case "MiterLimit":
			p.MiterLimitSet = p.MiterLimit == 0
		case "DashPattern":
			p.DashPatternSet = p.DashPattern == ""
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "FillColor":
			return d.DecodeElement(&p.FillColor, &node)
		case "StrokeColor":
			return d.DecodeElement(&p.StrokeColor, &node)
		case "AbbreviatedData":
			return d.DecodeElement(&p.AbbreviatedData, &node)
		case "Clips":
			return d.DecodeElement(&p.Clips, &node)
		case "Actions":
			return decodeOFDActions(d, node, &p.Actions)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析图片及其边框、裁剪和动作，区分省略样式与显式零值描边参数
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (image *ImageObject) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain ImageObject
	var value plain
	if err := decodeGraphicAttributes(start, &value); err != nil {
		return err
	}
	*image = ImageObject(value)
	for _, attr := range ofdXMLStart(start).Attr {
		switch attr.Name.Local {
		case "LineWidth":
			image.LineWidthSet = image.LineWidth == 0
		case "MiterLimit":
			image.MiterLimitSet = image.MiterLimit == 0
		case "DashPattern":
			image.DashPatternSet = image.DashPattern == ""
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Border":
			return d.DecodeElement(&image.Border, &node)
		case "Clips":
			return d.DecodeElement(&image.Clips, &node)
		case "Actions":
			return decodeOFDActions(d, node, &image.Actions)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析图片边框，保留省略线宽与显式零值的区别
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (border *ImageBorder) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain ImageBorder
	var value plain
	if err := decodeGraphicAttributes(start, &value); err != nil {
		return err
	}
	*border = ImageBorder(value)
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "BorderColor" {
			return d.DecodeElement(&border.BorderColor, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析绘制参数并保留显式实线和零值参数对基础参数的覆盖
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *DrawParam) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = DrawParam{
		ID: attrValue(start, "ID"), Relative: attrValue(start, "Relative"), Join: attrValue(start, "Join"),
		Cap: attrValue(start, "Cap"), DashPattern: attrValue(start, "DashPattern"),
	}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target *float64
		switch attr.Name.Local {
		case "LineWidth":
			target = &p.LineWidth
		case "MiterLimit":
			target = &p.MiterLimit
		case "DashOffset":
			p.DashOffset = new(float64)
			target = p.DashOffset
		case "DashPattern":
			p.DashPatternSet = p.DashPattern == ""
		}
		if value := strings.TrimSpace(attr.Value); target != nil && value != "" {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			*target = number
		}
		if attr.Name.Local == "LineWidth" {
			p.LineWidthSet = p.LineWidth == 0
		}
		if attr.Name.Local == "MiterLimit" {
			p.MiterLimitSet = p.MiterLimit == 0
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "FillColor":
			return d.DecodeElement(&p.FillColor, &node)
		case "StrokeColor":
			return d.DecodeElement(&p.StrokeColor, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析填充颜色并区分未支持的复杂颜色
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *FillColor) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var base ShdColor
	if err := base.decodeAttributes(start); err != nil {
		return err
	}
	*c = FillColor{Value: base.Value, Index: base.Index, ColorSpace: base.ColorSpace, Alpha: base.Alpha}
	var schemaMesh *LaGouraudShd
	err := decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target any
		switch node.Name.Local {
		case "Pattern":
			target = &c.Pattern
		case "AxialShd":
			target = &c.AxialShd
		case "RadialShd":
			target = &c.RadialShd
		case "GouraudShd":
			target = &c.GouraudShd
		case "LaGouraudShd":
			target = &c.LaGouraudShd
		case "LaGourandShd":
			target = &schemaMesh
		default:
			c.unsupported = true
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
	if c.LaGouraudShd == nil {
		c.LaGouraudShd = schemaMesh
	}
	return err
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
	return decodeOFDChildren(d, start, l.decodeObject)
}

// UnmarshalXML 解析复合图元资源定义并保留内容对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *CompositeGraphicUnit) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*c = CompositeGraphicUnit{ID: attrValue(start, "ID")}
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
	return decodeOFDChildren(d, start, c.decodeObject)
}

// UnmarshalXML 解析复合图元引用对象，属性格式非法时返回错误
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (c *CompositeObject) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*c = CompositeObject{
		ID: attrValue(start, "ID"), Name: attrValue(start, "Name"),
		ResourceID: attrValue(start, "ResourceID"), Boundary: attrValue(start, "Boundary"), CTM: attrValue(start, "CTM"),
		DrawParam: attrValue(start, "DrawParam"), Cap: attrValue(start, "Cap"), Join: attrValue(start, "Join"),
		DashPattern: attrValue(start, "DashPattern"),
	}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		switch attr.Name.Local {
		case "LineWidth":
			if value := strings.TrimSpace(attr.Value); value != "" {
				v, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return err
				}
				c.LineWidth = v
				c.LineWidthSet = v == 0
			}
		case "MiterLimit":
			if value := strings.TrimSpace(attr.Value); value != "" {
				v, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return err
				}
				c.MiterLimit = v
				c.MiterLimitSet = v == 0
			}
		case "DashOffset":
			if value := strings.TrimSpace(attr.Value); value != "" {
				v, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return err
				}
				c.DashOffset = &v
			}
		case "DashPattern":
			c.DashPatternSet = c.DashPattern == ""
		case "Alpha":
			if value := strings.TrimSpace(attr.Value); value != "" {
				alpha, err := strconv.Atoi(value)
				if err != nil {
					return err
				}
				c.Alpha = &alpha
			}
		case "Visible":
			if value := strings.TrimSpace(attr.Value); value != "" {
				visible, err := strconv.ParseBool(value)
				if err != nil {
					return err
				}
				c.Visible = &visible
			}
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Clips":
			var clips Clips
			if err := d.DecodeElement(&clips, &node); err != nil {
				return err
			}
			c.Clips = &clips
		case "Actions":
			return decodeOFDActions(d, node, &c.Actions)
		default:
			return d.Skip()
		}
		return nil
	})
}

// UnmarshalXML 解析注释外观并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *Appearance) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = Appearance{}
	a.Boundary = attrValue(start, "Boundary")
	return decodeOFDChildren(d, start, a.decodeObject)
}

// UnmarshalXML 解析图案单元内容并保留对象顺序
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *PatternContent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = PatternContent{Thumbnail: attrValue(start, "Thumbnail")}
	return decodeOFDChildren(d, start, p.decodeObject)
}

// UnmarshalXML 解析渐变分段并保留位置的缺省状态
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (s *ShdSegment) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*s = ShdSegment{positionMissing: attrValue(start, "Position") == ""}
	if value := strings.TrimSpace(attrValue(start, "Position")); value != "" {
		position, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		s.Position = position
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "Color" {
			return d.DecodeElement(&s.Color, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析图案参数及所属命名空间中的单元内容
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (p *Pattern) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = Pattern{ReflectMethod: attrValue(start, "ReflectMethod"), RelativeTo: attrValue(start, "RelativeTo"), CTM: attrValue(start, "CTM")}
	for _, field := range []struct {
		name   string
		target *float64
	}{
		{"Width", &p.Width}, {"Height", &p.Height}, {"XStep", &p.XStep}, {"YStep", &p.YStep},
	} {
		if value := strings.TrimSpace(attrValue(start, field.name)); value != "" {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			*field.target = number
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "CellContent" {
			return d.DecodeElement(&p.CellContent, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析轴向渐变参数及有序颜色分段
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (s *AxialShd) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*s = AxialShd{MapType: attrValue(start, "MapType"), Extend: attrValue(start, "Extend"), StartPoint: attrValue(start, "StartPoint"), EndPoint: attrValue(start, "EndPoint")}
	if value := strings.TrimSpace(attrValue(start, "MapUnit")); value != "" {
		number, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return err
		}
		s.MapUnit = number
	}
	return decodeOFDCollection(d, start, "Segment", &s.Segment)
}

// UnmarshalXML 解析径向渐变参数及有序颜色分段
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (s *RadialShd) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*s = RadialShd{MapType: attrValue(start, "MapType"), Extend: attrValue(start, "Extend"), StartPoint: attrValue(start, "StartPoint"), EndPoint: attrValue(start, "EndPoint")}
	for _, field := range []struct {
		name   string
		target *float64
	}{
		{"MapUnit", &s.MapUnit}, {"Eccentricity", &s.Eccentricity}, {"Angle", &s.Angle}, {"StartRadius", &s.StartRadius}, {"EndRadius", &s.EndRadius},
	} {
		if value := strings.TrimSpace(attrValue(start, field.name)); value != "" {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			*field.target = number
		}
	}
	return decodeOFDCollection(d, start, "Segment", &s.Segment)
}

// UnmarshalXML 解析渐变使用的基本颜色，不读取扩展子节点
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (c *ShdColor) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if err := c.decodeAttributes(start); err != nil {
		return err
	}
	return d.Skip()
}

// Token 返回起始节点和结束节点，不读取图形子节点
// 返回: xml.Token XML令牌, error 读取错误
func (s *graphicAttributeTokens) Token() (xml.Token, error) {
	s.step++
	switch s.step {
	case 1:
		return s.start, nil
	case 2:
		return s.start.End(), nil
	}
	return nil, io.EOF
}

// usesObjectMatrix 判断旧式扩展是否要求裁剪跟随对象变换，标准裁剪以外接矩形为基准
// 返回: bool 是否叠加对象CTM
func (clips *Clips) usesObjectMatrix() bool {
	return clips != nil && clips.TransFlag != nil && *clips.TransFlag
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
		for _, object := range p.CompositeObject {
			if !yield(GraphicObject{Type: "CompositeObject", CompositeObject: object}) {
				return
			}
		}
		for _, block := range p.PageBlock {
			if !yieldPageBlockObjects(block, nil, yield) {
				return
			}
		}
	}
}

// yieldPageBlockObjects 递归按绘制顺序遍历页块中的图形对象
// 入参: block 页块, prefix 父级页块标识栈, yield 回调函数
// 返回: bool 是否继续遍历
func yieldPageBlockObjects(block PageBlock, prefix []string, yield func(GraphicObject) bool) bool {
	stack := append(slices.Clone(prefix), block.ID)
	if len(block.Objects) != 0 {
		for _, object := range block.Objects {
			if len(object.pageBlocks) == 0 {
				object.pageBlocks = stack
			}
			if !yield(object) {
				return false
			}
		}
		return true
	}
	for _, object := range block.TextObject {
		if !yield(GraphicObject{Type: "TextObject", TextObject: object, pageBlocks: stack}) {
			return false
		}
	}
	for _, object := range block.PathObject {
		if !yield(GraphicObject{Type: "PathObject", PathObject: object, pageBlocks: stack}) {
			return false
		}
	}
	for _, object := range block.ImageObject {
		if !yield(GraphicObject{Type: "ImageObject", ImageObject: object, pageBlocks: stack}) {
			return false
		}
	}
	for _, object := range block.CompositeObject {
		if !yield(GraphicObject{Type: "CompositeObject", CompositeObject: object, pageBlocks: stack}) {
			return false
		}
	}
	for _, child := range block.PageBlock {
		if !yieldPageBlockObjects(child, stack, yield) {
			return false
		}
	}
	return true
}

// objects 按绘制顺序访问矢量内容，Objects优先于分类数组
// 返回: iter.Seq[GraphicObject] 对象序列
func (c CompositeGraphicUnit) objects() iter.Seq[GraphicObject] {
	return (PatternContent{Objects: c.Objects, TextObject: c.TextObject, PathObject: c.PathObject, ImageObject: c.ImageObject, CompositeObject: c.CompositeObject, PageBlock: c.PageBlock}).objects()
}

// empty 检查底纹单元是否没有绘制内容，缩略图不参与绘制
// 返回: bool 是否为空
func (p PatternContent) empty() bool {
	return len(p.Objects) == 0 && len(p.TextObject) == 0 && len(p.PathObject) == 0 && len(p.ImageObject) == 0 && len(p.CompositeObject) == 0 && len(p.PageBlock) == 0
}

// decodeAttributes 解析基本颜色属性，忽略带命名空间的扩展属性
// 入参: start 颜色起始节点
// 返回: error 属性格式错误
func (c *ShdColor) decodeAttributes(start xml.StartElement) error {
	*c = ShdColor{Value: attrValue(start, "Value"), ColorSpace: attrValue(start, "ColorSpace")}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target **int
		switch attr.Name.Local {
		case "Index":
			target = &c.Index
		case "Alpha":
			target = &c.Alpha
		default:
			continue
		}
		*target = new(int)
		if value := strings.TrimSpace(attr.Value); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			**target = number
		}
	}
	return nil
}

// decodeObject 解析图层子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (l *Layer) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &l.Objects,
		text:      &l.TextObject,
		path:      &l.PathObject,
		image:     &l.ImageObject,
		composite: &l.CompositeObject,
		blocks:    &l.PageBlock,
	}
	if decoded, err := decodeGraphicObject(d, start, target); decoded || err != nil {
		return err
	}
	if start.Name.Local == "PageBlock" {
		return decodePageBlock(d, start, target)
	}
	return d.Skip()
}

// decodeObject 解析复合图元资源子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (c *CompositeGraphicUnit) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &c.Objects,
		text:      &c.TextObject,
		path:      &c.PathObject,
		image:     &c.ImageObject,
		composite: &c.CompositeObject,
		blocks:    &c.PageBlock,
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
	case "Content":
		return decodeOFDChildren(d, start, c.decodeObject)
	case "PageBlock":
		return decodePageBlock(d, start, target)
	default:
		return d.Skip()
	}
	return nil
}

// decodeObject 解析注释外观子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (a *Appearance) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &a.Objects,
		text:      &a.TextObject,
		path:      &a.PathObject,
		image:     &a.ImageObject,
		composite: &a.CompositeObject,
		blocks:    &a.PageBlock,
	}
	if decoded, err := decodeGraphicObject(d, start, target); decoded || err != nil {
		return err
	}
	if start.Name.Local == "PageBlock" {
		return decodePageBlock(d, start, target)
	}
	return d.Skip()
}

// decodeObject 解析图案单元内容子对象
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (p *PatternContent) decodeObject(d *xml.Decoder, start xml.StartElement) error {
	target := graphicObjectTarget{
		objects:   &p.Objects,
		text:      &p.TextObject,
		path:      &p.PathObject,
		image:     &p.ImageObject,
		composite: &p.CompositeObject,
		blocks:    &p.PageBlock,
	}
	if decoded, err := decodeGraphicObject(d, start, target); decoded || err != nil {
		return err
	}
	if start.Name.Local == "PageBlock" {
		return decodePageBlock(d, start, target)
	}
	return d.Skip()
}
