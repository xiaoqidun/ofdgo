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
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// InvoiceDigest 计算原始导出数据的SHA256摘要
// 入参: data 导出数据
// 返回: string 小写十六进制摘要
func InvoiceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// invoiceReferenceEditability 一次计数并生成无共享引用的事实能力，字体和布局仍由事务预检
// 入参: invoice 发票
// 返回: []bool 与事实索引对应的编辑条件
func invoiceReferenceEditability(invoice Invoice) []bool {
	counts := make(map[string]int)
	for _, fact := range invoice.Facts {
		for _, ref := range fact.References {
			for _, id := range ref.ObjectIDs {
				counts[editorResourceID(ref.PageID)+"/"+editorResourceID(id)]++
			}
		}
	}
	result := make([]bool, len(invoice.Facts))
	for i, fact := range invoice.Facts {
		result[i] = len(fact.References) != 0
		for _, ref := range fact.References {
			result[i] = result[i] && editorResourceID(ref.PageID) != "" && len(ref.ObjectIDs) != 0
			for _, id := range ref.ObjectIDs {
				result[i] = result[i] && editorResourceID(id) != "" && counts[editorResourceID(ref.PageID)+"/"+editorResourceID(id)] == 1
			}
		}
	}
	return result
}

// invoiceReferenceDisplay 保留金额符号及已识别日期的显示格式，不改写引用结构
// 入参: invoice 原发票, fact 原事实, value 新业务值
// 返回: string 显示文字, error 值格式错误
func invoiceReferenceDisplay(invoice Invoice, fact InvoiceFact, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	binding, ok := invoiceNormalizedFactBinding(invoice, fact)
	if !ok {
		return value, nil
	}
	switch binding.Field {
	case "amount", "taxAmount", "totalAmount":
		old := strings.TrimSpace(fact.Value)
		prefix := ""
		for _, symbol := range []string{"¥", "￥"} {
			if strings.HasPrefix(old, symbol) {
				prefix = symbol
			}
		}
		value = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(value), "¥￥"))
		if invoiceDecimal(value) == nil {
			return "", fmt.Errorf("invalid invoice amount")
		}
		return prefix + value, nil
	case "date":
		layouts := []string{"2006年01月02日", "2006-01-02", "20060102", "2006/01/02"}
		for _, layout := range layouts {
			if _, err := time.Parse(layout, fact.Value); err == nil {
				for _, input := range layouts {
					if date, err := time.Parse(input, value); err == nil {
						return date.Format(layout), nil
					}
				}
				return "", fmt.Errorf("invalid invoice date")
			}
		}
		return "", fmt.Errorf("invoice date display format is unknown")
	}
	return value, nil
}

// invoiceNormalizedFactBinding 归一化标引命名空间并解析公共字段绑定
// 入参: invoice 发票, fact 原始事实
// 返回: InvoiceBinding 字段关联, bool 是否命中
func invoiceNormalizedFactBinding(invoice Invoice, fact InvoiceFact) (InvoiceBinding, bool) {
	if fact.Namespace == structureNamespaceEInvoice2016 || fact.Namespace == InvoiceStandardEDRM2019 {
		fact.Namespace = invoice.Standard
	}
	return invoiceEDRMFactBinding(invoice, fact)
}

// ReadInvoice 读取当前文档及版本声明的发票，字段路径相对导出XML，标引按对象引用提取文本
// 入参: ctx 取消上下文, source 精确来源
// 返回: InvoiceReport 发票及诊断, error 来源或读取错误
func (r *Reader) ReadInvoice(ctx context.Context, source InvoiceSource) (InvoiceReport, error) {
	ctx = invoiceWriteContext(ctx)
	data, err := r.ExportInvoice(ctx, source)
	if err != nil {
		return InvoiceReport{}, err
	}
	local := source
	local.NodePath = ""
	report, err := r.readInvoiceXML(ctx, data, local)
	for i := range report.Invoices {
		report.Invoices[i].Sources = []InvoiceSource{source}
	}
	report.DocumentIndex = r.DocumentIndex()
	if r.versionInfo != nil {
		report.Version = r.versionInfo.ID
	}
	return report, err
}

// ExportInvoice 导出当前文档及版本声明的发票，内联内容补齐继承的命名空间
// 相同选择器对应多个声明时拒绝操作，允许已识别的旧数据带解析诊断
// 入参: ctx 取消上下文, source 提取结果中的精确来源
// 返回: []byte 独立XML数据, error 来源或读取错误
func (r *Reader) ExportInvoice(ctx context.Context, source InvoiceSource) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report, err := r.InvoicesContext(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, invoice := range report.Invoices {
		for _, candidate := range invoice.Sources {
			found = found || candidate == source
		}
	}
	if !found {
		return nil, fmt.Errorf("invoice source is not declared in current document")
	}
	_, data, node, _, err := r.invoiceSourceNode(ctx, source)
	if err != nil {
		return nil, err
	}
	if source.NodePath != "" {
		return editorXMLStandaloneContext(ctx, data[node.start:node.end], node)
	}
	payload, err := r.invoiceData(ctx, source.FileLoc)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(payload), nil
}

