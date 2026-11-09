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
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strings"
)

// editorXML 保存XML节点的原文位置，不重写未修改的内容
type editorXML struct {
	name                    xml.Name
	attrs                   []xml.Attr
	start, open, close, end int
	children                []*editorXML
	parent                  *editorXML
}

// editorXMLPatch 替换原文中的连续区间
type editorXMLPatch struct {
	start, end int
	data       []byte
}

// editorXMLReader 在内存XML上直接读取字节，按块检查长词元的取消信号
type editorXMLReader struct {
	data   []byte
	offset int
	ctx    context.Context
}

// ReadByte 读取XML词元字节，每4096字节检查取消状态
// 返回: byte 当前字节, error 取消或读取错误
func (r *editorXMLReader) ReadByte() (byte, error) {
	if r.offset&4095 == 0 {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	b := r.data[r.offset]
	r.offset++
	return b, nil
}

// Read 读取XML原文字节并检查取消状态
// 入参: p 接收缓冲区
// 返回: int 已读字节数, error 取消或读取错误
func (r *editorXMLReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

// editorXMLPrefix 跳过UTF-8字节序标记和XML空白，探测后保留首个内容字节
// 入参: input 带缓冲的输入流，读取错误及取消信号由底层传递
// 返回: bool 是否以XML标记起始, error 读取错误
func editorXMLPrefix(input *bufio.Reader) (bool, error) {
	header, err := input.Peek(3)
	if err != nil && err != io.EOF {
		return false, err
	}
	if bytes.Equal(header, []byte{0xef, 0xbb, 0xbf}) {
		input.Discard(3)
	}
	for {
		if _, err := input.Peek(1); err != nil {
			if err == io.EOF {
				return false, nil
			}
			return false, err
		}
		chunk, _ := input.Peek(input.Buffered())
		count := 0
		for count < len(chunk) {
			switch chunk[count] {
			case ' ', '\t', '\r', '\n':
				count++
			default:
				input.Discard(count)
				return chunk[count] == '<', nil
			}
		}
		input.Discard(count)
	}
}

// parseEditorXML 解析节点位置，保留命名空间、注释和原始格式
// 入参: data XML原文
// 返回: *editorXML 根节点, error 错误信息
func parseEditorXML(data []byte) (*editorXML, error) {
	return parseEditorXMLContext(nil, data)
}

// parseEditorXMLContext 解析XML节点位置并响应取消
// 入参: ctx 取消上下文，可为nil, data XML原文
// 返回: *editorXML 根节点, error 解析或取消错误
func parseEditorXMLContext(ctx context.Context, data []byte) (*editorXML, error) {
	var input io.Reader = bytes.NewReader(data)
	if ctx != nil {
		input = &editorXMLReader{data: data, ctx: ctx}
	}
	d := xml.NewDecoder(input)
	var root *editorXML
	var stack []*editorXML
	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
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

// child 查找OFD直接子节点
// 入参: name 节点名称
// 返回: *editorXML 节点，不存在时为nil
func (n *editorXML) child(name string) *editorXML {
	return n.childAt(name, 0)
}

// childAt 查找指定序号的OFD直接子节点
// 入参: name 节点名称, index 从0开始的节点序号
// 返回: *editorXML 节点，不存在时为nil
func (n *editorXML) childAt(name string, index int) *editorXML {
	for _, child := range n.children {
		if child.matchesOFD(name) {
			if index == 0 {
				return child
			}
			index--
		}
	}
	return nil
}

// attr 获取无命名空间的属性值
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

// hasAttr 判断属性是否存在，区分空值与省略
// 入参: name 属性名称
// 返回: bool 是否存在
func (n *editorXML) hasAttr(name string) bool {
	return slices.ContainsFunc(n.attrs, func(attr xml.Attr) bool { return attr.Name.Space == "" && attr.Name.Local == name })
}

// editorPatchXML 按位置应用互不重叠的替换
// 入参: data 原文, patches 修改区间
// 返回: []byte 修改后的XML
func editorPatchXML(data []byte, patches []editorXMLPatch) []byte {
	slices.SortStableFunc(patches, func(a, b editorXMLPatch) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})
	size := len(data)
	for _, patch := range patches {
		size += len(patch.data) - (patch.end - patch.start)
	}
	var result bytes.Buffer
	result.Grow(size)
	position := 0
	for _, patch := range patches {
		result.Write(data[position:patch.start])
		result.Write(patch.data)
		position = patch.end
	}
	result.Write(data[position:])
	return result.Bytes()
}

// editorXMLContent 替换节点内部内容，兼容自闭合节点
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

// editorXMLText 编码带独立命名空间的文本节点
// 入参: name 节点名称, value 文本
// 返回: []byte XML片段
func editorXMLText(name, value string) []byte {
	return (&editorXML{name: xml.Name{Space: ofdNamespace2016}}).textXML(name, value)
}

// editorXMLSetText 更新或追加直接子节点，保留其他节点
// 入参: data 原文, node 父节点, values 需要修改的字段
// 返回: []byte 修改后的XML, error 文本编码错误
func editorXMLSetText(data []byte, node *editorXML, values [][2]string) ([]byte, error) {
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
			encoded = node.textXML(value[0], value[1])
		}
		if child != nil {
			if value[1] == "" {
				patches = append(patches, editorXMLPatch{child.start, child.end, nil})
			} else {
				encoded, err := editorXMLTextValue(data, child, editorImportText(data, child), value[1])
				if err != nil {
					return nil, err
				}
				patches = append(patches, editorXMLPatch{child.start, child.end, encoded})
			}
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
	return editorPatchXML(data, patches), nil
}

// editorXMLTextValue 修改字段文本，保留命名空间、属性、扩展子节点和注释
// 入参: data 原XML, node 字段节点, before 原值, after 新值
// 返回: []byte 字段XML, error 编码错误
func editorXMLTextValue(data []byte, node *editorXML, before, after string) ([]byte, error) {
	if before == after {
		return data[node.start:node.end], nil
	}
	textCode := node.matchesOFD("TextCode")
	if textCode {
		after = textCodeXMLValue(after)
	}
	var content bytes.Buffer
	if err := xml.EscapeText(&content, []byte(after)); err != nil {
		return nil, err
	}
	if textCode && bytes.IndexByte(content.Bytes(), ' ') >= 0 {
		escaped := bytes.ReplaceAll(content.Bytes(), []byte(" "), []byte("&#x20;"))
		content.Reset()
		content.Write(escaped)
	}
	return editorXMLDirectText(data, node, content.Bytes())
}

// editorXMLDirectText 替换直接文本，保留子节点、注释和处理指令
// 入参: data 原XML, node 目标节点, text 已转义的文本
// 返回: []byte 节点片段, error XML解析错误
func editorXMLDirectText(data []byte, node *editorXML, text []byte) ([]byte, error) {
	var content bytes.Buffer
	content.Write(text)
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

// editorXMLObjects 递归登记图层和页块内的OFD对象位置，不进入外部扩展
// 入参: container 图层或页块, nodes 标识与节点映射
// 返回: error 错误信息
func editorXMLObjects(container *editorXML, nodes map[string]*editorXML) error {
	for _, node := range container.children {
		if !ofdXMLChild(container.name, node.name) {
			continue
		}
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

// editorXMLAttributes 判断节点的命名空间与属性是否在编辑器支持范围内
// 入参: node 节点, allowed 允许的属性名称
// 返回: bool 是否支持
func editorXMLAttributes(node *editorXML, allowed string) bool {
	if classifyOFDNamespace(node.name.Space) == ofdXMLUnknown {
		return false
	}
	for _, attr := range node.attrs {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" && attr.Name.Space == "" {
			continue
		}
		if textMetadataAttribute(node.name, attr) {
			continue
		}
		if attr.Name.Space != "" || !slices.Contains(strings.Fields(allowed), attr.Name.Local) {
			return false
		}
	}
	return true
}

// editorXMLSupported 判断原文是否仅包含创建器能够保留的对象字段
// 入参: node 对象或子节点
// 返回: bool 是否支持完整编辑
func editorXMLSupported(node *editorXML) bool {
	var allowed, children string
	switch node.name.Local {
	case "TextObject", "Text":
		allowed, children = "ID Boundary CTM DrawParam Font Size HScale Weight ReadDirection CharDirection LineWidth MiterLimit Join Cap DashPattern DashOffset Italic Visible Stroke Fill Alpha", "Clips FillColor StrokeColor CGTransform TextCode"
	case "CGTransform":
		allowed, children = "CodePosition CodeCount GlyphCount", "Glyphs"
	case "Glyphs":
	case "PathObject", "Path":
		allowed, children = "Boundary CTM DrawParam LineWidth MiterLimit Join Cap Rule DashPattern DashOffset Visible Stroke Fill Alpha", "StrokeColor FillColor AbbreviatedData"
		if node.name.Local == "PathObject" {
			allowed += " ID"
			children += " Clips"
		}
	case "ImageObject":
		allowed, children = "ID Boundary CTM ResourceID Substitution ImageMask Visible Alpha", "Clips"
	case "TextCode":
		allowed = "X Y DeltaX DeltaY"
	case "FillColor", "StrokeColor":
		allowed = "Value Alpha ColorSpace Index"
	case "Clips":
		allowed, children = "TransFlag", "Clip"
	case "Clip":
		children = "Area"
	case "Area":
		allowed, children = "CTM DrawParam", "Path Text"
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

// editorXMLTransformable 判断对象是否支持保留原文的几何操作
// 入参: node 原始对象节点
// 返回: bool 是否支持
func editorXMLTransformable(node *editorXML) bool {
	var attrs string
	switch node.name.Local {
	case "TextObject":
		attrs = "ID Boundary CTM DrawParam Font Size HScale VScale Weight ReadDirection CharDirection LineWidth MiterLimit Join Cap DashPattern DashOffset Italic Decoration Visible Stroke Fill Alpha"
	case "PathObject":
		attrs = "ID Boundary CTM DrawParam LineWidth MiterLimit Join Cap Rule DashPattern DashOffset Visible Stroke Fill Alpha"
	case "ImageObject":
		attrs = "ID Boundary CTM ResourceID Substitution ImageMask Visible Alpha"
	case "CompositeObject", "CompositeGraphicUnit":
		attrs = "ID Boundary CTM ResourceID DrawParam Visible Alpha"
	default:
		return false
	}
	return editorXMLAttributes(node, attrs)
}

// editorXMLContainersSupported 判断父级容器是否仅含可保留的标识和样式
// 入参: node 对象节点
// 返回: bool 是否支持容器内操作
func editorXMLContainersSupported(node *editorXML) bool {
	for parent := node.parent; parent.name.Local != "Content"; parent = parent.parent {
		allowed := "ID"
		if parent.name.Local == "Layer" {
			allowed += " Type DrawParam"
		}
		if !editorXMLAttributes(parent, allowed) {
			return false
		}
	}
	return true
}

// editorXMLCopyable 判断内部标识能否按标准重映射，扩展节点的标识不作推断
// 入参: node 原始对象节点
// 返回: bool 是否支持保真复制
func editorXMLCopyable(node *editorXML) bool {
	if node.attr("ID") != "" && (editorResourceID(node.attr("ID")) == "" || classifyOFDNamespace(node.name.Space) == ofdXMLUnknown) {
		return false
	}
	for _, child := range node.children {
		if !editorXMLCopyable(child) {
			return false
		}
	}
	return true
}

// editorXMLObject 修改对象发生变化的属性与内容，保留原文中的显式默认值
// 有效样式快照在修改后展开原绘制参数，避免已清除的属性重新继承
// 入参: data 页面原文, node 原对象节点, before 原对象, after 新对象
// 返回: []byte 对象XML, error 错误信息
func editorXMLObject(data []byte, node *editorXML, before, after GraphicObject) ([]byte, error) {
	if before.Type != after.Type {
		encoded, err := editorObjectXML(after)
		if err != nil {
			return nil, err
		}
		return editorXMLGenerated(encoded, node.name.Space)
	}
	if node.attr("DrawParam") != "" && (before.Type == "TextObject" && before.TextObject.DrawParam == "" || before.Type == "PathObject" && before.PathObject.DrawParam == "") {
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

// editorXMLMerge 按标准编码的差异更新原节点，递归保留未变化的显式默认值与命名空间
// 入参: data 原文, node 原节点, oldXML 修改前编码, newXML 修改后编码
// 返回: []byte 更新节点, error 错误信息
func editorXMLMerge(data []byte, node *editorXML, oldXML, newXML []byte) ([]byte, error) {
	return editorXMLMergeNamespace(data, node, oldXML, newXML, node.name.Space)
}

// editorXMLMergeNamespace 合并节点差异，并向已换算的裁剪子树传递目标命名空间
// 入参: data 原文, node 原节点, oldXML 修改前编码, newXML 修改后编码, namespace 目标命名空间
// 返回: []byte 更新节点, error 解析错误
func editorXMLMergeNamespace(data []byte, node *editorXML, oldXML, newXML []byte, namespace string) ([]byte, error) {
	legacyClip := node.name.Local == "Clips" && (node.hasAttr("TransFlag") || classifyOFDNamespace(node.name.Space) == ofdXMLLegacy)
	if legacyClip {
		namespace = ofdNamespace2016
		var clips Clips
		if err := xml.Unmarshal(data[node.start:node.end], &clips); err != nil {
			return nil, err
		}
		clips.TransFlag = nil
		var err error
		oldXML, err = encodeOFDXML(func(x *ofdXML) { x.clips(&clips, "") })
		if err != nil {
			return nil, err
		}
	}
	oldNode, err := parseEditorXML(oldXML)
	if err != nil {
		return nil, err
	}
	newNode, err := parseEditorXML(newXML)
	if err != nil {
		return nil, err
	}
	changes := make(map[string]string)
	textValue := func(n *editorXML) string {
		for _, attr := range n.attrs {
			if attr.Name.Space == textNamespace && attr.Name.Local == "Text" {
				return attr.Value
			}
		}
		return ""
	}
	textChanged := textValue(oldNode) != textValue(newNode)
	if legacyClip {
		changes["TransFlag"] = ""
	}
	for _, attr := range append(oldNode.attrs, newNode.attrs...) {
		if attr.Name.Space == "" && (oldNode.attr(attr.Name.Local) != newNode.attr(attr.Name.Local) || oldNode.hasAttr(attr.Name.Local) != newNode.hasAttr(attr.Name.Local)) {
			changes[attr.Name.Local] = newNode.attr(attr.Name.Local)
		}
	}
	contentChanged := !bytes.Equal(oldXML[oldNode.open:oldNode.close], newXML[newNode.open:newNode.close])
	if len(changes) == 0 && !textChanged && !contentChanged && namespace == node.name.Space {
		return bytes.Clone(data[node.start:node.end]), nil
	}
	d := xml.NewDecoder(bytes.NewReader(data[node.start:node.open]))
	token, err := d.RawToken()
	if err != nil {
		return nil, err
	}
	start := token.(xml.StartElement)
	if textChanged {
		value, found := textValue(newNode), false
		attrs := start.Attr[:0]
		for i, attr := range start.Attr {
			resolved := node.attrs[i]
			if resolved.Name.Space == textNamespace && resolved.Name.Local == "Text" {
				found = true
				if value != "" {
					attr.Value = value
					attrs = append(attrs, attr)
				}
				continue
			}
			attrs = append(attrs, attr)
		}
		start.Attr = attrs
		if !found && value != "" {
			prefix := editorXMLNamespacePrefix(node, "ofdgo")
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Space: "xmlns", Local: prefix}, Value: textNamespace}, xml.Attr{Name: xml.Name{Space: prefix, Local: "Text"}, Value: value})
		}
	}
	if legacyClip || namespace != node.name.Space {
		start.Name.Space = editorXMLNamespacePrefix(node, "ofd")
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Space: "xmlns", Local: start.Name.Space}, Value: namespace})
	}
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
				if !newNode.hasAttr(attr.Name.Local) {
					continue
				}
			}
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
		if value, ok := changes[attr.Name.Local]; ok {
			result.WriteString(" " + attr.Name.Local + "=\"")
			_ = xml.EscapeText(&result, []byte(value))
			result.WriteByte('"')
		}
	}
	result.WriteByte('>')
	if contentChanged || namespace != node.name.Space {
		matching := len(node.children) > 0 && len(node.children) == len(oldNode.children) && len(node.children) == len(newNode.children)
		if matching {
			for i, child := range node.children {
				if !child.matchesOFD(oldNode.children[i].name.Local) || child.name.Local != newNode.children[i].name.Local {
					matching = false
					break
				}
			}
		}
		if matching {
			position := node.open
			for i, child := range node.children {
				before, after := oldNode.children[i], newNode.children[i]
				childNamespace := child.name.Space
				if childNamespace == node.name.Space {
					childNamespace = namespace
				}
				updated, err := editorXMLMergeNamespace(data, child, oldXML[before.start:before.end], newXML[after.start:after.end], childNamespace)
				if err != nil {
					return nil, err
				}
				result.Write(data[position:child.start])
				result.Write(updated)
				position = child.end
			}
			result.Write(data[position:node.close])
		} else {
			content, err := editorXMLMergeContent(data, node, oldXML, oldNode, newXML, newNode, namespace)
			if err != nil {
				return nil, err
			}
			result.Write(content)
		}
	} else if node.open != node.end {
		result.Write(data[node.open:node.close])
	}
	result.WriteString("</" + name + ">")
	return result.Bytes(), nil
}

