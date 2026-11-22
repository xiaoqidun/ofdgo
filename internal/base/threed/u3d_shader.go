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

// U3DTextureLayer 保存单个着色器纹理通道，矩阵按列排列
// Blend依次以0至3表示相乘、相加、替换及线性混合，BlendSource为1时使用BlendConstant
// Mode依次以0至4表示模型坐标、平面、圆柱、球面及反射映射
// Alpha控制是否使用纹理透明度，Repeat低两位分别启用横向及纵向平铺
// 平面映射直接取参考位置的XY；圆柱以Z为纵坐标，圆柱与球面的经线从正X轴朝正Y轴递增
// 球面纬度从正Z极到负Z极映射为0至1；反射映射使用相机空间球面环境图，图像纵坐标向下
type U3DTextureLayer struct {
	Texture                  string
	Intensity, BlendConstant float32
	Blend, BlendSource       uint8
	Mode, Repeat             uint8
	Alpha                    bool
	Transform, WrapTransform [16]float32
}

// u3dTextureSet 保存一次绘制内共用的有序纹理层
type u3dTextureSet struct {
	layers                  [8]*U3DTextureLayer
	images                  [8]*image.NRGBA64
	wrap                    [8]*[16]float64
	normal                  u3dMatrix
	mask                    uint8
	reflection, perspective bool
}

// u3dShaderSample 按归一化坐标采样纹理，平铺时跨边界插值，未平铺时钳制边缘
// 全透明采样保留原始RGB，供相加、固定权重混合及透明度覆盖使用
// 入参: texture 纹理, u 横坐标, v 纵坐标, repeat 平铺位图, alpha 是否使用透明度
// 返回: [4]float64 未预乘颜色及透明度
func u3dShaderSample(texture *image.NRGBA64, u, v float64, repeat uint8, alpha bool) [4]float64 {
	if repeat&1 != 0 {
		u -= math.Floor(u)
	} else {
		u = min(1, max(0, u))
	}
	if repeat&2 != 0 {
		v -= math.Floor(v)
	} else {
		v = min(1, max(0, v))
	}
	w, h := texture.Rect.Dx(), texture.Rect.Dy()
	x, y := u*float64(w)-.5, v*float64(h)-.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	wx, wy := [2]float64{1 - fx, fx}, [2]float64{1 - fy, fy}
	var result [4]float64
	var transparent [3]float64
	for row := range 2 {
		py := min(h-1, max(0, y0+row))
		if repeat&2 != 0 {
			py = (y0 + row + h) % h
		}
		for col := range 2 {
			px := min(w-1, max(0, x0+col))
			if repeat&1 != 0 {
				px = (x0 + col + w) % w
			}
			c := texture.NRGBA64At(px, py)
			a := 1.0
			if alpha {
				a = float64(c.A) / 65535
			}
			weight := wx[col] * wy[row]
			if a == 0 {
				transparent[0] += float64(c.R) / 65535 * weight
				transparent[1] += float64(c.G) / 65535 * weight
				transparent[2] += float64(c.B) / 65535 * weight
			}
			result[0] += float64(c.R) / 65535 * a * weight
			result[1] += float64(c.G) / 65535 * a * weight
			result[2] += float64(c.B) / 65535 * a * weight
			result[3] += a * weight
		}
	}
	if result[3] != 0 {
		for c := range 3 {
			result[c] /= result[3]
		}
	} else {
		copy(result[:3], transparent[:])
	}
	return result
}

// coordinates 保存模型纹理坐标或程序化映射的插值输入，避免经线两侧直接插值
// 入参: vertex 目标顶点, mesh 几何资源, shading 属性布局, corner 属性索引, normal 世界法线
// 返回: error 坐标、索引或维度错误
func (s *u3dTextureSet) coordinates(vertex *u3dVertex, mesh *U3DMesh, shading U3DShading, corner U3DCorner, normal u3dVector) error {
	if vertex.textureMask != 0 {
		clear(vertex.texture[:])
	}
	vertex.textureMask = 0
	if s == nil {
		return nil
	}
	vertex.textureMask = s.mask
	for i, layer := range s.layers {
		if layer == nil {
			continue
		}
		if layer.Mode != 0 {
			if layer.Mode == 4 {
				vertex.texture[i] = s.normal.normal(normal)
				if vertex.texture[i] == ([3]float64{}) {
					return fmt.Errorf("invalid U3D reflection normal")
				}
			} else {
				if uint64(corner.Position) >= uint64(len(mesh.Positions)) {
					return fmt.Errorf("invalid U3D texture position index")
				}
				for c, value := range mesh.Positions[corner.Position] {
					if !finite(float64(value)) {
						return fmt.Errorf("invalid U3D texture position")
					}
					vertex.texture[i][c] = float64(value)
				}
			}
			continue
		}
		if i >= len(shading.TextureDimensions) || uint64(corner.Texture[i]) >= uint64(len(mesh.TextureCoordinates)) {
			return fmt.Errorf("invalid U3D texture coordinate index")
		}
		dimension := shading.TextureDimensions[i]
		if dimension < 1 || dimension > 4 {
			return fmt.Errorf("invalid U3D texture coordinate dimensions")
		}
		value := [4]float64{0, 0, 0, 1}
		for component := range dimension {
			v := float64(mesh.TextureCoordinates[corner.Texture[i]][component])
			if !finite(v) {
				return fmt.Errorf("invalid U3D texture coordinate")
			}
			value[component] = v
		}
		for component, row := range [3]int{0, 1, 3} {
			total := 0.0
			for col, v := range value {
				total += float64(layer.Transform[col*4+row]) * v
			}
			if !finite(total) {
				return fmt.Errorf("invalid U3D transformed texture coordinate")
			}
			vertex.texture[i][component] = total
		}
	}
	return nil
}

