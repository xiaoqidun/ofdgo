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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/xiaoqidun/ofdgo"
)

// currentSession 当前WebUI文档会话
var currentSession *Session

// callbacks 浏览器回调函数引用
var callbacks []js.Func

// apiResult 浏览器接口返回结果
type apiResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// RunWASM 注册浏览器WASM接口并阻塞运行
func RunWASM() {
	registerCallback("ofdgoOpen", openDocument)
	registerCallback("ofdgoRenderPage", renderPage)
	registerCallback("ofdgoExportFormats", exportFormats)
	registerCallback("ofdgoExportPage", exportPage)
	registerCallback("ofdgoExportPDF", exportPDF)
	registerCallback("ofdgoFontSystemNames", fontSystemNames)
	select {}
}

// registerCallback 注册浏览器回调函数
// 入参: name 回调名称, fn 回调函数
func registerCallback(name string, fn func([]js.Value) (any, error)) {
	cb := js.FuncOf(func(this js.Value, args []js.Value) any {
		data, err := safeCall(fn, args)
		if err != nil {
			return encodeResult(apiResult{OK: false, Error: err.Error()})
		}
		return encodeResult(apiResult{OK: true, Data: data})
	})
	js.Global().Set(name, cb)
	callbacks = append(callbacks, cb)
}

// safeCall 调用浏览器回调并转换异常
// 入参: fn 回调函数, args 回调参数
// 返回: any 回调结果, error 错误信息
func safeCall(fn func([]js.Value) (any, error), args []js.Value) (data any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return fn(args)
}

// openDocument 打开OFD文档
// 入参: args 浏览器参数
// 返回: any 文档信息, error 错误信息
func openDocument(args []js.Value) (any, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("missing open arguments")
	}
	data, err := bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	fonts, err := fontsFromJS(args[1])
	if err != nil {
		return nil, err
	}
	renderAnnotations := args[2].Bool()
	if currentSession != nil {
		_ = currentSession.Close()
		currentSession = nil
	}
	session, err := Open(data, OpenOptions{Fonts: fonts, RenderAnnotations: renderAnnotations})
	if err != nil {
		return nil, err
	}
	currentSession = session
	return currentSession.Info(), nil
}

// renderPage 渲染OFD页面
// 入参: args 浏览器参数
// 返回: any 页面SVG结果, error 错误信息
func renderPage(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("missing page index")
	}
	return currentSession.RenderPageSVG(args[0].Int())
}

// exportFormats 获取导出格式
// 入参: args 浏览器参数
// 返回: any 导出格式, error 错误信息
func exportFormats(args []js.Value) (any, error) {
	return ExportFormats(), nil
}

// exportPage 导出OFD单页
// 入参: args 浏览器参数
// 返回: any 导出结果, error 错误信息
func exportPage(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("missing export page arguments")
	}
	data, format, err := currentSession.ExportPage(args[0].Int(), args[1].String())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"base64":    base64.StdEncoding.EncodeToString(data),
		"size":      len(data),
		"format":    format.Value,
		"label":     format.Label,
		"extension": format.Extension,
		"mime":      format.MIME,
	}, nil
}

// exportPDF 导出OFD文档为PDF
// 入参: args 浏览器参数
// 返回: any PDF结果, error 错误信息
func exportPDF(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	data, err := currentSession.ExportPDF()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"base64": base64.StdEncoding.EncodeToString(data),
		"size":   len(data),
	}, nil
}

// fontSystemNames 获取系统字体名称
// 入参: args 浏览器参数
// 返回: any 系统字体名称, error 错误信息
func fontSystemNames(args []js.Value) (any, error) {
	names := stringsFromJS(jsArg(args, 0))
	return ofdgo.FontSystemNames(names...), nil
}

// bytesFromJS 从浏览器值读取二进制数据
// 入参: value 浏览器值
// 返回: []byte 二进制数据, error 错误信息
func bytesFromJS(value js.Value) ([]byte, error) {
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

// fontsFromJS 从浏览器值读取字体文件
// 入参: value 浏览器值
// 返回: []FontFile 字体文件, error 错误信息
func fontsFromJS(value js.Value) ([]FontFile, error) {
	if value.IsUndefined() || value.IsNull() {
		return nil, nil
	}
	length := value.Get("length").Int()
	fonts := make([]FontFile, 0, length)
	for i := 0; i < length; i++ {
		item := value.Index(i)
		data, err := bytesFromJS(item.Get("data"))
		if err != nil {
			return nil, err
		}
		fonts = append(fonts, FontFile{Name: item.Get("name").String(), Data: data})
	}
	return fonts, nil
}

// stringsFromJS 从浏览器值读取字符串列表
// 入参: value 浏览器值
// 返回: []string 字符串列表
func stringsFromJS(value js.Value) []string {
	if value.IsUndefined() || value.IsNull() {
		return nil
	}
	if js.Global().Get("Array").Call("isArray", value).Bool() {
		length := value.Get("length").Int()
		items := make([]string, 0, length)
		for i := 0; i < length; i++ {
			item := value.Index(i)
			if !item.IsUndefined() && !item.IsNull() {
				items = append(items, item.String())
			}
		}
		return items
	}
	return []string{value.String()}
}

// jsArg 获取浏览器参数
// 入参: args 浏览器参数列表, index 参数索引
// 返回: js.Value 浏览器值
func jsArg(args []js.Value, index int) js.Value {
	if index < 0 || index >= len(args) {
		return js.Undefined()
	}
	return args[index]
}

// encodeResult 编码浏览器接口返回结果
// 入参: result 返回结果
// 返回: string JSON字符串
func encodeResult(result apiResult) string {
	data, err := json.Marshal(result)
	if err != nil {
		fallback, _ := json.Marshal(apiResult{OK: false, Error: err.Error()})
		return string(fallback)
	}
	return string(data)
}
