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
	"maps"
	"reflect"
	"slices"
)

// EditorChanges 描述最近一次修改或历史回放的刷新范围
// All表示文档结构或共享资源变化；否则Pages为受影响的零基页码
// 返回值只覆盖最近一次操作，调用方应在每次操作后读取
type EditorChanges struct {
	All   bool  `json:"all"`
	Pages []int `json:"pages,omitempty"`
}

// editorChange 页面或对象操作及其修订标识
type editorChange struct {
	undo    func(*Editor)
	redo    func(*Editor)
	before  uint64
	after   uint64
	changes EditorChanges
}

// editorOriginChange 记录单次按页增量事务中发生变动的单个对象原文映射
type editorOriginChange struct {
	id      string
	existed bool
	value   *editorObjectOrigin
}

// copyEditorPage 复制页面容器，共享经校验且按替换方式更新的对象数据
// 入参: page 页面
// 返回: PageContent 独立页面容器
func copyEditorPage(page PageContent) PageContent {
	page.Content.Layer = slices.Clone(page.Content.Layer)
	for i := range page.Content.Layer {
		page.Content.Layer[i].Objects = slices.Clone(page.Content.Layer[i].Objects)
	}
	return page
}

// moveEditorItem 按目标索引移动页面或对象
// 入参: items 页面或对象序列, from 原索引, to 目标索引
func moveEditorItem[T any](items []T, from, to int) {
	item := items[from]
	if from < to {
		copy(items[from:to], items[from+1:to+1])
	} else {
		copy(items[to+1:from+1], items[to:from])
	}
	items[to] = item
}

// isUnloadedSourcePage 判断页面是否为尚未惰性解析的原文档空壳占位
// 入参: page 页面内容
// 返回: bool 是否为未加载原始页面
func isUnloadedSourcePage(page PageContent) bool {
	return page.ID != "" && page.XMLName.Local == "" && page.Area == (PageArea{}) && len(page.Content.Layer) == 0 && len(page.Actions) == 0 && len(page.Template) == 0 && len(page.PageRes) == 0 && len(page.pageResources) == 0
}

// editorSourceInitialPage 根据已加载的原始页面记录构建未修改态页面内容
// 入参: source 已加载的原始页面记录
// 返回: PageContent 保留初始切片头的页面内容
func editorSourceInitialPage(source *editorSourcePage) PageContent {
	page := *source.original
	page.Content.Layer = source.initialLayers
	page.Actions = source.initialActions
	page.Template = source.initialTemplates
	page.PageRes = source.initialPageRes
	return page
}

// diffEditorOrigins 计算事务前后对象原文映射的最小差异集合
// 入参: before 修改前映射, after 修改后映射
// 返回: []editorOriginChange 撤销变更列表, []editorOriginChange 重做变更列表
func diffEditorOrigins(before, after map[string]*editorObjectOrigin) ([]editorOriginChange, []editorOriginChange) {
	var undo, redo []editorOriginChange
	for id, value := range after {
		if old, ok := before[id]; !ok || old != value {
			undo = append(undo, editorOriginChange{id: id, existed: ok, value: old})
			redo = append(redo, editorOriginChange{id: id, existed: true, value: value})
		}
	}
	for id, old := range before {
		if _, ok := after[id]; !ok {
			undo = append(undo, editorOriginChange{id: id, existed: true, value: old})
			redo = append(redo, editorOriginChange{id: id, existed: false})
		}
	}
	return undo, redo
}

// sameEditorSourceMeta 判断两个文档来源快照在包结构与元数据层面是否一致
// 入参: a 事务前来源, b 事务后来源
// 返回: bool 是否未发生包结构或文档级元数据变更
func sameEditorSourceMeta(a, b *editorSource) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.reader != b.reader || a.document != b.document || a.directory != b.directory || a.fromNew != b.fromNew || a.fallbackDocID != b.fallbackDocID {
		return false
	}
	if (a.pages == nil) != (b.pages == nil) || len(a.pages) != len(b.pages) {
		return false
	}
	if !reflect.DeepEqual(a.info, b.info) || !maps.Equal(a.annotationPages, b.annotationPages) || !maps.Equal(a.retiredAttachments, b.retiredAttachments) || !maps.Equal(a.removedAttachments, b.removedAttachments) {
		return false
	}
	if len(a.annotationStates) != len(b.annotationStates) || len(a.structureObjects) != len(b.structureObjects) || len(a.structureStarts) != len(b.structureStarts) {
		return false
	}
	for k, av := range a.annotationStates {
		if bv, ok := b.annotationStates[k]; !ok || !maps.Equal(av, bv) {
			return false
		}
	}
	for k, av := range a.structureObjects {
		if bv, ok := b.structureObjects[k]; !ok || !maps.Equal(av, bv) {
			return false
		}
	}
	for k, av := range a.structureStarts {
		if bv, ok := b.structureStarts[k]; !ok || !maps.Equal(av, bv) {
			return false
		}
	}
	return true
}

