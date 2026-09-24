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
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// PDFImportOptions 指定PDF转换的运行时后端与进度通知
type PDFImportOptions struct {
	Backends *RenderBackends
	Progress func(int) error
}

// PDFImportReport 汇总成功转换的页面和可编辑对象
type PDFImportReport struct {
	Pages        int
	TextObjects  int
	PathObjects  int
	ImageObjects int
}

// pdfImporter 保存单次转换的对象与资源状态
type pdfImporter struct {
	reader      *pdfgo.Reader
	editor      *Editor
	report      PDFImportReport
	matrix      pdfgo.Matrix
	page        int
	fontIDs     map[*pdfgo.Font]string
	fontMetrics map[string]FontMetrics
	cmykSpace   string
	objects     []GraphicObject
}

// ImportPDF 将PDF内容转换为独立OFD编辑文档，失败时不返回部分结果
// 入参: ctx 取消上下文, source PDF数据, size 字节数, options 转换选项
// 返回: *Editor 编辑文档, PDFImportReport 转换统计, error 错误信息
func ImportPDF(ctx context.Context, source io.ReaderAt, size int64, options PDFImportOptions) (*Editor, PDFImportReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, PDFImportReport{}, err
	}
	reader, err := pdfgo.NewReader(source, size)
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	editor := NewEditor()
	if options.Backends != nil {
		editor.SetRenderBackends(*options.Backends)
	}
	importer := pdfImporter{reader: reader, editor: editor, fontIDs: map[*pdfgo.Font]string{}, fontMetrics: map[string]FontMetrics{}}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		matrix, width, height := pdfPageMatrix(page)
		importer.matrix = matrix
		importer.page, err = editor.AddPage(width, height)
		if err != nil {
			return err
		}
		if err := reader.WalkPage(ctx, page, pdfgo.Visitor{Path: importer.path, Text: importer.text, Image: importer.image}); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if _, err := editor.CopyObjects(importer.page, importer.objects, 0, 0); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		importer.objects = nil
		importer.report.Pages++
		if options.Progress != nil {
			return options.Progress(index + 1)
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	if importer.report.Pages == 0 {
		return nil, PDFImportReport{}, fmt.Errorf("PDF contains no pages")
	}
	return editor, importer.report, nil
}

// ConvertPDF 将PDF转换并写入OFD，转换阶段失败时不写入目标
// 入参: ctx 取消上下文, source PDF数据, size 字节数, output OFD输出, options 转换选项
// 返回: PDFImportReport 转换统计, error 错误信息
func ConvertPDF(ctx context.Context, source io.ReaderAt, size int64, output io.Writer, options PDFImportOptions) (PDFImportReport, error) {
	editor, report, err := ImportPDF(ctx, source, size, options)
	if err != nil {
		return PDFImportReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return PDFImportReport{}, err
	}
	_, err = editor.WriteTo(output)
	return report, err
}

// pdfPageMatrix 将裁剪框、用户单位及页面旋转转换为OFD毫米坐标
func pdfPageMatrix(page *pdfgo.Page) (pdfgo.Matrix, float64, float64) {
	b := page.CropBox
	scale := 25.4 / 72 * page.UserUnit
	width, height := (b.XMax-b.XMin)*scale, (b.YMax-b.YMin)*scale
	m := pdfgo.Matrix{scale, 0, 0, -scale, -b.XMin * scale, b.YMax * scale}
	switch page.Rotation {
	case 90:
		m = pdfgo.Matrix{0, 1, -1, 0, height, 0}.Mul(m)
		width, height = height, width
	case 180:
		m = pdfgo.Matrix{-1, 0, 0, -1, width, height}.Mul(m)
	case 270:
		m = pdfgo.Matrix{0, -1, 1, 0, 0, width}.Mul(m)
		width, height = height, width
	}
	return m, width, height
}

// pdfNumbers 按OFD数值格式序列化坐标
func pdfNumbers(values ...float64) string {
	parts := make([]string, len(values))
	for n, v := range values {
		parts[n] = strconv.FormatFloat(v, 'g', -1, 64)
	}
	return strings.Join(parts, " ")
}

// pdfBoundary 序列化对象边界
func pdfBoundary(b Box) string { return pdfNumbers(b.X, b.Y, b.W, b.H) }

// pdfBounds 计算点集合的包围框
func pdfBounds(points []pdfgo.Point) Box {
	if len(points) == 0 {
		return Box{}
	}
	minX, minY, maxX, maxY := points[0].X, points[0].Y, points[0].X, points[0].Y
	for _, p := range points {
		minX = math.Min(minX, p.X)
		minY = math.Min(minY, p.Y)
		maxX = math.Max(maxX, p.X)
		maxY = math.Max(maxY, p.Y)
	}
	return Box{minX, minY, math.Max(maxX-minX, 1e-6), math.Max(maxY-minY, 1e-6)}
}

