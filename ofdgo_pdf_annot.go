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
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"

	"github.com/xiaoqidun/pdfgo"
)

// pdfAnnotationType 将具有静态外观的PDF注解类别对应到OFD注解类别
// 入参: subtype PDF注解类别
// 返回: string OFD注解类别，空字符串表示需单独处理
func pdfAnnotationType(subtype pdfgo.Name) string {
	switch subtype {
	case "Stamp":
		return "Stamp"
	case "Watermark":
		return "Watermark"
	case "Highlight", "Underline", "Squiggly", "StrikeOut":
		return "Highlight"
	case "Text", "FreeText", "Line", "Square", "Circle", "Polygon", "PolyLine", "Caret", "Ink", "FileAttachment", "Sound", "Movie", "Screen", "Link", "Popup", "Widget", "PrinterMark", "TrapNet", "3D", "RichMedia", "Redact", "Projection":
		return "Path"
	}
	if !(pdfgo.Annotation{Subtype: subtype}).IsStandard() {
		return "Path"
	}
	return ""
}

// recoverAnnotationObject 在非严格模式下保留或告警跳过故障注解，不吞掉取消与底层读取错误
// 入参: ctx 取消上下文, name 来源字段, object 原始对象, cause 故障原因
// 返回: error 严格模式、取消或不可恢复错误
func (p *pdfImporter) recoverAnnotationObject(ctx context.Context, name string, object pdfgo.Object, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.warning == nil || !pdfAuxiliaryError(cause) {
		return cause
	}
	if err := p.recoverPDFObject(ctx, name, object, cause); err != nil {
		if ctx.Err() != nil || !pdfAuxiliaryError(err) {
			return err
		}
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF " + name + " skipped: " + cause.Error()})
	}
	return nil
}

// recoverAnnotationAuxiliary 仅在存在有效正常外观时保留辅助错误，外观仍由调用方实际绘制
// 入参: ctx 取消上下文, annotation 原始注解, name 辅助字段名, object 原始对象, cause 辅助错误
// 返回: error 严格模式、缺少有效外观或不可恢复错误
func (p *pdfImporter) recoverAnnotationAuxiliary(ctx context.Context, annotation pdfgo.Annotation, name string, object pdfgo.Object, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.warning == nil || !pdfAuxiliaryError(cause) {
		return cause
	}
	appearance, err := p.reader.ReadAnnotationAppearance(annotation)
	if err != nil {
		return err
	}
	if appearance == nil {
		return cause
	}
	return p.recoverPDFObject(ctx, name, object, cause)
}

// popupAnnotation 单独恢复弹窗的打开状态和父引用，不忽略外观或几何错误
// 入参: ctx 取消上下文, object 原始弹窗对象
// 返回: pdfgo.PopupAnnotation 弹窗信息, error 读取或不可恢复错误
func (p *pdfImporter) popupAnnotation(ctx context.Context, object pdfgo.Object) (pdfgo.PopupAnnotation, error) {
	value, err := p.reader.Resolve(object)
	if err != nil {
		return pdfgo.PopupAnnotation{}, p.recoverPDFObject(ctx, "Annotation.Popup", object, err)
	}
	if value == nil {
		return pdfgo.PopupAnnotation{}, nil
	}
	dict, ok := value.(pdfgo.Dictionary)
	if !ok {
		return pdfgo.PopupAnnotation{}, p.recoverPDFObject(ctx, "Annotation.Popup", object, fmt.Errorf("invalid popup annotation"))
	}
	dict = maps.Clone(dict)
	open, err := p.reader.Resolve(dict["Open"])
	if err == nil && open != nil && open != pdfgo.Boolean(false) && open != pdfgo.Boolean(true) {
		err = fmt.Errorf("invalid popup open state")
	}
	if err != nil {
		if err := p.recoverPDFObject(ctx, "Annotation.Popup.Open", dict["Open"], err); err != nil {
			return pdfgo.PopupAnnotation{}, err
		}
		delete(dict, "Open")
	}
	if object := dict["Parent"]; object != nil {
		parent, reference := object.(pdfgo.Reference)
		value, err := p.reader.Resolve(object)
		if err == nil && value != nil {
			parentDict, ok := value.(pdfgo.Dictionary)
			if !reference || !ok {
				err = fmt.Errorf("invalid popup parent annotation")
			} else {
				subtype, resolveErr := p.reader.Resolve(parentDict["Subtype"])
				err = resolveErr
				if kind, ok := subtype.(pdfgo.Name); err == nil && (!ok || kind == "Popup") {
					err = fmt.Errorf("invalid popup parent subtype")
				}
			}
		} else if err == nil && !reference {
			err = fmt.Errorf("invalid popup parent reference")
		}
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Annotation.Popup.Parent", object, err); err != nil {
				return pdfgo.PopupAnnotation{}, err
			}
			delete(dict, "Parent")
		} else {
			dict["Parent"] = parent
		}
	}
	return p.reader.ReadPopupAnnotation(dict)
}

