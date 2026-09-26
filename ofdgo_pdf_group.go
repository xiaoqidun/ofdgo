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
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// errPDFGroupRaster 表示透明组需要按指定精度局部合成
var errPDFGroupRaster = errors.New("PDF transparency group requires local compositing")

// pdfPaintRegion 保存透明组内的单色填充区域
type pdfPaintRegion struct {
	paint pdfgo.Paint
	path  GeometryPath
}

// group 将单色图元的透明组转换为互不重叠的填充区域，保留矢量与组不透明度
// 入参: mark 组边界状态, walk 组内容访问函数, visitor 页面访问器
// 返回: error 不支持的组内容或转换错误
func (p *pdfImporter) group(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error, visitor pdfgo.Visitor) error {
	if mark.BlendMode != "" && mark.BlendMode != "Normal" && mark.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "group blend mode " + string(mark.BlendMode)}
	}
	if err := p.flushPath(); err != nil {
		return err
	}
	if mark.ColorSpace != nil && !mark.ColorSpace.SRGBEquivalent() {
		if mark.Alpha != 1 || mark.SoftMask != nil {
			return &pdfgo.UnsupportedError{Feature: "non-sRGB group color space"}
		}
		if err := walk(pdfOpaqueVisitor(visitor)); err != nil {
			return err
		}
	}
	if mark.SoftMask != nil {
		clip, err := p.maskClip(mark.SoftMask)
		if err != nil {
			if p.rasterMaskAllowed(err) {
				return p.rasterGroup(mark, walk)
			}
			return err
		}
		original := walk
		walk = func(v pdfgo.Visitor) error {
			return original(pdfClippedVisitor(v, clip))
		}
	}
	if mark.Alpha == 1 {
		if err := walk(visitor); err != nil {
			return err
		}
		return p.flushPath()
	}
	err := p.groupPaths(mark, walk, visitor)
	if errors.Is(err, errPDFGroupRaster) && p.warning != nil {
		return p.rasterGroup(mark, walk)
	}
	return err
}

// pdfOpaqueVisitor 验证组内没有透明混合，允许独立的已校准颜色保留为矢量
// 入参: visitor 正式访问器，语义提示由正式遍历交付
// 返回: pdfgo.Visitor 不透明性检查器
func pdfOpaqueVisitor(visitor pdfgo.Visitor) pdfgo.Visitor {
	check := func(s pdfgo.Style, fill, stroke bool) error {
		if s.SoftMask != nil || fill && s.Fill.Alpha != 1 || stroke && s.Stroke.Alpha != 1 || s.BlendMode != "" && s.BlendMode != "Normal" && s.BlendMode != "Compatible" {
			return &pdfgo.UnsupportedError{Feature: "non-sRGB transparent group content"}
		}
		return nil
	}
	v := pdfgo.Visitor{
		Path: func(m pdfgo.PathMark) error { return check(m.Style, m.Fill, m.Stroke) },
		Text: func(m pdfgo.TextMark) error {
			return check(m.Style, m.Mode%4 == 0 || m.Mode%4 == 2, m.Mode%4 == 1 || m.Mode%4 == 2)
		},
		Image: func(m pdfgo.ImageMark) error {
			if m.Image.ImageMask || m.Image.Mask != nil || m.Image.SoftMask != nil || m.Image.Stream.Dictionary["SMaskInData"] != nil {
				return &pdfgo.UnsupportedError{Feature: "non-sRGB masked group image"}
			}
			return check(m.Style, true, false)
		},
	}
	if visitor.Warning != nil {
		v.Warning = func(pdfgo.Diagnostic) {}
	}
	if visitor.MarkedContent != nil {
		v.MarkedContent = func(pdfgo.MarkedContentMark) error { return nil }
	}
	v.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
		if mark.Alpha != 1 || mark.SoftMask != nil || mark.BlendMode != "" && mark.BlendMode != "Normal" && mark.BlendMode != "Compatible" {
			return &pdfgo.UnsupportedError{Feature: "non-sRGB nested transparent group"}
		}
		return walk(v)
	}
	return v
}

