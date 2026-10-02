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
	"errors"
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// pdfReferencePageKey 区分目标文档、索引和文本标签，避免重复遍历页面树
type pdfReferencePageKey struct {
	reader  *pdfgo.Reader
	index   int64
	label   string
	labeled bool
}

// referencePage 解析引用目标，目标不可用时按PDF标准保留代理内容
// 入参: ctx 取消上下文, source 来源文档, reference 引用目标
// 返回: *pdfgo.Page 目标页面，不可用时为空, error 取消或调用方解析错误
func (p *pdfImporter) referencePage(ctx context.Context, source *pdfgo.Reader, reference pdfgo.ReferenceXObject) (*pdfgo.Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.resolveReference != nil {
		return p.resolveReference(ctx, source, reference)
	}
	reader := p.referenceStreams[reference.File.Embedded]
	if reader == nil {
		if reference.File.Embedded == nil && p.resolveFile == nil {
			return p.referenceProxy(ctx, fmt.Errorf("external reference file unavailable"))
		}
		data, err := source.ReadFileData(ctx, reference.File, p.resolveFile)
		if err != nil {
			return p.referenceProxy(ctx, err)
		}
		key := sha256.Sum256(data)
		reader = p.referenceReaders[key]
		if reader == nil {
			reader, err = pdfgo.NewReaderWithOptions(bytes.NewReader(data), int64(len(data)), pdfgo.ReaderOptions{Warning: p.warning})
			if err != nil {
				return p.referenceProxy(ctx, err)
			}
			p.referenceReaders[key] = reader
		}
		if reference.File.Embedded != nil {
			p.referenceStreams[reference.File.Embedded] = reader
		}
	}
	key := pdfReferencePageKey{reader: reader, index: reference.PageIndex}
	if reference.PageLabel != nil {
		key.index, key.label, key.labeled = 0, *reference.PageLabel, true
	}
	if page := p.referencePages[key]; page != nil {
		return page, ctx.Err()
	}
	page, err := reader.ResolveReferencePage(ctx, reference)
	if err != nil {
		return p.referenceProxy(ctx, err)
	}
	p.referencePages[key] = page
	return page, ctx.Err()
}

// referenceProxy 记录目标不可用原因，不将合法的代理绘制视为转换失败
// 入参: ctx 取消上下文, cause 目标不可用原因
// 返回: *pdfgo.Page 空目标, error 取消错误
func (p *pdfImporter) referenceProxy(ctx context.Context, cause error) (*pdfgo.Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return nil, cause
	}
	if p.warning != nil {
		p.warning(pdfgo.Diagnostic{Message: "reference XObject proxy retained: " + cause.Error()})
	}
	return nil, nil
}

// closeReferenceReaders 释放本次转换打开的引用文档，不关闭调用方提供的阅读器
func (p *pdfImporter) closeReferenceReaders() {
	for _, reader := range p.referenceReaders {
		reader.Close()
	}
	p.referenceReaders, p.referenceStreams = nil, nil
	p.referencePages = nil
}
