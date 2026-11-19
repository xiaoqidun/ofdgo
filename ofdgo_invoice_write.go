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
	"io"
	"math/big"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// InvoiceFormat 描述已适配格式，校验不代表完整XSD验证或发票验真
type InvoiceFormat struct {
	Kind     string `json:"kind"`
	Standard string `json:"standard"`
	Version  string `json:"version"`
	Read     bool   `json:"read"`
	Create   bool   `json:"create"`
	Edit     bool   `json:"edit"`
	Items    bool   `json:"items"`
}

// InvoiceFieldDescriptor 发票字段描述
// Item为从0开始的明细索引，非明细字段为-1
// Type为text、decimal、date或datetime，Editable表示字段支持编辑，提交时仍需校验
type InvoiceFieldDescriptor struct {
	Field    string `json:"field"`
	Label    string `json:"label"`
	Group    string `json:"group"`
	Type     string `json:"type"`
	Item     int    `json:"item"`
	Value    string `json:"value"`
	Editable bool   `json:"editable"`
	Bindable bool   `json:"bindable"`
}

// InvoiceFieldChange 修改已有叶节点，Revision为源XML的SHA256十六进制摘要
// 未提供Revision时必须提供ExpectedValue，ExpectedNil缺省表示原节点非空值
// Font和FontData仅用于图元引用字段的票面字体替换，FontData优先，不改变字号
type InvoiceFieldChange struct {
	Source        InvoiceSource `json:"source"`
	Value         string        `json:"value"`
	Font          string        `json:"font,omitempty"`
	FontData      []byte        `json:"fontData,omitempty"`
	Nil           bool          `json:"nil,omitempty"`
	Delete        bool          `json:"delete,omitempty"`
	Revision      string        `json:"revision,omitempty"`
	ExpectedValue *string       `json:"expectedValue,omitempty"`
	ExpectedNil   *bool         `json:"expectedNil,omitempty"`
}

// InvoiceContext 保存调用方指定的单主体、无维度XBRL上下文
type InvoiceContext struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Scheme     string `json:"scheme"`
	Instant    string `json:"instant,omitempty"`
	StartDate  string `json:"startDate,omitempty"`
	EndDate    string `json:"endDate,omitempty"`
}

// InvoiceUnit 保存单一度量的XBRL单位
type InvoiceUnit struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// InvoiceEncodingOptions 指定XBRL定义或官方XML片段，不加载外部资源或生成税务签名
type InvoiceEncodingOptions struct {
	SchemaRef               string         `json:"schemaRef"`
	Context                 InvoiceContext `json:"context"`
	Units                   []InvoiceUnit  `json:"units"`
	HeaderXML               []byte         `json:"headerXML,omitempty"`
	SellerAuthenticationXML []byte         `json:"sellerAuthenticationXML,omitempty"`
	TaxSupervisionXML       []byte         `json:"taxSupervisionXML,omitempty"`
	TaxBureauSignatureXML   []byte         `json:"taxBureauSignatureXML,omitempty"`
}

// InvoiceFields 获取发票字段描述，原始事实通过来源补丁修改
// 入参: value 发票数据
// 返回: []InvoiceFieldDescriptor 字段描述列表
func InvoiceFields(value Invoice) []InvoiceFieldDescriptor {
	format, supported := invoiceWriteFormat(value)
	probe := Invoice{Items: []InvoiceItem{{}}}
	if value.Standard == InvoiceStandardEDRM2019 || value.Standard == ofdNamespace2016 {
		for _, path := range invoiceEDRMPaths() {
			item := &probe.Items[0]
			if value.Standard == ofdNamespace2016 {
				item = nil
			}
			if p, _ := invoiceEDRMField(&probe, item, path); p != nil {
				*p = "editable"
			}
		}
	} else if value.Standard == InvoiceStandardEInvoice {
		probe.Number, probe.Date = "editable", "editable"
		for _, group := range invoicePlainGroups() {
			for _, name := range strings.Fields(group.value) {
				if p, _ := invoicePlainField(&probe, nil, "/EInvoice/EInvoiceData/"+group.name+"/"+name); p != nil {
					*p = "editable"
				}
			}
		}
		for _, name := range strings.Fields("ItemName SpecMod MeaUnits Quantity UnPrice Amount TaxRate ComTaxAm") {
			if p, _ := invoicePlainField(&probe, &probe.Items[0], "/EInvoice/EInvoiceData/IssuItemInformation/"+name); p != nil {
				*p = "editable"
			}
		}
	} else {
		for _, name := range invoiceWriteHeader(value) {
			if p := invoiceXMLField(&probe, value.Kind, name); p != nil {
				*p = "editable"
			}
		}
		for _, name := range invoiceWriteItemNames(value.Kind) {
			if p := invoiceItemField(&probe.Items[0], value.Kind, name); p != nil {
				*p = "editable"
			}
		}
	}
	referenceFields := map[string]int{}
	referenceEditable := map[string]bool{}
	var referenceFacts []bool
	if value.Standard == ofdNamespace2016 {
		referenceFacts = invoiceReferenceEditability(value)
		for i, fact := range value.Facts {
			if binding, ok := invoiceEDRMFactBinding(value, fact); ok && binding.Item < 0 {
				referenceFields[binding.Field]++
				referenceEditable[binding.Field] = fact.Source.NodePath != "" && referenceFacts[i]
			}
		}
	}
	var result []InvoiceFieldDescriptor
	for _, field := range invoiceLayoutFields(value) {
		if strings.HasPrefix(field.field, "facts.") || strings.HasPrefix(field.field, "items.fields.") {
			continue
		}
		d := InvoiceFieldDescriptor{Field: field.field, Label: field.label, Group: "basic", Type: "text", Item: field.item, Value: field.value, Bindable: value.Standard != ofdNamespace2016}
		binding := InvoiceBinding{Field: field.field, Item: field.item}
		if field.item >= 0 {
			binding.Item = 0
			d.Group = "items"
		}
		if strings.HasPrefix(field.field, "buyer.") {
			d.Group = "buyer"
		}
		if strings.HasPrefix(field.field, "seller.") {
			d.Group = "seller"
		}
		mapped, _ := invoiceBindingValue(probe, binding)
		d.Editable = supported && format.Edit && mapped == "editable"
		if value.Standard == ofdNamespace2016 && len(value.Facts) > 0 {
			d.Editable = d.Editable && referenceFields[field.field] == 1 && referenceEditable[field.field]
		}
		switch field.field {
		case "date":
			d.Type = "date"
			if value.Kind == "einv" {
				d.Type = "datetime"
			}
		case "amount", "taxAmount", "totalAmount", "items.quantity", "items.unitPrice", "items.amount", "items.taxAmount":
			d.Type = "decimal"
		case "items.taxRate":
			if value.Standard == InvoiceStandardEInvoice {
				d.Type = "decimal"
			}
		}
		result = append(result, d)
	}
	appendFact := func(fact InvoiceFact, field string, item, index int) {
		d := InvoiceFieldDescriptor{Field: field, Label: fact.Name, Group: "facts", Type: "text", Item: item, Value: fact.Value, Bindable: value.Standard != ofdNamespace2016}
		if item >= 0 {
			d.Group = "items"
		}
		d.Editable = supported && format.Edit && fact.Source.NodePath != "" && fact.Namespace != invoiceXBRL
		if value.Standard == ofdNamespace2016 {
			d.Editable = d.Editable && item < 0 && referenceFacts[index]
		}
		if invoiceNumeric(value.Kind, fact.Name) {
			d.Type = "decimal"
		}
		if invoiceWriteDateFact(value.Kind, fact.Name) {
			d.Type = "date"
		}
		if value.Kind == "einv" && (fact.Name == "RequestTime" || fact.Name == "IssueTime") {
			d.Type = "datetime"
		}
		result = append(result, d)
	}
	for i, fact := range value.Facts {
		appendFact(fact, fmt.Sprintf("facts.%d", i), -1, i)
	}
	for i, item := range value.Items {
		for j, fact := range item.Fields {
			appendFact(fact, fmt.Sprintf("items.fields.%d", j), i, j)
		}
	}
	return result
}

// InvoiceFormats 返回独立格式能力副本，创建及编辑能力按业务版本分别声明
// 对象引用的创建及编辑限于Editor文档接口，创建必须提供Layout及字体
// 返回: 已适配格式列表
func InvoiceFormats() []InvoiceFormat {
	result := []InvoiceFormat{{Kind: "einv", Standard: InvoiceStandardEInvoice, Version: "0.3", Read: true, Create: true, Edit: true, Items: true}}
	result = append(result, InvoiceFormat{Kind: "inv", Standard: InvoiceStandardEDRM2019, Version: "1.0", Read: true, Create: true, Edit: true, Items: true})
	result = append(result, InvoiceFormat{Kind: "inv", Standard: ofdNamespace2016, Version: "1.0", Read: true, Create: true, Edit: true})
	for _, standard := range []string{InvoiceStandardVAT2023, InvoiceStandardDigital2023, InvoiceStandardAir2023, InvoiceStandardRail2023} {
		kind, version := invoiceTaxonomy(standard)
		result = append(result, InvoiceFormat{Kind: kind, Standard: standard, Version: version, Read: true, Create: true, Edit: true, Items: kind == "inv" || kind == "atr"})
	}
	return append(result, InvoiceFormat{Kind: "atr", Standard: InvoiceStandardAir2021, Version: "2021-11-30", Read: true, Create: true, Edit: true, Items: true}, InvoiceFormat{Kind: "inv", Standard: InvoiceStandardVAT2021, Version: "2021-09-30", Read: true, Create: true, Edit: true, Items: true})
}

// DecodeInvoices 提取独立XML或XBRL中的发票，不执行联网或完整XSD验证
// 入参: ctx 取消上下文，data 原始XML
// 返回: 提取结果及解析错误
func DecodeInvoices(ctx context.Context, data []byte) (InvoiceReport, error) {
	return parseInvoiceXML(ctx, data, InvoiceSource{})
}

