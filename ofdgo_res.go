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
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

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
	bitsSet          bool
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
// Cap为端点样式，Join为转角样式，MiterLimit为尖角截断值，DashPattern为虚线间隔序列
// MiterLimitSet为true时，零截断值覆盖继承值，否则零值表示未指定
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
	MiterLimitSet  bool         `xml:"-"`
	WeightSet      bool         `xml:"-"`
	ItalicSet      bool         `xml:"-"`
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
	Name                 string                 `xml:"Name,attr"`
	BaseLoc              string                 `xml:"BaseLoc,attr"`
	ResourceID           string                 `xml:"ResourceID,attr"`
	Boundary             string                 `xml:"Boundary,attr"`
	CTM                  string                 `xml:"CTM,attr"`
	DrawParam            string                 `xml:"DrawParam,attr"`
	LineWidth            float64                `xml:"LineWidth,attr"`
	Cap                  string                 `xml:"Cap,attr"`
	Join                 string                 `xml:"Join,attr"`
	MiterLimit           float64                `xml:"MiterLimit,attr"`
	DashOffset           *float64               `xml:"DashOffset,attr"`
	DashPattern          string                 `xml:"DashPattern,attr"`
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
	LineWidthSet         bool                   `xml:"-"`
	MiterLimitSet        bool                   `xml:"-"`
	dashPatternSet       bool
	states               map[string]editorCompositeState
	extentSet            bool
}

// UnmarshalXML 解析资源文件，隔离外部命名空间的同名集合
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (res *Res) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Res" {
		return fmt.Errorf("expected Res element, got %s", start.Name.Local)
	}
	*res = Res{XMLName: start.Name, BaseLoc: attrValue(start, "BaseLoc")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target any
		switch node.Name.Local {
		case "ColorSpaces":
			target = &res.ColorSpaces
		case "Fonts":
			target = &res.Fonts
		case "MultiMedias":
			target = &res.MultiMedias
		case "DrawParams":
			target = &res.DrawParams
		case "CompositeGraphicUnits":
			target = &res.CompositeGraphicUnits
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
}

// UnmarshalXML 解析颜色空间集合
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (spaces *ColorSpaces) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "ColorSpace", &spaces.ColorSpace)
}

// UnmarshalXML 解析颜色空间属性及调色板
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (space *ColorSpace) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*space = ColorSpace{ID: attrValue(start, "ID"), Type: attrValue(start, "Type"), BitsPerComponent: 8, Profile: attrValue(start, "Profile")}
	if value := strings.TrimSpace(attrValue(start, "BitsPerComponent")); value != "" {
		bits, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		space.BitsPerComponent = bits
		space.bitsSet = true
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "Palette" {
			return decodeOFDCollection(d, node, "CV", &space.Palette)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析字体集合
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (fonts *Fonts) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "Font", &fonts.Font)
}

// UnmarshalXML 解析字体声明及内嵌字体路径
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (font *Font) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	charset := attrValue(start, "Charset")
	if strings.TrimSpace(charset) == "" {
		charset = "unicode"
	}
	*font = Font{ID: attrValue(start, "ID"), FontName: attrValue(start, "FontName"), FamilyName: attrValue(start, "FamilyName"), Charset: charset}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target *bool
		switch attr.Name.Local {
		case "Italic":
			target = &font.Italic
		case "Bold":
			target = &font.Bold
		case "Serif":
			target = &font.Serif
		case "FixedWidth":
			target = &font.FixedWidth
		}
		if value := strings.TrimSpace(attr.Value); target != nil && value != "" {
			flag, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			*target = flag
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "FontFile" {
			return d.DecodeElement(&font.FontFile, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析多媒体集合
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (media *MultiMedias) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "MultiMedia", &media.MultiMedia)
}

// UnmarshalXML 解析多媒体声明及包内文件路径
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (media *MultiMedia) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*media = MultiMedia{ID: attrValue(start, "ID"), Type: attrValue(start, "Type"), Format: attrValue(start, "Format")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "MediaFile" {
			return d.DecodeElement(&media.MediaFile, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析绘制参数集合
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (params *DrawParams) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "DrawParam", &params.DrawParam)
}

// UnmarshalXML 解析复合图元资源集合
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (units *CompositeGraphicUnits) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "CompositeGraphicUnit", &units.CompositeGraphicUnit)
}

