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
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"reflect"
	"sync"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// PDF原生有损照片防膨胀JPEG编码参数
const (
	pdfPhotoJPEGQuality = 92
	pdfPhotoMinPixels   = 65536
)

// pdfImageKey 区分图像引用或流、渲染意图、原生输出空间、模板填充色及采样尺寸
type pdfImageKey struct {
	ref     pdfgo.Reference
	stream  *pdfgo.Stream
	intent  pdfgo.Name
	process pdfgo.Name
	tint    color.NRGBA64
	size    image.Point
}

// pdfImageResource 保存图像的有效颜色空间及对应资源，避免跨资源重映射复用
type pdfImageResource struct {
	space pdfgo.Object
	id    string
}

// pdfImageContentKey 区分编码内容、渲染意图、原生输出空间、模板填充色及有损采样尺寸
type pdfImageContentKey struct {
	data    [32]byte
	intent  pdfgo.Name
	process pdfgo.Name
	tint    color.NRGBA64
	size    image.Point
}

// pdfImageContent 保存不依赖间接资源的图像描述及已注册编号
type pdfImageContent struct {
	source *pdfgo.Image
	id     string
}

// pdfSharedImageEntry 保存跨工作协程去重编码的单张图像结果
type pdfSharedImageEntry struct {
	space     pdfgo.Object
	done      chan struct{}
	data      []byte
	optimized bool
	err       error
}

// pdfSharedImageCache 在多核并发转换期间按流与参数去重编解码图像，避免重复工作
type pdfSharedImageCache struct {
	mu      sync.Mutex
	entries map[pdfImageKey][]*pdfSharedImageEntry
}

// newPDFSharedImageCache 创建跨工作协程图像编码缓存
// 返回: *pdfSharedImageCache 并发安全的图像编码缓存
func newPDFSharedImageCache() *pdfSharedImageCache {
	return &pdfSharedImageCache{entries: make(map[pdfImageKey][]*pdfSharedImageEntry)}
}

// pdfImageLossyFilter 检查图像底层流末级滤镜是否为JPXDecode或DCTDecode
// 入参: reader 来源阅读器, source 源图像
// 返回: bool 是否为JPXDecode, bool 是否为DCTDecode
func pdfImageLossyFilter(reader *pdfgo.Reader, source *pdfgo.Image) (bool, bool) {
	if source == nil || source.Stream == nil || source.Stream.Dictionary == nil {
		return false, false
	}
	filterObj := source.Stream.Dictionary["Filter"]
	if filterObj == nil {
		return false, false
	}
	if reader != nil {
		if resolved, err := reader.Resolve(filterObj); err == nil {
			filterObj = resolved
		}
	}
	var last pdfgo.Name
	switch value := filterObj.(type) {
	case pdfgo.Name:
		last = value
	case pdfgo.Array:
		if len(value) == 0 {
			return false, false
		}
		item := value[len(value)-1]
		if reader != nil {
			if resolved, err := reader.Resolve(item); err == nil {
				item = resolved
			}
		}
		last, _ = item.(pdfgo.Name)
	}
	return last == "JPXDecode", last == "DCTDecode" || last == "DCT"
}

