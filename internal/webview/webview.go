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

//go:build !js || !wasm

package webview

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xiaoqidun/guigo"
	"github.com/xiaoqidun/ofdgo"
	webuiassets "github.com/xiaoqidun/ofdgo/assets/webui"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	"github.com/xiaoqidun/ofdgo/internal/build"
	"github.com/xiaoqidun/ofdgo/internal/webui"
)

// 应用版本检查、发布下载根地址、字体与原生内存资源缓存上限
const (
	applicationVersionURL  = "https://ofdgo.aite.me/"
	applicationDownloadURL = "https://ofdgo.aite.me/releases/"
	fontCacheMaxEntries    = 16
	fontCacheMaxBytes      = 64 << 20
	docResourceMaxBytes    = 256 << 20
	inlineSVGMaxBytes      = 128 << 10
	svgStreamMaxBytes      = 256 << 20
	svgStreamTTL           = 60 * time.Second
	engineCacheMaxBytes    = 192 << 20
)

// Host 连接桌面系统能力和共用文档业务
type Host struct {
	themeMu        sync.Mutex
	theme          applicationTheme
	themePath      string
	mu             sync.Mutex
	window         *guigo.Window
	files          *fileStore
	docResources   *docResourceStore
	svgStreams     *svgStreamStore
	engines        map[string]*webui.NativeEngine
	engineStops    map[string]func() bool
	closingEngines map[string]*engineClose
	conversionPool *ofdgo.ConversionPool
	resources      *ofdgo.Resources
	cancels        map[string]context.CancelFunc
	batchResults   map[string]any
	batchOutputs   map[string]batchOutput
	fontOnce       sync.Once
	fonts          []map[string]any
	fontFiles      map[string]string
	fontCache      fontDataCache
	fontError      error
	pendingPaths   []string
	launchMu       sync.Mutex
	launchActivate bool
	launchClosed   bool
	launchPending  int
	frontendReady  bool
	closeAllowed   bool
}

// Run 启动共用WebUI的原生入口
// 入参: ctx 运行上下文, paths 初始文件路径, dataDir 浏览器数据目录
// 返回: error 启动或运行错误
func Run(ctx context.Context, paths []string, dataDir string) error {
	options, err := windowOptions(dataDir)
	if err != nil {
		return err
	}
	paths = append([]string(nil), paths...)
	for index, path := range paths {
		paths[index], err = filepath.Abs(path)
		if err != nil {
			return err
		}
	}
	h := &Host{pendingPaths: paths, themePath: filepath.Join(options.UserDataDir, "appearance.json")}
	if err := h.loadTheme(); err != nil {
		return err
	}
	options.TitleBarColors = themeColors(h.theme.Dark)
	options.WindowMaterial = windowMaterial(h.theme.Material)
	data, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(options.UserDataDir, 0700); err != nil {
		return err
	}
	key, err := instanceDirectory(options.UserDataDir)
	if err != nil {
		return err
	}
	instance, err := guigo.OpenWindowInstance(ctx, "OFDGo:"+key, data, h.receiveLaunch)
	if err != nil || instance == nil {
		return err
	}
	defer instance.Close()
	options.Instance = instance
	files, err := newFileStore()
	if err != nil {
		return err
	}
	h.files = files
	h.docResources = newDocResourceStore(docResourceMaxBytes)
	h.svgStreams = newSVGStreamStore(svgStreamMaxBytes)
	h.engines = make(map[string]*webui.NativeEngine)
	h.cancels = make(map[string]context.CancelFunc)
	h.fontFiles = make(map[string]string)
	cacheBytes := int64(256 << 20)
	if memory := conversionMemory; memory != nil {
		cacheBytes = max(32<<20, memory()/8)
	}
	h.resources, err = ofdgo.NewResources(ofdgo.ResourceOptions{CacheBytes: cacheBytes, AvailableMemory: conversionMemory})
	if err != nil {
		files.close()
		return err
	}
	h.conversionPool = h.resources.ConversionPool()
	defer h.close()
	bindings := make(map[string]guigo.HandlerFunc)
	for _, name := range []string{"engine.open", "engine.close", "engine.call", "engine.start", "engine.cancel", "engine.finish", "batch", "open", "save", "directory", "temp.directory", "temp.remove", "file", "release", "drop.release", "child", "remove", "write.begin", "write.data", "write.commit", "write.abort", "fonts", "ready", "launch.done", "title", "window.info", "window.drag", "window.close", "appearance", "update.check", "update.open", "association.query", "association.register", "association.remove", "print.capabilities", "print.dialog", "print.printers", "print.query", "print.properties", "print"} {
		bindings["ofdgo.host."+name] = func(ctx context.Context, args any) (any, error) { return h.call(ctx, name, args) }
	}
	static := http.FileServerFS(webuiassets.NativeFS)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__native/") {
			if h.serveMemoryResource(w, r) {
				return
			}
			files.serve(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".wasm") || strings.HasSuffix(r.URL.Path, "wasm_exec.js") || strings.HasSuffix(r.URL.Path, "ofdgo_work.js") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			data, err := h.themeIndex()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("X-OFDGo-Build-Version", build.Version)
			w.Header().Set("Cache-Control", "no-store")
			w.Write(data)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-OFDGo-Build-Version", build.Version)
		static.ServeHTTP(w, r)
	})
	options.Handler = handler
	options.ResourceHandler = h.resource
	options.Bindings = bindings
	options.OnReady = func(window *guigo.Window) {
		h.mu.Lock()
		h.window = window
		h.mu.Unlock()
	}
	options.OnClose = func() {
		h.launchMu.Lock()
		h.launchClosed = true
		h.launchMu.Unlock()
		h.mu.Lock()
		for _, cancel := range h.cancels {
			cancel()
		}
		h.mu.Unlock()
	}
	options.OnDrop = h.openPaths
	options.OnTitleBarChanged = func(info guigo.TitleBarInfo) {
		h.emit("ofdgo.window", info)
	}
	options.OnClosing = func(ctx context.Context) bool {
		h.mu.Lock()
		allowed := h.closeAllowed || !h.frontendReady
		h.mu.Unlock()
		if !allowed {
			h.emit("ofdgo.closing", nil)
		}
		return allowed
	}
	options.OnError = func(err error) {
		h.emit("ofdgo.error", err.Error())
	}
	return guigo.Run(ctx, options)
}

// associationState 保存单个范围的登记状态和查询错误
type associationState struct {
	guigo.AssociationStatus
	Error string `json:"error,omitempty"`
}

// engineClose 保存并发关闭共享的完成通知及结果
type engineClose struct {
	done chan struct{}
	err  error
}

// batchOutput 保存已提交的批量转换目标信息
type batchOutput struct {
	destination string
	name        string
}

// fontCacheEntry 保存单个系统字体的缓存条目
type fontCacheEntry struct {
	key  string
	data []byte
}

// fontDataCache 按条目数和字节上限缓存系统字体数据
type fontDataCache struct {
	mu      sync.Mutex
	entries map[string]*fontCacheEntry
	order   []*fontCacheEntry
	bytes   int64
}

// get 查询缓存并在命中时刷新最近使用顺序
// 入参: key 字体缓存键
// 返回: []byte 字体数据, bool 是否命中
func (c *fontDataCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return nil, false
	}
	c.touch(entry)
	return entry.data, true
}

// put 写入字体缓存并按容量上限淘汰最久未使用的条目
// 入参: key 字体缓存键, data 字体数据
func (c *fontDataCache) put(key string, data []byte) {
	size := int64(len(data))
	if size > fontCacheMaxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*fontCacheEntry)
	}
	if entry := c.entries[key]; entry != nil {
		c.bytes -= int64(len(entry.data))
		entry.data = data
		c.bytes += size
		c.touch(entry)
		for len(c.order) > 1 && c.bytes > fontCacheMaxBytes {
			c.evict()
		}
		return
	}
	for len(c.order) > 0 && (len(c.order) >= fontCacheMaxEntries || c.bytes+size > fontCacheMaxBytes) {
		c.evict()
	}
	entry := &fontCacheEntry{key: key, data: data}
	c.entries[key] = entry
	c.order = append(c.order, entry)
	c.bytes += size
}

