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
	"cmp"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"io"
	"math"
	"slices"
	"strconv"
	"sync"

	"github.com/xiaoqidun/pdfgo"
)

// PtPerMM 为毫米到PDF点的换算系数
const PtPerMM = 72.0 / 25.4

// PDF流式写出器的结构参数
const (
	pagesTreeDegree   = 64
	objStmBatchSize   = 128
	streamBufferBytes = 256 << 10
)

// PDF交叉引用流条目类型
const (
	xrefFree       uint8 = 0
	xrefNormal     uint8 = 1
	xrefCompressed uint8 = 2
)

// pdfFileHeader 为PDF 1.7二进制文件头
var pdfFileHeader = []byte("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")

// TextPage 保存单页文字对象计数及待替换区段
type TextPage struct {
	Count        int
	Replacements []pdfgo.TextReplacement
}

// RawImage 保存直通写入PDF的原始压缩图像流及元信息
type RawImage struct {
	Key              image.Image
	Filter           string
	Width            int
	Height           int
	BitsPerComponent int
	ColorSpace       string
	Data             []byte
	Interpolate      *bool
}

// StreamWriter 为PDF 1.7流式写出器，支持平衡页面树、对象流分批压缩及预测器交叉引用流
type StreamWriter struct {
	ctx           context.Context
	out           *bufio.Writer
	pos           int64
	err           error
	compress      bool
	compressLevel int
	xrefMu        sync.Mutex
	xref          []xrefEntry
	catalogRef    pdfgo.Reference
	infoRef       pdfgo.Reference
	pagesRootRef  pdfgo.Reference
	pageCount     int
	pageRefs      []pdfgo.Reference
	pageParents   []pdfgo.Reference
	pageTreeNodes []pageTreeNode
	objStmNums    []int
	objStmOffsets []int
	objStmBody    bytes.Buffer
	scratchBuf    bytes.Buffer
	zlibBuf       bytes.Buffer
	zlibWriter    *zlib.Writer
}

// outputWriter 在分块输出前后检查取消，不改变底层写入错误
type outputWriter struct {
	ctx    context.Context
	writer io.Writer
}

// xrefEntry 记录单个PDF间接对象的交叉引用定位信息
type xrefEntry struct {
	kind   uint8
	field2 uint64
	field3 uint32
}

// pageTreeNode 描述平衡页面树中的单个内部节点
type pageTreeNode struct {
	ref    pdfgo.Reference
	parent pdfgo.Reference
	kids   []pdfgo.Reference
	count  int
}

// NewStreamWriter 创建流式PDF写出器并预分配目录、平衡页面树与全部页面引用
// 入参: ctx 取消上下文, writer 目标输出流, pageCount 总页数, compress 是否压缩流对象, level zlib压缩级别
// 返回: *StreamWriter 流式写出器, error 写入或参数错误
func NewStreamWriter(ctx context.Context, writer io.Writer, pageCount int, compress bool, level int) (*StreamWriter, error) {
	return newStreamWriter(ctx, writer, pageCount, compress, level, false)
}

// newStreamWriter 创建流式PDF写出器并按需允许零页文档
// 入参: ctx 取消上下文, writer 目标输出流, pageCount 总页数, compress 是否压缩流对象, level zlib压缩级别, allowEmpty 是否允许零页
// 返回: *StreamWriter 流式写出器, error 写入或参数错误
func newStreamWriter(ctx context.Context, writer io.Writer, pageCount int, compress bool, level int, allowEmpty bool) (*StreamWriter, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if writer == nil {
		return nil, fmt.Errorf("PDF output writer is nil")
	}
	if pageCount < 0 || !allowEmpty && pageCount == 0 {
		return nil, fmt.Errorf("PDF page count must be positive")
	}
	if level < zlib.NoCompression || level > zlib.BestCompression {
		level = zlib.DefaultCompression
	}
	sw := &StreamWriter{
		ctx:           ctx,
		out:           bufio.NewWriterSize(outputWriter{ctx: ctx, writer: writer}, streamBufferBytes),
		compress:      compress,
		compressLevel: level,
		xref:          []xrefEntry{{kind: xrefFree, field2: 0, field3: 65535}},
		pageCount:     pageCount,
		objStmNums:    make([]int, 0, objStmBatchSize),
		objStmOffsets: make([]int, 0, objStmBatchSize),
	}
	sw.catalogRef = sw.AllocRef()
	sw.pagesRootRef = sw.AllocRef()
	sw.initPageTree(pageCount)
	if err := sw.writeRaw(pdfFileHeader); err != nil {
		return nil, err
	}
	return sw, nil
}

