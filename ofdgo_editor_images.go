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
	"fmt"
	"image"
	"math"
)

// ImagePageSource 图片页面来源，按列表顺序逐张读取，每张图片处理完成后不再引用返回数据
// Read不可重入修改编辑器，取消时应返回上下文错误
type ImagePageSource struct {
	Name string
	Read func(context.Context) ([]byte, error)
}

// ImagePageOptions 图片建页选项，尺寸和边距单位为毫米
// Width和Height均为零时随图建页，否则按纸张等比居中，不裁剪图片
// DPI为缺少物理尺寸时的分辨率，零值使用96；图片方向和有效密度优先采用原图信息
// OnProgress按images、commit阶段回报进度，返回错误则撤销整个操作，不可重入修改编辑器
type ImagePageOptions struct {
	Width, Height float64
	Margin        float64
	DPI           float64
	OnProgress    func(stage string, completed, total int) error
}

// ImportImages 将PNG、JPEG或JBIG2逐张建页并插入指定位置，一次操作计入一条撤销记录
// 保留像素及透明度，方向通过对象变换实现，不重新采样；图片压缩沿用AddImage规则
// 失败或取消时不修改文档，读取期间仍需容纳单张图片的解码数据
// 入参: ctx 取消上下文, sources 按页面顺序排列的图片, at 插入位置, options 建页选项
// 返回: error 错误信息
func (e *Editor) ImportImages(ctx context.Context, sources []ImagePageSource, at int, options ImagePageOptions) error {
	if at < 0 || at > len(e.pages) {
		return fmt.Errorf("page index %d out of range", at)
	}
	if !finite(options.Width) || !finite(options.Height) || !finite(options.Margin) || !finite(options.DPI) || options.Margin < 0 || options.DPI < 0 {
		return fmt.Errorf("invalid image page options")
	}
	if (options.Width != 0 || options.Height != 0) && (options.Width <= 2*options.Margin || options.Height <= 2*options.Margin) {
		return fmt.Errorf("page margins leave no image area")
	}
	if options.DPI == 0 {
		options.DPI = 96
	}
	for _, source := range sources {
		if source.Read == nil {
			return fmt.Errorf("image %q has no reader", source.Name)
		}
	}
	check := func(stage string, completed int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.OnProgress != nil {
			if err := options.OnProgress(stage, completed, len(sources)); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	if err := check("images", 0); err != nil || len(sources) == 0 {
		return err
	}
	return e.Transaction(func(e *Editor) error {
		indexes := make([]int, 0, len(sources))
		for i, source := range sources {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := source.Read(ctx)
			if err != nil {
				return fmt.Errorf("image %q: %w", source.Name, err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			page, err := e.addImagePage(data, options)
			if err != nil {
				return fmt.Errorf("image %q: %w", source.Name, err)
			}
			indexes = append(indexes, page)
			if err := check("images", i+1); err != nil {
				return err
			}
		}
		if err := e.MovePages(indexes, at); err != nil {
			return err
		}
		return check("commit", len(sources))
	})
}

// addImagePage 保留原图并按方向、物理尺寸与纸张选项建页
// 入参: data 图片数据, options 建页选项
// 返回: int 从0开始的页面索引, error 错误信息
func (e *Editor) addImagePage(data []byte, options ImagePageOptions) (int, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	if format != "png" && format != "jpeg" && format != "jbig2" {
		return 0, fmt.Errorf("only PNG, JPEG and JBIG2 images are supported")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	metadata, err := imagePageMetadata(data, format, options.DPI)
	if err != nil {
		return 0, err
	}
	w, h := float64(config.Width)*25.4/metadata.xdpi, float64(config.Height)*25.4/metadata.ydpi
	if metadata.orientation >= 5 {
		w, h = h, w
	}
	pw, ph := options.Width, options.Height
	if pw == 0 && ph == 0 {
		pw, ph = w+2*options.Margin, h+2*options.Margin
	} else {
		scale := math.Min((pw-2*options.Margin)/w, (ph-2*options.Margin)/h)
		w, h = w*scale, h*scale
	}
	if !finite(w) || !finite(h) || w <= 0 || h <= 0 {
		return 0, fmt.Errorf("invalid image page dimensions")
	}
	resource, err := e.addImage(data, config, format, decoded)
	if err != nil {
		return 0, err
	}
	page, err := e.AddPage(pw, ph)
	if err != nil {
		return 0, err
	}
	matrix := imagePageOrientation(metadata.orientation, w, h)
	_, err = e.AddObject(page, GraphicObject{Type: "ImageObject", ImageObject: ImageObject{
		ResourceID: resource,
		Boundary:   fmt.Sprintf("%s %s %s %s", ofdNumber((pw-w)/2), ofdNumber((ph-h)/2), ofdNumber(w), ofdNumber(h)),
		CTM:        matrix.String(),
	}})
	return page, err
}

// imagePageOrientation 将EXIF方向映射为单位图像到页面局部坐标的变换
// 入参: orientation EXIF方向值, w、h 图片在页面上的宽高，单位为毫米
// 返回: Matrix 图像坐标变换
func imagePageOrientation(orientation int, w, h float64) Matrix {
	switch orientation {
	case 2:
		return Matrix{a: -w, d: h, e: w}
	case 3:
		return Matrix{a: -w, d: -h, e: w, f: h}
	case 4:
		return Matrix{a: w, d: -h, f: h}
	case 5:
		return Matrix{b: h, c: w}
	case 6:
		return Matrix{b: h, c: -w, e: w}
	case 7:
		return Matrix{b: -h, c: -w, e: w, f: h}
	case 8:
		return Matrix{b: -h, c: w, f: h}
	default:
		return Matrix{a: w, d: h}
	}
}