// ValidateInvoice 检查已映射字段的词法、金额及可表达性，不替代税务业务或XSD验证
// 入参: ctx 取消上下文，value 发票
// 返回: 字段诊断及取消错误
func ValidateInvoice(ctx context.Context, value Invoice) ([]InvoiceIssue, error) {
	ctx = invoiceWriteContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := invoiceWriteLimits(ctx, value, InvoiceEncodingOptions{}); err != nil {
		return nil, err
	}
	if len(value.Items) > 100000 || len(value.Facts) > 100000 {
		return nil, fmt.Errorf("invoice exceeds structural limit")
	}
	var issues []InvoiceIssue
	add := func(code, field, message string) {
		issues = append(issues, InvoiceIssue{Code: code, Field: field, Message: message})
	}
	f, ok := invoiceWriteFormat(value)
	if !ok {
		add("unsupported", "Standard", "unsupported invoice format or version")
		return issues, nil
	}
	for _, field := range []invoiceWriteField{{"Number", value.Number}, {"Date", value.Date}, {"TotalAmount", value.TotalAmount}} {
		if value.Standard != ofdNamespace2016 && strings.TrimSpace(field.value) == "" {
			add("required", field.name, "missing invoice field")
		}
	}
	if value.Date != "" {
		date := strings.Replace(value.Date, " ", "T", 1)
		if _, ok := invoicePeriodTime(date); !ok {
			add("invalid-date", "Date", "invalid invoice date")
		}
	}
	if value.Currency != "" && !invoiceCurrencyUnit("{"+InvoiceCurrencyNamespace+"}"+value.Currency) {
		add("invalid-value", "Currency", "invalid ISO currency code")
	}
	for _, field := range []invoiceWriteField{{"Amount", value.Amount}, {"TaxAmount", value.TaxAmount}, {"TotalAmount", value.TotalAmount}} {
		if field.value != "" && invoiceDecimal(field.value) == nil {
			add("invalid-decimal", field.name, "invalid decimal lexical value")
		}
	}
	report := InvoiceReport{}
	invoiceAmountCheck(&value, &report, InvoiceSource{})
	issues = append(issues, report.Issues...)
	if !f.Items && len(value.Items) > 0 {
		add("unsupported", "Items", "format has no supported invoice item tuple")
	}
	for i, item := range value.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, field := range []invoiceWriteField{{"Quantity", item.Quantity}, {"UnitPrice", item.UnitPrice}, {"Amount", item.Amount}, {"TaxAmount", item.TaxAmount}} {
			if strings.TrimSpace(field.value) != "" && invoiceDecimal(field.value) == nil {
				add("invalid-decimal", fmt.Sprintf("Items[%d].%s", i, field.name), "invalid decimal lexical value")
			}
		}
		if value.Standard == InvoiceStandardEInvoice && !invoiceEInvoiceTaxRateValid(item.TaxRate) {
			add("invalid-decimal", fmt.Sprintf("Items[%d].TaxRate", i), "invalid decimal lexical value")
		}
	}
	if value.Kind == "inv" || value.Standard == InvoiceStandardEInvoice {
		for _, total := range []invoiceWriteField{{"Amount", value.Amount}, {"TaxAmount", value.TaxAmount}} {
			sum := new(big.Rat)
			complete := len(value.Items) > 0
			for _, item := range value.Items {
				s := item.Amount
				if total.name == "TaxAmount" {
					s = item.TaxAmount
				}
				n := invoiceDecimal(s)
				if n == nil {
					complete = false
					break
				}
				sum.Add(sum, n)
			}
			if n := invoiceDecimal(total.value); complete && n != nil && sum.Cmp(n) != 0 {
				add("amount-mismatch", total.name, "item sum differs from invoice total")
			}
		}
	}
	total := 0
	for _, field := range invoiceWriteAllValues(value) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		total += len(field.value)
		if total > invoiceXMLLimit {
			return nil, fmt.Errorf("invoice exceeds size limit")
		}
		if !invoiceWriteText(field.value) {
			add("invalid-value", field.name, "invalid XML character or oversized field")
		}
	}
	return issues, nil
}

// EncodeInvoice 从明确业务字段生成独立XML，不将提取摘要覆盖原始载荷
// 入参: ctx 取消上下文，value 发票
// 返回: 结构化XML及创建错误
func EncodeInvoice(ctx context.Context, value Invoice) ([]byte, error) {
	return EncodeInvoiceWithOptions(ctx, value, InvoiceEncodingOptions{})
}

// EncodeInvoiceWithOptions 使用调用方指定的XBRL定义创建独立结构化文件
// 入参: ctx 上下文，value 发票，options 分类标准入口、上下文及单位
// 返回: XML及创建错误
func EncodeInvoiceWithOptions(ctx context.Context, value Invoice, options InvoiceEncodingOptions) ([]byte, error) {
	ctx = invoiceWriteContext(ctx)
	if err := invoiceWriteLimits(ctx, value, options); err != nil {
		return nil, err
	}
	if value.Standard == ofdNamespace2016 {
		return nil, fmt.Errorf("object reference creation requires Editor.CreateInvoice with Layout.Font; standalone XML cannot create page objects")
	}
	issues, err := ValidateInvoice(ctx, value)
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 {
		return nil, fmt.Errorf("invoice %s: %s", issues[0].Field, issues[0].Message)
	}
	f, _ := invoiceWriteFormat(value)
	if !f.Create {
		return nil, fmt.Errorf("invoice format does not support standalone creation")
	}
	if err := invoiceWriteCreation(value); err != nil {
		return nil, err
	}
	var data []byte
	if value.Standard == InvoiceStandardEDRM2019 {
		if len(options.HeaderXML)+len(options.SellerAuthenticationXML)+len(options.TaxSupervisionXML)+len(options.TaxBureauSignatureXML) != 0 {
			return nil, fmt.Errorf("EDRM creation does not support EInvoice sections")
		}
		data, err = invoiceEncodeEDRM(value)
	} else if value.Standard == InvoiceStandardEInvoice {
		data, err = invoiceEncodePlain(value)
		if err == nil {
			data, err = invoiceWritePlainSections(ctx, data, options)
		}
	} else {
		data, err = invoiceEncodeXBRL(value, options)
	}
	if err != nil {
		return nil, err
	}
	report, err := DecodeInvoices(ctx, data)
	if err != nil {
		return nil, err
	}
	if len(report.Issues) > 0 {
		return nil, fmt.Errorf("encoded invoice: %s", report.Issues[0].Message)
	}
	if len(report.Invoices) != 1 {
		return nil, fmt.Errorf("encoded invoice has ambiguous contexts")
	}
	if err = invoiceWriteRepresented(value, report.Invoices[0]); err != nil {
		return nil, err
	}
	return data, nil
}

