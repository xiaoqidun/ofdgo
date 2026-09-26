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
	"errors"
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// annotations 将链接、印章和表单外观按页面顺序转换为OFD对象
// 入参: ctx 取消上下文, page PDF页面, strict 严格检查开关
// 返回: error 错误信息
func (p *pdfImporter) annotations(ctx context.Context, page *pdfgo.Page, strict bool) error {
	annotations, err := page.Annotations()
	if err != nil {
		return err
	}
	for _, annotation := range annotations {
		dict := annotation.Dictionary
		if annotation.Subtype == "Widget" {
			field, err := p.reader.ReadField(annotation)
			if err != nil {
				return err
			}
			annotation.Dictionary = field
			if field["FT"] == pdfgo.Name("Sig") && field["V"] != nil {
				err = p.signatureWidget(ctx, page, annotation)
			} else {
				err = p.formWidget(ctx, page, annotation, strict)
			}
			if err != nil {
				return err
			}
			continue
		}
		if annotation.Subtype == "Stamp" {
			if err := p.stampAnnotation(ctx, page, annotation); err != nil {
				return err
			}
			continue
		}
		if annotation.Subtype != "Link" {
			return &pdfgo.UnsupportedError{Feature: "annotation " + string(annotation.Subtype)}
		}
		if dict["AP"] != nil {
			visitor := pdfgo.Visitor{
				Path:  func(pdfgo.PathMark) error { return &pdfgo.UnsupportedError{Feature: "visible link appearance"} },
				Text:  func(pdfgo.TextMark) error { return &pdfgo.UnsupportedError{Feature: "visible link appearance"} },
				Image: func(pdfgo.ImageMark) error { return &pdfgo.UnsupportedError{Feature: "visible link appearance"} },
			}
			visitor.Group = func(_ pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
				return walk(visitor)
			}
			if err := p.reader.WalkAnnotationAppearance(ctx, page, annotation, visitor); err != nil {
				return err
			}
		}
		for _, key := range []pdfgo.Name{"AA", "OC"} {
			if dict[key] != nil {
				return &pdfgo.UnsupportedError{Feature: fmt.Sprintf("link annotation field %q", key)}
			}
		}
		if value, err := p.reader.Resolve(dict["F"]); err != nil {
			return err
		} else if value != nil {
			flags, ok := value.(pdfgo.Integer)
			if !ok {
				return fmt.Errorf("invalid PDF annotation flags")
			}
			if flags != 0 && flags != 4 {
				return &pdfgo.UnsupportedError{Feature: "link annotation flags"}
			}
		}
		borderStyle, err := p.reader.Resolve(dict["BS"])
		if err != nil {
			return err
		}
		if borderStyle != nil {
			style, ok := borderStyle.(pdfgo.Dictionary)
			if !ok {
				return fmt.Errorf("invalid PDF link border style")
			}
			width, err := p.reader.Resolve(style["W"])
			if err != nil {
				return err
			}
			if width != pdfgo.Integer(0) && width != pdfgo.Real(0) {
				return &pdfgo.UnsupportedError{Feature: "visible link border"}
			}
		} else {
			border, err := p.reader.Resolve(dict["Border"])
			if err != nil {
				return err
			}
			array, ok := border.(pdfgo.Array)
			if !ok || len(array) < 3 || (array[2] != pdfgo.Integer(0) && array[2] != pdfgo.Real(0)) {
				return &pdfgo.UnsupportedError{Feature: "visible link border"}
			}
		}
		action := Action{Event: "CLICK"}
		target := dict["Dest"]
		if dict["A"] != nil {
			value, err := p.reader.Resolve(dict["A"])
			if err != nil {
				return err
			}
			a, ok := value.(pdfgo.Dictionary)
			if !ok {
				return fmt.Errorf("invalid PDF link action")
			}
			if a["Next"] != nil {
				return &pdfgo.UnsupportedError{Feature: "chained link actions"}
			}
			switch a["S"] {
			case pdfgo.Name("GoTo"):
				target = a["D"]
			case pdfgo.Name("URI"):
				value, err := p.reader.Resolve(a["URI"])
				if err != nil {
					return err
				}
				uri, ok := value.(pdfgo.String)
				if !ok {
					return fmt.Errorf("invalid PDF URI")
				}
				isMap, err := p.reader.Resolve(a["IsMap"])
				if err != nil {
					return err
				}
				if isMap != nil && isMap != pdfgo.Boolean(false) && isMap != pdfgo.Boolean(true) {
					return fmt.Errorf("invalid PDF URI image map flag")
				}
				if isMap == pdfgo.Boolean(true) {
					return &pdfgo.UnsupportedError{Feature: "URI image map"}
				}
				base, err := p.reader.BaseURI()
				if err != nil {
					return err
				}
				action.URI = &URI{URI: string(uri), Base: base}
			default:
				return &pdfgo.UnsupportedError{Feature: "link action"}
			}
		}
		if action.URI == nil {
			destination, err := p.reader.ReadDestination(target)
			if err != nil {
				if !strict && errors.Is(err, pdfgo.ErrDestinationNotFound) {
					p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("%v; link omitted", err)})
					continue
				}
				return err
			}
			targetPage := p.pages[destination.Page]
			if targetPage == nil {
				if !strict {
					p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF link targets a page outside the document page tree; link omitted"})
					continue
				}
				return fmt.Errorf("PDF destination page missing")
			}
			matrix, _, _ := pdfPageMatrix(targetPage)
			dest := &Dest{Type: string(destination.Mode), PageID: p.pageIDs[destination.Page]}
			if destination.Mode == "XYZ" {
				values := [3]float64{}
				retained := [3]bool{}
				for n, v := range destination.Parameters {
					switch v := v.(type) {
					case pdfgo.Integer:
						values[n] = float64(v)
					case pdfgo.Real:
						values[n] = float64(v)
					case nil:
						retained[n] = true
					}
				}
				point := matrix.Apply(pdfgo.Point{X: values[0], Y: values[1]})
				dest.Left, dest.Top, dest.Zoom = point.X, point.Y, values[2]
				dest.OmitLeft, dest.OmitTop = retained[0], retained[1]
				if targetPage.Rotation == 90 || targetPage.Rotation == 270 {
					dest.OmitLeft, dest.OmitTop = retained[1], retained[0]
				}
				dest.OmitZoom = retained[2] || values[2] == 0
			} else if destination.Mode != "Fit" {
				return &pdfgo.UnsupportedError{Feature: "destination mode " + string(destination.Mode)}
			}
			action.Goto = &Goto{Dest: dest}
		}
		b := annotation.Rect
		box := pdfBounds([]pdfgo.Point{p.matrix.Apply(pdfgo.Point{X: b.XMin, Y: b.YMin}), p.matrix.Apply(pdfgo.Point{X: b.XMax, Y: b.YMax})})
		object, err := NewShape(ShapeRectangle, box)
		if err != nil {
			return err
		}
		no := false
		object.Fill, object.Stroke = &no, &no
		action.Region, err = p.linkRegion(annotation, box)
		if err != nil {
			return err
		}
		object.Actions = []Action{action}
		p.objects = append(p.objects, GraphicObject{Type: "PathObject", PathObject: object})
		p.report.Links++
	}
	return nil
}

