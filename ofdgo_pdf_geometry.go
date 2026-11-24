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
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image/color"
	"math"
	"reflect"
	"strconv"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfPointBounds 逐点汇总边界，不保留文字或路径的临时坐标数组
type pdfPointBounds struct {
	minX, minY, maxX, maxY float64
	found                  bool
}

// pdfClipPath 保存导入器只读的裁剪几何，不缓存对象位移或文字裁剪
type pdfClipPath struct {
	box  Box
	data string
	path *[1]PathObject
}

// pdfGeometryPath 转换PDF路径并衔接OFD几何类型
// 入参: ctx 取消上下文, source PDF路径, matrix 坐标变换
// 返回: GeometryPath 独立几何路径, error 指令、坐标或取消错误
func pdfGeometryPath(ctx context.Context, source pdfgo.Path, matrix pdfgo.Matrix) (GeometryPath, error) {
	path, err := codecpdf.GeometryPath(ctx, source, matrix)
	return fromBaseGeomPath(path), err
}

// pdfGlyphPath 将OFD字形几何交给PDF路径适配器转换
// 入参: outline 字形轮廓, matrix 字形到页面的变换
// 返回: pdfgo.Path 页面路径, error 非字体轮廓指令
func pdfGlyphPath(outline GeometryPath, matrix pdfgo.Matrix) (pdfgo.Path, error) {
	return codecpdf.GlyphPath(toBaseGeomPath(outline), matrix)
}

// pdfNumbers 按OFD数值格式序列化坐标
// 入参: values 数值列表
// 返回: string 空格分隔的数值
func pdfNumbers(values ...float64) string {
	var buffer [128]byte
	data := buffer[:0]
	for n, v := range values {
		if n != 0 {
			data = append(data, ' ')
		}
		data = strconv.AppendFloat(data, v, 'g', -1, 64)
	}
	return string(data)
}

// pdfDashPattern 换算PDF虚线长度，将单值数组展开为等长线段与间隔
// 入参: dash PDF虚线数组, scale 坐标缩放比例
// 返回: string OFD虚线数组
func pdfDashPattern(dash []float64, scale float64) string {
	if len(dash) == 0 {
		return ""
	}
	values := make([]float64, max(2, len(dash)))
	for i, value := range dash {
		values[i] = value * scale
	}
	if len(dash) == 1 {
		values[1] = values[0]
	}
	return pdfNumbers(values...)
}

// pdfBoundary 序列化对象边界
// 入参: b 对象边界
// 返回: string OFD边界数据
func pdfBoundary(b Box) string { return pdfNumbers(b.X, b.Y, b.W, b.H) }

// pdfBounds 计算点集合的包围框
// 入参: points 坐标点集合
// 返回: Box 包围框
func pdfBounds(points []pdfgo.Point) Box {
	var bounds pdfPointBounds
	for _, point := range points {
		bounds.add(point)
	}
	return bounds.box()
}

// pdfPaintedPathEmpty 判断无坐标路径，不排除具有坐标的退化描边
// 入参: path PDF路径
// 返回: bool 是否没有坐标
func pdfPaintedPathEmpty(path pdfgo.Path) bool {
	for _, segment := range path.Segments {
		if segment.Operator != "C" || len(segment.Points) != 0 {
			return false
		}
	}
	return true
}

// pdfPageClip 将导入裁剪区还原到页面坐标，仅提取包含大量重复路径数据的裁剪
// 入参: object 导入对象
// 返回: *Clips 页面坐标裁剪，不满足提取条件时为空
func pdfPageClip(object GraphicObject) *Clips {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" {
		return nil
	}
	clips := *editorObjectClips(&object)
	if clips == nil || clips.usesObjectMatrix() || len(clips.Clip) == 0 {
		return nil
	}
	boundary, _ := editorGeometry(object)
	box, err := ParseBox(boundary)
	if err != nil {
		return nil
	}
	translation := ""
	if box.X != 0 || box.Y != 0 {
		translation = pdfNumbers(1, 0, 0, 1, -box.X, -box.Y)
	}
	bytes := 0
	result := &Clips{Clip: make([]Clip, len(clips.Clip))}
	for index, clip := range clips.Clip {
		if len(clip.Area) != 1 || clip.Area[0].CTM != translation {
			return nil
		}
		area := clip.Area[0]
		area.CTM = ""
		result.Clip[index].Area = []ClipArea{area}
		for _, path := range area.Path {
			bytes += len(path.AbbreviatedData)
		}
	}
	if bytes < 2048 {
		return nil
	}
	return result
}

// pdfMatchesPageClip 比较页面坐标裁剪，不将相同外接矩形视为相同路径
// 入参: object 导入对象, pageClip 已还原的裁剪区
// 返回: bool 是否可共享裁剪区
func pdfMatchesPageClip(object GraphicObject, pageClip *Clips) bool {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" {
		return false
	}
	clips := *editorObjectClips(&object)
	if clips == nil || clips.usesObjectMatrix() || len(clips.Clip) != len(pageClip.Clip) {
		return false
	}
	boundary, _ := editorGeometry(object)
	box, err := ParseBox(boundary)
	if err != nil {
		return false
	}
	translation := ""
	if box.X != 0 || box.Y != 0 {
		translation = pdfNumbers(1, 0, 0, 1, -box.X, -box.Y)
	}
	for index, clip := range clips.Clip {
		if len(clip.Area) != 1 || clip.Area[0].CTM != translation {
			return false
		}
		area, expected := clip.Area[0], pageClip.Clip[index].Area[0]
		if area.DrawParam != expected.DrawParam || !reflect.DeepEqual(area.Path, expected.Path) || !reflect.DeepEqual(area.Text, expected.Text) {
			return false
		}
	}
	return true
}

