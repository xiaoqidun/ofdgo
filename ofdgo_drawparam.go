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

const defaultPathLineWidth = 0.353
const defaultMiterLimit = 3.528

// mergeGraphicObjectAlpha 合并图形对象透明度
// 入参: obj 图形对象, alpha 父级透明度
// 返回: GraphicObject 合并后的图形对象
func mergeGraphicObjectAlpha(obj GraphicObject, alpha *int) GraphicObject {
	if alpha == nil {
		return obj
	}
	switch obj.Type {
	case "TextObject":
		obj.TextObject.Alpha = mergeAlpha(obj.TextObject.Alpha, alpha)
	case "PathObject":
		obj.PathObject.Alpha = mergeAlpha(obj.PathObject.Alpha, alpha)
	case "ImageObject":
		obj.ImageObject.Alpha = mergeAlpha(obj.ImageObject.Alpha, alpha)
	case "CompositeGraphicUnit", "CompositeObject":
		obj.CompositeGraphicUnit.Alpha = mergeAlpha(obj.CompositeGraphicUnit.Alpha, alpha)
	}
	return obj
}

// drawParamDefaults 合并绘制参数默认样式
// 入参: id 绘制参数ID, defaults 默认绘制参数
// 返回: *DrawParam 合并后的绘制参数
func (r *Renderer) drawParamDefaults(id string, defaults *DrawParam) *DrawParam {
	if id == "" {
		return defaults
	}
	dp := r.getDrawParam(id, nil)
	if dp == nil {
		return defaults
	}
	if defaults == nil {
		return dp
	}
	return mergeDrawParam(*defaults, dp)
}

// mergeDrawParam 合并绘制参数属性
// 入参: base 基础绘制参数, dp 覆盖绘制参数
// 返回: *DrawParam 合并后的绘制参数
func mergeDrawParam(base DrawParam, dp *DrawParam) *DrawParam {
	if dp.LineWidth > 0 || dp.LineWidthSet {
		base.LineWidth = dp.LineWidth
		base.LineWidthSet = dp.LineWidthSet
	}
	if dp.Join != "" {
		base.Join = dp.Join
	}
	if dp.Cap != "" {
		base.Cap = dp.Cap
	}
	if dp.DashPattern != "" || dp.dashPatternSet {
		base.DashPattern = dp.DashPattern
		base.dashPatternSet = dp.dashPatternSet
	}
	if dp.DashOffset != nil {
		base.DashOffset = dp.DashOffset
	}
	if dp.MiterLimit > 0 {
		base.MiterLimit = dp.MiterLimit
	}
	if dp.FillColor != nil {
		base.FillColor = dp.FillColor
	}
	if dp.StrokeColor != nil {
		base.StrokeColor = dp.StrokeColor
	}
	if dp.Font != "" {
		base.Font = dp.Font
	}
	if dp.Size > 0 {
		base.Size = dp.Size
	}
	if dp.Weight > 0 {
		base.Weight = dp.Weight
	}
	if dp.Italic {
		base.Italic = dp.Italic
	}
	return &base
}

// getDrawParam 获取绘制参数逻辑
// 入参: id 参数ID, visited 访问记录
// 返回: *DrawParam 绘制参数
func (r *Renderer) getDrawParam(id string, visited map[string]bool) *DrawParam {
	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[id] {
		return nil
	}
	visited[id] = true
	if dp, ok := r.DrawParams[id]; ok {
		if dp.Relative != "" {
			base := r.getDrawParam(dp.Relative, visited)
			if base == nil {
				return dp
			}
			return mergeDrawParam(*base, dp)
		}
		return dp
	}
	return nil
}
