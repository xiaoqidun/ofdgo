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
	"slices"
	"strconv"
	"strings"
)

// Document 文档结构
// Signatures由阅读器按主入口和所选版本解析，不属于文档根节点的XML内容
type Document struct {
	XMLName        xml.Name      `xml:"Document"`
	CommonData     CommonData    `xml:"CommonData"`
	Pages          Pages         `xml:"Pages"`
	Outlines       Outlines      `xml:"Outlines"`
	Permissions    Permissions   `xml:"Permissions"`
	Actions        []Action      `xml:"Actions>Action"`
	VPreferences   *VPreferences `xml:"VPreferences,omitempty"`
	Bookmarks      Bookmarks     `xml:"Bookmarks"`
	Annotations    string        `xml:"Annotations"`
	Signatures     string        `xml:"-"`
	Attachments    Attachments   `xml:"Attachments"`
	CustomTags     CustomTags    `xml:"CustomTags"`
	Extensions     Extensions    `xml:"Extensions"`
	hasPermissions bool
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
	order      []extensionItem
}

// extensionItem 记录扩展节点内子元素的原始顺序
type extensionItem struct {
	kind  string
	index int
}

// Property 扩展属性
type Property struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
	Type  string `xml:"Type,attr"`
}

// ExtensionData 扩展数据
type ExtensionData struct {
	Attr       []xml.Attr `xml:",any,attr"`
	Content    string     `xml:",innerxml"`
	namespaces []xml.Attr
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
	MaxUnitID    uint64         `xml:"MaxUnitID"`
	PageArea     PageArea       `xml:"PageArea"`
	PublicRes    []string       `xml:"PublicRes"`
	DocumentRes  []string       `xml:"DocumentRes"`
	TemplatePage []TemplatePage `xml:"TemplatePage"`
	DefaultCS    uint64         `xml:"DefaultCS,omitempty"`
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
	PageMode     string   `xml:"PageMode,omitempty" json:"pageMode,omitempty"`
	PageLayout   string   `xml:"PageLayout,omitempty" json:"pageLayout,omitempty"`
	TabDisplay   string   `xml:"TabDisplay,omitempty" json:"tabDisplay,omitempty"`
	HideToolbar  *bool    `xml:"HideToolbar,omitempty" json:"hideToolbar,omitempty"`
	HideMenubar  *bool    `xml:"HideMenubar,omitempty" json:"hideMenubar,omitempty"`
	HideWindowUI *bool    `xml:"HideWindowUI,omitempty" json:"hideWindowUI,omitempty"`
	ZoomMode     string   `xml:"ZoomMode,omitempty" json:"zoomMode,omitempty"`
	Zoom         *float64 `xml:"Zoom,omitempty" json:"zoom,omitempty"`
}

// Permissions 权限声明
type Permissions struct {
	Edit        bool         `xml:"Edit" json:"edit"`
	Annot       bool         `xml:"Annot" json:"annot"`
	Export      bool         `xml:"Export" json:"export"`
	Signature   bool         `xml:"Signature" json:"signature"`
	Watermark   bool         `xml:"Watermark" json:"watermark"`
	PrintScreen bool         `xml:"PrintScreen" json:"printScreen"`
	Print       bool         `xml:"-" json:"print"`
	Copies      int          `xml:"-" json:"copies"`
	ValidPeriod *ValidPeriod `xml:"ValidPeriod" json:"validPeriod,omitempty"`
}

// ValidPeriod 文档访问有效期
type ValidPeriod struct {
	StartDate string `xml:"StartDate,attr" json:"startDate,omitempty"`
	EndDate   string `xml:"EndDate,attr" json:"endDate,omitempty"`
}

// print 打印权限节点
type print struct {
	Printable *bool `xml:"Printable,attr"`
	Copies    *int  `xml:"Copies,attr"`
}