// add 按原顺序累计一个控制点的最小和最大坐标
// 入参: point 坐标点
func (b *pdfPointBounds) add(point pdfgo.Point) {
	if !b.found {
		b.minX, b.minY, b.maxX, b.maxY = point.X, point.Y, point.X, point.Y
		b.found = true
	}
	b.minX, b.minY = math.Min(b.minX, point.X), math.Min(b.minY, point.Y)
	b.maxX, b.maxY = math.Max(b.maxX, point.X), math.Max(b.maxY, point.Y)
}

// box 取得原精度边界，空集合仍返回零值
// 返回: Box 坐标范围
func (b *pdfPointBounds) box() Box {
	if !b.found {
		return Box{}
	}
	return Box{X: b.minX, Y: b.minY, W: math.Max(b.maxX-b.minX, 1e-6), H: math.Max(b.maxY-b.minY, 1e-6)}
}

// compileCoverage 直接编译无文字裁剪的填充覆盖，其他语义交给配置的页面编译器
// 入参: node 图元, stroke 是否描边
// 返回: pdfCompositeGeometry 覆盖场景与延迟裁剪, error 编译错误
func (p *pdfImporter) compileCoverage(node pdfCompositeNode, stroke bool) (pdfCompositeGeometry, error) {
	if node.path != nil && pdfPaintedPathEmpty(node.path.Path) {
		return pdfCompositeGeometry{}, p.emptyPaintedPath()
	}
	fallback := func() (pdfCompositeGeometry, error) {
		scene, box, err := p.compileGroup(func(local *pdfImporter) error { return node.coverage(local, stroke) })
		return pdfCompositeGeometry{scene: scene, box: box}, err
	}
	if stroke || node.path == nil && node.image == nil {
		return fallback()
	}
	deferredClip := false
	switch p.editor.Backends().Compiler.(type) {
	case DefaultBackend, *DefaultBackend:
	case OFDCompiler, *OFDCompiler:
		deferredClip = true
	default:
		return fallback()
	}
	style, _, _ := node.style()
	for _, clip := range style.Clips {
		if len(clip.Text) != 0 {
			return fallback()
		}
	}
	geometry, err := p.renderer.Geometry()
	if err != nil {
		return pdfCompositeGeometry{}, err
	}
	switch geometry.(type) {
	case DefaultBackend, *DefaultBackend:
	default:
		return fallback()
	}
	var path GeometryPath
	evenOdd := false
	if node.path != nil {
		path, err = pdfGeometryPath(p.ctx, node.path.Path, p.matrix)
		evenOdd = node.path.Path.EvenOdd
	} else {
		path, err = geometry.Transform(geometryRectangle(Box{W: 1, H: 1}), MatrixFromValues([6]float64(p.matrix.Mul(node.image.Matrix))))
	}
	if err != nil {
		return pdfCompositeGeometry{}, err
	}
	if len(path) == 0 {
		return pdfCompositeGeometry{}, p.emptyPaintedPath()
	}
	path = geometryClose(path)
	clip, err := p.coverageClip(style.Clips, geometry)
	if err != nil || clip == nil || len(clip.path) == 0 {
		return pdfCompositeGeometry{}, err
	}
	compiler := semanticCompiler{geometry: geometry, page: &RasterPage{Width: p.pageWidth, Height: p.pageHeight, DPI: p.rasterDPI}}
	var bounds pdfPointBounds
	for _, segment := range path {
		if segment.Verb != GeometryClose {
			bounds.add(pdfgo.Point{X: segment.End.X, Y: segment.End.Y})
		}
		if segment.Verb == GeometryCubic {
			bounds.add(pdfgo.Point{X: segment.Control1.X, Y: segment.Control1.Y})
			bounds.add(pdfgo.Point{X: segment.Control2.X, Y: segment.Control2.Y})
		}
	}
	pathBox := bounds.box()
	cb := clip.box
	if pathBox.W <= 0 || pathBox.H <= 0 || cb.W <= 0 || cb.H <= 0 || pathBox.X >= cb.X+cb.W || pathBox.Y >= cb.Y+cb.H || pathBox.X+pathBox.W <= cb.X || pathBox.Y+pathBox.H <= cb.Y {
		return pdfCompositeGeometry{}, nil
	}
	if !clip.isRect && len(clip.path) > 64 {
		if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, nil, IdentityMatrix(), nil); err != nil {
			return pdfCompositeGeometry{}, err
		}
		scene, box, err := p.groupSceneBounds(compiler.page)
		if err != nil || scene == nil {
			return pdfCompositeGeometry{}, err
		}
		left, top := math.Max(box.X, cb.X), math.Max(box.Y, cb.Y)
		right, bottom := math.Min(box.X+box.W, cb.X+cb.W), math.Min(box.Y+box.H, cb.Y+cb.H)
		if right <= left || bottom <= top {
			return pdfCompositeGeometry{}, nil
		}
		step := 25.4 / p.rasterDPI
		x, y := math.Max(0, math.Floor(left/step)*step), math.Max(0, math.Floor(top/step)*step)
		xend, yend := math.Ceil(math.Min(p.pageWidth, right)/step)*step, math.Ceil(math.Min(p.pageHeight, bottom)/step)*step
		box = Box{X: x, Y: y, W: xend - x, H: yend - y}
		if box.W <= 0 || box.H <= 0 {
			return pdfCompositeGeometry{}, nil
		}
		return pdfCompositeGeometry{scene: scene, box: box, clip: clip}, nil
	}
	if clip.isRect {
		rect := clip.rect
		if pathBox.X >= rect.X && pathBox.Y >= rect.Y && pathBox.X+pathBox.W <= rect.X+rect.W && pathBox.Y+pathBox.H <= rect.Y+rect.H {
			if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, nil, IdentityMatrix(), nil); err != nil {
				return pdfCompositeGeometry{}, err
			}
			scene, box, err := p.groupSceneBounds(compiler.page)
			return pdfCompositeGeometry{scene: scene, box: box}, err
		}
		left, top := math.Max(rect.X, pathBox.X), math.Max(rect.Y, pathBox.Y)
		right, bottom := math.Min(rect.X+rect.W, pathBox.X+pathBox.W), math.Min(rect.Y+rect.H, pathBox.Y+pathBox.H)
		rectClip := geometryRectangle(Box{X: left, Y: top, W: right - left, H: bottom - top})
		if !evenOdd {
			clipped, linear, clipErr := clipLinearFillGeometry(path, rectClip)
			if clipErr != nil {
				return pdfCompositeGeometry{}, clipErr
			}
			if linear {
				if err := compiler.command(clipped, Paint{Color: color.RGBA{255, 255, 255, 255}}, false, nil, IdentityMatrix(), nil); err != nil {
					return pdfCompositeGeometry{}, err
				}
				scene, box, err := p.groupSceneBounds(compiler.page)
				return pdfCompositeGeometry{scene: scene, box: box}, err
			}
		}
		if deferredClip {
			if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, &rectClip, IdentityMatrix(), nil); err != nil {
				return pdfCompositeGeometry{}, err
			}
			scene, box, err := p.groupSceneBounds(compiler.page)
			return pdfCompositeGeometry{scene: scene, box: box}, err
		}
		if evenOdd {
			path, err = geometry.Normalize(path, true)
			if err != nil {
				return pdfCompositeGeometry{}, err
			}
			evenOdd = false
		}
		clipped, linear, clipErr := clipLinearFillGeometry(path, rectClip)
		if clipErr != nil {
			return pdfCompositeGeometry{}, clipErr
		}
		if linear {
			path = clipped
		} else {
			path, err = clipGeometry(geometry, path, &rectClip)
			if err != nil {
				return pdfCompositeGeometry{}, err
			}
		}
		if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, nil, IdentityMatrix(), nil); err != nil {
			return pdfCompositeGeometry{}, err
		}
		scene, box, err := p.groupSceneBounds(compiler.page)
		return pdfCompositeGeometry{scene: scene, box: box}, err
	}
	boundary := geometryRectangle(pathBox)
	clipPath, err := clipGeometry(geometry, clip.path, &boundary)
	if err != nil {
		return pdfCompositeGeometry{}, err
	}
	if deferredClip {
		if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, &clipPath, IdentityMatrix(), nil); err != nil {
			return pdfCompositeGeometry{}, err
		}
		scene, box, err := p.groupSceneBounds(compiler.page)
		return pdfCompositeGeometry{scene: scene, box: box}, err
	}
	box, err := geometry.Bounds(path)
	if err != nil {
		return pdfCompositeGeometry{}, err
	}
	rect, rectangle := geometryRectangleBounds(clipPath)
	if !rectangle || box.X < rect.X || box.Y < rect.Y || box.X+box.W > rect.X+rect.W || box.Y+box.H > rect.Y+rect.H {
		if evenOdd {
			path, err = geometry.Normalize(path, true)
			if err != nil {
				return pdfCompositeGeometry{}, err
			}
			evenOdd = false
		}
		var clipped GeometryPath
		var linear bool
		var clipErr error
		if rectangle {
			clipped, linear, clipErr = clipLinearFillGeometry(path, clipPath)
		}
		if clipErr != nil {
			return pdfCompositeGeometry{}, clipErr
		}
		if linear {
			path = clipped
		} else {
			path, err = clipGeometry(geometry, path, &clipPath)
			if err != nil {
				return pdfCompositeGeometry{}, err
			}
		}
	}
	if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, nil, IdentityMatrix(), nil); err != nil {
		return pdfCompositeGeometry{}, err
	}
	scene, box, err := p.groupSceneBounds(compiler.page)
	return pdfCompositeGeometry{scene: scene, box: box}, err
}

