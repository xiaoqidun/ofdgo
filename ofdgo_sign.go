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
	"bytes"
	"context"
	"encoding/asn1"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

const (
	// SignTypeSeal 印章签名
	SignTypeSeal SignType = "Seal"
	// SignTypeSign 数字签名
	SignTypeSign SignType = "Sign"
)

// SignType 签名类型
type SignType string

// Signatures 签名列表
type Signatures struct {
	XMLName   xml.Name    `xml:"Signatures"`
	MaxSignID string      `xml:"MaxSignId,omitempty"`
	List      []Signature `xml:"Signature"`
}

// Signature 签名列表引用
type Signature struct {
	ID      string   `xml:"ID,attr"`
	BaseLoc string   `xml:"BaseLoc,attr"`
	Type    SignType `xml:"Type,attr"`
}

// SignatureFile 签名文件内容描述
type SignatureFile struct {
	XMLName     xml.Name `xml:"Signature"`
	SignedValue string   `xml:"SignedValue"`
	SignedInfo  SignedInfo
}

// SignedInfo 签名信息
type SignedInfo struct {
	Provider          SignatureProvider   `xml:"Provider"`
	SignatureMethod   string              `xml:"SignatureMethod"`
	SignatureDateTime string              `xml:"SignatureDateTime"`
	Seal              SignatureSeal       `xml:"Seal"`
	StampAnnot        []SignatureStamp    `xml:"StampAnnot"`
	References        SignatureReferences `xml:"References"`
}

// SignatureProvider 签名提供者信息
type SignatureProvider struct {
	ProviderName string `xml:"ProviderName,attr"`
	Company      string `xml:"Company,attr,omitempty"`
	Version      string `xml:"Version,attr,omitempty"`
}

// SignatureSeal 签名印章引用
type SignatureSeal struct {
	BaseLoc string `xml:"BaseLoc"`
}

// SignatureStamp 签名印章注释
type SignatureStamp struct {
	ID       string `xml:"ID,attr"`
	PageRef  string `xml:"PageRef,attr"`
	Boundary string `xml:"Boundary,attr"`
	Clip     string `xml:"Clip,attr,omitempty"`
}

// SignatureStampPosition 签名外观位置信息，Page为从1开始的页码
type SignatureStampPosition struct {
	ID       string
	Page     int
	PageID   string
	Boundary string
	Box      Box
	Clip     string
	ClipBox  *Box
}

// SignatureReferences 签名保护文件列表
type SignatureReferences struct {
	CheckMethod string               `xml:"CheckMethod,attr"`
	Reference   []SignatureReference `xml:"Reference"`
}

// SignatureReference 签名保护文件引用
type SignatureReference struct {
	FileRef    string `xml:"FileRef,attr"`
	CheckValue string `xml:"CheckValue"`
}

// Stamp 印章信息结构
// SignatureID与ID共同定位签名外观，Type与Data保留原始印章媒体
type Stamp struct {
	ID          string
	SignatureID string
	Box         Box
	Clip        *Box
	Type        string
	Data        []byte
	sealSource  stampSealSource
}

// stampSealSource 保存印章二进制数据的惰性包内路径
type stampSealSource struct {
	sealPath        string
	signedValuePath string
}

// loadedStampMedia 缓存文档内同源印章媒体，Data为只读共享切片
type loadedStampMedia struct {
	sealType string
	sealData []byte
}

// stampSealCache 在文档及其派生只读视图间并发共享已提取的只读印章媒体
type stampSealCache struct {
	cache *renderCache[stampSealSource, loadedStampMedia]
}

// parsedStampEntry 暂存已校验几何范围的单条签章注释
type parsedStampEntry struct {
	id     string
	pageID string
	box    Box
	clip   *Box
}

// normalizeSignType 规范化签名类型，空值按印章签名处理
// 入参: t 原始签名类型
// 返回: SignType 规范化签名类型
func normalizeSignType(t SignType) SignType {
	raw := strings.TrimSpace(string(t))
	switch {
	case raw == "" || strings.EqualFold(raw, string(SignTypeSeal)):
		return SignTypeSeal
	case strings.EqualFold(raw, string(SignTypeSign)):
		return SignTypeSign
	default:
		return SignType(raw)
	}
}

// newStampSealCache 按共享资源策略创建有界印章媒体缓存
// 入参: resources 共享资源策略，可为nil
// 返回: *stampSealCache 印章媒体缓存
func newStampSealCache(resources *Resources) *stampSealCache {
	return &stampSealCache{cache: resourceCache[stampSealSource, loadedStampMedia](resources, 16<<20)}
}

