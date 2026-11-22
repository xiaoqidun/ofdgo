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
	"math"
	"slices"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// colorProfileKey 按颜色模型与包内路径标识ICC解析缓存
type colorProfileKey struct {
	kind string
	name string
}

// colorProfile 保存包内配置的不可变解析结果，不跨阅读器或文档共享
type colorProfile struct {
	space *pdfgo.ColorSpace
	err   error
}

// parseColorProfile 验证OFD模型与ICC源模型一致，不以设备色替代声明的配置
// 入参: kind OFD颜色模型, data ICC文件
// 返回: *colorProfile 解析结果, error 模型或配置错误
func parseColorProfile(kind string, data []byte) (*colorProfile, error) {
	model := map[string]string{"GRAY": "GRAY", "RGB": "RGB ", "CMYK": "CMYK"}[strings.ToUpper(kind)]
	if len(data) < 132 || model == "" || string(data[16:20]) != model {
		return nil, fmt.Errorf("ICC source model does not match OFD color space %q", kind)
	}
	space, err := pdfgo.NewICCColorSpace(data)
	if err != nil {
		return nil, err
	}
	return &colorProfile{space: space}, nil
}

// displayColor 按源模型及配置转换显示颜色，保持源分量精度
// 入参: kind 源模型, values 归一化分量, profile 可选ICC配置
// 返回: [3]float64 sRGB分量, error 模型或转换错误
func displayColor(kind string, values [4]float64, profile *colorProfile) ([3]float64, error) {
	if profile != nil {
		return profile.srgb(values)
	}
	switch kind {
	case "GRAY":
		return [3]float64{values[0], values[0], values[0]}, nil
	case "RGB":
		return [3]float64{values[0], values[1], values[2]}, nil
	case "CMYK":
		return pdfgo.DeviceCMYKToSRGB(values[0], values[1], values[2], values[3]), nil
	default:
		return [3]float64{}, fmt.Errorf("unsupported color space %q", kind)
	}
}

// profileColor 将显示分量一次量化并按透明度预乘
// 入参: rgb sRGB分量, alpha 非预乘透明度
// 返回: color.RGBA 预乘颜色
func profileColor(rgb [3]float64, alpha float64) color.RGBA {
	alpha = math.Max(0, math.Min(1, alpha))
	return color.RGBA{meshByte(rgb[0] * alpha), meshByte(rgb[1] * alpha), meshByte(rgb[2] * alpha), meshByte(alpha)}
}

// srgb 转换归一化源分量，不在ICC变换前量化为8位
// 入参: values 源分量
// 返回: [3]float64 sRGB分量, error 变换错误
func (p *colorProfile) srgb(values [4]float64) ([3]float64, error) {
	return p.space.SRGB(values[:p.space.Components()], p.space.DefaultIntent())
}

// colorProfileUsed 判断新增ICC文件是否被颜色空间引用，不作为资源XML登记
// 入参: spaces 当前使用的新增颜色空间
// 返回: bool 是否保留文件
func (r editorResource) colorProfileUsed(spaces []ColorSpace) bool {
	return r.space != nil && r.name != "" && r.space.Profile != "" && slices.ContainsFunc(spaces, func(space ColorSpace) bool { return space.Profile == r.space.Profile })
}

// colorProfile 按文件及模型在阅读器内复用只读配置，不修改共享的颜色空间定义
// 入参: space 颜色空间
// 返回: *colorProfile 配置，未声明时为nil, error 读取或配置错误
func (r *Reader) colorProfile(space *ColorSpace) (*colorProfile, error) {
	if space == nil {
		return nil, nil
	}
	if space.profile != nil {
		return space.profile, space.profile.err
	}
	if space.Profile == "" {
		return nil, nil
	}
	if r == nil {
		return nil, fmt.Errorf("color profile %q is unavailable", space.Profile)
	}
	key := colorProfileKey{kind: strings.ToUpper(space.Type), name: r.ResPath(space.Profile)}
	if cached := r.colorProfiles[key]; cached != nil {
		return cached, cached.err
	}
	for _, other := range r.colorSpaceCache {
		if other != nil && other.profile != nil && strings.EqualFold(other.Type, space.Type) && r.ResPath(other.Profile) == key.name {
			if r.colorProfiles == nil {
				r.colorProfiles = make(map[colorProfileKey]*colorProfile)
			}
			r.colorProfiles[key] = other.profile
			return other.profile, other.profile.err
		}
	}
	var parsed *colorProfile
	data, err := r.readFile(key.name)
	if err == nil {
		parsed, err = parseColorProfile(space.Type, data)
	}
	if err != nil {
		parsed = &colorProfile{err: fmt.Errorf("color profile %q: %w", space.Profile, err)}
	}
	if r.colorProfiles == nil {
		r.colorProfiles = make(map[colorProfileKey]*colorProfile)
	}
	r.colorProfiles[key] = parsed
	return parsed, parsed.err
}

// colorDefinition 获取页面局部优先或文档默认颜色空间，统一资源标识别名
// 入参: id 颜色空间标识
// 返回: *ColorSpace 已加载定义，未声明时为nil
func (r *Renderer) colorDefinition(id string) *ColorSpace {
	if r == nil || r.reader == nil {
		return nil
	}
	if id == "" && r.reader.doc != nil {
		id = formatUnitID(r.reader.doc.CommonData.DefaultCS)
	}
	space, _ := r.reader.resolveColorSpaceScoped(r.pageResources, id)
	return space
}
