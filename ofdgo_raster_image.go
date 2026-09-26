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

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// drawRasterImage 按页面变换对图像执行高质量采样
// 入参: dst 目标图像, src 源图像, m 像素坐标变换
func drawRasterImage(dst draw.Image, src image.Image, m RasterMatrix) {
	draw.CatmullRom.Transform(dst, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, src, src.Bounds(), draw.Over, nil)
}

// drawRasterImageCommand 使用当前后端生成裁剪蒙版，共用高质量图像采样
// 入参: backend 当前后端, page 页面, dst 目标图像, src 原始图像, command 图像指令, cache 蒙版缓存
// 返回: error 裁剪绘制错误
func drawRasterImageCommand(backend RasterBackend, page *RasterPage, dst draw.Image, src image.Image, command RasterCommand, cache *renderCache[[32]byte, *image.Alpha]) error {
	m := page.PixelTransform(command.Transform, dst.Bounds().Dy())
	if command.Clip == nil {
		drawRasterImage(dst, src, m)
		return nil
	}
	mask, err := rasterClipMask(backend, page, command.Clip, cache)
	if err != nil {
		return err
	}
	if mask.Rect.Empty() {
		return nil
	}
	draw.CatmullRom.Transform(dst, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, imagePixelSource(src), src.Bounds(), draw.Over, &draw.Options{DstMask: mask})
	return nil
}

// PixelTransform 将页面毫米变换转换为像素变换，保留尺寸取整后的原点位置
// 入参: m 局部到页面变换, height 目标像素高度
// 返回: RasterMatrix 局部到像素变换
func (p *RasterPage) PixelTransform(m RasterMatrix, height int) RasterMatrix {
	scale := p.DPI / 25.4
	for i := range m {
		m[i] *= scale
	}
	m[5] += float64(height) - p.Height*scale
	return m
}
