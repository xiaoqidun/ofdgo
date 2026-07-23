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

// Document 文档结构
type Document struct {
	XMLName     xml.Name    `xml:"Document"`
	CommonData  CommonData  `xml:"CommonData"`
	Pages       Pages       `xml:"Pages"`
	Outlines    Outlines    `xml:"Outlines"`
	Permissions Permissions `xml:"Permissions"`
	Actions     []Action    `xml:"Actions>Action"`
	Bookmarks   Bookmarks   `xml:"Bookmarks"`
	Annotations string      `xml:"Annotations"`
	Signatures  string      `xml:"Signatures"`
	Attachments Attachments `xml:"Attachments"`
	CustomTags  CustomTags  `xml:"CustomTags"`
	Extensions  Extensions  `xml:"Extensions"`
}

// Extensions 扩展集合
type Extensions struct {
	XMLName   xml.Name    `xml:"Extensions"`
	Path      string      `xml:",chardata"`
	Extension []Extension `xml:"Extension"`
}

// Extension 扩展信息
type Extension struct {
	AppName    string          `xml:"AppName,attr"`
	Company    string          `xml:"Company,attr"`
	AppVersion string          `xml:"AppVersion,attr"`
	Date       string          `xml:"Date,attr"`
	RefID      string          `xml:"RefId,attr"`
	Property   []Property      `xml:"Property"`
	ExtendData []string        `xml:"ExtendData"`
	Data       []ExtensionData `xml:"Data"`
}

// Property 扩展属性
type Property struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
	Type  string `xml:"Type,attr"`
}

// ExtensionData 扩展数据
type ExtensionData struct {
	Attr    []xml.Attr `xml:",any,attr"`
	Content string     `xml:",innerxml"`
}

// Attachments 附件集合
type Attachments struct {
	XMLName    xml.Name     `xml:"Attachments"`
	Path       string       `xml:",chardata"`
	Attachment []Attachment `xml:"Attachment"`
}

// Attachment 附件信息
type Attachment struct {
	ID           string   `xml:"ID,attr"`
	Name         string   `xml:"Name,attr"`
	Format       string   `xml:"Format,attr"`
	CreationDate string   `xml:"CreationDate,attr"`
	ModDate      string   `xml:"ModDate,attr"`
	Size         *float64 `xml:"Size,attr"`
	Visible      bool     `xml:"Visible,attr"`
	Usage        string   `xml:"Usage,attr"`
	FileLoc      string   `xml:"FileLoc"`
}

// CustomTags 自定义标引集合
type CustomTags struct {
	XMLName   xml.Name    `xml:"CustomTags"`
	Path      string      `xml:",chardata"`
	CustomTag []CustomTag `xml:"CustomTag"`
}

// CustomTag 自定义标引
type CustomTag struct {
	TypeID    string `xml:"TypeID,attr"`
	NameSpace string `xml:"NameSpace,attr"`
	SchemaLoc string `xml:"SchemaLoc"`
	FileLoc   string `xml:"FileLoc"`
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

// Bookmarks 书签集合
type Bookmarks struct {
	Bookmark []Bookmark `xml:"Bookmark"`
}

// Bookmark 书签
type Bookmark struct {
	Name string `xml:"Name,attr"`
	Dest Dest   `xml:"Dest"`
}

// Outlines 大纲集合
type Outlines struct {
	OutlineElem []OutlineElem `xml:"OutlineElem"`
}

// OutlineElem 大纲节点
type OutlineElem struct {
	Title       string        `xml:"Title,attr"`
	Count       int           `xml:"Count,attr"`
	Expanded    bool          `xml:"Expanded,attr"`
	Actions     []Action      `xml:"Actions>Action"`
	OutlineElem []OutlineElem `xml:"OutlineElem"`
}

// Permissions 权限声明
type Permissions struct {
	Edit          bool         `xml:"Edit"`
	Annot         bool         `xml:"Annot"`
	Export        bool         `xml:"Export"`
	Signature     bool         `xml:"Signature"`
	Watermark     bool         `xml:"Watermark"`
	PrintScreen   bool         `xml:"PrintScreen"`
	Print         bool         `xml:"-"`
	Copies        int          `xml:"-"`
	Copy          bool         `xml:"CopyText"`
	ContentRegist bool         `xml:"ContentRegist"`
	ValidPeriod   *ValidPeriod `xml:"ValidPeriod"`
}

// ValidPeriod 文档访问有效期
type ValidPeriod struct {
	StartDate string `xml:"StartDate,attr"`
	EndDate   string `xml:"EndDate,attr"`
}

// UnmarshalXML 解析文档并应用权限默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (doc *Document) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type document Document
	value := document{Permissions: defaultPermissions()}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*doc = Document(value)
	return nil
}

// UnmarshalXML 解析附件并应用默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *Attachment) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type attachment Attachment
	value := attachment{Visible: true, Usage: "none"}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*a = Attachment(value)
	return nil
}

// UnmarshalXML 解析大纲节点并应用默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (o *OutlineElem) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type outlineElem OutlineElem
	value := outlineElem{Expanded: true}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*o = OutlineElem(value)
	return nil
}

// UnmarshalXML 解析文档权限并应用默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *Permissions) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var value struct {
		Edit          *bool        `xml:"Edit"`
		Annot         *bool        `xml:"Annot"`
		Export        *bool        `xml:"Export"`
		Signature     *bool        `xml:"Signature"`
		Watermark     *bool        `xml:"Watermark"`
		PrintScreen   *bool        `xml:"PrintScreen"`
		Copy          *bool        `xml:"CopyText"`
		ContentRegist *bool        `xml:"ContentRegist"`
		Print         *print       `xml:"Print"`
		ValidPeriod   *ValidPeriod `xml:"ValidPeriod"`
	}
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*p = defaultPermissions()
	if value.Edit != nil {
		p.Edit = *value.Edit
	}
	if value.Annot != nil {
		p.Annot = *value.Annot
	}
	if value.Export != nil {
		p.Export = *value.Export
	}
	if value.Signature != nil {
		p.Signature = *value.Signature
	}
	if value.Watermark != nil {
		p.Watermark = *value.Watermark
	}
	if value.PrintScreen != nil {
		p.PrintScreen = *value.PrintScreen
	}
	if value.Copy != nil {
		p.Copy = *value.Copy
	}
	if value.ContentRegist != nil {
		p.ContentRegist = *value.ContentRegist
	}
	if value.Print != nil {
		if value.Print.Printable != nil {
			p.Print = *value.Print.Printable
		}
		if value.Print.Copies != nil {
			p.Copies = *value.Print.Copies
		}
	}
	p.ValidPeriod = value.ValidPeriod
	return nil
}

// print 打印权限节点
type print struct {
	Printable *bool `xml:"Printable,attr"`
	Copies    *int  `xml:"Copies,attr"`
}

// defaultPermissions 获取默认文档权限
// 返回: Permissions 文档权限
func defaultPermissions() Permissions {
	return Permissions{
		Edit:          true,
		Annot:         true,
		Export:        true,
		Signature:     true,
		Watermark:     true,
		PrintScreen:   true,
		Print:         true,
		Copies:        -1,
		Copy:          true,
		ContentRegist: true,
	}
}
