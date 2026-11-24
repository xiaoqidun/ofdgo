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
	"image"
	"image/color"
	"math"
	"runtime"
	"slices"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// rasterScaleBufferLimit 限制整像素缩放器的浮点中间缓冲，超出时使用逐行采样
const rasterScaleBufferLimit = 8 << 20

// rasterAxisSample 保存目标像素及其归一化源采样权重
type rasterAxisSample struct {
	pixel   int
	first   int
	weights []float64
}

// rasterImagePixel 保存未取整或裁切的预乘颜色累积值
type rasterImagePixel struct {
	r, g, b, a float64
}

// rasterImageRow 保存正在累积的目标行
type rasterImageRow struct {
	sample rasterAxisSample
	values []rasterImagePixel
}

// rasterImageSource 为通用图片提供预乘十六位采样
type rasterImageSource struct{ image.Image }

// DrawPixels 对轴向缩放使用可分离采样，其余变换保留通用路径
// 入参: dst 目标图像, src 源图像, m 按a、b、c、d、e、f排列的像素变换, op 合成操作
func DrawPixels(dst draw.Image, src image.Image, m [6]float64, op draw.Op) {
	DrawPixelsInterpolate(dst, src, m, op, nil)
}

// DrawPixelsInterpolate 按插值开关绘制图像，禁用插值放大或极小色块高倍放大时使用最近邻采样
// 入参: dst 目标图像, src 源图像, m 按a、b、c、d、e、f排列的像素变换, op 合成操作, interpolate 可选插值开关
func DrawPixelsInterpolate(dst draw.Image, src image.Image, m [6]float64, op draw.Op, interpolate *bool) {
	b := src.Bounds()
	if UseNearestNeighbor(b, m, interpolate) {
		drawNearestPixels(dst, src, b, m, op)
		return
	}
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
	src = rasterTransformSource(src, TransformBounds(b, m, dst.Bounds()))
	draw.CatmullRom.Transform(dst, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, src, b, op, nil)
}

// UseNearestNeighbor 判断当前图像绘制是否应采用最近邻放大采样
// 入参: b 源图像边界, m 像素变换矩阵, interpolate 可选插值开关
// 返回: bool 是否使用最近邻采样
func UseNearestNeighbor(b image.Rectangle, m [6]float64, interpolate *bool) bool {
	if b.Empty() || interpolate != nil && *interpolate {
		return false
	}
	sx, sy := math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3])
	if math.IsNaN(sx) || math.IsInf(sx, 0) || math.IsNaN(sy) || math.IsInf(sy, 0) {
		return false
	}
	if interpolate != nil && !*interpolate {
		return sx >= 1 && sy >= 1 && (sx > 1 || sy > 1)
	}
	return b.Dx() <= 8 && b.Dy() <= 8 && sx >= 4 && sy >= 4
}

// drawNearestPixels 使用最近邻采样完成整数轴向缩放或通用仿射图像绘制
// 入参: dst 目标图像, src 源图像, b 源边界, m 像素变换矩阵, op 合成操作
func drawNearestPixels(dst draw.Image, src image.Image, b image.Rectangle, m [6]float64, op draw.Op) {
	if TransformBounds(b, m, dst.Bounds()).Empty() {
		return
	}
	if m[1] == 0 && m[2] == 0 && m[0] > 0 && m[3] > 0 {
		x0, y0 := float64(b.Min.X)*m[0]+m[4], float64(b.Min.Y)*m[3]+m[5]
		x1, y1 := float64(b.Max.X)*m[0]+m[4], float64(b.Max.Y)*m[3]+m[5]
		if x0 == math.Floor(x0) && y0 == math.Floor(y0) && x1 == math.Floor(x1) && y1 == math.Floor(y1) && x0 >= math.MinInt32 && y0 >= math.MinInt32 && x1 <= math.MaxInt32 && y1 <= math.MaxInt32 && x1 > x0 && y1 > y0 {
			dr := image.Rect(int(x0), int(y0), int(x1), int(y1))
			if !dr.Intersect(dst.Bounds()).Empty() {
				draw.NearestNeighbor.Scale(dst, dr, src, b, op, nil)
			}
			return
		}
	}
	draw.NearestNeighbor.Transform(dst, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, src, b, op, nil)
}

