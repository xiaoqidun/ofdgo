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
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"strconv"
	"strings"
	"syscall/js"
	"time"

	"github.com/xiaoqidun/ofdgo"
)

// exportWriter 分块传递导出数据
type exportWriter struct {
	write js.Value
}

// Write 将数据块交给浏览器保存
// 入参: data 导出数据
// 返回: int 写入长度, error 错误信息
func (w exportWriter) Write(data []byte) (int, error) {
	size := len(data)
	for len(data) > 0 {
		n := min(len(data), 1<<20)
		if err := awaitExport(w.write, bytesToJS(data[:n])); err != nil {
			return size - len(data), err
		}
		data = data[n:]
	}
	return size, nil
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

// currentSession 当前WebUI文档会话
var currentSession *Session

// pendingImport 待插页文档，仅保留文件数据与索引，不创建渲染会话
var pendingImport *ofdgo.Reader

// copiedStyle 当前文档中的独立样式快照
var copiedStyle *ofdgo.GraphicObject

// apiResult 浏览器接口返回结果
type apiResult struct {
	OK            bool                     `json:"ok"`
	Error         string                   `json:"error,omitempty"`
	Data          any                      `json:"data,omitempty"`
	MissingGlyphs *ofdgo.MissingGlyphError `json:"missingGlyphs,omitempty"`
	ReasonCode    ofdgo.EditReason         `json:"reasonCode,omitempty"`
}

// RunWASM 注册浏览器WASM接口并阻塞运行
func RunWASM() {
	registerCallback("ofdgoOpen", openDocument)
	registerCallback("ofdgoConfigure", configureDocument)
	registerCallback("ofdgoDocumentInfo", documentInfo)
	registerCallback("ofdgoRenderPage", renderPage)
	registerCallback("ofdgoSVGFontData", svgFontData)
	registerCallback("ofdgoSearchPage", searchPage)
	registerCallback("ofdgoPageTextString", pageTextString)
	registerCallback("ofdgoExportFormats", exportFormats)
	registerCallback("ofdgoParsePageRange", parsePageRange)
	registerAsyncCallback("ofdgoExportPage", exportPage)
	registerAsyncCallback("ofdgoExportDocument", exportDocument)
	registerAsyncCallback("ofdgoExportAttachment", exportAttachment)
	registerCallback("ofdgoMatchFontFiles", matchFontFiles)
	registerCallback("ofdgoFontFaces", fontFaces)
	registerCallback("ofdgoFontFace", fontFace)
	registerCallback("ofdgoCreateDocument", createDocument)
	registerCallback("ofdgoEditDocument", editDocument)
	registerCallback("ofdgoUpdateInfo", updateInfo)
	registerCallback("ofdgoChangePage", changePage)
	registerCallback("ofdgoBatchPages", batchPages)
	registerCallback("ofdgoChangeOutline", changeOutline)
	registerCallback("ofdgoStyleObjects", styleObjects)
	registerCallback("ofdgoMoveOutline", moveOutline)
	registerCallback("ofdgoCaptureStyle", captureStyle)
	registerCallback("ofdgoPasteStyle", pasteStyle)
	registerCallback("ofdgoResizeObjects", resizeObjects)
	registerCallback("ofdgoLoadImport", loadImport)
	registerAsyncCallback("ofdgoImportPages", importPages)
	registerCallback("ofdgoInsertText", insertText)
	registerCallback("ofdgoUpdateText", updateText)
	registerCallback("ofdgoStyleText", styleText)
	registerCallback("ofdgoCheckTextFont", checkTextFont)
	registerCallback("ofdgoInsertImage", insertImage)
	registerCallback("ofdgoInsertShape", insertShape)
	registerCallback("ofdgoUpdatePathStyle", updatePathStyle)
	registerCallback("ofdgoReplaceImage", replaceImage)
	registerCallback("ofdgoEditorFont", editorFont)
	registerCallback("ofdgoAlignObject", alignObject)
	registerCallback("ofdgoTransformObject", transformObject)
	registerCallback("ofdgoReshapeObject", reshapeObject)
	registerCallback("ofdgoReshapeLine", reshapeLine)
	registerCallback("ofdgoResetImageCrop", resetImageCrop)
	registerCallback("ofdgoLayoutText", layoutText)
	registerCallback("ofdgoTransformObjects", transformObjects)
	registerCallback("ofdgoCompositeObjects", compositeObjects)
	registerCallback("ofdgoChangeCompositeObjects", changeCompositeObjects)
	registerCallback("ofdgoAlignObjects", alignObjects)
	registerCallback("ofdgoDeleteObjects", deleteObjects)
	registerCallback("ofdgoEraseObjects", eraseObjects)
	registerCallback("ofdgoEraseObjectsPath", eraseObjectsPath)
	registerCallback("ofdgoCopyObjects", copyObjects)
	registerCallback("ofdgoCaptureObjects", captureObjects)
	registerCallback("ofdgoPasteObjects", pasteObjects)
	registerCallback("ofdgoOrderObjects", orderObjects)
	registerCallback("ofdgoDistributeObjects", distributeObjects)
	registerCallback("ofdgoRotateObjects", rotateObjects)
	registerCallback("ofdgoFlipObjects", flipObjects)
	registerCallback("ofdgoCropImage", cropImage)
	registerCallback("ofdgoFitImage", fitImage)
	registerCallback("ofdgoPreviewImage", previewImage)
	registerCallback("ofdgoDeleteObject", deleteObject)
	registerCallback("ofdgoUndo", func([]js.Value) (any, error) { return restoreEditor(false) })
	registerCallback("ofdgoRedo", func([]js.Value) (any, error) { return restoreEditor(true) })
	registerAsyncCallback("ofdgoSaveDocument", saveDocument)
	select {}
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
	data, err := safeCall(fn, args)
	if err != nil {
		result := apiResult{Error: err.Error()}
		var failure *ofdgo.EditError
		if errors.As(err, &failure) {
			result.ReasonCode = failure.Code
		}
		if errors.As(err, &result.MissingGlyphs) {
			result.ReasonCode = ofdgo.EditMissingGlyphs
		}
		return encodeResult(result)
	}
	if result, ok := data.(js.Value); ok {
		return result
	}
	return encodeResult(apiResult{OK: true, Data: data})
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
	clearImport()
	currentEditor = nil
	copiedObjects = nil
	copiedStyle = nil
	if currentSession != nil {
		_ = currentSession.Close()
		currentSession = nil
	}
	session, err := Open(data, OpenOptions{Fonts: fonts, RenderAnnotations: renderAnnotations})
	if err != nil {
		return nil, err
	}
	currentSession = session
	return currentSession.Summary(), nil
}

// configureDocument 更新当前会话的字体和注解设置
// 入参: args 浏览器参数
// 返回: any 文档信息, error 错误信息
func configureDocument(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if !args[0].IsNull() && !args[0].IsUndefined() {
		fonts, err := fontsFromJS(args[0])
		if err != nil {
			return nil, err
		}
		if err := currentSession.SetFonts(fonts); err != nil {
			return nil, err
		}
		if currentEditor != nil {
			currentEditor.SetFontFS()
			if currentSession.fontFS != nil {
				currentEditor.SetFontFS(currentSession.fontFS)
			}
		}
	}
	if currentSession.Renderer.RenderAnnotations != args[1].Bool() {
		currentSession.Renderer.RenderAnnotations = args[1].Bool()
		clear(currentSession.textCache)
	}
	return currentSession.Summary(), nil
}

// documentInfo 获取首屏之后加载的字体统计和验签结果
// 入参: args 浏览器参数
// 返回: any 文档信息, error 错误信息
func documentInfo(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return currentSession.Info(), nil
}

// exportAttachment 分块导出附件
// 入参: args 浏览器参数
// 返回: any 附件数据, error 错误信息
func exportAttachment(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	stream, err := currentSession.Reader.OpenAttachment(args[0].String())
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	writer := bufio.NewWriterSize(exportWriter{write: args[1]}, 1<<20)
	if _, err := io.Copy(writer, stream); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{
		"mime": "application/octet-stream",
	}), nil
}

// svgFontData 读取SVG字体资源
// 入参: args 浏览器参数
// 返回: any 字体数据, error 错误信息
func svgFontData(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	data, err := currentSession.SVGFontData(args[0].String())
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(data)}), nil
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
	page, err := currentSession.RenderPageSVG(args[0].Int())
	if err != nil {
		return nil, err
	}
	text, err := currentSession.PageText(args[0].Int())
	if err != nil {
		return nil, err
	}
	textData, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	objects, err := editorObjects(args[0].Int(), text)
	if err != nil {
		return nil, err
	}
	links := make([]any, len(page.Links))
	for i, link := range page.Links {
		item := map[string]any{
			"uri":    link.URI,
			"x":      link.Box.X,
			"y":      link.Box.Y,
			"width":  link.Box.W,
			"height": link.Box.H,
		}
		if dest := link.Dest; dest != nil {
			item["dest"] = map[string]any{"type": dest.Type, "pageID": dest.PageID, "left": dest.Left, "top": dest.Top,
				"right": dest.Right, "bottom": dest.Bottom, "zoom": dest.Zoom}
		}
		links[i] = item
	}
	fonts := make([]any, len(page.Fonts))
	for i, font := range page.Fonts {
		fonts[i] = map[string]any{"name": font.Name, "weight": font.Weight, "style": font.Style}
	}
	return successResult(map[string]any{
		"index":   page.Index,
		"number":  page.Number,
		"id":      page.ID,
		"width":   page.Width,
		"height":  page.Height,
		"svg":     page.SVG,
		"links":   links,
		"fonts":   fonts,
		"images":  svgImagesToJS(page.Images),
		"text":    string(textData),
		"objects": objects,
	}), nil
}

