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

//go:build !js || !wasm

package webui

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/xiaoqidun/ofdgo"
)

const (
	// platformTypeUndefined 表示未定义值
	platformTypeUndefined = iota
	// platformTypeNumber 表示数值
	platformTypeNumber
	// platformTypeString 表示字符串
	platformTypeString
	// platformTypeBoolean 表示布尔值
	platformTypeBoolean
	// platformTypeObject 表示对象
	platformTypeObject
	// platformTypeFunction 表示函数
	platformTypeFunction
)

// nativeFields 缓存可直接展开的JSON字段，空值使用标准编码
var nativeFields sync.Map

// NativeResolver 将授权标识解析为随机读取源，生命周期由宿主管理
// 入参: token 资源授权标识
// 返回: io.ReaderAt 随机读取器, int64 资源大小, error 解析错误
type NativeResolver func(token string) (io.ReaderAt, int64, error)

// NativeOutputRequest 请求为整次批量转换预留唯一输出入口
type NativeOutputRequest struct {
	Destination           any
	Base, Extension, MIME string
	Paged                 bool
}

// NativeOutputTarget 保存预留目标，Name为根目录下可整体删除的入口名
// Destination交给OpenWriter，分页写入名为Base/页文件名，单文件写入名为Base.Extension
type NativeOutputTarget struct {
	Destination any
	Base, Name  string
}

// NativeEngine 保存独立会话，页面渲染可并行，修改等待已开始的只读任务完成
// 同一实例的回调不可重入Call或Close
// hooks应在首次Call前配置，Resolve返回的资源需保持有效直到相应会话关闭
type NativeEngine struct {
	businessEngine
	// Resources 协调会话的计算与缓存，需在首次Call前配置
	Resources *ofdgo.Resources
	// ConversionPool 协调不同会话的批量转换资源
	ConversionPool *ofdgo.ConversionPool
	// RenderParallelism 限制同时渲染的页面数，零值使用可用处理器数
	RenderParallelism int
	// Resolve 按授权标识提供资源读取源
	Resolve NativeResolver
	// LoadFont 按名称读取系统字体数据
	LoadFont func(context.Context, string) ([]byte, error)
	// OpenWriter 必须防止覆盖已有文件，成功时Close提交，失败时仅调用可选Abort
	// 不支持Abort的输出由宿主负责回收，宿主也负责回滚已完成的批量输出
	OpenWriter func(context.Context, any, string) (io.WriteCloser, error)
	// ReserveOutput 为单次批量预留整体入口，返回错误时由宿主释放未交付的预留
	ReserveOutput func(context.Context, NativeOutputRequest) (NativeOutputTarget, error)
	// CleanupOutput 必须幂等，以兼容前端收到output事件后重复removeEntry
	CleanupOutput func(context.Context, NativeOutputTarget) error
	gate          chan struct{}
	once          sync.Once
	saves         sync.WaitGroup
	closed        bool
	renderMu      sync.Mutex
	renderCount   int
	renderDone    chan struct{}
	renderSlots   chan struct{}
	renderIdle    []*Session
}

// nativeReader 在会话当前操作上下文中分块读取，不捕获已结束请求的上下文
type nativeReader struct {
	engine *NativeEngine
	input  io.ReaderAt
}

// nativeCall 保存单次worker适配状态
type nativeCall struct {
	engine      *NativeEngine
	ctx         context.Context
	emit        func(any)
	output      io.WriteCloser
	buffer      bytes.Buffer
	destination any
	files       []any
	currentName string
	mime        string
	size        int64
	exporting   bool
	batch       bool
	base        string
	extension   string
	paged       bool
	reserved    *NativeOutputTarget
	committed   bool
}

// platformValue 仅承载JSON参数、字节与宿主回调，不执行脚本
type platformValue struct{ value any }

// nativeCallback 同步宿主回调
type nativeCallback func(...any) (any, error)

// nativeField 保存字段名称、位置及空值省略规则
type nativeField struct {
	name      string
	index     int
	omitEmpty bool
}

// NewNativeEngine 创建独立业务会话，resolver不接收未经授权的文件路径
func NewNativeEngine(resolve NativeResolver) *NativeEngine { return &NativeEngine{Resolve: resolve} }

