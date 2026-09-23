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
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
)

// rasterClipKey 区分页面尺寸、分辨率和裁剪路径
// 入参: page 页面, path 页面坐标路径
// 返回: [32]byte 缓存键
func rasterClipKey(page *RasterPage, path []RasterSegment) [32]byte {
	hash := sha256.New()
	var data [8]byte
	number := func(v float64) {
		binary.LittleEndian.PutUint64(data[:], math.Float64bits(v))
		hash.Write(data[:])
	}
	number(page.Width)
	number(page.Height)
	number(page.DPI)
	for _, s := range path {
		hash.Write([]byte{byte(s.Verb)})
		for _, p := range []RasterPoint{s.End, s.Control1, s.Control2} {
			number(p.X)
			number(p.Y)
		}
	}
	var key [32]byte
	copy(key[:], hash.Sum(nil))
	return key
}

// rasterClipMask 使用所选后端生成区域化透明度蒙版，同页相同裁剪可复用
// 入参: backend 光栅后端, page 页面, path 页面坐标裁剪, cache 蒙版缓存
// 返回: *image.Alpha 页面像素坐标蒙版, error 绘制错误
func rasterClipMask(backend RasterBackend, page *RasterPage, path []RasterSegment, cache *renderCache[[32]byte, *image.Alpha]) (*image.Alpha, error) {
	key := rasterClipKey(page, path)
	if mask, ok := cache.get(key); ok {
		return mask, nil
	}
	w, h, err := page.PixelSize()
	if err != nil {
		return nil, err
	}
	matrix := page.PixelTransform(RasterMatrix{1, 0, 0, 1, 0, 0}, h)
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	include := func(p RasterPoint) {
		p = matrix.Apply(p)
		minX = math.Min(minX, p.X)
		minY = math.Min(minY, p.Y)
		maxX = math.Max(maxX, p.X)
		maxY = math.Max(maxY, p.Y)
	}
	for _, s := range path {
		switch s.Verb {
		case RasterMove, RasterLine:
			include(s.End)
		case RasterQuad:
			include(s.End)
			include(s.Control1)
		case RasterCubic:
			include(s.End)
			include(s.Control1)
			include(s.Control2)
		case RasterClose:
		default:
			return nil, fmt.Errorf("invalid clip path command %d", s.Verb)
		}
	}
	if len(path) == 0 || minX > maxX || minY > maxY {
		return image.NewAlpha(image.Rectangle{}), nil
	}
	if !finite(minX) || !finite(minY) || !finite(maxX) || !finite(maxY) {
		return nil, fmt.Errorf("invalid clip coordinates")
	}
	bounds := image.Rect(int(math.Max(0, math.Min(float64(w), math.Floor(minX)-1))), int(math.Max(0, math.Min(float64(h), math.Floor(minY)-1))), int(math.Max(0, math.Min(float64(w), math.Ceil(maxX)+1))), int(math.Max(0, math.Min(float64(h), math.Ceil(maxY)+1))))
	mask := image.NewAlpha(bounds)
	if !bounds.Empty() {
		scale := page.DPI / 25.4
		local := &RasterPage{Width: float64(bounds.Dx()) / scale, Height: float64(bounds.Dy()) / scale, DPI: page.DPI}
		local.Commands = []RasterCommand{{Path: path, Paint: RasterPaint{Color: color.RGBA{255, 255, 255, 255}}, Transform: RasterMatrix{1, 0, 0, 1, -float64(bounds.Min.X) / scale, (matrix[5] - float64(bounds.Min.Y)) / scale}}}
		pixels, err := backend.Render(local)
		if err != nil {
			return nil, err
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				_, _, _, alpha := pixels.At(x-bounds.Min.X, y-bounds.Min.Y).RGBA()
				mask.SetAlpha(x, y, color.Alpha{uint8(alpha >> 8)})
			}
		}
	}
	cache.put(key, mask, len(mask.Pix)+256)
	return mask, nil
}
