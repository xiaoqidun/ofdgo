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
	"slices"
	"strings"
)

// editorFontDocument 保存一个文档的字体声明与完整内容引用，不进入附件和签名封装
type editorFontDocument struct {
	reader *Reader
	index  int
	files  map[string]*editorFontXML
	fonts  map[string]*editorFontDeclaration
	unsafe bool
}

// editorFontXML 保存字体声明及文字引用所在的XML
type editorFontXML struct {
	name  string
	data  []byte
	root  *editorXML
	texts []*editorXML
}

// editorFontDeclaration 关联字体声明、所属资源文件及嵌入数据路径
type editorFontDeclaration struct {
	font     Font
	file     *editorFontXML
	node     *editorXML
	location string
}

// scanEditorFonts 按标准内容路径扫描正文、模板、注解、复合图元、图案及文字裁剪
// 入参: ctx 取消上下文, reader 包快照, index 文档索引
// 返回: *editorFontDocument 字体引用, error 读取或结构错误
func scanEditorFonts(ctx context.Context, reader *Reader, index int) (*editorFontDocument, error) {
	scan := &editorFontDocument{reader: reader, index: index, files: make(map[string]*editorFontXML), fonts: make(map[string]*editorFontDeclaration)}
	entry, err := reader.documentEntry(index)
	if err != nil {
		return nil, err
	}
	queue := []string{entry}
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := queue[0]
		queue = queue[1:]
		if scan.files[name] != nil {
			continue
		}
		data, err := reader.readFile(name)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		file := &editorFontXML{name: name, data: data, root: root}
		scan.files[name] = file
		base := ""
		if root.matchesOFD("Res") {
			base = root.attr("BaseLoc")
		}
		var visit func(*editorXML) error
		visit = func(node *editorXML) error {
			if classifyOFDNamespace(node.name.Space) == ofdXMLUnknown {
				scan.unsafe = true
				return nil
			}
			if node.matchesOFD("Attachments") || node.matchesOFD("Signatures") {
				return nil
			}
			if slices.Contains([]string{"CustomTags", "Extensions", "ExtendData", "Data"}, node.name.Local) {
				scan.unsafe = true
				return nil
			}
			for _, attr := range node.attrs {
				if attr.Name.Space != "" && attr.Name.Space != "xmlns" && attr.Name.Space != "http://www.w3.org/XML/1998/namespace" {
					scan.unsafe = true
				}
			}
			if node.matchesOFD("Font") && node.parent != nil && node.parent.matchesOFD("Fonts") && root.matchesOFD("Res") {
				if node.childAt("FontFile", 1) != nil {
					return fmt.Errorf("multiple font files for %s", node.attr("ID"))
				}
				var font Font
				if err := xml.Unmarshal(data[node.start:node.end], &font); err != nil {
					return err
				}
				id := editorResourceID(font.ID)
				if id == "" || scan.fonts[id] != nil {
					return fmt.Errorf("missing or duplicate font ID %q", font.ID)
				}
				location := ""
				if font.FontFile != "" {
					location = resolveResourcePath(name, base, font.FontFile)
				}
				scan.fonts[id] = &editorFontDeclaration{font: font, file: file, node: node, location: location}
			}
			if node.matchesOFD("TextObject") || node.matchesOFD("Text") && node.parent != nil && node.parent.matchesOFD("Area") {
				file.texts = append(file.texts, node)
				if node.attr("Font") == "" {
					scan.unsafe = true
				}
			}
			value := ""
			switch node.name.Local {
			case "PublicRes", "DocumentRes", "PageRes", "Annotations":
				value = strings.TrimSpace(editorImportText(data, node))
			case "Page", "TemplatePage", "CompositeGraphicUnit":
				value = node.attr("BaseLoc")
			case "PageAnnot":
				value = node.attr("FileLoc")
			case "DrawParam":
				value = node.attr("Link")
			case "FileLoc":
				if node.parent != nil && node.parent.matchesOFD("Page") && root.matchesOFD("Annotations") {
					value = strings.TrimSpace(editorImportText(data, node))
				}
			}
			if value != "" {
				queue = append(queue, resolveResourcePath(name, base, value))
			}
			for _, child := range node.children {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(root); err != nil {
			return nil, err
		}
	}
	return scan, nil
}

// apply 隔离其他文档及版本的共享文件，提交字体修改和路径引用
// 入参: ctx 取消上下文, changes 按原文件路径保存的修改
// 返回: error 引用扫描或XML修改错误
func (s *editorFontDocument) apply(ctx context.Context, changes map[string][]byte) error {
	view := *s.reader
	if s.index != view.documentIndex {
		view.selectedVersion = nil
	}
	view.documentIndex = s.index
	view.documentRoot, view.versionInfo = "", nil
	parts, err := view.versionChanges(ctx, changes)
	if err != nil {
		return err
	}
	next := *s.reader
	next.files = maps.Clone(next.files)
	if next.files == nil {
		next.files = make(map[string][]byte)
	}
	maps.Copy(next.files, parts)
	if err := next.initRoot(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*s.reader = next
	return nil
}

// editorFontXMLText 编码资源路径文本，不改变包内名称
// 入参: value 原始文本
// 返回: []byte XML文本
func editorFontXMLText(value string) []byte {
	var data bytes.Buffer
	_ = xml.EscapeText(&data, []byte(value))
	return data.Bytes()
}
