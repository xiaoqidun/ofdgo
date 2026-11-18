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
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// 发票读取限额
const (
	invoiceXMLLimit    = 16 << 20
	invoiceTotalLimit  = 64 << 20
	invoiceSourceLimit = 256
)

// InvoiceReport 保存当前文档及版本的提取结果，不代表验签或联网查验结论
// 不同载荷中的记录保持独立，未识别格式及局部错误通过Issues报告
// UnassignedFacts保留无法归属已知发票上下文的XBRL事实，不跨主体合并
type InvoiceReport struct {
	DocumentIndex   int            `json:"documentIndex"`
	Version         string         `json:"version,omitempty"`
	Invoices        []Invoice      `json:"invoices,omitempty"`
	UnassignedFacts []InvoiceFact  `json:"unassignedFacts,omitempty"`
	Issues          []InvoiceIssue `json:"issues,omitempty"`
}

// Invoice 保存业务规范明确映射的发票字段，空字符串表示未提供或无法唯一确定
// 金额、数量、税率保留十进制原文，号码和税号不转换为数字；Facts保留原始业务事实
type Invoice struct {
	Kind        string          `json:"kind"`
	Standard    string          `json:"standard"`
	Version     string          `json:"version"`
	Number      string          `json:"number,omitempty"`
	Code        string          `json:"code,omitempty"`
	Date        string          `json:"date,omitempty"`
	Currency    string          `json:"currency,omitempty"`
	Status      string          `json:"status,omitempty"`
	Amount      string          `json:"amount,omitempty"`
	TaxAmount   string          `json:"taxAmount,omitempty"`
	TotalAmount string          `json:"totalAmount,omitempty"`
	Remarks     string          `json:"remarks,omitempty"`
	Buyer       InvoiceParty    `json:"buyer"`
	Seller      InvoiceParty    `json:"seller"`
	Items       []InvoiceItem   `json:"items,omitempty"`
	Sources     []InvoiceSource `json:"sources"`
	Facts       []InvoiceFact   `json:"facts,omitempty"`
}

// InvoiceParty 保存购销双方信息
type InvoiceParty struct {
	Name    string `json:"name,omitempty"`
	TaxID   string `json:"taxId,omitempty"`
	Address string `json:"address,omitempty"`
	Phone   string `json:"phone,omitempty"`
	Bank    string `json:"bank,omitempty"`
	Account string `json:"account,omitempty"`
}

// InvoiceItem 保存明细公共字段，航段、旅客及其他票种特有字段通过Fields保留
type InvoiceItem struct {
	Name          string        `json:"name,omitempty"`
	Specification string        `json:"specification,omitempty"`
	Unit          string        `json:"unit,omitempty"`
	Quantity      string        `json:"quantity,omitempty"`
	UnitPrice     string        `json:"unitPrice,omitempty"`
	Amount        string        `json:"amount,omitempty"`
	TaxRate       string        `json:"taxRate,omitempty"`
	TaxAmount     string        `json:"taxAmount,omitempty"`
	Fields        []InvoiceFact `json:"fields,omitempty"`
}

// InvoiceSource 标识包内来源，Kind为tag、extension或attachment，NodePath为XML节点路径
type InvoiceSource struct {
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	FileLoc  string `json:"fileLoc"`
	NodePath string `json:"nodePath,omitempty"`
}

// InvoiceIssue 保存来源明确的解析诊断，Code为机器可读类别
type InvoiceIssue struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Source  InvoiceSource `json:"source"`
	Field   string        `json:"field,omitempty"`
}

// InvoiceFact 保存命名空间限定的业务事实、原始数值及精度属性，Nil与空文本分别表达
// References保留对象引用标引的原始页标识和有序图元标识
type InvoiceFact struct {
	Name       string               `json:"name"`
	Namespace  string               `json:"namespace,omitempty"`
	Value      string               `json:"value"`
	Context    string               `json:"context,omitempty"`
	Unit       string               `json:"unit,omitempty"`
	Decimals   string               `json:"decimals,omitempty"`
	Precision  string               `json:"precision,omitempty"`
	Nil        bool                 `json:"nil,omitempty"`
	Source     InvoiceSource        `json:"source"`
	Dimensions []InvoiceDimension   `json:"dimensions,omitempty"`
	References []StructureReference `json:"references,omitempty"`
}

// InvoiceDimension 保存上下文维度，名称和成员的命名空间不依赖XML前缀
type InvoiceDimension struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Value     string `json:"value"`
}

// Invoices 提取当前文档及版本中的结构化发票，不执行票面识别、验签或网络请求
// 返回: InvoiceReport 发票及诊断, error 文档读取错误
func (r *Reader) Invoices() (InvoiceReport, error) {
	return r.InvoicesContext(context.Background())
}

