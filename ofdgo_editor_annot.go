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
	"encoding/xml"
	"fmt"
	"maps"
	"math"
	"path"
	"strings"
	"time"
)

// AnnotationSelection 保存当前编辑器内可重复粘贴的注解原文及资源上下文
// 来源修改或删除不影响快照，不用于跨文档传输
type AnnotationSelection struct {
	editor    *Editor
	document  editorDocumentIdentity
	data      [][]byte
	pageScope []*parsedRes
	states    map[string]map[string]editorCompositeState
	richText  map[int]*richTextSnapshot
}

// ImageStampOptions 描述标准图片图章注解的构造参数
// Box为页面毫米区域，Angle为顺时针旋转角度，Alpha为nil时使用不透明默认值
type ImageStampOptions struct {
	ResourceID string
	Box        Box
	Angle      float64
	Alpha      *int
	Creator    string
	Remark     string
}

// NewImageStampAnnotation 构造标准图片图章注解，统一计算外观Boundary、居中旋转CTM及透明度
// 入参: options 图片图章参数
// 返回: Annotation 可直接提交的图章注解, error 参数校验错误
func NewImageStampAnnotation(options ImageStampOptions) (Annotation, error) {
	if strings.TrimSpace(options.ResourceID) == "" {
		return Annotation{}, fmt.Errorf("stamp requires an image resource ID")
	}
	if err := validateCreationBox(options.Box); err != nil {
		return Annotation{}, err
	}
	if !finite(options.Angle) {
		return Annotation{}, fmt.Errorf("stamp angle must be finite")
	}
	var alpha *int
	if options.Alpha != nil {
		if *options.Alpha < 0 || *options.Alpha > 255 {
			return Annotation{}, fmt.Errorf("alpha must be between 0 and 255")
		}
		value := *options.Alpha
		alpha = &value
	}
	ctm := Matrix{a: options.Box.W, d: options.Box.H}
	if options.Angle != 0 {
		rad := options.Angle * math.Pi / 180
		cos, sin := math.Cos(rad), math.Sin(rad)
		rot := Matrix{a: cos, b: sin, c: -sin, d: cos}
		ctm = TranslationMatrix(options.Box.W/2, options.Box.H/2).Multiply(rot).Multiply(TranslationMatrix(-options.Box.W/2, -options.Box.H/2)).Multiply(ctm)
	}
	object := GraphicObject{
		Type: "ImageObject",
		ImageObject: ImageObject{
			ResourceID: options.ResourceID,
			Boundary:   editorBoxString(Box{W: options.Box.W, H: options.Box.H}),
			CTM:        ctm.String(),
			Alpha:      alpha,
		},
	}
	return Annotation{
		Type:    "Stamp",
		Creator: options.Creator,
		Remark:  options.Remark,
		Appearance: &Appearance{
			Boundary: editorBoxString(options.Box),
			Objects:  []GraphicObject{object},
		},
	}, nil
}

// editorAnnotationDate 更新注解修改日期，采用标准xs:date格式
// 入参: data 注解XML片段
// 返回: []byte 新片段, error 错误信息
func editorAnnotationDate(data []byte) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	return editorXMLAttribute(data, root, "LastModDate", time.Now().Format("2006-01-02"))
}

// AddAnnotations 批量向指定页面新增标准注解并单次提交，支持多页共用单个注解或按页一一对应
// 同一页面可重复出现，返回的新注解标识与pages一一对应，一次批量操作计入一条撤销记录
// 入参: pages 目标页面索引列表, annotations 单个或与页码等长的注解列表
// 返回: []string 新注解标识列表, error 错误信息
func (e *Editor) AddAnnotations(pages []int, annotations []Annotation) ([]string, error) {
	return e.AddAnnotationsContext(context.Background(), pages, annotations)
}

