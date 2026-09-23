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
	"image"
	"math"

	"github.com/gogpu/gg"
)

// ggNativeImage 判断GG最近邻采样与公共高质量采样是否等价
// 仅接受整数像素平移及不会重复预乘的八位图片，缩放、错切和旋转沿用公共路径
// 入参: src 源像素, matrix 像素变换
// 返回: bool 是否可使用原生图片
func ggNativeImage(src image.Image, matrix RasterMatrix) bool {
	if matrix[0] != 1 || matrix[1] != 0 || matrix[2] != 0 || matrix[3] != 1 ||
		!finite(matrix[4]) || !finite(matrix[5]) || matrix[4] != math.Trunc(matrix[4]) || matrix[5] != math.Trunc(matrix[5]) || src.Bounds().Empty() {
		return false
	}
	switch src := src.(type) {
	case *image.NRGBA, *image.Gray:
		return true
	case *image.RGBA:
		return src.Opaque()
	}
	return false
}

// ggPrepareImage 复用GG图片采样器和裁剪，不能等价采样时保留公共图像指令
// 工厂方法只构造采样器，不创建上下文设备或调用可能隐藏错误的DrawImageEx
// 入参: page 页面, command 图片指令, masks 裁剪缓存, images 图片适配缓存
// 返回: ggRasterCommand 预编译图片, error 裁剪错误
func ggPrepareImage(page *RasterPage, command RasterCommand, masks *renderCache[[32]byte, *image.Alpha], images *renderCache[image.Image, *gg.ImageBuf]) (ggRasterCommand, error) {
	result := ggRasterCommand{command: command}
	_, height, _ := page.PixelSize()
	matrix := page.PixelTransform(command.Transform, height)
	src := imagePixelSource(command.Image)
	if !ggNativeImage(src, matrix) {
		return result, nil
	}
	buffer, ok := images.get(src)
	if !ok {
		buffer = gg.ImageBufFromImage(src)
		images.put(src, buffer, src.Bounds().Dx()*src.Bounds().Dy()*4+256)
	}
	bounds := src.Bounds()
	x, y := matrix[4]+float64(bounds.Min.X), matrix[5]+float64(bounds.Min.Y)
	pattern := new(gg.Context).CreateImagePattern(buffer, 0, 0, bounds.Dx(), bounds.Dy()).(*gg.ImagePattern)
	pattern.SetClamp(true)
	pattern.SetTransform(gg.Translate(x, y))
	paint := gg.NewPaint()
	paint.SetFillBrush(gg.BrushFromPattern(pattern))
	if command.Clip != nil {
		mask, err := rasterClipMask(GGBackend{}, page, command.Clip, masks)
		if err != nil {
			return result, err
		}
		if mask.Rect.Empty() {
			result.path, result.paint = gg.NewPath(), paint
			return result, nil
		}
		paint.ClipMask, paint.ClipMaskW, paint.ClipMaskH = mask.Pix, mask.Stride, mask.Rect.Dy()
		paint.ClipMaskX, paint.ClipMaskY = mask.Rect.Min.X, mask.Rect.Min.Y
	}
	path := gg.NewPath()
	path.MoveTo(x, y)
	path.LineTo(x+float64(bounds.Dx()), y)
	path.LineTo(x+float64(bounds.Dx()), y+float64(bounds.Dy()))
	path.LineTo(x, y+float64(bounds.Dy()))
	path.Close()
	result.path, result.paint = path, paint
	return result, nil
}
