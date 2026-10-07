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

// U3DMesh 保存静态三角面和线段的共享属性数组，索引对应原始作者几何
type U3DMesh struct {
	Name                                        string
	ExcludeNormals                              bool
	Positions, Normals                          [][3]float32
	Diffuse, Specular, TextureCoordinates       [][4]float32
	Shadings                                    []U3DShading
	Faces                                       []U3DFace
	Lines                                       []U3DLine
	Quality                                     [3]uint32
	InverseQuantization                         [5]float32
	NormalCrease, NormalUpdate, NormalTolerance float32
}

// U3DShading 描述图元使用的颜色及纹理坐标，纹理维数依层排列
type U3DShading struct {
	Attributes        uint32
	TextureDimensions []uint32
	OriginalID        uint32
}

// U3DFace 保存着色描述索引和依原始绕序排列的三个角点
type U3DFace struct {
	Shading uint32
	Corners [3]U3DCorner
}

// U3DLine 保存着色描述及线段两端的独立属性索引
type U3DLine struct {
	Shading uint32
	Corners [2]U3DCorner
}

// U3DCorner 保存各属性索引，未启用的属性不可用于访问数组
type U3DCorner struct {
	Position, Normal, Diffuse, Specular uint32
	Texture                             [8]uint32
}

// u3dMeshDeclaration 保存基础网格或线集声明及续块接收状态
type u3dMeshDeclaration struct {
	mesh    U3DMesh
	counts  [7]uint32
	decoded bool
	line    *u3dLineState
}

// primitive 访问三角面或线段的着色编号和角点，不复制属性数组
// 入参: index 图元下标，三角面在前，线段在后
// 返回: uint32 着色编号, []U3DCorner 只读角点
func (m *U3DMesh) primitive(index int) (uint32, []U3DCorner) {
	if index < len(m.Faces) {
		return m.Faces[index].Shading, m.Faces[index].Corners[:]
	}
	line := &m.Lines[index-len(m.Faces)]
	return line.Shading, line.Corners[:]
}

// meshDeclaration 读取完整基础网格声明，不将渐进网格当作完整模型
// 入参: r 字段读取器, name 网格名
// 返回: error 结构、能力或预算错误
func (d *u3dDecoder) meshDeclaration(r *u3dValues, name string) error {
	if _, exists := d.meshes[name]; exists {
		return fmt.Errorf("duplicate U3D mesh %q", name)
	}
	index, attributes := r.u32(), r.u32()
	if index != 0 || attributes > 1 {
		return fmt.Errorf("invalid U3D mesh declaration")
	}
	decl := &u3dMeshDeclaration{mesh: U3DMesh{Name: name, ExcludeNormals: attributes == 1}}
	for index := range decl.counts {
		decl.counts[index] = r.u32()
	}
	if err := d.meshShadings(r, decl); err != nil {
		return err
	}
	minimum, maximum := r.u32(), r.u32()
	for index := range decl.mesh.Quality {
		decl.mesh.Quality[index] = r.u32()
	}
	for index := range decl.mesh.InverseQuantization {
		decl.mesh.InverseQuantization[index] = r.f32()
	}
	decl.mesh.NormalCrease, decl.mesh.NormalUpdate, decl.mesh.NormalTolerance = r.f32(), r.f32(), r.f32()
	bones := r.u32()
	if r.err != nil {
		return r.err
	}
	if minimum > maximum || maximum != decl.counts[1] {
		return fmt.Errorf("invalid U3D mesh resolution")
	}
	if minimum != maximum || bones != 0 {
		return fmt.Errorf("unsupported U3D progressive or skeletal mesh")
	}
	if minimum == 0 {
		for _, count := range decl.counts[:6] {
			if count != 0 {
				return fmt.Errorf("invalid U3D empty mesh")
			}
		}
	}
	d.meshes[name] = decl
	d.meshOrder = append(d.meshOrder, name)
	return nil
}

// meshShadings 读取三角面与线集共用的着色描述
// 入参: r 字段读取器, decl 资源声明
// 返回: error 结构或预算错误
func (d *u3dDecoder) meshShadings(r *u3dValues, decl *u3dMeshDeclaration) error {
	if r.err != nil {
		return r.err
	}
	count := decl.counts[6]
	if uint64(count) > uint64(len(r.data)-r.pos)/12 {
		return io.ErrUnexpectedEOF
	}
	if err := d.reserve(uint64(count), 40); err != nil {
		return err
	}
	decl.mesh.Shadings = make([]U3DShading, int(count))
	for index := range decl.mesh.Shadings {
		shading := &decl.mesh.Shadings[index]
		shading.Attributes = r.u32()
		layers := r.u32()
		if shading.Attributes > 3 {
			return fmt.Errorf("invalid U3D shading attributes")
		}
		if layers > 8 {
			return fmt.Errorf("unsupported U3D texture layer count")
		}
		if err := d.reserve(uint64(layers), 4); err != nil {
			return err
		}
		shading.TextureDimensions = make([]uint32, int(layers))
		for layer := range shading.TextureDimensions {
			dimension := r.u32()
			if dimension < 1 || dimension > 4 {
				return fmt.Errorf("invalid U3D texture coordinate dimension")
			}
			shading.TextureDimensions[layer] = dimension
		}
		shading.OriginalID = r.u32()
	}
	return r.err
}