// parseSignatureStampBox 解析签名外观区域
// 入参: s 区域字符串
// 返回: Box 矩形对象, error 错误信息
func parseSignatureStampBox(s string) (Box, error) {
	box, err := ParseBox(s)
	if err != nil {
		return Box{}, fmt.Errorf("invalid signature stamp box: %s", s)
	}
	return box, nil
}

// signaturePageNumbers 获取签名页面编号索引
// 入参: doc 文档结构
// 返回: map[string]int 页面编号索引
func signaturePageNumbers(doc *Document) map[string]int {
	pages := make(map[string]int, len(doc.Pages.Page))
	for index, page := range doc.Pages.Page {
		if page.ID != "" {
			pages[page.ID] = index + 1
		}
	}
	return pages
}

// extractSeal 尝试提取印章数据
// 入参: data 签名值数据
// 返回: string 印章类型, []byte 印章数据
func extractSeal(data []byte) (string, []byte) {
	if sealType, sealData := probeSealMedia(data); len(sealData) > 0 {
		return sealType, sealData
	}
	var raw asn1.RawValue
	_, err := asn1.Unmarshal(data, &raw)
	if err != nil {
		return "", nil
	}
	var foundType string
	var foundData []byte
	var search func(node asn1.RawValue) bool
	search = func(node asn1.RawValue) bool {
		if node.Tag == asn1.TagOctetString {
			if mediaType, mediaData := probeSealMedia(node.Bytes); len(mediaData) > 0 {
				foundType, foundData = mediaType, mediaData
				return true
			}
		}
		if node.IsCompound {
			if elements, ok := asn1Children(node.Bytes); ok {
				if mediaType, mediaData, ok := sealMediaFromElements(elements); ok {
					foundType, foundData = mediaType, mediaData
					return true
				}
				for _, child := range elements {
					if search(child) {
						return true
					}
				}
			}
		}
		return false
	}
	if search(raw) {
		return foundType, foundData
	}
	return "", nil
}

// asn1Children 解析ASN.1复合节点子元素
// 入参: data 复合节点内容
// 返回: []asn1.RawValue 子元素列表, bool 是否成功
func asn1Children(data []byte) ([]asn1.RawValue, bool) {
	var elements []asn1.RawValue
	for len(data) > 0 {
		var child asn1.RawValue
		rest, err := asn1.Unmarshal(data, &child)
		if err != nil || len(rest) == len(data) {
			return nil, false
		}
		elements = append(elements, child)
		data = rest
	}
	return elements, true
}

// sealMediaFromElements 从印章ASN.1结构提取媒体数据
// 入参: elements ASN.1子元素
// 返回: string 媒体类型, []byte 媒体数据, bool 是否成功
func sealMediaFromElements(elements []asn1.RawValue) (string, []byte, bool) {
	if len(elements) < 2 || elements[1].Tag != asn1.TagOctetString {
		return "", nil, false
	}
	mediaType := baseimage.NormalizeFormat(sealString(elements[0]))
	if isSealMediaType(mediaType) {
		if mediaType == "ofd" {
			if data := trimOFDPackage(elements[1].Bytes); len(data) > 0 {
				return mediaType, data, true
			}
		}
		return mediaType, elements[1].Bytes, true
	}
	if probeType, probeData := probeSealMedia(elements[1].Bytes); len(probeData) > 0 {
		return probeType, probeData, true
	}
	return "", nil, false
}

// sealString 解析印章图片类型
// 入参: raw ASN.1原始值
// 返回: string 图片类型
func sealString(raw asn1.RawValue) string {
	var s string
	if _, err := asn1.Unmarshal(raw.FullBytes, &s); err != nil {
		s = string(raw.Bytes)
	}
	s = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "\x00", "")))
	if s == "jpg" {
		s = "jpeg"
	}
	return s
}

// isSealMediaType 判断是否为可渲染印章媒体类型
// 入参: s 媒体类型
// 返回: bool 是否可渲染
func isSealMediaType(s string) bool {
	switch s {
	case "png", "jpeg", "bmp", "jbig2", "ofd":
		return true
	default:
		return false
	}
}

// probeSealMedia 探测印章媒体数据
// 入参: data 原始数据
// 返回: string 媒体类型, []byte 媒体数据
func probeSealMedia(data []byte) (string, []byte) {
	if sealData := trimOFDPackage(data); len(sealData) > 0 {
		return "ofd", sealData
	}
	return probeImageMedia(data)
}

