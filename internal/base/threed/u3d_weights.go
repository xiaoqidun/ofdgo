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
	"fmt"
	"math"
)

// U3DBoneWeight 保存某骨骼对一个位置的影响系数
type U3DBoneWeight struct {
	Bone   uint32
	Weight float32
}

// U3DBoneWeights 保存修饰链中的权重，Index为链内位置，Attributes的1、2、4分别表示网格、线集、点集
// 第i个位置的权重为Weights[Offsets[i]:Offsets[i+1]]，空区间表示没有骨骼影响
type U3DBoneWeights struct {
	Index, Attributes   uint32
	InverseQuantization float32
	Offsets             []uint32
	Weights             []U3DBoneWeight
}

// boneWeights 读取ECMA-363第9.7.4节权重修饰器，末项权重由其余各项之和确定
// 入参: values 字段读取器, name 所属链名, chain 链类型, index 链内位置
// 返回: error 结构、数值、预算或取消错误
func (d *u3dDecoder) boneWeights(values *u3dValues, name string, chain int, index uint32) error {
	r := u3dResourceReader{values: values, decoder: d}
	defer r.release()
	weights := U3DBoneWeights{Index: index, Attributes: r.u32(), InverseQuantization: r.f32()}
	positions := r.u32()
	if err := r.err(); err != nil {
		return err
	}
	if weights.Attributes != 1 && weights.Attributes != 2 && weights.Attributes != 4 || weights.InverseQuantization < 0 {
		return fmt.Errorf("invalid U3D bone weight parameters")
	}
	if err := d.reserve(uint64(positions)+1, 4); err != nil {
		return err
	}
	weights.Offsets = make([]uint32, int(positions)+1)
	for position := range positions {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		count := r.value(0, false)
		if err := r.err(); err != nil {
			return err
		}
		start := len(weights.Weights)
		needed := uint64(start) + uint64(count)
		if needed > math.MaxUint32 {
			return fmt.Errorf("U3D bone weight count exceeds limit")
		}
		if needed > uint64(cap(weights.Weights)) {
			capacity := max(needed, uint64(cap(weights.Weights))*2)
			if err := d.reserve(capacity, 8); err != nil {
				return err
			}
			grown := make([]U3DBoneWeight, int(needed), int(capacity))
			copy(grown, weights.Weights)
			d.remaining += uint64(cap(weights.Weights)) * 8
			weights.Weights = grown
		} else {
			weights.Weights = weights.Weights[:int(needed)]
		}
		list := weights.Weights[start:]
		for i := range list {
			if i%256 == 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			list[i].Bone = r.value(1, false)
			if err := r.err(); err != nil {
				return err
			}
		}
		var total float64
		for i := 0; i+1 < len(list); i++ {
			if i%256 == 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			list[i].Weight = float32(float64(r.value(2, false)) * float64(weights.InverseQuantization))
			total += float64(list[i].Weight)
			if err := r.err(); err != nil {
				return err
			}
			if !finite(total) || total > 1+1e-6 {
				return fmt.Errorf("invalid U3D bone weight sum")
			}
		}
		if len(list) != 0 {
			list[len(list)-1].Weight = float32(max(0, 1-total))
		}
		weights.Offsets[position+1] = uint32(needed)
	}
	if err := r.done(); err != nil {
		return err
	}
	if chain == 1 {
		d.meshes[name].mesh.BoneWeights = append(d.meshes[name].mesh.BoneWeights, weights)
	} else {
		node := &d.model.Nodes[d.nodes[name]]
		node.BoneWeights = append(node.BoneWeights, weights)
	}
	return nil
}

// checkBoneWeights 在几何和节点声明齐备后校验权重引用，允许节点引用后续资源
// 返回: error 位置、骨骼引用或取消错误
func (d *u3dDecoder) checkBoneWeights() error {
	for _, name := range d.meshOrder {
		mesh := d.meshes[name]
		if err := d.checkMeshBoneWeights(mesh, mesh.mesh.BoneWeights); err != nil {
			return err
		}
	}
	for _, node := range d.model.Nodes {
		if err := d.checkMeshBoneWeights(d.meshes[node.Resource], node.BoneWeights); err != nil {
			return err
		}
	}
	return nil
}

// checkMeshBoneWeights 校验适用于指定几何类型的权重，不把未匹配的类型强加到几何上
// 入参: mesh 几何声明，可为空, sets 权重修饰器
// 返回: error 位置、骨骼引用或取消错误
func (d *u3dDecoder) checkMeshBoneWeights(mesh *u3dMeshDeclaration, sets []U3DBoneWeights) error {
	for _, weights := range sets {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		if mesh == nil {
			if len(weights.Weights) != 0 {
				return fmt.Errorf("missing U3D bone weight geometry")
			}
			continue
		}
		kind := uint32(1)
		if mesh.points {
			kind = 4
		} else if mesh.line != nil {
			kind = 2
		}
		if weights.Attributes != kind {
			continue
		}
		if uint64(len(weights.Offsets)-1) > uint64(len(mesh.mesh.Positions)) {
			return fmt.Errorf("invalid U3D bone weight position count")
		}
		for index, weight := range weights.Weights {
			if index%1024 == 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			if uint64(weight.Bone) >= uint64(len(mesh.mesh.Bones)) {
				return fmt.Errorf("invalid U3D bone weight index")
			}
		}
	}
	return nil
}
