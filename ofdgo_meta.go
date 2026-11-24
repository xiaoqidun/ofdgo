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
	"strconv"
	"strings"
)

// ofdCreator 标识本库生成的文档
const ofdCreator = "xiaoqidun/ofdgo"

// OFD OFD入口结构
// 代表OFD文档的根节点
type OFD struct {
	XMLName xml.Name  `xml:"OFD"`
	Version string    `xml:"Version,attr"`
	DocType string    `xml:"DocType,attr"`
	DocBody []DocBody `xml:"DocBody"`
}

// DocBody 文档体信息
// 包含文档元数据、根节点路径、版本列表和签名入口
type DocBody struct {
	DocInfo    DocInfo      `xml:"DocInfo"`
	DocRoot    string       `xml:"DocRoot"`
	Versions   *DocVersions `xml:"Versions,omitempty"`
	Signatures string       `xml:"Signatures"`
	versioned  bool
}

// DocVersions 文档版本列表
type DocVersions struct {
	Version []DocVersion `xml:"Version"`
}

// DocVersion 文档版本入口
type DocVersion struct {
	ID      string `xml:"ID,attr"`
	Index   int32  `xml:"Index,attr"`
	Current bool   `xml:"Current,attr,omitempty"`
	BaseLoc string `xml:"BaseLoc,attr"`
}

// DocInfo 文档元数据
type DocInfo struct {
	DocID          string       `xml:"DocID"`
	Title          string       `xml:"Title"`
	Author         string       `xml:"Author"`
	Subject        string       `xml:"Subject"`
	Abstract       string       `xml:"Abstract"`
	CreationDate   string       `xml:"CreationDate"`
	ModDate        string       `xml:"ModDate"`
	DocUsage       string       `xml:"DocUsage"`
	Cover          string       `xml:"Cover"`
	Keywords       Keywords     `xml:"Keywords,omitempty"`
	Creator        string       `xml:"Creator"`
	CreatorVersion string       `xml:"CreatorVersion"`
	CustomDatas    *CustomDatas `xml:"CustomDatas"`
}

// Keywords 保存有序关键词，保留重复及空文本，空集合不生成XML节点
type Keywords []string

// CustomDatas 自定义数据集合
type CustomDatas struct {
	CustomData []CustomData `xml:"CustomData"`
}

// CustomData 自定义数据项
type CustomData struct {
	Name        string `xml:"Name,attr"`
	Value       string `xml:",chardata"`
	sourceIndex int
}

// UnmarshalXML 读取主入口，区分标准文档条目与扩展中的同名节点
// 入参: decoder XML解码器, start 起始节点
// 返回: error 解析错误
func (ofd *OFD) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "OFD" {
		return fmt.Errorf("expected OFD root, got %s", start.Name.Local)
	}
	*ofd = OFD{XMLName: start.Name, Version: attrValue(start, "Version"), DocType: attrValue(start, "DocType")}
	return decodeOFDChildren(decoder, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "DocBody" {
			return d.Skip()
		}
		var body DocBody
		if err := d.DecodeElement(&body, &node); err != nil {
			return err
		}
		ofd.DocBody = append(ofd.DocBody, body)
		return nil
	})
}

// UnmarshalXML 读取文档入口并解析版本列表
// 入参: decoder XML解码器, start 起始节点
// 返回: error 解析错误
func (body *DocBody) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	*body = DocBody{}
	return decodeOFDChildren(decoder, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "DocInfo":
			return d.DecodeElement(&body.DocInfo, &node)
		case "DocRoot":
			return d.DecodeElement(&body.DocRoot, &node)
		case "Signatures":
			return d.DecodeElement(&body.Signatures, &node)
		case "Versions":
			body.versioned = true
			return d.DecodeElement(&body.Versions, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 按原顺序读取标准文档版本条目，跳过外部扩展节点
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (versions *DocVersions) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*versions = DocVersions{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Version" {
			return d.Skip()
		}
		var item DocVersion
		if err := d.DecodeElement(&item, &node); err != nil {
			return err
		}
		versions.Version = append(versions.Version, item)
		return nil
	})
}

// UnmarshalXML 读取文档版本入口属性，忽略带命名空间的属性和未知子节点
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (version *DocVersion) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*version = DocVersion{
		ID:      attrValue(start, "ID"),
		BaseLoc: attrValue(start, "BaseLoc"),
	}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		value := strings.TrimSpace(attr.Value)
		switch attr.Name.Local {
		case "Index":
			if value != "" {
				parsed, err := strconv.ParseInt(value, 10, 32)
				if err != nil {
					return fmt.Errorf("invalid version Index %q: %w", value, err)
				}
				version.Index = int32(parsed)
			}
		case "Current":
			if value != "" {
				switch value {
				case "true", "1":
					version.Current = true
				case "false", "0":
					version.Current = false
				default:
					return fmt.Errorf("invalid version Current %q", value)
				}
			}
		}
	}
	return d.Skip()
}

// UnmarshalXML 读取文档元数据，不将外部同名字段作为OFD信息
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (info *DocInfo) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*info = DocInfo{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target any
		switch node.Name.Local {
		case "DocID":
			target = &info.DocID
		case "Title":
			target = &info.Title
		case "Author":
			target = &info.Author
		case "Subject":
			target = &info.Subject
		case "Abstract":
			target = &info.Abstract
		case "CreationDate":
			target = &info.CreationDate
		case "ModDate":
			target = &info.ModDate
		case "DocUsage":
			target = &info.DocUsage
		case "Cover":
			target = &info.Cover
		case "Keywords":
			target = &info.Keywords
		case "Creator":
			target = &info.Creator
		case "CreatorVersion":
			target = &info.CreatorVersion
		case "CustomDatas":
			target = &info.CustomDatas
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
}

// UnmarshalXML 按原顺序读取标准关键词，跳过外部扩展条目
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (keywords *Keywords) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*keywords = nil
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Keyword" {
			return d.Skip()
		}
		var value string
		if err := d.DecodeElement(&value, &node); err != nil {
			return err
		}
		*keywords = append(*keywords, value)
		return nil
	})
}

// MarshalXML 编码非空关键词集合，子节点沿用集合的命名空间
// 入参: e XML编码器, start 起始节点
// 返回: error 编码错误
func (keywords Keywords) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if len(keywords) == 0 {
		return nil
	}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	for _, value := range keywords {
		child := xml.StartElement{Name: xml.Name{Space: start.Name.Space, Local: "Keyword"}}
		if err := e.EncodeElement(value, child); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

// UnmarshalXML 按原顺序读取OFD自定义字段，跳过外部扩展条目
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (data *CustomDatas) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*data = CustomDatas{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "CustomData" {
			return d.Skip()
		}
		return d.DecodeElement(&data.CustomData, &node)
	})
}

// UnmarshalXML 读取自定义字段的名称和文本，忽略带命名空间的属性
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (data *CustomData) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type customData CustomData
	var value customData
	start = ofdXMLStart(start)
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*data = CustomData(value)
	return nil
}
