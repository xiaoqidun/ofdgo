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
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

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

// printPDFObjects 写出自有打印对象，原PDF资源仅复制实际引用的部分
type printPDFObjects struct {
	ctx     context.Context
	objects []pdfgo.Object
	copied  map[pdfgo.Reference]pdfgo.Reference
	source  *pdfgo.Reader
}

// printPDFWriter 记录输出偏移及首次错误，支持对象编码的连续写入
type printPDFWriter struct {
	writer io.Writer
	size   int64
	err    error
}

// Write 写入数据并记录短写或底层错误
// 入参: data 输出数据
// 返回: int 已写字节数, error 写入错误
func (w *printPDFWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	total := 0
	for len(data) != 0 {
		chunk := data[:min(len(data), 32<<10)]
		n, err := w.writer.Write(chunk)
		total += n
		w.size += int64(n)
		if err == nil && n != len(chunk) {
			err = io.ErrShortWrite
		}
		w.err = err
		if err != nil {
			return total, err
		}
		data = data[n:]
	}
	return total, nil
}

// WriteString 写入PDF语法文本
// 入参: value 输出文本
// 返回: int 已写字节数, error 写入错误
func (w *printPDFWriter) WriteString(value string) (int, error) { return w.Write([]byte(value)) }

// WriteByte 写入PDF语法字符
// 入参: value 输出字符
// 返回: error 写入错误
func (w *printPDFWriter) WriteByte(value byte) error {
	_, err := w.Write([]byte{value})
	return err
}

// printPDFNumbers 编码内容流数值，不使用PDF语法不接受的科学记数法
// 入参: out 内容流缓冲区, values 待编码数值
func printPDFNumbers(out *bytes.Buffer, values ...float64) {
	for _, value := range values {
		out.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
		out.WriteByte(' ')
	}
}

// add 登记一个独立输出对象
// 入参: object 待登记对象
// 返回: pdfgo.Reference 输出对象引用
func (p *printPDFObjects) add(object pdfgo.Object) pdfgo.Reference {
	p.objects = append(p.objects, object)
	return pdfgo.Reference{Number: int64(len(p.objects))}
}

// copy 递归复制资源并改写引用，保留流编码及资源共享
// 入参: object 待复制对象, depth 当前递归深度
// 返回: pdfgo.Object 改写引用后的对象, error 资源、嵌套或取消错误
func (p *printPDFObjects) copy(object pdfgo.Object, depth int) (pdfgo.Object, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > 256 {
		return nil, fmt.Errorf("print PDF resource nesting too deep")
	}
	switch value := object.(type) {
	case pdfgo.Reference:
		if next, ok := p.copied[value]; ok {
			return next, nil
		}
		next := p.add(nil)
		p.copied[value] = next
		resolved, err := p.source.Object(value)
		if err != nil {
			return nil, err
		}
		copied, err := p.copy(resolved, depth+1)
		if err != nil {
			return nil, err
		}
		p.objects[next.Number-1] = copied
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
		dict := maps.Clone(value.Dictionary)
		delete(dict, "Length")
		copied, err := p.copy(dict, depth+1)
		if err != nil {
			return nil, err
		}
		return &pdfgo.Stream{Dictionary: copied.(pdfgo.Dictionary), Data: value.Data}, nil
	default:
		return object, nil
	}
}

