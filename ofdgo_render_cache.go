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
	"context"
	"errors"
	"image"
	"reflect"
	"sync"
	"time"
	"weak"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// defaultResourceCacheBytes 定义默认缓存总预算及类别扩展基准
const defaultResourceCacheBytes = 256 << 20

// ResourceOptions 设置共享计算、派生缓存与快照预算，字节预算不限制进程实际分配
// Parallelism为0时使用GOMAXPROCS，MemoryLimit为0时不指定计算预算
// CacheBytes为0时使用256MiB，SnapshotBytes为0时使用缓存预算的一半，负值禁用相应缓存
// AvailableMemory返回可用于新增任务的字节数，同时作为缓存动态上限，需支持并发且不可重入Resources
type ResourceOptions struct {
	Parallelism     int
	MemoryLimit     int64
	CacheBytes      int64
	SnapshotBytes   int64
	AvailableMemory func() int64
}

// Resources 协调共享缓存与计算任务，通过NewResources创建，可跨阅读器使用，不可复制
// 缓存仅持有存活实例的弱引用，Clear释放派生数据，不关闭输入或取消任务
type Resources struct {
	options ResourceOptions
	pool    *ConversionPool
	mu      sync.Mutex
	used    int64
	limit   int64
	checked time.Time
	order   list.List
}

// resourceEntry 保存共享缓存成本与弱引用淘汰操作
type resourceEntry struct {
	cost  int64
	evict func()
}

// renderCache 按估算字节数限制派生渲染数据，淘汰不修改源资源
type renderCache[K comparable, V any] struct {
	mu          sync.Mutex
	resources   *Resources
	limit, used int
	entries     map[K]*list.Element
	order       list.List
	loading     map[K]*renderCacheLoad[V]
}

// renderCacheLoad 合并同一资源的并发加载，不持有已完成任务
type renderCacheLoad[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// renderCacheEntry 保存缓存值及内存成本
type renderCacheEntry[K comparable, V any] struct {
	key    K
	value  V
	cost   int
	shared *list.Element
}

// NewResources 创建共享资源策略，不启动任务或修改进程全局配置
// 入参: options 并行及内存选项
// 返回: *Resources 共享策略, error 参数错误
func NewResources(options ResourceOptions) (*Resources, error) {
	pool, err := NewConversionPool(ConversionPoolOptions{Parallelism: options.Parallelism, MemoryLimit: options.MemoryLimit, AvailableMemory: options.AvailableMemory})
	if err != nil {
		return nil, err
	}
	if options.CacheBytes == 0 {
		options.CacheBytes = defaultResourceCacheBytes
	}
	options.CacheBytes = max(0, options.CacheBytes)
	if options.SnapshotBytes == 0 {
		options.SnapshotBytes = options.CacheBytes / 2
	}
	options.SnapshotBytes = max(0, min(options.SnapshotBytes, options.CacheBytes))
	return &Resources{options: options, pool: pool, limit: options.CacheBytes}, nil
}

// ConversionPool 返回资源策略共享的转换调度器
// 返回: *ConversionPool 转换调度器
func (r *Resources) ConversionPool() *ConversionPool { return r.pool }

// Acquire 等待一个计算任务及其估算工作集额度，允许超预算任务独占执行
// 调用方必须调用返回的释放函数，重复释放无效，不可持有额度再嵌套申请
// 入参: ctx 等待上下文, memory 估算字节数，零值仅限制并行
// 返回: func() 释放额度, error 参数或取消错误
func (r *Resources) Acquire(ctx context.Context, memory int64) (func(), error) {
	if memory < 0 {
		return nil, errors.New("invalid task memory")
	}
	if err := r.pool.acquire(ctx, true, memory); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { r.pool.release(true, memory) }) }, nil
}

// context 为未指定转换池的操作附加资源调度器
// 入参: ctx 操作上下文
// 返回: context.Context 计算上下文
func (r *Resources) context(ctx context.Context) context.Context {
	if r == nil || ctx.Value(conversionPoolKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, conversionPoolKey{}, r.pool)
}

// WithResources 设置阅读器及派生编辑器共享的资源策略
// 入参: resources 共享策略，nil使用默认策略
// 返回: ReaderOption 阅读选项
func WithResources(resources *Resources) ReaderOption {
	return func(options *readerOptions) { options.resources = resources }
}

