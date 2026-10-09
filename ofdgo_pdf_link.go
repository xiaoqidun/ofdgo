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
	annotations, err := page.AnnotationsContext(ctx)
	if err != nil {
		return err
	}
	markups, err := p.reader.ReadThreeDMarkups(ctx, annotations)
	if err != nil {
		return err
	}
	var markupState pdfThreeDMarkupState
	parents := make(map[pdfgo.Reference]int)
	for i, annotation := range annotations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if annotation.Reference != (pdfgo.Reference{}) && annotation.Subtype != "Popup" {
			parents[annotation.Reference] = i
		}
	}
	attached := make(map[int]bool)
	for i, annotation := range annotations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if annotation.Subtype != "Popup" {
			continue
		}
		popup, err := p.reader.ReadPopupAnnotation(annotation.Dictionary)
		if err != nil {
			return err
		}
		if parent, ok := parents[popup.Parent]; ok {
			dict := maps.Clone(annotations[parent].Dictionary)
			previous, err := p.reader.Resolve(dict["Popup"])
			if err != nil {
				return err
			}
			if previous != nil {
				if reference, ok := dict["Popup"].(pdfgo.Reference); !ok || reference != annotation.Reference {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if attached[i] {
			continue
		}
		if len(markups) != 0 {
			annotation, err = p.threeDMarkupAnnotation(ctx, annotation, markups[i], &markupState, strict)
			if err != nil {
				return err
			}
		}
		dict := annotation.Dictionary
		optional, err := p.reader.Resolve(dict["OC"])
		if err != nil {
			return err
		}
		if optional != nil {
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
		popupObject, err := p.reader.Resolve(dict["Popup"])
		if err != nil {
			return err
		}
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
			id, err := p.soundResource(ctx, sound)
			if err != nil {
				return err
			}
			if id == "" {
				if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
					return err
				}
				continue
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
		if annotation.Subtype == "Screen" {
			if err := p.screenAnnotation(ctx, page, annotation, strict); err != nil {
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
			id, err := p.attachmentFile(file.Name, data)
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
		if annotation.Subtype == "Redact" {
			if strict {
				return &pdfgo.UnsupportedError{Feature: "pending PDF redaction conversion"}
			}
			if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
				return err
			}
			p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF pending redaction appearance imported; content removal not applied; overlay data not transferred"})
			continue
		}
		if annotation.Subtype == "Projection" && strict {
			return &pdfgo.UnsupportedError{Feature: "projection annotation runtime conversion"}
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
		flags, err := p.reader.ReadAnnotationFlags(annotation)
		if err != nil {
			return err
		}
		if flags&64 != 0 {
			if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
				return err
			}
			continue
		}
		primary, err := p.reader.Resolve(dict["A"])
		if err != nil {
			return err
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
		if primary == nil {
			if err := appendAction(dict); err != nil {
				return err
			}
		} else if err := p.reader.WalkActions(ctx, primary, func(info pdfgo.ActionInfo) error {
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
	value, err := p.reader.Resolve(dict["A"])
	if err != nil {
		return nil, err
	}
	if target == nil && value == nil {
		return nil, nil
	}
	if value != nil {
		a, ok := value.(pdfgo.Dictionary)
		if !ok {
			return nil, fmt.Errorf("invalid PDF link action")
		}
		kind, err := p.reader.Resolve(a["S"])
		if err != nil {
			return nil, err
		}
		switch kind {
		case pdfgo.Name("GoTo"):
			target = a["D"]
		case pdfgo.Name("GoToR"):
			return p.remoteLinkAction(a, strict)
		case pdfgo.Name("Launch"):
			return p.launchLinkAction(a, strict)
		case pdfgo.Name("Sound"):
			return p.soundLinkAction(a, strict)
		case pdfgo.Name("JavaScript"), pdfgo.Name("ResetForm"), pdfgo.Name("ImportData"), pdfgo.Name("Hide"), pdfgo.Name("Movie"):
			return p.staticLinkAction(a, kind.(pdfgo.Name), strict)
		case pdfgo.Name("GoToE"):
			converted, local, err := p.embeddedLinkAction(a, strict)
			if err != nil || local == nil {
				return converted, err
			}
			target = local
		case pdfgo.Name("Named"):
			value, err := p.reader.Resolve(a["N"])
			if err != nil {
				return nil, err
			}
			if _, ok := value.(pdfgo.Name); !ok {
				return nil, fmt.Errorf("invalid PDF named action")
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
				return nil, nil
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
		if destination.Mode == "FitBH" || destination.Mode == "FitBV" {
			return nil, &pdfgo.UnsupportedError{Feature: "destination mode " + string(destination.Mode)}
		}
		target, err := destination.Transform(matrix)
		if err != nil {
			return nil, err
		}
		if destination.Mode == "FitB" {
			box, err := p.destinationBounds(targetPage)
			if err != nil {
				if canceled := p.ctx.Err(); canceled != nil {
					return nil, canceled
				}
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				if strict {
					return nil, err
				}
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("PDF FitB destination bounds unavailable: %v; link omitted", err)})
				return nil, nil
			}
			target.Mode = "FitR"
			target.Left, target.Top = box.X, box.Y
			target.Right, target.Bottom = box.X+box.W, box.Y+box.H
			target.KeepLeft, target.KeepTop = false, false
		}
		dest := &Dest{Type: string(target.Mode), PageID: p.pageIDs[destination.Page], Left: target.Left, Top: target.Top, Right: target.Right, Bottom: target.Bottom, Zoom: target.Zoom, OmitLeft: target.KeepLeft, OmitTop: target.KeepTop, OmitZoom: target.KeepZoom}
		if target.KeepLeft || target.KeepTop {
			if strict {
				return nil, &pdfgo.UnsupportedError{Feature: "destination retained coordinates cannot be represented in OFD"}
			}
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF destination retained coordinates use OFD default zero"})
		}
		if dest.Type == "XYZ" && !dest.OmitZoom && dest.Zoom != 0 && (dest.Zoom < 0.1 || dest.Zoom > 64) {
			if strict {
				return nil, &pdfgo.UnsupportedError{Feature: "destination zoom outside OFD range"}
			}
			dest.OmitZoom = true
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF destination zoom outside OFD range; current zoom retained"})
		}
		*dest = dest.effective()
		action.Goto = &Goto{Dest: dest}
		*currentPage = p.pageIndexes[destination.Page]
	}
	return &action, nil
}

// staticLinkAction 校验OFD无法等价表达的标准动作，按策略保留静态外观
// 入参: object PDF动作, kind 动作类型, strict 是否禁止交互损失
// 返回: *Action 空动作, error 无效参数或不可等价转换错误
func (p *pdfImporter) staticLinkAction(object pdfgo.Object, kind pdfgo.Name, strict bool) (*Action, error) {
	var err error
	switch kind {
	case "JavaScript":
		_, err = p.reader.ReadJavaScriptAction(p.ctx, object)
	case "ResetForm":
		_, err = p.reader.ReadResetFormAction(p.ctx, object)
	case "ImportData":
		_, err = p.reader.ReadImportDataAction(p.ctx, object)
	case "Hide":
		_, err = p.reader.ReadHideAction(p.ctx, object)
	case "Movie":
		_, err = p.reader.ReadMovieAction(p.ctx, object)
	}
	if err != nil {
		return nil, err
	}
	if strict {
		return nil, &pdfgo.UnsupportedError{Feature: "interactive " + string(kind) + " action conversion"}
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF " + string(kind) + " action not transferred to OFD; static appearance retained"})
	return nil, nil
}
