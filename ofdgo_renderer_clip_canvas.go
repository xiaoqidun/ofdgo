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
	"image"
	"slices"

	"github.com/tdewolff/canvas"
)

// clipRenderer 收集图形和文字的绘制轮廓
type clipRenderer struct {
	path *canvas.Path
	clip *canvas.Path
}

// Size 返回不限定边界的裁剪画布尺寸
// 返回: float64 宽度, float64 高度
func (r *clipRenderer) Size() (float64, float64) {
	return 0, 0
}

// RenderPath 合并路径填充及描边轮廓
// 入参: path 路径, style 绘制样式, m 变换矩阵
func (r *clipRenderer) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if style.HasFill() {
		r.add(applyClipPath(path.Copy().Transform(m), r.clip))
	}
	if style.HasStroke() {
		p := path.Dash(style.DashOffset, style.Dashes...).Stroke(style.StrokeWidth, style.StrokeCapper, style.StrokeJoiner, canvas.Tolerance)
		r.add(applyClipPath(p.Transform(m), r.clip))
	}
}

// RenderText 合并文字字形轮廓
// 入参: text 文字, m 变换矩阵
func (r *clipRenderer) RenderText(text *canvas.Text, m canvas.Matrix) {
	text.RenderTo(r, m, 0)
}

// RenderImage 裁剪区不包含图像对象
// 入参: img 图像, m 变换矩阵
func (r *clipRenderer) RenderImage(img image.Image, m canvas.Matrix) {}

// add 合并裁剪轮廓
// 入参: path 裁剪路径
func (r *clipRenderer) add(path *canvas.Path) {
	if r.path == nil {
		r.path = path
	} else {
		r.path = unionClipPath(r.path, path)
	}
}

// buildObjectClipPath 构建对象裁剪路径并应用父级变换
// 入参: clips 裁剪对象, pageH 页面高度, boundary 外接矩形, objectCTM 对象CTM, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildObjectClipPath(clips *Clips, pageH float64, boundary string, objectCTM Matrix, parentCTM *Matrix, boundaryInCTM bool) *canvas.Path {
	path, err := r.objectGeometryClip(clips, boundary, objectCTM, RenderState{Parent: parentCTM, BoundaryInCTM: boundaryInCTM})
	if err != nil {
		r.renderError = err
		return nil
	}
	p, err := geometryToCanvasPath(path)
	if err != nil {
		r.renderError = err
		return nil
	}
	if p != nil {
		p = p.Translate(0, pageH)
	}
	return p
}

// buildClipPath 构建裁剪路径
// 入参: clips 裁剪对象, pageH 页面高度, bx 边界X坐标, by 边界Y坐标, objectCTM 对象CTM
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildClipPath(clips *Clips, pageH float64, bx, by float64, objectCTM Matrix) *canvas.Path {
	if clips == nil {
		return nil
	}
	var p *canvas.Path
	for _, clip := range clips.Clip {
		renderer := &clipRenderer{}
		for _, area := range clip.Area {
			areaCTM := NewMatrix(area.CTM)
			if clips.TransFlag == nil || *clips.TransFlag {
				areaCTM = objectCTM.Multiply(areaCTM)
			}
			for _, pathObj := range area.Path {
				renderer.add(r.buildClipAreaPath(pathObj, area.DrawParam, areaCTM, pageH, bx, by))
				if r.renderError != nil {
					return nil
				}
			}
			for _, textObj := range area.Text {
				box, _ := ParseBox(textObj.Boundary)
				ctm := NewMatrix(textObj.CTM)
				m := areaCTM.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(ctm)
				ctx := canvas.NewContext(renderer)
				ctx.SetView(canvas.Matrix{{m.a, -m.c, bx + m.e}, {-m.b, m.d, pageH - by - m.f}})
				renderer.clip = r.buildObjectClipPath(textObj.Clips, pageH, textObj.Boundary, ctm, &areaCTM, true)
				if renderer.clip != nil {
					renderer.clip = renderer.clip.Translate(bx, -by)
				}
				textObj.Boundary, textObj.CTM = "", ""
				textObj.Clips = nil
				textObj.Alpha = nil
				textObj.FillColor = &FillColor{Value: "0 0 0"}
				textObj.StrokeColor = &StrokeColor{Value: "0 0 0"}
				textRenderer := *r
				textRenderer.pageText = nil
				textRenderer.textOnly = false
				textRenderer.renderText(ctx, textObj, 0, r.drawParamDefaults(area.DrawParam, nil), nil, false, nil)
				if textRenderer.renderError != nil {
					r.renderError = textRenderer.renderError
					return nil
				}
			}
		}
		if renderer.path == nil {
			renderer.path = &canvas.Path{}
		}
		p = r.intersectCachedClipPath(p, renderer.path)
	}
	return p
}

