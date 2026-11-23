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
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"

	basethreed "github.com/xiaoqidun/ofdgo/internal/base/threed"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfModelAttachmentLimit 限制单次转换保留的共享模型索引数量
const pdfModelAttachmentLimit = 1024

// pdfThreeDMarkupState 复用同页批注的宿主解析及模型校验，不保留模型解码缓冲
type pdfThreeDMarkupState struct {
	sources map[pdfgo.Reference]pdfgo.ThreeD
	sums    map[*pdfgo.Stream][16]byte
}

// threeDAnnotation 按非交互呈现规则处理激活状态，保留原始模型与附件动作
// 入参: ctx 取消上下文, page PDF页面, annotation 三维注解
// 返回: error 外观、资源或取消错误
func (p *pdfImporter) threeDAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation) error {
	source, err := p.reader.ReadThreeDContext(ctx, annotation)
	if err != nil {
		return p.interactiveAppearanceFallback(ctx, page, annotation, "metadata", err)
	}
	var data []byte
	id := p.modelAttachments[source.Stream]
	if id == "" {
		data, err = p.reader.DecodeStreamContext(ctx, source.Stream)
		if err != nil {
			return p.interactiveAppearanceFallback(ctx, page, annotation, "model", err)
		}
		id, err = p.editor.AddAttachment(fmt.Sprintf("Model_%d.%s", p.page+1, strings.ToLower(string(source.Format))), data)
		if err != nil {
			return err
		}
		if p.modelAttachments == nil {
			p.modelAttachments = make(map[*pdfgo.Stream]string)
		}
		if len(p.modelAttachments) < pdfModelAttachmentLimit {
			p.modelAttachments[source.Stream] = id
		}
	}
	action := Action{Event: "CLICK", GotoA: &GotoA{AttachID: id}}
	appearance, err := p.reader.ReadAnnotationAppearance(annotation)
	if err != nil {
		return p.interactiveAppearanceFallback(ctx, page, annotation, "appearance", err)
	}
	hasAppearance := appearance != nil
	var paint func(*pdfImporter) error
	hasStaticView := false
	if source.ActivationPolicy.Activation != "XA" {
		if data == nil {
			data, err = p.editor.source.reader.AttachmentData(id)
			if err != nil {
				return err
			}
		}
		paint, err = p.threeDPainter(ctx, page, source, annotation, data)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if hasAppearance {
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D rendering unavailable; normal appearance retained: " + err.Error()})
			} else {
				p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D rendering unavailable; normal appearance missing: " + err.Error()})
			}
		} else {
			hasStaticView = paint != nil
		}
	}
	if !hasStaticView && !hasAppearance {
		if err := p.recoverPDFObject(ctx, "Annotation.Appearance", annotation.Dictionary, fmt.Errorf("missing PDF 3D normal appearance")); err != nil {
			return err
		}
		paint = func(*pdfImporter) error { return nil }
	}
	if err := p.paintedAnnotation(ctx, page, annotation, paint, action); err != nil {
		return err
	}
	message := "PDF 3D original model retained; normal appearance missing and interactive rendering not transferred to OFD"
	if hasStaticView {
		message = "PDF 3D static view and original model retained; interactive rendering not transferred to OFD"
	} else if hasAppearance {
		message = "PDF 3D appearance and original model retained; advanced interaction not transferred to OFD"
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: message})
	return nil
}