// pageTextString 提取OFD页面原文
// 入参: args 浏览器参数
// 返回: any 页面原文, error 错误信息
func pageTextString(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return currentSession.PageTextString(args[0].Int())
}

// searchPage 搜索OFD页面文字
// 入参: args 浏览器参数
// 返回: any 搜索结果, error 错误信息
func searchPage(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return currentSession.SearchPage(args[0].Int(), args[1].String())
}

// exportFormats 获取导出格式
// 入参: args 浏览器参数
// 返回: any 导出格式, error 错误信息
func exportFormats(args []js.Value) (any, error) {
	return ExportFormats(), nil
}

// parsePageRange 解析当前文档的导出页码
// 入参: args 浏览器参数
// 返回: any 页面索引, error 错误信息
func parsePageRange(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	count, err := currentSession.Reader.PageCount()
	if err != nil {
		return nil, err
	}
	return ofdgo.ParsePageRange(args[0].String(), count)
}

// exportPage 导出OFD单页
// 入参: args 浏览器参数
// 返回: any 导出结果, error 错误信息
func exportPage(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if len(args) < 4 {
		return nil, fmt.Errorf("missing export page arguments")
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[3]}, 1<<20)
	format, err := currentSession.ExportPage(args[0].Int(), args[1].String(), args[2].Float(), writer)
	if err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{
		"label": format.Label,
		"mime":  format.MIME,
	}), nil
}

// exportDocument 导出OFD文档
// 入参: args 浏览器参数
// 返回: any 导出结果, error 错误信息
func exportDocument(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	renderer := currentSession.Renderer
	renderer.OnExportProgress = func(completed, total int) error {
		return awaitExport(args[4], completed, total)
	}
	defer func() { renderer.OnExportProgress = nil }()
	var indices []int
	if !args[2].IsNull() {
		indices = make([]int, args[2].Length())
		for i := range indices {
			indices[i] = args[2].Index(i).Int()
		}
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[3]}, 1<<20)
	format, err := currentSession.ExportDocument(args[0].String(), args[1].Float(), writer, indices...)
	if err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{
		"label": format.Label,
		"mime":  format.MIME,
	}), nil
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

// matchFontFiles 匹配文档所需的字体文件
// 入参: args 浏览器参数
// 返回: any 匹配的字体文件名称, error 错误信息
func matchFontFiles(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	return currentSession.Reader.MatchFontFiles(stringsFromJS(args[0]))
}

// fontFaces 读取上传字体的名称和集合索引
// 入参: args 字体数据
// 返回: any 字体列表, error 错误信息
func fontFaces(args []js.Value) (any, error) {
	data, err := bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	return (ofdgo.FontFile{Data: data}).Faces()
}

// fontFace 提取编辑时选定的字体，供输入预览和保存共用
// 入参: args 字体数据和零起始索引
// 返回: any 独立字体数据, error 错误信息
func fontFace(args []js.Value) (any, error) {
	data, err := bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	data, err = (ofdgo.FontFile{Data: data}).Face(args[1].Int())
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(data)}), nil
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
	items := make([]string, value.Length())
	for i := range items {
		items[i] = value.Index(i).String()
	}
	return items
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

// currentEditor 当前编辑文档
var currentEditor *ofdgo.Editor

// editorClipboard 当前编辑文档中的对象快照与剪贴板标识
type editorClipboard struct {
	token     string
	objects   []ofdgo.GraphicObject
	composite *ofdgo.CompositeSelection
}

// copiedObjects 当前对象剪贴板，不保存字体或图片的重复数据
var copiedObjects *editorClipboard

// editorInfo 编辑文档信息与操作状态
type editorInfo struct {
	DocumentInfo
	Revision         uint64   `json:"revision"`
	CanUndo          bool     `json:"canUndo"`
	CanRedo          bool     `json:"canRedo"`
	PageCapabilities []uint   `json:"pageCapabilities"`
	EditWarnings     []string `json:"editWarnings,omitempty"`
}

// editorPageInfo 页面操作结果与目标页面
type editorPageInfo struct {
	editorInfo
	PageIndex int `json:"pageIndex"`
}

// editorSelectionInfo 对象复制结果及新选区
type editorSelectionInfo struct {
	editorInfo
	SelectedIDs []string `json:"selectedIDs"`
}

// editorSummary 获取当前编辑文档状态
// 返回: editorInfo 文档信息
func editorSummary() editorInfo {
	info := editorInfo{DocumentInfo: currentSession.Summary(), Revision: currentEditor.Revision(), CanUndo: currentEditor.CanUndo(), CanRedo: currentEditor.CanRedo()}
	if !currentSession.doc.Permissions.Edit {
		info.EditWarnings = append(info.EditWarnings, "原文件声明不允许编辑")
	}
	if currentSession.doc.Signatures != "" {
		info.EditWarnings = append(info.EditWarnings, "修改文档可能使原签名失效")
	}
	info.PageCapabilities = make([]uint, currentEditor.PageCount())
	for i := range info.PageCapabilities {
		capability, _ := currentEditor.PageCapabilities(i)
		info.PageCapabilities[i] = pageCapabilityMask(capability)
	}
	return info
}

// pageCapabilityMask 按插入、复制、删除、移动和尺寸的固定顺序编码页面能力，避免重复传输字段名
// 入参: capability 页面能力
// 返回: uint 能力位标记
func pageCapabilityMask(capability ofdgo.PageCapabilities) uint {
	var mask uint
	for i, enabled := range [...]bool{capability.Insert, capability.Copy, capability.Delete, capability.Move, capability.Resize} {
		if enabled {
			mask |= 1 << i
		}
	}
	return mask
}

// restoreEditor 撤销或重做并更新预览
// 入参: redo 是否重做
// 返回: any 文档信息, error 错误信息
func restoreEditor(redo bool) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	apply := currentEditor.Undo
	if redo {
		apply = currentEditor.Redo
	}
	if !apply() {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// editorObjects 提供编辑画布的对象范围、文字排版及原始图片边界
// 入参: index 页面索引, text 页面文字
// 返回: []any 对象区域, error 错误信息
func editorObjects(index int, text *ofdgo.PageText) ([]any, error) {
	objects := []any{}
	if currentEditor == nil {
		return objects, nil
	}
	page, err := currentEditor.Page(index)
	if err != nil {
		return nil, err
	}
	textBoxes := make(map[string]ofdgo.Box)
	for _, run := range text.Runs {
		for _, box := range run.Boxes {
			if box.W <= 0 || box.H <= 0 {
				continue
			}
			prev, exists := textBoxes[run.ID]
			if !exists {
				textBoxes[run.ID] = box
				continue
			}
			x, y := math.Min(prev.X, box.X), math.Min(prev.Y, box.Y)
			textBoxes[run.ID] = ofdgo.Box{X: x, Y: y, W: math.Max(prev.X+prev.W, box.X+box.W) - x, H: math.Max(prev.Y+prev.H, box.Y+box.H) - y}
		}
	}
	fonts, err := currentSession.Reader.Fonts()
	if err != nil {
		return nil, err
	}
	fontNames := make(map[string]string, len(fonts))
	for _, font := range fonts {
		fontNames[font.ID] = font.FontName
	}
	for _, layer := range page.Content.Layer {
		for order, object := range layer.Objects {
			var id string
			switch object.Type {
			case "TextObject":
				id = object.TextObject.ID
			case "ImageObject":
				id = object.ImageObject.ID
			case "PathObject":
				id = object.PathObject.ID
			case "CompositeObject", "CompositeGraphicUnit":
				id = object.CompositeGraphicUnit.ID
			default:
				continue
			}
			capability, err := currentEditor.ObjectCapabilities(index, id)
			if err != nil {
				return nil, err
			}
			var box ofdgo.Box
			var contours []ofdgo.ObjectContour
			switch object.Type {
			case "TextObject":
				box = textBoxes[id]
			default:
				box, contours, err = currentSession.Renderer.ObjectGeometry(object, layer.DrawParam)
			}
			if err != nil {
				continue
			}
			if box.W > 0 && box.H > 0 {
				position, err := currentEditor.ObjectPosition(index, id)
				if err != nil {
					return nil, err
				}
				item := map[string]any{"id": id, "type": object.Type, "x": box.X, "y": box.Y, "width": box.W, "height": box.H, "order": order, "position": position.Index, "count": position.Count, "container": position.Container,
					"capabilities": editorCapabilities(capability, object.Type)}
				editorAppearance(item, object, capability.Paint, editorPathScale(object.PathObject))
				if object.Type == "PathObject" {
					path := object.PathObject
					kind, geometry := path.Shape()
					if kind != "" && capability.Update {
						item["shape"] = string(kind)
						item["geometry"] = map[string]any{"x": geometry.X, "y": geometry.Y, "width": geometry.W, "height": geometry.H}
					} else if outline, err := path.Outline(); err == nil {
						item["outline"] = outline
					}
				}
				if object.Type == "PathObject" || object.Type == "ImageObject" || object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit" {
					paths := make([]any, len(contours))
					for i, contour := range contours {
						paths[i] = map[string]any{"path": contour.Path, "evenOdd": contour.EvenOdd}
					}
					item["contours"] = paths
				}
				if object.Type == "ImageObject" && capability.Update {
					full, err := object.ImageObject.ImageBounds()
					if err != nil {
						return nil, err
					}
					item["imageBounds"] = editorBox(full)
				}
				if object.Type == "TextObject" {
					item["bounds"] = editorBox(box)
					value, layout := object.TextObject.TextLayout()
					item["text"], item["font"], item["fontName"], item["size"] = value, object.TextObject.Font, fontNames[object.TextObject.Font], object.TextObject.Size
					item["wrap"], item["align"], item["paragraphHeight"] = layout.Wrap, layout.Align, layout.LineHeight
					item["letterSpacing"] = layout.LetterSpacing
					item["leftIndent"], item["rightIndent"], item["firstLineIndent"] = layout.LeftIndent, layout.RightIndent, layout.FirstLineIndent
					if capability.Reflow {
						frame, err := object.TextObject.TextFrame()
						if err != nil {
							return nil, err
						}
						boundary, _ := ofdgo.ParseBox(object.TextObject.Boundary)
						matrix := ofdgo.TranslationMatrix(boundary.X, boundary.Y).Multiply(ofdgo.NewMatrix(object.TextObject.CTM))
						inverse, _ := matrix.Invert()
						local := inverse.TransformBox(box)
						width := math.Max(object.TextObject.Size, local.X+local.W)
						if layout.Wrap {
							width = frame.W
						}
						frame = ofdgo.Box{W: width, H: math.Max(object.TextObject.Size, local.Y+local.H)}
						item["textFrame"] = map[string]any{"width": frame.W, "height": frame.H, "matrix": editorMatrix(matrix)}
						if layout.Wrap {
							bounds := matrix.TransformBox(frame)
							item["x"], item["y"], item["width"], item["height"] = bounds.X, bounds.Y, bounds.W, bounds.H
						}
					}
					if codes := object.TextObject.TextCode; len(codes) > 1 {
						first, _ := strconv.ParseFloat(codes[0].Y, 64)
						second, _ := strconv.ParseFloat(codes[1].Y, 64)
						item["lineHeight"] = second - first
					}
				}
				objects = append(objects, item)
			}
		}
	}
	return objects, nil
}

