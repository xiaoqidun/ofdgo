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
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// structureTypeOfficial 公文标引类型标识
const structureTypeOfficial = "od"

// structureTypeInvoice 电子发票标引类型标识
const structureTypeInvoice = "fp"

// structureNamespaceEGov2013 2013版电子公文标引命名空间
const structureNamespaceEGov2013 = "http://www.nits.org.cn/eGovDoc/2013"

// structureNamespaceEInvoice2016 2016版电子发票标引命名空间
const structureNamespaceEInvoice2016 = "http://www.edrm.org.cn/schema/e-invoice/2016"

// Structure 保存自定义标引入口及结构树，单个文件解析失败时通过Error报告，不影响其他标引
// CanBind表示已识别标引引用规则，包括公文、富文本及发票标引，具体节点和目标仍由绑定接口校验
// Internal标记本库内部编辑标引，不删除或重排入口；界面跳过此项时必须保留原始数组索引
// 此标记不限制通用低级编辑接口，也不表示内部元数据内容已通过有效性校验
type Structure struct {
	TypeID    string         `json:"typeId,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	FileLoc   string         `json:"fileLoc"`
	CanBind   bool           `json:"canBind,omitempty"`
	Internal  bool           `json:"internal,omitempty"`
	Root      *StructureNode `json:"root,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// StructureNode 保存标引节点的名称、属性、直接文本和有序子节点，不改变原始XML
// Text按文档顺序汇总直接文本并保留空白，不包含子节点文本
// References仅解释已识别标引中的ObjectRef；未知扩展保留结构，不推定其引用语义
type StructureNode struct {
	Name       string               `json:"name"`
	Namespace  string               `json:"namespace,omitempty"`
	Text       string               `json:"text,omitempty"`
	Attributes []StructureAttribute `json:"attributes,omitempty"`
	Children   []StructureNode      `json:"children,omitempty"`
	References []StructureReference `json:"references,omitempty"`
}

// StructureAttribute 保存展开命名空间后的标引属性，不包含命名空间声明
type StructureAttribute struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Value     string `json:"value"`
}

// StructureReference 记录同一页上按标引顺序排列的图元标识，不将页面标识当作页码
type StructureReference struct {
	PageID    string   `json:"pageId"`
	ObjectIDs []string `json:"objectIds"`
}

// StructureTarget 保存单个标引图元的原文和可见范围，Page从1开始，Bounds单位为毫米
// Error说明引用无法定位的原因；空Bounds表示图元存在但没有可见范围
type StructureTarget struct {
	Page   int    `json:"page"`
	PageID string `json:"pageId"`
	ID     string `json:"id"`
	Text   string `json:"text,omitempty"`
	Bounds Box    `json:"bounds"`
	Error  string `json:"error,omitempty"`
}

// Structure 读取当前文档及版本的自定义标引，不扫描页面或访问外部Schema
// 返回: []Structure 标引结构, error 清单读取错误
func (r *Reader) Structure() ([]Structure, error) {
	return r.StructureContext(context.Background())
}

// StructureContext 读取标引并响应取消，每次返回独立结构，未知命名空间不影响正文阅读
// 入参: ctx 取消上下文
// 返回: []Structure 标引结构, error 清单读取或取消错误
func (r *Reader) StructureContext(ctx context.Context) ([]Structure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tags, err := r.CustomTagsContext(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Structure, len(tags))
	for i, tag := range tags {
		item, err := r.readStructureContext(ctx, tag)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			item.Error = err.Error()
		}
		result[i] = item
	}
	return result, nil
}

// StructureTargets 按标引顺序定位指定节点及其后代引用，每个目标页面只遍历一次
// 入参: index 标引清单索引, path 相对根节点的子节点索引，均从0开始
// 返回: []StructureTarget 原文和页面范围，缺失引用单独报告, error 解析或渲染错误
func (r *Renderer) StructureTargets(index int, path []int) ([]StructureTarget, error) {
	return r.StructureTargetsContext(context.Background(), index, path)
}