// CompressBytes 使用可复用zlib写入器压缩独立字节切片并返回新切片
// 入参: zw 可选复用zlib写入器, buf 可选复用缓冲, level 压缩级别, data 原始数据
// 返回: []byte 压缩副本, *zlib.Writer 复用后的写入器, error 压缩错误
func CompressBytes(zw *zlib.Writer, buf *bytes.Buffer, level int, data []byte) ([]byte, *zlib.Writer, error) {
	if buf == nil {
		buf = &bytes.Buffer{}
	} else {
		buf.Reset()
	}
	if zw == nil {
		var err error
		zw, err = zlib.NewWriterLevel(buf, level)
		if err != nil {
			return nil, nil, err
		}
	} else {
		zw.Reset(buf)
	}
	if _, err := zw.Write(data); err != nil {
		return nil, zw, err
	}
	if err := zw.Close(); err != nil {
		return nil, zw, err
	}
	return bytes.Clone(buf.Bytes()), zw, nil
}

// appendPDFStreamDict 序列化流字典并写入Length与可选FlateDecode，不修改调用方字典
// 入参: buf 目标缓冲, dict 原始流字典, length 载荷字节数, addFlate 是否补充FlateDecode
func appendPDFStreamDict(buf *bytes.Buffer, dict pdfgo.Dictionary, length int, addFlate bool) {
	keys := make([]pdfgo.Name, 0, len(dict)+2)
	hasFilter := false
	for k := range dict {
		if k == "Length" {
			continue
		}
		if k == "Filter" {
			hasFilter = true
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b pdfgo.Name) int { return cmp.Compare(a, b) })
	buf.WriteString("<<")
	for _, k := range keys {
		appendPDFName(buf, string(k))
		v := dict[k]
		if pdfObjectNeedsLeadingSpace(v) {
			buf.WriteByte(' ')
		}
		appendPDFObject(buf, v)
	}
	if addFlate && !hasFilter {
		buf.WriteString("/Filter/FlateDecode")
	}
	buf.WriteString("/Length ")
	buf.WriteString(strconv.Itoa(length))
	buf.WriteString(">>")
}

// appendPDFObject 按ISO 32000-1语法序列化任意PDF对象值
// 入参: buf 目标缓冲, obj PDF对象
func appendPDFObject(buf *bytes.Buffer, obj pdfgo.Object) {
	switch v := obj.(type) {
	case nil:
		buf.WriteString("null")
	case pdfgo.Boolean:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case pdfgo.Integer:
		buf.WriteString(strconv.FormatInt(int64(v), 10))
	case pdfgo.Real:
		fv := float64(v)
		if fv != 0 && math.Abs(fv) < 1e-4 && math.Abs(fv) >= 1e-12 {
			appendCompactFloat(buf, fv, -1)
		} else {
			appendCompactFloat(buf, fv, 6)
		}
	case pdfgo.Name:
		appendPDFName(buf, string(v))
	case pdfgo.String:
		appendPDFLiteralString(buf, []byte(v))
	case pdfgo.Reference:
		buf.WriteString(strconv.FormatInt(v.Number, 10))
		buf.WriteByte(' ')
		buf.WriteString(strconv.FormatInt(v.Generation, 10))
		buf.WriteString(" R")
	case pdfgo.Array:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 && pdfObjectNeedsLeadingSpace(item) {
				buf.WriteByte(' ')
			}
			appendPDFObject(buf, item)
		}
		buf.WriteByte(']')
	case pdfgo.Dictionary:
		keys := make([]pdfgo.Name, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b pdfgo.Name) int { return cmp.Compare(a, b) })
		buf.WriteString("<<")
		for _, k := range keys {
			appendPDFName(buf, string(k))
			val := v[k]
			if pdfObjectNeedsLeadingSpace(val) {
				buf.WriteByte(' ')
			}
			appendPDFObject(buf, val)
		}
		buf.WriteString(">>")
	default:
		buf.WriteString("null")
	}
}

