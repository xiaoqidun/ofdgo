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
	"path"
	"strconv"
	"strings"
)

// Document 文档结构
// Signatures由阅读器按主入口和所选版本解析，不属于文档根节点的XML内容
type Document struct {
	XMLName      xml.Name      `xml:"Document"`
	CommonData   CommonData    `xml:"CommonData"`
	Pages        Pages         `xml:"Pages"`
	Outlines     Outlines      `xml:"Outlines"`
	Permissions  Permissions   `xml:"Permissions"`
	Actions      []Action      `xml:"Actions>Action"`
	VPreferences *VPreferences `xml:"VPreferences,omitempty"`
	Bookmarks    Bookmarks     `xml:"Bookmarks"`
	Annotations  string        `xml:"Annotations"`
	Signatures   string        `xml:"-"`
	Attachments  Attachments   `xml:"Attachments"`
	CustomTags   CustomTags    `xml:"CustomTags"`
	Extensions   Extensions    `xml:"Extensions"`
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
	MaxUnitID    uint32         `xml:"MaxUnitID"`
	PageArea     PageArea       `xml:"PageArea"`
	PublicRes    []string       `xml:"PublicRes"`
	DocumentRes  []string       `xml:"DocumentRes"`
	TemplatePage []TemplatePage `xml:"TemplatePage"`
	DefaultCS    uint32         `xml:"DefaultCS"`
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

// OutlineInfo 目录节点信息，Page从1开始，0表示没有有效的文档内目标页
type OutlineInfo struct {
	Title       string        `json:"title"`
	Expanded    bool          `json:"expanded"`
	Page        int           `json:"page,omitempty"`
	ActionCount int           `json:"actionCount,omitempty"`
	Children    []OutlineInfo `json:"children,omitempty"`
}

// VPreferences 文档视图首选项
type VPreferences struct {
	PageMode     string   `xml:"PageMode,omitempty"`
	PageLayout   string   `xml:"PageLayout,omitempty"`
	TabDisplay   string   `xml:"TabDisplay,omitempty"`
	HideToolbar  *bool    `xml:"HideToolbar,omitempty"`
	HideMenubar  *bool    `xml:"HideMenubar,omitempty"`
	HideWindowUI *bool    `xml:"HideWindowUI,omitempty"`
	ZoomMode     string   `xml:"ZoomMode,omitempty"`
	Zoom         *float64 `xml:"Zoom,omitempty"`
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

// UnmarshalXML 按命名空间读取文档结构，扩展数据仍由原解码器保留原文
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (doc *Document) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Document" {
		return fmt.Errorf("expected Document root, got %s", start.Name.Local)
	}
	*doc = Document{XMLName: start.Name, Permissions: defaultPermissions()}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target any
		switch node.Name.Local {
		case "CommonData":
			target = &doc.CommonData
		case "Pages":
			target = &doc.Pages
		case "Outlines":
			target = &doc.Outlines
		case "Permissions":
			target = &doc.Permissions
		case "Actions":
			return decodeOFDActions(d, node, &doc.Actions)
		case "VPreferences":
			doc.VPreferences = &VPreferences{}
			target = doc.VPreferences
		case "Bookmarks":
			target = &doc.Bookmarks
		case "Annotations":
			target = &doc.Annotations
		case "Attachments":
			target = &doc.Attachments
		case "CustomTags":
			target = &doc.CustomTags
		case "Extensions":
			target = &doc.Extensions
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
}

// PageIndex 按标识查找页面，优先匹配原文和标准十进制写法，等价编号有歧义时不匹配
// 入参: id 页面标识
// 返回: int 从0开始的页面索引，未找到时为-1
func (doc *Document) PageIndex(id string) int {
	if doc == nil {
		return -1
	}
	index, key := -1, editorResourceID(id)
	for i, page := range doc.Pages.Page {
		if page.ID == id {
			return i
		}
		if key != "" && page.ID == key {
			index = i
		}
	}
	if index >= 0 || key == "" {
		return index
	}
	for i, page := range doc.Pages.Page {
		if editorResourceID(page.ID) == key {
			if index >= 0 {
				return -1
			}
			index = i
		}
	}
	return index
}

// OutlineInfos 获取目录层级和目标页码
// 返回: []OutlineInfo 目录节点信息
func (doc *Document) OutlineInfos() []OutlineInfo {
	if len(doc.Outlines.OutlineElem) == 0 {
		return nil
	}
	bookmarks := make(map[string]Dest, len(doc.Bookmarks.Bookmark))
	for _, bookmark := range doc.Bookmarks.Bookmark {
		bookmarks[bookmark.Name] = bookmark.Dest
	}
	pages := make(map[string]int, len(doc.Pages.Page))
	for index, page := range doc.Pages.Page {
		pages[page.ID] = index + 1
	}
	return outlineInfos(doc.Outlines.OutlineElem, bookmarks, pages)
}

// UnmarshalXML 读取扩展信息索引或文档中的索引路径
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (extensions *Extensions) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Extensions" {
		return fmt.Errorf("expected Extensions root, got %s", start.Name.Local)
	}
	*extensions = Extensions{XMLName: start.Name}
	return decodeDocumentIndex(d, start, "Extension", &extensions.Path, &extensions.Extension)
}

