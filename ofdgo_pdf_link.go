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

// annotations 将链接和签名外观按页面顺序转换为OFD对象
// 入参: ctx取消上下文, page为PDF页面, strict严格检查开关
// 返回: error 错误信息
func (p *pdfImporter) annotations(ctx context.Context, page *pdfgo.Page, strict bool) error {
	annotations, err := page.Annotations()
	if err != nil {
		return err
	}
	for _, annotation := range annotations {
		dict := annotation.Dictionary
		if annotation.Subtype == "Widget" {
			if err := p.signatureWidget(ctx, page, annotation); err != nil {
				return err
			}
			continue
		}
		if annotation.Subtype != "Link" {
			return &pdfgo.UnsupportedError{Feature: "annotation " + string(annotation.Subtype)}
		}
		for _, key := range []pdfgo.Name{"AP", "AA", "BS", "QuadPoints", "OC"} {
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
		border, err := p.reader.Resolve(dict["Border"])
		if err != nil {
			return err
		}
		array, ok := border.(pdfgo.Array)
		if !ok || len(array) != 3 || (array[2] != pdfgo.Integer(0) && array[2] != pdfgo.Real(0)) {
			return &pdfgo.UnsupportedError{Feature: "visible link border"}
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
				return fmt.Errorf("PDF destination page missing")
			}
			matrix, _, _ := pdfPageMatrix(targetPage)
			dest := &Dest{Type: string(destination.Mode), PageID: p.pageIDs[destination.Page]}
			if destination.Mode == "XYZ" {
				values := [3]float64{}
				for n, v := range destination.Parameters {
					switch v := v.(type) {
					case pdfgo.Integer:
						values[n] = float64(v)
					case pdfgo.Real:
						values[n] = float64(v)
					case nil:
						if n < 2 {
							return &pdfgo.UnsupportedError{Feature: "retained destination coordinates"}
						}
					}
				}
				point := matrix.Apply(pdfgo.Point{X: values[0], Y: values[1]})
				dest.Left, dest.Top, dest.Zoom = point.X, point.Y, values[2]
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
		object.Actions = []Action{action}
		p.objects = append(p.objects, GraphicObject{Type: "PathObject", PathObject: object})
		p.report.Links++
	}
	return nil
}