// pdfObjectNeedsLeadingSpace 判断对象序列化在名称或前驱项之后是否需要空格分隔
// 入参: obj PDF对象
// 返回: bool 是否需要前置空格
func pdfObjectNeedsLeadingSpace(obj pdfgo.Object) bool {
	switch obj.(type) {
	case pdfgo.Name, pdfgo.String, pdfgo.Array, pdfgo.Dictionary:
		return false
	default:
		return true
	}
}

// appendPDFName 按PDF名称转义规则序列化名称对象
// 入参: buf 目标缓冲, name 名称文本
func appendPDFName(buf *bytes.Buffer, name string) {
	buf.WriteByte('/')
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x21 || c > 0x7E || c == '#' || c == '/' || c == '%' || c == '(' || c == ')' || c == '<' || c == '>' || c == '[' || c == ']' || c == '{' || c == '}' {
			const hex = "0123456789ABCDEF"
			buf.WriteByte('#')
			buf.WriteByte(hex[c>>4])
			buf.WriteByte(hex[c&0x0F])
			continue
		}
		buf.WriteByte(c)
	}
}

// appendPDFLiteralString 按PDF字面量字符串规则转义并写入字节串
// 入参: buf 目标缓冲, data 原始字节
func appendPDFLiteralString(buf *bytes.Buffer, data []byte) {
	buf.WriteByte('(')
	for _, c := range data {
		switch c {
		case '\\', '(', ')':
			buf.WriteByte('\\')
			buf.WriteByte(c)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		default:
			buf.WriteByte(c)
		}
	}
	buf.WriteByte(')')
}

// appendCompactFloat 将有限浮点数格式化为紧凑PDF实数文本，省略尾随零与前导零
// 入参: buf 目标缓冲, value 浮点值, prec 最大小数位数
func appendCompactFloat(buf *bytes.Buffer, value float64, prec int) {
	var scratch [48]byte
	out := appendCompactFloatBytes(scratch[:0], value, prec)
	buf.Write(out)
}

// appendCompactFloatBytes 向字节切片追加紧凑PDF实数，避免临时字符串分配
// 入参: dst 目标切片, value 浮点值, prec 最大小数位数
// 返回: []byte 追加后的切片
func appendCompactFloatBytes(dst []byte, value float64, prec int) []byte {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return append(dst, '0')
	}
	if value == 0 {
		return append(dst, '0')
	}
	start := len(dst)
	dst = strconv.AppendFloat(dst, value, 'f', prec, 64)
	dot := bytes.IndexByte(dst[start:], '.')
	if dot >= 0 {
		end := len(dst)
		for end > start+dot+1 && dst[end-1] == '0' {
			end--
		}
		if end == start+dot+1 {
			end--
		}
		dst = dst[:end]
	}
	s := dst[start:]
	if len(s) == 2 && s[0] == '-' && s[1] == '0' {
		return append(dst[:start], '0')
	}
	if len(s) > 2 && s[0] == '0' && s[1] == '.' {
		copy(dst[start:], s[1:])
		return dst[:len(dst)-1]
	}
	if len(s) > 3 && s[0] == '-' && s[1] == '0' && s[2] == '.' {
		dst[start+1] = '.'
		copy(dst[start+2:], s[3:])
		return dst[:len(dst)-1]
	}
	return dst
}

// minByteWidth 计算无符号整数大端编码所需的最小字节宽度
// 入参: v 无符号整数
// 返回: int 最小字节数
func minByteWidth(v uint64) int {
	w := 1
	for v >>= 8; v != 0; v >>= 8 {
		w++
	}
	return w
}

// putBigEndian 按指定宽度以大端序写入无符号整数
// 入参: dst 目标切片, v 无符号整数
func putBigEndian(dst []byte, v uint64) {
	for i := len(dst) - 1; i >= 0; i-- {
		dst[i] = byte(v)
		v >>= 8
	}
}

// CatalogRef 返回文档目录对象引用
// 返回: pdfgo.Reference 目录引用
func (w *StreamWriter) CatalogRef() pdfgo.Reference {
	return w.catalogRef
}

