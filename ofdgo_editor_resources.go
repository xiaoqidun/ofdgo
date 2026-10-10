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
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// 资源引用类型，区分无引用、标识引用和文件引用
const (
	editorReferenceNone = iota
	editorReferenceID
	editorReferenceFile
)

// editorResourceRefs 保存全包资源标识和直接文件引用，不以页面是否加载判断资源存活
type editorResourceRefs struct {
	ids      map[string]bool
	files    map[string]bool
	fonts    map[string]*editorFontUsage
	versions map[string]bool
}

// compactSourceReferences 合并自产页面引用，版本文档按当前版本范围扫描
// 按GB/T33190-2016附录A检查标识和路径引用，保留扫描范围内孤立XML中的引用
// 多文档、未知扩展或无法解析的XML不清理资源，避免误删共享或无法确认用途的条目
// 入参: parts 输出条目, progress 保存进度回调, generated 自产页面及引用
// 返回: map[string]bool 可移除条目, error 读取或取消错误
func (e *Editor) compactSourceReferences(parts map[string][]byte, progress editorProgress, generated map[string]editorGeneratedReferences) (map[string]bool, error) {
	if e.source.reader.DocumentCount() > 1 || e.output != nil && e.output.protected {
		return nil, nil
	}
	if len(parts) == 0 && e.revision == 0 && (e.output == nil || e.output.options.Mode == CompressionUnchanged) {
		return nil, nil
	}
	generated = e.generatedVectorReferences(parts, generated)
	reader := e.source.reader
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	scope, err := reader.versionResourceFiles(ctx, parts, generated)
	if err != nil {
		return nil, err
	}
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
	if scope != nil {
		for name := range names {
			if !scope[cleanPackagePath(name)] {
				delete(names, name)
			}
		}
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
		data, replaced := parts[name]
		if known, ok := generated[name]; ok && replaced && known.refs != nil && known.matches(data) {
			refs.merge(known.refs)
			continue
		}
		var input io.ReadCloser
		if replaced {
			if known := generated[name]; data == nil && known.staged != nil {
				input = known.staged.open()
			} else {
				input = io.NopCloser(bytes.NewReader(data))
			}
		} else {
			var err error
			input, err = reader.openFile(name)
			if err != nil {
				return nil, err
			}
		}
		if e.output != nil {
			input = imageInput{ReadCloser: input, context: e.output.ctx}
		}
		resource, safe := refs.scan(input, name)
		input.Close()
		if e.output != nil && e.output.ctx.Err() != nil {
			return nil, e.output.ctx.Err()
		}
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
	removed := maps.Clone(e.source.retiredAttachments)
	if removed == nil {
		removed = make(map[string]bool)
	}
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
		preserved := refs.versions[cleanPackagePath(name)]
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
				keep := preserved || id == "" || refs.ids[id] || field == "MediaFile" && node.attr("Type") != "Image"
				if file := node.child(field); file != nil {
					var value string
					if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
						return nil, nil
					}
					if location := editorResourceLocation(name, root.attr("BaseLoc"), value); location != "" {
						alternate := cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))
						if field == "FontFile" {
							usage := refs.fonts[id]
							charset := node.attr("Charset")
							unsafe := preserved || id == "" || charset != "" && !strings.EqualFold(charset, "unicode") || location != alternate
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
		key := cleanPackagePath(name)
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
		if removed[cleanPackagePath(name)] {
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
	scan := editorReferenceScan{refs: r, name: name, safe: true}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return scan.resource, len(scan.stack) == 0
		}
		if err != nil {
			return false, false
		}
		if !scan.accept(token) {
			return false, false
		}
	}
}

// reference 记录标准资源标识和路径引用，不将普通数字或资源自身ID视为引用
// 入参: name 当前文件, base 资源目录, key 字段, value 值, resource 是否资源索引
func (r *editorResourceRefs) reference(name, base, key, value string, resource bool) {
	kind := editorResourceReferenceKind(key, resource)
	if kind == editorReferenceNone {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if kind == editorReferenceID {
		if id := editorResourceID(value); id != "" {
			r.ids[id] = true
		}
		return
	}
	r.files[editorResourceLocation(name, base, value)] = true
	r.files[cleanPackagePath(resolveResourcePath(name, base, value))] = true
}

// editorResourceReferenceKind 区分标准标识和文件引用，其他文本不参与资源扫描
// 入参: key 字段名称, resource 是否资源索引
// 返回: int 引用类型
func editorResourceReferenceKind(key string, resource bool) int {
	switch key {
	case "Font", "ResourceID", "Substitution", "ImageMask", "Relative", "DrawParam", "ColorSpace", "DefaultCS", "Thumbnail", "TemplateID", "PageID", "PageRef", "RefId":
		return editorReferenceID
	case "DocRoot", "Signatures", "Cover", "PublicRes", "DocumentRes", "PageRes", "Annotations", "Attachments", "FileLoc", "FileRef", "Profile", "SignedValue", "SchemaLoc", "ExtendData", "File":
		return editorReferenceFile
	case "BaseLoc":
	case "FontFile", "MediaFile":
	default:
		return editorReferenceNone
	}
	if resource {
		return editorReferenceNone
	}
	return editorReferenceFile
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

// normalizeResourceReferences 合并数值等价的资源引用，保留内部非数字标识
// 入参: references 待规范化的引用集合
func normalizeResourceReferences(references map[string]bool) {
	for id, used := range references {
		if key := editorResourceID(id); key != "" && key != id {
			references[key] = references[key] || used
			delete(references, id)
		}
	}
}

// editorResourceNode 按资源标识查找标准资源节点，等价别名冲突时不猜测
// 入参: root 资源文件根节点, group 资源集合名称, kind 资源类型, id 资源标识
// 返回: *editorXML 资源节点，未找到时为nil
func editorResourceNode(root *editorXML, group, kind, id string) *editorXML {
	nodes := make(map[string]*editorXML)
	if collection := root.child(group); collection != nil {
		for _, node := range collection.children {
			if node.matchesOFD(kind) {
				nodes[node.attr("ID")] = node
			}
		}
	}
	node, _ := resourceValue(nodes, id)
	return node
}

// editorResourceLocation 按ST_Loc与Res.BaseLoc解析路径，保留路径大小写
// 入参: name 所在XML, base 资源目录, value 路径值
// 返回: string 包内路径
func editorResourceLocation(name, base, value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/") {
		return cleanPackagePath(value)
	}
	base = strings.ReplaceAll(base, "\\", "/")
	dir := path.Dir(name)
	if strings.HasPrefix(base, "/") {
		dir = cleanPackagePath(base)
	} else {
		dir = path.Join(dir, base)
	}
	return cleanPackagePath(path.Join(dir, value))
}