// UnmarshalXML 读取扩展项，Data中的任意属性和XML原文保持不变
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (extension *Extension) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*extension = Extension{
		AppName: attrValue(start, "AppName"), Company: attrValue(start, "Company"),
		AppVersion: attrValue(start, "AppVersion"), Date: attrValue(start, "Date"), RefID: attrValue(start, "RefId"),
	}
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			var target any
			if ofdXMLChild(start.Name, node.Name) {
				switch node.Name.Local {
				case "Property":
					target = &extension.Property
				case "ExtendData":
					target = &extension.ExtendData
				case "Data":
					target = &extension.Data
				}
			}
			if target == nil {
				err = d.Skip()
			} else {
				err = d.DecodeElement(target, &node)
			}
			if err != nil {
				return err
			}
		case xml.EndElement:
			if node.Name == start.Name {
				return nil
			}
		}
	}
}

// UnmarshalXML 读取扩展属性名称、类型及文本值，忽略限定命名空间的同名属性
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (property *Property) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain Property
	var value plain
	start = ofdXMLStart(start)
	if err := d.DecodeElement(&value, &start); err != nil {
		return err
	}
	*property = Property(value)
	return nil
}

// UnmarshalXML 读取附件索引或文档中的索引路径，忽略外部同名条目
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (a *Attachments) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Attachments" {
		return fmt.Errorf("expected Attachments root, got %s", start.Name.Local)
	}
	*a = Attachments{XMLName: start.Name}
	return decodeDocumentIndex(d, start, "Attachment", &a.Path, &a.Attachment)
}

