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
	"encoding/xml"
	"fmt"
)

// versionSignatureScope 保存多版本签章归属判定的文件与页面索引
type versionSignatureScope struct {
	listPath          string
	listInVersion     bool
	versionLoc        string
	versionDocRoot    string
	versionFiles      map[string]bool
	versionPages      map[string]bool
	otherVersionRoots map[string]bool
	hasExplicitSig    bool
}

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
	body := r.ofd.DocBody[r.documentIndex]
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
	next := r.deriveView(nil, r.documentIndex, &id)
	next.pdfDestinations = r.pdfDestinations.clone()
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.docContext(ctx); err != nil {
		return nil, err
	}
	next.bindEncryptionHold()
	return next, nil
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
	next := &Reader{path: r.path, sourceFile: r.sourceFile, files: map[string][]byte{"OFD.xml": root}, encryption: r.encryption, resources: r.resources}
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
	if r.zip != nil {
		var kept []*zip.File
		for _, file := range r.zip.File {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := cleanPackagePath(file.Name)
			if name != "OFD.xml" && files[name] && !file.FileInfo().IsDir() {
				kept = append(kept, file)
			}
		}
		next.zip = &zip.Reader{Comment: r.zip.Comment, File: kept}
		next.archiveID = nextReaderArchiveID.Add(1)
		next.maxIDCache = &archiveMaxIDCache{}
	}
	if src := r.encryptedSource(); src != nil {
		keep := make(map[string]bool, len(files))
		for name := range files {
			if name != "OFD.xml" {
				keep[name] = true
			}
		}
		next.setEncryptedSource(src.filter(keep))
	}
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.docStructureContext(ctx); err != nil {
		return nil, err
	}
	next.bindEncryptionHold()
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

// versionSignatures 按OFD入口解析包级签名路径，选中版本时结合文件清单与签章归属判定
// 入参: location OFD入口中的签名路径
// 返回: string 签名路径，不属于该版本时为空
func (r *Reader) versionSignatures(location string) string {
	return r.versionSignaturesContext(context.Background(), location)
}

// versionSignaturesContext 按OFD入口解析包级签名路径并在读取归属信息时响应取消
// 入参: ctx 取消上下文, location OFD入口中的签名路径
// 返回: string 签名路径，不属于该版本时为空
func (r *Reader) versionSignaturesContext(ctx context.Context, location string) string {
	if location == "" {
		return ""
	}
	name := resolveResourcePath("OFD.xml", "", location)
	if !validPackagePath(name) {
		return ""
	}
	if r.versionInfo == nil {
		return "/" + name
	}
	for _, file := range r.versionInfo.Files {
		if cleanPackagePath(file.Location) == name {
			return "/" + name
		}
	}
	data, err := r.readFileViewContext(ctx, name)
	if err != nil {
		return ""
	}
	var list Signatures
	if err := xml.Unmarshal(data, &list); err != nil || len(list.List) == 0 {
		return ""
	}
	scope, err := r.buildVersionSignatureScope(ctx, name, list)
	if err != nil {
		return ""
	}
	for _, sigRef := range list.List {
		if r.versionSignatureMatches(ctx, scope, sigRef) {
			return "/" + name
		}
	}
	return ""
}

