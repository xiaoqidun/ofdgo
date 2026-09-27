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
	bounds := mask.Rect.Intersect(dst.Bounds()).Intersect(rasterImageBounds(src.Bounds(), m, dst.Bounds()))
	if bounds.Empty() {
		return nil
	}
	layer := image.NewRGBA64(bounds)
	draw.CatmullRom.Transform(layer, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, imagePixelSource(src), src.Bounds(), draw.Src, nil)
	draw.DrawMask(dst, bounds, layer, bounds.Min, mask, bounds.Min, draw.Over)
	return nil
}

// rasterImageBounds 计算采样器可能写入的像素范围，并在整数转换前裁至目标边界
// 入参: source 原始像素边界, m 像素变换, target 目标边界
// 返回: image.Rectangle 受影响的目标像素区域
func rasterImageBounds(source image.Rectangle, m RasterMatrix, target image.Rectangle) image.Rectangle {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range [4]image.Point{source.Min, {X: source.Max.X, Y: source.Min.Y}, {X: source.Min.X, Y: source.Max.Y}, source.Max} {
		x := math.Floor(float64(m[0]*float64(p.X)) + float64(m[2]*float64(p.Y)) + m[4])
		y := math.Floor(float64(m[1]*float64(p.X)) + float64(m[3]*float64(p.Y)) + m[5])
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x+1), math.Max(maxY, y+1)
	}
	minX, minY = math.Max(minX, float64(target.Min.X)), math.Max(minY, float64(target.Min.Y))
	maxX, maxY = math.Min(maxX, float64(target.Max.X)), math.Min(maxY, float64(target.Max.Y))
	if minX >= maxX || minY >= maxY {
		return image.Rectangle{}
	}
	return image.Rect(int(minX), int(minY), int(maxX), int(maxY))
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
