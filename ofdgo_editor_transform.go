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
	"bytes"
	"encoding/xml"
	"fmt"
	"math"
	"slices"
)

// TransformObjectsMatrix 对同页对象施加可逆页面变换，保留原文、资源与裁剪
// 入参: page 页面索引, ids 对象标识, matrix 页面变换
// 返回: error 错误信息
func (e *Editor) TransformObjectsMatrix(page int, ids []string, matrix Matrix) error {
	if _, ok := matrix.Invert(); !ok || !finite(matrix.a) || !finite(matrix.b) || !finite(matrix.c) || !finite(matrix.d) || !finite(matrix.e) || !finite(matrix.f) {
		return fmt.Errorf("object transform must be finite and invertible")
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) == 0 {
		return err
	}
	return e.transformObjects(page, objects, func(object GraphicObject) (GraphicObject, error) {
		return e.transformMatrix(object, matrix)
	})
}

// RotateObject 绕对象可见范围中心旋转
// 入参: page 页面索引, id 对象标识, degrees 顺时针角度
// 返回: error 错误信息
func (e *Editor) RotateObject(page int, id string, degrees int) error {
	return e.RotateObjects(page, []string{id}, degrees)
}

// RotateObjects 绕同页选区的可见范围中心旋转
// 保留相对位置及资源，一次撤销恢复全部
// 带边框图片转为保留原内容的复合对象，标识不变
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

// FlipObject 绕对象可见范围中心镜像
// 入参: page 页面索引, id 对象标识, axis 为horizontal或vertical
// 返回: error 错误信息
func (e *Editor) FlipObject(page int, id, axis string) error {
	return e.FlipObjects(page, []string{id}, axis)
}

// FlipObjects 以同页选区中心镜像，保留资源及绘制顺序，一次撤销恢复全部
// 以对象可见范围计算选区
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

// ResizeObjects 按选区可见范围计算目标矩形的变换，图片及纯图片资源支持独立宽高，描边遵循OFD变换规则
// 不重排文字或重采样图片，一次撤销恢复全部；基本图形独立调整宽高可使用PathObject.Reshape
// 入参: page 页面索引, ids 对象标识, box 页面毫米坐标中的目标范围
// 返回: error 错误信息
func (e *Editor) ResizeObjects(page int, ids []string, box Box) error {
	if _, err := creationBox(editorBoxString(box)); err != nil {
		return err
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil || len(objects) == 0 {
		return err
	}
	boxes, err := e.objectBounds(page, objects)
	if err != nil {
		return err
	}
	var before Box
	for _, bounds := range boxes {
		before = unionTextBox(before, bounds)
	}
	if before.W <= 0 || before.H <= 0 {
		return fmt.Errorf("selection has no visible bounds")
	}
	sx, sy := box.W/before.W, box.H/before.H
	uniform := math.Abs(sx-sy) <= 1e-9*math.Max(sx, sy)
	matrix := Matrix{a: sx, d: sy, e: box.X - before.X*sx, f: box.Y - before.Y*sy}
	return e.transformObjects(page, objects, func(object GraphicObject) (GraphicObject, error) {
		if uniform {
			return e.transformObject(object, matrix.e, matrix.f, sx)
		} else if e.objectStretchable(object, make(map[string]bool)) {
			return e.transformMatrix(object, matrix)
		}
		return GraphicObject{}, fmt.Errorf("nonuniform resizing requires images or image-only groups")
	})
}

// transformObject 对原有复杂对象仅更新变换，不重建其文字、画刷或其他局部属性
// 入参: object 对象, dx、dy 页面位移, scale 缩放比例
// 返回: GraphicObject 新对象, error 错误信息
func (e *Editor) transformObject(object GraphicObject, dx, dy, scale float64) (GraphicObject, error) {
	if object.Type == "ImageObject" && object.ImageObject.Border != nil && scale != 1 {
		return e.transformMatrix(object, Matrix{a: scale, d: scale, e: dx, f: dy})
	}
	if origin := e.objectOrigin(editorObjectID(object)); origin != nil && (!editorXMLSupported(origin.node) || origin.reason != nil) {
		return transformEditorMatrix(object, Matrix{a: scale, d: scale, e: dx, f: dy}), nil
	}
	return transformEditorObject(object, dx, dy, scale)
}

// orientObjects 更新Boundary与CTM，不重排文字或重采样图片
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
	return e.transformObjects(page, objects, func(object GraphicObject) (GraphicObject, error) {
		return e.transformMatrix(object, matrix)
	})
}

