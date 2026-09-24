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

	"github.com/xiaoqidun/pdfgo"
)

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
	base, err := renderImportedPage(p.editor, p.page)
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
	overlay, err := renderImportedPage(appearance, 0)
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
// 入参: editor 编辑文档, page 页序号
// 返回: image.Image 页面像素, error 渲染错误
func renderImportedPage(editor *Editor, page int) (image.Image, error) {
	reader, err := editor.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := reader.PageContentByIndex(page)
	if err != nil {
		return nil, err
	}
	return NewRenderer(reader, WithDPI(300), WithRenderBackends(editor.backends)).RenderToImage(content)
}
