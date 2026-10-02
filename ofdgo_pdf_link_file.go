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
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// pdfAttachmentKey 按显示名与原始数据复用PDF附件，不合并名称不同的附件
type pdfAttachmentKey struct {
	name   string
	digest [32]byte
}

// pdfEmbeddedFile 保存目标路径的容器与原始文件，阅读器按需打开
type pdfEmbeddedFile struct {
	reader *pdfgo.Reader
	file   *pdfgo.FileSpecification
	data   []byte
}

// remoteLinkAction 保留调用方提供的远程文件，不生成OFD无法表达的文件内目标
// 入参: object 远程跳转动作, strict 是否禁止目标语义损失
// 返回: *Action 附件动作, error 结构、取消或转换错误
func (p *pdfImporter) remoteLinkAction(object pdfgo.Object, strict bool) (*Action, error) {
	action, err := p.reader.ReadExternalGoToAction(p.ctx, object)
	if err != nil {
		return nil, err
	}
	if strict {
		return nil, &pdfgo.UnsupportedError{Feature: "remote document destination conversion"}
	}
	if p.resolveFile == nil {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF remote link file unavailable: " + action.File.Name + "; appearance retained, remote destination not transferred to OFD"})
		return nil, nil
	}
	data, err := p.reader.ReadFileData(p.ctx, *action.File, p.resolveFile)
	if err != nil {
		return nil, err
	}
	id, err := p.attachmentFile(action.File.Name, data)
	if err != nil {
		return nil, err
	}
	message := "PDF remote link target retained as attachment; destination inside attached PDF not transferred to OFD"
	if action.NewWindow == nil {
		message += "; reader window preference not transferred"
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: message})
	return &Action{Event: "CLICK", GotoA: &GotoA{AttachID: id, NewWindow: action.NewWindow}}, nil
}

// embeddedLinkAction 按目标路径定位附件，回到源文档时保留页内目标
// 入参: object 嵌入文件动作, strict 是否禁止附件内目标语义损失
// 返回: *Action 附件动作, pdfgo.Object 源文档目标, error 目标或转换错误
func (p *pdfImporter) embeddedLinkAction(object pdfgo.Object, strict bool) (*Action, pdfgo.Object, error) {
	action, err := p.reader.ReadExternalGoToAction(p.ctx, object)
	if err != nil {
		return nil, nil, err
	}
	stack := []pdfEmbeddedFile{{reader: p.reader}}
	if action.File != nil {
		data, available, err := p.mediaData(p.ctx, *action.File)
		if err != nil || !available {
			return nil, nil, err
		}
		stack[0] = pdfEmbeddedFile{file: action.File, data: data}
	}
	var opened []*pdfgo.Reader
	defer func() {
		for _, reader := range opened {
			reader.Close()
		}
	}()
	open := func(file *pdfEmbeddedFile) error {
		if file.reader != nil {
			return nil
		}
		if err := p.ctx.Err(); err != nil {
			return err
		}
		if file.data == nil {
			var err error
			file.data, err = p.reader.ReadFileData(p.ctx, *file.file, p.resolveFile)
			if err != nil {
				return err
			}
		}
		reader, err := pdfgo.NewReaderWithOptions(bytes.NewReader(file.data), int64(len(file.data)), pdfgo.ReaderOptions{Warning: p.warning})
		if err != nil {
			return err
		}
		file.reader = reader
		opened = append(opened, reader)
		return p.ctx.Err()
	}
	for _, target := range action.Target {
		if err := p.ctx.Err(); err != nil {
			return nil, nil, err
		}
		if target.Relation == "P" {
			if len(stack) == 1 {
				if strict {
					return nil, nil, &pdfgo.UnsupportedError{Feature: "embedded link parent document unavailable"}
				}
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded link parent document unavailable; link omitted"})
				return nil, nil, nil
			}
			stack = stack[:len(stack)-1]
			continue
		}
		current := &stack[len(stack)-1]
		if err := open(current); err != nil {
			return nil, nil, err
		}
		file, err := current.reader.ReadEmbeddedTargetFile(p.ctx, target)
		if err != nil {
			if !strict && errors.Is(err, pdfgo.ErrDestinationNotFound) {
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("%v; link omitted", err)})
				return nil, nil, nil
			}
			return nil, nil, err
		}
		stack = append(stack, pdfEmbeddedFile{file: &file})
	}
	current := &stack[len(stack)-1]
	if current.file == nil {
		destination, err := current.reader.ResolveRemoteDestination(p.ctx, action.Destination)
		if err != nil {
			if !strict && errors.Is(err, pdfgo.ErrDestinationNotFound) {
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("%v; link omitted", err)})
				return nil, nil, nil
			}
			return nil, nil, err
		}
		if action.NewWindow != nil && *action.NewWindow {
			if strict {
				return nil, nil, &pdfgo.UnsupportedError{Feature: "embedded link to source document in new window"}
			}
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded link new window not transferred to OFD; source document destination retained"})
		}
		return nil, append(pdfgo.Array{destination.Page, destination.Mode}, destination.Parameters...), nil
	}
	if strict {
		return nil, nil, &pdfgo.UnsupportedError{Feature: "destination inside attached PDF"}
	}
	if current.data == nil {
		current.data, err = p.reader.ReadFileData(p.ctx, *current.file, p.resolveFile)
		if err != nil {
			return nil, nil, err
		}
	}
	id, err := p.attachmentFile(current.file.Name, current.data)
	if err != nil {
		return nil, nil, err
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded link target retained as attachment; destination inside attached PDF not transferred to OFD"})
	return &Action{Event: "CLICK", GotoA: &GotoA{AttachID: id, NewWindow: action.NewWindow}}, nil, nil
}

// attachmentFile 添加或复用PDF附件，缺少显示名时使用固定名称，不改原始文件内容
// 入参: name 附件显示名, data 原始文件数据
// 返回: string 附件标识, error 附件写入错误
func (p *pdfImporter) attachmentFile(name string, data []byte) (string, error) {
	if name == "" {
		name = "Attachment"
	}
	key := pdfAttachmentKey{name: name, digest: sha256.Sum256(data)}
	if id, ok := p.attachmentIDs[key]; ok {
		return id, nil
	}
	id, err := p.editor.AddAttachment(name, data)
	if err != nil {
		return "", err
	}
	if p.attachmentIDs == nil {
		p.attachmentIDs = make(map[pdfAttachmentKey]string)
	}
	p.attachmentIDs[key] = id
	return id, nil
}
