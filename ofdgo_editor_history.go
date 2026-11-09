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
	"maps"
	"slices"
)

// editorChange 页面或对象操作及其修订标识
type editorChange struct {
	undo    func(*Editor)
	redo    func(*Editor)
	before  uint64
	after   uint64
	changes EditorChanges
}

// EditorChanges 描述最近一次修改或历史回放的刷新范围
// All表示文档结构或共享资源变化；否则Pages为受影响的零基页码
// 返回值只覆盖最近一次操作，调用方应在每次操作后读取
type EditorChanges struct {
	All   bool  `json:"all"`
	Pages []int `json:"pages,omitempty"`
}

// Changes 获取最近一次有效修改的独立刷新范围
// 返回: EditorChanges 变化范围；未发生修改时为空
func (e *Editor) Changes() EditorChanges {
	return EditorChanges{All: e.changes.All, Pages: slices.Clone(e.changes.Pages)}
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

// Transaction 将多项修改作为一次原子操作提交，失败时保留原文档及历史
// 回调仅操作传入的编辑器，不可保留该实例供后续使用
// 后端、字体来源和撤销配置仅用于本次回调，不随事务提交
// 入参: edit 批量修改回调
// 返回: error 修改错误
func (e *Editor) Transaction(edit func(*Editor) error) error {
	before := *e
	before.Info = cloneEditorData(e.Info)
	next := e.transactionSnapshot()
	next.history, next.historyIndex, next.historyLimit = nil, 0, 0
	pending := &EditorChanges{}
	next.transactionChanges = pending
	*e = next
	committed := false
	defer func() {
		if !committed {
			*e = before
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
	e.changes, e.objectIndex = change.changes, nil
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
	e.changes, e.objectIndex = change.changes, nil
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
	next.fonts, next.images, next.resourceID = maps.Clone(e.fonts), maps.Clone(e.images), maps.Clone(e.resourceID)
	next.origins = maps.Clone(e.origins)
	next.removedPages = maps.Clone(e.removedPages)
	next.structureRemaps = maps.Clone(e.structureRemaps)
	next.fontMetrics = maps.Clone(e.fontMetrics)
	if e.source != nil {
		source := *e.source
		source.pages, source.origins = maps.Clone(source.pages), maps.Clone(source.origins)
		next.source = &source
	}
	return next
}

// restoreTransaction 恢复文档状态，保留历史和配置，仅在同一资源视图内共享缓存
// 入参: state 文档状态
func (e *Editor) restoreTransaction(state Editor) {
	state = state.transactionSnapshot()
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
	state.encryption, state.output, state.OnWriteProgress = e.encryption, e.output, e.OnWriteProgress
	state.saveTarget = e.saveTarget
	*e = state
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
	if sameBackend(e.backends.FontResources, source.backends.FontResources) {
		e.fontRegistration = source.fontRegistration
	}
	e.maxID = max(e.maxID, source.maxID)
	e.fontRenderer, e.fontMetrics = source.fontRenderer, source.fontMetrics
}

// recordTransaction 记录事务快照，在各自资源视图中保留后续注册的资源
// 入参: before 修改前的文档状态, changes 可选的事务变化范围
func (e *Editor) recordTransaction(before Editor, changes ...EditorChanges) {
	scope := EditorChanges{All: true}
	if len(changes) != 0 {
		scope = changes[0]
	}
	if change := e.recordChangeScope(scope); change != nil {
		if !scope.All && len(scope.Pages) != 0 && len(before.pages) == len(e.pages) && e.sameDocument(&before) {
			beforePages, afterPages := make([]PageContent, len(scope.Pages)), make([]PageContent, len(scope.Pages))
			for i, index := range scope.Pages {
				beforePages[i], afterPages[i] = before.pages[index], e.pages[index]
			}
			after := e.transactionState()
			before.pages, after.pages, before.history, after.history = nil, nil, nil, nil
			change.undo = func(e *Editor) {
				after.retainDocumentResources(e)
				e.restorePageTransaction(before, scope.Pages, beforePages)
			}
			change.redo = func(e *Editor) {
				before.retainDocumentResources(e)
				e.restorePageTransaction(after, scope.Pages, afterPages)
			}
			return
		}
		after := e.transactionSnapshot()
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

// restorePageTransaction 仅替换事务涉及的页面，复用当前独占页面表
// 入参: state 文档状态, indexes 受影响页码, pages 对应页面快照
func (e *Editor) restorePageTransaction(state Editor, indexes []int, pages []PageContent) {
	state = state.transactionState()
	state.pages = e.pages
	for i, index := range indexes {
		state.pages[index] = pages[i]
	}
	e.restoreTransactionState(state)
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
	e.objectIndex = nil
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