// groupPaths 将正常混合的透明路径组分解为标准矢量区域
// 入参: mark 组状态, walk 内容访问函数, visitor 页面访问器
// 返回: error 无法矢量表达或几何错误
func (p *pdfImporter) groupPaths(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error, visitor pdfgo.Visitor) error {
	geometry, err := p.editor.Geometry()
	if err != nil {
		return err
	}
	var regions []pdfPaintRegion
	var combined GeometryPath
	var paint *pdfgo.Paint
	var clips []pdfgo.Path
	opaque := true
	scale := math.Hypot(p.matrix[0], p.matrix[1])
	collect := pdfgo.Visitor{
		Warning: visitor.Warning,
		Text:    func(pdfgo.TextMark) error { return errPDFGroupRaster },
		Image:   func(pdfgo.ImageMark) error { return errPDFGroupRaster },
		Group:   func(pdfgo.GroupMark, func(pdfgo.Visitor) error) error { return errPDFGroupRaster },
	}
	collect.Path = func(path pdfgo.PathMark) error {
		if path.Style.SoftMask != nil || path.Style.BlendMode != "" && path.Style.BlendMode != "Normal" && path.Style.BlendMode != "Compatible" {
			return &pdfgo.UnsupportedError{Feature: "masked or blended transparent group content"}
		}
		shape, err := ParseGeometryPath(p.pathData(path.Path, Box{}))
		if err != nil {
			return err
		}
		for _, stroke := range []bool{false, true} {
			if stroke && !path.Stroke || !stroke && !path.Fill {
				continue
			}
			color, overprint := path.Style.Fill, path.Style.FillOverprint
			if stroke {
				color, overprint = path.Style.Stroke, path.Style.StrokeOverprint
			}
			if color.Tiling != nil || color.Axial != nil || color.Radial != nil || overprint && pdfOverprintNeedsSeparation(color) {
				return fmt.Errorf("%w: patterned or overprinted group content", errPDFGroupRaster)
			}
			if color.Alpha == 0 {
				continue
			}
			opaque = opaque && color.Alpha == 1
			if paint == nil {
				paint, clips = &color, path.Style.Clips
			} else if !reflect.DeepEqual(clips, path.Style.Clips) {
				return &pdfgo.UnsupportedError{Feature: "independently clipped transparent group"}
			} else if color.Alpha != 1 || !reflect.DeepEqual(*paint, color) {
				regions = append(regions, pdfPaintRegion{*paint, combined})
				combined = nil
				paint = &color
			}
			outline := shape
			evenOdd := path.Path.EvenOdd
			if stroke {
				if path.Style.LineWidth == 0 {
					return fmt.Errorf("%w: zero-width group stroke", errPDFGroupRaster)
				}
				strokeShape, strokeScale, tolerance := shape, scale, 0.001
				var strokeTransform pdfgo.Matrix
				if path.StrokeMatrix != (pdfgo.Matrix{}) {
					inverse, ok := path.StrokeMatrix.Inverse()
					if !ok {
						return fmt.Errorf("invalid PDF stroke matrix")
					}
					local := pdfImporter{matrix: inverse}
					strokeShape, err = ParseGeometryPath(local.pathData(path.Path, Box{}))
					if err != nil {
						return err
					}
					strokeScale = 1
					strokeTransform = p.matrix.Mul(path.StrokeMatrix)
					tolerance /= math.Max(math.Hypot(strokeTransform[0], strokeTransform[2]), math.Hypot(strokeTransform[1], strokeTransform[3]))
				}
				dashes := make([]float64, len(path.Style.Dash))
				for i, dash := range path.Style.Dash {
					dashes[i] = dash * strokeScale
				}
				outline, err = geometry.Stroke(strokeShape, StrokeOptions{Width: path.Style.LineWidth * strokeScale, Cap: []string{"Butt", "Round", "Square"}[path.Style.Cap], Join: []string{"Miter", "Round", "Bevel"}[path.Style.Join], MiterLimit: path.Style.MiterLimit, DashOffset: path.Style.DashPhase * strokeScale, Dashes: dashes, Tolerance: tolerance})
				if err != nil {
					return err
				}
				if strokeTransform != (pdfgo.Matrix{}) {
					outline, err = geometry.Transform(outline, NewMatrix(pdfNumbers(strokeTransform[:]...)))
					if err != nil {
						return err
					}
				}
				evenOdd = false
			}
			outline, err = geometry.Normalize(outline, evenOdd)
			if err != nil {
				return err
			}
			combined = append(combined, outline...)
		}
		return nil
	}
	if err := walk(collect); err != nil {
		return err
	}
	if len(combined) == 0 {
		return nil
	}
	regions = append(regions, pdfPaintRegion{*paint, combined})
	if !opaque {
		regions, err = pdfCompositeRegions(geometry, regions)
		if err != nil {
			return err
		}
	}
	var covered GeometryPath
	for i := len(regions) - 1; i >= 0; i-- {
		region := regions[i]
		visible := region.path
		if opaque && len(covered) > 0 {
			visible, err = geometry.Combine(visible, covered, GeometrySubtract)
			if err != nil {
				return err
			}
		}
		if opaque && i > 0 {
			covered, err = geometry.Combine(covered, region.path, GeometryUnion)
			if err != nil {
				return err
			}
		}
		if len(visible) == 0 {
			continue
		}
		box, err := geometry.Bounds(visible)
		if err != nil {
			return err
		}
		if box.W == 0 || box.H == 0 {
			continue
		}
		visible, err = geometry.Transform(visible, TranslationMatrix(-box.X, -box.Y))
		if err != nil {
			return err
		}
		data, err := visible.OFD()
		if err != nil {
			return err
		}
		region.paint.Alpha *= mark.Alpha
		fill, stroke := true, false
		objectClips, err := p.clips(clips, box)
		if err != nil {
			return err
		}
		object := PathObject{Boundary: pdfBoundary(box), AbbreviatedData: data, Fill: &fill, Stroke: &stroke, FillColor: p.color(region.paint), Clips: objectClips}
		p.objects = append(p.objects, GraphicObject{Type: "PathObject", PathObject: object})
		p.report.PathObjects++
	}
	return nil
}

