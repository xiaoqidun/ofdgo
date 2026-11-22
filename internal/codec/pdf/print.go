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

package pdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"io"
	"maps"
	"strconv"

	"github.com/xiaoqidun/pdfgo"
)

// PrintPlacement 保存源PDF页索引及毫米纸面上的变换和裁剪
type PrintPlacement struct {
	Page   int
	Matrix [6]float64
	Clip   [4]float64
}

// PrintSheet 保存一个纸面的物理尺寸、放置内容和辅助线
type PrintSheet struct {
	Width, Height  float64
	Placements     []PrintPlacement
	Lines          [][4]float64
	Label          string
	LabelX, LabelY float64
}

// printStreamCopier 将源PDF实际引用的对象流式复制到目标StreamWriter，避免全量驻留内存
type printStreamCopier struct {
	ctx    context.Context
	source *pdfgo.Reader
	writer *StreamWriter
	copied map[pdfgo.Reference]pdfgo.Reference
}

// WritePrintPDF 从随机读取源组装打印纸面，保持矢量、文字、资源及透明组
// 入参: ctx 输出上下文, writer 输出流, source 源PDF, size 源数据长度, sheets 有序纸面
// 返回: error 资源、编码、写入或取消错误
func WritePrintPDF(ctx context.Context, writer io.Writer, source io.ReaderAt, size int64, sheets []PrintSheet) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var reader *pdfgo.Reader
	if size > 0 {
		var err error
		reader, err = pdfgo.NewReader(source, size)
		if err != nil {
			return err
		}
		defer reader.Close()
	}
	var pages []*pdfgo.Page
	if reader != nil {
		if err := reader.WalkPages(ctx, func(_ int, page *pdfgo.Page) error { pages = append(pages, page); return nil }); err != nil {
			return err
		}
	}
	sw, err := newStreamWriter(ctx, writer, len(sheets), true, zlib.DefaultCompression, true)
	if err != nil {
		return err
	}
	copier := &printStreamCopier{
		ctx:    ctx,
		source: reader,
		writer: sw,
		copied: make(map[pdfgo.Reference]pdfgo.Reference),
	}
	forms := make(map[int]pdfgo.Reference)
	var content bytes.Buffer
	var formData bytes.Buffer
	for i, sheet := range sheets {
		if err := ctx.Err(); err != nil {
			return err
		}
		sheetW, sheetH := sheet.Width*PtPerMM, sheet.Height*PtPerMM
		if !finite(sheet.Width) || !finite(sheet.Height) || sheet.Width <= 0 || sheet.Height <= 0 || !finite(sheetW) || !finite(sheetH) || sheetW <= 0 || sheetH <= 0 {
			return fmt.Errorf("invalid print PDF number")
		}
		content.Reset()
		objects := make(pdfgo.Dictionary)
		for _, placement := range sheet.Placements {
			if placement.Page < 0 || placement.Page >= len(pages) {
				return fmt.Errorf("print PDF page outside source")
			}
			for _, v := range placement.Clip {
				if !finite(v) {
					return fmt.Errorf("invalid print PDF number")
				}
			}
			for _, v := range placement.Matrix {
				if !finite(v) {
					return fmt.Errorf("invalid print PDF number")
				}
			}
			page := pages[placement.Page]
			if page.Rotation != 0 || page.UserUnit != 1 {
				return fmt.Errorf("print PDF backend must normalize page rotation and units")
			}
			form, ok := forms[placement.Page]
			if !ok {
				var resources pdfgo.Object = pdfgo.Dictionary{}
				if len(page.Resources) > 0 {
					var err error
					resources, err = copier.copy(page.Resources, 0)
					if err != nil {
						return err
					}
					if resources == nil {
						resources = pdfgo.Dictionary{}
					}
				}
				formData.Reset()
				if _, err := page.WriteContent(ctx, &formData); err != nil {
					return err
				}
				box := page.MediaBox
				if !finite(box.XMin) || !finite(box.YMin) || !finite(box.XMax) || !finite(box.YMax) {
					return fmt.Errorf("invalid print PDF number")
				}
				dict := pdfgo.Dictionary{
					"Type":      pdfgo.Name("XObject"),
					"Subtype":   pdfgo.Name("Form"),
					"FormType":  pdfgo.Integer(1),
					"BBox":      pdfgo.Array{pdfgo.Real(box.XMin), pdfgo.Real(box.YMin), pdfgo.Real(box.XMax), pdfgo.Real(box.YMax)},
					"Resources": resources,
				}
				if group := page.Dictionary["Group"]; group != nil {
					copied, err := copier.copy(group, 0)
					if err != nil {
						return err
					}
					if copied != nil {
						dict["Group"] = copied
					}
				}
				form = sw.AllocRef()
				if err := sw.WriteStream(form, dict, formData.Bytes(), true); err != nil {
					return err
				}
				forms[placement.Page] = form
			}
			name := pdfgo.Name("P" + strconv.Itoa(placement.Page))
			objects[name] = form
			c, m := placement.Clip, placement.Matrix
			hs := (page.MediaBox.YMax - page.MediaBox.YMin) / PtPerMM
			x0, y0 := page.MediaBox.XMin, page.MediaBox.YMin
			content.WriteString("q ")
			if err := printPDFNumbers(&content, c[0]*PtPerMM, (sheet.Height-c[1]-c[3])*PtPerMM, c[2]*PtPerMM, c[3]*PtPerMM); err != nil {
				return err
			}
			content.WriteString("re W n ")
			if err := printPDFNumbers(&content, m[0], -m[1], -m[2], m[3], (m[2]*hs+m[4])*PtPerMM-m[0]*x0+m[2]*y0, (sheet.Height-m[3]*hs-m[5])*PtPerMM+m[1]*x0-m[3]*y0); err != nil {
				return err
			}
			content.WriteString("cm ")
			appendPDFName(&content, string(name))
			content.WriteString(" Do Q\n")
		}
		for _, line := range sheet.Lines {
			content.WriteString("q 0 G ")
			if err := printPDFNumbers(&content, 0.15*PtPerMM); err != nil {
				return err
			}
			content.WriteString("w ")
			if err := printPDFNumbers(&content, line[0]*PtPerMM, (sheet.Height-line[1])*PtPerMM); err != nil {
				return err
			}
			content.WriteString("m ")
			if err := printPDFNumbers(&content, line[2]*PtPerMM, (sheet.Height-line[3])*PtPerMM); err != nil {
				return err
			}
			content.WriteString("l S Q\n")
		}
		resources := pdfgo.Dictionary{"XObject": objects}
		if sheet.Label != "" {
			content.WriteString("q 0 g BT /Label 7.94 Tf 1 0 0 1 ")
			if err := printPDFNumbers(&content, sheet.LabelX*PtPerMM, (sheet.Height-sheet.LabelY)*PtPerMM); err != nil {
				return err
			}
			content.WriteString("Tm ")
			appendPDFLiteralString(&content, []byte(sheet.Label))
			content.WriteString(" Tj ET Q\n")
			resources["Font"] = pdfgo.Dictionary{"Label": pdfgo.Dictionary{"Type": pdfgo.Name("Font"), "Subtype": pdfgo.Name("Type1"), "BaseFont": pdfgo.Name("Helvetica")}}
		}
		streamRef := sw.AllocRef()
		if err := sw.WriteStream(streamRef, pdfgo.Dictionary{}, content.Bytes(), true); err != nil {
			return err
		}
		pageDict := pdfgo.Dictionary{
			"Type":      pdfgo.Name("Page"),
			"Parent":    sw.PageParent(i),
			"MediaBox":  pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Real(sheetW), pdfgo.Real(sheetH)},
			"Resources": resources,
			"Contents":  streamRef,
		}
		if err := sw.WriteObject(sw.PageRef(i), pageDict); err != nil {
			return err
		}
	}
	return sw.Finish(nil, nil)
}