// editorCapabilities 统一顶层与内部对象的操作能力和受限原因
// 入参: capability 库层能力, kind 对象类型
// 返回: map[string]any 前端能力
func editorCapabilities(capability ofdgo.ObjectCapabilities, kind string) map[string]any {
	result := map[string]any{"update": capability.Update, "paint": capability.Paint, "replaceFont": capability.ReplaceFont, "reflow": capability.Reflow, "layoutKnown": capability.LayoutKnown, "transform": capability.Transform, "arrange": capability.Arrange, "copy": capability.Copy, "delete": capability.Delete, "order": capability.Order, "reason": capability.Reason, "reasonCode": string(capability.ReasonCode),
		"replaceImage": capability.ReplaceImage, "cropImage": capability.CropImage, "fitImage": capability.FitImage, "resetCrop": capability.ResetCrop, "enter": capability.Transform && (kind == "CompositeObject" || kind == "CompositeGraphicUnit")}
	if missing := capability.MissingGlyphs; missing != nil {
		result["missingGlyphs"] = map[string]any{"fontID": missing.FontID, "characters": missing.Characters}
	}
	return result
}

// editorAppearance 提供对象自身透明度及有效绘制外观，不混入父对象透明度
// 入参: item 前端对象, object 有效样式, paint 是否可改色, scale 描边页面倍率
func editorAppearance(item map[string]any, object ofdgo.GraphicObject, paint bool, scale float64) {
	var alpha *int
	switch object.Type {
	case "TextObject":
		alpha = object.TextObject.Alpha
		if paint {
			item["color"] = editorColorHex(object.TextObject.FillColor)
		}
	case "ImageObject":
		alpha = object.ImageObject.Alpha
		item["imageBorder"] = object.ImageObject.Border != nil
	case "PathObject":
		path := object.PathObject
		alpha = path.Alpha
		item["dashPattern"], item["cap"], item["join"], item["dashOffset"] = path.DashPattern, path.Cap, path.Join, 0.0
		if path.DashOffset != nil {
			item["dashOffset"] = *path.DashOffset
		}
		if paint {
			item["fill"], item["stroke"] = path.Fill != nil && *path.Fill, path.Stroke == nil || *path.Stroke
			item["fillColor"], item["strokeColor"] = editorColorHex(path.FillColor), editorColorHex((*ofdgo.FillColor)(path.StrokeColor))
			item["lineWidth"] = path.LineWidth * scale
		}
	case "CompositeObject", "CompositeGraphicUnit":
		alpha = object.CompositeGraphicUnit.Alpha
	}
	item["alpha"] = 255
	if alpha != nil {
		item["alpha"] = *alpha
	}
}

// compositePath 解析画布复合对象路径
// 入参: value 顶层标识与逐层序号
// 返回: ofdgo.ObjectPath 库层路径, error 错误信息
func compositePath(value string) (ofdgo.ObjectPath, error) {
	parts := strings.Split(value, "/")
	path := ofdgo.ObjectPath{ID: parts[0]}
	for _, part := range parts[1:] {
		index, err := strconv.Atoi(part)
		if err != nil || index < 0 {
			return path, fmt.Errorf("invalid composite path")
		}
		path.Children = append(path.Children, index)
	}
	return path, nil
}

// compositeObjects 获取内部编辑范围，不重新渲染页面或传输图片
// 入参: args 页面索引与复合路径
// 返回: any 画布对象列表, error 错误信息
func compositeObjects(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	key := args[1].String()
	path, err := compositePath(key)
	if err != nil {
		return nil, err
	}
	members, err := currentEditor.CompositeObjects(args[0].Int(), path)
	if err != nil {
		return nil, err
	}
	fonts, err := currentSession.Reader.Fonts()
	if err != nil {
		return nil, err
	}
	fontNames := make(map[string]string, len(fonts))
	for _, font := range fonts {
		fontNames[font.ID] = font.FontName
	}
	objects := make([]any, 0, len(members))
	for i, member := range members {
		box := member.Bounds
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		item := map[string]any{"id": fmt.Sprintf("%s/%d", key, i), "type": member.Object.Type, "x": box.X, "y": box.Y, "width": box.W, "height": box.H, "order": i, "position": member.Position.Index, "count": member.Position.Count, "container": key + ":" + member.Position.Container, "scoped": true, "contours": member.Contours,
			"capabilities": editorCapabilities(member.Capabilities, member.Object.Type)}
		editorAppearance(item, member.Object, member.Capabilities.Paint, member.StrokeScale)
		if kind, geometry := member.Shape(); kind != "" {
			item["shape"], item["geometry"] = string(kind), editorBox(geometry)
		}
		if member.Object.Type == "TextObject" {
			object := member.Object.TextObject
			value, layout := object.TextLayout()
			item["text"] = value
			item["wrap"], item["align"], item["paragraphHeight"] = layout.Wrap, layout.Align, layout.LineHeight
			item["letterSpacing"] = layout.LetterSpacing
			item["leftIndent"], item["rightIndent"], item["firstLineIndent"] = layout.LeftIndent, layout.RightIndent, layout.FirstLineIndent
			item["font"], item["fontName"], item["size"] = object.Font, fontNames[object.Font], object.Size
			item["bounds"] = editorBox(box)
			if member.Capabilities.Reflow {
				inverse, _ := member.Matrix.Invert()
				local := inverse.TransformBox(box)
				width := math.Max(object.Size, local.X+local.W)
				if layout.Wrap {
					frame, err := object.TextFrame()
					if err != nil {
						return nil, err
					}
					width = frame.W
				}
				frame := ofdgo.Box{W: width, H: math.Max(object.Size, local.Y+local.H)}
				item["textFrame"] = map[string]any{"width": frame.W, "height": frame.H, "matrix": editorMatrix(member.Matrix)}
				if layout.Wrap {
					bounds := member.Matrix.TransformBox(frame)
					item["x"], item["y"], item["width"], item["height"] = bounds.X, bounds.Y, bounds.W, bounds.H
				}
			}
		}
		if member.Capabilities.CropImage {
			item["imageBounds"] = editorBox(box)
			item["cropped"] = member.Cropped
		}
		objects = append(objects, item)
	}
	return objects, nil
}

