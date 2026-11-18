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
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// invoiceTagType 标识发票票面编辑关联，业务数据保持原规范格式
const invoiceTagType = "OFDGo.Invoice"

// invoiceNamespace 表示发票编辑关联命名空间
const invoiceNamespace = "https://ofdgo.aite.me/ns/invoice/1"

// InvoiceCreateOptions 指定结构化附件名称和可选票面，未指定票面时不新增页面
type InvoiceCreateOptions struct {
	Name     string                  `json:"name,omitempty"`
	Layout   *InvoiceLayout          `json:"layout,omitempty"`
	Encoding *InvoiceEncodingOptions `json:"encoding,omitempty"`
}

// InvoiceUpdateResult 保存写入后的来源、摘要及票面同步诊断，不代表发票真伪或签名验证结论
type InvoiceUpdateResult struct {
	Source       InvoiceSource  `json:"source"`
	Digest       string         `json:"digest"`
	Synchronized bool           `json:"synchronized"`
	Issues       []InvoiceIssue `json:"issues,omitempty"`
}

// InvoiceCapabilities 发票操作能力
// Create表示同格式创建能力，Delete表示整源删除，Reasons按操作小写名称索引
type InvoiceCapabilities struct {
	Create   bool              `json:"create"`
	Edit     bool              `json:"edit"`
	Items    bool              `json:"items"`
	Bind     bool              `json:"bind"`
	Generate bool              `json:"generate"`
	Sync     bool              `json:"sync"`
	Delete   bool              `json:"delete"`
	Reasons  map[string]string `json:"reasons"`
}

// InvoiceCapabilities 获取当前文档版本的发票操作能力
// 入参: ctx 取消上下文, source 精确来源, index 来源内从0开始的发票索引
// 返回: InvoiceCapabilities 操作能力, error 来源、索引或读取错误
func (r *Reader) InvoiceCapabilities(ctx context.Context, source InvoiceSource, index int) (InvoiceCapabilities, error) {
	ctx = invoiceWriteContext(ctx)
	result := InvoiceCapabilities{Reasons: make(map[string]string)}
	data, err := r.ExportInvoice(ctx, source)
	if err != nil {
		return result, err
	}
	report, err := r.readInvoiceXML(ctx, data, source)
	if err != nil {
		return result, err
	}
	if index < 0 || index >= len(report.Invoices) {
		return result, fmt.Errorf("invoice index out of range")
	}
	if report.Invoices[index].Standard == ofdNamespace2016 {
		result.Create = true
		result.Delete = true
		for _, operation := range []string{"items", "bind", "generate"} {
			result.Reasons[operation] = "对象引用依赖已有图元，不定义从零业务创建、明细分组或独立票面绑定"
		}
		for _, editable := range invoiceReferenceEditability(report.Invoices[index]) {
			result.Edit = result.Edit || editable
		}
		result.Edit = result.Edit && len(report.Issues) == 0
		result.Sync = result.Edit
		result.Reasons["edit"] = "需核对原文字，多对象仅支持按原引用顺序及原码点边界等长修改，字体和布局须通过校验"
		result.Reasons["sync"] = result.Reasons["edit"]
		if !result.Edit {
			result.Reasons["edit"] = "没有有效且无共享的文字引用"
			result.Reasons["sync"] = result.Reasons["edit"]
		}
		return result, nil
	}
	format, known := invoiceWriteFormat(report.Invoices[index])
	result.Create = known && format.Create
	result.Edit = known && format.Edit
	result.Items = result.Edit && format.Items
	result.Bind = known
	result.Generate = known
	result.Delete = true
	if !result.Create {
		result.Reasons["create"] = "当前格式不支持创建"
	}
	if !result.Edit {
		result.Reasons["edit"] = "当前格式不支持编辑"
	}
	if !result.Items {
		result.Reasons["items"] = "当前格式不支持明细增删"
	}
	if !known {
		result.Reasons["bind"] = "当前格式未适配"
		result.Reasons["generate"] = "当前格式未适配"
	}
	entries, err := r.invoiceBindings(ctx, source, InvoiceDigest(data), true)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Sync = false
		result.Generate = false
		result.Bind = false
		result.Edit = false
		result.Delete = false
		result.Items = false
		for _, operation := range []string{"edit", "delete", "items", "sync", "generate", "bind"} {
			result.Reasons[operation] = "票面关联损坏，请先修复关联"
		}
		return result, nil
	}
	for _, entry := range entries {
		if entry.record.Invoice != index {
			continue
		}
		result.Generate = false
		result.Reasons["generate"] = "请先解除已有票面关联"
		result.Sync = result.Edit && len(entry.bindings) > 0
		if result.Sync {
			editor, err := r.EditorContext(ctx)
			if err != nil {
				return result, err
			}
			for _, binding := range entry.bindings {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				_, fieldErr := invoiceBindingValue(report.Invoices[index], binding)
				object, objectErr := editor.Object(binding.Page, binding.ObjectID)
				if fieldErr != nil || objectErr != nil || object.Type != "TextObject" {
					result.Sync = false
					result.Reasons["sync"] = "票面关联字段或图元已失效"
				}
			}
			if entry.record.Layout != nil {
				ids := make([]string, len(entry.record.Pages))
				for i, page := range entry.record.Pages {
					ids[i] = page.ID
				}
				current, err := editor.invoiceOwnedPages(ctx, ids)
				if err != nil || len(ids) == 0 || !slices.Equal(current, entry.record.Pages) || editor.source.document.Signatures != "" {
					result.Sync = false
					result.Reasons["sync"] = "生成票面已修改或签名，请重新绑定"
				}
			}
		}
	}
	if !result.Sync && result.Reasons["sync"] == "" {
		result.Reasons["sync"] = "当前发票没有可同步的票面关联"
	}
	return result, ctx.Err()
}