// ExportInvoice 导出编辑器当前文档及版本声明的发票数据
// 入参: ctx 取消上下文, source 提取结果中的精确来源
// 返回: []byte 独立XML数据, error 来源或读取错误
func (e *Editor) ExportInvoice(ctx context.Context, source InvoiceSource) ([]byte, error) {
	if e == nil || e.source == nil {
		return nil, fmt.Errorf("invoice source not found")
	}
	if len(e.pages) == 0 {
		return e.source.reader.ExportInvoice(invoiceWriteContext(ctx), source)
	}
	r, err := e.Reader(invoiceWriteContext(ctx))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.ExportInvoice(ctx, source)
}

// ReplaceInvoiceData 仅替换选中声明，独立资源写时复制，旧数据允许诊断而新数据必须无诊断
// 不删除原签名，修改签名覆盖的内容后需重新验签
// 入参: ctx 取消上下文, source 精确来源, expectedDigest 必填原数据摘要, data 新发票XML
// 返回: InvoiceSource 提交后的实际来源, error 校验、冲突或提交错误
func (e *Editor) ReplaceInvoiceData(ctx context.Context, source InvoiceSource, expectedDigest string, data []byte) (InvoiceSource, error) {
	return e.changeInvoiceSource(ctx, source, expectedDigest, data, false)
}

// DeleteInvoiceData 仅删除选中来源声明或内联发票子树，不删除资源及其他别名
// 不删除原签名，修改签名覆盖的内容后需重新验签
// 入参: ctx 取消上下文, source 精确来源, expectedDigest 必填原数据摘要
// 返回: error 校验、冲突或提交错误
func (e *Editor) DeleteInvoiceData(ctx context.Context, source InvoiceSource, expectedDigest string) error {
	_, err := e.changeInvoiceSource(ctx, source, expectedDigest, nil, true)
	return err
}

