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

package pdf

import (
	"context"
	"fmt"
	"image"

	"github.com/xiaoqidun/pdfgo"
)

// compositeCancelInterval 保持像素合成的取消检查间隔
const compositeCancelInterval = 256

// CompositePixel 保存非预乘源空间分量及累计透明度
type CompositePixel struct {
	Values [4]float64
	Alpha  float64
	Effect float64
	Shape  float64
}

// TransferCache 在页面内复用设备传递函数，零值可用
// 缓存与所属阅读器同生命周期，不用于并发解析
type TransferCache struct {
	functions map[transferKey]*pdfgo.TransferFunction
}

// transferKey 区分图形状态、网屏覆盖函数及输出设备
type transferKey struct {
	transfer *pdfgo.TransferFunction
	halftone *pdfgo.Halftone
	model    pdfgo.Name
}

// OutputColor 将合成结果映射到OFD输出，与保留的设备四色对象使用相同显示换算
// 入参: space 合成空间, values 非预乘分量
// 返回: [3]float64 显示颜色, error 校准颜色转换错误
func OutputColor(space *pdfgo.ColorSpace, values [4]float64) ([3]float64, error) {
	return space.SRGB(values[:space.Components()], "RelativeColorimetric")
}

// NormalBlend 判断混合模式是否为普通覆盖
// 入参: mode 混合模式
// 返回: bool 是否普通覆盖
func NormalBlend(mode pdfgo.Name) bool {
	return mode == "" || mode == "Normal" || mode == "Compatible"
}

// NativePaint 使用库层色料定义求值设备四色，不以备用色代替原生浓度
// 入参: paint 源画刷, space 设备四色空间, intent 渲染意图
// 返回: *pdfgo.Paint 原生画刷或nil, [4]bool 显式通道, error 色料定义错误
func NativePaint(paint pdfgo.Paint, space *pdfgo.ColorSpace, intent pdfgo.Name) (*pdfgo.Paint, [4]bool, error) {
	if paint.Colorant == nil {
		return nil, [4]bool{}, nil
	}
	result, err := paint.Colorant.Space.ResolveInGroup(paint.Colorant.Tints, &pdfgo.ColorantDevice{Space: space}, space, intent, false)
	if err != nil || len(result.Alternates) != 0 {
		return nil, [4]bool{}, err
	}
	return result.Process, result.ProcessMask, nil
}