// rasterGroup 在透明背景上合成局部图元，组透明度应用于合成结果，保留组外对象
// 入参: mark 组状态, walk 内容访问函数
// 返回: error 转换或渲染错误
func (p *pdfImporter) rasterGroup(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
	img, box, err := p.renderGroup(func(local *pdfImporter) error { return walk(local.visitor()) }, nil)
	if err != nil || img == nil {
		return err
	}
	if mark.SoftMask != nil {
		img, err = p.applySoftMask(img, box, mark.SoftMask)
		if err != nil {
			return err
		}
	}
	return p.appendRasterGroup(img, box, mark.Alpha)
}

// visitor 绑定当前导入器的图元与透明组访问函数
// 返回: pdfgo.Visitor 页面访问器
func (p *pdfImporter) visitor() pdfgo.Visitor {
	v := pdfgo.Visitor{Path: p.path, Text: p.text, Image: p.image, Warning: p.warning}
	v.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error { return p.group(mark, walk, v) }
	return v
}

// renderGroup 按统一像素网格渲染局部内容，保留裁剪及后端选择
// 入参: build 局部图元构建函数, viewport 指定裁取区域，nil时按图元边界计算
// 返回: image.Image 透明图像, Box 页面区域, error 构建或渲染错误
func (p *pdfImporter) renderGroup(build func(*pdfImporter) error, viewport *Box) (image.Image, Box, error) {
	scene, box, err := p.compileGroup(build)
	if err != nil || scene == nil && viewport == nil {
		return nil, box, err
	}
	if viewport != nil {
		box = *viewport
	}
	if scene == nil {
		scene = &RasterPage{DPI: p.rasterDPI}
	}
	img, err := p.renderGroupScene(scene, box)
	return img, box, err
}

