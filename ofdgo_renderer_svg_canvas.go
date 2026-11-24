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
	"hash"
	"html"
	"image"
	"image/color"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers/svg"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// svgCreatorMetadata 记录SVG制作软件，不参与页面绘制
const svgCreatorMetadata = `<metadata>` + creatorXMP + `</metadata>`

// svgDefaultPrecision 指定纯矢量SVG路径默认毫米小数位数
const svgDefaultPrecision = 3

// svgHexDigits 提供十六进制颜色编码字符表
const svgHexDigits = "0123456789abcdef"

// svgNonReusableTemplate 标记含渐变或底纹定义、不可按片段复用的模板页哨兵
var svgNonReusableTemplate = &svgTemplateFragment{}

// svgScopeMarkers 定义SVG内置定义与引用的精确标签和属性匹配模式
var svgScopeMarkers = [...]struct {
	prefix       []byte
	end          byte
	requireSpace bool
	styleAttr    bool
}{
	{prefix: []byte(`<linearGradient id="d`), end: '"'},
	{prefix: []byte(`<radialGradient id="d`), end: '"'},
	{prefix: []byte(`<pattern id="d`), end: '"'},
	{prefix: []byte(`fill="url(#d`), end: ')', requireSpace: true},
	{prefix: []byte(`stroke="url(#d`), end: ')', requireSpace: true},
	{prefix: []byte(`clip-path="url(#d`), end: ')', requireSpace: true},
	{prefix: []byte(`mask="url(#d`), end: ')', requireSpace: true},
	{prefix: []byte(`filter="url(#d`), end: ')', requireSpace: true},
	{prefix: []byte(`fill:url(#d`), end: ')', styleAttr: true},
	{prefix: []byte(`stroke:url(#d`), end: ')', styleAttr: true},
	{prefix: []byte(`clip-path:url(#d`), end: ')', styleAttr: true},
	{prefix: []byte(`mask:url(#d`), end: ')', styleAttr: true},
	{prefix: []byte(`filter:url(#d`), end: ')', styleAttr: true},
	{prefix: []byte(`href="#d`), end: '"', requireSpace: true},
	{prefix: []byte(`xlink:href="#d`), end: '"', requireSpace: true},
}

// svgIntBox 保存量化后的二维整数包围盒
type svgIntBox struct {
	minX int64
	minY int64
	maxX int64
	maxY int64
}

// svgPathCursor 跟踪紧凑相对路径序列化的定点坐标状态
type svgPathCursor struct {
	curX    int64
	curY    int64
	startX  int64
	startY  int64
	hasPath bool
	lastCmd byte
}