// AddAnnotationsContext 批量向指定页面新增标准注解并单次提交，准备和提交阶段响应取消
// 入参: ctx 取消上下文, pages 目标页面索引列表, annotations 单个或与页码等长的注解列表
// 返回: []string 新注解标识列表, error 参数、校验或取消错误
func (e *Editor) AddAnnotationsContext(ctx context.Context, pages []int, annotations []Annotation) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		if len(annotations) <= 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("annotation count %d does not match page count %d", len(annotations), len(pages))
	}
	if len(annotations) != 1 && len(annotations) != len(pages) {
		return nil, fmt.Errorf("annotation count %d does not match page count %d", len(annotations), len(pages))
	}
	for _, page := range pages {
		if page < 0 || page >= len(e.pages) {
			return nil, fmt.Errorf("page index %d out of range", page)
		}
	}
	var ids []string
	err := e.Transaction(func(edit *Editor) error {
		if err := edit.prepareSourceIDsContext(ctx); err != nil {
			return err
		}
		ids = make([]string, len(pages))
		uniquePages := make([]int, 0, len(pages))
		grouped := make([][]byte, 0, len(pages))
		positions := make(map[int]int, len(pages))
		for i, page := range pages {
			if err := ctx.Err(); err != nil {
				return err
			}
			annotation := annotations[0]
			if len(annotations) > 1 {
				annotation = annotations[i]
			}
			id, data, err := edit.prepareAnnotation(page, annotation)
			if err != nil {
				return err
			}
			ids[i] = id
			fragment := bytes.TrimPrefix(data, []byte(xml.Header))
			if pos, ok := positions[page]; ok {
				grouped[pos] = append(grouped[pos], fragment...)
			} else {
				positions[page] = len(uniquePages)
				uniquePages = append(uniquePages, page)
				grouped = append(grouped, bytes.Clone(fragment))
			}
		}
		return edit.appendPageAnnotations(ctx, uniquePages, grouped, true)
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// Annotations 获取指定页面的注解信息，包含不可见注解，返回值与编辑器相互独立
// 入参: index 页面索引
// 返回: []AnnotationInfo 注解信息, error 错误信息
func (e *Editor) Annotations(index int) ([]AnnotationInfo, error) {
	if index < 0 || index >= len(e.pages) {
		return nil, fmt.Errorf("page index %d out of range", index)
	}
	if e.source == nil {
		return nil, nil
	}
	if _, err := e.source.reader.docInternal(); err != nil {
		return nil, err
	}
	return e.source.reader.annotationInfos(index, e.pages[index].ID), nil
}

// UpdateAnnotation 修改注解说明和作者，保留外观、动作、参数及扩展字段
// 有效修改更新LastModDate并计入一次撤销记录，不受原文件ReadOnly声明限制
// 入参: index 页面索引, id 注解标识, remark 说明, creator 作者
// 返回: error 错误信息
func (e *Editor) UpdateAnnotation(index int, id, remark, creator string) error {
	return e.editAnnotations(index, []string{id}, func(data []byte, node *editorXML) ([]byte, error) {
		standalone, err := editorXMLStandalone(data[node.start:node.end], node)
		if err != nil {
			return nil, err
		}
		var annotation Annotation
		if err := xml.Unmarshal(standalone, &annotation); err != nil {
			return nil, err
		}
		if annotation.Remark == remark && annotation.Creator == creator {
			return data[node.start:node.end], nil
		}
		data = standalone
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		if annotation.Remark != remark {
			var escaped bytes.Buffer
			if err := xml.EscapeText(&escaped, []byte(remark)); err != nil {
				return nil, err
			}
			if child := root.child("Remark"); child != nil {
				data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, child, escaped.Bytes())})
			} else if root.open == root.end {
				data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, root.textXML("Remark", remark))})
			} else {
				data = editorPatchXML(data, []editorXMLPatch{{root.open, root.open, root.textXML("Remark", remark)}})
			}
		}
		root, err = parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		if annotation.Creator != creator {
			data, err = editorXMLAttribute(data, root, "Creator", creator)
			if err != nil {
				return nil, err
			}
		}
		return editorAnnotationDate(data)
	})
}