// color 保留设备色彩分量并复用文档颜色空间
func (p *pdfImporter) color(paint pdfgo.Paint) *FillColor {
	alpha := int(math.Round(paint.Alpha * 255))
	if paint.CMYK != nil {
		if p.cmykSpace == "" {
			p.cmykSpace = p.editor.nextID()
			p.editor.resources = append(p.editor.resources, editorResource{space: &ColorSpace{ID: p.cmykSpace, Type: "CMYK", BitsPerComponent: 16}})
		}
		v := paint.CMYK
		return &FillColor{ColorSpace: p.cmykSpace, Value: pdfNumbers(math.Round(v[0]*65535), math.Round(v[1]*65535), math.Round(v[2]*65535), math.Round(v[3]*65535)), Alpha: &alpha}
	}
	return &FillColor{Value: pdfNumbers(math.Round(paint.RGB[0]*255), math.Round(paint.RGB[1]*255), math.Round(paint.RGB[2]*255)), Alpha: &alpha}
}

// pathData 将PDF路径转换为相对对象边界的OFD路径
func (p *pdfImporter) pathData(path pdfgo.Path, origin Box) string {
	var parts []string
	for _, segment := range path.Segments {
		parts = append(parts, segment.Operator)
		for _, point := range segment.Points {
			point = p.matrix.Apply(point)
			parts = append(parts, pdfNumbers(point.X-origin.X, point.Y-origin.Y))
		}
	}
	return strings.Join(parts, " ")
}

// clips 保持裁剪路径的交集，不随对象CTM重复变换
func (p *pdfImporter) clips(paths []pdfgo.Path, origin Box) *Clips {
	if len(paths) == 0 {
		return nil
	}
	flag := false
	clips := &Clips{TransFlag: &flag}
	for _, path := range paths {
		var points []pdfgo.Point
		for _, segment := range path.Segments {
			for _, point := range segment.Points {
				points = append(points, p.matrix.Apply(point))
			}
		}
		box := pdfBounds(points)
		rule := "NonZero"
		if path.EvenOdd {
			rule = "Even-Odd"
		}
		clips.Clip = append(clips.Clip, Clip{Area: []ClipArea{{Path: []PathObject{{Boundary: pdfBoundary(Box{box.X - origin.X, box.Y - origin.Y, box.W, box.H}), AbbreviatedData: p.pathData(path, box), Rule: rule}}}}})
	}
	return clips
}

// path 添加独立可编辑路径
func (p *pdfImporter) path(mark pdfgo.PathMark) error {
	var points []pdfgo.Point
	for _, segment := range mark.Path.Segments {
		for _, point := range segment.Points {
			points = append(points, p.matrix.Apply(point))
		}
	}
	if len(points) == 0 {
		return fmt.Errorf("empty PDF painted path")
	}
	box := pdfBounds(points)
	scale := math.Hypot(p.matrix[0], p.matrix[1])
	if mark.Stroke {
		margin := mark.Style.LineWidth * scale / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{box.X - margin, box.Y - margin, box.W + 2*margin, box.H + 2*margin}
	}
	strokeColor := StrokeColor(*p.color(mark.Style.Stroke))
	object := PathObject{Boundary: pdfBoundary(box), AbbreviatedData: p.pathData(mark.Path, box), Fill: &mark.Fill, Stroke: &mark.Stroke, FillColor: p.color(mark.Style.Fill), StrokeColor: &strokeColor, LineWidth: mark.Style.LineWidth * scale, Cap: []string{"Butt", "Round", "Square"}[mark.Style.Cap], Join: []string{"Miter", "Round", "Bevel"}[mark.Style.Join], MiterLimit: mark.Style.MiterLimit, Clips: p.clips(mark.Style.Clips, box)}
	if mark.Path.EvenOdd {
		object.Rule = "Even-Odd"
	}
	if len(mark.Style.Dash) > 0 {
		values := make([]float64, len(mark.Style.Dash))
		for n, v := range mark.Style.Dash {
			values[n] = v * scale
		}
		object.DashPattern = pdfNumbers(values...)
		phase := mark.Style.DashPhase * scale
		object.DashOffset = &phase
	}
	p.objects = append(p.objects, GraphicObject{Type: "PathObject", PathObject: object})
	p.report.PathObjects++
	return nil
}
