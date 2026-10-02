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

import "github.com/xiaoqidun/pdfgo"

// halftone 检查连续色调转换，避免将打印网屏误用于屏幕显示
// 入参: style 图形状态
// 返回: error 严格模式无法保留设备网屏
func (p *pdfImporter) halftone(style pdfgo.Style) error {
	h := style.Halftone
	if h == nil || h.Type == 0 {
		return nil
	}
	if p.warning == nil {
		return &pdfgo.UnsupportedError{Feature: "device halftone conversion"}
	}
	if p.halftoneWarnings == nil {
		p.halftoneWarnings = make(map[*pdfgo.Halftone]bool)
	}
	if !p.halftoneWarnings[h] {
		p.halftoneWarnings[h] = true
		p.warning(pdfgo.Diagnostic{Message: "PDF continuous-tone appearance retained; device halftone not transferred to OFD"})
	}
	return nil
}
