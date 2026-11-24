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
	"encoding/binary"
	"encoding/json"
	"image"
	"math"

	"github.com/tdewolff/canvas"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// canvasFontKey 按字体内容、名称及样式区分可复用的字体族，不依赖文档资源编号
type canvasFontKey struct {
	digest [32]byte
	name   string
	style  canvas.FontStyle
}

// canvasFontMapKey 按字体标识与局部字体定义指针隔离当前渲染器的字体族缓存
type canvasFontMapKey struct {
	id         string
	definition *Font
}

// canvasClipCacheKey 区分不依赖文档资源的共享裁剪缓存
type canvasClipCacheKey struct{}

// canvasClipUnionKey 区分文字和图形裁剪的共享并集缓存
type canvasClipUnionKey struct{}

// canvasPathCacheKey 区分不依赖文档资源的共享局部路径缓存
type canvasPathCacheKey struct{}

// canvasFillCacheKey 区分按填充规则规范化的共享路径缓存
type canvasFillCacheKey struct{}

// canvasDrawClipCacheKey 区分绘制轮廓的共享裁剪缓存
type canvasDrawClipCacheKey struct{}

// canvasStrokeCacheKey 区分原生描边与裁剪结果的共享缓存
type canvasStrokeCacheKey struct{}

// canvasStrokeKey 按完整路径、裁剪区域和描边选项区分绘制轮廓
type canvasStrokeKey struct {
	digest  [32]byte
	options string
	fill    bool
	compact bool
}

// canvasOutlineCacheKey 区分子渲染器共享的字体轮廓解析缓存
type canvasOutlineCacheKey struct{}

// canvasTextGlyphCacheKey 区分跨渲染器共享的字形路径缓存
type canvasTextGlyphCacheKey struct{}

// canvasTemplateCommandsKey 区分跨渲染器共享的模板页光栅指令缓存
type canvasTemplateCommandsKey struct{}

// svgTemplateCacheKey 区分跨渲染器共享的模板页SVG片段缓存
type svgTemplateCacheKey struct{}

// stampImageCacheKey 区分跨渲染器共享的图像印章缓存
type stampImageCacheKey struct{}

// stampOFDCacheKey 区分跨渲染器共享的OFD印章解析缓存
type stampOFDCacheKey struct{}

// imageEffectCacheKey 区分跨渲染器共享的透明与蒙版图像缓存
type imageEffectCacheKey struct{}

// templateRasterCacheKey 按模板页面、视口与渲染模式区分共享光栅指令
type templateRasterCacheKey struct {
	template      *PageContent
	width         float64
	height        float64
	offsetX       float64
	offsetY       float64
	dpi           float64
	printing      bool
	printContents string
	decodeImages  bool
}

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

// canvasBackendState 保存当前渲染器的Canvas快速查找缓存
type canvasBackendState struct {
	resources       *Resources
	fontFamilies    map[canvasFontKey]*canvas.FontFamily
	fontFamilyCache *renderCache[canvasFontKey, *canvas.FontFamily]
	fontMap         map[canvasFontMapKey]*canvas.FontFamily
	fontDigests     map[*canvas.Font][32]byte
	svgFontCache    map[*canvas.Font]SVGFont
	fontOutlines    map[*canvas.Font]*basefont.Outliner
	stampSessions   map[[32]byte]*cachedStampSession
}

// sharedCanvasCache 获取或初始化指定键名的线程安全Canvas共享缓存
// 入参: r 渲染器, key 缓存标识键, limit 容量上限字节数
// 返回: *renderCache[K, V] 共享缓存
func sharedCanvasCache[K comparable, V any](r *Renderer, key any, limit int) *renderCache[K, V] {
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[K, V])
	}
	if r.sharedBackendStates == nil {
		r.sharedBackendStates = make(map[any]any)
	}
	cache := resourceCache[K, V](r.reader.resources, limit)
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasGeometryDigest 按完整坐标、路径顺序及几何容差计算摘要
// 入参: paths 非空路径序列
// 返回: [32]byte 几何摘要
func canvasGeometryDigest(paths ...*canvas.Path) [32]byte {
	hash := sha256.New()
	var data [512]byte
	binary.LittleEndian.PutUint64(data[:8], math.Float64bits(canvas.Tolerance))
	binary.LittleEndian.PutUint64(data[8:16], math.Float64bits(canvas.Epsilon))
	binary.LittleEndian.PutUint64(data[16:24], math.Float64bits(canvas.BentleyOttmannEpsilon))
	hash.Write(data[:24])
	for _, path := range paths {
		values := path.Data()
		binary.LittleEndian.PutUint64(data[:8], uint64(len(values)))
		hash.Write(data[:8])
		for len(values) > 0 {
			count := min(len(values), len(data)/8)
			for i, value := range values[:count] {
				binary.LittleEndian.PutUint64(data[i*8:], math.Float64bits(value))
			}
			hash.Write(data[:count*8])
			values = values[count:]
		}
	}
	var key [32]byte
	hash.Sum(key[:0])
	return key
}

