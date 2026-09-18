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
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
)

// AnnotationSelection 保存当前编辑器内可重复粘贴的注解原文及资源上下文
// 来源修改或删除不影响快照，不用于跨文档传输
type AnnotationSelection struct {
	editor    *Editor
	data      [][]byte
	resources []string
	states    map[string]map[string]editorCompositeState
}

// CaptureAnnotations 捕获注解选区，保留未知字段、外观及动作
// 入参: page 页面索引, ids 不重复的注解标识
// 返回: *AnnotationSelection 独立快照, error 错误信息
func (e *Editor) CaptureAnnotations(page int, ids []string) (*AnnotationSelection, error) {
	content, err := e.page(page)
	if err != nil {
		return nil, err
	}
	selection := &AnnotationSelection{editor: e, states: make(map[string]map[string]editorCompositeState)}
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("duplicate annotation ID %q", id)
		}
		seen[id] = true
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
		selection.data = append(selection.data, data)
		selection.states[id] = maps.Clone(e.source.annotationStates[e.pages[page].ID+"/"+id])
	}
	if e.originalPage(page) {
		origin := e.source.pages[e.pages[page].ID]
		for _, name := range content.PageRes {
			selection.resources = append(selection.resources, e.source.reader.ResPath(resolveResourcePath(origin.ref.BaseLoc, "", name)))
		}
	}
	return selection, nil
}

// PasteAnnotations 将注解快照粘贴到指定页并平移，保留动作目标和原有外观
// 全部标识重新分配，页级资源提升为文档资源，一次操作计入一条撤销记录
// 入参: page 目标页面索引, selection 当前编辑器快照, dx、dy 页面毫米位移
// 返回: []string 新注解标识, error 错误信息
func (e *Editor) PasteAnnotations(page int, selection *AnnotationSelection, dx, dy float64) ([]string, error) {
	if selection == nil || selection.editor != e || !finite(dx) || !finite(dy) {
		return nil, fmt.Errorf("invalid annotation selection or offset")
	}
	var result []string
	err := e.Transaction(func(edit *Editor) error {
		if page < 0 || page >= len(edit.pages) {
			return fmt.Errorf("page index %d out of range", page)
		}
		if len(selection.data) == 0 {
			return nil
		}
		if err := edit.prepareSourceIDs(); err != nil {
			return err
		}
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
			states[edit.pages[page].ID+"/"+id] = remapCompositeStates(selection.states[root.attr("ID")], ids)
			data, err = editorXMLRemapIDs(data, root, ids)
			if err != nil {
				return err
			}
			root, err = parseEditorXML(data)
			if err != nil {
				return err
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
		if err := edit.appendAnnotations(page, content); err != nil {
			return err
		}
		edit.source.annotationStates = states
		if len(selection.resources) != 0 {
			reader := edit.source.reader
			name := cleanPackagePath(reader.OFD.DocBody[0].DocRoot)
			data, err := reader.readFile(name)
			if err != nil {
				return err
			}
			root, err := parseEditorXML(data)
			if err != nil {
				return err
			}
			common := root.child("CommonData")
			var added []byte
			for _, name := range selection.resources {
				if !slices.ContainsFunc(append(slices.Clone(edit.source.document.CommonData.PublicRes), edit.source.document.CommonData.DocumentRes...), func(value string) bool { return reader.ResPath(value) == name }) {
					added = append(added, editorXMLText("DocumentRes", "/"+name)...)
				}
			}
			if len(added) != 0 {
				position := common.close
				for _, node := range common.children {
					if node.name.Local == "TemplatePage" || node.name.Local == "DefaultCS" {
						position = node.start
						break
					}
				}
				return edit.commitAnnotationParts(edit.source, map[string][]byte{name: editorPatchXML(data, []editorXMLPatch{{position, position, added}})})
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
	if _, ok := matrix.Invert(); !ok || !finite(matrix.a) || !finite(matrix.b) || !finite(matrix.c) || !finite(matrix.d) || !finite(matrix.e) || !finite(matrix.f) {
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
			if matrix == IdentityMatrix {
				continue
			}
			box, err := ParseBox(annotation.Appearance.Boundary)
			if err != nil {
				return err
			}
			bounds := matrix.TransformBox(box)
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
				for i, member := range edit.measureCompositeMembers(renderer, nodes) {
					if member.Object.Type == "TextObject" && member.Object.TextObject.Text() == old {
						if err := edit.updateCompositeText(nodes[i], member, &value, style, nil, nil); err != nil {
							return err
						}
						count++
					} else if member.Object.Type == "CompositeObject" || member.Object.Type == "CompositeGraphicUnit" {
						resource := member.Object.CompositeGraphicUnit.ResourceID
						if resource != "" && visiting[resource] {
							return fmt.Errorf("cyclic composite resource %q", resource)
						}
						children, err := edit.compositeMembers(nodes[i], renderer.Reader, renderer, visiting)
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