// pdfImageOpaque 检查已解码图像是否所有像素完全不透明
// 入参: img 已解码图像
// 返回: bool 是否完全不透明
func pdfImageOpaque(img image.Image) bool {
	if img == nil {
		return false
	}
	if op, ok := img.(interface{ Opaque() bool }); ok {
		return op.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// pdfImageMostlyFlat 均匀抽样判断图像是否大面积平涂，用于决定大图是否对比PNG体积
// 入参: img 已解码图像
// 返回: bool 是否高度平涂
func pdfImageMostlyFlat(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 2 || h < 2 {
		return true
	}
	stepX := max(1, w/32)
	stepY := max(1, h/32)
	total, same := 0, 0
	for y := b.Min.Y; y+1 < b.Max.Y; y += stepY {
		for x := b.Min.X; x+1 < b.Max.X; x += stepX {
			r0, g0, b0, _ := img.At(x, y).RGBA()
			r1, g1, b1, _ := img.At(x+1, y).RGBA()
			if r0 == r1 && g0 == g1 && b0 == b1 {
				same++
			}
			total++
		}
	}
	return total > 0 && same*100 >= total*85
}

// encodeLossyPhotoCandidate 将不透明有损照片编码为高质量JPEG，小图或平涂图对比PNG后保留更小结果
// 入参: ctx 取消上下文, decoded 已解码图像
// 返回: []byte 编码字节, bool 是否输出为JPEG, error 编码或取消错误
func encodeLossyPhotoCandidate(ctx context.Context, decoded image.Image) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, decoded, &jpeg.Options{Quality: pdfPhotoJPEGQuality}); err != nil {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	b := decoded.Bounds()
	pixels := int64(b.Dx()) * int64(b.Dy())
	if pixels < pdfPhotoMinPixels || pdfImageMostlyFlat(decoded) || int64(jpgBuf.Len())*4 >= pixels*3 {
		var pngBuf bytes.Buffer
		if err := pdfgo.EncodePNG(ctx, &pngBuf, decoded); err == nil && pngBuf.Len() <= jpgBuf.Len() {
			return pngBuf.Bytes(), false, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
	}
	return jpgBuf.Bytes(), true, nil
}

// matches 核对完整编码与有效图像属性，不以摘要相同代替内容相同
// 入参: source 待复用图像
// 返回: bool 是否可使用同一图像资源
func (c pdfImageContent) matches(source *pdfgo.Image) bool {
	return codecpdf.SameImage(c.source, source)
}

// acquire 获取或占位指定图像编码任务，由首位调用方执行实际编解码
// 入参: key 图像流及采样键, space 有效颜色空间
// 返回: *pdfSharedImageEntry 任务条目, bool 是否由当前协程执行编码
func (c *pdfSharedImageCache) acquire(key pdfImageKey, space pdfgo.Object) (*pdfSharedImageEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries[key] {
		if reflect.DeepEqual(entry.space, space) {
			return entry, false
		}
	}
	entry := &pdfSharedImageEntry{space: space, done: make(chan struct{})}
	c.entries[key] = append(c.entries[key], entry)
	return entry, true
}

// finish 写入图像编码结果并唤醒等待者，失败时移除占位以便后续重试
// 入参: key 图像流及采样键, entry 占位条目, data 编码数据, optimized 是否已执行有损优化, err 编码错误
func (c *pdfSharedImageCache) finish(key pdfImageKey, entry *pdfSharedImageEntry, data []byte, optimized bool, err error) {
	entry.data, entry.optimized, entry.err = data, optimized, err
	if err != nil {
		c.mu.Lock()
		list := c.entries[key]
		for i, item := range list {
			if item == entry {
				c.entries[key] = append(list[:i], list[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
	}
	close(entry.done)
}

// image 转换图像样本，不将整页或其他对象栅格化
// 有损编码复用已解码像素，候选容量超限时沿用原编码路径
// 入参: mark PDF图像绘制信息
// 返回: error 错误信息
func (p *pdfImporter) image(mark pdfgo.ImageMark) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "image blend mode " + string(mark.Style.BlendMode)}
	}
	if mark.Image.ImageMask && (mark.Style.Fill.Axial != nil || mark.Style.Fill.Radial != nil || mark.Style.Fill.Function != nil || mark.Style.Fill.Mesh != nil || mark.Style.Fill.Shading != nil) {
		if p.warning != nil {
			return p.compositeRegion(nil, pdfCompositeNode{image: &mark}, &pdfgo.ColorSpace{Model: "DeviceRGB"}, false)
		}
		return &pdfgo.UnsupportedError{Feature: "gradient stencil image"}
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Image(mark) })
		}
		return err
	}
	if mark.Style.FillOverprint {
		return &pdfgo.UnsupportedError{Feature: "image color separation overprint"}
	}
	source := mark.Image
	if p.imageWorkers > 0 && source.Workers == 0 {
		source.Workers = p.imageWorkers
	}
	allowRefKey := !p.hasReferenceDocuments()
	key := pdfImageKey{stream: source.Stream, intent: source.Intent}
	if allowRefKey && source.Stream != nil && source.Stream.Reference != (pdfgo.Reference{}) {
		key.ref = source.Stream.Reference
		key.stream = nil
	}
	key.process, err = p.imageProcessModel(source)
	if err != nil {
		return err
	}
	if !source.ImageMask && p.compression.Mode == CompressionLossy {
		m := p.matrix.Mul(mark.Matrix)
		scaleBoost := max(1.0, p.imageScaleBoost)
		key.size = compressionImageSize(math.Hypot(m[0], m[1])*scaleBoost, math.Hypot(m[2], m[3])*scaleBoost, p.compression.ImageDPI())
		if key.size.X >= source.Width || key.size.Y >= source.Height {
			key.size = image.Point{}
		}
	}
	if source.ImageMask {
		fill := mark.Style.Fill.RGB
		if space := mark.Style.Fill.Space; space != nil {
			fill, err = space.SRGB(mark.Style.Fill.Values[:space.Components()], mark.Style.RenderingIntent)
		} else if values := mark.Style.Fill.CMYK; values != nil {
			space := &pdfgo.ColorSpace{Model: "DeviceCMYK"}
			fill, err = space.SRGB(values[:], mark.Style.RenderingIntent)
		}
		if err != nil {
			return err
		}
		key.tint = color.NRGBA64{
			R: uint16(math.Round(fill[0] * 65535)),
			G: uint16(math.Round(fill[1] * 65535)),
			B: uint16(math.Round(fill[2] * 65535)),
			A: 65535,
		}
	}
	id := ""
	for _, resource := range p.imageIDs[key] {
		if reflect.DeepEqual(resource.space, source.ColorSpace) {
			id = resource.id
			break
		}
	}
	streamCached := id != ""
	var contentKey pdfImageContentKey
	cacheable := false
	if id == "" {
		contentKey, cacheable, err = p.imageContentKey(source, key)
		if err != nil {
			return err
		}
		if cacheable {
			if p.imageContents == nil {
				p.imageContents = resourceCache[pdfImageContentKey, pdfImageContent](p.resources, 8<<20)
			}
			if cached, found := p.imageContents.get(contentKey); found && cached.matches(source) {
				id = cached.id
			}
		}
	}
	if id == "" {
		var data []byte
		var decoded image.Image
		optimized := false
		var sharedEntry *pdfSharedImageEntry
		sharedOwner := false
		if allowRefKey && p.sharedImages != nil && key.process == "" && key.ref != (pdfgo.Reference{}) {
			sharedEntry, sharedOwner = p.sharedImages.acquire(key, source.ColorSpace)
			if !sharedOwner {
				select {
				case <-sharedEntry.done:
					if sharedEntry.err != nil {
						return sharedEntry.err
					}
					data, optimized = sharedEntry.data, sharedEntry.optimized
				case <-p.ctx.Done():
					return p.ctx.Err()
				}
			}
		}
		if len(data) == 0 {
			var encodeErr error
			preferJPEG := false
			if key.process != "" {
				decoded, encodeErr = p.imageProcess(source, key.process)
				if encodeErr == nil && key.size != (image.Point{}) {
					decoded, encodeErr = pdfgo.ResizeImage(p.ctx, decoded, key.size)
				}
			} else {
				data, encodeErr = source.JBIG2FileContext(p.ctx)
				if encodeErr == nil && len(data) == 0 {
					data, encodeErr = source.JPEGFileContext(p.ctx)
				}
				if encodeErr == nil && len(data) == 0 && !source.ImageMask && p.compression.Mode != CompressionLossy {
					data, encodeErr = codecpdf.EncodeCMYKTIFF(p.ctx, source)
				}
				if encodeErr == nil && len(data) == 0 {
					preferJPEG, encodeErr = p.preferLossyPhotoJPEG(source, key.process)
				}
				if encodeErr == nil && len(data) == 0 && (source.ImageMask || key.size != (image.Point{}) || p.compression.Mode == CompressionLossy || preferJPEG) {
					decoded, encodeErr = source.DecodeImageSizeContext(p.ctx, key.size)
				}
			}
			if encodeErr == nil && source.ImageMask {
				decoded = &codecpdf.StencilImage{Source: decoded, Fill: key.tint}
			}
			if encodeErr == nil && len(data) == 0 && decoded != nil && !source.ImageMask && p.compression.Mode == CompressionLossy {
				data, encodeErr = pdfgo.EncodeImageResource(p.ctx, decoded, p.compression)
				if encodeErr == nil {
					optimized = true
				} else if errors.Is(encodeErr, io.ErrShortBuffer) {
					encodeErr = nil
				}
			}
			if encodeErr == nil && len(data) == 0 && decoded != nil && preferJPEG && pdfImageOpaque(decoded) {
				var isJPEG bool
				data, isJPEG, encodeErr = encodeLossyPhotoCandidate(p.ctx, decoded)
				if encodeErr == nil && isJPEG {
					decoded = nil
				}
			}
			if encodeErr == nil && len(data) == 0 {
				var encoded bytes.Buffer
				if decoded != nil {
					encodeErr = pdfgo.EncodePNG(p.ctx, &encoded, decoded)
				} else {
					encodeErr = source.EncodePNGContext(p.ctx, &encoded)
				}
				if encodeErr == nil {
					data = encoded.Bytes()
				}
			}
			if encodeErr == nil && !source.ImageMask && p.compression.Mode == CompressionLossy {
				if !optimized {
					data, encodeErr = pdfgo.OptimizeImageResource(p.ctx, data, p.compression, key.size)
					if encodeErr == nil {
						optimized = true
					}
				}
				decoded = nil
			}
			if sharedOwner {
				p.sharedImages.finish(key, sharedEntry, data, optimized, encodeErr)
			}
			if encodeErr != nil {
				return encodeErr
			}
		}
		id, err = p.editor.addDecodedImageContext(p.ctx, data, decoded)
		if err != nil {
			return err
		}
		if optimized {
			p.editor.markOptimizedImage(id, p.compression)
		}
		if cacheable {
			p.imageContents.put(contentKey, pdfImageContent{source: source, id: id}, len(source.Stream.Data)+65536)
		}
	}
	if !streamCached {
		if p.imageIDs == nil {
			p.imageIDs = map[pdfImageKey][]pdfImageResource{}
		}
		p.imageIDs[key] = append(p.imageIDs[key], pdfImageResource{space: source.ColorSpace, id: id})
	}
	return p.appendImage(mark, id)
}

