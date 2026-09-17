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
	"path"
	"strings"
	"time"
)

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
				data = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, root, editorXMLText("Remark", remark))})
			} else {
				data = editorPatchXML(data, []editorXMLPatch{{root.open, root.open, editorXMLText("Remark", remark)}})
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

// editAnnotations 原子修改页面注解原文，按需分离共用文件并保留输入包与历史快照
// 入参: index 页面索引, ids 注解标识, edit 单个注解修改函数
// 返回: error 错误信息
func (e *Editor) editAnnotations(index int, ids []string, edit func([]byte, *editorXML) ([]byte, error)) error {
	if index < 0 || index >= len(e.pages) {
		return fmt.Errorf("page index %d out of range", index)
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || wanted[id] {
			return fmt.Errorf("missing or duplicate annotation ID %q", id)
		}
		wanted[id] = true
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
	for _, ref := range root.children {
		if ref.name.Local != "Page" || ref.name.Space != root.name.Space {
			continue
		}
		remaining++
		if ref.attr("PageID") != e.pages[index].ID {
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
			if node.name.Local != "Annot" || node.name.Space != page.name.Space {
				continue
			}
			count++
			id := node.attr("ID")
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
			if other == ref || other.name.Local != "Page" || other.name.Space != root.name.Space || other.child("FileLoc") == nil {
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
			file = path.Join(e.source.directory, "Annotations", e.pages[index].ID, file)
			var escaped bytes.Buffer
			_ = xml.EscapeText(&escaped, []byte("/"+file))
			patches = append(patches, editorXMLContent(data, loc, escaped.Bytes()))
		}
		parts[file] = editorPatchXML(content, changes)
	}
	for _, id := range ids {
		if !found[id] {
			return fmt.Errorf("annotation %q not found", id)
		}
	}
	if len(patches) != 0 {
		parts[name] = editorPatchXML(data, patches)
		if remaining == 0 {
			docName, err := editorPageLocation(reader, nil, "", "/"+cleanPackagePath(reader.OFD.DocBody[0].DocRoot))
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
			parts[docName] = editorXMLSetText(docData, doc, [][2]string{{"Annotations", ""}})
		}
	}
	if len(parts) == 0 {
		return nil
	}
	files := maps.Clone(reader.files)
	if files == nil {
		files = make(map[string][]byte)
	}
	maps.Copy(files, parts)
	updated := &Reader{Zip: reader.Zip, files: files}
	if err := updated.initRoot(); err != nil {
		return err
	}
	doc, err := updated.Doc()
	if err != nil {
		return err
	}
	before, after := e.source, *e.source
	after.reader, after.document = updated, doc
	e.source = &after
	if change := e.recordChange(); change != nil {
		change.undo = func(e *Editor) { e.source = before }
		change.redo = func(e *Editor) { e.source = &after }
	}
	return nil
}
