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
	"fmt"
	"io"
)

const (
	u3dLinePositionSign = iota
	u3dLinePositionX
	u3dLinePositionY
	u3dLinePositionZ
	u3dLineNormalCount
	u3dLineNormalSign
	u3dLineNormalX
	u3dLineNormalY
	u3dLineNormalZ
	u3dLineCount
	u3dLineShading
	u3dLineNormalIndex
	u3dLineDiffuseDuplicate
	u3dLineSpecularDuplicate
	u3dLineTextureDuplicate
	u3dLineDiffuseSign
	u3dLineSpecularSign
	u3dLineTextureSign
	u3dLineColorR
	u3dLineColorG
	u3dLineColorB
	u3dLineColorA
	u3dLineTextureU
	u3dLineTextureV
	u3dLineTextureS
	u3dLineTextureT
	u3dLineContextCount
)

// u3dLineState 按顶点累计线段端点属性，预测开销不随顶点度数增长
type u3dLineState struct {
	offsets [11]int
	stride  int
	stats   []float64
}

// u3dLineReader 保存当前续块的独立压缩上下文
type u3dLineReader struct {
	values *u3dValues
	bits   *u3dBits
	hist   [u3dLineContextCount]u3dHistogram
}

// lineDeclaration 读取ECMA-363第9.6.3节的线集声明
// 入参: r 字段读取器, name 资源名
// 返回: error 结构、能力或预算错误
func (d *u3dDecoder) lineDeclaration(r *u3dValues, name string) error {
	if _, exists := d.meshes[name]; exists {
		return fmt.Errorf("duplicate U3D model resource %q", name)
	}
	if r.u32() != 0 || r.u32() != 0 {
		return fmt.Errorf("invalid U3D line declaration")
	}
	decl := &u3dMeshDeclaration{mesh: U3DMesh{Name: name}, line: new(u3dLineState)}
	for i := range decl.counts {
		decl.counts[i] = r.u32()
	}
	if err := d.meshShadings(r, decl); err != nil {
		return err
	}
	for i := range decl.mesh.Quality {
		decl.mesh.Quality[i] = r.u32()
	}
	for i := range decl.mesh.InverseQuantization {
		decl.mesh.InverseQuantization[i] = r.f32()
	}
	for range 3 {
		if r.u32() != 0 {
			return fmt.Errorf("invalid U3D reserved line parameter")
		}
	}
	if r.u32() != 0 {
		return fmt.Errorf("unsupported U3D skeletal line set")
	}
	if err := r.done(); err != nil {
		return err
	}
	if decl.counts[1] == 0 {
		for _, count := range decl.counts[:6] {
			if count != 0 {
				return fmt.Errorf("invalid U3D empty line set")
			}
		}
		decl.decoded = true
	}
	state := decl.line
	var used [11]bool
	used[0] = true
	for _, shading := range decl.mesh.Shadings {
		used[1] = used[1] || shading.Attributes&1 != 0
		used[2] = used[2] || shading.Attributes&2 != 0
		for layer := range shading.TextureDimensions {
			used[3+layer] = true
		}
	}
	for i, present := range used {
		state.offsets[i] = -1
		if present {
			state.offsets[i] = state.stride
			state.stride += 5
		}
	}
	c := decl.counts
	bytes := uint64(c[0])*100 + (uint64(c[1])+uint64(c[2]))*12 + (uint64(c[3])+uint64(c[4])+uint64(c[5]))*16
	if err := d.reserve(bytes, 1); err != nil {
		return err
	}
	if err := d.reserve(uint64(c[1]), uint64(state.stride)*8); err != nil {
		return err
	}
	m := &decl.mesh
	m.Lines = make([]U3DLine, 0, int(c[0]))
	m.Positions, m.Normals = make([][3]float32, 0, int(c[1])), make([][3]float32, 0, int(c[2]))
	m.Diffuse, m.Specular, m.TextureCoordinates = make([][4]float32, 0, int(c[3])), make([][4]float32, 0, int(c[4])), make([][4]float32, 0, int(c[5]))
	state.stats = make([]float64, int(c[1])*state.stride)
	d.meshes[name] = decl
	d.meshOrder = append(d.meshOrder, name)
	return nil
}

// average 取得指定顶点各属性的端点算术平均
// 入参: position 顶点索引
// 返回: [11][4]float64 法线、漫反射、镜面及八层纹理预测
func (s *u3dLineState) average(position uint32) (values [11][4]float64) {
	for slot, offset := range s.offsets {
		if offset < 0 {
			continue
		}
		base := int(position)*s.stride + offset
		count := s.stats[base+4]
		if count != 0 {
			for i := range values[slot] {
				values[slot][i] = s.stats[base+i] / count
			}
		}
	}
	return values
}

