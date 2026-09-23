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
	"errors"
	"fmt"
)

// GGGeometryBackend 使用公共边界、变换和描边，区域运算由显式配置的几何后端承担
// 未配置GeometryBackend时仍可独立处理路径、曲线和描边
type GGGeometryBackend struct{ GeometryBackend }

// Path 解析库自有路径并使用公共几何变换
// 入参: object 路径对象
// 返回: GeometryPath 页面路径, error 参数或变换错误
func (b GGGeometryBackend) Path(object PathObject) (GeometryPath, error) {
	return objectGeometryPath(b, object)
}

// Name 返回实际几何提供者组合
// 返回: string 后端组合标识
func (b GGGeometryBackend) Name() string {
	if b.GeometryBackend == nil {
		return "gg"
	}
	return "gg+" + backendName(b.GeometryBackend)
}

// Curves 使用公共弧线转换，不调用组合几何后端
// 入参: path 页面路径
// 返回: GeometryPath 贝塞尔路径, error 能力或路径错误
func (b GGGeometryBackend) Curves(path GeometryPath) (GeometryPath, error) {
	return path.Curves(.0001)
}

// Bounds 使用公共解析极值，不依赖组合几何后端
// 入参: path 页面路径
// 返回: Box 受浮点舍入影响的解析范围, error 路径或数值错误
func (b GGGeometryBackend) Bounds(path GeometryPath) (Box, error) {
	return path.Bounds()
}

// Transform 使用公共仿射变换，保持椭圆弧和退化投影的折返
// 入参: path 页面路径, matrix 页面变换
// 返回: GeometryPath 新路径, error 路径或变换错误
func (b GGGeometryBackend) Transform(path GeometryPath, matrix Matrix) (GeometryPath, error) {
	return path.Transform(matrix)
}

// Stroke 为后续区域运算提供描边轮廓，复杂轮廓交给显式补充后端
// 仅单条实线平头描边的公共轮廓无重叠，其他分片不能可靠进入补充后端的布尔运算
// 无补充后端时仍返回公共非零填充轮廓，但不提供区域运算能力
// 入参: path 页面路径, options 描边样式
// 返回: GeometryPath 非零填充轮廓, error 参数或精度限制错误
func (b GGGeometryBackend) Stroke(path GeometryPath, options StrokeOptions) (GeometryPath, error) {
	if b.GeometryBackend != nil && !geometrySimpleStroke(path, options) {
		if err := path.validate(); err != nil {
			return nil, err
		}
		if err := validateStroke(options); err != nil {
			return nil, err
		}
		return b.GeometryBackend.Stroke(path, options)
	}
	return b.strokeFill(path, options)
}

// strokeFill 仅供直接填充使用公共展开，不将重叠轮廓送入布尔运算
// 入参: path 页面路径, options 描边样式
// 返回: GeometryPath 非零填充轮廓, error 参数或能力错误
func (b GGGeometryBackend) strokeFill(path GeometryPath, options StrokeOptions) (GeometryPath, error) {
	outline, err := path.Stroke(options)
	if errors.Is(err, ErrBackendUnavailable) && b.GeometryBackend != nil {
		return b.GeometryBackend.Stroke(path, options)
	}
	return outline, err
}

// geometrySimpleStroke 判断公共描边是否保证生成单个无重叠矩形
// 入参: path 路径, options 描边样式
// 返回: bool 是否可直接参与区域运算
func geometrySimpleStroke(path GeometryPath, options StrokeOptions) bool {
	return len(path) == 2 && path[0].Verb == GeometryMove && path[1].Verb == GeometryLine &&
		(options.Cap == "" || options.Cap == "Butt") && len(options.Dashes) == 0
}

// Region 将区域解析交给显式提供者，缺失能力时返回错误
// 入参: region 区域, matrix 页面变换
// 返回: GeometryPath 区域路径, error 能力或解析错误
func (b GGGeometryBackend) Region(region *Region, matrix Matrix) (GeometryPath, error) {
	if b.GeometryBackend == nil {
		return nil, fmt.Errorf("GG region geometry: %w", ErrBackendUnavailable)
	}
	return b.GeometryBackend.Region(region, matrix)
}

// Normalize 将填充规则整理交给显式提供者
// 入参: path 路径, evenOdd 是否采用奇偶规则
// 返回: GeometryPath 非零填充轮廓, error 能力或几何错误
func (b GGGeometryBackend) Normalize(path GeometryPath, evenOdd bool) (GeometryPath, error) {
	if b.GeometryBackend == nil {
		return nil, fmt.Errorf("GG normalize geometry: %w", ErrBackendUnavailable)
	}
	return b.GeometryBackend.Normalize(path, evenOdd)
}

// Combine 将布尔运算交给显式提供者
// 入参: left、right 操作数, operation 运算类型
// 返回: GeometryPath 结果路径, error 能力或几何错误
func (b GGGeometryBackend) Combine(left, right GeometryPath, operation GeometryOperation) (GeometryPath, error) {
	if b.GeometryBackend == nil {
		return nil, fmt.Errorf("GG boolean geometry: %w", ErrBackendUnavailable)
	}
	return b.GeometryBackend.Combine(left, right, operation)
}

// Clip 将裁剪解析交给显式提供者
// 入参: renderer 渲染器, clips 裁剪, matrix 页面变换, parent 父裁剪
// 返回: *GeometryPath 裁剪路径, error 能力或几何错误
func (b GGGeometryBackend) Clip(renderer *Renderer, clips *Clips, matrix Matrix, parent *GeometryPath) (*GeometryPath, error) {
	if b.GeometryBackend == nil {
		return nil, fmt.Errorf("GG clip geometry: %w", ErrBackendUnavailable)
	}
	return b.GeometryBackend.Clip(renderer, clips, matrix, parent)
}