// sameStructureRemaps 比较两个标引重映射表是否一致
// 入参: a 事务前重映射表, b 事务后重映射表
// 返回: bool 是否一致
func sameStructureRemaps(a, b map[string][]map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !maps.Equal(av[i], bv[i]) {
				return false
			}
		}
	}
	return true
}

// cloneNestedMap 深复制两层键值映射，避免事务快照与历史记录共享内层容器
// 入参: src 源双层映射
// 返回: map[K1]map[K2]V 独立双层映射
func cloneNestedMap[K1 comparable, K2 comparable, V any](src map[K1]map[K2]V) map[K1]map[K2]V {
	if src == nil {
		return nil
	}
	dst := make(map[K1]map[K2]V, len(src))
	for k, v := range src {
		dst[k] = maps.Clone(v)
	}
	return dst
}

// cloneStructureRemaps 深复制标引重映射表，隔离切片及内层映射
// 入参: src 源标引重映射表
// 返回: map[string][]map[string]string 独立副本
func cloneStructureRemaps(src map[string][]map[string]string) map[string][]map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string][]map[string]string, len(src))
	for k, list := range src {
		if list == nil {
			dst[k] = nil
			continue
		}
		cloned := make([]map[string]string, len(list))
		for i, m := range list {
			cloned[i] = maps.Clone(m)
		}
		dst[k] = cloned
	}
	return dst
}

// cloneEditorSourceFields 复制文档来源的共享可变字段，按需隔离原始页面与对象缓存
// 入参: src 文档来源, clonePages 是否克隆原始页面与对象映射
// 返回: *editorSource 隔离后的来源副本
func cloneEditorSourceFields(src *editorSource, clonePages bool) *editorSource {
	if src == nil {
		return nil
	}
	source := *src
	source.info = cloneEditorData(src.info)
	source.pageResList = slices.Clone(src.pageResList)
	source.annotationStates = cloneNestedMap(src.annotationStates)
	source.annotationPages = maps.Clone(src.annotationPages)
	source.retiredAttachments = maps.Clone(src.retiredAttachments)
	source.removedAttachments = maps.Clone(src.removedAttachments)
	source.structureObjects = cloneNestedMap(src.structureObjects)
	source.structureStarts = cloneNestedMap(src.structureStarts)
	source.attachmentIDs = maps.Clone(src.attachmentIDs)
	source.initialFiles = maps.Clone(src.initialFiles)
	source.appendOnlyFiles = maps.Clone(src.appendOnlyFiles)
	if clonePages {
		source.pages = maps.Clone(src.pages)
		source.origins = maps.Clone(src.origins)
	}
	return &source
}

// samePDFDestinations 比较两份PDF跳转目标映射内容是否一致
// 入参: a 事务前目标映射, b 事务后目标映射
// 返回: bool 是否一致
func samePDFDestinations(a, b *pdfDestinations) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return maps.Equal(a.targets, b.targets)
}

// Changes 获取最近一次有效修改的独立刷新范围
// 返回: EditorChanges 变化范围；未发生修改时为空
func (e *Editor) Changes() EditorChanges {
	return EditorChanges{All: e.changes.All, Pages: slices.Clone(e.changes.Pages)}
}