// PatchInvoice 精确修改已有事实叶节点，未知节点及其他字节保持不变
// 入参: ctx 上下文，data 原始XML，changes 节点修改与原值或摘要前置条件
// 返回: 修改后XML及路径、校验或取消错误
func PatchInvoice(ctx context.Context, data []byte, changes []InvoiceFieldChange) ([]byte, error) {
	ctx = invoiceWriteContext(ctx)
	report, err := DecodeInvoices(ctx, data)
	if err != nil {
		return nil, err
	}
	if len(report.Invoices) == 0 {
		return nil, fmt.Errorf("unsupported invoice XML")
	}
	_, spans, err := invoiceWriteSpans(ctx, data)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, inv := range report.Invoices {
		format, supported := invoiceWriteFormat(inv)
		if !supported || !format.Edit {
			continue
		}
		for _, f := range inv.Facts {
			if f.Namespace != invoiceXBRL {
				allowed[f.Source.NodePath] = true
			}
		}
	}
	sum := sha256.Sum256(data)
	revision := hex.EncodeToString(sum[:])
	seen := map[string]bool{}
	var patches []invoiceXMLPatch
	for _, change := range changes {
		if change.Font != "" || len(change.FontData) != 0 {
			return nil, fmt.Errorf("invoice font replacement requires object references")
		}
		path := change.Source.NodePath
		s, ok := spans[path]
		if !ok || !allowed[path] || len(s.node.children) > 0 || seen[path] {
			return nil, fmt.Errorf("missing, ambiguous or non-leaf invoice path")
		}
		seen[path] = true
		if change.Revision == "" && change.ExpectedValue == nil {
			return nil, fmt.Errorf("invoice change requires source revision or expected value")
		}
		if change.Revision != "" && change.Revision != revision {
			return nil, fmt.Errorf("stale invoice source revision")
		}
		oldNil := invoiceXMLAttr(s.node, XMLSchemaInstanceNamespace, "nil")
		isNil := oldNil == "true" || oldNil == "1"
		expectedNil := false
		if change.ExpectedNil != nil {
			expectedNil = *change.ExpectedNil
		}
		if change.ExpectedValue != nil && (*change.ExpectedValue != s.node.text.String() || expectedNil != isNil) {
			return nil, fmt.Errorf("stale invoice field value")
		}
		if !invoiceWriteText(change.Value) || change.Nil && change.Value != "" || change.Delete && (change.Nil || change.Value != "") {
			return nil, fmt.Errorf("invalid invoice field change")
		}
		if !change.Delete && change.Nil == isNil && change.Value == s.node.text.String() {
			continue
		}
		if change.Delete {
			patches = append(patches, invoiceXMLPatch{s.begin, s.end, nil})
			continue
		}
		replacement, err := invoiceWriteLeaf(s.node, change.Value, change.Nil)
		if err != nil {
			return nil, err
		}
		if change.Nil == isNil && !s.empty {
			patches = append(patches, invoiceXMLPatch{s.inner, s.close, []byte(invoiceWriteEscape(change.Value))})
		} else {
			patches = append(patches, invoiceXMLPatch{s.begin, s.end, replacement})
		}
	}
	result, err := invoiceApplyXMLPatches(ctx, data, patches)
	if err != nil {
		return nil, err
	}
	after, err := DecodeInvoices(ctx, result)
	if err != nil {
		return nil, err
	}
	if len(after.Invoices) == 0 {
		return nil, fmt.Errorf("invoice patch removes all recognized facts")
	}
	if err := invoiceWriteNewIssues(report.Issues, after.Issues); err != nil {
		return nil, err
	}
	if err := invoiceValidateXMLWrite(ctx, data, result); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateInvoiceXML 更新指定候选的公共字段和明细，保留未映射原文及其他候选
// 入参: ctx 上下文，data 原XML，invoiceIndex 候选序号，value 完整目标值
// 返回: 修改后XML及不可安全表达的修改错误
func UpdateInvoiceXML(ctx context.Context, data []byte, invoiceIndex int, value Invoice) ([]byte, error) {
	ctx = invoiceWriteContext(ctx)
	before, err := DecodeInvoices(ctx, data)
	if err != nil {
		return nil, err
	}
	if invoiceIndex < 0 || invoiceIndex >= len(before.Invoices) {
		return nil, fmt.Errorf("invoice index out of range")
	}
	old := before.Invoices[invoiceIndex]
	if reflect.DeepEqual(old, value) {
		return bytes.Clone(data), nil
	}
	if format, supported := invoiceWriteFormat(old); !supported || !format.Edit {
		return nil, fmt.Errorf("invoice format does not support editing")
	}
	if value.Kind != old.Kind || value.Standard != old.Standard || value.Version != old.Version {
		return nil, fmt.Errorf("invoice update cannot change format")
	}
	if value.Currency != old.Currency {
		return nil, fmt.Errorf("invoice update cannot change shared currency units")
	}
	oldIssues, err := ValidateInvoice(ctx, old)
	if err != nil {
		return nil, err
	}
	newIssues, err := ValidateInvoice(ctx, value)
	if err != nil {
		return nil, err
	}
	filter := func(issues []InvoiceIssue) []InvoiceIssue {
		var r []InvoiceIssue
		for _, i := range issues {
			if i.Code != "required" {
				r = append(r, i)
			}
		}
		return r
	}
	if err = invoiceWriteNewIssues(filter(oldIssues), filter(newIssues)); err != nil {
		return nil, err
	}
	if value.Standard == InvoiceStandardEDRM2019 {
		return invoiceUpdateEDRM(ctx, data, old, value)
	}
	root, spans, err := invoiceWriteSpans(ctx, data)
	if err != nil {
		return nil, err
	}
	if value.Standard == InvoiceStandardEInvoice {
		var expanded []invoiceXMLPatch
		for _, s := range spans {
			n := s.node
			if !s.empty || n.name.Space != "" || n.parent == nil {
				continue
			}
			container := n.parent == root && (n.name.Local == "EInvoiceData" || n.name.Local == "TaxSupervisionInfo")
			if n.parent.name == (xml.Name{Local: "EInvoiceData"}) && n.parent.parent == root {
				for _, g := range invoicePlainGroups() {
					container = container || g.name == n.name.Local
				}
			}
			if container {
				p, e := invoiceWriteInsert(s, nil, data)
				if e != nil {
					return nil, e
				}
				expanded = append(expanded, p)
			}
		}
		if len(expanded) > 0 {
			normalized, e := invoiceApplyXMLPatches(ctx, data, expanded)
			if e != nil {
				return nil, e
			}
			return UpdateInvoiceXML(ctx, normalized, invoiceIndex, value)
		}
	}
	if err = invoiceWriteUnmapped(old.Facts, value.Facts); err != nil {
		return nil, err
	}
	clean := value
	clean.Facts = nil
	clean.Sources = nil
	clean.Items = append([]InvoiceItem(nil), value.Items...)
	if clean.Standard != InvoiceStandardEInvoice {
		for _, name := range invoiceWriteHeader(clean) {
			previous := invoiceXMLField(&old, old.Kind, name)
			target := invoiceXMLField(&clean, clean.Kind, name)
			if previous != nil && target != nil && *previous == *target {
				*target = ""
			}
		}
	}
	for i := range clean.Items {
		clean.Items[i].Fields = nil
		if value.Standard != InvoiceStandardEInvoice && (value.Kind == "atr" || value.Kind == "inv") {
			for _, f := range value.Items[i].Fields {
				if !invoiceWriteContains(invoiceWriteItemNames(value.Kind), f.Name) {
					continue
				}
				if p := invoiceItemField(&clean.Items[i], value.Kind, f.Name); p != nil {
					if *p == "" && !f.Nil {
						continue
					}
					f.Value = *p
					if *p != "" {
						f.Nil = false
					}
				}
				f.Source = InvoiceSource{}
				f.Context = ""
				clean.Items[i].Fields = append(clean.Items[i].Fields, f)
			}
		}
	}
	if reflect.DeepEqual(old.Items, value.Items) {
		clean.Items = nil
	}
	if invoiceWriteCreation(old) == nil {
		if err = invoiceWriteCreation(value); err != nil {
			return nil, err
		}
	}
	var generated []byte
	if value.Standard == InvoiceStandardEInvoice {
		generated, err = invoiceEncodePlain(clean)
	} else {
		options, e := invoiceWriteOriginalOptions(root, old)
		if e != nil {
			return nil, e
		}
		generated, err = invoiceEncodeXBRL(clean, options)
	}
	if err != nil {
		return nil, err
	}
	generatedRoot, err := invoiceXMLTree(ctx, generated)
	if err != nil {
		return nil, err
	}
	var patches []invoiceXMLPatch
	var trailingGroups []invoiceXMLPatch
	var itemParent, generatedParent *invoiceXMLNode
	if value.Standard == InvoiceStandardEInvoice {
		if len(invoiceChildren(root, "", "EInvoiceData")) != 1 || len(invoiceChildren(root, "", "TaxSupervisionInfo")) != 1 {
			return nil, fmt.Errorf("invoice requires unique data and supervision groups")
		}
		itemParent = invoiceChildren(root, "", "EInvoiceData")[0]
		generatedParent = invoiceChildren(generatedRoot, "", "EInvoiceData")[0]
		for _, group := range append(invoicePlainGroups(), invoiceWriteField{"TaxSupervisionInfo", "InvoiceNumber IssueTime"}) {
			parent, newParent := itemParent, generatedParent
			if group.name == "TaxSupervisionInfo" {
				parent, newParent = root, generatedRoot
			}
			existing := invoiceChildren(parent, "", group.name)
			desired := invoiceChildren(newParent, "", group.name)
			if len(existing) > 1 {
				return nil, fmt.Errorf("ambiguous invoice group %s", group.name)
			}
			if len(existing) == 0 {
				if len(desired) > 0 && len(desired[0].children) > 0 {
					raw, e := invoiceWriteNode(desired[0])
					if e != nil {
						return nil, e
					}
					p, e := invoiceWriteOrderedInsert(data, spans, parent, "", group.name, strings.Fields("SellerInformation BuyerInformation BasicInformation IssuItemInformation SpecificInformation AdditionalInformation"), raw)
					if e != nil {
						return nil, e
					}
					if group.name == "AdditionalInformation" {
						trailingGroups = append(trailingGroups, p)
					} else {
						patches = append(patches, p)
					}
				}
				continue
			}
			var target *invoiceXMLNode
			if len(desired) > 0 {
				target = desired[0]
			}
			retained := invoiceXMLNode{}
			if target != nil {
				retained = *target
				retained.children = append([]*invoiceXMLNode(nil), target.children...)
			}
			for _, name := range strings.Fields(group.value) {
				path := "/EInvoice/EInvoiceData/" + group.name + "/" + name
				if group.name == "TaxSupervisionInfo" {
					path = "/EInvoice/TaxSupervisionInfo/" + name
				}
				previous, _ := invoicePlainField(&old, nil, path)
				desired, _ := invoicePlainField(&value, nil, path)
				if previous != nil && desired != nil && *previous == *desired && len(invoiceChildren(&retained, "", name)) == 0 {
					retained.children = append(retained.children, invoiceChildren(existing[0], "", name)...)
				}
			}
			ps, e := invoiceWriteMergeChildren(data, spans, existing[0], &retained, "", strings.Fields(group.value))
			if e != nil {
				return nil, e
			}
			patches = append(patches, ps...)
		}
	} else {
		itemParent, generatedParent = root, generatedRoot
		contextID := ""
		for _, f := range old.Facts {
			if f.Namespace == invoiceXBRL && f.Name == "context" {
				contextID = f.Context
				break
			}
		}
		if contextID == "" {
			return nil, fmt.Errorf("missing invoice context")
		}
		filtered := *root
		filtered.children = nil
		for _, n := range root.children {
			if invoiceXMLAttr(n, "", "contextRef") == contextID {
				filtered.children = append(filtered.children, n)
			}
		}
		for _, name := range invoiceWriteHeader(value) {
			previous := invoiceXMLField(&old, old.Kind, name)
			desired := invoiceXMLField(&value, value.Kind, name)
			if previous != nil && desired != nil && *previous == *desired {
				generatedRoot.children = append(generatedRoot.children, invoiceChildren(&filtered, value.Standard, name)...)
			}
		}
		ps, e := invoiceWriteMergeChildren(data, spans, &filtered, generatedRoot, value.Standard, invoiceWriteHeader(value))
		if e != nil {
			return nil, e
		}
		patches = append(patches, ps...)
	}
	ps, err := invoiceWriteMergeItems(ctx, data, spans, itemParent, generatedParent, old, value)
	if err != nil {
		return nil, err
	}
	patches = append(patches, ps...)
	patches = append(patches, trailingGroups...)
	result, err := invoiceApplyXMLPatches(ctx, data, patches)
	if err != nil {
		return nil, err
	}
	after, err := DecodeInvoices(ctx, result)
	if err != nil {
		return nil, err
	}
	if err = invoiceWriteNewIssues(before.Issues, after.Issues); err != nil {
		return nil, err
	}
	if len(after.Invoices) != len(before.Invoices) {
		return nil, fmt.Errorf("invoice update changed candidate contexts")
	}
	var matches []Invoice
	for _, inv := range after.Invoices {
		if inv.Kind == value.Kind && inv.Standard == value.Standard {
			if value.Standard == InvoiceStandardEInvoice {
				matches = append(matches, inv)
				continue
			}
			matched := false
			for _, f := range inv.Facts {
				if f.Name == "context" && f.Namespace == invoiceXBRL {
					for _, of := range old.Facts {
						if of.Name == "context" && of.Namespace == invoiceXBRL && of.Context == f.Context {
							matches = append(matches, inv)
							matched = true
							break
						}
					}
				}
				if matched {
					break
				}
			}
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("invoice update target is ambiguous")
	}
	if err = invoiceWriteRepresented(value, matches[0]); err != nil {
		return nil, err
	}
	if err = invoiceValidateXMLWrite(ctx, data, result); err != nil {
		return nil, err
	}
	return result, nil
}

// invoiceWriteField 保存按规范排列的字段与值
type invoiceWriteField struct{ name, value string }

// invoiceXMLSpan 保存节点原始字节边界
type invoiceXMLSpan struct {
	node                     *invoiceXMLNode
	begin, inner, close, end int
	empty                    bool
}

// invoiceXMLPatch 保存不重序列化其他节点的字节修改
type invoiceXMLPatch struct {
	begin, end int
	data       []byte
}

// invoiceWriteContext 统一空上下文
// 入参: ctx 取消上下文
// 返回: 可用上下文
func invoiceWriteContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// invoiceWriteFormat 按标准、票种和版本查找格式
// 入参: value 发票
// 返回: 格式与匹配状态
func invoiceWriteFormat(value Invoice) (InvoiceFormat, bool) {
	for _, f := range InvoiceFormats() {
		if value.Kind == f.Kind && value.Standard == f.Standard && value.Version == f.Version {
			return f, true
		}
	}
	return InvoiceFormat{}, false
}

// invoiceWriteAllValues 收集公共字段及扩展事实文本
// 入参: value 发票
// 返回: 待校验字段
func invoiceWriteAllValues(value Invoice) []invoiceWriteField {
	r := []invoiceWriteField{{"Number", value.Number}, {"Code", value.Code}, {"Date", value.Date}, {"Status", value.Status}, {"Remarks", value.Remarks}}
	for _, p := range []InvoiceParty{value.Buyer, value.Seller} {
		for _, s := range []string{p.Name, p.TaxID, p.Address, p.Phone, p.Bank, p.Account} {
			r = append(r, invoiceWriteField{"Party", s})
		}
	}
	for _, item := range value.Items {
		for _, s := range []string{item.Name, item.Specification, item.Unit, item.Quantity, item.UnitPrice, item.Amount, item.TaxRate, item.TaxAmount} {
			r = append(r, invoiceWriteField{"Items", s})
		}
		for _, f := range item.Fields {
			r = append(r, invoiceWriteField{f.Name, f.Value})
		}
	}
	for _, f := range value.Facts {
		r = append(r, invoiceWriteField{f.Name, f.Value})
	}
	return r
}

// invoiceWriteText 检查XML文本且不替换非法字符
// 入参: value 原始文本
// 返回: 文本是否合法
func invoiceWriteText(value string) bool {
	if len(value) > invoiceXMLLimit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r != 9 && r != 10 && r != 13 && (r < 32 || r == 0xfffe || r == 0xffff) {
			return false
		}
	}
	return true
}

// invoiceWriteHeader 返回明确映射的规范字段顺序
// 入参: value 发票
// 返回: 字段名列表
func invoiceWriteHeader(value Invoice) []string {
	switch value.Kind {
	case "inv":
		return strings.Fields("CodeOfInvoice NumberOfInvoice DateOfIssue NameOfPurchaser TaxpayerIdentificationNumberUnifiedSocialCreditCodeOfPurchaser TotalAmountExcludingTax TotalTaxAmount TaxIncludedAmountInFigures NameOfSeller TaxpayerIdentificationNumberUnifiedSocialCreditCodeOfSeller Remarks")
	case "einv":
		return strings.Fields("InvoiceNumber SellerName SellerIdNum SellerAddr SellerTelNum SellerBankName SellerBankAccNum BuyerName BuyerIdNum BuyerAddr BuyerTelNum BuyerBankName BuyerBankAccNum TotalAmWithoutTax RequestTime TotalTaxAm TotalTax-includedAmount Remark")
	case "atr":
		return strings.Fields("ElectronicInvoiceAirTransportReceiptNumber IssuingStatus VatTaxAmount TotalAmount IssueDate NameOfPurchaser UnifiedSocialCreditCodeOfPurchaser NameOfSeller")
	case "rai":
		return strings.Fields("ElectronicInvoiceRailwayETicketNumber DateOfIssue NameOfPurchaser UnifiedSocialCreditCodeOfPurchaser TotalAmountExcludingTax TaxAmount Fare NameOfSeller UnifiedSocialCreditCodeOfSeller Remarks")
	}
	return nil
}

// invoiceWriteItemNames 返回元组内部字段顺序
// 入参: kind 票种
// 返回: 规范字段列表
func invoiceWriteItemNames(kind string) []string {
	if kind == "atr" {
		return strings.Fields("DepartureStation DestinationStation FlightSegment Carrier Flight Class CarrierDate DepartureTime FareBasis EffectiveDate ExpirationDate FreeBaggageAllowance")
	}
	return strings.Fields("NatureOfInvoiceLine NameOfTaxableGoodsOrServices Specification UnitOfGoodsOrServices QuantityOfGoodsOrServices UnitPriceOfGoodsOrServices AmountExcludingTax TaxRate TaxAmount")
}

// invoiceWriteTuple 返回已支持的明细元组名称
// 入参: kind 票种
// 返回: 元组名
func invoiceWriteTuple(kind string) string {
	if kind == "atr" {
		return "DetailInformationOfAirTicketTuple"
	}
	return "InformationOfTaxableGoodsOrServicesDetailItemsTuple"
}

// invoiceWriteEscape 转义已校验的XML值
// 入参: value 文本
// 返回: XML安全文本
func invoiceWriteEscape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

// invoiceWriteLimits 在构造字段列表和XML前限制节点数及转义后的保守空间上界
// 入参: ctx 上下文，value 发票，options 显式定义和XML片段
// 返回: 取消或限额错误
func invoiceWriteLimits(ctx context.Context, value Invoice, options InvoiceEncodingOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(value.Items) > 100000 || len(value.Facts) > 100000 || len(options.Units) > 256 || len(options.SchemaRef) > 4096 || len(options.Context.Scheme) > 4096 || len(options.Context.Identifier) > 64<<10 {
		return fmt.Errorf("invoice exceeds structural limit")
	}
	total := 0
	charge := func(values ...string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if total > invoiceXMLLimit-512 {
			return fmt.Errorf("invoice exceeds encoded size limit")
		}
		total += 512
		for _, s := range values {
			if len(s) > (invoiceXMLLimit-total)/6 {
				return fmt.Errorf("invoice exceeds encoded size limit")
			}
			total += 6 * len(s)
		}
		return nil
	}
	for _, s := range []string{value.Kind, value.Standard, value.Version, value.Number, value.Code, value.Date, value.Currency, value.Status, value.Amount, value.TaxAmount, value.TotalAmount, value.Remarks} {
		if err := charge(s); err != nil {
			return err
		}
	}
	for _, party := range []InvoiceParty{value.Buyer, value.Seller} {
		if err := charge(party.Name, party.TaxID, party.Address, party.Phone, party.Bank, party.Account); err != nil {
			return err
		}
	}
	facts := func(fields []InvoiceFact) error {
		if len(fields) > 100000 {
			return fmt.Errorf("invoice exceeds structural limit")
		}
		for _, f := range fields {
			if err := charge(f.Name, f.Namespace, f.Value, f.Context, f.Unit, f.Decimals, f.Precision); err != nil {
				return err
			}
			if len(f.Dimensions) > 128 {
				return fmt.Errorf("invoice exceeds structural limit")
			}
			for _, d := range f.Dimensions {
				if err := charge(d.Name, d.Namespace, d.Value); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := facts(value.Facts); err != nil {
		return err
	}
	for _, item := range value.Items {
		for _, text := range []string{item.Name, item.Specification, item.Unit, item.Quantity, item.UnitPrice, item.Amount, item.TaxRate, item.TaxAmount} {
			if err := charge(text); err != nil {
				return err
			}
		}
		if err := facts(item.Fields); err != nil {
			return err
		}
	}
	if err := charge(options.SchemaRef, options.Context.ID, options.Context.Identifier, options.Context.Scheme, options.Context.Instant, options.Context.StartDate, options.Context.EndDate); err != nil {
		return err
	}
	for _, unit := range options.Units {
		if len(unit.Namespace) > 4096 {
			return fmt.Errorf("invoice unit namespace exceeds limit")
		}
		if err := charge(unit.ID, unit.Namespace, unit.Name); err != nil {
			return err
		}
	}
	for _, raw := range [][]byte{options.HeaderXML, options.SellerAuthenticationXML, options.TaxSupervisionXML, options.TaxBureauSignatureXML} {
		if len(raw) > invoiceXMLLimit-total {
			return fmt.Errorf("invoice sections exceed size limit")
		}
		total += len(raw)
	}
	return nil
}

// invoiceWriteRepresented 拒绝格式无法承载的非空公共字段
// 入参: want 输入业务数据，got 编码后数据
// 返回: 不可表达字段错误
func invoiceWriteRepresented(want, got Invoice) error {
	a := []string{want.Number, want.Code, want.Date, want.Status, want.Amount, want.TaxAmount, want.TotalAmount, want.Remarks, want.Buyer.Name, want.Buyer.TaxID, want.Buyer.Address, want.Buyer.Phone, want.Buyer.Bank, want.Buyer.Account, want.Seller.Name, want.Seller.TaxID, want.Seller.Address, want.Seller.Phone, want.Seller.Bank, want.Seller.Account}
	b := []string{got.Number, got.Code, got.Date, got.Status, got.Amount, got.TaxAmount, got.TotalAmount, got.Remarks, got.Buyer.Name, got.Buyer.TaxID, got.Buyer.Address, got.Buyer.Phone, got.Buyer.Bank, got.Buyer.Account, got.Seller.Name, got.Seller.TaxID, got.Seller.Address, got.Seller.Phone, got.Seller.Bank, got.Seller.Account}
	for i, v := range a {
		if v != "" && v != b[i] {
			return fmt.Errorf("invoice format cannot represent field value at index %d", i)
		}
	}
	if want.Currency != "" && want.Currency != got.Currency {
		return fmt.Errorf("invoice format cannot represent currency")
	}
	if len(want.Items) != len(got.Items) {
		return fmt.Errorf("invoice format cannot represent items")
	}
	for i, v := range want.Items {
		w := got.Items[i]
		for j, a := range []string{v.Name, v.Specification, v.Unit, v.Quantity, v.UnitPrice, v.Amount, v.TaxRate, v.TaxAmount} {
			b := []string{w.Name, w.Specification, w.Unit, w.Quantity, w.UnitPrice, w.Amount, w.TaxRate, w.TaxAmount}[j]
			if a != "" && a != b {
				return fmt.Errorf("invoice format cannot represent item %d field %d", i, j)
			}
		}
	}
	return nil
}

// invoiceEncodeXBRL 构造单主体XBRL实例
// 入参: value 发票，options 显式分类标准、上下文和单位
// 返回: XBRL数据及表达错误
func invoiceEncodeXBRL(value Invoice, options InvoiceEncodingOptions) ([]byte, error) {
	if options.SchemaRef == "" || !invoiceWriteText(options.SchemaRef) || !invoiceNCName(options.Context.ID) || options.Context.Identifier == "" || options.Context.Scheme == "" {
		return nil, fmt.Errorf("XBRL creation requires schema reference and explicit context")
	}
	for _, s := range []string{options.Context.Identifier, options.Context.Scheme, options.Context.Instant, options.Context.StartDate, options.Context.EndDate} {
		if !invoiceWriteText(s) {
			return nil, fmt.Errorf("invalid XBRL context text")
		}
	}
	if value.Currency == "" {
		return nil, fmt.Errorf("XBRL creation requires an explicit currency")
	}
	var b strings.Builder
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, `<xbrli:xbrl xmlns:xbrli="%s" xmlns:xsi="%s" xmlns:iso4217="%s" xmlns:f="%s" xmlns:link="%s" xmlns:xlink="%s">`, invoiceXBRL, XMLSchemaInstanceNamespace, InvoiceCurrencyNamespace, value.Standard, invoiceLinkbase, XLinkNamespace)
	fmt.Fprintf(&b, `<link:schemaRef xlink:type="simple" xlink:href="%s"/><xbrli:context id="%s"><xbrli:entity><xbrli:identifier scheme="%s">%s</xbrli:identifier></xbrli:entity><xbrli:period>`, invoiceWriteEscape(options.SchemaRef), options.Context.ID, invoiceWriteEscape(options.Context.Scheme), invoiceWriteEscape(options.Context.Identifier))
	if options.Context.Instant != "" {
		if options.Context.StartDate != "" || options.Context.EndDate != "" {
			return nil, fmt.Errorf("ambiguous XBRL period")
		}
		fmt.Fprintf(&b, "<xbrli:instant>%s</xbrli:instant>", invoiceWriteEscape(options.Context.Instant))
	} else {
		fmt.Fprintf(&b, "<xbrli:startDate>%s</xbrli:startDate><xbrli:endDate>%s</xbrli:endDate>", invoiceWriteEscape(options.Context.StartDate), invoiceWriteEscape(options.Context.EndDate))
	}
	b.WriteString("</xbrli:period></xbrli:context>")
	ids := map[string]bool{options.Context.ID: true}
	for _, unit := range options.Units {
		if !invoiceNCName(unit.ID) || !invoiceNCName(unit.Name) || unit.Namespace == "" || !invoiceWriteText(unit.Namespace) || ids[unit.ID] {
			return nil, fmt.Errorf("invalid or duplicate XBRL unit")
		}
		ids[unit.ID] = true
		fmt.Fprintf(&b, `<xbrli:unit id="%s"><xbrli:measure xmlns:m="%s">m:%s</xbrli:measure></xbrli:unit>`, unit.ID, invoiceWriteEscape(unit.Namespace), unit.Name)
	}
	factsByName := map[string]InvoiceFact{}
	for _, fact := range value.Facts {
		if fact.Source.NodePath != "" || fact.Namespace == invoiceXBRL {
			return nil, fmt.Errorf("source facts require source-preserving invoice update")
		}
		if _, exists := factsByName[fact.Name]; exists {
			return nil, fmt.Errorf("duplicate invoice fact %s", fact.Name)
		}
		factsByName[fact.Name] = fact
	}
	header := map[string]bool{}
	for _, name := range invoiceWriteHeader(value) {
		p := invoiceXMLField(&value, value.Kind, name)
		fact, has := factsByName[name]
		if has && p != nil && (fact.Nil && *p != "" || !fact.Nil && fact.Value != *p) {
			return nil, fmt.Errorf("conflicting mapped invoice fact %s", name)
		}
		if !has && p != nil && *p != "" {
			fact, has = InvoiceFact{Name: name, Value: *p}, true
		}
		if has {
			if err := invoiceWriteFact(&b, value, fact, options); err != nil {
				return nil, err
			}
		}
		header[name] = true
	}
	for _, fact := range value.Facts {
		if header[fact.Name] {
			continue
		}
		if err := invoiceWriteFact(&b, value, fact, options); err != nil {
			return nil, err
		}
		header[fact.Name] = true
	}
	for _, item := range value.Items {
		fmt.Fprintf(&b, "<f:%s>", invoiceWriteTuple(value.Kind))
		facts := map[string]InvoiceFact{}
		for _, fact := range item.Fields {
			if _, ok := facts[fact.Name]; ok {
				return nil, fmt.Errorf("duplicate invoice item fact")
			}
			facts[fact.Name] = fact
		}
		for _, name := range invoiceWriteItemNames(value.Kind) {
			fact, has := facts[name]
			p := invoiceItemField(&item, value.Kind, name)
			if p != nil && *p != "" {
				if has && (fact.Nil || fact.Value != *p) {
					return nil, fmt.Errorf("conflicting invoice item fact %s", name)
				}
				if !has {
					fact = InvoiceFact{Name: name, Value: *p}
				}
				has = true
			}
			if has {
				if err := invoiceWriteFact(&b, value, fact, options); err != nil {
					return nil, err
				}
				delete(facts, name)
			}
		}
		if len(facts) > 0 {
			return nil, fmt.Errorf("unsupported invoice item fields")
		}
		fmt.Fprintf(&b, "</f:%s>", invoiceWriteTuple(value.Kind))
	}
	b.WriteString("</xbrli:xbrl>")
	return []byte(b.String()), nil
}

// invoiceWriteFact 写入单个无维度事实，数值不经过浮点转换
// 入参: b 输出，value 发票，fact 事实，options 上下文和单位
// 返回: 不支持的事实错误
func invoiceWriteFact(b *strings.Builder, value Invoice, fact InvoiceFact, options InvoiceEncodingOptions) error {
	if !invoiceWriteKnownFact(value.Kind, fact.Name) {
		return fmt.Errorf("unsupported invoice concept %s", fact.Name)
	}
	if !invoiceNCName(fact.Name) || fact.Namespace != "" && fact.Namespace != value.Standard || len(fact.Dimensions) > 0 || fact.Context != "" && fact.Context != options.Context.ID {
		return fmt.Errorf("unsupported invoice fact context or namespace")
	}
	if fact.Nil && fact.Value != "" {
		return fmt.Errorf("nil invoice fact contains value")
	}
	if !fact.Nil && invoiceWriteDateFact(value.Kind, fact.Name) {
		if _, ok := invoicePeriodTime(fact.Value); !ok || strings.ContainsAny(fact.Value, "T ") {
			return fmt.Errorf("invalid invoice date fact %s", fact.Name)
		}
	}
	fmt.Fprintf(b, `<f:%s contextRef="%s"`, fact.Name, options.Context.ID)
	numeric := invoiceNumeric(value.Kind, fact.Name)
	if numeric {
		unit := ""
		for _, u := range options.Units {
			match := u.Namespace == InvoiceCurrencyNamespace && u.Name == value.Currency
			if !invoiceMonetary(value.Kind, fact.Name) {
				match = u.Namespace == invoiceXBRL && u.Name == "pure"
				if fact.Unit != "" {
					match = u.ID == fact.Unit
				}
			}
			if match && (fact.Unit == "" || fact.Unit == u.ID) {
				unit = u.ID
				break
			}
		}
		if unit == "" {
			return fmt.Errorf("missing matching XBRL unit for %s", fact.Name)
		}
		fmt.Fprintf(b, ` unitRef="%s"`, unit)
	}
	if fact.Nil {
		b.WriteString(` xsi:nil="true"`)
	} else if numeric {
		if invoiceDecimal(fact.Value) == nil {
			return fmt.Errorf("invalid decimal invoice fact")
		}
		if fact.Precision != "" {
			if fact.Decimals != "" || !invoiceAccuracy(fact.Precision, true) {
				return fmt.Errorf("invalid fact accuracy")
			}
			fmt.Fprintf(b, ` precision="%s"`, fact.Precision)
		} else {
			decimals := fact.Decimals
			if decimals == "" {
				decimals = "INF"
			}
			if !invoiceAccuracy(decimals, false) {
				return fmt.Errorf("invalid fact accuracy")
			}
			fmt.Fprintf(b, ` decimals="%s"`, decimals)
		}
	} else if fact.Decimals != "" || fact.Precision != "" || fact.Unit != "" {
		return fmt.Errorf("accuracy or unit on nonnumeric fact")
	}
	fmt.Fprintf(b, ">%s</f:%s>", invoiceWriteEscape(fact.Value), fact.Name)
	return nil
}

// invoicePlainGroups 返回官方公共字段分组
// 返回: 组名与字段顺序
func invoicePlainGroups() []invoiceWriteField {
	return []invoiceWriteField{{"SellerInformation", "SellerIdNum SellerName SellerAddr SellerTelNum SellerBankName SellerBankAccNum"}, {"BuyerInformation", "BuyerIdNum BuyerName BuyerAddr BuyerTelNum BuyerBankName BuyerBankAccNum"}, {"BasicInformation", "TotalAmWithoutTax TotalTaxAm TotalTax-includedAmount"}, {"AdditionalInformation", "Remark"}}
}

// invoiceEncodePlain 构造0.3版电子发票业务XML
// 入参: value 发票
// 返回: XML及不可表达的扩展错误
func invoiceEncodePlain(value Invoice) ([]byte, error) {
	if len(value.Facts) > 0 {
		return nil, fmt.Errorf("plain XML facts require source-preserving invoice update")
	}
	var b strings.Builder
	b.WriteString(xml.Header + `<EInvoice><Header><Version>0.3</Version></Header><EInvoiceData>`)
	for _, group := range invoicePlainGroups() {
		if group.name == "AdditionalInformation" {
			continue
		}
		fmt.Fprintf(&b, "<%s>", group.name)
		for _, name := range strings.Fields(group.value) {
			p, _ := invoicePlainField(&value, nil, "/EInvoice/EInvoiceData/"+group.name+"/"+name)
			if p != nil && *p != "" {
				fmt.Fprintf(&b, "<%s>%s</%s>", name, invoiceWriteEscape(*p), name)
			}
		}
		fmt.Fprintf(&b, "</%s>", group.name)
	}
	for _, item := range value.Items {
		if len(item.Fields) > 0 {
			return nil, fmt.Errorf("plain XML item facts require source-preserving invoice update")
		}
		b.WriteString("<IssuItemInformation>")
		for _, name := range strings.Fields("ItemName SpecMod MeaUnits Quantity UnPrice Amount TaxRate ComTaxAm") {
			p, _ := invoicePlainField(&value, &item, "/EInvoice/EInvoiceData/IssuItemInformation/"+name)
			if p != nil && *p != "" {
				fmt.Fprintf(&b, "<%s>%s</%s>", name, invoiceWriteEscape(*p), name)
			}
		}
		b.WriteString("</IssuItemInformation>")
	}
	if value.Remarks != "" {
		fmt.Fprintf(&b, "<AdditionalInformation><Remark>%s</Remark></AdditionalInformation>", invoiceWriteEscape(value.Remarks))
	}
	fmt.Fprintf(&b, "</EInvoiceData><TaxSupervisionInfo><InvoiceNumber>%s</InvoiceNumber><IssueTime>%s</IssueTime></TaxSupervisionInfo></EInvoice>", invoiceWriteEscape(value.Number), invoiceWriteEscape(value.Date))
	return []byte(b.String()), nil
}

// invoiceWriteSpans 将有界解析树与原始字节边界关联，保留BOM和空元素
// 入参: ctx 上下文，data XML
// 返回: 根节点、路径索引及错误
func invoiceWriteSpans(ctx context.Context, data []byte) (*invoiceXMLNode, map[string]invoiceXMLSpan, error) {
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return nil, nil, err
	}
	var nodes []*invoiceXMLNode
	var walk func(*invoiceXMLNode)
	walk = func(n *invoiceXMLNode) {
		nodes = append(nodes, n)
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(root)
	bom := 0
	trimmed := data
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		bom = 3
		trimmed = data[3:]
	}
	d := xml.NewDecoder(bytes.NewReader(trimmed))
	spans := map[string]invoiceXMLSpan{}
	var stack []invoiceXMLSpan
	index := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		before := bom + int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		after := bom + int(d.InputOffset())
		switch token.(type) {
		case xml.StartElement:
			n := nodes[index]
			index++
			empty := after >= 2 && bytes.Equal(data[after-2:after], []byte("/>"))
			stack = append(stack, invoiceXMLSpan{node: n, begin: before, inner: after, empty: empty})
		case xml.EndElement:
			s := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			s.close = before
			s.end = after
			if s.empty {
				s.close = s.inner
				s.end = s.inner
			}
			spans[s.node.path] = s
		}
	}
	return root, spans, nil
}

// invoiceApplyXMLPatches 原子合并互不重叠的字节修改
// 入参: ctx 上下文，data 原文，patches 修改列表
// 返回: 新XML及错误
func invoiceApplyXMLPatches(ctx context.Context, data []byte, patches []invoiceXMLPatch) ([]byte, error) {
	sort.SliceStable(patches, func(i, j int) bool { return patches[i].begin < patches[j].begin })
	var b bytes.Buffer
	offset := 0
	for _, p := range patches {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.begin < offset || p.end < p.begin || p.end > len(data) {
			return nil, fmt.Errorf("overlapping invoice XML changes")
		}
		if b.Len()+p.begin-offset+len(p.data) > invoiceXMLLimit {
			return nil, fmt.Errorf("invoice XML exceeds size limit")
		}
		b.Write(data[offset:p.begin])
		b.Write(p.data)
		offset = p.end
	}
	if b.Len()+len(data)-offset > invoiceXMLLimit {
		return nil, fmt.Errorf("invoice XML exceeds size limit")
	}
	b.Write(data[offset:])
	return b.Bytes(), nil
}

// invoiceValidateXMLWrite 检查已有字段词法及相对原文新增的损坏，不强制补齐历史创建字段
// 入参: ctx 上下文，before 原文，nil表示导入，after 待写入XML
// 返回: 新增或变更诊断错误
func invoiceValidateXMLWrite(ctx context.Context, before, after []byte) error {
	ctx = invoiceWriteContext(ctx)
	var original map[string]InvoiceIssue
	if before != nil {
		var err error
		original, err = invoiceWriteXMLProblems(ctx, before, true)
		if err != nil {
			return err
		}
	}
	problems, err := invoiceWriteXMLProblems(ctx, after, before != nil)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(problems))
	for key := range problems {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := original[key]; !ok {
			issue := problems[key]
			return fmt.Errorf("invoice %s: %s", issue.Field, issue.Message)
		}
	}
	return nil
}

