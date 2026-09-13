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
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/svg"
)

// SVGFont SVG引用的字体资源，Name为CSS字体族标识，Data为只读字体数据
type SVGFont struct {
	Name   string
	Weight int
	Style  string
	Data   []byte
}

// svgFontRenderer 分离字体资源的SVG渲染器
type svgFontRenderer struct {
	*svg.SVG
	renderer *Renderer
	fonts    []SVGFont
	seen     map[string]bool
	styles   strings.Builder
}

// RenderToSVGWithFonts 渲染为SVG并返回页面引用的字体资源，不在SVG中嵌入字体
// 入参: page 页面内容, writer 输出流
// 返回: []SVGFont 字体资源，可按Name复用并按Weight、Style注册，error 错误信息
func (r *Renderer) RenderToSVGWithFonts(page *PageContent, writer io.Writer) ([]SVGFont, error) {
	c, err := r.renderPage(page)
	if err != nil {
		return nil, err
	}
	options := svg.DefaultOptions
	options.EmbedFonts = false
	s := &svgFontRenderer{
		SVG:      svg.New(writer, c.W, c.H, &options),
		renderer: r,
		seen:     make(map[string]bool),
	}
	c.RenderTo(s)
	s.SetCustomStyle(s.styles.String())
	return s.fonts, s.Close()
}

// RenderText 绘制文字并记录实际使用的字体资源
// 入参: text 文字对象, m 变换矩阵
func (s *svgFontRenderer) RenderText(text *canvas.Text, m canvas.Matrix) {
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
