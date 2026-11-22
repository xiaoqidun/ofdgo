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
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	basethreed "github.com/xiaoqidun/ofdgo/internal/base/threed"
	"github.com/xiaoqidun/pdfgo"
)

// ThreeDRenderOptions 控制PDF三维视口的静态绘制，不改变注解激活状态
// 缓存与实例上限为0时采用basethreed.U3DRenderOptions的默认值
// ResolveTexture按原始URL提供外部纹理，空值不访问外部资源
// Nodes按名称替换PDF视图中的节点状态，可指定动作轨道与采样时间，不改变原始注解
// Time以秒指定模型动画的采样时刻，零值为初始时刻
type ThreeDRenderOptions struct {
	Width, Height  int
	MaxMemoryBytes int64
	MaxInstances   int
	ResolveTexture basethreed.U3DTextureResolver
	Nodes          []basethreed.U3DNodeState
	Time           float64
}

// RenderThreeD 按PDF相机、投影、背景与灯光绘制指定时刻的3DB区域，不执行脚本
// 返回图像不含3DB以外的注解背景，未支持的视图效果返回明确错误
// 每次从传入场景建立独立节点状态，不继承此前绘制的运行时状态
// 入参: ctx 取消上下文, model 解码后的U3D模型, source PDF三维参数, options 输出与资源限制
// 返回: *image.RGBA 不透明图像, error 参数、能力、预算或取消错误
func RenderThreeD(ctx context.Context, model *basethreed.U3DModel, source pdfgo.ThreeD, options ThreeDRenderOptions) (*image.RGBA, error) {
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
	render := basethreed.U3DRenderOptions{Width: options.Width, Height: options.Height, MaxMemoryBytes: options.MaxMemoryBytes, MaxInstances: options.MaxInstances, Background: source.Background.RGB, ResolveTexture: options.ResolveTexture, Time: options.Time}
	if err := threeDRenderMode(source, &render); err != nil {
		return nil, err
	}
	if err := threeDNodeStates(ctx, presentation.Nodes, &render); err != nil {
		return nil, err
	}
	if err := threeDSections(ctx, presentation.CrossSections, &render); err != nil {
		return nil, err
	}
	r, err := basethreed.NewRenderer(ctx, model, render, options.Nodes)
	if err != nil {
		return nil, err
	}
	camera, view, err := threeDCamera(ctx, model, r, source.Camera)
	if err != nil {
		return nil, err
	}
	render.View = view
	camera.ScaleX, camera.ScaleY, camera.Near = 1, 1, 1
	render.Camera = &camera
	passes, err := r.SelectView(render)
	if err != nil {
		return nil, err
	}
	if err := r.PrepareSections(render.Sections); err != nil {
		return nil, err
	}
	scale, err := projection.ScaleFactor(source.ViewBox, source.AnnotationBox)
	if err != nil {
		return nil, err
	}
	if !finite(scale) || scale <= 0 {
		return nil, fmt.Errorf("invalid PDF 3D projection scale")
	}
	camera.Perspective = projection.Subtype == "P"
	camera.ScaleX = scale * float64(options.Width) / width
	camera.ScaleY = scale * float64(options.Height) / height
	if !finite(camera.ScaleX) || !finite(camera.ScaleY) || camera.ScaleX <= 0 || camera.ScaleY <= 0 {
		return nil, fmt.Errorf("invalid PDF 3D projection scale")
	}
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
		minimum, maximum, found, err := r.DepthRange()
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
	if _, err := r.SelectView(render); err != nil {
		return nil, err
	}
	if source.Lighting.Subtype != "Artwork" && source.Lighting.Subtype != "" {
		if err := r.Reserve(uint64(len(source.Lighting.Lights)), 56); err != nil {
			return nil, err
		}
		render.Lighting = &basethreed.U3DLighting{DiffuseOffset: source.Lighting.Ambient, Lights: make([]basethreed.U3DDirectionalLight, len(source.Lighting.Lights))}
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
			render.Lighting.Lights[i] = basethreed.U3DDirectionalLight{Color: light.Color, Direction: direction, CameraRelative: light.CameraAttached}
		}
	}
	return r.Render(render, passes)
}

// threeDCamera 解析PDF相机来源并保留模型视图的绘制轮次
// 入参: ctx 取消上下文, model 三维模型, renderer 场景实例, source 相机来源
// 返回: basethreed.U3DCamera 相机变换, string 视图节点, error 路径或变换错误
func threeDCamera(ctx context.Context, model *basethreed.U3DModel, renderer *basethreed.Renderer, source pdfgo.ThreeDCamera) (basethreed.U3DCamera, string, error) {
	var camera basethreed.U3DCamera
	selected := -1
	for i, node := range model.Nodes {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
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
			for i, node := range model.Nodes {
				if i%256 == 0 {
					if err := ctx.Err(); err != nil {
						return camera, "", err
					}
				}
				if node.Name == source.Path[len(source.Path)-1] {
					selected = i
					break
				}
			}
		}
		if selected < 0 || model.Nodes[selected].Kind != "View" {
			return camera, "", fmt.Errorf("missing PDF 3D camera node")
		}
		var err error
		camera.ToWorld, err = renderer.ViewCamera(selected, source.Path, source.Source == "U3D" && source.PathFromRoot)
		if errors.Is(err, basethreed.ErrAmbiguousCamera) {
			return camera, "", &pdfgo.UnsupportedError{Feature: "ambiguous 3D camera instance"}
		}
		if errors.Is(err, basethreed.ErrCameraNotFound) {
			return camera, "", fmt.Errorf("missing PDF 3D camera path")
		}
		if err != nil {
			return camera, "", err
		}
	}
	view := ""
	if selected >= 0 {
		view = model.Nodes[selected].Name
	}
	return camera, view, nil
}

// threeDNodeStates 将PDF节点参数转为场景覆盖，转换缓存计入绘制预算
// 入参: ctx 取消上下文, nodes 节点参数, options 绘制选项
// 返回: error 预算或取消错误
func threeDNodeStates(ctx context.Context, nodes []pdfgo.ThreeDNode, options *basethreed.U3DRenderOptions) error {
	if len(nodes) == 0 {
		return nil
	}
	limit := options.MaxMemoryBytes
	if limit == 0 {
		limit = basethreed.DefaultBytes
	}
	if limit <= 0 || uint64(len(nodes)) >= uint64(limit)/168 {
		return fmt.Errorf("PDF 3D node memory exceeds limit")
	}
	options.MaxMemoryBytes = limit - int64(len(nodes))*168
	options.Nodes = make([]basethreed.U3DNodeState, len(nodes))
	for i, node := range nodes {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		state := basethreed.U3DNodeState{Name: node.Name, Opacity: node.Opacity, Visible: node.Visible}
		if node.Matrix != nil {
			m := node.Matrix
			state.Matrix = &[16]float64{m[0], m[1], m[2], 0, m[3], m[4], m[5], 0, m[6], m[7], m[8], 0, m[9], m[10], m[11], 1}
		}
		options.Nodes[i] = state
	}
	return nil
}
