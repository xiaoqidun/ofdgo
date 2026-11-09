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
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
)

// outputImageKey 区分相同图像的压缩配置和像素需求，不跨写出保留状态
type outputImageKey struct {
	hash    [32]byte
	options CompressionOptions
	size    image.Point
}

// editorProfileReference 记录颜色配置文件所在的资源索引及颜色空间标识
type editorProfileReference struct {
	index string
	id    string
}

// compressResourceParts 优化输出图片并合并相同资源文件，保留资源标识及各自属性
// 入参: parts 输出改动, reader 原包，可为空, removed 已移除条目
// 返回: error 读取或取消错误
func (e *Editor) compressResourceParts(parts map[string][]byte, reader *Reader, removed map[string]bool) error {
	if reader == nil && e.output != nil && e.output.options.Mode != CompressionUnchanged && !e.output.protected {
		unused, err := e.compactResourceReferences(parts, nil, editorProgress(e.OnWriteProgress), nil, nil)
		if err != nil {
			return err
		}
		maps.Copy(removed, unused)
	}
	return e.compressResourceReferences(parts, reader, removed, nil)
}

// compressResourceReferences 优化资源，复用自产页面引用并流式读取暂存页面
// 版本文件隔离当前入口，其他文档仅优化未受版本保护的资源；结果仅在本次写出内复用
// 入参: parts 输出改动, reader 原包, removed 已移除条目, generated 本次自产页面及引用
// 返回: error 读取或取消错误
func (e *Editor) compressResourceReferences(parts map[string][]byte, reader *Reader, removed map[string]bool, generated map[string]editorGeneratedReferences) error {
	if e.output == nil || e.output.options.Mode == CompressionUnchanged || e.output.protected {
		return nil
	}
	generated = e.generatedVectorReferences(parts, generated)
	selected, scope, err := reader.versionResourceFiles(e.output.ctx, parts, generated)
	if err != nil {
		return err
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
	read := func(name string) ([]byte, error) {
		if err := e.output.ctx.Err(); err != nil {
			return nil, err
		}
		if data, ok := parts[name]; ok {
			return data, nil
		}
		return reader.readFileViewContext(e.output.ctx, name)
	}
	actual := make(map[string]string)
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		key := cleanPackagePath(name)
		if removed[key] || strings.HasSuffix(name, "/") {
			delete(names, name)
			continue
		}
		if prior, ok := actual[key]; ok && prior != name {
			return nil
		}
		actual[key] = name
		if scope != nil && !scope[key] {
			delete(names, name)
			continue
		}
	}
	collect := reader != nil && reader.DocumentCount() > 1 && e.backends.FontResources != nil
	refs, resources, references, safe, err := e.scanOutputResources(e.output.ctx, reader, parts, generated, names, nil, collect)
	if err != nil || !safe {
		return err
	}
	if selected != nil && reader.DocumentCount() > 1 {
		preserved, err := reader.versionPreservedFiles(e.output.ctx)
		if err != nil {
			return err
		}
		for name := range preserved {
			if !selected[name] {
				refs.files[name], refs.versions[name] = true, true
			}
		}
	}
	type resourceFile struct{ key, role string }
	type resourceScope struct {
		role     string
		selected bool
	}
	type resourceSize struct {
		role string
		size uint64
	}
	files := make(map[resourceFile]bool)
	for _, name := range resources {
		data, err := read(name)
		if err != nil {
			return err
		}
		root, err := parseEditorXMLContext(e.output.ctx, data)
		if err != nil {
			return err
		}
		preserved := refs.versions[cleanPackagePath(name)]
		for _, group := range root.children {
			for _, node := range group.children {
				if err := e.output.ctx.Err(); err != nil {
					return err
				}
				field := ""
				if group.name.Local == "Fonts" && node.name.Local == "Font" {
					field = "FontFile"
				}
				if group.name.Local == "MultiMedias" && node.name.Local == "MultiMedia" {
					field = "MediaFile"
				}
				if field == "" {
					continue
				}
				file := node.child(field)
				if file == nil {
					continue
				}
				var value string
				if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
					return err
				}
				key := editorResourceLocation(name, root.attr("BaseLoc"), value)
				if selected != nil && !selected[cleanPackagePath(name)] && selected[key] {
					continue
				}
				if preserved || field == "MediaFile" && node.attr("Type") != "Image" {
					refs.files[key] = true
					continue
				}
				if key != cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value)) {
					return nil
				}
				if actual[key] != "" && !refs.files[key] {
					files[resourceFile{key, field}] = true
				}
			}
		}
	}
	var fontUsage map[string]*editorFontUsage
	if references != nil {
		fontUsage, err = reader.compressionFontUsage(e.output.ctx, refs.documents, resources, references, read)
		if err != nil {
			return err
		}
	}
	aliases := make(map[string]string)
	locations := maps.Clone(actual)
	reserved := make(map[string]bool, len(actual))
	for key := range actual {
		reserved[key] = true
	}
	formats := make(map[string]string)
	seen := make(map[resourceScope]map[[32]byte]string)
	imageResults := make(map[outputImageKey][]byte)
	imageDigests := make(map[outputImageKey][32]byte)
	imageSources := make(map[string]outputImageSource)
	ordered := slices.Sorted(maps.Keys(actual))
	sizes := make(map[string]uint64, len(files))
	largeCounts := make(map[resourceSize]int)
	var imageKeys []string
	for _, key := range ordered {
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		font, media := files[resourceFile{key, "FontFile"}], files[resourceFile{key, "MediaFile"}]
		if refs.files[key] || font == media {
			continue
		}
		size, err := outputResourceSize(reader, parts, actual[key])
		if err != nil {
			return err
		}
		sizes[key] = size
		if size > outputOptimizationBufferLimit {
			role := "MediaFile"
			if font {
				role = "FontFile"
			}
			largeCounts[resourceSize{role, size}]++
		} else if media {
			imageKeys = append(imageKeys, key)
		}
	}
	load := func(key string) ([]byte, error) {
		if sizes[key] > outputOptimizationBufferLimit {
			return nil, nil
		}
		return read(actual[key])
	}
	if err := e.prepareOutputImages(imageKeys, load, imageResults, imageSources); err != nil {
		return err
	}
	var fontCache editorSourceFontCache
	var hashBuffer []byte
	for _, key := range ordered {
		if refs.files[key] || files[resourceFile{key, "FontFile"}] && files[resourceFile{key, "MediaFile"}] {
			continue
		}
		for _, role := range []string{"FontFile", "MediaFile"} {
			if !files[resourceFile{key, role}] {
				continue
			}
			if err := editorProgress(e.OnWriteProgress).withContext(e.output.ctx).report("compress", e.output.completed, 0); err != nil {
				return err
			}
			if size := sizes[key]; size > outputOptimizationBufferLimit && largeCounts[resourceSize{role, size}] < 2 {
				continue
			}
			source, prefetched := imageSources[key]
			usage := fontUsage[key]
			subset := role == "FontFile" && usage != nil && !usage.unsafe && e.backends.FontResources != nil
			var data []byte
			optimizable := prefetched
			if !prefetched {
				var err error
				if role == "MediaFile" || subset {
					data, err = load(key)
					if err != nil {
						return err
					}
				}
				optimizable = data != nil
				if optimizable {
					source.key.hash = sha256.Sum256(data)
				} else {
					if hashBuffer == nil {
						hashBuffer = make([]byte, 32*1024)
					}
					source.key.hash, err = outputResourceDigest(e.output.ctx, reader, parts, actual[key], hashBuffer)
					if err != nil {
						return err
					}
				}
			}
			if optimizable && subset {
				candidate, err := fontCache.subset(e.output.ctx, e.backends.FontResources, data, usage)
				if err != nil {
					return fmt.Errorf("subset font %s: %w", key, err)
				}
				if err := e.output.ctx.Err(); err != nil {
					return err
				}
				if len(candidate) != 0 && len(candidate) < len(data) {
					data = candidate
					parts[actual[key]] = data
					source.key.hash = sha256.Sum256(data)
				}
			}
			if role == "MediaFile" && optimizable {
				options, demand := e.outputImageOptions(key)
				cacheKey := outputImageKey{hash: source.key.hash, options: options, size: demand}
				candidate, cached := imageResults[cacheKey]
				var err error
				if !cached {
					candidate, err = optimizeOutputImage(e.output.ctx, outputImageJob{data: data, key: outputImageKey{options: options, size: demand}})
					if err := e.output.ctx.Err(); err != nil {
						return err
					}
					imageResults[cacheKey] = nil
					if err == nil && len(candidate) < len(data) {
						imageResults[cacheKey] = candidate
					} else {
						candidate = nil
					}
				}
				if candidate != nil {
					data = candidate
					parts[actual[key]] = data
					digest, ok := imageDigests[cacheKey]
					if !ok {
						digest = sha256.Sum256(data)
						imageDigests[cacheKey] = digest
					}
					source.key.hash = digest
				}
				if data != nil {
					source.format = outputImageFormat(data)
				}
				if source.format != "" {
					formats[key] = source.format
					suffix := ".png"
					if source.format == "JPEG" {
						suffix = ".jpg"
					}
					ext := path.Ext(actual[key])
					if !strings.EqualFold(ext, suffix) && !(source.format == "JPEG" && strings.EqualFold(ext, ".jpeg")) {
						if data == nil {
							data, err = read(actual[key])
							if err != nil {
								return err
							}
						}
						stem := strings.TrimSuffix(actual[key], ext)
						name := stem + suffix
						for n := 1; reserved[name]; n++ {
							name = fmt.Sprintf("%s-%d%s", stem, n, suffix)
						}
						reserved[name] = true
						locations[key] = name
						parts[name] = data
						delete(parts, actual[key])
						removed[key] = true
					}
				}
			}
			scope := resourceScope{role, selected[key]}
			if seen[scope] == nil {
				seen[scope] = make(map[[32]byte]string)
			}
			hash := source.key.hash
			if first, ok := seen[scope][hash]; ok {
				aliases[key] = first
				removed[key] = true
				if locations[key] != actual[key] {
					delete(parts, locations[key])
				}
			} else {
				seen[scope][hash] = key
			}
		}
	}
	if len(aliases) == 0 && len(formats) == 0 {
		return e.output.ctx.Err()
	}
	for _, name := range resources {
		if refs.versions[cleanPackagePath(name)] {
			continue
		}
		data, err := read(name)
		if err != nil {
			return err
		}
		root, err := parseEditorXMLContext(e.output.ctx, data)
		if err != nil {
			return err
		}
		var patches []editorXMLPatch
		for _, group := range root.children {
			for _, node := range group.children {
				if err := e.output.ctx.Err(); err != nil {
					return err
				}
				for _, field := range []string{"FontFile", "MediaFile"} {
					file := node.child(field)
					if file == nil {
						continue
					}
					var value string
					if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
						return err
					}
					key := editorResourceLocation(name, root.attr("BaseLoc"), value)
					if selected != nil && !selected[cleanPackagePath(name)] && selected[key] {
						continue
					}
					location := locations[key]
					if first, ok := aliases[key]; ok {
						location = locations[first]
					}
					if location != "" && location != actual[key] {
						var escaped bytes.Buffer
						xml.EscapeText(&escaped, []byte("/"+location))
						patches = append(patches, editorXMLContent(data, file, escaped.Bytes()))
					}
					if field == "MediaFile" && formats[key] != "" && node.attr("Format") != formats[key] {
						patch, err := editorXMLAttributePatch(data, node, "Format", formats[key])
						if err != nil {
							return err
						}
						patches = append(patches, patch)
					}
				}
			}
		}
		if len(patches) > 0 {
			parts[name] = editorPatchXML(data, patches)
		}
	}
	return e.output.ctx.Err()
}