// compileGroup 编译局部图元并计算页面内的像素对齐边界
// 入参: build 局部图元构建函数
// 返回: *RasterPage 中立绘制场景, Box 页面区域, error 编译错误
func (p *pdfImporter) compileGroup(build func(*pdfImporter) error) (*RasterPage, Box, error) {
	var box Box
	if err := p.ctx.Err(); err != nil {
		return nil, box, err
	}
	editor := NewEditor()
	editor.SetRenderBackends(p.editor.Backends())
	editor.SetFontDirs(p.editor.fontDirs...)
	editor.SetFontFS(p.editor.fontFS...)
	if _, err := editor.AddPage(p.pageWidth, p.pageHeight); err != nil {
		return nil, box, err
	}
	local := pdfImporter{ctx: p.ctx, reader: p.reader, editor: editor, renderer: p.renderer, matrix: p.matrix, pageBox: p.pageBox, pageWidth: p.pageWidth, pageHeight: p.pageHeight, rasterDPI: p.rasterDPI, warning: p.warning, rasterWarned: true, fontIDs: map[*pdfgo.Font]string{}, fontMetrics: map[string]FontMetrics{}}
	if err := build(&local); err != nil {
		return nil, box, err
	}
	if err := local.flushPath(); err != nil {
		return nil, box, err
	}
	if len(local.objects) == 0 {
		return nil, box, nil
	}
	if err := local.commitObjects(); err != nil {
		return nil, box, err
	}
	reader, err := editor.Reader()
	if err != nil {
		return nil, box, err
	}
	defer reader.Close()
	page, err := reader.PageContentByIndex(0)
	if err != nil {
		return nil, box, err
	}
	renderer := p.renderer.childRenderer(reader)
	renderer.TransparentBackground = true
	for _, layer := range page.Content.Layer {
		for _, object := range layer.Objects {
			bounds, err := renderer.ObjectBounds(object, layer.DrawParam)
			if err != nil {
				return nil, box, err
			}
			box = unionTextBox(box, bounds)
		}
	}
	if box.W <= 0 || box.H <= 0 {
		return nil, box, nil
	}
	step := 25.4 / p.rasterDPI
	x, y := math.Max(0, math.Floor(box.X/step)*step), math.Max(0, math.Floor(box.Y/step)*step)
	xend, yend := math.Ceil(math.Min(p.pageWidth, box.X+box.W)/step)*step, math.Ceil(math.Min(p.pageHeight, box.Y+box.H)/step)*step
	box = Box{X: x, Y: y, W: xend - x, H: yend - y}
	if box.W <= 0 || box.H <= 0 {
		return nil, box, nil
	}
	scene, err := renderer.CompilePage(page)
	return scene, box, err
}

// renderGroupScene 在指定区域渲染已编译场景，不修改缓存的命令与裁剪
// 入参: source 原始场景, box 页面裁取区域
// 返回: image.Image 透明图像, error 后端渲染错误
func (p *pdfImporter) renderGroupScene(source *RasterPage, box Box) (image.Image, error) {
	scene := *source
	scene.Commands = append([]RasterCommand(nil), source.Commands...)
	scene.Width, scene.Height = box.W, box.H
	for i := range scene.Commands {
		command := &scene.Commands[i]
		command.Transform[4] -= box.X
		command.Transform[5] -= box.Y
		if command.Clip != nil {
			command.Clip = append([]RasterSegment{}, command.Clip...)
			for j := range command.Clip {
				segment := &command.Clip[j]
				segment.End.X, segment.End.Y = segment.End.X-box.X, segment.End.Y-box.Y
				segment.Control1.X, segment.Control1.Y = segment.Control1.X-box.X, segment.Control1.Y-box.Y
				segment.Control2.X, segment.Control2.Y = segment.Control2.X-box.X, segment.Control2.Y-box.Y
			}
		}
	}
	backend := p.editor.Backends().Raster
	if backend == nil {
		return nil, fmt.Errorf("PDF local compositing: %w", ErrBackendUnavailable)
	}
	return backend.Render(&scene)
}

// appendRasterGroup 保存局部合成结果并记录输出精度
// 入参: img 透明图像, box 页面区域, opacity 组透明度
// 返回: error 保存错误
func (p *pdfImporter) appendRasterGroup(img image.Image, box Box, opacity float64) error {
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		return err
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	id, err := p.editor.AddImage(data.Bytes())
	if err != nil {
		return err
	}
	alpha := int(math.Round(opacity * 255))
	object := ImageObject{ResourceID: id, Boundary: pdfBoundary(box), CTM: pdfNumbers(box.W, 0, 0, box.H, 0, 0), Alpha: &alpha}
	p.objects = append(p.objects, GraphicObject{Type: "ImageObject", ImageObject: object})
	p.report.ImageObjects++
	if !p.rasterWarned {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF effect locally composited at %g DPI; other objects retained", p.rasterDPI)})
		p.rasterWarned = true
	}
	return nil
}