// StructureTargetsContext 仅读取所选标引，在当前文档和版本内定位目标，不搜索其他文档的同名图元
// 入参: ctx 取消上下文, index 标引清单索引, path 子节点索引路径
// 返回: []StructureTarget 有序目标, error 解析、渲染或取消错误
func (r *Renderer) StructureTargetsContext(ctx context.Context, index int, path []int) ([]StructureTarget, error) {
	tags, err := r.Reader.CustomTagsContext(ctx)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(tags) {
		return nil, fmt.Errorf("custom tag structure not found: %d", index)
	}
	structure, err := r.Reader.readStructureContext(ctx, tags[index])
	if err != nil {
		return nil, err
	}
	node := structure.Root
	for _, i := range path {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if i < 0 || i >= len(node.Children) {
			return nil, fmt.Errorf("custom tag node not found")
		}
		node = &node.Children[i]
	}
	var references []StructureReference
	var collect func(*StructureNode) error
	collect = func(node *StructureNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		references = append(references, node.References...)
		for i := range node.Children {
			if err := collect(&node.Children[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(node); err != nil {
		return nil, err
	}
	if len(references) == 0 {
		return nil, ctx.Err()
	}
	doc, err := r.Reader.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	pages := signaturePageNumbers(doc)
	var result []StructureTarget
	for _, ref := range references {
		page, _ := resourceValue(pages, ref.PageID)
		for _, id := range ref.ObjectIDs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result = append(result, StructureTarget{Page: page, PageID: ref.PageID, ID: id})
		}
	}
	byPage := make(map[int]map[string]*StructureTarget)
	for i := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target := &result[i]
		if target.Page == 0 {
			target.Error = "custom tag page not found"
			continue
		}
		if byPage[target.Page] == nil {
			byPage[target.Page] = make(map[string]*StructureTarget)
		}
		byPage[target.Page][editorResourceID(target.ID)] = target
		target.Error = "custom tag object not found"
	}
	view := *r
	view.RenderAnnotations, view.pageContext = true, ctx
	for page, targets := range byPage {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := r.Reader.PageContentByIndexContext(ctx, page-1)
		if err != nil {
			return nil, err
		}
		visitor := &structureVisitor{ctx: ctx, renderer: &view, targets: targets}
		if err := view.WalkPage(content, visitor); err != nil {
			return nil, err
		}
	}
	for i, target := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source := byPage[target.Page][editorResourceID(target.ID)]; source != nil {
			result[i] = *source
		}
	}
	return result, nil
}

// readStructureContext 读取单个标引文件，解析失败时保留入口信息及原始错误
// 入参: ctx 取消上下文, tag 标引入口
// 返回: Structure 标引结构, error 文件读取、解析或取消错误
func (r *Reader) readStructureContext(ctx context.Context, tag CustomTag) (Structure, error) {
	item := Structure{TypeID: tag.TypeID, Namespace: tag.NameSpace, FileLoc: tag.FileLoc}
	data, err := r.readFileViewContext(ctx, r.ResPath(tag.FileLoc))
	if err == nil {
		item.Root, err = parseStructure(ctx, data, tag)
	}
	if err != nil {
		return item, fmt.Errorf("read custom tag %s: %w", tag.FileLoc, err)
	}
	item.CanBind = structureOfficial(tag, item.Root.Namespace) || structureInvoiceReferences(item.Root)
	item.Internal = tag.TypeID == textTagType && tag.NameSpace == textNamespace && item.Root.Namespace == textNamespace && item.Root.Name == "Text"
	item.Internal = item.Internal || tag.TypeID == richTextTagType && tag.NameSpace == richTextNamespace && item.Root.Namespace == richTextNamespace && item.Root.Name == "RichText"
	item.Internal = item.Internal || tag.TypeID == invoiceTagType && tag.NameSpace == invoiceNamespace && item.Root.Namespace == invoiceNamespace && item.Root.Name == "Invoice"
	return item, nil
}

// parseStructure 解析有界XML结构，按GB/T33476.3的ObjectRef规则识别公文引用
// 入参: ctx 取消上下文, data 标引XML, tag 标引入口
// 返回: *StructureNode 根节点, error 解析、限额或取消错误
func parseStructure(ctx context.Context, data []byte, tag CustomTag) (*StructureNode, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	d := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	var root *StructureNode
	var stack []*StructureNode
	var text []*strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) >= 128 {
				return nil, fmt.Errorf("custom tag structure limit exceeded")
			}
			if len(stack) == len(text) {
				text = append(text, new(strings.Builder))
			}
			node := StructureNode{Name: token.Name.Local, Namespace: token.Name.Space}
			attributes := make(map[xml.Name]bool, len(token.Attr))
			for _, attr := range token.Attr {
				if attributes[attr.Name] {
					return nil, fmt.Errorf("duplicate custom tag attribute")
				}
				attributes[attr.Name] = true
				if attr.Name.Space != "xmlns" && !(attr.Name.Space == "" && attr.Name.Local == "xmlns") {
					node.Attributes = append(node.Attributes, StructureAttribute{Name: attr.Name.Local, Namespace: attr.Name.Space, Value: attr.Value})
				}
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("multiple custom tag roots")
				}
				root = &node
				stack = append(stack, root)
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
				stack = append(stack, &parent.Children[len(parent.Children)-1])
			}
		case xml.CharData:
			if len(stack) > 0 {
				text[len(stack)-1].Write(token)
			} else if strings.Trim(string(token), " \t\r\n") != "" {
				return nil, fmt.Errorf("text outside custom tag root")
			}
		case xml.EndElement:
			node := stack[len(stack)-1]
			builder := text[len(stack)-1]
			node.Text = builder.String()
			builder.Reset()
			official := structureOfficial(tag, root.Namespace)
			for _, parent := range stack {
				official = official && parent.Namespace == root.Namespace
			}
			if official && node.Name == "ObjectRef" && len(node.Children) == 0 {
				var page string
				for _, attr := range node.Attributes {
					if attr.Name == "PageRef" && attr.Namespace == "" {
						page = attr.Value
					}
				}
				ids := structureObjectIDs(node.Text)
				if editorResourceID(page) == "" || len(ids) == 0 {
					return nil, fmt.Errorf("custom tag ObjectRef requires PageRef and object identifiers")
				}
				for _, id := range ids {
					if editorResourceID(id) == "" {
						return nil, fmt.Errorf("invalid custom tag object identifier: %s", id)
					}
				}
				node.References = []StructureReference{{PageID: page, ObjectIDs: ids}}
			}
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty custom tag document")
	}
	if structureInvoiceReferences(root) {
		var bind func(*StructureNode)
		bind = func(node *StructureNode) {
			if node.Name == "ObjectRef" {
				for _, attr := range node.Attributes {
					if attr.Name == "PageRef" && attr.Namespace == "" {
						node.References = []StructureReference{{PageID: attr.Value, ObjectIDs: structureObjectIDs(node.Text)}}
					}
				}
			}
			for i := range node.Children {
				bind(&node.Children[i])
			}
		}
		bind(root)
	}
	return root, nil
}

