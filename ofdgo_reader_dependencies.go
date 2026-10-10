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
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// 包内引用分为无引用、原始数据及OFD结构文件
const (
	packageReferenceNone = iota
	packageReferenceData
	packageReferenceXML
)

// packageReference 根据节点位置识别文件引用，不将签名摘要引用或扩展数据解释为结构依赖
// 入参: root 文件根节点, parent 父节点, name 当前节点, attribute 属性名称，空值表示节点文本
// 返回: int 引用类别
func packageReference(root, parent, name, attribute string) int {
	if attribute != "" {
		switch attribute {
		case "BaseLoc":
			switch {
			case root == "OFD" && parent == "Versions" && name == "Version",
				root == "Document" && parent == "Pages" && name == "Page",
				root == "Document" && parent == "CommonData" && name == "TemplatePage",
				root == "Signatures" && parent == "Signatures" && name == "Signature",
				root == "Res" && parent == "CompositeGraphicUnits" && name == "CompositeGraphicUnit":
				return packageReferenceXML
			}
		case "FileLoc":
			if root == "Annotations" && name == "PageAnnot" {
				return packageReferenceXML
			}
		case "Link":
			if root == "Res" && name == "DrawParam" {
				return packageReferenceXML
			}
		case "Profile":
			if root == "Res" && name == "ColorSpace" {
				return packageReferenceData
			}
		}
		return packageReferenceNone
	}
	switch name {
	case "DocRoot":
		if root == "OFD" && parent == "DocBody" || root == "DocVersion" && parent == "DocVersion" {
			return packageReferenceXML
		}
	case "Signatures":
		if root == "OFD" && parent == "DocBody" {
			return packageReferenceXML
		}
	case "Cover":
		if root == "OFD" && parent == "DocInfo" {
			return packageReferenceData
		}
	case "PublicRes", "DocumentRes":
		if root == "Document" && parent == "CommonData" {
			return packageReferenceXML
		}
	case "PageRes":
		if root == "Page" && parent == "Page" {
			return packageReferenceXML
		}
	case "Annotations", "Attachments", "CustomTags", "Extensions":
		if root == "Document" && parent == "Document" {
			return packageReferenceXML
		}
	case "FileLoc":
		switch {
		case root == "Annotations" && parent == "Page":
			return packageReferenceXML
		case root == "Attachments" && parent == "Attachment", root == "CustomTags" && parent == "CustomTag":
			return packageReferenceData
		}
	case "File":
		if root == "DocVersion" && parent == "FileList" {
			return packageReferenceData
		}
	case "FontFile":
		if root == "Res" && parent == "Font" {
			return packageReferenceData
		}
	case "MediaFile":
		if root == "Res" && parent == "MultiMedia" {
			return packageReferenceData
		}
	case "Profile":
		if root == "Res" && parent == "ColorSpace" {
			return packageReferenceData
		}
	case "SignedValue":
		if root == "Signature" && parent == "Signature" {
			return packageReferenceData
		}
	case "BaseLoc":
		if root == "Signature" && parent == "Seal" {
			return packageReferenceData
		}
	case "SchemaLoc":
		if root == "CustomTags" && parent == "CustomTag" {
			return packageReferenceData
		}
	case "ExtendData":
		if parent == "Extension" {
			return packageReferenceData
		}
	}
	return packageReferenceNone
}

// documentFiles 遍历独立文档及版本依赖，只解析结构文件，不读取附件等原始数据
// 入参: ctx 取消上下文, index 文档索引
// 返回: map[string]bool 包内文件路径, error 读取或取消错误
func (r *Reader) documentFiles(ctx context.Context, index int) (map[string]bool, error) {
	files, _, err := r.documentDependencies(ctx, index)
	return files, err
}