// InfoRef 返回文档信息对象引用
// 返回: pdfgo.Reference 信息引用
func (w *StreamWriter) InfoRef() pdfgo.Reference {
	return w.infoRef
}

// PageRef 返回指定零基页码的预分配页面对象引用
// 入参: index 页面零基索引
// 返回: pdfgo.Reference 页面引用
func (w *StreamWriter) PageRef(index int) pdfgo.Reference {
	return w.pageRefs[index]
}

// PageParent 返回指定零基页码在平衡页面树中的直接父节点引用
// 入参: index 页面零基索引
// 返回: pdfgo.Reference 父节点引用
func (w *StreamWriter) PageParent(index int) pdfgo.Reference {
	return w.pageParents[index]
}

// PageRefs 返回全部预分配页面对象引用切片
// 返回: []pdfgo.Reference 页面引用列表
func (w *StreamWriter) PageRefs() []pdfgo.Reference {
	return w.pageRefs
}

// AllocRef 线程安全地分配下一个PDF间接对象引用
// 返回: pdfgo.Reference 新对象引用
func (w *StreamWriter) AllocRef() pdfgo.Reference {
	w.xrefMu.Lock()
	num := int64(len(w.xref))
	w.xref = append(w.xref, xrefEntry{kind: xrefFree})
	w.xrefMu.Unlock()
	return pdfgo.Reference{Number: num}
}

// WriteObject 将非流间接对象打入对象流批次，满额时自动压缩写出并释放内存
// 入参: ref 目标对象引用, obj PDF对象值
// 返回: error 写入或取消错误
func (w *StreamWriter) WriteObject(ref pdfgo.Reference, obj pdfgo.Object) error {
	if w.err != nil {
		return w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return err
	}
	w.xrefMu.Lock()
	valid := ref.Number > 0 && int(ref.Number) < len(w.xref)
	w.xrefMu.Unlock()
	if !valid {
		return fmt.Errorf("invalid PDF object reference %d", ref.Number)
	}
	if len(w.objStmNums) > 0 {
		w.objStmBody.WriteByte('\n')
	}
	w.objStmOffsets = append(w.objStmOffsets, w.objStmBody.Len())
	w.objStmNums = append(w.objStmNums, int(ref.Number))
	appendPDFObject(&w.objStmBody, obj)
	if len(w.objStmNums) >= objStmBatchSize || w.objStmBody.Len() >= streamBufferBytes {
		return w.flushObjStm()
	}
	return nil
}

// WriteDirectObject 将非流间接对象直接写出到文件顶层，不进入对象流
// 入参: ref 目标对象引用, obj PDF对象值
// 返回: error 写入或取消错误
func (w *StreamWriter) WriteDirectObject(ref pdfgo.Reference, obj pdfgo.Object) error {
	if w.err != nil {
		return w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return err
	}
	w.xrefMu.Lock()
	valid := ref.Number > 0 && int(ref.Number) < len(w.xref)
	if valid {
		w.xref[ref.Number] = xrefEntry{kind: xrefNormal, field2: uint64(w.pos), field3: 0}
	}
	w.xrefMu.Unlock()
	if !valid {
		return fmt.Errorf("invalid PDF object reference %d", ref.Number)
	}
	w.scratchBuf.Reset()
	w.scratchBuf.WriteString(strconv.FormatInt(ref.Number, 10))
	w.scratchBuf.WriteString(" 0 obj\n")
	appendPDFObject(&w.scratchBuf, obj)
	w.scratchBuf.WriteString("\nendobj\n")
	return w.writeRaw(w.scratchBuf.Bytes())
}

// WriteStream 写出PDF流对象并在启用压缩且无既有过滤器时执行Flate压缩
// 入参: ref 流对象引用, dict 流字典, data 原始流字节, allowCompress 是否允许Flate压缩
// 返回: error 压缩或写入错误
func (w *StreamWriter) WriteStream(ref pdfgo.Reference, dict pdfgo.Dictionary, data []byte, allowCompress bool) error {
	if w.err != nil {
		return w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return err
	}
	payload := data
	useFlate := allowCompress && w.compress && dict["Filter"] == nil
	if useFlate {
		compressed, err := w.deflate(data)
		if err != nil {
			w.err = err
			return err
		}
		payload = compressed
	}
	return w.writeStreamBytes(ref, dict, payload, useFlate)
}

