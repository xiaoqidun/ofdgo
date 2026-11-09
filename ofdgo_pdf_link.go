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
	"context"
	"errors"
	"fmt"
	"maps"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfEmbeddedFile 保存目标路径的容器与原始文件，阅读器按需打开
type pdfEmbeddedFile struct {
	reader *pdfgo.Reader
	file   *pdfgo.FileSpecification
	data   []byte
}

// annotations 将链接、静态注解和表单外观按页面顺序转换为OFD对象
// 入参: ctx 取消上下文, page PDF页面, strict 严格检查开关
// 返回: error 错误信息
func (p *pdfImporter) annotations(ctx context.Context, page *pdfgo.Page, strict bool) error {
	p.movieBounds = nil
	annotations, err := p.pageAnnotations(ctx, page)
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
		opened, err := p.annotationTriggerActions(ctx, annotation, "PO")
		if err != nil {
			return err
		}
		if len(opened) != 0 {
			p.editor.pages[p.page].Actions = append(p.editor.pages[p.page].Actions, opened...)
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
		popup, err := p.popupAnnotation(ctx, annotation.Dictionary)
		if err != nil {
			return err
		}
		if parent, ok := parents[popup.Parent]; ok {
			dict := maps.Clone(annotations[parent].Dictionary)
			previous, err := p.reader.Resolve(dict["Popup"])
			if err != nil {
				if err := p.recoverPDFObject(ctx, "Annotation.Popup", dict["Popup"], err); err != nil {
					return err
				}
			}
			if previous != nil {
				if reference, ok := dict["Popup"].(pdfgo.Reference); !ok || reference != annotation.Reference {
					if err := p.recoverPDFObject(ctx, "Annotation.Popup", dict["Popup"], fmt.Errorf("inconsistent PDF popup parent")); err != nil {
						return err
					}
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
			value, err := p.annotationOpen(ctx, dict)
			if err != nil {
				return err
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
			popup, err := p.popupAnnotation(ctx, popupObject)
			if err != nil {
				return err
			}
			if annotation.Subtype != "Popup" && popup.Parent != (pdfgo.Reference{}) && popup.Parent != annotation.Reference {
				if err := p.recoverPDFObject(ctx, "Annotation.Popup.Parent", popupObject, fmt.Errorf("inconsistent PDF popup parent")); err != nil {
					return err
				}
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
				if err := p.recoverPDFObject(ctx, "Annotation.Sound", dict["Sound"], err); err != nil {
					return err
				}
				if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
					return err
				}
				continue
			}
			id, err := p.soundResource(ctx, sound)
			if err != nil {
				if err := p.recoverPDFObject(ctx, "Annotation.Sound", dict["Sound"], err); err != nil {
					return err
				}
				id = ""
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
			if err := p.movieAnnotation(ctx, page, annotation, strict); err != nil {
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
				if err := p.recoverAnnotationAuxiliary(ctx, annotation, "Annotation.Field", annotation.Dictionary, err); err != nil {
					return err
				}
				if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
					return err
				}
				continue
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
		var actions []Action
		currentPage := p.page
		appendAction := func(dict pdfgo.Dictionary) error {
			current := annotation
			current.Dictionary = dict
			converted, err := p.linkActions(current, strict, &currentPage)
			if err != nil {
				return err
			}
			actions = append(actions, converted...)
			return nil
		}
		primary, err := p.reader.Resolve(dict["A"])
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Annotation.A", dict["A"], err); err != nil {
				return err
			}
		} else if primary == nil {
			if err := appendAction(dict); err != nil {
				return err
			}
		} else {
			actions, err = p.eventActions(ctx, dict["A"], "CLICK", annotation, &currentPage)
			if err != nil {
				return err
			}
		}

		b := annotation.Rect
		box := pdfBounds([]pdfgo.Point{p.matrix.Apply(pdfgo.Point{X: b.XMin, Y: b.YMin}), p.matrix.Apply(pdfgo.Point{X: b.XMax, Y: b.YMax})})
		region, err := p.linkRegion(annotation, box)
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Annotation.QuadPoints", dict["QuadPoints"], err); err != nil {
				return err
			}
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

// linkActions 将单个PDF动作转换为有序OFD动作，保留动态媒体控制的目标范围
// 入参: annotation 触发注解, strict 严格检查开关, currentPage 动作链当前页
// 返回: []Action 动作序列，空切片表示无操作，nil表示未转换, error 转换错误
func (p *pdfImporter) linkActions(annotation pdfgo.Annotation, strict bool, currentPage *int) (actions []Action, err error) {
	object := pdfgo.Dictionary{}
	for _, key := range []pdfgo.Name{"A", "Dest"} {
		if value, ok := annotation.Dictionary[key]; ok {
			object[key] = value
		}
	}
	warnings := len(p.report.Warnings)
	defer func() {
		if err != nil && pdfAuxiliaryError(err) {
			err = p.recoverPDFObject(p.ctx, "Link", object, err)
			if err == nil {
				actions = nil
				*currentPage = -1
			}
		} else if err == nil && (actions == nil || len(p.report.Warnings) != warnings) && len(object) != 0 && p.warning != nil {
			err = p.preservePDFObject(p.ctx, "Link", object)
		}
	}()
	return p.convertLinkActions(annotation, strict, currentPage)
}

// convertLinkActions 转换单个动作，由调用方在辅助失败时保留原始来源
// 入参: annotation 触发注解, strict 严格检查开关, currentPage 动作链当前页
// 返回: []Action 转换动作, error 动作或读取错误
func (p *pdfImporter) convertLinkActions(annotation pdfgo.Annotation, strict bool, currentPage *int) ([]Action, error) {
	value, err := p.reader.Resolve(annotation.Dictionary["A"])
	if err != nil {
		return nil, err
	}
	if dict, ok := value.(pdfgo.Dictionary); ok {
		kind, err := p.reader.Resolve(dict["S"])
		if err != nil {
			return nil, err
		}
		if kind == pdfgo.Name("Rendition") {
			return p.renditionLinkActions(dict, annotation, strict, *currentPage)
		}
	}
	action, err := p.linkAction(annotation, strict, currentPage)
	if err != nil || action == nil {
		return nil, err
	}
	return []Action{*action}, nil
}

// linkAction 将单个非呈现动作转换为OFD动作，失效目标按导入策略报告
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
		case pdfgo.Name("Movie"):
			return p.movieLinkAction(a, annotation, strict, *currentPage)
		case pdfgo.Name("JavaScript"), pdfgo.Name("ResetForm"), pdfgo.Name("ImportData"), pdfgo.Name("Hide"):
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
				if index < 0 {
					return nil, &pdfgo.UnsupportedError{Feature: "relative page action without a fixed current page"}
				}
				index = min(index+1, len(p.editor.pages)-1)
			case pdfgo.Name("PrevPage"):
				if index < 0 {
					return nil, &pdfgo.UnsupportedError{Feature: "relative page action without a fixed current page"}
				}
				index = max(index-1, 0)
			default:
				return nil, nil
			}
			action.Goto = &Goto{Dest: &Dest{Type: "XYZ", PageID: p.editor.pages[index].ID, OmitLeft: true, OmitTop: true, OmitZoom: true}}
			p.editor.setPDFDestination(*action.Goto.Dest, pdfgo.DestinationTarget{Mode: "XYZ", KeepLeft: true, KeepTop: true, KeepZoom: true})
			*currentPage = index
		case pdfgo.Name("URI"):
			value, err := p.reader.Resolve(a["URI"])
			if err != nil {
				return nil, err
			}
			if value == nil && !strict {
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF URI target missing; action omitted"})
				return nil, nil
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
		matrix, _, _ := codecpdf.PageMatrix(targetPage)
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
				if !pdfAuxiliaryError(err) {
					return nil, err
				}
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
		p.editor.setPDFDestination(*dest, target)
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

// launchLinkAction 保留文件打开目标，不执行应用程序或猜测平台启动参数
// 入参: object 启动动作, strict 是否禁止启动语义损失
// 返回: *Action 附件动作, error 解析、取消或无法表达的语义
func (p *pdfImporter) launchLinkAction(object pdfgo.Object, strict bool) (*Action, error) {
	action, err := p.reader.ReadLaunchAction(p.ctx, object)
	if err != nil {
		return nil, err
	}
	if action.File == nil || action.Windows != nil || action.Mac != nil || action.Unix != nil {
		if strict {
			return nil, &pdfgo.UnsupportedError{Feature: "platform launch action conversion"}
		}
		p.warning(pdfgo.Diagnostic{Message: "PDF platform launch parameters not transferred to OFD; appearance retained"})
		return nil, nil
	}
	if strict && action.NewWindow == nil {
		return nil, &pdfgo.UnsupportedError{Feature: "launch reader window preference conversion"}
	}
	data, available, err := p.mediaData(p.ctx, *action.File)
	if err != nil || !available {
		return nil, err
	}
	pdfTarget := bytes.HasPrefix(bytes.TrimSpace(data[:min(len(data), 1024)]), []byte("%PDF-"))
	if !pdfTarget && strict {
		return nil, &pdfgo.UnsupportedError{Feature: "application or non-PDF launch action conversion"}
	}
	id, err := p.attachmentFile(action.File.Name, data)
	if err != nil {
		return nil, err
	}
	window := action.NewWindow
	if !pdfTarget {
		window = nil
		p.warning(pdfgo.Diagnostic{Message: "PDF launch target retained as attachment; application execution not transferred to OFD"})
	} else if window == nil {
		p.warning(pdfgo.Diagnostic{Message: "PDF launch target retained as attachment; reader window preference not transferred to OFD"})
	}
	return &Action{Event: "CLICK", GotoA: &GotoA{AttachID: id, NewWindow: window}}, nil
}

// remoteLinkAction 保留调用方提供的远程文件，不生成OFD无法表达的文件内目标
// 入参: object 远程跳转动作, strict 是否禁止目标语义损失
// 返回: *Action 附件动作, error 结构、取消或转换错误
func (p *pdfImporter) remoteLinkAction(object pdfgo.Object, strict bool) (*Action, error) {
	action, err := p.reader.ReadExternalGoToAction(p.ctx, object)
	if err != nil {
		return nil, err
	}
	if strict {
		return nil, &pdfgo.UnsupportedError{Feature: "remote document destination conversion"}
	}
	if p.resolveFile == nil {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF remote link file unavailable: " + action.File.Name + "; appearance retained, remote destination not transferred to OFD"})
		return nil, nil
	}
	data, err := p.reader.ReadFileData(p.ctx, *action.File, p.resolveFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errPDFMediaRead, err)
	}
	id, err := p.attachmentFile(action.File.Name, data)
	if err != nil {
		return nil, err
	}
	message := "PDF remote link target retained as attachment; destination inside attached PDF not transferred to OFD"
	if action.NewWindow == nil {
		message += "; reader window preference not transferred"
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: message})
	return &Action{Event: "CLICK", GotoA: &GotoA{AttachID: id, NewWindow: action.NewWindow}}, nil
}

// embeddedLinkAction 按目标路径定位附件，回到源文档时保留页内目标
// 入参: object 嵌入文件动作, strict 是否禁止附件内目标语义损失
// 返回: *Action 附件动作, pdfgo.Object 源文档目标, error 目标或转换错误
func (p *pdfImporter) embeddedLinkAction(object pdfgo.Object, strict bool) (*Action, pdfgo.Object, error) {
	action, err := p.reader.ReadExternalGoToAction(p.ctx, object)
	if err != nil {
		return nil, nil, err
	}
	stack := []pdfEmbeddedFile{{reader: p.reader}}
	if action.File != nil {
		data, available, err := p.mediaData(p.ctx, *action.File)
		if err != nil || !available {
			return nil, nil, err
		}
		stack[0] = pdfEmbeddedFile{file: action.File, data: data}
	}
	var opened []*pdfgo.Reader
	defer func() {
		for _, reader := range opened {
			reader.Close()
		}
	}()
	open := func(file *pdfEmbeddedFile) error {
		if file.reader != nil {
			return nil
		}
		if err := p.ctx.Err(); err != nil {
			return err
		}
		if file.data == nil {
			var err error
			file.data, err = p.reader.ReadFileData(p.ctx, *file.file, p.resolveFile)
			if err != nil {
				return err
			}
		}
		reader, err := pdfgo.NewReaderWithOptions(bytes.NewReader(file.data), int64(len(file.data)), pdfgo.ReaderOptions{Warning: p.warning})
		if err != nil {
			return err
		}
		file.reader = reader
		opened = append(opened, reader)
		return p.ctx.Err()
	}
	for _, target := range action.Target {
		if err := p.ctx.Err(); err != nil {
			return nil, nil, err
		}
		if target.Relation == "P" {
			if len(stack) == 1 {
				if strict {
					return nil, nil, &pdfgo.UnsupportedError{Feature: "embedded link parent document unavailable"}
				}
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded link parent document unavailable; link omitted"})
				return nil, nil, nil
			}
			stack = stack[:len(stack)-1]
			continue
		}
		current := &stack[len(stack)-1]
		if err := open(current); err != nil {
			return nil, nil, err
		}
		file, err := current.reader.ReadEmbeddedTargetFile(p.ctx, target)
		if err != nil {
			if !strict && errors.Is(err, pdfgo.ErrDestinationNotFound) {
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("%v; link omitted", err)})
				return nil, nil, nil
			}
			return nil, nil, err
		}
		stack = append(stack, pdfEmbeddedFile{file: &file})
	}
	current := &stack[len(stack)-1]
	if current.file == nil {
		destination, err := current.reader.ResolveRemoteDestination(p.ctx, action.Destination)
		if err != nil {
			if !strict && errors.Is(err, pdfgo.ErrDestinationNotFound) {
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("%v; link omitted", err)})
				return nil, nil, nil
			}
			return nil, nil, err
		}
		if action.NewWindow != nil && *action.NewWindow {
			if strict {
				return nil, nil, &pdfgo.UnsupportedError{Feature: "embedded link to source document in new window"}
			}
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded link new window not transferred to OFD; source document destination retained"})
		}
		return nil, append(pdfgo.Array{destination.Page, destination.Mode}, destination.Parameters...), nil
	}
	if strict {
		return nil, nil, &pdfgo.UnsupportedError{Feature: "destination inside attached PDF"}
	}
	if current.data == nil {
		current.data, err = p.reader.ReadFileData(p.ctx, *current.file, p.resolveFile)
		if err != nil {
			return nil, nil, err
		}
	}
	id, err := p.attachmentFile(current.file.Name, current.data)
	if err != nil {
		return nil, nil, err
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded link target retained as attachment; destination inside attached PDF not transferred to OFD"})
	return &Action{Event: "CLICK", GotoA: &GotoA{AttachID: id, NewWindow: action.NewWindow}}, nil, nil
}

