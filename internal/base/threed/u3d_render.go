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

package threed

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
// 纹理合成后先应用节点透明度，再应用Opacity
// Mode控制绘制方式，AuxiliaryColor用于固定颜色的边点，FaceColor用于包围盒面及插图面，空值分别使用黑色和背景色
// CreaseAngle为轮廓夹角，单位为度，空值使用45；着色插图以漫反射色的1/4补充自发光
// Nodes按名称覆盖节点，重复名称使用最后一项，每次绘制均从原场景开始
// Time以秒指定动画采样时刻，零值为初始时刻，不随实际时钟推进
// ResolveTexture提供外部纹理，空值不访问外部资源；回调自身的内存与超时由调用方控制
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
	Time           float64
	Sections       []U3DCrossSection
	ResolveTexture U3DTextureResolver
	MaxMemoryBytes int64
	MaxInstances   int
}

// Renderer 保存一次绘制的有限预算，不修改调用方场景
type Renderer struct {
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
	connected                              []bool
	members                                []bool
	meshes, shaders, materials, lightIndex map[string]int
	textureIndex                           map[string]int
	viewIndex, motionIndex                 map[string]int
	textures                               map[string]*image.NRGBA64
	textureSets                            map[[8]*U3DTextureLayer]*u3dTextureSet
	resolveTexture                         U3DTextureResolver
	view                                   *U3DView
	lights                                 []u3dRenderLight
	lighting                               *U3DLighting
	opacity                                float64
	mode                                   U3DRenderMode
	creaseAngle                            float64
	auxiliaryColor, faceColor              [3]float64
	depthOnly                              bool
	primitives                             map[u3dPrimitiveKey]struct{}
	texturedPrimitives                     map[u3dTexturedPrimitiveKey]struct{}
	states                                 map[string]U3DNodeState
	posed                                  map[string]*U3DMesh
	animated                               map[string]u3dAnimationState
	sections                               []u3dSection
	sectionScratch                         [2][]u3dVertex
	chunks                                 [][]u3dFragment
	fragments                              int
	scratch                                []int
	order                                  uint64
}

// NewRenderer 建立有预算限制的场景实例与资源索引
// 入参: ctx 取消上下文, model 场景, options 输出与资源限制, states 后续节点覆盖分组
// 返回: *Renderer 绘制状态, error 参数、场景、预算或取消错误
func NewRenderer(ctx context.Context, model *U3DModel, options U3DRenderOptions, states ...[]U3DNodeState) (*Renderer, error) {
	if ctx == nil || model == nil || options.Width <= 0 || options.Height <= 0 || options.MaxMemoryBytes < 0 || options.MaxInstances < 0 || options.Instance < 0 || !finite(options.Time) {
		return nil, fmt.Errorf("invalid U3D render options")
	}
	limit := options.MaxMemoryBytes
	if limit == 0 {
		limit = DefaultBytes
	}
	r := &Renderer{ctx: ctx, model: model, remaining: uint64(limit), opacity: -1, mode: options.Mode, creaseAngle: 45, resolveTexture: options.ResolveTexture}
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
	if err := r.Reserve(pixels, 60); err != nil {
		return nil, err
	}
	if err := r.resources(); err != nil {
		return nil, err
	}
	order, index, err := r.sceneGraph()
	if err != nil {
		return nil, err
	}
	if err := r.checkAnimations(); err != nil {
		return nil, err
	}
	maxInstances := options.MaxInstances
	if maxInstances == 0 {
		maxInstances = 65536
	}
	groups := append([][]U3DNodeState{options.Nodes}, states...)
	if err := r.nodeStates(groups...); err != nil {
		return nil, err
	}
	if err := r.prepareAnimations(options.Time); err != nil {
		return nil, err
	}
	if err := r.instances(order, index, maxInstances); err != nil {
		return nil, err
	}
	if err := r.poseNodes(); err != nil {
		return nil, err
	}
	if err := r.Reserve(uint64(len(r.scene)), 1); err != nil {
		return nil, err
	}
	r.members = make([]bool, len(r.scene))
	return r, nil
}