// WritePreencodedStream 直接写出已完成压缩或编码的PDF流对象，避免二次压缩
// 入参: ref 流对象引用, dict 已含Filter声明的流字典, data 已编码流字节
// 返回: error 写入或取消错误
func (w *StreamWriter) WritePreencodedStream(ref pdfgo.Reference, dict pdfgo.Dictionary, data []byte) error {
	if w.err != nil {
		return w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return err
	}
	return w.writeStreamBytes(ref, dict, data, false)
}

// Finish 写出平衡页面树、目录、信息字典、剩余对象流及预测器交叉引用流并刷盘
// 入参: catalog 目录字典, info 信息字典，可为空
// 返回: error 写入或刷新错误
func (w *StreamWriter) Finish(catalog pdfgo.Dictionary, info pdfgo.Dictionary) error {
	if w.err != nil {
		return w.err
	}
	for i := range w.pageTreeNodes {
		node := &w.pageTreeNodes[i]
		kids := make(pdfgo.Array, len(node.kids))
		for j, kid := range node.kids {
			kids[j] = kid
		}
		dict := pdfgo.Dictionary{
			"Type":  pdfgo.Name("Pages"),
			"Kids":  kids,
			"Count": pdfgo.Integer(node.count),
		}
		if node.parent.Number != 0 {
			dict["Parent"] = node.parent
		}
		if err := w.WriteObject(node.ref, dict); err != nil {
			return err
		}
	}
	if catalog == nil {
		catalog = make(pdfgo.Dictionary)
	}
	catalog["Type"] = pdfgo.Name("Catalog")
	catalog["Pages"] = w.pagesRootRef
	if err := w.WriteObject(w.catalogRef, catalog); err != nil {
		return err
	}
	hasInfo := len(info) != 0
	if hasInfo {
		if w.infoRef.Number == 0 {
			w.infoRef = w.AllocRef()
		}
		if err := w.WriteObject(w.infoRef, info); err != nil {
			return err
		}
	}
	if err := w.flushObjStm(); err != nil {
		return err
	}
	if err := w.writeXRefStream(hasInfo); err != nil {
		return err
	}
	if err := w.out.Flush(); err != nil {
		w.err = err
		return err
	}
	return w.ctx.Err()
}

// Write 写入当前数据块并回报取消或写入错误
// 入参: data 输出数据
// 返回: int 写入字节数, error 取消或写入错误
func (w outputWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(data)
	if err == nil {
		err = w.ctx.Err()
	}
	return n, err
}

// initPageTree 自底向上构建度为64的平衡页面树并预分配所有页面与内部节点引用
// 入参: pageCount 总页数
func (w *StreamWriter) initPageTree(pageCount int) {
	w.pageRefs = make([]pdfgo.Reference, pageCount)
	w.pageParents = make([]pdfgo.Reference, pageCount)
	for i := range pageCount {
		w.pageRefs[i] = w.AllocRef()
	}
	if pageCount <= pagesTreeDegree {
		for i := range pageCount {
			w.pageParents[i] = w.pagesRootRef
		}
		w.pageTreeNodes = []pageTreeNode{{
			ref:   w.pagesRootRef,
			kids:  w.pageRefs,
			count: pageCount,
		}}
		return
	}
	type levelItem struct {
		ref   pdfgo.Reference
		count int
		node  int
	}
	current := make([]levelItem, pageCount)
	for i, ref := range w.pageRefs {
		current[i] = levelItem{ref: ref, count: 1, node: -1}
	}
	for len(current) > 1 {
		groups := (len(current) + pagesTreeDegree - 1) / pagesTreeDegree
		next := make([]levelItem, 0, groups)
		isRoot := groups == 1
		for g := range groups {
			start := g * pagesTreeDegree
			end := min(start+pagesTreeDegree, len(current))
			nodeRef := w.pagesRootRef
			if !isRoot {
				nodeRef = w.AllocRef()
			}
			kids := make([]pdfgo.Reference, end-start)
			total := 0
			for j := start; j < end; j++ {
				kids[j-start] = current[j].ref
				total += current[j].count
				if current[j].node < 0 {
					w.pageParents[j] = nodeRef
				} else {
					w.pageTreeNodes[current[j].node].parent = nodeRef
				}
			}
			idx := len(w.pageTreeNodes)
			w.pageTreeNodes = append(w.pageTreeNodes, pageTreeNode{ref: nodeRef, kids: kids, count: total})
			next = append(next, levelItem{ref: nodeRef, count: total, node: idx})
		}
		current = next
	}
}