// readInvoiceXML 在当前文档作用域解析业务载荷或对象引用标引
// 入参: ctx 取消上下文, data 原始XML, source 来源
// 返回: InvoiceReport 发票及诊断, error 读取错误
func (r *Reader) readInvoiceXML(ctx context.Context, data []byte, source InvoiceSource) (InvoiceReport, error) {
	report, err := parseInvoiceXML(ctx, data, source)
	if err != nil || source.Kind != "tag" || len(report.Invoices) != 0 {
		return report, err
	}
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return report, err
	}
	if !invoiceReferenceRoot(root) {
		if root.name.Local != "eInvoice" && root.name.Local != "EInvoice" && root.name.Local != "xbrl" {
			return InvoiceReport{}, nil
		}
		return report, nil
	}
	structure, err := parseStructure(ctx, data, CustomTag{TypeID: structureTypeOfficial})
	if err != nil {
		return report, err
	}
	if !structureInvoiceReferences(structure) {
		return report, nil
	}
	doc, err := r.docContext(ctx)
	if err != nil {
		return report, err
	}
	pages := signaturePageNumbers(doc)
	cache := make(map[int]map[string][]string)
	inv := Invoice{Kind: "inv", Standard: ofdNamespace2016, Version: "1.0", Sources: []InvoiceSource{source}}
	assigned := make(map[string]bool)
	total := 0
	var visit func(*StructureNode, string, string, int) error
	visit = func(node *StructureNode, nodePath, fieldPath string, itemIndex int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.Namespace != structure.Namespace {
			return nil
		}
		if fieldPath == "/eInvoice/GoodsInfos/GoodsInfo" {
			inv.Items = append(inv.Items, InvoiceItem{})
			itemIndex = len(inv.Items) - 1
		}
		var refs []StructureReference
		for _, child := range node.Children {
			refs = append(refs, child.References...)
		}
		if len(refs) != 0 {
			factSource := source
			factSource.NodePath = nodePath
			fact := InvoiceFact{Name: node.Name, Namespace: node.Namespace, Source: factSource, References: refs}
			valid := true
			for _, ref := range refs {
				page, _ := resourceValue(pages, ref.PageID)
				if page != 0 && cache[page] == nil {
					content, err := r.PageContentByIndexContext(ctx, page-1)
					if err != nil {
						return err
					}
					texts := make(map[string][]string)
					visitedUnits := make(map[*CompositeGraphicUnit]bool)
					var collect func(GraphicObject)
					collect = func(object GraphicObject) {
						if object.Type == "TextObject" {
							id := editorResourceID(object.TextObject.ID)
							texts[id] = append(texts[id], object.TextObject.Text())
						}
						if object.Type == "CompositeObject" {
							if resID := object.CompositeObject.ResourceID; resID != "" {
								if unit, _ := r.resolveCompositeScoped(content.pageResources, resID); unit != nil && !visitedUnits[unit] {
									visitedUnits[unit] = true
									for child := range unit.objects() {
										collect(child)
									}
									delete(visitedUnits, unit)
								}
							}
						}
					}
					for _, layer := range content.Content.Layer {
						for _, object := range layer.Objects {
							collect(object)
						}
					}
					cache[page] = texts
				}
				for _, id := range ref.ObjectIDs {
					values := cache[page][editorResourceID(id)]
					if len(values) != 1 {
						valid = false
						report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-reference", Message: "invoice text reference is missing or ambiguous", Source: factSource, Field: nodePath})
						continue
					}
					if len(values[0]) > invoiceXMLLimit-total {
						return fmt.Errorf("invoice extraction limit exceeded")
					}
					total += len(values[0])
					fact.Value += values[0]
				}
			}
			inv.Facts = append(inv.Facts, fact)
			var item *InvoiceItem
			if itemIndex >= 0 {
				item = &inv.Items[itemIndex]
				item.Fields = append(item.Fields, fact)
			}
			dst, numeric := invoiceEDRMField(&inv, item, fieldPath)
			if dst != nil && valid {
				value := strings.TrimSpace(fact.Value)
				if numeric {
					value = strings.TrimSpace(strings.TrimLeft(value, "¥￥"))
				}
				if numeric && value != "" && invoiceDecimal(value) == nil {
					report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-decimal", Message: "invalid invoice amount", Source: factSource, Field: nodePath})
				} else {
					key := fieldPath
					if itemIndex >= 0 {
						key = fmt.Sprintf("%d:%s", itemIndex, fieldPath)
					}
					if assigned[key] {
						*dst = ""
						report.Issues = append(report.Issues, InvoiceIssue{Code: "ambiguous-field", Message: "multiple invoice field references", Source: factSource, Field: nodePath})
					} else {
						*dst = value
						assigned[key] = true
					}
				}
			}
		}
		counts := make(map[string]int)
		for i := range node.Children {
			child := &node.Children[i]
			counts[child.Name]++
			if err := visit(child, fmt.Sprintf("%s/%s[%d]", nodePath, child.Name, counts[child.Name]), fieldPath+"/"+child.Name, itemIndex); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(structure, fmt.Sprintf("/%s[1]", structure.Name), "/eInvoice", -1); err != nil {
		return InvoiceReport{}, err
	}
	if date, err := time.Parse("2006年01月02日", inv.Date); err == nil {
		inv.Date = date.Format("2006-01-02")
	}
	invoiceAmountCheck(&inv, &report, source)
	report.Invoices = append(report.Invoices, inv)
	return report, ctx.Err()
}

// changeInvoiceSource 校验原始摘要并原子修改声明，预检隔离后的来源供绑定重定向
// 入参: ctx 取消上下文, source 来源, digest 原始摘要, data 替换数据, remove 删除声明
// 返回: InvoiceSource 新来源, error 校验或提交错误
func (e *Editor) changeInvoiceSource(ctx context.Context, source InvoiceSource, digest string, data []byte, remove bool) (InvoiceSource, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if digest == "" {
		return InvoiceSource{}, fmt.Errorf("invoice digest is required")
	}
	old, err := e.ExportInvoice(ctx, source)
	if err != nil {
		return InvoiceSource{}, err
	}
	if InvoiceDigest(old) != digest {
		return InvoiceSource{}, fmt.Errorf("invoice source has changed")
	}
	objectReference := false
	if source.Kind == "tag" {
		root, err := invoiceXMLTree(ctx, old)
		if err != nil {
			return InvoiceSource{}, err
		}
		objectReference = invoiceReferenceRoot(root)
	}
	if !remove {
		if len(data) > invoiceXMLLimit {
			return InvoiceSource{}, fmt.Errorf("invoice data limit exceeded")
		}
		if objectReference {
			r, err := e.Reader(ctx)
			if err != nil {
				return InvoiceSource{}, err
			}
			_, err = r.invoiceReferenceReport(ctx, data)
			r.Close()
			if err != nil {
				return InvoiceSource{}, err
			}
		} else {
			if _, err := invoiceWritableReport(ctx, data); err != nil {
				return InvoiceSource{}, err
			}
			if err := invoiceValidateXMLWrite(ctx, old, data); err != nil {
				return InvoiceSource{}, err
			}
		}
		if bytes.Equal(old, data) {
			return source, ctx.Err()
		}
	}
	base := e.source
	name, outer, selected, reference, err := base.reader.invoiceSourceNode(ctx, source)
	if err != nil {
		return InvoiceSource{}, err
	}
	parts := make(map[string][]byte)
	patch := editorXMLPatch{start: selected.start, end: selected.end}
	next := source
	if !remove && source.NodePath != "" {
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return InvoiceSource{}, err
		}
		patch.data, err = editorXMLStandaloneContext(ctx, data[root.start:root.end], root)
		if err != nil {
			return InvoiceSource{}, err
		}
	} else if !remove {
		file := e.packageName("Invoices/Invoice.xml")
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte("/"+file)); err != nil {
			return InvoiceSource{}, err
		}
		patch = editorXMLContent(outer, reference, escaped.Bytes())
		parts[file] = bytes.Clone(data)
		next.FileLoc = file
	}
	parts[name] = editorPatchXML(outer, []editorXMLPatch{patch})
	if !remove && source.Kind == "attachment" {
		root, err := parseEditorXMLContext(ctx, parts[name])
		if err != nil {
			return InvoiceSource{}, err
		}
		if root.matchesOFD("Document") {
			root = root.child("Attachments")
		}
		for _, child := range root.children {
			if child.matchesOFD("Attachment") && child.attr("ID") == source.ID {
				attribute, err := editorXMLAttributePatch(parts[name], child, "Size", ofdNumber(float64(len(data))/1024))
				if err != nil {
					return InvoiceSource{}, err
				}
				parts[name] = editorPatchXML(parts[name], []editorXMLPatch{attribute})
				break
			}
		}
	}
	isolated, err := base.reader.versionChanges(ctx, parts)
	if err != nil {
		return InvoiceSource{}, err
	}
	files := make(map[string][]byte, len(base.reader.files)+len(isolated))
	for key, value := range base.reader.files {
		files[key] = value
	}
	for key, value := range isolated {
		files[key] = value
	}
	view := base.reader.deriveView(files, base.reader.documentIndex, base.reader.selectedVersion)
	if err := view.initRootContext(ctx); err != nil {
		return InvoiceSource{}, err
	}
	if !remove {
		report, err := view.InvoicesContext(ctx)
		if err != nil {
			return InvoiceSource{}, err
		}
		found := false
		for _, invoice := range report.Invoices {
			for _, candidate := range invoice.Sources {
				if candidate.Kind == next.Kind && candidate.ID == next.ID && candidate.NodePath == next.NodePath && (next.NodePath != "" || candidate.FileLoc == next.FileLoc) {
					next, found = candidate, true
				}
			}
		}
		if !found {
			return InvoiceSource{}, fmt.Errorf("replacement invoice source is not reachable")
		}
	}
	if remove && source.Kind == "tag" {
		index, err := strconv.Atoi(source.ID)
		if err != nil {
			return InvoiceSource{}, err
		}
		updated := *base
		updated.structureStarts = make(map[int]map[string]int, len(base.structureStarts))
		for entry, starts := range base.structureStarts {
			if entry < index {
				updated.structureStarts[entry] = starts
			} else if entry > index {
				updated.structureStarts[entry-1] = starts
			}
		}
		base = &updated
	}
	if err := e.commitAnnotationPartsContext(ctx, base, parts); err != nil {
		return InvoiceSource{}, err
	}
	return next, nil
}

