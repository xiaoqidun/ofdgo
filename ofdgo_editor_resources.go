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
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// editorResourceRefs 保存全包资源标识和直接文件引用，不以页面是否加载判断资源存活
type editorResourceRefs struct {
	ids   map[string]bool
	files map[string]bool
	fonts map[string]*editorFontUsage
}

// compactSourceResources 在保存副本中清理无引用字体和图片，并裁剪可完整确认用字的原有字体
// 按GB/T 33190-2016附录A检查标识和路径引用，包括模板、注释、底纹、裁剪、版本和其他文档
// 全包检查保留孤立XML中的引用；未知命名空间、扩展数据或无法解析的XML使本次清理跳过
// 入参: parts 已修改和新增的包内条目, progress 保存进度回调
// 返回: map[string]bool 可移除的二进制条目, error 读取错误
func (e *Editor) compactSourceResources(parts map[string][]byte, progress editorProgress) (map[string]bool, error) {
	if len(parts) == 0 && e.revision == 0 {
		return nil, nil
	}
	reader := e.source.reader
	names := make(map[string]bool)
	for name := range reader.fileIndex {
		names[name] = true
	}
	for name := range reader.files {
		names[name] = true
	}
	for name := range parts {
		names[name] = true
	}
	refs := editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
	var resources []string
	ordered := slices.Sorted(maps.Keys(names))
	for i, name := range ordered {
		if err := progress.report("references", i, len(ordered)); err != nil {
			return nil, err
		}
		if strings.HasSuffix(name, "/") {
			continue
		}
		var input io.ReadCloser
		if data, ok := parts[name]; ok {
			input = io.NopCloser(bytes.NewReader(data))
		} else {
			var err error
			input, err = reader.openFile(name)
			if err != nil {
				return nil, err
			}
		}
		resource, safe := refs.scan(input, name)
		input.Close()
		if !safe {
			return nil, nil
		}
		if resource {
			resources = append(resources, name)
		}
	}
	if err := progress.report("references", len(ordered), len(ordered)); err != nil {
		return nil, err
	}
	removed := make(map[string]bool)
	usedFiles := maps.Clone(refs.files)
	updates := make(map[string][]byte)
	fontFiles := make(map[string]*editorFontUsage)
	for i, name := range resources {
		if err := progress.report("resources", i, len(resources)); err != nil {
			return nil, err
		}
		data, ok := parts[name]
		if !ok {
			var err error
			data, err = reader.readFile(name)
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, nil
		}
		var patches []editorXMLPatch
		for _, group := range root.children {
			var members []editorXMLPatch
			for _, node := range group.children {
				field := ""
				if group.name.Local == "Fonts" && node.name.Local == "Font" {
					field = "FontFile"
				} else if group.name.Local == "MultiMedias" && node.name.Local == "MultiMedia" {
					field = "MediaFile"
				}
				if field == "" {
					continue
				}
				id := editorResourceID(node.attr("ID"))
				keep := id == "" || refs.ids[id] || field == "MediaFile" && node.attr("Type") != "Image"
				if file := node.child(field); file != nil {
					var value string
					if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
						return nil, nil
					}
					if location := editorResourceLocation(name, root.attr("BaseLoc"), value); location != "" {
						alternate := strings.ToLower(cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value)))
						if field == "FontFile" {
							usage := refs.fonts[id]
							charset := node.attr("Charset")
							unsafe := id == "" || charset != "" && !strings.EqualFold(charset, "unicode") || location != alternate
							for _, file := range []string{location, alternate} {
								if fontFiles[file] == nil {
									fontFiles[file] = newEditorFontUsage()
								}
								fontFiles[file].merge(usage, unsafe || keep && usage == nil)
							}
						}
						if keep {
							usedFiles[location] = true
							usedFiles[alternate] = true
						} else {
							removed[location] = true
						}
					}
				}
				if !keep {
					members = append(members, editorXMLPatch{start: node.start, end: node.end})
				}
			}
			if len(members) > 0 && len(members) == len(group.children) {
				patches = append(patches, editorXMLPatch{start: group.start, end: group.end})
			} else {
				patches = append(patches, members...)
			}
		}
		if len(patches) != 0 {
			updates[name] = editorPatchXML(data, patches)
		}
	}
	if err := progress.report("resources", len(resources), len(resources)); err != nil {
		return nil, err
	}
	for name := range usedFiles {
		delete(removed, name)
	}
	for i, name := range ordered {
		if err := progress.report("fonts", i, len(ordered)); err != nil {
			return nil, err
		}
		key := strings.ToLower(cleanPackagePath(name))
		usage := fontFiles[key]
		if e.backends.FontResources == nil || usage == nil || usage.unsafe || refs.files[key] || removed[key] || parts[name] != nil {
			continue
		}
		data, err := reader.readFile(name)
		if err != nil {
			return nil, err
		}
		subset, err := e.backends.FontResources.SubsetSourceFont(data, FontUsage{Characters: slices.Sorted(maps.Keys(usage.chars)), Glyphs: slices.Sorted(maps.Keys(usage.glyphs)), Unsafe: usage.unsafe})
		if err != nil {
			return nil, fmt.Errorf("subset font %s: %w", name, err)
		}
		if subset != nil {
			updates[name] = subset
		}
	}
	if err := progress.report("fonts", len(ordered), len(ordered)); err != nil {
		return nil, err
	}
	maps.Copy(parts, updates)
	for name := range parts {
		if removed[strings.ToLower(cleanPackagePath(name))] {
			delete(parts, name)
		}
	}
	return removed, nil
}

