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

import "github.com/tdewolff/canvas"

// renderAnnotations 渲染页面注释外观
// 入参: ctx 画布上下文, pageID 页面ID, pageH 页面高度
func (r *Renderer) renderAnnotations(ctx *canvas.Context, pageID string, pageH float64) {
	for _, annot := range r.Reader.Annots[pageID] {
		if annot.Visible != nil && !*annot.Visible {
			continue
		}
		groups, grouped := ctx.Renderer.(interface {
			beginAnnotation(string) bool
			endObject()
		})
		grouped = grouped && groups.beginAnnotation(annot.ID)
		box, _ := ParseBox(annot.Appearance.Boundary)
		ctm := Matrix{a: 1, d: 1, e: box.X, f: box.Y}
		for i := range annot.Appearance.Objects {
			r.renderObject(ctx, &annot.Appearance.Objects[i], pageH, nil, &ctm, false, nil)
		}
		if grouped {
			groups.endObject()
		}
	}
}

// renderTemplate 渲染模板
// 入参: ctx 画布上下文, templateID 模板ID, pageH 页面高度
func (r *Renderer) renderTemplate(ctx *canvas.Context, templateID string, pageH float64) {
	tplContent := r.loadTemplate(templateID)
	if tplContent == nil {
		return
	}
	for order := range 3 {
		r.renderLayers(ctx, tplContent.Content.Layer, pageH, order)
	}
}

// renderLayers 按原顺序渲染指定类型的图层
// 入参: ctx 画布上下文, layers 图层列表, pageH 页面高度, order 图层类型顺序
func (r *Renderer) renderLayers(ctx *canvas.Context, layers []Layer, pageH float64, order int) {
	for _, layer := range layers {
		if layerOrder(layer.Type) == order {
			r.renderLayer(ctx, layer, pageH, nil, nil)
		}
	}
}

// renderLayer 渲染图层
// 入参: ctx 画布上下文, layer 图层对象, pageH 页面高度, defaults 默认绘制参数, parentCTM 父级CTM
func (r *Renderer) renderLayer(ctx *canvas.Context, layer Layer, pageH float64, defaults *DrawParam, parentCTM *Matrix) {
	defaults = r.drawParamDefaults(layer.DrawParam, defaults)
	if len(layer.Objects) > 0 {
		for i := range layer.Objects {
			r.renderObject(ctx, &layer.Objects[i], pageH, defaults, parentCTM, false, nil)
		}
		return
	}
	for _, textObj := range layer.TextObject {
		r.renderText(ctx, textObj, pageH, defaults, parentCTM, false, nil)
	}
	for _, pathObj := range layer.PathObject {
		r.renderPath(ctx, pathObj, pageH, defaults, parentCTM, false, nil)
	}
	for _, imgObj := range layer.ImageObject {
		r.renderImage(ctx, imgObj, pageH, parentCTM, false, nil)
	}
	for _, cgu := range layer.CompositeGraphicUnit {
		r.renderCompositeGraphicUnit(ctx, cgu, pageH, defaults, parentCTM, false, nil)
	}
}