// TransformBounds 计算采样器可能写入的像素范围，并在整数转换前裁至目标边界
// 入参: source 原始像素边界, m 像素变换, target 目标边界
// 返回: image.Rectangle 受影响的目标像素区域
func TransformBounds(source image.Rectangle, m [6]float64, target image.Rectangle) image.Rectangle {
	if source.Empty() || target.Empty() {
		return image.Rectangle{}
	}
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return image.Rectangle{}
		}
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range [4]image.Point{source.Min, {X: source.Max.X, Y: source.Min.Y}, {X: source.Min.X, Y: source.Max.Y}, source.Max} {
		x := math.Floor(float64(m[0]*float64(p.X)) + float64(m[2]*float64(p.Y)) + m[4])
		y := math.Floor(float64(m[1]*float64(p.X)) + float64(m[3]*float64(p.Y)) + m[5])
		if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
			return image.Rectangle{}
		}
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x+1), math.Max(maxY, y+1)
	}
	minX, minY = math.Max(minX, float64(target.Min.X)), math.Max(minY, float64(target.Min.Y))
	maxX, maxY = math.Min(maxX, float64(target.Max.X)), math.Min(maxY, float64(target.Max.Y))
	if math.IsNaN(minX) || math.IsNaN(minY) || math.IsNaN(maxX) || math.IsNaN(maxY) || minX >= maxX || minY >= maxY {
		return image.Rectangle{}
	}
	return image.Rect(int(minX), int(minY), int(maxX), int(maxY))
}

