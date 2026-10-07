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
	"maps"

	"github.com/xiaoqidun/pdfgo"
)

// pdfThreeDMarkupState 复用同页批注的宿主解析及模型校验，不保留模型解码缓冲
type pdfThreeDMarkupState struct {
	sources map[pdfgo.Reference]pdfgo.ThreeD
	sums    map[*pdfgo.Stream][16]byte
}

// threeDMarkupAnnotation 应用默认三维视图的批注可见性，其他视图批注保留为隐藏注解
// 入参: ctx 取消上下文, annotation 批注, markup 关联, state 同页缓存, strict 严格检查开关
// 返回: pdfgo.Annotation 静态批注, error 关联、校验或取消错误
func (p *pdfImporter) threeDMarkupAnnotation(ctx context.Context, annotation pdfgo.Annotation, markup *pdfgo.ThreeDMarkup, state *pdfThreeDMarkupState, strict bool) (pdfgo.Annotation, error) {
	if markup == nil {
		return annotation, nil
	}
	if strict {
		return annotation, &pdfgo.UnsupportedError{Feature: "3D markup view switching conversion"}
	}
	source, ok := state.sources[markup.Annotation.Reference]
	if !ok || markup.Annotation.Reference == (pdfgo.Reference{}) {
		var err error
		source, err = p.reader.ReadThreeDContext(ctx, markup.Annotation)
		if err != nil {
			return annotation, err
		}
		if markup.Annotation.Reference != (pdfgo.Reference{}) {
			if state.sources == nil {
				state.sources = make(map[pdfgo.Reference]pdfgo.ThreeD)
			}
			state.sources[markup.Annotation.Reference] = source
		}
	}
	if markup.Checksum != nil {
		sum, ok := state.sums[source.Stream]
		if !ok {
			var err error
			sum, err = pdfgo.ThreeDArtworkChecksum(ctx, source.Stream)
			if err != nil {
				return annotation, err
			}
			if state.sums == nil {
				state.sums = make(map[*pdfgo.Stream][16]byte)
			}
			state.sums[source.Stream] = sum
		}
		if sum != *markup.Checksum {
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D markup artwork checksum differs; view association may be stale"})
		}
	}
	visible := source.ActivationPolicy.Activation != "XA" && markup.MatchesView(source.SelectedView)
	if !visible {
		flags, err := p.reader.ReadAnnotationFlags(annotation)
		if err != nil {
			return annotation, err
		}
		annotation.Dictionary = maps.Clone(annotation.Dictionary)
		annotation.Dictionary["F"] = pdfgo.Integer(flags | 2)
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D markup default view visibility applied; view switching not transferred"})
	return annotation, ctx.Err()
}
