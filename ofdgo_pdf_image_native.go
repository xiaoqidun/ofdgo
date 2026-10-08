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
	"image"
	"image/color"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// pdfNativeImage 按源像素输出原生过程色，不分配完整显示颜色缓冲
type pdfNativeImage struct {
	source *pdfgo.ImageProcess
	space  *pdfgo.ColorSpace
}

// imageProcessModel 选择与页面原生过程色一致的图像输出，已等价的备用色沿用现有编码路径
// 入参: source 源图像
// 返回: pdfgo.Name 原生输出空间，无需重映射时为空, error 色料解析错误
func (p *pdfImporter) imageProcessModel(source *pdfgo.Image) (pdfgo.Name, error) {
	space := p.compositeSpace
	if source.ImageMask || !space.Device() {
		return "", nil
	}
	colorants, err := source.ColorantSpace()
	if err != nil {
		return "", err
	}
	process := colorants.PrepareProcess(&pdfgo.ColorantDevice{Space: space}, space, false)
	if process == nil || process.Mask() == ([4]bool{}) || process.AlternateEquivalent() {
		return "", nil
	}
	return space.Model, nil
}

// imageProcess 复用源分量并保留原始分辨率，颜色变换随编码逐像素执行
// 入参: source 源图像, model 原生设备空间
// 返回: image.Image 只读图像, error 解码或分量映射错误
func (p *pdfImporter) imageProcess(source *pdfgo.Image, model pdfgo.Name) (image.Image, error) {
	cache := p.compositingCache()
	space := &pdfgo.ColorSpace{Model: model}
	process, err := cache.imageProcess(p, source, space, false)
	if err != nil {
		return nil, err
	}
	if process == nil {
		return nil, fmt.Errorf("invalid native PDF image process space %s", model)
	}
	return &pdfNativeImage{source: process, space: space}, nil
}

// imageProcess 复用同组原生采样，仅在需要备用颜色时保留完整分量
// 入参: importer 导入器, source 源图像, space 组混合空间, softMask 是否用于软蒙版
// 返回: *pdfgo.ImageProcess 原生采样，需要备用色时为nil, error 解码或布局错误
func (c *pdfCompositeCache) imageProcess(importer *pdfImporter, source *pdfgo.Image, space *pdfgo.ColorSpace, softMask bool) (*pdfgo.ImageProcess, error) {
	key := pdfImageProcessKey{image: source, space: space, softMask: softMask}
	if process, found := c.imageProcesses[key]; found {
		return process, nil
	}
	device := &pdfgo.ColorantDevice{Space: space}
	var process *pdfgo.ImageProcess
	var err error
	if components := c.images[source]; components != nil {
		process, err = components.PrepareProcess(device, space, softMask)
	} else {
		process, err = source.DecodeProcessContext(importer.ctx, device, space, softMask)
	}
	if err != nil {
		return nil, err
	}
	if c.imageProcesses == nil {
		c.imageProcesses = make(map[pdfImageProcessKey]*pdfgo.ImageProcess)
	}
	c.imageProcesses[key] = process
	return process, nil
}

// ColorModel 保留16位原生分量与源透明度
// 返回: color.Model 非预乘颜色模型
func (s *pdfNativeImage) ColorModel() color.Model { return color.NRGBA64Model }

// Bounds 返回源图像的完整采样边界
// 返回: image.Rectangle 原始像素边界
func (s *pdfNativeImage) Bounds() image.Rectangle { return s.source.Bounds() }

// At 获取按OFD输出映射的原生颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 非预乘颜色
func (s *pdfNativeImage) At(x, y int) color.Color { return s.NRGBA64At(x, y) }

// NRGBA64At 按原始精度转换设备过程色，不进行页面采样或备用变换
// 入参: x 横坐标, y 纵坐标
// 返回: color.NRGBA64 非预乘颜色
func (s *pdfNativeImage) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.Bounds()) {
		return color.NRGBA64{}
	}
	values, alpha := s.source.ValuesAt(x, y)
	rgb, _ := pdfCompositeOutputColor(s.space, values)
	return color.NRGBA64{R: uint16(math.Round(rgb[0] * 65535)), G: uint16(math.Round(rgb[1] * 65535)), B: uint16(math.Round(rgb[2] * 65535)), A: uint16(math.Round(alpha * 65535))}
}