// annotationOpen 读取文本注解初始打开状态，单独保留无效辅助字段
// 入参: ctx 取消上下文, dict 注解字典
// 返回: pdfgo.Object 打开状态, error 不可恢复错误
func (p *pdfImporter) annotationOpen(ctx context.Context, dict pdfgo.Dictionary) (pdfgo.Object, error) {
	value, err := p.reader.Resolve(dict["Open"])
	if err == nil && value != nil && value != pdfgo.Boolean(false) && value != pdfgo.Boolean(true) {
		err = fmt.Errorf("invalid PDF text annotation open state")
	}
	if err != nil {
		return nil, p.recoverPDFObject(ctx, "Annotation.Open", dict["Open"], err)
	}
	return value, nil
}

// appearanceAnnotation 将PDF静态外观转换为对应类型的OFD注解
// 入参: ctx 取消上下文, page PDF页面, annotation PDF注解, actions 外观区域的动作
// 返回: error 外观或转换错误
func (p *pdfImporter) appearanceAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, actions ...Action) error {
	if annotation.Rect.XMin == annotation.Rect.XMax || annotation.Rect.YMin == annotation.Rect.YMax {
		return p.paintedAnnotation(ctx, page, annotation, nil, actions...)
	}
	if !annotation.IsStandard() {
		flags, err := p.reader.ReadAnnotationFlags(annotation)
		if err != nil {
			return err
		}
		if flags&1 != 0 {
			return p.paintedAnnotation(ctx, page, annotation, nil, actions...)
		}
	}
	if p.warning != nil && annotation.Dictionary["AP"] == nil && annotation.Dictionary["BS"] == nil && annotation.Dictionary["Border"] != nil {
		border, err := p.reader.Resolve(annotation.Dictionary["Border"])
		if err != nil {
			return err
		}
		if array, ok := border.(pdfgo.Array); ok && len(array) == 0 {
			if err := p.preservePDFObject(ctx, "Annotation.Border", annotation.Dictionary["Border"]); err != nil {
				return err
			}
		}
	}
	if annotation.Dictionary["AP"] != nil {
		if _, err := p.reader.ReadAnnotationAppearance(annotation); err != nil {
			if !errors.Is(err, pdfgo.ErrInvalidAnnotationAppearance) {
				return err
			}
			if p.warning == nil {
				return err
			}
			if err := p.preservePDFObject(ctx, "Annotation.Appearance", annotation.Dictionary); err != nil {
				return err
			}
		}
	}
	switch annotation.Subtype {
	case "PrinterMark", "TrapNet", "3D", "RichMedia", "Watermark":
		appearance, err := p.reader.Resolve(annotation.Dictionary["AP"])
		if err != nil {
			return err
		}
		if appearance == nil {
			return p.recoverPDFObject(ctx, "Annotation.Appearance", annotation.Dictionary, &pdfgo.UnsupportedError{Feature: "annotation without static appearance"})
		}
	}
	return p.paintedAnnotation(ctx, page, annotation, nil, actions...)
}

// pdfResolveNumber 解析PDF整数或实数为有限浮点数
// 入参: reader PDF阅读器, object PDF对象
// 返回: float64 数值, bool 是否为有效有限数值
func pdfResolveNumber(reader *pdfgo.Reader, object pdfgo.Object) (float64, bool) {
	resolved, err := reader.Resolve(object)
	if err != nil || resolved == nil {
		return 0, false
	}
	switch v := resolved.(type) {
	case pdfgo.Integer:
		return float64(v), true
	case pdfgo.Real:
		n := float64(v)
		return n, finite(n)
	}
	return 0, false
}