// touch 将缓存条目移至最近使用队尾
// 入参: entry 缓存条目
func (c *fontDataCache) touch(entry *fontCacheEntry) {
	for i, item := range c.order {
		if item == entry {
			copy(c.order[i:], c.order[i+1:])
			c.order[len(c.order)-1] = entry
			return
		}
	}
}

// evict 淘汰最久未使用的队头条目并清空引用
func (c *fontDataCache) evict() {
	evicted := c.order[0]
	c.order[0] = nil
	c.order = c.order[1:]
	delete(c.entries, evicted.key)
	c.bytes -= int64(len(evicted.data))
}

// clear 清空全部已缓存的系统字体数据
func (c *fontDataCache) clear() {
	c.mu.Lock()
	c.entries = nil
	c.order = nil
	c.bytes = 0
	c.mu.Unlock()
}

// docResourceItem 保存只读文档资源缓存中的单条数据及媒体类型
type docResourceItem struct {
	mime string
	data []byte
}

// docResourceBucket 保存单个文档令牌隔离的只读资源集合
type docResourceBucket struct {
	engineID string
	items    map[string]docResourceItem
	order    []string
	bytes    int64
}

// docResourceStore 维护按文档令牌隔离的只读内存资源池
type docResourceStore struct {
	mu           sync.RWMutex
	maxBytes     int64
	totalBytes   int64
	engineTokens map[string]string
	buckets      map[string]*docResourceBucket
}

// newDocResourceStore 创建带总字节上限的文档只读资源内存池
// 入参: maxBytes 内存池最大字节数
// 返回: *docResourceStore 文档只读资源池
func newDocResourceStore(maxBytes int64) *docResourceStore {
	return &docResourceStore{
		maxBytes:     maxBytes,
		engineTokens: make(map[string]string),
		buckets:      make(map[string]*docResourceBucket),
	}
}

