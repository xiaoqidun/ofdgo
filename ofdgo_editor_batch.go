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
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/tdewolff/canvas"
)

// Objects 按绘制顺序获取选区的独立快照，保留编辑中的段落信息。
// 入参: page 页面索引, ids 对象标识，不得重复
// 返回: []GraphicObject 独立对象副本, error 错误信息
func (e *Editor) Objects(page int, ids []string) ([]GraphicObject, error) {
	_, indexes, err := e.selectedObjects(page, ids)
	if err != nil {
		return nil, err
	}
	slices.Sort(indexes)
	objects := make([]GraphicObject, len(indexes))
	for i, index := range indexes {
		objects[i], err = cloneEditorObject(e.pages[page].Content.Layer[0].Objects[index])
		if err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// CopyObjects 将对象快照按输入顺序复制到目标页并平移，分配新ID，提交一次撤销记录。
// 对象引用当前Editor已注册的资源，保留段落信息，不修改输入快照。
// 入参: page 目标页面索引, objects 对象快照, dx、dy 毫米位移
// 返回: []string 按绘制顺序排列的新对象标识, error 错误信息
func (e *Editor) CopyObjects(page int, objects []GraphicObject, dx, dy float64) ([]string, error) {
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("copy requires finite offsets")
	}
	content, err := e.page(page)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, nil
	}
	before := content.Content.Layer[0].Objects
	prepared := make([]GraphicObject, len(objects))
	result := make([]string, len(objects))
	for i, object := range objects {
		result[i] = strconv.Itoa(e.maxID + i + 1)
		object, err := e.prepareObject(result[i], object)
		if err != nil {
			return nil, err
		}
		object, err = transformEditorObject(object, dx, dy, 1)
		if err != nil {
			return nil, err
		}
		prepared[i], err = e.prepareObject(result[i], object)
		if err != nil {
			return nil, err
		}
	}
	e.maxID += len(objects)
	e.replaceObjects(page, append(slices.Clone(before), prepared...))
	return result, nil
}

// OrderObjects 调整同页选区的绘制顺序，保留选中及未选中对象各自的相对顺序。
// 入参: page 页面索引, ids 对象标识, order 为up、down、top或bottom
// 返回: error 错误信息
func (e *Editor) OrderObjects(page int, ids []string, order string) error {
	if !slices.Contains([]string{"up", "down", "top", "bottom"}, order) {
		return fmt.Errorf("invalid object order %q", order)
	}
	_, indexes, err := e.selectedObjects(page, ids)
	if err != nil || len(indexes) == 0 {
		return err
	}
	objects := slices.Clone(e.pages[page].Content.Layer[0].Objects)
	selected := make([]bool, len(objects))
	for _, index := range indexes {
		selected[index] = true
	}
	switch order {
	case "up":
		for i := len(objects) - 2; i >= 0; i-- {
			if selected[i] && !selected[i+1] {
				objects[i], objects[i+1] = objects[i+1], objects[i]
				selected[i], selected[i+1] = false, true
			}
		}
	case "down":
		for i := 1; i < len(objects); i++ {
			if selected[i] && !selected[i-1] {
				objects[i], objects[i-1] = objects[i-1], objects[i]
				selected[i], selected[i-1] = false, true
			}
		}
	default:
		ordered := make([]GraphicObject, 0, len(objects))
		for _, takeSelected := range []bool{order == "bottom", order != "bottom"} {
			for i, object := range objects {
				if selected[i] == takeSelected {
					ordered = append(ordered, object)
				}
			}
		}
		objects = ordered
	}
	e.replaceObjects(page, objects)
	return nil
}

// DistributeObjects 按可见范围等距分布同页对象，固定两端对象，保留绘制顺序。
// 不足三个对象时不修改；允许负间距，无法保持位置顺序的重叠布局返回错误。
// 入参: page 页面索引, ids 对象标识, axis 为horizontal或vertical
// 返回: error 错误信息
func (e *Editor) DistributeObjects(page int, ids []string, axis string) error {
	if axis != "horizontal" && axis != "vertical" {
		return fmt.Errorf("invalid distribution axis %q", axis)
	}
	objects, indexes, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) < 3 {
		return err
	}
	boxes, err := e.objectBounds(page, objects)
	if err != nil {
		return err
	}
	positions, sizes := make([]float64, len(boxes)), make([]float64, len(boxes))
	order := make([]int, len(boxes))
	total := 0.0
	for i, box := range boxes {
		positions[i], sizes[i] = box.X, box.W
		if axis == "vertical" {
			positions[i], sizes[i] = box.Y, box.H
		}
		order[i] = i
		total += sizes[i]
	}
	slices.SortFunc(order, func(i, j int) int {
		if c := cmp.Compare(positions[i], positions[j]); c != 0 {
			return c
		}
		return cmp.Compare(indexes[i], indexes[j])
	})
	first, last := order[0], order[len(order)-1]
	gap := (positions[last] + sizes[last] - positions[first] - total) / float64(len(order)-1)
	for i, index := range order[:len(order)-1] {
		step := sizes[index] + gap
		if canvas.Equal(step, 0) {
			step = 0
		}
		if step < 0 || step == 0 && indexes[index] > indexes[order[i+1]] {
			return fmt.Errorf("overlap prevents ordered equal-gap distribution")
		}
	}
	next := positions[first] + sizes[first] + gap
	updates := make([]GraphicObject, 0, len(order)-2)
	for _, index := range order[1 : len(order)-1] {
		position := next
		next += sizes[index] + gap
		if canvas.Equal(position, positions[index]) {
			continue
		}
		dx, dy := position-positions[index], 0.0
		if axis == "vertical" {
			dx, dy = 0, dx
		}
		object, err := cloneEditorObject(objects[index])
		if err != nil {
			return err
		}
		object, err = transformEditorObject(object, dx, dy, 1)
		if err != nil {
			return err
		}
		updates = append(updates, object)
	}
	return e.UpdateObjects(page, updates)
}

// replaceObjects 替换正文对象列表，隔离历史快照与后续的增删、排序操作。
// 入参: page 页面索引, objects 新的正文对象列表
func (e *Editor) replaceObjects(page int, objects []GraphicObject) {
	before := e.pages[page].Content.Layer[0].Objects
	if reflect.DeepEqual(before, objects) {
		return
	}
	before, after := slices.Clone(before), slices.Clone(objects)
	e.pages[page].Content.Layer[0].Objects = slices.Clone(after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.pages[page].Content.Layer[0].Objects = slices.Clone(before) }
		change.redo = func(e *Editor) { e.pages[page].Content.Layer[0].Objects = slices.Clone(after) }
	}
}

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
	e.replaceObjects(page, after)
	return nil
}

// selectedObjects 校验同页选择，返回内部只读对象与对应索引。
// 入参: page 页面索引, ids 对象标识，不得重复
// 返回: []GraphicObject 所选对象, []int 正文对象索引, error 错误信息
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
// 入参: object 图形对象
// 返回: string 对象标识，不支持的类型返回空字符串
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
// 入参: page 页面索引, objects 待度量的对象
// 返回: []Box 对象范围，文字采用字形范围，其余采用Boundary, error 错误信息
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