// buildClipAreaPath 按绘制参数展开裁剪路径的填充及描边，再应用区域变换
// 入参: obj 裁剪路径, drawParam 区域绘制参数, parent 区域变换, pageH 页面高度, bx 横向位移, by 纵向位移
// 返回: *canvas.Path 裁剪轮廓
func (r *Renderer) buildClipAreaPath(obj PathObject, drawParam string, parent Matrix, pageH, bx, by float64) *canvas.Path {
	if obj.Visible != nil && !*obj.Visible {
		return &canvas.Path{}
	}
	box, _ := ParseBox(obj.Boundary)
	local := NewMatrix(obj.CTM)
	m := parent.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(local)
	clip := r.buildObjectClipPath(obj.Clips, pageH, obj.Boundary, local, &parent, true)
	obj.Boundary = ""
	p := r.buildPath(obj, 0, IdentityMatrix, false)
	var result *canvas.Path
	if obj.Fill != nil && *obj.Fill {
		paths := canvas.Paths(p.Copy().Split())
		for _, path := range paths {
			path.Close()
		}
		result = paths.Merge()
		if obj.Rule == "Even-Odd" {
			result = result.Settle(canvas.EvenOdd)
		}
	}
	if obj.Stroke == nil || *obj.Stroke {
		dp := r.drawParamDefaults(obj.DrawParam, r.drawParamDefaults(drawParam, nil))
		if dp != nil {
			copy := *dp
			copy.FillColor, copy.StrokeColor = nil, nil
			dp = &copy
		}
		obj.FillColor, obj.StrokeColor = nil, nil
		style := r.newPathStyle(dp, 0, 0, 0, nil)
		style.applyPathObject(r, obj, 0, 0, 0)
		if style.lineWidth == 0 {
			style.lineWidth = 25.4 / r.DPI
		}
		result = unionClipPath(result, r.strokeDashedCanvasPath(p, style.lineWidth, style.lineCap, style.lineJoin, style.dashOffset, style.dashPattern, false))
	}
	if result == nil {
		return &canvas.Path{}
	}
	result = result.Transform(canvas.Matrix{{m.a, -m.c, m.e}, {-m.b, m.d, pageH - m.f}})
	return applyClipPath(result, clip).Translate(bx, -by)
}

// intersectCachedClipPath 按完整几何内容复用复杂裁剪交集，缓存命中时返回独立副本
// 入参: parent 父级裁剪路径, current 当前裁剪路径
// 返回: *canvas.Path 相交后的裁剪路径
func (r *Renderer) intersectCachedClipPath(parent, current *canvas.Path) *canvas.Path {
	if parent == nil || current == nil || len(parent.Data())+len(current.Data()) < 256 {
		return intersectClipPath(parent, current)
	}
	key := canvasGeometryDigest(parent, current)
	cache := r.canvasClipIntersections()
	if path, ok := cache.get(key); ok {
		return path.Copy()
	}
	path := intersectClipPath(parent, current)
	if cost := len(path.Data())*8 + 256; cost <= cache.limit {
		cache.put(key, path.Copy(), cost)
	}
	return path
}

// intersectClipPath 求裁剪路径交集
// 入参: parent 父级裁剪路径, current 当前裁剪路径
// 返回: *canvas.Path 相交后的裁剪路径
func intersectClipPath(parent, current *canvas.Path) *canvas.Path {
	if parent == nil {
		return current
	}
	if current == nil {
		return parent
	}
	if slices.Equal(parent.Data(), current.Data()) {
		return parent
	}
	parentRect, parentOK := rectangularPath(parent)
	currentRect, currentOK := rectangularPath(current)
	if parentOK && currentOK {
		return parentRect.And(currentRect).ToPath()
	}
	if parent.Empty() || current.Empty() {
		return &canvas.Path{}
	}
	if parentOK {
		bounds := current.Bounds()
		if parentRect.Contains(bounds) {
			return current
		}
		if !parentRect.Overlaps(bounds) {
			return &canvas.Path{}
		}
	}
	if currentOK {
		bounds := parent.Bounds()
		if currentRect.Contains(bounds) {
			return parent
		}
		if !currentRect.Overlaps(bounds) {
			return &canvas.Path{}
		}
	}
	if result, ok := intersectConvexCanvasPaths(parent, current); ok {
		return result
	}
	if result, ok := intersectLinearCanvasContours(parent, current); ok {
		return result.Settle(canvas.NonZero)
	}
	if result, ok := intersectLinearCanvasContours(current, parent); ok {
		return result.Settle(canvas.NonZero)
	}
	return parent.And(current)
}

// unionClipPath 合并裁剪区域
// 入参: left 左侧裁剪路径, right 右侧裁剪路径
// 返回: *canvas.Path 合并后的裁剪路径
func unionClipPath(left, right *canvas.Path) *canvas.Path {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if slices.Equal(left.Data(), right.Data()) {
		return left
	}
	leftRect, leftOK := rectangularPath(left)
	rightRect, rightOK := rectangularPath(right)
	if leftOK && rightOK {
		bounds := leftRect.Add(rightRect)
		area := leftRect.Area() + rightRect.Area() - leftRect.And(rightRect).Area()
		if canvas.Equal(area, bounds.Area()) {
			return bounds.ToPath()
		}
	}
	return left.Or(right)
}