// editorXMLMergeContent 按字段合并子节点，保留未修改字段、扩展节点和注释
// 入参: data 原文数据, node 原文节点, oldXML 修改前数据, oldNode 修改前节点, newXML 修改后数据, newNode 修改后节点, namespace 目标命名空间
// 返回: []byte 子节点内容, error 错误信息
func editorXMLMergeContent(data []byte, node *editorXML, oldXML []byte, oldNode *editorXML, newXML []byte, newNode *editorXML, namespace string) ([]byte, error) {
	if len(oldNode.children) == 0 && len(newNode.children) == 0 {
		updated, err := editorXMLTextValue(data, node, editorImportText(oldXML, oldNode), editorImportText(newXML, newNode))
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(updated)
		if err != nil {
			return nil, err
		}
		return updated[root.open:root.close], nil
	}
	groups := func(root *editorXML, encoded bool) map[string][]*editorXML {
		result := make(map[string][]*editorXML)
		for _, child := range root.children {
			if encoded || child.matchesOFD(child.name.Local) {
				result[child.name.Local] = append(result[child.name.Local], child)
			}
		}
		return result
	}
	original, before, after := groups(node, false), groups(oldNode, true), groups(newNode, true)
	var names []string
	for _, child := range append(slices.Clone(newNode.children), oldNode.children...) {
		if !slices.Contains(names, child.name.Local) {
			names = append(names, child.name.Local)
		}
	}
	var patches []editorXMLPatch
	for _, name := range names {
		old, next, source := before[name], after[name], original[name]
		var oldData, newData []byte
		for _, child := range old {
			oldData = append(oldData, oldXML[child.start:child.end]...)
		}
		for _, child := range next {
			newData = append(newData, newXML[child.start:child.end]...)
		}
		if bytes.Equal(oldData, newData) && namespace == node.name.Space {
			continue
		}
		if len(source) > 0 && len(source) == len(old) {
			for i, child := range source {
				var encoded []byte
				if i < len(next) {
					var err error
					childNamespace := child.name.Space
					if childNamespace == node.name.Space {
						childNamespace = namespace
					}
					encoded, err = editorXMLMergeNamespace(data, child, oldXML[old[i].start:old[i].end], newXML[next[i].start:next[i].end], childNamespace)
					if err != nil {
						return nil, err
					}
				}
				patches = append(patches, editorXMLPatch{child.start - node.open, child.end - node.open, encoded})
			}
			if len(next) > len(source) {
				var added []byte
				for _, child := range next[len(source):] {
					fragment, err := editorXMLEncodedFragment(newXML, child, namespace)
					if err != nil {
						return nil, err
					}
					added = append(added, fragment...)
				}
				position := source[len(source)-1].end - node.open
				patches = append(patches, editorXMLPatch{position, position, added})
			}
			continue
		}
		newData = nil
		for _, child := range next {
			fragment, err := editorXMLEncodedFragment(newXML, child, namespace)
			if err != nil {
				return nil, err
			}
			newData = append(newData, fragment...)
		}
		if len(source) > 0 {
			for i, child := range source {
				var encoded []byte
				if i == 0 {
					encoded = newData
				}
				patches = append(patches, editorXMLPatch{child.start - node.open, child.end - node.open, encoded})
			}
		} else if len(next) > 0 {
			position := node.close
			for _, child := range newNode.children {
				if child.start > next[0].start && len(original[child.name.Local]) > 0 {
					position = original[child.name.Local][0].start
					break
				}
			}
			patches = append(patches, editorXMLPatch{position - node.open, position - node.open, newData})
		}
	}
	return editorPatchXML(data[node.open:node.close], patches), nil
}

