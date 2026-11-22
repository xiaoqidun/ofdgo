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

package image

import (
	"context"
	"image"
	"image/color"
)

// maskedTIFFImage 将独立透明蒙版叠加到主图，复用两个目录的只读像素
type maskedTIFFImage struct {
	source, mask image.Image
	bounds       image.Rectangle
}

// DecodeTIFF 解码主图及透明蒙版，保留颜色精度与高分辨率蒙版
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 主图布局
// 返回: image.Image 图像, error 解码或取消错误
func DecodeTIFF(ctx context.Context, data []byte, layout TIFFLayout) (image.Image, error) {
	source, err := decodeTIFFPixels(ctx, data, layout)
	if err != nil || layout.Transparency == nil {
		return source, err
	}
	mask, err := decodeTIFFPixels(ctx, data, *layout.Transparency)
	if err != nil {
		return nil, err
	}
	return &maskedTIFFImage{source: source, mask: mask, bounds: image.Rectangle{Max: layout.DisplaySize()}}, nil
}

// Bounds 返回主图和蒙版共同覆盖的像素区域
// 返回: image.Rectangle 显示区域
func (s *maskedTIFFImage) Bounds() image.Rectangle { return s.bounds }

// ColorModel 返回保留原图采样精度的非预乘颜色模型
// 返回: color.Model 颜色模型
func (s *maskedTIFFImage) ColorModel() color.Model { return color.NRGBA64Model }

// NRGBA64At 按像素中心读取主图及蒙版，零位隐藏原图并保留隐藏颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.NRGBA64 非预乘十六位颜色
func (s *maskedTIFFImage) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.bounds) {
		return color.NRGBA64{}
	}
	source := s.samplePoint(s.source.Bounds(), x, y)
	pixel := NRGBA64At(s.source, source.X, source.Y)
	mask := s.samplePoint(s.mask.Bounds(), x, y)
	if NRGBA64At(s.mask, mask.X, mask.Y).R == 0 {
		pixel.A = 0
	}
	return pixel
}

// At 返回非预乘颜色，区域外透明
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 像素颜色
func (s *maskedTIFFImage) At(x, y int) color.Color { return s.NRGBA64At(x, y) }

// RGBA64At 返回预乘十六位颜色，供采样器直接读取
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA64 预乘颜色
func (s *maskedTIFFImage) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := s.NRGBA64At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}

// samplePoint 将显示坐标映射到主图或蒙版的像素中心
// 入参: bounds 采样图像范围, x 横坐标, y 纵坐标
// 返回: image.Point 采样坐标
func (s *maskedTIFFImage) samplePoint(bounds image.Rectangle, x, y int) image.Point {
	size := s.bounds.Size()
	x, y = x-s.bounds.Min.X, y-s.bounds.Min.Y
	if bounds.Size() == size {
		return bounds.Min.Add(image.Pt(x, y))
	}
	return image.Pt(
		bounds.Min.X+int((uint64(x)*uint64(bounds.Dx())+uint64(bounds.Dx()/2))/uint64(size.X)),
		bounds.Min.Y+int((uint64(y)*uint64(bounds.Dy())+uint64(bounds.Dy()/2))/uint64(size.Y)),
	)
}

// alphaAt 读取蒙版与主图透明度，不解算主图颜色
// 入参: x 横坐标, y 纵坐标
// 返回: uint32 十六位透明度
func (s *maskedTIFFImage) alphaAt(x, y int) uint32 {
	if !image.Pt(x, y).In(s.bounds) {
		return 0
	}
	mask := s.samplePoint(s.mask.Bounds(), x, y)
	if NRGBA64At(s.mask, mask.X, mask.Y).R == 0 {
		return 0
	}
	source := s.samplePoint(s.source.Bounds(), x, y)
	return AlphaAt(s.source, source.X, source.Y)
}
