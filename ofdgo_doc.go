// Copyright 2025 肖其顿 (XIAO QI DUN)
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

// Document 文档结构
type Document struct {
	XMLName     xml.Name    `xml:"Document"`
	CommonData  CommonData  `xml:"CommonData"`
	Pages       Pages       `xml:"Pages"`
	Outlines    Outlines    `xml:"Outlines"`
	Permissions Permissions `xml:"Permissions"`
	Annotations string      `xml:"Annotations"`
	Signatures  string      `xml:"Signatures"`
	Attachments Attachments `xml:"Attachments"`
	Extensions  Extensions  `xml:"Extensions"`
}

// Extensions 扩展集合
type Extensions struct {
	Extension []Extension `xml:"Extension"`
}

// Extension 扩展信息
type Extension struct {
	AppName    string     `xml:"AppName,attr"`
	Company    string     `xml:"Company,attr"`
	AppVersion string     `xml:"AppVersion,attr"`
	Date       string     `xml:"Date,attr"`
	RefID      string     `xml:"RefId,attr"`
	Property   []Property `xml:"Property"`
	Data       string     `xml:"Data"`
}

// Property 扩展属性
type Property struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
	Type  string `xml:"Type,attr"`
}

// Attachments 附件集合
type Attachments struct {
	Attachment []Attachment `xml:"Attachment"`
}

// Attachment 附件信息
type Attachment struct {
	Name string `xml:"Name,attr"`
	File string `xml:"File,attr"`
	ID   string `xml:"ID,attr"`
}

// CommonData 文档公共数据
type CommonData struct {
	MaxUnitID    int            `xml:"MaxUnitID"`
	PageArea     PageArea       `xml:"PageArea"`
	PublicRes    string         `xml:"PublicRes"`
	DocumentRes  string         `xml:"DocumentRes"`
	TemplatePage []TemplatePage `xml:"TemplatePage"`
	DefaultCS    int            `xml:"DefaultCS"`
}

// PageArea 页面区域定义
type PageArea struct {
	PhysicalBox    string `xml:"PhysicalBox"`
	ApplicationBox string `xml:"ApplicationBox"`
	ContentBox     string `xml:"ContentBox"`
	BleedBox       string `xml:"BleedBox"`
}

// Pages 页面引用集合
type Pages struct {
	Page []Page `xml:"Page"`
}

// Page 页面引用
type Page struct {
	ID      string `xml:"ID,attr"`
	BaseLoc string `xml:"BaseLoc,attr"`
}

// TemplatePage 模板页
type TemplatePage struct {
	ID      string `xml:"ID,attr"`
	Name    string `xml:"Name,attr"`
	BaseLoc string `xml:"BaseLoc,attr"`
	ZOrder  string `xml:"ZOrder,attr"`
}

// Outlines 大纲集合
type Outlines struct {
	OutlineElem []OutlineElem `xml:"OutlineElem"`
}

// OutlineElem 大纲节点
type OutlineElem struct {
	Title   string `xml:"Title,attr"`
	Count   int    `xml:"Count,attr"`
	Actions string `xml:"Actions"`
}

// Permissions 权限声明
type Permissions struct {
	Edit   bool `xml:"Edit"`
	Print  bool `xml:"Print"`
	Export bool `xml:"Export"`
	Copy   bool `xml:"Copy"`
}
