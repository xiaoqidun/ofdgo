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

package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sync"
	"sync/atomic"

	"github.com/xiaoqidun/pdfgo"
)

// EncodedImage 保留PNG或JPEG原始编码，首次读取像素时解码
// Bytes返回只读数据，Image返回共享只读像素，解码错误由Image返回
type EncodedImage struct {
	data       []byte
	format     string
	config     image.Config
	profile    []byte
	colorSpace *pdfgo.ColorSpace
	pngColor   *pngColor
	result     atomic.Pointer[encodedImageResult]
	mu         sync.Mutex
	pending    chan struct{}
}

// NewEncodedImage 校验PNG或JPEG头信息后复制输入，不解码像素
// 入参: data 原始编码
// 返回: *EncodedImage 惰性图片, error 头信息或格式错误
func NewEncodedImage(data []byte) (*EncodedImage, error) {
	img, err := newEncodedImage(data)
	if err != nil {
		return nil, err
	}
	img.data = bytes.Clone(data)
	return img, nil
}

// Bytes 返回只读原始编码，调用方不得修改
// 返回: []byte 原始编码
func (img *EncodedImage) Bytes() []byte { return img.data }

// MIME 返回图片媒体类型
// 返回: string 媒体类型
func (img *EncodedImage) MIME() string { return "image/" + img.format }

// ColorModel 返回显示像素的颜色模型，不解码像素
// 返回: color.Model 颜色模型
func (img *EncodedImage) ColorModel() color.Model {
	if img.profile != nil || img.pngColor != nil || img.config.ColorModel == color.CMYKModel {
		return color.NRGBA64Model
	}
	return img.config.ColorModel
}

// Bounds 返回图片范围，不解码像素
// 返回: image.Rectangle 图片范围
func (img *EncodedImage) Bounds() image.Rectangle {
	return image.Rect(0, 0, img.config.Width, img.config.Height)
}

// Image 首次解码图片并缓存结果或错误，支持并发读取
// 返回: image.Image 只读像素, error 解码错误
func (img *EncodedImage) Image() (image.Image, error) {
	if result := img.result.Load(); result != nil {
		return result.image, result.err
	}
	return img.ImageContext(context.Background())
}

// ImageContext 解码并共享只读像素，取消或超时后可重试，nil使用后台上下文
// 并发调用等待正在进行的解码，等待方取消不影响其他调用
// 入参: ctx 取消上下文
// 返回: image.Image 只读像素, error 解码或取消错误
func (img *EncodedImage) ImageContext(ctx context.Context) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if result := img.result.Load(); result != nil {
			return result.image, result.err
		}
		img.mu.Lock()
		if img.result.Load() != nil {
			img.mu.Unlock()
			continue
		}
		pending := img.pending
		if pending == nil {
			pending = make(chan struct{})
			img.pending = pending
			img.mu.Unlock()
			return img.decodeContext(ctx, pending)
		}
		img.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending:
		}
	}
}

// At 返回像素颜色，解码失败时返回透明色，绘制前应通过Image检查错误
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 像素颜色
func (img *EncodedImage) At(x, y int) color.Color {
	decoded, err := img.Image()
	if err != nil {
		return color.Transparent
	}
	return decoded.At(x, y)
}

// encodedImageResult 保存已完成的像素解码结果，不保存取消或超时错误
type encodedImageResult struct {
	image image.Image
	err   error
}

// decodeContext 执行本次解码，退出时唤醒等待方，只缓存完整结果及稳定错误
// 入参: ctx 取消上下文, pending 本次解码的完成通知
// 返回: image.Image 只读像素, error 解码或取消错误
func (img *EncodedImage) decodeContext(ctx context.Context, pending chan struct{}) (image.Image, error) {
	defer func() {
		img.mu.Lock()
		img.pending = nil
		close(pending)
		img.mu.Unlock()
	}()
	pixels, err := img.decodePixels(ctx)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	img.result.Store(&encodedImageResult{image: pixels, err: err})
	return pixels, err
}

// decodeTransient 返回已缓存结果或执行单次临时解码，不将未压缩像素写入长期缓存
// 入参: ctx 取消上下文
// 返回: image.Image 解码像素, error 解码或取消错误
func (img *EncodedImage) decodeTransient(ctx context.Context) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result := img.result.Load(); result != nil {
		return result.image, result.err
	}
	pixels, err := img.decodePixels(ctx)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	return pixels, err
}

// decodePixels 解码底层字节流并按需执行色彩空间转换
// 入参: ctx 取消上下文
// 返回: image.Image 解码像素, error 解码或色彩错误
func (img *EncodedImage) decodePixels(ctx context.Context) (image.Image, error) {
	pixels, _, err := image.Decode(contextInput{ReadCloser: io.NopCloser(bytes.NewReader(img.data)), ctx: ctx})
	if err == nil && (img.format == "jpeg" || img.profile != nil) {
		profile := img.colorSpace
		if profile == nil && img.profile != nil {
			profile, err = pdfgo.NewICCColorSpace(img.profile)
		}
		if err == nil {
			pixels, err = jpegDisplayImage(ctx, pixels, profile)
		}
	}
	if err == nil && img.pngColor != nil {
		pixels, err = applyImageColorProfile(ctx, pixels, img.pngColor.space, img.pngColor.linearSRGB)
	}
	return pixels, err
}

// newEncodedImage 接管编码数据并读取头信息
// 入参: data 原始编码
// 返回: *EncodedImage 惰性图片, error 头信息或格式错误
func newEncodedImage(data []byte) (*EncodedImage, error) {
	return newEncodedImageContext(context.Background(), data)
}

// newEncodedImageContext 读取PNG或JPEG头信息，元数据扫描期间响应取消，不复制输入或解码像素
// 入参: ctx 取消上下文, data 只读图片编码
// 返回: *EncodedImage 惰性图片, error 头信息、格式或取消错误
func newEncodedImageContext(ctx context.Context, data []byte) (*EncodedImage, error) {
	config, format, err := image.DecodeConfig(contextInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	if err != nil {
		return nil, err
	}
	if format != "jpeg" && format != "png" {
		return nil, fmt.Errorf("unsupported encoded image %q", format)
	}
	img := &EncodedImage{data: data, format: format, config: config}
	if format == "jpeg" {
		img.profile, err = jpegColorProfile(ctx, data, config.ColorModel)
		if err != nil {
			return nil, err
		}
	}
	if format == "png" {
		img.profile, img.pngColor, err = pngColorProfile(ctx, data)
		if err != nil {
			return nil, err
		}
	}
	if isEightBitRGBModel(config.ColorModel) {
		if img.profile != nil {
			if space, profileErr := pdfgo.NewICCColorSpace(img.profile); profileErr == nil {
				if isEquivalentSRGBSpace(space, false) {
					img.profile = nil
				} else {
					img.colorSpace = space
				}
			}
		}
		if img.pngColor != nil && isEquivalentSRGBSpace(img.pngColor.space, img.pngColor.linearSRGB) {
			img.pngColor = nil
		}
	}
	return img, nil
}
