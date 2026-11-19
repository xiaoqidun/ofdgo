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
	"context"
	"crypto/sha256"
	"encoding/xml"
	"maps"
	"strings"
	"unicode/utf8"
)

// editorVectorUsageLimit 限制单个矢量引用快照的记录数，不限制资源内容
const editorVectorUsageLimit = 4096

// editorGeneratedReferences 保存本次输出条目、资源引用及完整图元编号，不跨写出复用
type editorGeneratedReferences struct {
	data    []byte
	refs    *editorResourceRefs
	staged  *editorStagedPage
	objects map[string]bool
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

// stagedLeaf 判断暂存页面是否无需改写文件路径，资源标识仍由资源索引解析
// 入参: data 当前条目数据
// 返回: bool 是否为引用完整且不含文件路径的暂存页面
func (r editorGeneratedReferences) stagedLeaf(data []byte) bool {
	return r.staged != nil && data == nil && r.refs != nil && len(r.refs.files) == 0
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

// editorReferenceScan 按XML事件收集资源、字体用字和图元编号，未知内容保持完整扫描
type editorReferenceScan struct {
	ctx               context.Context
	refs              *editorResourceRefs
	objects           map[string]bool
	graphicIDs        map[string]bool
	name              string
	base              string
	stack             []string
	fonts             []*editorFontUsage
	text              strings.Builder
	richText          strings.Builder
	currentCustomTag  CustomTag
	resource          bool
	capture           bool
	externalCustomTag bool
	safe              bool
	seen              bool
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

// customTagsDepth 返回栈内合法CustomTags容器的深度索引，不存在时返回-1
// 返回: int 栈内CustomTags索引
func (s *editorReferenceScan) customTagsDepth() int {
	if len(s.stack) > 0 && s.stack[0] == "CustomTags" {
		return 0
	}
	if len(s.stack) > 1 && s.stack[0] == "Document" && s.stack[1] == "CustomTags" {
		return 1
	}
	return -1
}

// accept 共用读取和自产页面的引用判断，不将对象自身标识视为引用
// 入参: token 已解析或已编码的XML事件
// 返回: bool 是否仍可完整判断引用
func (s *editorReferenceScan) accept(token xml.Token) bool {
	if s.ctx != nil && s.ctx.Err() != nil {
		s.safe = false
	}
	if !s.safe {
		return false
	}
	switch token := token.(type) {
	case xml.StartElement:
		plain := token.Name.Space == textNamespace
		rich := token.Name.Space == richTextNamespace
		known := classifyOFDNamespace(token.Name.Space) != ofdXMLUnknown
		if plain {
			known = len(s.stack) == 0 && token.Name.Local == "Text" || len(s.stack) == 1 && s.stack[0] == "Text" && token.Name.Local == "ObjectRef"
		} else if rich {
			known = len(s.stack) == 0 && token.Name.Local == "RichText" || len(s.stack) == 1 && s.stack[0] == "RichText" && token.Name.Local == "ObjectRef"
		} else if len(s.stack) > 0 && (s.stack[0] == "RichText" || s.stack[0] == "Text") {
			known = false
		}
		if token.Name.Local == "CustomTags" {
			known = known && (len(s.stack) == 0 || len(s.stack) == 1 && s.stack[0] == "Document")
		}
		ctDepth := s.customTagsDepth()
		if ctDepth >= 0 {
			known = known && (len(s.stack) == ctDepth+1 && token.Name.Local == "CustomTag" || len(s.stack) == ctDepth+2 && s.stack[ctDepth+1] == "CustomTag" && (token.Name.Local == "FileLoc" || token.Name.Local == "SchemaLoc"))
		} else if token.Name.Local == "CustomTag" {
			known = false
		}
		if !known {
			s.safe = false
			return false
		}
		if token.Name.Local == "Extensions" || token.Name.Local == "ExtendData" || token.Name.Local == "Data" {
			s.safe = false
			return false
		}
		if token.Name.Local == "CustomTag" {
			var kind, namespace string
			for _, attr := range token.Attr {
				if attr.Name.Space == "" && attr.Name.Local == "TypeID" {
					kind = attr.Value
				}
				if attr.Name.Space == "" && attr.Name.Local == "NameSpace" {
					namespace = attr.Value
				}
			}
			tag := CustomTag{TypeID: kind, NameSpace: namespace}
			internalFontSafe := kind == textTagType && namespace == textNamespace || kind == richTextTagType && namespace == richTextNamespace
			if !internalFontSafe && !isKnownResourceSafeTagMeta(tag) {
				s.safe = false
				return false
			}
			s.currentCustomTag = tag
			s.externalCustomTag = !internalFontSafe
		}
		if len(s.stack) == 0 {
			if s.seen {
				s.safe = false
				return false
			}
			s.seen = true
			if token.Name.Local == "Document" {
				if s.refs.documents == nil {
					s.refs.documents = make(map[string]bool)
				}
				s.refs.documents[s.name] = true
			}
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
		switch token.Name.Local {
		case "Template", "PageRes", "CompositeObject", "CompositeGraphicUnit", "PageBlock":
			s.graphicIDs = nil
		}
		if len(s.fonts) != 0 {
			usage = s.fonts[len(s.fonts)-1]
			if usage != nil && (s.stack[len(s.stack)-1] == "TextCode" || s.stack[len(s.stack)-1] == "Glyphs") {
				usage.unsafe = true
			}
		}
		for _, attr := range token.Attr {
			if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" || attr.Name.Local == "xmlns:ofd" || attr.Name.Space == XMLNamespace {
				continue
			}
			custom := token.Name.Local == "CustomTags" || ctDepth >= 0
			if attr.Name.Space != "" || (rich || plain) && (token.Name.Local != "ObjectRef" || attr.Name.Local != "PageRef") || custom && (token.Name.Local != "CustomTag" || attr.Name.Local != "TypeID" && attr.Name.Local != "NameSpace") {
				s.safe = false
				return false
			}
			if attr.Name.Local != "ID" {
				s.refs.reference(s.name, s.base, attr.Name.Local, attr.Value, s.resource)
			} else if s.graphicIDs != nil {
				switch token.Name.Local {
				case "TextObject", "PathObject", "ImageObject":
					if id := editorResourceID(attr.Value); id != "" {
						s.graphicIDs[id] = true
					}
				}
			}
			root, parent := token.Name.Local, ""
			if len(s.stack) != 0 {
				root, parent = s.stack[0], s.stack[len(s.stack)-1]
			}
			s.fileReference(root, parent, token.Name.Local, attr.Name.Local, attr.Value)
			if s.objects != nil && editorObjectReference(attr.Name.Local) {
				id := attr.Value
				if key := editorResourceID(id); key != "" {
					id = key
				}
				s.objects[id] = true
			}
			if attr.Name.Local == "Font" {
				usage = s.refs.fontUsage(attr.Value)
				if token.Name.Local != "TextObject" && token.Name.Local != "Text" {
					usage.unsafe = true
				}
			}
			if token.Name.Local == "TextCode" && attr.Name.Local == "Index" && attr.Value != "" && usage != nil {
				if err := usage.index(s.ctx, attr.Value); err != nil {
					s.safe = false
					return false
				}
			}
		}
		s.stack = append(s.stack, token.Name.Local)
		s.fonts = append(s.fonts, usage)
		s.text.Reset()
		s.capture = editorResourceReferenceKind(token.Name.Local, s.resource) != editorReferenceNone || usage != nil && (token.Name.Local == "TextCode" || token.Name.Local == "Glyphs")
		if len(s.stack) >= 2 && packageReference(s.stack[0], s.stack[len(s.stack)-2], token.Name.Local, "") != packageReferenceNone {
			s.capture = true
		}
	case xml.CharData:
		if len(s.stack) == 1 && (s.stack[0] == "RichText" || s.stack[0] == "Text") {
			s.richText.Write(token)
		}
		if len(s.stack) == 0 && len(bytes.Trim(token, " \t\r\n")) != 0 {
			s.safe = false
			return false
		}
		if s.capture {
			s.text.Write(token)
		}
	case xml.EndElement:
		ctDepth := s.customTagsDepth()
		if ctDepth >= 0 && len(s.stack) == ctDepth+3 && s.stack[ctDepth+1] == "CustomTag" && token.Name.Local == "SchemaLoc" {
			schema := strings.TrimSpace(s.text.String())
			if schema != "" {
				if !s.externalCustomTag {
					s.safe = false
					return false
				}
				s.currentCustomTag.SchemaLoc = schema
			}
		}
		if ctDepth >= 0 && len(s.stack) == ctDepth+3 && s.stack[ctDepth+1] == "CustomTag" && token.Name.Local == "FileLoc" {
			if target := cleanPackagePath(resolveResourcePath(s.name, s.base, s.text.String())); target != "" {
				s.refs.addCustomTagFile(target, s.currentCustomTag)
			}
		}
		if ctDepth >= 0 && len(s.stack) == ctDepth+2 && token.Name.Local == "CustomTag" {
			s.currentCustomTag = CustomTag{}
			s.externalCustomTag = false
		}
		if len(s.stack) == 1 && s.stack[0] == "RichText" && !richTextMetadataFontSafe(s.richText.String()) {
			s.safe = false
			return false
		}
		if len(s.stack) == 1 && s.stack[0] == "Text" && !textMetadataFontSafe(s.richText.String()) {
			s.safe = false
			return false
		}
		if len(s.stack) == 0 || s.stack[len(s.stack)-1] != token.Name.Local {
			s.safe = false
			return false
		}
		s.refs.reference(s.name, s.base, token.Name.Local, s.text.String(), s.resource)
		if len(s.stack) >= 2 {
			root := s.stack[0]
			if ctDepth >= 0 && len(s.stack) == ctDepth+3 {
				root = "CustomTags"
			}
			s.fileReference(root, s.stack[len(s.stack)-2], token.Name.Local, "", s.text.String())
		}
		if len(s.stack) == 3 && s.stack[0] == "DocVersion" && s.stack[1] == "FileList" && token.Name.Local == "File" {
			if name := editorResourceLocation(s.name, "", s.text.String()); name != "" {
				if s.refs.versions == nil {
					s.refs.versions = make(map[string]bool)
				}
				s.refs.versions[name] = true
			}
		}
		if s.objects != nil && (token.Name.Local == "Thumbnail" || token.Name.Local == "Substitution") {
			if id := editorResourceID(s.text.String()); id != "" {
				s.objects[id] = true
			}
		}
		if token.Name.Local == "Font" && strings.TrimSpace(s.text.String()) != "" {
			s.refs.fontUsage(s.text.String()).unsafe = true
		}
		if usage := s.fonts[len(s.fonts)-1]; usage != nil {
			if err := usage.text(s.ctx, token.Name.Local, s.text.String()); err != nil {
				s.safe = false
				return false
			}
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

// fileReference 按节点位置记录结构文件与原始数据，版本清单不改变文件用途
// 入参: root 根节点, parent 父节点, name 节点名称, attribute 属性名称, value 引用路径
func (s *editorReferenceScan) fileReference(root, parent, name, attribute, value string) {
	if root == "DocVersion" && parent == "FileList" && name == "File" || root == "CustomTags" && parent == "CustomTag" && name == "FileLoc" || strings.TrimSpace(value) == "" {
		return
	}
	kind := packageReference(root, parent, name, attribute)
	if kind == packageReferenceNone {
		return
	}
	if s.refs.roles == nil {
		s.refs.roles = make(map[string]int)
	}
	s.refs.roles[resolveResourcePath(s.name, s.base, value)] |= kind
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

// merge 合并本次编码已确认的引用，不共享字体用字的可变集合
// 入参: other 独立页面引用
func (r *editorResourceRefs) merge(other *editorResourceRefs) {
	maps.Copy(r.ids, other.ids)
	maps.Copy(r.files, other.files)
	if len(other.roles) != 0 {
		if r.roles == nil {
			r.roles = make(map[string]int)
		}
		for name, role := range other.roles {
			r.roles[name] |= role
		}
	}
	if len(other.documents) != 0 {
		if r.documents == nil {
			r.documents = make(map[string]bool)
		}
		maps.Copy(r.documents, other.documents)
	}
	if len(other.versions) != 0 {
		if r.versions == nil {
			r.versions = make(map[string]bool)
		}
		maps.Copy(r.versions, other.versions)
	}
	for id, usage := range other.fonts {
		r.fontUsage(id).merge(usage, false)
	}
	for target, tags := range other.customTagFiles {
		for _, tag := range tags {
			r.addCustomTagFile(target, tag)
		}
	}
	r.fontUnsafe = r.fontUnsafe || other.fontUnsafe
}

// referenceToken 记录自产OFD事件，编码会替换的字符使本页回到完整扫描
// 入参: token 已写入的XML事件
func (x *ofdXML) referenceToken(token xml.Token) {
	if x.references == nil || x.err != nil {
		return
	}
	s := x.references
	s.ctx = x.ctx
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
		x.checkContext()
	}
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