// outputResourceSize 获取资源的实际输出大小，优先使用本次改动及已加载数据
// 入参: reader 原包，可为空, parts 输出改动, name 资源路径
// 返回: uint64 未压缩字节数, error 文件不存在
func outputResourceSize(reader *Reader, parts map[string][]byte, name string) (uint64, error) {
	if data, ok := parts[name]; ok {
		return uint64(len(data)), nil
	}
	if reader != nil {
		if data, ok := reader.files[cleanPackagePath(name)]; ok {
			return uint64(len(data)), nil
		}
		if file, ok := reader.packageFile(name); ok {
			return file.UncompressedSize64, nil
		}
	}
	return 0, fmt.Errorf("file not found: %s", name)
}

// outputResourceDigest 流式计算资源摘要，不将原包中的大文件加载到内存
// 入参: ctx 取消上下文, reader 原包，可为空, parts 输出改动, name 资源路径, buffer 复用缓冲
// 返回: [32]byte 内容摘要, error 读取或取消错误
func outputResourceDigest(ctx context.Context, reader *Reader, parts map[string][]byte, name string, buffer []byte) ([32]byte, error) {
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	var input io.ReadCloser
	if data, ok := parts[name]; ok {
		input = io.NopCloser(bytes.NewReader(data))
	} else {
		if reader == nil {
			return [32]byte{}, fmt.Errorf("file not found: %s", name)
		}
		var err error
		input, err = reader.openFile(name)
		if err != nil {
			return [32]byte{}, err
		}
	}
	hash := sha256.New()
	_, err := io.CopyBuffer(hash, imageInput{ReadCloser: input, ctx: ctx}, buffer)
	closeErr := input.Close()
	if err != nil {
		return [32]byte{}, err
	}
	if closeErr != nil {
		return [32]byte{}, closeErr
	}
	return [32]byte(hash.Sum(nil)), ctx.Err()
}

