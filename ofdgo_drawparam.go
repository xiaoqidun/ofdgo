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

// defaultPathLineWidth 为默认描边宽度，单位为毫米
const defaultPathLineWidth = 0.353

// defaultMiterLimit 为默认尖角截断值
const defaultMiterLimit = 4.234

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
	case "CompositeObject":
		obj.CompositeObject.Alpha = mergeAlpha(obj.CompositeObject.Alpha, alpha)
	}
	return obj
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
	if dp.DashPattern != "" || dp.DashPatternSet {
		base.DashPattern = dp.DashPattern
		base.DashPatternSet = dp.DashPatternSet
	}
	if dp.DashOffset != nil {
		base.DashOffset = dp.DashOffset
	}
	if dp.MiterLimit > 0 || dp.MiterLimitSet {
		base.MiterLimit = dp.MiterLimit
		base.MiterLimitSet = dp.MiterLimitSet
	}
	if dp.FillColor != nil {
		base.FillColor = dp.FillColor
	}
	if dp.StrokeColor != nil {
		base.StrokeColor = dp.StrokeColor
	}
	return &base
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

// compositeObjectDefaults 合并复合图元直接声明的描边属性与继承样式
// 入参: object 复合图元对象, defaults 父级默认绘制参数
// 返回: *DrawParam 合并后的绘制参数
func (r *Renderer) compositeObjectDefaults(object CompositeObject, defaults *DrawParam) *DrawParam {
	merged := r.drawParamDefaults(object.DrawParam, defaults)
	hasDirect := object.LineWidth > 0 || object.LineWidthSet || object.Join != "" || object.Cap != "" ||
		object.DashPattern != "" || object.DashPatternSet || object.DashOffset != nil || object.MiterLimit > 0 || object.MiterLimitSet
	if !hasDirect {
		return merged
	}
	var base DrawParam
	if merged != nil {
		base = *merged
	}
	override := &DrawParam{
		LineWidth: object.LineWidth, LineWidthSet: object.LineWidthSet,
		Join: object.Join, Cap: object.Cap,
		DashPattern: object.DashPattern, DashPatternSet: object.DashPatternSet, DashOffset: object.DashOffset,
		MiterLimit: object.MiterLimit, MiterLimitSet: object.MiterLimitSet,
	}
	return mergeDrawParam(base, override)
}

// getDrawParam 解析绘制参数继承链，按实际资源识别循环引用
// 入参: id 参数标识, visited 已访问资源
// 返回: *DrawParam 绘制参数
func (r *Renderer) getDrawParam(id string, visited map[*DrawParam]bool) *DrawParam {
	dp, _ := resourceValue(r.drawParams, id)
	if dp == nil || visited[dp] {
		return nil
	}
	if visited == nil {
		visited = make(map[*DrawParam]bool)
	}
	visited[dp] = true
	if dp.Relative != "" {
		base := r.getDrawParam(dp.Relative, visited)
		if base != nil {
			return mergeDrawParam(*base, dp)
		}
	}
	return dp
}