// baseMesh 读取基础顶点与压缩面索引，保留各角点的独立属性
// 入参: r 字段读取器, name 网格名
// 返回: error 结构、能力、预算或取消错误
func (d *u3dDecoder) baseMesh(r *u3dValues, name string) error {
	decl := d.meshes[name]
	if decl == nil || decl.line != nil || decl.decoded || decl.counts[1] == 0 || r.u32() != 0 {
		return fmt.Errorf("invalid U3D base mesh reference")
	}
	var counts [6]uint32
	for index := range counts {
		counts[index] = r.u32()
		if counts[index] != decl.counts[index] {
			return fmt.Errorf("U3D base mesh does not match full resolution declaration")
		}
	}
	if r.err != nil {
		return r.err
	}
	literalBytes := (uint64(counts[1])+uint64(counts[2]))*12 + (uint64(counts[3])+uint64(counts[4])+uint64(counts[5]))*16
	if literalBytes > uint64(len(r.data)-r.pos) {
		return io.ErrUnexpectedEOF
	}
	if err := d.reserve(literalBytes, 1); err != nil {
		return err
	}
	if err := d.reserve(uint64(counts[0]), 148); err != nil {
		return err
	}
	mesh := &decl.mesh
	mesh.Positions, mesh.Normals = make([][3]float32, int(counts[1])), make([][3]float32, int(counts[2]))
	mesh.Diffuse, mesh.Specular, mesh.TextureCoordinates = make([][4]float32, int(counts[3])), make([][4]float32, int(counts[4])), make([][4]float32, int(counts[5]))
	for _, array := range [][][3]float32{mesh.Positions, mesh.Normals} {
		for index := range array {
			if index%1024 == 0 {
				if r.err != nil {
					return r.err
				}
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			for component := range array[index] {
				array[index][component] = r.f32()
			}
		}
	}
	for _, array := range [][][4]float32{mesh.Diffuse, mesh.Specular, mesh.TextureCoordinates} {
		for index := range array {
			if index%1024 == 0 {
				if r.err != nil {
					return r.err
				}
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			for component := range array[index] {
				array[index][component] = r.f32()
			}
		}
	}
	if r.err != nil {
		return r.err
	}
	mesh.Faces = make([]U3DFace, int(counts[0]))
	var histogram u3dHistogram
	var compressed *u3dBits
	if !d.plain && counts[0] != 0 {
		if err := d.reserve(1, 1<<20); err != nil {
			return err
		}
		compressed = newU3DBits(r.data[r.pos:])
	}
	readIndex := func(count uint32) (uint32, error) {
		var value uint32
		if compressed == nil {
			value = r.u32()
		} else {
			value = compressed.index(count)
			if compressed.err != nil {
				return 0, compressed.err
			}
		}
		if r.err != nil {
			return 0, r.err
		}
		if value >= count {
			return 0, fmt.Errorf("invalid U3D corner index")
		}
		return value, nil
	}
	for index := range mesh.Faces {
		if index%256 == 0 {
			if err := d.ctx.Err(); err != nil {
				return err
			}
		}
		face := &mesh.Faces[index]
		if compressed == nil {
			face.Shading = r.u32()
		} else {
			face.Shading = compressed.dynamic(&histogram)
			if compressed.err != nil {
				return compressed.err
			}
		}
		if r.err != nil {
			return r.err
		}
		if uint64(face.Shading) >= uint64(len(mesh.Shadings)) {
			return fmt.Errorf("invalid U3D face shading index")
		}
		shading := &mesh.Shadings[face.Shading]
		for cornerIndex := range face.Corners {
			corner := &face.Corners[cornerIndex]
			var err error
			if corner.Position, err = readIndex(counts[1]); err != nil {
				return err
			}
			if !mesh.ExcludeNormals {
				if corner.Normal, err = readIndex(counts[2]); err != nil {
					return err
				}
			}
			if shading.Attributes&1 != 0 {
				if corner.Diffuse, err = readIndex(counts[3]); err != nil {
					return err
				}
			}
			if shading.Attributes&2 != 0 {
				if corner.Specular, err = readIndex(counts[4]); err != nil {
					return err
				}
			}
			for layer := range shading.TextureDimensions {
				if corner.Texture[layer], err = readIndex(counts[5]); err != nil {
					return err
				}
			}
		}
	}
	if compressed != nil {
		if compressed.pos > uint64(len(compressed.data))*8 {
			return io.ErrUnexpectedEOF
		}
		if compressed.u32() != 0 {
			return fmt.Errorf("invalid U3D compression flush")
		}
		if compressed.err != nil {
			return compressed.err
		}
		if (compressed.pos-16+7)/8 != uint64(len(compressed.data)) {
			return fmt.Errorf("unexpected U3D compressed block data: decoded %d bits, stored %d bytes", compressed.pos-16, len(compressed.data))
		}
		r.pos = len(r.data)
	}
	decl.decoded = true
	return r.done()
}