// DefaultPermissions 获取GB/T 33190标准默认文档权限
// 返回: Permissions 默认文档权限
func DefaultPermissions() Permissions {
	return Permissions{
		Edit:        true,
		Annot:       true,
		Export:      true,
		Signature:   true,
		Watermark:   true,
		PrintScreen: true,
		Print:       true,
		Copies:      -1,
	}
}

// decodeOFDTrimmedUint64 解析忽略前后空白的无符号整数子节点
// 入参: d XML解码器, start 起始节点, target 目标无符号整数指针
// 返回: error 解析错误
func decodeOFDTrimmedUint64(d *xml.Decoder, start xml.StartElement, target *uint64) error {
	var raw string
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		*target = 0
		return nil
	}
	value, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return err
	}
	*target = value
	return nil
}

// decodeOFDTrimmedString 解析并裁剪节点文本前后空白
// 入参: d XML解码器, start 起始节点, target 目标字符串指针
// 返回: error 解析错误
func decodeOFDTrimmedString(d *xml.Decoder, start xml.StartElement, target *string) error {
	var raw string
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	if value := strings.TrimSpace(raw); value != "" {
		*target = value
	}
	return nil
}

// decodeOFDTrimmedBool 解析忽略前后空白的布尔子节点
// 入参: d XML解码器, start 起始节点, target 目标布尔指针
// 返回: error 解析错误
func decodeOFDTrimmedBool(d *xml.Decoder, start xml.StartElement, target *bool) error {
	var raw string
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	*target = value
	return nil
}

// decodeOFDTrimmedFloat 解析忽略前后空白的浮点子节点
// 入参: d XML解码器, start 起始节点, target 目标浮点指针
// 返回: error 解析错误
func decodeOFDTrimmedFloat(d *xml.Decoder, start xml.StartElement, target *float64) error {
	var raw string
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return err
	}
	*target = value
	return nil
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

// UnmarshalXML 按命名空间读取文档结构，扩展数据仍由原解码器保留原文
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (doc *Document) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Document" {
		return fmt.Errorf("expected Document root, got %s", start.Name.Local)
	}
	*doc = Document{XMLName: start.Name, Permissions: DefaultPermissions()}
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
			doc.hasPermissions = true
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

// HasPermissions 判断文档是否显式声明了Permissions节点
// 返回: bool 是否显式声明权限
func (doc *Document) HasPermissions() bool {
	return doc != nil && doc.hasPermissions
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

// collectXMLNamespaces 合并父级与当前元素的命名空间声明，保持首次声明顺序并按前缀去重
// 入参: attrs 当前元素属性, inherited 继承的命名空间声明
// 返回: []xml.Attr 合并后的命名空间声明
func collectXMLNamespaces(attrs, inherited []xml.Attr) []xml.Attr {
	result := slices.Clone(inherited)
	put := func(key, uri string) {
		for i := range result {
			if result[i].Name.Local == key {
				result[i].Value = uri
				return
			}
		}
		result = append(result, xml.Attr{Name: xml.Name{Local: key}, Value: uri})
	}
	for _, attr := range attrs {
		switch {
		case attr.Name.Space == "" && attr.Name.Local == "xmlns":
			put("xmlns", attr.Value)
		case attr.Name.Space == "xmlns" && attr.Name.Local != "":
			put("xmlns:"+attr.Name.Local, attr.Value)
		case attr.Name.Space == "" && strings.HasPrefix(attr.Name.Local, "xmlns:"):
			put(attr.Name.Local, attr.Value)
		}
	}
	return result
}

// UnmarshalXML 读取扩展信息索引或文档中的索引路径并向下传递命名空间声明
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (extensions *Extensions) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Extensions" {
		return fmt.Errorf("expected Extensions root, got %s", start.Name.Local)
	}
	*extensions = Extensions{XMLName: start.Name}
	inherited := collectXMLNamespaces(start.Attr, nil)
	var text strings.Builder
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if node.Name.Local == "Extension" && ofdXMLChild(start.Name, node.Name) {
				var item Extension
				if err := item.decodeXML(d, node, inherited); err != nil {
					return err
				}
				extensions.Extension = append(extensions.Extension, item)
			} else if err := d.Skip(); err != nil {
				return err
			}
		case xml.CharData:
			text.Write(node)
		case xml.EndElement:
			if node.Name == start.Name {
				extensions.Path = text.String()
				return nil
			}
		}
	}
}