// structureInvoiceReferences 仅识别已核实的root和InvoiceNo发票引用树，不扩展公文类型规则
// 入参: root 标引根节点
// 返回: bool 是否为纯对象引用发票
func structureInvoiceReferences(root *StructureNode) bool {
	if root == nil {
		return false
	}
	ns := root.Namespace
	var known bool
	switch ns {
	case ofdNamespace2016:
		if root.Name != "root" {
			return false
		}
		for _, child := range root.Children {
			known = known || child.Name == "InvoiceNo" && child.Namespace == ofdNamespace2016
		}
	case structureNamespaceEInvoice2016, InvoiceStandardEDRM2019:
		if root.Name != "eInvoice" && root.Name != "root" {
			return false
		}
		known = root.Name == "eInvoice"
		for _, child := range root.Children {
			known = known || child.Name == "InvoiceNo" && child.Namespace == ns
		}
	default:
		return false
	}
	refs := 0
	var check func(*StructureNode) bool
	check = func(node *StructureNode) bool {
		if node.Name == "ObjectRef" && (node.Namespace == ns || node.Namespace == ofdNamespace2016) {
			page := ""
			for _, attr := range node.Attributes {
				if attr.Name == "PageRef" && attr.Namespace == "" {
					page = attr.Value
				}
			}
			ids := structureObjectIDs(node.Text)
			if len(node.Children) != 0 || len(ids) == 0 || editorResourceID(page) == "" {
				return false
			}
			for _, id := range ids {
				if editorResourceID(id) == "" {
					return false
				}
			}
			refs++
			return true
		}
		if node.Namespace != ns {
			return false
		}
		if node.Name == "ObjectData" {
			return len(node.Children) == 0
		}
		if strings.TrimSpace(node.Text) != "" {
			return false
		}
		for i := range node.Children {
			if !check(&node.Children[i]) {
				return false
			}
		}
		return true
	}
	return known && check(root) && refs > 0
}

