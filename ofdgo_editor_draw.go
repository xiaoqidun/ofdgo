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
	"encoding/xml"
	"fmt"
)

// drawParamXML 读取绘制参数原文，保留复杂颜色、扩展属性及命名空间
// 入参: id 绘制参数标识
// 返回: []byte 独立参数片段, error 错误信息
func (e *Editor) drawParamXML(id string) ([]byte, error) {
	var data []byte
	for _, resource := range e.resources {
		if resource.draw != nil && resource.draw.ID == id {
			data = resource.data
			break
		}
	}
	if data == nil && e.source != nil {
		var err error
		data, err = e.source.reader.readFile(e.source.reader.resourceFiles[id])
		if err != nil {
			return nil, err
		}
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	if params := root.child("DrawParams"); params != nil {
		for _, node := range params.children {
			if node.name.Local == "DrawParam" && node.attr("ID") == id && (node.name.Space == "" || node.name.Space == ofdNamespace || node.name.Space == "http://www.ofdspec.org") {
				return editorXMLStandalone(data[node.start:node.end], node)
			}
		}
	}
	return nil, fmt.Errorf("draw parameter %q not found", id)
}

// copyDrawParam 将原绘制参数继承链接到独立基准，复用相同副本且不改写原资源
// 入参: id 源参数标识, base 独立基准标识, visited 当前继承链
// 返回: string 新继承链末端标识, error 错误信息
func (e *Editor) copyDrawParam(id, base string, visited map[string]bool) (string, error) {
	if id == "" || id == base {
		return base, nil
	}
	if visited[id] {
		return "", fmt.Errorf("cyclic draw parameter %q", id)
	}
	visited[id] = true
	data, err := e.drawParamXML(id)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return "", err
	}
	if root.attr("Relative") == base {
		return id, nil
	}
	base, err = e.copyDrawParam(root.attr("Relative"), base, visited)
	if err != nil {
		return "", err
	}
	for _, resource := range e.resources {
		if resource.drawSource == id && resource.draw.Relative == base {
			return resource.draw.ID, nil
		}
	}
	if !editorXMLCopyable(root) {
		return "", fmt.Errorf("draw parameter contains unsupported identifiers")
	}
	before, err := editorXMLContainer("DrawParam", ofdAttrs{{Name: xml.Name{Local: "Relative"}, Value: root.attr("Relative")}}, nil)
	if err != nil {
		return "", err
	}
	after, err := editorXMLContainer("DrawParam", ofdAttrs{{Name: xml.Name{Local: "Relative"}, Value: base}}, nil)
	if err != nil {
		return "", err
	}
	data, err = editorXMLMerge(data, root, before, after)
	if err != nil {
		return "", err
	}
	root, err = parseEditorXML(data)
	if err != nil {
		return "", err
	}
	ids := make(map[string]string)
	if err := e.collectCompositeIDs(root, ids); err != nil {
		return "", err
	}
	data, err = editorXMLRemapIDs(data, root, ids)
	if err != nil {
		return "", err
	}
	var draw DrawParam
	if err := xml.Unmarshal(data, &draw); err != nil {
		return "", err
	}
	data, err = editorXMLContainer("DrawParams", nil, data)
	if err != nil {
		return "", err
	}
	data, err = editorXMLContainer("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}}, data)
	if err != nil {
		return "", err
	}
	refs, err := editorResourceReferences(data)
	if err != nil {
		return "", err
	}
	e.resources = append(e.resources, editorResource{name: e.resourceDirectory() + "/DrawParam_" + draw.ID + ".xml", data: data, draw: &draw, drawSource: id, references: refs})
	return draw.ID, nil
}