// linkAppearanceEmpty 判断无AP的Link注解是否因边框宽为零或颜色为空而必然产出空外观
// 入参: annotation PDF注解
// 返回: bool 是否可安全跳过外观遍历与合成
func (p *pdfImporter) linkAppearanceEmpty(annotation pdfgo.Annotation) bool {
	if p.warning == nil || annotation.Subtype != "Link" || annotation.Dictionary["OC"] != nil || annotation.Rect.XMin >= annotation.Rect.XMax || annotation.Rect.YMin >= annotation.Rect.YMax {
		return false
	}
	if annotation.Dictionary["AP"] != nil {
		ap, err := p.reader.Resolve(annotation.Dictionary["AP"])
		if err != nil || ap != nil {
			return false
		}
	}
	width := 1.0
	if bsObj := annotation.Dictionary["BS"]; bsObj != nil {
		resolved, err := p.reader.Resolve(bsObj)
		if err != nil {
			return false
		}
		if resolved != nil {
			bs, ok := resolved.(pdfgo.Dictionary)
			if !ok {
				return false
			}
			if bs["W"] != nil {
				w, ok := pdfResolveNumber(p.reader, bs["W"])
				if !ok || w < 0 {
					return false
				}
				width = w
			}
			style := pdfgo.Name("S")
			if bs["S"] != nil {
				sv, err := p.reader.Resolve(bs["S"])
				if err != nil {
					return false
				}
				if sv != nil {
					name, ok := sv.(pdfgo.Name)
					if !ok {
						return false
					}
					style = name
				}
			}
			if style == "D" && bs["D"] != nil {
				dv, err := p.reader.Resolve(bs["D"])
				if err != nil {
					return false
				}
				if dv != nil {
					if _, ok := dv.(pdfgo.Array); !ok {
						return false
					}
				}
			}
		}
	} else if borderObj := annotation.Dictionary["Border"]; borderObj != nil {
		resolved, err := p.reader.Resolve(borderObj)
		if err != nil {
			return false
		}
		if resolved != nil {
			border, ok := resolved.(pdfgo.Array)
			if !ok || len(border) < 3 || len(border) > 4 {
				return false
			}
			rx, ok0 := pdfResolveNumber(p.reader, border[0])
			ry, ok1 := pdfResolveNumber(p.reader, border[1])
			w, ok2 := pdfResolveNumber(p.reader, border[2])
			if !ok0 || !ok1 || !ok2 || rx < 0 || ry < 0 || w < 0 {
				return false
			}
			if len(border) == 4 {
				dv, err := p.reader.Resolve(border[3])
				if err != nil {
					return false
				}
				if _, ok := dv.(pdfgo.Array); !ok {
					return false
				}
			}
			width = w
		}
	}
	hasColor := true
	if cObj := annotation.Dictionary["C"]; cObj != nil {
		resolved, err := p.reader.Resolve(cObj)
		if err != nil {
			return false
		}
		if resolved != nil {
			arr, ok := resolved.(pdfgo.Array)
			if !ok {
				return false
			}
			hasColor = len(arr) != 0
		}
	}
	return width == 0 || !hasColor
}

