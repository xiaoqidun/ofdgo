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
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

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

// FontOutlines 提供基线原点、纵轴向下的字形轮廓，不绑定字体实现
// GlyphOutline返回只读路径，字号与路径单位为毫米
type FontOutlines interface {
	FontMetrics
	GlyphOutline(glyph uint16, size float64) (GeometryPath, error)
}

// FontOutlineBatch 批量提取指定字形，不经过字符映射或文字塑形
// 返回路径与输入编号一一对应，允许重复编号和空轮廓，路径只读
type FontOutlineBatch interface {
	FontOutlines
	GlyphOutlines(glyphs []uint16, size float64) ([]GeometryPath, error)
}

// FontGlyphDiagnostics 提供已提取字形的恢复提示，不将提示作为轮廓解析失败
// 字形不存在或不支持的指令仍由GlyphOutline返回错误
type FontGlyphDiagnostics interface {
	FontMetrics
	GlyphWarning(glyph uint16) error
}

// FontGlyphBounds 提供与字形轮廓一致的边界，字号及边界单位为毫米
// 不使用字体全局边界替代单个字形，空轮廓返回零边界
type FontGlyphBounds interface {
	FontOutlines
	GlyphBounds(glyph uint16, size float64) (Box, error)
}

// FontShaper 提供可选的横向文字塑形，仅用于显式新建或重排
// 字形按逻辑簇顺序排列，Cluster使用原文的符文索引，坐标为毫米且纵轴向下
type FontShaper interface {
	FontMetrics
	ShapeText(value string, size float64) ([]ShapedGlyph, error)
}

// TextShapeOptions 指定文字塑形参数，Bidi启用双向及脚本分段，不执行字体回退
// Direction为空或ltr时从左向右，rtl时从右向左；Script为四字母ISO15924代码，空值自动检测
// Bidi启用时Direction指定段落方向，各文字片段按双向算法排列
// Language为语言标签，Features为逗号分隔的全局OpenType特性，如kern=0,liga=1，不接受字符区间
// 零值保持原有FontShaper行为，非零值要求FontShaperOptions能力
type TextShapeOptions struct {
	Bidi      bool
	Direction string
	Script    string
	Language  string
	Features  string
}

// FontShaperOptions 提供显式可配置塑形，簇与坐标遵循FontShaper约定
// 右向左文字仍按逻辑簇返回，视觉顺序由X和Y保留
type FontShaperOptions interface {
	FontShaper
	ShapeTextWithOptions(value string, size float64, options TextShapeOptions) ([]ShapedGlyph, error)
}

// FontShaperRun 保留完整单行原文的上下文，仅塑形指定区间，不执行字体回退
// start和end为原文的符文索引，区间左闭右开，返回Cluster仍使用原文索引
// 调用方负责方向和脚本分段，options.Bidi须为false；坐标相对当前区间的基线起点
// value为已校验的原文符文序列，调用期间不得修改，返回结果不引用该切片
type FontShaperRun interface {
	FontShaperOptions
	ShapeTextRun(value []rune, start, end int, size float64, options TextShapeOptions) ([]ShapedGlyph, error)
}

// ShapedGlyph 保存塑形后的字形、原文簇和基线定位
// VisualOrder为簇从左到右的零起始顺序，同簇字形共用顺序
type ShapedGlyph struct {
	Glyph         uint16
	Cluster       int
	VisualOrder   int
	X, Y, Advance float64
}

// ResolvedFont 保存字体来源和只读字体数据，不将外部字体写入文档
type ResolvedFont struct {
	Data   []byte
	Source string
	Face   *FontFace
	Exact  bool
	digest [32]byte
}