// tokenFor 获取或初始化指定会话当前的文档资源隔离令牌
// 入参: engineID 会话标识
// 返回: string 文档资源令牌
func (s *docResourceStore) tokenFor(engineID string) string {
	s.mu.RLock()
	token := s.engineTokens[engineID]
	s.mu.RUnlock()
	if token != "" {
		return token
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if token = s.engineTokens[engineID]; token != "" {
		return token
	}
	token = rand.Text()
	s.engineTokens[engineID] = token
	s.buckets[token] = &docResourceBucket{engineID: engineID, items: make(map[string]docResourceItem)}
	return token
}

// rotate 切换指定会话的文档资源令牌并立即释放旧令牌占用的全部内存
// 入参: engineID 会话标识
// 返回: string 新文档资源令牌
func (s *docResourceStore) rotate(engineID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if oldToken := s.engineTokens[engineID]; oldToken != "" {
		if old := s.buckets[oldToken]; old != nil {
			s.totalBytes -= old.bytes
			delete(s.buckets, oldToken)
		}
	}
	token := rand.Text()
	s.engineTokens[engineID] = token
	s.buckets[token] = &docResourceBucket{engineID: engineID, items: make(map[string]docResourceItem)}
	return token
}

// put 将只读资源写入指定令牌桶并返回原生零拷贝直读路径
// 入参: engineID 会话标识, token 文档令牌, name 资源名称, mimeType 媒体类型, data 只读字节
// 返回: string 原生资源路径
func (s *docResourceStore) put(engineID, token, name, mimeType string, data []byte) string {
	if name == "" || token == "" || len(data) == 0 {
		return ""
	}
	resURL := "/__native/resource/" + token + "/" + url.PathEscape(name)
	size := int64(len(data))
	if s.maxBytes > 0 && size > s.maxBytes {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engineTokens[engineID] != token {
		return ""
	}
	bucket := s.buckets[token]
	if bucket == nil {
		return ""
	}
	if existing, ok := bucket.items[name]; ok && len(existing.data) > 0 {
		if existing.mime == "" && mimeType != "" {
			existing.mime = mimeType
			bucket.items[name] = existing
		}
		return resURL
	}
	for s.maxBytes > 0 && s.totalBytes+size > s.maxBytes {
		if !s.evictOneLocked(token) {
			return ""
		}
	}
	bucket.items[name] = docResourceItem{mime: mimeType, data: data}
	bucket.order = append(bucket.order, name)
	bucket.bytes += size
	s.totalBytes += size
	return resURL
}

// lookup 查询指定会话与令牌中已登记的资源路径及大小
// 入参: engineID 会话标识, token 文档令牌, name 资源名称
// 返回: string 资源路径, int 资源字节数, bool 是否命中
func (s *docResourceStore) lookup(engineID, token, name string) (string, int, bool) {
	if name == "" {
		return "", 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if token == "" {
		token = s.engineTokens[engineID]
	}
	if token == "" || s.engineTokens[engineID] != token {
		return "", 0, false
	}
	bucket := s.buckets[token]
	if bucket == nil {
		return "", 0, false
	}
	item, ok := bucket.items[name]
	if !ok || len(item.data) == 0 {
		return "", 0, false
	}
	return "/__native/resource/" + token + "/" + url.PathEscape(name), len(item.data), true
}

// get 按令牌和资源名只读获取内存资源内容
// 入参: token 文档令牌, name 资源名称
// 返回: docResourceItem 资源条目, bool 是否命中
func (s *docResourceStore) get(token, name string) (docResourceItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := s.buckets[token]
	if bucket == nil {
		return docResourceItem{}, false
	}
	item, ok := bucket.items[name]
	return item, ok
}

// clearEngine 释放指定会话持有的全部文档只读资源
// 入参: engineID 会话标识
func (s *docResourceStore) clearEngine(engineID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token := s.engineTokens[engineID]; token != "" {
		if bucket := s.buckets[token]; bucket != nil {
			s.totalBytes -= bucket.bytes
			delete(s.buckets, token)
		}
		delete(s.engineTokens, engineID)
	}
}

// clearAll 清空整个窗口持有的全部文档只读资源
func (s *docResourceStore) clearAll() {
	s.mu.Lock()
	s.engineTokens = make(map[string]string)
	s.buckets = make(map[string]*docResourceBucket)
	s.totalBytes = 0
	s.mu.Unlock()
}

// evictOneLocked 在持锁状态下按先进先出淘汰一条只读资源
// 入参: currentToken 当前写入令牌
// 返回: bool 是否成功淘汰一条资源
func (s *docResourceStore) evictOneLocked(currentToken string) bool {
	for token, bucket := range s.buckets {
		if token == currentToken || len(bucket.order) == 0 {
			continue
		}
		return s.evictBucketHeadLocked(bucket)
	}
	if bucket := s.buckets[currentToken]; bucket != nil && len(bucket.order) > 0 {
		return s.evictBucketHeadLocked(bucket)
	}
	return false
}

// evictBucketHeadLocked 在持锁状态下淘汰指定令牌桶队头资源
// 入参: bucket 目标令牌桶
// 返回: bool 是否成功淘汰
func (s *docResourceStore) evictBucketHeadLocked(bucket *docResourceBucket) bool {
	if len(bucket.order) == 0 {
		return false
	}
	name := bucket.order[0]
	copy(bucket.order, bucket.order[1:])
	bucket.order[len(bucket.order)-1] = ""
	bucket.order = bucket.order[:len(bucket.order)-1]
	if item, ok := bucket.items[name]; ok {
		size := int64(len(item.data))
		delete(bucket.items, name)
		bucket.bytes -= size
		s.totalBytes -= size
		return true
	}
	return false
}

// svgStreamItem 保存一次性大SVG自动释放流的内存数据
type svgStreamItem struct {
	engineID  string
	data      []byte
	createdAt time.Time
	reading   bool
}

// svgStreamStore 管理大SVG一次性内存流并在读完、超时或会话切换时自动回收
type svgStreamStore struct {
	mu         sync.Mutex
	maxBytes   int64
	totalBytes int64
	items      map[string]*svgStreamItem
	order      []string
}

// newSVGStreamStore 创建带容量保护的大SVG一次性自动释放流存储
// 入参: maxBytes 最大保留字节数
// 返回: *svgStreamStore 大SVG内存流存储
func newSVGStreamStore(maxBytes int64) *svgStreamStore {
	return &svgStreamStore{maxBytes: maxBytes, items: make(map[string]*svgStreamItem)}
}

// put 登记一次性大SVG字节流并返回直读路径
// 入参: engineID 会话标识, data SVG字节数据
// 返回: string 一次性SVG流路径
func (s *svgStreamStore) put(engineID string, data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.pruneExpiredLocked(now)
	size := int64(len(data))
	for s.maxBytes > 0 && len(s.order) > 0 && s.totalBytes+size > s.maxBytes {
		if !s.evictOldestLocked() {
			break
		}
	}
	id := rand.Text()
	s.items[id] = &svgStreamItem{engineID: engineID, data: data, createdAt: now}
	s.order = append(s.order, id)
	s.totalBytes += size
	return "/__native/svg/" + id
}

// acquire 获取大SVG只读字节数据，调用方读取完毕后可按需释放条目
// 入参: id 流标识, consume 是否标记正在消费
// 返回: []byte SVG字节, bool 是否命中
func (s *svgStreamStore) acquire(id string, consume bool) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.items[id]
	if item == nil {
		return nil, false
	}
	if consume {
		item.reading = true
	}
	return item.data, true
}

// delete 立即从内存释放指定大SVG流条目
// 入参: id 流标识
func (s *svgStreamStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteLocked(id)
}

// clearEngine 清理指定会话尚未读取完毕的全部大SVG流
// 入参: engineID 会话标识
func (s *svgStreamStore) clearEngine(engineID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.order[:0]
	for _, id := range s.order {
		item := s.items[id]
		if item == nil {
			continue
		}
		if item.engineID == engineID {
			s.totalBytes -= int64(len(item.data))
			item.data = nil
			delete(s.items, id)
			continue
		}
		kept = append(kept, id)
	}
	for i := len(kept); i < len(s.order); i++ {
		s.order[i] = ""
	}
	s.order = kept
}

// clearAll 清空全部大SVG内存流
func (s *svgStreamStore) clearAll() {
	s.mu.Lock()
	for id, item := range s.items {
		item.data = nil
		delete(s.items, id)
	}
	s.order = nil
	s.totalBytes = 0
	s.mu.Unlock()
}

// pruneExpiredLocked 清理超过生存时间且未在读取中的残留SVG流
// 入参: now 当前时间
func (s *svgStreamStore) pruneExpiredLocked(now time.Time) {
	kept := s.order[:0]
	for _, id := range s.order {
		item := s.items[id]
		if item == nil {
			continue
		}
		if !item.reading && now.Sub(item.createdAt) > svgStreamTTL {
			s.totalBytes -= int64(len(item.data))
			item.data = nil
			delete(s.items, id)
			continue
		}
		kept = append(kept, id)
	}
	for i := len(kept); i < len(s.order); i++ {
		s.order[i] = ""
	}
	s.order = kept
}

// evictOldestLocked 淘汰最早登记且未在读取中的SVG流
// 返回: bool 是否成功淘汰
func (s *svgStreamStore) evictOldestLocked() bool {
	for i, id := range s.order {
		item := s.items[id]
		if item == nil {
			copy(s.order[i:], s.order[i+1:])
			s.order[len(s.order)-1] = ""
			s.order = s.order[:len(s.order)-1]
			return true
		}
		if item.reading {
			continue
		}
		s.totalBytes -= int64(len(item.data))
		item.data = nil
		delete(s.items, id)
		copy(s.order[i:], s.order[i+1:])
		s.order[len(s.order)-1] = ""
		s.order = s.order[:len(s.order)-1]
		return true
	}
	return false
}

// deleteLocked 在持锁状态下删除指定SVG流条目
// 入参: id 流标识
func (s *svgStreamStore) deleteLocked(id string) {
	item := s.items[id]
	if item == nil {
		return
	}
	s.totalBytes -= int64(len(item.data))
	item.data = nil
	delete(s.items, id)
	for i, entryID := range s.order {
		if entryID == id {
			copy(s.order[i:], s.order[i+1:])
			s.order[len(s.order)-1] = ""
			s.order = s.order[:len(s.order)-1]
			return
		}
	}
}

// resourceReader 保存资源读取位置及关闭操作
type resourceReader struct {
	*io.SectionReader
	close func() error
}

// Close 结束本次资源读取
// 返回: error 关闭错误
func (r *resourceReader) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

// resource 将静态资源、只读内存文档资源、大SVG自动释放流及文件直接交给宿主资源流
// 入参: r 资源请求
// 返回: *guigo.Resource 资源响应, error 读取错误
func (h *Host) resource(r *http.Request) (*guigo.Resource, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, nil
	}
	for _, name := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range"} {
		if r.Header.Get(name) != "" {
			return nil, nil
		}
	}
	var reader io.ReaderAt
	var size int64
	var close func() error
	var contentType string
	cacheControl := "no-store"
	svgStreamID := ""
	transferred := false
	defer func() {
		if !transferred && close != nil {
			close()
		}
	}()
	if strings.HasPrefix(r.URL.Path, "/__native/resource/") {
		rest := strings.TrimPrefix(r.URL.Path, "/__native/resource/")
		token, rawName, ok := strings.Cut(rest, "/")
		if !ok || token == "" || rawName == "" || h.docResources == nil {
			return nil, nil
		}
		name, err := url.PathUnescape(rawName)
		if err != nil || name == "" {
			return nil, nil
		}
		item, ok := h.docResources.get(token, name)
		if !ok {
			return nil, nil
		}
		reader, size = bytes.NewReader(item.data), int64(len(item.data))
		contentType = item.mime
		cacheControl = "private, max-age=3600"
	} else if strings.HasPrefix(r.URL.Path, "/__native/svg/") {
		id := strings.TrimPrefix(r.URL.Path, "/__native/svg/")
		if id == "" || strings.Contains(id, "/") || h.svgStreams == nil {
			return nil, nil
		}
		data, ok := h.svgStreams.acquire(id, r.Method == http.MethodGet)
		if !ok {
			return nil, nil
		}
		reader, size = bytes.NewReader(data), int64(len(data))
		contentType = "image/svg+xml; charset=utf-8"
		svgStreamID = id
	} else if strings.HasPrefix(r.URL.Path, "/__native/data/") {
		var err error
		reader, size, close, err = h.files.resource(strings.TrimPrefix(r.URL.Path, "/__native/data/"))
		if err != nil {
			return nil, nil
		}
	} else {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		var data []byte
		var err error
		if name == "index.html" {
			data, err = h.themeIndex()
		} else {
			data, err = webuiassets.NativeFS.ReadFile(name)
		}
		if err != nil {
			return nil, nil
		}
		reader, size = bytes.NewReader(data), int64(len(data))
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(r.URL.Path))
	}
	if contentType == "" {
		header := make([]byte, min(size, 512))
		_, err := reader.ReadAt(header, 0)
		if err != nil && err != io.EOF {
			return nil, err
		}
		contentType = http.DetectContentType(header)
	}
	headers := http.Header{"Content-Type": {contentType}, "Content-Length": {strconv.FormatInt(size, 10)}, "Cache-Control": {cacheControl}, "Accept-Ranges": {"bytes"}}
	headers.Set("X-OFDGo-Build-Version", build.Version)
	start, length, status := int64(0), size, http.StatusOK
	if value := r.Header.Get("Range"); value != "" && r.Method == http.MethodGet {
		if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
			return nil, nil
		}
		first, last, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
		if !ok {
			return nil, nil
		}
		if first == "" {
			count, err := strconv.ParseInt(last, 10, 64)
			if err != nil || count <= 0 {
				return nil, nil
			}
			length = min(count, size)
			start = size - length
		} else {
			var err error
			start, err = strconv.ParseInt(first, 10, 64)
			if err != nil || start < 0 || start >= size {
				return nil, nil
			}
			end := size - 1
			if last != "" {
				end, err = strconv.ParseInt(last, 10, 64)
				if err != nil || end < start {
					return nil, nil
				}
			}
			length = min(end, size-1) - start + 1
		}
		if length == 0 {
			return nil, nil
		}
		status = http.StatusPartialContent
		headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
		headers.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	if svgStreamID != "" && r.Method == http.MethodGet && start+length >= size {
		var once sync.Once
		id := svgStreamID
		close = func() error {
			once.Do(func() { h.svgStreams.delete(id) })
			return nil
		}
	}
	if r.Method == http.MethodHead {
		length = 0
	}
	transferred = true
	return &guigo.Resource{Body: &resourceReader{SectionReader: io.NewSectionReader(reader, start, length), close: close}, StatusCode: status, Header: headers}, nil
}

