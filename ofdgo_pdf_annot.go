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
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xiaoqidun/pdfgo"
)

// appearanceAnnotation 将PDF静态外观转换为对应类型的OFD注解
// 入参: ctx取消上下文, page PDF页面, annotation PDF注解, actions 外观区域的动作
// 返回: error 外观或转换错误
func (p *pdfImporter) appearanceAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, actions ...Action) error {
	for _, key := range []pdfgo.Name{"AA", "OC", "A"} {
		if annotation.Subtype == "Widget" && (key == "A" || key == "AA") {
			continue
		}
		if key == "A" && (annotation.Subtype == "Link" || annotation.Subtype == "Movie") {
			continue
		}
		if annotation.Dictionary[key] != nil {
			return &pdfgo.UnsupportedError{Feature: fmt.Sprintf("annotation field %q", key)}
		}
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
	stamp.clipTexts = make(map[*pdfgo.TextClip]TextObject)
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
	if flags&(2|32) == 0 {
		p.compositeNodes = stamp.compositeNodes
	}
	converted := Annotation{Type: pdfAnnotationType(annotation.Subtype), Subtype: string(annotation.Subtype), Appearance: Appearance{Boundary: pdfBoundary(box), Objects: stamp.objects}}
	if annotation.Subtype == "Text" {
		value, err := p.reader.Resolve(annotation.Dictionary["Open"])
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
	if value, err := p.reader.Resolve(annotation.Dictionary["Contents"]); err != nil {
		return err
	} else if value != nil {
		contents, ok := value.(pdfgo.String)
		if !ok {
			return fmt.Errorf("invalid PDF annotation contents")
		}
		converted.Remark, err = pdfgo.DecodeTextString(contents)
		if err != nil {
			return err
		}
	}
	if flags&(2|32) != 0 {
		visible := false
		converted.Visible = &visible
	}
	converted.NoZoom = flags&8 != 0 || annotation.Subtype == "Text"
	converted.NoRotate = flags&16 != 0 || annotation.Subtype == "Text"
	if value := annotation.Dictionary["T"]; value != nil {
		resolved, err := p.reader.Resolve(value)
		if err != nil {
			return err
		}
		creator, ok := resolved.(pdfgo.String)
		if !ok {
			return fmt.Errorf("invalid PDF annotation creator")
		}
		converted.Creator, err = pdfgo.DecodeTextString(creator)
		if err != nil {
			return err
		}
	}
	if value := annotation.Dictionary["M"]; value != nil {
		resolved, err := p.reader.Resolve(value)
		if err != nil {
			return err
		}
		encoded, ok := resolved.(pdfgo.String)
		if !ok {
			return fmt.Errorf("invalid PDF annotation modification date")
		}
		date, err := pdfgo.DecodeTextString(encoded)
		if err != nil {
			return err
		}
		date = strings.TrimPrefix(date, "D:")
		if len(date) < 4 {
			return fmt.Errorf("invalid PDF annotation modification date")
		}
		year, err := strconv.Atoi(date[:4])
		if err != nil {
			return fmt.Errorf("invalid PDF annotation modification date: %w", err)
		}
		month, day := 1, 1
		if len(date) >= 6 {
			month, err = strconv.Atoi(date[4:6])
			if err != nil {
				return fmt.Errorf("invalid PDF annotation modification month: %w", err)
			}
		}
		if len(date) >= 8 {
			day, err = strconv.Atoi(date[6:8])
			if err != nil {
				return fmt.Errorf("invalid PDF annotation modification day: %w", err)
			}
		}
		parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		if parsed.Year() != year || int(parsed.Month()) != month || parsed.Day() != day {
			return fmt.Errorf("invalid PDF annotation modification date")
		}
		converted.LastModDate = parsed.Format("2006-01-02")
		if len(date) > 8 {
			p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF annotation modification time cannot fit OFD annotation date"})
		}
	}
	if _, err := p.editor.AddAnnotation(p.page, converted); err != nil {
		return fmt.Errorf("PDF annotation: %w", err)
	}
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
	case "Text", "FreeText", "Line", "Square", "Circle", "Polygon", "PolyLine", "Caret", "Ink", "FileAttachment", "Sound", "Movie", "Screen", "Link", "Popup", "Widget", "PrinterMark", "TrapNet":
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
