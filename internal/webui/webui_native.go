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
	"math"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xiaoqidun/ofdgo"
)

// 平台值类型枚举
const (
	platformTypeUndefined = iota
	platformTypeNumber
	platformTypeString
	platformTypeBoolean
	platformTypeObject
	platformTypeFunction
)

// 主画布与预览渲染的默认调度优先级及资源配额
const (
	renderPriorityMain    = 2
	renderPriorityPreview = 5
	renderQuotaMain       = 64 << 20
	renderQuotaPreview    = 8 << 20
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
// Resources、RenderParallelism及各回调应在首次Call前配置，回调不可重入Call或Close
// Resolve返回的资源需保持有效直到相应会话关闭，RenderParallelism为零时使用可用处理器数
// OpenWriter须避免覆盖已有文件，成功时Close提交，失败时调用可选Abort，CleanupOutput须幂等
type NativeEngine struct {
	businessEngine
	Resources         *ofdgo.Resources
	ConversionPool    *ofdgo.ConversionPool
	RenderParallelism int
	Resolve           NativeResolver
	LoadFont          func(context.Context, string) ([]byte, error)
	OpenWriter        func(context.Context, any, string) (io.WriteCloser, error)
	ReserveOutput     func(context.Context, NativeOutputRequest) (NativeOutputTarget, error)
	CleanupOutput     func(context.Context, NativeOutputTarget) error
	gate              chan struct{}
	once              sync.Once
	saves             sync.WaitGroup
	closed            bool
	ctxMu             sync.Mutex
	pdfCtx            context.Context
	renderMu          sync.Mutex
	renderCount       int
	renderSeq         uint64
	renderContexts    map[uint64]context.Context
	saveSeq           uint64
	saveContexts      map[uint64]context.Context
	renderDone        chan struct{}
	renderSlots       *renderLimiter
	renderIdle        []*session
}

// renderWaiter 保存优先级渲染队列中的单个等待请求
type renderWaiter struct {
	priority int
	seq      uint64
	ready    chan struct{}
	granted  bool
}

// renderLimiter 按优先级调度并行渲染槽位并为高优主画布预留保底槽位
type renderLimiter struct {
	mu        sync.Mutex
	limit     int
	lowLimit  int
	active    int
	activeLow int
	nextSeq   uint64
	waiters   []*renderWaiter
}

// nativeReader 在会话当前操作上下文或请求上下文中分块读取，不捕获已结束请求的上下文
type nativeReader struct {
	engine *NativeEngine
	ctx    context.Context
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
// 入参: resolve 宿主资源解析回调
// 返回: *NativeEngine 独立原生业务引擎
func NewNativeEngine(resolve NativeResolver) *NativeEngine { return &NativeEngine{Resolve: resolve} }

// newRenderLimiter 创建支持优先级排序与高优保底槽位的渲染限流器
// 入参: limit 总并行度上限
// 返回: *renderLimiter 优先级渲染限流器
func newRenderLimiter(limit int) *renderLimiter {
	limit = max(2, limit)
	return &renderLimiter{limit: limit, lowLimit: max(1, limit/2)}
}

// renderPriority 从页面渲染参数中解析调度优先级及预览标记
// 入参: args 渲染调用参数
// 返回: int 优先级数值，越小越优先, bool 是否为仅预览模式
func renderPriority(args []any) (int, bool) {
	preview := len(args) > 6 && platformValueOf(args[6]).Type() == platformTypeBoolean && platformValueOf(args[6]).Bool()
	if len(args) > 7 && platformValueOf(args[7]).Type() == platformTypeNumber {
		priority := platformValueOf(args[7]).Int()
		if priority < 0 {
			return 0, preview
		}
		if priority > 6 {
			return 6, preview
		}
		return priority, preview
	}
	if preview {
		return renderPriorityPreview, true
	}
	return renderPriorityMain, false
}

// nativeReadOnly 判断不会改变文档、字体来源或渲染配置的查询
// 入参: name 操作名称
// 返回: bool 是否保留空闲渲染会话
func nativeReadOnly(name string) bool {
	switch name {
	case "ofdgoActionList", "ofdgoAnnotationInfos", "ofdgoCheckDocument", "ofdgoCheckTextFont", "ofdgoDestinationView",
		"ofdgoDistributionAxes", "ofdgoDocumentInfo", "ofdgoDocumentNavigation", "ofdgoDocumentVersions", "ofdgoEditorFont",
		"ofdgoEditorInfo", "ofdgoExportAttachment", "ofdgoExportDocument", "ofdgoExportFormats", "ofdgoExportImages",
		"ofdgoExportInvoice", "ofdgoExportMedia", "ofdgoExportPage", "ofdgoExportResource", "ofdgoExportVersion",
		"ofdgoFontFace", "ofdgoFontFaces", "ofdgoFontMenu", "ofdgoFontPreferenceOrder", "ofdgoFontPreview",
		"ofdgoGetRichText", "ofdgoImagePreview", "ofdgoImages", "ofdgoInvoice", "ofdgoInvoices",
		"ofdgoLoadPDF", "ofdgoMatchFontFiles", "ofdgoOpenLinks", "ofdgoOutlineLinks", "ofdgoOutputFormats",
		"ofdgoPackageDocuments", "ofdgoPageSettings", "ofdgoPagesText", "ofdgoParsePageRange", "ofdgoPreviewImage",
		"ofdgoPreviewRichText", "ofdgoPreviewSignatureSeal", "ofdgoPreviewText", "ofdgoPreviewTextSelection", "ofdgoPrintPDF",
		"ofdgoPrintSide", "ofdgoReadAnnotation", "ofdgoRenderBackends", "ofdgoRenderPage", "ofdgoResourceLocations",
		"ofdgoResourceReferences", "ofdgoResourceTargets", "ofdgoResources", "ofdgoRichTextFonts", "ofdgoSVGFontData",
		"ofdgoSearchPage", "ofdgoSearchPages", "ofdgoSignaturePlacement", "ofdgoStructureObjects", "ofdgoStructureTargets",
		"ofdgoStructureXML", "ofdgoStyleResource", "ofdgoStyleTextFonts", "ofdgoTextSelection", "ofdgoValidateInvoice":
		return true
	}
	return false
}

// nativeConcurrentRead 判断可在独立只读工作会话上与并行渲染池并发执行的查询
// 入参: name 操作名称, editing 是否存在活动编辑器实例
// 返回: bool 是否允许不排空渲染池并发执行
func nativeConcurrentRead(name string, editing bool) bool {
	switch name {
	case "ofdgoPagesText", "ofdgoSearchPage", "ofdgoSearchPages",
		"ofdgoOutlineLinks", "ofdgoDestinationView",
		"ofdgoAnnotationInfos", "ofdgoDocumentNavigation",
		"ofdgoStructureTargets", "ofdgoStructureXML", "ofdgoResources", "ofdgoResourceReferences",
		"ofdgoResourceLocations":
		return true
	case "ofdgoOpenLinks", "ofdgoDocumentVersions", "ofdgoInvoices", "ofdgoInvoice", "ofdgoPageSettings":
		return !editing
	}
	return false
}

// nativePDFConcurrentRead 判断在渐进打开PDF阶段仍可不经过PDF导入器并发执行的只读查询
// 入参: name 操作名称
// 返回: bool 是否可直接并发读取
func nativePDFConcurrentRead(name string) bool {
	return name == "ofdgoDocumentVersions"
}

// nativeReplacesPDF 判断操作是否会替换或销毁当前渐进PDF会话
// 入参: name 操作名称, args 调用参数
// 返回: bool 是否替换当前PDF会话
func nativeReplacesPDF(name string, args []any) bool {
	switch name {
	case "ofdgoOpen", "ofdgoOpenPDF", "ofdgoCreateDocument":
		return true
	case "ofdgoImportImages":
		return len(args) > 2 && platformValueOf(args[2]).Type() == platformTypeNumber && platformValueOf(args[2]).Int() < 0
	}
	return false
}

// closeDetachedPDF 在互斥锁保护下安全关闭已摘除的渐进PDF会话
// 入参: pending 已摘除的渐进PDF状态
func closeDetachedPDF(pending *pendingPDF) {
	if pending == nil {
		return
	}
	pending.markClosed()
	pending.mu.Lock()
	if pending.operation != nil && pending.operation.cancel != nil {
		pending.operation.cancel()
		pending.operation = nil
	}
	if pending.importer != nil {
		_ = pending.importer.Close()
		pending.importer = nil
	}
	if pending.input != nil {
		_ = pending.input.Close()
		pending.input = nil
	}
	pending.mu.Unlock()
}

// nativePlain 保留字节并转换JSON结构
// 入参: value 待转换值
// 返回: any 标准原生结构
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
// 入参: err 业务错误
// 返回: map[string]any 错误描述映射
func nativeFailure(err error) map[string]any {
	result := nativePlain(failureResult(err)).(map[string]any)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		result["canceled"] = true
	}
	return result
}

// nativeOutputBase 清理worker输出文件名，冲突后缀由宿主排他创建决定
// 入参: value 文件名参数
// 返回: string 清理后的基础文件名
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

// platformValueOf 包装参数
// 入参: v Go值
// 返回: platformValue 包装后的参数值
func platformValueOf(v any) platformValue {
	if p, ok := v.(platformValue); ok {
		return p
	}
	return platformValue{v}
}

// platformNull 返回空参数
// 返回: platformValue 空参数值
func platformNull() platformValue { return platformValue{} }

// platformCopyBytes 复制读取块
// 入参: dst 目标切片, src 原生字节参数
// 返回: int 复制字节数
func platformCopyBytes(dst []byte, src platformValue) int {
	return copy(dst, src.value.([]byte))
}

// platformJSON 编码小参数
// 入参: v 参数值
// 返回: string JSON字符串
func platformJSON(v platformValue) string {
	data, err := json.Marshal(v.value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// platformParseJSON 解析小参数
// 入参: data JSON字节
// 返回: map[string]any 解析后的键值结构
func platformParseJSON(data []byte) map[string]any {
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		panic(err)
	}
	return v
}

// bytesToJS 保留原生二进制数据
// 入参: data 二进制切片
// 返回: platformValue 包装后的参数值
func bytesToJS(data []byte) platformValue { return platformValueOf(data) }

// successResult 保留二进制返回结构
// 入参: data 业务返回数据
// 返回: platformValue 成功响应结构
func successResult(data any) platformValue {
	return platformValueOf(map[string]any{"ok": true, "data": data})
}

// awaitExport 执行可取消宿主回调
// 入参: fn 宿主回调函数, args 回调参数
// 返回: error 回调执行错误
func awaitExport(fn platformValue, args ...any) error {
	_, err := fn.value.(nativeCallback)(args...)
	return err
}

// acquire 按优先级申请并行渲染槽位，支持上下文取消
// 入参: ctx 取消上下文, priority 调度优先级
// 返回: func() 槽位释放函数, error 取消错误
func (l *renderLimiter) acquire(ctx context.Context, priority int) (func(), error) {
	isLow := priority >= renderPriorityPreview
	l.mu.Lock()
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if l.canGrantLocked(priority) {
		l.active++
		if isLow {
			l.activeLow++
		}
		l.mu.Unlock()
		var once sync.Once
		return func() { once.Do(func() { l.release(isLow) }) }, nil
	}
	l.nextSeq++
	waiter := &renderWaiter{priority: priority, seq: l.nextSeq, ready: make(chan struct{})}
	l.insertWaiterLocked(waiter)
	l.mu.Unlock()
	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			l.release(isLow)
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { l.release(isLow) }) }, nil
	case <-ctx.Done():
		l.mu.Lock()
		if waiter.granted {
			l.releaseLocked(isLow)
		} else {
			l.removeWaiterLocked(waiter)
			l.dispatchLocked()
		}
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}