// documentDependencies 区分文档全部依赖与需要解析的结构文件，避免重复读取原始数据
// 入参: ctx 取消上下文, index 文档索引
// 返回: map[string]bool 全部依赖, map[string]bool 结构文件, error 读取或取消错误
func (r *Reader) documentDependencies(ctx context.Context, index int) (map[string]bool, map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if index < 0 || index >= r.DocumentCount() {
		return nil, nil, fmt.Errorf("document index out of range: %d", index)
	}
	var queue []string
	seen := make(map[string]bool)
	queued := make(map[string]bool)
	add := func(name string, kind int) {
		seen[name] = true
		if kind == packageReferenceXML && !queued[name] {
			queued[name] = true
			queue = append(queue, name)
		}
	}
	data, err := r.readFile("OFD.xml")
	if err != nil {
		return nil, nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, nil, err
	}
	body := root.childAt("DocBody", index)
	if body == nil {
		return nil, nil, fmt.Errorf("document body not found: %d", index)
	}
	var collect func(*editorXML)
	collect = func(node *editorXML) {
		if !node.matchesOFD(node.name.Local) {
			return
		}
		parent := node.parent.name.Local
		if kind := packageReference("OFD", parent, node.name.Local, ""); kind != packageReferenceNone {
			if value := strings.TrimSpace(editorImportText(data, node)); value != "" {
				add(resolveResourcePath("OFD.xml", "", value), kind)
			}
		}
		for _, attr := range node.attrs {
			if attr.Name.Space != "" || strings.TrimSpace(attr.Value) == "" {
				continue
			}
			if kind := packageReference("OFD", parent, node.name.Local, attr.Name.Local); kind != packageReferenceNone {
				add(resolveResourcePath("OFD.xml", "", attr.Value), kind)
			}
		}
		for _, child := range node.children {
			collect(child)
		}
	}
	collect(body)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		actual := queue[0]
		queue = queue[1:]
		if !r.fileNames[actual] {
			continue
		}
		if err := r.documentFileReferences(ctx, actual, true, add); err != nil {
			return nil, nil, err
		}
	}
	return seen, queued, ctx.Err()
}

// documentFileReferences 流式扫描结构文件，支持同一作用域内的未知命名空间
// 入参: ctx 取消上下文, name 文件路径, rejectUnknown 是否拒绝无法识别的扩展节点, add 接收引用路径及类别
// 返回: error 读取或结构错误
func (r *Reader) documentFileReferences(ctx context.Context, name string, rejectUnknown bool, add func(string, int)) error {
	input, err := r.openFile(name)
	if err != nil {
		return err
	}
	defer input.Close()
	decoder := xml.NewDecoder(imageInput{ReadCloser: input, context: ctx})
	var stack []xml.Name
	root, base := "", ""
	var text strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			if root == "" {
				return fmt.Errorf("empty document dependency: %s", name)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("document dependency %s: %w", name, err)
		}
		switch node := token.(type) {
		case xml.StartElement:
			parent := ""
			if len(stack) == 0 {
				if root != "" {
					return fmt.Errorf("multiple XML roots in %s", name)
				}
				root = node.Name.Local
			} else {
				owner := stack[len(stack)-1]
				parent = owner.Local
				if classifyOFDNamespace(node.Name.Space) == ofdXMLUnknown && node.Name.Space != owner.Space {
					if rejectUnknown {
						return fmt.Errorf("cannot determine document dependencies in %s", name)
					}
					if err := decoder.Skip(); err != nil {
						return err
					}
					continue
				}
			}
			if parent == "Extension" && node.Name.Local == "Data" {
				if err := decoder.Skip(); err != nil {
					return err
				}
				continue
			}
			for _, attr := range node.Attr {
				if attr.Name.Space != "" || strings.TrimSpace(attr.Value) == "" {
					continue
				}
				if len(stack) == 0 && root == "Res" && attr.Name.Local == "BaseLoc" {
					base = attr.Value
				} else if kind := packageReference(root, parent, node.Name.Local, attr.Name.Local); kind != packageReferenceNone {
					add(resolveResourcePath(name, base, attr.Value), kind)
				}
			}
			stack = append(stack, node.Name)
			text.Reset()
		case xml.CharData:
			if len(stack) >= 2 && packageReference(root, stack[len(stack)-2].Local, stack[len(stack)-1].Local, "") != packageReferenceNone {
				text.Write(node)
			}
		case xml.EndElement:
			if len(stack) >= 2 {
				kind := packageReference(root, stack[len(stack)-2].Local, node.Name.Local, "")
				if value := strings.TrimSpace(text.String()); kind != packageReferenceNone && value != "" {
					add(resolveResourcePath(name, base, value), kind)
				}
			}
			stack = stack[:len(stack)-1]
			text.Reset()
		}
	}
}
