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
	"io"
)

// U3DView 保存视图节点的投影、裁剪、视口及叠加层参数
type U3DView struct {
	Attributes          uint32
	Near, Far           float32
	Projection          float32
	ProjectionVector    [3]float32
	Viewport            [4]float32
	Backdrops, Overlays []U3DViewLayer
}

// U3DViewLayer 保存背景或覆盖纹理，注册点使用纹理像素，旋转使用弧度
type U3DViewLayer struct {
	Texture         string
	Blend, Rotation float32
	Location        [2]float32
	Registration    [2]int32
	Scale           [2]float32
}

// U3DViewResource 保存视图的绘制轮次，各轮独立指定根节点及雾效
type U3DViewResource struct {
	Name   string
	Passes []U3DViewPass
}

// U3DViewPass 保存单轮根节点与雾效，Attributes最低位启用雾效
type U3DViewPass struct {
	Root                string
	Attributes, FogMode uint32
	FogColor            [4]float32
	FogNear, FogFar     float32
}

// view 按ECMA-363第9.5.4节读取视图节点的可变投影字段
// 入参: r 字段读取器
// 返回: *U3DView 视图, error 结构或预算错误
func (d *u3dDecoder) view(r *u3dValues) (*U3DView, error) {
	view := &U3DView{Attributes: r.u32(), Near: r.f32(), Far: r.f32()}
	if view.Attributes & ^uint32(7) != 0 {
		return nil, fmt.Errorf("invalid U3D view attributes")
	}
	if view.Attributes&6 < 4 {
		view.Projection = r.f32()
	} else {
		for i := range view.ProjectionVector {
			view.ProjectionVector[i] = r.f32()
		}
	}
	for i := range view.Viewport {
		view.Viewport[i] = r.f32()
	}
	for _, layers := range []*[]U3DViewLayer{&view.Backdrops, &view.Overlays} {
		count := r.u32()
		if uint64(count) > uint64(len(r.data)-r.pos)/34 {
			return nil, io.ErrUnexpectedEOF
		}
		if err := d.reserve(uint64(count), 64); err != nil {
			return nil, err
		}
		*layers = make([]U3DViewLayer, int(count))
		for i := range *layers {
			if err := d.ctx.Err(); err != nil {
				return nil, err
			}
			layer := &(*layers)[i]
			layer.Texture = r.text()
			layer.Blend, layer.Rotation = r.f32(), r.f32()
			for j := range layer.Location {
				layer.Location[j] = r.f32()
			}
			for j := range layer.Registration {
				layer.Registration[j] = int32(r.u32())
			}
			for j := range layer.Scale {
				layer.Scale[j] = r.f32()
			}
		}
	}
	return view, r.err
}

// viewResource 按ECMA-363第9.8.2节读取绘制轮次与雾效
// 入参: r 字段读取器, name 资源名
// 返回: error 结构或预算错误
func (d *u3dDecoder) viewResource(r *u3dValues, name string) error {
	count := r.u32()
	if uint64(count) > uint64(len(r.data)-r.pos)/34 {
		return io.ErrUnexpectedEOF
	}
	if err := d.reserve(uint64(count), 48); err != nil {
		return err
	}
	view := U3DViewResource{Name: name, Passes: make([]U3DViewPass, int(count))}
	for i := range view.Passes {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		pass := &view.Passes[i]
		pass.Root, pass.Attributes, pass.FogMode = r.text(), r.u32(), r.u32()
		if pass.Attributes > 1 || pass.FogMode > 2 {
			return fmt.Errorf("invalid U3D view fog parameters")
		}
		for j := range pass.FogColor {
			pass.FogColor[j] = r.f32()
		}
		pass.FogNear, pass.FogFar = r.f32(), r.f32()
	}
	d.model.Views = append(d.model.Views, view)
	return r.err
}
