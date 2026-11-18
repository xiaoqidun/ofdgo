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

//go:build js && wasm

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/xiaoqidun/ofdgo"
)

// 浏览器JS参数类型常量
const (
	platformTypeNumber   = js.TypeNumber
	platformTypeString   = js.TypeString
	platformTypeBoolean  = js.TypeBoolean
	platformTypeObject   = js.TypeObject
	platformTypeFunction = js.TypeFunction
)

// RunWASM 注册浏览器WASM接口并阻塞运行
func RunWASM() {
	resources, _ := ofdgo.NewResources(ofdgo.ResourceOptions{Parallelism: 1, MemoryLimit: 64 << 20, CacheBytes: 48 << 20, SnapshotBytes: 32 << 20})
	engines := &wasmEngines{resources: resources, engines: make(map[string]*businessEngine)}
	registerCallback("ofdgoEngineOpen", engines.open)
	registerAsyncCallback("ofdgoEngineCall", engines.call)
	registerCallback("ofdgoEngineClose", engines.close)
	select {}
}

// platformValue 浏览器参数值
type platformValue = js.Value

// wasmEngines 管理共享资源的独立浏览器业务会话
type wasmEngines struct {
	resources *ofdgo.Resources
	engines   map[string]*businessEngine
}

// open 创建具有稳定标识的独立业务会话
// 入参: args 会话标识
// 返回: any 空结果, error 参数或会话错误
func (w *wasmEngines) open(args []js.Value) (any, error) {
	if len(args) != 1 || args[0].Type() != js.TypeString || args[0].String() == "" {
		return nil, fmt.Errorf("missing engine ID")
	}
	id := args[0].String()
	if w.engines[id] != nil {
		return nil, fmt.Errorf("engine already exists: %s", id)
	}
	w.engines[id] = &businessEngine{resources: w.resources}
	return nil, nil
}