// trimOFDPackage 裁剪OFD包数据
// 入参: data 原始数据
// 返回: []byte OFD包数据
func trimOFDPackage(data []byte) []byte {
	if idx := bytes.Index(data, []byte("PK\x03\x04")); idx >= 0 {
		if end := zipDataEnd(data[idx:]); end > 0 {
			return data[idx : idx+end]
		}
	}
	return nil
}

// zipDataEnd 获取ZIP数据结束位置
// 入参: data ZIP数据
// 返回: int 结束位置
func zipDataEnd(data []byte) int {
	if len(data) < 4 || !bytes.Equal(data[:4], []byte("PK\x03\x04")) {
		return 0
	}
	sig := []byte("PK\x05\x06")
	for i := len(data) - 22; i >= 0; i-- {
		if i+22 > len(data) || !bytes.Equal(data[i:i+4], sig) {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(data[i+20 : i+22]))
		end := i + 22 + commentLen
		if end <= len(data) {
			return end
		}
	}
	return 0
}

// probeImageMedia 探测图片媒体数据
// 入参: data 原始数据
// 返回: string 媒体类型, []byte 图片数据
func probeImageMedia(data []byte) (string, []byte) {
	_, format, err := baseimage.DecodeConfigData(data)
	if err != nil {
		return "", nil
	}
	return baseimage.NormalizeFormat(format), data
}

// ObjectID 返回签名和外观共同确定的画布标识，避免不同签名中的外观标识冲突
// 返回: string 可用于SVG分组及编辑命中的标识
func (s Stamp) ObjectID() string {
	return "signature:" + url.QueryEscape(s.SignatureID) + ":" + url.QueryEscape(s.ID)
}

// Bounds 返回签章裁剪后的页面可见范围，Clip使用印章局部坐标
// 返回: Box 页面毫米范围
func (s Stamp) Bounds() Box {
	if s.Clip == nil {
		return s.Box
	}
	x, y := max(0, s.Clip.X), max(0, s.Clip.Y)
	return Box{X: s.Box.X + x, Y: s.Box.Y + y, W: max(0, min(s.Box.W, s.Clip.X+s.Clip.W)-x), H: max(0, min(s.Box.H, s.Clip.Y+s.Clip.H)-y)}
}

// SignatureStampPositions 获取签名外观位置信息
// 入参: stamps 签名外观列表
// 返回: []SignatureStampPosition 签名外观位置信息, error 错误信息
func (r *Reader) SignatureStampPositions(stamps []SignatureStamp) ([]SignatureStampPosition, error) {
	doc, err := r.docStructure()
	if err != nil {
		return nil, err
	}
	pages := signaturePageNumbers(doc)
	positions := make([]SignatureStampPosition, 0, len(stamps))
	for _, stamp := range stamps {
		page, ok := resourceValue(pages, stamp.PageRef)
		if !ok {
			return nil, fmt.Errorf("signature stamp page not found: %s", stamp.PageRef)
		}
		box, err := parseSignatureStampBox(stamp.Boundary)
		if err != nil {
			return nil, err
		}
		var clipBox *Box
		if stamp.Clip != "" {
			clip, err := parseSignatureStampBox(stamp.Clip)
			if err != nil {
				return nil, err
			}
			clipBox = &clip
		}
		positions = append(positions, SignatureStampPosition{
			ID:       stamp.ID,
			Page:     page,
			PageID:   stamp.PageRef,
			Boundary: stamp.Boundary,
			Box:      box,
			Clip:     stamp.Clip,
			ClipBox:  clipBox,
		})
	}
	return positions, nil
}

// PageStamps 按页面标识获取页面印章列表，按需惰性读取印章媒体数据
// 入参: pageID 页面标识
// 返回: []Stamp 页面印章列表, error 错误信息
func (r *Reader) PageStamps(pageID string) ([]Stamp, error) {
	return r.PageStampsContext(context.Background(), pageID)
}

// PageStampsContext 按页面标识获取页面印章列表并响应取消
// 入参: ctx 取消上下文, pageID 页面标识
// 返回: []Stamp 页面印章列表, error 读取或取消错误
func (r *Reader) PageStampsContext(ctx context.Context, pageID string) ([]Stamp, error) {
	if _, err := r.docStructureContext(ctx); err != nil {
		return nil, err
	}
	if err := r.ensurePageStampsContext(ctx, pageID); err != nil {
		return nil, err
	}
	return cloneEditorData(r.stamps[r.resolveStampPageID(pageID)]), nil
}

