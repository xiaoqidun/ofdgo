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

// patternCellKey 区分共享底纹、颜色、透明度和实际页面变换
type patternCellKey struct {
	pattern      *Pattern
	color, space string
	index, alpha int
	matrix       [6]float64
}

// patternCell 复用同次编译中相同底纹单元，不缓存外部对象的裁剪
// 入参: pattern 底纹画刷, matrix 单元到页面矩阵
// 返回: []RasterCommand 只读单元指令, error 编译错误
func (c *semanticCompiler) patternCell(pattern *PatternPaint, matrix Matrix) ([]RasterCommand, error) {
	key := patternCellKey{pattern: pattern.Pattern, color: pattern.Color.Value, space: pattern.Color.ColorSpace, index: -1, alpha: 255, matrix: matrix.Values()}
	if pattern.Color.Index != nil {
		key.index = *pattern.Color.Index
	}
	if pattern.Alpha != nil {
		key.alpha = *pattern.Alpha
	}
	if commands, ok := c.patterns.get(key); ok {
		return commands, nil
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
		cost += 256 + (len(cmd.Path)+len(cmd.Clip))*104
		if cmd.Paint.Gradient != nil {
			cost += 128 + len(cmd.Paint.Gradient.Stops)*16
		}
	}
	c.patterns.put(key, child.page.Commands, cost)
	return child.page.Commands, nil
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