// drawRasterAxisImage 轴向缩放复用逐行采样，细轴按面积覆盖并保留镜像与小数坐标
// 入参: dst 目标图像, src 源图像, m 像素变换, op 合成操作
// 返回: bool 是否使用轴向采样
func drawRasterAxisImage(dst draw.Image, src image.Image, m [6]float64, op draw.Op) bool {
	sx, sy := math.Abs(m[0]), math.Abs(m[3])
	if m[1] != 0 || m[2] != 0 || sx == 0 || sy == 0 {
		return false
	}
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	b := src.Bounds()
	if b.Empty() {
		return true
	}
	r := TransformBounds(b, m, dst.Bounds())
	if r.Empty() {
		return true
	}
	if float64(r.Dx())*math.Min(float64(r.Dy()), math.Ceil(4*math.Max(1, sy))+2) > rasterScaleBufferLimit/32 {
		if sx*float64(b.Dx()) < 1 || sy*float64(b.Dy()) < 1 {
			return drawRasterFootprintImage(dst, src, m, op, r)
		}
		return false
	}
	if !rasterFootprintWeightsFit(b, r, m) {
		return drawRasterFootprintImage(dst, src, m, op, r)
	}
	xs := rasterImageAxis(r.Min.X, r.Max.X, b.Min.X, b.Max.X, m[0], m[4])
	ys := rasterImageAxis(r.Min.Y, r.Max.Y, b.Min.Y, b.Max.Y, m[3], m[5])
	if len(xs) == 0 || len(ys) == 0 {
		return true
	}
	if m[3] < 0 {
		slices.Reverse(ys)
	}
	pixels, ok := src.(image.RGBA64Image)
	if !ok {
		pixels = rasterImageSource{src}
	}
	width := len(xs)
	drawBand := func(band []rasterAxisSample) {
		if len(band) == 0 {
			return
		}
		horizontal := make([]rasterImagePixel, width)
		output := image.NewRGBA64(image.Rect(0, 0, width, 1))
		var active []rasterImageRow
		var free [][]rasterImagePixel
		next := 0
		for y := band[0].first; next < len(band) || len(active) != 0; y++ {
			if len(active) == 0 && y < band[next].first {
				y = band[next].first
			}
			for next < len(band) && band[next].first == y {
				var row []rasterImagePixel
				if n := len(free); n != 0 {
					row, free = free[n-1], free[:n-1]
					clear(row)
				} else {
					row = make([]rasterImagePixel, width)
				}
				active = append(active, rasterImageRow{sample: band[next], values: row})
				next++
			}
			rasterImageHorizontal(horizontal, pixels, xs, y)
			n := 0
			for _, row := range active {
				weight := row.sample.weights[y-row.sample.first]
				if weight != 0 {
					for x, value := range horizontal {
						p := &row.values[x]
						p.r += value.r * weight
						p.g += value.g * weight
						p.b += value.b * weight
						p.a += value.a * weight
					}
				}
				if y+1 == row.sample.first+len(row.sample.weights) {
					rasterImageWriteRow(dst, output, row.values, xs[0].pixel, row.sample.pixel, op)
					free = append(free, row.values)
				} else {
					active[n] = row
					n++
				}
			}
			active = active[:n]
		}
	}
	workers := 1
	if rasterParallelSafeDst(dst) && len(ys) >= 2 {
		spanX := max(xs[0].first+len(xs[0].weights), xs[len(xs)-1].first+len(xs[len(xs)-1].weights)) - min(xs[0].first, xs[len(xs)-1].first)
		srcY0 := min(ys[0].first, ys[len(ys)-1].first)
		srcY1 := max(ys[0].first+len(ys[0].weights), ys[len(ys)-1].first+len(ys[len(ys)-1].weights))
		spanY := srcY1 - srcY0
		if spanX*spanY >= 65536 {
			workers = min(runtime.GOMAXPROCS(0), 8, max(len(ys), spanY/16))
			if workers > 1 && uint64(spanY)*uint64(width)*32 <= 32<<20 {
				horiz := make([]rasterImagePixel, spanY*width)
				hWorkers := min(workers, max(1, spanY/8))
				var wg sync.WaitGroup
				for worker := range hWorkers {
					y0 := srcY0 + worker*spanY/hWorkers
					y1 := srcY0 + (worker+1)*spanY/hWorkers
					wg.Go(func() {
						for y := y0; y < y1; y++ {
							off := (y - srcY0) * width
							rasterImageHorizontal(horiz[off:off+width], pixels, xs, y)
						}
					})
				}
				wg.Wait()
				vWorkers := min(workers, len(ys))
				for worker := range vWorkers {
					lo := worker * len(ys) / vWorkers
					hi := (worker + 1) * len(ys) / vWorkers
					wg.Go(func() {
						acc := make([]rasterImagePixel, width)
						output := image.NewRGBA64(image.Rect(0, 0, width, 1))
						for _, sample := range ys[lo:hi] {
							clear(acc)
							for i, weight := range sample.weights {
								if weight == 0 {
									continue
								}
								hRow := horiz[(sample.first+i-srcY0)*width : (sample.first+i-srcY0+1)*width]
								for x, value := range hRow {
									p := &acc[x]
									p.r += value.r * weight
									p.g += value.g * weight
									p.b += value.b * weight
									p.a += value.a * weight
								}
							}
							rasterImageWriteRow(dst, output, acc, xs[0].pixel, sample.pixel, op)
						}
					})
				}
				wg.Wait()
				return true
			}
			workers = min(workers, len(ys))
		}
	}
	if workers <= 1 {
		drawBand(ys)
		return true
	}
	var wg sync.WaitGroup
	for worker := range workers {
		lo := worker * len(ys) / workers
		hi := (worker + 1) * len(ys) / workers
		wg.Go(func() {
			drawBand(ys[lo:hi])
		})
	}
	wg.Wait()
	return true
}

// rasterParallelSafeDst 判断目标图像是否允许不同水平行无锁并发写入
// 入参: dst 目标图像
// 返回: bool 是否允许按行并发写入
func rasterParallelSafeDst(dst draw.Image) bool {
	switch dst.(type) {
	case *image.RGBA, *image.NRGBA, *image.RGBA64, *image.NRGBA64, *image.Gray:
		return true
	default:
		return false
	}
}