// load 按需读取并解析外置印章或签章值中的嵌入印章媒体
// 入参: ctx 取消上下文, r 阅读器
// 返回: string 媒体类型, []byte 媒体字节副本, error 读取或取消错误
func (src stampSealSource) load(ctx context.Context, r *Reader) (string, []byte, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	var sealType string
	var sealData []byte
	if src.sealPath != "" {
		data, err := r.readFileViewContext(ctx, src.sealPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", nil, err
			}
		} else {
			sealType, sealData = extractSeal(data)
		}
	}
	if len(sealData) == 0 && src.signedValuePath != "" {
		data, err := r.readFileViewContext(ctx, src.signedValuePath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", nil, err
			}
		} else if sig, parseErr := parseSESSignature(data); parseErr == nil {
			sealType, sealData = sig.Seal.PicType, sig.Seal.PicData
		} else {
			sealType, sealData = extractSeal(data)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if len(sealData) == 0 {
		return "", nil, nil
	}
	return sealType, bytes.Clone(sealData), nil
}

// load 并发复用或按需提取同源印章媒体，返回只读媒体数据
// 入参: ctx 取消上下文, r 阅读器, src 印章数据源
// 返回: loadedStampMedia 印章媒体, error 读取或取消错误
func (c *stampSealCache) load(ctx context.Context, r *Reader, src stampSealSource) (loadedStampMedia, error) {
	if err := ctx.Err(); err != nil {
		return loadedStampMedia{}, err
	}
	if c.cache != nil {
		if media, ok := c.cache.get(src); ok {
			return media, nil
		}
	}
	sealType, sealData, err := src.load(ctx, r)
	if err != nil {
		return loadedStampMedia{}, err
	}
	media := loadedStampMedia{sealType: sealType, sealData: sealData}
	if c.cache != nil {
		cost := max(64, 64+len(src.sealPath)+len(src.signedValuePath)+len(sealType)+len(sealData))
		c.cache.put(src, media, cost)
	}
	return media, nil
}

// ensureSignatureIndexContext 按需读取签名描述索引，不加载印章二进制数据
// 入参: ctx 取消上下文
// 返回: error 结构读取或取消错误
func (r *Reader) ensureSignatureIndexContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || (r.doc == nil && r.ofd == nil) {
		return nil
	}
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return err
	}
	if r.sigIndexLoaded {
		return nil
	}
	if err := r.parseSignatures(ctx, doc); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.sigIndexLoaded = true
	return nil
}

// resolveStampPageID 按原文与规范资源标识匹配印章页面键
// 入参: pageID 页面标识
// 返回: string 匹配的页面键
func (r *Reader) resolveStampPageID(pageID string) string {
	if _, ok := r.stamps[pageID]; ok {
		return pageID
	}
	if _, ok := r.pendingStamps[pageID]; ok {
		return pageID
	}
	if key := editorResourceID(pageID); key != "" {
		if len(r.stamps) > 0 {
			if candidate, ok := r.resolveStampMapByCanon(key); ok {
				return candidate
			}
		}
		if len(r.pendingStamps) > 0 {
			if candidates := r.pendingStampCandidates(key); len(candidates) > 0 {
				return candidates[0]
			}
		}
		if id, ok := r.resolveDocPageIDByCanon(key); ok {
			return id
		}
	}
	return pageID
}

// resolveStampMapByCanon 按规范标识常数时间查找已加载印章映射中的页面键
// 入参: key 规范化十进制页面标识
// 返回: string 匹配的页面键, bool 是否命中
func (r *Reader) resolveStampMapByCanon(key string) (string, bool) {
	rebuild := func() {
		index := make(map[string]string, len(r.stamps))
		for candidate := range r.stamps {
			if canon := editorResourceID(candidate); canon != "" {
				if prev, exists := index[canon]; !exists || candidate < prev {
					index[canon] = candidate
				}
			}
		}
		r.stampCanon = index
		r.stampCanonLen = len(r.stamps)
	}
	rebuilt := false
	if r.stampCanon == nil || r.stampCanonLen != len(r.stamps) {
		rebuild()
		rebuilt = true
	}
	candidate, ok := r.stampCanon[key]
	if ok {
		if _, exists := r.stamps[candidate]; exists {
			return candidate, true
		}
		if !rebuilt {
			rebuild()
			if candidate, ok = r.stampCanon[key]; ok {
				if _, exists := r.stamps[candidate]; exists {
					return candidate, true
				}
			}
		}
		return "", false
	}
	if !rebuilt && len(r.stamps) > 0 {
		rebuild()
		if candidate, ok = r.stampCanon[key]; ok {
			if _, exists := r.stamps[candidate]; exists {
				return candidate, true
			}
		}
	}
	return "", false
}