// applyClipPath 应用裁剪路径
// 入参: path 绘制路径, clip 裁剪路径
// 返回: *canvas.Path 裁剪后的绘制路径
func applyClipPath(path, clip *canvas.Path) *canvas.Path {
	if path == nil || clip == nil {
		return path
	}
	path = reduceRepeatedNonZeroPath(path)
	clip = reduceRepeatedNonZeroPath(clip)
	if slices.Equal(path.Data(), clip.Data()) {
		return path
	}
	if path.Empty() || clip.Empty() {
		return &canvas.Path{}
	}
	if rect, ok := rectangularPath(clip); ok {
		bounds := path.Bounds()
		if rect.Contains(bounds) {
			return path
		}
		if !rect.Overlaps(bounds) {
			return &canvas.Path{}
		}
		if pathRect, ok := rectangularPath(path); ok {
			return pathRect.And(rect).ToPath()
		}
	}
	if result, ok := intersectConvexCanvasPaths(path, clip); ok {
		return result
	}
	if result, ok := intersectLinearCanvasContours(path, clip); ok {
		return result.Settle(canvas.NonZero)
	}
	if result, ok := intersectLinearCanvasContours(clip, path); ok {
		return result.Settle(canvas.NonZero)
	}
	return path.And(clip)
}

// reduceRepeatedNonZeroPath 将完全相同的闭合轮廓约为一份，保持非零填充区域
// 入参: path 非零填充路径
// 返回: *canvas.Path 等价填充路径
func reduceRepeatedNonZeroPath(path *canvas.Path) *canvas.Path {
	if path == nil {
		return path
	}
	data := path.Data()
	if len(data) < 8 || data[0] != canvas.MoveToCmd {
		return path
	}
	end := 0
	scanner := path.Scanner()
	for scanner.Scan() {
		if scanner.Cmd() == canvas.MoveToCmd && end != 0 {
			break
		}
		end += len(scanner.Values()) + 2
	}
	if end == len(data) || data[end-1] != canvas.CloseCmd || len(data)%end != 0 {
		return path
	}
	for i := end; i < len(data); i += end {
		if !slices.Equal(data[:end], data[i:i+end]) {
			return path
		}
	}
	return canvas.NewPathFromData(slices.Clone(data[:end]))
}

// applyFillClipPath 为多段描边直接填充裁剪，不将相消轮廓用于几何测量
// 入参: path 非零填充描边, clip 裁剪路径
// 返回: *canvas.Path 绘制轮廓
func applyFillClipPath(path, clip *canvas.Path) *canvas.Path {
	if path != nil && clip != nil && len(path.Data()) >= 64 {
		if result, ok := intersectLinearCanvasContours(path, clip); ok {
			return result
		}
	}
	if path != nil && clip != nil && path.HasSubpaths() && len(path.Split()) >= 8 {
		if result, ok := intersectCanvasContours(path, clip); ok {
			return result
		}
	}
	return applyClipPath(path, clip)
}

// rectangularPath 获取矩形路径区域
// 入参: path 路径对象
// 返回: canvas.Rect 矩形区域, bool 是否为矩形
func rectangularPath(path *canvas.Path) (canvas.Rect, bool) {
	if path == nil || path.HasSubpaths() || !path.Closed() {
		return canvas.Rect{}, false
	}
	data := path.Data()
	if len(data) != 20 {
		return canvas.Rect{}, false
	}
	for i := 0; i < len(data); i += 4 {
		if data[i] != canvas.MoveToCmd && data[i] != canvas.LineToCmd && data[i] != canvas.CloseCmd {
			return canvas.Rect{}, false
		}
	}
	if !(canvas.Point{X: data[1], Y: data[2]}).Equals(canvas.Point{X: data[17], Y: data[18]}) {
		return canvas.Rect{}, false
	}
	rect := path.FastBounds()
	corners := 0
	for i := 0; i < 16; i += 4 {
		point := canvas.Point{X: data[i+1], Y: data[i+2]}
		next := (i + 4) % 16
		if !canvas.Equal(point.X, data[next+1]) && !canvas.Equal(point.Y, data[next+2]) {
			return canvas.Rect{}, false
		}
		corner := 0
		switch {
		case canvas.Equal(point.X, rect.X0) && canvas.Equal(point.Y, rect.Y0):
			corner = 1
		case canvas.Equal(point.X, rect.X1) && canvas.Equal(point.Y, rect.Y0):
			corner = 2
		case canvas.Equal(point.X, rect.X1) && canvas.Equal(point.Y, rect.Y1):
			corner = 4
		case canvas.Equal(point.X, rect.X0) && canvas.Equal(point.Y, rect.Y1):
			corner = 8
		default:
			return canvas.Rect{}, false
		}
		if corners&corner != 0 {
			return canvas.Rect{}, false
		}
		corners |= corner
	}
	return rect, corners == 15
}