// Call 执行worker输入协议，返回ok/data/error，字节保持[]byte，emit不含请求id
// 入参: ctx 取消上下文, name 原操作名, args worker参数, emit 同步进度通知
// 返回: map[string]any 操作结果，宿主负责将二进制转换为资源句柄
// nativeFile只接受授权标识，size以Resolve结果为准，不接受路径字符串或nativeData输出标识
// 输出目标为nil时导出返回data.blob，批量返回data.files中的name、mime及file字节
// 批量输出到非nil目标时必须同时提供ReserveOutput和CleanupOutput，避免误清理既有文件
// 取消为协作式检查，不能抢占宿主阻塞的ReadAt、字体读取或加密库内部计算
func (e *NativeEngine) Call(ctx context.Context, name string, args []any, emit func(any)) (result map[string]any) {
	e.once.Do(func() {
		e.gate = make(chan struct{}, 1)
		e.renderSlots = make(chan struct{}, e.renderParallelism())
		e.resolveInput = e.input
		e.conversionPool = e.ConversionPool
		e.resources = e.Resources
	})
	if ctx == nil {
		ctx = context.Background()
	}
	if name == "ofdgoRenderPage" {
		select {
		case e.renderSlots <- struct{}{}:
			defer func() { <-e.renderSlots }()
		case <-ctx.Done():
			return nativeFailure(ctx.Err())
		}
		if e.resources != nil {
			release, err := e.resources.Acquire(ctx, 64<<20)
			if err != nil {
				return nativeFailure(err)
			}
			defer release()
		}
	}
	select {
	case e.gate <- struct{}{}:
	case <-ctx.Done():
		return nativeFailure(ctx.Err())
	}
	locked := true
	defer func() {
		if locked {
			<-e.gate
		}
	}()
	if err := ctx.Err(); err != nil {
		return nativeFailure(err)
	}
	if e.closed {
		return nativeFailure(fmt.Errorf("native engine is closed"))
	}
	if name == "ofdgoRenderPage" && e.currentSession != nil && e.pdf == nil {
		defer func() {
			if failure := recover(); failure != nil {
				result = nativeFailure(fmt.Errorf("%v", failure))
			}
		}()
		worker, err := e.renderSession(ctx)
		if err != nil {
			return nativeFailure(err)
		}
		operation := &businessEngine{currentSession: worker, currentEditor: e.currentEditor, ctx: ctx}
		values := make([]platformValue, len(args))
		for i, arg := range args {
			values[i] = platformValueOf(arg)
		}
		if len(values) > 0 && operation.currentEditor != nil {
			pageErr := e.prepareRender(operation, values[0].Int())
			if pageErr != nil {
				return nativeFailure(pageErr)
			}
		}
		operation.currentEditor = nil
		e.renderMu.Lock()
		if e.renderCount == 0 {
			e.renderDone = make(chan struct{})
		}
		e.renderCount++
		e.renderMu.Unlock()
		<-e.gate
		locked = false
		defer func() {
			worker.Renderer.OutputContext = context.Background()
			e.renderMu.Lock()
			if result != nil && result["ok"] == true && ctx.Err() == nil {
				e.currentSession.svgFonts = maps.Clone(worker.svgFonts)
				clear(worker.pageCache)
				worker.textCache.Clear()
				clear(worker.svgFonts)
				e.renderIdle = append(e.renderIdle, worker)
			}
			e.renderCount--
			if e.renderCount == 0 {
				close(e.renderDone)
			}
			e.renderMu.Unlock()
		}()
		data, err := operation.safeCall(operation.renderPage, values)
		if err != nil {
			return nativeFailure(err)
		}
		result = nativePlain(data).(map[string]any)
		if result["ok"] != true {
			return result
		}
		page := result["data"].(map[string]any)
		known := make(map[string]bool)
		if len(args) > 4 {
			for i := 0; i < values[4].Length(); i++ {
				known[values[4].Index(i).String()] = true
			}
		}
		for _, value := range page["fonts"].([]any) {
			font := value.(map[string]any)
			if name := font["name"].(string); !known[name] {
				font["data"] = worker.svgFonts[name]
			}
		}
		if e.currentEditor != nil && !(len(values) > 6 && values[6].Bool()) {
			objects, textErr := e.renderObjects(operation, values[0].Int())
			if textErr != nil {
				return nativeFailure(textErr)
			}
			page["objects"] = nativePlain(objects)
		}
		if err := ctx.Err(); err != nil {
			return nativeFailure(err)
		}
		return result
	}
	if err := e.waitRenders(ctx); err != nil {
		return nativeFailure(err)
	}
	if e.pdf != nil || !nativeReadOnly(name) {
		e.renderIdle = nil
	}
	if name == "ofdgoSaveSnapshot" {
		snapshot := e.saveSnapshot
		e.saveSnapshot = nil
		if snapshot == nil {
			return nativeFailure(fmt.Errorf("save snapshot is not prepared"))
		}
		output := &NativeEngine{OpenWriter: e.OpenWriter}
		output.currentEditor = snapshot
		e.saves.Add(1)
		defer e.saves.Done()
		<-e.gate
		locked = false
		defer output.Close()
		return output.Call(ctx, "ofdgoSaveDocument", args, emit)
	}
	e.ctx = ctx
	if e.currentSession != nil {
		e.currentSession.Renderer.OutputContext = ctx
	}
	defer func() {
		if e.currentSession != nil {
			e.currentSession.Renderer.OutputContext = context.Background()
		}
		e.ctx = nil
		if failure := recover(); failure != nil {
			if err, ok := failure.(error); ok {
				result = nativeFailure(err)
			} else {
				result = nativeFailure(fmt.Errorf("%v", failure))
			}
		}
	}()
	fn := e.businessCommands()[name]
	if fn == nil {
		return nativeFailure(fmt.Errorf("unknown operation: %s", name))
	}
	if err := e.checkDocument(name); err != nil {
		return nativeFailure(err)
	}
	call := &nativeCall{engine: e, ctx: ctx, emit: emit}
	if e.pdf != nil {
		e.pdf.progress = platformValueOf(call.progress("conversion", "", true))
		e.pdf.font = platformValueOf(nativeCallback(call.font))
	}
	defer func() {
		if err := call.abort(); err != nil {
			if result == nil || result["ok"] == true {
				result = nativeFailure(err)
			} else {
				result["error"] = fmt.Sprintf("%v; cleanup: %v", result["error"], err)
			}
		}
	}()
	prepared, err := call.prepare(name, append([]any(nil), args...))
	if err != nil {
		return nativeFailure(err)
	}
	values := make([]platformValue, len(prepared))
	for i, arg := range prepared {
		values[i] = platformValueOf(arg)
	}
	if err := e.preparePDF(name, values); err != nil {
		return nativeFailure(err)
	}
	data, err := e.safeCall(fn, values)
	if err != nil {
		return nativeFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return nativeFailure(err)
	}
	if wrapped, ok := data.(platformValue); ok {
		result = nativePlain(wrapped).(map[string]any)
	} else {
		result = map[string]any{"ok": true, "data": nativePlain(data)}
	}
	if err := call.finish(name, result); err != nil {
		return nativeFailure(err)
	}
	return result
}

