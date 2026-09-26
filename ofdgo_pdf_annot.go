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
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/xiaoqidun/pdfgo"
)

// stampAnnotation 将PDF印章外观转换为OFD注解
// 入参: ctx取消上下文, page PDF页面, annotation PDF印章
// 返回: error 外观或转换错误
func (p *pdfImporter) stampAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation) error {
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
	var darkenImages []pdfgo.ImageMark
	visitor := pdfgo.Visitor{
		Path: func(mark pdfgo.PathMark) error {
			if mark.Style.BlendMode == "Darken" {
				return &pdfgo.UnsupportedError{Feature: "darken stamp path"}
			}
			return stamp.path(mark)
		},
		Text: func(mark pdfgo.TextMark) error {
			if mark.Style.BlendMode == "Darken" {
				return &pdfgo.UnsupportedError{Feature: "darken stamp text"}
			}
			if err := stamp.text(mark); err != nil {
				return fmt.Errorf("stamp text: %w", err)
			}
			return nil
		},
		Image: func(mark pdfgo.ImageMark) error {
			if mark.Style.BlendMode == "Darken" {
				darkenImages = append(darkenImages, mark)
				return nil
			}
			return stamp.image(mark)
		},
	}
	visitor.Group = func(group pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
		return stamp.group(group, walk, visitor)
	}
	if err := p.reader.WalkAnnotationAppearance(ctx, page, annotation, visitor); err != nil {
		return err
	}
	if err := stamp.flushPath(); err != nil {
		return err
	}
	if len(darkenImages) != 0 {
		if len(darkenImages) != 1 || len(stamp.objects) != 0 {
			return &pdfgo.UnsupportedError{Feature: "mixed darken stamp appearance"}
		}
		if err := p.darkenStampAppearance(page, box, darkenImages[0], &stamp); err != nil {
			return err
		}
		p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF darken stamp appearance flattened over page content"})
	}
	converted := Annotation{Type: "Stamp", Appearance: Appearance{Boundary: pdfBoundary(box), Objects: stamp.objects}}
	if flags&(1|2|32) != 0 {
		visible := false
		converted.Visible = &visible
	}
	converted.NoZoom = flags&8 != 0
	converted.NoRotate = flags&16 != 0
	if value := annotation.Dictionary["T"]; value != nil {
		resolved, err := p.reader.Resolve(value)
		if err != nil {
			return err
		}
		creator, ok := resolved.(pdfgo.String)
		if !ok {
			return fmt.Errorf("invalid PDF stamp creator")
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
			return fmt.Errorf("invalid PDF stamp modification date")
		}
		date, err := pdfgo.DecodeTextString(encoded)
		if err != nil {
			return err
		}
		date = strings.TrimPrefix(date, "D:")
		if len(date) < 4 {
			return fmt.Errorf("invalid PDF stamp modification date")
		}
		year, err := strconv.Atoi(date[:4])
		if err != nil {
			return fmt.Errorf("invalid PDF stamp modification date: %w", err)
		}
		month, day := 1, 1
		if len(date) >= 6 {
			month, err = strconv.Atoi(date[4:6])
			if err != nil {
				return fmt.Errorf("invalid PDF stamp modification month: %w", err)
			}
		}
		if len(date) >= 8 {
			day, err = strconv.Atoi(date[6:8])
			if err != nil {
				return fmt.Errorf("invalid PDF stamp modification day: %w", err)
			}
		}
		parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		if parsed.Year() != year || int(parsed.Month()) != month || parsed.Day() != day {
			return fmt.Errorf("invalid PDF stamp modification date")
		}
		converted.LastModDate = parsed.Format("2006-01-02")
		if len(date) > 8 {
			p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF stamp modification time cannot fit OFD annotation date"})
		}
	}
	if _, err := p.editor.AddAnnotation(p.page, converted); err != nil {
		return fmt.Errorf("stamp annotation: %w", err)
	}
	p.report.TextObjects += stamp.report.TextObjects
	p.report.PathObjects += stamp.report.PathObjects
	p.report.ImageObjects += stamp.report.ImageObjects
	if flags&64 != 0 {
		p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF stamp read-only flag not transferred"})
	}
	return nil
}

