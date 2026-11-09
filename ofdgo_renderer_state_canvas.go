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

// canvasBackendState 保存Canvas字体、字形和编码图片适配缓存
type canvasBackendState struct {
	resources          *Resources
	images             renderCache[*EncodedImage, image.Image]
	fontMap            map[string]*canvas.FontFamily
	svgFontCache       map[*canvas.Font]SVGFont
	textGlyphPathCache renderCache[textGlyphPathCacheKey, textGlyphPathCacheValue]
	fontOutlines       map[*canvas.Font]*basefont.Outliner
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
	cache := resourceCache[[32]byte, *canvas.Path](r.Reader.resources, 8<<20)
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasClipUnions 复用相同输入轮廓的裁剪并集，不改变布尔运算顺序
// 返回: *renderCache[[32]byte, *canvas.Path] 只读裁剪并集缓存
func (r *Renderer) canvasClipUnions() *renderCache[[32]byte, *canvas.Path] {
	key := canvasClipUnionKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[[32]byte, *canvas.Path])
	}
	if r.sharedBackendStates == nil {
		r.sharedBackendStates = make(map[any]any)
	}
	cache := resourceCache[[32]byte, *canvas.Path](r.Reader.resources, 8<<20)
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
	cache := resourceCache[canvasPathKey, *canvas.Path](r.Reader.resources, 16<<20)
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
	cache := resourceCache[canvasFillKey, *canvas.Path](r.Reader.resources, 16<<20)
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
	cache := resourceCache[canvasFontKey, *canvas.FontFamily](r.Reader.resources, 32<<20)
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasDrawClips 获取派生渲染器共享的绘制裁剪缓存
// 返回: *renderCache 绘制裁剪缓存
func (r *Renderer) canvasDrawClips() *renderCache[[32]byte, *canvas.Path] {
	key := canvasDrawClipCacheKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[[32]byte, *canvas.Path])
	}
	cache := resourceCache[[32]byte, *canvas.Path](r.Reader.resources, 16<<20)
	r.sharedBackendStates[key] = cache
	return cache
}

// canvasStrokeClips 获取原生描边与裁剪结果缓存，按资源策略限制保留量
// 返回: *renderCache[canvasStrokeKey, *canvas.Path] 描边缓存
func (r *Renderer) canvasStrokeClips() *renderCache[canvasStrokeKey, *canvas.Path] {
	key := canvasStrokeCacheKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[canvasStrokeKey, *canvas.Path])
	}
	cache := resourceCache[canvasStrokeKey, *canvas.Path](r.Reader.resources, 16<<20)
	r.sharedBackendStates[key] = cache
	return cache
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
		if fill {
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
		key := canvasStrokeKey{digest: canvasGeometryDigest(paths...), options: string(encoded), fill: fill}
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
// 返回: *renderCache[*canvas.Font, *basefont.Outliner] 字体轮廓缓存
func (r *Renderer) canvasFontOutlines() *renderCache[*canvas.Font, *basefont.Outliner] {
	key := canvasOutlineCacheKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[*canvas.Font, *basefont.Outliner])
	}
	cache := resourceCache[*canvas.Font, *basefont.Outliner](r.Reader.resources, 32<<20)
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
		resources: r.Reader.resources,
		images:    *resourceCache[*EncodedImage, image.Image](r.Reader.resources, imageCacheLimit),
	}
	state.resetFonts()
	r.backendStates[key] = state
	return state
}

// resetFonts 清除Canvas字体及字形缓存，保留编码图片适配对象
func (s *canvasBackendState) resetFonts() {
	s.textGlyphPathCache.clear()
	s.fontMap = make(map[string]*canvas.FontFamily)
	s.svgFontCache = make(map[*canvas.Font]SVGFont)
	s.textGlyphPathCache = *resourceCache[textGlyphPathCacheKey, textGlyphPathCacheValue](s.resources, 16<<20)
	s.fontOutlines = make(map[*canvas.Font]*basefont.Outliner)
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