// invoiceWriteXMLProblems 按节点路径及问题值归类诊断
// 入参: ctx 上下文，data 原文，tuples 是否检查元组必有字段
// 返回: 定位诊断及解析错误
func invoiceWriteXMLProblems(ctx context.Context, data []byte, tuples bool) (map[string]InvoiceIssue, error) {
	report, err := DecodeInvoices(ctx, data)
	if err != nil {
		return nil, err
	}
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return nil, err
	}
	problems := map[string]InvoiceIssue{}
	nodes := map[string]*invoiceXMLNode{}
	add := func(issue InvoiceIssue, value string) {
		hash := sha256.Sum256([]byte(value))
		key := issue.Code + "\x00" + issue.Field + "\x00" + issue.Source.NodePath + "\x00" + issue.Message + "\x00" + hex.EncodeToString(hash[:])
		problems[key] = issue
	}
	var walk func(*invoiceXMLNode, string) error
	walk = func(n *invoiceXMLNode, path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes[n.path] = n
		if n.name.Space != "" {
			path = "!" + path
		}
		path += "/" + n.name.Local
		kind, _ := invoiceTaxonomy(n.name.Space)
		nilValue := invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil")
		empty := nilValue == "true" || nilValue == "1"
		date := kind != "" && invoiceWriteDateFact(kind, n.name.Local)
		plainDate := root.name == (xml.Name{Local: "EInvoice"}) && path == "/EInvoice/TaxSupervisionInfo/IssueTime"
		edrmDate := n.parent == root && n.name == (xml.Name{Space: InvoiceStandardEDRM2019, Local: "IssueDate"})
		if len(n.children) == 0 && !empty && (date || plainDate || edrmDate) {
			value := n.text.String()
			parseValue := value
			if plainDate {
				parseValue = strings.Replace(value, " ", "T", 1)
			}
			if edrmDate {
				if d, err := time.Parse("2006年01月02日", value); err == nil {
					parseValue = d.Format("2006-01-02")
				}
			}
			_, valid := invoicePeriodTime(parseValue)
			if !valid || date && strings.ContainsAny(value, "T ") || edrmDate && len(parseValue) != 10 {
				add(InvoiceIssue{Code: "invalid-date", Field: n.name.Local, Message: "invalid invoice date", Source: InvoiceSource{NodePath: n.path}}, value)
			}
		}
		if tuples && kind != "" && invoiceItemTuple(kind, n.name.Local) {
			required := strings.Fields("NatureOfInvoiceLine NameOfTaxableGoodsOrServices AmountExcludingTax TaxRate TaxAmount")
			if kind == "atr" {
				required = strings.Fields("DepartureStation DestinationStation FlightSegment Carrier Flight Class FareBasis FreeBaggageAllowance")
			} else if n.name.Local == "InformationOfTollFeeDetailItemsTuple" {
				required = strings.Fields("NatureOfInvoiceLine NameOfTollFeeItems PlateNumber TypesOfVehicle EntranceDate ExitDate AmountExcludingTax TaxRate TaxAmount")
			}
			for _, name := range required {
				if len(invoiceChildren(n, n.name.Space, name)) == 0 {
					add(InvoiceIssue{Code: "required-tuple-field", Field: name, Message: "missing invoice tuple field", Source: InvoiceSource{NodePath: n.path}}, "")
				}
			}
		}
		for _, child := range n.children {
			if err := walk(child, path); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	for _, value := range report.Invoices {
		if value.Standard != InvoiceStandardEDRM2019 {
			continue
		}
		issues, err := ValidateInvoice(ctx, value)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			add(issue, "")
		}
	}
	for _, issue := range report.Issues {
		value := ""
		if n := nodes[issue.Source.NodePath]; n != nil {
			value = n.text.String() + "\x00" + invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil")
		}
		add(issue, value)
	}
	return problems, nil
}

