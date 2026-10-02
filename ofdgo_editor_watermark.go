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
	"math"
)

// WatermarkOptions 配置水印旋转、居中和平铺，Area为页面比例区域，nil使用整页
// Angle为顺时针角度，Gap为平铺最小毫米间距，零值使用10毫米
// 非平铺时Area宽高同时为零表示中心点定位，不缩放图元
type WatermarkOptions struct {
	Creator string
	Angle   float64
	Tile    bool
	Area    *Box
	Gap     float64
}

// AddWatermarks 向指定页面添加文字或图片水印，复用编辑器字体来源及后端，一次操作计入一条撤销记录
// 图元Boundary提供原始尺寸，忽略其位置；按实际绘制范围居中，超出区域时等比缩小
// 失败或取消时保留原文档，输入图元不被修改
// 入参: ctx 取消上下文, indexes 不重复的页面索引, object 水印图元, options 布局选项
// 返回: []string 按页面顺序排列的注解标识, error 错误信息
func (e *Editor) AddWatermarks(ctx context.Context, indexes []int, object GraphicObject, options WatermarkOptions) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.validatePageIndexes(indexes); err != nil {
		return nil, err
	}
	if !finite(options.Angle) || !finite(options.Gap) || options.Gap < 0 {
		return nil, fmt.Errorf("invalid watermark angle or gap")
	}
	if area := options.Area; area != nil {
		point := !options.Tile && area.W == 0 && area.H == 0
		if !finite(area.X) || !finite(area.Y) || !finite(area.W) || !finite(area.H) || area.X < 0 || area.Y < 0 || (!point && (area.W <= 0 || area.H <= 0)) || area.X+area.W > 1 || area.Y+area.H > 1 {
			return nil, fmt.Errorf("invalid watermark area")
		}
	}
	if options.Gap == 0 {
		options.Gap = 10
	}
	object = cloneEditorData(object)
	var boundary, ctm *string
	switch object.Type {
	case "TextObject":
		boundary, ctm = &object.TextObject.Boundary, &object.TextObject.CTM
	case "ImageObject":
		boundary, ctm = &object.ImageObject.Boundary, &object.ImageObject.CTM
	default:
		return nil, fmt.Errorf("watermark requires text or image")
	}
	box, err := creationBox(*boundary)
	if err != nil || box.W <= 0 || box.H <= 0 {
		return nil, fmt.Errorf("invalid watermark dimensions")
	}
	if len(indexes) == 0 {
		return nil, nil
	}
	box.X, box.Y = 0, 0
	*boundary = editorBoxString(box)
	angle := math.Mod(options.Angle, 360) * math.Pi / 180
	rotation := Matrix{a: math.Cos(angle), b: math.Sin(angle), c: -math.Sin(angle), d: math.Cos(angle)}
	rotation = TranslationMatrix(box.W/2, box.H/2).Multiply(rotation).Multiply(TranslationMatrix(-box.W/2, -box.H/2))
	if *ctm != "" {
		if _, err := creationNumbers(*ctm, 6); err != nil {
			return nil, err
		}
	}
	object = transformEditorMatrix(object, rotation)
	if object.Type == "TextObject" {
		boundary = &object.TextObject.Boundary
	} else {
		boundary = &object.ImageObject.Boundary
	}
	extent, _ := ParseBox(*boundary)
	extent.X, extent.Y = 0, 0
	*boundary = editorBoxString(extent)
	preview := e.transactionSnapshot()
	preview.SetHistoryLimit(0)
	id, err := preview.AddObject(indexes[0], object)
	if err != nil {
		return nil, err
	}
	object, err = preview.Object(indexes[0], id)
	if err != nil {
		return nil, err
	}
	reader, err := preview.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	page, err := reader.PageContentByIndex(indexes[0])
	if err != nil {
		return nil, err
	}
	renderer := e.newRenderer(reader)
	measured := cloneEditorData(object)
	if measured.Type == "TextObject" {
		measured.TextObject.Alpha = nil
	} else {
		measured.ImageObject.Alpha = nil
	}
	bounds, err := renderer.ObjectBounds(measured, "")
	if err != nil {
		return nil, err
	}
	if !finite(bounds.X) || !finite(bounds.Y) || !finite(bounds.W) || !finite(bounds.H) || bounds.W <= 0 || bounds.H <= 0 {
		return nil, fmt.Errorf("watermark has no visible content")
	}
	var ids []string
	err = e.Transaction(func(edit *Editor) error {
		for _, index := range indexes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if index != indexes[0] {
				page, err = reader.PageContentByIndex(index)
				if err != nil {
					return err
				}
			}
			pageBox, err := renderer.GetPageBox(page)
			if err != nil {
				return err
			}
			annotation, err := layoutWatermark(ctx, object, bounds, box, options, pageBox)
			if err != nil {
				return err
			}
			id, err := edit.AddAnnotation(index, annotation)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// layoutWatermark 按绘制范围布局独立水印外观，保留页面原点及图元变换
// 入参: ctx 取消上下文, base 水印图元, bounds 绘制范围, box 原始尺寸, options 布局选项, page 页面范围
// 返回: Annotation 水印注解, error 取消或布局错误
func layoutWatermark(ctx context.Context, base GraphicObject, bounds, box Box, options WatermarkOptions, page Box) (Annotation, error) {
	area := page
	if options.Area != nil {
		area = Box{X: page.X + options.Area.X*page.W, Y: page.Y + options.Area.Y*page.H, W: options.Area.W * page.W, H: options.Area.H * page.H}
	}
	if !finite(page.X) || !finite(page.Y) || !finite(page.W) || !finite(page.H) || page.W <= 0 || page.H <= 0 {
		return Annotation{}, fmt.Errorf("invalid watermark page area")
	}
	scale := 1.0
	if area.W > 0 && area.H > 0 {
		scale = math.Min(1, math.Min(area.W/bounds.W, area.H/bounds.H))
	}
	if scale != 1 {
		matrix := Matrix{a: scale, d: scale}
		base = transformEditorMatrix(base, matrix)
		bounds = matrix.TransformBox(bounds)
	}
	var extent Box
	if base.Type == "TextObject" {
		extent, _ = ParseBox(base.TextObject.Boundary)
	} else {
		extent, _ = ParseBox(base.ImageObject.Boundary)
	}
	columns, rows := 1.0, 1.0
	if options.Tile {
		angle := math.Mod(options.Angle, 360) * math.Pi / 180
		width := math.Max(bounds.W, (math.Abs(math.Cos(angle))*box.W+math.Abs(math.Sin(angle))*box.H)*scale)
		height := math.Max(bounds.H, (math.Abs(math.Sin(angle))*box.W+math.Abs(math.Cos(angle))*box.H)*scale)
		columns = math.Max(1, math.Floor((area.W+options.Gap)/(width+options.Gap)))
		rows = math.Max(1, math.Floor((area.H+options.Gap)/(height+options.Gap)))
	} else {
		area = Box{X: area.X + (area.W-bounds.W)/2, Y: area.Y + (area.H-bounds.H)/2, W: bounds.W, H: bounds.H}
	}
	if !finite(columns*rows) || columns*rows > 100000 {
		return Annotation{}, fmt.Errorf("too many watermark tiles")
	}
	annotation := Annotation{Type: "Watermark", Creator: options.Creator, Appearance: Appearance{Boundary: editorBoxString(area)}}
	annotation.Appearance.Objects = make([]GraphicObject, 0, int(columns*rows))
	for row := 0; row < int(rows); row++ {
		if err := ctx.Err(); err != nil {
			return Annotation{}, err
		}
		for column := 0; column < int(columns); column++ {
			x := (float64(column)+0.5)*area.W/columns - bounds.X - bounds.W/2
			y := (float64(row)+0.5)*area.H/rows - bounds.Y - bounds.H/2
			object := base
			boundary := editorBoxString(Box{X: x, Y: y, W: extent.W, H: extent.H})
			if object.Type == "TextObject" {
				object.TextObject.Boundary = boundary
			} else {
				object.ImageObject.Boundary = boundary
			}
			annotation.Appearance.Objects = append(annotation.Appearance.Objects, object)
		}
	}
	return annotation, nil
}
