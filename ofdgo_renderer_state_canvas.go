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
	"image"

	"github.com/tdewolff/canvas"
)

// canvasFontKey 按字体内容、名称及样式区分可复用的字体族，不依赖文档资源编号
type canvasFontKey struct {
	digest [32]byte
	name   string
	style  canvas.FontStyle
}

// canvasClipCacheKey 区分不依赖文档资源的共享裁剪缓存
type canvasClipCacheKey struct{}

// canvasPathCacheKey 区分不依赖文档资源的共享局部路径缓存
type canvasPathCacheKey struct{}

// canvasFillCacheKey 区分按填充规则规范化的共享路径缓存
type canvasFillCacheKey struct{}

// canvasFillKey 保存完整几何摘要及填充规则
type canvasFillKey struct {
	digest [32]byte
	rule   canvas.FillRule
}

// canvasPathKey 按完整路径文本与几何容差区分解析结果
type canvasPathKey struct {
	data    string
	epsilon float64
}

// canvasBackendState 保存Canvas字体、字形和编码图片适配缓存
type canvasBackendState struct {
	images             renderCache[*EncodedImage, image.Image]
	fontMap            map[string]*canvas.FontFamily
	svgFontCache       map[*canvas.Font]SVGFont
	textGlyphPathCache renderCache[textGlyphPathCacheKey, textGlyphPathCacheValue]
	fontOutlines       map[*canvas.Font]*sfntOutliner
}

// canvasClipIntersections 获取子渲染器共享的几何交集缓存，限制结果保留量
// 返回: *renderCache[[32]byte, *canvas.Path] 裁剪缓存
func (r *Renderer) canvasClipIntersections() *renderCache[[32]byte, *canvas.Path] {
	key := canvasClipCacheKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[[32]byte, *canvas.Path])
	}
	if r.sharedBackendStates == nil {
		r.sharedBackendStates = make(map[any]any)
	}
	cache := &renderCache[[32]byte, *canvas.Path]{limit: 8 << 20}
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasParsedPaths 获取子渲染器共享的局部路径缓存，不保留对象变换
// 返回: *renderCache[canvasPathKey, *canvas.Path] 路径缓存
func (r *Renderer) canvasParsedPaths() *renderCache[canvasPathKey, *canvas.Path] {
	key := canvasPathCacheKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[canvasPathKey, *canvas.Path])
	}
	if r.sharedBackendStates == nil {
		r.sharedBackendStates = make(map[any]any)
	}
	cache := &renderCache[canvasPathKey, *canvas.Path]{limit: 16 << 20}
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasSettledPaths 获取共享填充轮廓缓存，限制结果保留量
// 返回: *renderCache[canvasFillKey, *canvas.Path] 填充轮廓缓存
func (r *Renderer) canvasSettledPaths() *renderCache[canvasFillKey, *canvas.Path] {
	key := canvasFillCacheKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[canvasFillKey, *canvas.Path])
	}
	if r.sharedBackendStates == nil {
		r.sharedBackendStates = make(map[any]any)
	}
	cache := &renderCache[canvasFillKey, *canvas.Path]{limit: 16 << 20}
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasFontFamilies 获取子渲染器共享的只读字体族缓存，限制解析数据保留量
// 返回: *renderCache[canvasFontKey, *canvas.FontFamily] 字体族缓存
func (r *Renderer) canvasFontFamilies() *renderCache[canvasFontKey, *canvas.FontFamily] {
	key := CanvasBackend{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[canvasFontKey, *canvas.FontFamily])
	}
	cache := &renderCache[canvasFontKey, *canvas.FontFamily]{limit: 32 << 20}
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasState 获取当前渲染器的Canvas私有缓存，按需创建且不向其他后端暴露
// 返回: *canvasBackendState Canvas缓存
func (r *Renderer) canvasState() *canvasBackendState {
	key := CanvasBackend{}
	if state, ok := r.backendStates[key]; ok {
		return state.(*canvasBackendState)
	}
	state := &canvasBackendState{
		images: renderCache[*EncodedImage, image.Image]{limit: imageCacheLimit},
	}
	state.resetFonts()
	r.backendStates[key] = state
	return state
}

// resetFonts 清除Canvas字体及字形缓存，保留编码图片适配对象
func (s *canvasBackendState) resetFonts() {
	s.fontMap = make(map[string]*canvas.FontFamily)
	s.svgFontCache = make(map[*canvas.Font]SVGFont)
	s.textGlyphPathCache = renderCache[textGlyphPathCacheKey, textGlyphPathCacheValue]{limit: 16 << 20}
	s.fontOutlines = make(map[*canvas.Font]*sfntOutliner)
}