// invoiceSourceNode 定位唯一声明及其引用节点，拒绝歧义别名，不接受任意包路径
// 入参: ctx 取消上下文, source 已提取来源
// 返回: string 声明文件, []byte 原文, *editorXML 选中声明, *editorXML 资源引用, error 定位错误
func (r *Reader) invoiceSourceNode(ctx context.Context, source InvoiceSource) (string, []byte, *editorXML, *editorXML, error) {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return "", nil, nil, nil, err
	}
	container, entry, location := "", "", ""
	switch source.Kind {
	case "attachment":
		container, entry, location = "Attachments", "Attachment", doc.Attachments.Path
	case "tag":
		container, entry, location = "CustomTags", "CustomTag", doc.CustomTags.Path
	case "extension":
		container, entry, location = "Extensions", "Extension", doc.Extensions.Path
	default:
		return "", nil, nil, nil, fmt.Errorf("invalid invoice source kind")
	}
	name := r.documentRoot
	if strings.TrimSpace(location) != "" {
		name = r.ResPath(location)
	}
	data, err := r.invoiceData(ctx, name)
	if err != nil {
		return "", nil, nil, nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if root.matchesOFD("Document") {
		root = root.child(container)
	}
	var selected, reference *editorXML
	count, index, declarations := 0, 0, 0
	if root != nil && root.matchesOFD(container) {
		for _, child := range root.children {
			if !child.matchesOFD(entry) {
				continue
			}
			id := strconv.Itoa(index)
			index++
			if source.Kind == "attachment" {
				id = child.attr("ID")
			}
			if id != source.ID {
				continue
			}
			declarations++
			dataIndex := 0
			for _, node := range child.children {
				if source.NodePath != "" && source.Kind == "extension" && node.matchesOFD("Data") {
					dataIndex++
					for k, payload := range node.children {
						if source.NodePath == fmt.Sprintf("/Extensions/Extension[%d]/Data[%d]/*[%d]", index, dataIndex, k+1) && source.FileLoc == name {
							selected, count = payload, count+1
						}
					}
				} else if source.NodePath == "" && (source.Kind == "extension" && node.matchesOFD("ExtendData") || source.Kind != "extension" && node.matchesOFD("FileLoc")) {
					resolved := resolveResourcePath(name, "", strings.TrimSpace(editorImportText(data, node)))
					if resolved == source.FileLoc {
						selected, reference, count = child, node, count+1
						if source.Kind == "extension" {
							selected = node
						}
					}
				}
			}
		}
	}
	if count != 1 || declarations != 1 {
		return "", nil, nil, nil, fmt.Errorf("invoice source declaration is missing or ambiguous")
	}
	return name, data, selected, reference, ctx.Err()
}

