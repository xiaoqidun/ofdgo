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

// ofdXMLNamespace 标识XML节点采用的OFD命名空间
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
