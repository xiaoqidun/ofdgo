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
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"slices"
)

// compactPackageFonts 清理无引用字体，补齐多文档及历史版本的字体裁剪，不修改编辑器快照
// 入参: ctx 取消上下文, parts 输出修改, removed 待删除条目, generated 本次编码的页面引用
// 返回: error 读取、结构或取消错误
func (e *Editor) compactPackageFonts(ctx context.Context, parts map[string][]byte, removed map[string]bool, generated map[string]editorGeneratedReferences) error {
	reader := e.source.reader
	versioned := false
	for _, body := range reader.OFD.DocBody {
		versioned = versioned || body.versioned
	}
	if !versioned {
		if reader.DocumentCount() <= 1 || e.backends.FontResources == nil || e.output != nil && e.output.options.Mode != CompressionUnchanged {
			return nil
		}
	}
	protected, err := reader.hasOutputSignatures(ctx)
	if err != nil || protected {
		return err
	}
	resources := e.runtimeResources
	if resources == nil {
		resources = reader.resources
	}
	view := &Reader{Zip: reader.Zip, encrypted: reader.encryptedSource(), sourceFile: reader.sourceFile, files: maps.Clone(reader.files), resources: resources}
	if view.files == nil {
		view.files = make(map[string][]byte)
	}
	maps.Copy(view.files, parts)
	if err := view.initRootContext(ctx); err != nil {
		return err
	}
	data, err := view.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	candidates, seen := make(map[string]bool), make(map[string]bool)
	for _, body := range root.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !body.matchesOFD("DocBody") {
			continue
		}
		versions, err := parseVersionEntries(ctx, body)
		if err != nil {
			return err
		}
		for _, entry := range versions {
			version, err := view.readDocumentVersionContext(ctx, entry.ID, entry.Location)
			if err != nil {
				return fmt.Errorf("version %s: %w", entry.ID, err)
			}
			for _, file := range version.Files {
				if err := ctx.Err(); err != nil {
					return err
				}
				name := file.Location
				if seen[name] || removed[name] {
					continue
				}
				seen[name] = true
				candidate, err := view.fontCleanupCandidate(ctx, name)
				if err != nil {
					return err
				}
				if candidate {
					candidates[name] = true
				}
			}
		}
	}
	if err := pruneUnreferencedFiles(ctx, view, slices.Collect(maps.Keys(candidates))...); err != nil {
		return err
	}
	if err := e.subsetPackageFonts(ctx, view, parts, removed, generated); err != nil {
		return err
	}
	for name, data := range view.files {
		if original, ok := reader.files[name]; !ok || !bytes.Equal(original, data) {
			parts[name] = data
		}
	}
	for name := range candidates {
		if !view.fileNames[name] {
			removed[name] = true
			delete(parts, name)
		}
	}
	return nil
}

// fontCleanupCandidate 检查字体目录与文件范围，仅将可识别字体交给引用清理流程
// 入参: ctx 取消上下文, name 包内文件路径
// 返回: bool 是否为候选字体, error 读取或取消错误
func (r *Reader) fontCleanupCandidate(ctx context.Context, name string) (bool, error) {
	input, err := r.openFile(name)
	if err != nil {
		return false, err
	}
	defer input.Close()
	var size uint64
	if data, ok := r.files[cleanPackagePath(name)]; ok {
		size = uint64(len(data))
	} else if file, ok := r.packageFile(name); ok {
		size = file.UncompressedSize64
	} else if src := r.encryptedSource(); src != nil {
		if entrySize, ok := src.size(name); ok {
			size = entrySize
		}
	}
	candidate, err := fontCleanupDirectory(imageInput{ReadCloser: input, ctx: ctx}, size)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	if canceled := ctx.Err(); canceled != nil {
		return false, canceled
	}
	return candidate, err
}

