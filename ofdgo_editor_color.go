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

// editorPaintable 判断文字与路径颜色能否在保留其他字段的前提下修改
// 入参: object 图形对象
// 返回: bool 是否支持纯色编辑
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
		if _, err := e.Color(value); err != nil {
			return false
		}
	}
	return true
}

// editorColor 校验当前文档的颜色空间和分量
// 入参: value 颜色
// 返回: error 错误信息
func (e *Editor) editorColor(value *FillColor) error {
	_, err := e.Color(value)
	return err
}
