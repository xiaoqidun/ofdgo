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

import "image"

// pdfCompositeTileSize 限定单次合成块的像素边长
const pdfCompositeTileSize = 256

// pdfCompositeBuffers 在一次局部合成中保留至多八个块容量内的空闲缓冲
type pdfCompositeBuffers[T any] [8][]T

// pdfCompositeScratch 复用像素及蒙版缓冲，不跨页持有活动内容
type pdfCompositeScratch struct {
	pixels   pdfCompositeBuffers[pdfCompositePixel]
	masks    pdfCompositeBuffers[float64]
	channels pdfCompositeBuffers[uint8]
	rasters  [8]*image.RGBA
}

// acquireCoverage 借用当前块的独占画布，保留活动画布供嵌套合成读取
// 返回: *image.RGBA 未清空画布，不支持缓冲绘制的后端返回nil, error 无效绘制区域
func (c *pdfCompositor) acquireCoverage() (*image.RGBA, error) {
	if _, ok := c.importer.editor.Backends().Raster.(RasterBufferBackend); !ok {
		return nil, nil
	}
	page := RasterPage{Width: c.box.W, Height: c.box.H, DPI: c.importer.rasterDPI}
	w, h, err := page.PixelSize()
	if err != nil {
		return nil, err
	}
	if c.scratch == nil {
		c.scratch = &pdfCompositeScratch{}
	}
	rect := image.Rect(0, 0, w, h)
	for i, stored := range c.scratch.rasters {
		if stored != nil && stored.Rect == rect {
			c.scratch.rasters[i] = nil
			return stored, nil
		}
	}
	return image.NewRGBA(rect), nil
}

// releaseCoverage 归还当前调用不再读取的画布，限制单次合成保留的内存
// 入参: coverage 当前调用的覆盖图像
func (c *pdfCompositor) releaseCoverage(coverage image.Image) {
	if _, ok := c.importer.editor.Backends().Raster.(RasterBufferBackend); !ok || c.scratch == nil {
		return
	}
	buffer, ok := coverage.(*image.RGBA)
	if !ok || buffer == nil || cap(buffer.Pix) > 4*pdfCompositeTileSize*pdfCompositeTileSize {
		return
	}
	for i, stored := range c.scratch.rasters {
		if stored == nil {
			c.scratch.rasters[i] = buffer
			return
		}
	}
}

// acquire 借用尚未初始化的独立缓冲，嵌套调用不复用活动缓冲
// 入参: count 所需元素数量
// 返回: []T 可由当前调用独占的缓冲
func (s *pdfCompositeBuffers[T]) acquire(count int) []T {
	selected := -1
	for i, buffer := range s {
		if cap(buffer) >= count && (selected < 0 || cap(buffer) < cap(s[selected])) {
			selected = i
		}
	}
	if selected < 0 {
		return make([]T, count)
	}
	buffer := s[selected]
	s[selected] = nil
	return buffer[:count]
}

// release 归还已不再访问的缓冲，超出块容量或空闲槽数量时直接释放
// 入参: buffer 当前调用借用的缓冲
func (s *pdfCompositeBuffers[T]) release(buffer []T) {
	if len(buffer) == 0 || cap(buffer) > pdfCompositeTileSize*pdfCompositeTileSize {
		return
	}
	for i, stored := range s {
		if stored == nil {
			s[i] = buffer
			return
		}
	}
}

// acquirePixels 为当前合成器借用独立像素缓冲
// 入参: count 所需像素数量
// 返回: []pdfCompositePixel 尚未初始化的独占缓冲
func (c *pdfCompositor) acquirePixels(count int) []pdfCompositePixel {
	if c.scratch == nil {
		c.scratch = &pdfCompositeScratch{}
	}
	return c.scratch.pixels.acquire(count)
}

// releasePixels 归还当前调用已不再访问的像素缓冲
// 入参: pixels 当前调用借用的缓冲
func (c *pdfCompositor) releasePixels(pixels []pdfCompositePixel) {
	if c.scratch != nil {
		c.scratch.pixels.release(pixels)
	}
}

// acquireChannels 借用图案通道标记，仅在设备函数需要时分配
// 入参: count 当前块像素数
// 返回: []uint8 尚未初始化的独占通道缓冲
func (c *pdfCompositor) acquireChannels(count int) []uint8 {
	if c.scratch == nil {
		c.scratch = &pdfCompositeScratch{}
	}
	return c.scratch.channels.acquire(count)
}

// releaseChannels 归还不再参与嵌套合成的通道标记
// 入参: channels 当前调用借用的通道缓冲
func (c *pdfCompositor) releaseChannels(channels []uint8) {
	if c.scratch != nil {
		c.scratch.channels.release(channels)
	}
}

// releaseMasks 在当前块绘制结束后归还蒙版结果，不复用其他块的坐标缓存
func (c *pdfCompositor) releaseMasks() {
	if c.scratch != nil {
		for _, mask := range c.masks {
			c.scratch.masks.release(mask)
		}
	}
	clear(c.masks)
}