// darkenStampAppearance 按PDF的Darken公式合成印章范围
// 入参: page PDF页面, box印章范围, mark图像, stamp局部转换器
// 返回: error 转换错误
func (p *pdfImporter) darkenStampAppearance(page *pdfgo.Page, box Box, mark pdfgo.ImageMark, stamp *pdfImporter) error {
	if err := p.commitObjects(); err != nil {
		return err
	}
	base, err := renderImportedPage(p.editor, p.page, false)
	if err != nil {
		return err
	}
	_, width, height := pdfPageMatrix(page)
	appearance := NewEditor()
	appearance.SetRenderBackends(p.editor.backends)
	if _, err := appearance.AddPage(width, height); err != nil {
		return err
	}
	imageImporter := pdfImporter{reader: p.reader, editor: appearance, matrix: p.matrix, pageBox: page.CropBox}
	mark.Style.BlendMode = "Normal"
	if err := imageImporter.image(mark); err != nil {
		return err
	}
	if _, err := appearance.CopyObjects(0, imageImporter.objects, 0, 0); err != nil {
		return err
	}
	overlay, err := renderImportedPage(appearance, 0, true)
	if err != nil {
		return err
	}
	if base.Bounds() != overlay.Bounds() {
		return fmt.Errorf("stamp appearance page size mismatch")
	}
	const dpi = 300.0
	pixelsPerMM := dpi / 25.4
	region := image.Rect(int(math.Floor(box.X*pixelsPerMM)), int(math.Floor(box.Y*pixelsPerMM)), int(math.Ceil((box.X+box.W)*pixelsPerMM)), int(math.Ceil((box.Y+box.H)*pixelsPerMM))).Intersect(base.Bounds())
	if region.Empty() {
		return fmt.Errorf("stamp appearance outside page")
	}
	composite := image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			background := color.NRGBAModel.Convert(base.At(x, y)).(color.NRGBA)
			foreground := color.NRGBAModel.Convert(overlay.At(x, y)).(color.NRGBA)
			alpha := float64(foreground.A) / 255
			blend := func(back, front uint8) uint8 {
				return uint8(math.Round((1-alpha)*float64(back) + alpha*math.Min(float64(back), float64(front))))
			}
			composite.SetRGBA(x-region.Min.X, y-region.Min.Y, color.RGBA{R: blend(background.R, foreground.R), G: blend(background.G, foreground.G), B: blend(background.B, foreground.B), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, composite); err != nil {
		return err
	}
	id, err := p.editor.AddImage(encoded.Bytes())
	if err != nil {
		return err
	}
	regionBox := Box{X: float64(region.Min.X)/pixelsPerMM - box.X, Y: float64(region.Min.Y)/pixelsPerMM - box.Y, W: float64(region.Dx()) / pixelsPerMM, H: float64(region.Dy()) / pixelsPerMM}
	stamp.objects = append(stamp.objects, GraphicObject{Type: "ImageObject", ImageObject: ImageObject{Boundary: pdfBoundary(regionBox), ResourceID: id}})
	stamp.report.ImageObjects++
	return nil
}

// formWidget 将表单当前外观转换为页面对象并报告交互语义差异
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
	if flags&(8|16) != 0 || annotation.Dictionary["OC"] != nil {
		return &pdfgo.UnsupportedError{Feature: "widget viewing behavior"}
	}
	visitor := pdfgo.Visitor{Path: p.path, Text: p.text, Image: p.image, Warning: p.warning}
	visitor.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error { return p.group(mark, walk, visitor) }
	if err := p.reader.WalkAnnotationAppearance(ctx, page, annotation, visitor); err != nil {
		return err
	}
	if err := p.flushPath(); err != nil {
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
	var images []pdfgo.ImageMark
	visitor := pdfgo.Visitor{
		Path: func(pdfgo.PathMark) error { return &pdfgo.UnsupportedError{Feature: "signature appearance path"} },
		Text: func(pdfgo.TextMark) error { return &pdfgo.UnsupportedError{Feature: "signature appearance text"} },
		Image: func(mark pdfgo.ImageMark) error {
			images = append(images, mark)
			return nil
		},
	}
	if err := p.reader.WalkAnnotationAppearance(ctx, page, annotation, visitor); err != nil {
		return err
	}
	if len(images) != 1 {
		return &pdfgo.UnsupportedError{Feature: "signature appearance composition"}
	}
	mark := images[0]
	message := "PDF signature appearance imported; digital signature not transferred"
	if mark.Style.BlendMode == "Multiply" {
		if err := p.multiplySignatureAppearance(page, annotation, mark); err != nil {
			return err
		}
		message = "PDF signature appearance flattened over page content; digital signature not transferred"
	} else if mark.Style.BlendMode == "" || mark.Style.BlendMode == "Normal" || mark.Style.BlendMode == "Compatible" {
		if err := p.image(mark); err != nil {
			return err
		}
	} else {
		return &pdfgo.UnsupportedError{Feature: "signature appearance blend mode"}
	}
	p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: message})
	return nil
}

