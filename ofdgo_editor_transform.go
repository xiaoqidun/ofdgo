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

// RotateObject 绕对象中心旋转，文字采用字形范围，其余对象采用Boundary。
// 入参: page 页面索引, id 对象标识, degrees 顺时针角度
// 返回: error 错误信息
func (e *Editor) RotateObject(page int, id string, degrees int) error {
	return e.RotateObjects(page, []string{id}, degrees)
}

// RotateObjects 绕同页选区中心旋转，文字采用字形范围，其余对象采用Boundary。
// 保留相对位置及资源，一次撤销恢复全部。
// 入参: page 页面索引, ids 对象标识, degrees 顺时针角度，仅支持90度的整数倍
// 返回: error 错误信息
func (e *Editor) RotateObjects(page int, ids []string, degrees int) error {
	if degrees%90 != 0 {
		return fmt.Errorf("rotation must be a multiple of 90 degrees")
	}
	turn := (degrees%360 + 360) % 360 / 90
	m := []Matrix{IdentityMatrix, {b: 1, c: -1}, {a: -1, d: -1}, {b: -1, c: 1}}[turn]
	return e.orientObjects(page, ids, m)
}

// FlipObject 绕对象中心镜像，文字采用字形范围，其余对象采用Boundary。
// 入参: page 页面索引, id 对象标识, axis 为horizontal或vertical
// 返回: error 错误信息
func (e *Editor) FlipObject(page int, id, axis string) error {
	return e.FlipObjects(page, []string{id}, axis)
}

// FlipObjects 以同页选区中心镜像，保留资源及绘制顺序，一次撤销恢复全部。
// 文字采用字形范围，其余对象采用Boundary。
// 入参: page 页面索引, ids 对象标识, axis 为horizontal或vertical
// 返回: error 错误信息
func (e *Editor) FlipObjects(page int, ids []string, axis string) error {
	m := IdentityMatrix
	switch axis {
	case "horizontal":
		m.a = -1
	case "vertical":
		m.d = -1
	default:
		return fmt.Errorf("invalid flip axis %q", axis)
	}
	return e.orientObjects(page, ids, m)
}

// orientObjects 更新Boundary与CTM，不重排文字或重采样图片。
// 入参: page 页面索引, ids 对象标识, matrix 直角旋转或镜像矩阵
// 返回: error 错误信息
func (e *Editor) orientObjects(page int, ids []string, matrix Matrix) error {
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) == 0 || matrix == IdentityMatrix {
		return err
	}
	boxes, err := e.objectBounds(page, objects)
	if err != nil {
		return err
	}
	var box Box
	for _, item := range boxes {
		box = unionTextBox(box, item)
	}
	x, y := box.X+box.W/2, box.Y+box.H/2
	matrix = TranslationMatrix(x, y).Multiply(matrix).Multiply(TranslationMatrix(-x, -y))
	for i, object := range objects {
		var boundary, ctm *string
		switch object.Type {
		case "TextObject":
			boundary, ctm = &object.TextObject.Boundary, &object.TextObject.CTM
		case "PathObject":
			boundary, ctm = &object.PathObject.Boundary, &object.PathObject.CTM
		case "ImageObject":
			boundary, ctm = &object.ImageObject.Boundary, &object.ImageObject.CTM
		}
		before, _ := ParseBox(*boundary)
		after := matrix.TransformBox(before)
		local := TranslationMatrix(-after.X, -after.Y).Multiply(matrix).Multiply(TranslationMatrix(before.X, before.Y))
		*ctm = local.Multiply(NewMatrix(*ctm)).String()
		*boundary = editorBoxString(after)
		if object.Type == "ImageObject" {
			object.ImageObject.Clips = transformImageClips(object.ImageObject.Clips, local)
		}
		objects[i] = object
	}
	return e.UpdateObjects(page, objects)
}