// pdfGeometryPathBounds 计算几何路径控制点外接框，用于快速跳过冗余矩形裁剪
// 入参: path 几何路径
// 返回: Box 外接框
func pdfGeometryPathBounds(path GeometryPath) Box {
	if rect, ok := geometryRectangleBounds(path); ok {
		return rect
	}
	var bounds pdfPointBounds
	for _, segment := range path {
		if segment.Verb != GeometryClose {
			bounds.add(pdfgo.Point{X: segment.End.X, Y: segment.End.Y})
		}
		if segment.Verb == GeometryCubic {
			bounds.add(pdfgo.Point{X: segment.Control1.X, Y: segment.Control1.Y})
			bounds.add(pdfgo.Point{X: segment.Control2.X, Y: segment.Control2.Y})
		}
	}
	return bounds.box()
}

// pdfClipAxisBox 提取无文字单矩形裁剪在页面坐标下的轴对齐包围框
// 入参: path PDF裁剪路径, matrix 页面变换
// 返回: Box 页面矩形, bool 是否为轴对齐矩形
func pdfClipAxisBox(path pdfgo.Path, matrix pdfgo.Matrix) (Box, bool) {
	if len(path.Text) != 0 || len(path.Segments) < 4 || len(path.Segments) > 6 {
		return Box{}, false
	}
	var points [4]pdfgo.Point
	for index := range 4 {
		segment := path.Segments[index]
		operator := "L"
		if index == 0 {
			operator = "M"
		}
		if segment.Operator != operator || len(segment.Points) != 1 {
			return Box{}, false
		}
		points[index] = matrix.Apply(segment.Points[0])
		if !finite(points[index].X) || !finite(points[index].Y) {
			return Box{}, false
		}
	}
	end := 4
	if end < len(path.Segments) && path.Segments[end].Operator == "L" {
		if len(path.Segments[end].Points) != 1 || matrix.Apply(path.Segments[end].Points[0]) != points[0] {
			return Box{}, false
		}
		end++
	}
	if end < len(path.Segments) {
		if path.Segments[end].Operator != "C" || len(path.Segments[end].Points) != 0 {
			return Box{}, false
		}
		end++
	}
	if end != len(path.Segments) {
		return Box{}, false
	}
	a, b, c, d := points[0], points[1], points[2], points[3]
	if !(a.Y == b.Y && b.X == c.X && c.Y == d.Y && d.X == a.X || a.X == b.X && b.Y == c.Y && c.X == d.X && d.Y == a.Y) {
		return Box{}, false
	}
	left, right := math.Min(a.X, c.X), math.Max(a.X, c.X)
	top, bottom := math.Min(a.Y, c.Y), math.Max(a.Y, c.Y)
	if left >= right || top >= bottom {
		return Box{}, false
	}
	return Box{X: left, Y: top, W: right - left, H: bottom - top}, true
}