// WritePrintImages 逐纸面流式压缩并立刻写出光栅结果，消除多页打印时的整本内存峰值
// 入参: ctx 输出上下文, writer 输出流, sheets 有序纸面, render 按索引渲染纸面
// 返回: error 渲染、编码、写入或取消错误
func WritePrintImages(ctx context.Context, writer io.Writer, sheets []PrintSheet, render func(int) (image.Image, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sw, err := newStreamWriter(ctx, writer, len(sheets), true, zlib.DefaultCompression, true)
	if err != nil {
		return err
	}
	var imgBuf bytes.Buffer
	var content bytes.Buffer
	var zw *zlib.Writer
	var row []byte
	for i, sheet := range sheets {
		if err := ctx.Err(); err != nil {
			return err
		}
		sheetW, sheetH := sheet.Width*PtPerMM, sheet.Height*PtPerMM
		if !finite(sheet.Width) || !finite(sheet.Height) || sheet.Width <= 0 || sheet.Height <= 0 || !finite(sheetW) || !finite(sheetH) || sheetW <= 0 || sheetH <= 0 {
			return fmt.Errorf("invalid print PDF number")
		}
		img, err := render(i)
		if err != nil {
			return err
		}
		if img == nil {
			return fmt.Errorf("print PDF image is nil")
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		if _, err := checkedSampleBytes(w, h, 3); err != nil {
			return err
		}
		rowBytes := w * 3
		if cap(row) < rowBytes {
			row = make([]byte, rowBytes)
		} else {
			row = row[:rowBytes]
		}
		imgBuf.Reset()
		if zw == nil {
			zw = zlib.NewWriter(&imgBuf)
		} else {
			zw.Reset(&imgBuf)
		}
		if rgba, ok := img.(*image.RGBA); ok {
			for y := b.Min.Y; y < b.Max.Y; y++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				srcRow := rgba.Pix[rgba.PixOffset(b.Min.X, y):]
				for x := range w {
					si, di := x*4, x*3
					a := srcRow[si+3]
					if a == 255 {
						row[di], row[di+1], row[di+2] = srcRow[si], srcRow[si+1], srcRow[si+2]
					} else {
						inv := uint16(255 - a)
						row[di] = byte(min(255, uint16(srcRow[si])+inv))
						row[di+1] = byte(min(255, uint16(srcRow[si+1])+inv))
						row[di+2] = byte(min(255, uint16(srcRow[si+2])+inv))
					}
				}
				if _, err := zw.Write(row); err != nil {
					return err
				}
			}
		} else {
			for y := b.Min.Y; y < b.Max.Y; y++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				for x := b.Min.X; x < b.Max.X; x++ {
					r, g, blue, a := img.At(x, y).RGBA()
					if a < 0xffff {
						inv := 0xffff - a
						r = min(0xffff, r+inv)
						g = min(0xffff, g+inv)
						blue = min(0xffff, blue+inv)
					}
					offset := (x - b.Min.X) * 3
					row[offset], row[offset+1], row[offset+2] = byte(r>>8), byte(g>>8), byte(blue>>8)
				}
				if _, err := zw.Write(row); err != nil {
					return err
				}
			}
		}
		if err := zw.Close(); err != nil {
			return err
		}
		picRef := sw.AllocRef()
		picDict := pdfgo.Dictionary{
			"Type":             pdfgo.Name("XObject"),
			"Subtype":          pdfgo.Name("Image"),
			"Width":            pdfgo.Integer(w),
			"Height":           pdfgo.Integer(h),
			"ColorSpace":       pdfgo.Name("DeviceRGB"),
			"BitsPerComponent": pdfgo.Integer(8),
			"Filter":           pdfgo.Name("FlateDecode"),
		}
		if err := sw.WritePreencodedStream(picRef, picDict, imgBuf.Bytes()); err != nil {
			return err
		}
		content.Reset()
		content.WriteString("q ")
		if err := printPDFNumbers(&content, sheetW, 0, 0, sheetH, 0, 0); err != nil {
			return err
		}
		content.WriteString("cm /I Do Q\n")
		streamRef := sw.AllocRef()
		if err := sw.WriteStream(streamRef, pdfgo.Dictionary{}, content.Bytes(), true); err != nil {
			return err
		}
		pageDict := pdfgo.Dictionary{
			"Type":      pdfgo.Name("Page"),
			"Parent":    sw.PageParent(i),
			"MediaBox":  pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Real(sheetW), pdfgo.Real(sheetH)},
			"Resources": pdfgo.Dictionary{"XObject": pdfgo.Dictionary{"I": picRef}},
			"Contents":  streamRef,
		}
		if err := sw.WriteObject(sw.PageRef(i), pageDict); err != nil {
			return err
		}
	}
	return sw.Finish(nil, nil)
}