// pdfDestinations 保存PDF导入文档的运行时目标语义，不写入OFD包
type pdfDestinations struct {
	root    string
	targets map[Dest]pdfgo.DestinationTarget
}

// clone 复制定位信息，避免增量导入及编辑影响已交付的阅读快照
// 返回: *pdfDestinations 独立定位信息
func (d *pdfDestinations) clone() *pdfDestinations {
	if d == nil {
		return nil
	}
	return &pdfDestinations{root: d.root, targets: maps.Clone(d.targets)}
}

// PDFDestination 获取当前文档的PDF运行时定位语义，坐标单位为毫米
// 返回值独立于阅读器；普通OFD及保存为OFD再打开的文档不返回PDF目标
// 入参: dest 当前文档动作中的目标
// 返回: pdfgo.DestinationTarget 页面左上原点的定位目标, bool 是否来自PDF导入快照
func (r *Reader) PDFDestination(dest Dest) (pdfgo.DestinationTarget, bool) {
	if d := r.pdfDestinations; d != nil && d.root == r.documentRoot {
		target, ok := d.targets[pdfDestinationKey(dest)]
		return target, ok
	}
	return pdfgo.DestinationTarget{}, false
}

// setPDFDestination 保存PDF目标到导入文档的运行时索引
// 入参: dest OFD动作目标, target 保留空坐标的PDF定位语义
func (e *Editor) setPDFDestination(dest Dest, target pdfgo.DestinationTarget) {
	target.Zoom, target.KeepZoom = dest.effective().Zoom, dest.OmitZoom || dest.Zoom == 0
	if e.pdfDestinations == nil {
		e.pdfDestinations = &pdfDestinations{root: "Doc_0/Document.xml", targets: make(map[Dest]pdfgo.DestinationTarget)}
	}
	e.pdfDestinations.targets[pdfDestinationKey(dest)] = target
}

// pdfDestinationKey 按OFD模式实际编码的属性生成运行时目标索引
// 入参: dest OFD目标
// 返回: Dest 与序列化后目标一致的键
func pdfDestinationKey(dest Dest) Dest {
	dest = dest.effective()
	switch dest.Type {
	case "Fit":
		dest.Left, dest.Top, dest.Right, dest.Bottom, dest.Zoom = 0, 0, 0, 0, 0
		dest.OmitLeft, dest.OmitTop, dest.OmitZoom = true, true, true
	case "FitH":
		dest.Left, dest.Right, dest.Bottom, dest.Zoom = 0, 0, 0, 0
		dest.OmitLeft, dest.OmitZoom = true, true
	case "FitV":
		dest.Top, dest.Right, dest.Bottom, dest.Zoom = 0, 0, 0, 0
		dest.OmitTop, dest.OmitZoom = true, true
	case "FitR":
		dest.Zoom, dest.OmitZoom = 0, true
	case "XYZ":
		dest.Right, dest.Bottom = 0, 0
	}
	return dest
}
