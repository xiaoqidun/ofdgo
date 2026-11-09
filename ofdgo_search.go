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
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// PageTextCache 按内存预算保留最近访问的页面文字索引
// 缓存后不得修改PageText，页面或字体变化时须删除对应条目
type PageTextCache struct {
	cache *renderCache[int, *pageTextEntry]
}

// pageTextEntry 将页面原文和派生搜索索引放在同一预算与失效范围内
type pageTextEntry struct {
	text   *PageText
	search *TextSearchIndex
}

// TextSearchIndex 保存不可变的文字搜索快照，可并行搜索，不随源文字修改
// 页面或字体变化后需由调用方重新构建
type TextSearchIndex struct {
	runs   []TextRun
	groups []textSearchGroup
}

// textSearchGroup 保存同行连续文字及Unicode字符映射
type textSearchGroup struct {
	first   int
	last    int
	offsets []int
	runes   []rune
	folded  string
}

// textPageTokens 保留文字及其复合图元和裁剪，跳过独立路径与图片
type textPageTokens struct {
	*xml.Decoder
	parents []string
}

// PageText 页面文字，按绘制顺序保留文本对象，不包含图像和签名外观
type PageText struct {
	Runs []TextRun `json:"runs"`
}

// TextRun 文本对象原文及逐字符区域，坐标以页面左上角为原点，单位为毫米
// Boxes与Text的Unicode字符一一对应，缺少字体时使用文本对象区域，缺省区域不影响文字搜索
type TextRun struct {
	ID    string     `json:"id"`
	Text  string     `json:"text"`
	Boxes []Box      `json:"boxes"`
	Spans []TextSpan `json:"spans"`
}

// TextSpan 可选文字区间，Start和End为Unicode字符偏移
// Matrix依次为a、b、c、d、e、f，将单位矩形映射到页面左上角为原点的毫米坐标
type TextSpan struct {
	Start  int        `json:"start"`
	End    int        `json:"end"`
	Matrix [6]float64 `json:"matrix"`
}

// TextMatch 文字匹配结果，ID和Run定位首个对象，Start和End为该对象内的Unicode字符区间
// 跨对象匹配通过Parts保留各对象区间，Text和Boxes包含完整命中
type TextMatch struct {
	ID     string          `json:"id"`
	Run    int             `json:"run"`
	Start  int             `json:"start"`
	End    int             `json:"end"`
	Before string          `json:"before"`
	Text   string          `json:"text"`
	After  string          `json:"after"`
	Boxes  []Box           `json:"boxes"`
	Parts  []TextMatchPart `json:"parts,omitempty"`
}

