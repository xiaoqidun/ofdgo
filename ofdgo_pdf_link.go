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
	"maps"

	"github.com/xiaoqidun/pdfgo"
)

// annotations 将链接、静态注解和表单外观按页面顺序转换为OFD对象
// 入参: ctx 取消上下文, page PDF页面, strict 严格检查开关
// 返回: error 错误信息
func (p *pdfImporter) annotations(ctx context.Context, page *pdfgo.Page, strict bool) error {
	annotations, err := page.Annotations()
	if err != nil {
		return err
	}
	parents := make(map[pdfgo.Reference]int)
	for i, annotation := range annotations {
		if annotation.Reference != (pdfgo.Reference{}) && annotation.Subtype != "Popup" {
			parents[annotation.Reference] = i
		}
	}
	attached := make(map[int]bool)
	for i, annotation := range annotations {
		if annotation.Subtype != "Popup" {
			continue
		}
		popup, err := p.reader.ReadPopupAnnotation(annotation.Dictionary)
		if err != nil {
			return err
		}
		if parent, ok := parents[popup.Parent]; ok {
			dict := maps.Clone(annotations[parent].Dictionary)
			if previous := dict["Popup"]; previous != nil {
				if reference, ok := previous.(pdfgo.Reference); !ok || reference != annotation.Reference {
					return fmt.Errorf("inconsistent PDF popup parent")
				}
			}
			dict["Popup"] = annotation.Dictionary
			annotations[parent].Dictionary = dict
			attached[i] = true
		} else {
			annotations[i].Dictionary = popup.Dictionary
		}
	}
	for i, annotation := range annotations {
		if attached[i] {
			continue
		}
		dict := annotation.Dictionary
		if dict["OC"] != nil {
			if strict {
				return &pdfgo.UnsupportedError{Feature: "annotation optional content conversion"}
			}
			visible, err := p.reader.OptionalContentVisible(dict["OC"])
			if err != nil {
				return err
			}
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF annotation optional content default visibility applied; layer switching not transferred"})
			if !visible {
				continue
			}
			dict = maps.Clone(dict)
			delete(dict, "OC")
			annotation.Dictionary = dict
		}
		if annotation.Subtype == "Text" {
			value, err := p.reader.Resolve(dict["Open"])
			if err != nil {
				return err
			}
			if value != nil && value != pdfgo.Boolean(false) && value != pdfgo.Boolean(true) {
				return fmt.Errorf("invalid PDF text annotation open state")
			}
			if value == pdfgo.Boolean(true) {
				if strict {
					return &pdfgo.UnsupportedError{Feature: "initially open text annotation"}
				}
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF text annotation open state retained as annotation parameter"})
			}
		}
		popupObject := dict["Popup"]
		if annotation.Subtype == "Popup" {
			popupObject = dict
		}
		if popupObject != nil {
			popup, err := p.reader.ReadPopupAnnotation(popupObject)
			if err != nil {
				return err
			}
			if annotation.Subtype != "Popup" && popup.Parent != (pdfgo.Reference{}) && popup.Parent != annotation.Reference {
				return fmt.Errorf("inconsistent PDF popup parent")
			}
			if popup.Open {
				if strict {
					return &pdfgo.UnsupportedError{Feature: "initially open annotation popup"}
				}
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF popup open state retained as annotation parameter"})
			}
		}
		if annotation.Subtype == "Sound" {
			sound, err := p.reader.ReadSound(dict["Sound"])
			if err != nil {
				return err
			}
			var data []byte
			format := "WAV"
			if sound.File != nil {
				var available bool
				data, available, err = p.mediaData(ctx, *sound.File)
				if err == nil && !available {
					if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
						return err
					}
					continue
				}
				format = pdfMediaFormat(*sound.File)
			} else {
				data, err = pdfSoundWAV(sound)
			}
			if err != nil {
				return err
			}
			id, err := p.editor.AddMedia("Audio", format, data)
			if err != nil {
				return err
			}
			if err := p.appearanceAnnotation(ctx, page, annotation, Action{Event: "CLICK", Sound: &Sound{ResourceID: id}}); err != nil {
				return err
			}
			continue
		}
		if annotation.Subtype == "Movie" {
			if err := p.movieAnnotation(ctx, page, annotation); err != nil {
				return err
			}
			continue
		}
		if annotation.Subtype == "3D" || annotation.Subtype == "RichMedia" {
			if err := p.interactiveAnnotation(ctx, page, annotation, strict); err != nil {
				return err
			}
			continue
		}
		if annotation.Subtype == "FileAttachment" {
			file, err := p.reader.ReadFileSpecification(dict["FS"])
			if err != nil {
				return err
			}
			data, available, err := p.mediaData(ctx, file)
			if err != nil {
				return err
			}
			if !available {
				if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
					return err
				}
				continue
			}
			id, err := p.editor.AddAttachment(file.Name, data)
			if err != nil {
				return err
			}
			if err := p.appearanceAnnotation(ctx, page, annotation, Action{Event: "CLICK", GotoA: &GotoA{AttachID: id}}); err != nil {
				return err
			}
			continue
		}
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
		if annotation.Subtype != "Link" && pdfAnnotationType(annotation.Subtype) != "" {
			if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
				return err
			}
			continue
		}
		if annotation.Subtype != "Link" {
			return &pdfgo.UnsupportedError{Feature: "annotation " + string(annotation.Subtype)}
		}
		if dict["AA"] != nil {
			return &pdfgo.UnsupportedError{Feature: "link annotation field \"AA\""}
		}
		var actions []Action
		currentPage := p.page
		appendAction := func(dict pdfgo.Dictionary) error {
			current := annotation
			current.Dictionary = dict
			action, err := p.linkAction(current, strict, &currentPage)
			if err != nil {
				return err
			}
			if action != nil {
				actions = append(actions, *action)
			}
			return nil
		}
		if dict["A"] == nil {
			if err := appendAction(dict); err != nil {
				return err
			}
		} else if err := p.reader.WalkActions(ctx, dict["A"], func(info pdfgo.ActionInfo) error {
			current := maps.Clone(dict)
			current["A"] = info.Dictionary
			return appendAction(current)
		}); err != nil {
			return err
		}

		b := annotation.Rect
		box := pdfBounds([]pdfgo.Point{p.matrix.Apply(pdfgo.Point{X: b.XMin, Y: b.YMin}), p.matrix.Apply(pdfgo.Point{X: b.XMax, Y: b.YMax})})
		region, err := p.linkRegion(annotation, box)
		if err != nil {
			return err
		}
		for i := range actions {
			actions[i].Region = region
		}
		if err := p.appearanceAnnotation(ctx, page, annotation, actions...); err != nil {
			return err
		}
		if len(actions) != 0 {
			p.report.Links++
		}
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