// changeCompositeObjects 提交内部选区操作，结构变化后返回新的选区路径
// 入参: args 页面索引、父路径、成员路径列表、操作及参数
// 返回: any 文档信息, error 错误信息
func changeCompositeObjects(args []js.Value) (any, error) {
	var selected []int
	result, err := changeObjects(func() error {
		key := args[1].String()
		path, err := compositePath(key)
		if err != nil {
			return err
		}
		indexes, err := compositeIndexes(key, stringsFromJS(args[2]))
		if err != nil {
			return err
		}
		page := args[0].Int()
		operation := args[3].String()
		switch operation {
		case "pasteStyle":
			if copiedStyle == nil {
				return fmt.Errorf("style clipboard is empty")
			}
			return currentEditor.CopyCompositeStyle(page, path, indexes, *copiedStyle)
		case "reshape", "line":
			if len(indexes) != 1 {
				return fmt.Errorf("shape editing requires one member")
			}
			box := ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()}
			if operation == "line" {
				return currentEditor.ReshapeCompositeLine(page, path, indexes[0], ofdgo.ShapeKind(args[8].String()), box)
			}
			return currentEditor.ReshapeCompositeObject(page, path, indexes[0], box)
		case "erase":
			return currentEditor.EraseCompositeObjects(page, path, indexes, ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()})
		case "erasePath":
			return currentEditor.EraseCompositeObjectsPath(page, path, indexes, pointsFromJS(args[4]))
		case "delete":
			return currentEditor.DeleteCompositeObjects(page, path, indexes)
		case "copy":
			selected, err = currentEditor.CopyCompositeObjects(page, path, indexes, args[4].Float(), args[5].Float())
			return err
		case "order":
			selected, err = currentEditor.OrderCompositeObjects(page, path, indexes, args[4].String())
			return err
		case "text", "textStyle":
			return changeCompositeText(page, path, indexes, operation, args[4:])
		case "layout":
			if len(indexes) != 1 {
				return fmt.Errorf("text layout requires one member")
			}
			layout := ofdgo.TextLayout{Wrap: args[6].Bool(), Align: args[7].String(), LineHeight: args[8].Float(), LetterSpacing: args[9].Float(), LeftIndent: args[10].Float(), RightIndent: args[11].Float(), FirstLineIndent: args[12].Float()}
			if !args[4].IsNull() {
				return currentEditor.ResizeCompositeTextFrame(page, path, indexes[0], args[4].Float(), args[5].Float(), layout)
			}
			members, err := currentEditor.CompositeObjects(page, path)
			if err != nil {
				return err
			}
			if indexes[0] >= len(members) {
				return fmt.Errorf("text member is unavailable")
			}
			value, _ := members[indexes[0]].Object.TextObject.TextLayout()
			return currentEditor.LayoutCompositeText(page, path, indexes[0], value, layout)
		case "fit", "resetCrop":
			if len(indexes) != 1 {
				return fmt.Errorf("image operation requires one member")
			}
			if operation == "fit" {
				return currentEditor.FitCompositeImage(page, path, indexes[0], args[4].String())
			}
			return currentEditor.ResetCompositeImageCrop(page, path, indexes[0])
		case "crop":
			if len(indexes) != 1 {
				return fmt.Errorf("image crop requires one member")
			}
			return currentEditor.CropCompositeImage(page, path, indexes[0], ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()})
		case "image":
			if len(indexes) != 1 {
				return fmt.Errorf("image replacement requires one member")
			}
			data, err := bytesFromJS(args[4])
			if err != nil {
				return err
			}
			return currentEditor.ReplaceCompositeImage(page, path, indexes[0], data)
		case "transform":
			return currentEditor.TransformCompositeObjects(page, path, indexes, args[4].Float(), args[5].Float(), args[6].Float())
		case "rotate":
			return currentEditor.RotateCompositeObjects(page, path, indexes, args[4].Int())
		case "flip":
			return currentEditor.FlipCompositeObjects(page, path, indexes, args[4].String())
		case "resize":
			return currentEditor.ResizeCompositeObjects(page, path, indexes, ofdgo.Box{X: args[4].Float(), Y: args[5].Float(), W: args[6].Float(), H: args[7].Float()})
		case "align":
			return currentEditor.AlignCompositeObjects(page, path, indexes, args[4].String())
		case "distribute":
			return currentEditor.DistributeCompositeObjects(page, path, indexes, args[4].String())
		case "style":
			return currentEditor.StyleCompositeObjects(page, path, indexes, objectStyle(args[4]))
		case "paint", "textColor":
			style := ofdgo.ObjectStyle{}
			if operation == "textColor" {
				style.FillColor = &ofdgo.FillColor{}
				if err := setEditorColor(style.FillColor, args[4].String()); err != nil {
					return err
				}
			} else {
				if !args[4].IsNull() {
					value := args[4].Bool()
					style.Fill = &value
				}
				if !args[6].IsNull() {
					value := args[6].Bool()
					style.Stroke = &value
				}
				var colors [2]*ofdgo.FillColor
				for i := range colors {
					if !args[5+i*2].IsNull() {
						colors[i] = &ofdgo.FillColor{}
						if err := setEditorColor(colors[i], args[5+i*2].String()); err != nil {
							return err
						}
					}
				}
				style.FillColor, style.StrokeColor = colors[0], (*ofdgo.StrokeColor)(colors[1])
				if !args[8].IsNull() {
					value := args[8].Float()
					style.LineWidth = &value
				}
			}
			return currentEditor.StyleCompositeObjects(page, path, indexes, style)
		default:
			return fmt.Errorf("unsupported composite operation")
		}
	})
	if err != nil {
		return nil, err
	}
	if selected == nil {
		return result, nil
	}
	doc := editorSelectionInfo{editorInfo: result.(editorInfo)}
	for _, index := range selected {
		doc.SelectedIDs = append(doc.SelectedIDs, fmt.Sprintf("%s/%d", args[1].String(), index))
	}
	return doc, nil
}

// compositeIndexes 将画布成员路径转换为同一范围的序号
// 入参: key 父路径, ids 成员路径
// 返回: []int 成员序号, error 错误信息
func compositeIndexes(key string, ids []string) ([]int, error) {
	indexes := make([]int, 0, len(ids))
	for _, id := range ids {
		part, found := strings.CutPrefix(id, key+"/")
		if !found {
			return nil, fmt.Errorf("selection is outside composite")
		}
		i, err := strconv.Atoi(part)
		if err != nil || i < 0 {
			return nil, fmt.Errorf("invalid composite member")
		}
		indexes = append(indexes, i)
	}
	return indexes, nil
}

// changeCompositeText 修改内部文字，嵌入字体前校验选区用字
// 入参: page 页面索引, path 父路径, indexes 成员序号, operation 操作, args 内容及样式
// 返回: error 错误信息
func changeCompositeText(page int, path ofdgo.ObjectPath, indexes []int, operation string, args []js.Value) error {
	var value *string
	if operation == "text" {
		if len(indexes) != 1 {
			return fmt.Errorf("text editing requires one member")
		}
		if !args[0].IsNull() {
			text := args[0].String()
			value = &text
		}
		args = args[1:]
	}
	style := ofdgo.TextStyle{Size: args[1].Float()}
	if !args[0].IsNull() {
		data, err := bytesFromJS(args[0])
		if err != nil {
			return err
		}
		members, err := currentEditor.CompositeObjects(page, path)
		if err != nil {
			return err
		}
		var text strings.Builder
		for _, index := range indexes {
			if index >= len(members) || !members[index].Capabilities.ReplaceFont {
				return fmt.Errorf("composite font replacement is not supported")
			}
			if value != nil {
				text.WriteString(*value)
			} else {
				text.WriteString(members[index].Object.TextObject.Text())
			}
		}
		if err := checkFontGlyphs(data, text.String()); err != nil {
			return err
		}
		style.Font, err = currentEditor.AddFont(FontFile{Data: data}, 0)
		if err != nil {
			return err
		}
	}
	if !args[2].IsNull() {
		var fill ofdgo.FillColor
		if err := setEditorColor(&fill, args[2].String()); err != nil {
			return err
		}
		style.Color = fill.Value
	}
	if value != nil {
		return currentEditor.UpdateCompositeText(page, path, indexes[0], *value, style)
	}
	return currentEditor.StyleCompositeText(page, path, indexes, style)
}

// editorFont 读取对象实际使用的内嵌字体，供画布输入使用
// 入参: args 字体资源标识
// 返回: any 字体数据, error 错误信息
func editorFont(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := currentSession.Reader.FontData(args[0].String())
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"bytes": bytesToJS(data)}), nil
}