// color 按通道顺序合成纹理，在透明度覆盖、透明测试和雾效之前执行
// 入参: color 未预乘底色, coordinates 插值后的纹理输入, position 相机空间位置
// 返回: error 无效投影坐标或混合结果
func (s *u3dTextureSet) color(color *[4]float64, coordinates *[8][3]float64, position u3dVector) error {
	if s == nil {
		return nil
	}
	for i, layer := range s.layers {
		if layer == nil {
			continue
		}
		uv := coordinates[i]
		if layer.Mode != 0 {
			var err error
			uv, err = s.generated(i, uv, position)
			if err != nil {
				return err
			}
		}
		u, v := uv[0]/uv[2], uv[1]/uv[2]
		if !finite(u) || !finite(v) {
			return fmt.Errorf("invalid U3D projected texture coordinate")
		}
		sample := u3dShaderSample(s.images[i], u, v, layer.Repeat, layer.Alpha)
		for c := range 3 {
			sample[c] *= float64(layer.Intensity)
		}
		weight := sample[3]
		if layer.BlendSource == 1 {
			weight = min(1, max(0, float64(layer.BlendConstant)))
		}
		for c := range color {
			switch layer.Blend {
			case 0:
				color[c] *= sample[c]
			case 1:
				color[c] += sample[c]
			case 2:
				color[c] = sample[c]
			case 3:
				color[c] = sample[c]*weight + color[c]*(1-weight)
			}
			if !finite(color[c]) {
				return fmt.Errorf("invalid U3D texture blend result")
			}
			color[c] = min(1, max(0, color[c]))
		}
	}
	return nil
}

// shaderTextures 校验并缓存启用的纹理，未使用的着色器不会触发图像解码
// 入参: shader 着色器
// 返回: *u3dTextureSet 纹理组合，空值表示无纹理, error 参数、能力、预算或取消错误
func (r *Renderer) shaderTextures(shader U3DShader) (*u3dTextureSet, error) {
	if shader.Textures == ([8]*U3DTextureLayer{}) {
		return nil, nil
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if cached := r.textureSets[shader.Textures]; cached != nil {
		return cached, nil
	}
	if err := r.Reserve(1, 512); err != nil {
		return nil, err
	}
	set := &u3dTextureSet{layers: shader.Textures}
	for i, layer := range set.layers {
		if layer == nil {
			continue
		}
		if layer.Blend > 3 || layer.BlendSource > 1 || layer.Mode > 4 || layer.Repeat > 3 || !finite(float64(layer.Intensity)) || !finite(float64(layer.BlendConstant)) {
			return nil, fmt.Errorf("invalid U3D texture layer")
		}
		for _, matrix := range [2][16]float32{layer.Transform, layer.WrapTransform} {
			for _, value := range matrix {
				if !finite(float64(value)) {
					return nil, fmt.Errorf("invalid U3D texture transform")
				}
			}
		}
		if layer.Mode >= 1 && layer.Mode <= 3 {
			if err := r.Reserve(1, 256); err != nil {
				return nil, err
			}
			inverse, err := u3dTextureInverse(layer.WrapTransform)
			if err != nil {
				return nil, err
			}
			set.wrap[i] = &inverse
		}
		if layer.Mode == 4 && !set.reflection {
			var ok bool
			set.normal, ok = r.toCamera.inverse()
			if !ok {
				return nil, fmt.Errorf("invalid U3D reflection camera")
			}
			set.reflection, set.perspective = true, r.camera.Perspective
		}
		img, err := r.texture(layer.Texture)
		if err != nil {
			return nil, err
		}
		set.images[i] = img
		set.mask |= 1 << i
	}
	if r.textureSets == nil {
		r.textureSets = make(map[[8]*U3DTextureLayer]*u3dTextureSet)
	}
	r.textureSets[shader.Textures] = set
	return set, nil
}

// textureColor 合成纹理后恢复节点指定的透明度，再叠加视图透明度
// 入参: textures 纹理组合, color 顶点插值颜色, coordinates 纹理输入, position 相机空间位置, override 是否指定节点透明度
// 返回: error 纹理映射或混合错误
func (r *Renderer) textureColor(textures *u3dTextureSet, color *[4]float64, coordinates *[8][3]float64, position u3dVector, override bool) error {
	if textures == nil {
		return nil
	}
	opacity := color[3]
	if err := textures.color(color, coordinates, position); err != nil {
		return err
	}
	if override {
		color[3] = opacity
	}
	if r.opacity >= 0 {
		color[3] *= r.opacity
	}
	return nil
}