// resetFonts 清除Canvas字体及字形缓存
func (s *canvasBackendState) resetFonts() {
	s.fontFamilies = make(map[canvasFontKey]*canvas.FontFamily)
	s.fontFamilyCache = nil
	s.fontMap = make(map[canvasFontMapKey]*canvas.FontFamily)
	s.fontDigests = make(map[*canvas.Font][32]byte)
	s.svgFontCache = make(map[*canvas.Font]SVGFont)
	s.fontOutlines = make(map[*canvas.Font]*basefont.Outliner)
	s.stampSessions = make(map[[32]byte]*cachedStampSession)
}

// forkSharedState 将Canvas后端的线程安全共享缓存传递给Fork子渲染器
// 入参: src 源渲染器, dst 目标子渲染器
func (DefaultBackend) forkSharedState(src, dst *Renderer) {
	dst.sharedBackendStates[DefaultBackend{}] = src.canvasFontData()
	dst.sharedBackendStates[canvasTextGlyphCacheKey{}] = src.canvasTextGlyphPaths()
	dst.sharedBackendStates[canvasOutlineCacheKey{}] = src.canvasFontOutlines()
	dst.sharedBackendStates[canvasClipUnionKey{}] = src.canvasClipUnions()
	dst.sharedBackendStates[canvasPathCacheKey{}] = src.canvasParsedPaths()
	dst.sharedBackendStates[canvasClipCacheKey{}] = src.canvasClipIntersections()
	dst.sharedBackendStates[canvasFillCacheKey{}] = src.canvasSettledPaths()
	dst.sharedBackendStates[canvasDrawClipCacheKey{}] = src.canvasDrawClips()
	dst.sharedBackendStates[canvasStrokeCacheKey{}] = src.canvasStrokeClips()
	dst.sharedBackendStates[canvasTemplateCommandsKey{}] = src.canvasTemplateCommands()
	dst.sharedBackendStates[svgResourceCacheKey{}] = src.svgImageResources()
	dst.sharedBackendStates[svgFontCacheKey{}] = src.svgFontResources()
	dst.sharedBackendStates[svgTemplateCacheKey{}] = src.svgTemplateFragments()
	dst.sharedBackendStates[stampImageCacheKey{}] = src.stampImageCache()
	dst.sharedBackendStates[stampOFDCacheKey{}] = src.stampOFDCache()
	dst.sharedBackendStates[imageEffectCacheKey{}] = src.imageEffectCache()
}

// canvasClipIntersections 获取子渲染器共享的几何交集缓存，限制结果保留量
// 返回: *renderCache[[32]byte, *canvas.Path] 裁剪缓存
func (r *Renderer) canvasClipIntersections() *renderCache[[32]byte, *canvas.Path] {
	return sharedCanvasCache[[32]byte, *canvas.Path](r, canvasClipCacheKey{}, 8<<20)
}

// canvasClipUnions 复用相同输入轮廓的裁剪并集，不改变布尔运算顺序
// 返回: *renderCache[[32]byte, *canvas.Path] 只读裁剪并集缓存
func (r *Renderer) canvasClipUnions() *renderCache[[32]byte, *canvas.Path] {
	return sharedCanvasCache[[32]byte, *canvas.Path](r, canvasClipUnionKey{}, 8<<20)
}

// canvasParsedPaths 获取子渲染器共享的局部路径缓存，不保留对象变换
// 返回: *renderCache[canvasPathKey, *canvas.Path] 路径缓存
func (r *Renderer) canvasParsedPaths() *renderCache[canvasPathKey, *canvas.Path] {
	return sharedCanvasCache[canvasPathKey, *canvas.Path](r, canvasPathCacheKey{}, 16<<20)
}

// canvasSettledPaths 获取共享填充轮廓缓存，限制结果保留量
// 返回: *renderCache[canvasFillKey, *canvas.Path] 填充轮廓缓存
func (r *Renderer) canvasSettledPaths() *renderCache[canvasFillKey, *canvas.Path] {
	return sharedCanvasCache[canvasFillKey, *canvas.Path](r, canvasFillCacheKey{}, 16<<20)
}