// replaceImage 替换图片资源并更新预览
// 入参: args 页码、对象标识、图片数据和适应方式
// 返回: any 文档信息, error 错误信息
func replaceImage(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := bytesFromJS(args[2])
	if err != nil {
		return nil, err
	}
	revision := currentEditor.Revision()
	if err := currentEditor.ReplaceImage(args[0].Int(), args[1].String(), data, args[3].String()); err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// alignObject 将对象对齐页面并更新预览
// 入参: args 页码、对象标识和对齐方式
// 返回: any 文档信息, error 错误信息
func alignObject(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	revision := currentEditor.Revision()
	if err := currentEditor.AlignObject(args[0].Int(), args[1].String(), args[2].String()); err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// transformObject 等比缩放后平移对象
// 入参: args 页码、对象标识、位移和缩放比例
// 返回: any 文档信息, error 错误信息
func transformObject(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, id := args[0].Int(), args[1].String()
	revision := currentEditor.Revision()
	if err := currentEditor.TransformObject(page, id, args[2].Float(), args[3].Float(), args[4].Float()); err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// reshapeObject 调整基本图形的尺寸或直线端点，保留绘制样式
// 入参: args 页码、对象标识和页面几何范围
// 返回: any 文档信息, error 错误信息
func reshapeObject(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, id := args[0].Int(), args[1].String()
	object, err := currentEditor.Object(page, id)
	if err != nil {
		return nil, err
	}
	if object.Type != "PathObject" {
		return nil, fmt.Errorf("object is not a path")
	}
	object.PathObject, err = object.PathObject.Reshape(ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
	if err != nil {
		return nil, err
	}
	revision := currentEditor.Revision()
	if err := currentEditor.UpdateObject(page, id, object); err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// reshapeLine 修改直线端点与箭头类型，复用库层样式保留逻辑
// 入参: args 页码、对象标识、页面端点及箭头类型
// 返回: any 文档信息, error 错误信息
func reshapeLine(args []js.Value) (any, error) {
	return changeObjects(func() error {
		page, id := args[0].Int(), args[1].String()
		object, err := currentEditor.Object(page, id)
		if err != nil {
			return err
		}
		if object.Type != "PathObject" {
			return fmt.Errorf("object is not a path")
		}
		object.PathObject, err = object.PathObject.ReshapeLine(ofdgo.ShapeKind(args[6].String()), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
		if err != nil {
			return err
		}
		return currentEditor.UpdateObject(page, id, object)
	})
}

// copyObjects 复制选区并返回新对象标识，复用字体和图片资源
// 入参: args 页码、对象标识列表和位移
// 返回: any 文档信息, error 错误信息
func copyObjects(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	objects, err := currentEditor.Objects(args[0].Int(), stringsFromJS(args[1]))
	if err != nil {
		return nil, err
	}
	return copyEditorObjects(args[0].Int(), objects, args[2].Float(), args[3].Float())
}

// captureObjects 保存独立选区快照，不修改文档或历史
// 入参: args 页面索引、对象标识数组、剪贴板标识和可选父路径
// 返回: any 空结果, error 错误信息
func captureObjects(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	clipboard := &editorClipboard{token: args[2].String()}
	var err error
	if len(args) > 3 && args[3].String() != "" {
		key := args[3].String()
		path, pathErr := compositePath(key)
		if pathErr != nil {
			return nil, pathErr
		}
		indexes, indexErr := compositeIndexes(key, stringsFromJS(args[1]))
		if indexErr != nil {
			return nil, indexErr
		}
		clipboard.composite, err = currentEditor.CaptureCompositeObjects(args[0].Int(), path, indexes)
	} else {
		clipboard.objects, err = currentEditor.Objects(args[0].Int(), stringsFromJS(args[1]))
	}
	if err != nil {
		return nil, err
	}
	copiedObjects = clipboard
	return nil, nil
}

// pasteObjects 将当前文档的对象快照粘贴到目标页
// 入参: args 目标页、剪贴板标识、横纵位移和可选父路径
// 返回: any 文档信息及新选区, error 错误信息
func pasteObjects(args []js.Value) (any, error) {
	if currentEditor == nil || copiedObjects == nil || copiedObjects.token != args[1].String() {
		return nil, fmt.Errorf("object clipboard is no longer available")
	}
	key := ""
	if len(args) > 4 {
		key = args[4].String()
	}
	page, dx, dy := args[0].Int(), args[2].Float(), args[3].Float()
	if key != "" {
		path, err := compositePath(key)
		if err != nil {
			return nil, err
		}
		var indexes []int
		if copiedObjects.composite != nil {
			indexes, err = currentEditor.PasteCompositeObjects(page, path, copiedObjects.composite, dx, dy)
		} else {
			indexes, err = currentEditor.CopyObjectsToComposite(page, path, copiedObjects.objects, dx, dy)
		}
		if err != nil {
			return nil, err
		}
		info, err := previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
		result := editorSelectionInfo{editorInfo: info}
		for _, index := range indexes {
			result.SelectedIDs = append(result.SelectedIDs, fmt.Sprintf("%s/%d", key, index))
		}
		return result, err
	}
	if copiedObjects.composite != nil {
		ids, err := currentEditor.PasteCompositeSelection(page, copiedObjects.composite, dx, dy)
		if err != nil {
			return nil, err
		}
		info, err := previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
		return editorSelectionInfo{info, ids}, err
	}
	return copyEditorObjects(page, copiedObjects.objects, dx, dy)
}

// copyEditorObjects 复制快照并更新预览，返回新对象标识
// 入参: page 目标页, objects 对象快照, dx、dy 毫米位移
// 返回: any 文档信息及新选区, error 错误信息
func copyEditorObjects(page int, objects []ofdgo.GraphicObject, dx, dy float64) (any, error) {
	ids, err := currentEditor.CopyObjects(page, objects, dx, dy)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return editorSummary(), nil
	}
	info, err := previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
	return editorSelectionInfo{info, ids}, err
}

// orderObjects 调整选区的绘制顺序
// 入参: args 页码、对象标识列表和层级动作
// 返回: any 文档信息, error 错误信息
func orderObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.OrderObjects(args[0].Int(), stringsFromJS(args[1]), args[2].String())
	})
}

// distributeObjects 按指定轴等距分布选区
// 入参: args 页面索引、对象标识数组和分布轴
// 返回: any 文档信息, error 错误信息
func distributeObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.DistributeObjects(args[0].Int(), stringsFromJS(args[1]), args[2].String())
	})
}

// deleteObject 删除创作画布中选中的对象
// 入参: args 页码和对象标识
// 返回: any 文档信息, error 错误信息
func deleteObject(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	if err := currentEditor.DeleteObject(args[0].Int(), args[1].String()); err != nil {
		return nil, err
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// changePage 管理创作文档的页面
// 入参: args 操作、页面索引及尺寸或目标索引
// 返回: any 文档信息和目标页面, error 错误信息
func changePage(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	index := args[1].Int()
	revision := currentEditor.Revision()
	var err error
	switch action := args[0].String(); action {
	case "add":
		index, err = currentEditor.AddPage(args[2].Float(), args[3].Float())
	case "copy":
		index, err = currentEditor.CopyPage(index)
	case "delete":
		if currentEditor.PageCount() == 1 {
			return nil, fmt.Errorf("the document must contain at least one page")
		}
		err = currentEditor.DeletePage(index)
		index = min(index, currentEditor.PageCount()-1)
	case "move":
		target := args[2].Int()
		err = currentEditor.MovePage(index, target)
		index = target
	case "resize":
		err = currentEditor.ResizePage(index, args[2].Float(), args[3].Float())
	default:
		return nil, fmt.Errorf("unsupported page action %q", action)
	}
	if err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorPageInfo{editorSummary(), index}, nil
	}
	info, err := previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
	if err != nil {
		return nil, err
	}
	return editorPageInfo{info, index}, nil
}

// batchPages 按页码范围批量复制、删除、移动或调整页面尺寸
// 入参: args 操作、页码表达式及插入位置或宽高
// 返回: any 文档信息及目标页, error 错误信息
func batchPages(args []js.Value) (any, error) {
	index := 0
	info, err := changeObjects(func() error {
		indexes, err := ofdgo.ParsePageRange(args[1].String(), currentEditor.PageCount())
		if err != nil {
			return err
		}
		switch args[0].String() {
		case "copy":
			copies, err := currentEditor.CopyPages(indexes)
			if err == nil && len(copies) > 0 {
				index = copies[0]
			}
			return err
		case "delete":
			if len(indexes) == currentEditor.PageCount() {
				return fmt.Errorf("the document must contain at least one page")
			}
			if err := currentEditor.DeletePages(indexes); err != nil {
				return err
			}
			index = min(indexes[0], currentEditor.PageCount()-1)
			return nil
		case "move":
			at := args[2].Int()
			if at < 0 || at > currentEditor.PageCount() {
				return fmt.Errorf("page index %d out of range", at)
			}
			index = at
			for _, selected := range indexes {
				if selected < at {
					index--
				}
			}
			return currentEditor.MovePages(indexes, index)
		case "resize":
			index = indexes[0]
			return currentEditor.ResizePages(indexes, args[2].Float(), args[3].Float())
		default:
			return fmt.Errorf("unsupported page action %q", args[0].String())
		}
	})
	if err != nil {
		return nil, err
	}
	return editorPageInfo{info.(editorInfo), index}, nil
}

// editorOutlineInfo 目录操作后的位置
type editorOutlineInfo struct {
	editorInfo
	OutlinePath []int `json:"outlinePath"`
}

// indexesFromJS 读取整数索引列表
// 入参: value JavaScript数组
// 返回: []int 索引列表
func indexesFromJS(value js.Value) []int {
	indexes := make([]int, value.Length())
	for i := range indexes {
		indexes[i] = value.Index(i).Int()
	}
	return indexes
}

// moveOutline 调整目录顺序或层级
// 入参: args 源路径、目标父路径及插入索引
// 返回: any 文档及新路径, error 错误信息
func moveOutline(args []js.Value) (any, error) {
	var path []int
	info, err := changeObjects(func() error {
		var err error
		path, err = currentEditor.MoveOutline(indexesFromJS(args[0]), indexesFromJS(args[1]), args[2].Int())
		return err
	})
	if err != nil {
		return nil, err
	}
	return editorOutlineInfo{info.(editorInfo), path}, nil
}

// captureStyle 保存单个对象样式来源，不改写文档
// 入参: args 页面索引、对象标识及可选内部范围
// 返回: any 空结果, error 错误信息
func captureStyle(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	if len(args) > 2 && args[2].String() != "" {
		key := args[2].String()
		path, err := compositePath(key)
		if err != nil {
			return nil, err
		}
		indexes, err := compositeIndexes(key, []string{args[1].String()})
		if err != nil {
			return nil, err
		}
		members, err := currentEditor.CompositeObjects(args[0].Int(), path)
		if err != nil {
			return nil, err
		}
		if indexes[0] >= len(members) {
			return nil, fmt.Errorf("style source is unavailable")
		}
		object := members[indexes[0]].Style()
		copiedStyle = &object
		return nil, nil
	}
	object, err := currentEditor.Object(args[0].Int(), args[1].String())
	if err != nil {
		return nil, err
	}
	copiedStyle = &object
	return nil, nil
}

// pasteStyle 将保存的样式应用到同类型选区
// 入参: args 页面索引和对象标识列表
// 返回: any 文档信息, error 错误信息
func pasteStyle(args []js.Value) (any, error) {
	return changeObjects(func() error {
		if copiedStyle == nil {
			return fmt.Errorf("style clipboard is empty")
		}
		return currentEditor.CopyStyle(args[0].Int(), stringsFromJS(args[1]), *copiedStyle)
	})
}

// resizeObjects 设置选区位置及尺寸
// 入参: args 页面索引、对象标识、横纵坐标及宽高
// 返回: any 文档信息, error 错误信息
func resizeObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.ResizeObjects(args[0].Int(), stringsFromJS(args[1]), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
	})
}