// Clear 释放当前缓存，正在计算的任务完成后仍可写入新缓存
func (r *Resources) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.order.Len() != 0 {
		r.evict()
	}
}

// CacheUsage 返回当前登记的派生缓存估算字节数，不含任务临时内存
// 返回: int64 缓存成本
func (r *Resources) CacheUsage() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used
}

// refresh 按当前可用内存收缩缓存，需持有资源锁
func (r *Resources) refresh() {
	if r.options.AvailableMemory == nil || time.Since(r.checked) < 250*time.Millisecond {
		return
	}
	r.checked = time.Now()
	r.limit = min(r.options.CacheBytes, max(0, r.options.AvailableMemory()))
	for r.used > r.limit {
		r.evict()
	}
}

// evict 淘汰最久未访问的共享条目，需持有资源锁
func (r *Resources) evict() {
	item := r.order.Back()
	entry := item.Value.(resourceEntry)
	r.order.Remove(item)
	r.used -= entry.cost
	entry.evict()
}

// resourceCache 按类别基准分配共享预算，较大总预算同比扩展类别额度
// 入参: resources 共享策略, baseline 类别基准预算
// 返回: *renderCache 缓存实例
func resourceCache[K comparable, V any](resources *Resources, baseline int) *renderCache[K, V] {
	if resources == nil {
		return &renderCache[K, V]{limit: baseline}
	}
	limit := min(resources.options.CacheBytes, int64(int(^uint(0)>>1)-1))
	if baseline > 0 && limit > int64(baseline) {
		share := int64(baseline)
		if limit > defaultResourceCacheBytes {
			scaled := float64(baseline) * (float64(limit) / defaultResourceCacheBytes)
			share = limit
			if scaled < float64(limit) {
				share = int64(scaled)
			}
		}
		limit = min(limit, share)
	}
	return &renderCache[K, V]{resources: resources, limit: int(limit)}
}

// mutex 返回缓存数据及共享账本使用的同一把锁
// 返回: *sync.Mutex 缓存锁
func (c *renderCache[K, V]) mutex() *sync.Mutex {
	if c.resources != nil {
		return &c.resources.mu
	}
	return &c.mu
}

// touch 更新本地及共享条目的访问顺序，需持有缓存锁
// 入参: item 缓存条目
func (c *renderCache[K, V]) touch(item *list.Element) {
	c.order.MoveToFront(item)
	if shared := item.Value.(renderCacheEntry[K, V]).shared; shared != nil {
		c.resources.order.MoveToFront(shared)
	}
}

// remove 删除本地条目并归还共享预算，需持有缓存锁
// 入参: item 缓存条目
func (c *renderCache[K, V]) remove(item *list.Element) {
	entry := item.Value.(renderCacheEntry[K, V])
	c.used -= entry.cost
	c.order.Remove(item)
	delete(c.entries, entry.key)
	if entry.shared != nil {
		c.resources.used -= int64(entry.cost)
		c.resources.order.Remove(entry.shared)
	}
}

// clear 释放已有缓存条目并归还共享预算，不取消正在加载的任务
func (c *renderCache[K, V]) clear() {
	if c == nil {
		return
	}
	c.mutex().Lock()
	defer c.mutex().Unlock()
	for c.order.Len() != 0 {
		c.remove(c.order.Back())
	}
}

