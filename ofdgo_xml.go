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
	"unicode/utf8"
)

// XMLNamespace 表示XML预定义命名空间
const XMLNamespace = "http://www.w3.org/XML/1998/namespace"

// XMLSchemaInstanceNamespace 表示XMLSchema实例命名空间
const XMLSchemaInstanceNamespace = "http://www.w3.org/2001/XMLSchema-instance"

// XLinkNamespace 表示XML链接命名空间
const XLinkNamespace = "http://www.w3.org/1999/xlink"

// xmlDeclarationNamespace 表示XML命名空间声明标识
const xmlDeclarationNamespace = "http://www.w3.org/2000/xmlns/"

// 旧版与2016版OFD命名空间
const (
	ofdNamespaceLegacy = "http://www.ofdspec.org"
	ofdNamespace2016   = "http://www.ofdspec.org/2016"
)

// ofdXMLNamespace 标识XML命名空间，不代表文档版本或功能支持范围
type ofdXMLNamespace uint8

// 未声明、未知、旧版及2016版OFD命名空间，不限制已知内容的读取
const (
	ofdXMLUnqualified ofdXMLNamespace = iota
	ofdXMLUnknown
	ofdXMLLegacy
	ofdXML2016
)

// ofdXMLChild 判断子节点是否属于可识别的OFD命名空间或父节点所在的未知命名空间
// 入参: parent 父节点名称, child 子节点名称
// 返回: bool 是否属于同一OFD结构
func ofdXMLChild(parent, child xml.Name) bool {
	return classifyOFDNamespace(child.Space) != ofdXMLUnknown || child.Space == parent.Space
}

// ofdXMLStart 保留OFD节点的无命名空间属性，不修改解码器返回的属性切片
// 入参: start 起始节点
// 返回: xml.StartElement 供标准结构解码使用的节点
func ofdXMLStart(start xml.StartElement) xml.StartElement {
	for i, attr := range start.Attr {
		if attr.Name.Space == "" {
			continue
		}
		attrs := make([]xml.Attr, i, len(start.Attr)-1)
		copy(attrs, start.Attr[:i])
		for _, attr := range start.Attr[i+1:] {
			if attr.Name.Space == "" {
				attrs = append(attrs, attr)
			}
		}
		start.Attr = attrs
		break
	}
	return start
}

// decodeOFDChildren 按命名空间解析直接子节点，跳过外部扩展而不提升其内部节点
// 入参: d XML解码器, start 容器起始节点, decode 子节点解码函数
// 返回: error 解析错误
func decodeOFDChildren(d *xml.Decoder, start xml.StartElement, decode func(*xml.Decoder, xml.StartElement) error) error {
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if !ofdXMLChild(start.Name, node.Name) {
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			node = ofdXMLStart(node)
			if err := decode(d, node); err != nil {
				return err
			}
		case xml.EndElement:
			if node.Name == start.Name {
				return nil
			}
		}
	}
}

// decodeOFDCollection 按命名空间追加同类子节点，保留重复资源集合的记录
// 入参: d XML解码器, start 集合起始节点, name 子节点名称, values 接收结果的切片
// 返回: error 解析错误
func decodeOFDCollection[T any](d *xml.Decoder, start xml.StartElement, name string, values *[]T) error {
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == name {
			return d.DecodeElement(values, &node)
		}
		return d.Skip()
	})
}

// classifyOFDNamespace 识别OFD命名空间，未声明与未知版本分别处理
// 入参: namespace XML命名空间
// 返回: ofdXMLNamespace 命名空间类别
func classifyOFDNamespace(namespace string) ofdXMLNamespace {
	switch namespace {
	case "":
		return ofdXMLUnqualified
	case ofdNamespaceLegacy:
		return ofdXMLLegacy
	case ofdNamespace2016:
		return ofdXML2016
	default:
		return ofdXMLUnknown
	}
}

// ofdXMLIDValid 按XML 1.0的NCName规则校验xs:ID，不用于数值型ST_ID
// 入参: id 标识
// 返回: bool 是否有效
func ofdXMLIDValid(id string) bool {
	if id == "" || !utf8.ValidString(id) {
		return false
	}
	for i, c := range id {
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' ||
			c >= 0xC0 && c <= 0xD6 || c >= 0xD8 && c <= 0xF6 ||
			c >= 0xF8 && c <= 0x2FF || c >= 0x370 && c <= 0x37D ||
			c >= 0x37F && c <= 0x1FFF || c >= 0x200C && c <= 0x200D ||
			c >= 0x2070 && c <= 0x218F || c >= 0x2C00 && c <= 0x2FEF ||
			c >= 0x3001 && c <= 0xD7FF || c >= 0xF900 && c <= 0xFDCF ||
			c >= 0xFDF0 && c <= 0xFFFD || c >= 0x10000 && c <= 0xEFFFF {
			continue
		}
		if i == 0 || !(c == '-' || c == '.' || c >= '0' && c <= '9' ||
			c == 0xB7 || c >= 0x300 && c <= 0x36F || c >= 0x203F && c <= 0x2040) {
			return false
		}
	}
	return true
}
