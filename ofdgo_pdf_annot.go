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
	"encoding/xml"
	"fmt"
	"maps"
	"strconv"

	"github.com/xiaoqidun/pdfgo"
)

// appearanceAnnotation 将PDF静态外观转换为对应类型的OFD注解
// 入参: ctx 取消上下文, page PDF页面, annotation PDF注解, actions 外观区域的动作
// 返回: error 外观或转换错误
func (p *pdfImporter) appearanceAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, actions ...Action) error {
	for _, key := range []pdfgo.Name{"AA", "OC", "A"} {
		if annotation.Subtype == "Widget" && (key == "A" || key == "AA") {
			continue
		}
		if key == "A" && (annotation.Subtype == "Link" || annotation.Subtype == "Movie") {
			continue
		}
		value, err := p.reader.Resolve(annotation.Dictionary[key])
		if err != nil {
			return err
		}
		if value != nil {
			return &pdfgo.UnsupportedError{Feature: fmt.Sprintf("annotation field %q", key)}
		}
	}
	remark, err := p.reader.ReadAnnotationText(annotation, "Contents", p.warning)
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
	invisible := flags&1 != 0 && !annotation.IsStandard()
	if flags&256 != 0 {
		return &pdfgo.UnsupportedError{Feature: "annotation ToggleNoView flag"}
	}
	box := pdfBounds([]pdfgo.Point{
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMin, Y: annotation.Rect.YMin}),
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMax, Y: annotation.Rect.YMin}),
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMax, Y: annotation.Rect.YMax}),
		p.matrix.Apply(pdfgo.Point{X: annotation.Rect.XMin, Y: annotation.Rect.YMax}),
	})
	stamp := *p
	stamp.matrix = (pdfgo.Matrix{1, 0, 0, 1, -box.X, -box.Y}).Mul(p.matrix)
	stamp.objects = nil
	stamp.pendingPath = nil
	stamp.report = PDFImportReport{}
	stamp.compositeCache = nil
	stamp.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
	stamp.pageWidth, stamp.pageHeight = box.W, box.H
	nodes, err := p.collectCompositeNodes(func(visitor pdfgo.Visitor) error {
		if annotation.Subtype == "Widget" {
			visitor.MarkedContent = func(mark pdfgo.MarkedContentMark) error {
				if mark.Tag == "Tx" && len(mark.Properties) == 0 {
					return nil
				}
				if mark.Operator == "EMC" {
					return nil
				}
				if p.warning == nil {
					return &pdfgo.UnsupportedError{Feature: "widget marked content " + string(mark.Tag)}
				}
				p.warning(pdfgo.Diagnostic{Message: "PDF widget marked content " + string(mark.Tag) + " not preserved"})
				return nil
			}
		}
		return p.reader.WalkAnnotationAppearance(ctx, page, annotation, visitor)
	})
	if err != nil {
		return err
	}
	if err := stamp.compositeObjects(nodes); err != nil {
		return err
	}
	if flags&(2|32) == 0 && !invisible {
		p.compositeNodes = stamp.compositeNodes
		p.transferBackdrop = stamp.transferBackdrop
	}
	objects, err := stamp.objects.data(ctx)
	if err != nil {
		return err
	}
	converted := Annotation{Type: pdfAnnotationType(annotation.Subtype), Subtype: string(annotation.Subtype), Remark: remark, Appearance: Appearance{Boundary: pdfBoundary(box), Objects: objects}}
	if annotation.Subtype == "Redact" {
		converted.Parameters = &AnnotationParameters{Parameter: []AnnotationParameter{{Name: "PDF.Redact.Pending", Value: "true"}}}
	}
	if annotation.Subtype == "Text" {
		value, err := p.reader.Resolve(annotation.Dictionary["Open"])
		if err != nil {
			return err
		}
		converted.Parameters = &AnnotationParameters{Parameter: []AnnotationParameter{{Name: "PDF.Text.Open", Value: strconv.FormatBool(value == pdfgo.Boolean(true))}}}
	}
	popupObject, err := p.reader.Resolve(annotation.Dictionary["Popup"])
	if err != nil {
		return err
	}
	if annotation.Subtype == "Popup" {
		popupObject = annotation.Dictionary
	}
	if popupObject != nil {
		popup, err := p.reader.ReadPopupAnnotation(popupObject)
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
	if len(actions) != 0 {
		region, err := NewShape(ShapeRectangle, Box{W: box.W, H: box.H})
		if err != nil {
			return err
		}
		no := false
		region.Fill, region.Stroke, region.Actions = &no, &no, actions
		converted.Appearance.Objects = append(converted.Appearance.Objects, GraphicObject{Type: "PathObject", PathObject: region})
	}
	if flags&(2|32) != 0 || invisible {
		visible := false
		converted.Visible = &visible
	}
	converted.NoZoom = flags&8 != 0 || annotation.Subtype == "Text"
	converted.NoRotate = flags&16 != 0 || annotation.Subtype == "Text"
	converted.Creator, err = p.reader.ReadAnnotationText(annotation, "T", p.warning)
	if err != nil {
		return err
	}
	modified, err := p.reader.ReadAnnotationText(annotation, "M", p.warning)
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
	_, data, err := p.editor.prepareAnnotation(p.page, converted)
	if err != nil {
		return fmt.Errorf("PDF annotation: %w", err)
	}
	p.annotationData = append(p.annotationData, bytes.TrimPrefix(data, []byte(xml.Header))...)
	p.report.TextObjects += stamp.report.TextObjects
	p.report.PathObjects += stamp.report.PathObjects
	p.report.ImageObjects += stamp.report.ImageObjects
	p.report.Warnings = append(p.report.Warnings, stamp.report.Warnings...)
	if flags&64 != 0 {
		p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF annotation read-only flag not transferred"})
	}
	return nil
}

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
	case "Text", "FreeText", "Line", "Square", "Circle", "Polygon", "PolyLine", "Caret", "Ink", "FileAttachment", "Sound", "Movie", "Screen", "Link", "Popup", "Widget", "PrinterMark", "TrapNet", "3D", "RichMedia", "Redact":
		return "Path"
	}
	if !(pdfgo.Annotation{Subtype: subtype}).IsStandard() {
		return "Path"
	}
	return ""
}

