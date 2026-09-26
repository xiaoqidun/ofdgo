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
	"io"
	"reflect"
)

// sourceInfoXML 更新文档描述字段，保留未识别内容
// 返回: []byte 根索引XML, error 错误信息
func (e *Editor) sourceInfoXML() ([]byte, error) {
	data, err := e.source.reader.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	data, info, err := editorDocInfoXML(data, e.source.fallbackDocID)
	if err != nil {
		return nil, err
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
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		return editorCustomDatas(data, root.child("DocBody").child("DocInfo"), after.CustomDatas)
	}
	return data, nil
}

// editorCreatorXML 更新首份文档的制作软件，移除原软件版本，保留其余根索引内容
// 入参: data 当前根索引XML, docID 缺少文档描述时使用的标识
// 返回: []byte 修改后的XML, error 解析错误
func editorCreatorXML(data []byte, docID string) ([]byte, error) {
	data, info, err := editorDocInfoXML(data, docID)
	if err != nil {
		return nil, err
	}
	return editorXMLSetText(data, info, [][2]string{{"Creator", ofdCreator}, {"CreatorVersion", ""}}), nil
}

// editorDocInfoXML 获取文档描述，修改缺少描述的源文件时补建标准容器及标识
// 入参: data 根索引XML, docID 缺少文档描述时使用的标识
// 返回: []byte 根索引XML, *editorXML 描述节点, error 解析错误
func editorDocInfoXML(data []byte, docID string) ([]byte, *editorXML, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, nil, err
	}
	body := root.child("DocBody")
	if body == nil {
		return nil, nil, fmt.Errorf("document has no DocBody")
	}
	if info := body.child("DocInfo"); info != nil {
		return data, info, nil
	}
	info, err := editorXMLContainer("DocInfo", nil, editorXMLText("DocID", docID))
	if err != nil {
		return nil, nil, err
	}
	data = editorPatchXML(data, []editorXMLPatch{{body.open, body.open, info}})
	root, err = parseEditorXML(data)
	if err != nil {
		return nil, nil, err
	}
	return data, root.child("DocBody").child("DocInfo"), nil
}

// editorCustomDatas 更新自定义字段，保留原字段及容器的扩展内容
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
	used := make(map[*editorXML]bool)
	for _, item := range items {
		if item.sourceIndex > 0 && item.sourceIndex <= len(nodes) {
			used[nodes[item.sourceIndex-1]] = true
		}
	}
	for _, item := range items {
		var node *editorXML
		if item.sourceIndex > 0 && item.sourceIndex <= len(nodes) {
			node = nodes[item.sourceIndex-1]
		} else {
			for _, candidate := range byName[item.Name] {
				if !used[candidate] {
					node = candidate
					used[node] = true
					break
				}
			}
		}
		if node == nil {
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
		var original CustomData
		if err := xml.Unmarshal(data[node.start:node.end], &original); err != nil {
			return nil, err
		}
		if original.Name != item.Name {
			entry, err := editorXMLAttribute(data, node, "Name", item.Name)
			if err != nil {
				return nil, err
			}
			renamed, err := parseEditorXML(entry)
			if err != nil {
				return nil, err
			}
			updated, err := editorCustomDataValue(entry, renamed, original.Value, item.Value)
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, updated)
			continue
		}
		entry, err := editorCustomDataValue(data, node, original.Value, item.Value)
		if err != nil {
			return nil, err
		}
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

// editorCustomDataValue 修改字段文本，保留扩展子节点和注释
// 入参: data 原XML, node 字段节点, before 原值, after 新值
// 返回: []byte 字段XML, error 编码错误
func editorCustomDataValue(data []byte, node *editorXML, before, after string) ([]byte, error) {
	if before == after {
		return data[node.start:node.end], nil
	}
	var content bytes.Buffer
	if err := xml.EscapeText(&content, []byte(after)); err != nil {
		return nil, err
	}
	if node.open < node.close {
		inner := data[node.open:node.close]
		decoder := xml.NewDecoder(bytes.NewReader(inner))
		depth := 0
		for {
			start := decoder.InputOffset()
			token, err := decoder.RawToken()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			_, text := token.(xml.CharData)
			if !text || depth != 0 {
				content.Write(inner[start:decoder.InputOffset()])
			}
			switch token.(type) {
			case xml.StartElement:
				depth++
			case xml.EndElement:
				depth--
			}
		}
	}
	patch := editorXMLContent(data, node, content.Bytes())
	entry := append(bytes.Clone(data[node.start:patch.start]), patch.data...)
	return append(entry, data[patch.end:node.end]...), nil
}