// Transaction 将多项修改作为一次原子操作提交，失败时保留原文档及历史
// 回调仅操作传入的编辑器，不可保留该实例供后续使用
// 后端、字体来源和撤销配置仅用于本次回调，不随事务提交
// 入参: edit 批量修改回调
// 返回: error 修改错误
func (e *Editor) Transaction(edit func(*Editor) error) error {
	before := *e
	before.Info = cloneEditorData(e.Info)
	next := e.transactionSnapshotSharedSource()
	next.history, next.historyIndex, next.historyLimit = nil, 0, 0
	pending := &EditorChanges{}
	next.transactionChanges = pending
	*e = next
	committed := false
	defer func() {
		if !committed {
			*e = before
			e.rollbackLoadedSourcePages()
		}
	}()
	if err := edit(e); err != nil {
		return err
	}
	changed := e.revision != before.revision
	changes := *pending
	if e.transactionChanges != pending {
		changes = EditorChanges{All: true}
	}
	e.transactionChanges = before.transactionChanges
	if e.ownedTx == pending {
		e.ownedTx = before.transactionChanges
	}
	e.history, e.historyIndex, e.historyLimit = before.history, before.historyIndex, before.historyLimit
	if !sameBackend(e.backends.FontResources, before.backends.FontResources) {
		e.clearFontResourceCache()
	}
	e.backends, e.fontDirs, e.fontFS = before.backends, before.fontDirs, before.fontFS
	e.SetResources(before.runtimeResources)
	e.fontSourcesCache = before.fontSourcesCache
	if e.sameResourceView(&before) {
		e.fontRenderer, e.fontMetrics = before.fontRenderer, before.fontMetrics
	} else {
		e.fontRenderer, e.fontMetrics = nil, make(map[string]FontMetrics)
	}
	if changed {
		e.serial, e.revision = before.serial, before.revision
		e.recordTransaction(before, changes)
	}
	committed = true
	return nil
}

// SetHistoryLimit 设置撤销和重做记录的总上限，默认关闭，非正数关闭并释放记录
// 缩减上限时优先保留最近的撤销记录，不影响当前文档及修订标识
// 记录文档、页面、对象及SetInfo操作，不包含Info的直接修改及字体、图片注册
// 入参: limit 最大记录数
func (e *Editor) SetHistoryLimit(limit int) {
	e.historyLimit = max(0, limit)
	if e.historyLimit == 0 {
		e.history = nil
		e.historyIndex = 0
		return
	}
	if len(e.history) > e.historyLimit {
		start := max(0, e.historyIndex-e.historyLimit)
		end := min(len(e.history), start+e.historyLimit)
		e.history = slices.Clone(e.history[start:end])
		e.historyIndex -= start
	}
}

// CanUndo 判断是否有可撤销的操作
// 返回: bool 是否可以撤销
func (e *Editor) CanUndo() bool {
	return e.historyIndex > 0
}

// CanRedo 判断是否有可重做的操作
// 返回: bool 是否可以重做
func (e *Editor) CanRedo() bool {
	return e.historyIndex < len(e.history)
}

// Undo 撤销最近一次操作，保留资源和已分配的标识
// 返回: bool 是否执行了撤销
func (e *Editor) Undo() bool {
	if !e.CanUndo() {
		return false
	}
	e.historyIndex--
	change := e.history[e.historyIndex]
	change.undo(e)
	e.changes, e.objectIndices = change.changes, nil
	e.invalidatePreviewPages(change.changes)
	e.revision = change.before
	return true
}

// Redo 重做最近一次撤销的操作，新的有效修改会清除重做记录
// 返回: bool 是否执行了重做
func (e *Editor) Redo() bool {
	if !e.CanRedo() {
		return false
	}
	change := e.history[e.historyIndex]
	change.redo(e)
	e.changes, e.objectIndices = change.changes, nil
	e.invalidatePreviewPages(change.changes)
	e.historyIndex++
	e.revision = change.after
	return true
}

// Revision 获取当前修订标识，撤销或重做时恢复对应标识
// Info的直接修改和资源注册不计入修订，标识仅在当前Editor实例内有效
// 返回: uint64 修订标识
func (e *Editor) Revision() uint64 {
	return e.revision
}

// merge 合并同一事务中的变化范围
// 入参: change 待合并的变化
func (c *EditorChanges) merge(change EditorChanges) {
	if c.All {
		return
	}
	if change.All {
		c.All = true
		c.Pages = nil
		return
	}
	for _, page := range change.Pages {
		if !slices.Contains(c.Pages, page) {
			c.Pages = append(c.Pages, page)
		}
	}
}

// transactionSnapshot 复制顶层容器，共享按替换方式更新的页面图层、图元和二进制资源
// 返回: Editor 独立事务状态
func (e *Editor) transactionSnapshot() Editor {
	next := e.transactionState()
	next.pages = slices.Clone(e.pages)
	return next
}