// MoveAnnotations 平移同页注解，保留内部图元、动作坐标及原始尺寸，一次操作计入一条撤销记录
// 入参: index 页面索引, ids 不重复的注解标识, dx 横向毫米偏移, dy 纵向毫米偏移
// 返回: error 错误信息
func (e *Editor) MoveAnnotations(index int, ids []string, dx, dy float64) error {
	if !finite(dx) || !finite(dy) {
		return fmt.Errorf("annotation offset must be finite")
	}
	return e.editAnnotations(index, ids, func(data []byte, node *editorXML) ([]byte, error) {
		appearance := node.child("Appearance")
		if appearance == nil {
			return nil, fmt.Errorf("annotation %q has no appearance", node.attr("ID"))
		}
		box, err := ParseBox(appearance.attr("Boundary"))
		if err != nil || !finite(box.X+dx) || !finite(box.Y+dy) || !finite(box.W) || !finite(box.H) || box.W < 0 || box.H < 0 {
			return nil, fmt.Errorf("annotation %q has invalid boundary", node.attr("ID"))
		}
		if dx == 0 && dy == 0 {
			return data[node.start:node.end], nil
		}
		data, err = editorXMLStandalone(data[node.start:node.end], node)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		appearance = root.child("Appearance")
		boundary := fmt.Sprintf("%s %s %s %s", ofdNumber(box.X+dx), ofdNumber(box.Y+dy), ofdNumber(box.W), ofdNumber(box.H))
		updated, err := editorXMLAttribute(data, appearance, "Boundary", boundary)
		if err != nil {
			return nil, err
		}
		return editorAnnotationDate(editorPatchXML(data, []editorXMLPatch{{appearance.start, appearance.end, updated}}))
	})
}

// DeleteAnnotations 删除同页注解并清理空的页面注解引用，一次操作计入一条撤销记录
// 入参: index 页面索引, ids 不重复的注解标识
// 返回: error 错误信息
func (e *Editor) DeleteAnnotations(index int, ids []string) error {
	return e.editAnnotations(index, ids, func([]byte, *editorXML) ([]byte, error) { return nil, nil })
}

// CaptureAnnotations 捕获注解选区，保留未知字段、外观及动作
// 入参: page 页面索引, ids 不重复的注解标识
// 返回: *AnnotationSelection 独立快照, error 错误信息
func (e *Editor) CaptureAnnotations(page int, ids []string) (*AnnotationSelection, error) {
	if _, err := e.page(page); err != nil {
		return nil, err
	}
	selection := &AnnotationSelection{editor: e, document: e.documentIdentity(), pageScope: e.pageLocalScope(page), states: make(map[string]map[string]editorCompositeState), richText: make(map[int]*richTextSnapshot)}
	seen := make(map[string]bool)
	for _, id := range ids {
		key := editorResourceID(id)
		if seen[key] {
			return nil, fmt.Errorf("duplicate annotation ID %q", id)
		}
		seen[key] = true
		data, err := e.annotationXML(page, id)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		if !editorXMLCopyable(root) {
			return nil, fmt.Errorf("annotation contains unsupported identifiers")
		}
		annotation, err := e.Annotation(page, id)
		if err != nil {
			return nil, err
		}
		if annotation.Appearance != nil {
			snapshot, err := e.captureRichTextSnapshotObjects(page, annotation.Appearance.Objects)
			if err != nil {
				return nil, err
			}
			if snapshot != nil {
				selection.richText[len(selection.data)] = snapshot
			}
		}
		selection.data = append(selection.data, data)
		selection.states[key] = maps.Clone(e.source.annotationStates[e.pages[page].ID+"/"+key])
	}
	return selection, nil
}