// invoiceWriteLeaf 使用展开名称写入单个节点，并保留原有属性
// 入参: n 原节点，value 新值，nilValue 空值标记
// 返回: 节点XML及编码错误
func invoiceWriteLeaf(n *invoiceXMLNode, value string, nilValue bool) ([]byte, error) {
	var b bytes.Buffer
	e := xml.NewEncoder(&b)
	start := xml.StartElement{Name: n.name}
	var prefixes []string
	for prefix := range n.ns {
		if prefix != "" && prefix != "xml" {
			prefixes = append(prefixes, prefix)
		}
	}
	sort.Strings(prefixes)
	for _, prefix := range prefixes {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "xmlns:" + prefix}, Value: n.ns[prefix]})
	}
	for _, a := range n.attrs {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" && a.Name.Space == "" || a.Name.Space == XMLSchemaInstanceNamespace && a.Name.Local == "nil" || nilValue && a.Name.Space == "" && (a.Name.Local == "decimals" || a.Name.Local == "precision") {
			continue
		}
		start.Attr = append(start.Attr, a)
	}
	if nilValue {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Space: XMLSchemaInstanceNamespace, Local: "nil"}, Value: "true"})
	} else if invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil") == "true" || invoiceXMLAttr(n, XMLSchemaInstanceNamespace, "nil") == "1" {
		kind, _ := invoiceTaxonomy(n.name.Space)
		if invoiceNumeric(kind, n.name.Local) {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "decimals"}, Value: "INF"})
		}
	}
	if err := e.EncodeToken(start); err != nil {
		return nil, err
	}
	if err := e.EncodeToken(xml.CharData(value)); err != nil {
		return nil, err
	}
	if err := e.EncodeToken(start.End()); err != nil {
		return nil, err
	}
	if err := e.Flush(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// invoiceWriteNewIssues 拒绝修改新增的解析诊断，不隐藏原始文件诊断
// 入参: before 修改前诊断，after 修改后诊断
// 返回: 新增诊断错误
func invoiceWriteNewIssues(before, after []InvoiceIssue) error {
	counts := map[string]int{}
	key := func(i InvoiceIssue) string { return i.Code + "\x00" + i.Field + "\x00" + i.Message }
	for _, i := range before {
		counts[key(i)]++
	}
	for _, i := range after {
		k := key(i)
		if counts[k] == 0 {
			return fmt.Errorf("invoice update: %s", i.Message)
		}
		counts[k]--
	}
	return nil
}

// invoiceEDRMPaths 列出2019版摘要与明细的映射路径
// 返回: []string 2019版可映射字段路径
func invoiceEDRMPaths() []string {
	return strings.Fields("/eInvoice/InvoiceCode /eInvoice/InvoiceNo /eInvoice/IssueDate /eInvoice/Buyer/BuyerName /eInvoice/Buyer/BuyerTaxID /eInvoice/Seller/SellerName /eInvoice/Seller/SellerTaxID /eInvoice/TaxInclusiveTotalAmount /eInvoice/Note /eInvoice/TaxTotalAmount /eInvoice/TaxExclusiveTotalAmount /eInvoice/GoodsInfos/GoodsInfo/Item /eInvoice/GoodsInfos/GoodsInfo/Specification /eInvoice/GoodsInfos/GoodsInfo/MeasurementDimension /eInvoice/GoodsInfos/GoodsInfo/Quantity /eInvoice/GoodsInfos/GoodsInfo/Price /eInvoice/GoodsInfos/GoodsInfo/Amount /eInvoice/GoodsInfos/GoodsInfo/TaxScheme /eInvoice/GoodsInfos/GoodsInfo/TaxAmount")
}

// invoiceEncodeEDRM 创建2019版电子发票业务XML
// 入参: value 明确提供的发票业务字段
// 返回: 独立XML及不可表达错误
func invoiceEncodeEDRM(value Invoice) ([]byte, error) {
	if len(value.Facts) != 0 {
		return nil, fmt.Errorf("EDRM facts require source-preserving invoice update")
	}
	if _, err := time.Parse("2006-01-02", value.Date); err != nil {
		return nil, fmt.Errorf("EDRM creation requires a calendar date")
	}
	var b strings.Builder
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, `<eInvoice xmlns="%s" Version="1.0">`, InvoiceStandardEDRM2019)
	write := func(name, text string) {
		if text != "" {
			fmt.Fprintf(&b, "<%s>%s</%s>", name, invoiceWriteEscape(text), name)
		}
	}
	for _, name := range strings.Fields("InvoiceCode InvoiceNo IssueDate Buyer Seller TaxInclusiveTotalAmount Note TaxTotalAmount TaxExclusiveTotalAmount") {
		if name == "Buyer" || name == "Seller" {
			fmt.Fprintf(&b, "<%s>", name)
			for _, path := range invoiceEDRMPaths() {
				if strings.HasPrefix(path, "/eInvoice/"+name+"/") {
					p, _ := invoiceEDRMField(&value, nil, path)
					write(path[strings.LastIndexByte(path, '/')+1:], *p)
				}
			}
			fmt.Fprintf(&b, "</%s>", name)
		} else {
			p, _ := invoiceEDRMField(&value, nil, "/eInvoice/"+name)
			write(name, *p)
		}
	}
	if len(value.Items) > 0 {
		b.WriteString("<GoodsInfos>")
		for _, item := range value.Items {
			if len(item.Fields) != 0 {
				return nil, fmt.Errorf("EDRM item facts require source-preserving invoice update")
			}
			if reflect.DeepEqual(item, InvoiceItem{}) {
				return nil, fmt.Errorf("EDRM creation requires a nonempty item")
			}
			b.WriteString("<GoodsInfo>")
			for _, path := range invoiceEDRMPaths() {
				if strings.HasPrefix(path, "/eInvoice/GoodsInfos/GoodsInfo/") {
					p, _ := invoiceEDRMField(&value, &item, path)
					write(path[strings.LastIndexByte(path, '/')+1:], *p)
				}
			}
			b.WriteString("</GoodsInfo>")
		}
		b.WriteString("</GoodsInfos>")
	}
	b.WriteString("</eInvoice>")
	return []byte(b.String()), nil
}

