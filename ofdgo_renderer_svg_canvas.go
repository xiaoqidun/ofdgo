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
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"html"
	"image"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/tdewolff/canvas"
	canvasimage "github.com/tdewolff/canvas/image"
	"github.com/tdewolff/canvas/renderers/svg"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	"github.com/xiaoqidun/pdfgo"
)

// svgCreatorMetadata 记录SVG制作软件，不参与页面绘制
const svgCreatorMetadata = `<metadata>` + creatorXMP + `</metadata>`

// svgResourceRenderer 分离外部资源的SVG渲染器
type svgResourceRenderer struct {
	*svg.SVG
	renderer      *Renderer
	writer        io.Writer
	fonts         []SVGFont
	images        []SVGImage
	imageNames    map[svgImageKey]string
	seen          map[string]bool
	styles        strings.Builder
	err           error
	objects       map[*GraphicObject]string
	objectStack   []svgObjectGroup
	embeddedFonts bool
	pathData      []byte
}

// RenderPath 直接序列化纯色填充轮廓，其余画笔保留后端处理
// 入参: path 几何路径, style 绘制样式, m 页面变换
func (s *svgResourceRenderer) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if !style.HasFill() || !style.Fill.IsColor() || style.HasStroke() {
		s.SVG.RenderPath(path, style, m)
		return
	}
	for data := path.Data(); len(data) != 0; {
		switch data[0] {
		case canvas.MoveToCmd, canvas.LineToCmd, canvas.CloseCmd:
			data = data[4:]
		case canvas.QuadToCmd:
			data = data[6:]
		case canvas.CubeToCmd:
			data = data[8:]
		default:
			s.SVG.RenderPath(path, style, m)
			return
		}
	}
	_, height := s.Size()
	data := path.Copy().Transform(canvas.Identity.ReflectYAbout(height / 2).Mul(m)).Data()
	output := append(s.pathData[:0], `<path d="`...)
	for len(data) != 0 {
		command, count := byte('M'), 4
		switch data[0] {
		case canvas.LineToCmd:
			command = 'L'
		case canvas.CloseCmd:
			command = 'Z'
		case canvas.QuadToCmd:
			command, count = 'Q', 6
		case canvas.CubeToCmd:
			command, count = 'C', 8
		}
		output = append(output, command)
		if command != 'Z' {
			for _, value := range data[1 : count-1] {
				output = strconv.AppendFloat(output, value, 'g', canvas.Precision, 64)
				output = append(output, ' ')
			}
		}
		data = data[count:]
	}
	output = append(output, '"')
	if style.Fill.Color != canvas.Black {
		output = fmt.Appendf(output, ` fill="%s"`, canvas.CSSColor(style.Fill.Color))
	}
	if style.FillRule == canvas.EvenOdd {
		output = append(output, ` fill-rule="evenodd"`...)
	}
	output = append(output, '/', '>')
	if s.err == nil {
		_, s.err = s.writer.Write(output)
	}
	s.pathData = output
}

// svgImageKey 按原图及输出精度复用图片编码，避免不同显示尺寸共用低清结果
type svgImageKey struct {
	image image.Image
	size  image.Point
}

// svgResourceCacheKey 区分共享的图片编码缓存
type svgResourceCacheKey struct{}

// svgImageResourceKey 按像素、压缩策略及目标尺寸区分编码结果
type svgImageResourceKey struct {
	digest  [32]byte
	options CompressionOptions
	target  image.Point
}

// svgObjectGroup 记录编辑分组的稳定路径，不把底纹内部绘制当作成员
type svgObjectGroup struct {
	path      string
	next      int
	composite bool
}

// svgImageResources 获取派生渲染器共享的图片编码缓存
// 返回: *renderCache 图片编码缓存
func (r *Renderer) svgImageResources() *renderCache[svgImageResourceKey, SVGImage] {
	key := svgResourceCacheKey{}
	if state, ok := r.sharedBackendStates[key]; ok {
		return state.(*renderCache[svgImageResourceKey, SVGImage])
	}
	cache := resourceCache[svgImageResourceKey, SVGImage](r.Reader.resources, 4<<20)
	r.sharedBackendStates[key] = cache
	return cache
}

