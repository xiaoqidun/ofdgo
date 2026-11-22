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
	"path"
	"strings"
	"unicode/utf8"
)

// nextAttachmentID 分配以字母开头且不与已有附件重复的标识
// 入参: used 已用附件标识, maximum 当前编号上限
// 返回: string 附件标识, uint64 更新后的编号上限
func nextAttachmentID(used map[string]bool, maximum uint64) (string, uint64) {
	for {
		maximum++
		id := "a" + formatUnitID(maximum)
		if !used[id] {
			used[id] = true
			return id, maximum
		}
	}
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

// Attachments 获取当前编辑状态的附件列表，返回值与编辑器相互独立
// 返回: []Attachment 附件信息, error 结构错误
func (e *Editor) Attachments() ([]Attachment, error) {
	return e.AttachmentsContext(context.Background())
}

// AttachmentsContext 获取独立的附件列表，读取及解析期间响应取消
// 入参: ctx 取消上下文
// 返回: []Attachment 附件信息, error 结构或取消错误
func (e *Editor) AttachmentsContext(ctx context.Context) ([]Attachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.source == nil {
		return nil, nil
	}
	return e.source.reader.AttachmentsContext(ctx)
}

// AddAttachment 添加文档附件，保留原始数据，一次操作计入一条撤销记录
// 入参: name 显示文件名, data 文件数据
// 返回: string 附件标识, error 文件名或文档结构错误
func (e *Editor) AddAttachment(name string, data []byte) (string, error) {
	return e.AddAttachmentContext(context.Background(), name, data)
}

// AddAttachmentContext 添加附件并响应取消，失败时不修改文档和撤销记录
// 入参: ctx 取消上下文, name 显示文件名, data 文件数据
// 返回: string 附件标识, error 文件名、结构或取消错误
func (e *Editor) AddAttachmentContext(ctx context.Context, name string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateAttachmentName(name); err != nil {
		return "", err
	}
	maximum, err := e.sourceMaxID(editorProgress(nil).withContext(ctx))
	if err != nil {
		return "", err
	}
	base := e.source
	if base == nil {
		base, err = e.importBaseContext(ctx)
		if err != nil {
			return "", err
		}
	}
	var used map[string]bool
	if base.attachmentIDs == nil {
		attachments, err := base.reader.attachmentsContext(ctx, true)
		if err != nil {
			return "", err
		}
		used = make(map[string]bool, len(attachments)+len(base.removedAttachments)+1)
		for _, attachment := range attachments {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			used[attachment.ID] = true
		}
	} else {
		used = maps.Clone(base.attachmentIDs)
	}
	for removedID := range base.removedAttachments {
		used[removedID] = true
	}
	id, maximum := nextAttachmentID(used, maximum)
	if maximum > maxUnitID {
		return "", fmt.Errorf("MaxUnitID %d exceeds uint32 range", maximum)
	}
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
	parts := map[string][]byte{file: bytes.Clone(data)}
	if strings.TrimSpace(base.document.Attachments.Path) != "" {
		attName := base.reader.ResPath(base.document.Attachments.Path)
		existing, err := base.reader.readFileViewContext(ctx, attName)
		if err != nil {
			return "", err
		}
		if patched, ok := appendOFDXMLBeforeClosing(existing, "Attachments", entry); ok {
			parts[attName] = patched
		} else {
			root, err := parseEditorXMLContext(ctx, existing)
			if err != nil {
				return "", err
			}
			entry, err = editorXMLGenerated(entry, root.name.Space)
			if err != nil {
				return "", err
			}
			if root.open == root.end {
				parts[attName] = editorPatchXML(existing, []editorXMLPatch{editorXMLContent(existing, root, entry)})
			} else {
				parts[attName] = editorPatchXML(existing, []editorXMLPatch{{root.close, root.close, entry}})
			}
		}
	} else {
		docName := base.reader.documentRoot
		docData, err := base.reader.readFileViewContext(ctx, docName)
		if err != nil {
			return "", err
		}
		doc, err := parseEditorXMLContext(ctx, docData)
		if err != nil {
			return "", err
		}
		if node := doc.child("Attachments"); node != nil {
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
			attName := e.packageName("Attachments.xml")
			parts[attName] = container
			position := doc.close
			if next := doc.child("Extensions"); next != nil {
				position = next.start
			}
			parts[docName] = editorPatchXML(docData, []editorXMLPatch{{position, position, doc.textXML("Attachments", "/"+attName)}})
		}
	}
	next := *base
	if next.removedAttachments[id] {
		removed := maps.Clone(next.removedAttachments)
		delete(removed, id)
		next.removedAttachments = removed
	}
	next.idsReady = true
	if err := e.commitAnnotationPartsContext(ctx, &next, parts); err != nil {
		return "", err
	}
	used[id] = true
	if e.source != nil {
		updated := *e.source
		updated.attachmentIDs = used
		updated.idsReady = true
		e.source = &updated
	}
	e.maxID = maximum
	return id, nil
}

// RenameAttachment 修改附件显示名称，保留格式、内容及扩展字段，相同名称不产生撤销记录
// 入参: id 附件标识, name 显示名称
// 返回: error 名称或附件结构错误
func (e *Editor) RenameAttachment(id, name string) error {
	return e.RenameAttachmentContext(context.Background(), id, name)
}

// RenameAttachmentContext 修改附件名称并响应取消，保留内容、格式及扩展字段
// 入参: ctx 取消上下文, id 附件标识, name 显示名称
// 返回: error 名称、结构或取消错误
func (e *Editor) RenameAttachmentContext(ctx context.Context, id, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAttachmentName(name); err != nil {
		return err
	}
	return e.editAttachment(ctx, id, func(data []byte, node *editorXML, parts map[string][]byte) ([]byte, error) {
		if node.attr("Name") == name {
			return data, nil
		}
		return editorXMLAttribute(data, node, "Name", name)
	})
}

// ReplaceAttachment 替换附件内容，使用独立资源避免影响共享引用，保留名称和格式，不新增日期
// 内容相同时不修改文件及撤销记录
// 入参: id 附件标识, data 文件数据，调用后可复用
// 返回: error 附件结构或提交错误
func (e *Editor) ReplaceAttachment(id string, data []byte) error {
	return e.ReplaceAttachmentContext(context.Background(), id, data)
}

// ReplaceAttachmentContext 替换附件内容，比较、标识扫描及快照提交期间响应取消
// 内容相同不产生修改，失败或取消时保留原文档及撤销记录
// 入参: ctx 取消上下文, id 附件标识, data 新文件数据
// 返回: error 读取、取消或提交错误
func (e *Editor) ReplaceAttachmentContext(ctx context.Context, id string, data []byte) error {
	var maximum uint64
	changed := false
	err := e.editAttachment(ctx, id, func(fragment []byte, node *editorXML, parts map[string][]byte) ([]byte, error) {
		if child := node.child("FileLoc"); child != nil {
			location := strings.TrimSpace(editorImportText(fragment, child))
			if location != "" {
				name := e.source.reader.documentRoot
				if external := strings.TrimSpace(e.source.document.Attachments.Path); external != "" {
					name = e.source.reader.ResPath(external)
				}
				equal, err := packageDataEqual(ctx, e.source.reader, resolveResourcePath(name, "", location), data)
				if err != nil || equal {
					return fragment, err
				}
			}
		}
		var err error
		maximum, err = e.sourceMaxID(editorProgress(nil).withContext(ctx))
		if err != nil {
			return nil, err
		}
		if maximum+1 > maxUnitID {
			return nil, fmt.Errorf("MaxUnitID %d exceeds uint32 range", maximum+1)
		}
		file := e.packageName("Attachments/Attachment_" + formatUnitID(maximum+1))
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
		root, err := parseEditorXMLContext(ctx, fragment)
		if err != nil {
			return nil, err
		}
		fragment, err = editorXMLAttribute(fragment, root, "Size", ofdNumber(float64(len(data))/1024))
		if err != nil {
			return nil, err
		}
		parts[file] = bytes.Clone(data)
		changed = true
		return fragment, nil
	})
	if err != nil {
		return err
	}
	if changed {
		e.maxID = maximum + 1
		if e.source != nil {
			next := *e.source
			next.idsReady = true
			e.source = &next
		}
	}
	return nil
}

// DeleteAttachment 删除附件声明，输出时清理相关动作及可确认无引用的内容，支持撤销和重做
// 入参: id 附件标识
// 返回: error 附件结构或提交错误
func (e *Editor) DeleteAttachment(id string) error {
	return e.DeleteAttachmentContext(context.Background(), id)
}

// DeleteAttachmentContext 删除附件声明并响应取消，成功后可撤销及重做
// 入参: ctx 取消上下文, id 附件标识
// 返回: error 结构或取消错误
func (e *Editor) DeleteAttachmentContext(ctx context.Context, id string) error {
	return e.editAttachment(ctx, id, func([]byte, *editorXML, map[string][]byte) ([]byte, error) {
		return nil, nil
	})
}

// editAttachment 对单个附件片段执行原子编辑，兼容独立附件文件及内联声明
// 入参: ctx 取消上下文, id 附件标识, change 片段及资源修改函数
// 返回: error 结构、标识或提交错误
func (e *Editor) editAttachment(ctx context.Context, id string, change func([]byte, *editorXML, map[string][]byte) ([]byte, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base := e.source
	if base == nil {
		return fmt.Errorf("attachment not found: %s", id)
	}
	name := base.reader.documentRoot
	external := strings.TrimSpace(base.document.Attachments.Path) != ""
	if external {
		name = base.reader.ResPath(base.document.Attachments.Path)
	}
	data, err := base.reader.readFileViewContext(ctx, name)
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	if !external {
		root = root.child("Attachments")
	}
	var found *editorXML
	if root != nil {
		for _, child := range root.children {
			if err := ctx.Err(); err != nil {
				return err
			}
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
	fragment, err := editorXMLStandaloneContext(ctx, data[found.start:found.end], found)
	if err != nil {
		return err
	}
	node, err := parseEditorXMLContext(ctx, fragment)
	if err != nil {
		return err
	}
	parts := make(map[string][]byte)
	updated, err := change(fragment, node, parts)
	if err != nil {
		return err
	}
	if bytes.Equal(updated, fragment) && len(parts) == 0 {
		return ctx.Err()
	}
	parts[name] = editorPatchXML(data, []editorXMLPatch{{found.start, found.end, updated}})
	next := *base
	if len(updated) == 0 {
		removed := maps.Clone(base.removedAttachments)
		if removed == nil {
			removed = make(map[string]bool)
		}
		removed[id] = true
		next.removedAttachments = removed
	}
	if file := node.child("FileLoc"); file != nil {
		location := strings.TrimSpace(editorImportText(fragment, file))
		if location != "" {
			oldPath := editorResourceLocation(name, "", location)
			retireOld := len(updated) == 0
			if !retireOld {
				updatedNode, parseErr := parseEditorXMLContext(ctx, updated)
				if parseErr != nil {
					return parseErr
				}
				newPath := ""
				if updatedFile := updatedNode.child("FileLoc"); updatedFile != nil {
					if newLoc := strings.TrimSpace(editorImportText(updated, updatedFile)); newLoc != "" {
						newPath = editorResourceLocation(name, "", newLoc)
					}
				}
				retireOld = newPath != oldPath
			}
			if retireOld {
				retired := maps.Clone(base.retiredAttachments)
				if retired == nil {
					retired = make(map[string]bool)
				}
				retired[oldPath] = true
				next.retiredAttachments = retired
			}
		}
	}
	return e.commitAnnotationPartsContext(ctx, &next, parts)
}