// appendImage 定位已注册的图像资源，保留透明度与裁剪
// 入参: mark 图像绘制信息, id 资源编号
// 返回: error 裁剪错误
func (p *pdfImporter) appendImage(mark pdfgo.ImageMark, id string) error {
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1, 0, 0, -1, 0, 1})
	box := pdfBounds([]pdfgo.Point{m.Apply(pdfgo.Point{}), m.Apply(pdfgo.Point{X: 1}), m.Apply(pdfgo.Point{Y: 1}), m.Apply(pdfgo.Point{X: 1, Y: 1})})
	alpha := int(math.Round(mark.Style.Fill.Alpha * 255))
	clips, err := p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	object := ImageObject{Boundary: pdfBoundary(box), ResourceID: id, CTM: pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y), Alpha: &alpha, Clips: clips}
	p.appendSequenceObject(GraphicObject{Type: "ImageObject", ImageObject: object}, mark.Sequence)
	p.report.ImageObjects++
	return nil
}

// preferLossyPhotoJPEG 判断默认导入模式下是否将原生不透明8位有损照片编码为高质量JPEG
// 入参: source 源图像, process 原生过程色输出空间
// 返回: bool 是否优先编码为高质量JPEG, error 取消错误
func (p *pdfImporter) preferLossyPhotoJPEG(source *pdfgo.Image, process pdfgo.Name) (bool, error) {
	if p.compression.Mode != CompressionUnchanged || process != "" || source == nil || source.ImageMask || source.Mask != nil || source.SoftMask != nil || source.BitsPerComponent != 8 {
		return false, nil
	}
	if int64(source.Width)*int64(source.Height) < pdfPhotoMinPixels {
		return false, nil
	}
	jpx, dct := pdfImageLossyFilter(p.reader, source)
	if !jpx && !dct {
		return false, nil
	}
	if family, err := source.ColorSpaceFamily(); err != nil || family == "Indexed" {
		return false, nil
	}
	if masked, err := source.HasMask(); err != nil || masked {
		return false, nil
	}
	if jpx {
		lossless, maxBitDepth, err := source.JPXLosslessContext(p.ctx)
		if err != nil {
			if p.ctx.Err() != nil {
				return false, p.ctx.Err()
			}
			return false, nil
		}
		if lossless || maxBitDepth != 8 {
			return false, nil
		}
	}
	return true, nil
}

