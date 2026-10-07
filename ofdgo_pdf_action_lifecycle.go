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
	"errors"
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// documentOpenActions 转换文档初始目标与打开动作，不执行脚本或打开资源
// 入参: ctx 取消上下文
// 返回: error 动作、编码或取消错误
func (p *pdfImporter) documentOpenActions(ctx context.Context) error {
	opened, err := p.reader.ReadOpenAction(ctx)
	if err != nil {
		if p.warning == nil || !errors.Is(err, pdfgo.ErrInvalidDestination) {
			return err
		}
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("invalid PDF initial destination ignored: %v", err)})
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
	actions, err := p.eventActions(ctx, object, "DO", &currentPage)
	if err != nil {
		return err
	}
	if err := p.editor.SetDocumentActions(actions); err != nil {
		return err
	}
	triggers, err := p.reader.ReadDocumentTriggers(ctx)
	if err != nil {
		return err
	}
	for _, trigger := range triggers {
		if _, err := p.reader.ReadJavaScriptAction(ctx, trigger.Action); err != nil {
			return err
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
		return err
	}
	for _, trigger := range triggers {
		if trigger.Event != "O" {
			if err := p.unmappedTrigger(ctx, "page", trigger); err != nil {
				return err
			}
			continue
		}
		actions, err := p.eventActions(ctx, trigger.Action, "PO", &p.pageActionPage)
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
// 入参: ctx 取消上下文, object 动作对象, event OFD事件, currentPage 当前页面
// 返回: []Action 转换动作, error 动作或取消错误
func (p *pdfImporter) eventActions(ctx context.Context, object pdfgo.Object, event string, currentPage *int) ([]Action, error) {
	var actions []Action
	err := p.reader.WalkActions(ctx, object, func(info pdfgo.ActionInfo) error {
		annotation := pdfgo.Annotation{Dictionary: pdfgo.Dictionary{"A": info.Dictionary}}
		action, err := p.linkAction(annotation, p.warning == nil, currentPage)
		if err != nil {
			var unsupported *pdfgo.UnsupportedError
			if event != "CLICK" && p.warning != nil && errors.Is(err, pdfgo.ErrInvalidAction) {
				p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("invalid PDF %s action ignored: %v", event, err)})
				return nil
			}
			if event != "CLICK" && p.warning != nil && errors.Is(err, pdfgo.ErrInvalidDestination) {
				p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("invalid PDF %s destination ignored: %v", event, err)})
				return nil
			}
			if event != "CLICK" && p.warning != nil && errors.As(err, &unsupported) {
				p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF %s action not transferred to OFD: %s", event, unsupported.Feature)})
				return nil
			}
			return err
		}
		if action != nil {
			action.Event = event
			actions = append(actions, *action)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return actions, ctx.Err()
}

// unmappedTrigger 校验无法映射的事件链，按导入策略报告而不改变触发条件
// 入参: ctx 取消上下文, owner 事件归属, trigger PDF事件
// 返回: error 动作链、取消或严格模式错误
func (p *pdfImporter) unmappedTrigger(ctx context.Context, owner string, trigger pdfgo.ActionTrigger) error {
	if err := p.reader.WalkActions(ctx, trigger.Action, func(pdfgo.ActionInfo) error { return nil }); err != nil {
		return err
	}
	feature := owner + " trigger " + string(trigger.Event)
	if p.warning == nil {
		return &pdfgo.UnsupportedError{Feature: feature}
	}
	p.warning(pdfgo.Diagnostic{Message: "PDF " + feature + " not transferred to OFD"})
	return nil
}