// svgPixelDigest 对标准像素缓冲计算完整摘要，不展开延迟图像
// 入参: ctx 取消上下文, img 原始图像, limit 可参与缓存的像素字节上限
// 返回: [32]byte 像素摘要, bool 是否为支持的像素布局
func svgPixelDigest(ctx context.Context, img image.Image, limit int) ([32]byte, bool) {
	var pixels []byte
	var stride, kind int
	switch source := img.(type) {
	case *image.NRGBA:
		pixels, stride, kind = source.Pix, source.Stride, 1
	case *image.NRGBA64:
		pixels, stride, kind = source.Pix, source.Stride, 2
	case *image.RGBA:
		pixels, stride, kind = source.Pix, source.Stride, 3
	case *image.RGBA64:
		pixels, stride, kind = source.Pix, source.Stride, 4
	default:
		return [32]byte{}, false
	}
	if len(pixels) > limit {
		return [32]byte{}, false
	}
	bounds := img.Bounds()
	var header [48]byte
	for i, value := range [...]int{kind, stride, bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y} {
		binary.LittleEndian.PutUint64(header[i*8:], uint64(value))
	}
	hash := sha256.New()
	hash.Write(header[:])
	for len(pixels) > 0 {
		if ctx.Err() != nil {
			return [32]byte{}, false
		}
		count := min(len(pixels), 256<<10)
		hash.Write(pixels[:count])
		pixels = pixels[count:]
	}
	var digest [32]byte
	hash.Sum(digest[:0])
	return digest, true
}

// RenderImage 按输出策略编码图片，分离资源时保持尺寸和变换
// 入参: img 图片对象, m 变换矩阵
func (s *svgResourceRenderer) RenderImage(img image.Image, m canvas.Matrix) {
	if s.err != nil {
		return
	}
	ctx := context.Background()
	if s.renderer != nil {
		ctx = s.renderer.outputContext()
	}
	if s.err = ctx.Err(); s.err != nil {
		return
	}
	var target image.Point
	if s.renderer != nil && s.renderer.Compression.ImageDPI() > 0 {
		bounds := img.Bounds()
		target = compressionImageSize(math.Hypot(m[0][0], m[1][0])*float64(bounds.Dx()), math.Hypot(m[0][1], m[1][1])*float64(bounds.Dy()), s.renderer.Compression.ImageDPI())
	}
	key := svgImageKey{image: img, size: target}
	name, ok := s.imageNames[key]
	var resource SVGImage
	if !ok {
		resource, s.err = s.imageResource(ctx, img, target)
		if s.err != nil {
			return
		}
		if s.imageNames != nil {
			name = resource.Name
			s.imageNames[key] = name
			if !s.seen[name] {
				s.seen[name] = true
				s.images = append(s.images, resource)
			}
		}
	}
	_, height := s.Size()
	size := img.Bounds().Size()
	m = m.Translate(0, float64(size.Y))
	matrix := Matrix{a: m[0][0], b: -m[1][0], c: -m[0][1], d: m[1][1], e: m[0][2], f: height - m[1][2]}
	writer := imageOutput{ctx: ctx, writer: s.writer}
	if _, s.err = fmt.Fprintf(writer, `<image transform="matrix(%s)" width="%d" height="%d" xlink:href="`, matrix.String(), size.X, size.Y); s.err != nil {
		return
	}
	if s.imageNames != nil {
		if _, s.err = io.WriteString(writer, name); s.err != nil {
			return
		}
	} else {
		if _, s.err = fmt.Fprintf(writer, "data:%s;base64,", resource.MIME); s.err != nil {
			return
		}
		encoder := base64.NewEncoder(base64.StdEncoding, writer)
		if _, err := encoder.Write(resource.Data); err != nil {
			s.err = err
			return
		}
		if err := encoder.Close(); err != nil {
			s.err = err
			return
		}
	}
	_, s.err = io.WriteString(writer, `"/>`)
	if s.err == nil {
		s.err = ctx.Err()
	}
}

