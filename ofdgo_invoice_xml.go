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
	"maps"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// InvoiceStandardVAT2023 表示2023版增值税发票分类标准
const InvoiceStandardVAT2023 = "http://xbrl.mof.gov.cn/taxonomy/2023-12-31/inv"

// InvoiceStandardDigital2023 表示2023版数字化电子发票分类标准
const InvoiceStandardDigital2023 = "http://xbrl.mof.gov.cn/taxonomy/2023-12-31/einv"

// InvoiceStandardAir2023 表示2023版航空运输电子客票行程单分类标准
const InvoiceStandardAir2023 = "http://xbrl.mof.gov.cn/taxonomy/2023-12-31/atr"

// InvoiceStandardRail2023 表示2023版铁路电子客票分类标准
const InvoiceStandardRail2023 = "http://xbrl.mof.gov.cn/taxonomy/2023-12-31/rai"

// InvoiceStandardVAT2021 表示2021版增值税发票分类标准
const InvoiceStandardVAT2021 = "http://xbrl.mof.gov.cn/taxonomy/2021-09-30/inv"

// InvoiceStandardAir2021 表示2021版航空运输电子客票行程单分类标准
const InvoiceStandardAir2021 = "http://xbrl.mof.gov.cn/taxonomy/2021-11-30/atr"

// InvoiceStandardEDRM2019 表示2019版电子发票业务标准
const InvoiceStandardEDRM2019 = "http://www.edrm.org.cn/schema/e-invoice/2019"

// InvoiceStandardEInvoice 表示无命名空间的EInvoice业务标准，版本由Invoice.Version指定
const InvoiceStandardEInvoice = "EInvoice"

// InvoiceStandardObjectReference 表示基于OFD对象引用的发票标引
const InvoiceStandardObjectReference = ofdNamespace2016

// InvoiceCurrencyNamespace 表示InvoiceUnit中ISO货币度量使用的命名空间
const InvoiceCurrencyNamespace = "http://www.xbrl.org/2003/iso4217"

// invoiceXBRL 表示XBRL实例命名空间
const invoiceXBRL = "http://www.xbrl.org/2003/instance"

// invoiceLinkbase 表示XBRL链接库命名空间
const invoiceLinkbase = "http://www.xbrl.org/2003/linkbase"

// invoiceDimension 表示XBRL实例维度命名空间
const invoiceDimension = "http://xbrl.org/2006/xbrldi"

// invoiceXMLSignature 表示XML数字签名命名空间
const invoiceXMLSignature = "http://www.w3.org/2000/09/xmldsig#"

// invoiceStandardEDRM2016 表示2016版电子发票标引命名空间
const invoiceStandardEDRM2016 = "http://www.edrm.org.cn/schema/e-invoice/2016"

// invoiceDecimalPattern 仅接受XML十进制词法
var invoiceDecimalPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)

// invoiceXMLNode 保存有界XML节点及原始文本切片
type invoiceXMLNode struct {
	name     xml.Name
	attrs    []xml.Attr
	text     strings.Builder
	children []*invoiceXMLNode
	parent   *invoiceXMLNode
	ns       map[string]string
	path     string
	counts   map[xml.Name]int
	start    int64
	inner    []byte
	parts    []invoiceXMLPart
}

// invoiceXMLPart 保留混合内容顺序，供类型化维度规范化使用
type invoiceXMLPart struct {
	text  string
	child *invoiceXMLNode
}

// invoiceContextMeta 保存上下文的主体期间归并键与规范化维度键
type invoiceContextMeta struct {
	baseKey       string
	dimKey        string
	hasSubContext bool
	dims          []InvoiceDimension
}

// invoiceDecimalEqual 精确比较十进制词法值，不接受指数或分数
// 入参: a 左十进制文本, b 右十进制文本
// 返回: bool 两个合法值是否相等
func invoiceDecimalEqual(a, b string) bool {
	x, y := invoiceDecimal(a), invoiceDecimal(b)
	return x != nil && y != nil && x.Cmp(y) == 0
}

// invoiceDecimal 解析有界十进制文本
// 入参: s 原始文本
// 返回: *big.Rat 精确数值，非法时为空
func invoiceDecimal(s string) *big.Rat {
	s = strings.TrimSpace(s)
	if len(s) > 256 || !invoiceDecimalPattern.MatchString(s) {
		return nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil
	}
	return r
}