// pdfClipPolygonEdge 按单条轴对齐半平面裁剪闭合多边形，以较小绝对值端点作插值基准防止极端坐标精度损失
// 入参: poly 多边形顶点, axis 裁剪方向(0左1右2上3下), edge 边界值
// 返回: []pdfgo.Point 裁剪后顶点
func pdfClipPolygonEdge(poly []pdfgo.Point, axis int, edge float64) []pdfgo.Point {
	if len(poly) == 0 {
		return nil
	}
	inside := func(pt pdfgo.Point) bool {
		switch axis {
		case 0:
			return pt.X >= edge
		case 1:
			return pt.X <= edge
		case 2:
			return pt.Y >= edge
		default:
			return pt.Y <= edge
		}
	}
	intersect := func(a, b pdfgo.Point) pdfgo.Point {
		if axis <= 1 {
			if a.X == b.X {
				return pdfgo.Point{X: edge, Y: a.Y}
			}
			if math.Abs(a.X) <= math.Abs(b.X) {
				t := (edge - a.X) / (b.X - a.X)
				return pdfgo.Point{X: edge, Y: a.Y + t*(b.Y-a.Y)}
			}
			t := (edge - b.X) / (a.X - b.X)
			return pdfgo.Point{X: edge, Y: b.Y + t*(a.Y-b.Y)}
		}
		if a.Y == b.Y {
			return pdfgo.Point{X: a.X, Y: edge}
		}
		if math.Abs(a.Y) <= math.Abs(b.Y) {
			t := (edge - a.Y) / (b.Y - a.Y)
			return pdfgo.Point{X: a.X + t*(b.X-a.X), Y: edge}
		}
		t := (edge - b.Y) / (a.Y - b.Y)
		return pdfgo.Point{X: b.X + t*(a.X-b.X), Y: edge}
	}
	out := make([]pdfgo.Point, 0, len(poly)+2)
	prev := poly[len(poly)-1]
	prevIn := inside(prev)
	for _, curr := range poly {
		currIn := inside(curr)
		if currIn {
			if !prevIn {
				out = append(out, intersect(prev, curr))
			}
			out = append(out, curr)
		} else if prevIn {
			out = append(out, intersect(prev, curr))
		}
		prev, prevIn = curr, currIn
	}
	return out
}

