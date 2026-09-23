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
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"math"
)

// renderCache 按估算字节数限制派生渲染数据，淘汰不修改源资源
type renderCache[K comparable, V any] struct {
	limit, used int
	entries     map[K]*list.Element
	order       list.List
}

// renderCacheEntry 保存缓存值及内存成本
type renderCacheEntry[K comparable, V any] struct {
	key   K
	value V
	cost  int
}

// get 读取缓存并更新使用顺序
// 入参: key 缓存键
// 返回: V 缓存值, bool 是否命中
func (c *renderCache[K, V]) get(key K) (V, bool) {
	if item := c.entries[key]; item != nil {
		c.order.MoveToFront(item)
		return item.Value.(renderCacheEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// put 缓存预算内的派生数据，超大单项不进入缓存
// 入参: key 缓存键, value 缓存值, cost 字节成本
func (c *renderCache[K, V]) put(key K, value V, cost int) {
	if item := c.entries[key]; item != nil {
		c.used -= item.Value.(renderCacheEntry[K, V]).cost
		c.order.Remove(item)
		delete(c.entries, key)
	}
	if cost <= 0 || cost > c.limit {
		return
	}
	if c.entries == nil {
		c.entries = make(map[K]*list.Element)
	}
	for c.used+cost > c.limit {
		item := c.order.Back()
		entry := item.Value.(renderCacheEntry[K, V])
		c.used -= entry.cost
		delete(c.entries, entry.key)
		c.order.Remove(item)
	}
	c.entries[key] = c.order.PushFront(renderCacheEntry[K, V]{key, value, cost})
	c.used += cost
}

// rasterCommandCost 估算指令快照及后端路径的缓存成本，不含借用图片
// 入参: command 绘制指令
// 返回: int 字节成本
func rasterCommandCost(command RasterCommand) int {
	cost := 512 + (len(command.Path)+len(command.Clip))*208
	if command.Stroke != nil {
		cost += 128 + len(command.Stroke.Dashes)*16
	}
	if command.Paint.Gradient != nil {
		cost += 256 + len(command.Paint.Gradient.Stops)*64
	}
	return cost
}

// rasterCommandKey 按完整路径、样式、裁剪及像素配置标识非图片指令
// 入参: page 页面尺寸和DPI, command 绘制指令
// 返回: [32]byte 内容键
func rasterCommandKey(page *RasterPage, command RasterCommand) [32]byte {
	h := sha256.New()
	path, clip := rasterClipKey(page, command.Path), rasterClipKey(page, command.Clip)
	h.Write(path[:])
	h.Write(clip[:])
	var data [8]byte
	number := func(value float64) {
		binary.LittleEndian.PutUint64(data[:], math.Float64bits(value))
		h.Write(data[:])
	}
	flag := func(value bool) {
		if value {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	word := func(value string) {
		number(float64(len(value)))
		h.Write([]byte(value))
	}
	flag(command.Clip != nil)
	flag(command.EvenOdd)
	for _, value := range command.Transform {
		number(value)
	}
	c := command.Paint.Color
	h.Write([]byte{c.R, c.G, c.B, c.A})
	flag(command.Stroke != nil)
	if stroke := command.Stroke; stroke != nil {
		number(stroke.Width)
		number(stroke.MiterLimit)
		number(stroke.Tolerance)
		number(stroke.DashOffset)
		word(stroke.Cap)
		word(stroke.Join)
		number(float64(len(stroke.Dashes)))
		for _, dash := range stroke.Dashes {
			number(dash)
		}
	}
	flag(command.Paint.Gradient != nil)
	if g := command.Paint.Gradient; g != nil {
		h.Write([]byte{byte(g.Kind)})
		for _, value := range []float64{g.Start.X, g.Start.Y, g.End.X, g.End.Y, g.R0, g.R1} {
			number(value)
		}
		flag(g.Spread != nil)
		if spread := g.Spread; spread != nil {
			number(float64(spread.Extend))
			number(spread.Period)
			word(spread.MapType)
		}
		for _, stop := range g.Stops {
			number(stop.Offset)
			c := stop.Color
			h.Write([]byte{c.R, c.G, c.B, c.A})
		}
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}
