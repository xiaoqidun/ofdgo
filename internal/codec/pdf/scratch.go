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

package pdf

import "image"

// CompositeTileSize 限定单次合成块的像素边长
const CompositeTileSize = 256

// CompositeBuffers 在一次局部合成中保留至多八个块容量内的空闲缓冲
type CompositeBuffers[T any] [8][]T

// RasterBuffers 保存按像素尺寸复用的独占覆盖画布
type RasterBuffers [8]*image.RGBA

// CoverageRow 读取完整范围内的原生覆盖行，其他类型沿用逐点采样
// 入参: coverage 覆盖图像, y 行坐标, left 起始列, right 结束列
// 返回: []byte RGBA像素或空值
func CoverageRow(coverage image.Image, y, left, right int) []byte {
	buffer, ok := coverage.(*image.RGBA)
	if !ok || y < buffer.Rect.Min.Y || y >= buffer.Rect.Max.Y || left < buffer.Rect.Min.X || right > buffer.Rect.Max.X || left >= right {
		return nil
	}
	start := buffer.PixOffset(left, y)
	return buffer.Pix[start : start+4*(right-left)]
}

// Acquire 借用尚未初始化的独立缓冲，嵌套调用不复用活动缓冲
// 入参: count 所需元素数量
// 返回: []T 可由当前调用独占的缓冲
func (s *CompositeBuffers[T]) Acquire(count int) []T {
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

// Release 归还已不再访问的缓冲，超出块容量或空闲槽数量时直接释放
// 入参: buffer 当前调用借用的缓冲
func (s *CompositeBuffers[T]) Release(buffer []T) {
	if len(buffer) == 0 || cap(buffer) > CompositeTileSize*CompositeTileSize {
		return
	}
	for i, stored := range s {
		if stored == nil {
			s[i] = buffer
			return
		}
	}
}

// Acquire 借用尺寸相同的独占画布，不清空已有像素
// 入参: rect 输出像素范围
// 返回: *image.RGBA 当前调用独占的画布
func (s *RasterBuffers) Acquire(rect image.Rectangle) *image.RGBA {
	for i, stored := range s {
		if stored != nil && stored.Rect == rect {
			s[i] = nil
			return stored
		}
	}
	return image.NewRGBA(rect)
}

// Release 归还块容量内的画布，超出容量或空闲槽数量时直接释放
// 入参: buffer 当前调用借用的画布
func (s *RasterBuffers) Release(buffer *image.RGBA) {
	if buffer == nil || cap(buffer.Pix) > 4*CompositeTileSize*CompositeTileSize {
		return
	}
	for i, stored := range s {
		if stored == nil {
			s[i] = buffer
			return
		}
	}
}
