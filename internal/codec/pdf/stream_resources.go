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
	"bytes"
	"image/color"
	"strconv"
	"sync"

	"github.com/tdewolff/canvas"
	"github.com/xiaoqidun/pdfgo"
)

// GraphicResourceRegistry 管理文档级去重ExtGState、ShadingPattern与Resources字典对象
type GraphicResourceRegistry struct {
	mu           sync.Mutex
	allocRef     func() pdfgo.Reference
	extGStates   map[uint8]pdfgo.Reference
	patterns     map[string]patternItem
	patternSeq   int
	pendingObjs  []pendingDictObj
	resourceRefs map[string]pdfgo.Reference
}

// NewGraphicResourceRegistry 创建文档级图形资源去重注册表
// 入参: allocRef 对象引用分配器
// 返回: *GraphicResourceRegistry 图形资源注册表
func NewGraphicResourceRegistry(allocRef func() pdfgo.Reference) *GraphicResourceRegistry {
	return &GraphicResourceRegistry{
		allocRef:     allocRef,
		extGStates:   make(map[uint8]pdfgo.Reference),
		patterns:     make(map[string]patternItem),
		resourceRefs: make(map[string]pdfgo.Reference),
	}
}

// OpacityGS 获取或创建指定八位透明度对应的共享ExtGState对象
// 入参: alpha 八位透明度值
// 返回: pdfgo.Name 状态名, pdfgo.Reference 对象引用
func (r *GraphicResourceRegistry) OpacityGS(alpha uint8) (pdfgo.Name, pdfgo.Reference) {
	name := pdfgo.Name("GS" + strconv.Itoa(int(alpha)))
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref, ok := r.extGStates[alpha]; ok {
		return name, ref
	}
	ref := r.allocRef()
	a := pdfgo.Real(float64(alpha) / 255.0)
	dict := pdfgo.Dictionary{"Type": pdfgo.Name("ExtGState"), "CA": a, "ca": a}
	r.extGStates[alpha] = ref
	r.pendingObjs = append(r.pendingObjs, pendingDictObj{ref: ref, dict: dict})
	return name, ref
}

// ShadingPattern 获取或创建按完整字典内容去重的渐变Pattern对象
// 入参: gradient 画布渐变, m 变换矩阵
// 返回: pdfgo.Name Pattern名, pdfgo.Reference 对象引用
func (r *GraphicResourceRegistry) ShadingPattern(gradient canvas.Gradient, m canvas.Matrix) (pdfgo.Name, pdfgo.Reference) {
	dict := buildShadingPatternDict(gradient, m)
	var keyBuf bytes.Buffer
	appendPDFObject(&keyBuf, dict)
	key := keyBuf.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if item, ok := r.patterns[key]; ok {
		return item.name, item.ref
	}
	ref := r.allocRef()
	name := pdfgo.Name("P" + strconv.Itoa(r.patternSeq))
	r.patternSeq++
	r.patterns[key] = patternItem{name: name, ref: ref}
	r.pendingObjs = append(r.pendingObjs, pendingDictObj{ref: ref, dict: dict})
	return name, ref
}

// FlushPending 将新增的ExtGState与ShadingPattern字典流式写入对象流并清空待写队列
// 入参: writer 流式PDF写出器
// 返回: error 写入错误
func (r *GraphicResourceRegistry) FlushPending(writer *StreamWriter) error {
	r.mu.Lock()
	items := r.pendingObjs
	r.pendingObjs = nil
	r.mu.Unlock()
	for _, item := range items {
		if err := writer.WriteObject(item.ref, item.dict); err != nil {
			return err
		}
	}
	return nil
}

// ResourceDictRef 复用或流式写出完全相同的Resources间接字典对象，供同资源页面共享
// 入参: writer 流式PDF写出器, res 资源集合
// 返回: pdfgo.Reference 资源字典引用, error 写入错误
func (r *GraphicResourceRegistry) ResourceDictRef(writer *StreamWriter, res *ContentResources) (pdfgo.Reference, error) {
	key := res.Key()
	r.mu.Lock()
	if ref, ok := r.resourceRefs[key]; ok {
		r.mu.Unlock()
		return ref, nil
	}
	ref := r.allocRef()
	r.resourceRefs[key] = ref
	r.mu.Unlock()
	if err := writer.WriteObject(ref, res.Dictionary()); err != nil {
		return pdfgo.Reference{}, err
	}
	return ref, nil
}

// FormXObjectDict 构造标准PDF Form XObject流字典，可选附加透明组隔离字典
// 入参: widthMM 表单宽度(毫米), heightMM 表单高度(毫米), resourcesRef 资源字典引用, transparencyGroup 是否开启隔离透明组
// 返回: pdfgo.Dictionary Form流字典
func FormXObjectDict(widthMM, heightMM float64, resourcesRef pdfgo.Reference, transparencyGroup bool) pdfgo.Dictionary {
	dict := pdfgo.Dictionary{
		"Type":      pdfgo.Name("XObject"),
		"Subtype":   pdfgo.Name("Form"),
		"FormType":  pdfgo.Integer(1),
		"BBox":      pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Real(widthMM * PtPerMM), pdfgo.Real(heightMM * PtPerMM)},
		"Resources": resourcesRef,
	}
	if transparencyGroup {
		dict["Group"] = pdfgo.Dictionary{
			"Type": pdfgo.Name("Group"),
			"S":    pdfgo.Name("Transparency"),
			"I":    pdfgo.Boolean(true),
			"CS":   pdfgo.Name("DeviceRGB"),
		}
	}
	return dict
}