// nativeReadOnly 判断不会改变文档、字体来源或渲染配置的查询
// 入参: name 操作名称
// 返回: bool 是否保留空闲渲染会话
func nativeReadOnly(name string) bool {
	switch name {
	case "ofdgoImagePreview", "ofdgoImagePageSize", "ofdgoCheckDocument", "ofdgoDocumentInfo", "ofdgoEditorInfo", "ofdgoDocumentNavigation", "ofdgoDocumentVersions", "ofdgoPackageDocuments",
		"ofdgoAnnotationInfos", "ofdgoInvoices", "ofdgoInvoice", "ofdgoActionList", "ofdgoPageSettings", "ofdgoStyleResource",
		"ofdgoOutlineLinks", "ofdgoStructureTargets", "ofdgoStructureObjects", "ofdgoOpenLinks", "ofdgoRenderBackends",
		"ofdgoSVGFontData", "ofdgoSearchPage", "ofdgoSearchPages", "ofdgoPageTextString", "ofdgoResources", "ofdgoResourceReferences",
		"ofdgoResourceTargets", "ofdgoResourceLocations", "ofdgoOutputFormats", "ofdgoExportFormats", "ofdgoParsePageRange":
		return true
	}
	return false
}

// Close 释放会话，不关闭宿主持有的ReaderAt资源
func (e *NativeEngine) Close() error {
	e.once.Do(func() {
		e.gate = make(chan struct{}, 1)
		e.renderSlots = make(chan struct{}, e.renderParallelism())
		e.resolveInput = e.input
	})
	e.gate <- struct{}{}
	defer func() { <-e.gate }()
	e.closed = true
	e.clearPDF()
	_, _ = e.discardPrint(nil)
	_ = e.waitRenders(context.Background())
	e.renderIdle = nil
	e.saveSnapshot = nil
	e.saves.Wait()
	var err error
	if e.currentSession != nil {
		err = e.currentSession.Close()
	}
	if e.pendingImport != nil {
		err = errors.Join(err, e.pendingImport.Close())
	}
	e.currentSession, e.currentEditor, e.pendingImport, e.copiedObjects, e.portableObjects, e.copiedStyle = nil, nil, nil, nil, nil, nil
	return err
}