// transformImageClips 同步变换不随对象CTM变化的裁剪区域，不修改原始裁剪数据。
// 入参: clips 图片裁剪集合, matrix 对象边界坐标中的变换
// 返回: *Clips 变换后的裁剪集合
func transformImageClips(clips *Clips, matrix Matrix) *Clips {
	if clips == nil || clips.TransFlag == nil || *clips.TransFlag {
		return clips
	}
	result := *clips
	result.Clip = slices.Clone(clips.Clip)
	for i := range result.Clip {
		result.Clip[i].Area = slices.Clone(clips.Clip[i].Area)
		for j := range result.Clip[i].Area {
			area := &result.Clip[i].Area[j]
			area.CTM = matrix.Multiply(NewMatrix(area.CTM)).String()
		}
	}
	return &result
}

// ImageBounds 返回完整图片经Boundary位移及CTM变换后的轴对齐边界，不应用裁剪或父级变换。
// 返回: Box 所在坐标系中的完整图片边界, error 错误信息
func (obj ImageObject) ImageBounds() (Box, error) {
	box, err := creationBox(obj.Boundary)
	if err != nil {
		return Box{}, err
	}
	if obj.CTM == "" {
		return box, nil
	}
	if _, err := creationNumbers(obj.CTM, 6); err != nil {
		return Box{}, err
	}
	return TranslationMatrix(box.X, box.Y).Multiply(NewMatrix(obj.CTM)).TransformBox(Box{W: 1, H: 1}), nil
}

// CropImage 按页面毫米坐标重设图片裁剪，保留原始资源及像素，可再次扩大裁剪区域。
// 使用ImageObject.ImageBounds返回的范围可还原完整图片；提交一次撤销记录。
// 入参: page 页面索引, id 图片对象标识, box 页面毫米坐标中的保留范围
// 返回: error 错误信息
func (e *Editor) CropImage(page int, id string, box Box) error {
	object, err := e.Object(page, id)
	if err != nil {
		return err
	}
	if object.Type != "ImageObject" {
		return fmt.Errorf("object %q is not an image", id)
	}
	image := &object.ImageObject
	if err := cropImageObject(image, box); err != nil {
		return err
	}
	return e.UpdateObject(page, id, object)
}

// cropImageObject 更新图片副本的裁剪和局部坐标，不修改资源。
// 入参: image 图片对象副本, box 页面毫米坐标中的保留范围
// 返回: error 错误信息
func cropImageObject(image *ImageObject, box Box) error {
	full, err := image.ImageBounds()
	if err != nil {
		return err
	}
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return err
	}
	if box.X < full.X-1e-9 || box.Y < full.Y-1e-9 || box.X+box.W > full.X+full.W+1e-9 || box.Y+box.H > full.Y+full.H+1e-9 {
		return fmt.Errorf("crop must stay within the image bounds")
	}
	before, _ := ParseBox(image.Boundary)
	if box.X != before.X || box.Y != before.Y {
		image.CTM = TranslationMatrix(before.X-box.X, before.Y-box.Y).Multiply(NewMatrix(image.CTM)).String()
	}
	image.Boundary = editorBoxString(box)
	image.Clips = nil
	if box != full {
		inverse, ok := NewMatrix(image.CTM).Invert()
		if !ok {
			return fmt.Errorf("image transform is not invertible")
		}
		path, err := NewShape(ShapeRectangle, Box{W: box.W, H: box.H})
		if err != nil {
			return err
		}
		fill, stroke := true, false
		path.Fill, path.Stroke = &fill, &stroke
		image.Clips = &Clips{Clip: []Clip{{Area: []ClipArea{{CTM: inverse.String(), Path: []PathObject{path}}}}}}
	}
	return nil
}

// FitImage 按原始像素比例居中适应或填充当前Boundary，保留直角旋转及镜像方向。
// 保留原始图片，不重采样；重设裁剪，一次撤销恢复原布局。
// 入参: page 页面索引, id 图片对象标识, mode 为contain（完整显示）或cover（填满裁剪）
// 返回: error 错误信息
func (e *Editor) FitImage(page int, id, mode string) error {
	object, err := e.Object(page, id)
	if err != nil {
		return err
	}
	if object.Type != "ImageObject" {
		return fmt.Errorf("object %q is not an image", id)
	}
	if err := e.fitImage(&object.ImageObject, mode); err != nil {
		return err
	}
	return e.UpdateObject(page, id, object)
}