// renderCompositeGraphicUnit 渲染复合图元
// 入参: ctx 画布上下文, cgu 复合图元对象, pageH 页面高度, defaults 默认绘制参数, parentCTM 父级CTM, boundaryInCTM 边界是否参与CTM变换, parentClip 父级裁剪路径
func (r *Renderer) renderCompositeGraphicUnit(ctx *canvas.Context, cgu CompositeGraphicUnit, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if cgu.Visible != nil && !*cgu.Visible {
		if groups, ok := ctx.Renderer.(interface{ skipObjects(int) }); ok {
			count, ref, seen := len(cgu.Objects), cgu.ResourceID, make(map[string]bool)
			for ref != "" && !seen[ref] {
				seen[ref] = true
				unit := r.CompositeGraphicUnits[ref]
				if unit == nil {
					break
				}
				count, ref = count+len(unit.Objects), unit.ResourceID
			}
			groups.skipObjects(count)
		}
		return
	}
	ctx.Push()
	box, _ := ParseBox(cgu.Boundary)
	boundaryCTM := TranslationMatrix(box.X, box.Y)
	if parentCTM != nil {
		boundaryCTM = parentCTM.Multiply(boundaryCTM)
	}
	currentCTM := boundaryCTM.Multiply(NewMatrix(cgu.CTM))
	clips, clipCTM := cgu.Clips, currentCTM
	if clips != nil && clips.TransFlag != nil && !*clips.TransFlag {
		clipCopy := *clips
		clipCopy.TransFlag = nil
		clips, clipCTM = &clipCopy, boundaryCTM
	}
	clipPath := intersectClipPath(parentClip, r.buildClipPath(clips, pageH, 0, 0, clipCTM))
	defaults = r.drawParamDefaults(cgu.DrawParam, defaults)
	if cgu.ResourceID != "" {
		if ref, ok := r.CompositeGraphicUnits[cgu.ResourceID]; ok {
			refCopy := *ref
			refCopy.Alpha = mergeAlpha(refCopy.Alpha, cgu.Alpha)
			r.renderCompositeGraphicUnit(ctx, refCopy, pageH, defaults, &currentCTM, true, clipPath)
		}
	}
	if len(cgu.Objects) > 0 {
		for _, obj := range cgu.Objects {
			obj = mergeGraphicObjectAlpha(obj, cgu.Alpha)
			r.renderObject(ctx, &obj, pageH, defaults, &currentCTM, boundaryInCTM, clipPath)
		}
		ctx.Pop()
		return
	}
	for _, imgObj := range cgu.ImageObject {
		imgObj.Alpha = mergeAlpha(imgObj.Alpha, cgu.Alpha)
		r.renderImage(ctx, imgObj, pageH, &currentCTM, boundaryInCTM, clipPath)
	}
	for _, pathObj := range cgu.PathObject {
		pathObj.Alpha = mergeAlpha(pathObj.Alpha, cgu.Alpha)
		r.renderPath(ctx, pathObj, pageH, defaults, &currentCTM, boundaryInCTM, clipPath)
	}
	for _, textObj := range cgu.TextObject {
		textObj.Alpha = mergeAlpha(textObj.Alpha, cgu.Alpha)
		r.renderText(ctx, textObj, pageH, defaults, &currentCTM, boundaryInCTM, clipPath)
	}
	for _, subCgu := range cgu.CompositeGraphicUnit {
		subCgu.Alpha = mergeAlpha(subCgu.Alpha, cgu.Alpha)
		r.renderCompositeGraphicUnit(ctx, subCgu, pageH, defaults, &currentCTM, boundaryInCTM, clipPath)
	}
	ctx.Pop()
}

// renderObject 渲染图形对象
// 入参: ctx 画布上下文, obj 图形对象, pageH 页面高度, defaults 默认绘制参数, parentCTM 父级CTM, boundaryInCTM 边界是否参与CTM变换, parentClip 父级裁剪路径
func (r *Renderer) renderObject(ctx *canvas.Context, obj *GraphicObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if groups, ok := ctx.Renderer.(interface {
		beginObject(*GraphicObject) bool
		endObject()
	}); ok && groups.beginObject(obj) {
		defer groups.endObject()
	}
	switch obj.Type {
	case "TextObject":
		r.renderText(ctx, obj.TextObject, pageH, defaults, parentCTM, boundaryInCTM, parentClip)
	case "PathObject":
		r.renderPath(ctx, obj.PathObject, pageH, defaults, parentCTM, boundaryInCTM, parentClip)
	case "ImageObject":
		r.renderImage(ctx, obj.ImageObject, pageH, parentCTM, boundaryInCTM, parentClip)
	case "CompositeGraphicUnit", "CompositeObject":
		r.renderCompositeGraphicUnit(ctx, obj.CompositeGraphicUnit, pageH, defaults, parentCTM, boundaryInCTM, parentClip)
	}
}
