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

// FontResource 保存独立字体数据、标准元信息与只读度量，不包含绘图库类型
type FontResource struct {
	FontMetrics
	Data      []byte
	Font      Font
	Extension string
	CanSubset bool
}

// FontUsage 描述全包已确认的字体引用，Unsafe禁止裁剪
type FontUsage struct {
	Characters []rune
	Glyphs     []uint16
	Unsafe     bool
}

// FontResourceBackend 解析和裁剪保存资源，独立于显示字体后端
// SubsetFont的preserve为true时不得改变字形编号，SubsetSourceFont无安全收益时返回nil
type FontResourceBackend interface {
	Backend
	OpenFontResource(file FontFile, index int) (*FontResource, error)
	SubsetFont(data []byte, glyphs []uint16, preserve bool) ([]byte, error)
	SubsetSourceFont(data []byte, usage FontUsage) ([]byte, error)
}