// InvoicesContext 按标引、扩展及附件入口提取发票，取消或整体限额失败时不返回部分结果
// 入参: ctx 取消上下文
// 返回: InvoiceReport 独立发票及诊断, error 文档、取消或限额错误
func (r *Reader) InvoicesContext(ctx context.Context) (InvoiceReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return InvoiceReport{}, err
	}
	if r == nil {
		return InvoiceReport{}, fmt.Errorf("nil document reader")
	}
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return InvoiceReport{}, err
	}
	report := InvoiceReport{DocumentIndex: r.DocumentIndex()}
	if r.versionInfo != nil {
		report.Version = r.versionInfo.ID
	}
	total, count := 0, 0
	seen := make(map[string][]int)
	consume := func(source InvoiceSource, data []byte, readErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		total += len(data)
		if count > invoiceSourceLimit || total > invoiceTotalLimit {
			return fmt.Errorf("invoice extraction limit exceeded")
		}
		if readErr != nil {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "read-error", Message: readErr.Error(), Source: source})
			return nil
		}
		parsed, err := r.readInvoiceXML(ctx, data, source)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-data", Message: err.Error(), Source: source})
			return nil
		}
		report.Invoices = append(report.Invoices, parsed.Invoices...)
		report.UnassignedFacts = append(report.UnassignedFacts, parsed.UnassignedFacts...)
		report.Issues = append(report.Issues, parsed.Issues...)
		return nil
	}
	read := func(source InvoiceSource) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		resolved := r.ResPath(source.FileLoc)
		if !validPackagePath(resolved) {
			return consume(source, nil, fmt.Errorf("invalid invoice resource path"))
		}
		source.FileLoc = resolved
		if indexes, ok := seen[resolved]; ok {
			count++
			if count > invoiceSourceLimit {
				return fmt.Errorf("invoice extraction limit exceeded")
			}
			for _, index := range indexes {
				report.Invoices[index].Sources = append(report.Invoices[index].Sources, source)
			}
			return nil
		}
		before := len(report.Invoices)
		data, err := r.invoiceData(ctx, resolved)
		if err := consume(source, data, err); err != nil {
			return err
		}
		seen[resolved] = nil
		for i := before; i < len(report.Invoices); i++ {
			seen[resolved] = append(seen[resolved], i)
		}
		return nil
	}
	tags, err := r.CustomTagsContext(ctx)
	if err != nil {
		report.Issues = append(report.Issues, InvoiceIssue{Code: "read-error", Message: err.Error(), Source: InvoiceSource{Kind: "tag"}})
	}
	for i, tag := range tags {
		if tag.TypeID == textTagType && tag.NameSpace == textNamespace || tag.TypeID == richTextTagType && tag.NameSpace == richTextNamespace || tag.TypeID == invoiceTagType && tag.NameSpace == invoiceNamespace {
			continue
		}
		if err := read(InvoiceSource{Kind: "tag", ID: strconv.Itoa(i), FileLoc: tag.FileLoc}); err != nil {
			return InvoiceReport{}, err
		}
	}
	extensions, err := r.ExtensionsContext(ctx)
	if err != nil {
		report.Issues = append(report.Issues, InvoiceIssue{Code: "read-error", Message: err.Error(), Source: InvoiceSource{Kind: "extension"}})
	}
	inline := false
	for i, extension := range extensions {
		inline = inline || len(extension.Data) > 0
		for _, location := range extension.ExtendData {
			if err := read(InvoiceSource{Kind: "extension", ID: strconv.Itoa(i), FileLoc: location}); err != nil {
				return InvoiceReport{}, err
			}
		}
	}
	if inline {
		location := r.documentRoot
		if strings.TrimSpace(doc.Extensions.Path) != "" {
			location = r.ResPath(doc.Extensions.Path)
		}
		data, err := r.invoiceData(ctx, location)
		if err == nil {
			err = invoiceExtensionData(ctx, data, location, consume)
		}
		if count > invoiceSourceLimit || total > invoiceTotalLimit {
			return InvoiceReport{}, fmt.Errorf("invoice extraction limit exceeded")
		}
		if err != nil {
			report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-data", Message: err.Error(), Source: InvoiceSource{Kind: "extension", FileLoc: location}})
		}
	}
	attachments, err := r.AttachmentsContext(ctx)
	if err != nil {
		report.Issues = append(report.Issues, InvoiceIssue{Code: "read-error", Message: err.Error(), Source: InvoiceSource{Kind: "attachment"}})
	}
	for _, attachment := range attachments {
		format := strings.ToLower(strings.TrimSpace(attachment.Format))
		ext := strings.ToLower(path.Ext(attachment.FileLoc))
		if format != "xml" && format != "xbrl" && ext != ".xml" && ext != ".xbrl" {
			continue
		}
		if err := read(InvoiceSource{Kind: "attachment", ID: attachment.ID, FileLoc: attachment.FileLoc}); err != nil {
			return InvoiceReport{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return InvoiceReport{}, err
	}
	payloads := make(map[[3]string]bool)
	for _, value := range report.Invoices {
		if value.Standard != ofdNamespace2016 && value.Number != "" {
			payloads[[3]string{value.Kind, value.Code, value.Number}] = true
		}
	}
	filtered := report.Invoices[:0]
	for _, value := range report.Invoices {
		if value.Standard != ofdNamespace2016 || !payloads[[3]string{value.Kind, value.Code, value.Number}] {
			filtered = append(filtered, value)
		}
	}
	report.Invoices = filtered
	invoiceSourceConflicts(&report)
	if err := ctx.Err(); err != nil {
		return InvoiceReport{}, err
	}
	if len(report.Invoices) == 0 && len(report.Issues) == 0 {
		report.Issues = append(report.Issues, InvoiceIssue{Code: "missing-data", Message: "no structured invoice data"})
	}
	return report, nil
}

// invoiceSourceConflicts 报告同票种、代码和号码的跨来源字段差异，不合并独立记录
// 入参: report 当前文档及版本的发票报告
func invoiceSourceConflicts(report *InvoiceReport) {
	seen := make(map[[3]string]map[string]string)
	for _, invoice := range report.Invoices {
		if invoice.Number == "" {
			continue
		}
		key := [3]string{invoice.Kind, invoice.Code, invoice.Number}
		previous := seen[key]
		if previous == nil {
			previous = make(map[string]string)
			seen[key] = previous
		}
		fields := []struct{ name, value string }{
			{"date", invoice.Date}, {"currency", invoice.Currency}, {"status", invoice.Status},
			{"amount", invoice.Amount}, {"taxAmount", invoice.TaxAmount}, {"totalAmount", invoice.TotalAmount},
			{"buyer.name", invoice.Buyer.Name}, {"buyer.taxId", invoice.Buyer.TaxID},
			{"seller.name", invoice.Seller.Name}, {"seller.taxId", invoice.Seller.TaxID},
		}
		for _, field := range fields {
			if field.value == "" {
				continue
			}
			value := strings.TrimSpace(field.value)
			if old, ok := previous[field.name]; ok && old != value {
				if (field.name == "amount" || field.name == "taxAmount" || field.name == "totalAmount") && invoiceDecimalEqual(old, value) {
					continue
				}
				source := InvoiceSource{}
				if len(invoice.Sources) > 0 {
					source = invoice.Sources[0]
				}
				report.Issues = append(report.Issues, InvoiceIssue{Code: "source-conflict", Message: "invoice field differs between sources", Source: source, Field: field.name})
			} else {
				previous[field.name] = value
			}
		}
	}
}

// invoiceData 有界读取包内候选载荷，解压及读取期间响应取消
// 入参: ctx 取消上下文, location 规范化包内路径
// 返回: []byte XML数据, error 路径、限额、读取或取消错误
func (r *Reader) invoiceData(ctx context.Context, location string) ([]byte, error) {
	if !validPackagePath(location) {
		return nil, fmt.Errorf("invalid invoice resource path")
	}
	stream, err := r.openFile(location)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(imageInput{ReadCloser: stream, ctx: ctx}, invoiceXMLLimit+1))
	if err != nil {
		return data, err
	}
	if len(data) > invoiceXMLLimit {
		return data, fmt.Errorf("invoice XML size limit exceeded")
	}
	return data, ctx.Err()
}