// svgResourceRenderer 分离外部资源的SVG渲染器
type svgResourceRenderer struct {
	*svg.SVG
	renderer         *Renderer
	writer           io.Writer
	fonts            []SVGFont
	images           []SVGImage
	imageNames       map[svgImageKey]string
	transparentEdges map[image.Image]transparentEdgeResult
	seen             map[string]bool
	styles           strings.Builder
	err              error
	objects          map[*GraphicObject]string
	objectStack      []svgObjectGroup
	embeddedFonts    bool
	pathData         []byte
	imageClip        *canvas.Path
	clipDefs         []svgClipDef
	collectClips     bool
	hasSVGDefs       bool
	pathMerge        SVGPathMergeOptions
	idPrefix         string
	precision        int
	pathCount        int
	pendingPath      []byte
	pendingFill      color.RGBA
	pendingCursor    svgPathCursor
	pendingRowBox    svgIntBox
	pendingUnionBox  svgIntBox
	pendingOps       int
	pendingHasUnion  bool
	pendingHasPath   bool
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

// svgTemplateKey 按模板页、视口、精度、合并模式、分辨率、压缩与资源模式区分共享SVG片段
type svgTemplateKey struct {
	template       *PageContent
	width          float64
	height         float64
	offsetX        float64
	offsetY        float64
	dpi            float64
	precision      int
	pathMerge      SVGPathMergeOptions
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

// svgPrecisionScale 返回指定小数位数对应的定点缩放倍率
// 入参: precision 毫米小数位数
// 返回: int 规范化小数位数, int64 整数缩放倍率
func svgPrecisionScale(precision int) (int, int64) {
	if precision <= 0 {
		precision = svgDefaultPrecision
	} else if precision > 6 {
		precision = 6
	}
	scale := int64(1)
	for range precision {
		scale *= 10
	}
	return precision, scale
}

// svgQuantizePoint 按仿射矩阵变换二维点并量化为定点整数坐标
// 入参: x 原始横坐标, y 原始纵坐标, m 目标变换矩阵, scale 定点缩放倍率
// 返回: int64 量化横坐标, int64 量化纵坐标, bool 坐标是否有限且在安全范围内
func svgQuantizePoint(x, y float64, m canvas.Matrix, scale float64) (int64, int64, bool) {
	tx := m[0][0]*x + m[0][1]*y + m[0][2]
	ty := m[1][0]*x + m[1][1]*y + m[1][2]
	if math.IsNaN(tx) || math.IsInf(tx, 0) || math.IsNaN(ty) || math.IsInf(ty, 0) || math.Abs(tx) > 1e9 || math.Abs(ty) > 1e9 {
		return 0, 0, false
	}
	return int64(math.Round(tx * scale)), int64(math.Round(ty * scale)), true
}

// svgInspectPath 校验纯色路径命令并计算量化控制点包围盒与命令数
// 入参: data 只读路径数据, m 目标变换矩阵, scale 定点缩放倍率
// 返回: svgIntBox 量化包围盒, int 路径命令数, bool 是否支持紧凑序列化
func svgInspectPath(data []float64, m canvas.Matrix, scale float64) (svgIntBox, int, bool) {
	var box svgIntBox
	hasPoint := false
	ops := 0
	include := func(x, y float64) bool {
		qx, qy, ok := svgQuantizePoint(x, y, m, scale)
		if !ok {
			return false
		}
		if !hasPoint {
			box = svgIntBox{minX: qx, minY: qy, maxX: qx, maxY: qy}
			hasPoint = true
		} else {
			box.minX = min(box.minX, qx)
			box.minY = min(box.minY, qy)
			box.maxX = max(box.maxX, qx)
			box.maxY = max(box.maxY, qy)
		}
		return true
	}
	for len(data) != 0 {
		switch data[0] {
		case canvas.MoveToCmd, canvas.LineToCmd:
			if !include(data[1], data[2]) {
				return svgIntBox{}, 0, false
			}
			ops++
			data = data[4:]
		case canvas.CloseCmd:
			ops++
			data = data[4:]
		case canvas.QuadToCmd:
			if !include(data[1], data[2]) || !include(data[3], data[4]) {
				return svgIntBox{}, 0, false
			}
			ops++
			data = data[6:]
		case canvas.CubeToCmd:
			if !include(data[1], data[2]) || !include(data[3], data[4]) || !include(data[5], data[6]) {
				return svgIntBox{}, 0, false
			}
			ops++
			data = data[8:]
		default:
			return svgIntBox{}, 0, false
		}
	}
	return box, ops, hasPoint
}

// appendSVGCoord 将定点整数坐标格式化为去尾零的紧凑SVG数字
// 入参: buf 输出缓冲, q 定点整数坐标, prec 小数位数, scale 定点缩放倍率, needSep 非负数前是否需分隔空格
// 返回: []byte 追加后的字节切片
func appendSVGCoord(buf []byte, q int64, prec int, scale int64, needSep bool) []byte {
	var u uint64
	if q < 0 {
		buf = append(buf, '-')
		u = uint64(-q)
	} else {
		if needSep {
			buf = append(buf, ' ')
		}
		u = uint64(q)
	}
	div := uint64(scale)
	intPart := u / div
	fracPart := u % div
	if fracPart == 0 {
		return strconv.AppendUint(buf, intPart, 10)
	}
	if intPart > 0 {
		buf = strconv.AppendUint(buf, intPart, 10)
	}
	buf = append(buf, '.')
	digits := prec
	for fracPart%10 == 0 {
		fracPart /= 10
		digits--
	}
	var tmp [8]byte
	pos := len(tmp)
	for range digits {
		pos--
		tmp[pos] = byte('0' + fracPart%10)
		fracPart /= 10
	}
	return append(buf, tmp[pos:]...)
}

// appendCompactSVGPath 将路径按紧凑相对坐标语法追加到输出缓冲并更新游标
// 入参: buf 输出缓冲, data 只读路径数据, m 目标变换矩阵, prec 小数位数, scale 定点缩放倍率, cursor 坐标游标
// 返回: []byte 追加后的字节切片
func appendCompactSVGPath(buf []byte, data []float64, m canvas.Matrix, prec int, scale int64, cursor *svgPathCursor) []byte {
	fscale := float64(scale)
	for len(data) != 0 {
		switch data[0] {
		case canvas.MoveToCmd:
			qx, qy, _ := svgQuantizePoint(data[1], data[2], m, fscale)
			if !cursor.hasPath {
				buf = append(buf, 'M')
				buf = appendSVGCoord(buf, qx, prec, scale, false)
				buf = appendSVGCoord(buf, qy, prec, scale, true)
				cursor.hasPath = true
			} else {
				buf = append(buf, 'm')
				buf = appendSVGCoord(buf, qx-cursor.curX, prec, scale, false)
				buf = appendSVGCoord(buf, qy-cursor.curY, prec, scale, true)
			}
			cursor.curX, cursor.curY = qx, qy
			cursor.startX, cursor.startY = qx, qy
			cursor.lastCmd = 0
			data = data[4:]
		case canvas.LineToCmd:
			qx, qy, _ := svgQuantizePoint(data[1], data[2], m, fscale)
			dx, dy := qx-cursor.curX, qy-cursor.curY
			if dx != 0 || dy != 0 {
				if dy == 0 {
					if cursor.lastCmd != 'h' {
						buf = append(buf, 'h')
						buf = appendSVGCoord(buf, dx, prec, scale, false)
						cursor.lastCmd = 'h'
					} else {
						buf = appendSVGCoord(buf, dx, prec, scale, true)
					}
					cursor.curX = qx
				} else if dx == 0 {
					if cursor.lastCmd != 'v' {
						buf = append(buf, 'v')
						buf = appendSVGCoord(buf, dy, prec, scale, false)
						cursor.lastCmd = 'v'
					} else {
						buf = appendSVGCoord(buf, dy, prec, scale, true)
					}
					cursor.curY = qy
				} else {
					if cursor.lastCmd != 'l' {
						buf = append(buf, 'l')
						buf = appendSVGCoord(buf, dx, prec, scale, false)
						cursor.lastCmd = 'l'
					} else {
						buf = appendSVGCoord(buf, dx, prec, scale, true)
					}
					buf = appendSVGCoord(buf, dy, prec, scale, true)
					cursor.curX, cursor.curY = qx, qy
				}
			}
			data = data[4:]
		case canvas.QuadToCmd:
			qcx, qcy, _ := svgQuantizePoint(data[1], data[2], m, fscale)
			qx, qy, _ := svgQuantizePoint(data[3], data[4], m, fscale)
			if cursor.lastCmd != 'q' {
				buf = append(buf, 'q')
				buf = appendSVGCoord(buf, qcx-cursor.curX, prec, scale, false)
				cursor.lastCmd = 'q'
			} else {
				buf = appendSVGCoord(buf, qcx-cursor.curX, prec, scale, true)
			}
			buf = appendSVGCoord(buf, qcy-cursor.curY, prec, scale, true)
			buf = appendSVGCoord(buf, qx-cursor.curX, prec, scale, true)
			buf = appendSVGCoord(buf, qy-cursor.curY, prec, scale, true)
			cursor.curX, cursor.curY = qx, qy
			data = data[6:]
		case canvas.CubeToCmd:
			qc1x, qc1y, _ := svgQuantizePoint(data[1], data[2], m, fscale)
			qc2x, qc2y, _ := svgQuantizePoint(data[3], data[4], m, fscale)
			qx, qy, _ := svgQuantizePoint(data[5], data[6], m, fscale)
			if cursor.lastCmd != 'c' {
				buf = append(buf, 'c')
				buf = appendSVGCoord(buf, qc1x-cursor.curX, prec, scale, false)
				cursor.lastCmd = 'c'
			} else {
				buf = appendSVGCoord(buf, qc1x-cursor.curX, prec, scale, true)
			}
			buf = appendSVGCoord(buf, qc1y-cursor.curY, prec, scale, true)
			buf = appendSVGCoord(buf, qc2x-cursor.curX, prec, scale, true)
			buf = appendSVGCoord(buf, qc2y-cursor.curY, prec, scale, true)
			buf = appendSVGCoord(buf, qx-cursor.curX, prec, scale, true)
			buf = appendSVGCoord(buf, qy-cursor.curY, prec, scale, true)
			cursor.curX, cursor.curY = qx, qy
			data = data[8:]
		case canvas.CloseCmd:
			buf = append(buf, 'z')
			cursor.curX, cursor.curY = cursor.startX, cursor.startY
			cursor.lastCmd = 'z'
			data = data[4:]
		}
	}
	return buf
}

// appendSVGFillAttr 按SVG 1.1规范追加纯色填充属性，半透明色使用反预乘十六进制与fill-opacity
// 入参: buf 输出缓冲, c 预乘填充颜色
// 返回: []byte 追加后的字节切片
func appendSVGFillAttr(buf []byte, c color.RGBA) []byte {
	if c.A == 255 {
		if c == canvas.Black {
			return buf
		}
		buf = append(buf, ` fill="#`...)
		if c.R>>4 == c.R&0xf && c.G>>4 == c.G&0xf && c.B>>4 == c.B&0xf {
			return append(buf, svgHexDigits[c.R&0xf], svgHexDigits[c.G&0xf], svgHexDigits[c.B&0xf], '"')
		}
		return append(buf, svgHexDigits[c.R>>4], svgHexDigits[c.R&0xf], svgHexDigits[c.G>>4], svgHexDigits[c.G&0xf], svgHexDigits[c.B>>4], svgHexDigits[c.B&0xf], '"')
	}
	if c.A == 0 {
		return append(buf, ` fill="none"`...)
	}
	a := uint32(c.A)
	r := uint8(min(255, (uint32(c.R)*255+a/2)/a))
	g := uint8(min(255, (uint32(c.G)*255+a/2)/a))
	b := uint8(min(255, (uint32(c.B)*255+a/2)/a))
	buf = append(buf, ` fill="#`...)
	buf = append(buf, svgHexDigits[r>>4], svgHexDigits[r&0xf], svgHexDigits[g>>4], svgHexDigits[g&0xf], svgHexDigits[b>>4], svgHexDigits[b&0xf])
	buf = append(buf, `" fill-opacity="`...)
	opStart := len(buf)
	buf = strconv.AppendFloat(buf, float64(c.A)/255.0, 'f', 5, 64)
	for len(buf) > opStart && buf[len(buf)-1] == '0' {
		buf = buf[:len(buf)-1]
	}
	if len(buf) > opStart && buf[len(buf)-1] == '.' {
		buf = buf[:len(buf)-1]
	}
	return append(buf, '"')
}

// svgScopeDefsIDs 为SVG标签内的内置渐变、底纹与引用标识添加作用域前缀，不触碰正文文本
// 入参: raw 原始SVG字节, prefix 作用域前缀
// 返回: []byte 前缀化后的SVG字节
func svgScopeDefsIDs(raw []byte, prefix string) []byte {
	if prefix == "" || len(raw) == 0 {
		return raw
	}
	escaped := []byte(html.EscapeString(prefix))
	out := make([]byte, 0, len(raw)+256)
	pos := 0
	for pos < len(raw) {
		relOpen := bytes.IndexByte(raw[pos:], '<')
		if relOpen < 0 {
			out = append(out, raw[pos:]...)
			break
		}
		tagStart := pos + relOpen
		out = append(out, raw[pos:tagStart]...)
		relClose := bytes.IndexByte(raw[tagStart:], '>')
		if relClose < 0 {
			out = append(out, raw[tagStart:]...)
			break
		}
		tagEnd := tagStart + relClose + 1
		tag := raw[tagStart:tagEnd]
		if !bytes.Contains(tag, []byte("#d")) && !bytes.Contains(tag, []byte(`id="d`)) {
			out = append(out, tag...)
			pos = tagEnd
			continue
		}
		tpos := 0
		for tpos < len(tag) {
			bestIdx := -1
			bestKind := -1
			for k := range svgScopeMarkers {
				if idx := bytes.Index(tag[tpos:], svgScopeMarkers[k].prefix); idx >= 0 {
					abs := tpos + idx
					if bestIdx < 0 || abs < bestIdx {
						bestIdx = abs
						bestKind = k
					}
				}
			}
			if bestIdx < 0 {
				out = append(out, tag[tpos:]...)
				break
			}
			marker := svgScopeMarkers[bestKind]
			numStart := bestIdx + len(marker.prefix)
			validBoundary := true
			if marker.requireSpace {
				validBoundary = bestIdx > 0 && (tag[bestIdx-1] == ' ' || tag[bestIdx-1] == '\t' || tag[bestIdx-1] == '\n')
			} else if marker.styleAttr {
				validBoundary = bestIdx > 0 && (tag[bestIdx-1] == '"' || tag[bestIdx-1] == ';' || tag[bestIdx-1] == ' ')
			}
			numEnd := numStart
			for numEnd < len(tag) && tag[numEnd] >= '0' && tag[numEnd] <= '9' {
				numEnd++
			}
			validEnd := numEnd > numStart && numEnd < len(tag) && tag[numEnd] == marker.end
			if validEnd && marker.requireSpace && marker.end == ')' {
				validEnd = numEnd+1 < len(tag) && tag[numEnd+1] == '"'
			}
			if validBoundary && validEnd {
				out = append(out, tag[tpos:numStart-1]...)
				out = append(out, escaped...)
				out = append(out, tag[numStart-1:numEnd+1]...)
				tpos = numEnd + 1
			} else {
				out = append(out, tag[tpos:numStart]...)
				tpos = numStart
			}
		}
		pos = tagEnd
	}
	return out
}

// svgScopeClipURLs 仅在SVG标签属性内为clip-path的ofdgo-clip引用添加作用域前缀，不触碰正文文本
// 入参: raw 原始SVG字节, prefix 作用域前缀
// 返回: []byte 作用域化后的SVG字节
func svgScopeClipURLs(raw []byte, prefix string) []byte {
	if prefix == "" || len(raw) == 0 {
		return raw
	}
	marker := []byte(`clip-path="url(#ofdgo-clip-`)
	if !bytes.Contains(raw, marker) {
		return raw
	}
	escaped := []byte(html.EscapeString(prefix))
	out := make([]byte, 0, len(raw)+64)
	pos := 0
	for pos < len(raw) {
		relOpen := bytes.IndexByte(raw[pos:], '<')
		if relOpen < 0 {
			out = append(out, raw[pos:]...)
			break
		}
		tagStart := pos + relOpen
		out = append(out, raw[pos:tagStart]...)
		relClose := bytes.IndexByte(raw[tagStart:], '>')
		if relClose < 0 {
			out = append(out, raw[tagStart:]...)
			break
		}
		tagEnd := tagStart + relClose + 1
		tag := raw[tagStart:tagEnd]
		if !bytes.Contains(tag, marker) {
			out = append(out, tag...)
			pos = tagEnd
			continue
		}
		tpos := 0
		for tpos < len(tag) {
			idx := bytes.Index(tag[tpos:], marker)
			if idx < 0 {
				out = append(out, tag[tpos:]...)
				break
			}
			abs := tpos + idx
			validBoundary := abs > 0 && (tag[abs-1] == ' ' || tag[abs-1] == '\t' || tag[abs-1] == '\n')
			insertAt := abs + len(`clip-path="url(#`)
			if validBoundary {
				out = append(out, tag[tpos:insertAt]...)
				out = append(out, escaped...)
				out = append(out, tag[insertAt:abs+len(marker)]...)
			} else {
				out = append(out, tag[tpos:abs+len(marker)]...)
			}
			tpos = abs + len(marker)
		}
		pos = tagEnd
	}
	return out
}

// writeDigestBytes 按分块响应取消并将像素或编码字节写入哈希计算器
// 入参: ctx 取消上下文, h 哈希实例, data 输入字节
// 返回: bool 是否未取消并完成写入
func writeDigestBytes(ctx context.Context, h hash.Hash, data []byte) bool {
	for len(data) > 0 {
		if ctx.Err() != nil {
			return false
		}
		count := min(len(data), 256<<10)
		h.Write(data[:count])
		data = data[count:]
	}
	return ctx.Err() == nil
}

// materializeSVGImage 将延迟或包装像素图像转为标准缓冲，透传常见内存布局以控制体积与内存
// 入参: ctx 取消上下文, img 原始图像, limit 允许物化的像素字节上限
// 返回: image.Image 标准内存图像或原图, error 取消错误
func materializeSVGImage(ctx context.Context, img image.Image, limit int) (image.Image, error) {
	switch img.(type) {
	case *EncodedImage, *image.NRGBA, *image.NRGBA64, *image.RGBA, *image.RGBA64, *image.Gray, *image.Gray16, *image.Paletted, *image.YCbCr, *image.CMYK:
		return img, nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || limit <= 0 {
		return img, nil
	}
	high := baseimage.HighPrecision(img)
	bpp := 4
	if high {
		bpp = 8
	}
	if int64(w)*int64(h)*int64(bpp) > int64(limit) {
		return img, nil
	}
	if high {
		out := image.NewNRGBA64(image.Rect(0, 0, w, h))
		for y := range h {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			row := out.Pix[y*out.Stride : y*out.Stride+w*8]
			for x := range w {
				c := baseimage.NRGBA64At(img, bounds.Min.X+x, bounds.Min.Y+y)
				off := x * 8
				row[off], row[off+1] = byte(c.R>>8), byte(c.R)
				row[off+2], row[off+3] = byte(c.G>>8), byte(c.G)
				row[off+4], row[off+5] = byte(c.B>>8), byte(c.B)
				row[off+6], row[off+7] = byte(c.A>>8), byte(c.A)
			}
		}
		return out, nil
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := out.Pix[y*out.Stride : y*out.Stride+w*4]
		for x := range w {
			c := baseimage.NRGBAAt(img, bounds.Min.X+x, bounds.Min.Y+y)
			off := x * 4
			row[off], row[off+1], row[off+2], row[off+3] = c.R, c.G, c.B, c.A
		}
	}
	return out, nil
}

// svgPixelDigest 对编码图像或标准像素缓冲计算完整摘要，不展开延迟图像
// 入参: ctx 取消上下文, img 原始图像, limit 可参与缓存的像素字节上限
// 返回: [32]byte 内容摘要, bool 是否为支持的图像布局
func svgPixelDigest(ctx context.Context, img image.Image, limit int) ([32]byte, bool) {
	if limit <= 0 {
		return [32]byte{}, false
	}
	if encoded, ok := img.(*EncodedImage); ok {
		raw := encoded.Bytes()
		if len(raw) == 0 {
			return [32]byte{}, false
		}
		h := sha256.New()
		h.Write([]byte("encoded:"))
		h.Write([]byte(encoded.MIME()))
		h.Write([]byte{0})
		if !writeDigestBytes(ctx, h, raw) {
			return [32]byte{}, false
		}
		var digest [32]byte
		h.Sum(digest[:0])
		return digest, true
	}
	if ycbcr, ok := img.(*image.YCbCr); ok {
		if int64(len(ycbcr.Y))+int64(len(ycbcr.Cb))+int64(len(ycbcr.Cr)) > int64(limit) {
			return [32]byte{}, false
		}
		bounds := ycbcr.Bounds()
		var header [88]byte
		for i, value := range [...]int{9, ycbcr.YStride, ycbcr.CStride, int(ycbcr.SubsampleRatio), bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y, len(ycbcr.Y), len(ycbcr.Cb), len(ycbcr.Cr)} {
			binary.LittleEndian.PutUint64(header[i*8:], uint64(value))
		}
		h := sha256.New()
		h.Write(header[:])
		if !writeDigestBytes(ctx, h, ycbcr.Y) || !writeDigestBytes(ctx, h, ycbcr.Cb) || !writeDigestBytes(ctx, h, ycbcr.Cr) {
			return [32]byte{}, false
		}
		var digest [32]byte
		h.Sum(digest[:0])
		return digest, true
	}
	var pixels []byte
	var stride, kind int
	var palette color.Palette
	switch source := img.(type) {
	case *image.NRGBA:
		pixels, stride, kind = source.Pix, source.Stride, 1
	case *image.NRGBA64:
		pixels, stride, kind = source.Pix, source.Stride, 2
	case *image.RGBA:
		pixels, stride, kind = source.Pix, source.Stride, 3
	case *image.RGBA64:
		pixels, stride, kind = source.Pix, source.Stride, 4
	case *image.Gray:
		pixels, stride, kind = source.Pix, source.Stride, 5
	case *image.Gray16:
		pixels, stride, kind = source.Pix, source.Stride, 6
	case *image.CMYK:
		pixels, stride, kind = source.Pix, source.Stride, 7
	case *image.Paletted:
		pixels, stride, kind, palette = source.Pix, source.Stride, 8, source.Palette
	default:
		return [32]byte{}, false
	}
	if len(pixels) > limit {
		return [32]byte{}, false
	}
	bounds := img.Bounds()
	var header [56]byte
	for i, value := range [...]int{kind, stride, bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y, len(palette)} {
		binary.LittleEndian.PutUint64(header[i*8:], uint64(value))
	}
	h := sha256.New()
	h.Write(header[:])
	if len(palette) > 0 {
		var entry [8]byte
		for _, c := range palette {
			r, g, b, a := c.RGBA()
			binary.LittleEndian.PutUint16(entry[0:2], uint16(r))
			binary.LittleEndian.PutUint16(entry[2:4], uint16(g))
			binary.LittleEndian.PutUint16(entry[4:6], uint16(b))
			binary.LittleEndian.PutUint16(entry[6:8], uint16(a))
			h.Write(entry[:])
		}
	}
	if !writeDigestBytes(ctx, h, pixels) {
		return [32]byte{}, false
	}
	var digest [32]byte
	h.Sum(digest[:0])
	return digest, true
}

// disjoint 判断两个量化整数包围盒是否严格不相交
// 入参: other 另一包围盒
// 返回: bool 是否无面积重叠
func (b svgIntBox) disjoint(other svgIntBox) bool {
	return b.maxX <= other.minX || other.maxX <= b.minX || b.maxY <= other.minY || other.maxY <= b.minY
}

// union 返回包含两个量化整数包围盒的最小外接包围盒
// 入参: other 另一包围盒
// 返回: svgIntBox 联合包围盒
func (b svgIntBox) union(other svgIntBox) svgIntBox {
	return svgIntBox{minX: min(b.minX, other.minX), minY: min(b.minY, other.minY), maxX: max(b.maxX, other.maxX), maxY: max(b.maxY, other.maxY)}
}

// effectivePrecision 获取当前渲染器生效的毫米路径精度
// 返回: int 小数位数, int64 定点缩放倍率
func (s *svgResourceRenderer) effectivePrecision() (int, int64) {
	return svgPrecisionScale(s.precision)
}

// flushPendingPath 输出并清空已合并缓冲的纯色矢量路径
func (s *svgResourceRenderer) flushPendingPath() {
	if !s.pendingHasPath {
		return
	}
	output := append(s.pendingPath, '"')
	output = appendSVGFillAttr(output, s.pendingFill)
	output = append(output, '/', '>')
	if s.err == nil {
		_, s.err = s.writer.Write(output)
	}
	s.pendingPath = output[:0]
	s.pendingHasPath = false
	s.pendingHasUnion = false
	s.pendingOps = 0
	s.pendingCursor = svgPathCursor{}
}

// RenderPath 跳过空路径和不可见路径并按策略合并连续同色纯矢量路径，其余画笔保留后端处理
// 入参: path 几何路径, style 绘制样式, m 页面变换
func (s *svgResourceRenderer) RenderPath(path *canvas.Path, style canvas.Style, m canvas.Matrix) {
	if s.err != nil {
		return
	}
	s.pathCount++
	if s.pathCount&127 == 0 && s.renderer != nil && s.renderer.pageContext != nil {
		if err := s.renderer.pageContext.Err(); err != nil {
			s.err = err
			return
		}
	}
	raw := path.Data()
	if len(raw) == 0 || !style.HasFill() && !style.HasStroke() {
		return
	}
	if style.Fill.IsGradient() || style.Stroke.IsGradient() || style.Fill.IsPattern() || style.Stroke.IsPattern() {
		s.hasSVGDefs = true
	}
	if !style.HasFill() || !style.Fill.IsColor() || style.HasStroke() {
		s.flushPendingPath()
		s.SVG.RenderPath(path, style, m)
		return
	}
	_, height := s.Size()
	tm := canvas.Matrix{{1, 0, 0}, {0, -1, height}}.Mul(m)
	prec, scale := s.effectivePrecision()
	box, ops, ok := svgInspectPath(raw, tm, float64(scale))
	if !ok {
		s.flushPendingPath()
		s.SVG.RenderPath(path, style, m)
		return
	}
	opt := s.pathMerge
	if opt.Mode != SVGPathMergeOff && style.Fill.Color.A == 255 && style.FillRule == canvas.NonZero {
		if opt.Mode == SVGPathMergeFull {
			if s.pendingHasPath {
				disjoint := s.pendingFill == style.Fill.Color && s.pendingRowBox.disjoint(box) && (!s.pendingHasUnion || s.pendingUnionBox.disjoint(box))
				if !disjoint {
					s.flushPendingPath()
				}
			}
			if !s.pendingHasPath {
				s.pendingPath = append(s.pendingPath[:0], `<path d="`...)
				s.pendingFill = style.Fill.Color
				s.pendingRowBox = box
				s.pendingHasUnion = false
				s.pendingHasPath = true
				s.pendingCursor = svgPathCursor{}
			} else if box.minY >= s.pendingRowBox.maxY || box.maxY <= s.pendingRowBox.minY {
				if s.pendingHasUnion {
					s.pendingUnionBox = s.pendingUnionBox.union(s.pendingRowBox)
				} else {
					s.pendingUnionBox = s.pendingRowBox
					s.pendingHasUnion = true
				}
				s.pendingRowBox = box
			} else {
				s.pendingRowBox = s.pendingRowBox.union(box)
			}
			s.pendingPath = appendCompactSVGPath(s.pendingPath, raw, tm, prec, scale, &s.pendingCursor)
			return
		}
		maxExtentQ := int64(math.MaxInt64 / 2)
		if scaledExtent := opt.MaxExtent * float64(scale); scaledExtent < float64(maxExtentQ) {
			maxExtentQ = int64(math.Round(scaledExtent))
		}
		if s.pendingHasPath {
			sameRow := !(box.minY >= s.pendingRowBox.maxY || box.maxY <= s.pendingRowBox.minY)
			joined := s.pendingRowBox.union(box)
			canMerge := s.pendingFill == style.Fill.Color && sameRow && s.pendingRowBox.disjoint(box) && joined.maxX-joined.minX <= maxExtentQ && joined.maxY-joined.minY <= maxExtentQ && s.pendingOps+ops <= opt.MaxOps && len(s.pendingPath) < opt.MaxBytes
			if !canMerge {
				s.flushPendingPath()
			} else {
				s.pendingRowBox = joined
			}
		}
		if !s.pendingHasPath {
			s.pendingPath = append(s.pendingPath[:0], `<path d="`...)
			s.pendingFill = style.Fill.Color
			s.pendingRowBox = box
			s.pendingHasUnion = false
			s.pendingHasPath = true
			s.pendingOps = 0
			s.pendingCursor = svgPathCursor{}
		}
		s.pendingOps += ops
		s.pendingPath = appendCompactSVGPath(s.pendingPath, raw, tm, prec, scale, &s.pendingCursor)
		return
	}
	s.flushPendingPath()
	var cursor svgPathCursor
	output := append(s.pathData[:0], `<path d="`...)
	output = appendCompactSVGPath(output, raw, tm, prec, scale, &cursor)
	output = append(output, '"')
	output = appendSVGFillAttr(output, style.Fill.Color)
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
	s.flushPendingPath()
	if s.err != nil {
		return
	}
	ctx := context.Background()
	if s.renderer != nil {
		ctx = s.renderer.pageContext
		if ctx == nil {
			ctx = s.renderer.outputContext()
		}
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
		if !s.collectClips && s.idPrefix != "" {
			clipID = s.idPrefix + clipID
		}
	}
	var target image.Point
	if s.renderer != nil && s.renderer.compression.ImageDPI() > 0 {
		bounds := img.Bounds()
		target = compressionImageSize(math.Hypot(m[0][0], m[1][0])*float64(bounds.Dx()), math.Hypot(m[0][1], m[1][1])*float64(bounds.Dy()), s.renderer.compression.ImageDPI())
	}
	cacheKey := s.imageNames != nil && isPointerImage(img)
	var key svgImageKey
	var name string
	var ok bool
	if cacheKey {
		key = svgImageKey{image: img, size: target}
		name, ok = s.imageNames[key]
	}
	var resource SVGImage
	if !ok {
		resource, s.err = s.imageResource(ctx, img, target)
		if s.err != nil {
			return
		}
		if s.imageNames != nil {
			name = resource.Name
			if cacheKey {
				s.imageNames[key] = name
			}
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
			} else if _, s.err = fmt.Fprintf(writer, `<defs><clipPath id="%s"><path d="%s"/></clipPath></defs>`, html.EscapeString(clipID), clipData); s.err != nil {
				return
			}
		}
		if _, s.err = fmt.Fprintf(writer, `<g clip-path="url(#%s)">`, html.EscapeString(clipID)); s.err != nil {
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
	if s.err != nil {
		return
	}
	s.flushPendingPath()
	if s.err != nil {
		return
	}
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
		ctx := s.renderer.pageContext
		if ctx == nil {
			ctx = s.renderer.outputContext()
		}
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

// imageResource 复用相同编码或像素的输出结果，原始编码图零解码直出并物化包装像素图参与缓存
// 入参: ctx 取消上下文, img 原图, target 压缩像素需求
// 返回: SVGImage 图片资源, error 编码或取消错误
func (s *svgResourceRenderer) imageResource(ctx context.Context, img image.Image, target image.Point) (SVGImage, error) {
	if err := ctx.Err(); err != nil {
		return SVGImage{}, err
	}
	if s.renderer != nil {
		if encoded, ok := img.(*EncodedImage); ok && s.renderer.compression.Mode == CompressionUnchanged {
			sum, mime := encoded.ContentMeta()
			result := SVGImage{Name: fmt.Sprintf("ofdgo-image-%x", sum), MIME: mime}
			if s.imageNames == nil || !s.renderer.knownSVGResources[result.Name] {
				result.Data = bytes.Clone(encoded.Bytes())
			}
			return result, nil
		}
		cache := s.renderer.svgImageResources()
		limit := min(cache.limit, 64<<20)
		var err error
		img, err = materializeSVGImage(ctx, img, limit)
		if err != nil {
			return SVGImage{}, err
		}
		if digest, ok := svgPixelDigest(ctx, img, limit); ok {
			key := svgImageResourceKey{digest: digest, options: s.renderer.compression, target: target}
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
		options = s.renderer.compression
	}
	resource := SVGImage{MIME: "image/png"}
	if encoded, ok := img.(*EncodedImage); ok {
		resource.MIME, resource.Data = encoded.MIME(), encoded.Bytes()
		if options.Mode != CompressionUnchanged {
			data, err := pdfgo.OptimizeImageResource(ctx, resource.Data, options.pdfOptions(), target)
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
			resource.Data, err = pdfgo.EncodeImageResource(ctx, pixels, options.pdfOptions())
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
	if s.err != nil {
		return false
	}
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
	s.flushPendingPath()
	if s.err != nil {
		return false
	}
	if _, s.err = fmt.Fprintf(s.writer, `<g %s="%s">`, attribute, html.EscapeString(id)); s.err != nil {
		return false
	}
	s.objectStack = append(s.objectStack, svgObjectGroup{path: id, composite: object.Type == "CompositeObject"})
	return true
}

// endObject 结束对象分组
func (s *svgResourceRenderer) endObject() {
	s.flushPendingPath()
	s.objectStack = s.objectStack[:len(s.objectStack)-1]
	if s.err == nil {
		_, s.err = fmt.Fprint(s.writer, `</g>`)
	}
}

// beginTransparencyGroup 开启SVG标准透明组隔离
// 入参: alpha 透明组不透明度
// 返回: bool 是否开启成功
func (s *svgResourceRenderer) beginTransparencyGroup(alpha uint8) bool {
	if s.err != nil {
		return false
	}
	s.flushPendingPath()
	if s.err != nil {
		return false
	}
	opacity := math.Round(float64(alpha)/255*10000) / 10000
	_, s.err = fmt.Fprintf(s.writer, `<g opacity="%s">`, strconv.FormatFloat(opacity, 'f', -1, 64))
	return s.err == nil
}

// endTransparencyGroup 结束SVG透明组隔离
func (s *svgResourceRenderer) endTransparencyGroup() {
	s.flushPendingPath()
	if s.err == nil {
		_, s.err = fmt.Fprint(s.writer, `</g>`)
	}
}

// beginAnnotation 标记完整注解外观，内部图元保持原有层级和绘制顺序
// 入参: id 注解标识
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginAnnotation(id string) bool {
	if s.objects == nil || s.err != nil {
		return false
	}
	s.flushPendingPath()
	if s.err != nil {
		return false
	}
	if _, s.err = fmt.Fprintf(s.writer, `<g data-ofd-object="annotation:%s">`, html.EscapeString(id)); s.err != nil {
		return false
	}
	s.objectStack = append(s.objectStack, svgObjectGroup{path: "annotation:" + id, composite: true})
	return true
}

// beginStamp 为完整签章外观建立编辑分组，不将嵌套OFD中的对象当作页面成员
// 入参: stamp 签章外观
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginStamp(stamp Stamp) bool {
	if s.objects == nil || stamp.ID == "" || stamp.SignatureID == "" || s.err != nil {
		return false
	}
	s.flushPendingPath()
	if s.err != nil {
		return false
	}
	id := stamp.ObjectID()
	if _, s.err = fmt.Fprintf(s.writer, `<g data-ofd-object="%s">`, html.EscapeString(id)); s.err != nil {
		return false
	}
	s.objectStack = append(s.objectStack, svgObjectGroup{path: id})
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
	s.flushPendingPath()
	if s.err != nil {
		return s.err
	}
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	for _, def := range frag.clipDefs {
		clipID := def.id
		if !s.collectClips && s.idPrefix != "" {
			clipID = s.idPrefix + clipID
		}
		if s.seen[clipID] {
			continue
		}
		s.seen[clipID] = true
		if s.collectClips {
			s.clipDefs = append(s.clipDefs, svgClipDef{id: clipID, path: def.path})
		} else if _, err := fmt.Fprintf(s.writer, `<defs><clipPath id="%s"><path d="%s"/></clipPath></defs>`, html.EscapeString(clipID), def.path); err != nil {
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
		markup := frag.markup
		if !s.collectClips && s.idPrefix != "" && len(frag.clipDefs) > 0 {
			markup = svgScopeClipURLs(markup, s.idPrefix)
		}
		if _, err := s.writer.Write(markup); err != nil {
			s.err = err
			return err
		}
	}
	return nil
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

// renderSVGResources 渲染SVG并收集外部资源
// 入参: page 页面内容, writer 输出流, images 是否分离图片, objects 是否标识页面直接对象
// 返回: SVGResources 外部资源, error 错误信息
func (r *Renderer) renderSVGResources(page *PageContent, writer io.Writer, images, objects bool) (SVGResources, error) {
	ctx := r.pageContext
	if ctx == nil {
		ctx = r.outputContext()
	}
	if err := ctx.Err(); err != nil {
		return SVGResources{}, err
	}
	box, err := r.GetPageBox(page)
	if err != nil {
		return SVGResources{}, err
	}
	options := svg.DefaultOptions
	options.EmbedFonts = false
	var scopedBuf bytes.Buffer
	var target io.Writer
	var buffered *bufio.Writer
	if r.svgIDPrefix != "" {
		target = &scopedBuf
	} else {
		buffered = bufio.NewWriter(writer)
		target = buffered
	}
	s := &svgResourceRenderer{
		SVG:       svg.New(target, box.W, box.H, &options),
		renderer:  r,
		writer:    target,
		seen:      make(map[string]bool),
		pathMerge: r.SVGPathMerge(),
		idPrefix:  r.svgIDPrefix,
		precision: r.svgPrecision,
	}
	if _, err := io.WriteString(target, svgCreatorMetadata); err != nil {
		return SVGResources{}, err
	}
	if _, err := io.WriteString(target, `<g style="image-orientation:none">`); err != nil {
		return SVGResources{}, err
	}
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
	if err := r.renderCanvasPageToContext(canvas.NewContext(s), page, !r.transparentBackground); err != nil {
		return SVGResources{}, err
	}
	s.flushPendingPath()
	if s.err != nil {
		return SVGResources{}, s.err
	}
	if err := ctx.Err(); err != nil {
		return SVGResources{}, err
	}
	if _, err := io.WriteString(target, `</g>`); err != nil {
		return SVGResources{}, err
	}
	s.SetCustomStyle(s.styles.String())
	if err := s.Close(); err != nil {
		return SVGResources{}, err
	}
	if r.svgIDPrefix != "" {
		out := scopedBuf.Bytes()
		if s.hasSVGDefs {
			out = svgScopeDefsIDs(out, r.svgIDPrefix)
		}
		if _, err := writer.Write(out); err != nil {
			return SVGResources{}, err
		}
		return SVGResources{Fonts: s.fonts, Images: s.images}, ctx.Err()
	}
	return SVGResources{Fonts: s.fonts, Images: s.images}, buffered.Flush()
}