// invoiceXMLAttr 按命名空间读取属性
// 入参: n 节点, space 命名空间, name 属性名
// 返回: string 属性值
func invoiceXMLAttr(n *invoiceXMLNode, space, name string) string {
	for _, a := range n.attrs {
		if a.Name.Space == space && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// invoiceXMLHasAttr 区分缺省属性与空字符串属性
// 入参: n 节点, space 命名空间, name 属性名
// 返回: bool 属性是否存在
func invoiceXMLHasAttr(n *invoiceXMLNode, space, name string) bool {
	for _, a := range n.attrs {
		if a.Name == (xml.Name{Space: space, Local: name}) {
			return true
		}
	}
	return false
}

// invoiceXMLTree 读取受限XML树，不解析外部实体
// 入参: ctx 取消上下文, data 文档字节
// 返回: *invoiceXMLNode 根节点, error 解析错误
func invoiceXMLTree(ctx context.Context, data []byte) (*invoiceXMLNode, error) {
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("invoice XML exceeds size limit")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	d := xml.NewDecoder(bytes.NewReader(data))
	var root, current *invoiceXMLNode
	nodes, depth, bindings, pathBytes := 0, 0, 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before := d.InputOffset()
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch v := t.(type) {
		case xml.StartElement:
			attributes := make(map[xml.Name]bool, len(v.Attr))
			for _, attr := range v.Attr {
				if attributes[attr.Name] {
					return nil, fmt.Errorf("duplicate invoice XML attribute")
				}
				attributes[attr.Name] = true
			}
			nodes++
			depth++
			if nodes > 100000 || depth > 128 {
				return nil, fmt.Errorf("invoice XML exceeds structural limit")
			}
			for _, a := range v.Attr {
				if (a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns") && len(a.Value) > 4096 {
					return nil, fmt.Errorf("invoice XML exceeds namespace URI limit")
				}
			}
			n := &invoiceXMLNode{name: v.Name, attrs: v.Attr, parent: current, ns: map[string]string{"xml": XMLNamespace}, counts: map[xml.Name]int{}, start: d.InputOffset()}
			index := 1
			if current != nil {
				for k, v := range current.ns {
					n.ns[k] = v
				}
				current.counts[n.name]++
				index = current.counts[n.name]
				current.children = append(current.children, n)
				current.parts = append(current.parts, invoiceXMLPart{child: n})
				n.path = current.path
			}
			for _, a := range v.Attr {
				if a.Name.Space == "xmlns" {
					n.ns[a.Name.Local] = a.Value
				}
				if a.Name.Space == "" && a.Name.Local == "xmlns" {
					n.ns[""] = a.Value
				}
			}
			bindings += len(n.ns)
			if bindings > 1000000 {
				return nil, fmt.Errorf("invoice XML exceeds namespace limit")
			}
			indexText := strconv.Itoa(index)
			pathLength := len(n.path) + len(n.name.Space) + len(n.name.Local) + len(indexText) + 5
			if pathLength > 16<<10 || pathLength > (64<<20)-pathBytes {
				return nil, fmt.Errorf("invoice XML exceeds path limit")
			}
			pathBytes += pathLength
			n.path += "/{" + n.name.Space + "}" + n.name.Local + "[" + indexText + "]"
			if current == nil {
				if root != nil {
					return nil, fmt.Errorf("multiple XML roots")
				}
				root = n
			}
			current = n
		case xml.EndElement:
			if before >= current.start {
				current.inner = data[current.start:before]
			}
			current = current.parent
			depth--
		case xml.CharData:
			if current != nil {
				current.text.Write(v)
				current.parts = append(current.parts, invoiceXMLPart{text: string(v)})
			} else if strings.TrimSpace(string(v)) != "" {
				return nil, fmt.Errorf("text outside XML root")
			}
		case xml.Directive:
			return nil, fmt.Errorf("XML directives are unsupported")
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty XML document")
	}
	return root, nil
}

// invoiceTaxonomy 识别已核实的官方命名空间
// 入参: ns 命名空间URI
// 返回: string 票种, string 版本，未知时为空
func invoiceTaxonomy(ns string) (string, string) {
	switch ns {
	case InvoiceStandardVAT2023:
		return "inv", "2023-12-31"
	case InvoiceStandardDigital2023:
		return "einv", "2023-12-31"
	case InvoiceStandardAir2023:
		return "atr", "2023-12-31"
	case InvoiceStandardRail2023:
		return "rai", "2023-12-31"
	case InvoiceStandardAir2021:
		return "atr", "2021-11-30"
	case InvoiceStandardVAT2021:
		return "inv", "2021-09-30"
	}
	return "", ""
}

// parseInvoiceXML 解析明确识别的发票格式，未知格式仅产生诊断
// 入参: ctx 取消上下文, data XML数据, source 来源
// 返回: InvoiceReport 独立候选与诊断, error XML或限额错误
func parseInvoiceXML(ctx context.Context, data []byte, source InvoiceSource) (InvoiceReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var report InvoiceReport
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return report, err
	}
	issue := func(code, field, msg string, s InvoiceSource) {
		report.Issues = append(report.Issues, InvoiceIssue{Code: code, Field: field, Message: msg, Source: s})
	}
	if source.Kind == "tag" && (invoiceObjectReferences(root) || invoiceEmptyTagRoot(root)) {
		return report, nil
	}
	if root.name == (xml.Name{Local: "EInvoice"}) || root.name.Local == "eInvoice" && (root.name.Space == "" || root.name.Space == InvoiceStandardEDRM2019) {
		return invoicePlainXML(ctx, root, source)
	}
	if root.name != (xml.Name{Space: invoiceXBRL, Local: "xbrl"}) {
		issue("unsupported", "", "unsupported invoice XML root or version", source)
		return report, nil
	}
	contexts := map[string]*invoiceXMLNode{}
	units := map[string]*invoiceXMLNode{}
	definitionIDs := map[string]bool{}
	for _, n := range root.children {
		if n.name.Space != invoiceXBRL {
			continue
		}
		var target map[string]*invoiceXMLNode
		if n.name.Local == "context" {
			target = contexts
		}
		if n.name.Local == "unit" {
			target = units
		}
		if target == nil {
			continue
		}
		id := invoiceXMLAttr(n, "", "id")
		if definitionIDs[id] {
			contexts[id] = nil
			units[id] = nil
			issue("invalid-reference", n.name.Local, "duplicate context or unit ID", source)
			continue
		}
		definitionIDs[id] = true
		if _, ok := target[id]; ok || id == "" {
			target[id] = nil
			issue("invalid-reference", n.name.Local, "missing or duplicate definition ID", source)
		} else {
			target[id] = n
		}
	}
	indexes := map[string]int{}
	seen := map[string]InvoiceFact{}
	blocked := map[string]bool{}
	dimensions := map[string][]InvoiceDimension{}
	contextMetas := map[string]invoiceContextMeta{}
	for _, id := range invoiceSortedNodes(contexts) {
		n := contexts[id]
		if n == nil {
			continue
		}
		meta, valid := invoiceContextInfo(n)
		dimensions[id] = meta.dims
		if !valid {
			contexts[id] = nil
			issue("invalid-reference", "context", "invalid context entity, period or dimensions", source)
		} else {
			contextMetas[id] = meta
		}
	}
	unitValues := map[string]string{}
	for _, id := range invoiceSortedNodes(units) {
		n := units[id]
		if n == nil {
			continue
		}
		v, valid := invoiceUnit(n)
		if !valid {
			units[id] = nil
			issue("invalid-reference", "unit", "invalid unit measures or QName", source)
		} else {
			unitValues[id] = v
		}
	}
	type ctxFactStat struct {
		hasHeader bool
		hasItem   bool
	}
	ctxStats := map[string]*ctxFactStat{}
	var prescan func(*invoiceXMLNode)
	prescan = func(n *invoiceXMLNode) {
		if n.name.Space == invoiceXBRL || n.name.Space == invoiceLinkbase {
			return
		}
		if len(n.children) > 0 && invoiceXMLAttr(n, "", "contextRef") == "" {
			for _, c := range n.children {
				prescan(c)
			}
			return
		}
		kind, _ := invoiceTaxonomy(n.name.Space)
		ref := invoiceXMLAttr(n, "", "contextRef")
		if kind == "" || contexts[ref] == nil {
			return
		}
		pair := n.name.Space + "\x00" + ref
		stat := ctxStats[pair]
		if stat == nil {
			stat = &ctxFactStat{}
			ctxStats[pair] = stat
		}
		inTuple := n.parent != root && n.parent.name.Space == n.name.Space && invoiceItemTuple(kind, n.parent.name.Local)
		if inTuple {
			stat.hasHeader = true
			return
		}
		if invoiceKnownItemFact(kind, n.name.Local) {
			stat.hasItem = true
		} else {
			stat.hasHeader = true
		}
	}
	for _, n := range root.children {
		prescan(n)
	}
	hasPlainHeader := map[string]bool{}
	for pair, stat := range ctxStats {
		space, ref, _ := strings.Cut(pair, "\x00")
		meta := contextMetas[ref]
		if !meta.hasSubContext && stat.hasHeader {
			hasPlainHeader[space+"\x00"+meta.baseKey] = true
		}
	}
	itemContexts := map[string]bool{}
	mainRefsByGroup := map[string][]string{}
	for _, pair := range slices.Sorted(maps.Keys(ctxStats)) {
		stat := ctxStats[pair]
		space, ref, _ := strings.Cut(pair, "\x00")
		meta := contextMetas[ref]
		group := space + "\x00" + meta.baseKey
		if len(meta.dims) > 0 || meta.hasSubContext && !stat.hasHeader && stat.hasItem || !stat.hasHeader && stat.hasItem && hasPlainHeader[group] {
			itemContexts[pair] = true
		} else {
			mainRefsByGroup[group] = append(mainRefsByGroup[group], ref)
		}
	}
	invoiceBucketKey := func(space, ref string) (string, bool) {
		if contexts[ref] == nil {
			return "raw:" + space + "\x00" + ref, false
		}
		pair := space + "\x00" + ref
		meta := contextMetas[ref]
		group := space + "\x00" + meta.baseKey
		isItem := itemContexts[pair]
		mains := mainRefsByGroup[group]
		if len(mains) <= 1 {
			return "base:" + group, isItem
		}
		if !isItem {
			return "ctx:" + pair, false
		}
		matchedMain := ""
		for _, m := range mains {
			if strings.HasPrefix(ref, m) && len(m) > len(matchedMain) {
				matchedMain = m
			}
		}
		if matchedMain != "" {
			return "ctx:" + space + "\x00" + matchedMain, true
		}
		return "base:" + group, true
	}
	itemIndexes := map[string]int{}
	unknown := map[string][]InvoiceFact{}
	var unknownOrder []InvoiceFact
	assignedContexts := map[string]bool{}
	assign := func(dst *string, key string, f InvoiceFact, numeric bool) {
		if dst == nil {
			return
		}
		if old, ok := seen[key]; ok {
			equal := old.Value == f.Value || numeric && invoiceDecimalEqual(old.Value, f.Value)
			if !equal || old.Nil != f.Nil || unitValues[old.Unit] != unitValues[f.Unit] {
				blocked[key] = true
				*dst = ""
				issue("conflict", f.Name, "conflicting facts in the same context and tuple", f.Source)
			}
			return
		}
		seen[key] = f
		if !blocked[key] && !f.Nil {
			*dst = f.Value
		}
	}
	var visit func(*invoiceXMLNode) error
	visit = func(n *invoiceXMLNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.name.Space == invoiceXBRL || n.name.Space == invoiceLinkbase {
			return nil
		}
		if len(n.children) > 0 && invoiceXMLAttr(n, "", "contextRef") == "" {
			for _, c := range n.children {
				if err := visit(c); err != nil {
					return err
				}
			}
			return nil
		}
		kind, version := invoiceTaxonomy(n.name.Space)
		ref := invoiceXMLAttr(n, "", "contextRef")
		s := source
		s.NodePath += n.path
		fact := InvoiceFact{Name: n.name.Local, Namespace: n.name.Space, Value: n.text.String(), Context: ref, Unit: invoiceXMLAttr(n, "", "unitRef"), Decimals: invoiceXMLAttr(n, "", "decimals"), Precision: invoiceXMLAttr(n, "", "precision"), Source: s, Nil: invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil") == "true" || invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil") == "1", Dimensions: dimensions[ref]}
		if len(n.children) > 0 {
			fact.Value = string(n.inner)
		}
		if kind == "" {
			unknown[ref] = append(unknown[ref], fact)
			unknownOrder = append(unknownOrder, fact)
			return nil
		}
		key, isItemCtx := invoiceBucketKey(n.name.Space, ref)
		i, ok := indexes[key]
		if !ok {
			i = len(report.Invoices)
			indexes[key] = i
			report.Invoices = append(report.Invoices, Invoice{Kind: kind, Standard: n.name.Space, Version: version, Sources: []InvoiceSource{source}})
		}
		inv := &report.Invoices[i]
		inv.Facts = append(inv.Facts, fact)
		nilValue := invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil")
		if invoiceXMLHasAttr(n, XMLSchemaInstanceNamespace, "nil") && nilValue != "true" && nilValue != "false" && nilValue != "1" && nilValue != "0" {
			issue("invalid-value", fact.Name, "invalid xsi:nil", s)
			return nil
		}
		if fact.Nil && (strings.TrimSpace(fact.Value) != "" || len(n.children) > 0) {
			issue("invalid-value", fact.Name, "nil fact contains a value", s)
			return nil
		}
		if contexts[ref] == nil {
			issue("invalid-reference", fact.Name, "undefined or ambiguous contextRef", s)
			return nil
		}
		if fact.Unit != "" && units[fact.Unit] == nil {
			issue("invalid-reference", fact.Name, "undefined or ambiguous unitRef", s)
			return nil
		}
		numeric := invoiceNumeric(kind, fact.Name)
		if numeric && !fact.Nil && invoiceDecimal(fact.Value) == nil {
			issue("invalid-decimal", fact.Name, "invalid or oversized decimal lexical value", s)
			return nil
		}
		hasDecimals, hasPrecision := invoiceXMLHasAttr(n, "", "decimals"), invoiceXMLHasAttr(n, "", "precision")
		known := numeric || invoiceXMLField(&Invoice{}, kind, fact.Name) != nil || invoiceItemField(&InvoiceItem{}, kind, fact.Name) != nil
		if hasDecimals && hasPrecision || fact.Nil && (hasDecimals || hasPrecision) || known && !numeric && (hasDecimals || hasPrecision) || numeric && !fact.Nil && !hasDecimals && !hasPrecision || hasDecimals && !invoiceAccuracy(fact.Decimals, false) || hasPrecision && !invoiceAccuracy(fact.Precision, true) {
			issue("invalid-precision", fact.Name, "invalid, missing or mutually exclusive numeric accuracy attributes", s)
			return nil
		}
		if numeric && fact.Unit == "" {
			issue("invalid-reference", fact.Name, "numeric fact has no unitRef", s)
			return nil
		}
		if numeric && invoiceMonetary(kind, fact.Name) && !invoiceCurrencyUnit(unitValues[fact.Unit]) {
			issue("invalid-reference", fact.Name, "monetary fact requires a simple ISO 4217 unit", s)
			return nil
		}
		if numeric && invoiceMonetary(kind, fact.Name) && invoiceCurrencyUnit(unitValues[fact.Unit]) {
			currency := strings.TrimPrefix(unitValues[fact.Unit], "{"+InvoiceCurrencyNamespace+"}")
			cf := fact
			cf.Value = currency
			cf.Unit = ""
			cf.Nil = false
			assign(&inv.Currency, key+"\x00currency", cf, false)
		}
		if n.parent == root && len(fact.Dimensions) == 0 && !isItemCtx {
			assign(invoiceXMLField(inv, kind, fact.Name), key+"\x00"+fact.Name, fact, numeric)
		}
		tuple := ""
		if n.parent != root && n.parent.name.Space == n.name.Space && invoiceItemTuple(kind, n.parent.name.Local) {
			tuple = n.parent.path
		}
		if tuple != "" || len(fact.Dimensions) > 0 || isItemCtx {
			meta := contextMetas[ref]
			rowKey := "ctx:" + ref
			if tuple != "" {
				rowKey = "tuple:" + tuple + "\x00" + meta.dimKey
			} else if meta.dimKey != "" {
				rowKey = "dim:" + meta.dimKey
			}
			ik := key + "\x00" + rowKey
			idx, ok := itemIndexes[ik]
			if !ok {
				idx = len(inv.Items)
				itemIndexes[ik] = idx
				inv.Items = append(inv.Items, InvoiceItem{})
			}
			item := &inv.Items[idx]
			item.Fields = append(item.Fields, fact)
			assign(invoiceItemField(item, kind, fact.Name), ik+"\x00"+fact.Name, fact, numeric)
		}
		return nil
	}
	for _, n := range root.children {
		if err := visit(n); err != nil {
			return InvoiceReport{}, err
		}
	}
	if len(report.Invoices) == 0 {
		issue("unsupported", "", "no recognized invoice taxonomy facts", source)
	}
	for i := range report.Invoices {
		if err := ctx.Err(); err != nil {
			return InvoiceReport{}, err
		}
		inv := &report.Invoices[i]
		var primaryRefs, secondaryRefs []string
		seenRef := map[string]bool{}
		for _, f := range inv.Facts {
			if f.Context == "" || seenRef[f.Context] || contexts[f.Context] == nil {
				continue
			}
			seenRef[f.Context] = true
			if len(dimensions[f.Context]) == 0 && !itemContexts[inv.Standard+"\x00"+f.Context] {
				primaryRefs = append(primaryRefs, f.Context)
			} else {
				secondaryRefs = append(secondaryRefs, f.Context)
			}
		}
		orderedRefs := append(primaryRefs, secondaryRefs...)
		for _, ref := range orderedRefs {
			if !assignedContexts[ref] {
				inv.Facts = append(inv.Facts, unknown[ref]...)
				assignedContexts[ref] = true
			}
		}
		for _, ref := range orderedRefs {
			n := contexts[ref]
			s := source
			s.NodePath += n.path
			inv.Facts = append(inv.Facts, InvoiceFact{Name: "context", Namespace: invoiceXBRL, Context: ref, Value: string(n.inner), Source: s, Dimensions: dimensions[ref]})
		}
		used := map[string]bool{}
		for _, f := range inv.Facts {
			if f.Unit != "" {
				used[f.Unit] = true
			}
		}
		ids := make([]string, 0, len(used))
		for id := range used {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if n := units[id]; n != nil {
				s := source
				s.NodePath += n.path
				inv.Facts = append(inv.Facts, InvoiceFact{Name: "unit", Namespace: invoiceXBRL, Unit: id, Value: unitValues[id], Source: s})
			}
		}
		invoiceAmountCheck(inv, &report, source)
	}
	for _, fact := range unknownOrder {
		if err := ctx.Err(); err != nil {
			return InvoiceReport{}, err
		}
		if !assignedContexts[fact.Context] {
			report.UnassignedFacts = append(report.UnassignedFacts, fact)
		}
	}
	return report, nil
}

