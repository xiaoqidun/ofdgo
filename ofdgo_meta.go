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

// OFD OFD入口结构
// 代表OFD文档的根节点
type OFD struct {
	XMLName xml.Name  `xml:"OFD"`
	Version string    `xml:"Version,attr"`
	DocType string    `xml:"DocType,attr"`
	DocBody []DocBody `xml:"DocBody"`
}

// DocBody 文档体信息
// 包含文档元数据和根节点路径
type DocBody struct {
	DocInfo    DocInfo `xml:"DocInfo"`
	DocRoot    string  `xml:"DocRoot"`
	Signatures string  `xml:"Signatures"`
}

// DocInfo 文档元数据
type DocInfo struct {
	DocID        string       `xml:"DocID"`
	Title        string       `xml:"Title"`
	Author       string       `xml:"Author"`
	Subject      string       `xml:"Subject"`
	Abstract     string       `xml:"Abstract"`
	CreationDate string       `xml:"CreationDate"`
	ModDate      string       `xml:"ModDate"`
	CustomDatas  *CustomDatas `xml:"CustomDatas"`
}

// CustomDatas 自定义数据集合
type CustomDatas struct {
	CustomData []CustomData `xml:"CustomData"`
}

// CustomData 自定义数据项
type CustomData struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
}
