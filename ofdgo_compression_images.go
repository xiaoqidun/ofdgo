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
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// compressionPatternImageLimit 限制底纹尺寸分析的变换组合数，超限时保留原图尺寸
const compressionPatternImageLimit = 65536

// compressionImages 记录图片各处使用的最大像素需求
type compressionImages struct {
	ctx           context.Context
	dpi           int
	sizes         map[string]image.Point
	renderer      *Renderer
	patternPage   *Matrix
	patterns      map[*Pattern]bool
	patternVisits map[compressionPatternKey]bool
}

// DrawObject 汇总图片、替换图及文字、路径和图像边框底纹的尺寸，不使用裁剪后的局部面积
// 入参: object 图形对象, state 继承状态
// 返回: error 取消或几何错误
func (v *compressionImages) DrawObject(object *GraphicObject, state RenderState) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	switch object.Type {
	case "PathObject":
		return v.objectPatterns(object.PathObject, state)
	case "TextObject":
		text := object.TextObject
		return v.objectPatterns(PathObject{Boundary: text.Boundary, CTM: text.CTM, DrawParam: text.DrawParam, FillColor: text.FillColor, StrokeColor: text.StrokeColor}, state)
	case "ImageObject":
	default:
		return nil
	}
	img := object.ImageObject
	ids := [2]string{editorResourceID(img.ResourceID), editorResourceID(img.Substitution)}
	if ids[0] == "" || img.Substitution != "" && ids[1] == "" {
		return fmt.Errorf("invalid image resource")
	}
	box, err := ParseBox(img.Boundary)
	if err != nil {
		return err
	}
	if border := img.Border; border != nil && border.BorderColor != nil && border.BorderColor.Pattern != nil {
		matrix, _ := renderObjectMatrix(img.Boundary, IdentityMatrix, state)
		if err := v.patternImages((*FillColor)(border.BorderColor), matrix); err != nil {
			return err
		}
	}
	local := NewMatrix(img.CTM)
	if img.CTM == "" {
		local = Matrix{a: box.W, d: box.H}
	}
	matrix, _ := renderObjectMatrix(img.Boundary, local, state)
	size := compressionImageSize(math.Hypot(matrix.a, matrix.b), math.Hypot(matrix.c, matrix.d), v.dpi)
	return v.recordSize(ids, size)
}

// VisitComposite 按矢量资源的完整变换统计缩略图和替换图，不以裁剪区域缩减尺寸
// 入参: object 矢量资源或复合图元, state 已包含当前图元变换的状态
// 返回: error 取消、资源标识或几何错误
func (v *compressionImages) VisitComposite(object *CompositeGraphicUnit, state RenderState) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	if object.Thumbnail == "" && object.Substitution == "" {
		return nil
	}
	ids := [2]string{editorResourceID(object.Thumbnail), editorResourceID(object.Substitution)}
	if object.Thumbnail != "" && ids[0] == "" || object.Substitution != "" && ids[1] == "" {
		return fmt.Errorf("invalid composite image resource")
	}
	matrix := IdentityMatrix
	if state.Parent != nil {
		matrix = *state.Parent
	}
	size := compressionImageSize(object.Width*math.Hypot(matrix.a, matrix.b), object.Height*math.Hypot(matrix.c, matrix.d), v.dpi)
	return v.recordSize(ids, size)
}

// DrawStamp 保留签章内容，不参与图片降采样
// 入参: stamp 印章
// 返回: error 始终为空
func (v *compressionImages) DrawStamp(stamp Stamp) error { return nil }

// IncludeHidden 将隐藏内容纳入尺寸分析，保留图片在所有使用位置的精度
// 返回: bool 始终为真
func (v *compressionImages) IncludeHidden() bool { return true }

// SkipClips 按完整图片尺寸分析，不加载裁剪几何或文字轮廓
// 返回: bool 始终为真
func (v *compressionImages) SkipClips() bool { return true }

