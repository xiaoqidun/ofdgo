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

// editorObjectPosition 对象所属图层及层内索引
type editorObjectPosition struct {
	layer int
	index int
}

// compareEditorPosition 比较对象在页面结构中的先后顺序
// 入参: a、b 对象位置
// 返回: int 比较结果
func compareEditorPosition(a, b editorObjectPosition) int {
	if value := cmp.Compare(a.layer, b.layer); value != 0 {
		return value
	}
	return cmp.Compare(a.index, b.index)
}

// Objects 按绘制顺序获取选区的独立快照，保留编辑中的段落信息
// 入参: page 页面索引, ids 对象标识，不得重复
// 返回: []GraphicObject 独立对象副本, error 错误信息
func (e *Editor) Objects(page int, ids []string) ([]GraphicObject, error) {
	_, indexes, err := e.selectedObjects(page, ids)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(indexes, compareEditorPosition)
	objects := make([]GraphicObject, len(indexes))
	for i, index := range indexes {
		object := e.pages[page].Content.Layer[index.layer].Objects[index.index]
		capability, err := e.ObjectCapabilities(page, editorObjectID(object))
		if err != nil {
			return nil, err
		}
		if !capability.Copy {
			return nil, fmt.Errorf("object %q cannot be copied: %w", editorObjectID(object), capability.editError())
		}
		objects[i], err = cloneEditorObject(object)
		if err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// CopyObjects 将对象快照按输入顺序复制到目标页并平移，分配新ID，提交一次撤销记录
// 对象引用当前Editor已注册的资源，保留段落信息，不修改输入快照
// 入参: page 目标页面索引, objects 对象快照, dx、dy 毫米位移
// 返回: []string 按绘制顺序排列的新对象标识, error 错误信息
func (e *Editor) CopyObjects(page int, objects []GraphicObject, dx, dy float64) ([]string, error) {
	if !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("copy requires finite offsets")
	}
	_, err := e.page(page)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, nil
	}
	if !e.sourceRGB() {
		return nil, fmt.Errorf("inserting objects requires an RGB default color space")
	}
	if err := e.prepareSourceIDs(); err != nil {
		return nil, err
	}
	prepared := make([]GraphicObject, len(objects))
	result := make([]string, len(objects))
	for i, object := range objects {
		origin := e.objectOrigin(editorObjectID(object))
		result[i] = strconv.Itoa(e.maxID + i + 1)
		object, err := e.prepareObject(result[i], object)
		if err != nil {
			return nil, err
		}
		if dx != 0 || dy != 0 {
			object, err = transformEditorObject(object, dx, dy, 1)
			if err != nil {
				return nil, err
			}
			codes := object.TextObject.TextCode
			object, err = e.prepareObject(result[i], object)
			if err != nil {
				return nil, err
			}
			if origin != nil {
				object.TextObject.TextCode = codes
			}
		}
		prepared[i] = object
	}
	e.maxID += len(objects)
	for i, object := range objects {
		if origin := e.objectOrigin(editorObjectID(object)); origin != nil {
			e.source.origins[result[i]] = origin
		}
	}
	layers := copyEditorPage(e.pages[page]).Content.Layer
	target := 0
	if e.originalPage(page) {
		target = len(e.source.pages[e.pages[page].ID].original.Content.Layer)
	}
	if target == len(layers) {
		layers = append(layers, Layer{ID: e.nextID(), Type: "Body"})
	}
	layers[target].Objects = append(layers[target].Objects, prepared...)
	e.replaceLayers(page, layers)
	return result, nil
}

// ObjectPosition 对象在图层或页块直接成员中的位置，Index从0开始，Count不含嵌套页块的对象
type ObjectPosition struct {
	Layer     string
	Container string
	Index     int
	Count     int
}

// ObjectPosition 获取对象的直接容器及排序位置
// 入参: page 页面索引, id 对象标识
// 返回: ObjectPosition 对象位置, error 错误信息
func (e *Editor) ObjectPosition(page int, id string) (ObjectPosition, error) {
	layer, _, err := e.findObject(page, id)
	if err != nil {
		return ObjectPosition{}, err
	}
	parent, indexes := e.objectOrderIndexes(page, layer, id)
	position := ObjectPosition{Layer: layer.ID, Container: layer.ID, Count: len(indexes)}
	if parent != nil {
		position.Container = parent.attr("ID")
	}
	for i, index := range indexes {
		if editorObjectID(layer.Objects[index]) == id {
			position.Index = i
			break
		}
	}
	return position, nil
}