// accumulate 累计线段端点的独立属性，重复使用的属性按端点分别计数
// 入参: mesh 共享属性数组, line 已完成的线段
func (s *u3dLineState) accumulate(mesh *U3DMesh, line U3DLine) {
	shading := mesh.Shadings[line.Shading]
	for _, corner := range line.Corners {
		var values [11][4]float32
		var used [11]bool
		normal := mesh.Normals[corner.Normal]
		values[0] = [4]float32{normal[0], normal[1], normal[2], 0}
		used[0] = true
		if shading.Attributes&1 != 0 {
			values[1], used[1] = mesh.Diffuse[corner.Diffuse], true
		}
		if shading.Attributes&2 != 0 {
			values[2], used[2] = mesh.Specular[corner.Specular], true
		}
		for layer := range shading.TextureDimensions {
			values[3+layer], used[3+layer] = mesh.TextureCoordinates[corner.Texture[layer]], true
		}
		for slot, present := range used {
			if !present {
				continue
			}
			base := int(corner.Position)*s.stride + s.offsets[slot]
			for i, value := range values[slot] {
				s.stats[base+i] += float64(value)
			}
			s.stats[base+4]++
		}
	}
}

// value 按指定上下文和位宽读取线集字段
// 入参: context 压缩上下文, small 是否为8位字段
// 返回: uint32 字段值
func (r *u3dLineReader) value(context int, small bool) uint32 {
	if r.bits != nil {
		return r.bits.dynamicValue(&r.hist[context], small)
	}
	if small {
		return uint32(r.values.u8())
	}
	return r.values.u32()
}

// index 读取静态索引并校验声明范围
// 入参: count 可用索引数量
// 返回: uint32 索引, error 读取或范围错误
func (r *u3dLineReader) index(count uint32) (uint32, error) {
	if count == 0 {
		return 0, fmt.Errorf("invalid U3D line position range")
	}
	var value uint32
	if r.bits != nil {
		value = r.bits.index(count)
	} else {
		value = r.values.u32()
	}
	if err := r.err(); err != nil {
		return 0, err
	}
	if value >= count {
		return 0, fmt.Errorf("invalid U3D line position index")
	}
	return value, nil
}

// err 返回当前字段或算术解码错误
// 返回: error 读取错误
func (r *u3dLineReader) err() error {
	if r.bits != nil {
		return r.bits.err
	}
	return r.values.err
}

// reconstruct 按符号位和独立分量上下文执行逆量化
// 入参: prediction 预测值, scale 逆量化系数, signContext 符号上下文, componentContext 分量首上下文, dimensions 分量数
// 返回: [4]float32 重建属性, error 数值或读取错误
func (r *u3dLineReader) reconstruct(prediction [4]float64, scale float32, signContext, componentContext, dimensions int) (values [4]float32, err error) {
	signs := r.value(signContext, true)
	if signs >= 1<<dimensions {
		return values, fmt.Errorf("invalid U3D line difference signs")
	}
	for i := range dimensions {
		difference := float64(r.value(componentContext+i, false)) * float64(scale)
		if signs&(1<<i) != 0 {
			difference = -difference
		}
		values[i] = float32(prediction[i] + difference)
		if !finite(float64(values[i])) {
			return values, fmt.Errorf("invalid U3D reconstructed line attribute")
		}
	}
	return values, r.err()
}

// attribute 读取颜色或纹理属性，重复标记引用所属属性池的末项
// 入参: pool 属性池, limit 声明总数, prediction 预测值, scale 逆量化系数, duplicate 重复上下文, sign 符号上下文, component 分量首上下文
// 返回: uint32 属性索引, error 读取或范围错误
func (r *u3dLineReader) attribute(pool *[][4]float32, limit uint32, prediction [4]float64, scale float32, duplicate, sign, component int) (uint32, error) {
	flag := r.value(duplicate, true)
	if err := r.err(); err != nil {
		return 0, err
	}
	if flag == 2 && len(*pool) > 0 {
		return uint32(len(*pool) - 1), nil
	}
	if flag != 0 || uint64(len(*pool)) >= uint64(limit) {
		return 0, fmt.Errorf("invalid U3D line attribute pool")
	}
	value, err := r.reconstruct(prediction, scale, sign, component, 4)
	if err != nil {
		return 0, err
	}
	index := uint32(len(*pool))
	*pool = append(*pool, value)
	return index, nil
}