// pdfClipExtremeLinearPath 对坐标绝对值极端越界的纯直线闭合填充路径按生效矩形裁剪框预先裁剪
// 入参: path PDF路径, matrix 页面变换, clips 当前裁剪链, pageWidth 页面宽, pageHeight 页面高
// 返回: pdfgo.Path 裁剪后路径, bool 是否执行了裁剪, bool 裁剪后是否为空
func pdfClipExtremeLinearPath(path pdfgo.Path, matrix pdfgo.Matrix, clips []pdfgo.Path, pageWidth, pageHeight float64) (pdfgo.Path, bool, bool) {
	if pageWidth <= 0 || pageHeight <= 0 || len(path.Segments) < 3 {
		return path, false, false
	}
	const extremeLimit = 1e6
	extreme := false
	for _, seg := range path.Segments {
		switch seg.Operator {
		case "M", "L":
			if len(seg.Points) != 1 {
				return path, false, false
			}
			pt := matrix.Apply(seg.Points[0])
			if !finite(pt.X) || !finite(pt.Y) {
				return path, false, false
			}
			if math.Abs(pt.X) > extremeLimit || math.Abs(pt.Y) > extremeLimit {
				extreme = true
			}
		case "C":
			if len(seg.Points) != 0 {
				return path, false, false
			}
		default:
			return path, false, false
		}
	}
	if !extreme {
		return path, false, false
	}
	inverse, ok := matrix.Inverse()
	if !ok {
		return path, false, false
	}
	x0, y0, x1, y1 := 0.0, 0.0, pageWidth, pageHeight
	for _, clip := range clips {
		if rect, isRect := pdfClipAxisBox(clip, matrix); isRect {
			x0, y0 = math.Max(x0, rect.X), math.Max(y0, rect.Y)
			x1, y1 = math.Min(x1, rect.X+rect.W), math.Min(y1, rect.Y+rect.H)
		}
	}
	if x1 <= x0 || y1 <= y0 {
		return pdfgo.Path{EvenOdd: path.EvenOdd}, true, true
	}
	var result []pdfgo.Segment
	var poly []pdfgo.Point
	flush := func() {
		if len(poly) < 3 {
			poly = poly[:0]
			return
		}
		clipped := pdfClipPolygonEdge(poly, 0, x0)
		clipped = pdfClipPolygonEdge(clipped, 1, x1)
		clipped = pdfClipPolygonEdge(clipped, 2, y0)
		clipped = pdfClipPolygonEdge(clipped, 3, y1)
		if len(clipped) >= 3 {
			for i, pt := range clipped {
				op := "L"
				if i == 0 {
					op = "M"
				}
				result = append(result, pdfgo.Segment{Operator: op, Points: []pdfgo.Point{inverse.Apply(pt)}})
			}
			result = append(result, pdfgo.Segment{Operator: "C"})
		}
		poly = poly[:0]
	}
	for _, seg := range path.Segments {
		switch seg.Operator {
		case "M":
			flush()
			poly = append(poly, matrix.Apply(seg.Points[0]))
		case "L":
			poly = append(poly, matrix.Apply(seg.Points[0]))
		case "C":
			flush()
		}
	}
	flush()
	return pdfgo.Path{EvenOdd: path.EvenOdd, Segments: result}, true, len(result) == 0
}

// newPDFCompositeClip 封装规范化裁剪轮廓及其外接框与矩形判定
// 入参: key 裁剪缓存键, path 规范化裁剪路径
// 返回: *pdfCompositeClip 缓存裁剪对象
func newPDFCompositeClip(key [32]byte, path GeometryPath) *pdfCompositeClip {
	if len(path) == 0 {
		return &pdfCompositeClip{key: key}
	}
	if rect, ok := geometryRectangleBounds(path); ok {
		return &pdfCompositeClip{path: path, box: rect, rect: rect, isRect: true, key: key}
	}
	return &pdfCompositeClip{path: path, box: pdfGeometryPathBounds(path), key: key}
}

