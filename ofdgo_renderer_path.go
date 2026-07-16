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
	"math"
	"strconv"
	"strings"

	"github.com/tdewolff/canvas"
)

const defaultPathLineWidth = 0.353

type pathStyle struct {
	fillColor        color.Color
	strokeColor      color.Color
	fillPaint        any
	strokePaint      any
	fillPattern      *Pattern
	fillPatternColor color.Color
	lineWidth        float64
	lineCap          canvas.Capper
	lineJoin         canvas.Joiner
	dashOffset       float64
	dashPattern      []float64
}

// newPathStyle 创建路径样式
// 入参: defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, alpha 对象透明度
// 返回: pathStyle 路径样式
func newPathStyle(defaultFill, defaultStroke color.Color, defaultLW float64, alpha *int) pathStyle {
	style := pathStyle{
		fillColor:   colorWithAlpha(defaultFill, alpha),
		strokeColor: colorWithAlpha(defaultStroke, alpha),
		lineWidth:   defaultLW,
		lineCap:     canvas.ButtCap,
		lineJoin:    canvas.MiterJoin,
	}
	if style.lineWidth == 0 {
		style.lineWidth = defaultPathLineWidth
	}
	style.fillPaint = style.fillColor
	style.strokePaint = style.strokeColor
	return style
}

// applyFillColor 应用填充颜色
// 入参: fill 填充颜色, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, alpha 对象透明度
func (s *pathStyle) applyFillColor(fill *FillColor, bx, by, pageH float64, alpha *int) {
	fillColorNode := withFillAlpha(fill, alpha)
	s.fillPattern = fillColorNode.Pattern
	s.fillPatternColor = patternColor(fillColorNode)
	s.fillColor = parseFillColor(fillColorNode)
	s.fillPaint = parseFillPaint(fillColorNode, bx, by, pageH, 0, 0)
}

// applyStrokeColor 应用描边颜色
// 入参: stroke 描边颜色, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, alpha 对象透明度
func (s *pathStyle) applyStrokeColor(stroke *StrokeColor, bx, by, pageH float64, alpha *int) {
	strokeColorNode := withStrokeAlpha(stroke, alpha)
	s.strokeColor = parseStrokeColor(strokeColorNode)
	s.strokePaint = parseStrokePaint(strokeColorNode, bx, by, pageH, 0, 0)
}

// pathLineCap 转换线帽样式
// 入参: cap 线帽名称, fallback 默认线帽
// 返回: canvas.Capper 线帽样式
func pathLineCap(cap string, fallback canvas.Capper) canvas.Capper {
	switch cap {
	case "Round":
		return canvas.RoundCap
	case "Square":
		return canvas.SquareCap
	}
	return fallback
}

// pathLineJoin 转换线连接样式
// 入参: join 线连接名称, fallback 默认线连接
// 返回: canvas.Joiner 线连接样式
func pathLineJoin(join string, fallback canvas.Joiner) canvas.Joiner {
	switch join {
	case "Round":
		return canvas.RoundJoin
	case "Bevel":
		return canvas.BevelJoin
	}
	return fallback
}

// applyDrawParam 应用绘制参数样式
// 入参: dp 绘制参数, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, alpha 对象透明度
func (s *pathStyle) applyDrawParam(dp *DrawParam, bx, by, pageH float64, alpha *int) {
	if dp.LineWidth > 0 {
		s.lineWidth = dp.LineWidth
	}
	if dp.FillColor != nil {
		s.applyFillColor(dp.FillColor, bx, by, pageH, alpha)
	}
	if dp.StrokeColor != nil {
		s.applyStrokeColor(dp.StrokeColor, bx, by, pageH, alpha)
	}
	if dp.Cap != "" {
		s.lineCap = pathLineCap(dp.Cap, s.lineCap)
	}
	if dp.Join != "" {
		s.lineJoin = pathLineJoin(dp.Join, s.lineJoin)
	}
	if dp.DashPattern != "" {
		s.dashPattern = parseFloats(dp.DashPattern)
		s.dashOffset = dp.DashOffset
	}
}