// invoiceXMLField 映射官方分类标准明确规定的概念
// 入参: inv 发票, kind 标准类别, name 概念名
// 返回: *string 对应字段地址，未映射时为空
func invoiceXMLField(inv *Invoice, kind, name string) *string {
	if kind == "inv" {
		switch name {
		case "NumberOfInvoice":
			return &inv.Number
		case "CodeOfInvoice":
			return &inv.Code
		case "DateOfIssue":
			return &inv.Date
		case "TotalAmountExcludingTax":
			return &inv.Amount
		case "TotalTaxAmount":
			return &inv.TaxAmount
		case "TaxIncludedAmountInFigures":
			return &inv.TotalAmount
		case "TaxpayerIdentificationNumberUnifiedSocialCreditCodeOfPurchaser":
			return &inv.Buyer.TaxID
		case "TaxpayerIdentificationNumberUnifiedSocialCreditCodeOfSeller":
			return &inv.Seller.TaxID
		}
	}
	if kind == "einv" {
		switch name {
		case "InvoiceNumber":
			return &inv.Number
		case "SellerName":
			return &inv.Seller.Name
		case "SellerIdNum":
			return &inv.Seller.TaxID
		case "SellerAddr":
			return &inv.Seller.Address
		case "SellerTelNum":
			return &inv.Seller.Phone
		case "SellerBankName":
			return &inv.Seller.Bank
		case "SellerBankAccNum":
			return &inv.Seller.Account
		case "BuyerName":
			return &inv.Buyer.Name
		case "BuyerIdNum":
			return &inv.Buyer.TaxID
		case "BuyerAddr":
			return &inv.Buyer.Address
		case "BuyerTelNum":
			return &inv.Buyer.Phone
		case "BuyerBankName":
			return &inv.Buyer.Bank
		case "BuyerBankAccNum":
			return &inv.Buyer.Account
		case "TotalAmWithoutTax":
			return &inv.Amount
		case "TotalTaxAm":
			return &inv.TaxAmount
		case "TotalTax-includedAmount":
			return &inv.TotalAmount
		case "RequestTime":
			return &inv.Date
		case "Remark":
			return &inv.Remarks
		}
		return nil
	}
	if kind == "atr" {
		switch name {
		case "ElectronicInvoiceAirTransportReceiptNumber":
			return &inv.Number
		case "IssueDate":
			return &inv.Date
		case "VatTaxAmount":
			return &inv.TaxAmount
		case "TotalAmount":
			return &inv.TotalAmount
		case "IssuingStatus":
			return &inv.Status
		}
	}
	if kind == "rai" {
		switch name {
		case "ElectronicInvoiceRailwayETicketNumber":
			return &inv.Number
		case "DateOfIssue":
			return &inv.Date
		case "TotalAmountExcludingTax":
			return &inv.Amount
		case "TaxAmount":
			return &inv.TaxAmount
		case "Fare":
			return &inv.TotalAmount
		case "UnifiedSocialCreditCodeOfSeller":
			return &inv.Seller.TaxID
		}
	}
	switch name {
	case "NameOfPurchaser":
		return &inv.Buyer.Name
	case "NameOfSeller":
		return &inv.Seller.Name
	case "UnifiedSocialCreditCodeOfPurchaser":
		if kind == "atr" || kind == "rai" {
			return &inv.Buyer.TaxID
		}
	case "Remarks":
		if kind == "inv" || kind == "rai" {
			return &inv.Remarks
		}
	}
	return nil
}