// flushObjStm 将当前累积的非流对象打入PDF对象流并按压缩开关写出，随后释放临时缓冲
// 返回: error 压缩或写入错误
func (w *StreamWriter) flushObjStm() error {
	count := len(w.objStmNums)
	if count == 0 {
		return nil
	}
	stmRef := w.AllocRef()
	w.scratchBuf.Reset()
	for i := range count {
		if i > 0 {
			w.scratchBuf.WriteByte(' ')
		}
		w.scratchBuf.WriteString(strconv.Itoa(w.objStmNums[i]))
		w.scratchBuf.WriteByte(' ')
		w.scratchBuf.WriteString(strconv.Itoa(w.objStmOffsets[i]))
	}
	w.scratchBuf.WriteByte('\n')
	first := w.scratchBuf.Len()
	w.scratchBuf.Write(w.objStmBody.Bytes())
	dict := pdfgo.Dictionary{
		"Type":  pdfgo.Name("ObjStm"),
		"N":     pdfgo.Integer(count),
		"First": pdfgo.Integer(first),
	}
	var payload []byte
	if w.compress {
		compressed, err := w.deflate(w.scratchBuf.Bytes())
		if err != nil {
			w.err = err
			return err
		}
		payload = compressed
		dict["Filter"] = pdfgo.Name("FlateDecode")
	} else {
		payload = bytes.Clone(w.scratchBuf.Bytes())
	}
	if err := w.writeStreamBytes(stmRef, dict, payload, false); err != nil {
		return err
	}
	w.xrefMu.Lock()
	for i, objNum := range w.objStmNums {
		w.xref[objNum] = xrefEntry{kind: xrefCompressed, field2: uint64(stmRef.Number), field3: uint32(i)}
	}
	w.xrefMu.Unlock()
	w.objStmNums = w.objStmNums[:0]
	w.objStmOffsets = w.objStmOffsets[:0]
	w.objStmBody.Reset()
	return nil
}

// writeXRefStream 写出PDF 1.5+交叉引用流与文件尾，按压缩开关启用PNG Up预测器与Flate压缩
// 入参: hasInfo 是否包含有效文档信息对象
// 返回: error 压缩或写入错误
func (w *StreamWriter) writeXRefStream(hasInfo bool) error {
	xrefRef := w.AllocRef()
	xrefOffset := w.pos
	w.xrefMu.Lock()
	w.xref[xrefRef.Number] = xrefEntry{kind: xrefNormal, field2: uint64(xrefOffset), field3: 0}
	var maxF2 uint64
	var maxF3 uint32
	for i := 1; i < len(w.xref); i++ {
		if w.xref[i].field2 > maxF2 {
			maxF2 = w.xref[i].field2
		}
		if w.xref[i].field3 > maxF3 {
			maxF3 = w.xref[i].field3
		}
	}
	w2 := minByteWidth(maxF2)
	w3 := minByteWidth(uint64(maxF3))
	if w3 < 2 && w.xref[0].field3 > 255 {
		w3 = 2
	}
	cols := 1 + w2 + w3
	xrefSize := len(w.xref)
	var raw []byte
	if w.compress {
		rowStride := 1 + cols
		raw = make([]byte, xrefSize*rowStride)
		prev := make([]byte, cols)
		cur := make([]byte, cols)
		for i, entry := range w.xref {
			cur[0] = entry.kind
			putBigEndian(cur[1:1+w2], entry.field2)
			putBigEndian(cur[1+w2:cols], uint64(entry.field3))
			dst := raw[i*rowStride:]
			dst[0] = 2
			for j := range cols {
				dst[1+j] = cur[j] - prev[j]
			}
			copy(prev, cur)
		}
	} else {
		raw = make([]byte, xrefSize*cols)
		for i, entry := range w.xref {
			dst := raw[i*cols:]
			dst[0] = entry.kind
			putBigEndian(dst[1:1+w2], entry.field2)
			putBigEndian(dst[1+w2:cols], uint64(entry.field3))
		}
	}
	w.xrefMu.Unlock()
	dict := pdfgo.Dictionary{
		"Type": pdfgo.Name("XRef"),
		"Size": pdfgo.Integer(xrefSize),
		"Root": w.catalogRef,
		"W":    pdfgo.Array{pdfgo.Integer(1), pdfgo.Integer(w2), pdfgo.Integer(w3)},
	}
	payload := raw
	if w.compress {
		compressed, err := w.deflate(raw)
		if err != nil {
			w.err = err
			return err
		}
		payload = compressed
		dict["Filter"] = pdfgo.Name("FlateDecode")
		dict["DecodeParms"] = pdfgo.Dictionary{
			"Predictor": pdfgo.Integer(12),
			"Columns":   pdfgo.Integer(cols),
		}
	}
	if hasInfo {
		dict["Info"] = w.infoRef
	}
	if err := w.writeStreamBytes(xrefRef, dict, payload, false); err != nil {
		return err
	}
	w.scratchBuf.Reset()
	w.scratchBuf.WriteString("startxref\n")
	w.scratchBuf.WriteString(strconv.FormatInt(xrefOffset, 10))
	w.scratchBuf.WriteString("\n%%EOF\n")
	return w.writeRaw(w.scratchBuf.Bytes())
}