// invoiceExtensionData 提取扩展Data直接子树，复用XML片段编码并保留继承的命名空间
// 入参: ctx 取消上下文, data 扩展或文档XML, location 包内位置, consume 候选处理函数
// 返回: error XML、限额或取消错误
func invoiceExtensionData(ctx context.Context, data []byte, location string, consume func(InvoiceSource, []byte, error) error) error {
	if _, err := parseStructure(ctx, data, CustomTag{}); err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	if root.name.Local == "Document" {
		root = root.child("Extensions")
	}
	if root == nil || root.name.Local != "Extensions" || classifyOFDNamespace(root.name.Space) == ofdXMLUnknown {
		return fmt.Errorf("invalid invoice extension container")
	}
	i := 0
	for _, extension := range root.children {
		if extension.name.Local != "Extension" || !ofdXMLChild(root.name, extension.name) {
			continue
		}
		j := 0
		for _, entry := range extension.children {
			if entry.name.Local != "Data" || !ofdXMLChild(extension.name, entry.name) {
				continue
			}
			for k, node := range entry.children {
				source := InvoiceSource{Kind: "extension", ID: strconv.Itoa(i), FileLoc: location, NodePath: fmt.Sprintf("/Extensions/Extension[%d]/Data[%d]/*[%d]", i+1, j+1, k+1)}
				payload, err := editorXMLStandaloneContext(ctx, data[node.start:node.end], node)
				if err := consume(source, payload, err); err != nil {
					return err
				}
			}
			j++
		}
		i++
	}
	return nil
}