// transformObjects 原子变换对象副本，失败不保留新复合资源及其标识
// 入参: page 页面索引, objects 选区快照, transform 单对象变换
// 返回: error 错误信息
func (e *Editor) transformObjects(page int, objects []GraphicObject, transform func(GraphicObject) (GraphicObject, error)) (err error) {
	count, maximum := len(e.resources), e.maxID
	idsReady := e.source != nil && e.source.idsReady
	defer func() {
		if err != nil {
			clear(e.resources[count:])
			e.resources, e.maxID = e.resources[:count], maximum
			if e.source != nil {
				e.source.idsReady = idsReady
			}
		}
	}()
	after := make([]GraphicObject, len(objects))
	for i, object := range objects {
		object, err = cloneEditorObject(object)
		if err != nil {
			return err
		}
		after[i], err = transform(object)
		if err != nil {
			return err
		}
	}
	return e.updateObjects(page, after, true)
}

// transformMatrix 将带边框图片封装为标准复合资源后整体变换，不改变原边框及裁剪语义
// 入参: object 对象, matrix 页面变换
// 返回: GraphicObject 变换后的对象, error 错误信息
func (e *Editor) transformMatrix(object GraphicObject, matrix Matrix) (GraphicObject, error) {
	if object.Type != "ImageObject" || object.ImageObject.Border == nil {
		return transformEditorMatrix(object, matrix), nil
	}
	origin := e.objectOrigin(object.ImageObject.ID)
	if origin == nil {
		return GraphicObject{}, fmt.Errorf("image source is unavailable")
	}
	reader, err := e.Reader()
	if err != nil {
		return GraphicObject{}, err
	}
	defer reader.Close()
	return e.transformBorderedImage(object, matrix, origin, NewRenderer(reader, WithFontFS(e.fontFS...)))
}

// objectStretchable 允许图片及纯图片资源独立调整宽高，内联组合仍保持比例
// 入参: object 对象, visiting 当前资源链
// 返回: bool 是否支持独立宽高
func (e *Editor) objectStretchable(object GraphicObject, visiting map[string]bool) bool {
	if object.Type != "ImageObject" && (object.CompositeGraphicUnit.ResourceID == "" || len(object.CompositeGraphicUnit.Objects) != 0) {
		return false
	}
	return e.objectImagesOnly(object, visiting)
}

// objectImagesOnly 检查资源链中是否只有图片，保留原边框与裁剪语义
// 入参: object 对象, visiting 当前资源链
// 返回: bool 是否只包含图片
func (e *Editor) objectImagesOnly(object GraphicObject, visiting map[string]bool) bool {
	if object.Type == "ImageObject" {
		return object.ImageObject.Border == nil || len(object.ImageObject.Actions) == 0
	}
	if object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
		return false
	}
	group := object.CompositeGraphicUnit
	found := len(group.Objects) != 0
	if id := group.ResourceID; id != "" {
		if visiting[id] {
			return false
		}
		visiting[id] = true
		resource, err := e.compositeDefinition(id)
		if err != nil || !e.objectImagesOnly(resource.object, visiting) {
			return false
		}
		delete(visiting, id)
		found = true
	}
	for _, child := range group.Objects {
		if !e.objectImagesOnly(child, visiting) {
			return false
		}
	}
	return found
}

// transformBorderedImage 将原文图片封装为复合资源，保留边框和裁剪
// 入参: object 图片对象, matrix 变换, origin 原文, renderer 资源度量器
// 返回: GraphicObject 变换结果, error 错误信息
func (e *Editor) transformBorderedImage(object GraphicObject, matrix Matrix, origin *editorObjectOrigin, renderer *Renderer) (GraphicObject, error) {
	if err := e.prepareSourceIDs(); err != nil {
		return GraphicObject{}, err
	}
	box, err := creationBox(object.ImageObject.Boundary)
	if err != nil {
		return GraphicObject{}, err
	}
	visible, err := renderer.ObjectBounds(object, "")
	if err != nil {
		return GraphicObject{}, err
	}
	extent := unionTextBox(box, visible)
	child := cloneEditorData(object)
	child.ImageObject.ID = e.nextID()
	child.ImageObject.Boundary = editorBoxString(Box{X: box.X - extent.X, Y: box.Y - extent.Y, W: box.W, H: box.H})
	data, err := editorXMLObject(origin.data, origin.node, origin.object, child)
	if err != nil {
		return GraphicObject{}, err
	}
	data, err = editorXMLStandalone(data, origin.node)
	if err != nil {
		return GraphicObject{}, err
	}
	content, err := editorXMLContainer("Content", nil, data)
	if err != nil {
		return GraphicObject{}, err
	}
	id := e.nextID()
	attrs := ofdAttrs{{Name: xml.Name{Local: "ID"}, Value: id}, {Name: xml.Name{Local: "Width"}, Value: ofdNumber(extent.W)}, {Name: xml.Name{Local: "Height"}, Value: ofdNumber(extent.H)}}
	data, err = editorXMLContainer("CompositeGraphicUnit", attrs, bytes.TrimPrefix(content, []byte(xml.Header)))
	if err != nil {
		return GraphicObject{}, err
	}
	data, err = editorXMLContainer("CompositeGraphicUnits", nil, bytes.TrimPrefix(data, []byte(xml.Header)))
	if err != nil {
		return GraphicObject{}, err
	}
	data, err = editorXMLContainer("Res", ofdAttrs{{Name: xml.Name{Local: "BaseLoc"}, Value: "."}}, bytes.TrimPrefix(data, []byte(xml.Header)))
	if err != nil {
		return GraphicObject{}, err
	}
	resource, err := e.compositeResource(id, data)
	if err != nil {
		return GraphicObject{}, err
	}
	resource.states = map[string]editorCompositeState{child.ImageObject.ID: object.state}
	e.resources = append(e.resources, resource)
	object = GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: CompositeGraphicUnit{ID: object.ImageObject.ID, ResourceID: id, Boundary: editorBoxString(extent)}}
	return transformEditorMatrix(object, matrix), nil
}