// waitRenders 等待已开始的页面任务，取消等待不修改当前会话
// 入参: ctx 等待上下文
// 返回: error 取消错误
func (e *NativeEngine) waitRenders(ctx context.Context) error {
	e.renderMu.Lock()
	done, count := e.renderDone, e.renderCount
	e.renderMu.Unlock()
	if count == 0 {
		return ctx.Err()
	}
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// renderParallelism 返回首次调用时采用的页面并行数
// 返回: int 同时运行的页面任务数
func (e *NativeEngine) renderParallelism() int {
	if e.RenderParallelism > 0 {
		return e.RenderParallelism
	}
	return runtime.GOMAXPROCS(0)
}

// renderSession 复用独立只读会话，调用时已持有引擎门控
// 入参: ctx 渲染上下文
// 返回: *Session 渲染会话, error 准备错误
func (e *NativeEngine) renderSession(ctx context.Context) (*Session, error) {
	e.renderMu.Lock()
	defer e.renderMu.Unlock()
	if count := len(e.renderIdle); count > 0 {
		worker := e.renderIdle[count-1]
		e.renderIdle[count-1] = nil
		e.renderIdle = e.renderIdle[:count-1]
		worker.Renderer.OutputContext = ctx
		return worker, nil
	}
	source := e.currentSession
	renderer, err := source.Renderer.Fork(ctx)
	if err != nil {
		return nil, err
	}
	return &Session{Reader: renderer.Reader, Renderer: renderer, doc: source.doc, fontFS: source.fontFS,
		pageCache: make(map[int]*ofdgo.PageContent), boxCache: append([]pageBoxInfo(nil), source.boxCache...),
		textCache: ofdgo.NewPageTextCache(sessionTextCacheBytes), svgFonts: make(map[string][]byte), editing: source.editing}, nil
}

// prepareRender 同步当前编辑页面的图元标识，避免并行访问编辑器缓存
// 入参: operation 只读任务, index 页面索引
// 返回: error 页面或取消错误
func (e *NativeEngine) prepareRender(operation *businessEngine, index int) error {
	e.renderMu.Lock()
	defer e.renderMu.Unlock()
	page, err := operation.currentEditor.Page(index)
	if err != nil {
		return err
	}
	return operation.syncEditorPageIDs(index, page)
}

// renderObjects 读取编辑图元信息，渲染状态由当前任务独立持有
// 入参: operation 只读任务, index 页面索引
// 返回: []any 图元信息, error 提取错误
func (e *NativeEngine) renderObjects(operation *businessEngine, index int) ([]any, error) {
	e.renderMu.Lock()
	defer e.renderMu.Unlock()
	operation.currentEditor = e.currentEditor
	if !reflect.DeepEqual(e.currentEditor.Backends(), operation.currentSession.Renderer.Backends()) {
		e.currentEditor.SetRenderBackends(operation.currentSession.Renderer.Backends())
	}
	text, err := operation.currentSession.PageText(index)
	if err != nil {
		return nil, err
	}
	return operation.editorObjects(index, text)
}

// input 读取授权源并以宿主报告的大小为准
func (e *NativeEngine) input(ctx context.Context, value any) (io.ReaderAt, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if p, ok := value.(platformValue); ok {
		value = p.value
	}
	if data, ok := value.([]byte); ok {
		return nativeReader{engine: e, input: bytes.NewReader(data)}, int64(len(data)), nil
	}
	descriptor, ok := value.(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("expected bytes or nativeFile descriptor")
	}
	token, ok := descriptor["nativeFile"].(string)
	if !ok || token == "" || e.Resolve == nil {
		return nil, 0, fmt.Errorf("unauthorized native file")
	}
	input, size, err := e.Resolve(token)
	if err != nil {
		return nil, 0, err
	}
	if input == nil || size < 0 {
		return nil, 0, fmt.Errorf("invalid native file input")
	}
	return nativeReader{engine: e, input: input}, size, nil
}

// ReadAt 分块传递取消检查，宿主ReadAt自身阻塞时由宿主控制超时
func (r nativeReader) ReadAt(data []byte, offset int64) (int, error) {
	total := 0
	for len(data) > 0 {
		if err := r.engine.operationContext().Err(); err != nil {
			return total, err
		}
		n := min(len(data), 256<<10)
		got, err := r.input.ReadAt(data[:n], offset)
		total += got
		if err != nil {
			return total, err
		}
		if got != n {
			return total, io.ErrUnexpectedEOF
		}
		data, offset = data[n:], offset+int64(n)
	}
	return total, nil
}

// readBytes 分块读取完整资源并检查取消状态
// 入参: ctx 调用上下文, value 资源参数
// 返回: []byte 资源数据, error 读取错误
func (e *NativeEngine) readBytes(ctx context.Context, value any) ([]byte, error) {
	input, size, err := e.input(ctx, value)
	if err != nil {
		return nil, err
	}
	if size > int64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("resource exceeds address space")
	}
	output := make([]byte, int(size))
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := int(min(int64(256<<10), size-offset))
		got, err := input.ReadAt(output[int(offset):int(offset)+n], offset)
		if got != n {
			return nil, io.ErrUnexpectedEOF
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		offset += int64(got)
	}
	return output, nil
}