// multiplySignatureAppearance 合成签名覆盖区域，正文及其他页面对象保持独立
// 入参: page PDF页面, annotation 签名控件, mark 印章图像
// 返回: error 图像或渲染错误
func (p *pdfImporter) multiplySignatureAppearance(page *pdfgo.Page, annotation pdfgo.Annotation, mark pdfgo.ImageMark) error {
	if err := p.commitObjects(); err != nil {
		return err
	}
	base, err := renderImportedPage(p.editor, p.page, false)
	if err != nil {
		return err
	}
	_, width, height := pdfPageMatrix(page)
	appearance := NewEditor()
	appearance.SetRenderBackends(p.editor.backends)
	if _, err := appearance.AddPage(width, height); err != nil {
		return err
	}
	stamp := pdfImporter{reader: p.reader, editor: appearance, matrix: p.matrix, pageBox: page.CropBox}
	mark.Style.BlendMode = "Normal"
	if err := stamp.image(mark); err != nil {
		return err
	}
	if _, err := appearance.CopyObjects(0, stamp.objects, 0, 0); err != nil {
		return err
	}
	overlay, err := renderImportedPage(appearance, 0, false)
	if err != nil {
		return err
	}
	if base.Bounds() != overlay.Bounds() {
		return fmt.Errorf("signature appearance page size mismatch")
	}
	annotationBox := annotation.Rect
	box := pdfBounds([]pdfgo.Point{
		p.matrix.Apply(pdfgo.Point{X: annotationBox.XMin, Y: annotationBox.YMin}),
		p.matrix.Apply(pdfgo.Point{X: annotationBox.XMax, Y: annotationBox.YMin}),
		p.matrix.Apply(pdfgo.Point{X: annotationBox.XMax, Y: annotationBox.YMax}),
		p.matrix.Apply(pdfgo.Point{X: annotationBox.XMin, Y: annotationBox.YMax}),
	})
	const dpi = 300.0
	pixelsPerMM := dpi / 25.4
	region := image.Rect(int(math.Floor(box.X*pixelsPerMM)), int(math.Floor(box.Y*pixelsPerMM)), int(math.Ceil((box.X+box.W)*pixelsPerMM)), int(math.Ceil((box.Y+box.H)*pixelsPerMM))).Intersect(base.Bounds())
	if region.Empty() {
		return fmt.Errorf("signature appearance outside page")
	}
	composite := image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			background := color.RGBAModel.Convert(base.At(x, y)).(color.RGBA)
			foreground := color.RGBAModel.Convert(overlay.At(x, y)).(color.RGBA)
			composite.SetRGBA(x-region.Min.X, y-region.Min.Y, color.RGBA{
				R: uint8((int(background.R)*int(foreground.R) + 127) / 255),
				G: uint8((int(background.G)*int(foreground.G) + 127) / 255),
				B: uint8((int(background.B)*int(foreground.B) + 127) / 255),
				A: 255,
			})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, composite); err != nil {
		return err
	}
	id, err := p.editor.AddImage(encoded.Bytes())
	if err != nil {
		return err
	}
	regionBox := Box{X: float64(region.Min.X) / pixelsPerMM, Y: float64(region.Min.Y) / pixelsPerMM, W: float64(region.Dx()) / pixelsPerMM, H: float64(region.Dy()) / pixelsPerMM}
	p.objects = append(p.objects, GraphicObject{Type: "ImageObject", ImageObject: ImageObject{Boundary: pdfBoundary(regionBox), ResourceID: id}})
	p.report.ImageObjects++
	return nil
}

// renderImportedPage 在固定印刷分辨率绘制待合成页面
// 入参: editor 编辑文档, page 页序号, transparent 是否保留透明背景
// 返回: image.Image 页面像素, error 渲染错误
func renderImportedPage(editor *Editor, page int, transparent bool) (image.Image, error) {
	reader, err := editor.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := reader.PageContentByIndex(page)
	if err != nil {
		return nil, err
	}
	renderer := NewRenderer(reader, WithDPI(300), WithRenderBackends(editor.backends), WithAnnotations(!transparent))
	renderer.TransparentBackground = transparent
	return renderer.RenderToImage(content)
}
