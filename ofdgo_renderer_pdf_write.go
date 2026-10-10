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
	"io"

	"github.com/xiaoqidun/pdfgo"
)

// writePDF 合并图像、文字、导航修正及压缩，直接写出最终PDF
// 入参: ctx 取消上下文, data 后端PDF数据, writer 输出流, optimization 优化配置
// 返回: error 资源、压缩或写入错误
func (r *pdfRenderer) writePDF(ctx context.Context, data []byte, writer io.Writer, optimization pdfgo.OptimizeOptions) error {
	if r.imageError != nil {
		return r.imageError
	}
	nativeWrite := r.navigation != nil && r.navigation.nativeWrite
	if !r.exactImages && !r.exactText && !nativeWrite && optimization.Compression.Mode == CompressionUnchanged {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := writer.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}
	reader, err := pdfgo.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	defer reader.Close()
	if !r.exactImages && !r.exactText && !nativeWrite {
		_, err := reader.OptimizeTo(ctx, writer, optimization)
		return err
	}
	images := make(map[pdfgo.Reference]image.Image)
	links := make(map[pdfgo.AnnotationLocation]pdfgo.LinkRegion)
	texts := make(map[pdfgo.Reference][]pdfgo.TextReplacement)
	references := make([]pdfgo.Reference, len(r.images))
	pages := 0
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		if index >= len(r.images) {
			return fmt.Errorf("unexpected PDF output page")
		}
		pages++
		references[index] = page.Reference
		if nativeWrite {
			annotations, err := page.AnnotationsContext(ctx)
			if err != nil {
				return err
			}
			expected := r.navigation.Link[index]
			if len(annotations) != len(expected) {
				return fmt.Errorf("PDF output link count differs")
			}
			for i, link := range expected {
				if annotations[i].Subtype != pdfgo.Name("Link") {
					return fmt.Errorf("unexpected PDF output annotation")
				}
				if link.Region != nil {
					links[pdfgo.AnnotationLocation{Page: page.Reference, Index: i}] = *link.Region
				}
			}
		}
		if !r.exactImages && !r.exactText {
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
			if !r.exactImages {
				return nil
			}
			if operation.Operator != "Do" {
				return nil
			}
			if len(operation.Operands) != 1 || position >= len(r.images[index]) {
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
			original := r.images[index][position]
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
		if r.exactImages && position != len(r.images[index]) {
			return fmt.Errorf("PDF output image missing")
		}
		if r.exactText {
			if index >= len(r.text) || textCount != r.text[index].count {
				return fmt.Errorf("PDF output text count differs")
			}
			if len(r.text[index].replacements) != 0 {
				texts[page.Reference] = r.text[index].replacements
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if pages != len(r.images) {
		return fmt.Errorf("PDF output page missing")
	}
	options := pdfgo.RewriteOptions{Images: images, ImageOptions: pdfgo.ImageWriteOptions{PreblendBinary: true}, LinkRegions: links, TextReplacements: texts, Optimization: optimization}
	if nativeWrite {
		if err := r.navigation.prepareRewrite(ctx, reader, references, &options); err != nil {
			return err
		}
	}
	for ref, original := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		pixels, err := imagePixelData(original)
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