// printPDFNumbers 编码内容流数值，不使用PDF语法不接受的科学记数法并校验有限性
// 入参: out 内容流缓冲区, values 待编码数值
// 返回: error 非有限数值错误
func printPDFNumbers(out *bytes.Buffer, values ...float64) error {
	for _, value := range values {
		if !finite(value) {
			return fmt.Errorf("invalid print PDF number")
		}
		out.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
		out.WriteByte(' ')
	}
	return nil
}

// isCryptDecodeParms 判断单个解码参数字典是否仅属于Crypt过滤器或为空字典
// 入参: dict 解码参数字典
// 返回: bool 是否为Crypt解码参数
func isCryptDecodeParms(dict pdfgo.Dictionary) bool {
	if len(dict) == 0 {
		return true
	}
	if kind, ok := dict["Type"].(pdfgo.Name); ok && kind == "CryptFilterDecodeParms" {
		return true
	}
	for key := range dict {
		if key != "Type" && key != "Name" {
			return false
		}
	}
	return true
}

// copy 递归复制资源并立即流式写出间接对象，保留流编码、资源共享并安全处理循环引用
// 入参: object 待复制对象, depth 当前递归深度
// 返回: pdfgo.Object 改写引用后的对象, error 资源、嵌套、类型或取消错误
func (p *printStreamCopier) copy(object pdfgo.Object, depth int) (pdfgo.Object, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > 256 {
		return nil, fmt.Errorf("print PDF resource nesting too deep")
	}
	switch value := object.(type) {
	case nil, pdfgo.Boolean, pdfgo.Integer, pdfgo.Name, pdfgo.String:
		return object, nil
	case pdfgo.Real:
		if !finite(float64(value)) {
			return nil, fmt.Errorf("invalid print PDF number")
		}
		return object, nil
	case pdfgo.Reference:
		if next, ok := p.copied[value]; ok {
			return next, nil
		}
		if p.source == nil {
			return nil, fmt.Errorf("print PDF missing source reader")
		}
		next := p.writer.AllocRef()
		p.copied[value] = next
		resolved, err := p.source.Object(value)
		if err != nil {
			return nil, err
		}
		if stream, ok := resolved.(*pdfgo.Stream); ok {
			if err := p.copyStream(next, stream, depth); err != nil {
				return nil, err
			}
			return next, nil
		}
		copied, err := p.copy(resolved, depth+1)
		if err != nil {
			return nil, err
		}
		if err := p.writer.WriteObject(next, copied); err != nil {
			return nil, err
		}
		return next, nil
	case pdfgo.Dictionary:
		result := make(pdfgo.Dictionary, len(value))
		for key, item := range value {
			copied, err := p.copy(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = copied
		}
		return result, nil
	case pdfgo.Array:
		result := make(pdfgo.Array, len(value))
		for i, item := range value {
			copied, err := p.copy(item, depth+1)
			if err != nil {
				return nil, err
			}
			result[i] = copied
		}
		return result, nil
	case *pdfgo.Stream:
		ref := p.writer.AllocRef()
		if err := p.copyStream(ref, value, depth); err != nil {
			return nil, err
		}
		return ref, nil
	default:
		return nil, fmt.Errorf("unsupported print PDF object %T", object)
	}
}

// copyStream 克隆流字典、剥离Crypt过滤器并流式写出已编码流对象
// 入参: ref 目标流引用, stream 源流对象, depth 当前递归深度
// 返回: error 复制或写入错误
func (p *printStreamCopier) copyStream(ref pdfgo.Reference, stream *pdfgo.Stream, depth int) error {
	dict := maps.Clone(stream.Dictionary)
	if dict == nil {
		dict = pdfgo.Dictionary{}
	}
	delete(dict, "Length")
	if err := p.stripCryptFilter(dict, depth+1); err != nil {
		return err
	}
	copiedDict, err := p.copy(dict, depth+1)
	if err != nil {
		return err
	}
	return p.writer.WritePreencodedStream(ref, copiedDict.(pdfgo.Dictionary), stream.Data)
}

// stripCryptFilter 剥离流字典中的Crypt过滤器及其对应解码参数，避免未加密目标PDF残留无效过滤器
// 入参: dict 克隆后的流字典, depth 当前递归深度
// 返回: error 引用解析或嵌套错误
func (p *printStreamCopier) stripCryptFilter(dict pdfgo.Dictionary, depth int) error {
	filterObj, exists := dict["Filter"]
	if !exists || filterObj == nil {
		return nil
	}
	resolvedFilter, err := p.resolve(filterObj, depth)
	if err != nil {
		return err
	}
	switch filter := resolvedFilter.(type) {
	case pdfgo.Name:
		if filter == "Crypt" {
			delete(dict, "Filter")
			delete(dict, "DecodeParms")
		}
		return nil
	case pdfgo.Array:
		var keptFilters pdfgo.Array
		var keptIndices []int
		stripped := false
		for i, item := range filter {
			resolvedItem, err := p.resolve(item, depth+1)
			if err != nil {
				return err
			}
			if name, ok := resolvedItem.(pdfgo.Name); ok && name == "Crypt" {
				stripped = true
				continue
			}
			keptFilters = append(keptFilters, item)
			keptIndices = append(keptIndices, i)
		}
		if !stripped {
			return nil
		}
		if len(keptFilters) == 0 {
			delete(dict, "Filter")
			delete(dict, "DecodeParms")
			return nil
		}
		if len(keptFilters) == 1 {
			dict["Filter"] = keptFilters[0]
		} else {
			dict["Filter"] = keptFilters
		}
		parmsObj, hasParms := dict["DecodeParms"]
		if !hasParms || parmsObj == nil {
			return nil
		}
		resolvedParms, err := p.resolve(parmsObj, depth)
		if err != nil {
			return err
		}
		switch parms := resolvedParms.(type) {
		case nil:
			delete(dict, "DecodeParms")
		case pdfgo.Array:
			if len(keptFilters) == 1 {
				idx := keptIndices[0]
				if idx < len(parms) {
					resolvedItem, err := p.resolve(parms[idx], depth+1)
					if err != nil {
						return err
					}
					if resolvedItem != nil {
						dict["DecodeParms"] = parms[idx]
						return nil
					}
				}
				delete(dict, "DecodeParms")
				return nil
			}
			keptParms := make(pdfgo.Array, len(keptIndices))
			hasEntry := false
			for i, idx := range keptIndices {
				if idx >= len(parms) {
					continue
				}
				resolvedItem, err := p.resolve(parms[idx], depth+1)
				if err != nil {
					return err
				}
				if resolvedItem != nil {
					keptParms[i] = parms[idx]
					hasEntry = true
				}
			}
			if hasEntry {
				dict["DecodeParms"] = keptParms
			} else {
				delete(dict, "DecodeParms")
			}
		case pdfgo.Dictionary:
			if len(keptFilters) != 1 || isCryptDecodeParms(parms) {
				delete(dict, "DecodeParms")
			}
		default:
			delete(dict, "DecodeParms")
		}
		return nil
	default:
		return nil
	}
}

// resolve 沿间接引用链解析PDF对象，限制嵌套深度并响应上下文取消
// 入参: object 待解析对象, depth 当前递归深度
// 返回: pdfgo.Object 最终对象, error 嵌套、来源或取消错误
func (p *printStreamCopier) resolve(object pdfgo.Object, depth int) (pdfgo.Object, error) {
	for {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		if depth > 256 {
			return nil, fmt.Errorf("print PDF resource nesting too deep")
		}
		ref, ok := object.(pdfgo.Reference)
		if !ok {
			return object, nil
		}
		if p.source == nil {
			return nil, fmt.Errorf("print PDF missing source reader")
		}
		next, err := p.source.Object(ref)
		if err != nil {
			return nil, err
		}
		object = next
		depth++
	}
}
