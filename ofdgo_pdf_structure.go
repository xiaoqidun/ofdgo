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
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// PDF逻辑结构标引使用独立命名空间，图元引用沿用ObjectRef语义
const (
	pdfStructureTagType   = "OFDGo.PDFStructure"
	pdfStructureNamespace = "https://ofdgo.aite.me/ns/pdf/structure/1"
)

// PDF元数据使用的RDF、Dublin Core及Adobe命名空间
const (
	pdfMetadataRDF = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	pdfMetadataDC  = "http://purl.org/dc/elements/1.1/"
	pdfMetadataXMP = "http://ns.adobe.com/xap/1.0/"
	pdfMetadataPDF = "http://ns.adobe.com/pdf/1.3/"
	pdfMetadataXML = "adobe:ns:meta/"
)

// PDF来源元数据的标引定义
const (
	pdfMetadataTagType      = "OFDGo.PDFMetadata"
	pdfAuxiliaryTagType     = "OFDGo.PDFAuxiliary"
	pdfInformationTagType   = "OFDGo.PDFInformation"
	pdfInformationNamespace = "https://ofdgo.aite.me/ns/pdf/information/1"
)

// pdfStructureBindings 保存来源区段与最终页面图元的关联，不记录临时渲染对象
type pdfStructureBindings struct {
	parents   map[*pdfgo.Reader]map[[2]int64]pdfgo.Reference
	prepared  map[*pdfgo.Reader]bool
	invalid   map[*pdfgo.Reader]bool
	readers   []*pdfgo.Reader
	marked    map[pdfStructureContent]bool
	sequences map[*pdfgo.ContentSequence][]StructureReference
	order     []*pdfgo.ContentSequence
	contents  []*pdfgo.ContentSequence
	objects   map[pdfStructureObject][]StructureReference
}

// objectReferences 汇总注解及XObject调用对应的已提交图元
// 入参: ctx 取消上下文
// 返回: map[pdfStructureObject][]StructureReference 对象引用索引, error 来源或取消错误
func (b *pdfStructureBindings) objectReferences(ctx context.Context) (map[pdfStructureObject][]StructureReference, error) {
	result := make(map[pdfStructureObject][]StructureReference)
	sets := make(map[pdfStructureObject]*pdfStructureReferenceSet)
	appendReferences := func(key pdfStructureObject, sources []StructureReference) error {
		set := sets[key]
		if set == nil {
			set = &pdfStructureReferenceSet{}
			sets[key] = set
		}
		if err := set.append(ctx, sources); err != nil {
			return err
		}
		result[key] = set.references
		return nil
	}
	for key, refs := range b.objects {
		if err := appendReferences(key, refs); err != nil {
			return nil, err
		}
	}
	for _, sequence := range b.order {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if sequence.Object == (pdfgo.Reference{}) {
			continue
		}
		if sequence.Reader == nil || sequence.Page == nil {
			return nil, fmt.Errorf("missing PDF object source")
		}
		key := pdfStructureObject{reader: sequence.Reader, page: sequence.Page.Reference, object: sequence.Object}
		if err := appendReferences(key, b.sequences[sequence]); err != nil {
			return nil, err
		}
	}
	return result, ctx.Err()
}

// observe 保存已读取的标记区段，不登记尚未提交的图元
// 入参: sequence 来源区段
func (b *pdfStructureBindings) observe(sequence *pdfgo.ContentSequence) {
	if sequence != nil && sequence.Properties["MCID"] != nil {
		b.contents = append(b.contents, sequence)
	}
}

// contentProperties 保存区段标签及原始属性，重复调用须具有相同内容
// 入参: values 类型化属性编码器
// 返回: map[pdfStructureContent]StructureNode 属性索引, error 属性或取消错误
func (b *pdfStructureBindings) contentProperties(values *pdfStructureValues) (map[pdfStructureContent]StructureNode, error) {
	result := make(map[pdfStructureContent]StructureNode)
	seen := make(map[*pdfgo.ContentSequence]bool)
	for _, sequences := range [][]*pdfgo.ContentSequence{b.contents, b.order} {
		for _, sequence := range sequences {
			if err := values.ctx.Err(); err != nil {
				return nil, err
			}
			if seen[sequence] || sequence.Reader != values.reader {
				continue
			}
			seen[sequence] = true
			key, ok, err := pdfStructureContentKey(sequence)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			node := StructureNode{Name: "MarkedContent", Namespace: pdfStructureNamespace, Attributes: pdfStructureAttribute("Tag", string(sequence.Tag))}
			properties := sequence.Properties
			if _, ok := properties["MCID"].(pdfgo.Integer); ok {
				properties = maps.Clone(properties)
				delete(properties, "MCID")
			}
			if len(properties) != 0 {
				property, err := values.encode(properties, 0)
				if err != nil {
					return nil, err
				}
				node.Children = append(node.Children, property)
			}
			if previous, ok := result[key]; ok {
				equal, err := pdfStructurePropertiesEqual(values.ctx, previous, node)
				if err != nil {
					return nil, err
				}
				if !equal {
					return nil, fmt.Errorf("conflicting PDF marked content properties")
				}
				continue
			}
			result[key] = node
		}
	}
	return result, values.ctx.Err()
}

// contentReferences 按来源解释MCID并汇总重复调用的目标图元
// 入参: ctx 取消上下文
// 返回: pdfStructureReferences 内容引用索引, error 属性或取消错误
func (b *pdfStructureBindings) contentReferences(ctx context.Context) (pdfStructureReferences, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(pdfStructureReferences)
	sets := make(map[pdfStructureContent]*pdfStructureReferenceSet)
	for _, sequence := range b.order {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key, ok, err := pdfStructureContentKey(sequence)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		set := sets[key]
		if set == nil {
			set = &pdfStructureReferenceSet{}
			sets[key] = set
		}
		if err := set.append(ctx, b.sequences[sequence]); err != nil {
			return nil, err
		}
		result[key] = set.references
	}
	return result, ctx.Err()
}

// append 在页面提交成功后登记已分配的图元标识
// 入参: pageID 页面标识, objects 已分配标识的对象, bindings 已准备的绑定位置
func (b *pdfStructureBindings) append(pageID string, objects []GraphicObject, bindings []pdfStructureBinding) {
	for _, binding := range bindings {
		last := b.reference(pageID, binding.sequence)
		for _, object := range objects[binding.start:binding.end] {
			last.ObjectIDs = append(last.ObjectIDs, editorObjectID(object))
		}
	}
}