// Render 在已选择的相机和轮次下生成图像
// 入参: options 输出与灯光背景, passes 绘制轮次
// 返回: *image.RGBA 图像, error 参数、绘制、预算或取消错误
func (r *Renderer) Render(options U3DRenderOptions, passes []U3DViewPass) (*image.RGBA, error) {
	if r.sections == nil {
		if err := r.PrepareSections(options.Sections); err != nil {
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
	if r.view != nil {
		if err := r.viewLayers(r.view.Backdrops); err != nil {
			return nil, err
		}
	}
	for passIndex, pass := range passes {
		if err := r.renderPass(pass, passIndex, passIndex == len(passes)-1); err != nil {
			return nil, err
		}
	}
	if r.view != nil {
		if err := r.viewLayers(r.view.Overlays); err != nil {
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

// Reserve 在分配绘制缓存前扣减预算并检查取消状态
// 入参: count 元素数量, size 元素预算
// 返回: error 预算或取消错误
func (r *Renderer) Reserve(count, size uint64) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if size == 0 || count > r.remaining/size || count > uint64(^uint(0)>>1)/size {
		return fmt.Errorf("U3D render memory exceeds limit")
	}
	r.remaining -= count * size
	return nil
}

// SelectView 解析视图与相机覆盖，内部视口使用左上角坐标
// 入参: options 绘制选项
// 返回: []U3DViewPass 绘制轮次, error 视图或投影错误
func (r *Renderer) SelectView(options U3DRenderOptions) ([]U3DViewPass, error) {
	r.view = nil
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
		if view.Attributes & ^uint32(7) != 0 {
			return nil, fmt.Errorf("invalid U3D view attributes")
		}
		r.view = view
		if i, exists := r.viewIndex[node.Resource]; exists {
			passes = r.model.Views[i].Passes
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

// RenderU3D 绘制几何及视图纹理层，返回指定时刻的不透明图像，不执行脚本
// 可通过节点覆盖指定动作轨道与采样时间，逐次生成独立姿态，不修改原模型
// 显式骨骼姿态按权重变换位置和法线，暂不生成骨骼链接或推导缺失权重
// 动画修饰器支持单动作的节点和骨骼求值，尚未实现的动作衔接返回明确错误
// 几何纹理支持模型坐标与程序化映射，参考形状约定见U3DTextureLayer
// 纹理按需解码并复用，不自动访问外部图像
// 采用逐顶点光照及逐像素深度混合，空名称引用标准默认资源
// 缺失几何、材质和着色器采用默认资源，缺失的非空光源引用不产生光照
// 缺失纹理返回错误，空纹理名称使用标准默认图像
// 仅绘制默认根的后代，非空父节点引用缺失时不展开该父路径
// 入参: ctx 取消上下文, model 场景, options 视图、输出与资源限制
// 返回: *image.RGBA 图像, error 参数、能力、预算或取消错误
func RenderU3D(ctx context.Context, model *U3DModel, options U3DRenderOptions) (*image.RGBA, error) {
	r, err := NewRenderer(ctx, model, options)
	if err != nil {
		return nil, err
	}
	passes, err := r.SelectView(options)
	if err != nil {
		return nil, err
	}
	return r.Render(options, passes)
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

// resources 建立只读资源索引，空名称保留给标准默认资源
// 返回: error 名称、预算或取消错误
func (r *Renderer) resources() error {
	count := uint64(len(r.model.Meshes)) + uint64(len(r.model.Shaders)) + uint64(len(r.model.Materials)) + uint64(len(r.model.Lights)) + uint64(len(r.model.Textures)) + uint64(len(r.model.Views)) + uint64(len(r.model.Motions))
	if err := r.Reserve(count, 96); err != nil {
		return err
	}
	r.meshes = make(map[string]int, len(r.model.Meshes))
	r.shaders = make(map[string]int, len(r.model.Shaders))
	r.materials = make(map[string]int, len(r.model.Materials))
	r.lightIndex = make(map[string]int, len(r.model.Lights))
	r.textureIndex = make(map[string]int, len(r.model.Textures))
	r.viewIndex = make(map[string]int, len(r.model.Views))
	r.motionIndex = make(map[string]int, len(r.model.Motions))
	add := func(index map[string]int, name string, i int) error {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if name == "" {
			return fmt.Errorf("cannot redefine U3D default resource")
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
	for i, v := range r.model.Textures {
		if err := add(r.textureIndex, v.Name, i); err != nil {
			return err
		}
	}
	for i, v := range r.model.Views {
		if err := add(r.viewIndex, v.Name, i); err != nil {
			return err
		}
	}
	for i, v := range r.model.Motions {
		if err := add(r.motionIndex, v.Name, i); err != nil {
			return err
		}
	}
	return nil
}

// renderPass 分别解析遮挡与混合，绘制轮次之间保留颜色并重置深度
// 入参: pass 轮次参数, passIndex 轮次索引, last 是否为最后一轮
// 返回: error 场景、预算或取消错误
func (r *Renderer) renderPass(pass U3DViewPass, passIndex int, last bool) error {
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
func (r *Renderer) drawScene(pass U3DViewPass, passIndex int) error {
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
		mesh := r.nodeMesh(node)
		if mesh == nil {
			continue
		}
		if err := r.drawMesh(instance, node, mesh, pass, passIndex); err != nil {
			return err
		}
	}
	return nil
}
