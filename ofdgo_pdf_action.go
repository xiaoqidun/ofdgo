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
	"math"
	"strconv"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfDestinationFontBackend 禁止内容度量忽略实际绘制所需的缺失字体
type pdfDestinationFontBackend struct {
	FontBackend
}

// ResolveFont 沿用字体匹配，只在实际绘制缺少字体数据时中止度量
// 入参: renderer 度量渲染器, id 字体标识, exact 是否禁止无关回退
// 返回: ResolvedFont 字体资源, error 解析或缺失错误
func (b pdfDestinationFontBackend) ResolveFont(renderer *Renderer, id string, exact bool) (ResolvedFont, error) {
	font, err := b.FontBackend.ResolveFont(renderer, id, exact)
	if err == nil && len(font.Data) == 0 {
		err = fmt.Errorf("PDF destination font %q unavailable", id)
	}
	return font, err
}

// documentOpenActions 转换文档初始目标与打开动作，不执行脚本或打开资源
// 入参: ctx 取消上下文
// 返回: error 动作、编码或取消错误
func (p *pdfImporter) documentOpenActions(ctx context.Context) error {
	raw, err := p.pdfCatalogObject("OpenAction")
	if err != nil {
		return err
	}
	opened, err := p.reader.ReadOpenAction(ctx)
	if err != nil {
		if err := p.recoverPDFObject(ctx, "OpenAction", raw, err); err != nil {
			return err
		}
	}
	var object pdfgo.Object
	if opened.Action != nil {
		object = opened.Action
	}
	if dest := opened.Destination; dest != nil {
		array := append(pdfgo.Array{dest.Page, dest.Mode}, dest.Parameters...)
		object = pdfgo.Dictionary{"S": pdfgo.Name("GoTo"), "D": array}
	}
	currentPage := 0
	actions, err := p.eventActions(ctx, object, "DO", pdfgo.Annotation{}, &currentPage, raw)
	if err != nil {
		return err
	}
	if err := p.editor.SetDocumentActions(actions); err != nil {
		return err
	}
	triggers, err := p.reader.ReadDocumentTriggers(ctx)
	if err != nil {
		object, readErr := p.pdfCatalogObject("AA")
		if readErr != nil {
			return readErr
		}
		return p.recoverPDFObject(ctx, "Document.AA", object, err)
	}
	for _, trigger := range triggers {
		if _, err := p.reader.ReadJavaScriptAction(ctx, trigger.Action); err != nil {
			if err := p.recoverPDFObject(ctx, "Document.AA."+string(trigger.Event), trigger.Action, err); err != nil {
				return err
			}
			continue
		}
		if err := p.unmappedTrigger(ctx, "document", trigger); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// pageOpenActions 将页面打开动作放在注解打开动作之前，关闭事件不改为打开事件
// 入参: ctx 取消上下文, page PDF页面
// 返回: error 事件、动作或取消错误
func (p *pdfImporter) pageOpenActions(ctx context.Context, page *pdfgo.Page) error {
	p.pageActionPage = p.page
	triggers, err := p.reader.ReadPageTriggers(ctx, page)
	if err != nil {
		return p.recoverPDFObject(ctx, "Page.AA", page.Dictionary["AA"], err)
	}
	for _, trigger := range triggers {
		if trigger.Event != "O" {
			if err := p.unmappedTrigger(ctx, "page", trigger); err != nil {
				return err
			}
			continue
		}
		actions, err := p.eventActions(ctx, trigger.Action, "PO", pdfgo.Annotation{}, &p.pageActionPage)
		if err != nil {
			return err
		}
		if err := p.editor.SetPageActions(p.page, actions); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// eventActions 按原顺序转换动作链，保留事件类型与链内跳转页状态
// 入参: ctx 取消上下文, object 动作对象, event OFD事件, source 触发注解，零值表示非图元动作, currentPage 当前页面, original 可选原始来源
// 返回: []Action 转换动作, error 动作或取消错误
func (p *pdfImporter) eventActions(ctx context.Context, object pdfgo.Object, event string, source pdfgo.Annotation, currentPage *int, original ...pdfgo.Object) ([]Action, error) {
	raw := object
	if len(original) != 0 {
		raw = original[0]
	}
	retained, warned := false, false
	warning := p.warning
	if warning != nil {
		p.warning = func(diagnostic pdfgo.Diagnostic) {
			warned = true
			warning(diagnostic)
		}
		defer func() { p.warning = warning }()
	}
	retain := func(cause error) error {
		if cause != nil && (p.warning == nil || !pdfAuxiliaryError(cause)) {
			return cause
		}
		if retained {
			if cause != nil {
				p.warning(pdfgo.Diagnostic{Message: "PDF Action." + event + " retained as metadata: " + cause.Error()})
			}
			return ctx.Err()
		}
		var err error
		if cause != nil {
			err = p.recoverPDFObject(ctx, "Action."+event, raw, cause)
		} else if p.warning != nil {
			err = p.preservePDFObject(ctx, "Action."+event, raw)
		}
		retained = err == nil
		return err
	}
	var actions []Action
	var conversionErr error
	err := p.reader.WalkActions(ctx, object, func(info pdfgo.ActionInfo) error {
		annotation := source
		annotation.Dictionary = pdfgo.Dictionary{"A": info.Dictionary}
		warned = false
		warnings := p.warningTotal
		converted, err := p.convertLinkActions(annotation, p.warning == nil, currentPage)
		if err != nil {
			conversionErr = retain(err)
			*currentPage = -1
			return conversionErr
		}
		if p.warning != nil && (converted == nil || warned || p.warningTotal != warnings) {
			if err := retain(nil); err != nil {
				conversionErr = err
				return err
			}
		}
		for _, action := range converted {
			action.Event = event
			actions = append(actions, action)
		}
		return nil
	})
	if conversionErr != nil {
		return nil, conversionErr
	}
	if err != nil {
		if err := retain(err); err != nil {
			return nil, err
		}
		*currentPage = -1
	}
	return actions, ctx.Err()
}

// unmappedTrigger 校验无法映射的事件链，按导入策略报告而不改变触发条件
// 入参: ctx 取消上下文, owner 事件归属, trigger PDF事件
// 返回: error 动作链、取消或严格模式错误
func (p *pdfImporter) unmappedTrigger(ctx context.Context, owner string, trigger pdfgo.ActionTrigger) error {
	if err := p.reader.WalkActions(ctx, trigger.Action, func(pdfgo.ActionInfo) error { return nil }); err != nil {
		return p.recoverPDFObject(ctx, owner+".AA."+string(trigger.Event), trigger.Action, err)
	}
	feature := owner + " trigger " + string(trigger.Event)
	return p.recoverPDFObject(ctx, owner+".AA."+string(trigger.Event), trigger.Action, &pdfgo.UnsupportedError{Feature: feature})
}

// annotationTriggerActions 按事件转换注解动作，页面打开动作独立于注解外观
// 入参: ctx 取消上下文, annotation PDF注解, event OFD事件
// 返回: []Action 动作序列, error 事件、动作或取消错误
func (p *pdfImporter) annotationTriggerActions(ctx context.Context, annotation pdfgo.Annotation, event string) ([]Action, error) {
	if annotation.Subtype == "Widget" && event == "CLICK" {
		return nil, ctx.Err()
	}
	triggers, err := p.reader.ReadAnnotationTriggers(ctx, annotation)
	if err != nil {
		return nil, p.recoverPDFObject(ctx, "Annotation.AA", annotation.Dictionary["AA"], err)
	}
	if len(triggers) == 0 {
		return nil, ctx.Err()
	}
	flags, err := p.reader.ReadAnnotationFlags(annotation)
	if err != nil {
		return nil, err
	}
	var converted []Action
	for _, trigger := range triggers {
		if trigger.Event == "PC" || trigger.Event == "PV" || trigger.Event == "PI" {
			if event == "PO" {
				if err := p.unmappedTrigger(ctx, "annotation", trigger); err != nil {
					return nil, err
				}
			}
			continue
		}
		if (trigger.Event == "PO") != (event == "PO") {
			continue
		}
		if flags&64 != 0 && (trigger.Event == "E" || trigger.Event == "X" || trigger.Event == "D" || trigger.Event == "U") {
			continue
		}
		switch trigger.Event {
		case "U", "PO":
		default:
			if err := p.unmappedTrigger(ctx, "annotation", trigger); err != nil {
				return nil, err
			}
			continue
		}
		currentPage := p.page
		if event == "PO" {
			currentPage = p.pageActionPage
		}
		var source pdfgo.Annotation
		if event == "CLICK" {
			source = annotation
		}
		actions, err := p.eventActions(ctx, trigger.Action, event, source, &currentPage)
		if err != nil {
			return nil, err
		}
		converted = append(converted, actions...)
		if event == "PO" {
			p.pageActionPage = currentPage
		}
	}
	if event == "PO" && len(converted) != 0 && (p.page < 0 || p.page >= len(p.editor.pages)) {
		return nil, fmt.Errorf("invalid annotation trigger page")
	}
	return converted, ctx.Err()
}

// destinationBounds 度量目标页内容并缓存页面内范围，不包含注解或像素取整
// 入参: page PDF目标页
// 返回: Box OFD内容范围, error 度量错误
func (p *pdfImporter) destinationBounds(page *pdfgo.Page) (Box, error) {
	if err := p.ctx.Err(); err != nil {
		return Box{}, err
	}
	if box, ok := p.destinationBoxes[page.Reference]; ok {
		return box, nil
	}
	if index, ok := p.pageIndexes[page.Reference]; ok {
		previous := p.page
		p.page = index
		defer func() { p.page = previous }()
	}
	matrix, width, height := codecpdf.PageMatrix(page)
	editor := NewEditor()
	defer editor.Close()
	editor.SetResources(p.resources)
	editor.SetRenderBackends(p.editor.Backends())
	editor.SetFontDirs(p.editor.fontDirs...)
	editor.SetFontFS(p.editor.fontFS...)
	editor.fontSourcesCache = p.renderer.fontSourcesCache
	editor.validatedPaths = p.editor.pathValidationCache()
	if _, err := editor.AddPage(width, height); err != nil {
		return Box{}, err
	}
	local := pdfImporter{
		resources: p.resources,
		ctx:       p.ctx, reader: p.reader, editor: editor, renderer: p.renderer,
		warning: p.warning,
		matrix:  matrix, pageBox: page.CropBox, pageWidth: width, pageHeight: height,
		rasterDPI: p.rasterDPI, fontIDs: make(map[*pdfgo.Font]string),
		resolveFile: p.resolveFile, resolveReference: p.resolveReference,
		references:       &codecpdf.ReferenceCache{},
		halftoneWarnings: make(map[*pdfgo.Halftone]bool),
	}
	defer local.references.Close()
	if err := local.compositePage(nil, func(visitor pdfgo.Visitor) error {
		return p.reader.WalkPage(p.ctx, page, visitor)
	}); err != nil {
		return Box{}, fmt.Errorf("PDF destination content: %w", err)
	}
	if err := local.commitObjects(); err != nil {
		return Box{}, err
	}
	reader, err := local.temporaryReader()
	if err != nil {
		return Box{}, err
	}
	defer reader.Close()
	renderer := p.renderer.syncChildRenderer(reader)
	renderer.transparentBackground = true
	renderer.renderAnnotations = false
	if renderer.backends.Fonts != nil {
		renderer.backends.Fonts = pdfDestinationFontBackend{renderer.backends.Fonts}
	}
	scene, err := renderer.CompilePageContext(p.ctx, &editor.pages[0])
	if err != nil {
		return Box{}, err
	}
	if scene == nil {
		return Box{}, fmt.Errorf("PDF destination content scene unavailable")
	}
	geometry, err := renderer.Geometry()
	if err != nil {
		return Box{}, err
	}
	box, err := scene.BoundsContext(p.ctx, geometry)
	if err != nil {
		return Box{}, err
	}
	left, top := math.Max(0, box.X), math.Max(0, box.Y)
	right, bottom := math.Min(width, box.X+box.W), math.Min(height, box.Y+box.H)
	box = Box{X: left, Y: top, W: right - left, H: bottom - top}
	if !finite(left) || !finite(top) || !finite(right) || !finite(bottom) || box.W <= 0 || box.H <= 0 {
		return Box{}, fmt.Errorf("PDF destination content bounds empty or invalid")
	}
	if err := p.ctx.Err(); err != nil {
		return Box{}, err
	}
	if p.destinationBoxes == nil {
		p.destinationBoxes = make(map[pdfgo.Reference]Box)
	}
	p.destinationBoxes[page.Reference] = box
	return box, nil
}

// outlines 按先序批量转换目录，保留层级、展开状态和动作顺序
// 入参: ctx 取消上下文
// 返回: error 严格模式转换错误或取消错误
func (p *pdfImporter) outlines(ctx context.Context) error {
	count := 0
	var conversionErr error
	data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) {
		x.root("Outlines", nil)
		depth := 0
		err := p.reader.WalkOutlines(ctx, func(level int, item pdfgo.OutlineItem) error {
			actions, err := p.outlineActions(ctx, item)
			if err != nil {
				conversionErr = err
				return err
			}
			for depth > level {
				x.end("OutlineElem")
				depth--
			}
			attrs := ofdAttrs{{Name: xml.Name{Local: "Title"}, Value: item.Title}}
			attrs.add("Expanded", strconv.FormatBool(item.Expanded))
			x.start("OutlineElem", attrs)
			x.actions(actions)
			depth++
			count++
			return x.err
		})
		if err != nil {
			x.err = err
			return
		}
		for depth > 0 {
			x.end("OutlineElem")
			depth--
		}
		x.end("Outlines")
	})
	if conversionErr != nil {
		return conversionErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.warning == nil || !pdfAuxiliaryError(err) {
			return err
		}
		object, readErr := p.pdfCatalogObject("Outlines")
		if readErr != nil {
			return readErr
		}
		return p.recoverPDFObject(ctx, "Outlines", object, err)
	}
	if count > 0 {
		p.editor.outlines = bytes.TrimPrefix(data, []byte(xml.Header))
	}
	return nil
}

// outlineActions 转换目录目标及动作链，不为无目标节点补充跳转
// 入参: ctx 取消上下文, item PDF目录节点
// 返回: []Action OFD动作序列, error 动作或取消错误
func (p *pdfImporter) outlineActions(ctx context.Context, item pdfgo.OutlineItem) ([]Action, error) {
	raw := item.Action
	object, err := p.reader.Resolve(item.Action)
	if err != nil {
		return nil, p.recoverPDFObject(ctx, "Outline.A", item.Action, err)
	}
	dest, err := p.reader.Resolve(item.Destination)
	if err != nil {
		return nil, p.recoverPDFObject(ctx, "Outline.Dest", item.Destination, err)
	}
	if dest != nil {
		if object != nil {
			return nil, p.recoverPDFObject(ctx, "Outline", pdfgo.Dictionary{"A": item.Action, "Dest": item.Destination}, fmt.Errorf("PDF outline has both destination and action"))
		}
		object = pdfgo.Dictionary{"S": pdfgo.Name("GoTo"), "D": dest}
		raw = item.Destination
	}
	currentPage := -1
	return p.eventActions(ctx, object, "CLICK", pdfgo.Annotation{}, &currentPage, raw)
}
