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
	"fmt"
	"reflect"
	"slices"
)

// DocumentActions 获取文档打开动作的独立副本，不执行动作
// 返回: []Action 动作列表
func (e *Editor) DocumentActions() []Action {
	if e.source == nil {
		return nil
	}
	return cloneEditorData(e.source.document.Actions)
}

// SetDocumentActions 替换文档打开动作，空列表清除动作并记录撤销
// 入参: actions 事件为DO的动作及已注册的资源引用
// 返回: error 结构、编码或提交错误
func (e *Editor) SetDocumentActions(actions []Action) error {
	if err := validateLifecycleActions(actions, "DO"); err != nil {
		return err
	}
	current := e.DocumentActions()
	if reflect.DeepEqual(current, actions) || len(current) == 0 && len(actions) == 0 {
		return nil
	}
	base := e.source
	var err error
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return err
		}
	}
	name := cleanPackagePath(base.reader.OFD.DocBody[base.reader.documentIndex].DocRoot)
	data, err := base.reader.readFile(name)
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	encoded, err := editorActionsXML(actions)
	if err != nil {
		return err
	}
	patch := editorXMLPatch{root.close, root.close, encoded}
	context := root
	if node := root.child("Actions"); node != nil {
		patch.start, patch.end = node.start, node.end
		context = node
	} else {
		for _, child := range root.children {
			if packageOFDNode(child, child.name.Local) && slices.Contains([]string{"VPreferences", "Bookmarks", "Annotations", "CustomTags", "Attachments", "Extensions"}, child.name.Local) {
				patch.start, patch.end = child.start, child.start
				break
			}
		}
	}
	patch.data, err = editorXMLGenerated(encoded, context.name.Space)
	if err != nil {
		return err
	}
	return e.commitAnnotationParts(base, map[string][]byte{name: editorPatchXML(data, []editorXMLPatch{patch})})
}

// SetPageActions 替换页面打开动作，输入独立保存，空列表清除动作
// 入参: index 页面索引, actions 事件为PO的动作及已注册的资源引用
// 返回: error 页码或动作错误
func (e *Editor) SetPageActions(index int, actions []Action) error {
	if err := validateLifecycleActions(actions, "PO"); err != nil {
		return err
	}
	page, err := e.page(index)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(page.Actions, actions) || len(page.Actions) == 0 && len(actions) == 0 {
		return nil
	}
	before, after := cloneEditorData(page.Actions), cloneEditorData(actions)
	page.Actions = cloneEditorData(after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.pages[index].Actions = cloneEditorData(before) }
		change.redo = func(e *Editor) { e.pages[index].Actions = cloneEditorData(after) }
	}
	return nil
}

// validateLifecycleActions 校验文档或页面动作的事件类型和动作结构
// 入参: actions 动作列表, event 允许的事件
// 返回: error 事件或动作错误
func validateLifecycleActions(actions []Action, event string) error {
	for _, action := range actions {
		if action.Event != event {
			return fmt.Errorf("expected %s action event", event)
		}
	}
	return validateObjectActions(actions)
}

// editorActionsXML 复用动作编码并补齐独立片段的命名空间，空列表不输出节点
// 入参: actions 动作列表
// 返回: []byte 动作片段, error 编码错误
func editorActionsXML(actions []Action) ([]byte, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	data, err := encodeOFDXML(func(x *ofdXML) {
		x.root("Document", nil)
		x.actions(actions)
		x.end("Document")
	})
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	node := root.child("Actions")
	return editorXMLStandalone(data[node.start:node.end], node)
}