// linkAction 将单个链接动作转换为OFD动作，失效目标按导入策略报告
// 入参: annotation 链接注解, strict 严格检查开关, currentPage 动作链当前页
// 返回: *Action 动作，失效目标为空, error 转换错误
func (p *pdfImporter) linkAction(annotation pdfgo.Annotation, strict bool, currentPage *int) (*Action, error) {
	dict := annotation.Dictionary
	action := Action{Event: "CLICK"}
	target, err := p.reader.Resolve(dict["Dest"])
	if err != nil {
		return nil, err
	}
	if target == nil && dict["A"] == nil {
		return nil, nil
	}
	if dict["A"] != nil {
		value, err := p.reader.Resolve(dict["A"])
		if err != nil {
			return nil, err
		}
		a, ok := value.(pdfgo.Dictionary)
		if !ok {
			return nil, fmt.Errorf("invalid PDF link action")
		}
		switch a["S"] {
		case pdfgo.Name("GoTo"):
			target = a["D"]
		case pdfgo.Name("Named"):
			value, err := p.reader.Resolve(a["N"])
			if err != nil {
				return nil, err
			}
			index := *currentPage
			switch value {
			case pdfgo.Name("FirstPage"):
				index = 0
			case pdfgo.Name("LastPage"):
				index = len(p.editor.pages) - 1
			case pdfgo.Name("NextPage"):
				index = min(index+1, len(p.editor.pages)-1)
			case pdfgo.Name("PrevPage"):
				index = max(index-1, 0)
			default:
				return nil, &pdfgo.UnsupportedError{Feature: "named link action"}
			}
			action.Goto = &Goto{Dest: &Dest{Type: "XYZ", PageID: p.editor.pages[index].ID, OmitLeft: true, OmitTop: true, OmitZoom: true}}
			*currentPage = index
		case pdfgo.Name("URI"):
			value, err := p.reader.Resolve(a["URI"])
			if err != nil {
				return nil, err
			}
			uri, ok := value.(pdfgo.String)
			if !ok {
				return nil, fmt.Errorf("invalid PDF URI")
			}
			isMap, err := p.reader.Resolve(a["IsMap"])
			if err != nil {
				return nil, err
			}
			if isMap != nil && isMap != pdfgo.Boolean(false) && isMap != pdfgo.Boolean(true) {
				return nil, fmt.Errorf("invalid PDF URI image map flag")
			}
			if isMap == pdfgo.Boolean(true) {
				return nil, &pdfgo.UnsupportedError{Feature: "URI image map"}
			}
			base, err := p.reader.BaseURI()
			if err != nil {
				return nil, err
			}
			action.URI = &URI{URI: string(uri), Base: base}
		default:
			return nil, &pdfgo.UnsupportedError{Feature: "link action"}
		}
	}
	if action.URI == nil && action.Goto == nil {
		destination, err := p.reader.ReadDestination(target)
		if err != nil {
			if !strict && errors.Is(err, pdfgo.ErrDestinationNotFound) {
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("%v; link omitted", err)})
				return nil, nil
			}
			return nil, err
		}
		targetPage := p.pages[destination.Page]
		if targetPage == nil {
			if !strict {
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF link targets a page outside the document page tree; link omitted"})
				return nil, nil
			}
			return nil, fmt.Errorf("PDF destination page missing")
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
		} else if destination.Mode == "FitH" || destination.Mode == "FitV" {
			var coordinate float64
			switch value := destination.Parameters[0].(type) {
			case pdfgo.Integer:
				coordinate = float64(value)
			case pdfgo.Real:
				coordinate = float64(value)
			}
			point := pdfgo.Point{X: coordinate}
			if destination.Mode == "FitH" {
				point = pdfgo.Point{Y: coordinate}
			}
			point = matrix.Apply(point)
			if targetPage.Rotation == 90 || targetPage.Rotation == 270 {
				if dest.Type == "FitH" {
					dest.Type = "FitV"
				} else {
					dest.Type = "FitH"
				}
			}
			if dest.Type == "FitH" {
				dest.Top, dest.OmitTop = point.Y, destination.Parameters[0] == nil
			} else {
				dest.Left, dest.OmitLeft = point.X, destination.Parameters[0] == nil
			}
		} else if destination.Mode == "FitR" {
			values := [4]float64{}
			for n, v := range destination.Parameters {
				switch v := v.(type) {
				case pdfgo.Integer:
					values[n] = float64(v)
				case pdfgo.Real:
					values[n] = float64(v)
				default:
					return nil, fmt.Errorf("invalid PDF FitR destination coordinate")
				}
			}
			bounds := pdfBounds([]pdfgo.Point{
				matrix.Apply(pdfgo.Point{X: values[0], Y: values[1]}),
				matrix.Apply(pdfgo.Point{X: values[2], Y: values[3]}),
			})
			dest.Left, dest.Top = bounds.X, bounds.Y
			dest.Right, dest.Bottom = bounds.X+bounds.W, bounds.Y+bounds.H
		} else if destination.Mode != "Fit" {
			return nil, &pdfgo.UnsupportedError{Feature: "destination mode " + string(destination.Mode)}
		}
		action.Goto = &Goto{Dest: dest}
		*currentPage = p.pageIndexes[destination.Page]
	}
	return &action, nil
}
