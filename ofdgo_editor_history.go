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

// Transaction 将多项修改作为一次原子操作提交，失败时保留原文档及历史
// 回调仅操作传入的编辑器，不可保留该实例供后续使用
// 入参: edit 批量修改回调
// 返回: error 修改错误
func (e *Editor) Transaction(edit func(*Editor) error) error {
	before := *e
	before.Info = cloneEditorData(e.Info)
	next := e.transactionSnapshot()
	next.history, next.historyIndex, next.historyLimit = nil, 0, 0
	*e = next
	if err := edit(e); err != nil {
		*e = before
		return err
	}
	changed := e.revision != before.revision
	e.history, e.historyIndex, e.historyLimit = before.history, before.historyIndex, before.historyLimit
	e.backends, e.fontFS = before.backends, before.fontFS
	e.fontRenderer, e.fontMetrics = before.fontRenderer, before.fontMetrics
	if changed {
		e.serial, e.revision = before.serial, before.revision
		if change := e.recordChange(); change != nil {
			after := e.transactionSnapshot()
			before.history, after.history = nil, nil
			change.undo = func(e *Editor) { e.restoreTransaction(before) }
			change.redo = func(e *Editor) { e.restoreTransaction(after) }
		}
	}
	return nil
}

// transactionSnapshot 复制可变容器，保留只读XML、图元及二进制资源的共享
// 返回: Editor 独立事务状态
func (e *Editor) transactionSnapshot() Editor {
	next := *e
	next.Info = cloneEditorData(e.Info)
	next.pages = make([]PageContent, len(e.pages))
	for i, page := range e.pages {
		next.pages[i] = copyEditorPage(page)
	}
	next.resources = slices.Clone(e.resources)
	next.fonts, next.images, next.resourceID = maps.Clone(e.fonts), maps.Clone(e.images), maps.Clone(e.resourceID)
	next.origins = maps.Clone(e.origins)
	next.removedPages = maps.Clone(e.removedPages)
	next.fontMetrics = maps.Clone(e.fontMetrics)
	if e.source != nil {
		source := *e.source
		source.pages, source.origins = maps.Clone(source.pages), maps.Clone(source.origins)
		next.source = &source
	}
	return next
}

// restoreTransaction 恢复文档状态并保留历史容器、共享资源和递增标识
// 入参: state 文档状态
func (e *Editor) restoreTransaction(state Editor) {
	state = state.transactionSnapshot()
	if len(e.resources) > len(state.resources) {
		state.resources = append(state.resources, e.resources[len(state.resources):]...)
	}
	maps.Copy(state.fonts, e.fonts)
	maps.Copy(state.images, e.images)
	maps.Copy(state.resourceID, e.resourceID)
	state.history, state.historyIndex, state.historyLimit = e.history, e.historyIndex, e.historyLimit
	state.serial, state.maxID = max(e.serial, state.serial), max(e.maxID, state.maxID)
	state.backends, state.fontFS = e.backends, e.fontFS
	state.fontRenderer, state.fontMetrics = e.fontRenderer, e.fontMetrics
	*e = state
}

// editorChange 页面或对象操作及其修订标识
type editorChange struct {
	undo   func(*Editor)
	redo   func(*Editor)
	before uint64
	after  uint64
}

// SetHistoryLimit 设置撤销和重做记录的总上限，默认关闭，非正数关闭并释放记录
// 缩减上限时优先保留最近的撤销记录，不影响当前文档及修订标识
// 记录页面、对象及SetInfo操作，不包含Info的直接修改及字体、图片注册，资源继续共享
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

// recordChange 分配修订标识并为已完成的有效修改预留记录
// 返回: *editorChange 记录，未启用历史时为nil
func (e *Editor) recordChange() *editorChange {
	e.serial++
	change := editorChange{before: e.revision, after: e.serial}
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