// InvoiceBindings 读取当前来源指定发票的持久化票面关联，页码按当前页序解析
// 入参: ctx 取消上下文, source 来源, index 来源内从0开始的发票索引
// 返回: []InvoiceBinding 图元关联, error 来源、摘要或引用错误
func (r *Reader) InvoiceBindings(ctx context.Context, source InvoiceSource, index int) ([]InvoiceBinding, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := r.ExportInvoice(ctx, source)
	if err != nil {
		return nil, err
	}
	report, err := r.readInvoiceXML(ctx, data, source)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(report.Invoices) {
		return nil, fmt.Errorf("invoice index out of range")
	}
	if report.Invoices[index].Standard == ofdNamespace2016 {
		return r.invoiceReferenceBindings(ctx, report.Invoices[index])
	}
	entries, err := r.invoiceBindings(ctx, source, InvoiceDigest(data), true)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.record.Invoice == index {
			return entry.bindings, nil
		}
	}
	return nil, nil
}

// CheckInvoiceAppearance 核对显式绑定的可见字符，忽略换行差异，不推断未绑定字段或判断签名有效性
// 入参: ctx 取消上下文, source 来源, index 来源内发票索引
// 返回: []InvoiceIssue 未绑定、缺失图元或文字差异, error 来源或结构错误
func (r *Reader) CheckInvoiceAppearance(ctx context.Context, source InvoiceSource, index int) ([]InvoiceIssue, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := r.ExportInvoice(ctx, source)
	if err != nil {
		return nil, err
	}
	report, err := r.readInvoiceXML(ctx, data, source)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(report.Invoices) {
		return nil, fmt.Errorf("invoice index out of range")
	}
	if report.Invoices[index].Standard == ofdNamespace2016 {
		return slices.Clone(report.Issues), ctx.Err()
	}
	bindings, err := r.InvoiceBindings(ctx, source, index)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return []InvoiceIssue{{Code: "appearance-unbound", Message: "invoice has no appearance bindings", Source: source}}, nil
	}
	editor, err := r.EditorContext(ctx)
	if err != nil {
		return nil, err
	}
	var issues []InvoiceIssue
	for _, binding := range bindings {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := invoiceBindingValue(report.Invoices[index], binding)
		if err != nil {
			return nil, err
		}
		object, err := editor.Object(binding.Page, binding.ObjectID)
		if err != nil || object.Type != "TextObject" {
			issues = append(issues, InvoiceIssue{Code: "appearance-missing", Message: "bound text object is missing", Source: source, Field: binding.Field})
			continue
		}
		if !invoiceAppearanceMatches(object.TextObject, value) {
			issues = append(issues, InvoiceIssue{Code: "appearance-mismatch", Message: "bound text differs from invoice data", Source: source, Field: binding.Field})
		}
	}
	return issues, nil
}

// GenerateInvoicePages 为已有发票生成信息页并保存字段关联，失败时整体回滚
// 入参: ctx 取消上下文, source 精确来源, index 来源内发票索引, layout 排版选项
// 返回: error 来源、关联、排版或提交错误
func (e *Editor) GenerateInvoicePages(ctx context.Context, source InvoiceSource, index int, layout InvoiceLayout) error {
	ctx = invoiceWriteContext(ctx)
	return e.Transaction(func(next *Editor) error {
		r, err := next.Reader(ctx)
		if err != nil {
			return err
		}
		defer r.Close()
		capabilities, err := r.InvoiceCapabilities(ctx, source, index)
		if err != nil {
			return err
		}
		if !capabilities.Generate {
			return fmt.Errorf("invoice appearance cannot be generated: %s", capabilities.Reasons["generate"])
		}
		data, err := r.ExportInvoice(ctx, source)
		if err != nil {
			return err
		}
		report, err := parseInvoiceXML(ctx, data, source)
		if err != nil {
			return err
		}
		bindings, err := next.AppendInvoicePages(ctx, report.Invoices[index], layout)
		if err != nil {
			return err
		}
		if err := next.SetInvoiceBindings(ctx, source, index, bindings); err != nil {
			return err
		}
		if err := next.markInvoicePages(ctx, source, index, layout); err != nil {
			return err
		}
		return ctx.Err()
	})
}

// Invoices 获取编辑状态的发票提取结果，不修改文档
// 返回: InvoiceReport 发票及诊断, error 快照或解析错误
func (e *Editor) Invoices() (InvoiceReport, error) {
	return e.InvoicesContext(context.Background())
}

// InvoicesContext 获取当前文档及版本的编辑快照并提取发票
// 入参: ctx 取消上下文
// 返回: InvoiceReport 发票及诊断, error 快照或解析错误
func (e *Editor) InvoicesContext(ctx context.Context) (InvoiceReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r, err := e.Reader(ctx)
	if err != nil {
		return InvoiceReport{}, err
	}
	defer r.Close()
	return r.InvoicesContext(ctx)
}

// CreateInvoice 按指定业务规范创建结构化附件及可选票面，失败时整体回滚
// 入参: ctx 取消上下文, invoice 业务字段, options 附件及票面选项
// 返回: InvoiceSource 新增来源, error 校验、生成或提交错误
func (e *Editor) CreateInvoice(ctx context.Context, invoice Invoice, options InvoiceCreateOptions) (InvoiceSource, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if invoice.Standard == ofdNamespace2016 {
		return e.createInvoiceReferences(ctx, invoice, options)
	}
	encoding := InvoiceEncodingOptions{}
	if options.Encoding != nil {
		encoding = *options.Encoding
	}
	data, err := EncodeInvoiceWithOptions(ctx, invoice, encoding)
	if err != nil {
		return InvoiceSource{}, err
	}
	return e.ImportInvoice(ctx, data, options)
}

