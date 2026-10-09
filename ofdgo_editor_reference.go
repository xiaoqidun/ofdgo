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
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"maps"
	"strings"
	"unicode/utf8"
)

// editorVectorUsageLimit 限制单个矢量引用快照的记录数，不限制资源内容
const editorVectorUsageLimit = 4096

// editorGeneratedReferences 保存本次输出条目及其引用，不跨写出复用
type editorGeneratedReferences struct {
	data   []byte
	refs   *editorResourceRefs
	staged *editorStagedPage
}

// matches 判断条目仍为本次编码结果，空条目与暂存占位不混用
// 入参: data 当前条目数据
// 返回: bool 是否可以复用本次引用
func (r editorGeneratedReferences) matches(data []byte) bool {
	if r.staged != nil {
		return data == nil
	}
	return bytes.Equal(data, r.data)
}

// editorVectorUsage 保存自产矢量资源的引用快照，内容和路径变化时失效
type editorVectorUsage struct {
	name   string
	digest [32]byte
	refs   *editorResourceRefs
}

// matches 核对资源路径和完整内容摘要，不以切片地址判断内容
// 入参: name 当前资源路径, data 当前资源内容
// 返回: bool 是否仍可使用编码时的引用
func (u *editorVectorUsage) matches(name string, data []byte) bool {
	return u != nil && u.name == name && u.digest == sha256.Sum256(data)
}

// generatedVectorReferences 合并未改变的自产矢量引用，不修改调用方集合
// 入参: parts 输出条目, generated 本次已知引用
// 返回: map[string]editorGeneratedReferences 本次可复用引用
func (e *Editor) generatedVectorReferences(parts map[string][]byte, generated map[string]editorGeneratedReferences) map[string]editorGeneratedReferences {
	copied := false
	for _, resource := range e.resources {
		if e.output != nil && e.output.ctx.Err() != nil {
			return generated
		}
		data, ok := parts[resource.name]
		if !ok || !resource.usage.matches(resource.name, data) {
			continue
		}
		if !copied {
			generated = maps.Clone(generated)
			if generated == nil {
				generated = make(map[string]editorGeneratedReferences)
			}
			copied = true
		}
		generated[resource.name] = editorGeneratedReferences{data: data, refs: resource.usage.refs}
	}
	return generated
}

// editorReferenceScan 按XML事件收集资源和字体用字，未知内容保持完整扫描
type editorReferenceScan struct {
	refs     *editorResourceRefs
	objects  map[string]bool
	name     string
	base     string
	stack    []string
	fonts    []*editorFontUsage
	text     strings.Builder
	resource bool
	capture  bool
	safe     bool
	seen     bool
}

// vectorUsage 保留完整且限额内的自产矢量引用，超限时保存流程重新扫描
// 入参: data 本次编码内容
// 返回: *editorVectorUsage 引用快照，无法复用时为空
func (s *editorReferenceScan) vectorUsage(data []byte) *editorVectorUsage {
	if !s.safe || !s.seen || len(s.stack) != 0 {
		return nil
	}
	count := len(s.refs.ids) + len(s.refs.files) + len(s.refs.fonts)
	for _, usage := range s.refs.fonts {
		count += len(usage.chars) + len(usage.glyphs)
		if count > editorVectorUsageLimit {
			return nil
		}
	}
	if count > editorVectorUsageLimit {
		return nil
	}
	return &editorVectorUsage{name: s.name, digest: sha256.Sum256(data), refs: s.refs}
}

