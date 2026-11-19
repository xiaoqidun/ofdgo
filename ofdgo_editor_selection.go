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
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
)

// ObjectSelection 保存正文、注解及组合成员的独立快照，共享当前编辑器的不可变资源
// 来源修改、删除或撤销不改变快照，不用于跨文档传输
type ObjectSelection struct {
	editor      *Editor
	document    editorDocumentIdentity
	objects     []GraphicObject
	annotations *AnnotationSelection
	parts       []editorSelectionPart
}

// TextFontStyle 标识文字引用的字体资源及显式粗斜体样式
type TextFontStyle struct {
	Font   string `json:"font"`
	Bold   bool   `json:"bold"`
	Italic bool   `json:"italic"`
}

// TextFontChange 描述一种原字体款式到目标字体款式的替换
type TextFontChange struct {
	From TextFontStyle `json:"from"`
	To   TextFontStyle `json:"to"`
}

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
	if _, ok := matrix.Invert(); !ok {
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
	if err := validateCreationBox(box); err != nil {
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

// CaptureSelection 捕获正文与注解，不修改文档或历史
// 入参: page 页面索引, objects 正文标识, annotations 注解标识，各列表不得重复
// 返回: *ObjectSelection 独立快照, error 标识或资源错误
func (e *Editor) CaptureSelection(page int, objects, annotations []string) (*ObjectSelection, error) {
	items, err := e.Objects(page, objects)
	if err != nil {
		return nil, err
	}
	selection := &ObjectSelection{editor: e, document: e.documentIdentity(), objects: items}
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
	if selection == nil || selection.editor != e || !e.documentIdentity().matches(selection.document) || !finite(dx) || !finite(dy) {
		return nil, nil, fmt.Errorf("invalid object selection or offset")
	}
	var objects, annotations []string
	err := e.Transaction(func(edit *Editor) error {
		var err error
		objects, err = edit.CopyObjects(page, selection.objects, dx, dy)
		if err == nil && selection.annotations != nil {
			annotations, err = edit.PasteAnnotations(page, selection.annotations, dx, dy)
		}
		for _, part := range selection.parts {
			if err != nil {
				return err
			}
			var ids []string
			if part.composite != nil {
				ids, err = edit.PasteCompositeSelection(page, part.composite, dx, dy)
			} else {
				ids, err = edit.CopyObjects(page, part.objects, dx, dy)
			}
			objects = append(objects, ids...)
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
	if selection == nil || selection.editor != e || !e.documentIdentity().matches(selection.document) {
		return nil, fmt.Errorf("object selection belongs to another document or editor")
	}
	if selection.annotations != nil {
		return nil, fmt.Errorf("annotations cannot be nested in a page object")
	}
	var indexes []int
	err := e.Transaction(func(edit *Editor) error {
		var err error
		indexes, err = edit.CopyObjectsToComposite(page, path, selection.objects, dx, dy)
		for _, part := range selection.parts {
			if err != nil {
				return err
			}
			var next []int
			if part.composite != nil {
				next, err = edit.PasteCompositeObjects(page, path, part.composite, dx, dy)
			} else {
				next, err = edit.CopyObjectsToComposite(page, path, part.objects, dx, dy)
			}
			indexes = append(indexes, next...)
		}
		return err
	})
	return indexes, err
}

// CaptureTargets 捕获跨容器选区，内部快照保留源变换与裁剪，源删除不影响粘贴
// 入参: page 页面索引, paths 完整对象路径
// 返回: *ObjectSelection 独立快照, error 路径或资源错误
func (e *Editor) CaptureTargets(page int, paths []ObjectPath) (*ObjectSelection, error) {
	objects, annotations, groups, err := editorTargetGroups(paths)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return e.CaptureSelection(page, objects, annotations)
	}
	selection, err := e.CaptureSelection(page, nil, annotations)
	if err != nil {
		return nil, err
	}
	content, err := e.page(page)
	if err != nil {
		return nil, err
	}
	positions := e.objectPositions(content)
	paths = slices.Clone(paths)
	slices.SortStableFunc(paths, func(a, b ObjectPath) int {
		if a.ID != b.ID {
			if a.ID == "" {
				return 1
			}
			if b.ID == "" {
				return -1
			}
			return compareEditorPosition(positions[a.ID], positions[b.ID])
		}
		if a.Annotation != b.Annotation {
			return cmp.Compare(a.Annotation, b.Annotation)
		}
		return slices.Compare(a.Children, b.Children)
	})
	for _, path := range paths {
		if len(path.Children) == 0 {
			if path.Annotation != "" {
				continue
			}
			items, err := e.Objects(page, []string{path.ID})
			if err != nil {
				return nil, err
			}
			selection.parts = append(selection.parts, editorSelectionPart{objects: items})
		} else {
			index := path.Children[len(path.Children)-1]
			path.Children = path.Children[:len(path.Children)-1]
			composite, err := e.CaptureCompositeObjects(page, path, []int{index})
			if err != nil {
				return nil, err
			}
			selection.parts = append(selection.parts, editorSelectionPart{composite: composite})
		}
	}
	return selection, nil
}

// TransformTargets 按页面坐标原子变换跨容器选区，保留组合结构
// 签章外观仅支持平移与正向缩放，不改变签名媒体
// 入参: page 页面索引, paths 完整对象路径, matrix 有限可逆变换
// 返回: error 路径、变换或编辑错误
func (e *Editor) TransformTargets(page int, paths []ObjectPath, matrix Matrix) error {
	if _, ok := matrix.Invert(); !ok {
		return fmt.Errorf("selection transform must be finite and invertible")
	}
	regular, signatures, err := splitSignatureTargets(paths)
	if err != nil {
		return err
	}
	if len(signatures) != 0 {
		return e.Transaction(func(edit *Editor) error {
			if err := edit.TransformTargets(page, regular, matrix); err != nil {
				return err
			}
			return edit.changeSignatureStamps(page, signatures, &matrix)
		})
	}
	return e.editTargets(paths, func(objects, annotations []string) error {
		return e.TransformSelection(page, objects, annotations, matrix)
	}, func(path ObjectPath, indexes []int) error {
		return e.changeCompositeObjects(page, path, indexes, func(Box) Matrix { return matrix })
	})
}

// DeleteTargets 原子删除跨容器选区，一次撤销恢复全部
// 签章路径仅移除选中的StampAnnot，原签名值及其他外观保留
// 入参: page 页面索引, paths 完整对象路径
// 返回: error 路径或编辑错误
func (e *Editor) DeleteTargets(page int, paths []ObjectPath) error {
	regular, signatures, err := splitSignatureTargets(paths)
	if err != nil {
		return err
	}
	if len(signatures) != 0 {
		return e.Transaction(func(edit *Editor) error {
			if err := edit.DeleteTargets(page, regular); err != nil {
				return err
			}
			return edit.changeSignatureStamps(page, signatures, nil)
		})
	}
	return e.editTargets(paths, func(objects, annotations []string) error {
		return e.DeleteSelection(page, objects, annotations)
	}, func(path ObjectPath, indexes []int) error {
		return e.DeleteCompositeObjects(page, path, indexes)
	})
}

// StyleTargets 原子设置跨容器选区样式，一次撤销恢复全部
// 入参: page 页面索引, paths 完整对象路径, style 样式增量
// 返回: error 路径或编辑错误
func (e *Editor) StyleTargets(page int, paths []ObjectPath, style ObjectStyle) error {
	return e.editTargets(paths, func(objects, annotations []string) error {
		return e.StyleSelection(page, objects, annotations, style)
	}, func(path ObjectPath, indexes []int) error {
		return e.StyleCompositeObjects(page, path, indexes, style)
	})
}

// EraseTargets 按页面矩形原子擦除跨容器选区，一次撤销恢复全部
// 入参: page 页面索引, paths 完整对象路径, box 页面毫米范围
// 返回: error 路径或编辑错误
func (e *Editor) EraseTargets(page int, paths []ObjectPath, box Box) error {
	return e.editTargets(paths, func(objects, annotations []string) error {
		return e.EraseSelection(page, objects, annotations, box)
	}, func(path ObjectPath, indexes []int) error {
		return e.EraseCompositeObjects(page, path, indexes, box)
	})
}

// EraseTargetsPath 按页面多边形原子擦除跨容器选区，一次撤销恢复全部
// 入参: page 页面索引, paths 完整对象路径, points 页面毫米多边形
// 返回: error 路径或编辑错误
func (e *Editor) EraseTargetsPath(page int, paths []ObjectPath, points []Point) error {
	return e.editTargets(paths, func(objects, annotations []string) error {
		return e.EraseSelectionPath(page, objects, annotations, points)
	}, func(path ObjectPath, indexes []int) error {
		return e.EraseCompositeObjectsPath(page, path, indexes, points)
	})
}

// CopyTargetsStyle 原子复制样式至跨容器选区，保留各对象内容及位置
// 入参: page 页面索引, paths 完整对象路径, source 样式来源
// 返回: error 路径或样式错误
func (e *Editor) CopyTargetsStyle(page int, paths []ObjectPath, source GraphicObject) error {
	return e.editTargets(paths, func(objects, annotations []string) error {
		if err := e.CopyStyle(page, objects, source); err != nil {
			return err
		}
		return e.editSelectionAnnotations(page, annotations, func(path ObjectPath, indexes []int) error {
			return e.CopyCompositeStyle(page, path, indexes, source)
		})
	}, func(path ObjectPath, indexes []int) error {
		return e.CopyCompositeStyle(page, path, indexes, source)
	})
}

// TargetsBounds 获取跨容器选区的共同可见范围
// 入参: page 页面索引, paths 完整对象路径
// 返回: Box 页面毫米范围, error 路径或度量错误
func (e *Editor) TargetsBounds(page int, paths []ObjectPath) (Box, error) {
	regular, signatures, err := splitSignatureTargets(paths)
	if err != nil {
		return Box{}, err
	}
	objects, annotations, groups, err := editorTargetGroups(regular)
	if err != nil {
		return Box{}, err
	}
	if len(groups) == 0 && len(signatures) == 0 {
		return e.SelectionBounds(page, objects, annotations)
	}
	boxes, _, _, err := e.targetGeometry(page, paths)
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

// RotateTargets 绕共同中心旋转跨容器选区
// 入参: page 页面索引, paths 完整对象路径, degrees 顺时针直角角度
// 返回: error 路径或变换错误
func (e *Editor) RotateTargets(page int, paths []ObjectPath, degrees int) error {
	matrix, err := editorRotation(degrees)
	if err != nil {
		return err
	}
	return e.orientTargets(page, paths, matrix)
}

// FlipTargets 绕共同中心镜像跨容器选区
// 入参: page 页面索引, paths 完整对象路径, axis 为horizontal或vertical
// 返回: error 路径或变换错误
func (e *Editor) FlipTargets(page int, paths []ObjectPath, axis string) error {
	matrix, err := editorFlip(axis)
	if err != nil {
		return err
	}
	return e.orientTargets(page, paths, matrix)
}

// ResizeTargets 按共同范围调整跨容器选区，不重排文字
// 入参: page 页面索引, paths 完整对象路径, box 页面毫米目标范围
// 返回: error 路径、范围或编辑错误
func (e *Editor) ResizeTargets(page int, paths []ObjectPath, box Box) error {
	regular, signatures, err := splitSignatureTargets(paths)
	if err != nil {
		return err
	}
	objects, annotations, groups, err := editorTargetGroups(regular)
	if err != nil {
		return err
	}
	if len(groups) == 0 && len(signatures) == 0 {
		return e.ResizeSelection(page, objects, annotations, box)
	}
	if err := validateCreationBox(box); err != nil {
		return err
	}
	boxes, stretch, _, err := e.targetGeometry(page, paths)
	if err != nil {
		return err
	}
	var bounds Box
	for _, member := range boxes {
		bounds = unionTextBox(bounds, member)
	}
	if bounds.W <= 0 || bounds.H <= 0 {
		return fmt.Errorf("selection has no visible bounds")
	}
	sx, sy := box.W/bounds.W, box.H/bounds.H
	if math.Abs(sx-sy) > 1e-9*math.Max(sx, sy) {
		if !stretch {
			return fmt.Errorf("nonuniform resizing requires images or image-only groups")
		}
	} else {
		sy = sx
	}
	return e.TransformTargets(page, paths, Matrix{a: sx, d: sy, e: box.X - bounds.X*sx, f: box.Y - bounds.Y*sy})
}

// DistributionAxes 获取选区可保持位置顺序的等间距分布方向，不修改文档或历史
// 入参: page 页面索引, paths 按绘制顺序排列的完整路径
// 返回: []string 可用的horizontal和vertical方向, error 路径或度量错误
func (e *Editor) DistributionAxes(page int, paths []ObjectPath) ([]string, error) {
	objects, annotations, groups, err := editorTargetGroups(paths)
	if err != nil {
		return nil, err
	}
	var boxes []Box
	var positions []editorObjectPosition
	if len(groups) == 0 {
		boxes, positions, _, err = e.selectionGeometry(page, objects, annotations)
	} else {
		boxes, _, _, err = e.targetGeometry(page, paths)
		positions = make([]editorObjectPosition, len(paths))
		for i := range positions {
			positions[i].index = i
		}
	}
	if err != nil {
		return nil, err
	}
	axes := []string{}
	if len(boxes) < 3 {
		return axes, nil
	}
	for _, axis := range []string{"horizontal", "vertical"} {
		if _, err := editorDistribution(boxes, positions, axis); err == nil {
			axes = append(axes, axis)
		}
	}
	return axes, nil
}

// ArrangeTargets 对齐或等距分布跨容器选区，单选对齐页面
// 入参: page 页面索引, paths 按绘制顺序排列的完整路径, distribute 是否分布, direction 对齐方向或分布轴
// 返回: error 路径、方向或编辑错误
func (e *Editor) ArrangeTargets(page int, paths []ObjectPath, distribute bool, direction string) error {
	objects, annotations, groups, err := editorTargetGroups(paths)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		if distribute {
			return e.DistributeSelection(page, objects, annotations, direction)
		}
		return e.AlignSelection(page, objects, annotations, direction)
	}
	if distribute && direction != "horizontal" && direction != "vertical" || !distribute && !slices.Contains([]string{"left", "center", "right", "top", "middle", "bottom"}, direction) {
		return fmt.Errorf("invalid arrangement direction %q", direction)
	}
	boxes, _, target, err := e.targetGeometry(page, paths)
	if err != nil {
		return err
	}
	matrices := make([]Matrix, len(paths))
	if distribute {
		positions := make([]editorObjectPosition, len(paths))
		for i := range positions {
			positions[i].index = i
		}
		matrices, err = editorDistribution(boxes, positions, direction)
		if err != nil {
			return err
		}
	} else {
		if len(paths) > 1 {
			target = Box{}
			for _, box := range boxes {
				target = unionTextBox(target, box)
			}
		}
		for i, box := range boxes {
			matrices[i] = editorAlignment(box, target, direction)
		}
	}
	return e.Transaction(func(edit *Editor) error {
		for i, matrix := range matrices {
			if matrix != IdentityMatrix {
				if err := edit.TransformTargets(page, paths[i:i+1], matrix); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// StyleTargetsText 原子修改跨容器文字样式，保留各自定位及组合结构
// 入参: page 页面索引, paths 完整文字路径, style 字体、字号或颜色增量
// 返回: error 路径、文字或样式错误
func (e *Editor) StyleTargetsText(page int, paths []ObjectPath, style TextStyle) error {
	patch := TextFormatPatch{}
	if style.Font != "" {
		patch.Font = &style.Font
	}
	if style.Size != 0 {
		patch.Size = &style.Size
	}
	if style.Color != "" {
		patch.Color = &style.Color
	}
	return e.formatTargetsText(page, paths, patch, style.Reflow)
}

// FormatTargetsText 原子修改普通文字和富文本的指定格式，保留未指定的混排属性
// 入参: page 页面索引, paths 完整文字路径, patch 格式增量
// 返回: error 路径、格式或不安全重排错误
func (e *Editor) FormatTargetsText(page int, paths []ObjectPath, patch TextFormatPatch) error {
	return e.formatTargetsText(page, paths, patch, false)
}

// StyleTargetsFonts 原子替换跨容器文字的各款式，保留混排属性及对象变换
// 目标字体须已注册，可用Transaction将AddFont与本操作合并为一条历史记录
// 入参: page 页面索引, paths 完整文字路径, changes 字体款式映射, reflow 允许显式重排
// 返回: error 字体、路径或排版错误，失败时回滚全部修改
func (e *Editor) StyleTargetsFonts(page int, paths []ObjectPath, changes []TextFontChange, reflow bool) error {
	mapping := make(map[TextFontStyle]TextFontStyle, len(changes))
	for _, change := range changes {
		if change.To.Font == "" {
			return fmt.Errorf("text requires a font")
		}
		mapping[change.From] = change.To
	}
	return e.Transaction(func(e *Editor) error {
		for _, path := range paths {
			if path.Annotation != "" {
				return ErrNotRichText
			}
			var object GraphicObject
			var err error
			if len(path.Children) == 0 {
				object, err = e.Object(page, path.ID)
			} else {
				parent := path
				parent.Children = path.Children[:len(path.Children)-1]
				var members []CompositeMember
				members, err = e.CompositeObjectsAt(page, parent, path.Children[len(path.Children)-1:])
				if err == nil {
					object = members[0].Object
				}
			}
			if err != nil {
				return err
			}
			if object.Type == "TextObject" && !reflow {
				text := object.TextObject
				from := TextFontStyle{Font: text.Font, Bold: text.Weight >= 700, Italic: text.Italic}
				to, ok := mapping[from]
				if !ok || to == from {
					continue
				}
				if to.Bold == from.Bold && to.Italic == from.Italic {
					if err := e.StyleTargetsText(page, []ObjectPath{path}, TextStyle{Font: to.Font}); err != nil {
						return err
					}
					continue
				}
			}
			value, _, _, err := e.PrepareRichText(page, path, reflow)
			if err != nil {
				return err
			}
			changed := false
			for index := range value.Runs {
				format := &value.Runs[index].Format
				from := TextFontStyle{Font: format.Font, Bold: format.Bold, Italic: format.Italic}
				if to, ok := mapping[from]; ok && to != from {
					format.Font, format.Bold, format.Italic = to.Font, to.Bold, to.Italic
					changed = true
				}
			}
			if !changed {
				continue
			}
			if reflow {
				err = e.ReflowRichText(page, path, value)
			} else {
				err = e.UpdateTargetRichText(page, path, value, 0, 0)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ReflowTargetsText 显式重排跨容器文字并应用格式增量，保留对象位置及组合结构
// 入参: page 页面索引, paths 完整文字路径, patch 格式增量
// 返回: error 路径、格式或排版错误
func (e *Editor) ReflowTargetsText(page int, paths []ObjectPath, patch TextFormatPatch) error {
	return e.formatTargetsText(page, paths, patch, true)
}

// LayoutTargetsText 原子修改跨容器文字的段落增量，保留各自宽度、变换及未指定的样式
// 定位文字显式重排为富文本，任一对象失败时回滚全部修改及新增资源
// 入参: page 页面索引, paths 完整文字路径, patch 段落增量, wrap 折行选项，nil保持原值
// 返回: error 路径、段落或排版错误
func (e *Editor) LayoutTargetsText(page int, paths []ObjectPath, patch RichTextParagraph, wrap *bool) error {
	if patch == (RichTextParagraph{}) && wrap == nil {
		return nil
	}
	apply := func(path ObjectPath) error {
		value, _, _, err := e.PrepareRichText(page, path, true)
		if err != nil {
			return err
		}
		if wrap != nil {
			value.Layout.Wrap = *wrap
		}
		if err := value.FormatParagraphs(0, value.Len(), patch); err != nil {
			return err
		}
		if strings.HasSuffix(value.Text(), "\n") {
			if err := value.FormatParagraphs(value.Len(), value.Len(), patch); err != nil {
				return err
			}
		}
		return e.ReflowRichText(page, path, value)
	}
	return e.editTargets(paths, func(ids, annotations []string) error {
		if len(annotations) != 0 {
			return ErrNotRichText
		}
		for _, id := range ids {
			if err := apply(ObjectPath{ID: id}); err != nil {
				return err
			}
		}
		return nil
	}, func(path ObjectPath, indexes []int) error {
		for _, index := range indexes {
			target := path
			target.Children = append(slices.Clone(path.Children), index)
			if err := apply(target); err != nil {
				return err
			}
		}
		return nil
	})
}

// PrepareTargets 将选区及其关联资源复制为独立单页编辑器，来源关闭后仍可使用
// 不修改来源、历史或组合结构，加密来源须显式允许明文导出
// 不包含页面模板和签名，指向其他页面的跳转随单页导出移除
// 入参: ctx 取消上下文, page 来源页, paths 完整对象路径, allowDecrypted 允许明文导出
// 返回: *Editor 独立选区，可直接导出或继续编辑, error 路径或资源错误
func (e *Editor) PrepareTargets(ctx context.Context, page int, paths []ObjectPath, allowDecrypted bool) (*Editor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.encryption != nil && !allowDecrypted {
		return nil, ErrEncryptionPolicyRequired
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no objects selected")
	}
	snapshot := e.transactionSnapshot()
	snapshot.history, snapshot.historyIndex, snapshot.historyLimit = nil, 0, 0
	snapshot.transactionChanges = nil
	for id, origin := range snapshot.origins {
		if origin != nil && origin.editor == e {
			copy := *origin
			copy.editor = &snapshot
			snapshot.origins[id] = &copy
		}
	}
	selection, err := snapshot.CaptureTargets(page, paths)
	if err != nil {
		return nil, err
	}
	content, err := snapshot.page(page)
	if err != nil {
		return nil, err
	}
	objects, annotations, err := snapshot.PasteSelection(page, selection, 0, 0)
	if err != nil {
		return nil, err
	}
	box, err := snapshot.pageAreaBox(page)
	if err != nil {
		return nil, err
	}
	packet := NewEditor()
	packet.SetRenderBackends(e.backends)
	packet.SetResources(e.runtimeResources)
	packet.SetFontDirs(e.fontDirs...)
	packet.SetFontFS(e.fontFS...)
	packet.OnWriteProgress = e.OnWriteProgress
	if _, err := packet.AddPage(box.W, box.H); err != nil {
		return nil, err
	}
	packet.pages[0].Area = content.Area
	if _, _, err := packet.importSelection(ctx, &snapshot, page, objects, annotations, 0, ObjectImportOptions{AllowDecrypted: true, OnProgress: e.OnWriteProgress}, false); err != nil {
		return nil, err
	}
	packet.history, packet.historyIndex = nil, 0
	return packet, nil
}

// WriteTargetsTo 将选区及关联资源写成独立单页OFD，不修改来源与历史
// 入参: ctx 取消上下文, writer 输出流, page 来源页, paths 完整对象路径, allowDecrypted 允许明文导出
// 返回: int64 写入字节数, error 路径、资源或写入错误
func (e *Editor) WriteTargetsTo(ctx context.Context, writer io.Writer, page int, paths []ObjectPath, allowDecrypted bool) (int64, error) {
	if err := sameOutputFile(e.sourceFile, writer); err != nil {
		return 0, err
	}
	packet, err := e.PrepareTargets(ctx, page, paths, allowDecrypted)
	if err != nil {
		return 0, err
	}
	return packet.WriteToWithOptions(ctx, writer, WriteOptions{})
}

// editorSelectionPart 保存按绘制顺序排列的顶层或内部对象快照
type editorSelectionPart struct {
	objects   []GraphicObject
	composite *CompositeSelection
}

// editorTargetGroup 保存同一父路径的直接成员，结构操作按逆序处理父路径
type editorTargetGroup struct {
	path    ObjectPath
	indexes []int
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
	reader, err := e.reader(nil)
	if err != nil {
		return nil, nil, Box{}, err
	}
	defer reader.Close()
	content, err := reader.PageContentByIndex(page)
	if err != nil {
		return nil, nil, Box{}, err
	}
	renderer := e.newRenderer(reader)
	defer renderer.bindPageResources(content)()
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
	var list []Annotation
	if len(annotations) > 0 {
		list, err = reader.pageAnnotationsContext(context.Background(), content.ID)
		if err != nil {
			return nil, nil, Box{}, err
		}
	}
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

// editTargets 原子处理跨容器选区，结构变化从后向前避免路径位移
// 入参: paths 完整对象路径, root 顶层处理, member 内部处理
// 返回: error 路径或编辑错误
func (e *Editor) editTargets(paths []ObjectPath, root func([]string, []string) error, member func(ObjectPath, []int) error) error {
	objects, annotations, groups, err := editorTargetGroups(paths)
	if err != nil {
		return err
	}
	return e.Transaction(func(*Editor) error {
		for _, group := range groups {
			if err := member(group.path, group.indexes); err != nil {
				return err
			}
		}
		if len(objects)+len(annotations) != 0 {
			return root(objects, annotations)
		}
		return nil
	})
}

// targetGeometry 度量跨容器选区，保留输入顺序及独立宽高能力
// 入参: page 页面索引, paths 完整对象路径
// 返回: []Box 对象范围, bool 可独立缩放宽高, Box 页面范围, error 度量错误
func (e *Editor) targetGeometry(page int, paths []ObjectPath) ([]Box, bool, Box, error) {
	regular, _, err := splitSignatureTargets(paths)
	if err != nil {
		return nil, false, Box{}, err
	}
	if _, _, _, err := editorTargetGroups(regular); err != nil {
		return nil, false, Box{}, err
	}
	_, _, paper, err := e.selectionGeometry(page, nil, nil)
	if err != nil {
		return nil, false, Box{}, err
	}
	boxes := make([]Box, len(paths))
	stretch := true
	for i, path := range paths {
		if path.Signature != "" {
			stamp, err := e.signatureStamp(page, path)
			if err != nil {
				return nil, false, Box{}, err
			}
			boxes[i] = stamp.Bounds()
		} else if len(path.Children) != 0 {
			index := path.Children[len(path.Children)-1]
			path.Children = path.Children[:len(path.Children)-1]
			member, err := e.CompositeObject(page, path, index)
			if err != nil {
				return nil, false, Box{}, err
			}
			boxes[i] = member.Bounds
			stretch = stretch && member.Capabilities.Stretch
		} else {
			var objects, annotations []string
			if path.Annotation != "" {
				annotations = []string{path.Annotation}
			} else {
				objects = []string{path.ID}
				capability, err := e.ObjectCapabilities(page, path.ID)
				if err != nil {
					return nil, false, Box{}, err
				}
				stretch = stretch && capability.Stretch
			}
			boxes[i], err = e.SelectionBounds(page, objects, annotations)
			if err != nil {
				return nil, false, Box{}, err
			}
		}
	}
	return boxes, stretch, paper, nil
}

// orientTargets 绕跨容器选区中心应用旋转或镜像
// 入参: page 页面索引, paths 完整对象路径, matrix 方向变换
// 返回: error 路径或变换错误
func (e *Editor) orientTargets(page int, paths []ObjectPath, matrix Matrix) error {
	bounds, err := e.TargetsBounds(page, paths)
	if err != nil {
		return err
	}
	x, y := bounds.X+bounds.W/2, bounds.Y+bounds.H/2
	return e.TransformTargets(page, paths, TranslationMatrix(x, y).Multiply(matrix).Multiply(TranslationMatrix(-x, -y)))
}

// formatTargetsText 统一文字选区格式事务，普通字体和颜色修改沿用定位保留路径
// 入参: page 页面索引, paths 完整路径, patch 格式增量, reflow 显式重排
// 返回: error 校验或编辑错误
func (e *Editor) formatTargetsText(page int, paths []ObjectPath, patch TextFormatPatch, reflow bool) error {
	style := TextStyle{Reflow: reflow}
	if patch.Font != nil {
		if strings.TrimSpace(*patch.Font) == "" {
			return fmt.Errorf("text requires a font")
		}
		style.Font = *patch.Font
	}
	if patch.Size != nil {
		if !finite(*patch.Size) || *patch.Size <= 0 {
			return fmt.Errorf("invalid text size")
		}
		style.Size = *patch.Size
	}
	if patch.Color != nil {
		style.Color = *patch.Color
		if style.Color == "" {
			style.Color = "0 0 0"
		}
	}
	decorated := patch.Bold != nil || patch.Italic != nil || patch.Underline != nil || patch.Strikeout != nil || patch.Shadow != nil || patch.Background != nil
	return e.editTargets(paths, func(ids, annotations []string) error {
		if len(annotations) != 0 {
			return fmt.Errorf("annotation is not a text object")
		}
		objects, _, err := e.selectedObjects(page, ids)
		if err != nil {
			return err
		}
		var plain []string
		for _, object := range objects {
			id := editorObjectID(object)
			if object.Type == "TextObject" && !decorated {
				plain = append(plain, id)
				continue
			}
			value, _, _, err := e.PrepareRichText(page, ObjectPath{ID: id}, reflow)
			if err != nil {
				return err
			}
			if err := value.ApplyFormat(0, value.Len(), patch); err != nil {
				return err
			}
			if err := e.updateRichTextFrame(page, id, value, 0, 0, reflow); err != nil {
				return err
			}
		}
		if len(plain) != 0 {
			return e.StyleText(page, plain, style)
		}
		return nil
	}, func(path ObjectPath, indexes []int) error {
		members, err := e.CompositeObjectsAt(page, path, indexes)
		if err != nil {
			return err
		}
		var plain []int
		for i, member := range members {
			if member.Object.Type == "TextObject" && !decorated {
				plain = append(plain, indexes[i])
				continue
			}
			if err := e.formatCompositeText(page, path, indexes[i], patch, reflow); err != nil {
				return err
			}
		}
		if len(plain) != 0 {
			return e.StyleCompositeText(page, path, plain, style)
		}
		return nil
	})
}

// editorTargetGroups 分离顶层与内部选区，拒绝重复或相互包含的路径
// 入参: paths 完整对象路径
// 返回: []string 正文标识, []string 注解标识, []editorTargetGroup 内部成员, error 路径错误
func editorTargetGroups(paths []ObjectPath) ([]string, []string, []editorTargetGroup, error) {
	var objects, annotations []string
	var groups []editorTargetGroup
	for i, path := range paths {
		if path.Signature != "" || (path.ID == "") == (path.Annotation == "") || slices.ContainsFunc(path.Children, func(index int) bool { return index < 0 }) {
			return nil, nil, nil, fmt.Errorf("invalid object path")
		}
		for _, other := range paths[:i] {
			length := min(len(path.Children), len(other.Children))
			if path.ID == other.ID && path.Annotation == other.Annotation && slices.Equal(path.Children[:length], other.Children[:length]) {
				return nil, nil, nil, fmt.Errorf("selection paths overlap")
			}
		}
		if len(path.Children) == 0 {
			if path.Annotation != "" {
				annotations = append(annotations, path.Annotation)
			} else {
				objects = append(objects, path.ID)
			}
			continue
		}
		index := path.Children[len(path.Children)-1]
		path.Children = slices.Clone(path.Children[:len(path.Children)-1])
		group := slices.IndexFunc(groups, func(group editorTargetGroup) bool {
			return path.ID == group.path.ID && path.Annotation == group.path.Annotation && slices.Equal(path.Children, group.path.Children)
		})
		if group < 0 {
			groups = append(groups, editorTargetGroup{path: path, indexes: []int{index}})
		} else {
			groups[group].indexes = append(groups[group].indexes, index)
		}
	}
	slices.SortFunc(groups, func(a, b editorTargetGroup) int {
		if order := cmp.Compare(a.path.ID, b.path.ID); order != 0 {
			return order
		}
		if order := cmp.Compare(a.path.Annotation, b.path.Annotation); order != 0 {
			return order
		}
		return -slices.Compare(a.path.Children, b.path.Children)
	})
	return objects, annotations, groups, nil
}

// splitSignatureTargets 校验并分离签章外观，普通对象沿用既有路径校验
// 入参: paths 完整对象路径
// 返回: []ObjectPath 普通路径, []ObjectPath 签章路径, error 重复或无效路径
func splitSignatureTargets(paths []ObjectPath) ([]ObjectPath, []ObjectPath, error) {
	if !slices.ContainsFunc(paths, func(path ObjectPath) bool { return path.Signature != "" }) {
		return paths, nil, nil
	}
	var regular, signatures []ObjectPath
	for _, path := range paths {
		if path.Signature == "" {
			regular = append(regular, path)
			continue
		}
		if path.ID == "" || path.Annotation != "" || len(path.Children) != 0 || slices.ContainsFunc(signatures, func(other ObjectPath) bool { return other.Signature == path.Signature && other.ID == path.ID }) {
			return nil, nil, fmt.Errorf("invalid or duplicate signature appearance path")
		}
		signatures = append(signatures, path)
	}
	_, _, _, err := editorTargetGroups(regular)
	return regular, signatures, err
}