// objectOrderIndexes 获取同一直接容器的对象索引，不跨越图层或页块
// 入参: page 页面索引, layer 图层, id 对象标识
// 返回: *editorXML 原始容器, []int 对象索引
func (e *Editor) objectOrderIndexes(page int, layer *Layer, id string) (*editorXML, []int) {
	var nodes map[string]*editorXML
	var parent *editorXML
	if e.originalPage(page) {
		nodes = e.source.pages[e.pages[page].ID].nodes
		if node := nodes[id]; node != nil {
			parent = node.parent
		}
	}
	var indexes []int
	for i, object := range layer.Objects {
		node := nodes[editorObjectID(object)]
		if node == nil && parent == nil || node != nil && node.parent == parent {
			indexes = append(indexes, i)
		}
	}
	return parent, indexes
}

// OrderObjects 调整同一图层或页块内选区的绘制顺序，保留各组选中及未选中对象的相对顺序
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
	layer := &e.pages[page].Content.Layer[indexes[0].layer]
	_, siblings := e.objectOrderIndexes(page, layer, ids[0])
	for i, id := range ids {
		capability, err := e.ObjectCapabilities(page, id)
		if err != nil {
			return err
		}
		if !capability.Order || indexes[i].layer != indexes[0].layer || !slices.Contains(siblings, indexes[i].index) {
			return fmt.Errorf("ordering requires editable objects in the same container")
		}
	}
	layers := copyEditorPage(e.pages[page]).Content.Layer
	objects := make([]GraphicObject, len(siblings))
	for i, index := range siblings {
		objects[i] = layer.Objects[index]
	}
	selected := make([]bool, len(objects))
	for _, index := range indexes {
		selected[slices.Index(siblings, index.index)] = true
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
	for i, index := range siblings {
		layers[indexes[0].layer].Objects[index] = objects[i]
	}
	e.replaceLayers(page, layers)
	return nil
}

// DistributeObjects 按可见范围等距分布同页对象，固定两端对象，保留绘制顺序
// 不足三个对象时不修改；允许负间距，无法保持位置顺序的重叠布局返回错误
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
		return compareEditorPosition(indexes[i], indexes[j])
	})
	first, last := order[0], order[len(order)-1]
	gap := (positions[last] + sizes[last] - positions[first] - total) / float64(len(order)-1)
	for i, index := range order[:len(order)-1] {
		step := sizes[index] + gap
		if canvas.Equal(step, 0) {
			step = 0
		}
		if step < 0 || step == 0 && compareEditorPosition(indexes[index], indexes[order[i+1]]) > 0 {
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
	return e.updateObjects(page, updates, true)
}

// replaceLayers 替换图层容器，隔离历史快照与后续的增删、排序操作
// 入参: page 页面索引, layers 新图层列表
func (e *Editor) replaceLayers(page int, layers []Layer) {
	before := copyEditorPage(e.pages[page]).Content.Layer
	if reflect.DeepEqual(before, layers) {
		return
	}
	after := copyEditorPage(PageContent{Content: Content{Layer: layers}}).Content.Layer
	apply := func(e *Editor, layers []Layer) {
		e.pages[page].Content.Layer = copyEditorPage(PageContent{Content: Content{Layer: layers}}).Content.Layer
	}
	apply(e, after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { apply(e, before) }
		change.redo = func(e *Editor) { apply(e, after) }
	}
}

// UpdateObjects 原子替换同页对象，按各对象ID定位，全部校验通过后提交一次历史记录
// 入参: page 页面索引, objects 新对象内容，ID不得重复
// 返回: error 错误信息
func (e *Editor) UpdateObjects(page int, objects []GraphicObject) error {
	return e.updateObjects(page, objects, false)
}

// updateObjects 校验内容更新或几何与外观变换，原子提交跨图层选区
// 入参: page 页面索引, objects 新对象, preserveContent 是否保留原内容
// 返回: error 错误信息
func (e *Editor) updateObjects(page int, objects []GraphicObject, preserveContent bool) error {
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
		if e.originalPage(page) {
			capability, err := e.ObjectCapabilities(page, ids[i])
			if err != nil {
				return err
			}
			if !capability.Transform || !preserveContent && !e.sourceRGB() {
				return fmt.Errorf("object %q is read-only for this operation: %w", ids[i], capability.editError())
			}
		}
		if preserveContent && e.originalPage(page) {
			if err := validateEditorGeometry(object); err != nil {
				return err
			}
			after[i] = cloneEditorData(object)
		} else {
			after[i], err = e.prepareObject(ids[i], object)
			if err != nil {
				return err
			}
			if e.originalPage(page) {
				after[i], err = e.resolveEditorStyle(after[i], e.pages[page].Content.Layer[indexes[i].layer].DrawParam)
				if err != nil {
					return err
				}
			}
		}
	}
	if reflect.DeepEqual(before, after) {
		return nil
	}
	apply := func(e *Editor, objects []GraphicObject) {
		for i, index := range indexes {
			e.pages[page].Content.Layer[index.layer].Objects[index.index] = objects[i]
		}
	}
	apply(e, after)
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { apply(e, before) }
		change.redo = func(e *Editor) { apply(e, after) }
	}
	return nil
}