// transformEditorMatrix 在页面坐标中变换基本对象，保留局部绘制数据和裁剪
// 入参: object 对象, matrix 页面变换
// 返回: GraphicObject 变换后的独立对象
func transformEditorMatrix(object GraphicObject, matrix Matrix) GraphicObject {
	var boundary, ctm *string
	switch object.Type {
	case "TextObject":
		boundary, ctm = &object.TextObject.Boundary, &object.TextObject.CTM
	case "PathObject":
		boundary, ctm = &object.PathObject.Boundary, &object.PathObject.CTM
	case "ImageObject":
		boundary, ctm = &object.ImageObject.Boundary, &object.ImageObject.CTM
	case "CompositeObject", "CompositeGraphicUnit":
		boundary, ctm = &object.CompositeGraphicUnit.Boundary, &object.CompositeGraphicUnit.CTM
	}
	before, _ := ParseBox(*boundary)
	if object.Type == "ImageObject" && *ctm == "" {
		*ctm = Matrix{a: before.W, d: before.H}.String()
	}
	after := matrix.TransformBox(before)
	local := TranslationMatrix(-after.X, -after.Y).Multiply(matrix).Multiply(TranslationMatrix(before.X, before.Y))
	*ctm = local.Multiply(NewMatrix(*ctm)).String()
	*boundary = editorBoxString(after)
	if clips := editorObjectClips(&object); *clips != nil && (*clips).TransFlag != nil && !*(*clips).TransFlag {
		*clips = transformObjectClips(*clips, local)
	}
	return object
}

// transformObjectClips 变换裁剪区域，不修改原始裁剪数据
// 入参: clips 裁剪集合, matrix 对象边界坐标中的变换
// 返回: *Clips 变换后的裁剪集合
func transformObjectClips(clips *Clips, matrix Matrix) *Clips {
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

// ImageBounds 返回完整图片经Boundary位移及CTM变换后的轴对齐边界，不应用裁剪或父级变换
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

// CropImage 按页面毫米坐标重设图片裁剪，保留原始资源及像素，可再次扩大裁剪区域
// 使用ImageObject.ImageBounds返回的范围可还原完整图片；提交一次撤销记录
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
	object.state.crop = nil
	return e.UpdateObject(page, id, object)
}

// cropImageObject 更新图片副本的裁剪和局部坐标，不修改资源
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
	if image.CTM == "" {
		image.CTM = Matrix{a: before.W, d: before.H}.String()
	}
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

// FitImage 按原始像素比例居中适应或填充当前Boundary，保留直角旋转及镜像方向
// 保留原始图片，不重采样；重设裁剪，一次撤销恢复原布局
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
	object.state.crop = nil
	return e.UpdateObject(page, id, object)
}

// fitImage 将原始像素比例应用到当前图片框，适应时保留空白，填充时裁掉超出部分
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
	if obj.CTM == "" {
		m = Matrix{a: box.W, d: box.H}
	}
	if !axisAlignedMatrix(m) {
		return fmt.Errorf("image fitting requires an axis-aligned transform")
	}
	size, err := e.editorImage(obj.ResourceID)
	if err != nil {
		return err
	}
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

// TextFrame 将对象Boundary逆变换到本地坐标并返回轴对齐边界
// 直角旋转与翻转不改变本地段落宽度
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

// ResizeTextFrame 调整文字本地坐标的左侧偏移及宽度，保留页面方向；重新排版后通过UpdateObject提交
// 支持轴向缩放、直角旋转与镜像，不支持斜切或其他角度旋转
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

// editorBoxString 使用统一精度序列化创作边界
// 入参: box 矩形范围
// 返回: string 标准Boundary属性值
func editorBoxString(box Box) string {
	return fmt.Sprintf("%s %s %s %s", ofdNumber(box.X), ofdNumber(box.Y), ofdNumber(box.W), ofdNumber(box.H))
}
