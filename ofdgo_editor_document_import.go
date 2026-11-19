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
	"fmt"
	"maps"
	"slices"
	"strings"
)

// DocumentImportOptions 整文档导入选项
// AllowDecrypted显式允许解密来源导入未加密目标
// OnProgress报告documents、resources、commit阶段，total为0表示总量未知
// 回调返回错误则放弃导入，回调不可重入修改编辑器
type DocumentImportOptions struct {
	AllowDecrypted bool
	OnProgress     func(stage string, completed, total int) error
}

// ImportDocuments 导入整文档及历史版本并选中首项
// 入参: source 来源阅读器, indexes 从0开始的文档索引，nil表示全部，空切片不导入, at 从0开始的插入位置，可等于文档数
// 返回: []int 新文档索引, error 导入错误
func (e *Editor) ImportDocuments(source *Reader, indexes []int, at int) ([]int, error) {
	return e.ImportDocumentsContext(context.Background(), source, indexes, at, DocumentImportOptions{})
}

// ImportDocumentsWithOptions 按指定选项导入整文档
// 入参: source 来源阅读器, indexes 从0开始的文档索引，nil表示全部，空切片不导入, at 从0开始的插入位置，可等于文档数, options 导入选项
// 返回: []int 新文档索引, error 导入错误
func (e *Editor) ImportDocumentsWithOptions(source *Reader, indexes []int, at int, options DocumentImportOptions) ([]int, error) {
	return e.ImportDocumentsContext(context.Background(), source, indexes, at, options)
}

// ImportDocumentsContext 原子导入文档并保留一次撤销记录
// 保持输入索引顺序，拒绝重复索引，返回后可关闭来源阅读器
// 保留签名数据和证书，入口或引用迁移后应重新验签
// 入参: ctx 取消上下文, source 来源阅读器, indexes 从0开始的文档索引，nil表示全部，空切片不导入, at 从0开始的插入位置，可等于文档数, options 导入选项
// 返回: []int 新文档索引, error 校验、导入或取消错误
func (e *Editor) ImportDocumentsContext(ctx context.Context, source *Reader, indexes []int, at int, options DocumentImportOptions) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("document import requires a source")
	}
	if at < 0 || at > e.DocumentCount() {
		return nil, fmt.Errorf("document index %d out of range", at)
	}
	if source.encryption != nil && e.encryption == nil && !options.AllowDecrypted {
		return nil, ErrEncryptionPolicyRequired
	}
	if indexes == nil {
		indexes = make([]int, source.DocumentCount())
		for index := range indexes {
			indexes[index] = index
		}
	} else {
		indexes = slices.Clone(indexes)
	}
	seen := make(map[int]bool, len(indexes))
	for _, index := range indexes {
		if index < 0 || index >= source.DocumentCount() || seen[index] {
			return nil, fmt.Errorf("invalid or duplicate document index %d", index)
		}
		seen[index] = true
	}
	if len(indexes) == 0 {
		return []int{}, ctx.Err()
	}
	migration := &editorDocumentImport{ctx: ctx, source: source, files: make(map[string]*versionEditFile),
		borrowed: make(map[string]bool), kinds: make(map[string]int), progress: editorProgress(options.OnProgress).withContext(ctx)}
	if err := migration.progress.report("documents", 0, len(indexes)); err != nil {
		return nil, err
	}
	data, err := source.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	entry := &versionEditFile{reader: source, name: "OFD.xml", data: data}
	for position, index := range indexes {
		body := root.childAt("DocBody", index)
		if body == nil {
			return nil, fmt.Errorf("document body %d not found", index)
		}
		if err := migration.collect(entry, root, body, ""); err != nil {
			return nil, err
		}
		if err := migration.progress.report("documents", position+1, len(indexes)); err != nil {
			return nil, err
		}
	}
	if err := migration.load(false); err != nil {
		return nil, err
	}
	err = e.editDocumentsContext(ctx, func(reader *Reader, target []byte, targetRoot *editorXML) (int, error) {
		version, err := documentImportVersion(targetRoot.attr("Version"), root.attr("Version"))
		if err != nil {
			return 0, err
		}
		if version != targetRoot.attr("Version") {
			target, err = editorXMLAttribute(target, targetRoot, "Version", version)
			if err != nil {
				return 0, err
			}
			targetRoot, err = parseEditorXMLContext(ctx, target)
			if err != nil {
				return 0, err
			}
		}
		paths := make(map[string]string)
		occupied, err := documentImportOccupied(ctx, reader, target, targetRoot)
		if err != nil {
			return 0, err
		}
		reserved := maps.Clone(occupied)
		for name := range migration.kinds {
			reserved[name] = nil
		}
		for _, name := range slices.Sorted(maps.Keys(migration.kinds)) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			candidate := packageAvailableName(reader, occupied, name)
			if candidate != name {
				candidate = packageAvailableName(reader, reserved, name)
				paths[name] = candidate
				reserved[candidate] = nil
			}
		}
		for _, name := range slices.Sorted(maps.Keys(migration.files)) {
			file := migration.files[name]
			content, err := file.rewrite(ctx, paths)
			if err != nil {
				return 0, err
			}
			location := name
			if paths[name] != "" {
				location = paths[name]
			}
			if migration.borrowed[name] && len(content) == len(file.data) && (len(content) == 0 || &content[0] == &file.data[0]) {
				content = bytes.Clone(content)
			}
			reader.files[location] = content
			file.data = nil
		}
		updated, err := entry.rewrite(ctx, paths)
		if err != nil {
			return 0, err
		}
		updatedRoot, err := parseEditorXMLContext(ctx, updated)
		if err != nil {
			return 0, err
		}
		var inserted bytes.Buffer
		for _, index := range indexes {
			body := updatedRoot.childAt("DocBody", index)
			content, err := editorXMLStandaloneContext(ctx, updated[body.start:body.end], body)
			if err != nil {
				return 0, err
			}
			inserted.Write(content)
		}
		position := targetRoot.close
		if body := targetRoot.childAt("DocBody", at); body != nil {
			position = body.start
		}
		reader.files["OFD.xml"] = editorPatchXML(target, []editorXMLPatch{{start: position, end: position, data: inserted.Bytes()}})
		if err := migration.progress.report("commit", 0, 0); err != nil {
			return 0, err
		}
		return at, nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]int, len(indexes))
	for index := range result {
		result[index] = at + index
	}
	return result, nil
}