// imageResource 复用相同像素的编码结果，不持有源图像或改变输出策略
// 入参: ctx 取消上下文, img 原图, target 压缩像素需求
// 返回: SVGImage 图片资源, error 编码或取消错误
func (s *svgResourceRenderer) imageResource(ctx context.Context, img image.Image, target image.Point) (SVGImage, error) {
	if err := ctx.Err(); err != nil {
		return SVGImage{}, err
	}
	if s.renderer != nil {
		cache := s.renderer.svgImageResources()
		if digest, ok := svgPixelDigest(ctx, img, cache.limit); ok {
			key := svgImageResourceKey{digest: digest, options: s.renderer.Compression, target: target}
			result, err := cache.load(ctx, key, func() (SVGImage, int, error) {
				result, err := s.encodeImageResource(ctx, img, target)
				return result, cap(result.Data) + 256, err
			})
			if s.imageNames != nil && s.renderer.knownSVGResources[result.Name] {
				result.Data = nil
			} else {
				result.Data = bytes.Clone(result.Data)
			}
			return result, err
		}
	}
	result, err := s.encodeImageResource(ctx, img, target)
	if s.imageNames != nil && s.renderer != nil && s.renderer.knownSVGResources[result.Name] {
		result.Data = nil
	}
	return result, err
}

// encodeImageResource 保留原编码或压缩已解码像素，先降采样再编码并响应取消
// 优化失败时回退原图PNG，取消时终止，不改变SVG中的图片尺寸和变换
// 入参: ctx 取消上下文, img 原图, target 压缩像素需求
// 返回: SVGImage 图片资源, error 编码或取消错误
func (s *svgResourceRenderer) encodeImageResource(ctx context.Context, img image.Image, target image.Point) (SVGImage, error) {
	options := CompressionOptions{}
	if s.renderer != nil {
		options = s.renderer.Compression
	}
	resource := SVGImage{MIME: "image/png"}
	if encoded, ok := img.(*canvasimage.Image); ok {
		resource.MIME, resource.Data = encoded.Mimetype, encoded.Bytes
		if options.Mode != CompressionUnchanged {
			data, err := pdfgo.OptimizeImageResource(ctx, resource.Data, options, target)
			if ctx.Err() != nil {
				return SVGImage{}, ctx.Err()
			}
			if err == nil {
				resource.Data = data
			}
		}
	} else {
		if options.Mode != CompressionUnchanged {
			pixels := img
			var err error
			if options.Mode == CompressionLossy {
				pixels, err = pdfgo.ResizeImage(ctx, img, target)
				if err != nil {
					return SVGImage{}, err
				}
			}
			resource.Data, err = pdfgo.EncodeImageResource(ctx, pixels, options)
			if err != nil {
				if ctx.Err() != nil {
					return SVGImage{}, ctx.Err()
				}
				resource.Data = nil
			}
		}
		if resource.Data == nil {
			var buffer bytes.Buffer
			if err := pdfgo.EncodePNG(ctx, &buffer, img); err != nil {
				return SVGImage{}, err
			}
			resource.Data = buffer.Bytes()
		}
	}
	if bytes.HasPrefix(resource.Data, []byte{255, 216}) {
		resource.MIME = "image/jpeg"
	}
	if err := ctx.Err(); err != nil {
		return SVGImage{}, err
	}
	resource.Name = fmt.Sprintf("ofdgo-image-%x", sha256.Sum256(resource.Data))
	return resource, nil
}

// RenderText 绘制文字并记录实际使用的字体资源
// 入参: text 文字对象, m 变换矩阵
func (s *svgResourceRenderer) RenderText(text *canvas.Text, m canvas.Matrix) {
	if s.embeddedFonts {
		s.SVG.RenderText(text, m)
		return
	}
	if text.Empty() {
		return
	}
	font := text.MostCommonFontFace().Font
	resource, ok := s.renderer.canvasState().svgFontCache[font]
	if !ok {
		data := basefont.SFNTData(font.Tables)
		resource = SVGFont{
			Name:   fmt.Sprintf("ofdgo-%x-%d", sha256.Sum256(data), font.Style()),
			Weight: font.Style().CSS(),
			Style:  "normal",
			Data:   data,
		}
		if font.Style().Italic() {
			resource.Style = "italic"
		}
		s.renderer.canvasState().svgFontCache[font] = resource
	}
	if !s.seen[resource.Name] {
		s.seen[resource.Name] = true
		s.fonts = append(s.fonts, resource)
		fmt.Fprintf(&s.styles, ".%s{font-family:%s!important}", resource.Name, resource.Name)
	}
	s.SetClass(resource.Name)
	s.SVG.RenderText(text, m)
	s.SetClass()
}