// get 读取缓存并更新使用顺序
// 入参: key 缓存键
// 返回: V 缓存值, bool 是否命中
func (c *renderCache[K, V]) get(key K) (V, bool) {
	c.mutex().Lock()
	defer c.mutex().Unlock()
	if item := c.entries[key]; item != nil {
		c.touch(item)
		return item.Value.(renderCacheEntry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// put 缓存预算内的派生数据，超大单项不进入缓存
// 入参: key 缓存键, value 缓存值, cost 字节成本
func (c *renderCache[K, V]) put(key K, value V, cost int) {
	c.mutex().Lock()
	defer c.mutex().Unlock()
	c.putLocked(key, value, cost)
}

// putLocked 在持有缓存锁时替换条目并按预算淘汰
// 入参: key 缓存键, value 缓存值, cost 字节成本
func (c *renderCache[K, V]) putLocked(key K, value V, cost int) {
	if item := c.entries[key]; item != nil {
		c.remove(item)
	}
	if cost <= 0 || cost > c.limit {
		return
	}
	if c.entries == nil {
		c.entries = make(map[K]*list.Element)
	}
	for cost > c.limit-c.used {
		item := c.order.Back()
		c.remove(item)
	}
	entry := renderCacheEntry[K, V]{key: key, value: value, cost: cost}
	if r := c.resources; r != nil {
		r.refresh()
		if int64(cost) > r.limit {
			return
		}
		for int64(cost) > r.limit-r.used {
			r.evict()
		}
	}
	item := c.order.PushFront(entry)
	c.entries[key] = item
	c.used += cost
	if r := c.resources; r != nil {
		ref, entryRef := weak.Make(c), weak.Make(item)
		entry.shared = r.order.PushFront(resourceEntry{cost: int64(cost), evict: func() {
			if cache := ref.Value(); cache != nil {
				previous := entryRef.Value()
				if previous == nil {
					return
				}
				entry := previous.Value.(renderCacheEntry[K, V])
				if cache.entries[entry.key] != previous {
					return
				}
				cache.used -= entry.cost
				cache.order.Remove(previous)
				delete(cache.entries, entry.key)
			}
		}})
		item.Value = entry
		r.used += int64(cost)
	}
}

// load 复用缓存并合并并发请求，等待取消不影响正在读取的任务
// 入参: ctx 当前任务上下文, key 缓存键, read 加载资源及其内存成本
// 返回: V 缓存值, error 读取或取消错误
func (c *renderCache[K, V]) load(ctx context.Context, key K, read func() (V, int, error)) (V, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var zero V
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mutex().Lock()
		if item := c.entries[key]; item != nil {
			c.touch(item)
			value := item.Value.(renderCacheEntry[K, V]).value
			c.mutex().Unlock()
			return value, nil
		}
		if pending := c.loading[key]; pending != nil {
			c.mutex().Unlock()
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-pending.done:
				if errors.Is(pending.err, context.Canceled) || errors.Is(pending.err, context.DeadlineExceeded) {
					continue
				}
				return pending.value, pending.err
			}
		}
		if c.loading == nil {
			c.loading = make(map[K]*renderCacheLoad[V])
		}
		pending := &renderCacheLoad[V]{done: make(chan struct{}), err: errors.New("resource loading interrupted")}
		c.loading[key] = pending
		c.mutex().Unlock()
		defer func() {
			c.mutex().Lock()
			delete(c.loading, key)
			close(pending.done)
			c.mutex().Unlock()
		}()
		value, cost, err := read()
		if err == nil {
			err = ctx.Err()
		}
		pending.value, pending.err = value, err
		if err == nil {
			c.put(key, value, cost)
		}
		return value, err
	}
}

// rasterCommandCost 按保留容量估算中立指令成本，不含临时后端路径及借用图片
// 入参: command 绘制指令
// 返回: int 字节成本
func rasterCommandCost(command RasterCommand) int {
	cost := 512 + (cap(command.Path)+cap(command.Clip))*56
	if command.Stroke != nil {
		cost += 128 + cap(command.Stroke.Dashes)*8
	}
	if command.Paint.Gradient != nil {
		cost += 256 + cap(command.Paint.Gradient.Stops)*64
		if mesh := command.Paint.Gradient.Mesh; mesh != nil {
			cost += 32 + cap(mesh.Triangles)*240
		}
	}
	return cost
}

// rasterCommandsCost 估算整组指令保留的内存，同一图片实例只计一次
// 入参: commands 绘制指令, limit 缓存预算
// 返回: int 估算字节数，超预算时返回预算加一
func rasterCommandsCost(commands []RasterCommand, limit int) int {
	limit = max(0, limit)
	cost := 0
	var images map[image.Image]bool
	for _, command := range commands {
		part := rasterCommandCost(command)
		if part < 0 || part > limit-cost {
			return limit + 1
		}
		cost += part
		if command.Image == nil {
			continue
		}
		if reflect.ValueOf(command.Image).Kind() == reflect.Pointer {
			if images[command.Image] {
				continue
			}
			if images == nil {
				images = make(map[image.Image]bool)
			}
			images[command.Image] = true
		}
		part = baseimage.StorageCost(command.Image, limit-cost)
		if part > limit-cost {
			return limit + 1
		}
		cost += part
	}
	return cost
}
