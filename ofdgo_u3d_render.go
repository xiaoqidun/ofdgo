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
	"fmt"
	"image"
	"math"
)

// U3DCamera 定义沿正Z方向观察的相机，ScaleX和ScaleY为投影到像素的倍率
// ToWorld按列存储，全零表示单位矩阵；Far为0且ClipFar为假时不设置远裁剪面
// ShiftX和ShiftY为投影原点相对视口中心的像素偏移，向右及向下为正
type U3DCamera struct {
	ToWorld                   [16]float64
	Perspective               bool
	ScaleX, ScaleY, Near, Far float64
	ShiftX, ShiftY            float64
	ClipFar                   bool
}

// U3DRenderOptions 控制静态绘制，指定Camera时使用整个输出视口
// Camera为空时使用View指定的节点或首个视图节点，Instance指定其父路径实例
// Lighting为空时沿用模型灯光，非空时替换全部模型灯光
// Opacity为空时沿用着色器混合，非空时叠加面片、着色边点及包围盒轮廓的透明度
// Mode控制绘制方式，AuxiliaryColor用于固定颜色的边点，FaceColor用于包围盒面及插图面，空值分别使用黑色和背景色
// CreaseAngle为轮廓夹角，单位为度，空值使用45；着色插图以漫反射色的1/4补充自发光
// Nodes按名称覆盖节点，重复名称使用最后一项，每次绘制均从原场景开始
// Background为空时使用白色；缓存与实例上限为0时分别使用256MiB和65536，不含输入及运行时开销
type U3DRenderOptions struct {
	Width, Height  int
	View           string
	Instance       int
	Camera         *U3DCamera
	Background     *[3]float64
	Lighting       *U3DLighting
	Opacity        *float64
	Mode           U3DRenderMode
	AuxiliaryColor *[3]float64
	FaceColor      *[3]float64
	CreaseAngle    *float64
	Nodes          []U3DNodeState
	Sections       []U3DCrossSection
	MaxMemoryBytes int64
	MaxInstances   int
}

// u3dPixel 保存不透明底色、最近深度及透明片元链
type u3dPixel struct {
	color [3]float64
	depth float64
	order uint64
	head  int32
}

// u3dFragment 保存等待按像素深度混合的透明或非覆盖片元
type u3dFragment struct {
	color [4]float64
	depth float64
	order uint64
	blend uint32
	next  int32
}

// u3dRender 保存一次绘制的有限预算，不修改调用方场景
type u3dRender struct {
	ctx                                    context.Context
	model                                  *U3DModel
	remaining                              uint64
	frame                                  []u3dPixel
	output                                 *image.RGBA
	viewport                               [4]float64
	camera                                 U3DCamera
	toCamera                               u3dMatrix
	scene                                  []u3dInstance
	byNode                                 [][]int
	members                                []bool
	meshes, shaders, materials, lightIndex map[string]int
	lights                                 []u3dRenderLight
	lighting                               *U3DLighting
	opacity                                float64
	mode                                   U3DRenderMode
	creaseAngle                            float64
	auxiliaryColor, faceColor              [3]float64
	depthOnly                              bool
	primitives                             map[u3dPrimitiveKey]struct{}
	states                                 map[string]U3DNodeState
	sections                               []u3dSection
	sectionScratch                         [2][]u3dVertex
	chunks                                 [][]u3dFragment
	fragments                              int
	scratch                                []int
	order                                  uint64
}

// RenderU3D 绘制静态无纹理网格、线集及其顶点或包围盒，返回不透明图像，不执行脚本或动画
// 采用逐顶点光照及逐像素深度混合，缺失资源回退为空网格、灰色材质或无光源
// 入参: ctx 取消上下文, model 场景, options 视图、输出与资源限制
// 返回: *image.RGBA 图像, error 参数、能力、预算或取消错误
func RenderU3D(ctx context.Context, model *U3DModel, options U3DRenderOptions) (*image.RGBA, error) {
	r, err := newU3DRender(ctx, model, options)
	if err != nil {
		return nil, err
	}
	passes, err := r.selectView(options)
	if err != nil {
		return nil, err
	}
	return r.render(options, passes)
}

