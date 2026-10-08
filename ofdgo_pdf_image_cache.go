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
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// pdfImageContentKey 区分编码内容、渲染意图、原生输出空间、模板填充色及有损采样尺寸
type pdfImageContentKey struct {
	data    [32]byte
	intent  pdfgo.Name
	process pdfgo.Name
	tint    color.NRGBA64
	size    image.Point
}

// pdfImageContent 保存不依赖间接资源的图像描述及已注册编号
type pdfImageContent struct {
	source *pdfgo.Image
	id     string
}

// pdfImageValueBudget 限制图像描述的检查深度和保留字节数
type pdfImageValueBudget struct {
	nodes int
	bytes int
}

// imageContentKey 对无间接依赖的小图像生成内容键，超大图像仍沿用流缓存
// 入参: source 图像描述, key 流缓存键
// 返回: pdfImageContentKey 内容键, bool 是否可复用, error 取消错误
func (p *pdfImporter) imageContentKey(source *pdfgo.Image, key pdfImageKey) (pdfImageContentKey, bool, error) {
	if source == nil || source.Stream == nil || len(source.Stream.Data) > 1<<20 {
		return pdfImageContentKey{}, false, nil
	}
	budget := pdfImageValueBudget{nodes: 256, bytes: 65536}
	for _, value := range []pdfgo.Object{source.Stream.Dictionary, source.ColorSpace, source.Decode, source.Mask, source.SoftMask} {
		if !pdfImageDirectValue(value, &budget) {
			return pdfImageContentKey{}, false, p.ctx.Err()
		}
	}
	hash := sha256.New()
	data := source.Stream.Data
	for len(data) != 0 {
		if err := p.ctx.Err(); err != nil {
			return pdfImageContentKey{}, false, err
		}
		n := min(len(data), 65536)
		hash.Write(data[:n])
		data = data[n:]
	}
	result := pdfImageContentKey{intent: key.intent, process: key.process, tint: key.tint, size: key.size}
	hash.Sum(result.data[:0])
	return result, true, p.ctx.Err()
}

// pdfImageDirectValue 检查图像描述是否完全独立，拒绝间接引用和嵌套流
// 入参: value 描述值, budget 描述检查预算
// 返回: bool 是否不依赖阅读器资源
func pdfImageDirectValue(value pdfgo.Object, budget *pdfImageValueBudget) bool {
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
			if !pdfImageDirectValue(child, budget) {
				return false
			}
		}
		return true
	case pdfgo.Dictionary:
		for key, child := range v {
			budget.bytes -= len(key)
			if !pdfImageDirectValue(child, budget) {
				return false
			}
		}
		return true
	}
	return false
}

// matches 核对完整编码与有效图像属性，不以摘要相同代替内容相同
// 入参: source 待复用图像
// 返回: bool 是否可使用同一图像资源
func (c pdfImageContent) matches(source *pdfgo.Image) bool {
	s := c.source
	return s.Width == source.Width && s.Height == source.Height && s.BitsPerComponent == source.BitsPerComponent &&
		s.ImageMask == source.ImageMask && s.Interpolate == source.Interpolate &&
		bytes.Equal(s.Stream.Data, source.Stream.Data) && reflect.DeepEqual(s.Stream.Dictionary, source.Stream.Dictionary) &&
		reflect.DeepEqual(s.ColorSpace, source.ColorSpace) && reflect.DeepEqual(s.Decode, source.Decode) &&
		reflect.DeepEqual(s.Mask, source.Mask) && reflect.DeepEqual(s.SoftMask, source.SoftMask)
}
