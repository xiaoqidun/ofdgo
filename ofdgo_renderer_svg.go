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
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"

	"github.com/tdewolff/canvas"
	canvasimage "github.com/tdewolff/canvas/image"
	"github.com/tdewolff/canvas/renderers/svg"
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

// svgResourceRenderer 分离外部资源的SVG渲染器
type svgResourceRenderer struct {
	*svg.SVG
	renderer   *Renderer
	writer     io.Writer
	fonts      []SVGFont
	images     []SVGImage
	imageNames map[image.Image]string
	seen       map[string]bool
	styles     strings.Builder
	err        error
}

// RenderToSVGWithFonts 渲染为SVG并返回页面引用的字体资源，不在SVG中嵌入字体
// 入参: page 页面内容, writer 输出流
// 返回: []SVGFont 字体资源，可按Name复用并按Weight、Style注册，error 错误信息
func (r *Renderer) RenderToSVGWithFonts(page *PageContent, writer io.Writer) ([]SVGFont, error) {
	resources, err := r.renderSVGResources(page, writer, false)
	return resources.Fonts, err
}

// RenderToSVGWithResources 渲染为SVG并分离字体和图片，调用方需注册字体并将图片Name映射为可访问地址
// 入参: page 页面内容, writer 输出流
// 返回: SVGResources 可跨页面复用的资源, error 错误信息
func (r *Renderer) RenderToSVGWithResources(page *PageContent, writer io.Writer) (SVGResources, error) {
	return r.renderSVGResources(page, writer, true)
}

// renderSVGResources 渲染SVG并收集外部资源
// 入参: page 页面内容, writer 输出流, images 是否分离图片
// 返回: SVGResources 外部资源, error 错误信息
func (r *Renderer) renderSVGResources(page *PageContent, writer io.Writer, images bool) (SVGResources, error) {
	c, err := r.renderPage(page)
	if err != nil {
		return SVGResources{}, err
	}
	options := svg.DefaultOptions
	options.EmbedFonts = false
	buffer := bufio.NewWriter(writer)
	s := &svgResourceRenderer{
		SVG:      svg.New(buffer, c.W, c.H, &options),
		renderer: r,
		writer:   buffer,
		seen:     make(map[string]bool),
	}
	if images {
		s.imageNames = make(map[image.Image]string)
	}
	c.RenderTo(s)
	if s.err != nil {
		return SVGResources{}, s.err
	}
	s.SetCustomStyle(s.styles.String())
	if err := s.Close(); err != nil {
		return SVGResources{}, err
	}
	return SVGResources{Fonts: s.fonts, Images: s.images}, buffer.Flush()
}

// RenderImage 绘制图片，分离资源时保持原始编码、尺寸和变换
// 入参: img 图片对象, m 变换矩阵
func (s *svgResourceRenderer) RenderImage(img image.Image, m canvas.Matrix) {
	if s.imageNames == nil {
		s.SVG.RenderImage(img, m)
		return
	}
	name, ok := s.imageNames[img]
	if !ok {
		resource := SVGImage{MIME: "image/png"}
		if encoded, ok := img.(*canvasimage.Image); ok {
			resource.MIME, resource.Data = encoded.Mimetype, encoded.Bytes
		} else {
			var buffer bytes.Buffer
			if err := png.Encode(&buffer, img); err != nil {
				s.err = err
				return
			}
			resource.Data = buffer.Bytes()
		}
		name = fmt.Sprintf("ofdgo-image-%x", sha256.Sum256(resource.Data))
		resource.Name = name
		s.imageNames[img] = name
		if !s.seen[name] {
			s.seen[name] = true
			s.images = append(s.images, resource)
		}
	}
	_, height := s.Size()
	size := img.Bounds().Size()
	fmt.Fprintf(s.writer, `<image transform="%s" width="%d" height="%d" xlink:href="%s"/>`, m.Translate(0, float64(size.Y)).ToSVG(height), size.X, size.Y, name)
}

// RenderText 绘制文字并记录实际使用的字体资源
// 入参: text 文字对象, m 变换矩阵
func (s *svgResourceRenderer) RenderText(text *canvas.Text, m canvas.Matrix) {
	if text.Empty() {
		return
	}
	font := text.MostCommonFontFace().Font
	resource, ok := s.renderer.svgFontCache[font]
	if !ok {
		data := font.SFNT.Write()
		resource = SVGFont{
			Name:   fmt.Sprintf("ofdgo-%x-%d", sha256.Sum256(data), font.Style()),
			Weight: font.Style().CSS(),
			Style:  "normal",
			Data:   data,
		}
		if font.Style().Italic() {
			resource.Style = "italic"
		}
		s.renderer.svgFontCache[font] = resource
	}
	if !s.seen[resource.Name] {
		s.seen[resource.Name] = true
		s.fonts = append(s.fonts, resource)
		fmt.Fprintf(&s.styles, ".%s{font-family:%s!important}", resource.Name, resource.Name)
	}
	s.SetClass(resource.Name)
	s.SVG.RenderText(text, m)
	s.SetClass()
}