// newU3DRender 建立有预算限制的场景实例与资源索引
// 入参: ctx 取消上下文, model 场景, options 输出与资源限制
// 返回: *u3dRender 绘制状态, error 参数、场景、预算或取消错误
func newU3DRender(ctx context.Context, model *U3DModel, options U3DRenderOptions) (*u3dRender, error) {
	if ctx == nil || model == nil || options.Width <= 0 || options.Height <= 0 || options.MaxMemoryBytes < 0 || options.MaxInstances < 0 || options.Instance < 0 {
		return nil, fmt.Errorf("invalid U3D render options")
	}
	limit := options.MaxMemoryBytes
	if limit == 0 {
		limit = u3dDefaultBytes
	}
	r := &u3dRender{ctx: ctx, model: model, remaining: uint64(limit), opacity: -1, mode: options.Mode, creaseAngle: 45}
	if options.Mode > U3DRenderShadedIllustration {
		return nil, fmt.Errorf("unsupported U3D render mode")
	}
	if options.CreaseAngle != nil {
		if !finite(*options.CreaseAngle) {
			return nil, fmt.Errorf("invalid U3D crease angle")
		}
		r.creaseAngle = *options.CreaseAngle
	}
	if options.Opacity != nil {
		value := *options.Opacity
		if !finite(value) || value < 0 || value > 1 {
			return nil, fmt.Errorf("invalid U3D render opacity")
		}
		r.opacity = value
	}
	if uint64(options.Width) > uint64(^uint(0)>>1)/uint64(options.Height) {
		return nil, fmt.Errorf("invalid U3D render dimensions")
	}
	pixels := uint64(options.Width) * uint64(options.Height)
	if err := r.reserve(pixels, 60); err != nil {
		return nil, err
	}
	maxInstances := options.MaxInstances
	if maxInstances == 0 {
		maxInstances = 65536
	}
	if err := r.nodeStates(options.Nodes); err != nil {
		return nil, err
	}
	if err := r.instances(maxInstances); err != nil {
		return nil, err
	}
	if err := r.reserve(uint64(len(r.scene)), 1); err != nil {
		return nil, err
	}
	r.members = make([]bool, len(r.scene))
	if err := r.resources(); err != nil {
		return nil, err
	}
	return r, nil
}

// render 在已选择的相机和轮次下生成图像
// 入参: options 输出与灯光背景, passes 绘制轮次
// 返回: *image.RGBA 图像, error 参数、绘制、预算或取消错误
func (r *u3dRender) render(options U3DRenderOptions, passes []U3DViewPass) (*image.RGBA, error) {
	if r.sections == nil {
		if err := r.prepareSections(options.Sections); err != nil {
			return nil, err
		}
	}
	if err := r.overrideLighting(options.Lighting); err != nil {
		return nil, err
	}
	background := [3]float64{1, 1, 1}
	if options.Background != nil {
		background = *options.Background
	}
	for i, v := range background {
		if !finite(v) {
			return nil, fmt.Errorf("invalid U3D background")
		}
		background[i] = min(1, max(0, v))
	}
	r.faceColor = background
	for _, item := range []struct {
		source *[3]float64
		target *[3]float64
	}{{options.AuxiliaryColor, &r.auxiliaryColor}, {options.FaceColor, &r.faceColor}} {
		if item.source != nil {
			for i, value := range *item.source {
				if !finite(value) || value < 0 || value > 1 {
					return nil, fmt.Errorf("invalid U3D mode color")
				}
				item.target[i] = value
			}
		}
	}
	r.frame = make([]u3dPixel, options.Width*options.Height)
	r.output = image.NewRGBA(image.Rect(0, 0, options.Width, options.Height))
	for i := range r.frame {
		if i%4096 == 0 {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
		}
		r.frame[i].color = background
	}
	for passIndex, pass := range passes {
		if err := r.renderPass(pass, passIndex, passIndex == len(passes)-1); err != nil {
			return nil, err
		}
	}
	for i, pixel := range r.frame {
		if i%4096 == 0 {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
		}
		for component, v := range pixel.color {
			r.output.Pix[i*4+component] = byte(math.Round(min(1, max(0, v)) * 255))
		}
		r.output.Pix[i*4+3] = 255
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	return r.output, nil
}

// reserve 在分配绘制缓存前扣减预算并检查取消状态
// 入参: count 元素数量, size 元素预算
// 返回: error 预算或取消错误
func (r *u3dRender) reserve(count, size uint64) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if size == 0 || count > r.remaining/size || count > uint64(^uint(0)>>1)/size {
		return fmt.Errorf("U3D render memory exceeds limit")
	}
	r.remaining -= count * size
	return nil
}

