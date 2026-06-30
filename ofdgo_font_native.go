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

//go:build !js || !wasm

package ofdgo

import "github.com/tdewolff/canvas"

// loadDefaultFonts 加载默认字体
// 返回: bool 是否加载成功
func (r *Renderer) loadDefaultFonts() bool {
	for _, name := range fontDefaultSystemNames() {
		if err := r.fontFamily.LoadSystemFont(name, canvas.FontRegular); err == nil {
			return true
		}
	}
	return false
}

// canLoadSystemFonts 判断是否可以加载系统字体
// 返回: bool 是否可以加载系统字体
func canLoadSystemFonts() bool {
	return true
}