// PreparedFont 保存绘制用字体及GID、CID到包装字符的映射，所有数据只读
// Data可与源编码不同，原始资源仍由ResolveFont和Reader保留
type PreparedFont struct {
	ResolvedFont
	Glyphs     map[uint16]rune
	CIDs       map[uint16]rune
	definition *Font
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
// 页面取消及后端取消、超时结果不缓存，后续调用可重试
// 入参: id 字体资源标识, exact 是否要求精确匹配
// 返回: ResolvedFont 字体与来源, error 解析错误
func (r *Renderer) ResolveFont(id string, exact bool) (ResolvedFont, error) {
	if r.pageContext != nil {
		if err := r.pageContext.Err(); err != nil {
			return ResolvedFont{}, err
		}
	}
	if r.backends.Fonts == nil {
		return ResolvedFont{}, fmt.Errorf("fonts: %w", ErrBackendUnavailable)
	}
	key := resolvedFontKey{id: id, exact: exact, definition: r.fontDefinition(id)}
	if key.definition != nil && key.definition.ID != "" {
		key.id = key.definition.ID
	}
	if cached, ok := r.resolvedFonts[key]; ok {
		return cached.font, cached.err
	}
	resolved, err := r.backends.Fonts.ResolveFont(r, key.id, exact)
	if r.pageContext != nil {
		if err := r.pageContext.Err(); err != nil {
			return ResolvedFont{}, err
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ResolvedFont{}, err
	}
	key.definition = r.fontDefinition(id)
	if key.definition != nil && key.definition.ID != "" {
		key.id = key.definition.ID
	}
	r.resolvedFonts[key] = resolvedFontResult{font: resolved, err: err}
	return resolved, err
}

// PrepareFont 统一处理内嵌字体包装和索引映射，供各编译器复用并合并并发修复请求
// 子渲染器按原始内容复用只读包装结果，各文档及页面作用域保留独立的来源信息
// 入参: id 字体资源标识
// 返回: *PreparedFont 绘制字体, error 字体解析错误
func (r *Renderer) PrepareFont(id string) (*PreparedFont, error) {
	if r.pageContext != nil {
		if err := r.pageContext.Err(); err != nil {
			return nil, err
		}
	}
	origID := id
	definition := r.fontDefinition(id)
	if definition != nil && definition.ID != "" {
		id = definition.ID
	}
	if cached := r.preparedFonts[id]; cached != nil && cached.definition == definition {
		if origID != id {
			r.preparedFonts[origID] = cached
		}
		return cached, nil
	}
	resolved, err := r.ResolveFont(id, false)
	if err != nil {
		return nil, err
	}
	definition = r.fontDefinition(id)
	if definition != nil && definition.ID != "" {
		id = definition.ID
	}
	prepared := &PreparedFont{ResolvedFont: resolved, definition: definition}
	if definition != nil && definition.FontFile != "" && len(resolved.Data) > 0 {
		key := r.fontDigest(resolved.Data)
		ctx := r.pageContext
		if ctx == nil {
			ctx = r.outputContext()
		}
		content, err := r.fontPreparations.load(ctx, key, func() (PreparedFont, int, error) {
			out := PreparedFont{ResolvedFont: ResolvedFont{Data: resolved.Data}, CIDs: basefont.GetCFFCIDRuneMap(resolved.Data)}
			if _, data, mapping, _, fixErr := basefont.FixDataAggressive(resolved.Data, true, true); fixErr == nil {
				out.Data = data
				if mapping != nil {
					out.Glyphs = make(map[uint16]rune)
					for character, glyph := range mapping {
						if character == basefont.PackedGlyphRune(glyph) {
							out.Glyphs[glyph] = character
						}
					}
					for character, glyph := range mapping {
						if _, ok := out.Glyphs[glyph]; !ok {
							out.Glyphs[glyph] = character
						}
					}
				}
			}
			out.digest = r.fontDigest(out.Data)
			cost := len(out.Data) + (len(out.Glyphs)+len(out.CIDs))*16 + 512
			return out, cost, nil
		})
		if err != nil {
			return nil, err
		}
		prepared.Data, prepared.Glyphs, prepared.CIDs, prepared.digest = content.Data, content.Glyphs, content.CIDs, content.digest
	}
	r.preparedFonts[id] = prepared
	if origID != id {
		r.preparedFonts[origID] = prepared
	}
	return prepared, nil
}

// fontDigest 计算字体字节内容的SHA-256摘要
// 入参: data 字体数据
// 返回: [32]byte 字体摘要
func (r *Renderer) fontDigest(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// resetFontCache 重置字体解析及字形缓存，保留图片和非字体后端状态
func (r *Renderer) resetFontCache() {
	if r.fontSourcesCache != nil {
		r.fontSourcesCache.data.clear()
		r.fontSourcesCache.collections.clear()
		r.fontSourcesCache.resolved.clear()
	}
	r.fontSourcesCache = newFontSourceCache(r.reader.resources)
	r.resetFontBackendCache()
}

// resetFontBackendCache 清理后端字体对象，保留字体文件和图片缓存
func (r *Renderer) resetFontBackendCache() {
	r.fontPreparations.clear()
	r.fontMetrics.clear()
	r.glyphOutlines.clear()
	for _, state := range r.sharedBackendStates {
		if cache, ok := state.(interface{ clear() }); ok {
			cache.clear()
		}
	}
	r.sharedBackendStates = make(map[any]any)
	r.preparedFonts = make(map[string]*PreparedFont)
	r.fontPreparations = resourceCache[[32]byte, PreparedFont](r.reader.resources, 64<<20)
	r.resolvedFonts = make(map[resolvedFontKey]resolvedFontResult)
	if r.fontSourcesCache != nil {
		r.fontSourcesCache.fallback = ResolvedFont{}
		r.fontSourcesCache.fallbackRead = false
	}
	r.fontMetrics = resourceCache[[32]byte, FontMetrics](r.reader.resources, 64<<20)
	r.glyphOutlines = resourceCache[glyphOutlineKey, GeometryPath](r.reader.resources, 16<<20)
	for _, state := range r.backendStates {
		if fonts, ok := state.(interface{ resetFonts() }); ok {
			fonts.resetFonts()
		}
	}
}