// reference 返回已提交来源区段在页面中的引用
// 入参: pageID 页面标识, sequence 来源区段
// 返回: *StructureReference 页面引用
func (b *pdfStructureBindings) reference(pageID string, sequence *pdfgo.ContentSequence) *StructureReference {
	if b.sequences == nil {
		b.sequences = make(map[*pdfgo.ContentSequence][]StructureReference)
	}
	refs := b.sequences[sequence]
	if len(refs) == 0 {
		b.order = append(b.order, sequence)
	}
	if len(refs) == 0 || refs[len(refs)-1].PageID != pageID {
		refs = append(refs, StructureReference{PageID: pageID})
	}
	b.sequences[sequence] = refs
	return &refs[len(refs)-1]
}

// pdfStructureObject 隔离不同文件及页面的整对象引用
type pdfStructureObject struct {
	reader *pdfgo.Reader
	page   pdfgo.Reference
	object pdfgo.Reference
}

// pdfStructureObjectBinding 保存尚未提交的注解外观引用
type pdfStructureObjectBinding struct {
	source  pdfStructureObject
	ids     []string
	content []pdfStructureBinding
}

// pdfStructureContent 隔离不同文件、页面及内容流中的标记编号
type pdfStructureContent struct {
	reader *pdfgo.Reader
	page   pdfgo.Reference
	stream *pdfgo.Stream
	mcid   int64
}

// pdfStructureReferences 保存内容引用与已提交图元的索引
type pdfStructureReferences map[pdfStructureContent][]StructureReference

// targets 解析结构树中的内容流引用，不将未绘制内容绑定到其他同号区段
// 入参: ctx 取消上下文, reader 来源阅读器, item 标记内容引用
// 返回: []StructureReference 独立的目标引用, error 来源或取消错误
func (refs pdfStructureReferences) targets(ctx context.Context, reader *pdfgo.Reader, item pdfgo.StructureItem) ([]StructureReference, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if item.Type != "MCR" || item.MCID < 0 || item.Page == (pdfgo.Reference{}) {
		return nil, fmt.Errorf("invalid PDF marked content reference")
	}
	key := pdfStructureContent{reader: reader, page: item.Page, mcid: item.MCID}
	if item.Stream != (pdfgo.Reference{}) {
		value, err := reader.Resolve(item.Stream)
		if err != nil {
			return nil, err
		}
		var ok bool
		key.stream, ok = value.(*pdfgo.Stream)
		if !ok {
			return nil, fmt.Errorf("invalid PDF marked content stream")
		}
	}
	var result []StructureReference
	for _, ref := range refs[key] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		copy := StructureReference{PageID: ref.PageID}
		for _, id := range ref.ObjectIDs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			copy.ObjectIDs = append(copy.ObjectIDs, id)
		}
		result = append(result, copy)
	}
	return result, ctx.Err()
}

// pdfStructureReferenceSet 按首次出现顺序归并页内图元，单页时不建立页索引
type pdfStructureReferenceSet struct {
	references []StructureReference
	pages      map[string]int
	seen       map[[2]string]bool
}

// append 归并引用并去重，不持有输入切片
// 入参: ctx 取消上下文, sources 来源引用
// 返回: error 取消错误
func (s *pdfStructureReferenceSet) append(ctx context.Context, sources []StructureReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		index := -1
		if last := len(s.references) - 1; last >= 0 && s.references[last].PageID == source.PageID {
			index = last
		} else if page, ok := s.pages[source.PageID]; ok {
			index = page
		}
		for _, id := range source.ObjectIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := [2]string{source.PageID, id}
			if s.seen[key] {
				continue
			}
			if s.seen == nil {
				s.seen = make(map[[2]string]bool)
			}
			s.seen[key] = true
			if index < 0 {
				index = len(s.references)
				if index == 1 {
					s.pages = map[string]int{s.references[0].PageID: 0}
				}
				if s.pages != nil {
					s.pages[source.PageID] = index
				}
				s.references = append(s.references, StructureReference{PageID: source.PageID})
			}
			s.references[index].ObjectIDs = append(s.references[index].ObjectIDs, id)
		}
	}
	return ctx.Err()
}

// pdfStructureBinding 保存一个来源区段对应的连续对象位置
type pdfStructureBinding struct {
	sequence *pdfgo.ContentSequence
	start    int
	end      int
}

// pdfStructureValues 保存属性值类型及间接对象共享关系
type pdfStructureValues struct {
	ctx       context.Context
	reader    *pdfgo.Reader
	seen      map[pdfgo.Reference]bool
	elements  map[pdfgo.Reference]bool
	objects   []StructureNode
	auxiliary bool
}

// extensions 保留未投影字段，字段名不作为XML元素名称
// 入参: dictionary 来源字典, known 已投影字段
// 返回: []StructureNode 扩展字段, error 读取或取消错误
func (v *pdfStructureValues) extensions(dictionary pdfgo.Dictionary, known []pdfgo.Name) ([]StructureNode, error) {
	var result []StructureNode
	for _, key := range slices.Sorted(maps.Keys(dictionary)) {
		if err := v.ctx.Err(); err != nil {
			return nil, err
		}
		if slices.Contains(known, key) {
			continue
		}
		value, err := v.encode(dictionary[key], 0)
		if err != nil {
			return nil, err
		}
		result = append(result, StructureNode{Name: "Extension", Namespace: pdfStructureNamespace, Attributes: pdfStructureAttribute("Key", string(key)), Children: []StructureNode{value}})
	}
	return result, v.ctx.Err()
}

