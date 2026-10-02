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

import "github.com/xiaoqidun/pdfgo"

// disjointText 检查实际字形轮廓是否互不相交，允许无重叠挖空文字直接保留
// 入参: node 隐式文字组
// 返回: bool 是否互不相交, error 轮廓编译错误
func (p *pdfImporter) disjointText(node pdfCompositeNode) (bool, error) {
	cache := p.compositingCache()
	if result, found := cache.textDisjoint[node.group]; found {
		return result, nil
	}
	geometry, err := p.renderer.Geometry()
	if err != nil {
		return false, err
	}
	var inverse pdfgo.Matrix
	var boxes []Box
	for _, child := range node.children {
		mark := child.text
		if mark == nil {
			mark = child.glyph
		}
		if mark == nil || len(mark.Glyphs) != 1 {
			return false, nil
		}
		if inverse == (pdfgo.Matrix{}) {
			var ok bool
			inverse, ok = p.matrix.Mul(mark.Matrix).Inverse()
			if !ok {
				return false, nil
			}
		}
		scene, _, err := p.compileGroup(func(local *pdfImporter) error {
			if child.group != nil {
				return child.coverage(local, false)
			}
			_, fill, stroke := child.style()
			if fill {
				if err := child.coverage(local, false); err != nil {
					return err
				}
			}
			if stroke {
				return child.coverage(local, true)
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if scene == nil {
			continue
		}
		var box Box
		for _, command := range scene.Commands {
			path := geometryFromRaster(command.Path)
			if command.Image != nil {
				b := command.Image.Bounds()
				path = geometryRectangle(Box{X: float64(b.Min.X), Y: float64(b.Min.Y), W: float64(b.Dx()), H: float64(b.Dy())})
			}
			path, err = geometry.Transform(path, MatrixFromValues([6]float64(command.Transform)))
			if err != nil {
				return false, err
			}
			if command.Stroke != nil {
				path, err = geometry.Stroke(path, *command.Stroke)
				if err != nil {
					return false, err
				}
			}
			path, err = geometry.Transform(path, MatrixFromValues([6]float64(inverse)))
			if err != nil {
				return false, err
			}
			bounds, err := geometry.Bounds(path)
			if err != nil {
				return false, err
			}
			box = unionTextBox(box, bounds)
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		for _, previous := range boxes {
			if box.X < previous.X+previous.W && previous.X < box.X+box.W && box.Y < previous.Y+previous.H && previous.Y < box.Y+box.H {
				if cache.textDisjoint == nil {
					cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
				}
				cache.textDisjoint[node.group] = false
				return false, nil
			}
		}
		boxes = append(boxes, box)
	}
	if cache.textDisjoint == nil {
		cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
	}
	cache.textDisjoint[node.group] = true
	return true, nil
}
