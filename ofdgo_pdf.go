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
	"reflect"
	"strconv"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// PDFImportOptions 指定PDF转换的运行时后端、进度通知与源文件检查策略
// Strict禁止恢复缺失的图形状态资源和无目标链接，默认恢复并在报告中记录警告
type PDFImportOptions struct {
	Backends *RenderBackends
	Progress func(int) error
	Strict   bool
}

// PDFImportReport 汇总转换页数、对象数、链接数和源文件恢复警告
type PDFImportReport struct {
	Pages        int
	TextObjects  int
	PathObjects  int
	ImageObjects int
	Links        int
	Warnings     []pdfgo.Diagnostic
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
	pages       map[pdfgo.Reference]*pdfgo.Page
	pageIDs     map[pdfgo.Reference]string
	imageIDs    map[*pdfgo.Stream]string
	maskClips   map[*pdfgo.SoftMask]pdfgo.Path
	pageBox     pdfgo.Rectangle
	pendingPath *pdfgo.PathMark
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
	importer := pdfImporter{reader: reader, editor: editor, fontIDs: map[*pdfgo.Font]string{}, fontMetrics: map[string]FontMetrics{}, pages: map[pdfgo.Reference]*pdfgo.Page{}, pageIDs: map[pdfgo.Reference]string{}}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		_, width, height := pdfPageMatrix(page)
		if _, err := editor.AddPage(width, height); err != nil {
			return err
		}
		importer.pages[page.Reference] = page
		importer.pageIDs[page.Reference] = editor.pages[index].ID
		return nil
	})
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		matrix, _, _ := pdfPageMatrix(page)
		importer.matrix = matrix
		importer.page = index
		importer.pageBox = page.CropBox
		importer.maskClips = make(map[*pdfgo.SoftMask]pdfgo.Path)
		visitor := pdfgo.Visitor{Path: importer.path, Text: importer.text, Image: importer.image}
		visitor.Group = func(mark pdfgo.GroupMark, walk func(pdfgo.Visitor) error) error {
			return importer.group(mark, walk, visitor)
		}
		if !options.Strict {
			visitor.Warning = func(warning pdfgo.Diagnostic) {
				warning.Page = index + 1
				importer.report.Warnings = append(importer.report.Warnings, warning)
			}
		}
		if err := reader.WalkPage(ctx, page, visitor); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if err := importer.flushPath(); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if err := importer.annotations(ctx, page, options.Strict); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if err := importer.commitObjects(); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
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

// commitObjects 将当前页面待转换对象写入编辑文档
// 返回: error 对象写入错误
func (p *pdfImporter) commitObjects() error {
	if len(p.objects) == 0 {
		return nil
	}
	if _, err := p.editor.CopyObjects(p.page, p.objects, 0, 0); err != nil {
		return err
	}
	p.objects = nil
	return nil
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
// 入参: page PDF页面
// 返回: pdfgo.Matrix 坐标变换, float64 页面宽度, float64 页面高度
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
// 入参: values 数值列表
// 返回: string 空格分隔的数值
func pdfNumbers(values ...float64) string {
	parts := make([]string, len(values))
	for n, v := range values {
		parts[n] = strconv.FormatFloat(v, 'g', -1, 64)
	}
	return strings.Join(parts, " ")
}

// pdfBoundary 序列化对象边界
// 入参: b 对象边界
// 返回: string OFD边界数据
func pdfBoundary(b Box) string { return pdfNumbers(b.X, b.Y, b.W, b.H) }

// pdfBounds 计算点集合的包围框
// 入参: points 坐标点集合
// 返回: Box 包围框
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
// 入参: paint PDF颜色与不透明度
// 返回: *FillColor OFD颜色
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
// 入参: path PDF路径, origin 对象边界
// 返回: string OFD路径数据
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
// 入参: paths 裁剪路径, origin 对象边界
// 返回: *Clips OFD裁剪区域，无裁剪时为空
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

// path 合并相同状态下的不透明描边，保留独立子路径与绘制顺序
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) path(mark pdfgo.PathMark) error {
	if mark.Style.BlendMode == "Multiply" {
		return &pdfgo.UnsupportedError{Feature: "multiply path"}
	}
	if !mark.Stroke || mark.Fill || mark.Style.Stroke.Alpha != 1 {
		if err := p.flushPath(); err != nil {
			return err
		}
		return p.appendPath(mark)
	}
	if p.pendingPath != nil && !reflect.DeepEqual(p.pendingPath.Style, mark.Style) {
		if err := p.flushPath(); err != nil {
			return err
		}
	}
	if p.pendingPath == nil {
		p.pendingPath = &mark
	} else {
		p.pendingPath.Path.Segments = append(p.pendingPath.Path.Segments, mark.Path.Segments...)
	}
	if len(p.pendingPath.Path.Segments) >= 512 {
		return p.flushPath()
	}
	return nil
}