// serveMemoryResource 通过HTTP回退通道直读文档只读内存资源或一次性大SVG流
// 入参: w 响应写入器, r HTTP请求
// 返回: bool 是否已由内存资源通道处理
func (h *Host) serveMemoryResource(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/__native/resource/") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		rest := strings.TrimPrefix(r.URL.Path, "/__native/resource/")
		token, rawName, ok := strings.Cut(rest, "/")
		if !ok || token == "" || rawName == "" || h.docResources == nil {
			http.NotFound(w, r)
			return true
		}
		name, err := url.PathUnescape(rawName)
		if err != nil || name == "" {
			http.NotFound(w, r)
			return true
		}
		item, ok := h.docResources.get(token, name)
		if !ok {
			http.NotFound(w, r)
			return true
		}
		contentType := item.mime
		if contentType == "" {
			contentType = http.DetectContentType(item.data[:min(len(item.data), 512)])
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("X-OFDGo-Build-Version", build.Version)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(item.data))
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/__native/svg/") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		id := strings.TrimPrefix(r.URL.Path, "/__native/svg/")
		if id == "" || strings.Contains(id, "/") || h.svgStreams == nil {
			http.NotFound(w, r)
			return true
		}
		data, ok := h.svgStreams.acquire(id, r.Method == http.MethodGet)
		if !ok {
			http.NotFound(w, r)
			return true
		}
		if r.Method == http.MethodGet {
			defer h.svgStreams.delete(id)
		}
		w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-OFDGo-Build-Version", build.Version)
		http.ServeContent(w, r, id+".svg", time.Time{}, bytes.NewReader(data))
		return true
	}
	return false
}

// emit 发送当前窗口事件
// 入参: name 事件名称, value 事件数据
func (h *Host) emit(name string, value any) {
	_ = h.emitError(name, value)
}

// emitError 发送当前窗口事件并返回投递错误
// 入参: name 事件名称, value 事件数据
// 返回: error 投递错误
func (h *Host) emitError(name string, value any) error {
	h.mu.Lock()
	w := h.window
	h.mu.Unlock()
	if w == nil {
		return guigo.ErrClosed
	}
	return w.Emit(name, value)
}

// openPaths 将操作系统提供的文件登记后交给界面
// 入参: names 文件路径列表
func (h *Host) openPaths(names []string) {
	_, _ = h.sendPaths(names, "ofdgo.files")
}

// sendPaths 将文件登记后发送到指定界面入口
// 入参: names 文件路径列表, event 界面事件名称
// 返回: bool 是否实际发射事件, error 投递错误
func (h *Host) sendPaths(names []string, event string) (bool, error) {
	files := make([]any, 0, len(names))
	for _, name := range names {
		file, err := h.files.addDrop(name)
		if err != nil {
			h.emit("ofdgo.error", err.Error())
			continue
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return false, nil
	}
	err := h.emitError(event, files)
	if err == nil {
		return true, nil
	}
	for _, file := range files {
		h.files.discardDrop(file.(map[string]any)["nativeFile"].(string))
	}
	return false, err
}

// receiveLaunch 接收已有进程转交的文件及激活请求
// 入参: ctx 请求上下文, data 文件路径消息
// 返回: error 接收错误
func (h *Host) receiveLaunch(ctx context.Context, data []byte) error {
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return err
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return errors.New("file path must be absolute")
		}
	}
	h.launchMu.Lock()
	defer h.launchMu.Unlock()
	if h.launchClosed {
		return guigo.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	h.pendingPaths = append(h.pendingPaths, paths...)
	h.launchActivate = true
	return h.flushLaunch()
}

// flushLaunch 在界面就绪后按接收顺序投递外部打开请求，调用方持有launchMu
// 返回: error 投递错误
func (h *Host) flushLaunch() error {
	h.mu.Lock()
	ready, window := h.frontendReady, h.window
	h.mu.Unlock()
	if !ready || window == nil {
		return nil
	}
	if h.launchActivate {
		if err := window.Activate(); err != nil {
			return err
		}
		h.launchActivate = false
	}
	if len(h.pendingPaths) == 0 {
		return nil
	}
	emitted, err := h.sendPaths(h.pendingPaths, "ofdgo.launch")
	if err != nil {
		return err
	}
	h.pendingPaths = nil
	if emitted {
		h.launchPending++
	}
	return nil
}

