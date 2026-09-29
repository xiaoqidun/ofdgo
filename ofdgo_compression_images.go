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
	"math"
	"strings"
)

// compressionImages 记录图片各处使用的最大像素需求
type compressionImages struct {
	ctx   context.Context
	dpi   int
	sizes map[string]image.Point
}

// compressionImageSize 将毫米显示尺寸换算为像素，非法尺寸不降采样
// 入参: width 显示宽度, height 显示高度, dpi 分辨率上限
// 返回: image.Point 像素需求
func compressionImageSize(width, height float64, dpi int) image.Point {
	w, h := math.Ceil(width*float64(dpi)/25.4), math.Ceil(height*float64(dpi)/25.4)
	if w <= 0 || h <= 0 || math.IsNaN(w) || math.IsNaN(h) || math.IsInf(w, 0) || math.IsInf(h, 0) || w > 1<<30 || h > 1<<30 {
		return image.Point{}
	}
	return image.Pt(int(w), int(h))
}

// compressionImageSizes 按正文、模板及注解的实际变换汇总共享图片尺寸
// 入参: ctx 取消上下文, images 图片资源, dpi 分辨率上限
// 返回: map[string]image.Point 资源像素需求, error 取消错误
func (r *Reader) compressionImageSizes(ctx context.Context, images []ImageInfo, dpi int) (map[string]image.Point, error) {
	if dpi == 0 {
		return nil, nil
	}
	count, err := r.PageCount()
	if err != nil {
		return nil, ctx.Err()
	}
	renderer := NewRenderer(r)
	defer renderer.ClearCache()
	visitor := &compressionImages{ctx: ctx, dpi: dpi, sizes: make(map[string]image.Point)}
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := r.PageContentByIndex(index)
		if err != nil {
			return nil, ctx.Err()
		}
		for _, ref := range page.Template {
			if renderer.loadTemplate(ref.TemplateID) == nil {
				return nil, ctx.Err()
			}
		}
		if err := renderer.WalkPage(page, visitor); err != nil {
			return nil, ctx.Err()
		}
	}
	result := make(map[string]image.Point)
	for _, img := range images {
		name := strings.ToLower(cleanPackagePath(img.Location))
		size := visitor.sizes[img.ID]
		old := result[name]
		result[name] = image.Pt(max(old.X, size.X), max(old.Y, size.Y))
	}
	return result, ctx.Err()
}

// DrawObject 收集图片的完整显示尺寸，不使用裁剪后的局部面积
// 入参: object 图形对象, state 继承状态
// 返回: error 取消或几何错误
func (v *compressionImages) DrawObject(object *GraphicObject, state RenderState) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	if object.Type != "ImageObject" {
		return nil
	}
	img := object.ImageObject
	box, err := ParseBox(img.Boundary)
	if err != nil {
		return err
	}
	local := NewMatrix(img.CTM)
	if img.CTM == "" {
		local = Matrix{a: box.W, d: box.H}
	}
	matrix, _ := renderObjectMatrix(img.Boundary, local, state)
	size := compressionImageSize(math.Hypot(matrix.a, matrix.b), math.Hypot(matrix.c, matrix.d), v.dpi)
	if size.X == 0 || size.Y == 0 {
		return fmt.Errorf("invalid image placement")
	}
	old := v.sizes[img.ResourceID]
	v.sizes[img.ResourceID] = image.Pt(max(old.X, size.X), max(old.Y, size.Y))
	return nil
}

// DrawStamp 保留签章内容，不参与图片降采样
// 入参: stamp 印章
// 返回: error 始终为空
func (v *compressionImages) DrawStamp(stamp Stamp) error { return nil }