// lineContinuation 按位置分段恢复线集，预测状态跨续块保存
// 入参: r 字段读取器, name 资源名
// 返回: error 结构、数值、预算或取消错误
func (d *u3dDecoder) lineContinuation(r *u3dValues, name string) error {
	decl := d.meshes[name]
	if decl == nil || decl.line == nil || decl.decoded || r.u32() != 0 {
		return fmt.Errorf("invalid U3D line continuation reference")
	}
	start, end := r.u32(), r.u32()
	mesh := &decl.mesh
	if r.err != nil {
		return r.err
	}
	if uint64(start) != uint64(len(mesh.Positions)) || end <= start || end > decl.counts[1] {
		return fmt.Errorf("invalid U3D line resolution range")
	}
	reader := &u3dLineReader{values: r}
	var charged uint64
	if !d.plain {
		reader.bits = newU3DBits(r.data[r.pos:])
		reader.bits.reserve = func(count, size uint64) error {
			if err := d.reserve(count, size); err != nil {
				return err
			}
			charged += count * size
			return nil
		}
		defer func() { d.remaining += charged }()
	}
	for position := start; position < end; position++ {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		split, err := reader.index(max(1, position))
		if err != nil {
			return err
		}
		var prediction [4]float64
		if position > 0 {
			for i, value := range mesh.Positions[split] {
				prediction[i] = float64(value)
			}
		}
		attributes := decl.line.average(split)
		value, err := reader.reconstruct(prediction, mesh.InverseQuantization[0], u3dLinePositionSign, u3dLinePositionX, 3)
		if err != nil {
			return err
		}
		mesh.Positions = append(mesh.Positions, [3]float32{value[0], value[1], value[2]})
		normalStart := uint32(len(mesh.Normals))
		normals := reader.value(u3dLineNormalCount, false)
		if normals > decl.counts[2]-normalStart {
			return fmt.Errorf("invalid U3D line normal count")
		}
		for i := uint32(0); i < normals; i++ {
			if i%256 == 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			value, err := reader.reconstruct(attributes[0], mesh.InverseQuantization[1], u3dLineNormalSign, u3dLineNormalX, 3)
			if err != nil {
				return err
			}
			mesh.Normals = append(mesh.Normals, [3]float32{value[0], value[1], value[2]})
		}
		lines := reader.value(u3dLineCount, false)
		if lines > decl.counts[0]-uint32(len(mesh.Lines)) {
			return fmt.Errorf("invalid U3D line count")
		}
		for i := uint32(0); i < lines; i++ {
			if i%256 == 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			line := U3DLine{Shading: reader.value(u3dLineShading, false)}
			if uint64(line.Shading) >= uint64(len(mesh.Shadings)) {
				return fmt.Errorf("invalid U3D line shading index")
			}
			first, err := reader.index(position)
			if err != nil {
				return err
			}
			line.Corners[0].Position, line.Corners[1].Position = first, position
			shading := mesh.Shadings[line.Shading]
			for j := range line.Corners {
				corner := &line.Corners[j]
				normal := reader.value(u3dLineNormalIndex, false)
				if normal >= normals {
					return fmt.Errorf("invalid U3D line local normal index")
				}
				corner.Normal = normalStart + normal
				if shading.Attributes&1 != 0 {
					corner.Diffuse, err = reader.attribute(&mesh.Diffuse, decl.counts[3], attributes[1], mesh.InverseQuantization[3], u3dLineDiffuseDuplicate, u3dLineDiffuseSign, u3dLineColorR)
					if err != nil {
						return err
					}
				}
				if shading.Attributes&2 != 0 {
					corner.Specular, err = reader.attribute(&mesh.Specular, decl.counts[4], attributes[2], mesh.InverseQuantization[4], u3dLineSpecularDuplicate, u3dLineSpecularSign, u3dLineColorR)
					if err != nil {
						return err
					}
				}
				for layer := range shading.TextureDimensions {
					corner.Texture[layer], err = reader.attribute(&mesh.TextureCoordinates, decl.counts[5], attributes[3+layer], mesh.InverseQuantization[2], u3dLineTextureDuplicate, u3dLineTextureSign, u3dLineTextureU)
					if err != nil {
						return err
					}
				}
			}
			if err := reader.err(); err != nil {
				return err
			}
			mesh.Lines = append(mesh.Lines, line)
			decl.line.accumulate(mesh, line)
		}
		if err := reader.err(); err != nil {
			return err
		}
	}
	if b := reader.bits; b != nil {
		if b.pos > uint64(len(b.data))*8 {
			return io.ErrUnexpectedEOF
		}
		if b.u32() != 0 {
			return fmt.Errorf("invalid U3D line compression flush")
		}
		if b.err != nil {
			return b.err
		}
		if (b.pos-16+7)/8 != uint64(len(b.data)) {
			return fmt.Errorf("unexpected U3D line compressed data")
		}
		r.pos = len(r.data)
	}
	if err := r.done(); err != nil {
		return err
	}
	if end == decl.counts[1] {
		for i, count := range []int{len(mesh.Lines), len(mesh.Positions), len(mesh.Normals), len(mesh.Diffuse), len(mesh.Specular), len(mesh.TextureCoordinates)} {
			if uint64(count) != uint64(decl.counts[i]) {
				return fmt.Errorf("U3D line set does not match declaration")
			}
		}
		decl.decoded = true
		d.remaining += uint64(len(decl.line.stats)) * 8
		decl.line.stats = nil
	}
	return nil
}
