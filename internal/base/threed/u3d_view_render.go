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

package threed

import (
	"fmt"
	"image"
	"math"
)

// u3dTextureSample 在像素中心进行双线性采样，使用预乘颜色避免透明边缘色晕
// 入参: texture 纹理, x 横向像素坐标, y 纵向像素坐标
// 返回: [4]float64 预乘颜色及透明度
func u3dTextureSample(texture *image.NRGBA64, x, y float64) [4]float64 {
	x, y = x-.5, y-.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	wx, wy := [2]float64{1 - fx, fx}, [2]float64{1 - fy, fy}
	var result [4]float64
	for row := range 2 {
		for col := range 2 {
			weight := wx[col] * wy[row]
			c := texture.NRGBA64At(min(max(0, x0+col), texture.Rect.Max.X-1), min(max(0, y0+row), texture.Rect.Max.Y-1))
			alpha := float64(c.A) / 65535
			result[0] += float64(c.R) / 65535 * alpha * weight
			result[1] += float64(c.G) / 65535 * alpha * weight
			result[2] += float64(c.B) / 65535 * alpha * weight
			result[3] += alpha * weight
		}
	}
	return result
}

// viewLayers 按ECMA-363第9.5.4节依次绘制背景层或覆盖层
// 入参: layers 按后至前排列的纹理层
// 返回: error 参数、资源、预算或取消错误
func (r *Renderer) viewLayers(layers []U3DViewLayer) error {
	for _, layer := range layers {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		for _, value := range [...]float32{layer.Blend, layer.Rotation, layer.Location[0], layer.Location[1], layer.Scale[0], layer.Scale[1]} {
			if !finite(float64(value)) {
				return fmt.Errorf("invalid U3D view layer values")
			}
		}
		texture, err := r.texture(layer.Texture)
		if err != nil {
			return err
		}
		blend := min(1, max(0, float64(layer.Blend)))
		if blend == 0 || layer.Scale[0] == 0 || layer.Scale[1] == 0 || texture.Rect.Empty() {
			continue
		}
		sin, cos := math.Sincos(float64(layer.Rotation))
		x, y := float64(layer.Location[0]), float64(layer.Location[1])
		if r.view.Attributes&1 != 0 {
			x *= float64(r.output.Rect.Dx())
			y *= float64(r.output.Rect.Dy())
		}
		sx, sy := float64(layer.Scale[0]), float64(layer.Scale[1])
		rx, ry := float64(layer.Registration[0]), float64(layer.Registration[1])
		m := [6]float64{cos * sx, -sin * sx, sin * sy, cos * sy, x - cos*sx*rx - sin*sy*ry, y + sin*sx*rx - cos*sy*ry}
		for _, v := range m {
			if !finite(v) {
				return fmt.Errorf("invalid U3D view layer transform")
			}
		}
		bounds := viewImageBounds(texture.Bounds(), m, r.output.Bounds())
		for py := bounds.Min.Y; py < bounds.Max.Y; py++ {
			for px := bounds.Min.X; px < bounds.Max.X; px++ {
				if px%4096 == 0 || px == bounds.Min.X {
					if err := r.ctx.Err(); err != nil {
						return err
					}
				}
				cx, cy := float64(px)+.5, float64(py)+.5
				if cx < r.viewport[0] || cy < r.viewport[1] || cx >= r.viewport[0]+r.viewport[2] || cy >= r.viewport[1]+r.viewport[3] {
					continue
				}
				dx, dy := cx-x, cy-y
				u, v := (cos*dx-sin*dy)/sx+rx, (sin*dx+cos*dy)/sy+ry
				if !finite(u) || !finite(v) || u < 0 || v < 0 || u >= float64(texture.Rect.Dx()) || v >= float64(texture.Rect.Dy()) {
					continue
				}
				value := u3dTextureSample(texture, u, v)
				pixel := &r.frame[py*r.output.Rect.Dx()+px]
				alpha := value[3] * blend
				for i := range pixel.color {
					pixel.color[i] = value[i]*blend + pixel.color[i]*(1-alpha)
				}
			}
		}
	}
	return r.ctx.Err()
}