// transactionState 复制事务的文档和资源容器，页面表由调用方按作用范围保存
// 返回: Editor 独立文档状态
func (e *Editor) transactionState() Editor {
	next := *e
	next.Info = cloneEditorData(e.Info)
	next.resources = slices.Clone(e.resources)
	next.fonts, next.images, next.resourceID, next.promoted = maps.Clone(e.fonts), maps.Clone(e.images), maps.Clone(e.resourceID), maps.Clone(e.promoted)
	next.optimizedImages = maps.Clone(e.optimizedImages)
	next.origins = maps.Clone(e.origins)
	next.removedPages = maps.Clone(e.removedPages)
	next.structureRemaps = cloneStructureRemaps(e.structureRemaps)
	next.outlines = slices.Clone(e.outlines)
	next.pdfDestinations = e.pdfDestinations.clone()
	next.previewPages = maps.Clone(e.previewPages)
	next.pageScope = slices.Clone(e.pageScope)
	next.fontMetrics = maps.Clone(e.fontMetrics)
	next.objectIndices = nil
	next.nameCache = nil
	next.richIndex = nil
	next.source = cloneEditorSourceFields(e.source, true)
	return next
}

// transactionSnapshotSharedSource 复制顶层容器并共享只追加的原始页面解析缓存
// 返回: Editor 独立事务状态
func (e *Editor) transactionSnapshotSharedSource() Editor {
	next := e.transactionStateSharedSource()
	next.pages = slices.Clone(e.pages)
	return next
}

// transactionStateSharedSource 复制文档与资源容器，不克隆只追加的原始页面与对象缓存
// 返回: Editor 共享原始页面缓存的事务状态
func (e *Editor) transactionStateSharedSource() Editor {
	next := *e
	next.Info = cloneEditorData(e.Info)
	next.resources = slices.Clone(e.resources)
	next.fonts, next.images, next.resourceID, next.promoted = maps.Clone(e.fonts), maps.Clone(e.images), maps.Clone(e.resourceID), maps.Clone(e.promoted)
	next.optimizedImages = maps.Clone(e.optimizedImages)
	next.origins = maps.Clone(e.origins)
	next.removedPages = maps.Clone(e.removedPages)
	next.structureRemaps = cloneStructureRemaps(e.structureRemaps)
	next.outlines = slices.Clone(e.outlines)
	next.pdfDestinations = e.pdfDestinations.clone()
	next.previewPages = maps.Clone(e.previewPages)
	next.pageScope = slices.Clone(e.pageScope)
	next.fontMetrics = maps.Clone(e.fontMetrics)
	next.objectIndices = nil
	next.nameCache = nil
	next.richIndex = nil
	next.source = cloneEditorSourceFields(e.source, false)
	return next
}

// restoreTransaction 恢复文档状态，保留历史和配置，仅在同一资源视图内共享缓存
// 入参: state 文档状态
func (e *Editor) restoreTransaction(state Editor) {
	state = state.transactionSnapshotSharedSource()
	e.restoreTransactionState(state)
}

// restoreTransactionState 应用已隔离的文档状态，保留当前编辑器配置和资源
// 入参: state 已准备的事务状态
func (e *Editor) restoreTransactionState(state Editor) {
	state.retainDocumentResources(e)
	if !e.sameResourceView(&state) {
		state.fontRenderer, state.fontMetrics = nil, make(map[string]FontMetrics)
	}
	state.history, state.historyIndex, state.historyLimit = e.history, e.historyIndex, e.historyLimit
	state.serial = max(e.serial, state.serial)
	if !sameBackend(state.backends.FontResources, e.backends.FontResources) {
		state.clearFontResourceCache()
	}
	state.backends, state.fontDirs, state.fontFS = e.backends, e.fontDirs, e.fontFS
	state.SetResources(e.runtimeResources)
	state.fontSourcesCache = e.fontSourcesCache
	state.validatedPaths = e.validatedPaths
	state.encryption, state.encHold, state.output, state.OnWriteProgress = e.encryption, e.encHold, e.output, e.OnWriteProgress
	state.saveTarget, state.sourceFile = e.saveTarget, e.sourceFile
	*e = state
	e.syncLoadedSourcePages()
}

// sameResourceView 判断文档和资源来源是否一致，新建文档首次封装不改变已注册资源
// 入参: other 编辑快照
// 返回: bool 是否可共享注册资源和字体缓存
func (e *Editor) sameResourceView(other *Editor) bool {
	if !e.sameDocument(other) {
		return false
	}
	if e.source == nil {
		return other.source == nil || other.source.fromNew
	}
	if other.source == nil {
		return e.source.fromNew
	}
	return e.source.reader == other.source.reader
}

