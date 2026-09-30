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
	"slices"
)

// SelectionBounds 获取正文与注解混合选区的实际范围，复用同一次度量的资源缓存
// 入参: page 页面索引, objects 正文标识, annotations 注解标识，各列表不得重复
// 返回: Box 页面毫米范围, error 标识或度量错误
func (e *Editor) SelectionBounds(page int, objects, annotations []string) (Box, error) {
	boxes, _, _, err := e.selectionGeometry(page, objects, annotations)
	if err != nil {
		return Box{}, err
	}
	var bounds Box
	for _, box := range boxes {
		bounds = unionTextBox(bounds, box)
	}
	if bounds.W <= 0 || bounds.H <= 0 {
		return Box{}, fmt.Errorf("selection has no visible bounds")
	}
	return bounds, nil
}

// AlignSelection 将正文与注解统一对齐，单选对齐页面，多选对齐选区，一次撤销恢复全部
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, alignment 为left、center、right、top、middle或bottom
// 返回: error 标识、度量或编辑错误
func (e *Editor) AlignSelection(page int, objects, annotations []string, alignment string) error {
	if !slices.Contains([]string{"left", "center", "right", "top", "middle", "bottom"}, alignment) {
		return fmt.Errorf("invalid alignment %q", alignment)
	}
	boxes, _, target, err := e.selectionGeometry(page, objects, annotations)
	if err != nil || len(boxes) == 0 {
		return err
	}
	if len(boxes) > 1 {
		target = Box{}
		for _, box := range boxes {
			target = unionTextBox(target, box)
		}
	}
	matrices := make([]Matrix, len(boxes))
	for i, box := range boxes {
		matrices[i] = editorAlignment(box, target, alignment)
	}
	return e.transformSelection(page, objects, annotations, matrices)
}

// DistributeSelection 按正文与注解实际范围等距分布，固定两端并校验位置顺序，一次撤销恢复全部
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, axis 为horizontal或vertical
// 返回: error 标识、度量、重叠或编辑错误
func (e *Editor) DistributeSelection(page int, objects, annotations []string, axis string) error {
	if axis != "horizontal" && axis != "vertical" {
		return fmt.Errorf("invalid distribution axis %q", axis)
	}
	boxes, positions, _, err := e.selectionGeometry(page, objects, annotations)
	if err != nil || len(boxes) < 3 {
		return err
	}
	matrices, err := editorDistribution(boxes, positions, axis)
	if err != nil {
		return err
	}
	return e.transformSelection(page, objects, annotations, matrices)
}

// selectionGeometry 一次读取页面与资源，按正文、注解标识顺序度量并保留绘制位置
// 入参: page 页面索引, objects 正文标识, annotations 注解标识
// 返回: []Box 成员范围, []editorObjectPosition 绘制位置, Box 页面范围, error 标识或度量错误
func (e *Editor) selectionGeometry(page int, objects, annotations []string) ([]Box, []editorObjectPosition, Box, error) {
	items, positions, err := e.selectedObjects(page, objects)
	if err != nil {
		return nil, nil, Box{}, err
	}
	reader, err := e.Reader()
	if err != nil {
		return nil, nil, Box{}, err
	}
	defer reader.Close()
	content, err := reader.PageContentByIndex(page)
	if err != nil {
		return nil, nil, Box{}, err
	}
	renderer := e.newRenderer(reader)
	paper, err := renderer.GetPageBox(content)
	if err != nil {
		return nil, nil, Box{}, err
	}
	boxes := make([]Box, 0, len(items)+len(annotations))
	for i, item := range items {
		box, err := renderer.ObjectBounds(item, e.pages[page].Content.Layer[positions[i].layer].DrawParam)
		if err != nil {
			return nil, nil, Box{}, err
		}
		boxes = append(boxes, box)
	}
	list := reader.Annots[content.ID]
	lookup := make(map[string]int, len(list))
	for i, annotation := range list {
		lookup[annotation.ID] = i
	}
	seen := make(map[string]bool, len(annotations))
	for _, id := range annotations {
		index, found := lookup[id]
		if seen[id] || !found {
			return nil, nil, Box{}, fmt.Errorf("duplicate or missing annotation %q", id)
		}
		seen[id] = true
		box, _, err := renderer.AnnotationGeometry(list[index])
		if err != nil {
			return nil, nil, Box{}, err
		}
		if box.W <= 0 || box.H <= 0 {
			box, err = ParseBox(list[index].Appearance.Boundary)
			if err != nil {
				return nil, nil, Box{}, err
			}
		}
		boxes = append(boxes, box)
		positions = append(positions, editorObjectPosition{layer: len(content.Content.Layer), index: index})
	}
	for _, box := range boxes {
		if !finite(box.X) || !finite(box.Y) || !finite(box.W) || !finite(box.H) || box.W <= 0 || box.H <= 0 {
			return nil, nil, Box{}, fmt.Errorf("selection member has no visible bounds")
		}
	}
	return boxes, positions, paper, nil
}

// transformSelection 原子应用成员平移，跳过未改变的成员及历史记录
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, matrices 按正文、注解排列的平移
// 返回: error 编辑错误
func (e *Editor) transformSelection(page int, objects, annotations []string, matrices []Matrix) error {
	return e.Transaction(func(edit *Editor) error {
		items, _, err := edit.selectedObjects(page, objects)
		if err != nil {
			return err
		}
		changes := make(map[string]Matrix, len(objects))
		var updates []GraphicObject
		for i, id := range objects {
			if matrices[i] != IdentityMatrix {
				changes[id] = matrices[i]
				updates = append(updates, items[i])
			}
		}
		if len(updates) != 0 {
			if err := edit.transformObjects(page, updates, func(object GraphicObject) (GraphicObject, error) {
				return edit.transformMatrix(object, changes[editorObjectID(object)])
			}); err != nil {
				return err
			}
		}
		for i, id := range annotations {
			matrix := matrices[len(objects)+i]
			if matrix != IdentityMatrix {
				if err := edit.MoveAnnotations(page, []string{id}, matrix.e, matrix.f); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
