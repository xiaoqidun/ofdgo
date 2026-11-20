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
	"cmp"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// TextPage 保存后端文字对象数量及待替换区段
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
}

// TextFauxBold 保存需在PDF内容流中启用填充并描边伪粗体的文字对象区间
type TextFauxBold struct {
	First     int
	Count     int
	LineWidth float64
	Color     color.RGBA
}

// WriteOptions 保存PDF最终写出所需的元数据、像素、文字和导航修正
// Images逐页记录绘制图像，nil图像表示不替换；Links保留全部链接位置，包括矩形区域的nil项
// Placeholders按页和图像绘制顺序标记阶段1是否使用轻量占位图
// RawImages按页和图像绘制顺序提供JPX或JBIG2原始流直通数据
// ImageClips按页和图像绘制顺序提供单位方形坐标中的裁剪路径，空字符串表示不裁剪
// TextFauxBold按页提供需要Tr=2伪粗体描边的BT区间
// PrepareRewrite在页面引用确定后填充导航动作，不得提前写出PDF
type WriteOptions struct {
	Information    pdfgo.Dictionary
	Images         [][]image.Image
	Placeholders   [][]bool
	RawImages      [][]*RawImage
	ImageClips     [][]string
	Text           []TextPage
	TextFauxBold   [][]TextFauxBold
	Links          map[int][]*pdfgo.LinkRegion
	ExactImages    bool
	ExactText      bool
	Optimization   pdfgo.OptimizeOptions
	PrepareRewrite func(context.Context, *pdfgo.Reader, []pdfgo.Reference, *pdfgo.RewriteOptions) error
}

// Write 合并图像、文字、导航修正及压缩，直接写出最终PDF
// 入参: ctx 取消上下文, data 后端PDF数据, writer 输出流, settings 写出配置
// 返回: error 资源、压缩或写入错误
func Write(ctx context.Context, data []byte, writer io.Writer, settings WriteOptions) error {
	return WriteSource(ctx, bytes.NewReader(data), int64(len(data)), writer, settings)
}