// invoiceReferenceReport 校验当前文档中可解引用的发票标引，不将对象标识作为业务值
// 入参: ctx 取消上下文, data 标引原文
// 返回: InvoiceReport 解引用结果, error 格式或引用错误
func (r *Reader) invoiceReferenceReport(ctx context.Context, data []byte) (InvoiceReport, error) {
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return InvoiceReport{}, err
	}
	if !invoiceReferenceRoot(root) {
		return InvoiceReport{}, fmt.Errorf("expected an OFD object reference invoice")
	}
	report, err := r.readInvoiceXML(ctx, data, InvoiceSource{Kind: "tag"})
	if err != nil {
		return InvoiceReport{}, err
	}
	if len(report.Invoices) != 1 || len(report.Invoices[0].Facts) == 0 || len(report.Issues) != 0 {
		return InvoiceReport{}, fmt.Errorf("invoice object references are missing, ambiguous or invalid")
	}
	return report, nil
}

// importInvoiceReferences 将已有页面的有效引用创建为独立标引，保留原始XML扩展
// 入参: ctx 取消上下文, data 标引原文, options 创建选项
// 返回: InvoiceSource 标引来源, error 校验或提交错误
func (e *Editor) importInvoiceReferences(ctx context.Context, data []byte, options InvoiceCreateOptions) (InvoiceSource, error) {
	if options.Layout != nil || options.Encoding != nil {
		return InvoiceSource{}, fmt.Errorf("object reference import requires existing pages and no encoding options")
	}
	var source InvoiceSource
	err := e.Transaction(func(next *Editor) error {
		r, err := next.Reader(ctx)
		if err != nil {
			return err
		}
		_, err = r.invoiceReferenceReport(ctx, data)
		r.Close()
		if err != nil {
			return err
		}
		root, err := parseStructure(ctx, data, CustomTag{TypeID: structureTypeOfficial})
		if err != nil {
			return err
		}
		typeID := root.Name
		if root.Namespace != ofdNamespace2016 {
			typeID = structureTypeInvoice
		}
		index, err := next.AddStructure(CustomTag{TypeID: typeID, NameSpace: root.Namespace}, *root)
		if err != nil {
			return err
		}
		if err := next.editStructure(index, func(_ []byte, _ *editorXML, _ CustomTag) ([]byte, error) { return bytes.Clone(data), nil }); err != nil {
			return err
		}
		r, err = next.Reader(ctx)
		if err != nil {
			return err
		}
		defer r.Close()
		tags, err := r.CustomTagsContext(ctx)
		if err != nil {
			return err
		}
		source = InvoiceSource{Kind: "tag", ID: strconv.Itoa(index), FileLoc: r.ResPath(tags[index].FileLoc)}
		_, err = r.ExportInvoice(ctx, source)
		return err
	})
	if err != nil {
		return InvoiceSource{}, err
	}
	return source, nil
}

