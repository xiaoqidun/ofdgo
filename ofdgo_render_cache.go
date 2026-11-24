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
	"sync/atomic"
	"time"
	"weak"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// defaultResourceCacheBytes 定义默认缓存总预算及类别扩展基准
const defaultResourceCacheBytes = 256 << 20

// defaultMaxEntryBytes 定义包内单条目默认最大解压字节数
const defaultMaxEntryBytes = 512 << 20

// ResourceOptions 设置共享计算、派生缓存与快照预算，字节预算不限制进程实际分配
// Parallelism为0时使用GOMAXPROCS，MemoryLimit为0时不指定计算预算
// CacheBytes为0时使用256MiB，SnapshotBytes为0时使用缓存预算的一半，负值禁用相应缓存
// MaxEntryBytes为0时单条目解压上限取512MiB，负值不限制单条目解压大小
// AvailableMemory返回可用于新增任务的字节数，同时作为缓存动态上限，需支持并发且不可重入Resources
type ResourceOptions struct {
	Parallelism     int
	MemoryLimit     int64
	CacheBytes      int64
	SnapshotBytes   int64
	MaxEntryBytes   int64
	AvailableMemory func() int64
}

// Resources 协调共享缓存与计算任务，通过NewResources创建，可跨阅读器使用，不可复制
// 缓存仅持有存活实例的弱引用，Clear释放派生数据，不关闭输入或取消任务
type Resources struct {
	options ResourceOptions
	pool    *ConversionPool
	mu      sync.Mutex
	used    atomic.Int64
	limit   atomic.Int64
	checked atomic.Int64
	order   list.List
}

// resourceNode 保存共享缓存成本、唯一扣减标记与弱引用淘汰操作
type resourceNode struct {
	cost    int64
	removed atomic.Bool
	element *list.Element
	evict   func()
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
	key   K
	value V
	cost  int
	node  *resourceNode
}

// NewResources 创建共享资源策略，不启动任务或修改进程全局配置
// 入参: options 并行及内存选项
// 返回: *Resources 共享策略, error 参数错误
func NewResources(options ResourceOptions) (*Resources, error) {
	pool, err := NewConversionPool(ConversionPoolOptions{Parallelism: options.Parallelism, MemoryLimit: options.MemoryLimit, AvailableMemory: options.AvailableMemory})
	if err != nil {
		return nil, err
	}
	cacheDisabled := options.CacheBytes < 0
	if options.CacheBytes == 0 {
		options.CacheBytes = defaultResourceCacheBytes
	}
	options.CacheBytes = max(0, options.CacheBytes)
	if options.SnapshotBytes == 0 {
		options.SnapshotBytes = options.CacheBytes / 2
	}
	if cacheDisabled {
		options.SnapshotBytes = max(0, options.SnapshotBytes)
	} else {
		options.SnapshotBytes = max(0, min(options.SnapshotBytes, options.CacheBytes))
	}
	if options.MaxEntryBytes == 0 {
		options.MaxEntryBytes = defaultMaxEntryBytes
	}
	resources := &Resources{options: options, pool: pool}
	resources.limit.Store(options.CacheBytes)
	return resources, nil
}

// WithResources 设置阅读器及派生编辑器共享的资源策略
// 入参: resources 共享策略，nil使用默认策略
// 返回: ReaderOption 阅读选项
func WithResources(resources *Resources) ReaderOption {
	return func(options *readerOptions) { options.resources = resources }
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
		part = baseimage.StorageCost(unwrapEncodedImage(command.Image), limit-cost)
		if part > limit-cost {
			return limit + 1
		}
		cost += part
	}
	return cost
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

// Clear 释放当前缓存，正在计算的任务完成后仍可写入新缓存
func (r *Resources) Clear() {
	for r.evictOne() {
	}
}

// CacheUsage 返回当前登记的派生缓存估算字节数，不含任务临时内存
// 返回: int64 缓存成本
func (r *Resources) CacheUsage() int64 {
	return r.used.Load()
}

// putLocked 在持有本地缓存锁时替换本地条目并按本地预算淘汰
// 入参: key 缓存键, value 缓存值, cost 字节成本
func (c *renderCache[K, V]) putLocked(key K, value V, cost int) {
	if item := c.entries[key]; item != nil {
		c.removeLocal(item)
	}
	if cost <= 0 || cost > c.limit {
		return
	}
	if c.entries == nil {
		c.entries = make(map[K]*list.Element)
	}
	for cost > c.limit-c.used {
		c.removeLocal(c.order.Back())
	}
	entry := renderCacheEntry[K, V]{key: key, value: value, cost: cost}
	item := c.order.PushFront(entry)
	c.entries[key] = item
	c.used += cost
}

// removeLocal 删除本地条目并在未扣减时归还全局字节计数，需持有本地缓存锁
// 入参: item 缓存条目
// 返回: *resourceNode 待从全局队列摘除的节点
func (c *renderCache[K, V]) removeLocal(item *list.Element) *resourceNode {
	entry := item.Value.(renderCacheEntry[K, V])
	c.used -= entry.cost
	c.order.Remove(item)
	delete(c.entries, entry.key)
	if entry.node != nil && entry.node.removed.CompareAndSwap(false, true) {
		c.resources.used.Add(-entry.node.cost)
		return entry.node
	}
	return nil
}