// resourceIndex 提供原始标识与标准十进制标识的常数时间查找索引
type resourceIndex[T comparable] struct {
	exact     map[string]T
	canonical map[string]T
	ambiguous map[string]bool
}

// put 写入资源项并同步维护无歧义十进制标准键
// 入参: id 原始标识, value 资源值
func (idx *resourceIndex[T]) put(id string, value T) {
	if idx.exact == nil {
		idx.exact = make(map[string]T)
	}
	idx.exact[id] = value
	key := editorResourceID(id)
	if key == "" {
		return
	}
	if idx.ambiguous[key] {
		return
	}
	if idx.canonical == nil {
		idx.canonical = make(map[string]T)
	}
	if prev, exists := idx.canonical[key]; exists && prev != value {
		delete(idx.canonical, key)
		if idx.ambiguous == nil {
			idx.ambiguous = make(map[string]bool)
		}
		idx.ambiguous[key] = true
		return
	}
	idx.canonical[key] = value
}

// get 按原始标识或唯一标准十进制标识查找资源
// 入参: id 待查标识
// 返回: T 资源值, bool 是否命中
func (idx *resourceIndex[T]) get(id string) (T, bool) {
	var zero T
	if idx == nil || id == "" {
		return zero, false
	}
	if value, ok := idx.exact[id]; ok {
		return value, true
	}
	key := editorResourceID(id)
	if key == "" {
		return zero, false
	}
	if value, ok := idx.exact[key]; ok {
		return value, true
	}
	if idx.ambiguous[key] {
		return zero, false
	}
	value, ok := idx.canonical[key]
	return value, ok
}

// clone 复制资源索引表供派生阅读器独立扩展
// 返回: *resourceIndex[T] 索引副本
func (idx *resourceIndex[T]) clone() *resourceIndex[T] {
	if idx == nil {
		return &resourceIndex[T]{}
	}
	out := &resourceIndex[T]{}
	if len(idx.exact) != 0 {
		out.exact = make(map[string]T, len(idx.exact))
		for k, v := range idx.exact {
			out.exact[k] = v
		}
	}
	if len(idx.canonical) != 0 {
		out.canonical = make(map[string]T, len(idx.canonical))
		for k, v := range idx.canonical {
			out.canonical[k] = v
		}
	}
	if len(idx.ambiguous) != 0 {
		out.ambiguous = make(map[string]bool, len(idx.ambiguous))
		for k, v := range idx.ambiguous {
			out.ambiguous[k] = v
		}
	}
	return out
}

// parsedRes 保存单个资源描述文件的局部解析结果与常数时间索引
type parsedRes struct {
	path        string
	colorSpaces resourceIndex[*ColorSpace]
	medias      resourceIndex[MultiMedia]
	resPaths    resourceIndex[string]
	fonts       resourceIndex[*Font]
	drawParams  resourceIndex[*DrawParam]
	composites  resourceIndex[*CompositeGraphicUnit]
}

// clone 复制局部资源表及其内部可变资源定义
// 返回: *parsedRes 独立局部资源表副本
func (p *parsedRes) clone() *parsedRes {
	return p.cloneWith(make(map[*ColorSpace]*ColorSpace), make(map[*Font]*Font), make(map[*DrawParam]*DrawParam), make(map[*CompositeGraphicUnit]*CompositeGraphicUnit))
}

// cloneWith 按共享指针映射表深拷贝局部资源表，保持同一阅读器内的指针等价关系
// 入参: spaces 颜色空间映射, fonts 字体映射, drawParams 绘制参数映射, composites 复合图元映射
// 返回: *parsedRes 独立局部资源表副本
func (p *parsedRes) cloneWith(spaces map[*ColorSpace]*ColorSpace, fonts map[*Font]*Font, drawParams map[*DrawParam]*DrawParam, composites map[*CompositeGraphicUnit]*CompositeGraphicUnit) *parsedRes {
	if p == nil {
		return nil
	}
	return &parsedRes{
		path:        p.path,
		colorSpaces: *cloneResourceIndexPtr(&p.colorSpaces, spaces, cloneColorSpacePtr),
		medias:      *p.medias.clone(),
		resPaths:    *p.resPaths.clone(),
		fonts:       *cloneResourceIndexPtr(&p.fonts, fonts, cloneFontPtr),
		drawParams:  *cloneResourceIndexPtr(&p.drawParams, drawParams, cloneDrawParamPtr),
		composites:  *cloneResourceIndexPtr(&p.composites, composites, cloneCompositeUnitPtr),
	}
}