// changeOutline 新增、修改或删除层级目录
// 入参: args 操作、层级索引、标题及目标页面索引
// 返回: any 文档信息, error 错误信息
func changeOutline(args []js.Value) (any, error) {
	path := indexesFromJS(args[1])
	info, err := changeObjects(func() error {
		switch args[0].String() {
		case "add":
			var err error
			path, err = currentEditor.AddOutline(path, args[2].String(), args[3].Int())
			return err
		case "update":
			return currentEditor.UpdateOutline(path, args[2].String(), args[3].Int())
		case "delete":
			err := currentEditor.DeleteOutline(path)
			path = nil
			return err
		default:
			return fmt.Errorf("unsupported outline action %q", args[0].String())
		}
	})
	if err != nil {
		return nil, err
	}
	return editorOutlineInfo{info.(editorInfo), path}, nil
}

// styleObjects 原子更新对象透明度和路径描边属性
// 入参: args 页面索引、对象标识数组及样式，省略字段保持原值
// 返回: any 文档信息, error 错误信息
func styleObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.StyleObjects(args[0].Int(), stringsFromJS(args[1]), objectStyle(args[2]))
	})
}

// objectStyle 读取高级面板提交的外观字段，省略字段保持原值
// 入参: value 前端样式
// 返回: ofdgo.ObjectStyle 库层样式
func objectStyle(value js.Value) ofdgo.ObjectStyle {
	style := ofdgo.ObjectStyle{}
	if field := value.Get("alpha"); !field.IsUndefined() {
		v := field.Int()
		style.Alpha = &v
	}
	if field := value.Get("dashPattern"); !field.IsUndefined() {
		v := field.String()
		style.DashPattern = &v
	}
	if field := value.Get("dashOffset"); !field.IsUndefined() {
		v := field.Float()
		style.DashOffset = &v
	}
	if field := value.Get("cap"); !field.IsUndefined() {
		v := field.String()
		style.Cap = &v
	}
	if field := value.Get("join"); !field.IsUndefined() {
		v := field.String()
		style.Join = &v
	}
	return style
}

// clearImport 释放待插页文档
func clearImport() {
	if pendingImport != nil {
		_ = pendingImport.Close()
		pendingImport = nil
	}
}

// loadImport 加载待插页文件的索引，null参数清除待处理文件
// 入参: args OFD字节数组或null
// 返回: any 页数及签名提示, error 错误信息
func loadImport(args []js.Value) (any, error) {
	clearImport()
	if args[0].IsNull() {
		return nil, nil
	}
	data, err := bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	reader, err := ofdgo.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	doc, err := reader.Doc()
	if err != nil {
		reader.Close()
		return nil, err
	}
	if len(doc.Pages.Page) == 0 {
		reader.Close()
		return nil, fmt.Errorf("document has no pages")
	}
	pendingImport = reader
	return map[string]any{"pageCount": len(doc.Pages.Page), "signed": doc.Signatures != ""}, nil
}

// importPages 插入指定来源页面并保持目标文档元数据
// 入参: args 页码表达式，空值为全部、目标零基插入位置、是否导入对应目录和可选进度回调
// 返回: any 文档信息及首个插入页, error 错误信息
func importPages(args []js.Value) (any, error) {
	if currentEditor == nil || pendingImport == nil {
		return nil, fmt.Errorf("no document is ready for page import")
	}
	doc, err := pendingImport.Doc()
	if err != nil {
		return nil, err
	}
	var indexes []int
	if value := strings.TrimSpace(args[0].String()); value != "" {
		indexes, err = ofdgo.ParsePageRange(value, len(doc.Pages.Page))
		if err != nil {
			return nil, err
		}
	} else {
		for i := range doc.Pages.Page {
			indexes = append(indexes, i)
		}
	}
	at := args[1].Int()
	options := ofdgo.PageImportOptions{Outlines: len(args) > 2 && args[2].Bool()}
	if len(args) > 3 {
		options.OnProgress = editorOperationProgress(args[3])
	}
	if _, err := currentEditor.ImportPagesWithOptions(pendingImport, indexes, at, options); err != nil {
		return nil, err
	}
	clearImport()
	info, err := previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
	return editorPageInfo{info, at}, err
}

// createDocument 新建单页文档
// 入参: args 标题、纸张宽高和注解设置
// 返回: any 文档信息, error 错误信息
func createDocument(args []js.Value) (any, error) {
	clearImport()
	editor := ofdgo.NewEditor()
	editor.Info.Title = args[0].String()
	if _, err := editor.AddPage(args[1].Float(), args[2].Float()); err != nil {
		return nil, err
	}
	editor.SetHistoryLimit(100)
	return previewEditor(editor, args[3].Bool())
}

// updateInfo 修改标题、作者和主题，保留其他元数据及创建程序标识
// 入参: args 标题、作者、主题
// 返回: any 编辑状态, error 错误信息
func updateInfo(args []js.Value) (any, error) {
	return changeObjects(func() error {
		info := currentEditor.Info
		info.Title, info.Author, info.Subject = args[0].String(), args[1].String(), args[2].String()
		currentEditor.SetInfo(info)
		return nil
	})
}

// editDocument 将已打开文档接入编辑器，沿用页面、资源及字体配置
// 入参: args 浏览器参数
// 返回: any 编辑状态, error 错误信息
func editDocument(args []js.Value) (any, error) {
	if currentSession == nil {
		return nil, fmt.Errorf("ofd document is not opened")
	}
	if currentEditor != nil {
		return editorSummary(), nil
	}
	editor, err := currentSession.Reader.Editor()
	if err != nil {
		return nil, err
	}
	editor.SetHistoryLimit(100)
	return previewEditor(editor, currentSession.Renderer.RenderAnnotations)
}

// previewEditor 通过内存快照更新预览，不生成中间压缩包
// 入参: editor 编辑文档, annotations 是否显示注解
// 返回: editorInfo 文档信息, error 错误信息
func previewEditor(editor *ofdgo.Editor, annotations bool) (editorInfo, error) {
	reader, err := editor.Reader()
	if err != nil {
		return editorInfo{}, err
	}
	session, err := newSession(reader, OpenOptions{RenderAnnotations: annotations})
	if err != nil {
		return editorInfo{}, err
	}
	editor.SetFontFS()
	if currentSession != nil {
		if currentSession.fontFS != nil {
			session.fontFS = currentSession.fontFS
			session.Renderer.SetFontFS(session.fontFS)
			editor.SetFontFS(session.fontFS)
		}
		_ = currentSession.Close()
	}
	currentSession = session
	session.editing = true
	if currentEditor != editor {
		copiedObjects = nil
		copiedStyle = nil
	}
	currentEditor = editor
	return editorSummary(), nil
}

// updateText 修改文字，null内容保留定位且允许替换字体，null字体数据和颜色沿用对象属性
// 入参: args 页码、对象标识、内容、字体数据、字号和颜色
// 返回: any 文档信息, error 错误信息
func updateText(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, id := args[0].Int(), args[1].String()
	object, err := currentEditor.Object(page, id)
	if err != nil {
		return nil, err
	}
	if object.Type != "TextObject" {
		return nil, fmt.Errorf("object %q is not text", id)
	}
	if !args[3].IsNull() {
		data, err := bytesFromJS(args[3])
		if err != nil {
			return nil, err
		}
		value := object.TextObject.Text()
		if !args[2].IsNull() {
			value = args[2].String()
		}
		if err := checkFontGlyphs(data, value); err != nil {
			return nil, err
		}
		object.TextObject.Font, err = currentEditor.AddFont(FontFile{Data: data}, 0)
		if err != nil {
			return nil, err
		}
	}
	if !args[2].IsNull() {
		object.TextObject.Size = args[4].Float()
		_, layout := object.TextObject.TextLayout()
		if err := currentEditor.LayoutText(&object.TextObject, args[2].String(), layout); err != nil {
			return nil, err
		}
	}
	if !args[5].IsNull() {
		if err := setTextColor(&object.TextObject, args[5].String()); err != nil {
			return nil, err
		}
	}
	revision := currentEditor.Revision()
	if err := currentEditor.UpdateObject(page, id, object); err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// checkFontGlyphs 只检查候选字体，不注册资源或改变文档
// 入参: data 字体数据, value 待替换文字
// 返回: error 缺字或字体格式错误
func checkFontGlyphs(data []byte, value string) error {
	missing, err := (FontFile{Data: data}).MissingGlyphs(0, value)
	if err != nil {
		return err
	}
	if missing != "" {
		return &ofdgo.MissingGlyphError{Characters: missing}
	}
	return nil
}

// checkTextFont 为画布输入预检候选字体
// 入参: args 字体数据和输入文字
// 返回: any 空结果, error 缺字或字体格式错误
func checkTextFont(args []js.Value) (any, error) {
	data, err := bytesFromJS(args[0])
	if err != nil {
		return nil, err
	}
	return nil, checkFontGlyphs(data, args[1].String())
}

// styleText 批量修改文字样式，字体预检通过后交由库原子提交
// 入参: args 页码、对象标识、字体数据、字号和颜色，null字体与颜色保持原值
// 返回: any 文档信息, error 错误信息
func styleText(args []js.Value) (any, error) {
	return changeObjects(func() error {
		page, ids := args[0].Int(), stringsFromJS(args[1])
		style := ofdgo.TextStyle{Size: args[3].Float()}
		if !args[2].IsNull() {
			data, err := bytesFromJS(args[2])
			if err != nil {
				return err
			}
			var value strings.Builder
			for _, id := range ids {
				object, err := currentEditor.Object(page, id)
				if err != nil {
					return err
				}
				if object.Type != "TextObject" {
					return fmt.Errorf("object %q is not text", id)
				}
				value.WriteString(object.TextObject.Text())
			}
			if err := checkFontGlyphs(data, value.String()); err != nil {
				return err
			}
			style.Font, err = currentEditor.AddFont(FontFile{Data: data}, 0)
			if err != nil {
				return err
			}
		}
		if !args[4].IsNull() {
			var color ofdgo.FillColor
			if err := setEditorColor(&color, args[4].String()); err != nil {
				return err
			}
			style.Color = color.Value
		}
		return currentEditor.StyleText(page, ids, style)
	})
}

// setTextColor 将浏览器RGB色值写入文字对象，保留颜色透明度
// 入参: object 文字对象, value 十六进制色值
// 返回: error 错误信息
func setTextColor(object *ofdgo.TextObject, value string) error {
	if object.FillColor == nil {
		object.FillColor = &ofdgo.FillColor{}
	}
	return setEditorColor(object.FillColor, value)
}

// setEditorColor 写入当前文档的RGB纯色，保留透明度并替换原颜色空间和索引
// 入参: fill 颜色, value 十六进制色值
// 返回: error 错误信息
func setEditorColor(fill *ofdgo.FillColor, value string) error {
	if len(value) != 7 || value[0] != '#' {
		return fmt.Errorf("invalid RGB color %q", value)
	}
	rgb, err := hex.DecodeString(value[1:])
	if err != nil {
		return err
	}
	converted, err := currentEditor.RGBColor(color.NRGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 255})
	if err != nil {
		return err
	}
	converted.Alpha = fill.Alpha
	*fill = *converted
	return nil
}

