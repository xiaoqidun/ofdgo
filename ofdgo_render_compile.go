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
	"strconv"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
)

// maxPatternTiles 限制单次底纹填充展开的最大平铺单元数，防止极小步长引发拒绝服务
const maxPatternTiles = 262144

// OFDCompiler 使用库自有页面语义，字体和几何操作交给当前后端配置
type OFDCompiler struct{}

// Name 返回页面编译器标识
// 返回: string 编译器标识
func (OFDCompiler) Name() string { return "ofd" }

// CompilePage 使用公共OFD语义和配置的字形、几何能力编译页面
// 入参: r 渲染器, page 页面
// 返回: *RasterPage 绘制页面, error 编译错误
func (OFDCompiler) CompilePage(r *Renderer, page *PageContent) (*RasterPage, error) {
	defer r.bindPageResources(page)()
	box, err := r.GetPageBox(page)
	if err != nil {
		return nil, err
	}
	c, err := newSemanticCompiler(r)
	if err != nil {
		return nil, err
	}
	c.page = &RasterPage{Width: box.W, Height: box.H, DPI: r.DPI}
	if _, _, err := c.page.PixelSize(); err != nil {
		return nil, err
	}
	if box.X != 0 || box.Y != 0 {
		shift := TranslationMatrix(-box.X, -box.Y)
		c.patternPage = &shift
	}
	if !r.TransparentBackground {
		if err := c.fill(GeometryPath(basegeom.Rectangle(Box{W: box.W, H: box.H})), Paint{Kind: PaintSolid, Color: color.RGBA{255, 255, 255, 255}}, false, nil, IdentityMatrix, IdentityMatrix); err != nil {
			return nil, err
		}
	}
	if err := r.WalkPage(page, c); err != nil {
		return nil, err
	}
	if r.OnPageText != nil {
		r.OnPageText(page, &c.text)
	}
	return c.page, nil
}

// PageText 提取与页面绘制相同定位的原文，不读取图片像素
// 入参: r 渲染器, page 页面
// 返回: *PageText 页面文字, error 定位错误
func (OFDCompiler) PageText(r *Renderer, page *PageContent) (*PageText, error) {
	defer r.bindPageResources(page)()
	if _, err := r.GetPageBox(page); err != nil {
		return nil, err
	}
	c, err := newSemanticCompiler(r)
	if err != nil {
		return nil, err
	}
	c.textOnly = true
	if err := r.WalkPage(page, c); err != nil {
		return nil, err
	}
	return &c.text, nil
}

// MeasureObject 复用文字和图形编译规则收集范围，不分配页面像素
// 入参: r 渲染器, object 对象, options 度量上下文
// 返回: ObjectMeasurement 对象范围与轮廓, error 度量错误
func (OFDCompiler) MeasureObject(r *Renderer, object GraphicObject, options MeasureOptions) (ObjectMeasurement, error) {
	if len(r.pageResources) > 0 {
		defer r.bindParsedResources(r.pageResources)()
	}
	c, err := newSemanticCompiler(r)
	if err != nil {
		return ObjectMeasurement{}, err
	}
	boundary, ctm := editorGeometry(object)
	if _, err := creationBox(boundary); err != nil {
		return ObjectMeasurement{}, err
	}
	if ctm != "" {
		if _, err := creationNumbers(ctm, 6); err != nil {
			return ObjectMeasurement{}, err
		}
	}
	c.measure = true
	c.contours = options.Contours
	err = r.WalkObject(&object, RenderState{Defaults: options.Defaults, Parent: options.Parent, BoundaryInCTM: options.BoundaryInCTM, Clip: options.Clip}, c)
	return c.measurement, err
}

// semanticStyle 保存已继承的路径颜色、页面描边参数与对象局部描边参数
type semanticStyle struct {
	fill, stroke *FillColor
	options      StrokeOptions
	localOptions StrokeOptions
}

// semanticCompiler 将公共页面语义编译为路径和原始图片，绘制、搜索和度量共享定位
type semanticCompiler struct {
	renderer                             *Renderer
	geometry                             GeometryBackend
	page                                 *RasterPage
	text                                 PageText
	measurement                          ObjectMeasurement
	measure, contours, textOnly, pattern bool
	patterns                             *renderCache[patternCellKey, []RasterCommand]
	patternReferences                    map[*Pattern]bool
	patternPage                          *Matrix
}