// rasterImageAxis 按连续源坐标生成CatmullRom权重，亚像素细轴使用面积覆盖权重
// 入参: first 目标起点, end 目标终点, source 源起点, limit 源终点, scale 缩放比例, offset 平移距离
// 返回: []rasterAxisSample 轴向采样表
func rasterImageAxis(first, end, source, limit int, scale, offset float64) []rasterAxisSample {
	if first >= end || source >= limit || scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) || math.IsNaN(offset) || math.IsInf(offset, 0) {
		return nil
	}
	if math.Abs(scale)*float64(limit-source) < 1 {
		return rasterImageFootprintAxis(first, end, source, limit, scale, offset)
	}
	result := make([]rasterAxisSample, 0, end-first)
	argument := math.Min(1, math.Abs(scale))
	support := draw.CatmullRom.Support / argument
	if math.IsNaN(support) || math.IsInf(support, 0) {
		return nil
	}
	count := 0
	for pixel := first; pixel < end; pixel++ {
		center := (float64(pixel) + .5 - offset) / scale
		if math.IsNaN(center) || math.IsInf(center, 0) || center < float64(source) || center >= float64(limit) {
			continue
		}
		center -= .5
		start := int(max(float64(source), math.Floor(center-support)))
		stop := int(min(float64(limit), math.Ceil(center+support)))
		if stop <= start {
			continue
		}
		count += stop - start
		result = append(result, rasterAxisSample{pixel: pixel, first: start})
	}
	buffer := make([]float64, count)
	for index := range result {
		sample := &result[index]
		center := (float64(sample.pixel)+.5-offset)/scale - .5
		start := sample.first
		stop := int(min(float64(limit), math.Ceil(center+support)))
		n := stop - start
		weights := buffer[:n:n]
		buffer = buffer[n:]
		total := 0.0
		for i := range weights {
			distance := math.Abs((float64(start+i) - center) * argument)
			if distance < draw.CatmullRom.Support {
				weights[i] = draw.CatmullRom.At(distance)
				total += weights[i]
			}
		}
		if total != 0 && !math.IsNaN(total) && !math.IsInf(total, 0) {
			for i := range weights {
				weights[i] /= total
			}
		} else {
			clear(weights)
		}
		sample.weights = weights
	}
	return result
}

// rasterFootprintWeightsFit 检查细轴权重表的合计预算，乘法前排除溢出
// 入参: source 源边界, target 目标范围, m 像素变换
// 返回: bool 权重表是否符合预算
func rasterFootprintWeightsFit(source, target image.Rectangle, m [6]float64) bool {
	budget := rasterScaleBufferLimit / 8
	for _, axis := range [...]struct {
		source, target int
		scale          float64
	}{{source.Dx(), target.Dx(), m[0]}, {source.Dy(), target.Dy(), m[3]}} {
		if math.Abs(axis.scale)*float64(axis.source) < 1 && axis.target > 0 {
			n, samples := axis.source, min(2, axis.target)
			if n > budget/samples {
				return false
			}
			budget -= n * samples
		}
	}
	return true
}

// rasterImageWeightSpan 提供单个目标像素的源范围和权重，不分配整轴权重表
// 入参: pixel 目标像素, source 源起点, limit 源终点, scale 缩放比例, offset 平移距离
// 返回: int 源起点, int 源终点, func(int)float64 源权重
func rasterImageWeightSpan(pixel, source, limit int, scale, offset float64) (int, int, func(int) float64) {
	if source >= limit || scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) || math.IsNaN(offset) || math.IsInf(offset, 0) {
		return 0, 0, nil
	}
	if math.Abs(scale)*float64(limit-source) < 1 {
		left, right := (float64(pixel)-offset)/scale, (float64(pixel+1)-offset)/scale
		if math.IsNaN(left) || math.IsInf(left, 0) || math.IsNaN(right) || math.IsInf(right, 0) {
			return 0, 0, nil
		}
		if right < left {
			left, right = right, left
		}
		if right <= float64(source) || left >= float64(limit) {
			return 0, 0, nil
		}
		return source, limit, func(x int) float64 {
			return math.Max(0, math.Min(float64(x)+1, right)-math.Max(float64(x), left)) * math.Abs(scale)
		}
	}
	center := (float64(pixel) + .5 - offset) / scale
	if math.IsNaN(center) || math.IsInf(center, 0) || center < float64(source) || center >= float64(limit) {
		return 0, 0, nil
	}
	center -= .5
	argument := math.Min(1, math.Abs(scale))
	support := draw.CatmullRom.Support / argument
	if math.IsNaN(support) || math.IsInf(support, 0) {
		return 0, 0, nil
	}
	start := int(max(float64(source), math.Floor(center-support)))
	stop := int(min(float64(limit), math.Ceil(center+support)))
	if stop <= start {
		return 0, 0, nil
	}
	weight := func(x int) float64 {
		distance := math.Abs((float64(x) - center) * argument)
		if distance < draw.CatmullRom.Support {
			return draw.CatmullRom.At(distance)
		}
		return 0
	}
	var total float64
	for x := start; x < stop; x++ {
		total += weight(x)
	}
	if total == 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return 0, 0, nil
	}
	return start, stop, func(x int) float64 { return weight(x) / total }
}