// accept 共用读取和自产页面的引用判断，不将对象自身标识视为引用
// 入参: token 已解析或已编码的XML事件
// 返回: bool 是否仍可完整判断引用
func (s *editorReferenceScan) accept(token xml.Token) bool {
	if !s.safe {
		return false
	}
	switch token := token.(type) {
	case xml.StartElement:
		if token.Name.Space != "" && token.Name.Space != ofdNamespace2016 {
			s.safe = false
			return false
		}
		if token.Name.Local == "CustomTags" || token.Name.Local == "Extensions" || token.Name.Local == "ExtendData" || token.Name.Local == "Data" {
			s.safe = false
			return false
		}
		if len(s.stack) == 0 {
			s.seen = true
			s.resource = token.Name.Local == "Res"
			if s.resource {
				for _, attr := range token.Attr {
					if attr.Name.Local == "BaseLoc" {
						s.base = attr.Value
					}
				}
			}
		}
		var usage *editorFontUsage
		if len(s.fonts) != 0 {
			usage = s.fonts[len(s.fonts)-1]
			if usage != nil && (s.stack[len(s.stack)-1] == "TextCode" || s.stack[len(s.stack)-1] == "Glyphs") {
				usage.unsafe = true
			}
		}
		for _, attr := range token.Attr {
			if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" || attr.Name.Local == "xmlns:ofd" || attr.Name.Space == "http://www.w3.org/XML/1998/namespace" {
				continue
			}
			if attr.Name.Space != "" {
				s.safe = false
				return false
			}
			if attr.Name.Local != "ID" {
				s.refs.reference(s.name, s.base, attr.Name.Local, attr.Value, s.resource)
			}
			if s.objects != nil && editorObjectReference(attr.Name.Local) {
				s.objects[attr.Value] = true
			}
			if attr.Name.Local == "Font" {
				usage = s.refs.fontUsage(attr.Value)
				if token.Name.Local != "TextObject" && token.Name.Local != "Text" {
					usage.unsafe = true
				}
			} else if attr.Name.Local == "Substitution" {
				s.refs.fontUsage(attr.Value).unsafe = true
			}
		}
		s.stack = append(s.stack, token.Name.Local)
		s.fonts = append(s.fonts, usage)
		s.text.Reset()
		s.capture = editorResourceReferenceKind(token.Name.Local, s.resource) != editorReferenceNone || usage != nil && (token.Name.Local == "TextCode" || token.Name.Local == "Glyphs")
	case xml.CharData:
		if s.capture {
			s.text.Write(token)
		}
	case xml.EndElement:
		if len(s.stack) == 0 || s.stack[len(s.stack)-1] != token.Name.Local {
			s.safe = false
			return false
		}
		s.refs.reference(s.name, s.base, token.Name.Local, s.text.String(), s.resource)
		if s.objects != nil && (token.Name.Local == "Thumbnail" || token.Name.Local == "Substitution") {
			if id := editorResourceID(s.text.String()); id != "" {
				s.objects[id] = true
			}
		}
		if (token.Name.Local == "Substitution" || token.Name.Local == "Font") && strings.TrimSpace(s.text.String()) != "" {
			s.refs.fontUsage(s.text.String()).unsafe = true
		}
		if usage := s.fonts[len(s.fonts)-1]; usage != nil {
			usage.text(token.Name.Local, s.text.String())
		}
		s.stack = s.stack[:len(s.stack)-1]
		s.fonts = s.fonts[:len(s.fonts)-1]
		s.text.Reset()
		s.capture = false
		if len(s.stack) != 0 {
			key, usage := s.stack[len(s.stack)-1], s.fonts[len(s.fonts)-1]
			s.capture = editorResourceReferenceKind(key, s.resource) != editorReferenceNone || usage != nil && (key == "TextCode" || key == "Glyphs")
		}
	case xml.Directive:
		s.safe = false
	}
	return s.safe
}

// merge 合并本次编码已确认的引用，不共享字体用字的可变集合
// 入参: other 独立页面引用
func (r *editorResourceRefs) merge(other *editorResourceRefs) {
	maps.Copy(r.ids, other.ids)
	maps.Copy(r.files, other.files)
	for id, usage := range other.fonts {
		r.fontUsage(id).merge(usage, false)
	}
}

// referenceToken 记录自产OFD事件，编码会替换的字符使本页回到完整扫描
// 入参: token 已写入的XML事件
func (x *ofdXML) referenceToken(token xml.Token) {
	if x.references == nil || x.err != nil {
		return
	}
	s := x.references
	switch value := token.(type) {
	case xml.StartElement:
		value.Name = xml.Name{Space: ofdNamespace2016, Local: strings.TrimPrefix(value.Name.Local, "ofd:")}
		for _, attr := range value.Attr {
			if editorResourceReferenceKind(attr.Name.Local, s.resource) != editorReferenceNone && !editorReferenceTextSafe(attr.Value) {
				s.safe = false
			}
		}
		token = value
	case xml.EndElement:
		value.Name = xml.Name{Space: ofdNamespace2016, Local: strings.TrimPrefix(value.Name.Local, "ofd:")}
		token = value
	case xml.CharData:
		if s.capture && !editorReferenceTextSafe(string(value)) {
			s.safe = false
		}
	}
	if !s.accept(token) {
		x.references = nil
	}
}

// referenceElement 收集已知叶节点的引用，其他结构仍完整扫描
// 入参: name 元素名称, value 已编码的子树内容
func (x *ofdXML) referenceElement(name string, value any) {
	if x.references == nil || x.err != nil {
		return
	}
	var attrs ofdAttrs
	switch value := value.(type) {
	case *Dest:
		if value == nil {
			return
		}
		attrs.add("PageID", value.PageID)
	case ShdColor:
		attrs.add("ColorSpace", value.ColorSpace)
	case *ShdColor:
		if value == nil {
			return
		}
		attrs.add("ColorSpace", value.ColorSpace)
	case *Sound:
		if value == nil {
			return
		}
		attrs.add("ResourceID", value.ResourceID)
	case *Movie:
		if value == nil {
			return
		}
		attrs.add("ResourceID", value.ResourceID)
	case *GotoBookmark, *URI, *GotoA:
	default:
		x.references.safe = false
		x.references = nil
		return
	}
	x.referenceToken(xml.StartElement{Name: xml.Name{Local: "ofd:" + name}, Attr: attrs})
	x.referenceToken(xml.EndElement{Name: xml.Name{Local: "ofd:" + name}})
}

// editorReferenceTextSafe 判断XML编码是否保持引用文本的Unicode值
// 入参: value 原始引用或字体用字
// 返回: bool 是否无需字符替换
func editorReferenceTextSafe(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char != '\t' && char != '\n' && char != '\r' && (char < 0x20 || char == 0xfffe || char == 0xffff) {
			return false
		}
	}
	return true
}
