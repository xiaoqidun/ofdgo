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
	"crypto/sha256"
	"fmt"
	"image"
	"io"
	"math"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
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

// editorImagePageInfo 保存同批次已校验图片的建页信息，不持有编码、像素或颜色模型
type editorImagePageInfo struct {
	size     image.Point
	format   string
	metadata baseimage.PageInfo
}

// ImagePreviewInfo 保存图片按方向和密度换算后的毫米尺寸
type ImagePreviewInfo struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// RenderImagePreview 输出最长边不超过size像素的PNG预览，并返回建页物理尺寸
// 使用原图方向和密度，缺少密度时使用96DPI，不修改源图片
// 入参: ctx 取消上下文, data 图片数据, writer 预览输出流, size 最长边像素数
// 返回: ImagePreviewInfo 物理尺寸, error 解码、取消或输出错误
func RenderImagePreview(ctx context.Context, data []byte, writer io.Writer, size int) (ImagePreviewInfo, error) {
	if size <= 0 {
		return ImagePreviewInfo{}, fmt.Errorf("invalid preview size")
	}
	config, format, err := baseimage.DecodeConfigDataContext(ctx, data)
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	metadata, err := baseimage.PageMetadataContext(ctx, data, format, 96)
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	info := ImagePreviewInfo{Width: float64(config.Width) * 25.4 / metadata.XDPI, Height: float64(config.Height) * 25.4 / metadata.YDPI}
	pixels, _, err := baseimage.DecodePixelsContext(ctx, data)
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	scale := min(1, float64(size)/float64(max(config.Width, config.Height)))
	pixels, err = pdfgo.ResizeImage(ctx, pixels, image.Pt(max(1, int(float64(config.Width)*scale)), max(1, int(float64(config.Height)*scale))))
	if err != nil {
		return ImagePreviewInfo{}, err
	}
	bounds := pixels.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	width, height := w, h
	if metadata.Orientation >= 5 {
		width, height = h, w
		info.Width, info.Height = info.Height, info.Width
	}
	preview := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range h {
		if err := ctx.Err(); err != nil {
			return ImagePreviewInfo{}, err
		}
		for x := range w {
			dx, dy := x, y
			switch metadata.Orientation {
			case 2:
				dx = w - 1 - x
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dy = h - 1 - y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			preview.Set(dx, dy, pixels.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return info, pdfgo.EncodePNG(ctx, writer, preview)
}

// ImportImages 将PNG、JPEG、BMP、单页TIFF或JBIG2逐张建页并插入指定位置，一次操作计入一条撤销记录
// 保留像素及透明度，方向通过对象变换实现，不重新采样；图片压缩沿用AddImage规则
// 同一批次的相同图片复用解码校验及元数据，不额外保留像素数据
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
		verified := make(map[[32]byte]editorImagePageInfo)
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
			page, err := e.addImagePage(ctx, data, options, verified)
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
// 入参: ctx 取消上下文, data 图片数据, options 建页选项, verified 本批次已校验图片的建页信息
// 返回: int 从0开始的页面索引, error 错误信息
func (e *Editor) addImagePage(ctx context.Context, data []byte, options ImagePageOptions, verified map[[32]byte]editorImagePageInfo) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var decoded image.Image
	checksum := sha256.Sum256(data)
	info, known := verified[checksum]
	if !known {
		config, format, err := baseimage.DecodeConfigDataContext(ctx, data)
		if err != nil {
			return 0, err
		}
		if format != "png" && format != "jpeg" && format != "bmp" && format != "tiff" && format != "jbig2" {
			return 0, fmt.Errorf("only PNG, JPEG, BMP, single-page TIFF and JBIG2 images are supported")
		}
		decoded, _, err = baseimage.DecodePixelsContext(ctx, data)
		if err != nil {
			return 0, err
		}
		metadata, err := baseimage.PageMetadataContext(ctx, data, format, options.DPI)
		if err != nil {
			return 0, err
		}
		info = editorImagePageInfo{size: image.Pt(config.Width, config.Height), format: format, metadata: metadata}
		verified[checksum] = info
	}
	metadata := info.metadata
	w, h := float64(info.size.X)*25.4/metadata.XDPI, float64(info.size.Y)*25.4/metadata.YDPI
	if metadata.Orientation >= 5 {
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
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	resource, err := e.addImage(ctx, data, info.size, info.format, decoded, checksum)
	if err != nil {
		return 0, err
	}
	page, err := e.AddPage(pw, ph)
	if err != nil {
		return 0, err
	}
	matrix := imagePageOrientation(metadata.Orientation, w, h)
	_, err = e.AddObject(page, GraphicObject{Type: "ImageObject", ImageObject: ImageObject{
		ResourceID: resource,
		Boundary:   fmt.Sprintf("%s %s %s %s", ofdNumber((pw-w)/2), ofdNumber((ph-h)/2), ofdNumber(w), ofdNumber(h)),
		CTM:        matrix.String(),
	}})
	return page, err
}

// imagePageOrientation 将EXIF方向映射为单位图像到页面局部坐标的变换
// 入参: orientation EXIF方向值, w 页面上的图片宽度，单位为毫米, h 页面上的图片高度，单位为毫米
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
