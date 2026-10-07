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

	"github.com/xiaoqidun/pdfgo"
)

// annotationTriggerActions 将释放点击和页面打开事件转换为OFD，不改变其他事件的触发条件
// 入参: ctx 取消上下文, annotation PDF注解
// 返回: []Action 点击动作, []Action 页面打开动作, error 事件、动作或取消错误
func (p *pdfImporter) annotationTriggerActions(ctx context.Context, annotation pdfgo.Annotation) ([]Action, []Action, error) {
	if annotation.Subtype == "Widget" {
		return nil, nil, nil
	}
	triggers, err := p.reader.ReadAnnotationTriggers(ctx, annotation)
	if err != nil {
		return nil, nil, err
	}
	if len(triggers) == 0 {
		return nil, nil, nil
	}
	flags, err := p.reader.ReadAnnotationFlags(annotation)
	if err != nil {
		return nil, nil, err
	}
	var clicks, opened []Action
	for _, trigger := range triggers {
		if flags&64 != 0 && (trigger.Event == "E" || trigger.Event == "X" || trigger.Event == "D" || trigger.Event == "U") {
			continue
		}
		event := ""
		switch trigger.Event {
		case "U":
			event = "CLICK"
		case "PO":
			event = "PO"
		default:
			return nil, nil, &pdfgo.UnsupportedError{Feature: "annotation trigger " + string(trigger.Event)}
		}
		currentPage := p.page
		if event == "PO" {
			currentPage = p.pageActionPage
		}
		actions, err := p.eventActions(ctx, trigger.Action, event, &currentPage)
		if err != nil {
			return nil, nil, err
		}
		if event == "CLICK" {
			clicks = append(clicks, actions...)
		} else {
			p.pageActionPage = currentPage
			opened = append(opened, actions...)
		}
	}
	if len(opened) != 0 && (p.page < 0 || p.page >= len(p.editor.pages)) {
		return nil, nil, fmt.Errorf("invalid annotation trigger page")
	}
	return clicks, opened, ctx.Err()
}
