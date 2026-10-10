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
	"maps"
	"slices"
	"strings"
)

// compressionFontBinding 保存资源索引中的字体标识、文件路径及可裁剪状态
type compressionFontBinding struct {
	id     string
	file   string
	unsafe bool
}

// compressionFontUsage 按文档解析字体引用，按文件路径合并共享字体的全部用字
// 入参: ctx 取消上下文, parts 输出改动, generated 自产页面引用, documents 文档入口, resources 资源索引, references 各文件引用, read 输出文件读取方法
// 返回: map[string]*editorFontUsage 各字体文件的用字，无法确定作用域时为空, error 读取或取消错误
func (r *Reader) compressionFontUsage(ctx context.Context, parts map[string][]byte, generated map[string]editorGeneratedReferences, documents map[string]bool, resources []string, references map[string]*editorResourceRefs, read func(string) ([]byte, error)) (map[string]*editorFontUsage, error) {
	bindings := make(map[string][]compressionFontBinding)
	for _, name := range resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := read(name)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		fonts := root.child("Fonts")
		if fonts == nil {
			continue
		}
		for _, font := range fonts.children {
			file := font.child("FontFile")
			if file == nil {
				continue
			}
			var value string
			if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
				return nil, err
			}
			location := editorResourceLocation(name, root.attr("BaseLoc"), value)
			id, charset := editorResourceID(font.attr("ID")), font.attr("Charset")
			unsafe := id == "" || charset != "" && !strings.EqualFold(charset, "unicode") || location != cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))
			bindings[name] = append(bindings[name], compressionFontBinding{id: id, file: location, unsafe: unsafe})
		}
	}
	used := make(map[string]bool)
	result := make(map[string]*editorFontUsage)
	for _, entry := range slices.Sorted(maps.Keys(documents)) {
		scope, err := r.versionFileDependencies(ctx, entry, parts, generated)
		if err != nil {
			return nil, err
		}
		refs := editorResourceRefs{fonts: make(map[string]*editorFontUsage)}
		for name := range scope {
			if source := references[name]; source != nil {
				for id, usage := range source.fonts {
					refs.fontUsage(id).merge(usage, false)
				}
				used[name] = true
			}
		}
		for name := range scope {
			for _, binding := range bindings[name] {
				usage := refs.fonts[binding.id]
				if result[binding.file] == nil {
					result[binding.file] = newEditorFontUsage()
				}
				result[binding.file].merge(usage, binding.unsafe)
			}
		}
	}
	for name, refs := range references {
		if !used[name] && (len(refs.fonts) != 0 || len(bindings[name]) != 0) {
			return nil, nil
		}
	}
	return result, ctx.Err()
}