// UnmarshalXML 读取扩展项，保留子节点顺序与命名空间声明
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (extension *Extension) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return extension.decodeXML(d, start, nil)
}

// decodeXML 解析扩展项并继承外层命名空间声明，保持Property、ExtendData与Data的原始顺序
// 入参: d XML解码器, start 起始节点, inherited 继承的命名空间声明
// 返回: error 解析错误
func (extension *Extension) decodeXML(d *xml.Decoder, start xml.StartElement, inherited []xml.Attr) error {
	*extension = Extension{
		AppName: attrValue(start, "AppName"), Company: attrValue(start, "Company"),
		AppVersion: attrValue(start, "AppVersion"), Date: attrValue(start, "Date"), RefID: attrValue(start, "RefId"),
	}
	ns := collectXMLNamespaces(start.Attr, inherited)
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if ofdXMLChild(start.Name, node.Name) {
				switch node.Name.Local {
				case "Property":
					var prop Property
					if err := d.DecodeElement(&prop, &node); err != nil {
						return err
					}
					extension.Property = append(extension.Property, prop)
					extension.order = append(extension.order, extensionItem{kind: "Property", index: len(extension.Property) - 1})
					continue
				case "ExtendData":
					var loc string
					if err := d.DecodeElement(&loc, &node); err != nil {
						return err
					}
					extension.ExtendData = append(extension.ExtendData, loc)
					extension.order = append(extension.order, extensionItem{kind: "ExtendData", index: len(extension.ExtendData) - 1})
					continue
				case "Data":
					var data ExtensionData
					if err := data.decodeXML(d, node, ns); err != nil {
						return err
					}
					extension.Data = append(extension.Data, data)
					extension.order = append(extension.order, extensionItem{kind: "Data", index: len(extension.Data) - 1})
					continue
				}
			}
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if node.Name == start.Name {
				return nil
			}
		}
	}
}

// MarshalXML 按原始子节点顺序写出扩展项
// 入参: e XML编码器, start 起始节点
// 返回: error 编码错误
func (extension Extension) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if start.Name.Local == "" {
		start.Name = xml.Name{Local: "Extension"}
	}
	start.Attr = nil
	for _, pair := range [][2]string{
		{"AppName", extension.AppName},
		{"Company", extension.Company},
		{"AppVersion", extension.AppVersion},
		{"Date", extension.Date},
		{"RefId", extension.RefID},
	} {
		if pair[1] != "" {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: pair[0]}, Value: pair[1]})
		}
	}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	childName := func(local string) xml.StartElement {
		return xml.StartElement{Name: xml.Name{Space: start.Name.Space, Local: local}}
	}
	usedProp := make([]bool, len(extension.Property))
	usedExt := make([]bool, len(extension.ExtendData))
	usedData := make([]bool, len(extension.Data))
	encodeItem := func(kind string, idx int) error {
		switch kind {
		case "Property":
			if idx < 0 || idx >= len(extension.Property) || usedProp[idx] {
				return nil
			}
			usedProp[idx] = true
			return e.EncodeElement(extension.Property[idx], childName("Property"))
		case "ExtendData":
			if idx < 0 || idx >= len(extension.ExtendData) || usedExt[idx] {
				return nil
			}
			usedExt[idx] = true
			return e.EncodeElement(extension.ExtendData[idx], childName("ExtendData"))
		case "Data":
			if idx < 0 || idx >= len(extension.Data) || usedData[idx] {
				return nil
			}
			usedData[idx] = true
			return e.EncodeElement(extension.Data[idx], childName("Data"))
		}
		return nil
	}
	for _, item := range extension.order {
		if err := encodeItem(item.kind, item.index); err != nil {
			return err
		}
	}
	for i := range extension.Property {
		if err := encodeItem("Property", i); err != nil {
			return err
		}
	}
	for i := range extension.ExtendData {
		if err := encodeItem("ExtendData", i); err != nil {
			return err
		}
	}
	for i := range extension.Data {
		if err := encodeItem("Data", i); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

// UnmarshalXML 解析扩展数据，保留属性、命名空间声明与原始内部XML
// 入参: d XML解码器, start 起始节点
// 返回: error 解析错误
func (data *ExtensionData) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return data.decodeXML(d, start, nil)
}