// imageContentKey 对无间接依赖的小图像生成内容键，超大图像仍沿用流缓存
// 入参: source 图像描述, key 流缓存键
// 返回: pdfImageContentKey 内容键, bool 是否可复用, error 取消错误
func (p *pdfImporter) imageContentKey(source *pdfgo.Image, key pdfImageKey) (pdfImageContentKey, bool, error) {
	digest, reusable, err := codecpdf.ImageDigest(p.ctx, source)
	if !reusable {
		return pdfImageContentKey{}, reusable, err
	}
	return pdfImageContentKey{data: digest, intent: key.intent, process: key.process, tint: key.tint, size: key.size}, true, err
}

// imageProcessModel 选择与页面原生过程色一致的图像输出，已等价的备用色沿用现有编码路径
// 入参: source 源图像
// 返回: pdfgo.Name 原生输出空间，无需重映射时为空, error 色料解析错误
func (p *pdfImporter) imageProcessModel(source *pdfgo.Image) (pdfgo.Name, error) {
	space := p.compositeSpace
	if source.ImageMask || !space.Device() {
		return "", nil
	}
	colorants, err := source.ColorantSpace()
	if err != nil {
		return "", err
	}
	process := colorants.PrepareProcess(&pdfgo.ColorantDevice{Space: space}, space, false)
	if process == nil || process.Mask() == ([4]bool{}) || process.AlternateEquivalent() {
		return "", nil
	}
	return space.Model, nil
}

