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
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/ascii85"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"strconv"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/pdf"
	"github.com/tdewolff/canvas/renderers/ps"
	"github.com/tdewolff/canvas/renderers/svg"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// canvasEPSWriter 在EPS头部写入本库制作软件，不改动绘图内容
type canvasEPSWriter struct {
	io.Writer
}

// epsImageRenderer 在EPS输出端优化图片，不修改页面及渲染缓存
type epsImageRenderer struct {
	*ps.PS
	writer  io.Writer
	options CompressionOptions
	ctx     context.Context
	err     error
}

// RenderSVG 使用canvas输出SVG，按模式保留资源和对象分组
// 入参: r 渲染器, page 页面内容, writer 输出流, mode 资源模式
// 返回: SVGResources 外部资源, error 错误信息
func (CanvasBackend) RenderSVG(r *Renderer, page *PageContent, writer io.Writer, mode SVGMode) (SVGResources, error) {
	switch mode {
	case SVGExternalFonts:
		return r.renderSVGResources(page, writer, false, false)
	case SVGExternalResources:
		return r.renderSVGResources(page, writer, true, false)
	case SVGObjects:
		return r.renderSVGResources(page, writer, true, true)
	case SVGEmbedded:
		c, err := r.renderCanvasPage(page)
		if err != nil {
			return SVGResources{}, err
		}
		buffer := bufio.NewWriter(writer)
		renderer := &svgResourceRenderer{SVG: svg.New(buffer, c.W, c.H, nil), renderer: r, writer: buffer, embeddedFonts: true}
		io.WriteString(buffer, svgCreatorMetadata)
		io.WriteString(buffer, `<g style="image-orientation:none">`)
		c.RenderTo(renderer)
		if renderer.err != nil {
			return SVGResources{}, renderer.err
		}
		io.WriteString(buffer, `</g>`)
		if err := renderer.Close(); err != nil {
			return SVGResources{}, err
		}
		return SVGResources{}, buffer.Flush()
	default:
		return SVGResources{}, fmt.Errorf("unsupported SVG mode %d", mode)
	}
}

// RenderEPS 使用canvas输出单页EPS
// 入参: r 渲染器, page 页面内容, writer 输出流
// 返回: error 错误信息
func (CanvasBackend) RenderEPS(r *Renderer, page *PageContent, writer io.Writer) error {
	c, err := r.renderCanvasPage(page)
	if err != nil {
		return err
	}
	options := ps.DefaultOptions
	options.Format = ps.EncapsulatedPostScript
	buffer := bufio.NewWriter(writer)
	renderer := ps.New(canvasEPSWriter{buffer}, c.W, c.H, &options)
	if r.Compression.Mode == CompressionUnchanged {
		c.RenderTo(renderer)
	} else {
		optimized := &epsImageRenderer{PS: renderer, writer: buffer, options: r.Compression, ctx: r.outputContext()}
		c.RenderTo(optimized)
		if optimized.err != nil {
			return optimized.err
		}
	}
	if err := renderer.Close(); err != nil {
		return err
	}
	return buffer.Flush()
}

// Write 写入EPS片段并替换独立的制作软件声明
// 入参: data EPS片段
// 返回: int 已处理字节数, error 写入错误
func (w canvasEPSWriter) Write(data []byte) (int, error) {
	if bytes.Equal(data, []byte("%%Creator: tdewolff/canvas\n")) {
		if _, err := io.WriteString(w.Writer, "%%Creator: "+ofdCreator+"\n"); err != nil {
			return 0, err
		}
		return len(data), nil
	}
	return w.Writer.Write(data)
}

