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
	"math"
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

// TransformSelection 原子变换正文与注解，失败回滚，一次撤销恢复全部
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, matrix 有限可逆变换
// 返回: error 标识、变换或编辑错误
func (e *Editor) TransformSelection(page int, objects, annotations []string, matrix Matrix) error {
	if _, ok := matrix.Invert(); !ok || !finite(matrix.a) || !finite(matrix.b) || !finite(matrix.c) || !finite(matrix.d) || !finite(matrix.e) || !finite(matrix.f) {
		return fmt.Errorf("selection transform must be finite and invertible")
	}
	if matrix == IdentityMatrix {
		if _, _, err := e.selectedObjects(page, objects); err != nil {
			return err
		}
		seen := make(map[string]bool, len(annotations))
		for _, id := range annotations {
			if seen[id] {
				return fmt.Errorf("duplicate annotation ID %q", id)
			}
			seen[id] = true
			if _, err := e.Annotation(page, id); err != nil {
				return err
			}
		}
		return nil
	}
	return e.Transaction(func(edit *Editor) error {
		if err := edit.TransformObjectsMatrix(page, objects, matrix); err != nil {
			return err
		}
		return edit.TransformAnnotations(page, annotations, matrix)
	})
}

// RotateSelection 绕正文与注解的共同可见范围中心旋转，保留相对位置及资源
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, degrees 顺时针角度，仅支持90度的整数倍
// 返回: error 标识、度量或编辑错误
func (e *Editor) RotateSelection(page int, objects, annotations []string, degrees int) error {
	matrix, err := editorRotation(degrees)
	if err != nil {
		return err
	}
	return e.orientSelection(page, objects, annotations, matrix)
}

// FlipSelection 绕正文与注解的共同可见范围中心镜像，保留绘制层级
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, axis 为horizontal或vertical
// 返回: error 标识、度量或编辑错误
func (e *Editor) FlipSelection(page int, objects, annotations []string, axis string) error {
	matrix, err := editorFlip(axis)
	if err != nil {
		return err
	}
	return e.orientSelection(page, objects, annotations, matrix)
}

// ResizeSelection 按共同可见范围调整正文与注解的位置及尺寸，不重排文字或重采样图片
// 正文独立宽高遵循ResizeObjects规则，一次撤销恢复全部
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, box 页面毫米目标范围
// 返回: error 标识、度量或编辑错误
func (e *Editor) ResizeSelection(page int, objects, annotations []string, box Box) error {
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return err
	}
	bounds, err := e.SelectionBounds(page, objects, annotations)
	if err != nil {
		return err
	}
	sx, sy := box.W/bounds.W, box.H/bounds.H
	if math.Abs(sx-sy) > 1e-9*math.Max(sx, sy) {
		items, _, err := e.selectedObjects(page, objects)
		if err != nil {
			return err
		}
		for _, object := range items {
			if !e.objectStretchable(object, make(map[string]bool)) {
				return fmt.Errorf("nonuniform resizing requires images or image-only groups")
			}
		}
	} else {
		sy = sx
	}
	return e.TransformSelection(page, objects, annotations, Matrix{a: sx, d: sy, e: box.X - bounds.X*sx, f: box.Y - bounds.Y*sy})
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

// DeleteSelection 原子删除正文与注解，一次撤销恢复全部
// 入参: page 页面索引, objects 正文标识, annotations 注解标识
// 返回: error 标识或编辑错误
func (e *Editor) DeleteSelection(page int, objects, annotations []string) error {
	return e.Transaction(func(edit *Editor) error {
		if err := edit.DeleteObjects(page, objects); err != nil {
			return err
		}
		return edit.DeleteAnnotations(page, annotations)
	})
}

// StyleSelection 原子设置正文与注解内部图元的样式，保留未指定属性
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, style 样式增量
// 返回: error 标识、样式或编辑错误
func (e *Editor) StyleSelection(page int, objects, annotations []string, style ObjectStyle) error {
	return e.Transaction(func(edit *Editor) error {
		if err := edit.StyleObjects(page, objects, style); err != nil {
			return err
		}
		return edit.editSelectionAnnotations(page, annotations, func(path ObjectPath, indexes []int) error {
			return edit.StyleCompositeObjects(page, path, indexes, style)
		})
	})
}

// EraseSelection 按页面矩形原子擦除正文与注解内部内容，不删除其他选区成员
// 保留原始资源，不用于敏感信息脱敏
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, box 页面毫米擦除范围
// 返回: error 标识、范围或编辑错误
func (e *Editor) EraseSelection(page int, objects, annotations []string, box Box) error {
	return e.Transaction(func(edit *Editor) error {
		if err := edit.EraseObjects(page, objects, box); err != nil {
			return err
		}
		return edit.editSelectionAnnotations(page, annotations, func(path ObjectPath, indexes []int) error {
			return edit.EraseCompositeObjects(page, path, indexes, box)
		})
	})
}

// EraseSelectionPath 按页面多边形原子擦除正文与注解内部内容，不改变未擦除内容
// 保留原始资源，不用于敏感信息脱敏
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, points 页面毫米多边形
// 返回: error 标识、多边形或编辑错误
func (e *Editor) EraseSelectionPath(page int, objects, annotations []string, points []Point) error {
	return e.Transaction(func(edit *Editor) error {
		if err := edit.EraseObjectsPath(page, objects, points); err != nil {
			return err
		}
		return edit.editSelectionAnnotations(page, annotations, func(path ObjectPath, indexes []int) error {
			return edit.EraseCompositeObjectsPath(page, path, indexes, points)
		})
	})
}

// orientSelection 将直角旋转或镜像定位到混合选区中心，恒等变换仅校验标识
// 入参: page 页面索引, objects 正文标识, annotations 注解标识, matrix 方向变换
// 返回: error 标识、度量或编辑错误
func (e *Editor) orientSelection(page int, objects, annotations []string, matrix Matrix) error {
	if matrix != IdentityMatrix {
		bounds, err := e.SelectionBounds(page, objects, annotations)
		if err != nil {
			return err
		}
		x, y := bounds.X+bounds.W/2, bounds.Y+bounds.H/2
		matrix = TranslationMatrix(x, y).Multiply(matrix).Multiply(TranslationMatrix(-x, -y))
	}
	return e.TransformSelection(page, objects, annotations, matrix)
}

// editSelectionAnnotations 对注解直接成员统一操作，重复或不存在的标识返回错误
// 入参: page 页面索引, ids 注解标识, apply 成员操作
// 返回: error 标识、度量或编辑错误
func (e *Editor) editSelectionAnnotations(page int, ids []string, apply func(ObjectPath, []int) error) error {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate annotation ID %q", id)
		}
		seen[id] = true
		path := ObjectPath{Annotation: id}
		reader, _, _, members, err := e.compositeScope(page, path)
		if err != nil {
			return err
		}
		reader.Close()
		indexes := make([]int, len(members))
		for i := range indexes {
			indexes[i] = i
		}
		if err := apply(path, indexes); err != nil {
			return err
		}
	}
	return nil
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