// nativePlain 保留字节并转换JSON结构
func nativePlain(value any) any {
	switch v := value.(type) {
	case platformValue:
		return nativePlain(v.value)
	case []byte:
		return v
	case map[string]any:
		result := make(map[string]any, len(v))
		for k, item := range v {
			result[k] = nativePlain(item)
		}
		return result
	case nil:
		return nil
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		result := make([]any, v.Len())
		for i := range result {
			result[i] = nativePlain(v.Index(i).Interface())
		}
		return result
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return nativePlain(v.Elem().Interface())
	}
	if v.Kind() == reflect.Struct {
		if fields := nativeStructFields(v.Type()); fields != nil {
			result := make(map[string]any, len(fields))
			for _, field := range fields {
				item := v.Field(field.index)
				if field.omitEmpty {
					switch item.Kind() {
					case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
						if item.Len() == 0 {
							continue
						}
					default:
						if item.Kind() != reflect.Struct && item.IsZero() {
							continue
						}
					}
				}
				result[field.name] = nativePlain(item.Interface())
			}
			return result
		}
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		var result any
		if err := json.Unmarshal(data, &result); err != nil {
			panic(err)
		}
		if fields, ok := result.(map[string]any); ok {
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if !field.IsExported() {
					continue
				}
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if field.Anonymous && name == "" {
					if embedded, ok := nativePlain(v.Field(i).Interface()).(map[string]any); ok {
						for key, value := range embedded {
							if _, exists := fields[key]; exists {
								fields[key] = value
							}
						}
					}
					continue
				}
				if name == "" {
					name = field.Name
				}
				if _, exists := fields[name]; exists {
					fields[name] = nativePlain(v.Field(i).Interface())
				}
			}
		}
		return result
	}
	if v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 {
		if number := v.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			_, err := json.Marshal(value)
			panic(err)
		}
	}
	return value
}

// nativeStructFields 按类型复用普通结构体字段，自定义编码和嵌入字段交给JSON处理
// 入参: value 结构体类型
// 返回: []nativeField 字段列表，nil表示使用标准编码
func nativeStructFields(value reflect.Type) []nativeField {
	if fields, ok := nativeFields.Load(value); ok {
		return fields.([]nativeField)
	}
	var fields []nativeField
	if nativePlainType(value, make(map[reflect.Type]bool)) {
		fields = make([]nativeField, 0, value.NumField())
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" {
				name = field.Name
			}
			fields = append(fields, nativeField{name: name, index: i, omitEmpty: options == "omitempty"})
		}
	}
	stored, _ := nativeFields.LoadOrStore(value, fields)
	return stored.([]nativeField)
}

// nativePlainType 检查类型是否可直接展开，递归引用及复杂字段沿用标准编码
// 入参: value 字段类型, visiting 当前类型路径
// 返回: bool 是否可直接展开
func nativePlainType(value reflect.Type, visiting map[reflect.Type]bool) bool {
	if visiting[value] || value.Implements(reflect.TypeFor[json.Marshaler]()) || value.Implements(reflect.TypeFor[encoding.TextMarshaler]()) || reflect.PointerTo(value).Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(value).Implements(reflect.TypeFor[encoding.TextMarshaler]()) || value == reflect.TypeFor[json.Number]() {
		return false
	}
	visiting[value] = true
	defer delete(visiting, value)
	switch value.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.String:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return nativePlainType(value.Elem(), visiting)
	case reflect.Struct:
		names := make(map[string]bool)
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			if field.Anonymous {
				return false
			}
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" {
				name = field.Name
			}
			if name == "-" || names[name] || options != "" && options != "omitempty" || !nativePlainType(field.Type, visiting) {
				return false
			}
			for _, char := range name {
				if char != '_' && char != '-' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
					return false
				}
			}
			names[name] = true
		}
		return true
	}
	return false
}

// nativeFailure 转换结构化业务错误
func nativeFailure(err error) map[string]any {
	result := nativePlain(failureResult(err)).(map[string]any)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		result["canceled"] = true
	}
	return result
}

// event 发送标准worker进度
func (c *nativeCall) event(value map[string]any) {
	if c.emit != nil {
		c.emit(value)
	}
}

// checkpoint 检查操作取消
func (c *nativeCall) checkpoint(...any) (any, error) { return nil, c.ctx.Err() }

// progress 创建标准进度回调
func (c *nativeCall) progress(kind, stage string, phased bool) nativeCallback {
	return func(args ...any) (any, error) {
		if err := c.ctx.Err(); err != nil {
			return nil, err
		}
		event := map[string]any{"type": kind}
		if stage != "" {
			event["stage"] = stage
		}
		if phased {
			event["phase"], event["completed"], event["total"] = args[0], args[1], args[2]
		} else {
			event["completed"], event["total"] = args[0], args[1]
		}
		c.event(event)
		return nil, c.ctx.Err()
	}
}

