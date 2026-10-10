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
	"errors"
	"fmt"
	"strconv"

	"github.com/xiaoqidun/pdfgo"
)

// outlines 按先序批量转换目录，保留层级、展开状态和动作顺序
// 入参: ctx 取消上下文
// 返回: error 严格模式转换错误或取消错误
func (p *pdfImporter) outlines(ctx context.Context) error {
	count := 0
	data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) {
		x.root("Outlines", nil)
		depth := 0
		err := p.reader.WalkOutlines(ctx, func(level int, item pdfgo.OutlineItem) error {
			actions, err := p.outlineActions(ctx, item)
			if err != nil {
				if p.warning == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errPDFMediaRead) {
					return err
				}
				p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF outline %q actions not transferred to OFD: %v", item.Title, err)})
				actions = nil
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
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.warning == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errPDFMediaRead) {
			return err
		}
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF outlines not transferred to OFD: %v", err)})
		return nil
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
	object, err := p.reader.Resolve(item.Action)
	if err != nil {
		return nil, err
	}
	dest, err := p.reader.Resolve(item.Destination)
	if err != nil {
		return nil, err
	}
	if dest != nil {
		if object != nil {
			return nil, fmt.Errorf("PDF outline has both destination and action")
		}
		object = pdfgo.Dictionary{"S": pdfgo.Name("GoTo"), "D": dest}
	}
	currentPage := -1
	var actions []Action
	err = p.reader.WalkActions(ctx, object, func(info pdfgo.ActionInfo) error {
		converted, err := p.linkActions(pdfgo.Annotation{Dictionary: pdfgo.Dictionary{"A": info.Dictionary}}, p.warning == nil, &currentPage)
		if err != nil {
			if errors.Is(err, errPDFMediaRead) {
				return err
			}
			var unsupported *pdfgo.UnsupportedError
			if p.warning != nil && (errors.As(err, &unsupported) || errors.Is(err, pdfgo.ErrInvalidDestination) || errors.Is(err, pdfgo.ErrInvalidAction)) {
				p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF outline action not transferred to OFD: %v", err)})
				currentPage = -1
				return nil
			}
			return err
		}
		if converted != nil {
			actions = append(actions, converted...)
		} else {
			currentPage = -1
		}
		return nil
	})
	return actions, err
}