// editorDocumentImport 保存文档依赖及原始引用位置
type editorDocumentImport struct {
	ctx      context.Context
	source   *Reader
	files    map[string]*versionEditFile
	borrowed map[string]bool
	kinds    map[string]int
	queue    []string
	progress editorProgress
}

// reference 记录已解析的包内引用并调度结构文件
// 入参: file 来源文件, node 引用节点, attribute 属性名, base 资源目录, value 引用值, kind 引用类别
// 返回: error 路径错误
func (m *editorDocumentImport) reference(file *versionEditFile, node *editorXML, attribute, base, value string, kind int) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	target := resolveResourcePath(file.name, base, value)
	if !validPackagePath(target) {
		return fmt.Errorf("invalid document import path %q", value)
	}
	if target == "OFD.xml" && attribute == "FileRef" && node.matchesOFD("Reference") {
		return nil
	}
	file.links = append(file.links, versionEditLink{node: node, attribute: attribute, target: target})
	if previous := m.kinds[target]; previous < kind {
		m.kinds[target] = kind
		m.queue = append(m.queue, target)
	}
	return nil
}

// collect 收集标准引用并保持未知扩展载荷原文
// 入参: file 来源文件, root 文件根节点, node 当前节点, base 资源目录
// 返回: error 引用或取消错误
func (m *editorDocumentImport) collect(file *versionEditFile, root, node *editorXML, base string) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	if !node.matchesOFD(node.name.Local) {
		return nil
	}
	parent := ""
	if node.parent != nil {
		parent = node.parent.name.Local
	}
	if parent == "Extension" && node.name.Local == "Data" {
		return nil
	}
	if root.matchesOFD("Res") {
		base = root.attr("BaseLoc")
	}
	for _, attribute := range node.attrs {
		if attribute.Name.Space != "" {
			continue
		}
		kind := packageReference(root.name.Local, parent, node.name.Local, attribute.Name.Local)
		if root.matchesOFD("Signature") && parent == "References" && node.matchesOFD("Reference") && attribute.Name.Local == "FileRef" {
			kind = packageReferenceData
		}
		if attribute.Name.Local == "FileLoc" && (parent == "Action" || parent == "Actions" || node.matchesOFD("GotoA") || node.matchesOFD("URI") || node.matchesOFD("Sound") || node.matchesOFD("Movie")) {
			kind = packageReferenceData
		}
		if kind != packageReferenceNone {
			if err := m.reference(file, node, attribute.Name.Local, base, attribute.Value, kind); err != nil {
				return err
			}
		}
	}
	kind := packageReference(root.name.Local, parent, node.name.Local, "")
	if node.matchesOFD("FileLoc") && (parent == "GotoA" || parent == "URI" || parent == "Sound" || parent == "Movie" || parent == "Action") {
		kind = packageReferenceData
	}
	if kind != packageReferenceNone {
		if err := m.reference(file, node, "", base, editorImportText(file.data, node), kind); err != nil {
			return err
		}
	}
	for _, child := range node.children {
		if err := m.collect(file, root, child, base); err != nil {
			return err
		}
	}
	return nil
}

