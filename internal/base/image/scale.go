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
	"slices"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// rasterScaleBufferLimit 限制整像素缩放器的浮点中间缓冲，超出时使用逐行采样
const rasterScaleBufferLimit = 8 << 20

// DrawPixels 对轴向缩放使用可分离采样，其余变换保留通用路径
// 入参: dst 目标图像, src 源图像, m 按a、b、c、d、e、f排列的像素变换, op 合成操作
func DrawPixels(dst draw.Image, src image.Image, m [6]float64, op draw.Op) {
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
	src = rasterTransformSource(src, TransformBounds(b, m, dst.Bounds()))
	draw.CatmullRom.Transform(dst, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, src, b, op, nil)
}

// TransformBounds 计算采样器可能写入的像素范围，并在整数转换前裁至目标边界
// 入参: source 原始像素边界, m 像素变换, target 目标边界
// 返回: image.Rectangle 受影响的目标像素区域
func TransformBounds(source image.Rectangle, m [6]float64, target image.Rectangle) image.Rectangle {
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

// RGBA64At 读取通用颜色的原始预乘分量
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 预乘颜色
func (s rasterImageSource) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := s.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
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
	horizontal := make([]rasterImagePixel, width)
	output := image.NewRGBA64(image.Rect(0, 0, width, 1))
	var active []rasterImageRow
	var free [][]rasterImagePixel
	next := 0
	for y := ys[0].first; next < len(ys) || len(active) != 0; y++ {
		if len(active) == 0 && y < ys[next].first {
			y = ys[next].first
		}
		for next < len(ys) && ys[next].first == y {
			var row []rasterImagePixel
			if n := len(free); n != 0 {
				row, free = free[n-1], free[:n-1]
				clear(row)
			} else {
				row = make([]rasterImagePixel, width)
			}
			active = append(active, rasterImageRow{sample: ys[next], values: row})
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
	return true
}

// rasterImageAxis 按连续源坐标生成CatmullRom权重，亚像素细轴使用面积覆盖权重
// 入参: first 目标起点, end 目标终点, source 源起点, limit 源终点, scale 缩放比例, offset 平移距离
// 返回: []rasterAxisSample 轴向采样表
func rasterImageAxis(first, end, source, limit int, scale, offset float64) []rasterAxisSample {
	if math.Abs(scale)*float64(limit-source) < 1 {
		return rasterImageFootprintAxis(first, end, source, limit, scale, offset)
	}
	result := make([]rasterAxisSample, 0, end-first)
	argument := math.Min(1, math.Abs(scale))
	support := draw.CatmullRom.Support / argument
	count := 0
	for pixel := first; pixel < end; pixel++ {
		center := (float64(pixel) + .5 - offset) / scale
		if center < float64(source) || center >= float64(limit) {
			continue
		}
		center -= .5
		start := int(max(float64(source), math.Floor(center-support)))
		stop := int(min(float64(limit), math.Ceil(center+support)))
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
		for i := range weights {
			weights[i] /= total
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
	if math.Abs(scale)*float64(limit-source) < 1 {
		left, right := (float64(pixel)-offset)/scale, (float64(pixel+1)-offset)/scale
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
	if center < float64(source) || center >= float64(limit) {
		return 0, 0, nil
	}
	center -= .5
	argument := math.Min(1, math.Abs(scale))
	support := draw.CatmullRom.Support / argument
	start := int(max(float64(source), math.Floor(center-support)))
	stop := int(min(float64(limit), math.Ceil(center+support)))
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
	lo, hi := float64(source)*scale+offset, float64(limit)*scale+offset
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

// rasterImageHorizontal 按源列复用像素，保留各目标像素的权重累加顺序与中间精度
// 入参: row 横向缓冲, src 源像素, samples 采样表, y 源行坐标
func rasterImageHorizontal(row []rasterImagePixel, src image.RGBA64Image, samples []rasterAxisSample, y int) {
	clear(row)
	first, last := samples[0], samples[len(samples)-1]
	reversed := first.first > last.first || first.first == last.first && len(first.weights) > len(last.weights)
	start := min(first.first, last.first)
	end := max(first.first+len(first.weights), last.first+len(last.weights))
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
		r, g, b, a := float64(p.R), float64(p.G), float64(p.B), float64(p.A)
		for column := active; column < next; column++ {
			index := column
			if reversed {
				index = len(samples) - column - 1
			}
			sample := samples[index]
			weight := sample.weights[x-sample.first]
			if weight != 0 {
				value := &row[index]
				value.r += r * weight
				value.g += g * weight
				value.b += b * weight
				value.a += a * weight
			}
		}
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
	if value > alpha || math.IsNaN(alpha) {
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
