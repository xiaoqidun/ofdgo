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
	"fmt"
	"image"
	"image/color"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// imageCacheLimit 限制图片缓存的编码数据及预计解码内存
const imageCacheLimit = 64 << 20

// EncodedImage 保留PNG或JPEG原始编码，首次读取像素时解码
// Bytes返回只读数据，Image返回共享只读像素，解码错误由Image返回
type EncodedImage = baseimage.EncodedImage

// NewEncodedImage 复制输入数据并读取PNG或JPEG头信息，不解码像素
// 入参: data 原始编码
// 返回: *EncodedImage 惰性图片, error 头信息或格式错误
func NewEncodedImage(data []byte) (*EncodedImage, error) { return baseimage.NewEncodedImage(data) }

// ImageResource 按页面局部优先资源标识读取只读图片，PNG和JPEG保留原始编码并惰性解码
// 应先读取源页面以加载页面资源，返回EncodedImage时通过Image检查像素解码错误
// 入参: id 图片资源标识
// 返回: image.Image 图片资源, error 读取错误
func (r *Renderer) ImageResource(id string) (image.Image, error) {
	if r == nil || r.Reader == nil {
		return nil, fmt.Errorf("image resource %q not found", id)
	}
	path, ok := r.Reader.resolveResPathScoped(r.pageResources, id)
	if !ok {
		return nil, fmt.Errorf("image resource %q not found", id)
	}
	return r.cachedImageResource(path)
}

// resampledImageMask 按替换图尺寸读取已校验的二值蒙版，不分配缩放像素
type resampledImageMask struct {
	source   image.Image
	original image.Rectangle
	bounds   image.Rectangle
}

// Bounds 返回替换图对应的蒙版范围
// 返回: image.Rectangle 像素区域
func (s *resampledImageMask) Bounds() image.Rectangle { return s.bounds }

// ColorModel 返回二值蒙版的灰度颜色模型
// 返回: color.Model 灰度模型
func (s *resampledImageMask) ColorModel() color.Model { return color.GrayModel }

// GrayAt 按像素中心读取原蒙版，区域外返回黑色
// 入参: x 横坐标, y 纵坐标
// 返回: color.Gray 二值灰度
func (s *resampledImageMask) GrayAt(x, y int) color.Gray {
	if !image.Pt(x, y).In(s.bounds) {
		return color.Gray{}
	}
	bounds, size := s.original, s.bounds.Size()
	sx := bounds.Min.X + int((uint64(x-s.bounds.Min.X)*uint64(bounds.Dx())+uint64(bounds.Dx()/2))/uint64(size.X))
	sy := bounds.Min.Y + int((uint64(y-s.bounds.Min.Y)*uint64(bounds.Dy())+uint64(bounds.Dy()/2))/uint64(size.Y))
	return baseimage.GrayAt(s.source, sx, sy)
}

// At 返回采样后的二值灰度
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 像素颜色
func (s *resampledImageMask) At(x, y int) color.Color { return s.GrayAt(x, y) }

// BackingImage 返回共享只读蒙版，供图像缓存计算底层存储
// 返回: image.Image 共享只读蒙版
func (s *resampledImageMask) BackingImage() image.Image { return s.source }

// RGBA64At 返回不透明的十六位灰度颜色，供蒙版合成直接读取
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA64 像素颜色
func (s *resampledImageMask) RGBA64At(x, y int) color.RGBA64 {
	value := uint16(s.GrayAt(x, y).Y) * 257
	return color.RGBA64{R: value, G: value, B: value, A: 65535}
}

// imageObjectResource 在打印或高分辨率下优先读取替换图，失败时回退另一图像，零解码路径不展开像素
// 入参: object 图像对象
// 返回: image.Image 图片资源, error 读取或解码错误
func (r *Renderer) imageObjectResource(object ImageObject) (image.Image, error) {
	primary, fallback := object.ResourceID, object.Substitution
	if object.Substitution != "" && !sameResourceID(object.Substitution, object.ResourceID) && (r.printing || r.DPI >= 300) {
		primary, fallback = object.Substitution, object.ResourceID
	}
	hasFallback := fallback != "" && !sameResourceID(fallback, primary)
	read := func(id string) (image.Image, error) {
		img, err := r.ImageResource(id)
		if err == nil && r.decodeImages {
			img, err = baseimage.PixelDataContext(r.pageContext, img)
		} else if err == nil && hasFallback {
			_, err = baseimage.DecodeTransientContext(r.pageContext, img)
		}
		return img, err
	}
	img, err := read(primary)
	if err == nil || !hasFallback {
		return img, err
	}
	if r.pageContext != nil && r.pageContext.Err() != nil {
		return nil, r.pageContext.Err()
	}
	if r.OutputContext != nil && r.OutputContext.Err() != nil {
		return nil, r.OutputContext.Err()
	}
	img, fallbackErr := read(fallback)
	if fallbackErr != nil {
		return nil, fmt.Errorf("image resource %q: %w; fallback %q: %w", primary, err, fallback, fallbackErr)
	}
	return img, nil
}