// linkRegion 将PDF链接四边形映射为OFD动作区域，越出注解矩形时使用矩形
// 入参: annotation PDF链接注解, box OFD对象边界
// 返回: *Region 点击区域, error 解析错误
func (p *pdfImporter) linkRegion(annotation pdfgo.Annotation, box Box) (*Region, error) {
	value, err := p.reader.Resolve(annotation.Dictionary["QuadPoints"])
	if err != nil || value == nil {
		return nil, err
	}
	array, ok := value.(pdfgo.Array)
	if !ok || len(array) == 0 || len(array)%8 != 0 {
		return nil, nil
	}
	region := &Region{Area: make([]RegionArea, 0, len(array)/8)}
	for start := 0; start < len(array); start += 8 {
		var points [4]pdfgo.Point
		for index := range points {
			coordinates := [2]float64{}
			for axis := range coordinates {
				resolved, err := p.reader.Resolve(array[start+index*2+axis])
				if err != nil {
					return nil, err
				}
				switch number := resolved.(type) {
				case pdfgo.Integer:
					coordinates[axis] = float64(number)
				case pdfgo.Real:
					coordinates[axis] = float64(number)
				default:
					return nil, nil
				}
			}
			x, y := coordinates[0], coordinates[1]
			if !finite(x) || !finite(y) || x < annotation.Rect.XMin || x > annotation.Rect.XMax || y < annotation.Rect.YMin || y > annotation.Rect.YMax {
				return nil, nil
			}
			points[index] = p.matrix.Apply(pdfgo.Point{X: x, Y: y})
		}
		area := RegionArea{Start: pdfNumbers(points[0].X-box.X, points[0].Y-box.Y)}
		for _, point := range points[1:] {
			area.Command = append(area.Command, RegionCommand{Type: "Line", Point1: pdfNumbers(point.X-box.X, point.Y-box.Y)})
		}
		region.Area = append(region.Area, area)
	}
	return region, nil
}
