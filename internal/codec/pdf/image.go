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
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// StencilImage 按当前填充色读取模板图像，不分配整幅彩色样本
type StencilImage struct {
	Source image.Image
	Fill   color.NRGBA64
}

// NativeImage 按源像素输出原生过程色，不分配完整显示颜色缓冲
type NativeImage struct {
	Source *pdfgo.ImageProcess
	Space  *pdfgo.ColorSpace
}

// ColorModel 保留填充色和模板的有效采样精度
// 返回: color.Model 非预乘颜色模型
func (s *StencilImage) ColorModel() color.Model {
	model := s.Source.ColorModel()
	if s.Fill.R%257 == 0 && s.Fill.G%257 == 0 && s.Fill.B%257 == 0 && (model == color.GrayModel || model == color.NRGBAModel || model == color.RGBAModel) {
		return color.NRGBAModel
	}
	return color.NRGBA64Model
}

// Bounds 返回模板图像的采样边界
// 返回: image.Rectangle 图像边界
func (s *StencilImage) Bounds() image.Rectangle { return s.Source.Bounds() }

// At 以模板灰度计算填充覆盖率，不重复变换颜色
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 非预乘颜色
func (s *StencilImage) At(x, y int) color.Color {
	return s.NRGBA64At(x, y)
}

// NRGBA64At 直接读取模板覆盖率，保留填充色的有效精度
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.NRGBA64 非预乘颜色
func (s *StencilImage) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.Bounds()) {
		return color.NRGBA64{}
	}
	gray := baseimage.NRGBA64At(s.Source, x, y)
	pixel := s.Fill
	pixel.A = uint16(65535 - gray.R)
	return pixel
}

// ColorModel 保留16位原生分量与源透明度
// 返回: color.Model 非预乘颜色模型
func (s *NativeImage) ColorModel() color.Model { return color.NRGBA64Model }

// Bounds 返回源图像的完整采样边界
// 返回: image.Rectangle 原始像素边界
func (s *NativeImage) Bounds() image.Rectangle { return s.Source.Bounds() }

// At 获取按OFD输出映射的原生颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 非预乘颜色
func (s *NativeImage) At(x, y int) color.Color { return s.NRGBA64At(x, y) }

// NRGBA64At 按原始精度转换设备过程色，不进行页面采样或备用变换
// 入参: x 横坐标, y 纵坐标
// 返回: color.NRGBA64 非预乘颜色
func (s *NativeImage) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.Bounds()) {
		return color.NRGBA64{}
	}
	values, alpha := s.Source.ValuesAt(x, y)
	rgb, _ := OutputColor(s.Space, values)
	return color.NRGBA64{R: uint16(math.Round(rgb[0] * 65535)), G: uint16(math.Round(rgb[1] * 65535)), B: uint16(math.Round(rgb[2] * 65535)), A: uint16(math.Round(alpha * 65535))}
}