// decodeXML 解析扩展数据并合并继承自祖先元素的命名空间声明
// 入参: d XML解码器, start 起始节点, inherited 继承的命名空间声明
// 返回: error 解析错误
func (data *ExtensionData) decodeXML(d *xml.Decoder, start xml.StartElement, inherited []xml.Attr) error {
	var raw struct {
		Content string `xml:",innerxml"`
	}
	if err := d.DecodeElement(&raw, &start); err != nil {
		return err
	}
	*data = ExtensionData{
		Attr:       slices.Clone(start.Attr),
		Content:    raw.Content,
		namespaces: collectXMLNamespaces(start.Attr, inherited),
	}
	return nil
}

// MarshalXML 写出扩展数据，准确还原命名空间声明与属性前缀并保持内部XML顺序
// 入参: e XML编码器, start 起始节点
// 返回: error 编码错误
func (data ExtensionData) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if start.Name.Local == "" {
		start.Name = xml.Name{Local: "Data"}
	}
	nsDecls := collectXMLNamespaces(data.Attr, data.namespaces)
	nsDecls = collectXMLNamespaces(start.Attr, nsDecls)
	uriPrefix := make(map[string]string, len(nsDecls))
	usedPrefix := make(map[string]bool, len(nsDecls))
	var attrs []xml.Attr
	for _, decl := range nsDecls {
		if decl.Name.Local == "xmlns" && start.Name.Space != "" && decl.Value == start.Name.Space {
			continue
		}
		if prefix, ok := strings.CutPrefix(decl.Name.Local, "xmlns:"); ok {
			usedPrefix[prefix] = true
			if _, exists := uriPrefix[decl.Value]; !exists {
				uriPrefix[decl.Value] = prefix
			}
		}
		attrs = append(attrs, decl)
	}
	nextPrefix := 1
	for _, attr := range data.Attr {
		if attr.Name.Space == "xmlns" || attr.Name.Space == "" && (attr.Name.Local == "xmlns" || strings.HasPrefix(attr.Name.Local, "xmlns:")) {
			continue
		}
		if attr.Name.Space == "" {
			attrs = append(attrs, attr)
			continue
		}
		prefix, ok := uriPrefix[attr.Name.Space]
		if !ok {
			for {
				candidate := "ns" + strconv.Itoa(nextPrefix)
				nextPrefix++
				if !usedPrefix[candidate] {
					prefix = candidate
					break
				}
			}
			usedPrefix[prefix] = true
			uriPrefix[attr.Name.Space] = prefix
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns:" + prefix}, Value: attr.Name.Space})
		}
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: prefix + ":" + attr.Name.Local}, Value: attr.Value})
	}
	start.Attr = attrs
	return e.EncodeElement(struct {
		Content string `xml:",innerxml"`
	}{Content: data.Content}, start)
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
		switch node.Name.Local {
		case "MaxUnitID":
			return decodeOFDTrimmedUint64(d, node, &common.MaxUnitID)
		case "PageArea":
			return d.DecodeElement(&common.PageArea, &node)
		case "PublicRes":
			return d.DecodeElement(&common.PublicRes, &node)
		case "DocumentRes":
			return d.DecodeElement(&common.DocumentRes, &node)
		case "TemplatePage":
			return d.DecodeElement(&common.TemplatePage, &node)
		case "DefaultCS":
			return decodeOFDTrimmedUint64(d, node, &common.DefaultCS)
		default:
			return d.Skip()
		}
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
			return decodeOFDTrimmedString(d, node, &v.PageMode)
		case "PageLayout":
			return decodeOFDTrimmedString(d, node, &v.PageLayout)
		case "TabDisplay":
			return decodeOFDTrimmedString(d, node, &v.TabDisplay)
		case "HideToolbar":
			v.HideToolbar = new(bool)
			return decodeOFDTrimmedBool(d, node, v.HideToolbar)
		case "HideMenubar":
			v.HideMenubar = new(bool)
			return decodeOFDTrimmedBool(d, node, v.HideMenubar)
		case "HideWindowUI":
			v.HideWindowUI = new(bool)
			return decodeOFDTrimmedBool(d, node, v.HideWindowUI)
		case "ZoomMode":
			zoomSet = true
			v.Zoom = nil
			return decodeOFDTrimmedString(d, node, &v.ZoomMode)
		case "Zoom":
			zoomSet = true
			v.ZoomMode = ""
			v.Zoom = new(float64)
			return decodeOFDTrimmedFloat(d, node, v.Zoom)
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
	*p = DefaultPermissions()
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "Edit":
			return decodeOFDTrimmedBool(d, node, &p.Edit)
		case "Annot":
			return decodeOFDTrimmedBool(d, node, &p.Annot)
		case "Export":
			return decodeOFDTrimmedBool(d, node, &p.Export)
		case "Signature":
			return decodeOFDTrimmedBool(d, node, &p.Signature)
		case "Watermark":
			return decodeOFDTrimmedBool(d, node, &p.Watermark)
		case "PrintScreen":
			return decodeOFDTrimmedBool(d, node, &p.PrintScreen)
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
			vp.StartDate = strings.TrimSpace(vp.StartDate)
			vp.EndDate = strings.TrimSpace(vp.EndDate)
			p.ValidPeriod = &vp
			return nil
		default:
			return d.Skip()
		}
	})
}