// readCallback 创建有界随机读取回调
func (c *nativeCall) readCallback(value any) (nativeCallback, int64, error) {
	input, size, err := c.engine.input(c.ctx, value)
	if err != nil {
		return nil, 0, err
	}
	return func(args ...any) (any, error) {
		if err := c.ctx.Err(); err != nil {
			return nil, err
		}
		offset, n := int64(platformValueOf(args[0]).Float()), platformValueOf(args[1]).Int()
		if offset < 0 || n < 0 || n > 256<<10 || offset > size || int64(n) > size-offset {
			return nil, fmt.Errorf("invalid native read range")
		}
		data := make([]byte, n)
		got, err := input.ReadAt(data, offset)
		if got != n {
			return nil, io.ErrUnexpectedEOF
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		return data, nil
	}, size, nil
}

// write 写出数据块
func (c *nativeCall) write(args ...any) (any, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	value := platformValueOf(args[0])
	data, ok := value.value.([]byte)
	if !ok {
		return nil, fmt.Errorf("expected output bytes")
	}
	var writer io.Writer = &c.buffer
	if c.output != nil {
		writer = c.output
	}
	n, err := writer.Write(data)
	c.size += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return nil, err
}

// open 创建宿主输出，目标只交给授权hook解释
func (c *nativeCall) open(name string) error {
	c.currentName = name
	c.buffer.Reset()
	if c.destination == nil {
		return nil
	}
	if c.engine.OpenWriter == nil {
		return fmt.Errorf("native output resolver is not configured")
	}
	var err error
	c.output, err = c.engine.OpenWriter(c.ctx, c.destination, name)
	if err == nil && c.output == nil {
		return fmt.Errorf("native output resolver returned nil")
	}
	return err
}

// boundary 处理批量输出文件边界
func (c *nativeCall) boundary(args ...any) (any, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	action, name := args[0].(string), args[1].(string)
	if action == "open" {
		if c.paged {
			name = c.base + "/" + name
		} else {
			name = c.base + "." + c.extension
		}
		return nil, c.open(name)
	}
	file := map[string]any{"name": c.currentName, "mime": c.mime}
	if c.output != nil {
		if err := c.output.Close(); err != nil {
			return nil, err
		}
		c.output = nil
	} else {
		file["file"] = append([]byte(nil), c.buffer.Bytes()...)
	}
	c.files = append(c.files, file)
	c.buffer.Reset()
	return nil, nil
}

// font 按需请求授权字体
func (c *nativeCall) font(args ...any) (any, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if c.engine.LoadFont == nil {
		return nil, fmt.Errorf("native font resolver is not configured")
	}
	return c.engine.LoadFont(c.ctx, args[0].(string))
}

// fileCallback 逐个读取图片或附件
func (c *nativeCall) fileCallback(files platformValue) nativeCallback {
	return func(args ...any) (any, error) {
		data, err := c.engine.readBytes(c.ctx, files.Index(platformValueOf(args[0]).Int()).value)
		if err != nil {
			return nil, err
		}
		return map[string]any{"bytes": data}, nil
	}
}

// prepare 将worker输入转换为共用业务参数
func (c *nativeCall) prepare(name string, args []any) ([]any, error) {
	checkpoint := nativeCallback(c.checkpoint)
	write := nativeCallback(c.write)
	switch name {
	case "ofdgoDetectFormat":
		if len(args) != 1 {
			return nil, fmt.Errorf("missing detection input")
		}
		read, size, err := c.readCallback(args[0])
		return []any{read, size, checkpoint}, err
	case "ofdgoConvertFile":
		if len(args) != 4 {
			return nil, fmt.Errorf("missing conversion input")
		}
		read, size, err := c.readCallback(args[0])
		if err != nil {
			return nil, err
		}
		c.destination, c.batch = args[3], true
		options := platformValueOf(args[1])
		c.base = nativeOutputBase(options.Get("base"))
		for _, f := range ofdgo.OutputFormats() {
			if f.Value == options.Get("format").String() {
				c.mime = f.MIME
				c.extension, c.paged = f.Extension, f.Paged
			}
		}
		if c.extension == "" {
			return nil, fmt.Errorf("unsupported conversion format")
		}
		if c.destination != nil {
			if c.engine.ReserveOutput == nil || c.engine.CleanupOutput == nil {
				return nil, fmt.Errorf("batch destination requires reservation and cleanup hooks")
			}
			target, err := c.engine.ReserveOutput(c.ctx, NativeOutputRequest{Destination: c.destination, Base: c.base, Extension: c.extension, MIME: c.mime, Paged: c.paged})
			if err != nil {
				return nil, err
			}
			c.reserved = &target
			if target.Destination == nil || target.Base == "" || target.Name == "" || nativeOutputBase(platformValueOf(target.Base)) != target.Base {
				return nil, fmt.Errorf("invalid reserved output target")
			}
			c.destination, c.base = target.Destination, target.Base
			c.event(map[string]any{"type": "output", "name": target.Name})
		}
		return []any{read, size, args[1], write, c.progress("conversion", "", true), nativeCallback(c.boundary), checkpoint, args[2], nativeCallback(c.font)}, nil
	case "ofdgoPackFiles":
		if len(args) != 3 {
			return nil, fmt.Errorf("missing archive input")
		}
		entries := platformValueOf(args[0])
		metadata := make([]any, entries.Length())
		readers := make([]nativeCallback, entries.Length())
		for i := range readers {
			entry := entries.Index(i)
			read, size, err := c.readCallback(entry.Get("file").value)
			if err != nil {
				return nil, err
			}
			readers[i] = read
			metadata[i] = map[string]any{"name": entry.Get("name").String(), "size": size}
		}
		c.destination, c.exporting = args[1], true
		if err := c.open(""); err != nil {
			return nil, err
		}
		progress := nativeCallback(func(values ...any) (any, error) {
			c.event(map[string]any{"type": "conversion", "phase": "pack", "completed": values[0], "total": values[1]})
			return nil, c.ctx.Err()
		})
		return []any{metadata, nativeCallback(func(values ...any) (any, error) { return readers[platformValueOf(values[0]).Int()], nil }), write, progress, checkpoint, args[2]}, nil
	case "ofdgoChangeAttachment":
		if len(args) > 3 && (args[0] == "add" || args[0] == "replace") {
			args[3] = c.fileCallback(platformValueOf(args[3]))
		}
	case "ofdgoChangeResource":
		if len(args) > 4 && (args[0] == "add" || args[0] == "replace") {
			data, err := c.engine.readBytes(c.ctx, args[4])
			if err != nil {
				return nil, err
			}
			args[4] = data
		}
	case "ofdgoImportImages":
		if len(args) != 5 {
			return nil, fmt.Errorf("missing image import input")
		}
		files := platformValueOf(args[0])
		names := make([]any, files.Length())
		for i := range names {
			names[i] = files.Index(i).Get("name").String()
		}
		args[0] = names
		args = append(args, c.fileCallback(files))
	}
	switch name {
	case "ofdgoResources", "ofdgoPrepareSave":
		return append(args, checkpoint), nil
	case "ofdgoImportPages", "ofdgoImportDocuments", "ofdgoImportImages":
		return append(args, c.progress("import", "", true)), nil
	case "ofdgoOpenPDF":
		return append(args, c.progress("conversion", "", true), nativeCallback(c.font)), nil
	case "ofdgoExportImages", "ofdgoExportPage", "ofdgoExportDocument", "ofdgoExportAttachment", "ofdgoExportResource", "ofdgoExportMedia", "ofdgoExportVersion", "ofdgoSaveDocument", "ofdgoSaveSigned":
		if len(args) == 0 {
			return nil, fmt.Errorf("missing output arguments")
		}
		c.destination, c.exporting = args[len(args)-1], true
		args = args[:len(args)-1]
		if err := c.open(""); err != nil {
			return nil, err
		}
		saving := name == "ofdgoSaveDocument" || name == "ofdgoSaveSigned"
		var indices, compression any
		if saving {
			if name != "ofdgoSaveSigned" && len(args) > 0 {
				if m, ok := args[len(args)-1].(map[string]any); ok && m["mode"] != nil {
					compression = m
					args = args[:len(args)-1]
				}
			}
			if len(args) == 0 {
				return nil, fmt.Errorf("missing save page selection")
			}
			indices, args = args[0], args[1:]
		}
		args = append(args, write)
		if name == "ofdgoExportDocument" {
			args = append(args, c.progress("export", "pages", false))
		}
		if name == "ofdgoExportVersion" {
			args = append(args, c.progress("export", "prepare", true), checkpoint)
		}
		if saving {
			args = append(args, c.progress("export", "prepare", true))
			if compression != nil {
				args = append(args, indices, compression, checkpoint)
			} else if indices != nil {
				args = append(args, indices)
			}
		} else {
			for _, arg := range args {
				if m, ok := arg.(map[string]any); ok && m["mode"] != nil {
					args = append(args, checkpoint)
					break
				}
			}
		}
	}
	return args, nil
}

// finish 完成输出并保留原生字节
func (c *nativeCall) finish(name string, result map[string]any) error {
	if !c.exporting && !c.batch {
		return nil
	}
	data, ok := result["data"].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid output result")
	}
	if c.batch {
		if data["unchanged"] == true && c.size == 0 {
			c.files = []any{}
			if err := c.abort(); err != nil {
				return err
			}
		}
		data["files"] = c.files
	} else if c.output != nil {
		if err := c.output.Close(); err != nil {
			return err
		}
		c.output = nil
	} else {
		data["blob"] = c.buffer.Bytes()
		c.buffer = bytes.Buffer{}
	}
	data["size"] = c.size
	c.committed = true
	if c.exporting && name != "ofdgoExportMedia" && name != "ofdgoPackFiles" {
		c.event(map[string]any{"type": "export", "stage": "save"})
	}
	return nil
}

