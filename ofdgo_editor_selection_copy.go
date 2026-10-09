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

import "fmt"

// ObjectSelection 保存正文与注解的独立快照，共享当前编辑器的不可变资源
// 来源修改、删除或撤销不改变快照，不用于跨文档传输
type ObjectSelection struct {
	editor      *Editor
	objects     []GraphicObject
	annotations *AnnotationSelection
}

// CaptureSelection 捕获正文与注解，不修改文档或历史
// 入参: page 页面索引, objects 正文标识, annotations 注解标识，各列表不得重复
// 返回: *ObjectSelection 独立快照, error 标识或资源错误
func (e *Editor) CaptureSelection(page int, objects, annotations []string) (*ObjectSelection, error) {
	items, err := e.Objects(page, objects)
	if err != nil {
		return nil, err
	}
	selection := &ObjectSelection{editor: e, objects: items}
	if len(annotations) != 0 {
		selection.annotations, err = e.CaptureAnnotations(page, annotations)
		if err != nil {
			return nil, err
		}
	}
	return selection, nil
}

// PasteSelection 原子粘贴正文与注解并平移，失败回滚，一次撤销恢复全部
// 入参: page 目标页面索引, selection 当前编辑器快照, dx 横向位移，单位为毫米, dy 纵向位移，单位为毫米
// 返回: []string 新正文标识, []string 新注解标识, error 标识、资源或编辑错误
func (e *Editor) PasteSelection(page int, selection *ObjectSelection, dx, dy float64) ([]string, []string, error) {
	if selection == nil || selection.editor != e || !finite(dx) || !finite(dy) {
		return nil, nil, fmt.Errorf("invalid object selection or offset")
	}
	var objects, annotations []string
	err := e.Transaction(func(edit *Editor) error {
		var err error
		objects, err = edit.CopyObjects(page, selection.objects, dx, dy)
		if err == nil && selection.annotations != nil {
			annotations, err = edit.PasteAnnotations(page, selection.annotations, dx, dy)
		}
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return objects, annotations, nil
}

// PasteSelectionToComposite 将正文快照粘贴至内部容器，保留源外观，拒绝嵌套注解
// 入参: page 目标页面索引, path 父复合路径, selection 当前编辑器快照, dx 横向位移，单位为毫米, dy 纵向位移，单位为毫米
// 返回: []int 新成员序号, error 选区、资源或编辑错误
func (e *Editor) PasteSelectionToComposite(page int, path ObjectPath, selection *ObjectSelection, dx, dy float64) ([]int, error) {
	if selection == nil || selection.editor != e {
		return nil, fmt.Errorf("object selection belongs to another editor")
	}
	if selection.annotations != nil {
		return nil, fmt.Errorf("annotations cannot be nested in a page object")
	}
	return e.CopyObjectsToComposite(page, path, selection.objects, dx, dy)
}
