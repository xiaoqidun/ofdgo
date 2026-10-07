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
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// u3dDefaultBytes 限制默认解码数据规模，不包括调用方已有输入及运行时开销
const u3dDefaultBytes = 256 << 20

// U3DOptions 控制U3D解码规模，MaxDecodedBytes为0时采用256MiB上限
type U3DOptions struct {
	MaxDecodedBytes int64
}

// U3DModel 保存独立于PDF的静态U3D场景，不执行动作或读取外部文件
type U3DModel struct {
	Major, Minor int16
	DefinedUnits bool
	Units        float64
	Meshes       []U3DMesh
	Nodes        []U3DNode
	Materials    []U3DMaterial
	Shaders      []U3DShader
	Lights       []U3DLight
	Views        []U3DViewResource
	Metadata     []U3DMetadata
}

// U3DMetadata 保留块所属名称与原始元数据，数据不与输入共享存储
type U3DMetadata struct {
	Block uint32
	Name  string
	Data  []byte
}

// U3DNode 保存组、模型、光源或视图节点，父节点矩阵按列存储
type U3DNode struct {
	Name, Kind, Resource string
	Parents              []U3DParent
	Visibility           uint32
	Shaders              [][]string
	LineShaders          [][]string
	View                 *U3DView
}

// U3DParent 保存父节点名称及局部到父节点的变换，空名称引用默认节点
type U3DParent struct {
	Name      string
	Transform [16]float32
}

// U3DMaterial 保存材质参数，Attributes各位决定相应参数是否生效
type U3DMaterial struct {
	Name                                 string
	Attributes                           uint32
	Ambient, Diffuse, Specular, Emissive [3]float32
	Reflectivity, Opacity                float32
}

// U3DShader 保存无纹理着色器参数，不提前应用混合、透明测试或光照
type U3DShader struct {
	Name, Material                   string
	Attributes, AlphaFunction, Blend uint32
	RenderPass                       uint32
	AlphaReference                   float32
}

// U3DLight 保存环境、方向、点或聚光资源，Kind依次为0至3
type U3DLight struct {
	Name                 string
	Attributes           uint32
	Kind                 byte
	Color, Attenuation   [3]float32
	SpotAngle, Intensity float32
}

// u3dBlock 保存校验后的块边界，数据切片仅在解码期间引用输入
type u3dBlock struct {
	kind       uint32
	data, meta []byte
}

// u3dDecoder 保存单次解码预算及场景索引，不跨调用缓存输入
type u3dDecoder struct {
	ctx       context.Context
	model     U3DModel
	remaining uint64
	plain     bool
	meshes    map[string]*u3dMeshDeclaration
	meshOrder []string
	nodes     map[string]int
	palettes  map[uint32]map[string]bool
}

