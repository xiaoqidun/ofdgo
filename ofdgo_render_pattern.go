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
	"slices"
)

// stampContentKey 区分印章内容、物理尺寸、裁剪与字体栅格化精度，不含放置位置
type stampContentKey struct {
	data          [32]byte
	kind          string
	width, height float64
	dpi           float64
	clip          Box
	clipped       bool
	annotations   bool
}

// stampContentState 保存有界的共享签章编译结果及配置
type stampContentState struct {
	config RenderBackends
	cache  renderCache[stampContentKey, []RasterCommand]
}

// stampContentStateKey 隔离渲染器私有的共享签章数据
type stampContentStateKey struct{}

// resetFonts 使包含文字的签章编译结果随字体配置失效
func (s *stampContentState) resetFonts() {
	s.cache = renderCache[stampContentKey, []RasterCommand]{limit: 16 << 20}
}

// sharedStamp 复用相同签章的解码、嵌套文档编译及局部裁剪，不缓存放置位置
// 入参: stamp 签章, compile 未缓存的签章编译方法
// 返回: error 签章解析或编译错误
func (c *semanticCompiler) sharedStamp(stamp Stamp, compile func(*semanticCompiler, Stamp) error) error {
	if c.textOnly || c.measure || len(stamp.Data) == 0 {
		return nil
	}
	r := c.renderer
	state, _ := r.backendStates[stampContentStateKey{}].(*stampContentState)
	config := r.backends
	if state == nil || !sameBackend(state.config.Fonts, config.Fonts) || !sameBackend(state.config.Geometry, config.Geometry) ||
		!sameBackend(state.config.Compiler, config.Compiler) || !sameBackend(state.config.Raster, config.Raster) {
		state = &stampContentState{config: config}
		state.resetFonts()
		r.backendStates[stampContentStateKey{}] = state
	}
	key := stampContentKey{data: sha256.Sum256(stamp.Data), kind: stamp.Type, width: stamp.Box.W, height: stamp.Box.H, dpi: c.page.DPI, clipped: stamp.Clip != nil, annotations: r.RenderAnnotations}
	if stamp.Clip != nil {
		key.clip = *stamp.Clip
	}
	commands, ok := state.cache.get(key)
	if !ok {
		local := stamp
		local.Box.X, local.Box.Y = 0, 0
		page := &RasterPage{Width: c.page.Width, Height: c.page.Height, DPI: c.page.DPI}
		child := &semanticCompiler{renderer: r, geometry: c.geometry, page: page, patterns: c.patterns}
		if err := compile(child, local); err != nil {
			return err
		}
		commands = page.Commands
		cost := 0
		for _, command := range commands {
			cost += rasterCommandCost(command)
			if command.Image != nil {
				bounds := command.Image.Bounds()
				cost += bounds.Dx() * bounds.Dy() * 4
			}
		}
		state.cache.put(key, commands, cost)
	}
	c.page.Commands = append(c.page.Commands, translatePatternCommands(commands, stamp.Box.X, stamp.Box.Y)...)
	return nil
}

// patternCellKey 区分共享底纹、颜色、透明度和影响单元语义的页面变换
type patternCellKey struct {
	pattern      *Pattern
	color, space string
	index, alpha int
	colorAlpha   int
	matrix       [6]float64
}

// patternCell 复用同次编译中相同底纹单元，不缓存外部对象的裁剪
// 入参: pattern 底纹画刷, matrix 单元到页面矩阵
// 返回: []RasterCommand 只读单元指令, error 编译错误
func (c *semanticCompiler) patternCell(pattern *PatternPaint, matrix Matrix) ([]RasterCommand, error) {
	x, y := 0.0, 0.0
	if patternCellTranslatable(pattern) {
		x, y = matrix.e, matrix.f
		matrix.e, matrix.f = 0, 0
	}
	key := patternCellKey{pattern: pattern.Pattern, color: pattern.Color.Value, space: pattern.Color.ColorSpace, index: -1, alpha: 255, colorAlpha: 255, matrix: matrix.Values()}
	if pattern.Color.Index != nil {
		key.index = *pattern.Color.Index
	}
	if pattern.Alpha != nil {
		key.alpha = *pattern.Alpha
	}
	if pattern.Color.Alpha != nil {
		key.colorAlpha = *pattern.Color.Alpha
	}
	if commands, ok := c.patterns.get(key); ok {
		return translatePatternCommands(commands, x, y), nil
	}
	child := &semanticCompiler{renderer: c.renderer, geometry: c.geometry, page: &RasterPage{Width: c.page.Width, Height: c.page.Height, DPI: c.page.DPI}, pattern: true, patterns: c.patterns}
	defaults := &DrawParam{FillColor: &pattern.Color, StrokeColor: (*StrokeColor)(&pattern.Color)}
	for _, object := range pattern.CellContent.Objects {
		object = mergeGraphicObjectAlpha(object, pattern.Alpha)
		if err := c.renderer.WalkObject(&object, RenderState{Defaults: defaults, Parent: &matrix, BoundaryInCTM: true}, child); err != nil {
			return nil, err
		}
	}
	cost := 0
	for _, cmd := range child.page.Commands {
		cost += rasterCommandCost(cmd)
	}
	c.patterns.put(key, child.page.Commands, cost)
	return translatePatternCommands(child.page.Commands, x, y), nil
}