// fitImage 将原始像素比例应用到当前图片框，适应时保留空白，填充时裁掉超出部分。
// 入参: obj 图片对象副本, mode 为contain或cover
// 返回: error 错误信息
func (e *Editor) fitImage(obj *ImageObject, mode string) error {
	if mode != "contain" && mode != "cover" {
		return fmt.Errorf("invalid image fit %q", mode)
	}
	box, err := creationBox(obj.Boundary)
	if err != nil {
		return err
	}
	m := NewMatrix(obj.CTM)
	if !axisAlignedMatrix(m) {
		return fmt.Errorf("image fitting requires an axis-aligned transform")
	}
	size := e.images[obj.ResourceID]
	w, h := float64(size.X), float64(size.Y)
	if m.a == 0 {
		w, h = h, w
	}
	scale := math.Min(box.W/w, box.H/h)
	if mode == "cover" {
		scale = math.Max(box.W/w, box.H/h)
	}
	if m.a == 0 {
		m.b, m.c = math.Copysign(float64(size.X)*scale, m.b), math.Copysign(float64(size.Y)*scale, m.c)
	} else {
		m.a, m.d = math.Copysign(w*scale, m.a), math.Copysign(h*scale, m.d)
	}
	m.e, m.f = 0, 0
	full := m.TransformBox(Box{W: 1, H: 1})
	m.e, m.f = (box.W-full.W)/2-full.X, (box.H-full.H)/2-full.Y
	obj.CTM, obj.Clips = m.String(), nil
	if mode == "cover" {
		return cropImageObject(obj, box)
	}
	return nil
}

// TextFrame 将对象Boundary逆变换到本地坐标并返回轴对齐边界。
// 直角旋转与翻转不改变本地段落宽度。
// 返回: Box 对象本地坐标中的边界, error 错误信息
func (obj TextObject) TextFrame() (Box, error) {
	box, err := creationBox(obj.Boundary)
	if err != nil {
		return Box{}, err
	}
	if obj.CTM != "" {
		if _, err := creationNumbers(obj.CTM, 6); err != nil {
			return Box{}, err
		}
	}
	m, ok := NewMatrix(obj.CTM).Invert()
	if !ok {
		return Box{}, fmt.Errorf("text transform is not invertible")
	}
	return m.TransformBox(Box{W: box.W, H: box.H}), nil
}

// ResizeTextFrame 调整文字本地坐标的左侧偏移及宽度，保留页面方向；重新排版后通过UpdateObject提交。
// 支持轴向缩放、直角旋转与镜像，不支持斜切或其他角度旋转。
// 入参: offset 本地左侧位移, width 新的本地排版宽度，单位为毫米
// 返回: TextObject 调整后的文字对象, error 错误信息
func (obj TextObject) ResizeTextFrame(offset, width float64) (TextObject, error) {
	frame, err := obj.TextFrame()
	if err != nil {
		return TextObject{}, err
	}
	if !axisAlignedMatrix(NewMatrix(obj.CTM)) {
		return TextObject{}, fmt.Errorf("text frame resizing requires an axis-aligned transform")
	}
	if !finite(offset) || !finite(width) || width <= 0 {
		return TextObject{}, fmt.Errorf("text frame requires a finite offset and a positive width")
	}
	boundary, _ := ParseBox(obj.Boundary)
	m := TranslationMatrix(boundary.X, boundary.Y).Multiply(NewMatrix(obj.CTM))
	frame.X += offset
	frame.W = width
	box := m.TransformBox(frame)
	obj.CTM = TranslationMatrix(-box.X, -box.Y).Multiply(m).Multiply(TranslationMatrix(offset, 0)).String()
	obj.Boundary = editorBoxString(box)
	return obj, nil
}

// editorBoxString 使用统一精度序列化创作边界。
// 入参: box 矩形范围
// 返回: string 标准Boundary属性值
func editorBoxString(box Box) string {
	return fmt.Sprintf("%s %s %s %s", ofdNumber(box.X), ofdNumber(box.Y), ofdNumber(box.W), ofdNumber(box.H))
}