// recordSize 按当前资源作用域解析包内媒体路径并合并同一文件的最大像素需求
// 入参: ids 图像标识，空值不统计, size 当前像素需求
// 返回: error 尺寸无效时返回错误
func (v *compressionImages) recordSize(ids [2]string, size image.Point) error {
	if size.X == 0 || size.Y == 0 {
		return fmt.Errorf("invalid image placement")
	}
	r := v.renderer.Reader
	for _, id := range ids {
		if id == "" {
			continue
		}
		rel, ok := r.resolveResPathScoped(v.renderer.pageResources, id)
		if !ok {
			continue
		}
		name := r.ResPath(rel)
		if name == "" || name == "." {
			continue
		}
		old := v.sizes[name]
		v.sizes[name] = image.Pt(max(old.X, size.X), max(old.Y, size.Y))
	}
	return nil
}

// objectPatterns 统计文字和路径的填充及描边底纹，保留绘制参数继承关系
// 入参: object 对象样式及坐标, state 继承状态
// 返回: error 底纹分析错误
func (v *compressionImages) objectPatterns(object PathObject, state RenderState) error {
	fill, stroke := object.FillColor, (*FillColor)(object.StrokeColor)
	if defaults := v.renderer.drawParamDefaults(object.DrawParam, state.Defaults); defaults != nil {
		if fill == nil {
			fill = defaults.FillColor
		}
		if stroke == nil {
			stroke = (*FillColor)(defaults.StrokeColor)
		}
	}
	if (fill == nil || fill.Pattern == nil) && (stroke == nil || stroke.Pattern == nil) {
		return nil
	}
	matrix, _ := renderObjectMatrix(object.Boundary, IdentityMatrix, state)
	for _, color := range [2]*FillColor{fill, stroke} {
		if color != nil && color.Pattern != nil {
			if err := v.patternImages(color, matrix); err != nil {
				return err
			}
		}
	}
	return nil
}

