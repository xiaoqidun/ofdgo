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

// canvasBackendState 保存Canvas字体与字形适配缓存
type canvasBackendState struct {
	resources          *Resources
	fontFamilies       map[canvasFontKey]*canvas.FontFamily
	fontMap            map[canvasFontMapKey]*canvas.FontFamily
	fontDigests        map[*canvas.Font][32]byte
	svgFontCache       map[*canvas.Font]SVGFont
	textGlyphPathCache renderCache[textGlyphPathCacheKey, textGlyphPathCacheValue]
	fontOutlines       map[*canvas.Font]*basefont.Outliner
}

// resetFonts 清除Canvas字体及字形缓存
func (s *canvasBackendState) resetFonts() {
	s.textGlyphPathCache.clear()
	s.fontFamilies = make(map[canvasFontKey]*canvas.FontFamily)
	s.fontMap = make(map[canvasFontMapKey]*canvas.FontFamily)
	s.fontDigests = make(map[*canvas.Font][32]byte)
	s.svgFontCache = make(map[*canvas.Font]SVGFont)
	s.textGlyphPathCache = *resourceCache[textGlyphPathCacheKey, textGlyphPathCacheValue](s.resources, 16<<20)
	s.fontOutlines = make(map[*canvas.Font]*basefont.Outliner)
}

// forkSharedState 将Canvas后端的线程安全共享缓存传递给Fork子渲染器
// 入参: src 源渲染器, dst 目标子渲染器
func (CanvasBackend) forkSharedState(src, dst *Renderer) {
	dst.sharedBackendStates[CanvasBackend{}] = src.canvasFontData()
	dst.sharedBackendStates[canvasOutlineCacheKey{}] = src.canvasFontOutlines()
	dst.sharedBackendStates[canvasClipUnionKey{}] = src.canvasClipUnions()
	dst.sharedBackendStates[canvasPathCacheKey{}] = src.canvasParsedPaths()
	dst.sharedBackendStates[canvasClipCacheKey{}] = src.canvasClipIntersections()
	dst.sharedBackendStates[canvasFillCacheKey{}] = src.canvasSettledPaths()
	dst.sharedBackendStates[canvasDrawClipCacheKey{}] = src.canvasDrawClips()
	dst.sharedBackendStates[canvasStrokeCacheKey{}] = src.canvasStrokeClips()
	dst.sharedBackendStates[svgResourceCacheKey{}] = src.svgImageResources()
	dst.sharedBackendStates[svgFontCacheKey{}] = src.svgFontResources()
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
	cache := resourceCache[K, V](r.Reader.resources, limit)
	r.sharedBackendStates[key] = cache
	return cache
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
	return sharedCanvasCache[[32]byte, []byte](r, CanvasBackend{}, 32<<20)
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

// strokeClippedCanvasPath 复用原生描边与裁剪结果，自定义几何后端沿用逐次调用
// 入参: path 绘制路径, clip 裁剪路径, options 描边样式, fill 是否允许重叠填充轮廓
// 返回: *canvas.Path 可独立变换的描边轮廓
func (r *Renderer) strokeClippedCanvasPath(path, clip *canvas.Path, options StrokeOptions, fill bool) *canvas.Path {
	geometry, err := r.Geometry()
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	render := func() (*canvas.Path, int, error) {
		stroke := canvasStrokePath
		if fill && !r.compactStrokes {
			stroke = canvasStrokeFillPath
		}
		result, err := stroke(geometry, path, options)
		if err != nil {
			return nil, 0, err
		}
		if fill {
			result = applyFillClipPath(result, clip)
		} else {
			result = applyClipPath(result, clip)
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
		paths := []*canvas.Path{path}
		if clip != nil {
			paths = append(paths, clip)
		}
		key := canvasStrokeKey{digest: canvasGeometryDigest(paths...), options: string(encoded), fill: fill, compact: r.compactStrokes}
		result, err = r.canvasStrokeClips().load(r.pageContext, key, render)
		if err == nil {
			return result.Copy()
		}
	} else {
		result, _, err = render()
	}
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	return result
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

// canvasState 获取当前渲染器的Canvas私有缓存，按需创建且不向其他后端暴露
// 返回: *canvasBackendState Canvas缓存
func (r *Renderer) canvasState() *canvasBackendState {
	key := CanvasBackend{}
	if state, ok := r.backendStates[key]; ok {
		return state.(*canvasBackendState)
	}
	state := &canvasBackendState{
		resources: r.Reader.resources,
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
