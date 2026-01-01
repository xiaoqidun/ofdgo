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
	Fonts                 Fonts                 `xml:"Fonts"`
	MultiMedias           MultiMedias           `xml:"MultiMedias"`
	DrawParams            DrawParams            `xml:"DrawParams"`
	CompositeGraphicUnits CompositeGraphicUnits `xml:"CompositeGraphicUnits"`
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
	ID          string       `xml:"ID,attr"`
	Relative    string       `xml:"Relative,attr"`
	ResourceID  string       `xml:"ResourceID,attr"`
	BaseLoc     string       `xml:"BaseLoc,attr"`
	LineWidth   float64      `xml:"LineWidth,attr"`
	FillColor   *FillColor   `xml:"FillColor"`
	StrokeColor *StrokeColor `xml:"StrokeColor"`
}

// CompositeGraphicUnits 复合图元集合
type CompositeGraphicUnits struct {
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
}

// CompositeGraphicUnit 复合图元
type CompositeGraphicUnit struct {
	ID         string `xml:"ID,attr"`
	BaseLoc    string `xml:"BaseLoc,attr"`
	ResourceID string `xml:"ResourceID,attr"`
}