// pendingStampCandidates 按规范标识常数时间获取待加载印章的所有排序页面键
// 入参: key 规范化十进制页面标识
// 返回: []string 匹配的页面键列表
func (r *Reader) pendingStampCandidates(key string) []string {
	if len(r.pendingStamps) == 0 || key == "" {
		return nil
	}
	if r.pendingStampKeys == nil {
		index := make(map[string][]string, len(r.pendingStamps))
		for candidate := range r.pendingStamps {
			if canon := editorResourceID(candidate); canon != "" {
				index[canon] = append(index[canon], candidate)
			}
		}
		for canon, list := range index {
			if len(list) > 1 {
				slices.Sort(list)
				index[canon] = list
			}
		}
		r.pendingStampKeys = index
	}
	return r.pendingStampKeys[key]
}

// ensurePageStampsContext 按页面标识惰性加载并填充单页印章二进制数据
// 入参: ctx 取消上下文, pageID 页面标识
// 返回: error 结构读取或取消错误
func (r *Reader) ensurePageStampsContext(ctx context.Context, pageID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	if err := r.ensureSignatureIndexContext(ctx); err != nil {
		return err
	}
	resolvedID := r.resolveStampPageID(pageID)
	if r.pageStampsLoaded != nil && (r.pageStampsLoaded[resolvedID] || r.pageStampsLoaded[pageID]) {
		r.pageStampsLoaded[resolvedID] = true
		r.pageStampsLoaded[pageID] = true
		return nil
	}
	var pendingKeys []string
	var pending []Stamp
	if list, ok := r.pendingStamps[resolvedID]; ok {
		pendingKeys = append(pendingKeys, resolvedID)
		pending = append(pending, list...)
	}
	if key := editorResourceID(resolvedID); key != "" && len(r.pendingStamps) > 0 {
		for _, candidate := range r.pendingStampCandidates(key) {
			if candidate != resolvedID {
				if list, ok := r.pendingStamps[candidate]; ok {
					pendingKeys = append(pendingKeys, candidate)
					pending = append(pending, list...)
				}
			}
		}
	}
	existing, hasExisting := r.stamps[resolvedID]
	if len(pending) == 0 {
		if r.pageStampsLoaded == nil {
			r.pageStampsLoaded = make(map[string]bool)
		}
		r.pageStampsLoaded[resolvedID] = true
		r.pageStampsLoaded[pageID] = true
		return nil
	}
	if r.stampSealCache == nil {
		r.stampSealCache = newStampSealCache(r.resources)
	}
	loaded := make([]Stamp, 0, len(existing)+len(pending))
	for _, stamp := range existing {
		if len(stamp.Data) > 0 {
			stamp.sealSource = stampSealSource{}
			loaded = append(loaded, stamp)
		}
	}
	for _, stamp := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(stamp.Data) > 0 {
			stamp.sealSource = stampSealSource{}
			loaded = append(loaded, stamp)
			continue
		}
		if stamp.sealSource == (stampSealSource{}) {
			continue
		}
		media, err := r.stampSealCache.load(ctx, r, stamp.sealSource)
		if err != nil {
			return err
		}
		if len(media.sealData) == 0 {
			continue
		}
		stamp.Type = media.sealType
		stamp.Data = media.sealData
		stamp.sealSource = stampSealSource{}
		loaded = append(loaded, stamp)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(loaded) > 0 {
		if r.stamps == nil {
			r.stamps = make(map[string][]Stamp)
		}
		r.stamps[resolvedID] = loaded
		if r.stampCanon != nil {
			if canon := editorResourceID(resolvedID); canon != "" {
				if prev, exists := r.stampCanon[canon]; !exists || resolvedID < prev {
					r.stampCanon[canon] = resolvedID
				}
			}
			r.stampCanonLen = len(r.stamps)
		}
	} else if hasExisting {
		delete(r.stamps, resolvedID)
		r.stampCanon = nil
	}
	for _, k := range pendingKeys {
		delete(r.pendingStamps, k)
	}
	if len(pendingKeys) > 0 && r.pendingStampKeys != nil {
		if canon := editorResourceID(resolvedID); canon != "" {
			delete(r.pendingStampKeys, canon)
		}
	}
	if r.pageStampsLoaded == nil {
		r.pageStampsLoaded = make(map[string]bool)
	}
	r.pageStampsLoaded[resolvedID] = true
	r.pageStampsLoaded[pageID] = true
	return nil
}