// pdfCompositeRegions 按正常混合计算互不重叠的区域，组透明度由调用方统一应用
// 入参: geometry 几何后端, regions 按绘制顺序排列的区域
// 返回: []pdfPaintRegion 合成区域, error 几何错误
func pdfCompositeRegions(geometry GeometryBackend, regions []pdfPaintRegion) ([]pdfPaintRegion, error) {
	var result []pdfPaintRegion
	for _, source := range regions {
		remaining := source.path
		var next []pdfPaintRegion
		for _, backdrop := range result {
			outside, err := geometry.Combine(backdrop.path, source.path, GeometrySubtract)
			if err != nil {
				return nil, err
			}
			if len(outside) != 0 {
				next = append(next, pdfPaintRegion{backdrop.paint, outside})
			}
			overlap, err := geometry.Combine(backdrop.path, source.path, GeometryIntersect)
			if err != nil {
				return nil, err
			}
			if len(overlap) != 0 {
				paint := pdfgo.Paint{Alpha: source.paint.Alpha + backdrop.paint.Alpha*(1-source.paint.Alpha)}
				for i := range paint.RGB {
					paint.RGB[i] = (source.paint.RGB[i]*source.paint.Alpha + backdrop.paint.RGB[i]*backdrop.paint.Alpha*(1-source.paint.Alpha)) / paint.Alpha
				}
				next = append(next, pdfPaintRegion{paint, overlap})
			}
			remaining, err = geometry.Combine(remaining, backdrop.path, GeometrySubtract)
			if err != nil {
				return nil, err
			}
		}
		if len(remaining) != 0 {
			next = append(next, pdfPaintRegion{source.paint, remaining})
		}
		result = next
	}
	return result, nil
}

// pdfClippedVisitor 为图元及嵌套组附加裁剪，保留未提供的访问能力
// 入参: visitor 图元访问器, clip 页面坐标裁剪
// 返回: pdfgo.Visitor 裁剪访问器
func pdfClippedVisitor(visitor pdfgo.Visitor, clip pdfgo.Path) pdfgo.Visitor {
	result := visitor
	if visitor.Path != nil {
		result.Path = func(mark pdfgo.PathMark) error {
			mark.Style.Clips = append(append([]pdfgo.Path(nil), mark.Style.Clips...), clip)
			return visitor.Path(mark)
		}
	}
	if visitor.Text != nil {
		result.Text = func(mark pdfgo.TextMark) error {
			mark.Style.Clips = append(append([]pdfgo.Path(nil), mark.Style.Clips...), clip)
			return visitor.Text(mark)
		}
	}
	if visitor.Image != nil {
		result.Image = func(mark pdfgo.ImageMark) error {
			mark.Style.Clips = append(append([]pdfgo.Path(nil), mark.Style.Clips...), clip)
			return visitor.Image(mark)
		}
	}
	if visitor.Group != nil {
		result.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
			return visitor.Group(mark, func(v pdfgo.Visitor) error { return walk(pdfClippedVisitor(v, clip)) })
		}
	}
	return result
}