// renderSVGResources 渲染SVG并收集外部资源
// 入参: page 页面内容, writer 输出流, images 是否分离图片, objects 是否标识页面直接对象
// 返回: SVGResources 外部资源, error 错误信息
func (r *Renderer) renderSVGResources(page *PageContent, writer io.Writer, images, objects bool) (SVGResources, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return SVGResources{}, err
	}
	options := svg.DefaultOptions
	options.EmbedFonts = false
	buffer := bufio.NewWriter(writer)
	s := &svgResourceRenderer{
		SVG:      svg.New(buffer, box.W, box.H, &options),
		renderer: r,
		writer:   buffer,
		seen:     make(map[string]bool),
	}
	io.WriteString(buffer, svgCreatorMetadata)
	io.WriteString(buffer, `<g style="image-orientation:none">`)
	if images {
		s.imageNames = make(map[svgImageKey]string)
	}
	if objects {
		s.objects = make(map[*GraphicObject]string)
		for i := range page.Content.Layer {
			for j := range page.Content.Layer[i].Objects {
				object := &page.Content.Layer[i].Objects[j]
				s.objects[object] = editorObjectID(*object)
			}
		}
		if err := r.renderCanvasPageToContext(canvas.NewContext(s), page, !r.TransparentBackground); err != nil {
			return SVGResources{}, err
		}
	} else {
		c, err := r.renderCanvasPage(page)
		if err != nil {
			return SVGResources{}, err
		}
		c.RenderTo(s)
	}
	if s.err != nil {
		return SVGResources{}, s.err
	}
	io.WriteString(buffer, `</g>`)
	s.SetCustomStyle(s.styles.String())
	if err := s.Close(); err != nil {
		return SVGResources{}, err
	}
	return SVGResources{Fonts: s.fonts, Images: s.images}, buffer.Flush()
}

// beginObject 标记直接对象及复合或注解成员，模板图元不单独标记
// 入参: object 图形对象
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginObject(object *GraphicObject) bool {
	id := s.objects[object]
	attribute := "data-ofd-object"
	if id == "" {
		if len(s.objectStack) == 0 || !s.objectStack[len(s.objectStack)-1].composite {
			return false
		}
		parent := &s.objectStack[len(s.objectStack)-1]
		id = fmt.Sprintf("%s/%d", parent.path, parent.next)
		parent.next++
		attribute = "data-ofd-child"
	}
	s.objectStack = append(s.objectStack, svgObjectGroup{path: id, composite: object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit"})
	fmt.Fprintf(s.writer, `<g %s="%s">`, attribute, html.EscapeString(id))
	return true
}

// endObject 结束对象分组
func (s *svgResourceRenderer) endObject() {
	s.objectStack = s.objectStack[:len(s.objectStack)-1]
	fmt.Fprint(s.writer, `</g>`)
}

// beginAnnotation 标记完整注解外观，内部图元保持原有层级和绘制顺序
// 入参: id 注解标识
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginAnnotation(id string) bool {
	if s.objects == nil {
		return false
	}
	s.objectStack = append(s.objectStack, svgObjectGroup{path: "annotation:" + id, composite: true})
	fmt.Fprintf(s.writer, `<g data-ofd-object="annotation:%s">`, html.EscapeString(id))
	return true
}

// skipObjects 为不可见资源保留内部成员序号
// 入参: count 直接成员数量
func (s *svgResourceRenderer) skipObjects(count int) {
	if len(s.objectStack) > 0 {
		s.objectStack[len(s.objectStack)-1].next += count
	}
}
