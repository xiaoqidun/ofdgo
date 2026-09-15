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

// UpdateObjects 原子替换同页对象，按各对象ID定位，全部校验通过后提交一次历史记录。
// 入参: page 页面索引, objects 新对象内容，ID不得重复
// 返回: error 错误信息
func (e *Editor) UpdateObjects(page int, objects []GraphicObject) error {
	ids := make([]string, len(objects))
	for i, object := range objects {
		ids[i] = editorObjectID(object)
	}
	before, indexes, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	after := make([]GraphicObject, len(objects))
	for i, object := range objects {
		after[i], err = e.prepareObject(ids[i], object)
		if err != nil {
			return err
		}
	}
	if reflect.DeepEqual(before, after) {
		return nil
	}
	apply := func(e *Editor, objects []GraphicObject) {
		for i, index := range indexes {
			e.pages[page].Content.Layer[0].Objects[index] = objects[i]
		}
	}
	apply(e, after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { apply(e, before) }
		change.redo = func(e *Editor) { apply(e, after) }
	}
	return nil
}

// TransformObjects 同页对象以页面原点等比缩放后统一平移，保持相对位置，一次撤销恢复全部。
// 入参: page 页面索引, ids 对象标识, dx、dy 位移, scale 正缩放比例
// 返回: error 错误信息
func (e *Editor) TransformObjects(page int, ids []string, dx, dy, scale float64) error {
	if !finite(dx) || !finite(dy) || !finite(scale) || scale <= 0 {
		return fmt.Errorf("transform requires finite offsets and a positive finite scale")
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	if dx == 0 && dy == 0 && scale == 1 {
		return nil
	}
	for i, object := range objects {
		object, err = cloneEditorObject(object)
		if err != nil {
			return err
		}
		objects[i], err = transformEditorObject(object, dx, dy, scale)
		if err != nil {
			return err
		}
	}
	return e.UpdateObjects(page, objects)
}

// AlignObjects 单对象对齐页面，多对象相互对齐至选区边界，文字采用实际字形范围。
// 入参: page 页面索引, ids 对象标识, alignment 为left、center、right、top、middle或bottom
// 返回: error 错误信息
func (e *Editor) AlignObjects(page int, ids []string, alignment string) error {
	if !slices.Contains([]string{"left", "center", "right", "top", "middle", "bottom"}, alignment) {
		return fmt.Errorf("invalid alignment %q", alignment)
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) == 0 {
		return err
	}
	boxes, err := e.objectBounds(page, objects)
	if err != nil {
		return err
	}
	target, _ := ParseBox(e.pages[page].Area.PhysicalBox)
	if len(objects) > 1 {
		target = Box{}
		for _, box := range boxes {
			target = unionTextBox(target, box)
		}
	}
	for i, box := range boxes {
		dx, dy := 0.0, 0.0
		switch alignment {
		case "left":
			dx = target.X - box.X
		case "center":
			dx = target.X + (target.W-box.W)/2 - box.X
		case "right":
			dx = target.X + target.W - box.W - box.X
		case "top":
			dy = target.Y - box.Y
		case "middle":
			dy = target.Y + (target.H-box.H)/2 - box.Y
		case "bottom":
			dy = target.Y + target.H - box.H - box.Y
		}
		objects[i], err = cloneEditorObject(objects[i])
		if err != nil {
			return err
		}
		objects[i], err = transformEditorObject(objects[i], dx, dy, 1)
		if err != nil {
			return err
		}
	}
	return e.UpdateObjects(page, objects)
}

// DeleteObjects 原子删除同页对象，保留其余对象顺序，一次撤销恢复全部。
// 入参: page 页面索引, ids 对象标识
// 返回: error 错误信息
func (e *Editor) DeleteObjects(page int, ids []string) error {
	_, indexes, err := e.selectedObjects(page, ids)
	if err != nil || len(indexes) == 0 {
		return err
	}
	before := e.pages[page].Content.Layer[0].Objects
	after := slices.Clone(before)
	slices.Sort(indexes)
	for _, index := range slices.Backward(indexes) {
		after = slices.Delete(after, index, index+1)
	}
	e.pages[page].Content.Layer[0].Objects = slices.Clone(after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.pages[page].Content.Layer[0].Objects = slices.Clone(before) }
		change.redo = func(e *Editor) { e.pages[page].Content.Layer[0].Objects = slices.Clone(after) }
	}
	return nil
}

// selectedObjects 校验同页选择，返回内部只读对象与对应索引。
func (e *Editor) selectedObjects(page int, ids []string) ([]GraphicObject, []int, error) {
	if _, err := e.page(page); err != nil {
		return nil, nil, err
	}
	objects := make([]GraphicObject, len(ids))
	indexes := make([]int, len(ids))
	seen := make(map[string]bool, len(ids))
	for i, id := range ids {
		if seen[id] {
			return nil, nil, fmt.Errorf("duplicate object %q", id)
		}
		seen[id] = true
		layer, index, err := e.findObject(page, id)
		if err != nil {
			return nil, nil, err
		}
		objects[i], indexes[i] = layer.Objects[index], index
	}
	return objects, indexes, nil
}

// editorObjectID 获取可创作对象的标准标识。
func editorObjectID(object GraphicObject) string {
	switch object.Type {
	case "TextObject":
		return object.TextObject.ID
	case "PathObject":
		return object.PathObject.ID
	case "ImageObject":
		return object.ImageObject.ID
	}
	return ""
}

// objectBounds 批量获取对齐范围，每页只度量一次文字。
func (e *Editor) objectBounds(page int, objects []GraphicObject) ([]Box, error) {
	textBoxes := make(map[string]Box)
	if slices.ContainsFunc(objects, func(o GraphicObject) bool { return o.Type == "TextObject" }) {
		reader, err := e.Reader()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		text, err := NewRenderer(reader).PageText(&e.pages[page])
		if err != nil {
			return nil, err
		}
		for _, run := range text.Runs {
			for _, box := range run.Boxes {
				textBoxes[run.ID] = unionTextBox(textBoxes[run.ID], box)
			}
		}
	}
	boxes := make([]Box, len(objects))
	for i, object := range objects {
		switch object.Type {
		case "TextObject":
			boxes[i] = textBoxes[object.TextObject.ID]
		case "PathObject":
			boxes[i], _ = ParseBox(object.PathObject.Boundary)
		case "ImageObject":
			boxes[i], _ = ParseBox(object.ImageObject.Boundary)
		}
		if boxes[i].W <= 0 || boxes[i].H <= 0 {
			return nil, fmt.Errorf("object %q has no visible bounds", editorObjectID(object))
		}
	}
	return boxes, nil
}
