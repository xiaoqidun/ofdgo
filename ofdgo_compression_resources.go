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
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// outputImageKey 区分相同图像的压缩配置和像素需求，不跨写出保留状态
type outputImageKey struct {
	hash    [32]byte
	options CompressionOptions
	size    image.Point
}

// compressResourceParts 优化输出图片并合并相同资源文件，保留资源标识及各自属性
// 入参: parts 输出改动, reader 原包，可为空, removed 已移除条目
// 返回: error 读取或取消错误
func (e *Editor) compressResourceParts(parts map[string][]byte, reader *Reader, removed map[string]bool) error {
	return e.compressResourceReferences(parts, reader, removed, nil)
}

// compressResourceReferences 优化资源，复用自产页面引用并流式读取暂存页面
// 入参: parts 输出改动, reader 原包, removed 已移除条目, generated 本次自产页面及引用
// 返回: error 读取或取消错误
func (e *Editor) compressResourceReferences(parts map[string][]byte, reader *Reader, removed map[string]bool, generated map[string]editorGeneratedReferences) error {
	if e.output == nil || e.output.options.Mode == CompressionUnchanged || e.output.protected {
		return nil
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
		if data, ok := parts[name]; ok {
			return data, nil
		}
		return reader.readFile(name)
	}
	refs := editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
	var resources []string
	actual := make(map[string]string)
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		key := strings.ToLower(cleanPackagePath(name))
		if removed[key] || strings.HasSuffix(name, "/") {
			continue
		}
		if prior, ok := actual[key]; ok && prior != name {
			return nil
		}
		actual[key] = name
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
				return err
			}
		}
		resource, safe := refs.scan(imageInput{ReadCloser: input, context: e.output.ctx}, name)
		input.Close()
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		if !safe {
			return nil
		}
		if resource {
			resources = append(resources, name)
		}
	}
	type resourceFile struct{ key, role string }
	files := make(map[resourceFile]bool)
	for _, name := range resources {
		data, err := read(name)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		for _, group := range root.children {
			for _, node := range group.children {
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
				if field == "MediaFile" && node.attr("Type") != "Image" {
					refs.files[key] = true
					continue
				}
				if key != strings.ToLower(cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))) {
					return nil
				}
				if actual[key] != "" && !refs.files[key] {
					files[resourceFile{key, field}] = true
				}
			}
		}
	}
	aliases := make(map[string]string)
	locations := maps.Clone(actual)
	reserved := make(map[string]bool, len(actual))
	for key := range actual {
		reserved[key] = true
	}
	formats := make(map[string]string)
	seen := make(map[string]map[[32]byte]string)
	imageResults := make(map[outputImageKey][]byte)
	for _, key := range slices.Sorted(maps.Keys(actual)) {
		if refs.files[key] || files[resourceFile{key, "FontFile"}] && files[resourceFile{key, "MediaFile"}] {
			continue
		}
		for _, role := range []string{"FontFile", "MediaFile"} {
			if !files[resourceFile{key, role}] {
				continue
			}
			if err := editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0); err != nil {
				return err
			}
			if data, ok := parts[actual[key]]; ok && len(data) > outputOptimizationBufferLimit {
				continue
			}
			if reader != nil && parts[actual[key]] == nil {
				if file, ok := reader.packageFile(actual[key]); ok && file.UncompressedSize64 > outputOptimizationBufferLimit {
					continue
				}
			}
			data, err := read(actual[key])
			if err != nil {
				return err
			}
			if len(data) > outputOptimizationBufferLimit {
				continue
			}
			if role == "MediaFile" {
				options := e.output.options
				if !e.output.images[key] {
					options.Mode = CompressionLossless
				}
				demand := e.output.sizes[key]
				if options.Mode != CompressionLossy {
					options = CompressionOptions{Mode: options.Mode}
					demand = image.Point{}
				}
				cacheKey := outputImageKey{hash: sha256.Sum256(data), options: options, size: demand}
				candidate, cached := imageResults[cacheKey]
				var err error
				if !cached {
					candidate, err = pdfgo.OptimizeImageResource(e.output.ctx, data, options, demand)
					if err == nil {
						imageResults[cacheKey] = nil
						if len(candidate) < len(data) {
							imageResults[cacheKey] = candidate
						}
					}
				}
				if err != nil {
					if e.output.ctx.Err() != nil {
						return e.output.ctx.Err()
					}
				} else if candidate != nil && len(candidate) < len(data) {
					data = candidate
					parts[actual[key]] = data
				}
				if bytes.HasPrefix(data, []byte{255, 216}) {
					formats[key] = "JPEG"
					ext := path.Ext(actual[key])
					if !strings.EqualFold(ext, ".jpg") && !strings.EqualFold(ext, ".jpeg") {
						stem := strings.TrimSuffix(actual[key], ext)
						name := stem + ".jpg"
						for n := 1; reserved[strings.ToLower(name)]; n++ {
							name = fmt.Sprintf("%s-%d.jpg", stem, n)
						}
						reserved[strings.ToLower(name)] = true
						locations[key] = name
						parts[name] = data
						delete(parts, actual[key])
						removed[key] = true
					}
				}
			}
			if seen[role] == nil {
				seen[role] = make(map[[32]byte]string)
			}
			hash := sha256.Sum256(data)
			if first, ok := seen[role][hash]; ok {
				aliases[key] = first
				removed[key] = true
				if locations[key] != actual[key] {
					delete(parts, locations[key])
				}
			} else {
				seen[role][hash] = key
			}
		}
	}
	for _, name := range resources {
		data, err := read(name)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return err
		}
		var patches []editorXMLPatch
		for _, group := range root.children {
			for _, node := range group.children {
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
					fragment := data[node.start:node.end]
					location := locations[key]
					if first, ok := aliases[key]; ok {
						location = locations[first]
					}
					if location != "" && location != actual[key] {
						var escaped bytes.Buffer
						xml.EscapeText(&escaped, []byte("/"+location))
						patch := editorXMLContent(data, file, escaped.Bytes())
						patch.start -= node.start
						patch.end -= node.start
						fragment = editorPatchXML(fragment, []editorXMLPatch{patch})
					}
					if field == "MediaFile" && formats[key] != "" && node.attr("Format") != formats[key] {
						parsed, err := parseEditorXML(fragment)
						if err != nil {
							return err
						}
						fragment, err = editorXMLAttribute(fragment, parsed, "Format", formats[key])
						if err != nil {
							return err
						}
					}
					if !bytes.Equal(fragment, data[node.start:node.end]) {
						patches = append(patches, editorXMLPatch{start: node.start, end: node.end, data: fragment})
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