// load 读取依赖并仅解析标准结构文件
// 入参: structuresOnly 是否只读取结构文件
// 返回: error 读取、解析或取消错误
func (m *editorDocumentImport) load(structuresOnly bool) error {
	parsed := make(map[string]bool)
	for position := 0; position < len(m.queue); position++ {
		if err := m.progress.report("resources", position, 0); err != nil {
			return err
		}
		name := m.queue[position]
		if structuresOnly && m.kinds[name] != packageReferenceXML {
			continue
		}
		file := m.files[name]
		if file == nil {
			_, cached := m.source.files[name]
			if !cached {
				if _, exists := m.source.packageFile(name); !exists && !m.source.encryptedSource().has(name) {
					continue
				}
			}
			data, err := m.source.readFileViewContext(m.ctx, name)
			if err != nil {
				return fmt.Errorf("import document dependency %s: %w", name, err)
			}
			file = &versionEditFile{reader: m.source, name: name, data: data}
			m.files[name] = file
			if cached && m.borrowed != nil {
				m.borrowed[name] = true
			}
		}
		if m.kinds[name] == packageReferenceXML && !parsed[name] {
			parsed[name] = true
			root, err := parseEditorXMLContext(m.ctx, file.data)
			if err != nil {
				return err
			}
			if err := m.collect(file, root, root, ""); err != nil {
				return err
			}
		}
	}
	return m.progress.report("resources", len(m.files), len(m.files))
}

// documentImportVersion 合并OFD格式版本
// 入参: target 目标版本, source 来源版本
// 返回: string 合并版本, error 版本错误
func documentImportVersion(target, source string) (string, error) {
	if source == "" || source == target {
		return target, nil
	}
	if target == "" {
		return source, nil
	}
	if (source == "1.0" || source == "1.1") && (target == "1.0" || target == "1.1") {
		return "1.1", nil
	}
	return "", fmt.Errorf("cannot merge OFD versions %q and %q", target, source)
}

// documentImportOccupied 保留目标全部文档及版本的已声明路径
// 入参: ctx 取消上下文, reader 目标阅读器, data 根索引原文, root 根索引节点
// 返回: map[string][]byte 占用路径, error 扫描错误
func documentImportOccupied(ctx context.Context, reader *Reader, data []byte, root *editorXML) (map[string][]byte, error) {
	migration := &editorDocumentImport{ctx: ctx, source: reader, files: make(map[string]*versionEditFile),
		kinds: make(map[string]int), progress: editorProgress(nil).withContext(ctx)}
	file := &versionEditFile{reader: reader, name: "OFD.xml", data: data}
	for _, body := range root.children {
		if body.matchesOFD("DocBody") {
			if err := migration.collect(file, root, body, ""); err != nil {
				return nil, err
			}
		}
	}
	if err := migration.load(true); err != nil {
		return nil, err
	}
	occupied := make(map[string][]byte, len(migration.kinds))
	for name := range migration.kinds {
		occupied[name] = nil
	}
	return occupied, nil
}