// ensureAllStampsContext 补齐当前文档全部尚未加载的页面印章二进制数据
// 入参: ctx 取消上下文
// 返回: error 结构读取或取消错误
func (r *Reader) ensureAllStampsContext(ctx context.Context) error {
	if err := r.ensureSignatureIndexContext(ctx); err != nil {
		return err
	}
	for _, pageID := range slices.Sorted(maps.Keys(r.pendingStamps)) {
		if err := r.ensurePageStampsContext(ctx, pageID); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// parseSignatures 解析签名描述索引，待加载印章存入私有缓冲且绝不污染公开Stamps字段
// 入参: ctx 取消上下文, doc 文档结构
// 返回: error 错误信息
func (r *Reader) parseSignatures(ctx context.Context, doc *Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if doc.Signatures == "" {
		return nil
	}
	sigListPath := r.ResPath(doc.Signatures)
	f, err := r.openFile(sigListPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var signatures Signatures
	if err := xml.NewDecoder(imageInput{ReadCloser: f, ctx: ctx}).Decode(&signatures); err != nil {
		return fmt.Errorf("parse signatures %s: %w", sigListPath, err)
	}
	pages := signaturePageNumbers(doc)
	var pending map[string][]Stamp
	for _, sigRef := range signatures.List {
		if err := ctx.Err(); err != nil {
			return err
		}
		if normalizeSignType(sigRef.Type) != SignTypeSeal {
			continue
		}
		if err := func(sigRef Signature) error {
			sigPath := cleanPackagePath(resolveResourcePath(sigListPath, "", sigRef.BaseLoc))
			if !validPackagePath(sigPath) {
				return nil
			}
			sf, err := r.openFile(sigPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return fmt.Errorf("open signature %s: %w", sigPath, err)
			}
			defer sf.Close()
			var sigFile SignatureFile
			if err := xml.NewDecoder(imageInput{ReadCloser: sf, ctx: ctx}).Decode(&sigFile); err != nil {
				return fmt.Errorf("parse signature %s: %w", sigPath, err)
			}
			if len(sigFile.SignedInfo.StampAnnot) == 0 {
				return nil
			}
			var src stampSealSource
			if sigFile.SignedInfo.Seal.BaseLoc != "" {
				if p := cleanPackagePath(resolveResourcePath(sigPath, "", sigFile.SignedInfo.Seal.BaseLoc)); validPackagePath(p) {
					src.sealPath = p
				}
			}
			if sigFile.SignedValue != "" {
				if p := cleanPackagePath(resolveResourcePath(sigPath, "", sigFile.SignedValue)); validPackagePath(p) {
					src.signedValuePath = p
				}
			}
			if src == (stampSealSource{}) {
				return nil
			}
			var entries []parsedStampEntry
			for _, annot := range sigFile.SignedInfo.StampAnnot {
				if err := ctx.Err(); err != nil {
					return err
				}
				page, ok := resourceValue(pages, annot.PageRef)
				if !ok {
					continue
				}
				bbox, err := parseSignatureStampBox(annot.Boundary)
				if err != nil {
					continue
				}
				var clipBox *Box
				if annot.Clip != "" {
					clip, err := parseSignatureStampBox(annot.Clip)
					if err != nil {
						continue
					}
					clipBox = &clip
				}
				entries = append(entries, parsedStampEntry{
					id:     annot.ID,
					pageID: doc.Pages.Page[page-1].ID,
					box:    bbox,
					clip:   clipBox,
				})
			}
			if len(entries) == 0 {
				return nil
			}
			if pending == nil {
				pending = make(map[string][]Stamp)
			}
			for _, entry := range entries {
				pending[entry.pageID] = append(pending[entry.pageID], Stamp{
					ID:          entry.id,
					SignatureID: sigRef.ID,
					Box:         entry.box,
					Clip:        entry.clip,
					sealSource:  src,
				})
			}
			return nil
		}(sigRef); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.pendingStamps = pending
	r.pendingStampKeys = nil
	return nil
}
