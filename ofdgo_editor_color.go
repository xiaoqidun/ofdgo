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
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// PatternStyle 图案尺寸、步进和变换增量，nil字段保持原值，单位为毫米
type PatternStyle struct {
	Width  *float64
	Height *float64
	XStep  *float64
	YStep  *float64
	CTM    *string
}

// GradientStops 解析当前文档的渐变分段，保留顺序并合并透明度
// 入参: segments 渐变分段, alpha 外层透明度
// 返回: []ColorStop 后端无关的色标, error 颜色解析错误
func (e *Editor) GradientStops(segments []ShdSegment, alpha *int) ([]ColorStop, error) {
	positions := gradientPositions(segments)
	stops := make([]ColorStop, len(segments))
	for i, segment := range segments {
		c, err := e.Color(&FillColor{Value: segment.Color.Value, Index: segment.Color.Index, ColorSpace: segment.Color.ColorSpace, Alpha: mergeAlpha(segment.Color.Alpha, alpha)})
		if err != nil {
			return nil, err
		}
		stops[i] = ColorStop{Offset: positions[i], Color: colorToRGBA(c)}
	}
	return stops, nil
}

// StylePatterns 修改已有图案布局，保留单元内容、资源引用和未知扩展
// 入参: page 页面索引, ids 对象标识, stroke 是否修改描边, style 图案布局增量
// 返回: error 错误信息
func (e *Editor) StylePatterns(page int, ids []string, stroke bool, style PatternStyle) error {
	for _, size := range []*float64{style.Width, style.Height} {
		if size != nil && (!finite(*size) || *size <= 0) {
			return fmt.Errorf("pattern dimensions must be positive and finite")
		}
	}
	for _, step := range []*float64{style.XStep, style.YStep} {
		if step != nil && (!finite(*step) || *step < 0) {
			return fmt.Errorf("pattern steps must be nonnegative and finite")
		}
	}
	if style.CTM != nil && *style.CTM != "" {
		if _, err := creationNumbers(*style.CTM, 6); err != nil {
			return err
		}
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	for i := range objects {
		objects[i] = cloneEditorData(objects[i])
		object := &objects[i]
		var fill *FillColor
		switch object.Type {
		case "TextObject":
			fill = object.TextObject.FillColor
			if stroke {
				fill = (*FillColor)(object.TextObject.StrokeColor)
			}
		case "PathObject", "Path":
			fill = object.PathObject.FillColor
			if stroke {
				fill = (*FillColor)(object.PathObject.StrokeColor)
			}
		}
		if fill == nil || fill.Pattern == nil {
			return fmt.Errorf("object %q does not use a pattern", ids[i])
		}
		pattern := fill.Pattern
		for _, pair := range []struct{ value, target *float64 }{{style.Width, &pattern.Width}, {style.Height, &pattern.Height}, {style.XStep, &pattern.XStep}, {style.YStep, &pattern.YStep}} {
			if pair.value != nil {
				*pair.target = *pair.value
			}
		}
		if style.CTM != nil {
			pattern.CTM = *style.CTM
		}
	}
	return e.updateObjects(page, objects, true)
}

// Color 解析当前文档的纯色，支持RGB、灰度、CMYK及调色板，不修改原颜色
// 入参: value 颜色，nil表示默认黑色
// 返回: color.NRGBA 非预乘颜色, error 错误信息
func (e *Editor) Color(value *FillColor) (color.NRGBA, error) {
	space := ColorSpace{Type: "RGB", BitsPerComponent: 8}
	if value == nil {
		return color.NRGBA{A: 255}, nil
	}
	if value.Pattern != nil || value.AxialShd != nil || value.RadialShd != nil || value.unsupported {
		return color.NRGBA{}, fmt.Errorf("color is not a supported solid color")
	}
	id := value.ColorSpace
	if id == "" && e.source != nil && e.source.document.CommonData.DefaultCS != 0 {
		id = strconv.Itoa(e.source.document.CommonData.DefaultCS)
	}
	if id != "" {
		var found *ColorSpace
		if e.source != nil {
			found = e.source.reader.colorSpaceCache[id]
		}
		for _, resource := range e.resources {
			if resource.space != nil && resource.space.ID == id {
				found = resource.space
				break
			}
		}
		if found == nil {
			return color.NRGBA{}, fmt.Errorf("color space %q not found", id)
		}
		space = *found
	}
	bits := space.BitsPerComponent
	if bits == 0 {
		bits = 8
	}
	if bits != 1 && bits != 2 && bits != 4 && bits != 8 && bits != 16 {
		return color.NRGBA{}, fmt.Errorf("unsupported color depth %d", bits)
	}
	count := 3
	switch space.Type {
	case "RGB":
	case "GRAY":
		count = 1
	case "CMYK":
		count = 4
	default:
		return color.NRGBA{}, fmt.Errorf("unsupported color space %q", space.Type)
	}
	text := value.Value
	if strings.TrimSpace(text) == "" && value.Index != nil {
		if *value.Index < 0 || *value.Index >= len(space.Palette) {
			return color.NRGBA{}, fmt.Errorf("color palette index out of range")
		}
		text = space.Palette[*value.Index]
	}
	fields := strings.Fields(text)
	if len(fields) != count {
		return color.NRGBA{}, fmt.Errorf("expected %d color components", count)
	}
	var components [4]uint8
	limit := 1<<bits - 1
	for i, field := range fields {
		v, err := parseColorComponent(field)
		if err != nil || v < 0 || v > limit {
			return color.NRGBA{}, fmt.Errorf("invalid color component %q", field)
		}
		components[i] = uint8((v*255 + limit/2) / limit)
	}
	r, g, b := components[0], components[1], components[2]
	if space.Type == "GRAY" {
		g, b = r, r
	} else if space.Type == "CMYK" {
		r, g, b = color.CMYKToRGB(components[0], components[1], components[2], components[3])
	}
	a := 255
	if value.Alpha != nil {
		a = *value.Alpha
		if a < 0 || a > 255 {
			return color.NRGBA{}, fmt.Errorf("color alpha must be between 0 and 255")
		}
	}
	return color.NRGBA{R: r, G: g, B: b, A: uint8(a)}, nil
}

// RGBColor 创建浏览器等调用方使用的RGB纯色，非RGB文档按需注册独立颜色空间
// 不修改文档默认颜色空间，未使用的新增颜色空间不写出
// 入参: value 非预乘颜色
// 返回: *FillColor OFD颜色, error 错误信息
func (e *Editor) RGBColor(value color.NRGBA) (*FillColor, error) {
	result := &FillColor{Value: fmt.Sprintf("%d %d %d", value.R, value.G, value.B)}
	if value.A != 255 {
		alpha := int(value.A)
		result.Alpha = &alpha
	}
	if e.sourceRGB() {
		return result, nil
	}
	for _, id := range slices.Sorted(maps.Keys(e.source.reader.colorSpaceCache)) {
		space := e.source.reader.colorSpaceCache[id]
		if space.Type == "RGB" && (space.BitsPerComponent == 0 || space.BitsPerComponent == 8) && len(space.Palette) == 0 {
			result.ColorSpace = id
			return result, nil
		}
	}
	for _, resource := range e.resources {
		if resource.space != nil {
			result.ColorSpace = resource.space.ID
			return result, nil
		}
	}
	if err := e.prepareSourceIDs(); err != nil {
		return nil, err
	}
	result.ColorSpace = e.nextID()
	e.resources = append(e.resources, editorResource{space: &ColorSpace{ID: result.ColorSpace, Type: "RGB", BitsPerComponent: 8}})
	return result, nil
}

// PaintObjects 更新文字或路径的填充或描边，保留定位、其他样式和原文扩展
// 入参: page 页面索引, ids 对象标识, paint 绘制颜色，nil恢复默认, stroke 是否修改描边
// 返回: error 错误信息
func (e *Editor) PaintObjects(page int, ids []string, paint *FillColor, stroke bool) error {
	if err := e.editorColor(paint); err != nil {
		return err
	}
	objects, _, err := e.selectedObjects(page, ids)
	if err != nil {
		return err
	}
	for i := range objects {
		object := &objects[i]
		switch object.Type {
		case "TextObject":
			if stroke {
				object.TextObject.StrokeColor = (*StrokeColor)(cloneEditorData(paint))
			} else {
				object.TextObject.FillColor = cloneEditorData(paint)
			}
		case "PathObject":
			if stroke {
				object.PathObject.StrokeColor = (*StrokeColor)(cloneEditorData(paint))
			} else {
				object.PathObject.FillColor = cloneEditorData(paint)
			}
		default:
			return fmt.Errorf("object %q does not have text or path paint", editorObjectID(*object))
		}
	}
	return e.updateObjects(page, objects, true)
}

// editorPaintable 判断文字与路径颜色能否在保留其他字段的前提下修改
// 入参: object 图形对象
// 返回: bool 是否支持绘制颜色编辑
func (e *Editor) editorPaintable(object GraphicObject) bool {
	var fill, stroke *FillColor
	switch object.Type {
	case "TextObject":
		fill, stroke = object.TextObject.FillColor, (*FillColor)(object.TextObject.StrokeColor)
	case "PathObject":
		fill, stroke = object.PathObject.FillColor, (*FillColor)(object.PathObject.StrokeColor)
	default:
		return false
	}
	for _, value := range []*FillColor{fill, stroke} {
		if err := e.editorColor(value); err != nil {
			return false
		}
	}
	return true
}

// collectPaintReferences 收集纯色、渐变和图案引用的资源
// 入参: paint 绘制颜色, used 引用集合
func collectPaintReferences(paint *FillColor, used map[string]bool) {
	if paint == nil {
		return
	}
	used[paint.ColorSpace] = true
	var segments []ShdSegment
	if paint.AxialShd != nil {
		segments = paint.AxialShd.Segment
	}
	if paint.RadialShd != nil {
		segments = paint.RadialShd.Segment
	}
	for _, segment := range segments {
		used[segment.Color.ColorSpace] = true
	}
	if paint.Pattern != nil {
		for _, object := range paint.Pattern.CellContent.Objects {
			collectObjectReferences(object, used)
		}
	}
}

// collectObjectReferences 收集对象和嵌套绘制内容引用的资源
// 入参: object 图形对象, used 引用集合
func collectObjectReferences(object GraphicObject, used map[string]bool) {
	switch object.Type {
	case "TextObject":
		used[object.TextObject.Font], used[object.TextObject.DrawParam] = true, true
		collectPaintReferences(object.TextObject.FillColor, used)
		collectPaintReferences((*FillColor)(object.TextObject.StrokeColor), used)
	case "PathObject", "Path":
		used[object.PathObject.DrawParam] = true
		collectPaintReferences(object.PathObject.FillColor, used)
		collectPaintReferences((*FillColor)(object.PathObject.StrokeColor), used)
	case "ImageObject":
		used[object.ImageObject.ResourceID], used[object.ImageObject.ImageMask] = true, true
		if object.ImageObject.Border != nil {
			collectPaintReferences((*FillColor)(object.ImageObject.Border.BorderColor), used)
		}
	case "CompositeObject", "CompositeGraphicUnit":
		collectCompositeReferences(object.CompositeGraphicUnit, used)
	}
}

// editorColor 校验当前文档的纯色和渐变
// 入参: value 颜色
// 返回: error 错误信息
func (e *Editor) editorColor(value *FillColor) error {
	if value != nil && value.Pattern != nil {
		pattern := value.Pattern
		if value.unsupported || value.AxialShd != nil || value.RadialShd != nil || !finite(pattern.Width) || !finite(pattern.Height) || pattern.Width <= 0 || pattern.Height <= 0 || !finite(pattern.XStep) || !finite(pattern.YStep) || pattern.XStep < pattern.Width || pattern.YStep < pattern.Height {
			return fmt.Errorf("invalid pattern paint")
		}
		if pattern.RelativeTo != "" && pattern.RelativeTo != "Page" && pattern.RelativeTo != "Object" {
			return fmt.Errorf("invalid pattern reference")
		}
		if pattern.CTM != "" {
			if _, err := creationNumbers(pattern.CTM, 6); err != nil {
				return err
			}
		}
		base := *value
		base.Pattern = nil
		if _, err := e.Color(&base); err != nil {
			return err
		}
		for _, object := range pattern.CellContent.Objects {
			if object.Type == "PathObject" && (object.PathObject.FillColor != nil && object.PathObject.FillColor.Pattern != nil || object.PathObject.StrokeColor != nil && object.PathObject.StrokeColor.Pattern != nil) || object.Type == "TextObject" && (object.TextObject.FillColor != nil && object.TextObject.FillColor.Pattern != nil || object.TextObject.StrokeColor != nil && object.TextObject.StrokeColor.Pattern != nil) {
				return fmt.Errorf("nested pattern paint is not supported")
			}
			if _, err := e.prepareObject("", object); err != nil {
				return err
			}
		}
		return nil
	}
	if value == nil || value.AxialShd == nil && value.RadialShd == nil {
		_, err := e.Color(value)
		return err
	}
	if value.unsupported || value.Pattern != nil || value.AxialShd != nil && value.RadialShd != nil || value.Value != "" || value.Index != nil {
		return fmt.Errorf("specify one paint source")
	}
	if value.Alpha != nil && (*value.Alpha < 0 || *value.Alpha > 255) {
		return fmt.Errorf("color alpha must be between 0 and 255")
	}
	var mapType, extend, start, end string
	var unit float64
	var segments []ShdSegment
	if node := value.AxialShd; node != nil {
		mapType, unit, extend, start, end, segments = node.MapType, node.MapUnit, node.Extend, node.StartPoint, node.EndPoint, node.Segment
	} else {
		node := value.RadialShd
		mapType, unit, extend, start, end, segments = node.MapType, node.MapUnit, node.Extend, node.StartPoint, node.EndPoint, node.Segment
		if !finite(node.StartRadius) || !finite(node.EndRadius) || node.StartRadius < 0 || node.EndRadius < 0 || !finite(node.Eccentricity) || node.Eccentricity < 0 || node.Eccentricity >= 1 || !finite(node.Angle) {
			return fmt.Errorf("invalid radial gradient geometry")
		}
	}
	if !slices.Contains([]string{"", "Direct", "Repeat", "Reflect"}, mapType) || !finite(unit) || unit < 0 || (mapType == "Repeat" || mapType == "Reflect") && unit == 0 || !slices.Contains([]string{"", "0", "1", "2", "3"}, extend) {
		return fmt.Errorf("invalid gradient mapping")
	}
	for _, point := range []string{start, end} {
		if _, err := creationNumbers(point, 2); err != nil {
			return err
		}
	}
	if len(segments) < 2 {
		return fmt.Errorf("gradient requires at least two stops")
	}
	previous := -1.0
	positions := gradientPositions(segments)
	for index, segment := range segments {
		position := positions[index]
		if !finite(position) || position < previous || position < 0 || position > 1 {
			return fmt.Errorf("gradient stops must be ordered within zero and one")
		}
		previous = position
		color := segment.Color
		if color.ColorSpace == "" {
			color.ColorSpace = value.ColorSpace
		}
		if _, err := e.Color(&FillColor{Value: color.Value, Index: color.Index, ColorSpace: color.ColorSpace, Alpha: color.Alpha}); err != nil {
			return err
		}
	}
	return nil
}
