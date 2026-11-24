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
	"fmt"
	"image/color"
	"math"
	"slices"
	"strings"

	"github.com/tdewolff/canvas"
)

// pathStyle 路径绘制样式
type pathStyle struct {
	fillColor     color.Color
	strokeColor   color.Color
	fillPaint     any
	strokePaint   any
	fillPattern   *PatternPaint
	strokePattern *PatternPaint
	lineWidth     float64
	lineCap       canvas.Capper
	lineJoin      canvas.Joiner
	lineJoinName  string
	miterLimit    float64
	dashOffset    float64
	dashPattern   []float64
}

// pathLineCap 转换端点样式
// 入参: cap 端点样式名称, fallback 默认端点样式
// 返回: canvas.Capper 端点样式
func pathLineCap(cap string, fallback canvas.Capper) canvas.Capper {
	switch cap {
	case "Butt":
		return canvas.ButtCap
	case "Round":
		return canvas.RoundCap
	case "Square":
		return canvas.SquareCap
	}
	return fallback
}

// applyFillColor 应用填充颜色
// 入参: r 渲染器, fill 填充颜色, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, alpha 对象透明度
func (s *pathStyle) applyFillColor(r *Renderer, fill *FillColor, bx, by, pageH float64, alpha *int) {
	fillColorNode := withFillAlpha(fill, alpha)
	s.fillPattern = parsePatternPaint(fillColorNode)
	s.fillColor = r.parseFillColor(fillColorNode)
	s.fillPaint = r.parseFillPaint(fillColorNode, bx, by, pageH)
}

// applyStrokeColor 应用描边颜色
// 入参: r 渲染器, stroke 描边颜色, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, alpha 对象透明度
func (s *pathStyle) applyStrokeColor(r *Renderer, stroke *StrokeColor, bx, by, pageH float64, alpha *int) {
	strokeColorNode := withStrokeAlpha(stroke, alpha)
	s.strokePattern = parsePatternPaint((*FillColor)(strokeColorNode))
	s.strokeColor = r.parseStrokeColor(strokeColorNode)
	s.strokePaint = r.parseStrokePaint(strokeColorNode, bx, by, pageH)
}

// applyLineJoin 应用转角样式及尖角截断值
// 入参: join 转角样式名称, limit 尖角截断值, explicit 是否显式指定零截断值
func (s *pathStyle) applyLineJoin(join string, limit float64, explicit bool) {
	if limit > 0 || explicit {
		s.miterLimit = limit
	}
	if s.lineJoinName == "" {
		switch s.lineJoin.(type) {
		case canvas.RoundJoiner:
			s.lineJoinName = "Round"
		case canvas.BevelJoiner:
			s.lineJoinName = "Bevel"
		default:
			s.lineJoinName = "Miter"
		}
	}
	switch join {
	case "Miter", "Round", "Bevel":
		s.lineJoinName = join
	}
	switch s.lineJoinName {
	case "Round":
		s.lineJoin = canvas.RoundJoin
	case "Bevel":
		s.lineJoin = canvas.BevelJoin
	default:
		s.lineJoin = canvas.MiterJoiner{GapJoiner: canvas.BevelJoin, Limit: s.miterLimit}
		if s.miterLimit <= 1 {
			s.lineJoin = canvas.BevelJoin
		}
	}
}

// applyDrawParam 应用绘制参数样式
// 入参: r 渲染器, dp 绘制参数, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, alpha 对象透明度
func (s *pathStyle) applyDrawParam(r *Renderer, dp *DrawParam, bx, by, pageH float64, alpha *int) {
	if dp.LineWidth > 0 || dp.LineWidthSet {
		s.lineWidth = dp.LineWidth
	}
	if dp.FillColor != nil {
		s.applyFillColor(r, dp.FillColor, bx, by, pageH, alpha)
	}
	if dp.StrokeColor != nil {
		s.applyStrokeColor(r, dp.StrokeColor, bx, by, pageH, alpha)
	}
	if dp.Cap != "" {
		s.lineCap = pathLineCap(dp.Cap, s.lineCap)
	}
	s.applyLineJoin(dp.Join, dp.MiterLimit, dp.MiterLimitSet)
	if dp.DashPattern != "" || dp.DashPatternSet {
		s.dashPattern = parseFloats(dp.DashPattern)
	}
	if dp.DashOffset != nil {
		s.dashOffset = *dp.DashOffset
	}
}