// coverageClip 按完整路径、变换和页面范围复用只读裁剪，前缀增量缓存并跳过冗余矩形裁剪
// 入参: paths 无文字裁剪的路径集合, geometry 几何后端
// 返回: *pdfCompositeClip 共享只读轮廓与外接框, error 几何或取消错误
func (p *pdfImporter) coverageClip(paths []pdfgo.Path, geometry GeometryBackend) (*pdfCompositeClip, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	pageBox := Box{W: p.pageWidth, H: p.pageHeight}
	clip := &pdfCompositeClip{path: geometryRectangle(pageBox), box: pageBox, rect: pageBox, isRect: true}
	if len(paths) == 0 {
		return clip, nil
	}
	effective := make([]pdfgo.Path, 0, len(paths))
	for _, path := range paths {
		if pageBox.W > 0 && pageBox.H > 0 && codecpdf.ClipContainsBoundary(path, p.matrix, basegeom.Box(pageBox)) {
			continue
		}
		effective = append(effective, path)
	}
	if len(effective) == 0 {
		return clip, nil
	}
	compositeCache := p.compositingCache()
	hash := sha256.New()
	var size [16]byte
	binary.LittleEndian.PutUint64(size[:8], math.Float64bits(p.pageWidth))
	binary.LittleEndian.PutUint64(size[8:], math.Float64bits(p.pageHeight))
	hash.Write(size[:])
	prefixKeys := make([][32]byte, len(effective))
	for i, path := range effective {
		var stepKey [32]byte
		cachedStep := false
		var refKey pdfClipPathRefKey
		if len(path.Segments) > 0 && len(path.Text) == 0 {
			refKey = pdfClipPathRefKey{segments: &path.Segments[0], length: len(path.Segments), evenOdd: path.EvenOdd, matrix: p.matrix}
			if compositeCache.clipPathKeys != nil {
				stepKey, cachedStep = compositeCache.clipPathKeys[refKey]
			}
		}
		if !cachedStep {
			var err error
			stepKey, err = codecpdf.ClipPathKey(p.ctx, path, p.matrix)
			if err != nil {
				return nil, err
			}
			if refKey.segments != nil {
				if compositeCache.clipPathKeys == nil {
					compositeCache.clipPathKeys = make(map[pdfClipPathRefKey][32]byte)
				}
				compositeCache.clipPathKeys[refKey] = stepKey
			}
		}
		hash.Write(stepKey[:])
		hash.Sum(prefixKeys[i][:0])
	}
	cache := compositeCache.clipCache()
	start := 0
	for i := len(effective) - 1; i >= 0; i-- {
		if cached, ok := cache.get(prefixKeys[i]); ok {
			clip = cached
			start = i + 1
			break
		}
	}
	if start == len(effective) {
		return clip, nil
	}
	for i := start; i < len(effective); i++ {
		if len(clip.path) == 0 {
			empty := &pdfCompositeClip{key: prefixKeys[i]}
			cache.put(prefixKeys[i], empty, 128)
			clip = empty
			continue
		}
		source := effective[i]
		curBox := clip.box
		if curBox.W > 0 && curBox.H > 0 && codecpdf.ClipContainsBoundary(source, p.matrix, basegeom.Box(curBox)) {
			if len(clip.path) <= (cache.limit-128)/112 {
				cache.put(prefixKeys[i], clip, 128+len(clip.path)*112)
			}
			continue
		}
		if clip.isRect {
			if srcRect, ok := pdfClipAxisBox(source, p.matrix); ok {
				left, top := math.Max(clip.rect.X, srcRect.X), math.Max(clip.rect.Y, srcRect.Y)
				right, bottom := math.Min(clip.rect.X+clip.rect.W, srcRect.X+srcRect.W), math.Min(clip.rect.Y+clip.rect.H, srcRect.Y+srcRect.H)
				if right <= left || bottom <= top {
					clip = &pdfCompositeClip{key: prefixKeys[i]}
				} else {
					inter := Box{X: left, Y: top, W: right - left, H: bottom - top}
					clip = &pdfCompositeClip{path: geometryRectangle(inter), box: inter, rect: inter, isRect: true, key: prefixKeys[i]}
				}
				cache.put(prefixKeys[i], clip, 128+len(clip.path)*112)
				continue
			}
		}
		part, err := pdfGeometryPath(p.ctx, source, p.matrix)
		if err != nil {
			return nil, err
		}
		part = geometryClose(part)
		if _, rectangle := geometryRectangleBounds(part); source.EvenOdd && !rectangle {
			part, err = geometry.Normalize(part, true)
			if err != nil {
				return nil, err
			}
		}
		nextPath, err := clipGeometry(geometry, part, &clip.path)
		if err != nil {
			return nil, err
		}
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		clip = newPDFCompositeClip(prefixKeys[i], nextPath)
		if len(clip.path) <= (cache.limit-128)/112 {
			cache.put(prefixKeys[i], clip, 128+len(clip.path)*112)
		}
	}
	return clip, nil
}

// path 保留每次路径绘制操作，重叠描边不合并为单次绘制
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) path(mark pdfgo.PathMark) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if pdfPaintedPathEmpty(mark.Path) {
		return p.emptyPaintedPath()
	}
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "path blend mode " + string(mark.Style.BlendMode)}
	}
	return p.appendPath(mark)
}