// canvasFontData 获取子渲染器共享的只读字体字节缓存，限制解析数据保留量
// 返回: *renderCache[[32]byte, []byte] 字体数据缓存
func (r *Renderer) canvasFontData() *renderCache[[32]byte, []byte] {
	return sharedCanvasCache[[32]byte, []byte](r, DefaultBackend{}, 32<<20)
}

// canvasDrawClips 获取派生渲染器共享的绘制裁剪缓存
// 返回: *renderCache[[32]byte, *canvas.Path] 绘制裁剪缓存
func (r *Renderer) canvasDrawClips() *renderCache[[32]byte, *canvas.Path] {
	return sharedCanvasCache[[32]byte, *canvas.Path](r, canvasDrawClipCacheKey{}, 16<<20)
}

// canvasStrokeClips 获取原生描边与裁剪结果缓存，按资源策略限制保留量
// 返回: *renderCache[canvasStrokeKey, *canvas.Path] 描边缓存
func (r *Renderer) canvasStrokeClips() *renderCache[canvasStrokeKey, *canvas.Path] {
	return sharedCanvasCache[canvasStrokeKey, *canvas.Path](r, canvasStrokeCacheKey{}, 16<<20)
}

// strokeClippedCanvasPath 复用原生描边与裁剪结果，纯描边按原点归一化共享缓存，自定义几何后端沿用逐次调用
// 入参: path 原始路径, clip 裁剪路径, options 描边选项, fill 是否同时填充
// 返回: *canvas.Path 描边并裁剪后的路径
func (r *Renderer) strokeClippedCanvasPath(path, clip *canvas.Path, options StrokeOptions, fill bool) *canvas.Path {
	geometry, err := r.Geometry()
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	applyResultClip := func(stroked *canvas.Path) *canvas.Path {
		if fill {
			return applyFillClipPath(stroked, clip)
		}
		return applyClipPath(stroked, clip)
	}
	strokeOnly := func(target *canvas.Path) (*canvas.Path, int, error) {
		stroke := canvasStrokePath
		if fill && !r.compactStrokes {
			stroke = canvasStrokeFillPath
		}
		result, err := stroke(geometry, target, options)
		if err != nil {
			return nil, 0, err
		}
		return result, len(result.Data())*8 + 256, nil
	}
	var result *canvas.Path
	if canvasNativeStroke(geometry) {
		encoded, encodeErr := json.Marshal(options)
		if encodeErr != nil {
			r.renderError = encodeErr
			return &canvas.Path{}
		}
		if clip != nil {
			key := canvasStrokeKey{digest: canvasGeometryDigest(path, clip), options: string(encoded), fill: fill, compact: r.compactStrokes}
			if cached, ok := r.canvasStrokeClips().get(key); ok {
				return cached.Copy()
			}
		}
		bounds := path.Bounds()
		tx, ty := bounds.X0, bounds.Y0
		normPath := path
		shifted := (tx != 0 || ty != 0) && !path.Empty()
		if shifted {
			normPath = path.Copy().Translate(-tx, -ty)
		}
		strokeKey := canvasStrokeKey{digest: canvasGeometryDigest(normPath), options: string(encoded), fill: fill, compact: r.compactStrokes}
		stroked, strokeErr := r.canvasStrokeClips().load(r.pageContext, strokeKey, func() (*canvas.Path, int, error) {
			return strokeOnly(normPath)
		})
		if strokeErr != nil {
			r.renderError = strokeErr
			return &canvas.Path{}
		}
		result = stroked.Copy()
		if shifted {
			result = result.Translate(tx, ty)
		}
		if clip != nil {
			result = applyResultClip(result)
			clipKey := canvasStrokeKey{digest: canvasGeometryDigest(path, clip), options: string(encoded), fill: fill, compact: r.compactStrokes}
			r.canvasStrokeClips().put(clipKey, result.Copy(), len(result.Data())*8+256)
		}
		return result
	}
	result, _, err = strokeOnly(path)
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	return applyResultClip(result)
}

// applyCachedClipPath 复用绘制轮廓的裁剪结果，返回可独立变换的副本
// 入参: path 绘制轮廓, clip 裁剪轮廓
// 返回: *canvas.Path 裁剪结果
func (r *Renderer) applyCachedClipPath(path, clip *canvas.Path) *canvas.Path {
	if path == nil || clip == nil {
		return path
	}
	if rect, ok := rectangularPath(clip); ok && rect.Contains(path.Bounds()) {
		return reduceRepeatedNonZeroPath(path)
	}
	key := canvasGeometryDigest(path, clip)
	result, err := r.canvasDrawClips().load(r.pageContext, key, func() (*canvas.Path, int, error) {
		result := applyClipPath(path, clip).Copy()
		return result, len(result.Data())*8 + 256, nil
	})
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	return result.Copy()
}

