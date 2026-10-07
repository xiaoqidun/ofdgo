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
	"image"
	"math"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// pdfModelAttachmentLimit 限制单次转换保留的共享模型索引数量
const pdfModelAttachmentLimit = 1024

// PDFThreeDRenderOptions 控制PDF三维视口的静态绘制，不改变注解激活状态
// 缓存与实例上限为0时采用U3DRenderOptions的默认值
type PDFThreeDRenderOptions struct {
	Width, Height  int
	MaxMemoryBytes int64
	MaxInstances   int
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
		data, err = source.Stream.DecodeContext(ctx)
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
	var paint func(*pdfImporter) error
	if source.ActivationPolicy.Activation != "XA" {
		appearance, err := p.reader.ReadAnnotationAppearance(annotation)
		if err != nil {
			return p.interactiveAppearanceFallback(ctx, page, annotation, "appearance", err)
		}
		if appearance == nil {
			return fmt.Errorf("missing PDF 3D normal appearance")
		}
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
			p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF 3D rendering unavailable; normal appearance retained: " + err.Error()})
		}
	}
	if err := p.paintedAnnotation(ctx, page, annotation, paint, action); err != nil {
		return err
	}
	message := "PDF 3D appearance and original model retained; advanced interaction not transferred to OFD"
	if paint != nil {
		message = "PDF 3D static view and original model retained; interactive rendering not transferred to OFD"
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
			})
			if err != nil {
				return nil, err
			}
			source.Presentation.Overlay = nil
		}
		if source.Format != "U3D" {
			return nil, &pdfgo.UnsupportedError{Feature: "3D model format " + string(source.Format)}
		}
		model, err := DecodeU3D(ctx, data, U3DOptions{})
		if err != nil {
			return nil, err
		}
		widthMM := width * math.Hypot(p.matrix[0], p.matrix[1])
		heightMM := height * math.Hypot(p.matrix[2], p.matrix[3])
		w, h := math.Ceil(widthMM*p.rasterDPI/25.4), math.Ceil(heightMM*p.rasterDPI/25.4)
		if !finite(w) || !finite(h) || w <= 0 || h <= 0 || w > 1<<30 || h > 1<<30 {
			return nil, fmt.Errorf("invalid PDF 3D raster dimensions")
		}
		img, err := RenderPDFThreeD(ctx, model, source, PDFThreeDRenderOptions{Width: int(w), Height: int(h)})
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

