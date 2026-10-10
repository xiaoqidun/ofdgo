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
	"strconv"
	"strings"
	"unicode/utf8"
)

// Attachments 获取当前编辑状态的附件列表，返回值与编辑器相互独立
// 返回: []Attachment 附件信息, error 结构错误
func (e *Editor) Attachments() ([]Attachment, error) {
	if e.source == nil {
		return nil, nil
	}
	return e.source.reader.Attachments()
}

// AddAttachment 添加文档附件，保留原始数据，一次操作计入一条撤销记录
// 入参: name 显示文件名, data 文件数据
// 返回: string 附件标识, error 文件名或文档结构错误
func (e *Editor) AddAttachment(name string, data []byte) (string, error) {
	if err := validateAttachmentName(name); err != nil {
		return "", err
	}
	maximum, err := e.sourceMaxID(nil)
	if err != nil {
		return "", err
	}
	base := e.source
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return "", err
		}
	}
	attachments, err := base.reader.Attachments()
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(attachments))
	for _, attachment := range attachments {
		used[attachment.ID] = true
	}
	id, maximum := nextAttachmentID(used, maximum)
	file := e.packageName("Attachments/Attachment_" + id)
	attrs := ofdAttrs{}
	attrs.add("ID", id)
	attrs.add("Name", name)
	attrs.add("Format", strings.TrimPrefix(path.Ext(name), "."))
	attrs.add("Size", ofdNumber(float64(len(data))/1024))
	attrs.add("Visible", "true")
	entry, err := editorXMLContainer("Attachment", attrs, editorXMLText("FileLoc", "/"+file))
	if err != nil {
		return "", err
	}
	entry = bytes.TrimPrefix(entry, []byte(xml.Header))
	docName := cleanPackagePath(base.reader.OFD.DocBody[base.reader.documentIndex].DocRoot)
	docData, err := base.reader.readFile(docName)
	if err != nil {
		return "", err
	}
	doc, err := parseEditorXML(docData)
	if err != nil {
		return "", err
	}
	node := doc.child("Attachments")
	parts := map[string][]byte{file: bytes.Clone(data)}
	if base.document.Attachments.Path != "" {
		name := base.reader.ResPath(base.document.Attachments.Path)
		existing, err := base.reader.readFile(name)
		if err != nil {
			return "", err
		}
		root, err := parseEditorXML(existing)
		if err != nil {
			return "", err
		}
		entry, err = editorXMLGenerated(entry, root.name.Space)
		if err != nil {
			return "", err
		}
		if root.open == root.end {
			parts[name] = editorPatchXML(existing, []editorXMLPatch{editorXMLContent(existing, root, entry)})
		} else {
			parts[name] = editorPatchXML(existing, []editorXMLPatch{{root.close, root.close, entry}})
		}
	} else if node != nil {
		entry, err = editorXMLGenerated(entry, node.name.Space)
		if err != nil {
			return "", err
		}
		if node.open == node.end {
			parts[docName] = editorPatchXML(docData, []editorXMLPatch{editorXMLContent(docData, node, entry)})
		} else {
			parts[docName] = editorPatchXML(docData, []editorXMLPatch{{node.close, node.close, entry}})
		}
	} else {
		container, err := editorXMLContainer("Attachments", nil, entry)
		if err != nil {
			return "", err
		}
		name := e.packageName("Attachments.xml")
		parts[name] = container
		position := doc.close
		if next := doc.child("Extensions"); next != nil {
			position = next.start
		}
		parts[docName] = editorPatchXML(docData, []editorXMLPatch{{position, position, doc.textXML("Attachments", "/"+name)}})
	}
	if err := e.commitAnnotationParts(base, parts); err != nil {
		return "", err
	}
	e.maxID, e.source.idsReady = maximum, true
	return id, nil
}

// nextAttachmentID 分配以字母开头且不与已有附件重复的标识
// 入参: used 已用附件标识, maximum 当前编号上限
// 返回: string 附件标识, int 更新后的编号上限
func nextAttachmentID(used map[string]bool, maximum int) (string, int) {
	for {
		maximum++
		id := "a" + strconv.Itoa(maximum)
		if !used[id] {
			used[id] = true
			return id, maximum
		}
	}
}

// RenameAttachment 修改附件显示名称，保留格式、内容及扩展字段，相同名称不产生撤销记录
// 入参: id 附件标识, name 显示名称
// 返回: error 名称或附件结构错误
func (e *Editor) RenameAttachment(id, name string) error {
	if err := validateAttachmentName(name); err != nil {
		return err
	}
	return e.editAttachment(id, func(data []byte, node *editorXML, parts map[string][]byte) ([]byte, error) {
		if node.attr("Name") == name {
			return data, nil
		}
		return editorXMLAttribute(data, node, "Name", name)
	})
}