// invoiceSortedNodes 为定义生成稳定顺序
// 入参: nodes 定义索引
// 返回: []string 排序后的标识
func invoiceSortedNodes(nodes map[string]*invoiceXMLNode) []string {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// invoiceQName 依据节点处的前缀绑定展开QName
// 入参: n 节点, value QName文本
// 返回: xml.Name 展开名称, bool 有效标志
func invoiceQName(n *invoiceXMLNode, value string) (xml.Name, bool) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	prefix, local := "", value
	if len(parts) > 2 || value == "" {
		return xml.Name{}, false
	}
	if len(parts) == 2 {
		prefix, local = parts[0], parts[1]
		if prefix == "" {
			return xml.Name{}, false
		}
	}
	if !invoiceNCName(local) || prefix != "" && !invoiceNCName(prefix) {
		return xml.Name{}, false
	}
	ns, ok := n.ns[prefix]
	if prefix != "" && (!ok || ns == "") {
		return xml.Name{}, false
	}
	return xml.Name{Space: ns, Local: local}, true
}

// invoiceNCName 验证XML 1.0不含冒号的名称
// 入参: name 名称文本
// 返回: bool 是否满足NCName词法
func invoiceNCName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		start := r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= 0xc0 && r <= 0xd6 || r >= 0xd8 && r <= 0xf6 || r >= 0xf8 && r <= 0x2ff || r >= 0x370 && r <= 0x37d || r >= 0x37f && r <= 0x1fff || r >= 0x200c && r <= 0x200d || r >= 0x2070 && r <= 0x218f || r >= 0x2c00 && r <= 0x2fef || r >= 0x3001 && r <= 0xd7ff || r >= 0xf900 && r <= 0xfdcf || r >= 0xfdf0 && r <= 0xfffd || r >= 0x10000 && r <= 0xeffff
		if !start && (i == 0 || !(r == '-' || r == '.' || r >= '0' && r <= '9' || r == 0xb7 || r >= 0x300 && r <= 0x36f || r >= 0x203f && r <= 0x2040)) {
			return false
		}
	}
	return true
}