// encode 按原始类型保存属性对象，间接对象仅展开一次，字符串保留原始字节
// 入参: object 属性对象, depth 当前值层级
// 返回: StructureNode 类型化属性值, error 读取、层级或取消错误
func (v *pdfStructureValues) encode(object pdfgo.Object, depth int) (StructureNode, error) {
	node := StructureNode{Namespace: pdfStructureNamespace}
	if err := v.ctx.Err(); err != nil {
		return node, err
	}
	if depth > 128 && !v.auxiliary {
		return node, fmt.Errorf("PDF structure attribute limit exceeded")
	}
	switch value := object.(type) {
	case nil:
		node.Name = "Null"
	case pdfgo.Boolean:
		node.Name, node.Text = "Boolean", strconv.FormatBool(bool(value))
	case pdfgo.Integer:
		node.Name, node.Text = "Integer", strconv.FormatInt(int64(value), 10)
	case pdfgo.Real:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return node, fmt.Errorf("invalid PDF structure real")
		}
		node.Name, node.Text = "Real", strconv.FormatFloat(float64(value), 'g', -1, 64)
	case pdfgo.Name:
		node.Name = "Name"
		node.Attributes = pdfStructureAttribute("Value", string(value))
	case pdfgo.String:
		node.Name, node.Text = "String", base64.StdEncoding.EncodeToString(value)
		node.Attributes = []StructureAttribute{{Name: "Encoding", Value: "base64"}}
	case pdfgo.Reference:
		node.Name = "Reference"
		node.Attributes = []StructureAttribute{{Name: "Number", Value: strconv.FormatInt(value.Number, 10)}, {Name: "Generation", Value: strconv.FormatInt(value.Generation, 10)}}
		if v.elements[value] {
			node.Name = "ElementReference"
			return node, v.ctx.Err()
		}
		if !v.seen[value] {
			v.seen[value] = true
			resolved, err := v.reader.Resolve(value)
			if err != nil {
				if v.auxiliary && pdfAuxiliaryError(err) {
					node.Attributes = append(node.Attributes, pdfStructureAttribute("Error", err.Error())...)
					return node, nil
				}
				return node, err
			}
			if v.auxiliary {
				if dict, ok := resolved.(pdfgo.Dictionary); ok && (dict["Type"] == pdfgo.Name("Page") || dict["Type"] == pdfgo.Name("Pages") || dict["Type"] == pdfgo.Name("Catalog")) {
					return node, nil
				}
			}
			child, err := v.encode(resolved, depth+1)
			if err != nil {
				return node, err
			}
			v.objects = append(v.objects, StructureNode{Name: "Indirect", Namespace: pdfStructureNamespace, Attributes: slices.Clone(node.Attributes), Children: []StructureNode{child}})
		}
	case pdfgo.Array:
		node.Name = "Array"
		for _, object := range value {
			child, err := v.encode(object, depth+1)
			if err != nil {
				return node, err
			}
			node.Children = append(node.Children, child)
		}
	case pdfgo.Dictionary:
		node.Name = "Dictionary"
		for _, key := range slices.Sorted(maps.Keys(value)) {
			child, err := v.encode(value[key], depth+1)
			if err != nil {
				return node, err
			}
			node.Children = append(node.Children, StructureNode{Name: "Entry", Namespace: pdfStructureNamespace, Attributes: pdfStructureAttribute("Key", string(key)), Children: []StructureNode{child}})
		}
	case *pdfgo.Stream:
		node.Name = "Stream"
		dictionary, err := v.encode(value.Dictionary, depth+1)
		if err != nil {
			return node, err
		}
		data := value.Data
		dataName := "RawData"
		if !v.auxiliary {
			dataName = "DecodedData"
			data, err = value.DecodeContext(v.ctx)
		}
		if err != nil {
			return node, err
		}
		node.Children = []StructureNode{dictionary, {Name: dataName, Namespace: pdfStructureNamespace, Attributes: []StructureAttribute{{Name: "Encoding", Value: "base64"}}, Text: base64.StdEncoding.EncodeToString(data)}}
	default:
		return node, fmt.Errorf("invalid PDF structure attribute value")
	}
	return node, v.ctx.Err()
}

// pdfMetadataValue 保留字段是否声明及其来源，空文本不作为缺省值
type pdfMetadataValue struct {
	text string
	xmp  bool
}

// prepareStructure 校验结构内容关联，无法映射时保留原始结构
// 入参: ctx 取消上下文
// 返回: error 读取、严格校验或取消错误
func (p *pdfImporter) prepareStructure(ctx context.Context) error {
	err := p.readStructureBindings(ctx)
	if err == nil {
		return nil
	}
	if p.warning == nil || !pdfAuxiliaryError(err) {
		return err
	}
	object, readErr := p.pdfCatalogObject("StructTreeRoot")
	if readErr != nil {
		return readErr
	}
	if err := p.recoverPDFObject(ctx, "Structure", object, err); err != nil {
		return err
	}
	b := p.structureBindings
	if b.prepared == nil {
		b.prepared = make(map[*pdfgo.Reader]bool)
	}
	if b.invalid == nil {
		b.invalid = make(map[*pdfgo.Reader]bool)
	}
	b.prepared[p.reader], b.invalid[p.reader] = true, true
	return ctx.Err()
}