// editorColorHex 将当前文档纯色转换为浏览器色值
// 入参: color 颜色
// 返回: string 十六进制色值
func editorColorHex(value *ofdgo.FillColor) string {
	converted, err := currentEditor.Color(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", converted.R, converted.G, converted.B)
}

// editorPathScale 获取路径线宽的页面缩放比例
// 入参: object 路径对象
// 返回: float64 缩放比例
func editorPathScale(object ofdgo.PathObject) float64 {
	m := ofdgo.NewMatrix(object.CTM)
	x, y := m.Transform(0, 0)
	ax, ay := m.Transform(1, 0)
	bx, by := m.Transform(0, 1)
	return math.Sqrt(math.Abs((ax-x)*(by-y) - (ay-y)*(bx-x)))
}

// setPathStyle 应用图形填充、描边和线宽
// 入参: object 路径对象, args 填充开关及色值、描边开关及色值、线宽，null保持原值
// 返回: error 错误信息
func setPathStyle(object *ofdgo.PathObject, args []js.Value) error {
	fill, stroke := object.Fill != nil && *object.Fill, object.Stroke == nil || *object.Stroke
	if !args[0].IsNull() {
		fill = args[0].Bool()
	}
	if !args[2].IsNull() {
		stroke = args[2].Bool()
	}
	if kind, _ := object.Shape(); kind == ofdgo.ShapeLine || kind == ofdgo.ShapeArrow || kind == ofdgo.ShapeDoubleArrow {
		fill, stroke = false, true
	}
	if !fill && !stroke {
		if args[0].IsNull() || args[2].IsNull() {
			stroke = true
		} else {
			return fmt.Errorf("fill or stroke must be enabled")
		}
	}
	if !args[0].IsNull() || fill != (object.Fill != nil && *object.Fill) {
		object.Fill = &fill
	}
	if !args[2].IsNull() || stroke != (object.Stroke == nil || *object.Stroke) {
		object.Stroke = &stroke
	}
	if !args[4].IsNull() {
		width := args[4].Float()
		if width <= 0 || math.IsNaN(width) || math.IsInf(width, 0) {
			return fmt.Errorf("line width must be positive and finite")
		}
		if scale := editorPathScale(*object); width != object.LineWidth*scale {
			object.LineWidth = width / scale
		}
	}
	if !args[1].IsNull() {
		if object.FillColor == nil {
			object.FillColor = &ofdgo.FillColor{}
		}
		if err := setEditorColor(object.FillColor, args[1].String()); err != nil {
			return err
		}
	}
	if !args[3].IsNull() {
		if object.StrokeColor == nil {
			object.StrokeColor = &ofdgo.StrokeColor{}
		}
		if err := setEditorColor((*ofdgo.FillColor)(object.StrokeColor), args[3].String()); err != nil {
			return err
		}
	}
	return nil
}

// insertShape 创建标准路径图形
// 入参: args 页码、图形类型、范围和绘制样式
// 返回: any 文档信息, error 错误信息
func insertShape(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	path, err := ofdgo.NewShape(ofdgo.ShapeKind(args[1].String()), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
	if err != nil {
		return nil, err
	}
	if err := setPathStyle(&path, args[6:]); err != nil {
		return nil, err
	}
	return insertEditorObject(args[0].Int(), ofdgo.GraphicObject{Type: "PathObject", PathObject: path}, args[11:])
}

// updatePathStyle 原子更新选中路径的绘制样式，保留未修改属性
// 入参: args 页码、对象标识列表和绘制样式
// 返回: any 文档信息, error 错误信息
func updatePathStyle(args []js.Value) (any, error) {
	return changeObjects(func() error {
		page, ids := args[0].Int(), stringsFromJS(args[1])
		objects := make([]ofdgo.GraphicObject, len(ids))
		for i, id := range ids {
			object, err := currentEditor.Object(page, id)
			if err != nil {
				return err
			}
			if object.Type != "PathObject" {
				return fmt.Errorf("object is not a path")
			}
			if err := setPathStyle(&object.PathObject, args[2:]); err != nil {
				return err
			}
			objects[i] = object
		}
		return currentEditor.UpdateObjects(page, objects)
	})
}

// insertText 使用选定字体创建文字对象
// 入参: args 页码、内容、字体数据、横纵坐标、字号、颜色、框宽、折行、对齐、行距、字距及左右和首行缩进
// 返回: any 文档信息, error 错误信息
func insertText(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := bytesFromJS(args[2])
	if err != nil {
		return nil, err
	}
	fontID, err := currentEditor.AddFont(FontFile{Data: data}, 0)
	if err != nil {
		return nil, err
	}
	page := args[0].Int()
	content, err := currentSession.pageContent(page)
	if err != nil {
		return nil, err
	}
	box, err := currentSession.pageBox(page, content)
	if err != nil {
		return nil, err
	}
	x, y := args[3].Float(), args[4].Float()
	width := args[7].Float()
	if width == 0 {
		width = 80
	}
	box = ofdgo.Box{X: x, Y: y, W: math.Min(width, box.W-x), H: box.H - y}
	object := ofdgo.GraphicObject{Type: "TextObject", TextObject: ofdgo.TextObject{
		Boundary: fmt.Sprintf("%g %g %g %g", box.X, box.Y, box.W, box.H),
		Font:     fontID, Size: args[5].Float(),
	}}
	if err := currentEditor.LayoutText(&object.TextObject, args[1].String(), ofdgo.TextLayout{Wrap: args[8].Bool(), Align: args[9].String(), LineHeight: args[10].Float(), LetterSpacing: args[11].Float(), LeftIndent: args[12].Float(), RightIndent: args[13].Float(), FirstLineIndent: args[14].Float()}); err != nil {
		return nil, err
	}
	if err := setTextColor(&object.TextObject, args[6].String()); err != nil {
		return nil, err
	}
	return insertEditorObject(page, object, args[15:])
}

// layoutText 调整文字框和段落排版，保留软换行前的原文
// 入参: args 页码、对象标识、本地左侧偏移、宽度、折行、对齐、行距、字距及左右和首行缩进
// 返回: any 文档信息, error 错误信息
func layoutText(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, id := args[0].Int(), args[1].String()
	object, err := currentEditor.Object(page, id)
	if err != nil {
		return nil, err
	}
	if object.Type != "TextObject" {
		return nil, fmt.Errorf("object is not text")
	}
	if !args[2].IsNull() {
		object.TextObject, err = object.TextObject.ResizeTextFrame(args[2].Float(), args[3].Float())
		if err != nil {
			return nil, err
		}
	}
	value, _ := object.TextObject.TextLayout()
	if err := currentEditor.LayoutText(&object.TextObject, value, ofdgo.TextLayout{Wrap: args[4].Bool(), Align: args[5].String(), LineHeight: args[6].Float(), LetterSpacing: args[7].Float(), LeftIndent: args[8].Float(), RightIndent: args[9].Float(), FirstLineIndent: args[10].Float()}); err != nil {
		return nil, err
	}
	revision := currentEditor.Revision()
	if err := currentEditor.UpdateObject(page, id, object); err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// editorBox 将库层毫米边界传递给画布交互层
// 入参: box 毫米坐标中的矩形范围
// 返回: map[string]any 可直接传递给JavaScript的横纵坐标和宽高
func editorBox(box ofdgo.Box) map[string]any {
	return map[string]any{"x": box.X, "y": box.Y, "width": box.W, "height": box.H}
}

// editorMatrix 将标准CTM转换为浏览器仿射矩阵分量
// 入参: matrix 库层仿射矩阵
// 返回: []any 可直接传递给JavaScript的a、b、c、d、e、f分量，位移单位为毫米
func editorMatrix(matrix ofdgo.Matrix) []any {
	x, y := matrix.Transform(0, 0)
	ax, ay := matrix.Transform(1, 0)
	bx, by := matrix.Transform(0, 1)
	return []any{ax - x, ay - y, bx - x, by - y, x, y}
}

// rotateObjects 旋转同页选区，通用几何与历史记录由库负责
// 入参: args 页面索引、对象标识数组和顺时针角度
// 返回: any 文档信息, error 错误信息
func rotateObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.RotateObjects(args[0].Int(), stringsFromJS(args[1]), args[2].Int())
	})
}