// invoicePeriodTime 解析标准期间的日期或日期时间
// 入参: value 日期文本
// 返回: time.Time 时刻, bool 有效标志
func invoicePeriodTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		return time.Time{}, false
	}
	if len(value) >= 6 {
		suffix := value[len(value)-6:]
		if (suffix[0] == '+' || suffix[0] == '-') && suffix[3] == ':' {
			h, e := strconv.Atoi(suffix[1:3])
			m, e2 := strconv.Atoi(suffix[4:])
			if e != nil || e2 != nil || suffix[3] != ':' || h > 14 || m > 59 || h == 14 && m != 0 {
				return time.Time{}, false
			}
		}
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02Z07:00", time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, value); err == nil && t.Year() > 0 {
			return t, true
		}
	}
	return time.Time{}, false
}

// invoiceExpanded 返回不依赖前缀的名称表示
// 入参: name 展开名称
// 返回: string Clark名称
func invoiceExpanded(name xml.Name) string { return "{" + name.Space + "}" + name.Local }

// invoiceCanonicalXML 以展开名称序列化类型化维度，保留混合文本顺序
// 入参: n 类型化维度域元素
// 返回: string 不依赖输入元素前缀的XML
func invoiceCanonicalXML(n *invoiceXMLNode) string {
	var b bytes.Buffer
	e := xml.NewEncoder(&b)
	var walk func(*invoiceXMLNode)
	walk = func(c *invoiceXMLNode) {
		a := make([]xml.Attr, 0, len(c.attrs))
		for _, attr := range c.attrs {
			if attr.Name.Space != "xmlns" && attr.Name != (xml.Name{Local: "xmlns"}) {
				a = append(a, attr)
			}
		}
		sort.Slice(a, func(i, j int) bool {
			if a[i].Name.Space != a[j].Name.Space {
				return a[i].Name.Space < a[j].Name.Space
			}
			return a[i].Name.Local < a[j].Name.Local
		})
		start := xml.StartElement{Name: c.name, Attr: a}
		_ = e.EncodeToken(start)
		for _, p := range c.parts {
			if p.child != nil {
				walk(p.child)
			} else {
				_ = e.EncodeToken(xml.CharData(p.text))
			}
		}
		_ = e.EncodeToken(start.End())
	}
	walk(n)
	_ = e.Flush()
	return b.String()
}

// invoiceChildren 按展开名称选择直接子节点
// 入参: n 父节点, space 命名空间, local 本地名
// 返回: []*invoiceXMLNode 匹配节点
func invoiceChildren(n *invoiceXMLNode, space, local string) []*invoiceXMLNode {
	var result []*invoiceXMLNode
	for _, c := range n.children {
		if c.name == (xml.Name{Space: space, Local: local}) {
			result = append(result, c)
		}
	}
	return result
}

// invoiceContext 校验主体、期间及维度，不把不同上下文合并
// 入参: n 上下文节点
// 返回: []InvoiceDimension 维度, bool 有效标志
func invoiceContext(n *invoiceXMLNode) ([]InvoiceDimension, bool) {
	meta, valid := invoiceContextInfo(n)
	return meta.dims, valid
}

// invoiceContextInfo 校验主体、期间及维度并提取规范化归并键
// 入参: n 上下文节点
// 返回: invoiceContextMeta 上下文元数据, bool 有效标志
func invoiceContextInfo(n *invoiceXMLNode) (invoiceContextMeta, bool) {
	entities, periods := invoiceChildren(n, invoiceXBRL, "entity"), invoiceChildren(n, invoiceXBRL, "period")
	if len(entities) != 1 || len(periods) != 1 {
		return invoiceContextMeta{}, false
	}
	ids := invoiceChildren(entities[0], invoiceXBRL, "identifier")
	if len(ids) != 1 {
		return invoiceContextMeta{}, false
	}
	idText, scheme := strings.TrimSpace(ids[0].text.String()), strings.TrimSpace(invoiceXMLAttr(ids[0], "", "scheme"))
	if idText == "" || scheme == "" {
		return invoiceContextMeta{}, false
	}
	p := periods[0]
	instant, start, end, forever := invoiceChildren(p, invoiceXBRL, "instant"), invoiceChildren(p, invoiceXBRL, "startDate"), invoiceChildren(p, invoiceXBRL, "endDate"), invoiceChildren(p, invoiceXBRL, "forever")
	if !((len(instant) == 1 && len(p.children) == 1 && strings.TrimSpace(instant[0].text.String()) != "") || (len(start) == 1 && len(end) == 1 && len(p.children) == 2 && strings.TrimSpace(start[0].text.String()) != "" && strings.TrimSpace(end[0].text.String()) != "") || (len(forever) == 1 && len(p.children) == 1)) {
		return invoiceContextMeta{}, false
	}
	periodKey := "F"
	if len(instant) == 1 {
		t, ok := invoicePeriodTime(instant[0].text.String())
		if !ok {
			return invoiceContextMeta{}, false
		}
		periodKey = "I:" + t.UTC().Format(time.RFC3339Nano)
	}
	if len(start) == 1 {
		a, ok := invoicePeriodTime(start[0].text.String())
		b, ok2 := invoicePeriodTime(end[0].text.String())
		if !ok || !ok2 || a.After(b) {
			return invoiceContextMeta{}, false
		}
		periodKey = "D:" + a.UTC().Format(time.RFC3339Nano) + "~" + b.UTC().Format(time.RFC3339Nano)
	}
	var dims []InvoiceDimension
	valid := true
	seen := map[xml.Name]bool{}
	var walk func(*invoiceXMLNode)
	walk = func(c *invoiceXMLNode) {
		if c.name.Space == invoiceDimension && (c.name.Local == "explicitMember" || c.name.Local == "typedMember") {
			name, ok := invoiceQName(c, invoiceXMLAttr(c, "", "dimension"))
			if !ok || name.Space == "" || seen[name] {
				valid = false
				return
			}
			seen[name] = true
			value := ""
			if c.name.Local == "explicitMember" {
				v, ok := invoiceQName(c, c.text.String())
				if !ok || v.Space == "" || len(c.children) != 0 {
					valid = false
					return
				}
				value = invoiceExpanded(v)
			} else {
				if len(c.children) != 1 {
					valid = false
					return
				}
				value = invoiceExpanded(c.children[0].name) + ":" + invoiceCanonicalXML(c.children[0])
			}
			dims = append(dims, InvoiceDimension{Name: name.Local, Namespace: name.Space, Value: value})
		}
		for _, child := range c.children {
			walk(child)
		}
	}
	walk(n)
	if !valid {
		return invoiceContextMeta{}, false
	}
	var subParts []string
	for _, seg := range append(invoiceChildren(entities[0], invoiceXBRL, "segment"), invoiceChildren(n, invoiceXBRL, "scenario")...) {
		for _, child := range seg.children {
			if child.name.Space != invoiceDimension {
				subParts = append(subParts, invoiceCanonicalXML(child))
			}
		}
	}
	var dimParts []string
	if len(dims) > 0 {
		sortedDims := slices.Clone(dims)
		sort.Slice(sortedDims, func(i, j int) bool {
			if sortedDims[i].Namespace != sortedDims[j].Namespace {
				return sortedDims[i].Namespace < sortedDims[j].Namespace
			}
			return sortedDims[i].Name < sortedDims[j].Name
		})
		for _, d := range sortedDims {
			dimParts = append(dimParts, "{"+d.Namespace+"}"+d.Name+"="+d.Value)
		}
	}
	dimParts = append(dimParts, subParts...)
	return invoiceContextMeta{
		baseKey:       scheme + "\x00" + idText + "\x00" + periodKey,
		dimKey:        strings.Join(dimParts, "\x00"),
		hasSubContext: len(dims) > 0 || len(subParts) > 0,
		dims:          dims,
	}, true
}