// DecodeU3D 按ECMA-363解码静态基础网格、线集、组、材质、着色器和光源
// 暂不支持渐进网格、纹理和动画，遇到未支持内容返回错误而非部分场景
// 入参: ctx 取消上下文, data U3D数据, options 解码限制
// 返回: *U3DModel 独立场景数据, error 格式、能力、预算或取消错误
func DecodeU3D(ctx context.Context, data []byte, options U3DOptions) (*U3DModel, error) {
	if ctx == nil || options.MaxDecodedBytes < 0 {
		return nil, fmt.Errorf("invalid U3D decode options")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := options.MaxDecodedBytes
	if limit == 0 {
		limit = u3dDefaultBytes
	}
	d := u3dDecoder{ctx: ctx, remaining: uint64(limit), meshes: make(map[string]*u3dMeshDeclaration), nodes: make(map[string]int), palettes: make(map[uint32]map[string]bool)}
	header, next, err := u3dReadBlock(data, 0)
	if err != nil {
		return nil, err
	}
	if header.kind != 0x00443355 {
		return nil, fmt.Errorf("invalid U3D file header")
	}
	r := u3dValues{data: header.data}
	version, profile := r.u32(), r.u32()
	d.model.Major, d.model.Minor = int16(version), int16(version>>16)
	if d.model.Major > 0 || profile & ^uint32(14) != 0 {
		return nil, fmt.Errorf("unsupported U3D version or profile")
	}
	declarationEnd := uint64(r.u32())
	fileSize := uint64(r.u32())
	fileSize |= uint64(r.u32()) << 32
	if r.u32() != 106 {
		return nil, fmt.Errorf("unsupported U3D character encoding")
	}
	d.plain = profile&4 != 0
	d.model.DefinedUnits, d.model.Units = profile&8 != 0, 1
	if d.model.DefinedUnits {
		unitBytes := r.take(8)
		if r.err == nil {
			d.model.Units = math.Float64frombits(binary.LittleEndian.Uint64(unitBytes))
			if !finite(d.model.Units) || d.model.Units <= 0 {
				return nil, fmt.Errorf("invalid U3D units")
			}
		}
	}
	if err := r.done(); err != nil {
		return nil, err
	}
	if fileSize != uint64(len(data)) || declarationEnd < uint64(next) || declarationEnd > fileSize || declarationEnd%4 != 0 {
		return nil, fmt.Errorf("invalid U3D file size")
	}
	if err := d.metadata(header, ""); err != nil {
		return nil, err
	}
	priority := uint32(0)
	for offset := next; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		block, end, err := u3dReadBlock(data, offset)
		if err != nil {
			return nil, fmt.Errorf("U3D block at %d: %w", offset, err)
		}
		continuation := block.kind == 0xffffff15 || block.kind == 0xffffff3b || block.kind == 0xffffff3c || block.kind == 0xffffff3f
		if uint64(offset) < declarationEnd && (uint64(end) > declarationEnd || continuation) || uint64(offset) >= declarationEnd && !continuation {
			return nil, fmt.Errorf("invalid U3D declaration boundary at %d", offset)
		}
		if block.kind == 0xffffff15 {
			r := u3dValues{data: block.data}
			value := r.u32()
			if err := r.done(); err != nil {
				return nil, err
			}
			if value == 0 || value < priority || value > 0x7fffffff {
				return nil, fmt.Errorf("invalid U3D block priority")
			}
			priority = value
			err = d.metadata(block, "")
		} else if block.kind == 0xffffff14 {
			err = d.chain(block)
		} else {
			_, err = d.block(block, "", -1, 0)
		}
		if err != nil {
			return nil, fmt.Errorf("U3D block %#x at %d: %w", block.kind, offset, err)
		}
		offset = end
	}
	for _, name := range d.meshOrder {
		mesh := d.meshes[name]
		if !mesh.decoded && mesh.counts[1] != 0 {
			return nil, fmt.Errorf("missing U3D base mesh %q", name)
		}
		d.model.Meshes = append(d.model.Meshes, mesh.mesh)
	}
	if err := d.checkParents(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &d.model, nil
}

// u3dReadBlock 按64位长度计算校验块数据与零填充，避免长度溢出
// 入参: data 块序列, offset 块起点
// 返回: u3dBlock 块, int 下一块起点, error 边界或填充错误
func u3dReadBlock(data []byte, offset int) (u3dBlock, int, error) {
	if offset < 0 || offset > len(data) || len(data)-offset < 12 {
		return u3dBlock{}, 0, io.ErrUnexpectedEOF
	}
	header := data[offset : offset+12]
	size, meta := uint64(binary.LittleEndian.Uint32(header[4:])), uint64(binary.LittleEndian.Uint32(header[8:]))
	start := uint64(offset) + 12
	metaStart := start + ((size + 3) &^ 3)
	end := metaStart + ((meta + 3) &^ 3)
	if end > uint64(len(data)) {
		return u3dBlock{}, 0, io.ErrUnexpectedEOF
	}
	for _, padding := range [][]byte{data[start+size : metaStart], data[metaStart+meta : end]} {
		for _, value := range padding {
			if value != 0 {
				return u3dBlock{}, 0, fmt.Errorf("invalid U3D padding")
			}
		}
	}
	return u3dBlock{binary.LittleEndian.Uint32(header), data[start : start+size], data[metaStart : metaStart+meta]}, int(end), nil
}

// reserve 在分配解码数组前检查总预算及取消状态
// 入参: count 元素数量, size 元素规模上界
// 返回: error 预算或取消错误
func (d *u3dDecoder) reserve(count, size uint64) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if size == 0 || count > d.remaining/size || count > uint64(^uint(0)>>1)/size {
		return fmt.Errorf("U3D decoded data exceeds limit")
	}
	d.remaining -= count * size
	return nil
}

