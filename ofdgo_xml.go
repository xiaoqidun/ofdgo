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

import "unicode/utf8"

// 旧版与2016版OFD命名空间
const (
	ofdNamespaceLegacy = "http://www.ofdspec.org"
	ofdNamespace2016   = "http://www.ofdspec.org/2016"
)

// 未声明、未知、旧版及2016版OFD命名空间，不限制已知内容的读取
const (
	ofdXMLUnqualified ofdXMLNamespace = iota
	ofdXMLUnknown
	ofdXMLLegacy
	ofdXML2016
)

// ofdXMLNamespace 标识XML命名空间，不代表文档版本或功能支持范围
type ofdXMLNamespace uint8

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
