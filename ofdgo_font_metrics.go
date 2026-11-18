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
	"encoding/binary"
	"fmt"
	"sync"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// sfntGlyphBoundsLimit 限制单个字体保留的字形与字号边界数量
const sfntGlyphBoundsLimit = 4096

// FontResource 保存独立字体数据、标准元信息与只读度量，不包含绘图库类型
type FontResource struct {
	FontMetrics
	Data      []byte
	Font      Font
	Extension string
	CanSubset bool
}

// FontUsage 描述全包已确认的字体引用，Unsafe禁止裁剪
type FontUsage struct {
	Characters []rune
	Glyphs     []uint16
	Unsafe     bool
}

// FontResourceBackend 解析和裁剪保存资源，独立于显示字体后端
// SubsetFont的preserve为true时不得改变字形编号，SubsetSourceFont无安全收益时返回nil
type FontResourceBackend interface {
	Backend
	OpenFontResource(file FontFile, index int) (*FontResource, error)
	SubsetFont(data []byte, glyphs []uint16, preserve bool) ([]byte, error)
	SubsetSourceFont(data []byte, usage FontUsage) ([]byte, error)
}

// SFNTBackend 提供独立的OpenType字体度量、轮廓和资源处理
type SFNTBackend struct{}

// ResolveFont 按需加载字体资源，保留内嵌数据及外部字体的名称、样式和集合索引匹配
// 入参: r 渲染器, id 字体标识, exact 是否禁止无关回退
// 返回: ResolvedFont 字体与来源, error 字体不可用错误
func (b SFNTBackend) ResolveFont(r *Renderer, id string, exact bool) (ResolvedFont, error) {
	return r.resolveFontSource(b, id, exact)
}

// OpenFont 解析独立SFNT数据，度量与原有编辑排版保持一致
// 入参: data 字体数据
// 返回: FontMetrics 字体度量, error 解析错误
func (SFNTBackend) OpenFont(data []byte) (FontMetrics, error) {
	data, err := basefont.DefaultCFF2Font(data)
	if err != nil {
		return nil, err
	}
	sfnt, err := basefont.ParseSFNT(data)
	if err != nil {
		return nil, err
	}
	metrics := &sfntFontMetrics{SFNT: sfnt, outlines: &basefont.Outliner{Font: sfnt}, bounds: &sfntFontBounds{}}
	metrics.fontShaper = &fontShaper{metrics: metrics}
	return metrics, nil
}

// Name 返回资源后端标识
// 返回: string 后端标识
func (SFNTBackend) Name() string { return "sfnt" }

// OpenFontResource 提取独立字体并读取名称、样式和裁剪能力
// CFF2使用默认实例的静态轮廓，不改变原字体文件
// 入参: file 字体文件, index 集合索引
// 返回: *FontResource 字体资源, error 解析错误
func (SFNTBackend) OpenFontResource(file FontFile, index int) (*FontResource, error) {
	data, err := file.Face(index)
	if err != nil {
		return nil, err
	}
	data, err = basefont.DefaultCFF2Font(data)
	if err != nil {
		return nil, err
	}
	sfnt, err := basefont.ParseSFNT(data)
	if err != nil {
		return nil, err
	}
	name := basefont.SFNTName(sfnt, 6, 4, 1)
	if name == "" {
		return nil, fmt.Errorf("font has no name")
	}
	extension := ".ttf"
	if sfnt.IsCFF {
		extension = ".otf"
	}
	return &FontResource{
		FontMetrics: sfntResourceMetrics{sfnt}, Data: data, Extension: extension,
		Font:      Font{FontName: name, FamilyName: basefont.SFNTName(sfnt, 16, 1), Charset: "unicode", Bold: binary.BigEndian.Uint16(sfnt.Tables["head"][44:])&1 != 0, Italic: binary.BigEndian.Uint16(sfnt.Tables["head"][44:])&2 != 0, Serif: sfntSerif(sfnt.Tables["OS/2"]), FixedWidth: len(sfnt.Tables["post"]) >= 16 && binary.BigEndian.Uint32(sfnt.Tables["post"][12:]) != 0},
		CanSubset: basefont.Subsettable(sfnt),
	}, nil
}

// SubsetFont 裁剪新建字体并按要求保留显式字形编号
// 入参: data 字体数据, glyphs 用字, preserve 是否保留编号
// 返回: []byte 子集, error 裁剪错误
func (SFNTBackend) SubsetFont(data []byte, glyphs []uint16, preserve bool) ([]byte, error) {
	return basefont.SubsetSFNT(data, glyphs, preserve)
}

// SubsetSourceFont 按全包引用裁剪原字体，不确定时保留原资源
// 入参: data 字体数据, usage 用字信息
// 返回: []byte 子集, error 裁剪错误
func (SFNTBackend) SubsetSourceFont(data []byte, usage FontUsage) ([]byte, error) {
	u := &basefont.Usage{Characters: make(map[rune]bool), Glyphs: make(map[uint16]bool), Unsafe: usage.Unsafe}
	for _, char := range usage.Characters {
		u.Characters[char] = true
	}
	for _, glyph := range usage.Glyphs {
		u.Glyphs[glyph] = true
	}
	return basefont.SubsetSourceSFNT(data, u), nil
}

// sfntFontMetrics 保留默认字体度量，写出时不更新原始时间
type sfntFontMetrics struct {
	*basefont.SFNT
	*fontShaper
	outlines *basefont.Outliner
	bounds   *sfntFontBounds
}

// Write 返回字体数据，不修改字体元信息
// 返回: []byte 独立SFNT数据
func (f sfntFontMetrics) Write() []byte {
	return f.SFNT.Write()
}