// CompositeColor 将原始画刷变换到混合空间，渐变在源空间求值
// 入参: paint 画刷, point 页面坐标, space 混合空间, intent 渲染意图, conversion 设备转换函数
// 返回: [4]float64 混合分量, bool 是否着色, error 颜色错误
func CompositeColor(paint pdfgo.Paint, point pdfgo.Point, space *pdfgo.ColorSpace, intent pdfgo.Name, conversion pdfgo.ColorConversion) ([4]float64, bool, error) {
	if paint.Shading != nil {
		return [4]float64{}, false, &pdfgo.UnsupportedError{Feature: "local shading pattern compositing"}
	}
	if paint.Tiling != nil {
		return [4]float64{}, false, &pdfgo.UnsupportedError{Feature: "local tiling pattern compositing"}
	}
	var values [4]float64
	var source *pdfgo.ColorSpace
	if paint.Function != nil {
		g := paint.Function
		if g.Bounds != nil {
			inverse, ok := g.PatternMatrix.Inverse()
			if !ok {
				return values, false, fmt.Errorf("singular function shading bounds transform")
			}
			local := inverse.Apply(point)
			if local.X < g.Bounds.XMin || local.X > g.Bounds.XMax || local.Y < g.Bounds.YMin || local.Y > g.Bounds.YMax {
				return values, false, nil
			}
		}
		inverse, ok := g.Matrix.Inverse()
		if !ok {
			return values, false, fmt.Errorf("singular function shading transform")
		}
		local := inverse.Apply(point)
		source, intent = g.Space, g.Intent
		if local.X < g.Domain.XMin || local.X > g.Domain.XMax || local.Y < g.Domain.YMin || local.Y > g.Domain.YMax {
			if g.Background == nil {
				return values, false, nil
			}
			values = *g.Background
		} else {
			var err error
			values, err = g.ValuesAt(local)
			if err != nil {
				return values, false, err
			}
		}
	} else if paint.Axial != nil || paint.Radial != nil {
		var background *[4]float64
		var bounds *pdfgo.Rectangle
		var matrix pdfgo.Matrix
		if g := paint.Axial; g != nil {
			source, intent = g.Space, g.Intent
			background, bounds, matrix = g.Background, g.Bounds, g.Matrix
		} else {
			g := paint.Radial
			source, intent = g.Space, g.Intent
			background, bounds, matrix = g.Background, g.Bounds, g.Matrix
		}
		if bounds != nil {
			local := point
			if matrix != (pdfgo.Matrix{}) {
				inverse, ok := matrix.Inverse()
				if !ok {
					return values, false, fmt.Errorf("singular shading transform")
				}
				local = inverse.Apply(point)
			}
			if local.X < bounds.XMin || local.X > bounds.XMax || local.Y < bounds.YMin || local.Y > bounds.YMax {
				return values, false, nil
			}
		}
		t, visible := shadingPosition(paint, point)
		var err error
		if !visible {
			if background == nil {
				return values, false, nil
			}
			values = *background
		} else if paint.Axial != nil {
			values, err = paint.Axial.ValuesAt(t)
		} else {
			values, err = paint.Radial.ValuesAt(t)
		}
		if err != nil {
			return values, false, err
		}
	} else if paint.Space != nil {
		values, source = paint.Values, paint.Space
	} else if paint.CMYK != nil {
		values, source = *paint.CMYK, &pdfgo.ColorSpace{Model: "DeviceCMYK"}
	} else if paint.SourceSpace == "DeviceGray" {
		values[0], source = paint.RGB[0], &pdfgo.ColorSpace{Model: "DeviceGray"}
	} else {
		copy(values[:], paint.RGB[:])
		source = &pdfgo.ColorSpace{Model: "DeviceRGB"}
	}
	values, err := space.ConvertWith(values[:source.Components()], source, intent, conversion)
	return values, true, err
}

// CompositeInterpolate 按形状覆盖混合预乘颜色及累计透明度
// 入参: target 原像素与输出, source 替换像素, shape 覆盖率
// 返回: error 像素或覆盖率错误
func CompositeInterpolate(target *CompositePixel, source CompositePixel, shape float64) error {
	if shape >= 0 && shape <= 1 && target.Alpha >= 0 && target.Alpha <= 1 && target.Shape >= 0 && target.Shape <= 1 && target.Effect >= 0 && target.Effect <= 1 && source.Alpha >= 0 && source.Alpha <= 1 && source.Shape >= 0 && source.Shape <= 1 && source.Effect >= 0 && source.Effect <= 1 {
		invShape := 1 - shape
		tAlpha, sAlpha := target.Alpha, source.Alpha
		alpha := tAlpha*invShape + sAlpha*shape
		if alpha != 0 {
			var values [4]float64
			for c := range 4 {
				var tp, sp float64
				if tAlpha != 0 {
					v := target.Values[c]
					if !(v >= 0 && v <= 1) {
						goto slow
					}
					tp = v * tAlpha
				}
				if sAlpha != 0 {
					v := source.Values[c]
					if !(v >= 0 && v <= 1) {
						goto slow
					}
					sp = v * sAlpha
				}
				values[c] = (tp*invShape + sp*shape) / alpha
			}
			target.Values = values
		}
		target.Alpha = alpha
		target.Effect = target.Effect*invShape + source.Effect*shape
		return nil
	}
slow:
	pixel := pdfgo.ColorantPixel{Values: target.Values[:], Alpha: target.Alpha, Shape: target.Shape, Effect: target.Effect}
	if err := pixel.Interpolate(pdfgo.ColorantPixel{Values: source.Values[:], Alpha: source.Alpha, Shape: source.Shape, Effect: source.Effect}, shape); err != nil {
		return err
	}
	target.Alpha, target.Effect = pixel.Alpha, pixel.Effect
	return nil
}