// drawRasterFootprintImage 在细轴权重超预算时按原累加顺序逐点绘制
// 入参: dst 目标图像, src 源图像, m 像素变换, op 合成操作, target 目标范围
// 返回: bool 是否完成绘制
func drawRasterFootprintImage(dst draw.Image, src image.Image, m [6]float64, op draw.Op, target image.Rectangle) bool {
	pixels, ok := src.(image.RGBA64Image)
	if !ok {
		pixels = rasterImageSource{src}
	}
	b := src.Bounds()
	output := image.NewRGBA64(image.Rect(0, 0, 1, 1))
	var value [1]rasterImagePixel
	for y := target.Min.Y; y < target.Max.Y; y++ {
		firstY, endY, wy := rasterImageWeightSpan(y, b.Min.Y, b.Max.Y, m[3], m[5])
		if firstY == endY {
			continue
		}
		for x := target.Min.X; x < target.Max.X; x++ {
			firstX, endX, wx := rasterImageWeightSpan(x, b.Min.X, b.Max.X, m[0], m[4])
			if firstX == endX {
				continue
			}
			value[0] = rasterImagePixel{}
			for sy := firstY; sy < endY; sy++ {
				var row rasterImagePixel
				for sx := firstX; sx < endX; sx++ {
					p, weight := pixels.RGBA64At(sx, sy), wx(sx)
					if weight != 0 {
						row.r += float64(p.R) * weight
						row.g += float64(p.G) * weight
						row.b += float64(p.B) * weight
						row.a += float64(p.A) * weight
					}
				}
				if weight := wy(sy); weight != 0 {
					value[0].r += row.r * weight
					value[0].g += row.g * weight
					value[0].b += row.b * weight
					value[0].a += row.a * weight
				}
			}
			rasterImageWriteRow(dst, output, value[:], x, y, op)
		}
	}
	return true
}

// rasterImageFootprintAxis 对不足一个目标像素的整轴保留真实交叠面积及预乘透明度
// 入参: first 目标起点, end 目标终点, source 源起点, limit 源终点, scale 缩放比例, offset 平移距离
// 返回: []rasterAxisSample 轴向覆盖权重
func rasterImageFootprintAxis(first, end, source, limit int, scale, offset float64) []rasterAxisSample {
	if first >= end || source >= limit || scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) || math.IsNaN(offset) || math.IsInf(offset, 0) {
		return nil
	}
	lo, hi := float64(source)*scale+offset, float64(limit)*scale+offset
	if math.IsNaN(lo) || math.IsInf(lo, 0) || math.IsNaN(hi) || math.IsInf(hi, 0) {
		return nil
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	result := make([]rasterAxisSample, 0, min(2, end-first))
	for pixel := first; pixel < end; pixel++ {
		if math.Min(float64(pixel+1), hi) > math.Max(float64(pixel), lo) {
			result = append(result, rasterAxisSample{pixel: pixel, first: source})
		}
	}
	buffer := make([]float64, len(result)*(limit-source))
	for index := range result {
		sample := &result[index]
		left, right := (float64(sample.pixel)-offset)/scale, (float64(sample.pixel+1)-offset)/scale
		if math.IsNaN(left) || math.IsInf(left, 0) || math.IsNaN(right) || math.IsInf(right, 0) {
			return nil
		}
		if right < left {
			left, right = right, left
		}
		n := limit - source
		sample.weights, buffer = buffer[:n:n], buffer[n:]
		for i := range sample.weights {
			x := float64(source + i)
			sample.weights[i] = math.Max(0, math.Min(x+1, right)-math.Max(x, left)) * math.Abs(scale)
		}
	}
	return result
}

// rasterImageAccumulate 将非零预乘源像素累加到覆盖当前列的全部目标像素
// 入参: row 横向缓冲, samples 采样表, x 源列坐标, active 活跃采样起点, next 活跃采样终点, reversed 采样表是否逆序, r 红色预乘分量, g 绿色预乘分量, b 蓝色预乘分量, a 透明度分量
func rasterImageAccumulate(row []rasterImagePixel, samples []rasterAxisSample, x, active, next int, reversed bool, r, g, b, a float64) {
	if !reversed {
		for index := active; index < next; index++ {
			sample := samples[index]
			if weight := sample.weights[x-sample.first]; weight != 0 {
				value := &row[index]
				value.r += r * weight
				value.g += g * weight
				value.b += b * weight
				value.a += a * weight
			}
		}
		return
	}
	last := len(samples) - 1
	for column := active; column < next; column++ {
		index := last - column
		sample := samples[index]
		if weight := sample.weights[x-sample.first]; weight != 0 {
			value := &row[index]
			value.r += r * weight
			value.g += g * weight
			value.b += b * weight
			value.a += a * weight
		}
	}
}

