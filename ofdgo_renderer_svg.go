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
	"html"
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
	objects    map[*GraphicObject]string
}

// RenderToSVGWithFonts 渲染为SVG并返回页面引用的字体资源，不在SVG中嵌入字体
// 入参: page 页面内容, writer 输出流
// 返回: []SVGFont 字体资源，可按Name复用并按Weight、Style注册，error 错误信息
func (r *Renderer) RenderToSVGWithFonts(page *PageContent, writer io.Writer) ([]SVGFont, error) {
	resources, err := r.renderSVGResources(page, writer, false, false)
	return resources.Fonts, err
}

// RenderToSVGWithResources 渲染为SVG并分离字体和图片，调用方需注册字体并将图片Name映射为可访问地址
// 入参: page 页面内容, writer 输出流
// 返回: SVGResources 可跨页面复用的资源, error 错误信息
func (r *Renderer) RenderToSVGWithResources(page *PageContent, writer io.Writer) (SVGResources, error) {
	return r.renderSVGResources(page, writer, true, false)
}

// RenderToSVGWithObjects 渲染SVG并为页面直接对象添加data-ofd-object分组，保持原绘制顺序和分离资源
// 入参: page 页面内容, writer 输出流
// 返回: SVGResources 字体和图片资源, error 错误信息
func (r *Renderer) RenderToSVGWithObjects(page *PageContent, writer io.Writer) (SVGResources, error) {
	return r.renderSVGResources(page, writer, true, true)
}

// renderSVGResources 渲染SVG并收集外部资源
// 入参: page 页面内容, writer 输出流, images 是否分离图片, objects 是否标识页面直接对象
// 返回: SVGResources 外部资源, error 错误信息
func (r *Renderer) renderSVGResources(page *PageContent, writer io.Writer, images, objects bool) (SVGResources, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return SVGResources{}, err
	}
	options := svg.DefaultOptions
	options.EmbedFonts = false
	buffer := bufio.NewWriter(writer)
	s := &svgResourceRenderer{
		SVG:      svg.New(buffer, box.W, box.H, &options),
		renderer: r,
		writer:   buffer,
		seen:     make(map[string]bool),
	}
	if images {
		s.imageNames = make(map[image.Image]string)
	}
	if objects {
		s.objects = make(map[*GraphicObject]string)
		for i := range page.Content.Layer {
			for j := range page.Content.Layer[i].Objects {
				object := &page.Content.Layer[i].Objects[j]
				s.objects[object] = editorObjectID(*object)
			}
		}
		if err := r.renderPageToContext(canvas.NewContext(s), page, true); err != nil {
			return SVGResources{}, err
		}
	} else {
		c, err := r.renderPage(page)
		if err != nil {
			return SVGResources{}, err
		}
		c.RenderTo(s)
	}
	if s.err != nil {
		return SVGResources{}, s.err
	}
	s.SetCustomStyle(s.styles.String())
	if err := s.Close(); err != nil {
		return SVGResources{}, err
	}
	return SVGResources{Fonts: s.fonts, Images: s.images}, buffer.Flush()
}

// beginObject 开始标记当前页面直接对象，模板、注释和内部复合对象不单独标记
// 入参: object 图形对象
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginObject(object *GraphicObject) bool {
	id := s.objects[object]
	if id == "" {
		return false
	}
	fmt.Fprintf(s.writer, `<g data-ofd-object="%s">`, html.EscapeString(id))
	return true
}

// endObject 结束对象分组
func (s *svgResourceRenderer) endObject() {
	fmt.Fprint(s.writer, `</g>`)
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