// newSemanticCompiler 绑定当前字体和几何能力
// 入参: r 渲染器
// 返回: *semanticCompiler 编译器, error 能力错误
func newSemanticCompiler(r *Renderer) (*semanticCompiler, error) {
	geometry, err := r.Geometry()
	if err != nil {
		return nil, err
	}
	return &semanticCompiler{renderer: r, geometry: geometry, patterns: resourceCache[patternCellKey, []RasterCommand](r.Reader.resources, 8<<20)}, nil
}

// DrawObject 解释叶子对象，不调用任何具体页面编译器
// 入参: object 源对象, state 继承状态
// 返回: error 编译错误
func (c *semanticCompiler) DrawObject(object *GraphicObject, state RenderState) error {
	if state.Hidden {
		return nil
	}
	switch object.Type {
	case "TextObject":
		return c.textObject(object.TextObject, state)
	case "PathObject":
		if !c.textOnly {
			return c.pathObject(object.PathObject, state)
		}
	case "ImageObject":
		if !c.textOnly {
			return c.imageObject(object.ImageObject, state)
		}
	default:
		return fmt.Errorf("unsupported graphic object %q", object.Type)
	}
	return nil
}

// BeginTransparencyGroup 在非度量且非纯文本模式下开启离屏透明组
// 入参: alpha 对象级透明度
// 返回: bool 是否开启了透明组
func (c *semanticCompiler) BeginTransparencyGroup(alpha uint8) bool {
	if c.measure || c.textOnly || c.page == nil {
		return false
	}
	c.page.Commands = append(c.page.Commands, RasterCommand{Group: RasterGroupBegin, GroupAlpha: alpha})
	return true
}

// EndTransparencyGroup 结束当前离屏透明组
func (c *semanticCompiler) EndTransparencyGroup() {
	if c.page != nil {
		c.page.Commands = append(c.page.Commands, RasterCommand{Group: RasterGroupEnd})
	}
}