// call 执行宿主协议并保留业务结果结构
// 入参: ctx 调用上下文, name 方法名称, value 已解码参数
// 返回: any 调用结果, error 执行错误
func (h *Host) call(ctx context.Context, name string, value any) (any, error) {
	args, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("参数无效")
	}
	text := func(key string) string { value, _ := args[key].(string); return value }
	flag := func(key string) bool { value, _ := args[key].(bool); return value }
	switch name {
	case "appearance":
		return nil, h.setTheme(applicationTheme{Mode: text("mode"), Dark: flag("dark"), Material: text("material")})
	case "print.capabilities", "print.dialog", "print.printers", "print.query", "print.properties", "print":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, fmt.Errorf("窗口尚未就绪")
		}
		if name == "print.capabilities" {
			return window.PrintCapabilities(ctx)
		}
		if name == "print.dialog" {
			return nil, window.ShowPrintUI(ctx, text("kind"))
		}
		if name == "print.printers" {
			return window.ListPrinters(ctx)
		}
		if name == "print.properties" {
			return nil, window.ShowPrinterProperties(ctx, text("printer"))
		}
		data, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		var options guigo.PrintOptions
		if err := json.Unmarshal(data, &options); err != nil {
			return nil, err
		}
		profile, err := window.QueryPrinter(ctx, options)
		if err != nil {
			return nil, err
		}
		if name == "print.query" {
			return profile, nil
		}
		if text("fingerprint") == "" || text("fingerprint") != profile.Fingerprint {
			return nil, fmt.Errorf("打印机设置已变化，请重新预览")
		}
		return window.Print(ctx, options)
	case "engine.open":
		id := rand.Text()
		if err := h.files.openHolder(id); err != nil {
			return nil, err
		}
		engine := webui.NewNativeEngine(func(token string) (io.ReaderAt, int64, error) { return h.files.resolveHolder(id, token) })
		parallelism, cacheBytes := max(2, runtime.GOMAXPROCS(0)), int64(engineCacheMaxBytes)
		if memory := conversionMemory; memory != nil {
			available := memory()
			cacheBytes = min(int64(engineCacheMaxBytes), max(64<<20, available/16))
		}
		engine.RenderParallelism = parallelism
		engine.ConversionPool = h.conversionPool
		engine.Resources = h.resources
		engine.OpenWriter = h.openWriter
		engine.ReserveOutput = h.reserveOutput
		engine.CleanupOutput = h.cleanupOutput
		engine.LoadFont = h.loadFont
		if h.docResources != nil {
			h.docResources.tokenFor(id)
		}
		h.mu.Lock()
		h.engines[id] = engine
		if h.engineStops == nil {
			h.engineStops = make(map[string]func() bool)
		}
		h.engineStops[id] = context.AfterFunc(guigo.PageContext(ctx), func() { _ = h.closeEngine(id) })
		h.mu.Unlock()
		return guigo.Result{Value: map[string]any{"engine": id, "parallelism": parallelism, "cacheBytes": cacheBytes}, Discard: func() { _ = h.closeEngine(id) }}, nil
	case "batch":
		return map[string]any{"parallelism": h.conversionPool.Parallelism()}, nil
	case "update.check":
		return applicationUpdate(ctx, build.Version, applicationVersionURL)
	case "update.open":
		return nil, guigo.OpenURL(applicationDownloadURL)
	case "association.query":
		return queryAssociations()
	case "association.register", "association.remove":
		return nil, changeAssociations(text("scope"), name == "association.remove")
	case "engine.close":
		return nil, h.closeEngine(text("engine"))
	case "engine.finish":
		key := text("engine") + "/" + fmt.Sprint(args["id"])
		h.mu.Lock()
		delete(h.batchResults, key)
		output, ok := h.batchOutputs[key]
		delete(h.batchOutputs, key)
		h.mu.Unlock()
		if ok {
			h.files.disown(output.destination, output.name)
		}
		return nil, nil
	case "engine.cancel":
		key := text("engine") + "/" + fmt.Sprint(args["id"])
		h.mu.Lock()
		cancel := h.cancels[key]
		h.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil, nil
	case "engine.start":
		operationName := text("name")
		if operationName != "ofdgoConvertFile" && operationName != "ofdgoPackFiles" {
			return nil, fmt.Errorf("unsupported batch operation")
		}
		id := text("engine")
		key := id + "/" + fmt.Sprint(args["id"])
		page := guigo.PageContext(ctx)
		operation, cancel := context.WithCancel(page)
		h.mu.Lock()
		engine := h.engines[id]
		_, duplicate := h.cancels[key]
		if engine == nil || duplicate {
			h.mu.Unlock()
			cancel()
			return nil, fmt.Errorf("invalid batch operation")
		}
		h.cancels[key] = cancel
		h.mu.Unlock()
		values, _ := args["args"].([]any)
		destID := ""
		if operationName == "ofdgoConvertFile" && len(values) > 3 {
			destID = handleID(values[3])
		}
		go func() {
			defer cancel()
			defer func() { h.mu.Lock(); delete(h.cancels, key); h.mu.Unlock() }()
			finishCall := h.files.beginHolderCall(id, operationName, values)
			defer finishCall(false)
			outputName := ""
			result := engine.Call(operation, operationName, values, func(event any) {
				if value, ok := event.(map[string]any); ok {
					if value["type"] == "output" {
						if out, ok := value["name"].(string); ok {
							outputName = out
						}
					}
					value["id"] = args["id"]
					h.emit("ofdgo.engine."+id, value)
				}
			})
			succeeded := result != nil && result["ok"] == true
			finishCall(succeeded)
			encoded := h.encode(page, result)
			unchanged := false
			if succeeded {
				if data, ok := result["data"].(map[string]any); ok {
					unchanged, _ = data["unchanged"].(bool)
				}
			}
			h.mu.Lock()
			if page.Err() != nil || h.engines[id] != engine {
				h.mu.Unlock()
				h.discard(encoded)
				return
			}
			if h.batchResults == nil {
				h.batchResults = make(map[string]any)
			}
			h.batchResults[key] = encoded
			if succeeded && !unchanged && destID != "" && outputName != "" {
				if h.batchOutputs == nil {
					h.batchOutputs = make(map[string]batchOutput)
				}
				h.batchOutputs[key] = batchOutput{destination: destID, name: outputName}
			}
			h.mu.Unlock()
			if err := h.emitError("ofdgo.engine."+id, map[string]any{"id": args["id"], "type": "result", "result": encoded}); err != nil {
				h.mu.Lock()
				_, exists := h.batchResults[key]
				delete(h.batchResults, key)
				delete(h.batchOutputs, key)
				h.mu.Unlock()
				if exists {
					h.discard(encoded)
				}
			}
		}()
		return guigo.Result{Value: true, Discard: cancel}, nil
	case "engine.call":
		id := text("engine")
		key := id + "/" + fmt.Sprint(args["id"])
		operation, cancel := context.WithCancel(ctx)
		defer cancel()
		h.mu.Lock()
		engine := h.engines[id]
		if _, exists := h.cancels[key]; exists {
			h.mu.Unlock()
			return nil, fmt.Errorf("请求重复")
		}
		h.cancels[key] = cancel
		h.mu.Unlock()
		defer func() { h.mu.Lock(); delete(h.cancels, key); h.mu.Unlock() }()
		if engine == nil {
			return nil, fmt.Errorf("会话已关闭")
		}
		callName := text("name")
		values, _ := args["args"].([]any)
		docToken := ""
		if h.docResources != nil {
			docToken = h.docResources.tokenFor(id)
			if callName == "ofdgoSVGFontData" && len(values) > 0 {
				if fontName, ok := values[0].(string); ok && fontName != "" {
					if resURL, size, hit := h.docResources.lookup(id, docToken, fontName); hit {
						return guigo.Result{Value: map[string]any{"ok": true, "data": map[string]any{"url": resURL, "size": size}}}, nil
					}
				}
			}
		}
		finishCall := h.files.beginHolderCall(id, callName, values)
		defer finishCall(false)
		result := engine.Call(operation, callName, values, func(event any) {
			if value, ok := event.(map[string]any); ok {
				value["id"] = args["id"]
				h.emit("ofdgo.engine."+id, value)
			}
		})
		succeeded := result != nil && result["ok"] == true
		finishCall(succeeded)
		if succeeded && rotatesDocResources(callName) {
			if h.docResources != nil {
				h.docResources.rotate(id)
			}
			if h.svgStreams != nil {
				h.svgStreams.clearEngine(id)
			}
		}
		pageCtx := guigo.PageContext(ctx)
		var encoded any
		if succeeded && callName == "ofdgoRenderPage" {
			encoded = h.encodeRenderPage(pageCtx, id, docToken, result)
		} else if succeeded && callName == "ofdgoSVGFontData" {
			encoded = h.encodeSVGFontData(pageCtx, id, docToken, values, result)
		} else {
			encoded = h.encode(pageCtx, result)
		}
		return guigo.Result{Value: encoded, Discard: func() { h.discard(encoded) }}, nil
	case "open", "save", "directory":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, fmt.Errorf("窗口尚未就绪")
		}
		options := guigo.DialogOptions{DefaultName: text("suggestedName"), Filters: dialogFilters(args)}
		if name == "open" {
			var names []string
			var err error
			if flag("directory") {
				var directory string
				directory, err = window.OpenDirectory(options)
				if err == nil {
					allowed := make(map[string]bool)
					for _, filter := range options.Filters {
						if filter.Pattern == "*.*" {
							continue
						}
						for _, item := range strings.Split(filter.Pattern, ";") {
							if ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item)), "*"); strings.HasPrefix(ext, ".") {
								allowed[ext] = true
							}
						}
					}
					err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
						if walkErr != nil {
							if path != directory && errors.Is(walkErr, fs.ErrPermission) {
								if entry != nil && entry.IsDir() {
									return fs.SkipDir
								}
								return nil
							}
							return walkErr
						}
						if err := ctx.Err(); err != nil {
							return err
						}
						regular := entry.Type().IsRegular()
						if !regular && entry.Type()&fs.ModeSymlink != 0 {
							if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
								regular = true
							}
						}
						if regular {
							if len(allowed) != 0 && !allowed[strings.ToLower(filepath.Ext(entry.Name()))] {
								return nil
							}
							if len(names) >= 10000 {
								return fmt.Errorf("目录文件数量超出单次选择上限")
							}
							names = append(names, path)
						}
						return nil
					})
				}
			} else if flag("multiple") {
				names, err = window.OpenFiles(options)
			} else {
				var file string
				file, err = window.OpenFile(options)
				if err == nil {
					names = []string{file}
				}
			}
			if err != nil {
				return nil, dialogError(err)
			}
			result := make([]any, 0, len(names))
			registered := make([]string, 0, len(names)*2)
			writable := !flag("directory")
			for _, file := range names {
				handle, err := h.files.add(file, false, writable)
				if err != nil {
					h.files.discardEntries(registered)
					return nil, err
				}
				id := handle["nativeHandle"].(string)
				registered = append(registered, id)
				value, err := h.files.file(id)
				if err != nil {
					h.files.discardEntries(registered)
					return nil, err
				}
				if fileID, ok := value["nativeFile"].(string); ok && fileID != id {
					registered = append(registered, fileID)
				}
				result = append(result, value)
			}
			return result, nil
		}
		if name == "save" {
			file, err := window.SaveFile(options)
			if err != nil {
				return nil, dialogError(err)
			}
			return h.files.save(file)
		}
		file, err := window.OpenDirectory(options)
		if err != nil {
			return nil, dialogError(err)
		}
		return h.files.add(file, true, false)
	case "temp.directory":
		return h.files.tempDirectory()
	case "temp.remove":
		return nil, h.files.removeTempDirectory(text("id"))
	case "file":
		return h.files.file(text("handle"))
	case "release":
		id := text("file")
		if id == "" {
			id = text("handle")
		}
		h.files.release(id)
		return nil, nil
	case "drop.release":
		h.files.discardDrop(text("file"))
		return nil, nil
	case "child":
		value, err := h.files.child(text("handle"), text("name"), flag("directory"), flag("create"))
		if errors.Is(err, os.ErrNotExist) {
			return nil, &guigo.BridgeError{Name: "NotFoundError", Message: err.Error()}
		}
		return value, err
	case "remove":
		return nil, h.files.remove(text("handle"), text("name"), flag("recursive"))
	case "write.begin":
		writer, err := h.files.begin(guigo.PageContext(ctx), text("handle"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"writer": writer.id}, nil
	case "write.data", "write.commit", "write.abort":
		id := text("writer")
		h.files.mu.Lock()
		writer := h.files.writers[id]
		h.files.mu.Unlock()
		if writer == nil {
			return nil, fmt.Errorf("写入已结束")
		}
		if name == "write.commit" {
			return nil, writer.Close()
		}
		if name == "write.abort" {
			return nil, writer.Abort()
		}
		if data, ok := args["data"].([]byte); ok {
			_, err := writer.Write(data)
			return nil, err
		}
		descriptor, _ := args["data"].(map[string]any)
		key, _ := descriptor["nativeFile"].(string)
		input, size, release, err := h.files.acquire(key)
		if err != nil {
			return nil, err
		}
		defer release()
		_, err = io.Copy(writer, io.NewSectionReader(input, 0, size))
		return nil, err
	case "fonts":
		h.fontOnce.Do(h.readFonts)
		return h.fonts, h.fontError
	case "title":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, nil
		}
		return nil, window.SetTitle(text("title"))
	case "window.info", "window.drag", "window.close":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, fmt.Errorf("窗口尚未就绪")
		}
		if name == "window.info" {
			return window.TitleBarInfo()
		}
		if name == "window.close" {
			h.launchMu.Lock()
			defer h.launchMu.Unlock()
			if h.launchPending != 0 || len(h.pendingPaths) != 0 {
				return false, nil
			}
			h.launchClosed = true
			h.mu.Lock()
			h.closeAllowed = true
			h.mu.Unlock()
			return true, window.Close()
		}
		values, ok := args["regions"].([]any)
		if !ok {
			return nil, fmt.Errorf("拖动区域无效")
		}
		regions := make([]guigo.Rect, 0, len(values))
		for _, value := range values {
			item, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("拖动区域无效")
			}
			x, xOK := item["x"].(float64)
			y, yOK := item["y"].(float64)
			width, widthOK := item["width"].(float64)
			height, heightOK := item["height"].(float64)
			if !xOK || !yOK || !widthOK || !heightOK {
				return nil, fmt.Errorf("拖动区域无效")
			}
			regions = append(regions, guigo.Rect{X: x, Y: y, Width: width, Height: height})
		}
		return nil, window.SetDragRegions(regions)
	case "launch.done":
		h.launchMu.Lock()
		if h.launchPending > 0 {
			h.launchPending--
		}
		h.launchMu.Unlock()
		return nil, nil
	case "ready":
		h.launchMu.Lock()
		defer h.launchMu.Unlock()
		h.mu.Lock()
		h.frontendReady = true
		h.mu.Unlock()
		return nil, h.flushLaunch()
	}
	return nil, fmt.Errorf("未知宿主操作")
}

