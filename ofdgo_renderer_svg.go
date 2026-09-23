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
	"io"
)

// SVGFont SVG引用的字体资源，Name为CSS字体族标识，Data为只读字体数据
type SVGFont struct {
	Name   string
	Weight int
	Style  string
	Data   []byte
}

// SVGImage SVG引用的图片资源，Name为图片地址，Data为只读图片数据
type SVGImage struct {
	Name string
	MIME string
	Data []byte
}

// SVGResources SVG引用的字体和图片资源
type SVGResources struct {
	Fonts  []SVGFont
	Images []SVGImage
}

// RenderToSVGWithFonts 渲染为SVG并返回页面引用的字体资源，不在SVG中嵌入字体
// 入参: page 页面内容, writer 输出流
// 返回: []SVGFont 字体资源，可按Name复用并按Weight、Style注册，error 错误信息
func (r *Renderer) RenderToSVGWithFonts(page *PageContent, writer io.Writer) ([]SVGFont, error) {
	resources, err := r.renderSVG(page, writer, SVGExternalFonts)
	return resources.Fonts, err
}

// RenderToSVGWithResources 渲染为SVG并分离字体和图片，调用方需注册字体并将图片Name映射为可访问地址
// 入参: page 页面内容, writer 输出流
// 返回: SVGResources 可跨页面复用的资源, error 错误信息
func (r *Renderer) RenderToSVGWithResources(page *PageContent, writer io.Writer) (SVGResources, error) {
	return r.renderSVG(page, writer, SVGExternalResources)
}

// RenderToSVGWithObjects 渲染SVG并为页面对象和注解添加data-ofd-object分组，注解标识使用annotation:前缀
// 入参: page 页面内容, writer 输出流
// 返回: SVGResources 字体和图片资源, error 错误信息
func (r *Renderer) RenderToSVGWithObjects(page *PageContent, writer io.Writer) (SVGResources, error) {
	return r.renderSVG(page, writer, SVGObjects)
}

// renderSVG 将SVG输出及对象分组交给指定后端
// 入参: page 页面内容, writer 输出流, mode 资源封装方式
// 返回: SVGResources 字体和图片资源, error 输出错误
func (r *Renderer) renderSVG(page *PageContent, writer io.Writer, mode SVGMode) (SVGResources, error) {
	if r.backends.SVG == nil {
		return SVGResources{}, fmt.Errorf("svg: %w", ErrBackendUnavailable)
	}
	return r.backends.SVG.RenderSVG(r, page, writer, mode)
}