// compressionFontUsage 按文档合并共享字体用字，版本编辑仅统计选中入口
// 入参: ctx 取消上下文, documents 文档入口, resources 资源索引, references 各文件引用, read 输出文件读取方法
// 返回: map[string]*editorFontUsage 各字体文件的用字，无法确定作用域时为空, error 读取或取消错误
func (r *Reader) compressionFontUsage(ctx context.Context, documents map[string]bool, resources []string, references map[string]*editorResourceRefs, read func(string) ([]byte, error)) (map[string]*editorFontUsage, error) {
	selected := ""
	if r.OFD.DocBody[r.documentIndex].versioned {
		var err error
		selected, err = r.DocumentRootContext(ctx)
		if err != nil {
			return nil, err
		}
	}
	scoped, err := resourceReferenceScopes(ctx, documents, resources, references, selected)
	if err != nil || scoped == nil {
		return nil, err
	}
	return resourceFontUsage(ctx, resources, scoped, read)
}

// resourceFontUsage 按已确认的文档作用域合并各字体文件的字符和字形引用
// 入参: ctx 取消上下文, resources 资源索引, scoped 各索引的用字范围, read 输出文件读取方法
// 返回: map[string]*editorFontUsage 各字体文件的用字, error 读取或取消错误
func resourceFontUsage(ctx context.Context, resources []string, scoped map[string]*editorResourceRefs, read func(string) ([]byte, error)) (map[string]*editorFontUsage, error) {
	result := make(map[string]*editorFontUsage)
	protected := make(map[string]bool)
	for _, name := range resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		refs := scoped[name]
		if refs == nil {
			continue
		}
		data, err := read(name)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		for font := range editorResourceNodes(root, "Fonts", "Font") {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			file := font.child("FontFile")
			if file == nil {
				continue
			}
			var value string
			if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
				return nil, err
			}
			location := editorResourceLocation(name, root.attr("BaseLoc"), value)
			alternate := cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))
			id, charset := editorResourceID(font.attr("ID")), font.attr("Charset")
			unsafe := id == "" || charset != "" && !strings.EqualFold(charset, "unicode") || location != alternate
			for i, path := range [2]string{location, alternate} {
				if i == 1 && path == location {
					continue
				}
				if result[path] == nil {
					result[path] = newEditorFontUsage()
				}
				result[path].merge(refs.fonts[id], unsafe)
			}
		}
		for media := range editorResourceNodes(root, "MultiMedias", "MultiMedia") {
			file := media.child("MediaFile")
			if file == nil {
				continue
			}
			var value string
			if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
				return nil, err
			}
			for _, location := range []string{editorResourceLocation(name, root.attr("BaseLoc"), value), cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))} {
				protected[location] = true
			}
		}
	}
	for location := range protected {
		if usage := result[location]; usage != nil {
			usage.unsafe = true
		}
	}
	return result, ctx.Err()
}