// CompositeKnockout 移除未覆盖区域的初始背景贡献，保留形状与不透明度差异
// 入参: target 累计组结果, initial 初始背景, source 单个对象合成结果
// 返回: error 像素错误
func CompositeKnockout(target *CompositePixel, initial, source CompositePixel) error {
	pixel := pdfgo.ColorantPixel{Values: target.Values[:], Alpha: target.Alpha, Shape: target.Shape, Effect: target.Effect}
	if err := pixel.Knockout(
		pdfgo.ColorantPixel{Values: initial.Values[:], Alpha: initial.Alpha, Shape: initial.Shape, Effect: initial.Effect},
		pdfgo.ColorantPixel{Values: source.Values[:], Alpha: source.Alpha, Shape: source.Shape, Effect: source.Effect},
	); err != nil {
		return err
	}
	target.Alpha, target.Effect, target.Shape = pixel.Alpha, pixel.Effect, pixel.Shape
	return nil
}

// CompositeKnockoutFast 按ISO 32000-1挖空公式合并单像素，仅对非法数值回退完整校验
// 入参: target 累计组结果, initial 初始背景, source 单个对象合成结果
// 返回: error 像素错误
func CompositeKnockoutFast(target *CompositePixel, initial, source CompositePixel) error {
	f := source.Shape
	if f == 0 {
		return nil
	}
	if !(f >= 0 && f <= 1 && target.Alpha >= 0 && target.Alpha <= 1 && target.Shape >= 0 && target.Shape <= 1 && target.Effect >= 0 && target.Effect <= 1 && initial.Alpha >= 0 && initial.Alpha <= 1 && initial.Shape >= 0 && initial.Shape <= 1 && initial.Effect >= 0 && initial.Effect <= 1 && source.Alpha >= 0 && source.Alpha <= 1 && source.Effect >= 0 && source.Effect <= 1) {
		return CompositeKnockout(target, initial, source)
	}
	invF := 1 - f
	tAlpha, iAlpha, sAlpha := target.Alpha, initial.Alpha, source.Alpha
	alpha := tAlpha*invF + sAlpha - iAlpha*invF
	if alpha > 0 {
		var values [4]float64
		for c := range 4 {
			var tp, ip, sp float64
			if tAlpha != 0 {
				v := target.Values[c]
				if !(v >= 0 && v <= 1) {
					return CompositeKnockout(target, initial, source)
				}
				tp = v * tAlpha
			}
			if iAlpha != 0 {
				v := initial.Values[c]
				if !(v >= 0 && v <= 1) {
					return CompositeKnockout(target, initial, source)
				}
				ip = v * iAlpha
			}
			if sAlpha != 0 {
				v := source.Values[c]
				if !(v >= 0 && v <= 1) {
					return CompositeKnockout(target, initial, source)
				}
				sp = v * sAlpha
			}
			out := (tp*invF + sp - ip*invF) / alpha
			if out < 0 {
				out = 0
			} else if out > 1 {
				out = 1
			}
			values[c] = out
		}
		target.Values = values
	}
	if alpha < 0 {
		alpha = 0
	} else if alpha > 1 {
		alpha = 1
	}
	target.Alpha = alpha
	target.Effect = target.Effect*invF + source.Effect
	target.Shape += f * (1 - target.Shape)
	return nil
}