// flipObjects 镜像同页选区
// 入参: args 页面索引、对象标识数组和镜像轴
// 返回: any 文档信息, error 错误信息
func flipObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.FlipObjects(args[0].Int(), stringsFromJS(args[1]), args[2].String())
	})
}

// cropImage 设置图片裁剪范围，保留原始图片数据
// 入参: args 页面索引、对象标识及页面毫米坐标中的横纵坐标和宽高
// 返回: any 文档信息, error 错误信息
func cropImage(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.CropImage(args[0].Int(), args[1].String(), ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float()})
	})
}

// resetImageCrop 还原跨范围复制图片的会话裁剪，不移除原始及父级裁剪
// 入参: args 页面索引及对象标识
// 返回: any 文档信息, error 错误信息
func resetImageCrop(args []js.Value) (any, error) {
	return changeObjects(func() error { return currentEditor.ResetImageCrop(args[0].Int(), args[1].String()) })
}

// fitImage 按原始比例适应或填充图片框
// 入参: args 页面索引、对象标识和适应方式
// 返回: any 文档信息, error 错误信息
func fitImage(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.FitImage(args[0].Int(), args[1].String(), args[2].String())
	})
}

// previewImage 在页面副本中显示指定图片的完整内容，不改变正文、历史或缓存
// 入参: args 页面索引和图片对象标识
// 返回: any 页面SVG和分离的图片资源, error 错误信息
func previewImage(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	page, err := currentEditor.Page(args[0].Int())
	if err != nil {
		return nil, err
	}
	id := args[1].String()
	found := false
	for i := range page.Content.Layer {
		for j := range page.Content.Layer[i].Objects {
			object := &page.Content.Layer[i].Objects[j]
			if object.Type == "ImageObject" && object.ImageObject.ID == id {
				object.ImageObject.Clips = nil
				found = true
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("image object %q not found", id)
	}
	var out bytes.Buffer
	resources, err := currentSession.Renderer.RenderToSVGWithResources(page, &out)
	if err != nil {
		return nil, err
	}
	return successResult(map[string]any{"svg": out.String(), "images": svgImagesToJS(resources.Images)}), nil
}

// svgImagesToJS 将图片资源以二进制数组传给前端，不使用base64
// 入参: images SVG引用的图片资源
// 返回: []any 图片标识、类型和数据
func svgImagesToJS(images []ofdgo.SVGImage) []any {
	items := make([]any, len(images))
	for i, image := range images {
		items[i] = map[string]any{"name": image.Name, "mime": image.MIME, "bytes": bytesToJS(image.Data)}
	}
	return items
}

// transformObjects 统一移动或缩放选区，生成一次撤销记录
// 入参: args 页面索引、对象标识数组、横纵位移和正缩放比例
// 返回: any 文档信息, error 错误信息
func transformObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.TransformObjects(args[0].Int(), stringsFromJS(args[1]), args[2].Float(), args[3].Float(), args[4].Float())
	})
}

// alignObjects 对齐选区中的对象
// 入参: args 页面索引、对象标识数组和对齐方式
// 返回: any 文档信息, error 错误信息
func alignObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.AlignObjects(args[0].Int(), stringsFromJS(args[1]), args[2].String())
	})
}

// deleteObjects 一次删除选区中的对象
// 入参: args 页面索引和对象标识数组
// 返回: any 文档信息, error 错误信息
func deleteObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.DeleteObjects(args[0].Int(), stringsFromJS(args[1]))
	})
}

// eraseObjectsPath 按页面闭合折线范围擦除对象，保留局部裁剪之外的内容
// 入参: args 页面索引、对象标识数组和坐标点数组
// 返回: any 文档信息, error 错误信息
func eraseObjectsPath(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.EraseObjectsPath(args[0].Int(), stringsFromJS(args[1]), pointsFromJS(args[2]))
	})
}

// pointsFromJS 读取前端页面坐标点数组
// 入参: value 坐标点数组
// 返回: []ofdgo.Point 页面毫米坐标
func pointsFromJS(value js.Value) []ofdgo.Point {
	points := make([]ofdgo.Point, value.Length())
	for i := range points {
		point := value.Index(i)
		points[i] = ofdgo.Point{X: point.Get("x").Float(), Y: point.Get("y").Float()}
	}
	return points
}

// eraseObjects 按页面矩形范围擦除对象，保留局部裁剪之外的内容
// 入参: args 页面索引、对象标识数组和毫米坐标范围
// 返回: any 文档信息, error 错误信息
func eraseObjects(args []js.Value) (any, error) {
	return changeObjects(func() error {
		return currentEditor.EraseObjects(args[0].Int(), stringsFromJS(args[1]), ofdgo.Box{
			X: args[2].Float(), Y: args[3].Float(), W: args[4].Float(), H: args[5].Float(),
		})
	})
}

// changeObjects 提交库层批量操作，无修改时复用当前预览
// 入参: apply 待执行的编辑操作
// 返回: any 文档信息, error 错误信息
func changeObjects(apply func() error) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	revision := currentEditor.Revision()
	if err := apply(); err != nil {
		return nil, err
	}
	if currentEditor.Revision() == revision {
		return editorSummary(), nil
	}
	return previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
}

// insertImage 按原始比例创建图片对象
// 入参: args 页码、图片数据、横纵坐标和宽度
// 返回: any 文档信息, error 错误信息
func insertImage(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	data, err := bytesFromJS(args[1])
	if err != nil {
		return nil, err
	}
	resourceID, err := currentEditor.AddImage(data)
	if err != nil {
		return nil, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	width := args[4].Float()
	box := ofdgo.Box{X: args[2].Float(), Y: args[3].Float(), W: width, H: width * float64(config.Height) / float64(config.Width)}
	content, err := currentSession.pageContent(args[0].Int())
	if err != nil {
		return nil, err
	}
	pageBox, err := currentSession.pageBox(args[0].Int(), content)
	if err != nil {
		return nil, err
	}
	if height := pageBox.H - box.Y; box.H > height {
		box.W *= height / box.H
		box.H = height
	}
	object := ofdgo.GraphicObject{Type: "ImageObject", ImageObject: ofdgo.ImageObject{
		ResourceID: resourceID, Boundary: fmt.Sprintf("%g %g %g %g", box.X, box.Y, box.W, box.H),
	}}
	return insertEditorObject(args[0].Int(), object, args[5:])
}

// insertEditorObject 复用顶层和内部范围插入流程，返回稳定选区
// 入参: page 页码, object 新对象, scope 可选父路径
// 返回: any 文档信息及新选区, error 错误信息
func insertEditorObject(page int, object ofdgo.GraphicObject, scope []js.Value) (any, error) {
	var id string
	var err error
	if len(scope) > 0 && scope[0].String() != "" {
		key := scope[0].String()
		path, parseErr := compositePath(key)
		if parseErr != nil {
			return nil, parseErr
		}
		var index int
		index, err = currentEditor.AddCompositeObject(page, path, object)
		id = fmt.Sprintf("%s/%d", key, index)
	} else {
		id, err = currentEditor.AddObject(page, object)
	}
	if err != nil {
		return nil, err
	}
	info, err := previewEditor(currentEditor, currentSession.Renderer.RenderAnnotations)
	return editorSelectionInfo{info, []string{id}}, err
}

// editorOperationProgress 节流长操作检查点并向浏览器让出执行，保证阶段开始和结束可取消
// 入参: callback 浏览器进度回调
// 返回: func(string, int, int) error 进度回调
func editorOperationProgress(callback js.Value) func(string, int, int) error {
	var lastStage string
	var lastProgress time.Time
	return func(stage string, completed, total int) error {
		if stage == lastStage && (total == 0 || completed != total) && time.Since(lastProgress) < 32*time.Millisecond {
			return nil
		}
		lastStage, lastProgress = stage, time.Now()
		return awaitExport(callback, stage, completed, total)
	}
}

// saveDocument 分块写出当前编辑文档或指定页面
// 入参: args 数据写出回调、可选准备进度回调和可选页面索引
// 返回: any 保存结果, error 错误信息
func saveDocument(args []js.Value) (any, error) {
	if currentEditor == nil {
		return nil, fmt.Errorf("no document is being edited")
	}
	if len(args) > 1 {
		currentEditor.OnWriteProgress = editorOperationProgress(args[1])
		defer func() { currentEditor.OnWriteProgress = nil }()
	}
	writer := bufio.NewWriterSize(exportWriter{write: args[0]}, 1<<20)
	var err error
	if len(args) > 2 {
		_, err = currentEditor.WritePagesTo(writer, indexesFromJS(args[2]))
	} else {
		_, err = currentEditor.WriteTo(writer)
	}
	if err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return successResult(map[string]any{"mime": "application/ofd", "label": "OFD"}), nil
}