// retainDocumentResources 保留同一资源视图中后续注册的资源，不跨包快照混用编号和字体缓存
// 入参: source 当前文档状态
func (e *Editor) retainDocumentResources(source *Editor) {
	if !e.sameResourceView(source) {
		return
	}
	if len(source.resources) > len(e.resources) {
		e.resources = append(e.resources, source.resources[len(e.resources):]...)
	}
	maps.Copy(e.fonts, source.fonts)
	maps.Copy(e.images, source.images)
	maps.Copy(e.resourceID, source.resourceID)
	maps.Copy(e.promoted, source.promoted)
	if sameBackend(e.backends.FontResources, source.backends.FontResources) {
		e.fontRegistration = source.fontRegistration
	}
	e.maxID = max(e.maxID, source.maxID)
	e.fontRenderer, e.fontMetrics = source.fontRenderer, source.fontMetrics
}

// recordTransaction 记录事务快照，单页对象事务仅存按页增量，结构级事务保留完整快照
// 入参: before 修改前的文档状态, changes 可选的事务变化范围
func (e *Editor) recordTransaction(before Editor, changes ...EditorChanges) {
	scope := EditorChanges{All: true}
	if len(changes) != 0 {
		scope = changes[0]
	}
	if change := e.recordChangeScope(scope); change != nil {
		if e.canRecordPageDelta(&before, scope) {
			indexes := slices.Clone(scope.Pages)
			beforePages, afterPages := make([]PageContent, len(indexes)), make([]PageContent, len(indexes))
			for i, index := range indexes {
				page := before.pages[index]
				if isUnloadedSourcePage(page) && e.source != nil {
					if src := e.source.pages[page.ID]; src != nil && src.original != nil {
						page = editorSourceInitialPage(src)
					}
				}
				beforePages[i], afterPages[i] = page, e.pages[index]
			}
			beforeOrigins, afterOrigins := diffEditorOrigins(before.origins, e.origins)
			change.undo = func(e *Editor) {
				e.applyPageDelta(indexes, beforePages, beforeOrigins)
			}
			change.redo = func(e *Editor) {
				e.applyPageDelta(indexes, afterPages, afterOrigins)
			}
			return
		}
		if !scope.All && len(scope.Pages) != 0 && len(before.pages) == len(e.pages) && e.sameDocument(&before) {
			indexes := slices.Clone(scope.Pages)
			beforePages, afterPages := make([]PageContent, len(indexes)), make([]PageContent, len(indexes))
			for i, index := range indexes {
				page := before.pages[index]
				if isUnloadedSourcePage(page) && e.source != nil {
					if src := e.source.pages[page.ID]; src != nil && src.original != nil {
						page = editorSourceInitialPage(src)
					}
				}
				beforePages[i], afterPages[i] = page, e.pages[index]
			}
			after := e.transactionStateSharedSource()
			before.pages, after.pages, before.history, after.history = nil, nil, nil, nil
			change.undo = func(e *Editor) {
				after.retainDocumentResources(e)
				e.restorePageTransaction(before, indexes, beforePages)
			}
			change.redo = func(e *Editor) {
				before.retainDocumentResources(e)
				e.restorePageTransaction(after, indexes, afterPages)
			}
			return
		}
		after := e.transactionSnapshotSharedSource()
		before.history, after.history = nil, nil
		change.undo = func(e *Editor) {
			after.retainDocumentResources(e)
			e.restoreTransaction(before)
		}
		change.redo = func(e *Editor) {
			before.retainDocumentResources(e)
			e.restoreTransaction(after)
		}
	}
}

// restorePageTransaction 仅替换事务涉及的页面，复用当前独占页面表与共享原始页面缓存
// 入参: state 文档状态, indexes 受影响页码, pages 对应页面快照
func (e *Editor) restorePageTransaction(state Editor, indexes []int, pages []PageContent) {
	state = state.transactionStateSharedSource()
	state.pages = e.pages
	for i, index := range indexes {
		state.pages[index] = pages[i]
	}
	e.restoreTransactionState(state)
}

// canRecordPageDelta 判断事务是否仅影响局部页面及对象原文，可采用按页增量快照
// 入参: before 修改前的文档状态, scope 事务影响范围
// 返回: bool 是否允许记录按页增量快照
func (e *Editor) canRecordPageDelta(before *Editor, scope EditorChanges) bool {
	if scope.All || len(scope.Pages) == 0 || len(before.pages) != len(e.pages) || !e.sameResourceView(before) {
		return false
	}
	for _, index := range scope.Pages {
		if index < 0 || index >= len(e.pages) || before.pages[index].ID != e.pages[index].ID {
			return false
		}
	}
	if !sameEditorSourceMeta(before.source, e.source) || !reflect.DeepEqual(before.Info, e.Info) {
		return false
	}
	if !maps.Equal(before.removedPages, e.removedPages) || !sameStructureRemaps(before.structureRemaps, e.structureRemaps) {
		return false
	}
	if !bytes.Equal(before.outlines, e.outlines) || !samePDFDestinations(before.pdfDestinations, e.pdfDestinations) || len(before.optimizedImages) != len(e.optimizedImages) {
		return false
	}
	return len(before.resources) <= len(e.resources)
}