// editorXMLEncodedFragment 补齐自产片段的继承声明，再按目标命名空间编码
// 入参: data 标准编码内容, node 待插入节点, namespace 目标命名空间
// 返回: []byte 独立片段, error 解析错误
func editorXMLEncodedFragment(data []byte, node *editorXML, namespace string) ([]byte, error) {
	fragment, err := editorXMLStandalone(data[node.start:node.end], node)
	if err != nil {
		return nil, err
	}
	if node.name.Space == "ofd" {
		root, err := parseEditorXML(fragment)
		if err != nil {
			return nil, err
		}
		position := root.open - 1
		if fragment[position-1] == '/' {
			position--
		}
		fragment = editorPatchXML(fragment, []editorXMLPatch{{position, position, []byte(" xmlns:ofd=\"" + ofdNamespace2016 + "\"")}})
	}
	return editorXMLGenerated(fragment, namespace)
}

// editorXMLAttributePatch 生成标准属性的起始标签补丁，不复制子树
// 入参: data 原文, node 节点, name 属性名, value 新值
// 返回: editorXMLPatch 标签补丁, error 编码错误
func editorXMLAttributePatch(data []byte, node *editorXML, name, value string) (editorXMLPatch, error) {
	return editorXMLAttributeChange(data, node, name, &value)
}