// patternImages 按独立单元坐标汇总底纹图片及缩略图，不展开重复铺排的单元
// 入参: color 底纹颜色, object 对象到页面矩阵
// 返回: error 取消、循环引用或变换错误
func (v *compressionImages) patternImages(color *FillColor, object Matrix) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	pattern := color.Pattern
	if v.patterns[pattern] {
		return fmt.Errorf("cyclic pattern image reference")
	}
	if !finite(pattern.Width) || !finite(pattern.Height) || pattern.Width <= 0 || pattern.Height <= 0 {
		return fmt.Errorf("invalid pattern cell size")
	}
	matrix := NewMatrix(pattern.CTM)
	switch pattern.RelativeTo {
	case "", "Object":
		matrix = object.Multiply(matrix)
	case "Page":
		if v.patternPage != nil {
			matrix = v.patternPage.Multiply(matrix)
		}
	default:
		return fmt.Errorf("invalid pattern coordinate system")
	}
	if !finite(matrix.a) || !finite(matrix.b) || !finite(matrix.c) || !finite(matrix.d) {
		return fmt.Errorf("invalid pattern transform")
	}
	matrix.e, matrix.f = 0, 0
	key := compressionPatternKey{pattern: pattern, linear: [4]float64{matrix.a, matrix.b, matrix.c, matrix.d}}
	if v.patternVisits[key] {
		return nil
	}
	if len(v.patternVisits) >= compressionPatternImageLimit {
		return fmt.Errorf("pattern image analysis limit exceeded")
	}
	if v.patterns == nil {
		v.patterns = make(map[*Pattern]bool)
		v.patternVisits = make(map[compressionPatternKey]bool)
	}
	v.patternVisits[key] = true
	v.patterns[pattern] = true
	defer delete(v.patterns, pattern)
	if pattern.CellContent.Thumbnail != "" {
		id := editorResourceID(pattern.CellContent.Thumbnail)
		if id == "" {
			return fmt.Errorf("invalid pattern thumbnail resource")
		}
		size := compressionImageSize(pattern.Width*math.Hypot(matrix.a, matrix.b), pattern.Height*math.Hypot(matrix.c, matrix.d), v.dpi)
		if err := v.recordSize([2]string{id}, size); err != nil {
			return err
		}
	}
	columns, rows := 1, 1
	switch pattern.ReflectMethod {
	case "", "Normal":
	case "Column":
		columns = 2
	case "Row":
		rows = 2
	case "RowAndColumn":
		columns, rows = 2, 2
	default:
		return fmt.Errorf("invalid pattern reflection")
	}
	parent := v.patternPage
	defer func() { v.patternPage = parent }()
	solid := FillColor{Value: color.Value, Index: color.Index, ColorSpace: color.ColorSpace}
	defaults := &DrawParam{FillColor: &solid, StrokeColor: (*StrokeColor)(&solid)}
	for column := range columns {
		for row := range rows {
			cell := matrix.Multiply(Matrix{a: float64(1 - 2*column), d: float64(1 - 2*row)})
			v.patternPage = &cell
			for member := range pattern.CellContent.objects() {
				if err := v.ctx.Err(); err != nil {
					return err
				}
				if err := v.renderer.WalkObject(&member, RenderState{Defaults: defaults, Parent: &cell, BoundaryInCTM: true}, v); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// compressionPatternKey 复用相同底纹的尺寸分析，平移不影响像素需求
type compressionPatternKey struct {
	pattern *Pattern
	linear  [4]float64
}

// compressionImagePlan 分文档合并图片需求，版本文件的当前入口独立于其他文档处理
// 入参: ctx 取消上下文, options 压缩配置
// 返回: map[string]bool 可有损处理的路径, map[string]image.Point 像素需求, error 读取或取消错误
func (r *Reader) compressionImagePlan(ctx context.Context, options CompressionOptions) (map[string]bool, map[string]image.Point, error) {
	return r.compressionImagePlanProtected(ctx, options, nil, nil)
}

// compressionImagePlanProtected 按签名保护边界分文档合并图片需求，受保护文档和文件不参与有损降采样
// 入参: ctx 取消上下文, options 压缩配置, protectedDocs 受签名保护的文档索引, protectedFiles 受签名保护的文件路径
// 返回: map[string]bool 可有损处理的路径, map[string]image.Point 像素需求, error 读取或取消错误
func (r *Reader) compressionImagePlanProtected(ctx context.Context, options CompressionOptions, protectedDocs map[int]bool, protectedFiles map[string]bool) (map[string]bool, map[string]image.Point, error) {
	if options.Mode != CompressionLossy {
		return nil, nil, ctx.Err()
	}
	images := make(map[string]bool)
	sizes := make(map[string]image.Point)
	blocked := make(map[string]bool)
	isolated := r.OFD.DocBody[r.documentIndex].versioned
	current := make(map[string]bool)
	for offset := 0; offset < r.DocumentCount(); offset++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		index := offset
		if isolated {
			index = (r.documentIndex + offset) % r.DocumentCount()
		}
		if protectedDocs[index] {
			continue
		}
		document := r
		if index != r.DocumentIndex() {
			var err error
			document, err = r.DocumentContext(ctx, index)
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
		if len(catalog) == 0 {
			if document != r {
				document.Close()
			}
			continue
		}
		var demands map[string]image.Point
		masks, geometry, err := document.compressionImageSafety(ctx, catalog)
		if err == nil && geometry {
			demands, err = document.compressionImageSizes(ctx, catalog, options.ImageDPI())
		}
		if document != r {
			document.Close()
		}
		if err != nil {
			return nil, nil, err
		}
		for _, img := range catalog {
			name := cleanPackagePath(img.Location)
			if protectedFiles[name] {
				images[name] = false
				blocked[name] = true
				delete(sizes, name)
				continue
			}
			if isolated {
				if index == r.documentIndex {
					current[name] = true
				} else if current[name] {
					continue
				}
			}
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

// compressionImageSizes 按正文、模板、注解及底纹的实际变换汇总图片尺寸，包括隐藏内容
// 模板使用独立页面坐标，统一分析一次，避免随引用页数重复展开
// 入参: ctx 取消上下文, images 图片资源, dpi 分辨率上限
// 返回: map[string]image.Point 资源像素需求, error 取消错误
func (r *Reader) compressionImageSizes(ctx context.Context, images []ImageInfo, dpi int) (map[string]image.Point, error) {
	if dpi == 0 {
		return nil, nil
	}
	doc, err := r.DocContext(ctx)
	if err != nil {
		return nil, ctx.Err()
	}
	renderer := NewRenderer(r, WithRenderBackends(RenderBackends{}))
	renderer.pageContext = ctx
	defer renderer.ClearCache()
	visitor := &compressionImages{ctx: ctx, dpi: dpi, sizes: make(map[string]image.Point), renderer: renderer}
	for _, ref := range doc.Pages.Page {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := r.PageContentContext(ctx, ref)
		if err != nil {
			return nil, ctx.Err()
		}
		content := *page
		content.Template = nil
		if err := renderer.WalkPage(&content, visitor); err != nil {
			return nil, ctx.Err()
		}
	}
	for _, ref := range r.doc.CommonData.TemplatePage {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		template := renderer.loadTemplate(ref.ID)
		if template == nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		unbind := renderer.bindPageResources(template)
		for order := range 3 {
			if err := renderer.walkLayers(template.Content.Layer, order, nil, visitor); err != nil {
				unbind()
				return nil, ctx.Err()
			}
		}
		unbind()
	}
	result := make(map[string]image.Point)
	for _, img := range images {
		name := cleanPackagePath(img.Location)
		size := visitor.sizes[name]
		old := result[name]
		result[name] = image.Pt(max(old.X, size.X), max(old.Y, size.Y))
	}
	return result, ctx.Err()
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

// outputImageFormat 识别可由资源压缩生成的图片格式
// 入参: data 图片编码
// 返回: string 标准资源格式，未识别时为空
func outputImageFormat(data []byte) string {
	if baseimage.IsJPEG(data) {
		return "JPEG"
	}
	if baseimage.IsPNG(data) {
		return "PNG"
	}
	return ""
}

// optimizeImageResource 优化可完整保留的BMP及TIFF，复用图像压缩和降采样策略
// 原始模式及无法保留的色彩配置不转换，仅采用小于原始编码的结果
// 入参: ctx 取消上下文, data 图片编码, options 压缩选项, size 像素需求
// 返回: []byte 图片编码，可能引用输入, error 解码、编码或取消错误
func optimizeImageResource(ctx context.Context, data []byte, options CompressionOptions, size image.Point) ([]byte, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Mode != CompressionUnchanged && baseimage.IsTIFF(data) {
		return optimizeTIFFResource(ctx, data, options, size)
	}
	if options.Mode == CompressionUnchanged || !bytes.HasPrefix(data, []byte("BM")) {
		return pdfgo.OptimizeImageResource(ctx, data, options, size)
	}
	return optimizeBMPResource(ctx, data, options, size)
}

// optimizeBMPResource 压缩可保留色彩与密度的位图，未知元数据及高精度位域保持原样
// 入参: ctx 取消上下文, data BMP编码, options 压缩选项, size 像素需求
// 返回: []byte 图片编码, error 解码、编码或取消错误
func optimizeBMPResource(ctx context.Context, data []byte, options CompressionOptions, size image.Point) ([]byte, error) {
	offset, width, height, depth, compression, err := baseimage.ParseBMPHeader(data)
	if err != nil {
		return data, nil
	}
	if height < 0 {
		height = -height
	}
	if int64(width)*int64(height) > outputOptimizationBufferLimit/4 || compression != 0 && compression != 3 {
		return data, nil
	}
	header := binary.LittleEndian.Uint32(data[14:18])
	if header != 40 && header != 108 && header != 124 {
		return data, nil
	}
	if header >= 108 && binary.LittleEndian.Uint32(data[70:74]) != 0x73524742 {
		return data, nil
	}
	if header == 124 && (binary.LittleEndian.Uint32(data[122:126]) != 0 || binary.LittleEndian.Uint32(data[126:130]) != 0 || binary.LittleEndian.Uint32(data[130:134]) != 0) {
		return data, nil
	}
	stride := (int64(width)*int64(depth) + 31) / 32 * 4
	if int64(offset)+stride*int64(height) != int64(len(data)) {
		return data, nil
	}
	density, ok := baseimage.BMPDensity(data)
	if !ok {
		return data, nil
	}
	if depth == 16 || depth == 32 {
		masks, err := baseimage.BMPColorMasks(data, offset, depth, compression)
		if err != nil {
			return data, nil
		}
		if options.Mode == CompressionLossless {
			for _, mask := range masks {
				if mask.Max > 255 {
					return data, nil
				}
			}
		}
	}
	var pixels image.Image
	if depth == 16 || compression == 3 {
		pixels, err = baseimage.DecodeBMPContext(ctx, data)
	} else {
		pixels, _, err = image.Decode(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	}
	if err != nil {
		return nil, err
	}
	return optimizeRasterResource(ctx, data, pixels, options, size, density)
}

// optimizeRasterResource 编码已解码的资源图片，同步降采样后的像素密度，仅采用更小结果
// 先降采样再编码，绝对密度按最终尺寸量化，相对密度保留精确比例
// 有密度时保留PNG，未知密度按最终像素选择编码格式
// 入参: ctx 取消上下文, data 原编码, pixels 像素, options 压缩选项, size 像素需求, density 像素密度
// 返回: []byte 图片编码, error 编码或取消错误
func optimizeRasterResource(ctx context.Context, data []byte, pixels image.Image, options CompressionOptions, size image.Point, density baseimage.Density) ([]byte, error) {
	hasDensity := density.X != 0 || density.Y != 0
	before := pixels.Bounds().Size()
	if options.Mode == CompressionLossy {
		var err error
		pixels, err = pdfgo.ResizeImage(ctx, pixels, size)
		if err != nil {
			return nil, err
		}
	}
	var ok bool
	density, ok = density.Scale(before, pixels.Bounds().Size())
	if !ok {
		return data, ctx.Err()
	}
	var candidate []byte
	var err error
	if hasDensity {
		var encoded bytes.Buffer
		err = pdfgo.EncodePNGWithOptions(ctx, &encoded, pixels, options)
		candidate = encoded.Bytes()
	} else {
		candidate, err = pdfgo.EncodeImageResource(ctx, pixels, options)
	}
	if err != nil {
		if errors.Is(err, io.ErrShortBuffer) {
			return data, ctx.Err()
		}
		return nil, err
	}
	if hasDensity {
		if len(candidate) > outputOptimizationBufferLimit-baseimage.PNGDensityChunkSize {
			return data, ctx.Err()
		}
		candidate, ok = baseimage.WithPNGDensity(candidate, density)
		if !ok {
			return data, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(candidate) < len(data) {
		return candidate, nil
	}
	return data, nil
}

// optimizeTIFFResource 压缩单目录TIFF，保留方向、透明度、采样精度及像素密度
// 主图或单个瓦片的预计缓冲超限时保留原编码
// 入参: ctx 取消上下文, data TIFF编码, options 压缩选项, size 显示像素需求
// 返回: []byte 图片编码，不可完整转换时保留原文, error 编码或取消错误
func optimizeTIFFResource(ctx context.Context, data []byte, options CompressionOptions, size image.Point) ([]byte, error) {
	layout, err := baseimage.ReadTIFFLayoutContext(ctx, bytes.NewReader(data))
	if err != nil {
		return data, ctx.Err()
	}
	pixelSize := int64(layout.DecodedPixelBytes())
	if int64(layout.Width) > outputOptimizationBufferLimit/pixelSize/int64(layout.Height) {
		return data, ctx.Err()
	}
	if layout.TileWidth != 0 && int64(layout.TileWidth) > outputOptimizationBufferLimit/pixelSize/int64(layout.TileHeight) {
		return data, ctx.Err()
	}
	density, ok := baseimage.TIFFConversionDensity(data, layout)
	if !ok {
		return data, ctx.Err()
	}
	pixels, err := baseimage.DecodeTIFF(ctx, data, layout)
	if err != nil {
		return data, ctx.Err()
	}
	return optimizeRasterResource(ctx, data, pixels, options, size, density)
}