// structureReferenceTags 按声明及实际内容识别可维护引用的标引，不将未知解析失败当作公文
// 入参: ctx 取消上下文, tags 当前文档标引声明
// 返回: []bool 与声明顺序对应的引用能力, error 取消错误
func (r *Reader) structureReferenceTags(ctx context.Context, tags []CustomTag) ([]bool, error) {
	known := make([]bool, len(tags))
	for i, tag := range tags {
		known[i] = structureOfficial(tag, tag.NameSpace)
		if !known[i] {
			item, err := r.readStructureContext(ctx, tag)
			known[i] = err == nil && item.CanBind
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return known, nil
}

// structureOfficial 识别公文及库内标引的明确引用规则，不推断未知行业语义
// 入参: tag 标引入口, namespace 内容根节点命名空间
// 返回: bool 是否使用公文引用规则
func structureOfficial(tag CustomTag, namespace string) bool {
	if tag.TypeID == textTagType && tag.NameSpace == textNamespace && namespace == textNamespace {
		return true
	}
	if tag.TypeID == invoiceTagType && tag.NameSpace == invoiceNamespace && namespace == invoiceNamespace {
		return true
	}
	if tag.TypeID == richTextTagType && tag.NameSpace == richTextNamespace && namespace == richTextNamespace {
		return true
	}
	if tag.TypeID == pdfStructureTagType && tag.NameSpace == pdfStructureNamespace && namespace == pdfStructureNamespace {
		return true
	}
	if tag.TypeID == structureTypeOfficial {
		return strings.TrimSpace(tag.NameSpace) == "" || tag.NameSpace == namespace
	}
	if tag.NameSpace != namespace {
		return false
	}
	switch namespace {
	case structureNamespaceEGov2013:
		return true
	default:
		return false
	}
}

// structureObjectIDs 按公文标引规则拆分图元标识，保留顺序和重复项
// 入参: value ObjectRef文本
// 返回: []string 图元标识
func structureObjectIDs(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
}

// encodeStructureNode 编码有界标引子树，引用由ObjectRef节点表达，不重复编码派生References字段
// 入参: node 根节点
// 返回: []byte XML片段, error 名称、属性或编码错误
func encodeStructureNode(node StructureNode) ([]byte, error) {
	return encodeStructureNodeContext(context.Background(), node)
}

// encodeStructureNodeContext 编码标引子树并检查取消状态
// 入参: ctx 取消上下文, node 根节点
// 返回: []byte XML片段, error 名称、深度、编码或取消错误
func encodeStructureNodeContext(ctx context.Context, node StructureNode) ([]byte, error) {
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	var write func(StructureNode, int) error
	write = func(node StructureNode, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 128 {
			return fmt.Errorf("custom tag structure limit exceeded")
		}
		if err := validateStructureNode(node); err != nil {
			return err
		}
		start := xml.StartElement{Name: xml.Name{Space: node.Namespace, Local: node.Name}}
		switch node.Namespace {
		case "":
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: ""})
		case XMLNamespace:
			start.Name = xml.Name{Local: "xml:" + node.Name}
		}
		for _, attr := range node.Attributes {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Space: attr.Namespace, Local: attr.Name}, Value: attr.Value})
		}
		if err := encoder.EncodeToken(start); err != nil {
			return err
		}
		if err := encoder.EncodeToken(xml.CharData(node.Text)); err != nil {
			return err
		}
		for _, child := range node.Children {
			if err := write(child, depth+1); err != nil {
				return err
			}
		}
		return encoder.EncodeToken(start.End())
	}
	if err := write(node, 1); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// validateStructureNode 校验标引节点名称、属性和文本
