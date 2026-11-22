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

package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// imageValueBudget 限制图像描述的检查深度和保留字节数
type imageValueBudget struct {
	nodes int
	bytes int
}

// ImageDigest 对无间接依赖的小图像生成编码摘要，超预算时不参与内容缓存
// 入参: ctx 取消上下文, source 图像描述
// 返回: [32]byte 编码摘要, bool 是否可复用, error 取消错误
func ImageDigest(ctx context.Context, source *pdfgo.Image) ([32]byte, bool, error) {
	if source == nil || source.Stream == nil || len(source.Stream.Data) > 1<<20 {
		return [32]byte{}, false, nil
	}
	budget := imageValueBudget{nodes: 256, bytes: 65536}
	for _, value := range []pdfgo.Object{source.Stream.Dictionary, source.ColorSpace, source.Decode, source.Mask, source.SoftMask} {
		if !imageDirectValue(value, &budget) {
			return [32]byte{}, false, ctx.Err()
		}
	}
	hash := sha256.New()
	data := source.Stream.Data
	for len(data) != 0 {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, false, err
		}
		n := min(len(data), 65536)
		hash.Write(data[:n])
		data = data[n:]
	}
	var result [32]byte
	hash.Sum(result[:0])
	return result, true, ctx.Err()
}

// SameImage 核对完整编码与有效图像属性，不以摘要相同代替内容相同
// 入参: left 已缓存图像, right 待复用图像，二者须包含有效编码流
// 返回: bool 是否可使用同一图像资源
func SameImage(left, right *pdfgo.Image) bool {
	return left.Width == right.Width && left.Height == right.Height && left.BitsPerComponent == right.BitsPerComponent &&
		left.ImageMask == right.ImageMask && left.Interpolate == right.Interpolate &&
		bytes.Equal(left.Stream.Data, right.Stream.Data) && reflect.DeepEqual(left.Stream.Dictionary, right.Stream.Dictionary) &&
		reflect.DeepEqual(left.ColorSpace, right.ColorSpace) && reflect.DeepEqual(left.Decode, right.Decode) &&
		reflect.DeepEqual(left.Mask, right.Mask) && reflect.DeepEqual(left.SoftMask, right.SoftMask)
}

// imageDirectValue 检查图像描述是否完全独立，拒绝间接引用和嵌套流
// 入参: value 描述值, budget 描述检查预算
// 返回: bool 是否不依赖阅读器资源
func imageDirectValue(value pdfgo.Object, budget *imageValueBudget) bool {
	if budget.nodes <= 0 || budget.bytes < 64 {
		return false
	}
	budget.nodes--
	budget.bytes -= 64
	switch v := value.(type) {
	case nil, pdfgo.Boolean, pdfgo.Integer, pdfgo.Real:
		return true
	case pdfgo.Name:
		budget.bytes -= len(v)
		return budget.bytes >= 0
	case pdfgo.String:
		budget.bytes -= len(v)
		return budget.bytes >= 0
	case pdfgo.Array:
		for _, child := range v {
			if !imageDirectValue(child, budget) {
				return false
			}
		}
		return true
	case pdfgo.Dictionary:
		for key, child := range v {
			budget.bytes -= len(key)
			if !imageDirectValue(child, budget) {
				return false
			}
		}
		return true
	}
	return false
}