// editorXMLAttributeChange 更新或删除标准属性，仅重写起始标签并保留命名空间前缀
// 入参: data 原文, node 节点, name 属性名, value 新值，nil表示删除
// 返回: editorXMLPatch 标签补丁, error 编码错误
func editorXMLAttributeChange(data []byte, node *editorXML, name string, value *string) (editorXMLPatch, error) {
	patch := editorXMLPatch{start: node.start, end: node.open, data: data[node.start:node.open]}
	if value == nil && !node.hasAttr(name) || value != nil && node.hasAttr(name) && node.attr(name) == *value {
		return patch, nil
	}
	d := xml.NewDecoder(bytes.NewReader(patch.data))
	token, err := d.RawToken()
	if err != nil {
		return editorXMLPatch{}, err
	}
	start := token.(xml.StartElement)
	found := false
	for i, attr := range start.Attr {
		if attr.Name.Space == "" && attr.Name.Local == name {
			if value == nil {
				start.Attr = append(start.Attr[:i], start.Attr[i+1:]...)
			} else {
				start.Attr[i].Value = *value
			}
			found = true
			break
		}
	}
	if !found && value != nil {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: name}, Value: *value})
	}
	if start.Name.Space != "" {
		start.Name.Local = start.Name.Space + ":" + start.Name.Local
		start.Name.Space = ""
	}
	for i := range start.Attr {
		if attr := &start.Attr[i]; attr.Name.Space != "" {
			attr.Name.Local = attr.Name.Space + ":" + attr.Name.Local
			attr.Name.Space = ""
		}
	}
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	if err := encoder.EncodeToken(start); err != nil {
		return editorXMLPatch{}, err
	}
	if err := encoder.Flush(); err != nil {
		return editorXMLPatch{}, err
	}
	patch.data = output.Bytes()
	if node.open == node.end {
		patch.data = append(patch.data[:len(patch.data)-1], '/', '>')
	}
	return patch, nil
}

