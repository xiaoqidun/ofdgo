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
	"github.com/tdewolff/canvas/renderers/svg"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	"github.com/xiaoqidun/pdfgo"
)

// svgCreatorMetadata 记录SVG制作软件，不参与页面绘制
const svgCreatorMetadata = `<metadata>` + creatorXMP + `</metadata>`

// svgNonReusableTemplate 标记含渐变或底纹定义、不可按片段复用的模板页哨兵
var svgNonReusableTemplate = &svgTemplateFragment{}

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
	imageClip     *canvas.Path
	clipDefs      []svgClipDef
	collectClips  bool
	hasSVGDefs    bool
}

// RenderPath 直接序列化纯色填充轮廓，其余画笔保留后端处理
// 入参: path 几何路径, style 绘制样式, m 页面变换
func (s *svgResourceRenderer) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if style.Fill.IsGradient() || style.Stroke.IsGradient() || style.Fill.IsPattern() || style.Stroke.IsPattern() {
		s.hasSVGDefs = true
	}
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

// RenderImage 按输出策略编码图片，分离资源时保持尺寸和变换并支持原生SVG裁剪路径
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
	_, height := s.Size()
	var clipID, clipData string
	if s.imageClip != nil {
		if s.imageClip.Empty() {
			return
		}
		clipData = svgClipPathData(s.imageClip, height)
		if clipData == "" {
			return
		}
		clipID = svgClipID(clipData)
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
			if s.seen == nil {
				s.seen = make(map[string]bool)
			}
			if !s.seen[name] {
				s.seen[name] = true
				s.images = append(s.images, resource)
			}
		}
	}
	size := img.Bounds().Size()
	m = m.Translate(0, float64(size.Y))
	matrix := Matrix{a: m[0][0], b: -m[1][0], c: -m[0][1], d: m[1][1], e: m[0][2], f: height - m[1][2]}
	writer := imageOutput{ctx: ctx, writer: s.writer}
	if clipID != "" {
		if s.seen == nil {
			s.seen = make(map[string]bool)
		}
		if !s.seen[clipID] {
			s.seen[clipID] = true
			if s.collectClips {
				s.clipDefs = append(s.clipDefs, svgClipDef{id: clipID, path: clipData})
			} else if _, s.err = fmt.Fprintf(writer, `<defs><clipPath id="%s"><path d="%s"/></clipPath></defs>`, clipID, clipData); s.err != nil {
				return
			}
		}
		if _, s.err = fmt.Fprintf(writer, `<g clip-path="url(#%s)">`, clipID); s.err != nil {
			return
		}
	}
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
	if clipID != "" {
		_, s.err = io.WriteString(writer, `"/></g>`)
	} else {
		_, s.err = io.WriteString(writer, `"/>`)
	}
	if s.err == nil {
		s.err = ctx.Err()
	}
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
	state := s.renderer.canvasState()
	resource, ok := state.svgFontCache[font]
	if !ok {
		ctx := s.renderer.outputContext()
		var prebuilt []byte
		digest, hasDigest := state.fontDigests[font]
		if !hasDigest {
			prebuilt = basefont.SFNTData(font.Tables)
			digest = sha256.Sum256(prebuilt)
			state.fontDigests[font] = digest
		}
		cacheKey := svgFontResourceKey{digest: digest, style: font.Style()}
		var err error
		resource, err = s.renderer.svgFontResources().load(ctx, cacheKey, func() (SVGFont, int, error) {
			data := prebuilt
			if data == nil {
				data = basefont.SFNTData(font.Tables)
			}
			built := SVGFont{
				Name:   fmt.Sprintf("ofdgo-%x-%d", digest, font.Style()),
				Weight: font.Style().CSS(),
				Style:  "normal",
				Data:   data,
			}
			if font.Style().Italic() {
				built.Style = "italic"
			}
			return built, len(data) + 256, nil
		})
		if err != nil {
			s.err = err
			return
		}
		state.svgFontCache[font] = resource
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

// imageResource 复用相同编码或像素的输出结果，原始编码图零解码直出并避免无谓克隆
// 入参: ctx 取消上下文, img 原图, target 压缩像素需求
// 返回: SVGImage 图片资源, error 编码或取消错误
func (s *svgResourceRenderer) imageResource(ctx context.Context, img image.Image, target image.Point) (SVGImage, error) {
	if err := ctx.Err(); err != nil {
		return SVGImage{}, err
	}
	if s.renderer != nil {
		if encoded, ok := img.(*EncodedImage); ok && s.renderer.Compression.Mode == CompressionUnchanged {
			sum, mime := encoded.ContentMeta()
			result := SVGImage{Name: fmt.Sprintf("ofdgo-image-%x", sum), MIME: mime}
			if s.imageNames == nil || !s.renderer.knownSVGResources[result.Name] {
				result.Data = bytes.Clone(encoded.Bytes())
			}
			return result, nil
		}
		cache := s.renderer.svgImageResources()
		if digest, ok := svgPixelDigest(ctx, img, max(cache.limit*4, 64<<20)); ok {
			key := svgImageResourceKey{digest: digest, options: s.renderer.Compression, target: target}
			result, err := cache.load(ctx, key, func() (SVGImage, int, error) {
				result, err := s.encodeImageResource(ctx, img, target)
				return result, cap(result.Data) + 256, err
			})
			if err != nil {
				return SVGImage{}, err
			}
			if s.imageNames != nil && s.renderer.knownSVGResources[result.Name] {
				result.Data = nil
			} else {
				result.Data = bytes.Clone(result.Data)
			}
			return result, nil
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
	if encoded, ok := img.(*EncodedImage); ok {
		resource.MIME, resource.Data = encoded.MIME(), encoded.Bytes()
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

// beginStamp 为完整签章外观建立编辑分组，不将嵌套OFD中的对象当作页面成员
// 入参: stamp 签章外观
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginStamp(stamp Stamp) bool {
	if s.objects == nil || stamp.ID == "" || stamp.SignatureID == "" {
		return false
	}
	id := stamp.ObjectID()
	s.objectStack = append(s.objectStack, svgObjectGroup{path: id})
	fmt.Fprintf(s.writer, `<g data-ofd-object="%s">`, html.EscapeString(id))
	return true
}

// skipObjects 为不可见资源保留内部成员序号
// 入参: count 直接成员数量
func (s *svgResourceRenderer) skipObjects(count int) {
	if len(s.objectStack) > 0 {
		s.objectStack[len(s.objectStack)-1].next += count
	}
}

// appendTemplateFragment 将缓存的模板页SVG片段及其字体、图片与裁剪定义合并到当前渲染器
// 入参: frag 模板页SVG片段
// 返回: error 写入错误
func (s *svgResourceRenderer) appendTemplateFragment(frag *svgTemplateFragment) error {
	if s.err != nil {
		return s.err
	}
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	for _, def := range frag.clipDefs {
		if s.seen[def.id] {
			continue
		}
		s.seen[def.id] = true
		if s.collectClips {
			s.clipDefs = append(s.clipDefs, def)
		} else if _, err := fmt.Fprintf(s.writer, `<defs><clipPath id="%s"><path d="%s"/></clipPath></defs>`, def.id, def.path); err != nil {
			s.err = err
			return err
		}
	}
	for _, font := range frag.fonts {
		if s.seen[font.Name] {
			continue
		}
		s.seen[font.Name] = true
		s.fonts = append(s.fonts, font)
		fmt.Fprintf(&s.styles, ".%s{font-family:%s!important}", font.Name, font.Name)
	}
	if s.imageNames != nil {
		for _, img := range frag.images {
			if s.seen[img.Name] {
				continue
			}
			s.seen[img.Name] = true
			if s.renderer != nil && s.renderer.knownSVGResources[img.Name] {
				img.Data = nil
			} else {
				img.Data = bytes.Clone(img.Data)
			}
			s.images = append(s.images, img)
		}
	}
	if len(frag.markup) != 0 {
		if _, err := s.writer.Write(frag.markup); err != nil {
			s.err = err
			return err
		}
	}
	return nil
}

// svgImageKey 按原图及输出精度复用图片编码，避免不同显示尺寸共用低清结果
type svgImageKey struct {
	image image.Image
	size  image.Point
}

// svgResourceCacheKey 区分共享的图片编码缓存
type svgResourceCacheKey struct{}

// svgFontCacheKey 区分共享的SVG序列化字体缓存
type svgFontCacheKey struct{}

// svgClipDef 保存原生SVG裁剪路径标识与路径数据
type svgClipDef struct {
	id   string
	path string
}

// svgTemplateKey 按模板页、视口、压缩与资源模式区分共享SVG片段
type svgTemplateKey struct {
	template       *PageContent
	width          float64
	height         float64
	offsetX        float64
	offsetY        float64
	printing       bool
	printContents  string
	externalImages bool
	compression    CompressionOptions
}

// svgTemplateFragment 保存可复用的模板页SVG片段及关联外部资源与裁剪定义
type svgTemplateFragment struct {
	markup   []byte
	fonts    []SVGFont
	images   []SVGImage
	clipDefs []svgClipDef
}

// svgFontResourceKey 按字体内容摘要与样式区分共享的SVG字体缓存
type svgFontResourceKey struct {
	digest [32]byte
	style  canvas.FontStyle
}

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
	return sharedCanvasCache[svgImageResourceKey, SVGImage](r, svgResourceCacheKey{}, 64<<20)
}

// svgFontResources 获取派生渲染器共享的SVG字体数据缓存
// 返回: *renderCache 字体数据缓存
func (r *Renderer) svgFontResources() *renderCache[svgFontResourceKey, SVGFont] {
	return sharedCanvasCache[svgFontResourceKey, SVGFont](r, svgFontCacheKey{}, 32<<20)
}

// svgTemplateFragments 获取派生渲染器共享的模板页SVG片段缓存
// 返回: *renderCache 模板页SVG片段缓存
func (r *Renderer) svgTemplateFragments() *renderCache[svgTemplateKey, *svgTemplateFragment] {
	return sharedCanvasCache[svgTemplateKey, *svgTemplateFragment](r, svgTemplateCacheKey{}, 32<<20)
}

// svgClipPathData 将画布坐标裁剪路径转换为SVG用户坐标系路径数据
// 入参: path 画布坐标裁剪路径, height 页面高度
// 返回: string SVG路径d属性
func svgClipPathData(path *canvas.Path, height float64) string {
	if path == nil || path.Empty() {
		return ""
	}
	return path.Copy().Transform(canvas.Identity.ReflectYAbout(height / 2)).ToSVG()
}

// svgClipID 按裁剪路径数据生成稳定的内容寻址标识
// 入参: pathData SVG路径d属性
// 返回: string 裁剪路径标识
func svgClipID(pathData string) string {
	sum := sha256.Sum256([]byte(pathData))
	return fmt.Sprintf("ofdgo-clip-%x", sum[:8])
}

// svgPixelDigest 对编码图像或标准像素缓冲计算完整摘要，不展开延迟图像
// 入参: ctx 取消上下文, img 原始图像, limit 可参与缓存的像素字节上限
// 返回: [32]byte 内容摘要, bool 是否为支持的图像布局
func svgPixelDigest(ctx context.Context, img image.Image, limit int) ([32]byte, bool) {
	if encoded, ok := img.(*EncodedImage); ok {
		raw := encoded.Bytes()
		if len(raw) == 0 {
			return [32]byte{}, false
		}
		hash := sha256.New()
		hash.Write([]byte("encoded:"))
		hash.Write([]byte(encoded.MIME()))
		hash.Write([]byte{0})
		for len(raw) > 0 {
			if ctx.Err() != nil {
				return [32]byte{}, false
			}
			count := min(len(raw), 256<<10)
			hash.Write(raw[:count])
			raw = raw[count:]
		}
		var digest [32]byte
		hash.Sum(digest[:0])
		return digest, true
	}
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
	}
	if err := r.renderCanvasPageToContext(canvas.NewContext(s), page, !r.TransparentBackground); err != nil {
		return SVGResources{}, err
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
