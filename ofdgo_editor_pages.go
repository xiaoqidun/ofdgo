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
	"io"
	"maps"
	"strings"
)

// pageReferenceNode 记录跳转引用扫描中的父节点及删除范围
type pageReferenceNode struct {
	name              string
	start, patch      int
	children, removed int
	standard, drop    bool
}

// pruneCreatedPageReferences 清理创作文档中被明确删除页面的直接跳转，不检查其他目标是否可达
// 入参: ctx 取消上下文, data 创作XML, removed 已删除页面标识
// 返回: []byte 更新后的XML, error 解析或取消错误
func pruneCreatedPageReferences(ctx context.Context, data []byte, removed map[string]bool) ([]byte, error) {
	d := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	var stack []pageReferenceNode
	var patches []editorXMLPatch
	root := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			node := pageReferenceNode{name: token.Name.Local, start: start, patch: len(patches), standard: token.Name.Space == ofdNamespace2016}
			if len(stack) == 0 {
				if root {
					return nil, fmt.Errorf("multiple XML roots")
				}
				root = true
			} else {
				parent := &stack[len(stack)-1]
				parent.children++
				node.standard = node.standard && parent.standard
			}
			if node.standard && node.name == "Dest" && len(stack) >= 2 && stack[len(stack)-1].name == "Goto" && stack[len(stack)-2].name == "Action" {
				for _, attr := range token.Attr {
					if attr.Name.Space == "" && attr.Name.Local == "PageID" && removed[editorResourceID(attr.Value)] {
						stack[len(stack)-2].drop = true
					}
				}
			}
			stack = append(stack, node)
		case xml.EndElement:
			node := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if node.drop || node.standard && node.name == "Actions" && node.removed > 0 && node.children == node.removed {
				patches = append(patches[:node.patch], editorXMLPatch{start: node.start, end: int(d.InputOffset())})
				if len(stack) > 0 {
					stack[len(stack)-1].removed++
				}
			}
		}
	}
	if !root {
		return nil, fmt.Errorf("empty XML document")
	}
	if len(patches) > 0 {
		data = editorPatchXML(data, patches)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// editorPageLocation 按所在XML解析引用，绝对路径以包根目录为基准
// 入参: reader 原包, parts 覆盖条目, name 所在XML, value 路径
// 返回: string 包内路径, error 路径不存在
func editorPageLocation(reader *Reader, parts map[string][]byte, name, value string) (string, error) {
	return editorPageResourceLocation(reader, parts, name, "", value)
}

// editorPageResourceLocation 按所在XML与可选资源基准目录解析引用
// 入参: reader 原包, parts 覆盖条目, name 所在XML, base 资源基准目录, value 路径
// 返回: string 包内路径, error 路径不存在
func editorPageResourceLocation(reader *Reader, parts map[string][]byte, name, base, value string) (string, error) {
	candidate := resolveResourcePath(name, base, value)
	if !validPackagePath(candidate) {
		return "", fmt.Errorf("page reference %q in %s not found", value, name)
	}
	if _, ok := parts[candidate]; ok {
		return candidate, nil
	}
	if _, ok := reader.files[candidate]; ok {
		return candidate, nil
	}
	if file, ok := reader.packageFile(candidate); ok {
		return cleanPackagePath(file.Name), nil
	}
	if reader.encryptedSource().has(candidate) {
		return candidate, nil
	}
	return "", fmt.Errorf("page reference %q in %s not found", value, name)
}

// pruneDocumentReferences 在输出快照中清理已删除页面和附件的引用，不修改历史快照或其他文档
// 沿当前文档、页面、模板、资源、注释和签名索引检查，不进入附件、私有扩展和签名原始凭据
// 版本包通过独立副本隔离更新当前版本可见的签名列表与签名描述，保留历史版本原始签名快照
// 入参: ctx 取消上下文, parts 输出覆盖条目, generated 本次自产页面及引用，可为nil
// 返回: map[string]string 版本签名路径替换映射, error 读取、解析或取消错误
func (e *Editor) pruneDocumentReferences(ctx context.Context, parts map[string][]byte, generated map[string]editorGeneratedReferences) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	removed := maps.Clone(e.removedPages)
	if removed == nil {
		removed = make(map[string]bool)
	}
	for id := range e.source.pages {
		removed[editorResourceID(id)] = true
	}
	for id := range e.source.annotationPages {
		removed[editorResourceID(id)] = true
	}
	for _, page := range e.pages {
		delete(removed, editorResourceID(page.ID))
	}
	delete(removed, "")
	if len(removed) == 0 && len(e.source.removedAttachments) == 0 {
		return nil, nil
	}
	reader := e.source.reader
	bookmarks := make(map[string]bool)
	for _, bookmark := range e.source.document.Bookmarks.Bookmark {
		if removed[editorResourceID(bookmark.Dest.PageID)] {
			bookmarks[bookmark.Name] = true
		}
	}
	queue := []string{reader.documentRoot}
	if name := e.source.document.Signatures; len(removed) != 0 && name != "" && !reader.ofd.DocBody[reader.documentIndex].versioned {
		queue = append(queue, reader.ResPath(name))
	}
	seen := make(map[string]bool)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		data, ok := parts[name]
		if !ok {
			var err error
			data, err = reader.readFileViewContext(ctx, name)
			if err != nil {
				return nil, err
			}
		} else if known := generated[name]; data == nil && known.staged != nil {
			input := imageInput{ReadCloser: known.staged.open(), ctx: ctx}
			var err error
			data, err = io.ReadAll(input)
			input.Close()
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		resBase := ""
		if root.name.Local == "Res" {
			resBase = root.attr("BaseLoc")
		}
		var patches []editorXMLPatch
		var walk func(*editorXML) error
		walk = func(node *editorXML) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if classifyOFDNamespace(node.name.Space) == ofdXMLUnknown {
				return nil
			}
			remove := false
			switch node.name.Local {
			case "Action":
				if target := node.child("Goto"); target != nil {
					if dest := target.child("Dest"); dest != nil {
						remove = removed[editorResourceID(dest.attr("PageID"))]
					} else if bookmark := target.child("Bookmark"); bookmark != nil {
						remove = bookmarks[bookmark.attr("Name")]
					}
				} else if attachment := node.child("GotoA"); attachment != nil {
					remove = e.source.removedAttachments[attachment.attr("AttachID")]
				}
			case "Bookmark":
				if node.parent != nil && node.parent.name.Local == "Bookmarks" {
					remove = bookmarks[node.attr("Name")]
				}
			case "Page":
				if root.name.Local == "Annotations" {
					remove = removed[editorResourceID(node.attr("PageID"))]
				}
			case "Dest":
				remove = removed[editorResourceID(node.attr("PageID"))]
			case "StampAnnot":
				remove = removed[editorResourceID(node.attr("PageRef"))]
			case "Extension":
				remove = removed[editorResourceID(node.attr("RefId"))]
			}
			if remove {
				patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
				return nil
			}
			var location, locBase string
			switch node.name.Local {
			case "Page":
				if node.parent != nil && node.parent.name.Local == "Pages" {
					location = node.attr("BaseLoc")
				} else if root.name.Local == "Annotations" {
					location = strings.TrimSpace(node.attr("FileLoc"))
				}
			case "TemplatePage", "Signature":
				if node.parent != nil && (node.parent.name.Local == "CommonData" || node.parent.name.Local == "Signatures") {
					location = node.attr("BaseLoc")
				}
			case "CompositeGraphicUnit":
				if root.name.Local == "Res" && node.parent != nil && node.parent.name.Local == "CompositeGraphicUnits" {
					location, locBase = strings.TrimSpace(node.attr("BaseLoc")), resBase
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
				resolved, err := editorPageResourceLocation(reader, parts, name, locBase, location)
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
			return nil, err
		}
		if len(patches) != 0 {
			parts[name] = editorPatchXML(data, patches)
			delete(generated, name)
		}
	}
	if len(removed) != 0 && e.source.document.Signatures != "" && reader.ofd.DocBody[reader.documentIndex].versioned {
		remaps, err := e.pruneVersionSignatureReferences(ctx, parts, removed)
		if err != nil {
			return nil, err
		}
		return remaps, ctx.Err()
	}
	return nil, ctx.Err()
}

// pruneVersionSignatureReferences 在多版本文档删页时隔离更新当前版本可见签名中的悬空印章引用
// 入参: ctx 取消上下文, parts 输出覆盖条目, removed 已删除页面规范标识集合
// 返回: map[string]string 旧路径到新路径的替换映射, error 读取、解析或取消错误
func (e *Editor) pruneVersionSignatureReferences(ctx context.Context, parts map[string][]byte, removed map[string]bool) (map[string]string, error) {
	reader := e.source.reader
	listPath := cleanPackagePath(reader.ResPath(e.source.document.Signatures))
	if !validPackagePath(listPath) {
		return nil, nil
	}
	listData, ok := parts[listPath]
	if !ok {
		var err error
		listData, err = reader.readFileViewContext(ctx, listPath)
		if err != nil {
			return nil, err
		}
	}
	listRoot, err := parseEditorXMLContext(ctx, listData)
	if err != nil {
		return nil, err
	}
	var scope *versionSignatureScope
	if reader.versionInfo != nil {
		var list Signatures
		if xml.Unmarshal(listData, &list) == nil {
			scope, err = reader.buildVersionSignatureScope(ctx, listPath, list)
			if err != nil {
				return nil, err
			}
		}
	}
	staged := maps.Clone(parts)
	remaps := make(map[string]string)
	currentSigIDs := make(map[string]bool)
	var listPatches []editorXMLPatch
	for _, node := range listRoot.children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !node.matchesOFD("Signature") {
			continue
		}
		sigRef := Signature{ID: node.attr("ID"), BaseLoc: node.attr("BaseLoc"), Type: SignType(node.attr("Type"))}
		if scope != nil && !reader.versionSignatureMatches(ctx, scope, sigRef) {
			continue
		}
		currentSigIDs[sigRef.ID] = true
		sigPath := cleanPackagePath(signatureRefPath(listPath, sigRef.BaseLoc))
		if !validPackagePath(sigPath) {
			continue
		}
		sigData, exists := staged[sigPath]
		if !exists {
			sigData, err = reader.readFileViewContext(ctx, sigPath)
			if err != nil {
				return nil, err
			}
		}
		sigRoot, err := parseEditorXMLContext(ctx, sigData)
		if err != nil {
			return nil, err
		}
		var sigPatches []editorXMLPatch
		var walkSig func(*editorXML)
		walkSig = func(current *editorXML) {
			if classifyOFDNamespace(current.name.Space) == ofdXMLUnknown {
				return
			}
			if current.name.Local == "StampAnnot" && removed[editorResourceID(current.attr("PageRef"))] {
				sigPatches = append(sigPatches, editorXMLPatch{start: current.start, end: current.end})
				return
			}
			for _, child := range current.children {
				walkSig(child)
			}
		}
		walkSig(sigRoot)
		if len(sigPatches) == 0 {
			continue
		}
		newSigPath := packageAvailableName(reader, staged, sigPath)
		staged[newSigPath] = editorPatchXML(sigData, sigPatches)
		remaps[sigPath] = newSigPath
		patch, err := editorXMLAttributePatch(listData, node, "BaseLoc", "/"+newSigPath)
		if err != nil {
			return nil, err
		}
		listPatches = append(listPatches, patch)
	}
	if len(listPatches) == 0 {
		return nil, nil
	}
	ofdName := "OFD.xml"
	if file, ok := reader.packageFile(ofdName); ok {
		ofdName = cleanPackagePath(file.Name)
	}
	_, protectedFiles, protected, err := e.outputProtection(ctx, reader)
	if err != nil {
		return nil, err
	}
	ofdProtected := false
	listProtected := false
	if protected || protectedFiles[ofdName] {
		otherProtected, _, protErr := reader.signatureRemovalProtected(ctx, listPath, currentSigIDs, true)
		if protErr != nil {
			return nil, protErr
		}
		ofdProtected = otherProtected[ofdName] || otherProtected["OFD.xml"]
		listProtected = otherProtected[listPath]
	}
	if ofdProtected {
		listShared, shareErr := reader.versionSignatureListShared(ctx, listPath)
		if shareErr != nil {
			return nil, shareErr
		}
		if listProtected || listShared {
			return nil, nil
		}
		for _, newSigPath := range remaps {
			parts[newSigPath] = staged[newSigPath]
		}
		parts[listPath] = editorPatchXML(listData, listPatches)
		return remaps, nil
	}
	newListPath := packageAvailableName(reader, staged, listPath)
	staged[newListPath] = editorPatchXML(listData, listPatches)
	ofdData, ok := parts[ofdName]
	if !ok {
		ofdData, err = reader.readFileViewContext(ctx, ofdName)
		if err != nil {
			return nil, err
		}
	}
	ofdRoot, err := parseEditorXMLContext(ctx, ofdData)
	if err != nil {
		return nil, err
	}
	body := ofdRoot.childAt("DocBody", reader.documentIndex)
	if body == nil {
		return nil, fmt.Errorf("missing document body")
	}
	updatedOFD, err := editorXMLSetText(ofdData, body, [][2]string{{"Signatures", "/" + newListPath}})
	if err != nil {
		return nil, err
	}
	for _, newSigPath := range remaps {
		parts[newSigPath] = staged[newSigPath]
	}
	parts[newListPath] = staged[newListPath]
	parts[ofdName] = updatedOFD
	remaps[listPath] = newListPath
	return remaps, nil
}