// invoiceUnit 校验单位结构并展开所有度量QName
// 入参: n 单位定义
// 返回: string 规范单位表达, bool 有效标志
func invoiceUnit(n *invoiceXMLNode) (string, bool) {
	measures := func(c *invoiceXMLNode) (string, bool) {
		if len(c.children) == 0 {
			return "", false
		}
		values := make([]string, 0, len(c.children))
		for _, m := range c.children {
			if m.name != (xml.Name{Space: invoiceXBRL, Local: "measure"}) || len(m.children) > 0 {
				return "", false
			}
			q, ok := invoiceQName(m, m.text.String())
			if !ok || q.Space == "" {
				return "", false
			}
			values = append(values, invoiceExpanded(q))
		}
		sort.Strings(values)
		return strings.Join(values, "*"), true
	}
	if len(n.children) == 1 && n.children[0].name == (xml.Name{Space: invoiceXBRL, Local: "divide"}) {
		d := n.children[0]
		num, den := invoiceChildren(d, invoiceXBRL, "unitNumerator"), invoiceChildren(d, invoiceXBRL, "unitDenominator")
		if len(num) != 1 || len(den) != 1 || len(d.children) != 2 {
			return "", false
		}
		a, ok := measures(num[0])
		b, ok2 := measures(den[0])
		return "(" + a + ")/(" + b + ")", ok && ok2
	}
	return measures(n)
}

// invoiceNumeric 判断已核实标准中的十进制概念
// 入参: kind 标准类别, name 概念名称
// 返回: bool 是否为数值概念
func invoiceNumeric(kind, name string) bool {
	if name == "RecordedAmount" {
		return true
	}
	if (kind == "inv" || kind == "einv") && name == "AmountOfTransferredOutInputVat" {
		return true
	}
	if kind == "inv" {
		switch name {
		case "TotalAmountExcludingTax", "TotalTaxAmount", "TaxIncludedAmountInFigures", "QuantityOfGoodsOrServices", "UnitPriceOfGoodsOrServices", "AmountExcludingTax", "TaxAmount":
			return true
		}
	}
	if kind == "einv" {
		switch name {
		case "TotalAmWithoutTax", "TotalTaxAm", "TotalTax-includedAmount", "Quantity", "UnPrice", "Amount", "TaxRate", "ComTaxAm":
			return true
		}
	}
	if kind == "atr" {
		switch name {
		case "Fare", "FuelSurcharge", "VatRate", "VatTaxAmount", "CivilAviationDevelopmentFund", "OtherTaxes", "TotalAmount":
			return true
		}
	}
	if kind == "rai" {
		switch name {
		case "Fare", "AmountRefunded", "FareOfOriginalRailwayTicket", "TotalAmountExcludingTax", "TaxRate", "TaxAmount":
			return true
		}
	}
	return false
}

// invoiceAccuracy 校验XBRL精度属性，precision允许零
// 入参: value 原文, nonnegative 是否要求非负整数
// 返回: bool 是否为整数或INF
func invoiceAccuracy(value string, nonnegative bool) bool {
	value = strings.TrimSpace(value)
	if value == "INF" {
		return true
	}
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	negative := value[0] == '-'
	if value[0] == '+' || negative {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	nonzero := false
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
		nonzero = nonzero || r != '0'
	}
	return !nonnegative || !negative || !nonzero
}

// invoiceMonetary 区分货币事实与数量、百分率
// 入参: kind 类别, name 概念名称
// 返回: bool 是否为已映射货币概念
func invoiceMonetary(kind, name string) bool {
	return invoiceNumeric(kind, name) && name != "QuantityOfGoodsOrServices" && name != "Quantity" && name != "UnPrice" && name != "VatRate" && name != "TaxRate"
}

// invoiceCurrencyUnit 检查单一ISO币种度量
// 入参: unit 展开的单位表示
// 返回: bool 是否为三位大写币种度量
func invoiceCurrencyUnit(unit string) bool {
	const prefix = "{" + InvoiceCurrencyNamespace + "}"
	if !strings.HasPrefix(unit, prefix) {
		return false
	}
	code := strings.TrimPrefix(unit, prefix)
	return len(code) == 3 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' && code[2] >= 'A' && code[2] <= 'Z'
}

// invoiceItemTuple 识别明细元组，不把会计分录当作商品
// 入参: kind 标准类别, name 元组名
// 返回: bool 是否为明细
func invoiceItemTuple(kind, name string) bool {
	return kind == "atr" && name == "DetailInformationOfAirTicketTuple" || kind == "inv" && (name == "InformationOfTaxableGoodsOrServicesDetailItemsTuple" || name == "InformationOfTollFeeDetailItemsTuple")
}

// invoiceKnownItemFact 判断概念是否属于分类标准中的明细行专属字段
// 入参: kind 标准类别, name 概念名
// 返回: bool 是否为明细字段
func invoiceKnownItemFact(kind, name string) bool {
	if invoiceItemField(&InvoiceItem{}, kind, name) != nil {
		return true
	}
	switch kind {
	case "inv":
		switch name {
		case "NatureOfInvoiceLine", "PlateNumber", "TypesOfVehicle", "EntranceDate", "ExitDate":
			return true
		}
	case "atr":
		switch name {
		case "DepartureStation", "DestinationStation", "FlightSegment", "Carrier", "Flight", "Class", "CarrierDate", "DepartureTime", "FareBasis", "EffectiveDate", "ExpirationDate", "FreeBaggageAllowance":
			return true
		}
	}
	return false
}

// invoiceItemField 映射增值税及数电发票商品明细公共字段
// 入参: item 明细, kind 类别, name 概念名
// 返回: *string 字段地址，其他明细仅保留事实
func invoiceItemField(item *InvoiceItem, kind, name string) *string {
	if kind == "einv" {
		switch name {
		case "ItemName":
			return &item.Name
		case "SpecMod":
			return &item.Specification
		case "MeaUnits":
			return &item.Unit
		case "Quantity":
			return &item.Quantity
		case "UnPrice":
			return &item.UnitPrice
		case "Amount":
			return &item.Amount
		case "TaxRate":
			return &item.TaxRate
		case "ComTaxAm":
			return &item.TaxAmount
		}
		return nil
	}
	if kind != "inv" {
		return nil
	}
	switch name {
	case "NameOfTaxableGoodsOrServices", "NameOfTollFeeItems":
		return &item.Name
	case "Specification":
		return &item.Specification
	case "UnitOfGoodsOrServices":
		return &item.Unit
	case "QuantityOfGoodsOrServices":
		return &item.Quantity
	case "UnitPriceOfGoodsOrServices":
		return &item.UnitPrice
	case "AmountExcludingTax":
		return &item.Amount
	case "TaxRate":
		return &item.TaxRate
	case "TaxAmount":
		return &item.TaxAmount
	}
	return nil
}

