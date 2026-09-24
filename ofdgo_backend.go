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
	"errors"
	"fmt"
	"image"
	"io"
	"reflect"
)

// ErrBackendUnavailable 表示未配置所需后端，不会自动切换其他实现
var ErrBackendUnavailable = errors.New("render backend unavailable")

// Backend 提供稳定的后端标识，不包含前端文案
type Backend interface {
	Name() string
}

// RasterBackend 消费只读页面，返回独立图像，不得静默跳过指令或切换其他后端
type RasterBackend interface {
	Backend
	Render(*RasterPage) (image.Image, error)
}

// PageCompiler 统一页面编译、文字提取和对象度量，不得修改源数据
// 三项能力需采用相同的字体和几何语义，避免绘制、搜索与选区不一致
// renderer提供文档、字体配置和文字回调，实现中不得递归调用对应的Renderer方法
// CompilePage在配置OnPageText时需同步回报文字，PageText和MeasureObject不触发回调
type PageCompiler interface {
	Backend
	CompilePage(renderer *Renderer, page *PageContent) (*RasterPage, error)
	PageText(renderer *Renderer, page *PageContent) (*PageText, error)
	MeasureObject(renderer *Renderer, object GraphicObject, options MeasureOptions) (ObjectMeasurement, error)
}

// MeasureOptions 描述对象度量上下文，坐标以页面左上角为原点，单位为毫米
// Defaults为继承样式，Parent为父变换，BoundaryInCTM指定边界是否参与父变换
// Clip为页面坐标中的非零填充裁剪，nil表示不裁剪，空路径表示全部裁去
// Contours决定是否收集填充轮廓，关闭时文字采用逐字符范围
type MeasureOptions struct {
	Defaults      *DrawParam
	Parent        *Matrix
	BoundaryInCTM bool
	Clip          *GeometryPath
	Contours      bool
}

// ObjectMeasurement 保存对象在页面坐标中的范围与可选轮廓
type ObjectMeasurement struct {
	Bounds   Box
	Contours []ObjectContour
}

// SVGMode 指定SVG资源封装方式，Objects模式还需保留稳定的对象分组
type SVGMode uint8

const (
	SVGEmbedded SVGMode = iota
	SVGExternalFonts
	SVGExternalResources
	SVGObjects
)

// SVGBackend 输出SVG及资源，不得修改源页面或省略不支持的内容
// renderer提供文档和排版能力，输出时不得再次调用RenderToSVG系列方法
type SVGBackend interface {
	Backend
	RenderSVG(renderer *Renderer, page *PageContent, writer io.Writer, mode SVGMode) (SVGResources, error)
}

// RenderDocumentPage 保存已解析页面和物理区域，不得修改源页面内容
type RenderDocumentPage struct {
	Content *PageContent
	Box     Box
}

// PDFBackend 输出完整PDF，保留文字、链接、目录和文档信息
// pages交由后端消费，可逐页清空Content以释放引用，不得改变页面顺序
// progress返回错误时应停止输出，调用方在失败后丢弃输出
type PDFBackend interface {
	Backend
	RenderPDF(renderer *Renderer, pages []RenderDocumentPage, writer io.Writer, progress func(int, int) error) error
}

// EPSBackend 输出单页EPS，不得修改源页面
type EPSBackend interface {
	Backend
	RenderEPS(renderer *Renderer, page *PageContent, writer io.Writer) error
}

// RenderBackends 按职责组合后端，nil表示明确禁用，不隐式回退
// 实例及其Renderer需串行使用，第三方实现无需引用任何内置绘图库
type RenderBackends struct {
	Fonts    FontBackend
	Geometry GeometryBackend
	Compiler PageCompiler
	Raster   RasterBackend
	SVG      SVGBackend
	PDF      PDFBackend
	EPS      EPSBackend
}

// BackendInfo 描述各项能力实际使用的后端，空标识表示未配置
type BackendInfo struct {
	Fonts    string `json:"fonts"`
	Geometry string `json:"geometry"`
	Compiler string `json:"compiler"`
	Raster   string `json:"raster"`
	SVG      string `json:"svg"`
	PDF      string `json:"pdf"`
	EPS      string `json:"eps"`
}