// pendingDictObj 记录待流式写出的非流字典资源对象
type pendingDictObj struct {
	ref  pdfgo.Reference
	dict pdfgo.Dictionary
}

// patternItem 保存已登记的渐变Pattern资源名与引用
type patternItem struct {
	name pdfgo.Name
	ref  pdfgo.Reference
}

// buildShadingPatternDict 按ISO 32000-1构造线性或径向渐变PatternType 2字典
// 入参: gradient 画布渐变, m 变换矩阵
// 返回: pdfgo.Dictionary Pattern字典
func buildShadingPatternDict(gradient canvas.Gradient, m canvas.Matrix) pdfgo.Dictionary {
	shading := pdfgo.Dictionary{"ColorSpace": pdfgo.Name("DeviceRGB")}
	switch g := gradient.(type) {
	case *canvas.LinearGradient:
		shading["ShadingType"] = pdfgo.Integer(2)
		shading["Coords"] = pdfgo.Array{pdfgo.Real(g.Start.X * PtPerMM), pdfgo.Real(g.Start.Y * PtPerMM), pdfgo.Real(g.End.X * PtPerMM), pdfgo.Real(g.End.Y * PtPerMM)}
		shading["Function"] = buildGradientFunction(g.Grad)
		shading["Extend"] = pdfgo.Array{pdfgo.Boolean(true), pdfgo.Boolean(true)}
	case *canvas.RadialGradient:
		shading["ShadingType"] = pdfgo.Integer(3)
		shading["Coords"] = pdfgo.Array{pdfgo.Real(g.C0.X * PtPerMM), pdfgo.Real(g.C0.Y * PtPerMM), pdfgo.Real(g.R0 * PtPerMM), pdfgo.Real(g.C1.X * PtPerMM), pdfgo.Real(g.C1.Y * PtPerMM), pdfgo.Real(g.R1 * PtPerMM)}
		shading["Function"] = buildGradientFunction(g.Grad)
		shading["Extend"] = pdfgo.Array{pdfgo.Boolean(true), pdfgo.Boolean(true)}
	}
	return pdfgo.Dictionary{
		"Type":        pdfgo.Name("Pattern"),
		"PatternType": pdfgo.Integer(2),
		"Shading":     shading,
		"Matrix": pdfgo.Array{
			pdfgo.Real(m[0][0]), pdfgo.Real(m[1][0]),
			pdfgo.Real(m[0][1]), pdfgo.Real(m[1][1]),
			pdfgo.Real(m[0][2] * PtPerMM), pdfgo.Real(m[1][2] * PtPerMM),
		},
	}
}

// buildGradientFunction 构造多色标拼接Type 3或单段Type 2插值函数字典
// 入参: grad 色标序列
// 返回: pdfgo.Dictionary 函数字典
func buildGradientFunction(grad canvas.Grad) pdfgo.Dictionary {
	if len(grad) < 2 {
		return pdfgo.Dictionary{}
	}
	fs := make(pdfgo.Array, 0, len(grad)-1)
	bounds := make(pdfgo.Array, 0, max(0, len(grad)-2))
	encode := make(pdfgo.Array, 0, 2*(len(grad)-1))
	for i := 0; i < len(grad)-1; i++ {
		fs = append(fs, pdfgo.Dictionary{
			"FunctionType": pdfgo.Integer(2),
			"Domain":       pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(1)},
			"N":            pdfgo.Integer(1),
			"C0":           unpremultiplyGradientStop(grad[i].Color),
			"C1":           unpremultiplyGradientStop(grad[i+1].Color),
		})
		if i > 0 {
			bounds = append(bounds, pdfgo.Real(grad[i].Offset))
		}
		encode = append(encode, pdfgo.Integer(0), pdfgo.Integer(1))
	}
	if len(fs) == 1 {
		fn := fs[0].(pdfgo.Dictionary)
		fn["Domain"] = pdfgo.Array{pdfgo.Real(grad[0].Offset), pdfgo.Real(grad[len(grad)-1].Offset)}
		return fn
	}
	return pdfgo.Dictionary{
		"FunctionType": pdfgo.Integer(3),
		"Domain":       pdfgo.Array{pdfgo.Real(grad[0].Offset), pdfgo.Real(grad[len(grad)-1].Offset)},
		"Bounds":       bounds,
		"Encode":       encode,
		"Functions":    fs,
	}
}

// unpremultiplyGradientStop 将预乘色标还原为Type 2函数所需的非预乘RGB三元组
// 入参: c 预乘颜色
// 返回: pdfgo.Array RGB浮点数组
func unpremultiplyGradientStop(c color.RGBA) pdfgo.Array {
	if c.A == 0 {
		return pdfgo.Array{pdfgo.Integer(0), pdfgo.Integer(0), pdfgo.Integer(0)}
	}
	a := float64(c.A) / 255.0
	return pdfgo.Array{pdfgo.Real(float64(c.R) / 255.0 / a), pdfgo.Real(float64(c.G) / 255.0 / a), pdfgo.Real(float64(c.B) / 255.0 / a)}
}