// writeStreamBytes 写出流对象头、原始或压缩载荷与结束标记
// 入参: ref 流对象引用, dict 流字典, payload 写出字节, addFlate 是否补写FlateDecode过滤器
// 返回: error 写入错误
func (w *StreamWriter) writeStreamBytes(ref pdfgo.Reference, dict pdfgo.Dictionary, payload []byte, addFlate bool) error {
	w.xrefMu.Lock()
	valid := ref.Number > 0 && int(ref.Number) < len(w.xref)
	if valid {
		w.xref[ref.Number] = xrefEntry{kind: xrefNormal, field2: uint64(w.pos), field3: 0}
	}
	w.xrefMu.Unlock()
	if !valid {
		return fmt.Errorf("invalid PDF object reference %d", ref.Number)
	}
	w.scratchBuf.Reset()
	w.scratchBuf.WriteString(strconv.FormatInt(ref.Number, 10))
	w.scratchBuf.WriteString(" 0 obj\n")
	appendPDFStreamDict(&w.scratchBuf, dict, len(payload), addFlate)
	w.scratchBuf.WriteString("\nstream\n")
	if err := w.writeRaw(w.scratchBuf.Bytes()); err != nil {
		return err
	}
	if len(payload) > 0 {
		if err := w.writeRaw(payload); err != nil {
			return err
		}
	}
	return w.writeRaw([]byte("\nendstream\nendobj\n"))
}

// deflate 复用内部zlib写入器压缩字节切片，避免高频分配压缩字典
// 入参: data 待压缩字节
// 返回: []byte 压缩结果视图, error 压缩错误
func (w *StreamWriter) deflate(data []byte) ([]byte, error) {
	w.zlibBuf.Reset()
	if w.zlibWriter == nil {
		zw, err := zlib.NewWriterLevel(&w.zlibBuf, w.compressLevel)
		if err != nil {
			return nil, err
		}
		w.zlibWriter = zw
	} else {
		w.zlibWriter.Reset(&w.zlibBuf)
	}
	if _, err := w.zlibWriter.Write(data); err != nil {
		return nil, err
	}
	if err := w.zlibWriter.Close(); err != nil {
		return nil, err
	}
	return w.zlibBuf.Bytes(), nil
}

// writeRaw 向底层缓冲输出字节并累加文件偏移
// 入参: data 待写出字节
// 返回: error 写入错误
func (w *StreamWriter) writeRaw(data []byte) error {
	n, err := w.out.Write(data)
	w.pos += int64(n)
	if err != nil {
		w.err = err
		return err
	}
	return nil
}
