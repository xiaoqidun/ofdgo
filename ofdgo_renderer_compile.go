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

import "image/color"

// OFDCompiler 使用库自有页面语义，字体和几何操作交给当前后端配置
type OFDCompiler struct{}

// Name 返回页面编译器标识
// 返回: string 编译器标识
func (OFDCompiler) Name() string { return "ofd" }

// CompilePage 使用公共OFD语义和配置的字形、几何能力编译页面
// 入参: r 渲染器, page 页面
// 返回: *RasterPage 绘制页面, error 编译错误
func (OFDCompiler) CompilePage(r *Renderer, page *PageContent) (*RasterPage, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	c, err := newSemanticCompiler(r)
	if err != nil {
		return nil, err
	}
	c.page = &RasterPage{Width: box.W, Height: box.H, DPI: r.DPI}
	if _, _, err := c.page.PixelSize(); err != nil {
		return nil, err
	}
	if !r.TransparentBackground {
		if err := c.fill(geometryRectangle(Box{W: box.W, H: box.H}), Paint{Kind: PaintSolid, Color: color.RGBA{255, 255, 255, 255}}, false, nil, IdentityMatrix, IdentityMatrix); err != nil {
			return nil, err
		}
	}
	if err := r.WalkPage(page, c); err != nil {
		return nil, err
	}
	if r.OnPageText != nil {
		r.OnPageText(page, &c.text)
	}
	return c.page, nil
}

// PageText 提取与页面绘制相同定位的原文，不读取图片像素
// 入参: r 渲染器, page 页面
// 返回: *PageText 页面文字, error 定位错误
func (OFDCompiler) PageText(r *Renderer, page *PageContent) (*PageText, error) {
	if _, err := r.GetPageBox(page); err != nil {
		return nil, err
	}
	c, err := newSemanticCompiler(r)
	if err != nil {
		return nil, err
	}
	c.textOnly = true
	if err := r.WalkPage(page, c); err != nil {
		return nil, err
	}
	return &c.text, nil
}

// MeasureObject 复用文字和图形编译规则收集范围，不分配页面像素
// 入参: r 渲染器, object 对象, options 度量上下文
// 返回: ObjectMeasurement 对象范围与轮廓, error 度量错误
func (OFDCompiler) MeasureObject(r *Renderer, object GraphicObject, options MeasureOptions) (ObjectMeasurement, error) {
	c, err := newSemanticCompiler(r)
	if err != nil {
		return ObjectMeasurement{}, err
	}
	boundary, ctm := editorGeometry(object)
	if _, err := creationBox(boundary); err != nil {
		return ObjectMeasurement{}, err
	}
	if ctm != "" {
		if _, err := creationNumbers(ctm, 6); err != nil {
			return ObjectMeasurement{}, err
		}
	}
	c.measure = true
	c.contours = options.Contours
	err = r.WalkObject(&object, RenderState{Defaults: options.Defaults, Parent: options.Parent, BoundaryInCTM: options.BoundaryInCTM, Clip: options.Clip}, c)
	return c.measurement, err
}