// metadata 校验元数据结构并保留原始数据，不解释或执行其中的动作
// 入参: block 数据块, name 所属资源名
// 返回: error 结构、预算或取消错误
func (d *u3dDecoder) metadata(block u3dBlock, name string) error {
	if len(block.meta) == 0 {
		return nil
	}
	if err := d.reserve(uint64(len(block.meta))+128, 1); err != nil {
		return err
	}
	r := u3dValues{data: block.meta}
	count := r.u32()
	if uint64(count) > uint64(len(block.meta))/8 {
		return fmt.Errorf("invalid U3D metadata count")
	}
	for range count {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		flags := r.u32()
		r.text()
		if flags&1 != 0 {
			size := r.u32()
			if uint64(size) > uint64(len(r.data)-r.pos) {
				return io.ErrUnexpectedEOF
			}
			r.take(int(size))
		} else {
			r.text()
		}
		if r.err != nil {
			return r.err
		}
	}
	if err := r.done(); err != nil {
		return err
	}
	d.model.Metadata = append(d.model.Metadata, U3DMetadata{block.kind, name, bytes.Clone(block.meta)})
	return nil
}

// chain 解析单层修饰链并校验名称、链类型与修饰顺序
// 入参: block 修饰链块
// 返回: error 结构、能力或资源错误
func (d *u3dDecoder) chain(block u3dBlock) error {
	if err := d.reserve(256, 1); err != nil {
		return err
	}
	r := u3dValues{data: block.data}
	name, kind, flags := r.text(), r.u32(), r.u32()
	if kind > 2 || flags & ^uint32(3) != 0 {
		return fmt.Errorf("invalid U3D modifier chain")
	}
	if flags&1 != 0 {
		for range 4 {
			r.f32()
		}
	}
	if flags&2 != 0 {
		for range 6 {
			r.f32()
		}
	}
	for r.pos%4 != 0 && r.err == nil {
		if r.u8() != 0 {
			return fmt.Errorf("invalid U3D chain padding")
		}
	}
	count := r.u32()
	if r.err != nil {
		return r.err
	}
	if uint64(count) > uint64(len(r.data)-r.pos)/12 {
		return fmt.Errorf("invalid U3D modifier count")
	}
	for index := uint32(0); index < count; index++ {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		nested, next, err := u3dReadBlock(r.data, r.pos)
		if err != nil {
			return err
		}
		actual, err := d.block(nested, name, int(kind), index)
		if err != nil {
			return err
		}
		if actual != name {
			return fmt.Errorf("U3D modifier name mismatch")
		}
		r.pos = next
	}
	if err := r.done(); err != nil {
		return err
	}
	return d.metadata(block, name)
}