// PasteAnnotations 将注解快照粘贴到指定页并平移，保留动作目标和原有外观
// 全部标识重新分配，局部资源按需克隆为独立文档资源，一次操作计入一条撤销记录
// 入参: page 目标页面索引, selection 当前编辑器快照, dx 横向位移，单位为毫米, dy 纵向位移，单位为毫米
// 返回: []string 新注解标识, error 错误信息
func (e *Editor) PasteAnnotations(page int, selection *AnnotationSelection, dx, dy float64) ([]string, error) {
	if selection == nil || selection.editor != e || !e.documentIdentity().matches(selection.document) || !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("invalid annotation selection or offset")
	}
	var result []string
	err := e.Transaction(func(edit *Editor) error {
		if _, err := edit.page(page); err != nil {
			return err
		}
		if len(selection.data) == 0 {
			return nil
		}
		if err := edit.prepareSourceIDs(); err != nil {
			return err
		}
		dstScope := edit.pageLocalScope(page)
		var content []byte
		var states map[string]map[string]editorCompositeState
		if edit.source != nil {
			states = maps.Clone(edit.source.annotationStates)
		}
		if states == nil {
			states = make(map[string]map[string]editorCompositeState)
		}
		for _, data := range selection.data {
			root, err := parseEditorXML(data)
			if err != nil {
				return err
			}
			ids := make(map[string]string)
			if err := edit.collectCompositeIDs(root, ids); err != nil {
				return err
			}
			id := ids[editorResourceID(root.attr("ID"))]
			states[edit.pages[page].ID+"/"+id] = remapCompositeStates(selection.states[editorResourceID(root.attr("ID"))], ids)
			data, err = editorXMLRemapOnlyIDs(data, root, ids)
			if err != nil {
				return err
			}
			root, err = parseEditorXML(data)
			if err != nil {
				return err
			}
			data, changed, err := edit.remapXMLResources(data, root, selection.pageScope, dstScope, false, make(map[string]bool))
			if err != nil {
				return err
			}
			if changed {
				root, err = parseEditorXML(data)
				if err != nil {
					return err
				}
			}
			appearance := root.child("Appearance")
			if appearance == nil {
				return fmt.Errorf("annotation has no appearance")
			}
			box, err := ParseBox(appearance.attr("Boundary"))
			if err != nil || !finite(box.X+dx) || !finite(box.Y+dy) {
				return fmt.Errorf("invalid annotation boundary")
			}
			box.X, box.Y = box.X+dx, box.Y+dy
			fragment, err := editorXMLAttribute(data, appearance, "Boundary", editorBoxString(box))
			if err != nil {
				return err
			}
			data, err = editorAnnotationDate(editorPatchXML(data, []editorXMLPatch{{appearance.start, appearance.end, fragment}}))
			if err != nil {
				return err
			}
			content = append(content, bytes.TrimPrefix(data, []byte(xml.Header))...)
			result = append(result, id)
		}
		if err := edit.appendAnnotations(page, content, false); err != nil {
			return err
		}
		edit.source.annotationStates = states
		for i, id := range result {
			if snapshot := selection.richText[i]; snapshot != nil {
				annotation, err := edit.Annotation(page, id)
				if err != nil {
					return err
				}
				if annotation.Appearance != nil {
					if err := edit.putRichTextSnapshotObjects(page, annotation.Appearance.Objects, snapshot); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// TransformAnnotations 对注解选区施加页面仿射变换，保持内部图元与外观边界同步
// 入参: page 页面索引, ids 注解标识, matrix 可逆页面变换
// 返回: error 错误信息
func (e *Editor) TransformAnnotations(page int, ids []string, matrix Matrix) error {
	if _, ok := matrix.Invert(); !ok {
		return fmt.Errorf("annotation transform must be finite and invertible")
	}
	return e.Transaction(func(edit *Editor) error {
		seen := make(map[string]bool)
		for _, id := range ids {
			if seen[id] {
				return fmt.Errorf("duplicate annotation ID %q", id)
			}
			seen[id] = true
			annotation, err := edit.Annotation(page, id)
			if err != nil {
				return err
			}
			if matrix == IdentityMatrix() {
				continue
			}
			if annotation.Appearance == nil {
				return fmt.Errorf("annotation has no appearance")
			}
			box, err := ParseBox(annotation.Appearance.Boundary)
			if err != nil {
				return err
			}
			bounds := matrix.TransformBox(box)
			if !finite(bounds.X) || !finite(bounds.Y) || !finite(bounds.W) || !finite(bounds.H) || bounds.W < 0 || bounds.H < 0 {
				return fmt.Errorf("annotation transform exceeds finite range")
			}
			path := ObjectPath{Annotation: id}
			reader, _, _, members, err := edit.compositeScope(page, path)
			if err != nil {
				return err
			}
			reader.Close()
			indexes := make([]int, len(members))
			for i := range indexes {
				indexes[i] = i
			}
			local := TranslationMatrix(box.X-bounds.X, box.Y-bounds.Y).Multiply(matrix)
			if err := edit.changeCompositeObjects(page, path, indexes, func(Box) Matrix { return local }); err != nil {
				return err
			}
			if err := edit.editAnnotations(page, []string{id}, func(data []byte, node *editorXML) ([]byte, error) {
				fragment, err := editorXMLStandalone(data[node.start:node.end], node)
				if err != nil {
					return nil, err
				}
				root, err := parseEditorXML(fragment)
				if err != nil {
					return nil, err
				}
				appearance := root.child("Appearance")
				updated, err := editorXMLAttribute(fragment, appearance, "Boundary", editorBoxString(bounds))
				if err != nil {
					return nil, err
				}
				return editorAnnotationDate(editorPatchXML(fragment, []editorXMLPatch{{appearance.start, appearance.end, updated}}))
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceAnnotationText 修改注解内部匹配的普通文字，保留各成员位置、旋转及未修改属性
// 入参: page 页面索引, id 注解标识, old 原文字, value 新文字, style 文字样式增量
// 返回: int 修改数量, error 错误信息
func (e *Editor) ReplaceAnnotationText(page int, id, old, value string, style TextStyle) (int, error) {
	count := 0
	err := e.Transaction(func(edit *Editor) error {
		return edit.editCompositeScope(page, ObjectPath{Annotation: id}, func(renderer *Renderer, _ *editorCompositeNode, nodes []*editorCompositeNode) error {
			visiting := make(map[string]bool)
			var replace func([]*editorCompositeNode) error
			replace = func(nodes []*editorCompositeNode) error {
				members, err := edit.measureCompositeMembers(renderer, nodes)
				if err != nil {
					return err
				}
				for i, member := range members {
					if member.Object.Type == "TextObject" && member.Object.TextObject.Text() == old {
						if err := edit.updateCompositeText(nodes[i], member, &value, style, nil, nil); err != nil {
							return err
						}
						count++
					} else if member.Object.Type == "CompositeObject" {
						resource := member.Object.CompositeObject.ResourceID
						if id := editorResourceID(resource); id != "" {
							resource = id
						}
						if resource != "" && visiting[resource] {
							return fmt.Errorf("cyclic composite resource %q", resource)
						}
						children, err := edit.compositeMembers(nodes[i], renderer.reader, renderer, visiting)
						if err != nil {
							return err
						}
						visiting[resource] = true
						if err := replace(children); err != nil {
							return err
						}
						delete(visiting, resource)
					}
				}
				return nil
			}
			return replace(nodes)
		})
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// editAnnotations 原子修改页面注解原文，按需分离共用文件并保留输入包与历史快照
// 入参: index 页面索引, ids 注解标识, edit 单个注解修改函数
// 返回: error 错误信息
func (e *Editor) editAnnotations(index int, ids []string, edit func([]byte, *editorXML) ([]byte, error)) error {
	if index < 0 || index >= len(e.pages) {
		return fmt.Errorf("page index %d out of range", index)
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		key := editorResourceID(id)
		if key == "" || wanted[key] {
			return fmt.Errorf("missing or duplicate annotation ID %q", id)
		}
		wanted[key] = true
	}
	if len(wanted) == 0 {
		return nil
	}
	if e.source == nil || e.source.document.Annotations == "" {
		return fmt.Errorf("page has no annotations")
	}
	reader := e.source.reader
	name, err := editorPageLocation(reader, nil, "", "/"+reader.ResPath(e.source.document.Annotations))
	if err != nil {
		return err
	}
	data, err := reader.readFile(name)
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	parts := make(map[string][]byte)
	var patches []editorXMLPatch
	found := make(map[string]bool, len(ids))
	remaining := 0
	pageID := editorResourceID(e.pages[index].ID)
	for _, ref := range root.children {
		if !ref.matchesOFD("Page") {
			continue
		}
		remaining++
		if pageID == "" || editorResourceID(ref.attr("PageID")) != pageID {
			continue
		}
		loc := ref.child("FileLoc")
		if loc == nil {
			return fmt.Errorf("annotation page has no FileLoc")
		}
		file, err := editorPageLocation(reader, nil, name, strings.TrimSpace(editorImportText(data, loc)))
		if err != nil {
			return err
		}
		content, err := reader.readFile(file)
		if err != nil {
			return err
		}
		page, err := parseEditorXML(content)
		if err != nil {
			return err
		}
		var changes []editorXMLPatch
		count := 0
		for _, node := range page.children {
			if !node.matchesOFD("Annot") {
				continue
			}
			count++
			id := editorResourceID(node.attr("ID"))
			if !wanted[id] {
				continue
			}
			if found[id] {
				return fmt.Errorf("ambiguous annotation ID %q", id)
			}
			found[id] = true
			value, err := edit(content, node)
			if err != nil {
				return err
			}
			if value == nil {
				count--
			}
			if !bytes.Equal(value, content[node.start:node.end]) {
				changes = append(changes, editorXMLPatch{node.start, node.end, value})
			}
		}
		if len(changes) == 0 {
			continue
		}
		sharedFile := false
		for _, other := range root.children {
			if other == ref || !other.matchesOFD("Page") || other.child("FileLoc") == nil {
				continue
			}
			shared, err := editorPageLocation(reader, nil, name, strings.TrimSpace(editorImportText(data, other.child("FileLoc"))))
			if err == nil && shared == file {
				sharedFile = true
				break
			}
		}
		if count == 0 {
			patches = append(patches, editorXMLPatch{ref.start, ref.end, nil})
			remaining--
			if !sharedFile {
				parts[file] = editorPatchXML(content, changes)
			}
			continue
		}
		if sharedFile {
			file = packageAvailableName(reader, parts, e.packageName(path.Join("Annotations", "Page_"+e.pages[index].ID+".xml")))
			var escaped bytes.Buffer
			_ = xml.EscapeText(&escaped, []byte("/"+file))
			patches = append(patches, editorXMLContent(data, loc, escaped.Bytes()))
		}
		parts[file] = editorPatchXML(content, changes)
	}
	for _, id := range ids {
		if !found[editorResourceID(id)] {
			return fmt.Errorf("annotation %q not found", id)
		}
	}
	if len(patches) != 0 {
		parts[name] = editorPatchXML(data, patches)
		if remaining == 0 {
			docName, err := editorPageLocation(reader, nil, "", "/"+reader.documentRoot)
			if err != nil {
				return err
			}
			docData, err := reader.readFile(docName)
			if err != nil {
				return err
			}
			doc, err := parseEditorXML(docData)
			if err != nil {
				return err
			}
			parts[docName], err = editorXMLSetText(docData, doc, [][2]string{{"Annotations", ""}})
			if err != nil {
				return err
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	base, err := e.annotationStructureSource(index, wanted)
	if err != nil {
		return err
	}
	return e.commitAnnotationParts(base, parts)
}
