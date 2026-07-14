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
	"image/color"

	"github.com/tdewolff/canvas"
)

// renderAnnotations 渲染页面注释外观
// 入参: ctx 画布上下文, pageID 页面ID, pageH 页面高度
func (r *Renderer) renderAnnotations(ctx *canvas.Context, pageID string, pageH float64) {
	for _, annot := range r.Reader.Annots[pageID] {
		if len(annot.Appearance.Objects) == 0 {
			continue
		}
		box, _ := ParseBox(annot.Appearance.Boundary)
		ctm := Matrix{a: 1, d: 1, e: box.X, f: box.Y}
		for _, obj := range annot.Appearance.Objects {
			r.renderObject(ctx, obj, pageH, nil, nil, 0, &ctm, false)
		}
	}
}

// renderTemplate 渲染模板
// 入参: ctx 画布上下文, templateID 模板ID, pageH 页面高度
func (r *Renderer) renderTemplate(ctx *canvas.Context, templateID string, pageH float64) {
	tplContent := r.templatePageCache[templateID]
	if tplContent == nil {
		var tplPage *TemplatePage
		for i := range r.Reader.doc.CommonData.TemplatePage {
			if r.Reader.doc.CommonData.TemplatePage[i].ID == templateID {
				tplPage = &r.Reader.doc.CommonData.TemplatePage[i]
				break
			}
		}
		if tplPage == nil {
			return
		}
		var err error
		tplContent, err = r.Reader.PageContent(Page{BaseLoc: tplPage.BaseLoc})
		if err != nil {
			return
		}
		r.templatePageCache[templateID] = tplContent
	}
	if tplContent.Content.Layer != nil {
		for _, layer := range tplContent.Content.Layer {
			r.renderLayer(ctx, layer, pageH, nil, nil, 0, nil)
		}
	}
}

// renderLayer 渲染图层
// 入参: ctx 画布上下文, layer 图层对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM
func (r *Renderer) renderLayer(ctx *canvas.Context, layer Layer, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix) {
	defaultFill, defaultStroke, defaultLW = r.drawParamDefaults(layer.DrawParam, defaultFill, defaultStroke, defaultLW)
	if len(layer.Objects) > 0 {
		for _, obj := range layer.Objects {
			r.renderObject(ctx, obj, pageH, defaultFill, defaultStroke, defaultLW, parentCTM, false)
		}
		return
	}
	for _, textObj := range layer.TextObject {
		r.renderText(ctx, textObj, pageH, defaultFill, defaultStroke, parentCTM, false)
	}
	for _, pathObj := range layer.PathObject {
		r.renderPath(ctx, pathObj, pageH, defaultFill, defaultStroke, defaultLW, parentCTM, false)
	}
	for _, imgObj := range layer.ImageObject {
		r.renderImage(ctx, imgObj, pageH, parentCTM, false)
	}
	for _, cgu := range layer.CompositeGraphicUnit {
		r.renderCompositeGraphicUnit(ctx, cgu, pageH, defaultFill, defaultStroke, defaultLW, parentCTM, false)
	}
}