// invoiceAmountCheck 精确检查明确的价税合计关系，不推算航空费用
// 入参: inv 发票, report 诊断目标, source 来源
func invoiceAmountCheck(inv *Invoice, report *InvoiceReport, source InvoiceSource) {
	if inv.Kind != "inv" && inv.Kind != "einv" {
		return
	}
	a, b, c := invoiceDecimal(inv.Amount), invoiceDecimal(inv.TaxAmount), invoiceDecimal(inv.TotalAmount)
	if a != nil && b != nil && c != nil && new(big.Rat).Add(a, b).Cmp(c) != 0 {
		report.Issues = append(report.Issues, InvoiceIssue{Code: "amount-mismatch", Field: "TotalAmount", Message: "amount plus tax differs from total amount", Source: source})
	}
}

// invoiceObjectReferences 识别由对象引用及对象数据构成的票面标引
// 入参: root 标引根节点
// 返回: bool 是否为纯对象映射
func invoiceObjectReferences(root *invoiceXMLNode) bool {
	count := 0
	var walk func(*invoiceXMLNode) bool
	walk = func(n *invoiceXMLNode) bool {
		if n.name.Space == ofdNamespace2016 || n.name.Space == ofdNamespaceLegacy || n.name.Space == "ofd" || n.name.Space == invoiceStandardEDRM2016 || n.name.Space == InvoiceStandardEDRM2019 {
			if n.name.Local == "ObjectRef" {
				count++
				return len(n.children) == 0 && strings.TrimSpace(n.text.String()) != "" && invoiceXMLAttr(n, "", "PageRef") != ""
			}
			if n.name.Local == "ObjectData" {
				return len(n.children) == 0
			}
		}
		if strings.TrimSpace(n.text.String()) != "" {
			return false
		}
		for _, child := range n.children {
			if !walk(child) {
				return false
			}
		}
		return true
	}
	return walk(root) && count > 0
}

// invoiceEmptyTagRoot 识别不含子元素与文本的空电子发票标引壳
// 入参: root 标引根节点
// 返回: bool 是否为空电子发票标引壳
func invoiceEmptyTagRoot(root *invoiceXMLNode) bool {
	return root.name.Local == "eInvoice" && (root.name.Space == "" || root.name.Space == invoiceStandardEDRM2016 || root.name.Space == InvoiceStandardEDRM2019) && len(root.children) == 0 && strings.TrimSpace(root.text.String()) == ""
}

// invoiceReferenceRoot 识别OFD及电子发票对象引用标引根节点，不推断其他业务命名空间
// 入参: root XML根节点
// 返回: bool 是否为对象引用发票标引
func invoiceReferenceRoot(root *invoiceXMLNode) bool {
	switch root.name.Space {
	case ofdNamespace2016:
		return root.name.Local == "root" && invoiceObjectReferences(root)
	case invoiceStandardEDRM2016, InvoiceStandardEDRM2019:
		return (root.name.Local == "eInvoice" || root.name.Local == "root") && invoiceObjectReferences(root)
	default:
		return false
	}
}

// invoicePlainXML 解析数电发票0.3及2019版电子发票XML
// 入参: ctx 上下文, root 根节点, source 来源
// 返回: InvoiceReport 发票及诊断, error 取消错误
func invoicePlainXML(ctx context.Context, root *invoiceXMLNode, source InvoiceSource) (InvoiceReport, error) {
	report := InvoiceReport{}
	inv := Invoice{Kind: "einv", Standard: InvoiceStandardEInvoice, Version: "0.3", Sources: []InvoiceSource{source}}
	namespace, itemPath := "", "/EInvoice/EInvoiceData/IssuItemInformation"
	if root.name.Local == "eInvoice" {
		if invoiceXMLAttr(root, "", "Version") != "1.0" || len(invoiceChildren(root, InvoiceStandardEDRM2019, "InvoiceNo")) == 0 {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "unsupported", Message: "unsupported eInvoice namespace or version", Source: source})
			return report, nil
		}
		inv.Kind, inv.Standard, inv.Version = "inv", InvoiceStandardEDRM2019, "1.0"
		namespace, itemPath = InvoiceStandardEDRM2019, "/eInvoice/GoodsInfos/GoodsInfo"
	} else {
		headers := invoiceChildren(root, "", "Header")
		if len(headers) != 1 {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "unsupported", Message: "EInvoice requires a unique versioned Header", Source: source})
			return report, nil
		}
		versions := invoiceChildren(headers[0], "", "Version")
		if len(versions) != 1 || strings.TrimSpace(versions[0].text.String()) != "0.3" {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "unsupported", Field: "Version", Message: "unsupported EInvoice version", Source: source})
			return report, nil
		}
	}
	seen := map[string]InvoiceFact{}
	blocked := map[string]bool{}
	var walk func(*invoiceXMLNode, string, *InvoiceItem) error
	walk = func(n *invoiceXMLNode, path string, item *InvoiceItem) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path += "/" + n.name.Local
		if n != root && n.name.Space != namespace {
			path = "!" + path
		}
		if path == itemPath {
			inv.Items = append(inv.Items, InvoiceItem{})
			item = &inv.Items[len(inv.Items)-1]
		}
		if len(n.children) > 0 {
			for _, c := range n.children {
				if err := walk(c, path, item); err != nil {
					return err
				}
			}
			return nil
		}
		s := source
		s.NodePath += n.path
		f := InvoiceFact{Name: n.name.Local, Namespace: n.name.Space, Value: n.text.String(), Source: s, Nil: invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil") == "true" || invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil") == "1"}
		inv.Facts = append(inv.Facts, f)
		if item != nil {
			item.Fields = append(item.Fields, f)
		}
		nilValue := invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil")
		if invoiceXMLHasAttr(n, XMLSchemaInstanceNamespace, "nil") && nilValue != "true" && nilValue != "false" && nilValue != "1" && nilValue != "0" {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-value", Field: f.Name, Message: "invalid xsi:nil", Source: s})
			return nil
		}
		dst, numeric := invoicePlainField(&inv, item, path)
		if inv.Standard == InvoiceStandardEDRM2019 {
			dst, numeric = invoiceEDRMField(&inv, item, path)
		}
		if dst == nil {
			return nil
		}
		if f.Nil && strings.TrimSpace(f.Value) != "" {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-value", Field: f.Name, Message: "nil fact contains a value", Source: s})
			return nil
		}
		if numeric && (inv.Standard == InvoiceStandardEDRM2019 || inv.Standard == InvoiceStandardEInvoice) && strings.TrimSpace(f.Value) == "" && !f.Nil {
			return nil
		}
		if inv.Standard == InvoiceStandardEInvoice && path == "/EInvoice/EInvoiceData/IssuItemInformation/TaxRate" {
			if !f.Nil && !invoiceEInvoiceTaxRateValid(f.Value) {
				report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-decimal", Field: f.Name, Message: "invalid tax rate lexical value", Source: s})
				return nil
			}
		} else if numeric && !f.Nil && invoiceDecimal(f.Value) == nil {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-decimal", Field: f.Name, Message: "invalid decimal lexical value", Source: s})
			return nil
		}
		key := path
		if item != nil {
			key = n.parent.path + "/" + n.name.Local
		}
		if old, ok := seen[key]; ok {
			if old.Nil != f.Nil || old.Value != f.Value && !(numeric && invoiceDecimalEqual(old.Value, f.Value)) {
				blocked[key] = true
				*dst = ""
				report.Issues = append(report.Issues, InvoiceIssue{Code: "conflict", Field: f.Name, Message: "conflicting values at the same official field path", Source: s})
			}
			return nil
		}
		seen[key] = f
		if !blocked[key] && !f.Nil {
			*dst = f.Value
		}
		return nil
	}
	if err := walk(root, "", nil); err != nil {
		return InvoiceReport{}, err
	}
	if inv.Standard == InvoiceStandardEDRM2019 && inv.Date != "" {
		if date, err := time.Parse("2006年01月02日", inv.Date); err == nil {
			inv.Date = date.Format("2006-01-02")
		}
	}
	invoiceAmountCheck(&inv, &report, source)
	report.Invoices = append(report.Invoices, inv)
	return report, nil
}