// scan 流式读取包内XML引用，二进制条目只探测文件头，不展开页面对象或图片像素
// 入参: input 文件流, name 包内路径
// 返回: bool 是否资源索引, bool 是否可完整判断引用
func (r *editorResourceRefs) scan(input io.Reader, name string) (bool, bool) {
	buffer := bufio.NewReader(input)
	header, err := buffer.Peek(512)
	if err != nil && err != io.EOF {
		return false, false
	}
	header = bytes.TrimSpace(bytes.TrimPrefix(header, []byte{0xef, 0xbb, 0xbf}))
	if len(header) == 0 {
		return false, err == io.EOF && !strings.EqualFold(path.Ext(name), ".xml")
	}
	if header[0] != '<' {
		return false, !strings.EqualFold(path.Ext(name), ".xml")
	}
	decoder := xml.NewDecoder(buffer)
	var stack []string
	var fonts []*editorFontUsage
	var text strings.Builder
	resource, base := false, ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return resource, len(stack) == 0
		}
		if err != nil {
			return false, false
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Space != "" && token.Name.Space != ofdNamespace {
				return false, false
			}
			if token.Name.Local == "CustomTags" || token.Name.Local == "Extensions" || token.Name.Local == "ExtendData" || token.Name.Local == "Data" {
				return false, false
			}
			if len(stack) == 0 {
				resource = token.Name.Local == "Res"
				if resource {
					for _, attr := range token.Attr {
						if attr.Name.Local == "BaseLoc" {
							base = attr.Value
						}
					}
				}
			}
			var usage *editorFontUsage
			if len(fonts) != 0 {
				usage = fonts[len(fonts)-1]
				if usage != nil && (stack[len(stack)-1] == "TextCode" || stack[len(stack)-1] == "Glyphs") {
					usage.unsafe = true
				}
			}
			for _, attr := range token.Attr {
				if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" || attr.Name.Space == "http://www.w3.org/XML/1998/namespace" {
					continue
				}
				if attr.Name.Space != "" {
					return false, false
				}
				if attr.Name.Local != "ID" {
					r.reference(name, base, attr.Name.Local, attr.Value, resource)
				}
				if attr.Name.Local == "Font" {
					usage = r.fontUsage(attr.Value)
					if token.Name.Local != "TextObject" && token.Name.Local != "Text" {
						usage.unsafe = true
					}
				} else if attr.Name.Local == "Substitution" {
					r.fontUsage(attr.Value).unsafe = true
				}
			}
			stack = append(stack, token.Name.Local)
			fonts = append(fonts, usage)
			text.Reset()
		case xml.CharData:
			text.Write(token)
		case xml.EndElement:
			r.reference(name, base, token.Name.Local, text.String(), resource)
			if (token.Name.Local == "Substitution" || token.Name.Local == "Font") && strings.TrimSpace(text.String()) != "" {
				r.fontUsage(text.String()).unsafe = true
			}
			if usage := fonts[len(fonts)-1]; usage != nil {
				usage.text(token.Name.Local, text.String())
			}
			stack = stack[:len(stack)-1]
			fonts = fonts[:len(fonts)-1]
			text.Reset()
		case xml.Directive:
			return false, false
		}
	}
}

// reference 记录标准资源标识和路径引用，不将普通数字或资源自身ID视为引用
// 入参: name 当前文件, base 资源目录, key 字段, value 值, resource 是否资源索引
func (r *editorResourceRefs) reference(name, base, key, value string, resource bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	switch key {
	case "Font", "ResourceID", "Substitution", "ImageMask", "Relative", "DrawParam", "ColorSpace", "DefaultCS", "Thumbnail", "TemplateID", "PageID", "PageRef", "RefId":
		if id := editorResourceID(value); id != "" {
			r.ids[id] = true
		}
		return
	case "DocRoot", "Signatures", "Cover", "PublicRes", "DocumentRes", "PageRes", "Annotations", "Attachments", "FileLoc", "FileRef", "Profile", "SignedValue", "SchemaLoc", "ExtendData", "File":
	case "BaseLoc":
		if resource {
			return
		}
	case "FontFile", "MediaFile":
		if resource {
			return
		}
	default:
		return
	}
	r.files[editorResourceLocation(name, base, value)] = true
	r.files[strings.ToLower(cleanPackagePath(resolveResourcePath(name, base, value)))] = true
}

// editorResourceID 规范化ST_ID与ST_RefID的十进制表示
// 入参: value 标识文本
// 返回: string 标准十进制标识，无效时为空
func editorResourceID(value string) string {
	id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil || id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 10)
}

// editorResourceLocation 按ST_Loc与Res.BaseLoc解析路径，大小写折叠用于保守保留共享文件
// 入参: name 所在XML, base 资源目录, value 路径值
// 返回: string 包内路径
func editorResourceLocation(name, base, value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/") {
		return strings.ToLower(cleanPackagePath(value))
	}
	base = strings.ReplaceAll(base, "\\", "/")
	dir := path.Dir(name)
	if strings.HasPrefix(base, "/") {
		dir = cleanPackagePath(base)
	} else {
		dir = path.Join(dir, base)
	}
	return strings.ToLower(cleanPackagePath(path.Join(dir, value)))
}
