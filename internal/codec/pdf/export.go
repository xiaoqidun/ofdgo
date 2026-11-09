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
	"io"

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
	options := pdfgo.RewriteOptions{Information: settings.Information, Images: images, ImageOptions: pdfgo.ImageWriteOptions{PreblendBinary: true}, LinkRegions: links, TextReplacements: texts, Optimization: settings.Optimization}
	pageClips := make(map[pdfgo.Reference][]string, len(references))
	for i, ref := range references {
		if i < len(settings.ImageClips) {
			pageClips[ref] = settings.ImageClips[i]
		}
	}
	options.TransformContent = func(ctx context.Context, page *pdfgo.Page, data []byte) ([]byte, error) {
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
	for ref, original := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		pixels, err := baseimage.PixelDataContext(ctx, original)
		if err != nil {
			return err
		}
		if pixels == nil || pixels.Bounds() != original.Bounds() {
			return fmt.Errorf("PDF output image pixel bounds differ")
		}
		images[ref] = pixels
	}
	_, err = reader.RewriteTo(ctx, writer, options)
	return err
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
