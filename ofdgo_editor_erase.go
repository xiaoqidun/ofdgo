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
	"reflect"
	"slices"
	"strings"

	"github.com/tdewolff/canvas"
)

// EraseObjects 擦除同页对象与矩形区域重叠的可见部分，一次撤销恢复全部
// 全部覆盖时删除对象；局部覆盖使用OFD路径裁剪，保留原文字和资源，不用于敏感信息脱敏
// 不改变未相交对象，无法保真编辑时返回错误且不提交修改
// 入参: page 页面索引, ids 对象标识, box 页面毫米坐标中的擦除范围
// 返回: error 错误信息
func (e *Editor) EraseObjects(page int, ids []string, box Box) error {
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return err
	}
	return e.eraseObjects(page, ids, box, nil, nil)
}

// EraseObjectsPath 擦除同页对象与闭合折线区域重叠的可见部分，末点自动连接起点
// 自交区域使用奇偶规则；复用矩形擦除的裁剪、原子修改和撤销逻辑，不用于敏感信息脱敏
// 入参: page 页面索引, ids 对象标识, points 页面毫米坐标，至少三个点
// 返回: error 错误信息
func (e *Editor) EraseObjectsPath(page int, ids []string, points []Point) error {
	if len(points) < 3 {
		return fmt.Errorf("erase path requires at least three points")
	}
	region := &canvas.Path{}
	for i, point := range points {
		if !finite(point.X) || !finite(point.Y) {
			return fmt.Errorf("invalid erase path point %d", i)
		}
		if i == 0 {
			region.MoveTo(point.X, point.Y)
		} else {
			region.LineTo(point.X, point.Y)
		}
	}
	region.Close()
	region = region.Settle(canvas.EvenOdd)
	if region.Empty() {
		return nil
	}
	bounds := region.FastBounds()
	return e.eraseObjects(page, ids, Box{X: bounds.X0, Y: bounds.Y0, W: bounds.W(), H: bounds.H()}, points, region)
}

// eraseObjects 按矩形或闭合路径裁剪对象，只在全部对象处理成功后提交修改
// 入参: page 页面索引, ids 对象标识, box 区域边界, points 折线点, region 奇偶填充后的路径
// 返回: error 错误信息
func (e *Editor) eraseObjects(page int, ids []string, box Box, points []Point, region *canvas.Path) error {
	objects, indexes, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) == 0 {
		return err
	}
	boxes, err := e.objectBounds(page, objects)
	if err != nil {
		return err
	}
	layers := copyEditorPage(e.pages[page]).Content.Layer
	removed := make(map[string]bool)
	changed := false
	for i, object := range objects {
		bounds := boxes[i]
		x, y := math.Max(box.X, bounds.X), math.Max(box.Y, bounds.Y)
		right, bottom := math.Min(box.X+box.W, bounds.X+bounds.W), math.Min(box.Y+box.H, bounds.Y+bounds.H)
		if right <= x || bottom <= y {
			continue
		}
		covered := x == bounds.X && y == bounds.Y && right == bounds.X+bounds.W && bottom == bounds.Y+bounds.H
		if region != nil {
			path := canvas.Rectangle(bounds.W, bounds.H).Translate(bounds.X, bounds.Y)
			if path.And(region).Empty() {
				continue
			}
			covered = path.Not(region).Empty()
		}
		if covered {
			removed[ids[i]] = true
			changed = true
			continue
		}
		object, err := cloneEditorObject(object)
		if err != nil {
			return err
		}
		boundary, ctm := editorGeometry(object)
		frame, _ := ParseBox(boundary)
		outer := unionTextBox(bounds, frame)
		outer.X, outer.Y, outer.W, outer.H = outer.X-1, outer.Y-1, outer.W+2, outer.H+2
		x, y = math.Max(box.X, outer.X), math.Max(box.Y, outer.Y)
		right, bottom = math.Min(box.X+box.W, outer.X+outer.W), math.Min(box.Y+box.H, outer.Y+outer.H)
		clips := editorObjectClips(&object)
		if *clips == nil {
			*clips = &Clips{}
		}
		matrix := IdentityMatrix
		if (*clips).TransFlag == nil || *(*clips).TransFlag {
			if object.Type == "ImageObject" && ctm == "" {
				ctm = Matrix{a: frame.W, d: frame.H}.String()
			}
			var ok bool
			matrix, ok = NewMatrix(ctm).Invert()
			if !ok {
				return fmt.Errorf("object %q transform is not invertible", ids[i])
			}
		}
		clip := Clip{}
		areas := []Box{
			{X: outer.X, Y: outer.Y, W: outer.W, H: y - outer.Y},
			{X: outer.X, Y: bottom, W: outer.W, H: outer.Y + outer.H - bottom},
			{X: outer.X, Y: y, W: x - outer.X, H: bottom - y},
			{X: right, Y: y, W: outer.X + outer.W - right, H: bottom - y},
		}
		if region != nil {
			areas = []Box{unionTextBox(outer, box)}
		}
		for _, area := range areas {
			if area.W <= 0 || area.H <= 0 {
				continue
			}
			area.X, area.Y = area.X-frame.X, area.Y-frame.Y
			path, err := NewShape(ShapeRectangle, area)
			if err != nil {
				return err
			}
			fill, stroke := true, false
			path.Fill, path.Stroke = &fill, &stroke
			if region != nil {
				var data strings.Builder
				data.WriteString(path.AbbreviatedData)
				for i, point := range points {
					command := "L"
					if i == 0 {
						command = "M"
					}
					fmt.Fprintf(&data, " %s %g %g", command, point.X-frame.X-area.X, point.Y-frame.Y-area.Y)
				}
				data.WriteString(" C")
				path.AbbreviatedData, path.Rule = data.String(), "Even-Odd"
			}
			clip.Area = append(clip.Area, ClipArea{CTM: matrix.String(), Path: []PathObject{path}})
		}
		if slices.ContainsFunc((*clips).Clip, func(previous Clip) bool { return reflect.DeepEqual(previous, clip) }) {
			continue
		}
		(*clips).Clip = append((*clips).Clip, clip)
		changed = true
		position := indexes[i]
		layers[position.layer].Objects[position.index] = object
	}
	if changed {
		for i := range layers {
			objects := layers[i].Objects[:0]
			for _, object := range layers[i].Objects {
				if !removed[editorObjectID(object)] {
					objects = append(objects, object)
				}
			}
			layers[i].Objects = objects
		}
		e.replaceLayers(page, layers)
	}
	return nil
}

// editorObjectClips 获取基本对象的裁剪字段
// 入参: object 文字、路径或图片对象
// 返回: **Clips 裁剪字段地址，不支持的对象返回nil
func editorObjectClips(object *GraphicObject) **Clips {
	switch object.Type {
	case "TextObject":
		return &object.TextObject.Clips
	case "PathObject":
		return &object.PathObject.Clips
	case "ImageObject":
		return &object.ImageObject.Clips
	}
	return nil
}