// applyPathObject 应用路径对象样式
// 入参: obj 路径对象, bx 边界X坐标, by 边界Y坐标, pageH 页面高度
func (s *pathStyle) applyPathObject(obj PathObject, bx, by, pageH float64) {
	if obj.LineWidth > 0 {
		s.lineWidth = obj.LineWidth
	}
	if obj.FillColor != nil {
		s.applyFillColor(obj.FillColor, bx, by, pageH, obj.Alpha)
	}
	if obj.StrokeColor != nil {
		s.applyStrokeColor(obj.StrokeColor, bx, by, pageH, obj.Alpha)
	}
	if obj.Cap != "" {
		s.lineCap = pathLineCap(obj.Cap, canvas.ButtCap)
	}
	if obj.Join != "" {
		s.lineJoin = pathLineJoin(obj.Join, canvas.MiterJoin)
	}
	if obj.DashPattern != "" {
		s.dashPattern = parseFloats(obj.DashPattern)
		s.dashOffset = obj.DashOffset
	}
}

// scale 应用路径变换缩放
// 入参: ctm 变换矩阵
func (s *pathStyle) scale(ctm Matrix) {
	if scale := math.Sqrt(math.Abs(ctm.a*ctm.d - ctm.b*ctm.c)); scale > 0 {
		s.lineWidth *= scale
		s.dashOffset *= scale
		for i := range s.dashPattern {
			s.dashPattern[i] *= scale
		}
	}
}

// renderPath 渲染路径
// 入参: ctx 画布上下文, obj 路径对象, pageH 页面高度, defaultFill 默认填充色, defaultStroke 默认描边色, defaultLW 默认线宽, parentCTM 父级CTM, boundaryInCTM 边界是否参与CTM变换, parentClip 父级裁剪路径
func (r *Renderer) renderPath(ctx *canvas.Context, obj PathObject, pageH float64, defaultFill, defaultStroke color.Color, defaultLW float64, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	ctx.Push()
	bx, by := 0.0, 0.0
	if obj.Boundary != "" {
		if box, err := ParseBox(obj.Boundary); err == nil {
			bx, by = box.X, box.Y
		}
	}
	ctm := NewMatrix(obj.CTM)
	if parentCTM != nil {
		ctm = parentCTM.Multiply(ctm)
	}
	style := newPathStyle(defaultFill, defaultStroke, defaultLW, obj.Alpha)
	if obj.DrawParam != "" {
		if dp := r.getDrawParam(obj.DrawParam, nil); dp != nil {
			style.applyDrawParam(dp, bx, by, pageH, obj.Alpha)
		}
	}
	style.applyPathObject(obj, bx, by, pageH)
	style.scale(ctm)
	p := r.buildPath(obj, pageH, ctm, boundaryInCTM)
	if rectPath := r.buildTinyFillRectPath(obj, pageH, ctm, bx, by); rectPath != nil {
		p = rectPath
	}
	clipPath := intersectClipPath(parentClip, r.buildClipPath(obj.Clips, pageH, bx, by, ctm))
	shouldFill := false
	if obj.Fill != nil {
		shouldFill = *obj.Fill
	}
	if style.fillPaint == nil {
		style.fillPaint = style.fillColor
	}
	if shouldFill && style.fillPattern != nil {
		fp := p
		if clipPath != nil {
			fp = p.Copy()
			fp.Close()
			fp = applyClipPath(fp, clipPath)
		}
		r.renderPattern(ctx, style.fillPattern, style.fillPatternColor, pageH, fp, ctm, bx, by)
	} else if shouldFill && style.fillPaint != nil {
		ctx.SetFill(style.fillPaint)
		ctx.SetStrokeColor(canvas.Transparent)
		fp := p
		if clipPath != nil {
			fp = p.Copy()
			fp.Close()
			fp = applyClipPath(fp, clipPath)
		}
		ctx.DrawPath(0, 0, fp)
	}
	shouldStroke := true
	if obj.Stroke != nil {
		shouldStroke = *obj.Stroke
	}
	if shouldStroke {
		if style.strokePaint == nil {
			style.strokePaint = style.strokeColor
		}
		if style.strokePaint == nil {
			style.strokePaint = colorWithAlpha(canvas.Black, obj.Alpha)
		}
		ctx.SetFillColor(canvas.Transparent)
		ctx.SetStroke(style.strokePaint)
		ctx.SetStrokeWidth(style.lineWidth)
		ctx.SetStrokeCapper(style.lineCap)
		ctx.SetStrokeJoiner(style.lineJoin)
		if len(style.dashPattern) > 0 {
			ctx.SetDashes(style.dashOffset, style.dashPattern...)
		}
		if clipPath != nil {
			sp := p.Copy()
			if len(style.dashPattern) > 0 {
				sp = sp.Dash(style.dashOffset, style.dashPattern...)
			}
			sp = sp.Stroke(style.lineWidth, style.lineCap, style.lineJoin, canvas.Tolerance)
			sp = applyClipPath(sp, clipPath)
			ctx.SetFill(style.strokePaint)
			ctx.SetStrokeColor(canvas.Transparent)
			ctx.DrawPath(0, 0, sp)
		} else {
			ctx.DrawPath(0, 0, p)
		}
	}
	ctx.Pop()
}

