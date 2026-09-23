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

import "github.com/tdewolff/canvas"

// loadFont 加载字体
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
	of := r.Reader.fontCache[fontID]
	if len(resolved.Data) == 0 {
		return nil
	}
	if of == nil {
		of = &Font{ID: fontID, FontName: "default"}
	}
	fontData := resolved.Data
	fontStyle := canvasFontStyle(of)
	ff := canvas.NewFontFamily(of.FontName)
	if err := ff.LoadFont(fontData, 0, fontStyle); err != nil {
		r.renderError = err
		return nil
	}
	r.canvasState().fontMap[fontID] = ff
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