// rasterImageHorizontalTransparentEdgeNRGBA 对透明补边NRGBA源执行无虚派发逐行滤波
// 入参: row 横向缓冲, te 透明补边源包装, img 原始NRGBA图像, samples 采样表, y 源行坐标, start 起始源列, end 结束源列, reversed 采样表是否逆序
func rasterImageHorizontalTransparentEdgeNRGBA(row []rasterImagePixel, te *transparentEdgeImage, img *image.NRGBA, samples []rasterAxisSample, y, start, end int, reversed bool) {
	rect := te.rect
	if y < rect.Min.Y || y >= rect.Max.Y {
		return
	}
	b := img.Bounds()
	bw, bh := b.Dx(), b.Dy()
	if bw <= 0 || bh <= 0 {
		return
	}
	sy := min(max(y-1, 0), bh-1)
	borderY := y == 0 || y == rect.Max.Y-1
	pix := img.Pix
	stride := img.Stride
	curRowOff := sy * stride
	prevRowOff := max(sy-1, 0) * stride
	nextRowOff := min(sy+1, bh-1) * stride
	curRow := pix[curRowOff : curRowOff+bw*4]
	prevRow := pix[prevRowOff : prevRowOff+bw*4]
	nextRow := pix[nextRowOff : nextRowOff+bw*4]
	hasPrevY := sy > 0
	hasNextY := sy+1 < bh
	maxX := rect.Max.X - 1
	active, next := 0, 0
	for x := start; x < end; x++ {
		if !borderY && hasPrevY && hasNextY && x >= 2 && x+4 <= bw-1 && x+4 <= end {
			off0 := (x - 1) * 4
			if (curRow[off0-1] | curRow[off0+3] | curRow[off0+7] | curRow[off0+11] | curRow[off0+15] | curRow[off0+19] |
				prevRow[off0-1] | prevRow[off0+3] | prevRow[off0+7] | prevRow[off0+11] | prevRow[off0+15] | prevRow[off0+19] |
				nextRow[off0-1] | nextRow[off0+3] | nextRow[off0+7] | nextRow[off0+11] | nextRow[off0+15] | nextRow[off0+19]) == 0 {
				x += 3
				continue
			}
		}
		for next < len(samples) {
			index := next
			if reversed {
				index = len(samples) - next - 1
			}
			if samples[index].first > x {
				break
			}
			next++
		}
		for active < next {
			index := active
			if reversed {
				index = len(samples) - active - 1
			}
			if x < samples[index].first+len(samples[index].weights) {
				break
			}
			active++
		}
		if x < rect.Min.X || x > maxX {
			continue
		}
		sx := min(max(x-1, 0), bw-1)
		off := sx * 4
		r8, g8, b8, a8 := curRow[off], curRow[off+1], curRow[off+2], curRow[off+3]
		if a8 == 0 {
			if hasPrevY && hasNextY && sx > 0 && sx+1 < bw {
				if (prevRow[off-1] | prevRow[off+3] | prevRow[off+7] | curRow[off-1] | curRow[off+7] | nextRow[off-1] | nextRow[off+3] | nextRow[off+7]) == 0 {
					if !borderY && x != 0 && x != maxX {
						continue
					}
				} else {
					bestA := uint8(0)
					var br, bg, bb uint8
					check := func(s []byte, o int) bool {
						if a := s[o+3]; a > bestA {
							bestA, br, bg, bb = a, s[o], s[o+1], s[o+2]
							return a == 255
						}
						return false
					}
					if !check(prevRow, off-4) && !check(prevRow, off) && !check(prevRow, off+4) && !check(curRow, off-4) && !check(curRow, off+4) && !check(nextRow, off-4) && !check(nextRow, off) {
						check(nextRow, off+4)
					}
					if bestA != 0 {
						r8, g8, b8, a8 = br, bg, bb, 1
					}
				}
			} else {
				left, right := max(sx-1, 0), min(sx+2, bw)
				top, bottom := max(sy-1, 0), min(sy+2, bh)
				bestA, bestOff := uint8(0), 0
			scanEdge:
				for ny := top; ny < bottom; ny++ {
					o := ny*stride + left*4
					for nx := left; nx < right; nx++ {
						if (nx != sx || ny != sy) && pix[o+3] > bestA {
							bestA, bestOff = pix[o+3], o
							if bestA == 255 {
								break scanEdge
							}
						}
						o += 4
					}
				}
				if bestA != 0 {
					r8, g8, b8, a8 = pix[bestOff], pix[bestOff+1], pix[bestOff+2], 1
				}
			}
		}
		if borderY || x == 0 || x == maxX {
			a8 = 1
		}
		if a8 == 0 {
			continue
		}
		var r, g, bVal, a float64
		if a8 == 255 {
			r = float64(uint32(r8) * 0x101)
			g = float64(uint32(g8) * 0x101)
			bVal = float64(uint32(b8) * 0x101)
			a = 65535
		} else {
			a32 := uint32(a8)
			r = float64((uint32(r8) * 0x101 * a32) / 0xff)
			g = float64((uint32(g8) * 0x101 * a32) / 0xff)
			bVal = float64((uint32(b8) * 0x101 * a32) / 0xff)
			a = float64(a32 * 0x101)
		}
		rasterImageAccumulate(row, samples, x, active, next, reversed, r, g, bVal, a)
	}
}

