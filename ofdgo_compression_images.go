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
)

// compressionImages 记录图片各处使用的最大像素需求
type compressionImages struct {
	ctx   context.Context
	dpi   int
	sizes map[string]image.Point
}

// compressionImagePlan 分文档分析标识，按共享路径合并最大尺寸和保护条件
// 入参: ctx 取消上下文, options 压缩配置
// 返回: map[string]bool 可有损处理的路径, map[string]image.Point 像素需求, error 读取或取消错误
func (r *Reader) compressionImagePlan(ctx context.Context, options CompressionOptions) (map[string]bool, map[string]image.Point, error) {
	images := make(map[string]bool)
	sizes := make(map[string]image.Point)
	blocked := make(map[string]bool)
	for index := 0; index < r.DocumentCount(); index++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		document := r
		if index != r.DocumentIndex() {
			var err error
			document, err = r.Document(index)
			if err != nil {
				return nil, nil, err
			}
		}
		catalog, err := document.Images(ctx)
		if err != nil {
			if document != r {
				document.Close()
			}
			return nil, nil, err
		}
		var masks map[string]bool
		var demands map[string]image.Point
		if options.Mode == CompressionLossy {
			var geometry bool
			masks, geometry, err = document.compressionImageSafety(ctx, catalog)
			if err == nil && geometry {
				demands, err = document.compressionImageSizes(ctx, catalog, options.ImageDPI())
			}
		}
		if document != r {
			document.Close()
		}
		if err != nil {
			return nil, nil, err
		}
		for _, img := range catalog {
			name := cleanPackagePath(img.Location)
			if old, ok := images[name]; !ok || old {
				images[name] = !masks[name]
			}
			size := demands[name]
			if size.X <= 0 || size.Y <= 0 || masks[name] {
				blocked[name] = true
				delete(sizes, name)
				continue
			}
			if !blocked[name] {
				old := sizes[name]
				sizes[name] = image.Pt(max(old.X, size.X), max(old.Y, size.Y))
			}
		}
	}
	return images, sizes, ctx.Err()
}

// compressionImageSize 将毫米显示尺寸换算为像素，非法尺寸不降采样
// 整像素边界消除单位换算产生的单ULP上溢，不降低实际像素需求
// 入参: width 显示宽度, height 显示高度, dpi 分辨率上限
// 返回: image.Point 像素需求
func compressionImageSize(width, height float64, dpi int) image.Point {
	w := math.Ceil(math.Nextafter(width*float64(dpi)/25.4, math.Inf(-1)))
	h := math.Ceil(math.Nextafter(height*float64(dpi)/25.4, math.Inf(-1)))
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
	for _, ref := range r.doc.CommonData.TemplatePage {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		template := renderer.loadTemplate(ref.ID)
		if template == nil {
			return nil, ctx.Err()
		}
		for order := range 3 {
			if err := renderer.walkLayers(template.Content.Layer, order, visitor); err != nil {
				return nil, ctx.Err()
			}
		}
	}
	result := make(map[string]image.Point)
	for _, img := range images {
		name := cleanPackagePath(img.Location)
		size := visitor.sizes[editorResourceID(img.ID)]
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
	id := editorResourceID(img.ResourceID)
	if id == "" {
		return fmt.Errorf("invalid image resource")
	}
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
	old := v.sizes[id]
	v.sizes[id] = image.Pt(max(old.X, size.X), max(old.Y, size.Y))
	return nil
}

// DrawStamp 保留签章内容，不参与图片降采样
// 入参: stamp 印章
// 返回: error 始终为空
func (v *compressionImages) DrawStamp(stamp Stamp) error { return nil }
