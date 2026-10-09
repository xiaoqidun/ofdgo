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
	"reflect"
	"slices"
)

// 底纹去重的最小编码长度、候选数量及引用数量上限
const (
	compressionPatternMinimum = 4096
	compressionPatternLimit   = 4096
	compressionPatternUses    = 65536
)

// compressionPatternPaint 保存可共享的底纹颜色，不修改输入对象
type compressionPatternPaint struct {
	fill   *FillColor
	stroke *StrokeColor
	count  int
	id     string
}

// compressionPatternUse 记录输出快照内需要替换的直接对象
type compressionPatternUse struct {
	page, layer, object int
	paint               *compressionPatternPaint
}

// compressPatternPaints 将新文档中重复的大底纹颜色提取为绘制参数，仅修改输出快照
// 返回: error 编码或取消错误
func (e *Editor) compressPatternPaints() error {
	if e.output == nil || e.output.options.Mode == CompressionUnchanged || e.output.protected || e.source != nil {
		return nil
	}
	paints := make(map[[32]byte]*compressionPatternPaint)
	var uses []compressionPatternUse
	for pi, page := range e.pages {
		for li, layer := range page.Content.Layer {
			if layer.DrawParam != "" {
				continue
			}
			for oi, object := range layer.Objects {
				if err := e.output.ctx.Err(); err != nil {
					return err
				}
				if len(uses) == compressionPatternUses {
					break
				}
				fill, stroke := compressionObjectPattern(object)
				if fill == nil && stroke == nil {
					continue
				}
				hash := sha256.New()
				var size int64
				x := newOFDXML(convertWriter{writer: hash, count: &size, context: e.output.ctx})
				x.ctx = e.output.ctx
				x.color("FillColor", fill)
				x.color("StrokeColor", (*FillColor)(stroke))
				if err := x.finish(); err != nil {
					return err
				}
				if size < compressionPatternMinimum {
					continue
				}
				key := [32]byte(hash.Sum(nil))
				paint := paints[key]
				if paint == nil {
					if len(paints) == compressionPatternLimit {
						continue
					}
					paint = &compressionPatternPaint{fill: fill, stroke: stroke}
					paints[key] = paint
				} else if !reflect.DeepEqual(paint.fill, fill) || !reflect.DeepEqual(paint.stroke, stroke) {
					continue
				}
				paint.count++
				uses = append(uses, compressionPatternUse{pi, li, oi, paint})
			}
		}
	}
	var copied bool
	lastPage, lastLayer := -1, -1
	for _, use := range uses {
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		if use.paint.count < 2 {
			continue
		}
		if !copied {
			e.pages = slices.Clone(e.pages)
			e.resources = slices.Clone(e.resources)
			copied = true
		}
		if use.paint.id == "" {
			id, err := e.addEditorDrawParam(DrawParam{FillColor: use.paint.fill, StrokeColor: use.paint.stroke})
			if err != nil {
				return err
			}
			use.paint.id = id
		}
		if use.page != lastPage {
			e.pages[use.page].Content.Layer = slices.Clone(e.pages[use.page].Content.Layer)
			lastPage, lastLayer = use.page, -1
		}
		layer := &e.pages[use.page].Content.Layer[use.layer]
		if use.layer != lastLayer {
			layer.Objects = slices.Clone(layer.Objects)
			lastLayer = use.layer
		}
		object := &layer.Objects[use.object]
		if object.Type == "PathObject" {
			object.PathObject.DrawParam = use.paint.id
			object.PathObject.FillColor, object.PathObject.StrokeColor = nil, nil
		} else {
			object.TextObject.DrawParam = use.paint.id
			object.TextObject.FillColor, object.TextObject.StrokeColor = nil, nil
		}
	}
	return nil
}

// compressionObjectPattern 筛选没有原文及绘制参数继承的底纹对象
// 入参: object 图形对象
// 返回: *FillColor 填充颜色, *StrokeColor 描边颜色，不适用时均为空
func compressionObjectPattern(object GraphicObject) (*FillColor, *StrokeColor) {
	if object.origin != nil {
		return nil, nil
	}
	var fill *FillColor
	var stroke *StrokeColor
	switch object.Type {
	case "PathObject":
		if object.PathObject.DrawParam != "" {
			return nil, nil
		}
		fill, stroke = object.PathObject.FillColor, object.PathObject.StrokeColor
	case "TextObject":
		if object.TextObject.DrawParam != "" {
			return nil, nil
		}
		fill, stroke = object.TextObject.FillColor, object.TextObject.StrokeColor
	default:
		return nil, nil
	}
	if fill != nil && fill.Pattern != nil || stroke != nil && stroke.Pattern != nil {
		return fill, stroke
	}
	return nil, nil
}