// appendPath 添加独立可编辑路径
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) appendPath(mark pdfgo.PathMark) error {
	if pdfPaintedPathEmpty(mark.Path) {
		return p.emptyPaintedPath()
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Path(mark) })
		}
		return err
	}
	if mark.Fill && mark.Style.FillOverprint && codecpdf.OverprintNeedsSeparation(mark.Style.Fill) || mark.Stroke && mark.Style.StrokeOverprint && codecpdf.OverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "color separation overprint"}
	}
	if p.warning != nil && (mark.Fill && p.gradientError(mark.Style.Fill) != nil || mark.Stroke && p.gradientError(mark.Style.Stroke) != nil) {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		return p.gradientPath(mark)
	}
	if mark.Fill && !mark.Stroke && mark.StrokeMatrix == (pdfgo.Matrix{}) {
		clippedPath, clipped, empty := pdfClipExtremeLinearPath(mark.Path, p.matrix, mark.Style.Clips, p.pageWidth, p.pageHeight)
		if empty {
			return nil
		}
		if clipped {
			mark.Path = clippedPath
		}
	}
	rawBox, err := codecpdf.PathBounds(p.ctx, mark.Path, p.matrix)
	if err != nil {
		return err
	}
	box := Box(rawBox)
	scale := math.Hypot(p.matrix[0], p.matrix[1])
	drawing, origin, ctm := *p, box, ""
	marginScale := scale
	if mark.StrokeMatrix != (pdfgo.Matrix{}) {
		inverse, ok := mark.StrokeMatrix.Inverse()
		if !ok {
			return fmt.Errorf("invalid PDF stroke matrix")
		}
		drawing.matrix, origin, scale = inverse, Box{}, 1
		m := p.matrix.Mul(mark.StrokeMatrix)
		marginScale = math.Max(math.Hypot(m[0], m[2]), math.Hypot(m[1], m[3]))
	}
	if mark.Stroke {
		margin := mark.Style.LineWidth * marginScale / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{X: box.X - margin, Y: box.Y - margin, W: box.W + 2*margin, H: box.H + 2*margin}
	}
	if mark.StrokeMatrix == (pdfgo.Matrix{}) {
		origin = box
	} else {
		m := p.matrix.Mul(mark.StrokeMatrix)
		m[4], m[5] = m[4]-box.X, m[5]-box.Y
		ctm = pdfNumbers(m[:]...)
	}
	var objectAlpha *int
	fillSource, strokeSource := mark.Style.Fill, mark.Style.Stroke
	if mark.Fill && mark.Stroke && fillSource.Alpha == strokeSource.Alpha && fillSource.Alpha < 1 {
		alpha := int(math.Round(fillSource.Alpha * 255))
		objectAlpha = &alpha
		fillSource.Alpha, strokeSource.Alpha = 1, 1
	}
	paint := func(value pdfgo.Paint) (*FillColor, error) {
		if value.Tiling == nil && value.Axial == nil && value.Radial == nil && value.Mesh == nil && value.Function == nil && value.Shading == nil {
			return p.color(value, mark.Style.RenderingIntent)
		}
		if value.Tiling != nil || pdfGradientPattern(value) {
			return p.paintColor(value, box, mark.Style.RenderingIntent)
		}
		return drawing.paintColor(value, origin, mark.Style.RenderingIntent)
	}
	fillColor, err := p.color(fillSource, mark.Style.RenderingIntent)
	if err != nil {
		return err
	}
	strokePaint, err := p.color(strokeSource, mark.Style.RenderingIntent)
	if err != nil {
		return err
	}
	if mark.Fill {
		fillColor, err = paint(fillSource)
		if err != nil {
			return err
		}
	}
	if mark.Stroke {
		strokePaint, err = paint(strokeSource)
		if err != nil {
			return err
		}
	}
	strokeColor := StrokeColor(*strokePaint)
	clips, err := p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	data, err := codecpdf.PathData(p.ctx, mark.Path, drawing.matrix, basegeom.Box(origin))
	if err != nil {
		return err
	}
	object := PathObject{Boundary: pdfBoundary(box), CTM: ctm, AbbreviatedData: data, Fill: &mark.Fill, Stroke: &mark.Stroke, FillColor: fillColor, StrokeColor: &strokeColor, Alpha: objectAlpha, LineWidth: mark.Style.LineWidth * scale, Cap: pdfLineCapName(mark.Style.Cap), Join: pdfLineJoinName(mark.Style.Join), MiterLimit: mark.Style.MiterLimit, Clips: clips}
	object.LineWidthSet = object.LineWidth == 0
	if mark.Path.EvenOdd {
		object.Rule = "Even-Odd"
	}
	if len(mark.Style.Dash) > 0 {
		object.DashPattern = pdfDashPattern(mark.Style.Dash, scale)
		phase := mark.Style.DashPhase * scale
		object.DashOffset = &phase
	}
	p.appendSequenceObject(GraphicObject{Type: "PathObject", PathObject: object}, mark.Sequence)
	p.report.PathObjects++
	return nil
}

// emptyPaintedPath 按诊断策略忽略不绘制内容的空路径，严格模式仍拒绝
// 返回: error 严格模式或取消错误
func (p *pdfImporter) emptyPaintedPath() error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if p.warning == nil {
		return fmt.Errorf("empty PDF painted path")
	}
	p.warning(pdfgo.Diagnostic{Message: "empty PDF painted path ignored"})
	return p.ctx.Err()
}

// clipPath 复用页面内重复路径编码，按估算字节预算淘汰且不缓存错误
// 入参: path 裁剪路径
// 返回: pdfClipPath 变换后的边界及编码, error 取消错误
func (p *pdfImporter) clipPath(path pdfgo.Path) (pdfClipPath, error) {
	key, err := codecpdf.ClipPathKey(p.ctx, path, p.matrix)
	if err != nil {
		return pdfClipPath{}, err
	}
	if p.clipPaths == nil {
		p.clipPaths = resourceCache[[32]byte, pdfClipPath](p.resources, 8<<20)
	}
	if cached, ok := p.clipPaths.get(key); ok {
		return cached, nil
	}
	rawBox, err := codecpdf.PathBounds(p.ctx, path, p.matrix)
	if err != nil {
		return pdfClipPath{}, err
	}
	box := Box(rawBox)
	data, err := codecpdf.PathData(p.ctx, path, p.matrix, rawBox)
	if err != nil {
		return pdfClipPath{}, err
	}
	rule := "NonZero"
	if path.EvenOdd {
		rule = "Even-Odd"
	}
	boundary := pdfBoundary(box)
	fill, stroke := true, false
	result := pdfClipPath{box: box, data: data, path: &[1]PathObject{{Boundary: boundary, AbbreviatedData: data, Rule: rule, Fill: &fill, Stroke: &stroke}}}
	p.clipPaths.put(key, result, len(data)+len(boundary)+512)
	return result, nil
}