// encodeRenderPage 对页面渲染结果执行零拷贝编码，图片与字体直连只读内存池，大SVG转为一次性自动释放流
// 入参: ctx 页面上下文, engineID 会话标识, docToken 文档资源令牌, result 原生渲染结果
// 返回: any 零拷贝编码后的渲染响应
func (h *Host) encodeRenderPage(ctx context.Context, engineID, docToken string, result map[string]any) any {
	data, ok := result["data"].(map[string]any)
	if !ok {
		return h.encode(ctx, result)
	}
	out := make(map[string]any, len(data)+2)
	for key, item := range data {
		switch key {
		case "images":
			out[key] = h.encodeRenderImages(ctx, engineID, docToken, item)
		case "fonts":
			out[key] = h.encodeRenderFonts(engineID, docToken, item)
		case "text":
			out[key] = rawJSONValue(item, "{}")
		case "annotations":
			out[key] = rawJSONValue(item, "[]")
		case "svg":
			if svgStr, isStr := item.(string); isStr && len(svgStr) > inlineSVGMaxBytes && h.svgStreams != nil {
				out["svgUrl"] = h.svgStreams.put(engineID, []byte(svgStr))
				out["svgBytes"] = len(svgStr)
			} else {
				out[key] = item
			}
		default:
			out[key] = h.encode(ctx, item)
		}
	}
	encoded := make(map[string]any, len(result))
	for key, item := range result {
		if key == "data" {
			encoded[key] = out
		} else {
			encoded[key] = h.encode(ctx, item)
		}
	}
	return encoded
}

// encodeRenderImages 将渲染结果中的图片字节登记到只读文档内存池并返回直读地址
// 入参: ctx 页面上下文, engineID 会话标识, docToken 文档资源令牌, value 图片列表
// 返回: any 零拷贝图片描述列表
func (h *Host) encodeRenderImages(ctx context.Context, engineID, docToken string, value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, len(items))
	for i, item := range items {
		imageMap, ok := item.(map[string]any)
		if !ok {
			out[i] = item
			continue
		}
		name, _ := imageMap["name"].(string)
		mimeType, _ := imageMap["mime"].(string)
		entry := map[string]any{"name": name, "mime": mimeType}
		if raw, ok := imageMap["bytes"].([]byte); ok && len(raw) > 0 {
			if h.docResources != nil {
				if resURL := h.docResources.put(engineID, docToken, name, mimeType, raw); resURL != "" {
					entry["url"] = resURL
					entry["size"] = len(raw)
					out[i] = entry
					continue
				}
			}
			entry["bytes"] = h.encode(ctx, raw)
		} else if h.docResources != nil {
			if resURL, size, hit := h.docResources.lookup(engineID, docToken, name); hit {
				entry["url"] = resURL
				entry["size"] = size
			}
		}
		out[i] = entry
	}
	return out
}

// encodeRenderFonts 将渲染结果中的字体字节登记到只读文档内存池并返回直读地址
// 入参: engineID 会话标识, docToken 文档资源令牌, value 字体列表
// 返回: any 零拷贝字体描述列表
func (h *Host) encodeRenderFonts(engineID, docToken string, value any) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, len(items))
	for i, item := range items {
		fontMap, ok := item.(map[string]any)
		if !ok {
			out[i] = item
			continue
		}
		name, _ := fontMap["name"].(string)
		entry := make(map[string]any, len(fontMap)+2)
		for k, v := range fontMap {
			if k == "data" || k == "bytes" {
				continue
			}
			entry[k] = v
		}
		raw, _ := fontMap["data"].([]byte)
		if len(raw) == 0 {
			raw, _ = fontMap["bytes"].([]byte)
		}
		if len(raw) > 0 && h.docResources != nil {
			if resURL := h.docResources.put(engineID, docToken, name, fontMIME(raw), raw); resURL != "" {
				entry["url"] = resURL
				entry["size"] = len(raw)
			}
		} else if h.docResources != nil {
			if resURL, size, hit := h.docResources.lookup(engineID, docToken, name); hit {
				entry["url"] = resURL
				entry["size"] = size
			}
		}
		out[i] = entry
	}
	return out
}

// encodeSVGFontData 将按需请求的字体字节登记到只读文档内存池并返回直读地址
// 入参: ctx 页面上下文, engineID 会话标识, docToken 文档资源令牌, args 请求参数, result 原生响应
// 返回: any 编码后的字体响应
func (h *Host) encodeSVGFontData(ctx context.Context, engineID, docToken string, args []any, result map[string]any) any {
	data, ok := result["data"].(map[string]any)
	if !ok || h.docResources == nil || len(args) == 0 {
		return h.encode(ctx, result)
	}
	fontName, _ := args[0].(string)
	raw, _ := data["bytes"].([]byte)
	if fontName == "" || len(raw) == 0 {
		return h.encode(ctx, result)
	}
	resURL := h.docResources.put(engineID, docToken, fontName, fontMIME(raw), raw)
	if resURL == "" {
		return h.encode(ctx, result)
	}
	return map[string]any{"ok": true, "data": map[string]any{"url": resURL, "size": len(raw)}}
}