// invoiceReferenceBindings 解析唯一单对象事实的固有绑定，不猜测多对象文字分配
// 入参: ctx 取消上下文, invoice 解引用发票
// 返回: []InvoiceBinding 事实关联, error 文档读取错误
func (r *Reader) invoiceReferenceBindings(ctx context.Context, invoice Invoice) ([]InvoiceBinding, error) {
	doc, err := r.docContext(ctx)
	if err != nil {
		return nil, err
	}
	pages := signaturePageNumbers(doc)
	counts := make(map[string]int)
	for _, fact := range invoice.Facts {
		for _, ref := range fact.References {
			for _, id := range ref.ObjectIDs {
				counts[editorResourceID(ref.PageID)+"/"+editorResourceID(id)]++
			}
		}
	}
	var bindings []InvoiceBinding
	for i, fact := range invoice.Facts {
		if len(fact.References) != 1 || len(fact.References[0].ObjectIDs) != 1 {
			continue
		}
		ref := fact.References[0]
		page, _ := resourceValue(pages, ref.PageID)
		if page == 0 || counts[editorResourceID(ref.PageID)+"/"+editorResourceID(ref.ObjectIDs[0])] != 1 {
			continue
		}
		bindings = append(bindings, InvoiceBinding{Field: fmt.Sprintf("facts.%d", i), Item: -1, Page: page - 1, ObjectID: ref.ObjectIDs[0]})
	}
	return bindings, ctx.Err()
}

// editInvoiceReferences 原子修改引用目标并重新解引用校验，XML摘要外必须核对原文字
// 入参: ctx 取消上下文, source 来源, digest 标引摘要, changes 事实修改, synchronize 必须为真
// 返回: InvoiceUpdateResult 同步结果, error 冲突、布局或提交错误
func (e *Editor) editInvoiceReferences(ctx context.Context, source InvoiceSource, digest string, changes []InvoiceFieldChange, synchronize bool) (InvoiceUpdateResult, error) {
	if !synchronize {
		return InvoiceUpdateResult{}, fmt.Errorf("object reference values are page text and require synchronization")
	}
	var result InvoiceUpdateResult
	err := e.Transaction(func(next *Editor) error {
		r, err := next.Reader(ctx)
		if err != nil {
			return err
		}
		defer r.Close()
		data, err := r.ExportInvoice(ctx, source)
		if err != nil {
			return err
		}
		if digest == "" || InvoiceDigest(data) != digest {
			return fmt.Errorf("invoice source changed")
		}
		report, err := r.invoiceReferenceReport(ctx, data)
		if err != nil {
			return err
		}
		invoice := report.Invoices[0]
		invoice.Facts = slices.Clone(invoice.Facts)
		bindings, err := r.invoiceReferenceBindings(ctx, invoice)
		if err != nil {
			return err
		}
		var selected []InvoiceBinding
		var singles []InvoiceBinding
		joint := make(map[int][]InvoiceBinding)
		flow := make(map[int]InvoiceFieldChange)
		editable := invoiceReferenceEditability(invoice)
		seen := make(map[int]bool)
		for _, change := range changes {
			if change.Source.Kind != "" && change.Source.Kind != source.Kind || change.Source.ID != "" && change.Source.ID != source.ID || change.Source.FileLoc != "" && change.Source.FileLoc != source.FileLoc || change.Revision != "" && change.Revision != digest {
				return fmt.Errorf("invoice field belongs to another source or revision")
			}
			index := slices.IndexFunc(invoice.Facts, func(fact InvoiceFact) bool { return fact.Source.NodePath == change.Source.NodePath })
			if index < 0 || seen[index] || change.Delete || change.Nil || change.ExpectedNil != nil && *change.ExpectedNil || change.ExpectedValue == nil || *change.ExpectedValue != invoice.Facts[index].Value {
				return fmt.Errorf("object reference edit requires a unique fact and matching expected text")
			}
			seen[index] = true
			if change.Value == invoice.Facts[index].Value && change.Font == "" && len(change.FontData) == 0 {
				continue
			}
			if !editable[index] {
				return fmt.Errorf("invoice text object is shared by multiple facts")
			}
			binding := slices.IndexFunc(bindings, func(binding InvoiceBinding) bool { return binding.Field == fmt.Sprintf("facts.%d", index) })
			value, err := invoiceReferenceDisplay(invoice, invoice.Facts[index], change.Value)
			if err != nil {
				return err
			}
			if binding >= 0 && change.Font == "" && len(change.FontData) == 0 && value != "" {
				selected = append(selected, bindings[binding])
				singles = append(singles, bindings[binding])
			} else {
				fact := invoice.Facts[index]
				if utf8.RuneCountInString(value) != utf8.RuneCountInString(fact.Value) || value == "" || change.Font != "" || len(change.FontData) != 0 {
					flow[index] = change
				}
				for _, ref := range fact.References {
					page := slices.IndexFunc(next.pages, func(page PageContent) bool { return sameResourceID(page.ID, ref.PageID) })
					if page < 0 {
						return fmt.Errorf("invoice reference page not found")
					}
					for _, id := range ref.ObjectIDs {
						item := InvoiceBinding{Field: fmt.Sprintf("facts.%d", index), Item: -1, Page: page, ObjectID: id}
						joint[index] = append(joint[index], item)
						selected = append(selected, item)
					}
				}
			}
			invoice.Facts[index].Value = value
		}
		structures, err := r.StructureContext(ctx)
		if err != nil {
			return err
		}
		for i, structure := range structures {
			if strconv.Itoa(i) == source.ID {
				continue
			}
			if structure.Error != "" {
				return fmt.Errorf("cannot verify shared invoice references")
			}
			var check func(*StructureNode) error
			check = func(node *StructureNode) error {
				if node == nil {
					return nil
				}
				for _, ref := range node.References {
					for _, binding := range selected {
						if sameResourceID(next.pages[binding.Page].ID, ref.PageID) {
							for _, id := range ref.ObjectIDs {
								if sameResourceID(id, binding.ObjectID) {
									return fmt.Errorf("invoice text object is referenced by another structure")
								}
							}
						}
					}
				}
				for j := range node.Children {
					if err := check(&node.Children[j]); err != nil {
						return err
					}
				}
				return ctx.Err()
			}
			if err := check(structure.Root); err != nil {
				return err
			}
		}
		for i, fact := range report.Invoices[0].Facts {
			if len(joint[i]) == 0 {
				continue
			}
			if change, ok := flow[i]; ok {
				if err := next.layoutInvoiceReferenceSlots(ctx, joint[i], fact.Value, invoice.Facts[i].Value, change.Font, change.FontData); err != nil {
					return err
				}
				continue
			}
			value := []rune(invoice.Facts[i].Value)
			offset := 0
			var original strings.Builder
			for _, binding := range joint[i] {
				object, err := next.Object(binding.Page, binding.ObjectID)
				if err != nil || object.Type != "TextObject" {
					return fmt.Errorf("joint invoice reference requires text objects")
				}
				text := object.TextObject.Text()
				original.WriteString(text)
				end := offset + utf8.RuneCountInString(text)
				if end > len(value) {
					return fmt.Errorf("invoice reference text length changed")
				}
				replacement := string(value[offset:end])
				offset = end
				if text == replacement {
					continue
				}
				analysis, err := next.AnalyzeTextSelection(binding.Page, TextSelection{IDs: []string{binding.ObjectID}})
				if err != nil {
					return err
				}
				if analysis.Text != text {
					return fmt.Errorf("invoice reference requires explicit line break mapping")
				}
				if err := next.UpdateTextSelection(binding.Page, analysis.Selection, TextSelectionEdit{End: utf8.RuneCountInString(text), Text: replacement}); err != nil {
					return err
				}
			}
			if original.String() != fact.Value || offset != len(value) {
				return fmt.Errorf("invoice reference text changed")
			}
		}
		if err := next.SyncInvoicePages(ctx, invoice, singles); err != nil {
			return err
		}
		view, err := next.Reader(ctx)
		if err != nil {
			return err
		}
		defer view.Close()
		verified, err := view.invoiceReferenceReport(ctx, data)
		if err != nil {
			return err
		}
		for i := range invoice.Facts {
			if verified.Invoices[0].Facts[i].Value != invoice.Facts[i].Value {
				return fmt.Errorf("invoice text failed to round-trip after synchronization")
			}
		}
		result = InvoiceUpdateResult{Source: source, Digest: digest, Synchronized: true}
		signed, err := view.hasOutputSignatures(ctx)
		if err != nil {
			return err
		}
		if signed {
			result.Issues = append(result.Issues, InvoiceIssue{Code: "signature-recheck-required", Message: "modified signed content requires signature verification", Source: source})
		}
		return ctx.Err()
	})
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	return result, nil
}

