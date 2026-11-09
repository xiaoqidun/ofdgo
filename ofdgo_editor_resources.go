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
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"iter"
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

// editorResourceRefs 记录资源标识、文件引用及声明依赖，不以页面是否加载判断资源存活
type editorResourceRefs struct {
	ids         map[string]bool
	files       map[string]bool
	fonts       map[string]*editorFontUsage
	versions    map[string]bool
	documents   map[string]bool
	roles       map[string]int
	definitions map[string]*editorResourceRefs
}

// compactSourceReferences 合并自产页面引用，版本文档按当前版本范围扫描
// 按GB/T33190-2016附录A检查标识和路径引用，保留扫描范围内孤立XML中的引用
// 多文档按文档归集引用，未知扩展或无法解析的XML不清理资源
// 入参: parts 输出条目, progress 保存进度回调, generated 自产页面及引用
// 返回: map[string]bool 可移除条目, error 读取或取消错误
func (e *Editor) compactSourceReferences(parts map[string][]byte, progress editorProgress, generated map[string]editorGeneratedReferences) (map[string]bool, error) {
	return e.compactResourceReferences(parts, e.source.reader, progress, generated, e.source.retiredAttachments)
}

// compactResourceReferences 按文档引用清理输出副本，兼顾新建文档与已有包
// 入参: parts 输出条目, reader 原包，可为空, progress 保存进度回调, generated 自产引用, retired 待移除附件
// 返回: map[string]bool 可移除条目, error 读取或取消错误
func (e *Editor) compactResourceReferences(parts map[string][]byte, reader *Reader, progress editorProgress, generated map[string]editorGeneratedReferences, retired map[string]bool) (map[string]bool, error) {
	multiple := reader != nil && reader.DocumentCount() > 1
	prune := e.output != nil && e.output.options.Mode != CompressionUnchanged
	if e.output != nil && e.output.protected {
		return nil, nil
	}
	if !multiple && len(parts) == 0 && e.revision == 0 && (e.output == nil || e.output.options.Mode == CompressionUnchanged) {
		return nil, nil
	}
	generated = e.generatedVectorReferences(parts, generated)
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	if multiple {
		protected, err := reader.hasOutputSignatures(ctx)
		if err != nil || protected {
			return nil, err
		}
	}
	progress = progress.withContext(ctx)
	_, scope, err := reader.versionResourceFiles(ctx, parts, generated)
	if err != nil {
		if multiple && !prune {
			return nil, ctx.Err()
		}
		return nil, err
	}
	names := make(map[string]bool)
	if reader != nil {
		for name := range reader.fileIndex {
			names[name] = true
		}
		for name := range reader.files {
			names[name] = true
		}
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
	refs, resources, references, safe, err := e.scanOutputResources(ctx, reader, parts, generated, names, progress, multiple || prune)
	if err != nil {
		return nil, err
	}
	if !safe {
		return nil, nil
	}
	var scoped map[string]*editorResourceRefs
	if prune {
		for _, name := range resources {
			data, ok := parts[name]
			if !ok {
				data, err = reader.readFileViewContext(ctx, name)
				if err != nil {
					return nil, err
				}
			}
			indexed, err := editorResourceDefinitions(ctx, data, name, references[name], refs.versions[cleanPackagePath(name)])
			if err != nil || indexed == nil {
				return nil, err
			}
			references[name] = indexed
		}
	}
	if multiple || prune {
		selected := ""
		if reader != nil && reader.OFD.DocBody[reader.documentIndex].versioned {
			selected, err = reader.DocumentRootContext(ctx)
			if err != nil {
				return nil, err
			}
		}
		scoped, err = resourceReferenceScopes(ctx, refs.documents, resources, references, selected)
		if err != nil || scoped == nil {
			return nil, err
		}
	}
	ordered := slices.Sorted(maps.Keys(names))
	removed := maps.Clone(retired)
	if removed == nil {
		removed = make(map[string]bool)
	}
	var usedFiles map[string]bool
	var protectedProfiles map[string]bool
	var profiles map[string][]editorProfileReference
	if prune {
		usedFiles = make(map[string]bool)
		protectedProfiles = make(map[string]bool)
		profiles = make(map[string][]editorProfileReference)
		for name, source := range references {
			if source.definitions == nil {
				maps.Copy(usedFiles, source.files)
				maps.Copy(protectedProfiles, source.files)
				continue
			}
			usage := scoped[name]
			for id, definition := range source.definitions {
				if refs.versions[cleanPackagePath(name)] || usage == nil || usage.ids[id] {
					maps.Copy(usedFiles, definition.files)
					if refs.versions[cleanPackagePath(name)] || usage == nil {
						maps.Copy(protectedProfiles, definition.files)
					}
				}
			}
		}
	} else {
		usedFiles = maps.Clone(refs.files)
	}
	updates := make(map[string][]byte)
	fontFiles := make(map[string]*editorFontUsage)
	for i, name := range resources {
		if err := progress.report("resources", i, len(resources)); err != nil {
			return nil, err
		}
		data, ok := parts[name]
		if !ok {
			var err error
			data, err = reader.readFileViewContext(ctx, name)
			if err != nil {
				return nil, err
			}
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, ctx.Err()
		}
		usageRefs := refs
		if multiple || prune {
			usageRefs = scoped[name]
		}
		preserved := refs.versions[cleanPackagePath(name)] || usageRefs == nil
		var patches []editorXMLPatch
		for _, group := range root.children {
			var members []editorXMLPatch
			for _, node := range group.children {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				field := ""
				if group.name.Local == "Fonts" && node.name.Local == "Font" {
					field = "FontFile"
				} else if group.name.Local == "MultiMedias" && node.name.Local == "MultiMedia" {
					field = "MediaFile"
				}
				if field == "" && (!prune || node.name.Local != editorResourceElement(group.name.Local)) {
					continue
				}
				id := editorResourceID(node.attr("ID"))
				keep := preserved || id == "" || usageRefs.ids[id] || field == "MediaFile" && node.attr("Type") != "Image"
				profile := prune && group.name.Local == "ColorSpaces" && node.name.Local == "ColorSpace"
				if prune && keep && !profile {
					maps.Copy(protectedProfiles, references[name].definitions[id].files)
				}
				var value string
				if file := node.child(field); file != nil {
					if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
						return nil, nil
					}
				} else if profile {
					value = node.attr("Profile")
				}
				if location := editorResourceLocation(name, root.attr("BaseLoc"), value); location != "" {
					alternate := cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))
					if field == "FontFile" && !multiple && keep {
						var usage *editorFontUsage
						if usageRefs != nil {
							usage = usageRefs.fonts[id]
						}
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
						if prune {
							if profile && !preserved && location == alternate {
								profiles[location] = append(profiles[location], editorProfileReference{index: name, id: id})
							} else {
								protectedProfiles[location] = true
								protectedProfiles[alternate] = true
							}
						}
					} else {
						removed[location] = true
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
	var fontCache editorSourceFontCache
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		subset, err := fontCache.subset(ctx, e.backends.FontResources, data, usage)
		if err != nil {
			return nil, fmt.Errorf("subset font %s: %w", name, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
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
	if err := compactResourceProfiles(ctx, reader, parts, profiles, protectedProfiles, removed); err != nil {
		return nil, err
	}
	return removed, nil
}

// scan 流式读取包内XML引用，二进制条目只探测文件头，不展开页面对象或图片像素
// 入参: input 文件流, name 包内路径
// 返回: bool 是否资源索引, bool 是否可完整判断引用
func (r *editorResourceRefs) scan(input io.Reader, name string) (bool, bool) {
	return r.scanContext(nil, input, name)
}

// scanContext 流式收集资源引用，在读取和字体用字处理期间检查取消
// 入参: ctx 取消上下文，可为nil, input 文件流, name 包内路径
// 返回: bool 是否资源索引, bool 是否完整收集引用，取消时为false
func (r *editorResourceRefs) scanContext(ctx context.Context, input io.Reader, name string) (bool, bool) {
	if ctx != nil {
		if ctx.Err() != nil {
			return false, false
		}
		input = imageInput{ReadCloser: io.NopCloser(input), ctx: ctx}
	}
	buffer := bufio.NewReader(input)
	found, err := editorXMLPrefix(buffer)
	if err != nil {
		return false, false
	}
	if !found {
		return false, !strings.EqualFold(path.Ext(name), ".xml")
	}
	decoder := xml.NewDecoder(buffer)
	scan := editorReferenceScan{ctx: ctx, refs: r, name: name, safe: true}
	for {
		if ctx != nil && ctx.Err() != nil {
			return false, false
		}
		token, err := decoder.Token()
		if err == io.EOF {
			return scan.resource, scan.seen && len(scan.stack) == 0
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
	if kind == editorReferenceID {
		if id := editorResourceID(value); id != "" {
			r.ids[id] = true
		}
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
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
	id, err := strconv.ParseUint(strings.Trim(value, " \t\r\n"), 10, 32)
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

// editorResourceNode 按资源标识查找标准资源节点并检查别名冲突
// 入参: root 资源文件根节点, group 资源集合名称, kind 资源类型, id 资源标识
// 返回: *editorXML 资源节点，未找到时为nil
func editorResourceNode(root *editorXML, group, kind, id string) *editorXML {
	nodes := make(map[string]*editorXML)
	for node := range editorResourceNodes(root, group, kind) {
		nodes[node.attr("ID")] = node
	}
	node, _ := resourceValue(nodes, id)
	return node
}

// editorResourceNodes 按原顺序遍历同类资源，包含交错出现的重复集合
// 入参: root 资源文件根节点, group 集合名称, kind 资源类型
// 返回: iter.Seq[*editorXML] 资源节点序列
func editorResourceNodes(root *editorXML, group, kind string) iter.Seq[*editorXML] {
	return func(yield func(*editorXML) bool) {
		for _, collection := range root.children {
			if !collection.matchesOFD(group) {
				continue
			}
			for _, node := range collection.children {
				if node.matchesOFD(kind) && !yield(node) {
					return
				}
			}
		}
	}
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