// invoiceUpdateEDRM 按2019版原节点写回摘要及明细，保留扩展与未修改字节
// 入参: ctx 上下文，data 原文，old 原发票，value 目标发票
// 返回: 修改后XML及错误
func invoiceUpdateEDRM(ctx context.Context, data []byte, old, value Invoice) ([]byte, error) {
	if err := invoiceWriteUnmapped(old.Facts, value.Facts); err != nil {
		return nil, err
	}
	root, spans, err := invoiceWriteSpans(ctx, data)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"Buyer", "Seller", "GoodsInfos"} {
		changed := name == "GoodsInfos" && !reflect.DeepEqual(old.Items, value.Items) || name == "Buyer" && old.Buyer != value.Buyer || name == "Seller" && old.Seller != value.Seller
		groups := invoiceChildren(root, InvoiceStandardEDRM2019, name)
		if len(groups) > 1 {
			return nil, fmt.Errorf("ambiguous invoice group %s", name)
		}
		if changed && len(groups) == 1 && spans[groups[0].path].empty {
			patch, err := invoiceWriteInsert(spans[groups[0].path], nil, data)
			if err != nil {
				return nil, err
			}
			normalized, err := invoiceApplyXMLPatches(ctx, data, []invoiceXMLPatch{patch})
			if err != nil {
				return nil, err
			}
			return invoiceUpdateEDRM(ctx, normalized, old, value)
		}
	}
	var patches []invoiceXMLPatch
	order := strings.Fields("DocID InvoiceCode InvoiceNo TypeCode IssueDate InvoiceCheckCode MachineNo TaxControlCode Buyer Tpvn Seller TaxInclusiveTotalAmount Note InvoiceClerk Payee Checker TaxTotalAmount TaxExclusiveTotalAmount GraphCode InvoiceSIA1 InvoiceSIA2 Signature GoodsInfos")
	for _, group := range []string{"", "Buyer", "Seller"} {
		parent := root
		if group != "" {
			parents := invoiceChildren(root, InvoiceStandardEDRM2019, group)
			if len(parents) > 0 {
				parent = parents[0]
			} else {
				parent = nil
			}
		}
		desired := &invoiceXMLNode{name: xml.Name{Space: InvoiceStandardEDRM2019, Local: group}}
		var names []string
		for _, path := range invoiceEDRMPaths() {
			parts := strings.Split(path, "/")
			if group == "" && len(parts) != 3 || group != "" && (len(parts) != 4 || parts[2] != group) {
				continue
			}
			previous, _ := invoiceEDRMField(&old, nil, path)
			target, _ := invoiceEDRMField(&value, nil, path)
			if *previous == *target {
				continue
			}
			name := parts[len(parts)-1]
			names = append(names, name)
			node := &invoiceXMLNode{name: xml.Name{Space: InvoiceStandardEDRM2019, Local: name}}
			text := *target
			if name == "IssueDate" && parent != nil {
				existing := invoiceChildren(parent, InvoiceStandardEDRM2019, name)
				if len(existing) == 1 && strings.Contains(existing[0].text.String(), "年") && text != "" {
					date, err := time.Parse("2006-01-02", text)
					if err != nil {
						return nil, err
					}
					text = date.Format("2006年01月02日")
				}
			}
			node.text.WriteString(text)
			node.parts = []invoiceXMLPart{{text: text}}
			desired.children = append(desired.children, node)
			desired.parts = append(desired.parts, invoiceXMLPart{child: node})
		}
		if len(names) == 0 {
			continue
		}
		if parent == nil {
			raw, err := invoiceWriteNode(desired)
			if err != nil {
				return nil, err
			}
			patch, err := invoiceWriteOrderedInsert(data, spans, root, InvoiceStandardEDRM2019, group, order, raw)
			if err != nil {
				return nil, err
			}
			patches = append(patches, patch)
		} else {
			for _, name := range names {
				if len(invoiceChildren(parent, InvoiceStandardEDRM2019, name)) == 0 && group == "" {
					raw, err := invoiceWriteNode(invoiceChildren(desired, InvoiceStandardEDRM2019, name)[0])
					if err != nil {
						return nil, err
					}
					patch, err := invoiceWriteOrderedInsert(data, spans, parent, InvoiceStandardEDRM2019, name, order, raw)
					if err != nil {
						return nil, err
					}
					patches = append(patches, patch)
					continue
				}
				changes, err := invoiceWriteMergeChildren(data, spans, parent, desired, InvoiceStandardEDRM2019, []string{name})
				if err != nil {
					return nil, err
				}
				patches = append(patches, changes...)
			}
		}
	}
	if !reflect.DeepEqual(old.Items, value.Items) {
		generated := &invoiceXMLNode{name: xml.Name{Space: InvoiceStandardEDRM2019, Local: "GoodsInfos"}}
		for _, item := range value.Items {
			node := &invoiceXMLNode{name: xml.Name{Space: InvoiceStandardEDRM2019, Local: "GoodsInfo"}}
			for _, path := range invoiceEDRMPaths() {
				if !strings.HasPrefix(path, "/eInvoice/GoodsInfos/GoodsInfo/") {
					continue
				}
				p, _ := invoiceEDRMField(&value, &item, path)
				leaf := &invoiceXMLNode{name: xml.Name{Space: InvoiceStandardEDRM2019, Local: path[strings.LastIndexByte(path, '/')+1:]}}
				leaf.text.WriteString(*p)
				leaf.parts = []invoiceXMLPart{{text: *p}}
				node.children = append(node.children, leaf)
				node.parts = append(node.parts, invoiceXMLPart{child: leaf})
			}
			generated.children = append(generated.children, node)
			generated.parts = append(generated.parts, invoiceXMLPart{child: node})
		}
		parents := invoiceChildren(root, InvoiceStandardEDRM2019, "GoodsInfos")
		if len(parents) == 0 {
			raw, err := invoiceWriteNode(generated)
			if err != nil {
				return nil, err
			}
			patch, err := invoiceWriteInsert(spans[root.path], raw, data)
			if err != nil {
				return nil, err
			}
			patches = append(patches, patch)
		} else {
			changes, err := invoiceWriteMergeItems(ctx, data, spans, parents[0], generated, old, value)
			if err != nil {
				return nil, err
			}
			patches = append(patches, changes...)
		}
	}
	result, err := invoiceApplyXMLPatches(ctx, data, patches)
	if err != nil {
		return nil, err
	}
	report, err := DecodeInvoices(ctx, result)
	if err != nil {
		return nil, err
	}
	if len(report.Invoices) != 1 {
		return nil, fmt.Errorf("invoice update changed candidate count")
	}
	if err := invoiceWriteRepresented(value, report.Invoices[0]); err != nil {
		return nil, err
	}
	if err := invoiceWriteRepresented(report.Invoices[0], value); err != nil {
		return nil, err
	}
	if err := invoiceValidateXMLWrite(ctx, data, result); err != nil {
		return nil, err
	}
	return result, nil
}