// abort 失败时释放尚未提交的宿主输出，支持可选Abort回滚
func (c *nativeCall) abort() error {
	var err error
	if c.output != nil {
		if output, ok := c.output.(interface{ Abort() error }); ok {
			err = output.Abort()
		}
		c.output = nil
	}
	if c.reserved != nil && !c.committed {
		target := *c.reserved
		c.reserved = nil
		err = errors.Join(err, c.engine.CleanupOutput(context.WithoutCancel(c.ctx), target))
	}
	return err
}

// nativeOutputBase 清理worker输出文件名，冲突后缀由宿主排他创建决定
func nativeOutputBase(value platformValue) string {
	base := "document"
	if value.Type() == platformTypeString {
		base = value.String()
	}
	base = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune("<>:\"/\\|?*", r) {
			return '_'
		}
		return r
	}, base)
	base = strings.TrimRight(base, ". ")
	if base == "" {
		return "document"
	}
	return base
}

// inputFromValue 读取宿主授权的随机读取源
func (e *businessEngine) inputFromValue(value platformValue) (*ofdgo.Input, error) {
	reader, size, err := e.resolveInput(e.operationContext(), value.value)
	if err != nil {
		return nil, err
	}
	return ofdgo.NewInput(reader, size)
}

// platformValueOf 包装参数
func platformValueOf(v any) platformValue {
	if p, ok := v.(platformValue); ok {
		return p
	}
	return platformValue{v}
}