// 入参: node 节点字段
// 返回: error 字段校验错误
func validateStructureNode(node StructureNode) error {
	if !ofdXMLIDValid(node.Name) || node.Name == "xmlns" {
		return fmt.Errorf("invalid custom tag node name: %s", node.Name)
	}
	if !structureXMLText(node.Text) || !structureXMLText(node.Namespace) || node.Namespace == xmlDeclarationNamespace {
		return fmt.Errorf("invalid custom tag text or namespace")
	}
	seen := make(map[xml.Name]bool)
	for _, attr := range node.Attributes {
		name := xml.Name{Space: attr.Namespace, Local: attr.Name}
		if !ofdXMLIDValid(attr.Name) || attr.Name == "xmlns" || attr.Namespace == xmlDeclarationNamespace || !structureXMLText(attr.Namespace) || !structureXMLText(attr.Value) || seen[name] {
			return fmt.Errorf("invalid or duplicate custom tag attribute: %s", attr.Name)
		}
		seen[name] = true
	}
	return nil
}

// structureXMLText 检查XML 1.0允许的字符，不以替换字符掩盖无效输入
// 入参: value 文本
// 返回: bool 是否可无损编码
func structureXMLText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r != '\t' && r != '\n' && r != '\r' && (r < 0x20 || r == 0xfffe || r == 0xffff) {
			return false
		}
	}
	return true
}

// updateStructureNodeXML 替换节点字段，保留子树继承的命名空间和非文本XML内容
// 入参: data 原XML, source 原节点, node 新字段
// 返回: []byte 独立节点片段, error 校验或编码错误
func updateStructureNodeXML(data []byte, source *editorXML, node StructureNode) ([]byte, error) {
	if err := validateStructureNode(node); err != nil {
		return nil, err
	}
	fragment, err := editorXMLStandalone(data[source.start:source.end], source)
	if err != nil {
		return nil, err
	}
	before, err := parseStructure(context.Background(), fragment, CustomTag{})
	if err != nil {
		return nil, err
	}
	if node.Name == before.Name && node.Namespace == before.Namespace && node.Text == before.Text && slices.Equal(node.Attributes, before.Attributes) {
		return data[source.start:source.end], nil
	}
	root, err := parseEditorXML(fragment)
	if err != nil {
		return nil, err
	}
	if before.Text != node.Text {
		var text bytes.Buffer
		if err := xml.EscapeText(&text, []byte(node.Text)); err != nil {
			return nil, err
		}
		fragment, err = editorXMLDirectText(fragment, root, text.Bytes())
		if err != nil {
			return nil, err
		}
	}
	root, err = parseEditorXML(fragment)
	if err != nil {
		return nil, err
	}
	var attrs []xml.Attr
	used := make(map[string]bool)
	for _, attr := range root.attrs {
		if attr.Name.Space == "xmlns" {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns:" + attr.Name.Local}, Value: attr.Value})
			used[attr.Name.Local] = true
		} else if attr.Name.Space == "" && attr.Name.Local == "xmlns" {
			attrs = append(attrs, attr)
		}
	}
	qualify := func(name, namespace string) string {
		if namespace == "" {
			return name
		}
		if namespace == XMLNamespace {
			return "xml:" + name
		}
		for _, attr := range attrs {
			if strings.HasPrefix(attr.Name.Local, "xmlns:") && attr.Value == namespace {
				return strings.TrimPrefix(attr.Name.Local, "xmlns:") + ":" + name
			}
		}
		prefix := "tag"
		for i := 1; used[prefix]; i++ {
			prefix = "tag" + strconv.Itoa(i)
		}
		used[prefix] = true
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns:" + prefix}, Value: namespace})
		return prefix + ":" + name
	}
	name := qualify(node.Name, node.Namespace)
	if node.Namespace == "" {
		for i := range attrs {
			if attrs[i].Name.Local == "xmlns" {
				attrs[i].Value = ""
			}
		}
	}
	for _, attr := range node.Attributes {
		key := qualify(attr.Name, attr.Namespace)
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: key}, Value: attr.Value})
	}
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	start := xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs}
	if err := encoder.EncodeToken(start); err != nil {
		return nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	if root.open != root.end {
		if node.Namespace == "" && before.Namespace != "" {
			var patches []editorXMLPatch
			for _, child := range root.children {
				standalone, err := editorXMLStandalone(fragment[child.start:child.end], child)
				if err != nil {
					return nil, err
				}
				patches = append(patches, editorXMLPatch{child.start - root.open, child.end - root.open, standalone})
			}
			output.Write(editorPatchXML(fragment[root.open:root.close], patches))
		} else {
			output.Write(fragment[root.open:root.close])
		}
	}
	if err := encoder.EncodeToken(start.End()); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
