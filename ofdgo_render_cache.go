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

import "container/list"

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