// formWidget 将表单当前外观转换为注解并报告交互语义差异
// 入参: ctx 取消上下文, page PDF页面, annotation 表单控件, strict 严格检查开关
// 返回: error 外观或转换错误
func (p *pdfImporter) formWidget(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if strict {
		return &pdfgo.UnsupportedError{Feature: "interactive PDF form conversion"}
	}
	value, err := p.reader.Resolve(annotation.Dictionary["F"])
	if err != nil {
		return err
	}
	flags, ok := value.(pdfgo.Integer)
	if value != nil && !ok {
		return fmt.Errorf("invalid PDF widget flags")
	}
	if flags&(1|2|32) != 0 {
		p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "hidden PDF form field not transferred"})
		return nil
	}
	if annotation.Dictionary["OC"] != nil {
		return &pdfgo.UnsupportedError{Feature: "widget viewing behavior"}
	}
	if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
		return err
	}
	p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF form field appearance imported; interactive behavior not transferred"})
	return nil
}

// signatureWidget 转换可见签名外观，不将PDF签名误写成OFD签名
// 入参: ctx 取消上下文, page PDF页面, annotation 签名控件
// 返回: error 外观或转换错误
func (p *pdfImporter) signatureWidget(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation) error {
	if annotation.Dictionary["FT"] != pdfgo.Name("Sig") || annotation.Dictionary["V"] == nil {
		return &pdfgo.UnsupportedError{Feature: "non-signature widget"}
	}
	flags := int64(0)
	if value := annotation.Dictionary["F"]; value != nil {
		resolved, err := p.reader.Resolve(value)
		if err != nil {
			return err
		}
		number, ok := resolved.(pdfgo.Integer)
		if !ok {
			return fmt.Errorf("invalid PDF annotation flags")
		}
		flags = int64(number)
	}
	const invisibleFlags = 1 | 2 | 32
	if flags&invisibleFlags != 0 {
		p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "hidden PDF signature not transferred"})
		return nil
	}
	if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
		return err
	}
	p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF signature appearance imported; digital signature not transferred"})
	return nil
}
