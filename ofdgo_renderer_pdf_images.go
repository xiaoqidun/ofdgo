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

	"github.com/xiaoqidun/pdfgo"
)

// preserveImages 按实际绘制顺序恢复颜色及透明度精度，不依赖后端资源命名或对象编号
// 入参: ctx 取消上下文, data 后端PDF数据
// 返回: []byte 保留图像精度的PDF数据, error 资源或写入错误
func (r *pdfRenderer) preserveImages(ctx context.Context, data []byte) ([]byte, error) {
	if !r.exactImages {
		return data, nil
	}
	reader, err := pdfgo.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	images := make(map[pdfgo.Reference]image.Image)
	pages := 0
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		if index >= len(r.images) {
			return fmt.Errorf("unexpected PDF output page")
		}
		pages++
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
		if err := pdfgo.WalkOperations(ctx, content.Bytes(), func(operation pdfgo.Operation) error {
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
		if position != len(r.images[index]) {
			return fmt.Errorf("PDF output image missing")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if pages != len(r.images) {
		return nil, fmt.Errorf("PDF output page missing")
	}
	var result bytes.Buffer
	if _, err := reader.ReplaceImagesTo(ctx, &result, images); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}