// block 解析支持的静态场景块，不丢弃未实现的修饰器或几何
// 入参: block 数据块, owner 所属链名, chain 链类别，负值表示独立块, index 修饰位置
// 返回: string 资源名, error 格式、能力或资源错误
func (d *u3dDecoder) block(block u3dBlock, owner string, chain int, index uint32) (string, error) {
	if err := d.ctx.Err(); err != nil {
		return "", err
	}
	if err := d.reserve(uint64(len(block.data))+256, 1); err != nil {
		return "", err
	}
	r := u3dValues{data: block.data}
	name := r.text()
	var err error
	switch block.kind {
	case 0xffffff21, 0xffffff22, 0xffffff23, 0xffffff24:
		if chain >= 0 && (chain != 0 || index != 0) {
			return name, fmt.Errorf("invalid U3D node chain")
		}
		err = d.node(&r, block.kind, name)
	case 0xffffff31:
		if chain >= 0 && (chain != 1 || index != 0) {
			return name, fmt.Errorf("invalid U3D mesh chain")
		}
		err = d.meshDeclaration(&r, name)
	case 0xffffff3b:
		if chain >= 0 {
			return name, fmt.Errorf("invalid U3D mesh continuation location")
		}
		err = d.baseMesh(&r, name)
	case 0xffffff37:
		if chain >= 0 && (chain != 1 || index != 0) {
			return name, fmt.Errorf("invalid U3D line set chain")
		}
		err = d.lineDeclaration(&r, name)
	case 0xffffff3f:
		if chain >= 0 {
			return name, fmt.Errorf("invalid U3D line continuation location")
		}
		err = d.lineContinuation(&r, name)
	case 0xffffff45:
		if chain != 0 || index == 0 || owner != name || r.u32() != index {
			return name, fmt.Errorf("invalid U3D shading modifier chain")
		}
		err = d.shading(&r, name)
	case 0xffffff51, 0xffffff52, 0xffffff53, 0xffffff54:
		if chain >= 0 {
			return name, fmt.Errorf("invalid U3D palette resource location")
		}
		err = d.resource(&r, block.kind, name)
	default:
		return name, fmt.Errorf("unsupported U3D block %#x", block.kind)
	}
	if err != nil {
		return name, err
	}
	if err := r.done(); err != nil {
		return name, err
	}
	return name, d.metadata(block, name)
}

// node 读取节点与父变换，允许引用后续声明的父节点
// 入参: r 字段读取器, kind 节点类型, name 节点名
// 返回: error 结构或预算错误
func (d *u3dDecoder) node(r *u3dValues, kind uint32, name string) error {
	if _, exists := d.nodes[name]; exists {
		return fmt.Errorf("duplicate U3D node %q", name)
	}
	node := U3DNode{Name: name, Kind: "Group"}
	count := r.u32()
	if uint64(count) > uint64(len(r.data)-r.pos)/66 {
		return io.ErrUnexpectedEOF
	}
	if err := d.reserve(uint64(count), 96); err != nil {
		return err
	}
	node.Parents = make([]U3DParent, int(count))
	for index := range node.Parents {
		if index%256 == 0 {
			if err := d.ctx.Err(); err != nil {
				return err
			}
		}
		node.Parents[index].Name = r.text()
		for element := range node.Parents[index].Transform {
			node.Parents[index].Transform[element] = r.f32()
		}
	}
	if kind == 0xffffff22 {
		node.Kind, node.Resource, node.Visibility = "Model", r.text(), r.u32()
		if node.Visibility > 3 {
			return fmt.Errorf("invalid U3D model visibility")
		}
	} else if kind == 0xffffff23 {
		node.Kind, node.Resource = "Light", r.text()
	} else if kind == 0xffffff24 {
		node.Kind, node.Resource = "View", r.text()
		var err error
		node.View, err = d.view(r)
		if err != nil {
			return err
		}
	}
	d.nodes[name] = len(d.model.Nodes)
	d.model.Nodes = append(d.model.Nodes, node)
	return r.err
}

// shading 分别读取网格和线集着色列表，保留列表内各着色器的先后次序
// 入参: r 字段读取器, name 目标节点名
// 返回: error 结构、能力或预算错误
func (d *u3dDecoder) shading(r *u3dValues, name string) error {
	index, exists := d.nodes[name]
	if !exists || d.model.Nodes[index].Kind != "Model" {
		return fmt.Errorf("missing U3D shading target")
	}
	flags := r.u32()
	if flags & ^uint32(3) != 0 {
		return fmt.Errorf("unsupported U3D shading modifier target")
	}
	count := r.u32()
	if uint64(count) > uint64(len(r.data)-r.pos)/4 {
		return io.ErrUnexpectedEOF
	}
	if err := d.reserve(uint64(count), 24); err != nil {
		return err
	}
	lists := make([][]string, int(count))
	for index := range lists {
		n := r.u32()
		if uint64(n) > uint64(len(r.data)-r.pos)/2 {
			return io.ErrUnexpectedEOF
		}
		if err := d.reserve(uint64(n), 16); err != nil {
			return err
		}
		lists[index] = make([]string, int(n))
		for j := range lists[index] {
			if j%256 == 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			lists[index][j] = r.text()
		}
	}
	if flags&1 != 0 {
		d.model.Nodes[index].Shaders = lists
	}
	if flags&2 != 0 {
		d.model.Nodes[index].LineShaders = lists
	}
	return r.err
}