// maskClip 将二值亮度蒙版转换为标准矢量裁剪，不栅格化原图元
// 入参: mask PDF蒙版
// 返回: pdfgo.Path 页面坐标裁剪路径, error 不可精确转换或几何错误
func (p *pdfImporter) maskClip(mask *pdfgo.SoftMask) (pdfgo.Path, error) {
	if mask.Subtype != "Luminosity" || mask.ColorSpace == nil || mask.ColorSpace.Model != "DeviceGray" || mask.ColorSpace.Calibrated() {
		return pdfgo.Path{}, &pdfgo.UnsupportedError{Feature: "nonbinary luminosity mask"}
	}
	if clip, ok := p.maskClips[mask]; ok {
		return clip, nil
	}
	geometry, err := p.editor.Geometry()
	if err != nil {
		return pdfgo.Path{}, err
	}
	opacity := func(v float64) (bool, error) {
		v = math.Max(0, math.Min(1, mask.Transfer[0]+v*(mask.Transfer[1]-mask.Transfer[0])))
		if v != 0 && v != 1 {
			return false, &pdfgo.UnsupportedError{Feature: "nonbinary luminosity mask"}
		}
		return v == 1, nil
	}
	luminosity, err := mask.ColorSpace.Luminosity(mask.Backdrop, "RelativeColorimetric")
	if err != nil {
		return pdfgo.Path{}, err
	}
	background, err := opacity(luminosity)
	if err != nil {
		return pdfgo.Path{}, err
	}
	box := p.pageBox
	var region GeometryPath
	if background {
		region, err = ParseGeometryPath(pdfNumbersPathBox(box))
		if err != nil {
			return pdfgo.Path{}, err
		}
		region, err = geometry.Normalize(region, false)
		if err != nil {
			return pdfgo.Path{}, err
		}
	}
	identity := pdfImporter{matrix: pdfgo.Identity()}
	var visitor pdfgo.Visitor
	visitor.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
		if mark.Alpha != 1 || mark.SoftMask != nil || !pdfNormalBlend(mark.BlendMode) || mark.ColorSpace != nil && !mark.ColorSpace.Equal(mask.ColorSpace) {
			return &pdfgo.UnsupportedError{Feature: "transparent mask group"}
		}
		return walk(visitor)
	}
	visitor.Path = func(mark pdfgo.PathMark) error {
		paint := mark.Style.Fill
		if !mark.Fill || mark.Stroke || paint.Alpha != 1 || paint.CMYK != nil || paint.Axial != nil || paint.Radial != nil || paint.Tiling != nil || paint.Space != nil && paint.Space.Calibrated() || paint.RGB[0] != paint.RGB[1] || paint.RGB[1] != paint.RGB[2] || mark.Style.SoftMask != nil || !pdfNormalBlend(mark.Style.BlendMode) {
			return &pdfgo.UnsupportedError{Feature: "nonbinary mask graphic"}
		}
		visible, err := opacity(paint.RGB[0])
		if err != nil {
			return err
		}
		shape, err := ParseGeometryPath(identity.pathData(mark.Path, Box{}))
		if err != nil {
			return err
		}
		shape, err = geometry.Normalize(shape, mark.Path.EvenOdd)
		if err != nil {
			return err
		}
		for _, clip := range mark.Style.Clips {
			if len(clip.Text) != 0 {
				return &pdfgo.UnsupportedError{Feature: "text-clipped binary mask"}
			}
			c, err := ParseGeometryPath(identity.pathData(clip, Box{}))
			if err != nil {
				return err
			}
			c, err = geometry.Normalize(c, clip.EvenOdd)
			if err != nil {
				return err
			}
			shape, err = geometry.Combine(shape, c, GeometryIntersect)
			if err != nil {
				return err
			}
		}
		operation := GeometrySubtract
		if visible {
			operation = GeometryUnion
		}
		region, err = geometry.Combine(region, shape, operation)
		return err
	}
	if err := mask.Walk(visitor); err != nil {
		return pdfgo.Path{}, err
	}
	path := pdfgo.Path{}
	for _, segment := range region {
		point := func(p Point) pdfgo.Point { return pdfgo.Point{X: p.X, Y: p.Y} }
		switch segment.Verb {
		case GeometryMove:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "M", Points: []pdfgo.Point{point(segment.End)}})
		case GeometryLine:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "L", Points: []pdfgo.Point{point(segment.End)}})
		case GeometryCubic:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "B", Points: []pdfgo.Point{point(segment.Control1), point(segment.Control2), point(segment.End)}})
		case GeometryClose:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "C"})
		default:
			return pdfgo.Path{}, fmt.Errorf("unsupported mask geometry verb %d", segment.Verb)
		}
	}
	if p.maskClips == nil {
		p.maskClips = make(map[*pdfgo.SoftMask]pdfgo.Path)
	}
	p.maskClips[mask] = path
	return path, nil
}

// pdfNumbersPathBox 编码PDF坐标矩形的闭合路径
// 入参: box 矩形边界
// 返回: string 紧缩路径
func pdfNumbersPathBox(box pdfgo.Rectangle) string {
	return "M " + pdfNumbers(box.XMin, box.YMin) + " L " + pdfNumbers(box.XMax, box.YMin) + " L " + pdfNumbers(box.XMax, box.YMax) + " L " + pdfNumbers(box.XMin, box.YMax) + " C"
}

// maskStyle 将独立图元的二值蒙版附加到裁剪链
// 入参: style 图形状态
// 返回: pdfgo.Style 转换后的状态, error 蒙版转换错误
func (p *pdfImporter) maskStyle(style pdfgo.Style) (pdfgo.Style, error) {
	if style.SoftMask == nil {
		return style, nil
	}
	clip, err := p.maskClip(style.SoftMask)
	if err != nil {
		return style, err
	}
	style.Clips = append(append([]pdfgo.Path(nil), style.Clips...), clip)
	style.SoftMask = nil
	return style, nil
}
