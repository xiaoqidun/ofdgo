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

package webui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/xiaoqidun/ofdgo"
)

// preparePrint 从当前编辑入口创建独立打印快照，业务选项由公开库统一校验
// 入参: args 排版选项及可选输出任务标识
// 返回: any 纸面布局, error 快照、权限或排版错误
func (e *businessEngine) preparePrint(args []platformValue) (any, error) {
	if e.currentSession == nil || len(args) == 0 {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	var options ofdgo.PrintOptions
	if err := json.Unmarshal([]byte(platformJSON(args[0])), &options); err != nil {
		return nil, err
	}
	ctx := e.operationContext()
	if len(args) > 1 {
		ctx = e.newOutputContext(args[1])
	}
	editor := e.currentEditor
	if editor == nil {
		var err error
		editor, err = e.currentSession.Reader.EditorContext(ctx)
		if err != nil {
			return nil, err
		}
	}
	source := e.currentSession.Renderer
	dirs, fonts := source.FontSources()
	document, err := editor.PreparePrint(ctx, options, ofdgo.WithRenderBackends(source.Backends()), ofdgo.WithFontDirs(dirs...), ofdgo.WithFontFS(fonts...))
	if err != nil {
		return nil, err
	}
	if e.printDocument != nil {
		_ = e.printDocument.Close()
	}
	e.printDocument = document
	data, err := json.Marshal(document.Layout())
	if err != nil {
		return nil, err
	}
	return successResult(platformParseJSON(data)), nil
}

// layoutPrint 在既有快照上调整排版，预览期间不重复复制源包
// 入参: args 新排版选项及可选输出任务标识
// 返回: any 新纸面布局, error 状态、权限或排版错误
func (e *businessEngine) layoutPrint(args []platformValue) (any, error) {
	if e.printDocument == nil || len(args) == 0 {
		return nil, fmt.Errorf("print document is not prepared")
	}
	var options ofdgo.PrintOptions
	if err := json.Unmarshal([]byte(platformJSON(args[0])), &options); err != nil {
		return nil, err
	}
	ctx := e.operationContext()
	if len(args) > 1 {
		ctx = e.newOutputContext(args[1])
	}
	next, err := e.printDocument.Reconfigure(ctx, options)
	if err != nil {
		return nil, err
	}
	_ = e.printDocument.Close()
	e.printDocument = next
	data, err := json.Marshal(next.Layout())
	if err != nil {
		return nil, err
	}
	return successResult(platformParseJSON(data)), nil
}

// printSide 按需输出一个纸面，不使用阅读区缓存或缩略图
// 入参: args 零基纸面索引及可选输出任务标识
// 返回: any 内嵌资源的SVG纸面, error 状态或渲染错误
func (e *businessEngine) printSide(args []platformValue) (any, error) {
	if e.printDocument == nil || len(args) == 0 {
		return nil, fmt.Errorf("print document is not prepared")
	}
	ctx := e.operationContext()
	if len(args) > 1 {
		ctx = e.newOutputContext(args[1])
	}
	var output bytes.Buffer
	if err := e.printDocument.RenderSVG(ctx, args[0].Int(), &output); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"svg": output.String()}), nil
}

// printPDF 流式输出和预览相同的打印布局，便于另存或交给第三方打印系统
// 入参: args 数据写出回调及可选取消检查回调
// 返回: any PDF结果及媒体类型, error 状态或输出错误
func (e *businessEngine) printPDF(args []platformValue) (any, error) {
	if e.printDocument == nil {
		return nil, fmt.Errorf("print document is not prepared")
	}
	if len(args) == 0 || args[0].Type() != platformTypeFunction {
		return nil, fmt.Errorf("missing output callback")
	}
	ctx := e.operationContext()
	if len(args) > 1 && args[1].Type() == platformTypeFunction {
		ctx = e.newOutputContext(args[1])
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[0]}, 1<<20)
	if err := e.printDocument.WritePDF(ctx, writer); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"mime": "application/pdf", "label": "PDF"}), nil
}

// discardPrint 释放当前打印快照，可重复调用
// 入参: _ 保留的调用参数
// 返回: any 空结果, error 资源释放错误
func (e *businessEngine) discardPrint(_ []platformValue) (any, error) {
	if e.printDocument != nil {
		err := e.printDocument.Close()
		e.printDocument = nil
		if err != nil {
			return nil, err
		}
	}
	return successResult(nil), nil
}