// platformNull 返回空参数
func platformNull() platformValue { return platformValue{} }

// Type 返回协议参数类型
func (v platformValue) Type() int {
	switch v.value.(type) {
	case nil:
		return platformTypeUndefined
	case string:
		return platformTypeString
	case bool:
		return platformTypeBoolean
	case nativeCallback:
		return platformTypeFunction
	case float64, float32, int, int64, uint64, json.Number:
		return platformTypeNumber
	default:
		return platformTypeObject
	}
}

// IsNull 检查空参数
func (v platformValue) IsNull() bool { return v.value == nil }

// IsUndefined 检查缺省参数
func (v platformValue) IsUndefined() bool { return v.value == nil }

// String 读取字符串
func (v platformValue) String() string {
	if v.value == nil {
		return "<undefined>"
	}
	return fmt.Sprint(v.value)
}

// Bool 读取布尔参数
func (v platformValue) Bool() bool {
	b, ok := v.value.(bool)
	if !ok {
		panic("expected boolean")
	}
	return b
}

// Float 读取数值参数
func (v platformValue) Float() float64 {
	switch n := v.value.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			panic(err)
		}
		return f
	default:
		panic("expected number")
	}
}

// Int 读取整数参数
func (v platformValue) Int() int { return int(v.Float()) }

// Get 读取对象字段
func (v platformValue) Get(key string) platformValue {
	if m, ok := v.value.(map[string]any); ok {
		return platformValueOf(m[key])
	}
	if key == "length" || key == "byteLength" {
		return platformValueOf(v.Length())
	}
	return platformNull()
}

// Length 读取数组长度
func (v platformValue) Length() int {
	if v.value == nil {
		return 0
	}
	return reflect.ValueOf(v.value).Len()
}

// Index 读取数组元素
func (v platformValue) Index(i int) platformValue {
	return platformValueOf(reflect.ValueOf(v.value).Index(i).Interface())
}

// Invoke 调用宿主函数
func (v platformValue) Invoke(args ...any) platformValue {
	result, err := v.value.(nativeCallback)(args...)
	if err != nil {
		panic(err)
	}
	return platformValueOf(result)
}

// platformCopyBytes 复制读取块
func platformCopyBytes(dst []byte, src platformValue) int {
	return copy(dst, src.value.([]byte))
}

// platformJSON 编码小参数
func platformJSON(v platformValue) string {
	data, err := json.Marshal(v.value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// platformParseJSON 解析小参数
func platformParseJSON(data []byte) map[string]any {
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		panic(err)
	}
	return v
}

// bytesToJS 保留原生二进制数据
func bytesToJS(data []byte) platformValue { return platformValueOf(data) }

// bytesFromJS 读取字节或授权资源
func (e *businessEngine) bytesFromJS(v platformValue) ([]byte, error) {
	if data, ok := v.value.([]byte); ok {
		return append([]byte(nil), data...), nil
	}
	input, err := e.inputFromValue(v)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	return io.ReadAll(io.NewSectionReader(input, 0, input.Size()))
}

// successResult 保留二进制返回结构
func successResult(data any) platformValue {
	return platformValueOf(map[string]any{"ok": true, "data": data})
}

// awaitExport 执行可取消宿主回调
func awaitExport(fn platformValue, args ...any) error {
	_, err := fn.value.(nativeCallback)(args...)
	return err
}

// loadConversionFont 读取宿主授权字体
func (e *businessEngine) loadConversionFont(fn platformValue, name string) ([]byte, error) {
	result, err := fn.value.(nativeCallback)(name)
	if err != nil {
		return nil, err
	}
	return e.bytesFromJS(platformValueOf(result))
}
