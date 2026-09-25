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
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// group 将不透明图元的透明组转换为互不重叠的填充区域，保留矢量与组不透明度
// 入参: mark 组边界状态, walk 组内容访问函数, visitor 页面访问器
// 返回: error 不支持的组内容或转换错误
func (p *pdfImporter) group(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error, visitor pdfgo.Visitor) error {
	if err := p.flushPath(); err != nil {
		return err
	}
	if mark.SoftMask != nil {
		clip, err := p.maskClip(mark.SoftMask)
		if err != nil {
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
	geometry, err := p.editor.Geometry()
	if err != nil {
		return err
	}
	type paintRegion struct {
		paint pdfgo.Paint
		path  GeometryPath
	}
	var regions []paintRegion
	var combined GeometryPath
	var paint *pdfgo.Paint
	var clips []pdfgo.Path
	scale := math.Hypot(p.matrix[0], p.matrix[1])
	collect := pdfgo.Visitor{Warning: visitor.Warning}
	collect.Path = func(path pdfgo.PathMark) error {
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
			if color.Alpha != 1 || color.Axial != nil || color.Radial != nil || overprint && pdfOverprintNeedsSeparation(color) {
				return &pdfgo.UnsupportedError{Feature: "transparent or overprinted group content"}
			}
			if paint == nil {
				paint, clips = &color, path.Style.Clips
			} else if !reflect.DeepEqual(clips, path.Style.Clips) {
				return &pdfgo.UnsupportedError{Feature: "independently clipped transparent group"}
			} else if !reflect.DeepEqual(*paint, color) {
				regions = append(regions, paintRegion{*paint, combined})
				combined = nil
				paint = &color
			}
			outline := shape
			evenOdd := path.Path.EvenOdd
			if stroke {
				dashes := make([]float64, len(path.Style.Dash))
				for i, dash := range path.Style.Dash {
					dashes[i] = dash * scale
				}
				outline, err = geometry.Stroke(shape, StrokeOptions{Width: path.Style.LineWidth * scale, Cap: []string{"Butt", "Round", "Square"}[path.Style.Cap], Join: []string{"Miter", "Round", "Bevel"}[path.Style.Join], MiterLimit: path.Style.MiterLimit, DashOffset: path.Style.DashPhase * scale, Dashes: dashes, Tolerance: 0.001})
				if err != nil {
					return err
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
	regions = append(regions, paintRegion{*paint, combined})
	var covered GeometryPath
	for i := len(regions) - 1; i >= 0; i-- {
		region := regions[i]
		visible := region.path
		if len(covered) > 0 {
			visible, err = geometry.Combine(visible, covered, GeometrySubtract)
			if err != nil {
				return err
			}
		}
		if i > 0 {
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
		region.paint.Alpha = mark.Alpha
		fill, stroke := true, false
		object := PathObject{Boundary: pdfBoundary(box), AbbreviatedData: data, Fill: &fill, Stroke: &stroke, FillColor: p.color(region.paint), Clips: p.clips(clips, box)}
		p.objects = append(p.objects, GraphicObject{Type: "PathObject", PathObject: object})
		p.report.PathObjects++
	}
	return nil
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
	background, err := opacity(mask.Background)
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
		if mark.Alpha != 1 || mark.SoftMask != nil {
			return &pdfgo.UnsupportedError{Feature: "transparent mask group"}
		}
		return walk(visitor)
	}
	visitor.Path = func(mark pdfgo.PathMark) error {
		paint := mark.Style.Fill
		if !mark.Fill || mark.Stroke || paint.Alpha != 1 || paint.CMYK != nil || paint.Axial != nil || paint.Radial != nil || paint.RGB[0] != paint.RGB[1] || paint.RGB[1] != paint.RGB[2] || mark.Style.SoftMask != nil {
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
