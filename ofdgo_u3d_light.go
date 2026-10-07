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
	"math"
)

// U3DDirectionalLight 定义无限远光源，Direction为发射方向，CameraRelative使用相机坐标
type U3DDirectionalLight struct {
	Color, Direction [3]float64
	CameraRelative   bool
}

// U3DLighting 替换场景灯光，空灯光列表表示无光源，DiffuseOffset加到受光材质的漫反射颜色
// 颜色及漫反射增量的各分量取值为0到1；不修改材质自发光或不受光着色器
type U3DLighting struct {
	Lights        []U3DDirectionalLight
	DiffuseOffset [3]float64
}

// u3dRenderLight 保存世界空间的光源位置与正Z发射方向
type u3dRenderLight struct {
	value               U3DLight
	position, direction u3dVector
}

// u3dRenderStyle 保存当前着色器的有效材质参数
type u3dRenderStyle struct {
	shader                               U3DShader
	ambient, diffuse, specular, emissive [3]float64
	opacity, exponent                    float64
}

// collectLights 收集本轮根节点下启用的光源实例
// 返回: error 参数、预算或取消错误
func (r *u3dRender) collectLights() error {
	if r.lighting != nil {
		return nil
	}
	r.lights = r.lights[:0]
	for i, instance := range r.scene {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		node := r.model.Nodes[instance.node]
		if !r.members[i] || node.Kind != "Light" {
			continue
		}
		j, ok := r.lightIndex[node.Resource]
		if !ok {
			continue
		}
		light := r.model.Lights[j]
		if light.Attributes&1 == 0 {
			continue
		}
		if light.Attributes > 7 || light.Kind > 3 {
			return fmt.Errorf("invalid U3D light")
		}
		for _, v := range light.Color {
			if !finite(float64(v)) {
				return fmt.Errorf("invalid U3D light color")
			}
		}
		if light.Kind != 0 && !finite(float64(light.Intensity)) {
			return fmt.Errorf("invalid U3D light intensity")
		}
		if light.Kind >= 2 {
			for _, v := range light.Attenuation {
				if !finite(float64(v)) {
					return fmt.Errorf("invalid U3D light attenuation")
				}
			}
		}
		if light.Kind == 3 && (!finite(float64(light.SpotAngle)) || light.SpotAngle <= 0 || light.SpotAngle > 180) {
			return fmt.Errorf("invalid U3D spot angle")
		}
		world := instance.world
		item := u3dRenderLight{value: light, position: u3dVector{world[12], world[13], world[14]}, direction: (u3dVector{world[8], world[9], world[10]}).unit()}
		if (light.Kind == 1 || light.Kind == 3) && item.direction == (u3dVector{}) {
			return fmt.Errorf("invalid U3D light direction")
		}
		if len(r.lights) == cap(r.lights) {
			capacity := max(8, cap(r.lights)*2)
			if err := r.reserve(uint64(capacity), 128); err != nil {
				return err
			}
			values := make([]u3dRenderLight, len(r.lights), capacity)
			copy(values, r.lights)
			r.remaining += uint64(cap(r.lights)) * 128
			r.lights = values
		}
		r.lights = append(r.lights, item)
	}
	return nil
}

// overrideLighting 校验并建立替代光源，不依赖模型光源节点
// 入参: lighting 灯光覆盖，空值保留模型灯光
// 返回: error 参数、预算或取消错误
func (r *u3dRender) overrideLighting(lighting *U3DLighting) error {
	if lighting == nil {
		return nil
	}
	for _, v := range lighting.DiffuseOffset {
		if !finite(v) || v < 0 || v > 1 {
			return fmt.Errorf("invalid U3D diffuse offset")
		}
	}
	if err := r.reserve(uint64(len(lighting.Lights)), 128); err != nil {
		return err
	}
	r.lights = make([]u3dRenderLight, len(lighting.Lights))
	for i, light := range lighting.Lights {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		for j, v := range light.Color {
			if !finite(v) || v < 0 || v > 1 || !finite(light.Direction[j]) {
				return fmt.Errorf("invalid U3D directional light")
			}
		}
		direction := u3dVector(light.Direction).unit()
		if light.CameraRelative && r.camera.ToWorld != ([16]float64{}) {
			m := r.camera.ToWorld
			direction = u3dVector{m[0]*direction[0] + m[4]*direction[1] + m[8]*direction[2], m[1]*direction[0] + m[5]*direction[1] + m[9]*direction[2], m[2]*direction[0] + m[6]*direction[1] + m[10]*direction[2]}.unit()
		}
		if direction == (u3dVector{}) {
			return fmt.Errorf("invalid U3D directional light direction")
		}
		r.lights[i] = u3dRenderLight{value: U3DLight{Attributes: 3, Kind: 1, Intensity: 1}, direction: direction}
	}
	r.lighting = lighting
	return nil
}