// GlyphOutline 返回基线原点、纵轴向下的字形轮廓
// 入参: glyph 字形编号, size 字号，单位为毫米
// 返回: GeometryPath 字形路径, error 字形解析错误
func (f sfntFontMetrics) GlyphOutline(glyph uint16, size float64) (GeometryPath, error) {
	if !rasterPositive(size) || glyph >= f.NumGlyphs() {
		return nil, fmt.Errorf("invalid glyph or size")
	}
	path := &glyphGeometry{}
	if err := f.outlines.Path(path, glyph, size/float64(f.UnitsPerEm())); err != nil {
		return nil, err
	}
	return path.path, nil
}

// GlyphBounds 返回字形轮廓的精确边界，重复编号及字号复用有限缓存
// 入参: glyph 字形编号, size 字号，单位为毫米
// 返回: Box 字形边界, error 字形解析错误
func (f sfntFontMetrics) GlyphBounds(glyph uint16, size float64) (Box, error) {
	key := sfntGlyphSize{glyph: glyph, size: size}
	if f.bounds != nil {
		f.bounds.mu.Lock()
		value, ok := f.bounds.values[key]
		f.bounds.mu.Unlock()
		if ok {
			return value, nil
		}
	}
	outline, err := f.GlyphOutline(glyph, size)
	if err != nil {
		return Box{}, err
	}
	value, err := outline.Bounds()
	if err != nil {
		return Box{}, err
	}
	if f.bounds != nil {
		f.bounds.mu.Lock()
		if f.bounds.values == nil || len(f.bounds.values) >= sfntGlyphBoundsLimit {
			f.bounds.values = make(map[sfntGlyphSize]Box)
		}
		f.bounds.values[key] = value
		f.bounds.mu.Unlock()
	}
	return value, nil
}

// GlyphWarning 返回无效字形指令后保留设计轮廓的提示，原始字体不变
// 入参: glyph 字形编号
// 返回: error 恢复原因，无恢复时为空
func (f sfntFontMetrics) GlyphWarning(glyph uint16) error {
	return f.outlines.GlyphWarning(glyph)
}

// GlyphOutlines 批量提取字形轮廓，重复编号只解析一次
// 入参: glyphs 字形编号, size 毫米字号
// 返回: []GeometryPath 同序只读轮廓, error 轮廓错误
func (f sfntFontMetrics) GlyphOutlines(glyphs []uint16, size float64) ([]GeometryPath, error) {
	if !rasterPositive(size) {
		return nil, fmt.Errorf("invalid glyph size")
	}
	result := make([]GeometryPath, len(glyphs))
	paths := make(map[uint16]GeometryPath, len(glyphs))
	for i, glyph := range glyphs {
		path, ok := paths[glyph]
		if !ok {
			var err error
			path, err = f.GlyphOutline(glyph, size)
			if err != nil {
				return nil, err
			}
			paths[glyph] = path
		}
		result[i] = path
	}
	return result, nil
}

// sfntFontBounds 缓存已成功提取的字形边界，不保留轮廓或失败结果
type sfntFontBounds struct {
	mu     sync.Mutex
	values map[sfntGlyphSize]Box
}

// sfntGlyphSize 区分字形编号及精确毫米字号
type sfntGlyphSize struct {
	glyph uint16
	size  float64
}

// glyphGeometry 将字体轮廓转换为纵轴向下的几何路径
type glyphGeometry struct{ path GeometryPath }

// MoveTo 开始子路径
// 入参: x 横坐标, y 纵坐标
func (p *glyphGeometry) MoveTo(x, y float64) {
	p.path = append(p.path, GeometrySegment{Verb: GeometryMove, End: Point{X: x, Y: -y}})
}

// LineTo 添加直线
// 入参: x 横坐标, y 纵坐标
func (p *glyphGeometry) LineTo(x, y float64) {
	p.path = append(p.path, GeometrySegment{Verb: GeometryLine, End: Point{X: x, Y: -y}})
}

// QuadTo 添加二次贝塞尔曲线
// 入参: cx 控制点横坐标, cy 控制点纵坐标, x 终点横坐标, y 终点纵坐标
func (p *glyphGeometry) QuadTo(cx, cy, x, y float64) {
	p.path = append(p.path, GeometrySegment{Verb: GeometryQuad, Control1: Point{X: cx, Y: -cy}, End: Point{X: x, Y: -y}})
}

// CubeTo 添加三次贝塞尔曲线
// 入参: ax 第一控制点横坐标, ay 第一控制点纵坐标, bx 第二控制点横坐标, by 第二控制点纵坐标, x 终点横坐标, y 终点纵坐标
func (p *glyphGeometry) CubeTo(ax, ay, bx, by, x, y float64) {
	p.path = append(p.path, GeometrySegment{Verb: GeometryCubic, Control1: Point{X: ax, Y: -ay}, Control2: Point{X: bx, Y: -by}, End: Point{X: x, Y: -y}})
}

// Close 闭合当前子路径
func (p *glyphGeometry) Close() { p.path = append(p.path, GeometrySegment{Verb: GeometryClose}) }

// sfntResourceMetrics 隔离资源解析器的度量实现
type sfntResourceMetrics struct{ *basefont.SFNT }

// Write 返回未改动元信息的字体数据
// 返回: []byte 字体数据
func (f sfntResourceMetrics) Write() []byte { return f.SFNT.Write() }

// sfntSerif 从OpenType OS/2表家族分类与PANOSE参数识别衬线字体
// 入参: os2 OS/2表字节
// 返回: bool 是否为衬线字体
func sfntSerif(os2 []byte) bool {
	if len(os2) >= 32 {
		switch os2[30] {
		case 1, 2, 3, 4, 5, 7:
			return true
		}
	}
	return len(os2) >= 42 && os2[32] == 2 && os2[33] >= 2 && os2[33] <= 10
}