// CompositeGroupRegion 在组覆盖区域合成像素，保留原栅格及形状贡献
// 入参: ctx 取消上下文, pixels 父组输出, initial 初始背景, result 组结果, stride 行跨度, region 覆盖区域, space 组空间, parent 父空间, opacity 组不透明度, mask 蒙版, mode 混合模式, intent 渲染意图, conversion 设备转换函数
// 返回: error 缓冲、区域、颜色或混合错误
func CompositeGroupRegion(ctx context.Context, pixels, initial, result []CompositePixel, stride int, region image.Rectangle, space, parent *pdfgo.ColorSpace, opacity float64, mask []float64, mode, intent pdfgo.Name, conversion pdfgo.ColorConversion) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pixels) != len(initial) || len(pixels) != len(result) || mask != nil && len(mask) != len(pixels) {
		return fmt.Errorf("invalid PDF transparency group buffers")
	}
	if stride <= 0 || region.Min.X < 0 || region.Min.Y < 0 || region.Max.X > stride || !region.Empty() && region.Max.Y > len(pixels)/stride {
		return fmt.Errorf("invalid PDF transparency group region")
	}
	group, destination := pdfgo.ColorantGroup{Space: space}, pdfgo.ColorantGroup{Space: parent}
	compositor, err := destination.PrepareGroup(&group, mode, intent, conversion)
	if err != nil {
		return err
	}
	if region.Empty() {
		return nil
	}
	components, parentComponents := space.Components(), parent.Components()
	var target pdfgo.ColorantPixel
	var previous, previousInitial, previousResult, composed CompositePixel
	var previousAlpha float64
	ready := false
	for y := region.Min.Y; y < region.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for i, end := y*stride+region.Min.X, y*stride+region.Max.X; i < end; i++ {
			if i%compositeCancelInterval == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			alpha := opacity
			if mask != nil {
				alpha *= mask[i]
			}
			pixel := &result[i]
			if ready && pixels[i] == previous && initial[i] == previousInitial && *pixel == previousResult && alpha == previousAlpha {
				pixels[i] = composed
				continue
			}
			previous, previousInitial, previousResult, previousAlpha = pixels[i], initial[i], *pixel, alpha
			target.Values = pixels[i].Values[:parentComponents]
			target.Alpha = pixels[i].Alpha
			target.Shape = pixels[i].Shape
			target.Effect = pixels[i].Effect
			if err := compositor.Composite(&target,
				pdfgo.ColorantPixel{Values: initial[i].Values[:components], Alpha: initial[i].Alpha, Shape: initial[i].Shape, Effect: initial[i].Effect},
				pdfgo.ColorantPixel{Values: pixel.Values[:components], Alpha: pixel.Alpha, Shape: pixel.Shape, Effect: pixel.Effect},
				alpha,
			); err != nil {
				return err
			}
			pixels[i].Alpha, pixels[i].Effect, pixels[i].Shape = target.Alpha, target.Effect, target.Shape
			composed, ready = pixels[i], true
		}
	}
	return nil
}

// CompositePaintOver 以兼容叠印计算零色料分量，再应用对象混合模式
// 入参: backdrop 背景及输出, source 源分量, alpha 源透明度, space 混合空间, mode 混合模式, compatible 是否保留零色料背景, marked 原生过程通道，nil使用普通空间
// 返回: error 未支持的混合模式
func CompositePaintOver(backdrop *CompositePixel, source [4]float64, alpha float64, space *pdfgo.ColorSpace, mode pdfgo.Name, compatible bool, marked *[4]bool) error {
	if !compatible && marked == nil {
		return compositeOver(backdrop, source, alpha, space, mode)
	}
	if alpha == 0 {
		return nil
	}
	mask := PaintChannels(source, compatible, marked)
	values, combined, err := space.CompositeOverprint(backdrop.Values[:space.Components()], source[:space.Components()], backdrop.Alpha, alpha, mode, mask[:space.Components()])
	if err != nil {
		return err
	}
	copy(backdrop.Values[:space.Components()], values[:space.Components()])
	backdrop.Alpha = combined
	backdrop.Effect = alpha + backdrop.Effect*(1-alpha)
	return nil
}

