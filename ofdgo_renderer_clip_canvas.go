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

// add 合并裁剪轮廓
// 入参: path 裁剪路径
func (r *clipRenderer) add(path *canvas.Path) {
	if r.path == nil {
		r.path = path
	} else {
		r.path = unionClipPath(r.path, path)
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

// buildObjectClipPath 构建对象裁剪路径并应用父级变换
// 入参: clips 裁剪对象, pageH 页面高度, bx 边界X坐标, by 边界Y坐标, objectCTM 对象CTM, parentCTM 父级CTM, boundaryInCTM 边界是否参与父级CTM
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildObjectClipPath(clips *Clips, pageH float64, bx, by float64, objectCTM Matrix, parentCTM *Matrix, boundaryInCTM bool) *canvas.Path {
	if !boundaryInCTM && parentCTM != nil {
		objectCTM = parentCTM.Multiply(objectCTM)
	}
	p := r.buildClipPath(clips, pageH, bx, by, objectCTM)
	if p != nil && boundaryInCTM && parentCTM != nil {
		p = p.Transform(canvas.Matrix{
			{parentCTM.a, -parentCTM.c, parentCTM.c*pageH + parentCTM.e},
			{-parentCTM.b, parentCTM.d, pageH*(1-parentCTM.d) - parentCTM.f},
		})
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
				ctm := areaCTM.Multiply(NewMatrix(pathObj.CTM))
				cp := r.buildPath(pathObj, pageH, ctm, true)
				cp.Translate(bx, -by)
				cp.Close()
				if pathObj.Rule == "Even-Odd" {
					cp = cp.Settle(canvas.EvenOdd)
				}
				renderer.add(cp)
			}
			for _, textObj := range area.Text {
				box, _ := ParseBox(textObj.Boundary)
				ctm := NewMatrix(textObj.CTM)
				m := areaCTM.Multiply(TranslationMatrix(box.X, box.Y)).Multiply(ctm)
				ctx := canvas.NewContext(renderer)
				ctx.SetView(canvas.Matrix{{m.a, -m.c, bx + m.e}, {-m.b, m.d, pageH - by - m.f}})
				renderer.clip = r.buildObjectClipPath(textObj.Clips, pageH, box.X, box.Y, ctm, &areaCTM, true)
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
		p = intersectClipPath(p, renderer.path)
	}
	return p
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
	parentRect, parentOK := rectangularPath(parent)
	currentRect, currentOK := rectangularPath(current)
	if parentOK && currentOK {
		return parentRect.And(currentRect).ToPath()
	}
	if parent.Empty() || current.Empty() {
		return &canvas.Path{}
	}
	if parentOK && parentRect.Contains(current.FastBounds()) {
		return current
	}
	if currentOK && currentRect.Contains(parent.FastBounds()) {
		return parent
	}
	return parent.And(current)
}

// unionClipPath 合并裁剪区域
// 入参: left 左侧裁剪路径, right 右侧裁剪路径
// 返回: *canvas.Path 合并后的裁剪路径
func unionClipPath(left, right *canvas.Path) *canvas.Path {
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
	if path.Empty() || clip.Empty() {
		return &canvas.Path{}
	}
	if rect, ok := rectangularPath(clip); ok {
		bounds := path.FastBounds()
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
	return path.And(clip)
}

// rectangularPath 获取矩形路径区域
// 入参: path 路径对象
// 返回: canvas.Rect 矩形区域, bool 是否为矩形
func rectangularPath(path *canvas.Path) (canvas.Rect, bool) {
	if path == nil || path.HasSubpaths() || !path.Closed() {
		return canvas.Rect{}, false
	}
	data := path.Data()
	for i := 0; i < len(data); i += 4 {
		if i+4 > len(data) || (data[i] != canvas.MoveToCmd && data[i] != canvas.LineToCmd && data[i] != canvas.CloseCmd) {
			return canvas.Rect{}, false
		}
	}
	points := path.Coords()
	if len(points) != 5 || !points[0].Equals(points[4]) {
		return canvas.Rect{}, false
	}
	rect := path.FastBounds()
	corners := 0
	for _, point := range points[:4] {
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