// RenderPDF 使用canvas输出PDF，保留导航与文字并分块保存中间输出
// 入参: r 渲染器, pages 页面列表, writer 输出流, progress 页面处理进度
// 返回: error 错误信息
func (CanvasBackend) RenderPDF(r *Renderer, pages []RenderDocumentPage, writer io.Writer, progress func(int, int) error) error {
	ctx := r.outputContext()
	if err := ctx.Err(); err != nil {
		return err
	}
	view := *r
	view.pageContext = ctx
	view.compactStrokes = true
	r = &view
	if len(pages) == 0 {
		return fmt.Errorf("no pages found")
	}
	doc := r.Reader.doc
	navigation := &pdfNavigation{}
	if !r.printing {
		var err error
		navigation, err = newPDFNavigation(r, doc, pages)
		if err != nil {
			return err
		}
	}
	var buf editorSnapshotBuffer
	settings := pdf.DefaultOptions
	settings.Compress = r.Compression.Mode == CompressionUnchanged
	p := pdf.New(&buf, pages[0].Box.W, pages[0].Box.H, &settings)
	renderer := &pdfRenderer{PDF: p, glyphPaths: *resourceCache[*canvas.Path, *canvas.Path](r.Reader.resources, 16<<20), images: make([][]image.Image, 1), text: make([]codecpdf.TextPage, 1), navigation: navigation}
	defer func() {
		if renderer.imageError != nil {
			r.canvasState().images = *resourceCache[*EncodedImage, image.Image](r.Reader.resources, imageCacheLimit)
		}
	}()
	var info DocInfo
	if docInfo, err := r.Reader.DocInfo(); err == nil {
		info = *docInfo
	}
	renderer.information = make(pdfgo.Dictionary)
	for _, field := range []struct {
		key   pdfgo.Name
		value string
	}{{"Title", info.Title}, {"Subject", info.Subject}, {"Author", info.Author}, {"Creator", ofdCreator}} {
		if field.value == "" {
			continue
		}
		encoded, err := pdfgo.EncodeTextString(field.value)
		if err != nil {
			return err
		}
		renderer.information[field.key] = encoded
	}
	for i, page := range pages {
		content, err := page.ReadContent(ctx, r.Reader)
		if err != nil {
			return fmt.Errorf("failed to read page %d: %w", i+1, err)
		}
		if !r.printing {
			if err := navigation.addPage(r, i, content, pages); err != nil {
				return fmt.Errorf("failed to read page %d navigation: %w", i+1, err)
			}
		}
		if i > 0 {
			p.NewPage(page.Box.W, page.Box.H)
			renderer.images = append(renderer.images, nil)
			renderer.text = append(renderer.text, codecpdf.TextPage{})
		}
		if !r.printing {
			navigation.apply(p, i)
		}
		if err := r.renderCanvasPageToContext(canvas.NewContext(renderer), content, !r.TransparentBackground); err != nil {
			return fmt.Errorf("failed to render page %d: %w", i+1, err)
		}
		if renderer.imageError != nil {
			return fmt.Errorf("failed to render page %d: %w", i+1, renderer.imageError)
		}
		pages[i].Content = nil
		if err := ctx.Err(); err != nil {
			return err
		}
		if progress != nil {
			if err := progress(i+1, len(pages)); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if doc != nil && !r.printing {
		if err := navigation.addOutlines(doc.Outlines.OutlineElem, 0, navigation.bookmarks, navigation.pageIndex, pages); err != nil {
			return err
		}
	}
	for index, outline := range navigation.Outline {
		p.AddOutline(strconv.Itoa(index), outline.Level, 0)
	}
	if err := p.Close(); err != nil {
		return err
	}
	options := pdfgo.OptimizeOptions{Compression: r.Compression}
	if r.Compression.Mode != CompressionUnchanged && progress != nil {
		options.OnProgress = func(_ string, _, _ int) error {
			return progress(len(pages), len(pages))
		}
	}
	return renderer.writePDF(r.outputContext(), &buf, buf.size, writer, options)
}

// RenderImage 写出Flate或更小的JPEG图片，保留显示尺寸与变换
// 入参: img 图片, matrix 绘制变换
func (r *epsImageRenderer) RenderImage(img image.Image, matrix canvas.Matrix) {
	if r.err != nil {
		return
	}
	if dpi := r.options.ImageDPI(); dpi > 0 {
		original := img.Bounds().Size()
		target := compressionImageSize(math.Hypot(matrix[0][0], matrix[1][0])*float64(original.X), math.Hypot(matrix[0][1], matrix[1][1])*float64(original.Y), dpi)
		img, r.err = pdfgo.ResizeImage(r.ctx, img, target)
		if r.err != nil {
			return
		}
		size := img.Bounds().Size()
		if size.X > 0 && size.Y > 0 {
			matrix = matrix.Scale(float64(original.X)/float64(size.X), float64(original.Y)/float64(size.Y))
		}
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	pixels := make([]byte, width*3)
	var encoded bytes.Buffer
	compressor, _ := zlib.NewWriterLevel(&encoded, zlib.BestCompression)
	opaque := true
	native, _ := img.(image.RGBA64Image)
	for y := 0; y < height; y++ {
		if r.err = r.ctx.Err(); r.err != nil {
			return
		}
		clear(pixels)
		for x := 0; x < width; x++ {
			if x&4095 == 0 {
				if r.err = r.ctx.Err(); r.err != nil {
					return
				}
			}
			var red, green, blue, alpha uint32
			if native != nil {
				pixel := native.RGBA64At(bounds.Min.X+x, bounds.Min.Y+y)
				red, green, blue, alpha = uint32(pixel.R), uint32(pixel.G), uint32(pixel.B), uint32(pixel.A)
			} else {
				red, green, blue, alpha = img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			}
			if alpha != 65535 {
				opaque = false
			}
			if alpha == 65535 {
				offset := x * 3
				pixels[offset], pixels[offset+1], pixels[offset+2] = byte(red>>8), byte(green>>8), byte(blue>>8)
			} else if alpha != 0 {
				offset := x * 3
				pixels[offset], pixels[offset+1], pixels[offset+2] = byte(red*65535/alpha>>8), byte(green*65535/alpha>>8), byte(blue*65535/alpha>>8)
			}
		}
		if r.err = r.ctx.Err(); r.err != nil {
			return
		}
		if _, r.err = compressor.Write(pixels); r.err != nil {
			return
		}
	}
	if r.err = compressor.Close(); r.err != nil {
		return
	}
	data := encoded.Bytes()
	filter := "FlateDecode"
	if opaque && r.options.Mode == CompressionLossy {
		var candidate bytes.Buffer
		if r.err = jpeg.Encode(&candidate, img, &jpeg.Options{Quality: r.options.ImageQuality()}); r.err != nil {
			return
		}
		optimized, err := pdfgo.OptimizeJPEG(r.ctx, candidate.Bytes())
		if err != nil {
			r.err = err
			return
		}
		if len(optimized) < len(data) {
			data, filter = optimized, "DCTDecode"
		}
	}
	if r.err = r.ctx.Err(); r.err != nil {
		return
	}
	m := matrix.Scale(float64(width), float64(height))
	_, r.err = fmt.Fprintf(r.writer, " gsave /DeviceRGB setcolorspace [%s %s %s %s %s %s] concat <</ImageType 1 /BitsPerComponent 8 /Decode [0 1 0 1 0 1] /Interpolate true /Width %d /Height %d /ImageMatrix [%d 0 0 -%d 0 %d] /DataSource currentfile /ASCII85Decode filter /%s filter>>image\n", ofdNumber(m[0][0]), ofdNumber(m[1][0]), ofdNumber(m[0][1]), ofdNumber(m[1][1]), ofdNumber(m[0][2]), ofdNumber(m[1][2]), width, height, width, height, height, filter)
	if r.err != nil {
		return
	}
	writer := ascii85.NewEncoder(r.writer)
	if _, r.err = writer.Write(data); r.err != nil {
		return
	}
	if r.err = writer.Close(); r.err != nil {
		return
	}
	_, r.err = io.WriteString(r.writer, "~>\n grestore")
}