// compositeOver 按PDF透明模型合成源颜色，四色混合使用分量补色
// 入参: backdrop 背景及输出, source 源分量, alpha 源透明度, space 混合空间, mode 混合模式
// 返回: error 未支持的混合模式
func compositeOver(backdrop *CompositePixel, source [4]float64, alpha float64, space *pdfgo.ColorSpace, mode pdfgo.Name) error {
	if alpha == 0 {
		return nil
	}
	if NormalBlend(mode) && alpha > 0 && alpha <= 1 && backdrop.Alpha >= 0 && backdrop.Alpha <= 1 {
		components := space.Components()
		if components > 0 && components <= 4 {
			ba := backdrop.Alpha
			if ba == 0 || alpha == 1 {
				for i := range components {
					v := source[i]
					if !(v >= 0 && v <= 1) {
						goto slow
					}
				}
				for i := range components {
					backdrop.Values[i] = source[i]
				}
				backdrop.Alpha = alpha
				backdrop.Effect = alpha + backdrop.Effect*(1-alpha)
				return nil
			}
			invA := 1 - alpha
			combined := alpha + ba*invA
			wB := ba * invA / combined
			wS := alpha / combined
			for i := range components {
				bv, sv := backdrop.Values[i], source[i]
				if !(bv >= 0 && bv <= 1 && sv >= 0 && sv <= 1) {
					goto slow
				}
			}
			for i := range components {
				backdrop.Values[i] = backdrop.Values[i]*wB + source[i]*wS
			}
			backdrop.Alpha = combined
			backdrop.Effect = alpha + backdrop.Effect*invA
			return nil
		}
	}
slow:
	values, combined, err := space.Composite(backdrop.Values[:space.Components()], source[:space.Components()], backdrop.Alpha, alpha, mode)
	if err != nil {
		return err
	}
	copy(backdrop.Values[:space.Components()], values[:space.Components()])
	backdrop.Alpha = combined
	backdrop.Effect = alpha + backdrop.Effect*(1-alpha)
	return nil
}

// PaintChannels 选择兼容叠印实际使用源颜色的通道
// 入参: values 源四色分量, compatible 是否保留零分量背景, marked 指定通道，nil表示全部
// 返回: [4]bool 各通道是否取源颜色
func PaintChannels(values [4]float64, compatible bool, marked *[4]bool) [4]bool {
	channels := [4]bool{true, true, true, true}
	if marked != nil {
		channels = *marked
	}
	if compatible {
		for i := range channels {
			channels[i] = channels[i] && values[i] != 0
		}
	}
	return channels
}

// OpaqueBlack 判断不受底层分色影响的全黑设备色
// 入参: paint PDF颜色与不透明度
// 返回: bool 是否为不透明全黑CMYK颜色
func OpaqueBlack(paint pdfgo.Paint) bool {
	return paint.Alpha == 1 && paint.CMYK != nil && paint.CMYK[3] == 1
}

// OverprintNeedsSeparation 判断彩色CMYK套印是否依赖分色输出
// 入参: paint PDF颜色与不透明度
// 返回: bool 是否需要保留分色语义
func OverprintNeedsSeparation(paint pdfgo.Paint) bool {
	_, process := paint.Process.CMYKMask()
	return (paint.CMYK != nil || process) && !OpaqueBlack(paint)
}

// Resolve 在页面内复用输出设备的有效函数，不修改原始图形状态
// 入参: reader 原始阅读器, style 原始图形状态, model 原生设备颜色空间
// 返回: *pdfgo.TransferFunction 有效函数, error 网屏传递函数错误
func (c *TransferCache) Resolve(reader *pdfgo.Reader, style pdfgo.Style, model pdfgo.Name) (*pdfgo.TransferFunction, error) {
	if style.Halftone == nil && style.Transfer == nil {
		return nil, nil
	}
	if model == "" {
		model = "DeviceRGB"
	}
	if c.functions == nil {
		c.functions = make(map[transferKey]*pdfgo.TransferFunction)
	}
	key := transferKey{style.Transfer, style.Halftone, model}
	transfer, found := c.functions[key]
	if !found {
		var err error
		transfer, err = reader.DeviceTransfer(style.Transfer, style.Halftone, model)
		if err != nil {
			return nil, err
		}
		c.functions[key] = transfer
	}
	return transfer, nil
}