// TransformObjects 同页对象以页面原点等比缩放后统一平移，保持相对位置，一次撤销恢复全部
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
		if e.originalPage(page) {
			if err := validateEditorGeometry(object); err != nil {
				return err
			}
		}
		object, err = cloneEditorObject(object)
		if err != nil {
			return err
		}
		objects[i], err = transformEditorObject(object, dx, dy, scale)
		if err != nil {
			return err
		}
		if origin := e.objectOrigin(editorObjectID(object)); origin != nil && origin.node.attr("LineWidth") == "0" && object.Type == "TextObject" && object.TextObject.LineWidth == 0 {
			objects[i].TextObject.LineWidth = 0
		}
	}
	return e.updateObjects(page, objects, true)
}

// AlignObjects 单对象对齐页面，多对象相互对齐至选区边界，文字采用实际字形范围
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
	return e.updateObjects(page, objects, true)
}

// DeleteObjects 原子删除同页对象，保留其余对象顺序，一次撤销恢复全部
// 入参: page 页面索引, ids 对象标识
// 返回: error 错误信息
func (e *Editor) DeleteObjects(page int, ids []string) error {
	_, indexes, err := e.selectedObjects(page, ids)
	if err != nil || len(indexes) == 0 {
		return err
	}
	for _, id := range ids {
		capability, err := e.ObjectCapabilities(page, id)
		if err != nil {
			return err
		}
		if !capability.Delete {
			return fmt.Errorf("object %q cannot be deleted: %w", id, capability.editError())
		}
	}
	layers := copyEditorPage(e.pages[page]).Content.Layer
	slices.SortFunc(indexes, compareEditorPosition)
	for _, index := range slices.Backward(indexes) {
		layer := &layers[index.layer]
		layer.Objects = slices.Delete(layer.Objects, index.index, index.index+1)
	}
	e.replaceLayers(page, layers)
	return nil
}

// selectedObjects 校验同页选择，返回内部只读对象与对应索引
// 入参: page 页面索引, ids 对象标识，不得重复
// 返回: []GraphicObject 所选对象, []editorObjectPosition 图层和对象索引, error 错误信息
func (e *Editor) selectedObjects(page int, ids []string) ([]GraphicObject, []editorObjectPosition, error) {
	if _, err := e.page(page); err != nil {
		return nil, nil, err
	}
	objects := make([]GraphicObject, len(ids))
	indexes := make([]editorObjectPosition, len(ids))
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
		for j := range e.pages[page].Content.Layer {
			if &e.pages[page].Content.Layer[j] == layer {
				indexes[i] = editorObjectPosition{j, index}
				break
			}
		}
		objects[i] = layer.Objects[index]
	}
	return objects, indexes, nil
}

// editorObjectID 获取可创作对象的标准标识
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
	case "CompositeObject", "CompositeGraphicUnit":
		return object.CompositeGraphicUnit.ID
	}
	return ""
}

// objectBounds 批量获取对齐范围，复用同一渲染器的资源和字形缓存
// 入参: page 页面索引, objects 待度量的对象
// 返回: []Box 对象范围, error 错误信息
func (e *Editor) objectBounds(page int, objects []GraphicObject) ([]Box, error) {
	for _, object := range objects {
		capability, err := e.ObjectCapabilities(page, editorObjectID(object))
		if err != nil {
			return nil, err
		}
		if !capability.Arrange {
			return nil, fmt.Errorf("object %q cannot be arranged: %w", editorObjectID(object), capability.editError())
		}
	}
	reader, err := e.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	renderer := NewRenderer(reader)
	boxes := make([]Box, len(objects))
	for i, object := range objects {
		boxes[i], err = renderer.ObjectBounds(object, "")
		if err != nil {
			return nil, err
		}
		if boxes[i].W <= 0 || boxes[i].H <= 0 {
			return nil, fmt.Errorf("object %q has no visible bounds", editorObjectID(object))
		}
	}
	return boxes, nil
}