// canGrantLocked 判断当前是否可直接为指定优先级分配槽位
// 入参: priority 调度优先级
// 返回: bool 是否可立即分配
func (l *renderLimiter) canGrantLocked(priority int) bool {
	if l.active >= l.limit {
		return false
	}
	if len(l.waiters) > 0 && l.waiters[0].priority < priority {
		return false
	}
	if priority >= renderPriorityPreview && l.activeLow >= l.lowLimit {
		return false
	}
	return true
}

// insertWaiterLocked 按优先级升序及同优先级先进先出顺序插入等待队列
// 入参: waiter 等待节点
func (l *renderLimiter) insertWaiterLocked(waiter *renderWaiter) {
	pos := len(l.waiters)
	for i, item := range l.waiters {
		if waiter.priority < item.priority || (waiter.priority == item.priority && waiter.seq < item.seq) {
			pos = i
			break
		}
	}
	l.waiters = append(l.waiters, nil)
	copy(l.waiters[pos+1:], l.waiters[pos:])
	l.waiters[pos] = waiter
}

// removeWaiterLocked 从等待队列中移除指定等待节点
// 入参: waiter 等待节点
func (l *renderLimiter) removeWaiterLocked(waiter *renderWaiter) {
	for i, item := range l.waiters {
		if item == waiter {
			copy(l.waiters[i:], l.waiters[i+1:])
			l.waiters[len(l.waiters)-1] = nil
			l.waiters = l.waiters[:len(l.waiters)-1]
			return
		}
	}
}

// release 归还渲染槽位并唤醒队列中优先级最高的等待者
// 入参: isLow 是否为低优槽位
func (l *renderLimiter) release(isLow bool) {
	l.mu.Lock()
	l.releaseLocked(isLow)
	l.mu.Unlock()
}

// releaseLocked 在持锁状态下归还渲染槽位并分发空闲槽位
// 入参: isLow 是否为低优槽位
func (l *renderLimiter) releaseLocked(isLow bool) {
	l.active--
	if isLow {
		l.activeLow--
	}
	l.dispatchLocked()
}