// editorXMLAttribute 修改单个标准属性，保留节点其余原文
// 入参: data 原文, node 节点, name 属性名, value 新值
// 返回: []byte 修改后的节点, error 错误信息
func editorXMLAttribute(data []byte, node *editorXML, name, value string) ([]byte, error) {
	before, err := editorXMLContainer(node.name.Local, ofdAttrs{{Name: xml.Name{Local: name}, Value: node.attr(name)}}, nil)
	if err != nil {
		return nil, err
	}
	after, err := editorXMLContainer(node.name.Local, ofdAttrs{{Name: xml.Name{Local: name}, Value: value}}, nil)
	if err != nil {
		return nil, err
	}
	return editorXMLMerge(data, node, before, after)
}

// editorXMLContainer 用标准编码器封装已编码的子节点
// 入参: name 节点名称, attrs 属性, content 子节点XML
// 返回: []byte XML片段, error 错误信息
func editorXMLContainer(name string, attrs ofdAttrs, content []byte) ([]byte, error) {
	return (&editorXML{name: xml.Name{Space: ofdNamespace2016}}).containerXML(name, attrs, content)
}

// editorXMLStandalone 补齐原对象继承的命名空间，使跨页复制不依赖原父节点
// 入参: data 对象XML, source 原节点
// 返回: []byte 独立XML片段, error 错误信息
func editorXMLStandalone(data []byte, source *editorXML) ([]byte, error) {
	return editorXMLStandaloneContext(nil, data, source)
}