// fontCleanupDirectory 检查SFNT表目录或字体集合索引，不读取字形数据
// 入参: input 文件流, size 文件长度
// 返回: bool 是否具有有效目录, error 读取错误
func fontCleanupDirectory(input io.Reader, size uint64) (bool, error) {
	var header [12]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		return false, err
	}
	if size < uint64(len(header)) {
		return false, nil
	}
	if string(header[:4]) == "ttcf" {
		version := binary.BigEndian.Uint32(header[4:])
		count := uint64(binary.BigEndian.Uint32(header[8:]))
		if version != 0x00010000 && version != 0x00020000 || count == 0 || count > (size-12)/4 {
			return false, nil
		}
		end := 12 + count*4
		if version == 0x00020000 {
			end += 12
		}
		var entry [4]byte
		for range count {
			if _, err := io.ReadFull(input, entry[:]); err != nil {
				return false, err
			}
			offset := uint64(binary.BigEndian.Uint32(entry[:]))
			if offset < end || offset > size-12 {
				return false, nil
			}
		}
		return true, nil
	}
	switch string(header[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
	default:
		return false, nil
	}
	count := uint64(binary.BigEndian.Uint16(header[4:]))
	if count == 0 || count > (size-12)/16 {
		return false, nil
	}
	end := 12 + count*16
	seen := make(map[uint32]bool, count)
	var entry [16]byte
	for range count {
		if _, err := io.ReadFull(input, entry[:]); err != nil {
			return false, err
		}
		tag := binary.BigEndian.Uint32(entry[:])
		offset := uint64(binary.BigEndian.Uint32(entry[8:]))
		length := uint64(binary.BigEndian.Uint32(entry[12:]))
		if seen[tag] || offset < end || offset > size || length > size-offset {
			return false, nil
		}
		seen[tag] = true
	}
	return true, nil
}

// subsetPackageFonts 按文档作用域收集用字，复用引用缓存并跳过已裁剪收敛字体
// 入参: ctx 取消上下文, view 独立输出快照, parts 前序输出改动, removed 待删除条目, generated 本次编码的页面引用
// 返回: error 读取、裁剪或取消错误
func (e *Editor) subsetPackageFonts(ctx context.Context, view *Reader, parts map[string][]byte, removed map[string]bool, generated map[string]editorGeneratedReferences) error {
	if e.backends.FontResources == nil {
		return nil
	}
	names := maps.Clone(view.fileNames)
	for name := range removed {
		delete(names, name)
	}
	progress := editorProgress(e.OnWriteProgress)
	var refs *editorResourceRefs
	var resources []string
	var references map[string]*editorResourceRefs
	var safe bool
	var err error
	if e.output != nil && e.output.references != nil {
		refs, resources, references, safe, err = scanEditorResourceFiles(ctx, e.source.reader, view.files, generated, names, nil, progress, true, e.output.references)
	} else {
		refs, resources, references, safe, err = scanEditorResources(ctx, view, view.files, generated, names, progress, true)
	}
	if err != nil || !safe || refs.fontUnsafe {
		return err
	}
	scoped, err := resourceReferenceScopes(ctx, refs.documents, resources, references, "")
	if err != nil || scoped == nil {
		return err
	}
	read := func(name string) ([]byte, error) { return view.readFileViewContext(ctx, name) }
	usage, err := resourceFontUsage(ctx, resources, scoped, read)
	if err != nil {
		return err
	}
	for _, source := range references {
		for name := range source.files {
			if font := usage[name]; font != nil && !source.versions[name] {
				font.unsafe = true
			}
		}
		for _, definition := range source.definitions {
			for name := range definition.files {
				if font := usage[name]; font != nil {
					font.unsafe = true
				}
			}
		}
	}
	converged := make(map[[32]byte]bool)
	for _, resource := range e.resources {
		if resource.subset != nil && len(resource.subset.data) != 0 {
			converged[sha256.Sum256(resource.subset.data)] = true
		}
	}
	if e.output != nil && e.output.options.Mode != CompressionUnchanged {
		for name, data := range parts {
			if usage[name] != nil && e.source.reader.fileNames[name] && len(data) != 0 {
				converged[sha256.Sum256(data)] = true
			}
		}
	}
	var cache editorSourceFontCache
	for index, name := range slices.Sorted(maps.Keys(usage)) {
		if err := progress.report("fonts", index, len(usage)); err != nil {
			return err
		}
		if usage[name].unsafe || len(usage[name].chars)+len(usage[name].glyphs) == 0 {
			continue
		}
		data, err := read(name)
		if err != nil {
			return err
		}
		if converged[sha256.Sum256(data)] {
			continue
		}
		subset, err := cache.subset(ctx, e.backends.FontResources, data, usage[name])
		if err != nil {
			return err
		}
		if subset != nil {
			view.files[name] = subset
		}
	}
	if err := progress.report("fonts", len(usage), len(usage)); err != nil {
		return err
	}
	return ctx.Err()
}