// ImportInvoice 导入结构化发票原文，可同时生成带字段关联的票面，不执行联网查验
// 入参: ctx 取消上下文, data XML或XBRL原文, options 附件及票面选项
// 返回: InvoiceSource 新增来源, error 格式、票面或提交错误
func (e *Editor) ImportInvoice(ctx context.Context, data []byte, options InvoiceCreateOptions) (InvoiceSource, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return InvoiceSource{}, err
	}
	if invoiceReferenceRoot(root) {
		return e.importInvoiceReferences(ctx, data, options)
	}
	report, err := invoiceWritableReport(ctx, data)
	if err != nil {
		return InvoiceSource{}, err
	}
	if options.Layout != nil && len(report.Invoices) != 1 {
		return InvoiceSource{}, fmt.Errorf("invoice appearance requires one invoice per source")
	}
	name := options.Name
	if name == "" {
		name = "invoice.xml"
	}
	if ext := strings.ToLower(path.Ext(name)); ext != ".xml" && ext != ".xbrl" {
		return InvoiceSource{}, fmt.Errorf("invoice attachment requires XML or XBRL extension")
	}
	var source InvoiceSource
	err = e.Transaction(func(next *Editor) error {
		var bindings []InvoiceBinding
		if options.Layout != nil {
			bindings, err = next.AppendInvoicePages(ctx, report.Invoices[0], *options.Layout)
			if err != nil {
				return err
			}
		}
		id, err := next.AddAttachmentContext(ctx, name, data)
		if err != nil {
			return err
		}
		attachments, err := next.AttachmentsContext(ctx)
		if err != nil {
			return err
		}
		for _, attachment := range attachments {
			if attachment.ID == id {
				source = InvoiceSource{Kind: "attachment", ID: id, FileLoc: next.source.reader.ResPath(attachment.FileLoc)}
				break
			}
		}
		if source.FileLoc == "" {
			return fmt.Errorf("created invoice attachment not found")
		}
		if len(bindings) > 0 {
			if err := next.SetInvoiceBindings(ctx, source, 0, bindings); err != nil {
				return err
			}
			if err := next.markInvoicePages(ctx, source, 0, *options.Layout); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return InvoiceSource{}, err
	}
	return source, nil
}

// UpdateInvoice 原子替换指定来源并按显式关联更新票面，原始摘要用于拒绝过期修改
// 入参: ctx 取消上下文, source 来源, digest 读取时摘要, data 修改后的原文, synchronize 是否同步已绑定图元
// 返回: InvoiceUpdateResult 新来源及诊断, error 校验、绑定或提交错误
func (e *Editor) UpdateInvoice(ctx context.Context, source InvoiceSource, digest string, data []byte, synchronize bool) (InvoiceUpdateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	if source.Kind == "tag" && invoiceReferenceRoot(root) {
		var result InvoiceUpdateResult
		err := e.Transaction(func(next *Editor) error {
			updated, err := next.ReplaceInvoiceData(ctx, source, digest, data)
			if err != nil {
				return err
			}
			actual, err := next.ExportInvoice(ctx, updated)
			if err != nil {
				return err
			}
			result = InvoiceUpdateResult{Source: updated, Digest: InvoiceDigest(actual), Synchronized: true}
			r, err := next.Reader(ctx)
			if err != nil {
				return err
			}
			defer r.Close()
			signed, err := r.hasOutputSignatures(ctx)
			if err != nil {
				return err
			}
			if signed {
				result.Issues = append(result.Issues, InvoiceIssue{Code: "signature-recheck-required", Message: "modified signed content requires signature verification", Source: updated})
			}
			return ctx.Err()
		})
		if err != nil {
			return InvoiceUpdateResult{}, err
		}
		return result, nil
	}
	report, err := invoiceWritableReport(ctx, data)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	var result InvoiceUpdateResult
	err = e.Transaction(func(next *Editor) error {
		original, err := next.ExportInvoice(ctx, source)
		if err != nil {
			return err
		}
		previous, err := parseInvoiceXML(ctx, original, InvoiceSource{})
		if err != nil {
			return err
		}
		payloadSigned, err := invoicePayloadSignature(ctx, original)
		if err != nil {
			return err
		}
		r, err := next.Reader(ctx)
		if err != nil {
			return err
		}
		entries, err := r.invoiceBindings(ctx, source, digest, true)
		r.Close()
		if err != nil {
			return err
		}
		if synchronize && len(entries) == 0 {
			return fmt.Errorf("invoice has no appearance bindings")
		}
		pages := make(map[string]string)
		for _, entry := range entries {
			for i, binding := range entry.bindings {
				page, err := next.Page(binding.Page)
				if err != nil {
					return err
				}
				pages[fmt.Sprintf("%d/%d", entry.index, i)] = page.ID
			}
		}
		updated, err := next.ReplaceInvoiceData(ctx, source, digest, data)
		if err != nil {
			return err
		}
		actual, err := next.ExportInvoice(ctx, updated)
		if err != nil {
			return err
		}
		newDigest := InvoiceDigest(actual)
		for _, entry := range entries {
			if entry.record.Invoice < 0 || entry.record.Invoice >= len(report.Invoices) {
				return fmt.Errorf("bound invoice no longer exists")
			}
			entry.record.Kind, entry.record.ID, entry.record.NodePath = updated.Kind, updated.ID, updated.NodePath
			entry.record.Digest = newDigest
			if synchronize && entry.record.Layout != nil {
				if err := next.rebuildInvoicePages(ctx, report.Invoices[entry.record.Invoice], entry); err != nil {
					return err
				}
				continue
			}
			if entry.record.Invoice >= len(previous.Invoices) {
				return fmt.Errorf("bound invoice no longer exists")
			}
			originalBindings := slices.Clone(entry.bindings)
			entry.bindings, err = invoiceRemapBindings(previous.Invoices[entry.record.Invoice], report.Invoices[entry.record.Invoice], entry.bindings)
			if err != nil {
				return err
			}
			for i := range entry.bindings {
				id := pages[fmt.Sprintf("%d/%d", entry.index, i)]
				page := slices.IndexFunc(next.pages, func(page PageContent) bool { return sameResourceID(page.ID, id) })
				if page < 0 {
					return fmt.Errorf("invoice appearance page no longer exists")
				}
				entry.bindings[i].Page = page
			}
			if synchronize {
				if err := invoiceBindingCoverage(previous.Invoices[entry.record.Invoice], report.Invoices[entry.record.Invoice], originalBindings); err != nil {
					return err
				}
				if err := next.SyncInvoicePages(ctx, report.Invoices[entry.record.Invoice], entry.bindings); err != nil {
					return err
				}
			}
			encoded, err := json.Marshal(entry.record)
			if err != nil {
				return err
			}
			if err := next.editStructure(entry.index, func(data []byte, root *editorXML, _ CustomTag) ([]byte, error) {
				var patches []editorXMLPatch
				for i, binding := range entry.bindings {
					if binding.Field == originalBindings[i].Field {
						continue
					}
					patch, err := editorXMLAttributePatch(data, root.children[i], "Name", binding.Field)
					if err != nil {
						return nil, err
					}
					patches = append(patches, patch)
				}
				data = editorPatchXML(data, patches)
				root, err := parseEditorXMLContext(ctx, data)
				if err != nil {
					return nil, err
				}
				var text bytes.Buffer
				if err := xml.EscapeText(&text, encoded); err != nil {
					return nil, err
				}
				return editorXMLDirectText(data, root, text.Bytes())
			}); err != nil {
				return err
			}
		}
		result = InvoiceUpdateResult{Source: updated, Digest: newDigest, Synchronized: synchronize}
		if !synchronize {
			result.Issues = append(result.Issues, InvoiceIssue{Code: "appearance-not-synchronized", Message: "invoice appearance was not synchronized", Source: updated})
		}
		if next.source != nil {
			signed, err := next.source.reader.hasOutputSignatures(ctx)
			if err != nil {
				return err
			}
			signed = signed || next.source.document.Signatures != "" || payloadSigned
			if signed {
				result.Issues = append(result.Issues, InvoiceIssue{Code: "signature-review-required", Message: "file contains signatures; verify them after editing", Source: updated})
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	return result, nil
}

// EditInvoice 定点修改原始字段并提交结构化数据及可选票面，不覆盖其他事实
// 入参: ctx 取消上下文, source 来源, digest 读取时摘要, changes 字段修改, synchronize 是否同步票面
// 返回: InvoiceUpdateResult 新来源及诊断, error 读取、修改或提交错误
func (e *Editor) EditInvoice(ctx context.Context, source InvoiceSource, digest string, changes []InvoiceFieldChange, synchronize bool) (InvoiceUpdateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := e.ExportInvoice(ctx, source)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	if InvoiceDigest(data) != digest {
		return InvoiceUpdateResult{}, fmt.Errorf("invoice source changed")
	}
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	if source.Kind == "tag" && invoiceReferenceRoot(root) {
		return e.editInvoiceReferences(ctx, source, digest, changes, synchronize)
	}
	changes = slices.Clone(changes)
	for i := range changes {
		change := &changes[i]
		if change.Source.Kind != "" && change.Source.Kind != source.Kind || change.Source.ID != "" && change.Source.ID != source.ID || change.Source.FileLoc != "" && change.Source.FileLoc != source.FileLoc {
			return InvoiceUpdateResult{}, fmt.Errorf("invoice field belongs to another source")
		}
		if change.Revision != "" && change.Revision != digest {
			return InvoiceUpdateResult{}, fmt.Errorf("invoice field revision differs from source")
		}
		change.Revision = digest
		if source.NodePath != "" {
			if !strings.HasPrefix(change.Source.NodePath, source.NodePath+"/") {
				return InvoiceUpdateResult{}, fmt.Errorf("invoice field is outside selected inline source")
			}
			change.Source.NodePath = strings.TrimPrefix(change.Source.NodePath, source.NodePath)
		}
	}
	data, err = PatchInvoice(ctx, data, changes)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	return e.UpdateInvoice(ctx, source, digest, data, synchronize)
}

// SetInvoice 修改指定发票的业务字段和明细，保留原载荷中的未修改扩展
// 入参: ctx 取消上下文, source 来源, digest 读取时摘要, index 发票索引, invoice 新业务值, synchronize 是否同步票面
// 返回: InvoiceUpdateResult 新来源及诊断, error 读取、修改或提交错误
func (e *Editor) SetInvoice(ctx context.Context, source InvoiceSource, digest string, index int, invoice Invoice, synchronize bool) (InvoiceUpdateResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := e.ExportInvoice(ctx, source)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	if InvoiceDigest(data) != digest {
		return InvoiceUpdateResult{}, fmt.Errorf("invoice source changed")
	}
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	if source.Kind == "tag" && invoiceReferenceRoot(root) {
		return e.setInvoiceReferences(ctx, source, digest, index, invoice, synchronize)
	}
	data, err = UpdateInvoiceXML(ctx, data, index, invoice)
	if err != nil {
		return InvoiceUpdateResult{}, err
	}
	return e.UpdateInvoice(ctx, source, digest, data, synchronize)
}

// DeleteInvoice 移除指定来源和对应编辑关联，保留票面及其他入口共享的业务原文
// 入参: ctx 取消上下文, source 来源, digest 读取时摘要
// 返回: error 来源、摘要或提交错误
func (e *Editor) DeleteInvoice(ctx context.Context, source InvoiceSource, digest string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return e.Transaction(func(next *Editor) error {
		r, err := next.Reader(ctx)
		if err != nil {
			return err
		}
		entries, err := r.invoiceBindings(ctx, source, digest, false)
		structures, structureErr := r.StructureContext(ctx)
		r.Close()
		if err != nil {
			return err
		}
		if structureErr != nil {
			return structureErr
		}
		if err := next.DeleteInvoiceData(ctx, source, digest); err != nil {
			return err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			index := entries[i].index
			if source.Kind == "tag" {
				deleted, err := strconv.Atoi(source.ID)
				if err != nil {
					return err
				}
				if index > deleted {
					index--
				}
			}
			if err := next.DeleteStructure(index); err != nil {
				return err
			}
		}
		removed := make([]int, 0, len(entries)+1)
		for _, entry := range entries {
			removed = append(removed, entry.index)
		}
		if source.Kind == "tag" {
			index, err := strconv.Atoi(source.ID)
			if err != nil {
				return err
			}
			removed = append(removed, index)
		}
		if err := next.reindexInvoiceBindings(ctx, structures, removed, source); err != nil {
			return err
		}
		return ctx.Err()
	})
}

// SetInvoiceBindings 设置指定发票的显式票面关联，空列表清除关联，不自动识别相同文字
// 入参: ctx 取消上下文, source 来源, index 发票索引, bindings 字段与图元关联
// 返回: error 来源、字段、引用或提交错误
func (e *Editor) SetInvoiceBindings(ctx context.Context, source InvoiceSource, index int, bindings []InvoiceBinding) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return e.Transaction(func(next *Editor) error {
		r, err := next.Reader(ctx)
		if err != nil {
			return err
		}
		defer r.Close()
		data, err := r.ExportInvoice(ctx, source)
		if err != nil {
			return err
		}
		report, err := parseInvoiceXML(ctx, data, source)
		if err != nil {
			return err
		}
		if index < 0 || index >= len(report.Invoices) {
			return fmt.Errorf("invoice index out of range")
		}
		entries, err := r.invoiceBindings(ctx, source, InvoiceDigest(data), false)
		if err != nil {
			return err
		}
		record := invoiceBindingRecord{Kind: source.Kind, ID: source.ID, NodePath: source.NodePath, Digest: InvoiceDigest(data), Invoice: index}
		seen := make(map[string]bool)
		fields := make(map[string]bool)
		for _, binding := range bindings {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := invoiceBindingValue(report.Invoices[index], binding); err != nil {
				return err
			}
			page, err := next.Page(binding.Page)
			if err != nil {
				return err
			}
			object, err := next.Object(binding.Page, binding.ObjectID)
			if err != nil {
				return err
			}
			if object.Type != "TextObject" {
				return fmt.Errorf("invoice binding requires a text object")
			}
			key := editorResourceID(page.ID) + "/" + editorResourceID(binding.ObjectID)
			fieldKey := fmt.Sprintf("%s/%d", binding.Field, binding.Item)
			if seen[key] || fields[fieldKey] {
				return fmt.Errorf("duplicate invoice appearance target")
			}
			seen[key] = true
			fields[fieldKey] = true
		}
		root, err := next.invoiceBindingRoot(record, bindings)
		if err != nil {
			return err
		}
		structures, err := r.StructureContext(ctx)
		if err != nil {
			return err
		}
		for i, structure := range structures {
			if structure.TypeID != invoiceTagType || structure.Namespace != invoiceNamespace {
				continue
			}
			own := false
			for _, entry := range entries {
				own = own || entry.index == i && entry.record.Invoice == index
			}
			if own {
				continue
			}
			if structure.Error != "" || structure.Root == nil {
				return fmt.Errorf("invalid invoice appearance metadata")
			}
			var check func(*StructureNode) error
			check = func(node *StructureNode) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				for _, ref := range node.References {
					for _, id := range ref.ObjectIDs {
						if seen[editorResourceID(ref.PageID)+"/"+editorResourceID(id)] {
							return fmt.Errorf("text object is already bound to another invoice")
						}
					}
				}
				for j := range node.Children {
					if err := check(&node.Children[j]); err != nil {
						return err
					}
				}
				return nil
			}
			if err := check(structure.Root); err != nil {
				return err
			}
		}
		for _, entry := range entries {
			if entry.record.Invoice != index {
				continue
			}
			if len(bindings) == 0 {
				if err := next.DeleteStructure(entry.index); err != nil {
					return err
				}
				return ctx.Err()
			}
			if err := next.editStructure(entry.index, func(_ []byte, _ *editorXML, _ CustomTag) ([]byte, error) { return encodeStructureNode(root) }); err != nil {
				return err
			}
			return ctx.Err()
		}
		if len(bindings) == 0 {
			return ctx.Err()
		}
		_, err = next.AddStructure(CustomTag{TypeID: invoiceTagType, NameSpace: invoiceNamespace}, root)
		if err != nil {
			return err
		}
		return ctx.Err()
	})
}

// invoiceBindingRecord 保存当前文档内的来源选择器，资源路径变化时仍以入口和摘要核对载荷
type invoiceBindingRecord struct {
	Kind     string             `json:"kind"`
	ID       string             `json:"id"`
	NodePath string             `json:"nodePath,omitempty"`
	Digest   string             `json:"digest"`
	Invoice  int                `json:"invoice"`
	Layout   *InvoiceLayout     `json:"layout,omitempty"`
	Pages    []invoiceOwnedPage `json:"pages,omitempty"`
}

// invoiceOwnedPage 记录自动生成票面的标识和内容摘要，不覆盖后续人工修改
type invoiceOwnedPage struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// invoiceBindingEntry 保存标引索引及当前页序中的图元关联
type invoiceBindingEntry struct {
	index    int
	record   invoiceBindingRecord
	bindings []InvoiceBinding
}

// reindexInvoiceBindings 在删除入口后维护其他发票的标引序号及内联子树序号
// 入参: ctx 取消上下文, structures 删除前标引, removed 删除的标引索引, source 删除来源
// 返回: error 元数据或提交错误
func (e *Editor) reindexInvoiceBindings(ctx context.Context, structures []Structure, removed []int, source InvoiceSource) error {
	for i, structure := range structures {
		if err := ctx.Err(); err != nil {
			return err
		}
		if slices.Contains(removed, i) || structure.TypeID != invoiceTagType || structure.Namespace != invoiceNamespace {
			continue
		}
		if structure.Root == nil || structure.Error != "" {
			return fmt.Errorf("invalid invoice appearance metadata")
		}
		root := *structure.Root
		var record invoiceBindingRecord
		if err := json.Unmarshal([]byte(root.Text), &record); err != nil {
			return err
		}
		changed := false
		if record.Kind == "tag" {
			old, err := strconv.Atoi(record.ID)
			if err != nil {
				return err
			}
			index := old
			for _, deleted := range removed {
				if deleted < old {
					index--
				}
			}
			if index != old {
				record.ID, changed = strconv.Itoa(index), true
			}
		}
		if source.Kind == "extension" && source.NodePath != "" && record.Kind == source.Kind && record.ID == source.ID {
			position := strings.LastIndex(source.NodePath, "*")
			if position < 0 {
				return fmt.Errorf("invalid inline invoice source")
			}
			prefix := source.NodePath[:position]
			if strings.HasPrefix(record.NodePath, prefix+"*[") {
				deleted, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(source.NodePath, prefix+"*["), "]"))
				if err != nil {
					return err
				}
				index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(record.NodePath, prefix+"*["), "]"))
				if err != nil {
					return err
				}
				if index > deleted {
					record.NodePath, changed = fmt.Sprintf("%s*[%d]", prefix, index-1), true
				}
			}
		}
		if !changed {
			continue
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		root.Text, root.Children, root.References = string(encoded), nil, nil
		index := i
		for _, deleted := range removed {
			if deleted < i {
				index--
			}
		}
		if err := e.UpdateStructureNode(index, nil, root); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// invoiceBindings 读取入口和摘要匹配的关联，拒绝重复记录及损坏引用
// 入参: ctx 取消上下文, source 来源, digest 来源摘要, targets 是否解析图元引用
// 返回: []invoiceBindingEntry 关联记录, error 读取或结构错误
func (r *Reader) invoiceBindings(ctx context.Context, source InvoiceSource, digest string, targets bool) ([]invoiceBindingEntry, error) {
	structures, err := r.StructureContext(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := r.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	pages := signaturePageNumbers(doc)
	var entries []invoiceBindingEntry
	seen := make(map[int]bool)
	for i, structure := range structures {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if structure.TypeID != invoiceTagType || structure.Namespace != invoiceNamespace {
			continue
		}
		if structure.Error != "" || structure.Root == nil {
			return nil, fmt.Errorf("invalid invoice appearance metadata")
		}
		root := structure.Root
		if root.Name != "Invoice" || root.Namespace != invoiceNamespace {
			return nil, fmt.Errorf("invalid invoice appearance root")
		}
		var record invoiceBindingRecord
		if err := json.Unmarshal([]byte(root.Text), &record); err != nil {
			return nil, fmt.Errorf("invalid invoice appearance selector: %w", err)
		}
		if record.Kind != source.Kind || record.ID != source.ID || record.NodePath != source.NodePath {
			continue
		}
		if record.Digest != digest {
			continue
		}
		if record.Invoice < 0 || seen[record.Invoice] {
			return nil, fmt.Errorf("duplicate or invalid invoice appearance record")
		}
		seen[record.Invoice] = true
		entry := invoiceBindingEntry{index: i, record: record}
		if !targets {
			entries = append(entries, entry)
			continue
		}
		for _, field := range root.Children {
			if field.Namespace != invoiceNamespace || field.Name != "Field" || len(field.Children) != 1 {
				return nil, fmt.Errorf("invalid invoice appearance field")
			}
			binding := InvoiceBinding{Item: -1}
			for _, attr := range field.Attributes {
				if attr.Namespace != "" {
					return nil, fmt.Errorf("invalid invoice appearance attribute")
				}
				switch attr.Name {
				case "Name":
					binding.Field = attr.Value
				case "Item":
					binding.Item, err = strconv.Atoi(attr.Value)
					if err != nil {
						return nil, err
					}
				default:
					return nil, fmt.Errorf("unknown invoice appearance attribute")
				}
			}
			ref := field.Children[0]
			if ref.Name != "ObjectRef" || ref.Namespace != invoiceNamespace || len(ref.References) != 1 || len(ref.References[0].ObjectIDs) != 1 {
				return nil, fmt.Errorf("invalid invoice appearance reference")
			}
			page, _ := resourceValue(pages, ref.References[0].PageID)
			if page == 0 {
				return nil, fmt.Errorf("invoice appearance page not found")
			}
			binding.Page, binding.ObjectID = page-1, ref.References[0].ObjectIDs[0]
			entry.bindings = append(entry.bindings, binding)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// invoiceWritableReport 检查待写入载荷包含可识别发票且没有解析诊断
// 入参: ctx 取消上下文, data 结构化原文
// 返回: InvoiceReport 发票结果, error 格式或校验错误
func invoiceWritableReport(ctx context.Context, data []byte) (InvoiceReport, error) {
	report, err := parseInvoiceXML(ctx, data, InvoiceSource{})
	if err != nil {
		return InvoiceReport{}, err
	}
	if len(report.Invoices) == 0 {
		return InvoiceReport{}, fmt.Errorf("no supported invoice data")
	}
	if len(report.Issues) > 0 {
		return InvoiceReport{}, fmt.Errorf("invalid invoice data: %s", report.Issues[0].Message)
	}
	if err := invoiceValidateXMLWrite(ctx, nil, data); err != nil {
		return InvoiceReport{}, err
	}
	return report, nil
}

// invoicePayloadSignature 识别XML数字签名和官方税务签名段，不推断签名有效性
// 入参: ctx 取消上下文, data 结构化原文
// 返回: bool 是否存在签名声明, error XML或取消错误
func invoicePayloadSignature(ctx context.Context, data []byte) (bool, error) {
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return false, err
	}
	nodes := []*invoiceXMLNode{root}
	for len(nodes) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		node := nodes[len(nodes)-1]
		nodes = nodes[:len(nodes)-1]
		if node.name.Space == invoiceXMLSignature && node.name.Local == "Signature" || root.name.Space == "" && root.name.Local == "EInvoice" && node.parent == root && node.name.Space == "" && node.name.Local == "TaxBureauSignature" {
			return true, nil
		}
		nodes = append(nodes, node.children...)
	}
	return false, nil
}

// invoiceRemapBindings 按唯一事实身份维护索引关联，不限制未关联业务字段的修改
// 入参: before 原发票, after 新发票, bindings 原关联
// 返回: []InvoiceBinding 更新后的关联, error 身份缺失或歧义错误
func invoiceRemapBindings(before, after Invoice, bindings []InvoiceBinding) ([]InvoiceBinding, error) {
	result := slices.Clone(bindings)
	for i := range result {
		binding := &result[i]
		oldFacts, newFacts, prefix := before.Facts, after.Facts, "facts."
		if strings.HasPrefix(binding.Field, "items.") {
			if binding.Item < 0 || binding.Item >= len(before.Items) || binding.Item >= len(after.Items) {
				return nil, fmt.Errorf("bound invoice item no longer exists")
			}
			if len(before.Items) != len(after.Items) && !reflect.DeepEqual(before.Items[binding.Item], after.Items[binding.Item]) {
				return nil, fmt.Errorf("bound invoice item identity changed")
			}
			oldFacts, newFacts, prefix = before.Items[binding.Item].Fields, after.Items[binding.Item].Fields, "items.fields."
		}
		if !strings.HasPrefix(binding.Field, prefix) {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(binding.Field, prefix))
		if err != nil || index < 0 || index >= len(oldFacts) {
			return nil, fmt.Errorf("bound invoice fact no longer exists")
		}
		fact := oldFacts[index]
		identity := func(candidate InvoiceFact) bool {
			return candidate.Name == fact.Name && candidate.Namespace == fact.Namespace && candidate.Context == fact.Context && candidate.Unit == fact.Unit && slices.Equal(candidate.Dimensions, fact.Dimensions)
		}
		oldCount, match, count := 0, -1, 0
		for _, candidate := range oldFacts {
			if identity(candidate) {
				oldCount++
			}
		}
		for j, candidate := range newFacts {
			if identity(candidate) {
				count++
				match = j
			}
		}
		if oldCount != 1 || count != 1 || fact.Source.NodePath == "" || newFacts[match].Source.NodePath != fact.Source.NodePath {
			if reflect.DeepEqual(oldFacts, newFacts) {
				continue
			}
			return nil, fmt.Errorf("bound invoice fact identity is missing or ambiguous")
		}
		binding.Field = fmt.Sprintf("%s%d", prefix, match)
	}
	return result, nil
}

// invoiceBindingCoverage 检查修改字段的票面关联覆盖情况
// 入参: before 原发票, after 新发票, bindings 显式关联
// 返回: error 未绑定字段错误
func invoiceBindingCoverage(before, after Invoice, bindings []InvoiceBinding) error {
	type fieldKey struct {
		field string
		item  int
	}
	old, values := make(map[fieldKey]string), make(map[fieldKey]string)
	bound := make(map[fieldKey]bool)
	for _, field := range invoiceLayoutFields(before) {
		old[fieldKey{field.field, field.item}] = field.value
	}
	for _, field := range invoiceLayoutFields(after) {
		values[fieldKey{field.field, field.item}] = field.value
	}
	for _, binding := range bindings {
		bound[fieldKey{binding.Field, binding.Item}] = true
		var oldFacts, newFacts []InvoiceFact
		name := ""
		if strings.HasPrefix(binding.Field, "facts.") {
			oldFacts, newFacts, name = before.Facts, after.Facts, strings.TrimPrefix(binding.Field, "facts.")
		} else if strings.HasPrefix(binding.Field, "items.fields.") {
			if binding.Item < 0 || binding.Item >= len(before.Items) || binding.Item >= len(after.Items) {
				return fmt.Errorf("bound invoice item changed")
			}
			oldFacts, newFacts, name = before.Items[binding.Item].Fields, after.Items[binding.Item].Fields, strings.TrimPrefix(binding.Field, "items.fields.")
		}
		if name != "" {
			index, err := strconv.Atoi(name)
			if err != nil || index < 0 || index >= len(oldFacts) || index >= len(newFacts) {
				return fmt.Errorf("bound invoice fact changed")
			}
			a, b := oldFacts[index], newFacts[index]
			if a.Name != b.Name || a.Namespace != b.Namespace || a.Context != b.Context || a.Unit != b.Unit {
				return fmt.Errorf("bound invoice fact identity changed")
			}
			if mapped, ok := invoiceEDRMFactBinding(before, a); ok {
				if next, valid := invoiceEDRMFactBinding(after, b); valid && mapped.Field == next.Field && mapped.Item == next.Item {
					bound[fieldKey{mapped.Field, mapped.Item}] = true
				}
			}
		}
	}
	for key, value := range old {
		if value != values[key] && !bound[key] {
			return fmt.Errorf("changed invoice field %s has no appearance binding", key.field)
		}
	}
	for key, value := range values {
		if value != old[key] && !bound[key] {
			return fmt.Errorf("changed invoice field %s has no appearance binding", key.field)
		}
	}
	return nil
}

// invoiceOwnedPages 计算编辑快照中指定页面的语义摘要，忽略XML序列化差异
// 入参: ctx 取消上下文, ids 页面标识
// 返回: []invoiceOwnedPage 按指定顺序排列的摘要, error 页面或读取错误
func (e *Editor) invoiceOwnedPages(ctx context.Context, ids []string) ([]invoiceOwnedPage, error) {
	r, err := e.Reader(ctx)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	doc, err := r.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	pages := signaturePageNumbers(doc)
	result := make([]invoiceOwnedPage, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		index, _ := resourceValue(pages, id)
		if index == 0 {
			return nil, fmt.Errorf("generated invoice page not found")
		}
		page, err := r.PageContentByIndexContext(ctx, index-1)
		if err != nil {
			return nil, err
		}
		annotations, err := r.AnnotationInfosByIndex(index - 1)
		if err != nil {
			return nil, err
		}
		if len(annotations) != 0 {
			return nil, fmt.Errorf("generated invoice page has annotations; set explicit bindings before synchronizing")
		}
		data, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		result = append(result, invoiceOwnedPage{ID: id, Digest: InvoiceDigest(data)})
	}
	return result, nil
}

// markInvoicePages 保存新建票面的排版选项和页面摘要，供明细增删后安全重排
// 入参: ctx 取消上下文, source 来源, index 来源内发票索引, layout 排版选项
// 返回: error 关联或提交错误
func (e *Editor) markInvoicePages(ctx context.Context, source InvoiceSource, index int, layout InvoiceLayout) error {
	r, err := e.Reader(ctx)
	if err != nil {
		return err
	}
	data, err := r.ExportInvoice(ctx, source)
	if err != nil {
		r.Close()
		return err
	}
	entries, err := r.invoiceBindings(ctx, source, InvoiceDigest(data), true)
	r.Close()
	if err != nil {
		return err
	}
	position := slices.IndexFunc(entries, func(entry invoiceBindingEntry) bool { return entry.record.Invoice == index })
	if position < 0 {
		return fmt.Errorf("generated invoice binding record not found")
	}
	entry := entries[position]
	var ids []string
	for _, binding := range entry.bindings {
		page, err := e.Page(binding.Page)
		if err != nil {
			return err
		}
		if !slices.Contains(ids, page.ID) {
			ids = append(ids, page.ID)
		}
	}
	entry.record.Layout = &layout
	entry.record.Pages, err = e.invoiceOwnedPages(ctx, ids)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(entry.record)
	if err != nil {
		return err
	}
	return e.UpdateStructureNode(entry.index, nil, StructureNode{Name: "Invoice", Namespace: invoiceNamespace, Text: string(encoded)})
}

// rebuildInvoicePages 重排本库生成且未被修改的连续票面，保留前后无关页面及事务边界
// 入参: ctx 取消上下文, invoice 新发票数据, entry 已更新来源及摘要的关联
// 返回: error 页面变化、排版或提交错误
func (e *Editor) rebuildInvoicePages(ctx context.Context, invoice Invoice, entry invoiceBindingEntry) error {
	if len(entry.record.Pages) == 0 || entry.record.Layout == nil {
		return fmt.Errorf("generated invoice page ownership is missing")
	}
	if e.source != nil && e.source.document.Signatures != "" {
		return fmt.Errorf("signed invoice document requires explicit appearance bindings")
	}
	ids := make([]string, len(entry.record.Pages))
	indexes := make([]int, len(ids))
	for i, page := range entry.record.Pages {
		ids[i], indexes[i] = page.ID, -1
		for j := range e.pages {
			if sameResourceID(e.pages[j].ID, page.ID) {
				indexes[i] = j
				break
			}
		}
		if indexes[i] < 0 || i > 0 && indexes[i] != indexes[i-1]+1 {
			return fmt.Errorf("generated invoice pages were removed or reordered")
		}
	}
	current, err := e.invoiceOwnedPages(ctx, ids)
	if err != nil {
		return err
	}
	if !slices.Equal(current, entry.record.Pages) {
		return fmt.Errorf("generated invoice pages have been edited; set explicit bindings before synchronizing")
	}
	if err := invoiceCheckReflowReferences(ctx, e, ids, entry.index); err != nil {
		return err
	}
	first := e.PageCount()
	bindings, err := e.AppendInvoicePages(ctx, invoice, *entry.record.Layout)
	if err != nil {
		return err
	}
	var newIDs []string
	for i := first; i < e.PageCount(); i++ {
		newIDs = append(newIDs, e.pages[i].ID)
	}
	if err := e.DeletePages(indexes); err != nil {
		return err
	}
	newIndexes := make([]int, len(newIDs))
	for i := range newIndexes {
		newIndexes[i] = e.PageCount() - len(newIDs) + i
	}
	if err := e.MovePages(newIndexes, indexes[0]); err != nil {
		return err
	}
	for i := range bindings {
		bindings[i].Page = indexes[0] + bindings[i].Page - first
	}
	root, err := e.invoiceBindingRoot(entry.record, bindings)
	if err != nil {
		return err
	}
	if err := e.editStructure(entry.index, func(_ []byte, _ *editorXML, _ CustomTag) ([]byte, error) { return encodeStructureNode(root) }); err != nil {
		return err
	}
	entry.record.Pages, err = e.invoiceOwnedPages(ctx, newIDs)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(entry.record)
	if err != nil {
		return err
	}
	if err := e.UpdateStructureNode(entry.index, nil, StructureNode{Name: "Invoice", Namespace: invoiceNamespace, Text: string(encoded)}); err != nil {
		return err
	}
	return ctx.Err()
}

// invoiceBindingRoot 将已校验字段关联编码为标准图元引用
// 入参: record 来源及排版记录, bindings 当前页面关联
// 返回: StructureNode 标引根节点, error 页面或编码错误
func (e *Editor) invoiceBindingRoot(record invoiceBindingRecord, bindings []InvoiceBinding) (StructureNode, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return StructureNode{}, err
	}
	root := StructureNode{Name: "Invoice", Namespace: invoiceNamespace, Text: string(encoded)}
	for _, binding := range bindings {
		page, err := e.Page(binding.Page)
		if err != nil {
			return StructureNode{}, err
		}
		root.Children = append(root.Children, StructureNode{Name: "Field", Namespace: invoiceNamespace, Attributes: []StructureAttribute{{Name: "Name", Value: binding.Field}, {Name: "Item", Value: strconv.Itoa(binding.Item)}}, Children: []StructureNode{{Name: "ObjectRef", Namespace: invoiceNamespace, Attributes: []StructureAttribute{{Name: "PageRef", Value: page.ID}}, Text: binding.ObjectID}}})
	}
	return root, nil
}