// style 解析着色器与材质，未启用的材质项使用固定默认值
// 入参: name 着色器名
// 返回: u3dRenderStyle 有效绘制参数, error 参数错误
func (r *u3dRender) style(name string) (u3dRenderStyle, error) {
	style := u3dRenderStyle{shader: U3DShader{Blend: 0x606, AlphaFunction: 0x617, RenderPass: math.MaxUint32}, ambient: [3]float64{.2, .2, .2}, diffuse: [3]float64{.8, .8, .8}, opacity: 1}
	if index, ok := r.shaders[name]; ok {
		style.shader = r.model.Shaders[index]
	}
	shader := style.shader
	if shader.Attributes > 7 || shader.Blend < 0x604 || shader.Blend > 0x607 || shader.AlphaFunction < 0x610 || shader.AlphaFunction > 0x617 || shader.Attributes&2 != 0 && !finite(float64(shader.AlphaReference)) {
		return style, fmt.Errorf("invalid U3D render shader")
	}
	if index, ok := r.materials[shader.Material]; ok {
		material := r.model.Materials[index]
		if material.Attributes > 63 {
			return style, fmt.Errorf("invalid U3D render material")
		}
		for i, item := range []struct {
			source [3]float32
			target *[3]float64
		}{{material.Ambient, &style.ambient}, {material.Diffuse, &style.diffuse}, {material.Specular, &style.specular}, {material.Emissive, &style.emissive}} {
			if material.Attributes&(1<<i) != 0 {
				for j, v := range item.source {
					if !finite(float64(v)) {
						return style, fmt.Errorf("invalid U3D material color")
					}
					item.target[j] = float64(v)
				}
			}
		}
		if material.Attributes&16 != 0 {
			if !finite(float64(material.Reflectivity)) {
				return style, fmt.Errorf("invalid U3D reflectivity")
			}
			style.exponent = min(1, max(0, float64(material.Reflectivity))) * 128
		}
		if material.Attributes&32 != 0 {
			if !finite(float64(material.Opacity)) {
				return style, fmt.Errorf("invalid U3D opacity")
			}
			style.opacity = min(1, max(0, float64(material.Opacity)))
		}
	}
	return style, nil
}