// WritePrintPDF 从随机读取源组装打印纸面，保持矢量、文字、资源及透明组
// 入参: ctx 输出上下文, writer 输出流, source 源PDF, size 源数据长度, sheets 有序纸面
// 返回: error 资源、编码、写入或取消错误
func WritePrintPDF(ctx context.Context, writer io.Writer, source io.ReaderAt, size int64, sheets []PrintSheet) error {
	var reader *pdfgo.Reader
	if size > 0 {
		var err error
		reader, err = pdfgo.NewReader(source, size)
		if err != nil {
			return err
		}
		defer reader.Close()
	}
	p := &printPDFObjects{ctx: ctx, objects: make([]pdfgo.Object, 2), copied: make(map[pdfgo.Reference]pdfgo.Reference), source: reader}
	var pages []*pdfgo.Page
	if reader != nil {
		if err := reader.WalkPages(ctx, func(_ int, page *pdfgo.Page) error { pages = append(pages, page); return nil }); err != nil {
			return err
		}
	}
	forms := make(map[int]pdfgo.Reference)
	var children pdfgo.Array
	const pt = 72.0 / 25.4
	for _, sheet := range sheets {
		if err := ctx.Err(); err != nil {
			return err
		}
		var content bytes.Buffer
		objects := make(pdfgo.Dictionary)
		for _, placement := range sheet.Placements {
			if placement.Page < 0 || placement.Page >= len(pages) {
				return fmt.Errorf("print PDF page outside source")
			}
			page := pages[placement.Page]
			if page.Rotation != 0 || page.UserUnit != 1 {
				return fmt.Errorf("print PDF backend must normalize page rotation and units")
			}
			form, ok := forms[placement.Page]
			if !ok {
				resources, err := p.copy(page.Resources, 0)
				if err != nil {
					return err
				}
				var data bytes.Buffer
				if _, err := page.WriteContent(ctx, &data); err != nil {
					return err
				}
				box := page.MediaBox
				dict := pdfgo.Dictionary{"Type": pdfgo.Name("XObject"), "Subtype": pdfgo.Name("Form"), "FormType": pdfgo.Integer(1), "BBox": pdfgo.Array{pdfgo.Real(box.XMin), pdfgo.Real(box.YMin), pdfgo.Real(box.XMax), pdfgo.Real(box.YMax)}, "Resources": resources}
				if group := page.Dictionary["Group"]; group != nil {
					copied, err := p.copy(group, 0)
					if err != nil {
						return err
					}
					dict["Group"] = copied
				}
				form = p.add(&pdfgo.Stream{Dictionary: dict, Data: data.Bytes()})
				forms[placement.Page] = form
			}
			name := pdfgo.Name(fmt.Sprintf("P%d", placement.Page))
			objects[name] = form
			c, m := placement.Clip, placement.Matrix
			hs := (page.MediaBox.YMax - page.MediaBox.YMin) / pt
			x0, y0 := page.MediaBox.XMin, page.MediaBox.YMin
			content.WriteString("q ")
			printPDFNumbers(&content, c[0]*pt, (sheet.Height-c[1]-c[3])*pt, c[2]*pt, c[3]*pt)
			content.WriteString("re W n ")
			printPDFNumbers(&content, m[0], -m[1], -m[2], m[3], (m[2]*hs+m[4])*pt-m[0]*x0+m[2]*y0, (sheet.Height-m[3]*hs-m[5])*pt+m[1]*x0-m[3]*y0)
			fmt.Fprintf(&content, "cm /%s Do Q\n", name)
		}
		for _, line := range sheet.Lines {
			content.WriteString("q 0 G ")
			printPDFNumbers(&content, 0.15*pt)
			content.WriteString("w ")
			printPDFNumbers(&content, line[0]*pt, (sheet.Height-line[1])*pt)
			content.WriteString("m ")
			printPDFNumbers(&content, line[2]*pt, (sheet.Height-line[3])*pt)
			content.WriteString("l S Q\n")
		}
		resources := pdfgo.Dictionary{"XObject": objects}
		if sheet.Label != "" {
			content.WriteString("q 0 g BT /Label 7.94 Tf 1 0 0 1 ")
			printPDFNumbers(&content, sheet.LabelX*pt, (sheet.Height-sheet.LabelY)*pt)
			content.WriteString("Tm ")
			if err := printPDFObject(&printPDFWriter{writer: &content}, pdfgo.String(sheet.Label), 0); err != nil {
				return err
			}
			content.WriteString(" Tj ET Q\n")
			resources["Font"] = pdfgo.Dictionary{"Label": pdfgo.Dictionary{"Type": pdfgo.Name("Font"), "Subtype": pdfgo.Name("Type1"), "BaseFont": pdfgo.Name("Helvetica")}}
		}
		stream := p.add(&pdfgo.Stream{Dictionary: pdfgo.Dictionary{}, Data: content.Bytes()})
		children = append(children, p.add(pdfgo.Dictionary{"Type": pdfgo.Name("Page"), "Parent": pdfgo.Reference{Number: 2}, "MediaBox": pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Real(sheet.Width * pt), pdfgo.Real(sheet.Height * pt)}, "Resources": resources, "Contents": stream}))
	}
	return p.write(writer, children)
}

// WritePrintImages 逐纸面消费光栅结果，不在输出对象中保留未压缩像素
// 入参: ctx 输出上下文, writer 输出流, sheets 有序纸面, render 按索引渲染纸面
// 返回: error 渲染、编码、写入或取消错误
func WritePrintImages(ctx context.Context, writer io.Writer, sheets []PrintSheet, render func(int) (image.Image, error)) error {
	p := &printPDFObjects{ctx: ctx, objects: make([]pdfgo.Object, 2)}
	var children pdfgo.Array
	const pt = 72.0 / 25.4
	for i, sheet := range sheets {
		if err := ctx.Err(); err != nil {
			return err
		}
		img, err := render(i)
		if err != nil {
			return err
		}
		var data bytes.Buffer
		compressed := zlib.NewWriter(&data)
		b := img.Bounds()
		row := make([]byte, b.Dx()*3)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, blue, _ := img.At(x, y).RGBA()
				offset := (x - b.Min.X) * 3
				row[offset], row[offset+1], row[offset+2] = byte(r>>8), byte(g>>8), byte(blue>>8)
			}
			if _, err := compressed.Write(row); err != nil {
				return err
			}
		}
		if err := compressed.Close(); err != nil {
			return err
		}
		picture := p.add(&pdfgo.Stream{Dictionary: pdfgo.Dictionary{"Type": pdfgo.Name("XObject"), "Subtype": pdfgo.Name("Image"), "Width": pdfgo.Integer(b.Dx()), "Height": pdfgo.Integer(b.Dy()), "ColorSpace": pdfgo.Name("DeviceRGB"), "BitsPerComponent": pdfgo.Integer(8), "Filter": pdfgo.Name("FlateDecode")}, Data: data.Bytes()})
		var content bytes.Buffer
		content.WriteString("q ")
		printPDFNumbers(&content, sheet.Width*pt, 0, 0, sheet.Height*pt, 0, 0)
		content.WriteString("cm /I Do Q\n")
		stream := p.add(&pdfgo.Stream{Dictionary: pdfgo.Dictionary{}, Data: content.Bytes()})
		children = append(children, p.add(pdfgo.Dictionary{"Type": pdfgo.Name("Page"), "Parent": pdfgo.Reference{Number: 2}, "MediaBox": pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Real(sheet.Width * pt), pdfgo.Real(sheet.Height * pt)}, "Resources": pdfgo.Dictionary{"XObject": pdfgo.Dictionary{"I": picture}}, "Contents": stream}))
	}
	return p.write(writer, children)
}