// flushPath 写入连续描边批次，保持后续图元的叠放位置
// 返回: error 转换错误
func (p *pdfImporter) flushPath() error {
	if p.pendingPath == nil {
		return nil
	}
	mark := *p.pendingPath
	p.pendingPath = nil
	return p.appendPath(mark)
}

// appendPath 添加独立可编辑路径
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) appendPath(mark pdfgo.PathMark) error {
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		return err
	}
	if mark.Fill && mark.Style.FillOverprint && !pdfOpaqueBlack(mark.Style.Fill) || mark.Stroke && mark.Style.StrokeOverprint && !pdfOpaqueBlack(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "color separation overprint"}
	}
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
	strokeColor := StrokeColor(*p.pathColor(mark.Style.Stroke, box))
	object := PathObject{Boundary: pdfBoundary(box), AbbreviatedData: p.pathData(mark.Path, box), Fill: &mark.Fill, Stroke: &mark.Stroke, FillColor: p.pathColor(mark.Style.Fill, box), StrokeColor: &strokeColor, LineWidth: mark.Style.LineWidth * scale, Cap: []string{"Butt", "Round", "Square"}[mark.Style.Cap], Join: []string{"Miter", "Round", "Bevel"}[mark.Style.Join], MiterLimit: mark.Style.MiterLimit, Clips: p.clips(mark.Style.Clips, box)}
	object.LineWidthSet = object.LineWidth == 0
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

// pathColor 将页面渐变轴转换为对象局部坐标
// 入参: paint PDF画刷, box 对象边界
// 返回: *FillColor OFD颜色或渐变
func (p *pdfImporter) pathColor(paint pdfgo.Paint, box Box) *FillColor {
	color := p.color(paint)
	if paint.Axial == nil {
		return color
	}
	gradient := paint.Axial
	start, end := p.matrix.Apply(gradient.Start), p.matrix.Apply(gradient.End)
	extend := 0
	if gradient.Extend[0] {
		extend |= 1
	}
	if gradient.Extend[1] {
		extend |= 2
	}
	shading := &AxialShd{StartPoint: pdfNumbers(start.X-box.X, start.Y-box.Y), EndPoint: pdfNumbers(end.X-box.X, end.Y-box.Y), Extend: strconv.Itoa(extend)}
	for _, stop := range gradient.Stops {
		shading.Segment = append(shading.Segment, ShdSegment{Position: stop.Position, Color: ShdColor{Value: pdfNumbers(math.Round(stop.RGB[0]*255), math.Round(stop.RGB[1]*255), math.Round(stop.RGB[2]*255))}})
	}
	color.Value = ""
	color.AxialShd = shading
	return color
}

// pdfOpaqueBlack 判断不受底层分色影响的全黑设备色
// 入参: paint PDF颜色与不透明度
// 返回: bool 是否为不透明全黑CMYK颜色
func pdfOpaqueBlack(paint pdfgo.Paint) bool {
	return paint.Alpha == 1 && paint.CMYK != nil && paint.CMYK[3] == 1
}
