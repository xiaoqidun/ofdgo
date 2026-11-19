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
	reader       *Reader
	index        int
	files        map[string]*editorFontXML
	fonts        map[string]*editorFontDeclaration
	duplicateIDs map[string]bool
	texts        map[string][]editorFontText
	fontCache    editorBindingFontCache
	fontReader   *Reader
	unsafe       bool
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
	if err := next.initRootContext(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	*s.reader = next
	return nil
}

// editorFontXML 保存字体声明及文字引用所在的XML
type editorFontXML struct {
	name string
	data []byte
}

// editorFontText 关联文字节点及其原始文件，按字体标识归集
type editorFontText struct {
	file *editorFontXML
	node *editorXML
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
	return scanEditorFontFiles(ctx, reader, index, nil)
}

// scanEditorFontFiles 扫描字体与内容引用，按需复用当前文件的XML树，不额外保留节点树
// 入参: ctx 取消上下文, reader 包快照, index 文档索引, inspect 可选的文件节点访问器
// 返回: *editorFontDocument 字体引用, error 读取、结构或访问错误
func scanEditorFontFiles(ctx context.Context, reader *Reader, index int, inspect func(*editorFontXML, *editorXML) error) (*editorFontDocument, error) {
	return scanEditorFontFilesMode(ctx, reader, index, inspect, true, nil)
}

// scanEditorFontFilesMode 按需保留文字节点及原文，只读访问在扫描后释放非资源文件原文
// 入参: ctx 取消上下文, reader 包快照, index 文档索引, inspect 可选的文件节点访问器, retain 是否保留编辑数据, stream 可选的只读访问器
// 返回: *editorFontDocument 扫描结果, error 读取、结构或访问错误
func scanEditorFontFilesMode(ctx context.Context, reader *Reader, index int, inspect func(*editorFontXML, *editorXML) error, retain bool, stream func(*editorFontXML) (bool, bool, []string, error)) (*editorFontDocument, error) {
	scan := &editorFontDocument{reader: reader, index: index, files: make(map[string]*editorFontXML), fonts: make(map[string]*editorFontDeclaration), duplicateIDs: make(map[string]bool), texts: make(map[string][]editorFontText)}
	entry, err := reader.documentEntryContext(ctx, index)
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
		data, err := reader.readFileViewContext(ctx, name)
		if err != nil {
			return nil, err
		}
		file := &editorFontXML{name: name, data: data}
		if !retain && stream != nil {
			handled, unsafe, links, err := stream(file)
			if err != nil {
				return nil, err
			}
			if handled {
				scan.files[name] = file
				scan.unsafe = scan.unsafe || unsafe
				queue = append(queue, links...)
				file.data = nil
				continue
			}
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		scan.files[name] = file
		base := ""
		var fileFonts map[string]bool
		if root.matchesOFD("Res") {
			base = root.attr("BaseLoc")
			fileFonts = make(map[string]bool)
		}
		var visit func(*editorXML) error
		visit = func(node *editorXML) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if classifyOFDNamespace(node.name.Space) == ofdXMLUnknown {
				scan.unsafe = true
				return nil
			}
			if node.matchesOFD("Attachments") || node.matchesOFD("Signatures") {
				return nil
			}
			if slices.Contains([]string{"CustomTags", "Extensions", "ExtendData", "Data"}, node.name.Local) {
				if node.matchesOFD("CustomTags") {
					var safe bool
					if stream == nil {
						safe, err = richTextTagsFontSafe(ctx, reader, index)
					} else {
						safe, err = customTagsResourceSafe(ctx, reader, index)
					}
					if err != nil {
						return err
					}
					if safe {
						return nil
					}
				}
				scan.unsafe = true
				return nil
			}
			for _, attr := range node.attrs {
				if attr.Name.Space != "" && attr.Name.Space != "xmlns" && attr.Name.Space != XMLNamespace {
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
				if id == "" || fileFonts[id] {
					return fmt.Errorf("missing or duplicate font ID %q", font.ID)
				}
				fileFonts[id] = true
				location := ""
				if font.FontFile != "" {
					location = resolveResourcePath(name, base, font.FontFile)
				}
				if scan.fonts[id] != nil {
					scan.duplicateIDs[id] = true
				} else {
					scan.fonts[id] = &editorFontDeclaration{font: font, file: file, node: node, location: location}
				}
			}
			if node.matchesOFD("TextObject") || node.matchesOFD("Text") && node.parent != nil && node.parent.matchesOFD("Area") {
				id := editorResourceID(node.attr("Font"))
				if retain {
					scan.texts[id] = append(scan.texts[id], editorFontText{file: file, node: node})
				}
				if id == "" {
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
		if inspect != nil {
			if err := inspect(file, root); err != nil {
				return nil, err
			}
		}
		if !retain && !root.matchesOFD("Res") {
			file.data = nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return scan, nil
}

// editorFontXMLText 编码资源路径文本，不改变包内名称
// 入参: value 原始文本
// 返回: []byte XML文本
func editorFontXMLText(value string) []byte {
	var data bytes.Buffer
	_ = xml.EscapeText(&data, []byte(value))
	return data.Bytes()
}

// customTagsResourceSafe 按已知命名空间白名单核验自定义标引，未知行业命名空间不推定其资源引用语义
// 入参: ctx 取消上下文, reader 包阅读器, index 文档索引
// 返回: bool 是否满足已知命名空间资源引用完整性, error 读取或取消错误
func customTagsResourceSafe(ctx context.Context, reader *Reader, index int) (bool, error) {
	view, err := reader.DocumentContext(ctx, index)
	if err != nil {
		return false, err
	}
	defer view.Close()
	tags, err := view.CustomTagsContext(ctx)
	if err != nil {
		return false, err
	}
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if strings.TrimSpace(tag.FileLoc) == "" || !isKnownResourceSafeTagMeta(tag) {
			return false, nil
		}
		if tag.SchemaLoc != "" {
			schemaFile, err := view.openFile(view.ResPath(tag.SchemaLoc))
			if err != nil {
				return false, nil
			}
			_ = schemaFile.Close()
		}
		structure, err := view.readStructureContext(ctx, tag)
		if err != nil {
			return false, ctx.Err()
		}
		if !knownResourceSafeCustomTag(tag, structure.Root) {
			return false, nil
		}
	}
	return true, ctx.Err()
}