// encode 将二进制输出登记为页面资源
// 入参: ctx 页面上下文, value 原生调用结果
// 返回: any 包含资源描述的调用结果
func (h *Host) encode(ctx context.Context, value any) any {
	switch v := value.(type) {
	case []byte:
		return h.files.bytes(ctx, v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = h.encode(ctx, item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = h.encode(ctx, item)
		}
		return out
	default:
		return value
	}
}

// discard 释放页面未接收的临时结果及大SVG自动释放流
// 入参: value 包含资源描述的调用结果
func (h *Host) discard(value any) {
	switch v := value.(type) {
	case map[string]any:
		if svgURL, ok := v["svgUrl"].(string); ok && strings.HasPrefix(svgURL, "/__native/svg/") && h.svgStreams != nil {
			h.svgStreams.delete(strings.TrimPrefix(svgURL, "/__native/svg/"))
		}
		if id, ok := v["nativeFile"].(string); ok && v["temporary"] == true {
			h.files.release(id)
			return
		}
		for _, item := range v {
			h.discard(item)
		}
	case []any:
		for _, item := range v {
			h.discard(item)
		}
	}
}

// openWriter 将授权目标转换为原生事务写入器
// 入参: ctx 调用上下文, value 目标句柄, name 输出文件名
// 返回: io.WriteCloser 事务写入器, error 创建错误
func (h *Host) openWriter(ctx context.Context, value any, name string) (io.WriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := handleID(value)
	h.files.mu.Lock()
	entry := h.files.entries[id]
	directory := entry != nil && entry.directory
	h.files.mu.Unlock()
	if directory {
		parts := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
		for i, part := range parts {
			child, err := h.files.child(id, part, i < len(parts)-1, true)
			if err != nil {
				return nil, err
			}
			id = child["nativeHandle"].(string)
		}
		h.files.mu.Lock()
		owned := h.files.entries[id] != nil && h.files.entries[id].owned
		h.files.mu.Unlock()
		if !owned {
			return nil, fmt.Errorf("输出文件已存在")
		}
	}
	return h.files.begin(ctx, id)
}

// reserveOutput 为一次转换预留独立输出文件或目录
// 入参: ctx 调用上下文, request 输出请求
// 返回: webui.NativeOutputTarget 预留目标, error 创建错误
func (h *Host) reserveOutput(ctx context.Context, request webui.NativeOutputRequest) (webui.NativeOutputTarget, error) {
	if err := ctx.Err(); err != nil {
		return webui.NativeOutputTarget{}, err
	}
	id := handleID(request.Destination)
	base, name, err := h.files.reserve(id, request.Base, request.Extension, request.Paged)
	return webui.NativeOutputTarget{Destination: request.Destination, Base: base, Name: name}, err
}

// cleanupOutput 回滚本次转换创建的输出入口
// 入参: ctx 调用上下文, target 预留目标
// 返回: error 清理错误
func (h *Host) cleanupOutput(ctx context.Context, target webui.NativeOutputTarget) error {
	return h.files.remove(handleID(target.Destination), target.Name, true)
}

// readFonts 建立系统字体目录并登记按需读取资源
func (h *Host) readFonts() {
	platform, language := fontNameLanguage()
	priorities := make(map[string]int)
	for _, name := range ofdgo.FontSystemFiles() {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			continue
		}
		faces, err := basefont.ReadLocalizedFacesAt(f, info.Size(), platform, language)
		_ = f.Close()
		if err != nil {
			continue
		}
		file, err := h.files.addFont(name, info.Size(), info.ModTime().UnixMilli())
		if err != nil {
			continue
		}
		for _, face := range faces {
			h.fonts = append(h.fonts, map[string]any{"index": face.Index, "postscriptName": face.PostScriptName, "fullName": face.FullName, "family": face.Family, "style": face.Style, "names": face.Names, "menuFamily": face.MenuFamily, "menuStyle": face.MenuStyle, "familyKey": face.FamilyKey, "bold": face.Bold, "italic": face.Italic, "file": file})
			priority := 1
			if !face.Bold && !face.Italic {
				priority = 3
			} else if !face.Bold {
				priority = 2
			}
			for _, alias := range append([]string{face.FullName, face.Family, face.PostScriptName, strings.TrimSpace(face.Family + " " + face.Style)}, face.Names...) {
				if alias == "" {
					continue
				}
				key := alias + ".ttf"
				if priority > priorities[key] {
					h.fontFiles[key] = name + "\x00" + strconv.Itoa(face.Index)
					priorities[key] = priority
				}
			}
		}
	}
}

// loadFont 按转换请求读取并缓存单个系统字体
// 入参: ctx 调用上下文, name 字体别名对应的文件名
// 返回: []byte 字体数据, error 读取错误
func (h *Host) loadFont(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.fontOnce.Do(h.readFonts)
	cacheKey := h.fontFiles[name]
	parts := strings.Split(cacheKey, "\x00")
	if len(parts) != 2 {
		return nil, fmt.Errorf("系统字体不可用")
	}
	if data, ok := h.fontCache.get(cacheKey); ok {
		return data, nil
	}
	data, err := os.ReadFile(parts[0])
	if err != nil {
		return nil, err
	}
	index, _ := strconv.Atoi(parts[1])
	face, err := (ofdgo.FontFile{Data: data}).Face(index)
	if err != nil {
		return nil, err
	}
	h.fontCache.put(cacheKey, face)
	return face, nil
}

// closeEngine 取消会话调用并释放文档及文件引用
// 入参: id 会话标识
// 返回: error 关闭错误
func (h *Host) closeEngine(id string) error {
	var results []any
	h.mu.Lock()
	if closing := h.closingEngines[id]; closing != nil {
		h.mu.Unlock()
		<-closing.done
		return closing.err
	}
	engine := h.engines[id]
	if engine == nil {
		h.mu.Unlock()
		return nil
	}
	closing := &engineClose{done: make(chan struct{})}
	if h.closingEngines == nil {
		h.closingEngines = make(map[string]*engineClose)
	}
	h.closingEngines[id] = closing
	delete(h.engines, id)
	if stop := h.engineStops[id]; stop != nil {
		stop()
		delete(h.engineStops, id)
	}
	for key, cancel := range h.cancels {
		if strings.HasPrefix(key, id+"/") {
			cancel()
		}
	}
	for key, result := range h.batchResults {
		if strings.HasPrefix(key, id+"/") {
			results = append(results, result)
			delete(h.batchResults, key)
		}
	}
	for key := range h.batchOutputs {
		if strings.HasPrefix(key, id+"/") {
			delete(h.batchOutputs, key)
		}
	}
	h.mu.Unlock()
	for _, result := range results {
		h.discard(result)
	}
	if h.docResources != nil {
		h.docResources.clearEngine(id)
	}
	if h.svgStreams != nil {
		h.svgStreams.clearEngine(id)
	}
	err := errors.Join(engine.Close(), h.files.closeHolder(id))
	h.mu.Lock()
	closing.err = err
	delete(h.closingEngines, id)
	close(closing.done)
	h.mu.Unlock()
	return err
}

// close 取消任务并释放当前窗口持有的会话
func (h *Host) close() {
	h.launchMu.Lock()
	h.launchClosed = true
	h.launchMu.Unlock()
	h.mu.Lock()
	for _, cancel := range h.cancels {
		cancel()
	}
	engines := make([]string, 0, len(h.engines)+len(h.closingEngines))
	for id := range h.engines {
		engines = append(engines, id)
	}
	for id := range h.closingEngines {
		engines = append(engines, id)
	}
	results := h.batchResults
	h.batchResults = nil
	h.batchOutputs = nil
	h.mu.Unlock()
	for _, id := range engines {
		_ = h.closeEngine(id)
	}
	for _, result := range results {
		h.discard(result)
	}
	h.fontCache.clear()
	if h.docResources != nil {
		h.docResources.clearAll()
	}
	if h.svgStreams != nil {
		h.svgStreams.clearAll()
	}
	if h.resources != nil {
		h.resources.Clear()
	}
	_ = h.files.close()
}

// rotatesDocResources 判断操作成功时是否切换、重建或关闭当前文档会话
// 入参: name 操作名称
// 返回: bool 是否需要轮换文档资源令牌并清空旧内存池
func rotatesDocResources(name string) bool {
	switch name {
	case "ofdgoOpen", "ofdgoOpenPDF", "ofdgoCreateDocument", "ofdgoImportImages", "ofdgoSelectDocument", "ofdgoSelectDocumentVersion", "ofdgoClose":
		return true
	}
	return false
}

