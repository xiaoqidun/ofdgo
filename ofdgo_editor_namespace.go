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
	"encoding/xml"
	"strconv"
)

// textXML 按所在节点的命名空间编码文本节点
// 入参: name 节点名称, value 文本
// 返回: []byte XML片段
func (n *editorXML) textXML(name, value string) []byte {
	var content bytes.Buffer
	_ = xml.EscapeText(&content, []byte(value))
	data, _ := n.containerXML(name, nil, content.Bytes())
	return data
}

// containerXML 按所在节点的命名空间封装子节点，不改写子节点原文
// 入参: name 节点名称, attrs 属性, content 子节点XML
// 返回: []byte XML片段, error 编码错误
func (n *editorXML) containerXML(name string, attrs ofdAttrs, content []byte) ([]byte, error) {
	var output bytes.Buffer
	if n.name.Space == "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: ""})
	} else {
		name = "ofd:" + name
		attrs.add("xmlns:ofd", n.name.Space)
	}
	value := struct {
		Content []byte `xml:",innerxml"`
	}{content}
	err := xml.NewEncoder(&output).EncodeElement(value, xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs})
	return output.Bytes(), err
}

// editorXMLGenerated 将自产标准片段用于指定命名空间，已标准化的旧版裁剪保持2016语义
// 入参: data 自产XML片段，不含待保留的源文件原文, namespace 目标命名空间
// 返回: []byte XML片段, error 解析错误
func editorXMLGenerated(data []byte, namespace string) ([]byte, error) {
	if namespace == ofdNamespace2016 || len(data) == 0 {
		return data, nil
	}
	header := []byte(`<context xmlns:ofd="` + ofdNamespace2016 + `">`)
	wrapped := append(bytes.Clone(header), bytes.TrimPrefix(data, []byte(xml.Header))...)
	end := len(wrapped)
	wrapped = append(wrapped, []byte(`</context>`)...)
	root, err := parseEditorXML(wrapped)
	if err != nil {
		return nil, err
	}
	prefix := editorXMLNamespacePrefix(root)
	var patches []editorXMLPatch
	var visit func(*editorXML, bool) error
	visit = func(node *editorXML, declare bool) error {
		if node.name.Space != ofdNamespace2016 || namespace == ofdNamespaceLegacy && node.name.Local == "Clips" {
			value, err := editorXMLStandalone(wrapped[node.start:node.end], node)
			if err != nil {
				return err
			}
			patches = append(patches, editorXMLPatch{node.start, node.end, value})
			return nil
		}
		decoder := xml.NewDecoder(bytes.NewReader(wrapped[node.start:node.open]))
		token, err := decoder.RawToken()
		if err != nil {
			return err
		}
		start := token.(xml.StartElement)
		name := node.name.Local
		if namespace != "" {
			name = prefix + ":" + name
		}
		var output bytes.Buffer
		output.WriteString("<" + name)
		ofdDeclared := false
		for _, attr := range start.Attr {
			if attr.Name.Space == "xmlns" && attr.Name.Local == "ofd" {
				ofdDeclared = true
			}
			if namespace == "" && attr.Name.Space == "" && attr.Name.Local == "xmlns" {
				continue
			}
			key := attr.Name.Local
			if attr.Name.Space != "" {
				key = attr.Name.Space + ":" + key
			}
			output.WriteString(" " + key + `="`)
			_ = xml.EscapeText(&output, []byte(attr.Value))
			output.WriteByte('"')
		}
		if declare && !ofdDeclared {
			output.WriteString(` xmlns:ofd="` + ofdNamespace2016 + `"`)
		}
		if declare || namespace == "" {
			key := "xmlns"
			if namespace != "" {
				key = "xmlns:" + prefix
			}
			output.WriteString(" " + key + `="`)
			_ = xml.EscapeText(&output, []byte(namespace))
			output.WriteByte('"')
		}
		if node.open == node.end {
			output.WriteString("/>")
		} else {
			output.WriteByte('>')
			patches = append(patches, editorXMLPatch{node.close, node.end, []byte("</" + name + ">")})
		}
		patches = append(patches, editorXMLPatch{node.start, node.open, output.Bytes()})
		for _, child := range node.children {
			if err := visit(child, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, node := range root.children {
		if err := visit(node, true); err != nil {
			return nil, err
		}
	}
	patches = append(patches, editorXMLPatch{0, len(header), nil}, editorXMLPatch{end, len(wrapped), nil})
	return editorPatchXML(wrapped, patches), nil
}

// editorXMLNamespacePrefix 选择未被当前作用域及子树占用的命名空间前缀
// 入参: node 节点
// 返回: string 可用前缀
func editorXMLNamespacePrefix(node *editorXML) string {
	used := make(map[string]bool)
	collect := func(current *editorXML) {
		for _, attr := range current.attrs {
			if attr.Name.Space == "xmlns" {
				used[attr.Name.Local] = true
			}
		}
	}
	for parent := node.parent; parent != nil; parent = parent.parent {
		collect(parent)
	}
	var visit func(*editorXML)
	visit = func(current *editorXML) {
		collect(current)
		for _, child := range current.children {
			visit(child)
		}
	}
	visit(node)
	prefix := "ofd"
	for index := 1; used[prefix]; index++ {
		prefix = "ofd" + strconv.Itoa(index)
	}
	return prefix
}