// imageProcess 复用源分量并保留原始分辨率，颜色变换随编码逐像素执行
// 入参: source 源图像, model 原生设备空间
// 返回: image.Image 只读图像, error 解码或分量映射错误
func (p *pdfImporter) imageProcess(source *pdfgo.Image, model pdfgo.Name) (image.Image, error) {
	cache := p.compositingCache()
	space := &pdfgo.ColorSpace{Model: model}
	process, err := cache.imageProcess(p, source, space, false)
	if err != nil {
		return nil, err
	}
	if process == nil {
		return nil, fmt.Errorf("invalid native PDF image process space %s", model)
	}
	return &codecpdf.NativeImage{Source: process, Space: space}, nil
}

// imageProcess 复用同组原生采样，仅在需要备用颜色时保留完整分量
// 入参: importer 导入器, source 源图像, space 组混合空间, softMask 是否用于软蒙版
// 返回: *pdfgo.ImageProcess 原生采样，需要备用色时为nil, error 解码或布局错误
func (c *pdfCompositeCache) imageProcess(importer *pdfImporter, source *pdfgo.Image, space *pdfgo.ColorSpace, softMask bool) (*pdfgo.ImageProcess, error) {
	key := pdfImageProcessKey{image: source, space: space, softMask: softMask}
	if process, found := c.imageProcesses[key]; found {
		return process, nil
	}
	device := &pdfgo.ColorantDevice{Space: space}
	var process *pdfgo.ImageProcess
	var err error
	if components := c.images[source]; components != nil {
		process, err = components.PrepareProcess(device, space, softMask)
	} else {
		process, err = source.DecodeProcessContext(importer.ctx, device, space, softMask)
	}
	if err != nil {
		return nil, err
	}
	if c.imageProcesses == nil {
		c.imageProcesses = make(map[pdfImageProcessKey]*pdfgo.ImageProcess)
	}
	c.imageProcesses[key] = process
	return process, nil
}