// call 按会话标识分派业务命令，统一绑定本次调用的取消上下文
// 入参: args 会话标识、命令、取消检查点及业务参数
// 返回: any 业务结果, error 调用错误
func (w *wasmEngines) call(args []js.Value) (any, error) {
	if len(args) < 3 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeString {
		return nil, fmt.Errorf("missing engine ID or operation")
	}
	e := w.engines[args[0].String()]
	if e == nil {
		return nil, fmt.Errorf("unknown engine: %s", args[0].String())
	}
	name := args[1].String()
	if name == "ofdgoSaveSnapshot" {
		snapshot := e.saveSnapshot
		e.saveSnapshot = nil
		if snapshot == nil {
			return nil, fmt.Errorf("save snapshot is not prepared")
		}
		output := &businessEngine{resources: e.resources, ctx: context.Background()}
		if args[2].Type() == js.TypeFunction {
			output.ctx = output.newOutputContext(args[2])
		}
		if err := output.ctx.Err(); err != nil {
			return nil, err
		}
		return output.saveEditor(snapshot, args[3:])
	}
	fn := e.businessCommands()[name]
	if fn == nil {
		return nil, fmt.Errorf("unknown operation: %s", name)
	}
	if err := e.checkDocument(name); err != nil {
		return nil, err
	}
	e.ctx = context.Background()
	if args[2].Type() == js.TypeFunction {
		e.ctx = e.newOutputContext(args[2])
	}
	ctx := e.ctx
	if e.currentSession != nil {
		e.currentSession.Renderer.OutputContext = ctx
	}
	defer func() {
		if e.ctx == ctx {
			e.ctx = nil
		}
		if e.currentSession != nil {
			e.currentSession.Renderer.OutputContext = context.Background()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.preparePDF(name, args[3:]); err != nil {
		return nil, err
	}
	return fn(args[3:])
}

// close 释放会话的文档、编辑器、导入及剪贴板，不关闭共享资源
// 入参: args 会话标识
// 返回: any 空结果, error 参数或关闭错误
func (w *wasmEngines) close(args []js.Value) (any, error) {
	if len(args) != 1 || args[0].Type() != js.TypeString || args[0].String() == "" {
		return nil, fmt.Errorf("missing engine ID")
	}
	id := args[0].String()
	e := w.engines[id]
	if e == nil {
		return nil, nil
	}
	delete(w.engines, id)
	_, _ = e.discardPrint(nil)
	e.clearPDF()
	e.clearImport()
	err := e.currentSession.Close()
	*e = businessEngine{}
	return nil, err
}

// awaitExport 等待浏览器处理导出检查点
// 入参: fn 浏览器回调, args 回调参数
// 返回: error 写入错误或取消原因
func awaitExport(fn js.Value, args ...any) error {
	done := make(chan error)
	callback := js.FuncOf(func(this js.Value, args []js.Value) any {
		var err error
		if args[1].Bool() {
			err = context.Canceled
		} else if message := args[0].String(); message != "" {
			err = fmt.Errorf("%s", message)
		}
		done <- err
		return nil
	})
	defer callback.Release()
	fn.Invoke(append(args, callback)...)
	return <-done
}

// registerCallback 注册浏览器回调函数
// 入参: name 回调名称, fn 回调函数
func registerCallback(name string, fn func([]js.Value) (any, error)) {
	cb := js.FuncOf(func(this js.Value, args []js.Value) any {
		return callbackResult(fn, args)
	})
	js.Global().Set(name, cb)
}

// registerAsyncCallback 注册异步回调，为浏览器处理数据和取消请求让出事件循环
// 入参: name 回调名称, fn 回调函数
func registerAsyncCallback(name string, fn func([]js.Value) (any, error)) {
	cb := js.FuncOf(func(this js.Value, args []js.Value) any {
		executor := js.FuncOf(func(this js.Value, callbacks []js.Value) any {
			resolve := callbacks[0]
			go func() {
				resolve.Invoke(callbackResult(fn, args))
			}()
			return nil
		})
		defer executor.Release()
		return js.Global().Get("Promise").New(executor)
	})
	js.Global().Set(name, cb)
}

// callbackResult 转换浏览器接口结果
// 入参: fn 回调函数, args 回调参数
// 返回: any 浏览器结果
func callbackResult(fn func([]js.Value) (any, error), args []js.Value) any {
	data, err := (&businessEngine{}).safeCall(fn, args)
	if err != nil {
		result := failureResult(err)
		return (&businessEngine{}).encodeResult(result)
	}
	if result, ok := data.(js.Value); ok {
		return result
	}
	return (&businessEngine{}).encodeResult(apiResult{OK: true, Data: data})
}

// loadConversionFont 按需读取浏览器已授权字体，不预读系统字体目录
// 入参: load 浏览器读取函数, name 字体文件名
// 返回: []byte 字体内容, error 读取错误或取消原因
func (e *businessEngine) loadConversionFont(load js.Value, name string) ([]byte, error) {
	done := make(chan error, 1)
	var data []byte
	callback := js.FuncOf(func(this js.Value, args []js.Value) any {
		var err error
		if args[2].Bool() {
			err = context.Canceled
		} else if message := args[1].String(); message != "" {
			err = fmt.Errorf("%s", message)
		} else {
			data, err = e.bytesFromJS(args[0])
		}
		done <- err
		return nil
	})
	defer callback.Release()
	load.Invoke(name, callback)
	err := <-done
	return data, err
}

// successResult 创建成功接口结果
// 入参: data 返回数据
// 返回: js.Value 接口结果
func successResult(data any) js.Value {
	result := js.Global().Get("Object").New()
	result.Set("ok", true)
	result.Set("data", data)
	return result
}

// bytesToJS 将字节数据转换为Uint8Array
// 入参: data 字节数据
// 返回: js.Value Uint8Array对象
func bytesToJS(data []byte) js.Value {
	value := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(value, data)
	return value
}

// bytesFromJS 从浏览器值读取二进制数据
// 入参: value 浏览器值
// 返回: []byte 二进制数据, error 错误信息
func (e *businessEngine) bytesFromJS(value js.Value) ([]byte, error) {
	if value.IsUndefined() || value.IsNull() {
		return nil, fmt.Errorf("missing binary data")
	}
	uint8Array := js.Global().Get("Uint8Array")
	arrayBuffer := js.Global().Get("ArrayBuffer")
	if value.InstanceOf(arrayBuffer) {
		value = uint8Array.New(value)
	}
	if !value.InstanceOf(uint8Array) {
		return nil, fmt.Errorf("binary data must be Uint8Array or ArrayBuffer")
	}
	data := make([]byte, value.Get("byteLength").Int())
	if n := js.CopyBytesToGo(data, value); n != len(data) {
		return nil, fmt.Errorf("copied %d of %d bytes", n, len(data))
	}
	return data, nil
}

// platformValueOf 转换浏览器参数
// 入参: v Go值
// 返回: platformValue 浏览器参数值
func platformValueOf(v any) platformValue { return js.ValueOf(v) }

// platformNull 返回空参数
// 返回: platformValue 空参数值
func platformNull() platformValue { return js.Null() }

// platformCopyBytes 复制浏览器字节
// 入参: dst 目标切片, src 浏览器字节对象
// 返回: int 复制字节数
func platformCopyBytes(dst []byte, src platformValue) int { return js.CopyBytesToGo(dst, src) }

// platformJSON 编码小参数
// 入参: v 浏览器参数值
// 返回: string JSON字符串
func platformJSON(v platformValue) string {
	return js.Global().Get("JSON").Call("stringify", v).String()
}

// platformParseJSON 解析结构参数
// 入参: data JSON字节
// 返回: map[string]any 解析后的键值结构
func platformParseJSON(data []byte) map[string]any {
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		panic(err)
	}
	return result
}

// inputFromValue 读取浏览器随机输入或二进制数据
// 入参: value 浏览器输入参数
// 返回: *ofdgo.Input 文档输入源, error 读取错误
func (e *businessEngine) inputFromValue(value platformValue) (*ofdgo.Input, error) {
	if value.Type() == platformTypeObject && !value.IsNull() && value.Get("read").Type() == platformTypeFunction {
		return ofdgo.NewInput(&browserReaderAt{read: value.Get("read"), size: int64(value.Get("size").Float())}, int64(value.Get("size").Float()))
	}
	data, err := e.bytesFromJS(value)
	if err != nil {
		return nil, err
	}
	return ofdgo.NewInput(bytes.NewReader(data), int64(len(data)))
}