// sharedClipObjects 将连续重复的大裁剪区提取为复合对象，同步缓冲区及来源区段
// 入参: objects 本库生成且尚未分配标识的对象
// 返回: []GraphicObject 提取后的对象, error 资源创建或取消错误
func (p *pdfImporter) sharedClipObjects(objects []GraphicObject) ([]GraphicObject, error) {
	var output []GraphicObject
	var mapped pdfObjectBuffer
	for start := 0; start < len(objects); {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		clips := pdfPageClip(objects[start])
		end := start + 1
		if clips != nil {
			for end < len(objects) && p.objects.origin(start).equal(p.objects.origin(end)) && pdfMatchesPageClip(objects[end], clips) {
				if err := p.ctx.Err(); err != nil {
					return nil, err
				}
				end++
			}
		}
		if end-start < 2 {
			if output != nil {
				mapped.recordOrigin(len(output), p.objects.origin(start))
				output = append(output, objects[start])
			}
			start = end
			continue
		}
		if output == nil {
			output = append(make([]GraphicObject, 0, len(objects)), objects[:start]...)
			if p.objects != nil {
				for _, run := range p.objects.sequences {
					if run.start >= start {
						break
					}
					mapped.sequences = append(mapped.sequences, run)
				}
			}
		}
		members := objects[start:end:end]
		for index := range members {
			*editorObjectClips(&members[index]) = nil
		}
		var saved []GraphicObject
		if p.recordForms {
			saved = cloneEditorData(members)
		}
		id, err := p.objectValidation().addOwnedVector(p.ctx, members, p.pageWidth, p.pageHeight)
		if err != nil {
			return nil, err
		}
		if p.recordForms {
			if p.formRecords == nil {
				p.formRecords = make(map[string]pdfFormRecord)
			}
			p.formRecords[id] = pdfFormRecord{objects: saved, width: p.pageWidth, height: p.pageHeight}
		}
		mapped.recordOrigin(len(output), p.objects.origin(start))
		output = append(output, GraphicObject{Type: "CompositeObject", CompositeObject: CompositeObject{
			Boundary: pdfBoundary(Box{W: p.pageWidth, H: p.pageHeight}), ResourceID: id, Clips: clips,
		}})
		start = end
	}
	if output == nil {
		return objects, nil
	}
	if p.objects != nil {
		mapped.parts = [][]GraphicObject{output}
		mapped.size = len(output)
		*p.objects = mapped
	}
	return output, nil
}

// clips 省略覆盖对象边界的矩形裁剪，复用其余几何并保留位移及路径交集
// 入参: paths 裁剪路径, origin 对象边界
// 返回: *Clips OFD裁剪区域，无裁剪时为空, error 未解析的裁剪字形
func (p *pdfImporter) clips(paths []pdfgo.Path, origin Box) (*Clips, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var clips *Clips
	var areas []ClipArea
	translation := ""
	for _, path := range paths {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		if codecpdf.ClipContainsBoundary(path, p.matrix, basegeom.Box(origin)) {
			continue
		}
		if clips == nil {
			clips = &Clips{Clip: make([]Clip, 0, len(paths))}
			areas = make([]ClipArea, len(paths))
			if origin.X != 0 || origin.Y != 0 {
				translation = pdfNumbers(1, 0, 0, 1, -origin.X, -origin.Y)
			}
		}
		index := len(clips.Clip)
		area := &areas[index]
		area.CTM = translation
		if len(path.Segments) != 0 {
			encoded, err := p.clipPath(path)
			if err != nil {
				return nil, err
			}
			area.Path = encoded.path[:1:1]
		}
		for _, mark := range path.Text {
			entry, ok := p.clipTexts[mark]
			if !ok {
				count := p.objects.len()
				textCount := p.report.TextObjects
				if err := p.text(pdfgo.TextMark{Font: mark.Font, Glyphs: mark.Glyphs, Positions: mark.Positions, Matrix: mark.Matrix, Size: mark.Size, HorizontalScale: mark.HorizontalScale, Mode: 7, Clip: mark, Style: pdfgo.Style{Fill: pdfgo.Paint{Alpha: 1}}}); err != nil {
					return nil, err
				}
				p.objects.truncate(count)
				p.report.TextObjects = textCount
				entry = p.clipTexts[mark]
			}
			if len(entry.paths) != 0 {
				area.Path = append(area.Path, entry.paths...)
			}
			if len(entry.texts) != 0 {
				if len(path.Text) == 1 && len(area.Text) == 0 {
					area.Text = entry.texts[:len(entry.texts):len(entry.texts)]
				} else {
					area.Text = append(area.Text, entry.texts...)
				}
			}
		}
		if len(area.Path)+len(area.Text) == 0 {
			fill, stroke := true, false
			area.Path = []PathObject{{Boundary: "0 0 1 1", AbbreviatedData: "M 0 0 C", Fill: &fill, Stroke: &stroke}}
		}
		clips.Clip = append(clips.Clip, Clip{Area: areas[index : index+1 : index+1]})
	}
	return clips, nil
}

// pdfLineCapName 将PDF线帽编号安全映射为OFD线帽名称
// 入参: cap PDF线帽编号
// 返回: string OFD线帽名称
func pdfLineCapName(cap int) string {
	switch cap {
	case 1:
		return "Round"
	case 2:
		return "Square"
	default:
		return "Butt"
	}
}

// pdfLineJoinName 将PDF线连接编号安全映射为OFD连接名称
// 入参: join PDF线连接编号
// 返回: string OFD连接名称
func pdfLineJoinName(join int) string {
	switch join {
	case 1:
		return "Round"
	case 2:
		return "Bevel"
	default:
		return "Miter"
	}
}