// selectView 解析视图与相机覆盖，内部视口使用左上角坐标
// 入参: options 绘制选项
// 返回: []U3DViewPass 绘制轮次, error 视图或投影错误
func (r *u3dRender) selectView(options U3DRenderOptions) ([]U3DViewPass, error) {
	r.viewport = [4]float64{0, 0, float64(options.Width), float64(options.Height)}
	selected := -1
	for i, node := range r.model.Nodes {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
		}
		if options.View != "" && node.Name == options.View || options.View == "" && options.Camera == nil && node.Kind == "View" {
			selected = i
			break
		}
	}
	if options.View != "" && selected < 0 {
		return nil, fmt.Errorf("missing U3D view %q", options.View)
	}
	passes := []U3DViewPass{{}}
	if selected >= 0 {
		node := r.model.Nodes[selected]
		if node.Kind != "View" || node.View == nil {
			return nil, fmt.Errorf("invalid U3D view node")
		}
		view := node.View
		if len(view.Backdrops) != 0 || len(view.Overlays) != 0 {
			return nil, fmt.Errorf("unsupported U3D textured view layers")
		}
		found := false
		for i, resource := range r.model.Views {
			if i%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return nil, err
				}
			}
			if resource.Name == node.Resource {
				if found {
					return nil, fmt.Errorf("duplicate U3D view resource")
				}
				passes = resource.Passes
				found = true
			}
		}
		if options.Camera == nil {
			if options.Instance >= len(r.byNode[selected]) {
				return nil, fmt.Errorf("missing U3D camera instance")
			}
			r.camera.ToWorld = r.scene[r.byNode[selected][options.Instance]].world
			if view.Attributes & ^uint32(7) != 0 || view.Attributes&6 >= 4 {
				return nil, fmt.Errorf("unsupported U3D view projection")
			}
			for i, v := range view.Viewport {
				if !finite(float64(v)) {
					return nil, fmt.Errorf("invalid U3D viewport")
				}
				r.viewport[(i+2)%4] = float64(v)
			}
			if view.Attributes&1 != 0 {
				r.viewport[0] *= float64(options.Width)
				r.viewport[1] *= float64(options.Height)
				r.viewport[2] *= float64(options.Width)
				r.viewport[3] *= float64(options.Height)
			}
			if r.viewport[2] <= 0 || r.viewport[3] <= 0 {
				return nil, fmt.Errorf("invalid U3D viewport size")
			}
			r.camera.Near, r.camera.Far = float64(view.Near), float64(view.Far)
			if r.camera.Far <= r.camera.Near {
				return nil, fmt.Errorf("invalid U3D view clipping range")
			}
			r.camera.Perspective = view.Attributes&6 == 0
			if r.camera.Perspective {
				if view.Projection <= 0 || view.Projection >= 180 {
					return nil, fmt.Errorf("invalid U3D perspective field of view")
				}
				r.camera.ScaleY = r.viewport[3] / (2 * math.Tan(float64(view.Projection)*math.Pi/360))
			} else {
				if view.Projection <= 0 {
					return nil, fmt.Errorf("invalid U3D orthographic height")
				}
				r.camera.ScaleY = r.viewport[3] / float64(view.Projection)
			}
			r.camera.ScaleX = r.camera.ScaleY
		}
	}
	if options.Camera != nil {
		r.camera = *options.Camera
	} else if selected < 0 {
		return nil, fmt.Errorf("missing U3D camera")
	}
	if len(passes) > 32 {
		return nil, fmt.Errorf("unsupported U3D render pass count")
	}
	for _, v := range []float64{r.camera.ScaleX, r.camera.ScaleY, r.camera.Near, r.camera.Far, r.camera.ShiftX, r.camera.ShiftY} {
		if !finite(v) {
			return nil, fmt.Errorf("invalid U3D camera values")
		}
	}
	if r.camera.ScaleX <= 0 || r.camera.ScaleY <= 0 || r.camera.Perspective && r.camera.Near <= 0 || (r.camera.ClipFar || r.camera.Far != 0) && r.camera.Far < r.camera.Near {
		return nil, fmt.Errorf("invalid U3D camera projection")
	}
	matrix := u3dMatrix(r.camera.ToWorld)
	if matrix == (u3dMatrix{}) {
		matrix = u3dIdentity()
	}
	var ok bool
	r.toCamera, ok = matrix.inverse()
	if !ok {
		return nil, fmt.Errorf("invalid U3D camera transform")
	}
	return passes, nil
}

