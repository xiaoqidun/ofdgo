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

package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// ReferenceCache 保存单次会话拥有的引用文档及页面，零值可用，须串行使用且使用后不应复制
// Close只释放缓存自行打开的阅读器，不关闭来源文档或调用方提供的阅读器
type ReferenceCache struct {
	readers map[[32]byte]*pdfgo.Reader
	streams map[*pdfgo.Stream]*pdfgo.Reader
	pages   map[referencePageKey]*pdfgo.Page
}

// Resolve 解析引用目标，目标不可用时按PDF标准保留代理内容
// 入参: ctx 取消上下文, source 来源文档, reference 引用目标, fileResolver 外部文件读取, referenceResolver 调用方页面解析, warning 诊断回调
// 返回: *pdfgo.Page 目标页面，不可用时为空, error 取消或调用方解析错误
func (c *ReferenceCache) Resolve(ctx context.Context, source *pdfgo.Reader, reference pdfgo.ReferenceXObject, fileResolver pdfgo.FileResolver, referenceResolver pdfgo.ReferenceResolver, warning func(pdfgo.Diagnostic)) (*pdfgo.Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if referenceResolver != nil {
		return referenceResolver(ctx, source, reference)
	}
	reader := c.streams[reference.File.Embedded]
	if reader == nil {
		if reference.File.Embedded == nil && fileResolver == nil {
			return referenceProxy(ctx, fmt.Errorf("external reference file unavailable"), warning)
		}
		data, err := source.ReadFileData(ctx, reference.File, fileResolver)
		if err != nil {
			return referenceProxy(ctx, err, warning)
		}
		key := sha256.Sum256(data)
		reader = c.readers[key]
		if reader == nil {
			reader, err = pdfgo.NewReaderWithOptions(bytes.NewReader(data), int64(len(data)), pdfgo.ReaderOptions{Warning: warning})
			if err != nil {
				return referenceProxy(ctx, err, warning)
			}
			if c.readers == nil {
				c.readers = make(map[[32]byte]*pdfgo.Reader)
			}
			c.readers[key] = reader
		}
		if reference.File.Embedded != nil {
			if c.streams == nil {
				c.streams = make(map[*pdfgo.Stream]*pdfgo.Reader)
			}
			c.streams[reference.File.Embedded] = reader
		}
	}
	key := referencePageKey{reader: reader, index: reference.PageIndex}
	if reference.PageLabel != nil {
		key.index, key.label, key.labeled = 0, *reference.PageLabel, true
	}
	if page := c.pages[key]; page != nil {
		return page, ctx.Err()
	}
	page, err := reader.ResolveReferencePage(ctx, reference)
	if err != nil {
		return referenceProxy(ctx, err, warning)
	}
	if c.pages == nil {
		c.pages = make(map[referencePageKey]*pdfgo.Page)
	}
	c.pages[key] = page
	return page, ctx.Err()
}

// Close 释放本次会话打开的引用文档并清空缓存，可重复调用，之后可重新解析
func (c *ReferenceCache) Close() {
	for _, reader := range c.readers {
		reader.Close()
	}
	c.readers, c.streams, c.pages = nil, nil, nil
}

// referencePageKey 区分目标文档、索引和文本标签，避免重复遍历页面树
type referencePageKey struct {
	reader  *pdfgo.Reader
	index   int64
	label   string
	labeled bool
}

// referenceProxy 记录目标不可用原因，不将合法的代理绘制视为转换失败
// 入参: ctx 取消上下文, cause 目标不可用原因, warning 诊断回调
// 返回: *pdfgo.Page 空目标, error 取消错误
func referenceProxy(ctx context.Context, cause error, warning func(pdfgo.Diagnostic)) (*pdfgo.Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return nil, cause
	}
	if warning != nil {
		warning(pdfgo.Diagnostic{Message: "reference XObject proxy retained: " + cause.Error()})
	}
	return nil, nil
}
