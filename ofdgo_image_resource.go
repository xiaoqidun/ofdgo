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
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sync"
)

// EncodedImage 保留PNG或JPEG原始编码，首次读取像素时解码
// Bytes返回只读数据，Image返回共享只读像素，解码错误由Image返回
type EncodedImage struct {
	data    []byte
	format  string
	config  image.Config
	once    sync.Once
	decoded image.Image
	err     error
}

// NewEncodedImage 读取PNG或JPEG头信息，不解码像素，复制输入数据
// 入参: data 原始编码
// 返回: *EncodedImage 惰性图片, error 头信息或格式错误
func NewEncodedImage(data []byte) (*EncodedImage, error) {
	return newEncodedImage(bytes.Clone(data))
}

// newEncodedImage 接管编码数据并读取头信息
// 入参: data 原始编码
// 返回: *EncodedImage 惰性图片, error 头信息或格式错误
func newEncodedImage(data []byte) (*EncodedImage, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if format != "jpeg" && format != "png" {
		return nil, fmt.Errorf("unsupported encoded image %q", format)
	}
	return &EncodedImage{data: data, format: format, config: config}, nil
}

// Bytes 返回只读原始编码，调用方不得修改
// 返回: []byte 原始编码
func (img *EncodedImage) Bytes() []byte { return img.data }

// MIME 返回图片媒体类型
// 返回: string 媒体类型
func (img *EncodedImage) MIME() string { return "image/" + img.format }

// ColorModel 返回头信息中的颜色模型，不解码像素
// 返回: color.Model 颜色模型
func (img *EncodedImage) ColorModel() color.Model { return img.config.ColorModel }

// Bounds 返回图片范围，不解码像素
// 返回: image.Rectangle 图片范围
func (img *EncodedImage) Bounds() image.Rectangle {
	return image.Rect(0, 0, img.config.Width, img.config.Height)
}

// Image 首次解码图片并缓存结果或错误，支持并发读取
// 返回: image.Image 只读像素, error 解码错误
func (img *EncodedImage) Image() (image.Image, error) {
	img.once.Do(func() { img.decoded, _, img.err = image.Decode(bytes.NewReader(img.data)) })
	return img.decoded, img.err
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

// ImageResource 按资源标识读取只读图片，PNG和JPEG保留原始编码并惰性解码
// 应先读取源页面以加载页面资源，返回EncodedImage时通过Image检查像素解码错误
// 入参: id 图片资源标识
// 返回: image.Image 图片资源, error 读取错误
func (r *Renderer) ImageResource(id string) (image.Image, error) {
	path, ok := r.Reader.ResMap[id]
	if !ok {
		return nil, fmt.Errorf("image resource %q not found", id)
	}
	return r.cachedImageResource(path)
}

// cachedImageResource 复用原始图片资源，不依赖输出后端
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取错误
func (r *Renderer) cachedImageResource(resPath string) (image.Image, error) {
	resPath = cleanPackagePath(r.Reader.ResPath(resPath))
	img, ok := r.imageCache[resPath]
	if !ok {
		var err error
		img, err = r.readImageResource(resPath)
		if err != nil {
			return nil, err
		}
		if r.imageCache == nil {
			r.imageCache = make(map[string]image.Image)
		}
		r.imageCache[resPath] = img
	}
	return img, nil
}

// decodeImageResource 读取或复用图片资源，按输出需要解码像素
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取或解码错误
func (r *Renderer) decodeImageResource(resPath string) (image.Image, error) {
	img, err := r.cachedImageResource(resPath)
	if err != nil {
		return nil, err
	}
	if r.decodeImages {
		if source, ok := img.(*EncodedImage); ok {
			return source.Image()
		}
	}
	return img, nil
}

// readImageResource 读取图片资源并保留可直接嵌入的原始编码
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取或解码错误
func (r *Renderer) readImageResource(resPath string) (image.Image, error) {
	rc, err := r.Reader.openFile(resPath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	img, _, err := decodeImageData(data)
	return img, err
}
