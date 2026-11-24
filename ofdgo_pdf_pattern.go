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
	"fmt"
	"image"
	"math"
	"reflect"

	"github.com/xiaoqidun/pdfgo"
)

// pdfPatternCellLimit 限制单页图案矢量快照的估算字节数
const pdfPatternCellLimit = 8 << 20

// pdfPatternDenseFoldLimit 限制矢量重叠展开的最大单元副本数量
const pdfPatternDenseFoldLimit = 256

// pdfPatternOverPixel 使用标准预乘源覆盖混合单个像素
// 入参: dst 目标RGBA像素切片, src 源RGBA像素切片
func pdfPatternOverPixel(dst, src []byte) {
	sa := uint32(src[3])
	if sa == 0 {
		return
	}
	if sa == 255 {
		copy(dst[:4], src[:4])
		return
	}
	inv := 255 - sa
	dst[0] = uint8(min(255, uint32(src[0])+(uint32(dst[0])*inv+127)/255))
	dst[1] = uint8(min(255, uint32(src[1])+(uint32(dst[1])*inv+127)/255))
	dst[2] = uint8(min(255, uint32(src[2])+(uint32(dst[2])*inv+127)/255))
	dst[3] = uint8(min(255, sa+(uint32(dst[3])*inv+127)/255))
}

// pdfPatternAsRGBA 将任意图像转换为预乘RGBA画布以便周期折叠
// 入参: src 源图像
// 返回: *image.RGBA 预乘RGBA画布
func pdfPatternAsRGBA(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok && rgba.Rect.Min == (image.Point{}) {
		return rgba
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			r, g, b, a := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r>>8), uint8(g>>8), uint8(b>>8), uint8(a>>8)
		}
	}
	return dst
}

// pdfPatternCellKey 隔离图案来源、基色、坐标、精度、混合空间与目标文档资源
type pdfPatternCellKey struct {
	source                *pdfgo.TilingPattern
	base                  pdfgo.Paint
	matrix                pdfgo.Matrix
	dpi                   float64
	editor                *Editor
	space                 *pdfgo.ColorSpace
	pageWidth, pageHeight float64
}

// pdfPatternCell 保存图案快照及单元内部的来源区段
type pdfPatternCell struct {
	pattern *Pattern
	origin  pdfObjectOrigin
}

// pdfPatternInstanceRange 收缩已知路径及图片的实例范围，未知边界保留完整范围
// 入参: columns 横向实例数, rows 纵向实例数, bounds 对象边界, width 单元宽, height 单元高, xstep 横向步距, ystep 纵向步距
// 返回: int 左界, int 右界, int 上界, int 下界
func pdfPatternInstanceRange(columns, rows float64, bounds []Box, width, height, xstep, ystep float64) (int, int, int, int) {
	left, right, top, bottom := 0.0, 1-columns, 0.0, 1-rows
	for _, box := range bounds {
		if box.W <= 0 || box.H <= 0 || !finite(box.X) || !finite(box.Y) || !finite(box.X+box.W) || !finite(box.Y+box.H) {
			return 1 - int(columns), 0, 1 - int(rows), 0
		}
		left = math.Min(left, math.Max(1-columns, math.Ceil(-(box.X+box.W)/xstep)-1))
		right = math.Max(right, math.Min(0, math.Floor((width-box.X)/xstep)+1))
		top = math.Min(top, math.Max(1-rows, math.Ceil(-(box.Y+box.H)/ystep)-1))
		bottom = math.Max(bottom, math.Min(0, math.Floor((height-box.Y)/ystep)+1))
	}
	return int(left), int(right), int(top), int(bottom)
}

