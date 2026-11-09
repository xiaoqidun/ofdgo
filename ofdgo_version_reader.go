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
	"archive/zip"
	"context"
	"fmt"
)

// DocumentRoot 获取当前阅读入口的包内路径，不改写OFD中的主入口
// 未显式选择时使用唯一的Current版本；没有默认版本时使用主入口，多个默认版本返回错误
// 返回: string 阅读入口, error 版本选择或描述错误
func (r *Reader) DocumentRoot() (string, error) {
	return r.DocumentRootContext(context.Background())
}

// DocumentRootContext 获取当前阅读入口，解析版本列表和文件清单时响应取消
// 取消或失败时不缓存本次选择结果，入口选择规则与DocumentRoot相同
// 入参: ctx 取消上下文
// 返回: string 阅读入口, error 版本选择、读取或取消错误
func (r *Reader) DocumentRootContext(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.documentRoot != "" {
		return r.documentRoot, nil
	}
	if r.documentIndex < 0 || r.documentIndex >= r.DocumentCount() {
		return "", fmt.Errorf("no docbody found")
	}
	body := r.OFD.DocBody[r.documentIndex]
	if r.selectedVersion != nil && *r.selectedVersion == "" || !body.versioned && r.selectedVersion == nil {
		root, err := versionFileLocation("OFD.xml", body.DocRoot)
		if err == nil {
			r.documentRoot = root
		}
		return root, err
	}
	entries, err := r.documentVersionEntriesContext(ctx)
	if err != nil {
		return "", err
	}
	var selected *DocumentVersionInfo
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		entry := &entries[i]
		if r.selectedVersion != nil && entry.ID == *r.selectedVersion || r.selectedVersion == nil && entry.Current {
			if selected != nil {
				return "", fmt.Errorf("multiple current document versions")
			}
			selected = entry
		}
	}
	if selected == nil {
		if r.selectedVersion != nil {
			return "", fmt.Errorf("document version not found: %s", *r.selectedVersion)
		}
		root, err := versionFileLocation("OFD.xml", body.DocRoot)
		if err == nil {
			r.documentRoot = root
		}
		return root, err
	}
	info, err := r.readDocumentVersionContext(ctx, selected.ID, selected.Location)
	if err != nil {
		return "", fmt.Errorf("version %s: %w", selected.ID, err)
	}
	info.Index, info.Current = selected.Index, selected.Current
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.documentRoot, r.versionInfo = info.DocRoot, &info
	return r.documentRoot, nil
}

// DocumentVersionID 获取当前阅读的版本标识，主入口返回空字符串
// 返回: string 版本标识, error 版本选择或描述错误
func (r *Reader) DocumentVersionID() (string, error) {
	if _, err := r.DocumentRoot(); err != nil {
		return "", err
	}
	if r.versionInfo == nil {
		return "", nil
	}
	return r.versionInfo.ID, nil
}

// DocumentVersion 创建指定版本的独立阅读器，保留整个包，不改变当前阅读器或共享可变缓存
// 返回值不关闭输入文件，使用期间不得关闭持有输入文件的原阅读器
// 入参: id 版本标识，空值显式选择主入口；Document可重新按Current选择默认版本
// 返回: *Reader 版本阅读器, error 选择或读取错误
func (r *Reader) DocumentVersion(id string) (*Reader, error) {
	return r.DocumentVersionContext(context.Background(), id)
}

// DocumentVersionContext 创建指定版本的独立阅读器，读取版本及关联内容时响应取消
// 返回值不关闭输入文件，使用期间不得关闭持有输入文件的原阅读器
// 入参: ctx 取消上下文, id 版本标识，空值选择主入口
// 返回: *Reader 版本阅读器, error 读取或取消错误
func (r *Reader) DocumentVersionContext(ctx context.Context, id string) (*Reader, error) {
	next := &Reader{Path: r.Path, Zip: r.Zip, files: r.files, encryption: r.encryption, documentIndex: r.documentIndex, selectedVersion: &id, pageReady: r.pageReady, pdfDestinations: r.pdfDestinations.clone()}
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.DocContext(ctx); err != nil {
		return nil, err
	}
	return next, nil
}

// documentEntryContext 解析指定文档的阅读入口，当前文档保留显式选择
// 入参: ctx 取消上下文, index 文档索引
// 返回: string 包内路径, error 选择、读取或取消错误
func (r *Reader) documentEntryContext(ctx context.Context, index int) (string, error) {
	if index == r.documentIndex {
		return r.DocumentRootContext(ctx)
	}
	view := *r
	view.documentIndex, view.documentRoot = index, ""
	view.selectedVersion, view.versionInfo = nil, nil
	return view.DocumentRootContext(ctx)
}

// versionSignatures 按OFD入口解析包级签名路径，选中版本仅使用文件清单内的签名入口
// 入参: location OFD入口中的签名路径
// 返回: string 签名路径，不属于该版本时为空
func (r *Reader) versionSignatures(location string) string {
	if location == "" {
		return ""
	}
	name := resolveResourcePath("OFD.xml", "", location)
	if r.versionInfo == nil {
		return "/" + name
	}
	for _, file := range r.versionInfo.Files {
		if file.Location == name {
			return "/" + name
		}
	}
	return ""
}