// buildVersionSignatureScope 构建当前版本的签章归属判定上下文
// 入参: ctx 取消上下文, listPath 规范化签名列表路径, list 签名列表
// 返回: *versionSignatureScope 归属上下文, error 读取或取消错误
func (r *Reader) buildVersionSignatureScope(ctx context.Context, listPath string, list Signatures) (*versionSignatureScope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cleanList := cleanPackagePath(listPath)
	versionLoc := cleanPackagePath(r.versionInfo.Location)
	versionDocRoot := cleanPackagePath(r.versionInfo.DocRoot)
	scope := &versionSignatureScope{
		listPath:          cleanList,
		versionLoc:        versionLoc,
		versionDocRoot:    versionDocRoot,
		versionFiles:      make(map[string]bool, len(r.versionInfo.Files)+2),
		versionPages:      make(map[string]bool),
		otherVersionRoots: make(map[string]bool),
	}
	if validPackagePath(versionLoc) {
		scope.versionFiles[versionLoc] = true
	}
	if validPackagePath(versionDocRoot) {
		scope.versionFiles[versionDocRoot] = true
	}
	for _, file := range r.versionInfo.Files {
		clean := cleanPackagePath(file.Location)
		if !validPackagePath(clean) {
			continue
		}
		scope.versionFiles[clean] = true
		if clean == cleanList {
			scope.listInVersion = true
		}
	}
	for _, sigRef := range list.List {
		sigPath := cleanPackagePath(signatureRefPath(cleanList, sigRef.BaseLoc))
		if validPackagePath(sigPath) && scope.versionFiles[sigPath] {
			scope.hasExplicitSig = true
			break
		}
	}
	if r.doc != nil && cleanPackagePath(r.documentRoot) == versionDocRoot {
		for _, page := range r.doc.Pages.Page {
			if id := editorResourceID(page.ID); id != "" {
				scope.versionPages[id] = true
			}
		}
	} else if validPackagePath(versionDocRoot) {
		if data, err := r.readFileViewContext(ctx, versionDocRoot); err == nil {
			var doc documentPageList
			if xml.Unmarshal(data, &doc) == nil {
				for _, page := range doc.Pages.Page {
					if id := editorResourceID(page.ID); id != "" {
						scope.versionPages[id] = true
					}
				}
			}
		}
	}
	if versions, err := r.DocumentVersionsContext(ctx); err == nil {
		for _, v := range versions {
			if v.ID == r.versionInfo.ID {
				continue
			}
			if loc := cleanPackagePath(v.Location); validPackagePath(loc) && loc != versionLoc {
				scope.otherVersionRoots[loc] = true
			}
			if dr := cleanPackagePath(v.DocRoot); validPackagePath(dr) && dr != versionDocRoot {
				scope.otherVersionRoots[dr] = true
			}
		}
	}
	if r.ofd != nil && r.documentIndex >= 0 && r.documentIndex < len(r.ofd.DocBody) {
		if mainDoc := r.ofd.DocBody[r.documentIndex].DocRoot; mainDoc != "" {
			if dr := cleanPackagePath(resolveResourcePath("OFD.xml", "", mainDoc)); validPackagePath(dr) && dr != versionDocRoot {
				scope.otherVersionRoots[dr] = true
			}
		}
	}
	return scope, ctx.Err()
}

// versionSignatureMatches 判断单条签名是否归属当前选中版本
// 入参: ctx 取消上下文, scope 归属判定上下文, sigRef 签名条目
// 返回: bool 是否归属当前版本
func (r *Reader) versionSignatureMatches(ctx context.Context, scope *versionSignatureScope, sigRef Signature) bool {
	if scope == nil {
		return true
	}
	sigPath := cleanPackagePath(signatureRefPath(scope.listPath, sigRef.BaseLoc))
	if !validPackagePath(sigPath) {
		return false
	}
	if scope.versionFiles[sigPath] {
		return true
	}
	data, err := r.readFileViewContext(ctx, sigPath)
	if err != nil {
		return scope.listInVersion && !scope.hasExplicitSig
	}
	sigFile, err := parseSignatureFile(data)
	if err != nil {
		return scope.listInVersion && !scope.hasExplicitSig
	}
	ownValue := ""
	if sigFile.SignedValue != "" {
		if p := cleanPackagePath(signatureRefPath(sigPath, sigFile.SignedValue)); validPackagePath(p) {
			ownValue = p
		}
	}
	ownSeal := ""
	if sigFile.SignedInfo.Seal.BaseLoc != "" {
		if p := cleanPackagePath(signatureRefPath(sigPath, sigFile.SignedInfo.Seal.BaseLoc)); validPackagePath(p) {
			ownSeal = p
		}
	}
	hitsCurrentRoot := false
	hitsOtherRoot := false
	hitsVersionFile := false
	nonSharedRefs := 0
	for _, ref := range sigFile.SignedInfo.References.Reference {
		refPath := cleanPackagePath(signatureRefPath(sigPath, ref.FileRef))
		if !validPackagePath(refPath) || refPath == "OFD.xml" || refPath == scope.listPath || refPath == sigPath || refPath == ownValue || refPath == ownSeal {
			continue
		}
		nonSharedRefs++
		if refPath == scope.versionLoc || refPath == scope.versionDocRoot {
			hitsCurrentRoot = true
		}
		if scope.otherVersionRoots[refPath] {
			hitsOtherRoot = true
		}
		if scope.versionFiles[refPath] {
			hitsVersionFile = true
		}
	}
	if hitsCurrentRoot {
		return true
	}
	if hitsOtherRoot {
		return false
	}
	if scope.listInVersion && !scope.hasExplicitSig {
		return true
	}
	if hitsVersionFile {
		return true
	}
	if nonSharedRefs == 0 {
		for _, stamp := range sigFile.SignedInfo.StampAnnot {
			if scope.versionPages[editorResourceID(stamp.PageRef)] {
				return true
			}
		}
	}
	return false
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
		if validPackagePath(name) && files[name] {
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