// cloneResourceIndexPtr 按指针映射深拷贝指针型资源索引表
// 入参: idx 源索引, memo 指针映射表, copyFn 单项深拷贝函数
// 返回: *resourceIndex[*T] 独立索引副本
func cloneResourceIndexPtr[T any](idx *resourceIndex[*T], memo map[*T]*T, copyFn func(*T) *T) *resourceIndex[*T] {
	if idx == nil {
		return &resourceIndex[*T]{}
	}
	mapPtr := func(src *T) *T {
		if src == nil {
			return nil
		}
		if dst, ok := memo[src]; ok {
			return dst
		}
		dst := copyFn(src)
		memo[src] = dst
		return dst
	}
	out := &resourceIndex[*T]{}
	if len(idx.exact) != 0 {
		out.exact = make(map[string]*T, len(idx.exact))
		for k, v := range idx.exact {
			out.exact[k] = mapPtr(v)
		}
	}
	if len(idx.canonical) != 0 {
		out.canonical = make(map[string]*T, len(idx.canonical))
		for k, v := range idx.canonical {
			out.canonical[k] = mapPtr(v)
		}
	}
	if len(idx.ambiguous) != 0 {
		out.ambiguous = maps.Clone(idx.ambiguous)
	}
	return out
}

// cloneResourceMapPtr 按指针映射深拷贝指针型资源字典
// 入参: values 源字典, memo 指针映射表, copyFn 单项深拷贝函数
// 返回: map[string]*T 独立字典副本
func cloneResourceMapPtr[T any](values map[string]*T, memo map[*T]*T, copyFn func(*T) *T) map[string]*T {
	if values == nil {
		return nil
	}
	out := make(map[string]*T, len(values))
	for k, v := range values {
		if v == nil {
			out[k] = nil
			continue
		}
		dst, ok := memo[v]
		if !ok {
			dst = copyFn(v)
			memo[v] = dst
		}
		out[k] = dst
	}
	return out
}

// cloneColorSpacePtr 深拷贝颜色空间定义及调色板切片
// 入参: cs 原颜色空间
// 返回: *ColorSpace 独立颜色空间副本
func cloneColorSpacePtr(cs *ColorSpace) *ColorSpace {
	cloned := *cs
	cloned.Palette = slices.Clone(cs.Palette)
	return &cloned
}

// cloneFontPtr 深拷贝字体声明定义
// 入参: f 原字体定义
// 返回: *Font 独立字体副本
func cloneFontPtr(f *Font) *Font {
	cloned := *f
	return &cloned
}

// cloneDrawParamPtr 深拷贝绘制参数定义及嵌套颜色结构
// 入参: dp 原绘制参数
// 返回: *DrawParam 独立绘制参数副本
func cloneDrawParamPtr(dp *DrawParam) *DrawParam {
	cloned := cloneEditorData(*dp)
	return &cloned
}

// cloneCompositeUnitPtr 深拷贝复合图元定义及成员切片
// 入参: cgu 原复合图元
// 返回: *CompositeGraphicUnit 独立复合图元副本
func cloneCompositeUnitPtr(cgu *CompositeGraphicUnit) *CompositeGraphicUnit {
	cloned := copyEditorCompositeGraphicUnit(*cgu)
	return &cloned
}

// sameResourceID 判断资源标识是否相同，数字标识按数值比较
// 入参: first 第一个标识, second 第二个标识
// 返回: bool 是否相同
func sameResourceID(first, second string) bool {
	if first == second {
		return true
	}
	id := editorResourceID(first)
	return id != "" && id == editorResourceID(second)
}

// resourceValue 优先查找原标识与标准十进制标识，回退时检查别名冲突
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
		if len(alias) == 0 || alias[0] != '0' || editorResourceID(alias) != key {
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