// renderCompositeGraphicUnit 渲染复合图元
// 入参: ctx 画布上下文, cgu 复合图元对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM, boundaryInCTM 边界是否参与CTM变换
func (r *Renderer) renderCompositeGraphicUnit(ctx *canvas.Context, cgu CompositeGraphicUnit, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix, boundaryInCTM bool) {
	if cgu.Visible != nil && !*cgu.Visible {
		return
	}
	ctx.Push()
	currentCTM := NewMatrix(cgu.CTM)
	if parentCTM != nil {
		currentCTM = parentCTM.Multiply(currentCTM)
	}
	if cgu.ResourceID != "" {
		if ref, ok := r.CompositeGraphicUnits[cgu.ResourceID]; ok {
			refCopy := *ref
			refCopy.Alpha = mergeAlpha(refCopy.Alpha, cgu.Alpha)
			r.renderCompositeGraphicUnit(ctx, refCopy, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM, true)
		}
	}
	defaultFill, defaultStroke, defaultLW = r.drawParamDefaults(cgu.DrawParam, defaultFill, defaultStroke, defaultLW)
	if len(cgu.Objects) > 0 {
		for _, obj := range cgu.Objects {
			obj = mergeGraphicObjectAlpha(obj, cgu.Alpha)
			r.renderObject(ctx, obj, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM, boundaryInCTM)
		}
		ctx.Pop()
		return
	}
	for _, imgObj := range cgu.ImageObject {
		imgObj.Alpha = mergeAlpha(imgObj.Alpha, cgu.Alpha)
		r.renderImage(ctx, imgObj, pageH, &currentCTM, boundaryInCTM)
	}
	for _, pathObj := range cgu.PathObject {
		pathObj.Alpha = mergeAlpha(pathObj.Alpha, cgu.Alpha)
		r.renderPath(ctx, pathObj, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM, boundaryInCTM)
	}
	for _, textObj := range cgu.TextObject {
		textObj.Alpha = mergeAlpha(textObj.Alpha, cgu.Alpha)
		r.renderText(ctx, textObj, pageH, defaultFill, defaultStroke, &currentCTM, boundaryInCTM)
	}
	for _, subCgu := range cgu.CompositeGraphicUnit {
		subCgu.Alpha = mergeAlpha(subCgu.Alpha, cgu.Alpha)
		r.renderCompositeGraphicUnit(ctx, subCgu, pageH, defaultFill, defaultStroke, defaultLW, &currentCTM, boundaryInCTM)
	}
	ctx.Pop()
}

// renderObject 渲染图形对象
// 入参: ctx 画布上下文, obj 图形对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM, boundaryInCTM 边界是否参与CTM变换
func (r *Renderer) renderObject(ctx *canvas.Context, obj GraphicObject, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix, boundaryInCTM bool) {
	switch obj.Type {
	case "TextObject":
		r.renderText(ctx, obj.TextObject, pageH, defaultFill, defaultStroke, parentCTM, boundaryInCTM)
	case "PathObject":
		r.renderPath(ctx, obj.PathObject, pageH, defaultFill, defaultStroke, defaultLW, parentCTM, boundaryInCTM)
	case "ImageObject":
		r.renderImage(ctx, obj.ImageObject, pageH, parentCTM, boundaryInCTM)
	case "CompositeGraphicUnit", "CompositeObject":
		r.renderCompositeGraphicUnit(ctx, obj.CompositeGraphicUnit, pageH, defaultFill, defaultStroke, defaultLW, parentCTM, boundaryInCTM)
	}
}

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
// 入参: id 绘制参数ID, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽
// 返回: color.Color 默认填充色, color.Color 默认描边色, float64 默认线宽
func (r *Renderer) drawParamDefaults(id string, defaultFill, defaultStroke color.Color, defaultLW float64) (color.Color, color.Color, float64) {
	if id == "" {
		return defaultFill, defaultStroke, defaultLW
	}
	dp := r.getDrawParam(id, nil)
	if dp == nil {
		return defaultFill, defaultStroke, defaultLW
	}
	if dp.LineWidth > 0 {
		defaultLW = dp.LineWidth
	}
	if dp.FillColor != nil {
		defaultFill = parseFillColor(dp.FillColor)
	}
	if dp.StrokeColor != nil {
		defaultStroke = parseStrokeColor(dp.StrokeColor)
	}
	return defaultFill, defaultStroke, defaultLW
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
			merged := *base
			if dp.LineWidth > 0 {
				merged.LineWidth = dp.LineWidth
			}
			if dp.Join != "" {
				merged.Join = dp.Join
			}
			if dp.Cap != "" {
				merged.Cap = dp.Cap
			}
			if dp.DashPattern != "" {
				merged.DashPattern = dp.DashPattern
				merged.DashOffset = dp.DashOffset
			}
			if dp.MiterLimit > 0 {
				merged.MiterLimit = dp.MiterLimit
			}
			if dp.FillColor != nil {
				merged.FillColor = dp.FillColor
			}
			if dp.StrokeColor != nil {
				merged.StrokeColor = dp.StrokeColor
			}
			if dp.Font != "" {
				merged.Font = dp.Font
			}
			if dp.Size > 0 {
				merged.Size = dp.Size
			}
			if dp.Weight > 0 {
				merged.Weight = dp.Weight
			}
			if dp.Italic {
				merged.Italic = dp.Italic
			}
			return &merged
		}
		return dp
	}
	return nil
}