// rasterImageHorizontal 按源列复用像素，保留各目标像素的权重累加顺序与中间精度
// 入参: row 横向缓冲, src 源像素, samples 采样表, y 源行坐标
func rasterImageHorizontal(row []rasterImagePixel, src image.RGBA64Image, samples []rasterAxisSample, y int) {
	clear(row)
	first, last := samples[0], samples[len(samples)-1]
	reversed := first.first > last.first || first.first == last.first && len(first.weights) > len(last.weights)
	start := min(first.first, last.first)
	end := max(first.first+len(first.weights), last.first+len(last.weights))
	if te, ok := src.(*transparentEdgeImage); ok {
		if nrgba, ok := te.source.(*image.NRGBA); ok {
			rasterImageHorizontalTransparentEdgeNRGBA(row, te, nrgba, samples, y, start, end, reversed)
			return
		}
	}
	if nrgba, ok := src.(*image.NRGBA); ok {
		b := nrgba.Bounds()
		if y < b.Min.Y || y >= b.Max.Y {
			return
		}
		rowOff := (y - b.Min.Y) * nrgba.Stride
		active, next := 0, 0
		for x := start; x < end; x++ {
			for next < len(samples) {
				index := next
				if reversed {
					index = len(samples) - next - 1
				}
				if samples[index].first > x {
					break
				}
				next++
			}
			for active < next {
				index := active
				if reversed {
					index = len(samples) - active - 1
				}
				if x < samples[index].first+len(samples[index].weights) {
					break
				}
				active++
			}
			if x < b.Min.X || x >= b.Max.X {
				continue
			}
			off := rowOff + (x-b.Min.X)*4
			a8 := nrgba.Pix[off+3]
			if a8 == 0 {
				continue
			}
			r8, g8, b8 := nrgba.Pix[off], nrgba.Pix[off+1], nrgba.Pix[off+2]
			var r, g, bVal, a float64
			if a8 == 255 {
				r = float64(uint32(r8) * 0x101)
				g = float64(uint32(g8) * 0x101)
				bVal = float64(uint32(b8) * 0x101)
				a = 65535
			} else {
				a32 := uint32(a8)
				r = float64((uint32(r8) * 0x101 * a32) / 0xff)
				g = float64((uint32(g8) * 0x101 * a32) / 0xff)
				bVal = float64((uint32(b8) * 0x101 * a32) / 0xff)
				a = float64(a32 * 0x101)
			}
			rasterImageAccumulate(row, samples, x, active, next, reversed, r, g, bVal, a)
		}
		return
	}
	if rgba, ok := src.(*image.RGBA); ok {
		b := rgba.Bounds()
		if y < b.Min.Y || y >= b.Max.Y {
			return
		}
		rowOff := (y - b.Min.Y) * rgba.Stride
		active, next := 0, 0
		for x := start; x < end; x++ {
			for next < len(samples) {
				index := next
				if reversed {
					index = len(samples) - next - 1
				}
				if samples[index].first > x {
					break
				}
				next++
			}
			for active < next {
				index := active
				if reversed {
					index = len(samples) - active - 1
				}
				if x < samples[index].first+len(samples[index].weights) {
					break
				}
				active++
			}
			if x < b.Min.X || x >= b.Max.X {
				continue
			}
			off := rowOff + (x-b.Min.X)*4
			r8, g8, b8, a8 := rgba.Pix[off], rgba.Pix[off+1], rgba.Pix[off+2], rgba.Pix[off+3]
			if r8 == 0 && g8 == 0 && b8 == 0 && a8 == 0 {
				continue
			}
			rasterImageAccumulate(row, samples, x, active, next, reversed, float64(uint32(r8)*0x101), float64(uint32(g8)*0x101), float64(uint32(b8)*0x101), float64(uint32(a8)*0x101))
		}
		return
	}
	active, next := 0, 0
	for x := start; x < end; x++ {
		for next < len(samples) {
			index := next
			if reversed {
				index = len(samples) - next - 1
			}
			if samples[index].first > x {
				break
			}
			next++
		}
		for active < next {
			index := active
			if reversed {
				index = len(samples) - active - 1
			}
			if x < samples[index].first+len(samples[index].weights) {
				break
			}
			active++
		}
		p := src.RGBA64At(x, y)
		if p.R == 0 && p.G == 0 && p.B == 0 && p.A == 0 {
			continue
		}
		rasterImageAccumulate(row, samples, x, active, next, reversed, float64(p.R), float64(p.G), float64(p.B), float64(p.A))
	}
}

