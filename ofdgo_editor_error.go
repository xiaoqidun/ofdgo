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

import "errors"

// EditReason 编辑操作受限的原因，不依赖错误描述的语言或文案
type EditReason string

const (
	// EditUnsupportedObject 对象包含尚不支持编辑的特性
	EditUnsupportedObject EditReason = "unsupportedObject"
	// EditUnsupportedContainer 所在容器包含尚不支持编辑的特性
	EditUnsupportedContainer EditReason = "unsupportedContainer"
	// EditUnsupportedColor 颜色表达方式暂不支持编辑
	EditUnsupportedColor EditReason = "unsupportedColor"
	// EditUnsupportedStyle 绘制样式暂不支持编辑
	EditUnsupportedStyle EditReason = "unsupportedStyle"
	// EditFontUnavailable 对象所需字体不可用
	EditFontUnavailable EditReason = "fontUnavailable"
	// EditMissingGlyphs 字体缺少文字所需字形
	EditMissingGlyphs EditReason = "missingGlyphs"
	// EditInvalidObject 对象数据未通过校验
	EditInvalidObject EditReason = "invalidObject"
)

// EditError 编辑受限原因及原始错误，可通过errors.As取得Code和更具体的诊断
type EditError struct {
	Code EditReason
	Err  error
}

// Error 保留原始错误描述
// 返回: string 错误描述
func (e *EditError) Error() string {
	return e.Err.Error()
}

// Unwrap 保留原始错误链
// 返回: error 原始错误
func (e *EditError) Unwrap() error {
	return e.Err
}

// editReason 提取编辑错误分类，未分类的校验错误作为对象数据异常
// 入参: err 校验错误
// 返回: EditReason 原因
func editReason(err error) EditReason {
	var missing *MissingGlyphError
	if errors.As(err, &missing) {
		return EditMissingGlyphs
	}
	var failure *EditError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return EditInvalidObject
}
