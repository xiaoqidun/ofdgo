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
	"slices"
)

// RasterScene 固定尺寸、DPI、字体和资源的可重复绘制场景
// 每次Render返回独立图像；源图像只读借用，修改页面或配置后应重新准备
type RasterScene interface {
	Render() (image.Image, error)
}

// RasterSceneBackend 可选的光栅预编译能力，不改变后端选择
type RasterSceneBackend interface {
	RasterBackend
	Prepare(page *RasterPage) (RasterScene, error)
}

// PreparePage 编译并准备当前配置的页面场景，不自动跟踪后续编辑
// 入参: page 源页面
// 返回: RasterScene 可重复绘制场景, error 编译或准备错误
func (r *Renderer) PreparePage(page *PageContent) (RasterScene, error) {
	if r.backends.Raster == nil {
		return nil, fmt.Errorf("raster scene: %w", ErrBackendUnavailable)
	}
	compiled, err := r.CompilePage(page)
	if err != nil {
		return nil, err
	}
	return PrepareRasterScene(r.backends.Raster, compiled)
}

// PrepareRasterScene 准备绘制快照，未实现预编译的后端复用公共指令
// 入参: backend 光栅后端, page 绘制页面
// 返回: RasterScene 可重复绘制场景, error 准备错误
func PrepareRasterScene(backend RasterBackend, page *RasterPage) (RasterScene, error) {
	if backend == nil {
		return nil, fmt.Errorf("raster scene: %w", ErrBackendUnavailable)
	}
	if _, _, err := page.PixelSize(); err != nil {
		return nil, err
	}
	if provider, ok := backend.(RasterSceneBackend); ok {
		return provider.Prepare(page)
	}
	return &rasterScene{backend: backend, page: cloneRasterPage(page)}, nil
}

// rasterScene 复用后端无关指令，图片保持只读引用
type rasterScene struct {
	backend RasterBackend
	page    *RasterPage
}

// Render 绘制固定快照
// 返回: image.Image 独立图像, error 绘制错误
func (s *rasterScene) Render() (image.Image, error) { return s.backend.Render(s.page) }

// cloneRasterPage 隔离可变指令，源图片按接口约定只读借用
// 入参: page 绘制页面
// 返回: *RasterPage 指令快照
func cloneRasterPage(page *RasterPage) *RasterPage {
	result := *page
	result.Commands = slices.Clone(page.Commands)
	for i := range result.Commands {
		cmd := &result.Commands[i]
		cmd.Path, cmd.Clip = slices.Clone(cmd.Path), slices.Clone(cmd.Clip)
		if cmd.Stroke != nil {
			stroke := *cmd.Stroke
			stroke.Dashes = slices.Clone(stroke.Dashes)
			cmd.Stroke = &stroke
		}
		if cmd.Paint.Gradient != nil {
			gradient := *cmd.Paint.Gradient
			gradient.Stops = slices.Clone(gradient.Stops)
			if gradient.Spread != nil {
				spread := *gradient.Spread
				gradient.Spread = &spread
			}
			cmd.Paint.Gradient = &gradient
		}
	}
	return &result
}