// dispatchLocked 在持锁状态下按优先级唤醒可执行的等待者
func (l *renderLimiter) dispatchLocked() {
	for len(l.waiters) > 0 && l.active < l.limit {
		next := l.waiters[0]
		isLow := next.priority >= renderPriorityPreview
		if isLow && l.activeLow >= l.lowLimit {
			break
		}
		copy(l.waiters, l.waiters[1:])
		l.waiters[len(l.waiters)-1] = nil
		l.waiters = l.waiters[:len(l.waiters)-1]
		next.granted = true
		l.active++
		if isLow {
			l.activeLow++
		}
		close(next.ready)
	}
}

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
		e.renderSlots = newRenderLimiter(e.renderParallelism())
		e.renderContexts = make(map[uint64]context.Context)
		e.saveContexts = make(map[uint64]context.Context)
		e.resolveInput = func(ctx context.Context, value any) (io.ReaderAt, int64, error) {
			return e.resolveNativeInput(ctx, value, false)
		}
		e.loadFont = func(name string) ([]byte, error) {
			if err := e.readerContextErr(); err != nil {
				return nil, err
			}
			if e.LoadFont == nil {
				return nil, fmt.Errorf("native font resolver is not configured")
			}
			return e.LoadFont(context.Background(), name)
		}
		e.conversionPool = e.ConversionPool
		e.resources = e.Resources
	})
	if ctx == nil {
		ctx = context.Background()
	}
	if name == "ofdgoRenderPage" {
		priority, preview := renderPriority(args)
		if e.resources != nil {
			quota := int64(renderQuotaMain)
			if preview {
				quota = renderQuotaPreview
			}
			release, err := e.resources.Acquire(ctx, quota)
			if err != nil {
				return nativeFailure(err)
			}
			defer release()
		}
		releaseSlot, err := e.renderSlots.acquire(ctx, priority)
		if err != nil {
			return nativeFailure(err)
		}
		defer releaseSlot()
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
	if name == "ofdgoRenderPage" && e.currentSession != nil {
		<-e.gate
		locked = false
		return e.runConcurrentRender(ctx, args, emit)
	}
	if name == "ofdgoLoadPDF" && e.currentSession != nil && e.pdf != nil {
		<-e.gate
		locked = false
		return e.runLoadPDF(ctx, emit)
	}
	if name == "ofdgoDocumentInfo" {
		if e.pdf != nil {
			return map[string]any{"ok": true, "data": map[string]any{"detailsPending": true}}
		}
		if e.currentSession == nil {
			return nativeFailure(fmt.Errorf("ofd document is not opened"))
		}
		<-e.gate
		locked = false
		return e.runDocumentInfo(ctx, args)
	}
	if name == "ofdgoSVGFontData" && e.currentSession != nil {
		if len(args) == 0 {
			return nativeFailure(fmt.Errorf("missing font name"))
		}
		fontName := platformValueOf(args[0]).String()
		e.renderMu.Lock()
		data, err := e.currentSession.svgFontData(fontName)
		e.renderMu.Unlock()
		if err != nil {
			return nativeFailure(err)
		}
		return map[string]any{"ok": true, "data": map[string]any{"bytes": data}}
	}
	if e.canConcurrentRead(name, args) {
		session := e.currentSession
		worker, err := e.renderSession(ctx)
		if err != nil {
			return nativeFailure(err)
		}
		taskID := e.beginReadTask(ctx)
		<-e.gate
		locked = false
		return e.runConcurrentRead(ctx, name, args, emit, session, worker, taskID)
	}
	var lockedPDF *pendingPDF
	if e.pdf != nil {
		if nativeReplacesPDF(name, args) {
			old := e.pdf
			e.pdf = nil
			old.markClosed()
			<-e.gate
			locked = false
			closeDetachedPDF(old)
			select {
			case e.gate <- struct{}{}:
				locked = true
			case <-ctx.Done():
				return nativeFailure(ctx.Err())
			}
			if err := ctx.Err(); err != nil {
				return nativeFailure(err)
			}
			if e.closed {
				return nativeFailure(fmt.Errorf("native engine is closed"))
			}
		} else {
			var lockErr error
			lockedPDF, locked, lockErr = e.acquirePDFGate(ctx, locked)
			if lockErr != nil {
				return nativeFailure(lockErr)
			}
			if lockedPDF != nil {
				defer lockedPDF.mu.Unlock()
			}
		}
	}
	if !nativeReadOnly(name) || e.pdf != nil || e.currentEditor != nil {
		if err := e.waitRenders(ctx); err != nil {
			return nativeFailure(err)
		}
		e.clearRenderIdle()
	}
	if name == "ofdgoSaveSnapshot" {
		snapshot := e.takeSaveSnapshot()
		if snapshot == nil {
			return nativeFailure(fmt.Errorf("save snapshot is not prepared"))
		}
		output := &NativeEngine{OpenWriter: e.OpenWriter, Resources: e.Resources, ConversionPool: e.ConversionPool}
		output.currentEditor = snapshot
		e.ctxMu.Lock()
		e.saveSeq++
		saveID := e.saveSeq
		e.saveContexts[saveID] = ctx
		e.ctxMu.Unlock()
		e.saves.Add(1)
		defer func() {
			e.ctxMu.Lock()
			delete(e.saveContexts, saveID)
			e.ctxMu.Unlock()
			e.saves.Done()
		}()
		<-e.gate
		locked = false
		defer output.Close()
		return output.Call(ctx, "ofdgoSaveDocument", args, emit)
	}
	e.ctxMu.Lock()
	e.ctx = ctx
	e.ctxMu.Unlock()
	if e.currentSession != nil {
		e.currentSession.ctx = ctx
	}
	defer func() {
		if e.currentSession != nil {
			e.currentSession.ctx = nil
		}
		e.ctxMu.Lock()
		e.ctx = nil
		e.ctxMu.Unlock()
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
	if name == "ofdgoMatchFontFiles" {
		if e.currentSession == nil {
			return nativeFailure(fmt.Errorf("ofd document is not opened"))
		}
		if len(args) == 0 {
			return nativeFailure(fmt.Errorf("missing font files"))
		}
		e.renderMu.Lock()
		fontInfos := slices.Clone(e.currentSession.fontInfos)
		e.renderMu.Unlock()
		matched, err := e.matchFontFilesWith(platformValueOf(args[0]), fontInfos)
		if err != nil {
			return nativeFailure(err)
		}
		if err := ctx.Err(); err != nil {
			return nativeFailure(err)
		}
		return map[string]any{"ok": true, "data": nativePlain(matched)}
	}
	call := &nativeCall{engine: e, ctx: ctx, emit: emit}
	if e.pdf != nil {
		e.pdf.progress = platformValueOf(call.progress("conversion", "", true))
		e.pdf.font = platformValueOf(nativeCallback(call.font))
		e.pdfFont = e.pdf.font
	} else if name == "ofdgoSaveDocument" && e.currentSession != nil && e.currentSession.pdfSource != nil {
		e.pdfFont = platformValueOf(nativeCallback(call.font))
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
	cancelPDF, err := e.preparePDF(name, values)
	if err != nil {
		return nativeFailure(err)
	}
	if cancelPDF != nil {
		defer cancelPDF()
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

// Close 释放会话，不关闭宿主持有的ReaderAt资源
// 返回: error 关闭错误
func (e *NativeEngine) Close() error {
	e.once.Do(func() {
		e.gate = make(chan struct{}, 1)
		e.renderSlots = newRenderLimiter(e.renderParallelism())
	})
	e.gate <- struct{}{}
	e.closed = true
	oldPDF := e.pdf
	e.pdf = nil
	if oldPDF != nil {
		oldPDF.markClosed()
	}
	<-e.gate
	if oldPDF != nil {
		closeDetachedPDF(oldPDF)
	}
	e.gate <- struct{}{}
	defer func() { <-e.gate }()
	e.clearPDF()
	e.releaseBatchFonts()
	_, _ = e.discardPrint(nil)
	_ = e.waitRenders(context.Background())
	e.clearRenderIdle()
	e.closeSaveSnapshot()
	e.saves.Wait()
	e.clearPortableObjects()
	err := e.closeCurrentEditor()
	if e.currentSession != nil {
		err = errors.Join(err, e.currentSession.close())
	}
	if e.pendingImport != nil {
		err = errors.Join(err, e.pendingImport.Close())
	}
	e.currentSession, e.pendingImport, e.copiedObjects, e.copiedStyle = nil, nil, nil, nil
	return err
}

// ReadAt 分块传递取消检查，宿主ReadAt自身阻塞时由宿主控制超时
// 入参: data 目标切片, offset 读取偏移
// 返回: int 读取字节数, error 读取或取消错误
func (r nativeReader) ReadAt(data []byte, offset int64) (int, error) {
	total := 0
	for len(data) > 0 {
		if err := r.contextErr(); err != nil {
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

// contextErr 返回当前读取应响应的取消状态
// 返回: error 取消错误
func (r nativeReader) contextErr() error {
	if r.ctx != nil {
		return r.ctx.Err()
	}
	return r.engine.readerContextErr()
}

// event 发送标准worker进度
// 入参: value 进度事件内容
func (c *nativeCall) event(value map[string]any) {
	if c.emit != nil {
		c.emit(value)
	}
}

// checkpoint 检查操作取消
// 返回: any 空结果, error 取消错误
func (c *nativeCall) checkpoint(...any) (any, error) { return nil, c.ctx.Err() }

// progress 创建标准进度回调
// 入参: kind 事件类型, stage 阶段名, phased 是否包含子阶段
// 返回: nativeCallback 进度回调函数
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
// 入参: value 输入源参数
// 返回: nativeCallback 读取回调, int64 数据大小, error 授权或读取错误
func (c *nativeCall) readCallback(value any) (nativeCallback, int64, error) {
	input, size, err := c.engine.resolveNativeInput(c.ctx, value, true)
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
// 入参: args 输出字节参数
// 返回: any 空结果, error 写入或取消错误
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
// 入参: name 输出条目名
// 返回: error 打开错误
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
// 入参: args 动作与文件名参数
// 返回: any 空结果, error 边界处理错误
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
// 入参: args 字体名称参数
// 返回: any 字体字节数据, error 读取错误
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
// 入参: files 文件描述数组
// 返回: nativeCallback 文件读取回调
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
// 入参: name 操作名, args 原始调用参数
// 返回: []any 转换后的业务参数, error 参数校验或准备错误
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
	case "ofdgoResources", "ofdgoPrepareSave", "ofdgoPagesText":
		return append(args, checkpoint), nil
	case "ofdgoImportPages", "ofdgoImportDocuments", "ofdgoImportImages":
		return append(args, c.progress("import", "", true)), nil
	case "ofdgoProcessFonts":
		return append(args, c.progress("fonts", "", true)), nil
	case "ofdgoOpenPDF":
		return append(args, c.progress("conversion", "", true), nativeCallback(c.font)), nil
	case "ofdgoExportImages", "ofdgoExportPage", "ofdgoExportDocument", "ofdgoExportAttachment", "ofdgoExportResource", "ofdgoExportMedia", "ofdgoExportVersion", "ofdgoSaveDocument", "ofdgoSaveSigned", "ofdgoPrintPDF":
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
		if (name == "ofdgoSaveDocument" && len(args) > 0) || (name == "ofdgoExportVersion" && len(args) > 1) {
			if m, ok := args[len(args)-1].(map[string]any); ok && m["mode"] != nil {
				compression = m
				args = args[:len(args)-1]
			}
		}
		if saving {
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
			if compression == nil {
				compression = map[string]any{"mode": 0}
			}
			args = append(args, c.progress("export", "prepare", true), compression, checkpoint)
		}
		if name == "ofdgoPrintPDF" {
			args = append(args, checkpoint)
		}
		if saving {
			args = append(args, c.progress("export", "prepare", true))
			if compression != nil {
				args = append(args, indices, compression, checkpoint)
			} else if indices != nil {
				args = append(args, indices)
			}
		} else if name != "ofdgoPrintPDF" && name != "ofdgoExportVersion" {
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
// 入参: name 操作名, result 操作响应映射
// 返回: error 提交或关闭错误
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
	if c.exporting && name != "ofdgoExportMedia" && name != "ofdgoPackFiles" && name != "ofdgoPrintPDF" {
		c.event(map[string]any{"type": "export", "stage": "save"})
	}
	return nil
}

// abort 失败时释放尚未提交的宿主输出，支持可选Abort回滚
// 返回: error 回滚或清理错误
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

// Type 返回协议参数类型
// 返回: int 参数类型标识
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
// 返回: bool 是否为空
func (v platformValue) IsNull() bool { return v.value == nil }

// IsUndefined 检查缺省参数
// 返回: bool 是否未定义
func (v platformValue) IsUndefined() bool { return v.value == nil }

// String 读取字符串
// 返回: string 字符串文本
func (v platformValue) String() string {
	if v.value == nil {
		return "<undefined>"
	}
	return fmt.Sprint(v.value)
}

// Bool 读取布尔参数
// 返回: bool 布尔值
func (v platformValue) Bool() bool {
	b, ok := v.value.(bool)
	if !ok {
		panic("expected boolean")
	}
	return b
}

// Float 读取数值参数
// 返回: float64 浮点数值
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
// 返回: int 整数值
func (v platformValue) Int() int { return int(v.Float()) }

// Get 读取对象字段
// 入参: key 字段名
// 返回: platformValue 字段值
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
// 返回: int 元素数量
func (v platformValue) Length() int {
	if v.value == nil {
		return 0
	}
	return reflect.ValueOf(v.value).Len()
}

// Index 读取数组元素
// 入参: i 元素索引
// 返回: platformValue 数组元素值
func (v platformValue) Index(i int) platformValue {
	return platformValueOf(reflect.ValueOf(v.value).Index(i).Interface())
}

// Invoke 调用宿主函数
// 入参: args 调用参数
// 返回: platformValue 回调返回值
func (v platformValue) Invoke(args ...any) platformValue {
	result, err := v.value.(nativeCallback)(args...)
	if err != nil {
		panic(err)
	}
	return platformValueOf(result)
}

// canConcurrentRead 判断当前请求能否在不排空并行渲染池的情况下并发执行
// 入参: name 操作名称, args 调用参数
// 返回: bool 是否允许直接并发执行
func (e *NativeEngine) canConcurrentRead(name string, args []any) bool {
	if e.currentSession == nil || !nativeConcurrentRead(name, e.currentEditor != nil) {
		return false
	}
	if e.pdf == nil || nativePDFConcurrentRead(name) {
		return true
	}
	if name == "ofdgoOpenLinks" && e.currentEditor == nil && !e.pdf.retry.Load() {
		if len(args) == 0 {
			return true
		}
		if len(args) == 1 && platformValueOf(args[0]).Type() == platformTypeNumber {
			return e.pdf.importer.PageSynced(e.currentSession.Reader, platformValueOf(args[0]).Int())
		}
	}
	return false
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

// beginReadTask 登记并发只读或渲染任务并绑定取消上下文，调用时已持有引擎门控
// 入参: ctx 任务取消上下文
// 返回: uint64 任务编号
func (e *NativeEngine) beginReadTask(ctx context.Context) uint64 {
	e.ctxMu.Lock()
	e.renderSeq++
	id := e.renderSeq
	e.renderContexts[id] = ctx
	e.ctxMu.Unlock()
	e.renderMu.Lock()
	if e.renderCount == 0 {
		e.renderDone = make(chan struct{})
	}
	e.renderCount++
	e.renderMu.Unlock()
	return id
}

// closeSessions 关闭不再复用的只读工作会话，释放阅读器与凭据租约
// 入参: workers 待关闭的工作会话列表
func closeSessions(workers []*session) {
	for _, worker := range workers {
		if worker != nil {
			_ = worker.close()
		}
	}
}

// clearRenderIdle 清空并关闭全部空闲只读工作会话
func (e *NativeEngine) clearRenderIdle() {
	e.renderMu.Lock()
	idle := e.renderIdle
	e.renderIdle = nil
	e.renderMu.Unlock()
	closeSessions(idle)
}

// endReadTask 注销并发只读或渲染任务，并按条件归还空闲只读会话
// 入参: id 任务编号, mainSession 关联主会话, worker 只读工作会话, retain 是否归还到空闲池
func (e *NativeEngine) endReadTask(id uint64, mainSession, worker *session, retain bool) {
	if worker != nil {
		worker.ctx = nil
	}
	e.ctxMu.Lock()
	delete(e.renderContexts, id)
	e.ctxMu.Unlock()
	var discard *session
	e.renderMu.Lock()
	if worker != nil {
		if retain && mainSession != nil && e.currentSession == mainSession && worker.source == mainSession {
			if len(worker.svgFonts) > 0 {
				mainSession.retainSVGFonts(worker.svgFonts)
				clear(worker.svgFonts)
			}
			if worker.editing {
				worker.textCache.Clear()
			}
			e.renderIdle = append(e.renderIdle, worker)
		} else {
			discard = worker
		}
	}
	e.renderCount--
	if e.renderCount == 0 {
		close(e.renderDone)
	}
	e.renderMu.Unlock()
	if discard != nil {
		_ = discard.close()
	}
}

// renderParallelism 返回首次调用时采用的页面并行数，至少保留两个槽位
// 返回: int 同时运行的页面任务数
func (e *NativeEngine) renderParallelism() int {
	if e.RenderParallelism > 0 {
		return max(2, e.RenderParallelism)
	}
	return max(2, runtime.GOMAXPROCS(0))
}

// renderSession 复用独立只读会话，渐进同步代数落后时重绑阅读器与渲染器内部状态，调用时已持有引擎门控
// 入参: ctx 渲染上下文
// 返回: *session 渲染会话, error 准备错误
func (e *NativeEngine) renderSession(ctx context.Context) (*session, error) {
	source := e.currentSession
	var stale []*session
	var reused *session
	e.renderMu.Lock()
	for count := len(e.renderIdle); count > 0; count = len(e.renderIdle) {
		worker := e.renderIdle[count-1]
		e.renderIdle[count-1] = nil
		e.renderIdle = e.renderIdle[:count-1]
		if worker.source != source {
			stale = append(stale, worker)
			continue
		}
		reused = worker
		break
	}
	e.renderMu.Unlock()
	closeSessions(stale)
	if reused != nil {
		if reused.pdfSyncGen != source.pdfSyncGen {
			reader, err := source.Reader.Fork(ctx)
			if err != nil {
				_ = reused.close()
				return nil, err
			}
			doc, err := reader.DocStructureContext(ctx)
			if err != nil {
				_ = reader.Close()
				_ = reused.close()
				return nil, err
			}
			oldReader := reused.Reader
			reused.Reader = reader
			reused.Renderer.RebindReader(reader)
			if oldReader != nil && oldReader != reader {
				_ = oldReader.Close()
			}
			reused.doc = doc
			reused.boxCache = nil
			reused.openActionCache = make([]int8, len(doc.Pages.Page))
			reused.openTemplatesLoaded = false
			reused.openTemplates = nil
			reused.pdfSyncGen = source.pdfSyncGen
		}
		reused.ctx = ctx
		return reused, nil
	}
	return e.newReadWorker(ctx, source)
}

// newReadWorker 从主会话派生独立只读工作会话，共享只读页面内容与文字缓存
// 入参: ctx 任务上下文, source 主会话
// 返回: *session 独立工作会话, error 派生错误
func (e *NativeEngine) newReadWorker(ctx context.Context, source *session) (*session, error) {
	renderer, err := source.Renderer.Fork(ctx)
	if err != nil {
		return nil, err
	}
	reader := renderer.Reader()
	doc, err := reader.DocStructureContext(ctx)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	textCache := source.textCache
	if source.editing || textCache == nil {
		textCache = ofdgo.NewPageTextCache(sessionTextCacheBytes)
	}
	pageCache := source.pageCache
	if source.editing || pageCache == nil {
		pageCache = newSharedPageCache(sessionPageCacheBytes)
	}
	return &session{
		Reader:          reader,
		Renderer:        renderer,
		ctx:             ctx,
		source:          source,
		doc:             doc,
		fontFS:          source.fontFS,
		pageCache:       pageCache,
		boxCache:        append([]pageBoxInfo(nil), source.boxCache...),
		openActionCache: make([]int8, len(doc.Pages.Page)),
		textCache:       textCache,
		svgFonts:        make(map[string][]byte),
		fontAnnotations: renderer.RenderAnnotations(),
		editing:         source.editing,
		pdfSyncGen:      source.pdfSyncGen,
	}, nil
}

// acquirePDFGate 遵循pending.mu先于e.gate的单向锁序同时获取渐进PDF锁和引擎门控
// 入参: ctx 取消上下文, locked 当前是否持有门控
// 返回: *pendingPDF 已锁定的渐进PDF状态, bool 返回时是否持有门控, error 取消或关闭错误
func (e *NativeEngine) acquirePDFGate(ctx context.Context, locked bool) (*pendingPDF, bool, error) {
	for {
		pending := e.pdf
		if pending == nil {
			return nil, locked, nil
		}
		if locked {
			<-e.gate
			locked = false
		}
		pending.mu.Lock()
		select {
		case e.gate <- struct{}{}:
			locked = true
		case <-ctx.Done():
			pending.mu.Unlock()
			return nil, false, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			pending.mu.Unlock()
			return nil, locked, err
		}
		if e.closed {
			pending.mu.Unlock()
			return nil, locked, fmt.Errorf("native engine is closed")
		}
		if e.pdf == pending && !pending.closed.Load() {
			return pending, locked, nil
		}
		pending.mu.Unlock()
	}
}

// resetPDFNative 在持有渐进PDF互斥锁的前提下获取引擎门控并排空并发读者后重建导入器与阅读快照
// 入参: ctx 取消上下文, pending 渐进PDF状态
// 返回: *session 重建后的当前会话, error 重建或取消错误
func (e *NativeEngine) resetPDFNative(ctx context.Context, pending *pendingPDF) (*session, error) {
	gateHeld := false
	defer func() {
		if gateHeld {
			<-e.gate
		}
	}()
	select {
	case e.gate <- struct{}{}:
		gateHeld = true
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if e.closed || e.pdf != pending || e.currentSession == nil {
		if !e.closed && pending.finished.Load() {
			return nil, nil
		}
		return nil, context.Canceled
	}
	if err := e.waitRenders(ctx); err != nil {
		return nil, err
	}
	if err := pending.importer.Reset(ctx); err != nil {
		return nil, err
	}
	reader, err := pending.importer.Reader(ctx, -1)
	if err != nil {
		return nil, err
	}
	e.currentSession.textCache.Clear()
	clear(e.currentSession.svgFonts)
	err = e.replacePDFReader(reader)
	closeReader := err != nil && (e.currentSession == nil || e.currentSession.Reader != reader)
	var next *session
	var idle []*session
	if err == nil {
		next = e.currentSession
		next.ctx = nil
		pending.page, pending.next = -1, 0
		pending.retry.Store(false)
		pending.syncGen++
		e.renderMu.Lock()
		next.pdfSyncGen = pending.syncGen
		idle = e.renderIdle
		e.renderIdle = nil
		e.renderMu.Unlock()
	}
	<-e.gate
	gateHeld = false
	closeSessions(idle)
	if closeReader {
		_ = reader.Close()
	}
	return next, err
}

// preparePDFPageNative 在引擎门控外按页转换渐进PDF，仅在增量同步瞬间短暂持锁
// 入参: ctx 取消上下文, pending 渐进PDF状态, target 目标会话, pageIndex 页面索引, emit 进度回调
// 返回: error 转换、同步或取消错误
func (e *NativeEngine) preparePDFPageNative(ctx context.Context, pending *pendingPDF, target *session, pageIndex int, emit func(any)) error {
	pending.beginForeground()
	defer pending.endForeground()
	pending.mu.Lock()
	defer pending.mu.Unlock()
	if pending.finished.Load() {
		return nil
	}
	if pending.closed.Load() {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.ctxMu.Lock()
	e.pdfCtx = ctx
	e.ctxMu.Unlock()
	defer func() {
		e.ctxMu.Lock()
		if e.pdfCtx == ctx {
			e.pdfCtx = nil
		}
		e.ctxMu.Unlock()
	}()
	call := &nativeCall{engine: e, ctx: ctx, emit: emit}
	pending.progress = platformValueOf(call.progress("conversion", "", true))
	pending.font = platformValueOf(nativeCallback(call.font))
	cancelPDF := e.bindPDFContext(pending, ctx)
	defer cancelPDF()
	if pending.retry.Load() {
		next, err := e.resetPDFNative(ctx, pending)
		if err != nil {
			return err
		}
		if next == nil {
			return nil
		}
		target = next
	}
	if pageIndex >= 0 && !pending.importer.Ready(pageIndex) {
		if err := pending.importer.ConvertPage(ctx, pageIndex); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				pending.retry.Store(true)
			}
			return err
		}
	}
	select {
	case e.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-e.gate }()
	if e.closed || e.pdf != pending || e.currentSession == nil {
		if !e.closed && pending.finished.Load() {
			return nil
		}
		return context.Canceled
	}
	target = e.currentSession
	if pending.importer.PageSynced(target.Reader, pageIndex) {
		return nil
	}
	if err := e.waitRenders(ctx); err != nil {
		return err
	}
	if err := pending.importer.SyncReader(ctx, target.Reader, pageIndex); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			pending.retry.Store(true)
		}
		return err
	}
	target.Renderer.RebindReader(target.Reader)
	target.invalidatePageCache(pageIndex)
	pending.page = pageIndex
	pending.syncGen++
	e.renderMu.Lock()
	target.pdfSyncGen = pending.syncGen
	e.renderMu.Unlock()
	return nil
}

// runConcurrentRender 在并行渲染池中渲染普通或渐进PDF页面
// 入参: ctx 取消上下文, args 调用参数, emit 进度回调
// 返回: map[string]any 渲染结果
func (e *NativeEngine) runConcurrentRender(ctx context.Context, args []any, emit func(any)) (result map[string]any) {
	defer func() {
		if failure := recover(); failure != nil {
			result = nativeFailure(fmt.Errorf("%v", failure))
		}
	}()
	gateHeld := false
	defer func() {
		if gateHeld {
			<-e.gate
		}
	}()
	values := make([]platformValue, len(args))
	for i, arg := range args {
		values[i] = platformValueOf(arg)
	}
	var current, worker *session
	var editor *ofdgo.Editor
	var renderID uint64
	workerOwned := false
	defer func() {
		if workerOwned && worker != nil {
			_ = worker.close()
		}
	}()
	for {
		select {
		case e.gate <- struct{}{}:
			gateHeld = true
		case <-ctx.Done():
			return nativeFailure(ctx.Err())
		}
		if err := ctx.Err(); err != nil {
			return nativeFailure(err)
		}
		if e.closed {
			return nativeFailure(fmt.Errorf("native engine is closed"))
		}
		if e.currentSession == nil {
			return nativeFailure(fmt.Errorf("ofd document is not opened"))
		}
		if e.pdf != nil {
			if len(values) == 0 {
				return nativeFailure(fmt.Errorf("missing page index"))
			}
			pageIndex := values[0].Int()
			if e.pdf.retry.Load() || !e.pdf.importer.PageSynced(e.currentSession.Reader, pageIndex) {
				pending := e.pdf
				targetSession := e.currentSession
				<-e.gate
				gateHeld = false
				if err := e.preparePDFPageNative(ctx, pending, targetSession, pageIndex, emit); err != nil {
					return nativeFailure(err)
				}
				continue
			}
		}
		var err error
		worker, err = e.renderSession(ctx)
		if err != nil {
			return nativeFailure(err)
		}
		workerOwned = true
		current = e.currentSession
		editor = e.currentEditor
		if len(values) > 0 && editor != nil && current.editing {
			operation := &businessEngine{currentSession: worker, currentEditor: editor, ctx: ctx}
			if pageErr := e.prepareRender(operation, values[0].Int()); pageErr != nil {
				return nativeFailure(pageErr)
			}
		}
		renderID = e.beginReadTask(ctx)
		workerOwned = false
		<-e.gate
		gateHeld = false
		break
	}
	baseBackends := worker.Renderer.Backends()
	retain := false
	defer func() {
		e.endReadTask(renderID, current, worker, retain)
	}()
	operation := &businessEngine{currentSession: worker, ctx: ctx}
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
			fontData := worker.svgFonts[name]
			if len(fontData) == 0 && current != nil {
				e.renderMu.Lock()
				fontData = current.svgFonts[name]
				e.renderMu.Unlock()
			}
			font["data"] = fontData
		}
	}
	if editor != nil && current.editing && !(len(values) > 6 && values[6].Bool()) {
		objects, textErr := e.renderObjects(operation, values[0].Int())
		if textErr != nil {
			return nativeFailure(textErr)
		}
		page["objects"] = nativePlain(objects)
	}
	if err := ctx.Err(); err != nil {
		return nativeFailure(err)
	}
	retain = reflect.DeepEqual(worker.Renderer.Backends(), baseBackends)
	return result
}

// runLoadPDF 在不阻塞并行渲染池的前提下分片转换渐进PDF并在全部完成后切换完整会话
// 入参: ctx 取消上下文, emit 进度回调
// 返回: map[string]any 加载状态与文档摘要
func (e *NativeEngine) runLoadPDF(ctx context.Context, emit func(any)) (result map[string]any) {
	defer func() {
		if failure := recover(); failure != nil {
			result = nativeFailure(fmt.Errorf("%v", failure))
		}
	}()
	gateHeld := false
	defer func() {
		if gateHeld {
			<-e.gate
		}
	}()
	select {
	case e.gate <- struct{}{}:
		gateHeld = true
	case <-ctx.Done():
		return nativeFailure(ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return nativeFailure(err)
	}
	if e.closed {
		return nativeFailure(fmt.Errorf("native engine is closed"))
	}
	if e.currentSession == nil {
		return nativeFailure(fmt.Errorf("ofd document is not opened"))
	}
	pending := e.pdf
	current := e.currentSession
	if pending == nil {
		if err := e.waitRenders(ctx); err != nil {
			return nativeFailure(err)
		}
		summary := current.summary(nil)
		warnings := string(e.pdfWarnings)
		<-e.gate
		gateHeld = false
		return map[string]any{"ok": true, "data": map[string]any{"done": true, "doc": nativePlain(summary), "warnings": warnings}}
	}
	<-e.gate
	gateHeld = false
	if pending.foregroundWaiters.Load() > 0 {
		if err := pending.waitForeground(ctx); err != nil {
			return nativeFailure(err)
		}
	}
	pending.mu.Lock()
	pdfLocked := true
	defer func() {
		if pdfLocked {
			pending.mu.Unlock()
		}
	}()
	if pending.finished.Load() {
		select {
		case e.gate <- struct{}{}:
			gateHeld = true
		case <-ctx.Done():
			return nativeFailure(ctx.Err())
		}
		if e.closed || e.currentSession == nil {
			return nativeFailure(context.Canceled)
		}
		if err := e.waitRenders(ctx); err != nil {
			return nativeFailure(err)
		}
		summary := e.currentSession.summary(nil)
		doneWarnings := string(e.pdfWarnings)
		<-e.gate
		gateHeld = false
		return map[string]any{"ok": true, "data": map[string]any{"done": true, "doc": nativePlain(summary), "warnings": doneWarnings}}
	}
	if pending.closed.Load() {
		return nativeFailure(context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return nativeFailure(err)
	}
	e.ctxMu.Lock()
	e.pdfCtx = ctx
	e.ctxMu.Unlock()
	defer func() {
		e.ctxMu.Lock()
		if e.pdfCtx == ctx {
			e.pdfCtx = nil
		}
		e.ctxMu.Unlock()
	}()
	call := &nativeCall{engine: e, ctx: ctx, emit: emit}
	pending.progress = platformValueOf(call.progress("conversion", "", true))
	pending.font = platformValueOf(nativeCallback(call.font))
	cancelPDF := e.bindPDFContext(pending, ctx)
	defer func() {
		if cancelPDF != nil {
			cancelPDF()
		}
	}()
	if pending.retry.Load() {
		next, err := e.resetPDFNative(ctx, pending)
		if err != nil {
			return nativeFailure(err)
		}
		if next == nil {
			return map[string]any{"ok": true, "data": map[string]any{"done": false}}
		}
	}
	deadline := time.Now().Add(32 * time.Millisecond)
	for pending.next < pending.importer.PageCount() {
		if pending.foregroundWaiters.Load() > 0 {
			if cancelPDF != nil {
				cancelPDF()
				cancelPDF = nil
			}
			e.ctxMu.Lock()
			if e.pdfCtx == ctx {
				e.pdfCtx = nil
			}
			e.ctxMu.Unlock()
			pending.mu.Unlock()
			pdfLocked = false
			if err := pending.waitForeground(ctx); err != nil {
				return nativeFailure(err)
			}
			pending.mu.Lock()
			pdfLocked = true
			if pending.finished.Load() {
				return map[string]any{"ok": true, "data": map[string]any{"done": false}}
			}
			if pending.closed.Load() {
				return nativeFailure(context.Canceled)
			}
			if err := ctx.Err(); err != nil {
				return nativeFailure(err)
			}
			if pending.retry.Load() || time.Now().After(deadline) {
				return map[string]any{"ok": true, "data": map[string]any{"done": false}}
			}
			e.ctxMu.Lock()
			e.pdfCtx = ctx
			e.ctxMu.Unlock()
			pending.progress = platformValueOf(call.progress("conversion", "", true))
			pending.font = platformValueOf(nativeCallback(call.font))
			cancelPDF = e.bindPDFContext(pending, ctx)
			continue
		}
		index := pending.next
		if !pending.importer.Ready(index) {
			if err := pending.importer.ConvertPage(ctx, index); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					pending.retry.Store(true)
				}
				return nativeFailure(err)
			}
		}
		pending.next++
		if time.Now().After(deadline) {
			return map[string]any{"ok": true, "data": map[string]any{"done": false}}
		}
	}
	editor, report, err := pending.importer.Finish(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			pending.retry.Store(true)
		}
		return nativeFailure(err)
	}
	completed := false
	keepEditor := false
	defer func() {
		if !keepEditor {
			_ = editor.Close()
		}
		if !completed {
			pending.retry.Store(true)
		}
	}()
	encodedWarnings, warnErr := json.Marshal(report.Warnings)
	if warnErr != nil {
		return nativeFailure(warnErr)
	}
	reader, err := editor.Reader(ctx)
	if err != nil {
		return nativeFailure(err)
	}
	readerOwned := true
	defer func() {
		if readerOwned && reader != nil {
			_ = reader.Close()
		}
	}()
	select {
	case e.gate <- struct{}{}:
		gateHeld = true
	case <-ctx.Done():
		return nativeFailure(ctx.Err())
	}
	if e.closed || e.pdf != pending || e.currentSession == nil {
		return nativeFailure(context.Canceled)
	}
	if err := e.waitRenders(ctx); err != nil {
		return nativeFailure(err)
	}
	if err := e.replacePDFReader(reader); err != nil {
		readerOwned = e.currentSession == nil || e.currentSession.Reader != reader
		return nativeFailure(err)
	}
	readerOwned = false
	e.currentSession.ctx = nil
	e.currentSession.textCache.Clear()
	if pending.lazyFontFS != nil {
		if e.currentSession.fontFS != nil {
			e.currentSession.Renderer.SetFontFS(e.currentSession.fontFS)
		} else {
			e.currentSession.Renderer.SetFontFS()
		}
	}
	if e.currentSession.Reader.Encryption().Encrypted {
		e.currentSession.ownedEditor = editor
		keepEditor = true
		if pending.input != nil {
			_ = pending.input.Close()
		}
	} else {
		e.currentSession.input = pending.input
		e.currentSession.pdfSource = pending.input
	}
	pending.input = nil
	completed = true
	pending.finished.Store(true)
	pending.markClosed()
	e.pdf = nil
	e.pdfWarnings = encodedWarnings
	e.renderMu.Lock()
	idle := e.renderIdle
	e.renderIdle = nil
	e.renderMu.Unlock()
	summary := e.currentSession.summary(nil)
	<-e.gate
	gateHeld = false
	closeSessions(idle)
	if pending.importer != nil {
		_ = pending.importer.Close()
		pending.importer = nil
	}
	return map[string]any{"ok": true, "data": map[string]any{"done": true, "doc": nativePlain(summary), "warnings": string(encodedWarnings)}}
}

// runDocumentInfo 在不排空并行渲染池的前提下执行文档详情验签或分批字体扫描
// 入参: ctx 取消上下文, args 调用参数
// 返回: map[string]any 文档信息结果
func (e *NativeEngine) runDocumentInfo(ctx context.Context, args []any) (result map[string]any) {
	defer func() {
		if failure := recover(); failure != nil {
			result = nativeFailure(fmt.Errorf("%v", failure))
		}
	}()
	gateHeld := false
	defer func() {
		if gateHeld {
			<-e.gate
		}
	}()
	scanFonts := len(args) > 0 && platformValueOf(args[0]).Bool()
	select {
	case e.gate <- struct{}{}:
		gateHeld = true
	case <-ctx.Done():
		return nativeFailure(ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return nativeFailure(err)
	}
	if e.closed {
		return nativeFailure(fmt.Errorf("native engine is closed"))
	}
	if e.pdf != nil {
		return map[string]any{"ok": true, "data": map[string]any{"detailsPending": true}}
	}
	session := e.currentSession
	if session == nil {
		return nativeFailure(fmt.Errorf("ofd document is not opened"))
	}
	if scanFonts {
		e.renderMu.Lock()
		fontsRead := session.fontsRead
		cachedFonts := session.fontInfos
		e.renderMu.Unlock()
		if fontsRead {
			return map[string]any{"ok": true, "data": map[string]any{"fonts": nativePlain(cachedFonts), "fontCount": len(cachedFonts), "detailsPending": false}}
		}
		if session.fontScanMu == nil {
			session.fontScanMu = &sync.Mutex{}
		}
		if session.fontScanWorker == nil {
			worker, err := e.newReadWorker(ctx, session)
			if err != nil {
				return nativeFailure(err)
			}
			session.fontScanWorker = worker
		}
		fontScanMu := session.fontScanMu
		fontWorker := session.fontScanWorker
		taskID := e.beginReadTask(ctx)
		<-e.gate
		gateHeld = false
		defer e.endReadTask(taskID, session, nil, false)
		fontScanMu.Lock()
		defer fontScanMu.Unlock()
		if err := ctx.Err(); err != nil {
			return nativeFailure(err)
		}
		fontWorker.ctx = ctx
		done := fontWorker.scanFontInfoBatch()
		fontWorker.ctx = nil
		if err := ctx.Err(); err != nil {
			return nativeFailure(err)
		}
		if !done {
			return map[string]any{"ok": true, "data": map[string]bool{"detailsPending": true}}
		}
		e.renderMu.Lock()
		session.fontInfos = fontWorker.fontInfos
		session.fontsRead = true
		fonts := session.fontInfos
		e.renderMu.Unlock()
		return map[string]any{"ok": true, "data": map[string]any{"fonts": nativePlain(fonts), "fontCount": len(fonts), "detailsPending": false}}
	}
	worker, err := e.renderSession(ctx)
	if err != nil {
		return nativeFailure(err)
	}
	e.renderMu.Lock()
	worker.signaturesRead = session.signaturesRead
	worker.signatures = session.signatures
	worker.signatureError = session.signatureError
	e.renderMu.Unlock()
	taskID := e.beginReadTask(ctx)
	<-e.gate
	gateHeld = false
	retain := false
	defer func() {
		worker.signaturesRead = false
		worker.signatures = nil
		worker.signatureError = nil
		e.endReadTask(taskID, session, worker, retain)
	}()
	details, err := worker.detailsContext(ctx)
	if err != nil {
		return nativeFailure(err)
	}
	if worker.signaturesRead && ctx.Err() == nil {
		e.renderMu.Lock()
		if e.currentSession == session && !session.signaturesRead {
			session.signaturesRead = true
			session.signatures = worker.signatures
			session.signatureError = worker.signatureError
		}
		e.renderMu.Unlock()
	}
	retain = true
	return map[string]any{"ok": true, "data": nativePlain(details)}
}

// runConcurrentRead 在已登记的独立只读工作会话上并发执行查询，不排空并行渲染池
// 入参: ctx 取消上下文, name 操作名称, args 调用参数, emit 进度回调, current 主会话, worker 独立只读会话, taskID 任务编号
// 返回: map[string]any 查询结果
func (e *NativeEngine) runConcurrentRead(ctx context.Context, name string, args []any, emit func(any), current, worker *session, taskID uint64) (result map[string]any) {
	defer func() {
		if failure := recover(); failure != nil {
			if err, ok := failure.(error); ok {
				result = nativeFailure(err)
			} else {
				result = nativeFailure(fmt.Errorf("%v", failure))
			}
		}
	}()
	retain := false
	defer func() {
		e.endReadTask(taskID, current, worker, retain)
	}()
	operation := &businessEngine{
		resources:      e.resources,
		currentSession: worker,
		ctx:            ctx,
		resolveInput:   e.resolveInput,
		conversionPool: e.conversionPool,
	}
	fn := operation.businessCommands()[name]
	if fn == nil {
		return nativeFailure(fmt.Errorf("unknown operation: %s", name))
	}
	call := &nativeCall{engine: e, ctx: ctx, emit: emit}
	prepared, err := call.prepare(name, append([]any(nil), args...))
	if err != nil {
		return nativeFailure(err)
	}
	values := make([]platformValue, len(prepared))
	for i, arg := range prepared {
		values[i] = platformValueOf(arg)
	}
	data, err := operation.safeCall(fn, values)
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
	retain = true
	return result
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
	text, err := operation.currentSession.pageText(index)
	if err != nil {
		return nil, err
	}
	return operation.editorObjects(index, text)
}

// readerContextErr 检查当前串行操作、并行页面渲染与后台保存的取消状态
// 返回: error 取消错误
func (e *NativeEngine) readerContextErr() error {
	e.ctxMu.Lock()
	defer e.ctxMu.Unlock()
	var canceled error
	if e.ctx != nil {
		err := e.ctx.Err()
		if err == nil {
			return nil
		}
		canceled = err
	}
	if e.pdfCtx != nil {
		err := e.pdfCtx.Err()
		if err == nil {
			return nil
		}
		canceled = err
	}
	for _, ctx := range e.renderContexts {
		err := ctx.Err()
		if err == nil {
			return nil
		}
		canceled = err
	}
	for _, ctx := range e.saveContexts {
		err := ctx.Err()
		if err == nil {
			return nil
		}
		canceled = err
	}
	return canceled
}

// resolveNativeInput 解析原生二进制或授权句柄并包装分块取消读取器
// 入参: ctx 调用上下文, value 输入字节或授权描述, bindContext 是否绑定本次请求上下文
// 返回: io.ReaderAt 随机读取器, int64 数据大小, error 授权或读取错误
func (e *NativeEngine) resolveNativeInput(ctx context.Context, value any, bindContext bool) (io.ReaderAt, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if p, ok := value.(platformValue); ok {
		value = p.value
	}
	var bound context.Context
	if bindContext {
		bound = ctx
	}
	if data, ok := value.([]byte); ok {
		return nativeReader{engine: e, ctx: bound, input: bytes.NewReader(data)}, int64(len(data)), nil
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
	return nativeReader{engine: e, ctx: bound, input: input}, size, nil
}

// readBytes 分块读取完整资源并检查取消状态
// 入参: ctx 调用上下文, value 资源参数
// 返回: []byte 资源数据, error 读取错误
func (e *NativeEngine) readBytes(ctx context.Context, value any) ([]byte, error) {
	input, size, err := e.resolveNativeInput(ctx, value, true)
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

// inputFromValue 读取宿主授权的随机读取源
// 入参: value 输入源参数
// 返回: *ofdgo.Input 文档输入源, error 读取错误
func (e *businessEngine) inputFromValue(value platformValue) (*ofdgo.Input, error) {
	reader, size, err := e.resolveInput(e.operationContext(), value.value)
	if err != nil {
		return nil, err
	}
	return ofdgo.NewInput(reader, size)
}

// bytesFromJS 读取字节或授权资源
// 入参: v 二进制或资源描述参数
// 返回: []byte 字节数据, error 读取错误
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

// loadConversionFont 读取宿主授权字体
// 入参: fn 字体加载回调, name 字体名称
// 返回: []byte 字体字节数据, error 读取错误
func (e *businessEngine) loadConversionFont(fn platformValue, name string) ([]byte, error) {
	result, err := fn.value.(nativeCallback)(name)
	if err != nil {
		return nil, err
	}
	return e.bytesFromJS(platformValueOf(result))
}