// renderPattern 渲染图案填充
// 入参: ctx 画布上下文, pattern 图案对象, defaultColor 默认颜色, pageH 页面高度, clip 填充区域, parentCTM 父级CTM, bx 边界X坐标, by 边界Y坐标
func (r *Renderer) renderPattern(ctx *canvas.Context, pattern *Pattern, defaultColor color.Color, pageH float64, clip *canvas.Path, parentCTM Matrix, bx, by float64) {
	if pattern == nil || clip == nil || len(pattern.CellContent.Objects) == 0 {
		return
	}
	xStep, yStep := pattern.XStep, pattern.YStep
	if xStep == 0 {
		xStep = pattern.Width
	}
	if yStep == 0 {
		yStep = pattern.Height
	}
	if xStep <= 0 || yStep <= 0 {
		return
	}
	patternCTM := TranslationMatrix(bx, by).Multiply(parentCTM).Multiply(NewMatrix(pattern.CTM))
	invCTM, ok := patternCTM.Invert()
	if !ok {
		return
	}
	bounds := clip.FastBounds()
	points := [][2]float64{
		{bounds.X0, pageH - bounds.Y0},
		{bounds.X1, pageH - bounds.Y0},
		{bounds.X1, pageH - bounds.Y1},
		{bounds.X0, pageH - bounds.Y1},
	}
	minX, maxX := 0.0, 0.0
	minY, maxY := 0.0, 0.0
	for i, point := range points {
		x, y := invCTM.Transform(point[0], point[1])
		if i == 0 {
			minX, maxX = x, x
			minY, maxY = y, y
			continue
		}
		minX = math.Min(minX, x)
		maxX = math.Max(maxX, x)
		minY = math.Min(minY, y)
		maxY = math.Max(maxY, y)
	}
	startX := int(math.Floor(minX/xStep)) - 1
	endX := int(math.Ceil(maxX/xStep)) + 1
	startY := int(math.Floor(minY/yStep)) - 1
	endY := int(math.Ceil(maxY/yStep)) + 1
	for ix := startX; ix <= endX; ix++ {
		for iy := startY; iy <= endY; iy++ {
			tileCTM := patternCTM.Multiply(TranslationMatrix(float64(ix)*xStep, float64(iy)*yStep))
			for _, obj := range pattern.CellContent.Objects {
				r.renderObject(ctx, obj, pageH, defaultColor, defaultColor, 0, &tileCTM, false, clip)
			}
		}
	}
}