// segments 转换光栅曲线，仅在输出边界展开椭圆弧
// 入参: path 页面路径
// 返回: []RasterSegment 光栅路径, error 能力或路径错误
func (c *semanticCompiler) segments(path GeometryPath) ([]RasterSegment, error) {
	for _, segment := range path {
		if segment.Verb == GeometryArc {
			curves, ok := c.geometry.(GeometryCurves)
			if !ok {
				return nil, fmt.Errorf("geometry curves: %w", ErrBackendUnavailable)
			}
			var err error
			path, err = curves.Curves(path)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	result := make([]RasterSegment, 0, len(path))
	for _, s := range path {
		segment := RasterSegment{End: RasterPoint{s.End.X, s.End.Y}, Control1: RasterPoint{s.Control1.X, s.Control1.Y}, Control2: RasterPoint{s.Control2.X, s.Control2.Y}}
		switch s.Verb {
		case GeometryMove:
			segment.Verb = RasterMove
		case GeometryLine:
			segment.Verb = RasterLine
		case GeometryQuad:
			segment.Verb = RasterQuad
		case GeometryCubic:
			segment.Verb = RasterCubic
		case GeometryClose:
			segment.Verb = RasterClose
		default:
			return nil, fmt.Errorf("unsupported raster geometry %d", s.Verb)
		}
		result = append(result, segment)
	}
	return result, nil
}

// addBounds 收集精确范围和独立轮廓，不分配页面像素
// 入参: path 页面路径, evenOdd 奇偶规则
// 返回: error 几何错误
func (c *semanticCompiler) addBounds(path GeometryPath, evenOdd bool) error {
	if len(path) == 0 {
		return nil
	}
	box, err := c.geometry.Bounds(path)
	if err != nil {
		return err
	}
	c.measurement.Bounds = unionTextBox(c.measurement.Bounds, box)
	if c.contours {
		svg, err := GeometryPath(basegeom.Close(basegeom.Path(path))).SVG()
		if err != nil {
			return err
		}
		c.measurement.Contours = append(c.measurement.Contours, ObjectContour{Path: svg, EvenOdd: evenOdd})
	}
	return nil
}

// fill 应用对象裁剪、底纹和渐变，不改变输入路径
// 入参: path 页面路径, paint 画刷, evenOdd 奇偶规则, clip 对象裁剪, matrix 图案矩阵, shadingMatrix 渐变矩阵
// 返回: error 几何或编译错误
func (c *semanticCompiler) fill(path GeometryPath, paint Paint, evenOdd bool, clip *GeometryPath, matrix, shadingMatrix Matrix) error {
	if len(path) == 0 || paint.Kind == PaintNone || paint.Kind == PaintSolid && paint.Color.A == 0 {
		return nil
	}
	path = GeometryPath(basegeom.Close(basegeom.Path(path)))
	if !c.measure && paint.Kind != PaintPattern {
		return c.command(path, paint, evenOdd, clip, shadingMatrix, nil)
	}
	var err error
	if evenOdd && (clip != nil || paint.Kind == PaintPattern || paint.Gradient != nil || paint.Mesh != nil) {
		path, err = c.geometry.Normalize(path, true)
		if err != nil {
			return err
		}
		evenOdd = false
	}
	path, err = clipGeometry(c.geometry, path, clip)
	if err != nil || len(path) == 0 {
		return err
	}
	if paint.Gradient != nil {
		path, err = c.shadingClip(path, paint, shadingMatrix)
		if err != nil || len(path) == 0 {
			return err
		}
	}
	if c.measure {
		if paint.Mesh != nil && paint.Mesh.Background.A == 0 {
			outline, err := c.geometry.Normalize(paint.Mesh.outline(), false)
			if err != nil {
				return err
			}
			outline, err = c.geometry.Transform(outline, shadingMatrix)
			if err != nil {
				return err
			}
			path, err = clipGeometry(c.geometry, path, &outline)
			if err != nil {
				return err
			}
		}
		return c.addBounds(path, evenOdd)
	}
	if paint.Kind == PaintPattern {
		return c.fillPattern(path, paint.Pattern, matrix)
	}
	return nil
}

// command 保留路径、画刷与绘制裁剪，不提前求几何交集或展开描边
// 入参: path 页面路径, paint 画刷, evenOdd 奇偶规则, clip 绘制裁剪, shadingMatrix 渐变矩阵, stroke 页面描边样式
// 返回: error 路径或矩阵错误
func (c *semanticCompiler) command(path GeometryPath, paint Paint, evenOdd bool, clip *GeometryPath, shadingMatrix Matrix, stroke *StrokeOptions) error {
	var err error
	command := RasterCommand{Paint: RasterPaint{Color: paint.Color}, EvenOdd: evenOdd, Transform: RasterMatrix{1, 0, 0, 1, 0, 0}}
	command.Stroke = stroke
	if clip != nil {
		command.Clip, err = c.segments(*clip)
		if err != nil {
			return err
		}
	}
	if paint.Gradient != nil {
		gradient, view := semanticGradient(paint, shadingMatrix)
		inverse, ok := view.Invert()
		if !ok {
			return fmt.Errorf("invalid shading transform")
		}
		path, err = c.geometry.Transform(path, inverse)
		if err != nil {
			return err
		}
		command.Transform = RasterMatrix(view.Values())
		command.Paint.Gradient = gradient
	}
	if paint.Mesh != nil {
		inverse, ok := shadingMatrix.Invert()
		if !ok {
			return fmt.Errorf("invalid mesh shading transform")
		}
		path, err = c.geometry.Transform(path, inverse)
		if err != nil {
			return err
		}
		command.Transform = RasterMatrix(shadingMatrix.Values())
		command.Paint.Gradient = &RasterGradient{Kind: RasterMesh, Mesh: paint.Mesh}
	}
	command.Path, err = c.segments(path)
	if err != nil {
		return err
	}
	c.page.Commands = append(c.page.Commands, command)
	return nil
}

// stroke 将普通描边交给绘制后端，度量和底纹仍使用精确几何轮廓
// 入参: path 页面路径, paint 画刷, options 页面描边样式, clip 对象裁剪, matrix 图案矩阵, shadingMatrix 渐变矩阵
// 返回: error 描边或编译错误
func (c *semanticCompiler) stroke(path GeometryPath, paint Paint, options StrokeOptions, clip *GeometryPath, matrix, shadingMatrix Matrix) error {
	if len(path) == 0 || paint.Kind == PaintNone || paint.Kind == PaintSolid && paint.Color.A == 0 {
		return nil
	}
	if !c.measure && paint.Kind != PaintPattern {
		return c.command(path, paint, false, clip, shadingMatrix, &options)
	}
	outline, err := c.geometry.Stroke(path, options)
	if err != nil {
		return err
	}
	return c.fill(outline, paint, false, clip, matrix, shadingMatrix)
}

// objectStyle 合并继承与对象样式，不修改共享绘制参数
// 入参: object 路径样式, defaults 继承样式, linear 组合变换
// 返回: semanticStyle 解析样式
func (c *semanticCompiler) objectStyle(object PathObject, defaults *DrawParam, linear Matrix) semanticStyle {
	defaults = c.renderer.drawParamDefaults(object.DrawParam, defaults)
	style := semanticStyle{options: StrokeOptions{Width: defaultPathLineWidth, Cap: "Butt", Join: "Miter", MiterLimit: defaultMiterLimit}}
	if defaults != nil {
		style.fill = defaults.FillColor
		style.stroke = (*FillColor)(defaults.StrokeColor)
		if defaults.LineWidth > 0 || defaults.LineWidthSet {
			style.options.Width = defaults.LineWidth
		}
		if defaults.Cap != "" {
			style.options.Cap = defaults.Cap
		}
		if defaults.Join != "" {
			style.options.Join = defaults.Join
		}
		if defaults.MiterLimit > 0 || defaults.MiterLimitSet {
			style.options.MiterLimit = defaults.MiterLimit
			style.options.MiterLimitSet = defaults.MiterLimitSet
		}
		style.options.Dashes = parseFloats(defaults.DashPattern)
		if defaults.DashOffset != nil {
			style.options.DashOffset = *defaults.DashOffset
		}
	}
	if object.FillColor != nil {
		style.fill = object.FillColor
	}
	if object.StrokeColor != nil {
		style.stroke = (*FillColor)(object.StrokeColor)
	}
	if object.LineWidth > 0 || object.LineWidthSet {
		style.options.Width = object.LineWidth
	}
	if object.Cap != "" {
		style.options.Cap = object.Cap
	}
	if object.Join != "" {
		style.options.Join = object.Join
	}
	if object.MiterLimit > 0 || object.MiterLimitSet {
		style.options.MiterLimit = object.MiterLimit
		style.options.MiterLimitSet = object.MiterLimitSet
	}
	if object.DashPattern != "" || object.dashPatternSet {
		style.options.Dashes = parseFloats(object.DashPattern)
	}
	if object.DashOffset != nil {
		style.options.DashOffset = *object.DashOffset
	}
	style.fill = withFillAlpha(style.fill, object.Alpha)
	style.stroke = withFillAlpha(style.stroke, object.Alpha)
	style.localOptions = style.options
	style.localOptions.Width = c.renderer.localStrokeWidth(style.localOptions.Width, linear)
	if len(style.options.Dashes) > 0 {
		style.options.Dashes = append([]float64(nil), style.options.Dashes...)
	}
	if scale := math.Sqrt(math.Abs(linear.a*linear.d - linear.b*linear.c)); scale > 0 {
		style.options.Width *= scale
		style.options.DashOffset *= scale
		for i := range style.options.Dashes {
			style.options.Dashes[i] *= scale
		}
	}
	style.options.Width = c.renderer.deviceLineWidth(style.options.Width)
	return style
}

// pathObject 编译填充、描边和虚线路径
// 入参: object 路径对象, state 继承状态
// 返回: error 路径或画刷错误
func (c *semanticCompiler) pathObject(object PathObject, state RenderState) error {
	if object.Visible != nil && !*object.Visible {
		return nil
	}
	shouldFill := object.Fill != nil && *object.Fill
	shouldStroke := object.Stroke == nil || *object.Stroke
	if !shouldFill && !shouldStroke {
		return nil
	}
	objAlpha := fillAlphaByte(object.Alpha)
	if !c.measure && objAlpha == 0 {
		return nil
	}
	matrix, linear := renderObjectMatrix(object.Boundary, NewMatrix(object.CTM), state)
	local := object
	local.Boundary = "0 0 1 1"
	local.CTM = ""
	localPath, err := c.geometry.Path(local)
	if err != nil {
		return err
	}
	path, err := c.geometry.Transform(localPath, matrix)
	if err != nil {
		return err
	}
	clip, err := c.renderer.objectGeometryClip(object.Clips, object.Boundary, NewMatrix(object.CTM), state)
	if err != nil {
		return err
	}
	useGroup := !c.measure && shouldFill && shouldStroke && objAlpha < 255
	styleObj := object
	if useGroup {
		styleObj.Alpha = nil
	}
	style := c.objectStyle(styleObj, state.Defaults, linear)
	shading, _ := renderObjectMatrix(object.Boundary, IdentityMatrix, state)
	var fillPaint Paint
	if shouldFill {
		fillPaint, err = c.renderer.ResolvePaint(style.fill)
		if err != nil {
			return err
		}
	}
	var strokePaint Paint
	if shouldStroke {
		strokePaint = Paint{Kind: PaintSolid, Color: color.RGBA{A: fillAlphaByte(styleObj.Alpha)}}
		if style.stroke != nil {
			strokePaint, err = c.renderer.ResolvePaint(style.stroke)
			if err != nil {
				return err
			}
		}
	}
	if useGroup && (!paintVisible(fillPaint) || !paintVisible(strokePaint)) {
		useGroup = false
		style = c.objectStyle(object, state.Defaults, linear)
		if shouldFill {
			fillPaint, err = c.renderer.ResolvePaint(style.fill)
			if err != nil {
				return err
			}
		}
		if shouldStroke {
			strokePaint = Paint{Kind: PaintSolid, Color: color.RGBA{A: objAlpha}}
			if style.stroke != nil {
				strokePaint, err = c.renderer.ResolvePaint(style.stroke)
				if err != nil {
					return err
				}
			}
		}
	}
	if useGroup {
		c.BeginTransparencyGroup(objAlpha)
		defer c.EndTransparencyGroup()
	}
	if shouldFill {
		if err := c.fill(path, fillPaint, object.Rule == "Even-Odd", clip, shading, shading); err != nil {
			return err
		}
	}
	if shouldStroke {
		if !matrixIsSimilarity(linear) && len(localPath) > 0 && paintVisible(strokePaint) {
			outline, err := c.geometry.Stroke(localPath, style.localOptions)
			if err != nil {
				return err
			}
			outline, err = c.geometry.Transform(outline, matrix)
			if err != nil {
				return err
			}
			return c.fill(outline, strokePaint, false, clip, shading, shading)
		}
		return c.stroke(path, strokePaint, style.options, clip, shading, shading)
	}
	return nil
}

// textObject 共用字形定位处理绘制、原文提取和轮廓
// 入参: object 文字对象, state 继承状态
// 返回: error 字体或几何错误
func (c *semanticCompiler) textObject(object TextObject, state RenderState) error {
	shouldFill := object.Fill == nil || *object.Fill
	shouldStroke := object.Stroke != nil && *object.Stroke
	if object.Visible != nil && !*object.Visible || !shouldFill && !shouldStroke {
		return nil
	}
	positioned, err := c.renderer.positionText(object, state, c.textOnly && c.renderer.textContentOnly)
	if err != nil {
		return err
	}
	if !c.pattern && positioned.Run.Text != "" {
		c.text.Runs = append(c.text.Runs, positioned.Run)
	}
	if c.textOnly {
		return nil
	}
	if c.measure && !c.contours {
		for _, box := range positioned.Run.Boxes {
			c.measurement.Bounds = unionTextBox(c.measurement.Bounds, box)
		}
		return nil
	}
	objAlpha := fillAlphaByte(object.Alpha)
	if !c.measure && objAlpha == 0 {
		return nil
	}
	_, linear := renderObjectMatrix(object.Boundary, NewMatrix(object.CTM), state)
	useGroup := !c.measure && shouldFill && shouldStroke && objAlpha < 255 && len(positioned.Glyphs) > 0
	styleObj := object.pathStyle()
	if useGroup {
		styleObj.Alpha = nil
	}
	style := c.objectStyle(styleObj, state.Defaults, linear)
	shading, _ := renderObjectMatrix(object.Boundary, IdentityMatrix, state)
	fill := Paint{Kind: PaintSolid, Color: color.RGBA{A: fillAlphaByte(styleObj.Alpha)}}
	if style.fill != nil {
		fill, err = c.renderer.ResolvePaint(style.fill)
		if err != nil {
			return err
		}
	}
	var stroke Paint
	if shouldStroke {
		stroke, err = c.renderer.ResolvePaint(style.stroke)
		if err != nil {
			return err
		}
	}
	if useGroup && (!paintVisible(fill) || !paintVisible(stroke)) {
		useGroup = false
		style = c.objectStyle(object.pathStyle(), state.Defaults, linear)
		fill = Paint{Kind: PaintSolid, Color: color.RGBA{A: objAlpha}}
		if style.fill != nil {
			fill, err = c.renderer.ResolvePaint(style.fill)
			if err != nil {
				return err
			}
		}
		if shouldStroke {
			stroke, err = c.renderer.ResolvePaint(style.stroke)
			if err != nil {
				return err
			}
		}
	}
	if useGroup {
		c.BeginTransparencyGroup(objAlpha)
		defer c.EndTransparencyGroup()
	}
	nonUniform := shouldStroke && !matrixIsSimilarity(linear)
	var invLinear Matrix
	if nonUniform {
		var ok bool
		invLinear, ok = linear.Invert()
		if !ok {
			nonUniform = false
		}
	}
	for _, glyph := range positioned.Glyphs {
		if shouldFill {
			if err := c.fill(glyph.Path, fill, false, positioned.Clip, shading, shading); err != nil {
				return err
			}
		}
		if shouldStroke && len(glyph.Path) > 0 {
			if nonUniform && paintVisible(stroke) {
				localGlyph, err := c.geometry.Transform(glyph.Path, invLinear)
				if err != nil {
					return err
				}
				outline, err := c.geometry.Stroke(localGlyph, style.localOptions)
				if err != nil {
					return err
				}
				outline, err = c.geometry.Transform(outline, linear)
				if err != nil {
					return err
				}
				if err := c.fill(outline, stroke, false, positioned.Clip, shading, shading); err != nil {
					return err
				}
				continue
			}
			if err := c.stroke(glyph.Path, stroke, style.options, positioned.Clip, shading, shading); err != nil {
				return err
			}
		}
	}
	return nil
}

// fillPattern 在对象裁剪区域内展开底纹，保留单元反射和继承样式
// 入参: clip 绘制区域, pattern 底纹, object 对象原点矩阵
// 返回: error 编译错误
func (c *semanticCompiler) fillPattern(clip GeometryPath, pattern *PatternPaint, object Matrix) error {
	xstep, ystep := pattern.XStep, pattern.YStep
	if xstep <= 0 {
		xstep = pattern.Width
	}
	if ystep <= 0 {
		ystep = pattern.Height
	}
	if xstep <= 0 || ystep <= 0 {
		return fmt.Errorf("invalid pattern cell size")
	}
	matrix := NewMatrix(pattern.CTM)
	if pattern.RelativeTo != "Page" {
		matrix = object.Multiply(matrix)
	} else if c.patternPage != nil {
		matrix = c.patternPage.Multiply(matrix)
	}
	inverse, ok := matrix.Invert()
	if !ok {
		return fmt.Errorf("invalid pattern transform")
	}
	if pattern.Width <= 0 || pattern.Height <= 0 || pattern.CellContent.empty() {
		return nil
	}
	box, err := c.geometry.Bounds(clip)
	if err != nil {
		return err
	}
	box = inverse.TransformBox(box)
	segments, err := c.segments(clip)
	if err != nil {
		return err
	}
	startXF := math.Floor((box.X-pattern.Width)/xstep) - 1
	endXF := math.Ceil((box.X+box.W)/xstep) + 1
	startYF := math.Floor((box.Y-pattern.Height)/ystep) - 1
	endYF := math.Ceil((box.Y+box.H)/ystep) + 1
	if !finite(startXF) || !finite(endXF) || !finite(startYF) || !finite(endYF) {
		return fmt.Errorf("invalid pattern bounds")
	}
	countX, countY := endXF-startXF+1, endYF-startYF+1
	if countX <= 0 || countY <= 0 {
		return nil
	}
	if countX > maxPatternTiles || countY > maxPatternTiles || countX*countY > maxPatternTiles {
		return fmt.Errorf("pattern tile count exceeds limit")
	}
	for x, end := int(startXF), int(endXF); x <= end; x++ {
		for y, end := int(startYF), int(endYF); y <= end; y++ {
			tile := matrix.Multiply(TranslationMatrix(float64(x)*xstep, float64(y)*ystep))
			if x%2 != 0 && (pattern.ReflectMethod == "Column" || pattern.ReflectMethod == "RowAndColumn") {
				tile = tile.Multiply(Matrix{a: -1, d: 1, e: pattern.Width})
			}
			if y%2 != 0 && (pattern.ReflectMethod == "Row" || pattern.ReflectMethod == "RowAndColumn") {
				tile = tile.Multiply(Matrix{a: 1, d: -1, f: pattern.Height})
			}
			commands, err := c.patternCell(pattern, tile)
			if err != nil {
				return err
			}
			if err := c.appendClippedCommands(commands, clip, segments); err != nil {
				return err
			}
		}
	}
	return nil
}

// shadingClip 按渐变延伸规则裁剪绘制与选区，避免只在采样时隐藏区间外像素
// 入参: path 页面路径, paint 渐变画刷, matrix 渐变父矩阵
// 返回: GeometryPath 可见路径, error 几何错误
func (c *semanticCompiler) shadingClip(path GeometryPath, paint Paint, matrix Matrix) (GeometryPath, error) {
	gradient, view := semanticGradient(paint, matrix)
	spread := gradient.Spread
	if gradient.Kind == RasterRadial && math.Hypot(gradient.End.X-gradient.Start.X, gradient.End.Y-gradient.Start.Y) >= math.Abs(gradient.R1-gradient.R0) {
		return path, nil
	}
	if spread == nil || spread.Extend == 3 {
		return path, nil
	}
	inverse, ok := view.Invert()
	if !ok {
		return nil, nil
	}
	local, err := c.geometry.Transform(path, inverse)
	if err != nil {
		return nil, err
	}
	if gradient.Kind == RasterLinear {
		dx, dy := gradient.End.X-gradient.Start.X, gradient.End.Y-gradient.Start.Y
		axis := Matrix{a: dx, b: dy, c: -dy, d: dx, e: gradient.Start.X, f: gradient.Start.Y}
		inv, _ := axis.Invert()
		box, err := c.geometry.Bounds(local)
		if err != nil {
			return nil, err
		}
		box = inv.TransformBox(box)
		end := box.X + box.W
		if spread.Extend&1 == 0 {
			box.X = math.Max(box.X, 0)
		}
		if spread.Extend&2 == 0 {
			end = math.Min(end, 1)
		}
		box.W = end - box.X
		if box.W <= 0 {
			return nil, nil
		}
		clip, err := c.geometry.Transform(GeometryPath(basegeom.Rectangle(box)), axis)
		if err != nil {
			return nil, err
		}
		local, err = c.geometry.Combine(local, clip, GeometryIntersect)
		if err != nil {
			return nil, err
		}
	} else {
		inner, outer, r0, r1, extend := gradient.Start, gradient.End, gradient.R0, gradient.R1, spread.Extend
		if r1 < r0 {
			inner, outer, r0, r1 = outer, inner, r1, r0
			extend = (extend&1)<<1 | (extend&2)>>1
		}
		if extend&2 == 0 {
			local, err = c.geometry.Combine(local, geometryCircle(outer, r1), GeometryIntersect)
			if err != nil {
				return nil, err
			}
		}
		if extend&1 == 0 && r0 > 0 {
			local, err = c.geometry.Combine(local, geometryCircle(inner, r0), GeometrySubtract)
			if err != nil {
				return nil, err
			}
		}
	}
	return c.geometry.Transform(local, view)
}

// paintVisible 判断画刷是否产生可见输出
// 入参: paint 画刷
// 返回: bool 是否可见
func paintVisible(paint Paint) bool {
	return paint.Kind != PaintNone && (paint.Kind != PaintSolid || paint.Color.A != 0)
}

// matrixIsSimilarity 判断二维线性矩阵是否为非退化相似变换
// 入参: m 变换矩阵
// 返回: bool 是否为等比正交变换
func matrixIsSimilarity(m Matrix) bool {
	s1 := m.a*m.a + m.b*m.b
	s2 := m.c*m.c + m.d*m.d
	if s1 <= 0 || s2 <= 0 {
		return false
	}
	dot := m.a*m.c + m.b*m.d
	ref := max(s1, s2, 1e-12)
	return math.Abs(s1-s2) <= 1e-6*ref && math.Abs(dot) <= 1e-6*ref
}

// localStrokeWidth 按页面设备最小线宽约束折算对象局部描边线宽
// 入参: width 对象局部线宽, linear 线性变换矩阵
// 返回: float64 修正后的局部线宽
func (r *Renderer) localStrokeWidth(width float64, linear Matrix) float64 {
	scale := math.Sqrt(math.Abs(linear.a*linear.d - linear.b*linear.c))
	if scale <= 0 {
		return r.deviceLineWidth(width)
	}
	if width == 0 {
		return r.deviceLineWidth(0) / scale
	}
	if width > 0 {
		minWidth := r.deviceLineWidth(width*scale) / scale
		if width < minWidth {
			return minWidth
		}
	}
	return width
}

// semanticGradient 保留渐变周期、延伸与椭圆坐标
// 入参: paint 渐变画刷, matrix 渐变父矩阵
// 返回: *RasterGradient 光栅渐变, Matrix 局部到页面矩阵
func semanticGradient(paint Paint, matrix Matrix) (*RasterGradient, Matrix) {
	s := paint.Gradient
	extend, _ := strconv.Atoi(s.Extend)
	period := 1.0
	length := math.Hypot(s.End.X-s.Start.X, s.End.Y-s.Start.Y)
	if s.MapUnit > 0 && length > 0 {
		period = s.MapUnit / length
	}
	g := &RasterGradient{Start: RasterPoint{s.Start.X, s.Start.Y}, End: RasterPoint{s.End.X, s.End.Y}, R0: s.StartRadius, R1: s.EndRadius, Stops: s.Stops, Spread: &RasterSpread{Extend: extend, MapType: s.MapType, Period: period}}
	g.Sample = s.Sample
	if paint.Kind == PaintRadial {
		g.Kind = RasterRadial
		if s.Eccentricity > 0 && s.Eccentricity < 1 {
			angle := s.Angle * math.Pi / 180
			view := TranslationMatrix(s.Start.X, s.Start.Y).Multiply(Matrix{a: math.Cos(angle), b: math.Sin(angle), c: -math.Sin(angle), d: math.Cos(angle)}).Multiply(Matrix{a: 1, d: math.Sqrt(1 - s.Eccentricity*s.Eccentricity)}).Multiply(TranslationMatrix(-s.Start.X, -s.Start.Y))
			inverse, _ := view.Invert()
			g.End.X, g.End.Y = inverse.Transform(g.End.X, g.End.Y)
			matrix = matrix.Multiply(view)
		}
	}
	return g, matrix
}

// reflectShdPosition 获取镜像周期内的位置
// 入参: position 渐变位置
// 返回: float64 周期内的位置
func reflectShdPosition(position float64) float64 {
	index := math.Floor(position)
	position -= index
	if math.Mod(index, 2) != 0 {
		return 1 - position
	}
	return position
}

// geometryCircle 构建标准圆弧，不提前折线化
// 入参: center 圆心, radius 半径
// 返回: GeometryPath 圆路径
func geometryCircle(center RasterPoint, radius float64) GeometryPath {
	return GeometryPath{
		{Verb: GeometryMove, End: Point{X: center.X + radius, Y: center.Y}},
		{Verb: GeometryArc, RadiusX: radius, RadiusY: radius, Sweep: true, End: Point{X: center.X - radius, Y: center.Y}},
		{Verb: GeometryArc, RadiusX: radius, RadiusY: radius, Sweep: true, End: Point{X: center.X + radius, Y: center.Y}},
		{Verb: GeometryClose},
	}
}

// fillAlphaByte 将可选对象透明度转换为预乘黑色Alpha字节
// 入参: alpha 对象透明度
// 返回: uint8 Alpha字节
func fillAlphaByte(alpha *int) uint8 {
	if alpha == nil {
		return 255
	}
	return uint8(clampColor(*alpha))
}