// paintedAnnotation 复用注解坐标、标志与元数据处理，允许替换静态绘制内容
// 入参: ctx 取消上下文, page PDF页面, annotation PDF注解, paint 绘制回调，空值使用原外观, actions 外观区域的动作
// 返回: error 外观或转换错误
func (p *pdfImporter) paintedAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, paint func(*pdfImporter) error, actions ...Action) error {
	if annotation.Subtype == "Projection" {
		if _, err := p.reader.ReadProjectionAnnotation(annotation); err != nil {
			if p.warning == nil || !pdfAuxiliaryError(err) {
				return err
			}
			if _, err := p.reader.ReadAnnotationAppearance(annotation); err != nil {
				return err
			}
			if err := p.recoverPDFObject(ctx, "Annotation.Projection.ExData", annotation.Dictionary["ExData"], err); err != nil {
				return err
			}
			annotation.Dictionary = maps.Clone(annotation.Dictionary)
			delete(annotation.Dictionary, "ExData")
		}
	}
	clicks, err := p.annotationTriggerActions(ctx, annotation, "CLICK")
	if err != nil {
		return err
	}
	for _, key := range []pdfgo.Name{"OC", "A"} {
		if annotation.Subtype == "Widget" && key == "A" {
			continue
		}
		if key == "A" && (annotation.Subtype == "Link" || annotation.Subtype == "Movie") {
			continue
		}
		value, err := p.reader.Resolve(annotation.Dictionary[key])
		if value != nil || err != nil {
			if err == nil {
				err = &pdfgo.UnsupportedError{Feature: fmt.Sprintf("annotation field %q", key)}
			}
			if err := p.recoverPDFObject(ctx, "Annotation."+string(key), annotation.Dictionary[key], err); err != nil {
				return err
			}
		}
	}
	remark, err := p.reader.ReadAnnotationTextContext(ctx, annotation, "Contents", p.warning)
	if err != nil {
		return err
	}
	if annotation.Subtype == "FreeText" || annotation.Subtype == "Line" {
		annotation.Dictionary = maps.Clone(annotation.Dictionary)
		annotation.Dictionary["Contents"] = pdfgo.String("\xef\xbb\xbf" + remark)
	}
	flags, err := p.reader.ReadAnnotationFlags(annotation)
	if err != nil {
		return err
	}
	if flags&64 != 0 && annotation.Subtype != "Widget" {
		actions, clicks = nil, nil
	}
	invisible := flags&1 != 0 && !annotation.IsStandard()
	if flags&256 != 0 {
		if err := p.recoverPDFObject(ctx, "Annotation.F", annotation.Dictionary["F"], &pdfgo.UnsupportedError{Feature: "annotation ToggleNoView flag"}); err != nil {
			return err
		}
	}
	box := pdfBounds([]pdfgo.Point{
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMin, Y: annotation.Rect.YMin}),
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMax, Y: annotation.Rect.YMin}),
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMax, Y: annotation.Rect.YMax}),
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMin, Y: annotation.Rect.YMax}),
	})
	if len(clicks) != 0 && annotation.Subtype == "Link" {
		region, err := p.linkRegion(annotation, box)
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Annotation.QuadPoints", annotation.Dictionary["QuadPoints"], err); err != nil {
				return err
			}
		}
		for i := range clicks {
			clicks[i].Region = region
		}
	}
	actions = append(actions, clicks...)
	playback, err := p.annotationMovieBounds(actions, box, int64(flags))
	if err != nil {
		return err
	}
	source := box
	if playback != source {
		x, y := math.Min(source.X, playback.X), math.Min(source.Y, playback.Y)
		box = Box{X: x, Y: y, W: math.Max(source.X+source.W, playback.X+playback.W) - x, H: math.Max(source.Y+source.H, playback.Y+playback.H) - y}
	}
	skipAppearance := paint == nil && p.linkAppearanceEmpty(annotation)
	stamp := *p
	stamp.matrix = (pdfgo.Matrix{1, 0, 0, 1, -box.X, -box.Y}).Mul(p.matrix)
	stamp.objects = nil
	stamp.report = PDFImportReport{}
	stamp.resetWarnings()
	stamp.compositeCache = nil
	stamp.compositeNodes = p.compositeNodes[:len(p.compositeNodes):len(p.compositeNodes)]
	if !skipAppearance {
		stamp.clipTexts = make(map[*pdfgo.TextClip]pdfClipTextEntry)
	}
	stamp.pageWidth, stamp.pageHeight = box.W, box.H
	defer func() {
		if stamp.references != nil && stamp.references != p.references {
			if p.references == nil {
				p.references = stamp.references
			} else {
				stamp.references.Close()
			}
		}
		p.usedReferences = p.usedReferences || stamp.usedReferences
	}()
	paintFailed := false
	if paint != nil {
		if err := paint(&stamp); err != nil {
			if err := p.recoverAnnotationObject(ctx, "Annotation.Appearance", annotation.Dictionary, err); err != nil {
				return err
			}
			paintFailed = true
		}
	} else if !skipAppearance {
		nodes, err := p.collectCompositeNodes(func(visitor pdfgo.Visitor) error {
			return p.reader.WalkAnnotationAppearance(ctx, page, annotation, visitor)
		}, annotation.Subtype == "Widget")
		stamp.references = p.references
		stamp.halftoneWarnings = p.halftoneWarnings
		stamp.usedReferences = p.usedReferences
		if err == nil {
			err = stamp.compositeObjects(nodes)
		}
		if err != nil {
			if err := p.recoverAnnotationObject(ctx, "Annotation.Appearance", annotation.Dictionary, err); err != nil {
				return err
			}
			paintFailed = true
		}
	}
	if paintFailed {
		if len(actions) == 0 {
			return nil
		}
		stamp.objects = nil
	}
	objects, err := stamp.objects.data(ctx)
	if err != nil {
		return err
	}
	converted := Annotation{Type: pdfAnnotationType(annotation.Subtype), Subtype: string(annotation.Subtype), Remark: remark, Appearance: &Appearance{Boundary: pdfBoundary(box), Objects: objects}}
	if annotation.Subtype == "Projection" {
		projection, err := p.reader.ReadProjectionAnnotation(annotation)
		if err != nil {
			return err
		}
		converted.Parameters = &AnnotationParameters{Parameter: []AnnotationParameter{{Name: "PDF.Projection.Runtime", Value: "unavailable"}}}
		if projection.Measurement != nil {
			data, err := json.Marshal(projection.Measurement)
			if err != nil {
				return err
			}
			converted.Parameters.Parameter = append(converted.Parameters.Parameter, AnnotationParameter{Name: "PDF.Projection.Measurement", Value: string(data)})
		}
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF projection annotation metadata and static appearance retained; runtime rendering not transferred"})
	}
	if annotation.Subtype == "Redact" {
		converted.Parameters = &AnnotationParameters{Parameter: []AnnotationParameter{{Name: "PDF.Redact.Pending", Value: "true"}}}
	}
	if annotation.Subtype == "Text" {
		value, err := p.annotationOpen(ctx, annotation.Dictionary)
		if err != nil {
			return err
		}
		converted.Parameters = &AnnotationParameters{Parameter: []AnnotationParameter{{Name: "PDF.Text.Open", Value: strconv.FormatBool(value == pdfgo.Boolean(true))}}}
	}
	popupObject := annotation.Dictionary["Popup"]
	if annotation.Subtype == "Popup" {
		popupObject = annotation.Dictionary
	}
	if popupObject != nil {
		popup, err := p.popupAnnotation(ctx, popupObject)
		if err != nil {
			return err
		}
		if converted.Parameters == nil {
			converted.Parameters = &AnnotationParameters{}
		}
		converted.Parameters.Parameter = append(converted.Parameters.Parameter, []AnnotationParameter{
			{Name: "PDF.Popup.Open", Value: strconv.FormatBool(popup.Open)},
			{Name: "PDF.Popup.Rect", Value: fmt.Sprintf("%g %g %g %g", popup.Rect.XMin, popup.Rect.YMin, popup.Rect.XMax, popup.Rect.YMax)},
		}...)
	}
	if annotation.Subtype != "Popup" {
		if converted.Parameters == nil {
			converted.Parameters = &AnnotationParameters{}
		}
		converted.Parameters.Parameter = append(converted.Parameters.Parameter, AnnotationParameter{Name: "PDF.Printable", Value: strconv.FormatBool(flags&4 != 0 && flags&2 == 0 && !invisible)})
	}
	if len(actions) != 0 {
		region, err := NewShape(ShapeRectangle, Box{X: playback.X - box.X, Y: playback.Y - box.Y, W: playback.W, H: playback.H})
		if err != nil {
			return err
		}
		no := false
		if playback != source {
			region.CTM = pdfNumbers(1, 0, 0, 1, source.X-playback.X, source.Y-playback.Y)
			for i := range actions {
				if actions[i].Region == nil {
					actions[i].Region = &Region{Area: []RegionArea{{Start: "0 0", Command: []RegionCommand{
						{Type: "Line", Point1: pdfNumbers(source.W, 0)},
						{Type: "Line", Point1: pdfNumbers(source.W, source.H)},
						{Type: "Line", Point1: pdfNumbers(0, source.H)}, {Type: "Close"},
					}}}}
				}
			}
		}
		region.Fill, region.Stroke, region.Actions = &no, &no, actions
		converted.Appearance.Objects = append(converted.Appearance.Objects, GraphicObject{Type: "PathObject", PathObject: region})
	}
	if flags&(2|32) != 0 || invisible {
		visible := false
		converted.Visible = &visible
	}
	converted.NoZoom = flags&8 != 0 || annotation.Subtype == "Text"
	converted.NoRotate = flags&16 != 0 || annotation.Subtype == "Text"
	converted.Creator, err = p.reader.ReadAnnotationTextContext(ctx, annotation, "T", p.warning)
	if err != nil {
		return err
	}
	modified, err := p.reader.ReadAnnotationTextContext(ctx, annotation, "M", p.warning)
	if err != nil {
		return err
	}
	if modified != "" {
		if date, err := pdfgo.ParseDate(modified); err == nil && date.Year() > 0 {
			converted.LastModDate = date.Format("2006-01-02")
		}
		if converted.Parameters == nil {
			converted.Parameters = &AnnotationParameters{}
		}
		converted.Parameters.Parameter = append(converted.Parameters.Parameter, AnnotationParameter{Name: "PDF.M", Value: modified})
	}
	_, objectIDs, data, err := p.editor.prepareAnnotationWithIDs(p.page, converted)
	if err != nil {
		return fmt.Errorf("PDF annotation: %w", err)
	}
	var binding pdfStructureObjectBinding
	if p.structureBindings != nil {
		binding.content, err = stamp.objects.structureBindings(ctx)
		if err != nil {
			return err
		}
		if annotation.Reference != (pdfgo.Reference{}) && annotation.Dictionary["StructParent"] != nil {
			binding.source = pdfStructureObject{reader: p.reader, page: page.Reference, object: annotation.Reference}
		}
	}
	if binding.source.object != (pdfgo.Reference{}) || len(binding.content) != 0 {
		binding.ids = objectIDs
		p.annotationBindings = append(p.annotationBindings, binding)
	}
	p.annotationData = append(p.annotationData, bytes.TrimPrefix(data, []byte(xml.Header))...)
	if !paintFailed {
		p.mergeAnnotationStamp(&stamp)
		if flags&(2|32) == 0 && !invisible {
			p.compositeNodes = stamp.compositeNodes
			p.transferBackdrop = stamp.transferBackdrop
			p.compositeBackdropLoaded = stamp.compositeBackdropLoaded
			p.compositeBackdropNodes = stamp.compositeBackdropNodes
		} else if !p.compositeBackdropLoaded && stamp.compositeBackdropLoaded {
			p.compositeBackdropLoaded = true
			p.compositeBackdropNodes = stamp.compositeBackdropNodes
			if len(stamp.compositeBackdropNodes) != 0 {
				combined := make([]pdfCompositeNode, 0, len(stamp.compositeBackdropNodes)+len(p.compositeNodes))
				combined = append(combined, stamp.compositeBackdropNodes...)
				combined = append(combined, p.compositeNodes...)
				p.compositeNodes = combined
			}
		}
	}
	if len(actions) != 0 && annotation.Subtype == "Link" || len(clicks) != 0 {
		p.report.Links++
	}
	return nil
}

