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
	"math"
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// pdfPatternCellLimit 限制单页图案矢量快照的估算字节数
const pdfPatternCellLimit = 8 << 20

// pdfPatternCellKey 隔离图案来源、基色、坐标、精度与目标文档资源
type pdfPatternCellKey struct {
	source *pdfgo.TilingPattern
	base   pdfgo.Paint
	matrix pdfgo.Matrix
	dpi    float64
	editor *Editor
}

// pdfPatternInstanceRange 收缩已知路径及图片的实例范围，未知边界保留完整范围
// 入参: columns 横向实例数, rows 纵向实例数, bounds 对象边界, width 单元宽, height 单元高, xstep 横向步距, ystep 纵向步距
// 返回: int 左界, int 右界, int 上界, int 下界
func pdfPatternInstanceRange(columns, rows float64, bounds []Box, width, height, xstep, ystep float64) (int, int, int, int) {
	left, right, top, bottom := 0.0, 1-columns, 0.0, 1-rows
	for _, box := range bounds {
		if box.W <= 0 || box.H <= 0 || !finite(box.X) || !finite(box.Y) || !finite(box.X+box.W) || !finite(box.Y+box.H) {
			return 1 - int(columns), 0, 1 - int(rows), 0
		}
		left = math.Min(left, math.Max(1-columns, math.Ceil(-(box.X+box.W)/xstep)-1))
		right = math.Max(right, math.Min(0, math.Floor((width-box.X)/xstep)+1))
		top = math.Min(top, math.Max(1-rows, math.Ceil(-(box.Y+box.H)/ystep)-1))
		bottom = math.Max(bottom, math.Min(0, math.Floor((height-box.Y)/ystep)+1))
	}
	return int(left), int(right), int(top), int(bottom)
}

// tilingPattern 有界复用矢量图案，返回可独立编辑的对象副本
// 入参: source 平铺图案, base 无色图案基色
// 返回: *Pattern OFD图案, error 转换或取消错误
func (p *pdfImporter) tilingPattern(source *pdfgo.TilingPattern, base pdfgo.Paint) (*Pattern, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if p.resolveFile != nil || p.resolveReference != nil {
		return p.buildTilingPattern(source, base)
	}
	if p.patternCells == nil {
		p.patternCells = &renderCache[pdfPatternCellKey, *Pattern]{limit: pdfPatternCellLimit}
	}
	key := pdfPatternCellKey{source: source, matrix: p.matrix, dpi: p.rasterDPI, editor: p.editor}
	if source.PaintType == 2 {
		base.Tiling, base.Axial, base.Radial, base.Mesh, base.Function, base.Shading = nil, nil, nil, nil, nil, nil
		base.Alpha = 1
		key.base = base
	}
	if cached, ok := p.patternCells.get(key); ok {
		return cloneEditorData(cached), p.ctx.Err()
	}
	pattern, err := p.buildTilingPattern(source, base)
	if err != nil {
		return nil, err
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	cost := pdfPatternCellCost(reflect.ValueOf(pattern), p.patternCells.limit, 0)
	if cost <= p.patternCells.limit {
		p.patternCells.put(key, cloneEditorData(pattern), cost)
	}
	return pattern, p.ctx.Err()
}

// pdfPatternCellCost 有界估算公开数据的快照成本，不扫描共享的私有排版来源
// 入参: value 对象值, budget 剩余预算, depth 嵌套深度
// 返回: int 字节成本，超预算时返回预算加一
func pdfPatternCellCost(value reflect.Value, budget, depth int) int {
	if depth > 128 || budget < 0 {
		return budget + 1
	}
	cost := int(value.Type().Size())
	if cost > budget {
		return budget + 1
	}
	switch value.Kind() {
	case reflect.String:
		if value.Len() > budget-cost {
			return budget + 1
		}
		cost += value.Len()
	case reflect.Pointer:
		if !value.IsNil() {
			cost += pdfPatternCellCost(value.Elem(), budget-cost, depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			cost += pdfPatternCellCost(value.Index(i), budget-cost, depth+1)
			if cost > budget {
				return budget + 1
			}
		}
	case reflect.Struct:
		for i := range value.NumField() {
			if value.Type().Field(i).IsExported() {
				cost += pdfPatternCellCost(value.Field(i), budget-cost, depth+1)
				if cost > budget {
					return budget + 1
				}
			}
		}
	}
	return cost
}
