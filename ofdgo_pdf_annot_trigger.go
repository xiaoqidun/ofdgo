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

// annotationTriggerActions 按事件转换注解动作，页面打开动作独立于注解外观
// 入参: ctx 取消上下文, annotation PDF注解, event OFD事件
// 返回: []Action 动作序列, error 事件、动作或取消错误
func (p *pdfImporter) annotationTriggerActions(ctx context.Context, annotation pdfgo.Annotation, event string) ([]Action, error) {
	if annotation.Subtype == "Widget" && event == "CLICK" {
		return nil, ctx.Err()
	}
	triggers, err := p.reader.ReadAnnotationTriggers(ctx, annotation)
	if err != nil {
		return nil, err
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
			return nil, &pdfgo.UnsupportedError{Feature: "annotation trigger " + string(trigger.Event)}
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