// WriteSource 从随机读取源完成资源修正及压缩，避免合并中间PDF缓冲
// 入参: ctx 取消上下文, source 后端PDF, size 数据长度, writer 输出流, settings 写出配置
// 返回: error 读取、资源、压缩或写入错误
func WriteSource(ctx context.Context, source io.ReaderAt, size int64, writer io.Writer, settings WriteOptions) error {
	nativeWrite := settings.PrepareRewrite != nil
	hasRawImages := false
	for _, pageRaws := range settings.RawImages {
		for _, raw := range pageRaws {
			if raw != nil {
				hasRawImages = true
				break
			}
		}
		if hasRawImages {
			break
		}
	}
	hasFauxBold := false
	for _, pageBolds := range settings.TextFauxBold {
		if len(pageBolds) != 0 {
			hasFauxBold = true
			break
		}
	}
	needsImageWalk := settings.ExactImages || hasRawImages
	if !needsImageWalk && !settings.ExactText && !hasFauxBold && len(settings.ImageClips) == 0 && !nativeWrite && len(settings.Information) == 0 && settings.Optimization.Compression.Mode == pdfgo.CompressionUnchanged {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.Copy(outputWriter{ctx: ctx, writer: writer}, io.NewSectionReader(source, 0, size))
		if err == nil && n != size {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	reader, err := pdfgo.NewReader(source, size)
	if err != nil {
		return err
	}
	defer reader.Close()
	if !needsImageWalk && !settings.ExactText && !hasFauxBold && len(settings.ImageClips) == 0 && !nativeWrite {
		if len(settings.Information) != 0 {
			_, err := reader.RewriteTo(ctx, writer, pdfgo.RewriteOptions{Information: settings.Information, Optimization: settings.Optimization})
			return err
		}
		_, err := reader.OptimizeTo(ctx, writer, settings.Optimization)
		return err
	}
	images := make(map[pdfgo.Reference]image.Image)
	rawImages := make(map[pdfgo.Reference]*RawImage)
	links := make(map[pdfgo.AnnotationLocation]pdfgo.LinkRegion)
	texts := make(map[pdfgo.Reference][]pdfgo.TextReplacement)
	references := make([]pdfgo.Reference, len(settings.Images))
	pages := 0
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		if index >= len(settings.Images) {
			return fmt.Errorf("unexpected PDF output page")
		}
		pages++
		references[index] = page.Reference
		if nativeWrite {
			annotations, err := page.AnnotationsContext(ctx)
			if err != nil {
				return err
			}
			expected := settings.Links[index]
			if len(annotations) != len(expected) {
				return fmt.Errorf("PDF output link count differs")
			}
			for i, link := range expected {
				if annotations[i].Subtype != pdfgo.Name("Link") {
					return fmt.Errorf("unexpected PDF output annotation")
				}
				if link != nil {
					links[pdfgo.AnnotationLocation{Page: page.Reference, Index: i}] = *link
				}
			}
		}
		if !needsImageWalk && !settings.ExactText {
			return nil
		}
		value, err := reader.Resolve(page.Resources["XObject"])
		if err != nil {
			return err
		}
		resources, _ := value.(pdfgo.Dictionary)
		var content bytes.Buffer
		if _, err := page.WriteContent(ctx, &content); err != nil {
			return err
		}
		position := 0
		textCount := 0
		if err := pdfgo.WalkOperations(ctx, content.Bytes(), func(operation pdfgo.Operation) error {
			if operation.Operator == "BT" {
				textCount++
			}
			if !needsImageWalk {
				return nil
			}
			if operation.Operator != "Do" {
				return nil
			}
			if len(operation.Operands) != 1 || position >= len(settings.Images[index]) {
				return fmt.Errorf("unexpected PDF output image")
			}
			name, ok := operation.Operands[0].(pdfgo.Name)
			if !ok {
				return fmt.Errorf("invalid PDF output image name")
			}
			ref, ok := resources[name].(pdfgo.Reference)
			if !ok {
				return fmt.Errorf("invalid PDF output image reference")
			}
			original := settings.Images[index][position]
			var raw *RawImage
			if index < len(settings.RawImages) && position < len(settings.RawImages[index]) {
				raw = settings.RawImages[index][position]
			}
			placeholder := index < len(settings.Placeholders) && position < len(settings.Placeholders[index]) && settings.Placeholders[index][position]
			position++
			if original == nil && raw == nil {
				return nil
			}
			pdfImg, err := reader.ReadImage(ref)
			if err != nil {
				return err
			}
			if raw != nil {
				if placeholder {
					if pdfImg.Width <= 0 || pdfImg.Width > 2 || pdfImg.Height != 1 {
						return fmt.Errorf("PDF output placeholder image dimensions differ")
					}
				} else if pdfImg.Width != raw.Width || pdfImg.Height != raw.Height {
					return fmt.Errorf("PDF output image dimensions differ")
				}
				if previous := rawImages[ref]; previous != nil && previous.Key != raw.Key {
					return fmt.Errorf("PDF output image reference conflicts")
				}
				if images[ref] != nil {
					return fmt.Errorf("PDF output image reference conflicts")
				}
				rawImages[ref] = raw
				return nil
			}
			if placeholder {
				if pdfImg.Width <= 0 || pdfImg.Width > 2 || pdfImg.Height != 1 {
					return fmt.Errorf("PDF output placeholder image dimensions differ")
				}
			} else if pdfImg.Width != original.Bounds().Dx() || pdfImg.Height != original.Bounds().Dy() {
				return fmt.Errorf("PDF output image dimensions differ")
			}
			if previous := images[ref]; previous != nil && previous != original {
				return fmt.Errorf("PDF output image reference conflicts")
			}
			if rawImages[ref] != nil {
				return fmt.Errorf("PDF output image reference conflicts")
			}
			images[ref] = original
			return nil
		}); err != nil {
			return err
		}
		if needsImageWalk && position != len(settings.Images[index]) {
			return fmt.Errorf("PDF output image missing")
		}
		if settings.ExactText {
			if index >= len(settings.Text) || textCount != settings.Text[index].Count {
				return fmt.Errorf("PDF output text count differs")
			}
			if len(settings.Text[index].Replacements) != 0 {
				texts[page.Reference] = settings.Text[index].Replacements
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if pages != len(settings.Images) {
		return fmt.Errorf("PDF output page missing")
	}
	activeReader := reader
	if len(rawImages) != 0 {
		patchedSource, patchedSize, err := patchRawImagesIncremental(ctx, source, size, reader, rawImages)
		if err != nil {
			return err
		}
		patchedReader, err := pdfgo.NewReader(patchedSource, patchedSize)
		if err != nil {
			return err
		}
		defer patchedReader.Close()
		activeReader = patchedReader
	}
	lossy := settings.Optimization.Compression.Mode == pdfgo.CompressionLossy
	decoder := &exportImageDecoder{ctx: ctx, lossy: lossy}
	defer decoder.clear()
	options := pdfgo.RewriteOptions{Information: settings.Information, Images: images, ImageOptions: pdfgo.ImageWriteOptions{PreblendBinary: !lossy}, LinkRegions: links, TextReplacements: texts, Optimization: settings.Optimization}
	pageClips := make(map[pdfgo.Reference][]string, len(references))
	pageFauxBolds := make(map[pdfgo.Reference][]TextFauxBold, len(references))
	for i, ref := range references {
		if i < len(settings.ImageClips) {
			pageClips[ref] = settings.ImageClips[i]
		}
		if i < len(settings.TextFauxBold) && len(settings.TextFauxBold[i]) != 0 {
			pageFauxBolds[ref] = settings.TextFauxBold[i]
		}
	}
	options.TransformContent = func(ctx context.Context, page *pdfgo.Page, data []byte) ([]byte, error) {
		decoder.clear()
		changed := false
		clipped, err := clipPageImages(ctx, data, pageClips[page.Reference])
		if err != nil {
			return nil, err
		}
		if clipped != nil {
			data = clipped
			changed = true
		}
		if bolds := pageFauxBolds[page.Reference]; len(bolds) != 0 {
			bolded, err := applyFauxBoldText(ctx, data, bolds)
			if err != nil {
				return nil, err
			}
			if bolded != nil {
				data = bolded
				changed = true
			}
		}
		compact, err := pdfgo.CompactText(ctx, data)
		if err != nil {
			return nil, err
		}
		if compact != nil {
			return compact, nil
		}
		if changed {
			return data, nil
		}
		return nil, nil
	}
	if nativeWrite {
		if err := settings.PrepareRewrite(ctx, activeReader, references, &options); err != nil {
			return err
		}
	}
	for ref, original := range options.Images {
		if err := ctx.Err(); err != nil {
			return err
		}
		if original == nil {
			return fmt.Errorf("PDF output image pixel bounds differ")
		}
		options.Images[ref] = &lazyExportImage{decoder: decoder, source: original}
	}
	_, err = activeReader.RewriteTo(ctx, writer, options)
	decoder.clear()
	if decodeErr := decoder.failure(); decodeErr != nil {
		return decodeErr
	}
	return err
}

// outputWriter 在分块输出前后检查取消，不改变底层写入错误
type outputWriter struct {
	ctx    context.Context
	writer io.Writer
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

// decoderSlot 保存当前驻留的单张已解码图像及其快速访问视图
type decoderSlot struct {
	target  *lazyExportImage
	pixels  image.Image
	rgba64  image.RGBA64Image
	nrgba   *image.NRGBA
	nrgba64 interface{ NRGBA64At(int, int) color.NRGBA64 }
}

// exportImageDecoder 控制PDF写出期间的单槽懒解码，确保同时最多驻留单张图像像素
type exportImageDecoder struct {
	mu    sync.Mutex
	ctx   context.Context
	lossy bool
	slot  atomic.Pointer[decoderSlot]
	err   error
}

// load 按需解码目标图像并立即丢弃上一张图像的未压缩像素缓冲，热路径零加锁
// 入参: target 待访问的懒加载图像代理
// 返回: *decoderSlot 当前槽位快照，解码失败时返回nil
func (d *exportImageDecoder) load(target *lazyExportImage) *decoderSlot {
	if s := d.slot.Load(); s != nil && s.target == target {
		return s
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil
	}
	if s := d.slot.Load(); s != nil && s.target == target {
		return s
	}
	d.slot.Store(nil)
	pixels, err := decodeExportImage(d.ctx, target.source, d.lossy)
	if err != nil {
		d.err = err
		return nil
	}
	s := &decoderSlot{target: target, pixels: pixels}
	s.rgba64, _ = pixels.(image.RGBA64Image)
	s.nrgba, _ = pixels.(*image.NRGBA)
	s.nrgba64, _ = pixels.(interface{ NRGBA64At(int, int) color.NRGBA64 })
	d.slot.Store(s)
	return s
}

// clear 释放当前槽位驻留的未压缩图像像素引用
func (d *exportImageDecoder) clear() {
	d.mu.Lock()
	d.slot.Store(nil)
	d.mu.Unlock()
}

// failure 读取解码阶段记录的首个错误
// 返回: error 解码错误
func (d *exportImageDecoder) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// lazyExportImage 延迟解码待替换PDF图像，编码完成后随槽位切换自动释放
type lazyExportImage struct {
	decoder *exportImageDecoder
	source  image.Image
}

// Unwrap 触发单次按需解码并返回底层原生图像，消除逐像素接口与锁开销
// 返回: image.Image 底层原生图像，解码失败时返回nil
func (l *lazyExportImage) Unwrap() image.Image {
	s := l.decoder.load(l)
	if s == nil {
		return nil
	}
	pixels := s.pixels
	for {
		unwrapper, ok := pixels.(interface{ Unwrap() image.Image })
		if !ok {
			return pixels
		}
		next := unwrapper.Unwrap()
		if next == nil || next == pixels {
			return pixels
		}
		pixels = next
	}
}

// Bounds 触发单张按需解码并返回像素范围，解码失败时返回空范围以中断编码
// 返回: image.Rectangle 图像像素范围
func (l *lazyExportImage) Bounds() image.Rectangle {
	s := l.decoder.load(l)
	if s == nil {
		return image.Rectangle{}
	}
	return s.pixels.Bounds()
}

// ColorModel 返回当前已解码图像的颜色模型
// 返回: color.Model 颜色模型
func (l *lazyExportImage) ColorModel() color.Model {
	s := l.decoder.load(l)
	if s == nil {
		return color.NRGBAModel
	}
	return s.pixels.ColorModel()
}

// At 返回指定坐标处的原始颜色样本
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 像素颜色
func (l *lazyExportImage) At(x, y int) color.Color {
	s := l.decoder.load(l)
	if s == nil {
		return color.Transparent
	}
	return s.pixels.At(x, y)
}

// RGBA64At 返回指定坐标处的十六位预乘颜色样本
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 十六位预乘颜色
func (l *lazyExportImage) RGBA64At(x, y int) color.RGBA64 {
	s := l.decoder.load(l)
	if s == nil {
		return color.RGBA64{}
	}
	if s.rgba64 != nil {
		return s.rgba64.RGBA64At(x, y)
	}
	r, g, b, a := s.pixels.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}

// NRGBA64At 返回指定坐标处的十六位非预乘颜色样本，常见像素切片走无装箱快路径
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.NRGBA64 十六位非预乘颜色
func (l *lazyExportImage) NRGBA64At(x, y int) color.NRGBA64 {
	s := l.decoder.load(l)
	if s == nil {
		return color.NRGBA64{}
	}
	if s.nrgba != nil {
		if !(image.Point{X: x, Y: y}).In(s.nrgba.Rect) {
			return color.NRGBA64{}
		}
		i := s.nrgba.PixOffset(x, y)
		pix := s.nrgba.Pix[i : i+4 : i+4]
		return color.NRGBA64{R: uint16(pix[0]) * 257, G: uint16(pix[1]) * 257, B: uint16(pix[2]) * 257, A: uint16(pix[3]) * 257}
	}
	if s.nrgba64 != nil {
		return s.nrgba64.NRGBA64At(x, y)
	}
	return baseimage.NRGBA64At(s.pixels, x, y)
}

// multiReaderAt 零拷贝拼接原始PDF数据与尾部增量更新缓冲
type multiReaderAt struct {
	first     io.ReaderAt
	firstSize int64
	second    []byte
}

// ReadAt 按全局偏移读取拼接后的只读数据源
// 入参: p 目标缓冲, off 读取偏移
// 返回: int 实际读取字节数, error 读取错误
func (m multiReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	total := m.firstSize + int64(len(m.second))
	if off >= total {
		return 0, io.EOF
	}
	read := 0
	if off < m.firstSize {
		n := len(p)
		if int64(n) > m.firstSize-off {
			n = int(m.firstSize - off)
		}
		got, err := m.first.ReadAt(p[:n], off)
		read += got
		if err != nil && err != io.EOF {
			return read, err
		}
		if got < n {
			return read, io.ErrUnexpectedEOF
		}
		off += int64(got)
	}
	if read < len(p) && off >= m.firstSize {
		srcOff := int(off - m.firstSize)
		copied := copy(p[read:], m.second[srcOff:])
		read += copied
	}
	if read < len(p) {
		return read, io.EOF
	}
	return read, nil
}

// applyFauxBoldText 为指定的文字对象区间注入Tr=2填充并细描边状态并隔离图形状态
// 入参: ctx 取消上下文, data 页面指令, bolds 伪粗体文字区间列表
// 返回: []byte 更新指令，未变化时为nil, error 解析或取消错误
func applyFauxBoldText(ctx context.Context, data []byte, bolds []TextFauxBold) ([]byte, error) {
	if len(bolds) == 0 {
		return nil, ctx.Err()
	}
	byIndex := make(map[int]TextFauxBold)
	for _, item := range bolds {
		for k := 0; k < item.Count; k++ {
			byIndex[item.First+k] = item
		}
	}
	if len(byIndex) == 0 {
		return nil, ctx.Err()
	}
	var output bytes.Buffer
	position, btIndex := 0, 0
	inBold := false
	err := pdfgo.WalkOperations(ctx, data, func(op pdfgo.Operation) error {
		start := int(op.Offset)
		end := start + len(op.Operator)
		if op.Operator == "BT" {
			if item, ok := byIndex[btIndex]; ok {
				output.Write(data[position:start])
				output.WriteString(" q ")
				output.WriteString(formatPDFNumber(item.LineWidth))
				output.WriteString(" w 1 j 1 J ")
				if item.Color.R == item.Color.G && item.Color.G == item.Color.B {
					output.WriteString(formatPDFNumber(float64(item.Color.R) / 255))
					output.WriteString(" G ")
				} else {
					output.WriteString(formatPDFNumber(float64(item.Color.R) / 255))
					output.WriteByte(' ')
					output.WriteString(formatPDFNumber(float64(item.Color.G) / 255))
					output.WriteByte(' ')
					output.WriteString(formatPDFNumber(float64(item.Color.B) / 255))
					output.WriteString(" RG ")
				}
				output.Write(data[start:end])
				output.WriteString(" 2 Tr ")
				position = end
				inBold = true
			}
			btIndex++
			return nil
		}
		if op.Operator == "ET" && inBold {
			output.Write(data[position:end])
			output.WriteString(" Q ")
			position = end
			inBold = false
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if position == 0 {
		return nil, ctx.Err()
	}
	output.Write(data[position:])
	return output.Bytes(), ctx.Err()
}

// formatPDFNumber 格式化PDF实数，避免科学计数法
// 入参: v 实数值
// 返回: string PDF数字文本
func formatPDFNumber(v float64) string {
	s := strconv.FormatFloat(v, 'f', 6, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

// patchRawImagesIncremental 通过标准PDF增量更新把占位图像对象替换为JPX或JBIG2原始流
// 入参: ctx 取消上下文, source 原始中间PDF, size 原始长度, reader 已解析阅读器, rawImages 待替换流映射
// 返回: io.ReaderAt 拼接后PDF源, int64 总长度, error 解析或写入错误
func patchRawImagesIncremental(ctx context.Context, source io.ReaderAt, size int64, reader *pdfgo.Reader, rawImages map[pdfgo.Reference]*RawImage) (io.ReaderAt, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	tailSize := min(size, int64(1024))
	tail := make([]byte, tailSize)
	if _, err := source.ReadAt(tail, size-tailSize); err != nil && err != io.EOF {
		return nil, 0, err
	}
	idx := bytes.LastIndex(tail, []byte("startxref"))
	if idx < 0 {
		return nil, 0, fmt.Errorf("missing startxref in intermediate PDF")
	}
	fields := strings.Fields(string(tail[idx+len("startxref"):]))
	if len(fields) == 0 {
		return nil, 0, fmt.Errorf("invalid startxref in intermediate PDF")
	}
	prevXref, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || prevXref < 0 {
		return nil, 0, fmt.Errorf("invalid startxref offset")
	}
	trailerSize, ok := reader.Trailer["Size"].(pdfgo.Integer)
	if !ok || trailerSize <= 0 {
		return nil, 0, fmt.Errorf("invalid trailer Size in intermediate PDF")
	}
	rootRef, ok := reader.Trailer["Root"].(pdfgo.Reference)
	if !ok {
		return nil, 0, fmt.Errorf("invalid trailer Root in intermediate PDF")
	}
	refs := make([]pdfgo.Reference, 0, len(rawImages))
	for ref := range rawImages {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b pdfgo.Reference) int {
		if c := cmp.Compare(a.Number, b.Number); c != 0 {
			return c
		}
		return cmp.Compare(a.Generation, b.Generation)
	})
	var delta bytes.Buffer
	delta.WriteByte('\n')
	offsets := make([]int64, len(refs))
	for i, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		raw := rawImages[ref]
		offsets[i] = size + int64(delta.Len())
		fmt.Fprintf(&delta, "%d %d obj\n<< /Type /XObject /Subtype /Image /Width %d /Height %d", ref.Number, ref.Generation, raw.Width, raw.Height)
		if raw.BitsPerComponent > 0 {
			fmt.Fprintf(&delta, " /BitsPerComponent %d", raw.BitsPerComponent)
		}
		if raw.ColorSpace != "" {
			fmt.Fprintf(&delta, " /ColorSpace /%s", raw.ColorSpace)
		}
		fmt.Fprintf(&delta, " /Filter /%s /Length %d >>\nstream\n", raw.Filter, len(raw.Data))
		delta.Write(raw.Data)
		delta.WriteString("\nendstream\nendobj\n")
	}
	xrefOffset := size + int64(delta.Len())
	delta.WriteString("xref\n")
	for i, ref := range refs {
		fmt.Fprintf(&delta, "%d 1\n%010d %05d n \n", ref.Number, offsets[i], ref.Generation)
	}
	fmt.Fprintf(&delta, "trailer\n<< /Size %d /Root %d %d R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", trailerSize, rootRef.Number, rootRef.Generation, prevXref, xrefOffset)
	data := delta.Bytes()
	return multiReaderAt{first: source, firstSize: size, second: data}, size + int64(len(data)), nil
}

// clipPageImages 在图像局部坐标中应用独立裁剪，不解码或重新采样图像
// 入参: ctx 取消上下文, data 页面指令, clips 各次图像绘制的局部裁剪路径
// 返回: []byte 更新指令，未变化时为nil, error 解析或取消错误
func clipPageImages(ctx context.Context, data []byte, clips []string) ([]byte, error) {
	if len(clips) == 0 {
		return nil, ctx.Err()
	}
	var output bytes.Buffer
	position, previous, index := 0, 0, 0
	err := pdfgo.WalkOperations(ctx, data, func(op pdfgo.Operation) error {
		end := int(op.Offset) + len(op.Operator)
		if op.Operator == "Do" {
			if index < len(clips) && clips[index] != "" {
				output.Write(data[position:previous])
				output.WriteString(" q ")
				output.WriteString(clips[index])
				output.WriteString(" W n ")
				output.Write(data[previous:end])
				output.WriteString(" Q ")
				position = end
			}
			index++
		}
		previous = end
		return nil
	})
	if err != nil {
		return nil, err
	}
	if position == 0 {
		return nil, ctx.Err()
	}
	output.Write(data[position:])
	return output.Bytes(), ctx.Err()
}

// decodeExportImage 单次临时解码单张待导出图像并校验范围，有损模式降至8位以启用下游压缩，不在源惰性对象上保留像素缓存
// 入参: ctx 取消上下文, original 源图像或惰性图像, lossy 是否处于有损压缩模式
// 返回: image.Image 解码像素, error 解码、范围或取消错误
func decodeExportImage(ctx context.Context, original image.Image, lossy bool) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pixels, err := baseimage.DecodeTransientContext(ctx, original)
	if err != nil {
		return nil, err
	}
	if pixels == nil || pixels.Bounds() != original.Bounds() {
		return nil, fmt.Errorf("PDF output image pixel bounds differ")
	}
	if lossy {
		return normalizeLossyExportImage(ctx, pixels)
	}
	return pixels, nil
}

// normalizeLossyExportImage 将高精度或预乘图像规范化为8位非预乘格式，避免PDF写出阶段升级16位绕过有损压缩
// 入参: ctx 取消上下文, pixels 已解码像素
// 返回: image.Image 8位图像, error 取消错误
func normalizeLossyExportImage(ctx context.Context, pixels image.Image) (image.Image, error) {
	switch src := pixels.(type) {
	case *image.NRGBA, *image.Gray, *image.YCbCr, *image.CMYK:
		return pixels, ctx.Err()
	case *image.Gray16:
		b := src.Bounds()
		w, h := b.Dx(), b.Dy()
		dst := image.NewGray(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			srcRow := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			dstRow := dst.Pix[y*dst.Stride:]
			for x := 0; x < w; x++ {
				dstRow[x] = srcRow[x*2]
			}
		}
		return dst, ctx.Err()
	case *image.NRGBA64:
		b := src.Bounds()
		w, h := b.Dx(), b.Dy()
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			srcRow := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			dstRow := dst.Pix[y*dst.Stride:]
			for x := 0; x < w; x++ {
				si, di := x*8, x*4
				dstRow[di], dstRow[di+1], dstRow[di+2], dstRow[di+3] = srcRow[si], srcRow[si+2], srcRow[si+4], srcRow[si+6]
			}
		}
		return dst, ctx.Err()
	}
	b := pixels.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dstRow := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			if x&4095 == 4095 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			c := baseimage.NRGBAAt(pixels, b.Min.X+x, b.Min.Y+y)
			di := x * 4
			dstRow[di], dstRow[di+1], dstRow[di+2], dstRow[di+3] = c.R, c.G, c.B, c.A
		}
	}
	return dst, ctx.Err()
}
