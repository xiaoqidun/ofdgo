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
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"golang.org/x/image/draw"
)

// 光栅后端接收的路径指令
const (
	RasterMove RasterVerb = iota
	RasterLine
	RasterQuad
	RasterCubic
	RasterClose
)

// 光栅透明组指令：常规绘制、开启透明组与结束透明组
const (
	RasterGroupNone RasterGroupKind = iota
	RasterGroupBegin
	RasterGroupEnd
)

// 光栅渐变类型：轴向、径向和网格
const (
	RasterLinear RasterGradientKind = iota
	RasterRadial
	RasterMesh
)

// RasterPage 已解析的光栅绘制页面，单位为毫米，页面坐标原点在左上角
// DPI决定像素尺寸和编译时的字形、描边精度，修改DPI后应重新编译
// 页面及其引用资源在渲染期间只读，不用于重建或保存OFD
type RasterPage struct {
	Width, Height float64
	DPI           float64
	Commands      []RasterCommand
}

// RasterMatrix 按a、b、c、d、e、f排列的仿射矩阵，零值不是单位矩阵
type RasterMatrix [6]float64

// RasterPoint 路径或渐变坐标
type RasterPoint struct{ X, Y float64 }

// RasterVerb 路径指令
type RasterVerb uint8

// RasterSegment 路径片段，RasterMove和RasterLine使用End，RasterQuad使用Control1和End
type RasterSegment struct {
	Verb               RasterVerb
	Control1, Control2 RasterPoint
	End                RasterPoint
}

// RasterGroupKind 光栅透明组指令类型
type RasterGroupKind uint8

// RasterCommand 填充路径、绘制图像或管理透明组，Image非空时为图像指令
// Interpolate为可选内存态图像插值开关，非空时优先于默认缩放插值策略
// Group非零时为透明组边界指令，GroupAlpha为开启透明组时的整体透明度
// EvenOdd仅控制填充规则，不影响描边
// Transform将局部坐标映射到页面，图像局部坐标与image.Image的像素坐标一致
// Clip为页面坐标裁剪，适用于路径、图像和透明组，nil表示不裁剪，空切片表示全部裁去
// Stroke非空时为描边，宽度与虚线使用页面毫米，不随Transform再次缩放
type RasterCommand struct {
	Path        []RasterSegment
	Paint       RasterPaint
	EvenOdd     bool
	Transform   RasterMatrix
	Image       image.Image
	Interpolate *bool
	Clip        []RasterSegment
	Stroke      *StrokeOptions
	Group       RasterGroupKind
	GroupAlpha  uint8
}

// RasterPaint 预乘RGBA纯色或渐变，Gradient非空时忽略Color
type RasterPaint struct {
	Color    color.RGBA
	Gradient *RasterGradient
}

// RasterGradientKind 渐变类型
type RasterGradientKind uint8

// RasterGradient 局部坐标下的轴向、双圆径向或三角网格渐变
// 轴向及径向渐变的Spread为空时延续边界颜色，网格渐变使用Mesh
// Sample可选，按单位区间采样源颜色变换，优先于Stops且不负责几何延伸
type RasterGradient struct {
	Kind       RasterGradientKind
	Start, End RasterPoint
	R0, R1     float64
	Stops      []ColorStop
	Spread     *RasterSpread
	Mesh       *MeshShading
	Sample     func(float64) color.RGBA
}

// RasterSpread 保存OFD渐变延伸和周期，nil表示连续延伸
type RasterSpread struct {
	Extend  int
	MapType string
	Period  float64
}

// RasterScene 固定尺寸、DPI、字体和资源的可重复绘制场景
// 每次Render返回独立图像；源图像只读借用，修改页面或配置后应重新准备
type RasterScene interface {
	Render() (image.Image, error)
}

// RasterSceneBackend 可选的光栅预编译能力，不改变后端选择
type RasterSceneBackend interface {
	RasterBackend
	Prepare(page *RasterPage) (RasterScene, error)
}

// RasterSceneUpdater 可选的增量场景能力，保持原后端与显式几何配置
// Update按新页面的完整内容及DPI使派生数据失效，失败时原场景仍可绘制
// 图片仍为只读借用，编辑图片应提供新资源；修改后端配置应重新Prepare
type RasterSceneUpdater interface {
	RasterScene
	Update(page *RasterPage) error
}

// RasterSceneReuseBackend 可选的跨快照复用能力，不改变显式后端选择
// 实现必须验证前次场景及配置，返回独立快照，不能修改仍可使用的旧场景
type RasterSceneReuseBackend interface {
	RasterSceneBackend
	PrepareReuse(page *RasterPage, previous RasterScene) (RasterScene, error)
}

// rasterPreparationKey 隔离渲染器私有的前次场景
type rasterPreparationKey struct{}

// rasterPreparationState 记录前次场景及实际后端配置
type rasterPreparationState struct {
	backend RasterBackend
	scene   RasterScene
}