// invoiceWriteContains 判断字段是否位于已知顺序表
// 入参: names 字段列表，name 字段
// 返回: 是否存在
func invoiceWriteContains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// invoiceWriteUnmapped 拒绝通过摘要更改或删除未映射事实，原文修改使用PatchInvoice
// 入参: before 原事实，after 目标事实
// 返回: 不支持的事实修改错误
func invoiceWriteUnmapped(before, after []InvoiceFact) error {
	if len(after) == 0 {
		return nil
	}
	old := map[string]InvoiceFact{}
	for _, f := range before {
		old[f.Source.NodePath] = f
	}
	for _, f := range after {
		prior, ok := old[f.Source.NodePath]
		if !ok {
			return fmt.Errorf("new unmapped facts require explicit source editing")
		}
		if f.Name != prior.Name || f.Namespace != prior.Namespace || f.Context != prior.Context || f.Unit != prior.Unit || f.Decimals != prior.Decimals || f.Precision != prior.Precision || !slices.Equal(f.Dimensions, prior.Dimensions) {
			return fmt.Errorf("invoice update cannot alter fact metadata")
		}
		if f.Value != prior.Value || f.Nil != prior.Nil {
			return fmt.Errorf("unmapped fact changes require PatchInvoice")
		}
	}
	return nil
}

// invoiceWriteOriginalOptions 读取原上下文及单位定义
// 入参: root 原XML，value 原候选
// 返回: 原定义及不支持的复杂上下文错误
func invoiceWriteOriginalOptions(root *invoiceXMLNode, value Invoice) (InvoiceEncodingOptions, error) {
	var options InvoiceEncodingOptions
	ref := ""
	for _, f := range value.Facts {
		if f.Namespace == invoiceXBRL && f.Name == "context" {
			ref = f.Context
			break
		}
	}
	for _, n := range root.children {
		if n.name == (xml.Name{Space: invoiceLinkbase, Local: "schemaRef"}) && options.SchemaRef == "" {
			options.SchemaRef = invoiceXMLAttr(n, XLinkNamespace, "href")
		}
		if n.name == (xml.Name{Space: invoiceXBRL, Local: "context"}) && invoiceXMLAttr(n, "", "id") == ref {
			dims, ok := invoiceContext(n)
			if !ok || len(dims) > 0 {
				return options, fmt.Errorf("invoice update requires an unambiguous nondimensional context")
			}
			entity := invoiceChildren(n, invoiceXBRL, "entity")[0]
			id := invoiceChildren(entity, invoiceXBRL, "identifier")[0]
			options.Context = InvoiceContext{ID: ref, Identifier: id.text.String(), Scheme: invoiceXMLAttr(id, "", "scheme")}
			period := invoiceChildren(n, invoiceXBRL, "period")[0]
			for _, p := range period.children {
				switch p.name.Local {
				case "instant":
					options.Context.Instant = p.text.String()
				case "startDate":
					options.Context.StartDate = p.text.String()
				case "endDate":
					options.Context.EndDate = p.text.String()
				}
			}
		}
		if n.name == (xml.Name{Space: invoiceXBRL, Local: "unit"}) {
			ms := invoiceChildren(n, invoiceXBRL, "measure")
			if len(ms) == 1 {
				q, ok := invoiceQName(ms[0], ms[0].text.String())
				if ok {
					options.Units = append(options.Units, InvoiceUnit{ID: invoiceXMLAttr(n, "", "id"), Namespace: q.Space, Name: q.Local})
				}
			}
		}
	}
	if options.SchemaRef == "" {
		return options, fmt.Errorf("invoice source has no schema reference")
	}
	return options, nil
}

// invoiceWriteNode 序列化生成的节点，使移动节点的命名空间自足
// 入参: n 节点
// 返回: XML及编码错误
func invoiceWriteNode(n *invoiceXMLNode) ([]byte, error) {
	var b bytes.Buffer
	e := xml.NewEncoder(&b)
	var walk func(*invoiceXMLNode) error
	walk = func(c *invoiceXMLNode) error {
		start := xml.StartElement{Name: c.name}
		for _, a := range c.attrs {
			if a.Name.Space != "xmlns" && a.Name != (xml.Name{Local: "xmlns"}) {
				start.Attr = append(start.Attr, a)
			}
		}
		if err := e.EncodeToken(start); err != nil {
			return err
		}
		for _, p := range c.parts {
			if p.child != nil {
				if err := walk(p.child); err != nil {
					return err
				}
			} else {
				if err := e.EncodeToken(xml.CharData(p.text)); err != nil {
					return err
				}
			}
		}
		return e.EncodeToken(start.End())
	}
	if err := walk(n); err != nil {
		return nil, err
	}
	if err := e.Flush(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// invoiceWriteInsert 生成父节点内追加内容的修改，支持空元素
// 入参: span 父节点边界，raw 新内容，data 原文
// 返回: 插入修改及错误
func invoiceWriteInsert(span invoiceXMLSpan, raw, data []byte) (invoiceXMLPatch, error) {
	if !span.empty {
		return invoiceXMLPatch{span.close, span.close, raw}, nil
	}
	open := data[span.begin:span.inner]
	nameEnd := bytes.IndexAny(open, " \t\r\n/>")
	if nameEnd < 2 {
		return invoiceXMLPatch{}, fmt.Errorf("invalid empty invoice element")
	}
	name := string(open[1:nameEnd])
	replacement := append([]byte(nil), open[:len(open)-2]...)
	replacement = append(replacement, '>')
	replacement = append(replacement, raw...)
	replacement = append(replacement, []byte("</"+name+">")...)
	return invoiceXMLPatch{span.begin, span.end, replacement}, nil
}

// invoiceWriteMergeChildren 合并已知叶节点，其他属性、子节点及空白保持原样
// 入参: data 原文，spans 边界，old 原父节点，desired 目标父节点，space 命名空间，names 已知字段
// 返回: 修改列表及歧义错误
func invoiceWriteMergeChildren(data []byte, spans map[string]invoiceXMLSpan, old, desired *invoiceXMLNode, space string, names []string) ([]invoiceXMLPatch, error) {
	var patches []invoiceXMLPatch
	for rank, name := range names {
		existing := invoiceChildren(old, space, name)
		var targets []*invoiceXMLNode
		if desired != nil {
			targets = invoiceChildren(desired, space, name)
		}
		if len(existing) > 1 || len(targets) > 1 {
			return nil, fmt.Errorf("ambiguous invoice field %s", name)
		}
		var raw []byte
		if len(targets) > 0 {
			var err error
			raw, err = invoiceWriteNode(targets[0])
			if err != nil {
				return nil, err
			}
		}
		if len(existing) > 0 {
			s := spans[existing[0].path]
			if len(existing[0].children) > 0 {
				return nil, fmt.Errorf("non-leaf invoice field %s", name)
			}
			if len(targets) == 0 && (existing[0].text.String() == "" || invoiceXMLAttr(existing[0], XMLSchemaInstanceNamespace, "nil") == "true" || invoiceXMLAttr(existing[0], XMLSchemaInstanceNamespace, "nil") == "1") {
				continue
			}
			if len(targets) > 0 && existing[0].text.String() == targets[0].text.String() && invoiceXMLAttr(existing[0], XMLSchemaInstanceNamespace, "nil") == invoiceXMLAttr(targets[0], XMLSchemaInstanceNamespace, "nil") {
				continue
			}
			if len(targets) > 0 {
				oldNil := invoiceXMLAttr(existing[0], XMLSchemaInstanceNamespace, "nil")
				newNil := invoiceXMLAttr(targets[0], XMLSchemaInstanceNamespace, "nil")
				wasNil := oldNil == "true" || oldNil == "1"
				isNil := newNil == "true" || newNil == "1"
				if !s.empty && wasNil == isNil {
					patches = append(patches, invoiceXMLPatch{s.inner, s.close, []byte(invoiceWriteEscape(targets[0].text.String()))})
					continue
				}
				var err error
				raw, err = invoiceWriteLeaf(existing[0], targets[0].text.String(), isNil)
				if err != nil {
					return nil, err
				}
			}
			patches = append(patches, invoiceXMLPatch{s.begin, s.end, raw})
		} else if len(raw) > 0 {
			position := -1
			for _, next := range names[rank+1:] {
				nodes := invoiceChildren(old, space, next)
				if len(nodes) > 0 {
					candidate := spans[nodes[0].path].begin
					if position < 0 || candidate < position {
						position = candidate
					}
				}
			}
			if position >= 0 {
				patches = append(patches, invoiceXMLPatch{position, position, raw})
			} else {
				p, err := invoiceWriteInsert(spans[old.path], raw, data)
				if err != nil {
					return nil, err
				}
				patches = append(patches, p)
			}
		}
	}
	return patches, nil
}

// invoiceWriteMergeItems 按原明细来源重排或修改元组，删除仅作用于显式移除的明细
// 入参: ctx 上下文，data 原文，spans 边界，parent 原父节点，generated 新父节点，old 原发票，value 目标发票
// 返回: 明细修改及错误
func invoiceWriteMergeItems(ctx context.Context, data []byte, spans map[string]invoiceXMLSpan, parent, generated *invoiceXMLNode, old, value Invoice) ([]invoiceXMLPatch, error) {
	if reflect.DeepEqual(old.Items, value.Items) {
		return nil, nil
	}
	space, name := value.Standard, invoiceWriteTuple(value.Kind)
	names := invoiceWriteItemNames(value.Kind)
	if value.Standard == InvoiceStandardEDRM2019 {
		name = "GoodsInfo"
		names = strings.Fields("Item Specification MeasurementDimension Quantity Price Amount TaxScheme TaxAmount")
	} else if value.Standard == InvoiceStandardEInvoice {
		space = ""
		name = "IssuItemInformation"
		names = strings.Fields("ItemName SpecMod MeaUnits Quantity UnPrice Amount TaxRate ComTaxAm")
	}
	targets := invoiceChildren(generated, space, name)
	if len(targets) != len(value.Items) {
		return nil, fmt.Errorf("unsupported invoice item shape")
	}
	original := map[string]*invoiceXMLNode{}
	originalItems := map[string]InvoiceItem{}
	for _, item := range old.Items {
		for _, f := range item.Fields {
			s := spans[f.Source.NodePath]
			if s.node != nil && s.node.parent != nil && s.node.parent.parent == parent && s.node.parent.name.Space == space {
				ref := invoiceXMLAttr(s.node, "", "contextRef")
				for _, child := range s.node.parent.children {
					if other := invoiceXMLAttr(child, "", "contextRef"); other != "" && other != ref {
						return nil, fmt.Errorf("invoice item shares multiple contexts")
					}
				}
				original[s.node.parent.path] = s.node.parent
				originalItems[s.node.parent.path] = item
				break
			}
		}
	}
	if len(original) != len(old.Items) {
		return nil, fmt.Errorf("invoice items require explicit tuple sources")
	}
	seen := map[string]bool{}
	var combined []byte
	for i, item := range value.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := ""
		for _, f := range item.Fields {
			s := spans[f.Source.NodePath]
			if s.node != nil && s.node.parent != nil && original[s.node.parent.path] != nil {
				if path != "" && path != s.node.parent.path {
					return nil, fmt.Errorf("mixed invoice item sources")
				}
				path = s.node.parent.path
			}
		}
		if path == "" {
			for _, f := range item.Fields {
				mapped := invoiceItemField(&InvoiceItem{}, value.Kind, f.Name)
				if value.Standard == InvoiceStandardEInvoice || value.Standard == InvoiceStandardEDRM2019 || !invoiceWriteContains(names, f.Name) {
					return nil, fmt.Errorf("unsupported new invoice item fact %s", f.Name)
				}
				if mapped != nil {
					p := invoiceItemField(&item, value.Kind, f.Name)
					if f.Nil && *p != "" || !f.Nil && f.Value != *p {
						return nil, fmt.Errorf("conflicting new invoice item fact %s", f.Name)
					}
				}
			}
			raw, err := invoiceWriteNode(targets[i])
			if err != nil {
				return nil, err
			}
			combined = append(combined, raw...)
			continue
		}
		if seen[path] {
			return nil, fmt.Errorf("duplicate invoice item source")
		}
		seen[path] = true
		for _, f := range item.Fields {
			if invoiceWriteContains(names, f.Name) {
				continue
			}
			s, ok := spans[f.Source.NodePath]
			if !ok || s.node.parent == nil || s.node.parent.path != path || f.Value != s.node.text.String() || f.Nil != (invoiceXMLAttr(s.node, XMLSchemaInstanceNamespace, "nil") == "true" || invoiceXMLAttr(s.node, XMLSchemaInstanceNamespace, "nil") == "1") {
				return nil, fmt.Errorf("unmapped item fact changes require PatchInvoice")
			}
		}
		n := original[path]
		if value.Standard == InvoiceStandardEDRM2019 {
			previous := originalItems[path]
			if err := invoiceWriteUnmapped(previous.Fields, item.Fields); err != nil {
				return nil, err
			}
			var retained []*invoiceXMLNode
			for _, child := range targets[i].children {
				field := "/eInvoice/GoodsInfos/GoodsInfo/" + child.name.Local
				before, _ := invoiceEDRMField(&old, &previous, field)
				after, _ := invoiceEDRMField(&value, &item, field)
				if before != nil && after != nil && *before == *after {
					children := invoiceChildren(n, InvoiceStandardEDRM2019, child.name.Local)
					if len(children) == 0 {
						continue
					}
					if len(children) == 1 {
						child = children[0]
					}
				}
				retained = append(retained, child)
			}
			targets[i].children = retained
		}
		ps, err := invoiceWriteMergeChildren(data, spans, n, targets[i], space, names)
		if err != nil {
			return nil, err
		}
		s := spans[path]
		for j := range ps {
			ps[j].begin -= s.begin
			ps[j].end -= s.begin
		}
		raw, err := invoiceApplyXMLPatches(ctx, data[s.begin:s.end], ps)
		if err != nil {
			return nil, err
		}
		combined = append(combined, raw...)
	}
	var ordered []invoiceXMLSpan
	for path := range original {
		ordered = append(ordered, spans[path])
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].begin < ordered[j].begin })
	var patches []invoiceXMLPatch
	for i, s := range ordered {
		raw := []byte(nil)
		if i == 0 {
			raw = combined
		}
		patches = append(patches, invoiceXMLPatch{s.begin, s.end, raw})
	}
	if len(ordered) == 0 && len(combined) > 0 {
		var p invoiceXMLPatch
		var err error
		if value.Standard == InvoiceStandardEInvoice {
			p, err = invoiceWriteOrderedInsert(data, spans, parent, "", name, strings.Fields("SellerInformation BuyerInformation BasicInformation IssuItemInformation SpecificInformation AdditionalInformation"), combined)
		} else {
			p, err = invoiceWriteInsert(spans[parent.path], combined, data)
		}
		if err != nil {
			return nil, err
		}
		patches = append(patches, p)
	}
	return patches, nil
}

