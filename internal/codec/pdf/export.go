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

package pdf

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"sync"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// TextPage 保存后端文字对象数量及待替换区段
type TextPage struct {
	Count        int
	Replacements []pdfgo.TextReplacement
}

// WriteOptions 保存PDF最终写出所需的元数据、像素、文字和导航修正
// Images逐页记录绘制图像，nil图像表示不替换；Links保留全部链接位置，包括矩形区域的nil项
// ImageClips按页和图像绘制顺序提供单位方形坐标中的裁剪路径，空字符串表示不裁剪
// PrepareRewrite在页面引用确定后填充导航动作，不得提前写出PDF
type WriteOptions struct {
	Information    pdfgo.Dictionary
	Images         [][]image.Image
	ImageClips     [][]string
	Text           []TextPage
	Links          map[int][]*pdfgo.LinkRegion
	ExactImages    bool
	ExactText      bool
	Optimization   pdfgo.OptimizeOptions
	PrepareRewrite func(context.Context, *pdfgo.Reader, []pdfgo.Reference, *pdfgo.RewriteOptions) error
}

// Write 合并图像、文字、导航修正及压缩，直接写出最终PDF
// 入参: ctx 取消上下文, data 后端PDF数据, writer 输出流, settings 写出配置
// 返回: error 资源、压缩或写入错误
func Write(ctx context.Context, data []byte, writer io.Writer, settings WriteOptions) error {
	return WriteSource(ctx, bytes.NewReader(data), int64(len(data)), writer, settings)
}

