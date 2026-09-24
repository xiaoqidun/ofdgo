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
	"fmt"
	"reflect"
)

// sourceInfoXML 更新公开元数据，保留原创建程序和未识别字段
// 返回: []byte 根索引XML, error 错误信息
func (e *Editor) sourceInfoXML() ([]byte, error) {
	data, err := e.source.reader.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	body := root.child("DocBody")
	if body == nil {
		return nil, fmt.Errorf("document has no DocBody")
	}
	info := body.child("DocInfo")
	if info == nil {
		return nil, fmt.Errorf("document has no DocInfo")
	}
	before, after := e.source.info, e.Info
	var values [][2]string
	for _, field := range [][3]string{
		{"DocID", before.DocID, after.DocID}, {"Title", before.Title, after.Title}, {"Author", before.Author, after.Author},
		{"Subject", before.Subject, after.Subject}, {"Abstract", before.Abstract, after.Abstract},
		{"CreationDate", before.CreationDate, after.CreationDate}, {"ModDate", before.ModDate, after.ModDate},
	} {
		if field[1] != field[2] {
			values = append(values, [2]string{field[0], field[2]})
		}
	}
	data = editorXMLSetText(data, info, values)
	if !reflect.DeepEqual(before.CustomDatas, after.CustomDatas) {
		root, err = parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		return editorCustomDatas(data, root.child("DocBody").child("DocInfo"), after.CustomDatas)
	}
	return data, nil
}

// editorCustomDatas 更新自定义字段，保留容器扩展与同名字段的未知属性和子节点
// 入参: data 原XML, info 文档信息节点, values 新字段
// 返回: []byte 修改后的XML, error 编码错误
func editorCustomDatas(data []byte, info *editorXML, values *CustomDatas) ([]byte, error) {
	container := info.child("CustomDatas")
	var items []CustomData
	if values != nil {
		items = values.CustomData
	}
	byName := make(map[string][]*editorXML)
	var nodes []*editorXML
	if container != nil {
		for _, node := range container.children {
			if node.name.Local == "CustomData" && (node.name.Space == ofdNamespace || node.name.Space == container.name.Space) {
				nodes = append(nodes, node)
				byName[node.attr("Name")] = append(byName[node.attr("Name")], node)
			}
		}
	}
	var encoded [][]byte
	for _, item := range items {
		matches := byName[item.Name]
		if len(matches) == 0 {
			entry, err := xml.Marshal(struct {
				XMLName xml.Name
				CustomData
			}{xml.Name{Space: ofdNamespace, Local: "CustomData"}, item})
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, entry)
			continue
		}
		node := matches[0]
		byName[item.Name] = matches[1:]
		var original CustomData
		if err := xml.Unmarshal(data[node.start:node.end], &original); err != nil {
			return nil, err
		}
		if original.Value == item.Value {
			encoded = append(encoded, data[node.start:node.end])
			continue
		}
		var content bytes.Buffer
		if err := xml.EscapeText(&content, []byte(item.Value)); err != nil {
			return nil, err
		}
		for _, child := range node.children {
			content.Write(data[child.start:child.end])
		}
		patch := editorXMLContent(data, node, content.Bytes())
		entry := append(bytes.Clone(data[node.start:patch.start]), patch.data...)
		entry = append(entry, data[patch.end:node.end]...)
		encoded = append(encoded, entry)
	}
	if container == nil {
		if len(encoded) == 0 {
			return data, nil
		}
		content := []byte("<ofd:CustomDatas xmlns:ofd=\"" + ofdNamespace + "\">")
		content = append(content, bytes.Join(encoded, nil)...)
		content = append(content, []byte("</ofd:CustomDatas>")...)
		if info.open == info.end {
			return editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, info, content)}), nil
		}
		return editorPatchXML(data, []editorXMLPatch{{info.close, info.close, content}}), nil
	}
	var patches []editorXMLPatch
	for i, node := range nodes {
		var entry []byte
		if i < len(encoded) {
			entry = encoded[i]
		}
		patches = append(patches, editorXMLPatch{node.start, node.end, entry})
	}
	if len(encoded) > len(nodes) {
		added := bytes.Join(encoded[len(nodes):], nil)
		if container.open == container.end {
			patches = append(patches, editorXMLContent(data, container, added))
		} else {
			patches = append(patches, editorXMLPatch{container.close, container.close, added})
		}
	}
	return editorPatchXML(data, patches), nil
}