// TextMatchPart 跨对象匹配中的原文区间，Start和End为对象内Unicode字符偏移
type TextMatchPart struct {
	ID    string `json:"id"`
	Run   int    `json:"run"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// NewPageTextCache 创建文字索引缓存，预算包含字符区域和条目开销
// 入参: bytes 内存预算，非正数禁用缓存
// 返回: *PageTextCache 独立缓存
func NewPageTextCache(bytes int) *PageTextCache {
	return &PageTextCache{cache: resourceCache[int, *pageTextEntry](nil, max(0, bytes))}
}

// MemorySize 估算文字索引占用，包含切片容量和字符串内容
// 返回: int64 估算字节数，共享字符串按各次引用计费
func (p *PageText) MemorySize() int64 {
	size := int64(unsafe.Sizeof(*p)) + int64(cap(p.Runs))*int64(unsafe.Sizeof(TextRun{}))
	for _, run := range p.Runs {
		size += int64(len(run.ID)) + int64(len(run.Text))
		size += int64(cap(run.Boxes))*int64(unsafe.Sizeof(Box{})) + int64(cap(run.Spans))*int64(unsafe.Sizeof(TextSpan{}))
	}
	return size
}

// Get 获取文字索引并更新最近访问顺序
// 入参: index 页面索引
// 返回: *PageText 文字索引，未命中时为nil
func (c *PageTextCache) Get(index int) *PageText {
	entry, _ := c.cache.get(index)
	if entry == nil {
		return nil
	}
	return entry.text
}

// Put 缓存完整文字索引，超出预算的单页不驱逐其他页面
// 入参: index 页面索引, text 文字索引，nil删除已有条目
func (c *PageTextCache) Put(index int, text *PageText) {
	if text == nil {
		c.Delete(index)
		return
	}
	size := text.MemorySize() + 128
	if size > int64(c.cache.limit) {
		c.Delete(index)
		return
	}
	c.cache.put(index, &pageTextEntry{text: text}, int(size))
}

// Delete 删除页面索引并释放预算
// 入参: index 页面索引
func (c *PageTextCache) Delete(index int) {
	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()
	if entry := c.cache.entries[index]; entry != nil {
		c.cache.remove(entry)
	}
}

// Clear 清空缓存和计费状态
func (c *PageTextCache) Clear() {
	c.cache.clear()
}

// Len 返回缓存页面数
// 返回: int 已缓存页面数
func (c *PageTextCache) Len() int {
	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()
	return len(c.cache.entries)
}

// Range 按访问时间从旧到新遍历，遍历期间不得访问当前缓存
// 入参: visit 访问函数，返回false停止遍历
func (c *PageTextCache) Range(visit func(int, *PageText) bool) {
	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()
	for entry := c.cache.order.Back(); entry != nil; entry = entry.Prev() {
		value := entry.Value.(renderCacheEntry[int, *pageTextEntry])
		if !visit(value.key, value.value.text) {
			return
		}
	}
}

// Token 读取文字页面所需的XML节点
// 返回: xml.Token XML节点, error 错误信息
func (d *textPageTokens) Token() (xml.Token, error) {
	for {
		token, err := d.Decoder.Token()
		if err != nil {
			return nil, err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if len(d.parents) > 0 && (node.Name.Local == "PathObject" || node.Name.Local == "ImageObject") {
				switch d.parents[len(d.parents)-1] {
				case "Layer", "PageBlock", "CompositeGraphicUnit", "CompositeObject":
					if err := d.Decoder.Skip(); err != nil {
						return nil, err
					}
					continue
				}
			}
			d.parents = append(d.parents, node.Name.Local)
		case xml.EndElement:
			d.parents = d.parents[:len(d.parents)-1]
		}
		return token, nil
	}
}

// PageText 提取页面及启用注释的原文，复用当前编译器的字体和几何语义
// 入参: page 页面内容
// 返回: *PageText 页面文字, error 错误信息
func (r *Renderer) PageText(page *PageContent) (*PageText, error) {
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	return r.PageTextContext(ctx, page)
}

// PageTextContext 提取页面原文及字形位置，取消时不返回部分结果
// 入参: ctx 取消上下文，nil使用后台上下文, page 页面内容
// 返回: *PageText 页面文字, error 提取或取消错误
func (r *Renderer) PageTextContext(ctx context.Context, page *PageContent) (*PageText, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.backends.Compiler == nil {
		return nil, fmt.Errorf("%w: page text", ErrBackendUnavailable)
	}
	renderer := *r
	renderer.pageContext = ctx
	text, err := renderer.backends.Compiler.PageText(&renderer, page)
	if canceled := ctx.Err(); canceled != nil {
		return nil, canceled
	}
	return text, err
}

// PageTextByIndex 按页面索引提取文字，避免解析独立路径和图片图元
// 入参: index 页面索引，从0开始
// 返回: *PageText 页面文字, error 错误信息
func (r *Renderer) PageTextByIndex(index int) (*PageText, error) {
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	return r.PageTextByIndexContext(ctx, index)
}

// PageTextByIndexContext 按索引读取页面并提取文字，读取和页内计算均响应取消
// 入参: ctx 取消上下文，nil使用后台上下文, index 页面索引，从0开始
// 返回: *PageText 页面文字, error 读取、提取或取消错误
func (r *Renderer) PageTextByIndexContext(ctx context.Context, index int) (*PageText, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	doc, err := r.Reader.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(doc.Pages.Page) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	page, err := r.Reader.readPageContent(ctx, doc.Pages.Page[index], true, false)
	if err != nil {
		return nil, err
	}
	return r.PageTextContext(ctx, page)
}

// String 按文本对象顺序合并原文，以换行分隔非空对象
// 返回: string 页面原文
func (p *PageText) String() string {
	var text strings.Builder
	_, _ = p.WriteTo(&text)
	return text.String()
}

// WriteTo 按文本对象顺序输出UTF-8原文，以换行分隔非空对象，不拼接整页字符串
// 入参: writer 输出流
// 返回: int64 已写字节数, error 写入错误，出错时可能已有部分输出
func (p *PageText) WriteTo(writer io.Writer) (int64, error) {
	return p.writeTo(writer, false)
}

// Search 按原文进行忽略大小写的字面匹配，拼接同一行紧邻的文本对象，不识别图像
// 入参: query 搜索文字
// 返回: []TextMatch 匹配结果及上下文
func (p *PageText) Search(query string) []TextMatch {
	var matches []TextMatch
	_ = p.SearchEach(context.Background(), query, func(match TextMatch) error {
		matches = append(matches, match)
		return nil
	})
	return matches
}

// SearchEach 逐项返回忽略大小写的字面匹配，拼接同一行紧邻对象，在索引构建及匹配之间检查取消
// 入参: ctx 取消上下文, query 搜索文字, visit 匹配访问函数，可返回错误停止
// 返回: error 取消或访问错误
func (p *PageText) SearchEach(ctx context.Context, query string, visit func(TextMatch) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.searchEach(ctx, foldText(query), visit)
}

// SearchText 逐页搜索原文并立即返回匹配，不保留整本文字或识别图像
// 入参: ctx 取消上下文，页面读取、文字提取及匹配时检查, query 搜索文字, visit 零基页码和匹配访问函数, indices 页码，省略则全部，按原页序去重
// 返回: error 页面提取、取消或访问错误
func (r *Renderer) SearchText(ctx context.Context, query string, visit func(int, TextMatch) error, indices ...int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if query == "" {
		return nil
	}
	doc, err := r.Reader.DocContext(ctx)
	if err != nil {
		return err
	}
	indices, err = exportPageIndices(len(doc.Pages.Page), indices)
	if err != nil {
		return err
	}
	query = foldText(query)
	for _, index := range indices {
		if err := ctx.Err(); err != nil {
			return err
		}
		text, err := r.PageTextByIndexContext(ctx, index)
		if err != nil {
			return fmt.Errorf("failed to search page %d: %w", index+1, err)
		}
		if err := text.searchEach(ctx, query, func(match TextMatch) error { return visit(index, match) }); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// writeTo 输出非空文本对象，可在首个对象前分隔上一页
// 入参: writer 输出流, separator 是否需要前导换行
// 返回: int64 已写字节数, error 写入错误
func (p *PageText) writeTo(writer io.Writer, separator bool) (int64, error) {
	var written int64
	for _, run := range p.Runs {
		if run.Text == "" {
			continue
		}
		if separator {
			n, err := io.WriteString(writer, "\n")
			written += int64(n)
			if err != nil {
				return written, err
			}
			if n != 1 {
				return written, io.ErrShortWrite
			}
		}
		n, err := io.WriteString(writer, run.Text)
		written += int64(n)
		if err != nil {
			return written, err
		}
		if n != len(run.Text) {
			return written, io.ErrShortWrite
		}
		separator = true
	}
	return written, nil
}

// searchEach 使用预折叠的查询执行搜索，供跨页搜索复用
// 入参: ctx 取消上下文, query 已折叠查询, visit 匹配访问函数
// 返回: error 取消或访问错误
func (p *PageText) searchEach(ctx context.Context, query string, visit func(TextMatch) error) error {
	if query == "" {
		return ctx.Err()
	}
	index, err := p.searchIndex(ctx)
	if err != nil {
		return err
	}
	return index.searchEach(ctx, query, visit)
}

// NewTextSearchIndex 创建独立搜索快照，复制字符区域，保留Unicode折叠和跨对象匹配
// 入参: ctx 取消上下文, text 页面原文
// 返回: *TextSearchIndex 不可变索引, error 取消错误
func NewTextSearchIndex(ctx context.Context, text *PageText) (*TextSearchIndex, error) {
	copy := PageText{Runs: slices.Clone(text.Runs)}
	for i := range copy.Runs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		copy.Runs[i].Boxes = slices.Clone(copy.Runs[i].Boxes)
		copy.Runs[i].Spans = nil
	}
	return copy.searchIndex(ctx)
}

// searchIndex 为只读文字构建同行分组、字符偏移和大小写折叠
// 入参: ctx 取消上下文
// 返回: *TextSearchIndex 搜索索引, error 取消错误
func (p *PageText) searchIndex(ctx context.Context) (*TextSearchIndex, error) {
	result := &TextSearchIndex{runs: p.Runs}
	for first := 0; first < len(p.Runs); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		last := first + 1
		for last < len(p.Runs) && adjacentTextRuns(p.Runs[last-1], p.Runs[last]) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			last++
		}
		var source strings.Builder
		offsets := make([]int, last-first+1)
		for index := first; index < last; index++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			source.WriteString(p.Runs[index].Text)
			offsets[index-first+1] = offsets[index-first] + utf8.RuneCountInString(p.Runs[index].Text)
		}
		var folded strings.Builder
		folded.Grow(source.Len())
		polled := 0
		for _, char := range source.String() {
			if polled%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			polled++
			folded.WriteRune(foldRune(char))
		}
		result.groups = append(result.groups, textSearchGroup{first: first, last: last, offsets: offsets, runes: []rune(source.String()), folded: folded.String()})
		first = last
	}
	return result, ctx.Err()
}

// SearchEach 在不可变索引中逐项搜索，回调返回错误时立即停止
// 入参: ctx 取消上下文, query 搜索文字, visit 匹配访问函数
// 返回: error 取消或访问错误
func (p *TextSearchIndex) SearchEach(ctx context.Context, query string, visit func(TextMatch) error) error {
	return p.searchEach(ctx, foldText(query), visit)
}

// searchEach 使用已折叠查询访问预处理分组
// 入参: ctx 取消上下文, query 已折叠查询, visit 匹配访问函数
// 返回: error 取消或访问错误
func (p *TextSearchIndex) searchEach(ctx context.Context, query string, visit func(TextMatch) error) error {
	if query == "" {
		return ctx.Err()
	}
	length := utf8.RuneCountInString(query)
	for _, group := range p.groups {
		first, last, offsets, runes, text := group.first, group.last, group.offsets, group.runes, group.folded
		codeOffset := 0
		for offset := 0; offset < len(text); {
			if err := ctx.Err(); err != nil {
				return err
			}
			found := strings.Index(text[offset:], query)
			if found < 0 {
				break
			}
			start := codeOffset + utf8.RuneCountInString(text[offset:offset+found])
			offset += found
			end := start + length
			begin := sort.Search(len(offsets)-1, func(i int) bool { return offsets[i+1] > start })
			match := TextMatch{ID: p.runs[first+begin].ID, Run: first + begin, Start: start - offsets[begin], End: min(end, offsets[begin+1]) - offsets[begin],
				Before: string(runes[max(0, start-16):start]), Text: string(runes[start:end]), After: string(runes[end:min(len(runes), end+16)])}
			for index := begin; index < last-first && offsets[index] < end; index++ {
				run := p.runs[first+index]
				from, to := max(start, offsets[index])-offsets[index], min(end, offsets[index+1])-offsets[index]
				if end > offsets[begin+1] {
					match.Parts = append(match.Parts, TextMatchPart{ID: run.ID, Run: first + index, Start: from, End: to})
				}
				for _, box := range run.Boxes[min(from, len(run.Boxes)):min(to, len(run.Boxes))] {
					if box.W > 0 && box.H > 0 && (len(match.Boxes) == 0 || match.Boxes[len(match.Boxes)-1] != box) {
						match.Boxes = append(match.Boxes, box)
					}
				}
			}
			if err := visit(match); err != nil {
				return err
			}
			offset += len(query)
			codeOffset = end
		}
	}
	return ctx.Err()
}

// searchSize 估算派生分组占用，不重复计费共享的页面文字
// 返回: int64 派生索引字节数
func (p *TextSearchIndex) searchSize() int64 {
	size := int64(unsafe.Sizeof(*p)) + int64(cap(p.groups))*int64(unsafe.Sizeof(textSearchGroup{}))
	for _, group := range p.groups {
		size += int64(cap(group.offsets))*int64(unsafe.Sizeof(int(0))) + int64(cap(group.runes))*4 + int64(len(group.folded))
	}
	return size
}

// MemorySize 估算独立搜索快照的总内存占用
// 返回: int64 估算字节数
func (p *TextSearchIndex) MemorySize() int64 {
	return (&PageText{Runs: p.runs}).MemorySize() + p.searchSize()
}

// SearchEach 在已缓存页面上搜索，派生索引与原文共用预算及失效范围
// 入参: ctx 取消上下文, index 页面索引, query 搜索文字, visit 匹配访问函数
// 返回: bool 是否命中页面缓存, error 取消或访问错误
func (c *PageTextCache) SearchEach(ctx context.Context, index int, query string, visit func(TextMatch) error) (bool, error) {
	entry, ok := c.cache.get(index)
	if !ok {
		return false, ctx.Err()
	}
	search := entry.search
	if search == nil {
		var err error
		search, err = entry.text.searchIndex(ctx)
		if err != nil {
			return true, err
		}
		cost := entry.text.MemorySize() + search.searchSize() + 128
		c.cache.mu.Lock()
		if current := c.cache.entries[index]; current != nil && current.Value.(renderCacheEntry[int, *pageTextEntry]).value == entry && cost <= int64(c.cache.limit) {
			c.cache.putLocked(index, &pageTextEntry{text: entry.text, search: search}, int(cost))
		}
		c.cache.mu.Unlock()
	}
	return true, search.SearchEach(ctx, query, visit)
}

// foldText 统一Unicode大小写等价字符，保留字符数量
// 入参: text 原文
// 返回: string 匹配用文字
func foldText(text string) string {
	return strings.Map(foldRune, text)
}

// foldRune 统一单个Unicode大小写等价字符
// 入参: char 原字符
// 返回: rune 匹配字符
func foldRune(char rune) rune {
	if char < utf8.RuneSelf {
		if char >= 'a' && char <= 'z' {
			return char - ('a' - 'A')
		}
		return char
	}
	folded := char
	for next := unicode.SimpleFold(char); next != char; next = unicode.SimpleFold(next) {
		folded = min(folded, next)
	}
	return folded
}

// adjacentTextRuns 判断绘制顺序相邻的文字是否在同一行连续，缺少定位时保持独立
// 入参: before 前对象, after 后对象
// 返回: bool 是否可连续搜索
func adjacentTextRuns(before, after TextRun) bool {
	if before.Text == "" || after.Text == "" || len(before.Boxes) != utf8.RuneCountInString(before.Text) || len(after.Boxes) == 0 {
		return false
	}
	a, b := before.Boxes[len(before.Boxes)-1], after.Boxes[0]
	size := math.Min(a.H, b.H)
	if size <= 0 || a.W <= 0 || b.W <= 0 {
		return false
	}
	gap := b.X - a.X - a.W
	return math.Abs(a.Y+a.H/2-b.Y-b.H/2) <= size*.25 && gap >= -size*.15 && gap <= size*.35 && b.X > a.X
}

// addRun 收集原始文本，字形索引不替代Unicode原文
// 入参: obj 文本对象
// 返回: *TextRun 文本及待填充的字符区域
func (p *PageText) addRun(obj TextObject) *TextRun {
	text := obj.Text()
	p.Runs = append(p.Runs, TextRun{ID: obj.ID, Text: text, Boxes: make([]Box, utf8.RuneCountInString(text))})
	return &p.Runs[len(p.Runs)-1]
}

// textGlyphSpans 将绘制字形映射到原文字符区间
// 入参: runes 原文字符, transforms 字形变换, offset 文本编码偏移
// 返回: [][2]int 每个字形对应的字符起止位置
func textGlyphSpans(runes []rune, transforms map[int]textGlyphTransform, offset int) [][2]int {
	spans := make([][2]int, 0, len(runes))
	for i := 0; i < len(runes); {
		if transform, ok := transforms[offset+i]; ok && i+transform.CodeCount <= len(runes) {
			for range transform.Glyphs {
				spans = append(spans, [2]int{i, i + transform.CodeCount})
			}
			i += transform.CodeCount
		} else {
			spans = append(spans, [2]int{i, i + 1})
			i++
		}
	}
	return spans
}

// unionTextBox 合并同一字符的字形区域
// 入参: a 已有区域, b 新区域
// 返回: Box 合并区域
func unionTextBox(a, b Box) Box {
	if a.W <= 0 || a.H <= 0 {
		return b
	}
	if b.W <= 0 || b.H <= 0 {
		return a
	}
	x, y := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	return Box{X: x, Y: y, W: math.Max(a.X+a.W, b.X+b.W) - x, H: math.Max(a.Y+a.H, b.Y+b.H) - y}
}
