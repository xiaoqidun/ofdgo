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
	"slices"
)

// editorUpdateInfoXML 更新指定文档的描述和自定义字段，保留未修改内容
// 入参: data 根索引XML, docID 缺少描述时使用的标识, index 文档索引, before 原信息, after 新信息
// 返回: []byte 修改后的XML, error 解析错误
func editorUpdateInfoXML(data []byte, docID string, index int, before, after DocInfo) ([]byte, error) {
	data, info, err := editorDocInfoXML(data, docID, index)
	if err != nil {
		return nil, err
	}
	var values [][2]string
	for _, field := range [][3]string{
		{"DocID", before.DocID, after.DocID}, {"Title", before.Title, after.Title}, {"Author", before.Author, after.Author},
		{"Subject", before.Subject, after.Subject}, {"Abstract", before.Abstract, after.Abstract},
		{"CreationDate", before.CreationDate, after.CreationDate}, {"ModDate", before.ModDate, after.ModDate},
		{"DocUsage", before.DocUsage, after.DocUsage}, {"Cover", before.Cover, after.Cover},
		{"Creator", before.Creator, after.Creator}, {"CreatorVersion", before.CreatorVersion, after.CreatorVersion},
	} {
		if field[1] != field[2] {
			values = append(values, [2]string{field[0], field[2]})
		}
	}
	data, err = editorXMLSetText(data, info, values)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(before.Keywords, after.Keywords) {
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		data, err = editorKeywords(data, root.childAt("DocBody", index).child("DocInfo"), after.Keywords)
		if err != nil {
			return nil, err
		}
	}
	if !reflect.DeepEqual(before.CustomDatas, after.CustomDatas) {
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		return editorCustomDatas(data, root.childAt("DocBody", index).child("DocInfo"), after.CustomDatas)
	}
	return data, nil
}

// editorKeywords 更新有序关键词，保留已有词条的属性及未修改内容
// 入参: data 原XML, info 文档信息节点, values 关键词
// 返回: []byte 修改后的XML, error 编码错误
func editorKeywords(data []byte, info *editorXML, values []string) ([]byte, error) {
	container := info.child("Keywords")
	if len(values) == 0 {
		if container == nil {
			return data, nil
		}
		return editorPatchXML(data, []editorXMLPatch{{container.start, container.end, nil}}), nil
	}
	if container == nil {
		var content []byte
		for _, value := range values {
			content = append(content, info.textXML("Keyword", value)...)
		}
		encoded, err := info.containerXML("Keywords", nil, content)
		if err != nil {
			return nil, err
		}
		if info.open == info.end {
			return editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, info, encoded)}), nil
		}
		position := info.close
		for _, child := range info.children {
			if child.matchesOFD("Creator") || child.matchesOFD("CreatorVersion") || child.matchesOFD("CustomDatas") {
				position = child.start
				break
			}
		}
		return editorPatchXML(data, []editorXMLPatch{{position, position, encoded}}), nil
	}
	var patches []editorXMLPatch
	index := 0
	for _, child := range container.children {
		if !child.matchesOFD("Keyword") {
			continue
		}
		var encoded []byte
		if index < len(values) {
			var err error
			encoded, err = editorXMLTextValue(data, child, editorImportText(data, child), values[index])
			if err != nil {
				return nil, err
			}
		}
		patches = append(patches, editorXMLPatch{child.start, child.end, encoded})
		index++
	}
	var added []byte
	for ; index < len(values); index++ {
		added = append(added, container.textXML("Keyword", values[index])...)
	}
	if len(added) != 0 {
		if container.open == container.end {
			patches = append(patches, editorXMLContent(data, container, added))
		} else {
			patches = append(patches, editorXMLPatch{container.close, container.close, added})
		}
	}
	return editorPatchXML(data, patches), nil
}

// editorDocInfoXML 获取文档描述，修改缺少描述的源文件时补建标准容器及标识
// 入参: data 根索引XML, docID 缺少文档描述时使用的标识, index 文档索引
// 返回: []byte 根索引XML, *editorXML 描述节点, error 解析错误
func editorDocInfoXML(data []byte, docID string, index int) ([]byte, *editorXML, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, nil, err
	}
	body := root.childAt("DocBody", index)
	if body == nil {
		return nil, nil, fmt.Errorf("document has no DocBody")
	}
	if info := body.child("DocInfo"); info != nil {
		return data, info, nil
	}
	info, err := body.containerXML("DocInfo", nil, body.textXML("DocID", docID))
	if err != nil {
		return nil, nil, err
	}
	if body.open == body.end {
		data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, body, info)})
	} else {
		data = editorPatchXML(data, []editorXMLPatch{{body.open, body.open, info}})
	}
	root, err = parseEditorXML(data)
	if err != nil {
		return nil, nil, err
	}
	return data, root.childAt("DocBody", index).child("DocInfo"), nil
}

// editorCustomDatas 更新自定义字段，保留原字段及容器的扩展内容
// 入参: data 原XML, info 文档信息节点, values 新字段
// 返回: []byte 修改后的XML, error 编码错误
func editorCustomDatas(data []byte, info *editorXML, values *CustomDatas) ([]byte, error) {
	container := info.child("CustomDatas")
	ctx := info
	if container != nil {
		ctx = container
	}
	var items []CustomData
	if values != nil {
		items = values.CustomData
	}
	byName := make(map[string][]*editorXML)
	var nodes []*editorXML
	if container != nil {
		for _, node := range container.children {
			if node.matchesOFD("CustomData") {
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
			var value bytes.Buffer
			_ = xml.EscapeText(&value, []byte(item.Value))
			entry, err := ctx.containerXML("CustomData", ofdAttrs{{Name: xml.Name{Local: "Name"}, Value: item.Name}}, value.Bytes())
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
			updated, err := editorXMLTextValue(entry, renamed, original.Value, item.Value)
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, updated)
			continue
		}
		entry, err := editorXMLTextValue(data, node, original.Value, item.Value)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, entry)
	}
	if container == nil {
		if len(encoded) == 0 {
			return data, nil
		}
		content, err := info.containerXML("CustomDatas", nil, bytes.Join(encoded, nil))
		if err != nil {
			return nil, err
		}
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

// sourceInfoXML 更新文档描述字段，保留未识别内容
// 返回: []byte 根索引XML, error 错误信息
func (e *Editor) sourceInfoXML() ([]byte, error) {
	data, err := e.source.reader.readFile("OFD.xml")
	if err != nil {
		return nil, err
	}
	return editorUpdateInfoXML(data, e.source.fallbackDocID, e.source.reader.documentIndex, e.source.info, e.Info)
}