// resource 读取独立材质、着色器、光源和视图资源
// 入参: r 字段读取器, kind 资源类型, name 资源名
// 返回: error 结构或能力错误
func (d *u3dDecoder) resource(r *u3dValues, kind uint32, name string) error {
	palette := d.palettes[kind]
	if palette == nil {
		palette = make(map[string]bool)
		d.palettes[kind] = palette
	}
	if palette[name] {
		return fmt.Errorf("duplicate U3D resource %q", name)
	}
	palette[name] = true
	switch kind {
	case 0xffffff52:
		return d.viewResource(r, name)
	case 0xffffff54:
		material := U3DMaterial{Name: name, Attributes: r.u32()}
		if material.Attributes & ^uint32(63) != 0 {
			return fmt.Errorf("invalid U3D material attributes")
		}
		for _, color := range []*[3]float32{&material.Ambient, &material.Diffuse, &material.Specular, &material.Emissive} {
			for index := range color {
				color[index] = r.f32()
			}
		}
		material.Reflectivity, material.Opacity = r.f32(), r.f32()
		d.model.Materials = append(d.model.Materials, material)
	case 0xffffff53:
		shader := U3DShader{Name: name, Attributes: r.u32(), AlphaReference: r.f32(), AlphaFunction: r.u32(), Blend: r.u32(), RenderPass: r.u32()}
		channels, alpha := r.u32(), r.u32()
		shader.Material = r.text()
		if shader.Attributes & ^uint32(7) != 0 || shader.AlphaFunction < 0x610 || shader.AlphaFunction > 0x617 || shader.Blend < 0x604 || shader.Blend > 0x607 || channels & ^uint32(255) != 0 || alpha & ^channels != 0 {
			return fmt.Errorf("invalid U3D shader parameters")
		}
		if channels != 0 {
			return fmt.Errorf("unsupported U3D textured shader")
		}
		d.model.Shaders = append(d.model.Shaders, shader)
	case 0xffffff51:
		light := U3DLight{Name: name, Attributes: r.u32(), Kind: r.u8()}
		if light.Attributes & ^uint32(7) != 0 || light.Kind > 3 {
			return fmt.Errorf("invalid U3D light parameters")
		}
		for index := range light.Color {
			light.Color[index] = r.f32()
		}
		if r.f32() != 1 {
			return fmt.Errorf("invalid U3D light reserved parameter")
		}
		for index := range light.Attenuation {
			light.Attenuation[index] = r.f32()
		}
		light.SpotAngle, light.Intensity = r.f32(), r.f32()
		d.model.Lights = append(d.model.Lights, light)
	}
	return r.err
}

// checkParents 迭代检查场景父子环路，不递归展开多父节点的实例组合
// 返回: error 环路或取消错误
func (d *u3dDecoder) checkParents() error {
	state := make([]byte, len(d.model.Nodes))
	type frame struct{ node, parent int }
	var stack []frame
	for index := range d.model.Nodes {
		if state[index] != 0 {
			continue
		}
		stack = append(stack[:0], frame{node: index})
		state[index] = 1
		for len(stack) != 0 {
			if err := d.ctx.Err(); err != nil {
				return err
			}
			current := &stack[len(stack)-1]
			parents := d.model.Nodes[current.node].Parents
			if current.parent == len(parents) {
				state[current.node] = 2
				stack = stack[:len(stack)-1]
				continue
			}
			parent, exists := d.nodes[parents[current.parent].Name]
			current.parent++
			if !exists || state[parent] == 2 {
				continue
			}
			if state[parent] == 1 {
				return fmt.Errorf("cyclic U3D node parents")
			}
			state[parent] = 1
			stack = append(stack, frame{node: parent})
		}
	}
	return nil
}