// setInvoiceReferences 将已有摘要字段变化映射回唯一事实，原Facts作为图元旧值基线
// 入参: ctx 取消上下文, source 来源, digest 标引摘要, index 发票索引, invoice 新业务值, synchronize 是否同步
// 返回: InvoiceUpdateResult 同步结果, error 不可表达字段或提交错误
func (e *Editor) setInvoiceReferences(ctx context.Context, source InvoiceSource, digest string, index int, invoice Invoice, synchronize bool) (InvoiceUpdateResult, error) {
	if index != 0 {
		return InvoiceUpdateResult{}, fmt.Errorf("invoice index out of range")
	}
	r, err := e.Reader(ctx)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	defer r.Close()
	report, err := r.ReadInvoice(ctx, source)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	if len(report.Invoices) != 1 {
		return InvoiceUpdateResult{}, fmt.Errorf("object reference invoice not found")
	}
	before := report.Invoices[0]
	if !reflect.DeepEqual(invoice.Facts, before.Facts) || !reflect.DeepEqual(invoice.Items, before.Items) {
		return InvoiceUpdateResult{}, fmt.Errorf("object reference SetInvoice requires unchanged original facts and items; use EditInvoice for fact edits")
	}
	var changes []InvoiceFieldChange
	for _, field := range invoiceLayoutFields(invoice) {
		value, err := invoiceBindingValue(before, InvoiceBinding{Field: field.field, Item: field.item})
		if err != nil {
			return InvoiceUpdateResult{}, err
		}
		if value == field.value {
			continue
		}
		match := -1
		for i, fact := range before.Facts {
			binding, ok := invoiceNormalizedFactBinding(before, fact)
			if ok && binding.Field == field.field && binding.Item == field.item {
				if match >= 0 {
					return InvoiceUpdateResult{}, fmt.Errorf("invoice summary field is ambiguous")
				}
				match = i
			}
		}
		if match < 0 {
			return InvoiceUpdateResult{}, fmt.Errorf("invoice field %s has no object reference", field.field)
		}
		fact := before.Facts[match]
		changes = append(changes, InvoiceFieldChange{Source: fact.Source, Value: field.value, ExpectedValue: &fact.Value})
	}
	return e.editInvoiceReferences(ctx, source, digest, changes, synchronize)
}