// compactResourceProfiles 合并相同颜色配置文件，保留颜色空间声明和独立文件引用
// 入参: ctx 取消上下文, reader 原包，可为空, parts 输出改动, profiles 配置文件引用, protected 不可替换的文件, removed 待移除文件
// 返回: error 读取、改写或取消错误
func compactResourceProfiles(ctx context.Context, reader *Reader, parts map[string][]byte, profiles map[string][]editorProfileReference, protected, removed map[string]bool) error {
	if len(profiles) < 2 {
		return ctx.Err()
	}
	seen := make(map[[32]byte]string)
	aliases := make(map[string]string)
	indexes := make(map[string]map[string]string)
	ordered := slices.Sorted(maps.Keys(profiles))
	sizes := make(map[string]uint64, len(profiles))
	counts := make(map[uint64]int)
	for _, name := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if protected[name] {
			continue
		}
		size, err := outputResourceSize(reader, parts, name)
		if err != nil {
			return err
		}
		sizes[name] = size
		counts[size]++
	}
	var buffer []byte
	for _, name := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if protected[name] || counts[sizes[name]] < 2 {
			continue
		}
		if buffer == nil {
			buffer = make([]byte, 32*1024)
		}
		key, err := outputResourceDigest(ctx, reader, parts, name, buffer)
		if err != nil {
			return err
		}
		first, found := seen[key]
		if !found {
			seen[key] = name
			continue
		}
		aliases[name] = first
		for _, reference := range profiles[name] {
			if indexes[reference.index] == nil {
				indexes[reference.index] = make(map[string]string)
			}
			indexes[reference.index][reference.id] = first
		}
	}
	updates := make(map[string][]byte, len(indexes))
	for _, name := range slices.Sorted(maps.Keys(indexes)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, ok := parts[name]
		if !ok {
			if reader == nil {
				return fmt.Errorf("file not found: %s", name)
			}
			var err error
			data, err = reader.readFileViewContext(ctx, name)
			if err != nil {
				return err
			}
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		var patches []editorXMLPatch
		for _, group := range root.children {
			if !group.matchesOFD("ColorSpaces") {
				continue
			}
			for _, node := range group.children {
				if err := ctx.Err(); err != nil {
					return err
				}
				location := indexes[name][editorResourceID(node.attr("ID"))]
				if !node.matchesOFD("ColorSpace") || location == "" {
					continue
				}
				patch, err := editorXMLAttributePatch(data, node, "Profile", "/"+location)
				if err != nil {
					return err
				}
				patches = append(patches, patch)
			}
		}
		updates[name] = editorPatchXML(data, patches)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	maps.Copy(parts, updates)
	for name := range aliases {
		removed[name] = true
		delete(parts, name)
	}
	return nil
}