// ExtractVersion 将当前文档的指定版本提取为独立阅读器，可继续编辑或另存，不改变原包
// 仅保留版本文件清单中的文件并重建单文档入口，不保留版本列表；原签名可能因入口变化而失效
// 文件内容按需读取，使用期间不得关闭持有输入文件的原阅读器；返回值不关闭输入文件
// 入参: id 版本标识，空值选择唯一的Current版本，没有默认版本或存在歧义时返回错误
// 返回: *Reader 版本阅读器, error 选择、读取或文件缺失错误
func (r *Reader) ExtractVersion(id string) (*Reader, error) {
	return r.ExtractVersionContext(context.Background(), id)
}

// ExtractVersionContext 将指定版本提取为独立阅读器，扫描、读取和解析时响应取消
// 文件保留范围、入口选择和输入文件生命周期与ExtractVersion相同，取消或失败不改变原包
// 入参: ctx 取消上下文, id 版本标识，空值选择唯一的Current版本
// 返回: *Reader 版本阅读器, error 选择、读取或取消错误
func (r *Reader) ExtractVersionContext(ctx context.Context, id string) (*Reader, error) {
	entries, err := r.documentVersionEntriesContext(ctx)
	if err != nil {
		return nil, err
	}
	var selected *DocumentVersionInfo
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := &entries[i]
		if id != "" && entry.ID == id || id == "" && entry.Current {
			if selected != nil {
				return nil, fmt.Errorf("multiple current document versions")
			}
			selected = entry
		}
	}
	if selected == nil {
		if id == "" {
			return nil, fmt.Errorf("no current document version")
		}
		return nil, fmt.Errorf("document version not found: %s", id)
	}
	version, err := r.readDocumentVersionContext(ctx, selected.ID, selected.Location)
	if err != nil {
		return nil, fmt.Errorf("version %s: %w", selected.ID, err)
	}
	files := make(map[string]bool, len(version.Files))
	for _, file := range version.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !r.fileNames[file.Location] {
			return nil, fmt.Errorf("version %s file not found: %s", selected.ID, file.Location)
		}
		files[file.Location] = true
	}
	root, err := r.extractedVersionRoot(ctx, version, files)
	if err != nil {
		return nil, err
	}
	next := &Reader{Path: r.Path, files: map[string][]byte{"OFD.xml": root}, encryption: r.encryption}
	for name := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name != "OFD.xml" {
			if data, ok := r.files[name]; ok {
				next.files[name] = data
			}
		}
	}
	if r.Zip != nil {
		next.Zip = &zip.Reader{Comment: r.Zip.Comment}
		for _, file := range r.Zip.File {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := cleanPackagePath(file.Name)
			if name != "OFD.xml" && files[name] && !file.FileInfo().IsDir() {
				next.Zip.File = append(next.Zip.File, file)
			}
		}
	}
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.docStructureContext(ctx); err != nil {
		return nil, err
	}
	return next, nil
}

// extractedVersionRoot 重建版本副本入口，保留元数据及命名空间，移除其他文档与版本列表
// 入参: ctx 取消上下文, version 版本描述, files 版本包含的文件
// 返回: []byte 主入口XML, error 读取、结构或取消错误
func (r *Reader) extractedVersionRoot(ctx context.Context, version DocumentVersionInfo, files map[string]bool) ([]byte, error) {
	data, err := r.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	body := root.childAt("DocBody", r.documentIndex)
	if body == nil {
		return nil, fmt.Errorf("document entry not found: %d", r.documentIndex)
	}
	var patches []editorXMLPatch
	for _, child := range root.children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if child.matchesOFD("DocBody") && child != body {
			patches = append(patches, editorXMLPatch{start: child.start, end: child.end})
		}
	}
	entry := body.child("DocRoot")
	if entry != nil {
		patches = append(patches, editorXMLContent(data, entry, editorFontXMLText(version.DocRoot)))
	}
	if versions := body.child("Versions"); versions != nil {
		var replacement []byte
		if entry == nil {
			replacement = body.textXML("DocRoot", version.DocRoot)
		}
		patches = append(patches, editorXMLPatch{versions.start, versions.end, replacement})
	} else if entry == nil {
		return nil, fmt.Errorf("document entry not found: %d", r.documentIndex)
	}
	references := []*editorXML{body.child("Signatures")}
	if info := body.child("DocInfo"); info != nil {
		references = append(references, info.child("Cover"))
	}
	for _, node := range references {
		if node == nil {
			continue
		}
		name := resolveResourcePath("OFD.xml", "", editorImportText(data, node))
		if files[name] {
			patches = append(patches, editorXMLContent(data, node, editorFontXMLText("/"+name)))
		} else {
			patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
		}
	}
	data = editorPatchXML(data, patches)
	if version.Version != "" {
		root, err = parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		data, err = editorXMLAttribute(data, root, "Version", version.Version)
	}
	if err == nil {
		err = ctx.Err()
	}
	return data, err
}
