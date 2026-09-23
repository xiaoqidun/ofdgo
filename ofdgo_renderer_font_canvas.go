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
	"github.com/tdewolff/canvas"
	"github.com/tdewolff/font"
)

// ResolveFont 按需加载字体资源，保留内嵌数据及外部字体的名称、样式和集合索引匹配
// 入参: r 渲染器, id 字体标识, exact 是否禁止无关回退
// 返回: ResolvedFont 字体与来源, error 字体不可用错误
func (b CanvasBackend) ResolveFont(r *Renderer, id string, exact bool) (ResolvedFont, error) {
	return r.resolveFontSource(b, id, exact)
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

// GlyphOutline 返回基线原点、纵轴向下的字形轮廓
// 入参: glyph 字形编号, size 字号，单位为毫米
// 返回: GeometryPath 字形路径, error 字形解析错误
func (f canvasFontMetrics) GlyphOutline(glyph uint16, size float64) (GeometryPath, error) {
	path := &canvas.Path{}
	if err := f.GlyphPath(path, glyph, 0, 0, 0, size/float64(f.UnitsPerEm()), font.NoHinting); err != nil {
		return nil, err
	}
	return *geometryFromCanvasPath(path), nil
}