// fontMIME 根据字体文件魔数推断响应媒体类型
// 入参: data 字体字节数据
// 返回: string 媒体类型
func fontMIME(data []byte) string {
	if len(data) >= 4 {
		switch string(data[:4]) {
		case "wOF2":
			return "font/woff2"
		case "wOFF":
			return "font/woff"
		case "OTTO":
			return "font/otf"
		}
	}
	return "font/ttf"
}

// rawJSONValue 将已编码的JSON字符串直接转为原始JSON片段，避免发往WebView时二次转义
// 入参: value 原始字段值, fallback 空值时的JSON回退片段
// 返回: any 原始JSON片段或原对象
func rawJSONValue(value any, fallback string) any {
	text, ok := value.(string)
	if !ok {
		if value == nil {
			return json.RawMessage(fallback)
		}
		return value
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(trimmed)
}

// associationOptions 提供Windows文件关联的固定应用配置
// 返回: guigo.AssociationOptions 应用配置, error 路径错误
func associationOptions() (guigo.AssociationOptions, error) {
	executable, err := os.Executable()
	if err != nil {
		return guigo.AssociationOptions{}, err
	}
	return guigo.AssociationOptions{
		AppID: "OFDGo", Name: "OFDGo", Description: "OFD、PDF阅读与编辑工具",
		Executable: executable, Arguments: []string{"--"},
		Types: []guigo.FileAssociation{
			{ID: "OFD", Name: "OFD文档", Extensions: []string{".ofd"}},
			{ID: "PDF", Name: "PDF文档", Extensions: []string{".pdf"}},
		},
	}, nil
}

// queryAssociations 分别查询两个范围，不因其中一个失败而隐藏另一个
// 返回: any 两个范围的状态, error 配置错误
func queryAssociations() (any, error) {
	options, err := associationOptions()
	if err != nil {
		return nil, err
	}
	result := make(map[string]associationState)
	for _, scope := range []guigo.AssociationScope{guigo.AssociationUser, guigo.AssociationMachine} {
		state, err := guigo.QueryFileAssociations(options, scope)
		row := associationState{AssociationStatus: state}
		if err != nil {
			row.Error = associationError(err).Error()
		}
		result[string(scope)] = row
	}
	return result, nil
}

// changeAssociations 执行固定应用配置的单范围注册或撤销
// 入参: scope 作用范围, remove 是否撤销
// 返回: error 操作错误
func changeAssociations(scope string, remove bool) error {
	options, err := associationOptions()
	if err != nil {
		return err
	}
	if remove {
		err = guigo.UnregisterFileAssociations(options, guigo.AssociationScope(scope))
	} else {
		err = guigo.RegisterFileAssociations(options, guigo.AssociationScope(scope))
	}
	return associationError(err)
}

// associationError 区分未提升权限和实际访问失败，提供简短提示
// 入参: err 操作错误
// 返回: error 用户提示或原始错误
func associationError(err error) error {
	if errors.Is(err, guigo.ErrAssociationElevationRequired) {
		return errors.New("需管理员权限")
	}
	if errors.Is(err, os.ErrPermission) {
		return errors.New("无权访问")
	}
	return err
}

// applicationUpdate 比较官网与当前构建版本的时间戳前缀，仅远程更新时返回true
// 入参: ctx 请求上下文, version 当前版本, endpoint 官网地址
// 返回: bool 是否有更新, error 检查错误
func applicationUpdate(ctx context.Context, version, endpoint string) (bool, error) {
	current, ok := versionTimestamp(version)
	if !ok {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Cache-Control", "no-store")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("检查更新失败: %s", response.Status)
	}
	latest := strings.TrimSpace(response.Header.Get("X-OFDGo-Build-Version"))
	if latest == "" {
		return false, errors.New("缺少构建版本")
	}
	remote, ok := versionTimestamp(latest)
	if !ok {
		return false, errors.New("构建版本格式无效")
	}
	return remote > current, nil
}

// versionTimestamp 提取构建版本中的Unix时间戳前缀
// 入参: version 构建版本字符串
// 返回: int64 时间戳, bool 是否解析成功
func versionTimestamp(version string) (int64, bool) {
	version = strings.TrimSpace(version)
	if version == "" {
		return 0, false
	}
	prefix, _, _ := strings.Cut(version, "-")
	timestamp, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || timestamp <= 0 {
		return 0, false
	}
	return timestamp, true
}

// handleID 从宿主句柄映射中提取文件或目录授权标识
// 入参: value 句柄参数
// 返回: string 授权标识
func handleID(value any) string {
	handle, _ := value.(map[string]any)
	id, _ := handle["nativeHandle"].(string)
	return id
}

// dialogFilters 将共用界面的文件类型映射到系统对话框
// 入参: args 对话框参数
// 返回: []guigo.FileFilter 文件类型列表
func dialogFilters(args map[string]any) []guigo.FileFilter {
	patterns := func(values []string) string {
		var result []string
		seen := make(map[string]bool)
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if !strings.HasPrefix(value, ".") || strings.ContainsAny(value, "/\\;*?\x00") || seen[value] {
				continue
			}
			seen[value] = true
			result = append(result, "*"+value)
		}
		return strings.Join(result, ";")
	}
	var filters []guigo.FileFilter
	if accept, ok := args["accept"].(string); ok {
		var extensions []string
		for _, value := range strings.Split(accept, ",") {
			value = strings.ToLower(strings.TrimSpace(value))
			if strings.HasPrefix(value, ".") {
				extensions = append(extensions, value)
				continue
			}
			switch value {
			case "image/*":
				extensions = append(extensions, ".png", ".jpg", ".jpeg", ".bmp", ".tif", ".tiff", ".gif", ".webp", ".jb2", ".jbig2")
			case "font/*":
				extensions = append(extensions, ".ttf", ".otf", ".ttc", ".otc")
			default:
				values, err := mime.ExtensionsByType(value)
				if err == nil {
					sort.Strings(values)
					extensions = append(extensions, values...)
				}
			}
		}
		if pattern := patterns(extensions); pattern != "" {
			filters = append(filters, guigo.FileFilter{Name: "支持的文件", Pattern: pattern})
		}
	}
	types, _ := args["types"].([]any)
	for _, item := range types {
		value, _ := item.(map[string]any)
		accept, _ := value["accept"].(map[string]any)
		var extensions []string
		var names []string
		for name := range accept {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			values := accept[name]
			list, _ := values.([]any)
			for _, extension := range list {
				if text, ok := extension.(string); ok {
					extensions = append(extensions, text)
				}
			}
		}
		if pattern := patterns(extensions); pattern != "" {
			name, _ := value["description"].(string)
			if name == "" {
				name = pattern
			}
			filters = append(filters, guigo.FileFilter{Name: name, Pattern: pattern})
		}
	}
	return append(filters, guigo.FileFilter{Name: "所有文件", Pattern: "*.*"})
}

// dialogError 将用户取消与业务错误区分
// 入参: err 对话框错误
// 返回: error 页面可识别的错误
func dialogError(err error) error {
	if errors.Is(err, guigo.ErrCanceled) {
		return &guigo.BridgeError{Name: "AbortError", Message: "已取消"}
	}
	return err
}

// windowOptions 创建原生窗口配置
// 入参: dataDir 自定义浏览器数据目录
// 返回: guigo.Options 窗口配置, error 配置错误
func windowOptions(dataDir string) (guigo.Options, error) {
	appID := "ofdgo"
	cacheName := appID
	if runtime.GOOS == "windows" {
		appID = ""
		cacheName = "OFDGo"
	}
	if dataDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return guigo.Options{}, err
		}
		dataDir = filepath.Join(cache, cacheName)
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return guigo.Options{}, err
	}
	icon, err := webuiassets.NativeFS.ReadFile("ofdgo.png")
	if err != nil {
		return guigo.Options{}, err
	}
	return guigo.Options{
		AppID:          appID,
		Title:          "OFDGo - OFD编辑器",
		TitleBar:       guigo.TitleBarContent,
		TitleBarColors: themeColors(false),
		WindowMaterial: guigo.WindowSolid,
		Icon:           icon,
		Width:          1440,
		Height:         900,
		MinWidth:       360,
		MinHeight:      240,
		Center:         true,
		Maximized:      true,
		UserDataDir:    dataDir,
	}, nil
}
