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
	"slices"
	"strings"
)

// editorXML 保存XML节点的原文位置，不重写未修改的内容。
type editorXML struct {
	name                    xml.Name
	attrs                   []xml.Attr
	start, open, close, end int
	children                []*editorXML
	parent                  *editorXML
}

// editorXMLPatch 替换原文中的连续区间。
type editorXMLPatch struct {
	start, end int
	data       []byte
}

// parseEditorXML 解析节点位置，保留命名空间、注释和原始格式。
// 入参: data XML原文
// 返回: *editorXML 根节点, error 错误信息
func parseEditorXML(data []byte) (*editorXML, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var root *editorXML
	var stack []*editorXML
	for {
		start := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			node := &editorXML{name: token.Name, attrs: token.Attr, start: start, open: int(d.InputOffset())}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("multiple XML roots")
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				node.parent = parent
				parent.children = append(parent.children, node)
			}
			stack = append(stack, node)
		case xml.EndElement:
			node := stack[len(stack)-1]
			node.close, node.end = start, int(d.InputOffset())
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty XML document")
	}
	return root, nil
}

// child 查找OFD直接子节点。
// 入参: name 节点名称
// 返回: *editorXML 节点，不存在时为nil
func (n *editorXML) child(name string) *editorXML {
	for _, child := range n.children {
		if child.name.Local == name && (child.name.Space == n.name.Space || child.name.Space == ofdNamespace || child.name.Space == "") {
			return child
		}
	}
	return nil
}

