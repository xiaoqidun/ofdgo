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

// Package ofdgo 首款原生、全平台兼容的纯 Go 语言 OFD 渲染库
package ofdgo

import "encoding/xml"

// Signatures 签名集合
type Signatures struct {
	XMLName   xml.Name    `xml:"Signatures"`
	MaxSignID string      `xml:"MaxSignId,attr"`
	Signature []Signature `xml:"Signature"`
}

// Signature 签名信息
type Signature struct {
	ID      string `xml:"ID,attr"`
	Type    string `xml:"Type,attr"`
	BaseLoc string `xml:"BaseLoc,attr"`
}

// Annotations 注释集合
type Annotations struct {
	XMLName xml.Name  `xml:"Annotations"`
	Page    []AnnPage `xml:"Page"`
}

// AnnPage 页面注释引用
type AnnPage struct {
	PageID     string       `xml:"PageID,attr"`
	Annotation []Annotation `xml:"Annotation"`
}

// Annotation 注释定义
type Annotation struct {
	ID          string `xml:"ID,attr"`
	Type        string `xml:"Type,attr"`
	Creator     string `xml:"Creator,attr"`
	LastModDate string `xml:"LastModDate,attr"`
	Loc         string `xml:"Loc,attr"`
}