// threeDPainter 准备三维静态图像，复用库层压缩，失败时不提交注解对象
// 入参: ctx 取消上下文, page PDF页面, source 三维参数, annotation 注解, data 原始模型
// 返回: func(*pdfImporter) error 绘制回调, error 模型、绘制或资源错误
func (p *pdfImporter) threeDPainter(ctx context.Context, page *pdfgo.Page, source pdfgo.ThreeD, annotation pdfgo.Annotation, data []byte) (func(*pdfImporter) error, error) {
	box := source.ViewBox
	width, height := box.XMax-box.XMin, box.YMax-box.YMin
	x, y := annotation.Rect.XMin/2+annotation.Rect.XMax/2, annotation.Rect.YMin/2+annotation.Rect.YMax/2
	var nodes []pdfCompositeNode
	if source.Background.RGB == nil {
		return nil, &pdfgo.UnsupportedError{Feature: "3D background color space"}
	}
	if source.Background.EntireAnnotation {
		r := annotation.Rect
		path := pdfgo.Path{Segments: []pdfgo.Segment{
			{Operator: "M", Points: []pdfgo.Point{{X: r.XMin, Y: r.YMin}}},
			{Operator: "L", Points: []pdfgo.Point{{X: r.XMax, Y: r.YMin}}},
			{Operator: "L", Points: []pdfgo.Point{{X: r.XMax, Y: r.YMax}}},
			{Operator: "L", Points: []pdfgo.Point{{X: r.XMin, Y: r.YMax}}},
			{Operator: "C"},
		}}
		nodes = append(nodes, pdfCompositeNode{path: &pdfgo.PathMark{Path: path, Fill: true, Style: pdfgo.Style{Fill: pdfgo.Paint{RGB: *source.Background.RGB, Alpha: 1}}}})
	}
	if width > 0 && height > 0 {
		var overlay []pdfCompositeNode
		if source.Presentation.Overlay != nil {
			var err error
			overlay, err = p.collectCompositeNodes(func(visitor pdfgo.Visitor) error {
				return p.reader.WalkThreeDOverlay(ctx, page, source, visitor)
			}, false)
			if err != nil {
				return nil, err
			}
			source.Presentation.Overlay = nil
		}
		if source.Format != "U3D" {
			return nil, &pdfgo.UnsupportedError{Feature: "3D model format " + string(source.Format)}
		}
		model, err := basethreed.DecodeU3D(ctx, data, basethreed.U3DOptions{})
		if err != nil {
			return nil, err
		}
		widthMM := width * math.Hypot(p.matrix[0], p.matrix[1])
		heightMM := height * math.Hypot(p.matrix[2], p.matrix[3])
		const pdfMaxThreeDPixels = 64 << 20
		w, h := math.Ceil(widthMM*p.rasterDPI/25.4), math.Ceil(heightMM*p.rasterDPI/25.4)
		if !finite(w) || !finite(h) || w <= 0 || h <= 0 || w > 1<<30 || h > 1<<30 {
			return nil, fmt.Errorf("invalid PDF 3D raster dimensions")
		}
		if w*h > pdfMaxThreeDPixels {
			if p.warning == nil {
				return nil, fmt.Errorf("PDF 3D raster pixels exceed limit")
			}
			scale := math.Sqrt(pdfMaxThreeDPixels / (w * h))
			w, h = math.Max(1, math.Floor(w*scale)), math.Max(1, math.Floor(h*scale))
			p.addWarning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("oversized PDF 3D raster scaled to %dx%d", int(w), int(h))})
		}
		options := codecpdf.ThreeDRenderOptions{Width: int(w), Height: int(h)}
		if p.resolveFile != nil {
			options.ResolveTexture = func(ctx context.Context, address string) ([]byte, error) {
				return p.resolveFile(ctx, pdfgo.FileSpecification{Name: address, FileSystem: "URL"})
			}
		}
		img, err := codecpdf.RenderThreeD(ctx, model, source, options)
		if err != nil {
			return nil, err
		}
		resource, err := p.reader.CreateImage(ctx, img)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, pdfCompositeNode{image: &pdfgo.ImageMark{Image: resource, Matrix: pdfgo.Matrix{width, 0, 0, height, x + box.XMin, y + box.YMin}, Style: pdfgo.Style{Fill: pdfgo.Paint{Alpha: 1}}}})
		nodes = append(nodes, overlay...)
	}
	return func(stamp *pdfImporter) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return stamp.compositeObjects(nodes)
	}, nil
}

// threeDMarkupAnnotation 应用默认三维视图的批注可见性，其他视图批注保留为隐藏注解
// 入参: ctx 取消上下文, annotation 批注, markup 关联, state 同页缓存, strict 严格检查开关
// 返回: pdfgo.Annotation 静态批注, error 关联、校验或取消错误
func (p *pdfImporter) threeDMarkupAnnotation(ctx context.Context, annotation pdfgo.Annotation, markup *pdfgo.ThreeDMarkup, state *pdfThreeDMarkupState, strict bool) (pdfgo.Annotation, error) {
	if markup == nil {
		return annotation, nil
	}
	if strict {
		return annotation, &pdfgo.UnsupportedError{Feature: "3D markup view switching conversion"}
	}
	source, ok := state.sources[markup.Annotation.Reference]
	if !ok || markup.Annotation.Reference == (pdfgo.Reference{}) {
		var err error
		source, err = p.reader.ReadThreeDContext(ctx, markup.Annotation)
		if err != nil {
			return annotation, p.recoverPDFObject(ctx, "Annotation.ExData", annotation.Dictionary["ExData"], err)
		}
		if markup.Annotation.Reference != (pdfgo.Reference{}) {
			if state.sources == nil {
				state.sources = make(map[pdfgo.Reference]pdfgo.ThreeD)
			}
			state.sources[markup.Annotation.Reference] = source
		}
	}
	if markup.Checksum != nil {
		sum, ok := state.sums[source.Stream]
		if !ok {
			var err error
			sum, err = p.reader.ThreeDArtworkChecksum(ctx, source.Stream)
			if err != nil {
				return annotation, p.recoverPDFObject(ctx, "Annotation.ExData", annotation.Dictionary["ExData"], err)
			}
			if state.sums == nil {
				state.sums = make(map[*pdfgo.Stream][16]byte)
			}
			state.sums[source.Stream] = sum
		}
		if sum != *markup.Checksum {
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D markup artwork checksum differs; view association may be stale"})
		}
	}
	visible := source.ActivationPolicy.Activation != "XA" && markup.MatchesView(source.SelectedView)
	if !visible {
		flags, err := p.reader.ReadAnnotationFlags(annotation)
		if err != nil {
			return annotation, p.recoverPDFObject(ctx, "Annotation.F", annotation.Dictionary["F"], err)
		}
		annotation.Dictionary = maps.Clone(annotation.Dictionary)
		annotation.Dictionary["F"] = pdfgo.Integer(flags | 2)
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D markup default view visibility applied; view switching not transferred"})
	return annotation, ctx.Err()
}