// attr 获取无命名空间的属性值。
// 入参: name 属性名称
// 返回: string 属性值
func (n *editorXML) attr(name string) string {
	for _, attr := range n.attrs {
		if attr.Name.Space == "" && attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

// editorPatchXML 按位置应用互不重叠的替换。
// 入参: data 原文, patches 修改区间
// 返回: []byte 修改后的XML
func editorPatchXML(data []byte, patches []editorXMLPatch) []byte {
	slices.SortStableFunc(patches, func(a, b editorXMLPatch) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})
	var result bytes.Buffer
	position := 0
	for _, patch := range patches {
		result.Write(data[position:patch.start])
		result.Write(patch.data)
		position = patch.end
	}
	result.Write(data[position:])
	return result.Bytes()
}

// editorXMLContent 替换节点内部内容，兼容自闭合节点。
// 入参: data 原文, node 节点, content 新内容
// 返回: editorXMLPatch 修改区间
func editorXMLContent(data []byte, node *editorXML, content []byte) editorXMLPatch {
	if node.open != node.end {
		return editorXMLPatch{node.open, node.close, content}
	}
	header := data[node.start:node.open]
	end := bytes.IndexAny(header[1:], " \t\r\n/>") + 1
	name := header[1:end]
	replacement := append(bytes.Clone(header[:len(header)-2]), '>')
	replacement = append(replacement, content...)
	replacement = append(replacement, []byte("</"+string(name)+">")...)
	return editorXMLPatch{node.start, node.end, replacement}
}

// editorXMLText 编码带独立命名空间的文本节点。
// 入参: name 节点名称, value 文本
// 返回: []byte XML片段
func editorXMLText(name, value string) []byte {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return []byte("<ofd:" + name + " xmlns:ofd=\"" + ofdNamespace + "\">" + escaped.String() + "</ofd:" + name + ">")
}

// editorXMLSetText 更新或追加直接子节点，保留其他节点。
// 入参: data 原文, node 父节点, values 需要修改的字段
// 返回: []byte 修改后的XML
func editorXMLSetText(data []byte, node *editorXML, values [][2]string) []byte {
	var patches []editorXMLPatch
	var added []byte
	order := []string{"PhysicalBox", "ApplicationBox", "ContentBox", "BleedBox"}
	if node.name.Local == "DocInfo" {
		order = []string{"DocID", "Title", "Author", "Subject", "Abstract", "CreationDate", "ModDate", "DocUsage", "Cover", "Keywords", "Creator", "CreatorVersion", "CustomDatas"}
	}
	for _, value := range values {
		child := node.child(value[0])
		var encoded []byte
		if value[1] != "" {
			encoded = editorXMLText(value[0], value[1])
		}
		if child != nil {
			patches = append(patches, editorXMLPatch{child.start, child.end, encoded})
		} else if node.open == node.end {
			added = append(added, encoded...)
		} else if len(encoded) != 0 {
			position := node.close
			for _, next := range node.children {
				if slices.Index(order, next.name.Local) > slices.Index(order, value[0]) {
					position = next.start
					break
				}
			}
			patches = append(patches, editorXMLPatch{position, position, encoded})
		}
	}
	if len(added) != 0 {
		patches = append(patches, editorXMLContent(data, node, added))
	}
	return editorPatchXML(data, patches)
}

// editorXMLObjects 递归登记图层和页块内的对象位置，保留原容器层级。
// 入参: container 图层或页块, nodes 标识与节点映射
// 返回: error 错误信息
func editorXMLObjects(container *editorXML, nodes map[string]*editorXML) error {
	for _, node := range container.children {
		if id := node.attr("ID"); id != "" {
			if nodes[id] != nil {
				return fmt.Errorf("duplicate object ID %q", id)
			}
			nodes[id] = node
		}
		if node.name.Local == "PageBlock" {
			if err := editorXMLObjects(node, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

// editorXMLAttributes 判断节点的命名空间与属性是否在编辑器支持范围内。
// 入参: node 节点, allowed 允许的属性名称
// 返回: bool 是否支持
func editorXMLAttributes(node *editorXML, allowed string) bool {
	if node.name.Space != "" && node.name.Space != ofdNamespace && node.name.Space != "http://www.ofdspec.org" {
		return false
	}
	for _, attr := range node.attrs {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" && attr.Name.Space == "" {
			continue
		}
		if attr.Name.Space != "" || !slices.Contains(strings.Fields(allowed), attr.Name.Local) {
			return false
		}
	}
	return true
}

// editorXMLSupported 判断原文是否仅包含创建器能够保留的对象字段。
// 入参: node 对象或子节点
// 返回: bool 是否支持完整编辑
func editorXMLSupported(node *editorXML) bool {
	var allowed, children string
	switch node.name.Local {
	case "TextObject":
		allowed, children = "ID Boundary CTM DrawParam Font Size HScale Weight ReadDirection CharDirection LineWidth MiterLimit Join Italic Visible Stroke Fill Alpha", "FillColor StrokeColor TextCode"
	case "PathObject", "Path":
		allowed, children = "Boundary CTM LineWidth MiterLimit Join Cap Rule DashPattern DashOffset Visible Stroke Fill Alpha", "StrokeColor FillColor AbbreviatedData"
		if node.name.Local == "PathObject" {
			allowed += " ID DrawParam"
		}
	case "ImageObject":
		allowed, children = "ID Boundary CTM ResourceID ImageMask Visible Alpha", "Clips"
	case "TextCode":
		allowed = "X Y DeltaX DeltaY"
	case "FillColor", "StrokeColor":
		allowed = "Value Alpha"
	case "Clips":
		allowed, children = "TransFlag", "Clip"
	case "Clip":
		children = "Area"
	case "Area":
		allowed, children = "CTM", "Path"
	case "AbbreviatedData":
	default:
		return false
	}
	if !editorXMLAttributes(node, allowed) {
		return false
	}
	for _, child := range node.children {
		if !slices.Contains(strings.Fields(children), child.name.Local) || !editorXMLSupported(child) {
			return false
		}
	}
	return true
}

// editorXMLObject 修改对象发生变化的属性与内容，保留原文中的显式默认值。
// 有效样式快照在修改后展开原绘制参数，避免已清除的属性重新继承。
// 入参: data 页面原文, node 原对象节点, before 原对象, after 新对象
// 返回: []byte 对象XML, error 错误信息
func editorXMLObject(data []byte, node *editorXML, before, after GraphicObject) ([]byte, error) {
	if before.Type != after.Type {
		return editorObjectXML(after)
	}
	if node.attr("DrawParam") != "" && before.TextObject.DrawParam == "" && before.PathObject.DrawParam == "" {
		before = GraphicObject{Type: before.Type}
		var err error
		switch before.Type {
		case "TextObject":
			err = xml.Unmarshal(data[node.start:node.end], &before.TextObject)
		case "PathObject":
			err = xml.Unmarshal(data[node.start:node.end], &before.PathObject)
		}
		if err != nil {
			return nil, err
		}
	}
	oldXML, err := editorObjectXML(before)
	if err != nil {
		return nil, err
	}
	newXML, err := editorObjectXML(after)
	if err != nil {
		return nil, err
	}
	return editorXMLMerge(data, node, oldXML, newXML)
}

// editorXMLMerge 按标准编码的差异更新原节点，递归保留未变化的显式默认值与命名空间。
// 入参: data 原文, node 原节点, oldXML 修改前编码, newXML 修改后编码
// 返回: []byte 更新节点, error 错误信息
func editorXMLMerge(data []byte, node *editorXML, oldXML, newXML []byte) ([]byte, error) {
	oldNode, err := parseEditorXML(oldXML)
	if err != nil {
		return nil, err
	}
	newNode, err := parseEditorXML(newXML)
	if err != nil {
		return nil, err
	}
	changes := make(map[string]string)
	for _, attr := range append(oldNode.attrs, newNode.attrs...) {
		if attr.Name.Space == "" && oldNode.attr(attr.Name.Local) != newNode.attr(attr.Name.Local) {
			changes[attr.Name.Local] = newNode.attr(attr.Name.Local)
		}
	}
	contentChanged := !bytes.Equal(oldXML[oldNode.open:oldNode.close], newXML[newNode.open:newNode.close])
	if len(changes) == 0 && !contentChanged {
		return bytes.Clone(data[node.start:node.end]), nil
	}
	d := xml.NewDecoder(bytes.NewReader(data[node.start:node.open]))
	token, err := d.RawToken()
	if err != nil {
		return nil, err
	}
	start := token.(xml.StartElement)
	var result bytes.Buffer
	name := start.Name.Local
	if start.Name.Space != "" {
		name = start.Name.Space + ":" + name
	}
	result.WriteString("<" + name)
	for _, attr := range start.Attr {
		if attr.Name.Space == "" {
			if value, ok := changes[attr.Name.Local]; ok {
				attr.Value = value
				delete(changes, attr.Name.Local)
				if value == "" {
					continue
				}
			}
		}
		if contentChanged && attr.Name.Space == "xmlns" && attr.Name.Local == "ofd" {
			continue
		}
		key := attr.Name.Local
		if attr.Name.Space != "" {
			key = attr.Name.Space + ":" + key
		}
		result.WriteString(" " + key + "=\"")
		_ = xml.EscapeText(&result, []byte(attr.Value))
		result.WriteByte('"')
	}
	for _, attr := range newNode.attrs {
		if value, ok := changes[attr.Name.Local]; ok && value != "" {
			result.WriteString(" " + attr.Name.Local + "=\"")
			_ = xml.EscapeText(&result, []byte(value))
			result.WriteByte('"')
		}
	}
	if contentChanged {
		result.WriteString(" xmlns:ofd=\"" + ofdNamespace + "\"")
	}
	result.WriteByte('>')
	if contentChanged {
		matching := len(node.children) > 0 && len(node.children) == len(oldNode.children) && len(node.children) == len(newNode.children)
		if matching {
			for i, child := range node.children {
				if child.name.Local != oldNode.children[i].name.Local || child.name.Local != newNode.children[i].name.Local {
					matching = false
					break
				}
			}
		}
		if matching {
			position := node.open
			for i, child := range node.children {
				before, after := oldNode.children[i], newNode.children[i]
				updated, err := editorXMLMerge(data, child, oldXML[before.start:before.end], newXML[after.start:after.end])
				if err != nil {
					return nil, err
				}
				result.Write(data[position:child.start])
				result.Write(updated)
				position = child.end
			}
			result.Write(data[position:node.close])
		} else {
			result.Write(newXML[newNode.open:newNode.close])
		}
	} else if node.open != node.end {
		result.Write(data[node.open:node.close])
	}
	result.WriteString("</" + name + ">")
	return result.Bytes(), nil
}

// editorXMLContainer 用标准编码器封装已编码的子节点。
// 入参: name 节点名称, attrs 属性, content 子节点XML
// 返回: []byte XML片段, error 错误信息
func editorXMLContainer(name string, attrs ofdAttrs, content []byte) ([]byte, error) {
	var output bytes.Buffer
	attrs.add("xmlns:ofd", ofdNamespace)
	value := struct {
		Content []byte `xml:",innerxml"`
	}{content}
	err := xml.NewEncoder(&output).EncodeElement(value, xml.StartElement{Name: xml.Name{Local: "ofd:" + name}, Attr: attrs})
	return output.Bytes(), err
}

// editorXMLStandalone 补齐原对象继承的命名空间，使跨页复制不依赖原父节点。
// 入参: data 对象XML, source 原节点
// 返回: []byte 独立XML片段, error 错误信息
func editorXMLStandalone(data []byte, source *editorXML) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	namespaces := make(map[string]string)
	for node := source.parent; node != nil; node = node.parent {
		for _, attr := range node.attrs {
			key := ""
			if attr.Name.Space == "xmlns" {
				key = "xmlns:" + attr.Name.Local
			}
			if attr.Name.Space == "" && attr.Name.Local == "xmlns" {
				key = "xmlns"
			}
			if key != "" {
				if _, exists := namespaces[key]; !exists {
					namespaces[key] = attr.Value
				}
			}
		}
	}
	for _, attr := range root.attrs {
		if attr.Name.Space == "xmlns" {
			delete(namespaces, "xmlns:"+attr.Name.Local)
		}
		if attr.Name.Space == "" && attr.Name.Local == "xmlns" {
			delete(namespaces, "xmlns")
		}
	}
	var added bytes.Buffer
	keys := make([]string, 0, len(namespaces))
	for key := range namespaces {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		added.WriteString(" " + key + "=\"")
		_ = xml.EscapeText(&added, []byte(namespaces[key]))
		added.WriteByte('"')
	}
	position := root.open - 1
	if data[position-1] == '/' {
		position--
	}
	return editorPatchXML(data, []editorXMLPatch{{position, position, added.Bytes()}}), nil
}