// patternCellTranslatable 检查无需按页面位置重新解释的叶子单元
// 入参: pattern 底纹画刷
// 返回: bool 是否可仅平移编译结果
func patternCellTranslatable(pattern *PatternPaint) bool {
	if pattern.Color.Pattern != nil {
		return false
	}
	for _, object := range pattern.CellContent.Objects {
		var fill, stroke *FillColor
		switch object.Type {
		case "PathObject":
			if object.PathObject.DrawParam != "" {
				return false
			}
			fill, stroke = object.PathObject.FillColor, (*FillColor)(object.PathObject.StrokeColor)
		case "TextObject":
			if object.TextObject.DrawParam != "" {
				return false
			}
			fill, stroke = object.TextObject.FillColor, (*FillColor)(object.TextObject.StrokeColor)
		case "ImageObject":
			if object.ImageObject.Border != nil {
				stroke = (*FillColor)(object.ImageObject.Border.BorderColor)
			}
		default:
			return false
		}
		if fill != nil && fill.Pattern != nil || stroke != nil && stroke.Pattern != nil {
			return false
		}
	}
	return true
}

// translatePatternCommands 平移单元实例，共享只读路径和画刷，独立处理页面裁剪
// 入参: commands 原点单元, x 横向平移, y 纵向平移
// 返回: []RasterCommand 放置后的指令
func translatePatternCommands(commands []RasterCommand, x, y float64) []RasterCommand {
	if x == 0 && y == 0 {
		return commands
	}
	result := slices.Clone(commands)
	for i := range result {
		command := &result[i]
		command.Transform[4] += x
		command.Transform[5] += y
		if command.Clip != nil {
			command.Clip = slices.Clone(command.Clip)
			matrix := RasterMatrix{1, 0, 0, 1, x, y}
			for j := range command.Clip {
				segment := &command.Clip[j]
				segment.End = matrix.Apply(segment.End)
				segment.Control1 = matrix.Apply(segment.Control1)
				segment.Control2 = matrix.Apply(segment.Control2)
			}
		}
	}
	return result
}

// appendPatternCell 将缓存指令放入当前裁剪，不修改单元数据
// 入参: commands 单元指令, clip 页面裁剪, segments 已转换裁剪
// 返回: error 裁剪合并错误
func (c *semanticCompiler) appendPatternCell(commands []RasterCommand, clip GeometryPath, segments []RasterSegment) error {
	for _, command := range commands {
		commandClip := segments
		if command.Clip != nil {
			path := geometryFromRaster(command.Clip)
			combined, err := c.geometry.Combine(path, clip, GeometryIntersect)
			if err != nil {
				return err
			}
			commandClip, err = c.segments(combined)
			if err != nil {
				return err
			}
		}
		command.Clip = commandClip
		c.page.Commands = append(c.page.Commands, command)
	}
	return nil
}

// geometryFromRaster 将只含贝塞尔曲线的光栅路径还原为库路径
// 入参: path 光栅路径
// 返回: GeometryPath 库路径
func geometryFromRaster(path []RasterSegment) GeometryPath {
	result := make(GeometryPath, 0, len(path))
	for _, s := range path {
		verb := GeometryVerb(s.Verb)
		if s.Verb == RasterClose {
			verb = GeometryClose
		}
		result = append(result, GeometrySegment{Verb: verb, End: Point{s.End.X, s.End.Y}, Control1: Point{s.Control1.X, s.Control1.Y}, Control2: Point{s.Control2.X, s.Control2.Y}})
	}
	return result
}
