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
	"math"
	"strconv"
	"strings"
)

// semanticCompiler 将公共页面语义编译为路径和原始图片，绘制、搜索和度量共享定位
type semanticCompiler struct {
	renderer                             *Renderer
	geometry                             GeometryBackend
	page                                 *RasterPage
	text                                 PageText
	measurement                          ObjectMeasurement
	measure, contours, textOnly, pattern bool
	patterns                             *renderCache[patternCellKey, []RasterCommand]
}

// newSemanticCompiler 绑定当前字体和几何能力
// 入参: r 渲染器
// 返回: *semanticCompiler 编译器, error 能力错误
func newSemanticCompiler(r *Renderer) (*semanticCompiler, error) {
	geometry, err := r.Geometry()
	if err != nil {
		return nil, err
	}
	return &semanticCompiler{renderer: r, geometry: geometry, patterns: &renderCache[patternCellKey, []RasterCommand]{limit: 8 << 20}}, nil
}

// DrawObject 解释叶子对象，不调用任何具体页面编译器
// 入参: object 源对象, state 继承状态
// 返回: error 编译错误
func (c *semanticCompiler) DrawObject(object *GraphicObject, state RenderState) error {
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
		svg, err := closedGeometry(path).SVG()
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
	if !c.measure && paint.Kind != PaintPattern {
		return c.command(path, paint, evenOdd, clip, shadingMatrix, nil)
	}
	var err error
	if clip != nil || paint.Kind == PaintPattern || paint.Gradient != nil {
		path = closedGeometry(path)
	}
	if evenOdd && (clip != nil || paint.Kind == PaintPattern || paint.Gradient != nil) {
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
	if paint.Kind == PaintRadial {
		g.Kind = RasterRadial
		if s.Eccentricity > 0 && s.Eccentricity < 1 {
			angle := s.Angle * math.Pi / 180
			view := TranslationMatrix(s.Start.X, s.Start.Y).Multiply(Matrix{a: math.Cos(angle), b: math.Sin(angle), c: -math.Sin(angle), d: math.Cos(angle)}).Multiply(Matrix{a: 1, d: math.Sqrt(1 - s.Eccentricity*s.Eccentricity)}).Multiply(TranslationMatrix(-s.Start.X, -s.Start.Y))
			inverse, _ := view.Invert()
			g.End.X, g.End.Y = inverse.Transform(g.End.X, g.End.Y)
			matrix = matrix.Multiply(view)
		}
		if math.Hypot(g.End.X-g.Start.X, g.End.Y-g.Start.Y) >= math.Abs(g.R1-g.R0) {
			g.Spread = nil
		}
	}
	return g, matrix
}

// semanticStyle 保存已继承的路径颜色和描边参数
type semanticStyle struct {
	fill, stroke *FillColor
	options      StrokeOptions
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
		if defaults.LineWidth > 0 {
			style.options.Width = defaults.LineWidth
		}
		if defaults.Cap != "" {
			style.options.Cap = defaults.Cap
		}
		if defaults.Join != "" {
			style.options.Join = defaults.Join
		}
		if defaults.MiterLimit > 0 {
			style.options.MiterLimit = defaults.MiterLimit
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
	if object.LineWidth > 0 {
		style.options.Width = object.LineWidth
	}
	if object.Cap != "" {
		style.options.Cap = object.Cap
	}
	if object.Join != "" {
		style.options.Join = object.Join
	}
	if object.MiterLimit > 0 {
		style.options.MiterLimit = object.MiterLimit
	}
	if object.DashPattern != "" || object.dashPatternSet {
		style.options.Dashes = parseFloats(object.DashPattern)
	}
	if object.DashOffset != nil {
		style.options.DashOffset = *object.DashOffset
	}
	if style.stroke == nil {
		style.stroke = &FillColor{}
	}
	style.fill = withFillAlpha(style.fill, object.Alpha)
	style.stroke = withFillAlpha(style.stroke, object.Alpha)
	if scale := math.Sqrt(math.Abs(linear.a*linear.d - linear.b*linear.c)); scale > 0 {
		style.options.Width *= scale
		style.options.DashOffset *= scale
		for i := range style.options.Dashes {
			style.options.Dashes[i] *= scale
		}
	}
	return style
}

// pathObject 编译填充、描边和虚线路径
// 入参: object 路径对象, state 继承状态
// 返回: error 路径或画刷错误
func (c *semanticCompiler) pathObject(object PathObject, state RenderState) error {
	if object.Visible != nil && !*object.Visible {
		return nil
	}
	matrix, linear := renderObjectMatrix(object.Boundary, NewMatrix(object.CTM), state)
	local := object
	local.Boundary = "0 0 1 1"
	local.CTM = ""
	var path GeometryPath
	var err error
	tinyMatrix := matrix
	if state.BoundaryInCTM && state.Parent != nil {
		tinyMatrix, _ = renderObjectMatrix(object.Boundary, NewMatrix(object.CTM), RenderState{})
	}
	path = tinyFillRectPath(object, tinyMatrix)
	if path != nil {
		if state.BoundaryInCTM && state.Parent != nil {
			path, err = c.geometry.Transform(path, *state.Parent)
		}
	} else {
		path, err = c.geometry.Path(local)
		if err != nil {
			return err
		}
		path, err = c.geometry.Transform(path, matrix)
	}
	if err != nil {
		return err
	}
	clip, err := c.renderer.objectGeometryClip(object.Clips, object.Boundary, NewMatrix(object.CTM), state)
	if err != nil {
		return err
	}
	style := c.objectStyle(object, state.Defaults, linear)
	shading, _ := renderObjectMatrix(object.Boundary, IdentityMatrix, state)
	if object.Fill != nil && *object.Fill {
		if err := c.fill(path, c.renderer.ResolvePaint(style.fill), object.Rule == "Even-Odd", clip, matrix, shading); err != nil {
			return err
		}
	}
	if object.Stroke == nil || *object.Stroke {
		return c.stroke(path, c.renderer.ResolvePaint(style.stroke), style.options, clip, matrix, shading)
	}
	return nil
}

// textObject 共用字形定位处理绘制、原文提取和轮廓
// 入参: object 文字对象, state 继承状态
// 返回: error 字体或几何错误
func (c *semanticCompiler) textObject(object TextObject, state RenderState) error {
	if object.Visible != nil && !*object.Visible || object.Fill != nil && !*object.Fill && (object.Stroke == nil || !*object.Stroke) {
		return nil
	}
	positioned, err := c.renderer.PositionText(object, state)
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
	_, linear := renderObjectMatrix(object.Boundary, NewMatrix(object.CTM), state)
	style := c.objectStyle(PathObject{DrawParam: object.DrawParam, FillColor: object.FillColor, StrokeColor: object.StrokeColor, Alpha: object.Alpha, LineWidth: object.LineWidth, Join: object.Join, MiterLimit: object.MiterLimit}, state.Defaults, linear)
	if style.fill == nil {
		style.fill = withFillAlpha(&FillColor{}, object.Alpha)
	}
	shading, _ := renderObjectMatrix(object.Boundary, IdentityMatrix, state)
	for _, glyph := range positioned.Glyphs {
		if object.Fill == nil || *object.Fill {
			if err := c.fill(glyph.Path, c.renderer.ResolvePaint(style.fill), false, positioned.Clip, positioned.Matrix, shading); err != nil {
				return err
			}
		}
		if object.Stroke != nil && *object.Stroke && len(glyph.Path) > 0 {
			if err := c.stroke(glyph.Path, c.renderer.ResolvePaint(style.stroke), style.options, positioned.Clip, positioned.Matrix, shading); err != nil {
				return err
			}
		}
		if err := c.fill(glyph.Underline, c.renderer.ResolvePaint(style.fill), false, positioned.Clip, positioned.Matrix, shading); err != nil {
			return err
		}
	}
	return nil
}

// fillPattern 在对象裁剪区域内展开底纹，保留单元反射和继承样式
// 入参: clip 绘制区域, pattern 底纹, object 对象矩阵
// 返回: error 编译错误
func (c *semanticCompiler) fillPattern(clip GeometryPath, pattern *PatternPaint, object Matrix) error {
	xstep, ystep := math.Max(pattern.XStep, pattern.Width), math.Max(pattern.YStep, pattern.Height)
	if xstep <= 0 || ystep <= 0 {
		return fmt.Errorf("invalid pattern cell size")
	}
	matrix := NewMatrix(pattern.CTM)
	if pattern.RelativeTo != "Page" {
		matrix = object.Multiply(matrix)
	}
	inverse, ok := matrix.Invert()
	if !ok {
		return fmt.Errorf("invalid pattern transform")
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
	for x, end := int(math.Floor(box.X/xstep))-1, int(math.Ceil((box.X+box.W)/xstep))+1; x <= end; x++ {
		for y, end := int(math.Floor(box.Y/ystep))-1, int(math.Ceil((box.Y+box.H)/ystep))+1; y <= end; y++ {
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
			if err := c.appendPatternCell(commands, clip, segments); err != nil {
				return err
			}
		}
	}
	return nil
}

// tinyFillRectPath 保留历史文档中微小填充块的缺省末端处理
// 入参: object 路径对象, matrix 不含外层变换的页面矩阵
// 返回: GeometryPath 修正路径，非匹配格式时为空
func tinyFillRectPath(object PathObject, matrix Matrix) GeometryPath {
	if object.Fill == nil || !*object.Fill || object.Stroke == nil || *object.Stroke {
		return nil
	}
	box, err := ParseBox(object.Boundary)
	if err != nil || box.W <= 0 || box.H <= 0 || box.W > .6 || box.H > .6 {
		return nil
	}
	tokens := strings.Fields(object.AbbreviatedData)
	if len(tokens) != 11 || tokens[0] != "M" || tokens[3] != "L" || tokens[6] != "L" || tokens[9] != "L" || tokens[10] != "C" {
		return nil
	}
	var minX, minY, maxX, maxY float64
	for i := 1; i < 10; i += 3 {
		x, errX := strconv.ParseFloat(tokens[i], 64)
		y, errY := strconv.ParseFloat(tokens[i+1], 64)
		if errX != nil || errY != nil {
			return nil
		}
		x, y = matrix.Transform(x, y)
		if i == 1 {
			minX, maxX, minY, maxY = x, x, y, y
		} else {
			minX = math.Min(minX, x)
			maxX = math.Max(maxX, x)
			minY = math.Min(minY, y)
			maxY = math.Max(maxY, y)
		}
	}
	expand := math.Min(box.W, box.H) * .08
	return geometryRectangle(Box{X: minX - expand, Y: minY - expand, W: maxX - minX + 2*expand, H: maxY - minY + 2*expand})
}
