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

// editorFontXML 保存内容XML及其直接文件引用
type editorFontXML struct {
	name  string
	data  []byte
	root  *editorXML
	links []editorFontLink
	texts []*editorXML
}

// editorFontLink 记录需要随共享资源隔离而更新的路径
type editorFontLink struct {
	node      *editorXML
	attribute string
	target    string
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
	queue := []string{cleanPackagePath(reader.OFD.DocBody[index].DocRoot)}
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := queue[0]
		queue = queue[1:]
		if actual := reader.fileNamesFold[strings.ToLower(name)]; actual != "" {
			name = actual
		}
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
			attribute, value := "", ""
			switch node.name.Local {
			case "PublicRes", "DocumentRes", "PageRes", "Annotations":
				value = strings.TrimSpace(editorImportText(data, node))
			case "Page", "TemplatePage", "CompositeGraphicUnit":
				attribute, value = "BaseLoc", node.attr("BaseLoc")
			case "PageAnnot":
				attribute, value = "FileLoc", node.attr("FileLoc")
			case "DrawParam":
				attribute, value = "Link", node.attr("Link")
			case "FileLoc":
				if node.parent != nil && node.parent.matchesOFD("Page") && root.matchesOFD("Annotations") {
					value = strings.TrimSpace(editorImportText(data, node))
				}
			}
			if value != "" {
				target := resolveResourcePath(name, base, value)
				if actual := reader.fileNamesFold[strings.ToLower(target)]; actual != "" {
					target = actual
				}
				file.links = append(file.links, editorFontLink{node: node, attribute: attribute, target: target})
				queue = append(queue, target)
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

// apply 隔离其他文档共享的内容XML，再提交字体修改及上层路径引用
// 入参: ctx 取消上下文, changes 按原文件路径保存的修改
// 返回: error 引用扫描或XML修改错误
func (s *editorFontDocument) apply(ctx context.Context, changes map[string][]byte) error {
	shared := make(map[string]bool)
	if s.reader.DocumentCount() > 1 {
		for index := range s.reader.DocumentCount() {
			if index == s.index {
				continue
			}
			files, err := s.reader.documentFiles(ctx, index)
			if err != nil {
				return err
			}
			maps.Copy(shared, files)
		}
	}
	paths := make(map[string]string)
	for name := range changes {
		if shared[strings.ToLower(name)] {
			paths[name] = packageAvailableName(s.reader, changes, name+".font.xml")
		}
	}
	for changed := true; changed; {
		changed = false
		for name, file := range s.files {
			if paths[name] != "" || !shared[strings.ToLower(name)] {
				continue
			}
			for _, link := range file.links {
				if paths[link.target] != "" {
					paths[name] = packageAvailableName(s.reader, changes, name+".font.xml")
					changed = true
					break
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.files)) {
		file := s.files[name]
		if !slices.ContainsFunc(file.links, func(link editorFontLink) bool { return paths[link.target] != "" }) {
			continue
		}
		data, modified := changes[name]
		if !modified {
			data = file.data
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		var patches []editorXMLPatch
		var patch func(*editorXML) error
		patch = func(node *editorXML) error {
			for _, link := range file.links {
				target := paths[link.target]
				if target == "" || node.name != link.node.name {
					continue
				}
				if link.attribute != "" && node.attr(link.attribute) == link.node.attr(link.attribute) {
					fragment, err := editorXMLAttribute(data, node, link.attribute, "/"+target)
					if err != nil {
						return err
					}
					header, err := parseEditorXML(fragment)
					if err != nil {
						return err
					}
					patches = append(patches, editorXMLPatch{node.start, node.open, fragment[:header.open]})
					break
				}
				if link.attribute == "" && editorImportText(data, node) == editorImportText(file.data, link.node) {
					patches = append(patches, editorXMLContent(data, node, editorFontXMLText("/"+target)))
					break
				}
			}
			for _, child := range node.children {
				if err := patch(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := patch(root); err != nil {
			return err
		}
		if len(patches) != 0 {
			data, modified = editorPatchXML(data, patches), true
		}
		if modified {
			changes[name] = data
		}
	}
	rootName := cleanPackagePath(s.reader.OFD.DocBody[s.index].DocRoot)
	if actual := s.reader.fileNamesFold[strings.ToLower(rootName)]; actual != "" {
		rootName = actual
	}
	if target := paths[rootName]; target != "" {
		data, err := s.reader.readFile("OFD.xml")
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		node := root.childAt("DocBody", s.index).child("DocRoot")
		changes[s.reader.fileNamesFold["ofd.xml"]] = editorPatchXML(data, []editorXMLPatch{editorXMLContent(data, node, editorFontXMLText("/"+target))})
	}
	for name, data := range changes {
		if target := paths[name]; target != "" {
			name = target
		}
		s.reader.files[name] = data
	}
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
