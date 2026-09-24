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
	"maps"
	"strings"
)

// prunePageReferences 在输出快照中删除已移除页面的标准引用，不修改历史快照或其他文档
// 沿主文档、页面、模板、资源、注释和签名索引检查，不进入附件、私有扩展和签名原始凭据
// 入参: parts 输出覆盖条目
// 返回: error 读取或解析错误
func (e *Editor) prunePageReferences(parts map[string][]byte) error {
	removed := maps.Clone(e.removedPages)
	if removed == nil {
		removed = make(map[string]bool)
	}
	for id := range e.source.pages {
		removed[id] = true
	}
	for id := range e.source.annotationPages {
		removed[id] = true
	}
	for _, page := range e.pages {
		delete(removed, page.ID)
	}
	if len(removed) == 0 {
		return nil
	}
	reader := e.source.reader
	bookmarks := make(map[string]bool)
	for _, bookmark := range e.source.document.Bookmarks.Bookmark {
		if removed[bookmark.Dest.PageID] {
			bookmarks[bookmark.Name] = true
		}
	}
	queue := []string{reader.ResPath(reader.OFD.DocBody[0].DocRoot)}
	if name := e.source.document.Signatures; name != "" {
		queue = append(queue, reader.ResPath(name))
	}
	seen := make(map[string]bool)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		data, ok := parts[name]
		if !ok {
			var err error
			data, err = reader.readFile(name)
			if err != nil {
				return err
			}
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		var patches []editorXMLPatch
		var walk func(*editorXML) error
		walk = func(node *editorXML) error {
			if node.name.Space != "" && node.name.Space != ofdNamespace && node.name.Space != "http://www.ofdspec.org" {
				return nil
			}
			remove := false
			switch node.name.Local {
			case "Action":
				if target := node.child("Goto"); target != nil {
					if dest := target.child("Dest"); dest != nil {
						remove = removed[dest.attr("PageID")]
					} else if bookmark := target.child("Bookmark"); bookmark != nil {
						remove = bookmarks[bookmark.attr("Name")]
					}
				}
			case "Bookmark":
				if node.parent != nil && node.parent.name.Local == "Bookmarks" {
					remove = bookmarks[node.attr("Name")]
				}
			case "Page":
				if root.name.Local == "Annotations" {
					remove = removed[node.attr("PageID")]
				}
			case "Dest":
				remove = removed[node.attr("PageID")]
			case "StampAnnot":
				remove = removed[node.attr("PageRef")]
			case "Extension":
				remove = removed[node.attr("RefId")]
			}
			if remove {
				patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
				return nil
			}
			var location string
			switch node.name.Local {
			case "Page", "TemplatePage", "Signature":
				if node.parent != nil && (node.parent.name.Local == "Pages" || node.parent.name.Local == "CommonData" || node.parent.name.Local == "Signatures") {
					location = node.attr("BaseLoc")
				}
			case "PublicRes", "DocumentRes", "PageRes", "Annotations", "Extensions":
				if len(node.children) == 0 {
					location = strings.TrimSpace(editorImportText(data, node))
				}
			case "FileLoc":
				if root.name.Local == "Annotations" {
					location = strings.TrimSpace(editorImportText(data, node))
				}
			case "CustomTags", "ExtendData", "Data":
				return nil
			}
			if location != "" {
				resolved, err := editorPageLocation(reader, parts, name, location)
				if err != nil {
					return err
				}
				queue = append(queue, resolved)
			}
			start := len(patches)
			for _, child := range node.children {
				if err := walk(child); err != nil {
					return err
				}
			}
			if (node.name.Local == "Actions" || node.name.Local == "Bookmarks") && len(node.children) > 0 && len(patches)-start == len(node.children) {
				empty := true
				for i, child := range node.children {
					empty = empty && patches[start+i].start == child.start && patches[start+i].end == child.end
				}
				if empty {
					patches = append(patches[:start], editorXMLPatch{start: node.start, end: node.end})
				}
			}
			return nil
		}
		if err := walk(root); err != nil {
			return err
		}
		if len(patches) != 0 {
			parts[name] = editorPatchXML(data, patches)
		}
	}
	return nil
}

// pruneCreatedPageReferences 清理创作文档中被明确删除页面的直接跳转，不检查其他目标是否可达
// 入参: data 创作XML, removed 已删除页面标识
// 返回: []byte 更新后的XML, error 解析错误
func pruneCreatedPageReferences(data []byte, removed map[string]bool) ([]byte, error) {
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	var patches []editorXMLPatch
	var walk func(*editorXML)
	walk = func(node *editorXML) {
		if node.name.Space != ofdNamespace {
			return
		}
		if node.name.Local == "Action" {
			if target := node.child("Goto"); target != nil {
				if dest := target.child("Dest"); dest != nil && removed[dest.attr("PageID")] {
					patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
					return
				}
			}
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return editorPatchXML(data, patches), nil
}

// editorPageLocation 解析标准XML引用，优先使用所在文件的相对路径
// 入参: reader 原包, parts 覆盖条目, name 所在XML, value 路径
// 返回: string 包内路径, error 路径不存在
func editorPageLocation(reader *Reader, parts map[string][]byte, name, value string) (string, error) {
	for _, candidate := range []string{resolveResourcePath(name, "", value), reader.ResPath(value)} {
		if _, ok := parts[candidate]; ok || reader.files[candidate] != nil {
			return candidate, nil
		}
		if file, ok := reader.packageFile(candidate); ok {
			return cleanPackagePath(file.Name), nil
		}
	}
	return "", fmt.Errorf("page reference %q in %s not found", value, name)
}
