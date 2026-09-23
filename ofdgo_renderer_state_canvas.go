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

// canvasBackendState 保存Canvas字体、字形和编码图片适配缓存
type canvasBackendState struct {
	images             map[*EncodedImage]image.Image
	fontFamily         *canvas.FontFamily
	defaultFontLoaded  bool
	fontMap            map[string]*canvas.FontFamily
	fontCache          map[fontCacheKey]*canvas.FontFamily
	svgFontCache       map[*canvas.Font]SVGFont
	fontSourceCache    map[string][]fontSource
	fontSourceUsed     map[string]fontSource
	fontDirCandidates  map[string][]fontFileCandidate
	fontFSCandidates   map[int][]fontFileCandidate
	textGlyphPathCache map[textGlyphPathCacheKey]textGlyphPathCacheValue
}

// canvasState 获取当前渲染器的Canvas私有缓存，按需创建且不向其他后端暴露
// 返回: *canvasBackendState Canvas缓存
func (r *Renderer) canvasState() *canvasBackendState {
	key := CanvasBackend{}
	if state, ok := r.backendStates[key]; ok {
		return state.(*canvasBackendState)
	}
	state := &canvasBackendState{
		images:             make(map[*EncodedImage]image.Image),
		fontMap:            make(map[string]*canvas.FontFamily),
		fontCache:          make(map[fontCacheKey]*canvas.FontFamily),
		svgFontCache:       make(map[*canvas.Font]SVGFont),
		fontSourceCache:    make(map[string][]fontSource),
		fontSourceUsed:     make(map[string]fontSource),
		fontDirCandidates:  make(map[string][]fontFileCandidate),
		fontFSCandidates:   make(map[int][]fontFileCandidate),
		textGlyphPathCache: make(map[textGlyphPathCacheKey]textGlyphPathCacheValue),
	}
	r.backendStates[key] = state
	return state
}