// createInvoiceReferences 按已知样例摘要字段生成信息页和真实标引，不推定税务业务创建规范
// 入参: ctx 取消上下文, invoice 摘要数据, options 必填票面字体和排版
// 返回: InvoiceSource 标引来源, error 不可表达字段、排版或提交错误
func (e *Editor) createInvoiceReferences(ctx context.Context, invoice Invoice, options InvoiceCreateOptions) (InvoiceSource, error) {
	if options.Layout == nil || options.Layout.Font == "" || options.Encoding != nil {
		return InvoiceSource{}, fmt.Errorf("object reference creation requires Editor.CreateInvoice with Layout.Font and no standalone encoding")
	}
	if invoice.Kind != "inv" || invoice.Version != "1.0" || invoice.Number == "" || len(invoice.Facts) != 0 || len(invoice.Items) != 0 || len(invoice.Sources) != 0 {
		return InvoiceSource{}, fmt.Errorf("object reference creation supports version 1.0 invoice summaries without existing facts, sources or inferred item groups")
	}
	paths := make(map[string]string)
	probe := Invoice{Standard: ofdNamespace2016}
	for _, path := range invoiceEDRMPaths() {
		field, _ := invoiceEDRMField(&probe, nil, path)
		if field == nil {
			continue
		}
		*field = "mapped"
		for _, candidate := range invoiceLayoutFields(probe) {
			if candidate.value == "mapped" {
				paths[candidate.field] = strings.TrimPrefix(path, "/eInvoice/")
			}
		}
		*field = ""
	}
	for _, field := range invoiceLayoutFields(invoice) {
		if field.value != "" && field.field != "kind" && field.field != "standard" && field.field != "version" && paths[field.field] == "" {
			return InvoiceSource{}, fmt.Errorf("object reference sample cannot represent field %s", field.field)
		}
	}
	issues, err := ValidateInvoice(ctx, invoice)
	if err != nil {
		return InvoiceSource{}, err
	}
	if len(issues) != 0 {
		return InvoiceSource{}, fmt.Errorf("invalid object reference invoice: %s", issues[0].Message)
	}
	var source InvoiceSource
	err = e.Transaction(func(next *Editor) error {
		display := invoice
		display.Kind, display.Standard, display.Version = "", "", ""
		bindings, err := next.AppendInvoicePages(ctx, display, *options.Layout)
		if err != nil {
			return err
		}
		root := StructureNode{Name: "root", Namespace: ofdNamespace2016, Attributes: []StructureAttribute{{Name: "version", Value: "1.0"}}}
		for _, binding := range bindings {
			node := &root
			for _, name := range strings.Split(paths[binding.Field], "/") {
				i := slices.IndexFunc(node.Children, func(child StructureNode) bool { return child.Name == name })
				if i < 0 {
					node.Children = append(node.Children, StructureNode{Name: name, Namespace: ofdNamespace2016})
					i = len(node.Children) - 1
				}
				node = &node.Children[i]
			}
			node.Children = append(node.Children, StructureNode{Name: "ObjectRef", Namespace: ofdNamespace2016, Attributes: []StructureAttribute{{Name: "PageRef", Value: next.pages[binding.Page].ID}}, Text: binding.ObjectID})
		}
		data, err := encodeStructureNode(root)
		if err != nil {
			return err
		}
		source, err = next.importInvoiceReferences(ctx, data, InvoiceCreateOptions{})
		return err
	})
	if err != nil {
		return InvoiceSource{}, err
	}
	return source, nil
}
