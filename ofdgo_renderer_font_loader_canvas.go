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

	"github.com/tdewolff/canvas"
)

// loadFont 加载字体，绘图别名按数据和样式隔离，不改动原始字体名称
// 入参: fontID 字体ID
// 返回: *canvas.FontFamily 字体族
func (r *Renderer) loadFont(fontID string) *canvas.FontFamily {
	if ff, ok := r.canvasState().fontMap[fontID]; ok {
		return ff
	}
	resolved, err := r.PrepareFont(fontID)
	if err != nil {
		r.renderError = err
		return nil
	}
	of := r.Reader.fontDefinition(fontID)
	if len(resolved.Data) == 0 {
		return nil
	}
	if of == nil {
		of = &Font{ID: fontID, FontName: "OFDGoFallback"}
	}
	fontData := resolved.Data
	fontStyle := canvasFontStyle(of)
	if resolved.digest == ([32]byte{}) {
		resolved.digest = r.fontDigest(fontData)
	}
	key := canvasFontKey{digest: resolved.digest, name: of.FontName, style: fontStyle}
	cache := r.canvasFontFamilies()
	if ff, ok := cache.get(key); ok {
		r.canvasState().fontMap[fontID] = ff
		return ff
	}
	ff := canvas.NewFontFamily(fmt.Sprintf("ofdgo-%x-%d", resolved.digest, fontStyle))
	canvasFontLoadMu.Lock()
	err = ff.LoadFont(fontData, 0, fontStyle)
	canvasFontLoadMu.Unlock()
	if err != nil {
		r.renderError = err
		return nil
	}
	r.canvasState().fontMap[fontID] = ff
	cache.put(key, ff, min(len(fontData), cache.limit)*8+256)
	return ff
}

// canvasFontStyle 获取Canvas字体样式
// 入参: font OFD字体定义
// 返回: canvas.FontStyle Canvas字体样式
func canvasFontStyle(font *Font) canvas.FontStyle {
	var style canvas.FontStyle
	if font.Bold {
		style |= canvas.FontBold
	}
	if font.Italic {
		style |= canvas.FontItalic
	}
	return style
}
