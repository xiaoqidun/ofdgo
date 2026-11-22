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
	"unicode/utf8"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// FontFace 字体文件中的字体名称与零起始索引，不依赖文件名
// Family和Style保留排版族，MenuFamily和MenuStyle提供兼容四款式的菜单名称
// FamilyKey用于同来源内的族关联，Bold和Italic表示字体固有样式
type FontFace struct {
	Index          int      `json:"index"`
	FullName       string   `json:"fullName"`
	Family         string   `json:"family"`
	Style          string   `json:"style"`
	PostScriptName string   `json:"postscriptName"`
	Names          []string `json:"names"`
	MenuFamily     string   `json:"menuFamily"`
	MenuStyle      string   `json:"menuStyle"`
	FamilyKey      string   `json:"familyKey"`
	Bold           bool     `json:"bold"`
	Italic         bool     `json:"italic"`
}

// fromBaseFontFaces 将内部字体名称列表转换为公开字体名称列表
// 入参: faces 内部字体列表
// 返回: []FontFace 公开字体列表
func fromBaseFontFaces(faces []basefont.Face) []FontFace {
	if faces == nil {
		return nil
	}
	out := make([]FontFace, len(faces))
	for i, face := range faces {
		out[i] = FontFace(face)
	}
	return out
}

// Faces 读取字体名称表，集合文件按原顺序列出各项，不解析字形轮廓
// 返回: []FontFace 字体列表, error 格式或名称表错误
func (f FontFile) Faces() ([]FontFace, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	faces, err := basefont.ReadFaces(data)
	return fromBaseFontFaces(faces), err
}

// LocalizedFaces 读取指定语言的显示名称，保留PostScript名称、全部别名和集合索引
// 未找到对应名称时沿用Faces的名称选择规则，不修改字体数据
// 入参: platform OpenType名称表平台标识, language 该平台的语言标识
// 返回: []FontFace 字体列表, error 格式或名称表错误
func (f FontFile) LocalizedFaces(platform, language uint16) ([]FontFace, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	faces, err := basefont.ReadLocalizedFaces(data, platform, language)
	return fromBaseFontFaces(faces), err
}

// Face 提取指定字体，返回独立的OpenType数据，可用于预览或AddFont，且不修改原文件
// 入参: index 零起始字体索引，非集合文件只能为0
// 返回: []byte 独立字体数据, error 格式或索引错误
func (f FontFile) Face(index int) ([]byte, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	return basefont.ExtractFace(data, index)
}

// MissingGlyphs 检查指定字体的字符覆盖，不注册资源或修改文档，忽略段落换行
// 入参: index 零起始字体索引, text 待检查文字
// 返回: string 按首次出现顺序去重的缺失字符, error 字体或文字格式错误
func (f FontFile) MissingGlyphs(index int, text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("text must be UTF-8")
	}
	data, err := f.Face(index)
	if err != nil {
		return "", err
	}
	return basefont.MissingGlyphs(data, text)
}