// rasterScene 复用后端无关指令，图片保持只读引用
type rasterScene struct {
	backend RasterBackend
	page    *RasterPage
}

// rasterLinearPixel 保存单遍线性降采样中的预乘浮点累积像素
type rasterLinearPixel struct {
	r, g, b, a float64
}

// rasterBoxSpan 保存单个目标像素对应的水平源像素区间与首尾覆盖权重
type rasterBoxSpan struct {
	start  int
	end    int
	wFirst float64
	wLast  float64
}

// PrepareRasterScene 准备绘制快照，未实现预编译的后端复用公共指令
// 入参: backend 光栅后端, page 绘制页面
// 返回: RasterScene 可重复绘制场景, error 准备错误
func PrepareRasterScene(backend RasterBackend, page *RasterPage) (RasterScene, error) {
	if backend == nil {
		return nil, fmt.Errorf("raster scene: %w", ErrBackendUnavailable)
	}
	if _, _, err := page.PixelSize(); err != nil {
		return nil, err
	}
	if provider, ok := backend.(RasterSceneBackend); ok {
		return provider.Prepare(page)
	}
	return &rasterScene{backend: backend, page: cloneRasterPage(page)}, nil
}

// rasterPositive 判断正有限数
// 入参: v 待检查数值
// 返回: bool 是否为正有限数
func rasterPositive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

// rasterRadialDomain 求延伸区间内半径非负的最后一个双圆参数
// 入参: a 二次项系数, b 一次项系数, c 常数项, radius 起始半径, delta 半径变化, extend 延伸标志
// 返回: float64 参数, bool 是否覆盖采样点
func rasterRadialDomain(a, b, c, radius, delta float64, extend int) (float64, bool) {
	x, y := math.NaN(), math.NaN()
	if a == 0 {
		if b != 0 {
			x = -c / b
		}
	} else if d := b*b - 4*a*c; d >= 0 {
		q := -.5 * (b + math.Copysign(math.Sqrt(d), b))
		if q == 0 {
			x = -b / (2 * a)
		} else {
			x, y = q/a, c/q
		}
	}
	best := math.Inf(-1)
	for _, t := range []float64{x, y} {
		if !finite(t) || radius+delta*t < 0 || t < 0 && extend&1 == 0 || t > 1 && extend&2 == 0 {
			continue
		}
		best = math.Max(best, t)
	}
	return best, !math.IsInf(best, -1)
}

// rasterRadialPosition 求双圆渐变中半径非负的有效参数
// 入参: a 二次项系数, b 一次项系数, c 常数项, radius 起始半径, delta 半径增量
// 返回: float64 渐变位置，范围为0至1
func rasterRadialPosition(a, b, c, radius, delta float64) float64 {
	x, y := math.NaN(), math.NaN()
	if a == 0 {
		if b != 0 {
			x = -c / b
		}
	} else if d := b*b - 4*a*c; d >= 0 {
		q := -0.5 * (b + math.Copysign(math.Sqrt(d), b))
		if q == 0 {
			x = -b / (2 * a)
		} else {
			x, y = q/a, c/q
			if x > y {
				x, y = y, x
			}
		}
	}
	for _, t := range []float64{y, x} {
		if t >= 0 && t <= 1 && radius+delta*t >= 0 {
			return t
		}
	}
	for _, t := range []float64{x, y} {
		if t > 0 && radius+delta*t >= 0 {
			return 1
		}
	}
	return 0
}

// rasterCommandGeometry 共用指令的页面变换、描边与裁剪，不修改路径或样式
// 入参: geometry 几何后端, command 绘制指令
// 返回: GeometryPath 页面轮廓, error 几何或指令错误
func rasterCommandGeometry(geometry GeometryBackend, command RasterCommand) (GeometryPath, error) {
	if command.Group != RasterGroupNone {
		return nil, nil
	}
	if command.Clip != nil && len(command.Clip) == 0 {
		return nil, nil
	}
	var path GeometryPath
	if command.Image != nil {
		bounds := command.Image.Bounds()
		if bounds.Empty() {
			return nil, nil
		}
		path = geometryRectangle(Box{X: float64(bounds.Min.X), Y: float64(bounds.Min.Y), W: float64(bounds.Dx()), H: float64(bounds.Dy())})
	} else {
		if len(command.Path) == 0 || command.Paint.Gradient == nil && command.Paint.Color.A == 0 {
			return nil, nil
		}
		for _, segment := range command.Path {
			if segment.Verb > RasterClose {
				return nil, fmt.Errorf("unsupported raster path verb %d", segment.Verb)
			}
		}
		path = geometryFromRaster(command.Path)
	}
	matrix := MatrixFromValues([6]float64(command.Transform))
	path, err := geometry.Transform(path, matrix)
	if err != nil {
		return nil, err
	}
	if command.Image == nil && command.Stroke != nil {
		if err := validateStrokeOptions(*command.Stroke); err != nil {
			return nil, err
		}
		path, err = geometry.Stroke(path, *command.Stroke)
		if err != nil {
			return nil, err
		}
	} else if command.EvenOdd && command.Clip != nil {
		path, err = geometry.Normalize(path, true)
		if err != nil {
			return nil, err
		}
	}
	if command.Clip != nil {
		for _, segment := range command.Clip {
			if segment.Verb > RasterClose {
				return nil, fmt.Errorf("unsupported raster clip verb %d", segment.Verb)
			}
		}
		clip := geometryFromRaster(command.Clip)
		path, err = clipGeometry(geometry, path, &clip)
		if err != nil {
			return nil, err
		}
	}
	return path, nil
}