// write 写出完整对象表和交叉引用，失败时由调用方丢弃输出
// 入参: writer 输出流, children 页面对象引用
// 返回: error 编码、写入或取消错误
func (p *printPDFObjects) write(writer io.Writer, children pdfgo.Array) error {
	p.objects[0] = pdfgo.Dictionary{"Type": pdfgo.Name("Catalog"), "Pages": pdfgo.Reference{Number: 2}}
	p.objects[1] = pdfgo.Dictionary{"Type": pdfgo.Name("Pages"), "Count": pdfgo.Integer(len(children)), "Kids": children}
	buffer := bufio.NewWriter(outputWriter{ctx: p.ctx, writer: writer})
	out := &printPDFWriter{writer: buffer}
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int64, len(p.objects))
	for i, object := range p.objects {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		offsets[i] = out.size
		fmt.Fprintf(out, "%d 0 obj\n", i+1)
		if err := printPDFObject(out, object, 0); err != nil {
			return err
		}
		out.WriteString("\nendobj\n")
		p.objects[i] = nil
	}
	xref := out.size
	fmt.Fprintf(out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, offset := range offsets {
		fmt.Fprintf(out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(out, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	if out.err != nil {
		return out.err
	}
	return buffer.Flush()
}

// printPDFObject 编码已复制的标准PDF对象，名称和字符串按字节转义
// 入参: out 对象输出, object 待编码对象, depth 当前递归深度
// 返回: error 类型、嵌套或写入错误
func printPDFObject(out *printPDFWriter, object pdfgo.Object, depth int) error {
	if depth > 256 {
		return fmt.Errorf("print PDF object nesting too deep")
	}
	switch value := object.(type) {
	case nil:
		out.WriteString("null")
	case pdfgo.Boolean:
		fmt.Fprintf(out, "%t", value)
	case pdfgo.Integer:
		fmt.Fprintf(out, "%d", value)
	case pdfgo.Real:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("invalid print PDF number")
		}
		out.WriteString(strconv.FormatFloat(float64(value), 'f', -1, 64))
	case pdfgo.Reference:
		fmt.Fprintf(out, "%d %d R", value.Number, value.Generation)
	case pdfgo.Name:
		out.WriteByte('/')
		for _, b := range []byte(value) {
			if b <= 32 || b >= 127 || strings.ContainsRune("()<>[]{}/%#", rune(b)) {
				fmt.Fprintf(out, "#%02X", b)
			} else {
				out.WriteByte(b)
			}
		}
	case pdfgo.String:
		out.WriteByte('<')
		out.WriteString(hex.EncodeToString(value))
		out.WriteByte('>')
	case pdfgo.Array:
		out.WriteByte('[')
		for _, item := range value {
			if err := printPDFObject(out, item, depth+1); err != nil {
				return err
			}
			out.WriteByte(' ')
		}
		out.WriteByte(']')
	case pdfgo.Dictionary:
		out.WriteString("<<")
		for _, key := range slices.Sorted(maps.Keys(value)) {
			if err := printPDFObject(out, key, depth+1); err != nil {
				return err
			}
			out.WriteByte(' ')
			if err := printPDFObject(out, value[key], depth+1); err != nil {
				return err
			}
			out.WriteByte('\n')
		}
		out.WriteString(">>")
	case *pdfgo.Stream:
		dict := maps.Clone(value.Dictionary)
		dict["Length"] = pdfgo.Integer(len(value.Data))
		if err := printPDFObject(out, dict, depth+1); err != nil {
			return err
		}
		out.WriteString("\nstream\n")
		out.Write(value.Data)
		out.WriteString("\nendstream")
	default:
		return fmt.Errorf("unsupported print PDF object %T", object)
	}
	return out.err
}