// FileName 获取另存文件名，优先使用带扩展名的显示名称，剔除目录及文件名非法字符
// 返回: string 文件名
func (a Attachment) FileName() string {
	name := path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
	file := path.Base(strings.ReplaceAll(a.FileLoc, "\\", "/"))
	if name == "." || name == "/" || path.Ext(name) == "" && path.Ext(file) != "" {
		name = file
	}
	if name == "." || name == "/" {
		name = "attachment"
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
}

// UnmarshalXML 解析附件属性和内容路径，省略可见性及用途时采用标准默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (a *Attachment) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*a = Attachment{
		ID: attrValue(start, "ID"), Name: attrValue(start, "Name"), Format: attrValue(start, "Format"),
		CreationDate: attrValue(start, "CreationDate"), ModDate: attrValue(start, "ModDate"),
		Visible: true, Usage: "none",
	}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		switch attr.Name.Local {
		case "Size":
			value, err := strconv.ParseFloat(strings.TrimSpace(attr.Value), 64)
			if err != nil {
				return err
			}
			a.Size = &value
		case "Visible":
			value, err := strconv.ParseBool(strings.TrimSpace(attr.Value))
			if err != nil {
				return err
			}
			a.Visible = value
		case "Usage":
			a.Usage = attr.Value
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "FileLoc" {
			return d.DecodeElement(&a.FileLoc, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 读取自定义标引索引或文档中的索引路径
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (tags *CustomTags) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "CustomTags" {
		return fmt.Errorf("expected CustomTags root, got %s", start.Name.Local)
	}
	*tags = CustomTags{XMLName: start.Name}
	return decodeDocumentIndex(d, start, "CustomTag", &tags.Path, &tags.CustomTag)
}

// UnmarshalXML 读取自定义标引的类型及文件路径，不采用外部同名字段
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (tag *CustomTag) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*tag = CustomTag{TypeID: attrValue(start, "TypeID"), NameSpace: attrValue(start, "NameSpace")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "SchemaLoc":
			return d.DecodeElement(&tag.SchemaLoc, &node)
		case "FileLoc":
			return d.DecodeElement(&tag.FileLoc, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 读取文档公共区域、模板与资源引用，忽略外部同名节点
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (common *CommonData) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*common = CommonData{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target any
		switch node.Name.Local {
		case "MaxUnitID":
			target = &common.MaxUnitID
		case "PageArea":
			target = &common.PageArea
		case "PublicRes":
			target = &common.PublicRes
		case "DocumentRes":
			target = &common.DocumentRes
		case "TemplatePage":
			target = &common.TemplatePage
		case "DefaultCS":
			target = &common.DefaultCS
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
}

// UnmarshalXML 读取页面区域，防止外部同名字段覆盖标准区域
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (area *PageArea) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*area = PageArea{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target *string
		switch node.Name.Local {
		case "PhysicalBox":
			target = &area.PhysicalBox
		case "ApplicationBox":
			target = &area.ApplicationBox
		case "ContentBox":
			target = &area.ContentBox
		case "BleedBox":
			target = &area.BleedBox
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
}

// UnmarshalXML 读取标准页面引用，忽略外部扩展中的同名条目
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (pages *Pages) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*pages = Pages{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Page" {
			return d.Skip()
		}
		var page Page
		if err := d.DecodeElement(&page, &node); err != nil {
			return err
		}
		pages.Page = append(pages.Page, page)
		return nil
	})
}

// UnmarshalXML 读取OFD书签列表，跳过外部同名条目
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (bookmarks *Bookmarks) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*bookmarks = Bookmarks{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Bookmark" {
			return d.Skip()
		}
		return d.DecodeElement(&bookmarks.Bookmark, &node)
	})
}

// UnmarshalXML 读取书签名称及目标，不使用外部扩展中的跳转信息
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (bookmark *Bookmark) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*bookmark = Bookmark{Name: attrValue(start, "Name")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "Dest" {
			return d.Skip()
		}
		return d.DecodeElement(&bookmark.Dest, &node)
	})
}

// UnmarshalXML 读取OFD大纲列表，跳过外部同名节点
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (outlines *Outlines) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*outlines = Outlines{}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local != "OutlineElem" {
			return d.Skip()
		}
		return d.DecodeElement(&outlines.OutlineElem, &node)
	})
}

// UnmarshalXML 解析大纲节点、子大纲和动作，并应用默认展开状态
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (o *OutlineElem) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*o = OutlineElem{Title: attrValue(start, "Title"), Expanded: true}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		value := strings.TrimSpace(attr.Value)
		var err error
		switch attr.Name.Local {
		case "Count":
			if value != "" {
				o.Count, err = strconv.Atoi(value)
			}
		case "Expanded":
			o.Expanded = false
			if value != "" {
				o.Expanded, err = strconv.ParseBool(value)
			}
		}
		if err != nil {
			return err
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Actions":
			return decodeOFDActions(d, node, &o.Actions)
		case "OutlineElem":
			return d.DecodeElement(&o.OutlineElem, &node)
		default:
			return d.Skip()
		}
	})
}