// RenderPDFThreeD 按PDF相机、投影、背景与灯光绘制3DB区域，不执行脚本或动画
// 返回图像不含3DB以外的注解背景，未支持的视图效果返回明确错误
// 每次从传入场景建立独立节点状态，不继承此前绘制的运行时状态
// 入参: ctx 取消上下文, model 解码后的U3D模型, source PDF三维参数, options 输出与资源限制
// 返回: *image.RGBA 不透明图像, error 参数、能力、预算或取消错误
func RenderPDFThreeD(ctx context.Context, model *U3DModel, source pdfgo.ThreeD, options PDFThreeDRenderOptions) (*image.RGBA, error) {
	if ctx == nil {
		return nil, fmt.Errorf("invalid PDF 3D render context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source.Format != "U3D" {
		return nil, &pdfgo.UnsupportedError{Feature: "3D model format " + string(source.Format)}
	}
	if len(source.Measurements) != 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "3D view measurement rendering"}
	}
	presentation := source.Presentation
	if presentation.Overlay != nil {
		return nil, &pdfgo.UnsupportedError{Feature: "3D view overlay"}
	}
	if source.Background.RGB == nil {
		return nil, &pdfgo.UnsupportedError{Feature: "3D background color space"}
	}
	box := source.ViewBox
	width, height := box.XMax-box.XMin, box.YMax-box.YMin
	if !finite(width) || !finite(height) || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid PDF 3D viewport")
	}
	projection := pdfgo.ThreeDProjection{Subtype: "P", Clipping: "ANF", FieldOfView: 90, PerspectiveBinding: "W"}
	if source.Projection != nil {
		projection = *source.Projection
	}
	render := U3DRenderOptions{Width: options.Width, Height: options.Height, MaxMemoryBytes: options.MaxMemoryBytes, MaxInstances: options.MaxInstances, Background: source.Background.RGB}
	if err := pdfThreeDRenderMode(source, &render); err != nil {
		return nil, err
	}
	if err := pdfThreeDNodeStates(ctx, presentation.Nodes, &render); err != nil {
		return nil, err
	}
	if err := pdfThreeDSections(ctx, presentation.CrossSections, &render); err != nil {
		return nil, err
	}
	r, err := newU3DRender(ctx, model, render)
	if err != nil {
		return nil, err
	}
	camera, view, err := r.pdfCamera(source.Camera)
	if err != nil {
		return nil, err
	}
	render.View = view
	camera.ScaleX, camera.ScaleY, camera.Near = 1, 1, 1
	render.Camera = &camera
	passes, err := r.selectView(render)
	if err != nil {
		return nil, err
	}
	if err := r.prepareSections(render.Sections); err != nil {
		return nil, err
	}
	scale, err := projection.ScaleFactor(source.ViewBox, source.AnnotationBox)
	if err != nil {
		return nil, err
	}
	camera.Perspective = projection.Subtype == "P"
	camera.ScaleX = scale * float64(options.Width) / width
	camera.ScaleY = scale * float64(options.Height) / height
	switch projection.Clipping {
	case "XNF":
		camera.Near = 0
		if projection.Near != nil {
			camera.Near = *projection.Near
		}
		if !finite(camera.Near) || camera.Near < 0 || camera.Perspective && camera.Near == 0 {
			return nil, fmt.Errorf("invalid PDF 3D near distance")
		}
		if projection.Far != nil {
			camera.ClipFar = true
			camera.Far = *projection.Far
			if !finite(camera.Far) {
				return nil, fmt.Errorf("invalid PDF 3D far distance")
			}
			if camera.Far < camera.Near {
				passes, camera.Far = nil, 0
				camera.ClipFar = false
			}
		}
	case "ANF":
		minimum, maximum, found, err := r.depthRange()
		if err != nil {
			return nil, err
		}
		if !found {
			passes = nil
		}
		camera.Near = minimum
		if camera.Perspective {
			if maximum <= 0 {
				passes = nil
			}
			camera.Near = max(1e-300, maximum*1e-9)
			if minimum > 0 {
				camera.Near = max(1e-300, minimum/2)
			}
		}
		camera.Far = math.Nextafter(maximum, math.Inf(1))
		if !finite(camera.Far) || camera.Far <= camera.Near {
			camera.Far = 0
		}
	default:
		return nil, fmt.Errorf("invalid PDF 3D clipping style")
	}
	if _, err := r.selectView(render); err != nil {
		return nil, err
	}
	if source.Lighting.Subtype != "Artwork" && source.Lighting.Subtype != "" {
		if err := r.reserve(uint64(len(source.Lighting.Lights)), 56); err != nil {
			return nil, err
		}
		render.Lighting = &U3DLighting{DiffuseOffset: source.Lighting.Ambient, Lights: make([]U3DDirectionalLight, len(source.Lighting.Lights))}
		for i, light := range source.Lighting.Lights {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			direction := light.Direction
			if light.CameraAttached {
				direction = [3]float64{0, 0, 1}
			}
			render.Lighting.Lights[i] = U3DDirectionalLight{Color: light.Color, Direction: direction, CameraRelative: light.CameraAttached}
		}
	}
	return r.render(render, passes)
}

// pdfCamera 解析PDF相机来源并保留模型视图的绘制轮次
// 入参: source 相机来源
// 返回: U3DCamera 相机变换, string 视图节点, error 路径或变换错误
func (r *u3dRender) pdfCamera(source pdfgo.ThreeDCamera) (U3DCamera, string, error) {
	var camera U3DCamera
	selected := -1
	for i, node := range r.model.Nodes {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return camera, "", err
			}
		}
		if node.Kind == "View" {
			selected = i
			break
		}
	}
	if source.Source == "M" {
		m := source.Matrix
		camera.ToWorld = [16]float64{m[0], m[1], m[2], 0, m[3], m[4], m[5], 0, m[6], m[7], m[8], 0, m[9], m[10], m[11], 1}
	} else {
		if source.Source != "" && source.Source != "U3D" || source.Source == "U3D" && len(source.Path) == 0 {
			return camera, "", fmt.Errorf("invalid PDF 3D camera source")
		}
		if source.Source == "U3D" {
			selected = -1
			for i, node := range r.model.Nodes {
				if i%256 == 0 {
					if err := r.ctx.Err(); err != nil {
						return camera, "", err
					}
				}
				if node.Name == source.Path[len(source.Path)-1] {
					selected = i
					break
				}
			}
		}
		if selected < 0 || r.model.Nodes[selected].Kind != "View" {
			return camera, "", fmt.Errorf("missing PDF 3D camera node")
		}
		found := false
		for _, index := range r.byNode[selected] {
			if err := r.ctx.Err(); err != nil {
				return camera, "", err
			}
			current, match := index, true
			for j := len(source.Path) - 1; j >= 0; j-- {
				if j%256 == 0 {
					if err := r.ctx.Err(); err != nil {
						return camera, "", err
					}
				}
				if current < 0 || r.model.Nodes[r.scene[current].node].Name != source.Path[j] || r.model.Nodes[r.scene[current].node].Kind != "View" {
					match = false
					break
				}
				current = r.scene[current].parent
			}
			if source.Source == "U3D" && current >= 0 {
				match = false
			}
			if !match {
				continue
			}
			if found {
				return camera, "", &pdfgo.UnsupportedError{Feature: "ambiguous 3D camera instance"}
			}
			camera.ToWorld, found = r.scene[index].world, true
		}
		if !found {
			return camera, "", fmt.Errorf("missing PDF 3D camera path")
		}
	}
	view := ""
	if selected >= 0 {
		view = r.model.Nodes[selected].Name
	}
	return camera, view, nil
}

