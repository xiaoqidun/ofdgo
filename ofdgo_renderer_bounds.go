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

// ObjectBounds 获取文字、路径、图片或复合对象在页面坐标中的轴对齐范围，不修改对象
// 文字采用字形范围，路径包含描边与裁剪，图片采用裁剪后的几何范围，不解码像素或排除透明像素
// 底纹按填充或描边轮廓度量，不展开图案单元
// 不应用页面边界或父级变换；无可见范围时返回零值，不支持的对象类型返回错误
// 入参: object 图形对象, drawParam 图层绘制参数标识，无继承时为空
// 返回: Box 毫米坐标范围, error 错误信息
func (r *Renderer) ObjectBounds(object GraphicObject, drawParam string) (Box, error) {
	result, err := r.MeasureObject(object, MeasureOptions{Defaults: r.drawParamDefaults(drawParam, nil)})
	return result.Bounds, err
}

// ObjectContour 页面坐标中的填充轮廓，Path为SVG路径，EvenOdd表示奇偶填充
type ObjectContour struct {
	Path    string `json:"path"`
	EvenOdd bool   `json:"evenOdd,omitempty"`
}

// AnnotationGeometry 获取注解外观的实际内容轮廓，不将Appearance容器边界视为绘制内容
// 链接保留透明对象定义的交互区域，图片按几何范围度量而不解码透明像素
// 入参: annotation 注解
// 返回: Box 页面范围, []ObjectContour 命中轮廓, error 错误信息
func (r *Renderer) AnnotationGeometry(annotation Annotation) (Box, []ObjectContour, error) {
	object := GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: CompositeGraphicUnit{Boundary: annotation.Appearance.Boundary, Objects: annotation.Appearance.Objects}}
	box, contours, err := r.ObjectGeometry(object, "")
	if err != nil {
		return box, contours, err
	}
	for _, source := range r.annotationActionSources([]Annotation{annotation}) {
		for _, action := range source.Actions {
			if action.Event != "CLICK" {
				continue
			}
			region, path := actionLinkRegion(source, action)
			if region.W <= 0 || region.H <= 0 {
				continue
			}
			if path == "" {
				path = fmt.Sprintf("M%g %gH%gV%gH%gZ", region.X, region.Y, region.X+region.W, region.Y+region.H, region.X)
			}
			box = unionTextBox(box, region)
			contours = append(contours, ObjectContour{Path: path})
		}
	}
	return box, contours, nil
}

// ObjectContours 获取路径、图片或复合对象的实际绘制区域，包含描边、虚线、变换及对象裁剪
// 图片不解码像素，底纹不展开图案单元；不包含页面边界或父级变换
// 入参: object 路径、图片或复合对象, drawParam 图层绘制参数标识
// 返回: []ObjectContour 可用于点选的轮廓, error 错误信息
func (r *Renderer) ObjectContours(object GraphicObject, drawParam string) ([]ObjectContour, error) {
	_, contours, err := r.ObjectGeometry(object, drawParam)
	return contours, err
}

// ObjectGeometry 一次度量路径、图片或复合对象的范围与轮廓，语义与ObjectBounds和ObjectContours一致
// 入参: object 图形对象, drawParam 图层绘制参数标识
// 返回: Box 毫米范围, []ObjectContour 绘制轮廓, error 错误信息
func (r *Renderer) ObjectGeometry(object GraphicObject, drawParam string) (Box, []ObjectContour, error) {
	if object.Type != "PathObject" && object.Type != "ImageObject" && object.Type != "CompositeObject" && object.Type != "CompositeGraphicUnit" {
		return Box{}, nil, fmt.Errorf("contours require a path, image or composite object")
	}
	result, err := r.MeasureObject(object, MeasureOptions{Defaults: r.drawParamDefaults(drawParam, nil), Contours: true})
	return result.Bounds, result.Contours, err
}

// MeasureObject 使用当前页面编译器度量对象，保持绘制与选区的字体和几何语义一致
// 入参: object 图形对象, options 度量上下文
// 返回: ObjectMeasurement 度量结果, error 错误信息
func (r *Renderer) MeasureObject(object GraphicObject, options MeasureOptions) (ObjectMeasurement, error) {
	if r.backends.Compiler == nil {
		return ObjectMeasurement{}, fmt.Errorf("%w: object measurement", ErrBackendUnavailable)
	}
	return r.backends.Compiler.MeasureObject(r, object, options)
}