// MarshalXML 按GB/T 33190标准序列化文档权限，与UnmarshalXML严格对称
// 入参: e XML编码器, start 起始节点
// 返回: error 编码错误
func (p Permissions) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if start.Name.Local == "" {
		start.Name.Local = "Permissions"
	}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	space := start.Name.Space
	encodeBool := func(local string, value bool) error {
		return e.EncodeElement(strconv.FormatBool(value), xml.StartElement{Name: xml.Name{Space: space, Local: local}})
	}
	for _, item := range []struct {
		name  string
		value bool
	}{
		{"Edit", p.Edit},
		{"Annot", p.Annot},
		{"Export", p.Export},
		{"Signature", p.Signature},
		{"Watermark", p.Watermark},
		{"PrintScreen", p.PrintScreen},
	} {
		if err := encodeBool(item.name, item.value); err != nil {
			return err
		}
	}
	printElem := xml.StartElement{
		Name: xml.Name{Space: space, Local: "Print"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "Printable"}, Value: strconv.FormatBool(p.Print)},
			{Name: xml.Name{Local: "Copies"}, Value: strconv.Itoa(p.Copies)},
		},
	}
	if err := e.EncodeToken(printElem); err != nil {
		return err
	}
	if err := e.EncodeToken(printElem.End()); err != nil {
		return err
	}
	if p.ValidPeriod != nil {
		vpElem := xml.StartElement{Name: xml.Name{Space: space, Local: "ValidPeriod"}}
		if startDate := strings.TrimSpace(p.ValidPeriod.StartDate); startDate != "" {
			vpElem.Attr = append(vpElem.Attr, xml.Attr{Name: xml.Name{Local: "StartDate"}, Value: startDate})
		}
		if endDate := strings.TrimSpace(p.ValidPeriod.EndDate); endDate != "" {
			vpElem.Attr = append(vpElem.Attr, xml.Attr{Name: xml.Name{Local: "EndDate"}, Value: endDate})
		}
		if err := e.EncodeToken(vpElem); err != nil {
			return err
		}
		if err := e.EncodeToken(vpElem.End()); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}