// depthRange 计算可见模型几何的相机深度范围，不为自动裁剪复制顶点
// 返回: float64 最近深度, float64 最远深度, bool 是否含几何, error 几何或取消错误
func (r *u3dRender) depthRange() (float64, float64, bool, error) {
	minimum, maximum := math.Inf(1), math.Inf(-1)
	for i, instance := range r.scene {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return 0, 0, false, err
			}
		}
		node := r.model.Nodes[instance.node]
		index, ok := r.meshes[node.Resource]
		if node.Kind != "Model" || instance.modelVisibility(&node) == 0 || instance.opacity == 0 || !ok {
			continue
		}
		mesh := &r.model.Meshes[index]
		matrix := r.toCamera.multiply(instance.world)
		for j := range len(mesh.Faces) + len(mesh.Lines) {
			_, corners := mesh.primitive(j)
			if j%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return 0, 0, false, err
				}
			}
			for _, corner := range corners {
				if uint64(corner.Position) >= uint64(len(mesh.Positions)) {
					return 0, 0, false, fmt.Errorf("invalid U3D position index")
				}
				p := mesh.Positions[corner.Position]
				z := matrix[2]*float64(p[0]) + matrix[6]*float64(p[1]) + matrix[10]*float64(p[2]) + matrix[14]
				if !finite(z) {
					return 0, 0, false, fmt.Errorf("invalid U3D camera depth")
				}
				minimum, maximum = min(minimum, z), max(maximum, z)
			}
		}
	}
	for i, section := range r.sections {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return 0, 0, false, err
			}
		}
		if section.opacity > 0 {
			for _, corner := range section.corners {
				minimum, maximum = min(minimum, corner.point[2]), max(maximum, corner.point[2])
			}
		}
	}
	if math.IsInf(minimum, 1) {
		return 0, 0, false, nil
	}
	return minimum, maximum, true, nil
}

// pdfThreeDNodeStates 将PDF节点参数转为场景覆盖，转换缓存计入绘制预算
// 入参: ctx 取消上下文, nodes 节点参数, options 绘制选项
// 返回: error 预算或取消错误
func pdfThreeDNodeStates(ctx context.Context, nodes []pdfgo.ThreeDNode, options *U3DRenderOptions) error {
	if len(nodes) == 0 {
		return nil
	}
	limit := options.MaxMemoryBytes
	if limit == 0 {
		limit = u3dDefaultBytes
	}
	if limit <= 0 || uint64(len(nodes)) >= uint64(limit)/168 {
		return fmt.Errorf("PDF 3D node memory exceeds limit")
	}
	options.MaxMemoryBytes = limit - int64(len(nodes))*168
	options.Nodes = make([]U3DNodeState, len(nodes))
	for i, node := range nodes {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		state := U3DNodeState{Name: node.Name, Opacity: node.Opacity, Visible: node.Visible}
		if node.Matrix != nil {
			m := node.Matrix
			state.Matrix = &[16]float64{m[0], m[1], m[2], 0, m[3], m[4], m[5], 0, m[6], m[7], m[8], 0, m[9], m[10], m[11], 1}
		}
		options.Nodes[i] = state
	}
	return nil
}
