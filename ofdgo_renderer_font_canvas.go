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
	"path"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/font"
)

// ResolveFont 按需加载字体资源，保留内嵌数据及外部字体的名称、样式和集合索引匹配
// 入参: r 渲染器, id 字体标识, exact 是否禁止无关回退
// 返回: ResolvedFont 字体与来源, error 字体不可用错误
func (CanvasBackend) ResolveFont(r *Renderer, id string, exact bool) (ResolvedFont, error) {
	definition := r.Reader.fontCache[id]
	if definition == nil && !r.Reader.fontResourcesRead && r.Reader.OFD != nil {
		if _, err := r.Reader.Fonts(); err != nil {
			return ResolvedFont{}, err
		}
		definition = r.Reader.fontCache[id]
	}
	if definition == nil {
		if exact {
			return ResolvedFont{}, fmt.Errorf("font %q not found", id)
		}
		return r.canvasFallbackFont(), nil
	}
	if definition.FontFile != "" {
		data, err := r.Reader.FontData(id)
		if err != nil {
			return ResolvedFont{}, err
		}
		face, err := r.Reader.embeddedFontFace(*definition)
		if err != nil {
			return ResolvedFont{}, err
		}
		return ResolvedFont{Data: data, Source: path.Base(definition.FontFile), Face: face, Exact: true}, nil
	}
	style := canvasFontStyle(definition)
	if source, family := r.fontSourceMatch(id, definition, exact); family != nil {
		sfnt := family.Face(12, style).Font.SFNT
		face := fontFaceInfo(sfnt.Tables["name"], source.face)
		r.canvasState().fontSourceUsed[id] = source
		return ResolvedFont{Data: fontSFNTData(sfnt), Source: source.name, Face: &face, Exact: source.exact}, nil
	}
	if !exact {
		return r.canvasFallbackFont(), nil
	}
	return ResolvedFont{}, fmt.Errorf("font %q is unavailable", definition.FontName)
}

// canvasFallbackFont 按需加载默认阅读字体，不用于精确编辑匹配
// 返回: ResolvedFont 默认字体，未安装时数据为空
func (r *Renderer) canvasFallbackFont() ResolvedFont {
	if r.canvasState().fontFamily == nil {
		r.initCanvasFonts()
	}
	if r.canvasState().defaultFontLoaded {
		return ResolvedFont{Data: fontSFNTData(r.canvasState().fontFamily.Face(12, canvas.FontRegular).Font.SFNT)}
	}
	return ResolvedFont{}
}

// OpenFont 解析独立SFNT数据，度量与原有编辑排版保持一致
// 入参: data 字体数据
// 返回: FontMetrics 字体度量, error 解析错误
func (CanvasBackend) OpenFont(data []byte) (FontMetrics, error) {
	sfnt, err := font.ParseSFNT(data, 0)
	if err != nil {
		return nil, err
	}
	return canvasFontMetrics{sfnt}, nil
}

// canvasFontMetrics 保留默认字体度量，写出时不更新原始时间
type canvasFontMetrics struct {
	*font.SFNT
}

// Write 返回字体数据，不修改字体元信息
// 返回: []byte 独立SFNT数据
func (f canvasFontMetrics) Write() []byte {
	return fontSFNTData(f.SFNT)
}

// initCanvasFonts 初始化Canvas默认字体
func (r *Renderer) initCanvasFonts() {
	r.canvasState().fontFamily = canvas.NewFontFamily("default")
	r.canvasState().defaultFontLoaded = r.loadDefaultFonts()
}