// buildPath 解析路径并返回Canvas Path
// 入参: obj 路径对象, pageH 页面高度, ctm 变换矩阵, boundaryInCTM 边界是否参与CTM变换
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildPath(obj PathObject, pageH float64, ctm Matrix, boundaryInCTM bool) *canvas.Path {
	bx, by := 0.0, 0.0
	if obj.Boundary != "" {
		if box, err := ParseBox(obj.Boundary); err == nil {
			bx, by = box.X, box.Y
		}
	}
	point := func(x, y float64) (float64, float64) {
		if boundaryInCTM {
			tx, ty := ctm.Transform(x+bx, y+by)
			return tx, pageH - ty
		}
		tx, ty := ctm.Transform(x, y)
		return tx + bx, pageH - (ty + by)
	}
	p := &canvas.Path{}
	tokens := strings.Fields(obj.AbbreviatedData)
	for i := 0; i < len(tokens); {
		cmd := tokens[i]
		i++
		switch cmd {
		case "M", "S":
			if i+1 < len(tokens) {
				x, _ := strconv.ParseFloat(tokens[i], 64)
				y, _ := strconv.ParseFloat(tokens[i+1], 64)
				tx, ty := point(x, y)
				p.MoveTo(tx, ty)
				i += 2
			}
		case "L":
			if i+1 < len(tokens) {
				x, _ := strconv.ParseFloat(tokens[i], 64)
				y, _ := strconv.ParseFloat(tokens[i+1], 64)
				tx, ty := point(x, y)
				p.LineTo(tx, ty)
				i += 2
			}
		case "B":
			if i+5 < len(tokens) {
				x1, _ := strconv.ParseFloat(tokens[i], 64)
				y1, _ := strconv.ParseFloat(tokens[i+1], 64)
				x2, _ := strconv.ParseFloat(tokens[i+2], 64)
				y2, _ := strconv.ParseFloat(tokens[i+3], 64)
				x3, _ := strconv.ParseFloat(tokens[i+4], 64)
				y3, _ := strconv.ParseFloat(tokens[i+5], 64)
				tx1, ty1 := point(x1, y1)
				tx2, ty2 := point(x2, y2)
				tx3, ty3 := point(x3, y3)
				p.CubeTo(tx1, ty1, tx2, ty2, tx3, ty3)
				i += 6
			}
		case "Q":
			if i+3 < len(tokens) {
				x1, _ := strconv.ParseFloat(tokens[i], 64)
				y1, _ := strconv.ParseFloat(tokens[i+1], 64)
				x2, _ := strconv.ParseFloat(tokens[i+2], 64)
				y2, _ := strconv.ParseFloat(tokens[i+3], 64)
				tx1, ty1 := point(x1, y1)
				tx2, ty2 := point(x2, y2)
				p.QuadTo(tx1, ty1, tx2, ty2)
				i += 4
			}
		case "A":
			if i+6 < len(tokens) {
				rx, _ := strconv.ParseFloat(tokens[i], 64)
				ry, _ := strconv.ParseFloat(tokens[i+1], 64)
				rot, _ := strconv.ParseFloat(tokens[i+2], 64)
				large, _ := strconv.ParseBool(tokens[i+3])
				sweep, _ := strconv.ParseBool(tokens[i+4])
				x, _ := strconv.ParseFloat(tokens[i+5], 64)
				y, _ := strconv.ParseFloat(tokens[i+6], 64)
				sx := math.Hypot(ctm.a, ctm.c)
				sy := math.Hypot(ctm.b, ctm.d)
				ctmRot := math.Atan2(ctm.b, ctm.a) * 180 / math.Pi
				tx, ty := point(x, y)
				sweep = !sweep
				p.ArcTo(rx*sx, ry*sy, -(rot + ctmRot), large, sweep, tx, ty)
				i += 7
			}
		case "C":
			p.Close()
		}
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
		var clipPath *canvas.Path
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
				if clipPath == nil {
					clipPath = cp
				} else {
					clipPath = unionClipPath(clipPath, cp)
				}
			}
		}
		if clipPath != nil {
			if p == nil {
				p = clipPath
			} else {
				p = intersectClipPath(p, clipPath)
			}
		}
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
		return path.Flatten(canvas.Tolerance).Clip(rect.X0, rect.Y0, rect.X1, rect.Y1)
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

// buildTinyFillRectPath 构建微小填充矩形路径
// 入参: obj 路径对象, pageH 页面高度, ctm 变换矩阵, bx 边界X坐标, by 边界Y坐标
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildTinyFillRectPath(obj PathObject, pageH float64, ctm Matrix, bx, by float64) *canvas.Path {
	if obj.Fill == nil || !*obj.Fill || obj.Stroke == nil || *obj.Stroke {
		return nil
	}
	box, err := ParseBox(obj.Boundary)
	if err != nil || box.W <= 0 || box.H <= 0 || box.W > 0.6 || box.H > 0.6 {
		return nil
	}
	tokens := strings.Fields(obj.AbbreviatedData)
	if len(tokens) != 11 || tokens[0] != "M" || tokens[3] != "L" || tokens[6] != "L" || tokens[9] != "L" || tokens[10] != "C" {
		return nil
	}
	points := make([][2]float64, 0, 4)
	for i := 1; i < 10; i += 3 {
		x, errX := strconv.ParseFloat(tokens[i], 64)
		y, errY := strconv.ParseFloat(tokens[i+1], 64)
		if errX != nil || errY != nil {
			return nil
		}
		tx, ty := ctm.Transform(x, y)
		points = append(points, [2]float64{tx + bx, pageH - (ty + by)})
	}
	minX, maxX := points[0][0], points[0][0]
	minY, maxY := points[0][1], points[0][1]
	for _, point := range points[1:] {
		minX = math.Min(minX, point[0])
		maxX = math.Max(maxX, point[0])
		minY = math.Min(minY, point[1])
		maxY = math.Max(maxY, point[1])
	}
	expand := math.Min(box.W, box.H) * 0.08
	p := &canvas.Path{}
	p.MoveTo(minX-expand, minY-expand)
	p.LineTo(maxX+expand, minY-expand)
	p.LineTo(maxX+expand, maxY+expand)
	p.LineTo(minX-expand, maxY+expand)
	p.Close()
	return p
}