// invoiceWriteOrderedInsert 在后继已知元素前插入，未知节点不移动
// 入参: data 原文，spans 边界，parent 父节点，space 命名空间，name 新节点名，names 规范顺序，raw 新内容
// 返回: 插入修改及错误
func invoiceWriteOrderedInsert(data []byte, spans map[string]invoiceXMLSpan, parent *invoiceXMLNode, space, name string, names []string, raw []byte) (invoiceXMLPatch, error) {
	position := -1
	following := false
	for _, known := range names {
		if known == name {
			following = true
			continue
		}
		if !following {
			continue
		}
		for _, node := range invoiceChildren(parent, space, known) {
			offset := spans[node.path].begin
			if position < 0 || offset < position {
				position = offset
			}
		}
	}
	if position >= 0 {
		return invoiceXMLPatch{position, position, raw}, nil
	}
	return invoiceWriteInsert(spans[parent.path], raw, data)
}

// invoiceWriteCreation 检查官方元组序列中的必有字段
// 入参: value 发票
// 返回: 缺少元组字段错误
func invoiceWriteCreation(value Invoice) error {
	if value.Standard == InvoiceStandardEInvoice || value.Standard == InvoiceStandardEDRM2019 {
		return nil
	}
	for i, item := range value.Items {
		required := strings.Fields("NatureOfInvoiceLine NameOfTaxableGoodsOrServices AmountExcludingTax TaxRate TaxAmount")
		if value.Kind == "atr" {
			required = strings.Fields("DepartureStation DestinationStation FlightSegment Carrier Flight Class FareBasis FreeBaggageAllowance")
		}
		for _, name := range required {
			p := invoiceItemField(&item, value.Kind, name)
			present := p != nil && *p != ""
			for _, f := range item.Fields {
				if f.Name == name {
					present = true
				}
			}
			if !present {
				return fmt.Errorf("invoice item %d requires %s", i, name)
			}
		}
	}
	return nil
}

// invoiceWriteKnownFact 限制新建事实为已核实的分类标准概念
// 入参: kind 票种，name 概念名
// 返回: 是否支持新建
func invoiceWriteKnownFact(kind, name string) bool {
	if invoiceWriteContains(invoiceWriteHeader(Invoice{Kind: kind}), name) {
		return true
	}
	if (kind == "inv" || kind == "atr") && invoiceWriteContains(invoiceWriteItemNames(kind), name) {
		return true
	}
	var extras string
	switch kind {
	case "inv":
		extras = "TypeOfInvoice UniqueCodeOfInvoice LocationOfInvoice IdentifyingCode NumberOfInvoiceMachine SecurityCode SpecialInvoiceType InvoiceIssuingSign TaxIncludedAmountInWords Payee Reviewer Issuer AddressPhoneNumberOfPurchaser DepositBankAndAccountNumberOfPurchaser AddressPhoneNumberOfSeller DepositBankAndAccountNumberOfSeller"
	case "einv":
		extras = "IssueTime TotalTax-includedAmountInChinese DrawerChecker Payee Drawer RedNotificationNo OriginalInvoiceNo BlueInvoiceNo InvoiceType SpecificBusinessType DeductionAm AmountOfTransferredOutInputVat RecordedAmount"
	case "atr":
		extras = "PassengerName ValidIdNumber Endorsement NumberOfGpOrder Fare FuelSurcharge VatRate CivilAviationDevelopmentFund OtherTaxes ETicketNumber VerificationCode PromptInformation Insurance AgentCode IssueParty MarkingOfDomesticOrInternational"
	case "rai":
		extras = "TypeOfVoucher ETicketNumber IssueParty IssuePartyCode TypeOfBusiness DepartureStation PhonicsOfDepartureStation DestinationStation PhonicsOfDestinationStation TrainNumber TravelDate DepartureTime AirConditioningCharacteristics SeatLevel Carriage Seat TypeOfRailwayTicket IdNumber Name TaxRate AddressPhoneNumberOfPurchaser DepositBankAndAccountNumberOfPurchaser AddressPhoneNumberOfSeller DepositBankAndAccountNumberOfSeller"
	}
	return invoiceWriteContains(strings.Fields(extras), name)
}

// invoiceWriteDateFact 识别规范中的日期类型，而非自由文本日期时间
// 入参: kind 票种，name 概念名
// 返回: 是否为日期事实
func invoiceWriteDateFact(kind, name string) bool {
	return kind == "inv" && name == "DateOfIssue" || kind == "atr" && invoiceWriteContains(strings.Fields("IssueDate CarrierDate EffectiveDate ExpirationDate"), name) || kind == "rai" && (name == "DateOfIssue" || name == "TravelDate")
}

// invoiceWritePlainSections 接入调用方提供的官方头部、认证及签名片段，不生成税务签名
// 入参: ctx 上下文，data 生成XML，options 显式业务片段
// 返回: XML及片段错误
func invoiceWritePlainSections(ctx context.Context, data []byte, options InvoiceEncodingOptions) ([]byte, error) {
	root, spans, err := invoiceWriteSpans(ctx, data)
	if err != nil {
		return nil, err
	}
	var patches []invoiceXMLPatch
	for _, section := range []struct {
		name string
		data []byte
	}{{"Header", options.HeaderXML}, {"SellerAuthentication", options.SellerAuthenticationXML}, {"TaxSupervisionInfo", options.TaxSupervisionXML}, {"TaxBureauSignature", options.TaxBureauSignatureXML}} {
		if len(section.data) == 0 {
			continue
		}
		n, err := invoiceXMLTree(ctx, section.data)
		if err != nil {
			return nil, err
		}
		if n.name != (xml.Name{Local: section.name}) {
			return nil, fmt.Errorf("invalid invoice section %s", section.name)
		}
		if section.name == "Header" {
			vs := invoiceChildren(n, "", "Version")
			if len(vs) != 1 || vs[0].text.String() != "0.3" {
				return nil, fmt.Errorf("invalid invoice header version")
			}
		}
		old := invoiceChildren(root, "", section.name)
		if len(old) > 0 {
			s := spans[old[0].path]
			patches = append(patches, invoiceXMLPatch{s.begin, s.end, section.data})
		} else {
			pos := spans[root.path].close
			if section.name == "SellerAuthentication" {
				pos = spans[invoiceChildren(root, "", "TaxSupervisionInfo")[0].path].begin
			}
			patches = append(patches, invoiceXMLPatch{pos, pos, section.data})
		}
	}
	return invoiceApplyXMLPatches(ctx, data, patches)
}