// readStructureBindings 校验结构内容关联并准备有效图元绑定
// 入参: ctx 取消上下文
// 返回: error 结构、内容容器或取消错误
func (p *pdfImporter) readStructureBindings(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.structureBindings.prepared[p.reader] {
		return nil
	}
	tree, err := p.reader.StructureTree()
	if err != nil {
		return err
	}
	if tree == nil {
		if p.structureBindings.prepared == nil {
			p.structureBindings.prepared = make(map[*pdfgo.Reader]bool)
		}
		p.structureBindings.prepared[p.reader] = true
		return ctx.Err()
	}
	parents, err := p.structureParents(ctx)
	if err != nil {
		return err
	}
	marked := make(map[pdfStructureContent]bool)
	if err := p.reader.WalkStructure(ctx, func(_ int, item pdfgo.StructureItem) error {
		if item.Type != "MCR" && item.Type != "OBJR" {
			return nil
		}
		content, err := p.reader.StructureContent(item)
		if err != nil {
			return err
		}
		mcid := int64(-1)
		if item.Type == "MCR" {
			mcid = item.MCID
			marked[pdfStructureContent{reader: p.reader, page: item.Page, stream: content.Stream, mcid: item.MCID}] = true
		}
		if parent, ok := parents[[2]int64{content.ParentKey, mcid}]; !ok || parent != item.Parent {
			return fmt.Errorf("PDF structure content parent mismatch")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b := p.structureBindings
	if b.parents == nil {
		b.parents = make(map[*pdfgo.Reader]map[[2]int64]pdfgo.Reference)
	}
	if b.prepared == nil {
		b.prepared = make(map[*pdfgo.Reader]bool)
	}
	if b.marked == nil {
		b.marked = make(map[pdfStructureContent]bool)
	}
	b.parents[p.reader], b.prepared[p.reader] = parents, true
	maps.Copy(b.marked, marked)
	b.readers = append(b.readers, p.reader)
	return nil
}

// prepareSourceStructure 使用来源阅读器校验引用文档，共享最终图元索引
// 入参: ctx 取消上下文, reader 来源阅读器
// 返回: error 结构或取消错误
func (p *pdfImporter) prepareSourceStructure(ctx context.Context, reader *pdfgo.Reader) error {
	if p.structureBindings.prepared[reader] {
		return ctx.Err()
	}
	source := *p
	source.reader = reader
	return source.prepareStructure(ctx)
}

// structureParents 复用预检父树索引，未预检时读取当前文档
// 入参: ctx 取消上下文
// 返回: map[[2]int64]pdfgo.Reference 父树键及区段编号对应的元素, error 解析或取消错误
func (p *pdfImporter) structureParents(ctx context.Context) (map[[2]int64]pdfgo.Reference, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if parents := p.structureBindings.parents[p.reader]; parents != nil {
		return parents, nil
	}
	parents := make(map[[2]int64]pdfgo.Reference)
	if err := p.reader.WalkStructureParents(ctx, func(parent pdfgo.StructureParent) error {
		parents[[2]int64{parent.Key, parent.MCID}] = parent.Element
		return nil
	}); err != nil {
		return nil, err
	}
	return parents, nil
}

// documentStructures 分别保存主文档与引用文档的结构，不合并来源对象编号
// 入参: ctx 取消上下文
// 返回: error 结构或提交错误
func (p *pdfImporter) documentStructures(ctx context.Context) error {
	for _, sequence := range p.structureBindings.order {
		if err := p.prepareSourceStructure(ctx, sequence.Reader); err != nil {
			return err
		}
	}
	for _, reader := range p.structureBindings.readers {
		source := *p
		source.reader = reader
		if err := source.documentStructure(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// documentStructure 写出逻辑结构及已提交的图元引用，异常结构以类型化标引保留
// 入参: ctx 取消上下文
// 返回: error 解析、未支持的结构属性或提交错误
func (p *pdfImporter) documentStructure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.structureBindings.invalid[p.reader] {
		return ctx.Err()
	}
	err := p.projectStructure(ctx)
	if err == nil {
		return nil
	}
	if p.warning == nil || !pdfAuxiliaryError(err) {
		return err
	}
	object, readErr := p.pdfCatalogObject("StructTreeRoot")
	if readErr != nil {
		return readErr
	}
	return p.recoverPDFObject(ctx, "Structure", object, err)
}

// projectStructure 将有效逻辑结构投影为OFD层级及实际图元引用
// 入参: ctx 取消上下文
// 返回: error 结构、绑定或提交错误
func (p *pdfImporter) projectStructure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tree, err := p.reader.StructureTree()
	if err != nil || tree == nil {
		return err
	}
	refs, err := p.structureBindings.contentReferences(ctx)
	if err != nil {
		return err
	}
	objects, err := p.structureBindings.objectReferences(ctx)
	if err != nil {
		return err
	}
	parents, err := p.structureParents(ctx)
	if err != nil {
		return err
	}
	root := StructureNode{Name: "Structure", Namespace: pdfStructureNamespace}
	values := pdfStructureValues{ctx: ctx, reader: p.reader, seen: make(map[pdfgo.Reference]bool), elements: make(map[pdfgo.Reference]bool)}
	if tree.Reference != (pdfgo.Reference{}) {
		values.elements[tree.Reference] = true
		root.Attributes = pdfStructureSource(tree.Reference)
	}
	if err := p.reader.WalkStructure(ctx, func(_ int, item pdfgo.StructureItem) error {
		if item.Type == "StructElem" && item.Reference != (pdfgo.Reference{}) {
			values.elements[item.Reference] = true
		}
		return nil
	}); err != nil {
		return err
	}
	properties, err := p.structureBindings.contentProperties(&values)
	if err != nil {
		return err
	}
	for _, key := range []pdfgo.Name{"RoleMap", "ClassMap"} {
		if value := tree.Dictionary[key]; value != nil {
			node, err := values.encode(value, 0)
			if err != nil {
				return err
			}
			root.Children = append(root.Children, StructureNode{Name: string(key), Namespace: pdfStructureNamespace, Children: []StructureNode{node}})
		}
	}
	extensions, err := values.extensions(tree.Dictionary, []pdfgo.Name{"Type", "K", "ParentTree", "ParentTreeNextKey", "RoleMap", "ClassMap", "IDTree"})
	if err != nil {
		return err
	}
	root.Children = append(root.Children, extensions...)
	stack := []*StructureNode{&root}
	ids := make(map[string]pdfgo.Reference)
	err = p.reader.WalkStructure(ctx, func(depth int, item pdfgo.StructureItem) error {
		if depth >= len(stack) {
			return fmt.Errorf("invalid PDF structure depth")
		}
		stack = stack[:depth+1]
		parent := stack[depth]
		node := StructureNode{Name: string(item.Type), Namespace: pdfStructureNamespace}
		var content pdfgo.StructureContent
		if item.Type == "MCR" || item.Type == "OBJR" {
			content, err = p.reader.StructureContent(item)
			if err != nil {
				return err
			}
			mcid := int64(-1)
			if item.Type == "MCR" {
				mcid = item.MCID
			}
			if parent, ok := parents[[2]int64{content.ParentKey, mcid}]; !ok || parent != item.Parent {
				return fmt.Errorf("PDF structure content parent mismatch")
			}
		}
		switch item.Type {
		case "StructElem":
			extensions, err := values.extensions(item.Dictionary, []pdfgo.Name{"Type", "S", "P", "K", "Pg", "R", "ID", "T", "Lang", "Alt", "ActualText", "E", "A", "C"})
			if err != nil {
				return err
			}
			node.Children = append(node.Children, extensions...)
			if item.Reference != (pdfgo.Reference{}) {
				node.Attributes = pdfStructureSource(item.Reference)
			}
			if _, err := p.reader.StructureAttributes(ctx, item); err != nil {
				return err
			}
			for _, key := range []pdfgo.Name{"A", "C"} {
				if value := item.Dictionary[key]; value != nil {
					child, err := values.encode(value, 0)
					if err != nil {
						return err
					}
					node.Children = append(node.Children, StructureNode{Name: string(key), Namespace: pdfStructureNamespace, Children: []StructureNode{child}})
				}
			}
			node.Attributes = append(node.Attributes, pdfStructureAttribute("Tag", string(item.Tag))...)
			node.Attributes = append(node.Attributes, StructureAttribute{Name: "Revision", Value: strconv.FormatInt(item.Revision, 10)})
			for _, field := range []pdfgo.Name{"ID", "T", "Lang", "Alt", "ActualText", "E"} {
				value, err := p.reader.Resolve(item.Dictionary[field])
				if err != nil {
					return err
				}
				if value == nil {
					continue
				}
				text, ok := value.(pdfgo.String)
				if !ok {
					return fmt.Errorf("invalid PDF structure text: %s", field)
				}
				decoded := string(text)
				if field == "ID" {
					if _, exists := ids[decoded]; exists {
						return fmt.Errorf("duplicate PDF structure identifier")
					}
					ids[decoded] = item.Reference
				} else {
					decoded, err = pdfgo.DecodeTextString(text)
					if err != nil {
						original, err := values.encode(text, 0)
						if err != nil {
							return err
						}
						node.Children = append(node.Children, StructureNode{Name: string(field), Namespace: pdfStructureNamespace, Children: []StructureNode{original}})
						continue
					}
				}
				node.Attributes = append(node.Attributes, pdfStructureAttribute(string(field), decoded)...)
			}
		case "MCR":
			extensions, err := values.extensions(item.Dictionary, []pdfgo.Name{"Type", "Pg", "Stm", "StmOwn", "MCID"})
			if err != nil {
				return err
			}
			node.Children = append(node.Children, extensions...)
			targets, err := refs.targets(ctx, p.reader, item)
			if err != nil {
				return err
			}
			node.Attributes = []StructureAttribute{{Name: "MCID", Value: strconv.FormatInt(item.MCID, 10)}}
			key := pdfStructureContent{reader: p.reader, page: item.Page, stream: content.Stream, mcid: item.MCID}
			if property, ok := properties[key]; ok {
				node.Children = append(node.Children, property)
			}
			for _, target := range targets {
				node.Children = append(node.Children, StructureNode{Name: "ObjectRef", Namespace: pdfStructureNamespace, Attributes: []StructureAttribute{{Name: "PageRef", Value: target.PageID}}, Text: strings.Join(target.ObjectIDs, ",")})
			}
		case "OBJR":
			extensions, err := values.extensions(item.Dictionary, []pdfgo.Name{"Type", "Pg", "Obj"})
			if err != nil {
				return err
			}
			node.Children = append(node.Children, extensions...)
			targets, exists := objects[pdfStructureObject{reader: p.reader, page: item.Page, object: item.Target}]
			if !exists {
				kind, err := p.reader.Resolve(content.Dictionary["Type"])
				if err != nil {
					return err
				}
				subtype, err := p.reader.Resolve(content.Dictionary["Subtype"])
				if err != nil {
					return err
				}
				name, _ := subtype.(pdfgo.Name)
				if content.Stream != nil {
					if kind != nil && kind != pdfgo.Name("XObject") || name != "Form" && name != "Image" {
						return fmt.Errorf("unsupported PDF structure object target")
					}
				} else if kind != nil && kind != pdfgo.Name("Annot") || !(pdfgo.Annotation{Subtype: name}).IsStandard() {
					return fmt.Errorf("unsupported PDF structure object target")
				}
			}
			for _, target := range targets {
				if len(target.ObjectIDs) != 0 {
					node.Children = append(node.Children, StructureNode{Name: "ObjectRef", Namespace: pdfStructureNamespace, Attributes: []StructureAttribute{{Name: "PageRef", Value: target.PageID}}, Text: strings.Join(target.ObjectIDs, ",")})
				}
			}
		default:
			return fmt.Errorf("unsupported PDF structure item: %s", item.Type)
		}
		parent.Children = append(parent.Children, node)
		stack = append(stack, &parent.Children[len(parent.Children)-1])
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	if err := p.reader.WalkStructureIDs(ctx, func(id string, item pdfgo.StructureItem) error {
		ref, exists := ids[id]
		if !exists || ref != item.Reference {
			return fmt.Errorf("PDF structure identifier target is outside the hierarchy")
		}
		delete(ids, id)
		return nil
	}); err != nil {
		return err
	}
	if len(ids) != 0 {
		return fmt.Errorf("missing PDF structure identifier index")
	}
	if len(values.objects) != 0 {
		root.Children = append(root.Children, StructureNode{Name: "SourceObjects", Namespace: pdfStructureNamespace, Children: values.objects})
	}
	data, err := encodeStructureNodeContext(ctx, root)
	if err != nil {
		return err
	}
	_, err = p.editor.addStructureXML(ctx, CustomTag{TypeID: pdfStructureTagType, NameSpace: pdfStructureNamespace}, data)
	return err
}

// pdfStructureSource 保存源结构标识，不与OFD图元标识混用
// 入参: ref 来源引用
// 返回: []StructureAttribute 对象编号及代数
func pdfStructureSource(ref pdfgo.Reference) []StructureAttribute {
	return []StructureAttribute{{Name: "SourceNumber", Value: strconv.FormatInt(ref.Number, 10)}, {Name: "SourceGeneration", Value: strconv.FormatInt(ref.Generation, 10)}}
}

// pdfCatalogObject 获取文档级原始字段，不预先解析字段值
// 入参: name 目录字段名
// 返回: pdfgo.Object 原始字段, error 目录读取错误
func (p *pdfImporter) pdfCatalogObject(name pdfgo.Name) (pdfgo.Object, error) {
	value, err := p.reader.Resolve(p.reader.Trailer["Root"])
	if err != nil {
		return nil, err
	}
	dict, ok := value.(pdfgo.Dictionary)
	if !ok {
		return nil, fmt.Errorf("invalid PDF catalog")
	}
	return dict[name], nil
}

// pdfAuxiliaryError 区分辅助格式错误与必须传递的输入、关闭和取消错误
// 入参: err 待检查错误
// 返回: bool 是否可以保留原文后继续
func pdfAuxiliaryError(err error) bool {
	var read *pdfgo.ReadError
	return err != nil && !errors.As(err, &read) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, os.ErrClosed) && !errors.Is(err, errPDFMediaRead)
}

// preservePDFObject 保存辅助对象的原始类型、引用和流数据，不展开页面及目录
// 入参: ctx 取消上下文, name 来源字段, object 原始对象
// 返回: error 读取、编码或提交错误
func (p *pdfImporter) preservePDFObject(ctx context.Context, name string, object pdfgo.Object) error {
	values := pdfStructureValues{ctx: ctx, reader: p.reader, seen: make(map[pdfgo.Reference]bool), auxiliary: true}
	node, err := values.encode(object, 0)
	if err != nil {
		return err
	}
	root := StructureNode{Name: "Auxiliary", Namespace: pdfStructureNamespace, Attributes: pdfStructureAttribute("Name", name), Children: []StructureNode{node}}
	if len(values.objects) != 0 {
		root.Children = append(root.Children, StructureNode{Name: "Objects", Namespace: pdfStructureNamespace, Children: values.objects})
	}
	data, err := encodeStructureNode(root)
	if err != nil {
		return err
	}
	_, err = p.editor.addStructureXML(ctx, CustomTag{TypeID: pdfAuxiliaryTagType, NameSpace: pdfStructureNamespace}, data)
	return err
}

// recoverPDFObject 按导入策略诊断并保留辅助对象，读取和取消错误不恢复
// 入参: ctx 取消上下文, name 来源字段, object 原始对象, cause 辅助错误
// 返回: error 严格模式、读取、取消或提交错误
func (p *pdfImporter) recoverPDFObject(ctx context.Context, name string, object pdfgo.Object, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.warning == nil || !pdfAuxiliaryError(cause) {
		return cause
	}
	if err := p.preservePDFObject(ctx, name, object); err != nil {
		return err
	}
	p.warning(pdfgo.Diagnostic{Message: "PDF " + name + " retained as metadata: " + cause.Error()})
	return nil
}

// pdfStructureAttribute 保留XML不可表达的源字段，不替换原始字符
// 入参: name 属性名, value 属性值
// 返回: []StructureAttribute 属性及可选的编码声明
func pdfStructureAttribute(name, value string) []StructureAttribute {
	if structureXMLText(value) {
		return []StructureAttribute{{Name: name, Value: value}}
	}
	return []StructureAttribute{{Name: name, Value: base64.StdEncoding.EncodeToString([]byte(value))}, {Name: name + "Encoding", Value: "base64"}}
}

// pdfStructureContentKey 解析来源区段中的MCID，空值表示非结构标记
// 入参: sequence 来源区段
// 返回: pdfStructureContent 来源键, bool 是否具有MCID, error 属性或来源错误
func pdfStructureContentKey(sequence *pdfgo.ContentSequence) (pdfStructureContent, bool, error) {
	var key pdfStructureContent
	value := sequence.Properties["MCID"]
	if value == nil {
		return key, false, nil
	}
	if sequence.Reader == nil || sequence.Page == nil {
		return key, false, fmt.Errorf("missing PDF marked content source")
	}
	value, err := sequence.Reader.Resolve(value)
	if err != nil || value == nil {
		return key, false, err
	}
	mcid, ok := value.(pdfgo.Integer)
	if !ok || mcid < 0 {
		return key, false, fmt.Errorf("invalid PDF marked content ID")
	}
	return pdfStructureContent{reader: sequence.Reader, page: sequence.Page.Reference, stream: sequence.Stream, mcid: int64(mcid)}, true, nil
}

// pdfStructurePropertiesEqual 比较类型化属性树，不生成临时XML
// 入参: ctx 取消上下文, left 原属性, right 待比较属性
// 返回: bool 属性是否一致, error 取消错误
func pdfStructurePropertiesEqual(ctx context.Context, left, right StructureNode) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if left.Name != right.Name || left.Namespace != right.Namespace || left.Text != right.Text || !slices.Equal(left.Attributes, right.Attributes) || len(left.Children) != len(right.Children) {
		return false, nil
	}
	for i := range left.Children {
		equal, err := pdfStructurePropertiesEqual(ctx, left.Children[i], right.Children[i])
		if err != nil || !equal {
			return equal, err
		}
	}
	return true, nil
}

// structureBindings 在提交前收集直接来源及含MCID的外层区段
// 入参: ctx 取消上下文
// 返回: []pdfStructureBinding 绑定位置, error 取消错误
func (buffer *pdfObjectBuffer) structureBindings(ctx context.Context) ([]pdfStructureBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if buffer == nil {
		return nil, nil
	}
	var result []pdfStructureBinding
	for index, run := range buffer.sequences {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := buffer.len()
		if index+1 < len(buffer.sequences) {
			end = buffer.sequences[index+1].start
		}
		var sources pdfOriginSet
		collect := func(sequence *pdfgo.ContentSequence) error {
			for current := sequence; current != nil; current = current.Parent {
				if err := ctx.Err(); err != nil {
					return err
				}
				if current == sequence || current.Properties["MCID"] != nil || current.Object != (pdfgo.Reference{}) {
					sources.add(current)
				}
			}
			return nil
		}
		if err := collect(run.sequence); err != nil {
			return nil, err
		}
		for _, sequence := range run.related {
			if err := collect(sequence); err != nil {
				return nil, err
			}
		}
		if !sources.started {
			continue
		}
		result = append(result, pdfStructureBinding{sequence: sources.origin.sequence, start: run.start, end: end})
		for _, sequence := range sources.origin.related {
			result = append(result, pdfStructureBinding{sequence: sequence, start: run.start, end: end})
		}
	}
	return result, ctx.Err()
}

// documentMetadata 保留PDF元数据原文并映射文档描述，来源软件与扩展字段保存在独立标引中
// 入参: ctx 取消上下文, strict 是否严格校验
// 返回: error 读取、解析、日期或提交错误
func (p *pdfImporter) documentMetadata(ctx context.Context, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := p.reader.Info()
	if err != nil {
		return err
	}
	for _, field := range []struct {
		name string
		text *string
	}{{"CreationDate", &info.CreationDate}, {"ModDate", &info.ModDate}} {
		text, err := codecpdf.NormalizeDate(*field.text, strict)
		if err != nil {
			if strict || !pdfAuxiliaryError(err) {
				return fmt.Errorf("PDF metadata %s: %w", field.name, err)
			}
			p.warning(pdfgo.Diagnostic{Message: "PDF metadata " + field.name + " retained without date projection: " + err.Error()})
			*field.text = ""
			continue
		}
		if text != *field.text {
			p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Message: "PDF metadata " + field.name + ": date normalized"})
			*field.text = text
		}
	}
	values, original, err := p.informationMetadata(ctx, info)
	if err != nil {
		return err
	}
	stream, err := p.reader.Metadata()
	if err != nil {
		object, readErr := p.pdfCatalogObject("Metadata")
		if readErr != nil {
			return readErr
		}
		if err := p.recoverPDFObject(ctx, "Metadata", object, err); err != nil {
			return err
		}
		stream = nil
	}
	var packet []byte
	var root *StructureNode
	if stream != nil {
		packet, err = stream.DecodeContext(ctx)
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Metadata", stream, err); err != nil {
				return err
			}
		} else {
			root, err = parseStructure(ctx, packet, CustomTag{})
			if err != nil {
				if err := p.recoverPDFObject(ctx, "Metadata", stream, err); err != nil {
					return err
				}
				root = nil
			}
		}
	}
	if root != nil {
		xmp, err := pdfMetadataFields(ctx, root)
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Metadata", stream, err); err != nil {
				return err
			}
			xmp = nil
		}
		for _, key := range []string{"CreationDate", "ModDate", "MetadataDate"} {
			if value, ok := xmp[key]; ok {
				if _, _, _, err := codecpdf.MetadataDate(value.text, true); err != nil {
					if strict || !pdfAuxiliaryError(err) {
						return fmt.Errorf("PDF metadata %s: %w", key, err)
					}
					p.warning(pdfgo.Diagnostic{Message: "PDF metadata " + key + " retained without date projection: " + err.Error()})
					delete(xmp, key)
				}
			}
		}
		newer := false
		if stamp, ok := xmp["MetadataDate"]; ok && stamp.text != "" && info.ModDate != "" {
			date, _, zoned, err := codecpdf.MetadataDate(stamp.text, stamp.xmp)
			if err != nil {
				return err
			}
			modified, err := pdfgo.ParseDate(info.ModDate)
			if err != nil {
				return err
			}
			newer = zoned && modified.After(date)
		}
		for key, value := range xmp {
			if _, exists := values[key]; !newer || !exists {
				values[key] = value
			}
		}
	}
	projected := p.editor.Info
	for _, field := range []struct {
		name string
		text *string
	}{{"Title", &projected.Title}, {"Author", &projected.Author}, {"Subject", &projected.Subject}} {
		if value, ok := values[field.name]; ok && structureXMLText(value.text) {
			*field.text = value.text
		}
	}
	if value, ok := values["Keywords"]; ok && structureXMLText(value.text) {
		projected.Keywords = Keywords{value.text}
	}
	for _, field := range []struct {
		name string
		text *string
	}{{"CreationDate", &projected.CreationDate}, {"ModDate", &projected.ModDate}} {
		*field.text = ""
		if value, ok := values[field.name]; ok {
			date, day, _, err := codecpdf.MetadataDate(value.text, value.xmp)
			if err != nil {
				return fmt.Errorf("PDF metadata %s: %w", field.name, err)
			}
			if day {
				*field.text = date.Format("2006-01-02")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.editor.SetInfo(projected)
	if root != nil {
		if _, err := p.editor.addStructureXML(ctx, CustomTag{TypeID: pdfMetadataTagType, NameSpace: root.Namespace}, packet); err != nil {
			return err
		}
	}
	if len(original.Children) != 0 {
		data, err := encodeStructureNode(original)
		if err != nil {
			return err
		}
		if _, err = p.editor.addStructureXML(ctx, CustomTag{TypeID: pdfInformationTagType, NameSpace: pdfInformationNamespace}, data); err != nil {
			return err
		}
	}
	if !strict {
		object, err := p.pdfCatalogObject("OutputIntents")
		if err != nil {
			return err
		}
		if object != nil {
			return p.preservePDFObject(ctx, "OutputIntents", object)
		}
	}
	return ctx.Err()
}

// informationMetadata 收集已声明字段，日期分别保留原文及投影值，XML无法表示的文本使用Base64保留
// 入参: ctx 取消上下文, info 已解码的信息字典
// 返回: map[string]pdfMetadataValue 可映射字段, StructureNode 原始字段, error 读取或取消错误
func (p *pdfImporter) informationMetadata(ctx context.Context, info pdfgo.DocumentInfo) (map[string]pdfMetadataValue, StructureNode, error) {
	root := StructureNode{Name: "Information", Namespace: pdfInformationNamespace}
	values := make(map[string]pdfMetadataValue)
	object, err := p.reader.Resolve(p.reader.Trailer["Info"])
	if err != nil {
		return values, root, p.recoverPDFObject(ctx, "Information", p.reader.Trailer["Info"], err)
	}
	if object == nil {
		return values, root, nil
	}
	dict, ok := object.(pdfgo.Dictionary)
	if !ok {
		return values, root, p.recoverPDFObject(ctx, "Information", object, fmt.Errorf("invalid PDF information dictionary"))
	}
	known := map[pdfgo.Name]string{
		"Title": info.Title, "Author": info.Author, "Subject": info.Subject, "Keywords": info.Keywords,
		"Creator": info.Creator, "Producer": info.Producer, "CreationDate": info.CreationDate, "ModDate": info.ModDate,
		"Trapped": string(info.Trapped),
	}
	for _, key := range slices.Sorted(maps.Keys(dict)) {
		if err := ctx.Err(); err != nil {
			return nil, root, err
		}
		value, err := p.reader.Resolve(dict[key])
		if err != nil {
			if err := p.recoverPDFObject(ctx, "Information."+string(key), dict[key], err); err != nil {
				return nil, root, err
			}
			continue
		}
		if value == nil {
			continue
		}
		if text, ok := value.(pdfgo.String); ok {
			if _, err := pdfgo.DecodeTextString(text); err != nil {
				if err := p.recoverPDFObject(ctx, "Information."+string(key), dict[key], err); err != nil {
					return nil, root, err
				}
				if key == "CreationDate" || key == "ModDate" {
					values[string(key)] = pdfMetadataValue{}
				}
				continue
			}
		}
		text, standard := known[key]
		_, stringValue := value.(pdfgo.String)
		valid := stringValue
		if key == "Trapped" {
			valid = value == pdfgo.Name("True") || value == pdfgo.Name("False") || value == pdfgo.Name("Unknown")
		}
		if !valid {
			if err := p.preservePDFObject(ctx, "Information."+string(key), dict[key]); err != nil {
				return nil, root, err
			}
			if key == "CreationDate" || key == "ModDate" {
				values[string(key)] = pdfMetadataValue{}
			}
			continue
		}
		if !standard {
			text = info.Custom[key]
		} else {
			values[string(key)] = pdfMetadataValue{text: text}
		}
		name := string(key)
		field := StructureNode{Name: "Field", Namespace: pdfInformationNamespace}
		field.Attributes = append(field.Attributes, StructureAttribute{Name: "Name", Value: name})
		if !structureXMLText(name) {
			field.Attributes[0].Value = base64.StdEncoding.EncodeToString([]byte(name))
			field.Attributes = append(field.Attributes, StructureAttribute{Name: "NameEncoding", Value: "base64"})
		}
		if key == "Trapped" {
			field.Attributes = append(field.Attributes, StructureAttribute{Name: "Type", Value: "name"})
		}
		if !structureXMLText(text) {
			text = base64.StdEncoding.EncodeToString([]byte(text))
			field.Attributes = append(field.Attributes, StructureAttribute{Name: "Encoding", Value: "base64"})
		}
		if key == "CreationDate" || key == "ModDate" {
			original, err := pdfgo.DecodeTextString(value.(pdfgo.String))
			if err != nil {
				if err := p.recoverPDFObject(ctx, "Information."+string(key), dict[key], err); err != nil {
					return nil, root, err
				}
				continue
			}
			text = original
			if !structureXMLText(text) {
				text = base64.StdEncoding.EncodeToString([]byte(text))
				field.Attributes = append(field.Attributes, StructureAttribute{Name: "Encoding", Value: "base64"})
			}
		}
		field.Text = text
		root.Children = append(root.Children, field)
	}
	return values, root, nil
}

// pdfMetadataFields 读取文档级RDF描述，日期及PDF关键词不与其他同名扩展混用
// 入参: ctx 取消上下文, root 元数据根节点
// 返回: map[string]pdfMetadataValue 已声明的对应字段, error 取消错误
func pdfMetadataFields(ctx context.Context, root *StructureNode) (map[string]pdfMetadataValue, error) {
	result := make(map[string]pdfMetadataValue)
	var rdf *StructureNode
	if root.Namespace == pdfMetadataRDF && root.Name == "RDF" {
		rdf = root
	} else if root.Namespace == pdfMetadataXML && (root.Name == "xmpmeta" || root.Name == "xapmeta") {
		for i := range root.Children {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			child := &root.Children[i]
			if child.Namespace == pdfMetadataRDF && child.Name == "RDF" {
				rdf = child
				break
			}
		}
	}
	if rdf == nil {
		return result, ctx.Err()
	}
	fields := map[xml.Name]string{
		{Space: pdfMetadataDC, Local: "title"}: "Title", {Space: pdfMetadataDC, Local: "creator"}: "Author",
		{Space: pdfMetadataDC, Local: "description"}: "Subject", {Space: pdfMetadataPDF, Local: "Keywords"}: "Keywords",
		{Space: pdfMetadataXMP, Local: "CreateDate"}: "CreationDate", {Space: pdfMetadataXMP, Local: "ModifyDate"}: "ModDate",
		{Space: pdfMetadataXMP, Local: "MetadataDate"}: "MetadataDate",
	}
	for _, description := range rdf.Children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if description.Namespace != pdfMetadataRDF || description.Name != "Description" {
			continue
		}
		for _, attr := range description.Attributes {
			if key, ok := fields[xml.Name{Space: attr.Namespace, Local: attr.Name}]; ok && attr.Namespace != pdfMetadataDC {
				result[key] = pdfMetadataValue{text: attr.Value, xmp: true}
			}
		}
		for _, property := range description.Children {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key, ok := fields[xml.Name{Space: property.Namespace, Local: property.Name}]
			if !ok {
				continue
			}
			arrayValue := property.Namespace == pdfMetadataDC
			property = pdfMetadataProperty(property)
			if arrayValue {
				kind := "Alt"
				if key == "Author" {
					kind = "Seq"
				}
				found := false
				for _, array := range property.Children {
					if array.Namespace == pdfMetadataRDF && array.Name == kind && len(array.Children) != 0 {
						item := array.Children[0]
						if item.Namespace == pdfMetadataRDF && item.Name == "li" {
							property = item
							found = true
						}
						break
					}
				}
				if !found {
					continue
				}
			}
			if text, ok := pdfMetadataText(property); ok {
				result[key] = pdfMetadataValue{text: text, xmp: true}
			}
		}
	}
	return result, ctx.Err()
}

// pdfMetadataProperty 提取带限定符的rdf:value，保留数组及结构边界
// 入参: node 属性节点
// 返回: StructureNode 属性值节点
func pdfMetadataProperty(node StructureNode) StructureNode {
	for {
		if len(node.Children) == 1 && node.Children[0].Namespace == pdfMetadataRDF && node.Children[0].Name == "Description" {
			node = node.Children[0]
			continue
		}
		for _, attr := range node.Attributes {
			if attr.Namespace == pdfMetadataRDF && attr.Name == "value" {
				return StructureNode{Text: attr.Value}
			}
		}
		found := false
		for _, child := range node.Children {
			if child.Namespace == pdfMetadataRDF && child.Name == "value" {
				node, found = child, true
				break
			}
		}
		if !found {
			return node
		}
	}
}

// pdfMetadataText 读取简单值，不拼接结构或数组文本，也不将资源URI当作文本
// 入参: node 属性节点
// 返回: string 文本, bool 是否为支持的简单值
func pdfMetadataText(node StructureNode) (string, bool) {
	node = pdfMetadataProperty(node)
	for _, attr := range node.Attributes {
		if attr.Namespace == pdfMetadataRDF && (attr.Name == "resource" || attr.Name == "nodeID" || attr.Name == "parseType") {
			return "", false
		}
	}
	return node.Text, len(node.Children) == 0
}