// imageObjectMask 校验原图蒙版，替换图分辨率不同时按像素中心进行最近邻采样
// 入参: object 图片对象, target 实际绘制图像
// 返回: image.Image 二值蒙版，无蒙版时为nil, error 资源或蒙版错误
func (r *Renderer) imageObjectMask(object ImageObject, target image.Image) (image.Image, error) {
	if object.ImageMask == "" {
		return nil, nil
	}
	mask, err := r.ImageResource(object.ImageMask)
	if err != nil {
		return nil, err
	}
	size := target.Bounds().Size()
	if mask.Bounds().Size() == size {
		return mask, nil
	}
	if object.Substitution == "" {
		return nil, fmt.Errorf("image mask dimensions differ from source")
	}
	original, err := r.ImageResource(object.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("cannot verify image mask dimensions: %w", err)
	}
	if mask.Bounds().Size() != original.Bounds().Size() {
		return nil, fmt.Errorf("image mask dimensions differ from source")
	}
	ctx := r.pageContext
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateImageMask(ctx, mask, original.Bounds().Size()); err != nil {
		return nil, err
	}
	mask, err = baseimage.RawPixelDataContext(ctx, mask)
	if err != nil {
		return nil, err
	}
	return &resampledImageMask{source: mask, original: mask.Bounds(), bounds: image.Rectangle{Max: size}}, ctx.Err()
}

// cachedImageResource 复用原始图片资源，不依赖输出后端
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取错误
func (r *Renderer) cachedImageResource(resPath string) (image.Image, error) {
	if r.pageContext != nil {
		if err := r.pageContext.Err(); err != nil {
			return nil, err
		}
	}
	resPath = cleanPackagePath(r.Reader.ResPath(resPath))
	return r.imageCache.load(r.pageContext, resPath, func() (image.Image, int, error) {
		img, err := r.readImageResource(resPath)
		if err != nil {
			return nil, 0, err
		}
		return img, baseimage.StorageCost(img, r.imageCache.limit), nil
	})
}

// readImageResource 按页面上下文读取图片，借用已加载的只读编码
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取或解码错误
func (r *Renderer) readImageResource(resPath string) (image.Image, error) {
	ctx := r.pageContext
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := r.Reader.readFileViewContext(ctx, resPath)
	if err != nil {
		return nil, err
	}
	img, _, err := baseimage.DecodeContext(ctx, data)
	return img, err
}

// imageWithEffects 合并同尺寸二值蒙版和对象透明度，保留原图采样精度及只读像素
// 入参: ctx 取消上下文，nil使用后台上下文, img 原图, mask 二值蒙版，可为空, alpha 对象透明度，可为空
// 返回: image.Image 处理后的图片, error 蒙版、解码或取消错误
func imageWithEffects(ctx context.Context, img, mask image.Image, alpha *int) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a := 255
	if alpha != nil {
		a = clampColor(*alpha)
	}
	if img == nil || mask == nil && a == 255 {
		return img, nil
	}
	source, err := baseimage.PixelDataContext(ctx, img)
	if err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	var maskBounds image.Rectangle
	if mask != nil {
		mask, err = baseimage.RawPixelDataContext(ctx, mask)
		if err != nil {
			return nil, err
		}
		maskBounds = mask.Bounds()
		if bounds.Empty() || bounds.Size() != maskBounds.Size() {
			return nil, fmt.Errorf("image mask dimensions differ from source")
		}
	}
	if baseimage.HighPrecision(source) {
		out := image.NewNRGBA64(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				opacity := a
				if mask != nil {
					value, err := imageMaskAlpha(mask, maskBounds.Min.X+x-bounds.Min.X, maskBounds.Min.Y+y-bounds.Min.Y)
					if err != nil {
						return nil, err
					}
					opacity *= int(value) / 255
				}
				pixel := baseimage.NRGBA64At(source, x, y)
				pixel.A = uint16(uint32(pixel.A) * uint32(opacity) / 255)
				out.SetNRGBA64(x, y, pixel)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := out.Pix[(y-bounds.Min.Y)*out.Stride : (y-bounds.Min.Y+1)*out.Stride]
		if src, ok := source.(*image.NRGBA); ok {
			offset := src.PixOffset(bounds.Min.X, y)
			copy(row, src.Pix[offset:offset+len(row)])
		} else {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				out.SetNRGBA(x, y, baseimage.NRGBAAt(source, x, y))
			}
		}
		for x := 0; x < bounds.Dx(); x++ {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			opacity := a
			if mask != nil {
				value, err := imageMaskAlpha(mask, maskBounds.Min.X+x, maskBounds.Min.Y+y-bounds.Min.Y)
				if err != nil {
					return nil, err
				}
				opacity *= int(value) / 255
			}
			row[x*4+3] = uint8(int(row[x*4+3]) * opacity / 255)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// imageMaskAlpha 读取二值蒙版，拒绝灰阶、彩色及带透明度的像素
// 入参: mask 蒙版图像, x 横坐标, y 纵坐标
// 返回: uint8 透明度，黑色为0、白色为255, error 非二值像素错误
func imageMaskAlpha(mask image.Image, x, y int) (uint8, error) {
	var r, g, b, a uint32
	if source, ok := mask.(image.RGBA64Image); ok {
		pixel := source.RGBA64At(x, y)
		r, g, b, a = uint32(pixel.R), uint32(pixel.G), uint32(pixel.B), uint32(pixel.A)
	} else {
		r, g, b, a = mask.At(x, y).RGBA()
	}
	if a != 65535 || r != g || r != b || r != 0 && r != 65535 {
		return 0, fmt.Errorf("image mask contains non-binary pixel at (%d, %d)", x, y)
	}
	return uint8(r >> 8), nil
}

// validateImageMask 校验二值蒙版尺寸及像素，不分配合成图像
// 入参: ctx 取消上下文, mask 蒙版图像, size 原图尺寸
// 返回: error 解码、尺寸、像素或取消错误
func validateImageMask(ctx context.Context, mask image.Image, size image.Point) error {
	if mask.Bounds().Size() != size || size.X <= 0 || size.Y <= 0 {
		return fmt.Errorf("image mask dimensions differ from source")
	}
	mask, err := baseimage.RawPixelDataContext(ctx, mask)
	if err != nil {
		return err
	}
	bounds := mask.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if _, err := imageMaskAlpha(mask, x, y); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