// shade 按世界坐标计算逐顶点漫反射、镜面与环境光
// 入参: position 顶点, normal 法线, style 材质, diffuse 顶点漫反射, specular 顶点镜面颜色
// 返回: [4]float64 直通RGBA, error 参数或取消错误
func (r *u3dRender) shade(position, normal u3dVector, style u3dRenderStyle, diffuse, specular *[4]float32) ([4]float64, error) {
	color := [4]float64{style.diffuse[0], style.diffuse[1], style.diffuse[2], style.opacity}
	if style.shader.Attributes&4 != 0 {
		if diffuse != nil {
			for i, v := range diffuse {
				if !finite(float64(v)) {
					return color, fmt.Errorf("invalid U3D vertex color")
				}
				if i < 3 {
					color[i] = float64(v)
					style.diffuse[i] = float64(v)
					style.ambient[i] = float64(v)
				} else {
					color[3] *= min(1, max(0, float64(v)))
				}
			}
		}
		if specular != nil {
			for i, v := range specular[:3] {
				if !finite(float64(v)) {
					return color, fmt.Errorf("invalid U3D vertex specular color")
				}
				style.specular[i] = float64(v)
			}
		}
	}
	if style.shader.Attributes&1 == 0 {
		return color, nil
	}
	copy(color[:3], style.emissive[:])
	if r.mode == U3DRenderShadedIllustration {
		for c := range 3 {
			color[c] += max(0, style.diffuse[c]) / 4
		}
	}
	if r.lighting != nil {
		for c := range 3 {
			style.diffuse[c] += r.lighting.DiffuseOffset[c]
		}
	}
	eye := (u3dVector{r.camera.ToWorld[12], r.camera.ToWorld[13], r.camera.ToWorld[14]}).sub(position).unit()
	if !r.camera.Perspective {
		eye = (u3dVector{-r.camera.ToWorld[8], -r.camera.ToWorld[9], -r.camera.ToWorld[10]}).unit()
		if r.camera.ToWorld == ([16]float64{}) {
			eye = u3dVector{0, 0, -1}
		}
	}
	for i, light := range r.lights {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return color, err
			}
		}
		value := light.value
		if value.Kind == 0 {
			for c := range 3 {
				color[c] += style.ambient[c] * float64(value.Color[c])
			}
			continue
		}
		incoming := u3dVector{-light.direction[0], -light.direction[1], -light.direction[2]}
		factor := float64(value.Intensity)
		if value.Kind >= 2 {
			incoming = light.position.sub(position)
			distance := math.Hypot(math.Hypot(incoming[0], incoming[1]), incoming[2])
			denominator := float64(value.Attenuation[0]) + float64(value.Attenuation[1])*distance + float64(value.Attenuation[2])*distance*distance
			if !finite(denominator) || denominator == 0 {
				return color, fmt.Errorf("invalid U3D point attenuation")
			}
			factor /= denominator
			incoming = incoming.unit()
			if value.Kind == 3 {
				cosine := -incoming.dot(light.direction)
				cutoff := math.Cos(float64(value.SpotAngle) * math.Pi / 360)
				if cosine < cutoff {
					continue
				}
				if value.Attributes&4 != 0 && cutoff < 1 {
					factor *= min(1, max(0, (cosine-cutoff)/(1-cutoff)))
				}
			}
		}
		diffuseAmount := max(0, normal.dot(incoming))
		specularAmount := 0.0
		if diffuseAmount > 0 && style.exponent > 0 && value.Attributes&2 != 0 {
			reflection := u3dVector{2*diffuseAmount*normal[0] - incoming[0], 2*diffuseAmount*normal[1] - incoming[1], 2*diffuseAmount*normal[2] - incoming[2]}
			specularAmount = math.Pow(max(0, reflection.dot(eye)), style.exponent)
		}
		for c := range 3 {
			lightColor := float64(value.Color[c])
			if r.lighting != nil {
				lightColor = r.lighting.Lights[i].Color[c]
			}
			color[c] += lightColor * factor * (style.diffuse[c]*diffuseAmount + style.specular[c]*specularAmount)
		}
	}
	for _, v := range color {
		if !finite(v) {
			return color, fmt.Errorf("invalid U3D lighting result")
		}
	}
	return color, nil
}

// u3dFragmentColor 对点、线、面的插值颜色应用透明度测试和相机空间雾效
// 入参: color 待更新的未预乘颜色, position 相机位置, shader 着色器, pass 绘制轮次
// 返回: bool 是否绘制, error 非有限颜色错误
func u3dFragmentColor(color *[4]float64, position u3dVector, shader *U3DShader, pass *U3DViewPass) (bool, error) {
	for _, value := range color {
		if !finite(value) {
			return false, fmt.Errorf("invalid U3D fragment color")
		}
	}
	color[3] = min(1, max(0, color[3]))
	if shader.Attributes&2 != 0 && !u3dAlphaTest(shader.AlphaFunction, color[3], float64(shader.AlphaReference)) {
		return false, nil
	}
	if pass.Attributes&1 != 0 {
		distance := math.Hypot(math.Hypot(position[0], position[1]), position[2])
		factor := (float64(pass.FogFar) - distance) / (float64(pass.FogFar) - float64(pass.FogNear))
		if pass.FogMode != 0 {
			amount := distance * math.Log(100) / float64(pass.FogFar)
			if pass.FogMode == 2 {
				amount *= amount
			}
			factor = math.Exp(-amount)
		}
		factor = min(1, max(0, factor))
		for c := range 3 {
			color[c] = color[c]*factor + float64(pass.FogColor[c])*(1-factor)
		}
	}
	for c := range 3 {
		color[c] = min(1, max(0, color[c]))
	}
	return true, nil
}

// u3dAlphaTest 按ECMA-363枚举比较透明度，不套用图形接口的枚举顺序
// 入参: function 比较函数, value 透明度, reference 参考值
// 返回: bool 是否通过
func u3dAlphaTest(function uint32, value, reference float64) bool {
	switch function {
	case 0x610:
		return false
	case 0x611:
		return value < reference
	case 0x612:
		return value > reference
	case 0x613:
		return value == reference
	case 0x614:
		return value != reference
	case 0x615:
		return value <= reference
	case 0x616:
		return value >= reference
	case 0x617:
		return true
	}
	return false
}
