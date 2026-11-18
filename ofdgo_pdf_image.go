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
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"reflect"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfImageKey 区分图像流、渲染意图、原生输出空间、模板填充色及采样尺寸
type pdfImageKey struct {
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

// matches 核对完整编码与有效图像属性，不以摘要相同代替内容相同
// 入参: source 待复用图像
// 返回: bool 是否可使用同一图像资源
func (c pdfImageContent) matches(source *pdfgo.Image) bool {
	return codecpdf.SameImage(c.source, source)
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
	key := pdfImageKey{stream: source.Stream, intent: source.Intent}
	key.process, err = p.imageProcessModel(source)
	if err != nil {
		return err
	}
	if !source.ImageMask && p.compression.Mode == CompressionLossy {
		m := p.matrix.Mul(mark.Matrix)
		key.size = compressionImageSize(math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3]), p.compression.ImageDPI())
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
		if !source.ImageMask && key.process == "" && p.compression.Mode != CompressionLossy {
			data, err = codecpdf.EncodeCMYKTIFF(p.ctx, source)
			if err != nil {
				return err
			}
		}
		var jbig2Original, jpegOriginal []byte
		var decoded image.Image
		if len(data) == 0 {
			if key.process != "" {
				decoded, err = p.imageProcess(source, key.process)
				if err != nil {
					return err
				}
				if key.size != (image.Point{}) {
					decoded, err = pdfgo.ResizeImage(p.ctx, decoded, key.size)
					if err != nil {
						return err
					}
				}
			} else {
				jbig2Original, err = source.JBIG2FileContext(p.ctx)
				if err != nil {
					return err
				}
				jpegOriginal, err = source.JPEGFileContext(p.ctx)
				if err != nil {
					return err
				}
				if len(jbig2Original) == 0 && len(jpegOriginal) == 0 && (source.ImageMask || key.size != (image.Point{})) {
					decoded, err = source.DecodeImageSizeContext(p.ctx, key.size)
					if err != nil {
						return err
					}
				}
			}
			if source.ImageMask {
				decoded = &codecpdf.StencilImage{Source: decoded, Fill: key.tint}
			}
			data = jbig2Original
			if len(data) == 0 {
				data = jpegOriginal
			}
		}
		optimized := false
		if len(data) == 0 && decoded != nil && !source.ImageMask && p.compression.Mode == CompressionLossy {
			data, err = pdfgo.EncodeImageResource(p.ctx, decoded, p.compression)
			if err != nil && !errors.Is(err, io.ErrShortBuffer) {
				return err
			}
			optimized = err == nil
		}
		if len(data) == 0 {
			var encoded bytes.Buffer
			if decoded != nil {
				err = pdfgo.EncodePNG(p.ctx, &encoded, decoded)
			} else {
				err = source.EncodePNGContext(p.ctx, &encoded)
			}
			if err != nil {
				return err
			}
			data = encoded.Bytes()
		}
		if !source.ImageMask && p.compression.Mode == CompressionLossy {
			if !optimized {
				data, err = pdfgo.OptimizeImageResource(p.ctx, data, p.compression, key.size)
				if err != nil {
					return err
				}
			}
			decoded = nil
		}
		id, err = p.editor.addDecodedImageContext(p.ctx, data, decoded)
		if err != nil {
			return err
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
