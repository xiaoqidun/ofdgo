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
	"context"
	"fmt"
)

// Bounds 度量已编译指令的页面坐标范围，不重新定位文字或读取图片像素
// 包含描边与指令裁剪，不应用页面边界，不排除图片透明像素或渐变透明区域
// 入参: geometry 几何后端
// 返回: Box 毫米坐标范围, error 几何或指令错误
func (p *RasterPage) Bounds(geometry GeometryBackend) (Box, error) {
	return p.BoundsContext(context.Background(), geometry)
}

// BoundsContext 按上下文度量只读绘制指令，取消或失败时不返回部分范围
// 入参: ctx 取消上下文, geometry 几何后端
// 返回: Box 毫米坐标范围, error 取消、几何或指令错误
func (p *RasterPage) BoundsContext(ctx context.Context, geometry GeometryBackend) (Box, error) {
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	if p == nil {
		return Box{}, fmt.Errorf("invalid raster page")
	}
	if geometry == nil {
		return Box{}, fmt.Errorf("raster bounds: %w", ErrBackendUnavailable)
	}
	var box Box
	for index, command := range p.Commands {
		if err := ctx.Err(); err != nil {
			return Box{}, err
		}
		path, err := rasterCommandGeometry(geometry, command)
		if err == nil {
			var bounds Box
			bounds, err = geometry.Bounds(path)
			box = unionTextBox(box, bounds)
		}
		if err != nil {
			return Box{}, fmt.Errorf("raster command %d bounds: %w", index, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	return box, nil
}

// rasterCommandGeometry 共用指令的页面变换、描边与裁剪，不修改路径或样式
// 入参: geometry 几何后端, command 绘制指令
// 返回: GeometryPath 页面轮廓, error 几何或指令错误
func rasterCommandGeometry(geometry GeometryBackend, command RasterCommand) (GeometryPath, error) {
	if command.Clip != nil && len(command.Clip) == 0 {
		return nil, nil
	}
	var path GeometryPath
	if command.Image != nil {
		bounds := command.Image.Bounds()
		if bounds.Empty() {
			return nil, nil
		}
		path = geometryRectangle(Box{X: float64(bounds.Min.X), Y: float64(bounds.Min.Y), W: float64(bounds.Dx()), H: float64(bounds.Dy())})
	} else {
		if len(command.Path) == 0 || command.Paint.Gradient == nil && command.Paint.Color.A == 0 {
			return nil, nil
		}
		for _, segment := range command.Path {
			if segment.Verb > RasterClose {
				return nil, fmt.Errorf("unsupported raster path verb %d", segment.Verb)
			}
		}
		path = geometryFromRaster(command.Path)
	}
	matrix := MatrixFromValues([6]float64(command.Transform))
	path, err := geometry.Transform(path, matrix)
	if err != nil {
		return nil, err
	}
	if command.Image == nil && command.Stroke != nil {
		if err := validateStroke(*command.Stroke); err != nil {
			return nil, err
		}
		path, err = geometry.Stroke(path, *command.Stroke)
		if err != nil {
			return nil, err
		}
	} else if command.EvenOdd && command.Clip != nil {
		path, err = geometry.Normalize(path, true)
		if err != nil {
			return nil, err
		}
	}
	if command.Clip != nil {
		for _, segment := range command.Clip {
			if segment.Verb > RasterClose {
				return nil, fmt.Errorf("unsupported raster clip verb %d", segment.Verb)
			}
		}
		clip := geometryFromRaster(command.Clip)
		path, err = clipGeometry(geometry, path, &clip)
		if err != nil {
			return nil, err
		}
	}
	return path, nil
}