// rasterImageWriteRow 在两轴采样完成后取整并使用标准预乘合成
// 入参: dst 目标图像, output 行像素缓冲, values 累积颜色, x 横向起点, y 目标行坐标, op 合成操作
func rasterImageWriteRow(dst draw.Image, output *image.RGBA64, values []rasterImagePixel, x, y int, op draw.Op) {
	for i, value := range values {
		r := rasterImageComponent(value.r, value.a)
		g := rasterImageComponent(value.g, value.a)
		b := rasterImageComponent(value.b, value.a)
		a := rasterImageComponent(value.a, value.a)
		pixel := output.Pix[i*8 : i*8+8 : i*8+8]
		pixel[0], pixel[1] = uint8(r>>8), uint8(r)
		pixel[2], pixel[3] = uint8(g>>8), uint8(g)
		pixel[4], pixel[5] = uint8(b>>8), uint8(b)
		pixel[6], pixel[7] = uint8(a>>8), uint8(a)
	}
	draw.Draw(dst, image.Rect(x, y, x+len(values), y+1), output, image.Point{}, op)
}

// rasterImageComponent 按预乘透明度裁切并取整十六位分量
// 入参: value 采样分量, alpha 采样透明度
// 返回: uint16 输出分量
func rasterImageComponent(value, alpha float64) uint16 {
	if math.IsNaN(value) || math.IsNaN(alpha) {
		return 0
	}
	if value > alpha {
		value = alpha
	}
	value += .5
	if value <= 0 {
		return 0
	}
	if value >= 65535 {
		return 65535
	}
	return uint16(value)
}

// rasterTransformSource 为放大的通用图像缓存十六位预乘像素，限制临时缓冲大小
// 入参: src 源图像, affected 目标采样区域
// 返回: image.Image 等值采样图像
func rasterTransformSource(src image.Image, affected image.Rectangle) image.Image {
	switch src.(type) {
	case *image.RGBA, *image.RGBA64:
		return src
	}
	b := src.Bounds()
	if b.Empty() || affected.Empty() || b.Dx() > rasterScaleBufferLimit/8/b.Dy() || float64(affected.Dx())*float64(affected.Dy()) < float64(b.Dx())*float64(b.Dy()) {
		return src
	}
	pixels := image.NewRGBA64(b)
	draw.Draw(pixels, b, src, b.Min, draw.Src)
	return pixels
}

// rasterScaleBounds 检查变换是否可无几何取整地使用缩放器，并限制其中间缓冲
// 入参: b 源边界, m 像素变换
// 返回: image.Rectangle 缩放边界, bool 是否适用
func rasterScaleBounds(b image.Rectangle, m [6]float64) (image.Rectangle, bool) {
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

// RGBA64At 读取通用颜色的原始预乘分量
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 预乘颜色
func (s rasterImageSource) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := s.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}
