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

import "encoding/xml"

// Res 资源文件结构
type Res struct {
	XMLName               xml.Name              `xml:"Res"`
	BaseLoc               string                `xml:"BaseLoc,attr"`
	ColorSpaces           ColorSpaces           `xml:"ColorSpaces"`
	Fonts                 Fonts                 `xml:"Fonts"`
	MultiMedias           MultiMedias           `xml:"MultiMedias"`
	DrawParams            DrawParams            `xml:"DrawParams"`
	CompositeGraphicUnits CompositeGraphicUnits `xml:"CompositeGraphicUnits"`
}

// ColorSpaces 颜色空间集合
type ColorSpaces struct {
	ColorSpace []ColorSpace `xml:"ColorSpace"`
}

// ColorSpace 颜色空间定义
type ColorSpace struct {
	ID               string   `xml:"ID,attr"`
	Type             string   `xml:"Type,attr"`
	BitsPerComponent int      `xml:"BitsPerComponent,attr"`
	Profile          string   `xml:"Profile,attr,omitempty"`
	Palette          []string `xml:"Palette>CV"`
	profile          *colorProfile
}

// Fonts 字体集合
type Fonts struct {
	Font []Font `xml:"Font"`
}

// Font 字体定义
type Font struct {
	ID         string `xml:"ID,attr"`
	FontName   string `xml:"FontName,attr"`
	FamilyName string `xml:"FamilyName,attr"`
	Charset    string `xml:"Charset,attr"`
	Italic     bool   `xml:"Italic,attr"`
	Bold       bool   `xml:"Bold,attr"`
	Serif      bool   `xml:"Serif,attr"`
	FixedWidth bool   `xml:"FixedWidth,attr"`
	FontFile   string `xml:"FontFile"`
}

// MultiMedias 多媒体集合
type MultiMedias struct {
	MultiMedia []MultiMedia `xml:"MultiMedia"`
}

// MultiMedia 多媒体定义
type MultiMedia struct {
	ID        string `xml:"ID,attr"`
	Type      string `xml:"Type,attr"`
	Format    string `xml:"Format,attr"`
	MediaFile string `xml:"MediaFile"`
}

// DrawParams 绘制参数集合
type DrawParams struct {
	DrawParam []DrawParam `xml:"DrawParam"`
}

// DrawParam 绘制参数
type DrawParam struct {
	ID             string       `xml:"ID,attr"`
	Relative       string       `xml:"Relative,attr"`
	ResourceID     string       `xml:"ResourceID,attr"`
	BaseLoc        string       `xml:"BaseLoc,attr"`
	Link           string       `xml:"Link,attr"`
	LineWidth      float64      `xml:"LineWidth,attr"`
	Join           string       `xml:"Join,attr"`
	Cap            string       `xml:"Cap,attr"`
	DashOffset     *float64     `xml:"DashOffset,attr"`
	DashPattern    string       `xml:"DashPattern,attr"`
	MiterLimit     float64      `xml:"MiterLimit,attr"`
	Font           string       `xml:"Font,attr"`
	Size           float64      `xml:"Size,attr"`
	Weight         int          `xml:"Weight,attr"`
	Italic         bool         `xml:"Italic,attr"`
	FillColor      *FillColor   `xml:"FillColor"`
	StrokeColor    *StrokeColor `xml:"StrokeColor"`
	LineWidthSet   bool         `xml:"-"`
	dashPatternSet bool
}

// CompositeGraphicUnits 复合图元集合
type CompositeGraphicUnits struct {
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
}

// CompositeGraphicUnit 复合图元
// Width、Height、Thumbnail和Substitution用于矢量资源定义
type CompositeGraphicUnit struct {
	ID                   string                 `xml:"ID,attr"`
	BaseLoc              string                 `xml:"BaseLoc,attr"`
	ResourceID           string                 `xml:"ResourceID,attr"`
	Boundary             string                 `xml:"Boundary,attr"`
	CTM                  string                 `xml:"CTM,attr"`
	DrawParam            string                 `xml:"DrawParam,attr"`
	Alpha                *int                   `xml:"Alpha,attr"`
	Visible              *bool                  `xml:"Visible,attr"`
	Width                float64                `xml:"Width,attr"`
	Height               float64                `xml:"Height,attr"`
	Thumbnail            string                 `xml:"Thumbnail"`
	Substitution         string                 `xml:"Substitution"`
	Objects              []GraphicObject        `xml:"-"`
	TextObject           []TextObject           `xml:"TextObject"`
	PathObject           []PathObject           `xml:"PathObject"`
	ImageObject          []ImageObject          `xml:"ImageObject"`
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
	Clips                *Clips                 `xml:"Clips"`
	Actions              []Action               `xml:"Actions>Action"`
	states               map[string]editorCompositeState
	extentSet            bool
}

// resourceValue 优先查找原标识与标准十进制标识，回退别名有冲突时不猜测
// 入参: values 已加载资源, id 标识文本
// 返回: T 资源值, bool 是否存在唯一结果
func resourceValue[T comparable](values map[string]T, id string) (T, bool) {
	if value, ok := values[id]; ok {
		return value, true
	}
	var value T
	key := editorResourceID(id)
	if key == "" {
		return value, false
	}
	if value, ok := values[key]; ok {
		return value, true
	}
	found := false
	for alias, candidate := range values {
		if editorResourceID(alias) != key {
			continue
		}
		if found && value != candidate {
			var zero T
			return zero, false
		}
		value, found = candidate, true
	}
	return value, found
}