// commitAnnotations 提交注解后登记最终外观标识，失败时保留待提交状态
// 返回: error 注解提交错误
func (p *pdfImporter) commitAnnotations() error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if len(p.annotationData) == 0 {
		return nil
	}
	if err := p.editor.appendAnnotations(p.page, p.annotationData, true); err != nil {
		return err
	}
	for _, binding := range p.annotationBindings {
		pageID := p.editor.pages[p.page].ID
		if binding.source.object != (pdfgo.Reference{}) {
			if p.structureBindings.objects == nil {
				p.structureBindings.objects = make(map[pdfStructureObject][]StructureReference)
			}
			p.structureBindings.objects[binding.source] = append(p.structureBindings.objects[binding.source], StructureReference{PageID: pageID, ObjectIDs: binding.ids})
		}
		for _, content := range binding.content {
			ref := p.structureBindings.reference(pageID, content.sequence)
			ref.ObjectIDs = append(ref.ObjectIDs, binding.ids[content.start:content.end]...)
		}
	}
	p.annotationData, p.annotationBindings = nil, nil
	return nil
}

// formWidget 将表单当前外观转换为注解并报告交互语义差异
// 入参: ctx 取消上下文, page PDF页面, annotation 表单控件, strict 严格检查开关
// 返回: error 外观或转换错误
func (p *pdfImporter) formWidget(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if strict {
		return &pdfgo.UnsupportedError{Feature: "interactive PDF form conversion"}
	}
	flags, err := p.reader.ReadAnnotationFlags(annotation)
	if err != nil {
		return err
	}
	if flags&(2|32) != 0 {
		p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "hidden PDF form field not transferred"})
		return nil
	}
	if oc := annotation.Dictionary["OC"]; oc != nil {
		optional, err := p.reader.Resolve(oc)
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Annotation.OC", oc, err); err != nil {
				return err
			}
			annotation.Dictionary = maps.Clone(annotation.Dictionary)
			delete(annotation.Dictionary, "OC")
		} else if optional != nil {
			return &pdfgo.UnsupportedError{Feature: "widget viewing behavior"}
		}
	}
	if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
		return err
	}
	p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF form field appearance imported; interactive behavior not transferred"})
	return nil
}