// ReplaceAttachment 替换附件内容，使用独立资源避免影响共享引用，保留名称和格式，不新增日期
// 入参: id 附件标识, data 文件数据，调用后可复用
// 返回: error 附件结构或提交错误
func (e *Editor) ReplaceAttachment(id string, data []byte) error {
	var maximum int
	err := e.editAttachment(id, func(fragment []byte, node *editorXML, parts map[string][]byte) ([]byte, error) {
		var err error
		maximum, err = e.sourceMaxID(nil)
		if err != nil {
			return nil, err
		}
		file := e.packageName("Attachments/Attachment_" + strconv.Itoa(maximum+1))
		var location bytes.Buffer
		if err := xml.EscapeText(&location, []byte("/"+file)); err != nil {
			return nil, err
		}
		if child := node.child("FileLoc"); child != nil {
			fragment = editorPatchXML(fragment, []editorXMLPatch{editorXMLContent(fragment, child, location.Bytes())})
		} else if node.open == node.end {
			fragment = editorPatchXML(fragment, []editorXMLPatch{editorXMLContent(fragment, node, node.textXML("FileLoc", "/"+file))})
		} else {
			fragment = editorPatchXML(fragment, []editorXMLPatch{{node.open, node.open, node.textXML("FileLoc", "/"+file)}})
		}
		root, err := parseEditorXML(fragment)
		if err != nil {
			return nil, err
		}
		fragment, err = editorXMLAttribute(fragment, root, "Size", ofdNumber(float64(len(data))/1024))
		if err != nil {
			return nil, err
		}
		parts[file] = bytes.Clone(data)
		return fragment, nil
	})
	if err != nil {
		return err
	}
	e.maxID, e.source.idsReady = maximum+1, true
	return nil
}

// DeleteAttachment 删除附件声明，保存时清理可确认无引用的内容，支持撤销和重做
// 入参: id 附件标识
// 返回: error 附件结构或提交错误
func (e *Editor) DeleteAttachment(id string) error {
	return e.editAttachment(id, func([]byte, *editorXML, map[string][]byte) ([]byte, error) {
		return nil, nil
	})
}

// editAttachment 对单个附件片段执行原子编辑，兼容独立附件文件及内联声明
// 入参: id 附件标识, change 片段及资源修改函数
// 返回: error 结构、标识或提交错误
func (e *Editor) editAttachment(id string, change func([]byte, *editorXML, map[string][]byte) ([]byte, error)) error {
	base := e.source
	if base == nil {
		return fmt.Errorf("attachment not found: %s", id)
	}
	name := cleanPackagePath(base.reader.OFD.DocBody[base.reader.documentIndex].DocRoot)
	external := strings.TrimSpace(base.document.Attachments.Path) != ""
	if external {
		name = base.reader.ResPath(base.document.Attachments.Path)
	}
	data, err := base.reader.readFile(name)
	if err != nil {
		return err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return err
	}
	if !external {
		root = root.child("Attachments")
	}
	var found *editorXML
	if root != nil {
		for _, child := range root.children {
			if child.matchesOFD("Attachment") && child.attr("ID") == id {
				if found != nil {
					return fmt.Errorf("duplicate attachment ID: %s", id)
				}
				found = child
			}
		}
	}
	if found == nil {
		return fmt.Errorf("attachment not found: %s", id)
	}
	fragment, err := editorXMLStandalone(data[found.start:found.end], found)
	if err != nil {
		return err
	}
	node, err := parseEditorXML(fragment)
	if err != nil {
		return err
	}
	parts := make(map[string][]byte)
	updated, err := change(fragment, node, parts)
	if err != nil {
		return err
	}
	if bytes.Equal(updated, fragment) && len(parts) == 0 {
		return nil
	}
	parts[name] = editorPatchXML(data, []editorXMLPatch{{found.start, found.end, updated}})
	if err := e.commitAnnotationParts(base, parts); err != nil {
		return err
	}
	if file := node.child("FileLoc"); file != nil {
		location := strings.TrimSpace(editorImportText(fragment, file))
		if location != "" {
			retired := maps.Clone(base.retiredAttachments)
			if retired == nil {
				retired = make(map[string]bool)
			}
			retired[strings.ToLower(cleanPackagePath(base.reader.ResPath(location)))] = true
			e.source.retiredAttachments = retired
		}
	}
	return nil
}

// validateAttachmentName 校验非空附件名及XML可无损表示的Unicode字符
// 入参: name 显示名称
// 返回: error 名称错误
func validateAttachmentName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("attachment name is required")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("attachment name is not valid UTF-8")
	}
	for _, char := range name {
		if char < 0x20 && char != '\t' && char != '\n' && char != '\r' || char == 0xfffe || char == 0xffff {
			return fmt.Errorf("attachment name contains an invalid XML character")
		}
	}
	return nil
}