// clear 释放已有缓存条目并归还共享预算，不取消正在加载的任务
func (c *renderCache[K, V]) clear() {
	if c == nil {
		return
	}
	var detached []*resourceNode
	c.mu.Lock()
	for c.order.Len() != 0 {
		if node := c.removeLocal(c.order.Back()); node != nil {
			detached = append(detached, node)
		}
	}
	c.mu.Unlock()
	if c.resources != nil {
		c.resources.detachNodes(detached)
	}
}

// get 读取缓存并更新使用顺序
// 入参: key 缓存键
// 返回: V 缓存值, bool 是否命中
func (c *renderCache[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	r := c.resources
	if r != nil {
		r.refresh()
	}
	var detached []*resourceNode
	if cost <= 0 || cost > c.limit || r != nil && int64(cost) > r.limit.Load() {
		c.mu.Lock()
		if item := c.entries[key]; item != nil {
			if node := c.removeLocal(item); node != nil {
				detached = append(detached, node)
			}
		}
		c.mu.Unlock()
		if r != nil {
			r.detachNodes(detached)
		}
		return
	}
	if r != nil {
		r.reserve(int64(cost))
	}
	var added *resourceNode
	c.mu.Lock()
	if item := c.entries[key]; item != nil {
		if node := c.removeLocal(item); node != nil {
			detached = append(detached, node)
		}
	}
	if c.entries == nil {
		c.entries = make(map[K]*list.Element)
	}
	for cost > c.limit-c.used {
		if node := c.removeLocal(c.order.Back()); node != nil {
			detached = append(detached, node)
		}
	}
	entry := renderCacheEntry[K, V]{key: key, value: value, cost: cost}
	item := c.order.PushFront(entry)
	c.entries[key] = item
	c.used += cost
	if r != nil {
		ref, entryRef := weak.Make(c), weak.Make(item)
		var node *resourceNode
		node = &resourceNode{cost: int64(cost), evict: func() {
			cache := ref.Value()
			if cache == nil {
				return
			}
			cache.mu.Lock()
			defer cache.mu.Unlock()
			previous := entryRef.Value()
			if previous == nil {
				return
			}
			current := previous.Value.(renderCacheEntry[K, V])
			if cache.entries[current.key] != previous || current.node != node {
				return
			}
			cache.used -= current.cost
			cache.order.Remove(previous)
			delete(cache.entries, current.key)
		}}
		entry.node = node
		item.Value = entry
		r.used.Add(int64(cost))
		added = node
	}
	c.mu.Unlock()
	if r != nil {
		if len(detached) != 0 || added != nil {
			r.mu.Lock()
			for _, node := range detached {
				if node.element != nil {
					r.order.Remove(node.element)
					node.element = nil
				}
			}
			if added != nil && !added.removed.Load() {
				added.element = r.order.PushFront(added)
			}
			r.mu.Unlock()
		}
		if limit := r.limit.Load(); r.used.Load() > limit {
			r.trim(limit)
		}
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
		c.mu.Lock()
		if item := c.entries[key]; item != nil {
			c.order.MoveToFront(item)
			value := item.Value.(renderCacheEntry[K, V]).value
			c.mu.Unlock()
			return value, nil
		}
		if pending := c.loading[key]; pending != nil {
			c.mu.Unlock()
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
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.loading, key)
			close(pending.done)
			c.mu.Unlock()
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

// context 为未指定转换池的操作附加资源调度器
// 入参: ctx 操作上下文
// 返回: context.Context 计算上下文
func (r *Resources) context(ctx context.Context) context.Context {
	if r == nil || ctx.Value(conversionPoolKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, conversionPoolKey{}, r.pool)
}

// refresh 按当前可用内存节流刷新动态预算并在锁外收缩缓存
func (r *Resources) refresh() {
	if r.options.AvailableMemory == nil {
		return
	}
	now := time.Now().UnixNano()
	last := r.checked.Load()
	if last != 0 && now-last < int64(250*time.Millisecond) {
		return
	}
	if !r.checked.CompareAndSwap(last, now) {
		return
	}
	limit := min(r.options.CacheBytes, max(0, r.options.AvailableMemory()))
	r.limit.Store(limit)
	r.trim(limit)
}

// reserve 在写入前淘汰旧条目直到满足预算
// 入参: cost 待写入条目的估算字节数
func (r *Resources) reserve(cost int64) {
	limit := r.limit.Load()
	for r.used.Load()+cost > limit {
		if !r.evictOne() {
			return
		}
	}
}

// trim 将全局缓存使用量收缩到上限以内
// 入参: limit 目标缓存上限
func (r *Resources) trim(limit int64) {
	for r.used.Load() > limit {
		if !r.evictOne() {
			return
		}
	}
}

// detachNodes 批量从全局淘汰链表移除已本地删除的节点
// 入参: nodes 待移除节点切片
func (r *Resources) detachNodes(nodes []*resourceNode) {
	if len(nodes) == 0 {
		return
	}
	r.mu.Lock()
	for _, node := range nodes {
		if node.element != nil {
			r.order.Remove(node.element)
			node.element = nil
		}
	}
	r.mu.Unlock()
}

// evictOne 弹出最久未访问节点并在释放资源锁后执行本地淘汰
// 返回: bool 是否成功弹出节点
func (r *Resources) evictOne() bool {
	r.mu.Lock()
	item := r.order.Back()
	if item == nil {
		r.mu.Unlock()
		return false
	}
	node := item.Value.(*resourceNode)
	r.order.Remove(item)
	node.element = nil
	r.mu.Unlock()
	if node.removed.CompareAndSwap(false, true) {
		r.used.Add(-node.cost)
		node.evict()
	}
	return true
}