// signatureWidget 转换可见签名外观，不将PDF签名误写成OFD签名
// 入参: ctx 取消上下文, page PDF页面, annotation 签名控件, strict 严格检查开关
// 返回: error 外观或转换错误
func (p *pdfImporter) signatureWidget(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if strict {
		return &pdfgo.UnsupportedError{Feature: "PDF digital signature conversion"}
	}
	flags, err := p.reader.ReadAnnotationFlags(annotation)
	if err != nil {
		return err
	}
	if flags&(2|32) != 0 {
		p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "hidden PDF signature not transferred"})
		return nil
	}
	if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
		return err
	}
	p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF signature appearance imported; digital signature not transferred"})
	return nil
}

// mergeAnnotationStamp 将注解局部值拷贝期间产生的统计、告警及惰性缓存状态回写到主导入器
// 入参: stamp 注解绘制使用的局部导入器
func (p *pdfImporter) mergeAnnotationStamp(stamp *pdfImporter) {
	p.report.Pages += stamp.report.Pages
	p.report.TextObjects += stamp.report.TextObjects
	p.report.PathObjects += stamp.report.PathObjects
	p.report.ImageObjects += stamp.report.ImageObjects
	p.report.Links += stamp.report.Links
	for _, entry := range stamp.warningEntries() {
		p.addWarningCount(entry.diagnostic, entry.count)
	}
	p.rasterWarned = p.rasterWarned || stamp.rasterWarned
	if p.cmykSpace == "" {
		p.cmykSpace = stamp.cmykSpace
	}
	if p.rgbSpace == "" {
		p.rgbSpace = stamp.rgbSpace
	}
	if p.fontPrograms == nil {
		p.fontPrograms = stamp.fontPrograms
	}
	if p.symbolFonts == nil {
		p.symbolFonts = stamp.symbolFonts
	}
	if p.fontWarnings == nil {
		p.fontWarnings = stamp.fontWarnings
	}
	if p.textOrigins == nil {
		p.textOrigins = stamp.textOrigins
	}
	if p.clipPaths == nil {
		p.clipPaths = stamp.clipPaths
	}
	if p.iccSpaces == nil {
		p.iccSpaces = stamp.iccSpaces
	}
	if p.formResources == nil {
		p.formResources = stamp.formResources
	}
	if p.validation == nil {
		p.validation = stamp.validation
	}
	if p.imageIDs == nil {
		p.imageIDs = stamp.imageIDs
	}
	if p.imageContents == nil {
		p.imageContents = stamp.imageContents
	}
	if p.attachmentIDs == nil {
		p.attachmentIDs = stamp.attachmentIDs
	}
	if p.modelAttachments == nil {
		p.modelAttachments = stamp.modelAttachments
	}
	if p.mediaResources == nil {
		p.mediaResources = stamp.mediaResources
	}
	if p.mediaInstances == nil {
		p.mediaInstances = stamp.mediaInstances
	}
	if p.movieBounds == nil {
		p.movieBounds = stamp.movieBounds
	}
	if p.renditions == nil {
		p.renditions = stamp.renditions
	}
	if p.maskClips == nil {
		p.maskClips = stamp.maskClips
	}
	if p.patternCells == nil {
		p.patternCells = stamp.patternCells
	}
	if p.patternOrigins == nil {
		p.patternOrigins = stamp.patternOrigins
	}
	if p.references == nil {
		p.references = stamp.references
	}
	p.usedReferences = p.usedReferences || stamp.usedReferences
	if p.halftoneWarnings == nil {
		p.halftoneWarnings = stamp.halftoneWarnings
	}
	if p.isolatedGroupImages == nil {
		p.isolatedGroupImages = stamp.isolatedGroupImages
	}
}