// rasterClipKey 区分页面尺寸、分辨率和裁剪路径
// 入参: page 页面, path 页面坐标路径
// 返回: [32]byte 缓存键
func rasterClipKey(page *RasterPage, path []RasterSegment) [32]byte {
	hash := sha256.New()
	var data [8]byte
	number := func(v float64) {
		binary.LittleEndian.PutUint64(data[:], math.Float64bits(v))
		hash.Write(data[:])
	}
	number(page.Width)
	number(page.Height)
	number(page.DPI)
	for _, s := range path {
		hash.Write([]byte{byte(s.Verb)})
		for _, p := range []RasterPoint{s.End, s.Control1, s.Control2} {
			number(p.X)
			number(p.Y)
		}
	}
	var key [32]byte
	copy(key[:], hash.Sum(nil))
	return key
}

// rasterClipMask 使用所选后端生成区域化透明度蒙版，同页相同裁剪可复用并响应取消
// 入参: ctx 取消上下文, backend 光栅后端, page 页面, path 页面坐标裁剪, cache 蒙版缓存
// 返回: *image.Alpha 页面像素坐标蒙版, error 绘制或取消错误
func rasterClipMask(ctx context.Context, backend RasterBackend, page *RasterPage, path []RasterSegment, cache *renderCache[[32]byte, *image.Alpha]) (*image.Alpha, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := rasterClipKey(page, path)
	if mask, ok := cache.get(key); ok {
		return mask, nil
	}
	w, h, err := page.PixelSize()
	if err != nil {
		return nil, err
	}
	matrix := page.PixelTransform(RasterMatrix{1, 0, 0, 1, 0, 0}, h)
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	include := func(p RasterPoint) {
		p = matrix.Apply(p)
		minX = math.Min(minX, p.X)
		minY = math.Min(minY, p.Y)
		maxX = math.Max(maxX, p.X)
		maxY = math.Max(maxY, p.Y)
	}
	for _, s := range path {
		switch s.Verb {
		case RasterMove, RasterLine:
			include(s.End)
		case RasterQuad:
			include(s.End)
			include(s.Control1)
		case RasterCubic:
			include(s.End)
			include(s.Control1)
			include(s.Control2)
		case RasterClose:
		default:
			return nil, fmt.Errorf("invalid clip path command %d", s.Verb)
		}
	}
	if len(path) == 0 || minX > maxX || minY > maxY {
		return image.NewAlpha(image.Rectangle{}), nil
	}
	if !finite(minX) || !finite(minY) || !finite(maxX) || !finite(maxY) {
		return nil, fmt.Errorf("invalid clip coordinates")
	}
	bounds := image.Rect(int(math.Max(0, math.Min(float64(w), math.Floor(minX)-1))), int(math.Max(0, math.Min(float64(h), math.Floor(minY)-1))), int(math.Max(0, math.Min(float64(w), math.Ceil(maxX)+1))), int(math.Max(0, math.Min(float64(h), math.Ceil(maxY)+1))))
	mask := image.NewAlpha(bounds)
	if !bounds.Empty() {
		scale := page.DPI / 25.4
		local := &RasterPage{Width: float64(bounds.Dx()) / scale, Height: float64(bounds.Dy()) / scale, DPI: page.DPI}
		local.Commands = []RasterCommand{{Path: path, Paint: RasterPaint{Color: color.RGBA{255, 255, 255, 255}}, Transform: RasterMatrix{1, 0, 0, 1, -float64(bounds.Min.X) / scale, (matrix[5] - float64(bounds.Min.Y)) / scale}}}
		pixels, err := backend.Render(ctx, local)
		if cancelErr := ctx.Err(); cancelErr != nil {
			return nil, cancelErr
		}
		if err != nil {
			return nil, err
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			if (y-bounds.Min.Y)&63 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			start := mask.PixOffset(bounds.Min.X, y)
			row := mask.Pix[start : start+bounds.Dx()]
			for x := range row {
				row[x] = uint8(baseimage.AlphaAt(pixels, x, y-bounds.Min.Y) >> 8)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cache.put(key, mask, len(mask.Pix)+256)
	return mask, nil
}

// rasterLinearUnit16 将归一化浮点分量裁切并舍入为十六位无符号整型
// 入参: v 归一化通道值
// 返回: uint16 十六位分量值
func rasterLinearUnit16(v float64) uint16 {
	if v <= 0 || math.IsNaN(v) {
		return 0
	}
	if v >= 1 {
		return 65535
	}
	return uint16(v*65535 + 0.5)
}

// rasterBoxSpans 预计算面积盒滤波降采样在水平轴上的源像素覆盖区间与权重
// 入参: srcLen 源宽度, dstLen 目标宽度
// 返回: []rasterBoxSpan 水平覆盖表
func rasterBoxSpans(srcLen, dstLen int) []rasterBoxSpan {
	spans := make([]rasterBoxSpan, dstLen)
	scale := float64(srcLen) / float64(dstLen)
	for dx := range dstLen {
		x0 := float64(dx) * scale
		x1 := float64(dx+1) * scale
		start := int(math.Floor(x0))
		end := min(srcLen, int(math.Ceil(x1)))
		if end <= start {
			end = min(srcLen, start+1)
		}
		wFirst := min(float64(start+1), x1) - max(float64(start), x0)
		wLast := min(float64(end), x1) - max(float64(end-1), x0)
		spans[dx] = rasterBoxSpan{start: start, end: end, wFirst: wFirst, wLast: wLast}
	}
	return spans
}

// accumulateLinearRow 按源像素布局将单行源像素查表转为线性预乘颜色并累加至目标行缓冲
// 入参: acc 目标行累加缓冲, src 源图像, sb 源边界, rowY 相对行偏移, wy 垂直权重, xSpans 水平覆盖表, lut 非线性转线性查表, palLinear 调色板线性预乘表
func accumulateLinearRow(acc []rasterLinearPixel, src image.Image, sb image.Rectangle, rowY int, wy float64, xSpans []rasterBoxSpan, lut *[256]float64, palLinear []rasterLinearPixel) {
	sw := sb.Dx()
	y := sb.Min.Y + rowY
	switch s := src.(type) {
	case *image.NRGBA:
		start := s.PixOffset(sb.Min.X, y)
		row := s.Pix[start : start+sw*4]
		for dx, sp := range xSpans {
			var lr, lg, lb, la float64
			for x := sp.start; x < sp.end; x++ {
				o := x * 4
				a := row[o+3]
				if a == 0 {
					continue
				}
				wx := 1.0
				if x == sp.start {
					wx = sp.wFirst
				} else if x == sp.end-1 {
					wx = sp.wLast
				}
				if a == 255 {
					lr += lut[row[o]] * wx
					lg += lut[row[o+1]] * wx
					lb += lut[row[o+2]] * wx
					la += wx
				} else {
					wa := wx * float64(a) / 255.0
					lr += lut[row[o]] * wa
					lg += lut[row[o+1]] * wa
					lb += lut[row[o+2]] * wa
					la += wa
				}
			}
			acc[dx].r += lr * wy
			acc[dx].g += lg * wy
			acc[dx].b += lb * wy
			acc[dx].a += la * wy
		}
	case *image.RGBA:
		start := s.PixOffset(sb.Min.X, y)
		row := s.Pix[start : start+sw*4]
		for dx, sp := range xSpans {
			var lr, lg, lb, la float64
			for x := sp.start; x < sp.end; x++ {
				o := x * 4
				a := row[o+3]
				if a == 0 {
					continue
				}
				wx := 1.0
				if x == sp.start {
					wx = sp.wFirst
				} else if x == sp.end-1 {
					wx = sp.wLast
				}
				if a == 255 {
					lr += lut[row[o]] * wx
					lg += lut[row[o+1]] * wx
					lb += lut[row[o+2]] * wx
					la += wx
				} else {
					ua := uint32(a)
					r := uint8(min(255, (uint32(row[o])*255+ua/2)/ua))
					g := uint8(min(255, (uint32(row[o+1])*255+ua/2)/ua))
					b := uint8(min(255, (uint32(row[o+2])*255+ua/2)/ua))
					wa := wx * float64(a) / 255.0
					lr += lut[r] * wa
					lg += lut[g] * wa
					lb += lut[b] * wa
					la += wa
				}
			}
			acc[dx].r += lr * wy
			acc[dx].g += lg * wy
			acc[dx].b += lb * wy
			acc[dx].a += la * wy
		}
	case *image.Gray:
		start := s.PixOffset(sb.Min.X, y)
		row := s.Pix[start : start+sw]
		for dx, sp := range xSpans {
			var ly, la float64
			for x := sp.start; x < sp.end; x++ {
				wx := 1.0
				if x == sp.start {
					wx = sp.wFirst
				} else if x == sp.end-1 {
					wx = sp.wLast
				}
				ly += lut[row[x]] * wx
				la += wx
			}
			v := ly * wy
			acc[dx].r += v
			acc[dx].g += v
			acc[dx].b += v
			acc[dx].a += la * wy
		}
	case *image.Paletted:
		start := s.PixOffset(sb.Min.X, y)
		row := s.Pix[start : start+sw]
		for dx, sp := range xSpans {
			var lr, lg, lb, la float64
			for x := sp.start; x < sp.end; x++ {
				idx := int(row[x])
				if idx >= len(palLinear) {
					continue
				}
				p := palLinear[idx]
				if p.a == 0 {
					continue
				}
				wx := 1.0
				if x == sp.start {
					wx = sp.wFirst
				} else if x == sp.end-1 {
					wx = sp.wLast
				}
				lr += p.r * wx
				lg += p.g * wx
				lb += p.b * wx
				la += p.a * wx
			}
			acc[dx].r += lr * wy
			acc[dx].g += lg * wy
			acc[dx].b += lb * wy
			acc[dx].a += la * wy
		}
	case *image.YCbCr:
		for dx, sp := range xSpans {
			var lr, lg, lb, la float64
			for x := sp.start; x < sp.end; x++ {
				wx := 1.0
				if x == sp.start {
					wx = sp.wFirst
				} else if x == sp.end-1 {
					wx = sp.wLast
				}
				yc := s.YCbCrAt(sb.Min.X+x, y)
				r, g, b := color.YCbCrToRGB(yc.Y, yc.Cb, yc.Cr)
				lr += lut[r] * wx
				lg += lut[g] * wx
				lb += lut[b] * wx
				la += wx
			}
			acc[dx].r += lr * wy
			acc[dx].g += lg * wy
			acc[dx].b += lb * wy
			acc[dx].a += la * wy
		}
	default:
		for dx, sp := range xSpans {
			var lr, lg, lb, la float64
			for x := sp.start; x < sp.end; x++ {
				c := baseimage.NRGBAAt(src, sb.Min.X+x, y)
				if c.A == 0 {
					continue
				}
				wx := 1.0
				if x == sp.start {
					wx = sp.wFirst
				} else if x == sp.end-1 {
					wx = sp.wLast
				}
				if c.A == 255 {
					lr += lut[c.R] * wx
					lg += lut[c.G] * wx
					lb += lut[c.B] * wx
					la += wx
				} else {
					wa := wx * float64(c.A) / 255.0
					lr += lut[c.R] * wa
					lg += lut[c.G] * wa
					lb += lut[c.B] * wa
					la += wa
				}
			}
			acc[dx].r += lr * wy
			acc[dx].g += lg * wy
			acc[dx].b += lb * wy
			acc[dx].a += la * wy
		}
	}
}

// downsampleRasterImage 对缩小超过两倍的八位大图执行单遍线性空间面积盒滤波降采样并同步调整指令变换
// 入参: ctx 取消上下文, src 源像素图像, command 图像绘制指令, page 光栅页面, pageH 整页像素高度, lut 非线性转线性查表
// 返回: *image.NRGBA64 线性十六位降采样图像, RasterCommand 调整后的指令, bool 是否执行了预降采样, error 取消错误
func downsampleRasterImage(ctx context.Context, src image.Image, command RasterCommand, page *RasterPage, pageH int, lut *[256]float64) (*image.NRGBA64, RasterCommand, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, command, false, err
	}
	if lut == nil || baseimage.HighPrecision(src) {
		return nil, command, false, nil
	}
	if _, padded := src.(*rasterPaddedImage); padded {
		return nil, command, false, nil
	}
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 || int64(sw)*int64(sh) < 256*256 {
		return nil, command, false, nil
	}
	m := page.PixelTransform(command.Transform, pageH)
	sx, sy := math.Hypot(m[0], m[1]), math.Hypot(m[2], m[3])
	if !finite(sx) || !finite(sy) || sx <= 0 || sy <= 0 || sx >= 0.5 || sy >= 0.5 {
		return nil, command, false, nil
	}
	dw := max(1, int(math.Ceil(float64(sw)*sx*1.5)))
	dh := max(1, int(math.Ceil(float64(sh)*sy*1.5)))
	if dw >= sw || dh >= sh || int64(dw)*int64(dh)*2 >= int64(sw)*int64(sh) {
		return nil, command, false, nil
	}
	var palLinear []rasterLinearPixel
	if pal, ok := src.(*image.Paletted); ok {
		palLinear = make([]rasterLinearPixel, len(pal.Palette))
		for idx, c := range pal.Palette {
			nc := color.NRGBAModel.Convert(c).(color.NRGBA)
			if nc.A == 0 {
				continue
			}
			if nc.A == 255 {
				palLinear[idx] = rasterLinearPixel{r: lut[nc.R], g: lut[nc.G], b: lut[nc.B], a: 1}
			} else {
				af := float64(nc.A) / 255.0
				palLinear[idx] = rasterLinearPixel{r: lut[nc.R] * af, g: lut[nc.G] * af, b: lut[nc.B] * af, a: af}
			}
		}
	}
	xSpans := rasterBoxSpans(sw, dw)
	acc := make([]rasterLinearPixel, dw)
	down := image.NewNRGBA64(image.Rect(0, 0, dw, dh))
	yScale := float64(sh) / float64(dh)
	invArea := (float64(dw) * float64(dh)) / (float64(sw) * float64(sh))
	for dy := range dh {
		if err := ctx.Err(); err != nil {
			return nil, command, false, err
		}
		clear(acc)
		y0 := float64(dy) * yScale
		y1 := float64(dy+1) * yScale
		sy0 := int(math.Floor(y0))
		sy1 := min(sh, int(math.Ceil(y1)))
		for rowY := sy0; rowY < sy1; rowY++ {
			wy := min(float64(rowY+1), y1) - max(float64(rowY), y0)
			if wy <= 0 {
				continue
			}
			accumulateLinearRow(acc, src, sb, rowY, wy, xSpans, lut, palLinear)
		}
		dstRow := down.Pix[dy*down.Stride : dy*down.Stride+dw*8]
		for dx, p := range acc {
			a16 := rasterLinearUnit16(p.a * invArea)
			if a16 == 0 || p.a <= 0 {
				continue
			}
			invA := 1.0 / p.a
			r16 := rasterLinearUnit16(p.r * invA)
			g16 := rasterLinearUnit16(p.g * invA)
			b16 := rasterLinearUnit16(p.b * invA)
			off := dx * 8
			dstRow[off], dstRow[off+1] = byte(r16>>8), byte(r16)
			dstRow[off+2], dstRow[off+3] = byte(g16>>8), byte(g16)
			dstRow[off+4], dstRow[off+5] = byte(b16>>8), byte(b16)
			dstRow[off+6], dstRow[off+7] = byte(a16>>8), byte(a16)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, command, false, err
	}
	if sb.Min != (image.Point{}) {
		command.Transform[4] += command.Transform[0]*float64(sb.Min.X) + command.Transform[2]*float64(sb.Min.Y)
		command.Transform[5] += command.Transform[1]*float64(sb.Min.X) + command.Transform[3]*float64(sb.Min.Y)
	}
	scaleX, scaleY := float64(sw)/float64(dw), float64(sh)/float64(dh)
	command.Transform[0] *= scaleX
	command.Transform[1] *= scaleX
	command.Transform[2] *= scaleY
	command.Transform[3] *= scaleY
	return down, command, true, nil
}

// drawRasterImageCommandOffset 在带页面像素偏移的目标缓冲上绘制图像与裁剪蒙版
// 入参: ctx 取消上下文, backend 当前后端, page 页面, dst 目标图像, offset 目标左上角在整页像素中的偏移, pageH 整页像素高度, src 原始图像, command 图像指令, cache 蒙版缓存
// 返回: error 裁剪绘制或取消错误
func drawRasterImageCommandOffset(ctx context.Context, backend RasterBackend, page *RasterPage, dst draw.Image, offset image.Point, pageH int, src image.Image, command RasterCommand, cache *renderCache[[32]byte, *image.Alpha]) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := page.PixelTransform(command.Transform, pageH)
	localM := m
	localM[4] -= float64(offset.X)
	localM[5] -= float64(offset.Y)
	if command.Clip == nil {
		baseimage.DrawPixelsInterpolate(dst, src, localM, draw.Over, command.Interpolate)
		return ctx.Err()
	}
	mask, err := rasterClipMask(ctx, backend, page, command.Clip, cache)
	if err != nil {
		return err
	}
	if mask.Rect.Empty() {
		return nil
	}
	dstPageBounds := dst.Bounds().Add(offset)
	bounds := mask.Rect.Intersect(dstPageBounds).Intersect(baseimage.TransformBounds(src.Bounds(), m, dstPageBounds))
	if bounds.Empty() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var layer draw.Image
	if _, isRGBA := dst.(*image.RGBA); isRGBA && !baseimage.HighPrecision(src) {
		layer = image.NewRGBA(bounds)
	} else {
		layer = image.NewRGBA64(bounds)
	}
	baseimage.DrawPixelsInterpolate(layer, baseimage.PixelSource(src), m, draw.Src, command.Interpolate)
	if err := ctx.Err(); err != nil {
		return err
	}
	draw.DrawMask(dst, bounds.Sub(offset), layer, bounds.Min, mask, bounds.Min, draw.Over)
	return ctx.Err()
}

// cloneRasterPage 隔离可变指令，源图片按接口约定只读借用
// 入参: page 绘制页面
// 返回: *RasterPage 指令快照
func cloneRasterPage(page *RasterPage) *RasterPage {
	result := *page
	result.Commands = slices.Clone(page.Commands)
	for i := range result.Commands {
		cmd := &result.Commands[i]
		cmd.Path, cmd.Clip = slices.Clone(cmd.Path), slices.Clone(cmd.Clip)
		if cmd.Interpolate != nil {
			v := *cmd.Interpolate
			cmd.Interpolate = &v
		}
		if cmd.Stroke != nil {
			stroke := *cmd.Stroke
			stroke.Dashes = slices.Clone(stroke.Dashes)
			cmd.Stroke = &stroke
		}
		if cmd.Paint.Gradient != nil {
			gradient := *cmd.Paint.Gradient
			gradient.Stops = slices.Clone(gradient.Stops)
			if gradient.Mesh != nil {
				mesh := *gradient.Mesh
				mesh.Triangles = slices.Clone(mesh.Triangles)
				gradient.Mesh = &mesh
			}
			if gradient.Spread != nil {
				spread := *gradient.Spread
				gradient.Spread = &spread
			}
			cmd.Paint.Gradient = &gradient
		}
	}
	return &result
}

// PixelSize 检查页面尺寸并计算目标像素大小，合法正尺寸至少保留一个像素
// 返回: int 宽度, int 高度, error 无效尺寸
func (p *RasterPage) PixelSize() (int, int, error) {
	if p == nil || !rasterPositive(p.Width) || !rasterPositive(p.Height) || !rasterPositive(p.DPI) {
		return 0, 0, fmt.Errorf("invalid raster page size or DPI")
	}
	scale := p.DPI / 25.4
	if !rasterPositive(scale) {
		return 0, 0, fmt.Errorf("invalid raster page size or DPI")
	}
	w, h := math.Max(1, math.Floor(p.Width*scale+0.5)), math.Max(1, math.Floor(p.Height*scale+0.5))
	const maxRasterPagePixels = 256 << 20
	if !rasterPositive(w) || !rasterPositive(h) || w > 1<<20 || h > 1<<20 || !finite(w*h) || w*h > maxRasterPagePixels {
		return 0, 0, fmt.Errorf("invalid raster pixel dimensions: %g x %g", w, h)
	}
	return int(w), int(h), nil
}

// Bounds 度量已编译指令的页面坐标范围，不重新定位文字或读取图片像素
// 包含描边与指令裁剪，不应用页面边界，不排除图片透明像素或渐变透明区域
// 入参: geometry 几何后端
// 返回: Box 毫米坐标范围, error 几何或指令错误
func (p *RasterPage) Bounds(geometry GeometryBackend) (Box, error) {
	return p.BoundsContext(context.Background(), geometry)
}

// BoundsContext 按上下文度量只读绘制指令，取消或失败时不返回部分范围
// 入参: ctx 取消上下文, geometry 几何后端
// 返回: Box 毫米坐标范围, error 取消、几何或指令错误
func (p *RasterPage) BoundsContext(ctx context.Context, geometry GeometryBackend) (Box, error) {
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	if p == nil {
		return Box{}, fmt.Errorf("invalid raster page")
	}
	if geometry == nil {
		return Box{}, fmt.Errorf("raster bounds: %w", ErrBackendUnavailable)
	}
	var box Box
	for index, command := range p.Commands {
		if err := ctx.Err(); err != nil {
			return Box{}, err
		}
		path, err := rasterCommandGeometry(geometry, command)
		if err == nil {
			var bounds Box
			bounds, err = geometry.Bounds(path)
			box = unionTextBox(box, bounds)
		}
		if err != nil {
			return Box{}, fmt.Errorf("raster command %d bounds: %w", index, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	return box, nil
}

// PixelTransform 将页面毫米变换转换为像素变换，保留尺寸取整后的原点位置
// 入参: m 局部到页面变换, height 目标像素高度
// 返回: RasterMatrix 局部到像素变换
func (p *RasterPage) PixelTransform(m RasterMatrix, height int) RasterMatrix {
	scale := p.DPI / 25.4
	for i := range m {
		m[i] *= scale
	}
	m[5] += float64(height) - p.Height*scale
	return m
}

// Apply 将局部点映射到目标坐标
// 入参: p 局部点
// 返回: RasterPoint 目标点
func (m RasterMatrix) Apply(p RasterPoint) RasterPoint {
	return RasterPoint{m[0]*p.X + m[2]*p.Y + m[4], m[1]*p.X + m[3]*p.Y + m[5]}
}

// Inverse 求有限逆矩阵，复用几何变换的溢出、下溢及相消处理
// 返回: RasterMatrix 逆矩阵, error 无效矩阵
func (m RasterMatrix) Inverse() (RasterMatrix, error) {
	inverse, ok := MatrixFromValues([6]float64(m)).Invert()
	if !ok {
		return RasterMatrix{}, fmt.Errorf("invalid raster transform")
	}
	return RasterMatrix(inverse.Values()), nil
}

// At 采样局部坐标下的渐变，使用预乘颜色插值
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 采样颜色
func (g *RasterGradient) At(x, y float64) color.RGBA {
	if g.Kind == RasterMesh {
		return g.Mesh.At(x, y)
	}
	dx, dy := g.End.X-g.Start.X, g.End.Y-g.Start.Y
	px, py := x-g.Start.X, y-g.Start.Y
	t := 0.0
	if g.Kind == RasterRadial {
		dr := g.R1 - g.R0
		a, b, c := dx*dx+dy*dy-dr*dr, -2*(px*dx+py*dy+g.R0*dr), px*px+py*py-g.R0*g.R0
		if g.Spread != nil && math.Hypot(dx, dy) >= math.Abs(dr) {
			var ok bool
			t, ok = rasterRadialDomain(a, b, c, g.R0, dr, g.Spread.Extend)
			if !ok {
				return color.RGBA{}
			}
		} else {
			t = rasterRadialPosition(a, b, c, g.R0, dr)
			if g.Spread != nil && math.Hypot(dx, dy) < math.Abs(dr) {
				d := b*b - 4*a*c
				if d < 0 {
					return color.RGBA{}
				}
				t = (-b - math.Copysign(math.Sqrt(d), dr)) / (2 * a)
			}
		}
	} else if length := dx*dx + dy*dy; length > 0 {
		t = (px*dx + py*dy) / length
	}
	if spread := g.Spread; spread != nil {
		if t < 0 && spread.Extend&1 == 0 || t > 1 && spread.Extend&2 == 0 {
			return color.RGBA{}
		}
		period := spread.Period
		if period <= 0 {
			period = 1
		}
		switch spread.MapType {
		case "Repeat":
			t = math.Mod(t, period)
			if t < 0 {
				t += period
			}
			t /= period
		case "Reflect":
			t = reflectShdPosition(math.Mod(t, 2*period) / period)
		}
	}
	return g.colorAt(t)
}

// PreparePage 编译并准备当前配置的独立页面快照，按后端能力复用未修改指令
// 每次重新解释源页面，按内容使模板、签章、字体及样式派生数据失效
// 入参: page 源页面
// 返回: RasterScene 可重复绘制场景, error 编译或准备错误
func (r *Renderer) PreparePage(page *PageContent) (RasterScene, error) {
	return r.PreparePageContext(context.Background(), page)
}

// PreparePageContext 按取消上下文编译并准备当前配置的独立页面快照
// 入参: ctx 取消上下文，nil使用后台上下文, page 源页面
// 返回: RasterScene 可重复绘制场景, error 编译、准备或取消错误
func (r *Renderer) PreparePageContext(ctx context.Context, page *PageContent) (RasterScene, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.backends.Raster == nil {
		return nil, fmt.Errorf("raster scene: %w", ErrBackendUnavailable)
	}
	compiled, err := r.CompilePageContext(ctx, page)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backend, ok := r.backends.Raster.(RasterSceneReuseBackend); ok {
		state, _ := r.backendStates[rasterPreparationKey{}].(rasterPreparationState)
		var previous RasterScene
		if sameBackend(state.backend, r.backends.Raster) {
			previous = state.scene
		}
		scene, err := backend.PrepareReuse(compiled, previous)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.backendStates[rasterPreparationKey{}] = rasterPreparationState{backend: r.backends.Raster, scene: scene}
		return scene, nil
	}
	scene, err := PrepareRasterScene(r.backends.Raster, compiled)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scene, nil
}

// Render 绘制固定快照
// 返回: image.Image 独立图像, error 绘制错误
func (s *rasterScene) Render() (image.Image, error) {
	return s.backend.Render(context.Background(), s.page)
}

// colorAt 插值有序颜色节点
// 入参: t 渐变位置
// 返回: color.RGBA 预乘颜色
func (g *RasterGradient) colorAt(t float64) color.RGBA {
	if g.Sample != nil {
		return g.Sample(t)
	}
	if len(g.Stops) == 0 {
		return color.RGBA{}
	}
	if t <= g.Stops[0].Offset {
		return g.Stops[0].Color
	}
	for i := 1; i < len(g.Stops); i++ {
		before, after := g.Stops[i-1], g.Stops[i]
		if t < after.Offset {
			u := uint32((t-before.Offset)/(after.Offset-before.Offset)*65535 + 0.5)
			blend := func(a, b uint8) uint8 { return uint8(((65535-u)*uint32(a)*257 + u*uint32(b)*257) >> 24) }
			return color.RGBA{blend(before.Color.R, after.Color.R), blend(before.Color.G, after.Color.G), blend(before.Color.B, after.Color.B), blend(before.Color.A, after.Color.A)}
		}
	}
	return g.Stops[len(g.Stops)-1].Color
}
