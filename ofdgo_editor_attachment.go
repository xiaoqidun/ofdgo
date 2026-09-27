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
	"path"
	"strings"
)

// AddAttachment 添加文档附件，保留原始数据，一次操作计入一条撤销记录
// 入参: name 显示文件名, data 文件数据
// 返回: string 附件标识, error 文件名或文档结构错误
func (e *Editor) AddAttachment(name string, data []byte) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("attachment name is required")
	}
	if err := e.prepareSourceIDs(); err != nil {
		return "", err
	}
	base := e.source
	var err error
	if base == nil {
		base, err = e.importBase()
		if err != nil {
			return "", err
		}
	}
	id := e.nextID()
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
	docName := cleanPackagePath(base.reader.OFD.DocBody[0].DocRoot)
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
		if root.open == root.end {
			parts[name] = editorPatchXML(existing, []editorXMLPatch{editorXMLContent(existing, root, entry)})
		} else {
			parts[name] = editorPatchXML(existing, []editorXMLPatch{{root.close, root.close, entry}})
		}
	} else if node != nil {
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
		parts[docName] = editorPatchXML(docData, []editorXMLPatch{{position, position, editorXMLText("Attachments", "/"+name)}})
	}
	if err := e.commitAnnotationParts(base, parts); err != nil {
		return "", err
	}
	return id, nil
}