// editorXMLStandaloneContext 补齐继承的命名空间，解析及生成片段期间响应取消
// 入参: ctx 取消上下文，可为nil, data 对象XML, source 原节点
// 返回: []byte 独立XML片段, error 解析或取消错误
func editorXMLStandaloneContext(ctx context.Context, data []byte, source *editorXML) ([]byte, error) {
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	return editorXMLStandaloneRoot(ctx, data, source, root)
}

// editorXMLFragment 从已解析原文提取独立片段，复用根属性和开标签位置
// 入参: data 完整原文, source 原文节点
// 返回: []byte 独立片段, error 命名空间补齐错误
func editorXMLFragment(data []byte, source *editorXML) ([]byte, error) {
	root := *source
	root.open -= source.start
	return editorXMLStandaloneRoot(nil, data[source.start:source.end], source, &root)
}

// editorXMLStandaloneRoot 为已解析片段补齐祖先命名空间，不重复解析子节点
// 入参: ctx 取消上下文，可为nil, data 片段原文, source 原节点, root 片段根节点
// 返回: []byte 独立片段, error 取消错误
func editorXMLStandaloneRoot(ctx context.Context, data []byte, source, root *editorXML) ([]byte, error) {
	namespaces := make(map[string]string)
	for node := source.parent; node != nil; node = node.parent {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
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
	if _, exists := namespaces["xmlns"]; !exists {
		namespaces["xmlns"] = ""
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
	result := editorPatchXML(data, []editorXMLPatch{{position, position, added.Bytes()}})
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}