// pdfPatternCellCost 有界估算公开数据的快照成本，不扫描共享的私有排版来源
// 入参: value 对象值, budget 剩余预算, depth 嵌套深度
// 返回: int 字节成本，超预算时返回预算加一
func pdfPatternCellCost(value reflect.Value, budget, depth int) int {
	if depth > 128 || budget < 0 {
		return budget + 1
	}
	cost := int(value.Type().Size())
	if cost > budget {
		return budget + 1
	}
	switch value.Kind() {
	case reflect.String:
		if value.Len() > budget-cost {
			return budget + 1
		}
		cost += value.Len()
	case reflect.Pointer:
		if !value.IsNil() {
			cost += pdfPatternCellCost(value.Elem(), budget-cost, depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := range value.Len() {
			cost += pdfPatternCellCost(value.Index(i), budget-cost, depth+1)
			if cost > budget {
				return budget + 1
			}
		}
	case reflect.Struct:
		for i := range value.NumField() {
			if value.Type().Field(i).IsExported() {
				cost += pdfPatternCellCost(value.Field(i), budget-cost, depth+1)
				if cost > budget {
					return budget + 1
				}
			}
		}
	}
	return cost
}

// pdfPatternRectangleBounds 识别无文字裁剪的单个轴对齐矩形，不把边界框当作路径覆盖范围
// 入参: path 矩形路径
// 返回: pdfgo.Rectangle 精确覆盖范围, bool 是否单矩形
func pdfPatternRectangleBounds(path pdfgo.Path) (pdfgo.Rectangle, bool) {
	if len(path.Text) != 0 || len(path.Segments) != 5 || path.Segments[4].Operator != "C" || len(path.Segments[4].Points) != 0 {
		return pdfgo.Rectangle{}, false
	}
	var points [4]pdfgo.Point
	box := pdfgo.Rectangle{XMin: math.Inf(1), YMin: math.Inf(1), XMax: math.Inf(-1), YMax: math.Inf(-1)}
	for i, segment := range path.Segments[:4] {
		if len(segment.Points) != 1 || i == 0 && segment.Operator != "M" || i != 0 && segment.Operator != "L" {
			return pdfgo.Rectangle{}, false
		}
		point := segment.Points[0]
		if !finite(point.X) || !finite(point.Y) {
			return pdfgo.Rectangle{}, false
		}
		points[i] = point
		box.XMin, box.YMin = math.Min(box.XMin, point.X), math.Min(box.YMin, point.Y)
		box.XMax, box.YMax = math.Max(box.XMax, point.X), math.Max(box.YMax, point.Y)
	}
	if box.XMin == box.XMax || box.YMin == box.YMax {
		return pdfgo.Rectangle{}, false
	}
	corners := 0
	for i, point := range points {
		next := points[(i+1)%len(points)]
		if (point.X == next.X) == (point.Y == next.Y) || point.X != box.XMin && point.X != box.XMax || point.Y != box.YMin && point.Y != box.YMax {
			return pdfgo.Rectangle{}, false
		}
		corner := 0
		if point.X == box.XMax {
			corner++
		}
		if point.Y == box.YMax {
			corner += 2
		}
		corners |= 1 << corner
	}
	return box, corners == 15
}

// pdfPatternRectanglePath 构造精确闭合矩形，用于连续覆盖的绘制及裁剪
// 入参: box 矩形范围
// 返回: pdfgo.Path 闭合矩形
func pdfPatternRectanglePath(box pdfgo.Rectangle) pdfgo.Path {
	return pdfgo.Path{Segments: []pdfgo.Segment{{Operator: "M", Points: []pdfgo.Point{{X: box.XMin, Y: box.YMin}}}, {Operator: "L", Points: []pdfgo.Point{{X: box.XMax, Y: box.YMin}}}, {Operator: "L", Points: []pdfgo.Point{{X: box.XMax, Y: box.YMax}}}, {Operator: "L", Points: []pdfgo.Point{{X: box.XMin, Y: box.YMax}}}, {Operator: "C"}}}
}

// tilingPattern 有界复用矢量图案，返回可独立编辑的对象副本
// 入参: source 平铺图案, base 无色图案基色
// 返回: *Pattern OFD图案, error 转换或取消错误
func (p *pdfImporter) tilingPattern(source *pdfgo.TilingPattern, base pdfgo.Paint) (*Pattern, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if p.resolveFile != nil || p.resolveReference != nil {
		return p.buildTilingPattern(source, base)
	}
	if p.patternCells == nil {
		p.patternCells = resourceCache[pdfPatternCellKey, pdfPatternCell](p.resources, pdfPatternCellLimit)
	}
	key := pdfPatternCellKey{source: source, matrix: p.matrix, dpi: p.rasterDPI, editor: p.editor, space: p.compositeSpace, pageWidth: p.pageWidth, pageHeight: p.pageHeight}
	if source.PaintType == 2 {
		base.Tiling, base.Axial, base.Radial, base.Mesh, base.Function, base.Shading = nil, nil, nil, nil, nil, nil
		base.Alpha = 1
		key.base = base
	}
	if cached, ok := p.patternCells.get(key); ok {
		pattern := cloneEditorData(cached.pattern)
		if cached.origin.sequence != nil || len(cached.origin.related) != 0 {
			if p.patternOrigins == nil {
				p.patternOrigins = make(map[*Pattern]pdfObjectOrigin)
			}
			p.patternOrigins[pattern] = cached.origin
		}
		return pattern, p.ctx.Err()
	}
	pattern, err := p.buildTilingPattern(source, base)
	if err != nil {
		return nil, err
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	cost := pdfPatternCellCost(reflect.ValueOf(pattern), p.patternCells.limit, 0)
	cost += int(reflect.TypeFor[pdfPatternCell]().Size()) + len(p.patternOrigins[pattern].related)*int(reflect.TypeFor[*pdfgo.ContentSequence]().Size())
	if cost <= p.patternCells.limit {
		p.patternCells.put(key, pdfPatternCell{pattern: cloneEditorData(pattern), origin: p.patternOrigins[pattern]}, cost)
	}
	return pattern, p.ctx.Err()
}

// densePatternRectangle 对单一不透明矩形的连续重叠轴求精确并集，不修改源步距或近似采样
// 入参: source 原图案, base 基色, width 单元宽, height 单元高, xstep 横向步距, ystep 纵向步距
// 返回: *pdfgo.PathMark 并集矩形, bool 横轴已连续覆盖, bool 纵轴已连续覆盖, error 解析或取消错误
func (p *pdfImporter) densePatternRectangle(source *pdfgo.TilingPattern, base pdfgo.Paint, width, height, xstep, ystep float64) (*pdfgo.PathMark, bool, bool, error) {
	if width/xstep < 64 && height/ystep < 64 && !(width > xstep && height > ystep && math.Ceil(width/xstep)*math.Ceil(height/ystep) >= pdfPatternDenseFoldLimit) {
		return nil, false, false, nil
	}
	var mark pdfgo.PathMark
	paths, other := 0, false
	visitor := p.newBaseVisitor()
	visitor.Path = func(value pdfgo.PathMark) error {
		paths++
		if paths == 1 {
			mark = value
		}
		return nil
	}
	visitor.Text = func(pdfgo.TextMark) error { other = true; return nil }
	visitor.Image = func(pdfgo.ImageMark) error { other = true; return nil }
	visitor.Form = func(pdfgo.FormMark, func(pdfgo.Visitor) error) error { other = true; return nil }
	visitor.Group = func(pdfgo.GroupMark, func(pdfgo.Visitor) error) error { other = true; return nil }
	visitor.PostScript = func(pdfgo.PostScriptMark) error { other = true; return nil }
	if err := source.Walk(p.ctx, base, visitor); err != nil {
		return nil, false, false, err
	}
	if err := p.ctx.Err(); err != nil {
		return nil, false, false, err
	}
	style, paint := mark.Style, mark.Style.Fill
	if other || paths != 1 || !mark.Fill || mark.Stroke || paint.None || paint.Alpha != 1 || paint.Colorant != nil || paint.Process != nil || paint.Tiling != nil || paint.Shading != nil || paint.Axial != nil || paint.Radial != nil || paint.Mesh != nil || paint.Function != nil || style.SoftMask != nil || style.Transfer != nil || style.Halftone != nil || style.FillOverprint || style.AlphaIsShape || style.BlendMode != "" && style.BlendMode != "Normal" || len(style.Clips) != 1 {
		return nil, false, false, nil
	}
	clip, ok := pdfPatternRectangleBounds(style.Clips[0])
	if !ok || clip != source.BBox {
		return nil, false, false, nil
	}
	box, ok := pdfPatternRectangleBounds(mark.Path)
	if !ok {
		return nil, false, false, nil
	}
	box.XMin, box.YMin = math.Max(box.XMin, clip.XMin), math.Max(box.YMin, clip.YMin)
	box.XMax, box.YMax = math.Min(box.XMax, clip.XMax), math.Min(box.YMax, clip.YMax)
	if box.XMax <= box.XMin || box.YMax <= box.YMin {
		return nil, false, false, nil
	}
	continuousX := width > xstep && box.XMax-box.XMin >= xstep
	continuousY := height > ystep && box.YMax-box.YMin >= ystep
	if !continuousX && !continuousY {
		return nil, false, false, nil
	}
	if continuousX {
		box.XMin, box.XMax = clip.XMin, clip.XMax
	}
	if continuousY {
		box.YMin, box.YMax = clip.YMin, clip.YMax
	}
	mark.Path = pdfPatternRectanglePath(box)
	return &mark, continuousX, continuousY, nil
}

// foldDensePattern 对高密度或亚像素步距的非矩形平铺图案按单有效周期执行行列光栅折叠
// 入参: source 原图案, cell 单元导入器, nodes 单元图元, pattern 目标OFD图案, m 单元到页面矩阵, width 单元宽, height 单元高, xstep 横向步距, ystep 纵向步距, repeatWidth 横向重复宽, repeatHeight 纵向重复高
// 返回: bool 是否完成折叠, error 渲染或编码错误
func (p *pdfImporter) foldDensePattern(source *pdfgo.TilingPattern, cell pdfImporter, nodes []pdfCompositeNode, pattern *Pattern, m pdfgo.Matrix, width, height, xstep, ystep, repeatWidth, repeatHeight float64) (bool, error) {
	if p.warning == nil {
		return false, nil
	}
	const unit = 25.4 / 72
	dpi := p.rasterDPI
	if dpi <= 0 {
		dpi = 150
	}
	scaleX, scaleY := math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3])
	maxScale := math.Max(scaleX, scaleY)
	if !finite(maxScale) || maxScale <= 0 {
		maxScale = 1
	}
	renderDPI := dpi * maxScale
	const maxDim = 1024.0
	spanW, spanH := math.Max(width, xstep), math.Max(height, ystep)
	if spanW*unit*renderDPI/25.4 > maxDim {
		renderDPI = maxDim * 25.4 / (spanW * unit)
	}
	if spanH*unit*renderDPI/25.4 > maxDim {
		renderDPI = maxDim * 25.4 / (spanH * unit)
	}
	if !finite(renderDPI) || renderDPI <= 0 {
		renderDPI = 1
	}
	renderCell := cell
	renderCell.rasterDPI = renderDPI
	renderCell.objects = nil
	viewport := Box{W: spanW * unit, H: spanH * unit}
	rendered, _, err := renderCell.renderGroup(func(local *pdfImporter) error {
		visitor := local.visitor()
		for _, node := range nodes {
			if err := node.emit(visitor); err != nil {
				return err
			}
		}
		return nil
	}, &viewport)
	if err != nil {
		return false, err
	}
	if rendered == nil {
		pattern.CellContent.Objects = nil
		return true, nil
	}
	src := pdfPatternAsRGBA(rendered)
	srcW, srcH := src.Rect.Dx(), src.Rect.Dy()
	if srcW <= 0 || srcH <= 0 {
		pattern.CellContent.Objects = nil
		return true, nil
	}
	stepPxX := xstep * unit * renderDPI / 25.4
	stepPxY := ystep * unit * renderDPI / 25.4
	dx := max(1, min(srcW, int(math.Round(stepPxX))))
	dy := max(1, min(srcH, int(math.Round(stepPxY))))
	band := src
	if repeatHeight > ystep && dy < srcH {
		band = image.NewRGBA(image.Rect(0, 0, srcW, dy))
		rowCount := (srcH + dy - 1) / dy
		start, end, step := 0, rowCount-1, 1
		if source.YStep < 0 {
			start, end, step = rowCount-1, 0, -1
		}
		for k := start; k != end+step; k += step {
			if err := p.ctx.Err(); err != nil {
				return false, err
			}
			y0 := k * dy
			for y := 0; y < dy && y0+y < srcH; y++ {
				srcRow := src.Pix[src.PixOffset(0, y0+y):]
				dstRow := band.Pix[band.PixOffset(0, y):]
				for x := 0; x < srcW; x++ {
					off := x * 4
					pdfPatternOverPixel(dstRow[off:off+4], srcRow[off:off+4])
				}
			}
		}
	}
	folded := band
	if repeatWidth > xstep && dx < srcW {
		folded = image.NewRGBA(image.Rect(0, 0, dx, dy))
		colCount := (srcW + dx - 1) / dx
		start, end, step := colCount-1, 0, -1
		if source.XStep < 0 {
			start, end, step = 0, colCount-1, 1
		}
		for k := start; k != end+step; k += step {
			if err := p.ctx.Err(); err != nil {
				return false, err
			}
			x0 := k * dx
			for y := 0; y < dy; y++ {
				srcRow := band.Pix[band.PixOffset(0, y):]
				dstRow := folded.Pix[folded.PixOffset(0, y):]
				for x := 0; x < dx && x0+x < srcW; x++ {
					pdfPatternOverPixel(dstRow[x*4:x*4+4], srcRow[(x0+x)*4:(x0+x)*4+4])
				}
			}
		}
	} else if folded.Rect.Dx() != dx || folded.Rect.Dy() != dy {
		cropped := image.NewRGBA(image.Rect(0, 0, dx, dy))
		for y := 0; y < dy && y < folded.Rect.Dy(); y++ {
			copy(cropped.Pix[cropped.PixOffset(0, y):cropped.PixOffset(0, y)+dx*4], folded.Pix[folded.PixOffset(0, y):folded.PixOffset(0, y)+dx*4])
		}
		folded = cropped
	}
	var encoded bytes.Buffer
	if err := pdfgo.EncodePNG(p.ctx, &encoded, folded); err != nil {
		return false, err
	}
	id, err := p.editor.addDecodedImageContext(p.ctx, encoded.Bytes(), folded)
	if err != nil {
		return false, err
	}
	pageStepX := xstep * unit * scaleX * dpi / 25.4
	pageStepY := ystep * unit * scaleY * dpi / 25.4
	if dx == 1 && pageStepX < 1 && scaleX > 0 {
		periodW := math.Max(xstep*unit, 32*25.4/(dpi*scaleX))
		pattern.Width, pattern.XStep = periodW, periodW
	}
	if dy == 1 && pageStepY < 1 && scaleY > 0 {
		periodH := math.Max(ystep*unit, 32*25.4/(dpi*scaleY))
		pattern.Height, pattern.YStep = periodH, periodH
	}
	pattern.CellContent.Objects = []GraphicObject{{Type: "ImageObject", ImageObject: ImageObject{
		ResourceID: id,
		Boundary:   pdfBoundary(Box{W: pattern.Width, H: pattern.Height}),
		CTM:        pdfNumbers(pattern.Width, 0, 0, pattern.Height, 0, 0),
	}}}
	if !p.rasterWarned {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF effect locally composited at %g DPI; other objects retained", dpi)})
		p.rasterWarned = true
	}
	return true, nil
}

// densePatternPageBounds 将连续覆盖轴的等价单元边界置于页面外，避免子像素单元接缝
// 入参: source 原图案, box 原单元, x 横轴连续覆盖, y 纵轴连续覆盖
// 返回: pdfgo.Rectangle 等价单元边界, error 非有限范围或退化变换错误
func (p *pdfImporter) densePatternPageBounds(source *pdfgo.TilingPattern, box pdfgo.Rectangle, x, y bool) (pdfgo.Rectangle, error) {
	inverse, ok := p.matrix.Mul(source.Matrix).Inverse()
	if !ok {
		return box, fmt.Errorf("invalid PDF dense pattern matrix")
	}
	if !finite(p.pageWidth) || !finite(p.pageHeight) || p.pageWidth <= 0 || p.pageHeight <= 0 {
		return box, fmt.Errorf("invalid PDF dense pattern page bounds")
	}
	area := pdfgo.Rectangle{XMin: math.Inf(1), YMin: math.Inf(1), XMax: math.Inf(-1), YMax: math.Inf(-1)}
	for _, point := range []pdfgo.Point{{}, {X: p.pageWidth}, {Y: p.pageHeight}, {X: p.pageWidth, Y: p.pageHeight}} {
		point = inverse.Apply(point)
		area.XMin, area.YMin = math.Min(area.XMin, point.X), math.Min(area.YMin, point.Y)
		area.XMax, area.YMax = math.Max(area.XMax, point.X), math.Max(area.YMax, point.Y)
	}
	if x {
		span := box.XMax - box.XMin
		box.XMin, box.XMax = math.Min(box.XMin, area.XMin-span), math.Max(box.XMax, area.XMax+span)
	}
	if y {
		span := box.YMax - box.YMin
		box.YMin, box.YMax = math.Min(box.YMin, area.YMin-span), math.Max(box.YMax, area.YMax+span)
	}
	for _, value := range []float64{box.XMin, box.YMin, box.XMax, box.YMax, box.XMax - box.XMin, box.YMax - box.YMin} {
		if !finite(value) {
			return box, fmt.Errorf("nonfinite PDF dense pattern page bounds")
		}
	}
	return box, nil
}

// buildTilingPattern 保留PDF图案单元与页面坐标之间的仿射关系
// 入参: source 平铺图案, base 无色图案基色
// 返回: *Pattern OFD图案, error 转换错误
func (p *pdfImporter) buildTilingPattern(source *pdfgo.TilingPattern, base pdfgo.Paint) (*Pattern, error) {
	const unit = 25.4 / 72
	box := source.BBox
	width, height := box.XMax-box.XMin, box.YMax-box.YMin
	xstep, ystep := math.Abs(source.XStep), math.Abs(source.YStep)
	if (width == 0 || height == 0) && finite(width) && finite(height) && width >= 0 && height >= 0 && finite(xstep) && finite(ystep) && xstep > 0 && ystep > 0 {
		return &Pattern{Width: unit, Height: unit, XStep: unit, YStep: unit, RelativeTo: "Page"}, nil
	}
	if width <= 0 || height <= 0 || !finite(xstep) || !finite(ystep) || xstep == 0 || ystep == 0 {
		return nil, fmt.Errorf("invalid PDF tiling pattern dimensions")
	}
	dense, continuousX, continuousY, err := p.densePatternRectangle(source, base, width, height, xstep, ystep)
	if err != nil {
		return nil, err
	}
	if dense != nil {
		box, err = p.densePatternPageBounds(source, box, continuousX, continuousY)
		if err != nil {
			return nil, err
		}
		bounds, _ := pdfPatternRectangleBounds(dense.Path)
		if continuousX {
			bounds.XMin, bounds.XMax = box.XMin, box.XMax
		}
		if continuousY {
			bounds.YMin, bounds.YMax = box.YMin, box.YMax
		}
		dense.Path, dense.Style.Clips = pdfPatternRectanglePath(bounds), []pdfgo.Path{pdfPatternRectanglePath(box)}
		width, height = box.XMax-box.XMin, box.YMax-box.YMin
	}
	localToPDF := pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, box.XMin, box.YMax}
	m := p.matrix.Mul(source.Matrix).Mul(localToPDF)
	pattern := &Pattern{Width: math.Min(width, xstep) * unit, Height: math.Min(height, ystep) * unit, XStep: xstep * unit, YStep: ystep * unit, RelativeTo: "Page", CTM: pdfNumbers(m[:]...)}
	repeatWidth, repeatHeight := width, height
	if continuousX {
		pattern.Width, pattern.XStep, repeatWidth = width*unit, width*unit, xstep
	}
	if continuousY {
		pattern.Height, pattern.YStep, repeatHeight = height*unit, height*unit, ystep
	}
	if p.patternOrigins == nil {
		p.patternOrigins = make(map[*Pattern]pdfObjectOrigin)
	}
	cell := *p
	cell.matrix = pdfgo.Matrix{unit, 0, 0, -unit, -box.XMin * unit, box.YMax * unit}
	cell.pageWidth, cell.pageHeight = width*unit, height*unit
	cell.rasterDPI *= m.MaxScale()
	if !finite(cell.rasterDPI) || cell.rasterDPI < 0 {
		return nil, fmt.Errorf("invalid PDF tiling pattern raster scale")
	}
	maxDPI := max(p.rasterDPI*16, 2400)
	if dim := max(cell.pageWidth, cell.pageHeight); dim > 0 {
		if dimLimit := 4096 * 25.4 / dim; finite(dimLimit) && dimLimit > 0 {
			maxDPI = min(maxDPI, max(p.rasterDPI, dimLimit))
		}
	}
	if cell.rasterDPI > maxDPI {
		cell.rasterDPI = maxDPI
	}
	cell.objects = nil
	nodes, err := cell.collectCompositeNodes(func(visitor pdfgo.Visitor) error {
		if dense != nil {
			return visitor.Path(*dense)
		}
		return source.Walk(p.ctx, base, visitor)
	}, false)
	if err != nil {
		return nil, err
	}
	visitor := cell.visitor()
	for _, node := range nodes {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		if err := node.emit(visitor); err != nil {
			return nil, err
		}
	}
	objects, err := cell.objects.data(p.ctx)
	if err != nil {
		return nil, err
	}
	pattern.CellContent.Objects = objects
	if len(objects) != 0 && (repeatWidth > xstep || repeatHeight > ystep) {
		columns, rows := math.Ceil(repeatWidth/xstep), math.Ceil(repeatHeight/ystep)
		folded := false
		if !finite(columns*rows) || columns*rows > pdfPatternDenseFoldLimit {
			if p.warning != nil {
				folded, err = p.foldDensePattern(source, cell, nodes, pattern, m, width, height, xstep, ystep, repeatWidth, repeatHeight)
				if err != nil {
					return nil, err
				}
			}
			if !folded && (!finite(columns*rows) || columns*rows >= float64(uint(^uint(0)>>1)+1)) {
				return nil, fmt.Errorf("PDF tiling pattern instance count overflow")
			}
		}
		if !folded {
			bounds := make([]Box, len(objects))
			for index, object := range objects {
				boundary := ""
				if object.Type == "PathObject" && object.PathObject.Stroke != nil && !*object.PathObject.Stroke {
					boundary = object.PathObject.Boundary
				} else if object.Type == "ImageObject" && object.ImageObject.Border == nil {
					boundary = object.ImageObject.Boundary
				}
				if boundary != "" {
					var err error
					bounds[index], err = ParseBox(boundary)
					if err != nil {
						return nil, err
					}
				}
			}
			left, right, top, bottom := pdfPatternInstanceRange(columns, rows, bounds, pattern.Width, pattern.Height, xstep*unit, ystep*unit)
			if p.warning != nil && float64(right-left+1)*float64(bottom-top+1) > pdfPatternDenseFoldLimit {
				folded, err = p.foldDensePattern(source, cell, nodes, pattern, m, width, height, xstep, ystep, repeatWidth, repeatHeight)
				if err != nil {
					return nil, err
				}
			}
			if !folded {
				var expanded pdfObjectBuffer
				rowStart, rowEnd, rowStep := bottom, top, -1
				if source.YStep < 0 {
					rowStart, rowEnd, rowStep = top, bottom, 1
				}
				colStart, colEnd, colStep := left, right, 1
				if source.XStep < 0 {
					colStart, colEnd, colStep = right, left, -1
				}
				for row := rowStart; row != rowEnd+rowStep; row += rowStep {
					for column := colStart; column != colEnd+colStep; column += colStep {
						if err := p.ctx.Err(); err != nil {
							return nil, err
						}
						dx, dy := float64(column)*xstep*unit, float64(row)*ystep*unit
						for index, object := range objects {
							if index%pdfObjectChunkLimit == 0 {
								if err := p.ctx.Err(); err != nil {
									return nil, err
								}
							}
							b := bounds[index]
							if b.W > 0 && b.H > 0 && (b.X+dx+b.W < 0 || b.Y+dy+b.H < 0 || b.X+dx > pattern.Width || b.Y+dy > pattern.Height) {
								continue
							}
							translated, err := transformEditorObject(object, dx, dy, 1)
							if err != nil {
								return nil, err
							}
							expanded.appendOrigin(translated, cell.objects.origin(index))
						}
					}
				}
				pattern.CellContent.Objects, err = expanded.data(p.ctx)
				if err != nil {
					return nil, err
				}
				cell.objects = &expanded
			}
		}
	} else if len(objects) != 0 && !continuousX && !continuousY {
		dpi := p.rasterDPI
		if dpi <= 0 {
			dpi = 150
		}
		stepPxX := pattern.XStep * math.Hypot(m[0], m[1]) * dpi / 25.4
		stepPxY := pattern.YStep * math.Hypot(m[2], m[3]) * dpi / 25.4
		if stepPxX < 0.5 || stepPxY < 0.5 {
			if _, err := p.foldDensePattern(source, cell, nodes, pattern, m, width, height, xstep, ystep, repeatWidth, repeatHeight); err != nil {
				return nil, err
			}
		}
	}
	origin, err := cell.objects.combinedOrigin(p.ctx)
	if err != nil {
		return nil, err
	}
	if origin.sequence != nil || len(origin.related) != 0 {
		p.patternOrigins[pattern] = origin
	}
	return pattern, nil
}
