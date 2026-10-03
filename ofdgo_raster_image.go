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

// rasterScaleBufferLimit 限制整像素缩放器的浮点中间缓冲，超出时使用逐行采样
const rasterScaleBufferLimit = 8 << 20

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

// drawRasterImage 按页面变换对图像执行高质量采样
// 入参: dst 目标图像, src 源图像, m 像素坐标变换
func drawRasterImage(dst draw.Image, src image.Image, m RasterMatrix) {
	drawRasterImagePixels(dst, src, m, draw.Over)
}

// drawRasterImagePixels 对轴向缩小使用可分离采样，其余变换保留通用路径
// 入参: dst 目标图像, src 源图像, m 像素变换, op 合成操作
func drawRasterImagePixels(dst draw.Image, src image.Image, m RasterMatrix, op draw.Op) {
	b := src.Bounds()
	if dr, ok := rasterScaleBounds(b, m); ok {
		if dr.Intersect(dst.Bounds()).Empty() {
			return
		}
		draw.CatmullRom.Scale(dst, dr, src, b, op, nil)
		return
	}
	if drawRasterAxisImage(dst, src, m, op) {
		return
	}
	draw.CatmullRom.Transform(dst, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, src, b, op, nil)
}

// rasterScaleBounds 检查变换是否可无几何取整地使用缩放器，并限制其中间缓冲
// 入参: b 源边界, m 像素变换
// 返回: image.Rectangle 缩放边界, bool 是否适用
func rasterScaleBounds(b image.Rectangle, m RasterMatrix) (image.Rectangle, bool) {
	if b.Empty() || m[1] != 0 || m[2] != 0 || m[0] <= 0 || m[0] > 1 || m[3] <= 0 || m[3] > 1 || m[0] == 1 && m[3] == 1 {
		return image.Rectangle{}, false
	}
	x0, y0 := float64(m[0]*float64(b.Min.X))+m[4], float64(m[3]*float64(b.Min.Y))+m[5]
	x1, y1 := float64(m[0]*float64(b.Max.X))+m[4], float64(m[3]*float64(b.Max.Y))+m[5]
	for _, v := range [4]float64{x0, y0, x1, y1} {
		if v != math.Floor(v) || v < math.MinInt32 || v > math.MaxInt32 {
			return image.Rectangle{}, false
		}
	}
	if x1 <= x0 || y1 <= y0 || b.Dx() > math.MaxInt32 || b.Dy() > math.MaxInt32 || (x1-x0)*float64(b.Dy()) > rasterScaleBufferLimit/32 {
		return image.Rectangle{}, false
	}
	return image.Rect(int(x0), int(y0), int(x1), int(y1)), true
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
	drawRasterImagePixels(layer, imagePixelSource(src), m, draw.Src)
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