// canvasFontOutlines 获取子渲染器共享的字体轮廓解析缓存，限制字体及解析数据保留量
// 返回: *renderCache[[32]byte, *basefont.Outliner] 字体轮廓缓存
func (r *Renderer) canvasFontOutlines() *renderCache[[32]byte, *basefont.Outliner] {
	return sharedCanvasCache[[32]byte, *basefont.Outliner](r, canvasOutlineCacheKey{}, 32<<20)
}

// canvasFontFamilies 获取当前渲染器私有的字体族缓存，不跨Fork共享
// 返回: *renderCache[canvasFontKey, *canvas.FontFamily] 字体族缓存
func (r *Renderer) canvasFontFamilies() *renderCache[canvasFontKey, *canvas.FontFamily] {
	state := r.canvasState()
	if state.fontFamilyCache == nil {
		state.fontFamilyCache = resourceCache[canvasFontKey, *canvas.FontFamily](r.reader.resources, 64<<20)
	}
	return state.fontFamilyCache
}

// canvasTextGlyphPaths 获取跨渲染器共享的字形轮廓路径缓存
// 返回: *renderCache[textGlyphPathCacheKey, textGlyphPathCacheValue] 字形路径缓存
func (r *Renderer) canvasTextGlyphPaths() *renderCache[textGlyphPathCacheKey, textGlyphPathCacheValue] {
	return sharedCanvasCache[textGlyphPathCacheKey, textGlyphPathCacheValue](r, canvasTextGlyphCacheKey{}, 16<<20)
}

// canvasTemplateCommands 获取跨渲染器共享的模板页光栅指令缓存
// 返回: *renderCache[templateRasterCacheKey, []RasterCommand] 模板光栅指令缓存
func (r *Renderer) canvasTemplateCommands() *renderCache[templateRasterCacheKey, []RasterCommand] {
	return sharedCanvasCache[templateRasterCacheKey, []RasterCommand](r, canvasTemplateCommandsKey{}, 32<<20)
}

// stampImageCache 获取跨渲染器共享的图像印章缓存
// 返回: *renderCache[[32]byte, image.Image] 图像印章缓存
func (r *Renderer) stampImageCache() *renderCache[[32]byte, image.Image] {
	return sharedCanvasCache[[32]byte, image.Image](r, stampImageCacheKey{}, 32<<20)
}

// stampOFDCache 获取跨渲染器共享的OFD印章解析缓存
// 返回: *renderCache[[32]byte, *cachedStampOFD] OFD印章缓存
func (r *Renderer) stampOFDCache() *renderCache[[32]byte, *cachedStampOFD] {
	return sharedCanvasCache[[32]byte, *cachedStampOFD](r, stampOFDCacheKey{}, 32<<20)
}

// imageEffectCache 获取跨渲染器共享的透明与蒙版图像合成缓存
// 返回: *renderCache[imageEffectKey, image.Image] 效果图像缓存
func (r *Renderer) imageEffectCache() *renderCache[imageEffectKey, image.Image] {
	return sharedCanvasCache[imageEffectKey, image.Image](r, imageEffectCacheKey{}, 32<<20)
}

// canvasState 获取当前渲染器的Canvas私有缓存，按需创建且不向其他后端暴露
// 返回: *canvasBackendState Canvas缓存
func (r *Renderer) canvasState() *canvasBackendState {
	key := DefaultBackend{}
	if state, ok := r.backendStates[key]; ok {
		return state.(*canvasBackendState)
	}
	state := &canvasBackendState{
		resources: r.reader.resources,
	}
	state.resetFonts()
	r.backendStates[key] = state
	return state
}

// settleCanvasPath 复用复杂路径的填充规范化结果，返回独立副本
// 入参: path 页面坐标路径, rule 填充规则
// 返回: *canvas.Path 规范化填充轮廓
func (r *Renderer) settleCanvasPath(path *canvas.Path, rule canvas.FillRule) *canvas.Path {
	if len(path.Data()) < 256 {
		return settleCanvasPath(path, rule)
	}
	key := canvasFillKey{digest: canvasGeometryDigest(path), rule: rule}
	cache := r.canvasSettledPaths()
	if result, ok := cache.get(key); ok {
		return result.Copy()
	}
	result := settleCanvasPath(path, rule)
	if cost := len(result.Data())*8 + 256; cost <= cache.limit {
		cache.put(key, result.Copy(), cost)
	}
	return result
}
