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
	"encoding/xml"
)

// pdfFormBufferLimit 限制表单编码暂存容量，不限制资源大小
const pdfFormBufferLimit = 8 << 20

// pdfFormResource 保存生成内容及资源编号，复用前核对完整内容
type pdfFormResource struct {
	content []byte
	id      string
}

// pdfFormCache 隔离编辑器的表单资源，复用编码缓冲而不共享缓存内容
type pdfFormCache struct {
	editor  *Editor
	entries renderCache[[32]byte, pdfFormResource]
	buffer  bytes.Buffer
}

// encode 按原顺序编码对象分块，不为缓存检查整理连续对象数组
// 入参: ctx 取消上下文, objects 表单对象, width 页面宽度, height 页面高度
// 返回: []byte 下次编码或重置前有效的内容, error 编码或取消错误
func (c *pdfFormCache) encode(ctx context.Context, objects *pdfObjectBuffer, width, height float64) ([]byte, error) {
	c.buffer.Reset()
	x := newOFDXML(&c.buffer)
	x.ctx = ctx
	x.root("Content", ofdAttrs{{Name: xml.Name{Local: "Width"}, Value: ofdNumber(width)},
		{Name: xml.Name{Local: "Height"}, Value: ofdNumber(height)}})
	for _, part := range objects.parts {
		for _, object := range part {
			x.object(object, false)
			if x.err != nil {
				return nil, x.err
			}
		}
	}
	x.end("Content")
	if err := x.finish(); err != nil {
		return nil, err
	}
	return c.buffer.Bytes(), nil
}

// reset 清空临时内容，释放超限缓冲，不修改已登记资源
func (c *pdfFormCache) reset() {
	if c.buffer.Cap() > pdfFormBufferLimit {
		c.buffer = bytes.Buffer{}
	} else {
		c.buffer.Reset()
	}
}

// compositeForm 将无需背景合成的普通表单保存为矢量资源，保留图元的实际坐标和裁剪
// 入参: nodes 普通表单连续图元
// 返回: bool 是否已保存为复合对象, error 检查或转换错误
func (p *pdfImporter) compositeForm(nodes []pdfCompositeNode) (bool, error) {
	if found, err := p.compositeHasTransfer(nodes, p.transferModel); err != nil || found {
		return false, err
	}
	for _, node := range nodes {
		if err := p.ctx.Err(); err != nil {
			return false, err
		}
		direct, err := p.directCompositeNode(node, p.compositeSpace)
		if err != nil || !direct {
			return false, err
		}
	}
	if err := p.flushPath(); err != nil {
		return false, err
	}
	parent := p.objects
	p.objects = nil
	defer func() { p.objects = parent }()
	visitor := p.visitor()
	for _, node := range nodes {
		if err := node.emit(visitor); err != nil {
			return false, err
		}
	}
	if err := p.flushPath(); err != nil {
		return false, err
	}
	if err := p.ctx.Err(); err != nil {
		return false, err
	}
	if p.objects.len() == 0 {
		return true, nil
	}
	if p.formResources == nil || p.formResources.editor != p.editor {
		p.formResources = &pdfFormCache{editor: p.editor, entries: renderCache[[32]byte, pdfFormResource]{limit: 32 << 20}}
	}
	cache := p.formResources
	defer cache.reset()
	content, err := cache.encode(p.ctx, p.objects, p.pageWidth, p.pageHeight)
	if err != nil {
		return false, err
	}
	key := sha256.Sum256(content)
	resource, found := cache.entries.get(key)
	if !found || !bytes.Equal(resource.content, content) {
		objects, err := p.objects.data(p.ctx)
		if err != nil {
			return false, err
		}
		id, err := p.objectValidation().addOwnedVector(p.ctx, objects, p.pageWidth, p.pageHeight)
		if err != nil {
			return false, err
		}
		resource = pdfFormResource{id: id}
		if len(content) <= cache.entries.limit-len(id)-128 {
			resource.content = bytes.Clone(content)
			cache.entries.put(key, resource, cap(resource.content)+len(id)+128)
		}
	}
	p.objects = parent
	p.appendObject(GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: CompositeGraphicUnit{
		Boundary: pdfBoundary(Box{W: p.pageWidth, H: p.pageHeight}), ResourceID: resource.id,
	}})
	parent = p.objects
	return true, nil
}