// invoiceEDRMField 按2019版电子发票路径映射公共字段，合并信息保留原始事实
// 入参: inv 发票, item 当前明细, path 命名空间已校验的路径
// 返回: *string 字段地址, bool 数值标志
func invoiceEDRMField(inv *Invoice, item *InvoiceItem, path string) (*string, bool) {
	switch path {
	case "/eInvoice/InvoiceCode":
		return &inv.Code, false
	case "/eInvoice/InvoiceNo":
		return &inv.Number, false
	case "/eInvoice/IssueDate":
		return &inv.Date, false
	case "/eInvoice/Buyer/BuyerName":
		return &inv.Buyer.Name, false
	case "/eInvoice/Buyer/BuyerTaxID":
		return &inv.Buyer.TaxID, false
	case "/eInvoice/Seller/SellerName":
		return &inv.Seller.Name, false
	case "/eInvoice/Seller/SellerTaxID":
		return &inv.Seller.TaxID, false
	case "/eInvoice/TaxExclusiveTotalAmount":
		return &inv.Amount, true
	case "/eInvoice/TaxTotalAmount":
		return &inv.TaxAmount, true
	case "/eInvoice/TaxInclusiveTotalAmount":
		return &inv.TotalAmount, true
	case "/eInvoice/Note":
		return &inv.Remarks, false
	}
	if item != nil && strings.HasPrefix(path, "/eInvoice/GoodsInfos/GoodsInfo/") {
		switch strings.TrimPrefix(path, "/eInvoice/GoodsInfos/GoodsInfo/") {
		case "Item":
			return &item.Name, false
		case "Specification":
			return &item.Specification, false
		case "MeasurementDimension":
			return &item.Unit, false
		case "Quantity":
			return &item.Quantity, true
		case "Price":
			return &item.UnitPrice, true
		case "Amount":
			return &item.Amount, true
		case "TaxScheme":
			return &item.TaxRate, false
		case "TaxAmount":
			return &item.TaxAmount, true
		}
	}
	return nil, false
}

// invoicePlainField 按官方样例中的完整路径映射字段
// 入参: inv 发票, item 当前明细, path 完整无命名空间路径
// 返回: *string 字段地址, bool 数值标志
func invoicePlainField(inv *Invoice, item *InvoiceItem, path string) (*string, bool) {
	switch path {
	case "/EInvoice/TaxSupervisionInfo/InvoiceNumber":
		return &inv.Number, false
	case "/EInvoice/TaxSupervisionInfo/IssueTime":
		return &inv.Date, false
	case "/EInvoice/EInvoiceData/SellerInformation/SellerName":
		return &inv.Seller.Name, false
	case "/EInvoice/EInvoiceData/SellerInformation/SellerIdNum":
		return &inv.Seller.TaxID, false
	case "/EInvoice/EInvoiceData/SellerInformation/SellerAddr":
		return &inv.Seller.Address, false
	case "/EInvoice/EInvoiceData/SellerInformation/SellerTelNum":
		return &inv.Seller.Phone, false
	case "/EInvoice/EInvoiceData/SellerInformation/SellerBankName":
		return &inv.Seller.Bank, false
	case "/EInvoice/EInvoiceData/SellerInformation/SellerBankAccNum":
		return &inv.Seller.Account, false
	case "/EInvoice/EInvoiceData/BuyerInformation/BuyerName":
		return &inv.Buyer.Name, false
	case "/EInvoice/EInvoiceData/BuyerInformation/BuyerIdNum":
		return &inv.Buyer.TaxID, false
	case "/EInvoice/EInvoiceData/BuyerInformation/BuyerAddr":
		return &inv.Buyer.Address, false
	case "/EInvoice/EInvoiceData/BuyerInformation/BuyerTelNum":
		return &inv.Buyer.Phone, false
	case "/EInvoice/EInvoiceData/BuyerInformation/BuyerBankName":
		return &inv.Buyer.Bank, false
	case "/EInvoice/EInvoiceData/BuyerInformation/BuyerBankAccNum":
		return &inv.Buyer.Account, false
	case "/EInvoice/EInvoiceData/BasicInformation/TotalAmWithoutTax":
		return &inv.Amount, true
	case "/EInvoice/EInvoiceData/BasicInformation/TotalTaxAm":
		return &inv.TaxAmount, true
	case "/EInvoice/EInvoiceData/BasicInformation/TotalTax-includedAmount":
		return &inv.TotalAmount, true
	case "/EInvoice/EInvoiceData/AdditionalInformation/Remark":
		return &inv.Remarks, false
	}
	if item != nil && strings.HasPrefix(path, "/EInvoice/EInvoiceData/IssuItemInformation/") {
		switch strings.TrimPrefix(path, "/EInvoice/EInvoiceData/IssuItemInformation/") {
		case "ItemName":
			return &item.Name, false
		case "SpecMod":
			return &item.Specification, false
		case "MeaUnits":
			return &item.Unit, false
		case "Quantity":
			return &item.Quantity, true
		case "UnPrice":
			return &item.UnitPrice, true
		case "Amount":
			return &item.Amount, true
		case "TaxRate":
			return &item.TaxRate, false
		case "ComTaxAm":
			return &item.TaxAmount, true
		}
	}
	return nil, false
}

// invoiceEInvoiceTaxRateValid 校验数电票税率字段，兼容数值、百分比、免税、不征税、星号与空值
// 入参: value 税率文本
// 返回: bool 是否合法
func invoiceEInvoiceTaxRateValid(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "免税" || trimmed == "不征税" || trimmed == "***" {
		return true
	}
	if strings.HasSuffix(trimmed, "%") {
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "%"))
	}
	return invoiceDecimal(trimmed) != nil
}