// applyPathObject 应用路径对象样式
// 入参: r 渲染器, obj 路径对象, bx 边界X坐标, by 边界Y坐标, pageH 页面高度
func (s *pathStyle) applyPathObject(r *Renderer, obj PathObject, bx, by, pageH float64) {
	if obj.LineWidth > 0 || obj.LineWidthSet {
		s.lineWidth = obj.LineWidth
	}
	if obj.FillColor != nil {
		s.applyFillColor(r, obj.FillColor, bx, by, pageH, obj.Alpha)
	}
	if obj.StrokeColor != nil {
		s.applyStrokeColor(r, obj.StrokeColor, bx, by, pageH, obj.Alpha)
	}
	if obj.Cap != "" {
		s.lineCap = pathLineCap(obj.Cap, s.lineCap)
	}
	s.applyLineJoin(obj.Join, obj.MiterLimit, obj.MiterLimitSet)
	if obj.DashPattern != "" || obj.DashPatternSet {
		s.dashPattern = parseFloats(obj.DashPattern)
	}
	if obj.DashOffset != nil {
		s.dashOffset = *obj.DashOffset
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

// newPathStyle 创建路径样式
// 入参: dp 默认绘制参数, bx 边界X坐标, by 边界Y坐标, pageH 页面高度, alpha 对象透明度
// 返回: pathStyle 路径样式
func (r *Renderer) newPathStyle(dp *DrawParam, bx, by, pageH float64, alpha *int) pathStyle {
	style := pathStyle{
		lineWidth:  defaultPathLineWidth,
		lineCap:    canvas.ButtCap,
		lineJoin:   canvas.MiterJoiner{GapJoiner: canvas.BevelJoin, Limit: defaultMiterLimit},
		miterLimit: defaultMiterLimit,
	}
	if dp != nil {
		style.applyDrawParam(r, dp, bx, by, pageH, alpha)
	}
	return style
}

// renderPath 渲染路径
// 入参: ctx 画布上下文, obj 路径对象, pageH 页面高度, defaults 默认绘制参数, parentCTM 父级CTM, boundaryInCTM 边界是否参与CTM变换, parentClip 父级裁剪路径
func (r *Renderer) renderPath(ctx *canvas.Context, obj PathObject, pageH float64, defaults *DrawParam, parentCTM *Matrix, boundaryInCTM bool, parentClip *canvas.Path) {
	if r.textOnly {
		return
	}
	if bounds, ok := ctx.Renderer.(*boundsRenderer); ok {
		compiler, err := newSemanticCompiler(r)
		if err != nil {
			r.renderError = err
			return
		}
		compiler.measure, compiler.contours = true, bounds.collect
		err = compiler.pathObject(obj, RenderState{Defaults: defaults, Parent: parentCTM, BoundaryInCTM: boundaryInCTM, Clip: geometryFromCanvasPath(parentClip)})
		if err != nil {
			r.renderError = err
			return
		}
		bounds.box = unionTextBox(bounds.box, compiler.measurement.Bounds)
		bounds.contours = append(bounds.contours, compiler.measurement.Contours...)
		return
	}
	if obj.Visible != nil && !*obj.Visible {
		return
	}
	ctx.Push()
	shouldFill := false
	if obj.Fill != nil {
		shouldFill = *obj.Fill
	}
	shouldStroke := true
	if obj.Stroke != nil {
		shouldStroke = *obj.Stroke
	}
	if !shouldFill && !shouldStroke {
		ctx.Pop()
		return
	}
	objAlpha := fillAlphaByte(obj.Alpha)
	if objAlpha == 0 {
		ctx.Pop()
		return
	}
	useGroup := canCanvasTransparencyGroup(ctx.Renderer) && shouldFill && shouldStroke && objAlpha < 255
	styleObj := obj
	if useGroup {
		styleObj.Alpha = nil
	}
	bx, by := 0.0, 0.0
	if obj.Boundary != "" {
		if box, err := ParseBox(obj.Boundary); err == nil {
			bx, by = box.X, box.Y
		}
	}
	localCTM := NewMatrix(obj.CTM)
	ctm := localCTM
	if parentCTM != nil {
		ctm = parentCTM.Multiply(ctm)
	}
	buildStyle := func(src PathObject) (pathStyle, float64, float64, []float64) {
		st := r.newPathStyle(defaults, bx, by, pageH, src.Alpha)
		if src.DrawParam != "" {
			if dp := r.getDrawParam(src.DrawParam, nil); dp != nil {
				st.applyDrawParam(r, dp, bx, by, pageH, src.Alpha)
			}
		}
		st.applyPathObject(r, src, bx, by, pageH)
		lw := st.lineWidth
		do := st.dashOffset
		dpPat := slices.Clone(st.dashPattern)
		st.scale(ctm)
		st.lineWidth = r.deviceLineWidth(st.lineWidth)
		transformShdPaint(st.fillPaint, parentCTM, bx, by, pageH, boundaryInCTM)
		transformShdPaint(st.strokePaint, parentCTM, bx, by, pageH, boundaryInCTM)
		if st.fillPaint == nil {
			st.fillPaint = st.fillColor
		}
		return st, lw, do, dpPat
	}
	style, localLineWidth, localDashOffset, localDashPattern := buildStyle(styleObj)
	patternOrigin := TranslationMatrix(bx, by)
	if parentCTM != nil {
		if boundaryInCTM {
			patternOrigin = parentCTM.Multiply(patternOrigin)
		} else {
			patternOrigin = patternOrigin.Multiply(*parentCTM)
		}
	}
	pathCTM := ctm
	if boundaryInCTM {
		pathCTM = localCTM
	}
	p := r.buildPath(obj, pageH, pathCTM, false)
	if boundaryInCTM && parentCTM != nil {
		p = p.Transform(canvas.Matrix{
			{parentCTM.a, -parentCTM.c, parentCTM.c*pageH + parentCTM.e},
			{-parentCTM.b, parentCTM.d, pageH*(1-parentCTM.d) - parentCTM.f},
		})
	}
	clipPath := r.intersectCachedClipPath(parentClip, r.buildObjectClipPath(obj.Clips, pageH, obj.Boundary, localCTM, parentCTM, boundaryInCTM))
	if useGroup {
		if endGroup, ok := beginCanvasTransparencyGroup(ctx.Renderer, objAlpha); ok {
			defer endGroup()
		} else {
			styleObj = obj
			style, localLineWidth, localDashOffset, localDashPattern = buildStyle(styleObj)
		}
	}
	if shouldFill {
		fillPaint, shadingClip, fillView := resolveShdPaint(ctx, style.fillPaint)
		fillClip := r.intersectCachedClipPath(clipPath, shadingClip)
		paths := canvas.Paths(p.Copy().Split())
		for _, path := range paths {
			path.Close()
		}
		fp := paths.Merge()
		fillRule := canvas.NonZero
		if fillClip != nil && !fp.Bounds().Overlaps(fillClip.Bounds()) {
			fp = &canvas.Path{}
		}
		if obj.Rule == "Even-Odd" {
			fillRule = canvas.EvenOdd
			if bounds, ok := rectangularPath(fillClip); ok && bounds.Contains(fp.Bounds()) {
				fillClip = nil
			}
			if fillClip != nil || style.fillPattern != nil {
				fp = r.settleCanvasPath(fp, fillRule)
			}
		}
		if fillClip != nil {
			fp = applyClipPath(fp, fillClip)
		}
		if style.fillPattern != nil {
			r.renderPattern(ctx, style.fillPattern, pageH, fp, patternOrigin)
		} else if style.fillPaint != nil {
			ctx.SetFillRule(fillRule)
			ctx.SetFill(fillPaint)
			ctx.SetStrokeColor(canvas.Transparent)
			r.drawShdPath(ctx, fp, fillView)
		}
	}
	if shouldStroke {
		ctx.SetFillRule(canvas.NonZero)
		if style.strokePaint == nil {
			style.strokePaint = style.strokeColor
		}
		if style.strokePaint == nil {
			style.strokePaint = colorWithAlpha(canvas.Black, styleObj.Alpha)
		}
		strokePaint, shadingClip, strokeView := resolveShdPaint(ctx, style.strokePaint)
		ctx.SetFillColor(canvas.Transparent)
		ctx.SetStroke(strokePaint)
		ctx.SetStrokeWidth(style.lineWidth)
		ctx.SetStrokeCapper(style.lineCap)
		ctx.SetStrokeJoiner(style.lineJoin)
		strokeClip := r.intersectCachedClipPath(clipPath, shadingClip)
		if !matrixIsSimilarity(ctm) {
			localP, err := r.parseCanvasPath(obj.AbbreviatedData)
			if err != nil {
				r.renderError = err
				ctx.Pop()
				return
			}
			localOpts := canvasStrokeOptions(r.localStrokeWidth(localLineWidth, ctm), style.lineCap, style.lineJoin, canvas.Tolerance)
			localOpts.DashOffset, localOpts.Dashes = localDashOffset, localDashPattern
			sp := r.strokeClippedCanvasPath(localP, nil, localOpts, false)
			fullMatrix := TranslationMatrix(bx, by).Multiply(ctm)
			if boundaryInCTM && parentCTM != nil {
				fullMatrix = parentCTM.Multiply(TranslationMatrix(bx, by)).Multiply(localCTM)
			}
			if canvas.Equal(fullMatrix.a*fullMatrix.d-fullMatrix.b*fullMatrix.c, 0) {
				sp = sp.ReplaceArcs()
			}
			sp = sp.Transform(canvas.Matrix{{fullMatrix.a, fullMatrix.c, fullMatrix.e}, {-fullMatrix.b, -fullMatrix.d, pageH - fullMatrix.f}})
			if strokeClip != nil {
				sp = applyClipPath(sp, strokeClip)
			}
			if style.strokePattern != nil {
				r.renderPattern(ctx, style.strokePattern, pageH, sp, patternOrigin)
			} else {
				ctx.SetFill(strokePaint)
				ctx.SetStrokeColor(canvas.Transparent)
				r.drawShdPath(ctx, sp, strokeView)
			}
			ctx.Pop()
			return
		}
		sp := p
		zeroDash := slices.Contains(style.dashPattern, 0)
		if len(style.dashPattern) > 0 && !zeroDash {
			sp = sp.Dash(style.dashOffset, style.dashPattern...)
			ctx.SetDashes(0)
		}
		if bounds, ok := rectangularPath(strokeClip); ok {
			margin := style.lineWidth / 2 * math.Max(math.Sqrt2, style.miterLimit)
			if finite(margin) && bounds.Contains(sp.Bounds().Expand(margin)) {
				strokeClip = nil
			}
		}
		_, repeat := strokePaint.(*repeatAxialGradient)
		_, sampled := strokePaint.(rasterGradientCanvas)
		if strokeClip != nil || strokeView != canvas.Identity || repeat || sampled || style.strokePattern != nil || !canvasNativeStroke(r.backends.Geometry) || zeroDash {
			fill := style.strokePattern == nil && canvasFillClip(strokeClip)
			options := canvasStrokeOptions(style.lineWidth, style.lineCap, style.lineJoin, canvas.Tolerance)
			if zeroDash {
				options.DashOffset, options.Dashes = style.dashOffset, style.dashPattern
			}
			sp = r.strokeClippedCanvasPath(sp, strokeClip, options, fill)
			if style.strokePattern != nil {
				r.renderPattern(ctx, style.strokePattern, pageH, sp, patternOrigin)
			} else {
				ctx.SetFill(strokePaint)
				ctx.SetStrokeColor(canvas.Transparent)
				r.drawShdPath(ctx, sp, strokeView)
			}
		} else {
			ctx.DrawPath(0, 0, sp)
		}
	}
	ctx.Pop()
}

// renderPattern 渲染图案填充并施加底纹单元宽高边界裁剪，不以外层颜色覆盖单元默认样式
// 入参: ctx 画布上下文, pattern 底纹画刷, pageH 页面高度, clip 绘制区域, patternOrigin 对象空间原点矩阵
func (r *Renderer) renderPattern(ctx *canvas.Context, pattern *PatternPaint, pageH float64, clip *canvas.Path, patternOrigin Matrix) {
	if bounds, ok := ctx.Renderer.(*boundsRenderer); ok {
		if pattern.Alpha == nil || *pattern.Alpha > 0 {
			bounds.add(clip)
		}
		return
	}
	if r.pageText != nil {
		text := r.pageText
		r.pageText = nil
		defer func() { r.pageText = text }()
	}
	if clip == nil || clip.Empty() || pattern.Width <= 0 || pattern.Height <= 0 || pattern.CellContent.empty() {
		return
	}
	if r.patternReferences[pattern.Pattern] {
		r.renderError = fmt.Errorf("cyclic pattern paint")
		return
	}
	if r.patternReferences == nil {
		r.patternReferences = make(map[*Pattern]bool)
	}
	r.patternReferences[pattern.Pattern] = true
	parentPage := r.patternPage
	defer func() {
		delete(r.patternReferences, pattern.Pattern)
		r.patternPage = parentPage
	}()
	xStep, yStep := pattern.XStep, pattern.YStep
	if xStep <= 0 {
		xStep = pattern.Width
	}
	if yStep <= 0 {
		yStep = pattern.Height
	}
	if xStep <= 0 || yStep <= 0 {
		return
	}
	patternCTM := NewMatrix(pattern.CTM)
	if pattern.RelativeTo != "Page" {
		patternCTM = patternOrigin.Multiply(patternCTM)
	} else if parentPage != nil {
		patternCTM = parentPage.Multiply(patternCTM)
	}
	invCTM, ok := patternCTM.Invert()
	if !ok {
		return
	}
	bounds := clip.Bounds()
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
	startXF := math.Floor((minX-pattern.Width)/xStep) - 1
	endXF := math.Ceil(maxX/xStep) + 1
	startYF := math.Floor((minY-pattern.Height)/yStep) - 1
	endYF := math.Ceil(maxY/yStep) + 1
	if !finite(startXF) || !finite(endXF) || !finite(startYF) || !finite(endYF) {
		r.renderError = fmt.Errorf("invalid pattern bounds")
		return
	}
	countX, countY := endXF-startXF+1, endYF-startYF+1
	if countX <= 0 || countY <= 0 {
		return
	}
	if countX > maxPatternTiles || countY > maxPatternTiles || countX*countY > maxPatternTiles {
		r.renderError = fmt.Errorf("pattern tile count exceeds limit")
		return
	}
	patternAlpha := fillAlphaByte(pattern.Alpha)
	if patternAlpha == 0 {
		return
	}
	objectCount := 0
	for range pattern.CellContent.objects() {
		objectCount++
		if objectCount > 1 {
			break
		}
	}
	useGroup := canCanvasTransparencyGroup(ctx.Renderer) && patternAlpha < 255 && objectCount > 1
	var endGroup func()
	grouped := false
	if useGroup {
		endGroup, grouped = beginCanvasTransparencyGroup(ctx.Renderer, patternAlpha)
		if grouped {
			defer endGroup()
		}
	}
	startX, endX := int(startXF), int(endXF)
	startY, endY := int(startYF), int(endYF)
	if compiler, ok := ctx.Renderer.(*canvasPageCompiler); ok && countX*countY > 1 && r.renderPatternCompiled(ctx, compiler, pattern, patternCTM, xStep, yStep, startX, endX, startY, endY, pageH, clip, grouped) {
		return
	}
	for ix := startX; ix <= endX; ix++ {
		for iy := startY; iy <= endY; iy++ {
			tileCTM := patternCTM.Multiply(TranslationMatrix(float64(ix)*xStep, float64(iy)*yStep))
			if ix%2 != 0 && (pattern.ReflectMethod == "Column" || pattern.ReflectMethod == "RowAndColumn") {
				tileCTM = tileCTM.Multiply(Matrix{a: -1, d: 1, e: pattern.Width})
			}
			if iy%2 != 0 && (pattern.ReflectMethod == "Row" || pattern.ReflectMethod == "RowAndColumn") {
				tileCTM = tileCTM.Multiply(Matrix{a: 1, d: -1, f: pattern.Height})
			}
			cellClip := canvas.Rectangle(pattern.Width, pattern.Height).Transform(canvas.Matrix{{tileCTM.a, tileCTM.c, tileCTM.e}, {-tileCTM.b, -tileCTM.d, pageH - tileCTM.f}})
			tileClip := r.intersectCachedClipPath(clip, cellClip)
			if tileClip != nil && tileClip.Empty() {
				continue
			}
			r.patternPage = &tileCTM
			for obj := range pattern.CellContent.objects() {
				if !grouped {
					obj = mergeGraphicObjectAlpha(obj, pattern.Alpha)
				}
				r.renderObject(ctx, &obj, pageH, nil, &tileCTM, true, tileClip)
				if r.renderError != nil {
					return
				}
			}
		}
	}
}

// renderPatternCompiled 在光栅编译后端复用单个底纹单元的已编译命令，按瓦片平移展开
// 入参: ctx 绘制上下文, compiler 光栅页面编译器, pattern 底纹画刷, patternCTM 底纹基础变换矩阵, xStep X向步距, yStep Y向步距, startX X向起始瓦片索引, endX X向结束瓦片索引, startY Y向起始瓦片索引, endY Y向结束瓦片索引, pageH 页面高度, clip 裁剪路径, grouped 是否已启用外层透明组
// 返回: bool 是否完成快速编译展开
func (r *Renderer) renderPatternCompiled(ctx *canvas.Context, compiler *canvasPageCompiler, pattern *PatternPaint, patternCTM Matrix, xStep, yStep float64, startX, endX, startY, endY int, pageH float64, clip *canvas.Path, grouped bool) bool {
	if ctx.View() != canvas.Identity || ctx.CoordSystemView() != canvas.Identity {
		return false
	}
	if r.patternContentUsesPagePattern(pattern.CellContent) {
		return false
	}
	clipRect, clipIsRect := rectangularPath(clip)
	clipBounds := clip.Bounds()
	type cellTemplate struct {
		ready    bool
		commands []RasterCommand
		bounds   canvas.Rect
	}
	var templates [4]cellTemplate
	compileCell := func(flipX, flipY bool) *cellTemplate {
		idx := 0
		if flipX {
			idx |= 1
		}
		if flipY {
			idx |= 2
		}
		tpl := &templates[idx]
		if tpl.ready {
			return tpl
		}
		baseCTM := patternCTM
		if flipX {
			baseCTM = baseCTM.Multiply(Matrix{a: -1, d: 1, e: pattern.Width})
		}
		if flipY {
			baseCTM = baseCTM.Multiply(Matrix{a: 1, d: -1, f: pattern.Height})
		}
		cellClip := canvas.Rectangle(pattern.Width, pattern.Height).Transform(canvas.Matrix{{baseCTM.a, baseCTM.c, baseCTM.e}, {-baseCTM.b, -baseCTM.d, pageH - baseCTM.f}})
		tpl.bounds = cellClip.Bounds()
		subPage := &RasterPage{Width: compiler.page.Width, Height: compiler.page.Height, DPI: compiler.page.DPI}
		subCompiler := &canvasPageCompiler{page: subPage, geometry: compiler.geometry}
		subCtx := canvas.NewContext(subCompiler)
		r.patternPage = &baseCTM
		for obj := range pattern.CellContent.objects() {
			if !grouped {
				obj = mergeGraphicObjectAlpha(obj, pattern.Alpha)
			}
			r.renderObject(subCtx, &obj, pageH, nil, &baseCTM, true, cellClip)
			if r.renderError != nil {
				return tpl
			}
		}
		if subCompiler.err != nil {
			compiler.err = subCompiler.err
			return tpl
		}
		tpl.commands = subPage.Commands
		tpl.ready = true
		return tpl
	}
	reflectCol := pattern.ReflectMethod == "Column" || pattern.ReflectMethod == "RowAndColumn"
	reflectRow := pattern.ReflectMethod == "Row" || pattern.ReflectMethod == "RowAndColumn"
	for ix := startX; ix <= endX; ix++ {
		if r.pageContext != nil {
			if err := r.pageContext.Err(); err != nil {
				r.renderError = err
				return true
			}
		}
		flipX := reflectCol && ix%2 != 0
		for iy := startY; iy <= endY; iy++ {
			flipY := reflectRow && iy%2 != 0
			ox, oy := float64(ix)*xStep, float64(iy)*yStep
			dx := patternCTM.a*ox + patternCTM.c*oy
			dy := patternCTM.b*ox + patternCTM.d*oy
			tpl := compileCell(flipX, flipY)
			if r.renderError != nil || compiler.err != nil {
				return true
			}
			tileBounds := canvas.Rect{X0: tpl.bounds.X0 + dx, Y0: tpl.bounds.Y0 - dy, X1: tpl.bounds.X1 + dx, Y1: tpl.bounds.Y1 - dy}
			if !clipBounds.Overlaps(tileBounds) {
				continue
			}
			if clipIsRect && clipRect.Contains(tileBounds) {
				for i := range tpl.commands {
					cmd := tpl.commands[i]
					cmd.Transform[4] += dx
					cmd.Transform[5] += dy
					if len(cmd.Clip) > 0 {
						translated := make([]RasterSegment, len(cmd.Clip))
						for j, seg := range cmd.Clip {
							seg.End.X += dx
							seg.End.Y += dy
							seg.Control1.X += dx
							seg.Control1.Y += dy
							seg.Control2.X += dx
							seg.Control2.Y += dy
							translated[j] = seg
						}
						cmd.Clip = translated
					}
					compiler.page.Commands = append(compiler.page.Commands, cmd)
				}
				continue
			}
			tileCTM := patternCTM.Multiply(TranslationMatrix(ox, oy))
			if flipX {
				tileCTM = tileCTM.Multiply(Matrix{a: -1, d: 1, e: pattern.Width})
			}
			if flipY {
				tileCTM = tileCTM.Multiply(Matrix{a: 1, d: -1, f: pattern.Height})
			}
			cellClip := canvas.Rectangle(pattern.Width, pattern.Height).Transform(canvas.Matrix{{tileCTM.a, tileCTM.c, tileCTM.e}, {-tileCTM.b, -tileCTM.d, pageH - tileCTM.f}})
			tileClip := r.intersectCachedClipPath(clip, cellClip)
			if tileClip != nil && tileClip.Empty() {
				continue
			}
			r.patternPage = &tileCTM
			for obj := range pattern.CellContent.objects() {
				if !grouped {
					obj = mergeGraphicObjectAlpha(obj, pattern.Alpha)
				}
				r.renderObject(ctx, &obj, pageH, nil, &tileCTM, true, tileClip)
				if r.renderError != nil {
					return true
				}
			}
		}
	}
	return true
}

// patternContentUsesPagePattern 检测底纹单元内部是否嵌套引用了页面相对底纹
// 入参: content 底纹单元内容
// 返回: bool 是否引用页面相对底纹
func (r *Renderer) patternContentUsesPagePattern(content PatternContent) bool {
	var seen map[*CompositeGraphicUnit]bool
	var seenPatterns map[*Pattern]bool
	var hasPagePattern func(*FillColor) bool
	var checkSeq func(func(func(GraphicObject) bool)) bool
	hasPagePattern = func(fc *FillColor) bool {
		if fc == nil || fc.Pattern == nil {
			return false
		}
		if fc.Pattern.RelativeTo == "Page" {
			return true
		}
		if seenPatterns[fc.Pattern] {
			return false
		}
		if seenPatterns == nil {
			seenPatterns = make(map[*Pattern]bool)
		}
		seenPatterns[fc.Pattern] = true
		defer delete(seenPatterns, fc.Pattern)
		return checkSeq(fc.Pattern.CellContent.objects())
	}
	checkDrawParam := func(id string) bool {
		if id == "" {
			return false
		}
		dp := r.drawParamDefaults(id, nil)
		if dp == nil {
			return false
		}
		return hasPagePattern(dp.FillColor) || hasPagePattern((*FillColor)(dp.StrokeColor))
	}
	checkSeq = func(seq func(func(GraphicObject) bool)) bool {
		for member := range seq {
			switch member.Type {
			case "PathObject":
				if hasPagePattern(member.PathObject.FillColor) || hasPagePattern((*FillColor)(member.PathObject.StrokeColor)) || checkDrawParam(member.PathObject.DrawParam) {
					return true
				}
			case "TextObject":
				if hasPagePattern(member.TextObject.FillColor) || hasPagePattern((*FillColor)(member.TextObject.StrokeColor)) || checkDrawParam(member.TextObject.DrawParam) {
					return true
				}
			case "ImageObject":
				if member.ImageObject.Border != nil && hasPagePattern((*FillColor)(member.ImageObject.Border.BorderColor)) {
					return true
				}
			case "CompositeObject":
				if member.CompositeObject.ResourceID != "" {
					if child := r.compositeDefinition(member.CompositeObject.ResourceID); child != nil && !seen[child] {
						if seen == nil {
							seen = make(map[*CompositeGraphicUnit]bool)
						}
						seen[child] = true
						found := checkSeq(child.objects())
						delete(seen, child)
						if found {
							return true
						}
					}
				}
			}
		}
		return false
	}
	return checkSeq(content.objects())
}

// buildPath 解析路径并返回Canvas Path
// 入参: obj 路径对象, pageH 页面高度, ctm 变换矩阵, boundaryInCTM 边界是否参与CTM变换
// 返回: *canvas.Path 路径对象
func (r *Renderer) buildPath(obj PathObject, pageH float64, ctm Matrix, boundaryInCTM bool) *canvas.Path {
	p, err := r.parseCanvasPath(obj.AbbreviatedData)
	if err != nil {
		r.renderError = err
		return &canvas.Path{}
	}
	box, _ := ParseBox(obj.Boundary)
	if boundaryInCTM {
		ctm = ctm.Multiply(TranslationMatrix(box.X, box.Y))
	} else {
		ctm = TranslationMatrix(box.X, box.Y).Multiply(ctm)
	}
	if canvas.Equal(ctm.a*ctm.d-ctm.b*ctm.c, 0) {
		p = p.ReplaceArcs()
	}
	return p.Transform(canvas.Matrix{{ctm.a, ctm.c, ctm.e}, {-ctm.b, -ctm.d, pageH - ctm.f}})
}

// parseCanvasPath 复用局部路径解析，返回独立副本且不缓存错误或超预算结果
// 入参: data 紧缩路径
// 返回: *canvas.Path 局部路径, error 解析错误
func (r *Renderer) parseCanvasPath(data string) (*canvas.Path, error) {
	key := canvasPathKey{data: data, epsilon: canvas.Epsilon}
	cache := r.canvasParsedPaths()
	if path, ok := cache.get(key); ok {
		return path.Copy(), nil
	}
	geometry, err := ParseGeometryPath(data)
	if err != nil {
		return nil, err
	}
	path, err := canvasObjectPath(geometry)
	if err != nil {
		return nil, err
	}
	if cost := len(data) + len(path.Data())*8 + 256; cost <= cache.limit {
		key.data = strings.Clone(data)
		cache.put(key, path.Copy(), cost)
	}
	return path, nil
}
