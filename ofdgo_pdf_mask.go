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
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// rasterMaskAllowed 仅对不可矢量表达的蒙版启用显式局部合成，格式错误仍返回
// 入参: err 蒙版转换错误
// 返回: bool 是否允许局部合成
func (p *pdfImporter) rasterMaskAllowed(err error) bool {
	var unsupported *pdfgo.UnsupportedError
	return p.warning != nil && errors.As(err, &unsupported)
}

// applySoftMask 按原始颜色分量计算蒙版，逐块应用透明度及亮度传递
// 入参: source 源图像, box 页面区域, mask PDF软蒙版
// 返回: image.Image 合成图像, error 蒙版解释或渲染错误
func (p *pdfImporter) applySoftMask(source image.Image, box Box, mask *pdfgo.SoftMask) (image.Image, error) {
	inverse, ok := p.matrix.Inverse()
	if !ok {
		return nil, fmt.Errorf("invalid PDF mask transform")
	}
	out := image.NewNRGBA(source.Bounds())
	w, h := out.Rect.Dx(), out.Rect.Dy()
	step := 25.4 / p.rasterDPI
	cache := p.compositingCache()
	for y := 0; y < h; y += 256 {
		for x := 0; x < w; x += 256 {
			width, height := min(256, w-x), min(256, h-y)
			c := pdfCompositor{importer: p, box: Box{X: box.X + float64(x)*step, Y: box.Y + float64(y)*step, W: float64(width) * step, H: float64(height) * step}, width: width, height: height, inverse: inverse, cache: cache, masks: map[*pdfgo.SoftMask][]float64{}}
			alpha, err := c.mask(mask, &pdfgo.ColorSpace{Model: "DeviceRGB"})
			if err != nil {
				return nil, err
			}
			for i, opacity := range alpha {
				px, py := out.Rect.Min.X+x+i%width, out.Rect.Min.Y+y+i/width
				pixel := color.NRGBAModel.Convert(source.At(px, py)).(color.NRGBA)
				pixel.A = uint8(math.Round(float64(pixel.A) * opacity))
				out.SetNRGBA(px, py, pixel)
			}
		}
	}
	return out, nil
}