// resources 建立只读资源索引，不按名称反复扫描场景
// 返回: error 重名或预算错误
func (r *u3dRender) resources() error {
	count := uint64(len(r.model.Meshes)) + uint64(len(r.model.Shaders)) + uint64(len(r.model.Materials)) + uint64(len(r.model.Lights))
	if err := r.reserve(count, 96); err != nil {
		return err
	}
	r.meshes = make(map[string]int, len(r.model.Meshes))
	r.shaders = make(map[string]int, len(r.model.Shaders))
	r.materials = make(map[string]int, len(r.model.Materials))
	r.lightIndex = make(map[string]int, len(r.model.Lights))
	add := func(index map[string]int, name string, i int) error {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if _, ok := index[name]; ok {
			return fmt.Errorf("duplicate U3D render resource %q", name)
		}
		index[name] = i
		return nil
	}
	for i, v := range r.model.Meshes {
		if err := add(r.meshes, v.Name, i); err != nil {
			return err
		}
	}
	for i, v := range r.model.Shaders {
		if err := add(r.shaders, v.Name, i); err != nil {
			return err
		}
	}
	for i, v := range r.model.Materials {
		if err := add(r.materials, v.Name, i); err != nil {
			return err
		}
	}
	for i, v := range r.model.Lights {
		if err := add(r.lightIndex, v.Name, i); err != nil {
			return err
		}
	}
	return nil
}

// renderPass 分别解析遮挡与混合，绘制轮次之间保留颜色并重置深度
// 入参: pass 轮次参数, passIndex 轮次索引, last 是否为最后一轮
// 返回: error 场景、预算或取消错误
func (r *u3dRender) renderPass(pass U3DViewPass, passIndex int, last bool) error {
	if pass.Attributes > 1 || pass.FogMode > 2 {
		return fmt.Errorf("invalid U3D render pass")
	}
	if pass.Attributes&1 != 0 {
		for _, v := range append(pass.FogColor[:], pass.FogNear, pass.FogFar) {
			if !finite(float64(v)) {
				return fmt.Errorf("invalid U3D fog value")
			}
		}
		if pass.FogMode == 0 && pass.FogFar <= pass.FogNear || pass.FogMode != 0 && pass.FogFar <= 0 {
			return fmt.Errorf("invalid U3D fog range")
		}
	}
	for i, instance := range r.scene {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		r.members[i] = pass.Root == "" || r.model.Nodes[instance.node].Name == pass.Root || instance.parent >= 0 && r.members[instance.parent]
	}
	for i := range r.frame {
		if i%4096 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		r.frame[i].depth = math.Inf(1)
		r.frame[i].order = 0
		r.frame[i].head = -1
	}
	r.fragments = 0
	if r.mode.shaded() {
		if err := r.collectLights(); err != nil {
			return err
		}
	}
	if r.mode == U3DRenderHiddenWireframe {
		r.depthOnly = true
		if err := r.drawScene(pass, passIndex); err != nil {
			return err
		}
		r.depthOnly = false
	}
	if err := r.drawScene(pass, passIndex); err != nil {
		return err
	}
	if last {
		if err := r.drawSectionPlanes(); err != nil {
			return err
		}
	}
	return r.resolveFragments()
}

// drawScene 绘制当前轮次中的可见模型，不重置深度及片元缓存
// 入参: pass 绘制轮次, passIndex 轮次索引
// 返回: error 场景、资源或取消错误
func (r *u3dRender) drawScene(pass U3DViewPass, passIndex int) error {
	for i, instance := range r.scene {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		node := &r.model.Nodes[instance.node]
		if !r.members[i] || node.Kind != "Model" || instance.modelVisibility(node) == 0 || instance.opacity == 0 {
			continue
		}
		if node.Visibility > 3 {
			return fmt.Errorf("invalid U3D model visibility")
		}
		mesh, ok := r.meshes[node.Resource]
		if !ok {
			continue
		}
		if err := r.drawMesh(instance, node, &r.model.Meshes[mesh], pass, passIndex); err != nil {
			return err
		}
	}
	return nil
}