// applyPageDelta 应用按页增量快照，仅替换变动页面和变动的原文映射键
// 入参: indexes 受影响页码, pages 目标页面内容, origins 变动的原文映射项
func (e *Editor) applyPageDelta(indexes []int, pages []PageContent, origins []editorOriginChange) {
	for i, index := range indexes {
		page := pages[i]
		if isUnloadedSourcePage(page) && e.source != nil {
			if src := e.source.pages[page.ID]; src != nil && src.original != nil {
				page = editorSourceInitialPage(src)
			}
		}
		e.pages[index] = page
	}
	if len(origins) != 0 {
		if e.origins == nil {
			e.origins = make(map[string]*editorObjectOrigin, len(origins))
		}
		for _, item := range origins {
			if item.existed {
				e.origins[item.id] = item.value
			} else {
				delete(e.origins, item.id)
			}
		}
	}
}

// rollbackLoadedSourcePages 回滚失败事务期间惰性加载进共享缓存的原始页面与对象映射
func (e *Editor) rollbackLoadedSourcePages() {
	if e.source == nil || len(e.source.pages) == 0 {
		return
	}
	for i := range e.pages {
		page := e.pages[i]
		if !isUnloadedSourcePage(page) {
			continue
		}
		src := e.source.pages[page.ID]
		if src == nil || src.original == nil {
			continue
		}
		e.source.pages[page.ID] = &editorSourcePage{ref: src.ref}
		for id := range src.nodes {
			if cur := e.source.origins[id]; cur != nil && cur.page == src {
				delete(e.source.origins, id)
			}
		}
	}
}

// syncLoadedSourcePages 将恢复快照中的未加载空壳页与已加载的共享原始页面缓存对齐
func (e *Editor) syncLoadedSourcePages() {
	if e.source == nil || len(e.source.pages) == 0 {
		return
	}
	for i := range e.pages {
		page := e.pages[i]
		if !isUnloadedSourcePage(page) {
			continue
		}
		if src := e.source.pages[page.ID]; src != nil && src.original != nil {
			e.pages[i] = editorSourceInitialPage(src)
		}
	}
}

// recordChange 分配修订标识并为已完成的有效修改预留记录
// 入参: pages 受影响页码，省略表示全局变化
// 返回: *editorChange 记录，未启用历史时为nil
func (e *Editor) recordChange(pages ...int) *editorChange {
	return e.recordChangeScope(EditorChanges{All: len(pages) == 0, Pages: slices.Clone(pages)})
}

// recordChangeScope 分配修订并记录变更范围，事务中同步合并
// 入参: scope 变更范围
// 返回: *editorChange 可补充回放操作的记录
func (e *Editor) recordChangeScope(scope EditorChanges) *editorChange {
	e.invalidatePreviewPages(scope)
	e.objectIndices = nil
	e.changes = scope
	if e.transactionChanges != nil {
		e.transactionChanges.merge(scope)
	}
	e.serial++
	change := editorChange{before: e.revision, after: e.serial, changes: scope}
	e.revision = e.serial
	if e.historyLimit == 0 {
		return nil
	}
	clear(e.history[e.historyIndex:])
	e.history = e.history[:e.historyIndex]
	if len(e.history) == e.historyLimit {
		copy(e.history, e.history[1:])
		e.history = e.history[:len(e.history)-1]
	}
	e.history = append(e.history, change)
	e.historyIndex = len(e.history)
	return &e.history[e.historyIndex-1]
}

// recordPageAddition 记录新增或复制的页面
// 入参: index 页面索引
func (e *Editor) recordPageAddition(index int) {
	if change := e.recordChange(); change != nil {
		page := copyEditorPage(e.pages[index])
		change.undo = func(e *Editor) { e.pages = slices.Delete(e.pages, index, index+1) }
		change.redo = func(e *Editor) { e.pages = slices.Insert(e.pages, index, copyEditorPage(page)) }
	}
}