// NewRenderBackends 创建内置后端组合，各项能力通过Info报告实际提供者
// 入参: name 内置后端标识
// 返回: RenderBackends 后端组合, error 未知后端错误
func NewRenderBackends(name string) (RenderBackends, error) {
	if name != "canvas" {
		return RenderBackends{}, fmt.Errorf("unknown render backend %q", name)
	}
	return defaultRenderBackends(), nil
}

// RenderBackendInfos 列出内置组合及其实际能力，供原生程序和WASM共用
// 返回: map[string]BackendInfo 后端能力
func RenderBackendInfos() map[string]BackendInfo {
	return map[string]BackendInfo{"canvas": defaultRenderBackends().Info()}
}

// defaultRenderBackends 创建独立的默认配置
// 返回: RenderBackends 默认后端组合
func defaultRenderBackends() RenderBackends {
	backend := CanvasBackend{}
	return RenderBackends{Fonts: backend, Geometry: backend, Compiler: backend, Raster: backend, SVG: backend, PDF: backend, EPS: backend}
}

// Info 返回配置中各能力的提供者，不依赖WebUI推断
// 返回: BackendInfo 后端能力
func (b RenderBackends) Info() BackendInfo {
	return BackendInfo{Fonts: backendName(b.Fonts), Geometry: backendName(b.Geometry), Compiler: backendName(b.Compiler), Raster: backendName(b.Raster), SVG: backendName(b.SVG), PDF: backendName(b.PDF), EPS: backendName(b.EPS)}
}

// backendName 获取已配置的后端标识
// 入参: backend 后端实例
// 返回: string 后端标识，未配置时为空
func backendName(backend Backend) string {
	if backend == nil {
		return ""
	}
	return backend.Name()
}

// WithRenderBackends 替换完整后端组合，未配置的能力不会沿用默认实现
// 入参: backends 后端组合
// 返回: RendererOption 渲染选项
func WithRenderBackends(backends RenderBackends) RendererOption {
	return func(r *Renderer) {
		fontsChanged := !sameBackend(r.backends.Fonts, backends.Fonts)
		r.backends = backends
		if fontsChanged {
			r.resetFontBackendCache()
		}
	}
}

// sameBackend 判断不可变值配置是否未变，包含指针的配置重新应用时刷新可变状态
// 入参: left 原后端, right 新后端
// 返回: bool 是否为同一配置
func sameBackend(left, right Backend) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	a, b := reflect.ValueOf(left), reflect.ValueOf(right)
	return a.Comparable() && b.Comparable() && left == right && immutableBackendValue(a)
}

// immutableBackendValue 检查嵌套配置是否仅包含不可变值，不绑定具体后端类型
// 入参: value 配置值
// 返回: bool 是否可按值比较复用
func immutableBackendValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Map, reflect.Slice, reflect.Func:
		return false
	case reflect.Interface:
		return value.IsNil() || immutableBackendValue(value.Elem())
	case reflect.Struct:
		for i := range value.NumField() {
			if !immutableBackendValue(value.Field(i)) {
				return false
			}
		}
	case reflect.Array:
		for i := range value.Len() {
			if !immutableBackendValue(value.Index(i)) {
				return false
			}
		}
	}
	return true
}

// Backends 返回当前后端配置副本，可修改后通过WithRenderBackends应用
// 返回: RenderBackends 后端组合
func (r *Renderer) Backends() RenderBackends {
	return r.backends
}

// WithRasterBackend 选择当前渲染器的图像、PNG和JPEG后端，不改变SVG、PDF或OFD保存
// 入参: backend 光栅后端，nil禁用图像输出
// 返回: RendererOption 渲染选项
func WithRasterBackend(backend RasterBackend) RendererOption {
	return func(r *Renderer) { r.backends.Raster = backend }
}

// CompilePage 编译与后端无关的光栅页面，可交给任意RasterBackend绘制
// 字形、描边、裁剪和底纹沿用文档语义，不交由后端重新排版
// 入参: page 页面内容
// 返回: *RasterPage 只读绘制页面, error 编译错误
func (r *Renderer) CompilePage(page *PageContent) (*RasterPage, error) {
	if r.backends.Compiler == nil {
		return nil, fmt.Errorf("page compiler: %w", ErrBackendUnavailable)
	}
	return r.backends.Compiler.CompilePage(r, page)
}