// EncodeCMYKTIFF 将直接四色图像保存为十六位无损TIFF，不经过显示颜色转换
// 入参: ctx 取消上下文, source PDF源图像
// 返回: []byte 含ICC和非预乘透明度的TIFF，非四色图像为nil, error 解码或编码错误
func EncodeCMYKTIFF(ctx context.Context, source *pdfgo.Image) ([]byte, error) {
	if ctx == nil || source == nil {
		return nil, fmt.Errorf("missing CMYK image context or source")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source.ImageMask {
		return nil, nil
	}
	family, err := source.ColorSpaceFamily()
	if err != nil || family != "DeviceCMYK" && family != "ICCBased" {
		return nil, err
	}
	if family == "ICCBased" {
		count, err := source.ColorSpaceComponents()
		if err != nil || count != 4 {
			return nil, err
		}
	}
	components, err := source.DecodeComponentsViewContext(ctx)
	if err != nil {
		return nil, err
	}
	if components.Space == nil || components.Space.Model != "DeviceCMYK" || components.Space.Components() != 4 || components.ColorantSpace != nil {
		return nil, nil
	}
	bounds := components.Rect
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 || width > int(^uint(0)>>1)/10 || uint64(width) > uint64(^uint32(0))/10 || uint64(height) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("invalid CMYK TIFF dimensions")
	}
	profile := components.Space.ICCProfile(source.Intent)
	count := 12
	if len(profile) != 0 {
		count++
	}
	bitsOffset := uint64(8 + 2 + count*12 + 4)
	profileOffset := bitsOffset + 10
	stripOffset := (profileOffset + uint64(len(profile)) + 1) &^ uint64(1)
	if stripOffset > uint64(^uint32(0)) {
		return nil, fmt.Errorf("CMYK TIFF profile exceeds size limit")
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	row := make([]byte, width*10)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)%1024 == 0 {
				if err := ctx.Err(); err != nil {
					writer.Close()
					return nil, err
				}
			}
			values, alpha := components.ValuesAt(x, y)
			pixel := row[(x-bounds.Min.X)*10:]
			for channel, value := range [5]float64{values[0], values[1], values[2], values[3], alpha} {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
					writer.Close()
					return nil, fmt.Errorf("invalid CMYK TIFF component")
				}
				binary.LittleEndian.PutUint16(pixel[channel*2:], uint16(math.Round(value*65535)))
			}
		}
		if _, err := writer.Write(row); err != nil {
			writer.Close()
			return nil, err
		}
		if stripOffset+uint64(compressed.Len()) > uint64(^uint32(0)) {
			writer.Close()
			return nil, fmt.Errorf("CMYK TIFF exceeds size limit")
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	total := stripOffset + uint64(compressed.Len())
	if total > uint64(^uint32(0)) || total > uint64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("CMYK TIFF exceeds size limit")
	}
	data := make([]byte, int(total))
	copy(data, "II")
	binary.LittleEndian.PutUint16(data[2:], 42)
	binary.LittleEndian.PutUint32(data[4:], 8)
	binary.LittleEndian.PutUint16(data[8:], uint16(count))
	entries := [][4]uint32{
		{256, 4, 1, uint32(width)}, {257, 4, 1, uint32(height)},
		{258, 3, 5, uint32(bitsOffset)}, {259, 3, 1, 8}, {262, 3, 1, 5},
		{273, 4, 1, uint32(stripOffset)}, {277, 3, 1, 5}, {278, 4, 1, uint32(height)},
		{279, 4, 1, uint32(compressed.Len())}, {284, 3, 1, 1}, {332, 3, 1, 1}, {338, 3, 1, 2},
	}
	if len(profile) != 0 {
		entries = append(entries, [4]uint32{34675, 7, uint32(len(profile)), uint32(profileOffset)})
	}
	for i, entry := range entries {
		p := data[10+i*12:]
		binary.LittleEndian.PutUint16(p, uint16(entry[0]))
		binary.LittleEndian.PutUint16(p[2:], uint16(entry[1]))
		binary.LittleEndian.PutUint32(p[4:], entry[2])
		binary.LittleEndian.PutUint32(p[8:], entry[3])
	}
	for i := range 5 {
		binary.LittleEndian.PutUint16(data[int(bitsOffset)+2*i:], 16)
	}
	copy(data[int(profileOffset):], profile)
	copy(data[int(stripOffset):], compressed.Bytes())
	return data, nil
}

// ImageProcessColorants 识别可在四色设备上直接绘制的专色色料
// 入参: image PDF图像
// 返回: [4]bool 参与绘制的四色通道, bool 是否全部属于印刷原色, error 色空间错误
func ImageProcessColorants(image *pdfgo.Image) ([4]bool, bool, error) {
	source, err := image.ColorantSpace()
	if err != nil {
		return [4]bool{}, false, err
	}
	space := &pdfgo.ColorSpace{Model: "DeviceCMYK"}
	process := source.PrepareProcess(&pdfgo.ColorantDevice{Space: space}, space, false)
	mask := process.Mask()
	return mask, mask != ([4]bool{}), nil
}