// WriteSource 从随机读取源完成资源修正及压缩，避免合并中间PDF缓冲
// 入参: ctx 取消上下文, source 后端PDF, size 数据长度, writer 输出流, settings 写出配置
// 返回: error 读取、资源、压缩或写入错误
func WriteSource(ctx context.Context, source io.ReaderAt, size int64, writer io.Writer, settings WriteOptions) error {
	nativeWrite := settings.PrepareRewrite != nil
	if !settings.ExactImages && !settings.ExactText && len(settings.ImageClips) == 0 && !nativeWrite && len(settings.Information) == 0 && settings.Optimization.Compression.Mode == pdfgo.CompressionUnchanged {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.Copy(outputWriter{ctx: ctx, writer: writer}, io.NewSectionReader(source, 0, size))
		if err == nil && n != size {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	reader, err := pdfgo.NewReader(source, size)
	if err != nil {
		return err
	}
	defer reader.Close()
	if !settings.ExactImages && !settings.ExactText && len(settings.ImageClips) == 0 && !nativeWrite {
		if len(settings.Information) != 0 {
			_, err := reader.RewriteTo(ctx, writer, pdfgo.RewriteOptions{Information: settings.Information, Optimization: settings.Optimization})
			return err
		}
		_, err := reader.OptimizeTo(ctx, writer, settings.Optimization)
		return err
	}
	images := make(map[pdfgo.Reference]image.Image)
	links := make(map[pdfgo.AnnotationLocation]pdfgo.LinkRegion)
	texts := make(map[pdfgo.Reference][]pdfgo.TextReplacement)
	references := make([]pdfgo.Reference, len(settings.Images))
	pages := 0
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		if index >= len(settings.Images) {
			return fmt.Errorf("unexpected PDF output page")
		}
		pages++
		references[index] = page.Reference
		if nativeWrite {
			annotations, err := page.AnnotationsContext(ctx)
			if err != nil {
				return err
			}
			expected := settings.Links[index]
			if len(annotations) != len(expected) {
				return fmt.Errorf("PDF output link count differs")
			}
			for i, link := range expected {
				if annotations[i].Subtype != pdfgo.Name("Link") {
					return fmt.Errorf("unexpected PDF output annotation")
				}
				if link != nil {
					links[pdfgo.AnnotationLocation{Page: page.Reference, Index: i}] = *link
				}
			}
		}
		if !settings.ExactImages && !settings.ExactText {
			return nil
		}
		value, err := reader.Resolve(page.Resources["XObject"])
		if err != nil {
			return err
		}
		resources, _ := value.(pdfgo.Dictionary)
		var content bytes.Buffer
		if _, err := page.WriteContent(ctx, &content); err != nil {
			return err
		}
		position := 0
		textCount := 0
		if err := pdfgo.WalkOperations(ctx, content.Bytes(), func(operation pdfgo.Operation) error {
			if operation.Operator == "BT" {
				textCount++
			}
			if !settings.ExactImages {
				return nil
			}
			if operation.Operator != "Do" {
				return nil
			}
			if len(operation.Operands) != 1 || position >= len(settings.Images[index]) {
				return fmt.Errorf("unexpected PDF output image")
			}
			name, ok := operation.Operands[0].(pdfgo.Name)
			if !ok {
				return fmt.Errorf("invalid PDF output image name")
			}
			ref, ok := resources[name].(pdfgo.Reference)
			if !ok {
				return fmt.Errorf("invalid PDF output image reference")
			}
			original := settings.Images[index][position]
			position++
			if original == nil {
				return nil
			}
			image, err := reader.ReadImage(ref)
			if err != nil {
				return err
			}
			if image.Width != original.Bounds().Dx() || image.Height != original.Bounds().Dy() {
				return fmt.Errorf("PDF output image dimensions differ")
			}
			if previous := images[ref]; previous != nil && previous != original {
				return fmt.Errorf("PDF output image reference conflicts")
			}
			images[ref] = original
			return nil
		}); err != nil {
			return err
		}
		if settings.ExactImages && position != len(settings.Images[index]) {
			return fmt.Errorf("PDF output image missing")
		}
		if settings.ExactText {
			if index >= len(settings.Text) || textCount != settings.Text[index].Count {
				return fmt.Errorf("PDF output text count differs")
			}
			if len(settings.Text[index].Replacements) != 0 {
				texts[page.Reference] = settings.Text[index].Replacements
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if pages != len(settings.Images) {
		return fmt.Errorf("PDF output page missing")
	}
	decoder := &exportImageDecoder{ctx: ctx}
	defer decoder.clear()
	options := pdfgo.RewriteOptions{Information: settings.Information, Images: images, ImageOptions: pdfgo.ImageWriteOptions{PreblendBinary: true}, LinkRegions: links, TextReplacements: texts, Optimization: settings.Optimization}
	pageClips := make(map[pdfgo.Reference][]string, len(references))
	for i, ref := range references {
		if i < len(settings.ImageClips) {
			pageClips[ref] = settings.ImageClips[i]
		}
	}
	options.TransformContent = func(ctx context.Context, page *pdfgo.Page, data []byte) ([]byte, error) {
		decoder.clear()
		clipped, err := clipPageImages(ctx, data, pageClips[page.Reference])
		if err != nil {
			return nil, err
		}
		if clipped != nil {
			data = clipped
		}
		compact, err := pdfgo.CompactText(ctx, data)
		if err != nil {
			return nil, err
		}
		if compact != nil {
			return compact, nil
		}
		return clipped, nil
	}
	if nativeWrite {
		if err := settings.PrepareRewrite(ctx, reader, references, &options); err != nil {
			return err
		}
	}
	for ref, original := range options.Images {
		if err := ctx.Err(); err != nil {
			return err
		}
		if original == nil {
			return fmt.Errorf("PDF output image pixel bounds differ")
		}
		options.Images[ref] = &lazyExportImage{decoder: decoder, source: original}
	}
	_, err = reader.RewriteTo(ctx, writer, options)
	decoder.clear()
	if decodeErr := decoder.failure(); decodeErr != nil {
		return decodeErr
	}
	return err
}

// outputWriter 在分块输出前后检查取消，不改变底层写入错误
type outputWriter struct {
	ctx    context.Context
	writer io.Writer
}

// Write 写入当前数据块并回报取消或写入错误
// 入参: data 输出数据
// 返回: int 写入字节数, error 取消或写入错误
func (w outputWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(data)
	if err == nil {
		err = w.ctx.Err()
	}
	return n, err
}

// exportImageDecoder 控制PDF写出期间的单槽懒解码，确保同时最多驻留单张图像像素
type exportImageDecoder struct {
	mu      sync.Mutex
	ctx     context.Context
	current *lazyExportImage
	pixels  image.Image
	rgba64  image.RGBA64Image
	nrgba   *image.NRGBA
	nrgba64 interface{ NRGBA64At(int, int) color.NRGBA64 }
	err     error
}

// load 按需解码目标图像并立即丢弃上一张图像的未压缩像素缓冲
// 入参: target 待访问的懒加载图像代理
// 返回: image.Image 已解码图像, image.RGBA64Image 预乘快路径, *image.NRGBA 八位快路径, interface{ NRGBA64At(int, int) color.NRGBA64 } 十六位快路径
func (d *exportImageDecoder) load(target *lazyExportImage) (image.Image, image.RGBA64Image, *image.NRGBA, interface{ NRGBA64At(int, int) color.NRGBA64 }) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, nil, nil, nil
	}
	if d.current == target && d.pixels != nil {
		return d.pixels, d.rgba64, d.nrgba, d.nrgba64
	}
	d.reset()
	pixels, err := decodeExportImage(d.ctx, target.source)
	if err != nil {
		d.err = err
		return nil, nil, nil, nil
	}
	d.current = target
	d.pixels = pixels
	d.rgba64, _ = pixels.(image.RGBA64Image)
	d.nrgba, _ = pixels.(*image.NRGBA)
	d.nrgba64, _ = pixels.(interface{ NRGBA64At(int, int) color.NRGBA64 })
	return d.pixels, d.rgba64, d.nrgba, d.nrgba64
}

// clear 释放当前槽位驻留的未压缩图像像素引用
func (d *exportImageDecoder) clear() {
	d.mu.Lock()
	d.reset()
	d.mu.Unlock()
}

// reset 清空当前槽位状态，调用方需持有互斥锁
func (d *exportImageDecoder) reset() {
	d.current = nil
	d.pixels = nil
	d.rgba64 = nil
	d.nrgba = nil
	d.nrgba64 = nil
}

// failure 读取解码阶段记录的首个错误
// 返回: error 解码错误
func (d *exportImageDecoder) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// lazyExportImage 延迟解码待替换PDF图像，编码完成后随槽位切换自动释放
type lazyExportImage struct {
	decoder *exportImageDecoder
	source  image.Image
}

// Bounds 触发单张按需解码并返回像素范围，解码失败时返回空范围以中断编码
// 返回: image.Rectangle 图像像素范围
func (l *lazyExportImage) Bounds() image.Rectangle {
	pixels, _, _, _ := l.decoder.load(l)
	if pixels == nil {
		return image.Rectangle{}
	}
	return pixels.Bounds()
}

// ColorModel 返回当前已解码图像的颜色模型
// 返回: color.Model 颜色模型
func (l *lazyExportImage) ColorModel() color.Model {
	pixels, _, _, _ := l.decoder.load(l)
	if pixels == nil {
		return color.NRGBAModel
	}
	return pixels.ColorModel()
}

// At 返回指定坐标处的原始颜色样本
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 像素颜色
func (l *lazyExportImage) At(x, y int) color.Color {
	pixels, _, _, _ := l.decoder.load(l)
	if pixels == nil {
		return color.Transparent
	}
	return pixels.At(x, y)
}

// RGBA64At 返回指定坐标处的十六位预乘颜色样本
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 十六位预乘颜色
func (l *lazyExportImage) RGBA64At(x, y int) color.RGBA64 {
	pixels, rgba64, _, _ := l.decoder.load(l)
	if pixels == nil {
		return color.RGBA64{}
	}
	if rgba64 != nil {
		return rgba64.RGBA64At(x, y)
	}
	r, g, b, a := pixels.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}

// NRGBA64At 返回指定坐标处的十六位非预乘颜色样本，常见像素切片走无装箱快路径
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.NRGBA64 十六位非预乘颜色
func (l *lazyExportImage) NRGBA64At(x, y int) color.NRGBA64 {
	pixels, _, nrgba, nrgba64 := l.decoder.load(l)
	if pixels == nil {
		return color.NRGBA64{}
	}
	if nrgba != nil {
		if !(image.Point{X: x, Y: y}).In(nrgba.Rect) {
			return color.NRGBA64{}
		}
		i := nrgba.PixOffset(x, y)
		s := nrgba.Pix[i : i+4 : i+4]
		return color.NRGBA64{R: uint16(s[0]) * 257, G: uint16(s[1]) * 257, B: uint16(s[2]) * 257, A: uint16(s[3]) * 257}
	}
	if nrgba64 != nil {
		return nrgba64.NRGBA64At(x, y)
	}
	return baseimage.NRGBA64At(pixels, x, y)
}

// clipPageImages 在图像局部坐标中应用独立裁剪，不解码或重新采样图像
// 入参: ctx 取消上下文, data 页面指令, clips 各次图像绘制的局部裁剪路径
// 返回: []byte 更新指令，未变化时为nil, error 解析或取消错误
func clipPageImages(ctx context.Context, data []byte, clips []string) ([]byte, error) {
	if len(clips) == 0 {
		return nil, ctx.Err()
	}
	var output bytes.Buffer
	position, previous, index := 0, 0, 0
	err := pdfgo.WalkOperations(ctx, data, func(op pdfgo.Operation) error {
		end := int(op.Offset) + len(op.Operator)
		if op.Operator == "Do" {
			if index < len(clips) && clips[index] != "" {
				output.Write(data[position:previous])
				output.WriteString(" q ")
				output.WriteString(clips[index])
				output.WriteString(" W n ")
				output.Write(data[previous:end])
				output.WriteString(" Q ")
				position = end
			}
			index++
		}
		previous = end
		return nil
	})
	if err != nil {
		return nil, err
	}
	if position == 0 {
		return nil, ctx.Err()
	}
	output.Write(data[position:])
	return output.Bytes(), ctx.Err()
}

// decodeExportImage 单次临时解码单张待导出图像并校验范围，不在源惰性对象上保留像素缓存
// 入参: ctx 取消上下文, original 源图像或惰性图像
// 返回: image.Image 解码像素, error 解码、范围或取消错误
func decodeExportImage(ctx context.Context, original image.Image) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pixels, err := baseimage.DecodeTransientContext(ctx, original)
	if err != nil {
		return nil, err
	}
	if pixels == nil || pixels.Bounds() != original.Bounds() {
		return nil, fmt.Errorf("PDF output image pixel bounds differ")
	}
	return pixels, nil
}