// UnmarshalXML 解析视图首选项并应用标准默认值，忽略外部命名空间的同名节点
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (v *VPreferences) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*v = VPreferences{
		PageMode:     "None",
		PageLayout:   "OneColumn",
		TabDisplay:   "DocTitle",
		HideToolbar:  new(bool),
		HideMenubar:  new(bool),
		HideWindowUI: new(bool),
	}
	zoomSet := false
	err := decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "PageMode":
			return d.DecodeElement(&v.PageMode, &node)
		case "PageLayout":
			return d.DecodeElement(&v.PageLayout, &node)
		case "TabDisplay":
			return d.DecodeElement(&v.TabDisplay, &node)
		case "HideToolbar":
			v.HideToolbar = new(bool)
			return d.DecodeElement(v.HideToolbar, &node)
		case "HideMenubar":
			v.HideMenubar = new(bool)
			return d.DecodeElement(v.HideMenubar, &node)
		case "HideWindowUI":
			v.HideWindowUI = new(bool)
			return d.DecodeElement(v.HideWindowUI, &node)
		case "ZoomMode":
			zoomSet = true
			return d.DecodeElement(&v.ZoomMode, &node)
		case "Zoom":
			zoomSet = true
			v.Zoom = new(float64)
			return d.DecodeElement(v.Zoom, &node)
		default:
			return d.Skip()
		}
	})
	if err != nil {
		return err
	}
	if !zoomSet {
		v.ZoomMode = "Default"
	}
	return nil
}

// UnmarshalXML 解析文档权限并应用默认值
// 入参: d XML解码器, start 起始节点
// 返回: error 错误信息
func (p *Permissions) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = defaultPermissions()
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Edit":
			return d.DecodeElement(&p.Edit, &node)
		case "Annot":
			return d.DecodeElement(&p.Annot, &node)
		case "Export":
			return d.DecodeElement(&p.Export, &node)
		case "Signature":
			return d.DecodeElement(&p.Signature, &node)
		case "Watermark":
			return d.DecodeElement(&p.Watermark, &node)
		case "PrintScreen":
			return d.DecodeElement(&p.PrintScreen, &node)
		case "CopyText":
			return d.DecodeElement(&p.Copy, &node)
		case "ContentRegist":
			return d.DecodeElement(&p.ContentRegist, &node)
		case "Print":
			var pr print
			node = ofdXMLStart(node)
			if err := d.DecodeElement(&pr, &node); err != nil {
				return err
			}
			if pr.Printable != nil {
				p.Print = *pr.Printable
			}
			if pr.Copies != nil {
				p.Copies = *pr.Copies
			}
			return nil
		case "ValidPeriod":
			var vp ValidPeriod
			node = ofdXMLStart(node)
			if err := d.DecodeElement(&vp, &node); err != nil {
				return err
			}
			p.ValidPeriod = &vp
			return nil
		default:
			return d.Skip()
		}
	})
}

// print 打印权限节点
type print struct {
	Printable *bool `xml:"Printable,attr"`
	Copies    *int  `xml:"Copies,attr"`
}

// decodeDocumentIndex 读取索引的直接条目及路径文本，不提升外部扩展中的节点
// 入参: d XML解码器, start 起始节点, name 条目名称, location 路径文本, values 条目列表
// 返回: error 解析错误
func decodeDocumentIndex[T any](d *xml.Decoder, start xml.StartElement, name string, location *string, values *[]T) error {
	var text strings.Builder
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if node.Name.Local == name && ofdXMLChild(start.Name, node.Name) {
				err = d.DecodeElement(values, &node)
			} else {
				err = d.Skip()
			}
			if err != nil {
				return err
			}
		case xml.CharData:
			text.Write(node)
		case xml.EndElement:
			if node.Name == start.Name {
				*location = text.String()
				return nil
			}
		}
	}
}

// outlineInfos 转换目录节点信息
// 入参: outlines 大纲节点, bookmarks 书签, pages 页码索引
// 返回: []OutlineInfo 目录节点信息
func outlineInfos(outlines []OutlineElem, bookmarks map[string]Dest, pages map[string]int) []OutlineInfo {
	infos := make([]OutlineInfo, 0, len(outlines))
	for _, outline := range outlines {
		info := OutlineInfo{Title: outline.Title, Expanded: outline.Expanded, ActionCount: len(outline.Actions)}
		for _, action := range outline.Actions {
			if action.Goto != nil {
				if dest := gotoDest(action.Goto, bookmarks); dest != nil {
					info.Page, _ = resourceValue(pages, dest.PageID)
					if info.Page != 0 {
						break
					}
				}
			}
		}
		if len(outline.OutlineElem) > 0 {
			info.Children = outlineInfos(outline.OutlineElem, bookmarks, pages)
		}
		infos = append(infos, info)
	}
	return infos
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
