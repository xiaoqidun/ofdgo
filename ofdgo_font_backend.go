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

// FontMetrics 提供不依赖绘图库的字体度量，所有整数尺寸使用字体设计单位
// VerticalMetrics依次返回非负的上升高度、下降高度和行间隙
// Write返回独立的SFNT数据，调用方不得修改字体实例
type FontMetrics interface {
	UnitsPerEm() uint16
	NumGlyphs() uint16
	GlyphIndex(rune) uint16
	GlyphAdvance(uint16) uint16
	VerticalMetrics() (uint16, uint16, uint16)
	Write() []byte
}

// ResolvedFont 保存字体来源和只读字体数据，不将外部字体写入文档
type ResolvedFont struct {
	Data   []byte
	Source string
	Face   *FontFace
	Exact  bool
}

// FontBackend 统一字体来源与度量，exact禁止使用无关回退字体
// ResolveFont不得递归调用Renderer.ResolveFont，OpenFont不得修改输入数据
type FontBackend interface {
	Backend
	ResolveFont(renderer *Renderer, id string, exact bool) (ResolvedFont, error)
	OpenFont(data []byte) (FontMetrics, error)
}

// resolvedFontKey 区分只读资源的字体标识与精确匹配要求
type resolvedFontKey struct {
	id         string
	exact      bool
	definition *Font
}

// resolvedFontResult 缓存字体解析结果及不可用原因
type resolvedFontResult struct {
	font ResolvedFont
	err  error
}

// ResolveFont 使用配置的字体后端解析资源，保留精确匹配与回退的区别
// 入参: id 字体资源标识, exact 是否要求精确匹配
// 返回: ResolvedFont 字体与来源, error 解析错误
func (r *Renderer) ResolveFont(id string, exact bool) (ResolvedFont, error) {
	if r.backends.Fonts == nil {
		return ResolvedFont{}, fmt.Errorf("fonts: %w", ErrBackendUnavailable)
	}
	key := resolvedFontKey{id: id, exact: exact, definition: r.Reader.fontCache[id]}
	if cached, ok := r.resolvedFonts[key]; ok {
		return cached.font, cached.err
	}
	resolved, err := r.backends.Fonts.ResolveFont(r, id, exact)
	key.definition = r.Reader.fontCache[id]
	r.resolvedFonts[key] = resolvedFontResult{font: resolved, err: err}
	return resolved, err
}
